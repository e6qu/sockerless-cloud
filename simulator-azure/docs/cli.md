# Using the Azure simulator with the Azure CLI

## Prerequisites

- Azure CLI installed (`az version`)
- Simulator running on `http://localhost:4568`

## Setup

The simulator verifies a bearer token on every Azure Resource Manager request.
Mint one from its Microsoft Entra token endpoint with the client-credentials
grant, and send it with `az rest`, which passes the request through the real
Azure CLI's request and response handling:

```sh
export AZURE_CONFIG_DIR=/tmp/azure-sim-config
export AZURE_CORE_NO_COLOR=1

SIM=http://localhost:4568
TENANT=11111111-1111-1111-1111-111111111111
SUB=00000000-0000-0000-0000-000000000001
RG=my-resource-group

TOKEN=$(curl -s -X POST "$SIM/$TENANT/oauth2/v2.0/token" \
  -d grant_type=client_credentials -d client_id=test-client-id \
  -d client_secret=test-client-secret \
  --data-urlencode scope=https://management.azure.com/.default \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
azr() { az rest --headers "Authorization=Bearer $TOKEN" "$@"; }
```

The native commands (`az group create`, `az keyvault create`, …) work too once
the CLI is logged in to the simulator as a custom cloud. MSAL requires an HTTPS
authority, so serve the simulator over TLS (`SIM_TLS_CERT`/`SIM_TLS_KEY`, or the
Caddy gateway) and trust its CA through `REQUESTS_CA_BUNDLE`:

```sh
az cloud register -n sockerless-sim \
  --endpoint-resource-manager https://localhost:4568 \
  --endpoint-active-directory https://localhost:4568/adfs \
  --endpoint-active-directory-resource-id https://management.azure.com/ \
  --endpoint-active-directory-graph-resource-id https://localhost:4568
az cloud set -n sockerless-sim
az login --service-principal -u test-client-id -p test-client-secret \
  --tenant $TENANT --allow-no-subscriptions
```

## Creating a resource group

Most operations require a resource group to exist first:

```sh
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG?api-version=2021-04-01" \
  --body '{"location":"eastus"}'
```

## Examples

### Container App Jobs

```sh
# Create a Container App Environment
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.App/managedEnvironments/my-env?api-version=2023-05-01" \
  --body '{"location":"eastus","properties":{}}'

# Create a Container App Job
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.App/jobs/my-job?api-version=2023-05-01" \
  --body '{
    "location": "eastus",
    "properties": {
      "environmentId": "/subscriptions/'"$SUB"'/resourceGroups/'"$RG"'/providers/Microsoft.App/managedEnvironments/my-env",
      "configuration": {"triggerType": "Manual", "replicaTimeout": 300, "replicaRetryLimit": 0},
      "template": {
        "containers": [{"name": "main", "image": "nginx:latest"}]
      }
    }
  }'

# Start an execution
azr --method POST \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.App/jobs/my-job/start?api-version=2023-05-01" \
  --body '{}'

# List executions
azr --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.App/jobs/my-job/executions?api-version=2023-05-01"
```

### Azure Functions

```sh
# Create an App Service Plan
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Web/serverfarms/my-plan?api-version=2022-09-01" \
  --body '{"location":"eastus","sku":{"name":"Y1","tier":"Dynamic"}}'

# Create a Function App
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Web/sites/my-funcapp?api-version=2022-09-01" \
  --body '{
    "location": "eastus",
    "kind": "functionapp",
    "properties": {
      "serverFarmId": "/subscriptions/'"$SUB"'/resourceGroups/'"$RG"'/providers/Microsoft.Web/serverfarms/my-plan",
      "siteConfig": {
        "appSettings": [
          {"name": "FUNCTIONS_EXTENSION_VERSION", "value": "~4"},
          {"name": "FUNCTIONS_WORKER_RUNTIME", "value": "node"}
        ]
      }
    }
  }'

# Get the Function App
azr --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Web/sites/my-funcapp?api-version=2022-09-01"
```

### Azure Container Registry

```sh
# Create a registry
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.ContainerRegistry/registries/myregistry?api-version=2023-01-01-preview" \
  --body '{"location":"eastus","sku":{"name":"Basic"}}'

# Get registry
azr --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.ContainerRegistry/registries/myregistry?api-version=2023-01-01-preview"
```

### Azure Cache for Redis

```sh
# Create a Redis cache
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Cache/Redis/myredis?api-version=2024-11-01" \
  --body '{"location":"eastus","sku":{"name":"Basic","family":"C","capacity":1},"properties":{"redisVersion":"6"}}'

# List keys
azr --method POST \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Cache/Redis/myredis/listKeys?api-version=2024-11-01"

# Create a firewall rule
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Cache/Redis/myredis/firewallRules/allow-ci?api-version=2024-11-01" \
  --body '{"properties":{"startIP":"203.0.113.10","endIP":"203.0.113.10"}}'
```

### Private DNS

```sh
# Create a zone
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/privateDnsZones/example.local?api-version=2018-09-01" \
  --body '{"location":"global"}'

# Create an A record
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/privateDnsZones/example.local/A/myhost?api-version=2018-09-01" \
  --body '{"properties":{"ttl":300,"aRecords":[{"ipv4Address":"10.0.0.1"}]}}'

# Get the zone
azr --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/privateDnsZones/example.local?api-version=2018-09-01"
```

### Public DNS

```sh
# Create a public DNS zone
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/dnsZones/example.com?api-version=2018-05-01" \
  --body '{"location":"global"}'

# Create a public A record
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/dnsZones/example.com/A/www?api-version=2018-05-01" \
  --body '{"properties":{"TTL":300,"ARecords":[{"ipv4Address":"203.0.113.10"}]}}'

# List all record sets in the zone
azr --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/dnsZones/example.com/all?api-version=2018-05-01"
```

### Networking

```sh
# Create a VNet
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/virtualNetworks/my-vnet?api-version=2023-05-01" \
  --body '{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.0.0.0/16"]}}}'

# Create a subnet
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/virtualNetworks/my-vnet/subnets/my-subnet?api-version=2023-05-01" \
  --body '{"properties":{"addressPrefix":"10.0.1.0/24"}}'

# Create a Network Security Group
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/networkSecurityGroups/my-nsg?api-version=2023-05-01" \
  --body '{"location":"eastus","properties":{"securityRules":[]}}'
```

### Storage

```sh
# Create a storage account
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Storage/storageAccounts/mystorageacct?api-version=2023-01-01" \
  --body '{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"}}'

# List storage account keys
azr --method POST \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Storage/storageAccounts/mystorageacct/listKeys?api-version=2023-01-01"
```

### Log Analytics

```sh
# Create a workspace
azr --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.OperationalInsights/workspaces/my-workspace?api-version=2022-10-01" \
  --body '{"location":"eastus","properties":{}}'

# Get workspace
azr --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.OperationalInsights/workspaces/my-workspace?api-version=2022-10-01"
```

## Coverage

[`specs/SIM_PARITY_MATRIX.md`](../../specs/SIM_PARITY_MATRIX.md) lists the
served services, and
[`specs/SIM_TEST_COVERAGE_MATRIX.md`](../../specs/SIM_TEST_COVERAGE_MATRIX.md)
names the Azure CLI test of each surface (`simulator-azure/cli-tests/`).

## Notes

- State lives in memory unless the simulator runs with `SIM_PERSIST=true`, which
  keeps it under `SIM_DATA_DIR` across restarts.
- Every Azure Resource Manager request carries the `api-version` query
  parameter, and the simulator validates it.
- Request another resource's token with its own scope, such as
  `https://vault.azure.net/.default` for the Key Vault data plane.
