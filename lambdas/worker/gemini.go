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
// Covers both "budget_impact": 1234.56 and "amount": 1234.56 (inside budget_breakdown items).
var budgetAmountFloatRe = regexp.MustCompile(`("(?:budget_impact|amount)"\s*:\s*)(\d+)\.\d+`)

const deliberationPrompt = `Tu es un analyste de données factuel et neutre spécialisé dans les affaires publiques locales. 
Analyse ce document PDF de délibération du conseil municipal de Bègles.

Retourne UNIQUEMENT un objet JSON valide avec cette structure exacte :
{
  "title": "titre de la délibération (en casse normale, évite les MAJUSCULES intégrales)",
  "summary": "résumé factuel en 2 à 3 phrases maximum, vulgarisé pour un citoyen",
  "topic_tag": "Un seul mot parmi cette liste stricte: Budget, Urbanisme, Social, Culture, Environnement, Éducation, Sport, Sécurité, Mobilité, Administration",
  "is_substantial": true/false,
  "acronyms": {
    "ACRONYME1": "Définition complète",
    "ACRONYME2": "Définition complète"
  },
  "analysis_data": {
    "contexte": "en 1 à 2 phrases : pourquoi ce point est à l'ordre du jour, son origine",
    "decision": "en 1 phrase : ce qui a été concrètement acté ou voté",
    "impacts": "en 1 à 2 phrases : conséquences directes pour les Béglaises et Béglais (ou null si aucun impact identifiable)",
    "points_debattus": "en 1 phrase : s'il y a eu débat ou opposition, résumer le désaccord (ou null si vote unanime sans discussion)"
  },
  "budget_impact": 150000,
  "budget_breakdown": [],
  "climate_impact": "positif/neutre/negatif",
  "key_points": [
    "point clé 1 (style télégraphique, très concis)",
    "point clé 2",
    "point clé 3"
  ],
  "vote": {
    "has_vote": true/false,
    "pour": entier ou null,
    "contre": entier ou null,
    "abstention": entier ou null
  },
  "disagreements": "description factuelle des désaccords (ou null si vote unanime ou sans objet)"
}

Règles d'exécution strictes :
- Identifie systématiquement les acronymes ou sigles techniques (ex: CCAS, CREPAQ, EPCI, DSP) présents dans le PDF et donne leur définition complète dans le champ "acronyms".
- Le champ "budget_impact" est CRUCIAL. Cherche tous les montants monétaires (€, euros, HT, TTC, crédits).
  * Priorise le montant total de l'opération, de l'investissement ou de la subvention accordée.
  * Si plusieurs montants sont cités (ex: coût total vs subvention attendue), prends le coût total de l'action municipale.
  * Cherche aussi dans les tableaux financiers s'ils existent.
  * Si aucun montant n'est mentionné, mets 0. Ne mets jamais null.
  * budget_impact doit toujours être un entier (arrondi à l'euro inférieur, sans décimale). Exemple : 2028913, pas 2028913.40.
  * EXCEPTION BUDGET GLOBAL UNIQUEMENT : Si la délibération est un "VOTE DU BUDGET PRIMITIF", "BUDGET SUPPLÉMENTAIRE" ou "DÉCISION MODIFICATIVE" — c'est-à-dire un document qui présente l'enveloppe budgétaire globale de la commune avec des lignes par fonction — alors mets budget_impact = 0 et remplis budget_breakdown. ATTENTION : une délibération d'attribution de subventions aux associations n'est PAS un vote de budget global, même si elle mentionne "lors du vote du budget" en introduction. Dans ce dernier cas, mets budget_impact = montant total du tableau.
- Le champ "budget_breakdown" est un tableau de ventilation détaillée des dépenses. Règles selon le type de délibération :
  * Pour les délibérations ordinaires sans tableau de bénéficiaires multiples : laisse le tableau vide [].
  * Pour un VOTE DU BUDGET PRIMITIF ou document budgétaire global (maquette, annexe budgétaire avec lignes fonctionnelles) : extrait TOUTES les lignes de dépenses identifiables. Objectif : 15 à 40 entrées précises, pas de regroupement grossier. Chaque entrée : {"topic_tag": "...", "label": "libellé exact de la ligne budgétaire", "amount": entier_en_euros}. Pour label : copie le libellé exact de la ligne du tableau (ex: "Personnel enseignant", "Entretien voirie", "Subvention CCAS"). Ne double pas les montants : si une ligne est un sous-total d'une autre déjà présente, ne prends que le détail le plus fin.
  * Pour les délibérations d'ATTRIBUTION DE SUBVENTIONS aux associations (tableau listant des associations bénéficiaires avec leurs montants individuels) : agrège par thématique en 5 à 10 entrées. Chaque entrée : {"topic_tag": "...", "label": "Subventions [thème] (N associations)", "amount": somme_en_euros}. Exemple : {"topic_tag": "Sport", "label": "Subventions sportives (12 associations)", "amount": 407950}. budget_impact = montant total du tableau (PAS 0).
  Règles pour topic_tag (tous cas) : utilise uniquement Budget, Urbanisme, Social, Culture, Environnement, Éducation, Sport, Sécurité, Mobilité, Administration.
- Le champ "climate_impact" doit être "positif" (investissement vert, nature en ville, isolation), "negatif" (artificialisation, fossile) ou "neutre" (fonctionnement courant, social sans impact bâti). Par défaut, mets "neutre".
- Le champ "is_substantial" doit être "true" uniquement si le document est dense (budget, DSP, projet structurant).
- Séparation des préoccupations : Ne génère JAMAIS de balises HTML. Retourne uniquement du texte brut dans les champs.
- Si le document ne mentionne pas de vote, "has_vote" doit être "false" et les compteurs à "null".
- Ne génère absolument aucun texte en dehors de l'objet JSON.
- Assure-toi que le JSON est valide et ne contient pas de caractères d'échappement incorrects.`

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
	BudgetImpact     int64                 `json:"budget_impact"`
	BudgetBreakdown  []BudgetBreakdownItem `json:"budget_breakdown"`
	ClimateImpact string   `json:"climate_impact"`
	KeyPoints     []string `json:"key_points"`
	Vote          struct {
		HasVote    bool `json:"has_vote"`
		Pour       *int `json:"pour"`
		Contre     *int `json:"contre"`
		Abstention *int `json:"abstention"`
	} `json:"vote"`
	Disagreements *string `json:"disagreements"`
	
	// Métadonnées de consommation (ajoutées)
	InputTokens  int32 `json:"input_tokens"`
	OutputTokens int32 `json:"output_tokens"`
}

func analyzeWithGemini(ctx context.Context, apiKey string, pdfBytes []byte) (*GeminiResult, error) {
	modelName := os.Getenv("GEMINI_MODEL")
	if modelName == "" {
		modelName = "gemini-2.5-pro"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      apiKey,
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1"},
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
		nil,
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

	// Récupération de l'usage des tokens
	if resp.UsageMetadata != nil {
		result.InputTokens = resp.UsageMetadata.PromptTokenCount
		result.OutputTokens = resp.UsageMetadata.CandidatesTokenCount
	}

	return result, nil
}

func parseGeminiResponse(raw string) (*GeminiResult, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	// Normalize: Gemini sometimes returns budget_impact as a float (e.g. 2028913.40)
	// but our struct expects int64 — truncate the decimal part.
	raw = budgetAmountFloatRe.ReplaceAllString(raw, "${1}${2}")

	var res GeminiResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("unmarshal gemini json: %w (raw: %s)", err, raw)
	}
	return &res, nil
}
