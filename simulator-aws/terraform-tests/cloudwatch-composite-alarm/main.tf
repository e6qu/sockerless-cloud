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

variable "alarm_rule" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    cloudwatch = var.endpoint
  }
}

resource "aws_cloudwatch_metric_alarm" "child" {
  alarm_name          = "tf-composite-rule-child"
  namespace           = "Custom/CompositeRuleTerraform"
  metric_name         = "Errors"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  period              = 60
  threshold           = 0
  statistic           = "Sum"
  treat_missing_data  = "notBreaching"
}

resource "aws_cloudwatch_composite_alarm" "parent" {
  alarm_name = "tf-composite-rule-parent"
  alarm_rule = var.alarm_rule
  depends_on = [aws_cloudwatch_metric_alarm.child]
}
