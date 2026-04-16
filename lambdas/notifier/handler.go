package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/genai"
)

const brevoBaseURL = "https://api.brevo.com/v3"

// budgetFloatRe strips decimal parts from Gemini's numeric fields.
var budgetFloatRe = regexp.MustCompile(`("(?:budget_total|total_councils|total_delibs)"\s*:\s*)(\d+)\.\d+`)

// ── Input event ───────────────────────────────────────────────────────────────

type NotifierEvent struct {
	CouncilID string `json:"council_id"`
}

// ── DynamoDB records (minimal projection) ────────────────────────────────────

type councilRec struct {
	CouncilID string `dynamodbav:"council_id"`
	Title     string `dynamodbav:"title"`
	Date      string `dynamodbav:"date"`
}

type deliberationRec struct {
	ID           string  `dynamodbav:"id"`
	CouncilID    string  `dynamodbav:"council_id"`
	Title        string  `dynamodbav:"title"`
	TopicTag     string  `dynamodbav:"topic_tag"`
	Summary      string  `dynamodbav:"summary"`
	BudgetImpact int64   `dynamodbav:"budget_impact"`
	VotePour     *int    `dynamodbav:"vote_pour"`
	VoteContre   *int    `dynamodbav:"vote_contre"`
	VoteAbst     *int    `dynamodbav:"vote_abstention"`
	Disagreements *string `dynamodbav:"disagreements"`
	AnalysisData struct {
		Contexte *string `dynamodbav:"contexte"`
		Decision *string `dynamodbav:"decision"`
		Impacts  *string `dynamodbav:"impacts"`
	} `dynamodbav:"analysis_data"`
}

// ── Newsletter params (exact Brevo template schema) ───────────────────────────

type NewsletterParams struct {
	EmailSubject  string        `json:"email_subject"`
	CouncilTitle  string        `json:"council_title"`
	CouncilDate   string        `json:"council_date"`
	MainIssue     string        `json:"main_issue"`
	BudgetTotal   string        `json:"budget_total"`
	VoteClimat    string        `json:"vote_climat"`
	ClimatColor   string        `json:"climat_color"`
	VoteStats     string        `json:"vote_stats"`
	Tensions      []TensionItem `json:"tensions"`
	Adopted       []AdoptedItem `json:"adopted"`
	NextMeeting   string        `json:"next_meeting"`
	TotalCouncils int           `json:"total_councils"`
	TotalDelibs   int           `json:"total_delibs"`
}

type TensionItem struct {
	Title       string `json:"title"`
	Context     string `json:"context"`
	Impact      string `json:"impact"`
	Budget      string `json:"budget"`
	VoteDetails string `json:"vote_details"`
}

type AdoptedItem struct {
	Tag     string `json:"tag"`
	Title   string `json:"title"`
	Context string `json:"context"`
	Impact  string `json:"impact"`
	Budget  string `json:"budget"`
}

// ── Interfaces (for testability) ──────────────────────────────────────────────

type dynamoQuerier interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(ctx context.Context, params *dynamodb.ScanInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
}

type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ── Deps ──────────────────────────────────────────────────────────────────────

type notifierDeps struct {
	ddb                dynamoQuerier
	httpClient         httpDoer
	geminiKey          string
	geminiModel        string
	brevoKey           string
	brevoTemplateID    int
	brevoListID        int
	senderEmail        string
	councilsTable      string
	deliberationsTable string
}

// ── Handler ───────────────────────────────────────────────────────────────────

func HandleRequest(ctx context.Context, event NotifierEvent) error {
	cfg, err := loadAWSConfig(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}

	d := &notifierDeps{
		ddb:                dynamodb.NewFromConfig(cfg),
		httpClient:         &http.Client{},
		geminiKey:          os.Getenv("GEMINI_API_KEY"),
		geminiModel:        envOrDefault("GEMINI_MODEL", "gemini-2.5-pro"),
		brevoKey:           os.Getenv("BREVO_API_KEY"),
		brevoTemplateID:    envInt("BREVO_NEWSLETTER_TEMPLATE_ID", 2),
		brevoListID:        envInt("BREVO_LIST_ID", 2),
		senderEmail:        envOrDefault("SENDER_EMAIL", "noreply@lobservatoiredebegles.fr"),
		councilsTable:      os.Getenv("COUNCILS_TABLE"),
		deliberationsTable: os.Getenv("DELIBERATIONS_TABLE"),
	}
	return d.handle(ctx, event)
}

func (d *notifierDeps) handle(ctx context.Context, event NotifierEvent) error {
	council, err := d.fetchCouncil(ctx, event.CouncilID)
	if err != nil {
		return fmt.Errorf("fetch council %s: %w", event.CouncilID, err)
	}

	delibs, err := d.fetchDeliberations(ctx, event.CouncilID)
	if err != nil {
		return fmt.Errorf("fetch deliberations for %s: %w", event.CouncilID, err)
	}

	nextMeeting := d.fetchNextMeeting(ctx)
	totalCouncils, totalDelibs := d.fetchGlobalStats(ctx)

	params, err := d.generateNewsletterParams(ctx, council, delibs, nextMeeting, totalCouncils, totalDelibs)
	if err != nil {
		return fmt.Errorf("generate newsletter params: %w", err)
	}

	if err := d.sendCampaign(ctx, params); err != nil {
		return fmt.Errorf("send brevo campaign: %w", err)
	}

	log.Printf("newsletter campaign sent for council %s (%s)", event.CouncilID, council.Date)
	return nil
}

// ── DynamoDB helpers ──────────────────────────────────────────────────────────

func (d *notifierDeps) fetchCouncil(ctx context.Context, councilID string) (*councilRec, error) {
	out, err := d.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, fmt.Errorf("council %s not found", councilID)
	}
	var c councilRec
	if err := attributevalue.UnmarshalMap(out.Item, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (d *notifierDeps) fetchDeliberations(ctx context.Context, councilID string) ([]deliberationRec, error) {
	out, err := d.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(d.deliberationsTable),
		IndexName:              aws.String("council_id-index"),
		KeyConditionExpression: aws.String("council_id = :cid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":cid": &types.AttributeValueMemberS{Value: councilID},
		},
	})
	if err != nil {
		return nil, err
	}
	var delibs []deliberationRec
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &delibs); err != nil {
		return nil, err
	}
	return delibs, nil
}

func (d *notifierDeps) fetchNextMeeting(ctx context.Context) string {
	out, err := d.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: "metadata#next_council"},
		},
	})
	if err != nil || out.Item == nil {
		return ""
	}
	var meta struct {
		DateText string `dynamodbav:"date_text"`
	}
	if err := attributevalue.UnmarshalMap(out.Item, &meta); err != nil {
		return ""
	}
	return meta.DateText
}

func (d *notifierDeps) fetchGlobalStats(ctx context.Context) (councils int, delibs int) {
	cOut, err := d.ddb.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(d.councilsTable),
		FilterExpression: aws.String("NOT (council_id = :meta)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":meta": &types.AttributeValueMemberS{Value: "metadata#next_council"},
		},
		Select: types.SelectCount,
	})
	if err == nil {
		councils = int(cOut.Count)
	}

	dOut, err := d.ddb.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(d.deliberationsTable),
		Select:    types.SelectCount,
	})
	if err == nil {
		delibs = int(dOut.Count)
	}

	return councils, delibs
}

// ── Gemini integration ────────────────────────────────────────────────────────

func (d *notifierDeps) generateNewsletterParams(ctx context.Context, council *councilRec, delibs []deliberationRec, nextMeeting string, totalCouncils, totalDelibs int) (*NewsletterParams, error) {
	// Pre-compute stats so Gemini focuses on editorial content only
	stats := computeNewsletterStats(delibs)
	prompt := buildNewsletterPrompt(council, delibs, stats, nextMeeting, totalCouncils, totalDelibs)

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      d.geminiKey,
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1"},
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}

	resp, err := client.Models.GenerateContent(
		ctx,
		d.geminiModel,
		[]*genai.Content{{
			Role:  "user",
			Parts: []*genai.Part{{Text: prompt}},
		}},
		&genai.GenerateContentConfig{
			ResponseMIMEType: "application/json",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("gemini generate: %w", err)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned empty response")
	}

	raw := resp.Candidates[0].Content.Parts[0].Text
	return parseNewsletterParams(raw)
}

// ── Pure helpers (testable) ────────────────────────────────────────────────────

type newsletterStats struct {
	totalBudget int64
	totalPour   int
	totalContre int
	totalAbst   int
	voteClimat  string
	climatColor string
	voteStats   string
	budgetFmt   string
}

func computeNewsletterStats(delibs []deliberationRec) newsletterStats {
	var s newsletterStats
	for _, d := range delibs {
		s.totalBudget += d.BudgetImpact
		if d.VotePour != nil {
			s.totalPour += *d.VotePour
		}
		if d.VoteContre != nil {
			s.totalContre += *d.VoteContre
		}
		if d.VoteAbst != nil {
			s.totalAbst += *d.VoteAbst
		}
	}

	if s.totalPour+s.totalContre > 0 && float64(s.totalContre)/float64(s.totalPour+s.totalContre) > 0.10 {
		s.voteClimat = "TENSIONS"
		s.climatColor = "#ef4444"
	} else {
		s.voteClimat = "CONSENSUS"
		s.climatColor = "#22c55e"
	}

	// French locale: space as thousands separator (e.g. "121 451")
	s.budgetFmt = formatBudgetFR(s.totalBudget)

	// e.g. "3 oppositions / 6 abst." or ""
	parts := []string{}
	if s.totalContre > 0 {
		parts = append(parts, fmt.Sprintf("%d opposition%s", s.totalContre, plural(s.totalContre)))
	}
	if s.totalAbst > 0 {
		parts = append(parts, fmt.Sprintf("%d abst.", s.totalAbst))
	}
	s.voteStats = strings.Join(parts, " / ")

	return s
}

func formatBudgetFR(amount int64) string {
	if amount == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", amount)
	// Insert spaces as thousands separators
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

func buildNewsletterPrompt(council *councilRec, delibs []deliberationRec, stats newsletterStats, nextMeeting string, totalCouncils, totalDelibs int) string {
	var sb strings.Builder

	sb.WriteString("Tu es rédacteur de newsletter municipale pour L'Observatoire de Bègles.\n")
	sb.WriteString("Génère un objet JSON avec EXACTEMENT ce schéma (ne génère aucun texte en dehors) :\n\n")
	sb.WriteString(`{
  "email_subject": "accrocheur, < 60 caractères, reflète l'enjeu principal",
  "council_title": "titre complet du conseil municipal",
  "council_date": "date au format '24 février 2026'",
  "main_issue": "synthèse éditoriale de 1-2 phrases (25-45 mots) sur l'enjeu politique/social majeur",
  "budget_total": "montant total voté (fourni ci-dessous, copie verbatim)",
  "vote_climat": "TENSIONS ou CONSENSUS (fourni ci-dessous, copie verbatim)",
  "climat_color": "code hex couleur (fourni ci-dessous, copie verbatim)",
  "vote_stats": "résumé votes (fourni ci-dessous, copie verbatim)",
  "tensions": [
    {"title": "...", "context": "1 phrase", "impact": "1 phrase pour le citoyen", "budget": "X €", "vote_details": "Y votes contre"}
  ],
  "adopted": [
    {"tag": "TAG", "title": "...", "context": "1 phrase", "impact": "1 phrase", "budget": "X €"}
  ],
  "next_meeting": "date prochaine séance (fournie ci-dessous, copie verbatim)",
  "total_councils": 0,
  "total_delibs": 0
}

`)

	fmt.Fprintf(&sb, "DONNÉES D'ENTRÉE :\n")
	fmt.Fprintf(&sb, "- Conseil : %s du %s\n", council.Title, council.Date)
	fmt.Fprintf(&sb, "- budget_total (copie verbatim) : %s\n", stats.budgetFmt)
	fmt.Fprintf(&sb, "- vote_climat (copie verbatim) : %s\n", stats.voteClimat)
	fmt.Fprintf(&sb, "- climat_color (copie verbatim) : %s\n", stats.climatColor)
	fmt.Fprintf(&sb, "- vote_stats (copie verbatim) : %s\n", stats.voteStats)
	fmt.Fprintf(&sb, "- next_meeting (copie verbatim) : %s\n", nextMeeting)
	fmt.Fprintf(&sb, "- total_councils (copie verbatim) : %d\n", totalCouncils)
	fmt.Fprintf(&sb, "- total_delibs (copie verbatim) : %d\n\n", totalDelibs)

	// Délibérations avec opposition ou désaccords → tensions[]
	sb.WriteString("DÉLIBÉRATIONS AVEC OPPOSITION (pour le champ tensions[]) :\n")
	hasTension := false
	for _, d := range delibs {
		contre := 0
		if d.VoteContre != nil {
			contre = *d.VoteContre
		}
		hasDisagreement := d.Disagreements != nil && *d.Disagreements != ""
		if contre > 0 || hasDisagreement {
			hasTension = true
			fmt.Fprintf(&sb, "- Titre: %s\n", d.Title)
			fmt.Fprintf(&sb, "  Tag: %s | Budget: %d €\n", d.TopicTag, d.BudgetImpact)
			fmt.Fprintf(&sb, "  Résumé: %s\n", d.Summary)
			if d.AnalysisData.Contexte != nil {
				fmt.Fprintf(&sb, "  Contexte: %s\n", *d.AnalysisData.Contexte)
			}
			if d.AnalysisData.Impacts != nil {
				fmt.Fprintf(&sb, "  Impacts: %s\n", *d.AnalysisData.Impacts)
			}
			pour := 0
			if d.VotePour != nil {
				pour = *d.VotePour
			}
			abst := 0
			if d.VoteAbst != nil {
				abst = *d.VoteAbst
			}
			fmt.Fprintf(&sb, "  Vote: %d pour / %d contre / %d abst.\n", pour, contre, abst)
			if hasDisagreement {
				fmt.Fprintf(&sb, "  Désaccords: %s\n", *d.Disagreements)
			}
			sb.WriteString("\n")
		}
	}
	if !hasTension {
		sb.WriteString("(aucune délibération avec opposition)\n\n")
	}

	// Délibérations adoptées significatives → adopted[]
	sb.WriteString("DÉLIBÉRATIONS ADOPTÉES (pour le champ adopted[], max 5 les plus significatives) :\n")
	adoptedCount := 0
	for _, d := range delibs {
		contre := 0
		if d.VoteContre != nil {
			contre = *d.VoteContre
		}
		if contre == 0 && d.BudgetImpact > 0 && adoptedCount < 8 {
			fmt.Fprintf(&sb, "- Titre: %s\n", d.Title)
			fmt.Fprintf(&sb, "  Tag: %s | Budget: %d €\n", d.TopicTag, d.BudgetImpact)
			fmt.Fprintf(&sb, "  Résumé: %s\n", d.Summary)
			if d.AnalysisData.Impacts != nil {
				fmt.Fprintf(&sb, "  Impacts: %s\n", *d.AnalysisData.Impacts)
			}
			sb.WriteString("\n")
			adoptedCount++
		}
	}
	if adoptedCount == 0 {
		sb.WriteString("(aucune délibération adoptée avec budget)\n\n")
	}

	sb.WriteString("RÈGLES ÉDITORIALES :\n")
	sb.WriteString("- Langage citoyen vulgarisé, concret, accessible\n")
	sb.WriteString("- tensions[]: uniquement les délibérations listées ci-dessus avec opposition\n")
	sb.WriteString("- adopted[]: sélectionne les 5 plus impactantes (budget ou impact citoyen fort)\n")
	sb.WriteString("- Pour budget dans tensions[]/adopted[], formate en '45 451 €' (espaces comme séparateurs de milliers)\n")
	sb.WriteString("- Ne génère aucun texte en dehors de l'objet JSON\n")

	return sb.String()
}

func parseNewsletterParams(raw string) (*NewsletterParams, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	// Normalize any floats in numeric integer fields
	raw = budgetFloatRe.ReplaceAllString(raw, "${1}${2}")

	var p NewsletterParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("unmarshal newsletter params: %w (raw: %.200s)", err, raw)
	}
	return &p, nil
}

// ── Brevo campaign ────────────────────────────────────────────────────────────

func (d *notifierDeps) sendCampaign(ctx context.Context, params *NewsletterParams) error {
	campaignID, err := d.createCampaign(ctx, params)
	if err != nil {
		return fmt.Errorf("create campaign: %w", err)
	}

	if err := d.triggerSend(ctx, campaignID); err != nil {
		return fmt.Errorf("send campaign %d: %w", campaignID, err)
	}

	log.Printf("Brevo campaign %d dispatched", campaignID)
	return nil
}

func (d *notifierDeps) createCampaign(ctx context.Context, params *NewsletterParams) (int, error) {
	// Convert params struct → map[string]interface{} for the Brevo params field
	paramsJSON, _ := json.Marshal(params)
	var paramsMap map[string]interface{}
	json.Unmarshal(paramsJSON, &paramsMap)

	payload, err := json.Marshal(map[string]interface{}{
		"name":       fmt.Sprintf("Newsletter - %s", params.CouncilDate),
		"subject":    params.EmailSubject,
		"templateId": d.brevoTemplateID,
		"sender": map[string]string{
			"name":  "L'Observatoire de Bègles",
			"email": d.senderEmail,
		},
		"recipients": map[string]interface{}{
			"listIds": []int{d.brevoListID},
		},
		"params": paramsMap,
	})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, brevoBaseURL+"/emailCampaigns", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("api-key", d.brevoKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("create campaign request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("brevo create campaign status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.ID == 0 {
		return 0, fmt.Errorf("unexpected brevo response (no campaign id): %s", body)
	}

	log.Printf("Brevo campaign created: id=%d subject=%q", result.ID, params.EmailSubject)
	return result.ID, nil
}

func (d *notifierDeps) triggerSend(ctx context.Context, campaignID int) error {
	url := fmt.Sprintf("%s/emailCampaigns/%d/sendNow", brevoBaseURL, campaignID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("api-key", d.brevoKey)
	req.Header.Set("accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendNow request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// 204 No Content = success
	if resp.StatusCode >= 300 {
		return fmt.Errorf("brevo sendNow status %d: %s", resp.StatusCode, body)
	}
	return nil
}
