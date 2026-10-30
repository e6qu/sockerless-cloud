# The tutorial's AWS stack (docs/tutorial.md): a VPC with a NAT gateway, an
# EC2 instance, an Amazon ECS service on AWS Fargate, an S3 bucket, a DynamoDB
# table, an SQS queue that triggers a Lambda function, and CloudWatch logs and
# an alarm. The same file applies to AWS itself once the endpoints go.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.71.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "2.8.1"
    }
  }
}

variable "endpoint" {
  description = "The AWS simulator's address."
  type        = string
  default     = "http://localhost:4566"
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    cloudwatch     = var.endpoint
    cloudwatchlogs = var.endpoint
    dynamodb       = var.endpoint
    ec2            = var.endpoint
    ecs            = var.endpoint
    iam            = var.endpoint
    lambda         = var.endpoint
    s3             = var.endpoint
    sqs            = var.endpoint
    sts            = var.endpoint
  }
}

# Network: a VPC with a public subnet that reaches the internet through an
# internet gateway, and a private subnet that reaches it through a NAT gateway.

resource "aws_vpc" "main" {
  cidr_block           = "10.240.0.0/16"
  enable_dns_hostnames = true
  tags                 = { Name = "tutorial" }
}

resource "aws_subnet" "public" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.240.1.0/24"
  availability_zone = "us-east-1a"
}

resource "aws_subnet" "private" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.240.2.0/24"
  availability_zone = "us-east-1a"
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

resource "aws_nat_gateway" "main" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public.id
  depends_on    = [aws_internet_gateway.main]
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.main.id
  }
}

resource "aws_route_table_association" "private" {
  subnet_id      = aws_subnet.private.id
  route_table_id = aws_route_table.private.id
}

resource "aws_security_group" "app" {
  name        = "tutorial-app"
  description = "The tutorial's workloads"
  vpc_id      = aws_vpc.main.id

  ingress {
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = [aws_vpc.main.cidr_block]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# Compute: an EC2 instance from the newest Amazon Linux 2023 AMI.

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-kernel-6.1-x86_64"]
  }
}

resource "aws_instance" "app" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = "t3.micro"
  subnet_id              = aws_subnet.private.id
  vpc_security_group_ids = [aws_security_group.app.id]
  tags                   = { Name = "tutorial" }
}

# Containers: an Amazon ECS service on AWS Fargate in the private subnet.
# The provider waits until its task runs.

resource "aws_ecs_cluster" "main" {
  name = "tutorial"
}

resource "aws_ecs_task_definition" "web" {
  family                   = "tutorial-web"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([{
    name         = "web"
    image        = "mirror.gcr.io/library/busybox:latest"
    command      = ["httpd", "-f", "-p", "8080"]
    essential    = true
    portMappings = [{ containerPort = 8080 }]
  }])
}

resource "aws_ecs_service" "web" {
  name                  = "tutorial-web"
  cluster               = aws_ecs_cluster.main.id
  task_definition       = aws_ecs_task_definition.web.arn
  desired_count         = 1
  launch_type           = "FARGATE"
  wait_for_steady_state = true

  network_configuration {
    subnets         = [aws_subnet.private.id]
    security_groups = [aws_security_group.app.id]
  }
}

# Storage: an S3 bucket holding one object, and a DynamoDB table holding one
# item.

resource "aws_s3_bucket" "assets" {
  bucket        = "tutorial-assets"
  force_destroy = true
}

resource "aws_s3_object" "hello" {
  bucket       = aws_s3_bucket.assets.id
  key          = "hello.txt"
  content      = "Hello from S3"
  content_type = "text/plain"
}

data "aws_s3_object" "hello" {
  bucket = aws_s3_object.hello.bucket
  key    = aws_s3_object.hello.key
}

resource "aws_dynamodb_table" "orders" {
  name         = "tutorial-orders"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }
}

resource "aws_dynamodb_table_item" "first" {
  table_name = aws_dynamodb_table.orders.name
  hash_key   = aws_dynamodb_table.orders.hash_key
  item = jsonencode({
    id     = { S = "order-1" }
    status = { S = "placed" }
  })
}

# Messaging and functions: an SQS queue with a dead-letter queue, consumed by
# a Python Lambda function.

resource "aws_sqs_queue" "orders_dlq" {
  name = "tutorial-orders-dlq"
}

resource "aws_sqs_queue" "orders" {
  name = "tutorial-orders"
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.orders_dlq.arn
    maxReceiveCount     = 3
  })
}

resource "aws_iam_role" "worker" {
  name = "tutorial-worker"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "worker" {
  name = "tutorial-worker"
  role = aws_iam_role.worker.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"]
        Resource = aws_sqs_queue.orders.arn
      },
      {
        Effect   = "Allow"
        Action   = ["logs:CreateLogStream", "logs:PutLogEvents"]
        Resource = "${aws_cloudwatch_log_group.worker.arn}:*"
      },
    ]
  })
}

data "archive_file" "worker" {
  type        = "zip"
  output_path = "${path.module}/.terraform/worker.zip"

  source {
    filename = "handler.py"
    content  = <<-PY
      import json

      def handle(event, context):
          records = event.get("Records", [])
          print(json.dumps({"received": len(records)}))
          return {"received": len(records), "greeting": event.get("greeting")}
    PY
  }
}

resource "aws_cloudwatch_log_group" "worker" {
  name              = "/aws/lambda/tutorial-worker"
  retention_in_days = 7
}

resource "aws_lambda_function" "worker" {
  function_name    = "tutorial-worker"
  role             = aws_iam_role.worker.arn
  runtime          = "python3.12"
  handler          = "handler.handle"
  filename         = data.archive_file.worker.output_path
  source_code_hash = data.archive_file.worker.output_base64sha256
  timeout          = 30
  depends_on       = [aws_cloudwatch_log_group.worker]
}

resource "aws_lambda_event_source_mapping" "orders" {
  function_name    = aws_lambda_function.worker.arn
  event_source_arn = aws_sqs_queue.orders.arn
  batch_size       = 10
  depends_on       = [aws_iam_role_policy.worker]
}

data "aws_lambda_invocation" "hello" {
  function_name = aws_lambda_function.worker.function_name
  input         = jsonencode({ greeting = "hello" })
}

# Monitoring: an alarm on the queue's backlog.

resource "aws_cloudwatch_metric_alarm" "backlog" {
  alarm_name          = "tutorial-orders-backlog"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = aws_sqs_queue.orders.name }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 1
  threshold           = 100
  comparison_operator = "GreaterThanThreshold"
}

output "nat_gateway_public_ip" {
  value = aws_nat_gateway.main.public_ip
}

output "instance_state" {
  value = aws_instance.app.instance_state
}

output "instance_private_ip" {
  value = aws_instance.app.private_ip
}

output "ecs_service" {
  value = aws_ecs_service.web.name
}

output "s3_object_body" {
  value = data.aws_s3_object.hello.body
}

output "dynamodb_table_arn" {
  value = aws_dynamodb_table.orders.arn
}

output "queue_url" {
  value = aws_sqs_queue.orders.url
}

output "lambda_result" {
  value = data.aws_lambda_invocation.hello.result
}

output "event_source_mapping_state" {
  value = aws_lambda_event_source_mapping.orders.state
}

output "alarm_arn" {
  value = aws_cloudwatch_metric_alarm.backlog.arn
}
