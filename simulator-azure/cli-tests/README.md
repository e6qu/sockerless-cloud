# simulator-azure-cli-tests

Integration tests for the Azure simulator using the `az` CLI. Each test builds the simulator binary, starts it on a free port, and runs `az` commands against it.

## Services tested

| Test file | Service | Operations |
|-----------|---------|------------|
| `acr_test.go` | Container Registry | ACR create/list |
| `appserviceplan_test.go` | App Service | Plan create/delete |
| `authorization_test.go` | Authorization | Role assignment create/delete |
| `containerappenv_test.go` | Container Apps | Environment create/delete |
| `dns_test.go` | Private DNS | Zones, record sets, VNet links |
| `functions_test.go` | Functions | Function App create/delete |
| `monitor_test.go` | Monitor | Workspace create/delete |

## Running

```sh
cd simulator-azure/cli-tests
go test -v ./...
```

The test harness (`helpers_test.go`) handles binary build, port allocation, server startup, and shutdown. No external services required.

## Prerequisites

- Go 1.23+
- `az` CLI installed and on `PATH`
- The `simulator-azure/` parent module (built automatically by `TestMain`)

## CLI configuration

Tests configure the Azure CLI to point at the local simulator using `AZURE_CLI_DISABLE_CONNECTION_VERIFICATION=1` and endpoint environment variables.
