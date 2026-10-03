package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFactCheckResponse(t *testing.T) {
	result, err := parseFactCheckResponse(`{"verdict":"FAIL","findings":[{"field":"analysis_data.impacts","claim":"980 m² d'espaces paysagers","reason":"Le nombre est la superficie totale de deux parcelles cédées","pdf_evidence":"superficie totale de 980 m²"}]}`)
	require.NoError(t, err)
	assert.Equal(t, "FAIL", result.Verdict)
	require.Len(t, result.Findings, 1)
	assert.Equal(t, "analysis_data.impacts", result.Findings[0].Field)
	assert.Contains(t, acceptFactCheck(result).Error(), "superficie totale")
}

// Opt-in integration check against an official PDF. It exercises the real
// model boundary without writing DynamoDB or invoking the newsletter.
func TestLiveFactCheckRejectsWrongTiming(t *testing.T) {
	if os.Getenv("WATCHDOG_LIVE_FACTCHECK") != "1" {
		t.Skip("set WATCHDOG_LIVE_FACTCHECK=1 for a real Gemini/PDF check")
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Fatal("GEMINI_API_KEY is required for live fact check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	pdf, err := downloadPDF(ctx, "https://www.mairie-begles.fr/app/uploads/2026/10/D23-2026_113.pdf")
	require.NoError(t, err)
	result := &GeminiResult{
		Title:   "Subvention pour des plantations",
		Summary: "La ville a déjà reçu 23 792 € et 495 arbres seront plantés en 2027.",
	}
	zero, thirtyNine := 0, 39
	result.Vote.HasVote = true
	result.Vote.Pour = &thirtyNine
	result.Vote.Contre = &zero
	result.Vote.Abstention = &zero
	err = verifyFactsWithGemini(ctx, apiKey, pdf, result)
	require.ErrorContains(t, err, "fact-check rejected")
	result.Summary = "Le Conseil municipal approuve une convention de financement pour des travaux de plantation réalisés en 2024-2025 ; la subvention est plafonnée à 23 792 € et son versement dépend de justificatifs."
	err = verifyFactsWithGemini(ctx, apiKey, pdf, result)
	require.NoError(t, err)
}

func TestParseFactCheckResponseRejectsMalformedOrIncompleteJSON(t *testing.T) {
	for _, raw := range []string{
		`{"verdict":"PASS"}`,
		`{"verdict":"PASS","findings":null}`,
		`{"verdict":"PASS","findings":[],"unexpected":true}`,
		`{"verdict":"PASS","findings":[]} {"verdict":"PASS","findings":[]}`,
		`{"verdict":"PASS","findings":[]} trailing`,
	} {
		_, err := parseFactCheckResponse(raw)
		assert.Error(t, err, raw)
	}
}

func TestAcceptFactCheckFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		result factCheckResult
		want   string
	}{
		{"pass", factCheckResult{Verdict: "PASS", Findings: []factCheckFinding{}}, ""},
		{"contradiction", factCheckResult{Verdict: "FAIL", Findings: []factCheckFinding{{Field: "summary", Claim: "495 arbres seront plantés", Reason: "Les travaux sont déjà réalisés", PDFEvidence: "495 arbres est planté"}}}, "summary"},
		{"unsupported claim", factCheckResult{Verdict: "FAIL", Findings: []factCheckFinding{{Field: "summary", Claim: "subvention reçue", Reason: "Le versement dépend de justificatifs"}}}, "summary"},
		{"pass with finding", factCheckResult{Verdict: "PASS", Findings: []factCheckFinding{{Field: "summary", Claim: "x", Reason: "y"}}}, "inconsistent"},
		{"fail without finding", factCheckResult{Verdict: "FAIL", Findings: []factCheckFinding{}}, "without reasons"},
		{"fail with incomplete finding", factCheckResult{Verdict: "FAIL", Findings: []factCheckFinding{{Field: "summary"}}}, "incomplete"},
		{"unknown", factCheckResult{Verdict: "MAYBE", Findings: []factCheckFinding{}}, "unknown verdict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := acceptFactCheck(tt.result)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tt.want), err.Error())
		})
	}
}

func TestFactCheckPayloadIncludesSourceFacingFields(t *testing.T) {
	result := &GeminiResult{Title: "Piste cyclable", Summary: "980 m² de terrains cédés", BudgetImpact: 0}
	result.AnalysisData.Impacts = new(string)
	*result.AnalysisData.Impacts = "Des aménagements paysagers sont prévus."
	payload, err := factCheckPayload(result)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"analysis_data"`)
	assert.Contains(t, string(payload), `"vote"`)
	assert.Contains(t, string(payload), `"budget_impact"`)
	assert.NotContains(t, string(payload), `"input_tokens"`)
}

func TestFactCheckModelDefaultAndOverride(t *testing.T) {
	t.Setenv("GEMINI_FACT_CHECK_MODEL", "")
	assert.Equal(t, "gemini-2.5-pro", factCheckModel())
	t.Setenv("GEMINI_FACT_CHECK_MODEL", " gemini-2.5-flash ")
	assert.Equal(t, "gemini-2.5-flash", factCheckModel())
}
