package shared

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"
)

// ── Sensory-deprived deliberation view ───────────────────────────────────────

// ColdDeliberation is the whitelist of fields the newsletter-generation LLM
// may see. Prose fields (Summary, AnalysisData.*) are deliberately absent:
// every sentence the model writes must be deducible from these structured
// facts alone. Never add prose fields here.
type ColdDeliberation struct {
	Title           string // verbatim factual identifier; may be lightly cleaned, NO new facts
	TopicTag        string // enum
	BudgetImpact    int64
	BudgetType      string // enum
	HasVote         bool
	Pour            *int
	Contre          *int
	Abstention      *int
	ClimateImpact   string // enum
	IsSubstantial   bool
	HasDisagreement bool // derived from Disagreements != nil && != ""; pass the bool, not the prose
	Summary         string // Factual summary from worker
	Impacts         string // Factual citizen impacts from worker
}

// GeminiDeps carries the credentials needed to call Gemini.
type GeminiDeps struct {
	APIKey string
	Model  string
}

// ── Newsletter param types (exact Brevo template schema) ──────────────────────

type NewsletterParams struct {
	EmailSubject         string        `json:"email_subject"`
	CouncilTitle         string        `json:"council_title"`
	CouncilDate          string        `json:"council_date"`
	MainIssue            string        `json:"main_issue"`
	BudgetTotal          string        `json:"budget_total"`
	HasGlobalBudget      bool          `json:"has_global_budget"`
	VoteClimat           string        `json:"vote_climat"`
	ClimatColor          string        `json:"climat_color"`
	VoteStats            string        `json:"vote_stats"`
	TotalDelibsInCouncil int           `json:"total_delibs_in_council"`
	Tensions             []TensionItem `json:"tensions"`
	Adopted              []AdoptedItem `json:"adopted"`
	Briefs               []BriefItem   `json:"briefs"`
	NextMeeting          string        `json:"next_meeting"`
	WebsiteURL           string        `json:"website_url"`
	TotalCouncils        int           `json:"total_councils"`
	TotalDelibs          int           `json:"total_delibs"`
}

type TensionItem struct {
	Title       string `json:"title"`
	Context     string `json:"context"`
	Impact      string `json:"impact"`
	Budget      string `json:"budget"`
	HasBudget   bool   `json:"has_budget"`
	VoteDetails string `json:"vote_details"`
}

type AdoptedItem struct {
	Tag       string `json:"tag"`
	Title     string `json:"title"`
	Context   string `json:"context"`
	Impact    string `json:"impact"`
	Budget    string `json:"budget"`
	HasBudget bool   `json:"has_budget"`
}

type BriefItem struct {
	Tag     string `json:"tag"`
	Summary string `json:"summary"`
}

// ── Hardcoded site constants ───────────────────────────────────────────────────

const (
	newsletterEmailSubject = "L'Essentiel du Conseil"
	newsletterWebsiteURL   = "https://lobservatoiredebegles.fr"
)

// ── Internal stats (not exported) ─────────────────────────────────────────────

type coldNewsletterStats struct {
	totalBudget int64
	voteClimat  string
	climatColor string
	voteStats   string
	budgetFmt   string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func ptrFloat32(f float32) *float32 { return &f }

// budgetFloatRe strips decimal parts from Gemini's numeric fields.
var budgetFloatRe = regexp.MustCompile(`("(?:budget_total|total_councils|total_delibs)"\s*:\s*)(\d+)\.\d+`)

var frMonths = [13]string{"", "janvier", "février", "mars", "avril", "mai", "juin",
	"juillet", "août", "septembre", "octobre", "novembre", "décembre"}

func formatDateFR(isoDate string) string {
	t, err := time.Parse("2006-01-02", isoDate)
	if err != nil {
		return isoDate
	}
	return fmt.Sprintf("%d %s %d", t.Day(), frMonths[t.Month()], t.Year())
}

func formatBudgetFR(amount int64) string {
	if amount == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", amount)
	result := []byte{}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ' ')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}

// ── Stats computation ─────────────────────────────────────────────────────────

func computeColdNewsletterStats(cold []ColdDeliberation) coldNewsletterStats {
	var s coldNewsletterStats
	nonUnanimousCount := 0
	maxOpposition := 0
	totalPour := 0
	totalContre := 0

	var hasBudgetTopic bool
	var maxBudgetTopic int64
	var otherTopicsSum int64

	for _, d := range cold {
		if d.TopicTag == "Budget" {
			hasBudgetTopic = true
			if d.BudgetImpact > maxBudgetTopic {
				maxBudgetTopic = d.BudgetImpact
			}
		} else {
			otherTopicsSum += d.BudgetImpact
		}

		pour := 0
		if d.Pour != nil {
			pour = *d.Pour
		}
		totalPour += pour

		contre := 0
		if d.Contre != nil {
			contre = *d.Contre
		}
		totalContre += contre

		abst := 0
		if d.Abstention != nil {
			abst = *d.Abstention
		}

		if contre > maxOpposition {
			maxOpposition = contre
		}
		if contre > 0 || abst > 0 {
			nonUnanimousCount++
		}
	}

	if hasBudgetTopic {
		s.totalBudget = maxBudgetTopic
	} else {
		s.totalBudget = otherTopicsSum
	}

	totalVotesCast := totalPour + totalContre
	if totalVotesCast > 0 && float64(totalContre)/float64(totalVotesCast) > 0.10 {
		s.voteClimat = "VOTES PARTAGÉS"
		s.climatColor = "#E11D48" // Rose 600
	} else {
		s.voteClimat = "CONSENSUS"
		s.climatColor = "#059669" // Emerald 600
	}

	s.budgetFmt = formatBudgetFR(s.totalBudget)

	parts := []string{}
	if nonUnanimousCount > 0 {
		parts = append(parts, fmt.Sprintf("%d délib. non unanime%s", nonUnanimousCount, plural(nonUnanimousCount)))
		if maxOpposition > 0 {
			parts = append(parts, fmt.Sprintf("jusqu'à %d voix contre", maxOpposition))
		}
	} else {
		parts = append(parts, "Unanimité totale")
	}
	s.voteStats = strings.Join(parts, " / ")

	return s
}

// ── Gemini schema ─────────────────────────────────────────────────────────────

var newsletterSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"email_subject":           {Type: genai.TypeString},
		"council_title":           {Type: genai.TypeString},
		"council_date":            {Type: genai.TypeString},
		"main_issue":              {Type: genai.TypeString},
		"budget_total":            {Type: genai.TypeString},
		"has_global_budget":       {Type: genai.TypeBoolean},
		"vote_climat":             {Type: genai.TypeString},
		"climat_color":            {Type: genai.TypeString},
		"vote_stats":              {Type: genai.TypeString},
		"total_delibs_in_council": {Type: genai.TypeInteger},
		"tensions": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"title":        {Type: genai.TypeString},
					"context":      {Type: genai.TypeString},
					"impact":       {Type: genai.TypeString},
					"budget":       {Type: genai.TypeString},
					"has_budget":   {Type: genai.TypeBoolean},
					"vote_details": {Type: genai.TypeString},
				},
				PropertyOrdering: []string{"title", "context", "impact", "budget", "has_budget", "vote_details"},
				Required:         []string{"title", "context", "impact"},
			},
		},
		"adopted": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"tag":        {Type: genai.TypeString, Format: "enum", Enum: TopicTags},
					"title":      {Type: genai.TypeString},
					"context":    {Type: genai.TypeString},
					"impact":     {Type: genai.TypeString},
					"budget":     {Type: genai.TypeString},
					"has_budget": {Type: genai.TypeBoolean},
				},
				PropertyOrdering: []string{"tag", "title", "context", "impact", "budget", "has_budget"},
				Required:         []string{"tag", "title", "context", "impact"},
			},
		},
		"briefs": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"tag":     {Type: genai.TypeString, Format: "enum", Enum: TopicTags},
					"summary": {Type: genai.TypeString},
				},
				PropertyOrdering: []string{"tag", "summary"},
				Required:         []string{"tag", "summary"},
			},
		},
		"next_meeting":   {Type: genai.TypeString},
		"website_url":    {Type: genai.TypeString},
		"total_councils": {Type: genai.TypeInteger},
		"total_delibs":   {Type: genai.TypeInteger},
	},
	PropertyOrdering: []string{
		"email_subject", "council_title", "council_date", "main_issue",
		"budget_total", "has_global_budget", "vote_climat", "climat_color",
		"vote_stats", "total_delibs_in_council", "tensions", "adopted", "briefs",
		"next_meeting", "website_url", "total_councils", "total_delibs",
	},
	Required: []string{
		"email_subject", "council_title", "council_date", "main_issue",
		"tensions", "adopted", "briefs",
	},
}

// ── Prompt builder ────────────────────────────────────────────────────────────

func buildColdNewsletterPrompt(
	councilTitle, councilDate string,
	cold []ColdDeliberation,
	stats coldNewsletterStats,
	nextMeeting string,
	totalCouncils, totalDelibs int,
) string {
	var sb strings.Builder

	// Sensory-deprivation declaration (verbatim from spec).
	sb.WriteString("Tu reçois les faits structurés ci-dessous (incluant un titre, un résumé factuel, et l'impact citoyen extrait de chaque délibération). Tu n'as AUCUN accès au document PDF source. " +
		"N'invente rien. Chaque phrase doit être déductible de ces champs (montant, type, votes, catégorie, titre, résumé, impact).\n" +
		"Si un champ ne fournit aucune base factuelle pour 'context' ou 'impact', renvoie une chaîne vide. " +
		"Aucun jugement de valeur, aucune motivation politique, aucun fait historique ou géographique.\n\n")

	sb.WriteString("Tu es un vulgarisateur neutre et un traducteur factuel pour L'Observatoire de Bègles. " +
		"Tu n'es PAS journaliste : tu ne produis aucune ligne éditoriale, aucune interprétation politique, " +
		"aucun adjectif d'appréciation. Tu transformes des faits structurés en phrases simples et neutres.\n")
	sb.WriteString("Génère un objet JSON avec EXACTEMENT ce schéma (ne génère aucun texte en dehors) :\n\n")
	sb.WriteString(`{
  "email_subject": "laisse vide, imposé par le système",
  "council_title": "copie verbatim du council_title fourni ci-dessous",
  "council_date": "copie verbatim du council_date fourni ci-dessous",
  "main_issue": "1 à 2 phrases factuelles décrivant le ou les domaines au plus gros budget et la présence ou absence d'opposition. Aucune notion d'importance, d'enjeu politique ou de jugement de valeur. Déduis uniquement des faits structurés fournis.",
  "budget_total": "montant total voté (fourni ci-dessous, copie verbatim)",
  "has_global_budget": true,
  "vote_climat": "VOTES PARTAGÉS ou CONSENSUS (fourni ci-dessous, copie verbatim)",
  "climat_color": "code hex couleur (fourni ci-dessous, copie verbatim)",
  "vote_stats": "résumé votes (fourni ci-dessous, copie verbatim)",
  "total_delibs_in_council": 0,
  "tensions": [
    {
      "title": "Reformulation neutre et factuelle du titre fourni ; pas d'accroche, pas d'adjectif évaluatif",
      "context": "Neutre en 2 à 3 phrases maximum. Explique le besoin et le contexte en te basant sur le titre et le résumé (Résumé) fournis ci-dessous. Ne rajoute rien.",
      "impact": "Explique l'impact pratique et concret pour les habitants en 2 à 3 phrases maximum, en te basant sur le champ 'Impact' fourni. Reste factuel. Si le champ 'Impact' d'origine vaut 'Néant' ou est vide, laisse ce champ vide.",
      "budget": "X € (LAISSER VIDE '' SI IMPACT NUL)",
      "has_budget": true,
      "vote_details": "Y votes contre"
    }
  ],
  "adopted": [
    {
      "tag": "Administration, Sport, Budget, Sécurité, Environnement, Mobilité, Social, Culture, Urbanisme ou Éducation",
      "title": "Titre vulgarisé",
      "context": "2 à 3 phrases maximum. Explication factuelle du besoin en te basant sur le titre et le résumé (Résumé) fournis ci-dessous.",
      "impact": "Explique l'impact pratique et concret pour les habitants en 2 à 3 phrases maximum, en te basant sur le champ 'Impact' fourni. Reste factuel. Si le champ 'Impact' d'origine vaut 'Néant' ou est vide, laisse ce champ vide.",
      "budget": "X € (LAISSER VIDE '' SI IMPACT NUL)",
      "has_budget": true
    }
  ],
  "briefs": [
    {
      "tag": "Catégorie exacte",
      "summary": "Résumé ultra-court (1 à 2 phrases). Factuel, neutre. Déduis uniquement des faits structurés."
    }
  ],
  "next_meeting": "Date du prochain conseil (fourni ci-dessous, copie verbatim)",
  "website_url": "https://lobservatoiredebegles.fr",
  "total_councils": 0,
  "total_delibs": 0
}`)

	sb.WriteString("\n\nCONSIGNES ÉDITORIALES ET LOGIQUES :\n")
	sb.WriteString("- PRIORITÉ ABSOLUE : Toute délibération avec des votes contre ou division politique DOIT figurer dans 'tensions'.\n")
	sb.WriteString("- ENJEU CLÉ : Si le VOTE DES TAUX d'imposition est présent, il doit être le sujet prioritaire.\n")
	sb.WriteString("- HIÉRARCHISATION DES BUDGETS : Les délibérations adoptées avec les plus gros budgets (notamment les budgets supplémentaires, Comptes Financiers Uniques (CFU), Comptes Administratifs, etc.) DOIVENT figurer en priorité dans la section 'adopted' avec leurs détails, et non pas dans les simples résumés ('briefs').\n")
	sb.WriteString("- VULGARISATION INDEMNITÉS : Pour les indemnités des élus, explique simplement : 'Le conseil définit légalement la rémunération des élus pour leur travail, selon un barème national basé sur la taille de la ville'.\n")
	sb.WriteString("- INTERDICTION ABSOLUE DU JARGON COMPTABLE ET LÉGAL : Bannis tout vocabulaire administratif, technocratique ou juridique brut. Pas de codes d'imputation (ex: Chapitres budgétaires, articles comptables). Ne cite pas d'articles de loi bruts, utilise plutôt 'Conformément à la loi...'. Vulgarise systématiquement tous les acronymes ou termes techniques entre parenthèses lors de leur première apparition (ex: écrire 'CFU (le bilan financier de l'année passée)', 'CCAS (l'organisme d'action sociale de la ville)', 'AP/CP (la programmation pluriannuelle des investissements)', 'TPE (la taxe sur la publicité extérieure)', 'ZAC (zone d'aménagement concerté)', 'DSP (délégation de service public)').\n")
	sb.WriteString("- STYLE JOURNALISTIQUE PÉDAGOGIQUE : Traduis les termes administratifs complexes en langage clair. Par exemple, au lieu de parler de budget supplémentaire ou d'ajustements de crédits, explique : 'Le conseil ajuste les comptes en cours d'année pour réallouer l'argent là où les besoins sont les plus urgents.' Évite les répétitions vides ou tautologiques (ne pas écrire 'le budget est concentré sur le budget').\n")
	sb.WriteString("- HUMANISATION DES CHIFFRES : Dans les champs textuels ('context', 'impact', 'summary'), arrondis systématiquement les grands chiffres pour faciliter la lecture (ex: écris 'environ 7 millions d'euros' au lieu de '7 034 925,77 €'). Les montants exacts ne doivent figurer que dans le champ numérique 'budget'.\n")
	sb.WriteString("- RECADRAGE DES TENSIONS : Ne classe dans 'tensions' que les délibérations ayant fait l'objet d'une réelle contestation ou division politique (votes contre ou débat contradictoire). N'y inclus jamais des procédures administratives obligatoires (ex: le fait légal que le maire quitte la salle lors du vote de son propre bilan financier n'est pas une controverse, c'est une règle de procédure standard, indique alors 'Aucun désaccord, procédure standard.').\n")
	sb.WriteString("- NEUTRALITÉ ET IMPACTS : Pour le champ 'impact', décris les conséquences concrètes et opérationnelles en te basant sur le champ 'Impact' d'entrée. Bannis absolument toutes les notions subjectives ou politiques partisanes (comme 'le bien-être', 'la sécurité', 'le confort', 'le dynamisme'). Si aucun impact concret n'est mentionné ou s'il vaut 'Néant', laisse le champ vide.\n")
	sb.WriteString("- PÉDAGOGIE ET NEUTRALITÉ : Agis en traducteur neutre. Bannis le jargon juridique et administratif. N'utilise aucune formulation partisane.\n")
	sb.WriteString("- ANCRAGE STRICT : N'ajoute AUCUNE information qui n'est pas présente dans les données structurées d'entrée. Zéro fait géographique, historique ou éditorial externe.\n")
	sb.WriteString("- INTERDICTION FORMELLE : N'ajoute JAMAIS de liens HTML ou de texte 'En savoir plus' dans les champs context ou impact.\n")
	sb.WriteString("- CATÉGORISATION STRICTE : Police et Vidéoprotection → Sécurité. Clubs sportifs → Sport.\n")
	sb.WriteString("- AFFICHAGE CONDITIONNEL : Ne mentionne pas de budget ('0 €') si l'impact est nul. Laisse le champ budget vide.\n")
	sb.WriteString("- STYLE : descriptif, factuel, neutre. Phrases courtes. Aucun adjectif évaluatif (excellent, ambitieux, coûteux, important, crucial…).\n\n")

	fmt.Fprintf(&sb, "DONNÉES D'ENTRÉE :\n")
	fmt.Fprintf(&sb, "- council_title : %s\n", councilTitle)
	fmt.Fprintf(&sb, "- council_date : %s\n", formatDateFR(councilDate))
	fmt.Fprintf(&sb, "- Nombre total de délibérations ce jour : %d\n", len(cold))
	fmt.Fprintf(&sb, "- budget_total : %s\n", stats.budgetFmt)
	fmt.Fprintf(&sb, "- vote_climat : %s\n", stats.voteClimat)
	fmt.Fprintf(&sb, "- vote_stats : %s\n", stats.voteStats)
	fmt.Fprintf(&sb, "- next_meeting : %s\n", nextMeeting)
	fmt.Fprintf(&sb, "- total_councils : %d\n", totalCouncils)
	fmt.Fprintf(&sb, "- total_delibs : %d\n\n", totalDelibs)

	// ── Filtering funnel (relevance entonnoir) ────────────────────────────────
	var tensions []ColdDeliberation
	var major []ColdDeliberation
	var local []ColdDeliberation

	for _, d := range cold {
		contre := 0
		if d.Contre != nil {
			contre = *d.Contre
		}
		abst := 0
		if d.Abstention != nil {
			abst = *d.Abstention
		}
		if contre > 0 || (d.HasDisagreement && (contre > 0 || abst > 0)) {
			tensions = append(tensions, d)
			continue
		}
		if d.IsSubstantial || d.BudgetImpact >= 5000 {
			major = append(major, d)
			continue
		}
		isPlaisir := d.TopicTag == "Sport" || d.TopicTag == "Culture" || d.TopicTag == "Social"
		if d.BudgetImpact >= 500 && isPlaisir {
			local = append(local, d)
			continue
		}
		// Below thresholds and non-contentious: excluded (bruit).
	}

	sb.WriteString("\nDÉLIBÉRATIONS AVEC OPPOSITION (A METTRE DANS tensions[]) :\n")
	for _, d := range tensions {
		contre := 0
		if d.Contre != nil {
			contre = *d.Contre
		}
		abst := 0
		if d.Abstention != nil {
			abst = *d.Abstention
		}
		pour := 0
		if d.Pour != nil {
			pour = *d.Pour
		}
		fmt.Fprintf(&sb, "- Titre: %s\n  Tag: %s | Budget: %d€ | Type: %s | Vote: %d/%d/%d (pour/contre/abst)\n  Résumé: %s\n  Impact: %s\n\n",
			d.Title, d.TopicTag, d.BudgetImpact, d.BudgetType,
			pour, contre, abst, d.Summary, d.Impacts)
	}
	if len(tensions) == 0 {
		sb.WriteString("(néant)\n")
	}

	sb.WriteString("\nDÉLIBÉRATIONS ADOPTÉES SIGNIFICATIVES (A FILTRER POUR adopted[] et briefs[]) :\n")
	significant := append(major, local...)
	for _, d := range significant {
		fmt.Fprintf(&sb, "- Titre: %s\n  Tag: %s | Budget: %d€ | Type: %s\n  Résumé: %s\n  Impact: %s\n\n",
			d.Title, d.TopicTag, d.BudgetImpact, d.BudgetType, d.Summary, d.Impacts)
	}
	if len(significant) == 0 {
		sb.WriteString("(néant)\n")
	}

	return sb.String()
}

// ── Public generation entry point ─────────────────────────────────────────────

// GenerateNewsletterParams calls Gemini with a sensory-deprived prompt (only
// ColdDeliberation structured fields; no source prose) and returns the parsed,
// post-processed NewsletterParams ready for Brevo. Circuit-breaker recording
// is the caller's responsibility.
func GenerateNewsletterParams(
	ctx context.Context,
	deps GeminiDeps,
	councilTitle, councilDate string,
	cold []ColdDeliberation,
	nextMeeting string,
	totalCouncils, totalDelibs int,
) (*NewsletterParams, error) {
	stats := computeColdNewsletterStats(cold)
	prompt := buildColdNewsletterPrompt(councilTitle, councilDate, cold, stats, nextMeeting, totalCouncils, totalDelibs)

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      deps.APIKey,
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta"},
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}

	resp, err := CallGeminiWithRetry(ctx, func(ctx context.Context) (*genai.GenerateContentResponse, error) {
		return client.Models.GenerateContent(
			ctx,
			deps.Model,
			[]*genai.Content{{
				Role:  "user",
				Parts: []*genai.Part{{Text: prompt}},
			}},
			&genai.GenerateContentConfig{
				Temperature:      ptrFloat32(0),
				ResponseMIMEType: "application/json",
				ResponseSchema:   newsletterSchema,
				MaxOutputTokens:  8192,
			},
		)
	}, 4)
	if err != nil {
		return nil, fmt.Errorf("gemini generate: %w", err)
	}
	if len(resp.Candidates) == 0 ||
		resp.Candidates[0].Content == nil ||
		len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned empty response")
	}

	raw := resp.Candidates[0].Content.Parts[0].Text
	params, err := ParseNewsletterParams(raw)
	if err != nil {
		return nil, err
	}

	// Override site constants (schema asks Gemini to copy them; override to be safe).
	params.EmailSubject = newsletterEmailSubject
	params.WebsiteURL = newsletterWebsiteURL
	params.HasGlobalBudget = params.BudgetTotal != "" && params.BudgetTotal != "0"

	// Re-format budget strings: extract raw integer from whatever Gemini emitted
	// (e.g. "20 000 €", "20000", "20.000") and produce canonical "X XXX" spacing.
	reNonDigit := regexp.MustCompile(`\D`)
	formatBudgetStr := func(s string) string {
		if s == "" {
			return ""
		}
		digitsOnly := reNonDigit.ReplaceAllString(s, "")
		if digitsOnly == "" {
			return ""
		}
		val, err := strconv.ParseInt(digitsOnly, 10, 64)
		if err != nil || val == 0 {
			return ""
		}
		return formatBudgetFR(val)
	}

	// Strip any link text the model may have appended despite the rule.
	reLink := regexp.MustCompile(`(?i)\s*(en savoir plus[^.]*|voir sur le site[^.]*|→[^\n]*)$`)
	stripLinks := func(s string) string {
		return strings.TrimSpace(reLink.ReplaceAllString(s, ""))
	}

	for i := range params.Tensions {
		params.Tensions[i].Budget = formatBudgetStr(params.Tensions[i].Budget)
		params.Tensions[i].HasBudget = params.Tensions[i].Budget != ""
		params.Tensions[i].Context = stripLinks(params.Tensions[i].Context)
		params.Tensions[i].Impact = stripLinks(params.Tensions[i].Impact)
	}
	for i := range params.Adopted {
		params.Adopted[i].Budget = formatBudgetStr(params.Adopted[i].Budget)
		params.Adopted[i].HasBudget = params.Adopted[i].Budget != ""
		params.Adopted[i].Context = stripLinks(params.Adopted[i].Context)
		params.Adopted[i].Impact = stripLinks(params.Adopted[i].Impact)
	}
	for i := range params.Briefs {
		params.Briefs[i].Summary = stripLinks(params.Briefs[i].Summary)
	}

	return params, nil
}

// ParseNewsletterParams parses a raw JSON string (possibly wrapped in markdown
// fences) into NewsletterParams. Exported so callers can test the parser in
// isolation without a Gemini call.
func ParseNewsletterParams(raw string) (*NewsletterParams, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	// Normalize any floats in numeric integer fields.
	raw = budgetFloatRe.ReplaceAllString(raw, "${1}${2}")

	var p NewsletterParams
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("unmarshal newsletter params: %w (raw: %.200s)", err, raw)
	}
	return &p, nil
}
