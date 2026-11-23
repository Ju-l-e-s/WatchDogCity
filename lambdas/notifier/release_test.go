package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type releaseLedgerFake struct {
	mu     sync.Mutex
	writes int
}

func (s *releaseLedgerFake) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.ConditionExpression == nil || *in.ConditionExpression != "attribute_not_exists(campaign_id)" {
		return nil, fmt.Errorf("missing durable replay guard")
	}
	if s.writes != 0 {
		return nil, &types.ConditionalCheckFailedException{}
	}
	s.writes++
	return &dynamodb.PutItemOutput{}, nil
}

func TestReleaseRequiresMatchingConfirmationAndBlocksReplay(t *testing.T) {
	for _, mode := range []string{"now", "schedule", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
			request := releaseRequest{CouncilID: "council", CampaignID: 42, Expires: now.Add(10 * time.Minute)}
			if mode == "schedule" {
				request.Schedule = now.Add(time.Hour)
			}
			html := "Reviewed content"
			list := 2
			h := &fakeHTTP{route: func(req *http.Request) fakeResp {
				if req.Method == http.MethodGet {
					return fakeResp{200, fmt.Sprintf(`{"id":42,"status":"draft","modifiedAt":"2026-11-22T11:00:00Z","subject":"Digest","htmlContent":%q,"recipients":{"lists":[%d]}}`, html, list)}
				}
				if req.Header.Get("Idempotency-Key") != "" {
					t.Error("delivery request permits an undocumented transparent replay")
				}
				if mode == "schedule" {
					var body map[string]string
					if req.Method != http.MethodPut || req.URL.Path != "/v3/emailCampaigns/42" ||
						json.NewDecoder(req.Body).Decode(&body) != nil || body["scheduledAt"] != "2026-11-22T13:00:00.000Z" {
						t.Errorf("unexpected schedule request %s %s: %v", req.Method, req.URL.Path, body)
					}
				} else if !isSendNowPOST(req) || req.URL.Path != "/v3/emailCampaigns/42/sendNow" {
					t.Errorf("unexpected release request %s %s", req.Method, req.URL.Path)
				}
				if mode == "ambiguous" {
					return fakeResp{502, ""}
				}
				return fakeResp{204, ""}
			}}
			ddb := &fakeDDB{getResponse: func(_ *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
				return &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{
					"qc_status":              &types.AttributeValueMemberS{Value: "APPROVED"},
					"newsletter_campaign_id": &types.AttributeValueMemberN{Value: "42"},
					"newsletter_params_json": &types.AttributeValueMemberS{Value: `{"email_subject":"Digest","fact_check_version":1}`},
				}}, nil
			}}
			d := newDeps(ddb, now)
			d.httpClient, d.brevoListID, d.releasesTable = h, 2, "releases"
			store := &releaseLedgerFake{}
			ctx := context.Background()
			_, confirm, err := d.releaseSnapshot(ctx, request)
			if err != nil {
				t.Fatal(err)
			}

			if err := d.release(ctx, store, request, ""); err == nil {
				t.Fatal("missing consent accepted")
			}
			html = "Edited content"
			if err := d.release(ctx, store, request, confirm); err == nil {
				t.Fatal("edited content accepted")
			}
			html = "Reviewed content"
			list = 3
			if err := d.release(ctx, store, request, confirm); err == nil {
				t.Fatal("wrong recipients accepted")
			}
			list = 2
			changed := request
			changed.Schedule = now.Add(2 * time.Hour)
			if err := d.release(ctx, store, changed, confirm); err == nil {
				t.Fatal("different schedule accepted")
			}
			expired := request
			expired.Expires = now
			if err := d.release(ctx, store, expired, confirm); err == nil {
				t.Fatal("expired authorization accepted")
			}
			mutations := func() int { return h.count(func(req *http.Request) bool { return req.Method != http.MethodGet }) }
			if mutations() != 0 || store.writes != 0 {
				t.Fatal("rejected authorization produced a side effect")
			}

			if mode == "now" {
				results := make(chan error, 2)
				for range 2 {
					go func() { results <- d.release(ctx, store, request, confirm) }()
				}
				first, second := <-results, <-results
				if (first == nil) == (second == nil) {
					t.Fatalf("expected exactly one successful concurrent release: %v, %v", first, second)
				}
				if mutations() != 1 || store.writes != 1 {
					t.Fatalf("concurrent release produced %d mutations and %d authorization records", mutations(), store.writes)
				}
				return
			}
			err = d.release(ctx, store, request, confirm)
			if mode == "ambiguous" {
				if err == nil || !strings.Contains(err.Error(), "requires reconciliation") {
					t.Fatalf("uncertain release did not require reconciliation: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := d.release(ctx, store, request, confirm); err == nil || mutations() != 1 || store.writes != 1 {
				t.Fatalf("authorization replay was not blocked: mutations=%d records=%d err=%v", mutations(), store.writes, err)
			}
		})
	}
}

func TestPrepareDraftDoesNotRecreateAfterAmbiguousCreation(t *testing.T) {
	const draftName = "Newsletter-council-2026-11-22"
	visible := false
	h := &fakeHTTP{route: func(req *http.Request) fakeResp {
		switch {
		case isCreatePOST(req):
			return fakeResp{502, "uncertain"}
		case req.Method == http.MethodPut && req.URL.Path == "/v3/emailCampaigns/42":
			return fakeResp{204, ""}
		case req.URL.Query().Get("offset") == "0" && visible:
			return fakeResp{200, `{"campaigns":[{"id":42,"name":"Newsletter-council-2026-11-22","status":"draft"}]}`}
		default:
			return fakeResp{200, `{"campaigns":[]}`}
		}
	}}
	ddb := &fakeDDB{}
	d := &notifierDeps{ddb: ddb, httpClient: h, brevoListID: 2}
	ctx := context.Background()
	params := &NewsletterParams{EmailSubject: "Digest"}
	if _, err := d.prepareDraft(ctx, params, "council", "2026-11-22", 0, "", true); err == nil {
		t.Fatal("expected ambiguous creation")
	}
	if len(ddb.updateInputs) != 1 || *ddb.updateInputs[0].UpdateExpression != "SET newsletter_draft_request = :name" {
		t.Fatal("missing creation intent before calling Brevo")
	}
	if _, err := d.prepareDraft(ctx, params, "council", "2026-11-22", 0, draftName, true); err == nil {
		t.Fatal("retry accepted unresolved creation")
	}
	visible = true
	if id, err := d.prepareDraft(ctx, params, "council", "2026-11-23", 0, draftName, true); err != nil || id != 42 {
		t.Fatalf("could not recover existing draft: id=%d err=%v", id, err)
	}
	if h.count(isCreatePOST) != 1 || h.count(isSendNowPOST) != 0 {
		t.Fatal("ambiguous draft was recreated or sent")
	}
}
