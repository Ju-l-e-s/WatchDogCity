package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type WorkerHandler struct {
	ddb    *dynamodb.Client
	sm     *secretsmanager.Client
	lambda *awslambda.Client
}

type SQSPayload struct {
	CouncilID string `json:"council_id"`
	PDFTitle  string `json:"pdf_title"`
	PDFURL    string `json:"pdf_url"`
	TotalPDFs int    `json:"total_pdfs"`
}

func (h *WorkerHandler) HandleRequest(ctx context.Context, event events.SQSEvent) error {
	secretOut, err := h.sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(os.Getenv("GEMINI_SECRET_ARN")),
	})
	if err != nil {
		return fmt.Errorf("get secret: %w", err)
	}
	apiKey := *secretOut.SecretString

	for _, record := range event.Records {
		var msg SQSPayload
		if err := json.Unmarshal([]byte(record.Body), &msg); err != nil {
			log.Printf("error unmarshaling SQS body: %v", record.Body)
			continue
		}

		pdfBytes, err := downloadPDF(msg.PDFURL)
		if err != nil {
			log.Printf("error downloading PDF %s: %v", msg.PDFURL, err)
			continue
		}

		result, err := analyzeWithGemini(ctx, apiKey, pdfBytes)
		if err != nil {
			log.Printf("error analyzing with Gemini: %v", err)
			continue
		}

		if err := h.handleRecord(ctx, msg, result); err != nil {
			log.Printf("error handling record: %v", err)
			continue
		}
	}
	return nil
}

func (h *WorkerHandler) handleRecord(ctx context.Context, msg SQSPayload, result *GeminiResult) error {
	id := deliberationID(msg.PDFURL)

	// 1. Write to DynamoDB
	item, err := attributevalue.MarshalMap(map[string]interface{}{
		"id":              id,
		"council_id":      msg.CouncilID,
		"title":           result.Title,
		"topic_tag":      result.TopicTag,
		"pdf_url":         msg.PDFURL,
		"summary":         result.Summary,
		"is_substantial":  result.IsSubstantial,
		"analysis_data":   result.AnalysisData,
		"key_points":      result.KeyPoints,
		"has_vote":        result.Vote.HasVote,
		"vote_pour":       result.Vote.Pour,
		"vote_contre":     result.Vote.Contre,
		"vote_abstention": result.Vote.Abstention,
		"disagreements":   result.Disagreements,
		"input_tokens":    result.InputTokens,
		"output_tokens":   result.OutputTokens,
		"processed_at":    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal item: %w", err)
	}

	_, err = h.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(os.Getenv("DELIBERATIONS_TABLE")),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	if err != nil {
		if !strings.Contains(err.Error(), "ConditionalCheckFailedException") {
			return fmt.Errorf("put deliberation: %w", err)
		}
		log.Printf("deliberation %s already processed, skipping", id)
	}

	// 2. Increment counter
	out, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: msg.CouncilID},
		},
		UpdateExpression: aws.String("SET processed_pdfs = processed_pdfs + :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":one": &types.AttributeValueMemberN{Value: "1"},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return fmt.Errorf("update council counter: %w", err)
	}

	// 3. Complete?
	processed := attrInt(out.Attributes, "processed_pdfs")
	total := attrInt(out.Attributes, "total_pdfs")
	if processed >= total && total > 0 {
		log.Printf("council %s complete (%d/%d), invoking publisher", msg.CouncilID, processed, total)
		h.invokePublisher(ctx, msg.CouncilID)
	}

	// Metrics
	log.Printf("METRIC: GeminiUsage input=%d output=%d", result.InputTokens, result.OutputTokens)

	return nil
}

func (h *WorkerHandler) invokePublisher(ctx context.Context, councilID string) {
	payload, _ := json.Marshal(map[string]string{"council_id": councilID})
	_, err := h.lambda.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(os.Getenv("PUBLISHER_FUNCTION_NAME")),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payload,
	})
	if err != nil {
		log.Printf("error invoking publisher: %v", err)
	}
}

func downloadPDF(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func deliberationID(url string) string {
	parts := strings.Split(url, "/")
	return parts[len(parts)-1]
}

func attrInt(m map[string]types.AttributeValue, key string) int {
	if val, ok := m[key]; ok {
		if n, ok := val.(*types.AttributeValueMemberN); ok {
			i, _ := strconv.Atoi(n.Value)
			return i
		}
	}
	return 0
}
