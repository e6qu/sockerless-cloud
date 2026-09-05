package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Application Auto Scaling — terraform-provider-aws and the AWS SDK use this
// service (distinct from EC2 Auto Scaling in autoscaling.go) to scale ECS
// services, DynamoDB tables, Aurora replicas, and the like. Runner platform
// modules declare `aws_appautoscaling_target` + `aws_appautoscaling_policy`
// to autoscale the ECS service that backs sockerless. Wire format is the JSON
// protocol with `X-Amz-Target: AnyScaleFrontendService.<Action>`.

// AppScalableTarget is the registration that bounds a resource's capacity.
// The identity is the (ServiceNamespace, ResourceId, ScalableDimension)
// triple. Config blocks are held as raw JSON so they round-trip byte-exact.
type AppScalableTarget struct {
	ServiceNamespace  string            `json:"ServiceNamespace"`
	ResourceId        string            `json:"ResourceId"`
	ScalableDimension string            `json:"ScalableDimension"`
	MinCapacity       int               `json:"MinCapacity"`
	MaxCapacity       int               `json:"MaxCapacity"`
	RoleARN           string            `json:"RoleARN,omitempty"`
	CreationTime      float64           `json:"CreationTime"`
	ARN               string            `json:"ScalableTargetARN"`
	SuspendedState    json.RawMessage   `json:"SuspendedState,omitempty"`
	Tags              map[string]string `json:"Tags,omitempty"`
}

// AppScalingPolicy attaches a scaling rule to a scalable target. Identity is
// the target triple plus PolicyName.
type AppScalingPolicy struct {
	PolicyName        string          `json:"PolicyName"`
	PolicyARN         string          `json:"PolicyARN"`
	ServiceNamespace  string          `json:"ServiceNamespace"`
	ResourceId        string          `json:"ResourceId"`
	ScalableDimension string          `json:"ScalableDimension"`
	PolicyType        string          `json:"PolicyType"`
	TargetTracking    json.RawMessage `json:"TargetTrackingScalingPolicyConfiguration,omitempty"`
	StepScaling       json.RawMessage `json:"StepScalingPolicyConfiguration,omitempty"`
	CreationTime      float64         `json:"CreationTime"`
}

// AppScheduledAction is a one-off or recurring schedule that adjusts a
// scalable target's min/max capacity at a given time. Identity is the
// (ServiceNamespace, ResourceId, ScheduledActionName) triple.
type AppScheduledAction struct {
	ScheduledActionName  string          `json:"ScheduledActionName"`
	ScheduledActionARN   string          `json:"ScheduledActionARN"`
	ServiceNamespace     string          `json:"ServiceNamespace"`
	Schedule             string          `json:"Schedule"`
	Timezone             string          `json:"Timezone,omitempty"`
	ResourceId           string          `json:"ResourceId"`
	ScalableDimension    string          `json:"ScalableDimension,omitempty"`
	StartTime            float64         `json:"StartTime,omitempty"`
	EndTime              float64         `json:"EndTime,omitempty"`
	ScalableTargetAction json.RawMessage `json:"ScalableTargetAction,omitempty"`
	CreationTime         float64         `json:"CreationTime"`
}

// AppScalingActivity is one entry in a scalable target's activity log. The sim
// records an activity only when a real capacity change occurs; the store is
// empty (and DescribeScalingActivities returns []) until then — faithful, never
// fabricated.
type AppScalingActivity struct {
	ActivityId        string  `json:"ActivityId"`
	ServiceNamespace  string  `json:"ServiceNamespace"`
	ResourceId        string  `json:"ResourceId"`
	ScalableDimension string  `json:"ScalableDimension"`
	Description       string  `json:"Description"`
	Cause             string  `json:"Cause"`
	StartTime         float64 `json:"StartTime"`
	EndTime           float64 `json:"EndTime,omitempty"`
	StatusCode        string  `json:"StatusCode"`
	StatusMessage     string  `json:"StatusMessage,omitempty"`
	Details           string  `json:"Details,omitempty"`
}

var (
	appScalableTargets   sim.Store[AppScalableTarget]
	appScalingPolicies   sim.Store[AppScalingPolicy]
	appScheduledActions  sim.Store[AppScheduledAction]
	appScalingActivities sim.Store[AppScalingActivity]
)

func registerApplicationAutoScaling(r *AWSRouter, srv *sim.Server, startBackgroundEvaluator bool) {
	appScalableTargets = sim.MakeStore[AppScalableTarget](srv.DB(), "app_scalable_targets")
	appScalingPolicies = sim.MakeStore[AppScalingPolicy](srv.DB(), "app_scaling_policies")
	appScheduledActions = sim.MakeStore[AppScheduledAction](srv.DB(), "app_scheduled_actions")
	appScalingActivities = sim.MakeStore[AppScalingActivity](srv.DB(), "app_scaling_activities")

	r.Register("AnyScaleFrontendService.RegisterScalableTarget", handleAppASRegisterScalableTarget)
	r.Register("AnyScaleFrontendService.DeregisterScalableTarget", handleAppASDeregisterScalableTarget)
	r.Register("AnyScaleFrontendService.DescribeScalableTargets", handleAppASDescribeScalableTargets)
	r.Register("AnyScaleFrontendService.PutScalingPolicy", handleAppASPutScalingPolicy)
	r.Register("AnyScaleFrontendService.DeleteScalingPolicy", handleAppASDeleteScalingPolicy)
	r.Register("AnyScaleFrontendService.DescribeScalingPolicies", handleAppASDescribeScalingPolicies)
	r.Register("AnyScaleFrontendService.ListTagsForResource", handleAppASListTagsForResource)
	r.Register("AnyScaleFrontendService.TagResource", handleAppASTagResource)
	r.Register("AnyScaleFrontendService.UntagResource", handleAppASUntagResource)
	r.Register("AnyScaleFrontendService.PutScheduledAction", handleAppASPutScheduledAction)
	r.Register("AnyScaleFrontendService.DeleteScheduledAction", handleAppASDeleteScheduledAction)
	r.Register("AnyScaleFrontendService.DescribeScheduledActions", handleAppASDescribeScheduledActions)
	r.Register("AnyScaleFrontendService.DescribeScalingActivities", handleAppASDescribeScalingActivities)
	r.Register("AnyScaleFrontendService.GetPredictiveScalingForecast", handleAppASGetPredictiveScalingForecast)

	if startBackgroundEvaluator {
		// Evaluate target-tracking policies and adjust capacity on a short
		// cadence so a policy is observable inside a test. Idempotent across
		// re-registrations in one process.
		startAppScalingEvalLoop(srv)
	}
}

// appScalableTargetKey is the storage key for the identity triple.
func appScalableTargetKey(ns, resourceID, dim string) string {
	return ns + "|" + resourceID + "|" + dim
}

func appScalingPolicyKey(ns, resourceID, dim, name string) string {
	return ns + "|" + resourceID + "|" + dim + "|" + name
}

func appScalableTargetARN(id string) string {
	return fmt.Sprintf("arn:aws:application-autoscaling:%s:%s:scalable-target/%s",
		awsRegion(), awsAccountID(), id)
}

// appScalingPolicyARN matches the real PolicyARN shape, which embeds the
// resource path and policy name.
func appScalingPolicyARN(ns, resourceID, name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:scalingPolicy:%s:resource/%s/%s:policyName/%s",
		awsRegion(), awsAccountID(), generateUUID(), ns, resourceID, name)
}

func handleAppASRegisterScalableTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceNamespace  string            `json:"ServiceNamespace"`
		ResourceId        string            `json:"ResourceId"`
		ScalableDimension string            `json:"ScalableDimension"`
		MinCapacity       *int              `json:"MinCapacity"`
		MaxCapacity       *int              `json:"MaxCapacity"`
		RoleARN           string            `json:"RoleARN"`
		SuspendedState    json.RawMessage   `json:"SuspendedState"`
		Tags              map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceNamespace == "" || req.ResourceId == "" || req.ScalableDimension == "" {
		AWSError(w, "ValidationException",
			"ServiceNamespace, ResourceId, and ScalableDimension are required", http.StatusBadRequest)
		return
	}
	key := appScalableTargetKey(req.ServiceNamespace, req.ResourceId, req.ScalableDimension)

	// Register is upsert: an existing target keeps fields the caller omits.
	target, exists := appScalableTargets.Get(key)
	if !exists {
		target = AppScalableTarget{
			ServiceNamespace:  req.ServiceNamespace,
			ResourceId:        req.ResourceId,
			ScalableDimension: req.ScalableDimension,
			CreationTime:      float64(time.Now().Unix()),
			ARN:               appScalableTargetARN(generateUUID()),
		}
	}
	if req.MinCapacity != nil {
		target.MinCapacity = *req.MinCapacity
	}
	if req.MaxCapacity != nil {
		target.MaxCapacity = *req.MaxCapacity
	}
	// The role Application Auto Scaling modifies the target through. The model
	// marks it required on the ScalableTarget a describe returns, and its own
	// documentation says why a register may omit it: where the service
	// supports a service-linked role, Application Auto Scaling uses one and
	// creates it if it does not yet exist. So this account gets that role —
	// through IAM, where a caller can then read it — rather than the describe
	// answering without the member a client dereferences.
	switch {
	case req.RoleARN != "":
		target.RoleARN = req.RoleARN
	case target.RoleARN == "":
		target.RoleARN = iamEnsureServiceLinkedRole(
			target.ServiceNamespace+".application-autoscaling.amazonaws.com",
			"Service-linked role for Application Auto Scaling")
	}
	if req.SuspendedState != nil {
		target.SuspendedState = req.SuspendedState
	}
	if req.Tags != nil {
		target.Tags = req.Tags
	}
	appScalableTargets.Put(key, target)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ScalableTargetARN": target.ARN,
	})
}

func handleAppASDeregisterScalableTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceNamespace  string `json:"ServiceNamespace"`
		ResourceId        string `json:"ResourceId"`
		ScalableDimension string `json:"ScalableDimension"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := appScalableTargetKey(req.ServiceNamespace, req.ResourceId, req.ScalableDimension)
	if _, ok := appScalableTargets.Get(key); !ok {
		AWSErrorf(w, "ObjectNotFoundException", http.StatusBadRequest,
			"No scalable target registered for %s/%s/%s",
			req.ServiceNamespace, req.ResourceId, req.ScalableDimension)
		return
	}
	appScalableTargets.Delete(key)
	// Deregistering a target removes its policies too.
	for _, p := range appScalingPolicies.List() {
		if p.ServiceNamespace == req.ServiceNamespace &&
			p.ResourceId == req.ResourceId &&
			p.ScalableDimension == req.ScalableDimension {
			appScalingPolicies.Delete(appScalingPolicyKey(p.ServiceNamespace, p.ResourceId, p.ScalableDimension, p.PolicyName))
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleAppASDescribeScalableTargets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceNamespace  string   `json:"ServiceNamespace"`
		ResourceIds       []string `json:"ResourceIds"`
		ScalableDimension string   `json:"ScalableDimension"`
		MaxResults        *int32   `json:"MaxResults"`
		NextToken         string   `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceNamespace == "" {
		AWSError(w, "ValidationException", "ServiceNamespace is required", http.StatusBadRequest)
		return
	}
	wantIDs := map[string]bool{}
	for _, id := range req.ResourceIds {
		wantIDs[id] = true
	}
	matched := appScalableTargets.Filter(func(t AppScalableTarget) bool {
		if t.ServiceNamespace != req.ServiceNamespace {
			return false
		}
		if len(wantIDs) > 0 && !wantIDs[t.ResourceId] {
			return false
		}
		if req.ScalableDimension != "" && t.ScalableDimension != req.ScalableDimension {
			return false
		}
		return true
	})
	matched = sortBy(matched, func(t AppScalableTarget) string { return t.ResourceId })
	page, next := awsPageExplicit(matched, req.NextToken, awsMaxResults(req.MaxResults))

	out := make([]map[string]any, 0, len(page))
	for _, t := range page {
		out = append(out, scalableTargetToJSON(t))
	}
	resp := map[string]any{"ScalableTargets": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func scalableTargetToJSON(t AppScalableTarget) map[string]any {
	m := map[string]any{
		"ServiceNamespace":  t.ServiceNamespace,
		"ResourceId":        t.ResourceId,
		"ScalableDimension": t.ScalableDimension,
		"MinCapacity":       t.MinCapacity,
		"MaxCapacity":       t.MaxCapacity,
		"CreationTime":      t.CreationTime,
		"ScalableTargetARN": t.ARN,
	}
	m["RoleARN"] = t.RoleARN
	if t.SuspendedState != nil {
		m["SuspendedState"] = t.SuspendedState
	}
	return m
}

func handleAppASPutScalingPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyName        string          `json:"PolicyName"`
		ServiceNamespace  string          `json:"ServiceNamespace"`
		ResourceId        string          `json:"ResourceId"`
		ScalableDimension string          `json:"ScalableDimension"`
		PolicyType        string          `json:"PolicyType"`
		TargetTracking    json.RawMessage `json:"TargetTrackingScalingPolicyConfiguration"`
		StepScaling       json.RawMessage `json:"StepScalingPolicyConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyName == "" || req.ServiceNamespace == "" || req.ResourceId == "" || req.ScalableDimension == "" {
		AWSError(w, "ValidationException",
			"PolicyName, ServiceNamespace, ResourceId, and ScalableDimension are required", http.StatusBadRequest)
		return
	}
	// A policy requires a registered scalable target.
	targetKey := appScalableTargetKey(req.ServiceNamespace, req.ResourceId, req.ScalableDimension)
	if _, ok := appScalableTargets.Get(targetKey); !ok {
		AWSErrorf(w, "ObjectNotFoundException", http.StatusBadRequest,
			"No scalable target registered for %s/%s/%s",
			req.ServiceNamespace, req.ResourceId, req.ScalableDimension)
		return
	}
	if req.PolicyType == "" {
		req.PolicyType = "StepScaling"
	}
	key := appScalingPolicyKey(req.ServiceNamespace, req.ResourceId, req.ScalableDimension, req.PolicyName)
	policy, exists := appScalingPolicies.Get(key)
	if !exists {
		policy = AppScalingPolicy{
			PolicyName:        req.PolicyName,
			PolicyARN:         appScalingPolicyARN(req.ServiceNamespace, req.ResourceId, req.PolicyName),
			ServiceNamespace:  req.ServiceNamespace,
			ResourceId:        req.ResourceId,
			ScalableDimension: req.ScalableDimension,
			CreationTime:      float64(time.Now().Unix()),
		}
	}
	policy.PolicyType = req.PolicyType
	policy.TargetTracking = req.TargetTracking
	policy.StepScaling = req.StepScaling
	appScalingPolicies.Put(key, policy)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"PolicyARN": policy.PolicyARN,
		"Alarms":    []any{},
	})
}

func handleAppASDeleteScalingPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyName        string `json:"PolicyName"`
		ServiceNamespace  string `json:"ServiceNamespace"`
		ResourceId        string `json:"ResourceId"`
		ScalableDimension string `json:"ScalableDimension"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := appScalingPolicyKey(req.ServiceNamespace, req.ResourceId, req.ScalableDimension, req.PolicyName)
	if _, ok := appScalingPolicies.Get(key); !ok {
		AWSErrorf(w, "ObjectNotFoundException", http.StatusBadRequest,
			"No scaling policy named %q for %s/%s/%s",
			req.PolicyName, req.ServiceNamespace, req.ResourceId, req.ScalableDimension)
		return
	}
	appScalingPolicies.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// appASDescribePage is the shared describe-and-paginate body for the Application
// Auto Scaling list operations (ScalingPolicies / ScheduledActions): identical
// filter (by service namespace + optional names + resource id + scalable
// dimension), name-sort, pagination, and {<respKey>, NextToken} response shape,
// differing only in store, name accessor, response key, and JSON renderer.
func appASDescribePage[T any](
	w http.ResponseWriter, store sim.Store[T], respKey, namespace, resourceID, dimension string,
	wantNames map[string]bool, maxResults *int32, nextToken string,
	nameOf, nsOf, ridOf, dimOf func(T) string, toJSON func(T) map[string]any,
) {
	matched := store.Filter(func(x T) bool {
		if nsOf(x) != namespace {
			return false
		}
		if len(wantNames) > 0 && !wantNames[nameOf(x)] {
			return false
		}
		if resourceID != "" && ridOf(x) != resourceID {
			return false
		}
		if dimension != "" && dimOf(x) != dimension {
			return false
		}
		return true
	})
	matched = sortBy(matched, nameOf)
	page, next := awsPageExplicit(matched, nextToken, awsMaxResults(maxResults))
	out := make([]map[string]any, 0, len(page))
	for _, x := range page {
		out = append(out, toJSON(x))
	}
	resp := map[string]any{respKey: out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleAppASDescribeScalingPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyNames       []string `json:"PolicyNames"`
		ServiceNamespace  string   `json:"ServiceNamespace"`
		ResourceId        string   `json:"ResourceId"`
		ScalableDimension string   `json:"ScalableDimension"`
		MaxResults        *int32   `json:"MaxResults"`
		NextToken         string   `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceNamespace == "" {
		AWSError(w, "ValidationException", "ServiceNamespace is required", http.StatusBadRequest)
		return
	}
	wantNames := map[string]bool{}
	for _, n := range req.PolicyNames {
		wantNames[n] = true
	}
	appASDescribePage(w, appScalingPolicies, "ScalingPolicies",
		req.ServiceNamespace, req.ResourceId, req.ScalableDimension, wantNames, req.MaxResults, req.NextToken,
		func(p AppScalingPolicy) string { return p.PolicyName },
		func(p AppScalingPolicy) string { return p.ServiceNamespace },
		func(p AppScalingPolicy) string { return p.ResourceId },
		func(p AppScalingPolicy) string { return p.ScalableDimension },
		scalingPolicyToJSON)
}

func scalingPolicyToJSON(p AppScalingPolicy) map[string]any {
	m := map[string]any{
		"PolicyARN":         p.PolicyARN,
		"PolicyName":        p.PolicyName,
		"ServiceNamespace":  p.ServiceNamespace,
		"ResourceId":        p.ResourceId,
		"ScalableDimension": p.ScalableDimension,
		"PolicyType":        p.PolicyType,
		"CreationTime":      p.CreationTime,
		"Alarms":            []any{},
	}
	if p.TargetTracking != nil {
		m["TargetTrackingScalingPolicyConfiguration"] = p.TargetTracking
	}
	if p.StepScaling != nil {
		m["StepScalingPolicyConfiguration"] = p.StepScaling
	}
	return m
}

// appScalableTargetByARN finds the target whose ARN matches, for the tag ops
// (which address resources by ScalableTargetARN).
func appScalableTargetByARN(arn string) (string, AppScalableTarget, bool) {
	for _, t := range appScalableTargets.List() {
		if t.ARN == arn {
			return appScalableTargetKey(t.ServiceNamespace, t.ResourceId, t.ScalableDimension), t, true
		}
	}
	return "", AppScalableTarget{}, false
}

func handleAppASListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, target, ok := appScalableTargetByARN(req.ResourceARN)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"No resource found for ARN %q", req.ResourceARN)
		return
	}
	tags := target.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}

func handleAppASTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string            `json:"ResourceARN"`
		Tags        map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key, target, ok := appScalableTargetByARN(req.ResourceARN)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"No resource found for ARN %q", req.ResourceARN)
		return
	}
	if target.Tags == nil {
		target.Tags = map[string]string{}
	}
	for k, v := range req.Tags {
		target.Tags[k] = v
	}
	appScalableTargets.Put(key, target)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleAppASUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key, target, ok := appScalableTargetByARN(req.ResourceARN)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"No resource found for ARN %q", req.ResourceARN)
		return
	}
	for _, k := range req.TagKeys {
		delete(target.Tags, k)
	}
	appScalableTargets.Put(key, target)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// appScheduledActionKey is the storage key for a scheduled action's identity
// triple (ServiceNamespace, ResourceId, ScheduledActionName). Real AWS allows
// the same action name across different resources, so ResourceId is part of
// the key.
func appScheduledActionKey(ns, resourceID, name string) string {
	return ns + "|" + resourceID + "|" + name
}

func appScheduledActionARN(ns, resourceID, name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:scheduledAction:%s:resource/%s/%s:scheduledActionName/%s",
		awsRegion(), awsAccountID(), generateUUID(), ns, resourceID, name)
}

func handleAppASPutScheduledAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceNamespace     string          `json:"ServiceNamespace"`
		Schedule             string          `json:"Schedule"`
		Timezone             string          `json:"Timezone"`
		ScheduledActionName  string          `json:"ScheduledActionName"`
		ResourceId           string          `json:"ResourceId"`
		ScalableDimension    string          `json:"ScalableDimension"`
		StartTime            *float64        `json:"StartTime"`
		EndTime              *float64        `json:"EndTime"`
		ScalableTargetAction json.RawMessage `json:"ScalableTargetAction"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceNamespace == "" || req.ScheduledActionName == "" || req.ResourceId == "" {
		AWSError(w, "ValidationException",
			"ServiceNamespace, ScheduledActionName, and ResourceId are required", http.StatusBadRequest)
		return
	}
	key := appScheduledActionKey(req.ServiceNamespace, req.ResourceId, req.ScheduledActionName)
	action, exists := appScheduledActions.Get(key)
	if !exists {
		action = AppScheduledAction{
			ScheduledActionName: req.ScheduledActionName,
			ScheduledActionARN:  appScheduledActionARN(req.ServiceNamespace, req.ResourceId, req.ScheduledActionName),
			ServiceNamespace:    req.ServiceNamespace,
			ResourceId:          req.ResourceId,
			CreationTime:        float64(time.Now().Unix()),
		}
	}
	// PutScheduledAction is upsert: omitted optional fields are left as-is
	// on update, except Schedule which the caller always re-sends.
	if req.Schedule != "" {
		action.Schedule = req.Schedule
	}
	action.Timezone = req.Timezone
	if req.ScalableDimension != "" {
		action.ScalableDimension = req.ScalableDimension
	}
	if req.StartTime != nil {
		action.StartTime = *req.StartTime
	}
	if req.EndTime != nil {
		action.EndTime = *req.EndTime
	}
	if req.ScalableTargetAction != nil {
		action.ScalableTargetAction = req.ScalableTargetAction
	}
	appScheduledActions.Put(key, action)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleAppASDeleteScheduledAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceNamespace    string `json:"ServiceNamespace"`
		ScheduledActionName string `json:"ScheduledActionName"`
		ResourceId          string `json:"ResourceId"`
		ScalableDimension   string `json:"ScalableDimension"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := appScheduledActionKey(req.ServiceNamespace, req.ResourceId, req.ScheduledActionName)
	if _, ok := appScheduledActions.Get(key); !ok {
		AWSErrorf(w, "ObjectNotFoundException", http.StatusBadRequest,
			"No scheduled action named %q for %s/%s",
			req.ScheduledActionName, req.ServiceNamespace, req.ResourceId)
		return
	}
	appScheduledActions.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleAppASDescribeScheduledActions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ScheduledActionNames []string `json:"ScheduledActionNames"`
		ServiceNamespace     string   `json:"ServiceNamespace"`
		ResourceId           string   `json:"ResourceId"`
		ScalableDimension    string   `json:"ScalableDimension"`
		MaxResults           *int32   `json:"MaxResults"`
		NextToken            string   `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceNamespace == "" {
		AWSError(w, "ValidationException", "ServiceNamespace is required", http.StatusBadRequest)
		return
	}
	wantNames := map[string]bool{}
	for _, n := range req.ScheduledActionNames {
		wantNames[n] = true
	}
	appASDescribePage(w, appScheduledActions, "ScheduledActions",
		req.ServiceNamespace, req.ResourceId, req.ScalableDimension, wantNames, req.MaxResults, req.NextToken,
		func(a AppScheduledAction) string { return a.ScheduledActionName },
		func(a AppScheduledAction) string { return a.ServiceNamespace },
		func(a AppScheduledAction) string { return a.ResourceId },
		func(a AppScheduledAction) string { return a.ScalableDimension },
		scheduledActionToJSON)
}

func scheduledActionToJSON(a AppScheduledAction) map[string]any {
	m := map[string]any{
		"ScheduledActionName": a.ScheduledActionName,
		"ScheduledActionARN":  a.ScheduledActionARN,
		"ServiceNamespace":    a.ServiceNamespace,
		"Schedule":            a.Schedule,
		"ResourceId":          a.ResourceId,
		"CreationTime":        a.CreationTime,
	}
	if a.Timezone != "" {
		m["Timezone"] = a.Timezone
	}
	if a.ScalableDimension != "" {
		m["ScalableDimension"] = a.ScalableDimension
	}
	if a.StartTime != 0 {
		m["StartTime"] = a.StartTime
	}
	if a.EndTime != 0 {
		m["EndTime"] = a.EndTime
	}
	if a.ScalableTargetAction != nil {
		m["ScalableTargetAction"] = a.ScalableTargetAction
	}
	return m
}

// handleAppASDescribeScalingActivities returns the activity log for a scalable
// target. The sim records an activity only on a real capacity change, so the
// list is empty until then — faithful, never fabricated.
func handleAppASDescribeScalingActivities(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceNamespace           string `json:"ServiceNamespace"`
		ResourceId                 string `json:"ResourceId"`
		ScalableDimension          string `json:"ScalableDimension"`
		MaxResults                 *int32 `json:"MaxResults"`
		NextToken                  string `json:"NextToken"`
		IncludeNotScaledActivities *bool  `json:"IncludeNotScaledActivities"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceNamespace == "" {
		AWSError(w, "ValidationException", "ServiceNamespace is required", http.StatusBadRequest)
		return
	}
	matched := appScalingActivities.Filter(func(a AppScalingActivity) bool {
		if a.ServiceNamespace != req.ServiceNamespace {
			return false
		}
		if req.ResourceId != "" && a.ResourceId != req.ResourceId {
			return false
		}
		if req.ScalableDimension != "" && a.ScalableDimension != req.ScalableDimension {
			return false
		}
		return true
	})
	// Most-recent-first ordering, matching real AWS.
	matched = sortBy(matched, func(a AppScalingActivity) string { return a.ActivityId })
	page, next := awsPageExplicit(matched, req.NextToken, awsMaxResults(req.MaxResults))

	out := make([]map[string]any, 0, len(page))
	for _, a := range page {
		out = append(out, scalingActivityToJSON(a))
	}
	resp := map[string]any{"ScalingActivities": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func scalingActivityToJSON(a AppScalingActivity) map[string]any {
	m := map[string]any{
		"ActivityId":        a.ActivityId,
		"ServiceNamespace":  a.ServiceNamespace,
		"ResourceId":        a.ResourceId,
		"ScalableDimension": a.ScalableDimension,
		"Description":       a.Description,
		"Cause":             a.Cause,
		"StartTime":         a.StartTime,
		"StatusCode":        a.StatusCode,
	}
	if a.EndTime != 0 {
		m["EndTime"] = a.EndTime
	}
	if a.StatusMessage != "" {
		m["StatusMessage"] = a.StatusMessage
	}
	if a.Details != "" {
		m["Details"] = a.Details
	}
	return m
}

// handleAppASGetPredictiveScalingForecast returns the load and capacity
// forecast for a predictive-scaling policy. The sim does not run a forecasting
// model, so the forecasts are empty (no timestamps/values) — faithful to a
// target with no historical data rather than fabricated curves.
func handleAppASGetPredictiveScalingForecast(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceNamespace  string   `json:"ServiceNamespace"`
		ResourceId        string   `json:"ResourceId"`
		ScalableDimension string   `json:"ScalableDimension"`
		PolicyName        string   `json:"PolicyName"`
		StartTime         *float64 `json:"StartTime"`
		EndTime           *float64 `json:"EndTime"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceNamespace == "" || req.ResourceId == "" || req.PolicyName == "" {
		AWSError(w, "ValidationException",
			"ServiceNamespace, ResourceId, and PolicyName are required", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"LoadForecast": []any{},
		"CapacityForecast": map[string]any{
			"Timestamps": []any{},
			"Values":     []any{},
		},
		"UpdateTime": float64(time.Now().Unix()),
	})
}
