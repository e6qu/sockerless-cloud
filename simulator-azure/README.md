# simulator-azure

Local reimplementation of the Azure slice that sockerless touches. Not a mock — Container Apps job executions respect `replicaTimeout` for completion, Azure Functions invoke and produce real AppTraces entries, Kusto Query Language (KQL) queries parse and filter against real log data, and Azure Container Registry (ACR) stores real OCI manifests with chunked upload support.

## Reference adaptor

The simulator exposes one HTTP endpoint (default `:4568`) that fronts all Azure services. Three external tools exercise that endpoint at Azure-API fidelity:

| Adaptor | Min version | What it proves |
|---|---|---|
| [Azure SDK for Go](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk) (`armappcontainers`, `armappservice`, `armcontainerregistry`, ...) | v3+ | Wire-level SDK compatibility — ARM REST shape, OData filters, async LRO polling (`Azure-AsyncOperation` / `Location` headers). |
| [`az` CLI](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli) | 2.60+ | Endpoint-override fidelity (the sim accepts `https://management.azure.com` traffic when fronted with TLS). |
| [Terraform `azurerm` provider](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs) | v4+ | Full plan → apply → destroy round-trip across `azurerm_resource_group`, `azurerm_container_app_environment`, `azurerm_container_app_job`, `azurerm_linux_function_app`, `azurerm_log_analytics_workspace`, `azurerm_container_registry`, `azurerm_storage_account`, `azurerm_private_dns_zone`, etc. macOS direct `go test` delegates to the shared Linux Docker test image so Terraform can honor `SSL_CERT_FILE`. |

Anything any of these three tools does against the real Azure endpoint, it must do against this simulator. Gaps from that contract are real bugs (see [BUGS.md](../BUGS.md)).

The simulator is the **upstream** for the [Azure Container Apps](https://github.com/e6qu/sockerless/blob/main/backends/aca/README.md) and [Azure Functions](https://github.com/e6qu/sockerless/blob/main/backends/azure-functions/README.md) backends during local development and CI.

The Storage data plane includes Blob container/blob CRUD, block staging,
and Copy Blob via the public `x-ms-copy-source` REST operation. Copy Blob
accepts simulator host-style and Azurite-style path-style source URLs,
copies the stored source bytes, and returns Azure copy ID/status headers.

## Validation

| Test path | What runs | Last green |
|---|---|---|
| `sdk-tests/` (31 tests) | Real Azure SDK for Go clients against the sim. Per-op assertions on ARM response shape + error envelopes. | 2026-05-13 |
| `cli-tests/` (17 tests) | Real `az` CLI invoked via `os/exec` (using `az rest` for raw ARM calls). | 2026-05-13 |
| `terraform-tests/` (TLS; Docker delegation on macOS) | Real Terraform `azurerm` provider against the sim. | 2026-05-28 |
| `make simulator-azure/test` | Leaf-Makefile unit + integration suite per [`docs/MAKEFILE_STANDARD.md`](../docs/MAKEFILE_STANDARD.md). | 2026-05-13 |

## Wiring the adaptor

```bash
# 1. Build + start the sim (default :4568).
cd simulator-azure
go build -o simulator-azure .
SIM_LISTEN_ADDR=:4568 ./simulator-azure
```

With `SIM_PERSIST=true`, `SIM_DATA_DIR` is the persistence root: the SQLite
control-plane store plus the Azure Files content root, which defaults to
`<SIM_DATA_DIR>/files` (`SIM_AZURE_FILES_DATA_DIR` overrides) so share
contents survive restarts alongside the metadata that describes them.

Docker or Podman is required when Container Apps or Azure Functions calls execute
workloads. For API-only checks that do not invoke workload execution,
`SIM_RUNTIME=process` starts the Azure simulator without initializing
Docker/Podman.
The `/health` response reports `runtime` and
`capabilities.workloadExecution`; clients must require that capability before
submitting work that needs a running container.

Service Bus raw AMQP/TLS is a second, optional listener because real
Azure Service Bus exposes AMQP as a TCP/TLS transport in addition to
HTTP/REST and WebSocket tunneling:

```bash
SIM_LISTEN_ADDR=:4568 \
SIM_SERVICEBUS_AMQP_LISTEN_ADDR=:5671 \
SIM_SERVICEBUS_AMQP_TLS_CERT=server-cert.pem \
SIM_SERVICEBUS_AMQP_TLS_KEY=server-key.pem \
./simulator-azure
```

Official `azservicebus` SDK callers can then keep a normal Service Bus
connection string and point `ClientOptions.CustomEndpoint` at the raw
AMQP listener. The listener also accepts the shared `SIM_TLS_CERT` /
`SIM_TLS_KEY` values when the Service Bus-specific cert variables are
unset. AMQP queue and topic receivers honor outstanding flow credit:
messages sent after the receiver has issued credit are delivered without
requiring another flow frame.

### Local DNS for host-addressed data planes

Azure data planes are host-addressed. Blob Storage uses hosts like
`<account>.blob.core.windows.net`, Service Bus uses
`<namespace>.servicebus.windows.net`, and Event Grid topics/domains return
`*.eventgrid.azure.net` publish endpoints. The simulator preserves that
shape for local custom-cloud endpoints, so local clients need DNS that can
resolve the simulator's host-style names.

The Azure simulator can serve those names directly:

```bash
SIM_LISTEN_ADDR=:4568 \
SIM_AZURE_DNS_LISTEN_ADDR=127.0.0.1:53 \
SIM_AZURE_DNS_ZONES=sockerless.azure.local,localhost \
SIM_AZURE_DNS_TARGET_IPV4=127.0.0.1 \
./simulator-azure
```

`SIM_AZURE_DNS_LISTEN_ADDR` enables the DNS listener. It serves DNS over
UDP and TCP and fails startup if it cannot bind. `SIM_AZURE_DNS_ZONES`
is a comma-separated list of local zones to answer. `SIM_AZURE_DNS_TTL`
defaults to `60`. `SIM_AZURE_DNS_TARGET_IPV4` defaults to `127.0.0.1`;
`SIM_AZURE_DNS_TARGET_IPV6` is optional and is only answered when set.

For macOS, configure a resolver for the local simulator zone after the
DNS listener is running on port 53:

```bash
sudo mkdir -p /etc/resolver
printf 'nameserver 127.0.0.1\n' | sudo tee /etc/resolver/sockerless.azure.local
```

For systemd-resolved Linux hosts, route the simulator zone to the local
DNS listener on the active link:

```bash
sudo resolvectl dns "$(ip route show default | awk '{print $5; exit}')" 127.0.0.1
sudo resolvectl domain "$(ip route show default | awk '{print $5; exit}')" '~sockerless.azure.local'
```

Then use the local custom-cloud host as the ARM endpoint:

```bash
az rest --method GET \
  --url "http://sockerless.azure.local:4568/subscriptions/00000000-0000-0000-0000-000000000001?api-version=2021-04-01"
```

ARM-returned data-plane endpoints such as
`http://myacct.blob.sockerless.azure.local:4568/`,
`sb://myns.servicebus.sockerless.azure.local:4568/`, and
`http://mytopic.eventgrid.sockerless.azure.local:4568/api/events` keep the
same host-addressed contract and resolve through the simulator DNS server.

### Advertised data-plane endpoints

The ARM control plane can advertise data-plane hosts that are different from
the ARM request host. This is for deployments where another local component
fronts the data plane, such as a shim translating Azure-shaped Blob, Key Vault,
Service Bus, or Event Grid calls to another backing cloud. The public ARM API
shape does not change; only the normal Azure response fields point at the
configured host.

Configure the projection with `SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON`:

```bash
SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON='{
  "storage": {
    "blob": "https://{account}.blob.shim.azure.local/",
    "file": "https://{account}.file.shim.azure.local/",
    "queue": "https://{account}.queue.shim.azure.local/",
    "table": "https://{account}.table.shim.azure.local/",
    "web": "https://{account}.web.shim.azure.local/",
    "dfs": "https://{account}.dfs.shim.azure.local/"
  },
  "keyVault": "https://{vault}.vault.shim.azure.local/",
  "serviceBus": "https://{namespace}.servicebus.shim.azure.local/",
  "eventGrid": "https://{topic}.eventgrid.shim.azure.local/api/events"
}' ./simulator-azure
```

Supported template variables are `{name}`, `{account}`, `{vault}`,
`{namespace}`, `{topic}`, `{service}`, `{scheme}`, `{host}`, `{hostname}`,
and `{port}`. Storage Account ARM responses fill `properties.primaryEndpoints`,
Key Vault fills `properties.vaultUri`, Service Bus and Event Hubs fill
`serviceBusEndpoint` plus listKeys connection strings, Event Grid fills topic
and domain publish endpoints, and `/metadata/endpoints` emits matching storage
and Key Vault suffixes for Azure clients that validate custom-cloud metadata.

```bash
# 2. Point Azure clients at it.
# For az CLI: use az rest with explicit URL.
az rest --method GET --url "http://localhost:4568/subscriptions/00000000-0000-0000-0000-000000000001?api-version=2021-04-01"

# For the Go SDK:
```

```go
import "github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"

cfg := cloud.Configuration{
    ActiveDirectoryAuthorityHost: "http://localhost:4568",
    Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
        cloud.ResourceManager: {Endpoint: "http://localhost:4568", Audience: "http://localhost:4568"},
    },
}
```

For Terraform, use TLS + Docker (see `terraform-tests/` Makefile):

```hcl
provider "azurerm" {
  features {}
  environment      = "custom"
  metadata_host    = "localhost:4568"
  client_id        = "00000000-0000-0000-0000-000000000001"
  tenant_id        = "00000000-0000-0000-0000-000000000001"
  subscription_id  = "00000000-0000-0000-0000-000000000001"
  skip_provider_registration = true
}
```

## Services

### Authentication & Metadata

| Service | Endpoints |
|---|---|
| **OAuth2** | Token endpoint (`/{tenantId}/oauth2/v2.0/token`), OpenID discovery, JWKS |
| **Metadata** | `/metadata/endpoints` — cloud metadata (ARM endpoint, suffixes) |
| **Subscription** | Get subscription, list providers |

OAuth2 access tokens are RS256-signed JWTs. The simulator publishes the
matching RSA public key at `/{tenantId}/discovery/v2.0/keys`, so clients that
follow Azure AD's JWKS validation flow can verify simulator-issued bearer
tokens without shared secrets.

### Compute & Containers

| Service | Endpoints |
|---|---|
| **Container App Environments** | CRUD for managed environments |
| **Container App Jobs** | CRUD, Start execution, Stop execution, List/Get executions |
| **Azure Functions (Sites)** | CRUD for function apps, List functions, Invoke (`/api/function`) |
| **App Service Plans** | CRUD (serverFarms) |
| **ACR** | Registry CRUD, Name availability, [OCI Distribution](https://github.com/opencontainers/distribution-spec) (`/v2/` manifests + blobs + chunked upload) |

### Infrastructure

| Service | Endpoints |
|---|---|
| **Resource Groups** | CRUD, List resources, HEAD existence check |
| **Virtual Networks / Subnets / NSGs** | CRUD (subnets with delegations + NSG references; NSGs with security rules) |
| **Managed Identity** | User-assigned identity CRUD |
| **Authorization** | Role definitions (list with OData filter), Role assignments at any scope |

### Storage & Data

| Service | Endpoints |
|---|---|
| **Storage Accounts** | CRUD, List keys |
| **File Shares** | CRUD under storage accounts |
| **Storage Data-Plane** | Host-based routing (`{account}.blob.localhost:{port}`) for blob/file service properties and ACLs |
| **Cosmos DB for NoSQL** | ARM databaseAccounts, SQL databases, containers, throughput settings, listKeys/listConnectionStrings; SQL data-plane database/container/document CRUD and query |

Storage account `listKeys` returns Azure-shaped 512-bit base64 SharedKeys that
are deterministic per resource ID and key name, so downstream SharedKey
verifiers can validate requests signed with the simulator-emitted account key.

### Monitoring

| Service | Endpoints |
|---|---|
| **Log Analytics Workspaces** | CRUD, Shared keys |
| **Log Ingestion** | POST entries via data collection rules |
| **Log Query** | KQL query execution (simple `where`/`take` parsing) |
| **Application Insights** | Component CRUD, Billing features, Query |

### DNS

| Service | Endpoints |
|---|---|
| **Private DNS Zones** | `Microsoft.Network/privateDnsZones` zone CRUD, list-by-resource-group, and auto-created SOA record |
| **Public DNS Zones** | `Microsoft.Network/dnsZones` zone CRUD, list-by-resource-group, and record-set CRUD |
| **A Records** | CRUD under zones |
| **Virtual Network Links** | CRUD and list-by-zone |

## Special handling

These are the load-bearing wire-quirks the sim implements to satisfy the real adaptors:

- **Double-slash cleanup** — `CleanPathMiddleware` strips leading `//` from paths (the `azurerm` provider appends a trailing slash to the ARM endpoint).
- **Case-insensitive paths** — `AzurePathNormalizationMiddleware` normalises known segments (e.g., `/resourcegroups/` → `/resourceGroups/`).
- **Auth outside mux** — OAuth2 token endpoints are handled as outer middleware to avoid conflicts with ACR's `/v2/` catch-all.
- **TLS for Terraform** — Azure Terraform tests use self-signed certs because the `azurestack` provider hardcodes `https://`. On macOS, direct `go test` delegates into Linux Docker so the providers validate the generated CA through `SSL_CERT_FILE`.
- **Host-addressed data-plane DNS** — Data-plane requests are matched by Host header (`{account}.{service}.<zone>`). The optional simulator DNS listener serves the local zones over UDP/TCP so real clients can use returned Azure-shaped hosts directly.
- **Sync creates return 200** — `go-azure-sdk` treats 200 as immediate completion for `BeginCreate` LRO; the sim returns 200 instead of 201 for synchronous creates.

## Building

```bash
cd simulator-azure && go build -o simulator-azure .
```

## Sample

End-to-end via `az rest`:

```bash
$ SIM_LISTEN_ADDR=:4568 ./simulator-azure &

$ az rest --method PUT \
    --url "http://localhost:4568/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/my-rg?api-version=2021-04-01" \
    --body '{"location":"eastus"}'
{"name":"my-rg","location":"eastus","properties":{"provisioningState":"Succeeded"}}

$ az rest --method PUT \
    --url "http://localhost:4568/subscriptions/.../resourceGroups/my-rg/providers/Microsoft.App/jobs/my-job?api-version=2023-05-01" \
    --body '{"location":"eastus","properties":{...,"template":{"containers":[{"image":"alpine","command":["echo","hello-from-aca"]}]}}}'

$ az rest --method POST --url ".../jobs/my-job/start?api-version=2023-05-01" --body '{}'

$ az rest --method POST --url "http://localhost:4568/v1/workspaces/default/query" \
    --body '{"query":"ContainerAppConsoleLogs_CL | where ContainerGroupName_s == \"my-job\""}'
{"tables":[{"rows":[["...","my-job","hello-from-aca","stdout"]]}]}
```

## Project structure

```
azure/
├── main.go                 Entry point, middleware setup, service registration
├── auth.go                 OAuth2 tokens, OpenID discovery, path cleanup (102 lines)
├── authorization.go        Role definitions + assignments (263 lines)
├── metadata.go             Cloud metadata endpoint (69 lines)
├── subscription.go         Subscription + providers (65 lines)
├── resourcegroups.go       Resource group CRUD (103 lines)
├── network.go              VNets, subnets, NSGs (336 lines)
├── managedidentity.go      User-assigned identities (102 lines)
├── containerappsenv.go     Container App Environments (139 lines)
├── containerapps.go        Container App Jobs + executions (479 lines)
├── appserviceplan.go       App Service Plans (132 lines)
├── functions.go            Function Apps + invoke (312 lines)
├── acr.go                  Container Registry + OCI Distribution (491 lines)
├── files.go                Storage accounts, file shares, data-plane (481 lines)
├── monitor.go              Log Analytics, log ingestion, KQL query (348 lines)
├── insights.go             Application Insights (169 lines)
├── dns.go                  Private DNS zones, A records, VNet links (406 lines)
├── shared/                 Shared simulator framework
├── sdk-tests/              SDK integration tests (31 tests)
├── cli-tests/              CLI integration tests (17 tests)
└── terraform-tests/        Terraform apply/destroy tests (TLS; Docker delegation on macOS)
```

## Testing

```bash
# SDK tests (Azure SDK for Go against the running sim)
cd sdk-tests && go test -v ./...

# CLI tests (az CLI shell-outs)
cd cli-tests && go test -v ./...

# Terraform tests (TLS; macOS delegates to Docker)
cd terraform-tests && go test -v ./...
```

## Execution model

Container Apps job executions honor the `replicaTimeout` configuration (in seconds). When a command is provided, the simulator executes it as a real process and streams output to Log Analytics. When a replica timeout is configured and no command is present, the execution auto-completes with `Succeeded` status after that duration. When no timeout and no command are set, the execution stays running until explicitly stopped. Azure Functions invocations are synchronous and inject AppTraces entries queryable via KQL.

## Known issues

Active Azure simulator bugs live in [BUGS.md](../BUGS.md). The Terraform macOS TLS harness issue was fixed by delegating direct macOS test runs into the shared Linux Docker test image.

## What's out of scope

- **Full KQL parser**: only `where` + `take` + `limit` + simple `==` / `>=` predicates are supported.
- **gRPC for Application Insights ingestion**: REST only.
- **Multi-region replication / availability zones**.
- **Real authentication**: tokens are accepted but not cryptographically verified.
- **Cost / billing surfaces**.
- **Azure AD identity flows beyond OAuth2 token issuance** (`/.well-known/openid-configuration` + JWKS are provided so SDK validators pass; user / group / app management is not modelled).

## Extended examples

### Container Apps Jobs

```bash
# Create job
az rest --method PUT \
  --url "http://localhost:4568/subscriptions/.../resourceGroups/my-rg/providers/Microsoft.App/jobs/my-job?api-version=2023-05-01" \
  --body '{
    "location": "eastus",
    "properties": {
      "configuration": {"replicaTimeout": 30, "triggerType": "Manual", "manualTriggerConfig": {"parallelism":1,"replicaCompletionCount":1}},
      "template": {"containers": [{"name":"app","image":"alpine:latest","command":["echo","hello-from-aca"]}]}
    }
  }'

# Start execution
az rest --method POST --url ".../jobs/my-job/start?api-version=2023-05-01" --body '{}'

# Query Log Analytics for the execution output
az rest --method POST --url "http://localhost:4568/v1/workspaces/default/query" \
  --body '{"query": "ContainerAppConsoleLogs_CL | where ContainerGroupName_s == \"my-job\""}'
```

### Azure Functions

```bash
# Create App Service Plan (Consumption tier)
az rest --method PUT \
  --url "http://localhost:4568/subscriptions/.../resourceGroups/my-rg/providers/Microsoft.Web/serverfarms/my-plan?api-version=2022-09-01" \
  --body '{"location":"eastus","sku":{"name":"Y1","tier":"Dynamic"}}'

# Create Function App with normal container configuration
az rest --method PUT \
  --url "http://localhost:4568/subscriptions/.../resourceGroups/my-rg/providers/Microsoft.Web/sites/my-func-app?api-version=2022-09-01" \
  --body '{
    "location": "eastus", "kind": "functionapp",
    "properties": {
      "serverFarmId": ".../serverfarms/my-plan",
      "siteConfig": {
        "linuxFxVersion": "DOCKER|myregistry.azurecr.io/my-function:latest",
        "appSettings": [
          {"name": "FUNCTIONS_WORKER_RUNTIME", "value": "custom"}
        ]
      }
    }
  }'

# Invoke the configured container/function endpoint
az rest --method POST --url "http://localhost:4568/api/function" --body '{}'

# Query AppTraces
az rest --method POST --url "http://localhost:4568/v1/workspaces/default/query" \
  --body '{"query": "AppTraces | where AppRoleName == \"my-func-app\""}'
```

### Log Analytics

```bash
# Create workspace
az rest --method PUT \
  --url ".../providers/Microsoft.OperationalInsights/workspaces/my-workspace?api-version=2022-10-01" \
  --body '{"location":"eastus","properties":{"retentionInDays":30}}'

# Ingest entries via data collection rule
az rest --method POST \
  --url "http://localhost:4568/dataCollectionRules/dcr-1/streams/Custom-Logs" \
  --body '[{"TimeGenerated":"2025-01-01T00:00:00Z","ContainerGroupName_s":"my-job","Log_s":"running","Stream_s":"stdout"}]'

# KQL query (supports where, take/limit, datetime filters)
az rest --method POST --url "http://localhost:4568/v1/workspaces/default/query" \
  --body '{"query": "ContainerAppConsoleLogs_CL | where ContainerGroupName_s == \"my-job\" | take 100"}'
```

### ACR (Container Registry)

```bash
az rest --method PUT \
  --url ".../providers/Microsoft.ContainerRegistry/registries/myregistry?api-version=2023-01-01-preview" \
  --body '{"location":"eastus","sku":{"name":"Basic"},"properties":{"adminUserEnabled":false}}'

# OCI Distribution endpoints under /v2/:
#   GET  /v2/                                    → version check
#   POST /v2/{repo}/blobs/uploads/               → initiate blob upload
#   PATCH /v2/{repo}/blobs/uploads/{uuid}        → chunked upload
#   PUT  /v2/{repo}/blobs/uploads/{uuid}?digest= → finalize blob
#   PUT  /v2/{repo}/manifests/{tag}              → push manifest
#   GET  /v2/{repo}/manifests/{ref}              → pull manifest
```

### Storage

```bash
az rest --method PUT \
  --url ".../providers/Microsoft.Storage/storageAccounts/mystorageacct?api-version=2023-05-01" \
  --body '{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"}}'

az rest --method POST \
  --url ".../providers/Microsoft.Storage/storageAccounts/mystorageacct/listKeys?api-version=2023-05-01"
```

See also: [`backends/aca/README.md`](https://github.com/e6qu/sockerless/blob/main/backends/aca/README.md), [`backends/azure-functions/README.md`](https://github.com/e6qu/sockerless/blob/main/backends/azure-functions/README.md), [`specs/CLOUD_RESOURCE_MAPPING.md § Azure`](https://github.com/e6qu/sockerless/blob/main/specs/CLOUD_RESOURCE_MAPPING.md).
