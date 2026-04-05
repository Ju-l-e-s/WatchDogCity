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
		"analysis": "<h4>Contexte</h4><p>Election standard.</p>",
		"key_points": ["Point 1", "Point 2"],
		"vote": {"pour": 32, "contre": 5, "abstention": 2},
		"disagreements": "L'opposition a contesté la procédure."
	}`

	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "Élection du Maire", result.Title)
	assert.Equal(t, "Le conseil a élu son maire.", result.Summary)
	assert.Equal(t, "Éducation", result.TopicTag)
	assert.Equal(t, "<h4>Contexte</h4><p>Election standard.</p>", result.Analysis)
	assert.Equal(t, []string{"Point 1", "Point 2"}, result.KeyPoints)
	assert.Equal(t, 32, result.Vote.Pour)
	assert.Equal(t, 5, result.Vote.Contre)
	assert.Equal(t, 2, result.Vote.Abstention)
	assert.Equal(t, "L'opposition a contesté la procédure.", result.Disagreements)
}

func TestParseGeminiResponseRobust(t *testing.T) {
	// Test markdown wrapping and array unwrapping
	raw := " ```json\n[\n{\n\"title\": \"Wrapped\"\n}\n]\n``` "
	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "Wrapped", result.Title)
}

func TestParseGeminiResponseNoDisagreements(t *testing.T) {
	raw := `{
		"title": "Budget",
		"summary": "Vote unanime du budget.",
		"topic_tag": "Budget",
		"vote": {"pour": 39, "contre": 0, "abstention": 0},
		"disagreements": ""
	}`

	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "Budget", result.TopicTag)
	assert.Equal(t, "", result.Disagreements)
}

func TestParseGeminiResponseInvalidJSON(t *testing.T) {
	_, err := parseGeminiResponse("not json")
	assert.Error(t, err)
}
