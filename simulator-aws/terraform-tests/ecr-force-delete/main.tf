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
    ecr = var.endpoint
  }
}

# The provider sends DeleteRepository's force from force_delete.
resource "aws_ecr_repository" "forced" {
  name         = "tf-ecr-forced"
  force_delete = true
}

resource "aws_ecr_repository" "guarded" {
  name = "tf-ecr-guarded"
}
