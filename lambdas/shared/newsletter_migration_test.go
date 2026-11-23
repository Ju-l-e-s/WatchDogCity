package shared

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newsletterMigrationFacts() []ColdDeliberation {
	pour, contre, abstention := 29, 2, 1
	return []ColdDeliberation{
		{ID: "D23-2026_113.pdf", Title: "Convention de financement des plantations", TopicTag: "Environnement", BudgetImpact: 23792,
			BudgetType: "RECETTE", BudgetNote: "Subvention plafonnée à 23 792 euros, versement sur justificatifs.",
			Decision: "Le conseil approuve une convention de financement.", Summary: "Les plantations ont été réalisées de 2024 à 2025.",
			Impacts: "Néant", IsSubstantial: true},
		{ID: "D16.pdf", Title: "Projet de bail du CIO", TopicTag: "Administration", BudgetImpact: 34000, BudgetType: "DÉPENSE",
			BudgetNote: "Loyer prévu de 34 000 euros par an.", Decision: "Le conseil approuve un projet de renouvellement du bail pour neuf ans et autorise sa signature.",
			Summary: "Le bail concerne les locaux du CIO.", Impacts: "Néant", HasVote: true,
			Pour: &pour, Contre: &contre, Abstention: &abstention},
	}
}

func newsletterReferenceIssues(params *NewsletterParams) []string {
	var issues []string
	if params.CouncilDate != "16 novembre 2026" || params.NextMeeting != "14 décembre 2026" {
		issues = append(issues, "dates de séance")
	}
	digits := regexp.MustCompile(`\D+`)
	adopted := false
	for _, item := range params.Adopted {
		if item.SourceID != "D23-2026_113.pdf" {
			continue
		}
		adopted = true
		copy := strings.ToLower(item.Title + " " + item.Context + " " + item.Impact)
		for _, fragment := range []string{"convention", "2024", "2025", "justificati"} {
			if !strings.Contains(copy, fragment) {
				issues = append(issues, "plantations : "+fragment)
			}
		}
		if !mentionsAny(copy, "plafond", "plafonn", "jusqu'à", "maximum") ||
			(digits.ReplaceAllString(item.Budget, "") != "23792" && !strings.Contains(strings.NewReplacer(" ", "", "\u00a0", "", "\u202f", "").Replace(copy), "23792")) {
			issues = append(issues, "plantations : montant plafonné")
		}
	}
	if !adopted {
		issues = append(issues, "financement des plantations omis")
	}
	tension := false
	for _, item := range params.Tensions {
		if item.SourceID != "D16.pdf" {
			continue
		}
		tension = true
		copy := strings.ToLower(item.Title + " " + item.Context + " " + item.Impact)
		if item.VoteDetails != "2 votes contre, 1 abstention" ||
			!strings.Contains(copy, "projet") || !mentionsAny(copy, "neuf ans", "9 ans") ||
			!mentionsBudgetPeriod(copy) || digits.ReplaceAllString(item.Budget, "") != "34000" {
			issues = append(issues, "bail : stade, durée, loyer annuel ou vote")
		}
	}
	if !tension {
		issues = append(issues, "bail avec voix contre omis")
	}
	return issues
}

func mentionsAny(copy string, alternatives ...string) bool {
	for _, text := range alternatives {
		if strings.Contains(copy, text) {
			return true
		}
	}
	return false
}

func TestNewsletterMigrationReferenceOffline(t *testing.T) {
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "https://fixture.invalid")
	var calls []string
	deps := GeminiDeps{APIKey: "offline-fixture", Model: PDFExtractionModel, FactCheckModel: GeminiFlashModel,
		HTTPClient: &http.Client{Transport: geminiTransport(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.Path)
			response := `{"supported":true,"issues":[]}`
			if len(calls) == 1 {
				response = `{"adopted":[{"source_id":"D23-2026_113.pdf","tag":"Environnement","title":"Financement des plantations","context":"Le conseil approuve la convention de financement des plantations réalisées entre 2024 et 2025. La subvention, plafonnée à 23 792 €, sera versée sur justificatifs.","impact":"","budget":"23 792"}],"tensions":[{"source_id":"D16.pdf","title":"Projet de bail","context":"Le conseil approuve le projet de renouvellement pour neuf ans. Le loyer prévu est de 34 000 € par an.","impact":"","budget":"34 000"}],"briefs":[]}`
			}
			data, _ := json.Marshal(response)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":%s}]}}]}`, data)))}, nil
		})}}
	result, err := GenerateNewsletterParams(context.Background(), deps, "Conseil", "2026-11-16", newsletterMigrationFacts(), "14 décembre 2026", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], PDFExtractionModel) || !strings.Contains(calls[1], GeminiFlashModel) {
		t.Fatalf("generation and verification models are not independent: %v", calls)
	}
	if issues := newsletterReferenceIssues(result); len(issues) != 0 {
		t.Fatalf("reference copy misses %v", issues)
	}
	result.Adopted[0].Context = "Le conseil approuve la convention."
	result.Tensions[0].VoteDetails = "2 votes contre"
	if issues := newsletterReferenceIssues(result); len(issues) < 2 {
		t.Fatalf("reference rubric did not detect changed votes and missing facts: %v", issues)
	}
}

func TestMigrationLiveNewsletter(t *testing.T) {
	if os.Getenv("WATCHDOG_GEMINI_EVAL") != "1" {
		t.Skip("explicit opt-in required for paid Gemini evaluation")
	}
	deps := GeminiDeps{APIKey: os.Getenv("GEMINI_API_KEY"), Model: os.Getenv("GEMINI_MODEL"), FactCheckModel: os.Getenv("GEMINI_FACT_CHECK_MODEL")}
	if deps.APIKey == "" || deps.Model == "" || deps.FactCheckModel == "" {
		t.Fatal("controlled evaluation requires a key and explicit generation and verification models")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := GenerateNewsletterParams(ctx, deps, "Conseil", "2026-11-16", newsletterMigrationFacts(), "14 décembre 2026", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	copy, _ := json.Marshal(result)
	t.Logf("Synthetic comparison corpus, human review required: %s", copy)
	if issues := newsletterReferenceIssues(result); len(issues) != 0 {
		t.Fatalf("reference copy misses %v", issues)
	}
}
