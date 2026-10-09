terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.68.0"
    }
  }
}

variable "endpoint" {
  type = string
}

# Whether destroying the GOVERNANCE-retained object bypasses governance
# retention.
variable "force_destroy" {
  type    = bool
  default = false
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    s3 = var.endpoint
  }
}

# Object Lock at creation turns versioning on with it. force_destroy deletes
# every version bypassing governance retention, lifting legal holds first.
resource "aws_s3_bucket" "locked" {
  bucket              = "tf-s3-object-lock"
  object_lock_enabled = true
  force_destroy       = true
}

resource "aws_s3_bucket_object_lock_configuration" "locked" {
  bucket = aws_s3_bucket.locked.id
  rule {
    default_retention {
      mode = "GOVERNANCE"
      days = 1
    }
  }
}

resource "aws_s3_object" "defaulted" {
  bucket        = aws_s3_bucket.locked.id
  key           = "defaulted.txt"
  content       = "takes the default retention"
  force_destroy = true

  depends_on = [aws_s3_bucket_object_lock_configuration.locked]
}

resource "aws_s3_object" "governed" {
  bucket                        = aws_s3_bucket.locked.id
  key                           = "governed.txt"
  content                       = "governance retention"
  object_lock_mode              = "GOVERNANCE"
  object_lock_retain_until_date = "2999-01-01T00:00:00Z"
  force_destroy                 = var.force_destroy
}

resource "aws_s3_object" "held" {
  bucket                        = aws_s3_bucket.locked.id
  key                           = "held.txt"
  content                       = "legal hold"
  object_lock_legal_hold_status = "ON"
  force_destroy                 = true
}

data "aws_s3_object" "defaulted" {
  bucket = aws_s3_bucket.locked.id
  key    = aws_s3_object.defaulted.key
}

output "defaulted_version_id" {
  value = aws_s3_object.defaulted.version_id
}

output "default_mode" {
  value = data.aws_s3_object.defaulted.object_lock_mode
}

output "default_retain_until" {
  value = data.aws_s3_object.defaulted.object_lock_retain_until_date
}

output "governed_mode" {
  value = aws_s3_object.governed.object_lock_mode
}

output "held_status" {
  value = aws_s3_object.held.object_lock_legal_hold_status
}
