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

variable "source_cluster_identifier" {
  type = string
}

variable "restore_to_time" {
  type = string
}

variable "db_snapshot_arn" {
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

resource "aws_rds_cluster" "point_in_time" {
  cluster_identifier  = "tf-aurora-pitr-restored"
  engine              = "aurora-postgresql"
  skip_final_snapshot = true

  restore_to_point_in_time {
    source_cluster_identifier = var.source_cluster_identifier
    restore_to_time           = var.restore_to_time
  }
}

resource "aws_rds_cluster" "migrated" {
  cluster_identifier  = "tf-aurora-migrated"
  engine              = "aurora-postgresql"
  snapshot_identifier = var.db_snapshot_arn
  skip_final_snapshot = true
}

output "point_in_time_arn" {
  value = aws_rds_cluster.point_in_time.arn
}
output "point_in_time_master_username" {
  value = aws_rds_cluster.point_in_time.master_username
}
output "migrated_arn" {
  value = aws_rds_cluster.migrated.arn
}
output "migrated_engine" {
  value = aws_rds_cluster.migrated.engine
}
output "migrated_master_username" {
  value = aws_rds_cluster.migrated.master_username
}
