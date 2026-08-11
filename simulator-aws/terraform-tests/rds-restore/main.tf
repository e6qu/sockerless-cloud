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

output "rds_restored_instance_arn" {
  value = aws_db_instance.tf_rds_restored.arn
}
output "rds_restored_instance_engine" {
  value = aws_db_instance.tf_rds_restored.engine
}
output "rds_restored_instance_tags_env" {
  value = aws_db_instance.tf_rds_restored.tags["env"]
}
