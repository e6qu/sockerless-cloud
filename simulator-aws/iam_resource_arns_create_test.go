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

// A type that says the same words in another order names the same thing.
func TestIAMCreateWildcard_SameWordsInAnotherOrder(t *testing.T) {
	const region, account = "us-east-1", "123456789012"

	// RequestSpotFleet mints a spot-fleet-request: the verb sits where the type
	// puts its last word, so neither name is a suffix of the other.
	got := iamCreateWildcardARNs("ec2", "RequestSpotFleet",
		[]string{"image", "spot-fleet-request", "subnet"}, region, account)
	want := []string{"arn:aws:ec2:" + region + ":" + account + ":spot-fleet-request/*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RequestSpotFleet derived %v, want %v", got, want)
	}

	// The plural reads the same as the singular.
	got = iamCreateWildcardARNs("ec2", "RequestSpotInstances",
		[]string{"image", "spot-instances-request", "subnet"}, region, account)
	want = []string{"arn:aws:ec2:" + region + ":" + account + ":spot-instances-request/*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RequestSpotInstances derived %v, want %v", got, want)
	}

	// Sharing a word or two is not saying the same words: the parent guard has
	// to survive the looser match.
	if got := iamCreateWildcardARNs("states", "CreateStateMachineAlias",
		[]string{"statemachine"}, region, account); got != nil {
		t.Errorf("a create widening to its parent derived %v, want nothing", got)
	}
	if !iamSameWords("RequestSpotFleet", "spot-fleet-request") {
		t.Error("a reordering of the same words was not recognised")
	}
	for _, pair := range [][2]string{
		{"CreateVpcEndpoint", "vpc-endpoint-service"}, // a word the operation never says
		{"PurchaseCapacityBlock", "capacity-reservation"},
		{"CreateStateMachineAlias", "statemachine"},
	} {
		if iamSameWords(pair[0], pair[1]) {
			t.Errorf("%q and %q were read as the same words", pair[0], pair[1])
		}
	}
}

// The split keeps a run of capitals together, so an acronym stays one word.
func TestIAMCamelWords_KeepsAnAcronymWhole(t *testing.T) {
	for _, tc := range []struct {
		op   string
		want []string
	}{
		{"RequestSpotFleet", []string{"Request", "Spot", "Fleet"}},
		{"GetWebACL", []string{"Get", "Web", "ACL"}},
		{"CreateIPSet", []string{"Create", "IP", "Set"}},
		{"Get", []string{"Get"}},
	} {
		if got := iamCamelWords(tc.op); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("iamCamelWords(%q) = %v, want %v", tc.op, got, tc.want)
		}
	}
}
