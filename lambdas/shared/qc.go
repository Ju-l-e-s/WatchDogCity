package shared

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Severity classifies a QC violation.
type Severity string

const (
	SeverityHigh Severity = "HIGH"
	SeverityWarn Severity = "WARN"
)

// Violation is a single failed assertion emitted by the QC engine.
type Violation struct {
	Rule           string   `json:"rule"`
	Severity       Severity `json:"severity"`
	DeliberationID string   `json:"deliberation_id,omitempty"`
	Field          string   `json:"field,omitempty"`
	Detail         string   `json:"detail"`
}

// QcBudgetBreakdownItem is a single line of a budget breakdown used by QC checks.
type QcBudgetBreakdownItem struct {
	TopicTag string `json:"topic_tag"`
	Label    string `json:"label"`
	Amount   int64  `json:"amount"`
}

// QcAnalysisData holds the analysis_data fields consumed by QC checks.
type QcAnalysisData struct {
	Contexte *string
	Decision *string
	Impacts  *string
}

// DeliberationView is the minimal projection of a deliberation row for QC checks.
// Populated by the Validator Lambda from DynamoDB; never passed to any LLM.
type DeliberationView struct {
	ID              string
	Title           string
	TopicTag        string
	Summary         string
	AnalysisData    QcAnalysisData
	BudgetImpact    int64
	BudgetType      string
	BudgetBreakdown []QcBudgetBreakdownItem
	ClimateImpact   string
	HasVote         bool
	VotePour        *int
	VoteContre      *int
	VoteAbstention  *int
	Disagreements   *string
	IsSubstantial   bool
}

// CouncilView is the minimal projection of a council row for QC checks.
type CouncilView struct {
	CouncilID     string
	TotalPdfs     int64
	ProcessedPdfs int64
	Date          string
}

// Baseline holds historical statistics computed from APPROVED councils.
// Supplied by the Validator; computed with ComputeBaseline.
type Baseline struct {
	NeantRateMean float64
	NeantRateStd  float64
	BudgetMedian  int64
	SampleSize    int
}

// QcConfig holds tuneable thresholds; every field is env-driven in the Validator.
type QcConfig struct {
	// NeantRateCeiling: absolute max share of deliberations with impacts=="Néant". Default 0.65.
	NeantRateCeiling float64
	// NeantRateZMax: max z-score of Néant rate vs baseline before S3 fires. Default 3.0.
	NeantRateZMax float64
	// MaxAbsoluteBudgetEUR: single-deliberation sanity ceiling in EUR. Default 500_000_000.
	MaxAbsoluteBudgetEUR int64
	// MaxVoteTotal: plausibility cap for pour+contre+abstention. Default 60.
	MaxVoteTotal int
	// BreakdownToleranceEUR: allowed |Σamount - budget_impact| discrepancy. Default 1.
	BreakdownToleranceEUR int64
	// MinBaselineSample: min APPROVED councils required to run z-score drift (S3). Default 5.
	MinBaselineSample int
}

// DefaultQcConfig returns QcConfig with documented production defaults.
func DefaultQcConfig() QcConfig {
	return QcConfig{
		NeantRateCeiling:      0.65,
		NeantRateZMax:         3.0,
		MaxAbsoluteBudgetEUR:  500_000_000,
		MaxVoteTotal:          60,
		BreakdownToleranceEUR: 1,
		MinBaselineSample:     5,
	}
}

// Verdict is the final QC decision for a council.
type Verdict struct {
	Status     string      `json:"status"`     // "APPROVED" | "QUARANTINED"
	Violations []Violation `json:"violations"`
}

// HasHigh reports whether any violation has HIGH severity.
func (v Verdict) HasHigh() bool {
	for _, viol := range v.Violations {
		if viol.Severity == SeverityHigh {
			return true
		}
	}
	return false
}

// leakedMarkupRe detects HTML tags, markdown links, and banned CTA phrases.
var leakedMarkupRe = regexp.MustCompile(
	`(?i)(<[^>]+>|\[[^\]]+\]\([^)]+\)|en savoir plus|voir sur le site|→)`,
)

// ValidateDeterministic runs per-deliberation invariants D1–D10.
// Pure function — no AWS calls, no randomness. Same input → same output, every time.
func ValidateDeterministic(_ CouncilView, delibs []DeliberationView) []Violation {
	cfg := DefaultQcConfig()
	var viols []Violation

	for _, d := range delibs {
		id := d.ID

		// ── D1: Enum validity ───────────────────────────────────────────────
		if _, ok := MatchTopicTag(d.TopicTag); !ok {
			viols = append(viols, Violation{
				Rule: "D1_ENUM_TOPIC_TAG", Severity: SeverityHigh,
				DeliberationID: id, Field: "topic_tag",
				Detail: fmt.Sprintf("cannot canonicalize topic_tag %q", d.TopicTag),
			})
		}
		if _, ok := MatchBudgetType(d.BudgetType); !ok {
			viols = append(viols, Violation{
				Rule: "D1_ENUM_BUDGET_TYPE", Severity: SeverityHigh,
				DeliberationID: id, Field: "budget_type",
				Detail: fmt.Sprintf("cannot canonicalize budget_type %q", d.BudgetType),
			})
		}
		if _, ok := MatchClimateImpact(d.ClimateImpact); !ok {
			viols = append(viols, Violation{
				Rule: "D1_ENUM_CLIMATE_IMPACT", Severity: SeverityHigh,
				DeliberationID: id, Field: "climate_impact",
				Detail: fmt.Sprintf("cannot canonicalize climate_impact %q", d.ClimateImpact),
			})
		}

		// ── D2: Budget/AUCUN coherence ──────────────────────────────────────
		if btCanon, ok := MatchBudgetType(d.BudgetType); ok {
			if btCanon == "AUCUN" && d.BudgetImpact != 0 {
				viols = append(viols, Violation{
					Rule: "D2_BUDGET_AUCUN_NONZERO", Severity: SeverityHigh,
					DeliberationID: id, Field: "budget_impact",
					Detail: fmt.Sprintf("budget_type=AUCUN but budget_impact=%d", d.BudgetImpact),
				})
			}
		}

		// ── D3: Budget sign ─────────────────────────────────────────────────
		if d.BudgetImpact < 0 {
			viols = append(viols, Violation{
				Rule: "D3_BUDGET_NEGATIVE", Severity: SeverityHigh,
				DeliberationID: id, Field: "budget_impact",
				Detail: fmt.Sprintf("budget_impact=%d is negative", d.BudgetImpact),
			})
		}
		for j, item := range d.BudgetBreakdown {
			if item.Amount < 0 {
				viols = append(viols, Violation{
					Rule: "D3_BREAKDOWN_NEGATIVE", Severity: SeverityHigh,
					DeliberationID: id, Field: fmt.Sprintf("budget_breakdown[%d].amount", j),
					Detail: fmt.Sprintf("breakdown[%d].amount=%d is negative", j, item.Amount),
				})
			}
		}

		// ── D4: Breakdown accounting (HIGH — promoted from silent WARN) ─────
		if len(d.BudgetBreakdown) > 0 && d.BudgetImpact > 0 {
			var sum int64
			for _, item := range d.BudgetBreakdown {
				sum += item.Amount
			}
			diff := sum - d.BudgetImpact
			if diff < 0 {
				diff = -diff
			}
			if diff > cfg.BreakdownToleranceEUR {
				viols = append(viols, Violation{
					Rule: "D4_BREAKDOWN_MISMATCH", Severity: SeverityHigh,
					DeliberationID: id, Field: "budget_breakdown",
					Detail: fmt.Sprintf("breakdown sum=%d differs from budget_impact=%d (diff=%d > tol=%d)",
						sum, d.BudgetImpact, diff, cfg.BreakdownToleranceEUR),
				})
			}
		}

		// ── D5: Breakdown tags ──────────────────────────────────────────────
		for j, item := range d.BudgetBreakdown {
			if _, ok := MatchTopicTag(item.TopicTag); !ok {
				viols = append(viols, Violation{
					Rule: "D5_BREAKDOWN_TAG", Severity: SeverityHigh,
					DeliberationID: id, Field: fmt.Sprintf("budget_breakdown[%d].topic_tag", j),
					Detail: fmt.Sprintf("cannot canonicalize breakdown topic_tag %q", item.TopicTag),
				})
			}
		}

		// ── D6: Vote coherence ──────────────────────────────────────────────
		hasPour := d.VotePour != nil && *d.VotePour > 0
		hasContre := d.VoteContre != nil && *d.VoteContre > 0
		hasAbst := d.VoteAbstention != nil && *d.VoteAbstention > 0
		anyCounter := hasPour || hasContre || hasAbst

		if !d.HasVote && anyCounter {
			viols = append(viols, Violation{
				Rule: "D6_VOTE_COHERENCE", Severity: SeverityHigh,
				DeliberationID: id, Field: "has_vote",
				Detail: "has_vote=false but vote counters are non-zero",
			})
		}
		if d.HasVote && d.VotePour == nil && d.VoteContre == nil && d.VoteAbstention == nil {
			viols = append(viols, Violation{
				Rule: "D6_VOTE_COHERENCE", Severity: SeverityHigh,
				DeliberationID: id, Field: "has_vote",
				Detail: "has_vote=true but all vote counters are nil",
			})
		}

		// ── D7: Vote sign ───────────────────────────────────────────────────
		if d.VotePour != nil && *d.VotePour < 0 {
			viols = append(viols, Violation{
				Rule: "D7_VOTE_NEGATIVE", Severity: SeverityHigh,
				DeliberationID: id, Field: "vote_pour",
				Detail: fmt.Sprintf("vote_pour=%d is negative", *d.VotePour),
			})
		}
		if d.VoteContre != nil && *d.VoteContre < 0 {
			viols = append(viols, Violation{
				Rule: "D7_VOTE_NEGATIVE", Severity: SeverityHigh,
				DeliberationID: id, Field: "vote_contre",
				Detail: fmt.Sprintf("vote_contre=%d is negative", *d.VoteContre),
			})
		}
		if d.VoteAbstention != nil && *d.VoteAbstention < 0 {
			viols = append(viols, Violation{
				Rule: "D7_VOTE_NEGATIVE", Severity: SeverityHigh,
				DeliberationID: id, Field: "vote_abstention",
				Detail: fmt.Sprintf("vote_abstention=%d is negative", *d.VoteAbstention),
			})
		}

		// ── D8: Required text ───────────────────────────────────────────────
		if strings.TrimSpace(d.Title) == "" {
			viols = append(viols, Violation{
				Rule: "D8_REQUIRED_TEXT", Severity: SeverityHigh,
				DeliberationID: id, Field: "title",
				Detail: "title is empty or whitespace",
			})
		}
		if strings.TrimSpace(d.Summary) == "" {
			viols = append(viols, Violation{
				Rule: "D8_REQUIRED_TEXT", Severity: SeverityHigh,
				DeliberationID: id, Field: "summary",
				Detail: "summary is empty or whitespace",
			})
		}
		if d.AnalysisData.Decision == nil || strings.TrimSpace(*d.AnalysisData.Decision) == "" {
			viols = append(viols, Violation{
				Rule: "D8_REQUIRED_TEXT", Severity: SeverityHigh,
				DeliberationID: id, Field: "analysis_data.decision",
				Detail: "analysis_data.decision is nil or empty",
			})
		}

		// ── D9: Impacts present ─────────────────────────────────────────────
		if d.AnalysisData.Impacts == nil {
			viols = append(viols, Violation{
				Rule: "D9_IMPACTS_ABSENT", Severity: SeverityHigh,
				DeliberationID: id, Field: "analysis_data.impacts",
				Detail: "analysis_data.impacts is nil",
			})
		} else {
			imp := strings.TrimSpace(*d.AnalysisData.Impacts)
			if imp == "" || imp == "null" {
				viols = append(viols, Violation{
					Rule: "D9_IMPACTS_ABSENT", Severity: SeverityHigh,
					DeliberationID: id, Field: "analysis_data.impacts",
					Detail: fmt.Sprintf("analysis_data.impacts=%q is invalid (must be 'Néant' or substantive content)", imp),
				})
			}
		}

		// ── D10: Leaked markup (WARN) ───────────────────────────────────────
		textsToCheck := []struct{ field, val string }{
			{"summary", d.Summary},
		}
		if d.AnalysisData.Contexte != nil {
			textsToCheck = append(textsToCheck, struct{ field, val string }{"analysis_data.contexte", *d.AnalysisData.Contexte})
		}
		if d.AnalysisData.Decision != nil {
			textsToCheck = append(textsToCheck, struct{ field, val string }{"analysis_data.decision", *d.AnalysisData.Decision})
		}
		if d.AnalysisData.Impacts != nil {
			textsToCheck = append(textsToCheck, struct{ field, val string }{"analysis_data.impacts", *d.AnalysisData.Impacts})
		}
		for _, tc := range textsToCheck {
			if leakedMarkupRe.MatchString(tc.val) {
				viols = append(viols, Violation{
					Rule: "D10_LEAKED_MARKUP", Severity: SeverityWarn,
					DeliberationID: id, Field: tc.field,
					Detail: fmt.Sprintf("%s contains HTML, markdown link, or banned phrase", tc.field),
				})
				break // one WARN per deliberation is sufficient
			}
		}
	}

	return viols
}

// ValidateStatistical runs council-level and drift-vs-baseline checks S1–S6.
// Pure function — no AWS calls, no randomness.
func ValidateStatistical(c CouncilView, delibs []DeliberationView, base Baseline, cfg QcConfig) []Violation {
	var viols []Violation
	n := len(delibs)

	// ── S1: Empty council ───────────────────────────────────────────────────
	if c.ProcessedPdfs > 0 && n == 0 {
		viols = append(viols, Violation{
			Rule: "S1_EMPTY_COUNCIL", Severity: SeverityHigh,
			Detail: fmt.Sprintf("processed_pdfs=%d but council has no deliberations", c.ProcessedPdfs),
		})
		return viols
	}
	if n == 0 {
		return viols
	}

	// Compute Néant rate for S2/S3
	var neantCount int
	for _, d := range delibs {
		if d.AnalysisData.Impacts != nil && *d.AnalysisData.Impacts == "Néant" {
			neantCount++
		}
	}
	neantRate := float64(neantCount) / float64(n)

	// ── S2: Néant absolute ceiling ──────────────────────────────────────────
	if neantRate > cfg.NeantRateCeiling {
		viols = append(viols, Violation{
			Rule: "S2_NEANT_CEILING", Severity: SeverityHigh,
			Detail: fmt.Sprintf("Néant rate=%.2f exceeds ceiling=%.2f (%d/%d deliberations)",
				neantRate, cfg.NeantRateCeiling, neantCount, n),
		})
	}

	// ── S3: Néant drift (WARN) ──────────────────────────────────────────────
	if base.SampleSize >= cfg.MinBaselineSample && base.NeantRateStd > 0 {
		z := (neantRate - base.NeantRateMean) / base.NeantRateStd
		if z > cfg.NeantRateZMax {
			viols = append(viols, Violation{
				Rule: "S3_NEANT_DRIFT", Severity: SeverityWarn,
				Detail: fmt.Sprintf(
					"Néant rate z-score=%.2f exceeds max=%.2f (rate=%.2f, baseline mean=%.2f std=%.2f)",
					z, cfg.NeantRateZMax, neantRate, base.NeantRateMean, base.NeantRateStd),
			})
		}
	}

	// ── S4: Tag collapse (WARN) — all delibs share one tag, council has ≥4 ──
	if n >= 4 {
		tagCounts := make(map[string]int)
		for _, d := range delibs {
			tagCounts[d.TopicTag]++
		}
		if len(tagCounts) == 1 {
			for tag := range tagCounts {
				viols = append(viols, Violation{
					Rule: "S4_TAG_COLLAPSE", Severity: SeverityWarn,
					Detail: fmt.Sprintf("all %d deliberations share topic_tag=%q", n, tag),
				})
			}
		}
	}

	// ── S5: Budget outlier ──────────────────────────────────────────────────
	for _, d := range delibs {
		if d.BudgetImpact > cfg.MaxAbsoluteBudgetEUR {
			viols = append(viols, Violation{
				Rule: "S5_BUDGET_OUTLIER", Severity: SeverityHigh,
				DeliberationID: d.ID, Field: "budget_impact",
				Detail: fmt.Sprintf("budget_impact=%d exceeds ceiling=%d", d.BudgetImpact, cfg.MaxAbsoluteBudgetEUR),
			})
		}
	}

	// ── S6: Vote plausibility ───────────────────────────────────────────────
	for _, d := range delibs {
		var total int
		if d.VotePour != nil {
			total += *d.VotePour
		}
		if d.VoteContre != nil {
			total += *d.VoteContre
		}
		if d.VoteAbstention != nil {
			total += *d.VoteAbstention
		}
		if total > cfg.MaxVoteTotal {
			viols = append(viols, Violation{
				Rule: "S6_VOTE_IMPLAUSIBLE", Severity: SeverityHigh,
				DeliberationID: d.ID, Field: "vote_total",
				Detail: fmt.Sprintf("pour+contre+abstention=%d exceeds max=%d", total, cfg.MaxVoteTotal),
			})
		}
	}

	return viols
}

// Decide returns QUARANTINED iff any violation is HIGH; otherwise APPROVED.
// Deterministic: same violations → same verdict, every time.
func Decide(violations []Violation) Verdict {
	v := Verdict{Status: "APPROVED", Violations: violations}
	for _, viol := range violations {
		if viol.Severity == SeverityHigh {
			v.Status = "QUARANTINED"
			return v
		}
	}
	return v
}

// ComputeBaseline derives statistical baseline from a slice of Néant rates and
// council budget totals collected from historically APPROVED councils.
// Called by the Validator Lambda before running ValidateStatistical.
func ComputeBaseline(neantRates []float64, budgets []int64) Baseline {
	n := len(neantRates)
	if n == 0 {
		return Baseline{}
	}

	var sum float64
	for _, r := range neantRates {
		sum += r
	}
	mean := sum / float64(n)

	var variance float64
	for _, r := range neantRates {
		d := r - mean
		variance += d * d
	}
	variance /= float64(n)
	std := math.Sqrt(variance)

	var median int64
	if len(budgets) > 0 {
		sorted := make([]int64, len(budgets))
		copy(sorted, budgets)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		mid := len(sorted) / 2
		if len(sorted)%2 == 0 {
			median = (sorted[mid-1] + sorted[mid]) / 2
		} else {
			median = sorted[mid]
		}
	}

	return Baseline{
		NeantRateMean: mean,
		NeantRateStd:  std,
		BudgetMedian:  median,
		SampleSize:    n,
	}
}
