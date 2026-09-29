package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/watchdog/shared"
)

const (
	testNewsletterListID = 3
	brevoCampaignsURL    = "https://api.brevo.com/v3/emailCampaigns"
	maxTemplateBytes     = 1_000_000
)

type councilReader interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}

type newsletterCouncil struct {
	ID        string
	Title     string
	Date      string
	Category  string
	SourceURL string
	Params    shared.NewsletterParams
}

func runNewsletterTest(args []string) error {
	fs := flag.NewFlagSet("newsletter-test", flag.ContinueOnError)
	councilID := fs.String("council-id", "", "Identifiant du conseil municipal approuvé")
	fixturePath := fs.String("fixture", "", "Instantané JSON d'un conseil APPROVED")
	templatePath := fs.String("template-file", "", "Fichier HTML local à tester à la place du template Brevo actif")
	send := fs.Bool("send", false, "Créer et envoyer une campagne Brevo à la liste test #3")
	confirmListID := fs.Int("confirm-list-id", 0, "Confirmation explicite de la liste test #3")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (strings.TrimSpace(*councilID) == "") == (strings.TrimSpace(*fixturePath) == "") || fs.NArg() != 0 {
		return fmt.Errorf("usage: go run . newsletter-test (--council-id <id> | --fixture <fichier.json>) [--template-file <fichier.html>] [--send --confirm-list-id 3]")
	}
	if *send && *confirmListID != testNewsletterListID {
		return fmt.Errorf("envoi refusé : ajouter --confirm-list-id %d", testNewsletterListID)
	}
	if !*send && *confirmListID != 0 {
		return fmt.Errorf("--confirm-list-id n'est utile qu'avec --send")
	}
	if os.Getenv("BREVO_LIST_ID") == strconv.Itoa(testNewsletterListID) {
		return fmt.Errorf("envoi refusé : BREVO_LIST_ID indique que la liste de production est aussi #%d", testNewsletterListID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var council *newsletterCouncil
	if *fixturePath != "" {
		var err error
		council, err = loadNewsletterFixture(*fixturePath)
		if err != nil {
			return err
		}
	} else {
		awsCfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return fmt.Errorf("configuration AWS : %w", err)
		}
		table := envDefault("COUNCILS_TABLE", "watchdog-councils")
		council, err = loadApprovedNewsletter(ctx, dynamodb.NewFromConfig(awsCfg), table, *councilID)
		if err != nil {
			return err
		}
	}
	paramsJSON, err := json.MarshalIndent(council.Params, "", "  ")
	if err != nil {
		return fmt.Errorf("sérialisation de la newsletter : %w", err)
	}
	modelID := 0
	var htmlContent string
	modelLabel := ""
	if *templatePath != "" {
		data, err := os.ReadFile(*templatePath)
		if err != nil {
			return fmt.Errorf("lecture du template HTML : %w", err)
		}
		if len(data) < 11 || len(data) >= maxTemplateBytes {
			return fmt.Errorf("le template HTML doit contenir entre 11 et %d octets", maxTemplateBytes-1)
		}
		htmlContent = string(data)
		modelLabel = filepath.Base(*templatePath)
	} else {
		var err error
		modelID, err = strconv.Atoi(envDefault("BREVO_NEWSLETTER_TEMPLATE_ID", "7"))
		if err != nil || modelID < 1 {
			return fmt.Errorf("BREVO_NEWSLETTER_TEMPLATE_ID doit être un entier positif")
		}
		modelLabel = fmt.Sprintf("#%d", modelID)
	}
	fmt.Printf("Conseil : %s (%s)\nSource : %s\nStatut à la capture : APPROVED\nDestinataires : liste test Brevo #%d\nTemplate : %s\nSujet : %s\n\n%s\n", council.Title, council.Date, council.SourceURL, testNewsletterListID, modelLabel, council.Params.EmailSubject, paramsJSON)
	if !*send {
		fmt.Printf("\nAucun email envoyé. Ajouter --send --confirm-list-id %d pour recevoir le rendu réel.\n", testNewsletterListID)
		return nil
	}

	apiKey := os.Getenv("BREVO_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("BREVO_API_KEY non défini")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	campaignID, err := sendTestNewsletter(ctx, client, apiKey, council, modelID, htmlContent, modelLabel, envDefault("SENDER_EMAIL", "newsletter@lobservatoiredebegles.fr"))
	if err != nil {
		return err
	}
	fmt.Printf("\nCampagne test #%d envoyée à la liste Brevo #%d. Aucun statut d'envoi public modifié.\n", campaignID, testNewsletterListID)
	return nil
}

func loadNewsletterFixture(path string) (*newsletterCouncil, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lecture de la fixture : %w", err)
	}
	var fixture struct {
		ID        string                  `json:"council_id"`
		Title     string                  `json:"title"`
		Date      string                  `json:"date"`
		Category  string                  `json:"category"`
		SourceURL string                  `json:"source_url"`
		QcStatus  string                  `json:"qc_status"`
		Params    shared.NewsletterParams `json:"newsletter_params"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		return nil, fmt.Errorf("fixture JSON invalide : %w", err)
	}
	if fixture.QcStatus != "APPROVED" || fixture.Category != "Conseil municipal" ||
		strings.TrimSpace(fixture.ID) == "" || strings.TrimSpace(fixture.Params.EmailSubject) == "" {
		return nil, fmt.Errorf("fixture invalide : conseil municipal APPROVED et sujet obligatoires")
	}
	return &newsletterCouncil{ID: fixture.ID, Title: fixture.Title, Date: fixture.Date,
		Category: fixture.Category, SourceURL: fixture.SourceURL, Params: fixture.Params}, nil
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func loadApprovedNewsletter(ctx context.Context, db councilReader, table, councilID string) (*newsletterCouncil, error) {
	if strings.HasPrefix(councilID, "metadata#") {
		return nil, fmt.Errorf("identifiant de conseil invalide")
	}
	out, err := db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(table),
		Key:            map[string]types.AttributeValue{"council_id": &types.AttributeValueMemberS{Value: councilID}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("lecture du conseil : %w", err)
	}
	if out.Item == nil {
		return nil, fmt.Errorf("conseil %q introuvable", councilID)
	}
	stringAttr := func(key string) string {
		if value, ok := out.Item[key].(*types.AttributeValueMemberS); ok {
			return value.Value
		}
		return ""
	}
	if stringAttr("qc_status") != "APPROVED" {
		return nil, fmt.Errorf("conseil %q non APPROVED (statut %q) : envoi refusé", councilID, stringAttr("qc_status"))
	}
	if category := stringAttr("category"); category != "" && category != "Conseil municipal" {
		return nil, fmt.Errorf("conseil %q de catégorie %q : pas de newsletter prévue", councilID, category)
	}
	var params shared.NewsletterParams
	if err := json.Unmarshal([]byte(stringAttr("newsletter_params_json")), &params); err != nil {
		return nil, fmt.Errorf("newsletter_params_json absent ou invalide : %w", err)
	}
	if strings.TrimSpace(params.EmailSubject) == "" {
		return nil, fmt.Errorf("sujet de la newsletter absent : envoi refusé")
	}
	return &newsletterCouncil{
		ID: councilID, Title: stringAttr("title"), Date: stringAttr("date"), Category: stringAttr("category"),
		SourceURL: stringAttr("source_url"), Params: params,
	}, nil
}

type httpSender interface {
	Do(*http.Request) (*http.Response, error)
}

func sendTestNewsletter(ctx context.Context, client httpSender, apiKey string, council *newsletterCouncil, templateID int, htmlContent, label, senderEmail string) (int, error) {
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, fmt.Errorf("identifiant de campagne test : %w", err)
	}
	name := fmt.Sprintf("TEST-list-%d-%s-%s-%s", testNewsletterListID, label, time.Now().UTC().Format("20060102T150405"), hex.EncodeToString(nonce[:]))
	payload := map[string]any{
		"name":    name,
		"subject": council.Params.EmailSubject,
		"sender": map[string]string{
			"name": "L'Observatoire de Bègles", "email": senderEmail,
		},
		"recipients": map[string]any{"listIds": []int{testNewsletterListID}},
		"params":     council.Params,
	}
	if htmlContent != "" {
		payload["htmlContent"] = htmlContent
	} else {
		payload["templateId"] = templateID
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("préparation de la campagne : %w", err)
	}
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, brevoCampaignsURL, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	createReq.Header.Set("api-key", apiKey)
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := client.Do(createReq)
	if err != nil {
		return 0, fmt.Errorf("création Brevo incertaine ; vérifier les brouillons nommés %q avant de réessayer : %w", name, err)
	}
	defer createResp.Body.Close()
	createBody, _ := io.ReadAll(io.LimitReader(createResp.Body, 4096))
	if createResp.StatusCode >= 300 {
		return 0, fmt.Errorf("création Brevo refusée (%d) : %s", createResp.StatusCode, createBody)
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(createBody, &created); err != nil || created.ID < 1 {
		return 0, fmt.Errorf("réponse Brevo sans identifiant de campagne : %s", createBody)
	}
	// This fresh draft has recipients fixed to list #3. Never reuse an existing
	// public campaign or update the newsletter's production send ledger.
	sendReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/%d/sendNow", brevoCampaignsURL, created.ID), nil)
	if err != nil {
		return created.ID, err
	}
	sendReq.Header.Set("api-key", apiKey)
	sendResp, err := client.Do(sendReq)
	if err != nil {
		return created.ID, fmt.Errorf("envoi Brevo incertain pour la campagne test #%d ; vérifier son statut avant de réessayer : %w", created.ID, err)
	}
	defer sendResp.Body.Close()
	sendBody, _ := io.ReadAll(io.LimitReader(sendResp.Body, 4096))
	if sendResp.StatusCode >= 300 {
		return created.ID, fmt.Errorf("envoi Brevo refusé pour la campagne test #%d (%d) : %s", created.ID, sendResp.StatusCode, sendBody)
	}
	return created.ID, nil
}
