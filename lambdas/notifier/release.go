package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const maxConfirmationAge = 15 * time.Minute

type releaseRequest struct {
	CouncilID  string
	CampaignID int
	Schedule   time.Time
	Expires    time.Time
}

type releaseStore interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

func releaseKey(id int) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"campaign_id": &types.AttributeValueMemberN{Value: strconv.Itoa(id)}}
}

func (d *notifierDeps) releaseRecorded(ctx context.Context, campaignID int) (bool, error) {
	out, err := d.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.releasesTable), Key: releaseKey(campaignID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return false, fmt.Errorf("read campaign release record: %w", err)
	}
	return len(out.Item) > 0, nil
}

func (d *notifierDeps) releaseSnapshot(ctx context.Context, request releaseRequest) (brevoCampaignInfo, string, error) {
	if request.CouncilID == "" || request.CampaignID <= 0 || d.brevoListID != 2 {
		return brevoCampaignInfo{}, "", errors.New("release requires a council, campaign ID and production list #2")
	}
	now := d.now()
	if !request.Expires.After(now) || request.Expires.After(now.Add(maxConfirmationAge)) {
		return brevoCampaignInfo{}, "", errors.New("confirmation must expire within the next 15 minutes")
	}
	if !request.Schedule.IsZero() && (!request.Schedule.After(now) || request.Schedule.Nanosecond() != 0) {
		return brevoCampaignInfo{}, "", errors.New("scheduled release must be in the future with whole-second precision")
	}
	council, err := d.fetchCouncil(ctx, request.CouncilID)
	if err != nil {
		return brevoCampaignInfo{}, "", err
	}
	var params NewsletterParams
	if council.QCStatus != "APPROVED" || (council.Category != "" && council.Category != "Conseil municipal") ||
		council.NewsletterCampaignID != request.CampaignID || council.NewsletterSentAt != "" ||
		json.Unmarshal([]byte(council.NewsletterParamsJSON), &params) != nil || params.FactCheckVersion < 1 {
		return brevoCampaignInfo{}, "", errors.New("campaign must be the saved draft of an APPROVED, fact-checked council")
	}
	info, err := d.getCampaignInfo(ctx, request.CampaignID)
	if err != nil {
		return brevoCampaignInfo{}, "", err
	}
	if info.ID != request.CampaignID || info.Status != "draft" || info.ScheduledAt != "" || info.SendAtBestTime || !info.targetsOnly(2) ||
		info.Subject == "" || info.Subject != params.EmailSubject || info.HTML == "" || info.ModifiedAt == "" {
		return brevoCampaignInfo{}, "", errors.New("Brevo draft ID, content or recipients could not be verified")
	}
	fingerprint, err := json.Marshal(struct {
		Request       releaseRequest
		Draft         brevoCampaignInfo
		CheckedParams string
	}{request, info, council.NewsletterParamsJSON})
	if err != nil {
		return brevoCampaignInfo{}, "", err
	}
	digest := sha256.Sum256(fingerprint)
	return info, hex.EncodeToString(digest[:]), nil
}

func (d *notifierDeps) release(ctx context.Context, store releaseStore, request releaseRequest, confirmation string) error {
	if len(confirmation) != sha256.Size*2 {
		return errors.New("explicit campaign confirmation is required")
	}
	if err := d.claimPending(ctx, request.CouncilID); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := d.releasePending(cleanup, request.CouncilID); err != nil {
			fmt.Fprintf(os.Stderr, "release newsletter lock: %v\n", err)
		}
	}()
	_, digest, err := d.releaseSnapshot(ctx, request)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(confirmation), []byte(digest)) != 1 {
		return errors.New("campaign or requested action changed; obtain a new confirmation")
	}
	item := releaseKey(request.CampaignID)
	item["council_id"] = &types.AttributeValueMemberS{Value: request.CouncilID}
	item["confirmation_sha256"] = &types.AttributeValueMemberS{Value: digest}
	item["requested_at"] = &types.AttributeValueMemberS{Value: d.now().UTC().Format(time.RFC3339)}
	action := "send_now"
	if !request.Schedule.IsZero() {
		action = "schedule"
		item["scheduled_at"] = &types.AttributeValueMemberS{Value: request.Schedule.Format(time.RFC3339)}
	}
	item["action"] = &types.AttributeValueMemberS{Value: action}
	// The record is written before touching Brevo and survives an ambiguous HTTP result.
	_, err = store.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.releasesTable), Item: item, ConditionExpression: aws.String("attribute_not_exists(campaign_id)"),
	})
	if err != nil {
		return fmt.Errorf("release not executed: cannot record unique authorization; reconcile campaign %d: %w", request.CampaignID, err)
	}
	_, latest, err := d.releaseSnapshot(ctx, request)
	if err != nil || latest != digest {
		return fmt.Errorf("release not executed: campaign %d could not be revalidated after recording authorization", request.CampaignID)
	}
	if err := d.mutateDelivery(ctx, request.CampaignID, request.Schedule); err != nil {
		return fmt.Errorf("release outcome for campaign %d requires reconciliation before any further action: %w", request.CampaignID, err)
	}
	return nil
}

// mutateDelivery is reached from an authorized admin release or a verified test-list campaign.
func (d *notifierDeps) mutateDelivery(ctx context.Context, campaignID int, schedule time.Time) error {
	campaignURL := fmt.Sprintf("%s/emailCampaigns/%d", brevoBaseURL, campaignID)
	method := http.MethodPost
	url := campaignURL + "/sendNow"
	var body io.Reader
	if !schedule.IsZero() {
		method, url = http.MethodPut, campaignURL
		payload, err := json.Marshal(map[string]string{"scheduledAt": schedule.UTC().Format("2006-01-02T15:04:05.000Z")})
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("api-key", d.brevoKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Brevo %s delivery returned HTTP %d", method, resp.StatusCode)
	}
	return nil
}

func (d *notifierDeps) runReleaseCommand(args []string) error {
	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		return errors.New("release is available only to an authenticated local operator")
	}
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	council := flags.String("council-id", "", "approved council ID")
	campaign := flags.Int("campaign-id", 0, "saved Brevo campaign ID")
	scheduled := flags.String("scheduled-at", "", "optional RFC3339 schedule")
	expires := flags.String("expires", "", "RFC3339 expiration printed by preview")
	confirm := flags.String("confirm", "", "campaign fingerprint after separate explicit consent")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *council == "" || *campaign <= 0 || (*confirm != "" && *expires == "") {
		return errors.New("release requires --council-id and --campaign-id; --confirm also requires --expires")
	}
	request := releaseRequest{CouncilID: *council, CampaignID: *campaign, Expires: d.now().UTC().Add(10 * time.Minute).Truncate(time.Second)}
	for _, value := range []struct {
		raw    string
		target *time.Time
	}{
		{*scheduled, &request.Schedule}, {*expires, &request.Expires},
	} {
		if value.raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value.raw)
		if err != nil {
			return err
		}
		*value.target = parsed.UTC()
	}
	admin := *d
	admin.brevoKey = strings.TrimSpace(os.Getenv("WATCHDOG_BREVO_ADMIN_API_KEY"))
	if admin.brevoKey == "" {
		return errors.New("WATCHDOG_BREVO_ADMIN_API_KEY is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if *confirm == "" {
		info, digest, err := admin.releaseSnapshot(ctx, request)
		if err != nil {
			return err
		}
		used, err := admin.releaseRecorded(ctx, request.CampaignID)
		if err != nil {
			return err
		}
		if used {
			return errors.New("campaign already has a release record; reconcile its status")
		}
		action := "envoi immédiat"
		if !request.Schedule.IsZero() {
			action = "programmation à " + request.Schedule.Format(time.RFC3339)
		}
		fmt.Printf("Conseil %q — campagne Brevo %d — sujet %q — liste #2 — %s\nVersion du brouillon : %s\nAprès consentement explicite et distinct : --expires %s --confirm %s\n",
			request.CouncilID, request.CampaignID, info.Subject, action, info.ModifiedAt, request.Expires.Format(time.RFC3339), digest)
		return nil
	}
	store, ok := admin.ddb.(releaseStore)
	if !ok {
		return errors.New("release ledger is unavailable")
	}
	if err := admin.release(ctx, store, request, *confirm); err != nil {
		return err
	}
	fmt.Printf("Brevo a accepté la demande pour la campagne %d. L’état d’envoi se vérifie séparément.\n", request.CampaignID)
	return nil
}
