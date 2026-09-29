package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/watchdog/shared"
)

type fakeCouncilReader struct {
	item  map[string]types.AttributeValue
	input *dynamodb.GetItemInput
}

func (f *fakeCouncilReader) GetItem(_ context.Context, input *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.input = input
	return &dynamodb.GetItemOutput{Item: f.item}, nil
}

func approvedItem(status string) map[string]types.AttributeValue {
	params, _ := json.Marshal(shared.NewsletterParams{
		EmailSubject: "L'Essentiel du Conseil", CouncilTitle: "Conseil du 1er mai", CouncilDate: "2026-05-01", MainIssue: "Décision factuelle.",
	})
	return map[string]types.AttributeValue{
		"qc_status":              &types.AttributeValueMemberS{Value: status},
		"title":                  &types.AttributeValueMemberS{Value: "Conseil du 1er mai"},
		"date":                   &types.AttributeValueMemberS{Value: "2026-05-01"},
		"newsletter_params_json": &types.AttributeValueMemberS{Value: string(params)},
	}
}

func TestLoadApprovedNewsletterRejectsUnapproved(t *testing.T) {
	for _, status := range []string{"", "PENDING", "VALIDATING", "QUARANTINED"} {
		reader := &fakeCouncilReader{item: approvedItem(status)}
		_, err := loadApprovedNewsletter(context.Background(), reader, "watchdog-councils", "council-1")
		if err == nil || !strings.Contains(err.Error(), "non APPROVED") {
			t.Fatalf("status %q: expected APPROVED rejection, got %v", status, err)
		}
	}
}

func TestLoadApprovedNewsletterAcceptsStoredParams(t *testing.T) {
	reader := &fakeCouncilReader{item: approvedItem("APPROVED")}
	council, err := loadApprovedNewsletter(context.Background(), reader, "watchdog-councils", "council-1")
	if err != nil {
		t.Fatal(err)
	}
	if reader.input.ConsistentRead == nil || !*reader.input.ConsistentRead {
		t.Fatal("expected strongly consistent read")
	}
	if council.Params.MainIssue != "Décision factuelle." {
		t.Fatalf("stored newsletter content was not loaded: %+v", council.Params)
	}
}

func TestLoadApprovedNewsletterRejectsOtherCategory(t *testing.T) {
	item := approvedItem("APPROVED")
	item["category"] = &types.AttributeValueMemberS{Value: "CCAS"}
	_, err := loadApprovedNewsletter(context.Background(), &fakeCouncilReader{item: item}, "watchdog-councils", "council-1")
	if err == nil || !strings.Contains(err.Error(), "pas de newsletter prévue") {
		t.Fatalf("expected category rejection, got %v", err)
	}
}

type fakeBrevoSender struct {
	requests []*http.Request
	payload  map[string]any
}

func (f *fakeBrevoSender) Do(req *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, req)
	if len(f.requests) == 1 {
		if err := json.NewDecoder(req.Body).Decode(&f.payload); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{"id":42}`))}, nil
	}
	return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestSendTestNewsletterOnlyTargetsListThree(t *testing.T) {
	council := &newsletterCouncil{Params: shared.NewsletterParams{EmailSubject: "Sujet", MainIssue: "Fait"}}
	brevo := &fakeBrevoSender{}
	id, err := sendTestNewsletter(context.Background(), brevo, "secret", council, 7, "", "#7", "newsletter@lobservatoiredebegles.fr")
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 || len(brevo.requests) != 2 {
		t.Fatalf("expected new campaign 42 and one send, got id %d and %d requests", id, len(brevo.requests))
	}
	if !strings.HasPrefix(brevo.payload["name"].(string), "TEST-list-3-") {
		t.Fatalf("test campaign lacks separate name: %v", brevo.payload["name"])
	}
	recipients := brevo.payload["recipients"].(map[string]any)["listIds"].([]any)
	if len(recipients) != 1 || recipients[0] != float64(3) {
		t.Fatalf("unexpected recipient lists: %v", recipients)
	}
	if brevo.payload["templateId"] != float64(7) {
		t.Fatalf("unexpected template: %v", brevo.payload["templateId"])
	}
	if brevo.requests[1].URL.Path != "/v3/emailCampaigns/42/sendNow" {
		t.Fatalf("unexpected send endpoint: %s", brevo.requests[1].URL.Path)
	}
}

func TestSendTestNewsletterUsesInlineHTML(t *testing.T) {
	council := &newsletterCouncil{Params: shared.NewsletterParams{EmailSubject: "Sujet"}}
	brevo := &fakeBrevoSender{}
	_, err := sendTestNewsletter(context.Background(), brevo, "secret", council, 0, "<html><body>Version v2</body></html>", "v2", "newsletter@lobservatoiredebegles.fr")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := brevo.payload["templateId"]; ok {
		t.Fatal("inline campaign must not reference a Brevo template")
	}
	if brevo.payload["htmlContent"] != "<html><body>Version v2</body></html>" {
		t.Fatalf("wrong inline HTML: %v", brevo.payload["htmlContent"])
	}
	recipients := brevo.payload["recipients"].(map[string]any)["listIds"].([]any)
	if len(recipients) != 1 || recipients[0] != float64(3) {
		t.Fatalf("unexpected recipient lists: %v", recipients)
	}
}

func TestLoadNewsletterFixtureRejectsUnapproved(t *testing.T) {
	path := t.TempDir() + "/fixture.json"
	if err := os.WriteFile(path, []byte(`{"council_id":"x","category":"Conseil municipal","qc_status":"PENDING","newsletter_params":{"email_subject":"Sujet"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNewsletterFixture(path); err == nil {
		t.Fatal("unapproved fixture was accepted")
	}
}
