package shared

import (
	"strings"
	"testing"
)

func intPtr(n int) *int { return &n }

func TestColdNewsletterStatsReflectNonUnanimousVotes(t *testing.T) {
	stats := computeColdNewsletterStats([]ColdDeliberation{
		{HasVote: true, Pour: intPtr(26), Contre: intPtr(9), Abstention: intPtr(0)},
		{HasVote: true, Pour: intPtr(26), Contre: intPtr(0), Abstention: intPtr(9)},
		{HasVote: true, Pour: intPtr(35), Contre: intPtr(0), Abstention: intPtr(0)},
	})
	if stats.voteClimat != "VOTES AVEC OPPOSITION" {
		t.Fatalf("voteClimat = %q", stats.voteClimat)
	}
	if stats.voteStats != "2 délib. non unanimes / jusqu'à 9 voix contre" {
		t.Fatalf("voteStats = %q", stats.voteStats)
	}
}

func TestColdNewsletterStatsDistinguishAbstentionsAndMissingVotes(t *testing.T) {
	cases := []struct {
		name string
		cold []ColdDeliberation
		want string
	}{
		{"abstentions", []ColdDeliberation{{HasVote: true, Pour: intPtr(26), Abstention: intPtr(9)}}, "VOTES AVEC ABSTENTIONS"},
		{"unanimity", []ColdDeliberation{{HasVote: true, Pour: intPtr(35), Contre: intPtr(0)}}, "VOTES UNANIMES"},
		{"missing", []ColdDeliberation{{HasVote: true}}, "VOTES NON RENSEIGNÉS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeColdNewsletterStats(tc.cold).voteClimat; got != tc.want {
				t.Fatalf("voteClimat = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFinalizeNewsletterParamsOverridesModelFacts(t *testing.T) {
	cold := []ColdDeliberation{
		{Title: "Cession de parcelles à Domofrance pour 27 logements", BudgetImpact: 696000, BudgetType: "RECETTE", HasVote: true, Pour: intPtr(26), Abstention: intPtr(9), Summary: "8 logements sociaux et 19 intermédiaires"},
		{Title: "Annulation de créances", TopicTag: "Budget", BudgetImpact: 31247, BudgetType: "DÉPENSE", HasVote: true, Pour: intPtr(35)},
	}
	params := &NewsletterParams{
		MainIssue:            "27 logements sociaux sont construits",
		BudgetTotal:          "31 247",
		HasGlobalBudget:      true,
		VoteClimat:           "CONSENSUS",
		VoteStats:            "Unanimité totale",
		TotalDelibsInCouncil: 99,
		CouncilDate:          "2028",
		TotalCouncils:        999,
		TotalDelibs:          999,
	}
	finalizeNewsletterParams(params, "2026-09-28", cold,
		computeColdNewsletterStats(cold), "16 novembre", 10, 171)
	if params.BudgetTotal != "" || params.HasGlobalBudget {
		t.Fatalf("misleading global budget remains: %+v", params)
	}
	if params.VoteClimat != "VOTES AVEC ABSTENTIONS" || params.VoteStats != "1 délib. non unanime" {
		t.Fatalf("model vote figures remain: %+v", params)
	}
	if params.CouncilTitle != "Conseil municipal du 28 septembre 2026" || params.CouncilDate != "28 septembre 2026" || params.TotalDelibsInCouncil != 2 || params.TotalCouncils != 10 || params.TotalDelibs != 171 {
		t.Fatalf("model metadata remains: %+v", params)
	}
	if !strings.Contains(params.MainIssue, "696 000 € (recette)") || strings.Contains(params.MainIssue, "27 logements sociaux") {
		t.Fatalf("main issue changed the housing breakdown: %q", params.MainIssue)
	}
}
