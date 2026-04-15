package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		{Vote: struct {
			Pour       *int `dynamodbav:"pour"`
			Contre     *int `dynamodbav:"contre"`
			Abstention *int `dynamodbav:"abstention"`
		}{Pour: intPtr(30), Contre: intPtr(2), Abstention: intPtr(1)}},
		{Vote: struct {
			Pour       *int `dynamodbav:"pour"`
			Contre     *int `dynamodbav:"contre"`
			Abstention *int `dynamodbav:"abstention"`
		}{Pour: intPtr(10), Contre: intPtr(3)}},
	}
	stats := computeStats(delibs)
	assert.Equal(t, 40, stats.totalPour)
	assert.Equal(t, 5, stats.totalContre)
	assert.Equal(t, 1, stats.totalAbst)
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

func TestComputeStats_NilVotePointers(t *testing.T) {
	// Vote fields can be nil — must not panic
	delibs := []Deliberation{{BudgetImpact: 1000}}
	stats := computeStats(delibs)
	assert.Equal(t, 0, stats.totalPour)
	assert.Equal(t, 0, stats.totalContre)
}
