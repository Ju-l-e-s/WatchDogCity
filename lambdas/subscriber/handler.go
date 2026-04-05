package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/google/uuid"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func isValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}

func corsHeaders() map[string]string {
	return map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "POST,GET,OPTIONS",
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
	case http.MethodGet:
		if strings.Contains(req.Path, "unsubscribe") {
			return handleUnsubscribe(ctx, req)
		}
		return handleConfirm(ctx, req)
	default:
		return apiResponse(405, map[string]string{"error": "method not allowed"}), nil
	}
}

func handleUnsubscribe(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	token := req.QueryStringParameters["token"]
	if token == "" {
		return apiResponse(400, map[string]string{"error": "missing token"}), nil
	}

	cfg, _ := config.LoadDefaultConfig(ctx)
	ddb := dynamodb.NewFromConfig(cfg)

	// Find subscriber by token using GSI
	out, err := ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		IndexName:              aws.String("token-index"),
		KeyConditionExpression: aws.String("token = :t"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":t": &types.AttributeValueMemberS{Value: token},
		},
	})
	if err != nil || len(out.Items) == 0 {
		return apiResponse(404, map[string]string{"error": "invalid token"}), nil
	}

	emailAttr := out.Items[0]["email"].(*types.AttributeValueMemberS)
	_, err = ddb.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: emailAttr.Value}},
	})
	if err != nil {
		log.Printf("error deleting subscriber: %v", err)
		return apiResponse(500, map[string]string{"error": "internal error"}), nil
	}

	// Redirect to site
	return events.APIGatewayProxyResponse{
		StatusCode: 302,
		Headers:    map[string]string{"Location": os.Getenv("SITE_URL") + "?unsubscribed=true"},
	}, nil
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

	// Check existing subscriber
	existing, _ := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: email}},
	})
	if existing.Item != nil {
		return apiResponse(200, map[string]string{"message": "already registered"}), nil
	}

	token := uuid.New().String()
	item, _ := attributevalue.MarshalMap(map[string]interface{}{
		"email":      email,
		"status":     "pending",
		"token":      token,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	})
	ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		Item:      item,
	})

	apiID := os.Getenv("API_ID")
	region := os.Getenv("AWS_REGION")
	stage := os.Getenv("API_STAGE")
	apiBaseURL := fmt.Sprintf("https://%s.execute-api.%s.amazonaws.com/%s/", apiID, region, stage)
	confirmURL := fmt.Sprintf("%sconfirm?token=%s", apiBaseURL, token)
	sendConfirmationEmail(ctx, cfg, email, confirmURL)

	return apiResponse(200, map[string]string{"message": "confirmation email sent"}), nil
}

func handleConfirm(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	token := req.QueryStringParameters["token"]
	if token == "" {
		return apiResponse(400, map[string]string{"error": "missing token"}), nil
	}

	cfg, _ := config.LoadDefaultConfig(ctx)
	ddb := dynamodb.NewFromConfig(cfg)

	// Find subscriber by token using GSI
	out, err := ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		IndexName:              aws.String("token-index"),
		KeyConditionExpression: aws.String("token = :t"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":t": &types.AttributeValueMemberS{Value: token},
		},
	})
	if err != nil || len(out.Items) == 0 {
		return apiResponse(404, map[string]string{"error": "invalid token"}), nil
	}

	emailAttr := out.Items[0]["email"].(*types.AttributeValueMemberS)
	ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: emailAttr.Value}},
		UpdateExpression: aws.String("SET #s = :confirmed"),
		ExpressionAttributeNames:  map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":confirmed": &types.AttributeValueMemberS{Value: "confirmed"}},
	})

	// Redirect to site
	return events.APIGatewayProxyResponse{
		StatusCode: 302,
		Headers:    map[string]string{"Location": os.Getenv("SITE_URL") + "?subscribed=true"},
	}, nil
}

func sendConfirmationEmail(ctx context.Context, cfg aws.Config, email, confirmURL string) {
	ses := sesv2.NewFromConfig(cfg)
	body := fmt.Sprintf(`<p>Cliquez sur ce lien pour confirmer votre abonnement au Watchdog Bègles :</p>
<p><a href="%s">Confirmer mon abonnement</a></p>
<p>Si vous n'avez pas demandé cet abonnement, ignorez ce message.</p>`, confirmURL)

	_, err := ses.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(os.Getenv("FROM_EMAIL")),
		Destination:      &sestypes.Destination{ToAddresses: []string{email}},
		Content: &sestypes.EmailContent{
			Simple: &sestypes.Message{
				Subject: &sestypes.Content{Data: aws.String("Confirmez votre abonnement — Watchdog Bègles")},
				Body:    &sestypes.Body{Html: &sestypes.Content{Data: aws.String(body)}},
			},
		},
	})
	if err != nil {
		log.Printf("warn: confirmation email failed for %s: %v", email, err)
	}
}
