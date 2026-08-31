package main

import (
	"reflect"
	"testing"
)

// A create that declares its inputs alongside what it mints still authorizes
// against the one thing it creates.
func TestIAMCreateWildcard_OneMatchingTypeResolvesTheRest(t *testing.T) {
	const region, account = "us-east-1", "123456789012"

	// ec2:CreateVpc declares the IPAM pool and the IPv6 pool it draws from
	// besides the VPC it mints; only the VPC answers to the name.
	got := iamCreateWildcardARNs("ec2", "CreateVpc",
		[]string{"ipam-pool", "ipv6pool-ec2", "vpc"}, region, account)
	want := []string{"arn:aws:ec2:" + region + ":" + account + ":vpc/*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CreateVpc derived %v, want %v", got, want)
	}

	// The match reads the created noun, not a type that merely contains it:
	// CreateFlowLogs mints a vpc-flow-log and reads the VPC it watches.
	got = iamCreateWildcardARNs("ec2", "CreateFlowLogs",
		[]string{"subnet", "vpc", "vpc-flow-log"}, region, account)
	want = []string{"arn:aws:ec2:" + region + ":" + account + ":vpc-flow-log/*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CreateFlowLogs derived %v, want %v", got, want)
	}

	// An input the request names is never the created resource, so a create
	// whose own type is undeclared derives nothing rather than seize one.
	if got := iamCreateWildcardARNs("ec2", "CreateTransitGatewayConnect",
		[]string{"transit-gateway-attachment"}, region, account); got != nil {
		t.Errorf("a create naming no declared type of its own derived %v, want nothing", got)
	}

	// Two types answering to the name is genuine ambiguity, not a licence to
	// pick one.
	if got := iamCreateWildcardARNs("ec2", "CreateVpc",
		[]string{"vpc", "vpcs"}, region, account); got != nil {
		t.Errorf("an ambiguous create derived %v, want nothing", got)
	}

	// The parent guard survives the relaxation: creating one alias must not
	// wildcard every state machine.
	if got := iamCreateWildcardARNs("states", "CreateStateMachineAlias",
		[]string{"statemachine"}, region, account); got != nil {
		t.Errorf("a create widening to its parent derived %v, want nothing", got)
	}
}
