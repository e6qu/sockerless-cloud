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

variable "retention" {
  type = number
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    kms = var.endpoint
    rds = var.endpoint
    sts = var.endpoint
  }
}

resource "aws_kms_key" "insights" {
  description             = "Performance Insights"
  deletion_window_in_days = 7
}

resource "aws_db_instance" "insights" {
  identifier                            = "tf-pi-db"
  instance_class                        = "db.t3.micro"
  engine                                = "postgres"
  username                              = "dbadmin"
  password                              = "password123!"
  allocated_storage                     = 20
  skip_final_snapshot                   = true
  apply_immediately                     = true
  performance_insights_enabled          = true
  performance_insights_kms_key_id       = aws_kms_key.insights.arn
  performance_insights_retention_period = var.retention
}

output "enabled" {
  value = tostring(aws_db_instance.insights.performance_insights_enabled)
}

output "retention" {
  value = tostring(aws_db_instance.insights.performance_insights_retention_period)
}

output "kms_key_matches" {
  value = tostring(aws_db_instance.insights.performance_insights_kms_key_id == aws_kms_key.insights.arn)
}

output "resource_id" {
  value = aws_db_instance.insights.resource_id
}
