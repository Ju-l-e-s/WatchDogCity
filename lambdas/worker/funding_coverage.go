package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/watchdog/shared"
	"google.golang.org/genai"
)

const fundingCoveragePrompt = `Lis les articles du PDF pour déterminer si la décision porte sur une convention de financement ou une aide (subvention, fonds de concours ou contribution assimilée). Renseigne funding_coverage.applies à partir du PDF, même si le candidat n'en parle pas. Pour un autre document, rends checks=[].
Si applies=true, rends exactement un verdict pour chaque catégorie : PAYMENT_MODE (unique, échelonné ou autre), PREREQUISITES (justificatifs et conditions préalables), DEADLINE (durée, point de départ et phase, le cas échéant), CEILING (plafond, révision et règle de réduction), STAGE (approbation, autorisation, signature ou transfert attesté, en distinguant le financement des travaux). Vérifie tous les éléments matériels de chaque catégorie, pas seulement l'existence d'un fait du même type. N'infère aucune clause habituelle. Une modalité renvoyée à une convention future n'est pas établie.
Commence par le passage source complet. Pour MATCH, evidence cite la réponse source et matches relie les faits candidats avec leur fact_id et leur label intégral recopié dans candidate_text. Vérifie le sens complet, toutes les conditions et le référent. Une date de remise des pièces ne donne pas une date de paiement. NOT_ESTABLISHED exige un passage établissant positivement un renvoi à une convention future ou une exclusion explicite, avec reason. Une recherche infructueuse ou un doute vaut UNCERTAIN (SOURCE_AMBIGUITY/REVIEW).
Pour INCORRECT, MISSING ou UNCERTAIN, rattache via finding_indexes les indices des constats (base 0). INCORRECT exige CONTRADICTION/BLOCK y compris dans le résumé. MISSING exige OMISSION/BLOCK. Si une clause explicite de paiement, de justificatifs ou de délai manque entièrement, propose son ajout sous forme extractive : field=critical_facts, fact_id vide, claim et evidence.excerpt identiques (citation autonome exacte de la même page), financial_fact_id du financement concerné. Scinde en passages autonomes si nécessaire. Go construira un fait CONDITION par passage avec ce lien. Pour un délai, inclure obligation, durée et point de départ. Pour les autres omissions ne propose pas de lien. Renseigne prose_consistent après comparaison de TOUS les champs publiables, y compris le résumé, aux faits et aux ajouts proposés. Signale toute contradiction, même si la proposition d'ajout est exacte.`

var fundingCategories = []string{"PAYMENT_MODE", "PREREQUISITES", "DEADLINE", "CEILING", "STAGE"}

type fundingCoverage struct {
	Applies         *bool                  `json:"applies"`
	ProseConsistent *bool                  `json:"prose_consistent,omitempty"`
	Checks          []fundingCoverageCheck `json:"checks"`
}

type fundingMatch struct {
	FactID        string `json:"fact_id"`
	CandidateText string `json:"candidate_text"`
}

type fundingCoverageCheck struct {
	Matches        []fundingMatch     `json:"matches,omitempty"`
	Reason         string             `json:"reason,omitempty"`
	Category       string             `json:"category"`
	Status         string             `json:"status"`
	Evidence       *shared.PDFPassage `json:"evidence,omitempty"`
	FindingIndexes []int              `json:"finding_indexes,omitempty"`
}

func fundingFactCheckSchema() *genai.Schema {
	schema := *factCheckSchema
	schema.Required = append(slices.Clone(schema.Required), "funding_coverage")
	schema.Properties = maps.Clone(schema.Properties)
	schema.Properties["funding_coverage"] = &genai.Schema{Type: genai.TypeObject, Required: []string{"applies", "checks"},
		Properties: map[string]*genai.Schema{
			"applies":          {Type: genai.TypeBoolean},
			"prose_consistent": {Type: genai.TypeBoolean},
			"checks": {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeObject,
				Required: []string{"category", "status"}, Properties: map[string]*genai.Schema{
					"category":        schemaEnum(fundingCategories),
					"status":          schemaEnum([]string{"MATCH", "MISSING", "INCORRECT", "NOT_ESTABLISHED", "UNCERTAIN"}),
					"evidence":        passageSchema(),
					"reason":          {Type: genai.TypeString},
					"matches":         {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeObject, Required: []string{"fact_id", "candidate_text"}, Properties: map[string]*genai.Schema{"fact_id": {Type: genai.TypeString}, "candidate_text": {Type: genai.TypeString}}}},
					"finding_indexes": {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeInteger}},
				}}},
		}}
	return &schema
}

func fundingFactCheckContents(pdf []byte, payload []byte, targets map[string]financialTarget, discardedNote string) ([]*genai.Content, error) {
	contents, err := pdfFactCheckContents(pdf, payload, targets, discardedNote)
	if err != nil {
		return nil, err
	}
	contents[0].Parts[1].Text += "\n" + fundingCoveragePrompt
	return contents, nil
}

func parseFundingFactCheckResponse(raw string, facts []shared.SourceFact) (factCheckResult, error) {
	report, err := parseFactCheckResponse(raw)
	if err != nil {
		return report, err
	}
	if err := validateFundingCoverage(&report, facts); err != nil {
		return report, err
	}
	return report, nil
}

func validateFundingCoverage(report *factCheckResult, facts []shared.SourceFact) error {
	coverage := report.Funding
	if coverage == nil || coverage.Applies == nil || coverage.Checks == nil {
		return fmt.Errorf("%w: missing funding coverage", errFactCheckIncomplete)
	}
	if !*coverage.Applies {
		// A typed subsidy is a reason to require a complete report, not proof
		// that the candidate's qualification or any particular clause is correct.
		if len(coverage.Checks) != 0 || slices.ContainsFunc(facts, func(f shared.SourceFact) bool {
			return f.Money != nil && f.Money.Category == "SUBVENTION"
		}) {
			return fmt.Errorf("%w: inconsistent funding applicability", errFactCheckIncomplete)
		}
		return nil
	}
	if coverage.ProseConsistent == nil {
		return fmt.Errorf("%w: prose consistency unchecked", errFactCheckReview)
	}
	if len(coverage.Checks) != len(fundingCategories) {
		return fmt.Errorf("%w: incomplete funding categories", errFactCheckIncomplete)
	}
	seen := make(map[string]bool, len(fundingCategories))
	for _, row := range coverage.Checks {
		if !containsValue(fundingCategories, row.Category) || seen[row.Category] {
			return fmt.Errorf("%w: unknown or repeated funding category", errFactCheckIncomplete)
		}
		seen[row.Category] = true
		if err := validateFundingCheck(row, report.Findings, facts); err != nil {
			return err
		}
	}
	return nil
}

func validateFundingCheck(row fundingCoverageCheck, findings []factCheckFinding, facts []shared.SourceFact) error {
	bad := fmt.Errorf("%w: invalid funding verdict or supporting passage for %s", errFactCheckReview, row.Category)
	if row.Status != "MATCH" && len(row.Matches) > 0 {
		return bad
	}
	if row.Evidence != nil && !validPassage(*row.Evidence) {
		return bad
	}
	var kind, resolution string
	switch row.Status {
	case "MATCH":
		if len(row.FindingIndexes) != 0 || row.Evidence == nil || len(row.Matches) == 0 {
			return bad
		}
		seen := map[string]bool{}
		overlap := false
		for _, m := range row.Matches {
			idx := slices.IndexFunc(facts, func(f shared.SourceFact) bool { return f.ID == m.FactID })
			if idx < 0 || seen[m.FactID] || strings.TrimSpace(m.CandidateText) == "" || canonicalQuote(facts[idx].Label) != canonicalQuote(m.CandidateText) {
				return bad
			}
			f := facts[idx]
			if (row.Category == "DEADLINE" || row.Category == "PAYMENT_MODE" || row.Category == "PREREQUISITES") && f.Kind != "CONDITION" {
				return bad
			}
			a, b := canonicalQuote(f.Evidence.Excerpt), canonicalQuote(row.Evidence.Excerpt)
			if f.Evidence.Page == row.Evidence.Page && (strings.Contains(a, b) || strings.Contains(b, a)) {
				overlap = true
			}
			seen[m.FactID] = true
		}
		if !overlap {
			return bad
		}
		return nil
	case "NOT_ESTABLISHED":
		if len(row.FindingIndexes) != 0 || row.Evidence == nil || strings.TrimSpace(row.Reason) == "" {
			return fmt.Errorf("%w: absence not established for %s", errFactCheckReview, row.Category)
		}
		return nil
	case "MISSING":
		kind, resolution = "OMISSION", "BLOCK"
	case "INCORRECT":
		kind, resolution = "CONTRADICTION", "BLOCK"
	case "UNCERTAIN":
		kind, resolution = "SOURCE_AMBIGUITY", "REVIEW"
	default:
		return bad
	}
	if len(row.FindingIndexes) == 0 {
		return bad
	}
	for _, i := range row.FindingIndexes {
		if i < 0 || i >= len(findings) || findings[i].fundingCategory != "" ||
			findings[i].Kind != kind || findings[i].Resolution != resolution {
			return bad
		}
		findings[i].fundingCategory = row.Category
	}
	return nil
}

type fundingOmissionClaim struct {
	Index    int    `json:"index"`
	Category string `json:"category"`
	Claim    string `json:"claim"`
}

type fundingOmissionCheck struct {
	Index        *int   `json:"index"`
	Status       string `json:"status"`
	PassageIndex *int   `json:"passage_index,omitempty"`
}

func fundingOmissionChecksSchema() *genai.Schema {
	return &genai.Schema{Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeObject,
		Required: []string{"index", "status"}, Properties: map[string]*genai.Schema{
			"index":         {Type: genai.TypeInteger},
			"status":        schemaEnum([]string{"PROVED", "NOT_PROVED", "UNCERTAIN"}),
			"passage_index": {Type: genai.TypeInteger},
		}}}
}

func validateFundingOmissionChecks(findings []factCheckFinding, claims []fundingOmissionClaim, passages []shared.PDFPassage, checks []fundingOmissionCheck) error {
	if len(checks) != len(claims) {
		return fmt.Errorf("%w: incomplete targeted funding checks", errFactCheckIncomplete)
	}
	pending := make(map[int]string, len(claims))
	for _, c := range claims {
		pending[c.Index] = c.Category
	}
	var unresolved string
	for _, c := range checks {
		if c.Index == nil || pending[*c.Index] == "" {
			return fmt.Errorf("%w: invalid targeted funding index", errFactCheckIncomplete)
		}
		switch c.Status {
		case "PROVED":
			if c.PassageIndex == nil || *c.PassageIndex < 0 || *c.PassageIndex >= len(passages) {
				return fmt.Errorf("%w: invalid targeted passage index", errFactCheckIncomplete)
			}
			if !sameOmissionPassage(passages[*c.PassageIndex], *findings[*c.Index].Evidence) {
				unresolved = pending[*c.Index]
			}
		case "NOT_PROVED", "UNCERTAIN":
			unresolved = pending[*c.Index]
		default:
			return fmt.Errorf("%w: invalid targeted funding verdict", errFactCheckIncomplete)
		}
		delete(pending, *c.Index)
	}
	if unresolved != "" {
		return fmt.Errorf("%w: funding omission not established for %s", errFactCheckReview, unresolved)
	}
	return nil
}
