package main

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
