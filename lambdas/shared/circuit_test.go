package shared

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fakeCircuitDDB emulates the DynamoDB-side conditional semantics the breaker
// relies on: an increment-with-if_not_exists update, a guarded open update, and
// a guarded reset. State is held in memory so tests exercise the real
// orchestration without a live table.
type fakeCircuitDDB struct {
	errorCount int64
	windowEnd  int64
	openUntil  *int64
}

func (f *fakeCircuitDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	item := map[string]types.AttributeValue{
		"council_id":  &types.AttributeValueMemberS{Value: circuitItemKey},
		"error_count": &types.AttributeValueMemberN{Value: strconv.FormatInt(f.errorCount, 10)},
	}
	if f.windowEnd != 0 {
		item["window_end"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(f.windowEnd, 10)}
	}
	if f.openUntil != nil {
		item["open_until"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(*f.openUntil, 10)}
	}
	return &dynamodb.GetItemOutput{Item: item}, nil
}

func (f *fakeCircuitDDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	expr := aws.ToString(in.UpdateExpression)
	switch {
	case strings.HasPrefix(expr, "SET error_count"):
		f.errorCount++
		if f.windowEnd == 0 {
			if v, ok := in.ExpressionAttributeValues[":wend"].(*types.AttributeValueMemberN); ok {
				f.windowEnd, _ = strconv.ParseInt(v.Value, 10, 64)
			}
		}
		return &dynamodb.UpdateItemOutput{}, nil
	case strings.HasPrefix(expr, "SET open_until"):
		thresh, _ := strconv.ParseInt(in.ExpressionAttributeValues[":thresh"].(*types.AttributeValueMemberN).Value, 10, 64)
		if f.errorCount >= thresh && f.openUntil == nil {
			ou, _ := strconv.ParseInt(in.ExpressionAttributeValues[":ou"].(*types.AttributeValueMemberN).Value, 10, 64)
			f.openUntil = &ou
			return &dynamodb.UpdateItemOutput{}, nil
		}
		return nil, &types.ConditionalCheckFailedException{}
	case strings.HasPrefix(expr, "REMOVE"):
		now, _ := strconv.ParseInt(in.ExpressionAttributeValues[":now"].(*types.AttributeValueMemberN).Value, 10, 64)
		if f.windowEnd != 0 && f.windowEnd < now {
			f.errorCount, f.windowEnd, f.openUntil = 0, 0, nil
			return &dynamodb.UpdateItemOutput{}, nil
		}
		return nil, &types.ConditionalCheckFailedException{}
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

// TestGeminiCircuit_OpensAfterThreshold drives errors up to and past the
// threshold and asserts the breaker stays closed below it and trips at it.
func TestGeminiCircuit_OpensAfterThreshold(t *testing.T) {
	f := &fakeCircuitDDB{}
	ctx := context.Background()

	for i := 0; i < 9; i++ {
		if err := RecordGeminiError(ctx, f, "councils"); err != nil {
			t.Fatalf("record error %d: %v", i, err)
		}
	}
	if open, _ := GeminiCircuitOpen(ctx, f, "councils"); open {
		t.Fatalf("circuit should be closed at 9 errors")
	}

	for i := 9; i < 11; i++ {
		if err := RecordGeminiError(ctx, f, "councils"); err != nil {
			t.Fatalf("record error %d: %v", i, err)
		}
	}
	open, err := GeminiCircuitOpen(ctx, f, "councils")
	if err != nil {
		t.Fatalf("circuit open check: %v", err)
	}
	if !open {
		t.Fatalf("circuit should be open after 11 errors (count=%d)", f.errorCount)
	}
}

// TestGeminiCircuit_SuccessResetsExpiredWindow verifies a success after the
// window has elapsed wipes the counter and re-closes the breaker.
func TestGeminiCircuit_SuccessResetsExpiredWindow(t *testing.T) {
	openUntil := int64(1)
	f := &fakeCircuitDDB{errorCount: 11, windowEnd: 1, openUntil: &openUntil} // window_end far in the past
	ctx := context.Background()

	if err := RecordGeminiSuccess(ctx, f, "councils"); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if f.errorCount != 0 || f.openUntil != nil {
		t.Fatalf("success should clear state, got count=%d openUntil=%v", f.errorCount, f.openUntil)
	}
	if open, _ := GeminiCircuitOpen(ctx, f, "councils"); open {
		t.Fatalf("circuit should be closed after reset")
	}
}
