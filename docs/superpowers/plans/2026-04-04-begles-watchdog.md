# Bègles Watchdog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a serverless AWS pipeline that scrapes Bègles municipal council deliberations weekly, analyzes each PDF with Gemini 3.1 Pro, and publishes a static timeline site with newsletter subscription via SES.

**Architecture:** EventBridge triggers an Orchestrator Lambda (Go/arm64) weekly. It scrapes the deliberations page, detects new councils not yet in DynamoDB, and pushes one SQS message per PDF. Worker Lambdas (max concurrency 2) consume the queue, call Gemini 3.1 Pro, and write results to DynamoDB using a conditional PutItem for idempotency. When the last PDF of a council is processed, the Worker invokes the Publisher Lambda asynchronously. The Publisher compiles `data.json`, uploads it to S3, and sends the newsletter via SES. An API Gateway + Subscriber Lambda handles newsletter signups with double opt-in.

**Tech Stack:** AWS CDK v2 (Python), Go 1.22, Lambda arm64/Graviton (PROVIDED_AL2023), SQS + DLQ, DynamoDB (PAY_PER_REQUEST), S3 static website, SES v2, API Gateway REST, EventBridge, Secrets Manager, Gemini 3.1 Pro (`gemini-3.1-pro`), goquery, testify, Tailwind CSS (CDN)

---

## File Map

```
watchdog/
├── Makefile                          # build all lambdas + deploy helpers
├── .gitignore
├── dist/                             # compiled .zip files (gitignored)
├── cdk/
│   ├── app.py                        # CDK entry point
│   ├── cdk.json
│   ├── requirements.txt
│   └── watchdog_stack.py             # all AWS resources
├── lambdas/
│   ├── orchestrator/
│   │   ├── go.mod
│   │   ├── main.go                   # Lambda handler entry
│   │   ├── scraper.go                # HTML scraping logic
│   │   └── scraper_test.go
│   ├── worker/
│   │   ├── go.mod
│   │   ├── main.go                   # Lambda handler entry
│   │   ├── gemini.go                 # Gemini 3.1 Pro client
│   │   ├── gemini_test.go
│   │   ├── handler.go                # SQS message processing + DynamoDB
│   │   └── handler_test.go
│   ├── publisher/
│   │   ├── go.mod
│   │   ├── main.go
│   │   ├── handler.go                # DynamoDB → data.json → S3 + SES
│   │   └── handler_test.go
│   └── subscriber/
│       ├── go.mod
│       ├── main.go
│       ├── handler.go                # POST /subscribe + GET /confirm
│       └── handler_test.go
└── frontend/
    └── index.html                    # Tailwind timeline + newsletter form
```

---

## DynamoDB Schemas

**Table `watchdog-councils`** (PK: `council_id` string)
```
council_id    "conseil_municipal#2026-03-28"
category      "conseil_municipal" | "ccas" | "csc_estey" | "etablissements"
date          "2026-03-28"
title         "Délibérations du conseil municipal du 28 mars 2026"
source_url    "https://www.mairie-begles.fr/..."
total_pdfs    5
processed_pdfs 0  (incremented atomically by each Worker)
```

**Table `watchdog-deliberations`** (PK: `id` string = SHA-256 of PDF URL, hex)
```
id              "a3f2..."
council_id      "conseil_municipal#2026-03-28"
title           "D01-2026_020 Élection du Maire"
pdf_url         "https://www.mairie-begles.fr/app/uploads/2026/03/D01.pdf"
summary         "..."
vote_pour       32
vote_contre     5
vote_abstention 2
disagreements   "..." | "" (empty string = no disagreement)
processed_at    "2026-04-04T09:01:23Z"
```

**Table `watchdog-subscribers`** (PK: `email` string)
```
email        "user@example.com"
status       "pending" | "confirmed"
token        "550e8400-e29b-41d4-a716-446655440000"
created_at   "2026-04-04T09:00:00Z"
```

---

## Task 1: Project Scaffolding

**Files:**
- Create: `Makefile`
- Create: `.gitignore`
- Create: `lambdas/orchestrator/go.mod`
- Create: `lambdas/worker/go.mod`
- Create: `lambdas/publisher/go.mod`
- Create: `lambdas/subscriber/go.mod`

- [ ] **Step 1: Create .gitignore**

```
dist/
cdk/cdk.out/
cdk/.venv/
__pycache__/
*.pyc
cdk.context.json
```

- [ ] **Step 2: Create Makefile**

```makefile
.PHONY: build-orchestrator build-worker build-publisher build-subscriber build deploy test

build-orchestrator:
	cd lambdas/orchestrator && GOARCH=arm64 GOOS=linux go build -tags lambda.norpc -o bootstrap . && zip -j ../../dist/orchestrator.zip bootstrap && rm bootstrap

build-worker:
	cd lambdas/worker && GOARCH=arm64 GOOS=linux go build -tags lambda.norpc -o bootstrap . && zip -j ../../dist/worker.zip bootstrap && rm bootstrap

build-publisher:
	cd lambdas/publisher && GOARCH=arm64 GOOS=linux go build -tags lambda.norpc -o bootstrap . && zip -j ../../dist/publisher.zip bootstrap && rm bootstrap

build-subscriber:
	cd lambdas/subscriber && GOARCH=arm64 GOOS=linux go build -tags lambda.norpc -o bootstrap . && zip -j ../../dist/subscriber.zip bootstrap && rm bootstrap

build: build-orchestrator build-worker build-publisher build-subscriber

test:
	cd lambdas/orchestrator && go test ./...
	cd lambdas/worker && go test ./...
	cd lambdas/publisher && go test ./...
	cd lambdas/subscriber && go test ./...

deploy: build
	cd cdk && cdk deploy
```

- [ ] **Step 3: Init Go modules**

```bash
mkdir -p dist lambdas/orchestrator lambdas/worker lambdas/publisher lambdas/subscriber

cd lambdas/orchestrator && go mod init github.com/watchdog/orchestrator
cd lambdas/worker       && go mod init github.com/watchdog/worker
cd lambdas/publisher    && go mod init github.com/watchdog/publisher
cd lambdas/subscriber   && go mod init github.com/watchdog/subscriber
```

- [ ] **Step 4: Add dependencies to each module**

Run in each lambda directory (`orchestrator`, `worker`, `publisher`, `subscriber`):
```bash
go get github.com/aws/aws-lambda-go@latest
go get github.com/aws/aws-sdk-go-v2/config@latest
go get github.com/aws/aws-sdk-go-v2/service/dynamodb@latest
go get github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue@latest
go get github.com/stretchr/testify@latest
```

Additionally in `orchestrator`:
```bash
go get github.com/aws/aws-sdk-go-v2/service/sqs@latest
go get github.com/PuerkitoBio/goquery@latest
```

Additionally in `worker`:
```bash
go get github.com/google/generative-ai-go/genai@latest
go get google.golang.org/api/option@latest
go get github.com/aws/aws-sdk-go-v2/service/lambda@latest
```

Additionally in `publisher`:
```bash
go get github.com/aws/aws-sdk-go-v2/service/s3@latest
go get github.com/aws/aws-sdk-go-v2/service/sesv2@latest
go get github.com/aws/aws-sdk-go-v2/service/dynamodb@latest
```

Additionally in `subscriber`:
```bash
go get github.com/aws/aws-sdk-go-v2/service/sesv2@latest
go get github.com/google/uuid@latest
```

- [ ] **Step 5: Commit**

```bash
git init
git add .gitignore Makefile lambdas/
git commit -m "chore: project scaffolding with Go modules"
```

---

## Task 2: CDK Infrastructure Stack

**Files:**
- Create: `cdk/app.py`
- Create: `cdk/cdk.json`
- Create: `cdk/requirements.txt`
- Create: `cdk/watchdog_stack.py`

- [ ] **Step 1: Create CDK project files**

`cdk/requirements.txt`:
```
aws-cdk-lib>=2.140.0
constructs>=10.0.0
```

`cdk/cdk.json`:
```json
{
  "app": "python app.py",
  "context": {
    "@aws-cdk/aws-lambda:recognizeLayerVersion": true
  }
}
```

`cdk/app.py`:
```python
import aws_cdk as cdk
from watchdog_stack import WatchdogStack

app = cdk.App()
WatchdogStack(app, "WatchdogStack")
app.synth()
```

- [ ] **Step 2: Create the CDK stack**

`cdk/watchdog_stack.py`:
```python
from aws_cdk import (
    Stack, Duration, RemovalPolicy,
    aws_dynamodb as dynamodb,
    aws_s3 as s3,
    aws_sqs as sqs,
    aws_lambda as lambda_,
    aws_lambda_event_sources as lambda_events,
    aws_events as events,
    aws_events_targets as targets,
    aws_apigateway as apigw,
    aws_iam as iam,
    aws_secretsmanager as secretsmanager,
)
from constructs import Construct


class WatchdogStack(Stack):
    def __init__(self, scope: Construct, id: str, **kwargs):
        super().__init__(scope, id, **kwargs)

        # ── DynamoDB Tables ──────────────────────────────────────────────
        councils_table = dynamodb.Table(
            self, "CouncilsTable",
            table_name="watchdog-councils",
            partition_key=dynamodb.Attribute(name="council_id", type=dynamodb.AttributeType.STRING),
            billing_mode=dynamodb.BillingMode.PAY_PER_REQUEST,
            removal_policy=RemovalPolicy.RETAIN,
        )

        deliberations_table = dynamodb.Table(
            self, "DeliberationsTable",
            table_name="watchdog-deliberations",
            partition_key=dynamodb.Attribute(name="id", type=dynamodb.AttributeType.STRING),
            billing_mode=dynamodb.BillingMode.PAY_PER_REQUEST,
            removal_policy=RemovalPolicy.RETAIN,
        )
        deliberations_table.add_global_secondary_index(
            index_name="council_id-index",
            partition_key=dynamodb.Attribute(name="council_id", type=dynamodb.AttributeType.STRING),
        )

        subscribers_table = dynamodb.Table(
            self, "SubscribersTable",
            table_name="watchdog-subscribers",
            partition_key=dynamodb.Attribute(name="email", type=dynamodb.AttributeType.STRING),
            billing_mode=dynamodb.BillingMode.PAY_PER_REQUEST,
            removal_policy=RemovalPolicy.RETAIN,
        )
        subscribers_table.add_global_secondary_index(
            index_name="token-index",
            partition_key=dynamodb.Attribute(name="token", type=dynamodb.AttributeType.STRING),
        )

        # ── S3 Static Website ─────────────────────────────────────────────
        website_bucket = s3.Bucket(
            self, "WebsiteBucket",
            website_index_document="index.html",
            public_read_access=True,
            block_public_access=s3.BlockPublicAccess(
                block_public_acls=False,
                block_public_policy=False,
                ignore_public_acls=False,
                restrict_public_buckets=False,
            ),
            removal_policy=RemovalPolicy.RETAIN,
        )

        # ── SQS Queue + DLQ ───────────────────────────────────────────────
        dlq = sqs.Queue(
            self, "PdfDLQ",
            queue_name="watchdog-pdf-dlq",
            retention_period=Duration.days(14),
        )

        pdf_queue = sqs.Queue(
            self, "PdfQueue",
            queue_name="watchdog-pdf-queue",
            visibility_timeout=Duration.minutes(6),  # > worker timeout
            dead_letter_queue=sqs.DeadLetterQueue(
                max_receive_count=3,
                queue=dlq,
            ),
        )

        # ── Secrets ───────────────────────────────────────────────────────
        gemini_secret = secretsmanager.Secret(
            self, "GeminiSecret",
            secret_name="watchdog/gemini-api-key",
            description="Gemini 3.1 Pro API key",
        )

        # ── Lambda common config ──────────────────────────────────────────
        common_env = {
            "COUNCILS_TABLE": councils_table.table_name,
            "DELIBERATIONS_TABLE": deliberations_table.table_name,
            "SUBSCRIBERS_TABLE": subscribers_table.table_name,
            "PDF_QUEUE_URL": pdf_queue.queue_url,
            "WEBSITE_BUCKET": website_bucket.bucket_name,
            "GEMINI_SECRET_ARN": gemini_secret.secret_arn,
        }

        # ── Lambda: Orchestrator ──────────────────────────────────────────
        orchestrator = lambda_.Function(
            self, "Orchestrator",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/orchestrator.zip"),
            timeout=Duration.minutes(3),
            environment=common_env,
        )
        councils_table.grant_read_write_data(orchestrator)
        pdf_queue.grant_send_messages(orchestrator)

        # ── Lambda: Worker ────────────────────────────────────────────────
        worker = lambda_.Function(
            self, "Worker",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/worker.zip"),
            timeout=Duration.minutes(5),
            reserved_concurrent_executions=2,
            environment=common_env,
        )
        worker.add_event_source(lambda_events.SqsEventSource(
            pdf_queue,
            batch_size=1,
        ))
        councils_table.grant_read_write_data(worker)
        deliberations_table.grant_read_write_data(worker)
        gemini_secret.grant_read(worker)

        # ── Lambda: Publisher ─────────────────────────────────────────────
        publisher = lambda_.Function(
            self, "Publisher",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/publisher.zip"),
            timeout=Duration.minutes(5),
            environment={
                **common_env,
                "FROM_EMAIL": "watchdog@begles.example.com",  # must be SES-verified
                "SITE_URL": f"http://{website_bucket.bucket_website_url}",
            },
        )
        councils_table.grant_read_data(publisher)
        deliberations_table.grant_read_data(publisher)
        subscribers_table.grant_read_data(publisher)
        website_bucket.grant_put(publisher)
        publisher.add_to_role_policy(iam.PolicyStatement(
            actions=["ses:SendEmail"],
            resources=["*"],
        ))

        # Worker needs to invoke Publisher
        publisher.grant_invoke(worker)
        worker.add_environment("PUBLISHER_FUNCTION_NAME", publisher.function_name)

        # ── Lambda: Subscriber ────────────────────────────────────────────
        subscriber = lambda_.Function(
            self, "Subscriber",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/subscriber.zip"),
            timeout=Duration.seconds(10),
            environment={
                **common_env,
                "FROM_EMAIL": "watchdog@begles.example.com",
            },
        )
        subscribers_table.grant_read_write_data(subscriber)
        subscriber.add_to_role_policy(iam.PolicyStatement(
            actions=["ses:SendEmail"],
            resources=["*"],
        ))

        # ── API Gateway ───────────────────────────────────────────────────
        api = apigw.RestApi(
            self, "WatchdogApi",
            rest_api_name="watchdog-api",
            default_cors_preflight_options=apigw.CorsOptions(
                allow_origins=apigw.Cors.ALL_ORIGINS,
                allow_methods=["GET", "POST", "OPTIONS"],
                allow_headers=["Content-Type"],
            ),
        )
        subscriber_integration = apigw.LambdaIntegration(subscriber)
        subscribe_resource = api.root.add_resource("subscribe")
        subscribe_resource.add_method("POST", subscriber_integration)
        confirm_resource = api.root.add_resource("confirm")
        confirm_resource.add_method("GET", subscriber_integration)

        # Subscriber needs to know its own API URL for confirmation emails
        subscriber.add_environment("API_URL", api.url)

        # ── EventBridge: weekly trigger ───────────────────────────────────
        events.Rule(
            self, "WeeklyTrigger",
            schedule=events.Schedule.cron(
                week_day="MON",
                hour="7",   # 07:00 UTC = 09:00 Paris
                minute="0",
            ),
            targets=[targets.LambdaFunction(orchestrator)],
        )
```

- [ ] **Step 3: Bootstrap CDK and verify synth**

```bash
cd cdk
python -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
cdk synth 2>&1 | head -40
```

Expected: CloudFormation template printed, no errors.

- [ ] **Step 4: Commit**

```bash
git add cdk/
git commit -m "feat: CDK stack with all AWS resources"
```

---

## Task 3: Lambda Orchestrateur — HTML Scraper

**Files:**
- Create: `lambdas/orchestrator/scraper.go`
- Create: `lambdas/orchestrator/scraper_test.go`

- [ ] **Step 1: Write failing tests**

`lambdas/orchestrator/scraper_test.go`:
```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const listPageHTML = `
<ul class="list is-columns-4">
  <li class="list__item">
    <article class="publications-list-item">
      <h3 class="publications-list-item__title">
        <a href="https://example.com/conseil-28-mars/" class="publications-list-item__title-link">
          <span class="underline">Délibérations du conseil municipal du 28 mars 2026</span>
        </a>
      </h3>
      <time datetime="2026-03-28">28/03/2026</time>
    </article>
  </li>
  <li class="list__item">
    <article class="publications-list-item">
      <h3 class="publications-list-item__title">
        <span class="theme publications-list-item__category">Centre communal d'action sociale</span>
        <a href="https://example.com/ccas-26-jan/" class="publications-list-item__title-link">
          <span class="underline">Délibérations du CCAS du 26 janvier 2026</span>
        </a>
      </h3>
      <time datetime="2026-01-26">26/01/2026</time>
    </article>
  </li>
</ul>`

const detailPageHTML = `
<ul class="telecharger__list">
  <li class="telecharger__list-item">
    <div class="telecharger-item">
      <p class="telecharger-item__title">D01-2026_020 Élection du Maire</p>
      <a class="btn telecharger-item__link" href="https://example.com/D01.pdf" download="">Télécharger</a>
    </div>
  </li>
  <li class="telecharger__list-item">
    <div class="telecharger-item">
      <p class="telecharger-item__title">D02-2026_021 Détermination du nombre d'adjoints</p>
      <a class="btn telecharger-item__link" href="https://example.com/D02.pdf" download="">Télécharger</a>
    </div>
  </li>
</ul>`

func TestScrapeCouncilList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(listPageHTML))
	}))
	defer server.Close()

	s := NewScraper(server.URL)
	listings, err := s.ScrapeCouncilList()
	require.NoError(t, err)
	require.Len(t, listings, 2)

	assert.Equal(t, "conseil_municipal#2026-03-28", listings[0].CouncilID)
	assert.Equal(t, "conseil_municipal", listings[0].Category)
	assert.Equal(t, "2026-03-28", listings[0].Date)
	assert.Equal(t, "https://example.com/conseil-28-mars/", listings[0].URL)

	assert.Equal(t, "ccas#2026-01-26", listings[1].CouncilID)
	assert.Equal(t, "ccas", listings[1].Category)
}

func TestScrapePDFLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(detailPageHTML))
	}))
	defer server.Close()

	s := NewScraper("unused")
	pdfs, err := s.ScrapePDFLinks(server.URL)
	require.NoError(t, err)
	require.Len(t, pdfs, 2)

	assert.Equal(t, "https://example.com/D01.pdf", pdfs[0].URL)
	assert.Equal(t, "D01-2026_020 Élection du Maire", pdfs[0].Title)
	assert.Equal(t, "https://example.com/D02.pdf", pdfs[1].URL)
}

func TestNormalizeCategory(t *testing.T) {
	cases := []struct {
		raw      string
		expected string
	}{
		{"", "conseil_municipal"},
		{"Conseil municipal", "conseil_municipal"},
		{"Centre communal d'action sociale", "ccas"},
		{"Centre social et culturel de l'Estey", "csc_estey"},
		{"Les établissements", "etablissements"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, normalizeCategory(tc.raw), "input: %q", tc.raw)
	}
}
```

- [ ] **Step 2: Run tests — expect failure**

```bash
cd lambdas/orchestrator && go test ./... 2>&1
```

Expected: compilation error (scraper.go not yet created).

- [ ] **Step 3: Implement scraper.go**

`lambdas/orchestrator/scraper.go`:
```go
package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

type CouncilListing struct {
	CouncilID string
	Title     string
	Category  string
	Date      string
	URL       string
}

type PDFItem struct {
	Title string
	URL   string
}

type Scraper struct {
	listURL string
}

func NewScraper(listURL string) *Scraper {
	return &Scraper{listURL: listURL}
}

func (sc *Scraper) ScrapeCouncilList() ([]CouncilListing, error) {
	resp, err := http.Get(sc.listURL)
	if err != nil {
		return nil, fmt.Errorf("http get list page: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	var listings []CouncilListing
	doc.Find("ul.list li.list__item article.publications-list-item").Each(func(_ int, s *goquery.Selection) {
		link := s.Find("a.publications-list-item__title-link")
		url, _ := link.Attr("href")
		title := strings.TrimSpace(link.Find("span.underline").Text())
		category := normalizeCategory(strings.TrimSpace(s.Find("span.theme.publications-list-item__category").Text()))
		date, _ := s.Find("time").Attr("datetime")

		if url == "" || date == "" {
			return
		}
		listings = append(listings, CouncilListing{
			CouncilID: category + "#" + date,
			Title:     title,
			Category:  category,
			Date:      date,
			URL:       url,
		})
	})
	return listings, nil
}

func (sc *Scraper) ScrapePDFLinks(councilURL string) ([]PDFItem, error) {
	resp, err := http.Get(councilURL)
	if err != nil {
		return nil, fmt.Errorf("http get council page: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	var items []PDFItem
	doc.Find("li.telecharger__list-item").Each(func(_ int, s *goquery.Selection) {
		title := strings.TrimSpace(s.Find("p.telecharger-item__title").Text())
		href, exists := s.Find("a.telecharger-item__link").Attr("href")
		if exists && strings.HasSuffix(href, ".pdf") {
			items = append(items, PDFItem{Title: title, URL: href})
		}
	})
	return items, nil
}

func normalizeCategory(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "conseil municipal") || raw == "":
		return "conseil_municipal"
	case strings.Contains(lower, "centre communal"):
		return "ccas"
	case strings.Contains(lower, "estey"):
		return "csc_estey"
	default:
		return "etablissements"
	}
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
cd lambdas/orchestrator && go test ./... -v 2>&1
```

Expected:
```
--- PASS: TestScrapeCouncilList (0.00s)
--- PASS: TestScrapePDFLinks (0.00s)
--- PASS: TestNormalizeCategory (0.00s)
PASS
```

- [ ] **Step 5: Commit**

```bash
git add lambdas/orchestrator/scraper.go lambdas/orchestrator/scraper_test.go
git commit -m "feat(orchestrator): HTML scraper for deliberations list and PDF links"
```

---

## Task 4: Lambda Orchestrateur — Main Handler

**Files:**
- Create: `lambdas/orchestrator/main.go`

- [ ] **Step 1: Create main.go**

`lambdas/orchestrator/main.go`:
```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const deliberationsListURL = "https://www.mairie-begles.fr/d%C3%A9lib%C3%A9rations/"

type SQSMessage struct {
	CouncilID  string `json:"council_id"`
	PDFTitle   string `json:"pdf_title"`
	PDFURL     string `json:"pdf_url"`
	TotalPDFs  int    `json:"total_pdfs"`
}

func handler(ctx context.Context) error {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}

	ddb := dynamodb.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)
	scraper := NewScraper(deliberationsListURL)

	listings, err := scraper.ScrapeCouncilList()
	if err != nil {
		return fmt.Errorf("scrape council list: %w", err)
	}
	log.Printf("found %d councils on page", len(listings))

	for _, council := range listings {
		// Check if already processed
		existing, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
			Key: map[string]types.AttributeValue{
				"council_id": &types.AttributeValueMemberS{Value: council.CouncilID},
			},
		})
		if err != nil {
			return fmt.Errorf("get item %s: %w", council.CouncilID, err)
		}
		if existing.Item != nil {
			log.Printf("council %s already processed, skipping", council.CouncilID)
			continue
		}

		// Scrape PDF links
		pdfs, err := scraper.ScrapePDFLinks(council.URL)
		if err != nil {
			log.Printf("warn: failed to scrape PDFs for %s: %v", council.CouncilID, err)
			continue
		}
		if len(pdfs) == 0 {
			log.Printf("no PDFs found for council %s", council.CouncilID)
			continue
		}

		// Write council metadata to DynamoDB
		item, err := attributevalue.MarshalMap(map[string]interface{}{
			"council_id":     council.CouncilID,
			"category":       council.Category,
			"date":           council.Date,
			"title":          council.Title,
			"source_url":     council.URL,
			"total_pdfs":     len(pdfs),
			"processed_pdfs": 0,
		})
		if err != nil {
			return fmt.Errorf("marshal council: %w", err)
		}
		_, err = ddb.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:           aws.String(os.Getenv("COUNCILS_TABLE")),
			Item:                item,
			ConditionExpression: aws.String("attribute_not_exists(council_id)"),
		})
		if err != nil {
			log.Printf("warn: council %s already exists (race): %v", council.CouncilID, err)
			continue
		}

		// Push one SQS message per PDF
		for _, pdf := range pdfs {
			msg := SQSMessage{
				CouncilID: council.CouncilID,
				PDFTitle:  pdf.Title,
				PDFURL:    pdf.URL,
				TotalPDFs: len(pdfs),
			}
			body, _ := json.Marshal(msg)
			_, err = sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
				QueueUrl:    aws.String(os.Getenv("PDF_QUEUE_URL")),
				MessageBody: aws.String(string(body)),
				// Standard queue: no MessageDeduplicationId/MessageGroupId (FIFO-only).
				// Idempotency is handled by the Worker via DynamoDB attribute_not_exists.
			})
			if err != nil {
				log.Printf("warn: failed to enqueue %s: %v", pdf.URL, err)
			}
		}
		log.Printf("enqueued %d PDFs for council %s", len(pdfs), council.CouncilID)
	}
	return nil
}

func main() {
	lambda.Start(handler)
}
```

- [ ] **Step 2: Build and verify compilation**

```bash
cd lambdas/orchestrator
GOARCH=arm64 GOOS=linux go build -tags lambda.norpc -o /tmp/bootstrap_test . 2>&1
```

Expected: no errors, binary created.

- [ ] **Step 3: Commit**

```bash
git add lambdas/orchestrator/main.go
git commit -m "feat(orchestrator): main handler — scrape, check DynamoDB, fan-out to SQS"
```

---

## Task 5: Lambda Worker — Gemini Client

**Files:**
- Create: `lambdas/worker/gemini.go`
- Create: `lambdas/worker/gemini_test.go`

- [ ] **Step 1: Write failing test**

`lambdas/worker/gemini_test.go`:
```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGeminiResponse(t *testing.T) {
	raw := `{
		"title": "Élection du Maire",
		"summary": "Le conseil a élu son maire.",
		"vote": {"pour": 32, "contre": 5, "abstention": 2},
		"disagreements": "L'opposition a contesté la procédure."
	}`

	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "Élection du Maire", result.Title)
	assert.Equal(t, "Le conseil a élu son maire.", result.Summary)
	assert.Equal(t, 32, result.Vote.Pour)
	assert.Equal(t, 5, result.Vote.Contre)
	assert.Equal(t, 2, result.Vote.Abstention)
	assert.Equal(t, "L'opposition a contesté la procédure.", result.Disagreements)
}

func TestParseGeminiResponseNoDisagreements(t *testing.T) {
	raw := `{
		"title": "Budget",
		"summary": "Vote unanime du budget.",
		"vote": {"pour": 39, "contre": 0, "abstention": 0},
		"disagreements": ""
	}`

	result, err := parseGeminiResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "", result.Disagreements)
}

func TestParseGeminiResponseInvalidJSON(t *testing.T) {
	_, err := parseGeminiResponse("not json")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run — expect failure**

```bash
cd lambdas/worker && go test ./... 2>&1
```

Expected: compilation error.

- [ ] **Step 3: Implement gemini.go**

`lambdas/worker/gemini.go`:
```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

const geminiModel = "gemini-3.1-pro"

const deliberationPrompt = `Tu es un journaliste municipal factuel et neutre.
Analyse ce document PDF de délibération du conseil municipal de Bègles.

Retourne UNIQUEMENT un objet JSON valide avec cette structure exacte :
{
  "title": "titre exact de la délibération",
  "summary": "résumé factuel en 3 phrases maximum, sans jugement politique",
  "vote": {"pour": <nombre entier>, "contre": <nombre entier>, "abstention": <nombre entier>},
  "disagreements": "description factuelle des désaccords entre majorité et opposition, ou chaîne vide si vote unanime"
}

Si les chiffres du vote ne sont pas mentionnés dans le document, utilise 0.
Ne génère aucun texte en dehors du JSON.`

type GeminiResult struct {
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	Vote          struct {
		Pour       int `json:"pour"`
		Contre     int `json:"contre"`
		Abstention int `json:"abstention"`
	} `json:"vote"`
	Disagreements string `json:"disagreements"`
}

func analyzeWithGemini(ctx context.Context, apiKey string, pdfBytes []byte) (*GeminiResult, error) {
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	defer client.Close()

	model := client.GenerativeModel(geminiModel)
	model.ResponseMIMEType = "application/json"

	resp, err := model.GenerateContent(ctx,
		genai.Blob{MIMEType: "application/pdf", Data: pdfBytes},
		genai.Text(deliberationPrompt),
	)
	if err != nil {
		return nil, fmt.Errorf("gemini generate: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned empty response")
	}

	raw := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0])
	return parseGeminiResponse(raw)
}

func parseGeminiResponse(raw string) (*GeminiResult, error) {
	// Strip markdown code fences if present
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result GeminiResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parse gemini json: %w", err)
	}
	return &result, nil
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
cd lambdas/worker && go test ./... -v -run TestParseGemini 2>&1
```

Expected: all 3 TestParseGemini* tests PASS.

- [ ] **Step 5: Commit**

```bash
git add lambdas/worker/gemini.go lambdas/worker/gemini_test.go
git commit -m "feat(worker): Gemini 3.1 Pro client with JSON response parsing"
```

---

## Task 6: Lambda Worker — SQS Handler with Idempotency

**Files:**
- Create: `lambdas/worker/handler.go`
- Create: `lambdas/worker/handler_test.go`
- Create: `lambdas/worker/main.go`

- [ ] **Step 1: Write failing tests**

`lambdas/worker/handler_test.go`:
```go
package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock DynamoDB client
type mockDDB struct {
	putItemErr    error
	updateItemOut *dynamodb.UpdateItemOutput
	updateItemErr error
}

func (m *mockDDB) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, m.putItemErr
}

func (m *mockDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	if m.updateItemOut != nil {
		return m.updateItemOut, m.updateItemErr
	}
	// Default: processed_pdfs < total_pdfs (not last PDF)
	return &dynamodb.UpdateItemOutput{
		Attributes: map[string]types.AttributeValue{
			"processed_pdfs": &types.AttributeValueMemberN{Value: "1"},
			"total_pdfs":     &types.AttributeValueMemberN{Value: "5"},
		},
	}, m.updateItemErr
}

func buildSQSEvent(msg SQSPayload) events.SQSEvent {
	body, _ := json.Marshal(msg)
	return events.SQSEvent{
		Records: []events.SQSMessage{
			{Body: string(body)},
		},
	}
}

func TestHandleRecord_IdempotentDuplicate(t *testing.T) {
	condErr := &types.ConditionalCheckFailedException{}
	h := &WorkerHandler{
		ddb: &mockDDB{putItemErr: condErr},
	}
	msg := SQSPayload{CouncilID: "conseil_municipal#2026-03-28", PDFURL: "https://example.com/D01.pdf", PDFTitle: "D01", TotalPDFs: 5}
	err := h.handleRecord(context.Background(), msg, []byte("fakePDF"), &GeminiResult{Title: "t", Summary: "s"})
	// Should NOT return error — duplicate is silently skipped
	assert.NoError(t, err)
}

func TestHandleRecord_LastPDFDetection(t *testing.T) {
	publisherInvoked := false
	h := &WorkerHandler{
		ddb: &mockDDB{
			updateItemOut: &dynamodb.UpdateItemOutput{
				Attributes: map[string]types.AttributeValue{
					"processed_pdfs": &types.AttributeValueMemberN{Value: "5"},
					"total_pdfs":     &types.AttributeValueMemberN{Value: "5"},
				},
			},
		},
		invokePublisher: func(_ context.Context, councilID string) error {
			publisherInvoked = true
			assert.Equal(t, "conseil_municipal#2026-03-28", councilID)
			return nil
		},
	}
	msg := SQSPayload{CouncilID: "conseil_municipal#2026-03-28", PDFURL: "https://example.com/D05.pdf", PDFTitle: "D05", TotalPDFs: 5}
	err := h.handleRecord(context.Background(), msg, []byte("fakePDF"), &GeminiResult{Title: "t", Summary: "s"})
	require.NoError(t, err)
	assert.True(t, publisherInvoked)
}

func TestDeliborationID(t *testing.T) {
	id := deliberationID("https://example.com/D01.pdf")
	assert.Len(t, id, 64) // SHA-256 hex
	// Same URL always gives same ID
	assert.Equal(t, id, deliberationID("https://example.com/D01.pdf"))
	// Different URL gives different ID
	assert.NotEqual(t, id, deliberationID("https://example.com/D02.pdf"))
}
```

- [ ] **Step 2: Run — expect failure**

```bash
cd lambdas/worker && go test ./... 2>&1
```

Expected: compilation error.

- [ ] **Step 3: Implement handler.go**

`lambdas/worker/handler.go`:
```go
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	lambdaclient "github.com/aws/aws-sdk-go-v2/service/lambda"
	awslambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	secretsmanager "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type SQSPayload struct {
	CouncilID string `json:"council_id"`
	PDFTitle  string `json:"pdf_title"`
	PDFURL    string `json:"pdf_url"`
	TotalPDFs int    `json:"total_pdfs"`
}

type DynamoDBAPI interface {
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type WorkerHandler struct {
	ddb             DynamoDBAPI
	invokePublisher func(ctx context.Context, councilID string) error
	geminiAPIKey    string
}

func deliberationID(pdfURL string) string {
	h := sha256.Sum256([]byte(pdfURL))
	return fmt.Sprintf("%x", h)
}

func downloadPDF(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (h *WorkerHandler) handleRecord(ctx context.Context, msg SQSPayload, pdfBytes []byte, result *GeminiResult) error {
	id := deliberationID(msg.PDFURL)

	// 1. Write to DynamoDB with idempotency guard (attribute_not_exists)
	item, err := attributevalue.MarshalMap(map[string]interface{}{
		"id":             id,
		"council_id":     msg.CouncilID,
		"title":          result.Title,
		"pdf_url":        msg.PDFURL,
		"summary":        result.Summary,
		"vote_pour":      result.Vote.Pour,
		"vote_contre":    result.Vote.Contre,
		"vote_abstention": result.Vote.Abstention,
		"disagreements":  result.Disagreements,
		"processed_at":   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal item: %w", err)
	}

	_, err = h.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(os.Getenv("DELIBERATIONS_TABLE")),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	if err != nil {
		var cfe *types.ConditionalCheckFailedException
		if isType(err, cfe) {
			log.Printf("deliberation %s already processed, skipping", id)
			return nil // idempotent skip
		}
		return fmt.Errorf("put deliberation: %w", err)
	}

	// 2. Atomically increment processed_pdfs on the council
	out, err := h.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: msg.CouncilID},
		},
		UpdateExpression: aws.String("SET processed_pdfs = processed_pdfs + :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":one": &types.AttributeValueMemberN{Value: "1"},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return fmt.Errorf("update council counter: %w", err)
	}

	// 3. Check if this was the last PDF
	processed := attrInt(out.Attributes, "processed_pdfs")
	total := attrInt(out.Attributes, "total_pdfs")
	if processed == total && total > 0 {
		log.Printf("council %s complete (%d/%d), invoking publisher", msg.CouncilID, processed, total)
		if err := h.invokePublisher(ctx, msg.CouncilID); err != nil {
			log.Printf("warn: failed to invoke publisher: %v", err)
		}
	}
	return nil
}

func attrInt(attrs map[string]types.AttributeValue, key string) int {
	v, ok := attrs[key]
	if !ok {
		return 0
	}
	n, ok := v.(*types.AttributeValueMemberN)
	if !ok {
		return 0
	}
	i, _ := strconv.Atoi(n.Value)
	return i
}

func isType[T any](err error, target T) bool {
	_, ok := err.(T)
	return ok
}

func newWorkerHandler(ctx context.Context) (*WorkerHandler, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}

	// Retrieve Gemini API key from Secrets Manager
	sm := secretsmanager.NewFromConfig(cfg)
	secret, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(os.Getenv("GEMINI_SECRET_ARN")),
	})
	if err != nil {
		return nil, fmt.Errorf("get gemini secret: %w", err)
	}

	lambdaClient := lambdaclient.NewFromConfig(cfg)
	publisherFn := os.Getenv("PUBLISHER_FUNCTION_NAME")

	return &WorkerHandler{
		ddb:          dynamodb.NewFromConfig(cfg),
		geminiAPIKey: aws.ToString(secret.SecretString),
		invokePublisher: func(ctx context.Context, councilID string) error {
			payload, _ := json.Marshal(map[string]string{"council_id": councilID})
			_, err := lambdaClient.Invoke(ctx, &lambdaclient.InvokeInput{
				FunctionName:   aws.String(publisherFn),
				InvocationType: awslambdatypes.InvocationTypeEvent, // async
				Payload:        payload,
			})
			return err
		},
	}, nil
}

func sqsHandler(ctx context.Context, event events.SQSEvent) error {
	h, err := newWorkerHandler(ctx)
	if err != nil {
		return fmt.Errorf("init handler: %w", err)
	}

	for _, record := range event.Records {
		var msg SQSPayload
		if err := json.Unmarshal([]byte(record.Body), &msg); err != nil {
			log.Printf("error: invalid SQS message: %v", err)
			continue
		}

		pdfBytes, err := downloadPDF(msg.PDFURL)
		if err != nil {
			return fmt.Errorf("download pdf %s: %w", msg.PDFURL, err)
		}

		result, err := analyzeWithGemini(ctx, h.geminiAPIKey, pdfBytes)
		if err != nil {
			return fmt.Errorf("gemini analysis %s: %w", msg.PDFURL, err)
		}

		if err := h.handleRecord(ctx, msg, pdfBytes, result); err != nil {
			return fmt.Errorf("handle record %s: %w", msg.PDFURL, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Create main.go**

`lambdas/worker/main.go`:
```go
package main

import "github.com/aws/aws-lambda-go/lambda"

func main() {
	lambda.Start(sqsHandler)
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
cd lambdas/worker && go test ./... -v 2>&1
```

Expected:
```
--- PASS: TestHandleRecord_IdempotentDuplicate
--- PASS: TestHandleRecord_LastPDFDetection
--- PASS: TestDeliborationID
PASS
```

- [ ] **Step 6: Build and verify**

```bash
cd lambdas/worker && GOARCH=arm64 GOOS=linux go build -tags lambda.norpc -o /tmp/worker_test . 2>&1
```

- [ ] **Step 7: Commit**

```bash
git add lambdas/worker/
git commit -m "feat(worker): SQS handler with Gemini analysis, DynamoDB idempotency, publisher trigger"
```

---

## Task 7: Lambda Publisher

**Files:**
- Create: `lambdas/publisher/handler.go`
- Create: `lambdas/publisher/handler_test.go`
- Create: `lambdas/publisher/main.go`

- [ ] **Step 1: Write failing tests**

`lambdas/publisher/handler_test.go`:
```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDataJSON(t *testing.T) {
	councils := []CouncilRecord{
		{
			CouncilID:  "conseil_municipal#2026-03-28",
			Category:   "conseil_municipal",
			Date:       "2026-03-28",
			Title:      "Délibérations du 28 mars",
			SourceURL:  "https://example.com",
			TotalPDFs:  2,
			Processed:  2,
		},
	}
	delibs := map[string][]DeliberationRecord{
		"conseil_municipal#2026-03-28": {
			{
				ID:            "abc",
				CouncilID:     "conseil_municipal#2026-03-28",
				Title:         "Élection du Maire",
				PDFURL:        "https://example.com/D01.pdf",
				Summary:       "Résumé.",
				VotePour:      32,
				VoteContre:    5,
				VoteAbstention: 2,
				Disagreements: "",
			},
		},
	}

	data, err := buildDataJSON(councils, delibs)
	require.NoError(t, err)

	require.Len(t, data.Councils, 1)
	assert.Equal(t, "2026-03-28", data.Councils[0].Date)
	require.Len(t, data.Councils[0].Deliberations, 1)
	assert.Equal(t, "Élection du Maire", data.Councils[0].Deliberations[0].Title)
	assert.Equal(t, 32, data.Councils[0].Deliberations[0].Vote.Pour)
}
```

- [ ] **Step 2: Run — expect failure**

```bash
cd lambdas/publisher && go test ./... 2>&1
```

- [ ] **Step 3: Implement handler.go**

`lambdas/publisher/handler.go`:
```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// ── DynamoDB records ──────────────────────────────────────────────────────────

type CouncilRecord struct {
	CouncilID string `dynamodbav:"council_id"`
	Category  string `dynamodbav:"category"`
	Date      string `dynamodbav:"date"`
	Title     string `dynamodbav:"title"`
	SourceURL string `dynamodbav:"source_url"`
	TotalPDFs int    `dynamodbav:"total_pdfs"`
	Processed int    `dynamodbav:"processed_pdfs"`
}

type DeliberationRecord struct {
	ID             string `dynamodbav:"id"`
	CouncilID      string `dynamodbav:"council_id"`
	Title          string `dynamodbav:"title"`
	PDFURL         string `dynamodbav:"pdf_url"`
	Summary        string `dynamodbav:"summary"`
	VotePour       int    `dynamodbav:"vote_pour"`
	VoteContre     int    `dynamodbav:"vote_contre"`
	VoteAbstention int    `dynamodbav:"vote_abstention"`
	Disagreements  string `dynamodbav:"disagreements"`
	ProcessedAt    string `dynamodbav:"processed_at"`
}

// ── JSON output format ────────────────────────────────────────────────────────

type DataJSON struct {
	GeneratedAt string          `json:"generated_at"`
	Councils    []CouncilOutput `json:"councils"`
}

type CouncilOutput struct {
	CouncilID      string               `json:"id"`
	Category       string               `json:"category"`
	Date           string               `json:"date"`
	Title          string               `json:"title"`
	SourceURL      string               `json:"source_url"`
	Deliberations  []DeliberationOutput `json:"deliberations"`
}

type DeliberationOutput struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	PDFURL        string    `json:"pdf_url"`
	Summary       string    `json:"summary"`
	Vote          VoteCount `json:"vote"`
	Disagreements string    `json:"disagreements"`
}

type VoteCount struct {
	Pour       int `json:"pour"`
	Contre     int `json:"contre"`
	Abstention int `json:"abstention"`
}

// ── Business logic (pure, testable) ──────────────────────────────────────────

func buildDataJSON(councils []CouncilRecord, delibs map[string][]DeliberationRecord) (*DataJSON, error) {
	out := &DataJSON{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, c := range councils {
		co := CouncilOutput{
			CouncilID: c.CouncilID,
			Category:  c.Category,
			Date:      c.Date,
			Title:     c.Title,
			SourceURL: c.SourceURL,
		}
		for _, d := range delibs[c.CouncilID] {
			co.Deliberations = append(co.Deliberations, DeliberationOutput{
				ID:      d.ID,
				Title:   d.Title,
				PDFURL:  d.PDFURL,
				Summary: d.Summary,
				Vote: VoteCount{
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

// ── Lambda handler ────────────────────────────────────────────────────────────

type PublisherEvent struct {
	CouncilID string `json:"council_id"`
}

func handler(ctx context.Context, event PublisherEvent) error {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}
	ddb := dynamodb.NewFromConfig(cfg)

	// Fetch the council record
	councilOut, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
		Key: map[string]types.AttributeValue{
			"council_id": &types.AttributeValueMemberS{Value: event.CouncilID},
		},
	})
	if err != nil || councilOut.Item == nil {
		return fmt.Errorf("council %s not found: %w", event.CouncilID, err)
	}
	var council CouncilRecord
	if err := attributevalue.UnmarshalMap(councilOut.Item, &council); err != nil {
		return err
	}

	// Query deliberations for this council
	delibs, err := queryDeliberations(ctx, ddb, event.CouncilID)
	if err != nil {
		return fmt.Errorf("query deliberations: %w", err)
	}

	// Build all councils for the full data.json (scan councils table)
	allCouncils, allDelibs, err := fetchAllData(ctx, ddb)
	if err != nil {
		return fmt.Errorf("fetch all data: %w", err)
	}
	// Merge the freshly processed council into the full dataset
	allDelibs[event.CouncilID] = delibs

	data, err := buildDataJSON(allCouncils, allDelibs)
	if err != nil {
		return err
	}

	// Upload data.json to S3
	jsonBytes, _ := json.MarshalIndent(data, "", "  ")
	s3Client := s3.NewFromConfig(cfg)
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(os.Getenv("WEBSITE_BUCKET")),
		Key:         aws.String("data.json"),
		Body:        bytes.NewReader(jsonBytes),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("upload data.json: %w", err)
	}
	log.Printf("data.json uploaded (%d bytes)", len(jsonBytes))

	// Send newsletter
	return sendNewsletter(ctx, cfg, &council, delibs)
}

func queryDeliberations(ctx context.Context, ddb *dynamodb.Client, councilID string) ([]DeliberationRecord, error) {
	out, err := ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(os.Getenv("DELIBERATIONS_TABLE")),
		IndexName:              aws.String("council_id-index"),
		KeyConditionExpression: aws.String("council_id = :cid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":cid": &types.AttributeValueMemberS{Value: councilID},
		},
	})
	if err != nil {
		return nil, err
	}
	var records []DeliberationRecord
	return records, attributevalue.UnmarshalListOfMaps(out.Items, &records)
}

func fetchAllData(ctx context.Context, ddb *dynamodb.Client) ([]CouncilRecord, map[string][]DeliberationRecord, error) {
	councilScan, err := ddb.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(os.Getenv("COUNCILS_TABLE"))})
	if err != nil {
		return nil, nil, err
	}
	var councils []CouncilRecord
	if err := attributevalue.UnmarshalListOfMaps(councilScan.Items, &councils); err != nil {
		return nil, nil, err
	}

	deliberationScan, err := ddb.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(os.Getenv("DELIBERATIONS_TABLE"))})
	if err != nil {
		return nil, nil, err
	}
	var allDelibs []DeliberationRecord
	if err := attributevalue.UnmarshalListOfMaps(deliberationScan.Items, &allDelibs); err != nil {
		return nil, nil, err
	}

	delibs := make(map[string][]DeliberationRecord)
	for _, d := range allDelibs {
		delibs[d.CouncilID] = append(delibs[d.CouncilID], d)
	}
	return councils, delibs, nil
}

func sendNewsletter(ctx context.Context, cfg aws.Config, council *CouncilRecord, delibs []DeliberationRecord) error {
	subScan, err := dynamodb.NewFromConfig(cfg).Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		FilterExpression: aws.String("#s = :confirmed"),
		ExpressionAttributeNames:  map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":confirmed": &types.AttributeValueMemberS{Value: "confirmed"}},
	})
	if err != nil || len(subScan.Items) == 0 {
		log.Printf("no confirmed subscribers or error: %v", err)
		return nil
	}

	subject := fmt.Sprintf("Watchdog Bègles — %s", council.Title)
	body := buildEmailBody(council, delibs)

	ses := sesv2.NewFromConfig(cfg)
	for _, item := range subScan.Items {
		emailAttr, ok := item["email"].(*types.AttributeValueMemberS)
		if !ok {
			continue
		}
		_, err := ses.SendEmail(ctx, &sesv2.SendEmailInput{
			FromEmailAddress: aws.String(os.Getenv("FROM_EMAIL")),
			Destination:      &sestypes.Destination{ToAddresses: []string{emailAttr.Value}},
			Content: &sestypes.EmailContent{
				Simple: &sestypes.Message{
					Subject: &sestypes.Content{Data: aws.String(subject)},
					Body:    &sestypes.Body{Html: &sestypes.Content{Data: aws.String(body)}},
				},
			},
		})
		if err != nil {
			log.Printf("warn: failed to send email to %s: %v", emailAttr.Value, err)
		}
	}
	return nil
}

func buildEmailBody(council *CouncilRecord, delibs []DeliberationRecord) string {
	var sb bytes.Buffer
	sb.WriteString(fmt.Sprintf("<h1>%s</h1>", council.Title))
	sb.WriteString(fmt.Sprintf("<p><a href='%s'>Voir les documents officiels</a></p>", council.SourceURL))
	for _, d := range delibs {
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>", d.Title))
		sb.WriteString(fmt.Sprintf("<p>%s</p>", d.Summary))
		sb.WriteString(fmt.Sprintf("<p><strong>Vote :</strong> %d pour / %d contre / %d abstention</p>", d.VotePour, d.VoteContre, d.VoteAbstention))
		if d.Disagreements != "" {
			sb.WriteString(fmt.Sprintf("<p><em>Désaccords : %s</em></p>", d.Disagreements))
		}
		sb.WriteString(fmt.Sprintf("<p><a href='%s'>PDF source</a></p><hr>", d.PDFURL))
	}
	return sb.String()
}
```

Note: the `types` package import in publisher/handler.go needs to be:
```go
"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
```

- [ ] **Step 4: Create main.go**

`lambdas/publisher/main.go`:
```go
package main

import "github.com/aws/aws-lambda-go/lambda"

func main() {
	lambda.Start(handler)
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
cd lambdas/publisher && go test ./... -v 2>&1
```

Expected: `--- PASS: TestBuildDataJSON`

- [ ] **Step 6: Build**

```bash
cd lambdas/publisher && GOARCH=arm64 GOOS=linux go build -tags lambda.norpc -o /tmp/publisher_test . 2>&1
```

- [ ] **Step 7: Commit**

```bash
git add lambdas/publisher/
git commit -m "feat(publisher): compile data.json from DynamoDB, upload to S3, send SES newsletter"
```

---

## Task 8: Lambda Subscriber — Newsletter Signup

**Files:**
- Create: `lambdas/subscriber/handler.go`
- Create: `lambdas/subscriber/handler_test.go`
- Create: `lambdas/subscriber/main.go`

- [ ] **Step 1: Write failing tests**

`lambdas/subscriber/handler_test.go`:
```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateEmail(t *testing.T) {
	assert.True(t, isValidEmail("user@example.com"))
	assert.True(t, isValidEmail("user+tag@sub.domain.fr"))
	assert.False(t, isValidEmail("notanemail"))
	assert.False(t, isValidEmail("missing@tld"))
	assert.False(t, isValidEmail(""))
	assert.False(t, isValidEmail("@domain.com"))
}

func TestCORSHeaders(t *testing.T) {
	headers := corsHeaders()
	assert.Equal(t, "*", headers["Access-Control-Allow-Origin"])
	assert.Equal(t, "POST,GET,OPTIONS", headers["Access-Control-Allow-Methods"])
}
```

- [ ] **Step 2: Run — expect failure**

```bash
cd lambdas/subscriber && go test ./... 2>&1
```

- [ ] **Step 3: Implement handler.go**

`lambdas/subscriber/handler.go`:
```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/google/uuid"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func isValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}

func corsHeaders() map[string]string {
	return map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "POST,GET,OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type",
		"Content-Type":                 "application/json",
	}
}

func apiResponse(status int, body map[string]string) events.APIGatewayProxyResponse {
	b, _ := json.Marshal(body)
	return events.APIGatewayProxyResponse{
		StatusCode: status,
		Headers:    corsHeaders(),
		Body:       string(b),
	}
}

func handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	switch req.HTTPMethod {
	case http.MethodOptions:
		return events.APIGatewayProxyResponse{StatusCode: 200, Headers: corsHeaders()}, nil
	case http.MethodPost:
		return handleSubscribe(ctx, req)
	case http.MethodGet:
		return handleConfirm(ctx, req)
	default:
		return apiResponse(405, map[string]string{"error": "method not allowed"}), nil
	}
}

func handleSubscribe(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil || !isValidEmail(body.Email) {
		return apiResponse(400, map[string]string{"error": "invalid email"}), nil
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))

	cfg, _ := config.LoadDefaultConfig(ctx)
	ddb := dynamodb.NewFromConfig(cfg)

	// Check existing subscriber
	existing, _ := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: email}},
	})
	if existing.Item != nil {
		return apiResponse(200, map[string]string{"message": "already registered"}), nil
	}

	token := uuid.New().String()
	item, _ := attributevalue.MarshalMap(map[string]interface{}{
		"email":      email,
		"status":     "pending",
		"token":      token,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	})
	ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		Item:      item,
	})

	confirmURL := fmt.Sprintf("%sconfirm?token=%s", os.Getenv("API_URL"), token)
	sendConfirmationEmail(ctx, cfg, email, confirmURL)

	return apiResponse(200, map[string]string{"message": "confirmation email sent"}), nil
}

func handleConfirm(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	token := req.QueryStringParameters["token"]
	if token == "" {
		return apiResponse(400, map[string]string{"error": "missing token"}), nil
	}

	cfg, _ := config.LoadDefaultConfig(ctx)
	ddb := dynamodb.NewFromConfig(cfg)

	// Find subscriber by token using GSI
	out, err := ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		IndexName:              aws.String("token-index"),
		KeyConditionExpression: aws.String("token = :t"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":t": &types.AttributeValueMemberS{Value: token},
		},
	})
	if err != nil || len(out.Items) == 0 {
		return apiResponse(404, map[string]string{"error": "invalid token"}), nil
	}

	emailAttr := out.Items[0]["email"].(*types.AttributeValueMemberS)
	ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
		Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: emailAttr.Value}},
		UpdateExpression: aws.String("SET #s = :confirmed"),
		ExpressionAttributeNames:  map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":confirmed": &types.AttributeValueMemberS{Value: "confirmed"}},
	})

	// Redirect to site
	return events.APIGatewayProxyResponse{
		StatusCode: 302,
		Headers:    map[string]string{"Location": os.Getenv("SITE_URL") + "?subscribed=true"},
	}, nil
}

func sendConfirmationEmail(ctx context.Context, cfg aws.Config, email, confirmURL string) {
	ses := sesv2.NewFromConfig(cfg)
	body := fmt.Sprintf(`<p>Cliquez sur ce lien pour confirmer votre abonnement au Watchdog Bègles :</p>
<p><a href="%s">Confirmer mon abonnement</a></p>
<p>Si vous n'avez pas demandé cet abonnement, ignorez ce message.</p>`, confirmURL)

	_, err := ses.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(os.Getenv("FROM_EMAIL")),
		Destination:      &sestypes.Destination{ToAddresses: []string{email}},
		Content: &sestypes.EmailContent{
			Simple: &sestypes.Message{
				Subject: &sestypes.Content{Data: aws.String("Confirmez votre abonnement — Watchdog Bègles")},
				Body:    &sestypes.Body{Html: &sestypes.Content{Data: aws.String(body)}},
			},
		},
	})
	if err != nil {
		log.Printf("warn: confirmation email failed for %s: %v", email, err)
	}
}
```

- [ ] **Step 4: Create main.go**

`lambdas/subscriber/main.go`:
```go
package main

import "github.com/aws/aws-lambda-go/lambda"

func main() {
	lambda.Start(handler)
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
cd lambdas/subscriber && go test ./... -v 2>&1
```

Expected:
```
--- PASS: TestValidateEmail
--- PASS: TestCORSHeaders
PASS
```

- [ ] **Step 6: Commit**

```bash
git add lambdas/subscriber/
git commit -m "feat(subscriber): newsletter signup with double opt-in via SES + DynamoDB"
```

---

## Task 9: Frontend — Timeline + Newsletter Form

**Files:**
- Create: `frontend/index.html`

- [ ] **Step 1: Create index.html**

`frontend/index.html`:
```html
<!DOCTYPE html>
<html lang="fr">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Watchdog Bègles — Délibérations du conseil</title>
  <script src="https://cdn.tailwindcss.com"></script>
  <style>
    .category-conseil_municipal { background-color: #3b82f6; }
    .category-ccas              { background-color: #10b981; }
    .category-csc_estey         { background-color: #f59e0b; }
    .category-etablissements    { background-color: #8b5cf6; }
  </style>
</head>
<body class="bg-gray-50 text-gray-800 font-sans">

  <!-- Header -->
  <header class="bg-white shadow-sm sticky top-0 z-10">
    <div class="max-w-3xl mx-auto px-4 py-4 flex items-center justify-between">
      <div>
        <h1 class="text-xl font-bold text-gray-900">Watchdog Bègles</h1>
        <p class="text-xs text-gray-500">Délibérations analysées par IA — données officielles mairie-begles.fr</p>
      </div>
      <button onclick="document.getElementById('newsletter').scrollIntoView({behavior:'smooth'})"
              class="text-sm bg-blue-600 text-white px-3 py-1.5 rounded-lg hover:bg-blue-700 transition">
        S'abonner
      </button>
    </div>
  </header>

  <!-- Filters -->
  <div class="max-w-3xl mx-auto px-4 pt-6 pb-2 flex flex-wrap gap-2" id="filters">
    <button data-filter="all"
            class="filter-btn active px-3 py-1 rounded-full text-sm border border-gray-300 bg-white hover:bg-gray-100">
      Tous
    </button>
    <button data-filter="conseil_municipal"
            class="filter-btn px-3 py-1 rounded-full text-sm border border-blue-300 text-blue-700 bg-blue-50 hover:bg-blue-100">
      Conseil municipal
    </button>
    <button data-filter="ccas"
            class="filter-btn px-3 py-1 rounded-full text-sm border border-emerald-300 text-emerald-700 bg-emerald-50 hover:bg-emerald-100">
      CCAS
    </button>
    <button data-filter="csc_estey"
            class="filter-btn px-3 py-1 rounded-full text-sm border border-amber-300 text-amber-700 bg-amber-50 hover:bg-amber-100">
      CSC l'Estey
    </button>
    <button data-filter="etablissements"
            class="filter-btn px-3 py-1 rounded-full text-sm border border-violet-300 text-violet-700 bg-violet-50 hover:bg-violet-100">
      Établissements
    </button>
  </div>

  <!-- Timeline -->
  <main class="max-w-3xl mx-auto px-4 py-4" id="timeline">
    <p class="text-gray-400 text-sm">Chargement…</p>
  </main>

  <!-- Newsletter -->
  <section id="newsletter" class="bg-blue-600 text-white py-12 mt-10">
    <div class="max-w-xl mx-auto px-4 text-center">
      <h2 class="text-2xl font-bold mb-2">Recevez les analyses par email</h2>
      <p class="text-blue-100 mb-6 text-sm">À chaque nouveau conseil, un résumé dans votre boîte mail.</p>
      <form id="subscribe-form" class="flex gap-2 max-w-sm mx-auto">
        <input id="email-input" type="email" required placeholder="votre@email.fr"
               class="flex-1 px-4 py-2 rounded-lg text-gray-900 text-sm focus:outline-none focus:ring-2 focus:ring-white">
        <button type="submit"
                class="bg-white text-blue-700 font-semibold px-4 py-2 rounded-lg hover:bg-blue-50 transition text-sm">
          S'abonner
        </button>
      </form>
      <p id="subscribe-msg" class="mt-3 text-sm text-blue-200 hidden"></p>
    </div>
  </section>

  <footer class="text-center text-xs text-gray-400 py-6">
    Données issues de <a href="https://www.mairie-begles.fr/délibérations/" class="underline" target="_blank">mairie-begles.fr</a>.
    Analyse automatisée — vérifiez toujours la source officielle.
  </footer>

  <script>
    const API_URL = '__API_URL__'; // Replaced by CDK output or manually

    const categoryLabels = {
      conseil_municipal: 'Conseil municipal',
      ccas: 'CCAS',
      csc_estey: "CSC l'Estey",
      etablissements: 'Établissements',
    };

    let allCouncils = [];
    let activeFilter = 'all';

    async function loadData() {
      try {
        const resp = await fetch('./data.json');
        const data = await resp.json();
        allCouncils = (data.councils || []).sort((a, b) => b.date.localeCompare(a.date));
        render();
      } catch (e) {
        document.getElementById('timeline').innerHTML =
          '<p class="text-red-500 text-sm">Impossible de charger les données.</p>';
      }
    }

    function render() {
      const filtered = activeFilter === 'all'
        ? allCouncils
        : allCouncils.filter(c => c.category === activeFilter);

      const timeline = document.getElementById('timeline');
      if (filtered.length === 0) {
        timeline.innerHTML = '<p class="text-gray-400 text-sm">Aucun résultat.</p>';
        return;
      }

      timeline.innerHTML = filtered.map(council => `
        <div class="council-entry mb-8" data-category="${council.category}">
          <div class="flex items-center gap-3 mb-3">
            <span class="w-3 h-3 rounded-full category-${council.category} flex-shrink-0"></span>
            <div>
              <span class="text-xs font-medium text-gray-500 uppercase tracking-wide">
                ${categoryLabels[council.category] || council.category}
              </span>
              <h2 class="text-lg font-semibold leading-tight">
                <a href="${council.source_url}" target="_blank" class="hover:text-blue-600 transition">
                  ${council.title}
                </a>
              </h2>
              <time class="text-xs text-gray-400">${formatDate(council.date)}</time>
            </div>
          </div>
          <div class="ml-6 border-l-2 border-gray-200 pl-4 space-y-4">
            ${(council.deliberations || []).map(d => `
              <div class="bg-white rounded-lg shadow-sm p-4">
                <div class="flex items-start justify-between gap-2">
                  <h3 class="font-medium text-sm">${d.title}</h3>
                  <a href="${d.pdf_url}" target="_blank" class="text-xs text-blue-500 hover:underline flex-shrink-0">PDF</a>
                </div>
                <p class="text-sm text-gray-600 mt-2">${d.summary}</p>
                <div class="flex items-center gap-4 mt-3 text-xs text-gray-500">
                  <span class="text-green-600 font-medium">✓ ${d.vote.pour} pour</span>
                  <span class="text-red-500 font-medium">✗ ${d.vote.contre} contre</span>
                  ${d.vote.abstention > 0 ? `<span class="text-gray-400">${d.vote.abstention} abstention(s)</span>` : ''}
                </div>
                ${d.disagreements ? `
                  <div class="mt-3 bg-amber-50 border border-amber-200 rounded p-2 text-xs text-amber-800">
                    <strong>Désaccords :</strong> ${d.disagreements}
                  </div>
                ` : ''}
              </div>
            `).join('')}
          </div>
        </div>
      `).join('');
    }

    function formatDate(iso) {
      return new Date(iso).toLocaleDateString('fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
    }

    // Filter buttons
    document.getElementById('filters').addEventListener('click', e => {
      const btn = e.target.closest('[data-filter]');
      if (!btn) return;
      document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active', 'ring-2', 'ring-blue-400'));
      btn.classList.add('active', 'ring-2', 'ring-blue-400');
      activeFilter = btn.dataset.filter;
      render();
    });

    // Newsletter form
    document.getElementById('subscribe-form').addEventListener('submit', async e => {
      e.preventDefault();
      const email = document.getElementById('email-input').value.trim();
      const msg = document.getElementById('subscribe-msg');
      try {
        const resp = await fetch(API_URL + 'subscribe', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ email }),
        });
        const data = await resp.json();
        msg.textContent = resp.ok
          ? 'Vérifiez votre boîte mail pour confirmer votre abonnement.'
          : data.error || 'Une erreur est survenue.';
        msg.classList.remove('hidden');
      } catch {
        msg.textContent = 'Erreur réseau. Réessayez.';
        msg.classList.remove('hidden');
      }
    });

    // On load: check ?subscribed=true
    if (new URLSearchParams(location.search).get('subscribed') === 'true') {
      const msg = document.getElementById('subscribe-msg');
      msg.textContent = 'Abonnement confirmé ! Vous recevrez les prochaines analyses par email.';
      msg.classList.remove('hidden');
      document.getElementById('newsletter').scrollIntoView();
    }

    loadData();
  </script>
</body>
</html>
```

- [ ] **Step 2: Upload index.html to S3 (after CDK deploy)**

```bash
BUCKET=$(aws cloudformation describe-stacks --stack-name WatchdogStack \
  --query "Stacks[0].Outputs[?OutputKey=='WebsiteBucketName'].OutputValue" \
  --output text)
# Replace placeholder API_URL in HTML then upload
API_URL=$(aws cloudformation describe-stacks --stack-name WatchdogStack \
  --query "Stacks[0].Outputs[?OutputKey=='ApiUrl'].OutputValue" \
  --output text)
sed "s|__API_URL__|${API_URL}|g" frontend/index.html > /tmp/index.html
aws s3 cp /tmp/index.html s3://${BUCKET}/index.html --content-type text/html
```

- [ ] **Step 3: Commit**

```bash
git add frontend/index.html
git commit -m "feat(frontend): timeline site with category filters and newsletter form"
```

---

## Task 10: Build, Deploy & Smoke Test

**Files:** none (infra already defined)

- [ ] **Step 1: Add CDK stack outputs**

Add to the bottom of `WatchdogStack.__init__` in `cdk/watchdog_stack.py`:
```python
from aws_cdk import CfnOutput
CfnOutput(self, "WebsiteBucketName", value=website_bucket.bucket_name)
CfnOutput(self, "WebsiteUrl", value=website_bucket.bucket_website_url)
CfnOutput(self, "ApiUrl", value=api.url)
```

- [ ] **Step 2: Bootstrap CDK (first deploy only)**

```bash
cd cdk && source .venv/bin/activate
cdk bootstrap
```

Expected: `✅ Environment aws://ACCOUNT/REGION bootstrapped`

- [ ] **Step 3: Build all Lambdas**

```bash
cd /path/to/watchdog && make build 2>&1
```

Expected: 4 zip files in `dist/`: `orchestrator.zip`, `worker.zip`, `publisher.zip`, `subscriber.zip`

- [ ] **Step 4: Deploy CDK stack**

```bash
cd cdk && cdk deploy --require-approval never 2>&1
```

Expected: `✅ WatchdogStack` with outputs for bucket name, website URL, and API URL.

- [ ] **Step 5: Upload index.html with correct API URL**

```bash
API_URL=$(aws cloudformation describe-stacks --stack-name WatchdogStack \
  --query "Stacks[0].Outputs[?OutputKey=='ApiUrl'].OutputValue" --output text)
BUCKET=$(aws cloudformation describe-stacks --stack-name WatchdogStack \
  --query "Stacks[0].Outputs[?OutputKey=='WebsiteBucketName'].OutputValue" --output text)
sed "s|__API_URL__|${API_URL}|g" frontend/index.html > /tmp/index.html
aws s3 cp /tmp/index.html s3://${BUCKET}/index.html --content-type text/html
```

- [ ] **Step 6: Verify SES sender identity**

Before emails work, the FROM_EMAIL must be verified in SES. Go to AWS Console → SES → Verified Identities → Create identity → Email address → `watchdog@begles.example.com`. Click the link in the verification email.

- [ ] **Step 7: Store Gemini API key**

```bash
aws secretsmanager put-secret-value \
  --secret-id watchdog/gemini-api-key \
  --secret-string "YOUR_GEMINI_API_KEY_HERE"
```

- [ ] **Step 8: Smoke test — invoke Orchestrator manually**

```bash
aws lambda invoke \
  --function-name WatchdogStack-Orchestrator* \
  --payload '{}' \
  --cli-binary-format raw-in-base64-out \
  /tmp/orchestrator-output.json
cat /tmp/orchestrator-output.json
```

Expected: `{"statusCode":200}` or null (Go Lambda returns no body on success). Check CloudWatch logs:
```bash
aws logs tail /aws/lambda/WatchdogStack-Orchestrator --follow --since 5m
```
Expected: lines like `found N councils on page` and `enqueued N PDFs for council ...`

- [ ] **Step 9: Watch SQS → Worker processing**

```bash
# Check SQS queue depth (should drain as workers process)
aws sqs get-queue-attributes \
  --queue-url $(aws sqs get-queue-url --queue-name watchdog-pdf-queue --query QueueUrl --output text) \
  --attribute-names ApproximateNumberOfMessages

# Watch worker logs
aws logs tail /aws/lambda/WatchdogStack-Worker --follow --since 5m
```

Expected: worker logs show `deliberation X already processed` or successful Gemini calls, followed by Publisher invocation.

- [ ] **Step 10: Verify data.json on S3**

```bash
aws s3 cp s3://${BUCKET}/data.json - | python3 -m json.tool | head -50
```

Expected: valid JSON with `councils` array containing deliberations.

- [ ] **Step 11: Verify website is live**

```bash
SITE_URL=$(aws cloudformation describe-stacks --stack-name WatchdogStack \
  --query "Stacks[0].Outputs[?OutputKey=='WebsiteUrl'].OutputValue" --output text)
curl -s "${SITE_URL}" | grep -c "Watchdog Bègles"
```

Expected: `1`

- [ ] **Step 12: Final commit**

```bash
git add cdk/watchdog_stack.py
git commit -m "feat: add CDK stack outputs for website URL and API URL"
```

---

## Self-Review Checklist

| Requirement | Task |
|---|---|
| CDK Python + Go Lambdas arm64 | Task 2 |
| Scrape `mairie-begles.fr/délibérations/` | Task 3 |
| Differentiate categories (conseil_municipal, ccas, csc_estey, etablissements) | Task 3 — `normalizeCategory()` |
| SQS + DLQ + reservedConcurrentExecutions=2 | Task 2 — CDK stack |
| Worker idempotency: Gemini → PutItem(attribute_not_exists) → atomic increment | Task 6 — `handleRecord()` |
| Gemini model = `gemini-3.1-pro` | Task 5 — `const geminiModel` |
| DynamoDB PAY_PER_REQUEST for all 3 tables | Task 2 — CDK stack |
| No PDF stored on S3 (in-memory only) | Task 6 — `downloadPDF()` returns bytes |
| Publisher: DynamoDB → data.json → S3 + SES newsletter | Task 7 |
| API Gateway POST /subscribe + GET /confirm with CORS | Task 2 (CDK) + Task 8 |
| Double opt-in via SES confirmation email | Task 8 — `handleSubscribe()` |
| Timeline front-end with category filters | Task 9 |
| Newsletter form → API Gateway | Task 9 — JS fetch |
| EventBridge weekly Monday 9h Paris | Task 2 — cron(MON,7,0) UTC |
