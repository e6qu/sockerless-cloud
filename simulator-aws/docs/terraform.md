# Using the AWS simulator with Terraform

## Prerequisites

- Terraform installed (`terraform version`)
- Simulator running on `http://localhost:4566`, or the optional Caddy HTTPS gateway running at `https://aws.sockerless.localhost:8443`

## Provider configuration

Use the official `hashicorp/aws` provider with endpoint overrides pointing at the simulator:

```hcl
terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    ecs            = "http://localhost:4566"
    ecr            = "http://localhost:4566"
    lambda         = "http://localhost:4566"
    cloudwatchlogs = "http://localhost:4566"
    s3             = "http://localhost:4566"
    iam            = "http://localhost:4566"
    sts            = "http://localhost:4566"
    ec2            = "http://localhost:4566"
    efs            = "http://localhost:4566"
    servicediscovery = "http://localhost:4566"
  }
}
```

The `skip_*` flags prevent the provider from making calls that the simulator doesn't need to handle (metadata API, credential validation, account ID lookup).

## Optional HTTPS gateway

The AWS provider accepts full endpoint URLs, so the direct HTTP endpoint remains valid. To run through the local HTTPS gateway instead, start the simulator and Caddy, trust Caddy's local CA, and point each provider endpoint at the gateway URL:

```sh
make stack-https-up
export SSL_CERT_FILE="$(make -s stack-https-ca)"
terraform apply -auto-approve -var="endpoint=https://aws.sockerless.localhost:8443"
```

The provider block is otherwise the same as the direct HTTP example:

```hcl
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    ec2              = var.endpoint
    ecs              = var.endpoint
    ecr              = var.endpoint
    lambda           = var.endpoint
    cloudwatchlogs   = var.endpoint
    s3               = var.endpoint
    iam              = var.endpoint
    sts              = var.endpoint
    servicediscovery = var.endpoint
  }
}
```

The test harness exposes this path with:

```sh
cd simulator-aws
make terraform-https-test
```

The harness uses the same Caddyfile and CA flow, but points Terraform at Caddy's `https://localhost:<ephemeral-port>` single-simulator route. On macOS the target runs inside the shared Linux simulator test image so the real provider honors `SSL_CERT_FILE`. The route is explicit test transport and avoids relying on wildcard `.localhost` DNS on developer machines or CI runners; the simulator API paths and AWS provider endpoint settings are otherwise identical.

## Example resources

```hcl
data "aws_caller_identity" "current" {}

resource "aws_ecs_cluster" "main" {
  name = "my-cluster"
}

resource "aws_ecs_task_definition" "main" {
  family                = "my-task"
  container_definitions = jsonencode([{
    name      = "main"
    image     = "nginx:latest"
    essential = true
  }])
}

resource "aws_iam_role" "execution" {
  name               = "execution-role"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
    }]
  })
}
```

## Running

Pass the simulator endpoint via a variable or hardcode it:

```sh
# Using a variable
terraform init
terraform apply -auto-approve -var="endpoint=http://localhost:4566"
terraform destroy -auto-approve -var="endpoint=http://localhost:4566"
```

Or define the variable in a `variables.tf`:

```hcl
variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
  default     = "http://localhost:4566"
}
```

Then reference `var.endpoint` in the provider endpoints block.

## Supported resources

The simulator supports the AWS API operations that these Terraform resources use:

| Category | Resources |
|----------|-----------|
| ECS | `aws_ecs_cluster`, `aws_ecs_task_definition`, `aws_ecs_service` |
| ECR | `aws_ecr_repository`, `aws_ecr_lifecycle_policy` |
| Lambda | `aws_lambda_function`, `aws_lambda_alias`, `aws_lambda_permission`, `aws_lambda_function_url`, `aws_lambda_invocation` |
| IAM | `aws_iam_role`, `aws_iam_role_policy`, `aws_iam_role_policy_attachment` |
| EC2 | `aws_vpc`, `aws_subnet`, `aws_internet_gateway`, `aws_nat_gateway`, `aws_route_table`, `aws_security_group` |
| S3 | `aws_s3_bucket`, `aws_s3_object` |
| EFS | `aws_efs_file_system`, `aws_efs_mount_target`, `aws_efs_access_point` |
| CloudWatch | `aws_cloudwatch_log_group` |
| Cloud Map | `aws_service_discovery_private_dns_namespace`, `aws_service_discovery_service` |

## Notes

- All state is in-memory and resets when the simulator restarts. Terraform state files will become stale after a restart.
- S3 uses path-style addressing via `s3_use_path_style = true`, so bucket names stay in the URL path under the configured endpoint.
- Authentication is not validated — any access key and secret will work.
