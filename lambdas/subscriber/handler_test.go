package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDDB implements dynamoDBAPI for testing.
type mockDDB struct {
	getItemFn func(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	putItemFn func(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

func (m *mockDDB) GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if m.getItemFn != nil {
		return m.getItemFn(ctx, params, optFns...)
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (m *mockDDB) PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if m.putItemFn != nil {
		return m.putItemFn(ctx, params, optFns...)
	}
	return &dynamodb.PutItemOutput{}, nil
}

func newTestDeps(brevoURL string) *subscriberDeps {
	return &subscriberDeps{
		ddb:        &mockDDB{},
		httpClient: http.DefaultClient,
		tableName:  "test-table",
		brevoURL:   brevoURL,
		apiKey:     "test-key",
		templateID: 42,
		apiURL:     "https://api.test/",
	}
}

func postRequest(email string) events.APIGatewayProxyRequest {
	body, _ := json.Marshal(map[string]string{"email": email})
	return events.APIGatewayProxyRequest{HTTPMethod: "POST", Body: string(body)}
}

// --- Email validation ---

func TestValidateEmail(t *testing.T) {
	valid := []string{"user@example.com", "user+tag@sub.domain.fr", "a@b.io"}
	for _, e := range valid {
		assert.True(t, isValidEmail(e), "expected valid: %s", e)
	}
	invalid := []string{"notanemail", "missing@tld", "", "@domain.com", "user@", "user @example.com"}
	for _, e := range invalid {
		assert.False(t, isValidEmail(e), "expected invalid: %s", e)
	}
}

// --- CORS headers ---

func TestCORSHeaders(t *testing.T) {
	h := corsHeaders()
	assert.Equal(t, "*", h["Access-Control-Allow-Origin"])
	assert.Equal(t, "POST,OPTIONS", h["Access-Control-Allow-Methods"])
	assert.Equal(t, "Content-Type", h["Access-Control-Allow-Headers"])
	assert.Equal(t, "application/json", h["Content-Type"])
}

// --- HTTP method routing ---

func TestHandlerOPTIONS(t *testing.T) {
	d := newTestDeps("")
	resp, err := d.handler(context.Background(), events.APIGatewayProxyRequest{HTTPMethod: "OPTIONS"})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "*", resp.Headers["Access-Control-Allow-Origin"])
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	d := newTestDeps("")
	for _, method := range []string{"GET", "PUT", "DELETE", "PATCH"} {
		resp, err := d.handler(context.Background(), events.APIGatewayProxyRequest{HTTPMethod: method})
		require.NoError(t, err)
		assert.Equal(t, 405, resp.StatusCode, "method %s should return 405", method)
	}
}

// --- Input validation ---

func TestHandleSubscribe_InvalidJSON(t *testing.T) {
	d := newTestDeps("")
	resp, err := d.handler(context.Background(), events.APIGatewayProxyRequest{
		HTTPMethod: "POST",
		Body:       "not json {{{",
	})
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &body))
	assert.Equal(t, "invalid email", body["error"])
}

func TestHandleSubscribe_InvalidEmails(t *testing.T) {
	d := newTestDeps("")
	for _, email := range []string{"notvalid", "missing@", "@domain.com", "", "user @example.com"} {
		resp, err := d.handler(context.Background(), postRequest(email))
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode, "email %q should be rejected", email)
	}
}

// --- Already confirmed ---

func TestHandleSubscribe_AlreadyConfirmed(t *testing.T) {
	d := newTestDeps("")
	d.ddb = &mockDDB{
		getItemFn: func(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{
				Item: map[string]types.AttributeValue{
					"email":  &types.AttributeValueMemberS{Value: "user@example.com"},
					"status": &types.AttributeValueMemberS{Value: "CONFIRMED"},
				},
			}, nil
		},
	}

	resp, err := d.handler(context.Background(), postRequest("user@example.com"))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &body))
	assert.Equal(t, "already subscribed", body["message"])
}

func TestHandleSubscribe_PendingIsRetried(t *testing.T) {
	// A BREVO_PENDING subscriber should get a new confirmation email (no early return).
	brevoCallCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		brevoCallCount++
		w.WriteHeader(201)
		w.Write([]byte(`{"messageId":"<test>"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	d.ddb = &mockDDB{
		getItemFn: func(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{
				Item: map[string]types.AttributeValue{
					"email":  &types.AttributeValueMemberS{Value: "user@example.com"},
					"status": &types.AttributeValueMemberS{Value: "BREVO_PENDING"},
				},
			}, nil
		},
	}

	resp, err := d.handler(context.Background(), postRequest("user@example.com"))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, 1, brevoCallCount, "should have retried sending to Brevo")
}

// --- Successful subscription ---

func TestHandleSubscribe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "test-key", r.Header.Get("api-key"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(201)
		w.Write([]byte(`{"messageId":"<abc123@smtp.brevo.com>"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), postRequest("user@example.com"))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &body))
	assert.Equal(t, "confirmation email sent", body["message"])
}

func TestHandleSubscribe_ResponseHasCORSHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte(`{"messageId":"<test>"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), postRequest("user@example.com"))
	require.NoError(t, err)
	assert.Equal(t, "*", resp.Headers["Access-Control-Allow-Origin"])
	assert.Equal(t, "application/json", resp.Headers["Content-Type"])
}

// --- Brevo errors ---

func TestHandleSubscribe_Brevo500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"message":"internal server error"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), postRequest("user@example.com"))
	require.NoError(t, err)
	assert.Equal(t, 500, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &body))
	assert.Equal(t, "failed to send confirmation email, please try again", body["error"])
}

func TestHandleSubscribe_Brevo401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"authentication not found in headers","code":"unauthorized"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), postRequest("user@example.com"))
	require.NoError(t, err)
	assert.Equal(t, 500, resp.StatusCode)
}

func TestHandleSubscribe_BrevoUnavailable(t *testing.T) {
	// Point at a server that immediately closes the connection.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	resp, err := d.handler(context.Background(), postRequest("user@example.com"))
	require.NoError(t, err)
	assert.Equal(t, 500, resp.StatusCode)
}

// --- Email normalization ---

func TestHandleSubscribe_EmailNormalization(t *testing.T) {
	var capturedEmail string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		if to, ok := payload["to"].([]interface{}); ok && len(to) > 0 {
			if m, ok := to[0].(map[string]interface{}); ok {
				capturedEmail, _ = m["email"].(string)
			}
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"messageId":"<test>"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	body, _ := json.Marshal(map[string]string{"email": "  User@EXAMPLE.COM  "})
	resp, err := d.handler(context.Background(), events.APIGatewayProxyRequest{
		HTTPMethod: "POST",
		Body:       string(body),
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "user@example.com", capturedEmail)
}

// --- Confirmation link format ---

func TestSendConfirmationEmail_ConfirmLinkContainsBase64Token(t *testing.T) {
	var capturedParams map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		capturedParams, _ = payload["params"].(map[string]interface{})
		w.WriteHeader(201)
		w.Write([]byte(`{"messageId":"<test>"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	d.apiURL = "https://api.example.com/"
	err := d.sendConfirmationEmail("user@example.com")
	require.NoError(t, err)

	require.NotNil(t, capturedParams)
	confirmLink, _ := capturedParams["confirm_link"].(string)
	expectedToken := base64.URLEncoding.EncodeToString([]byte("user@example.com"))
	assert.Equal(t, fmt.Sprintf("https://api.example.com/confirm?token=%s", expectedToken), confirmLink)
}

func TestSendConfirmationEmail_TemplateIDSent(t *testing.T) {
	var capturedTemplateID float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		capturedTemplateID, _ = payload["templateId"].(float64)
		w.WriteHeader(201)
		w.Write([]byte(`{"messageId":"<test>"}`))
	}))
	defer srv.Close()

	d := newTestDeps(srv.URL)
	d.templateID = 99
	err := d.sendConfirmationEmail("user@example.com")
	require.NoError(t, err)
	assert.Equal(t, float64(99), capturedTemplateID)
}
