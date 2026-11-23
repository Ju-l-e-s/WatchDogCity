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
	"github.com/watchdog/shared"
)

// errAlreadyClaimed is returned when qc_status is not PENDING (another
// invocation claimed the slot, or the council is already in a terminal state).
var errAlreadyClaimed = errors.New("council already claimed for validation")

// ── Input event ───────────────────────────────────────────────────────────────

type ValidatorEvent struct {
	CouncilID         string `json:"council_id"`
	RefreshNewsletter bool   `json:"refresh_newsletter,omitempty"`
}

// ── Interfaces ────────────────────────────────────────────────────────────────

type ddbAPI interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type lambdaAPI interface {
	Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// ── Deps ──────────────────────────────────────────────────────────────────────

type ValidatorHandler struct {
	ddb                ddbAPI
	lambdaClient       lambdaAPI
	councilsTable      string
	deliberationsTable string
	publisherFnName    string
	notifierFnName     string
	geminiDeps         shared.GeminiDeps
	cfg                shared.QcConfig
}

// ── DynamoDB record types ─────────────────────────────────────────────────────

type councilRec struct {
	CouncilID            string   `dynamodbav:"council_id"`
	Title                string   `dynamodbav:"title"`
	Category             string   `dynamodbav:"category"`
	Date                 string   `dynamodbav:"date"`
	TotalPdfs            int      `dynamodbav:"total_pdfs"`
	ProcessedPdfs        int      `dynamodbav:"processed_pdfs"`
	QcStatus             string   `dynamodbav:"qc_status"`
	QcAttempts           int      `dynamodbav:"qc_attempts"`
	ExpectedPDFIDs       []string `dynamodbav:"expected_pdf_ids"`
	DispatchPending      bool     `dynamodbav:"qc_dispatch_pending"`
	NewsletterParamsJSON string   `dynamodbav:"newsletter_params_json"`
	CompletenessVersion  int      `dynamodbav:"qc_completeness_version"`
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
	ID              string                `dynamodbav:"id"`
	Counted         bool                  `dynamodbav:"counted"`
	FactCheckModel  string                `dynamodbav:"fact_check_model"`
	SourceFacts     *shared.SourceFacts   `dynamodbav:"source_facts"`
	FactCheckedAt   string                `dynamodbav:"fact_checked_at"`
	Title           string                `dynamodbav:"title"`
	TopicTag        string                `dynamodbav:"topic_tag"`
	PDFURL          string                `dynamodbav:"pdf_url"`
	Summary         string                `dynamodbav:"summary"`
	AnalysisData    analysisDataRec       `dynamodbav:"analysis_data"`
	BudgetImpact    int64                 `dynamodbav:"budget_impact"`
	BudgetType      string                `dynamodbav:"budget_type"`
	BudgetNote      string                `dynamodbav:"budget_note"`
	BudgetBreakdown []budgetBreakdownRec  `dynamodbav:"budget_breakdown"`
	BudgetChanges   []shared.BudgetChange `dynamodbav:"budget_changes"`
	ClimateImpact   string                `dynamodbav:"climate_impact"`
	HasVote         bool                  `dynamodbav:"has_vote"`
	VotePour        *int                  `dynamodbav:"vote_pour"`
	VoteContre      *int                  `dynamodbav:"vote_contre"`
	VoteAbst        *int                  `dynamodbav:"vote_abstention"`
	Disagreements   *string               `dynamodbav:"disagreements"`
	IsSubstantial   bool                  `dynamodbav:"is_substantial"`
}

// ── Handler ───────────────────────────────────────────────────────────────────

func (h *ValidatorHandler) HandleRequest(ctx context.Context, event ValidatorEvent) (resultErr error) {
	councilID := event.CouncilID
	if event.RefreshNewsletter {
		return h.refreshApprovedNewsletter(ctx, councilID)
	}

	// Atomic claim: PENDING → VALIDATING. Fails closed if council is in any
	// other state (already validated, already validating, or terminal).
	if err := h.claimValidating(ctx, councilID); err != nil {
		if errors.Is(err, errAlreadyClaimed) {
			var conflict *types.ConditionalCheckFailedException
			if errors.As(err, &conflict) && conflict.Item["pdf_review_held"] == nil && conflict.Item["pdf_reviews_pending"] == nil {
				var approved councilRec
				if err := attributevalue.UnmarshalMap(conflict.Item, &approved); err != nil {
					return err
				}
				if approved.QcStatus == "APPROVED" && approved.DispatchPending {
					var params shared.NewsletterParams
					if err := json.Unmarshal([]byte(approved.NewsletterParamsJSON), &params); err != nil {
						return err
					}
					return h.dispatchApproved(ctx, &approved, &params)
				}
			}
			log.Printf("council %s already claimed for validation — skipping", councilID)
			return nil
		}
		return fmt.Errorf("claim validating for %s: %w", councilID, err)
	}

	defer func() {
		if resultErr != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := h.releaseValidating(cleanup, councilID); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
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
		CouncilID:        councilID,
		TotalPdfs:        int64(council.TotalPdfs),
		ProcessedPdfs:    int64(council.ProcessedPdfs),
		Date:             council.Date,
		ExpectedPDFIDs:   council.ExpectedPDFIDs,
		RequireFactCheck: true,
	}
	deliberationViews := toDeliberationViews(delibs)

	violations := append(
		shared.ValidateDeterministic(councilView, deliberationViews),
		shared.ValidateStatistical(councilView, deliberationViews, baseline, h.cfg)...,
	)
	verdict := shared.Decide(violations)

	if verdict.Status == "QUARANTINED" {
		return h.handleQuarantine(ctx, councilID, verdict)
	}
	return h.handleApproved(ctx, council, deliberationViews, verdict)
}

// refreshApprovedNewsletter reruns the same QC and newsletter generator for an
// already approved, unsent council. It preserves APPROVED and never invokes the
// Publisher or Notifier; a separate Notifier run can use the refreshed params.
func (h *ValidatorHandler) refreshApprovedNewsletter(ctx context.Context, councilID string) error {
	council, err := h.fetchCouncil(ctx, councilID)
	if err != nil {
		return fmt.Errorf("fetch council %s: %w", councilID, err)
	}
	if council.QcStatus != "APPROVED" || council.Category != "Conseil municipal" {
		return fmt.Errorf("newsletter refresh requires an APPROVED Conseil municipal")
	}
	delibs, err := h.fetchDeliberations(ctx, councilID)
	if err != nil {
		return fmt.Errorf("fetch deliberations for %s: %w", councilID, err)
	}
	baseline, err := h.computeBaseline(ctx)
	if err != nil {
		log.Printf("warn: compute baseline failed, running without z-score: %v", err)
		baseline = shared.Baseline{}
	}
	views := toDeliberationViews(delibs)
	view := shared.CouncilView{
		CouncilID: councilID, TotalPdfs: int64(council.TotalPdfs),
		ProcessedPdfs: int64(council.ProcessedPdfs), Date: council.Date,
		ExpectedPDFIDs:   council.ExpectedPDFIDs,
		RequireFactCheck: council.CompletenessVersion >= 1,
	}
	violations := append(shared.ValidateDeterministic(view, views),
		shared.ValidateStatistical(view, views, baseline, h.cfg)...)
	if shared.Decide(violations).Status != "APPROVED" {
		return fmt.Errorf("newsletter refresh blocked by QC for %s", councilID)
	}
	params, err := h.generateNewsletterParams(ctx, council, views)
	if err != nil {
		return err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal newsletter params for %s: %w", councilID, err)
	}
	_, err = h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(h.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression:    aws.String("SET newsletter_params_json = :params"),
		ConditionExpression: aws.String("qc_status = :approved AND attribute_not_exists(newsletter_sent_at)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":params":   &types.AttributeValueMemberS{Value: string(paramsJSON)},
			":approved": &types.AttributeValueMemberS{Value: "APPROVED"},
		},
	})
	if err != nil {
		return fmt.Errorf("store refreshed newsletter for %s: %w", councilID, err)
	}
	log.Printf("newsletter params refreshed from approved council %s", councilID)
	return nil
}

// claimValidating atomically transitions qc_status PENDING → VALIDATING and
// increments qc_attempts. Returns errAlreadyClaimed for any non-PENDING state.
func (h *ValidatorHandler) claimValidating(ctx context.Context, councilID string) error {
	until, ok := ctx.Deadline()
	if !ok {
		until = time.Now().Add(10 * time.Minute)
	}
	_, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(h.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
		UpdateExpression:                    aws.String("SET qc_status = :validating, qc_validation_until = :until ADD qc_attempts :one"),
		ConditionExpression:                 aws.String("(qc_status = :pending OR (qc_status = :validating AND qc_validation_until < :now)) AND attribute_not_exists(pdf_review_held) AND attribute_not_exists(pdf_reviews_pending)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":validating": &types.AttributeValueMemberS{Value: "VALIDATING"},
			":pending":    &types.AttributeValueMemberS{Value: "PENDING"},
			":one":        &types.AttributeValueMemberN{Value: "1"},
			":now":        &types.AttributeValueMemberN{Value: fmt.Sprint(time.Now().UnixMilli())},
			":until":      &types.AttributeValueMemberN{Value: fmt.Sprint(until.UnixMilli())},
		},
	})
	if err != nil {
		var ccfe *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return errors.Join(errAlreadyClaimed, err)
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
	params, err := h.generateNewsletterParams(ctx, council, delibs)
	if err != nil {
		var factErr *shared.NewsletterFactCheckError
		if errors.As(err, &factErr) {
			violations := make([]shared.Violation, 0, len(factErr.Issues))
			for _, issue := range factErr.Issues {
				rule := "N1_UNSUPPORTED_NEWSLETTER_CLAIM"
				switch issue.Kind {
				case "CONTRADICTION":
					rule = "N1_NEWSLETTER_CONTRADICTION"
				case "OMISSION":
					rule = "N1_NEWSLETTER_ESSENTIAL_OMISSION"
				}
				violations = append(violations, shared.Violation{
					Rule: rule, Severity: shared.SeverityHigh,
					Field: issue.Location, Detail: issue.Claim + " — " + issue.Reason,
				})
			}
			if len(violations) == 0 {
				violations = append(violations, shared.Violation{
					Rule: "N1_INCONCLUSIVE_NEWSLETTER_FACT_CHECK", Severity: shared.SeverityHigh,
					Detail: "La vérification factuelle n'a pas rendu de verdict exploitable.",
				})
			}
			return h.handleQuarantine(ctx, council.CouncilID, shared.Decide(violations))
		}

		return err
	}

	paramsJSON, _ := json.Marshal(params)
	completenessVersion := 1
	if council.ExpectedPDFIDs != nil {
		completenessVersion = 2
	}
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
				", qc_completeness_version = :cv" +
				", qc_neant_rate = :nr, qc_budget_total = :bt, qc_dispatch_pending = :yes REMOVE qc_validation_until",
		),
		ConditionExpression:                 aws.String("qc_status = :validating AND attribute_not_exists(pdf_review_held) AND attribute_not_exists(pdf_reviews_pending)"),
		ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":yes":        &types.AttributeValueMemberBOOL{Value: true},
			":a":          &types.AttributeValueMemberS{Value: "APPROVED"},
			":ts":         &types.AttributeValueMemberS{Value: ts},
			":p":          &types.AttributeValueMemberS{Value: string(paramsJSON)},
			":cv":         &types.AttributeValueMemberN{Value: strconv.Itoa(completenessVersion)},
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
		return nil // The winner dispatches its persisted approval.
	}
	return h.dispatchApproved(ctx, council, params)
}

func (h *ValidatorHandler) dispatchApproved(ctx context.Context, council *councilRec, params *shared.NewsletterParams) error {
	if err := h.invokePublisher(ctx, council.CouncilID); err != nil {
		return err
	}
	if council.Category == "" || council.Category == "Conseil municipal" {
		if err := h.invokeNotifier(ctx, council.CouncilID, params); err != nil {
			return err
		}
	}
	_, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(h.councilsTable),
		Key:                       map[string]types.AttributeValue{"council_id": &types.AttributeValueMemberS{Value: council.CouncilID}},
		UpdateExpression:          aws.String("REMOVE qc_dispatch_pending"),
		ConditionExpression:       aws.String("qc_status = :approved"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":approved": &types.AttributeValueMemberS{Value: "APPROVED"}},
	})
	return err
}

func (h *ValidatorHandler) releaseValidating(ctx context.Context, councilID string) error {
	_, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(h.councilsTable),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: councilID},
		},
		UpdateExpression:    aws.String("SET qc_status = :pending REMOVE qc_validation_until"),
		ConditionExpression: aws.String("qc_status = :validating"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending":    &types.AttributeValueMemberS{Value: "PENDING"},
			":validating": &types.AttributeValueMemberS{Value: "VALIDATING"},
		},
	})
	var lost *types.ConditionalCheckFailedException
	if errors.As(err, &lost) {
		return nil
	}
	return err
}

func (h *ValidatorHandler) generateNewsletterParams(ctx context.Context, council *councilRec, delibs []shared.DeliberationView) (*shared.NewsletterParams, error) {
	nextMeeting := h.fetchNextMeeting(ctx)
	totalCouncils, totalDelibs := h.fetchGlobalStats(ctx)
	params, err := shared.GenerateNewsletterParams(ctx, h.geminiDeps,
		council.Title, council.Date, toColdDelibs(delibs), nextMeeting, totalCouncils, totalDelibs)
	if err != nil {
		return nil, fmt.Errorf("generate newsletter params for %s: %w", council.CouncilID, err)
	}
	return params, nil
}

// invokePublisher fires the Publisher Lambda asynchronously so it regenerates
// data.json now that this council is APPROVED and visible.
func (h *ValidatorHandler) invokePublisher(ctx context.Context, councilID string) error {
	payload, _ := json.Marshal(map[string]string{"council_id": councilID})
	_, err := h.lambdaClient.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(h.publisherFnName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payload,
	})
	if err != nil {
		return fmt.Errorf("invoke publisher for %s: %w", councilID, err)
	}
	return nil
}

// invokeNotifier fires the Notifier Lambda asynchronously with pre-generated
// newsletter params embedded in the event payload.
func (h *ValidatorHandler) invokeNotifier(ctx context.Context, councilID string, params *shared.NewsletterParams) error {
	if h.notifierFnName == "" || h.notifierFnName == "disabled" || h.notifierFnName == "placeholder" {
		log.Printf("notifier invocation skipped: notifier is disabled or placeholder (%s)", h.notifierFnName)
		return nil
	}
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
		return fmt.Errorf("invoke notifier for %s: %w", councilID, err)
	}
	return nil
}

// ── DynamoDB helpers ──────────────────────────────────────────────────────────

func (h *ValidatorHandler) fetchCouncil(ctx context.Context, councilID string) (*councilRec, error) {
	out, err := h.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		ConsistentRead: aws.Bool(true),
		TableName:      aws.String(h.councilsTable),
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
			ID:             r.ID,
			PDFURL:         r.PDFURL,
			Counted:        r.Counted,
			FactCheckModel: r.FactCheckModel,
			FactCheckedAt:  r.FactCheckedAt,
			SourceFacts:    r.SourceFacts,
			Title:          r.Title,
			TopicTag:       r.TopicTag,
			Summary:        r.Summary,
			AnalysisData: shared.QcAnalysisData{
				Contexte: r.AnalysisData.Contexte,
				Decision: r.AnalysisData.Decision,
				Impacts:  r.AnalysisData.Impacts,
			},
			BudgetImpact:    r.BudgetImpact,
			BudgetType:      r.BudgetType,
			BudgetNote:      r.BudgetNote,
			BudgetBreakdown: breakdown,
			BudgetChanges:   r.BudgetChanges,
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
// generation.
func toColdDelibs(delibs []shared.DeliberationView) []shared.ColdDeliberation {
	cold := make([]shared.ColdDeliberation, len(delibs))
	for i, d := range delibs {
		impactsVal := ""
		if d.AnalysisData.Impacts != nil {
			impactsVal = *d.AnalysisData.Impacts
		}
		decisionVal := ""
		if d.AnalysisData.Decision != nil {
			decisionVal = *d.AnalysisData.Decision
		}

		cold[i] = shared.ColdDeliberation{
			ID:              d.ID,
			Title:           d.Title,
			TopicTag:        d.TopicTag,
			BudgetImpact:    d.BudgetImpact,
			BudgetType:      d.BudgetType,
			BudgetNote:      d.BudgetNote,
			BudgetChanges:   d.BudgetChanges,
			HasVote:         d.HasVote,
			Pour:            d.VotePour,
			Contre:          d.VoteContre,
			Abstention:      d.VoteAbstention,
			ClimateImpact:   d.ClimateImpact,
			IsSubstantial:   d.IsSubstantial || d.BudgetImpact >= 5000,
			HasDisagreement: d.SourceFacts.HasKind("DISAGREEMENT"),
			SourceFacts:     d.SourceFacts,
			Summary:         d.Summary,
			Decision:        decisionVal,
			Impacts:         impactsVal,
		}
	}
	return cold
}
