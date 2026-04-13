package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	lambdaService "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdaTypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"google.golang.org/genai"
)

type CouncilAnalysis struct {
	BudgetImpact int64  `dynamodbav:"budget_impact" json:"budget_impact"`
	BudgetLabel  string `dynamodbav:"budget_label" json:"budget_label"`
	VoteClimat   string `dynamodbav:"vote_climat" json:"vote_climat"`
	VoteSummary  string `dynamodbav:"vote_summary" json:"vote_summary"`
	VotesPour    int    `dynamodbav:"votes_pour" json:"votes_pour"`
	VotesContre  int    `dynamodbav:"votes_contre" json:"votes_contre"`
}

type Deliberation struct {
	ID           string `dynamodbav:"id"`
	CouncilID    string `dynamodbav:"council_id"`
	BudgetImpact int64  `dynamodbav:"budget_impact"`
	TopicTag     string `dynamodbav:"topic_tag"`
	Summary      string `dynamodbav:"summary"`
	Vote         struct {
		Pour       *int `dynamodbav:"pour"`
		Contre     *int `dynamodbav:"contre"`
		Abstention *int `dynamodbav:"abstention"`
	} `dynamodbav:"vote"`
}

func handler(ctx context.Context, event events.DynamoDBEvent) error {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ddb := dynamodb.NewFromConfig(cfg)
	lambdaClient := lambdaService.NewFromConfig(cfg)

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
	var totalBudget int64
	var totalPour, totalContre, totalAbst int
	var summaries []string
	topicBudgets := make(map[string]int64)

	for _, d := range delibs {
		totalBudget += d.BudgetImpact
		if d.TopicTag != "" {
			topicBudgets[d.TopicTag] += d.BudgetImpact
		}
		if d.Vote.Pour != nil {
			totalPour += *d.Vote.Pour
		}
		if d.Vote.Contre != nil {
			totalContre += *d.Vote.Contre
		}
		if d.Vote.Abstention != nil {
			totalAbst += *d.Vote.Abstention
		}
		if d.Summary != "" {
			summaries = append(summaries, fmt.Sprintf("- %s", d.Summary))
		}
	}

	// Déterminer le thème dominant par budget
	mainTheme := "Administration"
	var maxB int64 = -1
	for t, b := range topicBudgets {
		if b > maxB {
			maxB = b
			mainTheme = t
		}
	}

	// Climat (Tension si > 10% d'opposition)
	climat := "consensus"
	if totalPour+totalContre > 0 && float64(totalContre)/float64(totalPour+totalContre) > 0.10 {
		climat = "tensions"
	}

	// 3. Synthèse IA (Enjeu Clé)
	voteSummary, err := askGeminiForSynthesis(ctx, summaries)
	if err != nil {
		log.Printf("IA Synthesis failed, using fallback: %v", err)
		voteSummary = "Synthèse des enjeux majeurs de la séance du conseil municipal."
	}

	// 4. Mise à jour du Conseil dans DynamoDB
	analysis := CouncilAnalysis{
		BudgetImpact: totalBudget,
		BudgetLabel:  mainTheme,
		VoteClimat:   climat,
		VoteSummary:  voteSummary,
		VotesPour:    totalPour,
		VotesContre:  totalContre,
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
			":u": &types.AttributeValueMemberS{Value: "now"}, // Placeholder for simplicity
		},
	})
	if err != nil {
		return err
	}

	// 5. Déclenchement du Publisher pour mettre à jour le JSON front-end
	_, err = lambdaClient.Invoke(ctx, &lambdaService.InvokeInput{
		FunctionName:   aws.String(os.Getenv("PUBLISHER_FUNCTION_NAME")),
		InvocationType: lambdaTypes.InvocationTypeEvent,
	})

	return err
}

func askGeminiForSynthesis(ctx context.Context, summaries []string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	modelName := os.Getenv("GEMINI_MODEL")
	if modelName == "" {
		modelName = "gemini-3.1-pro-preview"
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

	resp, err := client.Models.GenerateContent(ctx, modelName, []*genai.Content{{
		Role:  "user",
		Parts: []*genai.Part{{Text: prompt}},
	}}, nil)
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
