package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	lambdaSvc "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdaTypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// publisherLockKey is the councils-table metadata row used as a short-lived
// mutex so concurrent publishers can't clobber each other's data.json write.
const publisherLockKey = "metadata#publisher_lock"

type PublisherEvent struct {
	CouncilID string `json:"council_id"`
}

type CouncilRecord struct {
	CouncilID string          `dynamodbav:"council_id"`
	Category  string          `dynamodbav:"category"`
	Date      string          `dynamodbav:"date"`
	Title     string          `dynamodbav:"title"`
	Summary   string          `dynamodbav:"summary"`
	SourceURL string          `dynamodbav:"source_url"`
	TotalPDFs int             `dynamodbav:"total_pdfs"`
	Processed int             `dynamodbav:"processed_pdfs"`
	Analysis  CouncilAnalysis `dynamodbav:"analysis"`
}

type CouncilAnalysis struct {
	BudgetImpact int64  `dynamodbav:"budget_impact" json:"budget_impact"`
	BudgetLabel  string `dynamodbav:"budget_label" json:"budget_label"`
	VoteClimat   string `dynamodbav:"vote_climat" json:"vote_climat"`
	VoteSummary  string `dynamodbav:"vote_summary" json:"vote_summary"`
	VotesPour    int    `dynamodbav:"votes_pour" json:"votes_pour"`
	VotesContre  int    `dynamodbav:"votes_contre" json:"votes_contre"`
}

type BudgetBreakdownItem struct {
	TopicTag string `dynamodbav:"topic_tag" json:"topic_tag"`
	Label    string `dynamodbav:"label"     json:"label"`
	Amount   int64  `dynamodbav:"amount"    json:"amount"`
}

type DeliberationRecord struct {
	ID              string                `dynamodbav:"id"`
	CouncilID       string                `dynamodbav:"council_id"`
	Title           string                `dynamodbav:"title"`
	TopicTag        string                `dynamodbav:"topic_tag"`
	PDFURL          string                `dynamodbav:"pdf_url"`
	Summary         string                `dynamodbav:"summary"`
	IsSubstantial   bool                  `dynamodbav:"is_substantial"`
	Acronyms        map[string]string     `dynamodbav:"acronyms"`
	AnalysisData    AnalysisData          `dynamodbav:"analysis_data"`
	BudgetImpact    int64                 `dynamodbav:"budget_impact"`
	BudgetType      string                `dynamodbav:"budget_type"`
	BudgetBreakdown []BudgetBreakdownItem `dynamodbav:"budget_breakdown"`
	HasVote         bool                  `dynamodbav:"has_vote"`
	VotePour        *int                  `dynamodbav:"vote_pour"`
	VoteContre      *int                  `dynamodbav:"vote_contre"`
	VoteAbstention  *int                  `dynamodbav:"vote_abstention"`
	Disagreements   *string               `dynamodbav:"disagreements"`
	ProcessedAt     string                `dynamodbav:"processed_at"`
}

// ── JSON output format ────────────────────────────────────────────────────────

type DataJSON struct {
	GeneratedAt     string          `json:"generated_at"`
	NextCouncilDate string          `json:"next_council_date"`
	Councils        []CouncilOutput `json:"councils"`
}

type CouncilOutput struct {
	CouncilID     string               `json:"id"`
	Category      string               `json:"category"`
	Date          string               `json:"date"`
	Title         string               `json:"title"`
	Summary       string               `json:"summary"`
	SourceURL     string               `json:"source_url"`
	Analysis      CouncilAnalysis      `json:"analysis"`
	Deliberations []DeliberationOutput `json:"deliberations"`
}

type DeliberationOutput struct {
	ID              string                `json:"id"`
	Title           string                `json:"title"`
	TopicTag        string                `json:"topic_tag"`
	PDFURL          string                `json:"pdf_url"`
	Summary         string                `json:"summary"`
	IsSubstantial   bool                  `json:"is_substantial"`
	Acronyms        map[string]string     `json:"acronyms"`
	AnalysisData    AnalysisData          `json:"analysis_data"`
	BudgetImpact    int64                 `json:"budget_impact"`
	BudgetType      string                `json:"budget_type"`
	BudgetBreakdown []BudgetBreakdownItem `json:"budget_breakdown"`
	Vote            VoteCount             `json:"vote"`
	Disagreements   *string               `json:"disagreements"`
}

type AnalysisData struct {
	Contexte       *string `json:"contexte"`
	Decision       *string `json:"decision"`
	Impacts        *string `json:"impacts"`
	PointsDebattus *string `json:"points_debattus"`
}

type VoteCount struct {
	HasVote    bool `json:"has_vote"`
	Pour       *int `json:"pour"`
	Contre     *int `json:"contre"`
	Abstention *int `json:"abstention"`
}

// ── Business logic (pure, testable) ──────────────────────────────────────────

func buildDataJSON(ctx context.Context, ddb *dynamodb.Client, councils []CouncilRecord, delibs map[string][]DeliberationRecord) (*DataJSON, error) {
	out := &DataJSON{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		NextCouncilDate: fetchNextCouncilDate(ctx, ddb),
	}
	for _, c := range councils {
		analysis := c.Analysis
		// Budget impact = max of budget-tagged deliberations, or sum of other items to avoid double-counting
		var hasBudgetTopic bool
		var maxBudgetTopic int64
		var otherTopicsSum int64
		for _, d := range delibs[c.CouncilID] {
			if d.TopicTag == "Budget" {
				hasBudgetTopic = true
				if d.BudgetImpact > maxBudgetTopic {
					maxBudgetTopic = d.BudgetImpact
				}
			} else {
				otherTopicsSum += d.BudgetImpact
			}
		}
		if hasBudgetTopic {
			analysis.BudgetImpact = maxBudgetTopic
		} else {
			analysis.BudgetImpact = otherTopicsSum
		}

		co := CouncilOutput{
			CouncilID: c.CouncilID,
			Category:  c.Category,
			Date:      c.Date,
			Title:     c.Title,
			Summary:   c.Summary,
			SourceURL: c.SourceURL,
			Analysis:  analysis,
		}
		for _, d := range delibs[c.CouncilID] {
			co.Deliberations = append(co.Deliberations, DeliberationOutput{
				ID:              d.ID,
				Title:           d.Title,
				TopicTag:        d.TopicTag,
				PDFURL:          d.PDFURL,
				Summary:         d.Summary,
				IsSubstantial:   d.IsSubstantial,
				Acronyms:        d.Acronyms,
				AnalysisData:    d.AnalysisData,
				BudgetImpact:    d.BudgetImpact,
				BudgetType:      d.BudgetType,
				BudgetBreakdown: d.BudgetBreakdown,
				Vote: VoteCount{
					HasVote:    d.HasVote || d.VotePour != nil || d.VoteContre != nil || d.VoteAbstention != nil,
					Pour:       d.VotePour,
					Contre:     d.VoteContre,
					Abstention: d.VoteAbstention,
				},
				Disagreements: d.Disagreements,
			})
		}
		out.Councils = append(out.Councils, co)
	}
	return out, nil
}

func fetchNextCouncilDate(ctx context.Context, ddb *dynamodb.Client) string {
	if ddb == nil {
		return ""
	}
	out, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
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

// ── Lambda Handler ───────────────────────────────────────────────────────────

func HandleRequest(ctx context.Context, event PublisherEvent) error {
	// Serialize publishers behind a short-lived DynamoDB lock. data.json is a
	// single S3 object overwritten in full; two concurrent publishers would
	// otherwise race and the slower write would silently win (last-write-wins).
	owner := lambdaRequestID(ctx)
	lockTTL := time.Now().Add(2 * time.Minute).Unix()
	_, lerr := ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: publisherLockKey},
		},
		UpdateExpression:    aws.String("SET lock_ttl = :ttl, lock_owner = :owner"),
		ConditionExpression: aws.String("attribute_not_exists(lock_ttl) OR lock_ttl < :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":ttl":   &types.AttributeValueMemberN{Value: strconv.FormatInt(lockTTL, 10)},
			":now":   &types.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Unix(), 10)},
			":owner": &types.AttributeValueMemberS{Value: owner},
		},
	})
	if lerr != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(lerr, &ccfe) {
			log.Printf("publisher lock held by another instance, skipping (it will publish the latest state)")
			return nil
		}
		return fmt.Errorf("acquire publisher lock: %w", lerr)
	}
	defer releasePublisherLock(ctx, owner)

	// Build all councils for the full data.json
	allCouncils, allDelibs, err := fetchAllData(ctx, ddb)
	if err != nil {
		return fmt.Errorf("fetch all data: %w", err)
	}

	data, err := buildDataJSON(ctx, ddb, allCouncils, allDelibs)
	if err != nil {
		return err
	}

	// Upload data.json to S3
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal data.json: %w", err)
	}
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(os.Getenv("WEBSITE_BUCKET")),
		Key:          aws.String("data.json"),
		Body:         bytes.NewReader(jsonBytes),
		ContentType:  aws.String("application/json"),
		CacheControl: aws.String("public, max-age=60, must-revalidate"),
	})
	if err != nil {
		return fmt.Errorf("upload data.json: %w", err)
	}
	log.Printf("data.json uploaded (%d bytes)", len(jsonBytes))

	// A missing council_id means the Notifier would fetch council "" → 404 and
	// silently drop the newsletter. Bail before invoking; data.json is already
	// published, which is the side effect that matters.
	if event.CouncilID == "" {
		log.Printf("notifier skip: empty council_id (probably invoked by aggregator without payload)")
		return nil
	}

	// Trigger newsletter Notifier asynchronously — last instruction, after S3.
	if fn := os.Getenv("NOTIFIER_FUNCTION_NAME"); fn != "" && fn != "disabled" && fn != "placeholder" {
		notifierPayload, _ := json.Marshal(map[string]string{"council_id": event.CouncilID})
		_, err := lambdaClient.Invoke(ctx, &lambdaSvc.InvokeInput{
			FunctionName:   aws.String(fn),
			InvocationType: lambdaTypes.InvocationTypeEvent,
			Payload:        notifierPayload,
		})
		if err != nil {
			log.Printf("warn: could not invoke notifier for council %s: %v", event.CouncilID, err)
			// Emit a CloudWatch EMF metric so a failed fan-out is alarmable
			// rather than buried in a warn log.
			log.Printf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":"Watchdog","Dimensions":[["FunctionName"]],"Metrics":[{"Name":"NotifierInvokeFailed","Unit":"Count"}]}]},"FunctionName":"publisher","NotifierInvokeFailed":1}`,
				time.Now().UnixMilli())
		} else {
			log.Printf("notifier invoked async for council %s", event.CouncilID)
		}
	} else if fn == "disabled" || fn == "placeholder" {
		log.Printf("notifier invocation skipped: notifier is disabled or placeholder (%s)", fn)
	}

	return nil
}

// lambdaRequestID returns the current Lambda invocation id, used as the lock
// owner token. Falls back to a random id outside the Lambda runtime.
func lambdaRequestID(ctx context.Context) string {
	if lc, ok := lambdacontext.FromContext(ctx); ok && lc.AwsRequestID != "" {
		return lc.AwsRequestID
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("publisher-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// releasePublisherLock clears the lock only if we still own it. If it expired
// and was re-acquired by another instance, the owner guard fails and we leave
// it alone.
func releasePublisherLock(ctx context.Context, owner string) {
	_, err := ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: publisherLockKey},
		},
		UpdateExpression:    aws.String("REMOVE lock_ttl, lock_owner"),
		ConditionExpression: aws.String("lock_owner = :owner"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":owner": &types.AttributeValueMemberS{Value: owner},
		},
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return
		}
		log.Printf("warn: could not release publisher lock: %v", err)
	}
}

func fetchAllData(ctx context.Context, ddb *dynamodb.Client) ([]CouncilRecord, map[string][]DeliberationRecord, error) {
	// Scan councils with pagination
	var councils []CouncilRecord
	var lastKey map[string]types.AttributeValue
	for {
		cOut, err := ddb.Scan(ctx, &dynamodb.ScanInput{
			TableName:        aws.String(os.Getenv("COUNCILS_TABLE")),
			// Hard filter: only QC-approved councils appear on the public website.
			// Councils without qc_status (pre-gate) are excluded intentionally.
			FilterExpression: aws.String(
				"NOT begins_with(council_id, :metaprefix) AND qc_status = :approved",
			),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":metaprefix": &types.AttributeValueMemberS{Value: "metadata#"},
				":approved":   &types.AttributeValueMemberS{Value: "APPROVED"},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, nil, err
		}
		var batch []CouncilRecord
		if err := attributevalue.UnmarshalListOfMaps(cOut.Items, &batch); err != nil {
			return nil, nil, fmt.Errorf("unmarshal council batch: %w", err)
		}
		councils = append(councils, batch...)

		if cOut.LastEvaluatedKey == nil {
			break
		}
		lastKey = cOut.LastEvaluatedKey
	}

	// Only include Conseil municipal in the public website.
	var filtered []CouncilRecord
	for _, c := range councils {
		if c.Category == "" || c.Category == "Conseil municipal" {
			filtered = append(filtered, c)
		}
	}
	councils = filtered

	// Scan deliberations with pagination
	var delibs []DeliberationRecord
	lastKey = nil
	for {
		dOut, err := ddb.Scan(ctx, &dynamodb.ScanInput{
			TableName:         aws.String(os.Getenv("DELIBERATIONS_TABLE")),
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, nil, err
		}
		var batch []DeliberationRecord
		if err := attributevalue.UnmarshalListOfMaps(dOut.Items, &batch); err != nil {
			return nil, nil, fmt.Errorf("unmarshal deliberation batch: %w", err)
		}
		delibs = append(delibs, batch...)

		if dOut.LastEvaluatedKey == nil {
			break
		}
		lastKey = dOut.LastEvaluatedKey
	}

	// Group by council
	delibMap := make(map[string][]DeliberationRecord)
	for _, d := range delibs {
		delibMap[d.CouncilID] = append(delibMap[d.CouncilID], d)
	}

	return councils, delibMap, nil
}
