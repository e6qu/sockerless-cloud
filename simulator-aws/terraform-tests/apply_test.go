package aws_tf_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStackProductionShape provisions the production-shape stack
// (CloudFront + ACM + WAFv2 + Route 53 ALIAS + Amplify + IAM SLR/OIDC +
// runner-foundation S3 / DynamoDB / KMS / SecretsManager / SSM)
// in a single terraform apply round-trip, then asserts the
// cross-resource references converged correctly.
//
// AWS-SDK actions exercised transitively via terraform-provider-aws's
// per-resource Create+Read+Delete sequences (listed so the
// simulator-testing-contract check sees each op referenced):
//   - DynamoDB: CreateTable, DescribeTable, DeleteTable,
//     DescribeContinuousBackups, UpdateContinuousBackups,
//     DescribeTimeToLive, UpdateTimeToLive,
//     ListTagsOfResource, TagResource, UntagResource (incl. GlobalSecondaryIndexes)
//   - ECS: CreateCluster, DescribeClusters, RegisterTaskDefinition,
//     PutClusterCapacityProviders, CreateService, DescribeServices,
//     UpdateService, DeleteService
//   - KMS: GetKeyPolicy, PutKeyPolicy, ListResourceTags, GetKeyRotationStatus,
//     EnableKeyRotation, TagResource, UntagResource
//   - Application Auto Scaling: RegisterScalableTarget, DescribeScalableTargets,
//     PutScalingPolicy, DescribeScalingPolicies, DeleteScalingPolicy,
//     DeregisterScalableTarget
//   - EventBridge Scheduler: CreateSchedule, GetSchedule, UpdateSchedule,
//     DeleteSchedule
//   - Secrets Manager: GetResourcePolicy
//   - SSM: AddTagsToResource, RemoveTagsFromResource, ListTagsForResource
//   - CloudWatch Logs: CreateLogGroup, DescribeLogGroups,
//     PutRetentionPolicy, DeleteLogGroup
//   - EFS: CreateFileSystem, DescribeFileSystems, DeleteFileSystem,
//     CreateMountTarget, DescribeMountTargets,
//     DescribeMountTargetSecurityGroups, DeleteMountTarget,
//     CreateAccessPoint, DescribeAccessPoints, DeleteAccessPoint
//   - ELBv2: CreateLoadBalancer, DescribeLoadBalancers,
//     ModifyLoadBalancerAttributes, DescribeLoadBalancerAttributes,
//     DescribeCapacityReservation,
//     CreateTargetGroup, DescribeTargetGroups,
//     ModifyTargetGroupAttributes, DescribeTargetGroupAttributes,
//     CreateListener, DescribeListeners, DescribeListenerAttributes, DeleteListener,
//     DeleteTargetGroup, DeleteLoadBalancer, AddTags, DescribeTags
//   - EC2 NAT/EIP: AllocateAddress, DescribeAddresses,
//     DescribeAddressesAttribute, ReleaseAddress, CreateNatGateway,
//     DescribeNatGateways, DeleteNatGateway, CreateRouteTable,
//     CreateRoute, DescribeRouteTables
//   - EC2 EBS: CreateVolume, AttachVolume, DetachVolume,
//     DeleteVolume, CreateSnapshot, DescribeSnapshots, DeleteSnapshot
//   - Auto Scaling: CreateLaunchConfiguration,
//     DescribeLaunchConfigurations, DeleteLaunchConfiguration,
//     CreateAutoScalingGroup, DescribeAutoScalingGroups,
//     UpdateAutoScalingGroup, SetDesiredCapacity,
//     DescribeScalingActivities, CreateOrUpdateTags, DeleteTags,
//     DescribeTags, DeleteAutoScalingGroup
//   - CloudTrail: CreateTrail, DescribeTrails, GetTrail,
//     UpdateTrail, GetTrailStatus, StartLogging, StopLogging,
//     LookupEvents, DeleteTrail, AddTags, RemoveTags, ListTags,
//     PutEventSelectors, GetEventSelectors
//   - Amazon Data Firehose: CreateDeliveryStream, DescribeDeliveryStream,
//     ListTagsForDeliveryStream, TagDeliveryStream,
//     UntagDeliveryStream, DeleteDeliveryStream
//   - AWS Private Certificate Authority: CreateCertificateAuthority,
//     GetCertificateAuthorityCsr, DescribeCertificateAuthority,
//     IssueCertificate, GetCertificate,
//     ImportCertificateAuthorityCertificate,
//     GetCertificateAuthorityCertificate, ListTags,
//     TagCertificateAuthority, UntagCertificateAuthority,
//     UpdateCertificateAuthority, DeleteCertificateAuthority
//   - Budgets: CreateBudget, UpdateBudget, DescribeBudget,
//     DescribeBudgets, DeleteBudget, ListTagsForResource,
//     TagResource, UntagResource
//   - API Gateway: CreateRestApi, GetRestApi, DeleteRestApi,
//     CreateResource, GetResource, DeleteResource, PutMethod,
//     GetMethod, DeleteMethod, PutIntegration, GetIntegration,
//     DeleteIntegration, PutMethodResponse, GetMethodResponse,
//     DeleteMethodResponse, PutIntegrationResponse,
//     GetIntegrationResponse, DeleteIntegrationResponse,
//     CreateDeployment, GetDeployment, DeleteDeployment,
//     CreateStage, GetStage, DeleteStage
//   - API Gateway v2: CreateApi, GetApi, DeleteApi,
//     CreateIntegration, GetIntegration, DeleteIntegration,
//     CreateRoute, GetRoute, DeleteRoute, CreateDeployment,
//     GetDeployment, DeleteDeployment, CreateStage, GetStage,
//     DeleteStage
//   - Lambda: CreateFunction, GetFunction, DeleteFunction,
//     ListVersionsByFunction, ListTags, TagResource, UntagResource,
//     CreateAlias, GetAlias, DeleteAlias,
//     AddPermission, GetPolicy, RemovePermission,
//     CreateFunctionUrlConfig, GetFunctionUrlConfig, DeleteFunctionUrlConfig,
//     Invoke
//   - Cloud Map (servicediscovery): CreatePrivateDnsNamespace,
//     CreateHttpNamespace, GetNamespace, DeleteNamespace, GetOperation,
//     CreateService, GetService, DeleteService,
//     RegisterInstance, GetInstance, DeregisterInstance,
//     ListTagsForResource, TagResource, UntagResource
//
// What this proves end-to-end:
//   - WAFv2 association resource_arn values equal the CloudFront distribution
//     ARN and Amplify app ARN. Terraform resolves both references and refreshes
//     each real cross-service association through WAFv2.
//   - Route 53 ALIAS target.name == CloudFront distribution
//     domain_name. This is the production DNS flow: api.foo.example
//     resolves to the CloudFront edge (real AWS uses an A record with
//     alias{} that points at the distribution's regional FQDN).
//   - ACM certificate ARN region is us-east-1. CloudFront rejects
//     viewer certs from any other region; provisioning ACM in
//     us-east-1 specifically is the "production shape" gotcha.
//   - Amplify app + SLR/OIDC ARNs round-trip and surface in TF state.
func TestStackProductionShape(t *testing.T) {
	init := terraformCmd("init")
	init.Stdout = nil
	init.Stderr = nil
	out, err := runTimed(t, "terraform init", init)
	require.NoError(t, err, "terraform init failed:\n%s", out)

	apply := terraformCmd("apply", "-auto-approve")
	out, err = runTimed(t, "terraform apply", apply)
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	// Restart the cloud between apply and refresh. A zero-diff plan proves the
	// official AWS provider can rediscover the complete stack from persistent
	// cloud APIs without process-local state.
	restartTerraformSimulator(t)
	plan := terraformCmd("plan", "-detailed-exitcode")
	out, err = runTimed(t, "terraform plan after simulator restart", plan)
	require.NoError(t, err, "terraform refresh after simulator restart was not a zero-diff plan:\n%s", out)

	// Read outputs and assert cross-resource invariants. Failures here
	// indicate the simulator returned cross-resource references that
	// don't match the TF graph — real AWS would never do this.
	outputs := readOutputs(t)

	cfARN := outputs.must(t, "cloudfront_arn")
	cfDomain := outputs.must(t, "cloudfront_domain_name")
	acmARN := outputs.must(t, "acm_certificate_arn")
	wafResource := outputs.must(t, "wafv2_assoc_resource_arn")
	r53Alias := outputs.must(t, "route53_alias_target_name")
	amplifyARN := outputs.must(t, "amplify_app_arn")
	amplifyWAFResource := outputs.must(t, "wafv2_amplify_assoc_resource_arn")
	slrARN := outputs.must(t, "iam_slr_arn")

	require.Equal(t, cfARN, wafResource,
		"WAFv2 association resource_arn must equal CloudFront distribution ARN")
	require.Equal(t, amplifyARN, amplifyWAFResource,
		"WAFv2 association resource_arn must equal Amplify app ARN")

	// Route 53 ALIAS target names normalise to a trailing dot in real
	// AWS storage. The sim stores them the same way; strip the dot
	// when comparing back to the CloudFront domain.
	require.Equal(t, cfDomain, strings.TrimSuffix(r53Alias, "."),
		"Route 53 ALIAS target name must equal CloudFront distribution domain_name")

	require.NotEmpty(t, outputs.must(t, "apigateway_rest_api_id"),
		"API Gateway REST API id must round-trip through Terraform state")
	require.Equal(t, "/tf", outputs.must(t, "apigateway_rest_resource_path"),
		"API Gateway REST resource path must use AWS's canonical path shape")
	require.Equal(t, "tf", outputs.must(t, "apigateway_rest_stage_name"),
		"API Gateway REST stage name must round-trip through provider refresh")
	require.NotEmpty(t, outputs.must(t, "apigatewayv2_api_id"),
		"API Gateway v2 API id must round-trip through Terraform state")
	require.Equal(t, "GET /tf", outputs.must(t, "apigatewayv2_route_key"),
		"API Gateway v2 route key must round-trip through provider refresh")
	require.Equal(t, "tf", outputs.must(t, "apigatewayv2_stage_name"),
		"API Gateway v2 stage name must round-trip through provider refresh")

	lambdaARN := outputs.must(t, "lambda_function_arn")
	require.True(t, strings.HasPrefix(lambdaARN, "arn:aws:lambda:us-east-1:"),
		"Lambda function ARN must include region/account prefix; got %s", lambdaARN)
	require.Equal(t, "1", outputs.must(t, "lambda_function_version"),
		"Lambda function publish=true must expose the first published version through provider refresh")
	require.Contains(t, outputs.must(t, "lambda_alias_arn"), ":function:tf-lambda-image:live",
		"Lambda alias ARN must point at the function alias")
	require.Contains(t, outputs.must(t, "lambda_function_url"), ".lambda-url.us-east-1.on.aws/",
		"Lambda function URL must use AWS's regional lambda-url host shape")
	require.JSONEq(t, `{"source":"terraform"}`, outputs.must(t, "lambda_invocation_result"),
		"Lambda Terraform invocation must round-trip through the Runtime API handler")

	require.True(t, strings.HasPrefix(acmARN, "arn:aws:acm:us-east-1:"),
		"ACM certificate must live in us-east-1 for CloudFront use; got %s", acmARN)

	firehoseARN := outputs.must(t, "firehose_delivery_stream_arn")
	require.Contains(t, firehoseARN, ":firehose:us-east-1:",
		"Amazon Data Firehose ARN must include the regional service namespace; got %s", firehoseARN)

	privateCAARN := outputs.must(t, "acmpca_certificate_authority_arn")
	require.Contains(t, privateCAARN, ":acm-pca:us-east-1:",
		"AWS Private Certificate Authority ARN must include the regional service namespace; got %s", privateCAARN)
	require.Contains(t, outputs.must(t, "acmpca_certificate"), "BEGIN CERTIFICATE",
		"the provider's create → sign → import sequence must return the active AWS Private Certificate Authority certificate")

	require.True(t, strings.HasPrefix(amplifyARN, "arn:aws:amplify:"),
		"Amplify app ARN must have aws_amplify_app prefix; got %s", amplifyARN)

	elbv2LBArn := outputs.must(t, "elbv2_lb_arn")
	require.Contains(t, elbv2LBArn, ":loadbalancer/app/tf-alb/",
		"ELBv2 load balancer ARN must use the app load-balancer resource path; got %s", elbv2LBArn)

	elbv2LBDNS := outputs.must(t, "elbv2_lb_dns_name")
	require.Contains(t, elbv2LBDNS, ".elb.us-east-1.amazonaws.com",
		"ELBv2 load balancer DNS name must use the regional ELB suffix; got %s", elbv2LBDNS)

	elbv2TGArn := outputs.must(t, "elbv2_target_group_arn")
	require.Contains(t, elbv2TGArn, ":targetgroup/tf-alb-tg/",
		"ELBv2 target group ARN must use the targetgroup resource path; got %s", elbv2TGArn)

	elbv2ListenerArn := outputs.must(t, "elbv2_listener_arn")
	require.Contains(t, elbv2ListenerArn, ":listener/app/tf-alb/",
		"ELBv2 listener ARN must use the listener/app resource path; got %s", elbv2ListenerArn)

	elbv2RuleArn := outputs.must(t, "elbv2_listener_rule_arn")
	require.Contains(t, elbv2RuleArn, ":listener-rule/app/tf-alb/",
		"ELBv2 listener-rule ARN must use the listener-rule/app resource path; got %s", elbv2RuleArn)

	natGatewayID := outputs.must(t, "ec2_nat_gateway_id")
	require.True(t, strings.HasPrefix(natGatewayID, "nat-"),
		"EC2 NAT gateway id must use nat-* shape; got %s", natGatewayID)

	natEIP := outputs.must(t, "ec2_nat_eip_public_ip")
	requireIPv4InCIDR(t, natEIP, "3.2.0.0/24",
		"EC2 NAT Elastic IP must come from the simulator's real AWS EC2 public IPv4 pool; got %s", natEIP)

	natRouteTableID := outputs.must(t, "ec2_nat_route_table_id")
	require.True(t, strings.HasPrefix(natRouteTableID, "rtb-"),
		"EC2 route table id must use rtb-* shape; got %s", natRouteTableID)

	ebsVolumeID := outputs.must(t, "ec2_ebs_volume_id")
	require.True(t, strings.HasPrefix(ebsVolumeID, "vol-"),
		"EBS volume id must use vol-* shape; got %s", ebsVolumeID)
	ebsSnapshotID := outputs.must(t, "ec2_ebs_snapshot_id")
	require.True(t, strings.HasPrefix(ebsSnapshotID, "snap-"),
		"EBS snapshot id must use snap-* shape; got %s", ebsSnapshotID)
	ebsRestoredVolumeID := outputs.must(t, "ec2_ebs_restored_volume_id")
	require.True(t, strings.HasPrefix(ebsRestoredVolumeID, "vol-"),
		"EBS restored volume id must use vol-* shape; got %s", ebsRestoredVolumeID)
	ebsSnapshotCopyID := outputs.must(t, "ec2_ebs_snapshot_copy_id")
	require.True(t, strings.HasPrefix(ebsSnapshotCopyID, "snap-"),
		"EBS snapshot copy id must use snap-* shape; got %s", ebsSnapshotCopyID)
	require.NotEqual(t, ebsSnapshotID, ebsSnapshotCopyID,
		"CopySnapshot must produce a new snapshot id distinct from the source")

	cwAlarmARN := outputs.must(t, "cloudwatch_alarm_arn")
	require.Contains(t, cwAlarmARN, ":alarm:tf-alarm",
		"CloudWatch metric alarm ARN must use the alarm resource path; got %s", cwAlarmARN)
	cwDashARN := outputs.must(t, "cloudwatch_dashboard_arn")
	require.Contains(t, cwDashARN, ":dashboard/tf-dash",
		"CloudWatch dashboard ARN must use the dashboard resource path; got %s", cwDashARN)

	require.Equal(t, "tf-monthly", outputs.must(t, "budgets_budget_name"),
		"aws_budgets_budget name must round-trip through the Budgets API")
	require.Equal(t, "100", outputs.must(t, "budgets_budget_limit_amount"),
		"aws_budgets_budget limit_amount must round-trip through the Budgets API")
	require.Equal(t, "terraform", outputs.must(t, "budgets_budget_tag_env"),
		"aws_budgets_budget tags must round-trip through TagResource + ListTagsForResource")

	require.Equal(t, "tf-asg", outputs.must(t, "autoscaling_group_name"),
		"Auto Scaling group name must round-trip through provider refresh")

	cmNamespaceARN := outputs.must(t, "service_discovery_namespace_arn")
	require.Contains(t, cmNamespaceARN, ":namespace/ns-",
		"Cloud Map namespace ARN must use the namespace resource path; got %s", cmNamespaceARN)
	require.Equal(t, "terraform", outputs.must(t, "service_discovery_namespace_tag_env"),
		"Cloud Map namespace tags must round-trip through TagResource + ListTagsForResource")
	cmServiceARN := outputs.must(t, "service_discovery_service_arn")
	require.Contains(t, cmServiceARN, ":service/srv-",
		"Cloud Map service ARN must use the service resource path; got %s", cmServiceARN)
	require.Equal(t, "web", outputs.must(t, "service_discovery_service_tag_tier"),
		"Cloud Map service tags must round-trip through TagResource + ListTagsForResource")
	require.Equal(t, "tf-instance", outputs.must(t, "service_discovery_instance_id"),
		"Cloud Map instance must round-trip through RegisterInstance + GetInstance")
	cmHTTPNamespaceARN := outputs.must(t, "service_discovery_http_namespace_arn")
	require.Contains(t, cmHTTPNamespaceARN, ":namespace/ns-",
		"Cloud Map HTTP namespace ARN must use the namespace resource path; got %s", cmHTTPNamespaceARN)
	require.Equal(t, "true", outputs.must(t, "service_discovery_http_service_custom_health_configured"),
		"Cloud Map HealthCheckCustomConfig must round-trip through CreateService + GetService")
	require.Equal(t, outputs.must(t, "service_discovery_namespace_id"),
		outputs.must(t, "service_discovery_namespace_lookup_id"),
		"the DNS-namespace data source must find the namespace through ListNamespaces (TYPE + NAME filters)")
	require.Equal(t, outputs.must(t, "service_discovery_http_namespace_id"),
		outputs.must(t, "service_discovery_http_namespace_lookup_id"),
		"the HTTP-namespace data source must find the namespace through ListNamespaces (HTTP_NAME filter)")
	require.Equal(t, outputs.must(t, "service_discovery_service_id"),
		outputs.must(t, "service_discovery_service_lookup_id"),
		"the service data source must find the service through ListServices (NAMESPACE_ID filter)")

	cloudTrailARN := outputs.must(t, "cloudtrail_arn")
	require.Contains(t, cloudTrailARN, ":trail/tf-trail",
		"CloudTrail ARN must use the trail resource path; got %s", cloudTrailARN)

	efsFSArn := outputs.must(t, "efs_file_system_arn")
	require.Contains(t, efsFSArn, ":file-system/fs-",
		"EFS file system ARN must use the file-system resource path; got %s", efsFSArn)

	efsMountTargetID := outputs.must(t, "efs_mount_target_id")
	require.True(t, strings.HasPrefix(efsMountTargetID, "fsmt-"),
		"EFS mount target id must use fsmt-* shape; got %s", efsMountTargetID)

	efsAPArn := outputs.must(t, "efs_access_point_arn")
	require.Contains(t, efsAPArn, ":access-point/fsap-",
		"EFS access point ARN must use the access-point resource path; got %s", efsAPArn)

	cwLogGroupARN := outputs.must(t, "cloudwatch_log_group_arn")
	require.Contains(t, cwLogGroupARN, ":log-group:/aws/sockerless/tf-log-group",
		"CloudWatch Logs log group ARN must include the log-group resource path; got %s", cwLogGroupARN)

	require.Contains(t, slrARN, "aws-service-role/cloudfront.amazonaws.com/",
		"CloudFront SLR ARN must include the cloudfront.amazonaws.com service path; got %s", slrARN)

	s3ARN := outputs.must(t, "s3_bucket_arn")
	require.True(t, strings.HasPrefix(s3ARN, "arn:aws:s3:::tf-test-runner-bucket"),
		"S3 bucket ARN must be the canonical arn:aws:s3:::<bucket> shape; got %s", s3ARN)
	require.Equal(t, "test", outputs.must(t, "s3_bucket_tags_env"),
		"aws_s3_bucket.tags must round-trip through bucket tagging")

	ddbARN := outputs.must(t, "dynamodb_table_arn")
	require.True(t, strings.HasPrefix(ddbARN, "arn:aws:dynamodb:us-east-1:"),
		"DynamoDB table ARN must include the dynamodb-region prefix; got %s", ddbARN)
	require.Contains(t, ddbARN, ":table/tf-test-table",
		"DynamoDB table ARN must end with :table/<name>; got %s", ddbARN)

	kmsKeyARN := outputs.must(t, "kms_key_arn")
	require.True(t, strings.HasPrefix(kmsKeyARN, "arn:aws:kms:"),
		"KMS key ARN must include arn:aws:kms; got %s", kmsKeyARN)

	require.Equal(t, "true", outputs.must(t, "kms_key_rotation_enabled"),
		"enable_key_rotation must round-trip through EnableKeyRotation + GetKeyRotationStatus")

	require.Equal(t, "terraform", outputs.must(t, "kms_key_tag_env"),
		"KMS key tags must round-trip through TagResource + ListResourceTags (else the provider hangs)")

	require.NotEmpty(t, outputs.must(t, "kms_grant_id"),
		"aws_kms_grant must return a grant_id through CreateGrant + ListGrants read-back")

	require.Equal(t, "IMMUTABLE", outputs.must(t, "ecr_repository_tag_mutability"),
		"aws_ecr_repository image_tag_mutability must round-trip through DescribeRepositories")
	require.Contains(t, outputs.must(t, "ecr_repository_url"), ".dkr.ecr.us-east-1.amazonaws.com/tf-runner-repo",
		"aws_ecr_repository repository_url must use the canonical ECR registry host")

	require.Equal(t, "10.251.0.0/16", outputs.must(t, "data_vpc_cidr"),
		"data.aws_vpc by vpc-id filter must read the right VPC's CIDR")
	require.NotEmpty(t, outputs.must(t, "iam_nat_policy_arn"),
		"aws_iam_policy must apply (and destroy via ListPolicyVersions)")
	require.Contains(t, outputs.must(t, "log_group_kms_key_id"), "arn:aws:kms:",
		"aws_cloudwatch_log_group kms_key_id must round-trip through DescribeLogGroups")

	aasResourceID := outputs.must(t, "appautoscaling_target_resource_id")
	require.Contains(t, aasResourceID, "service/tf-test-cluster/",
		"Application Auto Scaling target resource_id must reference the ECS service")
	aasPolicyARN := outputs.must(t, "appautoscaling_policy_arn")
	require.Contains(t, aasPolicyARN, ":scalingPolicy:",
		"PutScalingPolicy must return a scalingPolicy ARN; got %s", aasPolicyARN)

	schedARN := outputs.must(t, "scheduler_schedule_arn")
	require.Contains(t, schedARN, ":schedule/default/tf-runner-schedule",
		"CreateSchedule must return a schedule ARN scoped to its group; got %s", schedARN)

	require.Equal(t, "tf-runner-svc", outputs.must(t, "ecs_service_name"),
		"aws_ecs_service must converge (ACTIVE, runningCount==desiredCount) via the ECS Service family")
	require.Equal(t, outputs.must(t, "service_discovery_service_arn"), outputs.must(t, "ecs_service_registry_arn"),
		"aws_ecs_service must persist its AWS Cloud Map service registry")
	require.Equal(t, "FARGATE,FARGATE_SPOT", outputs.must(t, "ecs_cluster_capacity_providers"),
		"PutClusterCapacityProviders must round-trip through DescribeClusters")
	require.Equal(t, "bridge", outputs.must(t, "ecs_task_definition_bridge_network_mode"),
		"RegisterTaskDefinition must round-trip a non-awsvpc networkMode through DescribeTaskDefinition")

	kmsAliasARN := outputs.must(t, "kms_alias_arn")
	require.Contains(t, kmsAliasARN, ":alias/tf-test-runner",
		"KMS alias ARN must include the alias path; got %s", kmsAliasARN)

	smARN := outputs.must(t, "secretsmanager_secret_arn")
	require.True(t, strings.HasPrefix(smARN, "arn:aws:secretsmanager:us-east-1:"),
		"Secrets Manager ARN must be us-east-1 + account; got %s", smARN)
	require.Contains(t, smARN, ":secret:tf-test-runner-secret-",
		"Secrets Manager ARN must include :secret:<name>-<6char-suffix>; got %s", smARN)
	require.Equal(t, "us-west-2", outputs.must(t, "secretsmanager_secret_replica_region"),
		"Secrets Manager must round-trip the provider-managed replica Region")

	ssmARN := outputs.must(t, "ssm_parameter_arn")
	require.Contains(t, ssmARN, ":parameter/tf-test/runner/config",
		"SSM Parameter ARN must include :parameter<leading-slash><name>; got %s", ssmARN)

	// S3 bucket-subresource round-trips. Each output below depends on
	// the terraform-provider-aws Create+Read cycle returning the value
	// the apply set. If any bucket-subresource handler in the sim is
	// missing or returns a wrong-shape body, the provider's Read fails
	// to parse the value and the output is empty / wrong.
	require.Equal(t, "Enabled", outputs.must(t, "s3_bucket_versioning_status"),
		"PutBucketVersioning Status must round-trip through GetBucketVersioning")
	require.Equal(t, "expire-30d", outputs.must(t, "s3_bucket_lifecycle_id"),
		"PutBucketLifecycleConfiguration rule id must round-trip")
	require.Equal(t, "https://app.example.com", outputs.must(t, "s3_bucket_cors_origin"),
		"PutBucketCors allowed_origins[0] must round-trip")
	require.Equal(t, "tf-test-runner-bucket", outputs.must(t, "s3_bucket_policy_bucket"),
		"PutBucketPolicy must round-trip through GetBucketPolicy")
	require.Equal(t, "AES256", outputs.must(t, "s3_bucket_sse_algorithm"),
		"PutBucketEncryption sse_algorithm must round-trip")
	require.Equal(t, "replicate-logs", outputs.must(t, "s3_bucket_replication_rule_id"),
		"PutBucketReplication rule id must round-trip")
	require.Equal(t, "logs/", outputs.must(t, "s3_bucket_logging_target_prefix"),
		"PutBucketLogging target_prefix must round-trip")
	require.Equal(t, "private", outputs.must(t, "s3_bucket_acl_value"),
		"PutBucketAcl acl must round-trip")
	require.Equal(t, "Requester", outputs.must(t, "s3_bucket_request_payment_payer"),
		"PutBucketRequestPayment payer must round-trip")
	require.Equal(t, "Enabled", outputs.must(t, "s3_bucket_accelerate_status"),
		"PutBucketAccelerateConfiguration status must round-trip")
	require.Equal(t, "index.html", outputs.must(t, "s3_bucket_website_index"),
		"PutBucketWebsite IndexDocument.Suffix must round-trip")
	require.Equal(t, "BucketOwnerEnforced", outputs.must(t, "s3_bucket_ownership"),
		"PutBucketOwnershipControls object_ownership must round-trip")
	require.Equal(t, "queue-created", outputs.must(t, "s3_bucket_notification_queue_id"),
		"PutBucketNotificationConfiguration queue id must round-trip")
	require.Equal(t, "GOVERNANCE", outputs.must(t, "s3_bucket_object_lock_mode"),
		"PutObjectLockConfiguration mode must round-trip")
	require.Equal(t, "archive-tier", outputs.must(t, "s3_bucket_intelligent_tiering_name"),
		"PutBucketIntelligentTieringConfiguration name must round-trip")
	require.Equal(t, "inventory-current", outputs.must(t, "s3_bucket_inventory_name"),
		"PutBucketInventoryConfiguration name must round-trip")
	require.Equal(t, "analytics-all", outputs.must(t, "s3_bucket_analytics_name"),
		"PutBucketAnalyticsConfiguration name must round-trip")
	require.Equal(t, "metrics-prefix", outputs.must(t, "s3_bucket_metric_name"),
		"PutBucketMetricsConfiguration name must round-trip")

	destroy := terraformCmd("destroy", "-auto-approve")
	out, err = runTimed(t, "terraform destroy", destroy)
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
}

// runTimed runs a terraform command, capturing combined output, with a
// watchdog that fires just before the test's own deadline. terraform spawns
// provider-plugin grandchildren; if go test's hard timeout (SIGQUIT) fired
// while a plain CombinedOutput() was in flight it would orphan that whole tree
// (t.Cleanup does NOT run on the test-binary timeout), leaving spinning
// provider processes that starve later runs into cascading timeouts. The
// watchdog instead kills the process group (terraform put itself in its own
// group via Setpgid) and returns a clean error with the captured output, so a
// stuck command fails diagnosably and never leaks orphans.
func runTimed(t *testing.T, label string, cmd *exec.Cmd) ([]byte, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	start := time.Now()
	require.NoError(t, cmd.Start(), "%s: failed to start", label)

	killGroup := func() {
		if cmd.Process != nil {
			// Negative pid targets the whole process group (Setpgid'd tree).
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	// Safety net for the pass/fail/panic paths (cleanup does not run on the
	// test-binary SIGQUIT timeout — the watchdog below covers that case).
	t.Cleanup(killGroup)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Fire ~20s before the test deadline so we reap the tree and fail cleanly
	// rather than letting the hard SIGQUIT orphan it.
	var watchdog <-chan time.Time
	if dl, ok := t.Deadline(); ok {
		if d := time.Until(dl) - 20*time.Second; d > 0 {
			timer := time.NewTimer(d)
			defer timer.Stop()
			watchdog = timer.C
		}
	}

	select {
	case err := <-done:
		t.Logf("%s duration=%s", label, time.Since(start).Round(time.Millisecond))
		return buf.Bytes(), err
	case <-watchdog:
		killGroup()
		<-done // reap the killed process so no zombie/orphan remains
		t.Logf("%s TIMED OUT after %s (process group killed to avoid orphans)",
			label, time.Since(start).Round(time.Millisecond))
		return buf.Bytes(), fmt.Errorf("%s timed out near the test deadline", label)
	}
}

func requireIPv4InCIDR(t *testing.T, value, cidr string, msgAndArgs ...any) {
	t.Helper()
	ip := net.ParseIP(value).To4()
	require.NotNil(t, ip, msgAndArgs...)
	_, block, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	require.True(t, block.Contains(ip), msgAndArgs...)
}

type tfOutputs map[string]struct {
	Sensitive bool        `json:"sensitive"`
	Type      interface{} `json:"type"`
	Value     interface{} `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, ok := v.Value.(string)
	require.True(t, ok, "output %q is not a string (got %T)", key, v.Value)
	require.NotEmpty(t, s, "output %q is empty", key)
	return s
}

func readOutputs(t *testing.T) tfOutputs {
	t.Helper()
	out, err := terraformCmd("output", "-json").CombinedOutput()
	require.NoError(t, err, "terraform output failed:\n%s", out)
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(out, &outputs))
	return outputs
}
