terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.72.0"
    }
  }
}

variable "endpoint" {
  type = string
}

variable "role_arn" {
  type = string
}

variable "duration" {
  type = string
}

variable "session_policy" {
  type    = string
  default = null
}

variable "create_queue" {
  type    = bool
  default = false
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    s3  = var.endpoint
    sqs = var.endpoint
    sts = var.endpoint
  }

  # The provider works as a session of the capped role.
  assume_role {
    role_arn     = var.role_arn
    session_name = "tf-session-limits"
    duration     = var.duration
    policy       = var.session_policy
  }
}

resource "aws_s3_bucket" "capped" {
  bucket        = "tf-session-limits"
  force_destroy = true
}

resource "aws_sqs_queue" "capped" {
  count = var.create_queue ? 1 : 0
  name  = "tf-session-limits"
}

output "bucket_arn" {
  value = aws_s3_bucket.capped.arn
}
