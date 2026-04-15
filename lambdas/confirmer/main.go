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
	listID, _ := strconv.Atoi(os.Getenv("BREVO_LIST_ID"))
	redirectionURL := os.Getenv("REDIRECTION_URL")
	if redirectionURL == "" {
		redirectionURL = "https://lobservatoiredebegles.fr/merci.html"
	}

	d := &confirmerDeps{
		ddb:            dynamodb.NewFromConfig(cfg),
		httpClient:     http.DefaultClient,
		tableName:      os.Getenv("TABLE_NAME"),
		brevoURL:       "https://api.brevo.com/v3/contacts",
		apiKey:         os.Getenv("MAIL_API_KEY"),
		brevoListID:    listID,
		redirectionURL: redirectionURL,
	}

	lambda.Start(d.handler)
}
