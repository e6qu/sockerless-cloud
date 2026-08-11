# AWS API Specification for Sockerless Simulator

This document contains the exact API specifications for each AWS service that the Sockerless
AWS simulator must implement. It covers endpoint formats, request routing, request/response
schemas, and error codes for all required API actions.

---

## Table of Contents

1. [ECS (Elastic Container Service)](#1-ecs-elastic-container-service)
2. [ECR (Elastic Container Registry)](#2-ecr-elastic-container-registry)
3. [CloudWatch Logs](#3-cloudwatch-logs)
4. [EFS (Elastic File System)](#4-efs-elastic-file-system)
5. [Cloud Map (Service Discovery)](#5-cloud-map-service-discovery)
6. [Lambda](#6-lambda)
7. [S3 (Simple Storage Service)](#7-s3-simple-storage-service)
8. [CloudFront](#8-cloudfront) (Phase 159)
9. [ACM (Certificate Manager)](#9-acm-certificate-manager) (Phase 159)
10. [Route 53](#10-route-53) (Phase 159)
11. [WAFv2](#11-wafv2) (Phase 159)
12. [Amplify](#12-amplify) (Phase 159)
13. [IAM extensions — Service-Linked Roles + OIDC](#13-iam-extensions--service-linked-roles--oidc) (Phase 159)

---

## General Notes

### AWS Request Signing
All requests require AWS Signature Version 4 (AWS4-HMAC-SHA256) authentication via the
`Authorization` header. The simulator should validate the presence of auth headers but may
skip cryptographic verification for local development.

### Common Headers (All Services)
```
Authorization: AWS4-HMAC-SHA256 Credential=...
X-Amz-Date: 20230101T000000Z
X-Amz-Security-Token: (optional, for temporary credentials)
```

### Error Response Format

**JSON Services** (ECS, ECR, CloudWatch Logs, Cloud Map):
```json
{
  "__type": "com.amazonaws.servicename#ErrorCode",
  "message": "Human-readable error description"
}
```
The `__type` field uses the format `ServicePrefix#ExceptionName`. The HTTP `Content-Type`
remains `application/x-amz-json-1.1`.

**REST/JSON Services** (Lambda, EFS):
```json
{
  "Type": "User",
  "Code": "ErrorCode",
  "Message": "Human-readable error description"
}
```

**REST/XML Services** (S3):
```xml
<?xml version="1.0" encoding="UTF-8"?>
<Error>
  <Code>ErrorCode</Code>
  <Message>Human-readable error description</Message>
  <Resource>/path/to/resource</Resource>
  <RequestId>request-id</RequestId>
</Error>
```

---

## 1. ECS (Elastic Container Service)

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `ecs.{region}.amazonaws.com` |
| **Protocol** | JSON (POST to `/`) |
| **Content-Type** | `application/x-amz-json-1.1` |
| **Target Prefix** | `AmazonEC2ContainerServiceV20141113` |
| **API Version** | `2014-11-13` |

All ECS API calls use `POST /` with the `X-Amz-Target` header to route to the correct action.

---

### 1.1 CreateCluster

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.CreateCluster`

#### Request

```json
{
  "clusterName": "string",
  "capacityProviders": ["string"],
  "defaultCapacityProviderStrategy": [
    {
      "capacityProvider": "string",
      "weight": 0,
      "base": 0
    }
  ],
  "configuration": {
    "executeCommandConfiguration": {
      "kmsKeyId": "string",
      "logConfiguration": {
        "cloudWatchEncryptionEnabled": false,
        "cloudWatchLogGroupName": "string",
        "s3BucketName": "string",
        "s3EncryptionEnabled": false,
        "s3KeyPrefix": "string"
      },
      "logging": "string"
    },
    "managedStorageConfiguration": {
      "fargateEphemeralStorageKmsKeyId": "string",
      "kmsKeyId": "string"
    }
  },
  "serviceConnectDefaults": {
    "namespace": "string"
  },
  "settings": [
    {
      "name": "string",
      "value": "string"
    }
  ],
  "tags": [
    {
      "key": "string",
      "value": "string"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `clusterName` | String | No | Up to 255 chars. Defaults to `default`. |
| `capacityProviders` | [String] | No | e.g. `FARGATE`, `FARGATE_SPOT` |
| `defaultCapacityProviderStrategy` | [Object] | No | Default capacity provider strategy |
| `configuration` | Object | No | Cluster configuration |
| `serviceConnectDefaults` | Object | No | Service Connect namespace |
| `settings` | [Object] | No | e.g. `containerInsights` |
| `tags` | [Object] | No | Max 50 tags |

#### Response (HTTP 200)

```json
{
  "cluster": {
    "activeServicesCount": 0,
    "attachments": [],
    "attachmentsStatus": "string",
    "capacityProviders": ["string"],
    "clusterArn": "arn:aws:ecs:us-east-1:012345678910:cluster/My-cluster",
    "clusterName": "My-cluster",
    "configuration": {},
    "defaultCapacityProviderStrategy": [],
    "pendingTasksCount": 0,
    "registeredContainerInstancesCount": 0,
    "runningTasksCount": 0,
    "serviceConnectDefaults": {},
    "settings": [],
    "statistics": [],
    "status": "ACTIVE",
    "tags": []
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `InvalidParameterException` | 400 | Invalid parameter value |
| `NamespaceNotFoundException` | 400 | Service Connect namespace not found |
| `ServerException` | 500 | Server-side error |

#### Example

```
POST / HTTP/1.1
Host: ecs.us-east-1.amazonaws.com
X-Amz-Target: AmazonEC2ContainerServiceV20141113.CreateCluster
Content-Type: application/x-amz-json-1.1

{"clusterName": "My-cluster"}
```

```json
{
  "cluster": {
    "activeServicesCount": 0,
    "clusterArn": "arn:aws:ecs:us-east-1:012345678910:cluster/My-cluster",
    "clusterName": "My-cluster",
    "pendingTasksCount": 0,
    "registeredContainerInstancesCount": 0,
    "runningTasksCount": 0,
    "status": "ACTIVE"
  }
}
```

---

### 1.2 DescribeClusters

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.DescribeClusters`

#### Request

```json
{
  "clusters": ["string"],
  "include": ["string"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `clusters` | [String] | No | Up to 100 cluster names or ARNs. Defaults to `default`. |
| `include` | [String] | No | `ATTACHMENTS`, `CONFIGURATIONS`, `SETTINGS`, `STATISTICS`, `TAGS` |

#### Response (HTTP 200)

```json
{
  "clusters": [
    {
      "activeServicesCount": 0,
      "clusterArn": "string",
      "clusterName": "string",
      "pendingTasksCount": 0,
      "registeredContainerInstancesCount": 0,
      "runningTasksCount": 0,
      "status": "ACTIVE",
      "attachments": [],
      "capacityProviders": [],
      "configuration": {},
      "defaultCapacityProviderStrategy": [],
      "settings": [],
      "statistics": [],
      "tags": []
    }
  ],
  "failures": [
    {
      "arn": "string",
      "detail": "string",
      "reason": "string"
    }
  ]
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServerException` | 500 | Server-side error |

---

### 1.3 DeleteCluster

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.DeleteCluster`

#### Request

```json
{
  "cluster": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cluster` | String | **Yes** | Short name or full ARN of cluster to delete |

#### Response (HTTP 200)

Returns the full cluster object (same schema as CreateCluster response) with `"status": "INACTIVE"`.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `ClusterContainsContainerInstancesException` | 400 | Cluster has registered instances |
| `ClusterContainsServicesException` | 400 | Cluster contains services |
| `ClusterContainsTasksException` | 400 | Cluster has active tasks |
| `ClusterNotFoundException` | 400 | Cluster not found |
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServerException` | 500 | Server-side error |

---

### 1.4 RegisterTaskDefinition

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition`

#### Request

```json
{
  "family": "string",
  "containerDefinitions": [
    {
      "name": "string",
      "image": "string",
      "essential": true,
      "cpu": 0,
      "memory": 0,
      "memoryReservation": 0,
      "portMappings": [
        {
          "containerPort": 0,
          "hostPort": 0,
          "protocol": "tcp",
          "name": "string",
          "appProtocol": "string"
        }
      ],
      "environment": [
        {
          "name": "string",
          "value": "string"
        }
      ],
      "mountPoints": [
        {
          "sourceVolume": "string",
          "containerPath": "string",
          "readOnly": false
        }
      ],
      "volumesFrom": [
        {
          "sourceContainer": "string",
          "readOnly": false
        }
      ],
      "logConfiguration": {
        "logDriver": "awslogs",
        "options": {
          "string": "string"
        },
        "secretOptions": [
          {
            "name": "string",
            "valueFrom": "string"
          }
        ]
      },
      "healthCheck": {
        "command": ["string"],
        "interval": 30,
        "timeout": 5,
        "retries": 3,
        "startPeriod": 0
      },
      "command": ["string"],
      "entryPoint": ["string"],
      "workingDirectory": "string",
      "user": "string",
      "privileged": false,
      "readonlyRootFilesystem": false,
      "secrets": [
        {
          "name": "string",
          "valueFrom": "string"
        }
      ],
      "dependsOn": [
        {
          "containerName": "string",
          "condition": "START"
        }
      ],
      "linuxParameters": {
        "capabilities": {
          "add": ["string"],
          "drop": ["string"]
        },
        "initProcessEnabled": false,
        "sharedMemorySize": 0
      },
      "repositoryCredentials": {
        "credentialsParameter": "string"
      },
      "resourceRequirements": [
        {
          "type": "GPU",
          "value": "string"
        }
      ],
      "restartPolicy": {
        "enabled": false,
        "restartAttemptPeriod": 0,
        "ignoredExitCodes": [0]
      }
    }
  ],
  "cpu": "string",
  "memory": "string",
  "networkMode": "awsvpc",
  "taskRoleArn": "string",
  "executionRoleArn": "string",
  "volumes": [
    {
      "name": "string",
      "host": {
        "sourcePath": "string"
      },
      "efsVolumeConfiguration": {
        "fileSystemId": "string",
        "rootDirectory": "string",
        "transitEncryption": "ENABLED",
        "transitEncryptionPort": 0,
        "authorizationConfig": {
          "accessPointId": "string",
          "iam": "ENABLED"
        }
      },
      "configuredAtLaunch": false
    }
  ],
  "placementConstraints": [
    {
      "type": "memberOf",
      "expression": "string"
    }
  ],
  "requiresCompatibilities": ["FARGATE"],
  "runtimePlatform": {
    "cpuArchitecture": "X86_64",
    "operatingSystemFamily": "LINUX"
  },
  "ephemeralStorage": {
    "sizeInGiB": 20
  },
  "tags": [
    {
      "key": "string",
      "value": "string"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `family` | String | **Yes** | Task definition family name, max 255 chars |
| `containerDefinitions` | [Object] | **Yes** | At least one container definition |
| `containerDefinitions[].name` | String | **Yes** | Container name |
| `containerDefinitions[].image` | String | **Yes** | Docker image reference |
| `cpu` | String | Conditional | Required for Fargate. e.g. `"256"`, `"1024"` |
| `memory` | String | Conditional | Required for Fargate. e.g. `"512"`, `"2048"` |
| `networkMode` | String | No | `bridge`, `host`, `awsvpc`, `none` |
| `taskRoleArn` | String | No | IAM role ARN for task |
| `executionRoleArn` | String | No | IAM role ARN for ECS agent |
| `requiresCompatibilities` | [String] | No | `EC2`, `FARGATE`, `EXTERNAL` |

#### Response (HTTP 200)

```json
{
  "taskDefinition": {
    "taskDefinitionArn": "arn:aws:ecs:us-east-1:012345678910:task-definition/hello_world:1",
    "family": "hello_world",
    "revision": 1,
    "status": "ACTIVE",
    "containerDefinitions": [],
    "cpu": "256",
    "memory": "512",
    "networkMode": "awsvpc",
    "taskRoleArn": "string",
    "executionRoleArn": "string",
    "volumes": [],
    "requiresCompatibilities": ["FARGATE"],
    "compatibilities": ["EC2", "FARGATE"],
    "requiresAttributes": [],
    "placementConstraints": [],
    "registeredAt": 1403301078,
    "registeredBy": "arn:aws:iam::012345678910:user/admin",
    "runtimePlatform": {},
    "ephemeralStorage": {}
  },
  "tags": []
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `InvalidParameterException` | 400 | Invalid parameter value |
| `ServerException` | 500 | Server-side error |

---

### 1.5 DeregisterTaskDefinition

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.DeregisterTaskDefinition`

#### Request

```json
{
  "taskDefinition": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `taskDefinition` | String | **Yes** | `family:revision` or full ARN |

#### Response (HTTP 200)

Same schema as RegisterTaskDefinition response, but `"status": "INACTIVE"` and `deregisteredAt` timestamp set.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServerException` | 500 | Server-side error |

#### Example

```
POST / HTTP/1.1
X-Amz-Target: AmazonEC2ContainerServiceV20141113.DeregisterTaskDefinition

{"taskDefinition": "cpu-wave:1"}
```

```json
{
  "taskDefinition": {
    "taskDefinitionArn": "arn:aws:ecs:us-west-2:012345678910:task-definition/cpu-wave:1",
    "family": "cpu-wave",
    "revision": 1,
    "status": "INACTIVE",
    "containerDefinitions": [
      {
        "name": "wave",
        "image": "public.ecr.aws/docker/library/ubuntu:latest",
        "cpu": 50,
        "memory": 100,
        "essential": true
      }
    ],
    "volumes": []
  }
}
```

---

### 1.6 DescribeTaskDefinition

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition`

#### Request

```json
{
  "taskDefinition": "string",
  "include": ["TAGS"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `taskDefinition` | String | **Yes** | `family`, `family:revision`, or full ARN |
| `include` | [String] | No | `TAGS` to include tags |

#### Response (HTTP 200)

```json
{
  "taskDefinition": {
    "taskDefinitionArn": "string",
    "family": "string",
    "revision": 0,
    "status": "ACTIVE",
    "containerDefinitions": [],
    "volumes": [],
    "cpu": "string",
    "memory": "string",
    "networkMode": "string",
    "taskRoleArn": "string",
    "executionRoleArn": "string",
    "requiresCompatibilities": [],
    "registeredAt": 0,
    "registeredBy": "string"
  },
  "tags": []
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServerException` | 500 | Server-side error |

---

### 1.7 RunTask

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.RunTask`

#### Request

```json
{
  "taskDefinition": "string",
  "cluster": "string",
  "count": 1,
  "launchType": "FARGATE",
  "networkConfiguration": {
    "awsvpcConfiguration": {
      "subnets": ["string"],
      "securityGroups": ["string"],
      "assignPublicIp": "ENABLED"
    }
  },
  "overrides": {
    "containerOverrides": [
      {
        "name": "string",
        "command": ["string"],
        "environment": [
          {
            "name": "string",
            "value": "string"
          }
        ],
        "cpu": 0,
        "memory": 0,
        "memoryReservation": 0
      }
    ],
    "cpu": "string",
    "memory": "string",
    "taskRoleArn": "string",
    "executionRoleArn": "string",
    "ephemeralStorage": {
      "sizeInGiB": 0
    }
  },
  "capacityProviderStrategy": [
    {
      "capacityProvider": "string",
      "weight": 0,
      "base": 0
    }
  ],
  "clientToken": "string",
  "enableECSManagedTags": false,
  "enableExecuteCommand": false,
  "group": "string",
  "placementConstraints": [],
  "placementStrategy": [],
  "platformVersion": "string",
  "propagateTags": "TASK_DEFINITION",
  "startedBy": "string",
  "tags": []
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `taskDefinition` | String | **Yes** | `family:revision` or full ARN |
| `cluster` | String | No | Defaults to `default` |
| `count` | Integer | No | 1-10. Default 1. |
| `launchType` | String | No | `EC2`, `FARGATE`, `EXTERNAL` |
| `networkConfiguration` | Object | Conditional | Required for `awsvpc` network mode |
| `overrides` | Object | No | Container/task overrides |
| `clientToken` | String | No | Idempotency token |

#### Response (HTTP 200)

```json
{
  "tasks": [
    {
      "taskArn": "arn:aws:ecs:us-east-1:012345678910:task/my-cluster/1234567890abcdef",
      "clusterArn": "arn:aws:ecs:us-east-1:012345678910:cluster/my-cluster",
      "taskDefinitionArn": "string",
      "containerInstanceArn": "string",
      "desiredStatus": "RUNNING",
      "lastStatus": "PENDING",
      "version": 1,
      "createdAt": 0,
      "startedAt": 0,
      "stoppedAt": 0,
      "containers": [
        {
          "containerArn": "string",
          "taskArn": "string",
          "name": "string",
          "image": "string",
          "lastStatus": "PENDING",
          "exitCode": 0,
          "reason": "string",
          "healthStatus": "UNKNOWN",
          "networkBindings": [],
          "networkInterfaces": [
            {
              "attachmentId": "string",
              "privateIpv4Address": "string",
              "ipv6Address": "string"
            }
          ]
        }
      ],
      "cpu": "string",
      "memory": "string",
      "launchType": "FARGATE",
      "platformVersion": "string",
      "availabilityZone": "string",
      "group": "string",
      "startedBy": "string",
      "attachments": [],
      "tags": [],
      "enableExecuteCommand": false,
      "healthStatus": "UNKNOWN",
      "overrides": {}
    }
  ],
  "failures": [
    {
      "arn": "string",
      "reason": "string",
      "detail": "string"
    }
  ]
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `AccessDeniedException` | 400 | No authorization |
| `BlockedException` | 400 | Account blocked |
| `ClientException` | 400 | Client error |
| `ClusterNotFoundException` | 400 | Cluster not found |
| `ConflictException` | 400 | clientToken reuse conflict |
| `InvalidParameterException` | 400 | Invalid parameter |
| `PlatformTaskDefinitionIncompatibilityException` | 400 | Platform incompatible |
| `PlatformUnknownException` | 400 | Unknown platform version |
| `ServerException` | 500 | Server error |
| `UnsupportedFeatureException` | 400 | Feature not supported in region |

---

### 1.8 DescribeTasks

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.DescribeTasks`

#### Request

```json
{
  "cluster": "string",
  "tasks": ["string"],
  "include": ["TAGS"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tasks` | [String] | **Yes** | Up to 100 task IDs or ARNs |
| `cluster` | String | No | Defaults to `default` |
| `include` | [String] | No | `TAGS` |

#### Response (HTTP 200)

```json
{
  "tasks": [
    {
      "taskArn": "string",
      "clusterArn": "string",
      "taskDefinitionArn": "string",
      "desiredStatus": "string",
      "lastStatus": "string",
      "containers": [],
      "cpu": "string",
      "memory": "string",
      "createdAt": 0,
      "startedAt": 0,
      "stoppedAt": 0,
      "stoppedReason": "string",
      "stopCode": "string",
      "launchType": "string",
      "version": 0,
      "tags": [],
      "overrides": {},
      "attachments": [],
      "availabilityZone": "string",
      "connectivity": "CONNECTED",
      "healthStatus": "UNKNOWN",
      "enableExecuteCommand": false
    }
  ],
  "failures": [
    {
      "arn": "string",
      "detail": "string",
      "reason": "string"
    }
  ]
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `ClusterNotFoundException` | 400 | Cluster not found |
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServerException` | 500 | Server error |

---

### 1.9 ExecuteCommand

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.ExecuteCommand`

#### Request

```json
{
  "cluster": "string",
  "command": "string",
  "container": "string",
  "interactive": true,
  "task": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cluster` | String | No | Cluster ARN or short name; defaults to `default` |
| `command` | String | **Yes** | Command to run in the container |
| `container` | String | No | Required when the task has multiple containers |
| `interactive` | Boolean | **Yes** | Must be `true`; ECS only supports interactive exec sessions |
| `task` | String | **Yes** | Task ARN or ID |

#### Response (HTTP 200)

```json
{
  "clusterArn": "string",
  "containerArn": "string",
  "containerName": "string",
  "interactive": true,
  "session": {
    "sessionId": "string",
    "streamUrl": "string",
    "tokenValue": "string"
  },
  "taskArn": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `AccessDeniedException` | 400 | Caller is not authorized |
| `ClientException` | 400 | Invalid permissions or identifier |
| `ClusterNotFoundException` | 400 | Cluster not found |
| `InvalidParameterException` | 400 | Invalid parameter or exec not enabled on the task |
| `ServerException` | 500 | Server error |
| `TargetNotConnectedException` | 400 | Execute-command target/agent is not connected |

---

### 1.10 StopTask

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.StopTask`

#### Request

```json
{
  "task": "string",
  "cluster": "string",
  "reason": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `task` | String | **Yes** | Full task ARN |
| `cluster` | String | No | Defaults to `default` |
| `reason` | String | No | Reason for stopping |

#### Response (HTTP 200)

```json
{
  "task": {
    "taskArn": "string",
    "clusterArn": "string",
    "taskDefinitionArn": "string",
    "desiredStatus": "STOPPED",
    "lastStatus": "STOPPING",
    "stoppedReason": "string",
    "stopCode": "UserInitiated",
    "containers": [],
    "createdAt": 0,
    "stoppingAt": 0,
    "stoppedAt": 0
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `ClusterNotFoundException` | 400 | Cluster not found |
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServerException` | 500 | Server error |

---

### 1.11 ListTasks

**X-Amz-Target:** `AmazonEC2ContainerServiceV20141113.ListTasks`

#### Request

```json
{
  "cluster": "string",
  "containerInstance": "string",
  "desiredStatus": "RUNNING",
  "family": "string",
  "launchType": "FARGATE",
  "maxResults": 100,
  "nextToken": "string",
  "serviceName": "string",
  "startedBy": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cluster` | String | No | Defaults to `default` |
| `desiredStatus` | String | No | `RUNNING` (default), `PENDING`, `STOPPED` |
| `family` | String | No | Filter by task family |
| `launchType` | String | No | `EC2`, `FARGATE`, `EXTERNAL` |
| `maxResults` | Integer | No | 1-100, default 100 |
| `nextToken` | String | No | Pagination token |
| `serviceName` | String | No | Filter by service |
| `startedBy` | String | No | Filter by starter |

#### Response (HTTP 200)

```json
{
  "taskArns": [
    "arn:aws:ecs:us-east-1:012345678910:task/0b69d5c0-d655-4695-98cd-5d2d526d9d5a"
  ],
  "nextToken": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ClientException` | 400 | Invalid permissions or identifier |
| `ClusterNotFoundException` | 400 | Cluster not found |
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServerException` | 500 | Server error |
| `ServiceNotFoundException` | 400 | Service not found |

---

## 2. ECR (Elastic Container Registry)

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `api.ecr.{region}.amazonaws.com` |
| **Protocol** | JSON (POST to `/`) |
| **Content-Type** | `application/x-amz-json-1.1` |
| **Target Prefix** | `AmazonEC2ContainerRegistry_V20150921` |
| **API Version** | `2015-09-21` |

---

### 2.1 GetAuthorizationToken

**X-Amz-Target:** `AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken`

#### Request

```json
{
  "registryIds": ["string"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `registryIds` | [String] | No | Deprecated. 1-10 AWS account IDs (12-digit). |

#### Response (HTTP 200)

```json
{
  "authorizationData": [
    {
      "authorizationToken": "QVdTOkNpQzErSHF1ZXZPcUR...",
      "expiresAt": 1653026173.652,
      "proxyEndpoint": "https://012345678910.dkr.ecr.us-east-1.amazonaws.com"
    }
  ]
}
```

The `authorizationToken` is a base64-encoded string of `AWS:{password}`, valid for 12 hours.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServerException` | 500 | Server error |

---

### 2.2 CreateRepository

**X-Amz-Target:** `AmazonEC2ContainerRegistry_V20150921.CreateRepository`

#### Request

```json
{
  "repositoryName": "string",
  "registryId": "string",
  "imageTagMutability": "MUTABLE",
  "imageScanningConfiguration": {
    "scanOnPush": false
  },
  "encryptionConfiguration": {
    "encryptionType": "AES256",
    "kmsKey": "string"
  },
  "tags": [
    {
      "Key": "string",
      "Value": "string"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repositoryName` | String | **Yes** | 2-256 chars, lowercase alphanumeric with `.`, `-`, `_`, `/` |
| `registryId` | String | No | 12-digit AWS account ID |
| `imageTagMutability` | String | No | `MUTABLE` (default) or `IMMUTABLE` |
| `imageScanningConfiguration` | Object | No | Scan on push setting |
| `encryptionConfiguration` | Object | No | `AES256` or `KMS` |
| `tags` | [Object] | No | Note: uses `Key`/`Value` (capital K and V) |

#### Response (HTTP 200)

```json
{
  "repository": {
    "repositoryArn": "arn:aws:ecr:us-west-2:012345678910:repository/sample-repo",
    "registryId": "012345678910",
    "repositoryName": "sample-repo",
    "repositoryUri": "012345678910.dkr.ecr.us-west-2.amazonaws.com/sample-repo",
    "createdAt": 1563223656.0,
    "imageTagMutability": "MUTABLE",
    "imageScanningConfiguration": {
      "scanOnPush": false
    },
    "encryptionConfiguration": {
      "encryptionType": "AES256"
    }
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `InvalidTagParameterException` | 400 | Tag key/value too long |
| `LimitExceededException` | 400 | Service quota exceeded |
| `RepositoryAlreadyExistsException` | 400 | Repository exists |
| `ServerException` | 500 | Server error |
| `TooManyTagsException` | 400 | More than 50 tags |

---

### 2.3 DescribeRepositories

**X-Amz-Target:** `AmazonEC2ContainerRegistry_V20150921.DescribeRepositories`

#### Request

```json
{
  "repositoryNames": ["string"],
  "registryId": "string",
  "maxResults": 100,
  "nextToken": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repositoryNames` | [String] | No | 1-100 repository names |
| `registryId` | String | No | 12-digit AWS account ID |
| `maxResults` | Integer | No | 1-1000, default 100. Cannot use with `repositoryNames`. |
| `nextToken` | String | No | Pagination token. Cannot use with `repositoryNames`. |

#### Response (HTTP 200)

```json
{
  "repositories": [
    {
      "repositoryArn": "string",
      "registryId": "string",
      "repositoryName": "string",
      "repositoryUri": "string",
      "createdAt": 0,
      "imageTagMutability": "MUTABLE",
      "imageScanningConfiguration": {
        "scanOnPush": false
      },
      "encryptionConfiguration": {
        "encryptionType": "AES256"
      }
    }
  ],
  "nextToken": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `RepositoryNotFoundException` | 400 | Repository not found |
| `ServerException` | 500 | Server error |

---

### 2.4 DeleteRepository

**X-Amz-Target:** `AmazonEC2ContainerRegistry_V20150921.DeleteRepository`

#### Request

```json
{
  "repositoryName": "string",
  "registryId": "string",
  "force": false
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repositoryName` | String | **Yes** | Repository name |
| `registryId` | String | No | 12-digit account ID |
| `force` | Boolean | No | Force delete even if images exist |

#### Response (HTTP 200)

```json
{
  "repository": {
    "repositoryArn": "string",
    "registryId": "string",
    "repositoryName": "string",
    "repositoryUri": "string",
    "createdAt": 0
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `RepositoryNotFoundException` | 400 | Repository not found |
| `RepositoryNotEmptyException` | 400 | Has images; use `force: true` |
| `ServerException` | 500 | Server error |

---

### 2.5 BatchGetImage

**X-Amz-Target:** `AmazonEC2ContainerRegistry_V20150921.BatchGetImage`

#### Request

```json
{
  "repositoryName": "string",
  "imageIds": [
    {
      "imageDigest": "string",
      "imageTag": "string"
    }
  ],
  "registryId": "string",
  "acceptedMediaTypes": ["string"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repositoryName` | String | **Yes** | Repository name |
| `imageIds` | [Object] | **Yes** | 1-100 image identifiers |
| `registryId` | String | No | 12-digit account ID |
| `acceptedMediaTypes` | [String] | No | 1-100 media types. e.g. `application/vnd.docker.distribution.manifest.v2+json` |

#### Response (HTTP 200)

```json
{
  "images": [
    {
      "registryId": "string",
      "repositoryName": "string",
      "imageId": {
        "imageDigest": "sha256:abc123...",
        "imageTag": "latest"
      },
      "imageManifest": "{...}",
      "imageManifestMediaType": "application/vnd.docker.distribution.manifest.v2+json"
    }
  ],
  "failures": [
    {
      "imageId": {
        "imageDigest": "string",
        "imageTag": "string"
      },
      "failureCode": "string",
      "failureReason": "string"
    }
  ]
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `LimitExceededException` | 400 | Service limit exceeded |
| `RepositoryNotFoundException` | 400 | Repository not found |
| `ServerException` | 500 | Server error |

---

### 2.6 PutImage

**X-Amz-Target:** `AmazonEC2ContainerRegistry_V20150921.PutImage`

#### Request

```json
{
  "repositoryName": "string",
  "imageManifest": "string",
  "imageManifestMediaType": "string",
  "imageTag": "string",
  "imageDigest": "string",
  "registryId": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repositoryName` | String | **Yes** | Repository name |
| `imageManifest` | String | **Yes** | JSON manifest, max 4,194,304 chars |
| `imageManifestMediaType` | String | No | Media type of manifest |
| `imageTag` | String | No | Tag for the image, 1-300 chars |
| `imageDigest` | String | No | Digest of the image |
| `registryId` | String | No | 12-digit account ID |

#### Response (HTTP 200)

```json
{
  "image": {
    "registryId": "string",
    "repositoryName": "string",
    "imageId": {
      "imageDigest": "string",
      "imageTag": "string"
    },
    "imageManifest": "string",
    "imageManifestMediaType": "string"
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ImageAlreadyExistsException` | 400 | Image already pushed |
| `ImageDigestDoesNotMatchException` | 400 | Digest mismatch |
| `ImageTagAlreadyExistsException` | 400 | Tag exists (immutable repo) |
| `InvalidParameterException` | 400 | Invalid parameter |
| `LayersNotFoundException` | 400 | Layers not found |
| `LimitExceededException` | 400 | Quota exceeded |
| `ReferencedImagesNotFoundException` | 400 | Referenced image not found |
| `RepositoryNotFoundException` | 400 | Repository not found |
| `ServerException` | 500 | Server error |

---

### 2.7 DescribeImages

**X-Amz-Target:** `AmazonEC2ContainerRegistry_V20150921.DescribeImages`

#### Request

```json
{
  "repositoryName": "string",
  "registryId": "string",
  "imageIds": [
    {
      "imageDigest": "string",
      "imageTag": "string"
    }
  ],
  "filter": {
    "tagStatus": "TAGGED",
    "imageStatus": "string"
  },
  "maxResults": 100,
  "nextToken": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repositoryName` | String | **Yes** | Repository name |
| `registryId` | String | No | 12-digit account ID |
| `imageIds` | [Object] | No | 1-100 specific images |
| `filter` | Object | No | Filter by tag/image status |
| `maxResults` | Integer | No | 1-1000. Cannot use with `imageIds`. |
| `nextToken` | String | No | Pagination. Cannot use with `imageIds`. |

#### Response (HTTP 200)

```json
{
  "imageDetails": [
    {
      "registryId": "string",
      "repositoryName": "string",
      "imageDigest": "sha256:...",
      "imageTags": ["latest"],
      "imageSizeInBytes": 78447648,
      "imagePushedAt": 1638567369,
      "imageManifestMediaType": "application/vnd.docker.distribution.manifest.v2+json",
      "artifactMediaType": "application/vnd.docker.container.image.v1+json",
      "lastRecordedPullTime": 1645584361514,
      "imageScanStatus": {
        "status": "COMPLETE",
        "description": "string"
      },
      "imageScanFindingsSummary": {
        "imageScanCompletedAt": 0,
        "vulnerabilitySourceUpdatedAt": 0,
        "findingSeverityCounts": {
          "HIGH": 4,
          "MEDIUM": 76
        }
      }
    }
  ],
  "nextToken": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `ImageNotFoundException` | 400 | Image not found |
| `InvalidParameterException` | 400 | Invalid parameter |
| `RepositoryNotFoundException` | 400 | Repository not found |
| `ServerException` | 500 | Server error |

---

## 3. CloudWatch Logs

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `logs.{region}.amazonaws.com` |
| **Protocol** | JSON (POST to `/`) |
| **Content-Type** | `application/x-amz-json-1.1` |
| **Target Prefix** | `Logs_20140328` |
| **API Version** | `2014-03-28` |

---

### 3.1 CreateLogGroup

**X-Amz-Target:** `Logs_20140328.CreateLogGroup`

#### Request

```json
{
  "logGroupName": "string",
  "kmsKeyId": "string",
  "logGroupClass": "STANDARD",
  "tags": {
    "string": "string"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logGroupName` | String | **Yes** | 1-512 chars. Pattern: `[\.\-_/#A-Za-z0-9]+` |
| `kmsKeyId` | String | No | KMS key ARN, max 256 chars |
| `logGroupClass` | String | No | `STANDARD` (default), `INFREQUENT_ACCESS`, `DELIVERY` |
| `tags` | Map | No | Max 50 key-value pairs |

#### Response (HTTP 200)

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `ResourceAlreadyExistsException` | 400 | Log group already exists |
| `LimitExceededException` | 400 | Max log groups reached (1,000,000) |
| `OperationAbortedException` | 400 | Concurrent update conflict |
| `ServiceUnavailableException` | 500 | Service unavailable |

---

### 3.2 DeleteLogGroup

**X-Amz-Target:** `Logs_20140328.DeleteLogGroup`

#### Request

```json
{
  "logGroupName": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logGroupName` | String | **Yes** | 1-512 chars |

#### Response (HTTP 200)

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `OperationAbortedException` | 400 | Concurrent update conflict |
| `ResourceNotFoundException` | 400 | Log group not found |
| `ServiceUnavailableException` | 500 | Service unavailable |

---

### 3.3 DescribeLogGroups

**X-Amz-Target:** `Logs_20140328.DescribeLogGroups`

#### Request

```json
{
  "logGroupNamePrefix": "string",
  "logGroupNamePattern": "string",
  "logGroupClass": "STANDARD",
  "logGroupIdentifiers": ["string"],
  "accountIdentifiers": ["string"],
  "includeLinkedAccounts": false,
  "limit": 50,
  "nextToken": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logGroupNamePrefix` | String | No | 1-512 chars. Mutually exclusive with `logGroupNamePattern`. |
| `logGroupNamePattern` | String | No | 0-512 chars. Mutually exclusive with `logGroupNamePrefix`. |
| `logGroupClass` | String | No | `STANDARD`, `INFREQUENT_ACCESS`, `DELIVERY` |
| `limit` | Integer | No | 1-50, default 50 |
| `nextToken` | String | No | Pagination token (expires after 24 hours) |

#### Response (HTTP 200)

```json
{
  "logGroups": [
    {
      "logGroupName": "string",
      "logGroupArn": "string",
      "arn": "string",
      "creationTime": 1393545600000,
      "retentionInDays": 0,
      "metricFilterCount": 0,
      "storedBytes": 0,
      "kmsKeyId": "string",
      "logGroupClass": "STANDARD",
      "dataProtectionStatus": "string",
      "deletionProtectionEnabled": false
    }
  ],
  "nextToken": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `ServiceUnavailableException` | 500 | Service unavailable |

---

### 3.4 CreateLogStream

**X-Amz-Target:** `Logs_20140328.CreateLogStream`

#### Request

```json
{
  "logGroupName": "string",
  "logStreamName": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logGroupName` | String | **Yes** | 1-512 chars |
| `logStreamName` | String | **Yes** | 1-512 chars. Pattern: `[^:*]*` |

#### Response (HTTP 200)

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `ResourceAlreadyExistsException` | 400 | Stream already exists |
| `ResourceNotFoundException` | 400 | Log group not found |
| `ServiceUnavailableException` | 500 | Service unavailable |

---

### 3.5 PutLogEvents

**X-Amz-Target:** `Logs_20140328.PutLogEvents`

#### Request

```json
{
  "logGroupName": "string",
  "logStreamName": "string",
  "logEvents": [
    {
      "timestamp": 1396035378988,
      "message": "string"
    }
  ],
  "sequenceToken": "string",
  "entity": {
    "attributes": {"string": "string"},
    "keyAttributes": {"string": "string"}
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logGroupName` | String | **Yes** | 1-512 chars |
| `logStreamName` | String | **Yes** | 1-512 chars |
| `logEvents` | [Object] | **Yes** | 1-10,000 events. Max batch 1,048,576 bytes. |
| `logEvents[].timestamp` | Long | **Yes** | Millis since epoch. Must be chronological. |
| `logEvents[].message` | String | **Yes** | Max 1 MB per event |
| `sequenceToken` | String | No | Deprecated/ignored |
| `entity` | Object | No | Entity attributes |

#### Response (HTTP 200)

```json
{
  "nextSequenceToken": "string",
  "rejectedLogEventsInfo": {
    "expiredLogEventEndIndex": 0,
    "tooNewLogEventStartIndex": 0,
    "tooOldLogEventEndIndex": 0
  },
  "rejectedEntityInfo": {
    "errorType": "string"
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `DataAlreadyAcceptedException` | 400 | Now ignored (always accepted) |
| `InvalidParameterException` | 400 | Invalid parameter |
| `InvalidSequenceTokenException` | 400 | Now ignored (always accepted) |
| `ResourceNotFoundException` | 400 | Log group/stream not found |
| `ServiceUnavailableException` | 500 | Service unavailable |
| `UnrecognizedClientException` | 400 | Invalid credentials |

#### Example

```
POST / HTTP/1.1
X-Amz-Target: Logs_20140328.PutLogEvents
Content-Type: application/x-amz-json-1.1

{
  "logGroupName": "my-log-group",
  "logStreamName": "my-log-stream",
  "logEvents": [
    {"timestamp": 1396035378988, "message": "Example event 1"},
    {"timestamp": 1396035378989, "message": "Example event 2"}
  ]
}
```

---

### 3.6 GetLogEvents

**X-Amz-Target:** `Logs_20140328.GetLogEvents`

#### Request

```json
{
  "logGroupName": "string",
  "logGroupIdentifier": "string",
  "logStreamName": "string",
  "startTime": 0,
  "endTime": 0,
  "limit": 10000,
  "nextToken": "string",
  "startFromHead": false,
  "unmask": false
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logStreamName` | String | **Yes** | Log stream name |
| `logGroupName` | String | Conditional | Mutually exclusive with `logGroupIdentifier` |
| `logGroupIdentifier` | String | Conditional | Log group name or ARN |
| `startTime` | Long | No | Millis since epoch |
| `endTime` | Long | No | Millis since epoch |
| `limit` | Integer | No | 1-10,000. Default fits in 1 MB. |
| `nextToken` | String | No | Pagination token |
| `startFromHead` | Boolean | No | `true` = oldest first. Default `false`. |
| `unmask` | Boolean | No | Unmask sensitive data |

#### Response (HTTP 200)

```json
{
  "events": [
    {
      "timestamp": 1396035378988,
      "message": "Example event 1",
      "ingestionTime": 1396035394997
    }
  ],
  "nextBackwardToken": "string",
  "nextForwardToken": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `ResourceNotFoundException` | 400 | Resource not found |
| `ServiceUnavailableException` | 500 | Service unavailable |

---

### 3.7 FilterLogEvents

**X-Amz-Target:** `Logs_20140328.FilterLogEvents`

#### Request

```json
{
  "logGroupName": "string",
  "logGroupIdentifier": "string",
  "logStreamNames": ["string"],
  "logStreamNamePrefix": "string",
  "filterPattern": "string",
  "startTime": 0,
  "endTime": 0,
  "limit": 10000,
  "nextToken": "string",
  "unmask": false
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logGroupName` | String | Conditional | Mutually exclusive with `logGroupIdentifier` |
| `logGroupIdentifier` | String | Conditional | Log group name or ARN |
| `logStreamNames` | [String] | No | 1-100 streams. Mutually exclusive with `logStreamNamePrefix`. |
| `logStreamNamePrefix` | String | No | Prefix filter |
| `filterPattern` | String | No | Filter pattern, 0-1024 chars |
| `startTime` | Long | No | Millis since epoch |
| `endTime` | Long | No | Millis since epoch |
| `limit` | Integer | No | 1-10,000, default 10,000 |
| `nextToken` | String | No | Pagination (expires 24 hours) |

#### Response (HTTP 200)

```json
{
  "events": [
    {
      "eventId": "string",
      "logStreamName": "string",
      "timestamp": 1396035378988,
      "message": "ERROR Event 1",
      "ingestionTime": 1396035394997
    }
  ],
  "nextToken": "string",
  "searchedLogStreams": []
}
```

Note: `searchedLogStreams` is deprecated and returns empty list.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterException` | 400 | Invalid parameter |
| `ResourceNotFoundException` | 400 | Resource not found |
| `ServiceUnavailableException` | 500 | Service unavailable |

---

## 4. EFS (Elastic File System)

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `elasticfilesystem.{region}.amazonaws.com` |
| **Protocol** | REST/JSON |
| **API Version Prefix** | `/2015-02-01` |
| **Content-Type** | `application/json` |

EFS uses a REST API with path-based routing (not X-Amz-Target headers).

---

### 4.1 CreateFileSystem

**Method:** `POST`
**Path:** `/2015-02-01/file-systems`

#### Request

```json
{
  "CreationToken": "string",
  "PerformanceMode": "generalPurpose",
  "ThroughputMode": "bursting",
  "ProvisionedThroughputInMibps": 0,
  "Encrypted": false,
  "KmsKeyId": "string",
  "Backup": false,
  "AvailabilityZoneName": "string",
  "Tags": [
    {
      "Key": "string",
      "Value": "string"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `CreationToken` | String | **Yes** | Idempotency token, 1-64 chars |
| `PerformanceMode` | String | No | `generalPurpose` (default) or `maxIO` |
| `ThroughputMode` | String | No | `bursting` (default), `provisioned`, `elastic` |
| `ProvisionedThroughputInMibps` | Double | Conditional | Required if ThroughputMode is `provisioned` |
| `Encrypted` | Boolean | No | Default `false` |
| `KmsKeyId` | String | No | KMS key ARN (only if Encrypted=true) |
| `Backup` | Boolean | No | Enable automatic backups |
| `AvailabilityZoneName` | String | No | For One Zone file systems |
| `Tags` | [Object] | No | Key-Value tag pairs |

#### Response (HTTP 201 Created)

```json
{
  "FileSystemId": "fs-01234567",
  "FileSystemArn": "arn:aws:elasticfilesystem:us-west-2:251839141158:file-system/fs-01234567",
  "CreationToken": "myFileSystem1",
  "OwnerId": "251839141158",
  "CreationTime": 1403301078,
  "LifeCycleState": "creating",
  "Name": "string",
  "NumberOfMountTargets": 0,
  "SizeInBytes": {
    "Value": 0,
    "Timestamp": 1403301078,
    "ValueInStandard": 0,
    "ValueInIA": 0,
    "ValueInArchive": 0
  },
  "PerformanceMode": "generalPurpose",
  "Encrypted": false,
  "KmsKeyId": "string",
  "ThroughputMode": "bursting",
  "ProvisionedThroughputInMibps": 0,
  "AvailabilityZoneName": "string",
  "AvailabilityZoneId": "string",
  "FileSystemProtection": {
    "ReplicationOverwriteProtection": "ENABLED"
  },
  "Tags": []
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `BadRequest` | 400 | Malformed request or invalid parameter |
| `FileSystemAlreadyExists` | 409 | Same creation token exists |
| `FileSystemLimitExceeded` | 403 | Max file systems reached |
| `InsufficientThroughputCapacity` | 503 | Cannot provision throughput |
| `InternalServerError` | 500 | Server error |
| `ThroughputLimitExceeded` | 400 | Throughput limit exceeded |

---

### 4.2 DescribeFileSystems

**Method:** `GET`
**Path:** `/2015-02-01/file-systems`

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `FileSystemId` | String | No | Specific file system ID |
| `CreationToken` | String | No | Filter by creation token |
| `MaxItems` | Integer | No | Max items, default 100 |
| `Marker` | String | No | Pagination token |

#### Response (HTTP 200)

```json
{
  "FileSystems": [
    {
      "FileSystemId": "fs-01234567",
      "FileSystemArn": "string",
      "CreationToken": "string",
      "OwnerId": "string",
      "CreationTime": 0,
      "LifeCycleState": "available",
      "Name": "string",
      "NumberOfMountTargets": 0,
      "SizeInBytes": {
        "Value": 0,
        "Timestamp": 0,
        "ValueInStandard": 0,
        "ValueInIA": 0,
        "ValueInArchive": 0
      },
      "PerformanceMode": "generalPurpose",
      "Encrypted": false,
      "ThroughputMode": "bursting",
      "Tags": []
    }
  ],
  "Marker": "string",
  "NextMarker": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `BadRequest` | 400 | Invalid parameter |
| `FileSystemNotFound` | 404 | File system not found |
| `InternalServerError` | 500 | Server error |

---

### 4.3 DeleteFileSystem

**Method:** `DELETE`
**Path:** `/2015-02-01/file-systems/{FileSystemId}`

#### URI Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `FileSystemId` | String | **Yes** | File system ID or ARN |

#### Response (HTTP 204 No Content)

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `BadRequest` | 400 | Invalid parameter |
| `FileSystemInUse` | 409 | Has mount targets attached |
| `FileSystemNotFound` | 404 | Not found |
| `InternalServerError` | 500 | Server error |

---

### 4.4 CreateMountTarget

**Method:** `POST`
**Path:** `/2015-02-01/mount-targets`

#### Request

```json
{
  "FileSystemId": "string",
  "SubnetId": "string",
  "IpAddress": "string",
  "SecurityGroups": ["string"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `FileSystemId` | String | **Yes** | File system ID or ARN |
| `SubnetId` | String | **Yes** | VPC subnet ID |
| `IpAddress` | String | No | IPv4 address (auto-assigned if omitted) |
| `SecurityGroups` | [String] | No | VPC security group IDs, max 100 |

#### Response (HTTP 200)

```json
{
  "MountTargetId": "fsmt-01234567",
  "FileSystemId": "fs-01234567",
  "SubnetId": "subnet-01234567",
  "IpAddress": "10.0.2.42",
  "LifeCycleState": "creating",
  "AvailabilityZoneName": "us-west-2a",
  "AvailabilityZoneId": "usw2-az1",
  "NetworkInterfaceId": "eni-12345678",
  "VpcId": "vpc-12345678",
  "OwnerId": "012345678910"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `BadRequest` | 400 | Invalid parameter |
| `FileSystemNotFound` | 404 | File system not found |
| `IncorrectFileSystemLifeCycleState` | 409 | Not in `available` state |
| `IpAddressInUse` | 409 | IP already in use |
| `MountTargetConflict` | 409 | Mount target restriction violated |
| `SubnetNotFound` | 400 | Subnet not found |
| `SecurityGroupNotFound` | 400 | Security group not found |
| `InternalServerError` | 500 | Server error |

---

### 4.5 DescribeMountTargets

**Method:** `GET`
**Path:** `/2015-02-01/mount-targets`

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `FileSystemId` | String | Conditional | One of FileSystemId, AccessPointId, or MountTargetId required |
| `AccessPointId` | String | Conditional | Access point ID |
| `MountTargetId` | String | Conditional | Mount target ID |
| `Marker` | String | No | Pagination token |
| `MaxItems` | Integer | No | Default 10, auto-paginated at 100 |

#### Response (HTTP 200)

```json
{
  "MountTargets": [
    {
      "MountTargetId": "fsmt-01234567",
      "FileSystemId": "fs-01234567",
      "SubnetId": "subnet-01234567",
      "IpAddress": "10.0.2.42",
      "LifeCycleState": "available",
      "AvailabilityZoneName": "string",
      "AvailabilityZoneId": "string",
      "NetworkInterfaceId": "string",
      "VpcId": "string",
      "OwnerId": "string"
    }
  ],
  "Marker": "string",
  "NextMarker": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `BadRequest` | 400 | Invalid parameter |
| `AccessPointNotFound` | 404 | Access point not found |
| `FileSystemNotFound` | 404 | File system not found |
| `MountTargetNotFound` | 404 | Mount target not found |
| `InternalServerError` | 500 | Server error |

---

### 4.6 DeleteMountTarget

**Method:** `DELETE`
**Path:** `/2015-02-01/mount-targets/{MountTargetId}`

#### URI Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `MountTargetId` | String | **Yes** | Mount target ID (e.g. `fsmt-01234567`) |

#### Response (HTTP 204 No Content)

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `BadRequest` | 400 | Invalid parameter |
| `MountTargetNotFound` | 404 | Not found |
| `DependencyTimeout` | 504 | Service timeout |
| `InternalServerError` | 500 | Server error |

---

### 4.7 CreateAccessPoint

**Method:** `POST`
**Path:** `/2015-02-01/access-points`

#### Request

```json
{
  "ClientToken": "string",
  "FileSystemId": "string",
  "PosixUser": {
    "Uid": 0,
    "Gid": 0,
    "SecondaryGids": [0]
  },
  "RootDirectory": {
    "Path": "/data",
    "CreationInfo": {
      "OwnerUid": 0,
      "OwnerGid": 0,
      "Permissions": "755"
    }
  },
  "Tags": [
    {
      "Key": "string",
      "Value": "string"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ClientToken` | String | **Yes** | Idempotency token, 1-64 chars |
| `FileSystemId` | String | **Yes** | File system ID or ARN |
| `PosixUser` | Object | No | POSIX user/group identity |
| `RootDirectory` | Object | No | Root directory and creation info |
| `Tags` | [Object] | No | Tags |

#### Response (HTTP 200)

```json
{
  "AccessPointId": "fsap-01234567",
  "AccessPointArn": "arn:aws:elasticfilesystem:us-west-2:012345678910:access-point/fsap-01234567",
  "ClientToken": "string",
  "FileSystemId": "fs-01234567",
  "LifeCycleState": "creating",
  "Name": "string",
  "OwnerId": "012345678910",
  "PosixUser": {
    "Uid": 0,
    "Gid": 0,
    "SecondaryGids": []
  },
  "RootDirectory": {
    "Path": "/data",
    "CreationInfo": {
      "OwnerUid": 0,
      "OwnerGid": 0,
      "Permissions": "755"
    }
  },
  "Tags": []
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `AccessPointAlreadyExists` | 409 | Same client token exists |
| `AccessPointLimitExceeded` | 403 | Max 10,000 access points |
| `BadRequest` | 400 | Invalid parameter |
| `FileSystemNotFound` | 404 | File system not found |
| `IncorrectFileSystemLifeCycleState` | 409 | Not in `available` state |
| `InternalServerError` | 500 | Server error |
| `ThrottlingException` | 429 | Rate limit exceeded |

---

### 4.8 DescribeAccessPoints

**Method:** `GET`
**Path:** `/2015-02-01/access-points`

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `AccessPointId` | String | No | Specific access point ID |
| `FileSystemId` | String | No | Filter by file system |
| `MaxResults` | Integer | No | Default 100 |
| `NextToken` | String | No | Pagination token |

#### Response (HTTP 200)

```json
{
  "AccessPoints": [
    {
      "AccessPointId": "string",
      "AccessPointArn": "string",
      "ClientToken": "string",
      "FileSystemId": "string",
      "LifeCycleState": "available",
      "Name": "string",
      "OwnerId": "string",
      "PosixUser": {},
      "RootDirectory": {},
      "Tags": []
    }
  ],
  "NextToken": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `AccessPointNotFound` | 404 | Not found |
| `FileSystemNotFound` | 404 | File system not found |
| `BadRequest` | 400 | Invalid parameter |
| `InternalServerError` | 500 | Server error |

---

### 4.9 DeleteAccessPoint

**Method:** `DELETE`
**Path:** `/2015-02-01/access-points/{AccessPointId}`

#### URI Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `AccessPointId` | String | **Yes** | Access point ID or ARN |

#### Response (HTTP 204 No Content)

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `AccessPointNotFound` | 404 | Not found |
| `BadRequest` | 400 | Invalid parameter |
| `InternalServerError` | 500 | Server error |

---

## 5. Cloud Map (Service Discovery)

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `servicediscovery.{region}.amazonaws.com` |
| **DiscoverInstances Endpoint** | `data-servicediscovery.{region}.amazonaws.com` |
| **Protocol** | JSON (POST to `/`) |
| **Content-Type** | `application/x-amz-json-1.1` |
| **Target Prefix** | `Route53AutoNaming_v20170314` |
| **API Version** | `2017-03-14` |

Note: `DiscoverInstances` uses a **different endpoint** (`data-servicediscovery`) from all other operations.

---

### 5.1 CreatePrivateDnsNamespace

**X-Amz-Target:** `Route53AutoNaming_v20170314.CreatePrivateDnsNamespace`

#### Request

```json
{
  "Name": "string",
  "Vpc": "string",
  "CreatorRequestId": "string",
  "Description": "string",
  "Properties": {
    "DnsProperties": {
      "SOA": {
        "TTL": 60
      }
    }
  },
  "Tags": [
    {
      "Key": "string",
      "Value": "string"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Name` | String | **Yes** | Namespace name, max 253 chars |
| `Vpc` | String | **Yes** | VPC ID, max 64 chars |
| `CreatorRequestId` | String | No | Idempotency token, max 64 chars |
| `Description` | String | No | Max 1024 chars |
| `Properties` | Object | No | DNS SOA TTL |
| `Tags` | [Object] | No | Max 50 tags |

#### Response (HTTP 200)

```json
{
  "OperationId": "dns1voqozuhfet5kzxoxg-a-response-example"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidInput` | 400 | Invalid values |
| `NamespaceAlreadyExists` | 400 | Namespace exists |
| `DuplicateRequest` | 400 | Operation in progress |
| `ResourceLimitExceeded` | 400 | Quota exceeded |
| `TooManyTagsException` | 400 | More than 50 tags |

---

### 5.2 GetNamespace

**X-Amz-Target:** `Route53AutoNaming_v20170314.GetNamespace`

#### Request

```json
{
  "Id": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Id` | String | **Yes** | Namespace ID or ARN, max 255 chars |

#### Response (HTTP 200)

```json
{
  "Namespace": {
    "Id": "ns-e4anhexample0004",
    "Arn": "arn:aws:servicediscovery:us-west-2:123456789012:namespace/ns-e4anhexample0004",
    "Name": "example-private-dns.com",
    "Type": "DNS_PRIVATE",
    "Description": "string",
    "ServiceCount": 0,
    "CreateDate": 0,
    "CreatorRequestId": "string",
    "ResourceOwner": "string",
    "Properties": {
      "DnsProperties": {
        "HostedZoneId": "string",
        "SOA": {
          "TTL": 60
        }
      },
      "HttpProperties": {
        "HttpName": "string"
      }
    }
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidInput` | 400 | Invalid values |
| `NamespaceNotFound` | 400 | Namespace not found |

---

### 5.3 DeleteNamespace

**X-Amz-Target:** `Route53AutoNaming_v20170314.DeleteNamespace`

#### Request

```json
{
  "Id": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Id` | String | **Yes** | Namespace ID or ARN |

#### Response (HTTP 200)

```json
{
  "OperationId": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `DuplicateRequest` | 400 | Operation in progress |
| `InvalidInput` | 400 | Invalid values |
| `NamespaceNotFound` | 400 | Namespace not found |
| `ResourceInUse` | 400 | Namespace contains services |

---

### 5.4 CreateService

**X-Amz-Target:** `Route53AutoNaming_v20170314.CreateService`

#### Request

```json
{
  "Name": "string",
  "NamespaceId": "string",
  "CreatorRequestId": "string",
  "Description": "string",
  "Type": "HTTP",
  "DnsConfig": {
    "NamespaceId": "string",
    "RoutingPolicy": "MULTIVALUE",
    "DnsRecords": [
      {
        "Type": "A",
        "TTL": 60
      }
    ]
  },
  "HealthCheckConfig": {
    "Type": "HTTP",
    "ResourcePath": "/",
    "FailureThreshold": 1
  },
  "HealthCheckCustomConfig": {
    "FailureThreshold": 1
  },
  "Tags": []
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Name` | String | **Yes** | Service name |
| `NamespaceId` | String | No | Namespace ID, max 255 chars |
| `CreatorRequestId` | String | No | Idempotency token |
| `Description` | String | No | Max 1024 chars |
| `DnsConfig` | Object | No | DNS configuration |
| `DnsConfig.DnsRecords[].Type` | String | No | `A`, `AAAA`, `SRV`, `CNAME` |
| `DnsConfig.RoutingPolicy` | String | No | `MULTIVALUE` or `WEIGHTED` |
| `HealthCheckConfig` | Object | No | Cannot use with HealthCheckCustomConfig |
| `HealthCheckCustomConfig` | Object | No | Cannot use with HealthCheckConfig |
| `Tags` | [Object] | No | Max 200 tags |

#### Response (HTTP 200)

```json
{
  "Service": {
    "Id": "srv-e4anhexample0004",
    "Arn": "arn:aws:servicediscovery:us-west-2:123456789012:service/srv-e4anhexample0004",
    "Name": "string",
    "NamespaceId": "string",
    "Description": "string",
    "Type": "HTTP",
    "InstanceCount": 0,
    "CreateDate": 0,
    "CreatorRequestId": "string",
    "CreatedByAccount": "string",
    "ResourceOwner": "string",
    "DnsConfig": {},
    "HealthCheckConfig": {},
    "HealthCheckCustomConfig": {}
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidInput` | 400 | Invalid values |
| `NamespaceNotFound` | 400 | Namespace not found |
| `ResourceLimitExceeded` | 400 | Quota exceeded |
| `ServiceAlreadyExists` | 400 | Service with same name exists |
| `TooManyTagsException` | 400 | Too many tags |

---

### 5.5 GetService

**X-Amz-Target:** `Route53AutoNaming_v20170314.GetService`

#### Request

```json
{
  "Id": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Id` | String | **Yes** | Service ID or ARN, max 255 chars |

#### Response (HTTP 200)

Same `Service` object schema as CreateService response.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidInput` | 400 | Invalid values |
| `ServiceNotFound` | 400 | Service not found |

---

### 5.6 DeleteService

**X-Amz-Target:** `Route53AutoNaming_v20170314.DeleteService`

#### Request

```json
{
  "Id": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Id` | String | **Yes** | Service ID or ARN |

#### Response (HTTP 200)

```json
{}
```

Empty body on success.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidInput` | 400 | Invalid values |
| `ResourceInUse` | 400 | Service has registered instances |
| `ServiceNotFound` | 400 | Service not found |

---

### 5.7 RegisterInstance

**X-Amz-Target:** `Route53AutoNaming_v20170314.RegisterInstance`

#### Request

```json
{
  "ServiceId": "string",
  "InstanceId": "string",
  "CreatorRequestId": "string",
  "Attributes": {
    "AWS_INSTANCE_IPV4": "192.0.2.44",
    "AWS_INSTANCE_IPV6": "2001:0db8:85a3::abcd:0001:2345",
    "AWS_INSTANCE_PORT": "80",
    "AWS_ALIAS_DNS_NAME": "string",
    "AWS_EC2_INSTANCE_ID": "string",
    "AWS_INIT_HEALTH_STATUS": "HEALTHY",
    "custom-key": "custom-value"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ServiceId` | String | **Yes** | Service ID or ARN, max 255 chars |
| `InstanceId` | String | **Yes** | Unique ID, max 64 chars, pattern `[0-9a-zA-Z_/:.@-]+` |
| `Attributes` | Map | **Yes** | Max 30 custom attributes, total <= 5,000 chars |
| `CreatorRequestId` | String | No | Idempotency token, max 64 chars |

#### Response (HTTP 200)

```json
{
  "OperationId": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `DuplicateRequest` | 400 | Operation in progress |
| `InvalidInput` | 400 | Invalid values |
| `ResourceInUse` | 400 | Resource conflict |
| `ResourceLimitExceeded` | 400 | Quota exceeded |
| `ServiceNotFound` | 400 | Service not found |

---

### 5.8 DeregisterInstance

**X-Amz-Target:** `Route53AutoNaming_v20170314.DeregisterInstance`

#### Request

```json
{
  "ServiceId": "string",
  "InstanceId": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ServiceId` | String | **Yes** | Service ID or ARN |
| `InstanceId` | String | **Yes** | Instance ID, max 64 chars |

#### Response (HTTP 200)

```json
{
  "OperationId": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `DuplicateRequest` | 400 | Operation in progress |
| `InstanceNotFound` | 400 | Instance not found |
| `InvalidInput` | 400 | Invalid values |
| `ResourceInUse` | 400 | Resource conflict |
| `ServiceNotFound` | 400 | Service not found |

---

### 5.9 DiscoverInstances

**X-Amz-Target:** `Route53AutoNaming_v20170314.DiscoverInstances`

**IMPORTANT:** Uses different endpoint: `data-servicediscovery.{region}.amazonaws.com`

#### Request

```json
{
  "NamespaceName": "string",
  "ServiceName": "string",
  "HealthStatus": "HEALTHY",
  "MaxResults": 100,
  "QueryParameters": {
    "string": "string"
  },
  "OptionalParameters": {
    "string": "string"
  },
  "OwnerAccount": "string"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `NamespaceName` | String | **Yes** | Namespace HttpName, max 1024 chars |
| `ServiceName` | String | **Yes** | Service name |
| `HealthStatus` | String | No | `HEALTHY`, `UNHEALTHY`, `ALL`, `HEALTHY_OR_ELSE_ALL` |
| `MaxResults` | Integer | No | 1-1000, default 100 |
| `QueryParameters` | Map | No | AND filter on attributes |
| `OptionalParameters` | Map | No | OR filter on attributes |
| `OwnerAccount` | String | No | 12-digit account ID for shared namespaces |

#### Response (HTTP 200)

```json
{
  "Instances": [
    {
      "InstanceId": "i-abcd1234",
      "NamespaceName": "example-public-dns.com",
      "ServiceName": "example-dns-pub-service",
      "HealthStatus": "HEALTHY",
      "Attributes": {
        "AWS_INSTANCE_IPV4": "192.0.2.44",
        "AWS_INSTANCE_PORT": "80",
        "color": "green",
        "region": "us-west-2"
      }
    }
  ],
  "InstancesRevision": 2
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidInput` | 400 | Invalid values |
| `NamespaceNotFound` | 400 | Namespace not found |
| `ServiceNotFound` | 400 | Service not found |
| `RequestLimitExceeded` | 400 | Rate limit exceeded |

---

## 6. Lambda

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `lambda.{region}.amazonaws.com` |
| **Protocol** | REST/JSON |
| **API Version Prefix** | `/2015-03-31` |
| **Content-Type** | `application/json` |

Lambda uses a REST API with path-based routing.

---

### 6.1 CreateFunction

**Method:** `POST`
**Path:** `/2015-03-31/functions`

#### Request

```json
{
  "FunctionName": "string",
  "Runtime": "python3.12",
  "Role": "arn:aws:iam::123456789012:role/lambda-role",
  "Handler": "index.handler",
  "Code": {
    "ZipFile": "base64-encoded-bytes",
    "S3Bucket": "string",
    "S3Key": "string",
    "S3ObjectVersion": "string",
    "ImageUri": "string"
  },
  "Description": "string",
  "Timeout": 3,
  "MemorySize": 128,
  "Publish": false,
  "PackageType": "Zip",
  "Environment": {
    "Variables": {
      "string": "string"
    }
  },
  "VpcConfig": {
    "SubnetIds": ["string"],
    "SecurityGroupIds": ["string"]
  },
  "DeadLetterConfig": {
    "TargetArn": "string"
  },
  "TracingConfig": {
    "Mode": "PassThrough"
  },
  "Tags": {
    "string": "string"
  },
  "Layers": ["string"],
  "FileSystemConfigs": [
    {
      "Arn": "string",
      "LocalMountPath": "string"
    }
  ],
  "Architectures": ["x86_64"],
  "EphemeralStorage": {
    "Size": 512
  },
  "LoggingConfig": {
    "LogFormat": "Text",
    "ApplicationLogLevel": "string",
    "SystemLogLevel": "string",
    "LogGroup": "string"
  },
  "KMSKeyArn": "string",
  "CodeSigningConfigArn": "string",
  "ImageConfig": {
    "EntryPoint": ["string"],
    "Command": ["string"],
    "WorkingDirectory": "string"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `FunctionName` | String | **Yes** | 1-140 chars |
| `Code` | Object | **Yes** | One of: `ZipFile`, `S3Bucket`+`S3Key`, or `ImageUri` |
| `Role` | String | **Yes** | IAM execution role ARN |
| `Handler` | String | Conditional | Required for Zip packages. e.g. `index.handler` |
| `Runtime` | String | Conditional | Required for Zip. e.g. `python3.12`, `nodejs20.x` |
| `Timeout` | Integer | No | 1-900 seconds, default 3 |
| `MemorySize` | Integer | No | 128-10240 MB, default 128 |
| `PackageType` | String | No | `Zip` (default) or `Image` |
| `Architectures` | [String] | No | `x86_64` (default) or `arm64` |
| `EphemeralStorage.Size` | Integer | No | 512-10240 MB, default 512 |
| `Environment.Variables` | Map | No | Environment variables |
| `Tags` | Map | No | Key-value tags |

#### Response (HTTP 201 Created)

```json
{
  "FunctionName": "my-function",
  "FunctionArn": "arn:aws:lambda:us-east-1:123456789012:function:my-function",
  "Runtime": "python3.12",
  "Role": "arn:aws:iam::123456789012:role/lambda-role",
  "Handler": "index.handler",
  "CodeSize": 5797206,
  "CodeSha256": "string",
  "Description": "string",
  "Timeout": 3,
  "MemorySize": 128,
  "LastModified": "2023-01-01T00:00:00.000+0000",
  "Version": "$LATEST",
  "State": "Active",
  "StateReason": "string",
  "StateReasonCode": "string",
  "LastUpdateStatus": "Successful",
  "PackageType": "Zip",
  "Architectures": ["x86_64"],
  "EphemeralStorage": {
    "Size": 512
  },
  "Environment": {
    "Variables": {}
  },
  "TracingConfig": {
    "Mode": "PassThrough"
  },
  "Layers": [],
  "LoggingConfig": {},
  "RevisionId": "string"
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `CodeStorageExceededException` | 400 | Max code size exceeded |
| `InvalidParameterValueException` | 400 | Invalid parameter |
| `ResourceConflictException` | 409 | Function already exists |
| `ResourceNotFoundException` | 404 | Resource not found |
| `ServiceException` | 500 | Internal error |
| `TooManyRequestsException` | 429 | Rate limit exceeded |

---

### 6.2 GetFunction

**Method:** `GET`
**Path:** `/2015-03-31/functions/{FunctionName}`

#### URI Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `FunctionName` | String | **Yes** | Function name, ARN, or partial ARN |

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `Qualifier` | String | No | Version or alias, 1-128 chars |

#### Response (HTTP 200)

```json
{
  "Configuration": {
    "FunctionName": "string",
    "FunctionArn": "string",
    "Runtime": "string",
    "Role": "string",
    "Handler": "string",
    "CodeSize": 0,
    "CodeSha256": "string",
    "Description": "string",
    "Timeout": 0,
    "MemorySize": 0,
    "LastModified": "string",
    "Version": "string",
    "State": "Active",
    "LastUpdateStatus": "Successful",
    "PackageType": "Zip",
    "Architectures": ["x86_64"],
    "EphemeralStorage": {"Size": 512},
    "Environment": {"Variables": {}},
    "VpcConfig": {},
    "TracingConfig": {},
    "Layers": [],
    "LoggingConfig": {},
    "RevisionId": "string"
  },
  "Code": {
    "RepositoryType": "S3",
    "Location": "string",
    "ImageUri": "string",
    "ResolvedImageUri": "string"
  },
  "Tags": {},
  "Concurrency": {
    "ReservedConcurrentExecutions": 0
  }
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterValueException` | 400 | Invalid parameter |
| `ResourceNotFoundException` | 404 | Function not found |
| `ServiceException` | 500 | Internal error |
| `TooManyRequestsException` | 429 | Rate limit exceeded |

---

### 6.3 DeleteFunction

**Method:** `DELETE`
**Path:** `/2015-03-31/functions/{FunctionName}`

#### URI Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `FunctionName` | String | **Yes** | Function name or ARN, 1-256 chars |

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `Qualifier` | String | No | Version to delete |

#### Response (HTTP 204 No Content)

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterValueException` | 400 | Invalid parameter |
| `ResourceConflictException` | 409 | Operation in progress |
| `ResourceNotFoundException` | 404 | Function not found |
| `ServiceException` | 500 | Internal error |
| `TooManyRequestsException` | 429 | Rate limit exceeded |

---

### 6.4 UpdateFunctionCode

**Method:** `PUT`
**Path:** `/2015-03-31/functions/{FunctionName}/code`

#### URI Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `FunctionName` | String | **Yes** | Function name or ARN, 1-140 chars |

#### Request

```json
{
  "ZipFile": "base64-encoded-bytes",
  "S3Bucket": "string",
  "S3Key": "string",
  "S3ObjectVersion": "string",
  "ImageUri": "string",
  "Publish": false,
  "DryRun": false,
  "RevisionId": "string",
  "Architectures": ["x86_64"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ZipFile` | Blob | Conditional | Base64-encoded zip. One of ZipFile/S3/ImageUri required. |
| `S3Bucket` | String | Conditional | S3 bucket name |
| `S3Key` | String | Conditional | S3 object key |
| `ImageUri` | String | Conditional | Container image URI |
| `Publish` | Boolean | No | Publish as version 1 |
| `DryRun` | Boolean | No | Validate without updating |
| `RevisionId` | String | No | Optimistic concurrency |
| `Architectures` | [String] | No | `x86_64` or `arm64` |

#### Response (HTTP 200)

Same schema as CreateFunction response (FunctionConfiguration object).

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `CodeStorageExceededException` | 400 | Max code size exceeded |
| `InvalidParameterValueException` | 400 | Invalid parameter |
| `PreconditionFailedException` | 412 | RevisionId mismatch |
| `ResourceConflictException` | 409 | Operation in progress |
| `ResourceNotFoundException` | 404 | Function not found |
| `ServiceException` | 500 | Internal error |
| `TooManyRequestsException` | 429 | Rate limit exceeded |

---

### 6.5 Invoke

**Method:** `POST`
**Path:** `/2015-03-31/functions/{FunctionName}/invocations`

#### URI Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `FunctionName` | String | **Yes** | Function name, ARN, or partial ARN |

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `Qualifier` | String | No | Version or alias |

#### Request Headers

| Header | Required | Description |
|--------|----------|-------------|
| `X-Amz-Invocation-Type` | No | `RequestResponse` (default, sync), `Event` (async), `DryRun` |
| `X-Amz-Log-Type` | No | `None` (default) or `Tail` (include last 4KB of logs) |
| `X-Amz-Client-Context` | No | Base64-encoded client context, max 3583 bytes |

#### Request Body

Binary/JSON payload. Max 6 MB synchronous, 1 MB asynchronous.

```json
{
  "key": "value"
}
```

#### Response

| Invocation Type | HTTP Status |
|-----------------|-------------|
| `RequestResponse` | 200 |
| `Event` | 202 |
| `DryRun` | 204 |

#### Response Headers

| Header | Description |
|--------|-------------|
| `X-Amz-Function-Error` | Present if function error (`Handled` or `Unhandled`) |
| `X-Amz-Log-Result` | Base64-encoded last 4KB of logs (if `X-Amz-Log-Type: Tail`) |
| `X-Amz-Executed-Version` | Actual version executed (when using alias) |

#### Response Body

Function return value (JSON or binary), or error object:

```json
{
  "errorMessage": "string",
  "errorType": "string",
  "stackTrace": ["string"]
}
```

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidParameterValueException` | 400 | Invalid parameter |
| `InvalidRequestContentException` | 400 | Invalid JSON or header |
| `RequestTooLargeException` | 413 | Payload exceeds limit |
| `ResourceNotFoundException` | 404 | Function not found |
| `ResourceNotReadyException` | 502 | Function inactive |
| `TooManyRequestsException` | 429 | Rate limit exceeded |
| `ServiceException` | 500 | Internal error |
| `EC2AccessDeniedException` | 502 | VPC permissions issue |
| `InvalidZipFileException` | 502 | Zip deployment failed |
| `EFSMountFailureException` | 403 | EFS mount error |

---

## 7. S3 (Simple Storage Service)

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint (path-style)** | `s3.{region}.amazonaws.com/{bucket}` |
| **Endpoint (virtual-hosted)** | `{bucket}.s3.{region}.amazonaws.com` |
| **Protocol** | REST (mixed XML/binary) |
| **API Version** | `2006-03-01` |

S3 uses a REST API with path and HTTP method based routing. Request/response bodies may be
XML, binary, or empty depending on the operation. The simulator should support both
virtual-hosted-style and path-style addressing.

---

### 7.1 CreateBucket

**Method:** `PUT`
**Path:** `/` (virtual-hosted: `{bucket}.s3.{region}.amazonaws.com/`)
**Path:** `/{bucket}` (path-style: `s3.{region}.amazonaws.com/{bucket}`)

#### Request Headers

| Header | Required | Description |
|--------|----------|-------------|
| `x-amz-acl` | No | `private`, `public-read`, `public-read-write`, `authenticated-read` |
| `x-amz-bucket-object-lock-enabled` | No | Enable Object Lock |

#### Request Body (XML, optional)

```xml
<?xml version="1.0" encoding="UTF-8"?>
<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
    <LocationConstraint>us-west-2</LocationConstraint>
</CreateBucketConfiguration>
```

The body is optional. If omitted in us-east-1, the bucket is created there.

#### Response (HTTP 200)

| Header | Description |
|--------|-------------|
| `Location` | `/{bucket-name}` |

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `BucketAlreadyExists` | 409 | Bucket name taken globally |
| `BucketAlreadyOwnedByYou` | 409 | You already own this bucket |

#### Example

```
PUT / HTTP/1.1
Host: my-bucket.s3.amazonaws.com
Content-Length: 124

<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
    <LocationConstraint>EU</LocationConstraint>
</CreateBucketConfiguration>
```

```
HTTP/1.1 200 OK
Location: /my-bucket
```

---

### 7.2 HeadBucket

**Method:** `HEAD`
**Path:** `/` (virtual-hosted: `{bucket}.s3.{region}.amazonaws.com/`)

#### Request Headers

| Header | Required | Description |
|--------|----------|-------------|
| `x-amz-expected-bucket-owner` | No | Account ID verification |

#### Response (HTTP 200)

| Header | Description |
|--------|-------------|
| `x-amz-bucket-region` | Region of bucket |
| `x-amz-access-point-alias` | `true` or `false` |

Empty body (HEAD request).

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| (moved) | 301 | Redirect to correct region |
| (bad request) | 400 | Generic error |
| (forbidden) | 403 | No permission |
| `NoSuchBucket` | 404 | Bucket does not exist |

---

### 7.3 PutObject

**Method:** `PUT`
**Path:** `/{Key}` (virtual-hosted: `{bucket}.s3.{region}.amazonaws.com/{key}`)

#### Request Headers

| Header | Required | Description |
|--------|----------|-------------|
| `Content-Type` | No | MIME type of object |
| `Content-Length` | Yes | Size in bytes |
| `Content-MD5` | No | Base64 MD5 digest |
| `Cache-Control` | No | Caching behavior |
| `Content-Disposition` | No | Presentational info |
| `Content-Encoding` | No | Content encoding |
| `x-amz-storage-class` | No | `STANDARD` (default), `REDUCED_REDUNDANCY`, `STANDARD_IA`, etc. |
| `x-amz-server-side-encryption` | No | `AES256`, `aws:kms` |
| `x-amz-tagging` | No | URL-encoded: `key1=value1&key2=value2` |
| `x-amz-meta-*` | No | User metadata headers |
| `x-amz-acl` | No | Canned ACL |

#### Request Body

Binary object data.

#### Response (HTTP 200)

| Header | Description |
|--------|-------------|
| `ETag` | `"hex-md5-digest"` (quoted) |
| `x-amz-version-id` | Version ID (if versioning enabled) |
| `x-amz-server-side-encryption` | Encryption algorithm used |

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `InvalidRequest` | 400 | Invalid parameter/header |
| `AccessDenied` | 403 | No permission |
| `NoSuchBucket` | 404 | Bucket not found |

#### Example

```
PUT /my-image.jpg HTTP/1.1
Host: my-bucket.s3.us-east-1.amazonaws.com
Content-Type: image/jpeg
Content-Length: 11434

[11434 bytes of binary data]
```

```
HTTP/1.1 200 OK
ETag: "1b2cf535f27731c974343645a3985328"
```

---

### 7.4 GetObject

**Method:** `GET`
**Path:** `/{Key}` (virtual-hosted: `{bucket}.s3.{region}.amazonaws.com/{key}`)

#### Request Headers

| Header | Required | Description |
|--------|----------|-------------|
| `Range` | No | Byte range: `bytes=0-999` |
| `If-Match` | No | Return if ETag matches |
| `If-Modified-Since` | No | Return if modified since |
| `If-None-Match` | No | Return if ETag differs |
| `If-Unmodified-Since` | No | Return if not modified since |

#### Query Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `versionId` | No | Specific version |
| `response-content-type` | No | Override Content-Type |
| `response-content-disposition` | No | Override Content-Disposition |
| `response-cache-control` | No | Override Cache-Control |

#### Response (HTTP 200 or 206 for range)

| Header | Description |
|--------|-------------|
| `Content-Type` | Object MIME type |
| `Content-Length` | Object size |
| `ETag` | Entity tag |
| `Last-Modified` | Last modification timestamp |
| `x-amz-version-id` | Version ID |
| `x-amz-storage-class` | Storage class |
| `x-amz-server-side-encryption` | Encryption used |
| `x-amz-meta-*` | User metadata |
| `Content-Range` | For range requests: `bytes 0-999/8000` |
| `accept-ranges` | `bytes` |

Body: Binary object data.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `NoSuchKey` | 404 | Object not found |
| `InvalidObjectState` | 403 | Object archived |
| `AccessDenied` | 403 | No permission |
| `PreconditionFailed` | 412 | Conditional header not met |
| (not modified) | 304 | Object unchanged |

#### Example

```
GET /my-image.jpg HTTP/1.1
Host: my-bucket.s3.us-east-1.amazonaws.com
```

```
HTTP/1.1 200 OK
Content-Type: image/jpeg
Content-Length: 11434
ETag: "1b2cf535f27731c974343645a3985328"
Last-Modified: Wed, 12 Oct 2009 17:50:00 GMT

[11434 bytes of binary data]
```

---

### 7.5 DeleteObject

**Method:** `DELETE`
**Path:** `/{Key}` (virtual-hosted: `{bucket}.s3.{region}.amazonaws.com/{key}`)

#### Request Headers

| Header | Required | Description |
|--------|----------|-------------|
| `x-amz-mfa` | Conditional | For MFA Delete enabled buckets |
| `x-amz-expected-bucket-owner` | No | Account ID verification |

#### Query Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `versionId` | No | Specific version to delete |

#### Response (HTTP 204 No Content)

| Header | Description |
|--------|-------------|
| `x-amz-delete-marker` | `true` if delete marker created |
| `x-amz-version-id` | Version ID of delete marker |

Empty body.

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `AccessDenied` | 403 | No permission |

Note: Deleting a non-existent key returns 204 (not 404).

---

### 7.6 ListObjectsV2

**Method:** `GET`
**Path:** `/?list-type=2` (virtual-hosted: `{bucket}.s3.{region}.amazonaws.com/?list-type=2`)

#### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `list-type` | String | **Yes** | Must be `2` |
| `prefix` | String | No | Filter by key prefix |
| `delimiter` | String | No | Grouping character (typically `/`) |
| `max-keys` | Integer | No | Max 1000, default 1000 |
| `continuation-token` | String | No | Pagination token |
| `start-after` | String | No | Start listing after this key |
| `encoding-type` | String | No | `url` to URL-encode keys |
| `fetch-owner` | Boolean | No | Include owner info |

#### Response (HTTP 200, XML)

```xml
<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
    <Name>bucket-name</Name>
    <Prefix>string</Prefix>
    <Delimiter>string</Delimiter>
    <KeyCount>1</KeyCount>
    <MaxKeys>1000</MaxKeys>
    <IsTruncated>false</IsTruncated>
    <ContinuationToken>string</ContinuationToken>
    <NextContinuationToken>string</NextContinuationToken>
    <StartAfter>string</StartAfter>
    <EncodingType>string</EncodingType>
    <Contents>
        <Key>object-key</Key>
        <LastModified>2023-01-01T00:00:00.000Z</LastModified>
        <ETag>"hex-md5-digest"</ETag>
        <Size>12345</Size>
        <StorageClass>STANDARD</StorageClass>
        <Owner>
            <ID>string</ID>
            <DisplayName>string</DisplayName>
        </Owner>
    </Contents>
    <CommonPrefixes>
        <Prefix>prefix/</Prefix>
    </CommonPrefixes>
</ListBucketResult>
```

Key fields:
- `IsTruncated`: `true` if more results available
- `NextContinuationToken`: Use as `continuation-token` for next request
- `Contents`: Repeated for each object
- `CommonPrefixes`: Repeated for each prefix group (when delimiter is used)

#### Errors

| Error | HTTP | Description |
|-------|------|-------------|
| `NoSuchBucket` | 404 | Bucket does not exist |
| `AccessDenied` | 403 | No `s3:ListBucket` permission |

#### Example

```
GET /?list-type=2&prefix=photos/&delimiter=/&max-keys=10 HTTP/1.1
Host: my-bucket.s3.us-east-1.amazonaws.com
```

```xml
<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
    <Name>my-bucket</Name>
    <Prefix>photos/</Prefix>
    <Delimiter>/</Delimiter>
    <KeyCount>2</KeyCount>
    <MaxKeys>10</MaxKeys>
    <IsTruncated>false</IsTruncated>
    <Contents>
        <Key>photos/photo1.jpg</Key>
        <LastModified>2023-09-17T18:07:53.000Z</LastModified>
        <ETag>"599bab3ed2c697f1d26842727561fd94"</ETag>
        <Size>857</Size>
        <StorageClass>STANDARD</StorageClass>
    </Contents>
    <CommonPrefixes>
        <Prefix>photos/2023/</Prefix>
    </CommonPrefixes>
</ListBucketResult>
```

---

## Appendix A: Service Endpoint Summary

| Service | Endpoint Pattern | Routing | Content-Type |
|---------|-----------------|---------|--------------|
| ECS | `ecs.{region}.amazonaws.com` | `X-Amz-Target` header | `application/x-amz-json-1.1` |
| ECR | `api.ecr.{region}.amazonaws.com` | `X-Amz-Target` header | `application/x-amz-json-1.1` |
| CloudWatch Logs | `logs.{region}.amazonaws.com` | `X-Amz-Target` header | `application/x-amz-json-1.1` |
| Cloud Map | `servicediscovery.{region}.amazonaws.com` | `X-Amz-Target` header | `application/x-amz-json-1.1` |
| Cloud Map (Discover) | `data-servicediscovery.{region}.amazonaws.com` | `X-Amz-Target` header | `application/x-amz-json-1.1` |
| EFS | `elasticfilesystem.{region}.amazonaws.com` | REST path `/2015-02-01/...` | `application/json` |
| Lambda | `lambda.{region}.amazonaws.com` | REST path `/2015-03-31/...` | `application/json` |
| S3 | `{bucket}.s3.{region}.amazonaws.com` | REST path + HTTP method | `application/xml` / binary |

## Appendix B: X-Amz-Target Reference

### ECS (AmazonEC2ContainerServiceV20141113)
```
AmazonEC2ContainerServiceV20141113.CreateCluster
AmazonEC2ContainerServiceV20141113.DescribeClusters
AmazonEC2ContainerServiceV20141113.DeleteCluster
AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition
AmazonEC2ContainerServiceV20141113.DeregisterTaskDefinition
AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition
AmazonEC2ContainerServiceV20141113.RunTask
AmazonEC2ContainerServiceV20141113.DescribeTasks
AmazonEC2ContainerServiceV20141113.StopTask
AmazonEC2ContainerServiceV20141113.ListTasks
```

### ECR (AmazonEC2ContainerRegistry_V20150921)
```
AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken
AmazonEC2ContainerRegistry_V20150921.CreateRepository
AmazonEC2ContainerRegistry_V20150921.DescribeRepositories
AmazonEC2ContainerRegistry_V20150921.DeleteRepository
AmazonEC2ContainerRegistry_V20150921.BatchGetImage
AmazonEC2ContainerRegistry_V20150921.PutImage
AmazonEC2ContainerRegistry_V20150921.DescribeImages
```

### CloudWatch Logs (Logs_20140328)
```
Logs_20140328.CreateLogGroup
Logs_20140328.DeleteLogGroup
Logs_20140328.DescribeLogGroups
Logs_20140328.CreateLogStream
Logs_20140328.PutLogEvents
Logs_20140328.GetLogEvents
Logs_20140328.FilterLogEvents
```

### Cloud Map (Route53AutoNaming_v20170314)
```
Route53AutoNaming_v20170314.CreatePrivateDnsNamespace
Route53AutoNaming_v20170314.GetNamespace
Route53AutoNaming_v20170314.DeleteNamespace
Route53AutoNaming_v20170314.CreateService
Route53AutoNaming_v20170314.GetService
Route53AutoNaming_v20170314.DeleteService
Route53AutoNaming_v20170314.RegisterInstance
Route53AutoNaming_v20170314.DeregisterInstance
Route53AutoNaming_v20170314.DiscoverInstances
```

## Appendix C: REST Path Reference

### EFS (elasticfilesystem.{region}.amazonaws.com)
```
POST   /2015-02-01/file-systems                          CreateFileSystem
GET    /2015-02-01/file-systems                          DescribeFileSystems
DELETE /2015-02-01/file-systems/{FileSystemId}            DeleteFileSystem
POST   /2015-02-01/mount-targets                         CreateMountTarget
GET    /2015-02-01/mount-targets                         DescribeMountTargets
DELETE /2015-02-01/mount-targets/{MountTargetId}          DeleteMountTarget
POST   /2015-02-01/access-points                         CreateAccessPoint
GET    /2015-02-01/access-points                         DescribeAccessPoints
DELETE /2015-02-01/access-points/{AccessPointId}          DeleteAccessPoint
```

### Lambda (lambda.{region}.amazonaws.com)
```
POST   /2015-03-31/functions                             CreateFunction
GET    /2015-03-31/functions/{FunctionName}               GetFunction
DELETE /2015-03-31/functions/{FunctionName}               DeleteFunction
PUT    /2015-03-31/functions/{FunctionName}/code          UpdateFunctionCode
POST   /2015-03-31/functions/{FunctionName}/invocations   Invoke
```

### S3 ({bucket}.s3.{region}.amazonaws.com)
```
PUT    /                                                  CreateBucket
HEAD   /                                                  HeadBucket
PUT    /{Key}                                             PutObject
GET    /{Key}                                             GetObject
DELETE /{Key}                                             DeleteObject
GET    /?list-type=2                                      ListObjectsV2
```

### CloudFront (cloudfront.amazonaws.com)
```
POST   /2020-05-31/distribution                                  CreateDistribution
POST   /2020-05-31/distribution?WithTags                         CreateDistributionWithTags
GET    /2020-05-31/distribution                                  ListDistributions
GET    /2020-05-31/distribution/{Id}                             GetDistribution
GET    /2020-05-31/distribution/{Id}/config                      GetDistributionConfig
PUT    /2020-05-31/distribution/{Id}/config                      UpdateDistribution
DELETE /2020-05-31/distribution/{Id}                             DeleteDistribution
POST   /2020-05-31/origin-access-control                         CreateOriginAccessControl
GET    /2020-05-31/origin-access-control                         ListOriginAccessControls
GET    /2020-05-31/origin-access-control/{Id}                    GetOriginAccessControl
GET    /2020-05-31/origin-access-control/{Id}/config             GetOriginAccessControlConfig
PUT    /2020-05-31/origin-access-control/{Id}/config             UpdateOriginAccessControl
DELETE /2020-05-31/origin-access-control/{Id}                    DeleteOriginAccessControl
POST   /2020-05-31/cache-policy                                  CreateCachePolicy
GET    /2020-05-31/cache-policy                                  ListCachePolicies
GET    /2020-05-31/cache-policy/{Id}                             GetCachePolicy
PUT    /2020-05-31/cache-policy/{Id}                             UpdateCachePolicy
DELETE /2020-05-31/cache-policy/{Id}                             DeleteCachePolicy
POST   /2020-05-31/origin-request-policy                         CreateOriginRequestPolicy
POST   /2020-05-31/response-headers-policy                       CreateResponseHeadersPolicy
POST   /2020-05-31/function                                      CreateFunction
GET    /2020-05-31/function/{Name}/describe                      DescribeFunction
GET    /2020-05-31/function/{Name}                               GetFunction
PUT    /2020-05-31/function/{Name}                               UpdateFunction
POST   /2020-05-31/function/{Name}/publish                       PublishFunction
DELETE /2020-05-31/function/{Name}                               DeleteFunction
POST   /2020-05-31/distribution/{DistId}/invalidation            CreateInvalidation
GET    /2020-05-31/distribution/{DistId}/invalidation            ListInvalidations
GET    /2020-05-31/distribution/{DistId}/invalidation/{Id}       GetInvalidation
POST   /2020-05-31/public-key                                    CreatePublicKey
POST   /2020-05-31/key-group                                     CreateKeyGroup
GET    /2020-05-31/tagging                                       ListTagsForResource
POST   /2020-05-31/tagging?Operation=Tag&Resource={ARN}          TagResource
POST   /2020-05-31/tagging?Operation=Untag&Resource={ARN}        UntagResource
```

### Route 53 (route53.amazonaws.com)
```
POST   /2013-04-01/hostedzone                                    CreateHostedZone
GET    /2013-04-01/hostedzone                                    ListHostedZones
GET    /2013-04-01/hostedzone/{Id}                               GetHostedZone
DELETE /2013-04-01/hostedzone/{Id}                               DeleteHostedZone
POST   /2013-04-01/hostedzone/{Id}/rrset                         ChangeResourceRecordSets
POST   /2013-04-01/hostedzone/{Id}/rrset/                        ChangeResourceRecordSets (CLI form)
GET    /2013-04-01/hostedzone/{Id}/rrset                         ListResourceRecordSets
GET    /2013-04-01/change/{Id}                                   GetChange
GET    /2013-04-01/tags/hostedzone/{Id}                          ListTagsForResource
POST   /2013-04-01/tags/hostedzone/{Id}                          ChangeTagsForResource
```

### Amplify (amplify.{region}.amazonaws.com)
```
POST   /apps                                                     CreateApp
GET    /apps                                                     ListApps
GET    /apps/{AppId}                                             GetApp
POST   /apps/{AppId}                                             UpdateApp
DELETE /apps/{AppId}                                             DeleteApp
POST   /apps/{AppId}/branches                                    CreateBranch
GET    /apps/{AppId}/branches                                    ListBranches
GET    /apps/{AppId}/branches/{BranchName}                       GetBranch
POST   /apps/{AppId}/branches/{BranchName}                       UpdateBranch
DELETE /apps/{AppId}/branches/{BranchName}                       DeleteBranch
POST   /apps/{AppId}/webhooks                                    CreateWebhook
GET    /apps/{AppId}/webhooks                                    ListWebhooks
GET    /webhooks/{WebhookId}                                     GetWebhook
POST   /webhooks/{WebhookId}                                     UpdateWebhook
DELETE /webhooks/{WebhookId}                                     DeleteWebhook
POST   /apps/{AppId}/branches/{BranchName}/jobs                  StartJob
GET    /apps/{AppId}/branches/{BranchName}/jobs                  ListJobs
GET    /apps/{AppId}/branches/{BranchName}/jobs/{JobId}          GetJob
DELETE /apps/{AppId}/branches/{BranchName}/jobs/{JobId}          StopJob
POST   /apps/{AppId}/branches/{BranchName}/deployments           CreateDeployment
POST   /apps/{AppId}/branches/{BranchName}/deployments/start     StartDeployment
POST   /apps/{AppId}/domains                                     CreateDomainAssociation
GET    /apps/{AppId}/domains/{DomainName}                        GetDomainAssociation
DELETE /apps/{AppId}/domains/{DomainName}                        DeleteDomainAssociation
POST   /apps/{AppId}/backendenvironments                         CreateBackendEnvironment
GET    /apps/{AppId}/backendenvironments/{Name}                  GetBackendEnvironment
DELETE /apps/{AppId}/backendenvironments/{Name}                  DeleteBackendEnvironment
POST   /tags/{ARN...}                                            TagResource
GET    /tags/{ARN...}                                            ListTagsForResource
DELETE /tags/{ARN...}                                            UntagResource
```

---

## 8. CloudFront

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `cloudfront.amazonaws.com` (global service, no region segment) |
| **Protocol** | REST + XML |
| **Content-Type** | `application/xml` (request body), `text/xml` (response body) |
| **API Version** | `2020-05-31` |
| **XML Namespace** | `http://cloudfront.amazonaws.com/doc/2020-05-31/` |
| **Concurrency** | Strong ETag + `If-Match` precondition on `PUT /distribution/{Id}/config`, `PUT /origin-access-control/{Id}/config`, and `DELETE` of distributions/OACs/policies/functions/keys/key-groups. |

CloudFront speaks REST + XML, not AWS-JSON. Every body is namespaced XML; every mutation returns an `ETag` header that the next mutator must echo via `If-Match`. The simulator implements this end-to-end.

### Verbs covered

| Subsystem | Verbs |
|---|---|
| **Distribution** | `CreateDistribution`, `CreateDistributionWithTags` (POST `?WithTags`), `ListDistributions`, `GetDistribution`, `GetDistributionConfig`, `UpdateDistribution`, `DeleteDistribution` |
| **OriginAccessControl** | `CreateOriginAccessControl`, `ListOriginAccessControls`, `GetOriginAccessControl`, `GetOriginAccessControlConfig`, `UpdateOriginAccessControl`, `DeleteOriginAccessControl` |
| **CachePolicy / OriginRequestPolicy / ResponseHeadersPolicy** | Full CRUD per type (Create/List/Get/GetConfig/Update/Delete). |
| **CloudFront Functions** | `CreateFunction`, `DescribeFunction`, `GetFunction` (returns raw function bytes, `Content-Type: application/octet-stream`), `UpdateFunction`, `PublishFunction` (DEVELOPMENT → LIVE), `DeleteFunction`. |
| **Invalidations** | `CreateInvalidation`, `ListInvalidations`, `GetInvalidation`. Eager: status is `Completed` immediately. |
| **PublicKey / KeyGroup** | Full CRUD for signed-URL trust. |
| **Tagging** | `ListTagsForResource` (`GET /tagging?Resource=…`), `TagResource` (`POST /tagging?Operation=Tag&Resource=…`), `UntagResource` (`POST /tagging?Operation=Untag&Resource=…`). |

### Validation enforced

- **ACM us-east-1 pin** (`cfValidateViewerCertificate` in `cloudfront.go`): when `DistributionConfig.ViewerCertificate.ACMCertificateArn` is set, the sim looks up the cert in the ACM store and returns `InvalidViewerCertificate` (HTTP 400) if the cert is missing or its ARN region is not `us-east-1`. Real CloudFront rejects with the same error code.
- **DistributionNotDisabled** on delete when `Enabled=true`.
- **PreconditionFailed** (HTTP 412) when `If-Match` is absent or doesn't match the stored ETag.
- **NoSuchDistribution / NoSuchOriginAccessControl / NoSuchCachePolicy / …** (HTTP 404) on unknown ID.

### Error envelope

```xml
<?xml version="1.0" encoding="UTF-8"?>
<ErrorResponse xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Error>
    <Type>Sender</Type>
    <Code>InvalidViewerCertificate</Code>
    <Message>The specified ACM certificate must be in the us-east-1 region for use with CloudFront: arn:aws:acm:us-west-2:...</Message>
  </Error>
  <RequestId>...</RequestId>
</ErrorResponse>
```

### Sim deviations from real AWS

- All distributions are reported `Status: Deployed` immediately. Real CloudFront takes 5–15 minutes to deploy edge configs.
- All invalidations report `Status: Completed` immediately.
- Function `PublishFunction` is synchronous (stage `DEVELOPMENT → LIVE` in one request).

---

## 9. ACM (Certificate Manager)

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `acm.{region}.amazonaws.com` |
| **Protocol** | AWS-JSON 1.1 |
| **Target Prefix** | `CertificateManager` |
| **API Version** | `2015-12-08` |
| **Time format** | Unix epoch as JSON number (`*float64`), not RFC3339. |

### Verbs covered

`RequestCertificate`, `DescribeCertificate`, `DeleteCertificate`, `ListCertificates`, `AddTagsToCertificate`, `RemoveTagsFromCertificate`, `ListTagsForCertificate`, `ImportCertificate`, `UpdateCertificateOptions`, `ResendValidationEmail`, `RenewCertificate`.

### Behaviour

- `RequestCertificate` returns `CertificateArn`; status starts at `PENDING_VALIDATION`. When `ValidationMethod=DNS`, the response includes a synthesised `ResourceRecord` per domain (CNAME `_acm-challenge.{domain}` → `_acm-challenge-{rand}.acm-validations.aws.`).
- `ImportCertificate` returns a cert with `Status=ISSUED` and `Type=IMPORTED` immediately.
- Region pinning enforced cross-resource: CloudFront `ViewerCertificate.ACMCertificateArn` references must resolve to a cert whose ARN region is `us-east-1` (see [§8](#8-cloudfront)).

### Error codes

| Code | When |
|---|---|
| `ResourceNotFoundException` (HTTP 400) | Unknown `CertificateArn`. |
| `InvalidParameterValueException` (HTTP 400) | Missing required fields. |
| `ValidationException` (HTTP 400) | Malformed input. |

---

## 10. Route 53

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `route53.amazonaws.com` (global service, no region segment) |
| **Protocol** | REST + XML |
| **Content-Type** | `application/xml` |
| **API Version** | `2013-04-01` |
| **XML Namespace** | `https://route53.amazonaws.com/doc/2013-04-01/` |

### Verbs covered

`CreateHostedZone`, `ListHostedZones`, `GetHostedZone`, `DeleteHostedZone`, `ChangeResourceRecordSets` (CREATE / UPSERT / DELETE), `ListResourceRecordSets`, `GetChange`, `ListTagsForResource`, `ChangeTagsForResource`.

### Behaviour

- `CreateHostedZone` seeds default `NS` + `SOA` records. Cannot delete a zone with non-seed records (returns `HostedZoneNotEmpty`).
- Names are normalised to trailing dot (`api.tf-route53.local.`) before storage; comparisons honour the dot.
- `ListResourceRecordSets` honours `name` + `type` query params as cursor (required for Terraform's per-record reads to converge).
- `ChangeResourceRecordSets` supports the `AliasTarget { DNSName, HostedZoneId, EvaluateTargetHealth }` block — Route 53 → CloudFront ALIAS is the production-shape integration.
- Both `/rrset` and `/rrset/` are registered (`aws` CLI uses the trailing slash).
- `GetChange` returns `Status=INSYNC` immediately; real Route 53 cycles `PENDING → INSYNC` over ~60s.

### Error codes

| Code | When |
|---|---|
| `NoSuchHostedZone` (HTTP 404) | Unknown zone ID. |
| `InvalidInput` (HTTP 400) | Malformed `ChangeBatch`. |
| `HostedZoneNotEmpty` (HTTP 400) | Delete with non-seed records remaining. |

---

## 11. WAFv2

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `wafv2.us-east-1.amazonaws.com` (real region for CLOUDFRONT scope; scoped resources live at `arn:aws:wafv2:us-east-1:<acct>:global/...`) |
| **Protocol** | AWS-JSON 1.1 |
| **Target Prefix** | `AWSWAF_20190729` |
| **API Version** | `2019-07-29` |
| **Concurrency** | `LockToken` returned on Get/List; required on Update/Delete. |

### Verbs covered (24)

| Subsystem | Verbs |
|---|---|
| **WebACL** | `CreateWebACL`, `GetWebACL`, `UpdateWebACL`, `DeleteWebACL`, `ListWebACLs` |
| **Association** | `AssociateWebACL`, `DisassociateWebACL`, `GetWebACLForResource`, `ListResourcesForWebACL` |
| **IPSet** | `CreateIPSet`, `GetIPSet`, `UpdateIPSet`, `DeleteIPSet`, `ListIPSets` |
| **RuleGroup** | `CreateRuleGroup`, `GetRuleGroup`, `UpdateRuleGroup`, `DeleteRuleGroup`, `ListRuleGroups` |
| **RegexPatternSet** | `CreateRegexPatternSet`, `GetRegexPatternSet`, `UpdateRegexPatternSet`, `DeleteRegexPatternSet`, `ListRegexPatternSets` |
| **Tagging** | `TagResource`, `UntagResource`, `ListTagsForResource` |
| **Inspection** | `GetSampledRequests` (returns empty sample list — sim doesn't actually inspect traffic) |

### ARN shape

```
arn:aws:wafv2:us-east-1:<acct>:global/<resourceType>/<name>/<id>
```

`<resourceType>` is `webacl` / `ipset` / `rulegroup` / `regexpatternset`. The region segment IS literally `us-east-1` and the path includes `global/`; this is real AWS's convention for CLOUDFRONT-scoped resources. The simulator keys its store on `<scope>/<type>/<id>` and resolves ARNs back to this shape.

### Behaviour

- `Rules` and `VisibilityConfig` pass through as `json.RawMessage` (opaque) — the sim doesn't interpret rule statements, but round-trips them byte-for-byte.
- `AssociateWebACL` stores a `resource_arn → web_acl_arn` map (`sync.Map`). `GetWebACLForResource` reads it; `DisassociateWebACL` deletes the entry.
- `LockToken` is regenerated on every mutation; mismatched tokens return `WAFOptimisticLockException`.

### Error codes

| Code | When |
|---|---|
| `WAFNonexistentItemException` (HTTP 400) | Unknown WebACL/IPSet/RuleGroup/RegexPatternSet ID. |
| `WAFOptimisticLockException` (HTTP 400) | LockToken mismatch on Update/Delete. |
| `WAFInvalidParameterException` (HTTP 400) | Missing or malformed required fields. |
| `WAFAssociatedItemException` (HTTP 400) | Delete WebACL while still associated to a resource. |

---

## 12. Amplify

### Service Configuration

| Property | Value |
|----------|-------|
| **Endpoint** | `amplify.{region}.amazonaws.com` |
| **Protocol** | REST + JSON (versionless paths — `/apps`, `/apps/{appId}/branches`, etc.) |
| **Content-Type** | `application/json` |
| **API Version** | `2017-07-25` |

### Verbs covered

| Subsystem | Verbs |
|---|---|
| **App** | `CreateApp`, `ListApps`, `GetApp`, `UpdateApp`, `DeleteApp` |
| **Branch** | `CreateBranch`, `ListBranches`, `GetBranch`, `UpdateBranch`, `DeleteBranch` |
| **Webhook** | `CreateWebhook`, `ListWebhooks`, `GetWebhook`, `UpdateWebhook`, `DeleteWebhook` |
| **Job** | `StartJob`, `ListJobs`, `GetJob`, `StopJob` |
| **Deployment** | `CreateDeployment` (POST `/apps/{appId}/branches/{name}/deployments`), `StartDeployment` (POST `/apps/{appId}/branches/{name}/deployments/start`) |
| **Domain** | `CreateDomainAssociation`, `GetDomainAssociation`, `UpdateDomainAssociation`, `DeleteDomainAssociation`, `ListDomainAssociations` |
| **BackendEnvironment** | `CreateBackendEnvironment`, `ListBackendEnvironments`, `GetBackendEnvironment`, `DeleteBackendEnvironment` |
| **Tagging** | `TagResource`, `ListTagsForResource`, `UntagResource` (all keyed off `/tags/{arn...}`) |

### Behaviour

- `StartJob` synthesises a `SUCCEEDED` job with 3 canonical steps (`PROVISION`, `BUILD`, `DEPLOY`).
- `DeleteApp` cascades to associated branches + webhooks + jobs.
- `CreateDomainAssociation` eagerly sets `DomainStatus=AVAILABLE` and `UpdateStatus=UPDATE_COMPLETE` and synthesises CNAME DNS records for each sub-domain. Real Amplify cycles through `PENDING_DEPLOYMENT → AVAILABLE` over minutes.
- Custom rules pass through as `json.RawMessage`.

### Error codes

| Code | When |
|---|---|
| `NotFoundException` (HTTP 404) | Unknown app / branch / webhook / domain. |
| `BadRequestException` (HTTP 400) | Missing required fields. |
| `LimitExceededException` (HTTP 400) | Reserved; not currently emitted by sim. |

---

## 13. IAM extensions — Service-Linked Roles + OIDC

These are layered on top of the existing IAM (AWS Query Protocol) surface. See [§Appendix A](#appendix-a-error-code-tables) for the shared IAM Query envelope.

### Verbs covered

**Service-Linked Roles:**
- `CreateServiceLinkedRole`
- `DeleteServiceLinkedRole`
- `GetServiceLinkedRoleDeletionStatus`

**OIDC providers:**
- `CreateOpenIDConnectProvider`
- `GetOpenIDConnectProvider`
- `UpdateOpenIDConnectProviderThumbprint`
- `AddClientIDToOpenIDConnectProvider`
- `RemoveClientIDFromOpenIDConnectProvider`
- `DeleteOpenIDConnectProvider`
- `ListOpenIDConnectProviders`

### Behaviour

- `CreateServiceLinkedRole` maps `AWSServiceName` to a canonical name: `cloudfront.amazonaws.com → AWSServiceRoleForCloudFrontLogger`, `amplify.amazonaws.com → AWSServiceRoleForAmplify`, `ecs.amazonaws.com → AWSServiceRoleForECS`, etc. Unknown principals fall back to a PascalCase derivation.
- SLR ARN: `arn:aws:iam::<acct>:role/aws-service-role/<service-principal>/<name>`.
- A shadow `IAMRole` record is also written into the standard `iamRoles` store so the Terraform aws_iam_service_linked_role.Read path (which calls `GetRole`, not `GetServiceLinkedRole`) finds the role.
- `DeleteServiceLinkedRole` returns a `DeletionTaskId`; `GetServiceLinkedRoleDeletionStatus` returns `SUCCEEDED` immediately. Real AWS cycles `IN_PROGRESS → SUCCEEDED` over seconds.
- OIDC ARN: `arn:aws:iam::<acct>:oidc-provider/<url-without-scheme>`.
- `AddClientIDToOpenIDConnectProvider` is idempotent (dedupes existing client IDs).
- AWS Query Protocol encodes lists as `ClientIDList.member.1=...&ClientIDList.member.2=...`; the sim parses both forms.

### Error codes

| Code | When |
|---|---|
| `NoSuchEntity` (HTTP 404) | Unknown role / OIDC provider. |
| `EntityAlreadyExists` (HTTP 409) | OIDC provider with the same URL already exists. |
| `InvalidInput` (HTTP 400) | Malformed input (missing URL/Thumbprint, etc.). |
