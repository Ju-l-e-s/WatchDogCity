package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/watchdog/shared"
)

type DynamoDBAPI interface {
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
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

func (h *WorkerHandler) HandleRequest(ctx context.Context, event events.SQSEvent) (events.SQSEventResponse, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	councilsTable := os.Getenv("COUNCILS_TABLE")
	var failures []events.SQSBatchItemFailure

	// If Gemini is in an extended outage, defer the whole batch back to SQS
	// rather than burning attempts (and quota) on calls that will fail.
	if open, err := shared.GeminiCircuitOpen(ctx, h.ddb, councilsTable); err != nil {
		log.Printf("gemini circuit check failed, proceeding: %v", err)
	} else if open {
		log.Printf("gemini circuit OPEN — deferring %d SQS records for later retry", len(event.Records))
		for _, record := range event.Records {
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
		}
		return events.SQSEventResponse{BatchItemFailures: failures}, nil
	}

	for _, record := range event.Records {
		var msg SQSPayload
		if err := json.Unmarshal([]byte(record.Body), &msg); err != nil {
			log.Printf("error unmarshaling SQS body: %v", shared.TruncForLog(record.Body, 200))
			continue
		}

		// Idempotence is enforced by the conditional PutItem in handleRecord.
		// A pre-check GetItem here would only widen the race: two workers could
		// both read "absent", both spend ~minutes downloading and calling Gemini,
		// and the PutItem loser would have to recover afterwards anyway.
		dlCtx, dlCancel := context.WithTimeout(ctx, 60*time.Second)
		pdfBytes, err := downloadPDF(dlCtx, msg.PDFURL)
		dlCancel()
		if err != nil {
			log.Printf("error downloading PDF %s: %v", msg.PDFURL, err)
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
			continue
		}

		gemCtx, gemCancel := context.WithTimeout(ctx, 120*time.Second)
		result, err := analyzeWithGemini(gemCtx, apiKey, pdfBytes)
		gemCancel()
		if err != nil {
			log.Printf("error analyzing with Gemini: %v", err)
			if rerr := shared.RecordGeminiError(ctx, h.ddb, councilsTable); rerr != nil {
				log.Printf("warn: record gemini error: %v", rerr)
			}
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
			continue
		}
		if rerr := shared.RecordGeminiSuccess(ctx, h.ddb, councilsTable); rerr != nil {
			log.Printf("warn: record gemini success: %v", rerr)
		}

		if err := h.handleRecord(ctx, msg, result); err != nil {
			log.Printf("error handling record: %v", err)
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
			continue
		}
	}
	return events.SQSEventResponse{BatchItemFailures: failures}, nil
}

func (h *WorkerHandler) handleRecord(ctx context.Context, msg SQSPayload, result *GeminiResult) error {
	id := deliberationID(msg.PDFURL)

	// 1. Write to DynamoDB
	item, err := attributevalue.MarshalMap(map[string]interface{}{
		"id":               id,
		"council_id":       msg.CouncilID,
		"title":            result.Title,
		"topic_tag":        result.TopicTag,
		"pdf_url":          msg.PDFURL,
		"summary":          result.Summary,
		"is_substantial":   result.IsSubstantial,
		"analysis_data":    result.AnalysisData,
		"budget_impact":    result.BudgetImpact,
		"budget_type":      result.BudgetType,
		"budget_breakdown": result.BudgetBreakdown,
		"climate_impact":   result.ClimateImpact,
		"key_points":       result.KeyPoints,
		"has_vote":         result.Vote.HasVote,
		"vote_pour":        result.Vote.Pour,
		"vote_contre":      result.Vote.Contre,
		"vote_abstention":  result.Vote.Abstention,
		"disagreements":    result.Disagreements,
		"input_tokens":     result.InputTokens,
		"output_tokens":    result.OutputTokens,
		"processed_at":     time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal item: %w", err)
	}

	shouldCount := true
	_, err = h.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(os.Getenv("DELIBERATIONS_TABLE")),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	if err != nil {
		var ccfe *ddbtypes.ConditionalCheckFailedException
		if !errors.As(err, &ccfe) {
			return fmt.Errorf("put deliberation: %w", err)
		}
		// Lost the PutItem race, or a previous attempt already claimed this id.
		// Re-read to tell a fully-analyzed duplicate apart from a partial write
		// left behind by a worker that crashed before storing analysis_data.
		existing, gerr := h.ddb.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(os.Getenv("DELIBERATIONS_TABLE")),
			Key: map[string]types.AttributeValue{
				"id": &types.AttributeValueMemberS{Value: id},
			},
		})
		if gerr != nil {
			return fmt.Errorf("get deliberation %s after conflict: %w", id, gerr)
		}
		if hasAnalysisData(existing.Item) {
			log.Printf("deliberation %s already analyzed, skipping", id)
			return nil
		}
		// Partial state: fill the analysis fields, guarding against a third
		// worker that may complete the same item concurrently.
		setExpr, names, values := buildSetExpression(item, "id")
		_, uerr := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(os.Getenv("DELIBERATIONS_TABLE")),
			Key: map[string]types.AttributeValue{
				"id": &types.AttributeValueMemberS{Value: id},
			},
			UpdateExpression:          aws.String(setExpr),
			ConditionExpression:       aws.String("attribute_not_exists(analysis_data)"),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
		})
		if uerr != nil {
			if errors.As(uerr, &ccfe) {
				log.Printf("deliberation %s completed by another worker, skipping", id)
				return nil
			}
			return fmt.Errorf("recover partial deliberation %s: %w", id, uerr)
		}
	}

	if shouldCount {
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

func downloadPDF(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download PDF: HTTP %d for %s", resp.StatusCode, url)
	}
	limitedBody := http.MaxBytesReader(nil, resp.Body, 50<<20)
	pdfBytes, err := io.ReadAll(limitedBody)
	if err != nil {
		return nil, err
	}
	head := pdfBytes
	if len(head) > 128 {
		head = head[:128]
	}
	if !bytes.Contains(head, []byte("%PDF-")) {
		prefix := pdfBytes
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		return nil, fmt.Errorf("not a PDF: got prefix %q", prefix)
	}
	return pdfBytes, nil
}

func deliberationID(url string) string {
	u := strings.TrimRight(url, "/")
	parts := strings.Split(u, "/")
	last := parts[len(parts)-1]
	if last == "" {
		sum := sha256.Sum256([]byte(url))
		return hex.EncodeToString(sum[:8])
	}
	return last
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

// hasAnalysisData reports whether a deliberation item carries a populated
// analysis_data attribute, i.e. it was fully analyzed and is a real duplicate
// rather than a bare claim left behind by a crashed worker.
func hasAnalysisData(item map[string]types.AttributeValue) bool {
	v, ok := item["analysis_data"]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case *types.AttributeValueMemberNULL:
		return !t.Value
	case *types.AttributeValueMemberM:
		return len(t.Value) > 0
	case *types.AttributeValueMemberS:
		return t.Value != ""
	default:
		return true
	}
}

// buildSetExpression renders a DynamoDB SET update writing every attribute in
// item except the skipped keys. Name placeholders keep attribute names clear of
// reserved-word collisions.
func buildSetExpression(item map[string]types.AttributeValue, skip ...string) (string, map[string]string, map[string]types.AttributeValue) {
	skipped := make(map[string]bool, len(skip))
	for _, k := range skip {
		skipped[k] = true
	}
	names := map[string]string{}
	values := map[string]types.AttributeValue{}
	var sets []string
	i := 0
	for k, v := range item {
		if skipped[k] {
			continue
		}
		nk := fmt.Sprintf("#k%d", i)
		vk := fmt.Sprintf(":v%d", i)
		names[nk] = k
		values[vk] = v
		sets = append(sets, nk+" = "+vk)
		i++
	}
	return "SET " + strings.Join(sets, ", "), names, values
}
