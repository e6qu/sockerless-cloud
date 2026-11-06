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

# The management account's root user signs every request.
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "root"
  secret_key                  = "root"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    iam = var.endpoint
    sts = var.endpoint
  }
}

data "aws_caller_identity" "root" {}

resource "aws_iam_user" "made_by_root" {
  name = "tf-made-by-root"
}

output "caller_arn" {
  value = data.aws_caller_identity.root.arn
}

output "account_id" {
  value = data.aws_caller_identity.root.account_id
}

output "user_arn" {
  value = aws_iam_user.made_by_root.arn
}
