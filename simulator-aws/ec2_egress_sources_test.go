package main

import (
	"reflect"
	"testing"
)

func publicTask(id, subnetID, ip string, status ECSTaskStatus) ECSTask {
	return ECSTask{
		TaskArn:    "arn:aws:ecs:us-east-1:123456789012:task/c/" + id,
		LastStatus: status,
		NetworkConfiguration: &ECSTaskNetworkConfig{AwsvpcConfiguration: &ECSTaskVpcConfig{
			Subnets: []string{subnetID}, AssignPublicIp: "ENABLED",
		}},
		Attachments: []ECSAttachment{{
			Type:    "ElasticNetworkInterface",
			Details: []ECSKeyValuePair{{Name: "privateIPv4Address", Value: ip}},
		}},
	}
}

// A stopped task's ENI went with it, so its old address must not keep an
// egress allowance another workload could inherit.
func TestPublicEgressSourcesSkipStoppedTasks(t *testing.T) {
	tasks := []ECSTask{
		publicTask("running", "subnet-a", "10.0.0.5", ECSTaskStatusRunning),
		publicTask("stopped", "subnet-a", "10.0.0.6", ECSTaskStatusStopped),
		publicTask("elsewhere", "subnet-b", "10.0.1.5", ECSTaskStatusRunning),
	}
	got := ecsPublicEgressSourcesForSubnet(tasks, "subnet-a")
	if want := []string{"10.0.0.5/32"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sources %v, want %v", got, want)
	}
}
