package main

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int { return &i }

// --- voteClimat ---

func TestVoteClimat_Consensus(t *testing.T) {
	// Under 10% opposition → consensus
	assert.Equal(t, "consensus", voteClimat(39, 0))
	assert.Equal(t, "consensus", voteClimat(39, 1)) // 2.5%
	assert.Equal(t, "consensus", voteClimat(10, 1)) // 9.09% — just under threshold
}

func TestVoteClimat_Tensions(t *testing.T) {
	// Strictly over 10% opposition → tensions (condition is >, not >=)
	// 10.0% exactly (9 pour, 1 contre) → consensus (not strictly above threshold)
	assert.Equal(t, "consensus", voteClimat(9, 1)) // 10.0% — AT threshold, not above → consensus
	assert.Equal(t, "tensions", voteClimat(8, 1))  // 11.1% — above threshold
	assert.Equal(t, "tensions", voteClimat(5, 2))  // 28.6%
	assert.Equal(t, "tensions", voteClimat(0, 5))  // 100%
}

func TestVoteClimat_NoVotes(t *testing.T) {
	// No votes cast → consensus (avoid division by zero)
	assert.Equal(t, "consensus", voteClimat(0, 0))
}

// --- dominantTheme ---

func TestDominantTheme_SingleTopic(t *testing.T) {
	assert.Equal(t, "Sport", dominantTheme(map[string]int64{"Sport": 100_000}))
}

func TestDominantTheme_PicksHighest(t *testing.T) {
	topics := map[string]int64{
		"Sport":    100_000,
		"Culture":  200_000,
		"Social":   50_000,
	}
	assert.Equal(t, "Culture", dominantTheme(topics))
}

func TestDominantTheme_Empty(t *testing.T) {
	// No topics → fallback to "Administration"
	assert.Equal(t, "Administration", dominantTheme(map[string]int64{}))
}

func TestDominantTheme_AllZero(t *testing.T) {
	// All budgets are zero → first found (non-deterministic), but must not panic
	// and must not be "Administration" since at least one topic exists
	result := dominantTheme(map[string]int64{"Sport": 0, "Culture": 0})
	assert.NotEmpty(t, result)
}

// --- computeStats ---

func TestComputeStats_SumsBudget(t *testing.T) {
	delibs := []Deliberation{
		{BudgetImpact: 100_000, TopicTag: "Sport"},
		{BudgetImpact: 50_000, TopicTag: "Culture"},
		{BudgetImpact: 0},
	}
	stats := computeStats(delibs)
	assert.Equal(t, int64(150_000), stats.totalBudget)
	assert.Equal(t, int64(100_000), stats.topicBudgets["Sport"])
	assert.Equal(t, int64(50_000), stats.topicBudgets["Culture"])
}

func TestComputeStats_SumsVotes(t *testing.T) {
	delibs := []Deliberation{
		{VotePour: intPtr(30), VoteContre: intPtr(2), VoteAbst: intPtr(1)},
		{VotePour: intPtr(10), VoteContre: intPtr(3)},
	}
	stats := computeStats(delibs)
	assert.Equal(t, 40, stats.totalPour)
	assert.Equal(t, 5, stats.totalContre)
	assert.Equal(t, 1, stats.totalAbst)
}

// TestUnmarshal_FlatVoteAttributes reproduces the exact DynamoDB item shape
// produced by the worker (lambdas/worker/handler.go) and asserts that the
// aggregator's Deliberation struct correctly decodes flat vote_* attributes.
// Regression guard for the silent nested-vs-flat mismatch that left every
// Pour/Contre/Abst pointer at nil and forced voteClimat() to "consensus".
func TestUnmarshal_FlatVoteAttributes(t *testing.T) {
	item := map[string]types.AttributeValue{
		"id":              &types.AttributeValueMemberS{Value: "delib-42"},
		"council_id":      &types.AttributeValueMemberS{Value: "council-2026-05"},
		"budget_impact":   &types.AttributeValueMemberN{Value: "12500"},
		"topic_tag":       &types.AttributeValueMemberS{Value: "Urbanisme"},
		"summary":         &types.AttributeValueMemberS{Value: "Vote serré sur la ZAC"},
		"vote_pour":       &types.AttributeValueMemberN{Value: "10"},
		"vote_contre":     &types.AttributeValueMemberN{Value: "4"},
		"vote_abstention": &types.AttributeValueMemberN{Value: "2"},
	}

	var d Deliberation
	require.NoError(t, attributevalue.UnmarshalMap(item, &d))

	require.NotNil(t, d.VotePour, "VotePour should be populated from flat vote_pour")
	require.NotNil(t, d.VoteContre, "VoteContre should be populated from flat vote_contre")
	require.NotNil(t, d.VoteAbst, "VoteAbst should be populated from flat vote_abstention")
	assert.Equal(t, 10, *d.VotePour)
	assert.Equal(t, 4, *d.VoteContre)
	assert.Equal(t, 2, *d.VoteAbst)

	// And the downstream pipeline now flags this council as tensions.
	stats := computeStats([]Deliberation{d})
	assert.Equal(t, "tensions", voteClimat(stats.totalPour, stats.totalContre))
}

func TestComputeStats_CollectsSummaries(t *testing.T) {
	delibs := []Deliberation{
		{Summary: "Premier résumé."},
		{Summary: ""},
		{Summary: "Deuxième résumé."},
	}
	stats := computeStats(delibs)
	assert.Equal(t, []string{"- Premier résumé.", "- Deuxième résumé."}, stats.summaries)
}

func TestComputeStats_Empty(t *testing.T) {
	stats := computeStats(nil)
	assert.Equal(t, int64(0), stats.totalBudget)
	assert.Equal(t, 0, stats.totalPour)
	assert.Empty(t, stats.summaries)
}

// TestBuildPublisherPayload_ContainsCouncilID guards against the silent
// regression where aggregator invoked the publisher without Payload, leaving
// the downstream notifier with an empty council_id and dropping the
// newsletter for every council triggered via the DynamoDB Stream path.
func TestBuildPublisherPayload_ContainsCouncilID(t *testing.T) {
	payload, err := buildPublisherPayload("council-2026-05")
	require.NoError(t, err)
	require.NotEmpty(t, payload)

	var decoded map[string]string
	require.NoError(t, json.Unmarshal(payload, &decoded))
	assert.Equal(t, "council-2026-05", decoded["council_id"])
}

func TestComputeStats_NilVotePointers(t *testing.T) {
	// Vote fields can be nil — must not panic
	delibs := []Deliberation{{BudgetImpact: 1000}}
	stats := computeStats(delibs)
	assert.Equal(t, 0, stats.totalPour)
	assert.Equal(t, 0, stats.totalContre)
	assert.Equal(t, 0, stats.totalAbst)
}
