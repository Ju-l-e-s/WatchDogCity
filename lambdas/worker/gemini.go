package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"google.golang.org/genai"
)

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
- Le champ "climate_impact" doit être "positif" (investissement vert, nature en ville, isolation), "negatif" (artificialisation, fossile) ou "neutre" (fonctionnement courant, social sans impact bâti). Par défaut, mets "neutre".
- Le champ "is_substantial" doit être "true" uniquement si le document est dense (budget, DSP, projet structurant).
- Séparation des préoccupations : Ne génère JAMAIS de balises HTML. Retourne uniquement du texte brut dans les champs.
- Si le document ne mentionne pas de vote, "has_vote" doit être "false" et les compteurs à "null".
- Ne génère absolument aucun texte en dehors de l'objet JSON.
- Assure-toi que le JSON est valide et ne contient pas de caractères d'échappement incorrects.`

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
	BudgetImpact  int64    `json:"budget_impact"`
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

	var res GeminiResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("unmarshal gemini json: %w (raw: %s)", err, raw)
	}
	return &res, nil
}
