package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/aws/aws-lambda-go/events"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/watchdog/shared"
)

func verifiedGeminiResult(title, summary string) *GeminiResult {
	return &GeminiResult{Title: title, Summary: summary,
		FactCheckModel: "gemini-2.5-pro", FactCheckedAt: "2026-10-03T10:00:00Z"}
}

type mockDDB struct {
	putItemInput  *types.Put
	getItemOut    *dynamodb.GetItemOutput
	updateItemOut *dynamodb.UpdateItemOutput
	updateItemErr error
	transactErr   error
}

func (m *mockDDB) GetItem(_ context.Context, params *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if m.getItemOut != nil {
		return m.getItemOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (m *mockDDB) UpdateItem(_ context.Context, params *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	if m.updateItemOut != nil {
		return m.updateItemOut, m.updateItemErr
	}
	return &dynamodb.UpdateItemOutput{
		Attributes: map[string]types.AttributeValue{
			"processed_pdfs": &types.AttributeValueMemberN{Value: "1"},
			"total_pdfs":     &types.AttributeValueMemberN{Value: "5"},
		},
	}, m.updateItemErr
}

func (m *mockDDB) TransactWriteItems(_ context.Context, params *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	for _, item := range params.TransactItems {
		if item.Put != nil {
			m.putItemInput = item.Put
		}
	}
	return &dynamodb.TransactWriteItemsOutput{}, m.transactErr
}

type mockLambda struct {
	invokeInput *awslambda.InvokeInput
	invokeErr   error
}

func (m *mockLambda) Invoke(_ context.Context, params *awslambda.InvokeInput, _ ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	m.invokeInput = params
	return &awslambda.InvokeOutput{}, m.invokeErr
}

func TestHandleRecord_TopicTagPersistence(t *testing.T) {
	mockD := &mockDDB{}
	mockL := &mockLambda{}
	h := &WorkerHandler{
		ddb:    mockD,
		lambda: mockL,
	}
	msg := SQSPayload{CouncilID: "C1", PDFURL: "https://example.com/D01.pdf"}

	pour, contre, abs := 20, 5, 2
	result := &GeminiResult{
		Title:          "Budget 2026",
		Summary:        "Le budget a été voté.",
		TopicTag:       "Budget",
		FactCheckModel: "gemini-2.5-pro",
		FactCheckedAt:  "2026-10-03T10:00:00Z",
		BudgetNote:     "Dépense ponctuelle votée de 10 000 €.",
	}
	result.Vote.HasVote = true
	result.Vote.Pour = &pour
	result.Vote.Contre = &contre
	result.Vote.Abstention = &abs

	err := h.handleRecord(context.Background(), msg, result, "owner")
	require.NoError(t, err)

	assert.NotNil(t, mockD.putItemInput)
	item := mockD.putItemInput.Item
	assert.Equal(t, &types.AttributeValueMemberS{Value: "Budget"}, item["topic_tag"])
	assert.Equal(t, &types.AttributeValueMemberS{Value: "Budget 2026"}, item["title"])
	assert.Equal(t, &types.AttributeValueMemberS{Value: "gemini-2.5-pro"}, item["fact_check_model"])
	assert.Equal(t, &types.AttributeValueMemberS{Value: "2026-10-03T10:00:00Z"}, item["fact_checked_at"])
	assert.Equal(t, &types.AttributeValueMemberS{Value: "Dépense ponctuelle votée de 10 000 €."}, item["budget_note"])
}

func TestHandleRecord_IdempotentDuplicate(t *testing.T) {
	m := newConcurrentMock(1)
	h := WorkerHandler{ddb: m}
	msg := SQSPayload{CouncilID: "C1", PDFURL: "D1.pdf"}
	run := func(context.Context, SQSPayload) (*GeminiResult, []byte, error) {
		return verifiedGeminiResult("T", "S"), nil, nil
	}
	require.NoError(t, h.processPDF(context.Background(), msg, run))
	require.NoError(t, h.processPDF(context.Background(), msg, nil))
	require.Equal(t, 1, m.processed)
}

func TestHandleRecordRejectsUnverifiedResult(t *testing.T) {
	h := &WorkerHandler{ddb: &mockDDB{}}
	err := h.handleRecord(context.Background(), SQSPayload{PDFURL: "https://example.com/D01.pdf"}, &GeminiResult{Title: "T"}, "owner")
	require.ErrorContains(t, err, "without a passed PDF fact check")
}

func TestDeliberationID(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		checkFn func(string) bool
	}{
		{
			name:    "normal URL",
			url:     "https://example.com/D01.pdf",
			checkFn: func(id string) bool { return id == "D01.pdf" },
		},
		{
			name:    "URL with trailing slash",
			url:     "https://example.com/D01.pdf/",
			checkFn: func(id string) bool { return id == "D01.pdf" },
		},
		{
			name: "empty last segment (double slash)",
			url:  "https://example.com/D01.pdf//",
			checkFn: func(id string) bool {
				// Should fall back to SHA256 hash instead of empty string
				return id != "" && len(id) == 16 // hex of 8 bytes
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := deliberationID(tt.url)
			assert.True(t, tt.checkFn(id), "deliberationID(%q) = %q", tt.url, id)
		})
	}
}

// Models the conditional writes and set membership used by the Worker.
type concurrentMockDDB struct {
	mockDDB
	mu               sync.Mutex
	council          map[string]types.AttributeValue
	pending          map[string]bool
	delibs           map[string]map[string]types.AttributeValue
	processed, total int
	failHold         bool
}

func newConcurrentMock(total int) *concurrentMockDDB {
	return &concurrentMockDDB{
		council: map[string]types.AttributeValue{}, pending: map[string]bool{}, delibs: map[string]map[string]types.AttributeValue{}, total: total}
}
func (m *concurrentMockDDB) GetItem(_ context.Context, p *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := p.Key["id"].(*types.AttributeValueMemberS); ok {
		return &dynamodb.GetItemOutput{Item: maps.Clone(m.delibs[id.Value])}, nil
	}
	return &dynamodb.GetItemOutput{Item: maps.Clone(m.council)}, nil
}
func (m *concurrentMockDDB) TransactWriteItems(_ context.Context, p *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := p.TransactItems[0].Update
	field := u.ExpressionAttributeNames["#hold"]
	token := u.ExpressionAttributeValues[":owner"].(*types.AttributeValueMemberS).Value
	id := u.ExpressionAttributeValues[":ids"].(*types.AttributeValueMemberSS).Value[0]
	other := p.TransactItems[1]
	if m.processed >= m.total || m.council["pdf_review_held"] != nil || m.council["qc_status"] != nil || isCounted(m.delibs[id]) {
		return nil, &types.TransactionCanceledException{}
	}
	if other.ConditionCheck != nil {
		if aws.ToString(u.UpdateExpression) != "SET #hold = :owner ADD pdf_reviews_pending :ids" || !strings.Contains(aws.ToString(u.ConditionExpression), "attribute_not_exists(#hold)") || other.ConditionCheck.Key["id"].(*types.AttributeValueMemberS).Value != id {
			panic("unguarded claim")
		}
		if m.council[field] != nil {
			return nil, &types.TransactionCanceledException{}
		}
		m.council[field] = &types.AttributeValueMemberS{Value: token}
		m.pending[id] = true
	} else {
		if !strings.Contains(aws.ToString(u.ConditionExpression), "#hold = :owner") || !strings.Contains(aws.ToString(u.UpdateExpression), "DELETE pdf_reviews_pending :ids") {
			panic("unfenced commit")
		}
		if owner, ok := m.council[field].(*types.AttributeValueMemberS); !ok || owner.Value != token {
			return nil, &types.TransactionCanceledException{}
		}
		m.delibs[id] = maps.Clone(other.Put.Item)
		delete(m.council, field)
		delete(m.pending, id)
		m.processed++
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}
func (m *concurrentMockDDB) UpdateItem(_ context.Context, p *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	field := p.ExpressionAttributeNames["#hold"]
	if aws.ToString(p.ConditionExpression) != "#hold = :owner" {
		panic("unfenced release")
	}
	owner, ok := m.council[field].(*types.AttributeValueMemberS)
	if !ok || owner.Value != p.ExpressionAttributeValues[":owner"].(*types.AttributeValueMemberS).Value {
		return nil, &types.ConditionalCheckFailedException{}
	}
	if marker := p.ExpressionAttributeValues[":marker"]; marker != nil {
		if m.failHold {
			return nil, errors.New("DynamoDB unavailable")
		}
		m.council[field] = marker
		m.council["pdf_review_held"] = p.ExpressionAttributeValues[":yes"]
	} else {
		delete(m.council, field)
	}
	delete(m.pending, p.ExpressionAttributeValues[":ids"].(*types.AttributeValueMemberSS).Value[0])
	return &dynamodb.UpdateItemOutput{}, nil
}

// Two workers racing the same deliberation id must increment the council
// counter exactly once and leave analysis_data populated.
func TestHandleRecord_ConcurrentDuplicateCountsOnce(t *testing.T) {
	m := newConcurrentMock(5)
	h := &WorkerHandler{ddb: m, lambda: &mockLambda{}}
	msg := SQSPayload{CouncilID: "C1", PDFURL: "https://example.com/D01.pdf", TotalPDFs: 5}

	newResult := func() *GeminiResult {
		r := verifiedGeminiResult("T", "S")
		r.TopicTag = "Budget"
		return r
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = h.processPDF(context.Background(), msg, func(context.Context, SQSPayload) (*GeminiResult, []byte, error) { return newResult(), nil, nil })
		}(i)
	}
	wg.Wait()

	require.True(t, errs[0] == nil || errs[1] == nil)
	for _, err := range errs {
		if err != nil {
			require.ErrorContains(t, err, "unfinished PDF review")
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Equal(t, 1, m.processed, "counter must be incremented exactly once")
	assert.True(t, m.delibs["D01.pdf"]["analysis_data"] != nil, "analysis_data must be present")
}

// countingLambda counts Invoke calls thread-safely.
type countingLambda struct {
	mu    sync.Mutex
	count int
}

func (l *countingLambda) Invoke(_ context.Context, p *awslambda.InvokeInput, _ ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	l.mu.Lock()
	l.count++
	l.mu.Unlock()
	return &awslambda.InvokeOutput{}, nil
}

// Several workers crossing the completion boundary together must increment the counter exactly once per PDF.
func TestHandleRecord_ConcurrentCountingAtBoundary(t *testing.T) {
	m := newConcurrentMock(3)
	cl := &countingLambda{}
	h := &WorkerHandler{ddb: m, lambda: cl}

	urls := []string{"D01.pdf", "D02.pdf", "D03.pdf"}
	var wg sync.WaitGroup
	errs := make([]error, len(urls))
	for i, u := range urls {
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			msg := SQSPayload{CouncilID: "C1", PDFURL: "https://example.com/" + url, TotalPDFs: 3}
			errs[idx] = h.processPDF(context.Background(), msg, func(context.Context, SQSPayload) (*GeminiResult, []byte, error) {
				return verifiedGeminiResult("T", "S"), nil, nil
			})
		}(i, u)
	}
	wg.Wait()

	for _, e := range errs {
		require.NoError(t, e)
	}

	m.mu.Lock()
	assert.Equal(t, 3, m.processed, "every distinct pdf must be counted once")
	m.mu.Unlock()

	cl.mu.Lock()
	assert.Equal(t, 0, cl.count, "validator must not be invoked by the worker")
	cl.mu.Unlock()
}

// A worker that wrote the claim then crashed before counting leaves an item
// with analysis_data but no `counted`. A retry must count it exactly once, and
// further retries must not double-count.
func TestHandleRecord_RecoversUncountedAfterCrash(t *testing.T) {
	m := newConcurrentMock(3)
	m.delibs["D01.pdf"] = map[string]types.AttributeValue{
		"id": &types.AttributeValueMemberS{Value: "D01.pdf"},
		"analysis_data": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"contexte": &types.AttributeValueMemberS{Value: "written before crash"},
		}},
		// no "counted" attribute — the crash happened before the increment
	}
	h := &WorkerHandler{ddb: m, lambda: &countingLambda{}}
	msg := SQSPayload{CouncilID: "C1", PDFURL: "https://example.com/D01.pdf", TotalPDFs: 3}

	// Two sequential retries: the first recovers and counts, the second is a noop.
	require.NoError(t, h.processPDF(context.Background(), msg, func(context.Context, SQSPayload) (*GeminiResult, []byte, error) {
		return verifiedGeminiResult("T", "S"), nil, nil
	}))
	require.NoError(t, h.processPDF(context.Background(), msg, func(context.Context, SQSPayload) (*GeminiResult, []byte, error) {
		return verifiedGeminiResult("T", "S"), nil, nil
	}))

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Equal(t, 1, m.processed, "counter must be exactly 1: not 0 (lost) nor 2 (double-counted)")
	_, counted := m.delibs["D01.pdf"]["counted"]
	assert.True(t, counted, "deliberation must be marked counted after recovery")
}

func TestHandleRecordRejectsChangedReviewedPayload(t *testing.T) {
	result := verifiedGeminiResult("Titre", "Résumé validé")
	result.CriticalFacts = []shared.SourceFact{{ID: "decision", Kind: "DECISION", Label: "Une décision"}}
	payload, err := factCheckPayload(result)
	require.NoError(t, err)
	result.SourceFacts = &shared.SourceFacts{Version: shared.SourceFactsVersion, PDFSHA256: digest([]byte("%PDF")), ProofStatus: shared.ProofModelReviewed,
		ReviewedPayloadSHA256: digest(payload), Facts: result.CriticalFacts}
	result.BudgetNote = "La subvention a déjà été reçue."
	// A write would dereference the absent DDB client. The check must happen first.
	err = (&WorkerHandler{}).handleRecord(context.Background(), SQSPayload{}, result, "owner")
	require.ErrorContains(t, err, "matching source fact review")
}

func TestSQSUnresolvedCriticalReview(t *testing.T) {
	for _, writeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "persisted", true: "DynamoDB_unavailable"}[writeFails], func(t *testing.T) {
			m := newConcurrentMock(1)
			m.failHold = writeFails
			h := WorkerHandler{ddb: m}
			msg := SQSPayload{CouncilID: "C1", PDFURL: "D1.pdf"}
			body, err := json.Marshal(msg)
			require.NoError(t, err)
			event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "first", Body: string(body)}}}
			duplicate := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "duplicate", Body: string(body)}}}
			started, finish := make(chan struct{}), make(chan struct{})
			first := make(chan events.SQSEventResponse, 1)
			go func() {
				r, _ := h.handleBatch(context.Background(), event, func(context.Context, SQSPayload) (*GeminiResult, []byte, error) {
					close(started)
					<-finish
					return nil, []byte("PDF"), errFactCheckRejected
				})
				first <- r
			}()
			<-started
			r, err := h.handleBatch(context.Background(), duplicate, nil) // nil review must never be called
			close(finish)
			require.NoError(t, err)
			require.Len(t, r.BatchItemFailures, 1)
			result := <-first
			replay, err := h.handleBatch(context.Background(), event, nil)
			require.NoError(t, err)
			if writeFails {
				require.Equal(t, []events.SQSBatchItemFailure{{ItemIdentifier: "first"}}, result.BatchItemFailures)
				require.Equal(t, result.BatchItemFailures, replay.BatchItemFailures)
				require.True(t, m.pending["D1.pdf"])
			} else {
				require.Empty(t, result.BatchItemFailures)
				require.Empty(t, replay.BatchItemFailures)
				require.NotNil(t, m.council["pdf_review_held"])
			}
			require.Error(t, h.handleRecord(context.Background(), msg, verifiedGeminiResult("T", "S"), "obsolete"))
			require.Error(t, h.finishAttempt(context.Background(), msg, "obsolete", nil, nil))
			require.Zero(t, m.processed)
			require.Empty(t, m.delibs)
		})
	}
}
func TestTechnicalFailureAllowsRetry(t *testing.T) {
	m := newConcurrentMock(1)
	h := WorkerHandler{ddb: m}
	msg := SQSPayload{CouncilID: "C1", PDFURL: "D1.pdf"}
	require.ErrorIs(t, h.processPDF(context.Background(), msg, func(context.Context, SQSPayload) (*GeminiResult, []byte, error) {
		return nil, nil, context.DeadlineExceeded
	}), context.DeadlineExceeded)
	require.Empty(t, m.pending)
	require.NoError(t, h.processPDF(context.Background(), msg, func(context.Context, SQSPayload) (*GeminiResult, []byte, error) {
		return verifiedGeminiResult("T", "S"), nil, nil
	}))
	require.Equal(t, 1, m.processed)
}
