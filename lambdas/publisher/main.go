package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	lambdaSvc "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var (
	publisherCfg aws.Config
	ddb          *dynamodb.Client
	s3Client     *s3.Client
	lambdaClient *lambdaSvc.Client
)

func init() {
	var err error
	publisherCfg, err = config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("init: load aws config: %v", err)
	}
	ddb = dynamodb.NewFromConfig(publisherCfg)
	s3Client = s3.NewFromConfig(publisherCfg)
	lambdaClient = lambdaSvc.NewFromConfig(publisherCfg)
}

func main() {
	lambda.Start(HandleRequest)
}
