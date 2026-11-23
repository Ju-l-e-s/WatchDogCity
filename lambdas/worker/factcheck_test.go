package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/watchdog/shared"
	"google.golang.org/genai"
)

func factCheckResponse(text string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		FinishReason: genai.FinishReasonStop,
		Content:      &genai.Content{Parts: []*genai.Part{{Text: text}}},
	}}}
}

func factCheckFixtureResponse(facts []shared.SourceFact, findings ...factCheckFinding) string {
	result := factCheckResult{CheckedFields: factCheckFields, Coverage: []factCoverage{}, Findings: []factCheckFinding{}}
	result.Findings = append(result.Findings, findings...)
	for _, f := range facts {
		result.Coverage = append(result.Coverage, factCoverage{FactID: f.ID, Status: "CONFIRMED", Page: f.Evidence.Page})
	}
	raw, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestParseFactCheckResponse(t *testing.T) {
	result, err := parseFactCheckResponse(factCheckFixtureResponse(nil, factCheckFinding{
		Kind: "CONTRADICTION", Resolution: "BLOCK", Field: "analysis_data.impacts", Claim: "980 m² d'espaces paysagers",
		Reason: "Le nombre est la superficie totale de deux parcelles cédées", Evidence: &shared.PDFPassage{Page: 2, Excerpt: "superficie totale de 980 m²"},
	}))
	require.NoError(t, err)
	require.Len(t, result.Findings, 1)
	assert.Equal(t, "analysis_data.impacts", result.Findings[0].Field)
	assert.ErrorIs(t, acceptFactCheck(result, nil), errFactCheckRejected)
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
	result, err := analyzeWithGemini(ctx, apiKey, pdf)
	require.NoError(t, err)
	require.NoError(t, verifyFactsWithGemini(ctx, apiKey, pdf, result))
	result.BudgetNote = "La ville a déjà reçu 23 792 €."
	err = verifyFactsWithGemini(ctx, apiKey, pdf, result)
	require.ErrorIs(t, err, errFactCheckRejected)
}

// Opt-in end-to-end regression check for the PDFs that were stranded in the
// November council. It runs extraction and the independent PDF check only; it
// does not write AWS data or invoke publishing/newsletter functions.
func TestLiveRecentCouncilPDFsPassFactCheck(t *testing.T) {
	if os.Getenv("WATCHDOG_LIVE_RECENT_COUNCIL") != "1" {
		t.Skip("set WATCHDOG_LIVE_RECENT_COUNCIL=1 for real Gemini/PDF checks")
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Fatal("GEMINI_API_KEY is required for live fact check")
	}
	pdfs := []string{
		"https://www.mairie-begles.fr/app/uploads/2026/11/D06-2026_124.pdf",
		"https://www.mairie-begles.fr/app/uploads/2026/11/D07-2026_125.pdf",
		"https://www.mairie-begles.fr/app/uploads/2026/11/D09-2026_127.pdf",
	}
	for _, pdfURL := range pdfs {
		t.Run(pdfURL, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			pdf, err := downloadPDF(ctx, pdfURL)
			require.NoError(t, err)
			result, err := analyzeWithGemini(ctx, apiKey, pdf)
			require.NoError(t, err)
			require.NoError(t, verifyFactsWithGemini(ctx, apiKey, pdf, result))
		})
	}
}

func TestParseFactCheckResponseRejectsMalformedOrIncompleteJSON(t *testing.T) {
	valid := factCheckFixtureResponse(nil)
	for _, raw := range []string{
		`{"verdict":"PASS","findings":[]}`,
		strings.Replace(valid, `"coverage":[]`, `"coverage":null`, 1),
		strings.Replace(valid, `"findings":[]`, `"findings":[],"unexpected":true`, 1),
		strings.Replace(valid, `"budget_note",`, "", 1),
		valid + valid,
		valid + " trailing",
	} {
		_, err := parseFactCheckResponse(raw)
		assert.Error(t, err, raw)
	}
}

func TestSubstantiveFactCheckRejectionClassification(t *testing.T) {
	assert.True(t, isSubstantiveFactCheckRejection(fmt.Errorf("%w: votes", errFactCheckRejected)))
	assert.True(t, isSubstantiveFactCheckRejection(errFactCheckReview))
	assert.False(t, isSubstantiveFactCheckRejection(nil))
	assert.False(t, isSubstantiveFactCheckRejection(context.DeadlineExceeded))
	assert.False(t, isSubstantiveFactCheckRejection(parseFactCheckErrorForTest()))
}

func parseFactCheckErrorForTest() error {
	_, err := parseFactCheckResponse(`{`)
	return err
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
	assert.Contains(t, string(payload), `"budget_changes"`)
	assert.NotContains(t, string(payload), `"input_tokens"`)
}

func TestCallFactCheckWithResponseRetry(t *testing.T) {
	t.Run("retries malformed structured output once", func(t *testing.T) {
		calls := 0
		resp, raw, err := callFactCheckWithResponseRetry(context.Background(), nil, parseFactCheckResponse, func(context.Context) (*genai.GenerateContentResponse, error) {
			calls++
			if calls == 1 {
				return factCheckResponse(`{"verdict":`), nil
			}
			return factCheckResponse(factCheckFixtureResponse(nil)), nil
		})
		require.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Contains(t, raw, `"coverage":[]`)
		assert.Equal(t, 2, calls)
	})

	t.Run("does not retry a valid document rejection", func(t *testing.T) {
		calls := 0
		_, raw, err := callFactCheckWithResponseRetry(context.Background(), nil, parseFactCheckResponse, func(context.Context) (*genai.GenerateContentResponse, error) {
			calls++
			return factCheckResponse(factCheckFixtureResponse(nil, factCheckFinding{
				Kind: "UNSUPPORTED", Resolution: "BLOCK", Field: "budget_note", Claim: "perçue", Reason: "Versement non établi",
			})), nil
		})
		require.NoError(t, err)
		assert.Contains(t, raw, `"UNSUPPORTED"`)
		assert.Equal(t, 1, calls)
	})

	t.Run("fails closed after repeated empty responses", func(t *testing.T) {
		calls := 0
		_, _, err := callFactCheckWithResponseRetry(context.Background(), nil, parseFactCheckResponse, func(context.Context) (*genai.GenerateContentResponse, error) {
			calls++
			return &genai.GenerateContentResponse{}, nil
		})
		require.ErrorContains(t, err, "no candidate")
		assert.Equal(t, 2, calls)
	})
}

func TestExtractionAndFactCheckPromptsAlignOnVoteAndBudgetSemantics(t *testing.T) {
	assert.Contains(t, deliberationPrompt, "voix contre ou des abstentions")
	assert.Contains(t, deliberationPrompt, "ni voix contre ni abstention")
	assert.Contains(t, factCheckPrompt, "indicateur historique")
	assert.Contains(t, deliberationPrompt, "Une dépense ou une recette autorisée n'est pas à elle seule un montant chiffré")
	assert.Contains(t, factCheckPrompt, "seules abstentions")
}

func TestFactCheckModelDefaultAndOverride(t *testing.T) {
	t.Setenv("GEMINI_FACT_CHECK_MODEL", "")
	assert.Equal(t, "gemini-3.8-flash", factCheckModel())
	t.Setenv("GEMINI_FACT_CHECK_MODEL", " gemini-2.5-flash ")
	assert.Equal(t, "gemini-2.5-flash", factCheckModel())
}
