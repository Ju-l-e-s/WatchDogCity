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
		Adopted: []AdoptedItem{
			{Context: "La Ville attribue 30 000 euros à l'association SAGE. Cette aide soutient l'accueil des enfants."},
			{Context: "La ville reçoit un financement pour la plantation d'arbres. Le projet concerne deux sites."},
		},
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
	if params.MainIssue != "La ville attribue 30 000 euros à l'association SAGE. Elle reçoit un financement pour la plantation d'arbres." {
		t.Fatalf("main issue does not lead with adopted decisions: %q", params.MainIssue)
	}
}

func TestDeterministicMainIssueSelectsDecisionsAfterBackground(t *testing.T) {
	params := &NewsletterParams{Adopted: []AdoptedItem{
		{Context: "L'association gère trois établissements et fait face à des difficultés financières. Le conseil lui attribue une subvention exceptionnelle de 30 000 €."},
		{Context: "Des travaux ont été réalisés en 2024-2025. Le conseil approuve une convention de financement avec Bordeaux Métropole."},
	}}
	got := deterministicMainIssue(params)
	if !strings.HasPrefix(got, "Le conseil lui attribue une subvention") || !strings.Contains(got, "Le conseil approuve une convention") || strings.Contains(got, "fait face") {
		t.Fatalf("introduction must lead with two decisions, got %q", got)
	}
}

func TestNewsletterIntroRequiresStandaloneDecisionSentences(t *testing.T) {
	params := &NewsletterParams{Adopted: []AdoptedItem{
		{Context: "L'association SAGE gère 64 places d'accueil. Le conseil lui attribue 30 000 €."},
		{Context: "Le conseil approuve une convention de financement pour des travaux déjà réalisés."},
	}}
	if err := validateNewsletterIntro(params); err == nil {
		t.Fatal("background sentence was accepted as introduction")
	}
	params.Adopted[0].Context = "Le conseil attribue une subvention de 30 000 € à l'association SAGE. Cette aide vise le maintien de 64 places."
	if err := validateNewsletterIntro(params); err != nil {
		t.Fatalf("standalone decisions rejected: %v", err)
	}
}

func TestFinalizeNewsletterParamsHidesUnqualifiedRecurringAmount(t *testing.T) {
	cold := []ColdDeliberation{{
		Title: "Restaurant au Chapitô", BudgetImpact: 12264, BudgetType: "RECETTE",
		Summary: "Une redevance annuelle fixe de 10 200 euros, une part variable de 5 % et un forfait mensuel de 172 euros sont prévus.",
	}}
	params := &NewsletterParams{Adopted: []AdoptedItem{{
		Title: "Restaurant au Chapitô", Context: "La ville autorise un restaurant pour trois ans.", Budget: "12264", HasBudget: true,
	}}}
	finalizeNewsletterParams(params, "2026-09-28", cold, computeColdNewsletterStats(cold), "", 0, 0)
	if params.Adopted[0].HasBudget || params.Adopted[0].Budget != "" {
		t.Fatalf("unqualified annual amount remains visible: %+v", params.Adopted[0])
	}
	params.Adopted[0].Budget = "12264"
	params.Adopted[0].Context = "La ville percevra au moins 12 264 euros par an, plus une part variable."
	finalizeNewsletterParams(params, "2026-09-28", cold, computeColdNewsletterStats(cold), "", 0, 0)
	if !params.Adopted[0].HasBudget || params.Adopted[0].Budget != "12 264" {
		t.Fatalf("qualified annual amount was hidden: %+v", params.Adopted[0])
	}
}

func TestSourceAmountQualifiedRequiresCapInCopy(t *testing.T) {
	cold := []ColdDeliberation{{ID: "D18.pdf", BudgetImpact: 1200,
		BudgetNote: "Aide de 40 € par foyer, soit 1 200 € au maximum."}}
	if sourceAmountQualified("D18.pdf", "1 200", "La ville verse 1 200 euros.", cold) {
		t.Fatal("uncertain ceiling presented as a fixed expense")
	}
	if !sourceAmountQualified("D18.pdf", "1 200", "La dépense atteindra au maximum 1 200 euros.", cold) {
		t.Fatal("qualified ceiling was rejected")
	}
}
