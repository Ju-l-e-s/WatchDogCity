package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock DynamoDB client
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

func (m *mockDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	if m.updateItemOut != nil {
		return m.updateItemOut, m.updateItemErr
	}
	// Default: processed_pdfs < total_pdfs (not last PDF)
	return &dynamodb.UpdateItemOutput{
		Attributes: map[string]types.AttributeValue{
			"processed_pdfs": &types.AttributeValueMemberN{Value: "1"},
			"total_pdfs":     &types.AttributeValueMemberN{Value: "5"},
		},
	}, m.updateItemErr
}

func buildSQSEvent(msg SQSPayload) events.SQSEvent {
	body, _ := json.Marshal(msg)
	return events.SQSEvent{
		Records: []events.SQSMessage{
			{Body: string(body)},
		},
	}
}

func TestHandleRecord_TopicTagPersistence(t *testing.T) {
	mock := &mockDDB{}
	h := &WorkerHandler{
		ddb: mock,
	}
	msg := SQSPayload{CouncilID: "C1", PDFURL: "https://example.com/D01.pdf"}
	result := &GeminiResult{
		Title:    "Budget 2026",
		Summary:  "Le budget a été voté.",
		TopicTag: "Budget",
		Vote: struct {
			Pour       int `json:"pour"`
			Contre     int `json:"contre"`
			Abstention int `json:"abstention"`
		}{Pour: 20, Contre: 5, Abstention: 2},
	}
	err := h.handleRecord(context.Background(), msg, []byte("fakePDF"), result)
	require.NoError(t, err)

	assert.NotNil(t, mock.putItemInput)
	item := mock.putItemInput.Item
	assert.Equal(t, &types.AttributeValueMemberS{Value: "Budget"}, item["topic_tag"])
	assert.Equal(t, &types.AttributeValueMemberS{Value: "Budget 2026"}, item["title"])
}

func TestHandleRecord_IdempotentDuplicate(t *testing.T) {
	condErr := &types.ConditionalCheckFailedException{}
	h := &WorkerHandler{
		ddb: &mockDDB{putItemErr: condErr},
	}
	msg := SQSPayload{CouncilID: "conseil_municipal#2026-03-28", PDFURL: "https://example.com/D01.pdf", PDFTitle: "D01", TotalPDFs: 5}
	err := h.handleRecord(context.Background(), msg, []byte("fakePDF"), &GeminiResult{Title: "t", Summary: "s"})
	// Should NOT return error — duplicate is silently skipped
	assert.NoError(t, err)
}

func TestHandleRecord_LastPDFDetection(t *testing.T) {
	publisherInvoked := false
	h := &WorkerHandler{
		ddb: &mockDDB{
			updateItemOut: &dynamodb.UpdateItemOutput{
				Attributes: map[string]types.AttributeValue{
					"processed_pdfs": &types.AttributeValueMemberN{Value: "5"},
					"total_pdfs":     &types.AttributeValueMemberN{Value: "5"},
				},
			},
		},
		invokePublisher: func(_ context.Context, councilID string) error {
			publisherInvoked = true
			assert.Equal(t, "conseil_municipal#2026-03-28", councilID)
			return nil
		},
	}
	msg := SQSPayload{CouncilID: "conseil_municipal#2026-03-28", PDFURL: "https://example.com/D05.pdf", PDFTitle: "D05", TotalPDFs: 5}
	err := h.handleRecord(context.Background(), msg, []byte("fakePDF"), &GeminiResult{Title: "t", Summary: "s"})
	require.NoError(t, err)
	assert.True(t, publisherInvoked)
}

func TestDeliberationID(t *testing.T) {
	id := deliberationID("https://example.com/D01.pdf")
	assert.Len(t, id, 64) // SHA-256 hex
	// Same URL always gives same ID
	assert.Equal(t, id, deliberationID("https://example.com/D01.pdf"))
	// Different URL gives different ID
	assert.NotEqual(t, id, deliberationID("https://example.com/D02.pdf"))
}

// Suppress unused import for time
var _ = time.Now
