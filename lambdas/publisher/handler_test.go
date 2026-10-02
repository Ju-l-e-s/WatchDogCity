package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }

// --- JSON schema contract ---
// Verifies that the field names in the JSON output match what the frontend expects.
// A struct tag rename would silently break the frontend — this test catches it.
func TestDataJSONFieldNames(t *testing.T) {
	councils := []CouncilRecord{{
		CouncilID: "conseil_municipal#2026-03-28",
		Category:  "conseil_municipal",
		Date:      "2026-03-28",
		Title:     "Test",
	}}
	pour, contre, abs := 32, 5, 2
	delibs := map[string][]DeliberationRecord{
		"conseil_municipal#2026-03-28": {{
			ID:             "D01.pdf",
			CouncilID:      "conseil_municipal#2026-03-28",
			Title:          "Titre",
			TopicTag:       "Budget",
			BudgetImpact:   100000,
			VotePour:       &pour,
			VoteContre:     &contre,
			VoteAbstention: &abs,
			BudgetBreakdown: []BudgetBreakdownItem{
				{TopicTag: "Sport", Label: "Subventions Sport", Amount: 50000},
			},
		}},
	}

	data, err := buildDataJSON(context.Background(), nil, councils, delibs)
	require.NoError(t, err)

	b, err := json.Marshal(data)
	require.NoError(t, err)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &raw))

	// Top-level fields
	assert.Contains(t, raw, "generated_at")
	assert.Contains(t, raw, "councils")

	councilsArr := raw["councils"].([]interface{})
	require.Len(t, councilsArr, 1)
	c := councilsArr[0].(map[string]interface{})
	assert.Contains(t, c, "id")
	assert.Contains(t, c, "category")
	assert.Contains(t, c, "date")
	assert.Contains(t, c, "deliberations")
	assert.Contains(t, c, "analysis")

	delib := c["deliberations"].([]interface{})[0].(map[string]interface{})
	assert.Contains(t, delib, "id")
	assert.Contains(t, delib, "title")
	assert.Contains(t, delib, "topic_tag")
	assert.Contains(t, delib, "budget_impact")
	assert.Contains(t, delib, "budget_breakdown")
	assert.Contains(t, delib, "vote")

	vote := delib["vote"].(map[string]interface{})
	assert.Contains(t, vote, "has_vote")
	assert.Contains(t, vote, "pour")
	assert.Contains(t, vote, "contre")
	assert.Contains(t, vote, "abstention")

	breakdown := delib["budget_breakdown"].([]interface{})
	require.Len(t, breakdown, 1)
	item := breakdown[0].(map[string]interface{})
	assert.Contains(t, item, "topic_tag")
	assert.Contains(t, item, "label")
	assert.Contains(t, item, "amount")
}

func TestBuildDataJSON(t *testing.T) {
	ctx := context.Background()

	councils := []CouncilRecord{
		{
			CouncilID: "conseil_municipal#2026-03-28",
			Category:  "conseil_municipal",
			Date:      "2026-03-28",
			Title:     "Délibérations du 28 mars",
			SourceURL: "https://example.com",
			TotalPDFs: 2,
			Processed: 2,
		},
	}
	delibs := map[string][]DeliberationRecord{
		"conseil_municipal#2026-03-28": {
			{
				ID:             "abc",
				CouncilID:      "conseil_municipal#2026-03-28",
				Title:          "Élection du Maire",
				TopicTag:       "politique",
				PDFURL:         "https://example.com/D01.pdf",
				Summary:        "Résumé.",
				AnalysisData:   AnalysisData{Contexte: strPtr("Analyse détaillée.")},
				VotePour:       intPtr(32),
				VoteContre:     intPtr(5),
				VoteAbstention: intPtr(2),
				Disagreements:  strPtr(""),
			},
		},
	}

	data, err := buildDataJSON(ctx, nil, councils, delibs)
	require.NoError(t, err)

	require.Len(t, data.Councils, 1)
	assert.Equal(t, "2026-03-28", data.Councils[0].Date)
	require.Len(t, data.Councils[0].Deliberations, 1)
	assert.Equal(t, "Élection du Maire", data.Councils[0].Deliberations[0].Title)
	assert.Equal(t, "politique", data.Councils[0].Deliberations[0].TopicTag)
	assert.Equal(t, "Analyse détaillée.", *data.Councils[0].Deliberations[0].AnalysisData.Contexte)
	assert.Equal(t, 32, *data.Councils[0].Deliberations[0].Vote.Pour)
}

// Council-level aggregates can mix receipts, expenses and repeated operations.
// The publisher must expose only the classified deliberation-level amounts.
func TestBuildDataJSON_OmitsMisleadingCouncilAggregates(t *testing.T) {
	councils := []CouncilRecord{{
		CouncilID: "c1",
		Analysis: CouncilAnalysis{
			BudgetImpact: 9_999_999,
			VoteClimat:   "consensus",
			VoteSummary:  "Le budget est concentré sur l'urbanisme.",
		},
	}}
	delibs := map[string][]DeliberationRecord{
		"c1": {
			{ID: "d1", CouncilID: "c1", BudgetImpact: 100_000, BudgetType: "RECETTE"},
			{ID: "d2", CouncilID: "c1", BudgetImpact: 50_000, BudgetType: "DÉPENSE"},
		},
	}
	data, err := buildDataJSON(context.Background(), nil, councils, delibs)
	require.NoError(t, err)
	assert.Equal(t, CouncilAnalysis{}, data.Councils[0].Analysis)
	assert.Equal(t, int64(100_000), data.Councils[0].Deliberations[0].BudgetImpact)
	assert.Equal(t, "RECETTE", data.Councils[0].Deliberations[0].BudgetType)
	raw, err := json.Marshal(data.Councils[0].Analysis)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(raw))
}

func TestBuildDataJSON_BudgetZeroWhenNoDeliberations(t *testing.T) {
	councils := []CouncilRecord{{
		CouncilID: "c1",
		Analysis:  CouncilAnalysis{BudgetImpact: 500_000},
	}}
	data, err := buildDataJSON(context.Background(), nil, councils, map[string][]DeliberationRecord{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), data.Councils[0].Analysis.BudgetImpact)
}

// --- HasVote detection ---
// HasVote in the output must be true whenever vote counts are present,
// even if the source record has HasVote=false (e.g. reprocessed without the flag).
func TestBuildDataJSON_HasVoteFromVoteCounts(t *testing.T) {
	pour := 32
	councils := []CouncilRecord{{CouncilID: "c1"}}
	delibs := map[string][]DeliberationRecord{
		"c1": {{
			ID: "d1", CouncilID: "c1",
			HasVote:  false, // explicitly false — but vote counts are present
			VotePour: &pour,
		}},
	}
	data, err := buildDataJSON(context.Background(), nil, councils, delibs)
	require.NoError(t, err)
	assert.True(t, data.Councils[0].Deliberations[0].Vote.HasVote,
		"HasVote must be true when VotePour is present, regardless of the HasVote flag")
}

func TestBuildDataJSON_HasVoteFalseWhenNoCounts(t *testing.T) {
	councils := []CouncilRecord{{CouncilID: "c1"}}
	delibs := map[string][]DeliberationRecord{
		"c1": {{ID: "d1", CouncilID: "c1", HasVote: false}},
	}
	data, err := buildDataJSON(context.Background(), nil, councils, delibs)
	require.NoError(t, err)
	assert.False(t, data.Councils[0].Deliberations[0].Vote.HasVote)
}
