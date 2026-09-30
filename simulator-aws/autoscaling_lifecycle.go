package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

const asLaunchingTransition = "autoscaling:EC2_INSTANCE_LAUNCHING"

// asLifecycleWait is the in-process side of one open lifecycle action: the
// channels CompleteLifecycleAction and RecordLifecycleActionHeartbeat reach
// its timeout goroutine through.
type asLifecycleWait struct {
	heartbeat chan struct{}
	result    chan string
}

var asLifecycleWaits = struct {
	mu sync.Mutex
	m  map[string]*asLifecycleWait
}{m: map[string]*asLifecycleWait{}}

// asLifecycleFinish serializes the decision an instance's last completed
// lifecycle action triggers, so two hooks finishing together decide once.
var asLifecycleFinish sync.Mutex

func asHooksFor(group, transition string) []ASLifecycleHook {
	var hooks []ASLifecycleHook
	for _, h := range asLifecycleHooks.List() {
		if h.AutoScalingGroupName == group && h.LifecycleTransition == transition {
			hooks = append(hooks, h)
		}
	}
	return hooks
}

// asInstanceAwaitsLifecycleAction reports whether an instance has an open
// lifecycle action, which holds it in Pending:Wait.
func asInstanceAwaitsLifecycleAction(group, instanceID string) bool {
	for _, a := range asLifecycleActions.List() {
		if a.AutoScalingGroupName == group && a.InstanceId == instanceID && !a.Completed {
			return true
		}
	}
	return false
}

// asLifecycleTimeouts returns when an action times out without a heartbeat,
// and when it times out regardless: the global timeout, or 100 times the
// heartbeat timeout if that is sooner.
func asLifecycleTimeouts(hook ASLifecycleHook, action ASLifecycleAction) (heartbeat, global time.Time) {
	last, err := time.Parse(time.RFC3339Nano, action.HeartbeatTime)
	if err != nil {
		panic(fmt.Sprintf("lifecycle action %s heartbeat time %q: %v", action.Token, action.HeartbeatTime, err))
	}
	started, err := time.Parse(time.RFC3339Nano, action.StartTime)
	if err != nil {
		panic(fmt.Sprintf("lifecycle action %s start time %q: %v", action.Token, action.StartTime, err))
	}
	limit := time.Duration(hook.GlobalTimeout) * time.Second
	if byHeartbeat := 100 * time.Duration(hook.HeartbeatTimeout) * time.Second; byHeartbeat < limit {
		limit = byHeartbeat
	}
	return last.Add(time.Duration(hook.HeartbeatTimeout) * time.Second), started.Add(limit)
}

// asBeginLaunchLifecycleActions holds a launched instance in Pending:Wait:
// one lifecycle action per launch hook, a notification to each hook's target,
// and a wait that ends on CompleteLifecycleAction or on the hook's timeout.
func asBeginLaunchLifecycleActions(group, instanceID, activityID string, hooks []ASLifecycleHook) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, hook := range hooks {
		action := ASLifecycleAction{
			Token:                sim.NewUUID(),
			AutoScalingGroupName: group,
			LifecycleHookName:    hook.Name,
			InstanceId:           instanceID,
			HeartbeatTime:        now,
			StartTime:            now,
			ActivityId:           activityID,
		}
		asLifecycleActions.Put(asxLifecycleKey(group, hook.Name, action.Token, instanceID), action)
		asNotifyLifecycleAction(hook, action)
		asStartLifecycleWait(hook, action)
	}
}

func asStartLifecycleWait(hook ASLifecycleHook, action ASLifecycleAction) {
	wait := &asLifecycleWait{heartbeat: make(chan struct{}, 1), result: make(chan string, 1)}
	asLifecycleWaits.mu.Lock()
	asLifecycleWaits.m[action.Token] = wait
	asLifecycleWaits.mu.Unlock()
	var result, reason string
	bg.WatchThen(func() {
		result, reason = asAwaitLifecycleAction(hook, action, wait)
	}, func() {
		asLifecycleWaits.mu.Lock()
		delete(asLifecycleWaits.m, action.Token)
		asLifecycleWaits.mu.Unlock()
		asCompleteLifecycleAction(action, result, reason)
	})
}

func asAwaitLifecycleAction(hook ASLifecycleHook, action ASLifecycleAction, wait *asLifecycleWait) (result, reason string) {
	heartbeatAt, globalAt := asLifecycleTimeouts(hook, action)
	heartbeat := time.NewTimer(time.Until(heartbeatAt))
	defer heartbeat.Stop()
	global := time.NewTimer(time.Until(globalAt))
	defer global.Stop()
	for {
		select {
		case result := <-wait.result:
			return result, "Lifecycle Action Completed with " + result + " Result"
		case <-wait.heartbeat:
			if !heartbeat.Stop() {
				<-heartbeat.C
			}
			heartbeat.Reset(time.Duration(hook.HeartbeatTimeout) * time.Second)
		case <-heartbeat.C:
			return hook.DefaultResult, "Heartbeat Timeout"
		case <-global.C:
			return hook.DefaultResult, "Global Timeout"
		}
	}
}

// asCompleteLifecycleAction records an action's result and, once every hook
// has answered for the instance, lets it proceed to InService or, if any
// answered ABANDON, terminates it so the group launches a replacement.
func asCompleteLifecycleAction(action ASLifecycleAction, result, reason string) {
	asLifecycleFinish.Lock()
	defer asLifecycleFinish.Unlock()
	key := asxLifecycleKey(action.AutoScalingGroupName, action.LifecycleHookName, action.Token, action.InstanceId)
	asLifecycleActions.Update(key, func(a *ASLifecycleAction) {
		a.Completed = true
		a.Result = result
		a.StatusReason = reason
	})
	var abandoned *ASLifecycleAction
	var done []ASLifecycleAction
	for _, a := range asLifecycleActions.List() {
		if a.AutoScalingGroupName != action.AutoScalingGroupName || a.InstanceId != action.InstanceId {
			continue
		}
		if !a.Completed {
			return
		}
		if a.Result == "ABANDON" && abandoned == nil {
			abandoned = &a
		}
		done = append(done, a)
	}
	for _, a := range done {
		asLifecycleActions.Delete(asxLifecycleKey(a.AutoScalingGroupName, a.LifecycleHookName, a.Token, a.InstanceId))
	}
	if abandoned == nil {
		asFinishActivity(action.ActivityId, "Successful", "")
		asRefreshInstanceSettled(action.AutoScalingGroupName, action.InstanceId, "")
		return
	}
	message := fmt.Sprintf("Instance failed to complete user's Lifecycle Action: Lifecycle Action with token %s was abandoned: %s",
		abandoned.Token, abandoned.StatusReason)
	asFinishActivity(action.ActivityId, "Cancelled", message)
	asAbandonLaunchedInstance(action.AutoScalingGroupName, action.InstanceId)
	asRefreshInstanceSettled(action.AutoScalingGroupName, action.InstanceId, message)
}

// asAbandonLaunchedInstance terminates an instance whose launch was abandoned
// and has the group launch a replacement toward its desired capacity.
func asAbandonLaunchedInstance(group, instanceID string) {
	terminateASGInstance(instanceID)
	asg, ok := autoScalingGroups.Get(group)
	if !ok {
		return
	}
	if idx := indexOfString(asg.InstanceIds, instanceID); idx >= 0 {
		asg.InstanceIds = append(asg.InstanceIds[:idx], asg.InstanceIds[idx+1:]...)
	}
	cause := fmt.Sprintf("At %s an instance was started in response to a difference between desired and actual capacity, increasing the capacity from %d to %d.",
		time.Now().UTC().Format(time.RFC3339), len(asg.InstanceIds), asg.DesiredCapacity)
	if err := reconcileAutoScalingGroup(&asg, cause); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-autoscaling] replace abandoned instance %s in %s: %v\n", instanceID, group, err)
	}
}

// asFindLifecycleAction finds the open action CompleteLifecycleAction or
// RecordLifecycleActionHeartbeat addresses, by token or else by instance.
func asFindLifecycleAction(group, hook, token, instanceID string) (ASLifecycleAction, bool) {
	for _, a := range asLifecycleActions.List() {
		if a.AutoScalingGroupName != group || a.LifecycleHookName != hook || a.Completed {
			continue
		}
		if (token != "" && a.Token == token) || (token == "" && a.InstanceId == instanceID) {
			return a, true
		}
	}
	return ASLifecycleAction{}, false
}

func asNoActiveLifecycleAction(token, instanceID string) string {
	if token != "" {
		return "No active Lifecycle Action found with token " + token
	}
	return "No active Lifecycle Action found with instance ID " + instanceID
}

// asSignalLifecycleAction hands a result or a heartbeat to the action's wait.
// An action whose wait is not running is completed directly.
func asSignalLifecycleAction(action ASLifecycleAction, result string) {
	asLifecycleWaits.mu.Lock()
	wait := asLifecycleWaits.m[action.Token]
	asLifecycleWaits.mu.Unlock()
	if wait == nil {
		if result != "" {
			asCompleteLifecycleAction(action, result, "Lifecycle Action Completed with "+result+" Result")
		}
		return
	}
	if result == "" {
		select {
		case wait.heartbeat <- struct{}{}:
		default:
		}
		return
	}
	select {
	case wait.result <- result:
	default:
	}
}

// asLifecycleNotificationTarget delivers a lifecycle message to an Amazon SQS
// queue or Amazon SNS topic target.
func asLifecycleNotificationTarget(target string, message map[string]string) {
	body, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	switch {
	case strings.HasPrefix(target, "arn:aws:sqs:"):
		sqsEnqueueByARN(target, string(body))
	case strings.HasPrefix(target, "arn:aws:sns:"):
		subject := "Auto Scaling:  test notification for group " + message["AutoScalingGroupName"]
		if message["EC2InstanceId"] != "" {
			subject = "Auto Scaling:  Lifecycle action 'LAUNCHING' for instance " + message["EC2InstanceId"] + " in progress."
		}
		snsFanout(target, sim.NewUUID(), subject, string(body), nil)
	}
}

func asNotifyLifecycleAction(hook ASLifecycleHook, action ASLifecycleAction) {
	if hook.NotificationTargetARN == "" {
		return
	}
	message := map[string]string{
		"Origin":               "EC2",
		"LifecycleHookName":    hook.Name,
		"Destination":          "AutoScalingGroup",
		"AccountId":            awsAccountID(),
		"RequestId":            sim.NewUUID(),
		"LifecycleTransition":  hook.LifecycleTransition,
		"AutoScalingGroupName": hook.AutoScalingGroupName,
		"Service":              "AWS Auto Scaling",
		"Time":                 time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"EC2InstanceId":        action.InstanceId,
		"LifecycleActionToken": action.Token,
	}
	if hook.NotificationMetadata != "" {
		message["NotificationMetadata"] = hook.NotificationMetadata
	}
	asLifecycleNotificationTarget(hook.NotificationTargetARN, message)
}

// asValidateLifecycleHookTarget proves, as PutLifecycleHook does by sending a
// test notification, that the hook's role lets Amazon EC2 Auto Scaling
// publish to its target, and sends that test notification. It returns the
// ValidationError message when the role cannot publish.
func asValidateLifecycleHookTarget(asg AutoScalingGroup, hook ASLifecycleHook) string {
	if hook.NotificationTargetARN == "" {
		return ""
	}
	action := "sqs:SendMessage"
	exists := false
	switch {
	case strings.HasPrefix(hook.NotificationTargetARN, "arn:aws:sqs:"):
		_, exists = sqsQueueByARN(hook.NotificationTargetARN)
	case strings.HasPrefix(hook.NotificationTargetARN, "arn:aws:sns:"):
		action = "sns:Publish"
		_, exists = snsTopics.Get(snsTopicNameFromARN(hook.NotificationTargetARN))
	}
	if !exists || hook.RoleARN == "" ||
		iamValidateServiceRole(hook.RoleARN, "autoscaling.amazonaws.com", map[string]string{action: hook.NotificationTargetARN}) != nil {
		return fmt.Sprintf("Unable to publish test message to notification target %s using IAM role %s. Please check your target and role configuration and try to put lifecycle hook again.",
			hook.NotificationTargetARN, hook.RoleARN)
	}
	asLifecycleNotificationTarget(hook.NotificationTargetARN, map[string]string{
		"AccountId":            awsAccountID(),
		"RequestId":            sim.NewUUID(),
		"AutoScalingGroupARN":  asg.ARN,
		"AutoScalingGroupName": asg.Name,
		"Service":              "AWS Auto Scaling",
		"Event":                "autoscaling:TEST_NOTIFICATION",
		"Time":                 time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	return ""
}

// asResumeLifecycleWaits restarts the timeout of every lifecycle action a
// previous run left open.
func asResumeLifecycleWaits() {
	for _, action := range asLifecycleActions.List() {
		if action.Completed || action.StartTime == "" {
			continue
		}
		hook, ok := asLifecycleHooks.Get(asResourceKey(action.AutoScalingGroupName, action.LifecycleHookName))
		if !ok {
			asCompleteLifecycleAction(action, "ABANDON", "Lifecycle hook deleted")
			continue
		}
		asStartLifecycleWait(hook, action)
	}
}
