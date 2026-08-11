# GCP API Specification for Sockerless Simulator

This document provides the exact REST API specifications needed to implement the Sockerless GCP
simulator. All field names use camelCase as per GCP conventions. Each service section documents
endpoint URLs, HTTP methods, request/response schemas, error formats, and LRO patterns.

---

## Table of Contents

1. [Common Patterns](#1-common-patterns)
2. [Cloud Run Jobs v2](#2-cloud-run-jobs-v2)
3. [Cloud Logging v2](#3-cloud-logging-v2)
4. [Cloud DNS v1](#4-cloud-dns-v1)
5. [Cloud Storage (GCS) JSON API v1](#5-cloud-storage-gcs-json-api-v1)
6. [Artifact Registry v1](#6-artifact-registry-v1)
7. [Cloud Functions v2](#7-cloud-functions-v2)

---

## 1. Common Patterns

### 1.1 Standard Error Response Format

All GCP REST APIs return errors in this format (per AIP-193):

```json
{
  "error": {
    "code": 404,
    "message": "Resource 'projects/my-project/locations/us-central1/jobs/my-job' was not found.",
    "status": "NOT_FOUND",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "notFound",
        "domain": "googleapis.com",
        "metadata": {}
      }
    ]
  }
}
```

**Standard error codes:**

| HTTP Code | gRPC Code         | Status String            | Typical Cause                          |
|-----------|-------------------|--------------------------|----------------------------------------|
| 400       | INVALID_ARGUMENT  | `INVALID_ARGUMENT`       | Malformed request, validation failure  |
| 401       | UNAUTHENTICATED   | `UNAUTHENTICATED`        | Missing or invalid credentials         |
| 403       | PERMISSION_DENIED | `PERMISSION_DENIED`      | Insufficient permissions               |
| 404       | NOT_FOUND         | `NOT_FOUND`              | Resource does not exist                |
| 409       | ALREADY_EXISTS    | `ALREADY_EXISTS`         | Resource already exists (create)       |
| 412       | FAILED_PRECONDITION| `FAILED_PRECONDITION`   | Etag mismatch, resource state conflict |
| 429       | RESOURCE_EXHAUSTED| `RESOURCE_EXHAUSTED`     | Rate limit exceeded                    |
| 500       | INTERNAL          | `INTERNAL`               | Internal server error                  |
| 503       | UNAVAILABLE       | `UNAVAILABLE`            | Service temporarily unavailable        |

### 1.2 Long-Running Operation (LRO) Pattern (AIP-151)

Many mutating operations (create, delete, update) return an `Operation` resource instead of the
final resource. The simulator must implement the Operation polling pattern.

**Operation resource (`google.longrunning.Operation`):**

```json
{
  "name": "projects/{project}/locations/{location}/operations/{operationId}",
  "metadata": {
    "@type": "type.googleapis.com/google.cloud.run.v2.{ServiceName}",
    "createTime": "2024-01-15T10:30:00.000000Z",
    "target": "projects/{project}/locations/{location}/jobs/{jobId}",
    "verb": "create",
    "apiVersion": "v2"
  },
  "done": false,
  "response": null,
  "error": null
}
```

**When `done: true` (success):**

```json
{
  "name": "projects/{project}/locations/{location}/operations/{operationId}",
  "metadata": { ... },
  "done": true,
  "response": {
    "@type": "type.googleapis.com/google.cloud.run.v2.Job",
    "name": "projects/{project}/locations/{location}/jobs/{jobId}",
    ...
  }
}
```

**When `done: true` (failure):**

```json
{
  "name": "projects/{project}/locations/{location}/operations/{operationId}",
  "metadata": { ... },
  "done": true,
  "error": {
    "code": 3,
    "message": "Invalid argument: ...",
    "details": []
  }
}
```

**Polling endpoint:**

```
GET https://{service}.googleapis.com/v2/projects/{project}/locations/{location}/operations/{operationId}
```

**For the simulator:** Operations should transition to `done: true` immediately or after a short
simulated delay. Store operations in memory and make them retrievable via the polling endpoint.

### 1.3 Pagination Pattern

List endpoints use cursor-based pagination:

- Request query parameter: `pageSize` (int), `pageToken` (string)
- Response fields: `nextPageToken` (string, empty when no more results)

### 1.4 Field Mask Pattern

PATCH/Update operations use `updateMask` query parameter:

```
PATCH /v2/{name}?updateMask=field1,field2.subfield
```

The `updateMask` is a comma-separated list of fully qualified field names.

---

## 2. Cloud Run Jobs v2

**Service endpoint:** `https://run.googleapis.com`
**API version:** `v2`
**Base path:** `/v2/projects/{project}/locations/{location}/jobs`

### 2.1 Job Resource

```json
{
  "name": "projects/{project}/locations/{location}/jobs/{jobId}",
  "uid": "abc123-def456-...",
  "generation": "1",
  "labels": {
    "env": "production"
  },
  "annotations": {
    "run.googleapis.com/launch-stage": "BETA"
  },
  "createTime": "2024-01-15T10:30:00.000000Z",
  "updateTime": "2024-01-15T10:30:00.000000Z",
  "deleteTime": null,
  "expireTime": null,
  "creator": "user@example.com",
  "lastModifier": "user@example.com",
  "client": "gcloud",
  "clientVersion": "450.0.0",
  "launchStage": "GA",
  "binaryAuthorization": {
    "useDefault": false,
    "policy": ""
  },
  "template": {
    "labels": {},
    "annotations": {},
    "parallelism": 1,
    "taskCount": 1,
    "template": {
      "containers": [
        {
          "name": "job-0",
          "image": "us-docker.pkg.dev/cloudrun/container/job:latest",
          "command": [],
          "args": [],
          "env": [
            {
              "name": "ENV_VAR",
              "value": "value"
            },
            {
              "name": "SECRET_VAR",
              "valueSource": {
                "secretKeyRef": {
                  "secret": "projects/{project}/secrets/{secret}",
                  "version": "latest"
                }
              }
            }
          ],
          "resources": {
            "limits": {
              "cpu": "1000m",
              "memory": "512Mi"
            }
          },
          "ports": [],
          "volumeMounts": [
            {
              "name": "vol1",
              "mountPath": "/mnt/data"
            }
          ],
          "workingDir": "",
          "livenessProbe": null,
          "startupProbe": null
        }
      ],
      "volumes": [
        {
          "name": "vol1",
          "secret": {
            "secret": "projects/{project}/secrets/{secret}",
            "items": [
              {
                "path": "secret.txt",
                "version": "latest",
                "mode": 256
              }
            ]
          }
        },
        {
          "name": "vol2",
          "emptyDir": {
            "medium": "MEMORY",
            "sizeLimit": "128Mi"
          }
        }
      ],
      "maxRetries": 3,
      "timeout": "600s",
      "serviceAccount": "sa@project.iam.gserviceaccount.com",
      "executionEnvironment": "EXECUTION_ENVIRONMENT_GEN2",
      "encryptionKey": ""
    }
  },
  "observedGeneration": "1",
  "terminalCondition": {
    "type": "Ready",
    "state": "CONDITION_SUCCEEDED",
    "message": "",
    "lastTransitionTime": "2024-01-15T10:30:05.000000Z",
    "severity": "INFO",
    "reason": "",
    "revisionReason": "",
    "executionReason": ""
  },
  "conditions": [
    {
      "type": "Ready",
      "state": "CONDITION_SUCCEEDED",
      "message": "",
      "lastTransitionTime": "2024-01-15T10:30:05.000000Z",
      "severity": "INFO",
      "reason": ""
    }
  ],
  "executionCount": 0,
  "latestCreatedExecution": {
    "name": "projects/{project}/locations/{location}/jobs/{jobId}/executions/{executionId}",
    "createTime": "2024-01-15T10:35:00.000000Z",
    "completionTime": "2024-01-15T10:35:30.000000Z"
  },
  "reconciling": false,
  "satisfiesPzs": false,
  "etag": "\"abc123\"",
  "startExecutionToken": "",
  "runExecutionToken": ""
}
```

**Condition states:** `CONDITION_PENDING`, `CONDITION_RECONCILING`, `CONDITION_FAILED`, `CONDITION_SUCCEEDED`

**Execution environments:** `EXECUTION_ENVIRONMENT_GEN1`, `EXECUTION_ENVIRONMENT_GEN2`

**Launch stages:** `UNIMPLEMENTED`, `PRELAUNCH`, `EARLY_ACCESS`, `ALPHA`, `BETA`, `GA`, `DEPRECATED`

### 2.2 jobs.create

```
POST https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs?jobId={jobId}&validateOnly={bool}
```

**Path parameters:**
- `project` (required): GCP project ID
- `location` (required): Region (e.g., `us-central1`)

**Query parameters:**
- `jobId` (required): The unique identifier for the job. Must be unique per project/location.
- `validateOnly` (optional, bool): If true, validate the request without creating.

**Request body:** Job resource (the `name` field is ignored; it is derived from the path).

**Minimal request example:**

```json
{
  "template": {
    "template": {
      "containers": [
        {
          "image": "us-docker.pkg.dev/cloudrun/container/job:latest"
        }
      ]
    }
  }
}
```

**Full request example:**

```json
{
  "labels": {"env": "staging"},
  "annotations": {},
  "launchStage": "GA",
  "template": {
    "parallelism": 1,
    "taskCount": 3,
    "template": {
      "containers": [
        {
          "image": "gcr.io/my-project/my-job:latest",
          "command": ["/bin/sh"],
          "args": ["-c", "echo hello"],
          "env": [
            {"name": "BATCH_SIZE", "value": "100"}
          ],
          "resources": {
            "limits": {
              "cpu": "2000m",
              "memory": "1Gi"
            }
          }
        }
      ],
      "maxRetries": 3,
      "timeout": "3600s",
      "serviceAccount": "my-sa@my-project.iam.gserviceaccount.com"
    }
  }
}
```

**Response:** `google.longrunning.Operation` with `response` containing the Job resource.

```json
{
  "name": "projects/my-project/locations/us-central1/operations/op-123",
  "metadata": {
    "@type": "type.googleapis.com/google.cloud.run.v2.Job",
    "name": "projects/my-project/locations/us-central1/jobs/my-job",
    "uid": "...",
    "generation": "1",
    "createTime": "2024-01-15T10:30:00.000000Z"
  },
  "done": true,
  "response": {
    "@type": "type.googleapis.com/google.cloud.run.v2.Job",
    "name": "projects/my-project/locations/us-central1/jobs/my-job",
    "uid": "abc-123",
    "generation": "1",
    "template": { ... },
    "terminalCondition": {
      "type": "Ready",
      "state": "CONDITION_SUCCEEDED"
    },
    "conditions": [ ... ],
    "reconciling": false,
    "etag": "\"abc123\""
  }
}
```

**Common errors:**
- 409 `ALREADY_EXISTS`: Job with same name already exists
- 400 `INVALID_ARGUMENT`: Invalid job configuration (bad image, invalid resource limits)

### 2.3 jobs.get

```
GET https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs/{jobId}
```

**Response:** Job resource (see section 2.1).

**Common errors:**
- 404 `NOT_FOUND`: Job does not exist

### 2.4 jobs.list

```
GET https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs?pageSize={int}&pageToken={string}&showDeleted={bool}
```

**Query parameters:**
- `pageSize` (optional, int): Max results per page (default 20, max 100)
- `pageToken` (optional, string): Continuation token
- `showDeleted` (optional, bool): If true, include soft-deleted jobs

**Response:**

```json
{
  "jobs": [
    { ... Job resource ... },
    { ... Job resource ... }
  ],
  "nextPageToken": "token-for-next-page"
}
```

### 2.5 jobs.delete

```
DELETE https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs/{jobId}?validateOnly={bool}&etag={string}
```

**Query parameters:**
- `validateOnly` (optional, bool)
- `etag` (optional, string): If provided, must match current etag

**Response:** `google.longrunning.Operation` with `response` containing the deleted Job resource.

**Common errors:**
- 404 `NOT_FOUND`: Job does not exist
- 412 `FAILED_PRECONDITION`: Etag mismatch

### 2.6 jobs.run

```
POST https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs/{jobId}:run
```

**Request body (optional overrides):**

```json
{
  "overrides": {
    "containerOverrides": [
      {
        "name": "job-0",
        "args": ["--batch-id=42"],
        "env": [
          {"name": "OVERRIDE_VAR", "value": "override_value"}
        ],
        "clearArgs": false
      }
    ],
    "taskCount": 5,
    "timeout": "1800s"
  },
  "validateOnly": false
}
```

**Response:** `google.longrunning.Operation` with `response` containing an Execution resource.

```json
{
  "name": "projects/my-project/locations/us-central1/operations/op-456",
  "metadata": {
    "@type": "type.googleapis.com/google.cloud.run.v2.Execution"
  },
  "done": false
}
```

**Common errors:**
- 404 `NOT_FOUND`: Job does not exist
- 400 `INVALID_ARGUMENT`: Invalid overrides

### 2.7 Execution Resource

```json
{
  "name": "projects/{project}/locations/{location}/jobs/{jobId}/executions/{executionId}",
  "uid": "exec-uid-123",
  "generation": "1",
  "labels": {},
  "annotations": {},
  "createTime": "2024-01-15T10:35:00.000000Z",
  "startTime": "2024-01-15T10:35:01.000000Z",
  "completionTime": "2024-01-15T10:35:30.000000Z",
  "updateTime": "2024-01-15T10:35:30.000000Z",
  "deleteTime": null,
  "expireTime": null,
  "launchStage": "GA",
  "job": "projects/{project}/locations/{location}/jobs/{jobId}",
  "parallelism": 1,
  "taskCount": 3,
  "template": {
    "containers": [
      {
        "image": "gcr.io/my-project/my-job:latest",
        "command": [],
        "args": [],
        "env": [],
        "resources": {
          "limits": {
            "cpu": "1000m",
            "memory": "512Mi"
          }
        }
      }
    ],
    "maxRetries": 3,
    "timeout": "600s",
    "serviceAccount": "sa@project.iam.gserviceaccount.com"
  },
  "reconciling": false,
  "conditions": [
    {
      "type": "Ready",
      "state": "CONDITION_SUCCEEDED",
      "message": "Execution completed successfully.",
      "lastTransitionTime": "2024-01-15T10:35:30.000000Z",
      "severity": "INFO",
      "reason": ""
    },
    {
      "type": "Completed",
      "state": "CONDITION_SUCCEEDED",
      "message": "",
      "lastTransitionTime": "2024-01-15T10:35:30.000000Z",
      "severity": "INFO",
      "reason": ""
    }
  ],
  "observedGeneration": "1",
  "runningCount": 0,
  "succeededCount": 3,
  "failedCount": 0,
  "cancelledCount": 0,
  "retriedCount": 0,
  "logUri": "https://console.cloud.google.com/logs?project={project}",
  "satisfiesPzs": false,
  "etag": "\"def456\""
}
```

**Execution completion states** (for the `completionStatus` field on `latestCreatedExecution`):
- `COMPLETION_STATUS_UNSPECIFIED`
- `SUCCEEDED`
- `FAILED`
- `CANCELLED`

### 2.8 executions.get

```
GET https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs/{jobId}/executions/{executionId}
```

**Response:** Execution resource (see section 2.7).

### 2.9 executions.list

```
GET https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs/{jobId}/executions?pageSize={int}&pageToken={string}&showDeleted={bool}
```

**Response:**

```json
{
  "executions": [
    { ... Execution resource ... }
  ],
  "nextPageToken": ""
}
```

### 2.10 executions.cancel

```
POST https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs/{jobId}/executions/{executionId}:cancel
```

**Request body:** Empty `{}`

**Response:** `google.longrunning.Operation` with `response` containing the updated Execution resource (with `cancelledCount` updated).

**Common errors:**
- 404 `NOT_FOUND`: Execution does not exist
- 400 `FAILED_PRECONDITION`: Execution already completed

### 2.11 executions.delete

```
DELETE https://run.googleapis.com/v2/projects/{project}/locations/{location}/jobs/{jobId}/executions/{executionId}?validateOnly={bool}&etag={string}
```

**Response:** `google.longrunning.Operation` with `response` containing the deleted Execution.

---

## 3. Cloud Logging v2

**Service endpoint:** `https://logging.googleapis.com`
**API version:** `v2`

### 3.1 LogEntry Resource

```json
{
  "logName": "projects/{project}/logs/{logId}",
  "resource": {
    "type": "cloud_run_job",
    "labels": {
      "project_id": "my-project",
      "job_name": "my-job",
      "location": "us-central1"
    }
  },
  "timestamp": "2024-01-15T10:35:05.123456Z",
  "receiveTimestamp": "2024-01-15T10:35:05.200000Z",
  "severity": "INFO",
  "insertId": "abc123def456",
  "httpRequest": {
    "requestMethod": "GET",
    "requestUrl": "https://example.com/api",
    "requestSize": "256",
    "status": 200,
    "responseSize": "1024",
    "userAgent": "curl/7.68.0",
    "remoteIp": "203.0.113.1",
    "serverIp": "10.0.0.1",
    "referer": "",
    "latency": "0.123s",
    "cacheLookup": false,
    "cacheHit": false,
    "cacheValidatedWithOriginServer": false,
    "cacheFillBytes": "0",
    "protocol": "HTTP/1.1"
  },
  "labels": {
    "instanceId": "abc-123"
  },
  "operation": {
    "id": "op-123",
    "producer": "my-service",
    "first": true,
    "last": false
  },
  "trace": "projects/{project}/traces/{traceId}",
  "spanId": "span-123",
  "traceSampled": false,
  "sourceLocation": {
    "file": "main.go",
    "line": "42",
    "function": "main"
  },

  "textPayload": "Job completed successfully.",

  "jsonPayload": {
    "message": "Job completed",
    "duration_ms": 1234,
    "task_index": 0
  },

  "protoPayload": {
    "@type": "type.googleapis.com/google.cloud.audit.AuditLog",
    "serviceName": "run.googleapis.com",
    "methodName": "google.cloud.run.v2.Jobs.RunJob"
  }
}
```

**Notes:**
- A LogEntry has exactly ONE of: `textPayload` (string), `jsonPayload` (object), or `protoPayload` (object with `@type`).
- `severity` values: `DEFAULT`, `DEBUG`, `INFO`, `NOTICE`, `WARNING`, `ERROR`, `CRITICAL`, `ALERT`, `EMERGENCY`
- `logName` format: `projects/{project}/logs/{url-encoded-log-id}`
- The `logId` portion is URL-encoded (e.g., `cloudaudit.googleapis.com%2Factivity`)

### 3.2 entries.list

```
POST https://logging.googleapis.com/v2/entries:list
```

**Note:** This uses POST (not GET) per gRPC transcoding. The filter is in the request body.

**Request body:**

```json
{
  "resourceNames": [
    "projects/my-project"
  ],
  "filter": "resource.type=\"cloud_run_job\" AND severity>=ERROR AND timestamp>=\"2024-01-15T00:00:00Z\"",
  "orderBy": "timestamp desc",
  "pageSize": 100,
  "pageToken": ""
}
```

**Request fields:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `resourceNames` | string[] | Yes | Parent resources: `projects/{id}`, `organizations/{id}`, `billingAccounts/{id}`, `folders/{id}` |
| `filter` | string | No | Logging query language filter (max 20,000 chars) |
| `orderBy` | string | No | `"timestamp asc"` (default) or `"timestamp desc"` |
| `pageSize` | int | No | Max entries per response |
| `pageToken` | string | No | Continuation token from previous response |

**Response:**

```json
{
  "entries": [
    { ... LogEntry ... },
    { ... LogEntry ... }
  ],
  "nextPageToken": "token-for-next-page"
}
```

### 3.3 entries.write

```
POST https://logging.googleapis.com/v2/entries:write
```

**Request body:**

```json
{
  "logName": "projects/my-project/logs/my-log",
  "resource": {
    "type": "cloud_run_job",
    "labels": {
      "project_id": "my-project",
      "job_name": "my-job",
      "location": "us-central1"
    }
  },
  "labels": {
    "common-label": "value"
  },
  "entries": [
    {
      "logName": "projects/my-project/logs/my-log",
      "severity": "INFO",
      "textPayload": "Hello, world!",
      "timestamp": "2024-01-15T10:35:05.123456Z"
    },
    {
      "severity": "ERROR",
      "jsonPayload": {
        "message": "Something went wrong",
        "error_code": 42
      }
    }
  ],
  "partialSuccess": false,
  "dryRun": false
}
```

**Request fields:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logName` | string | No | Default log name for entries that don't specify one |
| `resource` | MonitoredResource | No | Default monitored resource for entries |
| `labels` | map<string,string> | No | Default labels merged into each entry |
| `entries` | LogEntry[] | Yes | Log entries to write (max 1000 per request, max 1000 unique `resourceNames`) |
| `partialSuccess` | bool | No | If true, write valid entries even if some are invalid |
| `dryRun` | bool | No | If true, validate but do not write |

**Response (success):**

```json
{}
```

The response body is empty on success.

**Common errors:**
- 400 `INVALID_ARGUMENT`: Invalid log entries, malformed filter
- 404 `NOT_FOUND`: Referenced resource does not exist
- 429 `RESOURCE_EXHAUSTED`: Write quota exceeded

### 3.4 Logging Query Language (Filter Syntax)

The simulator must parse filter strings used in `entries.list`. Here is the grammar:

**Comparison operators:**
- `=` : Equality
- `!=` : Inequality
- `<`, `<=`, `>`, `>=` : Ordering (works on strings, numbers, timestamps)
- `:` : "Has" operator (substring match for strings, field existence for objects)

**Logical operators:**
- `AND` (default between expressions; can be implicit)
- `OR`
- `NOT` (prefix negation)

**Grouping:**
- Parentheses: `(expr1 OR expr2) AND expr3`

**Field paths:**
- Dot-separated: `resource.type`, `resource.labels.project_id`
- Payload fields: `jsonPayload.message`, `protoPayload.serviceName`

**String values:**
- Double-quoted: `resource.type = "cloud_run_job"`
- Single-quoted: `resource.type = 'cloud_run_job'`

**Timestamp format:**
- RFC 3339: `timestamp >= "2024-01-15T00:00:00Z"`

**Examples:**

```
resource.type = "cloud_run_job"
severity >= ERROR
timestamp >= "2024-01-15T00:00:00Z" AND timestamp < "2024-01-16T00:00:00Z"
resource.type = "cloud_run_job" AND resource.labels.job_name = "my-job"
textPayload : "error"
jsonPayload.message = "Job completed"
logName = "projects/my-project/logs/cloudaudit.googleapis.com%2Factivity"
NOT severity = "DEBUG"
(severity = "ERROR" OR severity = "CRITICAL") AND resource.type = "cloud_run_job"
```

**Minimum filter support for simulator:**
1. `resource.type = "..."` comparison
2. `resource.labels.{key} = "..."` comparison
3. `severity >= {LEVEL}` comparison (must understand severity ordering)
4. `timestamp >= "..."` and `timestamp < "..."` range queries
5. `logName = "..."` comparison
6. `AND` / `OR` logical operators
7. `textPayload : "substring"` has-operator
8. `jsonPayload.{field} = "..."` nested field access

---

## 4. Cloud DNS v1

**Service endpoint:** `https://dns.googleapis.com`
**API version:** `dns/v1`
**Base path:** `/dns/v1/projects/{project}`

### 4.1 ManagedZone Resource

```json
{
  "kind": "dns#managedZone",
  "name": "my-zone",
  "id": "1234567890",
  "dnsName": "example.com.",
  "description": "My DNS zone",
  "nameServers": [
    "ns-cloud-a1.googledomains.com.",
    "ns-cloud-a2.googledomains.com.",
    "ns-cloud-a3.googledomains.com.",
    "ns-cloud-a4.googledomains.com."
  ],
  "creationTime": "2024-01-15T10:30:00.000Z",
  "dnssecConfig": {
    "state": "off",
    "kind": "dns#managedZoneDnsSecConfig"
  },
  "visibility": "public",
  "nameServerSet": "",
  "cloudLoggingConfig": {
    "kind": "dns#managedZoneCloudLoggingConfig",
    "enableLogging": false
  }
}
```

**Key fields:**
| Field | Type | Required (create) | Description |
|-------|------|-------------------|-------------|
| `kind` | string | No (output) | Always `"dns#managedZone"` |
| `name` | string | Yes | Zone name (1-63 chars, lowercase alphanumeric + hyphens) |
| `dnsName` | string | Yes | DNS name suffix (must end with `.`) |
| `description` | string | Yes | Mutable description (max 1024 chars) |
| `id` | string | No (output) | Server-assigned numeric ID |
| `nameServers` | string[] | No (output) | Assigned name servers |
| `creationTime` | string | No (output) | RFC 3339 timestamp |
| `visibility` | string | No | `"public"` (default) or `"private"` |
| `nameServerSet` | string | No | Custom name server set |

### 4.2 managedZones.create

```
POST https://dns.googleapis.com/dns/v1/projects/{project}/managedZones
```

**Request body:** ManagedZone resource.

**Minimal request:**

```json
{
  "name": "my-zone",
  "dnsName": "example.com.",
  "description": "My DNS zone"
}
```

**Response:** ManagedZone resource with server-populated fields (`id`, `nameServers`, `creationTime`).

**Common errors:**
- 409 `ALREADY_EXISTS` / `alreadyExists`: Zone name already taken
- 400 `INVALID_ARGUMENT`: Invalid zone name or DNS name

### 4.3 managedZones.get

```
GET https://dns.googleapis.com/dns/v1/projects/{project}/managedZones/{managedZone}
```

**Path parameters:**
- `managedZone`: Zone name or numeric ID

**Response:** ManagedZone resource.

### 4.4 managedZones.list

```
GET https://dns.googleapis.com/dns/v1/projects/{project}/managedZones?maxResults={int}&pageToken={string}&dnsName={string}
```

**Query parameters:**
- `maxResults` (optional, int): Max zones per response
- `pageToken` (optional, string): Continuation token
- `dnsName` (optional, string): Filter by DNS name

**Response:**

```json
{
  "kind": "dns#managedZonesListResponse",
  "managedZones": [
    { ... ManagedZone ... }
  ],
  "nextPageToken": "",
  "header": {
    "operationId": "op-123"
  }
}
```

### 4.5 managedZones.delete

```
DELETE https://dns.googleapis.com/dns/v1/projects/{project}/managedZones/{managedZone}
```

**Response:** Empty body with HTTP 204 on success.

**Common errors:**
- 400 `FAILED_PRECONDITION`: Zone is not empty (has non-NS/SOA records)
- 404 `NOT_FOUND`: Zone does not exist

### 4.6 ResourceRecordSet Resource

```json
{
  "kind": "dns#resourceRecordSet",
  "name": "www.example.com.",
  "type": "A",
  "ttl": 300,
  "rrdatas": [
    "203.0.113.1",
    "203.0.113.2"
  ],
  "signatureRrdatas": [],
  "routingPolicy": null
}
```

**Key fields:**
| Field | Type | Required (create) | Description |
|-------|------|-------------------|-------------|
| `kind` | string | No (output) | Always `"dns#resourceRecordSet"` |
| `name` | string | Yes | DNS name (must end with `.`) |
| `type` | string | Yes | Record type: `A`, `AAAA`, `CNAME`, `MX`, `NS`, `SOA`, `TXT`, `SRV`, `CAA`, `PTR` |
| `ttl` | int | Yes | Time-to-live in seconds |
| `rrdatas` | string[] | Yes | Resource data entries |
| `signatureRrdatas` | string[] | No | DNSSEC signatures |
| `routingPolicy` | object | No | Geo/weighted routing policy |

### 4.7 resourceRecordSets.create

```
POST https://dns.googleapis.com/dns/v1/projects/{project}/managedZones/{managedZone}/rrsets
```

**Request body:** ResourceRecordSet resource.

**Example request:**

```json
{
  "name": "www.example.com.",
  "type": "A",
  "ttl": 300,
  "rrdatas": ["203.0.113.1"]
}
```

**Response:** ResourceRecordSet resource.

**Common errors:**
- 409 `ALREADY_EXISTS`: Record set with same name+type already exists
- 404 `NOT_FOUND`: Managed zone does not exist

### 4.8 resourceRecordSets.get

```
GET https://dns.googleapis.com/dns/v1/projects/{project}/managedZones/{managedZone}/rrsets/{name}/{type}
```

**Path parameters:**
- `name`: Fully qualified DNS name (URL-encoded, including trailing `.`)
- `type`: Record type (`A`, `AAAA`, `CNAME`, etc.)

**Response:** ResourceRecordSet resource.

### 4.9 resourceRecordSets.list

```
GET https://dns.googleapis.com/dns/v1/projects/{project}/managedZones/{managedZone}/rrsets?name={string}&type={string}&maxResults={int}&pageToken={string}
```

**Query parameters:**
- `name` (optional): Filter by record name
- `type` (optional): Filter by record type
- `maxResults` (optional, int)
- `pageToken` (optional, string)

**Response:**

```json
{
  "kind": "dns#resourceRecordSetsListResponse",
  "rrsets": [
    { ... ResourceRecordSet ... }
  ],
  "nextPageToken": "",
  "header": {
    "operationId": "op-123"
  }
}
```

### 4.10 resourceRecordSets.delete

```
DELETE https://dns.googleapis.com/dns/v1/projects/{project}/managedZones/{managedZone}/rrsets/{name}/{type}
```

**Response:** Empty body with HTTP 204 on success.

**Common errors:**
- 404 `NOT_FOUND`: Record set does not exist
- 400 `INVALID_ARGUMENT`: Cannot delete SOA or NS records at zone apex

---

## 5. Cloud Storage (GCS) JSON API v1

**Service endpoint:** `https://storage.googleapis.com`
**API version:** `storage/v1`
**Upload endpoint:** `https://storage.googleapis.com/upload/storage/v1`

**Note:** GCS uses a slightly different error format from other GCP APIs:

```json
{
  "error": {
    "code": 404,
    "message": "Not Found",
    "errors": [
      {
        "message": "Not Found",
        "domain": "global",
        "reason": "notFound",
        "location": "",
        "locationType": ""
      }
    ]
  }
}
```

### 5.1 Bucket Resource

```json
{
  "kind": "storage#bucket",
  "id": "my-bucket",
  "selfLink": "https://storage.googleapis.com/storage/v1/b/my-bucket",
  "projectNumber": "123456789",
  "name": "my-bucket",
  "timeCreated": "2024-01-15T10:30:00.000Z",
  "updated": "2024-01-15T10:30:00.000Z",
  "metageneration": "1",
  "iamConfiguration": {
    "bucketPolicyOnly": {
      "enabled": false
    },
    "uniformBucketLevelAccess": {
      "enabled": true,
      "lockedTime": ""
    },
    "publicAccessPrevention": "inherited"
  },
  "location": "US-CENTRAL1",
  "locationType": "region",
  "storageClass": "STANDARD",
  "etag": "CAE=",
  "defaultEventBasedHold": false,
  "versioning": {
    "enabled": false
  },
  "labels": {
    "env": "test"
  },
  "lifecycle": {
    "rule": []
  },
  "logging": null,
  "cors": [],
  "website": null,
  "encryption": null,
  "retentionPolicy": null,
  "rpo": "DEFAULT",
  "satisfiesPZS": false
}
```

### 5.2 buckets.insert

```
POST https://storage.googleapis.com/storage/v1/b?project={projectId}
```

**Query parameters:**
- `project` (required): Project ID or number
- `predefinedAcl` (optional): `authenticatedRead`, `private`, `projectPrivate`, `publicRead`, `publicReadWrite`
- `predefinedDefaultObjectAcl` (optional): ACL for new objects
- `projection` (optional): `full` or `noAcl`

**Request body:**

```json
{
  "name": "my-new-bucket",
  "location": "US-CENTRAL1",
  "storageClass": "STANDARD",
  "labels": {"env": "dev"},
  "versioning": {"enabled": true}
}
```

**Response:** Bucket resource with server-populated fields.

**Common errors:**
- 409 `conflict` / reason `conflict`: Bucket name already exists globally
- 400 `invalid` / reason `invalid`: Invalid bucket name
- 403 `forbidden` / reason `forbidden`: Insufficient permissions

### 5.3 buckets.get

```
GET https://storage.googleapis.com/storage/v1/b/{bucket}?projection={string}
```

**Query parameters:**
- `projection` (optional): `full` (include ACLs) or `noAcl` (default)
- `ifMetagenerationMatch` (optional, long)
- `ifMetagenerationNotMatch` (optional, long)

**Response:** Bucket resource.

### 5.4 buckets.list

```
GET https://storage.googleapis.com/storage/v1/b?project={projectId}&prefix={string}&maxResults={int}&pageToken={string}&projection={string}
```

**Query parameters:**
- `project` (required): Project ID
- `prefix` (optional): Filter by bucket name prefix
- `maxResults` (optional, int)
- `pageToken` (optional, string)
- `projection` (optional): `full` or `noAcl`

**Response:**

```json
{
  "kind": "storage#buckets",
  "items": [
    { ... Bucket resource ... }
  ],
  "nextPageToken": ""
}
```

### 5.5 buckets.delete

```
DELETE https://storage.googleapis.com/storage/v1/b/{bucket}
```

**Query parameters:**
- `ifMetagenerationMatch` (optional, long)
- `ifMetagenerationNotMatch` (optional, long)

**Response:** Empty body with HTTP 204.

**Common errors:**
- 404 `notFound`: Bucket does not exist
- 409 `conflict`: Bucket is not empty

### 5.6 Object Resource

```json
{
  "kind": "storage#object",
  "id": "my-bucket/my-object/1234567890",
  "selfLink": "https://storage.googleapis.com/storage/v1/b/my-bucket/o/my-object",
  "mediaLink": "https://storage.googleapis.com/download/storage/v1/b/my-bucket/o/my-object?generation=1234567890&alt=media",
  "name": "my-object",
  "bucket": "my-bucket",
  "generation": "1234567890",
  "metageneration": "1",
  "contentType": "application/octet-stream",
  "storageClass": "STANDARD",
  "size": "1024",
  "md5Hash": "base64-encoded-md5",
  "crc32c": "base64-encoded-crc32c",
  "etag": "CJDm+Zy2wIYDEAE=",
  "timeCreated": "2024-01-15T10:30:00.000Z",
  "updated": "2024-01-15T10:30:00.000Z",
  "timeStorageClassUpdated": "2024-01-15T10:30:00.000Z",
  "componentCount": null,
  "metadata": {
    "custom-key": "custom-value"
  },
  "contentEncoding": "",
  "contentDisposition": "",
  "contentLanguage": "",
  "cacheControl": "",
  "owner": {
    "entity": "user-email@example.com",
    "entityId": ""
  },
  "acl": [],
  "temporaryHold": false,
  "eventBasedHold": false,
  "retentionExpirationTime": null,
  "customTime": null
}
```

### 5.7 objects.insert (upload)

**Simple upload (small files):**

```
POST https://storage.googleapis.com/upload/storage/v1/b/{bucket}/o?uploadType=media&name={objectName}
Content-Type: {mime-type}

<binary data>
```

**Multipart upload (metadata + data):**

```
POST https://storage.googleapis.com/upload/storage/v1/b/{bucket}/o?uploadType=multipart
Content-Type: multipart/related; boundary=boundary_string

--boundary_string
Content-Type: application/json; charset=UTF-8

{"name": "my-object", "contentType": "text/plain", "metadata": {"key": "value"}}
--boundary_string
Content-Type: text/plain

Hello, world!
--boundary_string--
```

**Resumable upload (large files):**

Step 1 - Initiate:
```
POST https://storage.googleapis.com/upload/storage/v1/b/{bucket}/o?uploadType=resumable&name={objectName}
Content-Type: application/json
X-Upload-Content-Type: {mime-type}
X-Upload-Content-Length: {total-bytes}

{"name": "my-object", "contentType": "text/plain"}
```

Response: HTTP 200 with `Location` header containing the resumable upload URI.

Step 2 - Upload data:
```
PUT {resumable-upload-uri}
Content-Length: {chunk-bytes}
Content-Range: bytes {start}-{end}/{total}

<binary data>
```

**Response (all upload types):** Object resource.

**Query parameters (common):**
- `name` (required for simple/resumable): Object name
- `uploadType` (required): `media`, `multipart`, or `resumable`
- `contentEncoding` (optional): e.g., `gzip`
- `ifGenerationMatch` (optional, long)
- `ifGenerationNotMatch` (optional, long)
- `ifMetagenerationMatch` (optional, long)
- `ifMetagenerationNotMatch` (optional, long)
- `predefinedAcl` (optional)
- `projection` (optional)

### 5.8 objects.get

**Metadata:**
```
GET https://storage.googleapis.com/storage/v1/b/{bucket}/o/{object}
```

**Download data:**
```
GET https://storage.googleapis.com/storage/v1/b/{bucket}/o/{object}?alt=media
```

**Query parameters:**
- `alt` (optional): `json` (default, returns metadata) or `media` (returns object data)
- `generation` (optional, long): Specific generation
- `ifGenerationMatch` (optional, long)
- `ifGenerationNotMatch` (optional, long)
- `ifMetagenerationMatch` (optional, long)
- `ifMetagenerationNotMatch` (optional, long)
- `projection` (optional)

**Response:** Object resource (for `alt=json`) or raw binary data (for `alt=media`).

### 5.9 objects.list

```
GET https://storage.googleapis.com/storage/v1/b/{bucket}/o?prefix={string}&delimiter={string}&maxResults={int}&pageToken={string}&versions={bool}&projection={string}&startOffset={string}&endOffset={string}&matchGlob={string}
```

**Key query parameters:**
- `prefix` (optional): Filter objects by name prefix
- `delimiter` (optional): Usually `/` for directory-like listing
- `maxResults` (optional, int, default 1000)
- `pageToken` (optional, string)
- `versions` (optional, bool): Include all object versions
- `projection` (optional): `full` or `noAcl`
- `startOffset` (optional, string): Filter objects >= this name
- `endOffset` (optional, string): Filter objects < this name

**Response:**

```json
{
  "kind": "storage#objects",
  "items": [
    { ... Object resource ... }
  ],
  "prefixes": [
    "dir1/",
    "dir2/"
  ],
  "nextPageToken": ""
}
```

**Notes:**
- `prefixes` is returned when `delimiter` is set; contains "virtual directory" prefixes
- `items` may be absent if there are only prefixes

### 5.10 objects.delete

```
DELETE https://storage.googleapis.com/storage/v1/b/{bucket}/o/{object}
```

**Query parameters:**
- `generation` (optional, long)
- `ifGenerationMatch` (optional, long)
- `ifGenerationNotMatch` (optional, long)
- `ifMetagenerationMatch` (optional, long)
- `ifMetagenerationNotMatch` (optional, long)

**Response:** Empty body with HTTP 204.

### 5.11 objects.compose

```
POST https://storage.googleapis.com/storage/v1/b/{destinationBucket}/o/{destinationObject}/compose
```

**Request body:**

```json
{
  "kind": "storage#composeRequest",
  "sourceObjects": [
    {
      "name": "part1.bin",
      "generation": "1234567890",
      "objectPreconditions": {
        "ifGenerationMatch": "1234567890"
      }
    },
    {
      "name": "part2.bin"
    },
    {
      "name": "part3.bin"
    }
  ],
  "destination": {
    "contentType": "application/octet-stream",
    "metadata": {
      "composed": "true"
    }
  }
}
```

**Key constraints:**
- Maximum 32 source objects per compose request
- All source objects must be in the same bucket as the destination

**Response:** Object resource for the composed object. The `componentCount` field will be set.

**Common errors:**
- 404 `notFound`: Source object does not exist
- 400 `invalid`: Too many source objects (>32)

---

## 6. Artifact Registry v1

**Service endpoint:** `https://artifactregistry.googleapis.com`
**API version:** `v1`
**Base path:** `/v1/projects/{project}/locations/{location}/repositories`

**Docker/OCI endpoint:** `https://{location}-docker.pkg.dev`

### 6.1 Repository Resource

```json
{
  "name": "projects/{project}/locations/{location}/repositories/{repositoryId}",
  "format": "DOCKER",
  "description": "Docker image repository",
  "labels": {
    "env": "production"
  },
  "createTime": "2024-01-15T10:30:00.000000Z",
  "updateTime": "2024-01-15T10:30:00.000000Z",
  "kmsKeyName": "",
  "mode": "STANDARD_REPOSITORY",
  "cleanupPolicies": {},
  "sizeBytes": "0",
  "satisfiesPzs": false,
  "cleanupPolicyDryRun": false,
  "dockerConfig": {
    "immutableTags": false
  }
}
```

**Key fields:**
| Field | Type | Required (create) | Description |
|-------|------|-------------------|-------------|
| `name` | string | No (output) | Full resource name |
| `format` | string | Yes | `DOCKER`, `MAVEN`, `NPM`, `APT`, `YUM`, `PYTHON`, `GO`, `KFP`, `GENERIC` |
| `description` | string | No | Human-readable description |
| `labels` | map | No | User labels |
| `mode` | string | No | `STANDARD_REPOSITORY` (default), `VIRTUAL_REPOSITORY`, `REMOTE_REPOSITORY` |
| `kmsKeyName` | string | No | CMEK key resource name |
| `createTime` | string | No (output) | RFC 3339 timestamp |
| `updateTime` | string | No (output) | RFC 3339 timestamp |
| `sizeBytes` | string | No (output) | Total storage size |
| `dockerConfig` | object | No | Docker-specific config (`immutableTags` bool) |

**Repository formats (enum):** `FORMAT_UNSPECIFIED`, `DOCKER`, `MAVEN`, `NPM`, `APT`, `YUM`, `KUBEFLOW_PIPELINES` (alias `KFP`), `PYTHON`, `GO`, `GENERIC`

**Modes:** `MODE_UNSPECIFIED`, `STANDARD_REPOSITORY`, `VIRTUAL_REPOSITORY`, `REMOTE_REPOSITORY`

### 6.2 repositories.create

```
POST https://artifactregistry.googleapis.com/v1/projects/{project}/locations/{location}/repositories?repositoryId={repositoryId}
```

**Query parameters:**
- `repositoryId` (required): Repository ID (unique within project+location)

**Request body:**

```json
{
  "format": "DOCKER",
  "description": "My Docker repo",
  "labels": {"team": "backend"},
  "dockerConfig": {
    "immutableTags": false
  }
}
```

**Response:** `google.longrunning.Operation` with `response` containing the Repository resource.

```json
{
  "name": "projects/my-project/locations/us-central1/operations/op-789",
  "metadata": {
    "@type": "type.googleapis.com/google.devtools.artifactregistry.v1.OperationMetadata"
  },
  "done": true,
  "response": {
    "@type": "type.googleapis.com/google.devtools.artifactregistry.v1.Repository",
    "name": "projects/my-project/locations/us-central1/repositories/my-repo",
    "format": "DOCKER",
    "createTime": "2024-01-15T10:30:00.000000Z",
    "updateTime": "2024-01-15T10:30:00.000000Z"
  }
}
```

**Common errors:**
- 409 `ALREADY_EXISTS`: Repository with same name exists
- 400 `INVALID_ARGUMENT`: Invalid format or configuration

### 6.3 repositories.get

```
GET https://artifactregistry.googleapis.com/v1/projects/{project}/locations/{location}/repositories/{repositoryId}
```

**Response:** Repository resource.

### 6.4 repositories.list

```
GET https://artifactregistry.googleapis.com/v1/projects/{project}/locations/{location}/repositories?pageSize={int}&pageToken={string}
```

**Response:**

```json
{
  "repositories": [
    { ... Repository resource ... }
  ],
  "nextPageToken": ""
}
```

### 6.5 repositories.delete

```
DELETE https://artifactregistry.googleapis.com/v1/projects/{project}/locations/{location}/repositories/{repositoryId}
```

**Response:** `google.longrunning.Operation` (the repository is asynchronously deleted).

**Common errors:**
- 404 `NOT_FOUND`: Repository does not exist

### 6.6 Docker/OCI Distribution API

Artifact Registry implements the OCI Distribution Specification v1.1. The Docker API is
served at `https://{location}-docker.pkg.dev/v2/`.

**Authentication:** Use `Authorization: Bearer {access_token}` header with a Google OAuth2 access token.

**Base URL pattern:** `https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}`

#### 6.6.1 Check API Version

```
GET https://{location}-docker.pkg.dev/v2/
```

**Response:** HTTP 200 with `{}` or `{"errors":[]}` (confirms v2 support).

#### 6.6.2 Pull Manifest (GET)

```
GET https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/manifests/{reference}
```

Where `{reference}` is either a tag (e.g., `latest`) or a digest (e.g., `sha256:abc123...`).

**Request headers:**
- `Accept: application/vnd.oci.image.manifest.v1+json` (OCI)
- `Accept: application/vnd.docker.distribution.manifest.v2+json` (Docker v2)
- `Accept: application/vnd.oci.image.index.v1+json` (OCI index/manifest list)
- `Accept: application/vnd.docker.distribution.manifest.list.v2+json` (Docker manifest list)

**Response (OCI image manifest):**

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {
    "mediaType": "application/vnd.oci.image.config.v1+json",
    "digest": "sha256:config-digest-here",
    "size": 1234
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
      "digest": "sha256:layer-digest-here",
      "size": 5678901
    }
  ],
  "annotations": {}
}
```

**Response headers:**
- `Content-Type: application/vnd.oci.image.manifest.v1+json`
- `Docker-Content-Digest: sha256:{digest}`
- `Content-Length: {size}`

**Common errors:**
- 404 `MANIFEST_UNKNOWN`: Manifest does not exist

#### 6.6.3 Check Manifest Exists (HEAD)

```
HEAD https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/manifests/{reference}
```

**Response:** HTTP 200 with headers only (same headers as GET but no body).

#### 6.6.4 Push Manifest (PUT)

```
PUT https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/manifests/{reference}
Content-Type: application/vnd.oci.image.manifest.v1+json

{manifest JSON body}
```

**Response:** HTTP 201 Created

**Response headers:**
- `Location: /v2/{project}/{repository}/{image}/manifests/{digest}`
- `Docker-Content-Digest: sha256:{digest}`

#### 6.6.5 Pull Blob (GET)

```
GET https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/blobs/{digest}
```

**Response:** HTTP 200 with binary blob data.

**Response headers:**
- `Content-Type: application/octet-stream`
- `Docker-Content-Digest: {digest}`
- `Content-Length: {size}`

**Common errors:**
- 404 `BLOB_UNKNOWN`: Blob does not exist

#### 6.6.6 Check Blob Exists (HEAD)

```
HEAD https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/blobs/{digest}
```

**Response:** HTTP 200 with headers only.

#### 6.6.7 Initiate Blob Upload (POST)

```
POST https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/blobs/uploads/
```

**Optional query parameter:**
- `digest`: If provided, this is a monolithic upload (single PUT with all data)

**Response:** HTTP 202 Accepted

**Response headers:**
- `Location: /v2/{project}/{repository}/{image}/blobs/uploads/{uuid}`
- `Docker-Upload-UUID: {uuid}`
- `Range: 0-0`

#### 6.6.8 Upload Blob Chunk (PATCH)

```
PATCH https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/blobs/uploads/{uuid}
Content-Type: application/octet-stream
Content-Range: {start}-{end}
Content-Length: {chunk-size}

<binary data>
```

**Response:** HTTP 202 Accepted

**Response headers:**
- `Location: /v2/{project}/{repository}/{image}/blobs/uploads/{uuid}`
- `Docker-Upload-UUID: {uuid}`
- `Range: 0-{end}`

#### 6.6.9 Complete Blob Upload (PUT)

```
PUT https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/blobs/uploads/{uuid}?digest={digest}
Content-Length: {size}

<final chunk data or empty>
```

**Response:** HTTP 201 Created

**Response headers:**
- `Location: /v2/{project}/{repository}/{image}/blobs/{digest}`
- `Docker-Content-Digest: {digest}`

#### 6.6.10 Delete Blob (DELETE)

```
DELETE https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/blobs/{digest}
```

**Response:** HTTP 202 Accepted.

#### 6.6.11 List Tags

```
GET https://{location}-docker.pkg.dev/v2/{project}/{repository}/{image}/tags/list?n={int}&last={string}
```

**Query parameters:**
- `n` (optional, int): Max tags to return
- `last` (optional, string): Last tag from previous response (for pagination)

**Response:**

```json
{
  "name": "{project}/{repository}/{image}",
  "tags": [
    "latest",
    "v1.0",
    "v1.1"
  ]
}
```

#### 6.6.12 OCI Error Response Format

Docker/OCI endpoints use a different error format from the standard GCP format:

```json
{
  "errors": [
    {
      "code": "MANIFEST_UNKNOWN",
      "message": "manifest unknown",
      "detail": "..."
    }
  ]
}
```

**Error codes:**
| Code | HTTP Status | Description |
|------|-------------|-------------|
| `BLOB_UNKNOWN` | 404 | Blob not found |
| `BLOB_UPLOAD_INVALID` | 400 | Invalid blob upload |
| `BLOB_UPLOAD_UNKNOWN` | 404 | Upload session not found |
| `DIGEST_INVALID` | 400 | Digest format invalid |
| `MANIFEST_BLOB_UNKNOWN` | 404 | Blob referenced in manifest not found |
| `MANIFEST_INVALID` | 400 | Invalid manifest |
| `MANIFEST_UNKNOWN` | 404 | Manifest not found |
| `NAME_INVALID` | 400 | Invalid repository name |
| `NAME_UNKNOWN` | 404 | Repository not found |
| `SIZE_INVALID` | 400 | Content size mismatch |
| `TAG_INVALID` | 400 | Invalid tag name |
| `UNAUTHORIZED` | 401 | Authentication required |
| `DENIED` | 403 | Access denied |
| `UNSUPPORTED` | 405 | Operation not supported |
| `TOOMANYREQUESTS` | 429 | Rate limited |

---

## 7. Cloud Functions v2

**Service endpoint:** `https://cloudfunctions.googleapis.com`
**API version:** `v2`
**Base path:** `/v2/projects/{project}/locations/{location}/functions`

### 7.1 Function Resource

```json
{
  "name": "projects/{project}/locations/{location}/functions/{functionId}",
  "description": "My HTTP function",
  "buildConfig": {
    "build": "projects/{project}/locations/{location}/builds/{buildId}",
    "runtime": "nodejs20",
    "entryPoint": "helloWorld",
    "source": {
      "storageSource": {
        "bucket": "gcf-v2-sources-{projectNumber}-{location}",
        "object": "my-function/function-source.zip",
        "generation": "1234567890"
      }
    },
    "sourceProvenance": {
      "resolvedStorageSource": {
        "bucket": "gcf-v2-sources-{projectNumber}-{location}",
        "object": "my-function/function-source.zip",
        "generation": "1234567890"
      }
    },
    "workerPool": "",
    "environmentVariables": {
      "BUILD_VAR": "value"
    },
    "dockerRegistry": "ARTIFACT_REGISTRY",
    "dockerRepository": "projects/{project}/locations/{location}/repositories/gcf-artifacts",
    "serviceAccount": ""
  },
  "serviceConfig": {
    "service": "projects/{project}/locations/{location}/services/{functionId}",
    "timeoutSeconds": 60,
    "availableMemory": "256Mi",
    "availableCpu": "0.1666",
    "environmentVariables": {
      "MY_VAR": "value"
    },
    "maxInstanceCount": 100,
    "minInstanceCount": 0,
    "maxInstanceRequestConcurrency": 1,
    "vpcConnector": "",
    "vpcConnectorEgressSettings": "PRIVATE_RANGES_ONLY",
    "ingressSettings": "ALLOW_ALL",
    "uri": "https://{functionId}-{hash}-{location}.a.run.app",
    "serviceAccountEmail": "{projectNumber}-compute@developer.gserviceaccount.com",
    "allTrafficOnLatestRevision": true,
    "secretEnvironmentVariables": [],
    "secretVolumes": [],
    "revision": "projects/{project}/locations/{location}/services/{functionId}/revisions/{revisionId}"
  },
  "eventTrigger": null,
  "state": "ACTIVE",
  "updateTime": "2024-01-15T10:30:00.000000Z",
  "labels": {
    "deployment-tool": "cli-gcloud"
  },
  "stateMessages": [],
  "environment": "GEN_2",
  "url": "https://{functionId}-{hash}-{location}.a.run.app",
  "kmsKeyName": "",
  "satisfiesPzs": false,
  "createTime": "2024-01-15T10:25:00.000000Z"
}
```

**Function states (enum):** `STATE_UNSPECIFIED`, `ACTIVE`, `FAILED`, `DEPLOYING`, `DELETING`, `UNKNOWN`

**Environment (enum):** `ENVIRONMENT_UNSPECIFIED`, `GEN_1`, `GEN_2`

**Ingress settings:** `INGRESS_SETTINGS_UNSPECIFIED`, `ALLOW_ALL`, `ALLOW_INTERNAL_ONLY`, `ALLOW_INTERNAL_AND_GCLB`

**VPC connector egress:** `VPC_CONNECTOR_EGRESS_SETTINGS_UNSPECIFIED`, `PRIVATE_RANGES_ONLY`, `ALL_TRAFFIC`

**Docker registry:** `DOCKER_REGISTRY_UNSPECIFIED`, `CONTAINER_REGISTRY`, `ARTIFACT_REGISTRY`

**Runtimes (common):** `nodejs18`, `nodejs20`, `nodejs22`, `python39`, `python310`, `python311`, `python312`, `go121`, `go122`, `java11`, `java17`, `java21`, `dotnet6`, `dotnet8`, `ruby32`, `ruby33`, `php82`, `php83`

### 7.2 EventTrigger (for event-driven functions)

```json
{
  "eventTrigger": {
    "trigger": "projects/{project}/locations/{location}/triggers/{triggerId}",
    "triggerRegion": "us-central1",
    "eventType": "google.cloud.pubsub.topic.v1.messagePublished",
    "eventFilters": [
      {
        "attribute": "type",
        "value": "google.cloud.pubsub.topic.v1.messagePublished"
      }
    ],
    "pubsubTopic": "projects/{project}/topics/{topic}",
    "serviceAccountEmail": "{projectNumber}-compute@developer.gserviceaccount.com",
    "retryPolicy": "RETRY_POLICY_DO_NOT_RETRY",
    "channel": "",
    "service": ""
  }
}
```

**Retry policies:** `RETRY_POLICY_UNSPECIFIED`, `RETRY_POLICY_DO_NOT_RETRY`, `RETRY_POLICY_RETRY`

### 7.3 functions.create

```
POST https://cloudfunctions.googleapis.com/v2/projects/{project}/locations/{location}/functions?functionId={functionId}
```

**Query parameters:**
- `functionId` (required): Unique function identifier

**Request body (HTTP-triggered function):**

```json
{
  "description": "My HTTP function",
  "buildConfig": {
    "runtime": "nodejs20",
    "entryPoint": "helloWorld",
    "source": {
      "storageSource": {
        "bucket": "my-source-bucket",
        "object": "function-source.zip"
      }
    }
  },
  "serviceConfig": {
    "availableMemory": "256Mi",
    "timeoutSeconds": 60,
    "maxInstanceCount": 10,
    "minInstanceCount": 0,
    "environmentVariables": {
      "MY_VAR": "value"
    },
    "ingressSettings": "ALLOW_ALL",
    "allTrafficOnLatestRevision": true
  },
  "labels": {"team": "backend"},
  "environment": "GEN_2"
}
```

**Response:** `google.longrunning.Operation` with `response` containing Function resource.

```json
{
  "name": "projects/my-project/locations/us-central1/operations/op-abc",
  "metadata": {
    "@type": "type.googleapis.com/google.cloud.functions.v2.OperationMetadata",
    "createTime": "2024-01-15T10:25:00.000000Z",
    "target": "projects/my-project/locations/us-central1/functions/my-function",
    "verb": "create",
    "requestedCancellation": false,
    "apiVersion": "v2",
    "stages": [
      {
        "name": "ARTIFACT_REGISTRY",
        "message": "",
        "state": "NOT_STARTED"
      },
      {
        "name": "BUILD",
        "message": "",
        "state": "NOT_STARTED"
      },
      {
        "name": "SERVICE",
        "message": "",
        "state": "NOT_STARTED"
      },
      {
        "name": "TRIGGER",
        "message": "",
        "state": "NOT_STARTED"
      }
    ],
    "buildName": ""
  },
  "done": false
}
```

**Common errors:**
- 409 `ALREADY_EXISTS`: Function with same name exists
- 400 `INVALID_ARGUMENT`: Invalid configuration (bad runtime, missing entryPoint)

### 7.4 functions.get

```
GET https://cloudfunctions.googleapis.com/v2/projects/{project}/locations/{location}/functions/{functionId}
```

**Query parameters:**
- `revision` (optional, string): Specific revision to fetch

**Response:** Function resource (see section 7.1).

### 7.5 functions.list

```
GET https://cloudfunctions.googleapis.com/v2/projects/{project}/locations/{location}/functions?pageSize={int}&pageToken={string}&filter={string}&orderBy={string}
```

**Query parameters:**
- `pageSize` (optional, int)
- `pageToken` (optional, string)
- `filter` (optional, string): CEL filter expression
- `orderBy` (optional, string): Sort field

**Response:**

```json
{
  "functions": [
    { ... Function resource ... }
  ],
  "nextPageToken": "",
  "unreachable": []
}
```

### 7.6 functions.delete

```
DELETE https://cloudfunctions.googleapis.com/v2/projects/{project}/locations/{location}/functions/{functionId}
```

**Response:** `google.longrunning.Operation`.

**Common errors:**
- 404 `NOT_FOUND`: Function does not exist

### 7.7 functions.patch

```
PATCH https://cloudfunctions.googleapis.com/v2/projects/{project}/locations/{location}/functions/{functionId}?updateMask={fieldMask}
```

**Query parameters:**
- `updateMask` (required): Comma-separated field paths (e.g., `description,serviceConfig.environmentVariables,labels`)

**Request body:** Function resource with updated fields.

**Example (update environment variables and memory):**

```json
{
  "serviceConfig": {
    "environmentVariables": {
      "NEW_VAR": "new_value"
    },
    "availableMemory": "512Mi"
  }
}
```

```
PATCH ...?updateMask=serviceConfig.environmentVariables,serviceConfig.availableMemory
```

**Response:** `google.longrunning.Operation` with `response` containing updated Function resource.

**Common errors:**
- 404 `NOT_FOUND`: Function does not exist
- 400 `INVALID_ARGUMENT`: Invalid field mask or field values

### 7.8 functions.generateUploadUrl

```
POST https://cloudfunctions.googleapis.com/v2/projects/{project}/locations/{location}/functions:generateUploadUrl
```

**Request body:**

```json
{
  "kmsKeyName": "",
  "environment": "GEN_2"
}
```

**Response:**

```json
{
  "uploadUrl": "https://storage.googleapis.com/uploads-{hash}.{region}.cloudfunctions.appspot.com/{uuid}?GoogleAccessId=...&Expires=...&Signature=...",
  "storageSource": {
    "bucket": "uploads-{hash}.{region}.cloudfunctions.appspot.com",
    "object": "{uuid}",
    "generation": "0"
  }
}
```

**Usage flow:**
1. Call `generateUploadUrl` to get a signed URL
2. Upload the function source ZIP to the signed URL:
   ```
   PUT {uploadUrl}
   Content-Type: application/zip
   x-goog-content-length-range: 0,104857600

   <zip file bytes>
   ```
3. Use the `storageSource` from the response in the `functions.create` or `functions.patch` request body's `buildConfig.source.storageSource`.

### 7.9 HTTP Trigger Invocation

When a function is deployed with an HTTP trigger, it receives a URL in `serviceConfig.uri`.

**Invocation:**

```
POST https://{functionId}-{hash}-{location}.a.run.app
Content-Type: application/json
Authorization: Bearer {id-token-or-access-token}

{arbitrary request body}
```

**Notes for simulator:**
- The function URL is assigned during deployment
- For authenticated functions, require a valid auth header
- For unauthenticated functions (with `allUsers` invoker binding), no auth needed
- The response is whatever the function code returns
- The simulator should execute the function container and return its HTTP response

### 7.10 Operations Endpoint

For polling long-running operations across Cloud Functions v2:

```
GET https://cloudfunctions.googleapis.com/v2/projects/{project}/locations/{location}/operations/{operationId}
```

**Response:** `google.longrunning.Operation` resource.

**List operations:**

```
GET https://cloudfunctions.googleapis.com/v2/projects/{project}/locations/{location}/operations?filter={string}&pageSize={int}&pageToken={string}
```

---

## Appendix A: Monitored Resource Types

For Cloud Logging integration, the simulator should support these resource types:

| Resource Type | Labels | Used By |
|--------------|--------|---------|
| `cloud_run_job` | `project_id`, `job_name`, `location` | Cloud Run Jobs |
| `cloud_run_revision` | `project_id`, `service_name`, `revision_name`, `location`, `configuration_name` | Cloud Run Services |
| `cloud_function` | `project_id`, `function_name`, `region` | Cloud Functions |
| `gcs_bucket` | `project_id`, `bucket_name`, `location` | Cloud Storage |
| `dns_managed_zone` | `project_id`, `zone_name`, `location` | Cloud DNS |
| `global` | `project_id` | General-purpose |

## Appendix B: Timestamp Formats

GCP APIs use two timestamp formats:

1. **RFC 3339 with nanoseconds** (Cloud Run, Cloud Functions, Artifact Registry):
   `"2024-01-15T10:30:00.000000Z"` (always UTC, `Z` suffix)

2. **RFC 3339 without nanoseconds** (Cloud DNS, Cloud Storage):
   `"2024-01-15T10:30:00.000Z"` (millisecond precision)

The simulator should accept and produce timestamps in either format depending on the service.

## Appendix C: Resource Name Patterns

| Service | Resource | Name Pattern |
|---------|----------|-------------|
| Cloud Run Jobs | Job | `projects/{project}/locations/{location}/jobs/{jobId}` |
| Cloud Run Jobs | Execution | `projects/{project}/locations/{location}/jobs/{jobId}/executions/{executionId}` |
| Cloud Run Jobs | Task | `projects/{project}/locations/{location}/jobs/{jobId}/executions/{executionId}/tasks/{taskId}` |
| Cloud Run Jobs | Operation | `projects/{project}/locations/{location}/operations/{operationId}` |
| Cloud Functions | Function | `projects/{project}/locations/{location}/functions/{functionId}` |
| Cloud Functions | Operation | `projects/{project}/locations/{location}/operations/{operationId}` |
| Artifact Registry | Repository | `projects/{project}/locations/{location}/repositories/{repositoryId}` |
| Cloud Logging | Log | `projects/{project}/logs/{logId}` |
| Cloud DNS | ManagedZone | (uses `name` field directly, not resource name pattern) |
| Cloud Storage | Bucket | (uses `name` field directly, not resource name pattern) |
| Cloud Storage | Object | (uses `bucket` + `name` fields) |

## Appendix D: Simulator Implementation Notes

### Priority Order for Implementation

1. **Cloud Storage (GCS)** - Required for function source uploads and general storage
2. **Artifact Registry** - Required for container image references (Docker/OCI)
3. **Cloud Run Jobs** - Core job execution engine
4. **Cloud Functions v2** - Depends on GCS (source) and AR (images)
5. **Cloud Logging** - Cross-cutting concern for all services
6. **Cloud DNS** - Independent, can be implemented last

### Key Behaviors to Simulate

1. **LRO completion**: Cloud Run and Cloud Functions create/delete operations should transition
   from `done: false` to `done: true`. The simulator can do this instantly or after a configurable
   delay.

2. **Execution lifecycle**: When `jobs.run` is called, create an Execution resource with
   `runningCount = taskCount`, then transition to `succeededCount = taskCount` (or `failedCount`
   for failure simulation).

3. **Etag management**: Generate etags on create, update on every mutation. Reject conditional
   requests with mismatched etags (HTTP 412).

4. **Generation counters**: Increment `generation` on every update, track `observedGeneration`
   separately.

5. **UID generation**: Generate unique IDs (UUID v4) for `uid` fields.

6. **Timestamp management**: Set `createTime` on creation, `updateTime` on every mutation,
   `deleteTime` on soft-delete.

7. **Filter parsing**: For Cloud Logging, implement at minimum: equality comparisons, severity
   ordering, timestamp ranges, AND/OR operators, and the has (`:`) operator.

8. **Docker content-addressable storage**: For Artifact Registry, implement proper digest
   verification (SHA-256 of content must match the declared digest).
