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

// PutScalingPolicy creates the two alarms Application Auto Scaling manages:
// AlarmHigh, three one-minute periods above the target, and AlarmLow, fifteen
// one-minute periods below 90% of it. Like Amazon CloudWatch, the evaluator
// invokes the policy when an alarm enters ALARM and once a minute while it
// stays there, subject to the policy's cooldown in that direction.

// The alarms' periods and the cooldowns, not this cadence, pace scaling.
const appScalingEvalInterval = time.Second

// "The alarm continues to invoke the action once per minute that the alarm
// remains in the new state."
const appScalingAlarmActionRepeat = time.Minute

// The Application Auto Scaling User Guide's Amazon ECS default when the policy
// sets no cooldown.
const appScalingDefaultCooldown = 300 * time.Second

const (
	appScalingAlarmPeriod          = 60
	appScalingAlarmHighPeriods     = 3
	appScalingAlarmLowPeriods      = 15
	appScalingAlarmLowTargetFactor = 0.9
)

var (
	appScalingEvalOnce         sync.Once
	appScalingAlarmInvocations sync.Map
)

// Registering the service more than once starts only one evaluator.
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

// A capacity controller reads and applies capacity for one
// (ServiceNamespace, ScalableDimension) pair.
type appScalingCapacityController interface {
	Read(target AppScalableTarget) (current int, ok bool)
	Apply(target AppScalableTarget, newCount int) bool
}

var appScalingControllers = map[string]appScalingCapacityController{
	"ecs:service:DesiredCount": ecsDesiredCountController{},
}

type AppScalingAlarm struct {
	AlarmName string `json:"AlarmName"`
	AlarmARN  string `json:"AlarmARN"`
	// ScaleOut is true for the AlarmHigh alarm and false for AlarmLow.
	ScaleOut bool `json:"ScaleOut"`
}

// The predefined Amazon ECS metrics resolve to AWS/ECS with ClusterName and
// ServiceName; ALBRequestCountPerTarget resolves to the RequestCountPerTarget
// an Application Load Balancer publishes for the target group ResourceLabel
// names.
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
		case "ALBREQUESTCOUNTPERTARGET":
			targetGroup, ok := appScalingResourceLabelTargetGroup(cfg.PredefinedMetric.ResourceLabel)
			if !ok {
				return "", "", nil, "", false
			}
			return elbv2MetricNamespace, "RequestCountPerTarget", []CWDimension{targetGroup}, "Sum", true
		default:
			return "", "", nil, "", false
		}
		return "AWS/ECS", name, []CWDimension{{Name: "ClusterName", Value: cluster}, {Name: "ServiceName", Value: service}}, "Average", true
	}
	return "", "", nil, "", false
}

// A ResourceLabel reads "app/<lb-name>/<lb-id>/targetgroup/<tg-name>/<tg-id>".
func appScalingResourceLabelTargetGroup(label string) (CWDimension, bool) {
	loadBalancer, targetGroup, ok := strings.Cut(label, "/targetgroup/")
	if !ok || strings.Count(loadBalancer, "/") != 2 || !strings.HasPrefix(loadBalancer, "app/") || strings.Count(targetGroup, "/") != 1 {
		return CWDimension{}, false
	}
	return CWDimension{Name: "TargetGroup", Value: "targetgroup/" + targetGroup}, true
}

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

func appScalingDeleteAlarms(policy AppScalingPolicy) {
	for _, alarm := range policy.Alarms {
		cwAlarms.Delete(alarm.AlarmName)
		appScalingAlarmInvocations.Delete(alarm.AlarmName)
	}
}

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

// AlarmHigh may only add capacity and AlarmLow only remove it.
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

// A target tracking metric must "increase or decrease in inverse proportion
// to the number of capacity units", so ceil(current * metric / target),
// clamped to the target's bounds, is the smallest capacity meeting it.
func appScalingComputeCapacity(current int, metricValue, targetValue float64, minCap, maxCap int) int {
	if current <= 0 {
		current = 1
	}
	// Round away the quotient's binary error so a metric at an exact multiple of
	// the target does not ceil one unit high.
	raw := math.Round(float64(current)*metricValue/targetValue*1e9) / 1e9
	newCount := int(math.Ceil(raw))
	if newCount < 1 {
		newCount = 1
	}
	return min(max(newCount, minCap), maxCap)
}

// An activity waits out the cooldown since the last activity in the same
// direction.
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

func appScalingActivityID(now time.Time) string {
	return sim.NewUUID() + "-" + strconv.Itoa(int(now.UnixNano()))
}

type targetTrackingConfig struct {
	TargetValue float64
	// Cooldowns are in seconds; nil takes the service default.
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

// appScalingAlarmMetricValue returns the newest period's statistic in the
// alarm's evaluation window: the datapoint that put it in ALARM.
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

// The legacy `service/<service>` form resolves against the "default" cluster.
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

// Applying a count asks the service scheduler to converge the tasks.
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
