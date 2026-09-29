package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Application Auto Scaling target tracking. PutScalingPolicy creates the two
// CloudWatch alarms the service manages for the policy — AlarmHigh, three
// one-minute periods above the target, and AlarmLow, fifteen one-minute
// periods below 90% of it — whose actions invoke the policy. The evaluator
// invokes the policy the way CloudWatch invokes an Auto Scaling action: when
// an alarm enters ALARM, and once a minute for as long as it stays there. An
// invocation scales out or in by the target-tracking formula unless the
// policy's ScaleOutCooldown or ScaleInCooldown since the last activity in the
// same direction has not yet passed.

// appScalingEvalInterval is how often the evaluator reads the alarms. The
// alarms' periods and the policy's cooldowns, not this cadence, pace scaling.
const appScalingEvalInterval = time.Second

// appScalingAlarmActionRepeat is how often an alarm that stays in ALARM
// invokes its Auto Scaling action again: "the alarm continues to invoke the
// action once per minute that the alarm remains in the new state."
const appScalingAlarmActionRepeat = time.Minute

// appScalingDefaultCooldown is the scale-out and scale-in cooldown the
// Application Auto Scaling User Guide lists for Amazon ECS services when the
// policy sets none.
const appScalingDefaultCooldown = 300 * time.Second

const (
	appScalingAlarmPeriod          = 60
	appScalingAlarmHighPeriods     = 3
	appScalingAlarmLowPeriods      = 15
	appScalingAlarmLowTargetFactor = 0.9
)

var (
	appScalingEvalOnce sync.Once
	// appScalingAlarmInvocations records when each alarm last invoked its
	// policy, keyed by alarm name.
	appScalingAlarmInvocations sync.Map
)

// startAppScalingEvalLoop launches the periodic target-tracking evaluator. It
// is idempotent — registering Application Auto Scaling more than once (the
// in-process build path) does not start a second goroutine.
func startAppScalingEvalLoop(srv *sim.Server) {
	appScalingEvalOnce.Do(func() {
		srv.StartBackground("Application Auto Scaling evaluator", func(ctx context.Context) {
			ticker := time.NewTicker(appScalingEvalInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					appScalingEvaluatePolicies(time.Now().UTC())
				}
			}
		})
	})
}

// appScalingCapacityController abstracts the "read current capacity" and "apply
// new capacity" operations for a (ServiceNamespace, ScalableDimension) pair.
// ECS service DesiredCount is the common case; DynamoDB, Aurora, etc. can plug
// in by registering against the same dimension string.
type appScalingCapacityController interface {
	Read(target AppScalableTarget) (current int, ok bool)
	Apply(target AppScalableTarget, newCount int) bool
}

var appScalingControllers = map[string]appScalingCapacityController{
	"ecs:service:DesiredCount": ecsDesiredCountController{},
}

// AppScalingAlarm is a CloudWatch alarm Application Auto Scaling created for a
// target tracking policy.
type AppScalingAlarm struct {
	AlarmName string `json:"AlarmName"`
	AlarmARN  string `json:"AlarmARN"`
	// ScaleOut is true for the AlarmHigh alarm and false for AlarmLow.
	ScaleOut bool `json:"ScaleOut"`
}

// appScalingPolicyMetric names the CloudWatch metric a target tracking policy
// tracks. Predefined ECS metrics resolve to the AWS/ECS namespace with the
// ClusterName and ServiceName dimensions.
func appScalingPolicyMetric(resourceID string, cfg targetTrackingConfig) (namespace, name string, dims []CWDimension, statistic string, ok bool) {
	switch {
	case cfg.CustomizedMetric != nil:
		statistic = cfg.CustomizedMetric.Statistic
		if statistic == "" {
			statistic = "Average"
		}
		return cfg.CustomizedMetric.Namespace, cfg.CustomizedMetric.MetricName, cfg.CustomizedMetric.Dimensions, statistic, true
	case cfg.PredefinedMetric != nil:
		cluster, service := parseECSResourceID(resourceID)
		if cluster == "" && service == "" {
			return "", "", nil, "", false
		}
		switch strings.ToUpper(cfg.PredefinedMetric.Type) {
		case "ECSSERVICEAVERAGECPUUTILIZATION":
			name = "CPUUtilization"
		case "ECSSERVICEAVERAGEMEMORYUTILIZATION":
			name = "MemoryUtilization"
		default:
			return "", "", nil, "", false
		}
		return "AWS/ECS", name, []CWDimension{{Name: "ClusterName", Value: cluster}, {Name: "ServiceName", Value: service}}, "Average", true
	}
	return "", "", nil, "", false
}

// appScalingCreateAlarms creates the AlarmHigh and, unless scale-in is
// disabled, AlarmLow alarms for a target tracking policy.
func appScalingCreateAlarms(policy AppScalingPolicy, cfg targetTrackingConfig) []AppScalingAlarm {
	namespace, metricName, dims, statistic, ok := appScalingPolicyMetric(policy.ResourceId, cfg)
	if !ok {
		return nil
	}
	type alarmSpec struct {
		suffix     string
		periods    int32
		threshold  float64
		comparison string
		scaleOut   bool
	}
	specs := []alarmSpec{{"AlarmHigh", appScalingAlarmHighPeriods, cfg.TargetValue, "GreaterThanThreshold", true}}
	if !cfg.DisableScaleIn {
		specs = append(specs, alarmSpec{"AlarmLow", appScalingAlarmLowPeriods, cfg.TargetValue * appScalingAlarmLowTargetFactor, "LessThanThreshold", false})
	}
	var alarms []AppScalingAlarm
	for _, spec := range specs {
		name := fmt.Sprintf("TargetTracking-%s-%s-%s", policy.ResourceId, spec.suffix, sim.NewUUID())
		cwAlarms.Put(name, CWAlarm{
			AlarmName:          name,
			AlarmArn:           cwAlarmArn(name),
			AlarmDescription:   "DO NOT EDIT OR DELETE. For TargetTrackingScaling policy " + policy.PolicyARN + ".",
			Namespace:          namespace,
			MetricName:         metricName,
			Dimensions:         dims,
			Statistic:          statistic,
			Period:             appScalingAlarmPeriod,
			EvaluationPeriods:  spec.periods,
			Threshold:          spec.threshold,
			ComparisonOperator: spec.comparison,
			ActionsEnabled:     true,
			AlarmActions:       []string{policy.PolicyARN},
		})
		alarms = append(alarms, AppScalingAlarm{AlarmName: name, AlarmARN: cwAlarmArn(name), ScaleOut: spec.scaleOut})
	}
	return alarms
}

// appScalingDeleteAlarms deletes the alarms Application Auto Scaling created
// for a policy.
func appScalingDeleteAlarms(policy AppScalingPolicy) {
	for _, alarm := range policy.Alarms {
		cwAlarms.Delete(alarm.AlarmName)
		appScalingAlarmInvocations.Delete(alarm.AlarmName)
	}
}

// appScalingEvaluatePolicies invokes every target tracking policy whose alarm
// is in ALARM and due an invocation.
func appScalingEvaluatePolicies(now time.Time) {
	for _, policy := range appScalingPolicies.List() {
		if !strings.EqualFold(policy.PolicyType, "TargetTrackingScaling") {
			continue
		}
		cfg, ok := parseTargetTrackingConfig(policy.TargetTracking)
		if !ok || cfg.TargetValue <= 0 {
			continue
		}
		for _, reference := range policy.Alarms {
			alarm, ok := cwAlarms.Get(reference.AlarmName)
			if !ok || !alarm.ActionsEnabled {
				continue
			}
			state := alarm.ManualState
			if state == "" {
				state, _ = cwEvaluateAlarmStateAt(alarm, now)
			}
			if state != "ALARM" {
				appScalingAlarmInvocations.Delete(alarm.AlarmName)
				continue
			}
			if value, invoked := appScalingAlarmInvocations.Load(alarm.AlarmName); invoked {
				if last, ok := value.(time.Time); ok && now.Sub(last) < appScalingAlarmActionRepeat {
					continue
				}
			}
			appScalingAlarmInvocations.Store(alarm.AlarmName, now)
			appScalingInvokeTargetTracking(policy, cfg, alarm, reference.ScaleOut, now)
		}
	}
}

// appScalingInvokeTargetTracking is one invocation of the policy by one of its
// alarms: AlarmHigh may only add capacity and AlarmLow only remove it.
func appScalingInvokeTargetTracking(policy AppScalingPolicy, cfg targetTrackingConfig, alarm CWAlarm, scaleOut bool, now time.Time) {
	if !scaleOut && cfg.DisableScaleIn {
		return
	}
	targetKey := appScalableTargetKey(policy.ServiceNamespace, policy.ResourceId, policy.ScalableDimension)
	target, ok := appScalableTargets.Get(targetKey)
	if !ok {
		return
	}
	controller := appScalingControllers[policy.ScalableDimension]
	if controller == nil {
		return
	}
	current, ok := controller.Read(target)
	if !ok {
		return
	}
	metricValue, ok := appScalingAlarmMetricValue(alarm, now)
	if !ok {
		return
	}
	newCount := appScalingComputeCapacity(current, metricValue, cfg.TargetValue, target.MinCapacity, target.MaxCapacity)
	if (scaleOut && newCount <= current) || (!scaleOut && newCount >= current) {
		return
	}
	if !appScalingCooldownAllows(targetKey, now, scaleOut, cfg) {
		return
	}
	if !controller.Apply(target, newCount) {
		return
	}
	appScalingRecordActivity(policy, target, alarm.AlarmName, current, newCount, now)
}

// appScalingComputeCapacity is the target-tracking capacity formula. AWS uses
// floor for scale-in and ceil for scale-out (then rounds), with a 10% breach
// band; the sim uses the documented simplification: round(current * metric /
// target), clamped to bounds. Whole-number capacity only.
func appScalingComputeCapacity(current int, metricValue, targetValue float64, minCap, maxCap int) int {
	if current <= 0 {
		current = 1
	}
	raw := float64(current) * metricValue / targetValue
	newCount := int(math.Round(raw))
	if newCount < 1 {
		newCount = 1
	}
	if newCount < minCap {
		newCount = minCap
	}
	if newCount > maxCap {
		newCount = maxCap
	}
	return newCount
}

// appScalingCooldownAllows enforces the policy's ScaleOutCooldown or
// ScaleInCooldown: a scaling activity in one direction waits out the cooldown
// since the last activity in the same direction.
func appScalingCooldownAllows(targetKey string, now time.Time, scaleOut bool, cfg targetTrackingConfig) bool {
	direction, cooldownSeconds := "scale in", cfg.ScaleInCooldown
	if scaleOut {
		direction, cooldownSeconds = "scale out", cfg.ScaleOutCooldown
	}
	cooldown := appScalingDefaultCooldown
	if cooldownSeconds != nil {
		cooldown = time.Duration(*cooldownSeconds) * time.Second
	}
	var last time.Time
	for _, activity := range appScalingActivities.List() {
		if activity.Description != direction ||
			appScalableTargetKey(activity.ServiceNamespace, activity.ResourceId, activity.ScalableDimension) != targetKey {
			continue
		}
		if at := time.Unix(int64(activity.StartTime), 0).UTC(); at.After(last) {
			last = at
		}
	}
	return last.IsZero() || now.Sub(last) >= cooldown
}

// appScalingRecordActivity writes a ScalingActivity entry describing the
// change. The sim only records an activity on a real capacity change — never a
// fabricated "evaluated but unchanged" entry.
func appScalingRecordActivity(policy AppScalingPolicy, target AppScalableTarget, alarmName string, fromCount, toCount int, now time.Time) {
	direction := "scale in"
	if toCount > fromCount {
		direction = "scale out"
	}
	activity := AppScalingActivity{
		ActivityId:        appScalingActivityID(now),
		ServiceNamespace:  policy.ServiceNamespace,
		ResourceId:        policy.ResourceId,
		ScalableDimension: policy.ScalableDimension,
		Cause:             "monitor alarm " + alarmName + " in state ALARM triggered policy " + policy.PolicyName,
		Description:       direction,
		StartTime:         float64(now.Unix()),
		EndTime:           float64(now.Unix()),
		StatusCode:        "Successful",
		StatusMessage:     "Setting desired capacity to " + strconv.Itoa(toCount) + ".",
	}
	appScalingActivities.Put(activity.ActivityId, activity)
}

// appScalingActivityID produces an ActivityId that sorts newest-last (the
// DescribeScalingActivities handler sorts ascending), embedding epoch nanos so
// successive evaluations never collide.
func appScalingActivityID(now time.Time) string {
	return sim.NewUUID() + "-" + strconv.Itoa(int(now.UnixNano()))
}

// targetTrackingConfig is the typed view over the raw
// TargetTrackingScalingPolicyConfiguration JSON the policy stores.
type targetTrackingConfig struct {
	TargetValue float64
	// ScaleOutCooldown and ScaleInCooldown are in seconds; nil takes the
	// service's default.
	ScaleOutCooldown *int
	ScaleInCooldown  *int
	DisableScaleIn   bool
	PredefinedMetric *predefinedMetricConfig
	CustomizedMetric *customizedMetricConfig
}

type predefinedMetricConfig struct {
	Type          string
	ResourceLabel string
}

type customizedMetricConfig struct {
	Namespace  string
	MetricName string
	Statistic  string
	Dimensions []CWDimension
	Unit       string
}

func parseTargetTrackingConfig(raw []byte) (targetTrackingConfig, bool) {
	if len(raw) == 0 {
		return targetTrackingConfig{}, false
	}
	var dec struct {
		TargetValue      *float64 `json:"TargetValue"`
		ScaleOutCooldown *int     `json:"ScaleOutCooldown"`
		ScaleInCooldown  *int     `json:"ScaleInCooldown"`
		DisableScaleIn   *bool    `json:"DisableScaleIn"`
		Predefined       *struct {
			Type          string `json:"PredefinedMetricType"`
			ResourceLabel string `json:"ResourceLabel"`
		} `json:"PredefinedMetricSpecification"`
		Customized *struct {
			Namespace  string        `json:"Namespace"`
			MetricName string        `json:"MetricName"`
			Statistic  string        `json:"Statistic"`
			Dimensions []CWDimension `json:"Dimensions"`
			Unit       string        `json:"Unit"`
		} `json:"CustomizedMetricSpecification"`
	}
	if err := json.Unmarshal(raw, &dec); err != nil {
		return targetTrackingConfig{}, false
	}
	cfg := targetTrackingConfig{DisableScaleIn: falseIfNil(dec.DisableScaleIn)}
	if dec.TargetValue != nil {
		cfg.TargetValue = *dec.TargetValue
	}
	cfg.ScaleOutCooldown, cfg.ScaleInCooldown = dec.ScaleOutCooldown, dec.ScaleInCooldown
	if dec.Predefined != nil {
		cfg.PredefinedMetric = &predefinedMetricConfig{
			Type:          dec.Predefined.Type,
			ResourceLabel: dec.Predefined.ResourceLabel,
		}
	}
	if dec.Customized != nil {
		cfg.CustomizedMetric = &customizedMetricConfig{
			Namespace:  dec.Customized.Namespace,
			MetricName: dec.Customized.MetricName,
			Statistic:  dec.Customized.Statistic,
			Dimensions: dec.Customized.Dimensions,
			Unit:       dec.Customized.Unit,
		}
	}
	return cfg, true
}

func falseIfNil(b *bool) bool { return b != nil && *b }

// appScalingAlarmMetricValue is the metric value the alarm's newest period
// in its evaluation window holds, reduced by the alarm's statistic — the
// datapoint that put the alarm in ALARM.
func appScalingAlarmMetricValue(alarm CWAlarm, now time.Time) (float64, bool) {
	period := int64(alarm.Period)
	if period <= 0 {
		period = appScalingAlarmPeriod
	}
	windowStart := now.Unix() - int64(alarm.EvaluationPeriods)*period
	data, _ := cwMetrics.Get(metricsKey(alarm.Namespace, alarm.MetricName, alarm.Dimensions))
	newest := int64(-1)
	var values []float64
	for _, datum := range data {
		timestamp := int64(datum.Timestamp)
		if timestamp < windowStart || timestamp > now.Unix() {
			continue
		}
		bucket := timestamp / period * period
		switch {
		case bucket > newest:
			newest, values = bucket, []float64{datum.Value}
		case bucket == newest:
			values = append(values, datum.Value)
		}
	}
	if len(values) == 0 {
		return 0, false
	}
	return cwApplyAlarmStat(alarm, values), true
}

// parseECSResourceID splits an Application Auto Scaling ECS resource ID into
// cluster name and service name. The canonical form is
// `service/<cluster>/<service>`; the legacy `service/<service>` form (no
// cluster) resolves against the implicit "default" cluster.
func parseECSResourceID(resourceID string) (cluster, service string) {
	if !strings.HasPrefix(resourceID, "service/") {
		return "", ""
	}
	rest := strings.TrimPrefix(resourceID, "service/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "default", parts[0]
}

// ecsDesiredCountController implements capacityController for
// ecs:service:DesiredCount. Reading returns the service's current DesiredCount;
// applying mutates the service's DesiredCount and asks the real service
// scheduler to converge durable tasks.
type ecsDesiredCountController struct{}

func (ecsDesiredCountController) Read(target AppScalableTarget) (int, bool) {
	cluster, service := parseECSResourceID(target.ResourceId)
	if cluster == "" && service == "" {
		return 0, false
	}
	svc, ok := ecsServices.Get(ecsServiceKey(cluster, service))
	if !ok {
		return 0, false
	}
	return svc.DesiredCount, true
}

func (ecsDesiredCountController) Apply(target AppScalableTarget, newCount int) bool {
	cluster, service := parseECSResourceID(target.ResourceId)
	if cluster == "" && service == "" {
		return false
	}
	key := ecsServiceKey(cluster, service)
	svc, ok := ecsServices.Get(key)
	if !ok {
		return false
	}
	svc.DesiredCount = newCount
	now := float64(time.Now().Unix())
	ecsUpdatePrimaryDeploymentCounts(&svc, now)
	ecsServices.Put(key, svc)
	ecsRequestServiceReconcile(key)
	return true
}
