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
	TransactWriteItems(ctx context.Context, params *dynamodb.TransactWriteItemsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
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
		// `counted` — not analysis_data — is the source of truth for "already
		// accounted". A worker that crashed after PutItem leaves analysis_data
		// behind but never counted; keying off counted lets us recover it.
		if isCounted(existing.Item) {
			log.Printf("deliberation %s already counted, skipping", id)
			return nil
		}
		// Not yet counted. If analysis is missing this is a bare/partial claim —
		// fill the fields (guarded so a concurrent worker can't be clobbered).
		// Either way we proceed to the counting transaction below, which is
		// itself idempotent via attribute_not_exists(counted).
		if !hasAnalysisData(existing.Item) {
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
			if uerr != nil && !errors.As(uerr, &ccfe) {
				return fmt.Errorf("recover partial deliberation %s: %w", id, uerr)
			}
		}
	}

	if shouldCount {
		// 2. Count this pdf atomically: bump the council counter (capped at
		//    total_pdfs) AND mark the deliberation counted, in one transaction.
		//    The attribute_not_exists(counted) guard makes counting idempotent —
		//    a retry of the same pdf can never double-count, and a council can
		//    never be pushed past total.
		_, terr := h.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			TransactItems: []types.TransactWriteItem{
				{
					Update: &types.Update{
						TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
						Key: map[string]types.AttributeValue{
							"council_id": &types.AttributeValueMemberS{Value: msg.CouncilID},
						},
						UpdateExpression:    aws.String("ADD processed_pdfs :one"),
						ConditionExpression: aws.String("processed_pdfs < total_pdfs"),
						ExpressionAttributeValues: map[string]types.AttributeValue{
							":one": &types.AttributeValueMemberN{Value: "1"},
						},
					},
				},
				{
					Update: &types.Update{
						TableName: aws.String(os.Getenv("DELIBERATIONS_TABLE")),
						Key: map[string]types.AttributeValue{
							"id": &types.AttributeValueMemberS{Value: id},
						},
						UpdateExpression:    aws.String("SET counted = :true"),
						ConditionExpression: aws.String("attribute_not_exists(counted)"),
						ExpressionAttributeValues: map[string]types.AttributeValue{
							":true": &types.AttributeValueMemberBOOL{Value: true},
						},
					},
				},
			},
		})
		if terr != nil {
			var tce *ddbtypes.TransactionCanceledException
			if !errors.As(terr, &tce) {
				return fmt.Errorf("transact counter for %s: %w", msg.CouncilID, terr)
			}
			// reasons[0] = council update, reasons[1] = deliberation update.
			reasons := tce.CancellationReasons
			switch {
			case len(reasons) > 1 && reasons[1].Code != nil && *reasons[1].Code == "ConditionalCheckFailed":
				log.Printf("deliberation %s already counted, skipping increment", id)
			case len(reasons) > 0 && reasons[0].Code != nil && *reasons[0].Code == "ConditionalCheckFailed":
				log.Printf("council %s already capped, skipping increment", msg.CouncilID)
			default:
				return fmt.Errorf("transact counter for %s: %w", msg.CouncilID, terr)
			}
			return nil
		}

		// 3. Complete? The transaction returns no attributes, so re-read the
		//    council, then claim the publish slot so a single worker fans out to
		//    the Publisher even when several cross the boundary together.
		cur, gerr := h.ddb.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
			Key: map[string]types.AttributeValue{
				"council_id": &types.AttributeValueMemberS{Value: msg.CouncilID},
			},
		})
		if gerr != nil {
			return fmt.Errorf("read council %s after counting: %w", msg.CouncilID, gerr)
		}
		processed := attrInt(cur.Item, "processed_pdfs")
		total := attrInt(cur.Item, "total_pdfs")
		if processed >= total && total > 0 {
			// Gate: only one worker fans out to the Validator.
			// qc_status starts absent; SET succeeds exactly once.
			_, perr := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
				TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
				Key: map[string]types.AttributeValue{
					"council_id": &types.AttributeValueMemberS{Value: msg.CouncilID},
				},
				UpdateExpression:    aws.String("SET qc_status = :pending"),
				ConditionExpression: aws.String("attribute_not_exists(qc_status)"),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":pending": &types.AttributeValueMemberS{Value: "PENDING"},
				},
			})
			if perr != nil {
				var ccfe *ddbtypes.ConditionalCheckFailedException
				if !errors.As(perr, &ccfe) {
					return fmt.Errorf("set qc_status=PENDING for %s: %w", msg.CouncilID, perr)
				}
				log.Printf("council %s qc_status already set, skipping validator", msg.CouncilID)
			} else {
				log.Printf("council %s complete (%d/%d), invoking validator", msg.CouncilID, processed, total)
				h.invokeValidator(ctx, msg.CouncilID)
			}
		}
	}

	// Metrics
	log.Printf("METRIC: GeminiUsage input=%d output=%d", result.InputTokens, result.OutputTokens)

	return nil
}

func (h *WorkerHandler) invokeValidator(ctx context.Context, councilID string) {
	payload, _ := json.Marshal(map[string]string{"council_id": councilID})
	_, err := h.lambda.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(os.Getenv("VALIDATOR_FUNCTION_NAME")),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payload,
	})
	if err != nil {
		log.Printf("error invoking validator: %v", err)
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

// isCounted reports whether a deliberation has already been tallied into its
// council counter. It is the authoritative "already accounted" signal,
// independent of whether analysis_data was written.
func isCounted(item map[string]types.AttributeValue) bool {
	if v, ok := item["counted"].(*types.AttributeValueMemberBOOL); ok {
		return v.Value
	}
	return false
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
