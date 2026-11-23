package shared

import (
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	SourceFactsVersion = 2
	ProofStructural    = "STRUCTURAL"
	ProofModelReviewed = "MODEL_REVIEWED"
)

type PDFPassage struct {
	Page    int    `json:"page" dynamodbav:"page"`
	Excerpt string `json:"excerpt" dynamodbav:"excerpt"`
	Cell    string `json:"cell,omitempty" dynamodbav:"cell,omitempty"`
}

type SourceMoney struct {
	Cents    *int64   `json:"cents" dynamodbav:"cents"`
	Category string   `json:"category" dynamodbav:"category"`
	Role     string   `json:"role" dynamodbav:"role"`
	Basis    string   `json:"basis,omitempty" dynamodbav:"basis,omitempty"`
	Period   string   `json:"period,omitempty" dynamodbav:"period,omitempty"`
	TotalOf  []string `json:"total_of,omitempty" dynamodbav:"total_of,omitempty"`
}

type SourceVote struct {
	For     *int `json:"for" dynamodbav:"for"`
	Against *int `json:"against" dynamodbav:"against"`
	Abstain *int `json:"abstain" dynamodbav:"abstain"`
}

type SourceComparison struct {
	Before  *int64 `json:"before_cents" dynamodbav:"before_cents"`
	After   *int64 `json:"after_cents" dynamodbav:"after_cents"`
	Printed *int64 `json:"printed_delta_cents" dynamodbav:"printed_delta_cents"`
}

// SourceFact is one critical proposition. Audience is the shortest format
// that must include it. A fact required in a brief is also required on the site
// and in a main article.
type SourceFact struct {
	ID         string            `json:"id" dynamodbav:"id"`
	Kind       string            `json:"kind" dynamodbav:"kind"`
	Audience   string            `json:"audience" dynamodbav:"audience"`
	Label      string            `json:"label" dynamodbav:"label"`
	Status     string            `json:"status" dynamodbav:"status"`
	Evidence   PDFPassage        `json:"evidence" dynamodbav:"evidence"`
	Money      *SourceMoney      `json:"money,omitempty" dynamodbav:"money,omitempty"`
	Vote       *SourceVote       `json:"vote,omitempty" dynamodbav:"vote,omitempty"`
	Comparison *SourceComparison `json:"comparison,omitempty" dynamodbav:"comparison,omitempty"`
	// Optional link from an essential condition to the amount it qualifies.
	FinancialFactID string `json:"financial_fact_id,omitempty" dynamodbav:"financial_fact_id,omitempty"`
}

type SourceFacts struct {
	Version               int          `json:"version" dynamodbav:"version"`
	PDFSHA256             string       `json:"pdf_sha256" dynamodbav:"pdf_sha256"`
	ProofStatus           string       `json:"proof_status" dynamodbav:"proof_status"`
	ReviewedPayloadSHA256 string       `json:"reviewed_payload_sha256,omitempty" dynamodbav:"reviewed_payload_sha256,omitempty"`
	Facts                 []SourceFact `json:"facts" dynamodbav:"facts"`
}

func (f SourceFact) Text() string {
	label := strings.TrimRight(strings.TrimSpace(f.Label), ".")
	switch {
	case f.Money != nil && f.Money.Cents != nil:
		detail := formatSourceCents(*f.Money.Cents)
		if f.Money.Role == "PLAFOND" {
			detail += " au maximum"
		}
		for _, part := range []string{f.Money.Basis, f.Money.Period} {
			if part != "" {
				detail += " " + part
			}
		}
		switch f.Status {
		case "PROPOSE":
			detail += " (proposé)"
		case "ADOPTE":
			detail += " (voté)"
		case "AUTORISE":
			detail += " (autorisé)"
		case "ENGAGE":
			detail += " (engagé)"
		case "SIGNE":
			detail += " (signature attestée)"
		case "PREVU":
			detail += " (prévu)"
		case "CONDITIONNEL":
			detail += " (sous conditions)"
		case "REALISE":
			detail += " (réalisé)"
		case "PAYE":
			detail += " (payé)"
		case "RECU":
			detail += " (reçu)"
		case "INCERTAIN":
			detail += " (à confirmer)"
		}
		return label + " : " + detail
	case f.Vote != nil:
		var parts []string
		for i, count := range []*int{f.Vote.For, f.Vote.Against, f.Vote.Abstain} {
			if count != nil {
				name := []string{"pour", "contre", "abstention" + plural(*count)}[i]
				parts = append(parts, fmt.Sprintf("%d %s", *count, name))
			}
		}
		return label + " : " + strings.Join(parts, ", ")
	case f.Comparison != nil && f.Comparison.Before != nil && f.Comparison.After != nil && f.Comparison.Printed != nil:
		c := f.Comparison
		return fmt.Sprintf("%s : le tableau indique %s avant, %s après et un ajustement de %s. La différence entre les deux montants donne %s",
			label, formatSourceCents(*c.Before), formatSourceCents(*c.After), formatSourceCents(*c.Printed), formatSourceCents(*c.After-*c.Before))
	default:
		return label
	}
}

func formatSourceCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "−"
	}
	euros, remainder := cents/100, cents%100
	if euros < 0 {
		euros = -euros
	}
	if remainder < 0 {
		remainder = -remainder
	}
	s := formatBudgetFR(euros)
	if remainder != 0 {
		s += fmt.Sprintf(",%02d", remainder)
	}
	return sign + s + " €"
}

func (s *SourceFacts) Reviewed() bool {
	return s != nil && s.Version == SourceFactsVersion && s.ProofStatus == ProofModelReviewed &&
		len(s.Facts) > 0 && isSHA256(s.PDFSHA256) && isSHA256(s.ReviewedPayloadSHA256)
}

func isSHA256(text string) bool {
	if len(text) != 64 {
		return false
	}
	_, err := hex.DecodeString(text)
	return err == nil
}

// HasKind uses only the reviewed packet. Historical records have no packet.
func (s *SourceFacts) HasKind(kind string) bool {
	if !s.Reviewed() {
		return false
	}
	for _, fact := range s.Facts {
		if fact.Kind == kind {
			return true
		}
	}
	return false
}
