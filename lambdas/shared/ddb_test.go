package shared

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// --- Query mock ---

type mockQuerier struct {
	pages [][]map[string]types.AttributeValue
	call  int
	err   error
}

func (m *mockQuerier) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	items := m.pages[m.call]
	out := &dynamodb.QueryOutput{Items: items}
	m.call++
	if m.call < len(m.pages) {
		out.LastEvaluatedKey = map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: "next"},
		}
	}
	return out, nil
}

func TestPaginateQuery_SinglePage(t *testing.T) {
	mock := &mockQuerier{pages: [][]map[string]types.AttributeValue{
		{{"id": &types.AttributeValueMemberS{Value: "a"}}},
	}}
	items, err := PaginateQuery(context.Background(), mock, &dynamodb.QueryInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
}

func TestPaginateQuery_TwoPages(t *testing.T) {
	mock := &mockQuerier{pages: [][]map[string]types.AttributeValue{
		{{"id": &types.AttributeValueMemberS{Value: "a"}}},
		{{"id": &types.AttributeValueMemberS{Value: "b"}}, {"id": &types.AttributeValueMemberS{Value: "c"}}},
	}}
	items, err := PaginateQuery(context.Background(), mock, &dynamodb.QueryInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d", len(items))
	}
}

func TestPaginateQuery_Error(t *testing.T) {
	mock := &mockQuerier{err: errors.New("ddb down")}
	_, err := PaginateQuery(context.Background(), mock, &dynamodb.QueryInput{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Scan mock ---

type mockScanner struct {
	pages []int32
	call  int
	err   error
}

func (m *mockScanner) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := &dynamodb.ScanOutput{Count: m.pages[m.call]}
	m.call++
	if m.call < len(m.pages) {
		out.LastEvaluatedKey = map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: "next"},
		}
	}
	return out, nil
}

func TestPaginateScanCount_SinglePage(t *testing.T) {
	mock := &mockScanner{pages: []int32{42}}
	n, err := PaginateScanCount(context.Background(), mock, &dynamodb.ScanInput{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Fatalf("want 42, got %d", n)
	}
}

func TestPaginateScanCount_TwoPages(t *testing.T) {
	mock := &mockScanner{pages: []int32{100, 50}}
	n, err := PaginateScanCount(context.Background(), mock, &dynamodb.ScanInput{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 150 {
		t.Fatalf("want 150, got %d", n)
	}
}

func TestPaginateScanCount_Error(t *testing.T) {
	mock := &mockScanner{err: errors.New("timeout")}
	_, err := PaginateScanCount(context.Background(), mock, &dynamodb.ScanInput{})
	if err == nil {
		t.Fatal("expected error")
	}
}
