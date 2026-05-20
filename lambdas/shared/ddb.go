package shared

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type DDBQueryAPI interface {
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type DDBScanAPI interface {
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
}

// PaginateQuery runs a DynamoDB Query looping on LastEvaluatedKey and returns all items.
func PaginateQuery(ctx context.Context, api DDBQueryAPI, in *dynamodb.QueryInput) ([]map[string]types.AttributeValue, error) {
	var all []map[string]types.AttributeValue
	var lastKey map[string]types.AttributeValue
	for {
		if lastKey != nil {
			in.ExclusiveStartKey = lastKey
		}
		out, err := api.Query(ctx, in)
		if err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
		if out.LastEvaluatedKey == nil {
			return all, nil
		}
		lastKey = out.LastEvaluatedKey
	}
}

// PaginateScan runs a DynamoDB Scan looping on LastEvaluatedKey and returns all items.
func PaginateScan(ctx context.Context, api DDBScanAPI, in *dynamodb.ScanInput) ([]map[string]types.AttributeValue, error) {
	var all []map[string]types.AttributeValue
	var lastKey map[string]types.AttributeValue
	for {
		if lastKey != nil {
			in.ExclusiveStartKey = lastKey
		}
		out, err := api.Scan(ctx, in)
		if err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
		if out.LastEvaluatedKey == nil {
			return all, nil
		}
		lastKey = out.LastEvaluatedKey
	}
}

// PaginateScanCount sums Count across all pages. Use with Select=COUNT for global stats.
func PaginateScanCount(ctx context.Context, api DDBScanAPI, in *dynamodb.ScanInput) (int64, error) {
	var total int64
	var lastKey map[string]types.AttributeValue
	for {
		if lastKey != nil {
			in.ExclusiveStartKey = lastKey
		}
		out, err := api.Scan(ctx, in)
		if err != nil {
			return 0, err
		}
		total += int64(out.Count)
		if out.LastEvaluatedKey == nil {
			return total, nil
		}
		lastKey = out.LastEvaluatedKey
	}
}
