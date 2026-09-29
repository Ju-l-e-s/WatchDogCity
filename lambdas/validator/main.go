package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/watchdog/shared"
)

func main() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	handler := &ValidatorHandler{
		ddb:                dynamodb.NewFromConfig(cfg),
		lambdaClient:       awslambda.NewFromConfig(cfg),
		sqsClient:          sqs.NewFromConfig(cfg),
		councilsTable:      mustEnv("COUNCILS_TABLE"),
		deliberationsTable: mustEnv("DELIBERATIONS_TABLE"),
		publisherFnName:    mustEnv("PUBLISHER_FUNCTION_NAME"),
		notifierFnName:     mustEnv("NOTIFIER_FUNCTION_NAME"),
		sqsQueueURL:        os.Getenv("PDF_QUEUE_URL"), // optional; empty disables self-heal
		geminiDeps: shared.GeminiDeps{
			APIKey: mustEnv("GEMINI_API_KEY"),
			Model:  mustEnv("GEMINI_MODEL"),
		},
		cfg: shared.DefaultQcConfig(),
	}
	lambda.Start(handler.HandleRequest)
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s not set", key)
	}
	return v
}
