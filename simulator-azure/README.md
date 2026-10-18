# simulator-azure

Local reimplementation of a slice of Microsoft Azure. Container Apps job executions honour `replicaTimeout`, App Service and Azure Functions sites run their containers and write real AppTraces entries, Kusto Query Language (KQL) queries run against real log data, and Azure Container Registry (ACR) stores real OCI images.

## Reference adaptor

The simulator exposes one HTTP endpoint (default `:4568`) that fronts all Azure services. Three external tools exercise that endpoint at Azure-API fidelity:

| Adaptor | Min version | What it proves |
|---|---|---|
| [Azure SDK for Go](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk) (`armappcontainers`, `armappservice`, `armcontainerregistry`, ...) | the releases `sdk-tests/go.mod` pins | Wire-level SDK compatibility — ARM REST shape, OData filters, async LRO polling (`Azure-AsyncOperation` / `Location` headers). |
| [`az` CLI](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli) | the host's `az` | `az rest` with a simulator-minted bearer, and native commands after `az cloud register` and `az login` against the simulator over TLS. |
| [Terraform `azurerm` provider](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs) | the version `terraform-tests` pins | Full plan → apply → destroy round-trip across `azurerm_resource_group`, `azurerm_container_app_environment`, `azurerm_container_app_job`, `azurerm_linux_function_app`, `azurerm_log_analytics_workspace`, `azurerm_container_registry`, `azurerm_storage_account`, `azurerm_private_dns_zone`, etc. On macOS the harness runs inside the shared Linux test image so Terraform honours `SSL_CERT_FILE`. |

Anything any of these three tools does against the real Azure endpoint, it must do against this simulator. Gaps from that contract are real bugs (see [BUGS.md](../BUGS.md)).


## Validation

| Test path | What runs |
|---|---|
| `sdk-tests/` | Real Azure SDK for Go clients against the simulator. Per-operation assertions on ARM response shape and error envelopes. |
| `cli-tests/` | The real `az` CLI invoked via `os/exec`, including `az rest` for raw ARM calls. |
| `terraform-tests/` (TLS; Docker delegation on macOS) | The real Terraform `azurerm` and `azuread` providers. |
| `make simulator-azure/test` | The module's unit tests, per [`docs/MAKEFILE_STANDARD.md`](../docs/MAKEFILE_STANDARD.md). |

## Wiring the adaptor

```bash
# 1. Build + start the sim (default :4568).
cd simulator-azure
go build -tags noui -o simulator-azure .
./simulator-azure
```

With `SIM_PERSIST=true`, `SIM_DATA_DIR` is the persistence root: the SQLite
control-plane store plus the Azure Files content root, which defaults to
`<SIM_DATA_DIR>/files` (`SIM_AZURE_FILES_DATA_DIR` overrides) so share
contents survive restarts alongside the metadata that describes them.

Container Apps and Azure Functions calls require Docker or Podman to execute
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
the broker delivers messages sent after the receiver issues credit without
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

`SIM_AZURE_DNS_LISTEN_ADDR` enables an operator-facing DNS listener at an
IPv4 address and port. It serves DNS over UDP and TCP and fails startup if it
cannot bind. Virtual networks reach a second listener the simulator always
runs on every address, at `SIM_DNS_PORT` or a kernel-chosen port, which
answers from the same zones.
`SIM_AZURE_DNS_ZONES` is a comma-separated list of the custom cloud's endpoint
suffixes: the suffix and every name under it answer with
`SIM_AZURE_DNS_TARGET_IPV4` (default `127.0.0.1`) and, when set,
`SIM_AZURE_DNS_TARGET_IPV6`, for `SIM_AZURE_DNS_TTL` seconds (default `60`).
The listener also answers every Azure DNS public zone with its record sets,
and recurses for other names through `SIM_DNS_UPSTREAM` or the host's
resolvers. Inside a virtual network a sidecar stands in for Azure-provided
DNS at the address Azure reserves for it in the network's first subnet (the
subnet's `.2`, "reserved to map the Azure DNS IPs"): every workload on the
network — a virtual machine, a Container Apps replica or job, a container
group, an App Service site with VNet integration — names it as its resolver
and reaches the Instance Metadata Service and the managed identity endpoint
through it. The catalog attributes a query to the network its sidecar
forwarded it from and answers the Azure Private DNS zones linked to that
virtual network ahead of public zones of the same name; a link with
registration enabled registers each machine in its virtual network under its
computer name. Workloads on no virtual network run on the simulator's
platform network, which carries a sidecar of its own, as does each Container
Apps environment's and ACR Tasks run's network.

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
  "eventGrid": "https://{topic}.eventgrid.shim.azure.local/api/events",
  "acr": "https://{name}.azurecr.shim.azure.local/",
  "appServiceScm": "https://{name}.scm.shim.azure.local/"
}' ./simulator-azure
```

Supported template variables are `{name}`, `{account}`, `{vault}`,
`{namespace}`, `{topic}`, `{service}`, `{scheme}`, `{host}`, `{hostname}`,
and `{port}`. Storage Account ARM responses fill `properties.primaryEndpoints`,
Key Vault fills `properties.vaultUri`, Service Bus and Event Hubs fill
`serviceBusEndpoint` plus listKeys connection strings, Event Grid fills topic
and domain publish endpoints, Azure Container Registry fills `loginServer`, App
Service web apps report the `appServiceScm` host as their SCM (Kudu) site — the
Repository entry of `hostNameSslStates`, which `az webapp deploy`, `az webapp
deployment source config-zip` and terraform-provider-azurerm's
`zip_deploy_file` deploy to — and `/metadata/endpoints` emits matching storage
and Key Vault suffixes for Azure clients that validate custom-cloud metadata.
Without an `appServiceScm` template, a web app's SCM host is its subdomain of
the ARM request host, such as `myapp.scm.localhost:4568`.

```bash
# 2. Point Azure clients at it. az rest carries a bearer minted by the
#    simulator's token endpoint (docs/cli.md shows the full setup).
TOKEN=$(curl -s -X POST http://localhost:4568/11111111-1111-1111-1111-111111111111/oauth2/v2.0/token \
  -d grant_type=client_credentials -d client_id=test-client-id -d client_secret=test-client-secret \
  --data-urlencode scope=https://management.azure.com/.default \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
az rest --method GET --headers "Authorization=Bearer $TOKEN" \
  --url "http://localhost:4568/subscriptions/00000000-0000-0000-0000-000000000001?api-version=2021-04-01"
```

The Go SDK takes the simulator's coordinates as a cloud configuration:

```go
import "github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"

cfg := cloud.Configuration{
    ActiveDirectoryAuthorityHost: "http://localhost:4568",
    Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
        cloud.ResourceManager: {Endpoint: "http://localhost:4568", Audience: "http://localhost:4568"},
    },
}
```

For Terraform, serve the simulator over HTTPS and point `azurerm` at its
metadata host; [`docs/terraform.md`](docs/terraform.md) has the full setup:

```hcl
provider "azurerm" {
  client_id       = "test-client-id"
  client_secret   = "test-client-secret"
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  subscription_id = "00000000-0000-0000-0000-000000000001"
  metadata_host   = "azure.sockerless.localhost:8443"

  resource_provider_registrations = "none"

  features {}
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

### Everything else

The simulator serves the Swagger documents vendored under
[`specs/cloud-api/azure`](../specs/cloud-api/azure): 2,653 of 2,660 operations
across 122 documents, the rest answering a declared 501.
[`specs/SIM_PARITY_MATRIX.md`](../specs/SIM_PARITY_MATRIX.md) lists the
services, and [`specs/SIM_SURFACE_TABLES/`](../specs/SIM_SURFACE_TABLES/README.md)
holds the per-operation tables. Storage account `listKeys` returns Azure-shaped
512-bit base64 Shared Keys, deterministic per resource ID and key name, so a
downstream Shared Key verifier validates requests signed with them.

## Special handling

These are the load-bearing wire-quirks the sim implements to satisfy the real adaptors:

- **Double-slash cleanup** — `CleanPathMiddleware` strips leading `//` from paths (the `azurerm` provider appends a trailing slash to the ARM endpoint).
- **Case-insensitive paths** — `azureNormalizeRequestPath`, the simulator's `Config.RewriteRequest`, folds known segments to their registered spelling (`/resourcegroups/` → `/resourceGroups/`).
- **Auth outside mux** — outer middleware serves the OAuth2 token endpoints, so they do not collide with ACR's `/v2/` catch-all.
- **TLS for Terraform and MSAL** — the `azurerm` provider and MSAL-based clients (the Azure CLI's `az login`, `azure-identity`) reach the cloud over HTTPS only, so they talk to the simulator through its TLS listener or the Caddy gateway. On macOS the Terraform harness runs inside the shared Linux test image so the providers trust the CA through `SSL_CERT_FILE`.
- **Host-addressed data-plane DNS** — the Host header routes data-plane requests (`{account}.{service}.<zone>`). The optional simulator DNS listener serves the local zones over UDP/TCP so real clients can use returned Azure-shaped hosts directly.

## Building

```bash
cd simulator-azure && go build -tags noui -o simulator-azure .
```

## Sample

End-to-end via `az rest`; each call carries `--headers "Authorization=Bearer $TOKEN"`, omitted below:

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

$ az rest --method POST --url "http://localhost:4568/v1/workspaces/$WORKSPACE_ID/query" \
    --body '{"query":"ContainerAppConsoleLogs_CL | where ContainerGroupName_s == \"my-job\""}'
{"tables":[{"rows":[["...","my-job","hello-from-aca","stdout"]]}]}
```

## Project structure

One Go file per service family at the module root (`containerapps.go`,
`functions.go`, `acr.go`, `keyvault.go`, `cosmos*.go`, `network*.go`, …),
each registering its routes in `buildSimulator` in `main.go`. The framework —
server, container-engine layer, durable state, OCI data plane — is the
[`sim`](../sim/README.md) module; the Azure-specific pieces of it (the ARM error
shape, the sandbox profiles, the CORS surfaces, the path case-folding and the
Microsoft Entra federation broker) live here. `sdk-tests/`, `cli-tests/` and
`terraform-tests/` are separate Go modules driving the real Azure SDK, the az
CLI and terraform-provider-azurerm against a running simulator.

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

Every Container Apps job execution and replica, Container Instance, Azure Functions site and App Service site runs as a real container on the engine the simulator started against, under the Container Apps or Azure Functions sandbox profile, with its output streamed into Log Analytics as it is produced. `SIM_PLACEMENT` (`service[:qualifier]=container|microvm`, comma-separated; the services are `virtualmachines`, `containerapps`, `containerinstances` and `web`) moves a service onto Firecracker microVMs run by a node agent the simulator starts on its platform network, which needs a Linux engine with `/dev/kvm`; containers are every service's default.

A virtual machine is an instance on the `virtualmachines` backend: its `storageProfile.imageReference` resolves against the marketplace catalog the Compute resource provider lists under `locations/{location}/publishers` — `Canonical:ubuntu-24_04-lts:server` and `server-arm64`, `Debian:debian-12:12` and `12-arm64`, each pinned to an OCI image by digest — or against a `Microsoft.Compute/images` resource, whose `storageProfile.osDisk.blobUri` names either an OCI reference or the blob a capture wrote, or whose `sourceVirtualMachine` is a generalized machine. The machine takes its primary interface's address on its virtual network's engine network, its `osProfile.computerName` as hostname, and runs `osProfile.customData` once at first boot through the instance init, with the output in `/var/log/cloud-init-output.log`; `userData` is served by the Instance Metadata Service and runs nothing, as in Azure. `PowerState/running` reads once the init is ready. A power-off or deallocate stops the instance and keeps its OS disk, which a start boots again; a reimage discards it; a delete removes it unless the disk's `deleteOption` is `Detach`; a capture or an image created from the machine commits the disk and a machine created from that image boots it. Extensions, run commands, patch assessment and installation, and boot diagnostics execute in and read from the instance. cloud-init `#cloud-config` user data is not interpreted. A job execution runs until its container exits, is stopped, or reaches the job's `replicaTimeout`, which the simulator enforces; a stopped execution's container gets SIGTERM and then SIGKILL after the template's `terminationGracePeriodSeconds` (30 by default), and a site's container after its `WEBSITES_CONTAINER_STOP_TIME_LIMIT` (5 by default). Function apps and sites are invoked over HTTP against the container's own listener, and their traces land in Application Insights and Log Analytics, queryable through the KQL subset below.

## Known issues

Open simulator bugs live in [BUGS.md](../BUGS.md).

## What's out of scope

- **Full KQL**: the query engine runs a single tabular expression over one table with the operators `where`, `take`/`limit`, `project`, `project-away`, `project-rename`, `extend`, `order by`/`sort by`, `top`, `count`, `summarize` (`count`, `countif`, `dcount`, `sum`, `avg`, `min`, `max`) and `distinct`, the comparison, string and set operators, and a core set of scalar functions. `let`, `join`, `union` and every other operator or function answer the service's `SyntaxError` or `SemanticError` rather than running.
- **gRPC for Application Insights ingestion**: REST only.
- **Multi-region replication / availability zones**.
- **Cost / billing surfaces**.
- **Identity providers outside the simulator**: Microsoft Entra issues and verifies its own tokens, and the Microsoft Graph `v1.0` and `beta` surfaces serve users, groups, applications, service principals and directory objects; federating an external identity provider into it is a coordinate a deployment supplies.

## Extended examples

Each `az rest` call below carries `--headers "Authorization=Bearer $TOKEN"`,
omitted for brevity; data-plane calls addressed by `Host` carry their own
credentials.

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
az rest --method POST --url "http://localhost:4568/v1/workspaces/$WORKSPACE_ID/query" \
  --body '{"query": "ContainerAppConsoleLogs_CL | where ContainerGroupName_s == \"my-job\""}'
```

### Azure Functions

```bash
# Create a Linux App Service plan (Basic tier; a custom container needs a
# dedicated or Premium plan)
az rest --method PUT \
  --url "http://localhost:4568/subscriptions/.../resourceGroups/my-rg/providers/Microsoft.Web/serverfarms/my-plan?api-version=2022-09-01" \
  --body '{"location":"eastus","kind":"linux","sku":{"name":"B1","tier":"Basic"},"properties":{"reserved":true}}'

# Create a Function App that runs a container image: the image's entrypoint
# runs with appCommandLine as its command, listening on WEBSITES_PORT (80 unset)
az rest --method PUT \
  --url "http://localhost:4568/subscriptions/.../resourceGroups/my-rg/providers/Microsoft.Web/sites/my-func-app?api-version=2022-09-01" \
  --body '{
    "location": "eastus", "kind": "functionapp",
    "properties": {
      "serverFarmId": ".../serverfarms/my-plan",
      "siteConfig": {
        "linuxFxVersion": "DOCKER|myregistry.azurecr.io/my-function:latest",
        "appCommandLine": "serve --port 8080",
        "appSettings": [
          {"name": "FUNCTIONS_WORKER_RUNTIME", "value": "custom"},
          {"name": "WEBSITES_PORT", "value": "8080"}
        ]
      }
    }
  }'

# Send a request to the site's hostname; the front end forwards it to the container
az rest --method POST --url "http://localhost:4568/api/function" --body '{}' \
  --headers "Host=my-func-app.azurewebsites.net" --skip-authorization-header

# Query AppTraces
az rest --method POST --url "http://localhost:4568/v1/workspaces/$WORKSPACE_ID/query" \
  --body '{"query": "AppTraces | where AppRoleName == \"my-func-app\""}'

# Create a Linux web app on a built-in stack (NODE|20-lts, NODE|22-lts or
# PYTHON|3.12): the site runs the platform's mcr.microsoft.com/appsvc image for
# that stack on the content its deployments wrote, and serves the platform's
# default page until something is deployed
az rest --method PUT \
  --url "http://localhost:4568/subscriptions/.../resourceGroups/my-rg/providers/Microsoft.Web/sites/my-node-app?api-version=2022-09-01" \
  --body '{"location": "eastus", "kind": "app,linux",
    "properties": {"serverFarmId": ".../serverfarms/my-plan", "siteConfig": {"linuxFxVersion": "NODE|20-lts"}}}'

# Deploy a zip package from a URL through the MSDeploy extension; the
# deployment restarts the site on the new content
az rest --method PUT \
  --url "http://localhost:4568/subscriptions/.../resourceGroups/my-rg/providers/Microsoft.Web/sites/my-node-app/extensions/MSDeploy?api-version=2022-09-01" \
  --body '{"properties": {"packageUri": "https://example.com/app.zip"}}'

# Create a Linux function app on the built-in Node|22 stack: the site runs the
# Azure Functions host image (mcr.microsoft.com/azure-functions/node, the
# 4-node22-appservice build) on its deployed content. With
# AzureWebJobsSecretStorageType=files the host reads its keys from
# /home/data/Functions/secrets, which holds the keys the ARM key operations
# list and change
az functionapp create -g my-rg -p my-plan -n my-node-func -s mystorage \
  --runtime node --runtime-version 22 --functions-version 4 --os-type Linux
az functionapp config appsettings set -g my-rg -n my-node-func \
  --settings AzureWebJobsSecretStorageType=files
az functionapp deploy -g my-rg -n my-node-func --src-url https://example.com/func.zip --type zip
az functionapp function keys list -g my-rg -n my-node-func --function-name hello
```

### Log Analytics

```bash
# Create workspace
az rest --method PUT \
  --url ".../providers/Microsoft.OperationalInsights/workspaces/my-workspace?api-version=2022-10-01" \
  --body '{"location":"eastus","properties":{"retentionInDays":30}}'

# Ingest rows through a data collection rule that declares the stream and
# routes it to a table of the workspace, addressed by the rule's immutable ID
az rest --method POST \
  --url "http://localhost:4568/dataCollectionRules/$DCR_IMMUTABLE_ID/streams/Custom-Logs" \
  --body '[{"TimeGenerated":"2025-01-01T00:00:00Z","Message":"running"}]'

# KQL query
az rest --method POST --url "http://localhost:4568/v1/workspaces/$WORKSPACE_ID/query" \
  --body '{"query": "ContainerAppConsoleLogs_CL | where ContainerGroupName_s == \"my-job\" | take 100"}'
```

`$WORKSPACE_ID` is the workspace's `customerId`. A Container Apps job's output
reaches the workspace its environment names in `appLogsConfiguration`.

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
