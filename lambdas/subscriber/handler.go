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
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
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

type dynamoDBAPI interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

type subscriberDeps struct {
	ddb        dynamoDBAPI
	httpClient *http.Client
	tableName  string
	brevoURL   string
	apiKey     string
	templateID int
	apiURL     string
}

func (d *subscriberDeps) handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	switch req.HTTPMethod {
	case http.MethodOptions:
		return events.APIGatewayProxyResponse{StatusCode: 200, Headers: corsHeaders()}, nil
	case http.MethodPost:
		return d.handleSubscribe(ctx, req)
	default:
		return apiResponse(405, map[string]string{"error": "method not allowed"}), nil
	}
}

func (d *subscriberDeps) handleSubscribe(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return apiResponse(400, map[string]string{"error": "invalid email"}), nil
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !isValidEmail(email) {
		return apiResponse(400, map[string]string{"error": "invalid email"}), nil
	}

	existing, _ := d.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.tableName),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: email}},
	})
	if existing != nil && existing.Item != nil {
		if status, ok := existing.Item["status"].(*types.AttributeValueMemberS); ok && status.Value == "CONFIRMED" {
			return apiResponse(200, map[string]string{"message": "already subscribed"}), nil
		}
	}

	item, _ := attributevalue.MarshalMap(map[string]interface{}{
		"email":      email,
		"status":     "BREVO_PENDING",
		"created_at": time.Now().UTC().Format(time.RFC3339),
	})
	if _, err := d.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.tableName),
		Item:      item,
	}); err != nil {
		log.Printf("warn: failed to save subscriber info for %s: %v", email, err)
	}

	if err := d.sendConfirmationEmail(email); err != nil {
		log.Printf("error: failed to send confirmation email to %s: %v", email, err)
		return apiResponse(500, map[string]string{"error": "failed to send confirmation email, please try again"}), nil
	}

	return apiResponse(200, map[string]string{"message": "confirmation email sent"}), nil
}

func (d *subscriberDeps) sendConfirmationEmail(email string) error {
	token := base64.URLEncoding.EncodeToString([]byte(email))
	confirmLink := fmt.Sprintf("%sconfirm?token=%s", d.apiURL, token)

	payload, _ := json.Marshal(map[string]interface{}{
		"to": []map[string]string{
			{"email": email},
		},
		"templateId": d.templateID,
		"params": map[string]interface{}{
			"confirm_link": confirmLink,
		},
		"tracking": map[string]bool{
			"clicks": false,
		},
	})

	brevoReq, err := http.NewRequest(http.MethodPost, d.brevoURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to build confirmation request: %w", err)
	}
	brevoReq.Header.Set("api-key", d.apiKey)
	brevoReq.Header.Set("Content-Type", "application/json")
	brevoReq.Header.Set("accept", "application/json")

	resp, err := d.httpClient.Do(brevoReq)
	if err != nil {
		return fmt.Errorf("confirmation request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("brevo returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	log.Printf("success: brevo confirmation email sent for %s. Response: %s", email, string(bodyBytes))
	return nil
}
