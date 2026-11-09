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

variable "band_width" {
  type = number
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true

  endpoints {
    cloudwatch     = var.endpoint
    cloudwatchlogs = var.endpoint
    sts            = var.endpoint
  }
}

resource "aws_cloudwatch_log_group" "app" {
  name = "/tf/cloudwatch-anomaly"
}

resource "aws_cloudwatch_log_anomaly_detector" "app" {
  detector_name           = "tf-anomalies"
  log_group_arn_list      = [aws_cloudwatch_log_group.app.arn]
  evaluation_frequency    = "FIVE_MIN"
  anomaly_visibility_time = 14
  enabled                 = true
}

resource "aws_cloudwatch_metric_alarm" "latency" {
  alarm_name          = "tf-latency-band"
  comparison_operator = "LessThanLowerOrGreaterThanUpperThreshold"
  evaluation_periods  = 2
  threshold_metric_id = "ad1"
  treat_missing_data  = "notBreaching"

  metric_query {
    id          = "m1"
    return_data = true
    metric {
      namespace   = "TF/Anomaly"
      metric_name = "Latency"
      period      = 300
      stat        = "Average"
    }
  }

  metric_query {
    id          = "ad1"
    expression  = "ANOMALY_DETECTION_BAND(m1, ${var.band_width})"
    label       = "Latency (expected)"
    return_data = true
  }
}

resource "aws_cloudwatch_metric_alarm" "error_rate" {
  alarm_name          = "tf-error-rate"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  threshold           = 5
  treat_missing_data  = "notBreaching"

  metric_query {
    id = "errors"
    metric {
      namespace   = "TF/Anomaly"
      metric_name = "Errors"
      period      = 60
      stat        = "Sum"
    }
  }

  metric_query {
    id = "requests"
    metric {
      namespace   = "TF/Anomaly"
      metric_name = "Requests"
      period      = 60
      stat        = "Sum"
    }
  }

  metric_query {
    id          = "rate"
    expression  = "IF(requests > 0, FILL(errors, 0) / requests * 100, 0)"
    label       = "Error rate"
    return_data = true
  }
}

resource "aws_cloudwatch_metric_alarm" "error_rate_band" {
  alarm_name          = "tf-error-rate-band"
  comparison_operator = "GreaterThanUpperThreshold"
  evaluation_periods  = 1
  threshold_metric_id = "ad1"
  treat_missing_data  = "notBreaching"

  metric_query {
    id = "errors"
    metric {
      namespace   = "TF/Anomaly"
      metric_name = "Errors"
      period      = 60
      stat        = "Sum"
    }
  }

  metric_query {
    id = "requests"
    metric {
      namespace   = "TF/Anomaly"
      metric_name = "Requests"
      period      = 60
      stat        = "Sum"
    }
  }

  metric_query {
    id          = "rate"
    expression  = "errors / requests * 100"
    return_data = true
  }

  metric_query {
    id          = "ad1"
    expression  = "ANOMALY_DETECTION_BAND(rate, ${var.band_width})"
    return_data = true
  }
}

resource "aws_cloudwatch_contributor_insight_rule" "talkers" {
  rule_name  = "tf-top-talkers"
  rule_state = "ENABLED"
  rule_definition = jsonencode({
    Schema        = { Name = "CloudWatchLogRule", Version = 1 }
    LogGroupNames = [aws_cloudwatch_log_group.app.name]
    LogFormat     = "JSON"
    Contribution  = { Keys = ["$.ip"] }
    AggregateOn   = "Count"
  })
}

output "detector_arn" {
  value = aws_cloudwatch_log_anomaly_detector.app.arn
}

output "alarm_threshold_metric_id" {
  value = aws_cloudwatch_metric_alarm.latency.threshold_metric_id
}

output "error_rate_expression" {
  value = [for q in aws_cloudwatch_metric_alarm.error_rate.metric_query : q.expression if q.id == "rate"][0]
}

output "error_rate_band_threshold_metric_id" {
  value = aws_cloudwatch_metric_alarm.error_rate_band.threshold_metric_id
}

output "rule_arn" {
  value = aws_cloudwatch_contributor_insight_rule.talkers.resource_arn
}
