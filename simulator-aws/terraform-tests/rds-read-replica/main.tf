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

resource "aws_db_instance" "source" {
  identifier              = "tf-rds-rr-source"
  instance_class          = "db.t3.micro"
  engine                  = "mysql"
  engine_version          = "8.0"
  username                = "dbadmin"
  password                = "MasterPassword-123!"
  db_name                 = "application"
  allocated_storage       = 20
  backup_retention_period = 1
  skip_final_snapshot     = true
  apply_immediately       = true
}

resource "aws_db_instance" "replica" {
  identifier          = "tf-rds-rr-replica"
  instance_class      = "db.t3.micro"
  replicate_source_db = aws_db_instance.source.identifier
  skip_final_snapshot = true
  apply_immediately   = true
}

output "source_address" {
  value = aws_db_instance.source.address
}
output "source_port" {
  value = tostring(aws_db_instance.source.port)
}
output "source_engine_version" {
  value = aws_db_instance.source.engine_version_actual
}
output "source_parameter_group_name" {
  value = aws_db_instance.source.parameter_group_name
}
output "replica_address" {
  value = aws_db_instance.replica.address
}
output "replica_port" {
  value = tostring(aws_db_instance.replica.port)
}
output "replica_source" {
  value = aws_db_instance.replica.replicate_source_db
}
output "replica_engine" {
  value = aws_db_instance.replica.engine
}
