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
	// Gemini sometimes returns floats like 2028913.40 instead of integer.
	// The regex must strip the decimal part so int64 unmarshal succeeds.
	raw := `{"title":"T","summary":"S","budget_impact": 2028913.40}`
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, int64(2028913), result.BudgetImpact)
}

func TestParseGeminiResponse_BudgetBreakdownAmountFloat(t *testing.T) {
	// The same fix must apply to "amount" fields inside budget_breakdown items.
	// Without the fix, unmarshaling a float into int64 would fail silently or error.
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
