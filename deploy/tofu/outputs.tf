output "backup_bucket" {
  description = "Bucket name; set as LITESTREAM_S3_BUCKET in deploy/.env."
  value       = aws_s3_bucket.backup.id
}

output "backup_region" {
  description = "Region; set as LITESTREAM_S3_REGION in deploy/.env."
  value       = var.region
}

output "backup_endpoint" {
  description = "S3-compatible endpoint; set as LITESTREAM_S3_ENDPOINT in deploy/.env (empty for AWS S3)."
  value       = var.s3_endpoint
}
