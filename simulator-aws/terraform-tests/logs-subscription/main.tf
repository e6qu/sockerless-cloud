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

variable "lambda_architecture" {
  type = string
}

variable "deletion_protection" {
  type = bool
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    cloudwatchlogs = var.endpoint
    firehose       = var.endpoint
    iam            = var.endpoint
    kinesis        = var.endpoint
    lambda         = var.endpoint
    s3             = var.endpoint
    sqs            = var.endpoint
    sts            = var.endpoint
  }
}

resource "aws_cloudwatch_log_group" "app" {
  name                        = "/tf/logs-subscription"
  deletion_protection_enabled = var.deletion_protection
}

resource "aws_cloudwatch_log_group" "archive" {
  name            = "/tf/logs-subscription-archive"
  log_group_class = "INFREQUENT_ACCESS"
}

# The role CloudWatch Logs writes to the streams with, as the subscription
# guide creates it.
resource "aws_iam_role" "cwl" {
  name = "tf-cwl-to-streams"

  assume_role_policy = jsonencode({
    Statement = {
      Effect    = "Allow"
      Principal = { Service = "logs.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = { ArnLike = { "aws:SourceArn" = "arn:aws:logs:us-east-1:123456789012:*" } }
    }
  })
}

resource "aws_iam_role_policy" "cwl" {
  name = "Permissions-Policy-For-CWL"
  role = aws_iam_role.cwl.id
  policy = jsonencode({
    Statement = [
      {
        Effect   = "Allow"
        Action   = "kinesis:PutRecord"
        Resource = [aws_kinesis_stream.root_access.arn, aws_kinesis_stream.recipient.arn]
      },
      {
        Effect   = "Allow"
        Action   = "firehose:PutRecord"
        Resource = aws_kinesis_firehose_delivery_stream.archive.arn
      },
    ]
  })
}

resource "aws_kinesis_stream" "root_access" {
  name        = "tf-RootAccess"
  shard_count = 1
}

resource "aws_cloudwatch_log_subscription_filter" "kinesis" {
  name            = "RootAccess"
  log_group_name  = aws_cloudwatch_log_group.app.name
  filter_pattern  = "{$.userIdentity.type = Root}"
  destination_arn = aws_kinesis_stream.root_access.arn
  role_arn        = aws_iam_role.cwl.arn
  distribution    = "Random"
  depends_on      = [aws_iam_role_policy.cwl]
}

resource "aws_s3_bucket" "archive" {
  bucket        = "tf-logs-subscription-archive"
  force_destroy = true
}

resource "aws_iam_role" "firehose" {
  name = "tf-logs-subscription-firehose"

  assume_role_policy = jsonencode({
    Statement = {
      Effect    = "Allow"
      Principal = { Service = "firehose.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }
  })
}

resource "aws_iam_role_policy" "firehose" {
  name = "Permissions-Policy-For-Firehose"
  role = aws_iam_role.firehose.id
  policy = jsonencode({
    Statement = [{
      Effect   = "Allow"
      Action   = ["s3:GetBucketLocation", "s3:ListBucket", "s3:PutObject"]
      Resource = [aws_s3_bucket.archive.arn, "${aws_s3_bucket.archive.arn}/*"]
    }]
  })
}

resource "aws_kinesis_firehose_delivery_stream" "archive" {
  name        = "tf-logs-subscription-archive"
  destination = "extended_s3"
  depends_on  = [aws_iam_role_policy.firehose]

  extended_s3_configuration {
    role_arn           = aws_iam_role.firehose.arn
    bucket_arn         = aws_s3_bucket.archive.arn
    prefix             = "firehose/"
    buffering_interval = 0
    buffering_size     = 1
  }
}

resource "aws_cloudwatch_log_subscription_filter" "firehose" {
  name            = "Destination"
  log_group_name  = aws_cloudwatch_log_group.app.name
  filter_pattern  = "ERROR"
  destination_arn = aws_kinesis_firehose_delivery_stream.archive.arn
  role_arn        = aws_iam_role.cwl.arn
  depends_on      = [aws_iam_role_policy.cwl]
}

resource "aws_iam_role" "function" {
  name = "tf-logs-subscription-function"

  assume_role_policy = jsonencode({
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "function" {
  name = "send-to-queue"
  role = aws_iam_role.function.id
  policy = jsonencode({
    Statement = [{
      Effect   = "Allow"
      Action   = "sqs:SendMessage"
      Resource = aws_sqs_queue.invocations.arn
    }]
  })
}

resource "aws_lambda_function" "subscriber" {
  function_name = "tf-logs-subscriber"
  role          = aws_iam_role.function.arn
  package_type  = "Image"
  image_uri     = "123456789012.dkr.ecr.us-east-1.amazonaws.com/sockerless-lambda-runtime-handler:aws-terraform"
  architectures = [var.lambda_architecture]
  timeout       = 30
}

resource "aws_sqs_queue" "invocations" {
  name = "tf-logs-subscriber-invocations"
}

resource "aws_lambda_function_event_invoke_config" "subscriber" {
  function_name          = aws_lambda_function.subscriber.function_name
  maximum_retry_attempts = 0

  destination_config {
    on_success {
      destination = aws_sqs_queue.invocations.arn
    }
  }

  depends_on = [aws_iam_role_policy.function]
}

resource "aws_lambda_permission" "logs" {
  statement_id   = "cloudwatch-logs"
  action         = "lambda:InvokeFunction"
  function_name  = aws_lambda_function.subscriber.function_name
  principal      = "logs.amazonaws.com"
  source_arn     = "${aws_cloudwatch_log_group.app.arn}:*"
  source_account = "123456789012"
}

resource "aws_cloudwatch_log_subscription_filter" "lambda" {
  name            = "demo"
  log_group_name  = aws_cloudwatch_log_group.app.name
  filter_pattern  = "lambda"
  destination_arn = aws_lambda_function.subscriber.arn
  depends_on      = [aws_lambda_permission.logs, aws_lambda_function_event_invoke_config.subscriber]
}

resource "aws_kinesis_stream" "recipient" {
  name        = "tf-RecipientStream"
  shard_count = 1
}

resource "aws_cloudwatch_log_destination" "recipient" {
  name       = "tfDestination"
  target_arn = aws_kinesis_stream.recipient.arn
  role_arn   = aws_iam_role.cwl.arn
  depends_on = [aws_iam_role_policy.cwl]
}

resource "aws_cloudwatch_log_destination_policy" "recipient" {
  destination_name = aws_cloudwatch_log_destination.recipient.name
  access_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "123456789012" }
      Action    = "logs:PutSubscriptionFilter"
      Resource  = aws_cloudwatch_log_destination.recipient.arn
    }]
  })
}

resource "aws_cloudwatch_log_subscription_filter" "destination" {
  name            = "RecipientStream"
  log_group_name  = aws_cloudwatch_log_group.app.name
  filter_pattern  = "shared"
  destination_arn = aws_cloudwatch_log_destination.recipient.arn
  depends_on      = [aws_cloudwatch_log_destination_policy.recipient]
}

output "log_group_arn" {
  value = aws_cloudwatch_log_group.app.arn
}

output "archive_class" {
  value = aws_cloudwatch_log_group.archive.log_group_class
}

output "kinesis_distribution" {
  value = aws_cloudwatch_log_subscription_filter.kinesis.distribution
}

output "firehose_distribution" {
  value = aws_cloudwatch_log_subscription_filter.firehose.distribution
}

output "destination_arn" {
  value = aws_cloudwatch_log_destination.recipient.arn
}
