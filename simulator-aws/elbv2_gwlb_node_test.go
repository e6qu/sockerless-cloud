package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A node that replaces another for the same load balancer and zone is served
// only on its own requests: the node it replaced runs on until its container
// stops, and a request it makes then must not make the new node's endpoints
// Available before the new node applies them.
func TestGatewayLoadBalancerEndpointIsServedOnlyByItsOwnNode(t *testing.T) {
	srv, err := sim.NewServer(sim.Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ec2VpcEndpoints = sim.MakeStore[EC2VpcEndpoint](srv.DB(), "gwlb_node_test_endpoints")
	ec2VpcEndpointConns = sim.MakeStore[EC2VpcEndpointConnection](srv.DB(), "gwlb_node_test_connections")
	ec2VpcEndpoints.Put("vpce-0000aaaa", EC2VpcEndpoint{VpcEndpointId: "vpce-0000aaaa", VpcEndpointType: "GatewayLoadBalancer", State: "Pending"})

	key := elbv2GatewayNodeKey{lbArn: "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/gwy/g/0123", zone: "us-east-1a"}
	elbv2GatewayNodesMu.Lock()
	elbv2GatewayNodes[key] = &elbv2GatewayNode{
		instance: key.String() + "@new",
		design:   elbv2GatewayNodeDesign{endpoints: []elbv2GatewayNodeEndpoint{{id: "vpce-0000aaaa"}}},
	}
	elbv2GatewayNodesMu.Unlock()
	t.Cleanup(func() {
		elbv2GatewayNodesMu.Lock()
		delete(elbv2GatewayNodes, key)
		elbv2GatewayNodesMu.Unlock()
	})

	elbv2GatewayEndpointsServed(key.String() + "@old")
	if ep, _ := ec2VpcEndpoints.Get("vpce-0000aaaa"); ep.State != "Pending" {
		t.Fatalf("the replaced node's request made the endpoint %s", ep.State)
	}
	if _, ok := elbv2GatewayNodeStateFor(key.String() + "@old"); ok {
		t.Fatalf("the replaced node was answered the new node's state")
	}
	elbv2GatewayEndpointsServed(key.String() + "@new")
	if ep, _ := ec2VpcEndpoints.Get("vpce-0000aaaa"); ep.State != "Available" {
		t.Fatalf("the new node's request left the endpoint %s", ep.State)
	}
}
