# simulator-azure

`simulator-azure` serves a slice of Microsoft Azure from one binary on one
port, `4568`: the Azure Resource Manager control plane, Microsoft Entra ID's
token service and Microsoft Graph, and the services' data planes, which it
tells apart by the request's `Host` as Azure does (`<account>.blob.<host>`,
`<vault>.vault.<host>`, `<namespace>.servicebus.<host>`). It verifies every
Azure Resource Manager bearer token and every data plane's own credential, and
runs virtual machines, containers and sites as real workloads on a container
engine. Unit tests check every route it serves against the Swagger documents
vendored in [`specs/cloud-api/azure/`](../specs/cloud-api/azure/), and
`SOCKERLESS_SPEC_VALIDATE` checks live responses against them.

## Run it

Install a release, run its container image, or build from a checkout:

```sh
go install github.com/e6qu/sockerless-cloud/simulator-azure@<release-commit> && simulator-azure

docker run --rm -p 4568:4568 -e SIM_RUNTIME=process ghcr.io/e6qu/sockerless-simulator-azure:vX.Y.Z

# From the repository root: build the console, then embed it with -tags console.
(cd ui && bun install && bunx turbo run build --filter=@sockerless/ui-simulator-azure)
make simulator-azure/build && ./simulator-azure/simulator-azure
```

`go install` and a plain `go build` embed no console; the release binaries and
image embed it. Go pins a release by its commit, because the repository tags
only `vX.Y.Z` at its root.

The simulator needs a Docker or Podman engine to run virtual machines,
Azure Container Apps, Azure Container Instances, App Service and Azure
Functions sites, Azure Container Registry Tasks runs, Azure Database for
PostgreSQL servers and Azure Cache for Redis caches, and refuses to start without one. `SIM_RUNTIME=process`
starts it without an engine for API-only use; every operation that would run a
workload is then refused. `GET /health` reports `runtime` and
`capabilities.workloadExecution`.

[`docs/configuration.md`](../docs/configuration.md) lists the variables every
simulator shares (`SIM_LISTEN_ADDR`, `SIM_AZURE_PORT`, `SIM_TLS_CERT`,
`SIM_TLS_KEY`, `SIM_PERSIST`, `SIM_DATA_DIR`, `SIM_RUNTIME`, `SIM_PLACEMENT`,
`SIM_LOG_LEVEL`, the console's sign-in). These are Azure's own:

| Variable | Default | Effect |
|---|---|---|
| `SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON` | unset | Templates for the data-plane endpoints Azure Resource Manager responses advertise ([Terraform](#terraform-hashicorpazurerm)) |
| `SIM_AZURE_DNS_LISTEN_ADDR` | unset | An IPv4 `host:port` where the simulator answers DNS over UDP and TCP for the host. Startup fails if it can't bind. |
| `SIM_AZURE_DNS_ZONES` | `localhost,sockerless.azure.local` | Endpoint suffixes, comma-separated: each one and every name under it resolves to the simulator |
| `SIM_AZURE_DNS_TARGET_IPV4`, `SIM_AZURE_DNS_TARGET_IPV6` | `127.0.0.1`, unset | The addresses those names resolve to |
| `SIM_AZURE_DNS_TTL` | `60` | The TTL of those answers, in seconds |
| `SIM_DNS_PORT` | a kernel-chosen port | The port of the resolver virtual networks reach through their sidecars |
| `SIM_DNS_UPSTREAM` | the host's `resolv.conf` | Resolvers for names no zone holds |
| `SIM_AZURE_FILES_DATA_DIR` | `<SIM_DATA_DIR>/files` | Where Azure Files share contents live, one directory per `<account>/<share>` |
| `SIM_SERVICEBUS_AMQP_LISTEN_ADDR` | unset | Serve Azure Service Bus and Azure Event Hubs AMQP over TLS at this address |
| `SIM_SERVICEBUS_AMQP_TLS_CERT`, `SIM_SERVICEBUS_AMQP_TLS_KEY` | `SIM_TLS_CERT`, `SIM_TLS_KEY` | That listener's certificate and key |
| `SOCKERLESS_CONSOLE_FEDERATION_ENDPOINT`, `_TENANT`, `_CLIENT_ID` | unset | Where the console federates operators into Microsoft Entra ID. Set the tenant and client ID together; an empty endpoint means the console's own origin. |
| `SOCKERLESS_CONSOLE_CLOUD_API_ENDPOINT`, `_LOGS_API_ENDPOINT`, `_GRAPH_API_ENDPOINT` | the console's origin | The Azure Resource Manager, Log Analytics and Microsoft Graph endpoints the console reads |
| `SOCKERLESS_SPEC_VALIDATE`, `SOCKERLESS_SPEC_DIR` | unset | Validate every response against the Swagger documents in `SOCKERLESS_SPEC_DIR`, writing violations to the file `SOCKERLESS_SPEC_VALIDATE` names |

## Connect a client

Every client signs in as the seeded bootstrap application, a Microsoft Entra ID
service principal:

| Coordinate | Value |
|---|---|
| Tenant ID | `11111111-1111-1111-1111-111111111111` |
| Client ID | `test-client-id` |
| Client secret | `test-client-secret` |
| Subscription ID | `00000000-0000-0000-0000-000000000001` |
| Token endpoint | `<endpoint>/<tenant>/oauth2/v2.0/token` (v1: `<endpoint>/<tenant>/oauth2/token`) |
| Cloud metadata | `<endpoint>/metadata/endpoints?api-version=2022-09-01` |

The token endpoint issues RS256 JWTs for the `scope` (v2) or `resource` (v1) a
client asks for, such as `https://management.azure.com/.default` or
`https://vault.azure.net/.default`, and publishes its keys at
`<endpoint>/<tenant>/discovery/v2.0/keys`. Applications created through
Microsoft Graph sign in the same way.

Azure Resource Manager authorizes every operation with Azure RBAC, as Azure
does. The bootstrap service principal is Owner on the tenant root management
group, which holds every subscription. Any other principal, a managed identity
among them, does only what its role assignments grant, and a Microsoft Entra
bearer reaches blobs, queues, tables, Key Vault objects and Event Grid topics
only through a data role such as Storage Blob Data Reader. A storage account
whose `allowSharedKeyAccess` is `false` refuses its keys.
[Identity and access](../docs/iam.md#authorization-with-azure-rbac) describes
the evaluation.

MSAL, `azidentity` and `azure-identity` accept only an HTTPS authority. A
client that signs in through one of them needs the simulator's TLS listener
(`SIM_TLS_CERT`, `SIM_TLS_KEY`) or the [HTTPS gateway](#terraform-hashicorpazurerm).
A client that fetches its own token works over plain HTTP.

### Azure SDK for Go

The SDK suite runs over plain HTTP. Its credential (`fakeCredential` in
`sdk-tests/helpers_test.go`) runs the client-credentials grant against the
token endpoint, and `clientOpts` gives every client this configuration:

```go
opts := &arm.ClientOptions{ClientOptions: azcore.ClientOptions{
    Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
        cloud.ResourceManager: {Endpoint: "http://localhost:4568", Audience: "https://management.azure.com/"},
    }},
    InsecureAllowCredentialWithHTTP: true,
}}
```

Against the TLS listener, `azidentity` signs in itself. Name the authority host
and turn instance discovery off, or MSAL asks `login.microsoftonline.com` to
vouch for the host. No suite runs this form:

```go
simCloud := cloud.Configuration{
    ActiveDirectoryAuthorityHost: "https://localhost:4568/",
    Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
        cloud.ResourceManager: {Endpoint: "https://localhost:4568", Audience: "https://management.azure.com/"},
    },
}
cred, err := azidentity.NewClientSecretCredential(tenantID, "test-client-id", "test-client-secret",
    &azidentity.ClientSecretCredentialOptions{
        ClientOptions: azcore.ClientOptions{Cloud: simCloud}, DisableInstanceDiscovery: true,
    })
```

`azservicebus` and `azeventhubs` clients take a connection string from the
namespace's `listKeys` and the [AMQP listener](#azure-service-bus-over-amqp) as
`CustomEndpoint` (`sdk-tests/messaging_sas_auth_test.go`).

### Azure CLI

Over plain HTTP, mint a token and pass it to `az rest`, as the CLI suite's
`azRest` (`cli-tests/helpers_test.go`) does for every Azure Resource Manager
call, with an isolated `AZURE_CONFIG_DIR`:

```sh
TOKEN=$(curl -s -X POST http://localhost:4568/11111111-1111-1111-1111-111111111111/oauth2/v2.0/token \
  -d grant_type=client_credentials -d client_id=test-client-id -d client_secret=test-client-secret \
  --data-urlencode scope=https://management.azure.com/.default | jq -r .access_token)
az rest --method PUT --headers "Authorization=Bearer $TOKEN" --body '{"location":"eastus"}' \
  --url "http://localhost:4568/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/my-rg?api-version=2021-04-01"
```

For the native commands, register the TLS listener as a cloud and log in, as
`TestAzLogin_ServicePrincipalAgainstSimulatorCloud` (`cli-tests/az_login_test.go`)
does. `--endpoint-active-directory` takes the AD FS form, `<endpoint>/adfs`,
the one authority shape MSAL resolves against the configured host.
`REQUESTS_CA_BUNDLE` names the certificate authority the CLI trusts:

```sh
export REQUESTS_CA_BUNDLE=/path/to/ca.pem
az cloud register -n sockerless-sim \
  --endpoint-resource-manager https://localhost:4568 \
  --endpoint-active-directory https://localhost:4568/adfs \
  --endpoint-active-directory-resource-id https://management.azure.com/ \
  --endpoint-active-directory-graph-resource-id https://localhost:4568
az cloud set -n sockerless-sim
az login --service-principal -u test-client-id -p test-client-secret \
  --tenant 11111111-1111-1111-1111-111111111111 --allow-no-subscriptions
az group create -n my-rg -l eastus
```

### Terraform (`hashicorp/azurerm`)

azurerm reads the cloud's endpoints from `https://<metadata_host>/metadata/endpoints`,
over HTTPS only, and signs in at the login endpoint that document names. It
reaches each storage account, key vault and app's deployment (SCM) site at a
host name of its own. The Caddy gateway in
[`make/https-gateway/Caddyfile`](../make/https-gateway/Caddyfile) terminates
TLS for `azure.sockerless.localhost:8443` and each data plane's wildcard under
it (`*.blob.`, `*.file.`, `*.queue.`, `*.table.`, `*.vault.`, `*.servicebus.`,
`*.eventgrid.`, `*.documents.`, `*.azurecr.`, `*.scm.`), and forwards to the
simulator at `SOCKERLESS_AZURE_SIM_PORT` (default 4568).

Start the simulator advertising the gateway's names, start Caddy from the
repository root, and trust Caddy's local root (on macOS it lives under
`~/Library/Application Support/Caddy/`):

```sh
G=azure.sockerless.localhost:8443
SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON='{
  "storage": {"blob": "https://{account}.blob.'$G'/", "file": "https://{account}.file.'$G'/",
              "queue": "https://{account}.queue.'$G'/", "table": "https://{account}.table.'$G'/"},
  "keyVault": "https://{vault}.vault.'$G'/", "serviceBus": "https://{namespace}.servicebus.'$G'/",
  "eventGrid": "https://{topic}.eventgrid.'$G'/api/events", "acr": "https://{name}.azurecr.'$G'/",
  "appServiceScm": "https://{name}.scm.'$G'/"}' simulator-azure &
caddy run --config make/https-gateway/Caddyfile --adapter caddyfile &
export SSL_CERT_FILE="$HOME/.local/share/caddy/pki/authorities/local/root.crt"
```

The templates fill `primaryEndpoints`, `vaultUri`, `serviceBusEndpoint`,
`listKeys` connection strings, Event Grid endpoints, `loginServer` and SCM
hosts, and take `{name}`, `{account}`, `{vault}`, `{namespace}`, `{topic}`,
`{service}`, `{scheme}`, `{host}`, `{hostname}` and `{port}`. Without one, each
endpoint is a subdomain of the request's host. Point the provider at the
gateway:

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

`TestMain` and `azureGatewayDataPlaneEndpoints` in
`terraform-tests/helpers_test.go` set the suite up exactly this way, and
[Part 3 of the tutorial](../docs/tutorial.md#part-3-azure), which CI runs as
`TestTerraformTutorial`, walks through it by hand. Where `*.localhost` doesn't
resolve to 127.0.0.1, add the names to `/etc/hosts` or answer them with
`SIM_AZURE_DNS_LISTEN_ADDR`. The `hashicorp/azuread` provider takes the same
`metadata_host` and reaches Microsoft Graph at `/v1.0` and `/beta`.

### Azure SDK for Python

`azure-identity` needs the TLS listener, with instance discovery off; each
management client takes the endpoint as `base_url`, and `REQUESTS_CA_BUNDLE`
names the certificate authority. No suite runs the Python libraries:

```python
from azure.identity import ClientSecretCredential
from azure.mgmt.resource.resources import ResourceManagementClient

ENDPOINT = "https://localhost:4568"
credential = ClientSecretCredential(
    "11111111-1111-1111-1111-111111111111", "test-client-id", "test-client-secret",
    authority=ENDPOINT, disable_instance_discovery=True)
client = ResourceManagementClient(credential, "00000000-0000-0000-0000-000000000001", base_url=ENDPOINT)
client.resource_groups.create_or_update("my-rg", {"location": "eastus"})
```

## Services

[`specs/SIM_SURFACE_TABLES/`](../specs/SIM_SURFACE_TABLES/README.md) lists the
operations of each surface whose routes register under a literal path; the
handler files hold the rest.

| Area | Service | Namespace or data-plane host | Surface table |
|---|---|---|---|
| Identity | Microsoft Entra ID token service and Microsoft Graph | `/{tenant}/oauth2/…`, `/v1.0/…`, `/beta/…` | [entra](../specs/SIM_SURFACE_TABLES/azure-entra.md) |
| Identity | Managed identities | `Microsoft.ManagedIdentity`, `/metadata/identity/oauth2/token`, `/msi/token` | [workload_identity](../specs/SIM_SURFACE_TABLES/azure-workload_identity.md) |
| Identity | Azure role-based access control | `Microsoft.Authorization` | none |
| Management | Resource groups, resources and tags | `Microsoft.Resources` | [resourcegroups](../specs/SIM_SURFACE_TABLES/azure-resourcegroups.md), [resources](../specs/SIM_SURFACE_TABLES/azure-resources.md) |
| Management | Subscriptions | `Microsoft.Subscription` | [subscription](../specs/SIM_SURFACE_TABLES/azure-subscription.md), [operations](../specs/SIM_SURFACE_TABLES/azure-subscription_operations.md), [ownership](../specs/SIM_SURFACE_TABLES/azure-subscription_ownership.md), [policy](../specs/SIM_SURFACE_TABLES/azure-subscription_policy.md) |
| Monitoring | Azure Monitor metrics, diagnostic settings and data collection rules | `Microsoft.Insights`, `/dataCollectionRules/{id}/streams/{stream}` | [monitor](../specs/SIM_SURFACE_TABLES/azure-monitor.md), [monitor_dcr](../specs/SIM_SURFACE_TABLES/azure-monitor_dcr.md) |
| Monitoring | Azure Monitor activity log | `Microsoft.Insights/eventtypes` | [activitylog](../specs/SIM_SURFACE_TABLES/azure-activitylog.md) |
| Monitoring | Log Analytics | `Microsoft.OperationalInsights`, `/v1/workspaces/{id}/query` | [monitor](../specs/SIM_SURFACE_TABLES/azure-monitor.md) |
| Monitoring | Application Insights | `Microsoft.Insights/components`, `/v1/apps/{appId}/…` | [insights_dataplane](../specs/SIM_SURFACE_TABLES/azure-insights_dataplane.md) |
| Compute | Azure Virtual Machines | `Microsoft.Compute`, `/metadata/instance` | [compute](../specs/SIM_SURFACE_TABLES/azure-compute.md), [vm_operations](../specs/SIM_SURFACE_TABLES/azure-compute_vm_operations.md), [vm_extensions](../specs/SIM_SURFACE_TABLES/azure-compute_vm_extensions.md), [vm_patches](../specs/SIM_SURFACE_TABLES/azure-compute_vm_patches.md), [images](../specs/SIM_SURFACE_TABLES/azure-compute_images_catalog.md) |
| Compute | Azure Container Apps | `Microsoft.App`, an app's ingress FQDN | [containerapps](../specs/SIM_SURFACE_TABLES/azure-containerapps.md), [replicas](../specs/SIM_SURFACE_TABLES/azure-containerapps_replicas.md) |
| Compute | Azure Container Instances | `Microsoft.ContainerInstance` | [containerinstance](../specs/SIM_SURFACE_TABLES/azure-containerinstance.md) |
| Compute | Azure App Service, Azure Functions and Azure Static Web Apps | `Microsoft.Web`, `<site>.azurewebsites.net`, `<site>.scm.<host>` | [functions](../specs/SIM_SURFACE_TABLES/azure-functions.md), [appserviceplan](../specs/SIM_SURFACE_TABLES/azure-appserviceplan.md) |
| Compute | Azure Logic Apps | `Microsoft.Logic` | [logicapps](../specs/SIM_SURFACE_TABLES/azure-logicapps.md) |
| Containers | Azure Container Registry and ACR Tasks | `Microsoft.ContainerRegistry`, `<registry>.azurecr.<host>/v2/` | [acr](../specs/SIM_SURFACE_TABLES/azure-acr.md), [acr_tasks](../specs/SIM_SURFACE_TABLES/azure-acr_tasks.md), [acr_dataplane_properties](../specs/SIM_SURFACE_TABLES/azure-acr_dataplane_properties.md) |
| Networking | Azure Virtual Network, network security groups, Azure Load Balancer, Azure Application Gateway, Azure NAT Gateway, public IP addresses, Azure Private Link, Azure Network Watcher, Azure Virtual Network Manager | `Microsoft.Network` | [network](../specs/SIM_SURFACE_TABLES/azure-network.md) |
| Networking | Azure DNS | `Microsoft.Network/dnsZones` | [public_dns](../specs/SIM_SURFACE_TABLES/azure-public_dns.md), [dns_more](../specs/SIM_SURFACE_TABLES/azure-dns_more.md) |
| Networking | Azure Private DNS | `Microsoft.Network/privateDnsZones` | [private-dns](../specs/SIM_SURFACE_TABLES/azure-private-dns.md), [dns](../specs/SIM_SURFACE_TABLES/azure-dns.md) |
| Networking | Azure API Management | `Microsoft.ApiManagement` | [apim](../specs/SIM_SURFACE_TABLES/azure-apim.md) |
| Storage | Azure Storage accounts | `Microsoft.Storage` | [storage](../specs/SIM_SURFACE_TABLES/azure-storage.md) |
| Storage | Azure Blob Storage, Azure Files, Azure Queue Storage, Azure Table Storage | `<account>.blob.<host>`, `.file.`, `.queue.`, `.table.` | [storage-data-plane](../specs/SIM_SURFACE_TABLES/azure-storage-data-plane.md) |
| Security | Azure Key Vault and Azure Key Vault Managed HSM | `Microsoft.KeyVault`, `<vault>.vault.<host>` | [keyvault](../specs/SIM_SURFACE_TABLES/azure-keyvault.md), [kv-data-plane](../specs/SIM_SURFACE_TABLES/azure-kv-data-plane.md), [managedhsm](../specs/SIM_SURFACE_TABLES/azure-keyvault_managedhsm.md) |
| Databases | Azure Cosmos DB, with the Azure Cosmos DB for NoSQL data plane | `Microsoft.DocumentDB`, `<account>.documents.<host>` | [cosmos](../specs/SIM_SURFACE_TABLES/azure-cosmos.md) |
| Databases | Azure Database for PostgreSQL flexible server | `Microsoft.DBforPostgreSQL`, `<server>.postgres.database.<host>:5432` through `SIM_AZURE_DNS_LISTEN_ADDR` | [postgresql-flexible-server](../specs/SIM_SURFACE_TABLES/azure-postgresql-flexible-server.md) |
| Databases | Azure Cache for Redis | `Microsoft.Cache` | [cache_redis](../specs/SIM_SURFACE_TABLES/azure-cache_redis.md) |
| Messaging | Azure Service Bus | `Microsoft.ServiceBus`, `<namespace>.servicebus.<host>`, AMQP | [servicebus-arm](../specs/SIM_SURFACE_TABLES/azure-servicebus-arm.md), [servicebus-data-plane](../specs/SIM_SURFACE_TABLES/azure-servicebus-data-plane.md), [servicebus-admin](../specs/SIM_SURFACE_TABLES/azure-servicebus-admin.md) |
| Messaging | Azure Event Hubs | `Microsoft.EventHub`, AMQP | [eventhubs](../specs/SIM_SURFACE_TABLES/azure-eventhubs.md) |
| Messaging | Azure Event Grid | `Microsoft.EventGrid`, `<topic>.eventgrid.<host>/api/events` | [eventgrid](../specs/SIM_SURFACE_TABLES/azure-eventgrid.md) |

## How it runs your workloads

[`docs/architecture.md`](../docs/architecture.md) covers what every simulator
shares: placement on containers or Firecracker microVMs (Azure's
`SIM_PLACEMENT` services are `virtualmachines`, `containerapps`,
`containerinstances`, `web` and `redis`), network sidecars and persistence. These parts
are Azure's own.

### Virtual networks

Each address prefix of a virtual network is an engine network over that
range, and a workload in a subnet takes its address on the network of the
prefix the subnet lies in. Each network carries a sidecar at the address
Azure reserves for its DNS, the lowest subnet's `.2`, and every workload on it
names the sidecar as its resolver. The resolver answers the Azure Private DNS
zones linked to that virtual network ahead of public zones of the same name,
then every Azure DNS zone and the `SIM_AZURE_DNS_ZONES` suffixes, and recurses
for the rest. A link with registration enabled registers each virtual machine
under its computer name. Workloads on no virtual network join the platform
network, and each Container Apps environment on no virtual network gets a
network of its own. Network security groups filter the flows the simulator
forwards (Azure Load Balancer and Azure Application Gateway backends), not
traffic between two containers on one network.

### Instance Metadata Service and managed identities

A virtual machine boots the image its `storageProfile.imageReference` names:
a marketplace image (`Canonical:ubuntu-24_04-lts:server` or `server-arm64`,
`Debian:debian-12:12` or `12-arm64`, each pinned by digest) or a
`Microsoft.Compute/images` resource. It runs cloud-init over
`osProfile.customData`, and keeps its OS disk across a stop or deallocate.

The sidecar serves the Azure Instance Metadata Service (`/metadata/instance`,
`/metadata/attested/document`) and its managed identity token endpoint
(`/metadata/identity/oauth2/token`), all requiring `Metadata: true`; the
simulator knows the machine by its source address. The machine receives
`AZURE_INSTANCE_METADATA_ENDPOINT` naming its sidecar. An App Service, Azure
Functions or Azure Container Apps workload with a managed identity receives
`IDENTITY_ENDPOINT` (`http://<sidecar>/msi/token`) and `IDENTITY_HEADER`; one
without an identity gets neither, as in Azure. The simulator signs every token
with the key its JWKS publishes.

### App Service and Azure Functions sites

The App Service front end routes by `Host`. A request for an app's or slot's
host name (`<site>.azurewebsites.net`, `<site>-<slot>.azurewebsites.net`, or a
bound custom host name) goes to that site's container, which the front end
starts if none is running:

```sh
curl -H "Host: my-func.azurewebsites.net" "http://localhost:4568/api/hello?code=$FUNCTION_KEY"
```

A site runs its `linuxFxVersion`: a `DOCKER|<image>`, the App Service image for
`NODE|20-lts`, `NODE|22-lts` or `PYTHON|3.12`, or the Azure Functions host for
`Node|22`, on the content its deployments wrote (zip deploy, MSDeploy, or the
Kudu APIs on its SCM site, `<site>.scm.<host>`). A stop sends SIGTERM, then
SIGKILL after `WEBSITES_CONTAINER_STOP_TIME_LIMIT` seconds, 5 by default. Site
output reaches the site's docker log, the `AppTraces` table of its Application
Insights component, and `AppServiceConsoleLogs` in each workspace a diagnostic
setting names.

### Azure Container Apps

A revision runs its replicas, each a container group whose sidecars share the
main container's network namespace. Ingress routes by `Host`: a request for
the app's `configuration.ingress.fqdn`, a revision's FQDN or a traffic label's
FQDN goes to a ready replica, by traffic weight. Revisions scale on HTTP,
Azure Queue Storage and `azure-servicebus` rules, or on the default HTTP rule.
A job execution runs `parallelism` replicas, retries each up to
`replicaRetryLimit`, and stops one at `replicaTimeout` (1800 seconds by
default) with SIGTERM, then SIGKILL after `terminationGracePeriodSeconds` (30
by default). `Manual` jobs run on `start`, `Schedule` jobs on their cron
expression. Console output reaches `ContainerAppConsoleLogs_CL` in the
workspace the environment's `appLogsConfiguration` names, which
`/v1/workspaces/{customerId}/query` answers in Kusto Query Language (KQL).

### Azure Service Bus over AMQP

Azure Service Bus serves its REST data plane at `<namespace>.servicebus.<host>`
and AMQP over WebSocket at `/$servicebus/websocket` on that host. With
`SIM_SERVICEBUS_AMQP_LISTEN_ADDR=:5671` set it also serves AMQP over TLS
there, with the certificate `SIM_SERVICEBUS_AMQP_TLS_CERT` names, and Azure
Event Hubs shares that listener. A connection authenticates through the claims-based security (CBS)
`put-token` handshake, and the simulator checks each shared access signature
against the namespace's authorization rules. Receivers honour their link
credit, peek-lock settles through dispositions, and session-enabled entities
lock sessions.

## Test it

```sh
make simulator-azure/unit-test       # the module's own tests: no engine
make simulator-azure/sdk-test        # Azure SDK for Go clients
make simulator-azure/cli-test        # the Azure CLI
make simulator-azure/terraform-test  # azurerm and azuread through the HTTPS gateway
```

- **Unit tests** check every registered route against the vendored Swagger
  documents, hold the operation-coverage floor, and run the parsers' fuzz seed
  corpora (KQL, OData `$filter`, AMQP).
- **SDK suite** (`sdk-tests/`) builds the simulator into `.build/sdk-tests/`
  and starts it on a free port with the AMQP listener, the DNS listener,
  advertised data-plane endpoints and its own Azure Files directory. It needs
  a Linux engine; on macOS `make` runs it in the Linux test image.
- **CLI suite** (`cli-tests/`) needs `az` on `PATH`. It drives `az rest` over
  plain HTTP, and starts TLS simulators of its own for `az login` and the
  native commands.
- **Terraform suite** (`terraform-tests/`) needs `terraform` and `caddy` (or
  `CADDY=/path/to/caddy`). It starts the simulator, then the gateway on free
  ports with its own Caddy state, and runs one test per configuration:
  `main.tf` and the `entra`, `subscription`, `tutorial`, `webslots`,
  `webkvscm`, `kvconsumers`, `forwardedheaders`, `redis`, `rgdelete` and
  `rgmissing` stacks. `TERRAFORM_TEST_ARGS` selects one (`-run TestTerraformApplyDestroy`)
  and `TERRAFORM_TEST_TIMEOUT` bounds it. On macOS the suite runs in the
  Linux test image, because Go there ignores `SSL_CERT_FILE`.

## Limits

- Microsoft Entra ID is the simulator's own; it federates no external
  identity provider.
- Azure Cache for Redis runs real Redis nodes, but refuses `shardCount` above
  0, and its import and export move no data.
- A storage account's static website (`.web.`) and Azure Data Lake Storage
  (`.dfs.`) endpoints answer a declared error.
- Application Insights has no ingestion API; its tables fill from site output.
- KQL runs one tabular expression over one table with `where`, `take`,
  `project`, `project-away`, `project-rename`, `extend`, `order by`, `top`,
  `count`, `summarize` and `distinct`; `let`, `join`, `union` and every other
  operator answer the service's error.
- A workload reaches metadata and DNS at its sidecar's address, not at
  `169.254.169.254` or `168.63.129.16`, and workloads in two address prefixes
  of one virtual network don't reach each other.
- Event-triggered Container Apps jobs run only on `start`.
- Multi-region replication, availability zones and billing are out of scope.

[`BUGS.md`](../BUGS.md) lists every open gap.
