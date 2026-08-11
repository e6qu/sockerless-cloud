# simulator-aws-sdk-tests

Integration tests for the AWS simulator using the official AWS SDK for Go v2. Each test builds the simulator binary, starts it on a free port, runs SDK calls, and verifies responses.

## Services tested

| Test file | Service | Operations |
|-----------|---------|------------|
| `ec2_test.go` | EC2 | VPC, Subnet, Security Group, Internet Gateway |
| `ecr_test.go` | ECR | Repository CRUD, lifecycle policies, auth tokens |
| `ecs_test.go` | ECS | Cluster, task definitions, describe |
| `iam_test.go` | IAM | Role CRUD, inline policies |
| `s3_test.go` | S3 | Bucket CRUD, object put/get/list/delete |
| `sts_test.go` | STS | GetCallerIdentity |

## Running

```sh
cd simulator-aws/sdk-tests
go test -v ./...
```

The test harness (`helpers_test.go`) handles binary build, port allocation, server startup, and shutdown. No external services required.

## Prerequisites

- Go 1.23+
- The `simulator-aws/` parent module (built automatically by `TestMain`)

## SDK configuration

Tests configure the AWS SDK client with:

```go
cfg.EndpointResolverWithOptions = /* points to local simulator */
cfg.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
cfg.Region = "us-east-1"
```
