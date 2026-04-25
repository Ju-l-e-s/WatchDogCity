package main

import (
	"context"
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
		ddb: mockD,
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
	id := deliberationID("https://example.com/D01.pdf")
	assert.Equal(t, "D01.pdf", id)
}

var _ = time.Now
