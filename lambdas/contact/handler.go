package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
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

type turnstileResp struct {
	Success bool `json:"success"`
}

func verifyTurnstile(ctx context.Context, secret, token string) bool {
	resp, err := http.PostForm(
		"https://challenges.cloudflare.com/turnstile/v0/siteverify",
		url.Values{"secret": {secret}, "response": {token}},
	)
	if err != nil {
		log.Printf("turnstile request error: %v", err)
		return false
	}
	defer resp.Body.Close()
	var tr turnstileResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return false
	}
	return tr.Success
}

type contactRequest struct {
	Name         string `json:"name"`
	EmailSender  string `json:"email_sender"`
	Message      string `json:"message"`
	CaptchaToken string `json:"captcha_token"`
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

	if !verifyTurnstile(ctx, os.Getenv("TURNSTILE_SECRET"), cr.CaptchaToken) {
		return apiResponse(403, map[string]string{"error": "captcha verification failed"}), nil
	}

	cfg, _ := config.LoadDefaultConfig(ctx)
	ses := sesv2.NewFromConfig(cfg)

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

	_, err := ses.SendEmail(ctx, &sesv2.SendEmailInput{
		// Source must be an SES-verified identity (SPF/DKIM pass).
		FromEmailAddress: aws.String(domainSender),
		// ReplyTo is the visitor's address so you can reply directly.
		ReplyToAddresses: []string{cr.EmailSender},
		Destination:      &sestypes.Destination{ToAddresses: []string{adminEmail}},
		Content: &sestypes.EmailContent{
			Simple: &sestypes.Message{
				Subject: &sestypes.Content{Data: aws.String(subject)},
				Body: &sestypes.Body{
					Html: &sestypes.Content{Data: aws.String(htmlBody)},
					Text: &sestypes.Content{Data: aws.String(textBody)},
				},
			},
		},
	})
	if err != nil {
		log.Printf("ses send error: %v", err)
		return apiResponse(500, map[string]string{"error": "failed to send message"}), nil
	}

	return apiResponse(200, map[string]string{"message": "message sent"}), nil
}
