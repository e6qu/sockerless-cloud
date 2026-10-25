terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.70.0"
    }
  }
}

variable "endpoint" {
  type = string
}

variable "lambda_architecture" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    iam     = var.endpoint
    kinesis = var.endpoint
    lambda  = var.endpoint
    sts     = var.endpoint
  }
}

resource "aws_iam_role" "function" {
  name = "tf-lambda-concurrency"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# The event source mapping reads the stream as the function's execution role.
resource "aws_iam_role_policy" "stream_reader" {
  name = "tf-lambda-concurrency-stream"
  role = aws_iam_role.function.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["kinesis:GetShardIterator", "kinesis:GetRecords", "kinesis:DescribeStream", "kinesis:ListShards"]
      Resource = aws_kinesis_stream.records.arn
    }]
  })
}

resource "aws_lambda_function" "counter" {
  function_name                  = "tf-lambda-concurrency"
  role                           = aws_iam_role.function.arn
  package_type                   = "Image"
  image_uri                      = "123456789012.dkr.ecr.us-east-1.amazonaws.com/sockerless-lambda-runtime-handler:aws-terraform"
  architectures                  = [var.lambda_architecture]
  timeout                        = 30
  publish                        = true
  reserved_concurrent_executions = 2
}

resource "aws_lambda_alias" "live" {
  name             = "live"
  function_name    = aws_lambda_function.counter.function_name
  function_version = aws_lambda_function.counter.version
}

# The provider waits for the configuration to read READY: every provisioned
# execution environment initialised.
resource "aws_lambda_provisioned_concurrency_config" "live" {
  function_name                     = aws_lambda_function.counter.function_name
  qualifier                         = aws_lambda_alias.live.name
  provisioned_concurrent_executions = 1
}

data "aws_lambda_invocation" "warm" {
  function_name = aws_lambda_function.counter.function_name
  qualifier     = aws_lambda_alias.live.name

  input = jsonencode({
    action = "count"
  })

  depends_on = [aws_lambda_provisioned_concurrency_config.live]
}

resource "aws_kinesis_stream" "records" {
  name        = "tf-lambda-concurrency"
  shard_count = 1
}

resource "aws_lambda_event_source_mapping" "records" {
  function_name     = aws_lambda_function.counter.arn
  event_source_arn  = aws_kinesis_stream.records.arn
  starting_position = "TRIM_HORIZON"
  batch_size        = 50

  tags = {
    stream = "records"
  }

  depends_on = [aws_iam_role_policy.stream_reader]
}

output "warm_invocation" {
  value = data.aws_lambda_invocation.warm.result
}

output "reserved_concurrent_executions" {
  value = tostring(aws_lambda_function.counter.reserved_concurrent_executions)
}

output "mapping_state" {
  value = aws_lambda_event_source_mapping.records.state
}

output "mapping_maximum_retry_attempts" {
  value = tostring(aws_lambda_event_source_mapping.records.maximum_retry_attempts)
}
