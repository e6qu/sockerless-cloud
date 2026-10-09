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

variable "content" {
  type = string
}

# The version id of an earlier write to read back, empty for none.
variable "earlier_version" {
  type    = string
  default = ""
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

# force_destroy empties the bucket of every version and delete marker before
# deleting it, which DeleteBucket refuses while any remains.
resource "aws_s3_bucket" "versioned" {
  bucket        = "tf-s3-versioning"
  force_destroy = true
}

resource "aws_s3_bucket_versioning" "versioned" {
  bucket = aws_s3_bucket.versioned.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_object" "doc" {
  bucket       = aws_s3_bucket.versioned.id
  key          = "doc.txt"
  content      = var.content
  content_type = "text/plain"

  depends_on = [aws_s3_bucket_versioning.versioned]
}

data "aws_s3_object" "earlier" {
  count      = var.earlier_version == "" ? 0 : 1
  bucket     = aws_s3_bucket.versioned.id
  key        = aws_s3_object.doc.key
  version_id = var.earlier_version
}

output "version_id" {
  value = aws_s3_object.doc.version_id
}

output "earlier_body" {
  value = var.earlier_version == "" ? "" : data.aws_s3_object.earlier[0].body
}
