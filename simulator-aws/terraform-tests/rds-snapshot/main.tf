terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.50.0"
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

resource "aws_db_instance" "tf_rds" {
  identifier          = "tf-rds-snapshot-source"
  instance_class      = "db.t3.micro"
  engine              = "postgres"
  engine_version      = "17.5"
  username            = "admin"
  password            = "password123!"
  allocated_storage   = 20
  skip_final_snapshot = true
  apply_immediately   = true
}

resource "aws_db_snapshot" "tf_rds_snapshot" {
  db_instance_identifier = aws_db_instance.tf_rds.identifier
  db_snapshot_identifier = "tf-rds-snapshot"

  tags = {
    env = "terraform"
  }
}

output "rds_snapshot_arn" {
  value = aws_db_snapshot.tf_rds_snapshot.db_snapshot_arn
}
output "rds_snapshot_status" {
  value = aws_db_snapshot.tf_rds_snapshot.status
}
output "rds_snapshot_tags_env" {
  value = aws_db_snapshot.tf_rds_snapshot.tags["env"]
}
