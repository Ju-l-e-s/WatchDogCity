package shared

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// normalize strips diacritics (NFD → drop Mn category → NFC) and lowercases.
// "DÉPENSE", "DEPENSE", "depense", "Dépense" all normalize to "depense".
func normalize(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, _ := transform.String(t, s)
	return strings.ToLower(out)
}

// MatchTopicTag returns the canonical form of a TopicTag, or ("", false).
func MatchTopicTag(s string) (string, bool) {
	target := normalize(s)
	for _, t := range TopicTags {
		if normalize(t) == target {
			return t, true
		}
	}
	return "", false
}

// MatchBudgetType returns the canonical form of a BudgetType, or ("", false).
func MatchBudgetType(s string) (string, bool) {
	target := normalize(s)
	for _, t := range BudgetTypes {
		if normalize(t) == target {
			return t, true
		}
	}
	return "", false
}

// MatchClimateImpact returns the canonical form of a ClimateImpact, or ("", false).
func MatchClimateImpact(s string) (string, bool) {
	target := normalize(s)
	for _, t := range ClimateImpacts {
		if normalize(t) == target {
			return t, true
		}
	}
	return "", false
}
