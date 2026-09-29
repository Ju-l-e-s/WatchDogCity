package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func main() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	listID, err := strconv.Atoi(os.Getenv("BREVO_LIST_ID"))
	if err != nil || listID <= 0 {
		log.Fatal("BREVO_LIST_ID must be a positive integer")
	}
	h := &webhookHandler{
		ddb:       dynamodb.NewFromConfig(cfg),
		http:      &http.Client{Timeout: 10 * time.Second},
		table:     os.Getenv("COUNCILS_TABLE"),
		secret:    os.Getenv("WEBHOOK_SECRET"),
		brevoKey:  os.Getenv("BREVO_API_KEY"),
		prodList:  listID,
		now:       time.Now,
		brevoBase: "https://api.brevo.com/v3",
	}
	lambda.Start(h.handle)
}
