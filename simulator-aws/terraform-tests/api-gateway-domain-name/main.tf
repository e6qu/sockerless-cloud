terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.71.0"
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
    acm        = var.endpoint
    apigateway = var.endpoint
    ec2        = var.endpoint
    elbv2      = var.endpoint
    route53    = var.endpoint
  }
}

resource "aws_route53_zone" "tf_zone" {
  name = "example.test"
}

resource "aws_acm_certificate" "tf_cert" {
  domain_name       = "api.example.test"
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "tf_validation" {
  for_each = {
    for dvo in aws_acm_certificate.tf_cert.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  }

  zone_id = aws_route53_zone.tf_zone.zone_id
  name    = each.value.name
  type    = each.value.type
  ttl     = 60
  records = [each.value.record]
}

resource "aws_acm_certificate_validation" "tf_cert_val" {
  certificate_arn         = aws_acm_certificate.tf_cert.arn
  validation_record_fqdns = [for r in aws_route53_record.tf_validation : r.fqdn]
}

# API Gateway refuses a certificate that is not ISSUED, so the domain name
# takes its ARN from the validation.
resource "aws_api_gateway_domain_name" "tf_domain" {
  domain_name              = "api.example.test"
  regional_certificate_arn = aws_acm_certificate_validation.tf_cert_val.certificate_arn
  security_policy          = "TLS_1_2"

  endpoint_configuration {
    types = ["REGIONAL"]
  }

  tags = {
    team = "api"
  }
}

resource "aws_route53_record" "tf_api_alias" {
  zone_id = aws_route53_zone.tf_zone.zone_id
  name    = aws_api_gateway_domain_name.tf_domain.domain_name
  type    = "A"

  alias {
    name                   = aws_api_gateway_domain_name.tf_domain.regional_domain_name
    zone_id                = aws_api_gateway_domain_name.tf_domain.regional_zone_id
    evaluate_target_health = false
  }
}

resource "aws_api_gateway_rest_api" "tf_api" {
  name = "tf-domain-api"
}

resource "aws_api_gateway_method" "tf_root_get" {
  rest_api_id   = aws_api_gateway_rest_api.tf_api.id
  resource_id   = aws_api_gateway_rest_api.tf_api.root_resource_id
  http_method   = "GET"
  authorization = "NONE"
}

resource "aws_api_gateway_integration" "tf_root_get" {
  rest_api_id = aws_api_gateway_rest_api.tf_api.id
  resource_id = aws_api_gateway_rest_api.tf_api.root_resource_id
  http_method = aws_api_gateway_method.tf_root_get.http_method
  type        = "MOCK"
}

resource "aws_api_gateway_deployment" "tf_deployment" {
  rest_api_id = aws_api_gateway_rest_api.tf_api.id
  depends_on  = [aws_api_gateway_integration.tf_root_get]
}

resource "aws_api_gateway_stage" "tf_stage" {
  rest_api_id   = aws_api_gateway_rest_api.tf_api.id
  deployment_id = aws_api_gateway_deployment.tf_deployment.id
  stage_name    = "v1"
}

resource "aws_api_gateway_base_path_mapping" "tf_mapping" {
  domain_name = aws_api_gateway_domain_name.tf_domain.domain_name
  api_id      = aws_api_gateway_rest_api.tf_api.id
  stage_name  = aws_api_gateway_stage.tf_stage.stage_name
  base_path   = "orders"
}

resource "aws_vpc" "tf_vpc" {
  cidr_block = "10.91.0.0/16"
}

resource "aws_subnet" "tf_subnet" {
  vpc_id            = aws_vpc.tf_vpc.id
  cidr_block        = "10.91.1.0/24"
  availability_zone = "us-east-1a"
}

resource "aws_lb" "tf_nlb" {
  name               = "tf-vpclink-nlb"
  load_balancer_type = "network"
  internal           = true
  subnets            = [aws_subnet.tf_subnet.id]
}

# The provider waits for the link to leave PENDING.
resource "aws_api_gateway_vpc_link" "tf_link" {
  name        = "tf-private-backends"
  description = "Network Load Balancer behind the REST API"
  target_arns = [aws_lb.tf_nlb.arn]

  tags = {
    team = "network"
  }
}

output "regional_domain_name" {
  value = aws_api_gateway_domain_name.tf_domain.regional_domain_name
}

output "regional_zone_id" {
  value = aws_api_gateway_domain_name.tf_domain.regional_zone_id
}

output "domain_arn" {
  value = aws_api_gateway_domain_name.tf_domain.arn
}

output "alias_fqdn" {
  value = aws_route53_record.tf_api_alias.fqdn
}

output "base_path" {
  value = aws_api_gateway_base_path_mapping.tf_mapping.base_path
}

output "vpc_link_id" {
  value = aws_api_gateway_vpc_link.tf_link.id
}
