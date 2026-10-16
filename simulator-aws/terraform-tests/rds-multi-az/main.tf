terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.69.0"
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

resource "aws_db_instance" "multi_az" {
  identifier          = "tf-rds-multi-az"
  instance_class      = "db.t3.micro"
  engine              = "postgres"
  username            = "dbadmin"
  password            = "MasterPassword-123!"
  db_name             = "application"
  allocated_storage   = 20
  multi_az            = true
  skip_final_snapshot = true
  apply_immediately   = true
}

output "identifier" {
  value = aws_db_instance.multi_az.identifier
}
output "availability_zone" {
  value = aws_db_instance.multi_az.availability_zone
}
output "multi_az" {
  value = tostring(aws_db_instance.multi_az.multi_az)
}
output "address" {
  value = aws_db_instance.multi_az.address
}
output "port" {
  value = tostring(aws_db_instance.multi_az.port)
}
