package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func asLaunchTestStores(t *testing.T) {
	t.Helper()
	bg.Await()
	saved := []func(){}
	swap := func(restore func()) { saved = append(saved, restore) }
	{
		prev := ec2Subnets
		swap(func() { ec2Subnets = prev })
		ec2Subnets = sim.MakeStore[EC2Subnet](nil, "ec2_subnets")
	}
	{
		prev := ec2SubnetIPCursor
		swap(func() { ec2SubnetIPCursor = prev })
		ec2SubnetIPCursor = sim.MakeStore[uint32](nil, "ec2_subnet_ip_cursor")
	}
	{
		prev := ec2NetworkInterfaces
		swap(func() { ec2NetworkInterfaces = prev })
		ec2NetworkInterfaces = sim.MakeStore[EC2NetworkInterface](nil, "ec2_network_interfaces")
	}
	{
		prev := ec2NatGateways
		swap(func() { ec2NatGateways = prev })
		ec2NatGateways = sim.MakeStore[EC2NatGateway](nil, "ec2_nat_gateways")
	}
	{
		prev := ec2Instances
		swap(func() { ec2Instances = prev })
		ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	}
	{
		prev := ec2Volumes
		swap(func() { ec2Volumes = prev })
		ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")
	}
	{
		prev := ecsTasks
		swap(func() { ecsTasks = prev })
		ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	}
	{
		prev := asLaunchConfigurations
		swap(func() { asLaunchConfigurations = prev })
		asLaunchConfigurations = sim.MakeStore[ASLaunchConfiguration](nil, "autoscaling_launch_configurations")
	}
	{
		prev := autoScalingGroups
		swap(func() { autoScalingGroups = prev })
		autoScalingGroups = sim.MakeStore[AutoScalingGroup](nil, "autoscaling_groups")
	}
	{
		prev := scalingActivities
		swap(func() { scalingActivities = prev })
		scalingActivities = sim.MakeStore[ScalingActivity](nil, "autoscaling_activities")
	}
	{
		prev := asGroupExtras
		swap(func() { asGroupExtras = prev })
		asGroupExtras = sim.MakeStore[ASGroupExtras](nil, "autoscaling_group_extras")
	}
	{
		prev := asBootInstance
		swap(func() { asBootInstance = prev })
	}
	t.Cleanup(func() {
		bg.Await()
		for _, restore := range saved {
			restore()
		}
	})

	t.Setenv("SIM_EBS_DATA_DIR", t.TempDir())
	ec2Subnets.Put("subnet-asg", EC2Subnet{SubnetId: "subnet-asg", CidrBlock: "10.61.0.0/24"})
	asLaunchConfigurations.Put("lc", ASLaunchConfiguration{Name: "lc", ImageId: "ami-launch", InstanceType: "t3.micro"})
}

func asQuery(t *testing.T, h http.HandlerFunc, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func asOnlyActivity(t *testing.T, group string) ScalingActivity {
	t.Helper()
	var found []ScalingActivity
	for _, a := range scalingActivities.List() {
		if a.AutoScalingGroupName == group {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("activities for %s = %+v, want one launch activity", group, found)
	}
	return found[0]
}

// TestAutoScalingAnswersBeforeTheInstanceBoots holds the VM boot open and shows
// CreateAutoScalingGroup has already answered with a Pending member and an
// InProgress activity; releasing the boot brings the member InService.
func TestAutoScalingAnswersBeforeTheInstanceBoots(t *testing.T) {
	asLaunchTestStores(t)
	booting := make(chan string, 1)
	release := make(chan struct{})
	asBootInstance = func(_ context.Context, inst EC2Instance) error {
		booting <- inst.InstanceId
		<-release
		return nil
	}

	rec := asQuery(t, handleASCreateAutoScalingGroup, url.Values{
		"AutoScalingGroupName":    {"async"},
		"LaunchConfigurationName": {"lc"},
		"MinSize":                 {"1"},
		"MaxSize":                 {"1"},
		"VPCZoneIdentifier":       {"subnet-asg"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateAutoScalingGroup = %d %s", rec.Code, rec.Body)
	}
	instanceID := <-booting

	group, _ := autoScalingGroups.Get("async")
	if len(group.InstanceIds) != 1 || group.InstanceIds[0] != instanceID {
		t.Fatalf("group members = %v, want [%s]", group.InstanceIds, instanceID)
	}
	if got := asInstanceLifecycleState("async", instanceID); got != "Pending" {
		t.Fatalf("lifecycle state while booting = %q, want Pending", got)
	}
	if !strings.Contains(autoScalingGroupXML(group), "<LifecycleState>Pending</LifecycleState>") {
		t.Fatalf("DescribeAutoScalingGroups member while booting: %s", autoScalingGroupXML(group))
	}
	if inst, _ := ec2Instances.Get(instanceID); inst.State != "pending" {
		t.Fatalf("EC2 state while booting = %q, want pending", inst.State)
	}
	if a := asOnlyActivity(t, "async"); a.StatusCode != "InProgress" || a.EndTime != "" {
		t.Fatalf("activity while booting = %+v, want InProgress without EndTime", a)
	}

	close(release)
	bg.Await()

	if got := asInstanceLifecycleState("async", instanceID); got != "InService" {
		t.Fatalf("lifecycle state after boot = %q, want InService", got)
	}
	if inst, _ := ec2Instances.Get(instanceID); inst.State != "running" {
		t.Fatalf("EC2 state after boot = %q, want running", inst.State)
	}
	a := asOnlyActivity(t, "async")
	if a.StatusCode != "Successful" || a.EndTime == "" || a.Description != "Launching a new EC2 instance: "+instanceID {
		t.Fatalf("activity after boot = %+v", a)
	}
	if !strings.Contains(asActivityMemberXML(a), "<Progress>100</Progress>") {
		t.Fatalf("finished activity XML: %s", asActivityMemberXML(a))
	}
}

// TestAutoScalingFailsTheLaunchWhenTheBootFails shows a failed boot fails the
// launch activity with the cause, terminates the instance and takes it out of
// the group instead of reporting it InService.
func TestAutoScalingFailsTheLaunchWhenTheBootFails(t *testing.T) {
	asLaunchTestStores(t)
	asBootInstance = func(context.Context, EC2Instance) error {
		return errors.New("guest never answered")
	}

	rec := asQuery(t, handleASCreateAutoScalingGroup, url.Values{
		"AutoScalingGroupName":    {"broken"},
		"LaunchConfigurationName": {"lc"},
		"MinSize":                 {"1"},
		"MaxSize":                 {"1"},
		"VPCZoneIdentifier":       {"subnet-asg"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateAutoScalingGroup = %d %s", rec.Code, rec.Body)
	}
	bg.Await()

	a := asOnlyActivity(t, "broken")
	if a.StatusCode != "Failed" || !strings.Contains(a.StatusMessage, "guest never answered") || a.EndTime == "" {
		t.Fatalf("activity after failed boot = %+v", a)
	}
	instanceID := strings.TrimPrefix(a.Description, "Launching a new EC2 instance: ")
	inst, _ := ec2Instances.Get(instanceID)
	if inst.State != "terminated" || inst.StateReasonCode != "Server.InternalError" {
		t.Fatalf("instance after failed boot = %q / %q, want terminated / Server.InternalError", inst.State, inst.StateReasonCode)
	}
	if group, _ := autoScalingGroups.Get("broken"); len(group.InstanceIds) != 0 {
		t.Fatalf("group kept the failed instance: %v", group.InstanceIds)
	}
}
