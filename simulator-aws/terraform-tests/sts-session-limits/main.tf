terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.71.0"
    }
  }
}

variable "endpoint" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    iam = var.endpoint
    sts = var.endpoint
  }
}

# The boundary lets the role's sessions use Amazon S3 and nothing else.
resource "aws_iam_policy" "boundary" {
  name = "tf-session-limits-boundary"
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "s3:*", Resource = "*" }]
  })
}

resource "aws_iam_role" "capped" {
  name                 = "tf-session-limits"
  max_session_duration = 7200
  permissions_boundary = aws_iam_policy.boundary.arn
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:aws:iam::123456789012:root" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# The role's own policy allows Amazon S3 and Amazon SQS.
resource "aws_iam_role_policy" "storage" {
  name = "storage"
  role = aws_iam_role.capped.name
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["s3:*", "sqs:*"], Resource = "*" }]
  })
}

output "role_arn" {
  value = aws_iam_role.capped.arn
}

output "max_session_duration" {
  value = aws_iam_role.capped.max_session_duration
}
