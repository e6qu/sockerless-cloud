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
  identifier          = "tf-rds-db"
  instance_class      = "db.t3.micro"
  engine              = "postgres"
  engine_version      = "17.5"
  username            = "admin"
  password            = "password123!"
  allocated_storage   = 20
  skip_final_snapshot = true
  apply_immediately   = true

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
