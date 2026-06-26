package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/watchdog/shared"
)

// errAlreadyClaimed is returned when qc_status is not PENDING (another
// invocation claimed the slot, or the council is already in a terminal state).
var errAlreadyClaimed = errors.New("council already claimed for validation")

// ── Input event ───────────────────────────────────────────────────────────────

type ValidatorEvent struct {
	CouncilID string `json:"council_id"`
}

// ── Interfaces ────────────────────────────────────────────────────────────────

type ddbAPI interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type sqsAPI interface {
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

type lambdaAPI interface {
	Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// ── Deps ──────────────────────────────────────────────────────────────────────

type ValidatorHandler struct {
	ddb                ddbAPI
	lambdaClient       lambdaAPI
	sqsClient          sqsAPI
	councilsTable      string
	deliberationsTable string
	publisherFnName    string
	notifierFnName     string
	sqsQueueURL        string // PDF processing queue; empty = self-heal disabled
	geminiDeps         shared.GeminiDeps
	cfg                shared.QcConfig
}

// ── DynamoDB record types ─────────────────────────────────────────────────────

type councilRec struct {
	CouncilID     string `dynamodbav:"council_id"`
	Title         string `dynamodbav:"title"`
	Category      string `dynamodbav:"category"`
	Date          string `dynamodbav:"date"`
	TotalPdfs     int    `dynamodbav:"total_pdfs"`
	ProcessedPdfs int    `dynamodbav:"processed_pdfs"`
	QcStatus      string `dynamodbav:"qc_status"`
	QcAttempts    int    `dynamodbav:"qc_attempts"`
}

type analysisDataRec struct {
	Contexte *string `dynamodbav:"contexte"`
	Decision *string `dynamodbav:"decision"`
	Impacts  *string `dynamodbav:"impacts"`
}

type budgetBreakdownRec struct {
	TopicTag string `dynamodbav:"topic_tag"`
	Label    string `dynamodbav:"label"`
	Amount   int64  `dynamodbav:"amount"`
}

type deliberationRec struct {
	ID              string               `dynamodbav:"id"`
	Title           string               `dynamodbav:"title"`
	TopicTag        string               `dynamodbav:"topic_tag"`
	PDFURL          string               `dynamodbav:"pdf_url"`
	Summary         string               `dynamodbav:"summary"`
	AnalysisData    analysisDataRec      `dynamodbav:"analysis_data"`
	BudgetImpact    int64                `dynamodbav:"budget_impact"`
	BudgetType      string               `dynamodbav:"budget_type"`
	BudgetBreakdown []budgetBreakdownRec `dynamodbav:"budget_breakdown"`
	ClimateImpact   string               `dynamodbav:"climate_impact"`
	HasVote         bool                 `dynamodbav:"has_vote"`
	VotePour        *int                 `dynamodbav:"vote_pour"`
	VoteContre      *int                 `dynamodbav:"vote_contre"`
	VoteAbst        *int                 `dynamodbav:"vote_abstention"`
	Disagreements   *string              `dynamodbav:"disagreements"`
	IsSubstantial   bool                 `dynamodbav:"is_substantial"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

func (h *ValidatorHandler) HandleRequest(ctx context.Context, event ValidatorEvent) error {
	councilID := event.CouncilID

	// Atomic claim: PENDING → VALIDATING. Fails closed if council is in any
	// other state (already validated, already validating, or terminal).
	if err := h.claimValidating(ctx, councilID); err != nil {
		if errors.Is(err, errAlreadyClaimed) {
			log.Printf("council %s already claimed for validation — skipping", councilID)
			return nil
		}
		return fmt.Errorf("claim validating for %s: %w", councilID, err)
	}

	council, err := h.fetchCouncil(ctx, councilID)
	if err != nil {
		return fmt.Errorf("fetch council %s: %w", councilID, err)
	}

	delibs, err := h.fetchDeliberations(ctx, councilID)
	if err != nil {
		return fmt.Errorf("fetch deliberations for %s: %w", councilID, err)
	}

	// Compute baseline from previously APPROVED councils; best-effort.
	baseline, err := h.computeBaseline(ctx)
	if err != nil {
		log.Printf("warn: compute baseline failed, running without z-score: %v", err)
		baseline = shared.Baseline{}
	}

	councilView := shared.CouncilView{
		CouncilID:     councilID,
		TotalPdfs:     int64(council.TotalPdfs),
		ProcessedPdfs: int64(council.ProcessedPdfs),
		Date:          council.Date,
	}
	deliberationViews := toDeliberationViews(delibs)

	violations := append(
		shared.ValidateDeterministic(councilView, deliberationViews),
		shared.ValidateStatistical(councilView, deliberationViews, baseline, h.cfg)...,
	)
	verdict := shared.Decide(violations)

	if verdict.Status == "QUARANTINED" {
		if err := h.handleQuarantine(ctx, councilID, verdict); err != nil {
			return err
		}
		// Self-heal: re-enqueue PDFs for re-analysis if under retry cap.
		h.maybeHeal(ctx, council, delibs)
		return nil
	}
	return h.handleApproved(ctx, council, deliberationViews, verdict)
}

// claimValidating atomically transitions qc_status PENDING → VALIDATING and
// increments qc_attempts. Returns errAlreadyClaimed for any non-PENDING state.
func (h *ValidatorHandler) claimValidating(ctx context.Context, councilID string) error {
	_, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(h.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression:    aws.String("SET qc_status = :validating ADD qc_attempts :one"),
		ConditionExpression: aws.String("qc_status = :pending"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":validating": &types.AttributeValueMemberS{Value: "VALIDATING"},
			":pending":    &types.AttributeValueMemberS{Value: "PENDING"},
			":one":        &types.AttributeValueMemberN{Value: "1"},
		},
	})
	if err != nil {
		var ccfe *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return errAlreadyClaimed
		}
		return err
	}
	return nil
}

// handleQuarantine writes the QUARANTINED terminal state and emits CloudWatch
// EMF metric QcQuarantined. Idempotent: ConditionalCheckFailed is silently
// absorbed (another invocation already wrote the state).
func (h *ValidatorHandler) handleQuarantine(ctx context.Context, councilID string, verdict shared.Verdict) error {
	violationsJSON, _ := json.Marshal(verdict.Violations)
	ts := time.Now().UTC().Format(time.RFC3339)

	_, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(h.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression:    aws.String("SET qc_status = :q, qc_violations = :v, qc_validated_at = :ts"),
		ConditionExpression: aws.String("qc_status = :validating"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":q":          &types.AttributeValueMemberS{Value: "QUARANTINED"},
			":v":          &types.AttributeValueMemberS{Value: string(violationsJSON)},
			":ts":         &types.AttributeValueMemberS{Value: ts},
			":validating": &types.AttributeValueMemberS{Value: "VALIDATING"},
		},
	})
	if err != nil {
		var ccfe *ddbtypes.ConditionalCheckFailedException
		if !errors.As(err, &ccfe) {
			return fmt.Errorf("write quarantine for %s: %w", councilID, err)
		}
		log.Printf("council %s: quarantine already written", councilID)
	}

	// CloudWatch EMF structured log — Lambda flushes this as an embedded metric.
	log.Printf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":"Watchdog","Dimensions":[["CouncilID"]],"Metrics":[{"Name":"QcQuarantined","Unit":"Count"}]}]},"CouncilID":%q,"QcQuarantined":1}`,
		time.Now().UnixMilli(), councilID)
	log.Printf("council %s QUARANTINED — %d violations", councilID, len(verdict.Violations))
	for _, v := range verdict.Violations {
		log.Printf("  [%s] %s: %s", v.Severity, v.Rule, v.Detail)
	}
	return nil
}

// handleApproved generates newsletter params (sensory-deprived Gemini call),
// writes the APPROVED terminal state, and invokes the Notifier Lambda.
func (h *ValidatorHandler) handleApproved(
	ctx context.Context,
	council *councilRec,
	delibs []shared.DeliberationView,
	verdict shared.Verdict,
) error {
	cold := toColdDelibs(delibs)

	nextMeeting := h.fetchNextMeeting(ctx)
	totalCouncils, totalDelibs := h.fetchGlobalStats(ctx)

	params, err := shared.GenerateNewsletterParams(
		ctx, h.geminiDeps,
		council.Title, council.Date,
		cold,
		nextMeeting, totalCouncils, totalDelibs,
	)
	if err != nil {
		return fmt.Errorf("generate newsletter params for %s: %w", council.CouncilID, err)
	}

	paramsJSON, _ := json.Marshal(params)
	ts := time.Now().UTC().Format(time.RFC3339)

	// Pre-compute Néant rate and budget total for future baseline queries.
	neantCount := 0
	var budgetTotal int64
	for _, d := range delibs {
		if d.AnalysisData.Impacts != nil && *d.AnalysisData.Impacts == "Néant" {
			neantCount++
		}
		budgetTotal += d.BudgetImpact
	}
	neantRate := 0.0
	if len(delibs) > 0 {
		neantRate = float64(neantCount) / float64(len(delibs))
	}

	_, err = h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(h.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: council.CouncilID},
		},
		UpdateExpression: aws.String(
			"SET qc_status = :a, qc_validated_at = :ts, newsletter_params_json = :p" +
				", qc_neant_rate = :nr, qc_budget_total = :bt",
		),
		ConditionExpression: aws.String("qc_status = :validating"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":a":          &types.AttributeValueMemberS{Value: "APPROVED"},
			":ts":         &types.AttributeValueMemberS{Value: ts},
			":p":          &types.AttributeValueMemberS{Value: string(paramsJSON)},
			":nr":         &types.AttributeValueMemberN{Value: fmt.Sprintf("%.6f", neantRate)},
			":bt":         &types.AttributeValueMemberN{Value: strconv.FormatInt(budgetTotal, 10)},
			":validating": &types.AttributeValueMemberS{Value: "VALIDATING"},
		},
	})
	if err != nil {
		var ccfe *ddbtypes.ConditionalCheckFailedException
		if !errors.As(err, &ccfe) {
			return fmt.Errorf("write approved for %s: %w", council.CouncilID, err)
		}
		// Another invocation got there first; proceed to notify anyway so
		// the newsletter is not silently dropped on a race.
		log.Printf("council %s: approved state already written by a concurrent invocation", council.CouncilID)
	}

	// Refresh the public website (data.json now includes this APPROVED council).
	h.invokePublisher(ctx, council.CouncilID)
	// Send newsletter only for Conseil municipal.
	if council.Category == "" || council.Category == "Conseil municipal" {
		h.invokeNotifier(ctx, council.CouncilID, params)
		log.Printf("council %s APPROVED — invoking publisher and notifier", council.CouncilID)
	} else {
		log.Printf("council %s APPROVED (category=%q) — invoking publisher only, skipping newsletter", council.CouncilID, council.Category)
	}
	if len(verdict.Violations) > 0 {
		log.Printf("  (%d WARN violations logged, not blocking)", len(verdict.Violations))
	}
	return nil
}

// invokePublisher fires the Publisher Lambda asynchronously so it regenerates
// data.json now that this council is APPROVED and visible.
func (h *ValidatorHandler) invokePublisher(ctx context.Context, councilID string) {
	payload, _ := json.Marshal(map[string]string{"council_id": councilID})
	_, err := h.lambdaClient.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(h.publisherFnName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payload,
	})
	if err != nil {
		log.Printf("error invoking publisher for council %s: %v", councilID, err)
	}
}

// invokeNotifier fires the Notifier Lambda asynchronously with pre-generated
// newsletter params embedded in the event payload.
func (h *ValidatorHandler) invokeNotifier(ctx context.Context, councilID string, params *shared.NewsletterParams) {
	payload, _ := json.Marshal(map[string]interface{}{
		"council_id":        councilID,
		"newsletter_params": params,
	})
	_, err := h.lambdaClient.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(h.notifierFnName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payload,
	})
	if err != nil {
		log.Printf("error invoking notifier for council %s: %v", councilID, err)
	}
}

// ── Self-heal ─────────────────────────────────────────────────────────────────

// maxHealAttempts is the inclusive upper bound on qc_attempts before self-heal
// is disabled. Councils that reach this threshold stay QUARANTINED permanently
// and require manual review.
const maxHealAttempts = 3

// maybeHeal re-enqueues deliberation PDFs for fresh Gemini analysis when a
// council has been QUARANTINED but has not yet exhausted its retry budget.
//
// Steps (best-effort — individual errors are logged, not returned):
//  1. Delete all existing deliberation records so workers can re-insert them
//     and the transactional counted-marker is reset.
//  2. Reset the council row: REMOVE qc_status, SET processed_pdfs = 0.
//  3. Send one SQS message per PDF so the worker Lambda re-processes them.
//
// If sqsClient or sqsQueueURL is unset (e.g. in tests or staging), heal is
// silently skipped — the council remains QUARANTINED.
func (h *ValidatorHandler) maybeHeal(ctx context.Context, council *councilRec, delibs []deliberationRec) {
	if h.sqsClient == nil || h.sqsQueueURL == "" {
		return
	}
	if council.QcAttempts >= maxHealAttempts {
		log.Printf("council %s exhausted %d heal attempts — staying QUARANTINED", council.CouncilID, council.QcAttempts)
		return
	}

	log.Printf("council %s QUARANTINED after %d attempt(s) — scheduling re-analysis", council.CouncilID, council.QcAttempts)

	// 1. Delete deliberation records so workers write fresh analysis.
	for _, d := range delibs {
		if _, err := h.ddb.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(h.deliberationsTable),
			Key: map[string]types.AttributeValue{
				"id": &types.AttributeValueMemberS{Value: d.ID},
			},
		}); err != nil {
			log.Printf("warn: heal delete deliberation %s: %v", d.ID, err)
		}
	}

	// 2. Reset council: clear qc_status so worker can re-claim; reset counter.
	if _, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(h.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: council.CouncilID},
		},
		UpdateExpression: aws.String("REMOVE qc_status SET processed_pdfs = :zero"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":zero": &types.AttributeValueMemberN{Value: "0"},
		},
	}); err != nil {
		log.Printf("error resetting council %s for heal: %v", council.CouncilID, err)
		return // do not re-enqueue if state reset failed
	}

	// 3. Re-enqueue each PDF.
	enqueued := 0
	for _, d := range delibs {
		if d.PDFURL == "" {
			continue
		}
		body, _ := json.Marshal(map[string]interface{}{
			"council_id": council.CouncilID,
			"pdf_title":  d.Title,
			"pdf_url":    d.PDFURL,
			"total_pdfs": council.TotalPdfs,
		})
		if _, err := h.sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl:    aws.String(h.sqsQueueURL),
			MessageBody: aws.String(string(body)),
		}); err != nil {
			log.Printf("warn: heal re-enqueue PDF %s: %v", d.PDFURL, err)
		} else {
			enqueued++
		}
	}
	log.Printf("council %s heal: re-enqueued %d/%d PDFs", council.CouncilID, enqueued, len(delibs))
}

// ── DynamoDB helpers ──────────────────────────────────────────────────────────

func (h *ValidatorHandler) fetchCouncil(ctx context.Context, councilID string) (*councilRec, error) {
	out, err := h.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(h.councilsTable),
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

func (h *ValidatorHandler) fetchDeliberations(ctx context.Context, councilID string) ([]deliberationRec, error) {
	items, err := shared.PaginateQuery(ctx, h.ddb, &dynamodb.QueryInput{
		TableName:              aws.String(h.deliberationsTable),
		IndexName:              aws.String("council_id-index"),
		KeyConditionExpression: aws.String("council_id = :cid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":cid": &types.AttributeValueMemberS{Value: councilID},
		},
	})
	if err != nil {
		return nil, err
	}
	var recs []deliberationRec
	if err := attributevalue.UnmarshalListOfMaps(items, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

func (h *ValidatorHandler) fetchNextMeeting(ctx context.Context) string {
	out, err := h.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(h.councilsTable),
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

func (h *ValidatorHandler) fetchGlobalStats(ctx context.Context) (councils int, delibs int) {
	if n, err := shared.PaginateScanCount(ctx, h.ddb, &dynamodb.ScanInput{
		TableName:        aws.String(h.councilsTable),
		FilterExpression: aws.String("NOT begins_with(council_id, :meta)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":meta": &types.AttributeValueMemberS{Value: "metadata#"},
		},
		Select: types.SelectCount,
	}); err == nil {
		councils = int(n)
	}
	if n, err := shared.PaginateScanCount(ctx, h.ddb, &dynamodb.ScanInput{
		TableName: aws.String(h.deliberationsTable),
		Select:    types.SelectCount,
	}); err == nil {
		delibs = int(n)
	}
	return councils, delibs
}

// computeBaseline scans APPROVED councils for their pre-stored Néant rate and
// budget total and passes them to shared.ComputeBaseline. Best-effort: errors
// are logged by the caller and cause the z-score (S3) to be skipped, not panicked.
func (h *ValidatorHandler) computeBaseline(ctx context.Context) (shared.Baseline, error) {
	items, err := shared.PaginateScan(ctx, h.ddb, &dynamodb.ScanInput{
		TableName:        aws.String(h.councilsTable),
		FilterExpression: aws.String("qc_status = :approved AND attribute_exists(qc_neant_rate)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":approved": &types.AttributeValueMemberS{Value: "APPROVED"},
		},
		ProjectionExpression: aws.String("qc_neant_rate, qc_budget_total"),
	})
	if err != nil {
		return shared.Baseline{}, err
	}
	var neantRates []float64
	var budgets []int64
	for _, item := range items {
		if v, ok := item["qc_neant_rate"].(*types.AttributeValueMemberN); ok {
			if r, err := strconv.ParseFloat(v.Value, 64); err == nil {
				neantRates = append(neantRates, r)
			}
		}
		if v, ok := item["qc_budget_total"].(*types.AttributeValueMemberN); ok {
			if b, err := strconv.ParseInt(v.Value, 10, 64); err == nil {
				budgets = append(budgets, b)
			}
		}
	}
	return shared.ComputeBaseline(neantRates, budgets), nil
}

// ── Projection helpers ────────────────────────────────────────────────────────

// toDeliberationViews projects DynamoDB records into the view consumed by the
// QC engine. Every field needed by D1–D10 and S1–S6 is populated here.
func toDeliberationViews(recs []deliberationRec) []shared.DeliberationView {
	views := make([]shared.DeliberationView, len(recs))
	for i, r := range recs {
		breakdown := make([]shared.QcBudgetBreakdownItem, len(r.BudgetBreakdown))
		for j, b := range r.BudgetBreakdown {
			breakdown[j] = shared.QcBudgetBreakdownItem{
				TopicTag: b.TopicTag,
				Label:    b.Label,
				Amount:   b.Amount,
			}
		}
		views[i] = shared.DeliberationView{
			ID:      r.ID,
			Title:   r.Title,
			TopicTag: r.TopicTag,
			Summary: r.Summary,
			AnalysisData: shared.QcAnalysisData{
				Contexte: r.AnalysisData.Contexte,
				Decision: r.AnalysisData.Decision,
				Impacts:  r.AnalysisData.Impacts,
			},
			BudgetImpact:    r.BudgetImpact,
			BudgetType:      r.BudgetType,
			BudgetBreakdown: breakdown,
			ClimateImpact:   r.ClimateImpact,
			HasVote:         r.HasVote,
			VotePour:        r.VotePour,
			VoteContre:      r.VoteContre,
			VoteAbstention:  r.VoteAbst,
			Disagreements:   r.Disagreements,
			IsSubstantial:   r.IsSubstantial,
		}
	}
	return views
}

// toColdDelibs builds the whitelist ColdDeliberation slice for newsletter
// generation. No prose fields — sensory deprivation enforced here.
func toColdDelibs(delibs []shared.DeliberationView) []shared.ColdDeliberation {
	cold := make([]shared.ColdDeliberation, len(delibs))
	for i, d := range delibs {
		cold[i] = shared.ColdDeliberation{
			Title:           d.Title,
			TopicTag:        d.TopicTag,
			BudgetImpact:    d.BudgetImpact,
			BudgetType:      d.BudgetType,
			HasVote:         d.HasVote,
			Pour:            d.VotePour,
			Contre:          d.VoteContre,
			Abstention:      d.VoteAbstention,
			ClimateImpact:   d.ClimateImpact,
			IsSubstantial:   d.IsSubstantial,
			HasDisagreement: d.Disagreements != nil && *d.Disagreements != "",
		}
	}
	return cold
}
