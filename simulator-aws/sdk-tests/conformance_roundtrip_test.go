package aws_sdk_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigwv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	batchtypes "github.com/aws/aws-sdk-go-v2/service/batch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	wafv2types "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DescribeAutoScalingGroups returns the group ARN and the defaulted
// HealthCheckType terraform reads back.
func TestConformanceAutoScalingGroupARNAndHealthCheck(t *testing.T) {
	t.Parallel()
	c := autoScalingClient()
	ec2c := ec2Client()
	lcName := "conf-asg-lc"
	asgName := "conf-asg-roundtrip"

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.91.0.0/16")})
	require.NoError(t, err)
	subnetOut, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.91.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	_, err = c.CreateLaunchConfiguration(ctx, &autoscaling.CreateLaunchConfigurationInput{
		LaunchConfigurationName: aws.String(lcName),
		ImageId:                 aws.String(ec2AmazonLinuxAMI(t)),
		InstanceType:            aws.String("t3.micro"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteLaunchConfiguration(ctx, &autoscaling.DeleteLaunchConfigurationInput{
			LaunchConfigurationName: aws.String(lcName),
		})
	})

	_, err = c.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName:    aws.String(asgName),
		LaunchConfigurationName: aws.String(lcName),
		MinSize:                 aws.Int32(1),
		MaxSize:                 aws.Int32(3),
		DesiredCapacity:         aws.Int32(1),
		VPCZoneIdentifier:       subnetOut.Subnet.SubnetId,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{
			AutoScalingGroupName: aws.String(asgName),
			ForceDelete:          aws.Bool(true),
		})
	})

	out, err := c.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{asgName},
	})
	require.NoError(t, err)
	require.Len(t, out.AutoScalingGroups, 1)
	g := out.AutoScalingGroups[0]
	arn := aws.ToString(g.AutoScalingGroupARN)
	require.NotEmpty(t, arn, "DescribeAutoScalingGroups must return AutoScalingGroupARN")
	assert.Contains(t, arn, ":autoScalingGroup:")
	assert.Equal(t, "EC2", aws.ToString(g.HealthCheckType))
}

// DescribeJobs returns the jobArn SubmitJob answered with.
func TestConformanceBatchDescribeJobsArn(t *testing.T) {
	t.Parallel()
	c := batchClient()

	_, err := c.CreateComputeEnvironment(ctx, &batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: aws.String("conf-batch-ce"),
		Type:                   batchtypes.CETypeManaged,
		State:                  batchtypes.CEStateEnabled,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteComputeEnvironment(ctx, &batch.DeleteComputeEnvironmentInput{
			ComputeEnvironment: aws.String("conf-batch-ce"),
		})
	})

	_, err = c.CreateJobQueue(ctx, &batch.CreateJobQueueInput{
		JobQueueName: aws.String("conf-batch-q"),
		Priority:     aws.Int32(1),
		ComputeEnvironmentOrder: []batchtypes.ComputeEnvironmentOrder{
			{Order: aws.Int32(1), ComputeEnvironment: aws.String("conf-batch-ce")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteJobQueue(ctx, &batch.DeleteJobQueueInput{JobQueue: aws.String("conf-batch-q")})
	})

	reg, err := c.RegisterJobDefinition(ctx, &batch.RegisterJobDefinitionInput{
		JobDefinitionName: aws.String("conf-batch-jd"),
		Type:              batchtypes.JobDefinitionTypeContainer,
		ContainerProperties: &batchtypes.ContainerProperties{
			Image:   aws.String("mirror.gcr.io/library/busybox:latest"),
			Command: []string{"true"},
			ResourceRequirements: []batchtypes.ResourceRequirement{
				{Type: batchtypes.ResourceTypeVcpu, Value: aws.String("1")},
				{Type: batchtypes.ResourceTypeMemory, Value: aws.String("512")},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeregisterJobDefinition(ctx, &batch.DeregisterJobDefinitionInput{
			JobDefinition: reg.JobDefinitionArn,
		})
	})

	submit, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName:       aws.String("conf-batch-job"),
		JobQueue:      aws.String("conf-batch-q"),
		JobDefinition: reg.JobDefinitionArn,
	})
	require.NoError(t, err)
	jobID := aws.ToString(submit.JobId)
	require.NotEmpty(t, jobID)

	desc, err := c.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: []string{jobID}})
	require.NoError(t, err)
	require.Len(t, desc.Jobs, 1)
	require.NotEmpty(t, aws.ToString(desc.Jobs[0].JobArn), "DescribeJobs must return jobArn")
}

// CreateRestApi returns the root resource id it creates, which CreateResource
// takes as ParentId.
func TestConformanceAPIGatewayRootResourceId(t *testing.T) {
	t.Parallel()
	c := apigwClient()
	create, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String("conf-rest-root"),
	})
	require.NoError(t, err)
	apiId := aws.ToString(create.Id)
	require.NotEmpty(t, apiId)
	t.Cleanup(func() {
		_, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(apiId)})
	})

	rootId := aws.ToString(create.RootResourceId)
	require.NotEmpty(t, rootId, "CreateRestApi must return RootResourceId")

	get, err := c.GetRestApi(ctx, &apigateway.GetRestApiInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	assert.Equal(t, rootId, aws.ToString(get.RootResourceId))

	child, err := c.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: aws.String(apiId),
		ParentId:  aws.String(rootId),
		PathPart:  aws.String("hello"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(child.Id))
}

func TestConformanceAPIGatewayV2ApiEndpoint(t *testing.T) {
	t.Parallel()
	c := apigwv2Client()
	create, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String("conf-http-api"),
		ProtocolType: apigwv2types.ProtocolTypeHttp,
	})
	require.NoError(t, err)
	apiId := aws.ToString(create.ApiId)
	require.NotEmpty(t, apiId)
	t.Cleanup(func() {
		_, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)})
	})
	require.NotEmpty(t, aws.ToString(create.ApiEndpoint), "CreateApi must return ApiEndpoint")
	assert.Contains(t, aws.ToString(create.ApiEndpoint), apiId)
}

// RequestCertificate without Options defaults CT logging to ENABLED, as
// DescribeCertificate reports.
func TestConformanceACMDefaultCertificateOptions(t *testing.T) {
	t.Parallel()
	c := acmClient()
	req, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("conf-acm.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	arn := aws.ToString(req.CertificateArn)
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		_, _ = c.DeleteCertificate(ctx, &acm.DeleteCertificateInput{CertificateArn: aws.String(arn)})
	})

	desc, err := c.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	require.NotNil(t, desc.Certificate)
	require.NotNil(t, desc.Certificate.Options, "DescribeCertificate must return Options")
	assert.Equal(t,
		acmtypes.CertificateTransparencyLoggingPreferenceEnabled,
		desc.Certificate.Options.CertificateTransparencyLoggingPreference)
}

// An unencrypted stream reports EncryptionType=NONE, not an empty value.
func TestConformanceKinesisEncryptionTypeNone(t *testing.T) {
	t.Parallel()
	c := kinesisClient()
	streamName := "conf-kinesis-enc"
	_, err := c.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})

	summary, err := c.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	require.NotNil(t, summary.StreamDescriptionSummary)
	assert.Equal(t, ktypes.EncryptionTypeNone, summary.StreamDescriptionSummary.EncryptionType)
	assert.Empty(t, strings.TrimSpace(aws.ToString(summary.StreamDescriptionSummary.KeyId)))
}

// GetFunction returns an Image function's image config under
// ImageConfigResponse, the field the SDK's FunctionConfiguration reads.
func TestConformanceLambdaImageConfigResponse(t *testing.T) {
	t.Parallel()
	c := lambdaClient()
	fnName := "conf-lambda-imageconfig"
	_, err := c.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String(serviceRole(t, "lambda.amazonaws.com")),
		PackageType:  lambdatypes.PackageTypeImage,
		Code: &lambdatypes.FunctionCode{
			ImageUri: aws.String("123456789012.dkr.ecr.us-east-1.amazonaws.com/x:latest"),
		},
		ImageConfig: &lambdatypes.ImageConfig{
			Command:    []string{"/bin/sh"},
			EntryPoint: []string{"x"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})

	out, err := c.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	require.NotNil(t, out.Configuration)
	require.NotNil(t, out.Configuration.ImageConfigResponse,
		"GetFunction must return ImageConfigResponse")
	require.NotNil(t, out.Configuration.ImageConfigResponse.ImageConfig,
		"ImageConfigResponse must carry ImageConfig")
	assert.Equal(t, []string{"/bin/sh"}, out.Configuration.ImageConfigResponse.ImageConfig.Command)
	assert.Equal(t, []string{"x"}, out.Configuration.ImageConfigResponse.ImageConfig.EntryPoint)
}

// UpdateItem honours ReturnValues: the default NONE returns no Attributes,
// ALL_NEW the new item, UPDATED_NEW the changed attributes, ALL_OLD the
// pre-update item.
func TestConformanceDynamoDBUpdateItemReturnValues(t *testing.T) {
	t.Parallel()
	c := ddbClient()
	tableName := "conf-ddb-returnvalues"
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})
	})

	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item: map[string]ddbtypes.AttributeValue{
			"id":   &ddbtypes.AttributeValueMemberS{Value: "k1"},
			"val":  &ddbtypes.AttributeValueMemberS{Value: "before"},
			"keep": &ddbtypes.AttributeValueMemberS{Value: "same"},
		},
	})
	require.NoError(t, err)

	key := map[string]ddbtypes.AttributeValue{
		"id": &ddbtypes.AttributeValueMemberS{Value: "k1"},
	}

	noneOut, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tableName),
		Key:                       key,
		UpdateExpression:          aws.String("SET #a = :v"),
		ExpressionAttributeNames:  map[string]string{"#a": "val"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberS{Value: "n1"}},
	})
	require.NoError(t, err)
	assert.Empty(t, noneOut.Attributes, "default ReturnValues NONE must return no Attributes")

	allNewOut, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tableName),
		Key:                       key,
		UpdateExpression:          aws.String("SET #a = :v"),
		ExpressionAttributeNames:  map[string]string{"#a": "val"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberS{Value: "n2"}},
		ReturnValues:              ddbtypes.ReturnValueAllNew,
	})
	require.NoError(t, err)
	require.Contains(t, allNewOut.Attributes, "id")
	require.Contains(t, allNewOut.Attributes, "keep")
	valNew, ok := allNewOut.Attributes["val"].(*ddbtypes.AttributeValueMemberS)
	require.True(t, ok)
	assert.Equal(t, "n2", valNew.Value)

	updNewOut, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tableName),
		Key:                       key,
		UpdateExpression:          aws.String("SET #a = :v"),
		ExpressionAttributeNames:  map[string]string{"#a": "val"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberS{Value: "n3"}},
		ReturnValues:              ddbtypes.ReturnValueUpdatedNew,
	})
	require.NoError(t, err)
	require.Contains(t, updNewOut.Attributes, "val")
	assert.NotContains(t, updNewOut.Attributes, "keep", "UPDATED_NEW must omit unchanged attrs")
	assert.NotContains(t, updNewOut.Attributes, "id", "UPDATED_NEW must omit unchanged key attrs")
	valUpd, ok := updNewOut.Attributes["val"].(*ddbtypes.AttributeValueMemberS)
	require.True(t, ok)
	assert.Equal(t, "n3", valUpd.Value)

	// val was n3 before this update.
	allOldOut, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tableName),
		Key:                       key,
		UpdateExpression:          aws.String("SET #a = :v"),
		ExpressionAttributeNames:  map[string]string{"#a": "val"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberS{Value: "n4"}},
		ReturnValues:              ddbtypes.ReturnValueAllOld,
	})
	require.NoError(t, err)
	valOld, ok := allOldOut.Attributes["val"].(*ddbtypes.AttributeValueMemberS)
	require.True(t, ok)
	assert.Equal(t, "n3", valOld.Value, "ALL_OLD must return the pre-update value")
}

// ListTargetsByRule returns the EcsParameters and RetryPolicy terraform reads
// back.
func TestConformanceEventBridgeTargetParameters(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()
	busName := "conf-eb-target-bus"
	ruleName := "conf-eb-target-rule"

	_, err := eb.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String(busName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String(busName)})
	})

	_, err = eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(ruleName),
		EventBusName: aws.String(busName),
		EventPattern: aws.String(`{"source":["conf.test"]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
			Rule: aws.String(ruleName), EventBusName: aws.String(busName), Ids: []string{"t1"},
		})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{
			Name: aws.String(ruleName), EventBusName: aws.String(busName),
		})
	})

	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule:         aws.String(ruleName),
		EventBusName: aws.String(busName),
		Targets: []ebtypes.Target{
			{
				Id:      aws.String("t1"),
				Arn:     aws.String("arn:aws:ecs:us-east-1:123456789012:cluster/conf"),
				RoleArn: aws.String("arn:aws:iam::123456789012:role/eb-role"),
				EcsParameters: &ebtypes.EcsParameters{
					TaskDefinitionArn: aws.String("arn:aws:ecs:us-east-1:123456789012:task-definition/x:1"),
					TaskCount:         aws.Int32(1),
					LaunchType:        ebtypes.LaunchTypeFargate,
				},
				RetryPolicy: &ebtypes.RetryPolicy{
					MaximumRetryAttempts: aws.Int32(2),
				},
			},
		},
	})
	require.NoError(t, err)

	out, err := eb.ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{
		Rule:         aws.String(ruleName),
		EventBusName: aws.String(busName),
	})
	require.NoError(t, err)
	require.Len(t, out.Targets, 1)
	tgt := out.Targets[0]
	require.NotNil(t, tgt.EcsParameters, "ListTargetsByRule must round-trip EcsParameters")
	assert.Equal(t, "arn:aws:ecs:us-east-1:123456789012:task-definition/x:1",
		aws.ToString(tgt.EcsParameters.TaskDefinitionArn))
	require.NotNil(t, tgt.RetryPolicy, "ListTargetsByRule must round-trip RetryPolicy")
	assert.Equal(t, int32(2), aws.ToInt32(tgt.RetryPolicy.MaximumRetryAttempts))
}

// Describing a load balancer, target group or listener by an identifier that
// does not exist raises the typed NotFound exception, not an empty 200, while
// an unfiltered Describe succeeds.
func TestConformanceELBv2DescribeMissingExplicitIdentifier(t *testing.T) {
	t.Parallel()
	c := elbv2Client()

	_, err := c.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{
			"arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/nope/0",
		},
	})
	var lbNotFound *elbtypes.LoadBalancerNotFoundException
	require.Error(t, err)
	require.True(t, errors.As(err, &lbNotFound),
		"DescribeLoadBalancers with a missing ARN must raise LoadBalancerNotFoundException, got %v", err)

	_, err = c.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
		TargetGroupArns: []string{
			"arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/nope/0",
		},
	})
	var tgNotFound *elbtypes.TargetGroupNotFoundException
	require.Error(t, err)
	require.True(t, errors.As(err, &tgNotFound),
		"DescribeTargetGroups with a missing ARN must raise TargetGroupNotFoundException, got %v", err)

	_, err = c.DescribeListeners(ctx, &elbv2.DescribeListenersInput{
		ListenerArns: []string{
			"arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/nope/0/0",
		},
	})
	var lsnNotFound *elbtypes.ListenerNotFoundException
	require.Error(t, err)
	require.True(t, errors.As(err, &lsnNotFound),
		"DescribeListeners with a missing ARN must raise ListenerNotFoundException, got %v", err)

	_, err = c.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{})
	require.NoError(t, err, "DescribeLoadBalancers with no filter must not error")
	_, err = c.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{})
	require.NoError(t, err, "DescribeTargetGroups with no filter must not error")
	_, err = c.DescribeListeners(ctx, &elbv2.DescribeListenersInput{
		LoadBalancerArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/listall/0"),
	})
	require.NoError(t, err, "DescribeListeners filtered by LoadBalancerArn must not error")
}

// DescribeServices on a missing cluster raises ClusterNotFoundException rather
// than per-service MISSING failures.
func TestConformanceECSDescribeServicesGhostCluster(t *testing.T) {
	t.Parallel()
	c := ecsClient()

	_, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String("ghost-cluster"),
		Services: []string{"x"},
	})
	var clusterNotFound *ecstypes.ClusterNotFoundException
	require.Error(t, err)
	require.True(t, errors.As(err, &clusterNotFound),
		"DescribeServices against a missing cluster must raise ClusterNotFoundException, got %v", err)
}

func TestConformanceIAMCreateRoleDuplicate(t *testing.T) {
	t.Parallel()
	c := iamClient()
	name := "conf-dup-role"
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

	_, err := c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(policy),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})

	_, err = c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(policy),
	})
	var dup *iamtypes.EntityAlreadyExistsException
	require.Error(t, err)
	require.True(t, errors.As(err, &dup),
		"CreateRole with a duplicate name must raise EntityAlreadyExistsException, got %v", err)
}

// AWS Batch client errors are typed ClientException, which the restJson1 SDK
// classifies.
func TestConformanceBatchTypedClientException(t *testing.T) {
	t.Parallel()
	c := batchClient()
	name := "conf-dup-ce"

	_, err := c.CreateComputeEnvironment(ctx, &batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: aws.String(name),
		Type:                   batchtypes.CETypeUnmanaged,
		State:                  batchtypes.CEStateEnabled,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteComputeEnvironment(ctx, &batch.DeleteComputeEnvironmentInput{
			ComputeEnvironment: aws.String(name),
		})
	})

	_, err = c.CreateComputeEnvironment(ctx, &batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: aws.String(name),
		Type:                   batchtypes.CETypeUnmanaged,
		State:                  batchtypes.CEStateEnabled,
	})
	var clientErr *batchtypes.ClientException
	require.Error(t, err)
	require.True(t, errors.As(err, &clientErr),
		"Batch client error must be typed as ClientException, got %v", err)
}

// GetService for a missing id answers HTTP 400 with the modeled awsJson1.1
// exception, which deserializes to ServiceNotFound.
func TestConformanceCloudMapServiceNotFoundStatus(t *testing.T) {
	t.Parallel()
	c := cmClient()

	_, err := c.GetService(ctx, &servicediscovery.GetServiceInput{
		Id: aws.String("srv-missing000000000"),
	})
	require.Error(t, err)

	var svcNotFound *sdtypes.ServiceNotFound
	require.True(t, errors.As(err, &svcNotFound),
		"GetService for a missing id must raise ServiceNotFound, got %v", err)

	var respErr *awshttp.ResponseError
	require.True(t, errors.As(err, &respErr), "expected an HTTP ResponseError, got %v", err)
	assert.Equal(t, http.StatusBadRequest, respErr.HTTPStatusCode(),
		"servicediscovery modeled errors must be HTTP 400, not 404")
}

func TestConformanceWAFv2CreateWebACLDuplicate(t *testing.T) {
	t.Parallel()
	c := wafClient()
	name := uniquename.New("conf-dup-acl")
	visibility := &wafv2types.VisibilityConfig{
		CloudWatchMetricsEnabled: true,
		MetricName:               aws.String(name),
		SampledRequestsEnabled:   true,
	}

	_, err := c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:             aws.String(name),
		Scope:            wafv2types.ScopeRegional,
		DefaultAction:    &wafv2types.DefaultAction{Allow: &wafv2types.AllowAction{}},
		VisibilityConfig: visibility,
	})
	require.NoError(t, err)

	_, err = c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:             aws.String(name),
		Scope:            wafv2types.ScopeRegional,
		DefaultAction:    &wafv2types.DefaultAction{Allow: &wafv2types.AllowAction{}},
		VisibilityConfig: visibility,
	})
	var dup *wafv2types.WAFDuplicateItemException
	require.Error(t, err)
	require.True(t, errors.As(err, &dup),
		"CreateWebACL with a duplicate name+scope must raise WAFDuplicateItemException, got %v", err)
}

// Each list operation below honours its page-size parameter: a page of one
// returns one item and a continuation token, following the token returns the
// rest and clears it, and no page size returns the whole list without a token.
// Where the result set is process-global, the parallel suite allows only a
// "more on the next page" assertion in place of the token clearing.

// ListAttachedRolePolicies is role-scoped, so the two attachments are the
// whole result set.
func TestConformanceIAMListAttachedRolePoliciesPagination(t *testing.T) {
	t.Parallel()
	c := iamClient()
	role := "conf-page-role"
	_, err := c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(role),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)}) })

	arns := []string{
		"arn:aws:iam::aws:policy/conf-page-A",
		"arn:aws:iam::aws:policy/conf-page-B",
	}
	for _, arn := range arns {
		_, err = c.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
			RoleName: aws.String(role), PolicyArn: aws.String(arn),
		})
		require.NoError(t, err)
		a := arn
		t.Cleanup(func() {
			_, _ = c.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(role), PolicyArn: aws.String(a)})
		})
	}

	full, err := c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(role)})
	require.NoError(t, err)
	require.Len(t, full.AttachedPolicies, 2)
	assert.False(t, full.IsTruncated)
	assert.Empty(t, aws.ToString(full.Marker))

	page1, err := c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(role), MaxItems: aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, page1.AttachedPolicies, 1)
	assert.True(t, page1.IsTruncated)
	require.NotEmpty(t, aws.ToString(page1.Marker))

	page2, err := c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(role), MaxItems: aws.Int32(1), Marker: page1.Marker,
	})
	require.NoError(t, err)
	require.Len(t, page2.AttachedPolicies, 1)
	assert.False(t, page2.IsTruncated)
	assert.Empty(t, aws.ToString(page2.Marker))
	assert.NotEqual(t,
		aws.ToString(page1.AttachedPolicies[0].PolicyArn),
		aws.ToString(page2.AttachedPolicies[0].PolicyArn),
		"the second page must surface the other attachment")
}

// ListPolicies is account-global, so assert the structural contract.
func TestConformanceIAMListPoliciesPagination(t *testing.T) {
	t.Parallel()
	c := iamClient()
	for _, name := range []string{"conf-lp-page-1", "conf-lp-page-2"} {
		n := name
		_, err := c.CreatePolicy(ctx, &iam.CreatePolicyInput{
			PolicyName:     aws.String(n),
			PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.DeletePolicy(ctx, &iam.DeletePolicyInput{
				PolicyArn: aws.String("arn:aws:iam::123456789012:policy/" + n),
			})
		})
	}

	page1, err := c.ListPolicies(ctx, &iam.ListPoliciesInput{MaxItems: aws.Int32(1)})
	require.NoError(t, err)
	require.Len(t, page1.Policies, 1)
	assert.True(t, page1.IsTruncated)
	require.NotEmpty(t, aws.ToString(page1.Marker))

	page2, err := c.ListPolicies(ctx, &iam.ListPoliciesInput{MaxItems: aws.Int32(1), Marker: page1.Marker})
	require.NoError(t, err)
	require.Len(t, page2.Policies, 1)
	assert.NotEqual(t, aws.ToString(page1.Policies[0].Arn), aws.ToString(page2.Policies[0].Arn))
}

// ListInstanceProfiles is account-global, so assert the structural contract.
func TestConformanceIAMListInstanceProfilesPagination(t *testing.T) {
	t.Parallel()
	c := iamClient()
	for _, name := range []string{"conf-ip-page-1", "conf-ip-page-2"} {
		n := name
		_, err := c.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{InstanceProfileName: aws.String(n)})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{InstanceProfileName: aws.String(n)})
		})
	}

	page1, err := c.ListInstanceProfiles(ctx, &iam.ListInstanceProfilesInput{MaxItems: aws.Int32(1)})
	require.NoError(t, err)
	require.Len(t, page1.InstanceProfiles, 1)
	assert.True(t, page1.IsTruncated)
	require.NotEmpty(t, aws.ToString(page1.Marker))

	page2, err := c.ListInstanceProfiles(ctx, &iam.ListInstanceProfilesInput{MaxItems: aws.Int32(1), Marker: page1.Marker})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(page2.InstanceProfiles), 1)
	assert.NotEqual(t,
		aws.ToString(page1.InstanceProfiles[0].InstanceProfileName),
		aws.ToString(page2.InstanceProfiles[0].InstanceProfileName))
}

// ListRules is event-bus-scoped, so a fresh bus's two rules are the whole
// result set.
func TestConformanceEventBridgeListRulesPagination(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()
	bus := "conf-page-rules-bus"
	_, err := eb.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String(bus)})
	require.NoError(t, err)

	for _, rule := range []string{"conf-page-rule-a", "conf-page-rule-b"} {
		rn := rule
		_, err = eb.PutRule(ctx, &eventbridge.PutRuleInput{
			Name: aws.String(rn), EventBusName: aws.String(bus),
			EventPattern: aws.String(`{"source":["conf.page"]}`),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(rn), EventBusName: aws.String(bus)})
		})
	}
	t.Cleanup(func() { _, _ = eb.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String(bus)}) })

	full, err := eb.ListRules(ctx, &eventbridge.ListRulesInput{EventBusName: aws.String(bus)})
	require.NoError(t, err)
	require.Len(t, full.Rules, 2)
	assert.Empty(t, aws.ToString(full.NextToken))

	page1, err := eb.ListRules(ctx, &eventbridge.ListRulesInput{EventBusName: aws.String(bus), Limit: aws.Int32(1)})
	require.NoError(t, err)
	require.Len(t, page1.Rules, 1)
	require.NotEmpty(t, aws.ToString(page1.NextToken))

	page2, err := eb.ListRules(ctx, &eventbridge.ListRulesInput{
		EventBusName: aws.String(bus), Limit: aws.Int32(1), NextToken: page1.NextToken,
	})
	require.NoError(t, err)
	require.Len(t, page2.Rules, 1)
	assert.Empty(t, aws.ToString(page2.NextToken))
	assert.NotEqual(t, aws.ToString(page1.Rules[0].Name), aws.ToString(page2.Rules[0].Name))
}

// DescribeComputeEnvironments lists process-global results, so assert the
// structural contract.
func TestConformanceBatchDescribeComputeEnvironmentsPagination(t *testing.T) {
	t.Parallel()
	c := batchClient()
	for _, name := range []string{"conf-page-ce-1", "conf-page-ce-2"} {
		n := name
		_, err := c.CreateComputeEnvironment(ctx, &batch.CreateComputeEnvironmentInput{
			ComputeEnvironmentName: aws.String(n),
			Type:                   batchtypes.CETypeManaged,
			State:                  batchtypes.CEStateEnabled,
			ComputeResources: &batchtypes.ComputeResource{
				Type: batchtypes.CRTypeEc2, MinvCpus: aws.Int32(0), MaxvCpus: aws.Int32(16),
				Subnets: []string{"subnet-00000001"},
			},
			ServiceRole: aws.String(serviceRole(t, "batch.amazonaws.com")),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.DeleteComputeEnvironment(ctx, &batch.DeleteComputeEnvironmentInput{ComputeEnvironment: aws.String(n)})
		})
	}

	page1, err := c.DescribeComputeEnvironments(ctx, &batch.DescribeComputeEnvironmentsInput{MaxResults: aws.Int32(1)})
	require.NoError(t, err)
	require.Len(t, page1.ComputeEnvironments, 1)
	require.NotEmpty(t, aws.ToString(page1.NextToken))

	page2, err := c.DescribeComputeEnvironments(ctx, &batch.DescribeComputeEnvironmentsInput{
		MaxResults: aws.Int32(1), NextToken: page1.NextToken,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(page2.ComputeEnvironments), 1)
	assert.NotEqual(t,
		aws.ToString(page1.ComputeEnvironments[0].ComputeEnvironmentName),
		aws.ToString(page2.ComputeEnvironments[0].ComputeEnvironmentName))
}

// DescribeAutoScalingGroups lists process-global results, so assert the
// structural MaxRecords and NextToken contract.
func TestConformanceAutoScalingDescribeGroupsPagination(t *testing.T) {
	t.Parallel()
	c := autoScalingClient()
	lc := "conf-page-asg-lc"
	_, err := c.CreateLaunchConfiguration(ctx, &autoscaling.CreateLaunchConfigurationInput{
		LaunchConfigurationName: aws.String(lc),
		ImageId:                 aws.String(ec2AmazonLinuxAMI(t)),
		InstanceType:            aws.String("t3.micro"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteLaunchConfiguration(ctx, &autoscaling.DeleteLaunchConfigurationInput{LaunchConfigurationName: aws.String(lc)})
	})
	for _, name := range []string{"conf-page-asg-1", "conf-page-asg-2"} {
		n := name
		_, err = c.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
			AutoScalingGroupName:    aws.String(n),
			LaunchConfigurationName: aws.String(lc),
			MinSize:                 aws.Int32(0), MaxSize: aws.Int32(1),
			AvailabilityZones: []string{"us-east-1a"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{
				AutoScalingGroupName: aws.String(n), ForceDelete: aws.Bool(true),
			})
		})
	}

	page1, err := c.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{MaxRecords: aws.Int32(1)})
	require.NoError(t, err)
	require.Len(t, page1.AutoScalingGroups, 1)
	require.NotEmpty(t, aws.ToString(page1.NextToken))

	page2, err := c.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		MaxRecords: aws.Int32(1), NextToken: page1.NextToken,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(page2.AutoScalingGroups), 1)
	assert.NotEqual(t,
		aws.ToString(page1.AutoScalingGroups[0].AutoScalingGroupName),
		aws.ToString(page2.AutoScalingGroups[0].AutoScalingGroupName))
}

// DescribeScalableTargets is namespace and resource scoped, so the two targets
// are the whole result set.
func TestConformanceAppAutoScalingDescribeTargetsPagination(t *testing.T) {
	t.Parallel()
	c := appAutoScalingClient()
	const ns = aastypes.ServiceNamespaceEcs
	ids := []string{"service/conf-page/svc-a", "service/conf-page/svc-b"}
	for _, id := range ids {
		rid := id
		_, err := c.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
			ServiceNamespace:  ns,
			ResourceId:        aws.String(rid),
			ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
			MinCapacity:       aws.Int32(1), MaxCapacity: aws.Int32(4),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
				ServiceNamespace: ns, ResourceId: aws.String(rid),
				ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
			})
		})
	}

	full, err := c.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: ns, ResourceIds: ids,
	})
	require.NoError(t, err)
	require.Len(t, full.ScalableTargets, 2)
	assert.Empty(t, aws.ToString(full.NextToken))

	page1, err := c.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: ns, ResourceIds: ids, MaxResults: aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, page1.ScalableTargets, 1)
	require.NotEmpty(t, aws.ToString(page1.NextToken))

	page2, err := c.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: ns, ResourceIds: ids, MaxResults: aws.Int32(1), NextToken: page1.NextToken,
	})
	require.NoError(t, err)
	require.Len(t, page2.ScalableTargets, 1)
	assert.Empty(t, aws.ToString(page2.NextToken))
	assert.NotEqual(t,
		aws.ToString(page1.ScalableTargets[0].ResourceId),
		aws.ToString(page2.ScalableTargets[0].ResourceId))
}

// DescribeAccessPoints is file-system-scoped, so the two access points are the
// whole result set.
func TestConformanceEFSDescribeAccessPointsPagination(t *testing.T) {
	t.Parallel()
	c := efsClient()
	fs, err := c.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken:   aws.String("conf-page-ap-fs"),
		PerformanceMode: efstypes.PerformanceModeGeneralPurpose,
	})
	require.NoError(t, err)
	fsID := aws.ToString(fs.FileSystemId)
	t.Cleanup(func() { _, _ = c.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(fsID)}) })

	for i := 0; i < 2; i++ {
		ap, err := c.CreateAccessPoint(ctx, &efs.CreateAccessPointInput{FileSystemId: aws.String(fsID)})
		require.NoError(t, err)
		apID := aws.ToString(ap.AccessPointId)
		t.Cleanup(func() { _, _ = c.DeleteAccessPoint(ctx, &efs.DeleteAccessPointInput{AccessPointId: aws.String(apID)}) })
	}

	full, err := c.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{FileSystemId: aws.String(fsID)})
	require.NoError(t, err)
	require.Len(t, full.AccessPoints, 2)
	assert.Empty(t, aws.ToString(full.NextToken))

	page1, err := c.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
		FileSystemId: aws.String(fsID), MaxResults: aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, page1.AccessPoints, 1)
	require.NotEmpty(t, aws.ToString(page1.NextToken))

	page2, err := c.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
		FileSystemId: aws.String(fsID), MaxResults: aws.Int32(1), NextToken: page1.NextToken,
	})
	require.NoError(t, err)
	require.Len(t, page2.AccessPoints, 1)
	assert.Empty(t, aws.ToString(page2.NextToken))
	assert.NotEqual(t,
		aws.ToString(page1.AccessPoints[0].AccessPointId),
		aws.ToString(page2.AccessPoints[0].AccessPointId))
}

// DescribeFileSystems lists process-global results, so assert the structural
// Marker, MaxItems and NextMarker contract.
func TestConformanceEFSDescribeFileSystemsPagination(t *testing.T) {
	t.Parallel()
	c := efsClient()
	for i := 0; i < 2; i++ {
		fs, err := c.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
			CreationToken:   aws.String(fmt.Sprintf("conf-page-fs-%d", i)),
			PerformanceMode: efstypes.PerformanceModeGeneralPurpose,
		})
		require.NoError(t, err)
		fsID := aws.ToString(fs.FileSystemId)
		t.Cleanup(func() { _, _ = c.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(fsID)}) })
	}

	page1, err := c.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{MaxItems: aws.Int32(1)})
	require.NoError(t, err)
	require.Len(t, page1.FileSystems, 1)
	require.NotEmpty(t, aws.ToString(page1.NextMarker))

	page2, err := c.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{
		MaxItems: aws.Int32(1), Marker: page1.NextMarker,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(page2.FileSystems), 1)
	assert.NotEqual(t,
		aws.ToString(page1.FileSystems[0].FileSystemId),
		aws.ToString(page2.FileSystems[0].FileSystemId))
}

// LookupEvents reads a process-global, time-ordered event log, so assert the
// structural MaxResults and NextToken contract.
func TestConformanceCloudTrailLookupEventsPagination(t *testing.T) {
	t.Parallel()
	ct := cloudTrailClient()
	ecsc := ecsClient()
	for _, name := range []string{"conf-page-ct-1", "conf-page-ct-2"} {
		n := name
		_, err := ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(n)})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = ecsc.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(n)}) })
	}

	page1, err := ct.LookupEvents(ctx, &cloudtrail.LookupEventsInput{MaxResults: aws.Int32(1)})
	require.NoError(t, err)
	require.Len(t, page1.Events, 1)
	require.NotEmpty(t, aws.ToString(page1.NextToken))

	page2, err := ct.LookupEvents(ctx, &cloudtrail.LookupEventsInput{
		MaxResults: aws.Int32(1), NextToken: page1.NextToken,
	})
	require.NoError(t, err)
	require.Len(t, page2.Events, 1)
	assert.NotEqual(t, aws.ToString(page1.Events[0].EventId), aws.ToString(page2.Events[0].EventId))
}
