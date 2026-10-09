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

variable "mariadb_restore_time" {
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

resource "aws_db_instance" "tf_rds_restored" {
  identifier          = "tf-rds-restored"
  instance_class      = "db.t3.micro"
  snapshot_identifier = "tf-rds-snapshot-source"
  skip_final_snapshot = true
  apply_immediately   = true

  tags = {
    env = "terraform"
  }
}

resource "aws_db_instance" "tf_rds_point_in_time" {
  identifier               = "tf-rds-point-in-time"
  instance_class           = "db.t3.micro"
  skip_final_snapshot      = true
  delete_automated_backups = false

  restore_to_point_in_time {
    source_db_instance_identifier = "tf-rds-restore-source"
    use_latest_restorable_time    = true
  }
}

output "rds_point_in_time_resource_id" {
  value = aws_db_instance.tf_rds_point_in_time.resource_id
}
output "rds_point_in_time_engine" {
  value = aws_db_instance.tf_rds_point_in_time.engine
}
output "rds_point_in_time_backup_retention_period" {
  value = tostring(aws_db_instance.tf_rds_point_in_time.backup_retention_period)
}
output "rds_restored_instance_arn" {
  value = aws_db_instance.tf_rds_restored.arn
}
output "rds_restored_instance_engine" {
  value = aws_db_instance.tf_rds_restored.engine
}
output "rds_restored_instance_tags_env" {
  value = aws_db_instance.tf_rds_restored.tags["env"]
}

resource "aws_db_instance" "tf_rds_mariadb_point_in_time" {
  identifier          = "tf-rds-mariadb-point-in-time"
  instance_class      = "db.t3.micro"
  skip_final_snapshot = true

  restore_to_point_in_time {
    source_db_instance_identifier = "tf-rds-mariadb-source"
    restore_time                  = var.mariadb_restore_time
  }
}

output "rds_mariadb_point_in_time_engine" {
  value = aws_db_instance.tf_rds_mariadb_point_in_time.engine
}
output "rds_mariadb_point_in_time_address" {
  value = aws_db_instance.tf_rds_mariadb_point_in_time.address
}
output "rds_mariadb_point_in_time_port" {
  value = tostring(aws_db_instance.tf_rds_mariadb_point_in_time.port)
}
