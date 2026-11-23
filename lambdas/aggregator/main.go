package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	lambdaService "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdaTypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

var (
	ddb          *dynamodb.Client
	lambdaClient *lambdaService.Client
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("init: load aws config: %v", err)
	}
	ddb = dynamodb.NewFromConfig(cfg)
	lambdaClient = lambdaService.NewFromConfig(cfg)
}

type CouncilAnalysis struct {
	BudgetImpact int64  `dynamodbav:"budget_impact" json:"budget_impact"`
	BudgetLabel  string `dynamodbav:"budget_label" json:"budget_label"`
	VoteClimat   string `dynamodbav:"vote_climat" json:"vote_climat"`
	VoteSummary  string `dynamodbav:"vote_summary" json:"vote_summary"`
	VotesPour    int    `dynamodbav:"votes_pour" json:"votes_pour"`
	VotesContre  int    `dynamodbav:"votes_contre" json:"votes_contre"`
}

// Vote attributes are written FLAT at the top level by worker
// (lambdas/worker/handler.go), and read FLAT by publisher and notifier.
// Aggregator must match that contract — a previous nested layout under a
// "vote" map silently produced nil pointers and broke voteClimat detection.
type Deliberation struct {
	Counted      bool   `dynamodbav:"counted"`
	ID           string `dynamodbav:"id"`
	CouncilID    string `dynamodbav:"council_id"`
	BudgetImpact int64  `dynamodbav:"budget_impact"`
	TopicTag     string `dynamodbav:"topic_tag"`
	VotePour     *int   `dynamodbav:"vote_pour"`
	VoteContre   *int   `dynamodbav:"vote_contre"`
	VoteAbst     *int   `dynamodbav:"vote_abstention"`
}

func handler(ctx context.Context, event events.DynamoDBEvent) error {
	for _, record := range event.Records {
		// The Worker commits counted and the council counter in one transaction.
		counted, ok := record.Change.NewImage["counted"]
		if !ok || counted.DataType() != events.DataTypeBoolean || !counted.Boolean() {
			continue
		}
		id, ok := record.Change.NewImage["council_id"]
		if !ok || id.DataType() != events.DataTypeString {
			return fmt.Errorf("counted record missing council_id")
		}
		councilID := id.String()
		councilResp, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(os.Getenv("COUNCILS_TABLE")), ConsistentRead: aws.Bool(true),
			Key: map[string]types.AttributeValue{"council_id": &types.AttributeValueMemberS{Value: councilID}},
		})
		if err != nil {
			return err
		}
		if councilResp.Item == nil {
			return fmt.Errorf("missing council %s", councilID)
		}
		var c struct {
			Processed int    `dynamodbav:"processed_pdfs"`
			Total     int    `dynamodbav:"total_pdfs"`
			Status    string `dynamodbav:"qc_status"`
		}
		if err := attributevalue.UnmarshalMap(councilResp.Item, &c); err != nil {
			return err
		}
		if councilResp.Item["pdf_review_held"] != nil || councilResp.Item["pdf_reviews_pending"] != nil {
			continue
		}
		if c.Status == "PENDING" {
			if err := invokeValidator(ctx, lambdaClient, councilID); err != nil {
				return err
			}
			continue
		}
		if c.Status == "" && c.Total > 0 && c.Processed == c.Total {
			if err := runSynthesis(ctx, ddb, lambdaClient, councilID, c.Total); err != nil {
				log.Printf("Synthesis failed for %s: %v", councilID, err)
				return err
			}
		}
	}

	return nil
}

func runSynthesis(ctx context.Context, ddb *dynamodb.Client, lambdaClient *lambdaService.Client, councilID string, expected int) error {
	// 1. Récupérer toutes les délibérations du conseil
	pages := dynamodb.NewQueryPaginator(ddb, &dynamodb.QueryInput{
		TableName:              aws.String(os.Getenv("DELIBERATIONS_TABLE")),
		IndexName:              aws.String("council_id-index"),
		KeyConditionExpression: aws.String("council_id = :cid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":cid": &types.AttributeValueMemberS{Value: councilID},
		},
	})
	var delibs []Deliberation
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		var rows []Deliberation
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if !row.Counted {
				return fmt.Errorf("index not yet counted for %s", row.ID)
			}
		}
		delibs = append(delibs, rows...)
	}
	if len(delibs) != expected {
		return fmt.Errorf("council %s: index returned %d of %d deliberations", councilID, len(delibs), expected)
	}

	// 2. Calculs statistiques
	stats := computeStats(delibs)

	mainTheme := dominantTheme(stats.topicBudgets)
	climat := voteClimat(stats.totalPour, stats.totalContre)

	// 3. Synthesis: fully deterministic template from pre-computed stats.
	// No LLM involved — output is reproducible and cannot hallucinate political intent.
	voteSummary := buildDeterministicSummary(stats)

	// 4. Mise à jour du Conseil dans DynamoDB
	analysis := CouncilAnalysis{
		BudgetImpact: stats.totalBudget,
		BudgetLabel:  mainTheme,
		VoteClimat:   climat,
		VoteSummary:  voteSummary,
		VotesPour:    stats.totalPour,
		VotesContre:  stats.totalContre,
	}

	analysisMap, _ := attributevalue.MarshalMap(analysis)
	_, err := ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression:    aws.String("SET analysis = :a, updated_at = :u, qc_status = :pending"),
		ConditionExpression: aws.String("attribute_not_exists(qc_status) AND attribute_not_exists(pdf_review_held) AND attribute_not_exists(pdf_reviews_pending) AND processed_pdfs = total_pdfs AND total_pdfs = :total"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":total":   &types.AttributeValueMemberN{Value: fmt.Sprint(expected)},
			":a":       &types.AttributeValueMemberM{Value: analysisMap},
			":u":       &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
			":pending": &types.AttributeValueMemberS{Value: "PENDING"},
		},
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			log.Printf("council %s qc_status already set (or completed), skipping validator", councilID)
			return nil
		}
		return fmt.Errorf("update analysis and set qc_status=PENDING for %s: %w", councilID, err)
	}

	return invokeValidator(ctx, lambdaClient, councilID)
}

func invokeValidator(ctx context.Context, lambdaClient *lambdaService.Client, councilID string) error {
	payload, err := buildValidatorPayload(councilID)
	if err != nil {
		return fmt.Errorf("marshal validator payload: %w", err)
	}
	_, err = lambdaClient.Invoke(ctx, &lambdaService.InvokeInput{
		FunctionName:   aws.String(os.Getenv("VALIDATOR_FUNCTION_NAME")),
		InvocationType: lambdaTypes.InvocationTypeEvent,
		Payload:        payload,
	})
	return err
}

// ── Pure calculation functions (extracted for testability) ───────────────────

type councilStats struct {
	totalBudget  int64
	topicBudgets map[string]int64
	totalPour    int
	totalContre  int
	totalAbst    int
}

func computeStats(delibs []Deliberation) councilStats {
	s := councilStats{topicBudgets: make(map[string]int64)}
	var hasBudgetTopic bool
	var maxBudgetTopic int64
	var otherTopicsSum int64

	for _, d := range delibs {
		if d.TopicTag == "Budget" {
			hasBudgetTopic = true
			if d.BudgetImpact > maxBudgetTopic {
				maxBudgetTopic = d.BudgetImpact
			}
		} else {
			otherTopicsSum += d.BudgetImpact
		}

		if d.TopicTag != "" {
			s.topicBudgets[d.TopicTag] += d.BudgetImpact
		}
		if d.VotePour != nil {
			s.totalPour += *d.VotePour
		}
		if d.VoteContre != nil {
			s.totalContre += *d.VoteContre
		}
		if d.VoteAbst != nil {
			s.totalAbst += *d.VoteAbst
		}
	}

	if hasBudgetTopic {
		s.totalBudget = maxBudgetTopic
		s.topicBudgets["Budget"] = maxBudgetTopic
	} else {
		s.totalBudget = otherTopicsSum
	}
	return s
}

// dominantTheme returns the topic with the highest total budget.
// Falls back to "Administration" when there are no budget amounts.
func dominantTheme(topicBudgets map[string]int64) string {
	mainTheme := "Administration"
	var maxB int64 = -1
	for t, b := range topicBudgets {
		if b > maxB {
			maxB = b
			mainTheme = t
		}
	}
	return mainTheme
}

// buildValidatorPayload returns the JSON payload for invoking the Validator Lambda.
func buildValidatorPayload(councilID string) ([]byte, error) {
	return json.Marshal(map[string]string{"council_id": councilID})
}

// voteClimat returns "tensions" when opposition exceeds 10% of votes cast, else "consensus".
func voteClimat(totalPour, totalContre int) string {
	if totalPour+totalContre > 0 && float64(totalContre)/float64(totalPour+totalContre) > 0.10 {
		return "tensions"
	}
	return "consensus"
}

// tagBudget pairs a topic tag with its total budget for ranking.
type tagBudget struct {
	tag    string
	budget int64
}

// topBudgetTopics returns the top n topics ordered by budget (budget > 0 only).
// Uses a simple selection pass to avoid importing sort.
func topBudgetTopics(topicBudgets map[string]int64, n int) []tagBudget {
	var result []tagBudget
	selected := make(map[string]bool)
	for i := 0; i < n; i++ {
		var best tagBudget
		for tag, b := range topicBudgets {
			if !selected[tag] && b > best.budget {
				best = tagBudget{tag, b}
			}
		}
		if best.budget > 0 {
			result = append(result, best)
			selected[best.tag] = true
		}
	}
	return result
}

// buildDeterministicSummary constructs a factual, template-based council summary
// from pre-computed statistics. No LLM involved; output is fully deterministic.
func buildDeterministicSummary(stats councilStats) string {
	top := topBudgetTopics(stats.topicBudgets, 2)

	var summary string
	switch len(top) {
	case 0:
		summary = "Séance sans impact budgétaire significatif."
	case 1:
		summary = fmt.Sprintf("La séance a concentré le budget sur la thématique %s (%d €).", top[0].tag, top[0].budget)
	default:
		summary = fmt.Sprintf("La séance a concentré le budget sur les thématiques %s (%d €) et %s (%d €).",
			top[0].tag, top[0].budget, top[1].tag, top[1].budget)
	}

	if stats.totalContre > 0 {
		suffix := ""
		if stats.totalContre > 1 {
			suffix = "s"
		}
		summary += fmt.Sprintf(" %d voix contre enregistrée%s.", stats.totalContre, suffix)
	}

	return summary
}

func main() {
	lambda.Start(handler)
}
