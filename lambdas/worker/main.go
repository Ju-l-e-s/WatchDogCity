package main

import (
	"context"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
)

func main() {
	cfg, _ := config.LoadDefaultConfig(context.TODO())
	h := &WorkerHandler{
		ddb:    dynamodb.NewFromConfig(cfg),
		lambda: awslambda.NewFromConfig(cfg),
	}
	lambda.Start(h.HandleRequest)
}
