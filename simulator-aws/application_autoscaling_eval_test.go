package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func appScalingTestStores(t *testing.T) {
	t.Helper()
	bg.Await()
	appScalableTargets = sim.MakeStore[AppScalableTarget](nil, "app_scalable_targets")
	appScalingPolicies = sim.MakeStore[AppScalingPolicy](nil, "app_scaling_policies")
	appScalingActivities = sim.MakeStore[AppScalingActivity](nil, "app_scaling_activities")
	cwAlarms = sim.MakeStore[CWAlarm](nil, "cw_alarms")
	cwMetrics = sim.MakeStore[[]CWMetricDatum](nil, "cw_metrics")
	ecsServices = sim.MakeStore[ECSService](nil, "ecs_services")
	appScalingAlarmInvocations.Clear()
	t.Cleanup(bg.Await)
}

// appScalingTrackedService registers an Amazon ECS service as a scalable
// target and puts a CPU target tracking policy on it, returning the policy.
func appScalingTrackedService(t *testing.T, service string, desired int, configuration map[string]any) AppScalingPolicy {
	t.Helper()
	ecsServices.Put(ecsServiceKey("tracking", service), ECSService{ServiceName: service, DesiredCount: desired})
	resourceID := "service/tracking/" + service
	appScalableTargets.Put(appScalableTargetKey("ecs", resourceID, "ecs:service:DesiredCount"), AppScalableTarget{
		ServiceNamespace: "ecs", ResourceId: resourceID, ScalableDimension: "ecs:service:DesiredCount",
		MinCapacity: 1, MaxCapacity: 10,
	})
	configuration["PredefinedMetricSpecification"] = map[string]any{"PredefinedMetricType": "ECSServiceAverageCPUUtilization"}
	code, out := sqsCall(t, handleAppASPutScalingPolicy, map[string]any{
		"PolicyName": "cpu", "ServiceNamespace": "ecs", "ResourceId": resourceID, "ScalableDimension": "ecs:service:DesiredCount",
		"PolicyType": "TargetTrackingScaling", "TargetTrackingScalingPolicyConfiguration": configuration,
	})
	if code != http.StatusOK {
		t.Fatalf("PutScalingPolicy: %d %v", code, out)
	}
	policy, _ := appScalingPolicies.Get(appScalingPolicyKey("ecs", resourceID, "ecs:service:DesiredCount", "cpu"))
	if len(out["Alarms"].([]any)) != len(policy.Alarms) {
		t.Fatalf("PutScalingPolicy returned alarms %v, want the %d it created", out["Alarms"], len(policy.Alarms))
	}
	return policy
}

// appScalingPutCPU records one CPU datapoint per minute over the last minutes.
func appScalingPutCPU(service string, value float64, minutes int, now time.Time) {
	dims := []CWDimension{{Name: "ClusterName", Value: "tracking"}, {Name: "ServiceName", Value: service}}
	key := metricsKey("AWS/ECS", "CPUUtilization", dims)
	data, _ := cwMetrics.Get(key)
	for minute := minutes - 1; minute >= 0; minute-- {
		data = append(data, CWMetricDatum{
			Namespace: "AWS/ECS", MetricName: "CPUUtilization", Dimensions: dims, Value: value,
			Timestamp: float64(now.Add(-time.Duration(minute) * time.Minute).Unix()),
		})
	}
	cwMetrics.Put(key, data)
}

func appScalingDesired(t *testing.T, service string) int {
	t.Helper()
	stored, _ := ecsServices.Get(ecsServiceKey("tracking", service))
	return stored.DesiredCount
}

// PutScalingPolicy creates the AlarmHigh and AlarmLow alarms Application Auto
// Scaling manages for a target tracking policy, and DeleteScalingPolicy
// removes them.
func TestAppScalingTargetTrackingCreatesItsAlarms(t *testing.T) {
	appScalingTestStores(t)
	policy := appScalingTrackedService(t, "alarms", 1, map[string]any{"TargetValue": 50.0})
	if len(policy.Alarms) != 2 {
		t.Fatalf("policy alarms %v, want AlarmHigh and AlarmLow", policy.Alarms)
	}
	for _, reference := range policy.Alarms {
		alarm, ok := cwAlarms.Get(reference.AlarmName)
		if !ok {
			t.Fatalf("alarm %s was not created in CloudWatch", reference.AlarmName)
		}
		periods, threshold, comparison := int32(3), 50.0, "GreaterThanThreshold"
		if !reference.ScaleOut {
			periods, threshold, comparison = 15, 45.0, "LessThanThreshold"
		}
		if !strings.HasPrefix(alarm.AlarmName, "TargetTracking-service/tracking/alarms-Alarm") || alarm.Period != 60 ||
			alarm.EvaluationPeriods != periods || alarm.Threshold != threshold || alarm.ComparisonOperator != comparison ||
			alarm.Namespace != "AWS/ECS" || alarm.MetricName != "CPUUtilization" ||
			len(alarm.AlarmActions) != 1 || alarm.AlarmActions[0] != policy.PolicyARN {
			t.Fatalf("alarm %+v, want %d one-minute periods %s %g invoking %s", alarm, periods, comparison, threshold, policy.PolicyARN)
		}
	}
	if code, out := sqsCall(t, handleAppASDeleteScalingPolicy, map[string]any{
		"PolicyName": "cpu", "ServiceNamespace": "ecs", "ResourceId": "service/tracking/alarms", "ScalableDimension": "ecs:service:DesiredCount",
	}); code != http.StatusOK {
		t.Fatalf("DeleteScalingPolicy: %d %v", code, out)
	}
	for _, reference := range policy.Alarms {
		if _, ok := cwAlarms.Get(reference.AlarmName); ok {
			t.Fatalf("alarm %s outlived its policy", reference.AlarmName)
		}
	}

	disabled := appScalingTrackedService(t, "no-scale-in", 1, map[string]any{"TargetValue": 50.0, "DisableScaleIn": true})
	if len(disabled.Alarms) != 1 || !disabled.Alarms[0].ScaleOut {
		t.Fatalf("a policy with scale-in disabled has alarms %v, want AlarmHigh alone", disabled.Alarms)
	}
}

// AlarmHigh invokes the policy when three one-minute periods breach the
// target and again each minute it stays in ALARM; the policy's own
// ScaleOutCooldown spaces the scale-outs.
func TestAppScalingTargetTrackingScalesOutOnAlarmHighWithItsCooldown(t *testing.T) {
	appScalingTestStores(t)
	appScalingTrackedService(t, "out", 1, map[string]any{"TargetValue": 50.0, "ScaleOutCooldown": 120})
	now := time.Now().UTC()

	appScalingPutCPU("out", 90, 2, now)
	appScalingEvaluatePolicies(now)
	if desired := appScalingDesired(t, "out"); desired != 1 {
		t.Fatalf("desired %d after two breaching minutes, want 1: AlarmHigh needs three", desired)
	}
	for _, step := range []struct {
		offset  time.Duration
		desired int
	}{
		{0, 2},                   // AlarmHigh enters ALARM: round(1 * 90 / 50)
		{30 * time.Second, 2},    // the alarm invokes the policy once a minute
		{61 * time.Second, 2},    // invoked again, inside the 120-second cooldown
		{125 * time.Second, 4},   // cooldown over: round(2 * 90 / 50)
		{150 * time.Second, 4},   // not yet a minute since the last invocation
		{185 * time.Second, 4},   // invoked, inside the cooldown of the second scale-out
		{246 * time.Second, 7},   // round(4 * 90 / 50)
		{1000 * time.Second, 10}, // clamped to MaxCapacity
	} {
		appScalingPutCPU("out", 90, 3, now.Add(step.offset))
		appScalingEvaluatePolicies(now.Add(step.offset))
		if desired := appScalingDesired(t, "out"); desired != step.desired {
			t.Fatalf("at +%s desired %d, want %d", step.offset, desired, step.desired)
		}
	}
	activities := appScalingActivities.List()
	if len(activities) != 4 || !strings.Contains(activities[0].Cause, "-AlarmHigh-") || !strings.Contains(activities[0].Cause, "in state ALARM triggered policy cpu") {
		t.Fatalf("activities %+v, want four scale-outs caused by AlarmHigh", activities)
	}
}

// AlarmLow invokes the policy after fifteen one-minute periods below 90% of
// the target, and without a ScaleInCooldown the Amazon ECS default of 300
// seconds spaces the scale-ins.
func TestAppScalingTargetTrackingScalesInOnAlarmLowWithTheDefaultCooldown(t *testing.T) {
	appScalingTestStores(t)
	appScalingTrackedService(t, "in", 8, map[string]any{"TargetValue": 50.0})
	now := time.Now().UTC()

	appScalingPutCPU("in", 20, 14, now)
	appScalingEvaluatePolicies(now)
	if desired := appScalingDesired(t, "in"); desired != 8 {
		t.Fatalf("desired %d after fourteen low minutes, want 8: AlarmLow needs fifteen", desired)
	}
	for _, step := range []struct {
		offset  time.Duration
		desired int
	}{
		{0, 3},                 // round(8 * 20 / 50)
		{61 * time.Second, 3},  // inside the 300-second default cooldown
		{301 * time.Second, 1}, // round(3 * 20 / 50)
	} {
		appScalingPutCPU("in", 20, 15, now.Add(step.offset))
		appScalingEvaluatePolicies(now.Add(step.offset))
		if desired := appScalingDesired(t, "in"); desired != step.desired {
			t.Fatalf("at +%s desired %d, want %d", step.offset, desired, step.desired)
		}
	}
}
