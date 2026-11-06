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

variable "enabled_features" {
  type = list(string)
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    iam           = var.endpoint
    organizations = var.endpoint
    sts           = var.endpoint
  }
}

data "aws_organizations_organization" "current" {}

# The account already runs its organization: adopt it and turn on trusted
# access for IAM, which centralized root access needs.
import {
  to = aws_organizations_organization.this
  id = data.aws_organizations_organization.current.id
}

resource "aws_organizations_organization" "this" {
  feature_set                   = "ALL"
  aws_service_access_principals = ["iam.amazonaws.com"]
  # The adopted organization's root enables service control policies.
  enabled_policy_types = ["SERVICE_CONTROL_POLICY"]
}

resource "aws_iam_organizations_features" "this" {
  enabled_features = var.enabled_features

  depends_on = [aws_organizations_organization.this]
}

resource "aws_organizations_account" "member" {
  name  = "tf-root-access-member"
  email = "tf-root-access-member@sim.invalid"

  depends_on = [aws_organizations_organization.this]
}

# An IAM user of the management account that may open root sessions.
resource "aws_iam_user" "root_access" {
  name = "tf-root-access"
}

resource "aws_iam_user_policy" "assume_root" {
  name = "assume-root"
  user = aws_iam_user.root_access.name
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "sts:AssumeRoot", Resource = "*" }]
  })
}

resource "aws_iam_access_key" "root_access" {
  user = aws_iam_user.root_access.name
}

output "organization_id" {
  value = aws_iam_organizations_features.this.id
}

output "enabled_features" {
  value = aws_iam_organizations_features.this.enabled_features
}

output "member_account_id" {
  value = aws_organizations_account.member.id
}

output "access_key_id" {
  value = aws_iam_access_key.root_access.id
}

output "secret_access_key" {
  value     = aws_iam_access_key.root_access.secret
  sensitive = true
}
