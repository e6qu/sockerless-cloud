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

resource "aws_rds_cluster" "source" {
  cluster_identifier  = "tf-aurora-snapshot-source"
  engine              = "aurora-postgresql"
  master_username     = "dbadmin"
  master_password     = "MasterPassword-123!"
  database_name       = "application"
  skip_final_snapshot = true
}

resource "aws_db_cluster_snapshot" "snapshot" {
  db_cluster_identifier          = aws_rds_cluster.source.cluster_identifier
  db_cluster_snapshot_identifier = "tf-aurora-snapshot"

  tags = {
    env = "terraform"
  }
}

resource "aws_rds_cluster" "restored" {
  cluster_identifier  = "tf-aurora-snapshot-restored"
  engine              = "aurora-postgresql"
  snapshot_identifier = aws_db_cluster_snapshot.snapshot.db_cluster_snapshot_arn
  skip_final_snapshot = true
}

output "snapshot_arn" {
  value = aws_db_cluster_snapshot.snapshot.db_cluster_snapshot_arn
}
output "snapshot_status" {
  value = aws_db_cluster_snapshot.snapshot.status
}
output "snapshot_tags_env" {
  value = aws_db_cluster_snapshot.snapshot.tags["env"]
}
output "restored_arn" {
  value = aws_rds_cluster.restored.arn
}
output "restored_master_username" {
  value = aws_rds_cluster.restored.master_username
}
output "restored_database_name" {
  value = aws_rds_cluster.restored.database_name
}
