terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.67.0"
    }
  }
}

variable "endpoint" {
  type = string
}

variable "role_arn" {
  type = string
}

variable "team" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    s3  = var.endpoint
    sts = var.endpoint
  }

  # The provider works as the role, in a session tagged with the team.
  assume_role {
    role_arn            = var.role_arn
    session_name        = "tf-session-tags"
    tags                = { team = var.team }
    transitive_tag_keys = ["team"]
  }
}

resource "aws_s3_bucket" "team" {
  bucket        = "tf-session-tags-${var.team}"
  force_destroy = true
}

output "bucket_arn" {
  value = aws_s3_bucket.team.arn
}
