# Cross-simulator feature parity matrix

Source of truth for which cloud-API calls each simulator implements at the fidelity sockerless requires. The backend-dependency tables enumerate every cloud-API call made by one of the seven backends. The cloud-slice inventory links every simulator service to its complete registered-operation table and to its official SDK, vendor CLI, and Terraform-provider evidence.

Legend:
- ✓ — sim implements the call at sockerless's required fidelity (used by integration tests; passes against the sim).
- ⚠ — sim implements the call but with reduced fidelity (e.g. incomplete field coverage, missing pagination, or ignored filters). Filed as a bug.
- ✗ — sim does not implement the call. Filed as a bug.
- — — call is not made by any backend pointed at this cloud (not applicable).

**Standing rules:** any new SDK call added to a backend must update this matrix and add the sim handler in the same commit (PLAN.md principle #10). Every simulator service slice must appear in the cloud-slice inventory; CI compares it with `specs/SIM_SURFACE_TABLES/`. Every ⚠ / ✗ row is a bug that gets a real fix in the same session per the no-defer rule.

## AWS

Backends: ECS (Fargate), Lambda. Sim: `simulator-aws/`. **33/33 ✓.**

| Service | Method | Used by | Sim status | Notes |
|---|---|---|---|---|
| ECS | DescribeClusters | ECS | ✓ | `handleECSDescribeClusters` (ecs.go:281) |
| ECS | DescribeTasks | ECS | ✓ | `handleECSDescribeTasks` (ecs.go:833) |
| ECS | ListTasks | ECS | ✓ | `handleECSListTasks` (ecs.go:930) |
| ECS | RunTask | ECS | ✓ | `handleECSRunTask` (ecs.go:473) — full task-def + VPC config |
| ECS | StopTask | ECS | ✓ | `handleECSStopTask` (ecs.go:873) |
| ECS | RegisterTaskDefinition | ECS | ✓ | revision tracking |
| ECS | DeregisterTaskDefinition | ECS | ✓ | |
| ECS | TagResource | ECS | ✓ | `handleECSTagResource` + `handleECSUntagResource` + `mergeECSTagsByKey` helper; rejects STOPPED / DEPROVISIONING tasks like real ECS. |
| ECS | ListTagsForResource | ECS | ✓ | `handleECSListTagsForResource` (ecs.go:1020) |
| ECS | ExecuteCommand | ECS | ✓ | sim parity via WebSocket + SSM AgentMessage frame writer + exit-code marker |
| Lambda | CreateFunction | Lambda | ✓ | tags + VpcConfig + ImageConfig |
| Lambda | DeleteFunction | Lambda | ✓ | |
| Lambda | Invoke | Lambda | ✓ | container-based exec via Runtime API sidecar |
| Lambda | UpdateFunctionConfiguration | Lambda | ✓ | |
| Lambda | TagResource | Lambda | ✓ | InvocationResult persisted to tags for `docker wait` exit-code recovery. |
| Lambda | ListTags | Lambda | ✓ | |
| ECR | CreatePullThroughCacheRule | ECS, Lambda | ✓ | |
| ECR | DescribePullThroughCacheRules | ECS, Lambda | ✓ | filters by prefix |
| ECR | CreateRepository | aws-common | ✓ | |
| ECR | BatchDeleteImage | aws-common | ✓ | Surfaces real errors via the ImageManager.Remove aggregator. |
| ECR | GetAuthorizationToken | aws-common | ✓ | |
| CloudWatch Logs | DescribeLogStreams | ECS, Lambda | ✓ | |
| CloudWatch Logs | GetLogEvents | ECS, Lambda | ✓ | pagination |
| EFS | DescribeFileSystems / CreateFileSystem / CreateMountTarget / DescribeMountTargets / CreateAccessPoint | aws-common | ✓ | All five EFS calls under `EnsureFilesystem` helper |
| ServiceDiscovery (Cloud Map) | CreatePrivateDnsNamespace | ECS | ✓ | also creates Docker network for sim cross-talk |
| ServiceDiscovery | DeleteNamespace | ECS | ✓ | |
| ServiceDiscovery | GetNamespace | ECS | ✓ | |
| ServiceDiscovery | ListNamespaces | ECS | ✓ | |
| ServiceDiscovery | CreateService | ECS | ✓ | |
| ServiceDiscovery | DeleteService | ECS | ✓ | |
| ServiceDiscovery | ListServices | ECS | ✓ | filters by namespace |
| ServiceDiscovery | RegisterInstance | ECS | ✓ | |
| ServiceDiscovery | DeregisterInstance | ECS | ✓ | |
| ServiceDiscovery | ListInstances | ECS | ✓ | |
| ServiceDiscovery | DiscoverInstances | ECS | ✓ | DNS discovery |
| ServiceDiscovery | ListTagsForResource | ECS | ✓ | |
| ServiceDiscovery | GetOperation | ECS | ✓ | |

### AWS simulator cloud-slice inventory

The backend table above answers “can a sockerless backend make every cloud call it needs?” This inventory answers the broader question “which AWS public API slices does the simulator expose, and where is every registered operation tracked?” The linked surface tables currently enumerate **42 AWS surfaces and 2,695 registered operations**. Each operation row names its handler and test status. [`SIM_TEST_COVERAGE_MATRIX.md`](SIM_TEST_COVERAGE_MATRIX.md) supplies the official AWS SDK, AWS CLI, and Terraform AWS Provider evidence for every surface.

The inventory is intentionally exhaustive rather than customer-report-driven. It includes AWS Amplify, AWS WAF, AWS Step Functions, Amazon DynamoDB, Amazon Route 53, Amazon Relational Database Service (RDS), AWS Secrets Manager, AWS CodeBuild, Amazon CloudWatch, Amazon EventBridge, AWS CloudTrail, Amazon Simple Notification Service (SNS), Amazon Simple Queue Service (SQS), and every other registered AWS cloud slice.

| AWS cloud slice | Per-operation inventory | External client evidence |
|---|---|---|
| AWS Certificate Manager (ACM) | [`aws-acm`](SIM_SURFACE_TABLES/aws-acm.md) | [`aws-acm`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Certificate Manager ACME data plane | [`aws-acm_acme`](SIM_SURFACE_TABLES/aws-acm_acme.md) | [`aws-acm_acme`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Private Certificate Authority | [`aws-acmpca`](SIM_SURFACE_TABLES/aws-acmpca.md) | [`aws-acmpca`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Amplify | [`aws-amplify`](SIM_SURFACE_TABLES/aws-amplify.md) | [`aws-amplify`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon API Gateway REST APIs | [`aws-apigateway`](SIM_SURFACE_TABLES/aws-apigateway.md) | [`aws-apigateway`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon API Gateway V2 APIs | [`aws-apigatewayv2`](SIM_SURFACE_TABLES/aws-apigatewayv2.md) | [`aws-apigatewayv2`](SIM_TEST_COVERAGE_MATRIX.md) |
| Application Auto Scaling | [`aws-application-autoscaling`](SIM_SURFACE_TABLES/aws-application-autoscaling.md) | [`aws-application-autoscaling`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon EC2 Auto Scaling | [`aws-autoscaling`](SIM_SURFACE_TABLES/aws-autoscaling.md) | [`aws-autoscaling`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Batch | [`aws-batch`](SIM_SURFACE_TABLES/aws-batch.md) | [`aws-batch`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Budgets | [`aws-budgets`](SIM_SURFACE_TABLES/aws-budgets.md) | [`aws-budgets`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Cloud Map | [`aws-cloudmap`](SIM_SURFACE_TABLES/aws-cloudmap.md) | [`aws-cloudmap`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS CloudTrail | [`aws-cloudtrail`](SIM_SURFACE_TABLES/aws-cloudtrail.md) | [`aws-cloudtrail`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon CloudWatch | [`aws-cloudwatch`](SIM_SURFACE_TABLES/aws-cloudwatch.md) | [`aws-cloudwatch`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS CodeBuild | [`aws-codebuild`](SIM_SURFACE_TABLES/aws-codebuild.md) | [`aws-codebuild`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon DynamoDB | [`aws-dynamodb`](SIM_SURFACE_TABLES/aws-dynamodb.md) | [`aws-dynamodb`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Elastic Compute Cloud (EC2) | [`aws-ec2`](SIM_SURFACE_TABLES/aws-ec2.md) | [`aws-ec2`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Elastic Container Registry (ECR) | [`aws-ecr`](SIM_SURFACE_TABLES/aws-ecr.md) | [`aws-ecr`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Elastic Container Service (ECS) | [`aws-ecs`](SIM_SURFACE_TABLES/aws-ecs.md) | [`aws-ecs`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Elastic File System (EFS) | [`aws-efs`](SIM_SURFACE_TABLES/aws-efs.md) | [`aws-efs`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon ElastiCache | [`aws-elasticache`](SIM_SURFACE_TABLES/aws-elasticache.md) | [`aws-elasticache`](SIM_TEST_COVERAGE_MATRIX.md) |
| Elastic Load Balancing V2 | [`aws-elbv2`](SIM_SURFACE_TABLES/aws-elbv2.md) | [`aws-elbv2`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon EventBridge | [`aws-eventbridge`](SIM_SURFACE_TABLES/aws-eventbridge.md) | [`aws-eventbridge`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Data Firehose | [`aws-firehose`](SIM_SURFACE_TABLES/aws-firehose.md) | [`aws-firehose`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Glue | [`aws-glue`](SIM_SURFACE_TABLES/aws-glue.md) | [`aws-glue`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Identity and Access Management (IAM) | [`aws-iam`](SIM_SURFACE_TABLES/aws-iam.md) | [`aws-iam`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Kinesis Data Streams | [`aws-kinesis`](SIM_SURFACE_TABLES/aws-kinesis.md) | [`aws-kinesis`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Key Management Service (KMS) | [`aws-kms`](SIM_SURFACE_TABLES/aws-kms.md) | [`aws-kms`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Lambda | [`aws-lambda`](SIM_SURFACE_TABLES/aws-lambda.md) | [`aws-lambda`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Organizations | [`aws-organizations`](SIM_SURFACE_TABLES/aws-organizations.md) | [`aws-organizations`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Relational Database Service (RDS) | [`aws-rds`](SIM_SURFACE_TABLES/aws-rds.md) | [`aws-rds`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Route 53 | [`aws-route53`](SIM_SURFACE_TABLES/aws-route53.md) | [`aws-route53`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Simple Storage Service (S3) bucket subresources | [`aws-s3-bucket-subresources`](SIM_SURFACE_TABLES/aws-s3-bucket-subresources.md) | [`aws-s3-bucket-subresources`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon S3 multipart upload | [`aws-s3-multipart`](SIM_SURFACE_TABLES/aws-s3-multipart.md) | [`aws-s3-multipart`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Simple Storage Service (S3) | [`aws-s3`](SIM_SURFACE_TABLES/aws-s3.md) | [`aws-s3`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon EventBridge Scheduler | [`aws-scheduler`](SIM_SURFACE_TABLES/aws-scheduler.md) | [`aws-scheduler`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Secrets Manager | [`aws-secretsmanager`](SIM_SURFACE_TABLES/aws-secretsmanager.md) | [`aws-secretsmanager`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Simple Notification Service (SNS) | [`aws-sns`](SIM_SURFACE_TABLES/aws-sns.md) | [`aws-sns`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Simple Queue Service (SQS) | [`aws-sqs`](SIM_SURFACE_TABLES/aws-sqs.md) | [`aws-sqs`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Systems Manager Parameter Store | [`aws-ssm_parameters`](SIM_SURFACE_TABLES/aws-ssm_parameters.md) | [`aws-ssm_parameters`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Step Functions | [`aws-stepfunctions`](SIM_SURFACE_TABLES/aws-stepfunctions.md) | [`aws-stepfunctions`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Security Token Service (STS) | [`aws-sts`](SIM_SURFACE_TABLES/aws-sts.md) | [`aws-sts`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS WAF | [`aws-wafv2`](SIM_SURFACE_TABLES/aws-wafv2.md) | [`aws-wafv2`](SIM_TEST_COVERAGE_MATRIX.md) |

## GCP

Backends: Cloud Run Jobs (cloudrun), Cloud Run Functions (cloudrun-functions). Sim: `simulator-gcp/`. **16/16 ✓ (current backends) + 8 forward-looking rows for Phase 126/127 prep, all ✓.**

| Service | Method | Used by | Sim status | Notes |
|---|---|---|---|---|
| Cloud Run | Jobs.CreateJob | cloudrun | ✓ | `registerCloudRunJobs` (cloudrunjobs.go:225) — full LRO + job metadata |
| Cloud Run | Jobs.DeleteJob | cloudrun | ✓ | (cloudrunjobs.go:317) — cascades execution delete |
| Cloud Run | Jobs.ListJobs | cloudrun | ✓ | (cloudrunjobs.go:302) — filters by project/location prefix |
| Cloud Run | Jobs.RunJob | cloudrun | ✓ | (cloudrunjobs.go:344) — creates execution with task metadata |
| Cloud Run | Executions.GetExecution | cloudrun | ✓ | (cloudrunjobs.go:539) — full execution state |
| Cloud Run | Executions.CancelExecution | cloudrun | ✓ | (cloudrunjobs.go:571) — stops container + injects cancel log |
| Cloud Run | Services.CreateService | cloudrun (UseService) | ✓ | v2 REST routes in `simulator-gcp/cloudrunservices.go::registerCloudRunServicesV2` covering Create/Get/List/Update/Delete on `/v2/projects/{p}/locations/{l}/services`. Returns proto-JSON shape `runpb.Service` expects (TerminalCondition=CONDITION_SUCCEEDED, LatestReadyRevision populated, generation as int64-string). |
| Cloud Run | Services.GetService | cloudrun (UseService) | ✓ | (cloudrunservices.go) — service_discovery_cloud.go uses this for CNAME resolution |
| Cloud Run | Services.UpdateService | cloudrun (declarative) | ✓ | (cloudrunservices.go) — terraform `google_cloud_run_v2_service` parity; backend recreates rather than patches today |
| Cloud Run | Services.DeleteService | cloudrun (UseService) | ✓ | (cloudrunservices.go) — LRO + store delete |
| Cloud Functions | CreateFunction | cloudrun-functions | ✓ | (cloudfunctions.go:57) — full LRO + function URI |
| Cloud Functions | DeleteFunction | cloudrun-functions | ✓ | (cloudfunctions.go:181) — LRO |
| Cloud Functions | ListFunctions | cloudrun-functions | ✓ | (cloudfunctions.go:114) — filters by project/location prefix |
| Cloud Logging | LogAdmin.Entries | cloudrun, cloudrun-functions | ✓ | (logging.go:151) — REST ListLogEntries with filter + pageSize |
| Cloud DNS | ManagedZones | cloudrun, cloudrun-functions | ✓ | (dns.go:44/96/114/128) — Create/Get/List/Delete + Docker network backing for private zones |
| Cloud DNS | ResourceRecordSets | cloudrun, cloudrun-functions | ✓ | (dns.go:159/190/236) — List/Create/Delete + Docker network connection for A records |

### Phase 126/127 forward-looking (no current backend caller; SDK-test-validated)

| Service | Method | Phase | Sim status | Notes |
|---|---|---|---|---|
| IAM Credentials | ServiceAccounts.GenerateIdToken | 126 (Access driver `id-token`) | ✓ | `simulator-gcp/iam.go` — `:emailAction` switch handles `:generateIdToken` alongside existing `:generateAccessToken`. Mints HS256 JWT via `mintSimIdToken` in `oauth2.go`; `aud` claim equals request audience; `email` claim included when `includeEmail=true`. SDK test: `iam_test.go::TestIAMCredentials_GenerateIdToken*`. |
| Compute | Disks.Insert | 127 (Storage `pd-ephemeral`) | ✓ | `simulator-gcp/compute.go::registerComputeDisks`. Default Type `pd-standard` when unset. Returns zonal LRO. |
| Compute | Disks.Get | 127 | ✓ | (compute.go) |
| Compute | Disks.List | 127 | ✓ | (compute.go) — zonal |
| Compute | Disks.Delete | 127 | ✓ | (compute.go) — 404 on missing |
| Compute | Disks.Resize | 127 | ✓ | (compute.go) — `DisksResizeRequest{SizeGb}` |
| Compute | Disks.SetLabels | 127 | ✓ | (compute.go) — refreshes `LabelFingerprint` |
| Compute | Disks.AggregatedList | 127 | ✓ | (compute.go) — `compute#diskAggregatedList` shape with `zones/<zone>` keys |

## Azure

Backends: Container Apps (aca), Azure Functions (azure-functions). Sim: `simulator-azure/`. **28/28 ✓.**

| Service | Method | Used by | Sim status | Notes |
|---|---|---|---|---|
| Container Apps | Jobs.BeginCreateOrUpdate | aca | ✓ | (containerapps.go:240) — full LRO + JobProperties + provisioningState=Succeeded |
| Container Apps | Jobs.BeginDelete | aca | ✓ | (containerapps.go:325) — cascades execution delete |
| Container Apps | Jobs.BeginStart | aca | ✓ | (containerapps.go:347) — execution metadata + LRO; started containers derive Docker platform from the resolved local image manifest. |
| Container Apps | Jobs.BeginStopExecution | aca | ✓ | (containerapps.go:592) |
| Container Apps | Jobs.NewListByResourceGroupPager | aca | ✓ | (containerapps.go:310) — pagination |
| Container Apps | ContainerApps.BeginCreateOrUpdate | aca (UseApp) | ✓ | `registerContainerAppsApps` in `simulator-azure/containerapps_apps.go`. Returns `provisioningState=Succeeded` + `LatestReadyRevisionName` + ARM-host-derived `LatestRevisionFqdn` so `appContainerState` reads "running" and `cloudServiceRegisterCNAME` can seed Private DNS. Started replicas derive Docker platform from the resolved local image manifest. |
| Container Apps | ContainerApps.BeginDelete | aca (UseApp) | ✓ | `containerapps_apps.go` |
| Container Apps | ContainerApps.Get | aca (UseApp) | ✓ | `containerapps_apps.go` — backend reads `LatestRevisionFqdn` for CNAME registration |
| Container Apps | EnvStorages.CreateOrUpdate | aca | ✓ | (containerappsenv.go:210) |
| Container Apps | EnvStorages.Delete | aca | ✓ | (containerappsenv.go:254) |
| Container Apps | Executions.NewListPager | aca | ✓ | (containerapps.go:548) |
| Network | NSG.BeginCreateOrUpdate | aca | ✓ | (network.go:276) — SecurityRules + provisioningState |
| Network | NSG.BeginDelete | aca | ✓ | (network.go:329) |
| Network | NSG.Get | aca | ✓ | (network.go:313) |
| Network | NSGRules.BeginCreateOrUpdate | aca | ✓ | (network.go:353) |
| Private DNS | PrivateDNSZones.BeginCreateOrUpdate | aca | ✓ | (dns.go:84) |
| Private DNS | PrivateDNSZones.BeginDelete | aca | ✓ | (dns.go:176) |
| Private DNS | PrivateDNSZones.Get | aca | ✓ | (dns.go:132) |
| Private DNS | PrivateDNSRecords.CreateOrUpdate | aca | ✓ | (dns.go:192) — A + CNAME |
| Private DNS | PrivateDNSRecords.Delete | aca | ✓ | (dns.go:232) |
| Private DNS | PrivateDNSRecords.Get | aca | ✓ | (dns.go:212) |
| Storage | StorageAccounts.ListKeys | aca, azure-functions | ✓ | (files.go:335) |
| Log Analytics | Logs.QueryWorkspace | aca, azure-functions | ✓ | (monitor.go:349) — KQL parsing + Tables[0].Rows |
| Log Analytics | LogsHTTP.QueryWorkspace | aca | ✓ | (monitor.go:349) — HTTP fallback for non-TLS sim runs |
| App Service | WebApps.BeginCreateOrUpdate | azure-functions | ✓ | (functions.go:88) |
| App Service | WebApps.Delete | azure-functions | ✓ | (functions.go:178) |
| App Service | WebApps.NewListByResourceGroupPager | azure-functions | ✓ | (functions.go:163) |
| App Service | WebApps.UpdateAzureStorageAccounts | azure-functions | ✓ | `PUT /sites/{name}/config/azurestorageaccounts` in `simulator-azure/functions.go`. Round-trip of `AzureStoragePropertyDictionaryResource` matches `armappservice` wire format. |

## Closure tracking

All 77 current-backend rows (33 AWS + 16 GCP + 28 Azure) ship ✓. The full AWS cloud-slice inventory additionally tracks 42 surfaces and 2,695 registered operations with official-client evidence. Plus 8 forward-looking GCP rows for Phase 126/127 driver work (no current backend caller — validated by SDK tests today; backend caller lands when those phases ship). Standing rule: any new SDK call added to a backend must update this matrix and add the sim handler in the same commit (PLAN.md principle #10). Forward-looking rows are sim-side prep only — they don't violate the rule because no backend uses them yet.
