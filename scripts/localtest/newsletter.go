package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"github.com/watchdog/shared"
)

type DataJSON struct {
	GeneratedAt     string          `json:"generated_at"`
	NextCouncilDate string          `json:"next_council_date"`
	Councils        []CouncilOutput `json:"councils"`
}

type CouncilOutput struct {
	CouncilID     string               `json:"id"`
	Category      string               `json:"category"`
	Date          string               `json:"date"`
	Title         string               `json:"title"`
	Summary       string               `json:"summary"`
	SourceURL     string               `json:"source_url"`
	Deliberations []DeliberationOutput `json:"deliberations"`
}

type DeliberationOutput struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	TopicTag      string `json:"topic_tag"`
	Summary       string `json:"summary"`
	IsSubstantial bool   `json:"is_substantial"`
	BudgetImpact  int64  `json:"budget_impact"`
	BudgetType    string `json:"budget_type"`
	Vote          struct {
		HasVote    bool `json:"has_vote"`
		Pour       *int `json:"pour"`
		Contre     *int `json:"contre"`
		Abstention *int `json:"abstention"`
	} `json:"vote"`
	Disagreements *string `json:"disagreements"`
	ClimateImpact string  `json:"climate_impact"`
	AnalysisData  struct {
		Impacts *string `json:"impacts"`
	} `json:"analysis_data"`
}

func runNewsletter(args []string) error {
	fs := flag.NewFlagSet("newsletter", flag.ExitOnError)
	fixturesPath := fs.String("fixtures", "", "Chemin vers le fichier JSON des délibérations")
	sample := fs.Bool("sample", false, "Utiliser les données de délibérations de test par défaut (frontend/data.json)")
	model := fs.String("model", "gemini-2.5-pro", "Modèle Gemini à utiliser")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *fixturesPath == "" && !*sample {
		return fmt.Errorf("vous devez spécifier soit --fixtures <chemin.json> soit --sample\n\nUsage: go run . newsletter --sample")
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("GEMINI_API_KEY non défini. Exportez-le: export GEMINI_API_KEY=<votre clé>")
	}

	// 1. Determine JSON path
	var jsonPath string
	if *sample {
		// Test paths
		candidates := []string{
			"frontend/data.json",
			"../../frontend/data.json",
			filepath.Join(os.Getenv("CWD"), "frontend/data.json"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				jsonPath = c
				break
			}
		}
		if jsonPath == "" {
			return fmt.Errorf("impossible de trouver le fichier frontend/data.json. Spécifiez son chemin via --fixtures")
		}
	} else {
		jsonPath = *fixturesPath
	}

	fmt.Printf("📂 Chargement des données de test depuis : %s\n", jsonPath)

	// 2. Read and parse JSON
	dataBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("lecture du fichier de données : %w", err)
	}

	var data DataJSON
	if err := json.Unmarshal(dataBytes, &data); err != nil {
		return fmt.Errorf("unmarshal json data : %w", err)
	}

	if len(data.Councils) == 0 {
		return fmt.Errorf("le fichier de données ne contient aucun conseil municipal")
	}

	// 3. Find a council with substantial deliberations, or just the first one
	var targetCouncil *CouncilOutput
	for i := range data.Councils {
		c := &data.Councils[i]
		// Let's prefer a council with some deliberations
		if len(c.Deliberations) > 0 {
			targetCouncil = c
			break
		}
	}
	if targetCouncil == nil {
		targetCouncil = &data.Councils[0]
	}

	fmt.Printf("🏛️  Conseil sélectionné : %q (%s) avec %d délibérations\n", targetCouncil.Title, targetCouncil.Date, len(targetCouncil.Deliberations))

	// 4. Map deliberations to ColdDeliberations
	var coldDelibs []shared.ColdDeliberation
	for _, d := range targetCouncil.Deliberations {
		contre := 0
		if d.Vote.Contre != nil {
			contre = *d.Vote.Contre
		}
		abst := 0
		if d.Vote.Abstention != nil {
			abst = *d.Vote.Abstention
		}
		hasDisagreement := d.Disagreements != nil && *d.Disagreements != "" && *d.Disagreements != "null"

		budgetType := d.BudgetType
		if budgetType == "" {
			if d.BudgetImpact > 0 {
				budgetType = "DÉPENSE"
			} else {
				budgetType = "AUCUN"
			}
		}

		climateImpact := d.ClimateImpact
		if climateImpact == "" {
			climateImpact = "neutre"
		}

		impactsVal := ""
		if d.AnalysisData.Impacts != nil {
			impactsVal = *d.AnalysisData.Impacts
		}

		coldDelibs = append(coldDelibs, shared.ColdDeliberation{
			Title:           d.Title,
			TopicTag:        d.TopicTag,
			BudgetImpact:    d.BudgetImpact,
			BudgetType:      budgetType,
			HasVote:         d.Vote.HasVote,
			Pour:            d.Vote.Pour,
			Contre:          d.Vote.Contre,
			Abstention:      d.Vote.Abstention,
			ClimateImpact:   climateImpact,
			IsSubstantial:   d.IsSubstantial || d.BudgetImpact >= 5000,
			HasDisagreement: hasDisagreement || contre > 0 || abst > 0,
			Summary:         d.Summary,
			Impacts:         impactsVal,
		})
	}

	// 5. Gather global statistics from fixtures
	totalCouncils := len(data.Councils)
	totalDelibs := 0
	for _, c := range data.Councils {
		totalDelibs += len(c.Deliberations)
	}

	nextMeeting := data.NextCouncilDate
	if nextMeeting == "" {
		nextMeeting = "À déterminer"
	}

	fmt.Printf("🤖 Modèle Gemini : %s\n", *model)
	fmt.Println("⏳ Génération des paramètres de la newsletter en cours...")

	// 6. Call GenerateNewsletterParams
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	start := time.Now()
	deps := shared.GeminiDeps{
		APIKey: apiKey,
		Model:  *model,
	}
	params, err := shared.GenerateNewsletterParams(
		ctx,
		deps,
		targetCouncil.Title,
		targetCouncil.Date,
		coldDelibs,
		nextMeeting,
		totalCouncils,
		totalDelibs,
	)
	elapsed := time.Since(start)

	if err != nil {
		return fmt.Errorf("échec de la génération Gemini de la newsletter : %w", err)
	}

	// 7. Output results
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println("📊 NEWSLETTER PARAMS GENERATED")
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("⏱  Durée:         %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("📧 Sujet:         %s\n", params.EmailSubject)
	fmt.Printf("📅 Titre Conseil:  %s\n", params.CouncilTitle)
	fmt.Printf("💰 Budget Total:  %s € (Global budget: %v)\n", params.BudgetTotal, params.HasGlobalBudget)
	fmt.Printf("🌿 Climat:        %s (Color: %s)\n", params.VoteClimat, params.ClimatColor)
	fmt.Printf("🗳  Stats Votes:   %s\n", params.VoteStats)
	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Printf("📝 Main Issue:\n   %s\n", params.MainIssue)

	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Printf("⚡ Tensions (%d) :\n", len(params.Tensions))
	for _, t := range params.Tensions {
		fmt.Printf("   • [%s] %s\n", t.Budget, t.Title)
		fmt.Printf("     Contexte : %s\n", t.Context)
		fmt.Printf("     Impact   : %s\n", t.Impact)
		fmt.Printf("     Vote     : %s\n", t.VoteDetails)
	}

	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Printf("✅ Adopted (%d) :\n", len(params.Adopted))
	for _, a := range params.Adopted {
		fmt.Printf("   • [%s][%s] %s\n", a.Tag, a.Budget, a.Title)
		fmt.Printf("     Contexte : %s\n", a.Context)
		fmt.Printf("     Impact   : %s\n", a.Impact)
	}

	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Printf("📝 Briefs (%d) :\n", len(params.Briefs))
	for _, b := range params.Briefs {
		fmt.Printf("   • [%s] %s\n", b.Tag, b.Summary)
	}

	// 8. Generate HTML Preview
	if err := os.MkdirAll("output", 0o755); err != nil {
		return fmt.Errorf("création du dossier output : %w", err)
	}

	ts := time.Now().Format("20060102_150405")
	jsonOutPath := filepath.Join("output", fmt.Sprintf("newsletter_%s.json", ts))
	htmlOutPath := filepath.Join("output", fmt.Sprintf("newsletter_%s.html", ts))

	// Save JSON params
	paramsJSON, err := json.MarshalIndent(params, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal params json : %w", err)
	}
	if err := os.WriteFile(jsonOutPath, paramsJSON, 0o644); err != nil {
		return fmt.Errorf("écriture du fichier json params : %w", err)
	}

	// Save HTML preview
	t, err := template.New("newsletter").Parse(htmlTemplate)
	if err != nil {
		return fmt.Errorf("parse html template : %w", err)
	}

	htmlFile, err := os.OpenFile(htmlOutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("création du fichier HTML de preview : %w", err)
	}
	defer htmlFile.Close()

	if err := t.Execute(htmlFile, params); err != nil {
		return fmt.Errorf("exécution du template HTML : %w", err)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("💾 JSON sauvegardé : %s\n", jsonOutPath)
	fmt.Printf("🌐 Preview HTML sauvegardée : %s\n", htmlOutPath)
	fmt.Println("   Vous pouvez ouvrir le fichier HTML dans votre navigateur pour voir le rendu.")
	fmt.Println("═══════════════════════════════════════════════════════════════")

	return nil
}

const htmlTemplate = `<!DOCTYPE html>
<html lang="fr">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>L'Essentiel du Conseil</title>
</head>
<body style="margin: 0; padding: 0; background-color: #F3F4F6; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; -webkit-font-smoothing: antialiased;">

    <center style="width: 100%; table-layout: fixed; background-color: #F3F4F6; padding: 50px 0;">
        <div style="max-width: 620px; margin: 0 auto; background-color: #FFFFFF; border: 1px solid #E5E7EB; border-radius: 8px; overflow: hidden; text-align: left;">
            
            <!-- HEADER -->
            <div style="padding: 40px 40px 30px 40px; border-bottom: 1px solid #E5E7EB;">
                <span style="font-size: 11px; font-weight: 700; color: #6B7280; text-transform: uppercase; letter-spacing: 1.5px;">Observatoire de Bègles</span>
                <h1 style="margin: 15px 0 10px 0; font-size: 28px; color: #111827; line-height: 1.2; font-weight: 800; letter-spacing: -0.5px;">L'Essentiel du Conseil</h1>
                <span style="font-size: 15px; color: #4B5563;">Conseil Municipal du <strong style="color: #111827;">{{.CouncilDate}}</strong></span>
            </div>

            <div style="padding: 40px;">
                
                <!-- ENJEU CLÉ -->
                <p style="font-size: 18px; line-height: 1.6; color: #374151; margin: 0 0 35px 0; font-style: italic; border-left: 3px solid #3B82F6; padding-left: 15px;">
                    <strong>L'Enjeu Clé :</strong> {{.MainIssue}}
                </p>

                <!-- STATS (STACKED ON MOBILE/DESKTOP TO PREVENT TEXT COMPRESSION) -->
                <div style="margin-bottom: 40px;">
                    {{if .HasGlobalBudget}}
                    <div style="background-color: #F9FAFB; border: 1px solid #E5E7EB; border-radius: 8px; padding: 16px 20px; margin-bottom: 12px;">
                        <span style="font-size: 11px; font-weight: 700; color: #6B7280; text-transform: uppercase; letter-spacing: 1px;">Impact Financier</span><br>
                        <span style="font-size: 26px; font-weight: 800; color: #111827; line-height: 1.3;">{{.BudgetTotal}} €</span>
                    </div>
                    {{end}}
                    <div style="background-color: #F9FAFB; border: 1px solid #E5E7EB; border-radius: 8px; padding: 16px 20px;">
                        <span style="font-size: 11px; font-weight: 700; color: #6B7280; text-transform: uppercase; letter-spacing: 1px;">Climat des Votes</span><br>
                        <span style="font-size: 22px; font-weight: 800; color: {{.ClimatColor}}; line-height: 1.3;">{{.VoteClimat}}</span><br>
                        <span style="font-size: 13px; color: #4B5563;">{{.VoteStats}}</span>
                    </div>
                </div>

                <!-- TENSIONS -->
                {{if .Tensions}}
                <div style="margin-bottom: 40px;">
                    <h2 style="font-size: 15px; color: #991B1B; text-transform: uppercase; letter-spacing: 1px; border-bottom: 1px solid #FECACA; padding-bottom: 8px; margin-bottom: 25px;">Les points de tension</h2>
                    
                    {{range .Tensions}}
                    <div style="margin-bottom: 35px; border-bottom: 1px solid #F3F4F6; padding-bottom: 25px;">
                        <h3 style="margin: 0 0 10px 0; font-size: 18px; color: #111827; font-weight: 700; line-height: 1.3;">{{.Title}}</h3>
                        
                        <!-- UPPER BADGES (VOTES & BUDGETS) -->
                        <div style="margin: 0 0 12px 0; line-height: 1;">
                            <span style="display: inline-block; background-color: #FEE2E2; color: #991B1B; padding: 4px 8px; border-radius: 4px; font-size: 11px; font-weight: 700; text-transform: uppercase; margin-right: 6px; margin-bottom: 4px; vertical-align: middle;">⚡ Débat</span>
                            <span style="display: inline-block; background-color: #FEF3C7; color: #92400E; padding: 4px 8px; border-radius: 4px; font-size: 11px; font-weight: 700; margin-right: 6px; margin-bottom: 4px; vertical-align: middle;">🗳️ {{.VoteDetails}}</span>
                            {{if .HasBudget}}
                            <span style="display: inline-block; background-color: #D1FAE5; color: #065F46; padding: 4px 8px; border-radius: 4px; font-size: 11px; font-weight: 700; margin-bottom: 4px; vertical-align: middle;">💰 {{.Budget}} €</span>
                            {{end}}
                        </div>

                        <p style="margin: 0 0 8px 0; font-size: 15px; color: #4B5563; line-height: 1.6;">
                            <strong style="color: #111827;">Le Contexte :</strong> {{.Context}}
                        </p>
                        <p style="margin: 0; font-size: 15px; color: #4B5563; line-height: 1.6;">
                            <strong style="color: #111827;">L'Impact :</strong> {{.Impact}}
                        </p>
                    </div>
                    {{end}}
                </div>
                {{end}}

                <!-- ADOPTED -->
                {{if .Adopted}}
                <div style="margin-bottom: 40px;">
                    <h2 style="font-size: 15px; color: #065F46; text-transform: uppercase; letter-spacing: 1px; border-bottom: 1px solid #A7F3D0; padding-bottom: 8px; margin-bottom: 25px;">Décisions Majeures Adoptées</h2>
                    
                    {{range .Adopted}}
                    <div style="margin-bottom: 35px; border-bottom: 1px solid #F3F4F6; padding-bottom: 25px;">
                        <h3 style="margin: 0 0 10px 0; font-size: 18px; color: #111827; font-weight: 700; line-height: 1.3;">{{.Title}}</h3>
                        
                        <!-- UPPER BADGES -->
                        <div style="margin: 0 0 12px 0; line-height: 1;">
                            <span style="display: inline-block; background-color: #E0F2FE; color: #0369A1; padding: 4px 8px; border-radius: 4px; font-size: 11px; font-weight: 700; text-transform: uppercase; margin-right: 6px; margin-bottom: 4px; vertical-align: middle;">{{.Tag}}</span>
                            {{if .HasBudget}}
                            <span style="display: inline-block; background-color: #D1FAE5; color: #065F46; padding: 4px 8px; border-radius: 4px; font-size: 11px; font-weight: 700; margin-bottom: 4px; vertical-align: middle;">💰 {{.Budget}} €</span>
                            {{end}}
                        </div>

                        <p style="margin: 0 0 8px 0; font-size: 15px; color: #4B5563; line-height: 1.6;">
                            <strong style="color: #111827;">Pourquoi ?</strong> {{.Context}}
                        </p>
                        <p style="margin: 0; font-size: 15px; color: #4B5563; line-height: 1.6;">
                            <strong style="color: #111827;">Concrètement :</strong> {{.Impact}}
                        </p>
                    </div>
                    {{end}}
                </div>
                {{end}}

                <!-- BRIEFS -->
                {{if .Briefs}}
                <div style="margin-bottom: 40px; background-color: #F9FAFB; padding: 25px; border-radius: 8px; border: 1px solid #E5E7EB;">
                    <h2 style="font-size: 15px; color: #4B5563; text-transform: uppercase; letter-spacing: 1px; margin: 0 0 15px 0;">Également au Conseil...</h2>
                    <ul style="margin: 0; padding-left: 20px; color: #374151; font-size: 14px; line-height: 1.6;">
                        {{range .Briefs}}
                        <li style="margin-bottom: 8px;"><strong>{{.Tag}} :</strong> {{.Summary}}</li>
                        {{end}}
                    </ul>
                </div>
                {{end}}

                <!-- REFERRAL CARD -->
                <div style="background-color: #F8FAFC; padding: 30px; border-radius: 12px; border: 1px solid #E2E8F0; margin-bottom: 35px; text-align: center;">
                    <p style="margin: 0 0 10px 0; font-size: 16px; color: #1E293B; font-weight: 700;">Vous trouvez ce service utile ?</p>
                    <p style="margin: 0 0 20px 0; font-size: 14px; color: #64748B; line-height: 1.5;">
                        L'Observatoire est un projet indépendant. Le meilleur moyen de nous soutenir est d'inviter un proche à s'abonner pour qu'il reçoive lui aussi l'essentiel de Bègles.
                    </p>
                    <div style="background-color: #ffffff; border: 1px dashed #CBD5E1; padding: 12px; border-radius: 6px; display: inline-block; width: 80%;">
                        <span style="font-family: monospace; color: #3B82F6; font-size: 14px; font-weight: 600;">lobservatoiredebegles.fr</span>
                    </div>
                    <p style="margin: 15px 0 0 0; font-size: 12px; color: #94A3B8; font-style: italic;">
                        Copiez et envoyez ce lien par WhatsApp ou SMS à vos amis béglais.
                    </p>
                </div>

                <!-- MAIN CTA -->
                <div style="text-align: center;">
                    <p style="font-size: 15px; color: #4B5563; margin-bottom: 20px; font-style: italic;">Ceci n'est qu'une synthèse. Les subtilités, débats et PDFs officiels vous attendent sur l'Observatoire.</p>
                    <a href="{{.WebsiteURL}}" style="display: inline-block; background-color: #111827; color: #FFFFFF; text-decoration: none; font-weight: 600; font-size: 16px; padding: 18px 36px; border-radius: 6px;">
                        Explorer les {{.TotalDelibsInCouncil}} délibérations
                    </a>
                </div>

            </div>

            <!-- FOOTER -->
            <div style="background-color: #F9FAFB; padding: 40px; text-align: left; border-top: 1px solid #E5E7EB;">
                <p style="margin: 0 0 15px 0; color: #374151; font-size: 14px; font-weight: 600;">Prochain Conseil : <span style="color: #111827;">{{.NextMeeting}}</span></p>
                <p style="margin: 0; color: #6B7280; font-size: 13px; line-height: 1.5;">{{.TotalCouncils}} Conseils analysés • {{.TotalDelibs}} Décryptages IA<br>Données publiques gratuites sourcées sur mairie-begles.fr</p>
                <p style="margin: 25px 0 0 0; color: #9CA3AF; font-size: 12px;">
                    Vous recevez cet email car vous êtes inscrit à l'Observatoire.<br>
                    <a href="#" style="color: #6B7280; text-decoration: underline;">Se désabonner</a>
                </p>
            </div>

        </div>
    </center>

</body>
</html>`
