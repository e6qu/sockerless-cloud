package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func glueConditionContext(operation, body string) map[string][]string {
	return populatedConditionContext(jsonConditionRequest("AWSGlue."+operation), "glue", operation, body)
}

func useGlueConditionStores(t *testing.T) {
	t.Helper()
	previousConnections, previousSubnets := glueConnections, ec2Subnets
	t.Cleanup(func() { glueConnections, ec2Subnets = previousConnections, previousSubnets })
	AwaitSimulatorBackground()
	glueConnections = sim.MakeStore[GlueConnection](nil, "glue_connections")
	ec2Subnets = sim.MakeStore[EC2Subnet](nil, "ec2_subnets")
	ec2Subnets.Put("subnet-a", EC2Subnet{SubnetId: "subnet-a", VpcId: "vpc-a"})
	ec2Subnets.Put("subnet-b", EC2Subnet{SubnetId: "subnet-b", VpcId: "vpc-b"})
	glueConnections.Put("warehouse", GlueConnection{Name: "warehouse", PhysicalConnectionRequirements: map[string]any{
		"SubnetId":            "subnet-a",
		"SecurityGroupIdList": []any{"sg-1", "sg-2"},
	}})
	glueConnections.Put("lake", GlueConnection{Name: "lake", PhysicalConnectionRequirements: map[string]any{
		"SubnetId":            "subnet-b",
		"SecurityGroupIdList": []any{"sg-2"},
	}})
	glueConnections.Put("public", GlueConnection{Name: "public", ConnectionType: "NETWORK"})
}

func TestGlueConditionKeysReadTheJobConnections(t *testing.T) {
	useGlueConditionStores(t)
	want := map[string][]string{
		"glue:SubnetIds":        {"subnet-a", "subnet-b"},
		"glue:SecurityGroupIds": {"sg-1", "sg-2"},
		"glue:VpcIds":           {"vpc-a", "vpc-b"},
	}
	connections := `{"Connections": ["warehouse", "lake", "public"]}`
	assertPopulatedConditionValues(t, glueConditionContext("CreateJob",
		`{"Name": "j", "Role": "r", "Command": {"Name": "glueetl"}, "Connections": `+connections+`}`), want)
	assertPopulatedConditionValues(t, glueConditionContext("CreateSession",
		`{"Id": "s", "Role": "r", "Command": {"Name": "glueetl"}, "Connections": `+connections+`}`), want)
	assertPopulatedConditionValues(t, glueConditionContext("UpdateJob",
		`{"JobName": "j", "JobUpdate": {"Connections": `+connections+`}}`), want)
}

func TestGlueConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	useGlueConditionStores(t)
	keys := []string{"glue:SubnetIds", "glue:SecurityGroupIds", "glue:VpcIds"}
	assertConditionKeysAbsent(t, glueConditionContext("CreateJob", `{"Name": "j"}`), keys...)
	assertConditionKeysAbsent(t, glueConditionContext("CreateJob",
		`{"Name": "j", "Connections": {"Connections": ["public", "missing"]}}`), keys...)
	// UpdateJob carries its connections inside JobUpdate only.
	assertConditionKeysAbsent(t, glueConditionContext("UpdateJob",
		`{"JobName": "j", "Connections": {"Connections": ["warehouse"]}}`), keys...)
}
