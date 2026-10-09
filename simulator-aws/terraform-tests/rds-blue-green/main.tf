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

variable "engine_version" {
  type = string
}

variable "parameter_group_name" {
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

resource "aws_db_parameter_group" "blue" {
  name   = "tf-rds-bg-blue"
  family = "mysql8.0"
}

resource "aws_db_parameter_group" "green" {
  name   = "tf-rds-bg-green"
  family = "mysql8.0"
}

resource "aws_db_instance" "tf_rds_bg" {
  identifier              = "tf-rds-bg"
  instance_class          = "db.t3.micro"
  engine                  = "mysql"
  engine_version          = var.engine_version
  parameter_group_name    = var.parameter_group_name == "green" ? aws_db_parameter_group.green.name : aws_db_parameter_group.blue.name
  username                = "dbadmin"
  password                = "MasterPassword-123!"
  db_name                 = "application"
  allocated_storage       = 20
  backup_retention_period = 1
  skip_final_snapshot     = true
  apply_immediately       = true

  blue_green_update {
    enabled = true
  }
}

output "resource_id" {
  value = aws_db_instance.tf_rds_bg.resource_id
}
output "engine_version" {
  value = aws_db_instance.tf_rds_bg.engine_version_actual
}
output "parameter_group_name" {
  value = aws_db_instance.tf_rds_bg.parameter_group_name
}
output "address" {
  value = aws_db_instance.tf_rds_bg.address
}
output "port" {
  value = tostring(aws_db_instance.tf_rds_bg.port)
}
