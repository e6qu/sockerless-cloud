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
  s3_use_path_style           = true

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

The provider signs with `test`/`test`, the administrator every simulator
starts with. The `skip_*` flags keep it from probing the EC2 instance metadata
service and calling AWS STS before each run; leave `skip_requesting_account_id`
unset for resources addressed by account ID, such as the Amazon S3 control
plane's. `s3_use_path_style` keeps bucket names in the URL path, since
`<bucket>.localhost` does not resolve everywhere.

## Optional HTTPS gateway

The AWS provider accepts full endpoint URLs, so the direct HTTP endpoint remains valid. To run through the local HTTPS gateway instead, start the simulator and Caddy, trust Caddy's local CA, and point each provider endpoint at the gateway URL:

```sh
caddy run --config make/https-gateway/Caddyfile --adapter caddyfile &
# Caddy's local root CA; on macOS it lives under ~/Library/Application Support/Caddy/
export SSL_CERT_FILE="$HOME/.local/share/caddy/pki/authorities/local/root.crt"
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

## Coverage

`simulator-aws/terraform-tests/` holds the stacks the provider is tested with
(`main.tf` and one directory per focused stack), and
[`specs/SIM_TEST_COVERAGE_MATRIX.md`](../../specs/SIM_TEST_COVERAGE_MATRIX.md)
names the Terraform test of each surface.

## Notes

- State lives in memory unless the simulator runs with `SIM_PERSIST=true`, which
  keeps it under `SIM_DATA_DIR`; restarting a non-persistent simulator leaves
  Terraform state describing resources that no longer exist.
