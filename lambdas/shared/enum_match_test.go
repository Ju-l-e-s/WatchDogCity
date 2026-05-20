package shared

import "testing"

func TestMatchBudgetType(t *testing.T) {
	cases := []struct {
		input    string
		wantOK   bool
		wantForm string
	}{
		{"DÉPENSE", true, "DÉPENSE"},
		{"DEPENSE", true, "DÉPENSE"},
		{"depense", true, "DÉPENSE"},
		{"Depense", true, "DÉPENSE"},
		{"Dépense", true, "DÉPENSE"},
		{"RECETTE", true, "RECETTE"},
		{"recette", true, "RECETTE"},
		{"AUCUN", true, "AUCUN"},
		{"CAUTION", true, "CAUTION"},
		{"GIFT", false, ""},
		{"", false, ""},
	}
	for _, c := range cases {
		got, ok := MatchBudgetType(c.input)
		if ok != c.wantOK {
			t.Errorf("MatchBudgetType(%q) ok=%v, want %v", c.input, ok, c.wantOK)
		}
		if got != c.wantForm {
			t.Errorf("MatchBudgetType(%q) = %q, want %q", c.input, got, c.wantForm)
		}
	}
}

func TestMatchTopicTag(t *testing.T) {
	cases := []struct {
		input    string
		wantOK   bool
		wantForm string
	}{
		{"Budget", true, "Budget"},
		{"BUDGET", true, "Budget"},
		{"budget", true, "Budget"},
		{"Éducation", true, "Éducation"},
		{"EDUCATION", true, "Éducation"},
		{"education", true, "Éducation"},
		{"Environnement", true, "Environnement"},
		{"ENVIRONNEMENT", true, "Environnement"},
		{"UNKNOWN_TAG", false, ""},
	}
	for _, c := range cases {
		got, ok := MatchTopicTag(c.input)
		if ok != c.wantOK {
			t.Errorf("MatchTopicTag(%q) ok=%v, want %v", c.input, ok, c.wantOK)
		}
		if got != c.wantForm {
			t.Errorf("MatchTopicTag(%q) = %q, want %q", c.input, got, c.wantForm)
		}
	}
}

func TestMatchClimateImpact(t *testing.T) {
	cases := []struct {
		input    string
		wantOK   bool
		wantForm string
	}{
		{"positif", true, "positif"},
		{"POSITIF", true, "positif"},
		{"neutre", true, "neutre"},
		{"NEUTRE", true, "neutre"},
		{"negatif", true, "negatif"},
		{"NEGATIF", true, "negatif"},
		{"négatif", true, "negatif"},
		{"unknown", false, ""},
	}
	for _, c := range cases {
		got, ok := MatchClimateImpact(c.input)
		if ok != c.wantOK {
			t.Errorf("MatchClimateImpact(%q) ok=%v, want %v", c.input, ok, c.wantOK)
		}
		if got != c.wantForm {
			t.Errorf("MatchClimateImpact(%q) = %q, want %q", c.input, got, c.wantForm)
		}
	}
}
