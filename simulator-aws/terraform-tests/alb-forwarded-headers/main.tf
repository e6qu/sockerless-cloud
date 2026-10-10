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

# The address and port of the header-echoing target the test serves.
variable "target_ip" {
  type = string
}

variable "target_port" {
  type = number
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    ec2   = var.endpoint
    elbv2 = var.endpoint
  }
}

resource "aws_vpc" "xff" {
  cidr_block = "10.84.0.0/16"
}

resource "aws_subnet" "a" {
  vpc_id            = aws_vpc.xff.id
  cidr_block        = "10.84.1.0/24"
  availability_zone = "us-east-1a"
}

resource "aws_subnet" "b" {
  vpc_id            = aws_vpc.xff.id
  cidr_block        = "10.84.2.0/24"
  availability_zone = "us-east-1b"
}

resource "aws_lb" "xff" {
  name                       = "tf-xff-alb"
  load_balancer_type         = "application"
  subnets                    = [aws_subnet.a.id, aws_subnet.b.id]
  xff_header_processing_mode = "append"
  enable_xff_client_port     = true
}

resource "aws_lb_target_group" "xff" {
  name        = "tf-xff-tg"
  port        = 80
  protocol    = "HTTP"
  vpc_id      = aws_vpc.xff.id
  target_type = "ip"

  health_check {
    path     = "/healthz"
    protocol = "HTTP"
    interval = 5
    timeout  = 2
  }
}

resource "aws_lb_target_group_attachment" "echo" {
  target_group_arn = aws_lb_target_group.xff.arn
  target_id        = var.target_ip
  port             = var.target_port
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.xff.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.xff.arn
  }
}

output "dns_name" {
  value = aws_lb.xff.dns_name
}

output "target_group_arn" {
  value = aws_lb_target_group.xff.arn
}
