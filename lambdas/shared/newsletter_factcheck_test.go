package shared

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseNewsletterFactVerdict(t *testing.T) {
	if err := parseNewsletterFactVerdict(`{"supported":true,"issues":[]}`); err != nil {
		t.Fatalf("supported copy rejected: %v", err)
	}
	for _, raw := range []string{
		`{"supported":false,"issues":[{"location":"adopted[0].impact","claim":"495 arbres seront plantés","reason":"La source indique que les plantations ont déjà été réalisées."}]}`,
		`{"supported":false,"issues":[]}`,
		`{"supported":true,"issues":[{"location":"briefs[0]","claim":"la convention est signée","reason":"Seule la signature est autorisée."}]}`,
	} {
		err := parseNewsletterFactVerdict(raw)
		var factErr *NewsletterFactCheckError
		if !errors.As(err, &factErr) {
			t.Fatalf("unsupported or inconclusive copy was accepted: %s (%v)", raw, err)
		}
	}
	if err := parseNewsletterFactVerdict(`{"supported":false,"issues":[{"location":"","claim":"x","reason":"y"}]}`); err == nil {
		t.Fatal("malformed issue was accepted")
	}
	if err := parseNewsletterFactVerdict(`invalid`); err == nil || !strings.Contains(err.Error(), "parse newsletter") {
		t.Fatalf("invalid JSON not rejected: %v", err)
	}
	for _, raw := range []string{
		`{"supported":true}`,
		`{"supported":true,"issues":null}`,
		`{"supported":true,"issues":[],"extra":true}`,
		`{"supported":true,"issues":[]} {"supported":true,"issues":[]}`,
	} {
		if err := parseNewsletterFactVerdict(raw); err == nil {
			t.Fatalf("malformed verdict accepted: %s", raw)
		}
	}
}

func TestLiveNewsletterFactCheckRejectsCompletedClaims(t *testing.T) {
	if os.Getenv("WATCHDOG_LIVE_FACTCHECK") != "1" {
		t.Skip("set WATCHDOG_LIVE_FACTCHECK=1 for a real Gemini check")
	}
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		t.Fatal("GEMINI_API_KEY is required")
	}
	cold := []ColdDeliberation{
		{ID: "D13.pdf", Title: "Piste cyclable", Decision: "Le conseil approuve la cession gratuite de deux terrains totalisant 980 m² et autorise le maire à signer l'acte.", Summary: "Les terrains sont destinés à une piste cyclable et à des aménagements paysagers."},
		{ID: "D16.pdf", Title: "Bail du CIO", Decision: "Le conseil approuve un projet de renouvellement du bail des locaux du CIO pour neuf ans et autorise le maire à le signer.", Summary: "Loyer prévu de 34 000 € par an."},
	}
	params := &NewsletterParams{
		Adopted: []AdoptedItem{{SourceID: "D13.pdf", Title: "Piste cyclable", Impact: "Les terrains cédés permettront de construire la piste cyclable."}},
		Briefs:  []BriefItem{{SourceID: "D16.pdf", Summary: "Le bail des locaux du CIO est renouvelé pour neuf ans."}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	deps := GeminiDeps{APIKey: key, Model: "gemini-2.5-pro"}
	var factErr *NewsletterFactCheckError
	if err := VerifyNewsletterFacts(ctx, deps, cold, params); !errors.As(err, &factErr) {
		t.Fatalf("unsupported completion claims were accepted: %v", err)
	}
	params.Adopted[0].Impact = "Les terrains dont la cession est approuvée sont destinés à une piste cyclable."
	params.Briefs[0].Summary = "Le conseil approuve le projet de renouvellement du bail des locaux du CIO pour neuf ans."
	if err := VerifyNewsletterFacts(ctx, deps, cold, params); err != nil {
		t.Fatalf("faithful approval wording was rejected: %v", err)
	}
}

func TestValidateNewsletterSourceLinks(t *testing.T) {
	no := 0
	nine := 9
	cold := []ColdDeliberation{
		{ID: "D01.pdf", Title: "Règlement", TopicTag: "Administration", BudgetImpact: 0, Contre: &nine},
		{ID: "D02.pdf", Title: "Aide", TopicTag: "Social", BudgetImpact: 30000, Contre: &no},
	}
	valid := &NewsletterParams{
		Tensions: []TensionItem{{SourceID: "D01.pdf", Title: "Règlement", VoteDetails: "9 votes contre"}},
		Adopted:  []AdoptedItem{{SourceID: "D02.pdf", Tag: "Social", Title: "Aide", Budget: "30 000 €"}},
	}
	if err := validateNewsletterSourceLinks(valid, cold); err != nil {
		t.Fatalf("valid mapping rejected: %v", err)
	}
	for name, modify := range map[string]func(*NewsletterParams){
		"invented ID":        func(p *NewsletterParams) { p.Adopted[0].SourceID = "D99.pdf" },
		"wrong amount":       func(p *NewsletterParams) { p.Adopted[0].Budget = "40 000 €" },
		"wrong category":     func(p *NewsletterParams) { p.Adopted[0].Tag = "Culture" },
		"duplicate":          func(p *NewsletterParams) { p.Briefs = []BriefItem{{SourceID: "D02.pdf", Tag: "Social"}} },
		"missing opposition": func(p *NewsletterParams) { p.Tensions = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p := *valid
			p.Tensions = append([]TensionItem(nil), valid.Tensions...)
			p.Adopted = append([]AdoptedItem(nil), valid.Adopted...)
			modify(&p)
			var factErr *NewsletterFactCheckError
			if err := validateNewsletterSourceLinks(&p, cold); !errors.As(err, &factErr) {
				t.Fatalf("invalid mapping was accepted: %v", err)
			}
		})
	}
}
