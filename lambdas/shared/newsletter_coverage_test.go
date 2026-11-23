package shared

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPilotNewsletterCoverageAndEligibility(t *testing.T) {
	data, err := os.ReadFile("../worker/corpus/pilot_2026-11_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		Records []struct {
			Kind string
			Data json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	var cold []ColdDeliberation
	var params NewsletterParams
	for _, record := range baseline.Records {
		switch record.Kind {
		case "frozen_newsletter_input":
			if err := json.Unmarshal(record.Data, &cold); err != nil {
				t.Fatal(err)
			}
		case "final_newsletter":
			if err := json.Unmarshal(record.Data, &params); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(cold) != 3 || len(params.Briefs) != 1 {
		t.Fatalf("baseline fixture incomplete")
	}
	for i := range cold {
		d := &cold[i]
		// The pilot made D07 substantial manually. Restore the observed
		// extraction eligibility inputs before testing the production rule.
		d.IsSubstantial, d.HasDisagreement = false, false
		switch d.ID {
		case "D07-2026_125.pdf":
			d.SourceFacts = reviewedNewsletterFacts(
				SourceFact{Kind: "CIVIC_SCOPE", Audience: "SITE", Label: "Recensement de la population"},
				SourceFact{Kind: "DECISION", Audience: "BRIEF", Label: "Le conseil décide de recruter sept agents pour le recensement du 21 janvier au 27 février 2027"},
				SourceFact{Kind: "DATE", Audience: "SITE", Label: "Recensement des habitations mobiles les 21 et 22 janvier"},
			)
		case "D09-2026_127.pdf":
			d.SourceFacts = reviewedNewsletterFacts(
				SourceFact{Kind: "FINANCE", Audience: "BRIEF", Label: "Variation des crédits d’investissement", Money: &SourceMoney{Cents: new(int64(-110_255_042))}},
				SourceFact{Kind: "FINANCE", Audience: "BRIEF", Label: "Variation des crédits de fonctionnement", Money: &SourceMoney{Cents: new(int64(23_336_406))}},
			)
		case "D10-2026_128.pdf":
			d.SourceFacts = reviewedNewsletterFacts(
				SourceFact{Kind: "FINANCE", Audience: "BRIEF", Label: "Révision de l’enveloppe pluriannuelle Prévert", Money: &SourceMoney{Cents: new(int64(-50_000_000))}},
				SourceFact{Kind: "FINANCE", Audience: "BRIEF", Label: "Variation imprimée des crédits de paiement Prévert", Money: &SourceMoney{Cents: new(int64(-20_000_000)), Period: "en 2026"}},
				SourceFact{Kind: "FINANCE", Audience: "BRIEF", Label: "Variation des crédits de paiement du complexe sportif", Money: &SourceMoney{Cents: new(int64(-100_000_000)), Period: "en 2026"}},
				SourceFact{Kind: "FINANCE", Audience: "ARTICLE", Label: "Total des crédits de paiement", Money: &SourceMoney{Cents: new(int64(-120_000_000))}},
				SourceFact{Kind: "VOTE", Audience: "BRIEF", Label: "Vote du conseil", Vote: &SourceVote{For: d.Pour, Against: d.Contre, Abstain: d.Abstention}},
			)
		}
	}
	byID := newsletterSources(cold)
	d07, d10 := byID["D07-2026_125.pdf"], byID["D10-2026_128.pdf"]
	if !civicPriority(d07) || d07.BudgetImpact != 0 || d07.IsSubstantial || nonUnanimousNewsletterItem(d10) {
		t.Fatal("civic priority or abstention classified incorrectly")
	}
	prompt := buildColdNewsletterPrompt("Conseil", "2026-11-16", cold, computeColdNewsletterStats(cold), "", 0, 0)
	if !strings.Contains(prompt, "- ID: "+d07.ID) || !strings.Contains(prompt, "Portée citoyenne prioritaire") {
		t.Fatal("census missing from the actual writer input")
	}
	if err := validateNewsletterSourceLinks(&params, cold); err != nil {
		t.Fatal(err)
	}
	withoutCensus := params
	withoutCensus.Adopted = nil
	if err := validateNewsletterSourceLinks(&withoutCensus, cold); err == nil || !strings.Contains(err.Error(), "portée citoyenne") {
		t.Fatalf("missing census must fail selection: %v", err)
	}
	var issues *NewsletterFactCheckError
	err = validateNewsletterCoverage(&params, cold)
	if !errors.As(err, &issues) || len(issues.Issues) == 0 {
		t.Fatalf("baseline omissions went unnoticed: %v", err)
	}
	for _, issue := range issues.Issues {
		if issue.Kind != "OMISSION" {
			t.Fatalf("baseline omission reported as %s", issue.Kind)
		}
	}
	finalizeNewsletterParams(&params, "2026-11-16", cold, computeColdNewsletterStats(cold), "", 0, 0)
	if err := validateNewsletterCoverage(&params, cold); err != nil {
		t.Fatal(err)
	}
	brief := params.Briefs[0].Summary
	for _, expected := range []string{"−500 000 €", "−200 000 €", "−1 000 000 €", "9 abstentions"} {
		if !strings.Contains(brief, expected) {
			t.Errorf("brief missing %q: %s", expected, brief)
		}
	}
	if strings.Contains(brief, "Total des crédits de paiement") || len(newsletterFactLines(d07, "adopted")) != 1 {
		t.Fatal("detail included in a shorter audience than requested")
	}
}

func TestBriefPreservesConditionAndDeadline(t *testing.T) {
	cold := []ColdDeliberation{{ID: "D23.pdf", SourceFacts: reviewedNewsletterFacts(
		SourceFact{Kind: "DECISION", Audience: "BRIEF", Label: "Le conseil approuve la convention"},
		SourceFact{Kind: "FINANCE", Audience: "BRIEF", Label: "Plafond de la subvention", Money: &SourceMoney{Cents: new(int64(2_379_200))}},
		SourceFact{Kind: "CONDITION", Audience: "BRIEF", Label: "Le versement unique dépend des justificatifs à fournir dans les six mois"},
		SourceFact{Kind: "FINANCE", Audience: "SITE", Label: "Coût éligible HT", Money: &SourceMoney{Cents: new(int64(4_758_218))}},
	)}}
	params := NewsletterParams{Briefs: []BriefItem{{SourceID: "D23.pdf", Summary: "Le conseil approuve la convention."}}}
	finalizeNewsletterParams(&params, "2026-11-16", cold, coldNewsletterStats{}, "", 0, 0)
	if err := validateNewsletterCoverage(&params, cold); err != nil {
		t.Fatal(err)
	}
	summary := params.Briefs[0].Summary
	if strings.Count(summary, "Le conseil approuve la convention") != 1 ||
		!strings.Contains(summary, "23 792 €") || !strings.Contains(summary, "justificatifs à fournir dans les six mois") ||
		strings.Contains(summary, "47 582,18") {
		t.Fatalf("unexpected brief: %s", summary)
	}
	params.Briefs[0].Summary = strings.Replace(summary, "dans les six mois", "dans les trois mois", 1)
	if err := validateNewsletterCoverage(&params, cold); err == nil {
		t.Fatal("changed condition satisfied deterministic coverage")
	}
}

func TestPilotD10RealBriefReplay(t *testing.T) {
	data, err := os.ReadFile("../worker/corpus/pilot_2026-11_second.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Input     []ColdDeliberation `json:"newsletter_input"`
		Assembled string             `json:"assembled_d10_brief"`
		Responses []struct{ Stage, Text string }
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var params NewsletterParams
	for _, response := range fixture.Responses {
		if response.Stage == "newsletter_write" {
			if err := json.Unmarshal([]byte(response.Text), &params); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(params.Briefs) != 1 || len(strings.Fields(params.Briefs[0].Summary)) != 54 || len(strings.Fields(fixture.Assembled)) != 124 {
		t.Fatal("recorded raw and assembled brief sizes differ from pilot")
	}
	// The frozen D10 annotations establish that both printed CP adjustments
	// belong to the same printed total. Record that relationship explicitly.
	for i := range fixture.Input {
		d := &fixture.Input[i]
		if d.ID == "D10-2026_128.pdf" {
			for j := range d.SourceFacts.Facts {
				f := &d.SourceFacts.Facts[j]
				if f.ID == "D10-cp-total" {
					f.Money.TotalOf = []string{"D10-cp-prevert", "D10-cp-complexe"}
				}
			}
		}
	}
	finalizeNewsletterParams(&params, "2026-11-16", fixture.Input, computeColdNewsletterStats(fixture.Input), "", 0, 0)
	if err := validateNewsletterCoverage(&params, fixture.Input); err != nil {
		t.Fatal(err)
	}
	brief := params.Briefs[0].Summary
	for _, atom := range []string{"conseil approuve", "−500 000 €", "−200 000 €", "−1 000 000 €", "−1 200 000 €", "2026", "30 pour, 0 contre, 9 abstentions"} {
		if n := strings.Count(brief, atom); n != 1 {
			t.Errorf("%q occurred %d times in %q", atom, n, brief)
		}
	}
	if n := len(strings.Fields(brief)); n >= 90 {
		t.Fatalf("brief should remain concise, got %d words: %s", n, brief)
	}
	t.Logf("Pilot brief raw 54 words, old assembly 124, new assembly %d", len(strings.Fields(brief)))
}

func reviewedNewsletterFacts(facts ...SourceFact) *SourceFacts {
	return &SourceFacts{Version: SourceFactsVersion, ProofStatus: ProofModelReviewed,
		PDFSHA256: strings.Repeat("a", 64), ReviewedPayloadSHA256: strings.Repeat("b", 64), Facts: facts}
}
