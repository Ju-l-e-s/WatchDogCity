# data.json Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable long-term caching for `data.json` with manual invalidation for better performance.

**Architecture:** Update both the dynamic generator (Publisher Lambda) and the static deployer (CDK) to set aggressive `Cache-Control` headers.

**Tech Stack:** Go (Lambda), Python (CDK).

---

### Task 1: Update Publisher Lambda (Go)

**Files:**
- Modify: `lambdas/publisher/handler.go`

- [ ] **Step 1: Add CacheControl to S3 PutObject call**

Modify `lambdas/publisher/handler.go` around line 175:
```go
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(os.Getenv("WEBSITE_BUCKET")),
		Key:          aws.String("data.json"),
		Body:         bytes.NewReader(jsonBytes),
		ContentType:  aws.String("application/json"),
		CacheControl: aws.String("public, max-age=31536000, immutable"),
	})
```

- [ ] **Step 2: Verify Go compilation**

Run: `cd lambdas/publisher && go build ./...`
Expected: PASS (no syntax errors)

- [ ] **Step 3: Commit Publisher changes**

```bash
git add lambdas/publisher/handler.go
git commit -m "feat(publisher): set aggressive cache-control for data.json"
```

---

### Task 2: Update Infrastructure (CDK Python)

**Files:**
- Modify: `cdk/watchdog_stack.py`

- [ ] **Step 1: Update CacheControl for data.json in CDK**

Modify `cdk/watchdog_stack.py` (around lines 220-235) to split `data.json` from the no-cache deployment:

```python
        s3_deploy.BucketDeployment(
            self, "DeployWebsiteConfig",
            sources=[s3_deploy.Source.asset("../frontend", exclude=["*.png", "*.svg", "node_modules/*", "data.json"])],
            destination_bucket=website_bucket,
            distribution=distribution,
            distribution_paths=["/index.html", "/style.css", "/app.js", "/fonts/*"],
            cache_control=[s3_deploy.CacheControl.set_no_cache()],
            prune=False,
        )

        s3_deploy.BucketDeployment(
            self, "DeployDataJson",
            sources=[s3_deploy.Source.asset("../frontend", exclude=["*", "!data.json"])],
            destination_bucket=website_bucket,
            distribution=distribution,
            distribution_paths=["/data.json"],
            cache_control=[s3_deploy.CacheControl.max_age(Duration.days(365))],
            prune=False,
        )
```

- [ ] **Step 2: Verify CDK synthesis**

Run: `cd cdk && cdk synth`
Expected: PASS (valid CloudFormation template)

- [ ] **Step 3: Commit CDK changes**

```bash
git add cdk/watchdog_stack.py
git commit -m "infra: enable long-term cache for data.json in CDK"
```
