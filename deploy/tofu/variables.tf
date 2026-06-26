variable "backup_bucket" {
  description = "Name of the object-storage bucket Litestream replicates the sqlite database to."
  type        = string
}

variable "region" {
  description = "Bucket region. Use \"auto\" for Cloudflare R2; a real region for AWS S3."
  type        = string
  default     = "auto"
}

variable "s3_endpoint" {
  description = "S3-compatible endpoint URL. Leave empty for AWS S3; set it for R2, B2, or MinIO."
  type        = string
  default     = ""
}

variable "tags" {
  description = "Tags applied to created resources."
  type        = map(string)
  default     = {}
}
