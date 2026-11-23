package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/watchdog/shared"
)

const fundingFixtures = "corpus/fixtures/funding-review/"

func fundingCandidate(t *testing.T, name string) (*GeminiResult, []byte) {
	t.Helper()
	path := "corpus/fixtures/pilot-2026-11/D23-2026_113.pdf"
	if strings.HasPrefix(name, "D02") {
		path = fundingFixtures + "D02-2026_120.pdf"
	}
	document, err := os.ReadFile(path)
	require.NoError(t, err)
	raw, err := os.ReadFile(fundingFixtures + name + ".json")
	require.NoError(t, err)
	r, err := validatePDFExtraction(factCheckResponse(string(raw)), document)
	require.NoError(t, err)
	return r, document
}

// Simulate only the remote judgments. Proposals for A come from the frozen
// correct reference B, never from the candidate under review.
func fundingReplay(t *testing.T, name string, r *GeminiResult) factCheckResult {
	t.Helper()
	reference, _ := fundingCandidate(t, "D23-complet")
	var report factCheckResult
	require.NoError(t, json.Unmarshal([]byte(factCheckFixtureResponse(r.CriticalFacts)), &report))
	financialEvidence := r.CriticalFacts[1].Evidence
	for field, target := range financialTargets(r) {
		evidence := financialEvidence
		if target.Mode == "TRANSFER" {
			evidence = target.Value.(shared.SourceFact).Evidence
		}
		report.Financial = append(report.Financial, financialCheck{Field: field, Status: "SUPPORTED", Evidence: []shared.PDFPassage{evidence}})
	}
	report.Funding = &fundingCoverage{Applies: ptrBool(true), ProseConsistent: ptrBool(true)}
	for _, category := range fundingCategories {
		row := fundingCoverageCheck{Category: category, Status: "MATCH"}
		ids := map[string][]string{"PAYMENT_MODE": {"versement"}, "PREREQUISITES": {"decompte", "quantite", "bilan"}, "DEADLINE": {"delai"}, "CEILING": {"fact_subvention_montant", "fact_depenses_eligibles", "fact_condition_calcul"}, "STAGE": {"fact_decision_convention", "fact_impact_arbres"}}[category]
		if name == "D02-sans-delai" {
			ids = map[string][]string{"CEILING": {"aide"}, "STAGE": {"engagement"}}[category]
			if len(ids) == 0 {
				row.Status = "NOT_ESTABLISHED"
				row.Reason = "Les modalités financières sont renvoyées à la convention."
				row.Evidence = &shared.PDFPassage{Page: 2, Excerpt: "selon les modalités financières qui seront définies dans le cadre d'une convention."}
			}
		}
		for _, id := range ids {
			idx := slices.IndexFunc(r.CriticalFacts, func(f shared.SourceFact) bool { return f.ID == id })
			if idx >= 0 {
				f := r.CriticalFacts[idx]
				row.Matches = append(row.Matches, fundingMatch{FactID: id, CandidateText: f.Label})
				row.Evidence = &f.Evidence
				continue
			}
			idx = slices.IndexFunc(reference.CriticalFacts, func(f shared.SourceFact) bool { return f.ID == id })
			require.NotEqual(t, -1, idx)
			f := reference.CriticalFacts[idx]
			row.Status = "MISSING"
			row.FindingIndexes = append(row.FindingIndexes, len(report.Findings))
			report.Findings = append(report.Findings, factCheckFinding{Kind: "OMISSION", Resolution: "BLOCK", Field: "critical_facts", Claim: f.Label, Reason: "Clause absente", Evidence: &f.Evidence, FinancialFactID: f.FinancialFactID})
		}
		if name == "D23-incorrect" && category == "DEADLINE" {
			row.Status = "INCORRECT"
			row.Matches = nil
			row.FindingIndexes = []int{len(report.Findings)}
			report.Funding.ProseConsistent = ptrBool(false)
			report.Findings = append(report.Findings, factCheckFinding{Kind: "CONTRADICTION", Resolution: "BLOCK", Field: "summary", Claim: "La subvention est versée sous six mois.", Reason: "Le délai porte sur la fourniture des justificatifs.", Evidence: row.Evidence})
		}
		report.Funding.Checks = append(report.Funding.Checks, row)
	}
	return report
}

func TestFundingFrozenCandidates(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		omissions, contradictions int
		accept                    bool
	}{
		{"D23-incomplet", 5, 0, true}, {"D23-complet", 0, 0, true}, {"D23-incorrect", 0, 1, false}, {"D02-sans-delai", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, document := fundingCandidate(t, tc.name)
			report := fundingReplay(t, tc.name, r)
			raw, err := json.Marshal(report)
			require.NoError(t, err)
			parsed, err := parseFundingFactCheckResponse(string(raw), r.CriticalFacts)
			require.NoError(t, err)
			omissions, contradictions := 0, 0
			for _, f := range parsed.Findings {
				switch f.Kind {
				case "OMISSION":
					omissions++
					require.Equal(t, 6, f.Evidence.Page)
					require.Equal(t, f.Claim, f.Evidence.Excerpt)
					if f.fundingCategory == "DEADLINE" {
						require.Contains(t, f.Claim, "s'engage à fournir")
						require.Contains(t, f.Claim, "six mois suivant la fin")
						require.Contains(t, f.Claim, "phase réalisée")
					}
				case "CONTRADICTION":
					contradictions++
					require.Equal(t, "summary", f.Field)
				}
			}
			require.Equal(t, tc.omissions, omissions)
			require.Equal(t, tc.contradictions, contradictions)

			calls := 0
			t.Setenv("GOOGLE_GEMINI_BASE_URL", "https://fixture.invalid")
			saved := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = saved })
			http.DefaultTransport = workerMigrationTransport(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "fixture.invalid", req.URL.Host)
				calls++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":%q}]}}]}`, raw)))}, nil
			})
			before, err := factCheckPayload(r)
			require.NoError(t, err)
			original := slices.Clone(r.CriticalFacts)
			summary := r.Summary
			err = verifyFactsWithGemini(context.Background(), "fixture-key", document, r)
			require.Equal(t, 1, calls)
			if !tc.accept {
				require.True(t, isSubstantiveFactCheckRejection(err))
				require.False(t, r.SourceFacts.Reviewed())
				after, _ := factCheckPayload(r)
				require.Equal(t, before, after)
				return
			}
			require.NoError(t, err)
			require.True(t, r.SourceFacts.Reviewed())
			require.Equal(t, summary, r.Summary)
			require.Len(t, r.CriticalFacts, len(original)+tc.omissions)
			require.Equal(t, original, r.CriticalFacts[:len(original)])
			for _, f := range r.CriticalFacts[len(original):] {
				require.Equal(t, "ARTICLE", f.Audience)
				require.Equal(t, "fact_subvention_montant", f.FinancialFactID)
				require.Equal(t, f.Evidence.Excerpt, f.Label)
				require.Equal(t, strings.TrimRight(f.Label, "."), f.Text())
			}
			final, _ := factCheckPayload(r)
			require.Equal(t, digest(final), r.SourceFacts.ReviewedPayloadSHA256)
			if tc.omissions == 0 {
				require.Equal(t, before, final)
			}
			r.FactCheckedAt = "fixture"
			r.FactCheckModel = "fixture"
			h := WorkerHandler{ddb: &mockDDB{}}
			msg := SQSPayload{CouncilID: "fixture", PDFURL: "https://fixture.invalid/document.pdf"}
			require.NoError(t, h.handleRecord(context.Background(), msg, r, "owner"))
			r.Summary += " Altéré."
			require.ErrorContains(t, h.handleRecord(context.Background(), msg, r, "owner"), "matching source fact review")
		})
	}
}

func TestFundingRejectsUnprovedLinksAndUnsafeRepairs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*factCheckResult)
	}{
		{"missing match", func(r *factCheckResult) { r.Funding.Checks[3].Matches = nil }},
		{"unrelated id", func(r *factCheckResult) { r.Funding.Checks[3].Matches[0].FactID = "fact_vote" }},
		{"no evidence for absence", func(r *factCheckResult) {
			r.Funding.Checks[0] = fundingCoverageCheck{Category: "PAYMENT_MODE", Status: "NOT_ESTABLISHED"}
		}},
		{"unsupported deferral", func(r *factCheckResult) {
			r.Funding.Checks[0] = fundingCoverageCheck{Category: "PAYMENT_MODE", Status: "NOT_ESTABLISHED", Reason: "introuvable", Evidence: r.Findings[0].Evidence}
		}},
		{"wrong page", func(r *factCheckResult) { r.Findings[0].Evidence.Page = 5 }},
		{"wrong operation", func(r *factCheckResult) { r.Findings[0].FinancialFactID = "fact_depenses_payees" }},
		{"free paraphrase", func(r *factCheckResult) { r.Findings[0].Claim = "Paiement sous six mois." }},
		{"invented passage", func(r *factCheckResult) {
			r.Findings[0].Claim = "Le paiement sera effectué sous trois mois."
			r.Findings[0].Evidence.Excerpt = r.Findings[0].Claim
		}},
		{"unchecked prose", func(r *factCheckResult) { r.Funding.ProseConsistent = nil }},
		{"prose conflicts", func(r *factCheckResult) { r.Funding.ProseConsistent = ptrBool(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, pdf := fundingCandidate(t, "D23-incomplet")
			before, _ := factCheckPayload(r)
			report := fundingReplay(t, "D23-incomplet", r)
			tc.change(&report)
			raw, err := json.Marshal(report)
			require.NoError(t, err)
			checked, err := parseFundingFactCheckResponse(string(raw), r.CriticalFacts)
			if err == nil {
				err = finishFundingReview(context.Background(), pdf, r, checked)
			}
			require.Error(t, err)
			require.False(t, r.SourceFacts.Reviewed())
			after, _ := factCheckPayload(r)
			require.Equal(t, before, after)
		})
	}
}
