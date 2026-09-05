# Simulator cloud-slice inventory

Which public API slices each simulator exposes, where every registered
operation of a slice is tracked, and where the official-client evidence for it
lives.

- Each per-operation table under [`SIM_SURFACE_TABLES/`](SIM_SURFACE_TABLES/)
  is generated from the simulator's registered routes by
  `scripts/seed-surface-tables.sh`; every row names the handler and marks
  whether it reaches state. `scripts/check-surface-tables-generated.sh` fails a
  commit whose tables are stale.
- [`SIM_TEST_COVERAGE_MATRIX.md`](SIM_TEST_COVERAGE_MATRIX.md) records, per
  surface, the official SDK, vendor CLI and Terraform provider flows that
  exercise it. `scripts/check-simulator-coverage-matrix.sh` holds its rows to
  exactly the set of surface tables and holds this inventory to every AWS
  surface.
- Which slices a simulator covers is this project's choice; nothing downstream
  defines it. The inventory is exhaustive rather than consumer-driven.

## AWS — 50 surfaces

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
| Amazon CloudFront | [`aws-cloudfront`](SIM_SURFACE_TABLES/aws-cloudfront.md) | [`aws-cloudfront`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon CloudFront (functions, keys, policies) | [`aws-cloudfront_extras`](SIM_SURFACE_TABLES/aws-cloudfront_extras.md) | [`aws-cloudfront_extras`](SIM_TEST_COVERAGE_MATRIX.md) |
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
| Amazon S3 Object Lambda | [`aws-s3_object_lambda`](SIM_SURFACE_TABLES/aws-s3_object_lambda.md) | [`aws-s3_object_lambda`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon S3 Access Grants | [`aws-s3control_access_grants`](SIM_SURFACE_TABLES/aws-s3control_access_grants.md) | [`aws-s3control_access_grants`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon S3 Batch Operations | [`aws-s3control_jobs`](SIM_SURFACE_TABLES/aws-s3control_jobs.md) | [`aws-s3control_jobs`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon S3 access point scope, Outposts and directory-bucket listings | [`aws-s3control_misc`](SIM_SURFACE_TABLES/aws-s3control_misc.md) | [`aws-s3control_misc`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon S3 Multi-Region Access Points | [`aws-s3control_mrap`](SIM_SURFACE_TABLES/aws-s3control_mrap.md) | [`aws-s3control_mrap`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon S3 Storage Lens | [`aws-s3control_storage_lens`](SIM_SURFACE_TABLES/aws-s3control_storage_lens.md) | [`aws-s3control_storage_lens`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon EventBridge Scheduler | [`aws-scheduler`](SIM_SURFACE_TABLES/aws-scheduler.md) | [`aws-scheduler`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Secrets Manager | [`aws-secretsmanager`](SIM_SURFACE_TABLES/aws-secretsmanager.md) | [`aws-secretsmanager`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Simple Notification Service (SNS) | [`aws-sns`](SIM_SURFACE_TABLES/aws-sns.md) | [`aws-sns`](SIM_TEST_COVERAGE_MATRIX.md) |
| Amazon Simple Queue Service (SQS) | [`aws-sqs`](SIM_SURFACE_TABLES/aws-sqs.md) | [`aws-sqs`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Systems Manager Parameter Store | [`aws-ssm_parameters`](SIM_SURFACE_TABLES/aws-ssm_parameters.md) | [`aws-ssm_parameters`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Step Functions | [`aws-stepfunctions`](SIM_SURFACE_TABLES/aws-stepfunctions.md) | [`aws-stepfunctions`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS Security Token Service (STS) | [`aws-sts`](SIM_SURFACE_TABLES/aws-sts.md) | [`aws-sts`](SIM_TEST_COVERAGE_MATRIX.md) |
| AWS WAF | [`aws-wafv2`](SIM_SURFACE_TABLES/aws-wafv2.md) | [`aws-wafv2`](SIM_TEST_COVERAGE_MATRIX.md) |

## Google Cloud — 56 surfaces

| Google Cloud surface | Per-operation inventory | External client evidence |
|---|---|---|
| `gcp-apigateway` | [`gcp-apigateway`](SIM_SURFACE_TABLES/gcp-apigateway.md) | [`gcp-apigateway`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-artifactregistry` | [`gcp-artifactregistry`](SIM_SURFACE_TABLES/gcp-artifactregistry.md) | [`gcp-artifactregistry`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-bigquery` | [`gcp-bigquery`](SIM_SURFACE_TABLES/gcp-bigquery.md) | [`gcp-bigquery`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-bigtable` | [`gcp-bigtable`](SIM_SURFACE_TABLES/gcp-bigtable.md) | [`gcp-bigtable`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-cloudbilling` | [`gcp-cloudbilling`](SIM_SURFACE_TABLES/gcp-cloudbilling.md) | [`gcp-cloudbilling`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-cloudbuild` | [`gcp-cloudbuild`](SIM_SURFACE_TABLES/gcp-cloudbuild.md) | [`gcp-cloudbuild`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-cloudbuild_regional` | [`gcp-cloudbuild_regional`](SIM_SURFACE_TABLES/gcp-cloudbuild_regional.md) | [`gcp-cloudbuild_regional`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-cloudfunctions` | [`gcp-cloudfunctions`](SIM_SURFACE_TABLES/gcp-cloudfunctions.md) | [`gcp-cloudfunctions`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-cloudkms` | [`gcp-cloudkms`](SIM_SURFACE_TABLES/gcp-cloudkms.md) | [`gcp-cloudkms`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-cloudresourcemanager` | [`gcp-cloudresourcemanager`](SIM_SURFACE_TABLES/gcp-cloudresourcemanager.md) | [`gcp-cloudresourcemanager`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-cloudresourcemanager_folders_v2` | [`gcp-cloudresourcemanager_folders_v2`](SIM_SURFACE_TABLES/gcp-cloudresourcemanager_folders_v2.md) | [`gcp-cloudresourcemanager_folders_v2`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-cloudrun` | [`gcp-cloudrun`](SIM_SURFACE_TABLES/gcp-cloudrun.md) | [`gcp-cloudrun`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute` | [`gcp-compute`](SIM_SURFACE_TABLES/gcp-compute.md) | [`gcp-compute`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_bulk_verbs` | [`gcp-compute_bulk_verbs`](SIM_SURFACE_TABLES/gcp-compute_bulk_verbs.md) | [`gcp-compute_bulk_verbs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_catalogs` | [`gcp-compute_catalogs`](SIM_SURFACE_TABLES/gcp-compute_catalogs.md) | [`gcp-compute_catalogs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_disk_verbs` | [`gcp-compute_disk_verbs`](SIM_SURFACE_TABLES/gcp-compute_disk_verbs.md) | [`gcp-compute_disk_verbs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_instance_verbs` | [`gcp-compute_instance_verbs`](SIM_SURFACE_TABLES/gcp-compute_instance_verbs.md) | [`gcp-compute_instance_verbs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_interconnect_diagnostics` | [`gcp-compute_interconnect_diagnostics`](SIM_SURFACE_TABLES/gcp-compute_interconnect_diagnostics.md) | [`gcp-compute_interconnect_diagnostics`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_interconnect_locations` | [`gcp-compute_interconnect_locations`](SIM_SURFACE_TABLES/gcp-compute_interconnect_locations.md) | [`gcp-compute_interconnect_locations`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_interconnect_macsec` | [`gcp-compute_interconnect_macsec`](SIM_SURFACE_TABLES/gcp-compute_interconnect_macsec.md) | [`gcp-compute_interconnect_macsec`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_interconnect_remote_locations` | [`gcp-compute_interconnect_remote_locations`](SIM_SURFACE_TABLES/gcp-compute_interconnect_remote_locations.md) | [`gcp-compute_interconnect_remote_locations`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_last_verbs` | [`gcp-compute_last_verbs`](SIM_SURFACE_TABLES/gcp-compute_last_verbs.md) | [`gcp-compute_last_verbs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_lb_more_verbs` | [`gcp-compute_lb_more_verbs`](SIM_SURFACE_TABLES/gcp-compute_lb_more_verbs.md) | [`gcp-compute_lb_more_verbs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_lb_verbs` | [`gcp-compute_lb_verbs`](SIM_SURFACE_TABLES/gcp-compute_lb_verbs.md) | [`gcp-compute_lb_verbs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_loadbalancing` | [`gcp-compute_loadbalancing`](SIM_SURFACE_TABLES/gcp-compute_loadbalancing.md) | [`gcp-compute_loadbalancing`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_members` | [`gcp-compute_members`](SIM_SURFACE_TABLES/gcp-compute_members.md) | [`gcp-compute_members`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_nested_prefixes` | [`gcp-compute_nested_prefixes`](SIM_SURFACE_TABLES/gcp-compute_nested_prefixes.md) | [`gcp-compute_nested_prefixes`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_policies` | [`gcp-compute_policies`](SIM_SURFACE_TABLES/gcp-compute_policies.md) | [`gcp-compute_policies`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_preview_features` | [`gcp-compute_preview_features`](SIM_SURFACE_TABLES/gcp-compute_preview_features.md) | [`gcp-compute_preview_features`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_project` | [`gcp-compute_project`](SIM_SURFACE_TABLES/gcp-compute_project.md) | [`gcp-compute_project`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_reads` | [`gcp-compute_reads`](SIM_SURFACE_TABLES/gcp-compute_reads.md) | [`gcp-compute_reads`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_region_instance_groups` | [`gcp-compute_region_instance_groups`](SIM_SURFACE_TABLES/gcp-compute_region_instance_groups.md) | [`gcp-compute_region_instance_groups`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_reservation_hosts` | [`gcp-compute_reservation_hosts`](SIM_SURFACE_TABLES/gcp-compute_reservation_hosts.md) | [`gcp-compute_reservation_hosts`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_reservation_verbs` | [`gcp-compute_reservation_verbs`](SIM_SURFACE_TABLES/gcp-compute_reservation_verbs.md) | [`gcp-compute_reservation_verbs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-compute_settings` | [`gcp-compute_settings`](SIM_SURFACE_TABLES/gcp-compute_settings.md) | [`gcp-compute_settings`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-dataflow` | [`gcp-dataflow`](SIM_SURFACE_TABLES/gcp-dataflow.md) | [`gcp-dataflow`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-dns` | [`gcp-dns`](SIM_SURFACE_TABLES/gcp-dns.md) | [`gcp-dns`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-eventarc` | [`gcp-eventarc`](SIM_SURFACE_TABLES/gcp-eventarc.md) | [`gcp-eventarc`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-firestore` | [`gcp-firestore`](SIM_SURFACE_TABLES/gcp-firestore.md) | [`gcp-firestore`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-firestore_change_streams` | [`gcp-firestore_change_streams`](SIM_SURFACE_TABLES/gcp-firestore_change_streams.md) | [`gcp-firestore_change_streams`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-firestore_document_verbs` | [`gcp-firestore_document_verbs`](SIM_SURFACE_TABLES/gcp-firestore_document_verbs.md) | [`gcp-firestore_document_verbs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-gcs` | [`gcp-gcs`](SIM_SURFACE_TABLES/gcp-gcs.md) | [`gcp-gcs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-gcs_object_acls` | [`gcp-gcs_object_acls`](SIM_SURFACE_TABLES/gcp-gcs_object_acls.md) | [`gcp-gcs_object_acls`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-gcs_object_iam` | [`gcp-gcs_object_iam`](SIM_SURFACE_TABLES/gcp-gcs_object_iam.md) | [`gcp-gcs_object_iam`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-gcs_object_restore` | [`gcp-gcs_object_restore`](SIM_SURFACE_TABLES/gcp-gcs_object_restore.md) | [`gcp-gcs_object_restore`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-iam` | [`gcp-iam`](SIM_SURFACE_TABLES/gcp-iam.md) | [`gcp-iam`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-logging` | [`gcp-logging`](SIM_SURFACE_TABLES/gcp-logging.md) | [`gcp-logging`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-memorystore_redis` | [`gcp-memorystore_redis`](SIM_SURFACE_TABLES/gcp-memorystore_redis.md) | [`gcp-memorystore_redis`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-operations_cancel` | [`gcp-operations_cancel`](SIM_SURFACE_TABLES/gcp-operations_cancel.md) | [`gcp-operations_cancel`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-pubsub` | [`gcp-pubsub`](SIM_SURFACE_TABLES/gcp-pubsub.md) | [`gcp-pubsub`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-secretmanager` | [`gcp-secretmanager`](SIM_SURFACE_TABLES/gcp-secretmanager.md) | [`gcp-secretmanager`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-spanner` | [`gcp-spanner`](SIM_SURFACE_TABLES/gcp-spanner.md) | [`gcp-spanner`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-sqladmin` | [`gcp-sqladmin`](SIM_SURFACE_TABLES/gcp-sqladmin.md) | [`gcp-sqladmin`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-sqladmin_blue_green` | [`gcp-sqladmin_blue_green`](SIM_SURFACE_TABLES/gcp-sqladmin_blue_green.md) | [`gcp-sqladmin_blue_green`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-sts` | [`gcp-sts`](SIM_SURFACE_TABLES/gcp-sts.md) | [`gcp-sts`](SIM_TEST_COVERAGE_MATRIX.md) |
| `gcp-vpcaccess` | [`gcp-vpcaccess`](SIM_SURFACE_TABLES/gcp-vpcaccess.md) | [`gcp-vpcaccess`](SIM_TEST_COVERAGE_MATRIX.md) |

## Azure — 45 surfaces

| Azure surface | Per-operation inventory | External client evidence |
|---|---|---|
| `azure-acr` | [`azure-acr`](SIM_SURFACE_TABLES/azure-acr.md) | [`azure-acr`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-acr_dataplane_properties` | [`azure-acr_dataplane_properties`](SIM_SURFACE_TABLES/azure-acr_dataplane_properties.md) | [`azure-acr_dataplane_properties`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-acr_tasks` | [`azure-acr_tasks`](SIM_SURFACE_TABLES/azure-acr_tasks.md) | [`azure-acr_tasks`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-apim` | [`azure-apim`](SIM_SURFACE_TABLES/azure-apim.md) | [`azure-apim`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-appserviceplan` | [`azure-appserviceplan`](SIM_SURFACE_TABLES/azure-appserviceplan.md) | [`azure-appserviceplan`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-azure_dns` | [`azure-azure_dns`](SIM_SURFACE_TABLES/azure-azure_dns.md) | [`azure-azure_dns`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-cache_redis` | [`azure-cache_redis`](SIM_SURFACE_TABLES/azure-cache_redis.md) | [`azure-cache_redis`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-compute` | [`azure-compute`](SIM_SURFACE_TABLES/azure-compute.md) | [`azure-compute`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-compute_operations` | [`azure-compute_operations`](SIM_SURFACE_TABLES/azure-compute_operations.md) | [`azure-compute_operations`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-compute_vm_extensions` | [`azure-compute_vm_extensions`](SIM_SURFACE_TABLES/azure-compute_vm_extensions.md) | [`azure-compute_vm_extensions`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-compute_vm_operations` | [`azure-compute_vm_operations`](SIM_SURFACE_TABLES/azure-compute_vm_operations.md) | [`azure-compute_vm_operations`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-compute_vm_patches` | [`azure-compute_vm_patches`](SIM_SURFACE_TABLES/azure-compute_vm_patches.md) | [`azure-compute_vm_patches`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-containerapps` | [`azure-containerapps`](SIM_SURFACE_TABLES/azure-containerapps.md) | [`azure-containerapps`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-containerinstance` | [`azure-containerinstance`](SIM_SURFACE_TABLES/azure-containerinstance.md) | [`azure-containerinstance`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-cosmos` | [`azure-cosmos`](SIM_SURFACE_TABLES/azure-cosmos.md) | [`azure-cosmos`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-dns` | [`azure-dns`](SIM_SURFACE_TABLES/azure-dns.md) | [`azure-dns`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-dns_more` | [`azure-dns_more`](SIM_SURFACE_TABLES/azure-dns_more.md) | [`azure-dns_more`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-entra` | [`azure-entra`](SIM_SURFACE_TABLES/azure-entra.md) | [`azure-entra`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-eventgrid` | [`azure-eventgrid`](SIM_SURFACE_TABLES/azure-eventgrid.md) | [`azure-eventgrid`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-eventhub` | [`azure-eventhub`](SIM_SURFACE_TABLES/azure-eventhub.md) | [`azure-eventhub`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-eventhubs` | [`azure-eventhubs`](SIM_SURFACE_TABLES/azure-eventhubs.md) | [`azure-eventhubs`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-functions` | [`azure-functions`](SIM_SURFACE_TABLES/azure-functions.md) | [`azure-functions`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-insights_dataplane` | [`azure-insights_dataplane`](SIM_SURFACE_TABLES/azure-insights_dataplane.md) | [`azure-insights_dataplane`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-keyvault` | [`azure-keyvault`](SIM_SURFACE_TABLES/azure-keyvault.md) | [`azure-keyvault`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-keyvault_managedhsm` | [`azure-keyvault_managedhsm`](SIM_SURFACE_TABLES/azure-keyvault_managedhsm.md) | [`azure-keyvault_managedhsm`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-keyvault_managedhsm_tail` | [`azure-keyvault_managedhsm_tail`](SIM_SURFACE_TABLES/azure-keyvault_managedhsm_tail.md) | [`azure-keyvault_managedhsm_tail`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-kv-data-plane` | [`azure-kv-data-plane`](SIM_SURFACE_TABLES/azure-kv-data-plane.md) | [`azure-kv-data-plane`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-logicapps` | [`azure-logicapps`](SIM_SURFACE_TABLES/azure-logicapps.md) | [`azure-logicapps`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-monitor` | [`azure-monitor`](SIM_SURFACE_TABLES/azure-monitor.md) | [`azure-monitor`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-network` | [`azure-network`](SIM_SURFACE_TABLES/azure-network.md) | [`azure-network`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-postgresql-flexible-server` | [`azure-postgresql-flexible-server`](SIM_SURFACE_TABLES/azure-postgresql-flexible-server.md) | [`azure-postgresql-flexible-server`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-private-dns` | [`azure-private-dns`](SIM_SURFACE_TABLES/azure-private-dns.md) | [`azure-private-dns`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-public_dns` | [`azure-public_dns`](SIM_SURFACE_TABLES/azure-public_dns.md) | [`azure-public_dns`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-resourcegroups` | [`azure-resourcegroups`](SIM_SURFACE_TABLES/azure-resourcegroups.md) | [`azure-resourcegroups`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-resources` | [`azure-resources`](SIM_SURFACE_TABLES/azure-resources.md) | [`azure-resources`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-servicebus` | [`azure-servicebus`](SIM_SURFACE_TABLES/azure-servicebus.md) | [`azure-servicebus`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-servicebus-admin` | [`azure-servicebus-admin`](SIM_SURFACE_TABLES/azure-servicebus-admin.md) | [`azure-servicebus-admin`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-servicebus-arm` | [`azure-servicebus-arm`](SIM_SURFACE_TABLES/azure-servicebus-arm.md) | [`azure-servicebus-arm`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-servicebus-data-plane` | [`azure-servicebus-data-plane`](SIM_SURFACE_TABLES/azure-servicebus-data-plane.md) | [`azure-servicebus-data-plane`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-storage` | [`azure-storage`](SIM_SURFACE_TABLES/azure-storage.md) | [`azure-storage`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-storage-data-plane` | [`azure-storage-data-plane`](SIM_SURFACE_TABLES/azure-storage-data-plane.md) | [`azure-storage-data-plane`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-subscription` | [`azure-subscription`](SIM_SURFACE_TABLES/azure-subscription.md) | [`azure-subscription`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-subscription_operations` | [`azure-subscription_operations`](SIM_SURFACE_TABLES/azure-subscription_operations.md) | [`azure-subscription_operations`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-subscription_ownership` | [`azure-subscription_ownership`](SIM_SURFACE_TABLES/azure-subscription_ownership.md) | [`azure-subscription_ownership`](SIM_TEST_COVERAGE_MATRIX.md) |
| `azure-subscription_policy` | [`azure-subscription_policy`](SIM_SURFACE_TABLES/azure-subscription_policy.md) | [`azure-subscription_policy`](SIM_TEST_COVERAGE_MATRIX.md) |
