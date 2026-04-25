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

const deliberationPrompt = `Tu es un analyste juridique et financier chargé de décrypter les délibérations de la ville de Bègles.
Extrais les informations du PDF fourni au format JSON strict.

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
   - CLIMAT : "climate_impact" est "positif" uniquement pour des mesures environnementales directes (énergie renouvelable, espaces verts), "negatif" pour des énergies fossiles, sinon "neutre".

Format JSON attendu :
{
  "title": "titre de la délibération (en casse normale)",
  "summary": "résumé factuel en 2 à 3 phrases maximum, vulgarisé pour un citoyen",
  "topic_tag": "Un seul mot parmi cette liste stricte: Budget, Urbanisme, Social, Culture, Environnement, Éducation, Sport, Sécurité, Mobilité, Administration",
  "is_substantial": true, // true uniquement si budget > 1000€ ou modification d'un service public
  "acronyms": {
    "ACR": "Définition simple (limite à 3 acronymes max)"
  },
  "analysis_data": {
    "contexte": "Pourquoi ce sujet est sur la table.",
    "decision": "L'action concrète qui a été votée.",
    "impacts": "L'impact direct, ou 'Néant'.",
    "points_debattus": "Les arguments de l'opposition s'il y en a, sinon null."
  },
  "budget_impact": 10000,
  "budget_type": "DÉPENSE",
  "budget_breakdown": [],
  "climate_impact": "positif/neutre/negatif",
  "key_points": [
    "point clé 1 (2 à 3 phrases max)",
    "point clé 2"
  ],
  "vote": {
    "has_vote": true,
    "pour": 0,
    "contre": 0,
    "abstention": 0
  },
  "disagreements": "description factuelle des désaccords ou null"
}

Règles supplémentaires :
- Le champ "budget_breakdown" est un tableau de ventilation détaillée. Laisse vide [] sauf si c'est un VOTE DU BUDGET ou des subventions à de multiples associations (dans ce cas, extraire {"topic_tag": "...", "label": "...", "amount": entier}).
- Le champ "climate_impact" doit être "positif" (investissement vert), "negatif" (fossile) ou "neutre". Par défaut: "neutre".
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
	BudgetImpact     int64                 `json:"budget_impact"`
	BudgetType       string                `json:"budget_type"`
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
