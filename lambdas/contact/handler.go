package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

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

type contactRequest struct {
	Name        string `json:"name"`
	EmailSender string `json:"email_sender"`
	Message     string `json:"message"`
}

func handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	if req.HTTPMethod == http.MethodOptions {
		return events.APIGatewayProxyResponse{StatusCode: 200, Headers: corsHeaders()}, nil
	}

	var cr contactRequest
	if err := json.Unmarshal([]byte(req.Body), &cr); err != nil {
		return apiResponse(400, map[string]string{"error": "invalid request body"}), nil
	}

	cr.Name = strings.TrimSpace(cr.Name)
	cr.EmailSender = strings.TrimSpace(cr.EmailSender)
	cr.Message = strings.TrimSpace(cr.Message)

	if cr.Name == "" || cr.EmailSender == "" || cr.Message == "" {
		return apiResponse(400, map[string]string{"error": "name, email_sender and message are required"}), nil
	}

	domainSender := os.Getenv("SENDER_EMAIL")
	adminEmail := os.Getenv("ADMIN_EMAIL")

	subject := fmt.Sprintf("[Observatoire Bègles] Message de %s", cr.Name)
	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html lang="fr">
<body style="font-family:sans-serif;max-width:600px;margin:40px auto;color:#333">
  <h2>Nouveau message via le formulaire de contact</h2>
  <table style="width:100%%;border-collapse:collapse">
    <tr><td style="padding:8px;font-weight:bold;width:120px">Nom</td><td style="padding:8px">%s</td></tr>
    <tr style="background:#f9f9f9"><td style="padding:8px;font-weight:bold">Email</td><td style="padding:8px"><a href="mailto:%s">%s</a></td></tr>
    <tr><td style="padding:8px;font-weight:bold;vertical-align:top">Message</td><td style="padding:8px;white-space:pre-wrap">%s</td></tr>
  </table>
  <hr style="margin:24px 0;border:none;border-top:1px solid #eee"/>
  <p style="font-size:12px;color:#888">Envoyé depuis l'Observatoire de Bègles — répondez directement à cet email pour contacter %s.</p>
</body>
</html>`, cr.Name, cr.EmailSender, cr.EmailSender, cr.Message, cr.Name)

	textBody := fmt.Sprintf("Nom : %s\nEmail : %s\n\n%s", cr.Name, cr.EmailSender, cr.Message)

	payload, _ := json.Marshal(map[string]interface{}{
		"sender":      map[string]string{"email": domainSender},
		"to":          []map[string]string{{"email": adminEmail}},
		"replyTo":     map[string]string{"email": cr.EmailSender},
		"subject":     subject,
		"htmlContent": htmlBody,
		"textContent": textBody,
	})

	brevoReq, err := http.NewRequest(http.MethodPost, "https://api.brevo.com/v3/smtp/email", bytes.NewReader(payload))
	if err != nil {
		log.Printf("failed to build brevo request: %v", err)
		return apiResponse(500, map[string]string{"error": "failed to build message"}), nil
	}

	brevoReq.Header.Set("api-key", os.Getenv("MAIL_API_KEY"))
	brevoReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(brevoReq)
	if err != nil {
		log.Printf("brevo send error: %v", err)
		return apiResponse(500, map[string]string{"error": "failed to send message"}), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("brevo returned error status: %d", resp.StatusCode)
		return apiResponse(500, map[string]string{"error": "delivery failed"}), nil
	}

	return apiResponse(200, map[string]string{"message": "message sent"}), nil
}
