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
    cognitoidp = var.endpoint
  }
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

resource "aws_cognito_user_pool_client" "alb" {
  name                                 = "alb"
  user_pool_id                         = aws_cognito_user_pool.pool.id
  generate_secret                      = true
  allowed_oauth_flows                  = ["code"]
  allowed_oauth_flows_user_pool_client = true
  allowed_oauth_scopes                 = ["openid", "email"]
  callback_urls                        = ["https://app.example.com/oauth2/idpresponse"]
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
  value = aws_cognito_user_pool.pool.domain
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
