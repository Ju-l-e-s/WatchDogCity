package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDDB implements ddbClient for tests.
type mockDDB struct {
	getItemFn   func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	updateCount int
}

func (m *mockDDB) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return m.getItemFn(ctx, in, opts...)
}

func (m *mockDDB) UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	m.updateCount++
	return &dynamodb.UpdateItemOutput{}, nil
}

func (m *mockDDB) PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}

func (m *mockDDB) Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

// mockSQS implements sqsClientAPI for tests.
type mockSQS struct {
	batchCount int
}

func (m *mockSQS) SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	m.batchCount++
	entries := make([]sqstypes.SendMessageBatchResultEntry, len(in.Entries))
	for i, e := range in.Entries {
		entries[i] = sqstypes.SendMessageBatchResultEntry{Id: e.Id}
	}
	return &sqs.SendMessageBatchOutput{Successful: entries}, nil
}

// mockScraper implements scraperAPI for tests.
type mockScraper struct {
	councils []CouncilListing
	pdfs     map[string][]PDFItem
}

func (m *mockScraper) ScrapeCouncilList(ctx context.Context) ([]CouncilListing, error) {
	return m.councils, nil
}

func (m *mockScraper) ScrapePDFLinks(ctx context.Context, url string) ([]PDFItem, error) {
	return m.pdfs[url], nil
}

func (m *mockScraper) ScrapeNextCouncilDate(ctx context.Context, url string) (string, error) {
	return "", fmt.Errorf("skipped in test")
}

// TestBuildCouncilUpdateInput_FirstDiscovery covers the path where a council
// is seen for the first time: processed_pdfs and created_at must be seeded
// with the supplied values via if_not_exists.
func TestBuildCouncilUpdateInput_FirstDiscovery(t *testing.T) {
	now := time.Date(2026, 5, 19, 18, 0, 0, 0, time.UTC)
	c := CouncilListing{
		CouncilID: "council-2026-05",
		Title:     "Conseil municipal du 5 mai",
		Category:  "Conseil municipal",
		Date:      "2026-05-05",
		URL:       "https://example.test/c1",
		Summary:   "Ordre du jour résumé",
	}

	in := buildCouncilUpdateInput("councils-test", c, 4, 0, now)

	require.NotNil(t, in.TableName)
	assert.Equal(t, "councils-test", *in.TableName)

	expr := *in.UpdateExpression
	assert.Contains(t, expr, "processed_pdfs = if_not_exists(processed_pdfs, :pp)",
		"counter must be preserved if it already exists")
	assert.Contains(t, expr, "created_at = if_not_exists(created_at, :ca)",
		"created_at must never be overwritten")
	assert.Contains(t, expr, "total_pdfs = :tp",
		"total_pdfs must follow the live scrape value")
	assert.Contains(t, expr, "#date = :d", "'date' is reserved and must be aliased")
	assert.Equal(t, "date", in.ExpressionAttributeNames["#date"])

	assert.Equal(t, "council-2026-05",
		in.Key["council_id"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, "0", in.ExpressionAttributeValues[":pp"].(*types.AttributeValueMemberN).Value)
	assert.Equal(t, "4", in.ExpressionAttributeValues[":tp"].(*types.AttributeValueMemberN).Value)
	assert.Equal(t, now.Format(time.RFC3339),
		in.ExpressionAttributeValues[":ca"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, c.Title, in.ExpressionAttributeValues[":t"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, c.Summary, in.ExpressionAttributeValues[":s"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, c.Category, in.ExpressionAttributeValues[":c"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, c.Date, in.ExpressionAttributeValues[":d"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, c.URL, in.ExpressionAttributeValues[":u"].(*types.AttributeValueMemberS).Value)
}

// TestBuildCouncilUpdateInput_RescanPreservesCounter mirrors the race
// scenario the fix targets: the orchestrator re-emits an update while the
// worker is still incrementing processed_pdfs. The :pp value is still
// transmitted (as a seed for first-discovery cases) but the expression must
// route it through if_not_exists.
func TestBuildCouncilUpdateInput_RescanPreservesCounter(t *testing.T) {
	in := buildCouncilUpdateInput(
		"councils-test",
		CouncilListing{CouncilID: "council-rescan"},
		5,
		3,
		time.Date(2026, 5, 19, 18, 0, 0, 0, time.UTC),
	)

	assert.True(t,
		strings.Contains(*in.UpdateExpression, "if_not_exists(processed_pdfs, :pp)"),
		"rescan must preserve in-flight counter via if_not_exists")
	assert.Equal(t, "3", in.ExpressionAttributeValues[":pp"].(*types.AttributeValueMemberN).Value)
	assert.Equal(t, "5", in.ExpressionAttributeValues[":tp"].(*types.AttributeValueMemberN).Value)
}

// TestBuildCouncilUpdateInput_AllowsShrinkingTotal — if the source page
// removes PDFs after some were already processed, the helper still emits a
// well-formed update; the surrounding orchestrator is responsible for
// logging the anomaly.
func TestBuildCouncilUpdateInput_AllowsShrinkingTotal(t *testing.T) {
	in := buildCouncilUpdateInput(
		"councils-test",
		CouncilListing{CouncilID: "council-shrink"},
		2,
		3,
		time.Now(),
	)
	assert.Equal(t, "2", in.ExpressionAttributeValues[":tp"].(*types.AttributeValueMemberN).Value)
	assert.Equal(t, "3", in.ExpressionAttributeValues[":pp"].(*types.AttributeValueMemberN).Value)
}

// TestHandleIsolatesPerCouncilErrors verifies that a GetItem failure on one
// council does not abort the entire cycle: councils before and after the
// failing one must still be processed (UpdateItem called for each).
func TestHandleIsolatesPerCouncilErrors(t *testing.T) {
	councils := []CouncilListing{
		{CouncilID: "c1", Title: "Council 1", URL: "https://example.test/c1", Category: "Conseil municipal", Date: "2026-01-01"},
		{CouncilID: "c2", Title: "Council 2", URL: "https://example.test/c2", Category: "Conseil municipal", Date: "2026-02-01"},
		{CouncilID: "c3", Title: "Council 3", URL: "https://example.test/c3", Category: "Conseil municipal", Date: "2026-03-01"},
	}

	db := &mockDDB{
		getItemFn: func(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			id := in.Key["council_id"].(*types.AttributeValueMemberS).Value
			if id == "c2" {
				return nil, fmt.Errorf("simulated DynamoDB error")
			}
			return &dynamodb.GetItemOutput{}, nil
		},
	}
	sqsMock := &mockSQS{}
	scraperMock := &mockScraper{
		councils: councils,
		pdfs: map[string][]PDFItem{
			"https://example.test/c1": {{Title: "PDF1", URL: "https://example.test/c1/doc.pdf"}},
			"https://example.test/c3": {{Title: "PDF3", URL: "https://example.test/c3/doc.pdf"}},
		},
	}

	o := &orchestrator{
		ddb:                db,
		sqs:                sqsMock,
		scraper:            scraperMock,
		councilsTable:      "councils-test",
		deliberationsTable: "deliberations-test",
		queueURL:           "https://sqs.test/queue",
	}

	err := o.handle(context.Background(), OrchestratorEvent{})
	require.NoError(t, err, "handle must return nil even when one council fails")
	assert.Equal(t, 2, db.updateCount, "UpdateItem must be called for c1 and c3, not c2")
	assert.Equal(t, 2, sqsMock.batchCount, "one SQS batch per successfully processed council")
}
