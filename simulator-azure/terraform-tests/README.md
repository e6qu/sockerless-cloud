# simulator-azure-terraform-tests

Integration tests that run `terraform apply` and `terraform destroy` against the Azure simulator. Verifies that the simulator implements enough of the Azure ARM API surface for real Terraform providers to provision and tear down resources.

Resources covered (azurerm — sim ships custom cloud metadata + OAuth2 token endpoint + JWKS so azurerm bootstraps against the sim instead of real Azure):
- `azurerm_resource_group`
- `azurerm_virtual_network` / `azurerm_subnet`
- `azurerm_network_security_group` / `azurerm_network_security_rule`
- `azurerm_storage_account` (Azure Files shares; a second account backs the Function App)
- `azurerm_storage_container` / `azurerm_storage_table` (storage data plane)
- `azurerm_storage_share` + `azurerm_storage_share_directory` (Azure Files data plane; the nested directory a Container Apps / Azure Functions volume mount walks)
- `azurerm_key_vault` + access policy + secret / key / certificate (control and data plane)
- `azurerm_container_registry` (Standard)
- `azurerm_user_assigned_identity`
- `azurerm_public_ip` / `azurerm_public_ip_prefix` / `azurerm_nat_gateway` + associations / `azurerm_lb` + backend pool + probe + rule
- `azurerm_network_interface` + `azurerm_linux_virtual_machine`
- `azurerm_private_dns_zone` / `azurerm_dns_zone` + A record
- `azurerm_eventgrid_topic` / domain / domain topic / system topic
- `azurerm_eventhub_namespace` + eventhub + consumer group + authorization rule
- `azurerm_servicebus_namespace` + queue
- `azurerm_cosmosdb_account` + SQL database + container + table
- `azurerm_redis_cache` + `azurerm_redis_firewall_rule`
- `azurerm_log_analytics_workspace`
- `azurerm_application_insights`
- `azurerm_container_app_environment` + `azurerm_container_app` + `azurerm_container_app_job`
- `azurerm_logic_app_workflow` / `azurerm_container_group`
- `azurerm_service_plan` + `azurerm_linux_function_app`
- `azurerm_api_management` + API + product + subscription
- `azurerm_application_gateway` (the layer-7 load balancer, with its listener, URL path map, probes and routing rule)
- `azurerm_network_watcher` (the provider refuses to create NSG flow logs — Azure retired their creation on 2025-06-30 — so the simulator's flowLogs surface is covered by the SDK and CLI suites instead)
- `azurerm_network_manager`

The `entra/` stack (`entra_apply_test.go`) applies the `hashicorp/azuread` provider against the
simulator's Microsoft Graph endpoints (`/v1.0` and `/beta`), with
`metadata_host` as its only coordinate: `azuread_application`,
`azuread_application_password`, `azuread_service_principal`, `azuread_user`
with a manager, `azuread_group` and `azuread_group_member`. The
`subscription/` stack (`subscription_apply_test.go`) applies the subscription-scoped resources.
The `webslots/` stack (`webslots_apply_test.go`) applies a Linux web app with an
`azurerm_linux_web_app_slot` and makes the slot the active one with
`azurerm_web_app_active_slot`, which swaps it into production; each slot runs
its own container, and the test reads what each hostname serves after the
swap.

### Instance discovery is not on the azurerm authentication path

The simulator serves Microsoft Entra ID instance discovery
(`GET /common/discovery/instance`), but no Terraform resource exercises it and
none can. The azurerm provider authenticates through `go-azure-sdk`, which
performs a plain OAuth2 client-credentials exchange against the Active
Directory endpoint named by `metadata_host` — it never builds an MSAL authority
and so never calls instance discovery. The proof is in this harness itself: it
points the provider at an `http://` endpoint, which MSAL rejects outright
because it requires an https authority, and the apply still succeeds.

That endpoint's client surfaces are therefore the CLI (`simulator-azure/cli-tests/az_login_test.go`)
and the SDK (`simulator-azure/sdk-tests/auth_instance_discovery_test.go`).

## Running

These tests require Terraform and Docker. On Linux, direct `go test` runs Terraform locally. On macOS, the harness delegates the same test command into the shared Linux Docker test image because Go's Security.framework-backed trust store ignores `SSL_CERT_FILE`.

```sh
# Inside Docker (via the parent simulator Makefile)
cd simulator-azure
make docker-test

# Or directly; macOS delegates this command into Linux Docker
cd simulator-azure/terraform-tests
go test -v ./...
```

The test harness (`helpers_test.go`) handles simulator binary build, port allocation, simulator startup, Caddy HTTPS gateway startup, Terraform init/apply/destroy, and shutdown.

## Prerequisites

- The Go toolchain this module's `go.mod` names
- `terraform` CLI installed and on `PATH` for direct Linux runs; the shared Docker image supplies Terraform for macOS delegation
- `caddy` installed and on `PATH` for direct Linux runs; the shared Docker image supplies Caddy for macOS delegation
- Docker (required for Container Apps resources and for macOS Linux-container delegation)
- The `simulator-azure/` parent module (built automatically by `TestMain`)

## TLS requirement

The AzureRM Terraform provider hardcodes `https://` for metadata endpoint calls. The test harness starts the simulator on HTTP loopback, starts the repo's Caddy HTTPS gateway in front of it, and points Terraform at `https://azure.sockerless.localhost:<port>`. Terraform trusts Caddy's local CA via `SSL_CERT_FILE`.

## How it works

1. `TestMain` builds the Azure simulator binary and starts it on HTTP loopback
2. The harness starts Caddy with isolated per-test state and waits for its local CA
3. The harness verifies Azure metadata JSON through the HTTPS gateway
4. Tests write Terraform configurations to a temp directory
5. `terraform init` downloads the Terraform providers used by the test configuration
6. `terraform apply -auto-approve` provisions resources against the simulator
7. Test assertions verify the Terraform state
8. `terraform destroy -auto-approve` tears down resources
