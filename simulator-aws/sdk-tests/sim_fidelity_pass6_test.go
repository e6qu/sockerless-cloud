package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_DescribeSecurityGroupRulesFilters covers the is-egress filter (only
// group-id was honored before, so a scoped query returned every rule).
func TestEC2_DescribeSecurityGroupRulesFilters(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.92.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("sgr-filter"), Description: aws.String("sgr-filter"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	gid := aws.ToString(sg.GroupId)

	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(gid),
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
			IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)
	// Revoke the default ALLOW ALL egress rule before authorizing a test egress
	// rule, matching the real AWS workflow for VPC security groups.
	_, err = c.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
		GroupId: aws.String(gid),
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("-1"), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)
	_, err = c.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: aws.String(gid),
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("-1"), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)

	egress, err := c.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-id"), Values: []string{gid}},
			{Name: aws.String("is-egress"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, egress.SecurityGroupRules, 1, "is-egress=true must return only the egress rule")
	assert.True(t, aws.ToBool(egress.SecurityGroupRules[0].IsEgress))

	ingress, err := c.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-id"), Values: []string{gid}},
			{Name: aws.String("is-egress"), Values: []string{"false"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, ingress.SecurityGroupRules, 1, "is-egress=false must return only the ingress rule")
	assert.False(t, aws.ToBool(ingress.SecurityGroupRules[0].IsEgress))
}

// TestEventBridge_CreateEventBusPersistsKMS covers the KmsKeyIdentifier
// round-trip: CreateEventBus echoed it but never stored it, so DescribeEventBus
// returned empty and terraform's aws_cloudwatch_event_bus drifted every plan.
func TestEventBridge_CreateEventBusPersistsKMS(t *testing.T) {
	c := eventbridgeClient()
	kms := "arn:aws:kms:us-east-1:123456789012:key/eb-bus-key"
	name := "fidelity-kms-bus"
	_, err := c.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{
		Name: aws.String(name), KmsKeyIdentifier: aws.String(kms),
	})
	require.NoError(t, err)
	defer c.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String(name)})

	out, err := c.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, kms, aws.ToString(out.KmsKeyIdentifier), "DescribeEventBus must return the stored KMS key")
}

// TestCloudMap_DiscoverInstancesHealth covers real per-instance health +
// the HealthStatus filter (the handler hardcoded HEALTHY and ignored the filter).
func TestCloudMap_DiscoverInstancesHealth(t *testing.T) {
	client := cmClient()
	createOut, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("health-test.local"), Vpc: aws.String("vpc-health"),
	})
	require.NoError(t, err)
	opOut, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{OperationId: createOut.OperationId})
	require.NoError(t, err)
	nsID := opOut.Operation.Targets["NAMESPACE"]
	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name: aws.String("api"), NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{DnsRecords: []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)}}},
	})
	require.NoError(t, err)
	svcID := aws.ToString(svcOut.Service.Id)

	reg := func(id, health string) {
		_, err := client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
			ServiceId: aws.String(svcID), InstanceId: aws.String(id),
			Attributes: map[string]string{"AWS_INSTANCE_IPV4": "10.0.0.9", "AWS_INIT_HEALTH_STATUS": health},
		})
		require.NoError(t, err)
	}
	reg("ok-1", "HEALTHY")
	reg("bad-1", "UNHEALTHY")

	healthy, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("health-test.local"), ServiceName: aws.String("api"),
		HealthStatus: sdtypes.HealthStatusFilterHealthy,
	})
	require.NoError(t, err)
	require.Len(t, healthy.Instances, 1, "HealthStatus=HEALTHY must scope to the healthy instance")
	assert.Equal(t, "ok-1", aws.ToString(healthy.Instances[0].InstanceId))
	assert.Equal(t, sdtypes.HealthStatusHealthy, healthy.Instances[0].HealthStatus)

	unhealthy, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("health-test.local"), ServiceName: aws.String("api"),
		HealthStatus: sdtypes.HealthStatusFilterUnhealthy,
	})
	require.NoError(t, err)
	require.Len(t, unhealthy.Instances, 1)
	assert.Equal(t, sdtypes.HealthStatusUnhealthy, unhealthy.Instances[0].HealthStatus,
		"unhealthy instance must report its real status, not a hardcoded HEALTHY")

	all, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("health-test.local"), ServiceName: aws.String("api"),
		HealthStatus: sdtypes.HealthStatusFilterAll,
	})
	require.NoError(t, err)
	assert.Len(t, all.Instances, 2, "HealthStatus=ALL returns both")
}

// TestLambda_GetFunctionRepositoryType covers the documented Code.RepositoryType
// field ("S3" for a ZIP package), previously omitted from GetFunction.
func TestLambda_GetFunctionRepositoryType(t *testing.T) {
	lc := lambdaClient()
	name := "fidelity-repotype"
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})

	out, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, out.Code)
	assert.Equal(t, "S3", aws.ToString(out.Code.RepositoryType), "a ZIP function reports RepositoryType S3")
}

// TestECS_ListTasksLaunchTypeFilter covers the launchType filter, previously
// ignored (only family + desiredStatus were honored).
func TestECS_ListTasksLaunchTypeFilter(t *testing.T) {
	c := ecsClient()
	cluster := "fidelity-launchtype"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)

	td, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("launchtype-probe"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("main"),
			Image:      aws.String(busyboxImage),
			EntryPoint: []string{"sh", "-c"},
			Command:    []string{"sleep 5"},
		}},
	})
	require.NoError(t, err)
	subnet := createECSTestSubnet(t, "launchtype")

	run, err := c.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: td.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnet}},
		},
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := aws.ToString(run.Tasks[0].TaskArn)
	defer c.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: aws.String(taskArn)})

	fargate, err := c.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster), LaunchType: ecstypes.LaunchTypeFargate})
	require.NoError(t, err)
	assert.Contains(t, fargate.TaskArns, taskArn, "FARGATE filter must include the FARGATE task")

	ec2lt, err := c.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster), LaunchType: ecstypes.LaunchTypeEc2})
	require.NoError(t, err)
	assert.NotContains(t, ec2lt.TaskArns, taskArn, "EC2 filter must exclude the FARGATE task")
}
