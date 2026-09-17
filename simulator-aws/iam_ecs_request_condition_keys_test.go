package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

const ecsConditionTarget = "AmazonEC2ContainerServiceV20141113."

func ecsConditionContext(operation, body string) map[string][]string {
	return requestConditionContext(jsonConditionRequest(ecsConditionTarget+operation), "ecs", operation, body)
}

func TestECSConditionKeysReadTheRequestShape(t *testing.T) {
	for _, tc := range []struct {
		operation string
		body      string
		want      map[string][]string
	}{
		{"PutAccountSetting", `{"name": "containerInsights", "value": "enabled"}`,
			map[string][]string{"ecs:account-setting": {"containerInsights"}}},
		{"DeleteAccountSetting", `{"name": "awsvpcTrunking"}`,
			map[string][]string{"ecs:account-setting": {"awsvpcTrunking"}}},
		{"PutAccountSettingDefault", `{"name": "fargateFIPSMode", "value": "enabled"}`,
			map[string][]string{"ecs:account-setting": {"fargateFIPSMode"}}},
		{"RegisterTaskDefinition", `{
			"family": "f",
			"requiresCompatibilities": ["FARGATE", "EC2"],
			"containerDefinitions": [{"name": "a", "privileged": false}, {"name": "b", "privileged": true}]
		}`, map[string][]string{
			"ecs:compute-compatibility": {"FARGATE", "EC2"},
			"ecs:privileged":            {"true"},
		}},
		{"RegisterDaemonTaskDefinition", `{"family": "d", "containerDefinitions": [{"name": "a", "privileged": false}]}`,
			map[string][]string{"ecs:privileged": {"false"}}},
		{"CreateService", `{
			"serviceName": "s",
			"networkConfiguration": {"awsvpcConfiguration": {"subnets": ["subnet-1"], "assignPublicIp": "ENABLED"}},
			"serviceConnectConfiguration": {"enabled": true},
			"vpcLatticeConfigurations": [{"roleArn": "arn:aws:iam::123456789012:role/r", "targetGroupArn": "tg", "portName": "p"}]
		}`, map[string][]string{
			"ecs:auto-assign-public-ip":  {"true"},
			"ecs:enable-service-connect": {"true"},
			"ecs:enable-vpc-lattice":     {"true"},
		}},
		{"UpdateService", `{
			"service": "s",
			"networkConfiguration": {"awsvpcConfiguration": {"subnets": ["subnet-1"], "assignPublicIp": "DISABLED"}},
			"serviceConnectConfiguration": {"enabled": false},
			"vpcLatticeConfigurations": []
		}`, map[string][]string{
			"ecs:auto-assign-public-ip":  {"false"},
			"ecs:enable-service-connect": {"false"},
			"ecs:enable-vpc-lattice":     {"false"},
		}},
		{"CreateCluster", `{"clusterName": "c", "configuration": {"managedStorageConfiguration": {"fargateEphemeralStorageKmsKeyId": "key-1"}}}`,
			map[string][]string{"ecs:fargate-ephemeral-storage-kms-key": {"key-1"}}},
		{"UpdateCapacityProvider", `{"name": "cp", "managedInstancesProvider": {"instanceLaunchTemplate": {"instanceMetadataTagsPropagation": true}}}`,
			map[string][]string{"ecs:instance-metadata-tags-propagation": {"true"}}},
		{"StartTask", `{
			"cluster": "c",
			"taskDefinition": "f:1",
			"containerInstances": ["abc", "arn:aws:ecs:us-west-2:123456789012:container-instance/c/def"]
		}`, map[string][]string{"ecs:container-instances": {
			ecsArn("container-instance", "c/abc"),
			"arn:aws:ecs:us-west-2:123456789012:container-instance/c/def",
		}}},
		{"ExecuteCommand", `{"cluster": "c", "task": "t", "container": "web", "command": "sh", "interactive": true}`,
			map[string][]string{"ecs:container-name": {"web"}}},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			assertConditionValues(t, ecsConditionContext(tc.operation, tc.body), tc.want)
		})
	}
}

// ExecuteCommand may leave out the container of a single-container task; the
// key then names the container Amazon ECS runs the command in.
func TestECSConditionKeysResolveTheOnlyExecContainer(t *testing.T) {
	previous := ecsTasks
	t.Cleanup(func() { ecsTasks = previous })
	AwaitSimulatorBackground()
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	ecsTasks.Put("single", ECSTask{Containers: []ECSTaskContainer{{Name: "app"}}})
	ecsTasks.Put("pair", ECSTask{Containers: []ECSTaskContainer{{Name: "app"}, {Name: "sidecar"}}})

	ctx := ecsConditionContext("ExecuteCommand",
		`{"task": "arn:aws:ecs:us-east-1:123456789012:task/c/single", "command": "sh", "interactive": true}`)
	assertConditionValues(t, ctx, map[string][]string{"ecs:container-name": {"app"}})

	ctx = ecsConditionContext("ExecuteCommand", `{"task": "pair", "command": "sh", "interactive": true}`)
	assertConditionKeysAbsent(t, ctx, "ecs:container-name")
}

func TestECSConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	ctx := ecsConditionContext("CreateService", `{"serviceName": "s"}`)
	assertConditionKeysAbsent(t, ctx, "ecs:auto-assign-public-ip", "ecs:enable-service-connect", "ecs:enable-vpc-lattice")
	ctx = ecsConditionContext("RegisterTaskDefinition", `{"family": "f", "containerDefinitions": [{"name": "a"}]}`)
	assertConditionKeysAbsent(t, ctx, "ecs:privileged", "ecs:compute-compatibility")
	// A member of one operation settles nothing on another.
	ctx = ecsConditionContext("RunTask", `{"name": "containerInsights", "requiresCompatibilities": ["FARGATE"]}`)
	assertConditionKeysAbsent(t, ctx, "ecs:account-setting", "ecs:compute-compatibility")
}
