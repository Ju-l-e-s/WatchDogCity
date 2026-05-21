package main

import (
	"context"
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

// fakeDDB captures UpdateItem inputs and replays scripted responses.
type fakeDDB struct {
	mu             sync.Mutex
	updateInputs   []*dynamodb.UpdateItemInput
	updateResponse func(call int, in *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error)
}

func (f *fakeDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
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
	d := &notifierDeps{httpClient: h, brevoKey: "k"}

	if err := d.sendCampaign(context.Background(), &NewsletterParams{}, councilID, councilDate); err != nil {
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
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/emailCampaigns/42"):
			return fakeResp{200, `{"status":"draft"}`}
		case isSendNowPOST(req):
			return fakeResp{204, ""}
		}
		return fakeResp{200, "{}"}
	}}
	d := &notifierDeps{httpClient: h, brevoKey: "k"}

	if err := d.sendCampaign(context.Background(), &NewsletterParams{}, councilID, councilDate); err != nil {
		t.Fatalf("sendCampaign: %v", err)
	}
	if n := h.count(isCreatePOST); n != 0 {
		t.Errorf("expected 0 create POSTs (draft reused), got %d", n)
	}
	if n := h.count(isSendNowPOST); n != 1 {
		t.Errorf("expected exactly 1 sendNow POST, got %d", n)
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
