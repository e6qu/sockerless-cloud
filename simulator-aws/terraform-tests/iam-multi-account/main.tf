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
    iam           = var.endpoint
    organizations = var.endpoint
    sts           = var.endpoint
  }
}

# CreateAccount puts OrganizationAccountAccessRole in the new member account.
resource "aws_organizations_account" "member" {
  name  = "tf-multi-account-member"
  email = "tf-multi-account-member@sim.invalid"
}

# A service control policy that refuses the member account's principals any
# Amazon SQS queue.
resource "aws_organizations_policy" "deny_queues" {
  name        = "tf-deny-queues"
  description = "Refuses Amazon SQS CreateQueue"
  type        = "SERVICE_CONTROL_POLICY"
  content = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Deny", Action = "sqs:CreateQueue", Resource = "*" }]
  })
}

resource "aws_organizations_policy_attachment" "member" {
  policy_id = aws_organizations_policy.deny_queues.id
  target_id = aws_organizations_account.member.id
}

data "aws_organizations_organization" "current" {}

output "organization_root_id" {
  value = data.aws_organizations_organization.current.roots[0].id
}

output "member_account_id" {
  value = aws_organizations_account.member.id
}
