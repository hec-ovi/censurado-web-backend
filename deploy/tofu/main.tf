# Off-site backup target for the Litestream sidecar, the one piece of
# infrastructure that is genuinely portable: the same module provisions the
# backup bucket on AWS S3 or on any S3-compatible store. Credentials come from
# the environment (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY); for a non-AWS
# store also set var.s3_endpoint. Wire the outputs into deploy/.env as
# LITESTREAM_S3_BUCKET / LITESTREAM_S3_REGION / LITESTREAM_S3_ENDPOINT.

provider "aws" {
  region = var.region

  # When pointed at an S3-compatible store (var.s3_endpoint set), skip the
  # AWS-only preflight checks and region validation that do not apply there.
  skip_credentials_validation = var.s3_endpoint != ""
  skip_requesting_account_id  = var.s3_endpoint != ""
  skip_metadata_api_check     = var.s3_endpoint != ""
  skip_region_validation      = var.s3_endpoint != ""

  endpoints {
    s3 = var.s3_endpoint
  }
}

resource "aws_s3_bucket" "backup" {
  bucket = var.backup_bucket
  tags   = var.tags
}

# Keep prior generations so a bad replication cannot silently overwrite a good
# backup; pair this with the restore drill, which is what actually proves them.
resource "aws_s3_bucket_versioning" "backup" {
  bucket = aws_s3_bucket.backup.id

  versioning_configuration {
    status = "Enabled"
  }
}

# The backup bucket holds the database; it must never be public.
resource "aws_s3_bucket_public_access_block" "backup" {
  bucket = aws_s3_bucket.backup.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
