# Simulator SDK / CLI / Terraform Coverage Matrix

This matrix is the maintained client-surface index for the simulator testing contract. Each row corresponds exactly to one canonical surface table in `specs/SIM_SURFACE_TABLES/`; `scripts/check-simulator-coverage-matrix.sh` fails CI if rows drift from that directory.

Legend:

- `direct` means a real official SDK, vendor CLI, or Terraform provider flow exercises the surface.
- `not applicable` means that client family does not expose that cloud surface in a meaningful way for the implemented simulator slice.
- `tracked #...` means a broader implementation issue owns that surface family.

| Surface | SDK | CLI | Terraform | Evidence |
|---|---|---|---|---|
| `aws-acm` | direct | direct | direct | `simulator-aws/sdk-tests/acm_test.go`; `simulator-aws/cli-tests/acm_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-acm_acme` | direct | direct | not applicable | `simulator-aws/sdk-tests/acm_acme_test.go`; `simulator-aws/cli-tests/acm_acme_test.go`; the RFC 8555 data-plane routes are listed in `simulator-aws/tests-exempt.txt` because no Terraform resource wraps them |
| `aws-acmpca` | direct | direct | direct | `simulator-aws/sdk-tests/acmpca_test.go`; `simulator-aws/cli-tests/acmpca_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-amplify` | direct | direct | direct | `simulator-aws/sdk-tests/amplify_test.go`; `simulator-aws/cli-tests/amplify_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-apigateway` | direct | direct | direct | `simulator-aws/sdk-tests/apigateway_test.go`; `simulator-aws/sdk-tests/apigateway_method_response_test.go`; `simulator-aws/cli-tests/apigateway_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-apigatewayv2` | direct | direct | direct | `simulator-aws/sdk-tests/apigatewayv2_deployment_test.go`; `simulator-aws/cli-tests/apigateway_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-application-autoscaling` | direct | direct | direct | `simulator-aws/sdk-tests/application_autoscaling_test.go`; `simulator-aws/cli-tests/application_autoscaling_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-autoscaling` | direct | direct | direct | `simulator-aws/sdk-tests/autoscaling_cloudtrail_test.go`; `simulator-aws/cli-tests/autoscaling_cloudtrail_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-budgets` | direct | direct | direct | `simulator-aws/sdk-tests/budgets_test.go`; `simulator-aws/cli-tests/budgets_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-cloudmap` | direct | direct | direct | `simulator-aws/sdk-tests/cloudmap_test.go`; `simulator-aws/cli-tests/cloudmap_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-cloudtrail` | direct | direct | direct | `simulator-aws/sdk-tests/autoscaling_cloudtrail_test.go`; `simulator-aws/cli-tests/autoscaling_cloudtrail_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-cloudwatch` | direct | direct | direct | `simulator-aws/sdk-tests/cloudwatch_test.go`; `simulator-aws/cli-tests/cloudwatch_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-dynamodb` | direct | direct | direct | `simulator-aws/sdk-tests/dynamodb_test.go`; `simulator-aws/cli-tests/dynamodb_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-ec2` | direct | direct | direct | `simulator-aws/sdk-tests/ec2_test.go`; `simulator-aws/cli-tests/ec2_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-ecr` | direct | direct | direct | `simulator-aws/sdk-tests/ecr_test.go`; `simulator-aws/cli-tests/ecr_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-ecs` | direct | direct | direct | `simulator-aws/sdk-tests/ecs_test.go`; `simulator-aws/cli-tests/ecs_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-efs` | direct | direct | direct | `simulator-aws/sdk-tests/efs_test.go`; `simulator-aws/cli-tests/efs_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-elasticache` | direct | direct | direct | `simulator-aws/sdk-tests/rds_elasticache_test.go`; `simulator-aws/cli-tests/rds_elasticache_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-elbv2` | direct | direct | direct | `simulator-aws/sdk-tests/elbv2_test.go`; `simulator-aws/cli-tests/elbv2_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-eventbridge` | direct | direct | direct | `simulator-aws/sdk-tests/eventbridge_test.go`; `simulator-aws/cli-tests/eventbridge_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-firehose` | direct | direct | direct | `simulator-aws/sdk-tests/firehose_test.go`; `simulator-aws/cli-tests/firehose_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-iam` | direct | direct | direct | `simulator-aws/sdk-tests/iam_test.go`; `simulator-aws/cli-tests/iam_slr_oidc_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-kinesis` | direct | direct | direct | `simulator-aws/sdk-tests/kinesis_test.go`; `simulator-aws/cli-tests/kinesis_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-kms` | direct | direct | direct | `simulator-aws/sdk-tests/kms_test.go`; `simulator-aws/cli-tests/kms_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-lambda` | direct | direct | direct | `simulator-aws/sdk-tests/lambda_test.go`; `simulator-aws/cli-tests/lambda_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-organizations` | direct | direct | direct | `simulator-aws/sdk-tests/organizations_test.go`; `simulator-aws/cli-tests/organizations_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-rds` | direct | direct | direct | `simulator-aws/sdk-tests/rds_elasticache_test.go`; `simulator-aws/sdk-tests/rds_snapshot_test.go`; `simulator-aws/cli-tests/rds_elasticache_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-route53` | direct | direct | direct | `simulator-aws/sdk-tests/route53_test.go`; `simulator-aws/cli-tests/route53_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-s3` | direct | direct | direct | `simulator-aws/sdk-tests/s3_test.go`; `simulator-aws/cli-tests/s3_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-s3-bucket-subresources` | direct | direct | direct | `simulator-aws/sdk-tests/s3_bucket_subresources_test.go`; `simulator-aws/cli-tests/s3_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-scheduler` | direct | direct | direct | `simulator-aws/sdk-tests/scheduler_test.go`; `simulator-aws/cli-tests/scheduler_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-s3-multipart` | direct | direct | not applicable | `simulator-aws/sdk-tests/s3_list_parts_test.go`; `simulator-aws/cli-tests/s3_test.go` |
| `aws-secretsmanager` | direct | direct | direct | `simulator-aws/sdk-tests/secretsmanager_test.go`; `simulator-aws/cli-tests/secretsmanager_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-sns` | direct | direct | direct | `simulator-aws/sdk-tests/sns_sqs_ops_test.go`; `simulator-aws/cli-tests/sqs_sns_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-sqs` | direct | direct | direct | `simulator-aws/sdk-tests/sqs_test.go`; `simulator-aws/cli-tests/sqs_sns_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-ssm_parameters` | direct | direct | direct | `simulator-aws/sdk-tests/ssm_parameters_test.go`; `simulator-aws/cli-tests/ssm_parameters_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-sts` | direct | direct | direct | `simulator-aws/sdk-tests/sts_test.go`; `simulator-aws/cli-tests/sts_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-wafv2` | direct | direct | direct | `simulator-aws/sdk-tests/wafv2_test.go`; `simulator-aws/cli-tests/wafv2_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-stepfunctions` | direct | direct | direct | `simulator-aws/sdk-tests/stepfunctions_test.go`; `simulator-aws/cli-tests/stepfunctions_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-codebuild` | direct | direct | direct | `simulator-aws/sdk-tests/codebuild_test.go`; `simulator-aws/cli-tests/codebuild_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-glue` | direct | direct | direct | `simulator-aws/sdk-tests/glue_test.go`; `simulator-aws/cli-tests/glue_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `aws-batch` | direct | direct | direct | `simulator-aws/sdk-tests/batch_test.go`; `simulator-aws/cli-tests/batch_test.go`; `simulator-aws/terraform-tests/main.tf` |
| `azure-acr` | direct | direct | direct | `simulator-azure/sdk-tests/acr_test.go`; `simulator-azure/cli-tests/acr_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-acr_tasks` | direct | not applicable | not applicable | `simulator-azure/sdk-tests/acr_tasks_test.go` (schedules a real docker build, follows the emitted log link); the slice serves the SDK `scheduleRun` quick-build contract — `az acr build` layers an interactive source-pack/streamed-log flow on top of it that the slice does not implement, and no AzureRM resource wraps quick-runs |
| `azure-apim` | direct | direct | direct | `simulator-azure/sdk-tests/apim_completion_test.go`; `simulator-azure/sdk-tests/apim_more_test.go`; `simulator-azure/cli-tests/apim_more_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-cache_redis` | direct | direct | direct | `simulator-azure/sdk-tests/redis_pg_test.go`; `simulator-azure/cli-tests/redis_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-compute` | direct | direct | direct | `simulator-azure/sdk-tests/compute_test.go`; `simulator-azure/sdk-tests/network_test.go`; `simulator-azure/cli-tests/compute_test.go`; `simulator-azure/cli-tests/loadbalancer_test.go`; `simulator-azure/cli-tests/nat_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-containerinstance` | direct | direct | direct | `simulator-azure/sdk-tests/logicapps_containerinstance_test.go`; `simulator-azure/cli-tests/logicapps_containerinstance_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-containerapps` | direct | direct | direct | `simulator-azure/sdk-tests/containerapps_test.go`; `simulator-azure/cli-tests/containerapps_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-cosmos` | direct | direct | direct | `simulator-azure/sdk-tests/cosmos_test.go`; `simulator-azure/cli-tests/cosmos_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-eventgrid` | direct | direct | direct | `simulator-azure/sdk-tests/eventgrid_test.go`; `simulator-azure/cli-tests/eventgrid_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-eventhubs` | direct | direct | direct | `simulator-azure/sdk-tests/eventhub_test.go`; `simulator-azure/cli-tests/eventhub_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-functions` | direct | direct | direct | `simulator-azure/sdk-tests/functions_test.go`; `simulator-azure/cli-tests/functions_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-keyvault` | direct | direct | direct | `simulator-azure/sdk-tests/keyvault_test.go`; `simulator-azure/cli-tests/arm_foundation_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-kv-data-plane` | direct | direct | direct | `simulator-azure/sdk-tests/keyvault_sdk_test.go`; `simulator-azure/cli-tests/keyvault_dataplane_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-logicapps` | direct | direct | direct | `simulator-azure/sdk-tests/logicapps_containerinstance_test.go`; `simulator-azure/cli-tests/logicapps_containerinstance_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-monitor` | direct | direct | direct | `simulator-azure/sdk-tests/monitor_test.go`; `simulator-azure/cli-tests/monitor_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-postgresql-flexible-server` | direct | direct | direct | `simulator-azure/sdk-tests/postgres_more_test.go`; `simulator-azure/sdk-tests/postgres_completion_test.go`; `simulator-azure/cli-tests/pg_resources_appinsights_cli_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-resourcegroups` | direct | direct | direct | `simulator-azure/sdk-tests/resourcegroup_test.go`; `simulator-azure/cli-tests/arm_foundation_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-resources` | direct | direct | direct | `simulator-azure/sdk-tests/resourcesarm_test.go`; `simulator-azure/cli-tests/pg_resources_appinsights_cli_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-servicebus-admin` | direct | not applicable | not applicable | `simulator-azure/sdk-tests/servicebus_admin_test.go` |
| `azure-servicebus-arm` | direct | direct | direct | `simulator-azure/sdk-tests/servicebus_arm_sdk_test.go`; `simulator-azure/cli-tests/servicebus_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-servicebus-data-plane` | direct | not applicable | not applicable | `simulator-azure/sdk-tests/servicebus_dataplane_test.go` |
| `azure-storage` | direct | direct | direct | `simulator-azure/sdk-tests/storage_test.go`; `simulator-azure/cli-tests/blob_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-storage-data-plane` | direct | direct | direct | `simulator-azure/sdk-tests/storage_dataplanes_test.go`; `simulator-azure/cli-tests/blob_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-subscription` | direct | direct | direct | `simulator-azure/sdk-tests/integration_test.go`; `simulator-azure/sdk-tests/subscription_alias_test.go`; `simulator-azure/cli-tests/arm_foundation_test.go`; `simulator-azure/cli-tests/subscription_alias_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `azure-subscription_operations` | direct | direct | not applicable | `simulator-azure/sdk-tests/subscription_ownership_test.go`; `simulator-azure/cli-tests/subscription_test.go`; the provider operation catalog has no AzureRM resource |
| `azure-subscription_ownership` | direct | direct | not applicable | `simulator-azure/sdk-tests/subscription_ownership_test.go`; `simulator-azure/cli-tests/subscription_test.go`; AzureRM's `azurerm_subscription` wraps alias create/cancel only, not the ownership handover |
| `azure-subscription_policy` | direct | direct | not applicable | `simulator-azure/sdk-tests/subscription_ownership_test.go`; `simulator-azure/cli-tests/subscription_test.go`; no AzureRM resource wraps the tenant subscription policy |
| `azure-entra` | direct | direct | not applicable | `simulator-azure/sdk-tests/entra_test.go`; `simulator-azure/cli-tests/entra_test.go` |
| `azure-private-dns` | direct | direct | direct | `simulator-azure/sdk-tests/dns_private_test.go`; `simulator-azure/cli-tests/dns_test.go`; `simulator-azure/terraform-tests/main.tf` |
| `gcp-apigateway` | direct | direct | direct | `simulator-gcp/sdk-tests/memorystore_apigw_test.go`; `simulator-gcp/cli-tests/client_surface_audit_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-artifactregistry` | direct | direct | direct | `simulator-gcp/sdk-tests/artifactregistry_oci_test.go`; `simulator-gcp/cli-tests/artifactregistry_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-bigtable` | direct | direct | direct | `simulator-gcp/sdk-tests/spanner_dataflow_bigtable_test.go`; `simulator-gcp/cli-tests/spanner_dataflow_bigtable_test.go`; `simulator-gcp/terraform-tests/main.tf`; `simulator-gcp/terraform-tests/apply_test.go` |
| `gcp-bigquery` | direct | direct | direct | `simulator-gcp/sdk-tests/data_saas_test.go`; `simulator-gcp/cli-tests/data_saas_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-cloudbuild` | direct | direct | direct | `simulator-gcp/sdk-tests/build_test.go`; `simulator-gcp/cli-tests/client_surface_audit_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-cloudfunctions` | direct | direct | direct | `simulator-gcp/sdk-tests/functions_sdk_test.go`; `simulator-gcp/cli-tests/functions_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-cloudkms` | direct | direct | direct | `simulator-gcp/sdk-tests/cloudkms_test.go`; `simulator-gcp/cli-tests/cloudkms_test.go`; `simulator-gcp/terraform-tests/fixtures/kms-lifecycle/main.tf` |
| `gcp-cloudresourcemanager` | direct | direct | direct | `simulator-gcp/sdk-tests/resourcemanager_projects_test.go`; `simulator-gcp/sdk-tests/cloudresourcemanager_v3_test.go`; `simulator-gcp/sdk-tests/resourcemanager_orgpolicy_test.go`; `simulator-gcp/cli-tests/projects_test.go`; `simulator-gcp/cli-tests/org_policies_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-cloudresourcemanager_folders_v2` | direct | direct | direct | `simulator-gcp/sdk-tests/resourcemanager_orgpolicy_test.go`; `simulator-gcp/cli-tests/org_policies_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-cloudrun` | direct | direct | direct | `simulator-gcp/sdk-tests/run_sdk_test.go`; `simulator-gcp/cli-tests/run_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-compute` | direct | direct | direct | `simulator-gcp/sdk-tests/compute_test.go`; `simulator-gcp/cli-tests/compute_disks_test.go`; `simulator-gcp/cli-tests/compute_instances_test.go`; `simulator-gcp/cli-tests/compute_nat_test.go`; `simulator-gcp/cli-tests/client_surface_audit_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-compute_loadbalancing` | direct | direct | direct | `simulator-gcp/sdk-tests/compute_test.go`; `simulator-gcp/cli-tests/compute_loadbalancing_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-dataflow` | direct | direct | not applicable | `simulator-gcp/sdk-tests/spanner_dataflow_bigtable_test.go`; `simulator-gcp/cli-tests/spanner_dataflow_bigtable_test.go` |
| `gcp-dns` | direct | direct | direct | `simulator-gcp/sdk-tests/dns_test.go`; `simulator-gcp/cli-tests/dns_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-eventarc` | direct | direct | direct | `simulator-gcp/sdk-tests/eventarc_test.go`; `simulator-gcp/cli-tests/eventarc_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-firestore` | direct | direct | direct | `simulator-gcp/sdk-tests/data_saas_test.go`; `simulator-gcp/cli-tests/data_saas_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-gcs` | direct | direct | direct | `simulator-gcp/sdk-tests/storage_test.go`; `simulator-gcp/cli-tests/storage_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-iam` | direct | direct | direct | `simulator-gcp/sdk-tests/iam_test.go`; `simulator-gcp/cli-tests/client_surface_audit_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-logging` | direct | direct | direct | `simulator-gcp/sdk-tests/logging_test.go`; `simulator-gcp/cli-tests/logging_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-memorystore_redis` | direct | direct | direct | `simulator-gcp/sdk-tests/memorystore_apigw_test.go`; `simulator-gcp/cli-tests/redis_sql_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-pubsub` | direct | direct | direct | `simulator-gcp/sdk-tests/pubsub_test.go`; `simulator-gcp/cli-tests/client_surface_audit_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-secretmanager` | direct | direct | direct | `simulator-gcp/sdk-tests/secretmanager_test.go`; `simulator-gcp/cli-tests/secretmanager_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-sqladmin` | direct | direct | direct | `simulator-gcp/sdk-tests/cloudsql_test.go`; `simulator-gcp/cli-tests/redis_sql_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-spanner` | direct | direct | direct | `simulator-gcp/sdk-tests/spanner_dataflow_bigtable_test.go`; `simulator-gcp/sdk-tests/spanner_grpc_test.go`; `simulator-gcp/sdk-tests/spanner_rest_data_test.go`; `simulator-gcp/cli-tests/spanner_dataflow_bigtable_test.go`; `simulator-gcp/terraform-tests/main.tf` |
| `gcp-sts` | direct | direct | not applicable | `simulator-gcp/sdk-tests/sts_test.go`; `simulator-gcp/cli-tests/workforce_login_test.go` |
| `gcp-vpcaccess` | direct | direct | direct | `simulator-gcp/sdk-tests/integration_test.go`; `simulator-gcp/cli-tests/vpcaccess_test.go`; `simulator-gcp/terraform-tests/main.tf` |
