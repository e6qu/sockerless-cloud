# Using the Azure simulator with the Azure CLI

## Prerequisites

- Azure CLI installed (`az version`)
- Simulator running on `http://localhost:4568`

## Setup

The Azure CLI doesn't natively support pointing high-level ARM commands at a custom endpoint. The simulator CLI tests use `az rest`, which sends raw HTTP requests to the simulator's ARM endpoint while still using the real Azure CLI request and response handling.

Set up an isolated config directory:

```sh
export AZURE_CONFIG_DIR=/tmp/azure-sim-config
export AZURE_CORE_NO_COLOR=1
```

Define helper variables:

```sh
SIM=http://localhost:4568
SUB=00000000-0000-0000-0000-000000000001
RG=my-resource-group
```

## Creating a resource group

Most operations require a resource group to exist first:

```sh
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG?api-version=2021-04-01" \
  --body '{"location":"eastus"}'
```

## Examples

### Container App Jobs

```sh
# Create a Container App Environment
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.App/managedEnvironments/my-env?api-version=2023-05-01" \
  --body '{"location":"eastus","properties":{}}'

# Create a Container App Job
az rest --method PUT \
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
az rest --method POST \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.App/jobs/my-job/start?api-version=2023-05-01" \
  --body '{}'

# List executions
az rest --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.App/jobs/my-job/executions?api-version=2023-05-01"
```

### Azure Functions

```sh
# Create an App Service Plan
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Web/serverfarms/my-plan?api-version=2022-09-01" \
  --body '{"location":"eastus","sku":{"name":"Y1","tier":"Dynamic"}}'

# Create a Function App
az rest --method PUT \
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
az rest --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Web/sites/my-funcapp?api-version=2022-09-01"
```

### Azure Container Registry

```sh
# Create a registry
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.ContainerRegistry/registries/myregistry?api-version=2023-01-01-preview" \
  --body '{"location":"eastus","sku":{"name":"Basic"}}'

# Get registry
az rest --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.ContainerRegistry/registries/myregistry?api-version=2023-01-01-preview"
```

### Azure Cache for Redis

```sh
# Create a Redis cache
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Cache/Redis/myredis?api-version=2024-11-01" \
  --body '{"location":"eastus","sku":{"name":"Basic","family":"C","capacity":1},"properties":{"redisVersion":"6"}}'

# List keys
az rest --method POST \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Cache/Redis/myredis/listKeys?api-version=2024-11-01"

# Create a firewall rule
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Cache/Redis/myredis/firewallRules/allow-ci?api-version=2024-11-01" \
  --body '{"properties":{"startIP":"203.0.113.10","endIP":"203.0.113.10"}}'
```

### Private DNS

```sh
# Create a zone
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/privateDnsZones/example.local?api-version=2018-09-01" \
  --body '{"location":"global"}'

# Create an A record
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/privateDnsZones/example.local/A/myhost?api-version=2018-09-01" \
  --body '{"properties":{"ttl":300,"aRecords":[{"ipv4Address":"10.0.0.1"}]}}'

# Get the zone
az rest --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/privateDnsZones/example.local?api-version=2018-09-01"
```

### Public DNS

```sh
# Create a public DNS zone
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/dnsZones/example.com?api-version=2018-05-01" \
  --body '{"location":"global"}'

# Create a public A record
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/dnsZones/example.com/A/www?api-version=2018-05-01" \
  --body '{"properties":{"TTL":300,"ARecords":[{"ipv4Address":"203.0.113.10"}]}}'

# List all record sets in the zone
az rest --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/dnsZones/example.com/all?api-version=2018-05-01"
```

### Networking

```sh
# Create a VNet
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/virtualNetworks/my-vnet?api-version=2023-05-01" \
  --body '{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.0.0.0/16"]}}}'

# Create a subnet
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/virtualNetworks/my-vnet/subnets/my-subnet?api-version=2023-05-01" \
  --body '{"properties":{"addressPrefix":"10.0.1.0/24"}}'

# Create a Network Security Group
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Network/networkSecurityGroups/my-nsg?api-version=2023-05-01" \
  --body '{"location":"eastus","properties":{"securityRules":[]}}'
```

### Storage

```sh
# Create a storage account
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Storage/storageAccounts/mystorageacct?api-version=2023-01-01" \
  --body '{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"}}'

# List storage account keys
az rest --method POST \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Storage/storageAccounts/mystorageacct/listKeys?api-version=2023-01-01"
```

### Log Analytics

```sh
# Create a workspace
az rest --method PUT \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.OperationalInsights/workspaces/my-workspace?api-version=2022-10-01" \
  --body '{"location":"eastus","properties":{}}'

# Get workspace
az rest --method GET \
  --url "$SIM/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.OperationalInsights/workspaces/my-workspace?api-version=2022-10-01"
```

## Supported services

| Service | API Provider | Notes |
|---------|-------------|-------|
| Container App Environments | `Microsoft.App` | CRUD |
| Container App Jobs | `Microsoft.App` | CRUD + executions |
| Azure Functions | `Microsoft.Web` | Sites + invocation |
| App Service Plans | `Microsoft.Web` | CRUD |
| Container Registry | `Microsoft.ContainerRegistry` | CRUD + OCI Distribution |
| Azure Cache for Redis | `Microsoft.Cache` | Cache lifecycle, listKeys, firewall rules |
| Resource Groups | `Microsoft.Resources` | CRUD |
| Virtual Networks | `Microsoft.Network` | VNets, subnets, NSGs |
| Private DNS | `Microsoft.Network` | Zones, A records, VNet links |
| Managed Identity | `Microsoft.ManagedIdentity` | User-assigned identities |
| Authorization | `Microsoft.Authorization` | Role definitions, assignments |
| Storage | `Microsoft.Storage` | Accounts, file shares, keys |
| Log Analytics | `Microsoft.OperationalInsights` | Workspaces, queries |
| Application Insights | `Microsoft.Insights` | Components |

## Automated bash tests

A self-contained bash test script exercises Resource Groups, Container Apps, Functions, Storage, Monitor, and App Service Plans via `az rest` in both JSON and table output modes:

```sh
cd simulator-azure/bash-tests
./test_azure_cli.sh
```

The script builds the simulator, starts it on a random port, runs 42 tests, and prints a pass/fail summary.

## Notes

- The simulator accepts local-test credentials through its OAuth2 token endpoint and returns token shapes that Azure clients can consume. ARM uses `https://management.azure.com/.default`; data-plane clients can request their own audience with OAuth v2 `scope` or OAuth v1 `resource`.
- All state is in-memory and resets when the simulator restarts.
- The `api-version` query parameter is required on all ARM requests (the simulator validates its presence).
- Use `az rest` instead of high-level `az` commands to avoid cloud registration and subscription validation issues with HTTP endpoints.
