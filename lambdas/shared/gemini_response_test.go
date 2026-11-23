package shared

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/genai"
)

type geminiTransport func(*http.Request) (*http.Response, error)

func (f geminiTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGeminiMigrationSDKRequestAndResponse(t *testing.T) {
	for _, tc := range []struct {
		model  string
		level  genai.ThinkingLevel
		schema *genai.Schema
		max    int32
	}{
		{PDFExtractionModel, genai.ThinkingLevelMinimal, newsletterFactSchema, 8192},
		{GeminiFlashModel, genai.ThinkingLevelMedium, newsletterFactSchema, 4096},
		{GeminiFlashModel, genai.ThinkingLevelLow, newsletterSchema, 32768},
		{GeminiFlashModel, genai.ThinkingLevelMedium, newsletterFactSchema, 16384},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.model, tc.max), func(t *testing.T) {
			ctx := context.Background()
			config := GeminiJSONConfig(tc.model, tc.level, tc.schema, tc.max)
			client, err := genai.NewClient(ctx, &genai.ClientConfig{
				APIKey: "offline-fixture", Backend: genai.BackendGeminiAPI,
				HTTPOptions: genai.HTTPOptions{BaseURL: "https://fixture.invalid", APIVersion: "v1beta"},
				HTTPClient: &http.Client{Transport: geminiTransport(func(r *http.Request) (*http.Response, error) {
					var request struct {
						GenerationConfig map[string]json.RawMessage
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						return nil, err
					}
					if r.URL.Host != "fixture.invalid" || r.URL.Path != "/v1beta/models/"+tc.model+":generateContent" {
						t.Errorf("unexpected endpoint %s", r.URL)
					}
					g := request.GenerationConfig
					if g["temperature"] != nil || string(g["responseMimeType"]) != `"application/json"` ||
						g["responseSchema"] == nil || string(g["maxOutputTokens"]) != fmt.Sprint(tc.max) ||
						!strings.Contains(string(g["thinkingConfig"]), string(tc.level)) {
						t.Errorf("unexpected generation parameters: %v", request.GenerationConfig)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
						Body: io.NopCloser(strings.NewReader(`{"modelVersion":"test-version","candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"internal","thought":true},{"text":"{\"supported\":"},{"text":"true}"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"thoughtsTokenCount":2}}`))}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Models.GenerateContent(ctx, tc.model, []*genai.Content{genai.NewContentFromText("fixture", genai.RoleUser)}, config)
			if err != nil {
				t.Fatal(err)
			}
			if raw, err := GeminiJSONText(response); err != nil || raw != `{"supported":true}` ||
				response.ModelVersion != "test-version" || response.UsageMetadata.ThoughtsTokenCount != 2 {
				t.Fatalf("unexpected decoded response %q, %v", raw, err)
			}
			for _, reason := range []genai.FinishReason{genai.FinishReasonMaxTokens, genai.FinishReasonSafety} {
				response.Candidates[0].FinishReason = reason
				if _, err := GeminiJSONText(response); err == nil {
					t.Fatalf("accepted response ending with %s", reason)
				}
			}
			if _, err := GeminiJSONText(&genai.GenerateContentResponse{}); err == nil {
				t.Fatal("accepted response without candidate")
			}
		})
	}
}
