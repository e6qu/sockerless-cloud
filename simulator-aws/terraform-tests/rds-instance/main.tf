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

provider "aws" {
  alias                       = "destination"
  region                      = "us-west-2"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    rds = var.endpoint
  }
}

resource "aws_db_instance" "tf_rds" {
  identifier              = "tf-rds-db"
  instance_class          = "db.t3.micro"
  engine                  = "postgres"
  engine_version          = "17.5"
  username                = "admin"
  password                = "password123!"
  allocated_storage       = 20
  backup_retention_period = 1
  skip_final_snapshot     = true
  apply_immediately       = true

  tags = {
    env = "terraform"
  }
}

output "rds_instance_arn" {
  value = aws_db_instance.tf_rds.arn
}
output "rds_instance_engine" {
  value = aws_db_instance.tf_rds.engine
}
output "rds_instance_port" {
  value = tostring(aws_db_instance.tf_rds.port)
}
output "rds_instance_tags_env" {
  value = aws_db_instance.tf_rds.tags["env"]
}

resource "aws_db_instance_automated_backups_replication" "tf_rds" {
  provider               = aws.destination
  source_db_instance_arn = aws_db_instance.tf_rds.arn
  retention_period       = 3
}

output "rds_replicated_backup_arn" {
  value = aws_db_instance_automated_backups_replication.tf_rds.id
}
output "rds_replicated_backup_retention_period" {
  value = tostring(aws_db_instance_automated_backups_replication.tf_rds.retention_period)
}

# A replication turns replicating once it holds the source's first automated
# snapshot, which Amazon RDS takes when it creates the instance.
data "aws_db_snapshot" "tf_rds_first_automated" {
  db_instance_identifier = aws_db_instance.tf_rds.identifier
  snapshot_type          = "automated"
  most_recent            = true
  depends_on             = [aws_db_instance_automated_backups_replication.tf_rds]
}

output "rds_first_automated_snapshot_id" {
  value = data.aws_db_snapshot.tf_rds_first_automated.id
}
output "rds_first_automated_snapshot_status" {
  value = data.aws_db_snapshot.tf_rds_first_automated.status
}
