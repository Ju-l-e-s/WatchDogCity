package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/watchdog/shared"
)

// fakeResp is a scripted HTTP response for fakeHTTP.
type fakeResp struct {
	status int
	body   string
}

// fakeHTTP records outbound requests and replays scripted responses routed by
// the caller-supplied route func. It matches the httpDoer interface.
type fakeHTTP struct {
	mu       sync.Mutex
	requests []*http.Request
	route    func(req *http.Request) fakeResp
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	r := fakeResp{status: 200, body: "{}"}
	if f.route != nil {
		r = f.route(req)
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     make(http.Header),
	}, nil
}

func (f *fakeHTTP) count(pred func(*http.Request) bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if pred(r) {
			n++
		}
	}
	return n
}

func isCreatePOST(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/emailCampaigns")
}

func isSendNowPOST(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sendNow")
}

func isSendTestPOST(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sendTest")
}

// fakeDDB captures UpdateItem inputs and replays scripted responses.
type fakeDDB struct {
	mu             sync.Mutex
	updateInputs   []*dynamodb.UpdateItemInput
	updateResponse func(call int, in *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error)
	getResponse    func(in *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error)
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if f.getResponse != nil {
		return f.getResponse(in)
	}
	return &dynamodb.GetItemOutput{}, nil
}
func (f *fakeDDB) Query(_ context.Context, _ *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}
func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (f *fakeDDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.mu.Lock()
	call := len(f.updateInputs)
	f.updateInputs = append(f.updateInputs, in)
	f.mu.Unlock()
	if f.updateResponse != nil {
		return f.updateResponse(call, in)
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

func newDeps(ddb *fakeDDB, now time.Time) *notifierDeps {
	return &notifierDeps{
		ddb:           ddb,
		councilsTable: "councils-test",
		now:           fixedClock(now),
	}
}

// TestClaimPending_BuildsExpectedUpdate guards that the conditional update
// references both the sent flag and a stale-pending threshold computed from
// the injected clock.
func TestClaimPending_BuildsExpectedUpdate(t *testing.T) {
	now := time.Date(2026, 5, 19, 17, 30, 0, 0, time.UTC)
	ddb := &fakeDDB{}
	d := newDeps(ddb, now)

	if err := d.claimPending(context.Background(), "council-42"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got := len(ddb.updateInputs); got != 1 {
		t.Fatalf("expected 1 UpdateItem call, got %d", got)
	}
	in := ddb.updateInputs[0]

	if *in.TableName != "councils-test" {
		t.Errorf("table = %q, want councils-test", *in.TableName)
	}
	if *in.UpdateExpression != "SET newsletter_pending_at = :now" {
		t.Errorf("update expr = %q", *in.UpdateExpression)
	}
	cond := *in.ConditionExpression
	if !strings.Contains(cond, "attribute_not_exists(newsletter_sent_at)") {
		t.Errorf("condition missing sent-at guard: %q", cond)
	}
	if !strings.Contains(cond, "attribute_not_exists(newsletter_pending_at) OR newsletter_pending_at < :stale") {
		t.Errorf("condition missing pending/stale guard: %q", cond)
	}

	nowVal := in.ExpressionAttributeValues[":now"].(*types.AttributeValueMemberS).Value
	staleVal := in.ExpressionAttributeValues[":stale"].(*types.AttributeValueMemberS).Value
	expectedNow := now.Format(time.RFC3339)
	expectedStale := now.Add(-pendingClaimTTL).Format(time.RFC3339)
	if nowVal != expectedNow {
		t.Errorf(":now = %q, want %q", nowVal, expectedNow)
	}
	if staleVal != expectedStale {
		t.Errorf(":stale = %q, want %q", staleVal, expectedStale)
	}
}

// TestClaimPending_AlreadyHeld maps a ConditionalCheckFailedException onto
// the sentinel errClaimAlreadyHeld, so the caller can treat concurrent
// invocations as a clean no-op.
func TestClaimPending_AlreadyHeld(t *testing.T) {
	ddb := &fakeDDB{
		updateResponse: func(_ int, _ *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
			return nil, &types.ConditionalCheckFailedException{}
		},
	}
	d := newDeps(ddb, time.Now())

	err := d.claimPending(context.Background(), "council-42")
	if !errors.Is(err, errClaimAlreadyHeld) {
		t.Fatalf("expected errClaimAlreadyHeld, got %v", err)
	}
}

// TestClaimPending_PropagatesOtherErrors ensures we don't swallow non-condition
// DDB failures (throttling, missing table, etc.) as silent no-ops.
func TestClaimPending_PropagatesOtherErrors(t *testing.T) {
	boom := errors.New("ProvisionedThroughputExceededException")
	ddb := &fakeDDB{
		updateResponse: func(_ int, _ *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
			return nil, boom
		},
	}
	d := newDeps(ddb, time.Now())

	err := d.claimPending(context.Background(), "council-42")
	if errors.Is(err, errClaimAlreadyHeld) {
		t.Fatalf("non-condition error must not collapse to errClaimAlreadyHeld")
	}
	if err == nil || !strings.Contains(err.Error(), "ProvisionedThroughputExceededException") {
		t.Fatalf("expected propagated error, got %v", err)
	}
}

// TestConfirmSent flips pending → sent in a single update.
func TestConfirmSent(t *testing.T) {
	now := time.Date(2026, 5, 19, 18, 0, 0, 0, time.UTC)
	ddb := &fakeDDB{}
	d := newDeps(ddb, now)

	if err := d.confirmSent(context.Background(), "council-42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	in := ddb.updateInputs[0]
	if *in.UpdateExpression != "SET newsletter_sent_at = :ts REMOVE newsletter_pending_at" {
		t.Errorf("confirm expr = %q", *in.UpdateExpression)
	}
	if in.ConditionExpression != nil {
		t.Errorf("confirmSent should be unconditional, got %q", *in.ConditionExpression)
	}
	if got := in.ExpressionAttributeValues[":ts"].(*types.AttributeValueMemberS).Value; got != now.Format(time.RFC3339) {
		t.Errorf(":ts = %q, want %q", got, now.Format(time.RFC3339))
	}
}

// TestReleasePending wipes only the pending flag, leaving sent_at untouched
// (it should never have been set if we get here).
func TestReleasePending(t *testing.T) {
	ddb := &fakeDDB{}
	d := newDeps(ddb, time.Now())

	if err := d.releasePending(context.Background(), "council-42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	in := ddb.updateInputs[0]
	if *in.UpdateExpression != "REMOVE newsletter_pending_at" {
		t.Errorf("release expr = %q", *in.UpdateExpression)
	}
	if in.ConditionExpression != nil {
		t.Errorf("releasePending must be unconditional, got %q", *in.ConditionExpression)
	}
}

// TestClaimPending_ConcurrentRace simulates two callers racing the same
// council: only the first UpdateItem succeeds, the second receives a
// ConditionalCheckFailedException. Guards against the regression where a
// duplicate-newsletter race could slip through.
func TestClaimPending_ConcurrentRace(t *testing.T) {
	var winner int32
	ddb := &fakeDDB{
		updateResponse: func(call int, _ *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
			if call == 0 {
				atomicStore(&winner, 1)
				return &dynamodb.UpdateItemOutput{}, nil
			}
			return nil, &types.ConditionalCheckFailedException{}
		},
	}
	d := newDeps(ddb, time.Now())

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		idx := i
		go func() {
			defer wg.Done()
			errs[idx] = d.claimPending(context.Background(), "council-race")
		}()
	}
	wg.Wait()

	successes, conflicts := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			successes++
		case errors.Is(e, errClaimAlreadyHeld):
			conflicts++
		default:
			t.Errorf("unexpected error: %v", e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Errorf("expected 1 success + 1 conflict, got %d / %d", successes, conflicts)
	}
}

// atomicStore is a tiny helper to keep the race test self-contained without
// pulling sync/atomic int aliasing details into the readers' eyeline.
func atomicStore(p *int32, v int32) { *p = v }

// TestParseNewsletterParams_SchemaConformant feeds a JSON payload shaped exactly
// like newsletterSchema permits (enum tags from shared.TopicTags, string budgets,
// bool flags, integer counters) through the parser and asserts the fields land
// on NewsletterParams. Guards that the ResponseSchema contract and the Go struct
// stay in sync. No network call.
func TestParseNewsletterParams_SchemaConformant(t *testing.T) {
	const fixture = `{
  "email_subject": "Conseil du 14 mai",
  "council_title": "Conseil Municipal de Bègles",
  "council_date": "14 mai 2026",
  "main_issue": "Vote du budget primitif et débat sur la vidéoprotection.",
  "budget_total": "1 250 000",
  "has_global_budget": true,
  "vote_climat": "VOTES PARTAGÉS",
  "climat_color": "#E11D48",
  "vote_stats": "2 délib. non unanimes / jusqu'à 5 voix contre",
  "total_delibs_in_council": 12,
  "tensions": [
    {
      "title": "Extension de la vidéoprotection",
      "context": "La ville installe douze caméras supplémentaires.",
      "impact": "Surveillance accrue de l'espace public.",
      "budget": "180000",
      "has_budget": true,
      "vote_details": "5 votes contre"
    }
  ],
  "adopted": [
    {
      "tag": "Sport",
      "title": "Subvention au club de handball",
      "context": "Soutien à la saison sportive.",
      "impact": "Maintien des créneaux jeunes.",
      "budget": "20000",
      "has_budget": true
    }
  ],
  "briefs": [
    {"tag": "Administration", "summary": "Désignation des représentants en commission."}
  ],
  "next_meeting": "18 juin 2026",
  "website_url": "https://lobservatoiredebegles.fr",
  "total_councils": 7,
  "total_delibs": 84
}`

	p, err := parseNewsletterParams(fixture)
	if err != nil {
		t.Fatalf("parseNewsletterParams: %v", err)
	}

	if p.EmailSubject != "Conseil du 14 mai" {
		t.Errorf("email_subject = %q", p.EmailSubject)
	}
	if !p.HasGlobalBudget {
		t.Errorf("has_global_budget should be true")
	}
	if p.TotalDelibsInCouncil != 12 || p.TotalCouncils != 7 || p.TotalDelibs != 84 {
		t.Errorf("counters = %d/%d/%d", p.TotalDelibsInCouncil, p.TotalCouncils, p.TotalDelibs)
	}
	if len(p.Tensions) != 1 || p.Tensions[0].VoteDetails != "5 votes contre" || !p.Tensions[0].HasBudget {
		t.Errorf("tensions parsed incorrectly: %+v", p.Tensions)
	}
	if len(p.Adopted) != 1 || p.Adopted[0].Tag != "Sport" {
		t.Errorf("adopted parsed incorrectly: %+v", p.Adopted)
	}
	if len(p.Briefs) != 1 || p.Briefs[0].Tag != "Administration" {
		t.Errorf("briefs parsed incorrectly: %+v", p.Briefs)
	}

	// Every emitted tag must be a canonical TopicTag (the schema enum source).
	for _, tag := range []string{p.Adopted[0].Tag, p.Briefs[0].Tag} {
		found := false
		for _, valid := range shared.TopicTags {
			if tag == valid {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tag %q not in shared.TopicTags", tag)
		}
	}
}

// TestSendCampaign_SkipsWhenAlreadySent verifies that an existing campaign in a
// terminal/in-flight status short-circuits both the create and sendNow POSTs.
func TestSendCampaign_SkipsWhenAlreadySent(t *testing.T) {
	const councilID, councilDate = "council-7", "2026-05-19"
	name := fmt.Sprintf("Newsletter-%s-%s", councilID, councilDate)

	h := &fakeHTTP{route: func(req *http.Request) fakeResp {
		if req.Method == http.MethodGet && strings.Contains(req.URL.RawQuery, "limit=50") {
			return fakeResp{200, fmt.Sprintf(`{"campaigns":[{"id":777,"name":%q,"status":"sent"}],"count":1}`, name)}
		}
		return fakeResp{200, "{}"}
	}}
	d := &notifierDeps{httpClient: h, brevoKey: "k", testEmail: "owner@example.com", autoSendEnabled: true}

	if _, err := d.sendCampaign(context.Background(), &NewsletterParams{}, councilID, councilDate, nil); err != nil {
		t.Fatalf("sendCampaign: %v", err)
	}
	if n := h.count(isCreatePOST); n != 0 {
		t.Errorf("expected 0 create POSTs, got %d", n)
	}
	if n := h.count(isSendNowPOST); n != 0 {
		t.Errorf("expected 0 sendNow POSTs, got %d", n)
	}
}

// TestSendCampaign_ReusesDraft verifies that a leftover draft is reused: no new
// campaign is created and exactly one sendNow is issued.
func TestSendCampaign_ReusesDraft(t *testing.T) {
	const councilID, councilDate = "council-7", "2026-05-19"
	name := fmt.Sprintf("Newsletter-%s-%s", councilID, councilDate)

	h := &fakeHTTP{route: func(req *http.Request) fakeResp {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.RawQuery, "limit=50"):
			return fakeResp{200, fmt.Sprintf(`{"campaigns":[{"id":42,"name":%q,"status":"draft"}],"count":1}`, name)}
		case req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, "/emailCampaigns/42"):
			var body struct {
				Subject string `json:"subject"`
				Params  struct {
					EmailSubject string `json:"email_subject"`
				} `json:"params"`
				Recipients struct {
					ListIDs []int `json:"listIds"`
				} `json:"recipients"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Subject != "Sujet corrigé" || body.Params.EmailSubject != "Sujet corrigé" || len(body.Recipients.ListIDs) != 1 || body.Recipients.ListIDs[0] != 2 {
				t.Errorf("draft was not refreshed with checked production content: %+v", body)
			}
			return fakeResp{204, ""}
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/emailCampaigns/42"):
			return fakeResp{200, `{"status":"draft"}`}
		case isSendTestPOST(req):
			return fakeResp{204, ""}
		case isSendNowPOST(req):
			return fakeResp{204, ""}
		}
		return fakeResp{200, "{}"}
	}}
	d := &notifierDeps{httpClient: h, brevoKey: "k", brevoListID: 2, testEmail: "owner@example.com", autoSendEnabled: true}

	if _, err := d.sendCampaign(context.Background(), &NewsletterParams{EmailSubject: "Sujet corrigé"}, councilID, councilDate, nil); err != nil {
		t.Fatalf("sendCampaign: %v", err)
	}
	if n := h.count(isCreatePOST); n != 0 {
		t.Errorf("expected 0 create POSTs (draft reused), got %d", n)
	}
	if n := h.count(func(req *http.Request) bool { return req.Method == http.MethodPut }); n != 1 {
		t.Errorf("expected one draft refresh, got %d", n)
	}
	if n := h.count(isSendNowPOST); n != 1 {
		t.Errorf("expected exactly 1 sendNow POST, got %d", n)
	}
	if n := h.count(isSendTestPOST); n != 1 {
		t.Errorf("expected exactly 1 sendTest POST, got %d", n)
	}
}

// TestTriggerSend_SkipsIfAlreadyQueued verifies the pre-send status guard:
// a campaign already queued must not be dispatched again.
func TestTriggerSend_SkipsIfAlreadyQueued(t *testing.T) {
	h := &fakeHTTP{route: func(req *http.Request) fakeResp {
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/emailCampaigns/99") {
			return fakeResp{200, `{"status":"queued"}`}
		}
		return fakeResp{200, "{}"}
	}}
	d := &notifierDeps{httpClient: h, brevoKey: "k"}

	if err := d.triggerSend(context.Background(), 99, "idem-key"); err != nil {
		t.Fatalf("triggerSend: %v", err)
	}
	if n := h.count(isSendNowPOST); n != 0 {
		t.Errorf("expected 0 sendNow POSTs when already queued, got %d", n)
	}
}

func TestSendCampaign_DraftRefreshFailureBlocksDelivery(t *testing.T) {
	name := "Newsletter-council-7-2026-05-19"
	h := &fakeHTTP{route: func(req *http.Request) fakeResp {
		if req.Method == http.MethodGet && req.URL.Path == "/v3/emailCampaigns" {
			return fakeResp{200, fmt.Sprintf(`{"campaigns":[{"id":42,"name":%q,"status":"draft"}]}`, name)}
		}
		if req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, "/emailCampaigns/42") {
			return fakeResp{400, `{"message":"invalid params"}`}
		}
		return fakeResp{500, "unexpected request"}
	}}
	d := &notifierDeps{httpClient: h, brevoKey: "k", brevoListID: 2}
	_, err := d.sendCampaign(context.Background(), &NewsletterParams{EmailSubject: "Sujet corrigé"}, "council-7", "2026-05-19", nil)
	if err == nil || !strings.Contains(err.Error(), "refresh Brevo draft") {
		t.Fatalf("expected draft refresh error, got %v", err)
	}
	if h.count(isSendNowPOST) != 0 || h.count(isSendTestPOST) != 0 {
		t.Fatal("delivered a stale draft after failed refresh")
	}
}

func TestHandleTest_RejectsUncheckedStoredParams(t *testing.T) {
	ddb := &fakeDDB{getResponse: func(_ *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		return &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{
			"qc_status":              &types.AttributeValueMemberS{Value: "APPROVED"},
			"category":               &types.AttributeValueMemberS{Value: "Conseil municipal"},
			"newsletter_params_json": &types.AttributeValueMemberS{Value: `{"email_subject":"Unchecked"}`},
		}}, nil
	}}
	h := &fakeHTTP{}
	d := &notifierDeps{ddb: ddb, httpClient: h, councilsTable: "councils-test"}
	listID := 3
	if err := d.handle(context.Background(), NotifierEvent{CouncilID: "council-1", TestListID: &listID}); err == nil || !strings.Contains(err.Error(), "fact check") {
		t.Fatalf("expected fact-check gate, got %v", err)
	}
	if len(h.requests) != 0 {
		t.Fatal("Brevo was called for unchecked content")
	}
}

func TestHandle_SkipsMetadata(t *testing.T) {
	d := &notifierDeps{} // All fields nil/zero-value
	err := d.handle(context.Background(), NotifierEvent{
		CouncilID: "metadata#next_council",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestHandleTest_UsesApprovedStoredContentAndIsolatedCampaign(t *testing.T) {
	ddb := &fakeDDB{getResponse: func(in *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		if in.ConsistentRead == nil || !*in.ConsistentRead {
			t.Error("test council must be read consistently")
		}
		return &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{
			"council_id":             &types.AttributeValueMemberS{Value: "council-1"},
			"category":               &types.AttributeValueMemberS{Value: "Conseil municipal"},
			"qc_status":              &types.AttributeValueMemberS{Value: "APPROVED"},
			"date":                   &types.AttributeValueMemberS{Value: "2026-06-22"},
			"newsletter_params_json": &types.AttributeValueMemberS{Value: `{"email_subject":"Sujet validé","fact_check_version":1}`},
		}}, nil
	}}
	var names []string
	h := &fakeHTTP{route: func(req *http.Request) fakeResp {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.RawQuery, "limit=50"):
			return fakeResp{200, `{"campaigns":[]}`}
		case isCreatePOST(req):
			var body struct {
				Name       string `json:"name"`
				Subject    string `json:"subject"`
				Recipients struct {
					ListIDs []int `json:"listIds"`
				} `json:"recipients"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Subject != "Sujet validé" || len(body.Recipients.ListIDs) != 1 || body.Recipients.ListIDs[0] != 3 {
				t.Errorf("unexpected campaign: %+v", body)
			}
			if !strings.HasPrefix(body.Name, "Newsletter-TEST-list-3-") {
				t.Errorf("campaign name %q is not isolated from production", body.Name)
			}
			names = append(names, body.Name)
			return fakeResp{201, `{"id":42}`}
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/emailCampaigns/42"):
			return fakeResp{200, `{"status":"draft"}`}
		case isSendTestPOST(req):
			if req.Header.Get("Content-Type") != "application/json" || req.Header.Get("api-key") != "k" {
				t.Error("sendTest missing Brevo headers")
			}
			var body struct {
				EmailTo []string `json:"emailTo"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.EmailTo) != 1 || body.EmailTo[0] != "owner@example.com" {
				t.Errorf("unexpected sendTest recipients: %v", body.EmailTo)
			}
			return fakeResp{204, ""}
		case isSendNowPOST(req):
			return fakeResp{204, ""}
		}
		return fakeResp{200, `{}`}
	}}
	d := &notifierDeps{ddb: ddb, httpClient: h, brevoKey: "k", brevoListID: 2, brevoTemplateID: 7, councilsTable: "councils-test", testEmail: "owner@example.com"}
	listID := 3
	for range 2 {
		if err := d.handle(context.Background(), NotifierEvent{CouncilID: "council-1", TestListID: &listID}); err != nil {
			t.Fatal(err)
		}
	}
	if len(names) != 2 || names[0] == names[1] || h.count(isSendTestPOST) != 0 || h.count(isSendNowPOST) != 2 {
		t.Fatalf("expected two distinct test-list sends, got names=%v previews=%d sends=%d", names, h.count(isSendTestPOST), h.count(isSendNowPOST))
	}
	if d.brevoListID != 2 || len(ddb.updateInputs) != 0 {
		t.Fatal("test send changed production configuration or send ledger")
	}
}

func TestHandleTest_RejectsUnapprovedAndNonTestEvents(t *testing.T) {
	listID := 2
	d := &notifierDeps{}
	if err := d.handle(context.Background(), NotifierEvent{CouncilID: "council-1", TestListID: &listID}); err == nil {
		t.Fatal("accepted a non-test Brevo list")
	}
	listID = 3
	params := &NewsletterParams{EmailSubject: "Injected"}
	if err := d.handle(context.Background(), NotifierEvent{CouncilID: "council-1", TestListID: &listID, NewsletterParams: params}); err == nil {
		t.Fatal("accepted caller-supplied newsletter params")
	}
	ddb := &fakeDDB{getResponse: func(_ *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		return &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{
			"qc_status": &types.AttributeValueMemberS{Value: "QUARANTINED"},
			"category":  &types.AttributeValueMemberS{Value: "Conseil municipal"},
		}}, nil
	}}
	d.ddb = ddb
	d.councilsTable = "councils-test"
	if err := d.handle(context.Background(), NotifierEvent{CouncilID: "council-1", TestListID: &listID}); err == nil {
		t.Fatal("accepted a council without APPROVED status")
	}
}

func TestHandle_PreviewAndAutomaticSend(t *testing.T) {
	for _, tc := range []struct {
		name      string
		autoSend  bool
		testEmail string
	}{
		{name: "manual list 3 only"},
		{name: "manual draft with sendTest", testEmail: "owner@example.com"},
		{name: "automatic send", autoSend: true, testEmail: "owner@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ddb := &fakeDDB{getResponse: func(_ *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
				return &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{
					"council_id":             &types.AttributeValueMemberS{Value: "council-1"},
					"qc_status":              &types.AttributeValueMemberS{Value: "APPROVED"},
					"date":                   &types.AttributeValueMemberS{Value: "2026-06-22"},
					"newsletter_params_json": &types.AttributeValueMemberS{Value: `{"email_subject":"Sujet validé","fact_check_version":1}`},
				}}, nil
			}}
			var calls []string
			h := &fakeHTTP{route: func(req *http.Request) fakeResp {
				switch {
				case req.Method == http.MethodGet && req.URL.Path == "/v3/emailCampaigns":
					return fakeResp{200, `{"campaigns":[]}`}
				case isCreatePOST(req):
					var body map[string]json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if _, scheduled := body["scheduledAt"]; scheduled {
						t.Error("manual preview must create an unscheduled draft")
					}
					var subject string
					if err := json.Unmarshal(body["subject"], &subject); err != nil || subject != "Sujet validé" {
						t.Errorf("campaign subject = %q, want Validator content", subject)
					}
					var recipients struct {
						ListIDs []int `json:"listIds"`
					}
					if err := json.Unmarshal(body["recipients"], &recipients); err != nil {
						t.Fatal(err)
					}
					if len(recipients.ListIDs) != 1 {
						t.Fatalf("unexpected recipients: %v", recipients.ListIDs)
					}
					if recipients.ListIDs[0] == 3 {
						calls = append(calls, "createTest")
						return fakeResp{201, `{"id":43}`}
					}
					if recipients.ListIDs[0] != 2 {
						t.Errorf("production campaign targets list %d, want 2", recipients.ListIDs[0])
					}
					calls = append(calls, "create")
					return fakeResp{201, `{"id":42}`}
				case isSendTestPOST(req):
					calls = append(calls, "sendTest")
					return fakeResp{204, ""}
				case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/emailCampaigns/42"):
					return fakeResp{200, `{"status":"draft"}`}
				case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/emailCampaigns/43"):
					return fakeResp{200, `{"status":"draft"}`}
				case isSendNowPOST(req):
					wantID := "/emailCampaigns/43/sendNow"
					if tc.autoSend {
						wantID = "/emailCampaigns/42/sendNow"
					}
					if !strings.HasSuffix(req.URL.Path, wantID) {
						t.Errorf("sent wrong campaign: %s", req.URL.Path)
					}
					calls = append(calls, "sendNow")
					return fakeResp{204, ""}
				}
				return fakeResp{500, "unexpected request"}
			}}
			d := &notifierDeps{
				ddb: ddb, httpClient: h, brevoKey: "k", testEmail: tc.testEmail, brevoListID: 2,
				autoSendEnabled: tc.autoSend, councilsTable: "councils-test", now: fixedClock(time.Now()),
			}
			var scheduledAt *string
			if !tc.autoSend {
				value := "2026-10-01T18:00:00Z"
				scheduledAt = &value
			}
			if err := d.handle(context.Background(), NotifierEvent{
				CouncilID: "council-1", NewsletterParams: &NewsletterParams{EmailSubject: "Injected"},
				ScheduledAt: scheduledAt,
			}); err != nil {
				t.Fatal(err)
			}
			wantCalls := []string{"create"}
			if tc.testEmail != "" {
				wantCalls = append(wantCalls, "sendTest")
			}
			wantUpdate := "REMOVE newsletter_pending_at"
			if tc.autoSend {
				wantCalls = append(wantCalls, "sendNow")
				wantUpdate = "SET newsletter_sent_at = :ts REMOVE newsletter_pending_at"
			} else {
				wantCalls = append(wantCalls, "createTest", "sendNow")
			}
			if fmt.Sprint(calls) != fmt.Sprint(wantCalls) {
				t.Errorf("Brevo calls = %v, want %v", calls, wantCalls)
			}
			if len(ddb.updateInputs) != 3 || *ddb.updateInputs[2].UpdateExpression != wantUpdate {
				t.Errorf("DynamoDB updates do not match %q", wantUpdate)
			}
			if len(ddb.updateInputs) >= 2 && *ddb.updateInputs[1].UpdateExpression != "SET newsletter_campaign_id = :id" {
				t.Error("production campaign ID was not recorded")
			}
			if len(ddb.updateInputs) >= 2 {
				if got := ddb.updateInputs[1].ExpressionAttributeValues[":id"].(*types.AttributeValueMemberN).Value; got != "42" {
					t.Errorf("recorded campaign ID = %s, want production campaign 42", got)
				}
			}
		})
	}
}

func TestSendCampaign_RequiresTestRecipientForAutomaticSend(t *testing.T) {
	h := &fakeHTTP{}
	d := &notifierDeps{httpClient: h, brevoKey: "k", autoSendEnabled: true}
	_, err := d.sendCampaign(context.Background(), &NewsletterParams{}, "council-1", "2026-06-22", nil)
	if err == nil || !strings.Contains(err.Error(), "BREVO_TEST_EMAIL") {
		t.Fatalf("expected missing test recipient error, got %v", err)
	}
	if len(h.requests) != 0 {
		t.Fatal("Brevo was called without a test recipient")
	}
}

func TestSendCampaign_TestFailureDoesNotSendNow(t *testing.T) {
	h := &fakeHTTP{route: func(req *http.Request) fakeResp {
		switch {
		case req.Method == http.MethodGet:
			return fakeResp{200, `{"campaigns":[]}`}
		case isCreatePOST(req):
			return fakeResp{201, `{"id":42}`}
		case isSendTestPOST(req):
			return fakeResp{400, `{"message":"invalid recipient"}`}
		}
		return fakeResp{500, "unexpected request"}
	}}
	d := &notifierDeps{httpClient: h, brevoKey: "k", testEmail: "owner@example.com", autoSendEnabled: true}
	_, err := d.sendCampaign(context.Background(), &NewsletterParams{}, "council-1", "2026-06-22", nil)
	if err == nil || !strings.Contains(err.Error(), "sendTest status 400") {
		t.Fatalf("expected Brevo sendTest failure, got %v", err)
	}
	if h.count(isSendNowPOST) != 0 {
		t.Fatal("sent campaign despite failed preview")
	}
}

func TestReconcileOnly_RecordsBrevoSentDate(t *testing.T) {
	ddb := &fakeDDB{getResponse: func(_ *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		return &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{
			"council_id":             &types.AttributeValueMemberS{Value: "council-1"},
			"qc_status":              &types.AttributeValueMemberS{Value: "APPROVED"},
			"newsletter_campaign_id": &types.AttributeValueMemberN{Value: "42"},
		}}, nil
	}}
	h := &fakeHTTP{route: func(req *http.Request) fakeResp {
		if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/emailCampaigns/42") {
			t.Errorf("unexpected Brevo request: %s %s", req.Method, req.URL.Path)
		}
		return fakeResp{200, `{"status":"sent","sentDate":"2026-09-29T15:30:00Z"}`}
	}}
	d := &notifierDeps{ddb: ddb, httpClient: h, brevoKey: "k", councilsTable: "councils-test", now: fixedClock(time.Now())}
	if err := d.handle(context.Background(), NotifierEvent{CouncilID: "council-1", ReconcileOnly: true}); err != nil {
		t.Fatal(err)
	}
	if len(ddb.updateInputs) != 1 || *ddb.updateInputs[0].UpdateExpression != "SET newsletter_sent_at = :ts REMOVE newsletter_pending_at" {
		t.Fatal("manual send was not recorded")
	}
	if got := ddb.updateInputs[0].ExpressionAttributeValues[":ts"].(*types.AttributeValueMemberS).Value; got != "2026-09-29T15:30:00Z" {
		t.Errorf("recorded timestamp = %q", got)
	}
}

func TestReconcileOnly_LeavesDraftUnsent(t *testing.T) {
	ddb := &fakeDDB{getResponse: func(_ *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		return &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{
			"qc_status":              &types.AttributeValueMemberS{Value: "APPROVED"},
			"newsletter_campaign_id": &types.AttributeValueMemberN{Value: "42"},
		}}, nil
	}}
	h := &fakeHTTP{route: func(_ *http.Request) fakeResp { return fakeResp{200, `{"status":"draft"}`} }}
	d := &notifierDeps{ddb: ddb, httpClient: h, councilsTable: "councils-test"}
	if err := d.handle(context.Background(), NotifierEvent{CouncilID: "council-1", ReconcileOnly: true}); err == nil {
		t.Fatal("draft was incorrectly reconciled as sent")
	}
	if len(ddb.updateInputs) != 0 {
		t.Fatal("draft changed the send ledger")
	}
}
