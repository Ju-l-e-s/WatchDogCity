package shared

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"google.golang.org/genai"
)

// GeminiCall wraps a single GenerateContent invocation. The retry wrapper calls
// it once per attempt, passing the (possibly deadline-bound) context through.
type GeminiCall func(ctx context.Context) (*genai.GenerateContentResponse, error)

// retryBaseDelay is the first-attempt backoff unit (doubled each attempt, capped
// at 16×). Overridable in tests to keep them fast; do not change in production.
var retryBaseDelay = time.Second

// CallGeminiWithRetry retries a Gemini call with exponential backoff
// (1s, 2s, 4s, 8s, 16s, cap 16s) plus one-sided jitter up to +20%. It retries
// only on transient failures (429, 5xx, network timeouts/resets) and returns
// immediately on any other error. A cancelled context aborts the wait and
// returns ctx.Err().
func CallGeminiWithRetry(ctx context.Context, call GeminiCall, maxAttempts int) (*genai.GenerateContentResponse, error) {
	if maxAttempts <= 0 {
		maxAttempts = 4
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := call(ctx)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetriable(err) {
			return nil, err
		}
		if attempt == maxAttempts-1 {
			break
		}
		select {
		case <-time.After(backoff(attempt)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("gemini retry exhausted after %d attempts: %w", maxAttempts, lastErr)
}

func isRetriable(err error) bool {
	msg := err.Error()
	for _, sig := range []string{"429", "500", "502", "503", "504", "deadline exceeded", "connection reset"} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

func backoff(attempt int) time.Duration {
	base := retryBaseDelay * time.Duration(1<<attempt)
	if cap := retryBaseDelay * 16; base > cap {
		base = cap
	}
	jitter := time.Duration(rand.Int63n(int64(base)/5 + 1))
	return base + jitter
}
