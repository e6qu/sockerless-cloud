package main

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Application Auto Scaling target-tracking evaluator. AWS runs the
// target-tracking algorithm against the CloudWatch metric the policy points at
// and adjusts the scalable target's capacity every minute; the sim runs the
// same algorithm on a shorter cadence so a test can observe a scale-out /
// scale-in within seconds rather than minutes. The math is faithful to the
// documented algorithm: new capacity = round(current * metricValue / target),
// clamped to [MinCapacity, MaxCapacity].

// appScalingEvalInterval is the cadence at which the evaluator runs. Real AWS
// warms up for two minutes before the first evaluation and re-evaluates each
// minute; the sim collapses that to seconds so a target-tracking policy is
// observable inside a test.
const appScalingEvalInterval = 3 * time.Second

// appScalingCooldown is the minimum gap between two capacity changes for the
// same target. Application Auto Scaling honours ScaleInCooldown /
// ScaleOutCooldown from the policy; the sim applies a single short cooldown so
// successive ticks do not thrash the service while a metric is held high.
const appScalingCooldown = 5 * time.Second

var (
	appScalingEvalOnce sync.Once
)

// startAppScalingEvalLoop launches the periodic target-tracking evaluator. It
// is idempotent — registering Application Auto Scaling more than once (the
// in-process build path) does not start a second goroutine.
func startAppScalingEvalLoop(srv *sim.Server) {
	appScalingEvalOnce.Do(func() {
		srv.StartBackground(func(ctx context.Context) {
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

// appScalingEvaluatePolicies runs one evaluation pass over every registered
// target-tracking policy and adjusts capacity when the metric diverges from
// the target value.
func appScalingEvaluatePolicies(now time.Time) {
	for _, policy := range appScalingPolicies.List() {
		if !strings.EqualFold(policy.PolicyType, "TargetTrackingScaling") {
			continue
		}
		cfg, ok := parseTargetTrackingConfig(policy.TargetTracking)
		if !ok || cfg.TargetValue <= 0 {
			continue
		}
		targetKey := appScalableTargetKey(policy.ServiceNamespace, policy.ResourceId, policy.ScalableDimension)
		target, ok := appScalableTargets.Get(targetKey)
		if !ok {
			continue
		}
		controller := appScalingControllers[policy.ScalableDimension]
		if controller == nil {
			continue
		}
		current, ok := controller.Read(target)
		if !ok {
			continue
		}
		metricValue, ok := appScalingMetricValue(target, cfg)
		if !ok {
			continue
		}
		newCount := appScalingComputeCapacity(current, metricValue, cfg.TargetValue, target.MinCapacity, target.MaxCapacity)
		if newCount == current {
			continue
		}
		scaleOut := newCount > current
		if !scaleOut && cfg.DisableScaleIn {
			continue
		}
		if !appScalingCooldownAllows(targetKey, now, scaleOut, cfg) {
			continue
		}
		if !controller.Apply(target, newCount) {
			continue
		}
		appScalingRecordActivity(policy, target, current, newCount, now)
	}
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

// appScalingCooldownAllows enforces ScaleOutCooldown / ScaleInCooldown. AWS
// keeps separate cooldown windows for scale-out and scale-in; the sim honours
// whichever applies to the pending change. A short floor (appScalingCooldown)
// keeps a single metric datapoint from triggering two changes back-to-back.
func appScalingCooldownAllows(targetKey string, now time.Time, scaleOut bool, cfg targetTrackingConfig) bool {
	var last time.Time
	for _, activity := range appScalingActivities.List() {
		if appScalableTargetKey(activity.ServiceNamespace, activity.ResourceId, activity.ScalableDimension) != targetKey {
			continue
		}
		at := time.Unix(int64(activity.StartTime), 0).UTC()
		if at.After(last) {
			last = at
		}
	}
	if last.IsZero() {
		return true
	}
	cooldown := time.Duration(cfg.ScaleOutCooldown) * time.Second
	if !scaleOut {
		cooldown = time.Duration(cfg.ScaleInCooldown) * time.Second
	}
	if cooldown <= 0 {
		cooldown = appScalingCooldown
	}
	return now.Sub(last) >= cooldown
}

// appScalingRecordActivity writes a ScalingActivity entry describing the
// change. The sim only records an activity on a real capacity change — never a
// fabricated "evaluated but unchanged" entry.
func appScalingRecordActivity(policy AppScalingPolicy, target AppScalableTarget, fromCount, toCount int, now time.Time) {
	direction := "scale in"
	if toCount > fromCount {
		direction = "scale out"
	}
	activity := AppScalingActivity{
		ActivityId:        appScalingActivityID(now),
		ServiceNamespace:  policy.ServiceNamespace,
		ResourceId:        policy.ResourceId,
		ScalableDimension: policy.ScalableDimension,
		Cause:             "due to a target-tracking scaling policy: " + policy.PolicyName,
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
	return generateUUID() + "-" + strconv.Itoa(int(now.UnixNano()))
}

// targetTrackingConfig is the typed view over the raw
// TargetTrackingScalingPolicyConfiguration JSON the policy stores.
type targetTrackingConfig struct {
	TargetValue      float64
	ScaleOutCooldown int
	ScaleInCooldown  int
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
	if dec.ScaleOutCooldown != nil {
		cfg.ScaleOutCooldown = *dec.ScaleOutCooldown
	}
	if dec.ScaleInCooldown != nil {
		cfg.ScaleInCooldown = *dec.ScaleInCooldown
	}
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

// appScalingMetricValue reads the current value of the policy's metric from
// CloudWatch Metrics. Predefined ECS metrics resolve to the AWS/ECS namespace
// with ClusterName + ServiceName dimensions; customized metrics use the
// namespace / name / dimensions the caller supplied.
func appScalingMetricValue(target AppScalableTarget, cfg targetTrackingConfig) (float64, bool) {
	switch {
	case cfg.PredefinedMetric != nil:
		return appScalingPredefinedMetricValue(target, cfg.PredefinedMetric)
	case cfg.CustomizedMetric != nil:
		return appScalingReadMetric(cfg.CustomizedMetric.Namespace, cfg.CustomizedMetric.MetricName,
			cfg.CustomizedMetric.Dimensions, cfg.CustomizedMetric.Statistic)
	}
	return 0, false
}

// appScalingPredefinedMetricValue maps an Application Auto Scaling predefined
// metric type to its (namespace, metricName, dimensions) triple, then reads it.
// Only the ECS service family is implemented today — DynamoDB and Aurora
// predefined metrics return false (no scaling) until their controllers plug in.
func appScalingPredefinedMetricValue(target AppScalableTarget, pm *predefinedMetricConfig) (float64, bool) {
	cluster, service := parseECSResourceID(target.ResourceId)
	if cluster == "" && service == "" {
		return 0, false
	}
	var metricName string
	switch strings.ToUpper(pm.Type) {
	case "ECSSERVICEAVERAGECPUUTILIZATION":
		metricName = "CPUUtilization"
	case "ECSSERVICEAVERAGEMEMORYUTILIZATION":
		metricName = "MemoryUtilization"
	default:
		return 0, false
	}
	dims := []CWDimension{
		{Name: "ClusterName", Value: cluster},
		{Name: "ServiceName", Value: service},
	}
	return appScalingReadMetric("AWS/ECS", metricName, dims, "Average")
}

// appScalingReadMetric computes the current value of a metric by reading the
// CloudWatch Metrics store directly (the same store PutMetricData writes to)
// and reducing the last 5 minutes of datapoints with the requested statistic.
// Defaulting to Average matches the predefined ECS metric semantics.
func appScalingReadMetric(namespace, metricName string, dims []CWDimension, stat string) (float64, bool) {
	if stat == "" {
		stat = "Average"
	}
	key := metricsKey(namespace, metricName, dims)
	data, ok := cwMetrics.Get(key)
	if !ok || len(data) == 0 {
		return 0, false
	}
	cutoff := float64(time.Now().Add(-5 * time.Minute).Unix())
	var recent []float64
	for _, d := range data {
		if d.Timestamp < cutoff {
			continue
		}
		recent = append(recent, d.Value)
	}
	if len(recent) == 0 {
		return 0, false
	}
	return cwApplyStat(stat, recent), true
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
