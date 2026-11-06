terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.72.0"
    }
  }
}

variable "endpoint" {
  type = string
}

variable "feature_set" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    organizations = var.endpoint
    sts           = var.endpoint
  }
}

resource "aws_organizations_organization" "this" {
  feature_set = var.feature_set
}

output "feature_set" {
  value = aws_organizations_organization.this.feature_set
}
