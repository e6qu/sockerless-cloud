# simulator-aws-cli-tests

Integration tests for the AWS simulator using the AWS CLI. Each test builds the simulator binary, starts it on a free port, and runs `aws` CLI commands against it.

## Services tested

| Test file | Service | Operations |
|-----------|---------|------------|
| `s3_test.go` | S3 | Bucket operations, object upload/download |
| `sts_test.go` | STS | GetCallerIdentity |
| `cloudwatch_test.go` | CloudWatch Logs | Log groups, streams, put/get/filter events |
| `lambda_test.go` | Lambda | Create, invoke, update configuration, delete |
| `efs_test.go` | EFS | File systems, mount targets, access points |
| `cloudmap_test.go` | Cloud Map | Namespaces, services, instance register/deregister |

## Running

```sh
cd simulator-aws/cli-tests
go test -v ./...
```

The test harness (`helpers_test.go`) handles binary build, port allocation, server startup, and shutdown. No external services required.

## Prerequisites

- Go 1.23+
- `aws` CLI installed and on `PATH`
- The `simulator-aws/` parent module (built automatically by `TestMain`)

## CLI configuration

Tests set these environment variables before running `aws` commands:

```sh
AWS_ENDPOINT_URL=http://localhost:{port}
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
AWS_DEFAULT_REGION=us-east-1
```
