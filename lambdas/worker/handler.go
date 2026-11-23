package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/watchdog/shared"
)

type DynamoDBAPI interface {
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
	return h.handleBatch(ctx, event, h.reviewPDF)
}

type pdfReview func(context.Context, SQSPayload) (*GeminiResult, []byte, error)

func (h *WorkerHandler) handleBatch(ctx context.Context, event events.SQSEvent, review pdfReview) (events.SQSEventResponse, error) {
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
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
			log.Printf("error unmarshaling SQS body: %v", shared.TruncForLog(record.Body, 200))
			continue
		}

		if err := h.processPDF(ctx, msg, review); err != nil {
			log.Printf("PDF %s: %v", msg.PDFURL, err)
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
		}
	}
	return events.SQSEventResponse{BatchItemFailures: failures}, nil
}

func (h *WorkerHandler) reviewPDF(ctx context.Context, msg SQSPayload) (*GeminiResult, []byte, error) {
	dlCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	pdf, err := downloadPDF(dlCtx, msg.PDFURL)
	cancel()
	if err != nil {
		return nil, nil, err
	}
	key := os.Getenv("GEMINI_API_KEY")
	table := os.Getenv("COUNCILS_TABLE")
	gemCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	result, err := analyzeWithGemini(gemCtx, key, pdf)
	cancel()
	if err == nil {
		if e := shared.RecordGeminiSuccess(ctx, h.ddb, table); e != nil {
			log.Printf("record Gemini success: %v", e)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = verifyFactsWithGemini(checkCtx, key, pdf, result)
		cancel()
		if err != nil {
			outcome := "technical"
			if errors.Is(err, errFactCheckRejected) {
				outcome = "rejected"
			} else if errors.Is(err, errFactCheckReview) {
				outcome = "source_review"
			}
			log.Printf("METRIC: GeminiFactCheckFailure count=1 deliberation=%s outcome=%s", deliberationID(msg.PDFURL), outcome)
		}
	}
	if err != nil {
		if !isSubstantiveFactCheckRejection(err) {
			if e := shared.RecordGeminiError(ctx, h.ddb, table); e != nil {
				log.Printf("record Gemini error: %v", e)
			}
		}
		return nil, pdf, err
	}
	result.FactCheckModel = factCheckModel()
	result.FactCheckedAt = time.Now().UTC().Format(time.RFC3339)
	return result, pdf, nil
}

func (h *WorkerHandler) processPDF(ctx context.Context, msg SQSPayload, review pdfReview) error {
	if msg.CouncilID == "" || msg.PDFURL == "" {
		return errors.New("missing council_id or pdf_url")
	}
	owner := rand.Text()
	err := h.claimReview(ctx, msg, owner)
	if err != nil {
		var conflict *types.TransactionCanceledException
		if !errors.As(err, &conflict) {
			// The request may have committed despite a lost response. No review has started.
			return errors.Join(err, h.finishAttempt(ctx, msg, owner, nil, nil))
		}
		done, readErr := h.alreadyProcessedOrHeld(ctx, msg)
		if readErr != nil {
			return readErr
		}
		if done {
			return nil
		}
		return fmt.Errorf("claim PDF review: %w", err)
	}
	result, pdf, err := review(ctx, msg)
	if isSubstantiveFactCheckRejection(err) {
		// Never release the claim on this path, including on an ambiguous write response.
		return h.finishAttempt(ctx, msg, owner, pdf, err)
	}
	if err == nil {
		err = h.handleRecord(ctx, msg, result, owner)
		if err == nil {
			return nil
		}
		if isSubstantiveFactCheckRejection(err) {
			return h.finishAttempt(ctx, msg, owner, pdf, err)
		}
	}
	// Only a known technical outcome may unlock the document for another review.
	return errors.Join(err, h.finishAttempt(ctx, msg, owner, nil, nil))
}

func reviewHoldField(msg SQSPayload) string {
	return "pdf_hold_" + digest([]byte(deliberationID(msg.PDFURL)))
}

func (h *WorkerHandler) alreadyProcessedOrHeld(ctx context.Context, msg SQSPayload) (bool, error) {
	field := reviewHoldField(msg)
	out, err := h.ddb.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key:            map[string]types.AttributeValue{"council_id": &types.AttributeValueMemberS{Value: msg.CouncilID}},
		ConsistentRead: aws.Bool(true), ProjectionExpression: aws.String("#hold, qc_status, pdf_review_held"), ExpressionAttributeNames: map[string]string{"#hold": field}})
	if err != nil {
		return false, err
	}
	if _, pending := out.Item[field].(*types.AttributeValueMemberS); pending {
		return false, fmt.Errorf("unfinished PDF review for %s", msg.PDFURL)
	}
	if out.Item[field] != nil || out.Item["pdf_review_held"] != nil {
		return true, nil
	}
	if status, ok := out.Item["qc_status"].(*types.AttributeValueMemberS); ok && (status.Value == "APPROVED" || status.Value == "QUARANTINED") {
		return true, nil
	}
	id := deliberationID(msg.PDFURL)
	stored, err := h.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(os.Getenv("DELIBERATIONS_TABLE")),
		Key:            map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: id}},
		ConsistentRead: aws.Bool(true), ProjectionExpression: aws.String("council_id, counted"),
	})
	if err != nil {
		return false, err
	}
	if owner, ok := stored.Item["council_id"].(*types.AttributeValueMemberS); ok && owner.Value != msg.CouncilID {
		return false, fmt.Errorf("deliberation %s belongs to a different council", id)
	}
	return isCounted(stored.Item), nil
}

// #hold stores the sole reviewing attempt until it becomes a durable rejection.
// The set lets the council-wide gates test for any outstanding PDF in one condition.
func attemptUpdate(msg SQSPayload, owner string) *types.Update {
	return &types.Update{
		TableName:                aws.String(os.Getenv("COUNCILS_TABLE")),
		Key:                      map[string]types.AttributeValue{"council_id": &types.AttributeValueMemberS{Value: msg.CouncilID}},
		ExpressionAttributeNames: map[string]string{"#hold": reviewHoldField(msg)},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":owner": &types.AttributeValueMemberS{Value: owner},
			":ids":   &types.AttributeValueMemberSS{Value: []string{deliberationID(msg.PDFURL)}},
		},
	}
}

func (h *WorkerHandler) claimReview(ctx context.Context, msg SQSPayload, owner string) error {
	u := attemptUpdate(msg, owner)
	u.UpdateExpression = aws.String("SET #hold = :owner ADD pdf_reviews_pending :ids")
	u.ConditionExpression = aws.String("processed_pdfs < total_pdfs AND attribute_not_exists(qc_status) AND attribute_not_exists(pdf_review_held) AND attribute_not_exists(#hold)")
	_, err := h.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Update: u},
		{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(os.Getenv("DELIBERATIONS_TABLE")), Key: map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: deliberationID(msg.PDFURL)}},
			ConditionExpression:       aws.String("attribute_not_exists(counted) AND (attribute_not_exists(council_id) OR council_id = :cid)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":cid": &types.AttributeValueMemberS{Value: msg.CouncilID}},
		}},
	}})
	return err
}

func (h *WorkerHandler) finishAttempt(ctx context.Context, msg SQSPayload, owner string, pdf []byte, rejection error) error {
	u := attemptUpdate(msg, owner)
	expr := "REMOVE #hold DELETE pdf_reviews_pending :ids"
	if rejection != nil {
		marker, err := attributevalue.Marshal(map[string]string{"pdf_sha256": digest(pdf), "pdf_url": msg.PDFURL, "reason": shared.TruncForLog(rejection.Error(), 1000)})
		if err != nil {
			return err
		}
		u.ExpressionAttributeValues[":marker"] = marker
		u.ExpressionAttributeValues[":yes"] = &types.AttributeValueMemberBOOL{Value: true}
		expr = "SET #hold = :marker, pdf_review_held = :yes DELETE pdf_reviews_pending :ids"
	}
	_, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: u.TableName, Key: u.Key, ExpressionAttributeNames: u.ExpressionAttributeNames, ExpressionAttributeValues: u.ExpressionAttributeValues,
		ConditionExpression: aws.String("#hold = :owner"), UpdateExpression: aws.String(expr),
	})
	return err
}

func (h *WorkerHandler) handleRecord(ctx context.Context, msg SQSPayload, result *GeminiResult, owner string) error {
	if result == nil || result.FactCheckModel == "" || result.FactCheckedAt == "" {
		return fmt.Errorf("%w: refusing to store deliberation without a passed PDF fact check", errFactCheckReview)
	}
	if result.SourceFacts != nil {
		payload, err := factCheckPayload(result)
		if err != nil || result.SourceFacts.Version != shared.SourceFactsVersion ||
			result.SourceFacts.ProofStatus != shared.ProofModelReviewed ||
			result.SourceFacts.ReviewedPayloadSHA256 != digest(payload) {
			return fmt.Errorf("%w: refusing to store deliberation without matching source fact review", errFactCheckReview)
		}
	}
	id := deliberationID(msg.PDFURL)

	// 1. Write to DynamoDB
	fields := map[string]interface{}{
		"id":               id,
		"council_id":       msg.CouncilID,
		"title":            result.Title,
		"topic_tag":        result.TopicTag,
		"pdf_url":          msg.PDFURL,
		"summary":          result.Summary,
		"is_substantial":   result.IsSubstantial,
		"analysis_data":    result.AnalysisData,
		"acronyms":         result.Acronyms,
		"budget_note":      result.BudgetNote,
		"budget_impact":    result.BudgetImpact,
		"budget_type":      result.BudgetType,
		"budget_breakdown": result.BudgetBreakdown,
		"budget_changes":   result.BudgetChanges,
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
	}
	if result.FactCheckModel != "" && result.FactCheckedAt != "" {
		fields["fact_check_model"] = result.FactCheckModel
		fields["fact_checked_at"] = result.FactCheckedAt
	}
	if result.SourceFacts != nil {
		packet := *result.SourceFacts
		packet.Facts = result.CriticalFacts
		fields["source_facts"] = packet
	}
	item, err := attributevalue.MarshalMap(fields)
	if err != nil {
		return fmt.Errorf("marshal item: %w", err)
	}

	u := attemptUpdate(msg, owner)
	u.ConditionExpression = aws.String("#hold = :owner AND attribute_not_exists(pdf_review_held) AND attribute_not_exists(qc_status) AND processed_pdfs < total_pdfs")
	u.ExpressionAttributeValues[":one"] = &types.AttributeValueMemberN{Value: "1"}
	u.UpdateExpression = aws.String("REMOVE #hold ADD processed_pdfs :one DELETE pdf_reviews_pending :ids")
	item["counted"] = &types.AttributeValueMemberBOOL{Value: true}
	_, err = h.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Update: u},
		{Put: &types.Put{TableName: aws.String(os.Getenv("DELIBERATIONS_TABLE")), Item: item,
			ConditionExpression:       aws.String("attribute_not_exists(counted) AND (attribute_not_exists(council_id) OR council_id = :cid)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":cid": &types.AttributeValueMemberS{Value: msg.CouncilID}},
		}},
	}})
	if err != nil {
		return fmt.Errorf("commit verified PDF: %w", err)
	}
	// Metrics
	log.Printf("METRIC: GeminiUsage input=%d output=%d", result.InputTokens, result.OutputTokens)

	return nil
}

func downloadPDF(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	client := &http.Client{Timeout: 30 * time.Second}
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
	u := strings.TrimSuffix(url, "/")
	parts := strings.Split(u, "/")
	last := parts[len(parts)-1]
	if last == "" {
		sum := sha256.Sum256([]byte(url))
		return hex.EncodeToString(sum[:8])
	}
	return last
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
