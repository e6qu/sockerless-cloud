terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
      # aws_ecs_express_gateway_service was added in terraform-provider-aws
      # v6.23.0 (ECS Express Mode, launched 2025-11-21). Pin a version that has
      # the resource.
      version = "6.70.0"
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
    ecs            = var.endpoint
    ec2            = var.endpoint
    elbv2          = var.endpoint
    appautoscaling = var.endpoint
    iam            = var.endpoint
  }
}

resource "aws_ecs_cluster" "express" {
  name = "tf-express-cluster"
}

resource "aws_iam_role" "express_infra" {
  name = "tf-express-infra"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# The infrastructure role provisions the service's load balancer, target
# group, listener rule, certificate, security groups and auto scaling.
resource "aws_iam_role_policy" "express_infra" {
  name = "tf-express-infrastructure"
  role = aws_iam_role.express_infra.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["elasticloadbalancing:*", "ec2:*", "acm:*", "application-autoscaling:*"]
      Resource = "*"
    }]
  })
}

resource "aws_iam_role" "express_exec" {
  name = "tf-express-exec"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# An ECS Express Gateway service: ECS provisions the managed bundle (Fargate
# service, ALB + target group + HTTPS listener + ACM cert, security group, and an
# Application Auto Scaling target/policy) and returns an HTTPS ingress endpoint.
resource "aws_ecs_express_gateway_service" "web" {
  cluster                 = aws_ecs_cluster.express.name
  service_name            = "tf-express-web"
  infrastructure_role_arn = aws_iam_role.express_infra.arn
  execution_role_arn      = aws_iam_role.express_exec.arn

  depends_on = [aws_iam_role_policy.express_infra]

  primary_container {
    image          = "mirror.gcr.io/library/busybox:latest"
    container_port = 8080
    command        = ["sh", "-c", "mkdir -p /www && printf ok >/www/ping && exec httpd -f -p 8080 -h /www"]
  }

  scaling_target = [{
    min_task_count            = 1
    max_task_count            = 4
    auto_scaling_metric       = "AVERAGE_CPU"
    auto_scaling_target_value = 60
  }]

  tags = {
    env = "test"
  }
}

output "express_service_arn" {
  value = aws_ecs_express_gateway_service.web.service_arn
}

output "express_ingress_paths" {
  value = aws_ecs_express_gateway_service.web.ingress_paths
}

output "express_service_name" {
  value = aws_ecs_express_gateway_service.web.service_name
}
