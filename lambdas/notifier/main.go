package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func loadAWSConfig(ctx context.Context) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

var sharedDeps *notifierDeps

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("init: load aws config: %v", err)
	}
	sharedDeps = &notifierDeps{
		ddb: dynamodb.NewFromConfig(cfg),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
		geminiKey:          os.Getenv("GEMINI_API_KEY"),
		geminiModel:        envOrDefault("GEMINI_MODEL", "gemini-2.5-pro"),
		brevoKey:           os.Getenv("BREVO_API_KEY"),
		brevoTemplateID:    envInt("BREVO_NEWSLETTER_TEMPLATE_ID", 2),
		brevoListID:        envInt("BREVO_LIST_ID", 2),
		senderEmail:        envOrDefault("SENDER_EMAIL", "noreply@lobservatoiredebegles.fr"),
		councilsTable:      os.Getenv("COUNCILS_TABLE"),
		deliberationsTable: os.Getenv("DELIBERATIONS_TABLE"),
		now:                time.Now,
	}
}

func main() {
	lambda.Start(HandleRequest)
}
