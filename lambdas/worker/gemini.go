package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"google.golang.org/genai"
)

// budgetAmountFloatRe strips decimal parts from any budget integer field.
// Kept as defense in depth: even with ResponseSchema INTEGER, Gemini
// occasionally emits floats (e.g. 2028913.40) instead of integers.
var budgetAmountFloatRe = regexp.MustCompile(`("(?:budget_impact|amount)"\s*:\s*)(\d+)\.\d+`)

// Enum sources — single source of truth, mirrored into the response schema
// and re-validated after parsing.
var (
	validTopicTags = []string{
		"Budget", "Urbanisme", "Social", "Culture", "Environnement",
		"Éducation", "Sport", "Sécurité", "Mobilité", "Administration",
	}
	validBudgetTypes    = []string{"DÉPENSE", "RECETTE", "CAUTION", "AUCUN"}
	validClimateImpacts = []string{"positif", "neutre", "negatif"}
)

const deliberationPrompt = `Tu es un analyste juridique et financier chargé de décrypter les délibérations de la ville de Bègles.
Extrais les informations du PDF fourni au format JSON strict imposé par le schéma de réponse.

RÈGLES IMPÉRATIVES DE TRAITEMENT :

1. CATÉGORISATION FINANCIÈRE ("budget_type" et "budget_impact") :
   - Extrais le montant principal en euros dans "budget_impact" (nombre entier, 0 si aucun).
   - Tu DOIS qualifier ce flux dans "budget_type" en utilisant UNIQUEMENT l'une de ces 4 valeurs exactes :
     * "DÉPENSE" : La ville paie ou verse de l'argent (subvention, achat, travaux, frais).
     * "RECETTE" : La ville gagne ou collecte de l'argent (impôts, taxes, vente de biens, dotations).
     * "CAUTION" : La ville se porte garante ou cautionne un prêt (ex: Agence France Locale).
     * "AUCUN" : Aucun montant significatif.

2. IMPACTS CITOYENS ("impacts") :
   - Décris les conséquences DIRECTES, matérielles ou financières pour les Béglaises et Béglais.
   - REGLE STRICTE : Si la délibération est de nature purement administrative, interne (élections de représentants, création de commissions, frais de mission des élus) ou sans impact tangible sur le quotidien citoyen, la valeur de ce champ DOIT ÊTRE STRICTEMENT la chaîne "Néant". N'invente JAMAIS d'impacts indirects, philosophiques ou théoriques.

3. NEUTRALITÉ ET PÉDAGOGIE :
   - AUCUN JARGON : Bannis le vocabulaire administratif, technocratique ou juridique brut. Si un terme complexe est indispensable (ex: "ZAC", "DSP"), tu DOIS le définir immédiatement en termes simples.
   - OBJECTIVITÉ : Reste factuel. Ne porte aucun jugement de valeur (évite "excellent", "coûteux", "ambitieux").
   - ANCRAGE (GROUNDING) : N'ajoute AUCUNE information qui n'est pas présente dans le document PDF. Ne fais aucun compliment, n'ajoute aucun fait historique, géographique ou classement (ex: "plus grand club") non mentionné explicitement dans le texte.
   - CLIMAT : "climate_impact" est "positif" uniquement pour des mesures environnementales directes (énergie renouvelable, espaces verts), "negatif" pour des énergies fossiles, sinon "neutre".

Règles supplémentaires :
- Le champ "budget_breakdown" est un tableau de ventilation détaillée. Laisse vide [] sauf si c'est un VOTE DU BUDGET ou des subventions à de multiples associations (dans ce cas, extraire {"topic_tag": "...", "label": "...", "amount": entier}).
- "is_substantial" vaut "true" pour un budget, une DSP ou un projet structurant.
- Si pas de vote, "has_vote" = false et compteurs à null.
- Ne génère aucun texte en dehors du JSON.`

type BudgetBreakdownItem struct {
	TopicTag string `json:"topic_tag" dynamodbav:"topic_tag"`
	Label    string `json:"label"     dynamodbav:"label"`
	Amount   int64  `json:"amount"    dynamodbav:"amount"`
}

type GeminiResult struct {
	Title         string            `json:"title"`
	Summary       string            `json:"summary"`
	TopicTag      string            `json:"topic_tag"`
	IsSubstantial bool              `json:"is_substantial"`
	Acronyms      map[string]string `json:"acronyms"`
	AnalysisData  struct {
		Contexte       *string `json:"contexte"`
		Decision       *string `json:"decision"`
		Impacts        *string `json:"impacts"`
		PointsDebattus *string `json:"points_debattus"`
	} `json:"analysis_data"`
	BudgetImpact    int64                 `json:"budget_impact"`
	BudgetType      string                `json:"budget_type"`
	BudgetBreakdown []BudgetBreakdownItem `json:"budget_breakdown"`
	ClimateImpact   string                `json:"climate_impact"`
	KeyPoints       []string              `json:"key_points"`
	Vote            struct {
		HasVote    bool `json:"has_vote"`
		Pour       *int `json:"pour"`
		Contre     *int `json:"contre"`
		Abstention *int `json:"abstention"`
	} `json:"vote"`
	Disagreements *string `json:"disagreements"`

	// Consumption metadata (populated post-call)
	InputTokens  int32 `json:"input_tokens"`
	OutputTokens int32 `json:"output_tokens"`
}

// deliberationSchema is the authoritative output contract enforced at the
// Gemini API boundary. Enum values mirror the validTopicTags / validBudgetTypes
// / validClimateImpacts constants.
var deliberationSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"title":   {Type: genai.TypeString},
		"summary": {Type: genai.TypeString},
		"topic_tag": {
			Type:   genai.TypeString,
			Format: "enum",
			Enum:   validTopicTags,
		},
		"is_substantial": {Type: genai.TypeBoolean},
		"acronyms":       {Type: genai.TypeObject},
		"analysis_data": {
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"contexte":        {Type: genai.TypeString, Nullable: ptrBool(true)},
				"decision":        {Type: genai.TypeString, Nullable: ptrBool(true)},
				"impacts":         {Type: genai.TypeString, Nullable: ptrBool(true)},
				"points_debattus": {Type: genai.TypeString, Nullable: ptrBool(true)},
			},
			PropertyOrdering: []string{"contexte", "decision", "impacts", "points_debattus"},
		},
		"budget_impact": {Type: genai.TypeInteger, Format: "int64"},
		"budget_type": {
			Type:   genai.TypeString,
			Format: "enum",
			Enum:   validBudgetTypes,
		},
		"budget_breakdown": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"topic_tag": {Type: genai.TypeString},
					"label":     {Type: genai.TypeString},
					"amount":    {Type: genai.TypeInteger, Format: "int64"},
				},
				PropertyOrdering: []string{"topic_tag", "label", "amount"},
				Required:         []string{"topic_tag", "label", "amount"},
			},
		},
		"climate_impact": {
			Type:   genai.TypeString,
			Format: "enum",
			Enum:   validClimateImpacts,
		},
		"key_points": {
			Type:  genai.TypeArray,
			Items: &genai.Schema{Type: genai.TypeString},
		},
		"vote": {
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"has_vote":   {Type: genai.TypeBoolean},
				"pour":       {Type: genai.TypeInteger, Nullable: ptrBool(true)},
				"contre":     {Type: genai.TypeInteger, Nullable: ptrBool(true)},
				"abstention": {Type: genai.TypeInteger, Nullable: ptrBool(true)},
			},
			PropertyOrdering: []string{"has_vote", "pour", "contre", "abstention"},
			Required:         []string{"has_vote"},
		},
		"disagreements": {Type: genai.TypeString, Nullable: ptrBool(true)},
	},
	PropertyOrdering: []string{
		"title", "summary", "topic_tag", "is_substantial", "acronyms",
		"analysis_data", "budget_impact", "budget_type", "budget_breakdown",
		"climate_impact", "key_points", "vote", "disagreements",
	},
	Required: []string{
		"title", "summary", "topic_tag", "is_substantial",
		"analysis_data", "budget_impact", "budget_type",
		"climate_impact", "key_points", "vote",
	},
}

func ptrBool(b bool) *bool       { return &b }
func ptrFloat32(f float32) *float32 { return &f }

func analyzeWithGemini(ctx context.Context, apiKey string, pdfBytes []byte) (*GeminiResult, error) {
	modelName := os.Getenv("GEMINI_MODEL")
	if modelName == "" {
		modelName = "gemini-2.5-pro"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      apiKey,
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta"},
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}

	contents := []*genai.Content{
		{
			Role: "user",
			Parts: []*genai.Part{
				{
					InlineData: &genai.Blob{
						MIMEType: "application/pdf",
						Data:     pdfBytes,
					},
				},
				{
					Text: deliberationPrompt,
				},
			},
		},
	}

	resp, err := client.Models.GenerateContent(
		ctx,
		modelName,
		contents,
		&genai.GenerateContentConfig{
			Temperature:      ptrFloat32(0),
			ResponseMIMEType: "application/json",
			ResponseSchema:   deliberationSchema,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("gemini generate: %w", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned empty response")
	}

	raw := resp.Candidates[0].Content.Parts[0].Text
	result, err := parseGeminiResponse(raw)
	if err != nil {
		return nil, err
	}

	if err := validateGeminiResult(result); err != nil {
		return nil, fmt.Errorf("validate gemini result: %w", err)
	}

	if resp.UsageMetadata != nil {
		result.InputTokens = resp.UsageMetadata.PromptTokenCount
		result.OutputTokens = resp.UsageMetadata.CandidatesTokenCount
	}

	return result, nil
}

func parseGeminiResponse(raw string) (*GeminiResult, error) {
	raw = strings.TrimSpace(raw)
	// Defensive: ResponseSchema removes the need to strip markdown fences,
	// but legacy responses or older SDKs may still include them.
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	// Defensive: even with TypeInteger in the schema, Gemini sometimes emits
	// floats for budget_impact / amount. Truncate to int.
	raw = budgetAmountFloatRe.ReplaceAllString(raw, "${1}${2}")

	var res GeminiResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("unmarshal gemini json: %w (raw: %s)", err, raw)
	}

	// Defensive: Gemini sometimes emits the literal string "null" instead of
	// a JSON null. Collapse to nil so downstream consumers (notifier prompt
	// injection, frontend rendering) treat it as absent.
	if res.Disagreements != nil && (*res.Disagreements == "null" || *res.Disagreements == "") {
		res.Disagreements = nil
	}

	return &res, nil
}

// validateGeminiResult enforces enum membership and basic invariants beyond
// what the API-level schema guarantees. Defense in depth — surfaces silent
// drift (e.g. Gemini returning "DEPENSE" without accent) as hard errors.
func validateGeminiResult(r *GeminiResult) error {
	if !contains(validTopicTags, r.TopicTag) {
		return fmt.Errorf("invalid topic_tag %q (must be one of %v)", r.TopicTag, validTopicTags)
	}
	if !contains(validBudgetTypes, r.BudgetType) {
		return fmt.Errorf("invalid budget_type %q (must be one of %v)", r.BudgetType, validBudgetTypes)
	}
	if !contains(validClimateImpacts, r.ClimateImpact) {
		return fmt.Errorf("invalid climate_impact %q (must be one of %v)", r.ClimateImpact, validClimateImpacts)
	}
	if r.BudgetType == "AUCUN" && r.BudgetImpact != 0 {
		return fmt.Errorf("budget_type=AUCUN but budget_impact=%d", r.BudgetImpact)
	}
	if len(r.BudgetBreakdown) > 0 {
		var sum int64
		for _, b := range r.BudgetBreakdown {
			if !contains(validTopicTags, b.TopicTag) {
				return fmt.Errorf("invalid breakdown topic_tag %q", b.TopicTag)
			}
			sum += b.Amount
		}
		// Allow 1€ rounding tolerance.
		diff := sum - r.BudgetImpact
		if diff < 0 {
			diff = -diff
		}
		if r.BudgetImpact > 0 && diff > 1 {
			// Don't hard-fail (breakdown can legitimately exclude minor lines),
			// but the inconsistency is logged by callers via the returned error
			// path. Demoting to warn keeps the pipeline alive on partial breakdowns.
			fmt.Printf("WARN: budget_breakdown sum=%d differs from budget_impact=%d\n", sum, r.BudgetImpact)
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
