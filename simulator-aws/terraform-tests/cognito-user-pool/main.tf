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
    acm        = var.endpoint
    cognitoidp = var.endpoint
    ec2        = var.endpoint
    elbv2      = var.endpoint
  }
}

variable "certificate_pem" {
  type = string
}

variable "private_key_pem" {
  type      = string
  sensitive = true
}

resource "aws_cognito_user_pool" "pool" {
  name = "tf-users"

  password_policy {
    minimum_length                   = 10
    require_numbers                  = true
    require_uppercase                = true
    require_lowercase                = true
    require_symbols                  = false
    temporary_password_validity_days = 3
  }

  schema {
    name                = "team"
    attribute_data_type = "String"
    mutable             = true

    string_attribute_constraints {
      min_length = 1
      max_length = 32
    }
  }

  tags = {
    env = "test"
  }
}

resource "aws_vpc" "alb" {
  cidr_block = "10.41.0.0/16"
}

resource "aws_subnet" "alb" {
  count             = 2
  vpc_id            = aws_vpc.alb.id
  cidr_block        = "10.41.${count.index + 1}.0/24"
  availability_zone = ["us-east-1a", "us-east-1b"][count.index]
}

resource "aws_lb" "app" {
  name               = "tf-cognito-alb"
  load_balancer_type = "application"
  subnets            = aws_subnet.alb[*].id
}

resource "aws_lb_target_group" "app" {
  name        = "tf-cognito-tg"
  port        = 8080
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = aws_vpc.alb.id
}

resource "aws_acm_certificate" "alb" {
  certificate_body = var.certificate_pem
  private_key      = var.private_key_pem
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.app.arn
  port              = 9443
  protocol          = "HTTPS"
  certificate_arn   = aws_acm_certificate.alb.arn

  default_action {
    type  = "authenticate-cognito"
    order = 1

    authenticate_cognito {
      user_pool_arn       = aws_cognito_user_pool.pool.arn
      user_pool_client_id = aws_cognito_user_pool_client.alb.id
      user_pool_domain    = aws_cognito_user_pool_domain.login.domain
      session_cookie_name = "TfSession"
      session_timeout     = 3600
    }
  }

  default_action {
    type             = "forward"
    order            = 2
    target_group_arn = aws_lb_target_group.app.arn
  }
}

resource "aws_cognito_user_pool_client" "alb" {
  name                                 = "alb"
  user_pool_id                         = aws_cognito_user_pool.pool.id
  generate_secret                      = true
  allowed_oauth_flows                  = ["code"]
  allowed_oauth_flows_user_pool_client = true
  allowed_oauth_scopes                 = ["openid", "email"]
  callback_urls                        = ["https://${aws_lb.app.dns_name}:9443/oauth2/idpresponse"]
  supported_identity_providers         = ["COGNITO"]
}

resource "aws_cognito_user_pool_domain" "login" {
  domain       = "tf-login"
  user_pool_id = aws_cognito_user_pool.pool.id
}

resource "aws_cognito_user" "erin" {
  user_pool_id = aws_cognito_user_pool.pool.id
  username     = "erin"
  password     = "Permanent-Pass-11"

  attributes = {
    email  = "erin@example.com"
    "team" = "red"
  }
}

output "pool_id" {
  value = aws_cognito_user_pool.pool.id
}

output "pool_arn" {
  value = aws_cognito_user_pool.pool.arn
}

output "pool_domain" {
  value = aws_cognito_user_pool_domain.login.domain
}

output "client_id" {
  value = aws_cognito_user_pool_client.alb.id
}

output "client_secret" {
  value     = aws_cognito_user_pool_client.alb.client_secret
  sensitive = true
}

output "user_status" {
  value = aws_cognito_user.erin.status
}

output "user_sub" {
  value = aws_cognito_user.erin.sub
}

output "listener_cookie" {
  value = aws_lb_listener.https.default_action[0].authenticate_cognito[0].session_cookie_name
}

output "listener_domain" {
  value = aws_lb_listener.https.default_action[0].authenticate_cognito[0].user_pool_domain
}
