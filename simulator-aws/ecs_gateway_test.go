package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func ecsGatewayTestCall(t *testing.T, router *AWSRouter, op string, input any) (int, map[string]any) {
	t.Helper()
	return awsJSONCall(t, router, "AmazonEC2ContainerServiceV20141113."+op, input)
}

func ecsGatewayTestOK(t *testing.T, srv *AWSRouter, op string, input any) map[string]any {
	t.Helper()
	code, out := ecsGatewayTestCall(t, srv, op, input)
	require.Equal(t, http.StatusOK, code, "%s: %v", op, out)
	return out
}

func ecsGatewayTestRoutes(t *testing.T, srv *AWSRouter, filter map[string]any) []any {
	t.Helper()
	routes, _ := ecsGatewayTestOK(t, srv, "ListGatewayRoutes", filter)["gatewayRoutes"].([]any)
	return routes
}

// Two services share one load balancer; a gateway whose role may not create
// load balancers fails its route; disconnecting removes everything the routes
// made.
func TestECSGatewayRouteLifecycle(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	t.Setenv("SIM_DNS_PORT", "0")
	server, srv, _, err := buildSimulator(sim.Config{Provider: "aws", ListenAddr: ":0", LogLevel: "error"})
	require.NoError(t, err)
	t.Cleanup(server.StopBackground)
	roleArn := putServiceRole(t, "gateway-infrastructure", "ecs.amazonaws.com", "elasticloadbalancing:*", "ec2:*", "acm:*")

	code, out := ecsGatewayTestCall(t, srv, "CreateGateway", map[string]any{
		"name": "gw", "infrastructureRoleArn": "arn:aws:iam::" + awsAccountID() + ":role/missing",
		"configuration": map[string]any{"hostSharedAlb": map[string]any{}},
	})
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, ecsGatewayRoleNotAssumable, out["message"])

	created := ecsGatewayTestOK(t, srv, "CreateGateway", map[string]any{
		"name": "gw", "infrastructureRoleArn": roleArn,
		"configuration": map[string]any{"hostSharedAlb": map[string]any{
			"networkConfiguration": map[string]any{"ipAddressType": "ipv4"},
			"testTrafficRules":     map[string]any{"header": map[string]any{"name": "x-test", "values": []string{"canary"}}},
		}},
		"tags": []map[string]string{{"key": "team", "value": "web"}},
	})
	assert.Equal(t, "ACTIVE", created["status"])
	gateway := ecsGatewayTestOK(t, srv, "DescribeGateway", map[string]any{"gateway": "gw"})["gateway"].(map[string]any)
	alb := gateway["configuration"].(map[string]any)["hostSharedAlb"].(map[string]any)
	assert.Equal(t, []any{"subnet-0123456789abcdef0", "subnet-0123456789abcdef1", "subnet-0123456789abcdef2"},
		alb["networkConfiguration"].(map[string]any)["subnets"], "a gateway without subnets takes the default VPC's")
	assert.EqualValues(t, 0, alb["currentAlbCount"])

	code, out = ecsGatewayTestCall(t, srv, "CreateGateway", map[string]any{
		"name": "gw-https-only", "infrastructureRoleArn": roleArn,
		"configuration": map[string]any{"hostSharedAlb": map[string]any{"port": 80}},
	})
	require.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["__type"], "InvalidParameterException")

	ecsGatewayTestOK(t, srv, "CreateCluster", map[string]any{"clusterName": "gw-cluster"})
	ecsGatewayTestOK(t, srv, "RegisterTaskDefinition", map[string]any{
		"family": "gw-task", "networkMode": "awsvpc", "requiresCompatibilities": []string{"FARGATE"},
		"cpu": "256", "memory": "512",
		"containerDefinitions": []map[string]any{{
			"name": "web", "image": "busybox",
			"portMappings": []map[string]any{{"name": "web", "containerPort": 8080, "appProtocol": "http2"}},
		}},
	})
	service := func(name string, connectors any) map[string]any {
		return map[string]any{
			"cluster": "gw-cluster", "serviceName": name, "taskDefinition": "gw-task", "desiredCount": 0,
			"launchType": "FARGATE", "gatewayConnectors": connectors,
			"networkConfiguration": map[string]any{"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-0123456789abcdef0"}}},
		}
	}
	code, out = ecsGatewayTestCall(t, srv, "CreateService", service("svc-a", []map[string]any{{"portName": "web"}}))
	require.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["message"], "no gateways are associated with cluster")

	association := ecsGatewayTestOK(t, srv, "AssociateGatewayWithCluster", map[string]any{"gateway": "gw", "cluster": "gw-cluster"})
	assert.Equal(t, "ACTIVE", association["status"])

	code, out = ecsGatewayTestCall(t, srv, "CreateService", service("svc-a", []map[string]any{{"portName": "admin"}}))
	require.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["message"], "portName not found in task definition portMappings")
	code, out = ecsGatewayTestCall(t, srv, "CreateService", service("svc-a", []map[string]any{{"portName": "web", "filters": map[string]any{"accessTypes": []string{"PRIVATE"}}}}))
	require.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["message"], "none of the gateways associated with cluster gw-cluster match")

	ecsGatewayTestOK(t, srv, "CreateService", service("svc-a", []map[string]any{{"portName": "web"}}))
	ecsGatewayTestOK(t, srv, "CreateService", service("svc-b", []map[string]any{{"portName": "web", "healthCheck": map[string]any{"healthCheckPath": "/health"}}}))
	routes := ecsGatewayTestRoutes(t, srv, map[string]any{"gateway": "gw"})
	require.Len(t, routes, 2)
	bg.Await()

	routes = ecsGatewayTestRoutes(t, srv, map[string]any{"gateway": "gw", "status": []string{"ACTIVE"}})
	require.Len(t, routes, 2, "both routes provision")
	var loadBalancers []string
	for _, summary := range routes {
		routeArn := summary.(map[string]any)["gatewayRouteArn"].(string)
		route, ok := ecsGatewayRoutes.Get(routeArn)
		require.True(t, ok)
		loadBalancers = append(loadBalancers, route.LoadBalancerArn)

		rule, ok := elbv2Rules.Get(route.ListenerRuleArn)
		require.True(t, ok)
		assert.Equal(t, []string{route.Hostname}, rule.Conditions[0].Values)
		require.Len(t, route.TestListenerRuleArns, 1)
		testRule, ok := elbv2Rules.Get(route.TestListenerRuleArns[0])
		require.True(t, ok)
		assert.Equal(t, "x-test", testRule.Conditions[1].HttpHeaderName)
		assert.Less(t, atoiDefault(testRule.Priority, 0), atoiDefault(rule.Priority, 0), "the test rule precedes the production rule")
		targetGroup, ok := elbv2TargetGroups.Get(route.TargetGroupArn)
		require.True(t, ok)
		assert.Equal(t, 8080, targetGroup.Port)
		assert.Equal(t, "HTTP2", targetGroup.ProtocolVersion)
		cert, ok := acmCertificates.Get(acmARNToID(route.CertificateArn))
		require.True(t, ok)
		assert.Equal(t, "ISSUED", cert.Cert.Status)
		assert.Equal(t, route.Hostname, cert.Cert.DomainName)
		assert.True(t, strings.HasSuffix(route.Hostname, ".ecs."+awsRegion()+".on.aws"), route.Hostname)

		described := ecsGatewayTestOK(t, srv, "DescribeGatewayRoute", map[string]any{"gatewayRouteArn": routeArn})["gatewayRoute"].(map[string]any)
		assert.Equal(t, "https://"+route.Hostname, described["serviceUrl"])
	}
	assert.Equal(t, loadBalancers[0], loadBalancers[1], "services share one load balancer")
	lb, ok := elbv2LoadBalancers.Get(loadBalancers[0])
	require.True(t, ok)
	assert.Equal(t, "true", lb.Tags["AmazonECSManaged"])
	assert.Equal(t, "web", lb.Tags["team"], "the gateway propagates its tags")
	assert.Equal(t, "true", lb.Attributes["deletion_protection.enabled"])
	resources := ecsGatewayTestOK(t, srv, "DescribeGatewayResources", map[string]any{"gateway": "gw"})
	groups := resources["gatewayResources"].(map[string]any)["resources"].(map[string]any)["hostSharedAlb"].([]any)
	require.Len(t, groups, 1)
	assert.Len(t, groups[0].(map[string]any)["gatewayRouteArns"], 2)

	ecsGatewayTestOK(t, srv, "UpdateGateway", map[string]any{
		"gateway":       "gw",
		"configuration": map[string]any{"hostSharedAlb": map[string]any{"httpDesync": "strictest"}},
	})
	bg.Await()
	gateway = ecsGatewayTestOK(t, srv, "DescribeGateway", map[string]any{"gateway": "gw"})["gateway"].(map[string]any)
	assert.Equal(t, "UPDATE_COMPLETE", gateway["status"])
	assert.Equal(t, "1/1 load balancers updated", gateway["statusReason"])
	lb, _ = elbv2LoadBalancers.Get(loadBalancers[0])
	assert.Equal(t, "strictest", lb.Attributes["routing.http.desync_mitigation_mode"])

	unauthorized := putServiceRole(t, "gateway-unauthorized", "ecs.amazonaws.com")
	ecsGatewayTestOK(t, srv, "CreateGateway", map[string]any{
		"name": "gw-denied", "infrastructureRoleArn": unauthorized, "deletionProtection": false,
		"configuration": map[string]any{"hostSharedAlb": map[string]any{"networkConfiguration": map[string]any{"ipAddressType": "ipv4"}}},
	})
	ecsGatewayTestOK(t, srv, "AssociateGatewayWithCluster", map[string]any{"gateway": "gw-denied", "cluster": "gw-cluster"})
	ecsGatewayTestOK(t, srv, "CreateService", service("svc-c", []map[string]any{{"portName": "web"}}))
	bg.Await()
	failed := ecsGatewayTestRoutes(t, srv, map[string]any{"gateway": "gw-denied"})
	require.Len(t, failed, 1)
	assert.Equal(t, "CREATE_FAILED", failed[0].(map[string]any)["status"], "a role that may not create infrastructure fails the route")
	connected := ecsGatewayTestRoutes(t, srv, map[string]any{"cluster": "gw-cluster", "service": "svc-c"})
	assert.Len(t, connected, 2, "a service connects to every eligible gateway")

	ecsGatewayTestOK(t, srv, "CreateGateway", map[string]any{
		"name": "gw-dualstack", "infrastructureRoleArn": roleArn, "deletionProtection": false,
		"configuration": map[string]any{"hostSharedAlb": map[string]any{}},
	})
	ecsGatewayTestOK(t, srv, "AssociateGatewayWithCluster", map[string]any{"gateway": "gw-dualstack", "cluster": "gw-cluster"})
	ecsGatewayTestOK(t, srv, "CreateService", service("svc-d", []map[string]any{{"portName": "web"}}))
	bg.Await()
	failed = ecsGatewayTestRoutes(t, srv, map[string]any{"gateway": "gw-dualstack", "cluster": "gw-cluster", "service": "svc-d"})
	require.Len(t, failed, 1)
	assert.Equal(t, "CREATE_FAILED", failed[0].(map[string]any)["status"],
		"a dualstack gateway on the default VPC, whose subnets hold no IPv6 range, cannot create its load balancer")
	described := ecsGatewayTestOK(t, srv, "DescribeServices", map[string]any{"cluster": "gw-cluster", "services": []string{"svc-d"}})
	events, err := json.Marshal(described["services"].([]any)[0].(map[string]any)["events"])
	require.NoError(t, err)
	assert.Contains(t, string(events), "elasticloadbalancing:CreateLoadBalancer: InvalidSubnet")

	for _, name := range []string{"svc-a", "svc-b", "svc-c", "svc-d"} {
		ecsGatewayTestOK(t, srv, "UpdateService", map[string]any{"cluster": "gw-cluster", "service": name, "gatewayConnectors": []any{}})
	}
	bg.Await()
	assert.Empty(t, ecsGatewayTestRoutes(t, srv, map[string]any{}))
	_, exists := elbv2LoadBalancers.Get(loadBalancers[0])
	assert.False(t, exists, "the last route to leave a load balancer removes it")
	gateway = ecsGatewayTestOK(t, srv, "DescribeGateway", map[string]any{"gateway": "gw"})["gateway"].(map[string]any)
	assert.EqualValues(t, 0, gateway["configuration"].(map[string]any)["hostSharedAlb"].(map[string]any)["currentAlbCount"])

	code, out = ecsGatewayTestCall(t, srv, "DeleteGateway", map[string]any{"gateway": "gw"})
	require.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["__type"], "ClientException")
	disassociated := ecsGatewayTestOK(t, srv, "DisassociateGatewayFromCluster", map[string]any{"gateway": "gw", "cluster": "gw-cluster"})
	assert.Equal(t, "DISASSOCIATING", disassociated["status"])
	associations := ecsGatewayTestOK(t, srv, "ListGatewayClusterAssociations", map[string]any{"gateway": "gw"})["gatewayClusterAssociations"].([]any)
	assert.Empty(t, associations, "with no route left the disassociation completes at once")
	ecsGatewayTestOK(t, srv, "UpdateGateway", map[string]any{"gateway": "gw", "deletionProtection": false})
	deleted := ecsGatewayTestOK(t, srv, "DeleteGateway", map[string]any{"gateway": "gw"})
	assert.Equal(t, "DELETED", deleted["status"])
	code, out = ecsGatewayTestCall(t, srv, "DescribeGateway", map[string]any{"gateway": "gw"})
	require.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["__type"], "GatewayNotFoundException")
}
