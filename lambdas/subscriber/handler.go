package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func isValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}

func corsHeaders() map[string]string {
	return map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "POST,OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type",
		"Content-Type":                 "application/json",
	}
}

func apiResponse(status int, body map[string]string) events.APIGatewayProxyResponse {
	b, _ := json.Marshal(body)
	return events.APIGatewayProxyResponse{
		StatusCode: status,
		Headers:    corsHeaders(),
		Body:       string(b),
	}
}

func handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	switch req.HTTPMethod {
	case http.MethodOptions:
		return events.APIGatewayProxyResponse{StatusCode: 200, Headers: corsHeaders()}, nil
	case http.MethodPost:
		return handleSubscribe(ctx, req)
	default:
		return apiResponse(405, map[string]string{"error": "method not allowed"}), nil
	}
}

func handleSubscribe(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil || !isValidEmail(body.Email) {
		return apiResponse(400, map[string]string{"error": "invalid email"}), nil
	}

	email := strings.ToLower(strings.TrimSpace(body.Email))

	cfg, _ := config.LoadDefaultConfig(ctx)
	ddb := dynamodb.NewFromConfig(cfg)
	tableName := os.Getenv("TABLE_NAME")

	// Block re-registration if already confirmed.
	existing, _ := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(tableName),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: email}},
	})
	if existing.Item != nil {
		if status, ok := existing.Item["status"].(*types.AttributeValueMemberS); ok && status.Value == "CONFIRMED" {
			return apiResponse(200, map[string]string{"message": "already subscribed"}), nil
		}
	}

	token := uuid.New().String()
	item, _ := attributevalue.MarshalMap(map[string]interface{}{
		"email":      email,
		"status":     "PENDING",
		"token":      token,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	})
	if _, err := ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item:      item,
	}); err != nil {
		log.Printf("error saving subscriber %s: %v", email, err)
		return apiResponse(500, map[string]string{"error": "internal error"}), nil
	}

	confirmURL := fmt.Sprintf("%s?token=%s&email=%s",
		os.Getenv("CONFIRM_BASE_URL"),
		url.QueryEscape(token),
		url.QueryEscape(email),
	)
	sendConfirmationEmail(email, confirmURL)

	return apiResponse(200, map[string]string{"message": "confirmation email sent"}), nil
}

func sendConfirmationEmail(email, confirmURL string) {
	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html lang="fr">
<body style="font-family:sans-serif;max-width:600px;margin:40px auto;color:#333">
  <h2>Confirmez votre inscription</h2>
  <p>Merci de votre intérêt pour <strong>L'Observatoire de Bègles</strong>.</p>
  <p>Cliquez sur le bouton ci-dessous pour confirmer votre abonnement à nos alertes citoyennes :</p>
  <p style="text-align:center;margin:32px 0">
    <a href="%s" style="background:#1d4ed8;color:#fff;padding:14px 28px;border-radius:6px;text-decoration:none;font-weight:bold">
      Confirmer mon abonnement
    </a>
  </p>
  <p style="font-size:12px;color:#888">Si vous n'avez pas demandé cet abonnement, ignorez simplement ce message.</p>
</body>
</html>`, confirmURL)

	payload, _ := json.Marshal(map[string]interface{}{
		"sender":      map[string]string{"email": os.Getenv("SENDER_EMAIL")},
		"to":          []map[string]string{{"email": email}},
		"subject":     "Confirmez votre abonnement — L'Observatoire de Bègles",
		"htmlContent": htmlBody,
		"textContent": fmt.Sprintf("Confirmez votre abonnement : %s", confirmURL),
	})

	req, err := http.NewRequest(http.MethodPost, "https://api.brevo.com/v3/smtp/email", bytes.NewReader(payload))
	if err != nil {
		log.Printf("warn: failed to build email request for %s: %v", email, err)
		return
	}
	req.Header.Set("api-key", os.Getenv("MAIL_API_KEY"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("warn: confirmation email failed for %s: %v", email, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("warn: brevo returned %d for %s", resp.StatusCode, email)
	}
}
