package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func corsHeaders() map[string]string {
	return map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "GET,OPTIONS",
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
	if req.HTTPMethod == http.MethodOptions {
		return events.APIGatewayProxyResponse{StatusCode: 200, Headers: corsHeaders()}, nil
	}

	email := req.QueryStringParameters["email"]
	token := req.QueryStringParameters["token"]
	if email == "" || token == "" {
		return apiResponse(400, map[string]string{"error": "missing email or token"}), nil
	}

	cfg, _ := config.LoadDefaultConfig(ctx)
	ddb := dynamodb.NewFromConfig(cfg)
	tableName := os.Getenv("TABLE_NAME")

	// Fetch subscriber by email (partition key — no GSI needed).
	out, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(tableName),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: email}},
	})
	if err != nil {
		log.Printf("dynamodb get error: %v", err)
		return apiResponse(500, map[string]string{"error": "internal error"}), nil
	}
	if out.Item == nil {
		return apiResponse(404, map[string]string{"error": "subscriber not found"}), nil
	}

	// Verify token.
	storedToken, ok := out.Item["token"].(*types.AttributeValueMemberS)
	if !ok || storedToken.Value != token {
		return apiResponse(400, map[string]string{"error": "invalid token"}), nil
	}

	// Already confirmed — idempotent success.
	if status, ok := out.Item["status"].(*types.AttributeValueMemberS); ok && status.Value == "CONFIRMED" {
		return redirect(os.Getenv("SITE_URL") + "?subscribed=true"), nil
	}

	// Confirm the subscription.
	_, err = ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: email}},
		UpdateExpression:         aws.String("SET #s = :confirmed"),
		ExpressionAttributeNames: map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":confirmed": &types.AttributeValueMemberS{Value: "CONFIRMED"},
		},
	})
	if err != nil {
		log.Printf("dynamodb update error: %v", err)
		return apiResponse(500, map[string]string{"error": "internal error"}), nil
	}

	return redirect(os.Getenv("SITE_URL") + "?subscribed=true"), nil
}

func redirect(location string) events.APIGatewayProxyResponse {
	return events.APIGatewayProxyResponse{
		StatusCode: http.StatusFound,
		Headers: map[string]string{
			"Location":                     location,
			"Access-Control-Allow-Origin":  "*",
		},
	}
}
