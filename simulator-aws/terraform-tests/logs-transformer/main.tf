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

variable "environment" {
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
    sts            = var.endpoint
  }
}

resource "aws_cloudwatch_log_group" "app" {
  name = "/tf/logs-transformer"
}

resource "aws_cloudwatch_log_transformer" "app" {
  log_group_arn = aws_cloudwatch_log_group.app.arn

  transformer_config {
    parse_json {}
  }
  transformer_config {
    add_keys {
      entry {
        key   = "metadata.environment"
        value = var.environment
      }
    }
  }
  transformer_config {
    lower_case_string {
      with_keys = ["level"]
    }
  }
  transformer_config {
    type_converter {
      entry {
        key  = "status"
        type = "integer"
      }
    }
  }
  transformer_config {
    date_time_converter {
      source         = "time"
      target         = "time_utc"
      match_patterns = ["dd/MMM/yyyy:HH:mm:ss Z"]
      locale         = "en"
    }
  }
  transformer_config {
    copy_value {
      entry {
        source = "@logGroupName"
        target = "group"
      }
    }
  }
}

output "log_group_arn" {
  value = aws_cloudwatch_log_transformer.app.log_group_arn
}

output "processor_count" {
  value = tostring(length(aws_cloudwatch_log_transformer.app.transformer_config))
}

output "environment" {
  value = aws_cloudwatch_log_transformer.app.transformer_config[1].add_keys[0].entry[0].value
}

output "date_pattern" {
  value = aws_cloudwatch_log_transformer.app.transformer_config[4].date_time_converter[0].match_patterns[0]
}
