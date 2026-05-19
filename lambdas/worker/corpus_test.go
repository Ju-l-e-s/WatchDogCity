//go:build corpus
// +build corpus

// Gold-corpus accuracy harness. Opt-in: requires `-tags=corpus` and a live
// GEMINI_API_KEY. Walks corpus/manifest.json, calls Gemini on each fixture's
// source.pdf, and enforces structural invariants from expected.json.
//
// Run:   GEMINI_API_KEY=... go test -tags=corpus -run TestCorpusAccuracy -v ./...
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type expectedFields struct {
	TitleContains     []string `json:"title_contains"`
	TopicTag          string   `json:"topic_tag"`
	BudgetType        string   `json:"budget_type"`
	BudgetImpactMin   *int64   `json:"budget_impact_min"`
	BudgetImpactMax   *int64   `json:"budget_impact_max"`
	ClimateImpact     string   `json:"climate_impact"`
	HasVote           *bool    `json:"has_vote"`
	IsSubstantial     *bool    `json:"is_substantial"`
	ImpactsIsNeant    *bool    `json:"impacts_is_neant"`
	MinBreakdownItems *int     `json:"min_breakdown_items"`
}

type manifest struct {
	Fixtures []struct {
		Slug        string   `json:"slug"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	} `json:"fixtures"`
}

func TestCorpusAccuracy(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set — skipping corpus accuracy run")
	}

	raw, err := os.ReadFile("corpus/manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	realFixtures := 0
	for _, f := range m.Fixtures {
		if strings.HasPrefix(f.Slug, "_") {
			continue
		}
		realFixtures++
		t.Run(f.Slug, func(t *testing.T) {
			runFixture(t, apiKey, f.Slug)
		})
	}

	if realFixtures == 0 {
		t.Skip("manifest contains only template fixtures — add real PDFs to corpus/fixtures/<slug>/")
	}
}

func runFixture(t *testing.T, apiKey, slug string) {
	t.Helper()
	dir := filepath.Join("corpus", "fixtures", slug)

	pdfBytes, err := os.ReadFile(filepath.Join(dir, "source.pdf"))
	if err != nil {
		t.Fatalf("read source.pdf: %v", err)
	}

	expRaw, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatalf("read expected.json: %v", err)
	}
	var exp expectedFields
	if err := json.Unmarshal(expRaw, &exp); err != nil {
		t.Fatalf("parse expected.json: %v", err)
	}

	res, err := analyzeWithGemini(context.Background(), apiKey, pdfBytes)
	if err != nil {
		t.Fatalf("gemini call failed: %v", err)
	}

	for _, frag := range exp.TitleContains {
		if !strings.Contains(res.Title, frag) {
			t.Errorf("title %q missing required fragment %q", res.Title, frag)
		}
	}
	if exp.TopicTag != "" && res.TopicTag != exp.TopicTag {
		t.Errorf("topic_tag = %q, want %q", res.TopicTag, exp.TopicTag)
	}
	if exp.BudgetType != "" && res.BudgetType != exp.BudgetType {
		t.Errorf("budget_type = %q, want %q", res.BudgetType, exp.BudgetType)
	}
	if exp.BudgetImpactMin != nil && res.BudgetImpact < *exp.BudgetImpactMin {
		t.Errorf("budget_impact = %d, want >= %d", res.BudgetImpact, *exp.BudgetImpactMin)
	}
	if exp.BudgetImpactMax != nil && res.BudgetImpact > *exp.BudgetImpactMax {
		t.Errorf("budget_impact = %d, want <= %d", res.BudgetImpact, *exp.BudgetImpactMax)
	}
	if exp.ClimateImpact != "" && res.ClimateImpact != exp.ClimateImpact {
		t.Errorf("climate_impact = %q, want %q", res.ClimateImpact, exp.ClimateImpact)
	}
	if exp.HasVote != nil && res.Vote.HasVote != *exp.HasVote {
		t.Errorf("vote.has_vote = %v, want %v", res.Vote.HasVote, *exp.HasVote)
	}
	if exp.IsSubstantial != nil && res.IsSubstantial != *exp.IsSubstantial {
		t.Errorf("is_substantial = %v, want %v", res.IsSubstantial, *exp.IsSubstantial)
	}
	if exp.ImpactsIsNeant != nil {
		got := res.AnalysisData.Impacts != nil && *res.AnalysisData.Impacts == "Néant"
		if got != *exp.ImpactsIsNeant {
			t.Errorf("impacts_is_neant = %v, want %v (impacts=%v)", got, *exp.ImpactsIsNeant, res.AnalysisData.Impacts)
		}
	}
	if exp.MinBreakdownItems != nil && len(res.BudgetBreakdown) < *exp.MinBreakdownItems {
		t.Errorf("budget_breakdown len = %d, want >= %d", len(res.BudgetBreakdown), *exp.MinBreakdownItems)
	}

	t.Logf("OK: %s → topic=%s budget=%d€ type=%s climate=%s vote=%v",
		slug, res.TopicTag, res.BudgetImpact, res.BudgetType, res.ClimateImpact, res.Vote.HasVote)
}
