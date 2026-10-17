terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.69.1"
    }
  }
}

variable "endpoint" {
  type = string
}

variable "identifier" {
  type = string
}

variable "port" {
  type = number
}

variable "storage_type" {
  type = string
}

variable "iops" {
  type = number
}

variable "storage_throughput" {
  type    = number
  default = null
}

variable "multi_az" {
  type = bool
}

variable "ca_cert_identifier" {
  type = string
}

variable "option_group" {
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

resource "aws_db_subnet_group" "settings" {
  name       = "tf-rds-settings"
  subnet_ids = ["subnet-aaaa1111", "subnet-bbbb2222"]
}

resource "aws_db_option_group" "first" {
  name                     = "tf-rds-settings-first"
  option_group_description = "first option group"
  engine_name              = "postgres"
  major_engine_version     = "16"
}

resource "aws_db_option_group" "second" {
  name                     = "tf-rds-settings-second"
  option_group_description = "second option group"
  engine_name              = "postgres"
  major_engine_version     = "16"
}

locals {
  option_groups = {
    first  = aws_db_option_group.first.name
    second = aws_db_option_group.second.name
  }
}

resource "aws_db_instance" "settings" {
  identifier              = var.identifier
  instance_class          = "db.t3.micro"
  engine                  = "postgres"
  engine_version          = "16.15"
  username                = "dbadmin"
  password                = "MasterPassword-123!"
  db_name                 = "application"
  allocated_storage       = 100
  storage_type            = var.storage_type
  iops                    = var.iops
  storage_throughput      = var.storage_throughput
  multi_az                = var.multi_az
  port                    = var.port
  ca_cert_identifier      = var.ca_cert_identifier
  db_subnet_group_name    = aws_db_subnet_group.settings.name
  option_group_name       = local.option_groups[var.option_group]
  backup_retention_period = 0
  skip_final_snapshot     = true
  apply_immediately       = true
}

output "address" {
  value = aws_db_instance.settings.address
}
output "port" {
  value = tostring(aws_db_instance.settings.port)
}
output "resource_id" {
  value = aws_db_instance.settings.resource_id
}
output "arn" {
  value = aws_db_instance.settings.arn
}
