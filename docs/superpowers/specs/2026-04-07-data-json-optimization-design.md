# Design Spec: data.json Optimization (Long Cache + Manual Invalidation)

**Status:** Approved
**Date:** 2026-04-07
**Author:** Gemini CLI

## 1. Goal
Optimize the delivery of `data.json` by using aggressive caching headers (`max-age=31536000, immutable`) and relying on manual CloudFront invalidations triggered by the Publisher Lambda. This reduces token/bandwidth usage and improves load times.

## 2. Technical Strategy

### 2.1. Publisher (Go)
The Publisher Lambda is responsible for updating the data. It must set the correct metadata on S3 to ensure CloudFront and browsers respect the long-lived cache.

- **Change:** Add `CacheControl` to `s3.PutObjectInput` in `lambdas/publisher/handler.go`.
- **Value:** `public, max-age=31536000, immutable`.

### 2.2. Infrastructure (CDK)
The CDK stack manages the initial/static deployment of the frontend.

- **Change:** Update `DeployWebsiteConfig` in `cdk/watchdog_stack.py`.
- **Value:** Replace `s3_deploy.CacheControl.set_no_cache()` with `s3_deploy.CacheControl.max_age(Duration.days(365))`.
- **Note:** Ensure `data.json` is specifically targeted with this long-lived cache in the CDK deployment logic.

## 3. Validation Plan
1. **Local Test**: Verify the Go code compiles with the new field.
2. **Infrastructure Verification**: Check `cdk synth` output for the updated `CacheControl`.
3. **End-to-End (Manual)**: After deployment, verify headers via `curl -I <CloudFront-URL>/data.json`.
