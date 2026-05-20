package shared

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// circuitItemKey is the councils-table partition key under which the Gemini
// circuit state lives (same metadata-row pattern as metadata#next_council).
const circuitItemKey = "metadata#gemini_circuit"

const (
	errorThreshold = 10
	windowDuration = 5 * time.Minute
	openDuration   = 10 * time.Minute
)

// CircuitAPI is the slice of DynamoDB used by the breaker. *dynamodb.Client and
// the lambda-local dynamoQuerier interfaces both satisfy it.
type CircuitAPI interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

func circuitKey() map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"council_id": &types.AttributeValueMemberS{Value: circuitItemKey},
	}
}

// GeminiCircuitOpen reports whether Gemini should be short-circuited right now.
// A missing item or read error is treated as closed (fail-open) so circuit
// bookkeeping never blocks the pipeline on its own.
func GeminiCircuitOpen(ctx context.Context, api CircuitAPI, table string) (bool, error) {
	out, err := api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key:       circuitKey(),
	})
	if err != nil || out.Item == nil {
		return false, err
	}
	openUntilAttr, ok := out.Item["open_until"].(*types.AttributeValueMemberN)
	if !ok {
		return false, nil
	}
	openUntil, _ := strconv.ParseInt(openUntilAttr.Value, 10, 64)
	return time.Now().Unix() < openUntil, nil
}

// RecordGeminiError increments the failure counter and opens the circuit once
// the threshold is reached within the current window. ConditionalCheckFailed on
// the open step is expected (threshold not yet hit, or circuit already open) and
// swallowed; other errors propagate so callers can log them.
func RecordGeminiError(ctx context.Context, api CircuitAPI, table string) error {
	now := time.Now().Unix()
	windowEnd := now + int64(windowDuration.Seconds())
	openUntil := now + int64(openDuration.Seconds())

	if _, err := api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        aws.String(table),
		Key:              circuitKey(),
		UpdateExpression: aws.String("SET error_count = if_not_exists(error_count, :zero) + :one, window_end = if_not_exists(window_end, :wend)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":zero": &types.AttributeValueMemberN{Value: "0"},
			":one":  &types.AttributeValueMemberN{Value: "1"},
			":wend": &types.AttributeValueMemberN{Value: strconv.FormatInt(windowEnd, 10)},
		},
	}); err != nil {
		return err
	}

	_, err := api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(table),
		Key:                 circuitKey(),
		UpdateExpression:    aws.String("SET open_until = :ou"),
		ConditionExpression: aws.String("error_count >= :thresh AND attribute_not_exists(open_until)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":ou":     &types.AttributeValueMemberN{Value: strconv.FormatInt(openUntil, 10)},
			":thresh": &types.AttributeValueMemberN{Value: strconv.Itoa(errorThreshold)},
		},
	})
	if err != nil && !isConditionalCheckFailed(err) {
		return err
	}
	return nil
}

// RecordGeminiSuccess clears the circuit state once the current window has
// elapsed, so a healthy Gemini fully resets the breaker. The conditional guards
// against wiping a still-active window; ConditionalCheckFailed is expected and
// swallowed.
func RecordGeminiSuccess(ctx context.Context, api CircuitAPI, table string) error {
	now := time.Now().Unix()
	_, err := api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(table),
		Key:                 circuitKey(),
		UpdateExpression:    aws.String("REMOVE error_count, window_end, open_until"),
		ConditionExpression: aws.String("attribute_exists(window_end) AND window_end < :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now": &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
		},
	})
	if err != nil && !isConditionalCheckFailed(err) {
		return err
	}
	return nil
}

func isConditionalCheckFailed(err error) bool {
	var ccfe *types.ConditionalCheckFailedException
	return errors.As(err, &ccfe)
}
