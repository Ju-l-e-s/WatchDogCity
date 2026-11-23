package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/genai"
)

const (
	PDFExtractionModel = "gemini-3.5-flash-lite"
	GeminiFlashModel   = "gemini-3.8-flash"
)

// GeminiJSONConfig leaves sampling options unset for the replacement models.
func GeminiJSONConfig(model string, level genai.ThinkingLevel, schema *genai.Schema, limit int32) *genai.GenerateContentConfig {
	cfg := &genai.GenerateContentConfig{ResponseMIMEType: "application/json", ResponseSchema: schema, MaxOutputTokens: limit}
	if strings.HasPrefix(model, "gemini-2.5-") {
		cfg.Temperature = ptrFloat32(0)
	} else {
		if model == PDFExtractionModel {
			level = genai.ThinkingLevelMinimal
		} else if level == genai.ThinkingLevelMinimal {
			level = genai.ThinkingLevelLow
		}
		cfg.ThinkingConfig = &genai.ThinkingConfig{ThinkingLevel: level}
	}
	return cfg
}

func GeminiJSONText(resp *genai.GenerateContentResponse) (string, error) {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0] == nil {
		return "", errors.New("Gemini returned no candidate")
	}
	if reason := resp.Candidates[0].FinishReason; reason != genai.FinishReasonStop {
		return "", fmt.Errorf("Gemini response incomplete: finish_reason=%s", reason)
	}
	if content := resp.Candidates[0].Content; content != nil {
		for _, part := range content.Parts {
			if part == nil {
				return "", errors.New("Gemini returned a null response part")
			}
		}
	}
	if raw := strings.TrimSpace(resp.Text()); raw != "" {
		return raw, nil
	}
	return "", errors.New("Gemini returned no JSON text")
}

// ObserveGemini logs each API attempt without including source content or error bodies.
func ObserveGemini(stage, model string, cfg *genai.GenerateContentConfig, call GeminiCall) GeminiCall {
	return func(ctx context.Context) (*genai.GenerateContentResponse, error) {
		start := time.Now()
		resp, err := call(ctx)
		entry := map[string]any{"event": "gemini_attempt", "stage": stage, "requested_model": model, "duration_ms": time.Since(start).Milliseconds()}
		if cfg.ThinkingConfig != nil {
			entry["thinking_level"] = cfg.ThinkingConfig.ThinkingLevel
		}
		if resp != nil {
			if resp.ModelVersion != "" {
				entry["model_version"] = resp.ModelVersion
			}
			entry["response_id"] = resp.ResponseID
			if len(resp.Candidates) > 0 && resp.Candidates[0] != nil {
				entry["finish_reason"] = resp.Candidates[0].FinishReason
			}
			if resp.PromptFeedback != nil {
				entry["block_reason"] = resp.PromptFeedback.BlockReason
			}
			if usage := resp.UsageMetadata; usage != nil {
				entry["input_tokens"] = usage.PromptTokenCount
				entry["input_modalities"] = usage.PromptTokensDetails
				entry["output_tokens"] = usage.CandidatesTokenCount
				entry["thinking_tokens"] = usage.ThoughtsTokenCount
				entry["total_tokens"] = usage.TotalTokenCount
			}
		}
		var apiErr genai.APIError
		switch {
		case errors.As(err, &apiErr):
			entry["http_status"] = apiErr.Code
			entry["error_class"] = "api"
		case errors.Is(err, context.DeadlineExceeded):
			entry["error_class"] = "timeout"
		case errors.Is(err, context.Canceled):
			entry["error_class"] = "canceled"
		case err != nil:
			entry["error_class"] = "transport"
		default:
			if raw, responseErr := GeminiJSONText(resp); responseErr != nil {
				entry["error_class"] = "incomplete_response"
			} else if !json.Valid([]byte(raw)) {
				entry["error_class"] = "invalid_json"
			}
		}
		line, _ := json.Marshal(entry)
		log.Print(string(line))
		return resp, err
	}
}
