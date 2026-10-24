package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func expressCreateCluster(t *testing.T, c *ecs.Client, name string) string {
	t.Helper()
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)}) })
	return name
}

// createECSExpressInfrastructureRole creates the role an Express service
// provisions its load balancer, target group, listener rule, certificate,
// security groups and auto scaling as: trusted by ecs.amazonaws.com, and
// allowed those services.
func createECSExpressInfrastructureRole(t *testing.T, name string) string {
	t.Helper()
	return createServiceRole(t, uniqueName(name), "ecs.amazonaws.com",
		"elasticloadbalancing:*", "ec2:*", "acm:*", "application-autoscaling:*")
}

// expressManagedResources reads the resources Amazon ECS manages for the
// service's current revision, which DescribeServiceRevisions reports.
func expressManagedResources(t *testing.T, c *ecs.Client, svc *ecstypes.ECSExpressGatewayService) *ecstypes.ECSManagedResources {
	t.Helper()
	require.NotEmpty(t, svc.ActiveConfigurations)
	revision := svc.ActiveConfigurations[len(svc.ActiveConfigurations)-1].ServiceRevisionArn
	require.NotEmpty(t, aws.ToString(revision))
	out, err := c.DescribeServiceRevisions(ctx, &ecs.DescribeServiceRevisionsInput{ServiceRevisionArns: []string{aws.ToString(revision)}})
	require.NoError(t, err)
	require.Len(t, out.ServiceRevisions, 1, "failures: %v", out.Failures)
	require.NotNil(t, out.ServiceRevisions[0].EcsManagedResources)
	return out.ServiceRevisions[0].EcsManagedResources
}

// expressLoadBalancerArn is the load balancer that serves the service's
// ingress path, as the managed resources report it.
func expressLoadBalancerArn(t *testing.T, c *ecs.Client, svc *ecstypes.ECSExpressGatewayService) string {
	t.Helper()
	managed := expressManagedResources(t, c, svc)
	require.Len(t, managed.IngressPaths, 1)
	require.NotNil(t, managed.IngressPaths[0].LoadBalancer)
	assert.Equal(t, ecstypes.ManagedResourceStatusActive, managed.IngressPaths[0].LoadBalancer.Status,
		aws.ToString(managed.IngressPaths[0].LoadBalancer.StatusReason))
	return aws.ToString(managed.IngressPaths[0].LoadBalancer.Arn)
}

// TestECSExpress_CreateAssemblyAndLifecycle drives the full ECS Express Mode
// service lifecycle (Create/Describe/Update/Delete) through the ECS SDK and
// asserts both the control-plane response shape and the faithful assembly: the
// underlying ALB, scalable target, and Fargate service all exist via their own
// service APIs, and Delete tears them down.
func TestECSExpress_CreateAssemblyAndLifecycle(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	elb := elbv2Client()
	aa := appAutoScalingClient()
	cluster := expressCreateCluster(t, c, "express-cluster")
	// Isolate the lifecycle from the shared default VPC: other shard tests
	// launch into it, and the real-VPC tier plumbs task networking per VPC.
	vpcID, subnets := createECSTestVPCSubnets(t, "express-lifecycle", 2)
	sg, err := ec2Client().CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("express-lifecycle"),
		Description: aws.String("Amazon ECS Express Mode lifecycle test"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)
	// A caller-supplied group owns its rules: admit the load balancer's
	// health-check and forwarding path, which reaches the task ENI from the
	// VPC gateway coordinate.
	vpcs, err := ec2Client().DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	require.Len(t, vpcs.Vpcs, 1)
	_, err = ec2Client().AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sg.GroupId,
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(8080),
			ToPort:     aws.Int32(8080),
			IpRanges:   []ec2types.IpRange{{CidrIp: vpcs.Vpcs[0].CidrBlock}},
		}},
	})
	require.NoError(t, err)

	out, err := c.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
		Cluster:               aws.String(cluster),
		ServiceName:           aws.String("web"),
		InfrastructureRoleArn: aws.String(createECSExpressInfrastructureRole(t, "express-lifecycle")),
		NetworkConfiguration: &ecstypes.ExpressGatewayServiceNetworkConfiguration{
			Subnets:        subnets,
			SecurityGroups: []string{aws.ToString(sg.GroupId)},
		},
		PrimaryContainer: &ecstypes.ExpressGatewayContainer{
			Image:         aws.String(containerCommandImage),
			ContainerPort: aws.Int32(8080),
			Command:       []string{"http", "8080", "express-ok"},
		},
		Tags: []ecstypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	svc := out.Service
	require.NotNil(t, svc)

	require.NotNil(t, svc.ServiceArn)
	assert.NotEmpty(t, aws.ToString(svc.ServiceArn))
	require.NotNil(t, svc.Status)
	assert.Equal(t, ecstypes.ExpressGatewayServiceStatusCodeActive, svc.Status.StatusCode)
	require.NotEmpty(t, svc.ActiveConfigurations)

	cfg := svc.ActiveConfigurations[0]
	// Defaults applied.
	assert.Equal(t, "256", aws.ToString(cfg.Cpu))
	assert.Equal(t, "512", aws.ToString(cfg.Memory))
	assert.Equal(t, "/ping", aws.ToString(cfg.HealthCheckPath))

	// Ingress path: PUBLIC, an https endpoint in a domain AWS owns, served by
	// the load balancer the managed resources name.
	require.NotEmpty(t, cfg.IngressPaths)
	ingress := cfg.IngressPaths[0]
	assert.Equal(t, ecstypes.AccessTypePublic, ingress.AccessType)
	endpoint := aws.ToString(ingress.Endpoint)
	require.True(t, strings.HasPrefix(endpoint, "https://"), "endpoint %q must start https://", endpoint)
	assert.True(t, strings.HasSuffix(endpoint, ".on.aws"), endpoint)
	albArn := expressLoadBalancerArn(t, c, svc)
	lbs, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}})
	require.NoError(t, err)
	require.Len(t, lbs.LoadBalancers, 1)
	assert.Equal(t, "internet-facing", string(lbs.LoadBalancers[0].Scheme))

	// The Application Auto Scaling scalable target exists at service/<cluster>/<svc>.
	resourceID := "service/" + cluster + "/web"
	st, err := aa.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: aastypes.ServiceNamespaceEcs,
		ResourceIds:      []string{resourceID},
	})
	require.NoError(t, err)
	require.Len(t, st.ScalableTargets, 1, "expected scalable target for %s", resourceID)
	assert.Equal(t, resourceID, aws.ToString(st.ScalableTargets[0].ResourceId))

	// The backing ECS Fargate service exists.
	ds, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []string{"web"},
	})
	require.NoError(t, err)
	require.Len(t, ds.Services, 1)
	assert.Equal(t, "ACTIVE", aws.ToString(ds.Services[0].Status))
	assert.Equal(t, ecstypes.LaunchTypeFargate, ds.Services[0].LaunchType)
	initialTaskDefinition := aws.ToString(ds.Services[0].TaskDefinition)

	desc, err := c.DescribeExpressGatewayService(ctx, &ecs.DescribeExpressGatewayServiceInput{
		ServiceArn: svc.ServiceArn,
		Include:    []ecstypes.ExpressGatewayServiceInclude{ecstypes.ExpressGatewayServiceIncludeTags},
	})
	require.NoError(t, err)
	require.NotNil(t, desc.Service)
	require.Len(t, desc.Service.Tags, 1)
	assert.Equal(t, "env", aws.ToString(desc.Service.Tags[0].Key))
	assert.Equal(t, "test", aws.ToString(desc.Service.Tags[0].Value))

	// Without include, tags are absent.
	descNoTags, err := c.DescribeExpressGatewayService(ctx, &ecs.DescribeExpressGatewayServiceInput{
		ServiceArn: svc.ServiceArn,
	})
	require.NoError(t, err)
	assert.Empty(t, descNoTags.Service.Tags)

	upd, err := c.UpdateExpressGatewayService(ctx, &ecs.UpdateExpressGatewayServiceInput{
		ServiceArn: svc.ServiceArn,
		Cpu:        aws.String("512"),
		Memory:     aws.String("1024"),
		ScalingTarget: &ecstypes.ExpressGatewayScalingTarget{
			MinTaskCount:           aws.Int32(2),
			MaxTaskCount:           aws.Int32(8),
			AutoScalingMetric:      ecstypes.ExpressGatewayServiceScalingMetricAverageCPUUtilization,
			AutoScalingTargetValue: aws.Int32(75),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, upd.Service)
	require.NotNil(t, upd.Service.TargetConfiguration, "Update must return targetConfiguration")
	tc := upd.Service.TargetConfiguration
	assert.Equal(t, "512", aws.ToString(tc.Cpu))
	assert.Equal(t, "1024", aws.ToString(tc.Memory))
	require.NotNil(t, tc.ScalingTarget)
	assert.EqualValues(t, 8, aws.ToInt32(tc.ScalingTarget.MaxTaskCount))

	// The scalable target reflects the new capacity bounds.
	st2, err := aa.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: aastypes.ServiceNamespaceEcs,
		ResourceIds:      []string{resourceID},
	})
	require.NoError(t, err)
	require.Len(t, st2.ScalableTargets, 1)
	assert.EqualValues(t, 8, aws.ToInt32(st2.ScalableTargets[0].MaxCapacity))
	assert.EqualValues(t, 2, aws.ToInt32(st2.ScalableTargets[0].MinCapacity))
	// The rollout launches two replacement tasks and waits out their
	// steady-state and target-health gating while the scheduler's bounded
	// placement-retry chain (1+2+4+8+16+32s) may recover from a transient launch
	// failure, so the wait covers the full retry budget.
	rolled := waitForECSServicesStable(t, c, cluster, 2*time.Minute, "web").Services[0]
	assert.EqualValues(t, 2, rolled.DesiredCount)
	assert.EqualValues(t, 2, rolled.RunningCount)
	assert.NotEqual(t, initialTaskDefinition, aws.ToString(rolled.TaskDefinition),
		"Express update did not roll the backing Fargate service to the new managed task definition")

	del, err := c.DeleteExpressGatewayService(ctx, &ecs.DeleteExpressGatewayServiceInput{
		ServiceArn: svc.ServiceArn,
	})
	require.NoError(t, err)
	require.NotNil(t, del.Service)
	require.NotNil(t, del.Service.Status)
	assert.Equal(t, ecstypes.ExpressGatewayServiceStatusCodeDraining, del.Service.Status.StatusCode)

	// The ALB is gone (it was the only service on it).
	_, err = elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}})
	assert.Equal(t, "LoadBalancerNotFound", errCode(t, err), "ALB %s should have been torn down on Delete", albArn)

	// The scalable target is gone.
	stAfter, err := aa.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: aastypes.ServiceNamespaceEcs,
		ResourceIds:      []string{resourceID},
	})
	require.NoError(t, err)
	assert.Empty(t, stAfter.ScalableTargets, "scalable target should be torn down on Delete")
}

// TestECSExpress_Errors covers the documented Create/Describe error cases.
func TestECSExpress_Errors(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	cluster := expressCreateCluster(t, c, "express-err-cluster")

	// Note: infrastructureRoleArn is a required field in the SDK model, so the
	// "missing infra role → InvalidParameterException" case is enforced
	// client-side by the SDK before the request is sent (matching real AWS). The
	// server-side InvalidParameterException for a missing infra role is exercised
	// by the CLI test, which can send a request without it.
	infrastructureRole := createECSExpressInfrastructureRole(t, "express-errors")

	// Create with BOTH taskDefinitionArn and primaryContainer → InvalidParameterException.
	_, err := c.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
		Cluster:               aws.String(cluster),
		ServiceName:           aws.String("mutex-violation"),
		InfrastructureRoleArn: aws.String(infrastructureRole),
		TaskDefinitionArn:     aws.String("arn:aws:ecs:us-east-1:000000000000:task-definition/foo:1"),
		PrimaryContainer: &ecstypes.ExpressGatewayContainer{
			Image: aws.String("mirror.gcr.io/library/busybox:latest"),
		},
	})
	assert.Equal(t, "InvalidParameterException", errCode(t, err))

	// Describe a bogus serviceArn → ResourceNotFoundException.
	_, err = c.DescribeExpressGatewayService(ctx, &ecs.DescribeExpressGatewayServiceInput{
		ServiceArn: aws.String("arn:aws:ecs:us-east-1:000000000000:express-gateway-service/express-err-cluster/nope"),
	})
	assert.Equal(t, "ResourceNotFoundException", errCode(t, err))

	// Create against a nonexistent cluster → ClusterNotFoundException.
	_, err = c.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
		Cluster:               aws.String("no-such-cluster"),
		ServiceName:           aws.String("orphan"),
		InfrastructureRoleArn: aws.String(infrastructureRole),
		PrimaryContainer: &ecstypes.ExpressGatewayContainer{
			Image: aws.String("mirror.gcr.io/library/busybox:latest"),
		},
	})
	assert.Equal(t, "ClusterNotFoundException", errCode(t, err))

	// A role whose trust policy does not let Amazon ECS assume it, and a role
	// that does not exist, are refused before anything is provisioned.
	for name, role := range map[string]string{
		"not trusted": serviceRole(t, "lambda.amazonaws.com"),
		"missing":     "arn:aws:iam::000000000000:role/express-infra-missing",
	} {
		_, err = c.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
			Cluster:               aws.String(cluster),
			ServiceName:           aws.String("unassumable"),
			InfrastructureRoleArn: aws.String(role),
			PrimaryContainer: &ecstypes.ExpressGatewayContainer{
				Image: aws.String("mirror.gcr.io/library/busybox:latest"),
			},
		})
		assert.Equal(t, "InvalidParameterException", errCode(t, err), name)
	}
}

// TestECSExpress_ALBConsolidation asserts two Express services with identical
// network configuration share one ALB (the documented 25-per-ALB consolidation).
func TestECSExpress_ALBConsolidation(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	cluster := expressCreateCluster(t, c, "express-consolidate")
	ec2c := ec2Client()
	vpcID, subnets := createECSTestVPCSubnets(t, "express-consolidate", 2)
	securityGroup, err := ec2c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("express-consolidate"),
		Description: aws.String("Amazon ECS Express Mode consolidation test"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)

	net := &ecstypes.ExpressGatewayServiceNetworkConfiguration{
		Subnets:        subnets,
		SecurityGroups: []string{aws.ToString(securityGroup.GroupId)},
	}

	infrastructureRole := createECSExpressInfrastructureRole(t, "express-consolidate")
	create := func(name string) (string, string) {
		out, err := c.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
			Cluster:               aws.String(cluster),
			ServiceName:           aws.String(name),
			InfrastructureRoleArn: aws.String(infrastructureRole),
			NetworkConfiguration:  net,
			PrimaryContainer: &ecstypes.ExpressGatewayContainer{
				Image:         aws.String(containerCommandImage),
				ContainerPort: aws.Int32(8080),
				Command:       []string{"http", "8080", "express-ok"},
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			c.DeleteExpressGatewayService(ctx, &ecs.DeleteExpressGatewayServiceInput{ServiceArn: out.Service.ServiceArn})
		})
		require.NotEmpty(t, out.Service.ActiveConfigurations)
		require.NotEmpty(t, out.Service.ActiveConfigurations[0].IngressPaths)
		return aws.ToString(out.Service.ActiveConfigurations[0].IngressPaths[0].Endpoint), expressLoadBalancerArn(t, c, out.Service)
	}

	endpointA, albA := create("svc-a")
	endpointB, albB := create("svc-b")
	assert.Equal(t, albA, albB, "two Express services with identical network config must share one ALB")
	assert.NotEqual(t, endpointA, endpointB, "each Express service has its own hostname on the shared ALB")
}
