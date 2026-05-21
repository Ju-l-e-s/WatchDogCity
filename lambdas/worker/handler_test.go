package main

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDDB struct {
	putItemInput  *dynamodb.PutItemInput
	putItemErr    error
	updateItemOut *dynamodb.UpdateItemOutput
	updateItemErr error
}

func (m *mockDDB) PutItem(_ context.Context, params *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	m.putItemInput = params
	return &dynamodb.PutItemOutput{}, m.putItemErr
}

func (m *mockDDB) GetItem(_ context.Context, params *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
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
			updateItemOut: &dynamodb.UpdateItemOutput{
				Attributes: map[string]types.AttributeValue{
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
	id := p.Key["id"].(*types.AttributeValueMemberS).Value
	if it, ok := m.delibs[id]; ok {
		return &dynamodb.GetItemOutput{Item: it}, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (m *concurrentMockDDB) UpdateItem(_ context.Context, p *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Council counter increment.
	if _, ok := p.Key["council_id"]; ok {
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

var _ = time.Now
