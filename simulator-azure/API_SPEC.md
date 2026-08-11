# Azure Simulator API Specification

> **Version:** 1.0.0
>
> **Date:** February 2026
>
> **Purpose:** Comprehensive REST API reference for implementing the Sockerless Azure simulator.
> All endpoint patterns, JSON schemas, and examples are derived from official Azure REST API documentation.

---

## Table of Contents

1. [General Conventions](#1-general-conventions)
2. [Container Apps Jobs (ARM API)](#2-container-apps-jobs-arm-api)
3. [Azure Monitor / Log Analytics](#3-azure-monitor--log-analytics)
4. [Azure Files (Storage Resource Provider)](#4-azure-files-storage-resource-provider)
5. [Azure Container Registry (ACR)](#5-azure-container-registry-acr)
6. [Private DNS Zones](#6-private-dns-zones)
7. [Azure Functions (App Service)](#7-azure-functions-app-service)
8. [Application Insights](#8-application-insights)

---

## 1. General Conventions

### 1.1 ARM Base URL

All Azure Resource Manager (ARM) APIs use the base URL:

```
https://management.azure.com
```

### 1.2 Authentication

All ARM requests require:

```
Authorization: Bearer <access-token>
Content-Type: application/json
```

Token is obtained via OAuth2 from `https://login.microsoftonline.com/{tenantId}/oauth2/v2.0/token` with scope `https://management.azure.com/.default`. The simulator also honors data-plane token audiences the same way Azure AD does: OAuth v2 `scope={resource}/.default` mints `aud={resource}`, OAuth v1 `resource={resource}` mints `aud={resource}`, and omitted audience fields default to ARM.

The simulator also serves the authority discovery endpoints an OpenID Connect client walks before it asks for a token:

| Endpoint | Purpose |
|----------|---------|
| `GET /common/discovery/instance?api-version={1.0\|1.1}&authorization_endpoint={url}` | Instance discovery. Answers whether an authority host belongs to this identity service and returns its `tenant_discovery_endpoint`; `api-version=1.1` adds the `metadata` alias table. Rejects an authority the simulator does not serve with `400 invalid_instance` (AADSTS50049), exactly one of `issuer`/`authorization_endpoint` with `AADSTS500502`, and a bad `api-version` with `AADSTS500501`. Served under `/common` only, as on Entra. |
| `GET /{tenantId}/v2.0/.well-known/openid-configuration` | Tenant OpenID metadata (v2.0 issuer). |
| `GET /{tenantId}/.well-known/openid-configuration` | Tenant OpenID metadata (v1 issuer). |
| `GET /{tenantId}/discovery/v2.0/keys` | JWKS for the RS256 key the simulator signs tokens with. |

MSAL-based clients (the Azure CLI, `azidentity`) send instance discovery to `login.microsoftonline.com` for any authority host outside their own hard-coded table, never to the configured authority. To point the Azure CLI at the simulator, register the Active Directory endpoint in the AD FS shape Azure Stack Hub deployments use — `az cloud register --endpoint-active-directory https://{host}/adfs` — which is the one authority shape MSAL resolves against the configured host.

### 1.3 Common ARM Request Headers

| Header | Required | Description |
|--------|----------|-------------|
| `Authorization` | Yes | `Bearer <token>` |
| `Content-Type` | Yes (for PUT/POST/PATCH) | `application/json` |
| `If-Match` | No | ETag for optimistic concurrency |
| `If-None-Match` | No | `*` to prevent overwrite |
| `x-ms-client-request-id` | No | Client-generated correlation GUID |

### 1.4 ARM Error Response Format

All ARM APIs use this standard error envelope:

```json
{
  "error": {
    "code": "ResourceNotFound",
    "message": "The Resource 'Microsoft.App/jobs/myjob' under resource group 'myrg' was not found.",
    "details": [
      {
        "code": "SubCode",
        "message": "More specific detail.",
        "target": "properties.configuration"
      }
    ],
    "innererror": "optional debug info string"
  }
}
```

**Common ARM Error Codes:**

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `InvalidAuthenticationToken` | 401 | Token invalid or expired |
| `AuthorizationFailed` | 403 | Insufficient permissions |
| `ResourceNotFound` | 404 | Resource does not exist |
| `ResourceGroupNotFound` | 404 | Resource group does not exist |
| `SubscriptionNotFound` | 404 | Subscription does not exist |
| `Conflict` | 409 | Resource already exists (with If-None-Match: *) |
| `InvalidRequestContent` | 400 | Malformed request body |
| `MissingSubscription` | 400 | Missing subscription in path |
| `InvalidApiVersionParameter` | 400 | Missing or unknown api-version value |
| `OperationNotAllowed` | 409 | Operation conflicts with current state |
| `ResourceProviderNotRegistered` | 409 | Provider not registered for subscription |

### 1.5 Async Operation Pattern (Long-Running Operations)

Many PUT/DELETE operations are asynchronous. The pattern is:

1. Client sends PUT/POST/DELETE
2. Server responds with `202 Accepted` and headers:
   ```
   Azure-AsyncOperation: https://management.azure.com/subscriptions/{sub}/providers/{provider}/operationStatuses/{opId}?api-version=...
   Location: https://management.azure.com/subscriptions/{sub}/providers/{provider}/operationResults/{opId}?api-version=...
   Retry-After: 15
   ```
3. Client polls `Azure-AsyncOperation` URL until terminal state:
   ```json
   {
     "id": "/subscriptions/{sub}/providers/{provider}/operationStatuses/{opId}",
     "name": "{opId}",
     "status": "Succeeded",
     "startTime": "2024-01-01T00:00:00Z",
     "endTime": "2024-01-01T00:00:30Z"
   }
   ```
   Terminal states: `Succeeded`, `Failed`, `Canceled`

4. For PUT: final GET on the resource URL returns the completed resource.
   For DELETE: `200 OK` or `204 No Content` on the original resource means done.

### 1.6 ARM Pagination

List operations use `nextLink` for pagination:

```json
{
  "value": [ ... ],
  "nextLink": "https://management.azure.com/...?$skiptoken=..."
}
```

Client follows `nextLink` until it is `null` or absent.

### 1.7 systemData Envelope

All ARM resources include read-only `systemData`:

```json
{
  "systemData": {
    "createdBy": "user@example.com",
    "createdByType": "User",
    "createdAt": "2024-01-01T00:00:00Z",
    "lastModifiedBy": "user@example.com",
    "lastModifiedByType": "User",
    "lastModifiedAt": "2024-01-01T00:00:30Z"
  }
}
```

`createdByType` enum: `User`, `Application`, `ManagedIdentity`, `Key`

---

## 2. Container Apps Jobs (ARM API)

**Provider:** `Microsoft.App`
**api-version:** `2024-03-01`

### 2.1 Jobs - Create or Update

**Endpoint:**
```
PUT https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/jobs/{jobName}?api-version=2024-03-01
```

**URI Parameters:**

| Name | In | Required | Type | Constraints |
|------|-----|----------|------|-------------|
| subscriptionId | path | Yes | string | minLength: 1 |
| resourceGroupName | path | Yes | string | minLength: 1, maxLength: 90 |
| jobName | path | Yes | string | pattern: `^[-\w\.\_\(\)]+$` |
| api-version | query | Yes | string | `2024-03-01` |

**Request Body (Job):**

```json
{
  "location": "East US",
  "tags": {
    "key1": "value1"
  },
  "identity": {
    "type": "SystemAssigned|UserAssigned|SystemAssigned,UserAssigned|None",
    "userAssignedIdentities": {
      "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.ManagedIdentity/userAssignedIdentities/{name}": {}
    }
  },
  "properties": {
    "environmentId": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/managedEnvironments/{envName}",
    "workloadProfileName": "Consumption",
    "configuration": {
      "triggerType": "Manual",
      "replicaTimeout": 1800,
      "replicaRetryLimit": 3,
      "manualTriggerConfig": {
        "parallelism": 1,
        "replicaCompletionCount": 1
      },
      "scheduleTriggerConfig": {
        "cronExpression": "*/5 * * * *",
        "parallelism": 1,
        "replicaCompletionCount": 1
      },
      "eventTriggerConfig": {
        "parallelism": 1,
        "replicaCompletionCount": 1,
        "scale": {
          "pollingInterval": 30,
          "minExecutions": 0,
          "maxExecutions": 100,
          "rules": [
            {
              "name": "rulename",
              "type": "azure-servicebus",
              "metadata": { "topicName": "my-topic" },
              "auth": [
                { "secretRef": "connection-string", "triggerParameter": "connection" }
              ]
            }
          ]
        }
      },
      "registries": [
        {
          "server": "myregistry.azurecr.io",
          "username": "admin",
          "passwordSecretRef": "registry-password",
          "identity": "system"
        }
      ],
      "secrets": [
        {
          "name": "registry-password",
          "value": "secret-value",
          "identity": "system",
          "keyVaultUrl": "https://myvault.vault.azure.net/secrets/mysecret"
        }
      ]
    },
    "template": {
      "containers": [
        {
          "name": "main",
          "image": "myregistry.azurecr.io/myapp:latest",
          "command": ["/bin/sh"],
          "args": ["-c", "echo hello"],
          "env": [
            { "name": "ENV_VAR", "value": "plain-value" },
            { "name": "SECRET_VAR", "secretRef": "registry-password" }
          ],
          "resources": {
            "cpu": 0.5,
            "memory": "1Gi",
            "ephemeralStorage": "2Gi"
          },
          "volumeMounts": [
            {
              "volumeName": "azure-files-vol",
              "mountPath": "/mnt/data",
              "subPath": ""
            }
          ],
          "probes": [
            {
              "type": "Liveness",
              "httpGet": {
                "path": "/health",
                "port": 8080,
                "scheme": "HTTP",
                "host": "",
                "httpHeaders": [
                  { "name": "Custom-Header", "value": "Awesome" }
                ]
              },
              "tcpSocket": { "host": "", "port": 8080 },
              "initialDelaySeconds": 5,
              "periodSeconds": 10,
              "timeoutSeconds": 1,
              "failureThreshold": 3,
              "successThreshold": 1,
              "terminationGracePeriodSeconds": 30
            }
          ]
        }
      ],
      "initContainers": [
        {
          "name": "init",
          "image": "busybox:latest",
          "command": ["/bin/sh"],
          "args": ["-c", "echo init"],
          "env": [],
          "resources": { "cpu": 0.25, "memory": "0.5Gi" },
          "volumeMounts": []
        }
      ],
      "volumes": [
        {
          "name": "azure-files-vol",
          "storageType": "AzureFile",
          "storageName": "my-storage-resource",
          "mountOptions": ""
        },
        {
          "name": "empty-vol",
          "storageType": "EmptyDir"
        },
        {
          "name": "secret-vol",
          "storageType": "Secret",
          "secrets": [
            { "secretRef": "registry-password", "path": "password.txt" }
          ]
        }
      ]
    }
  }
}
```

**Required fields:** `location`
**StorageType enum:** `AzureFile`, `EmptyDir`, `Secret`
**TriggerType enum:** `Manual`, `Schedule`, `Event`
**Probe Type enum:** `Liveness`, `Readiness`, `Startup`
**Scheme enum:** `HTTP`, `HTTPS`

**Response (200 OK / 201 Created) -- Job object:**

```json
{
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/jobs/{jobName}",
  "name": "{jobName}",
  "type": "Microsoft.App/jobs",
  "location": "East US",
  "tags": {},
  "systemData": { "...": "..." },
  "identity": { "...": "..." },
  "properties": {
    "provisioningState": "InProgress",
    "environmentId": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/managedEnvironments/{envName}",
    "workloadProfileName": "Consumption",
    "outboundIpAddresses": ["20.1.2.3"],
    "eventStreamEndpoint": "https://...",
    "configuration": { "...same as request..." },
    "template": { "...same as request..." }
  }
}
```

**provisioningState enum:** `InProgress`, `Succeeded`, `Failed`, `Canceled`, `Deleting`

**201 response includes header:**
```
Location: https://management.azure.com/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/jobs/{jobName}/operationResults/{opId}?api-version=2024-03-01
```

### 2.2 Jobs - Get

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/jobs/{jobName}?api-version=2024-03-01
```

**Response (200 OK):** Same `Job` object as above with `provisioningState: "Succeeded"`.

### 2.3 Jobs - List By Resource Group

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/jobs?api-version=2024-03-01
```

**Response (200 OK):**

```json
{
  "value": [
    { "...Job object..." }
  ],
  "nextLink": null
}
```

### 2.4 Jobs - Delete

```
DELETE https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/jobs/{jobName}?api-version=2024-03-01
```

**Responses:**

| Status | Description | Headers |
|--------|-------------|---------|
| 200 OK | Deleted successfully | (empty body) |
| 202 Accepted | Delete in progress | `azure-asyncoperation`, `Location` |
| 204 No Content | Resource does not exist | (empty body) |

**202 Headers example:**
```
azure-asyncoperation: https://management.azure.com/subscriptions/{sub}/providers/Microsoft.App/jobs/{jobName}/operationResults/{opId}?api-version=2024-03-01
```

### 2.5 Jobs - Start (Create Execution)

```
POST https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/jobs/{jobName}/start?api-version=2024-03-01
```

**Request Body (optional -- overrides job template):**

```json
{
  "containers": [
    {
      "name": "main",
      "image": "myregistry.azurecr.io/myapp:v2",
      "command": ["/bin/sh"],
      "args": ["-c", "echo override"],
      "env": [
        { "name": "ENV_VAR", "value": "override-value" }
      ],
      "resources": {
        "cpu": 0.5,
        "memory": "1Gi"
      }
    }
  ],
  "initContainers": []
}
```

Note: The start request body uses `JobExecutionContainer` (no probes or volumeMounts), not the full `Container` type.

**Response (200 OK) -- JobExecutionBase:**

```json
{
  "name": "testcontainerappsjob0-pjxhsye",
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/jobs/{jobName}/executions/{executionName}"
}
```

**Response (202 Accepted):**
```
Location: https://management.azure.com/...
```

### 2.6 Job Executions - List

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/jobs/{jobName}/executions?api-version=2024-03-01
```

**Optional query parameter:** `$filter` (string)

**Response (200 OK) -- ContainerAppJobExecutions:**

```json
{
  "value": [
    {
      "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/jobs/{jobName}/executions/{executionName}",
      "name": "testcontainerAppJob-27944454",
      "type": "Microsoft.App/jobs/executions",
      "properties": {
        "status": "Running",
        "startTime": "2023-02-13T20:37:30+00:00",
        "endTime": "2023-02-13T20:47:30+00:00",
        "template": {
          "containers": [
            {
              "name": "testcontainerappsjob0",
              "image": "repo/testcontainerappsjob0:v4",
              "resources": { "cpu": 0.5, "memory": "1Gi" }
            }
          ],
          "initContainers": []
        }
      }
    }
  ],
  "nextLink": null
}
```

**JobExecutionRunningState enum:** `Running`, `Processing`, `Stopped`, `Degraded`, `Failed`, `Unknown`, `Succeeded`

### 2.7 Managed Environments - Get (Read-Only, Pre-Seeded)

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/managedEnvironments/{environmentName}?api-version=2024-03-01
```

**Response (200 OK) -- ManagedEnvironment:**

```json
{
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/managedEnvironments/{envName}",
  "name": "{envName}",
  "type": "Microsoft.App/managedEnvironments",
  "location": "East US",
  "tags": {},
  "properties": {
    "provisioningState": "Succeeded",
    "defaultDomain": "{envName}.k4apps.io",
    "staticIp": "20.42.33.145",
    "zoneRedundant": false,
    "deploymentErrors": null,
    "eventStreamEndpoint": "https://...",
    "infrastructureResourceGroup": "capp-svc-{envName}-eastus",
    "vnetConfiguration": {
      "infrastructureSubnetId": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/virtualNetworks/{vnet}/subnets/{subnet}",
      "internal": false,
      "dockerBridgeCidr": "10.1.0.1/16",
      "platformReservedCidr": "10.0.0.0/16",
      "platformReservedDnsIP": "10.0.0.2"
    },
    "appLogsConfiguration": {
      "destination": "log-analytics",
      "logAnalyticsConfiguration": {
        "customerId": "workspace-guid"
      }
    },
    "customDomainConfiguration": {
      "customDomainVerificationId": "...",
      "dnsSuffix": "www.example.com",
      "subjectName": "CN=www.example.com",
      "expirationDate": "2025-12-31T00:00:00Z",
      "thumbprint": "..."
    },
    "workloadProfiles": [
      {
        "name": "Consumption",
        "workloadProfileType": "Consumption"
      }
    ],
    "kedaConfiguration": { "version": "2.12" },
    "daprConfiguration": { "version": "1.12" },
    "peerAuthentication": { "mtls": { "enabled": true } },
    "peerTrafficConfiguration": { "encryption": { "enabled": true } }
  }
}
```

**EnvironmentProvisioningState enum:** `Succeeded`, `Failed`, `Canceled`, `Waiting`, `InitializationInProgress`, `InfrastructureSetupInProgress`, `InfrastructureSetupComplete`, `ScheduledForDelete`, `UpgradeRequested`, `UpgradeFailed`

---

## 3. Azure Monitor / Log Analytics

### 3.1 Log Analytics Query API

**Base URL:** `https://api.loganalytics.azure.com` (data-plane, NOT ARM)
**api-version:** `v1` (path-based, not query parameter)

**Authentication:**
```
Authorization: Bearer <access-token>
```
Scope: `https://api.loganalytics.io/.default`

#### POST Query

```
POST https://api.loganalytics.azure.com/v1/workspaces/{workspaceId}/query
Content-Type: application/json
Authorization: Bearer <token>
```

**Request Body:**

```json
{
  "query": "ContainerAppConsoleLogs_CL | where ContainerGroupName_s == 'myjob' | project TimeGenerated, Log_s | order by TimeGenerated desc | take 100",
  "timespan": "PT24H",
  "workspaces": [
    "workspace-guid-1",
    "workspace-guid-2"
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | Yes | KQL query string |
| `timespan` | string | No | ISO 8601 duration (e.g., `PT1H`, `P1D`, `PT24H`) or interval `start/end` |
| `workspaces` | string[] | No | Additional workspace IDs to query across |

#### GET Query (alternative)

```
GET https://api.loganalytics.azure.com/v1/workspaces/{workspaceId}/query?query=AzureActivity%20|%20take%2010&timespan=PT1H
Authorization: Bearer <token>
```

**Response (200 OK):**

```json
{
  "tables": [
    {
      "name": "PrimaryResult",
      "columns": [
        { "name": "TimeGenerated", "type": "datetime" },
        { "name": "Log_s", "type": "string" },
        { "name": "count_", "type": "long" }
      ],
      "rows": [
        ["2024-01-15T10:30:00Z", "Hello from container", 1],
        ["2024-01-15T10:30:01Z", "Processing request", 2]
      ]
    }
  ]
}
```

**Column type values:** `string`, `long`, `int`, `real`, `datetime`, `bool`, `guid`, `timespan`, `dynamic`, `decimal`

**Error Response (400/403/etc):**

```json
{
  "error": {
    "code": "BadArgumentError",
    "message": "The query syntax is invalid.",
    "innererror": {
      "code": "SyntaxError",
      "message": "Query could not be parsed at 'invalid' on line [1,12]"
    }
  }
}
```

**Partial Error Response (200 OK with error):**

```json
{
  "tables": [ { "...partial results..." } ],
  "error": {
    "code": "PartialError",
    "message": "Some workspaces were not reachable.",
    "details": [
      {
        "code": "WorkspaceNotFound",
        "message": "Workspace xyz was not found."
      }
    ]
  }
}
```

**Common Log Analytics Error Codes:**

| Code | Description |
|------|-------------|
| `BadArgumentError` | Invalid query syntax or parameters |
| `PathNotFoundError` | Invalid workspace ID |
| `InsufficientAccessError` | No permission to query workspace |
| `WorkspaceNotFoundError` | Workspace does not exist |

### 3.2 Log Ingestion (for Writing Logs)

**Base URL:** `https://{dce-endpoint}.ingest.monitor.azure.com`
**api-version:** `2023-01-01`

```
POST https://{dce-endpoint}.ingest.monitor.azure.com/dataCollectionRules/{dcrImmutableId}/streams/{streamName}?api-version=2023-01-01
Content-Type: application/json
Authorization: Bearer <token>
```

**Request Body (array of log entries):**

```json
[
  {
    "TimeGenerated": "2024-01-15T10:30:00Z",
    "ContainerGroupName_s": "myjob-abc123",
    "Log_s": "Hello from container",
    "Stream_s": "stdout"
  }
]
```

**Response:** `204 No Content` on success.

**Scope:** `https://monitor.azure.com/.default`

---

## 4. Azure Files (Storage Resource Provider)

**Provider:** `Microsoft.Storage`
**api-version:** `2024-01-01`

### 4.1 Storage Accounts - Get Properties

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}?api-version=2024-01-01
```

**Response (200 OK):**

```json
{
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Storage/storageAccounts/{accountName}",
  "name": "{accountName}",
  "type": "Microsoft.Storage/storageAccounts",
  "location": "eastus",
  "kind": "StorageV2",
  "sku": {
    "name": "Standard_LRS",
    "tier": "Standard"
  },
  "properties": {
    "provisioningState": "Succeeded",
    "primaryEndpoints": {
      "file": "https://{accountName}.file.core.windows.net/",
      "blob": "https://{accountName}.blob.core.windows.net/",
      "table": "https://{accountName}.table.core.windows.net/",
      "queue": "https://{accountName}.queue.core.windows.net/"
    },
    "primaryLocation": "eastus",
    "statusOfPrimary": "available",
    "creationTime": "2024-01-01T00:00:00Z",
    "encryption": {
      "services": {
        "file": { "enabled": true, "lastEnabledTime": "2024-01-01T00:00:00Z" },
        "blob": { "enabled": true, "lastEnabledTime": "2024-01-01T00:00:00Z" }
      },
      "keySource": "Microsoft.Storage"
    }
  }
}
```

### 4.2 File Shares - Create

```
PUT https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/fileServices/default/shares/{shareName}?api-version=2024-01-01
```

**URI Parameters:**

| Name | In | Required | Type | Constraints |
|------|-----|----------|------|-------------|
| accountName | path | Yes | string | 3-24 chars, lowercase alphanumeric |
| shareName | path | Yes | string | 3-63 chars, lowercase alphanumeric and dash |
| subscriptionId | path | Yes | string | |
| resourceGroupName | path | Yes | string | 1-90 chars |

**Request Body:**

```json
{
  "properties": {
    "shareQuota": 1024,
    "accessTier": "TransactionOptimized",
    "enabledProtocols": "SMB",
    "metadata": {
      "key1": "value1"
    }
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `properties.shareQuota` | int32 | Size in GiB (max 5120 or 102400 for large shares) |
| `properties.accessTier` | enum | `TransactionOptimized`, `Hot`, `Cool`, `Premium` |
| `properties.enabledProtocols` | enum | `SMB` or `NFS` (set at creation only) |
| `properties.metadata` | object | Key-value metadata |
| `properties.rootSquash` | enum | `NoRootSquash`, `RootSquash`, `AllSquash` (NFS only) |

**Response (200 OK / 201 Created) -- FileShare:**

```json
{
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Storage/storageAccounts/{account}/fileServices/default/shares/{shareName}",
  "name": "{shareName}",
  "type": "Microsoft.Storage/storageAccounts/fileServices/shares",
  "etag": "\"0x8D592D74CC20EBA\"",
  "properties": {
    "shareQuota": 1024,
    "accessTier": "TransactionOptimized",
    "enabledProtocols": "SMB",
    "lastModifiedTime": "2024-01-15T10:30:00Z",
    "leaseStatus": "Unlocked",
    "leaseState": "Available",
    "metadata": {}
  }
}
```

**Storage error format (different from ARM):**

```json
{
  "error": {
    "code": "AccountAlreadyExists",
    "message": "The specified account already exists.",
    "details": [],
    "target": "accountName"
  }
}
```

### 4.3 File Shares - Get

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/fileServices/default/shares/{shareName}?api-version=2024-01-01
```

Optional query: `$expand=stats` (to include `shareUsageBytes`)

**Response (200 OK):** Same `FileShare` object. With `$expand=stats`:

```json
{
  "id": "...",
  "name": "share1634",
  "type": "Microsoft.Storage/storageAccounts/fileServices/shares",
  "etag": "\"0x8D592D74CC20EBA\"",
  "properties": {
    "lastModifiedTime": "2024-01-15T10:30:00Z",
    "shareQuota": 1024,
    "shareUsageBytes": 652945
  }
}
```

### 4.4 File Shares - List

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/fileServices/default/shares?api-version=2024-01-01
```

Optional query: `$maxpagesize`, `$filter`, `$expand=deleted`

**Response (200 OK):**

```json
{
  "value": [
    { "...FileShare object..." }
  ],
  "nextLink": null
}
```

### 4.5 File Shares - Delete

```
DELETE https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/fileServices/default/shares/{shareName}?api-version=2024-01-01
```

Optional header: `x-ms-snapshot` (to delete a specific snapshot)

**Responses:** `200 OK` (success), `204 No Content` (not found), error.

### 4.6 Azure Files Data Plane (File Operations)

The data-plane APIs for file upload/download use a different base URL:

**Base URL:** `https://{accountName}.file.core.windows.net`
**Authentication:** `Authorization: SharedKey {accountName}:{signature}` or SAS token

#### Create File (PUT)

```
PUT https://{accountName}.file.core.windows.net/{shareName}/{directoryPath}/{fileName}
x-ms-type: file
x-ms-content-length: {fileSize}
x-ms-version: 2024-08-04
```

Then upload content with PUT Range:
```
PUT https://{accountName}.file.core.windows.net/{shareName}/{directoryPath}/{fileName}?comp=range
x-ms-range: bytes=0-{fileSize-1}
x-ms-write: update
Content-Length: {fileSize}
x-ms-version: 2024-08-04

<binary content>
```

#### Download File (GET)

```
GET https://{accountName}.file.core.windows.net/{shareName}/{directoryPath}/{fileName}
x-ms-version: 2024-08-04
```

#### Delete File

```
DELETE https://{accountName}.file.core.windows.net/{shareName}/{directoryPath}/{fileName}
x-ms-version: 2024-08-04
```

---

## 5. Azure Container Registry (ACR)

ACR implements the OCI Distribution Specification plus Azure-specific extensions.

### 5.1 Base URL and Authentication

**Registry URL:** `https://{registryName}.azurecr.io`

**OAuth2 Token Exchange (ACR-specific):**

```
POST https://{registryName}.azurecr.io/oauth2/exchange
Content-Type: application/x-www-form-urlencoded

grant_type=access_token&service={registryName}.azurecr.io&access_token={aad_token}
```

**Response:**
```json
{
  "refresh_token": "eyJ..."
}
```

**Get Scope Token:**

```
POST https://{registryName}.azurecr.io/oauth2/token
Content-Type: application/x-www-form-urlencoded

grant_type=refresh_token&service={registryName}.azurecr.io&scope=repository:{name}:pull,push&refresh_token={refresh_token}
```

**Response:**
```json
{
  "access_token": "eyJ..."
}
```

Use the `access_token` as: `Authorization: Bearer {access_token}`

**Alternatively, Basic auth with admin credentials:**
```
Authorization: Basic base64({username}:{password})
```

### 5.2 Admin Credentials - List Credentials (ARM)

```
POST https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerRegistry/registries/{registryName}/listCredentials?api-version=2023-07-01
```

**Response:**

```json
{
  "username": "{registryName}",
  "passwords": [
    { "name": "password", "value": "..." },
    { "name": "password2", "value": "..." }
  ]
}
```

### 5.3 OCI Distribution API - Version Check

```
GET https://{registryName}.azurecr.io/v2/
```

**Response (200 OK):** `{}` (empty JSON or `Docker-Distribution-API-Version: registry/2.0` header)
**Response (401 Unauthorized):** Includes `Www-Authenticate` header with realm and service info:
```
Www-Authenticate: Bearer realm="https://{registryName}.azurecr.io/oauth2/token",service="{registryName}.azurecr.io"
```

### 5.4 OCI Distribution API - Manifests

**ACR api-version:** `2021-07-01` (optional query param, ACR-specific)

#### GET Manifest

```
GET https://{registryName}.azurecr.io/v2/{name}/manifests/{reference}
Accept: application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.index.v1+json
```

`{reference}` can be a tag (e.g., `latest`) or a digest (e.g., `sha256:abc123...`).

**Response (200 OK):**

```
Content-Type: application/vnd.docker.distribution.manifest.v2+json
Docker-Content-Digest: sha256:abc123...
```

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
  "config": {
    "mediaType": "application/vnd.docker.container.image.v1+json",
    "size": 7023,
    "digest": "sha256:config-digest..."
  },
  "layers": [
    {
      "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
      "size": 32654,
      "digest": "sha256:layer1-digest..."
    }
  ]
}
```

#### HEAD Manifest (check existence)

```
HEAD https://{registryName}.azurecr.io/v2/{name}/manifests/{reference}
Accept: application/vnd.docker.distribution.manifest.v2+json
```

**Response (200 OK):** Headers only (`Docker-Content-Digest`, `Content-Length`, `Content-Type`)

#### PUT Manifest

```
PUT https://{registryName}.azurecr.io/v2/{name}/manifests/{reference}
Content-Type: application/vnd.docker.distribution.manifest.v2+json
```

Body: the manifest JSON.

**Response (201 Created):**
```
Docker-Content-Digest: sha256:abc123...
Location: /v2/{name}/manifests/sha256:abc123...
Content-Length: 0
```

#### DELETE Manifest

```
DELETE https://{registryName}.azurecr.io/v2/{name}/manifests/{digest}
```

**Response:** `202 Accepted`

### 5.5 OCI Distribution API - Blobs

#### GET Blob

```
GET https://{registryName}.azurecr.io/v2/{name}/blobs/{digest}
```

**Response (200 OK):** Binary content of the blob.
Headers: `Content-Length`, `Docker-Content-Digest`, `Content-Type: application/octet-stream`

#### HEAD Blob (check existence)

```
HEAD https://{registryName}.azurecr.io/v2/{name}/blobs/{digest}
```

**Response (200 OK):** Headers only.

#### DELETE Blob

```
DELETE https://{registryName}.azurecr.io/v2/{name}/blobs/{digest}
```

**Response:** `202 Accepted`

#### POST Blob Upload (initiate)

```
POST https://{registryName}.azurecr.io/v2/{name}/blobs/uploads/
```

**Response (202 Accepted):**
```
Location: /v2/{name}/blobs/uploads/{uuid}
Docker-Upload-UUID: {uuid}
Range: 0-0
```

#### PATCH Blob Upload (chunked)

```
PATCH https://{registryName}.azurecr.io/v2/{name}/blobs/uploads/{uuid}
Content-Type: application/octet-stream
Content-Range: {start}-{end}
Content-Length: {size}

<binary chunk data>
```

**Response (202 Accepted):**
```
Location: /v2/{name}/blobs/uploads/{uuid}
Docker-Upload-UUID: {uuid}
Range: 0-{end}
```

#### PUT Blob Upload (complete / monolithic)

Monolithic upload (single PUT with digest):

```
PUT https://{registryName}.azurecr.io/v2/{name}/blobs/uploads/{uuid}?digest={digest}
Content-Type: application/octet-stream
Content-Length: {size}

<binary blob data>
```

Complete chunked upload:

```
PUT https://{registryName}.azurecr.io/v2/{name}/blobs/uploads/{uuid}?digest={digest}
Content-Length: 0
```

**Response (201 Created):**
```
Docker-Content-Digest: {digest}
Location: /v2/{name}/blobs/{digest}
Content-Length: 0
```

### 5.6 OCI Distribution API - Tags

#### List Tags

```
GET https://{registryName}.azurecr.io/v2/{name}/tags/list
```

Optional query: `?n={count}&last={lastTag}` for pagination.

**Response (200 OK):**

```json
{
  "name": "myrepo/myimage",
  "tags": ["latest", "v1", "v2", "v3"]
}
```

With pagination, response includes `Link` header:
```
Link: </v2/{name}/tags/list?last=v3&n=10>; rel="next"
```

### 5.7 ACR Error Format

```json
{
  "errors": [
    {
      "code": "MANIFEST_UNKNOWN",
      "message": "manifest tagged by \"v999\" is not found",
      "detail": {}
    }
  ]
}
```

**Common OCI/ACR error codes:**

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `BLOB_UNKNOWN` | 404 | Blob unknown to registry |
| `BLOB_UPLOAD_INVALID` | 400 | Blob upload invalid |
| `BLOB_UPLOAD_UNKNOWN` | 404 | Blob upload unknown |
| `DIGEST_INVALID` | 400 | Provided digest did not match content |
| `MANIFEST_BLOB_UNKNOWN` | 404 | Blob in manifest unknown |
| `MANIFEST_INVALID` | 400 | Manifest invalid |
| `MANIFEST_UNKNOWN` | 404 | Manifest unknown |
| `NAME_INVALID` | 400 | Invalid repository name |
| `NAME_UNKNOWN` | 404 | Repository name not known |
| `TAG_INVALID` | 400 | Tag invalid |
| `UNAUTHORIZED` | 401 | Authentication required |
| `DENIED` | 403 | Access denied |
| `UNSUPPORTED` | 415 | Operation unsupported |
| `TOOMANYREQUESTS` | 429 | Too many requests |

---

## 6. Private DNS Zones

**Provider:** `Microsoft.Network`
**api-version:** `2018-09-01`

### 6.1 Private Zones - Create or Update

```
PUT https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}?api-version=2018-09-01
```

**Request Headers (optional):**

| Header | Description |
|--------|-------------|
| `If-Match` | ETag to prevent concurrent overwrites |
| `If-None-Match` | `*` to create-only (fail if exists) |

**Request Body:**

```json
{
  "location": "Global",
  "tags": {
    "key1": "value1"
  }
}
```

Note: `location` MUST be `"Global"` for Private DNS zones.

**Response (200 OK / 201 Created) -- PrivateZone:**

```json
{
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/privateDnsZones/{zoneName}",
  "name": "{zoneName}",
  "type": "Microsoft.Network/privateDnsZones",
  "location": "global",
  "etag": "00000000-0000-0000-0000-000000000000",
  "tags": {},
  "properties": {
    "maxNumberOfRecordSets": 25000,
    "numberOfRecordSets": 1,
    "maxNumberOfVirtualNetworkLinks": 1000,
    "numberOfVirtualNetworkLinks": 0,
    "maxNumberOfVirtualNetworkLinksWithRegistration": 100,
    "numberOfVirtualNetworkLinksWithRegistration": 0,
    "provisioningState": "Succeeded"
  }
}
```

**202 Accepted response includes:**
```
Location: https://management.azure.com/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/privateDnsOperationResults/{opId}?api-version=2018-09-01
Azure-AsyncOperation: https://management.azure.com/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/privateDnsOperationStatuses/{opId}?api-version=2018-09-01
Retry-After: 60
```

**ProvisioningState enum:** `Creating`, `Updating`, `Deleting`, `Succeeded`, `Failed`, `Canceled`

### 6.2 Private Zones - Get

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}?api-version=2018-09-01
```

**Response (200 OK):** Same `PrivateZone` object.

### 6.3 Private Zones - List By Resource Group

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones?api-version=2018-09-01
```

Optional query: `$top` (int)

### 6.4 Private Zones - Delete

```
DELETE https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}?api-version=2018-09-01
```

**Responses:** `200 OK`, `202 Accepted` (async), `204 No Content`

### 6.5 Record Sets - Create or Update

```
PUT https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/{recordType}/{relativeRecordSetName}?api-version=2018-09-01
```

`{recordType}` values: `A`, `AAAA`, `CNAME`, `MX`, `PTR`, `SOA`, `SRV`, `TXT`

**Request Body (example for A record):**

```json
{
  "properties": {
    "ttl": 300,
    "aRecords": [
      { "ipv4Address": "10.0.0.1" },
      { "ipv4Address": "10.0.0.2" }
    ],
    "metadata": {
      "key1": "value1"
    }
  }
}
```

**Record type fields:**

| Record Type | Property | Sub-fields |
|-------------|----------|------------|
| A | `aRecords` | `ipv4Address` |
| AAAA | `aaaaRecords` | `ipv6Address` |
| CNAME | `cnameRecord` | `cname` (singular, not array) |
| MX | `mxRecords` | `preference`, `exchange` |
| PTR | `ptrRecords` | `ptrdname` |
| SOA | `soaRecord` | `host`, `email`, `serialNumber`, `refreshTime`, `retryTime`, `expireTime`, `minimumTtl` |
| SRV | `srvRecords` | `priority`, `weight`, `port`, `target` |
| TXT | `txtRecords` | `value` (string[]) |

**Response (200 OK / 201 Created) -- RecordSet:**

```json
{
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/privateDnsZones/{zone}/A/{name}",
  "name": "{name}",
  "type": "Microsoft.Network/privateDnsZones/A",
  "etag": "...",
  "properties": {
    "ttl": 300,
    "fqdn": "{name}.{zone}.",
    "isAutoRegistered": false,
    "aRecords": [
      { "ipv4Address": "10.0.0.1" },
      { "ipv4Address": "10.0.0.2" }
    ],
    "metadata": {}
  }
}
```

### 6.6 Record Sets - Get

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/{recordType}/{relativeRecordSetName}?api-version=2018-09-01
```

### 6.7 Record Sets - List By Type

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/{recordType}?api-version=2018-09-01
```

### 6.8 Record Sets - List All

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/all?api-version=2018-09-01
```

### 6.9 Record Sets - Delete

```
DELETE https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/{recordType}/{relativeRecordSetName}?api-version=2018-09-01
```

**Responses:** `200 OK`, `204 No Content`

### 6.10 Virtual Network Links - Create or Update

```
PUT https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/virtualNetworkLinks/{virtualNetworkLinkName}?api-version=2018-09-01
```

**Request Body:**

```json
{
  "location": "Global",
  "tags": {},
  "properties": {
    "virtualNetwork": {
      "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/virtualNetworks/{vnetName}"
    },
    "registrationEnabled": false
  }
}
```

**Response (200 OK / 201 Created / 202 Accepted):**

```json
{
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/privateDnsZones/{zone}/virtualNetworkLinks/{linkName}",
  "name": "{linkName}",
  "type": "Microsoft.Network/privateDnsZones/virtualNetworkLinks",
  "location": "global",
  "etag": "...",
  "properties": {
    "virtualNetwork": {
      "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Network/virtualNetworks/{vnetName}"
    },
    "registrationEnabled": false,
    "virtualNetworkLinkState": "Completed",
    "provisioningState": "Succeeded"
  }
}
```

**virtualNetworkLinkState enum:** `InProgress`, `Completed`

### 6.11 Virtual Network Links - Get

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/virtualNetworkLinks/{virtualNetworkLinkName}?api-version=2018-09-01
```

### 6.12 Virtual Network Links - List

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/virtualNetworkLinks?api-version=2018-09-01
```

Returns `{"value":[...]}` with an empty array when no virtual network links exist for the zone.

### 6.13 Virtual Network Links - Delete

```
DELETE https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/privateDnsZones/{privateZoneName}/virtualNetworkLinks/{virtualNetworkLinkName}?api-version=2018-09-01
```

**Responses:** `200 OK`, `202 Accepted`, `204 No Content`

---

## 6.5 Public DNS Zones

**Provider:** `Microsoft.Network`
**api-version:** `2018-05-01`

### 6.5.1 Zones - Create or Update

```
PUT https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}?api-version=2018-05-01
```

Creates or updates a public DNS zone. The simulator returns Azure-shaped public zone properties, including `zoneType: "Public"`, generated Azure DNS name servers, `maxNumberOfRecordSets`, `maxNumberOfRecordsPerRecordSet`, and `numberOfRecordSets`. New zones create default `NS/@` and `SOA/@` record sets.

### 6.5.2 Zones - Get/List/Delete

```
GET    https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}?api-version=2018-05-01
GET    https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones?api-version=2018-05-01
DELETE https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}?api-version=2018-05-01
```

Deleting a zone deletes its record sets.

### 6.5.3 Record Sets

```
PUT    https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}/{recordType}/{relativeRecordSetName}?api-version=2018-05-01
GET    https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}/{recordType}/{relativeRecordSetName}?api-version=2018-05-01
GET    https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}/{recordType}?api-version=2018-05-01
GET    https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}/recordsets?api-version=2018-05-01
GET    https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}/all?api-version=2018-05-01
DELETE https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/dnsZones/{zoneName}/{recordType}/{relativeRecordSetName}?api-version=2018-05-01
```

Supported record types: `A`, `AAAA`, `CAA`, `CNAME`, `MX`, `NS`, `PTR`, `SOA`, `SRV`, and `TXT`. Public DNS record-set JSON follows Azure DNS casing such as `TTL`, `ARecords`, `AAAARecords`, `CNAMERecord`, `MXRecords`, `NSRecords`, `SOARecord`, `SRVRecords`, and `TXTRecords`.

---

## 7. Azure Functions (App Service)

**Provider:** `Microsoft.Web`
**api-version:** `2024-04-01`

### 7.1 Function Apps - Create or Update

```
PUT https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}?api-version=2024-04-01
```

**Request Body:**

```json
{
  "location": "East US",
  "kind": "functionapp,linux",
  "tags": {},
  "properties": {
    "serverFarmId": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Web/serverfarms/{planName}",
    "reserved": true,
    "siteConfig": {
      "appSettings": [
        { "name": "FUNCTIONS_WORKER_RUNTIME", "value": "node" },
        { "name": "FUNCTIONS_EXTENSION_VERSION", "value": "~4" },
        { "name": "AzureWebJobsStorage", "value": "DefaultEndpointsProtocol=https;AccountName=...;AccountKey=...;EndpointSuffix=core.windows.net" },
        { "name": "WEBSITE_CONTENTAZUREFILECONNECTIONSTRING", "value": "..." },
        { "name": "WEBSITE_CONTENTSHARE", "value": "funcapp-content" }
      ],
      "linuxFxVersion": "Node|18",
      "ftpsState": "Disabled"
    },
    "httpsOnly": true
  }
}
```

**`kind` values for functions:** `functionapp`, `functionapp,linux`, `functionapp,linux,container`

**Response (200 OK) -- Site:**

```json
{
  "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Web/sites/{name}",
  "name": "{name}",
  "type": "Microsoft.Web/sites",
  "kind": "functionapp,linux",
  "location": "East US",
  "properties": {
    "state": "Running",
    "hostNames": ["{name}.azurewebsites.net"],
    "defaultHostName": "{name}.azurewebsites.net",
    "enabled": true,
    "enabledHostNames": ["{name}.azurewebsites.net", "{name}.scm.azurewebsites.net"],
    "hostNameSslStates": [],
    "serverFarmId": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Web/serverfarms/{planName}",
    "reserved": true,
    "siteConfig": null,
    "outboundIpAddresses": "1.2.3.4,5.6.7.8",
    "possibleOutboundIpAddresses": "1.2.3.4,5.6.7.8,9.10.11.12",
    "resourceGroup": "{rg}",
    "lastModifiedTimeUtc": "2024-01-15T10:30:00Z",
    "repositorySiteName": "{name}",
    "availabilityState": "Normal",
    "httpsOnly": true,
    "ftpUsername": "{name}\\$funcappname",
    "maxNumberOfWorkers": null,
    "containerSize": 1536,
    "dailyMemoryTimeQuota": 0,
    "suspendedTill": null,
    "isXenon": false,
    "hyperV": false
  }
}
```

### 7.2 Function Apps - Get

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}?api-version=2024-04-01
```

### 7.3 Function Apps - List By Resource Group

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites?api-version=2024-04-01
```

### 7.4 Function Apps - Delete

```
DELETE https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}?api-version=2024-04-01
```

Optional query: `deleteMetrics=true`, `deleteEmptyServerFarm=false`

### 7.5 Functions - List

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}/functions?api-version=2024-04-01
```

**Response (200 OK):**

```json
{
  "value": [
    {
      "id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Web/sites/{name}/functions/{functionName}",
      "name": "{functionName}",
      "type": "Microsoft.Web/sites/functions",
      "properties": {
        "name": "{functionName}",
        "function_app_id": "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Web/sites/{name}",
        "script_root_path_href": "https://{name}.azurewebsites.net/admin/vfs/site/wwwroot/{functionName}/",
        "script_href": "https://{name}.azurewebsites.net/admin/vfs/site/wwwroot/{functionName}/index.js",
        "config_href": "https://{name}.azurewebsites.net/admin/vfs/site/wwwroot/{functionName}/function.json",
        "test_data_href": "https://{name}.azurewebsites.net/admin/vfs/data/Functions/sampledata/{functionName}.dat",
        "href": "https://{name}.azurewebsites.net/admin/functions/{functionName}",
        "config": {
          "bindings": [
            {
              "authLevel": "function",
              "type": "httpTrigger",
              "direction": "in",
              "name": "req",
              "methods": ["get", "post"]
            },
            {
              "type": "http",
              "direction": "out",
              "name": "res"
            }
          ]
        },
        "invoke_url_template": "https://{name}.azurewebsites.net/api/{functionName}",
        "language": "node",
        "isDisabled": false
      }
    }
  ],
  "nextLink": null
}
```

### 7.6 Functions - Get

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}/functions/{functionName}?api-version=2024-04-01
```

### 7.7 HTTP Trigger Invocation (Data Plane)

```
POST https://{name}.azurewebsites.net/api/{functionName}?code={functionKey}
Content-Type: application/json

{
  "name": "World"
}
```

Or with function key in header:
```
POST https://{name}.azurewebsites.net/api/{functionName}
x-functions-key: {functionKey}
Content-Type: application/json
```

**Response:** Depends on function implementation. Typically `200 OK` with function output.

### 7.8 Function Keys - List

```
POST https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}/functions/{functionName}/listkeys?api-version=2024-04-01
```

**Response:**

```json
{
  "default": "function-key-value"
}
```

### 7.9 Host Keys - List

```
POST https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}/host/default/listkeys?api-version=2024-04-01
```

**Response:**

```json
{
  "masterKey": "master-key-value",
  "functionKeys": {
    "default": "host-function-key-value"
  },
  "systemKeys": {}
}
```

---

## 8. Application Insights

### 8.1 Query API (Data Plane)

**Base URL:** `https://api.applicationinsights.io`
**api-version:** `v1` (path-based)

**Authentication:**
```
Authorization: Bearer <access-token>
```
Scope: `https://api.applicationinsights.io/.default`

Alternatively, use API key: `x-api-key: {api-key}`

#### POST Query

```
POST https://api.applicationinsights.io/v1/apps/{appId}/query
Content-Type: application/json
Authorization: Bearer <token>
```

**Request Body:**

```json
{
  "query": "requests | summarize count() by resultCode | order by count_ desc",
  "timespan": "PT24H",
  "applications": [
    "app-guid-1"
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | Yes | KQL query |
| `timespan` | string | No | ISO 8601 duration or `start/end` |
| `applications` | string[] | No | Additional Application Insights app IDs |

**Response (200 OK):**

```json
{
  "tables": [
    {
      "name": "PrimaryResult",
      "columns": [
        { "name": "resultCode", "type": "string" },
        { "name": "count_", "type": "long" }
      ],
      "rows": [
        ["200", 15234],
        ["404", 123],
        ["500", 12]
      ]
    }
  ]
}
```

The response format is identical to the Log Analytics query API (Section 3.1).

#### GET Query

```
GET https://api.applicationinsights.io/v1/apps/{appId}/query?query=requests%20|%20take%2010&timespan=PT1H
```

### 8.2 Application Insights via ARM (Query through Log Analytics)

Application Insights workspaces can also be queried through Log Analytics:

```
POST https://api.loganalytics.azure.com/v1/workspaces/{workspaceId}/query
Content-Type: application/json

{
  "query": "AppRequests | summarize count() by ResultCode | order by count_ desc"
}
```

### 8.3 Live Metrics (Data Plane)

```
GET https://api.applicationinsights.io/v1/apps/{appId}/metrics/{metricId}
```

**Common metricId values:**

| metricId | Description |
|----------|-------------|
| `requests/count` | Total request count |
| `requests/duration` | Average request duration |
| `requests/failed` | Failed request count |
| `exceptions/count` | Exception count |
| `performanceCounters/processCpuPercentage` | Process CPU % |
| `performanceCounters/processPrivateBytes` | Process private bytes |

**Query parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `timespan` | string | ISO 8601 duration |
| `interval` | string | Aggregation interval (e.g., `PT1M`) |
| `aggregation` | string | `sum`, `avg`, `min`, `max`, `count` |
| `segment` | string | Dimension to segment by |
| `top` | int | Number of segments to return |

**Response (200 OK):**

```json
{
  "value": {
    "start": "2024-01-15T09:30:00Z",
    "end": "2024-01-15T10:30:00Z",
    "requests/count": {
      "sum": 12345
    }
  }
}
```

### 8.4 Error Response Format

Same OneAPI format as Log Analytics:

```json
{
  "error": {
    "code": "InvalidQueryError",
    "message": "The query is invalid.",
    "innererror": {
      "code": "SyntaxError",
      "message": "Unexpected token at position 15"
    }
  }
}
```

---

## Appendix A: API Version Summary

| Service | Provider | api-version | Base URL |
|---------|----------|-------------|----------|
| Container Apps Jobs | Microsoft.App | `2024-03-01` | `management.azure.com` |
| Managed Environments | Microsoft.App | `2024-03-01` | `management.azure.com` |
| Log Analytics Query | - | `v1` (path) | `api.loganalytics.azure.com` |
| Log Ingestion | - | `2023-01-01` | `{dce}.ingest.monitor.azure.com` |
| Storage Accounts | Microsoft.Storage | `2024-01-01` | `management.azure.com` |
| File Shares (ARM) | Microsoft.Storage | `2024-01-01` | `management.azure.com` |
| File Operations (data) | - | `2024-08-04` | `{account}.file.core.windows.net` |
| Container Registry (ARM) | Microsoft.ContainerRegistry | `2023-07-01` | `management.azure.com` |
| Container Registry (OCI) | - | `2021-07-01` | `{registry}.azurecr.io` |
| Private DNS Zones | Microsoft.Network | `2018-09-01` | `management.azure.com` |
| Function Apps | Microsoft.Web | `2024-04-01` | `management.azure.com` |
| Function Invocation (data) | - | - | `{app}.azurewebsites.net` |
| Application Insights Query | - | `v1` (path) | `api.applicationinsights.io` |
| Application Insights Metrics | - | `v1` (path) | `api.applicationinsights.io` |
| Subscription aliases + cancel/rename/enable | Microsoft.Subscription | `2021-10-01` | `management.azure.com` |

## Appendix B: Simulator Implementation Notes

### B.1 What the Simulator Must Handle

For each service, the simulator must:

1. **Route matching:** Parse the ARM path segments (`/subscriptions/{sub}/resourceGroups/{rg}/providers/...`) and extract parameters.
2. **api-version validation:** Accept the specified api-version and reject unknown versions with `InvalidApiVersionParameter`.
3. **Request body validation:** Validate required fields and reject with `InvalidRequestContent` on malformed input.
4. **Resource state:** Maintain in-memory state for all resources (jobs, environments, file shares, DNS zones, etc.).
5. **Async operations:** For PUT/DELETE that return 202, generate an operation ID and support polling.
6. **Error responses:** Return the correct error envelope format for each service (ARM uses `error.code/message`, Storage uses `CloudError`, ACR uses `errors[]`).
7. **Pagination:** Support `nextLink` for list operations.

### B.2 Pre-Seeded Resources

The simulator should pre-seed:

- One `ManagedEnvironment` (configured via simulator config)
- One `StorageAccount` (configured via simulator config)
- One `ContainerRegistry` (configured via simulator config)

### B.3 Authentication Handling

The simulator should:

- Accept any `Authorization: Bearer <token>` header without validation (for testing)
- Optionally validate tokens against a configured test tenant
- Return `401 Unauthorized` when no Authorization header is present
- Return `403 Forbidden` for explicitly denied operations (optional)

### B.4 Provisioning State Machine

For async resources (Jobs, DNS Zones), the simulator should transition:

```
PUT  -> provisioningState: "InProgress" -> (after delay) -> "Succeeded"
DELETE -> provisioningState: "Deleting" -> (after delay) -> resource removed
```

The delay should be configurable (default: 0 for fast tests, or a few seconds for realism).
