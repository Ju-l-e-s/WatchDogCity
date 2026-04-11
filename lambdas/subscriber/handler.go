package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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

	// Check if already in DynamoDB (for info)
	existing, _ := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(tableName),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: email}},
	})
	if existing.Item != nil {
		if status, ok := existing.Item["status"].(*types.AttributeValueMemberS); ok && status.Value == "CONFIRMED" {
			return apiResponse(200, map[string]string{"message": "already subscribed"}), nil
		}
	}

	// Save/Update in DynamoDB (informational status)
	item, _ := attributevalue.MarshalMap(map[string]interface{}{
		"email":      email,
		"status":     "BREVO_PENDING",
		"created_at": time.Now().UTC().Format(time.RFC3339),
	})
	if _, err := ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item:      item,
	}); err != nil {
		log.Printf("warn: failed to save subscriber info for %s: %v", email, err)
	}

	sendConfirmationEmail(email)

	return apiResponse(200, map[string]string{"message": "confirmation email sent"}), nil
}

func sendConfirmationEmail(email string) {
	templateID, _ := strconv.Atoi(os.Getenv("BREVO_TEMPLATE_ID"))
	apiURL := os.Getenv("API_URL")

	token := base64.URLEncoding.EncodeToString([]byte(email))
	confirmLink := fmt.Sprintf("%sconfirm?token=%s", apiURL, token)

	payload, _ := json.Marshal(map[string]interface{}{
		"to": []map[string]string{
			{"email": email},
		},
		"templateId": templateID,
		"params": map[string]interface{}{
			"confirm_link": confirmLink,
		},
		"tracking": map[string]bool{
			"clicks": false,
		},
	})

	brevoReq, err := http.NewRequest(http.MethodPost, "https://api.brevo.com/v3/smtp/email", bytes.NewReader(payload))
	if err != nil {
		log.Printf("warn: failed to build confirmation request for %s: %v", email, err)
		return
	}
	brevoReq.Header.Set("api-key", os.Getenv("MAIL_API_KEY"))
	brevoReq.Header.Set("Content-Type", "application/json")
	brevoReq.Header.Set("accept", "application/json")

	resp, err := http.DefaultClient.Do(brevoReq)
	if err != nil {
		log.Printf("error: confirmation request failed for %s: %v", email, err)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Printf("error: brevo confirmation failed for %s. Status: %d, Body: %s", email, resp.StatusCode, string(bodyBytes))
	} else {
		log.Printf("success: brevo confirmation email sent for %s. Response: %s", email, string(bodyBytes))
	}
}
