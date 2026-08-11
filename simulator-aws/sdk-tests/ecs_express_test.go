package aws_sdk_test

import (
	"fmt"
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

// expressCreateCluster makes a cluster for an Express test and returns its name.
func expressCreateCluster(t *testing.T, c *ecs.Client, name string) string {
	t.Helper()
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)}) })
	return name
}

// TestECSExpress_CreateAssemblyAndLifecycle drives the full ECS Express Mode
// service lifecycle (Create/Describe/Update/Delete) through the ECS SDK and
// asserts both the control-plane response shape and the faithful assembly: the
// underlying ALB, scalable target, and Fargate service all exist via their own
// service APIs, and Delete tears them down.
func TestECSExpress_CreateAssemblyAndLifecycle(t *testing.T) {
	c := ecsClient()
	elb := elbv2Client()
	aa := appAutoScalingClient()
	cluster := expressCreateCluster(t, c, "express-cluster")
	// Isolate the lifecycle from the shared default VPC: other shard tests
	// launch into it, and the real-VPC tier plumbs task networking per VPC.
	vpcID, subnetID := createECSTestVPCSubnet(t, "express-lifecycle")
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
		InfrastructureRoleArn: aws.String("arn:aws:iam::000000000000:role/express-infra"),
		NetworkConfiguration: &ecstypes.ExpressGatewayServiceNetworkConfiguration{
			Subnets:        []string{subnetID},
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

	// Ingress path: PUBLIC, https endpoint.
	require.NotEmpty(t, cfg.IngressPaths)
	ingress := cfg.IngressPaths[0]
	assert.Equal(t, ecstypes.AccessTypePublic, ingress.AccessType)
	endpoint := aws.ToString(ingress.Endpoint)
	require.True(t, strings.HasPrefix(endpoint, "https://"), "endpoint %q must start https://", endpoint)
	albHost := strings.TrimPrefix(endpoint, "https://")

	// ---- Faithful assembly assertions ----

	// The ALB exists via ELBv2 and its DNSName matches the ingress endpoint host.
	lbs, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{})
	require.NoError(t, err)
	var matched bool
	for _, lb := range lbs.LoadBalancers {
		if strings.EqualFold(aws.ToString(lb.DNSName), albHost) {
			matched = true
			assert.Equal(t, "internet-facing", string(lb.Scheme))
			break
		}
	}
	assert.True(t, matched, "no ALB with DNSName %q found in DescribeLoadBalancers", albHost)

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

	// ---- Describe round-trips tags (include TAGS) ----
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

	// ---- Update changes cpu/memory/scalingTarget ----
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
	// The rollout launches two replacement tasks, waits out their steady-state
	// and target-health gating, and tears down the previous task while the
	// scheduler's own bounded placement-retry chain (1+2+4+8+16+32s) may be
	// recovering from a transient launch failure. Real Amazon ECS expresses
	// this rollout in minutes, so the window covers the full retry budget and
	// the diagnostic retains the last observed service state.
	var rolloutDiagnostic string
	rolled := assert.Eventually(t, func() bool {
		services, describeErr := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{"web"},
		})
		if describeErr != nil {
			rolloutDiagnostic = "DescribeServices error: " + describeErr.Error()
			return false
		}
		if len(services.Services) != 1 {
			rolloutDiagnostic = fmt.Sprintf("DescribeServices returned %d services", len(services.Services))
			return false
		}
		service := services.Services[0]
		deploymentState, deploymentReason := "", ""
		if len(service.Deployments) > 0 {
			deploymentState = string(service.Deployments[0].RolloutState)
			deploymentReason = aws.ToString(service.Deployments[0].RolloutStateReason)
		}
		events := ""
		for i, event := range service.Events {
			if i >= 3 {
				break
			}
			events += " [" + aws.ToString(event.Message) + "]"
		}
		tasks := ""
		if listed, listErr := c.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster: aws.String(cluster), ServiceName: aws.String("web"),
		}); listErr == nil && len(listed.TaskArns) > 0 {
			if described, descErr := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String(cluster), Tasks: listed.TaskArns,
			}); descErr == nil {
				for _, task := range described.Tasks {
					tasks += fmt.Sprintf(" {status=%s health=%s taskDef=%s stoppedReason=%q}",
						aws.ToString(task.LastStatus), string(task.HealthStatus),
						aws.ToString(task.TaskDefinitionArn), aws.ToString(task.StoppedReason))
				}
			}
		}
		targets := ""
		if groups, groupErr := elb.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
			Names: []string{"express-web"},
		}); groupErr == nil && len(groups.TargetGroups) == 1 {
			if health, healthErr := elb.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
				TargetGroupArn: groups.TargetGroups[0].TargetGroupArn,
			}); healthErr == nil {
				for _, description := range health.TargetHealthDescriptions {
					targets += fmt.Sprintf(" {target=%s:%d state=%s reason=%s}",
						aws.ToString(description.Target.Id), aws.ToInt32(description.Target.Port),
						string(description.TargetHealth.State), string(description.TargetHealth.Reason))
				}
			}
		}
		rolloutDiagnostic = fmt.Sprintf(
			"desired=%d running=%d pending=%d taskDefinition=%s initialTaskDefinition=%s deployment=%s %q events=%s tasks=%s targets=%s",
			service.DesiredCount, service.RunningCount, service.PendingCount,
			aws.ToString(service.TaskDefinition), initialTaskDefinition,
			deploymentState, deploymentReason, events, tasks, targets)
		return service.DesiredCount == 2 &&
			service.RunningCount == 2 &&
			aws.ToString(service.TaskDefinition) != initialTaskDefinition
	}, 2*time.Minute, time.Second)
	require.True(t, rolled,
		"Express update did not roll the backing Fargate service to the new managed task definition: %s",
		rolloutDiagnostic)

	// ---- Delete: status DRAINING + assembly torn down ----
	del, err := c.DeleteExpressGatewayService(ctx, &ecs.DeleteExpressGatewayServiceInput{
		ServiceArn: svc.ServiceArn,
	})
	require.NoError(t, err)
	require.NotNil(t, del.Service)
	require.NotNil(t, del.Service.Status)
	assert.Equal(t, ecstypes.ExpressGatewayServiceStatusCodeDraining, del.Service.Status.StatusCode)

	// The ALB is gone (it was the only service on it).
	lbsAfter, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{})
	require.NoError(t, err)
	for _, lb := range lbsAfter.LoadBalancers {
		assert.NotEqualf(t, strings.ToLower(albHost), strings.ToLower(aws.ToString(lb.DNSName)),
			"ALB %q should have been torn down on Delete", albHost)
	}

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
	c := ecsClient()
	cluster := expressCreateCluster(t, c, "express-err-cluster")

	// Note: infrastructureRoleArn is a required field in the SDK model, so the
	// "missing infra role → InvalidParameterException" case is enforced
	// client-side by the SDK before the request is sent (matching real AWS). The
	// server-side InvalidParameterException for a missing infra role is exercised
	// by the CLI test, which can send a request without it.

	// Create with BOTH taskDefinitionArn and primaryContainer → InvalidParameterException.
	_, err := c.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
		Cluster:               aws.String(cluster),
		ServiceName:           aws.String("mutex-violation"),
		InfrastructureRoleArn: aws.String("arn:aws:iam::000000000000:role/express-infra"),
		TaskDefinitionArn:     aws.String("arn:aws:ecs:us-east-1:000000000000:task-definition/foo:1"),
		PrimaryContainer: &ecstypes.ExpressGatewayContainer{
			Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
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
		InfrastructureRoleArn: aws.String("arn:aws:iam::000000000000:role/express-infra"),
		PrimaryContainer: &ecstypes.ExpressGatewayContainer{
			Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
		},
	})
	assert.Equal(t, "ClusterNotFoundException", errCode(t, err))
}

// TestECSExpress_ALBConsolidation asserts two Express services with identical
// network configuration share one ALB (the documented 25-per-ALB consolidation).
func TestECSExpress_ALBConsolidation(t *testing.T) {
	c := ecsClient()
	cluster := expressCreateCluster(t, c, "express-consolidate")
	ec2c := ec2Client()
	vpcID, subnetID := createECSTestVPCSubnet(t, "express-consolidate")
	securityGroup, err := ec2c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("express-consolidate"),
		Description: aws.String("Amazon ECS Express Mode consolidation test"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)

	net := &ecstypes.ExpressGatewayServiceNetworkConfiguration{
		Subnets:        []string{subnetID},
		SecurityGroups: []string{aws.ToString(securityGroup.GroupId)},
	}

	mkEndpointHost := func(name string) string {
		out, err := c.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
			Cluster:               aws.String(cluster),
			ServiceName:           aws.String(name),
			InfrastructureRoleArn: aws.String("arn:aws:iam::000000000000:role/express-infra"),
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
		return strings.TrimPrefix(aws.ToString(out.Service.ActiveConfigurations[0].IngressPaths[0].Endpoint), "https://")
	}

	host1 := mkEndpointHost("svc-a")
	host2 := mkEndpointHost("svc-b")
	assert.Equal(t, host1, host2, "two Express services with identical network config must share one ALB DNSName")
}
