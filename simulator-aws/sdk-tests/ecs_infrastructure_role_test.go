package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Amazon ECS acts as the roles a request hands it, so a role it cannot assume
// is refused where the request names it: a task's managedEBSVolume.roleArn and
// a service's role.
func TestECS_InfrastructureRolesMustBeAssumable(t *testing.T) {
	t.Parallel()
	ecsc := ecsClient()
	cluster := "infra-role-assumable"
	_, err := ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ecsc.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })
	subnetID := createECSTestSubnet(t, "infra-role-assumable")
	notTrusted := serviceRole(t, "lambda.amazonaws.com")

	volumeTask, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("infra-role-volume"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		Volumes:                 []ecstypes.Volume{{Name: aws.String("data"), ConfiguredAtLaunch: aws.Bool(true)}},
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:        aws.String("app"),
			Image:       aws.String("mirror.gcr.io/library/alpine:3.20"),
			MountPoints: []ecstypes.MountPoint{{SourceVolume: aws.String("data"), ContainerPath: aws.String("/data")}},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsc.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{TaskDefinition: volumeTask.TaskDefinition.TaskDefinitionArn})
	})
	for name, role := range map[string]string{
		"not trusted": notTrusted,
		"missing":     "arn:aws:iam::000000000000:role/infra-role-missing",
	} {
		_, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
			Cluster:        aws.String(cluster),
			TaskDefinition: volumeTask.TaskDefinition.TaskDefinitionArn,
			LaunchType:     ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{
				AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
			},
			VolumeConfigurations: []ecstypes.TaskVolumeConfiguration{{
				Name: aws.String("data"),
				ManagedEBSVolume: &ecstypes.TaskManagedEBSVolumeConfiguration{
					RoleArn:   aws.String(role),
					SizeInGiB: aws.Int32(1),
				},
			}},
		})
		assert.Equal(t, "InvalidParameterException", errCode(t, err), "RunTask with a %s volume role", name)
	}

	// A service's role lets Amazon ECS reach the load balancer; the model
	// permits it only with a load balancer and outside the awsvpc network mode.
	bridgeTask, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:      aws.String("infra-role-bridge"),
		NetworkMode: ecstypes.NetworkModeBridge,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:         aws.String("web"),
			Image:        aws.String("mirror.gcr.io/library/alpine:3.20"),
			Memory:       aws.Int32(128),
			PortMappings: []ecstypes.PortMapping{{ContainerPort: aws.Int32(80), HostPort: aws.Int32(0)}},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsc.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{TaskDefinition: bridgeTask.TaskDefinition.TaskDefinitionArn})
	})
	vpcs, err := ec2Client().DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{subnetID}})
	require.NoError(t, err)
	require.Len(t, vpcs.Subnets, 1)
	targetGroup, err := elbv2Client().CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:       aws.String("infra-role-tg"),
		Protocol:   elbtypes.ProtocolEnumHttp,
		Port:       aws.Int32(80),
		VpcId:      vpcs.Subnets[0].VpcId,
		TargetType: elbtypes.TargetTypeEnumInstance,
	})
	require.NoError(t, err)
	targetGroupArn := targetGroup.TargetGroups[0].TargetGroupArn
	t.Cleanup(func() {
		_, _ = elbv2Client().DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: targetGroupArn})
	})
	loadBalancers := []ecstypes.LoadBalancer{{
		TargetGroupArn: targetGroupArn, ContainerName: aws.String("web"), ContainerPort: aws.Int32(80),
	}}
	for name, in := range map[string]*ecs.CreateServiceInput{
		"not trusted": {TaskDefinition: bridgeTask.TaskDefinition.TaskDefinitionArn, Role: aws.String(notTrusted), LoadBalancers: loadBalancers},
		"missing":     {TaskDefinition: bridgeTask.TaskDefinition.TaskDefinitionArn, Role: aws.String("infra-role-missing"), LoadBalancers: loadBalancers},
		"without a load balancer": {
			TaskDefinition: bridgeTask.TaskDefinition.TaskDefinitionArn,
			Role:           aws.String(createServiceRole(t, uniquename.New("ecs-service"), "ecs.amazonaws.com", "elasticloadbalancing:*")),
		},
		"with the awsvpc network mode": {
			TaskDefinition: volumeTask.TaskDefinition.TaskDefinitionArn,
			Role:           aws.String(createServiceRole(t, uniquename.New("ecs-service"), "ecs.amazonaws.com", "elasticloadbalancing:*")),
			LoadBalancers: []ecstypes.LoadBalancer{{
				TargetGroupArn: targetGroupArn, ContainerName: aws.String("app"), ContainerPort: aws.Int32(80),
			}},
		},
	} {
		in.Cluster = aws.String(cluster)
		in.ServiceName = aws.String(uniquename.New("infra-role"))
		in.DesiredCount = aws.Int32(0)
		_, err := ecsc.CreateService(ctx, in)
		assert.Equal(t, "InvalidParameterException", errCode(t, err), "CreateService %s", name)
	}
}

// An Express service's infrastructure role is what Amazon ECS provisions the
// service's resources as: a role allowed none of them leaves every managed
// resource FAILED, with the refusal as its statusReason, while the service
// itself is created.
func TestECSExpress_ManagedResourcesFailAsTheInfrastructureRole(t *testing.T) {
	t.Parallel()
	ecsc := ecsClient()
	cluster := expressCreateCluster(t, ecsc, "express-refused")
	out, err := ecsc.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
		Cluster:               aws.String(cluster),
		ServiceName:           aws.String("refused"),
		InfrastructureRoleArn: aws.String(createServiceRole(t, uniquename.New("express-refused"), "ecs.amazonaws.com", "acm:*")),
		PrimaryContainer: &ecstypes.ExpressGatewayContainer{
			Image:         aws.String(containerCommandImage),
			ContainerPort: aws.Int32(8080),
			Command:       []string{"http", "8080", "express-ok"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsc.DeleteExpressGatewayService(ctx, &ecs.DeleteExpressGatewayServiceInput{ServiceArn: out.Service.ServiceArn})
	})
	require.NotNil(t, out.Service.Status)
	assert.Equal(t, ecstypes.ExpressGatewayServiceStatusCodeActive, out.Service.Status.StatusCode)
	assert.Contains(t, aws.ToString(out.Service.Status.StatusReason), "UnauthorizedOperation")
	require.NotEmpty(t, out.Service.ActiveConfigurations)
	assert.Empty(t, out.Service.ActiveConfigurations[0].IngressPaths, "a service without a load balancer has no ingress path")

	managed := expressManagedResources(t, ecsc, out.Service)
	require.Len(t, managed.IngressPaths, 1)
	path := managed.IngressPaths[0]
	require.NotNil(t, path.LoadBalancer)
	assert.Equal(t, ecstypes.ManagedResourceStatusFailed, path.LoadBalancer.Status)
	assert.Contains(t, aws.ToString(path.LoadBalancer.StatusReason), "ec2:CreateSecurityGroup")
	assert.Nil(t, path.Certificate, "nothing that needs the load balancer is attempted")
	assert.Empty(t, path.TargetGroups)
	require.NotNil(t, managed.AutoScaling)
	require.NotNil(t, managed.AutoScaling.ScalableTarget)
	assert.Equal(t, ecstypes.ManagedResourceStatusFailed, managed.AutoScaling.ScalableTarget.Status)
	assert.Contains(t, aws.ToString(managed.AutoScaling.ScalableTarget.StatusReason), "application-autoscaling:RegisterScalableTarget")

	described, err := ecsc.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(cluster), Services: []string{"refused"}})
	require.NoError(t, err)
	require.Len(t, described.Services, 1)
	assert.Equal(t, "ACTIVE", aws.ToString(described.Services[0].Status))
	assert.Empty(t, described.Services[0].LoadBalancers, "the service has no target group to bind")
}
