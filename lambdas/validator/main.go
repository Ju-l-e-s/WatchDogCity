package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/watchdog/shared"
)

var handler *ValidatorHandler

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("init: load aws config: %v", err)
	}
	handler = &ValidatorHandler{
		ddb:                dynamodb.NewFromConfig(cfg),
		lambdaClient:       awslambda.NewFromConfig(cfg),
		councilsTable:      mustEnv("COUNCILS_TABLE"),
		deliberationsTable: mustEnv("DELIBERATIONS_TABLE"),
		notifierFnName:     mustEnv("NOTIFIER_FUNCTION_NAME"),
		geminiDeps: shared.GeminiDeps{
			APIKey: mustEnv("GEMINI_API_KEY"),
			Model:  mustEnv("GEMINI_MODEL"),
		},
		cfg: shared.DefaultQcConfig(),
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("init: required env var %s not set", key)
	}
	return v
}

func main() {
	lambda.Start(handler.HandleRequest)
}
