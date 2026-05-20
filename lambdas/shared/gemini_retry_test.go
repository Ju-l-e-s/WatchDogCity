package shared

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/genai"
)

func init() {
	// Keep backoff waits negligible so retry tests run in milliseconds.
	retryBaseDelay = time.Millisecond
}

// TestCallGeminiWithRetry_RetriesThenSucceeds: three transient 429s followed by
// a success → four total calls, no error.
func TestCallGeminiWithRetry_RetriesThenSucceeds(t *testing.T) {
	calls := 0
	resp, err := CallGeminiWithRetry(context.Background(), func(_ context.Context) (*genai.GenerateContentResponse, error) {
		calls++
		if calls <= 3 {
			return nil, fmt.Errorf("rpc error: code = Unavailable desc = 429 Too Many Requests")
		}
		return &genai.GenerateContentResponse{}, nil
	}, 4)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if resp == nil {
		t.Fatalf("expected non-nil response")
	}
	if calls != 4 {
		t.Fatalf("expected 4 calls, got %d", calls)
	}
}

// TestCallGeminiWithRetry_NonRetriable: a non-transient error returns immediately
// after a single call.
func TestCallGeminiWithRetry_NonRetriable(t *testing.T) {
	calls := 0
	_, err := CallGeminiWithRetry(context.Background(), func(_ context.Context) (*genai.GenerateContentResponse, error) {
		calls++
		return nil, fmt.Errorf("400 invalid argument: bad schema")
	}, 4)
	if err == nil {
		t.Fatalf("expected error")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

// TestCallGeminiWithRetry_ContextCancelled: a cancelled context aborts during the
// backoff wait and surfaces context.Canceled.
func TestCallGeminiWithRetry_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := CallGeminiWithRetry(ctx, func(_ context.Context) (*genai.GenerateContentResponse, error) {
		calls++
		return nil, fmt.Errorf("503 service unavailable")
	}, 4)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before cancellation abort, got %d", calls)
	}
}
