package shared

import (
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func ptrInt(i int) *int       { return &i }
func ptrStr(s string) *string { return &s }

// okDelib returns a fully-valid DeliberationView with all required fields.
func okDelib(id string) DeliberationView {
	dec := "La ville approuve le projet."
	imp := "Néant"
	ctx := "Contexte factuel."
	return DeliberationView{
		ID:            id,
		Title:         "Délibération " + id,
		Summary:       "Résumé court.",
		TopicTag:      "Budget",
		BudgetType:    "DÉPENSE",
		BudgetImpact:  1000,
		ClimateImpact: "neutre",
		HasVote:       true,
		VotePour:      ptrInt(35),
		VoteContre:    ptrInt(0),
		VoteAbstention: ptrInt(0),
		AnalysisData: QcAnalysisData{
			Contexte: &ctx,
			Decision: &dec,
			Impacts:  &imp,
		},
	}
}

func okCouncil() CouncilView {
	return CouncilView{CouncilID: "c1", TotalPdfs: 3, ProcessedPdfs: 3}
}

func hasRule(viols []Violation, rule string) bool {
	for _, v := range viols {
		if v.Rule == rule {
			return true
		}
	}
	return false
}

func hasRuleAndSev(viols []Violation, rule string, sev Severity) bool {
	for _, v := range viols {
		if v.Rule == rule && v.Severity == sev {
			return true
		}
	}
	return false
}

// ── D1: Enum validity ─────────────────────────────────────────────────────────

func TestD1_Pass(t *testing.T) {
	d := okDelib("d1")
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D1_ENUM_TOPIC_TAG") || hasRule(viols, "D1_ENUM_BUDGET_TYPE") || hasRule(viols, "D1_ENUM_CLIMATE_IMPACT") {
		t.Errorf("expected no D1 violation for valid enums, got %v", viols)
	}
}

func TestD1_InvalidTopicTag(t *testing.T) {
	d := okDelib("d1")
	d.TopicTag = "CategorieFictive"
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D1_ENUM_TOPIC_TAG", SeverityHigh) {
		t.Errorf("expected D1_ENUM_TOPIC_TAG HIGH, got %v", viols)
	}
}

func TestD1_InvalidBudgetType(t *testing.T) {
	d := okDelib("d1")
	d.BudgetType = "REMBOURSEMENT"
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D1_ENUM_BUDGET_TYPE", SeverityHigh) {
		t.Errorf("expected D1_ENUM_BUDGET_TYPE HIGH, got %v", viols)
	}
}

func TestD1_InvalidClimateImpact(t *testing.T) {
	d := okDelib("d1")
	d.ClimateImpact = "incertain"
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D1_ENUM_CLIMATE_IMPACT", SeverityHigh) {
		t.Errorf("expected D1_ENUM_CLIMATE_IMPACT HIGH, got %v", viols)
	}
}

func TestD1_AccentDrift_StillValid(t *testing.T) {
	d := okDelib("d1")
	d.BudgetType = "DEPENSE" // no accent — should normalize
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D1_ENUM_BUDGET_TYPE") {
		t.Errorf("DEPENSE without accent should canonicalize, got %v", viols)
	}
}

// ── D2: Budget/AUCUN coherence ────────────────────────────────────────────────

func TestD2_Pass(t *testing.T) {
	d := okDelib("d2")
	d.BudgetType = "AUCUN"
	d.BudgetImpact = 0
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D2_BUDGET_AUCUN_NONZERO") {
		t.Errorf("AUCUN+0 must not trigger D2, got %v", viols)
	}
}

func TestD2_AucunNonzero(t *testing.T) {
	d := okDelib("d2")
	d.BudgetType = "AUCUN"
	d.BudgetImpact = 5000
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D2_BUDGET_AUCUN_NONZERO", SeverityHigh) {
		t.Errorf("expected D2_BUDGET_AUCUN_NONZERO HIGH, got %v", viols)
	}
}

// ── D3: Budget sign ───────────────────────────────────────────────────────────

func TestD3_Pass(t *testing.T) {
	d := okDelib("d3")
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D3_BUDGET_NEGATIVE") && hasRule(viols, "D3_BREAKDOWN_NEGATIVE") {
		t.Errorf("expected no D3 for positive budget, got %v", viols)
	}
}

func TestD3_NegativeBudget(t *testing.T) {
	d := okDelib("d3")
	d.BudgetImpact = -1000
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D3_BUDGET_NEGATIVE", SeverityHigh) {
		t.Errorf("expected D3_BUDGET_NEGATIVE HIGH, got %v", viols)
	}
}

func TestD3_NegativeBreakdown(t *testing.T) {
	d := okDelib("d3")
	d.BudgetImpact = 1000
	d.BudgetBreakdown = []QcBudgetBreakdownItem{{TopicTag: "Budget", Label: "test", Amount: -500}}
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D3_BREAKDOWN_NEGATIVE", SeverityHigh) {
		t.Errorf("expected D3_BREAKDOWN_NEGATIVE HIGH, got %v", viols)
	}
}

// ── D4: Breakdown accounting ──────────────────────────────────────────────────

func TestD4_Pass(t *testing.T) {
	d := okDelib("d4")
	d.BudgetImpact = 1000
	d.BudgetBreakdown = []QcBudgetBreakdownItem{
		{TopicTag: "Budget", Label: "a", Amount: 600},
		{TopicTag: "Sport", Label: "b", Amount: 400},
	}
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D4_BREAKDOWN_MISMATCH") {
		t.Errorf("sum==budget must not trigger D4, got %v", viols)
	}
}

func TestD4_BreakdownMismatch(t *testing.T) {
	d := okDelib("d4")
	d.BudgetImpact = 1000
	d.BudgetBreakdown = []QcBudgetBreakdownItem{
		{TopicTag: "Budget", Label: "a", Amount: 600},
		{TopicTag: "Sport", Label: "b", Amount: 300}, // sum=900, diff=100 > tol=1
	}
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D4_BREAKDOWN_MISMATCH", SeverityHigh) {
		t.Errorf("expected D4_BREAKDOWN_MISMATCH HIGH, got %v", viols)
	}
}

func TestD4_WithinTolerance(t *testing.T) {
	d := okDelib("d4")
	d.BudgetImpact = 1000
	d.BudgetBreakdown = []QcBudgetBreakdownItem{
		{TopicTag: "Budget", Label: "a", Amount: 1001}, // diff=1, within tol=1
	}
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D4_BREAKDOWN_MISMATCH") {
		t.Errorf("diff==tol must not trigger D4, got %v", viols)
	}
}

// ── D5: Breakdown tags ────────────────────────────────────────────────────────

func TestD5_Pass(t *testing.T) {
	d := okDelib("d5")
	d.BudgetImpact = 1000
	d.BudgetBreakdown = []QcBudgetBreakdownItem{
		{TopicTag: "Sport", Label: "club", Amount: 1000},
	}
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D5_BREAKDOWN_TAG") {
		t.Errorf("valid tag must not trigger D5, got %v", viols)
	}
}

func TestD5_InvalidBreakdownTag(t *testing.T) {
	d := okDelib("d5")
	d.BudgetImpact = 500
	d.BudgetBreakdown = []QcBudgetBreakdownItem{
		{TopicTag: "INVALID_TAG", Label: "x", Amount: 500},
	}
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D5_BREAKDOWN_TAG", SeverityHigh) {
		t.Errorf("expected D5_BREAKDOWN_TAG HIGH, got %v", viols)
	}
}

// ── D6: Vote coherence ────────────────────────────────────────────────────────

func TestD6_Pass(t *testing.T) {
	d := okDelib("d6")
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D6_VOTE_COHERENCE") {
		t.Errorf("valid vote must not trigger D6, got %v", viols)
	}
}

func TestD6_FalseVoteWithCounters(t *testing.T) {
	d := okDelib("d6")
	d.HasVote = false
	d.VoteContre = ptrInt(3)
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D6_VOTE_COHERENCE", SeverityHigh) {
		t.Errorf("expected D6_VOTE_COHERENCE HIGH, got %v", viols)
	}
}

func TestD6_TrueVoteAllNil(t *testing.T) {
	d := okDelib("d6")
	d.HasVote = true
	d.VotePour = nil
	d.VoteContre = nil
	d.VoteAbstention = nil
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D6_VOTE_COHERENCE", SeverityHigh) {
		t.Errorf("expected D6_VOTE_COHERENCE HIGH for all-nil counters, got %v", viols)
	}
}

// ── D7: Vote sign ─────────────────────────────────────────────────────────────

func TestD7_Pass(t *testing.T) {
	d := okDelib("d7")
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D7_VOTE_NEGATIVE") {
		t.Errorf("positive votes must not trigger D7, got %v", viols)
	}
}

func TestD7_NegativePour(t *testing.T) {
	d := okDelib("d7")
	d.VotePour = ptrInt(-1)
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D7_VOTE_NEGATIVE", SeverityHigh) {
		t.Errorf("expected D7_VOTE_NEGATIVE HIGH, got %v", viols)
	}
}

func TestD7_NegativeContre(t *testing.T) {
	d := okDelib("d7")
	d.VoteContre = ptrInt(-2)
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D7_VOTE_NEGATIVE", SeverityHigh) {
		t.Errorf("expected D7_VOTE_NEGATIVE HIGH for contre, got %v", viols)
	}
}

// ── D8: Required text ─────────────────────────────────────────────────────────

func TestD8_Pass(t *testing.T) {
	d := okDelib("d8")
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D8_REQUIRED_TEXT") {
		t.Errorf("all required fields present must not trigger D8, got %v", viols)
	}
}

func TestD8_EmptyTitle(t *testing.T) {
	d := okDelib("d8")
	d.Title = "   "
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D8_REQUIRED_TEXT", SeverityHigh) {
		t.Errorf("expected D8_REQUIRED_TEXT HIGH for empty title, got %v", viols)
	}
}

func TestD8_NilDecision(t *testing.T) {
	d := okDelib("d8")
	d.AnalysisData.Decision = nil
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D8_REQUIRED_TEXT", SeverityHigh) {
		t.Errorf("expected D8_REQUIRED_TEXT HIGH for nil decision, got %v", viols)
	}
}

// ── D9: Impacts present ───────────────────────────────────────────────────────

func TestD9_Pass_Neant(t *testing.T) {
	d := okDelib("d9")
	d.AnalysisData.Impacts = ptrStr("Néant")
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D9_IMPACTS_ABSENT") {
		t.Errorf("'Néant' must not trigger D9, got %v", viols)
	}
}

func TestD9_Pass_Content(t *testing.T) {
	d := okDelib("d9")
	d.AnalysisData.Impacts = ptrStr("Impact direct sur les habitants.")
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D9_IMPACTS_ABSENT") {
		t.Errorf("substantive content must not trigger D9, got %v", viols)
	}
}

func TestD9_NilImpacts(t *testing.T) {
	d := okDelib("d9")
	d.AnalysisData.Impacts = nil
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D9_IMPACTS_ABSENT", SeverityHigh) {
		t.Errorf("expected D9_IMPACTS_ABSENT HIGH for nil impacts, got %v", viols)
	}
}

func TestD9_LiteralNull(t *testing.T) {
	d := okDelib("d9")
	d.AnalysisData.Impacts = ptrStr("null")
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D9_IMPACTS_ABSENT", SeverityHigh) {
		t.Errorf("expected D9_IMPACTS_ABSENT HIGH for literal 'null', got %v", viols)
	}
}

// ── D10: Leaked markup (WARN) ─────────────────────────────────────────────────

func TestD10_Pass(t *testing.T) {
	d := okDelib("d10")
	d.Summary = "Un résumé propre sans lien."
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if hasRule(viols, "D10_LEAKED_MARKUP") {
		t.Errorf("clean text must not trigger D10, got %v", viols)
	}
}

func TestD10_HTMLInSummary(t *testing.T) {
	d := okDelib("d10")
	d.Summary = "Voir <a href='x'>ici</a> pour les détails."
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D10_LEAKED_MARKUP", SeverityWarn) {
		t.Errorf("expected D10_LEAKED_MARKUP WARN for HTML, got %v", viols)
	}
}

func TestD10_EnSavoirPlus(t *testing.T) {
	d := okDelib("d10")
	d.Summary = "Budget voté. En savoir plus sur le site."
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D10_LEAKED_MARKUP", SeverityWarn) {
		t.Errorf("expected D10_LEAKED_MARKUP WARN for 'En savoir plus', got %v", viols)
	}
}

func TestD10_MarkdownLink(t *testing.T) {
	d := okDelib("d10")
	imp := "Voir [le site](https://example.com) pour plus d'infos."
	d.AnalysisData.Impacts = &imp
	viols := ValidateDeterministic(okCouncil(), []DeliberationView{d})
	if !hasRuleAndSev(viols, "D10_LEAKED_MARKUP", SeverityWarn) {
		t.Errorf("expected D10_LEAKED_MARKUP WARN for markdown link, got %v", viols)
	}
}

// ── S1: Empty council ─────────────────────────────────────────────────────────

func TestS1_Pass(t *testing.T) {
	cfg := DefaultQcConfig()
	c := CouncilView{ProcessedPdfs: 3}
	delibs := []DeliberationView{okDelib("s1a"), okDelib("s1b")}
	viols := ValidateStatistical(c, delibs, Baseline{}, cfg)
	if hasRule(viols, "S1_EMPTY_COUNCIL") {
		t.Errorf("non-empty council must not trigger S1, got %v", viols)
	}
}

func TestS1_EmptyCouncil(t *testing.T) {
	cfg := DefaultQcConfig()
	c := CouncilView{ProcessedPdfs: 5}
	viols := ValidateStatistical(c, nil, Baseline{}, cfg)
	if !hasRuleAndSev(viols, "S1_EMPTY_COUNCIL", SeverityHigh) {
		t.Errorf("expected S1_EMPTY_COUNCIL HIGH, got %v", viols)
	}
}

// ── S2: Néant absolute ceiling ────────────────────────────────────────────────

func TestS2_Pass(t *testing.T) {
	cfg := DefaultQcConfig()
	delibs := make([]DeliberationView, 4)
	neant := "Néant"
	content := "Impact citoyen."
	for i := range delibs {
		delibs[i] = okDelib("s2" + string(rune('a'+i)))
		if i < 2 {
			delibs[i].AnalysisData.Impacts = &neant
		} else {
			delibs[i].AnalysisData.Impacts = &content
		}
	}
	// 2/4 = 0.50 < 0.65 ceiling
	viols := ValidateStatistical(okCouncil(), delibs, Baseline{}, cfg)
	if hasRule(viols, "S2_NEANT_CEILING") {
		t.Errorf("rate=0.50 must not trigger S2 (ceiling=0.65), got %v", viols)
	}
}

func TestS2_CeilingExceeded(t *testing.T) {
	cfg := DefaultQcConfig()
	delibs := make([]DeliberationView, 4)
	neant := "Néant"
	for i := range delibs {
		delibs[i] = okDelib("s2" + string(rune('a'+i)))
		delibs[i].AnalysisData.Impacts = &neant // 4/4 = 1.0 > 0.65
	}
	viols := ValidateStatistical(okCouncil(), delibs, Baseline{}, cfg)
	if !hasRuleAndSev(viols, "S2_NEANT_CEILING", SeverityHigh) {
		t.Errorf("expected S2_NEANT_CEILING HIGH, got %v", viols)
	}
}

// ── S3: Néant drift (WARN) ────────────────────────────────────────────────────

func TestS3_SkipsBelowMinSample(t *testing.T) {
	cfg := DefaultQcConfig() // MinBaselineSample=5
	neant := "Néant"
	delibs := []DeliberationView{okDelib("s3")}
	delibs[0].AnalysisData.Impacts = &neant

	base := Baseline{NeantRateMean: 0.0, NeantRateStd: 0.05, SampleSize: 4} // below MinBaselineSample
	viols := ValidateStatistical(okCouncil(), delibs, base, cfg)
	if hasRule(viols, "S3_NEANT_DRIFT") {
		t.Errorf("S3 must be skipped when SampleSize < MinBaselineSample, got %v", viols)
	}
}

func TestS3_DriftExceedsMax(t *testing.T) {
	cfg := DefaultQcConfig()
	neant := "Néant"
	delibs := make([]DeliberationView, 10)
	for i := range delibs {
		delibs[i] = okDelib("s3" + string(rune('a'+i)))
		delibs[i].AnalysisData.Impacts = &neant // rate=1.0
	}
	// baseline mean=0.1, std=0.05 → z=(1.0-0.1)/0.05=18 > 3.0
	base := Baseline{NeantRateMean: 0.1, NeantRateStd: 0.05, SampleSize: 10}
	viols := ValidateStatistical(okCouncil(), delibs, base, cfg)
	if !hasRuleAndSev(viols, "S3_NEANT_DRIFT", SeverityWarn) {
		t.Errorf("expected S3_NEANT_DRIFT WARN, got %v", viols)
	}
}

// ── S4: Tag collapse ──────────────────────────────────────────────────────────

func TestS4_Pass(t *testing.T) {
	cfg := DefaultQcConfig()
	delibs := []DeliberationView{okDelib("s4a"), okDelib("s4b"), okDelib("s4c")}
	delibs[1].TopicTag = "Sport"
	delibs[2].TopicTag = "Culture"
	viols := ValidateStatistical(okCouncil(), delibs, Baseline{}, cfg)
	if hasRule(viols, "S4_TAG_COLLAPSE") {
		t.Errorf("diverse tags must not trigger S4, got %v", viols)
	}
}

func TestS4_CollapseTriggered(t *testing.T) {
	cfg := DefaultQcConfig()
	delibs := make([]DeliberationView, 4)
	for i := range delibs {
		delibs[i] = okDelib("s4" + string(rune('a'+i)))
		delibs[i].TopicTag = "Budget" // all same
	}
	viols := ValidateStatistical(okCouncil(), delibs, Baseline{}, cfg)
	if !hasRuleAndSev(viols, "S4_TAG_COLLAPSE", SeverityWarn) {
		t.Errorf("expected S4_TAG_COLLAPSE WARN, got %v", viols)
	}
}

func TestS4_OnlyThreeDelibs_NoWarn(t *testing.T) {
	cfg := DefaultQcConfig()
	delibs := []DeliberationView{okDelib("a"), okDelib("b"), okDelib("c")}
	// all Budget, but < 4
	viols := ValidateStatistical(okCouncil(), delibs, Baseline{}, cfg)
	if hasRule(viols, "S4_TAG_COLLAPSE") {
		t.Errorf("S4 must not fire for <4 deliberations, got %v", viols)
	}
}

// ── S5: Budget outlier ────────────────────────────────────────────────────────

func TestS5_Pass(t *testing.T) {
	cfg := DefaultQcConfig()
	d := okDelib("s5")
	d.BudgetImpact = 100_000
	viols := ValidateStatistical(okCouncil(), []DeliberationView{d}, Baseline{}, cfg)
	if hasRule(viols, "S5_BUDGET_OUTLIER") {
		t.Errorf("reasonable budget must not trigger S5, got %v", viols)
	}
}

func TestS5_OutlierDetected(t *testing.T) {
	cfg := DefaultQcConfig()
	d := okDelib("s5")
	d.BudgetImpact = 600_000_000 // > 500M ceiling
	viols := ValidateStatistical(okCouncil(), []DeliberationView{d}, Baseline{}, cfg)
	if !hasRuleAndSev(viols, "S5_BUDGET_OUTLIER", SeverityHigh) {
		t.Errorf("expected S5_BUDGET_OUTLIER HIGH, got %v", viols)
	}
}

// ── S6: Vote plausibility ─────────────────────────────────────────────────────

func TestS6_Pass(t *testing.T) {
	cfg := DefaultQcConfig()
	d := okDelib("s6")
	d.VotePour = ptrInt(35)
	d.VoteContre = ptrInt(3)
	d.VoteAbstention = ptrInt(1)
	viols := ValidateStatistical(okCouncil(), []DeliberationView{d}, Baseline{}, cfg)
	if hasRule(viols, "S6_VOTE_IMPLAUSIBLE") {
		t.Errorf("plausible vote total must not trigger S6, got %v", viols)
	}
}

func TestS6_ImplausibleTotal(t *testing.T) {
	cfg := DefaultQcConfig()
	d := okDelib("s6")
	d.VotePour = ptrInt(50)
	d.VoteContre = ptrInt(20)
	d.VoteAbstention = ptrInt(5) // total=75 > 60
	viols := ValidateStatistical(okCouncil(), []DeliberationView{d}, Baseline{}, cfg)
	if !hasRuleAndSev(viols, "S6_VOTE_IMPLAUSIBLE", SeverityHigh) {
		t.Errorf("expected S6_VOTE_IMPLAUSIBLE HIGH, got %v", viols)
	}
}

// ── Decide ────────────────────────────────────────────────────────────────────

func TestDecide_Empty_APPROVED(t *testing.T) {
	v := Decide(nil)
	if v.Status != "APPROVED" {
		t.Errorf("no violations must yield APPROVED, got %q", v.Status)
	}
}

func TestDecide_OnlyWarn_APPROVED(t *testing.T) {
	viols := []Violation{{Rule: "D10_LEAKED_MARKUP", Severity: SeverityWarn}}
	v := Decide(viols)
	if v.Status != "APPROVED" {
		t.Errorf("only-WARN must yield APPROVED, got %q", v.Status)
	}
}

func TestDecide_OneHigh_QUARANTINED(t *testing.T) {
	viols := []Violation{
		{Rule: "D10_LEAKED_MARKUP", Severity: SeverityWarn},
		{Rule: "D2_BUDGET_AUCUN_NONZERO", Severity: SeverityHigh},
	}
	v := Decide(viols)
	if v.Status != "QUARANTINED" {
		t.Errorf("any HIGH must yield QUARANTINED, got %q", v.Status)
	}
}

func TestDecide_Deterministic(t *testing.T) {
	viols := []Violation{{Rule: "D1_ENUM_TOPIC_TAG", Severity: SeverityHigh, Detail: "x"}}
	v1 := Decide(viols)
	v2 := Decide(viols)
	if v1.Status != v2.Status {
		t.Errorf("Decide must be deterministic: got %q vs %q", v1.Status, v2.Status)
	}
}

// ── S3 with baseline below MinBaselineSample: still runs S2/S5/S6 ─────────────

func TestStatistical_SmallBaseline_StillRunsAbsoluteChecks(t *testing.T) {
	cfg := DefaultQcConfig()
	d := okDelib("abs")
	d.BudgetImpact = 600_000_000 // S5 trigger
	base := Baseline{SampleSize: 2} // below MinBaselineSample

	viols := ValidateStatistical(okCouncil(), []DeliberationView{d}, base, cfg)
	if !hasRule(viols, "S5_BUDGET_OUTLIER") {
		t.Errorf("S5 must fire regardless of baseline sample size, got %v", viols)
	}
	if hasRule(viols, "S3_NEANT_DRIFT") {
		t.Errorf("S3 must be skipped when sample too small, got %v", viols)
	}
}

// ── ComputeBaseline ───────────────────────────────────────────────────────────

func TestComputeBaseline_Empty(t *testing.T) {
	b := ComputeBaseline(nil, nil)
	if b.SampleSize != 0 || b.NeantRateMean != 0 {
		t.Errorf("empty input must yield zero baseline, got %+v", b)
	}
}

func TestComputeBaseline_MedianOdd(t *testing.T) {
	b := ComputeBaseline([]float64{0.1, 0.2, 0.3}, []int64{100, 300, 200})
	if b.BudgetMedian != 200 {
		t.Errorf("median of [100,200,300] must be 200, got %d", b.BudgetMedian)
	}
	if b.SampleSize != 3 {
		t.Errorf("sample size must be 3, got %d", b.SampleSize)
	}
}
