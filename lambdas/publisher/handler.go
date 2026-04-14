package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type PublisherEvent struct {
	CouncilID string `json:"council_id"`
}

type CouncilRecord struct {
	CouncilID string          `dynamodbav:"council_id"`
	Category  string          `dynamodbav:"category"`
	Date      string          `dynamodbav:"date"`
	Title     string          `dynamodbav:"title"`
	Summary   string          `dynamodbav:"summary"`
	SourceURL string          `dynamodbav:"source_url"`
	TotalPDFs int             `dynamodbav:"total_pdfs"`
	Processed int             `dynamodbav:"processed_pdfs"`
	Analysis  CouncilAnalysis `dynamodbav:"analysis"`
}

type CouncilAnalysis struct {
	BudgetImpact int64  `dynamodbav:"budget_impact" json:"budget_impact"`
	BudgetLabel  string `dynamodbav:"budget_label" json:"budget_label"`
	VoteClimat   string `dynamodbav:"vote_climat" json:"vote_climat"`
	VoteSummary  string `dynamodbav:"vote_summary" json:"vote_summary"`
	VotesPour    int    `dynamodbav:"votes_pour" json:"votes_pour"`
	VotesContre  int    `dynamodbav:"votes_contre" json:"votes_contre"`
}

type BudgetBreakdownItem struct {
	TopicTag string `dynamodbav:"topic_tag" json:"topic_tag"`
	Amount   int64  `dynamodbav:"amount"    json:"amount"`
}

type DeliberationRecord struct {
	ID              string                `dynamodbav:"id"`
	CouncilID       string                `dynamodbav:"council_id"`
	Title           string                `dynamodbav:"title"`
	TopicTag        string                `dynamodbav:"topic_tag"`
	PDFURL          string                `dynamodbav:"pdf_url"`
	Summary         string                `dynamodbav:"summary"`
	IsSubstantial   bool                  `dynamodbav:"is_substantial"`
	Acronyms        map[string]string     `dynamodbav:"acronyms"`
	AnalysisData    AnalysisData          `dynamodbav:"analysis_data"`
	BudgetImpact    int64                 `dynamodbav:"budget_impact"`
	BudgetBreakdown []BudgetBreakdownItem `dynamodbav:"budget_breakdown"`
	HasVote         bool                  `dynamodbav:"has_vote"`
	VotePour        *int                  `dynamodbav:"vote_pour"`
	VoteContre      *int                  `dynamodbav:"vote_contre"`
	VoteAbstention  *int                  `dynamodbav:"vote_abstention"`
	Disagreements   *string               `dynamodbav:"disagreements"`
	ProcessedAt     string                `dynamodbav:"processed_at"`
}

// ── JSON output format ────────────────────────────────────────────────────────

type DataJSON struct {
	GeneratedAt     string          `json:"generated_at"`
	NextCouncilDate string          `json:"next_council_date"`
	Councils        []CouncilOutput `json:"councils"`
}

type CouncilOutput struct {
	CouncilID     string               `json:"id"`
	Category      string               `json:"category"`
	Date          string               `json:"date"`
	Title         string               `json:"title"`
	Summary       string               `json:"summary"`
	SourceURL     string               `json:"source_url"`
	Analysis      CouncilAnalysis      `json:"analysis"`
	Deliberations []DeliberationOutput `json:"deliberations"`
}

type DeliberationOutput struct {
	ID              string                `json:"id"`
	Title           string                `json:"title"`
	TopicTag        string                `json:"topic_tag"`
	PDFURL          string                `json:"pdf_url"`
	Summary         string                `json:"summary"`
	IsSubstantial   bool                  `json:"is_substantial"`
	Acronyms        map[string]string     `json:"acronyms"`
	AnalysisData    AnalysisData          `json:"analysis_data"`
	BudgetImpact    int64                 `json:"budget_impact"`
	BudgetBreakdown []BudgetBreakdownItem `json:"budget_breakdown"`
	Vote            VoteCount             `json:"vote"`
	Disagreements   *string               `json:"disagreements"`
}

type AnalysisData struct {
	Contexte       *string `json:"contexte"`
	Decision       *string `json:"decision"`
	Impacts        *string `json:"impacts"`
	PointsDebattus *string `json:"points_debattus"`
}

type VoteCount struct {
	HasVote    bool `json:"has_vote"`
	Pour       *int `json:"pour"`
	Contre     *int `json:"contre"`
	Abstention *int `json:"abstention"`
}

// ── Business logic (pure, testable) ──────────────────────────────────────────

func buildDataJSON(ctx context.Context, ddb *dynamodb.Client, councils []CouncilRecord, delibs map[string][]DeliberationRecord) (*DataJSON, error) {
	out := &DataJSON{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		NextCouncilDate: fetchNextCouncilDate(ctx, ddb),
	}
	for _, c := range councils {
		analysis := c.Analysis
		// Budget impact = sum of individual deliberations only (verified PDF amounts)
		var deliberationsTotal int64
		for _, d := range delibs[c.CouncilID] {
			deliberationsTotal += d.BudgetImpact
		}
		analysis.BudgetImpact = deliberationsTotal

		co := CouncilOutput{
			CouncilID: c.CouncilID,
			Category:  c.Category,
			Date:      c.Date,
			Title:     c.Title,
			Summary:   c.Summary,
			SourceURL: c.SourceURL,
			Analysis:  analysis,
		}
		for _, d := range delibs[c.CouncilID] {
			co.Deliberations = append(co.Deliberations, DeliberationOutput{
				ID:              d.ID,
				Title:           d.Title,
				TopicTag:        d.TopicTag,
				PDFURL:          d.PDFURL,
				Summary:         d.Summary,
				IsSubstantial:   d.IsSubstantial,
				Acronyms:        d.Acronyms,
				AnalysisData:    d.AnalysisData,
				BudgetImpact:    d.BudgetImpact,
				BudgetBreakdown: d.BudgetBreakdown,
				Vote: VoteCount{
					HasVote:    d.HasVote || d.VotePour != nil || d.VoteContre != nil || d.VoteAbstention != nil,
					Pour:       d.VotePour,
					Contre:     d.VoteContre,
					Abstention: d.VoteAbstention,
				},
				Disagreements: d.Disagreements,
			})
		}
		out.Councils = append(out.Councils, co)
	}
	return out, nil
}

func fetchNextCouncilDate(ctx context.Context, ddb *dynamodb.Client) string {
	if ddb == nil {
		return ""
	}
	out, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: "metadata#next_council"},
		},
	})
	if err != nil || out.Item == nil {
		return ""
	}

	var meta struct {
		DateText string `dynamodbav:"date_text"`
	}
	if err := attributevalue.UnmarshalMap(out.Item, &meta); err != nil {
		return ""
	}
	return meta.DateText
}

// ── Lambda Handler ───────────────────────────────────────────────────────────

func HandleRequest(ctx context.Context, event PublisherEvent) error {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}
	ddb := dynamodb.NewFromConfig(cfg)

	// Build all councils for the full data.json
	allCouncils, allDelibs, err := fetchAllData(ctx, ddb)
	if err != nil {
		return fmt.Errorf("fetch all data: %w", err)
	}

	data, err := buildDataJSON(ctx, ddb, allCouncils, allDelibs)
	if err != nil {
		return err
	}

	// Upload data.json to S3
	jsonBytes, _ := json.MarshalIndent(data, "", "  ")
	s3Client := s3.NewFromConfig(cfg)
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(os.Getenv("WEBSITE_BUCKET")),
		Key:          aws.String("data.json"),
		Body:         bytes.NewReader(jsonBytes),
		ContentType:  aws.String("application/json"),
		CacheControl: aws.String("public, max-age=31536000, immutable"),
	})
	if err != nil {
		return fmt.Errorf("upload data.json: %w", err)
	}
	log.Printf("data.json uploaded (%d bytes)", len(jsonBytes))

	// CloudFront Invalidation
	distID := os.Getenv("CLOUDFRONT_DISTRIBUTION_ID")
	if distID != "" {
		cfClient := cloudfront.NewFromConfig(cfg)
		_, err := cfClient.CreateInvalidation(ctx, &cloudfront.CreateInvalidationInput{
			DistributionId: aws.String(distID),
			InvalidationBatch: &cftypes.InvalidationBatch{
				CallerReference: aws.String(fmt.Sprintf("watchdog-%d", time.Now().Unix())),
				Paths: &cftypes.Paths{
					Quantity: aws.Int32(1),
					Items:    []string{"/data.json"},
				},
			},
		})
		if err != nil {
			log.Printf("warn: could not invalidate CloudFront cache: %v", err)
		} else {
			log.Printf("CloudFront cache invalidated for /data.json")
		}
	}

	return nil
}

func fetchAllData(ctx context.Context, ddb *dynamodb.Client) ([]CouncilRecord, map[string][]DeliberationRecord, error) {
	// Scan councils (excluant metadata#next_council)
	cOut, err := ddb.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(os.Getenv("COUNCILS_TABLE")),
		FilterExpression: aws.String("NOT (council_id = :meta)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":meta": &types.AttributeValueMemberS{Value: "metadata#next_council"},
		},
	})
	if err != nil {
		return nil, nil, err
	}
	var councils []CouncilRecord
	attributevalue.UnmarshalListOfMaps(cOut.Items, &councils)

	// Scan deliberations
	dOut, err := ddb.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(os.Getenv("DELIBERATIONS_TABLE")),
	})
	if err != nil {
		return nil, nil, err
	}
	var delibs []DeliberationRecord
	attributevalue.UnmarshalListOfMaps(dOut.Items, &delibs)

	// Group by council
	delibMap := make(map[string][]DeliberationRecord)
	for _, d := range delibs {
		delibMap[d.CouncilID] = append(delibMap[d.CouncilID], d)
	}

	return councils, delibMap, nil
}
