package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/watchdog/shared"
	"google.golang.org/genai"
)

var (
	validTopicTags      = shared.TopicTags
	validBudgetTypes    = shared.BudgetTypes
	validClimateImpacts = shared.ClimateImpacts
)

const deliberationPrompt = `Tu es un vulgarisateur neutre et un traducteur factuel pour L'Observatoire de Bègles, chargé de décrypter les délibérations de la ville.
Extrais les informations du PDF fourni au format JSON strict imposé par le schéma de réponse.

Commence par critical_facts, puis rédige les champs publics à partir de ces faits. Parcours le dispositif, les tableaux et les articles pertinents de la convention. Pour chaque opération, relève le passage qui établit son stade et sa date avant de résumer. Vérifie en priorité les conditions qui changent le montant, le droit au versement ou son échéance.

RÈGLES IMPÉRATIVES DE TRAITEMENT :

1. CATÉGORISATION FINANCIÈRE ("budget_type", "budget_impact", "budget_changes") :
   - Extrais le montant principal en EUROS ENTIERS dans "budget_impact" (pas en centimes ; ne multiplie aucun montant dans ce champ historique). Valeur 0 si aucun montant principal adapté.
   - Une dépense ou une recette autorisée n'est pas à elle seule un montant chiffré. Si seul un barème est fourni, conserve les tarifs et consigne l'absence de total dans un fait OTHER sans money, avec son passage. Mets 0 et "AUCUN" uniquement comme indicateurs historiques ; n'estime pas un volume inconnu.
   - Tu DOIS qualifier ce flux dans "budget_type" en utilisant UNIQUEMENT l'une de ces 4 valeurs exactes :
     * "DÉPENSE" : Sortie du point de vue de la ville, à son stade documenté (achat, subvention, travaux, frais).
     * "RECETTE" : Entrée du point de vue de la ville, à son stade documenté (impôts, vente, dotations, aide sollicitée).
     * "CAUTION" : La ville se porte garante ou cautionne un prêt (ex: Agence France Locale).
     * "AUCUN" : si et seulement si budget_impact = 0. Tout montant non nul (même faible) DOIT porter le type DÉPENSE, RECETTE ou CAUTION. Réciproquement, budget_impact = 0 impose budget_type = "AUCUN".
   - Pour une délibération qui modifie des crédits ou enveloppes pluriannuelles (AP/CP), n'enregistre PAS ces variations comme une dépense ou une recette dans budget_impact. Mets budget_impact=0, budget_type="AUCUN", et décris chaque variation dans budget_changes avec scope="AP" ou "CP", direction="HAUSSE" ou "BAISSE", le libellé, le montant positif en euros entiers et la période si elle est indiquée. Distingue chaque enveloppe et période ; ne confonds jamais le total CP et une composante AP.
   - Pour les autres montants financiers, laisse budget_changes vide. budget_impact/type est un affichage historique en euros entiers, réserve-le à un montant représentatif. Conserve tous les chiffres essentiels, leur signe et leurs centimes dans critical_facts. Si aucun montant unique ne représente fidèlement la décision, utilise 0 et AUCUN dans les champs historiques. Ne somme pas des flux hétérogènes.
   - Go reconstruit "budget_note". Préserve chaque clause qui change le montant, le versement ou son délai dans un fait CONDITION avec son propre passage. Indique dans financial_fact_id l'identifiant du fait FINANCE concerné lorsqu'il est identifiable. Un plafond ajustable exige le plafond, l'assiette et une condition distincte pour la règle de réduction. Réserve « forfaitaire » à un forfait attesté. Sans montant principal, laisse "budget_note" vide.

2. TITRE ("title") :
   - Titre vulgarisé, factuel, neutre et descriptif de l'objet de la délibération. Rédige un titre simple pour un non-initié, évite le jargon technocratique.
   - La newsletter peut reprendre ce titre tel quel. Rédige une phrase autonome, contrôlée comme les autres champs.

3. DÉCISION ("decision") :
   - Champ OBLIGATOIRE et non vide : indique ce qui a été décidé ou voté, en une phrase factuelle et simple.

4. IMPACTS CITOYENS ("impacts") :
   - Décris uniquement les changements physiques ou factuels concrets (factual changes) au moyen de verbes d'action concrets (ex: "Remplacement de 4 700 ampoules", "Ouverture de 10 postes").
   - Bannis absolument toutes les notions politiques, subjectives, marketing ou promotionnelles (ex: "sécurité accrue", "bien-être", "transition écologique", "confort des usagers").
   - "impacts" n'est JAMAIS null ni vide. Soit il décrit un impact physique concret, soit il vaut EXACTEMENT la chaîne "Néant". Aucune autre valeur (pas de "null", "N/A", "-", chaîne vide).
   - RÈGLE STRICTE : Si la délibération est de nature purement administrative, interne (élections de représentants, création de commissions, frais de mission des élus) ou sans impact matériel tangible direct sur le quotidien citoyen, la valeur DOIT ÊTRE STRICTEMENT "Néant". N'invente JAMAIS d'impacts indirects, théoriques ou idéologiques.
   - CONSERVE LE RÉFÉRENT DES CHIFFRES : une surface totale de terrains cédés pour plusieurs usages ne devient pas une surface d'espaces paysagers. Associe chaque nombre uniquement à ce qu'il mesure explicitement dans le PDF ; si la répartition n'est pas donnée, n'en invente aucune.

5. VULGARISATION ET PÉDAGOGIE CITOYENNE :
   - INTERDICTION ABSOLUE DU JARGON COMPTABLE ET LÉGAL :
     * Ne mentionne jamais de codes d'imputation budgétaire (ex: pas de "Chapitre 65", "nature 657363", "article 65748").
     * Ne cite jamais d'articles de loi ou de codes juridiques bruts (ex: bannir "article L.2241-1 du CGCT", "décret n°..."). Écris plutôt : "Conformément à la loi..." ou "Selon la réglementation...".
     * Vulgarise systématiquement tous les acronymes ou termes techniques entre parenthèses lors de leur première apparition. Pour AP, explique "enveloppe autorisée pour engager un projet sur plusieurs années" ; pour CP, explique "paiements prévus pour une année donnée". Ne présente jamais AP/CP sans cette explication dans le texte destiné au public.
   - STYLE JOURNALISTIQUE PÉDAGOGIQUE :
     * Traduis les termes administratifs en langage clair (ex: au lieu d'écrire "décision budgétaire modificative" ou "budget supplémentaire", explique : "Le conseil ajuste les comptes en cours d'année pour modifier les crédits de l'année.").
     * Évite les répétitions et les formules vides de sens (ne pas écrire "le budget est concentré sur le budget").
     * Dans les champs textuels, un grand chiffre peut être arrondi si tu écris « environ » et conserves le signe, la période et le référent. Conserve les montants exacts dans critical_facts. Un tarif, un écart ou un seuil dont la précision change le sens reste exact.
     * Reste neutre et objectif. Aucun jugement de valeur (bannis "excellent", "ambitieux", "très coûteux").

6. RECADRAGE DE LA SECTION DÉSACCORDS / CONTROVERSES ("disagreements") :
   - Fais la différence entre une règle de procédure administrative obligatoire (ex: "Le maire a dû quitter la salle car il ne peut pas voter sur son propre bilan financier") et un véritable débat politique ou vote d'opposition.
   - Si le vote comporte des voix contre ou des abstentions, indique leurs nombres de façon neutre et n'affirme pas que le vote était unanime. Un désaccord exprimé exige un passage du PDF qui le documente. N'invente pas les raisons d'une abstention ou d'un vote contre.
   - Écris "Aucun désaccord, procédure standard." uniquement si le PDF établit qu'il n'y a ni voix contre ni abstention, ou si aucun vote n'a eu lieu et qu'aucune controverse n'est rapportée.

7. STADE, TEMPS ET PÉRIODE :
   - Un fait par opération et stade : REALISE pour les travaux attestés, même si leur financement reste à venir. Distingue ADOPTE pour la décision, AUTORISE pour la signature permise, SIGNE si celle-ci est attestée, PAYE ou RECU uniquement pour le transfert lui-même. Un mandat de dépenses de la ville ne prouve pas l'encaissement d'une aide. Sans attestation de transfert, ne conclus pas non plus au non-versement.
   - Pour les aides, relève le mode de versement, les justificatifs, le délai et son point de départ, y compris par phase. Conserve les conditions cumulatives de caducité. Crée un fait DATE pour la décision datée et pour chaque créneau distinct applicable à un public précis. Le texte public reprend ces stades et périodes.
   - Pour les tableaux, conserve le total et les variations par section et par ligne, y compris AP/CP. Ne compare que la même série, le même périmètre et la même période. Une baisse des crédits d'un exercice ne prouve pas un report sur un autre. Pour une divergence, distingue les valeurs imprimées de l'écart calculé et conserve les en-têtes. Dans total_of, utilise seulement les identifiants extraits et comparables. Si l'écart reste incertain, signale-le sans inventer de cause.

Règles supplémentaires :
- Le champ "budget_breakdown" est un tableau de ventilation détaillée. Laisse vide [] sauf si c'est un VOTE DU BUDGET ou des subventions à de multiples associations.
  Si renseigné, représente une ventilation complète. L’arrondi à l’euro peut faire varier la somme historique de 1 € au plus.
  Pour "topic_tag" dans budget_breakdown, utilise UNIQUEMENT une de ces valeurs exactes : Administration, Sport, Budget, Sécurité, Environnement, Mobilité, Social, Culture, Urbanisme, Éducation.
- "is_substantial" vaut "true" pour un budget, une DSP, un projet structurant, ou une décision réglementaire / motion à forte portée symbolique ou écologique globale (ex: motions d'opposition aux vols de nuit, chartes environnementales, etc.), même si son budget est de 0 €.
- Si pas de vote, "has_vote" = false et compteurs à null.
- Si "has_vote" = true, au moins un des compteurs (pour/contre/abstention) doit être un entier ≥ 0. Total plausible ≤ 60. Un compteur non indiqué reste à null.
- Ne génère aucun texte en dehors du JSON.` + sourceFactsInstructions

type BudgetBreakdownItem struct {
	TopicTag string `json:"topic_tag" dynamodbav:"topic_tag"`
	Label    string `json:"label"     dynamodbav:"label"`
	Amount   int64  `json:"amount"    dynamodbav:"amount"`
}

type BudgetChangeItem struct {
	Scope     string `json:"scope" dynamodbav:"scope"`
	Direction string `json:"direction" dynamodbav:"direction"`
	Label     string `json:"label" dynamodbav:"label"`
	Amount    int64  `json:"amount" dynamodbav:"amount"`
	Period    string `json:"period,omitempty" dynamodbav:"period,omitempty"`
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
	BudgetNote      string                `json:"budget_note"`
	BudgetBreakdown []BudgetBreakdownItem `json:"budget_breakdown"`
	BudgetChanges   []BudgetChangeItem    `json:"budget_changes"`
	ClimateImpact   string                `json:"climate_impact"`
	KeyPoints       []string              `json:"key_points"`
	Vote            struct {
		HasVote    bool `json:"has_vote"`
		Pour       *int `json:"pour"`
		Contre     *int `json:"contre"`
		Abstention *int `json:"abstention"`
	} `json:"vote"`
	Disagreements *string             `json:"disagreements"`
	CriticalFacts []shared.SourceFact `json:"critical_facts,omitempty"`
	SourceFacts   *shared.SourceFacts `json:"-"`

	// Consumption metadata (populated post-call)
	InputTokens  int32 `json:"input_tokens"`
	OutputTokens int32 `json:"output_tokens"`
	// Set only after the separate PDF fact-check has accepted the public fields.
	FactCheckModel string `json:"-"`
	FactCheckedAt  string `json:"-"`
	// In-memory clue for locating a possible omission in the original PDF.
	budgetNoteBeforeProjection string
}

// deliberationSchema is the authoritative output contract enforced at the
// Gemini API boundary. Enum values mirror the validTopicTags / validBudgetTypes
// / validClimateImpacts constants.
var deliberationSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"critical_facts": deliberationFactsSchema(),
		"title":          {Type: genai.TypeString},
		"summary":        {Type: genai.TypeString},
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
			Required:         []string{"decision", "impacts"},
		},
		"budget_impact": {Type: genai.TypeInteger, Format: "int64"},
		"budget_type": {
			Type:   genai.TypeString,
			Format: "enum",
			Enum:   validBudgetTypes,
		},
		"budget_note": {Type: genai.TypeString},
		"budget_changes": {
			Type: genai.TypeArray,
			Items: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
				"scope":     {Type: genai.TypeString, Format: "enum", Enum: []string{"AP", "CP", "AUTRE"}},
				"direction": {Type: genai.TypeString, Format: "enum", Enum: []string{"HAUSSE", "BAISSE"}},
				"label":     {Type: genai.TypeString}, "amount": {Type: genai.TypeInteger, Format: "int64"}, "period": {Type: genai.TypeString},
			}, PropertyOrdering: []string{"scope", "direction", "label", "amount", "period"}, Required: []string{"scope", "direction", "label", "amount"}},
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
		"critical_facts", "title", "summary", "topic_tag", "is_substantial", "acronyms",
		"analysis_data", "budget_impact", "budget_type", "budget_note", "budget_breakdown", "budget_changes",
		"climate_impact", "key_points", "vote", "disagreements",
	},
	Required: []string{
		"title", "summary", "topic_tag", "is_substantial",
		"analysis_data", "budget_impact", "budget_type", "budget_note",
		"budget_changes", "climate_impact", "key_points", "vote", "critical_facts",
	},
}

func ptrBool(b bool) *bool          { return &b }
func ptrFloat32(f float32) *float32 { return &f }
func ptrInt32(i int32) *int32       { return &i }

func analyzeWithGemini(ctx context.Context, apiKey string, pdfBytes []byte) (*GeminiResult, error) {
	modelName := os.Getenv("GEMINI_MODEL")
	if modelName == "" {
		modelName = shared.PDFExtractionModel
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      apiKey,
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta"},
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}

	cfg := shared.GeminiJSONConfig(modelName, genai.ThinkingLevelMinimal, deliberationSchema, pdfExtractionMaxTokens)
	resp, err := shared.CallGeminiWithRetry(ctx, shared.ObserveGemini("pdf_extract", modelName, cfg, func(ctx context.Context) (*genai.GenerateContentResponse, error) {
		return client.Models.GenerateContent(ctx, modelName, pdfExtractionContents(pdfBytes), cfg)
	}), 4)
	if err != nil {
		return nil, fmt.Errorf("gemini generate: %w", err)
	}
	return validatePDFExtraction(resp, pdfBytes)
}

const pdfExtractionMaxTokens = 8192

func pdfExtractionContents(pdfBytes []byte) []*genai.Content {
	return []*genai.Content{
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

}

func validatePDFExtraction(resp *genai.GenerateContentResponse, pdfBytes []byte) (*GeminiResult, error) {
	raw, err := shared.GeminiJSONText(resp)
	if err != nil {
		return nil, err
	}
	result, err := parseGeminiResponse(raw)
	if err != nil {
		return nil, err
	}

	if err := validateCriticalFacts(result); err != nil {
		return nil, fmt.Errorf("validate Gemini source facts: %w", err)
	}
	originalBudgetNote := strings.TrimSpace(result.BudgetNote)
	if err := projectBudgetNote(result); err != nil {
		return nil, err
	}
	if originalBudgetNote != strings.TrimSpace(result.BudgetNote) {
		result.budgetNoteBeforeProjection = originalBudgetNote
	}
	if err := validateGeminiResult(result); err != nil {
		return nil, fmt.Errorf("validate gemini result: %w", err)
	}

	result.SourceFacts = &shared.SourceFacts{Version: shared.SourceFactsVersion, PDFSHA256: digest(pdfBytes),
		ProofStatus: shared.ProofStructural, Facts: result.CriticalFacts}

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

	var res GeminiResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("unmarshal gemini json: %w", err)
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

	if r.BudgetImpact < 0 || (r.BudgetType == "AUCUN") != (r.BudgetImpact == 0) {
		return fmt.Errorf("budget_type=%s incompatible with budget_impact=%d", r.BudgetType, r.BudgetImpact)
	}
	if len(r.BudgetChanges) > 0 && (r.BudgetImpact != 0 || r.BudgetType != "AUCUN") {
		return fmt.Errorf("budget_changes must be separate from budget_impact/type")
	}
	for i, change := range r.BudgetChanges {
		if change.Scope != "AP" && change.Scope != "CP" && change.Scope != "AUTRE" {
			return fmt.Errorf("budget_changes[%d] has invalid scope %q", i, change.Scope)
		}
		if change.Direction != "HAUSSE" && change.Direction != "BAISSE" {
			return fmt.Errorf("budget_changes[%d] has invalid direction %q", i, change.Direction)
		}
		if change.Amount <= 0 || strings.TrimSpace(change.Label) == "" {
			return fmt.Errorf("budget_changes[%d] requires a positive amount and label", i)
		}
	}
	if r.BudgetImpact != 0 && strings.TrimSpace(r.BudgetNote) == "" {
		return fmt.Errorf("budget_note required when budget_impact=%d", r.BudgetImpact)
	}
	if r.CriticalFacts != nil {
		if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Summary) == "" ||
			r.AnalysisData.Decision == nil || strings.TrimSpace(*r.AnalysisData.Decision) == "" ||
			r.AnalysisData.Impacts == nil || strings.TrimSpace(*r.AnalysisData.Impacts) == "" {
			return fmt.Errorf("missing decision, impacts, title or summary")
		}
		total, known := 0, 0
		for _, count := range []*int{r.Vote.Pour, r.Vote.Contre, r.Vote.Abstention} {
			if count != nil {
				if !r.Vote.HasVote || *count < 0 || *count > 60 {
					return fmt.Errorf("invalid counter for documented vote")
				}
				total += *count
				known++
			}
		}
		if (r.Vote.HasVote && known == 0) || total > 60 {
			return fmt.Errorf("incomplete or implausible documented vote")
		}
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
			if b.Amount < 0 {
				return fmt.Errorf("negative budget breakdown amount")
			}
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
