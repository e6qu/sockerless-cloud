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
    apigateway = var.endpoint
    iam        = var.endpoint
    logs       = var.endpoint
  }
}

resource "aws_iam_role" "tf_cloudwatch" {
  name = "api-gateway-cloudwatch"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "apigateway.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_api_gateway_account" "tf_account" {
  cloudwatch_role_arn = aws_iam_role.tf_cloudwatch.arn
}

resource "aws_cloudwatch_log_group" "tf_access" {
  name = "/aws/apigateway/orders-access"
}

resource "aws_api_gateway_rest_api" "tf_api" {
  name = "orders"
}

resource "aws_api_gateway_method" "tf_get" {
  rest_api_id   = aws_api_gateway_rest_api.tf_api.id
  resource_id   = aws_api_gateway_rest_api.tf_api.root_resource_id
  http_method   = "GET"
  authorization = "NONE"
}

resource "aws_api_gateway_integration" "tf_get" {
  rest_api_id = aws_api_gateway_rest_api.tf_api.id
  resource_id = aws_api_gateway_rest_api.tf_api.root_resource_id
  http_method = aws_api_gateway_method.tf_get.http_method
  type        = "MOCK"
}

resource "aws_api_gateway_deployment" "tf_deployment" {
  rest_api_id = aws_api_gateway_rest_api.tf_api.id
  depends_on  = [aws_api_gateway_integration.tf_get]
}

resource "aws_api_gateway_client_certificate" "tf_backend" {
  description = "orders backend"
}

resource "aws_api_gateway_stage" "tf_prod" {
  rest_api_id           = aws_api_gateway_rest_api.tf_api.id
  deployment_id         = aws_api_gateway_deployment.tf_deployment.id
  stage_name            = "prod"
  client_certificate_id = aws_api_gateway_client_certificate.tf_backend.id
  description           = "production"
  xray_tracing_enabled  = true
  cache_cluster_enabled = true
  cache_cluster_size    = "0.5"
  variables = {
    backend = "blue"
  }

  access_log_settings {
    destination_arn = aws_cloudwatch_log_group.tf_access.arn
    format          = jsonencode({ requestId = "$context.requestId" })
  }

  tags = {
    env = "prod"
  }

  depends_on = [aws_api_gateway_account.tf_account]
}

resource "aws_api_gateway_method_settings" "tf_all" {
  rest_api_id = aws_api_gateway_rest_api.tf_api.id
  stage_name  = aws_api_gateway_stage.tf_prod.stage_name
  method_path = "*/*"

  settings {
    metrics_enabled        = true
    logging_level          = "INFO"
    throttling_burst_limit = 100
    throttling_rate_limit  = 50
  }
}

output "stage_arn" {
  value = aws_api_gateway_stage.tf_prod.arn
}

output "invoke_url" {
  value = aws_api_gateway_stage.tf_prod.invoke_url
}

output "api_id" {
  value = aws_api_gateway_rest_api.tf_api.id
}

output "client_certificate_id" {
  value = aws_api_gateway_client_certificate.tf_backend.id
}

output "client_certificate_pem" {
  value = aws_api_gateway_client_certificate.tf_backend.pem_encoded_certificate
}
