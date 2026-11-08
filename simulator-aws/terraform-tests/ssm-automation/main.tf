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

variable "parameter_name" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    iam = var.endpoint
    ssm = var.endpoint
    sts = var.endpoint
  }
}

resource "aws_ssm_document" "runbook" {
  name            = "tf-automation-runbook"
  document_type   = "Automation"
  document_format = "YAML"
  content         = <<-DOC
    schemaVersion: '0.3'
    description: Writes a parameter.
    parameters:
      ParamName:
        type: String
    mainSteps:
    - name: put
      action: aws:executeAwsApi
      inputs:
        Service: ssm
        Api: PutParameter
        Name: '{{ ParamName }}'
        Value: from-terraform
        Type: String
        Overwrite: true
  DOC
}

resource "aws_iam_role" "task" {
  name = "tf-ssm-automation-task"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ssm.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "task" {
  name = "automation"
  role = aws_iam_role.task.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["ssm:StartAutomationExecution", "ssm:PutParameter"]
      Resource = "*"
    }]
  })
}

resource "aws_ssm_maintenance_window" "window" {
  name                       = "tf-ssm-automation"
  schedule                   = "cron(0 3 ? * SUN *)"
  duration                   = 2
  cutoff                     = 1
  allow_unassociated_targets = true
}

resource "aws_ssm_maintenance_window_task" "automation" {
  window_id        = aws_ssm_maintenance_window.window.id
  task_type        = "AUTOMATION"
  task_arn         = aws_ssm_document.runbook.name
  service_role_arn = aws_iam_role.task.arn
  priority         = 1

  task_invocation_parameters {
    automation_parameters {
      document_version = "$DEFAULT"
      parameter {
        name   = "ParamName"
        values = [var.parameter_name]
      }
    }
  }

  depends_on = [aws_iam_role_policy.task]
}

output "document_type" {
  value = aws_ssm_document.runbook.document_type
}

output "schema_version" {
  value = aws_ssm_document.runbook.schema_version
}

output "document_version" {
  value = aws_ssm_document.runbook.document_version
}

output "task_type" {
  value = aws_ssm_maintenance_window_task.automation.task_type
}

output "task_arn" {
  value = aws_ssm_maintenance_window_task.automation.task_arn
}
