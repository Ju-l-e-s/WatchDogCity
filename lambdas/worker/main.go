package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
)

var workerHandler *WorkerHandler

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("init: load aws config: %v", err)
	}
	workerHandler = &WorkerHandler{
		ddb:    dynamodb.NewFromConfig(cfg),
		lambda: awslambda.NewFromConfig(cfg),
	}
}

func main() {
	lambda.Start(workerHandler.HandleRequest)
}
