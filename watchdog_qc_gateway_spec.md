# Production Prompt — Watchdog QC Gateway (Deterministic Gate + Sensory-Deprived Generation)

> Self-contained implementation brief. Hand this file verbatim to a coding agent.
> Paradigm: **Correctness by Construction**. No LLM-as-a-Judge anywhere. The QC gate
> is 100% deterministic (math/logic/statistics, binary verdict). Editorial neutrality
> is guaranteed structurally, by **sensory deprivation**: the newsletter-writing LLM
> never sees source prose — only cold, pre-validated, structured facts.
>
> There is NO `judge.go`, NO `QC_JUDGE_MODE`, NO semantic auditing of any kind. Do not
> add any LLM that grades, scores, or approves content. If you are tempted to, stop.

---

## 0. Role & operating rules

You are a senior Go / AWS serverless engineer working in the `watchdog` repo (an
automated transparency pipeline that analyses French municipal council PDFs for the
town of Bègles and publishes a website + Brevo newsletter).

Hard constraints — violating any is a failed task:

- **Language**: code, comments, identifiers, logs in **English**. User-facing
  newsletter/website strings stay **French** (do not translate domain text).
- **Architecture**: all Lambdas are `PROVIDED_AL2023`, **arm64**, handler `bootstrap`,
  compiled to `dist/<name>.zip`. Always build with `GOARCH=arm64`.
- **Module layout**: each Lambda is its own Go module with
  `replace github.com/watchdog/shared => ../shared`. Go 1.26.1.
- **Fail-closed**: the gate never publishes/sends on uncertainty. If a check cannot run
  (Gemini down, read error, malformed data), the council stays `PENDING`/`QUARANTINED` —
  never silently `APPROVED`.
- **Determinism**: the QC verdict is a pure function of the data. Same input → same
  verdict, every time. No randomness, no model in the gate.
- **Idempotence**: every state transition is a conditional write, safe to retry. Reuse
  the repo's existing conditional-write patterns (§2).
- **No over-engineering**: no feature flags, no backward-compat shims, no single-use
  helpers. Validate only at the data boundary.
- **Commits**: author Jules, **no `Co-Authored-By` trailer**. Conventional Commits, one
  logical change per commit.
- **Done = green**: `go test ./...` passes in every touched module; all arm64 zips
  build; `cdk synth` succeeds.

---

## 1. Repo facts you must rely on (do not re-derive)

### Pipeline flow (current)
```
EventBridge cron (Mon–Fri 16:00 UTC)
  → Orchestrator: scrapes council page, enqueues 1 SQS msg per PDF (queue watchdog-pdf-queue).
  → Worker (gemini-2.5-flash, concurrency 5, batch 1): downloads PDF, analyses it,
      writes a deliberation row, atomically counts it; when processed_pdfs == total_pdfs
      it claims published_at and invokes Publisher.
  → Aggregator (gemini-2.5-pro, DynamoDB stream on deliberations INSERT): recomputes
      council analysis and ALSO invokes Publisher when the council is complete.
  → Publisher: scans ALL councils + deliberations, builds data.json, uploads to S3,
      async-invokes Notifier.
  → Notifier (gemini-2.5-pro): generates newsletter params from the council's
      deliberations, two-phase claims the send slot, sends a Brevo campaign.
```

### DynamoDB tables
- `watchdog-councils` — PK `council_id` (String). PAY_PER_REQUEST, PITR, RETAIN.
  - Real rows: `council_id`, `category`, `date`, `title`, `summary`, `source_url`,
    `total_pdfs`, `processed_pdfs`, `published_at`, `newsletter_pending_at`,
    `newsletter_sent_at`, `analysis` (map).
  - Metadata rows (`metadata#` prefix): `metadata#next_council`,
    `metadata#gemini_circuit`, `metadata#publisher_lock`. **All council scans already
    filter `begins_with(council_id, "metadata#")` — keep doing so.**
- `watchdog-deliberations` — PK `id` (String). GSI `council_id-index` (PK `council_id`).
  Stream `NEW_IMAGE`.
  - Flat attributes: `id`, `council_id`, `title`, `topic_tag`, `pdf_url`, `summary`,
    `is_substantial`, `analysis_data` (map: `contexte`,`decision`,`impacts`,
    `points_debattus`, nullable String), `budget_impact` (Int64), `budget_type` (String),
    `budget_breakdown` (List of {`topic_tag`,`label`,`amount`}), `climate_impact`,
    `key_points` (List<String>), `has_vote` (Bool), `vote_pour`/`vote_contre`/
    `vote_abstention` (nullable Int), `disagreements` (nullable String), `counted` (Bool),
    `processed_at`.

### Env vars present
`COUNCILS_TABLE`, `DELIBERATIONS_TABLE`, `SUBSCRIBERS_TABLE`, `GEMINI_API_KEY`,
`GEMINI_MODEL`, `PUBLISHER_FUNCTION_NAME`, `NOTIFIER_FUNCTION_NAME`, `WEBSITE_BUCKET`,
`PDF_QUEUE_URL`.

### Reusable helpers in `lambdas/shared` (USE THESE)
- `shared.TopicTags` (10), `shared.BudgetTypes` (`DÉPENSE`,`RECETTE`,`CAUTION`,`AUCUN`),
  `shared.ClimateImpacts` (`positif`,`neutre`,`negatif`).
- `shared.MatchTopicTag/MatchBudgetType/MatchClimateImpact` → `(canonical, ok)`.
- `shared.PaginateQuery / PaginateScan / PaginateScanCount`.
- `shared.CallGeminiWithRetry(ctx, fn, maxAttempts)`.
- `shared.GeminiCircuitOpen / RecordGeminiError / RecordGeminiSuccess`.
- `shared.TruncForLog(s, n)`.

### Gemini calling convention (mirror exactly)
```go
client, _ := genai.NewClient(ctx, &genai.ClientConfig{
    APIKey: apiKey, HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta"}})
resp, err := shared.CallGeminiWithRetry(ctx, func(ctx context.Context) (*genai.GenerateContentResponse, error) {
    return client.Models.GenerateContent(ctx, model, contents, &genai.GenerateContentConfig{
        Temperature:      ptrFloat32(0),
        ResponseMIMEType: "application/json",
        ResponseSchema:   <schema>,
        MaxOutputTokens:  8192,
    })
}, 4)
```

---

## 2. Target architecture

A **blocking, fully-deterministic QC gateway** sits between deliberation analysis and
any externally visible output. A council is published/sent **only** when its whole batch
reaches `qc_status = "APPROVED"`. Newsletter prose is generated under sensory deprivation.

```
Worker counts last PDF of a council  ─┐
Aggregator sees council complete     ─┴─►  set council qc_status=PENDING, invoke VALIDATOR
VALIDATOR (deterministic gate + sensory-deprived generator):
   1. Load council + all its deliberations (paginated GSI query).
   2. ValidateDeterministic  (per-deliberation + cross-deliberation invariants)  → D1–D10
   3. ValidateStatistical    (council-level + drift vs APPROVED baseline)        → S1–S6
   4. verdict := Decide(violations)  // QUARANTINED iff any HIGH. Pure, binary, no LLM.
   5a. QUARANTINED → write qc_report, emit QcQuarantined metric, OPTIONAL self-heal
       (re-enqueue offending PDFs, §3.6); DO NOT publish/send.
   5b. APPROVED → build ColdNewsletterInput (whitelisted structured facts ONLY, §3.3),
       generate newsletter params via the sensory-deprived LLM, persist them, set
       qc_status=APPROVED, invoke Publisher, then Notifier.
PUBLISHER: hard filter — data.json includes ONLY councils with qc_status=APPROVED.
NOTIFIER:  thin Brevo sender of the pre-generated, pre-approved params handed by the
           Validator. No Gemini call, no validation. (Test-list direct-send path kept.)
```

`qc_status` lifecycle: `PENDING → VALIDATING → APPROVED | QUARANTINED`.

> **Why centralise generation in the Validator**: the generated newsletter is the only
> externally visible synthesised artifact. Centralising it means exactly one Gemini
> generation per council, the Notifier becomes side-effect-only, and "what was validated"
> equals "what was sent". The generator's neutrality is guaranteed by its *input*, not by
> any downstream grader.

### The neutrality guarantee (sensory deprivation)
Partisan/extrapolated prose is impossible to produce from data that contains no prose.
The generation LLM receives **only** cold, typed, gate-validated facts (integers, enums,
booleans, and the verbatim factual title). It has **no** access to: the source PDF, the
worker's `summary`, any `analysis_data.*` field, `disagreements` text, `key_points`, or
`acronyms`. With no narrative source, the model can only phrase arithmetic and
categories — it cannot invent motive, blame, history, or value judgements.

> **Design consequence (accepted)**: `context`/`impact` newsletter fields become
> *phrasings of facts*, not narrative. Where the cold facts give no basis, the field is
> emitted empty. This is the deliberate trade for zero-maintenance neutrality. The vote
> climate, vote stats and budget totals remain **computed in Go** (no LLM), as today.

---

## 3. Deliverables (exact files)

### 3.1 `lambdas/shared/qc.go` — deterministic + statistical assertion engine
Pure, no AWS calls inside the checks. Fully unit-tested.

```go
type Severity string
const ( SeverityHigh Severity = "HIGH"; SeverityWarn Severity = "WARN" )

type Violation struct {
    Rule           string   `json:"rule"`            // e.g. "D2_BUDGET_AUCUN_NONZERO"
    Severity       Severity `json:"severity"`
    DeliberationID string   `json:"deliberation_id,omitempty"`
    Field          string   `json:"field,omitempty"`
    Detail         string   `json:"detail"`
}
type DeliberationView struct { /* flat deliberation attrs the checks need */ }
type CouncilView      struct { /* council_id, total_pdfs, processed_pdfs, date */ }
type Baseline struct { NeantRateMean, NeantRateStd float64; BudgetMedian int64; SampleSize int }
type Verdict struct {
    Status     string      `json:"status"`     // "APPROVED" | "QUARANTINED"
    Violations []Violation `json:"violations"`
}
func (v Verdict) HasHigh() bool

func ValidateDeterministic(c CouncilView, delibs []DeliberationView) []Violation
func ValidateStatistical(c CouncilView, delibs []DeliberationView, base Baseline, cfg QcConfig) []Violation
func Decide(violations []Violation) Verdict   // QUARANTINED iff any HIGH; else APPROVED
```

`QcConfig` (env-driven; document each default):
- `NeantRateCeiling` (0.65) — absolute max share of delibs with `impacts == "Néant"`.
- `NeantRateZMax` (3.0) — max z-score of this council's Néant rate vs baseline.
- `MaxAbsoluteBudgetEUR` (500_000_000) — single-deliberation sanity ceiling.
- `MaxVoteTotal` (60) — plausibility cap for pour+contre+abstention (Bègles ≈35–39 seats).
- `BreakdownToleranceEUR` (1).
- `MinBaselineSample` (5) — below this, skip z-score drift (S3) but keep absolute checks.

#### Deterministic rules (HIGH unless noted)
| ID | Rule | FAILS when |
|----|------|-----------|
| D1 | Enum validity | `topic_tag`/`budget_type`/`climate_impact` not canonicalisable via `shared.Match*`. |
| D2 | Budget/AUCUN coherence | `AUCUN ∧ budget_impact≠0`, OR `budget_impact≠0 ∧ AUCUN`. |
| D3 | Budget sign | `budget_impact<0`, or any breakdown `amount<0`. |
| D4 | **Breakdown accounting** | breakdown non-empty ∧ `budget_impact>0` ∧ `\|Σamount − budget_impact\| > BreakdownToleranceEUR`. **(Currently a silent `WARN`+`fmt.Printf` in `worker/gemini.go`; promote to HIGH.)** |
| D5 | Breakdown tags | any breakdown `topic_tag` not canonicalisable. |
| D6 | Vote coherence | `has_vote=false` but a counter >0; OR `has_vote=true` but all three nil. |
| D7 | Vote sign | any vote counter <0. |
| D8 | Required text | `title` or `summary` empty/whitespace; `analysis_data.decision` nil/empty. |
| D9 | Impacts present | `analysis_data.impacts` nil, empty, or literal `"null"` (must be `"Néant"` or content). |
| D10 | Leaked markup (WARN) | `summary`/`analysis_data.*` contains HTML, markdown links, or `En savoir plus`/`Voir sur le site`/`→`. |

#### Statistical rules
| ID | Rule | Sev | FAILS when |
|----|------|-----|-----------|
| S1 | Empty council | HIGH | `processed_pdfs>0` but `len(delibs)==0`. |
| S2 | Néant absolute ceiling | HIGH | share of `impacts=="Néant"` > `NeantRateCeiling`. |
| S3 | Néant drift | WARN | `SampleSize≥MinBaselineSample` ∧ z-score vs baseline > `NeantRateZMax`. |
| S4 | Tag collapse | WARN | all delibs share one `topic_tag` ∧ `len(delibs)≥4`. |
| S5 | Budget outlier | HIGH | any deliberation `budget_impact > MaxAbsoluteBudgetEUR` (catches float/parse 100× bugs). |
| S6 | Vote plausibility | HIGH | any deliberation `pour+contre+abstention > MaxVoteTotal`. |

`ValidateStatistical` computes the council's Néant rate itself; the `Baseline` is supplied
by the Validator Lambda (queries historical APPROVED councils).

### 3.2 `lambdas/shared/qc_test.go`
Table-driven tests for **every** rule D1–D10 and S1–S6: ≥1 passing and ≥1 failing fixture
per rule; assert exact `Rule` IDs and `Severity`. `Decide`: HIGH→QUARANTINED,
only-WARN→APPROVED, empty→APPROVED. Statistical: baseline below `MinBaselineSample` skips
S3 but still runs S2/S5/S6.

### 3.3 `lambdas/shared/newsletter.go` — sensory-deprived generation (hoisted from notifier)
Move the pure newsletter logic out of `lambdas/notifier/handler.go` into shared so the
Validator owns it. Keep behaviour identical except for the **input contract** below.

Define the strict whitelist — the ONLY data the generation LLM may receive:
```go
// ColdDeliberation is the COMPLETE set of facts exposed to the generator.
// Adding a free-text/source-derived field here breaks the neutrality guarantee.
type ColdDeliberation struct {
    Title         string  // verbatim factual identifier; may be lightly cleaned, NO new facts
    TopicTag      string  // enum
    BudgetImpact  int64
    BudgetType    string  // enum
    HasVote       bool
    Pour          *int
    Contre        *int
    Abstention    *int
    ClimateImpact string  // enum
    IsSubstantial bool
}
```
**Explicitly forbidden as generator input** (assert this in code review and tests):
`summary`, `analysis_data.*`, `disagreements`, `key_points`, `acronyms`, PDF bytes/text.

Generation rules:
- The funnel/routing logic (tensions vs adopted vs briefs) stays in Go and may use
  `disagreements != ""` and vote counts as a **boolean tension signal** — but the
  `disagreements` *text is never passed to the model*. Derive `HasDisagreement bool`;
  pass the bool, not the prose.
- `vote_climat`, `climat_color`, `vote_stats`, `budget_total` stay **computed in Go**
  (`ComputeNewsletterStats` — keep as is). The LLM copies them verbatim.
- The generation prompt (French) must state: *"Tu reçois UNIQUEMENT les faits structurés
  ci-dessous. Tu n'as AUCUN accès au document source. N'invente rien. Chaque phrase doit
  être déductible de ces champs (montant, type, votes, catégorie, titre). Si un champ ne
  fournit aucune base factuelle pour 'context' ou 'impact', renvoie une chaîne vide. Aucun
  jugement de valeur, aucune motivation politique, aucun fait historique ou géographique."*
- Keep the existing `NewsletterParams`/`TensionItem`/`AdoptedItem`/`BriefItem` types,
  `newsletterSchema`, `parseNewsletterParams`, and post-processing (`formatBudgetFR`,
  `stripLinks`, `formatStr`, the link-strip regex). These are belt-and-suspenders.
- Expose `func GenerateNewsletterParams(ctx, geminiDeps, council, cold []ColdDeliberation, stats, nextMeeting, totalCouncils, totalDelibs int) (*NewsletterParams, error)`.
- Re-point existing notifier tests at `shared`; they must stay green.

### 3.4 `lambdas/validator/` — new Lambda module
Files: `go.mod` (module `github.com/watchdog/validator`, `replace
github.com/watchdog/shared => ../shared`), `main.go`, `handler.go`, `handler_test.go`.

`type ValidatorEvent struct { CouncilID string `json:"council_id"` }`

`handler.go`:
1. `shared.GeminiCircuitOpen` → return nil (leave PENDING; later trigger retries). Never
   quarantine on infra outage.
2. Idempotent claim: conditional `SET qc_status="VALIDATING"` guarded by
   `qc_status = "PENDING" OR attribute_not_exists(qc_status)` — exactly one Validator
   processes a council; concurrent invocations no-op (mirror publisher-lock / claimPending).
3. Load council + deliberations (`shared.PaginateQuery` on `council_id-index`).
4. Build `Baseline` from councils with `qc_status="APPROVED"` (paginated, metadata filtered):
   Néant-rate mean/std + budget median.
5. `det := shared.ValidateDeterministic(...)`; `stat := shared.ValidateStatistical(..., baseline, cfg)`.
6. `verdict := shared.Decide(append(det, stat...))`.
7a. QUARANTINED → conditional `SET qc_status="QUARANTINED", qc_report=:r, qc_validated_at=:ts`
    (guard `qc_status="VALIDATING"`); emit EMF `QcQuarantined` (Namespace `Watchdog`,
    dimension `CouncilId`); OPTIONAL self-heal (§3.6); DO NOT invoke Publisher/Notifier.
7b. APPROVED → build `[]ColdDeliberation` (whitelist ONLY), `GenerateNewsletterParams(...)`,
    then conditional `SET qc_status="APPROVED", qc_validated_at=:ts, newsletter_params=:p`
    (guard `qc_status="VALIDATING"`); invoke Publisher then Notifier
    (async `InvocationType=Event`, payload `{council_id, newsletter_params}`).
8. All deps behind interfaces (`DynamoDBAPI`, `LambdaAPI`, a Gemini generation fn) so
   `handler_test.go` runs with mocks — **no network, no real Gemini**.

`handler_test.go` covers: clean council → APPROVED + Publisher&Notifier invoked once each;
deterministic HIGH (e.g. D2, D4) → QUARANTINED + generator NOT called + no invokes;
statistical HIGH (S2) → QUARANTINED; circuit open → no-op; concurrent claim → second
invocation no-ops; APPROVED transition idempotent; **assert the generation fn receives a
`ColdDeliberation` with no source-prose fields populated** (neutrality contract test).

### 3.5 Write-path changes
- **Worker** (`worker/handler.go`): write `"qc_status":"PENDING"` in the deliberation item;
  on council completion set the **council** row `qc_status="PENDING"` and invoke the
  **Validator** instead of the Publisher (env `VALIDATOR_FUNCTION_NAME`). Keep the
  `published_at` conditional claim as the single-fan-out guard. Also **promote the existing
  D4 breakdown `WARN` in `worker/gemini.go` to rely on the gate** (the worker may keep its
  log, but the gate is now authoritative).
- **Aggregator** (`aggregator/main.go`): on completion, set `qc_status="PENDING"` and invoke
  the Validator; must NOT invoke Publisher directly anymore.
- **Publisher** (`publisher/handler.go`): in `fetchAllData`, add `qc_status = :approved` to
  the council scan filter (alongside the metadata-prefix filter) so data.json can only ever
  contain APPROVED councils. Defense in depth.
- **Notifier** (`notifier/handler.go`): accept pre-generated params from the event; when
  present, skip Gemini entirely and just claim/send/confirm via Brevo. Keep the `TestListID`
  direct path (which may still generate, for manual test sends).

### 3.6 OPTIONAL self-heal (recommended for zero-maintenance)
A deterministic quarantine means the *data* is wrong; re-validating identical data loops.
To stay set-and-forget, on QUARANTINE the Validator may re-enqueue the offending PDFs to
`watchdog-pdf-queue` for fresh worker analysis, capped by a `qc_attempts` counter on the
council (default cap 2). After the cap, hard-quarantine + `QcQuarantined` alarm (human
signal, rare, indisputable). Re-enqueue only deliberations implicated by a HIGH violation
(use `Violation.DeliberationID`). Implement behind `QC_SELF_HEAL` (default on) and
`QC_MAX_ATTEMPTS` (default 2). If you implement it, unit-test: attempt < cap → re-enqueue +
counter bump; attempt ≥ cap → hard quarantine, no re-enqueue.

### 3.7 CDK (`cdk/watchdog_stack.py`)
- Add `validator` Lambda (`PROVIDED_AL2023`, arm64, `../dist/validator.zip`, timeout 5 min,
  `GEMINI_MODEL="gemini-2.5-pro"`, common_env + QC knobs).
- Grants: councils `GetItem/UpdateItem/Scan`, deliberations `Query`/read,
  `publisher.grant_invoke(validator)`, `notifier.grant_invoke(validator)`; if self-heal,
  `pdf_queue.grant_send_messages(validator)`.
- `validator.add_environment(PUBLISHER_FUNCTION_NAME=..., NOTIFIER_FUNCTION_NAME=...)`.
- Worker + Aggregator: add `VALIDATOR_FUNCTION_NAME` + `validator.grant_invoke(...)`; drop
  the now-unused publisher invoke grant on their completion path.
- QC env knobs with §3.1 defaults: `QC_NEANT_CEILING`, `QC_NEANT_Z_MAX`,
  `QC_MAX_BUDGET_EUR`, `QC_MAX_VOTE_TOTAL`, `QC_BREAKDOWN_TOL_EUR`, `QC_MIN_BASELINE`,
  `QC_SELF_HEAL`, `QC_MAX_ATTEMPTS`.
- CloudWatch Alarm on `QcQuarantined > 0` (mirror the existing `NotifierDLQAlarm` SNS pattern).
- Update `Makefile` to build `validator.zip` (arm64) alongside the others.

---

## 4. Build / verification gate (all must pass before "done")
```bash
cd lambdas/shared     && go test ./...
cd lambdas/validator  && go test ./...
cd lambdas/worker     && go test ./...
cd lambdas/notifier   && go test ./...
cd lambdas/publisher  && go test ./...
cd lambdas/aggregator && go test ./...
make build                       # arm64 zips, incl. validator.zip
cd cdk && cdk synth >/dev/null
```

## 5. Acceptance criteria
1. A deliberation with `budget_type=AUCUN` and `budget_impact=5000` → council QUARANTINED
   (D2); Publisher/Notifier never invoked; `QcQuarantined` emitted.
2. A breakdown whose sum diverges > tolerance → QUARANTINED (D4); the old silent WARN no
   longer leaks to publication.
3. `impacts=="Néant"` share > ceiling → QUARANTINED (S2).
4. A clean council → PENDING→APPROVED exactly once; persists `newsletter_params`; invokes
   Publisher then Notifier once each; re-invocation is a no-op.
5. **Neutrality contract**: the generation fn is called with `ColdDeliberation` values only;
   a test asserts no `summary`/`analysis_data`/`disagreements` text is ever passed to it.
6. `data.json` from Publisher contains zero non-APPROVED councils.
7. Gemini circuit open → Validator leaves council PENDING, returns nil (no quarantine, no
   publish).
8. (If self-heal built) quarantine with `qc_attempts < cap` re-enqueues offending PDFs and
   bumps the counter; at the cap it hard-quarantines without re-enqueue.
9. The verdict is deterministic: running the Validator twice on identical data yields the
   identical `qc_report`. No LLM, no `judge.go`, no `QC_JUDGE_MODE` anywhere in the tree.

## 6. Suggested commit sequence (one logical change each, independently green)
1. `feat(shared): add deterministic + statistical QC assertion engine` (+tests)
2. `refactor(shared): hoist newsletter generation out of notifier into shared`
3. `feat(shared): restrict newsletter generator to cold validated facts (sensory deprivation)`
4. `feat(validator): blocking deterministic QC gateway Lambda` (+handler tests)
5. `feat(worker,aggregator): set qc_status=PENDING and route completion to validator`
6. `feat(publisher): publish only APPROVED councils in data.json`
7. `feat(notifier): send pre-approved newsletter params from validator`
8. `feat(validator): optional self-heal re-enqueue on quarantine` (if built)
9. `feat(cdk): wire validator Lambda, IAM, QcQuarantined alarm, build target`
