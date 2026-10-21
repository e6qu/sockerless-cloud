package aws_sdk_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createECSGatewayInfrastructureRole creates the role a gateway provisions its
// load balancers, listeners, rules, target groups, security groups and
// certificates as: trusted by ecs.amazonaws.com, and allowed those services.
func createECSGatewayInfrastructureRole(t *testing.T, name string) string {
	t.Helper()
	iamC := iamClient()
	role, err := iamC.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(name),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"ecs.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	_, err = iamC.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:   aws.String(name),
		PolicyName: aws.String("gateway-infrastructure"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Action":["elasticloadbalancing:*","ec2:*","acm:*","wafv2:*"],"Resource":"*"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamC.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("gateway-infrastructure")})
		_, _ = iamC.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})
	return aws.ToString(role.Role.Arn)
}

func requireECSAPIErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "error %v is not an API error", err)
	assert.Equal(t, code, apiErr.ErrorCode(), apiErr.ErrorMessage())
}

// awaitECSGatewayRoutes polls ListGatewayRoutes, the only state Amazon ECS
// exposes for a route's provisioning, until done accepts the routes.
func awaitECSGatewayRoutes(t *testing.T, client *ecs.Client, input *ecs.ListGatewayRoutesInput, done func([]ecstypes.GatewayRouteSummary) bool) []ecstypes.GatewayRouteSummary {
	t.Helper()
	var routes []ecstypes.GatewayRouteSummary
	require.Eventually(t, func() bool {
		out, err := client.ListGatewayRoutes(ctx, input)
		require.NoError(t, err)
		routes = out.GatewayRoutes
		return done(routes)
	}, 60*time.Second, waiterMinDelay)
	return routes
}

func awaitECSGatewayStatus(t *testing.T, client *ecs.Client, gateway string, want ecstypes.GatewayStatus) *ecstypes.Gateway {
	t.Helper()
	var described *ecstypes.Gateway
	require.Eventually(t, func() bool {
		out, err := client.DescribeGateway(ctx, &ecs.DescribeGatewayInput{Gateway: aws.String(gateway)})
		require.NoError(t, err)
		described = out.Gateway
		return described.Status == want
	}, 60*time.Second, waiterMinDelay)
	return described
}

// TestECSGateway_ServiceConnectsThroughSharedLoadBalancer walks a gateway's
// whole lifecycle through the ECS client: a gateway, its association with a
// cluster, a service whose gateway connector gives it a route with a
// hostname, certificate, target group and listener rule on a shared
// Application Load Balancer the Elastic Load Balancing client describes, an
// update the load balancer takes, and the disconnection, disassociation and
// deletion that remove all of it.
func TestECSGateway_ServiceConnectsThroughSharedLoadBalancer(t *testing.T) {
	t.Parallel()
	ecsC := ecsClient()
	elbC := elbv2Client()
	const (
		gatewayName = "sdk-gw"
		cluster     = "sdk-gw-cluster"
		serviceName = "sdk-gw-svc"
	)
	roleArn := createECSGatewayInfrastructureRole(t, "sdk-gw-infrastructure")
	_, subnetID := createECSTestVPCSubnet(t, "sdk-gw")

	created, err := ecsC.CreateGateway(ctx, &ecs.CreateGatewayInput{
		Name:                  aws.String(gatewayName),
		InfrastructureRoleArn: aws.String(roleArn),
		Configuration: &ecstypes.GatewayConfigurationMemberHostSharedAlb{Value: ecstypes.HostSharedAlbConfiguration{
			NetworkConfiguration: &ecstypes.GatewayElbNetworkConfiguration{
				Subnets:       []string{subnetID},
				IpAddressType: ecstypes.GatewayElbIpAddressTypeIpv4,
			},
		}},
		Tags: []ecstypes.Tag{{Key: aws.String("team"), Value: aws.String("web")}},
	})
	require.NoError(t, err)
	gatewayArn := aws.ToString(created.GatewayArn)
	assert.Equal(t, ecstypes.GatewayStatusActive, created.Status)
	assert.Contains(t, gatewayArn, ":gateway/"+gatewayName+"/")
	t.Cleanup(func() {
		_, _ = ecsC.UpdateGateway(ctx, &ecs.UpdateGatewayInput{Gateway: aws.String(gatewayArn), DeletionProtection: aws.Bool(false)})
		_, _ = ecsC.DisassociateGatewayFromCluster(ctx, &ecs.DisassociateGatewayFromClusterInput{Gateway: aws.String(gatewayArn), Cluster: aws.String(cluster)})
		_, _ = ecsC.DeleteGateway(ctx, &ecs.DeleteGatewayInput{Gateway: aws.String(gatewayArn)})
	})

	described, err := ecsC.DescribeGateway(ctx, &ecs.DescribeGatewayInput{Gateway: aws.String(gatewayName)})
	require.NoError(t, err)
	gateway := described.Gateway
	assert.Equal(t, gatewayArn, aws.ToString(gateway.GatewayArn))
	assert.True(t, aws.ToBool(gateway.DeletionProtection), "deletion protection defaults on")
	assert.Equal(t, ecstypes.AccessTypePublic, gateway.AccessType)
	assert.Equal(t, ecstypes.PropagateGatewayTagsGateway, gateway.PropagateTags)
	alb, ok := gateway.Configuration.(*ecstypes.DescribeGatewayConfigurationMemberHostSharedAlb)
	require.True(t, ok)
	assert.Equal(t, int32(443), aws.ToInt32(alb.Value.Port))
	assert.Equal(t, ecstypes.GatewayAlbProtocolHttps, alb.Value.Protocol)
	assert.Equal(t, ecstypes.GatewayElbSchemeInternetFacing, alb.Value.Scheme)
	assert.Equal(t, int32(10), aws.ToInt32(alb.Value.MaxAlbCount))
	assert.Equal(t, int32(0), aws.ToInt32(alb.Value.CurrentAlbCount), "a gateway provisions nothing until a service connects")
	assert.Equal(t, ecstypes.GatewayAlbDesyncMitigationModeDefensive, alb.Value.HttpDesync)

	listed, err := ecsC.ListGateways(ctx, &ecs.ListGatewaysInput{Names: []string{gatewayName}, Status: []ecstypes.GatewayStatus{ecstypes.GatewayStatusActive}})
	require.NoError(t, err)
	require.Len(t, listed.Gateways, 1)
	assert.Equal(t, gatewayArn, aws.ToString(listed.Gateways[0].GatewayArn))

	_, err = ecsC.DescribeGateway(ctx, &ecs.DescribeGatewayInput{Gateway: aws.String("sdk-gw-missing")})
	var notFound *ecstypes.GatewayNotFoundException
	require.ErrorAs(t, err, &notFound)

	_, err = ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ecsC.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	taskDefinition, err := ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("sdk-gw-task"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String("web"),
			Image: aws.String(containerCommandImage),
			PortMappings: []ecstypes.PortMapping{{
				Name: aws.String("web"), ContainerPort: aws.Int32(8080), AppProtocol: ecstypes.ApplicationProtocolHttp,
			}},
		}},
	})
	require.NoError(t, err)
	serviceInput := &ecs.CreateServiceInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
		TaskDefinition: taskDefinition.TaskDefinition.TaskDefinitionArn,
		DesiredCount:   aws.Int32(0), LaunchType: ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
		GatewayConnectors: []ecstypes.GatewayConnector{{PortName: aws.String("web")}},
	}
	_, err = ecsC.CreateService(ctx, serviceInput)
	requireECSAPIErrorCode(t, err, "InvalidParameterException")

	associated, err := ecsC.AssociateGatewayWithCluster(ctx, &ecs.AssociateGatewayWithClusterInput{
		Gateway: aws.String(gatewayName), Cluster: aws.String(cluster),
	})
	require.NoError(t, err)
	assert.Equal(t, ecstypes.GatewayClusterAssociationStatusActive, associated.Status)
	associations, err := ecsC.ListGatewayClusterAssociations(ctx, &ecs.ListGatewayClusterAssociationsInput{Gateway: aws.String(gatewayName)})
	require.NoError(t, err)
	require.Len(t, associations.GatewayClusterAssociations, 1)
	assert.Equal(t, aws.ToString(associated.ClusterArn), aws.ToString(associations.GatewayClusterAssociations[0].ClusterArn))

	_, err = ecsC.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster: serviceInput.Cluster, ServiceName: aws.String(serviceName + "-badport"),
		TaskDefinition: serviceInput.TaskDefinition, DesiredCount: aws.Int32(0), LaunchType: ecstypes.LaunchTypeFargate,
		NetworkConfiguration: serviceInput.NetworkConfiguration,
		GatewayConnectors:    []ecstypes.GatewayConnector{{PortName: aws.String("admin")}},
	})
	requireECSAPIErrorCode(t, err, "InvalidParameterException")

	_, err = ecsC.CreateService(ctx, serviceInput)
	require.NoError(t, err)
	cleanupECSService(t, ecsC, cluster, serviceName)

	routes := awaitECSGatewayRoutes(t, ecsC, &ecs.ListGatewayRoutesInput{Gateway: aws.String(gatewayName), Cluster: aws.String(cluster)},
		func(routes []ecstypes.GatewayRouteSummary) bool {
			return len(routes) == 1 && routes[0].Status != ecstypes.GatewayRouteStatusCreateInProgress
		})
	require.Equal(t, ecstypes.GatewayRouteStatusActive, routes[0].Status)
	assert.Equal(t, "web", aws.ToString(routes[0].ServiceContainerPortName))
	routeArn := aws.ToString(routes[0].GatewayRouteArn)
	assert.Contains(t, routeArn, ":gateway-route/"+gatewayName+"/")

	route, err := ecsC.DescribeGatewayRoute(ctx, &ecs.DescribeGatewayRouteInput{GatewayRouteArn: aws.String(routeArn)})
	require.NoError(t, err)
	serviceURL := aws.ToString(route.GatewayRoute.ServiceUrl)
	assert.True(t, strings.HasPrefix(serviceURL, "https://") && strings.HasSuffix(serviceURL, ".on.aws"), serviceURL)
	assert.Contains(t, serviceURL, ".ecs.")
	routeResources, ok := route.GatewayRoute.Resources.(*ecstypes.GatewayRouteResourcesMemberHostSharedAlb)
	require.True(t, ok)
	require.Len(t, routeResources.Value, 1)
	albResources := routeResources.Value[0]
	require.NotEmpty(t, aws.ToString(albResources.LoadBalancerArn))
	require.NotEmpty(t, aws.ToString(albResources.ListenerRuleArn))
	require.NotEmpty(t, aws.ToString(albResources.AcmCertificateArn))
	require.NotEmpty(t, albResources.LoadBalancerSecurityGroups)

	rules, err := elbC.DescribeRules(ctx, &elbv2.DescribeRulesInput{RuleArns: []string{aws.ToString(albResources.ListenerRuleArn)}})
	require.NoError(t, err)
	require.Len(t, rules.Rules, 1)
	hostname := strings.TrimPrefix(serviceURL, "https://")
	require.NotEmpty(t, rules.Rules[0].Conditions)
	assert.Equal(t, []string{hostname}, rules.Rules[0].Conditions[0].HostHeaderConfig.Values)
	targetGroupArn := aws.ToString(rules.Rules[0].Actions[0].TargetGroupArn)
	targetGroups, err := elbC.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{targetGroupArn}})
	require.NoError(t, err)
	assert.Equal(t, int32(8080), aws.ToInt32(targetGroups.TargetGroups[0].Port))
	assert.Equal(t, "/", aws.ToString(targetGroups.TargetGroups[0].HealthCheckPath))
	loadBalancers, err := elbC.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{aws.ToString(albResources.LoadBalancerArn)}})
	require.NoError(t, err)
	assert.Equal(t, "internet-facing", string(loadBalancers.LoadBalancers[0].Scheme))

	resources, err := ecsC.DescribeGatewayResources(ctx, &ecs.DescribeGatewayResourcesInput{Gateway: aws.String(gatewayName)})
	require.NoError(t, err)
	groups, ok := resources.GatewayResources.Resources.(*ecstypes.GatewayInfrastructureResourcesMemberHostSharedAlb)
	require.True(t, ok)
	require.Len(t, groups.Value, 1)
	assert.Equal(t, aws.ToString(albResources.LoadBalancerArn), aws.ToString(groups.Value[0].LoadBalancerArn))
	assert.Equal(t, []string{routeArn}, groups.Value[0].GatewayRouteArns)

	deployments, err := ecsC.ListServiceDeployments(ctx, &ecs.ListServiceDeploymentsInput{Cluster: aws.String(cluster), Service: aws.String(serviceName)})
	require.NoError(t, err)
	require.NotEmpty(t, deployments.ServiceDeployments)
	revisions, err := ecsC.DescribeServiceRevisions(ctx, &ecs.DescribeServiceRevisionsInput{
		ServiceRevisionArns: []string{aws.ToString(deployments.ServiceDeployments[0].TargetServiceRevisionArn)},
	})
	require.NoError(t, err)
	require.Len(t, revisions.ServiceRevisions, 1)
	revision := revisions.ServiceRevisions[0]
	require.Len(t, revision.GatewayConnectors, 1)
	require.NotNil(t, revision.EcsManagedResources)
	require.Len(t, revision.EcsManagedResources.GatewayConnections, 1)
	connection := revision.EcsManagedResources.GatewayConnections[0]
	assert.Equal(t, serviceURL, aws.ToString(connection.ServiceUrl))
	assert.Equal(t, ecstypes.AccessTypePublic, connection.AccessType)
	connectionResources, ok := connection.Resources.(*ecstypes.GatewayConnectionResourcesMemberHostSharedAlb)
	require.True(t, ok)
	assert.Equal(t, targetGroupArn, aws.ToString(connectionResources.Value[0].TargetGroupArn))

	_, err = ecsC.DeleteGateway(ctx, &ecs.DeleteGatewayInput{Gateway: aws.String(gatewayName)})
	requireECSAPIErrorCode(t, err, "ClientException")

	updated, err := ecsC.UpdateGateway(ctx, &ecs.UpdateGatewayInput{
		Gateway: aws.String(gatewayName),
		Configuration: &ecstypes.UpdateGatewayConfigurationMemberHostSharedAlb{Value: ecstypes.UpdateGatewayAlbConfiguration{
			MaxAlbCount: aws.Int32(20),
			HttpDesync:  ecstypes.GatewayAlbDesyncMitigationModeStrictest,
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, ecstypes.GatewayStatusUpdateInProgress, updated.Status)
	gateway = awaitECSGatewayStatus(t, ecsC, gatewayName, ecstypes.GatewayStatusUpdateComplete)
	assert.Equal(t, "1/1 load balancers updated", aws.ToString(gateway.StatusReason))
	attributes, err := elbC.DescribeLoadBalancerAttributes(ctx, &elbv2.DescribeLoadBalancerAttributesInput{LoadBalancerArn: albResources.LoadBalancerArn})
	require.NoError(t, err)
	values := map[string]string{}
	for _, attribute := range attributes.Attributes {
		values[aws.ToString(attribute.Key)] = aws.ToString(attribute.Value)
	}
	assert.Equal(t, "strictest", values["routing.http.desync_mitigation_mode"])
	assert.Equal(t, "true", values["deletion_protection.enabled"])

	disassociated, err := ecsC.DisassociateGatewayFromCluster(ctx, &ecs.DisassociateGatewayFromClusterInput{
		Gateway: aws.String(gatewayName), Cluster: aws.String(cluster),
	})
	require.NoError(t, err)
	assert.Equal(t, ecstypes.GatewayClusterAssociationStatusDisassociating, disassociated.Status)
	associations, err = ecsC.ListGatewayClusterAssociations(ctx, &ecs.ListGatewayClusterAssociationsInput{Gateway: aws.String(gatewayName)})
	require.NoError(t, err)
	require.Len(t, associations.GatewayClusterAssociations, 1, "the route holds the association open")
	assert.Equal(t, ecstypes.GatewayClusterAssociationStatusDisassociating, associations.GatewayClusterAssociations[0].Status)

	_, err = ecsC.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(serviceName),
		GatewayConnectors: []ecstypes.GatewayConnector{},
	})
	require.NoError(t, err)
	awaitECSGatewayRoutes(t, ecsC, &ecs.ListGatewayRoutesInput{Gateway: aws.String(gatewayName)},
		func(routes []ecstypes.GatewayRouteSummary) bool { return len(routes) == 0 })
	associations, err = ecsC.ListGatewayClusterAssociations(ctx, &ecs.ListGatewayClusterAssociationsInput{Gateway: aws.String(gatewayName)})
	require.NoError(t, err)
	assert.Empty(t, associations.GatewayClusterAssociations, "the last route's removal completes the disassociation")
	_, err = elbC.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{aws.ToString(albResources.LoadBalancerArn)}})
	require.Error(t, err, "the gateway removes a load balancer its last route left")

	_, err = ecsC.UpdateGateway(ctx, &ecs.UpdateGatewayInput{Gateway: aws.String(gatewayName), DeletionProtection: aws.Bool(false)})
	require.NoError(t, err)
	deleted, err := ecsC.DeleteGateway(ctx, &ecs.DeleteGatewayInput{Gateway: aws.String(gatewayName)})
	require.NoError(t, err)
	assert.Equal(t, ecstypes.GatewayStatusDeleted, deleted.Status)
	_, err = ecsC.DescribeGateway(ctx, &ecs.DescribeGatewayInput{Gateway: aws.String(gatewayArn)})
	require.ErrorAs(t, err, &notFound)
}
