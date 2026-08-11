# simulator-azure-sdk-tests

Integration tests for the Azure simulator using the official Azure SDK for Go. Each test builds the simulator binary, starts it on a free port, runs SDK calls, and verifies responses.

## Services tested

| Test file | Service | Operations |
|-----------|---------|------------|
| `resourcegroup_test.go` | Resource Manager | Resource group create/delete/exists |
| `storage_test.go` | Storage | Storage account create/get |
| `containerapps_test.go` | Container Apps | Container Apps job create/get |
| `identity_test.go` | Managed Identity | User-assigned identity create/get/delete |
| `network_test.go` | Virtual Network | VNet, subnet, NSG create |

## Running

```sh
cd simulator-azure/sdk-tests
go test -v ./...
```

The test harness (`helpers_test.go`) handles binary build, port allocation, server startup, and shutdown. No external services required.

## Prerequisites

- Go 1.24+
- The `simulator-azure/` parent module (built automatically by `TestMain`)

## SDK configuration

Tests configure Azure SDK clients to use the local simulator by overriding the endpoint URL and using local-test credentials accepted by the simulator's OAuth2 slice.
