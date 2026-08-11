# simulator-gcp-cli-tests

Integration tests for the GCP simulator using the `gcloud` CLI. Each test builds the simulator binary, starts it on a free port, and runs `gcloud` commands against it.

## Services tested

| Test file | Service | Operations |
|-----------|---------|------------|
| `dns_test.go` | Cloud DNS | Zone and record set create/list/delete |
| `functions_test.go` | Cloud Functions | Function create/list/delete |
| `logging_test.go` | Cloud Logging | Log write and read with filtering |
| `serviceusage_test.go` | Service Usage | Service enable/disable/list |
| `vpcaccess_test.go` | VPC Access | Connector create/describe/list/delete |

## Running

```sh
cd simulator-gcp/cli-tests
go test -v ./...
```

The test harness (`helpers_test.go`) handles binary build, port allocation, server startup, and shutdown. No external services required.

## Prerequisites

- Go 1.23+
- `gcloud` CLI installed and on `PATH`
- The `simulator-gcp/` parent module (built automatically by `TestMain`)

## CLI configuration

Tests configure gcloud with environment variables and flags to point at the local simulator endpoint.
