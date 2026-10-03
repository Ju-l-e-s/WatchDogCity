package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"
)

// ── Sensory-deprived deliberation view ───────────────────────────────────────

// ColdDeliberation is the whitelist of facts the newsletter-generation LLM
// may see. Summary and Impacts have already passed the PDF fact check.
type ColdDeliberation struct {
	ID              string // source PDF identifier, stable within the council
	Title           string // verbatim factual identifier; may be lightly cleaned, NO new facts
	TopicTag        string // enum
	BudgetImpact    int64
	BudgetType      string // enum
	BudgetNote      string // source-grounded period, components and conditions
	HasVote         bool
	Pour            *int
	Contre          *int
	Abstention      *int
	ClimateImpact   string // enum
	IsSubstantial   bool
	HasDisagreement bool   // derived from Disagreements != nil && != ""; pass the bool, not the prose
	Summary         string // Factual summary from worker
	Decision        string // What the council actually approved or authorized
	Impacts         string // Factual citizen impacts from worker
}

// GeminiDeps carries the credentials needed to call Gemini.
type GeminiDeps struct {
	APIKey string
	Model  string
}

// ── Newsletter param types (exact Brevo template schema) ──────────────────────

type NewsletterParams struct {
	FactCheckVersion     int           `json:"fact_check_version"`
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
	SourceID    string `json:"source_id"`
	Title       string `json:"title"`
	Context     string `json:"context"`
	Impact      string `json:"impact"`
	Budget      string `json:"budget"`
	HasBudget   bool   `json:"has_budget"`
	VoteDetails string `json:"vote_details"`
}

type AdoptedItem struct {
	SourceID  string `json:"source_id"`
	Tag       string `json:"tag"`
	Title     string `json:"title"`
	Context   string `json:"context"`
	Impact    string `json:"impact"`
	Budget    string `json:"budget"`
	HasBudget bool   `json:"has_budget"`
}

type BriefItem struct {
	SourceID string `json:"source_id"`
	Tag      string `json:"tag"`
	Summary  string `json:"summary"`
}

// ── Hardcoded site constants ───────────────────────────────────────────────────

const (
	newsletterEmailSubject = "L'Essentiel du Conseil"
	newsletterWebsiteURL   = "https://lobservatoiredebegles.fr"
)

// ── Internal stats (not exported) ─────────────────────────────────────────────

type coldNewsletterStats struct {
	voteClimat  string
	climatColor string
	voteStats   string
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
	hasRecordedVote := false
	hasAbstention := false

	for _, d := range cold {
		contre := 0
		if d.Contre != nil {
			contre = *d.Contre
		}
		abst := 0
		if d.Abstention != nil {
			abst = *d.Abstention
		}
		if d.HasVote && (d.Pour != nil || d.Contre != nil || d.Abstention != nil) {
			hasRecordedVote = true
		}
		if abst > 0 {
			hasAbstention = true
		}

		if contre > maxOpposition {
			maxOpposition = contre
		}
		if contre > 0 || abst > 0 {
			nonUnanimousCount++
		}
	}

	if maxOpposition > 0 {
		s.voteClimat = "VOTES AVEC OPPOSITION"
		s.climatColor = "#E11D48" // Rose 600
	} else if hasAbstention {
		s.voteClimat = "VOTES AVEC ABSTENTIONS"
		s.climatColor = "#B45309" // Amber 700
	} else if hasRecordedVote {
		s.voteClimat = "VOTES UNANIMES"
		s.climatColor = "#059669" // Emerald 600
	} else {
		s.voteClimat = "VOTES NON RENSEIGNÉS"
		s.climatColor = "#6B7280" // Gray 500
	}

	parts := []string{}
	if nonUnanimousCount > 0 {
		parts = append(parts, fmt.Sprintf("%d délib. non unanime%s", nonUnanimousCount, plural(nonUnanimousCount)))
		if maxOpposition > 0 {
			parts = append(parts, fmt.Sprintf("jusqu'à %d voix contre", maxOpposition))
		}
	} else {
		if hasRecordedVote {
			parts = append(parts, "Aucun vote non unanime renseigné")
		} else {
			parts = append(parts, "Résultats des votes non renseignés")
		}
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
					"source_id":    {Type: genai.TypeString},
					"title":        {Type: genai.TypeString},
					"context":      {Type: genai.TypeString},
					"impact":       {Type: genai.TypeString},
					"budget":       {Type: genai.TypeString},
					"has_budget":   {Type: genai.TypeBoolean},
					"vote_details": {Type: genai.TypeString},
				},
				PropertyOrdering: []string{"source_id", "title", "context", "impact", "budget", "has_budget", "vote_details"},
				Required:         []string{"source_id", "title", "context", "impact"},
			},
		},
		"adopted": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"source_id":  {Type: genai.TypeString},
					"tag":        {Type: genai.TypeString, Format: "enum", Enum: TopicTags},
					"title":      {Type: genai.TypeString},
					"context":    {Type: genai.TypeString},
					"impact":     {Type: genai.TypeString},
					"budget":     {Type: genai.TypeString},
					"has_budget": {Type: genai.TypeBoolean},
				},
				PropertyOrdering: []string{"source_id", "tag", "title", "context", "impact", "budget", "has_budget"},
				Required:         []string{"source_id", "tag", "title", "context", "impact"},
			},
		},
		"briefs": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"source_id": {Type: genai.TypeString},
					"tag":       {Type: genai.TypeString, Format: "enum", Enum: TopicTags},
					"summary":   {Type: genai.TypeString},
				},
				PropertyOrdering: []string{"source_id", "tag", "summary"},
				Required:         []string{"source_id", "tag", "summary"},
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
  "main_issue": "laisse vide, imposé par le système à partir des décisions adoptées sélectionnées",
  "budget_total": "laisse vide : recettes, dépenses et cautions ne forment pas un total comparable",
  "has_global_budget": false,
  "vote_climat": "libellé calculé (fourni ci-dessous, copie verbatim)",
  "climat_color": "code hex couleur (fourni ci-dessous, copie verbatim)",
  "vote_stats": "résumé votes (fourni ci-dessous, copie verbatim)",
  "total_delibs_in_council": 0,
  "tensions": [
    {
      "source_id": "copie exacte de l'ID de la délibération fournie",
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
      "source_id": "copie exacte de l'ID de la délibération fournie",
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
      "source_id": "copie exacte de l'ID de la délibération fournie",
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
	sb.WriteString("- PRIORITÉ ABSOLUE : Toute délibération avec des votes contre DOIT figurer dans 'tensions'. Une abstention seule peut y figurer seulement si un désaccord est explicitement documenté. Une abstention ne prouve pas qu'un débat a eu lieu.\n")
	sb.WriteString("- TRAÇABILITÉ : Copie l'ID source de chaque délibération dans source_id. Une délibération ne doit apparaître qu'une seule fois entre les trois sections. N'invente ni ID, ni décision, ni montant.\n")
	sb.WriteString("- ORDRE DES DÉCISIONS ADOPTÉES : Classe d'abord les décisions aux conséquences concrètes les plus larges pour les habitants. Si le VOTE DES TAUX d'imposition est présent, place-le en premier. Les deux premières décisions alimentent l'introduction : choisis des décisions distinctes. Leur première phrase de contexte doit commencer par l'acteur ('Le conseil municipal', 'La ville') et le verbe de sa décision ('approuve', 'attribue', 'autorise', 'adopte'), puis nommer l'objet sans pronom qui renvoie à une phrase précédente. Place l'explication du contexte après cette phrase.\n")
	sb.WriteString("- HIÉRARCHISATION DES BUDGETS : Les délibérations adoptées avec les plus gros budgets (notamment les budgets supplémentaires, Comptes Financiers Uniques (CFU), Comptes Administratifs, etc.) DOIVENT figurer en priorité dans la section 'adopted' avec leurs détails, et non pas dans les simples résumés ('briefs').\n")
	sb.WriteString("- VULGARISATION INDEMNITÉS : Pour les indemnités des élus, explique simplement : 'Le conseil définit légalement la rémunération des élus pour leur travail, selon un barème national basé sur la taille de la ville'.\n")
	sb.WriteString("- INTERDICTION ABSOLUE DU JARGON COMPTABLE ET LÉGAL : Bannis tout vocabulaire administratif, technocratique ou juridique brut. Pas de codes d'imputation (ex: Chapitres budgétaires, articles comptables). Ne cite pas d'articles de loi bruts, utilise plutôt 'Conformément à la loi...'. Vulgarise systématiquement tous les acronymes ou termes techniques entre parenthèses lors de leur première apparition (ex: écrire 'CFU (le bilan financier de l'année passée)', 'CCAS (l'organisme d'action sociale de la ville)', 'AP/CP (la programmation pluriannuelle des investissements)', 'TPE (la taxe sur la publicité extérieure)', 'ZAC (zone d'aménagement concerté)', 'DSP (délégation de service public)').\n")
	sb.WriteString("- STYLE JOURNALISTIQUE PÉDAGOGIQUE : Traduis les termes administratifs complexes en langage clair. Par exemple, au lieu de parler de budget supplémentaire ou d'ajustements de crédits, explique : 'Le conseil ajuste les comptes en cours d'année pour réallouer l'argent là où les besoins sont les plus urgents.' Évite les répétitions vides ou tautologiques (ne pas écrire 'le budget est concentré sur le budget').\n")
	sb.WriteString("- HUMANISATION DES CHIFFRES : Dans les champs textuels ('context', 'impact', 'summary'), arrondis systématiquement les grands chiffres pour faciliter la lecture (ex: écris 'environ 7 millions d'euros' au lieu de '7 034 925,77 €'). Les montants exacts ne doivent figurer que dans le champ numérique 'budget'.\n")
	sb.WriteString("- RECADRAGE DES TENSIONS : Ne qualifie jamais un vote non unanime de débat ou de controverse sans preuve explicite. Décris séparément les voix contre et les abstentions. N'y inclus jamais les procédures administratives obligatoires.\n")
	sb.WriteString("- NEUTRALITÉ ET IMPACTS : Pour le champ 'impact', décris les conséquences concrètes et opérationnelles en te basant sur le champ 'Impact' d'entrée. Bannis absolument toutes les notions subjectives ou politiques partisanes (comme 'le bien-être', 'la sécurité', 'le confort', 'le dynamisme'). Si aucun impact concret n'est mentionné ou s'il vaut 'Néant', laisse le champ vide.\n")
	sb.WriteString("- PÉDAGOGIE ET NEUTRALITÉ : Agis en traducteur neutre. Bannis le jargon juridique et administratif. N'utilise aucune formulation partisane.\n")
	sb.WriteString("- ANCRAGE STRICT : N'ajoute AUCUNE information qui n'est pas présente dans les données structurées d'entrée. Zéro fait géographique, historique ou éditorial externe.\n")
	sb.WriteString("- PARTS ET SOUS-ENSEMBLES : Si un projet comprend plusieurs catégories (par exemple logements sociaux et logements intermédiaires), conserve les quantités de chaque catégorie ; ne présente jamais l'ensemble comme appartenant à une seule catégorie.\n")
	sb.WriteString("- PORTÉE DES CHIFFRES : Un total (par exemple une surface de terrains cédés pour une piste cyclable et des espaces paysagers) ne décrit pas automatiquement la taille de chacune de ses composantes. N'attribue une quantité à une composante que si les champs d'entrée l'indiquent explicitement et sans contradiction.\n")
	sb.WriteString("- MONTANTS RÉCURRENTS : Si une recette ou une dépense est annuelle ou mensuelle, indique explicitement sa période dans le contexte de la décision. Si une recette comporte une part fixe et une part variable, explique cette distinction ; ne présente pas le montant de base comme le total définitif.\n")
	sb.WriteString("- STADE DES FAITS : Distingue un projet, une demande, une autorisation du conseil, une convention signée, un paiement reçu et des travaux réalisés. Un vote qui autorise une vente ou une signature ne prouve pas que l'acte est signé ; une subvention approuvée ne prouve pas qu'elle est versée. Ne transforme pas une finalité ('pour maintenir 64 places') en résultat acquis.\n")
	sb.WriteString("- FORMULATION DU STADE : Si le Conseil a seulement approuvé une cession, un projet de bail ou une convention et autorisé sa signature, écris 'le conseil approuve la cession/le projet de bail/la convention' ou 'les terrains dont la cession est approuvée'. Conserve le mot 'projet' si la source l'emploie. N'écris pas 'terrains cédés', 'le bail est renouvelé' ou 'la convention est signée/reconduite' sans preuve de réalisation dans la source. Applique cette règle aussi aux brefs.\n")
	sb.WriteString("- CHRONOLOGIE : Conserve les dates d'effet et les périodes indiquées dans la source. Des travaux décrits comme déjà réalisés ne doivent jamais être annoncés au futur. Évite le futur affirmatif pour un projet simplement prévu ou autorisé.\n")
	sb.WriteString("- INTERDICTION FORMELLE : N'ajoute JAMAIS de liens HTML ou de texte 'En savoir plus' dans les champs context ou impact.\n")
	sb.WriteString("- CATÉGORISATION STRICTE : Police et Vidéoprotection → Sécurité. Clubs sportifs → Sport.\n")
	sb.WriteString("- AFFICHAGE CONDITIONNEL : Ne mentionne pas de budget ('0 €') si l'impact est nul. Laisse le champ budget vide.\n")
	sb.WriteString("- STYLE : descriptif, factuel, neutre. Phrases courtes. Aucun adjectif évaluatif (excellent, ambitieux, coûteux, important, crucial…).\n\n")

	fmt.Fprintf(&sb, "DONNÉES D'ENTRÉE :\n")
	fmt.Fprintf(&sb, "- council_title : %s\n", councilTitle)
	fmt.Fprintf(&sb, "- council_date : %s\n", formatDateFR(councilDate))
	fmt.Fprintf(&sb, "- Nombre total de délibérations ce jour : %d\n", len(cold))
	sb.WriteString("- budget_total : non calculé (flux financiers hétérogènes)\n")
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
		if contre > 0 || (d.HasDisagreement && abst > 0) {
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

	sb.WriteString("\nDÉLIBÉRATIONS AVEC VOTE NON UNANIME ET DÉSACCORD DOCUMENTÉ (A METTRE DANS tensions[]) :\n")
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
		fmt.Fprintf(&sb, "- ID: %s\n  Titre: %s\n  Tag: %s | Budget: %d€ | Type: %s | Note financière: %s | Vote: %d/%d/%d (pour/contre/abst)\n  Décision votée: %s\n  Résumé: %s\n  Impact: %s\n\n",
			d.ID, d.Title, d.TopicTag, d.BudgetImpact, d.BudgetType, d.BudgetNote,
			pour, contre, abst, d.Decision, d.Summary, d.Impacts)
	}
	if len(tensions) == 0 {
		sb.WriteString("(néant)\n")
	}

	sb.WriteString("\nDÉLIBÉRATIONS ADOPTÉES SIGNIFICATIVES (A FILTRER POUR adopted[] et briefs[]) :\n")
	significant := append(major, local...)
	for _, d := range significant {
		fmt.Fprintf(&sb, "- ID: %s\n  Titre: %s\n  Tag: %s | Budget: %d€ | Type: %s | Note financière: %s\n  Décision votée: %s\n  Résumé: %s\n  Impact: %s\n\n",
			d.ID, d.Title, d.TopicTag, d.BudgetImpact, d.BudgetType, d.BudgetNote, d.Decision, d.Summary, d.Impacts)
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

	for attempt := 0; attempt < 3; attempt++ {
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
					// A full council can include dozens of selected items plus model
					// reasoning tokens. An 8192-token cap intermittently truncated JSON.
					MaxOutputTokens: 32768,
				},
			)
		}, 4)
		if err != nil {
			return nil, fmt.Errorf("gemini generate: %w", err)
		}
		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
			return nil, fmt.Errorf("gemini returned empty response")
		}
		params, err := ParseNewsletterParams(resp.Candidates[0].Content.Parts[0].Text)
		if err == nil {
			err = validateNewsletterSourceLinks(params, cold)
		}
		if err == nil {
			err = validateNewsletterIntro(params)
		}
		if err == nil {
			finalizeNewsletterParams(params, councilDate, cold, stats, nextMeeting, totalCouncils, totalDelibs)
			err = VerifyNewsletterFacts(ctx, deps, cold, params)
		}
		if err == nil {
			params.FactCheckVersion = 1
			return params, nil
		}
		var factErr *NewsletterFactCheckError
		if attempt == 2 || !errors.As(err, &factErr) || len(factErr.Issues) == 0 {
			return nil, err
		}
		// A content error can be repaired once with precise feedback. Every
		// candidate still passes the same source-link and fact-check gates.
		var feedback strings.Builder
		feedback.WriteString("\n\nCORRECTIONS OBLIGATOIRES AVANT PUBLICATION :\n")
		for i, issue := range factErr.Issues {
			if i == 6 {
				break
			}
			fmt.Fprintf(&feedback, "- %s : %s — %s\n", issue.Location, issue.Claim, issue.Reason)
		}
		feedback.WriteString("Réécris la newsletter depuis les données sources et corrige toutes ces affirmations. Conserve les identifiants source.\n")
		prompt += feedback.String()
	}
	return nil, fmt.Errorf("newsletter generation exhausted fact-check attempts")
}

// The introduction is assembled from the first sentence of the first two
// adopted items. Require those sentences to name the decision and its actor,
// so a background sentence cannot become the newsletter's headline.
func validateNewsletterIntro(params *NewsletterParams) error {
	for i, item := range params.Adopted {
		if i == 2 {
			break
		}
		first := strings.TrimSpace(strings.SplitN(item.Context, ". ", 2)[0])
		lower := strings.ToLower(first)
		actor := strings.HasPrefix(lower, "le conseil ") || strings.HasPrefix(lower, "la ville ") ||
			strings.HasPrefix(lower, "le maire ") || strings.HasPrefix(lower, "une subvention ") ||
			strings.HasPrefix(lower, "un financement ")
		action := strings.Contains(lower, "approuv") || strings.Contains(lower, "attribu") ||
			strings.Contains(lower, "accord") || strings.Contains(lower, "autoris") ||
			strings.Contains(lower, "décid") || strings.Contains(lower, "adopt") ||
			strings.Contains(lower, "vote") || strings.Contains(lower, "met en place") ||
			strings.Contains(lower, "cré") || strings.Contains(lower, "lanc") ||
			strings.Contains(lower, "financ") || strings.Contains(lower, "instaur") ||
			strings.Contains(lower, "fixe")
		if !actor || !action {
			return &NewsletterFactCheckError{Issues: []NewsletterFactIssue{{
				Location: fmt.Sprintf("adopted[%d].context", i), Claim: first,
				Reason: "La première phrase doit commencer par une décision autonome du conseil ou de la ville, avec son acteur et son action ; place le contexte ensuite.",
			}}}
		}
	}
	return nil
}

// finalizeNewsletterParams keeps factual fields outside the model's control.
func finalizeNewsletterParams(params *NewsletterParams, councilDate string,
	cold []ColdDeliberation, stats coldNewsletterStats, nextMeeting string,
	totalCouncils, totalDelibs int,
) {
	params.EmailSubject = newsletterEmailSubject
	params.WebsiteURL = newsletterWebsiteURL
	// The municipal page title can contain a typo in its year. The approved
	// council date is the canonical source for the newsletter heading.
	params.CouncilTitle = "Conseil municipal du " + formatDateFR(councilDate)
	params.CouncilDate = formatDateFR(councilDate)
	params.NextMeeting = nextMeeting
	params.TotalDelibsInCouncil = len(cold)
	params.TotalCouncils = totalCouncils
	params.TotalDelibs = totalDelibs
	params.VoteClimat = stats.voteClimat
	params.ClimatColor = stats.climatColor
	params.VoteStats = stats.voteStats
	// A sum of expenses, receipts and guarantees is not a meaningful financial impact.
	// Keep each decision's amount below, but hide the aggregate tile.
	params.BudgetTotal = ""
	params.HasGlobalBudget = false

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
		if params.Tensions[i].HasBudget && !sourceAmountQualified(params.Tensions[i].SourceID, params.Tensions[i].Budget, params.Tensions[i].Context+" "+params.Tensions[i].Impact, cold) {
			params.Tensions[i].Budget = ""
			params.Tensions[i].HasBudget = false
		}
		for _, d := range cold {
			if d.ID == "" || d.ID != params.Tensions[i].SourceID {
				continue
			}
			parts := []string{}
			if d.Contre != nil && *d.Contre > 0 {
				parts = append(parts, fmt.Sprintf("%d vote%s contre", *d.Contre, plural(*d.Contre)))
			}
			if d.Abstention != nil && *d.Abstention > 0 {
				parts = append(parts, fmt.Sprintf("%d abstention%s", *d.Abstention, plural(*d.Abstention)))
			}
			params.Tensions[i].VoteDetails = strings.Join(parts, ", ")
			break
		}
	}
	for i := range params.Adopted {
		params.Adopted[i].Budget = formatBudgetStr(params.Adopted[i].Budget)
		params.Adopted[i].HasBudget = params.Adopted[i].Budget != ""
		params.Adopted[i].Context = stripLinks(params.Adopted[i].Context)
		params.Adopted[i].Impact = stripLinks(params.Adopted[i].Impact)
		// A bare number is misleading for recurring rents and fees. When the
		// model omits the period or a variable part, hide the amount badge.
		if params.Adopted[i].HasBudget && !sourceAmountQualified(params.Adopted[i].SourceID, params.Adopted[i].Budget, params.Adopted[i].Context+" "+params.Adopted[i].Impact, cold) {
			params.Adopted[i].Budget = ""
			params.Adopted[i].HasBudget = false
		}
	}
	for i := range params.Briefs {
		params.Briefs[i].Summary = stripLinks(params.Briefs[i].Summary)
	}
	params.MainIssue = deterministicMainIssue(params)

}

func mentionsBudgetPeriod(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "annuel") || strings.Contains(s, "mensuel") ||
		strings.Contains(s, "par an") || strings.Contains(s, "par mois")
}

func sourceAmountQualified(sourceID, budget, copyText string, cold []ColdDeliberation) bool {
	amount, err := strconv.ParseInt(strings.ReplaceAll(budget, " ", ""), 10, 64)
	if err != nil {
		return true
	}
	copyText = strings.ToLower(copyText)
	for _, d := range cold {
		if sourceID != "" && d.ID != sourceID {
			continue
		}
		if d.BudgetImpact != amount {
			continue
		}
		sourceInfo := strings.ToLower(d.Summary + " " + d.BudgetNote)
		if mentionsBudgetPeriod(sourceInfo) && !mentionsBudgetPeriod(copyText) {
			return false
		}
		if strings.Contains(sourceInfo, "variable") && !strings.Contains(copyText, "variable") {
			return false
		}
		budgetNote := strings.ToLower(d.BudgetNote)
		if (strings.Contains(budgetNote, "maxim") || strings.Contains(budgetNote, "plafond")) &&
			!(strings.Contains(copyText, "maxim") || strings.Contains(copyText, "plafond") || strings.Contains(copyText, "jusqu'à")) {
			return false
		}
	}
	return true
}

// deterministicMainIssue reuses a sentence describing the council's action
// from each of the first two adopted decisions. A context paragraph may start
// with background information, which makes a weak or misleading introduction.
func deterministicMainIssue(params *NewsletterParams) string {
	var sentences []string
	for _, item := range params.Adopted {
		context := strings.TrimSpace(item.Context)
		if context == "" {
			continue
		}
		parts := strings.Split(context, ". ")
		selected := strings.TrimSpace(parts[0])
		for _, part := range parts {
			lower := strings.ToLower(strings.TrimSpace(part))
			if (strings.Contains(lower, "conseil") || strings.Contains(lower, "ville") || strings.Contains(lower, "maire")) &&
				(strings.Contains(lower, "approuv") || strings.Contains(lower, "attribu") || strings.Contains(lower, "accord") ||
					strings.Contains(lower, "autoris") || strings.Contains(lower, "décid") || strings.Contains(lower, "adopt") ||
					strings.Contains(lower, "vote") || strings.Contains(lower, "met en place")) {
				selected = strings.TrimSpace(part)
				break
			}
		}
		if !strings.HasSuffix(selected, ".") {
			selected += "."
		}
		selected = strings.Replace(selected, "La Ville ", "La ville ", 1)
		if len(sentences) == 1 && strings.HasPrefix(sentences[0], "La ville ") && strings.HasPrefix(selected, "La ville ") {
			selected = "Elle " + strings.TrimPrefix(selected, "La ville ")
		}
		sentences = append(sentences, selected)
		if len(sentences) == 2 {
			break
		}
	}
	if len(sentences) == 0 {
		return "Cette édition présente les décisions du conseil et les résultats des votes renseignés."
	}
	return strings.Join(sentences, " ")
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
