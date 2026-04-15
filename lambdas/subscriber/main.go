package main

import (
	"context"
	"net/http"
	"os"
	"strconv"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func main() {
	cfg, _ := config.LoadDefaultConfig(context.Background())
	templateID, _ := strconv.Atoi(os.Getenv("BREVO_TEMPLATE_ID"))

	d := &subscriberDeps{
		ddb:        dynamodb.NewFromConfig(cfg),
		httpClient: http.DefaultClient,
		tableName:  os.Getenv("TABLE_NAME"),
		brevoURL:   "https://api.brevo.com/v3/smtp/email",
		apiKey:     os.Getenv("MAIL_API_KEY"),
		templateID: templateID,
		apiURL:     os.Getenv("API_URL"),
	}

	lambda.Start(d.handler)
}
