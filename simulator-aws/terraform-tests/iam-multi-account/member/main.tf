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

variable "member_account_id" {
  type = string
}

variable "create_queue" {
  type    = bool
  default = false
}

variable "in_organization" {
  type    = bool
  default = true
}

variable "organization_root_id" {
  type    = string
  default = ""
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
    sqs           = var.endpoint
    sts           = var.endpoint
  }

  # The provider works in the member account as the management account's
  # administrator.
  assume_role {
    role_arn     = "arn:aws:iam::${var.member_account_id}:role/OrganizationAccountAccessRole"
    session_name = "tf-multi-account"
  }
}

resource "aws_iam_role" "member" {
  name = "tf-member-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_sqs_queue" "denied" {
  count = var.create_queue ? 1 : 0
  name  = "tf-multi-account-denied"
}

# Only the management account creates organizational units.
resource "aws_organizations_organizational_unit" "denied" {
  count     = var.organization_root_id != "" ? 1 : 0
  name      = "tf-member-ou"
  parent_id = var.organization_root_id
}

# Any account in the organization describes it; ListAccounts answers a member
# account AccessDeniedException, which the data source reads as "not a
# delegated administrator".
data "aws_organizations_organization" "member_view" {
  count = var.in_organization ? 1 : 0
}

output "management_account_id" {
  value = one(data.aws_organizations_organization.member_view[*].master_account_id)
}

output "listed_accounts" {
  value = try(length(one(data.aws_organizations_organization.member_view[*].accounts)), 0)
}

output "role_arn" {
  value = aws_iam_role.member.arn
}
