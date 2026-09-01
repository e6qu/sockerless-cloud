terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.62.0"
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
  # Every s3control operation is addressed by account id, which the provider
  # learns from sts:GetCallerIdentity — so unlike the other fixtures this one
  # lets the provider ask.

  endpoints {
    s3        = var.endpoint
    s3control = var.endpoint
    iam       = var.endpoint
    lambda    = var.endpoint
    sts       = var.endpoint
  }
}

resource "aws_s3_bucket" "source" {
  bucket        = "tf-s3control-source"
  force_destroy = true
}

# The standard access point an Object Lambda access point serves reads from.
resource "aws_s3_access_point" "source" {
  bucket = aws_s3_bucket.source.id
  name   = "tf-s3control-ap"
}

resource "aws_iam_role" "transform" {
  name = "tf-s3control-transform"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# The transformation function an Object Lambda access point runs on every read.
resource "aws_lambda_function" "transform" {
  function_name = "tf-s3control-transform"
  role          = aws_iam_role.transform.arn
  package_type  = "Image"
  image_uri     = "123456789012.dkr.ecr.us-east-1.amazonaws.com/sockerless-lambda-runtime-handler:aws-terraform"
}

resource "aws_s3control_object_lambda_access_point" "transform" {
  name = "tf-s3control-olap"

  configuration {
    supporting_access_point = aws_s3_access_point.source.arn

    transformation_configuration {
      actions = ["GetObject"]

      content_transformation {
        aws_lambda {
          function_arn = aws_lambda_function.transform.arn
        }
      }
    }
  }
}

resource "aws_s3control_storage_lens_configuration" "dashboard" {
  config_id = "tf-s3control-lens"

  storage_lens_configuration {
    enabled = true

    account_level {
      bucket_level {}
    }
  }
}

resource "aws_s3control_access_grants_instance" "grants" {}

resource "aws_iam_role" "grants_location" {
  name = "tf-s3control-grants-location"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "access-grants.s3.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_s3control_access_grants_location" "data" {
  depends_on = [aws_s3control_access_grants_instance.grants]

  iam_role_arn   = aws_iam_role.grants_location.arn
  location_scope = "s3://${aws_s3_bucket.source.id}/data/*"
}

resource "aws_s3control_access_grant" "analyst" {
  access_grants_location_id = aws_s3control_access_grants_location.data.access_grants_location_id
  permission                = "READ"

  grantee {
    grantee_type       = "IAM"
    grantee_identifier = aws_iam_role.grants_location.arn
  }
}

output "access_point_arn" {
  value = aws_s3_access_point.source.arn
}

output "object_lambda_access_point_arn" {
  value = aws_s3control_object_lambda_access_point.transform.arn
}

output "storage_lens_arn" {
  value = aws_s3control_storage_lens_configuration.dashboard.arn
}

output "access_grants_instance_arn" {
  value = aws_s3control_access_grants_instance.grants.access_grants_instance_arn
}

output "access_grant_scope" {
  value = aws_s3control_access_grant.analyst.grant_scope
}
