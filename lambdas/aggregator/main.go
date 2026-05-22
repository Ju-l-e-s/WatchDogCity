package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
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
	"github.com/watchdog/shared"
	"google.golang.org/genai"
)

func ptrInt32(i int32) *int32 { return &i }

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
	ID           string `dynamodbav:"id"`
	CouncilID    string `dynamodbav:"council_id"`
	BudgetImpact int64  `dynamodbav:"budget_impact"`
	TopicTag     string `dynamodbav:"topic_tag"`
	Summary      string `dynamodbav:"summary"`
	VotePour     *int   `dynamodbav:"vote_pour"`
	VoteContre   *int   `dynamodbav:"vote_contre"`
	VoteAbst     *int   `dynamodbav:"vote_abstention"`
}

func handler(ctx context.Context, event events.DynamoDBEvent) error {
	for _, record := range event.Records {
		// On ne traite que les nouvelles délibérations
		if record.EventName != "INSERT" {
			continue
		}

		councilIDAttr := record.Change.NewImage["council_id"]
		councilID := councilIDAttr.String()
		if councilID == "" {
			continue
		}

		// 1. Récupérer le nombre total attendu (total_pdfs)
		councilResp, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
			Key: map[string]types.AttributeValue{
				"council_id": &types.AttributeValueMemberS{Value: councilID},
			},
		})
		if err != nil || councilResp.Item == nil {
			log.Printf("council %s not found: %v", councilID, err)
			continue
		}

		var totalExpected int
		if val, ok := councilResp.Item["total_pdfs"].(*types.AttributeValueMemberN); ok {
			fmt.Sscanf(val.Value, "%d", &totalExpected)
		}

		// 2. Compter les délibérations actuelles pour ce conseil
		queryOutput, err := ddb.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(os.Getenv("DELIBERATIONS_TABLE")),
			IndexName:              aws.String("council_id-index"),
			KeyConditionExpression: aws.String("council_id = :cid"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":cid": &types.AttributeValueMemberS{Value: councilID},
			},
			Select: types.SelectCount,
		})
		if err != nil {
			log.Printf("error counting deliberations for %s: %v", councilID, err)
			continue
		}

		count := int(queryOutput.Count)
		log.Printf("Council %s progress: %d/%d", councilID, count, totalExpected)

		// 3. Déclenchement de l'agrégation si complet
		if count >= totalExpected && totalExpected > 0 {
			log.Printf("🎯 All deliberations received for council %s. Starting synthesis...", councilID)
			if err := runSynthesis(ctx, ddb, lambdaClient, councilID); err != nil {
				log.Printf("Synthesis failed for %s: %v", councilID, err)
				return err
			}
		}
	}

	return nil
}

func runSynthesis(ctx context.Context, ddb *dynamodb.Client, lambdaClient *lambdaService.Client, councilID string) error {
	// 1. Récupérer toutes les délibérations du conseil
	queryOutput, err := ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(os.Getenv("DELIBERATIONS_TABLE")),
		IndexName:              aws.String("council_id-index"),
		KeyConditionExpression: aws.String("council_id = :cid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":cid": &types.AttributeValueMemberS{Value: councilID},
		},
	})
	if err != nil {
		return err
	}

	var delibs []Deliberation
	if err := attributevalue.UnmarshalListOfMaps(queryOutput.Items, &delibs); err != nil {
		return err
	}

	// 2. Calculs statistiques
	stats := computeStats(delibs)

	mainTheme := dominantTheme(stats.topicBudgets)
	climat := voteClimat(stats.totalPour, stats.totalContre)

	// 3. Synthèse IA (Enjeu Clé) — short-circuited when Gemini is down for long.
	councilsTable := os.Getenv("COUNCILS_TABLE")
	const synthesisFallback = "Synthèse des enjeux majeurs de la séance du conseil municipal."
	var voteSummary string
	if open, cerr := shared.GeminiCircuitOpen(ctx, ddb, councilsTable); cerr == nil && open {
		log.Printf("gemini circuit OPEN — skipping synthesis for %s, using fallback", councilID)
		voteSummary = synthesisFallback
	} else {
		voteSummary, err = askGeminiForSynthesis(ctx, stats.summaries)
		if err != nil {
			log.Printf("IA Synthesis failed, using fallback: %v", err)
			voteSummary = synthesisFallback
			if rerr := shared.RecordGeminiError(ctx, ddb, councilsTable); rerr != nil {
				log.Printf("warn: record gemini error: %v", rerr)
			}
		} else if rerr := shared.RecordGeminiSuccess(ctx, ddb, councilsTable); rerr != nil {
			log.Printf("warn: record gemini success: %v", rerr)
		}
	}

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
	_, err = ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression: aws.String("SET analysis = :a, updated_at = :u"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":a": &types.AttributeValueMemberM{Value: analysisMap},
			":u": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		return err
	}

	// 5. Route through QC gateway: set qc_status=PENDING (idempotent gate) and
	//    invoke Validator. Validator runs the deterministic gate; on APPROVED it
	//    invokes Publisher (website JSON) and Notifier (newsletter).
	_, qerr := ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression:    aws.String("SET qc_status = :pending"),
		ConditionExpression: aws.String("attribute_not_exists(qc_status)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending": &types.AttributeValueMemberS{Value: "PENDING"},
		},
	})
	if qerr != nil {
		var ccfe *types.ConditionalCheckFailedException
		if !errors.As(qerr, &ccfe) {
			return fmt.Errorf("set qc_status=PENDING for %s: %w", councilID, qerr)
		}
		log.Printf("council %s qc_status already set, skipping validator", councilID)
		return nil
	}

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
	summaries    []string
}

func computeStats(delibs []Deliberation) councilStats {
	s := councilStats{topicBudgets: make(map[string]int64)}
	for _, d := range delibs {
		s.totalBudget += d.BudgetImpact
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
		if d.Summary != "" {
			s.summaries = append(s.summaries, fmt.Sprintf("- %s", d.Summary))
		}
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

func askGeminiForSynthesis(ctx context.Context, summaries []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	modelName := os.Getenv("GEMINI_MODEL")
	if modelName == "" {
		modelName = "gemini-2.5-pro"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      apiKey,
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1"},
	})
	if err != nil {
		return "", err
	}

	prompt := fmt.Sprintf(`Voici les résumés des délibérations d'un conseil municipal :
%s

Rédige une synthèse de 1 à 2 phrases complètes (entre 25 et 45 mots) identifiant l'enjeu politique ou social majeur de cette séance.
Ne commence pas par "Enjeu Clé :". Ne sois pas trop court. Sois précis sur l'impact citoyen.`, strings.Join(summaries, "\n"))

	resp, err := shared.CallGeminiWithRetry(ctx, func(ctx context.Context) (*genai.GenerateContentResponse, error) {
		return client.Models.GenerateContent(ctx, modelName, []*genai.Content{{
			Role:  "user",
			Parts: []*genai.Part{{Text: prompt}},
		}}, &genai.GenerateContentConfig{
			MaxOutputTokens: 8192,
		})
	}, 4)
	if err != nil {
		return "", err
	}

	if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
		return strings.TrimSpace(resp.Candidates[0].Content.Parts[0].Text), nil
	}

	return "", fmt.Errorf("no response from gemini")
}

func main() {
	lambda.Start(handler)
}
