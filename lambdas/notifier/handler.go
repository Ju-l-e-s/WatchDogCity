package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/watchdog/shared"
)

// errClaimAlreadyHeld signals that another invocation already claimed the
// newsletter send slot (either pending or completed). Callers should treat
// this as a clean no-op, not an error worth retrying.
var errClaimAlreadyHeld = errors.New("newsletter claim already held")

const brevoBaseURL = "https://api.brevo.com/v3"

// NewsletterParams is a transparent alias for the type that lives in shared.
// Kept here so package-level tests can reference the type without an explicit
// shared. prefix — the test file predates the hoist and must not be changed.
type NewsletterParams = shared.NewsletterParams

// ── Input event ───────────────────────────────────────────────────────────────

type NotifierEvent struct {
	CouncilID        string                  `json:"council_id"`
	TestListID       *int                    `json:"test_list_id"`
	NewsletterParams *shared.NewsletterParams `json:"newsletter_params,omitempty"`
	ScheduledAt      *string                 `json:"scheduled_at,omitempty"`
}

// ── DynamoDB records (minimal projection) ────────────────────────────────────

type councilRec struct {
	CouncilID           string `dynamodbav:"council_id"`
	Title               string `dynamodbav:"title"`
	Date                string `dynamodbav:"date"`
	NewsletterSentAt    string `dynamodbav:"newsletter_sent_at,omitempty"`
	NewsletterPendingAt string `dynamodbav:"newsletter_pending_at,omitempty"`
}

type deliberationRec struct {
	ID            string  `dynamodbav:"id"`
	CouncilID     string  `dynamodbav:"council_id"`
	Title         string  `dynamodbav:"title"`
	TopicTag      string  `dynamodbav:"topic_tag"`
	BudgetImpact  int64   `dynamodbav:"budget_impact"`
	BudgetType    string  `dynamodbav:"budget_type"`
	VotePour      *int    `dynamodbav:"vote_pour"`
	VoteContre    *int    `dynamodbav:"vote_contre"`
	VoteAbst      *int    `dynamodbav:"vote_abstention"`
	Disagreements *string `dynamodbav:"disagreements"`
	ClimateImpact string  `dynamodbav:"climate_impact"`
	IsSubstantial bool    `dynamodbav:"is_substantial"`
	Summary       string  `dynamodbav:"summary"`
	AnalysisData  struct {
		Impacts *string `dynamodbav:"impacts"`
	} `dynamodbav:"analysis_data"`
}

// ── Interfaces (for testability) ──────────────────────────────────────────────

type dynamoQuerier interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(ctx context.Context, params *dynamodb.ScanInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ── Deps ──────────────────────────────────────────────────────────────────────

// pendingClaimTTL bounds how long a pending claim is honored before being
// considered stale and recoverable. Set well above the Lambda timeout (15 min
// max for this function) divided by AWS async retry policy, so a crashing
// Lambda can never park the newsletter forever.
const pendingClaimTTL = 10 * time.Minute

func ptrInt32(i int32) *int32 { return &i }

type notifierDeps struct {
	ddb                dynamoQuerier
	httpClient         httpDoer
	geminiKey          string
	geminiModel        string
	brevoKey           string
	brevoTemplateID    int
	brevoListID        int
	senderEmail        string
	councilsTable      string
	deliberationsTable string
	now                func() time.Time // injectable for tests; defaults to time.Now
}

// ── Handler ───────────────────────────────────────────────────────────────────

func HandleRequest(ctx context.Context, event NotifierEvent) error {
	return sharedDeps.handle(ctx, event)
}

func (d *notifierDeps) handle(ctx context.Context, event NotifierEvent) error {
	if strings.HasPrefix(event.CouncilID, "metadata#") {
		log.Printf("notifier skip: council_id represents metadata (%q)", event.CouncilID)
		return nil
	}

	// Only check the Gemini circuit when we are about to call Gemini.
	// If the validator already generated newsletter_params, skip Gemini entirely.
	if event.NewsletterParams == nil {
		if open, err := shared.GeminiCircuitOpen(ctx, d.ddb, d.councilsTable); err != nil {
			log.Printf("gemini circuit check failed, proceeding: %v", err)
		} else if open {
			log.Printf("gemini circuit OPEN — skipping newsletter for council %s; will retry on next schedule", event.CouncilID)
			return nil
		}
	}

	// Override list ID if provided in event
	if event.TestListID != nil {
		d.brevoListID = *event.TestListID
		log.Printf("OVERRIDE: sending newsletter to test list ID %d", d.brevoListID)
	}

	council, err := d.fetchCouncil(ctx, event.CouncilID)
	if err != nil {
		return fmt.Errorf("fetch council %s: %w", event.CouncilID, err)
	}

	var params *shared.NewsletterParams
	if event.NewsletterParams != nil {
		// Fast path: validator pre-generated the params; no Gemini call needed.
		params = event.NewsletterParams
	} else {
		// Legacy / test path: generate via Gemini.
		delibs, err := d.fetchDeliberations(ctx, event.CouncilID)
		if err != nil {
			return fmt.Errorf("fetch deliberations for %s: %w", event.CouncilID, err)
		}
		nextMeeting := d.fetchNextMeeting(ctx)
		totalCouncils, totalDelibs := d.fetchGlobalStats(ctx)
		params, err = d.generateNewsletterParams(ctx, council, delibs, nextMeeting, totalCouncils, totalDelibs)
		if err != nil {
			return fmt.Errorf("generate newsletter params: %w", err)
		}
	}

	// Two-phase claim/commit:
	//   1. claimPending sets newsletter_pending_at conditionally — exactly one
	//      concurrent invocation wins. A pending claim older than
	//      pendingClaimTTL is considered stale (crashed Lambda) and reusable.
	//   2. sendCampaign hits Brevo.
	//   3. confirmSent flips pending → sent on success.
	//   4. releasePending wipes the pending flag on Brevo failure, so the
	//      next async retry (handled by AWS Lambda + DLQ, see CDK config)
	//      can take over without waiting for the TTL.
	// TestListID branch keeps the legacy direct-send behaviour: tests
	// against a Brevo list ID should never touch the claim ledger.
	if event.TestListID == nil {
		if err := d.claimPending(ctx, event.CouncilID); err != nil {
			if errors.Is(err, errClaimAlreadyHeld) {
				log.Printf("newsletter already claimed or in-flight for council %s — skipping", event.CouncilID)
				return nil
			}
			return fmt.Errorf("claim newsletter slot: %w", err)
		}
	}

	if err := d.sendCampaign(ctx, params, event.CouncilID, council.Date, event.ScheduledAt); err != nil {
		if event.TestListID == nil {
			if relErr := d.releasePending(ctx, event.CouncilID); relErr != nil {
				log.Printf("warn: failed to release pending claim for %s: %v", event.CouncilID, relErr)
			}
		}
		return fmt.Errorf("send brevo campaign: %w", err)
	}

	if event.TestListID == nil {
		if err := d.confirmSent(ctx, event.CouncilID); err != nil {
			// Best-effort: Brevo accepted the campaign — losing the confirmation
			// just risks a duplicate on next async retry, never a lost send.
			log.Printf("warn: brevo accepted campaign but DDB confirm failed for %s: %v", event.CouncilID, err)
		}
	}

	log.Printf("newsletter campaign sent for council %s (%s)", event.CouncilID, council.Date)
	return nil
}

// claimPending atomically claims a send slot. Returns errClaimAlreadyHeld
// when another invocation already holds the slot AND its pending claim has
// not expired (or the newsletter is already sent).
func (d *notifierDeps) claimPending(ctx context.Context, councilID string) error {
	now := d.now().UTC()
	staleThreshold := now.Add(-pendingClaimTTL).Format(time.RFC3339)

	_, err := d.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression: aws.String("SET newsletter_pending_at = :now"),
		ConditionExpression: aws.String(
			"attribute_not_exists(newsletter_sent_at) AND " +
				"(attribute_not_exists(newsletter_pending_at) OR newsletter_pending_at < :stale)",
		),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now":   &types.AttributeValueMemberS{Value: now.Format(time.RFC3339)},
			":stale": &types.AttributeValueMemberS{Value: staleThreshold},
		},
	})
	if err != nil {
		var ccfe *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return errClaimAlreadyHeld
		}
		return err
	}
	return nil
}

func (d *notifierDeps) confirmSent(ctx context.Context, councilID string) error {
	_, err := d.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression: aws.String("SET newsletter_sent_at = :ts REMOVE newsletter_pending_at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":ts": &types.AttributeValueMemberS{Value: d.now().UTC().Format(time.RFC3339)},
		},
	})
	return err
}

func (d *notifierDeps) releasePending(ctx context.Context, councilID string) error {
	_, err := d.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression: aws.String("REMOVE newsletter_pending_at"),
	})
	return err
}

// ── DynamoDB helpers ──────────────────────────────────────────────────────────

func (d *notifierDeps) fetchCouncil(ctx context.Context, councilID string) (*councilRec, error) {
	out, err := d.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, fmt.Errorf("council %s not found", councilID)
	}
	var c councilRec
	if err := attributevalue.UnmarshalMap(out.Item, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (d *notifierDeps) fetchDeliberations(ctx context.Context, councilID string) ([]deliberationRec, error) {
	items, err := shared.PaginateQuery(ctx, d.ddb, &dynamodb.QueryInput{
		TableName:              aws.String(d.deliberationsTable),
		IndexName:              aws.String("council_id-index"),
		KeyConditionExpression: aws.String("council_id = :cid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":cid": &types.AttributeValueMemberS{Value: councilID},
		},
	})
	if err != nil {
		return nil, err
	}
	var delibs []deliberationRec
	if err := attributevalue.UnmarshalListOfMaps(items, &delibs); err != nil {
		return nil, err
	}
	return delibs, nil
}

func (d *notifierDeps) fetchNextMeeting(ctx context.Context) string {
	out, err := d.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: "metadata#next_council"},
		},
	})
	if err != nil || out.Item == nil {
		return ""
	}
	var meta struct {
		DateText string `dynamodbav:"date_text"`
	}
	if err := attributevalue.UnmarshalMap(out.Item, &meta); err != nil {
		return ""
	}
	return meta.DateText
}

func (d *notifierDeps) fetchGlobalStats(ctx context.Context) (councils int, delibs int) {
	if n, err := shared.PaginateScanCount(ctx, d.ddb, &dynamodb.ScanInput{
		TableName:        aws.String(d.councilsTable),
		FilterExpression: aws.String("NOT (council_id = :meta)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":meta": &types.AttributeValueMemberS{Value: "metadata#next_council"},
		},
		Select: types.SelectCount,
	}); err == nil {
		councils = int(n)
	}

	if n, err := shared.PaginateScanCount(ctx, d.ddb, &dynamodb.ScanInput{
		TableName: aws.String(d.deliberationsTable),
		Select:    types.SelectCount,
	}); err == nil {
		delibs = int(n)
	}

	return councils, delibs
}

// ── Newsletter generation (thin wrapper — delegates to shared) ─────────────────

// toColdDelibs projects deliberation records into the whitelist struct that
// the newsletter LLM may see.
func toColdDelibs(delibs []deliberationRec) []shared.ColdDeliberation {
	cold := make([]shared.ColdDeliberation, len(delibs))
	for i, d := range delibs {
		contre := 0
		if d.VoteContre != nil {
			contre = *d.VoteContre
		}
		abst := 0
		if d.VoteAbst != nil {
			abst = *d.VoteAbst
		}
		hasDisagreement := contre > 0 || abst > 0

		impactsVal := ""
		if d.AnalysisData.Impacts != nil {
			impactsVal = *d.AnalysisData.Impacts
		}

		cold[i] = shared.ColdDeliberation{
			Title:           d.Title,
			TopicTag:        d.TopicTag,
			BudgetImpact:    d.BudgetImpact,
			BudgetType:      d.BudgetType,
			HasVote:         d.VotePour != nil || d.VoteContre != nil || d.VoteAbst != nil,
			Pour:            d.VotePour,
			Contre:          d.VoteContre,
			Abstention:      d.VoteAbst,
			ClimateImpact:   d.ClimateImpact,
			IsSubstantial:   d.IsSubstantial || d.BudgetImpact >= 5000,
			HasDisagreement: hasDisagreement,
			Summary:         d.Summary,
			Impacts:         impactsVal,
		}
	}
	return cold
}

func (d *notifierDeps) generateNewsletterParams(
	ctx context.Context,
	council *councilRec,
	delibs []deliberationRec,
	nextMeeting string,
	totalCouncils, totalDelibs int,
) (*shared.NewsletterParams, error) {
	cold := toColdDelibs(delibs)
	params, err := shared.GenerateNewsletterParams(
		ctx,
		shared.GeminiDeps{APIKey: d.geminiKey, Model: d.geminiModel},
		council.Title, council.Date,
		cold,
		nextMeeting, totalCouncils, totalDelibs,
	)
	if err != nil {
		if rerr := shared.RecordGeminiError(ctx, d.ddb, d.councilsTable); rerr != nil {
			log.Printf("warn: record gemini error: %v", rerr)
		}
		return nil, fmt.Errorf("gemini generate: %w", err)
	}
	if rerr := shared.RecordGeminiSuccess(ctx, d.ddb, d.councilsTable); rerr != nil {
		log.Printf("warn: record gemini success: %v", rerr)
	}
	return params, nil
}

// parseNewsletterParams is a thin shim kept for backward compatibility with
// the existing test file (which calls the unexported name directly). All logic
// lives in shared.ParseNewsletterParams.
func parseNewsletterParams(raw string) (*NewsletterParams, error) {
	return shared.ParseNewsletterParams(raw)
}

// ── Brevo campaign ────────────────────────────────────────────────────────────

func (d *notifierDeps) sendCampaign(ctx context.Context, params *NewsletterParams, councilID, councilDate string, scheduledAt *string) error {
	// A deterministic name keyed on (councilID, councilDate) lets us recognise a
	// campaign a previous — possibly transparently retried — invocation already
	// created, so a client-side HTTP retry can never create a second campaign.
	name := fmt.Sprintf("Newsletter-%s-%s", councilID, councilDate)
	idemKey := idempotencyKey(councilID, councilDate)

	existingID, status, err := d.lookupCampaign(ctx, name)
	if err != nil {
		return fmt.Errorf("lookup campaign %q: %w", name, err)
	}

	var campaignID int
	switch {
	case existingID != 0 && (status == "sent" || status == "queued" || status == "in_process" || status == "scheduled"):
		log.Printf("Brevo campaign %d (%q) already %s — skipping send", existingID, name, status)
		return nil
	case existingID != 0:
		// A leftover draft from an aborted run: reuse it instead of creating a duplicate.
		log.Printf("reusing existing Brevo campaign %d (%q, status %q)", existingID, name, status)
		campaignID = existingID
	default:
		campaignID, err = d.createCampaign(ctx, params, name, idemKey, scheduledAt)
		if err != nil {
			return fmt.Errorf("create campaign: %w", err)
		}
	}

	if scheduledAt != nil {
		log.Printf("Brevo campaign %d scheduled for %s — skipping immediate sendNow", campaignID, *scheduledAt)
		return nil
	}

	if err := d.triggerSend(ctx, campaignID, idemKey); err != nil {
		return fmt.Errorf("send campaign %d: %w", campaignID, err)
	}

	log.Printf("Brevo campaign %d dispatched", campaignID)
	return nil
}

// idempotencyKey derives a stable key from the council identity so that two
// requests for the same newsletter carry an identical Idempotency-Key header.
// Brevo may ignore the header; if so this is a harmless no-op and the
// lookupCampaign/pre-send-status guards still prevent a double send.
func idempotencyKey(councilID, councilDate string) string {
	sum := sha256.Sum256([]byte(councilID + "|" + councilDate))
	return hex.EncodeToString(sum[:16])
}

// lookupCampaign returns the id and status of the first campaign whose name
// matches exactly. Brevo offers no server-side name filter, so we page the
// most recent campaigns and match locally. (0, "", nil) means no match.
func (d *notifierDeps) lookupCampaign(ctx context.Context, name string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, brevoBaseURL+"/emailCampaigns?limit=50&offset=0", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("api-key", d.brevoKey)
	req.Header.Set("accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("list campaigns request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("brevo list campaigns status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Campaigns []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"campaigns"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, "", fmt.Errorf("decode campaign list: %w", err)
	}

	for _, c := range result.Campaigns {
		if c.Name == name {
			return c.ID, c.Status, nil
		}
	}
	return 0, "", nil
}

func (d *notifierDeps) createCampaign(ctx context.Context, params *NewsletterParams, name, idemKey string, scheduledAt *string) (int, error) {
	// Convert params struct → map[string]interface{} for the Brevo params field
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return 0, fmt.Errorf("marshal newsletter params: %w", err)
	}
	var paramsMap map[string]interface{}
	if err := json.Unmarshal(paramsJSON, &paramsMap); err != nil {
		return 0, fmt.Errorf("unmarshal newsletter params to map: %w", err)
	}

	campaignData := map[string]interface{}{
		"name":       name,
		"subject":    params.EmailSubject,
		"templateId": d.brevoTemplateID,
		"sender": map[string]string{
			"name":  "L'Observatoire de Bègles",
			"email": d.senderEmail,
		},
		"recipients": map[string]interface{}{
			"listIds": []int{d.brevoListID},
		},
		"params": paramsMap,
	}
	if scheduledAt != nil {
		campaignData["scheduledAt"] = *scheduledAt
	}

	payload, err := json.Marshal(campaignData)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, brevoBaseURL+"/emailCampaigns", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("api-key", d.brevoKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("create campaign request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("brevo create campaign status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.ID == 0 {
		return 0, fmt.Errorf("unexpected brevo response (no campaign id): %s", body)
	}

	log.Printf("Brevo campaign created: id=%d subject=%q", result.ID, params.EmailSubject)
	return result.ID, nil
}

// getCampaignStatus returns the current status of a single campaign.
func (d *notifierDeps) getCampaignStatus(ctx context.Context, campaignID int) (string, error) {
	url := fmt.Sprintf("%s/emailCampaigns/%d", brevoBaseURL, campaignID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("api-key", d.brevoKey)
	req.Header.Set("accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get campaign request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("brevo get campaign status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode campaign: %w", err)
	}
	return result.Status, nil
}

func (d *notifierDeps) triggerSend(ctx context.Context, campaignID int, idemKey string) error {
	// Pre-send guard: if Brevo already accepted this campaign for delivery, a
	// transparent HTTP retry must not fire sendNow a second time. A failed
	// status check is non-fatal — we'd rather risk Brevo's own dedup than drop
	// the newsletter on a transient read error.
	if status, err := d.getCampaignStatus(ctx, campaignID); err != nil {
		log.Printf("warn: pre-send status check failed for campaign %d, proceeding: %v", campaignID, err)
	} else if status == "sent" || status == "queued" || status == "in_process" {
		log.Printf("Brevo campaign %d already %s — skipping sendNow", campaignID, status)
		return nil
	}

	url := fmt.Sprintf("%s/emailCampaigns/%d/sendNow", brevoBaseURL, campaignID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("api-key", d.brevoKey)
	req.Header.Set("accept", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendNow request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// 204 No Content = success
	if resp.StatusCode >= 300 {
		return fmt.Errorf("brevo sendNow status %d: %s", resp.StatusCode, body)
	}
	return nil
}
