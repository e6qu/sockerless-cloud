# Using the Azure simulator with the Azure SDK for Python

## Prerequisites

- Python 3.8+
- Azure SDK packages installed (see per-service examples below)
- Simulator running on `http://localhost:4568`

## Setup

The Azure SDK for Python uses `azure-identity` for authentication and per-client `base_url` or `endpoint` overrides to point at custom endpoints.

```sh
pip install azure-identity azure-mgmt-resource azure-mgmt-containerinstance azure-mgmt-network azure-mgmt-storage
```

```python
from azure.identity import ClientSecretCredential
from azure.core.pipeline.policies import CustomHookPolicy

ENDPOINT = "http://localhost:4568"
SUBSCRIPTION_ID = "00000000-0000-0000-0000-000000000001"
TENANT_ID = "11111111-1111-1111-1111-111111111111"
RESOURCE_GROUP = "my-resource-group"

# The simulator accepts local-test credentials via its OAuth2 endpoint.
# Point the credential at the simulator's authority host.
credential = ClientSecretCredential(
    tenant_id=TENANT_ID,
    client_id="test-client-id",
    client_secret="test-client-secret",
    authority=ENDPOINT,
)
```

Most management clients accept a `base_url` parameter:

```python
from azure.mgmt.resource import ResourceManagementClient

client = ResourceManagementClient(
    credential=credential,
    subscription_id=SUBSCRIPTION_ID,
    base_url=ENDPOINT,
)
```

## Examples

### Resource Groups (`azure-mgmt-resource`)

```sh
pip install azure-mgmt-resource
```

```python
from azure.mgmt.resource import ResourceManagementClient

client = ResourceManagementClient(
    credential=credential,
    subscription_id=SUBSCRIPTION_ID,
    base_url=ENDPOINT,
)

# Create a resource group
rg = client.resource_groups.create_or_update(
    RESOURCE_GROUP,
    {"location": "eastus"},
)
print(rg.name)

# Get
rg = client.resource_groups.get(RESOURCE_GROUP)
print(rg.location)

# Delete
client.resource_groups.begin_delete(RESOURCE_GROUP).result()
```

### Networking (`azure-mgmt-network`)

```sh
pip install azure-mgmt-network
```

```python
from azure.mgmt.network import NetworkManagementClient

client = NetworkManagementClient(
    credential=credential,
    subscription_id=SUBSCRIPTION_ID,
    base_url=ENDPOINT,
)

# Create a VNet
vnet = client.virtual_networks.begin_create_or_update(
    RESOURCE_GROUP,
    "my-vnet",
    {
        "location": "eastus",
        "address_space": {"address_prefixes": ["10.0.0.0/16"]},
    },
).result()
print(vnet.name)

# Create a subnet
subnet = client.subnets.begin_create_or_update(
    RESOURCE_GROUP,
    "my-vnet",
    "my-subnet",
    {"address_prefix": "10.0.1.0/24"},
).result()
print(subnet.name)

# Create an NSG
nsg = client.network_security_groups.begin_create_or_update(
    RESOURCE_GROUP,
    "my-nsg",
    {"location": "eastus"},
).result()
print(nsg.name)

# Cleanup
client.subnets.begin_delete(RESOURCE_GROUP, "my-vnet", "my-subnet").result()
client.virtual_networks.begin_delete(RESOURCE_GROUP, "my-vnet").result()
client.network_security_groups.begin_delete(RESOURCE_GROUP, "my-nsg").result()
```

### Storage (`azure-mgmt-storage`)

```sh
pip install azure-mgmt-storage
```

```python
from azure.mgmt.storage import StorageManagementClient

client = StorageManagementClient(
    credential=credential,
    subscription_id=SUBSCRIPTION_ID,
    base_url=ENDPOINT,
)

# Create a storage account
account = client.storage_accounts.begin_create(
    RESOURCE_GROUP,
    "mystorageacct",
    {
        "location": "eastus",
        "kind": "StorageV2",
        "sku": {"name": "Standard_LRS"},
    },
).result()
print(account.name)

# List keys
keys = client.storage_accounts.list_keys(RESOURCE_GROUP, "mystorageacct")
print(keys.keys[0].value)

# Delete
client.storage_accounts.delete(RESOURCE_GROUP, "mystorageacct")
```

### Managed Identity (`azure-mgmt-msi`)

```sh
pip install azure-mgmt-msi
```

```python
from azure.mgmt.msi import ManagedServiceIdentityClient

client = ManagedServiceIdentityClient(
    credential=credential,
    subscription_id=SUBSCRIPTION_ID,
    base_url=ENDPOINT,
)

# Create a user-assigned identity
identity = client.user_assigned_identities.create_or_update(
    RESOURCE_GROUP,
    "my-identity",
    {"location": "eastus"},
)
print(identity.name)
print(identity.principal_id)

# Delete
client.user_assigned_identities.delete(RESOURCE_GROUP, "my-identity")
```

### Direct HTTP (for services without Python SDK wrappers)

For Container App Jobs, Azure Functions, ACR, DNS, and other services, use `requests` with direct ARM API calls:

```python
import requests

# Get a token from the simulator
token_resp = requests.post(
    f"{ENDPOINT}/{TENANT_ID}/oauth2/v2.0/token",
    data={
        "grant_type": "client_credentials",
        "client_id": "test-client-id",
        "client_secret": "test-client-secret",
        "scope": "https://management.azure.com/.default",
    },
)
token = token_resp.json()["access_token"]
headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}

# Create a Container App Job
resp = requests.put(
    f"{ENDPOINT}/subscriptions/{SUBSCRIPTION_ID}/resourceGroups/{RESOURCE_GROUP}"
    f"/providers/Microsoft.App/jobs/my-job?api-version=2023-05-01",
    headers=headers,
    json={
        "location": "eastus",
        "properties": {
            "configuration": {
                "triggerType": "Manual",
                "replicaTimeout": 300,
                "replicaRetryLimit": 0,
            },
            "template": {
                "containers": [{"name": "main", "image": "nginx:latest"}],
            },
        },
    },
)
print(resp.json())

# Create a Private DNS Zone
resp = requests.put(
    f"{ENDPOINT}/subscriptions/{SUBSCRIPTION_ID}/resourceGroups/{RESOURCE_GROUP}"
    f"/providers/Microsoft.Network/privateDnsZones/example.local?api-version=2018-09-01",
    headers=headers,
    json={"location": "global"},
)
print(resp.json()["name"])

# Create a public DNS Zone
resp = requests.put(
    f"{ENDPOINT}/subscriptions/{SUBSCRIPTION_ID}/resourceGroups/{RESOURCE_GROUP}"
    f"/providers/Microsoft.Network/dnsZones/example.com?api-version=2018-05-01",
    headers=headers,
    json={"location": "global"},
)
print(resp.json()["properties"]["zoneType"])

# Create a public A record
resp = requests.put(
    f"{ENDPOINT}/subscriptions/{SUBSCRIPTION_ID}/resourceGroups/{RESOURCE_GROUP}"
    f"/providers/Microsoft.Network/dnsZones/example.com/A/www?api-version=2018-05-01",
    headers=headers,
    json={"properties": {"TTL": 300, "ARecords": [{"ipv4Address": "203.0.113.10"}]}},
)
print(resp.json()["properties"]["fqdn"])

# Create a Function App
resp = requests.put(
    f"{ENDPOINT}/subscriptions/{SUBSCRIPTION_ID}/resourceGroups/{RESOURCE_GROUP}"
    f"/providers/Microsoft.Web/sites/my-funcapp?api-version=2022-09-01",
    headers=headers,
    json={
        "location": "eastus",
        "kind": "functionapp",
        "properties": {
            "siteConfig": {
                "appSettings": [
                    {"name": "FUNCTIONS_EXTENSION_VERSION", "value": "~4"},
                ],
            },
        },
    },
)
print(resp.json()["name"])
```

## Approach summary

| Service | Recommended Client | Package |
|---------|-------------------|---------|
| Resource Groups | `ResourceManagementClient` | `azure-mgmt-resource` |
| Networking | `NetworkManagementClient` | `azure-mgmt-network` |
| Storage | `StorageManagementClient` | `azure-mgmt-storage` |
| Managed Identity | `ManagedServiceIdentityClient` | `azure-mgmt-msi` |
| Container App Jobs | `requests` (direct HTTP) | — |
| Azure Functions | `requests` (direct HTTP) | — |
| Container Registry | `requests` (direct HTTP) | — |
| Private DNS | `requests` (direct HTTP) | — |
| Log Analytics | `requests` (direct HTTP) | — |
| App Insights | `requests` (direct HTTP) | — |
| Authorization | `requests` (direct HTTP) | — |

## Notes

- The simulator's OAuth2 endpoint accepts local-test credentials and returns token responses shaped for Azure SDK clients, including JWT `aud` claims derived from OAuth v2 `scope` or OAuth v1 `resource` requests.
- All state is in-memory and resets when the simulator restarts.
- The `api-version` query parameter is required on all ARM requests.
- Management clients that use `base_url` should work. Clients that hardcode `management.azure.com` may require patching or direct HTTP.
- The `authority` parameter on `ClientSecretCredential` must point to the simulator for the OAuth2 token flow to work.
