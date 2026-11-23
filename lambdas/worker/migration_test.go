package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/watchdog/shared"
)

func requireAPCPReference(t *testing.T, result *GeminiResult) {
	t.Helper()
	require.Equal(t, "AUCUN", result.BudgetType)
	require.Zero(t, result.BudgetImpact)
	for _, expected := range []struct {
		scope, label, period string
		amount               int64
	}{
		{"CP", "prévert", "2026", 200_000},
		{"CP", "complexe sportif", "2026", 1_000_000},
		{"AP", "prévert", "", 500_000},
	} {
		found := false
		for _, got := range result.BudgetChanges {
			if got.Scope == expected.scope && strings.Contains(strings.ToLower(got.Label), expected.label) &&
				(expected.period == "" || strings.Contains(got.Period, expected.period)) &&
				got.Amount == expected.amount && got.Direction == "BAISSE" {
				found = true
			}
		}
		require.True(t, found, "missing essential budget change %+v", expected)
	}
}

func TestMigrationPDFAPCPReferenceOffline(t *testing.T) {
	raw := `{"title":"Ajustement budgétaire","topic_tag":"Budget","budget_impact":0,"budget_type":"AUCUN","climate_impact":"neutre","budget_changes":[{"scope":"CP","direction":"BAISSE","label":"Prévert","amount":200000,"period":"2026"},{"scope":"CP","direction":"BAISSE","label":"Complexe sportif polyvalent","amount":1000000,"period":"2026"},{"scope":"AP","direction":"BAISSE","label":"Prévert","amount":500000}]}`
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	require.NoError(t, validateGeminiResult(result))
	requireAPCPReference(t, result)
}

func TestMigrationLiveFinancialPDF(t *testing.T) {
	if os.Getenv("WATCHDOG_GEMINI_EVAL") != "1" {
		t.Skip("explicit opt-in required for paid Gemini evaluation")
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	require.NotEmpty(t, apiKey)
	path := os.Getenv("WATCHDOG_EVAL_PDF")
	wantHash := os.Getenv("WATCHDOG_EVAL_PDF_SHA256")
	which := os.Getenv("WATCHDOG_EVAL_PDF_CASE")
	require.Contains(t, []string{"D10-2026_128", "D23-2026_113"}, which)
	require.NotEmpty(t, path)
	pdf, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, wantHash, fmt.Sprintf("%x", sha256.Sum256(pdf)))
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	extractCtx, stopExtract := context.WithTimeout(ctx, 120*time.Second)
	result, err := analyzeWithGemini(extractCtx, apiKey, pdf)
	stopExtract()
	require.NoError(t, err)
	switch which {
	case "D10-2026_128":
		requireAPCPReference(t, result)
	case "D23-2026_113":
		require.EqualValues(t, 23_792, result.BudgetImpact)
		require.Equal(t, "RECETTE", result.BudgetType)
		require.True(t, result.Vote.HasVote)
		require.NotNil(t, result.Vote.Pour)
		require.Equal(t, 39, *result.Vote.Pour)
		for _, count := range []*int{result.Vote.Contre, result.Vote.Abstention} {
			if count != nil {
				require.Zero(t, *count)
			}
		}
		require.NotNil(t, result.AnalysisData.Decision)
		copy := strings.ToLower(result.Summary + " " + result.BudgetNote + " " + *result.AnalysisData.Decision)
		require.Regexp(t, `plafon|maxim|jusqu'à`, copy, "missing capped amount")
		for _, fact := range []string{"2024", "2025", "justificati", "convention", "réalis"} {
			require.Contains(t, copy, fact, "missing essential condition, decision or date")
		}
	}
	verifyCtx, stopVerify := context.WithTimeout(ctx, 90*time.Second)
	defer stopVerify()
	require.NoError(t, verifyFactsWithGemini(verifyCtx, apiKey, pdf, result))
	payload, err := factCheckPayload(result)
	require.NoError(t, err)
	t.Logf("Human review required: %s", payload)
}

// The Worker explicitly constructs its own HTTP clients. Their default
// transport is replaced here so both production schemas reach the Go SDK.
type workerMigrationTransport func(*http.Request) (*http.Response, error)

func (f workerMigrationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWorkerMigrationSerializesBothProductionSchemas(t *testing.T) {
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "https://fixture.invalid")
	t.Setenv("GEMINI_MODEL", "gemini-3.5-flash-lite")
	t.Setenv("GEMINI_FACT_CHECK_MODEL", "gemini-3.8-flash")
	savedTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = savedTransport })
	calls := 0
	http.DefaultTransport = workerMigrationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			Contents []struct {
				Parts []struct {
					InlineData *struct {
						MIMEType string
						Data     string
					}
				}
			}
			GenerationConfig struct {
				Temperature      *float32
				ResponseMIMEType string
				MaxOutputTokens  int
				ThinkingConfig   struct{ ThinkingLevel string }
				ResponseSchema   struct {
					Properties map[string]json.RawMessage
				}
			}
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if req.URL.Host != "fixture.invalid" || len(body.Contents) != 1 ||
			len(body.Contents[0].Parts) == 0 || body.Contents[0].Parts[0].InlineData == nil ||
			body.Contents[0].Parts[0].InlineData.MIMEType != "application/pdf" ||
			body.Contents[0].Parts[0].InlineData.Data == "" {
			t.Errorf("missing native PDF input to local fixture")
		}
		cfg := body.GenerationConfig
		if cfg.Temperature != nil || cfg.ResponseMIMEType != "application/json" {
			t.Errorf("unexpected Gemini parameters %+v", cfg)
		}
		var result string
		if calls == 1 {
			if !strings.Contains(req.URL.Path, "gemini-3.5-flash-lite") || cfg.ThinkingConfig.ThinkingLevel != "MINIMAL" ||
				cfg.MaxOutputTokens != 8192 || cfg.ResponseSchema.Properties["budget_changes"] == nil || cfg.ResponseSchema.Properties["critical_facts"] == nil {
				t.Errorf("unexpected extraction request %s", req.URL.Path)
			}
			result = `{"title":"Convention","summary":"Le conseil approuve une convention.","analysis_data":{"decision":"Le conseil approuve une convention.","impacts":"Néant"},"topic_tag":"Administration","budget_impact":0,"budget_type":"AUCUN","climate_impact":"neutre","budget_note":"","budget_changes":[],"is_substantial":false,"key_points":[],"vote":{"has_vote":false},"critical_facts":[{"id":"decision","kind":"DECISION","audience":"BRIEF","label":"Le conseil approuve une convention.","status":"ADOPTE","evidence":{"page":1,"excerpt":"Approuve la convention"}}]}`
		} else {
			if !strings.Contains(req.URL.Path, "gemini-3.8-flash") || cfg.ThinkingConfig.ThinkingLevel != "MEDIUM" ||
				cfg.MaxOutputTokens != 4096 || cfg.ResponseSchema.Properties["findings"] == nil || cfg.ResponseSchema.Properties["funding_coverage"] == nil || len(body.Contents[0].Parts) != 3 {
				t.Errorf("unexpected verification request %s", req.URL.Path)
			}
			result = fundingFixtureResponse(t, []shared.SourceFact{{ID: "decision", Evidence: shared.PDFPassage{Page: 1}}}, false, []fundingCoverageCheck{})
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":%q}]}}]}`, result)))}, nil
	})
	ctx := context.Background()
	pdf := []byte("%PDF-fixture")
	result, err := analyzeWithGemini(ctx, "fixture-key", pdf)
	require.NoError(t, err)
	require.Equal(t, shared.ProofStructural, result.SourceFacts.ProofStatus)
	require.NoError(t, verifyFactsWithGemini(ctx, "fixture-key", pdf, result))
	require.True(t, result.SourceFacts.Reviewed())
	require.ErrorContains(t, verifyFactsWithGemini(ctx, "fixture-key", []byte("%PDF-modifié"), result), "fingerprint")
	require.Equal(t, 2, calls)
}

func fundingFixtureResponse(t *testing.T, facts []shared.SourceFact, applies bool, checks []fundingCoverageCheck) string {
	t.Helper()
	var report factCheckResult
	require.NoError(t, json.Unmarshal([]byte(factCheckFixtureResponse(facts)), &report))
	report.Funding = &fundingCoverage{Applies: &applies, Checks: checks}
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	return string(raw)
}
