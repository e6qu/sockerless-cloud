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

variable "bucket" {
  type = string
}

variable "ingestion_role" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    rds = var.endpoint
  }
}

resource "aws_rds_cluster" "imported" {
  cluster_identifier      = "tf-aurora-s3-import"
  engine                  = "aurora-mysql"
  master_username         = "dbadmin"
  master_password         = "MasterPassword-123!"
  database_name           = "application"
  preferred_backup_window = "03:00-03:30"
  skip_final_snapshot     = true

  s3_import {
    source_engine         = "mysql"
    source_engine_version = "8.0.40"
    bucket_name           = var.bucket
    bucket_prefix         = "backups"
    ingestion_role        = var.ingestion_role
  }
}

output "imported_arn" {
  value = aws_rds_cluster.imported.arn
}
output "imported_backup_window" {
  value = aws_rds_cluster.imported.preferred_backup_window
}
