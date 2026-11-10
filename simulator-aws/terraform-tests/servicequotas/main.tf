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

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    servicequotas = var.endpoint
    sts           = var.endpoint
  }
}

data "aws_servicequotas_service" "registry" {
  service_name = "Amazon ECR"
}

data "aws_servicequotas_service_quota" "images" {
  service_code = data.aws_servicequotas_service.registry.service_code
  quota_name   = "Images per repository"
}

resource "aws_servicequotas_service_quota" "images" {
  service_code = data.aws_servicequotas_service_quota.images.service_code
  quota_code   = data.aws_servicequotas_service_quota.images.quota_code
  value        = 150000
}

resource "aws_servicequotas_template" "roles" {
  aws_region   = "us-east-1"
  service_code = "iam"
  quota_code   = "L-FE177D64"
  value        = 1500
}

resource "aws_servicequotas_template_association" "this" {
  depends_on = [aws_servicequotas_template.roles]
}

output "service_code" {
  value = data.aws_servicequotas_service.registry.service_code
}

output "images_default" {
  value = tostring(data.aws_servicequotas_service_quota.images.default_value)
}

output "images_value" {
  value = tostring(aws_servicequotas_service_quota.images.value)
}

output "images_usage_metric" {
  value = aws_servicequotas_service_quota.images.usage_metric[0].metric_name
}

output "template_quota_name" {
  value = aws_servicequotas_template.roles.quota_name
}

output "association_status" {
  value = aws_servicequotas_template_association.this.status
}
