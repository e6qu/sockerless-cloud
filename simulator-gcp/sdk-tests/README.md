# simulator-gcp-sdk-tests

Integration tests for the GCP simulator using the official Google Cloud Go SDK. Each test builds the simulator binary, starts it on a free port, runs SDK calls, and verifies responses.

## Services tested

| Test file | Service | Operations |
|-----------|---------|------------|
| `compute_test.go` | Compute Engine | Network, subnetwork create/list |
| `dns_test.go` | Cloud DNS | Managed zone create/get/delete |
| `iam_test.go` | IAM | Service account create/get/list/delete |
| `run_test.go` | Cloud Run | Job create/get/list/delete |
| `storage_test.go` | Cloud Storage | Bucket create, object upload/download/list/delete |

## Running

```sh
cd simulator-gcp/sdk-tests
go test -v ./...
```

The test harness (`helpers_test.go`) handles binary build, port allocation, server startup, and shutdown. No external services required.

## Prerequisites

- Go 1.24+
- The `simulator-gcp/` parent module (built automatically by `TestMain`)

## SDK configuration

Tests configure GCP SDK clients with:

```go
option.WithEndpoint("http://localhost:{port}/...")
option.WithoutAuthentication()
option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials()))
```
