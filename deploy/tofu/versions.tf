terraform {
  required_version = ">= 1.6"

  required_providers {
    # The S3 API is identical across AWS S3, Cloudflare R2, Backblaze B2, and
    # MinIO, so the aws provider provisions the backup bucket for any of them
    # (set var.s3_endpoint for the non-AWS ones). This is the only provider this
    # skeleton needs; CDN and compute are vendor-specific and left to you.
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}
