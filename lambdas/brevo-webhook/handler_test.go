package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDDB struct {
	queries int
	updates []*dynamodb.UpdateItemInput
	query   func(*dynamodb.QueryInput) (*dynamodb.QueryOutput, error)
	update  func(*dynamodb.UpdateItemInput) error
}

func (f *fakeDDB) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.queries++
	if f.query != nil {
		return f.query(in)
	}
	return &dynamodb.QueryOutput{}, nil
}

func (f *fakeDDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.updates = append(f.updates, in)
	if f.update != nil {
		return nil, f.update(in)
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

type fakeHTTP struct {
	calls int
	body  string
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.calls++
	if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/emailCampaigns/42") || req.Header.Get("api-key") != "brevo-key" {
		return nil, errors.New("wrong Brevo request")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(f.body))}, nil
}

func request(body string) events.LambdaFunctionURLRequest {
	req := events.LambdaFunctionURLRequest{RawQueryString: "token=secret", Body: body}
	req.RequestContext.HTTP.Method = http.MethodPost
	return req
}

func handlerForTest() (*webhookHandler, *fakeDDB, *fakeHTTP) {
	ddb := &fakeDDB{query: func(_ *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{{
			"council_id": &types.AttributeValueMemberS{Value: "council-1"},
		}}}, nil
	}}
	http := &fakeHTTP{body: `{"status":"sent","sentDate":"2026-09-29T15:30:00Z","recipients":{"lists":[2]}}`}
	h := &webhookHandler{
		ddb: ddb, http: http, table: "councils", secret: "secret",
		brevoKey: "brevo-key", prodList: 2, now: func() time.Time { return time.Date(2026, 9, 29, 17, 0, 0, 0, time.UTC) },
		brevoBase: "https://api.brevo.com/v3",
	}
	return h, ddb, http
}

func TestDeliveredWebhookRecordsOnlyProductionCampaign(t *testing.T) {
	h, ddb, httpClient := handlerForTest()
	ddb.query = func(in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		if *in.IndexName != "newsletter_campaign_id-index" || in.ExpressionAttributeValues[":id"].(*types.AttributeValueMemberN).Value != "42" {
			t.Error("campaign lookup must query the index for ID 42")
		}
		return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{{
			"council_id": &types.AttributeValueMemberS{Value: "council-1"},
		}}}, nil
	}
	resp, err := h.handle(context.Background(), request(`{"event":"delivered","id":999,"camp_id":42,"ts_sent":1790695800}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("webhook response = %+v, %v", resp, err)
	}
	if httpClient.calls != 1 || len(ddb.updates) != 1 {
		t.Fatalf("Brevo calls=%d, DynamoDB updates=%d", httpClient.calls, len(ddb.updates))
	}
	update := ddb.updates[0]
	if update.ExpressionAttributeValues[":id"].(*types.AttributeValueMemberN).Value != "42" ||
		update.ExpressionAttributeValues[":sent"].(*types.AttributeValueMemberS).Value != "2026-09-29T15:30:00Z" ||
		!strings.Contains(*update.ConditionExpression, "qc_status = :approved") {
		t.Errorf("unsafe or incorrect DynamoDB update: %+v", update)
	}
}

func TestWebhookRejectsMissingOrWrongToken(t *testing.T) {
	h, ddb, httpClient := handlerForTest()
	for _, raw := range []string{"", "token=wrong", "token=secret&token=secret"} {
		req := request(`{"event":"delivered","camp_id":42}`)
		req.RawQueryString = raw
		resp, err := h.handle(context.Background(), req)
		if err != nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token query %q produced %+v, %v", raw, resp, err)
		}
	}
	if ddb.queries != 0 || httpClient.calls != 0 {
		t.Fatal("unauthorized requests reached dependencies")
	}
}

func TestWebhookIgnoresUnrelatedEventsAndTestCampaigns(t *testing.T) {
	h, ddb, httpClient := handlerForTest()
	resp, _ := h.handle(context.Background(), request(`{"event":"opened","camp_id":42}`))
	if resp.StatusCode != 200 || ddb.queries != 0 {
		t.Fatal("unrelated event triggered a lookup")
	}
	// Brevo's id is a webhook delivery ID. It must not be used as a campaign ID.
	resp, _ = h.handle(context.Background(), request(`{"event":"delivered","id":42}`))
	if resp.StatusCode != 400 || ddb.queries != 0 {
		t.Fatal("webhook delivery ID was mistaken for a campaign ID")
	}
	httpClient.body = `{"status":"sent","sentDate":"2026-09-29T15:30:00Z","recipients":{"lists":[3]}}`
	resp, _ = h.handle(context.Background(), request(`{"event":"delivered","camp_id":42}`))
	if resp.StatusCode != 200 || len(ddb.updates) != 0 {
		t.Fatal("test-list campaign changed the production send ledger")
	}
}

func TestWebhookHandlesInProcessDeliveryAndDuplicate(t *testing.T) {
	h, ddb, httpClient := handlerForTest()
	httpClient.body = `{"status":"in_process","recipients":{"lists":[2]}}`
	resp, _ := h.handle(context.Background(), request(`{"event":"delivered","camp_id":"42","ts_sent":1790695800}`))
	if resp.StatusCode != 200 || len(ddb.updates) != 1 {
		t.Fatalf("in-process delivery was not recorded: %+v", resp)
	}
	ddb.update = func(_ *dynamodb.UpdateItemInput) error { return &types.ConditionalCheckFailedException{} }
	resp, _ = h.handle(context.Background(), request(`{"event":"delivered","camp_id":42}`))
	if resp.StatusCode != 200 {
		t.Fatalf("duplicate webhook response = %d", resp.StatusCode)
	}
}

func TestCampaignSentAcceptsIDFallbackOnlyAfterBrevoConfirmation(t *testing.T) {
	h, ddb, _ := handlerForTest()
	resp, err := h.handle(context.Background(), request(`{"event":"campaign_sent","id":42}`))
	if err != nil || resp.StatusCode != 200 || len(ddb.updates) != 1 {
		t.Fatalf("campaign_sent fallback response = %+v, %v", resp, err)
	}
}

func TestWebhookRequestsRetryWhileCampaignDetailsLag(t *testing.T) {
	h, ddb, httpClient := handlerForTest()
	httpClient.body = `{"status":"queued","recipients":{"lists":[2]}}`
	resp, err := h.handle(context.Background(), request(`{"event":"delivered","camp_id":42}`))
	if err != nil || resp.StatusCode != http.StatusTooManyRequests || len(ddb.updates) != 0 {
		t.Fatalf("queued campaign response = %+v, %v", resp, err)
	}
}

func TestFindCouncilPaginates(t *testing.T) {
	h, ddb, _ := handlerForTest()
	ddb.query = func(in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		if in.ExclusiveStartKey == nil {
			return &dynamodb.QueryOutput{LastEvaluatedKey: map[string]types.AttributeValue{
				"council_id": &types.AttributeValueMemberS{Value: "page-1"},
			}}, nil
		}
		return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{{
			"council_id": &types.AttributeValueMemberS{Value: "council-1"},
		}}}, nil
	}
	id, sent, err := h.findCouncil(context.Background(), 42)
	if err != nil || id != "council-1" || sent || ddb.queries != 2 {
		t.Fatalf("findCouncil = %q, %t, %v after %d queries", id, sent, err, ddb.queries)
	}
}
