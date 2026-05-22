package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDDB struct {
	putItemInput  *dynamodb.PutItemInput
	putItemErr    error
	getItemOut    *dynamodb.GetItemOutput
	updateItemOut *dynamodb.UpdateItemOutput
	updateItemErr error
	transactErr   error
}

func (m *mockDDB) PutItem(_ context.Context, params *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	m.putItemInput = params
	return &dynamodb.PutItemOutput{}, m.putItemErr
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
		Title:    "Budget 2026",
		Summary:  "Le budget a été voté.",
		TopicTag: "Budget",
	}
	result.Vote.HasVote = true
	result.Vote.Pour = &pour
	result.Vote.Contre = &contre
	result.Vote.Abstention = &abs

	err := h.handleRecord(context.Background(), msg, result)
	require.NoError(t, err)

	assert.NotNil(t, mockD.putItemInput)
	item := mockD.putItemInput.Item
	assert.Equal(t, &types.AttributeValueMemberS{Value: "Budget"}, item["topic_tag"])
	assert.Equal(t, &types.AttributeValueMemberS{Value: "Budget 2026"}, item["title"])
}

func TestHandleRecord_IdempotentDuplicate(t *testing.T) {
	condErr := &types.ConditionalCheckFailedException{}
	h := &WorkerHandler{
		ddb: &mockDDB{putItemErr: condErr},
	}
	msg := SQSPayload{CouncilID: "conseil_municipal#2026-03-28", PDFURL: "https://example.com/D01.pdf", PDFTitle: "D01", TotalPDFs: 5}
	err := h.handleRecord(context.Background(), msg, &GeminiResult{Title: "t", Summary: "s"})
	assert.NoError(t, err)
}

func TestHandleRecord_LastPDFDetection(t *testing.T) {
	mockL := &mockLambda{}
	h := &WorkerHandler{
		ddb: &mockDDB{
			// Post-transaction GetItem reports the council as complete.
			getItemOut: &dynamodb.GetItemOutput{
				Item: map[string]types.AttributeValue{
					"processed_pdfs": &types.AttributeValueMemberN{Value: "5"},
					"total_pdfs":     &types.AttributeValueMemberN{Value: "5"},
				},
			},
		},
		lambda: mockL,
	}
	msg := SQSPayload{CouncilID: "conseil_municipal#2026-03-28", PDFURL: "https://example.com/D05.pdf", PDFTitle: "D05", TotalPDFs: 5}
	err := h.handleRecord(context.Background(), msg, &GeminiResult{Title: "t", Summary: "s"})
	require.NoError(t, err)
	assert.NotNil(t, mockL.invokeInput)
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

// concurrentMockDDB is a thread-safe in-memory DynamoDB stand-in that honors
// the conditional PutItem (attribute_not_exists(id)) so concurrent workers race
// realistically over the same deliberation id.
type concurrentMockDDB struct {
	mu        sync.Mutex
	delibs    map[string]map[string]types.AttributeValue
	processed int
	total     int
	published bool
}

func newConcurrentMock(total int) *concurrentMockDDB {
	return &concurrentMockDDB{delibs: map[string]map[string]types.AttributeValue{}, total: total}
}

func (m *concurrentMockDDB) PutItem(_ context.Context, p *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := p.Item["id"].(*types.AttributeValueMemberS).Value
	if _, exists := m.delibs[id]; exists {
		return nil, &types.ConditionalCheckFailedException{}
	}
	cp := make(map[string]types.AttributeValue, len(p.Item))
	for k, v := range p.Item {
		cp[k] = v
	}
	m.delibs[id] = cp
	return &dynamodb.PutItemOutput{}, nil
}

func (m *concurrentMockDDB) GetItem(_ context.Context, p *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := p.Key["council_id"]; ok {
		item := map[string]types.AttributeValue{
			"processed_pdfs": &types.AttributeValueMemberN{Value: strconv.Itoa(m.processed)},
			"total_pdfs":     &types.AttributeValueMemberN{Value: strconv.Itoa(m.total)},
		}
		if m.published {
			item["published_at"] = &types.AttributeValueMemberS{Value: "set"}
		}
		return &dynamodb.GetItemOutput{Item: item}, nil
	}
	id := p.Key["id"].(*types.AttributeValueMemberS).Value
	if it, ok := m.delibs[id]; ok {
		// Return a copy, like real DynamoDB — the stored map keeps being mutated
		// (e.g. counted) under lock by concurrent transactions.
		cp := make(map[string]types.AttributeValue, len(it))
		for k, v := range it {
			cp[k] = v
		}
		return &dynamodb.GetItemOutput{Item: cp}, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

// TransactWriteItems emulates the atomic counter bump + counted flag. It honors
// attribute_not_exists(counted) on the deliberation and processed_pdfs <
// total_pdfs on the council, returning per-item cancellation reasons on failure.
func (m *concurrentMockDDB) TransactWriteItems(_ context.Context, p *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var delibID string
	for _, ti := range p.TransactItems {
		if ti.Update == nil {
			continue
		}
		if k, ok := ti.Update.Key["id"].(*types.AttributeValueMemberS); ok {
			delibID = k.Value
		}
	}
	it := m.delibs[delibID]
	if it == nil {
		it = map[string]types.AttributeValue{}
		m.delibs[delibID] = it
	}
	ccf, none := "ConditionalCheckFailed", "None"
	if _, counted := it["counted"]; counted {
		return nil, &types.TransactionCanceledException{
			CancellationReasons: []types.CancellationReason{{Code: &none}, {Code: &ccf}},
		}
	}
	if m.processed >= m.total {
		return nil, &types.TransactionCanceledException{
			CancellationReasons: []types.CancellationReason{{Code: &ccf}, {Code: &none}},
		}
	}
	m.processed++
	it["counted"] = &types.AttributeValueMemberBOOL{Value: true}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func (m *concurrentMockDDB) UpdateItem(_ context.Context, p *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := p.Key["council_id"]; ok {
		expr := aws.ToString(p.UpdateExpression)
		// QC-gate claim, guarded by attribute_not_exists(qc_status).
		if strings.Contains(expr, "qc_status") {
			if m.published {
				return nil, &types.ConditionalCheckFailedException{}
			}
			m.published = true
			return &dynamodb.UpdateItemOutput{}, nil
		}
		// Counter increment, capped by processed_pdfs < total_pdfs.
		if m.processed >= m.total {
			return nil, &types.ConditionalCheckFailedException{}
		}
		m.processed++
		return &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{
			"processed_pdfs": &types.AttributeValueMemberN{Value: strconv.Itoa(m.processed)},
			"total_pdfs":     &types.AttributeValueMemberN{Value: strconv.Itoa(m.total)},
		}}, nil
	}
	// Deliberation partial-recovery SET, guarded by attribute_not_exists(analysis_data).
	id := p.Key["id"].(*types.AttributeValueMemberS).Value
	it := m.delibs[id]
	if it == nil {
		it = map[string]types.AttributeValue{}
		m.delibs[id] = it
	}
	if hasAnalysisData(it) {
		return nil, &types.ConditionalCheckFailedException{}
	}
	it["analysis_data"] = &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
		"contexte": &types.AttributeValueMemberS{Value: "recovered"},
	}}
	return &dynamodb.UpdateItemOutput{}, nil
}

// Two workers racing the same deliberation id must increment the council
// counter exactly once and leave analysis_data populated.
func TestHandleRecord_ConcurrentDuplicateCountsOnce(t *testing.T) {
	m := newConcurrentMock(5)
	h := &WorkerHandler{ddb: m, lambda: &mockLambda{}}
	msg := SQSPayload{CouncilID: "C1", PDFURL: "https://example.com/D01.pdf", TotalPDFs: 5}

	newResult := func() *GeminiResult {
		return &GeminiResult{Title: "T", Summary: "S", TopicTag: "Budget"}
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = h.handleRecord(context.Background(), msg, newResult())
		}(i)
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Equal(t, 1, m.processed, "counter must be incremented exactly once")
	assert.True(t, hasAnalysisData(m.delibs["D01.pdf"]), "analysis_data must be present")
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

// Several workers crossing the completion boundary together must invoke the
// Validator exactly once, thanks to the capped counter and the qc_status gate.
func TestHandleRecord_SinglePublisherInvokeAtBoundary(t *testing.T) {
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
			errs[idx] = h.handleRecord(context.Background(), msg, &GeminiResult{Title: "T", Summary: "S"})
		}(i, u)
	}
	wg.Wait()

	for _, e := range errs {
		require.NoError(t, e)
	}

	m.mu.Lock()
	assert.Equal(t, 3, m.processed, "every distinct pdf must be counted once")
	assert.True(t, m.published, "completion must be claimed once")
	m.mu.Unlock()

	cl.mu.Lock()
	assert.Equal(t, 1, cl.count, "publisher must be invoked exactly once")
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
	require.NoError(t, h.handleRecord(context.Background(), msg, &GeminiResult{Title: "T", Summary: "S"}))
	require.NoError(t, h.handleRecord(context.Background(), msg, &GeminiResult{Title: "T", Summary: "S"}))

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Equal(t, 1, m.processed, "counter must be exactly 1: not 0 (lost) nor 2 (double-counted)")
	_, counted := m.delibs["D01.pdf"]["counted"]
	assert.True(t, counted, "deliberation must be marked counted after recovery")
}

var _ = time.Now
