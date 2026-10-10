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

variable "work_mem" {
  type = string
}

variable "instance_class" {
  type = string
}

variable "maintenance_window" {
  type = string
}

variable "backup_window" {
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

resource "aws_db_parameter_group" "engine" {
  name   = "tf-rds-engine-parameters"
  family = "postgres16"

  parameter {
    name         = "work_mem"
    value        = var.work_mem
    apply_method = "immediate"
  }

  parameter {
    name         = "max_connections"
    value        = "150"
    apply_method = "pending-reboot"
  }
}

resource "aws_db_instance" "tf_rds_parameters" {
  identifier              = "tf-rds-parameters"
  instance_class          = var.instance_class
  engine                  = "postgres"
  engine_version          = "16.15"
  parameter_group_name    = aws_db_parameter_group.engine.name
  username                = "dbadmin"
  password                = "MasterPassword-123!"
  db_name                 = "application"
  allocated_storage       = 20
  backup_retention_period = 0
  maintenance_window      = var.maintenance_window
  backup_window           = var.backup_window
  skip_final_snapshot     = true
  apply_immediately       = false
}

output "address" {
  value = aws_db_instance.tf_rds_parameters.address
}
output "port" {
  value = tostring(aws_db_instance.tf_rds_parameters.port)
}
output "instance_class" {
  value = aws_db_instance.tf_rds_parameters.instance_class
}
output "maintenance_window" {
  value = aws_db_instance.tf_rds_parameters.maintenance_window
}
