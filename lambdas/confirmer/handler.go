package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type dynamoDBAPI interface {
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type confirmerDeps struct {
	ddb            dynamoDBAPI
	httpClient     *http.Client
	tableName      string
	brevoURL       string
	apiKey         string
	brevoListID    int
	redirectionURL string
}

func (d *confirmerDeps) handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	redirectResponse := events.APIGatewayProxyResponse{
		StatusCode: 302,
		Headers:    map[string]string{"Location": d.redirectionURL},
	}

	token := req.QueryStringParameters["token"]
	if token == "" {
		log.Printf("error: missing token")
		return redirectResponse, nil
	}

	emailBytes, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		log.Printf("error: invalid token %s: %v", token, err)
		return redirectResponse, nil
	}
	email := strings.ToLower(strings.TrimSpace(string(emailBytes)))

	_, err = d.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"email": &types.AttributeValueMemberS{Value: email},
		},
		UpdateExpression: aws.String("SET #s = :s"),
		ExpressionAttributeNames: map[string]string{
			"#s": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s": &types.AttributeValueMemberS{Value: "CONFIRMED"},
		},
	})
	if err != nil {
		log.Printf("error: failed to update dynamodb status to CONFIRMED for %s: %v", email, err)
	} else {
		log.Printf("success: updated status for %s to CONFIRMED", email)
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"email":         email,
		"listIds":       []int{d.brevoListID},
		"updateEnabled": true,
	})

	brevoReq, err := http.NewRequest(http.MethodPost, d.brevoURL, bytes.NewReader(payload))
	if err != nil {
		log.Printf("error: failed to build brevo request: %v", err)
		return redirectResponse, nil
	}
	brevoReq.Header.Set("api-key", d.apiKey)
	brevoReq.Header.Set("Content-Type", "application/json")
	brevoReq.Header.Set("accept", "application/json")

	resp, err := d.httpClient.Do(brevoReq)
	if err != nil {
		log.Printf("error: brevo request failed: %v", err)
		return redirectResponse, nil
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Printf("error: brevo add contact failed for %s. Status: %d, Body: %s", email, resp.StatusCode, string(bodyBytes))
	} else {
		log.Printf("success: brevo contact added/updated for %s. Response: %s", email, string(bodyBytes))
	}

	return redirectResponse, nil
}
