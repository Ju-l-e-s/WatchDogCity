package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/watchdog/shared"
	"google.golang.org/genai"
)

// budgetAmountFloatRe strips decimal parts from any budget integer field.
// Kept as defense in depth: even with ResponseSchema INTEGER, Gemini
// occasionally emits floats (e.g. 2028913.40) instead of integers.
var budgetAmountFloatRe = regexp.MustCompile(`("(?:budget_impact|amount)"\s*:\s*)(\d+)\.\d+`)

var (
	validTopicTags      = shared.TopicTags
	validBudgetTypes    = shared.BudgetTypes
	validClimateImpacts = shared.ClimateImpacts
)

const deliberationPrompt = `Tu es un vulgarisateur neutre et un traducteur factuel pour L'Observatoire de Bègles, chargé de décrypter les délibérations de la ville.
Extrais les informations du PDF fourni au format JSON strict imposé par le schéma de réponse.

RÈGLES IMPÉRATIVES DE TRAITEMENT :

1. CATÉGORISATION FINANCIÈRE ("budget_type" et "budget_impact") :
   - Extrais le montant principal en EUROS ENTIERS dans "budget_impact" (pas en centimes ; ne multiplie ni ne convertis aucun montant). Valeur 0 si aucun montant.
   - Tu DOIS qualifier ce flux dans "budget_type" en utilisant UNIQUEMENT l'une de ces 4 valeurs exactes :
     * "DÉPENSE" : La ville paie ou verse de l'argent (subvention, achat, travaux, frais).
     * "RECETTE" : La ville gagne ou collecte de l'argent (impôts, taxes, vente de biens, dotations).
     * "CAUTION" : La ville se porte garante ou cautionne un prêt (ex: Agence France Locale).
     * "AUCUN" : si et seulement si budget_impact = 0. Tout montant non nul (même faible) DOIT porter le type DÉPENSE, RECETTE ou CAUTION. Réciproquement, budget_impact = 0 impose budget_type = "AUCUN".

2. TITRE ("title") :
   - Titre vulgarisé, factuel, neutre et descriptif de l'objet de la délibération. Rédige un titre simple pour un non-initié, évite le jargon technocratique.
   - Ce titre est la SEULE phrase reprise telle quelle par la newsletter publique : il doit pouvoir être publié sans relecture.

3. DÉCISION ("decision") :
   - Champ OBLIGATOIRE et non vide : indique ce qui a été décidé ou voté, en une phrase factuelle et simple.

4. IMPACTS CITOYENS ("impacts") :
   - Décris les conséquences DIRECTES, matérielles ou financières pour les Béglaises et Béglais.
   - "impacts" n'est JAMAIS null ni vide. Soit il décrit un impact citoyen concret, soit il vaut EXACTEMENT la chaîne "Néant". Aucune autre valeur (pas de "null", "N/A", "-", chaîne vide).
   - RÈGLE STRICTE : Si la délibération est de nature purement administrative, interne (élections de représentants, création de commissions, frais de mission des élus) ou sans impact tangible sur le quotidien citoyen, la valeur DOIT ÊTRE STRICTEMENT "Néant". N'invente JAMAIS d'impacts indirects, philosophiques ou théoriques.

5. VULGARISATION ET PÉDAGOGIE CITOYENNE :
   - INTERDICTION ABSOLUE DU JARGON COMPTABLE ET LÉGAL :
     * Ne mentionne jamais de codes d'imputation budgétaire (ex: pas de "Chapitre 65", "nature 657363", "article 65748").
     * Ne cite jamais d'articles de loi ou de codes juridiques bruts (ex: bannir "article L.2241-1 du CGCT", "décret n°..."). Écris plutôt : "Conformément à la loi..." ou "Selon la réglementation...".
     * Vulgarise systématiquement tous les acronymes ou termes techniques entre parenthèses lors de leur première apparition (ex: écrire "CFU (le bilan financier de l'année passée)", "CCAS (l'organisme d'action sociale de la ville)", "AP/CP (la programmation pluriannuelle des investissements)", "TPE (la taxe sur la publicité extérieure)", "ZAC (zone d'aménagement concerté)", "DSP (délégation de service public)").
   - STYLE JOURNALISTIQUE PÉDAGOGIQUE :
     * Traduis les termes administratifs en langage clair (ex: au lieu d'écrire "décision budgétaire modificative" ou "budget supplémentaire", explique : "Le conseil ajuste les comptes en cours d'année pour réallouer l'argent là où les besoins sont les plus urgents.").
     * Évite les répétitions et les formules vides de sens (ne pas écrire "le budget est concentré sur le budget").
     * Dans les champs textuels ("summary", "impacts", etc.), arrondis systématiquement les grands chiffres pour faciliter la lecture (ex: écris "environ 7 millions d'euros" au lieu de "7 034 925,77 euros" ou "7034925"). Remarque : le montant exact en centimes ne doit figurer que dans le champ numérique "budget_impact" du JSON.
     * Reste neutre et objectif. Aucun jugement de valeur (bannis "excellent", "ambitieux", "très coûteux").

6. RECADRAGE DE LA SECTION DÉSACCORDS / CONTROVERSES ("disagreements") :
   - Fais la différence entre une règle de procédure administrative obligatoire (ex: "Le maire a dû quitter la salle car il ne peut pas voter sur son propre bilan financier") et un véritable débat politique ou vote d'opposition.
   - Ne décris sous "disagreements" que les véritables tensions politiques, les débats de fond, ou les votes d'opposition (ex: voix contre). Si le vote s'est déroulé de façon standard sans opposition, écris : "Aucun désaccord, procédure standard."

Règles supplémentaires :
- Le champ "budget_breakdown" est un tableau de ventilation détaillée. Laisse vide [] sauf si c'est un VOTE DU BUDGET ou des subventions à de multiples associations.
  Si renseigné : la SOMME EXACTE des "amount" DOIT être rigoureusement égale à "budget_impact" (tolérance 0).
  Pour "topic_tag" dans budget_breakdown, utilise UNIQUEMENT une de ces valeurs exactes : Administration, Sport, Budget, Sécurité, Environnement, Mobilité, Social, Culture, Urbanisme, Éducation.
- "is_substantial" vaut "true" pour un budget, une DSP ou un projet structurant.
- Si pas de vote, "has_vote" = false et compteurs à null.
- Si "has_vote" = true, au moins un des compteurs (pour/contre/abstention) doit être un entier ≥ 0. Total plausible ≤ 40.
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
				"decision":        {Type: genai.TypeString},
				"impacts":         {Type: genai.TypeString},
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
					"topic_tag": {Type: genai.TypeString, Format: "enum", Enum: validTopicTags},
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

func ptrBool(b bool) *bool          { return &b }
func ptrFloat32(f float32) *float32 { return &f }
func ptrInt32(i int32) *int32       { return &i }

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

	resp, err := shared.CallGeminiWithRetry(ctx, func(ctx context.Context) (*genai.GenerateContentResponse, error) {
		return client.Models.GenerateContent(
			ctx,
			modelName,
			contents,
			&genai.GenerateContentConfig{
				Temperature:      ptrFloat32(0),
				ResponseMIMEType: "application/json",
				ResponseSchema:   deliberationSchema,
				MaxOutputTokens:  8192,
			},
		)
	}, 4)
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
// what the API-level schema guarantees. Defense in depth — normalizes accent
// and case drift (e.g. "DEPENSE" → "DÉPENSE") before validating.
func validateGeminiResult(r *GeminiResult) error {
	canonical, ok := shared.MatchTopicTag(r.TopicTag)
	if !ok {
		return fmt.Errorf("invalid topic_tag %q (must be one of %v)", r.TopicTag, validTopicTags)
	}
	r.TopicTag = canonical

	canonical, ok = shared.MatchBudgetType(r.BudgetType)
	if !ok {
		return fmt.Errorf("invalid budget_type %q (must be one of %v)", r.BudgetType, validBudgetTypes)
	}
	r.BudgetType = canonical

	canonical, ok = shared.MatchClimateImpact(r.ClimateImpact)
	if !ok {
		return fmt.Errorf("invalid climate_impact %q (must be one of %v)", r.ClimateImpact, validClimateImpacts)
	}
	r.ClimateImpact = canonical

	if r.BudgetType == "AUCUN" && r.BudgetImpact != 0 {
		return fmt.Errorf("budget_type=AUCUN but budget_impact=%d", r.BudgetImpact)
	}
	if len(r.BudgetBreakdown) > 0 {
		var sum int64
		for i := range r.BudgetBreakdown {
			b := &r.BudgetBreakdown[i]
			canonical, ok := shared.MatchTopicTag(b.TopicTag)
			if !ok {
				return fmt.Errorf("invalid breakdown topic_tag %q", b.TopicTag)
			}
			b.TopicTag = canonical
			sum += b.Amount
		}
		// Allow 1€ rounding tolerance.
		diff := sum - r.BudgetImpact
		if diff < 0 {
			diff = -diff
		}
		if r.BudgetImpact > 0 && diff > 1 {
			return fmt.Errorf("budget_breakdown sum=%d differs from budget_impact=%d (diff=%d > tolerance 1); set budget_breakdown=[] if full allocation is not possible", sum, r.BudgetImpact, diff)
		}
	}
	return nil
}
