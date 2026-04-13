package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type SQSMessage struct {
	CouncilID string `json:"council_id"`
	PDFTitle  string `json:"pdf_title"`
	PDFURL    string `json:"pdf_url"`
	TotalPDFs int    `json:"total_pdfs"`
}

type CouncilInfo struct {
	CouncilId string `dynamodbav:"council_id"`
	Date      string `dynamodbav:"date"`
	TotalPdfs int    `dynamodbav:"total_pdfs"`
}

type DeliberationInfo struct {
	CouncilId string `dynamodbav:"council_id"`
	Title     string `dynamodbav:"title"`
	PdfUrl    string `dynamodbav:"pdf_url"`
}

func main() {
	councilsTable := os.Getenv("COUNCILS_TABLE")
	delibTable := os.Getenv("DELIBERATIONS_TABLE")
	queueUrl := os.Getenv("PDF_QUEUE_URL")

	if councilsTable == "" || delibTable == "" || queueUrl == "" {
		log.Fatal("❌ COUNCILS_TABLE, DELIBERATIONS_TABLE et PDF_QUEUE_URL doivent être définis")
	}

	ctx := context.TODO()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("❌ Impossible de charger la config AWS: %v", err)
	}

	dbClient := dynamodb.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)

	fmt.Printf("🔍 Scanning table %s for 2026 councils...\n", councilsTable)

	// 1. Collecter les infos des conseils 2026
	councilsMap := make(map[string]CouncilInfo)
	councilInput := &dynamodb.ScanInput{
		TableName:        aws.String(councilsTable),
		FilterExpression: aws.String("begins_with(#d, :y)"),
		ExpressionAttributeNames: map[string]string{
			"#d": "date",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":y": &types.AttributeValueMemberS{Value: "2026"},
		},
	}

	councilPaginator := dynamodb.NewScanPaginator(dbClient, councilInput)
	for councilPaginator.HasMorePages() {
		page, err := councilPaginator.NextPage(ctx)
		if err != nil {
			log.Fatalf("❌ Erreur scan conseils: %v", err)
		}
		var councils []CouncilInfo
		err = attributevalue.UnmarshalListOfMaps(page.Items, &councils)
		if err != nil {
			log.Fatalf("❌ Erreur unmarshal conseils: %v", err)
		}
		for _, c := range councils {
			councilsMap[c.CouncilId] = c
			fmt.Printf("  ✅ Trouvé: %s (%s, %d PDFs)\n", c.CouncilId, c.Date, c.TotalPdfs)
		}
	}

	if len(councilsMap) == 0 {
		fmt.Println("⚠️ Aucun conseil trouvé pour 2026.")
		return
	}

	// 2. Scanner les délibérations et envoyer vers SQS si elles appartiennent à un conseil 2026
	fmt.Printf("\n🔍 Scanning table %s for deliberations...\n", delibTable)
	delibInput := &dynamodb.ScanInput{
		TableName: aws.String(delibTable),
	}

	totalSent := 0
	delibPaginator := dynamodb.NewScanPaginator(dbClient, delibInput)
	for delibPaginator.HasMorePages() {
		page, err := delibPaginator.NextPage(ctx)
		if err != nil {
			log.Fatalf("❌ Erreur scan délibérations: %v", err)
		}
		var delibs []DeliberationInfo
		err = attributevalue.UnmarshalListOfMaps(page.Items, &delibs)
		if err != nil {
			log.Fatalf("❌ Erreur unmarshal délibérations: %v", err)
		}

		for _, delib := range delibs {
			council, exists := councilsMap[delib.CouncilId]
			if !exists {
				continue
			}

			if delib.PdfUrl == "" {
				continue
			}

			msg := SQSMessage{
				CouncilID: delib.CouncilId,
				PDFTitle:  delib.Title,
				PDFURL:    delib.PdfUrl,
				TotalPDFs: council.TotalPdfs,
			}

			body, _ := json.Marshal(msg)

			_, err := sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
				QueueUrl:    aws.String(queueUrl),
				MessageBody: aws.String(string(body)),
			})

			if err != nil {
				fmt.Printf("  ⚠️ Erreur envoi SQS pour %s: %v\n", delib.PdfUrl, err)
			} else {
				totalSent++
				fmt.Printf("  🚀 [%s] Envoyé: %s\n", council.Date, delib.PdfUrl)
			}
		}
	}

	fmt.Printf("\n✨ Terminé ! %d délibérations envoyées dans la file pour re-traitement.\n", totalSent)
}
