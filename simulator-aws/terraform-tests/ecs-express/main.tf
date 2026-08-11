terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
      # aws_ecs_express_gateway_service was added in terraform-provider-aws
      # v6.23.0 (ECS Express Mode, launched 2025-11-21). Pin a version that has
      # the resource.
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
    ecs            = var.endpoint
    ec2            = var.endpoint
    elbv2          = var.endpoint
    appautoscaling = var.endpoint
  }
}

resource "aws_ecs_cluster" "express" {
  name = "tf-express-cluster"
}

# An ECS Express Gateway service: ECS provisions the managed bundle (Fargate
# service, ALB + target group + HTTPS listener + ACM cert, security group, and an
# Application Auto Scaling target/policy) and returns an HTTPS ingress endpoint.
resource "aws_ecs_express_gateway_service" "web" {
  cluster                 = aws_ecs_cluster.express.name
  service_name            = "tf-express-web"
  infrastructure_role_arn = "arn:aws:iam::000000000000:role/express-infra"
  execution_role_arn      = "arn:aws:iam::000000000000:role/express-exec"

  primary_container {
    image          = "public.ecr.aws/docker/library/busybox:latest"
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
