package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/watchdog/shared"
	"google.golang.org/genai"
)

// ═══════════════════════════════════════════════════════════════════════════════
// SOURCE OF TRUTH: lambdas/worker/gemini.go
// Ce fichier duplique le prompt, le schema et les types du worker pour
// permettre le test local. Toute modification du prompt de production
// doit être reportée ici.
// ═══════════════════════════════════════════════════════════════════════════════

var (
	validTopicTags      = shared.TopicTags
	validBudgetTypes    = shared.BudgetTypes
	validClimateImpacts = shared.ClimateImpacts
)

var budgetAmountFloatRe = regexp.MustCompile(`("(?:budget_impact|amount)"\s*:\s*)(\d+)\.\d+`)

const deliberationPrompt = `Tu es un analyste juridique et financier chargé de décrypter les délibérations de la ville de Bègles.
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
   - Titre factuel, neutre et descriptif de l'objet de la délibération. Aucun adjectif évaluatif, aucune accroche éditoriale.
   - Ce titre est la SEULE phrase reprise telle quelle par la newsletter publique : il doit pouvoir être publié sans relecture.

3. DÉCISION ("decision") :
   - Champ OBLIGATOIRE et non vide : indique ce qui a été décidé ou voté, en une phrase factuelle.

4. IMPACTS CITOYENS ("impacts") :
   - Décris les conséquences DIRECTES, matérielles ou financières pour les Béglaises et Béglais.
   - "impacts" n'est JAMAIS null ni vide. Soit il décrit un impact citoyen concret, soit il vaut EXACTEMENT la chaîne "Néant". Aucune autre valeur (pas de "null", "N/A", "-", chaîne vide).
   - RÈGLE STRICTE : Si la délibération est de nature purement administrative, interne (élections de représentants, création de commissions, frais de mission des élus) ou sans impact tangible sur le quotidien citoyen, la valeur DOIT ÊTRE STRICTEMENT "Néant". N'invente JAMAIS d'impacts indirects, philosophiques ou théoriques.

5. NEUTRALITÉ ET PÉDAGOGIE :
   - AUCUN JARGON : Bannis le vocabulaire administratif, technocratique ou juridique brut. Si un terme complexe est indispensable (ex: "ZAC", "DSP"), tu DOIS le définir immédiatement en termes simples.
   - OBJECTIVITÉ : Reste factuel. Ne porte aucun jugement de valeur (évite "excellent", "coûteux", "ambitieux").
   - ANCRAGE (GROUNDING) : N'ajoute AUCUNE information qui n'est pas présente dans le document PDF. Ne fais aucun compliment, n'ajoute aucun fait historique, géographique ou classement (ex: "plus grand club") non mentionné explicitement dans le texte.
   - CLIMAT : "climate_impact" est "positif" uniquement pour des mesures environnementales directes (énergie renouvelable, espaces verts), "negatif" pour des énergies fossiles, sinon "neutre".

Règles supplémentaires :
- Le champ "budget_breakdown" est un tableau de ventilation détaillée. Laisse vide [] sauf si c'est un VOTE DU BUDGET ou des subventions à de multiples associations.
  Si renseigné : la SOMME EXACTE des "amount" DOIT être rigoureusement égale à "budget_impact" (tolérance 0). Additionne mentalement tous les montants et corrige jusqu'à l'égalité parfaite avant de produire le JSON. Si tu ne peux pas ventiler la totalité, laisse "budget_breakdown" = [] plutôt que de produire une ventilation partielle.
  Pour "topic_tag" dans budget_breakdown, utilise UNIQUEMENT une de ces valeurs exactes : Administration, Sport, Budget, Sécurité, Environnement, Mobilité, Social, Culture, Urbanisme, Éducation.
- "is_substantial" vaut "true" pour un budget, une DSP ou un projet structurant.
- Si pas de vote, "has_vote" = false et compteurs à null.
- Si "has_vote" = true, au moins un des compteurs (pour/contre/abstention) doit être un entier ≥ 0 ; ne les laisse pas tous à null. Les compteurs correspondent aux conseillers municipaux élus (environ 35) ; jamais au public, à la population ou à un quorum. Total plausible ≤ 40.
- Ne génère aucun texte en dehors du JSON.`

// ── Types (mirror of worker/gemini.go) ──────────────────────────────────────

type BudgetBreakdownItem struct {
	TopicTag string `json:"topic_tag"`
	Label    string `json:"label"`
	Amount   int64  `json:"amount"`
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

	InputTokens  int32 `json:"input_tokens"`
	OutputTokens int32 `json:"output_tokens"`
}

func ptrBool(b bool) *bool       { return &b }
func ptrFloat32(f float32) *float32 { return &f }

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

// ── Gemini call ─────────────────────────────────────────────────────────────

func analyzeWithGemini(ctx context.Context, apiKey, model string, pdfBytes []byte) (*GeminiResult, error) {
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
			model,
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
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	raw = budgetAmountFloatRe.ReplaceAllString(raw, "${1}${2}")

	var res GeminiResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("unmarshal gemini json: %w (raw: %s)", err, raw)
	}

	if res.Disagreements != nil && (*res.Disagreements == "null" || *res.Disagreements == "") {
		res.Disagreements = nil
	}

	return &res, nil
}

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
		diff := sum - r.BudgetImpact
		if diff < 0 {
			diff = -diff
		}
		if r.BudgetImpact > 0 && diff > 1 {
			return fmt.Errorf("budget_breakdown sum=%d differs from budget_impact=%d (diff=%d > tolerance 1)", sum, r.BudgetImpact, diff)
		}
	}
	return nil
}

// ── QC bridge: map GeminiResult → shared.DeliberationView ───────────────────

func toDeliberationView(r *GeminiResult) shared.DeliberationView {
	dv := shared.DeliberationView{
		ID:            "localtest",
		Title:         r.Title,
		TopicTag:      r.TopicTag,
		Summary:       r.Summary,
		BudgetImpact:  r.BudgetImpact,
		BudgetType:    r.BudgetType,
		ClimateImpact: r.ClimateImpact,
		HasVote:       r.Vote.HasVote,
		VotePour:      r.Vote.Pour,
		VoteContre:    r.Vote.Contre,
		VoteAbstention: r.Vote.Abstention,
		Disagreements: r.Disagreements,
		IsSubstantial: r.IsSubstantial,
	}

	dv.AnalysisData = shared.QcAnalysisData{
		Contexte: r.AnalysisData.Contexte,
		Decision: r.AnalysisData.Decision,
		Impacts:  r.AnalysisData.Impacts,
	}

	for _, b := range r.BudgetBreakdown {
		dv.BudgetBreakdown = append(dv.BudgetBreakdown, shared.QcBudgetBreakdownItem{
			TopicTag: b.TopicTag,
			Label:    b.Label,
			Amount:   b.Amount,
		})
	}

	return dv
}

// ── CLI command ─────────────────────────────────────────────────────────────

func runArticle(args []string) error {
	fs := flag.NewFlagSet("article", flag.ExitOnError)
	pdfPath := fs.String("pdf", "", "Chemin vers le fichier PDF à analyser")
	model := fs.String("model", "gemini-2.5-pro", "Modèle Gemini à utiliser")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *pdfPath == "" {
		return fmt.Errorf("--pdf est obligatoire\n\nUsage: go run . article --pdf <chemin.pdf>")
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("GEMINI_API_KEY non défini. Exportez-le: export GEMINI_API_KEY=<votre clé>")
	}

	// 1. Read PDF
	pdfBytes, err := os.ReadFile(*pdfPath)
	if err != nil {
		return fmt.Errorf("lecture du PDF: %w", err)
	}
	fmt.Printf("📄 PDF chargé: %s (%d Ko)\n", filepath.Base(*pdfPath), len(pdfBytes)/1024)
	fmt.Printf("🤖 Modèle: %s\n", *model)
	fmt.Println("⏳ Analyse Gemini en cours...")

	// 2. Call Gemini
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	start := time.Now()
	result, err := analyzeWithGemini(ctx, apiKey, *model, pdfBytes)
	elapsed := time.Since(start)

	if err != nil {
		return fmt.Errorf("analyse Gemini échouée: %w", err)
	}

	// 3. Display results
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println("📊 RÉSULTAT DE L'ANALYSE")
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("⏱  Durée:         %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("📥 Tokens input:  %d\n", result.InputTokens)
	fmt.Printf("📤 Tokens output: %d\n", result.OutputTokens)
	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Printf("📌 Titre:         %s\n", result.Title)
	fmt.Printf("🏷  Topic:         %s\n", result.TopicTag)
	fmt.Printf("💰 Budget:        %d € (%s)\n", result.BudgetImpact, result.BudgetType)
	fmt.Printf("🌿 Climat:        %s\n", result.ClimateImpact)
	fmt.Printf("⭐ Substantiel:   %v\n", result.IsSubstantial)

	if result.Vote.HasVote {
		pour, contre, abst := 0, 0, 0
		if result.Vote.Pour != nil {
			pour = *result.Vote.Pour
		}
		if result.Vote.Contre != nil {
			contre = *result.Vote.Contre
		}
		if result.Vote.Abstention != nil {
			abst = *result.Vote.Abstention
		}
		fmt.Printf("🗳  Vote:          %d pour / %d contre / %d abstention\n", pour, contre, abst)
	} else {
		fmt.Println("🗳  Vote:          pas de vote")
	}

	if result.Disagreements != nil && *result.Disagreements != "" {
		fmt.Printf("⚡ Désaccords:    %s\n", *result.Disagreements)
	}

	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Println("📝 Résumé:")
	fmt.Printf("   %s\n", result.Summary)

	if result.AnalysisData.Decision != nil {
		fmt.Println("───────────────────────────────────────────────────────────────")
		fmt.Println("⚖️  Décision:")
		fmt.Printf("   %s\n", *result.AnalysisData.Decision)
	}

	if result.AnalysisData.Impacts != nil {
		fmt.Println("───────────────────────────────────────────────────────────────")
		fmt.Println("👥 Impacts citoyens:")
		fmt.Printf("   %s\n", *result.AnalysisData.Impacts)
	}

	if result.AnalysisData.Contexte != nil {
		fmt.Println("───────────────────────────────────────────────────────────────")
		fmt.Println("📖 Contexte:")
		fmt.Printf("   %s\n", *result.AnalysisData.Contexte)
	}

	if len(result.KeyPoints) > 0 {
		fmt.Println("───────────────────────────────────────────────────────────────")
		fmt.Println("🔑 Points clés:")
		for _, kp := range result.KeyPoints {
			fmt.Printf("   • %s\n", kp)
		}
	}

	if len(result.BudgetBreakdown) > 0 {
		fmt.Println("───────────────────────────────────────────────────────────────")
		fmt.Println("📊 Ventilation budget:")
		for _, b := range result.BudgetBreakdown {
			fmt.Printf("   • [%s] %s: %d €\n", b.TopicTag, b.Label, b.Amount)
		}
	}

	if len(result.Acronyms) > 0 {
		fmt.Println("───────────────────────────────────────────────────────────────")
		fmt.Println("📚 Acronymes:")
		for k, v := range result.Acronyms {
			fmt.Printf("   • %s = %s\n", k, v)
		}
	}

	// 4. Run QC
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println("🔍 CONTRÔLE QUALITÉ (QC déterministe)")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	dv := toDeliberationView(result)
	council := shared.CouncilView{
		CouncilID:     "localtest",
		TotalPdfs:     1,
		ProcessedPdfs: 1,
		Date:          time.Now().Format("2006-01-02"),
	}
	violations := shared.ValidateDeterministic(council, []shared.DeliberationView{dv})

	if len(violations) == 0 {
		fmt.Println("✅ Aucune violation détectée — QC APPROVED")
	} else {
		highCount := 0
		warnCount := 0
		for _, v := range violations {
			if v.Severity == shared.SeverityHigh {
				highCount++
			} else {
				warnCount++
			}
		}
		if highCount > 0 {
			fmt.Printf("❌ QC QUARANTINED — %d HIGH, %d WARN\n", highCount, warnCount)
		} else {
			fmt.Printf("⚠️  QC APPROVED avec avertissements — %d WARN\n", warnCount)
		}
		fmt.Println()
		for _, v := range violations {
			icon := "⚠️ "
			if v.Severity == shared.SeverityHigh {
				icon = "❌"
			}
			fmt.Printf("   %s [%s] %s: %s\n", icon, v.Rule, v.Field, v.Detail)
		}
	}

	// 5. Save output
	if err := os.MkdirAll("output", 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	ts := time.Now().Format("20060102_150405")
	outPath := filepath.Join("output", fmt.Sprintf("article_%s.json", ts))
	outData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := os.WriteFile(outPath, outData, 0o644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	fmt.Println()
	fmt.Printf("💾 Résultat sauvegardé: %s\n", outPath)

	return nil
}
