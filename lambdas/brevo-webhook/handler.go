package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type dynamoClient interface {
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type httpClient interface {
	Do(*http.Request) (*http.Response, error)
}

type webhookHandler struct {
	ddb       dynamoClient
	http      httpClient
	table     string
	secret    string
	brevoKey  string
	prodList  int
	now       func() time.Time
	brevoBase string
}

type webhookEvent struct {
	Event      string          `json:"event"`
	CampaignID json.RawMessage `json:"campaign_id"`
	CampID     json.RawMessage `json:"camp_id"`
	ID         json.RawMessage `json:"id"`
	TSSent     int64           `json:"ts_sent"`
}

type campaignInfo struct {
	Status     string `json:"status"`
	SentDate   string `json:"sentDate"`
	Recipients struct {
		Lists []int `json:"lists"`
	} `json:"recipients"`
}

func response(status int, body string) events.LambdaFunctionURLResponse {
	return events.LambdaFunctionURLResponse{
		StatusCode: status,
		Headers:    map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:       body,
	}
}

func (h *webhookHandler) handle(ctx context.Context, req events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	if req.RequestContext.HTTP.Method != http.MethodPost {
		return response(http.StatusMethodNotAllowed, "POST required"), nil
	}
	if h.secret == "" {
		return response(http.StatusServiceUnavailable, "webhook unavailable"), nil
	}
	query, err := url.ParseQuery(req.RawQueryString)
	if err != nil || len(query["token"]) != 1 || subtle.ConstantTimeCompare([]byte(query.Get("token")), []byte(h.secret)) != 1 {
		return response(http.StatusUnauthorized, "unauthorized"), nil
	}

	var body []byte
	if req.IsBase64Encoded {
		body, err = base64.StdEncoding.DecodeString(req.Body)
	} else {
		body = []byte(req.Body)
	}
	if err != nil || len(body) == 0 || len(body) > 64*1024 {
		return response(http.StatusBadRequest, "invalid body"), nil
	}
	var event webhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return response(http.StatusBadRequest, "invalid JSON"), nil
	}
	if event.Event != "delivered" && event.Event != "campaign_sent" && event.Event != "sent" {
		return response(http.StatusOK, "ignored"), nil
	}
	campaignID := positiveID(event.CampaignID)
	if campaignID == 0 {
		campaignID = positiveID(event.CampID)
	}
	// In Brevo marketing webhooks, id identifies the webhook delivery, not the
	// campaign. Only the explicit campaign_sent format may use id as a fallback.
	if campaignID == 0 && event.Event == "campaign_sent" {
		campaignID = positiveID(event.ID)
	}
	if campaignID == 0 {
		return response(http.StatusBadRequest, "missing campaign ID"), nil
	}

	councilID, alreadySent, err := h.findCouncil(ctx, campaignID)
	if err != nil {
		log.Printf("webhook council lookup failed for campaign %d: %v", campaignID, err)
		return response(http.StatusTooManyRequests, "retry lookup"), nil
	}
	if councilID == "" || alreadySent {
		return response(http.StatusOK, "ignored"), nil
	}
	info, err := h.getCampaign(ctx, campaignID)
	if err != nil {
		log.Printf("webhook Brevo status check failed for campaign %d: %v", campaignID, err)
		return response(http.StatusTooManyRequests, "retry status check"), nil
	}
	if len(info.Recipients.Lists) == 0 {
		return response(http.StatusTooManyRequests, "retry campaign details"), nil
	}
	if len(info.Recipients.Lists) != 1 || info.Recipients.Lists[0] != h.prodList {
		return response(http.StatusOK, "ignored"), nil
	}
	if event.Event == "delivered" && info.Status == "queued" {
		return response(http.StatusTooManyRequests, "retry campaign status"), nil
	}
	if info.Status != "sent" && !(event.Event == "delivered" && info.Status == "in_process") {
		return response(http.StatusOK, "ignored"), nil
	}
	sentAt := h.now().UTC()
	if info.SentDate != "" {
		if parsed, err := time.Parse(time.RFC3339, info.SentDate); err == nil {
			sentAt = parsed.UTC()
		}
	} else if event.TSSent > 0 {
		sentAt = time.Unix(event.TSSent, 0).UTC()
	}
	if err := h.recordSent(ctx, councilID, campaignID, sentAt); err != nil {
		var conditional *types.ConditionalCheckFailedException
		if errors.As(err, &conditional) {
			return response(http.StatusOK, "ignored"), nil
		}
		log.Printf("webhook DynamoDB update failed for campaign %d: %v", campaignID, err)
		return response(http.StatusTooManyRequests, "retry update"), nil
	}
	log.Printf("newsletter campaign %d recorded as sent for council %s", campaignID, councilID)
	return response(http.StatusOK, "ok"), nil
}

func positiveID(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		id, err := strconv.Atoi(number.String())
		if err == nil && id > 0 {
			return id
		}
	}
	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil {
		id, err := strconv.Atoi(stringID)
		if err == nil && id > 0 {
			return id
		}
	}
	return 0
}

func (h *webhookHandler) findCouncil(ctx context.Context, campaignID int) (string, bool, error) {
	var start map[string]types.AttributeValue
	var councilID string
	var alreadySent bool
	for {
		out, err := h.ddb.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(h.table),
			IndexName:              aws.String("newsletter_campaign_id-index"),
			KeyConditionExpression: aws.String("newsletter_campaign_id = :id"),
			FilterExpression:       aws.String("qc_status = :approved"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":id":       &types.AttributeValueMemberN{Value: strconv.Itoa(campaignID)},
				":approved": &types.AttributeValueMemberS{Value: "APPROVED"},
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return "", false, err
		}
		for _, item := range out.Items {
			id, ok := item["council_id"].(*types.AttributeValueMemberS)
			if !ok || id.Value == "" || councilID != "" {
				return "", false, fmt.Errorf("ambiguous council for Brevo campaign %d", campaignID)
			}
			councilID = id.Value
			if sent, ok := item["newsletter_sent_at"].(*types.AttributeValueMemberS); ok && sent.Value != "" {
				alreadySent = true
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return councilID, alreadySent, nil
		}
		start = out.LastEvaluatedKey
	}
}

func (h *webhookHandler) getCampaign(ctx context.Context, campaignID int) (campaignInfo, error) {
	url := fmt.Sprintf("%s/emailCampaigns/%d?excludeHtmlContent=true", strings.TrimRight(h.brevoBase, "/"), campaignID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return campaignInfo{}, err
	}
	req.Header.Set("api-key", h.brevoKey)
	req.Header.Set("accept", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		return campaignInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return campaignInfo{}, fmt.Errorf("Brevo returned HTTP %d", resp.StatusCode)
	}
	var info campaignInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&info); err != nil {
		return campaignInfo{}, err
	}
	return info, nil
}

func (h *webhookHandler) recordSent(ctx context.Context, councilID string, campaignID int, sentAt time.Time) error {
	_, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(h.table),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression:    aws.String("SET newsletter_sent_at = :sent REMOVE newsletter_pending_at"),
		ConditionExpression: aws.String("newsletter_campaign_id = :id AND qc_status = :approved AND attribute_not_exists(newsletter_sent_at)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":id":       &types.AttributeValueMemberN{Value: strconv.Itoa(campaignID)},
			":approved": &types.AttributeValueMemberS{Value: "APPROVED"},
			":sent":     &types.AttributeValueMemberS{Value: sentAt.Format(time.RFC3339)},
		},
	})
	return err
}
