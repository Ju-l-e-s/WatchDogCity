package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGeminiResponse(t *testing.T) {
	raw := `{
		"title": "Élection du Maire",
		"summary": "Le conseil a élu son maire.",
		"topic_tag": "Éducation",
		"analysis_data": {
			"contexte": "Election standard.",
			"decision": "Validé.",
			"impacts": "Aucun.",
			"points_debattus": null
		},
		"key_points": ["Point 1", "Point 2"],
		"vote": {"has_vote": true, "pour": 32, "contre": 5, "abstention": 2},
		"disagreements": "L'opposition a contesté la procédure."
	}`

	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "Élection du Maire", result.Title)
	assert.Equal(t, "Le conseil a élu son maire.", result.Summary)
	assert.Equal(t, "Éducation", result.TopicTag)
	assert.Equal(t, "Election standard.", *result.AnalysisData.Contexte)
	assert.Equal(t, "Validé.", *result.AnalysisData.Decision)
	assert.Equal(t, []string{"Point 1", "Point 2"}, result.KeyPoints)
	assert.Equal(t, 32, *result.Vote.Pour)
	assert.Equal(t, 5, *result.Vote.Contre)
	assert.Equal(t, 2, *result.Vote.Abstention)
	assert.Equal(t, "L'opposition a contesté la procédure.", *result.Disagreements)
}

func TestParseGeminiResponseRobust(t *testing.T) {
	raw := " ```json\n{\n\"title\": \"Wrapped\"\n}\n``` "
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "Wrapped", result.Title)
}

func TestParseGeminiResponseNoDisagreements(t *testing.T) {
	raw := `{
		"title": "Budget",
		"summary": "Vote unanime du budget.",
		"topic_tag": "Budget",
		"vote": {"has_vote": true, "pour": 39, "contre": 0, "abstention": 0},
		"disagreements": null
	}`

	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "Budget", result.TopicTag)
	assert.Nil(t, result.Disagreements)
}

func TestParseGeminiResponseInvalidJSON(t *testing.T) {
	_, err := parseGeminiResponse("not json")
	assert.Error(t, err)
}

// --- Float sanitization regression (obs 299) ---

func TestParseGeminiResponse_BudgetImpactFloat(t *testing.T) {
	raw := `{"title":"T","summary":"S","budget_impact": 2028913.40}`
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, int64(2028913), result.BudgetImpact)
}

func TestParseGeminiResponse_BudgetBreakdownAmountFloat(t *testing.T) {
	raw := `{
		"title": "Budget Primitif",
		"summary": "Vote du budget.",
		"budget_impact": 0,
		"budget_breakdown": [
			{"topic_tag": "Sport", "label": "Subventions sportives", "amount": 407950.00},
			{"topic_tag": "Social", "label": "CCAS", "amount": 150000.50}
		]
	}`
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	require.Len(t, result.BudgetBreakdown, 2)
	assert.Equal(t, int64(407950), result.BudgetBreakdown[0].Amount)
	assert.Equal(t, int64(150000), result.BudgetBreakdown[1].Amount)
}

func TestParseGeminiResponse_BudgetImpactZeroFloat(t *testing.T) {
	raw := `{"title":"T","summary":"S","budget_impact": 0.00}`
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, int64(0), result.BudgetImpact)
}

// --- Defensive coercion: string "null" → nil pointer ---

func TestParseGeminiResponse_DisagreementsLiteralNullString(t *testing.T) {
	raw := `{"title":"T","summary":"S","disagreements":"null"}`
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Nil(t, result.Disagreements)
}

func TestParseGeminiResponse_DisagreementsEmptyString(t *testing.T) {
	raw := `{"title":"T","summary":"S","disagreements":""}`
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Nil(t, result.Disagreements)
}

// --- Enum validation (defense in depth on top of API-level ResponseSchema) ---

func validResultFixture() *GeminiResult {
	r := &GeminiResult{
		Title:         "T",
		Summary:       "S",
		TopicTag:      "Budget",
		IsSubstantial: true,
		BudgetImpact:  10000,
		BudgetType:    "DÉPENSE",
		ClimateImpact: "neutre",
		KeyPoints:     []string{"k"},
	}
	r.Vote.HasVote = false
	return r
}

func TestValidateGeminiResult_Valid(t *testing.T) {
	assert.NoError(t, validateGeminiResult(validResultFixture()))
}

func TestValidateGeminiResult_InvalidTopicTag(t *testing.T) {
	r := validResultFixture()
	r.TopicTag = "Sport " // trailing space — exact match required
	err := validateGeminiResult(r)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "topic_tag")
}

func TestValidateGeminiResult_InvalidBudgetType_UnaccentedDepense(t *testing.T) {
	r := validResultFixture()
	r.BudgetType = "DEPENSE" // missing accent — must fail
	err := validateGeminiResult(r)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "budget_type")
}

func TestValidateGeminiResult_InvalidClimateImpact(t *testing.T) {
	r := validResultFixture()
	r.ClimateImpact = "positive" // English — must fail
	err := validateGeminiResult(r)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "climate_impact")
}

func TestValidateGeminiResult_AucunWithImpact(t *testing.T) {
	r := validResultFixture()
	r.BudgetType = "AUCUN"
	r.BudgetImpact = 5000 // contradicts AUCUN
	err := validateGeminiResult(r)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AUCUN")
}

func TestValidateGeminiResult_BreakdownInvalidTopic(t *testing.T) {
	r := validResultFixture()
	r.BudgetImpact = 1000
	r.BudgetBreakdown = []BudgetBreakdownItem{
		{TopicTag: "NotARealTopic", Label: "x", Amount: 1000},
	}
	err := validateGeminiResult(r)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "breakdown")
}
