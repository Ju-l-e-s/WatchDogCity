package main

import (
	"fmt"

	"github.com/watchdog/shared"
)

// projectBudgetNote replaces the editable copy for new extractions before it
// is checked against the PDF. The historic amount stays in whole euros.
func projectBudgetNote(r *GeminiResult) error {
	if r.BudgetImpact <= 0 {
		r.BudgetNote = ""
		return nil
	}
	budgetType, ok := shared.MatchBudgetType(r.BudgetType)
	if !ok || budgetType == "AUCUN" {
		return nil // validateGeminiResult reports the inconsistent legacy fields.
	}
	var primary *shared.SourceFact
	for i := range r.CriticalFacts {
		f := &r.CriticalFacts[i]
		m := f.Money
		if m == nil || *m.Cents < 0 || (*m.Cents+50)/100 != r.BudgetImpact ||
			!containsValue([]string{"MONTANT", "PLAFOND", "TOTAL"}, m.Role) ||
			!matchesBudgetType(m.Category, budgetType) {
			continue
		}
		if primary != nil {
			return fmt.Errorf("%w: budget_note has several possible principal amounts", errFactCheckReview)
		}
		primary = f
	}
	if primary == nil {
		return fmt.Errorf("%w: no corresponding structured principal amount for budget_note", errFactCheckReview)
	}
	label := map[string]string{"DÉPENSE": "Dépense", "RECETTE": "Recette", "CAUTION": "Garantie"}[budgetType]
	switch primary.Money.Category {
	case "SUBVENTION":
		label = "Subvention"
	case "CAUTION":
		label = "Garantie"
	case "AUTRE":
		label = "Montant"
	}
	// Use the linked fact's exact amount, period and basis. Conditions and
	// components retain their own facts and evidence.
	fact := *primary
	fact.Label = label
	r.BudgetNote = fact.Text() + "."
	return nil
}

func matchesBudgetType(category, budgetType string) bool {
	switch category {
	case "RECETTE":
		return budgetType == "RECETTE"
	case "DEPENSE":
		return budgetType == "DÉPENSE"
	case "CAUTION":
		return budgetType == "CAUTION"
	case "SUBVENTION", "AUTRE":
		return budgetType == "RECETTE" || budgetType == "DÉPENSE"
	default:
		return false
	}
}
