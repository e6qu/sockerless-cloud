package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

type ASLaunchConfiguration struct {
	Name string
	// ARN carries the identifier AWS assigns beside the name, which is what
	// makes it the resource's own ARN rather than a restatement of the name.
	ARN                      string
	ImageId                  string
	InstanceType             string
	KeyName                  string
	AssociatePublicIPAddress bool
	EbsOptimized             bool
	InstanceMonitoring       bool
	SecurityGroups           []string
	UserData                 string
}

type AutoScalingGroup struct {
	Name                    string
	ARN                     string
	LaunchConfigurationName string
	MinSize                 int
	MaxSize                 int
	DesiredCapacity         int
	HealthCheckType         string
	HealthCheckGracePeriod  int
	VPCZoneIdentifier       string
	InstanceIds             []string
	CreatedTime             string
	Tags                    []EC2Tag
}

type ScalingActivity struct {
	ActivityId           string
	AutoScalingGroupName string
	Description          string
	Cause                string
	StartTime            string
	EndTime              string
	StatusCode           string
}

type ASScalingPolicy struct {
	Name                  string
	ARN                   string
	AutoScalingGroupName  string
	PolicyType            string
	AdjustmentType        string
	ScalingAdjustment     int
	HasScalingAdjustment  bool
	Cooldown              int
	HasCooldown           bool
	MetricAggregationType string
	Enabled               bool
}

type ASScheduledAction struct {
	Name                 string
	ARN                  string
	AutoScalingGroupName string
	MinSize              int
	HasMinSize           bool
	MaxSize              int
	HasMaxSize           bool
	DesiredCapacity      int
	HasDesiredCapacity   bool
	Recurrence           string
	StartTime            string
	EndTime              string
	TimeZone             string
}

type ASLifecycleHook struct {
	Name                  string
	AutoScalingGroupName  string
	LifecycleTransition   string
	DefaultResult         string
	HeartbeatTimeout      int
	GlobalTimeout         int
	NotificationTargetARN string
	NotificationMetadata  string
	RoleARN               string
}

var (
	asLaunchConfigurations sim.Store[ASLaunchConfiguration]
	autoScalingGroups      sim.Store[AutoScalingGroup]
	scalingActivities      sim.Store[ScalingActivity]
	asScalingPolicies      sim.Store[ASScalingPolicy]
	asScheduledActions     sim.Store[ASScheduledAction]
	asLifecycleHooks       sim.Store[ASLifecycleHook]
)

func registerAutoScaling(r *AWSQueryRouter, srv *sim.Server) {
	asLaunchConfigurations = sim.MakeStore[ASLaunchConfiguration](srv.DB(), "autoscaling_launch_configurations")
	autoScalingGroups = sim.MakeStore[AutoScalingGroup](srv.DB(), "autoscaling_groups")
	scalingActivities = sim.MakeStore[ScalingActivity](srv.DB(), "autoscaling_activities")
	asScalingPolicies = sim.MakeStore[ASScalingPolicy](srv.DB(), "autoscaling_policies")
	asScheduledActions = sim.MakeStore[ASScheduledAction](srv.DB(), "autoscaling_scheduled_actions")
	asLifecycleHooks = sim.MakeStore[ASLifecycleHook](srv.DB(), "autoscaling_lifecycle_hooks")

	r.RegisterVersioned("2011-01-01", "CreateLaunchConfiguration", handleASCreateLaunchConfiguration)
	r.RegisterVersioned("2011-01-01", "DescribeLaunchConfigurations", handleASDescribeLaunchConfigurations)
	r.RegisterVersioned("2011-01-01", "DeleteLaunchConfiguration", handleASDeleteLaunchConfiguration)
	r.RegisterVersioned("2011-01-01", "CreateAutoScalingGroup", handleASCreateAutoScalingGroup)
	r.RegisterVersioned("2011-01-01", "DescribeAutoScalingGroups", handleASDescribeAutoScalingGroups)
	r.RegisterVersioned("2011-01-01", "UpdateAutoScalingGroup", handleASUpdateAutoScalingGroup)
	r.RegisterVersioned("2011-01-01", "SetDesiredCapacity", handleASSetDesiredCapacity)
	r.RegisterVersioned("2011-01-01", "DescribeScalingActivities", handleASDescribeScalingActivities)
	r.RegisterVersioned("2011-01-01", "CreateOrUpdateTags", handleASCreateOrUpdateTags)
	r.RegisterVersioned("2011-01-01", "DeleteTags", handleASDeleteTags)
	r.RegisterVersioned("2011-01-01", "DescribeTags", handleASDescribeTags)
	r.RegisterVersioned("2011-01-01", "DeleteAutoScalingGroup", handleASDeleteAutoScalingGroup)
	r.RegisterVersioned("2011-01-01", "PutScalingPolicy", handleASPutScalingPolicy)
	r.RegisterVersioned("2011-01-01", "DescribePolicies", handleASDescribePolicies)
	r.RegisterVersioned("2011-01-01", "DeletePolicy", handleASDeletePolicy)
	r.RegisterVersioned("2011-01-01", "ExecutePolicy", handleASExecutePolicy)
	r.RegisterVersioned("2011-01-01", "PutScheduledUpdateGroupAction", handleASPutScheduledUpdateGroupAction)
	r.RegisterVersioned("2011-01-01", "DescribeScheduledActions", handleASDescribeScheduledActions)
	r.RegisterVersioned("2011-01-01", "DeleteScheduledAction", handleASDeleteScheduledAction)
	r.RegisterVersioned("2011-01-01", "PutLifecycleHook", handleASPutLifecycleHook)
	r.RegisterVersioned("2011-01-01", "DescribeLifecycleHooks", handleASDescribeLifecycleHooks)
	r.RegisterVersioned("2011-01-01", "DeleteLifecycleHook", handleASDeleteLifecycleHook)
	r.RegisterVersioned("2011-01-01", "DescribeAutoScalingInstances", handleASDescribeAutoScalingInstances)
	r.RegisterVersioned("2011-01-01", "SetInstanceHealth", handleASSetInstanceHealth)
	r.RegisterVersioned("2011-01-01", "TerminateInstanceInAutoScalingGroup", handleASTerminateInstanceInAutoScalingGroup)

	registerAutoScalingExtra(r, srv)
}

func handleASCreateLaunchConfiguration(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("LaunchConfigurationName")
	if name == "" {
		asError(w, "ValidationError", "LaunchConfigurationName is required", http.StatusBadRequest)
		return
	}
	if _, exists := asLaunchConfigurations.Get(name); exists {
		asError(w, "AlreadyExists", "LaunchConfiguration already exists", http.StatusBadRequest)
		return
	}
	lc := ASLaunchConfiguration{
		Name:                     name,
		ARN:                      launchConfigurationARN(name),
		ImageId:                  firstNonEmpty(r.FormValue("ImageId"), "ami-simulated"),
		InstanceType:             firstNonEmpty(r.FormValue("InstanceType"), "t3.micro"),
		KeyName:                  r.FormValue("KeyName"),
		AssociatePublicIPAddress: strings.EqualFold(r.FormValue("AssociatePublicIpAddress"), "true"),
		EbsOptimized:             strings.EqualFold(r.FormValue("EbsOptimized"), "true"),
		InstanceMonitoring:       !strings.EqualFold(r.FormValue("InstanceMonitoring.Enabled"), "false"),
		SecurityGroups:           autoscalingParamList(r, "SecurityGroups.member"),
		UserData:                 r.FormValue("UserData"),
	}
	asLaunchConfigurations.Put(name, lc)
	asEmptyResponse(w, "CreateLaunchConfiguration")
}

func handleASDescribeLaunchConfigurations(w http.ResponseWriter, r *http.Request) {
	names := autoscalingParamList(r, "LaunchConfigurationNames.member")
	configs := make([]ASLaunchConfiguration, 0)
	if len(names) > 0 {
		for _, name := range names {
			if lc, ok := asLaunchConfigurations.Get(name); ok {
				configs = append(configs, lc)
			}
		}
	} else {
		configs = asLaunchConfigurations.List()
	}
	var items strings.Builder
	for _, lc := range configs {
		var groups strings.Builder
		for _, group := range lc.SecurityGroups {
			fmt.Fprintf(&groups, "<member>%s</member>", xmlEscape(group))
		}
		fmt.Fprintf(&items, `<member><LaunchConfigurationName>%s</LaunchConfigurationName><LaunchConfigurationARN>%s</LaunchConfigurationARN><ImageId>%s</ImageId><InstanceType>%s</InstanceType><KeyName>%s</KeyName><AssociatePublicIpAddress>%t</AssociatePublicIpAddress><EbsOptimized>%t</EbsOptimized><InstanceMonitoring><Enabled>%t</Enabled></InstanceMonitoring><SecurityGroups>%s</SecurityGroups><BlockDeviceMappings></BlockDeviceMappings><UserData>%s</UserData></member>`,
			xmlEscape(lc.Name), xmlEscape(lc.ARN), xmlEscape(lc.ImageId), xmlEscape(lc.InstanceType), xmlEscape(lc.KeyName), lc.AssociatePublicIPAddress, lc.EbsOptimized, lc.InstanceMonitoring, groups.String(), xmlEscape(lc.UserData))
	}
	asResponse(w, "DescribeLaunchConfigurations", fmt.Sprintf("<LaunchConfigurations>%s</LaunchConfigurations>", items.String()))
}

func handleASDeleteLaunchConfiguration(w http.ResponseWriter, r *http.Request) {
	asLaunchConfigurations.Delete(r.FormValue("LaunchConfigurationName"))
	asEmptyResponse(w, "DeleteLaunchConfiguration")
}

func handleASCreateAutoScalingGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("AutoScalingGroupName")
	if name == "" {
		asError(w, "ValidationError", "AutoScalingGroupName is required", http.StatusBadRequest)
		return
	}
	if _, exists := autoScalingGroups.Get(name); exists {
		asError(w, "AlreadyExists", "AutoScalingGroup already exists", http.StatusBadRequest)
		return
	}
	minSize := asAtoiDefault(r.FormValue("MinSize"), 0)
	maxSize := asAtoiDefault(r.FormValue("MaxSize"), minSize)
	desired := asAtoiDefault(r.FormValue("DesiredCapacity"), minSize)
	if desired > maxSize {
		maxSize = desired
	}
	healthCheckType := r.FormValue("HealthCheckType")
	if healthCheckType == "" {
		healthCheckType = "EC2"
	}
	asg := AutoScalingGroup{
		Name:                    name,
		ARN:                     autoScalingGroupARN(name),
		LaunchConfigurationName: r.FormValue("LaunchConfigurationName"),
		MinSize:                 minSize,
		MaxSize:                 maxSize,
		DesiredCapacity:         desired,
		HealthCheckType:         healthCheckType,
		HealthCheckGracePeriod:  asAtoiDefault(r.FormValue("HealthCheckGracePeriod"), 0),
		VPCZoneIdentifier:       r.FormValue("VPCZoneIdentifier"),
		CreatedTime:             time.Now().UTC().Format(time.RFC3339),
		Tags:                    autoscalingTags(r),
	}
	if err := reconcileAutoScalingGroup(&asg, "Created Auto Scaling group"); err != nil {
		asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
	autoScalingGroups.Put(name, asg)
	asEmptyResponse(w, "CreateAutoScalingGroup")
}

func handleASDescribeAutoScalingGroups(w http.ResponseWriter, r *http.Request) {
	names := autoscalingParamList(r, "AutoScalingGroupNames.member")
	groups := make([]AutoScalingGroup, 0)
	if len(names) > 0 {
		for _, name := range names {
			if asg, ok := autoScalingGroups.Get(name); ok {
				groups = append(groups, asg)
			}
		}
	} else {
		groups = autoScalingGroups.List()
		sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	}
	if filters := asDescribeFilters(r); len(filters) > 0 {
		kept := groups[:0]
		for _, asg := range groups {
			if asgMatchesFilters(asg, filters) {
				kept = append(kept, asg)
			}
		}
		groups = kept
	}
	page, next := awsPageExplicit(groups, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0))
	var items strings.Builder
	for _, asg := range page {
		items.WriteString(autoScalingGroupXML(asg))
	}
	body := fmt.Sprintf("<AutoScalingGroups>%s</AutoScalingGroups>", items.String())
	if next != "" {
		body += "<NextToken>" + xmlEscape(next) + "</NextToken>"
	}
	asResponse(w, "DescribeAutoScalingGroups", body)
}

func handleASUpdateAutoScalingGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("AutoScalingGroupName")
	asg, ok := autoScalingGroups.Get(name)
	if !ok {
		asError(w, "ValidationError", "AutoScalingGroup not found", http.StatusBadRequest)
		return
	}
	if v := r.FormValue("LaunchConfigurationName"); v != "" {
		asg.LaunchConfigurationName = v
	}
	if v := r.FormValue("MinSize"); v != "" {
		asg.MinSize = asAtoiDefault(v, asg.MinSize)
	}
	if v := r.FormValue("MaxSize"); v != "" {
		asg.MaxSize = asAtoiDefault(v, asg.MaxSize)
	}
	if v := r.FormValue("DesiredCapacity"); v != "" {
		asg.DesiredCapacity = asAtoiDefault(v, asg.DesiredCapacity)
	}
	if v := r.FormValue("VPCZoneIdentifier"); v != "" {
		asg.VPCZoneIdentifier = v
	}
	if err := reconcileAutoScalingGroup(&asg, "Updated Auto Scaling group"); err != nil {
		asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
	autoScalingGroups.Put(name, asg)
	asEmptyResponse(w, "UpdateAutoScalingGroup")
}

func handleASSetDesiredCapacity(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("AutoScalingGroupName")
	asg, ok := autoScalingGroups.Get(name)
	if !ok {
		asError(w, "ValidationError", "AutoScalingGroup not found", http.StatusBadRequest)
		return
	}
	asg.DesiredCapacity = asAtoiDefault(r.FormValue("DesiredCapacity"), asg.DesiredCapacity)
	if asg.DesiredCapacity > asg.MaxSize {
		asg.MaxSize = asg.DesiredCapacity
	}
	if err := reconcileAutoScalingGroup(&asg, "Set desired capacity"); err != nil {
		asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
	autoScalingGroups.Put(name, asg)
	asEmptyResponse(w, "SetDesiredCapacity")
}

func handleASDescribeScalingActivities(w http.ResponseWriter, r *http.Request) {
	groupName := r.FormValue("AutoScalingGroupName")
	activities := make([]ScalingActivity, 0)
	for _, activity := range scalingActivities.List() {
		if groupName != "" && activity.AutoScalingGroupName != groupName {
			continue
		}
		activities = append(activities, activity)
	}
	sort.Slice(activities, func(i, j int) bool { return activities[i].ActivityId < activities[j].ActivityId })
	page, next := awsPageExplicit(activities, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0))
	var items strings.Builder
	for _, activity := range page {
		fmt.Fprintf(&items, `<member><ActivityId>%s</ActivityId><AutoScalingGroupName>%s</AutoScalingGroupName><Description>%s</Description><Cause>%s</Cause><StartTime>%s</StartTime><EndTime>%s</EndTime><StatusCode>%s</StatusCode></member>`,
			activity.ActivityId, xmlEscape(activity.AutoScalingGroupName), xmlEscape(activity.Description), xmlEscape(activity.Cause), activity.StartTime, activity.EndTime, activity.StatusCode)
	}
	body := fmt.Sprintf("<Activities>%s</Activities>", items.String())
	if next != "" {
		body += "<NextToken>" + xmlEscape(next) + "</NextToken>"
	}
	asResponse(w, "DescribeScalingActivities", body)
}

func handleASDeleteAutoScalingGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("AutoScalingGroupName")
	asg, ok := autoScalingGroups.Get(name)
	if !ok {
		asEmptyResponse(w, "DeleteAutoScalingGroup")
		return
	}
	asg.DesiredCapacity = 0
	_ = reconcileAutoScalingGroup(&asg, "Deleted Auto Scaling group")
	autoScalingGroups.Delete(name)
	asEmptyResponse(w, "DeleteAutoScalingGroup")
}

func handleASCreateOrUpdateTags(w http.ResponseWriter, r *http.Request) {
	for i := 1; ; i++ {
		resourceID := r.FormValue(fmt.Sprintf("Tags.member.%d.ResourceId", i))
		if resourceID == "" {
			break
		}
		asg, ok := autoScalingGroups.Get(resourceID)
		if !ok {
			continue
		}
		key := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
		if key == "" {
			continue
		}
		value := r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))
		found := false
		for j := range asg.Tags {
			if asg.Tags[j].Key == key {
				asg.Tags[j].Value = value
				found = true
				break
			}
		}
		if !found {
			asg.Tags = append(asg.Tags, EC2Tag{Key: key, Value: value})
		}
		autoScalingGroups.Put(asg.Name, asg)
	}
	asEmptyResponse(w, "CreateOrUpdateTags")
}

func handleASDeleteTags(w http.ResponseWriter, r *http.Request) {
	for i := 1; ; i++ {
		resourceID := r.FormValue(fmt.Sprintf("Tags.member.%d.ResourceId", i))
		if resourceID == "" {
			break
		}
		asg, ok := autoScalingGroups.Get(resourceID)
		if !ok {
			continue
		}
		key := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
		keep := asg.Tags[:0]
		for _, tag := range asg.Tags {
			if tag.Key != key {
				keep = append(keep, tag)
			}
		}
		asg.Tags = keep
		autoScalingGroups.Put(asg.Name, asg)
	}
	asEmptyResponse(w, "DeleteTags")
}

func handleASDescribeTags(w http.ResponseWriter, r *http.Request) {
	var items strings.Builder
	for _, asg := range autoScalingGroups.List() {
		for _, tag := range asg.Tags {
			fmt.Fprintf(&items, `<member><ResourceId>%s</ResourceId><ResourceType>auto-scaling-group</ResourceType><Key>%s</Key><Value>%s</Value><PropagateAtLaunch>true</PropagateAtLaunch></member>`,
				xmlEscape(asg.Name), xmlEscape(tag.Key), xmlEscape(tag.Value))
		}
	}
	asResponse(w, "DescribeTags", fmt.Sprintf("<Tags>%s</Tags>", items.String()))
}

// asResourceKey composes the storage key for per-group child resources
// (policies, scheduled actions, lifecycle hooks): the same name can exist
// under different Auto Scaling groups, so the group name is part of the key.
func asResourceKey(groupName, resourceName string) string {
	return groupName + "|" + resourceName
}

func handleASPutScalingPolicy(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	name := r.FormValue("PolicyName")
	if group == "" || name == "" {
		asError(w, "ValidationError", "AutoScalingGroupName and PolicyName are required", http.StatusBadRequest)
		return
	}
	if _, ok := autoScalingGroups.Get(group); !ok {
		asError(w, "ValidationError", fmt.Sprintf("AutoScalingGroup %q not found", group), http.StatusBadRequest)
		return
	}
	key := asResourceKey(group, name)
	policyType := firstNonEmpty(r.FormValue("PolicyType"), "SimpleScaling")
	policy := ASScalingPolicy{
		Name:                  name,
		ARN:                   scalingPolicyARN(group, name),
		AutoScalingGroupName:  group,
		PolicyType:            policyType,
		AdjustmentType:        r.FormValue("AdjustmentType"),
		MetricAggregationType: r.FormValue("MetricAggregationType"),
		Enabled:               r.FormValue("Enabled") != "false",
	}
	if existing, ok := asScalingPolicies.Get(key); ok {
		policy.ARN = existing.ARN // ARN is stable across updates
	}
	if v := r.FormValue("ScalingAdjustment"); v != "" {
		policy.ScalingAdjustment = asAtoiDefault(v, 0)
		policy.HasScalingAdjustment = true
	}
	if v := r.FormValue("Cooldown"); v != "" {
		policy.Cooldown = asAtoiDefault(v, 0)
		policy.HasCooldown = true
	}
	asScalingPolicies.Put(key, policy)
	asResponse(w, "PutScalingPolicy", fmt.Sprintf("<PolicyARN>%s</PolicyARN>", xmlEscape(policy.ARN)))
}

func handleASDescribePolicies(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	wantNames := autoscalingParamList(r, "PolicyNames.member")
	wantTypes := autoscalingParamList(r, "PolicyTypes.member")
	policies := make([]ASScalingPolicy, 0)
	for _, p := range asScalingPolicies.List() {
		if group != "" && p.AutoScalingGroupName != group {
			continue
		}
		if len(wantNames) > 0 && !asNameOrARNMatches(wantNames, p.Name, p.ARN) {
			continue
		}
		if len(wantTypes) > 0 && !containsString(wantTypes, p.PolicyType) {
			continue
		}
		policies = append(policies, p)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].ARN < policies[j].ARN })
	page, next := awsPageExplicit(policies, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0))
	var items strings.Builder
	for _, p := range page {
		items.WriteString(scalingPolicyXML(p))
	}
	body := fmt.Sprintf("<ScalingPolicies>%s</ScalingPolicies>", items.String())
	if next != "" {
		body += "<NextToken>" + xmlEscape(next) + "</NextToken>"
	}
	asResponse(w, "DescribePolicies", body)
}

func handleASDeletePolicy(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	policy := r.FormValue("PolicyName")
	for _, p := range asScalingPolicies.List() {
		if group != "" && p.AutoScalingGroupName != group {
			continue
		}
		if p.Name == policy || p.ARN == policy {
			asScalingPolicies.Delete(asResourceKey(p.AutoScalingGroupName, p.Name))
		}
	}
	asEmptyResponse(w, "DeletePolicy")
}

func handleASExecutePolicy(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	policy := r.FormValue("PolicyName")
	var found *ASScalingPolicy
	for _, p := range asScalingPolicies.List() {
		if group != "" && p.AutoScalingGroupName != group {
			continue
		}
		if p.Name == policy || p.ARN == policy {
			pc := p
			found = &pc
			break
		}
	}
	if found == nil {
		asError(w, "ValidationError", fmt.Sprintf("ScalingPolicy %q not found", policy), http.StatusBadRequest)
		return
	}
	asg, ok := autoScalingGroups.Get(found.AutoScalingGroupName)
	if !ok {
		asError(w, "ValidationError", "AutoScalingGroup not found", http.StatusBadRequest)
		return
	}
	if found.HasScalingAdjustment {
		desired := asg.DesiredCapacity
		switch found.AdjustmentType {
		case "ExactCapacity":
			desired = found.ScalingAdjustment
		case "PercentChangeInCapacity":
			desired += desired * found.ScalingAdjustment / 100
		default: // ChangeInCapacity (and unset)
			desired += found.ScalingAdjustment
		}
		if desired < asg.MinSize {
			desired = asg.MinSize
		}
		if desired > asg.MaxSize {
			desired = asg.MaxSize
		}
		asg.DesiredCapacity = desired
		if err := reconcileAutoScalingGroup(&asg, fmt.Sprintf("Executing policy %s", found.Name)); err != nil {
			asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
			return
		}
		autoScalingGroups.Put(asg.Name, asg)
	}
	asEmptyResponse(w, "ExecutePolicy")
}

func handleASPutScheduledUpdateGroupAction(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	name := r.FormValue("ScheduledActionName")
	if group == "" || name == "" {
		asError(w, "ValidationError", "AutoScalingGroupName and ScheduledActionName are required", http.StatusBadRequest)
		return
	}
	if _, ok := autoScalingGroups.Get(group); !ok {
		asError(w, "ValidationError", fmt.Sprintf("AutoScalingGroup %q not found", group), http.StatusBadRequest)
		return
	}
	key := asResourceKey(group, name)
	action := ASScheduledAction{
		Name:                 name,
		ARN:                  scheduledActionARN(group, name),
		AutoScalingGroupName: group,
		Recurrence:           r.FormValue("Recurrence"),
		StartTime:            r.FormValue("StartTime"),
		EndTime:              r.FormValue("EndTime"),
		TimeZone:             r.FormValue("TimeZone"),
	}
	if existing, ok := asScheduledActions.Get(key); ok {
		action.ARN = existing.ARN
	}
	if v := r.FormValue("MinSize"); v != "" {
		action.MinSize = asAtoiDefault(v, 0)
		action.HasMinSize = true
	}
	if v := r.FormValue("MaxSize"); v != "" {
		action.MaxSize = asAtoiDefault(v, 0)
		action.HasMaxSize = true
	}
	if v := r.FormValue("DesiredCapacity"); v != "" {
		action.DesiredCapacity = asAtoiDefault(v, 0)
		action.HasDesiredCapacity = true
	}
	asScheduledActions.Put(key, action)
	asEmptyResponse(w, "PutScheduledUpdateGroupAction")
}

func handleASDescribeScheduledActions(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	wantNames := autoscalingParamList(r, "ScheduledActionNames.member")
	actions := make([]ASScheduledAction, 0)
	for _, a := range asScheduledActions.List() {
		if group != "" && a.AutoScalingGroupName != group {
			continue
		}
		if len(wantNames) > 0 && !asNameOrARNMatches(wantNames, a.Name, a.ARN) {
			continue
		}
		actions = append(actions, a)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].ARN < actions[j].ARN })
	page, next := awsPageExplicit(actions, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0))
	var items strings.Builder
	for _, a := range page {
		items.WriteString(scheduledActionXML(a))
	}
	body := fmt.Sprintf("<ScheduledUpdateGroupActions>%s</ScheduledUpdateGroupActions>", items.String())
	if next != "" {
		body += "<NextToken>" + xmlEscape(next) + "</NextToken>"
	}
	asResponse(w, "DescribeScheduledActions", body)
}

func handleASDeleteScheduledAction(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	name := r.FormValue("ScheduledActionName")
	asScheduledActions.Delete(asResourceKey(group, name))
	asEmptyResponse(w, "DeleteScheduledAction")
}

func handleASPutLifecycleHook(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	name := r.FormValue("LifecycleHookName")
	if group == "" || name == "" {
		asError(w, "ValidationError", "AutoScalingGroupName and LifecycleHookName are required", http.StatusBadRequest)
		return
	}
	if _, ok := autoScalingGroups.Get(group); !ok {
		asError(w, "ValidationError", fmt.Sprintf("AutoScalingGroup %q not found", group), http.StatusBadRequest)
		return
	}
	hook := ASLifecycleHook{
		Name:                  name,
		AutoScalingGroupName:  group,
		LifecycleTransition:   r.FormValue("LifecycleTransition"),
		DefaultResult:         firstNonEmpty(r.FormValue("DefaultResult"), "ABANDON"),
		HeartbeatTimeout:      asAtoiDefault(r.FormValue("HeartbeatTimeout"), 3600),
		GlobalTimeout:         172800,
		NotificationTargetARN: r.FormValue("NotificationTargetARN"),
		NotificationMetadata:  r.FormValue("NotificationMetadata"),
		RoleARN:               r.FormValue("RoleARN"),
	}
	asLifecycleHooks.Put(asResourceKey(group, name), hook)
	asEmptyResponse(w, "PutLifecycleHook")
}

func handleASDescribeLifecycleHooks(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	wantNames := autoscalingParamList(r, "LifecycleHookNames.member")
	hooks := make([]ASLifecycleHook, 0)
	for _, h := range asLifecycleHooks.List() {
		if group != "" && h.AutoScalingGroupName != group {
			continue
		}
		if len(wantNames) > 0 && !containsString(wantNames, h.Name) {
			continue
		}
		hooks = append(hooks, h)
	}
	sort.Slice(hooks, func(i, j int) bool { return hooks[i].Name < hooks[j].Name })
	var items strings.Builder
	for _, h := range hooks {
		items.WriteString(lifecycleHookXML(h))
	}
	asResponse(w, "DescribeLifecycleHooks", fmt.Sprintf("<LifecycleHooks>%s</LifecycleHooks>", items.String()))
}

func handleASDeleteLifecycleHook(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	name := r.FormValue("LifecycleHookName")
	asLifecycleHooks.Delete(asResourceKey(group, name))
	asEmptyResponse(w, "DeleteLifecycleHook")
}

func handleASDescribeAutoScalingInstances(w http.ResponseWriter, r *http.Request) {
	wantIDs := autoscalingParamList(r, "InstanceIds.member")
	type asgInstance struct {
		instanceID string
		asg        AutoScalingGroup
	}
	instances := make([]asgInstance, 0)
	groups := autoScalingGroups.List()
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	for _, asg := range groups {
		for _, id := range asg.InstanceIds {
			if len(wantIDs) > 0 && !containsString(wantIDs, id) {
				continue
			}
			instances = append(instances, asgInstance{instanceID: id, asg: asg})
		}
	}
	page, next := awsPageExplicit(instances, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0))
	var items strings.Builder
	for _, ai := range page {
		items.WriteString(autoScalingInstanceXML(ai.instanceID, ai.asg))
	}
	body := fmt.Sprintf("<AutoScalingInstances>%s</AutoScalingInstances>", items.String())
	if next != "" {
		body += "<NextToken>" + xmlEscape(next) + "</NextToken>"
	}
	asResponse(w, "DescribeAutoScalingInstances", body)
}

func handleASSetInstanceHealth(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if instanceID == "" {
		asError(w, "ValidationError", "InstanceId is required", http.StatusBadRequest)
		return
	}
	// SetInstanceHealth marks an instance Healthy/Unhealthy. An Unhealthy
	// instance is replaced by the group on the next reconcile; we model that
	// by terminating it and reconciling back to DesiredCapacity.
	if r.FormValue("HealthStatus") == "Unhealthy" {
		for _, asg := range autoScalingGroups.List() {
			if idx := indexOfString(asg.InstanceIds, instanceID); idx >= 0 {
				asg.InstanceIds = append(asg.InstanceIds[:idx], asg.InstanceIds[idx+1:]...)
				terminateASGInstance(instanceID)
				if err := reconcileAutoScalingGroup(&asg, "Replacing unhealthy instance"); err != nil {
					asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
					return
				}
				autoScalingGroups.Put(asg.Name, asg)
				break
			}
		}
	}
	asEmptyResponse(w, "SetInstanceHealth")
}

func handleASTerminateInstanceInAutoScalingGroup(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if instanceID == "" {
		asError(w, "ValidationError", "InstanceId is required", http.StatusBadRequest)
		return
	}
	decrement := r.FormValue("ShouldDecrementDesiredCapacity") == "true"
	var owner *AutoScalingGroup
	for _, asg := range autoScalingGroups.List() {
		if idx := indexOfString(asg.InstanceIds, instanceID); idx >= 0 {
			asg.InstanceIds = append(asg.InstanceIds[:idx], asg.InstanceIds[idx+1:]...)
			ownerCopy := asg
			owner = &ownerCopy
			break
		}
	}
	if owner == nil {
		asError(w, "ValidationError", fmt.Sprintf("Instance %q is not part of an Auto Scaling group", instanceID), http.StatusBadRequest)
		return
	}
	terminateASGInstance(instanceID)
	if decrement && owner.DesiredCapacity > 0 {
		owner.DesiredCapacity--
	}
	cause := fmt.Sprintf("Terminating instance %s", instanceID)
	if err := reconcileAutoScalingGroup(owner, cause); err != nil {
		asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
	autoScalingGroups.Put(owner.Name, *owner)
	now := time.Now().UTC().Format(time.RFC3339)
	activity := ScalingActivity{
		ActivityId:           generateUUID(),
		AutoScalingGroupName: owner.Name,
		Description:          cause,
		Cause:                cause,
		StartTime:            now,
		EndTime:              now,
		StatusCode:           "InProgress",
	}
	scalingActivities.Put(activity.ActivityId, activity)
	asResponse(w, "TerminateInstanceInAutoScalingGroup", fmt.Sprintf("<Activity>%s</Activity>", scalingActivityInnerXML(activity)))
}

// terminateASGInstance tears down the EC2 instance backing an Auto Scaling
// group member, mirroring reconcileAutoScalingGroup's scale-in path.
func terminateASGInstance(instanceID string) {
	inst, ok := ec2Instances.Get(instanceID)
	if !ok {
		return
	}
	_ = ec2StopRealVM(context.Background(), instanceID)
	inst.State = "terminated"
	ec2Instances.Put(instanceID, inst)
	if inst.NetworkInterfaceId != "" {
		ec2NetworkInterfaces.Delete(inst.NetworkInterfaceId)
		_ = ec2DeleteRealNIC(context.Background(), inst.NetworkInterfaceId)
	}
	ec2DeleteOnTerminationVolumes(instanceID)
}

func scalingPolicyARN(group, name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:scalingPolicy:%s:autoScalingGroupName/%s:policyName/%s",
		awsRegion(), awsAccountID(), generateUUID(), group, name)
}

func scheduledActionARN(group, name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:scheduledUpdateGroupAction:%s:autoScalingGroupName/%s:scheduledActionName/%s",
		awsRegion(), awsAccountID(), generateUUID(), group, name)
}

func scalingPolicyXML(p ASScalingPolicy) string {
	var b strings.Builder
	b.WriteString("<member>")
	fmt.Fprintf(&b, "<AutoScalingGroupName>%s</AutoScalingGroupName>", xmlEscape(p.AutoScalingGroupName))
	fmt.Fprintf(&b, "<PolicyName>%s</PolicyName>", xmlEscape(p.Name))
	fmt.Fprintf(&b, "<PolicyARN>%s</PolicyARN>", xmlEscape(p.ARN))
	fmt.Fprintf(&b, "<PolicyType>%s</PolicyType>", xmlEscape(p.PolicyType))
	if p.AdjustmentType != "" {
		fmt.Fprintf(&b, "<AdjustmentType>%s</AdjustmentType>", xmlEscape(p.AdjustmentType))
	}
	if p.HasScalingAdjustment {
		fmt.Fprintf(&b, "<ScalingAdjustment>%d</ScalingAdjustment>", p.ScalingAdjustment)
	}
	if p.HasCooldown {
		fmt.Fprintf(&b, "<Cooldown>%d</Cooldown>", p.Cooldown)
	}
	if p.MetricAggregationType != "" {
		fmt.Fprintf(&b, "<MetricAggregationType>%s</MetricAggregationType>", xmlEscape(p.MetricAggregationType))
	}
	fmt.Fprintf(&b, "<Enabled>%t</Enabled>", p.Enabled)
	b.WriteString("<Alarms/>")
	b.WriteString("</member>")
	return b.String()
}

func scheduledActionXML(a ASScheduledAction) string {
	var b strings.Builder
	b.WriteString("<member>")
	fmt.Fprintf(&b, "<AutoScalingGroupName>%s</AutoScalingGroupName>", xmlEscape(a.AutoScalingGroupName))
	fmt.Fprintf(&b, "<ScheduledActionName>%s</ScheduledActionName>", xmlEscape(a.Name))
	fmt.Fprintf(&b, "<ScheduledActionARN>%s</ScheduledActionARN>", xmlEscape(a.ARN))
	if a.Recurrence != "" {
		fmt.Fprintf(&b, "<Recurrence>%s</Recurrence>", xmlEscape(a.Recurrence))
	}
	if a.StartTime != "" {
		fmt.Fprintf(&b, "<StartTime>%s</StartTime>", xmlEscape(a.StartTime))
		fmt.Fprintf(&b, "<Time>%s</Time>", xmlEscape(a.StartTime))
	}
	if a.EndTime != "" {
		fmt.Fprintf(&b, "<EndTime>%s</EndTime>", xmlEscape(a.EndTime))
	}
	if a.TimeZone != "" {
		fmt.Fprintf(&b, "<TimeZone>%s</TimeZone>", xmlEscape(a.TimeZone))
	}
	if a.HasMinSize {
		fmt.Fprintf(&b, "<MinSize>%d</MinSize>", a.MinSize)
	}
	if a.HasMaxSize {
		fmt.Fprintf(&b, "<MaxSize>%d</MaxSize>", a.MaxSize)
	}
	if a.HasDesiredCapacity {
		fmt.Fprintf(&b, "<DesiredCapacity>%d</DesiredCapacity>", a.DesiredCapacity)
	}
	b.WriteString("</member>")
	return b.String()
}

func lifecycleHookXML(h ASLifecycleHook) string {
	var b strings.Builder
	b.WriteString("<member>")
	fmt.Fprintf(&b, "<LifecycleHookName>%s</LifecycleHookName>", xmlEscape(h.Name))
	fmt.Fprintf(&b, "<AutoScalingGroupName>%s</AutoScalingGroupName>", xmlEscape(h.AutoScalingGroupName))
	if h.LifecycleTransition != "" {
		fmt.Fprintf(&b, "<LifecycleTransition>%s</LifecycleTransition>", xmlEscape(h.LifecycleTransition))
	}
	if h.NotificationTargetARN != "" {
		fmt.Fprintf(&b, "<NotificationTargetARN>%s</NotificationTargetARN>", xmlEscape(h.NotificationTargetARN))
	}
	if h.RoleARN != "" {
		fmt.Fprintf(&b, "<RoleARN>%s</RoleARN>", xmlEscape(h.RoleARN))
	}
	if h.NotificationMetadata != "" {
		fmt.Fprintf(&b, "<NotificationMetadata>%s</NotificationMetadata>", xmlEscape(h.NotificationMetadata))
	}
	fmt.Fprintf(&b, "<HeartbeatTimeout>%d</HeartbeatTimeout>", h.HeartbeatTimeout)
	fmt.Fprintf(&b, "<GlobalTimeout>%d</GlobalTimeout>", h.GlobalTimeout)
	fmt.Fprintf(&b, "<DefaultResult>%s</DefaultResult>", xmlEscape(h.DefaultResult))
	b.WriteString("</member>")
	return b.String()
}

func autoScalingInstanceXML(instanceID string, asg AutoScalingGroup) string {
	imageID := ""
	instanceType := ""
	if inst, ok := ec2Instances.Get(instanceID); ok {
		imageID = inst.ImageId
		instanceType = inst.InstanceType
	}
	var b strings.Builder
	b.WriteString("<member>")
	fmt.Fprintf(&b, "<InstanceId>%s</InstanceId>", xmlEscape(instanceID))
	fmt.Fprintf(&b, "<AutoScalingGroupName>%s</AutoScalingGroupName>", xmlEscape(asg.Name))
	fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(awsAvailabilityZone()))
	b.WriteString("<LifecycleState>InService</LifecycleState>")
	b.WriteString("<HealthStatus>HEALTHY</HealthStatus>")
	if asg.LaunchConfigurationName != "" {
		fmt.Fprintf(&b, "<LaunchConfigurationName>%s</LaunchConfigurationName>", xmlEscape(asg.LaunchConfigurationName))
	}
	if instanceType != "" {
		fmt.Fprintf(&b, "<InstanceType>%s</InstanceType>", xmlEscape(instanceType))
	}
	if imageID != "" {
		fmt.Fprintf(&b, "<ImageId>%s</ImageId>", xmlEscape(imageID))
	}
	b.WriteString("<ProtectedFromScaleIn>false</ProtectedFromScaleIn>")
	b.WriteString("</member>")
	return b.String()
}

// scalingActivityInnerXML emits the body of an <Activity> element (used by
// TerminateInstanceInAutoScalingGroup's ActivityType output).
func scalingActivityInnerXML(a ScalingActivity) string {
	return fmt.Sprintf("<ActivityId>%s</ActivityId><AutoScalingGroupName>%s</AutoScalingGroupName><Description>%s</Description><Cause>%s</Cause><StartTime>%s</StartTime><StatusCode>%s</StatusCode><Progress>0</Progress>",
		a.ActivityId, xmlEscape(a.AutoScalingGroupName), xmlEscape(a.Description), xmlEscape(a.Cause), a.StartTime, a.StatusCode)
}

func asNameOrARNMatches(wants []string, name, arn string) bool {
	for _, w := range wants {
		if w == name || w == arn {
			return true
		}
	}
	return false
}

func indexOfString(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

func reconcileAutoScalingGroup(asg *AutoScalingGroup, cause string) error {
	lc, ok := asLaunchConfigurations.Get(asg.LaunchConfigurationName)
	if !ok {
		return fmt.Errorf("LaunchConfiguration %q not found", asg.LaunchConfigurationName)
	}
	subnetID := strings.TrimSpace(strings.Split(asg.VPCZoneIdentifier, ",")[0])
	if subnetID == "" {
		// No VPCZoneIdentifier — fall back to the account's default VPC subnet
		// (a real ASG without AZ/subnet config lands in the default VPC), not a
		// hardcoded ID.
		subnetID = defaultVPCSubnetID()
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return fmt.Errorf("subnet %q not found", subnetID)
	}
	for len(asg.InstanceIds) < asg.DesiredCapacity {
		ip, err := AllocateSubnetIP(subnetID)
		if err != nil {
			return err
		}
		inst, err := ec2CreateInstance(EC2InstanceCreateSpec{
			Context:          context.Background(),
			ReservationId:    ec2ID("r"),
			ImageId:          lc.ImageId,
			InstanceType:     lc.InstanceType,
			Subnet:           subnet,
			SubnetId:         subnetID,
			PrivateIP:        ip,
			SecurityGroupIds: nil,
			Tags:             asg.Tags,
			LaunchTime:       time.Now().UTC().Format(time.RFC3339),
			KeyName:          lc.KeyName,
			State:            "pending",
		})
		if err != nil {
			return err
		}
		// Boot a real Firecracker VM only on a real-execution host; on an
		// API-only host the launched instance is modeled as "running" at the
		// control plane, like a direct RunInstances.
		if ec2RealVMHostAvailable() {
			if err := ec2StartRealVM(context.Background(), inst); err != nil {
				_ = ec2DeleteRealNIC(context.Background(), inst.NetworkInterfaceId)
				ec2Instances.Delete(inst.InstanceId)
				ec2NetworkInterfaces.Delete(inst.NetworkInterfaceId)
				ec2DeleteOnTerminationVolumes(inst.InstanceId)
				return fmt.Errorf("failed to launch EC2 instance %s for Auto Scaling group %s: %w", inst.InstanceId, asg.Name, err)
			}
		}
		inst.State = "running"
		ec2Instances.Put(inst.InstanceId, inst)
		asg.InstanceIds = append(asg.InstanceIds, inst.InstanceId)
	}
	for len(asg.InstanceIds) > asg.DesiredCapacity {
		id := asg.InstanceIds[len(asg.InstanceIds)-1]
		asg.InstanceIds = asg.InstanceIds[:len(asg.InstanceIds)-1]
		if inst, ok := ec2Instances.Get(id); ok {
			if err := ec2StopRealVM(context.Background(), id); err != nil {
				return fmt.Errorf("failed to stop EC2 instance %s for Auto Scaling group %s: %w", id, asg.Name, err)
			}
			inst.State = "terminated"
			ec2Instances.Put(id, inst)
			if inst.NetworkInterfaceId != "" {
				ec2NetworkInterfaces.Delete(inst.NetworkInterfaceId)
				if err := ec2DeleteRealNIC(context.Background(), inst.NetworkInterfaceId); err != nil {
					return fmt.Errorf("failed to delete EC2 network interface %s for Auto Scaling group %s: %w", inst.NetworkInterfaceId, asg.Name, err)
				}
			}
			ec2DeleteOnTerminationVolumes(id)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	activity := ScalingActivity{
		ActivityId:           generateUUID(),
		AutoScalingGroupName: asg.Name,
		Description:          fmt.Sprintf("%s to %d instances", cause, asg.DesiredCapacity),
		Cause:                cause,
		StartTime:            now,
		EndTime:              now,
		StatusCode:           "Successful",
	}
	scalingActivities.Put(activity.ActivityId, activity)
	return nil
}

// launchConfigurationARN builds the ARN AWS publishes for a launch
// configuration: an assigned identifier and then the name it is addressed by.
// The identifier slot held the name before, which made the ARN a restatement of
// the name rather than the resource's own — and a policy written against the
// real ARN matched nothing.
func launchConfigurationARN(name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:launchConfiguration:%s:launchConfigurationName/%s",
		awsRegion(), awsAccountID(), generateUUID(), name)
}

func autoScalingGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:autoScalingGroup:%s:autoScalingGroupName/%s",
		awsRegion(), awsAccountID(), generateUUID(), name)
}

func autoScalingGroupXML(asg AutoScalingGroup) string {
	var instances strings.Builder
	for _, id := range asg.InstanceIds {
		instances.WriteString("<member><InstanceId>")
		instances.WriteString(id)
		instances.WriteString("</InstanceId><LifecycleState>InService</LifecycleState><HealthStatus>Healthy</HealthStatus></member>")
	}
	arn := asg.ARN
	if arn == "" {
		arn = autoScalingGroupARN(asg.Name)
	}
	healthCheckType := asg.HealthCheckType
	if healthCheckType == "" {
		healthCheckType = "EC2"
	}
	return fmt.Sprintf(`<member><AutoScalingGroupName>%s</AutoScalingGroupName><AutoScalingGroupARN>%s</AutoScalingGroupARN><LaunchConfigurationName>%s</LaunchConfigurationName><MinSize>%d</MinSize><MaxSize>%d</MaxSize><DesiredCapacity>%d</DesiredCapacity><DefaultCooldown>300</DefaultCooldown><HealthCheckType>%s</HealthCheckType><HealthCheckGracePeriod>%d</HealthCheckGracePeriod><AvailabilityZones><member>%s</member></AvailabilityZones><VPCZoneIdentifier>%s</VPCZoneIdentifier><CreatedTime>%s</CreatedTime><Instances>%s</Instances><Tags>%s</Tags></member>`,
		xmlEscape(asg.Name), xmlEscape(arn), xmlEscape(asg.LaunchConfigurationName), asg.MinSize, asg.MaxSize, asg.DesiredCapacity, xmlEscape(healthCheckType), asg.HealthCheckGracePeriod, awsAvailabilityZone(), xmlEscape(asg.VPCZoneIdentifier), asg.CreatedTime, instances.String(), autoscalingTagXML(asg.Tags))
}

func autoscalingTagXML(tags []EC2Tag) string {
	var out strings.Builder
	for _, tag := range tags {
		fmt.Fprintf(&out, `<member><Key>%s</Key><Value>%s</Value><PropagateAtLaunch>true</PropagateAtLaunch></member>`, xmlEscape(tag.Key), xmlEscape(tag.Value))
	}
	return out.String()
}

func autoscalingTags(r *http.Request) []EC2Tag {
	var tags []EC2Tag
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
		if key == "" {
			break
		}
		tags = append(tags, EC2Tag{Key: key, Value: r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))})
	}
	return tags
}

func autoscalingParamList(r *http.Request, prefix string) []string {
	var out []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.%d", prefix, i))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

type asFilter struct {
	Name   string
	Values []string
}

// asDescribeFilters parses the DescribeAutoScalingGroups Filters.member.N.{Name,
// Values.member.M} query structure. Supported names match the real API:
// "tag-key", "tag-value", and "tag:<key>".
func asDescribeFilters(r *http.Request) []asFilter {
	var out []asFilter
	for i := 1; ; i++ {
		name := r.FormValue(fmt.Sprintf("Filters.member.%d.Name", i))
		if name == "" {
			break
		}
		out = append(out, asFilter{
			Name:   name,
			Values: autoscalingParamList(r, fmt.Sprintf("Filters.member.%d.Values.member", i)),
		})
	}
	return out
}

// asgMatchesFilters reports whether asg satisfies every filter (AND across
// filters; OR across a filter's values), matching real AWS filter semantics.
func asgMatchesFilters(asg AutoScalingGroup, filters []asFilter) bool {
	for _, f := range filters {
		if !asgMatchesOneFilter(asg, f) {
			return false
		}
	}
	return true
}

func asgMatchesOneFilter(asg AutoScalingGroup, f asFilter) bool {
	valueMatch := func(v string) bool {
		if len(f.Values) == 0 {
			return true
		}
		for _, want := range f.Values {
			if want == v {
				return true
			}
		}
		return false
	}
	switch {
	case f.Name == "tag-key":
		for _, t := range asg.Tags {
			if valueMatch(t.Key) {
				return true
			}
		}
		return false
	case f.Name == "tag-value":
		for _, t := range asg.Tags {
			if valueMatch(t.Value) {
				return true
			}
		}
		return false
	case strings.HasPrefix(f.Name, "tag:"):
		key := strings.TrimPrefix(f.Name, "tag:")
		for _, t := range asg.Tags {
			if t.Key == key && valueMatch(t.Value) {
				return true
			}
		}
		return false
	default:
		// Unknown filter name: real AWS rejects it, but a conservative
		// no-match keeps the sim from silently returning everything.
		return false
	}
}

func asAtoiDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func asEmptyResponse(w http.ResponseWriter, action string) {
	asResponse(w, action, "")
}

func asResponse(w http.ResponseWriter, action, body string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse xmlns="https://autoscaling.amazonaws.com/doc/2011-01-01/"><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata><%sResult>%s</%sResult></%sResponse>`,
		action, generateUUID(), action, body, action, action)
}

func asError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<ErrorResponse xmlns="https://autoscaling.amazonaws.com/doc/2011-01-01/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		code, xmlEscape(message), generateUUID())
}
