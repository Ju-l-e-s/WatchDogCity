#!/usr/bin/env bash
# Applies security settings to the existing S3 bucket without recreating it.
# Run once manually: bash docs/secure-bucket.sh
# Requires: aws cli + watchdog-admin profile with s3 permissions.

set -euo pipefail

BUCKET="watchdogstack-websitebucket75c24d94-clsmaf2ocvxq"
PROFILE="watchdog-admin"

echo "→ Enabling versioning on $BUCKET..."
aws s3api put-bucket-versioning \
  --bucket "$BUCKET" \
  --versioning-configuration Status=Enabled \
  --profile "$PROFILE"

echo "→ Applying lifecycle rule (expire old versions after 30 days)..."
aws s3api put-bucket-lifecycle-configuration \
  --bucket "$BUCKET" \
  --profile "$PROFILE" \
  --lifecycle-configuration '{
    "Rules": [{
      "ID": "expire-old-versions",
      "Status": "Enabled",
      "Filter": {"Prefix": ""},
      "NoncurrentVersionExpiration": {"NoncurrentDays": 30},
      "Expiration": {"ExpiredObjectDeleteMarker": true}
    }]
  }'

echo "→ Enabling default encryption (SSE-S3)..."
aws s3api put-bucket-encryption \
  --bucket "$BUCKET" \
  --profile "$PROFILE" \
  --server-side-encryption-configuration '{
    "Rules": [{
      "ApplyServerSideEncryptionByDefault": {"SSEAlgorithm": "AES256"},
      "BucketKeyEnabled": true
    }]
  }'

echo "✓ Done. Bucket $BUCKET is now versioned, encrypted and has a lifecycle rule."
echo ""
echo "To verify:"
echo "  aws s3api get-bucket-versioning --bucket $BUCKET --profile $PROFILE"
echo "  aws s3api get-bucket-lifecycle-configuration --bucket $BUCKET --profile $PROFILE"
echo "  aws s3api get-bucket-encryption --bucket $BUCKET --profile $PROFILE"
