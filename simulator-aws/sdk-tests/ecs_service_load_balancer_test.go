package aws_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_ServiceRegistersHealthyLoadBalancerTargets proves the complete
// Amazon ECS service data plane through the official ECS and Elastic Load
// Balancing v2 clients: a real task serves HTTP on its elastic network
// interface, the service registers that address, the target passes a real
// health probe, an Application Load Balancer forwards to it, and replacement
// swaps the target without losing the service endpoint.
func TestECS_ServiceRegistersHealthyLoadBalancerTargets(t *testing.T) {
	ecsC := ecsClient()
	elbC := elbv2Client()
	const (
		cluster     = "svc-lb-cluster"
		serviceName = "svc-lb"
		container   = "web"
		port        = int32(8080)
	)
	vpcID, subnetID := createECSTestVPCSubnet(t, "svc-lb")
	_, err := ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsC.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	taskDefinition, err := ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("svc-lb-task"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String(container),
			Image: aws.String(containerCommandImage),
			// The server starts after the first steady-state probe. Amazon ECS
			// must keep reconciling target health without another API request or
			// task transition.
			Command:   []string{"http", fmt.Sprint(port), "ecs-service-ok", "3"},
			Essential: aws.Bool(true),
			PortMappings: []ecstypes.PortMapping{{
				ContainerPort: aws.Int32(port),
				Protocol:      ecstypes.TransportProtocolTcp,
			}},
		}},
	})
	require.NoError(t, err)

	targetGroup, err := elbC.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String("svc-lb-tg"), Protocol: elbtypes.ProtocolEnumHttp,
		Port: aws.Int32(port), VpcId: aws.String(vpcID), TargetType: elbtypes.TargetTypeEnumIp,
		HealthCheckPath: aws.String("/health"),
	})
	require.NoError(t, err)
	targetGroupArn := aws.ToString(targetGroup.TargetGroups[0].TargetGroupArn)
	loadBalancer, err := elbC.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String("svc-lb-alb"), Type: elbtypes.LoadBalancerTypeEnumApplication,
		Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	loadBalancerArn := aws.ToString(loadBalancer.LoadBalancers[0].LoadBalancerArn)
	loadBalancerDNS := aws.ToString(loadBalancer.LoadBalancers[0].DNSName)
	listenerPort := availableELBv2ListenerPort(t)
	listener, err := elbC.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(loadBalancerArn), Protocol: elbtypes.ProtocolEnumHttp,
		Port: aws.Int32(listenerPort),
		DefaultActions: []elbtypes.Action{{
			Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(targetGroupArn),
		}},
	})
	require.NoError(t, err)
	listenerArn := aws.ToString(listener.Listeners[0].ListenerArn)
	t.Cleanup(func() {
		_, _ = elbC.DeleteListener(ctx, &elbv2.DeleteListenerInput{ListenerArn: aws.String(listenerArn)})
		_, _ = elbC.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(loadBalancerArn)})
		_, _ = elbC.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(targetGroupArn)})
	})

	_, err = ecsC.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
		TaskDefinition: taskDefinition.TaskDefinition.TaskDefinitionArn,
		DesiredCount:   aws.Int32(1), LaunchType: ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
		LoadBalancers: []ecstypes.LoadBalancer{{
			TargetGroupArn: aws.String(targetGroupArn),
			ContainerName:  aws.String(container),
			ContainerPort:  aws.Int32(port),
		}},
	})
	require.NoError(t, err)
	cleanupECSService(t, ecsC, cluster, serviceName)

	var firstTarget string
	var targetDiagnostic string
	targetBecameHealthy := assert.Eventually(t, func() bool {
		health, healthErr := elbC.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(targetGroupArn),
		})
		if healthErr != nil || len(health.TargetHealthDescriptions) != 1 ||
			health.TargetHealthDescriptions[0].TargetHealth.State != elbtypes.TargetHealthStateEnumHealthy {
			listed, _ := ecsC.ListTasks(ctx, &ecs.ListTasksInput{
				Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
			})
			described, _ := ecsC.DescribeTasks(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String(cluster), Tasks: listed.TaskArns,
			})
			targetState, targetID := "", ""
			if health != nil && len(health.TargetHealthDescriptions) > 0 {
				targetState = string(health.TargetHealthDescriptions[0].TargetHealth.State)
				targetID = aws.ToString(health.TargetHealthDescriptions[0].Target.Id)
			}
			taskState, stoppedReason := "", ""
			if len(described.Tasks) > 0 {
				taskState = aws.ToString(described.Tasks[0].LastStatus)
				stoppedReason = aws.ToString(described.Tasks[0].StoppedReason)
			}
			targetDiagnostic = fmt.Sprintf("target=%s:%s task=%s stoppedReason=%q",
				targetID, targetState, taskState, stoppedReason)
			return false
		}
		firstTarget = aws.ToString(health.TargetHealthDescriptions[0].Target.Id)
		return firstTarget != ""
	}, 30*time.Second, 100*time.Millisecond, "service target never became healthy")
	require.True(t, targetBecameHealthy, "service target diagnostic: %s", targetDiagnostic)
	require.Eventually(t, func() bool {
		services, describeErr := ecsC.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{serviceName},
		})
		return describeErr == nil &&
			len(services.Services) == 1 &&
			len(services.Services[0].Deployments) > 0 &&
			services.Services[0].Deployments[0].RolloutState == ecstypes.DeploymentRolloutStateCompleted
	}, 30*time.Second, 100*time.Millisecond,
		"service deployment did not complete after its target became healthy")

	assertServiceResponse := func() {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/customer", nil)
		require.NoError(t, requestErr)
		request.Host = fmt.Sprintf("%s:%d", loadBalancerDNS, listenerPort)
		response, requestErr := http.DefaultClient.Do(request)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		assert.Equal(t, http.StatusOK, response.StatusCode)
		assert.Equal(t, "ecs-service-ok", string(body))
	}
	assertServiceResponse()

	tasks, err := ecsC.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
		DesiredStatus: ecstypes.DesiredStatusRunning,
	})
	require.NoError(t, err)
	require.Len(t, tasks.TaskArns, 1)
	_, err = ecsC.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(cluster), Task: aws.String(tasks.TaskArns[0]),
		Reason: aws.String("validate load-balanced replacement"),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		health, healthErr := elbC.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(targetGroupArn),
		})
		return healthErr == nil &&
			len(health.TargetHealthDescriptions) == 1 &&
			health.TargetHealthDescriptions[0].TargetHealth.State == elbtypes.TargetHealthStateEnumHealthy &&
			aws.ToString(health.TargetHealthDescriptions[0].Target.Id) != firstTarget
	}, 30*time.Second, 100*time.Millisecond, "replacement target did not become healthy")
	assertServiceResponse()
}
