package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const (
	deliberationsListURL = "https://www.mairie-begles.fr/d%C3%A9lib%C3%A9rations/"
	nextCouncilURL       = "https://www.mairie-begles.fr/vie-municipale/le-conseil-municipal-2/les-seances-du-conseil-municipal/"
)

type OrchestratorEvent struct {
	TargetURL string `json:"target_url"`
}

type SQSMessage struct {
	CouncilID string `json:"council_id"`
	PDFTitle  string `json:"pdf_title"`
	PDFURL    string `json:"pdf_url"`
	TotalPDFs int    `json:"total_pdfs"`
}

// buildCouncilUpdateInput crafts the conditional UpdateItem that creates or
// refreshes a council record without clobbering the worker-incremented
// processed_pdfs counter or the original created_at. Extracted for tests.
//
// "date" is a DynamoDB reserved word — it must be aliased via
// ExpressionAttributeNames.
func buildCouncilUpdateInput(table string, c CouncilListing, totalPDFs, processedCount int, now time.Time) *dynamodb.UpdateItemInput {
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(table),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: c.CouncilID},
		},
		UpdateExpression: aws.String(
			"SET title = :t, summary = :s, category = :c, #date = :d, " +
				"source_url = :u, total_pdfs = :tp, " +
				"processed_pdfs = if_not_exists(processed_pdfs, :pp), " +
				"created_at = if_not_exists(created_at, :ca)",
		),
		ExpressionAttributeNames: map[string]string{
			"#date": "date",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":t":  &types.AttributeValueMemberS{Value: c.Title},
			":s":  &types.AttributeValueMemberS{Value: c.Summary},
			":c":  &types.AttributeValueMemberS{Value: c.Category},
			":d":  &types.AttributeValueMemberS{Value: c.Date},
			":u":  &types.AttributeValueMemberS{Value: c.URL},
			":tp": &types.AttributeValueMemberN{Value: strconv.Itoa(totalPDFs)},
			":pp": &types.AttributeValueMemberN{Value: strconv.Itoa(processedCount)},
			":ca": &types.AttributeValueMemberS{Value: now.Format(time.RFC3339)},
		},
	}
}

func handler(ctx context.Context, event OrchestratorEvent) error {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}

	ddb := dynamodb.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)
	scraper := NewScraper(deliberationsListURL)

	// 1. Gérer la date du prochain conseil
	nextDate, err := scraper.ScrapeNextCouncilDate(ctx, nextCouncilURL)
	if err != nil {
		log.Printf("warn: failed to scrape next council date: %v", err)
	} else {
		log.Printf("Found next council date: %s", nextDate)
		updateNextCouncilMetadata(ctx, ddb, nextDate)
	}

	// 2. Gérer la liste des délibérations
	listings, err := scraper.ScrapeCouncilList(ctx)
	if err != nil {
		return fmt.Errorf("scrape council list: %w", err)
	}
	log.Printf("found %d councils on page", len(listings))

	for _, council := range listings {
		// Vérification de changement (URL unique)
		existing, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
			Key: map[string]types.AttributeValue{
				"council_id": &types.AttributeValueMemberS{Value: council.CouncilID},
			},
		})
		if err != nil {
			return fmt.Errorf("get item %s: %w", council.CouncilID, err)
		}
		if existing.Item != nil {
			processed, okP := attrInt(existing.Item, "processed_pdfs")
			total, okT := attrInt(existing.Item, "total_pdfs")

			if !okT || !okP {
				log.Printf("warn: council %s has invalid counters, skipping", council.CouncilID)
				continue
			}

			if processed >= total && total > 0 {
				log.Printf("council %s already processed, updating summary only", council.CouncilID)
				_, _ = ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
					TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
					Key: map[string]types.AttributeValue{
						"council_id": &types.AttributeValueMemberS{Value: council.CouncilID},
					},
					UpdateExpression: aws.String("SET summary = :s"),
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":s": &types.AttributeValueMemberS{Value: council.Summary},
					},
				})
				continue
			}
			log.Printf("council %s is incomplete (%d/%d), forcing rescan", council.CouncilID, processed, total)
		}

		// Nouveau conseil détecté ! Téléchargement de tous les PDF
		pdfs, err := scraper.ScrapePDFLinks(ctx, council.URL)
		if err != nil {
			log.Printf("warn: failed to scrape PDFs for %s: %v", council.CouncilID, err)
			continue
		}
		if len(pdfs) == 0 {
			log.Printf("no PDFs found for council %s", council.CouncilID)
			continue
		}

		// Query existing processed PDFs
		processedSet := make(map[string]bool)
		qOutput, err := ddb.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(os.Getenv("DELIBERATIONS_TABLE")),
			IndexName:              aws.String("council_id-index"),
			KeyConditionExpression: aws.String("council_id = :cid"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":cid": &types.AttributeValueMemberS{Value: council.CouncilID},
			},
		})
		if err == nil {
			for _, pitem := range qOutput.Items {
				if idAttr, ok := pitem["id"].(*types.AttributeValueMemberS); ok {
					processedSet[idAttr.Value] = true
				}
			}
		}

		// Sauvegarde des métadonnées du conseil via UpdateItem :
		// un PutItem inconditionnel écrasait processed_pdfs avec un snapshot
		// (len(processedSet)) calculé en début d'orchestration, perdant tous
		// les ADD effectués par les workers en cours d'exécution. On utilise
		// if_not_exists pour préserver le compteur s'il existe déjà, et
		// pareil pour created_at.
		if len(processedSet) > len(pdfs) {
			log.Printf(
				"warn: council %s — processedSet=%d > scraped pdfs=%d (source page may have removed items)",
				council.CouncilID, len(processedSet), len(pdfs),
			)
		}

		input := buildCouncilUpdateInput(os.Getenv("COUNCILS_TABLE"), council, len(pdfs), len(processedSet), time.Now().UTC())
		if _, err := ddb.UpdateItem(ctx, input); err != nil {
			return fmt.Errorf("update council: %w", err)
		}

		queuedCount := 0
		// Envoi de chaque PDF manquant vers le Worker via SQS
		for _, pdf := range pdfs {
			if processedSet[deliberationID(pdf.URL)] {
				continue
			}

			msg := SQSMessage{
				CouncilID: council.CouncilID,
				PDFTitle:  pdf.Title,
				PDFURL:    pdf.URL,
				TotalPDFs: len(pdfs),
			}
			body, _ := json.Marshal(msg)
			_, err = sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
				QueueUrl:    aws.String(os.Getenv("PDF_QUEUE_URL")),
				MessageBody: aws.String(string(body)),
			})
			if err != nil {
				log.Printf("error sending msg to SQS: %v", err)
			} else {
				queuedCount++
			}
		}
		log.Printf("Queued %d new PDFs for council %s (already processed: %d/%d)", queuedCount, council.Title, len(processedSet), len(pdfs))
	}

	return nil
}

func updateNextCouncilMetadata(ctx context.Context, ddb *dynamodb.Client, nextDate string) {
	// Stockage dans un item spécial pour le front-end
	item, _ := attributevalue.MarshalMap(map[string]interface{}{
		"council_id": "metadata#next_council",
		"date_text":  nextDate,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
	ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Item:      item,
	})
}

func attrInt(m map[string]types.AttributeValue, key string) (int, bool) {
	if val, ok := m[key]; ok {
		if n, ok := val.(*types.AttributeValueMemberN); ok {
			var i int
			if _, err := fmt.Sscanf(n.Value, "%d", &i); err == nil {
				return i, true
			}
		}
	}
	return 0, false
}

func deliberationID(url string) string {
	parts := strings.Split(url, "/")
	return parts[len(parts)-1]
}

func main() {
	lambda.Start(handler)
}
