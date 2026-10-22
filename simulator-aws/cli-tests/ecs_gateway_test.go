package aws_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliGatewayRoutes struct {
	GatewayRoutes []struct {
		GatewayRouteArn          string `json:"gatewayRouteArn"`
		ServiceContainerPortName string `json:"serviceContainerPortName"`
		Status                   string `json:"status"`
	} `json:"gatewayRoutes"`
}

// awaitCLIGatewayRoutes polls list-gateway-routes, the only state Amazon ECS
// exposes for a route's provisioning, until done accepts the routes.
func awaitCLIGatewayRoutes(t *testing.T, done func(cliGatewayRoutes) bool, args ...string) cliGatewayRoutes {
	t.Helper()
	var routes cliGatewayRoutes
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, awsCLI(append([]string{"ecs", "list-gateway-routes", "--output", "json"}, args...)...)), &routes)
		return done(routes)
	}, 90*time.Second, time.Second)
	return routes
}

// TestECSGateway_CLI walks a gateway through the AWS CLI: create, describe,
// list, associate, a service connecting with --gateway-connectors, the route
// and the infrastructure behind it, an update, and the disconnection,
// disassociation and deletion that remove it.
func TestECSGateway_CLI(t *testing.T) {
	t.Parallel()
	const (
		gateway = "cli-gw"
		cluster = "cli-gw-cluster"
		service = "cli-gw-svc"
		role    = "cli-gw-infrastructure"
	)
	subnetID := createCLIECSTestSubnet(t, "cli-gw", "10.237.0.0/16")
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-subnets", "--subnet-ids", subnetID,
		"--query", "Subnets[0].VpcId", "--output", "text")))
	subnet2ID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpcID,
		"--cidr-block", "10.237.1.0/24", "--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")))
	t.Cleanup(func() { _ = awsCLI("ec2", "delete-subnet", "--subnet-id", subnet2ID).Run() })
	roleArn := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-role", "--role-name", role,
		"--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		"--query", "Role.Arn", "--output", "text")))
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", role, "--policy-name", "gateway-infrastructure",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["elasticloadbalancing:*","ec2:*","acm:*","wafv2:*"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-role-policy", "--role-name", role, "--policy-name", "gateway-infrastructure").Run()
		_ = awsCLI("iam", "delete-role", "--role-name", role).Run()
	})

	var created struct {
		GatewayArn string `json:"gatewayArn"`
		Name       string `json:"name"`
		Status     string `json:"status"`
	}
	parseJSON(t, runCLI(t, awsCLI("ecs", "create-gateway", "--name", gateway,
		"--infrastructure-role-arn", roleArn,
		"--configuration", `{"hostSharedAlb":{"scheme":"internal","networkConfiguration":{"subnets":["`+subnetID+`","`+subnet2ID+`"],"ipAddressType":"ipv4"}}}`,
		"--propagate-tags", "NONE",
		"--output", "json")), &created)
	assert.Equal(t, "ACTIVE", created.Status)
	assert.Contains(t, created.GatewayArn, ":gateway/"+gateway+"/")
	t.Cleanup(func() {
		_ = awsCLI("ecs", "update-gateway", "--gateway", gateway, "--no-deletion-protection").Run()
		_ = awsCLI("ecs", "disassociate-gateway-from-cluster", "--gateway", gateway, "--cluster", cluster).Run()
		_ = awsCLI("ecs", "delete-gateway", "--gateway", gateway).Run()
	})

	var described struct {
		Gateway struct {
			AccessType         string `json:"accessType"`
			DeletionProtection bool   `json:"deletionProtection"`
			PropagateTags      string `json:"propagateTags"`
			Status             string `json:"status"`
			StatusReason       string `json:"statusReason"`
			Configuration      struct {
				HostSharedAlb struct {
					Scheme          string `json:"scheme"`
					Port            int    `json:"port"`
					Protocol        string `json:"protocol"`
					MaxAlbCount     int    `json:"maxAlbCount"`
					CurrentAlbCount int    `json:"currentAlbCount"`
					HttpDesync      string `json:"httpDesync"`
				} `json:"hostSharedAlb"`
			} `json:"configuration"`
		} `json:"gateway"`
	}
	parseJSON(t, runCLI(t, awsCLI("ecs", "describe-gateway", "--gateway", gateway, "--output", "json")), &described)
	assert.Equal(t, "PRIVATE", described.Gateway.AccessType, "an internal scheme makes a PRIVATE gateway")
	assert.True(t, described.Gateway.DeletionProtection)
	assert.Equal(t, "NONE", described.Gateway.PropagateTags)
	alb := described.Gateway.Configuration.HostSharedAlb
	assert.Equal(t, "internal", alb.Scheme)
	assert.Equal(t, 443, alb.Port)
	assert.Equal(t, "HTTPS", alb.Protocol)
	assert.Equal(t, 10, alb.MaxAlbCount)
	assert.Equal(t, 0, alb.CurrentAlbCount)

	var listed struct {
		Gateways []struct {
			GatewayArn string `json:"gatewayArn"`
		} `json:"gateways"`
	}
	parseJSON(t, runCLI(t, awsCLI("ecs", "list-gateways", "--names", gateway, "--output", "json")), &listed)
	require.Len(t, listed.Gateways, 1)
	assert.Equal(t, created.GatewayArn, listed.Gateways[0].GatewayArn)
	assert.Contains(t, runCLIExpectError(t, awsCLI("ecs", "describe-gateway", "--gateway", "cli-gw-missing")), "GatewayNotFoundException")

	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })
	var association struct {
		Status string `json:"status"`
	}
	parseJSON(t, runCLI(t, awsCLI("ecs", "associate-gateway-with-cluster", "--gateway", gateway, "--cluster", cluster, "--output", "json")), &association)
	assert.Equal(t, "ACTIVE", association.Status)
	var associations struct {
		GatewayClusterAssociations []struct {
			ClusterArn string `json:"clusterArn"`
			Status     string `json:"status"`
		} `json:"gatewayClusterAssociations"`
	}
	parseJSON(t, runCLI(t, awsCLI("ecs", "list-gateway-cluster-associations", "--cluster", cluster, "--output", "json")), &associations)
	require.Len(t, associations.GatewayClusterAssociations, 1)

	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-gw-task", "--network-mode", "awsvpc",
		"--requires-compatibilities", "FARGATE", "--cpu", "256", "--memory", "512",
		"--container-definitions", `[{"name":"web","image":"`+containerCommandImage+`","portMappings":[{"name":"web","containerPort":8080,"appProtocol":"http"}]}]`))
	assert.Contains(t, runCLIExpectError(t, awsCLI("ecs", "create-service",
		"--cluster", cluster, "--service-name", service+"-public",
		"--task-definition", "cli-gw-task", "--desired-count", "0", "--launch-type", "FARGATE",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--gateway-connectors", `[{"portName":"web","filters":{"accessTypes":["PUBLIC"]}}]`)),
		"InvalidParameterException", "a PUBLIC filter excludes the only, PRIVATE, gateway")
	runCLI(t, awsCLI("ecs", "create-service",
		"--cluster", cluster, "--service-name", service,
		"--task-definition", "cli-gw-task", "--desired-count", "0", "--launch-type", "FARGATE",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--gateway-connectors", `[{"portName":"web","healthCheck":{"healthCheckPath":"/health"}}]`))
	cleanupCLIService(t, cluster, service)

	routes := awaitCLIGatewayRoutes(t, func(routes cliGatewayRoutes) bool {
		return len(routes.GatewayRoutes) == 1 && routes.GatewayRoutes[0].Status != "CREATE_IN_PROGRESS"
	}, "--cluster", cluster, "--service", service)
	require.Equal(t, "ACTIVE", routes.GatewayRoutes[0].Status)
	routeArn := routes.GatewayRoutes[0].GatewayRouteArn

	var route struct {
		GatewayRoute struct {
			ServiceURL string `json:"serviceUrl"`
			Resources  struct {
				HostSharedAlb []struct {
					LoadBalancerArn string `json:"loadBalancerArn"`
					ListenerRuleArn string `json:"listenerRuleArn"`
				} `json:"hostSharedAlb"`
			} `json:"resources"`
		} `json:"gatewayRoute"`
	}
	parseJSON(t, runCLI(t, awsCLI("ecs", "describe-gateway-route", "--gateway-route-arn", routeArn, "--output", "json")), &route)
	assert.True(t, strings.HasSuffix(route.GatewayRoute.ServiceURL, ".on.aws"), route.GatewayRoute.ServiceURL)
	require.Len(t, route.GatewayRoute.Resources.HostSharedAlb, 1)
	ruleArn := route.GatewayRoute.Resources.HostSharedAlb[0].ListenerRuleArn
	targetGroupArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "describe-rules", "--rule-arns", ruleArn,
		"--query", "Rules[0].Actions[0].TargetGroupArn", "--output", "text")))
	assert.Equal(t, "/health", strings.TrimSpace(runCLI(t, awsCLI("elbv2", "describe-target-groups",
		"--target-group-arns", targetGroupArn, "--query", "TargetGroups[0].HealthCheckPath", "--output", "text"))))
	assert.Equal(t, "internal", strings.TrimSpace(runCLI(t, awsCLI("elbv2", "describe-load-balancers",
		"--load-balancer-arns", route.GatewayRoute.Resources.HostSharedAlb[0].LoadBalancerArn,
		"--query", "LoadBalancers[0].Scheme", "--output", "text"))))

	var resources struct {
		GatewayResources struct {
			Resources struct {
				HostSharedAlb []struct {
					GatewayRouteArns []string `json:"gatewayRouteArns"`
				} `json:"hostSharedAlb"`
			} `json:"resources"`
		} `json:"gatewayResources"`
	}
	parseJSON(t, runCLI(t, awsCLI("ecs", "describe-gateway-resources", "--gateway", gateway, "--output", "json")), &resources)
	require.Len(t, resources.GatewayResources.Resources.HostSharedAlb, 1)
	assert.Equal(t, []string{routeArn}, resources.GatewayResources.Resources.HostSharedAlb[0].GatewayRouteArns)

	runCLI(t, awsCLI("ecs", "update-gateway", "--gateway", gateway,
		"--configuration", `{"hostSharedAlb":{"httpDesync":"monitor","testTrafficRules":{"header":{"name":"x-test-traffic","values":["canary"]}}}}`))
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, awsCLI("ecs", "describe-gateway", "--gateway", gateway, "--output", "json")), &described)
		return described.Gateway.Status != "UPDATE_IN_PROGRESS"
	}, 60*time.Second, time.Second)
	assert.Equal(t, "UPDATE_COMPLETE", described.Gateway.Status)
	assert.Equal(t, "1/1 load balancers updated", described.Gateway.StatusReason)
	assert.Equal(t, "monitor", described.Gateway.Configuration.HostSharedAlb.HttpDesync)

	parseJSON(t, runCLI(t, awsCLI("ecs", "disassociate-gateway-from-cluster", "--gateway", gateway, "--cluster", cluster, "--output", "json")), &association)
	assert.Equal(t, "DISASSOCIATING", association.Status)
	runCLI(t, awsCLI("ecs", "update-service", "--cluster", cluster, "--service", service, "--gateway-connectors", "[]"))
	awaitCLIGatewayRoutes(t, func(routes cliGatewayRoutes) bool { return len(routes.GatewayRoutes) == 0 }, "--gateway", gateway)
	parseJSON(t, runCLI(t, awsCLI("ecs", "list-gateway-cluster-associations", "--gateway", gateway, "--output", "json")), &associations)
	assert.Empty(t, associations.GatewayClusterAssociations)

	assert.Contains(t, runCLIExpectError(t, awsCLI("ecs", "delete-gateway", "--gateway", gateway)), "ClientException",
		"deletion protection is on until it is turned off")
	runCLI(t, awsCLI("ecs", "update-gateway", "--gateway", gateway, "--no-deletion-protection"))
	var deleted struct {
		Status string `json:"status"`
	}
	parseJSON(t, runCLI(t, awsCLI("ecs", "delete-gateway", "--gateway", gateway, "--output", "json")), &deleted)
	assert.Equal(t, "DELETED", deleted.Status)
}
