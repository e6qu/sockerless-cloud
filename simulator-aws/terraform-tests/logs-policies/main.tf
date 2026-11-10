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

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    cloudwatchlogs = var.endpoint
    events         = var.endpoint
    sts            = var.endpoint
  }
}

resource "aws_cloudwatch_log_group" "app" {
  name = "/tf/logs-policies/app"
}

resource "aws_cloudwatch_log_group" "own_index" {
  name = "/tf/logs-policies/own-index"
}

resource "aws_cloudwatch_log_group" "findings" {
  name = "/tf/logs-policies-findings"
}

locals {
  email = "arn:aws:dataprotection::aws:data-identifier/EmailAddress"
}

resource "aws_cloudwatch_log_data_protection_policy" "app" {
  log_group_name = aws_cloudwatch_log_group.app.name
  policy_document = jsonencode({
    Name    = "tf-data-protection"
    Version = "2021-06-01"
    Statement = [
      {
        Sid            = "audit"
        DataIdentifier = [local.email]
        Operation = {
          Audit = {
            FindingsDestination = {
              CloudWatchLogs = { LogGroup = aws_cloudwatch_log_group.findings.name }
            }
          }
        }
      },
      {
        Sid            = "redact"
        DataIdentifier = [local.email]
        Operation      = { Deidentify = { MaskConfig = {} } }
      },
    ]
  })
}

resource "aws_cloudwatch_log_account_policy" "data_protection" {
  policy_name = "tf-account-data-protection"
  policy_type = "DATA_PROTECTION_POLICY"
  policy_document = jsonencode({
    Name          = "tf-account-data-protection"
    Version       = "2021-06-01"
    Configuration = { CustomDataIdentifier = [{ Name = "TfToken", Regex = "TfToken-\\d{6}" }] }
    Statement = [
      {
        Sid            = "audit"
        DataIdentifier = ["TfToken"]
        Operation      = { Audit = { FindingsDestination = {} } }
      },
      {
        Sid            = "redact"
        DataIdentifier = ["TfToken"]
        Operation      = { Deidentify = { MaskConfig = {} } }
      },
    ]
  })
}

resource "aws_cloudwatch_log_account_policy" "field_index" {
  policy_name        = "tf-field-index"
  policy_type        = "FIELD_INDEX_POLICY"
  policy_document    = jsonencode({ Fields = ["requestId"] })
  selection_criteria = "LogGroupNamePrefix IN [\"/tf/logs-policies/\"]"
}

resource "aws_cloudwatch_log_index_policy" "own" {
  log_group_name  = aws_cloudwatch_log_group.own_index.name
  policy_document = jsonencode({ FieldsV2 = { sessionId = { type = "FACET" } } })
}

resource "aws_cloudwatch_log_account_policy" "metric_extraction" {
  policy_name        = "tf-disable-emf"
  policy_type        = "METRIC_EXTRACTION_POLICY"
  policy_document    = jsonencode({ EmbeddedMetricFormat = { Status = "Disabled" } })
  selection_criteria = "LogGroupName IN [\"${aws_cloudwatch_log_group.app.name}\"]"
}

resource "aws_cloudwatch_log_group" "orders" {
  name = "/tf/logs-policies/orders"
  tags = {
    "cw:datasource:name" = "tfapp"
    "cw:datasource:type" = "orders"
  }
}

resource "aws_cloudwatch_log_account_policy" "data_source_index" {
  policy_name        = "tf-data-source-index"
  policy_type        = "FIELD_INDEX_POLICY"
  policy_document    = jsonencode({ Fields = ["orderId"] })
  selection_criteria = "DataSourceName = \"tfapp\" AND DataSourceType = \"orders\""
}

resource "aws_cloudwatch_log_group" "events" {
  name = "/aws/events/tf-logs-policies"
}

resource "aws_cloudwatch_event_rule" "to_logs" {
  name          = "tf-logs-policies-to-logs"
  event_pattern = jsonencode({ source = ["tf.logs-policies"] })
}

resource "aws_cloudwatch_log_resource_policy" "eventbridge" {
  policy_name = "tf-eventbridge-to-logs"
  policy_document = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = ["events.amazonaws.com", "delivery.logs.amazonaws.com"] }
      Action    = ["logs:CreateLogStream", "logs:PutLogEvents"]
      Resource  = "${aws_cloudwatch_log_group.events.arn}:*"
      Condition = { ArnEquals = { "aws:SourceArn" = aws_cloudwatch_event_rule.to_logs.arn } }
    }]
  })
}

resource "aws_cloudwatch_event_target" "logs" {
  rule       = aws_cloudwatch_event_rule.to_logs.name
  target_id  = "logs"
  arn        = aws_cloudwatch_log_group.events.arn
  depends_on = [aws_cloudwatch_log_resource_policy.eventbridge]
}

output "data_source_selection" {
  value = aws_cloudwatch_log_account_policy.data_source_index.selection_criteria
}

output "log_group_target" {
  value = aws_cloudwatch_event_target.logs.arn
}

output "data_protection_group" {
  value = aws_cloudwatch_log_data_protection_policy.app.log_group_name
}

output "index_policy_group" {
  value = aws_cloudwatch_log_index_policy.own.log_group_name
}

output "field_index_selection" {
  value = aws_cloudwatch_log_account_policy.field_index.selection_criteria
}
