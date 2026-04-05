package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDataJSON(t *testing.T) {
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
				Analysis:       "Analyse détaillée.",
				VotePour:       32,
				VoteContre:     5,
				VoteAbstention: 2,
				Disagreements:  "",
			},
		},
	}

	data, err := buildDataJSON(councils, delibs)
	require.NoError(t, err)

	require.Len(t, data.Councils, 1)
	assert.Equal(t, "2026-03-28", data.Councils[0].Date)
	require.Len(t, data.Councils[0].Deliberations, 1)
	assert.Equal(t, "Élection du Maire", data.Councils[0].Deliberations[0].Title)
	assert.Equal(t, "politique", data.Councils[0].Deliberations[0].TopicTag)
	assert.Equal(t, "Analyse détaillée.", data.Councils[0].Deliberations[0].Analysis)
	assert.Equal(t, 32, data.Councils[0].Deliberations[0].Vote.Pour)
}
