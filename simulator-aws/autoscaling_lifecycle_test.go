package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func TestAutoScalingLifecycleActionTimesOutToTheDefaultResult(t *testing.T) {
	hook := ASLifecycleHook{Name: "h", HeartbeatTimeout: 30, GlobalTimeout: 172800, DefaultResult: "CONTINUE"}
	started := time.Now().Add(-31 * time.Second).UTC().Format(time.RFC3339Nano)
	action := ASLifecycleAction{Token: "t", HeartbeatTime: started, StartTime: started}
	wait := &asLifecycleWait{heartbeat: make(chan struct{}, 1), result: make(chan string, 1)}
	if result, reason := asAwaitLifecycleAction(hook, action, wait); result != "CONTINUE" || reason != "Heartbeat Timeout" {
		t.Fatalf("expired action = %s (%s), want the default result on a heartbeat timeout", result, reason)
	}

	wait.result <- "ABANDON"
	action.HeartbeatTime = time.Now().UTC().Format(time.RFC3339Nano)
	action.StartTime = action.HeartbeatTime
	if result, reason := asAwaitLifecycleAction(hook, action, wait); result != "ABANDON" || !strings.Contains(reason, "ABANDON") {
		t.Fatalf("completed action = %s (%s), want the completed result", result, reason)
	}
}

// TestAutoScalingAbandonedLaunchIsReplaced shows an instance held in
// Pending:Wait is terminated when its lifecycle action is abandoned, its launch
// activity is cancelled with the reason, and the group launches a replacement.
func TestAutoScalingAbandonedLaunchIsReplaced(t *testing.T) {
	asLaunchTestStores(t)
	rec := asQuery(t, handleASCreateAutoScalingGroup, url.Values{
		"AutoScalingGroupName":    {"hooked"},
		"LaunchConfigurationName": {"lc"},
		"MinSize":                 {"1"},
		"MaxSize":                 {"1"},
		"VPCZoneIdentifier":       {"subnet-asg"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateAutoScalingGroup = %d %s", rec.Code, rec.Body)
	}
	bg.Await()
	launch := asOnlyActivity(t, "hooked")
	instanceID := strings.TrimPrefix(launch.Description, "Launching a new EC2 instance: ")
	scalingActivities.Update(launch.ActivityId, func(a *ScalingActivity) { a.StatusCode = "MidLifecycleAction"; a.EndTime = "" })
	now := time.Now().UTC().Format(time.RFC3339Nano)
	action := ASLifecycleAction{
		Token: "token-1", AutoScalingGroupName: "hooked", LifecycleHookName: "launch", InstanceId: instanceID,
		HeartbeatTime: now, StartTime: now, ActivityId: launch.ActivityId,
	}
	asLifecycleActions.Put(asxLifecycleKey("hooked", "launch", action.Token, instanceID), action)
	if got := asInstanceLifecycleState("hooked", instanceID); got != "Pending:Wait" {
		t.Fatalf("lifecycle state with an open action = %q, want Pending:Wait", got)
	}

	asCompleteLifecycleAction(action, "ABANDON", "Lifecycle Action Completed with ABANDON Result")
	bg.Await()

	if inst, _ := ec2Instances.Get(instanceID); inst.State != "terminated" {
		t.Fatalf("abandoned instance state = %q, want terminated", inst.State)
	}
	cancelled, _ := scalingActivities.Get(launch.ActivityId)
	if cancelled.StatusCode != "Cancelled" || !strings.Contains(cancelled.StatusMessage, "token-1 was abandoned") {
		t.Fatalf("launch activity after abandonment = %+v", cancelled)
	}
	group, _ := autoScalingGroups.Get("hooked")
	if len(group.InstanceIds) != 1 || group.InstanceIds[0] == instanceID {
		t.Fatalf("group members after abandonment = %v, want one replacement", group.InstanceIds)
	}
	if inst, _ := ec2Instances.Get(group.InstanceIds[0]); inst.State != "running" {
		t.Fatalf("replacement state = %q, want running", inst.State)
	}
	if len(asLifecycleActions.List()) != 0 {
		t.Fatalf("finished lifecycle actions were kept: %+v", asLifecycleActions.List())
	}
}
