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
)

type DynamoDBAPI interface {
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type LambdaAPI interface {
	Invoke(ctx context.Context, params *awslambda.InvokeInput, optFns ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

type WorkerHandler struct {
	ddb    DynamoDBAPI
	lambda LambdaAPI
}

type SQSPayload struct {
	CouncilID string `json:"council_id"`
	PDFTitle  string `json:"pdf_title"`
	PDFURL    string `json:"pdf_url"`
	TotalPDFs int    `json:"total_pdfs"`
}

func (h *WorkerHandler) HandleRequest(ctx context.Context, event events.SQSEvent) error {
	apiKey := os.Getenv("GEMINI_API_KEY")

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
		"budget_impact":   result.BudgetImpact,
		"climate_impact":  result.ClimateImpact,
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
		// Item exists — update budget_impact if it was previously missing or zero
		if result.BudgetImpact > 0 {
			_, uerr := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
				TableName: aws.String(os.Getenv("DELIBERATIONS_TABLE")),
				Key: map[string]types.AttributeValue{
					"id": &types.AttributeValueMemberS{Value: id},
				},
				UpdateExpression:    aws.String("SET budget_impact = :bi"),
				ConditionExpression: aws.String("attribute_not_exists(budget_impact) OR budget_impact = :zero"),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":bi":   &types.AttributeValueMemberN{Value: strconv.FormatInt(result.BudgetImpact, 10)},
					":zero": &types.AttributeValueMemberN{Value: "0"},
				},
			})
			if uerr != nil && !strings.Contains(uerr.Error(), "ConditionalCheckFailedException") {
				log.Printf("warn: could not update budget_impact for %s: %v", id, uerr)
			}
		}
		log.Printf("deliberation %s already processed, budget_impact updated if needed", id)
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
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download PDF: HTTP %d for %s", resp.StatusCode, url)
	}
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
