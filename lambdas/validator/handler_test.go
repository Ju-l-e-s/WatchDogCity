package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/watchdog/shared"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

func ptrInt(i int) *int       { return &i }
func ptrStr(s string) *string { return &s }

// okDelibRec returns a fully-valid deliberationRec for DynamoDB.
func okDelibRec(id string) deliberationRec {
	ctx := "Contexte factuel."
	dec := "La ville approuve le projet."
	imp := "Impact direct sur les habitants."
	return deliberationRec{
		ID:       id,
		Title:    "Délibération " + id,
		Summary:  "Résumé court.",
		TopicTag: "Budget",
		PDFURL:   "https://example.com/" + id + ".pdf",
		AnalysisData: analysisDataRec{
			Contexte: &ctx,
			Decision: &dec,
			Impacts:  &imp,
		},
		BudgetImpact:  1000,
		BudgetType:    "DÉPENSE",
		ClimateImpact: "neutre",
		HasVote:       true,
		VotePour:      ptrInt(35),
		VoteContre:    ptrInt(0),
		VoteAbst:      ptrInt(0),
		IsSubstantial: true,
	}
}

// okDelibRecNeant returns a deliberationRec with impacts="Néant".
func okDelibRecNeant(id string) deliberationRec {
	d := okDelibRec(id)
	neant := "Néant"
	d.AnalysisData.Impacts = &neant
	d.BudgetType = "AUCUN"
	d.BudgetImpact = 0
	return d
}

func okCouncilRec() councilRec {
	return councilRec{
		CouncilID:     "council-1",
		Title:         "Conseil municipal du 15 mars 2025",
		Category:      "Conseil municipal",
		Date:          "2025-03-15",
		TotalPdfs:     3,
		ProcessedPdfs: 3,
		QcStatus:      "PENDING",
		QcAttempts:    0,
	}
}

// okVerdictApproved returns an APPROVED verdict.
func okVerdictApproved() shared.Verdict {
	return shared.Verdict{Status: "APPROVED", Violations: nil}
}

// okVerdictQuarantined returns a QUARANTINED verdict with one HIGH violation.
func okVerdictQuarantined() shared.Verdict {
	return shared.Verdict{
		Status: "QUARANTINED",
		Violations: []shared.Violation{{
			Rule:           "D2_BUDGET_AUCUN_NONZERO",
			Severity:       shared.SeverityHigh,
			DeliberationID: "delib-1",
			Field:          "budget_impact",
			Detail:         "budget_type=AUCUN but budget_impact=100",
		}},
	}
}

// marshalCouncil marshals a councilRec to a DynamoDB attribute-value map.
func marshalCouncil(c councilRec) map[string]ddbtypes.AttributeValue {
	m, err := attributevalue.MarshalMap(c)
	if err != nil {
		panic(err)
	}
	return m
}

// marshalDelib marshals a deliberationRec to a DynamoDB attribute-value map.
func marshalDelib(d deliberationRec) map[string]ddbtypes.AttributeValue {
	m, err := attributevalue.MarshalMap(d)
	if err != nil {
		panic(err)
	}
	return m
}

// makeHandler builds a ValidatorHandler with the given mocks and sensible defaults.
func makeHandler(ddb ddbAPI, lambdaClient lambdaAPI, sqsClient sqsAPI) *ValidatorHandler {
	return &ValidatorHandler{
		ddb:                ddb,
		lambdaClient:       lambdaClient,
		sqsClient:          sqsClient,
		councilsTable:      "councils",
		deliberationsTable: "deliberations",
		publisherFnName:    "publisher-fn",
		notifierFnName:     "notifier-fn",
		sqsQueueURL:        "https://sqs.example.com/queue",
		geminiDeps:         shared.GeminiDeps{APIKey: "", Model: ""},
		cfg:                shared.DefaultQcConfig(),
	}
}

// ── Mock DDB ─────────────────────────────────────────────────────────────────

type mockDDB struct {
	getItemFn    func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	queryFn      func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	scanFn       func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	updateItemFn func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	deleteItemFn func(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)

	updateItemCalls []*dynamodb.UpdateItemInput
	deleteItemCalls []*dynamodb.DeleteItemInput
}

func (m *mockDDB) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return m.getItemFn(ctx, in, opts...)
}
func (m *mockDDB) Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return m.queryFn(ctx, in, opts...)
}
func (m *mockDDB) Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return m.scanFn(ctx, in, opts...)
}
func (m *mockDDB) UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	m.updateItemCalls = append(m.updateItemCalls, in)
	if m.updateItemFn != nil {
		return m.updateItemFn(ctx, in, opts...)
	}
	return &dynamodb.UpdateItemOutput{}, nil
}
func (m *mockDDB) DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	m.deleteItemCalls = append(m.deleteItemCalls, in)
	if m.deleteItemFn != nil {
		return m.deleteItemFn(ctx, in, opts...)
	}
	return &dynamodb.DeleteItemOutput{}, nil
}

// ── Mock Lambda ──────────────────────────────────────────────────────────────

type mockLambda struct {
	invokeFn    func(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
	invokeCalls []*awslambda.InvokeInput
}

func (m *mockLambda) Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	m.invokeCalls = append(m.invokeCalls, in)
	if m.invokeFn != nil {
		return m.invokeFn(ctx, in, opts...)
	}
	return &awslambda.InvokeOutput{}, nil
}

// ── Mock SQS ─────────────────────────────────────────────────────────────────

type mockSQS struct {
	sendMessageFn    func(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	sendMessageCalls []*sqs.SendMessageInput
}

func (m *mockSQS) SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	m.sendMessageCalls = append(m.sendMessageCalls, in)
	if m.sendMessageFn != nil {
		return m.sendMessageFn(ctx, in, opts...)
	}
	return &sqs.SendMessageOutput{}, nil
}

// ── claimValidating tests ────────────────────────────────────────────────────

func TestClaimValidating_Success(t *testing.T) {
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return &dynamodb.UpdateItemOutput{}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	err := h.claimValidating(context.Background(), "council-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(ddb.updateItemCalls) != 1 {
		t.Fatalf("expected 1 UpdateItem call, got %d", len(ddb.updateItemCalls))
	}
	in := ddb.updateItemCalls[0]
	if *in.TableName != "councils" {
		t.Errorf("expected table=councils, got %q", *in.TableName)
	}
	if *in.ConditionExpression != "qc_status = :pending" {
		t.Errorf("unexpected ConditionExpression: %q", *in.ConditionExpression)
	}
}

func TestClaimValidating_AlreadyClaimed(t *testing.T) {
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("conditional check failed")}
		},
	}
	h := makeHandler(ddb, nil, nil)
	err := h.claimValidating(context.Background(), "council-1")
	if !errors.Is(err, errAlreadyClaimed) {
		t.Fatalf("expected errAlreadyClaimed, got %v", err)
	}
}

func TestClaimValidating_OtherDDBError(t *testing.T) {
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return nil, fmt.Errorf("internal server error")
		},
	}
	h := makeHandler(ddb, nil, nil)
	err := h.claimValidating(context.Background(), "council-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, errAlreadyClaimed) {
		t.Errorf("should not be errAlreadyClaimed for generic error")
	}
}

// ── handleQuarantine tests ───────────────────────────────────────────────────

func TestHandleQuarantine_Success(t *testing.T) {
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return &dynamodb.UpdateItemOutput{}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	err := h.handleQuarantine(context.Background(), "council-1", okVerdictQuarantined())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(ddb.updateItemCalls) != 1 {
		t.Fatalf("expected 1 UpdateItem call, got %d", len(ddb.updateItemCalls))
	}
	in := ddb.updateItemCalls[0]
	// Verify the value mapped to :q is "QUARANTINED" (it's an attribute value, not in the expression text)
	vals := in.ExpressionAttributeValues
	qVal, ok := vals[":q"].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		t.Fatal("expected :q to be AttributeValueMemberS")
	}
	if qVal.Value != "QUARANTINED" {
		t.Errorf("expected :q = QUARANTINED, got %q", qVal.Value)
	}
	// Verify violations JSON is present and valid
	vVal, ok := vals[":v"].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		t.Fatal("expected :v to be AttributeValueMemberS")
	}
	if vVal.Value == "" {
		t.Error("expected :v (violations JSON) to be non-empty")
	}
	// Verify ConditionalExpression
	if *in.ConditionExpression != "qc_status = :validating" {
		t.Errorf("ConditionExpression mismatch: %q", *in.ConditionExpression)
	}
	// Verify the update expression contains the expected fields
	if !strings.Contains(*in.UpdateExpression, "qc_status") {
		t.Errorf("UpdateExpression should contain qc_status, got %q", *in.UpdateExpression)
	}
}

func TestHandleQuarantine_AlreadyWritten_Idempotent(t *testing.T) {
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("conditional check failed")}
		},
	}
	h := makeHandler(ddb, nil, nil)
	err := h.handleQuarantine(context.Background(), "council-1", okVerdictQuarantined())
	if err != nil {
		t.Fatalf("expected nil (idempotent), got %v", err)
	}
}

// ── invokeNotifier tests ─────────────────────────────────────────────────────

func TestInvokeNotifier_EmptyFunctionName(t *testing.T) {
	lambda := &mockLambda{}
	h := makeHandler(nil, lambda, nil)
	h.notifierFnName = ""
	params := &shared.NewsletterParams{EmailSubject: "test"}
	h.invokeNotifier(context.Background(), "council-1", params)
	if len(lambda.invokeCalls) != 0 {
		t.Errorf("expected no Invoke calls, got %d", len(lambda.invokeCalls))
	}
}

func TestInvokeNotifier_Disabled(t *testing.T) {
	lambda := &mockLambda{}
	h := makeHandler(nil, lambda, nil)
	h.notifierFnName = "disabled"
	params := &shared.NewsletterParams{EmailSubject: "test"}
	h.invokeNotifier(context.Background(), "council-1", params)
	if len(lambda.invokeCalls) != 0 {
		t.Errorf("expected no Invoke calls for 'disabled', got %d", len(lambda.invokeCalls))
	}
}

func TestInvokeNotifier_Placeholder(t *testing.T) {
	lambda := &mockLambda{}
	h := makeHandler(nil, lambda, nil)
	h.notifierFnName = "placeholder"
	params := &shared.NewsletterParams{EmailSubject: "test"}
	h.invokeNotifier(context.Background(), "council-1", params)
	if len(lambda.invokeCalls) != 0 {
		t.Errorf("expected no Invoke calls for 'placeholder', got %d", len(lambda.invokeCalls))
	}
}

func TestInvokeNotifier_Valid(t *testing.T) {
	lambda := &mockLambda{}
	h := makeHandler(nil, lambda, nil)
	h.notifierFnName = "notifier-prod"
	params := &shared.NewsletterParams{EmailSubject: "test"}
	h.invokeNotifier(context.Background(), "council-1", params)

	if len(lambda.invokeCalls) != 1 {
		t.Fatalf("expected 1 Invoke call, got %d", len(lambda.invokeCalls))
	}
	in := lambda.invokeCalls[0]
	if *in.FunctionName != "notifier-prod" {
		t.Errorf("expected FunctionName=notifier-prod, got %q", *in.FunctionName)
	}
	if in.InvocationType != lambdatypes.InvocationTypeEvent {
		t.Errorf("expected InvocationType=Event, got %q", in.InvocationType)
	}
	if in.Payload == nil || len(in.Payload) == 0 {
		t.Error("expected non-empty Payload")
	}
	// Verify the payload contains council_id and newsletter_params
	var payload map[string]interface{}
	if err := json.Unmarshal(in.Payload, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if _, ok := payload["council_id"]; !ok {
		t.Error("payload missing council_id")
	}
	if _, ok := payload["newsletter_params"]; !ok {
		t.Error("payload missing newsletter_params")
	}
}

func TestInvokeNotifier_InvokeError_Logged(t *testing.T) {
	lambda := &mockLambda{
		invokeFn: func(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
			return nil, fmt.Errorf("invoke failed")
		},
	}
	h := makeHandler(nil, lambda, nil)
	h.notifierFnName = "notifier-prod"
	params := &shared.NewsletterParams{EmailSubject: "test"}
	// Should not panic — errors are logged, not returned
	h.invokeNotifier(context.Background(), "council-1", params)
	if len(lambda.invokeCalls) != 1 {
		t.Fatalf("expected 1 Invoke call, got %d", len(lambda.invokeCalls))
	}
}

// ── invokePublisher tests ────────────────────────────────────────────────────

func TestInvokePublisher_Success(t *testing.T) {
	lambda := &mockLambda{}
	h := makeHandler(nil, lambda, nil)
	h.invokePublisher(context.Background(), "council-1")

	if len(lambda.invokeCalls) != 1 {
		t.Fatalf("expected 1 Invoke call, got %d", len(lambda.invokeCalls))
	}
	in := lambda.invokeCalls[0]
	if *in.FunctionName != "publisher-fn" {
		t.Errorf("expected FunctionName=publisher-fn, got %q", *in.FunctionName)
	}
	if in.InvocationType != lambdatypes.InvocationTypeEvent {
		t.Errorf("expected InvocationType=Event, got %q", in.InvocationType)
	}
	if in.Payload == nil {
		t.Error("expected non-nil Payload")
	}
	var payload map[string]string
	if err := json.Unmarshal(in.Payload, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if payload["council_id"] != "council-1" {
		t.Errorf("expected council_id=council-1, got %q", payload["council_id"])
	}
}

func TestInvokePublisher_InvokeError_Logged(t *testing.T) {
	lambda := &mockLambda{
		invokeFn: func(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
			return nil, fmt.Errorf("invoke failed")
		},
	}
	h := makeHandler(nil, lambda, nil)
	// Should not panic
	h.invokePublisher(context.Background(), "council-1")
	if len(lambda.invokeCalls) != 1 {
		t.Fatalf("expected 1 Invoke call, got %d", len(lambda.invokeCalls))
	}
}

// ── maybeHeal tests ──────────────────────────────────────────────────────────

func TestMaybeHeal_NoSQSClient(t *testing.T) {
	ddb := &mockDDB{}
	h := makeHandler(ddb, nil, nil)
	h.sqsClient = nil
	h.sqsQueueURL = "https://sqs.example.com/queue"
	c := okCouncilRec()
	c.QcAttempts = 1
	h.maybeHeal(context.Background(), &c, []deliberationRec{okDelibRec("d1")})
	if len(ddb.deleteItemCalls) != 0 {
		t.Error("expected no DeleteItem calls when SQS is nil")
	}
}

func TestMaybeHeal_EmptyQueueURL(t *testing.T) {
	sqs := &mockSQS{}
	ddb := &mockDDB{}
	h := makeHandler(ddb, nil, sqs)
	h.sqsQueueURL = ""
	c := okCouncilRec()
	c.QcAttempts = 1
	h.maybeHeal(context.Background(), &c, []deliberationRec{okDelibRec("d1")})
	if len(ddb.deleteItemCalls) != 0 {
		t.Error("expected no DeleteItem calls when queue URL is empty")
	}
}

func TestMaybeHeal_AtMaxAttempts(t *testing.T) {
	sqs := &mockSQS{}
	ddb := &mockDDB{}
	h := makeHandler(ddb, nil, sqs)
	c := okCouncilRec()
	c.QcAttempts = maxHealAttempts // exactly at limit
	h.maybeHeal(context.Background(), &c, []deliberationRec{okDelibRec("d1")})
	if len(ddb.deleteItemCalls) != 0 {
		t.Error("expected no DeleteItem calls when at max attempts")
	}
	if len(sqs.sendMessageCalls) != 0 {
		t.Error("expected no SendMessage calls when at max attempts")
	}
}

func TestMaybeHeal_ExceededMaxAttempts(t *testing.T) {
	sqs := &mockSQS{}
	ddb := &mockDDB{}
	h := makeHandler(ddb, nil, sqs)
	c := okCouncilRec()
	c.QcAttempts = maxHealAttempts + 1
	h.maybeHeal(context.Background(), &c, []deliberationRec{okDelibRec("d1")})
	if len(ddb.deleteItemCalls) != 0 {
		t.Error("expected no DeleteItem calls when over max attempts")
	}
}

func TestMaybeHeal_NoPDFs(t *testing.T) {
	sqs := &mockSQS{}
	ddb := &mockDDB{}
	h := makeHandler(ddb, nil, sqs)
	c := okCouncilRec()
	c.QcAttempts = 1
	c.TotalPdfs = 0
	// Empty deliberation list
	h.maybeHeal(context.Background(), &c, []deliberationRec{})
	// Still resets and re-enqueues even with 0 PDFs — but DeleteItem and SendMessage loops are skipped because slice is empty
	if len(ddb.updateItemCalls) != 1 {
		t.Errorf("expected reset UpdateItem call, got %d", len(ddb.updateItemCalls))
	}
	// Verify REMOVE qc_status + SET processed_pdfs=0
	u := ddb.updateItemCalls[0]
	if !strings.Contains(*u.UpdateExpression, "REMOVE qc_status") {
		t.Errorf("expected REMOVE qc_status in expression, got %q", *u.UpdateExpression)
	}
}

func TestMaybeHeal_UnderLimit_WithPDFs(t *testing.T) {
	sqs := &mockSQS{}
	ddb := &mockDDB{
		deleteItemFn: func(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
			return &dynamodb.DeleteItemOutput{}, nil
		},
	}
	h := makeHandler(ddb, nil, sqs)
	c := okCouncilRec()
	c.QcAttempts = 1
	c.TotalPdfs = 2
	delibs := []deliberationRec{
		okDelibRec("d1"),
		okDelibRec("d2"),
	}
	h.maybeHeal(context.Background(), &c, delibs)

	// Should delete both deliberations
	if len(ddb.deleteItemCalls) != 2 {
		t.Errorf("expected 2 DeleteItem calls, got %d", len(ddb.deleteItemCalls))
	}
	// Should reset council
	if len(ddb.updateItemCalls) != 1 {
		t.Errorf("expected 1 UpdateItem call for reset, got %d", len(ddb.updateItemCalls))
	}
	// Should enqueue 2 SQS messages
	if len(sqs.sendMessageCalls) != 2 {
		t.Errorf("expected 2 SendMessage calls, got %d", len(sqs.sendMessageCalls))
	}
	// Verify SQS payload contains council_id and pdf_url
	for i, msg := range sqs.sendMessageCalls {
		if *msg.QueueUrl != "https://sqs.example.com/queue" {
			t.Errorf("SQS[%d]: expected queue URL, got %q", i, *msg.QueueUrl)
		}
		var body map[string]interface{}
		if err := json.Unmarshal([]byte(*msg.MessageBody), &body); err != nil {
			t.Errorf("SQS[%d]: invalid JSON body: %v", i, err)
		}
		if body["council_id"] != "council-1" {
			t.Errorf("SQS[%d]: council_id mismatch", i)
		}
	}
}

func TestMaybeHeal_SkipsEmptyPDFURL(t *testing.T) {
	sqs := &mockSQS{}
	ddb := &mockDDB{}
	h := makeHandler(ddb, nil, sqs)
	c := okCouncilRec()
	c.QcAttempts = 1
	c.TotalPdfs = 2
	d := okDelibRec("d1")
	d.PDFURL = "" // empty URL — should skip SQS
	delibs := []deliberationRec{d, okDelibRec("d2")}
	h.maybeHeal(context.Background(), &c, delibs)

	// Only 1 SQS message (the empty-PDF one is skipped)
	if len(sqs.sendMessageCalls) != 1 {
		t.Errorf("expected 1 SendMessage call (one skipped), got %d", len(sqs.sendMessageCalls))
	}
	if len(ddb.deleteItemCalls) != 2 {
		t.Errorf("expected 2 DeleteItem calls (still deleted even if no PDF), got %d", len(ddb.deleteItemCalls))
	}
}

func TestMaybeHeal_ResetFailure_NoReenqueue(t *testing.T) {
	sqs := &mockSQS{}
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			// Check if this is the reset call (REMOVE qc_status)
			if in.UpdateExpression != nil && strings.Contains(*in.UpdateExpression, "REMOVE qc_status") {
				return nil, fmt.Errorf("reset failed")
			}
			return &dynamodb.UpdateItemOutput{}, nil
		},
	}
	h := makeHandler(ddb, nil, sqs)
	c := okCouncilRec()
	c.QcAttempts = 1
	h.maybeHeal(context.Background(), &c, []deliberationRec{okDelibRec("d1")})

	// DeleteItem should have been called (happens before reset)
	if len(ddb.deleteItemCalls) == 0 {
		t.Error("expected DeleteItem calls before reset attempt")
	}
	// But SQS should NOT have been called because reset failed
	if len(sqs.sendMessageCalls) != 0 {
		t.Errorf("expected 0 SendMessage calls after reset failure, got %d", len(sqs.sendMessageCalls))
	}
}

// ── fetchCouncil tests ───────────────────────────────────────────────────────

func TestFetchCouncil_Found(t *testing.T) {
	c := okCouncilRec()
	ddb := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	got, err := h.fetchCouncil(context.Background(), "council-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got.CouncilID != "council-1" {
		t.Errorf("expected council_id=council-1, got %q", got.CouncilID)
	}
	if got.Title != "Conseil municipal du 15 mars 2025" {
		t.Errorf("title mismatch: %q", got.Title)
	}
}

func TestFetchCouncil_NotFound(t *testing.T) {
	ddb := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: nil}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	_, err := h.fetchCouncil(context.Background(), "council-1")
	if err == nil {
		t.Fatal("expected error for missing council")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %q", err.Error())
	}
}

func TestFetchCouncil_DDBError(t *testing.T) {
	ddb := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return nil, fmt.Errorf("dynamodb unavailable")
		},
	}
	h := makeHandler(ddb, nil, nil)
	_, err := h.fetchCouncil(context.Background(), "council-1")
	if err == nil {
		t.Fatal("expected error for DDB failure")
	}
}

// ── fetchDeliberations tests ─────────────────────────────────────────────────

func TestFetchDeliberations_Found(t *testing.T) {
	d1 := okDelibRec("d1")
	d2 := okDelibRec("d2")
	ddb := &mockDDB{
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			return &dynamodb.QueryOutput{
				Items:            []map[string]ddbtypes.AttributeValue{marshalDelib(d1), marshalDelib(d2)},
				LastEvaluatedKey: nil,
			}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	got, err := h.fetchDeliberations(context.Background(), "council-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 deliberations, got %d", len(got))
	}
	if got[0].ID != "d1" {
		t.Errorf("expected d1, got %q", got[0].ID)
	}
}

func TestFetchDeliberations_Empty(t *testing.T) {
	ddb := &mockDDB{
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			return &dynamodb.QueryOutput{Items: nil, LastEvaluatedKey: nil}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	got, err := h.fetchDeliberations(context.Background(), "council-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 deliberations, got %d", len(got))
	}
}

func TestFetchDeliberations_DDBError(t *testing.T) {
	ddb := &mockDDB{
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			return nil, fmt.Errorf("query failed")
		},
	}
	h := makeHandler(ddb, nil, nil)
	_, err := h.fetchDeliberations(context.Background(), "council-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ── computeBaseline tests ────────────────────────────────────────────────────

func TestComputeBaseline_WithData(t *testing.T) {
	ddb := &mockDDB{
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{
				Items: []map[string]ddbtypes.AttributeValue{
					{
						"qc_neant_rate":   &ddbtypes.AttributeValueMemberN{Value: "0.3"},
						"qc_budget_total": &ddbtypes.AttributeValueMemberN{Value: "50000"},
					},
					{
						"qc_neant_rate":   &ddbtypes.AttributeValueMemberN{Value: "0.1"},
						"qc_budget_total": &ddbtypes.AttributeValueMemberN{Value: "10000"},
					},
					{
						"qc_neant_rate":   &ddbtypes.AttributeValueMemberN{Value: "0.2"},
						"qc_budget_total": &ddbtypes.AttributeValueMemberN{Value: "30000"},
					},
				},
				LastEvaluatedKey: nil,
			}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	base, err := h.computeBaseline(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if base.SampleSize != 3 {
		t.Errorf("expected SampleSize=3, got %d", base.SampleSize)
	}
	// Median of [10000, 30000, 50000] = 30000
	if base.BudgetMedian != 30000 {
		t.Errorf("expected BudgetMedian=30000, got %d", base.BudgetMedian)
	}
	// Mean = (0.3+0.1+0.2)/3 = 0.2
	if base.NeantRateMean < 0.19 || base.NeantRateMean > 0.21 {
		t.Errorf("expected NeantRateMean≈0.2, got %f", base.NeantRateMean)
	}
}

func TestComputeBaseline_Empty(t *testing.T) {
	ddb := &mockDDB{
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{Items: nil, LastEvaluatedKey: nil}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	base, err := h.computeBaseline(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if base.SampleSize != 0 {
		t.Errorf("expected SampleSize=0, got %d", base.SampleSize)
	}
}

func TestComputeBaseline_ScanError(t *testing.T) {
	ddb := &mockDDB{
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return nil, fmt.Errorf("scan failed")
		},
	}
	h := makeHandler(ddb, nil, nil)
	_, err := h.computeBaseline(context.Background())
	if err == nil {
		t.Fatal("expected error for scan failure")
	}
}

// ── fetchNextMeeting tests ───────────────────────────────────────────────────

func TestFetchNextMeeting_Found(t *testing.T) {
	ddb := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			key := in.Key["council_id"].(*ddbtypes.AttributeValueMemberS).Value
			if key == "metadata#next_council" {
				meta, _ := attributevalue.MarshalMap(struct {
					DateText string `dynamodbav:"date_text"`
				}{DateText: "22 avril 2025"})
				return &dynamodb.GetItemOutput{Item: meta}, nil
			}
			return &dynamodb.GetItemOutput{}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	got := h.fetchNextMeeting(context.Background())
	if got != "22 avril 2025" {
		t.Errorf("expected '22 avril 2025', got %q", got)
	}
}

func TestFetchNextMeeting_NotFound(t *testing.T) {
	ddb := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: nil}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	got := h.fetchNextMeeting(context.Background())
	if got != "" {
		t.Errorf("expected empty string for missing meeting, got %q", got)
	}
}

func TestFetchNextMeeting_DDBError(t *testing.T) {
	ddb := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return nil, fmt.Errorf("dynamodb down")
		},
	}
	h := makeHandler(ddb, nil, nil)
	got := h.fetchNextMeeting(context.Background())
	if got != "" {
		t.Errorf("expected empty string on error, got %q", got)
	}
}

// ── fetchGlobalStats tests ───────────────────────────────────────────────────

func TestFetchGlobalStats_Success(t *testing.T) {
	callCount := 0
	ddb := &mockDDB{
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			callCount++
			// First call: councils count, second: deliberations count
			if callCount == 1 {
				return &dynamodb.ScanOutput{Count: 42, ScannedCount: 42, LastEvaluatedKey: nil}, nil
			}
			return &dynamodb.ScanOutput{Count: 150, ScannedCount: 150, LastEvaluatedKey: nil}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	councils, delibs := h.fetchGlobalStats(context.Background())
	if councils != 42 {
		t.Errorf("expected 42 councils, got %d", councils)
	}
	if delibs != 150 {
		t.Errorf("expected 150 delibs, got %d", delibs)
	}
}

func TestFetchGlobalStats_ScanError(t *testing.T) {
	ddb := &mockDDB{
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return nil, fmt.Errorf("scan failed")
		},
	}
	h := makeHandler(ddb, nil, nil)
	councils, delibs := h.fetchGlobalStats(context.Background())
	if councils != 0 || delibs != 0 {
		t.Errorf("expected 0,0 on scan error, got %d,%d", councils, delibs)
	}
}

// ── toDeliberationViews tests ────────────────────────────────────────────────

func TestToDeliberationViews(t *testing.T) {
	recs := []deliberationRec{okDelibRec("d1"), okDelibRec("d2")}
	views := toDeliberationViews(recs)
	if len(views) != 2 {
		t.Fatalf("expected 2 views, got %d", len(views))
	}
	if views[0].ID != "d1" {
		t.Errorf("expected d1, got %q", views[0].ID)
	}
	if views[0].Title != "Délibération d1" {
		t.Errorf("title mismatch: %q", views[0].Title)
	}
	if views[0].BudgetImpact != 1000 {
		t.Errorf("budget mismatch: %d", views[0].BudgetImpact)
	}
	if views[0].HasVote != true {
		t.Error("expected HasVote=true")
	}
}

func TestToDeliberationViews_EmptyBreakdown(t *testing.T) {
	r := okDelibRec("d1")
	r.BudgetBreakdown = nil
	views := toDeliberationViews([]deliberationRec{r})
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	if len(views[0].BudgetBreakdown) != 0 {
		t.Errorf("expected empty breakdown, got %d items", len(views[0].BudgetBreakdown))
	}
}

func TestToDeliberationViews_WithBreakdown(t *testing.T) {
	r := okDelibRec("d1")
	r.BudgetBreakdown = []budgetBreakdownRec{
		{TopicTag: "Sport", Label: "stade", Amount: 600},
		{TopicTag: "Culture", Label: "theatre", Amount: 400},
	}
	views := toDeliberationViews([]deliberationRec{r})
	if len(views[0].BudgetBreakdown) != 2 {
		t.Fatalf("expected 2 breakdown items, got %d", len(views[0].BudgetBreakdown))
	}
	if views[0].BudgetBreakdown[0].TopicTag != "Sport" {
		t.Errorf("breakdown tag mismatch: %q", views[0].BudgetBreakdown[0].TopicTag)
	}
	if views[0].BudgetBreakdown[0].Amount != 600 {
		t.Errorf("breakdown amount mismatch: %d", views[0].BudgetBreakdown[0].Amount)
	}
}

// ── HandleRequest table-driven tests ─────────────────────────────────────────

func TestHandleRequest(t *testing.T) {
	tests := []struct {
		name          string
		councilID     string
		setupMocks    func() (*mockDDB, *mockLambda, *mockSQS)
		wantErr       bool
		errContains   string
		validateFunc  func(t *testing.T, ddb *mockDDB, lambda *mockLambda, sqs *mockSQS)
	}{
		{
			name:      "Council already VALIDATING (errAlreadyClaimed)",
			councilID: "council-1",
			setupMocks: func() (*mockDDB, *mockLambda, *mockSQS) {
				ddb := &mockDDB{
					updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
						return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("conditional check failed")}
					},
				}
				return ddb, &mockLambda{}, &mockSQS{}
			},
			wantErr:     false,
			errContains: "",
			validateFunc: func(t *testing.T, ddb *mockDDB, lambda *mockLambda, sqs *mockSQS) {
				// Only claimValidating was called
				if len(ddb.updateItemCalls) != 1 {
					t.Errorf("expected 1 UpdateItem call (claim), got %d", len(ddb.updateItemCalls))
				}
			},
		},
		{
			name:      "Council already APPROVED (errAlreadyClaimed)",
			councilID: "council-1",
			setupMocks: func() (*mockDDB, *mockLambda, *mockSQS) {
				ddb := &mockDDB{
					updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
						return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("conditional check failed")}
					},
				}
				return ddb, &mockLambda{}, &mockSQS{}
			},
			wantErr: false,
			validateFunc: func(t *testing.T, ddb *mockDDB, lambda *mockLambda, sqs *mockSQS) {
				if len(ddb.updateItemCalls) != 1 {
					t.Errorf("expected 1 UpdateItem call, got %d", len(ddb.updateItemCalls))
				}
			},
		},
		{
			name:      "Council not found",
			councilID: "council-missing",
			setupMocks: func() (*mockDDB, *mockLambda, *mockSQS) {
				ddb := &mockDDB{
					updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
						return &dynamodb.UpdateItemOutput{}, nil
					},
					getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
						return &dynamodb.GetItemOutput{Item: nil}, nil
					},
				}
				return ddb, &mockLambda{}, &mockSQS{}
			},
			wantErr:     true,
			errContains: "not found",
			validateFunc: func(t *testing.T, ddb *mockDDB, lambda *mockLambda, sqs *mockSQS) {
				// claim should have succeeded, then fetch failed
				if len(ddb.updateItemCalls) < 1 {
					t.Error("expected claimValidating call")
				}
			},
		},
		{
			name:      "QUARANTINED via D2 (budget_type=AUCUN but budget_impact=100)",
			councilID: "council-1",
			setupMocks: func() (*mockDDB, *mockLambda, *mockSQS) {
				c := okCouncilRec()
				d := okDelibRec("d1")
				// D2 violation: AUCUN + nonzero budget
				d.BudgetType = "AUCUN"
				d.BudgetImpact = 100
				// Need impacts to not trigger D9
				d.AnalysisData.Impacts = ptrStr("Impact citoyen.")
				ddb := &mockDDB{
					updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
						return &dynamodb.UpdateItemOutput{}, nil
					},
					getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
						return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
					},
					queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
						return &dynamodb.QueryOutput{
							Items:            []map[string]ddbtypes.AttributeValue{marshalDelib(d)},
							LastEvaluatedKey: nil,
						}, nil
					},
					scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
						return &dynamodb.ScanOutput{Items: nil, Count: 0, LastEvaluatedKey: nil}, nil
					},
				}
				return ddb, &mockLambda{}, &mockSQS{}
			},
			wantErr: false,
			validateFunc: func(t *testing.T, ddb *mockDDB, lambda *mockLambda, sqs *mockSQS) {
				// Should have claim + quarantine (+ maybeHeal resets). Look for quarantine update
				// by checking expression attribute values, not expression text.
				foundQuarantine := false
				for _, u := range ddb.updateItemCalls {
					if u.ExpressionAttributeValues != nil {
						if qv, ok := u.ExpressionAttributeValues[":q"].(*ddbtypes.AttributeValueMemberS); ok && qv.Value == "QUARANTINED" {
							foundQuarantine = true
							break
						}
					}
				}
				if !foundQuarantine {
					t.Errorf("expected a QUARANTINED update (looked for :q=QUARANTINED in %d calls)", len(ddb.updateItemCalls))
				}
				// Publisher should NOT be invoked for QUARANTINED
				if len(lambda.invokeCalls) != 0 {
					t.Errorf("expected 0 lambda Invoke calls for QUARANTINED, got %d", len(lambda.invokeCalls))
				}
			},
		},
		{
			name:      "QUARANTINED via S2 (100% Néant above ceiling 0.65)",
			councilID: "council-1",
			setupMocks: func() (*mockDDB, *mockLambda, *mockSQS) {
				c := okCouncilRec()
				c.TotalPdfs = 4
				c.ProcessedPdfs = 4
				// All 4 deliberations have impacts="Néant" → rate=1.0 > 0.65
				var items []map[string]ddbtypes.AttributeValue
				for i := 1; i <= 4; i++ {
					d := okDelibRecNeant("d" + strconv.Itoa(i))
					items = append(items, marshalDelib(d))
				}
				ddb := &mockDDB{
					updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
						return &dynamodb.UpdateItemOutput{}, nil
					},
					getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
						return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
					},
					queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
						return &dynamodb.QueryOutput{Items: items, LastEvaluatedKey: nil}, nil
					},
					scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
						return &dynamodb.ScanOutput{Items: nil, Count: 0, LastEvaluatedKey: nil}, nil
					},
				}
				return ddb, &mockLambda{}, &mockSQS{}
			},
			wantErr: false,
			validateFunc: func(t *testing.T, ddb *mockDDB, lambda *mockLambda, sqs *mockSQS) {
				foundQuarantine := false
				for _, u := range ddb.updateItemCalls {
					if u.ExpressionAttributeValues != nil {
						if qv, ok := u.ExpressionAttributeValues[":q"].(*ddbtypes.AttributeValueMemberS); ok && qv.Value == "QUARANTINED" {
							foundQuarantine = true
							break
						}
					}
				}
				if !foundQuarantine {
					t.Error("expected QUARANTINED update to be written")
				}
				if len(lambda.invokeCalls) != 0 {
					t.Errorf("publisher should not be invoked for QUARANTINED, got %d calls", len(lambda.invokeCalls))
				}
			},
		},
		{
			name:      "handleApproved - Gemini error propagation (no real API key)",
			councilID: "council-1",
			setupMocks: func() (*mockDDB, *mockLambda, *mockSQS) {
				c := okCouncilRec()
				// Valid deliberation with NO HIGH violations: impacts != Néant,
				// valid enums, budget > 0, etc.
				d := okDelibRec("d1")
				d.BudgetType = "DÉPENSE"
				d.BudgetImpact = 1000
				d.AnalysisData.Impacts = ptrStr("Impact citoyen concret.") // not Néant → no S2
				d.TopicTag = "Budget"
				d.ClimateImpact = "neutre"
				d.HasVote = true
				d.VotePour = ptrInt(30)
				d.VoteContre = ptrInt(0)
				d.VoteAbst = ptrInt(0)
				ddb := &mockDDB{
					updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
						return &dynamodb.UpdateItemOutput{}, nil
					},
					getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
						key := in.Key["council_id"].(*ddbtypes.AttributeValueMemberS).Value
						if key == "council-1" {
							return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
						}
						return &dynamodb.GetItemOutput{Item: nil}, nil
					},
					queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
						return &dynamodb.QueryOutput{
							Items:            []map[string]ddbtypes.AttributeValue{marshalDelib(d)},
							LastEvaluatedKey: nil,
						}, nil
					},
					scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
						return &dynamodb.ScanOutput{Items: nil, Count: 0, LastEvaluatedKey: nil}, nil
					},
				}
				return ddb, &mockLambda{}, &mockSQS{}
			},
			wantErr:     true,
			errContains: "generate newsletter params",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ddb, lambdaClient, sqsClient := tt.setupMocks()
			h := makeHandler(ddb, lambdaClient, sqsClient)
			// Empty Gemini deps to force Gemini error in APPROVED path
			h.geminiDeps = shared.GeminiDeps{APIKey: "", Model: "gemini-2.0-flash"}

			err := h.HandleRequest(context.Background(), ValidatorEvent{CouncilID: tt.councilID})

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			}
			if tt.validateFunc != nil {
				tt.validateFunc(t, ddb, lambdaClient, sqsClient)
			}
		})
	}
}

// ── Integration-style test: HandleRequest QUARANTINED with self-heal ─────────

func TestHandleRequest_QuarantineWithSelfHeal(t *testing.T) {
	c := okCouncilRec()
	c.QcAttempts = 1
	d := okDelibRec("d1")
	d.BudgetType = "AUCUN"
	d.BudgetImpact = 100 // triggers D2
	d.AnalysisData.Impacts = ptrStr("Impact citoyen.")

	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return &dynamodb.UpdateItemOutput{}, nil
		},
		deleteItemFn: func(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
			return &dynamodb.DeleteItemOutput{}, nil
		},
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
		},
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			return &dynamodb.QueryOutput{
				Items:            []map[string]ddbtypes.AttributeValue{marshalDelib(d)},
				LastEvaluatedKey: nil,
			}, nil
		},
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{Items: nil, Count: 0, LastEvaluatedKey: nil}, nil
		},
	}
	sqs := &mockSQS{
		sendMessageFn: func(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
			return &sqs.SendMessageOutput{}, nil
		},
	}
	lambda := &mockLambda{}

	h := makeHandler(ddb, lambda, sqs)
	err := h.HandleRequest(context.Background(), ValidatorEvent{CouncilID: "council-1"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify quarantine was written
	quarantineFound := false
	for _, u := range ddb.updateItemCalls {
		if u.ExpressionAttributeValues != nil {
			if qv, ok := u.ExpressionAttributeValues[":q"].(*ddbtypes.AttributeValueMemberS); ok && qv.Value == "QUARANTINED" {
				quarantineFound = true
				break
			}
		}
	}
	if !quarantineFound {
		t.Error("expected QUARANTINED update")
	}

	// Self-heal should have deleted the deliberation
	if len(ddb.deleteItemCalls) != 1 {
		t.Errorf("expected 1 DeleteItem for self-heal, got %d", len(ddb.deleteItemCalls))
	}

	// Self-heal should have sent SQS message
	if len(sqs.sendMessageCalls) != 1 {
		t.Errorf("expected 1 SQS SendMessage for self-heal, got %d", len(sqs.sendMessageCalls))
	}

	// Self-heal should have reset the council
	resetFound := false
	for _, u := range ddb.updateItemCalls {
		if u.UpdateExpression != nil && strings.Contains(*u.UpdateExpression, "REMOVE qc_status") {
			resetFound = true
			break
		}
	}
	if !resetFound {
		t.Error("expected REMOVE qc_status reset update for self-heal")
	}
}

// ── handleApproved tests (isolated, testing non-Gemini branches) ─────────────

func TestHandleApproved_GeminiError(t *testing.T) {
	// handleApproved will fail because Gemini has no credentials
	c := okCouncilRec()
	delibs := []shared.DeliberationView{
		{
			ID: "d1", Title: "Test", Summary: "Summary", TopicTag: "Budget",
			BudgetType: "DÉPENSE", BudgetImpact: 1000, ClimateImpact: "neutre",
			HasVote: true, VotePour: ptrInt(35), VoteContre: ptrInt(0), VoteAbstention: ptrInt(0),
			AnalysisData: shared.QcAnalysisData{
				Contexte: ptrStr("ctx"), Decision: ptrStr("dec"), Impacts: ptrStr("Néant"),
			},
		},
	}
	verdict := okVerdictApproved()

	ddb := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			// fetchNextMeeting call
			return &dynamodb.GetItemOutput{Item: nil}, nil
		},
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			// fetchGlobalStats calls
			return &dynamodb.ScanOutput{Count: 0, LastEvaluatedKey: nil}, nil
		},
	}
	h := makeHandler(ddb, &mockLambda{}, &mockSQS{})
	h.geminiDeps = shared.GeminiDeps{APIKey: "", Model: "gemini-2.0-flash"}

	err := h.handleApproved(context.Background(), &c, delibs, verdict)
	if err == nil {
		t.Fatal("expected error from Gemini call, got nil")
	}
	if !strings.Contains(err.Error(), "generate newsletter params") {
		t.Errorf("expected 'generate newsletter params' in error, got %q", err.Error())
	}
}

// ── handleApproved: category not "Conseil municipal" — skips notifier ───────
// Note: This test exercises the code path AFTER GenerateNewsletterParams.
// Since we cannot mock Gemini without modifying handler.go, we test that
// the code correctly branches on category. The Gemini error catches us
// before we can reach the DDB update, but a full integration test would
// validate the complete path.

// ── Edge cases and regression tests ──────────────────────────────────────────

func TestToColdDelibs_BasicMapping(t *testing.T) {
	dv := shared.DeliberationView{
		Title:           "Test délib",
		TopicTag:        "Budget",
		BudgetImpact:    5000,
		BudgetType:      "DÉPENSE",
		HasVote:         true,
		VotePour:        ptrInt(30),
		VoteContre:      ptrInt(5),
		VoteAbstention:  ptrInt(2),
		ClimateImpact:   "neutre",
		IsSubstantial:   false,
		Summary:         "Résumé.",
		AnalysisData:    shared.QcAnalysisData{Impacts: ptrStr("Impact fort.")},
	}
	cold := toColdDelibs([]shared.DeliberationView{dv})
	if len(cold) != 1 {
		t.Fatalf("expected 1, got %d", len(cold))
	}
	c := cold[0]
	if c.Title != "Test délib" {
		t.Errorf("title mismatch: %q", c.Title)
	}
	if c.BudgetImpact != 5000 {
		t.Errorf("budget mismatch: %d", c.BudgetImpact)
	}
	if !c.IsSubstantial {
		t.Error("expected IsSubstantial=true (BudgetImpact >= 5000)")
	}
	if !c.HasDisagreement {
		t.Error("expected HasDisagreement=true (Contre=5)")
	}
	if c.Impacts != "Impact fort." {
		t.Errorf("impacts mismatch: %q", c.Impacts)
	}
}

func TestToColdDelibs_NoVoteNoDisagreement(t *testing.T) {
	dv := shared.DeliberationView{
		Title:         "Simple",
		TopicTag:      "Culture",
		Summary:       "x",
		BudgetImpact:  100,
		BudgetType:    "DÉPENSE",
		ClimateImpact: "neutre",
		HasVote:       false,
		IsSubstantial: false,
	}
	cold := toColdDelibs([]shared.DeliberationView{dv})
	if cold[0].HasDisagreement {
		t.Error("expected no disagreement when Contre=0 and Abstention=0 (both nil)")
	}
	if cold[0].IsSubstantial {
		t.Error("expected IsSubstantial=false for BudgetImpact=100")
	}
}

func TestToColdDelibs_AbstentionOnly_NoDisagreement(t *testing.T) {
	dv := shared.DeliberationView{
		Title:          "Abst only",
		TopicTag:       "Social",
		Summary:        "x",
		BudgetImpact:   0,
		BudgetType:     "AUCUN",
		ClimateImpact:  "neutre",
		HasVote:        true,
		VotePour:       ptrInt(10),
		VoteContre:     nil,
		VoteAbstention: ptrInt(3),
	}
	cold := toColdDelibs([]shared.DeliberationView{dv})
	// hasDisagreement = contre>0 || abst>0 → abst=3 >0 → true
	if !cold[0].HasDisagreement {
		t.Error("expected HasDisagreement=true when abstention > 0")
	}
}

// ── claimValidating with DynamoDB error propagation ─────────────────────────

func TestClaimValidating_NonCCFError(t *testing.T) {
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return nil, fmt.Errorf("provisioned throughput exceeded")
		},
	}
	h := makeHandler(ddb, nil, nil)
	err := h.claimValidating(context.Background(), "council-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errAlreadyClaimed) {
		t.Error("provisioned error should not be errAlreadyClaimed")
	}
}

// ── handleQuarantine with non-CCF DDB error ─────────────────────────────────

func TestHandleQuarantine_DDBError_NotCCF(t *testing.T) {
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return nil, fmt.Errorf("internal error")
		},
	}
	h := makeHandler(ddb, nil, nil)
	err := h.handleQuarantine(context.Background(), "council-1", okVerdictQuarantined())
	if err == nil {
		t.Fatal("expected error for non-CCF failure")
	}
}

// ── fetchDeliberations with pagination ───────────────────────────────────────

func TestFetchDeliberations_Paginated(t *testing.T) {
	d1 := okDelibRec("d1")
	d2 := okDelibRec("d2")
	d3 := okDelibRec("d3")
	callCount := 0
	ddb := &mockDDB{
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			callCount++
			if callCount == 1 {
				return &dynamodb.QueryOutput{
					Items:            []map[string]ddbtypes.AttributeValue{marshalDelib(d1)},
					LastEvaluatedKey: map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: "d1"}},
				}, nil
			}
			return &dynamodb.QueryOutput{
				Items:            []map[string]ddbtypes.AttributeValue{marshalDelib(d2), marshalDelib(d3)},
				LastEvaluatedKey: nil,
			}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	got, err := h.fetchDeliberations(context.Background(), "council-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 deliberations, got %d", len(got))
	}
	if callCount != 2 {
		t.Errorf("expected 2 query calls (pagination), got %d", callCount)
	}
}

// ── computeBaseline with paginated scan ──────────────────────────────────────

func TestComputeBaseline_Paginated(t *testing.T) {
	callCount := 0
	ddb := &mockDDB{
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			callCount++
			if callCount == 1 {
				return &dynamodb.ScanOutput{
					Items: []map[string]ddbtypes.AttributeValue{
						{"qc_neant_rate": &ddbtypes.AttributeValueMemberN{Value: "0.1"}, "qc_budget_total": &ddbtypes.AttributeValueMemberN{Value: "10000"}},
					},
					LastEvaluatedKey: map[string]ddbtypes.AttributeValue{"council_id": &ddbtypes.AttributeValueMemberS{Value: "c1"}},
				}, nil
			}
			return &dynamodb.ScanOutput{
				Items: []map[string]ddbtypes.AttributeValue{
					{"qc_neant_rate": &ddbtypes.AttributeValueMemberN{Value: "0.3"}, "qc_budget_total": &ddbtypes.AttributeValueMemberN{Value: "30000"}},
				},
				LastEvaluatedKey: nil,
			}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	base, err := h.computeBaseline(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if base.SampleSize != 2 {
		t.Errorf("expected SampleSize=2, got %d", base.SampleSize)
	}
}

// ── fetchDeliberations with invalid DynamoDB item structure ──────────────────

func TestFetchDeliberations_EmptyItems_Success(t *testing.T) {
	ddb := &mockDDB{
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			return &dynamodb.QueryOutput{Items: []map[string]ddbtypes.AttributeValue{}, LastEvaluatedKey: nil}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	got, err := h.fetchDeliberations(context.Background(), "council-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 deliberations, got %d", len(got))
	}
}

// ── fetchCouncil with valid record verifying all fields ──────────────────────

func TestFetchCouncil_AllFieldsParsed(t *testing.T) {
	c := okCouncilRec()
	c.QcAttempts = 2
	c.QcStatus = "PENDING"
	c.Category = "Conseil municipal"
	c.TotalPdfs = 5
	c.ProcessedPdfs = 3
	ddb := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
		},
	}
	h := makeHandler(ddb, nil, nil)
	got, err := h.fetchCouncil(context.Background(), "council-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got.QcAttempts != 2 {
		t.Errorf("expected QcAttempts=2, got %d", got.QcAttempts)
	}
	if got.QcStatus != "PENDING" {
		t.Errorf("expected QcStatus=PENDING, got %q", got.QcStatus)
	}
	if got.TotalPdfs != 5 {
		t.Errorf("expected TotalPdfs=5, got %d", got.TotalPdfs)
	}
	if got.ProcessedPdfs != 3 {
		t.Errorf("expected ProcessedPdfs=3, got %d", got.ProcessedPdfs)
	}
}

// ── invokeNotifier with full payload verification ────────────────────────────

func TestInvokeNotifier_PayloadContainsParams(t *testing.T) {
	lambda := &mockLambda{}
	h := makeHandler(nil, lambda, nil)
	h.notifierFnName = "notifier-prod"
	params := &shared.NewsletterParams{
		EmailSubject:  "Test Subject",
		CouncilTitle:  "Test Council",
		TotalDelibs:   5,
		TotalCouncils: 10,
	}
	h.invokeNotifier(context.Background(), "council-42", params)

	if len(lambda.invokeCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(lambda.invokeCalls))
	}
	call := lambda.invokeCalls[0]
	var payload map[string]interface{}
	if err := json.Unmarshal(call.Payload, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["council_id"] != "council-42" {
		t.Errorf("council_id mismatch: %v", payload["council_id"])
	}
	np, ok := payload["newsletter_params"].(map[string]interface{})
	if !ok {
		t.Fatal("newsletter_params missing or wrong type")
	}
	if np["email_subject"] != "Test Subject" {
		t.Errorf("email_subject mismatch: %v", np["email_subject"])
	}
}

// ── HandleRequest: computeBaseline failure is non-fatal ──────────────────────

func TestHandleRequest_ComputeBaselineFails_StillQuarantines(t *testing.T) {
	c := okCouncilRec()
	d := okDelibRec("d1")
	d.BudgetType = "AUCUN"
	d.BudgetImpact = 100
	d.AnalysisData.Impacts = ptrStr("Impact.")
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return &dynamodb.UpdateItemOutput{}, nil
		},
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
		},
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			return &dynamodb.QueryOutput{
				Items:            []map[string]ddbtypes.AttributeValue{marshalDelib(d)},
				LastEvaluatedKey: nil,
			}, nil
		},
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return nil, fmt.Errorf("scan unavailable")
		},
	}
	h := makeHandler(ddb, &mockLambda{}, &mockSQS{})
	// Disable heal so we only test quarantine
	h.sqsQueueURL = ""

	err := h.HandleRequest(context.Background(), ValidatorEvent{CouncilID: "council-1"})
	if err != nil {
		t.Fatalf("expected no error (baseline failure is non-fatal), got %v", err)
	}
	// Should still quarantine via D2
	quarantineFound := false
	for _, u := range ddb.updateItemCalls {
		if u.ExpressionAttributeValues != nil {
			if qv, ok := u.ExpressionAttributeValues[":q"].(*ddbtypes.AttributeValueMemberS); ok && qv.Value == "QUARANTINED" {
				quarantineFound = true
				break
			}
		}
	}
	if !quarantineFound {
		t.Error("expected QUARANTINED even with baseline failure")
	}
}

// ── HandleRequest: empty deliberation list ──────────────────────────────────

func TestHandleRequest_EmptyDeliberations(t *testing.T) {
	c := okCouncilRec()
	c.ProcessedPdfs = 3 // S1 should trigger (processed > 0 but no delibs)
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return &dynamodb.UpdateItemOutput{}, nil
		},
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
		},
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			return &dynamodb.QueryOutput{Items: nil, LastEvaluatedKey: nil}, nil
		},
		scanFn: func(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{Items: nil, Count: 0, LastEvaluatedKey: nil}, nil
		},
	}
	h := makeHandler(ddb, &mockLambda{}, &mockSQS{})
	h.sqsQueueURL = ""

	err := h.HandleRequest(context.Background(), ValidatorEvent{CouncilID: "council-1"})
	if err != nil {
		// S1 fires HIGH → QUARANTINED (no Gemini needed)
		t.Fatalf("expected no error for S1 quarantine, got %v", err)
	}
	quarantineFound := false
	for _, u := range ddb.updateItemCalls {
		if u.ExpressionAttributeValues != nil {
			if qv, ok := u.ExpressionAttributeValues[":q"].(*ddbtypes.AttributeValueMemberS); ok && qv.Value == "QUARANTINED" {
				quarantineFound = true
				break
			}
		}
	}
	if !quarantineFound {
		t.Error("expected QUARANTINED for empty deliberations (S1)")
	}
}

// ── maybeHeal: deletes with individual errors logged (non-fatal) ─────────────

func TestMaybeHeal_DeleteErrorsNonFatal(t *testing.T) {
	sqs := &mockSQS{}
	deleteCalls := 0
	ddb := &mockDDB{
		deleteItemFn: func(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
			deleteCalls++
			if deleteCalls == 2 {
				return nil, fmt.Errorf("delete failed for d2")
			}
			return &dynamodb.DeleteItemOutput{}, nil
		},
	}
	h := makeHandler(ddb, nil, sqs)
	c := okCouncilRec()
	c.QcAttempts = 1
	c.TotalPdfs = 3
	delibs := []deliberationRec{okDelibRec("d1"), okDelibRec("d2"), okDelibRec("d3")}
	h.maybeHeal(context.Background(), &c, delibs)

	// All 3 deletes attempted even though d2 fails
	if len(ddb.deleteItemCalls) != 3 {
		t.Errorf("expected 3 delete attempts, got %d", len(ddb.deleteItemCalls))
	}
	// Reset should still happen
	if len(ddb.updateItemCalls) != 1 {
		t.Errorf("expected 1 reset update, got %d", len(ddb.updateItemCalls))
	}
	// All 3 SQS should be sent (error in delete doesn't block SQS)
	if len(sqs.sendMessageCalls) != 3 {
		t.Errorf("expected 3 SQS messages, got %d", len(sqs.sendMessageCalls))
	}
}

// ── maybeHeal: SQS send errors logged but non-fatal ─────────────────────────

func TestMaybeHeal_SQSErrorsNonFatal(t *testing.T) {
	sqs := &mockSQS{
		sendMessageFn: func(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
			if strings.Contains(*in.MessageBody, "d2") {
				return nil, fmt.Errorf("sqs send failed for d2")
			}
			return &sqs.SendMessageOutput{}, nil
		},
	}
	ddb := &mockDDB{}
	h := makeHandler(ddb, nil, sqs)
	c := okCouncilRec()
	c.QcAttempts = 1
	c.TotalPdfs = 2
	delibs := []deliberationRec{okDelibRec("d1"), okDelibRec("d2")}
	h.maybeHeal(context.Background(), &c, delibs)

	// Both SendMessage calls attempted
	if len(sqs.sendMessageCalls) != 2 {
		t.Errorf("expected 2 SQS attempts, got %d", len(sqs.sendMessageCalls))
	}
}

// ── HandleRequest: claimValidating error (non-CCF) propagates ───────────────

func TestHandleRequest_ClaimFails_PropagatesError(t *testing.T) {
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return nil, fmt.Errorf("dynamodb internal failure")
		},
	}
	h := makeHandler(ddb, &mockLambda{}, &mockSQS{})
	err := h.HandleRequest(context.Background(), ValidatorEvent{CouncilID: "council-1"})
	if err == nil {
		t.Fatal("expected error for claim failure")
	}
	if !strings.Contains(err.Error(), "claim validating") {
		t.Errorf("error should mention 'claim validating', got %q", err.Error())
	}
}

// ── HandleRequest: fetchDeliberations error propagates ──────────────────────

func TestHandleRequest_FetchDeliberationsFails(t *testing.T) {
	c := okCouncilRec()
	ddb := &mockDDB{
		updateItemFn: func(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return &dynamodb.UpdateItemOutput{}, nil
		},
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: marshalCouncil(c)}, nil
		},
		queryFn: func(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
			return nil, fmt.Errorf("query failed")
		},
	}
	h := makeHandler(ddb, &mockLambda{}, &mockSQS{})
	err := h.HandleRequest(context.Background(), ValidatorEvent{CouncilID: "council-1"})
	if err == nil {
		t.Fatal("expected error for fetchDeliberations failure")
	}
	if !strings.Contains(err.Error(), "fetch deliberations") {
		t.Errorf("error should mention fetch, got %q", err.Error())
	}
}
