package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestContactDeps(brevoURL string) *contactDeps {
	return &contactDeps{
		httpClient:  http.DefaultClient,
		brevoURL:    brevoURL,
		apiKey:      "test-key",
		senderEmail: "no-reply@example.com",
		adminEmail:  "admin@example.com",
	}
}

func contactPostRequest(name, email, message string) events.APIGatewayProxyRequest {
	body, _ := json.Marshal(map[string]string{
		"name":         name,
		"email_sender": email,
		"message":      message,
	})
	return events.APIGatewayProxyRequest{HTTPMethod: "POST", Body: string(body)}
}

// --- CORS ---

func TestContactHandler_OPTIONS(t *testing.T) {
	d := newTestContactDeps("")
	resp, err := d.handler(context.Background(), events.APIGatewayProxyRequest{HTTPMethod: "OPTIONS"})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "*", resp.Headers["Access-Control-Allow-Origin"])
}

func TestContactHandler_CORSOnAllResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
	}))
	defer srv.Close()
	d := newTestContactDeps(srv.URL)

	resp, err := d.handler(context.Background(), contactPostRequest("Alice", "alice@example.com", "Bonjour"))
	require.NoError(t, err)
	assert.Equal(t, "*", resp.Headers["Access-Control-Allow-Origin"])
}

// --- Input validation ---

func TestContactHandler_InvalidJSON(t *testing.T) {
	d := newTestContactDeps("")
	resp, err := d.handler(context.Background(), events.APIGatewayProxyRequest{
		HTTPMethod: "POST",
		Body:       "{bad json",
	})
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)
}

func TestContactHandler_MissingFields(t *testing.T) {
	d := newTestContactDeps("")
	cases := []struct {
		name    string
		email   string
		message string
	}{
		{"", "user@example.com", "message"},
		{"Alice", "", "message"},
		{"Alice", "user@example.com", ""},
		{"  ", "user@example.com", "message"},  // whitespace-only name
		{"Alice", "  ", "message"},             // whitespace-only email
		{"Alice", "user@example.com", "   "},   // whitespace-only message
	}
	for _, tc := range cases {
		resp, err := d.handler(context.Background(), contactPostRequest(tc.name, tc.email, tc.message))
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode, "name=%q email=%q message=%q", tc.name, tc.email, tc.message)
		var body map[string]string
		json.Unmarshal([]byte(resp.Body), &body)
		assert.Equal(t, "name, email_sender and message are required", body["error"])
	}
}

// --- Successful send ---

func TestContactHandler_Success(t *testing.T) {
	var capturedPayload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedPayload)
		assert.Equal(t, "test-key", r.Header.Get("api-key"))
		w.WriteHeader(201)
	}))
	defer srv.Close()

	d := newTestContactDeps(srv.URL)
	resp, err := d.handler(context.Background(), contactPostRequest("Alice", "alice@example.com", "Bonjour"))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	var body map[string]string
	json.Unmarshal([]byte(resp.Body), &body)
	assert.Equal(t, "message sent", body["message"])
}

func TestContactHandler_BrevoPayloadStructure(t *testing.T) {
	var capturedPayload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedPayload)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	d := newTestContactDeps(srv.URL)
	d.handler(context.Background(), contactPostRequest("Alice", "alice@example.com", "Mon message"))

	// Verify required Brevo fields are present
	assert.Contains(t, capturedPayload, "sender")
	assert.Contains(t, capturedPayload, "to")
	assert.Contains(t, capturedPayload, "replyTo")
	assert.Contains(t, capturedPayload, "subject")
	assert.Contains(t, capturedPayload, "htmlContent")
	assert.Contains(t, capturedPayload, "textContent")

	// replyTo must be the sender's email (for the admin to reply directly)
	replyTo := capturedPayload["replyTo"].(map[string]interface{})
	assert.Equal(t, "alice@example.com", replyTo["email"])

	// Subject must mention the sender's name
	assert.Contains(t, capturedPayload["subject"], "Alice")
}

// --- Brevo errors ---

func TestContactHandler_BrevoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	d := newTestContactDeps(srv.URL)
	resp, err := d.handler(context.Background(), contactPostRequest("Alice", "alice@example.com", "Message"))
	require.NoError(t, err)
	assert.Equal(t, 500, resp.StatusCode)
	var body map[string]string
	json.Unmarshal([]byte(resp.Body), &body)
	assert.Equal(t, "delivery failed", body["error"])
}
