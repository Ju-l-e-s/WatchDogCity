package shared

import (
	"fmt"
	"strings"
)

func civicPriority(d ColdDeliberation) bool {
	if d.SourceFacts != nil {
		return d.SourceFacts.HasKind("CIVIC_SCOPE")
	}
	// Existing approved deliberations have no structured civic scope. Use
	// the subject of the municipal census, without inferring scope from votes.
	title := strings.ToLower(d.Title)
	return strings.Contains(title, "recensement") &&
		(strings.Contains(title, "population") || strings.Contains(title, "agents recenseurs") ||
			strings.Contains(strings.ToLower(d.Decision+" "+d.Summary), "recensement de la population"))
}

func nonUnanimousNewsletterItem(d ColdDeliberation) bool {
	return (d.Contre != nil && *d.Contre > 0) ||
		(d.Abstention != nil && *d.Abstention > 0 && d.HasDisagreement)
}

func newsletterFactLines(d ColdDeliberation, section string) []string {
	var lines []string
	if d.SourceFacts.Reviewed() {
		for _, fact := range d.SourceFacts.Facts {
			if fact.Audience == "BRIEF" || (section != "briefs" && fact.Audience == "ARTICLE") {
				lines = append(lines, fact.Text())
			}
		}
		return lines
	}
	// A legacy record can still supply exact AP/CP adjustments and recorded
	// votes. No inferred conditions or source locations are added.
	if section == "briefs" || (section == "adopted" && d.Abstention != nil && *d.Abstention > 0) {
		if len(d.BudgetChanges) > 0 && section == "briefs" {
			lines = append(lines, formatBudgetChangesFR(d.BudgetChanges))
		}
		if (d.Contre != nil && *d.Contre > 0) || (d.Abstention != nil && *d.Abstention > 0) {
			lines = append(lines, (SourceFact{Label: "Vote du conseil",
				Vote: &SourceVote{For: d.Pour, Against: d.Contre, Abstain: d.Abstention}}).Text())
		}
	}
	return lines
}

func addNewsletterFactLines(text string, lines []string) string {
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || containsNewsletterFactLine(text, line) {
			continue
		}
		if text != "" {
			if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "!") && !strings.HasSuffix(text, "?") {
				text += "."
			}
			text += " "
		}
		text += line + "."
	}
	return text
}

// A reviewed brief has one source for its wording. Use the short decision and
// all essential facts instead of appending them to a second generated summary.
func reviewedBrief(d ColdDeliberation) (string, bool) {
	if !d.SourceFacts.Reviewed() {
		return "", false
	}
	required := make(map[string]SourceFact)
	var hasDecision bool
	for _, f := range d.SourceFacts.Facts {
		if f.Audience == "BRIEF" {
			required[f.ID] = f
			hasDecision = hasDecision || f.Kind == "DECISION"
		}
	}
	if !hasDecision {
		return "", false
	}
	groups, grouped := groupBriefTotals(d.SourceFacts.Facts, required)
	var lines []string
	seen := make(map[string]bool)
	for _, f := range d.SourceFacts.Facts {
		line, isTotal := groups[f.ID]
		if !isTotal {
			if f.Audience != "BRIEF" || grouped[f.ID] {
				continue
			}
			line = f.Text()
		}
		if line != "" && !seen[line] {
			lines = append(lines, line)
			seen[line] = true
		}
	}
	return strings.Join(lines, ". ") + ".", true
}

// A printed total can accompany its required components when the reviewed
// packet explicitly links them and the partition is exact.
func groupBriefTotals(facts []SourceFact, required map[string]SourceFact) (map[string]string, map[string]bool) {
	totals := make(map[string]string)
	grouped := make(map[string]bool)
	for _, f := range facts {
		m := f.Money
		if f.ID == "" || m == nil || m.Cents == nil || m.Role != "TOTAL" || f.Audience == "EXTRACTION" || len(m.TotalOf) < 2 {
			continue
		}
		var components []string
		var sum int64
		seen := make(map[string]bool)
		for _, id := range m.TotalOf {
			part := required[id]
			p := part.Money
			if id == "" || seen[id] || grouped[id] || p == nil || p.Cents == nil || p.Role != "COMPOSANTE" ||
				p.Category != m.Category || p.Period != m.Period || p.Basis != m.Basis || part.Status != f.Status {
				break
			}
			seen[id] = true
			sum += *p.Cents
			components = append(components, strings.TrimRight(strings.TrimSpace(part.Label), ".")+" ("+formatSourceCents(*p.Cents)+")")
		}
		if len(components) != len(m.TotalOf) || sum != *m.Cents {
			continue
		}
		totals[f.ID] = f.Text() + ", dont " + strings.Join(components, " et ")
		for _, id := range m.TotalOf {
			grouped[id] = true
		}
	}
	return totals, grouped
}

// Search for the complete rendered clause, so that an approximate amount or
// a number appearing in a different context does not satisfy coverage.
func containsNewsletterFactLine(text, line string) bool {
	return strings.Contains(" "+strings.TrimSpace(text)+".", " "+line+".")
}

func validateNewsletterCoverage(params *NewsletterParams, cold []ColdDeliberation) error {
	sources := newsletterSources(cold)
	var issues []NewsletterFactIssue
	check := func(section, sourceID, text string, index int) {
		for _, line := range newsletterFactLines(sources[sourceID], section) {
			if !containsNewsletterFactLine(text, line) {
				issues = append(issues, NewsletterFactIssue{Kind: "OMISSION", Location: fmt.Sprintf("%s[%d]", section, index),
					Claim: line, Reason: "Fait essentiel manquant dans le texte final pour " + sourceID + "."})
			}
		}
	}
	for i, item := range params.Tensions {
		check("tensions", item.SourceID, item.Context, i)
	}
	for i, item := range params.Adopted {
		check("adopted", item.SourceID, item.Context, i)
	}
	for i, item := range params.Briefs {
		if canonical, ok := reviewedBrief(sources[item.SourceID]); ok {
			if item.Summary != canonical {
				issues = append(issues, NewsletterFactIssue{Kind: "OMISSION", Location: fmt.Sprintf("briefs[%d]", i),
					Claim: canonical, Reason: "Les faits essentiels de la brève ont été modifiés."})
			}
			continue
		}
		check("briefs", item.SourceID, item.Summary, i)
	}
	if len(issues) != 0 {
		return &NewsletterFactCheckError{Issues: issues}
	}
	return nil
}

func newsletterSources(cold []ColdDeliberation) map[string]ColdDeliberation {
	sources := make(map[string]ColdDeliberation, len(cold))
	for _, d := range cold {
		sources[d.ID] = d
	}
	return sources
}
