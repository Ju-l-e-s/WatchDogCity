package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDDB implements dynamoDBAPI for testing.
type mockDDB struct {
	updateItemFn func(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	calledWith   *dynamodb.UpdateItemInput
}

func (m *mockDDB) UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	m.calledWith = params
	if m.updateItemFn != nil {
		return m.updateItemFn(ctx, params, optFns...)
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func newTestDeps(brevoURL string) *confirmerDeps {
	return &confirmerDeps{
		ddb:            &mockDDB{},
		httpClient:     http.DefaultClient,
		tableName:      "test-table",
		brevoURL:       brevoURL,
		apiKey:         "test-key",
		brevoListID:    7,
		redirectionURL: "https://example.com/merci.html",
	}
}

func tokenFor(email string) string {
	return base64.URLEncoding.EncodeToString([]byte(email))
}

func confirmRequest(token string) events.APIGatewayProxyRequest {
	return events.APIGatewayProxyRequest{
		QueryStringParameters: map[string]string{"token": token},
	}
}

// --- Missing / invalid token ---

func TestHandler_MissingToken(t *testing.T) {
	d := newTestDeps("")
	resp, err := d.handler(context.Background(), events.APIGatewayProxyRequest{})
	require.NoError(t, err)
	assert.Equal(t, 302, resp.StatusCode)
	assert.Equal(t, "https://example.com/merci.html", resp.Headers["Location"])
}

func TestHandler_InvalidBase64Token(t *testing.T) {
	d := newTestDeps("")
	resp, err := d.handler(context.Background(), confirmRequest("!!!not-base64!!!"))
	require.NoError(t, err)
	assert.Equal(t, 302, resp.StatusCode)
	assert.Equal(t, "https://example.com/merci.html", resp.Headers["Location"])
}

// --- Successful confirmation ---

func TestHandler_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "test-key", r.Header.Get("api-key"))
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), confirmRequest(tokenFor("user@example.com")))
	require.NoError(t, err)
	assert.Equal(t, 302, resp.StatusCode)
	assert.Equal(t, "https://example.com/merci.html", resp.Headers["Location"])
}

func TestHandler_UpdatesDynamoDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	mock := &mockDDB{}
	d := newTestDeps(srv.URL)
	d.ddb = mock

	resp, err := d.handler(context.Background(), confirmRequest(tokenFor("user@example.com")))
	require.NoError(t, err)
	assert.Equal(t, 302, resp.StatusCode)

	require.NotNil(t, mock.calledWith, "UpdateItem should have been called")
	assert.Equal(t, "test-table", *mock.calledWith.TableName)
}

func TestHandler_SendsEmailToBrevo(t *testing.T) {
	var capturedPayload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedPayload)
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), confirmRequest(tokenFor("user@example.com")))
	require.NoError(t, err)
	assert.Equal(t, 302, resp.StatusCode)

	assert.Equal(t, "user@example.com", capturedPayload["email"])
	assert.Equal(t, true, capturedPayload["updateEnabled"])

	listIDs, _ := capturedPayload["listIds"].([]interface{})
	require.Len(t, listIDs, 1)
	assert.Equal(t, float64(7), listIDs[0])
}

// --- Email normalization ---

func TestHandler_EmailNormalization(t *testing.T) {
	var capturedEmail string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		capturedEmail, _ = payload["email"].(string)
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	// Token encodes mixed-case email with spaces
	token := base64.URLEncoding.EncodeToString([]byte("  User@EXAMPLE.COM  "))
	resp, err := d.handler(context.Background(), confirmRequest(token))
	require.NoError(t, err)
	assert.Equal(t, 302, resp.StatusCode)
	assert.Equal(t, "user@example.com", capturedEmail)
}

// --- Brevo errors (still redirect — confirmation is best-effort) ---

func TestHandler_BrevoError_StillRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"message":"internal error"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), confirmRequest(tokenFor("user@example.com")))
	require.NoError(t, err)
	// Always redirect regardless of Brevo outcome
	assert.Equal(t, 302, resp.StatusCode)
	assert.Equal(t, "https://example.com/merci.html", resp.Headers["Location"])
}

func TestHandler_BrevoUnavailable_StillRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), confirmRequest(tokenFor("user@example.com")))
	require.NoError(t, err)
	assert.Equal(t, 302, resp.StatusCode)
}

// --- DynamoDB error (non-fatal) ---

func TestHandler_DynamoDBError_StillCallsBrevo(t *testing.T) {
	brevoCallCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		brevoCallCount++
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	d.ddb = &mockDDB{
		updateItemFn: func(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
			return nil, assert.AnError
		},
	}

	resp, err := d.handler(context.Background(), confirmRequest(tokenFor("user@example.com")))
	require.NoError(t, err)
	assert.Equal(t, 302, resp.StatusCode)
	assert.Equal(t, 1, brevoCallCount, "should still call Brevo even if DynamoDB fails")
}
