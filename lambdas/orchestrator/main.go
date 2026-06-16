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
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/watchdog/shared"
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

// ddbClient is satisfied by *dynamodb.Client and test mocks.
type ddbClient interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// sqsClientAPI is satisfied by *sqs.Client and test mocks.
type sqsClientAPI interface {
	SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
}

// lambdaClientAPI is satisfied by *lambda.Client and test mocks.
type lambdaClientAPI interface {
	Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// scraperAPI is satisfied by *Scraper and test mocks.
type scraperAPI interface {
	ScrapeCouncilList(ctx context.Context) ([]CouncilListing, error)
	ScrapePDFLinks(ctx context.Context, url string) ([]PDFItem, error)
	ScrapeNextCouncilDate(ctx context.Context, url string) (string, error)
}

type orchestrator struct {
	ddb                ddbClient
	sqs                sqsClientAPI
	lambda             lambdaClientAPI
	scraper            scraperAPI
	councilsTable      string
	deliberationsTable string
	queueURL           string
	publisherFnName    string
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

func (o *orchestrator) getCouncilWithRetry(ctx context.Context, councilID string) (*dynamodb.GetItemOutput, error) {
	input := &dynamodb.GetItemInput{
		TableName: aws.String(o.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
	}
	out, err := o.ddb.GetItem(ctx, input)
	if err == nil {
		return out, nil
	}
	time.Sleep(500 * time.Millisecond)
	return o.ddb.GetItem(ctx, input)
}

func (o *orchestrator) handle(ctx context.Context, event OrchestratorEvent) error {
	// 1. Gérer la date du prochain conseil
	nextDate, err := o.scraper.ScrapeNextCouncilDate(ctx, nextCouncilURL)
	if err != nil {
		log.Printf("warn: failed to scrape next council date: %v", err)
	} else {
		log.Printf("Found next council date: %s", nextDate)
		oldDate, err := o.getNextCouncilMetadata(ctx)
		if err != nil {
			log.Printf("warn: failed to get old next council date from DB: %v", err)
		}
		if nextDate != oldDate {
			log.Printf("Next council date changed from %q to %q — updating DB and invoking publisher", oldDate, nextDate)
			o.updateNextCouncilMetadata(ctx, nextDate)
			o.invokePublisher(ctx)
		} else {
			log.Printf("Next council date unchanged (%q) — no-op", nextDate)
		}
	}

	// 2. Gérer la liste des délibérations
	listings, err := o.scraper.ScrapeCouncilList(ctx)
	if err != nil {
		return fmt.Errorf("scrape council list: %w", err)
	}
	log.Printf("found %d councils on page", len(listings))

	var errs []error
	for _, council := range listings {
		existing, err := o.getCouncilWithRetry(ctx, council.CouncilID)
		if err != nil {
			log.Printf("error processing council %s: %v", council.CouncilID, err)
			errs = append(errs, fmt.Errorf("council %s: %w", council.CouncilID, err))
			continue
		}
		if existing.Item != nil {
			processed, okP := attrInt(existing.Item, "processed_pdfs")
			total, okT := attrInt(existing.Item, "total_pdfs")

			if !okT || !okP {
				log.Printf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":"Watchdog","Dimensions":[["FunctionName"]],"Metrics":[{"Name":"CouncilCountersInvalid","Unit":"Count"}]}]},"FunctionName":"orchestrator","CouncilCountersInvalid":1,"CouncilID":"%s"}`,
					time.Now().UnixMilli(), council.CouncilID)
				continue
			}

			if processed >= total && total > 0 {
				log.Printf("council %s already processed, updating summary only", council.CouncilID)
				_, _ = o.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
					TableName: aws.String(o.councilsTable),
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

		pdfs, err := o.scraper.ScrapePDFLinks(ctx, council.URL)
		if err != nil {
			log.Printf("warn: failed to scrape PDFs for %s: %v", council.CouncilID, err)
			continue
		}
		if len(pdfs) == 0 {
			log.Printf("no PDFs found for council %s", council.CouncilID)
			continue
		}

		processedSet := make(map[string]bool)
		qItems, err := shared.PaginateQuery(ctx, o.ddb, &dynamodb.QueryInput{
			TableName:              aws.String(o.deliberationsTable),
			IndexName:              aws.String("council_id-index"),
			KeyConditionExpression: aws.String("council_id = :cid"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":cid": &types.AttributeValueMemberS{Value: council.CouncilID},
			},
		})
		if err == nil {
			for _, pitem := range qItems {
				if idAttr, ok := pitem["id"].(*types.AttributeValueMemberS); ok {
					processedSet[idAttr.Value] = true
				}
			}
		}

		if len(processedSet) > len(pdfs) {
			log.Printf(
				"warn: council %s — processedSet=%d > scraped pdfs=%d (source page may have removed items)",
				council.CouncilID, len(processedSet), len(pdfs),
			)
		}

		input := buildCouncilUpdateInput(o.councilsTable, council, len(pdfs), len(processedSet), time.Now().UTC())
		if _, err := o.ddb.UpdateItem(ctx, input); err != nil {
			log.Printf("error processing council %s: %v", council.CouncilID, err)
			errs = append(errs, fmt.Errorf("council %s: %w", council.CouncilID, err))
			continue
		}

		type pendingMsg struct {
			id   string
			body string
		}
		var pending []pendingMsg
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
			pending = append(pending, pendingMsg{
				id:   deliberationID(pdf.URL),
				body: string(body),
			})
		}

		queuedCount := 0
		for i := 0; i < len(pending); i += 10 {
			end := i + 10
			if end > len(pending) {
				end = len(pending)
			}
			entries := make([]sqstypes.SendMessageBatchRequestEntry, 0, end-i)
			for j, m := range pending[i:end] {
				entries = append(entries, sqstypes.SendMessageBatchRequestEntry{
					Id:          aws.String(fmt.Sprintf("%d", i+j)),
					MessageBody: aws.String(m.body),
				})
			}
			out, err := o.sqs.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
				QueueUrl: aws.String(o.queueURL),
				Entries:  entries,
			})
			if err != nil {
				log.Printf("error sending SQS batch (council %s): %v", council.CouncilID, err)
				continue
			}
			queuedCount += len(out.Successful)
			for _, f := range out.Failed {
				log.Printf("warn: SQS batch entry %s failed: code=%s msg=%s", aws.ToString(f.Id), aws.ToString(f.Code), aws.ToString(f.Message))
			}
		}
		log.Printf("Queued %d new PDFs for council %s (already processed: %d/%d)", queuedCount, council.Title, len(processedSet), len(pdfs))
	}

	if len(errs) > 0 {
		log.Printf("orchestrator finished with %d council errors (continued best-effort):", len(errs))
		for _, e := range errs {
			log.Printf("  - %v", e)
		}
	}
	return nil
}

func (o *orchestrator) getNextCouncilMetadata(ctx context.Context) (string, error) {
	out, err := o.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(o.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: "metadata#next_council"},
		},
	})
	if err != nil {
		return "", err
	}
	if out.Item == nil {
		return "", nil
	}
	var meta struct {
		DateText string `dynamodbav:"date_text"`
	}
	if err := attributevalue.UnmarshalMap(out.Item, &meta); err != nil {
		return "", err
	}
	return meta.DateText, nil
}

func (o *orchestrator) invokePublisher(ctx context.Context) {
	if o.lambda == nil || o.publisherFnName == "" {
		log.Printf("warn: lambda client or publisherFnName not set, skipping invocation")
		return
	}
	payload, _ := json.Marshal(map[string]string{"council_id": "metadata#next_council"})
	_, err := o.lambda.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(o.publisherFnName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payload,
	})
	if err != nil {
		log.Printf("error invoking publisher for next council date update: %v", err)
	} else {
		log.Printf("successfully invoked publisher to regenerate data.json")
	}
}

func (o *orchestrator) updateNextCouncilMetadata(ctx context.Context, nextDate string) {
	item, _ := attributevalue.MarshalMap(map[string]interface{}{
		"council_id": "metadata#next_council",
		"date_text":  nextDate,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
	o.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(o.councilsTable),
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

var orch *orchestrator

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	orch = &orchestrator{
		ddb:                dynamodb.NewFromConfig(cfg),
		sqs:                sqs.NewFromConfig(cfg),
		lambda:             awslambda.NewFromConfig(cfg),
		scraper:            NewScraper(deliberationsListURL),
		councilsTable:      os.Getenv("COUNCILS_TABLE"),
		deliberationsTable: os.Getenv("DELIBERATIONS_TABLE"),
		queueURL:           os.Getenv("PDF_QUEUE_URL"),
		publisherFnName:    os.Getenv("PUBLISHER_FUNCTION_NAME"),
	}
}

func main() {
	lambda.Start(orch.handle)
}
