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

variable "success_sample_rate" {
  type = number
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    iam = var.endpoint
    sns = var.endpoint
    sts = var.endpoint
  }
}

resource "aws_iam_role" "feedback" {
  name = "tf-sns-delivery-status"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "sns.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "feedback" {
  name = "delivery-status-logs"
  role = aws_iam_role.feedback.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents", "logs:PutMetricFilter", "logs:PutRetentionPolicy"]
      Resource = "*"
    }]
  })
}

data "aws_caller_identity" "current" {}

resource "aws_sns_topic" "logged" {
  name = "tf-sns-delivery-status"

  # The owner-only policy Amazon SNS gives a new topic.
  policy = jsonencode({
    Version = "2008-10-17"
    Id      = "__default_policy_ID"
    Statement = [{
      Sid       = "__default_statement_ID"
      Effect    = "Allow"
      Principal = { AWS = "*" }
      Action    = ["SNS:GetTopicAttributes", "SNS:SetTopicAttributes", "SNS:AddPermission", "SNS:RemovePermission", "SNS:DeleteTopic", "SNS:Subscribe", "SNS:ListSubscriptionsByTopic", "SNS:Publish"]
      Resource  = "arn:aws:sns:us-east-1:${data.aws_caller_identity.current.account_id}:tf-sns-delivery-status"
      Condition = { StringEquals = { "AWS:SourceOwner" = data.aws_caller_identity.current.account_id } }
    }]
  })

  lambda_success_feedback_role_arn    = aws_iam_role.feedback.arn
  lambda_success_feedback_sample_rate = var.success_sample_rate
  lambda_failure_feedback_role_arn    = aws_iam_role.feedback.arn
  sqs_success_feedback_role_arn       = aws_iam_role.feedback.arn
  sqs_success_feedback_sample_rate    = var.success_sample_rate
  sqs_failure_feedback_role_arn       = aws_iam_role.feedback.arn

  depends_on = [aws_iam_role_policy.feedback]
}

output "role_arn" {
  value = aws_iam_role.feedback.arn
}

output "lambda_success_feedback_role_arn" {
  value = aws_sns_topic.logged.lambda_success_feedback_role_arn
}

output "lambda_success_feedback_sample_rate" {
  value = tostring(aws_sns_topic.logged.lambda_success_feedback_sample_rate)
}

output "lambda_failure_feedback_role_arn" {
  value = aws_sns_topic.logged.lambda_failure_feedback_role_arn
}

output "sqs_success_feedback_sample_rate" {
  value = tostring(aws_sns_topic.logged.sqs_success_feedback_sample_rate)
}

output "sqs_failure_feedback_role_arn" {
  value = aws_sns_topic.logged.sqs_failure_feedback_role_arn
}
