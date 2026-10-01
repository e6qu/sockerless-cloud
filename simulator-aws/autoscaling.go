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
	"github.com/e6qu/sockerless-cloud/sim/bg"
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
	LaunchTemplate          ASLaunchTemplateSpec
	// DefaultInstanceWarmup is nil until set; -1 on update clears it.
	DefaultInstanceWarmup *int
	// InstanceSources records what each member was launched from.
	InstanceSources map[string]ASLaunchSource
}

// ASLaunchTemplateSpec names a launch template and the version a group
// launches from: a version number, $Latest or $Default.
type ASLaunchTemplateSpec struct {
	LaunchTemplateId   string
	LaunchTemplateName string
	Version            string
}

func (s ASLaunchTemplateSpec) set() bool {
	return s.LaunchTemplateId != "" || s.LaunchTemplateName != ""
}

// ASLaunchSource is what an instance launches from: a launch configuration or
// a launch template. A member's source carries the version number it launched
// from; a group's carries the version as configured.
type ASLaunchSource struct {
	LaunchConfigurationName string
	LaunchTemplate          ASLaunchTemplateSpec
}

type ScalingActivity struct {
	ActivityId           string
	AutoScalingGroupName string
	Description          string
	Cause                string
	StartTime            string
	EndTime              string
	StatusCode           string
	StatusMessage        string
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
	asg.LaunchTemplate = asLaunchTemplateParam(r)
	if asg.LaunchTemplate.set() {
		if _, err := asResolveLaunchSource(asg.launchSource()); err != nil {
			asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
			return
		}
	}
	asSetDefaultInstanceWarmup(&asg, r)
	if err := reconcileAutoScalingGroup(&asg, "Created Auto Scaling group"); err != nil {
		asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
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
	page, next, pageOK := awsPage(w, asBadToken, groups, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0), 0)
	if !pageOK {
		return
	}
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
		asg.LaunchTemplate = ASLaunchTemplateSpec{}
	}
	if lt := asLaunchTemplateParam(r); lt.set() {
		if _, err := asResolveLaunchSource(ASLaunchSource{LaunchTemplate: lt}); err != nil {
			asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
			return
		}
		asg.LaunchTemplate = lt
		asg.LaunchConfigurationName = ""
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
	asSetDefaultInstanceWarmup(&asg, r)
	if err := reconcileAutoScalingGroup(&asg, "Updated Auto Scaling group"); err != nil {
		asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
	asEmptyResponse(w, "UpdateAutoScalingGroup")
}

// asLaunchTemplateParam reads a request's LaunchTemplate specification, which
// names the template by ID or name and a version selector.
func asLaunchTemplateParam(r *http.Request) ASLaunchTemplateSpec {
	return ASLaunchTemplateSpec{
		LaunchTemplateId:   r.FormValue("LaunchTemplate.LaunchTemplateId"),
		LaunchTemplateName: r.FormValue("LaunchTemplate.LaunchTemplateName"),
		Version:            r.FormValue("LaunchTemplate.Version"),
	}
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
	sort.SliceStable(activities, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, activities[i].StartTime)
		tj, _ := time.Parse(time.RFC3339, activities[j].StartTime)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return activities[i].ActivityId < activities[j].ActivityId
	})
	page, next, pageOK := awsPage(w, asBadToken, activities, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0), 0)
	if !pageOK {
		return
	}
	var items strings.Builder
	for _, activity := range page {
		items.WriteString(asActivityMemberXML(activity))
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
	asDeleteGroupChildren(name)
	asEmptyResponse(w, "DeleteAutoScalingGroup")
}

// asDeleteGroupChildren removes what AWS deletes along with a group: its
// scaling policies, scheduled actions, lifecycle hooks and actions, instance
// refreshes, and warm pool and instance settings.
func asDeleteGroupChildren(group string) {
	for _, p := range asScalingPolicies.List() {
		if p.AutoScalingGroupName == group {
			asScalingPolicies.Delete(asResourceKey(group, p.Name))
		}
	}
	for _, a := range asScheduledActions.List() {
		if a.AutoScalingGroupName == group {
			asScheduledActions.Delete(asResourceKey(group, a.Name))
		}
	}
	for _, h := range asLifecycleHooks.List() {
		if h.AutoScalingGroupName == group {
			asLifecycleHooks.Delete(asResourceKey(group, h.Name))
		}
	}
	for _, a := range asLifecycleActions.List() {
		if a.AutoScalingGroupName == group {
			asLifecycleActions.Delete(asxLifecycleKey(group, a.LifecycleHookName, a.Token, a.InstanceId))
		}
	}
	for _, ref := range asInstanceRefreshes.List() {
		if ref.AutoScalingGroupName == group {
			asInstanceRefreshes.Delete(ref.InstanceRefreshId)
		}
	}
	asGroupExtras.Delete(group)
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
	page, next, pageOK := awsPage(w, asBadToken, policies, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0), 0)
	if !pageOK {
		return
	}
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
	page, next, pageOK := awsPage(w, asBadToken, actions, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0), 0)
	if !pageOK {
		return
	}
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
	asg, ok := autoScalingGroups.Get(group)
	if !ok {
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
	if hook.DefaultResult != "CONTINUE" && hook.DefaultResult != "ABANDON" {
		asError(w, "ValidationError", "DefaultResult must be CONTINUE or ABANDON", http.StatusBadRequest)
		return
	}
	if hook.HeartbeatTimeout < 30 || hook.HeartbeatTimeout > 7200 {
		asError(w, "ValidationError", "HeartbeatTimeout must be between 30 and 7200 seconds", http.StatusBadRequest)
		return
	}
	if message := asValidateLifecycleHookTarget(asg, hook); message != "" {
		asError(w, "ValidationError", message, http.StatusBadRequest)
		return
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
	page, next, pageOK := awsPage(w, asBadToken, instances, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0), 0)
	if !pageOK {
		return
	}
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
		if indexOfString(asg.InstanceIds, instanceID) >= 0 {
			owner = &asg
			break
		}
	}
	if owner == nil {
		asError(w, "ValidationError", fmt.Sprintf("Instance %q is not part of an Auto Scaling group", instanceID), http.StatusBadRequest)
		return
	}
	cause := fmt.Sprintf("Terminating instance %s", instanceID)
	activity := asTerminateMember(owner, instanceID, cause)
	if decrement && owner.DesiredCapacity > 0 {
		owner.DesiredCapacity--
	}
	if err := reconcileAutoScalingGroup(owner, cause); err != nil {
		asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
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
		awsRegion(), awsAccountID(), sim.NewUUID(), group, name)
}

func scheduledActionARN(group, name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:scheduledUpdateGroupAction:%s:autoScalingGroupName/%s:scheduledActionName/%s",
		awsRegion(), awsAccountID(), sim.NewUUID(), group, name)
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
	fmt.Fprintf(&b, "<LifecycleState>%s</LifecycleState>", asInstanceLifecycleState(asg.Name, instanceID))
	b.WriteString("<HealthStatus>HEALTHY</HealthStatus>")
	b.WriteString(asLaunchSourceXML(asMemberLaunchSource(asg, instanceID)))
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

// reconcileAutoScalingGroup brings the group's membership to its desired
// capacity and stores it. Amazon EC2 Auto Scaling answers the request that
// changed the capacity at once: each launched instance joins the group Pending
// and its scaling activity stays InProgress until the instance is running,
// which happens behind the request in asLaunchInstance.
func reconcileAutoScalingGroup(asg *AutoScalingGroup, cause string) error {
	var launches []asLaunch
	if missing := asg.DesiredCapacity - len(asg.InstanceIds); missing > 0 {
		var err error
		launches, err = asLaunchMembers(asg, asGroupLaunchSource(*asg), missing, cause)
		if err != nil {
			return err
		}
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
			asFinishActivity(asNewActivity(asg.Name, "Terminating EC2 instance: "+id, cause, "InProgress").ActivityId, "Successful", "")
		}
	}
	for id := range asg.InstanceSources {
		if indexOfString(asg.InstanceIds, id) < 0 {
			delete(asg.InstanceSources, id)
		}
	}
	autoScalingGroups.Put(asg.Name, *asg)
	asStartLaunches(asg.Name, launches)
	return nil
}

// asLaunchMembers creates n Pending members from src and a launch activity for
// each. The caller stores the group and then boots them with asStartLaunches.
func asLaunchMembers(asg *AutoScalingGroup, src ASLaunchSource, n int, cause string) ([]asLaunch, error) {
	spec, err := asResolveLaunchSource(src)
	if err != nil {
		return nil, err
	}
	subnetID := strings.TrimSpace(strings.Split(asg.VPCZoneIdentifier, ",")[0])
	if subnetID == "" {
		// A group without VPCZoneIdentifier lands in the default VPC, as it
		// does on AWS.
		subnetID = defaultVPCSubnetID()
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return nil, fmt.Errorf("subnet %q not found", subnetID)
	}
	var launches []asLaunch
	for range n {
		ip, err := AllocateSubnetIP(subnetID)
		if err != nil {
			return launches, err
		}
		inst, err := ec2CreateInstance(EC2InstanceCreateSpec{
			Context:       context.Background(),
			ReservationId: ec2ID("r"),
			ImageId:       spec.imageID,
			InstanceType:  spec.instanceType,
			Subnet:        subnet,
			SubnetId:      subnetID,
			PrivateIP:     ip,
			Tags:          asg.Tags,
			LaunchTime:    time.Now().UTC().Format(time.RFC3339),
			KeyName:       spec.keyName,
			State:         "pending",
		})
		if err != nil {
			return launches, err
		}
		asg.InstanceIds = append(asg.InstanceIds, inst.InstanceId)
		if asg.InstanceSources == nil {
			asg.InstanceSources = map[string]ASLaunchSource{}
		}
		asg.InstanceSources[inst.InstanceId] = spec.source
		activity := asNewActivity(asg.Name, "Launching a new EC2 instance: "+inst.InstanceId, cause, "InProgress")
		launches = append(launches, asLaunch{instanceID: inst.InstanceId, activityID: activity.ActivityId})
	}
	return launches, nil
}

func asStartLaunches(group string, launches []asLaunch) {
	for _, l := range launches {
		bg.Go(func() { asLaunchInstance(group, l) })
	}
}

// asTerminateMember takes an instance out of its group and terminates it,
// recording the termination activity.
func asTerminateMember(asg *AutoScalingGroup, instanceID, cause string) ScalingActivity {
	if idx := indexOfString(asg.InstanceIds, instanceID); idx >= 0 {
		asg.InstanceIds = append(asg.InstanceIds[:idx], asg.InstanceIds[idx+1:]...)
	}
	delete(asg.InstanceSources, instanceID)
	if ex, ok := asGroupExtras.Get(asg.Name); ok {
		if idx := indexOfString(ex.StandbyInstances, instanceID); idx >= 0 {
			ex.StandbyInstances = append(ex.StandbyInstances[:idx], ex.StandbyInstances[idx+1:]...)
		}
		if idx := indexOfString(ex.ProtectedInstances, instanceID); idx >= 0 {
			ex.ProtectedInstances = append(ex.ProtectedInstances[:idx], ex.ProtectedInstances[idx+1:]...)
		}
		asGroupExtras.Put(asg.Name, ex)
	}
	terminateASGInstance(instanceID)
	activity := asNewActivity(asg.Name, "Terminating EC2 instance: "+instanceID, cause, "InProgress")
	asFinishActivity(activity.ActivityId, "Successful", "")
	return activity
}

// asMessage is an error whose text the service returns verbatim, as Amazon EC2
// words it.
type asMessage string

func (m asMessage) Error() string { return string(m) }

type asLaunchSpec struct {
	imageID      string
	instanceType string
	keyName      string
	source       ASLaunchSource
}

// asResolveLaunchSource reads what src launches: a launch configuration, or
// the launch template version its version selector picks now.
func asResolveLaunchSource(src ASLaunchSource) (asLaunchSpec, error) {
	if !src.LaunchTemplate.set() {
		lc, ok := asLaunchConfigurations.Get(src.LaunchConfigurationName)
		if !ok {
			return asLaunchSpec{}, fmt.Errorf("LaunchConfiguration %q not found", src.LaunchConfigurationName)
		}
		return asLaunchSpec{imageID: lc.ImageId, instanceType: lc.InstanceType, keyName: lc.KeyName, source: src}, nil
	}
	spec := src.LaunchTemplate
	lt, ok := lookupLaunchTemplate(spec.LaunchTemplateId, spec.LaunchTemplateName)
	if !ok {
		return asLaunchSpec{}, asMessage(fmt.Sprintf("Launch template %s does not exist.", firstNonEmpty(spec.LaunchTemplateId, spec.LaunchTemplateName)))
	}
	number := lt.DefaultVersionNumber
	switch spec.Version {
	case "", "$Default":
	case "$Latest":
		number = lt.LatestVersionNumber
	default:
		n, err := strconv.ParseInt(spec.Version, 10, 64)
		if err != nil || !ltHasVersion(lt, n) {
			return asLaunchSpec{}, asMessage(fmt.Sprintf("Launch template version %s does not exist.", spec.Version))
		}
		number = n
	}
	for _, v := range lt.Versions {
		if v.VersionNumber != number {
			continue
		}
		return asLaunchSpec{
			imageID:      v.Data.ImageId,
			instanceType: v.Data.InstanceType,
			keyName:      v.Data.KeyName,
			source: ASLaunchSource{LaunchTemplate: ASLaunchTemplateSpec{
				LaunchTemplateId:   lt.LaunchTemplateId,
				LaunchTemplateName: lt.LaunchTemplateName,
				Version:            strconv.FormatInt(number, 10),
			}},
		}, nil
	}
	return asLaunchSpec{}, asMessage(fmt.Sprintf("Launch template version %d does not exist.", number))
}

func (asg AutoScalingGroup) launchSource() ASLaunchSource {
	if asg.LaunchTemplate.set() {
		return ASLaunchSource{LaunchTemplate: asg.LaunchTemplate}
	}
	return ASLaunchSource{LaunchConfigurationName: asg.LaunchConfigurationName}
}

// asGroupLaunchSource is what the group launches from now: the desired
// configuration of an instance refresh under way, which is what a scale-out
// during the refresh launches, else the group's own.
func asGroupLaunchSource(asg AutoScalingGroup) ASLaunchSource {
	if ref := asActiveRefresh(asg.Name); ref != nil && ref.Desired != nil && ref.Rollback == nil {
		return *ref.Desired
	}
	return asg.launchSource()
}

// asMemberLaunchSource is what a member launched from; a member that predates
// the record launched from the group's own configuration.
func asMemberLaunchSource(asg AutoScalingGroup, instanceID string) ASLaunchSource {
	if src, ok := asg.InstanceSources[instanceID]; ok {
		return src
	}
	return asg.launchSource()
}

func asLaunchSourceXML(src ASLaunchSource) string {
	if !src.LaunchTemplate.set() {
		if src.LaunchConfigurationName == "" {
			return ""
		}
		return fmt.Sprintf("<LaunchConfigurationName>%s</LaunchConfigurationName>", xmlEscape(src.LaunchConfigurationName))
	}
	return "<LaunchTemplate>" + asLaunchTemplateSpecXML(src.LaunchTemplate) + "</LaunchTemplate>"
}

func asLaunchTemplateSpecXML(spec ASLaunchTemplateSpec) string {
	var b strings.Builder
	if spec.LaunchTemplateId != "" {
		fmt.Fprintf(&b, "<LaunchTemplateId>%s</LaunchTemplateId>", xmlEscape(spec.LaunchTemplateId))
	}
	if spec.LaunchTemplateName != "" {
		fmt.Fprintf(&b, "<LaunchTemplateName>%s</LaunchTemplateName>", xmlEscape(spec.LaunchTemplateName))
	}
	if spec.Version != "" {
		fmt.Fprintf(&b, "<Version>%s</Version>", xmlEscape(spec.Version))
	}
	return b.String()
}

// asSetDefaultInstanceWarmup applies DefaultInstanceWarmup from a create or
// update request; -1 removes a value set before.
func asSetDefaultInstanceWarmup(asg *AutoScalingGroup, r *http.Request) {
	raw := r.FormValue("DefaultInstanceWarmup")
	if raw == "" {
		return
	}
	v := asAtoiDefault(raw, -1)
	if v < 0 {
		asg.DefaultInstanceWarmup = nil
		return
	}
	asg.DefaultInstanceWarmup = &v
}

type asLaunch struct {
	instanceID string
	activityID string
}

// asLaunchInstance boots a Pending group member. The instance turns running,
// and so InService, only once its VM is up; a failed boot fails the launch
// activity and takes the instance out of the group.
func asLaunchInstance(group string, l asLaunch) {
	if _, ok := ec2Instances.Get(l.instanceID); !ok {
		const reason = "The instance was deleted before it launched."
		asFinishActivity(l.activityID, "Cancelled", reason)
		asRefreshInstanceSettled(group, l.instanceID, reason)
		return
	}
	// The launch activity ends once EC2 has launched the instance, so it is
	// Successful by the time a caller sees the instance running. A launch
	// hook holds it at MidLifecycleAction until every hook has answered.
	hooks := asHooksFor(group, asLaunchingTransition)
	launched, err := ec2LaunchInstance(l.instanceID, func() {
		if len(hooks) > 0 {
			scalingActivities.Update(l.activityID, func(a *ScalingActivity) { a.StatusCode = "MidLifecycleAction" })
			return
		}
		asFinishActivity(l.activityID, "Successful", "")
	})
	switch {
	case err != nil:
		autoScalingGroups.Update(group, func(asg *AutoScalingGroup) {
			if idx := indexOfString(asg.InstanceIds, l.instanceID); idx >= 0 {
				asg.InstanceIds = append(asg.InstanceIds[:idx], asg.InstanceIds[idx+1:]...)
			}
		})
		reason := fmt.Sprintf("Instance %s failed to launch: %v", l.instanceID, err)
		asFinishActivity(l.activityID, "Failed", reason)
		asRefreshInstanceSettled(group, l.instanceID, reason)
	case !launched:
		const reason = "The instance was terminated before it entered service."
		asFinishActivity(l.activityID, "Cancelled", reason)
		asRefreshInstanceSettled(group, l.instanceID, reason)
	case len(hooks) > 0:
		asBeginLaunchLifecycleActions(group, l.instanceID, l.activityID, hooks)
	default:
		asRefreshInstanceSettled(group, l.instanceID, "")
	}
}

// asActivityMemberXML renders an Activity; EndTime and StatusMessage appear
// once the activity has them, and Progress reaches 100 when it ends.
func asActivityMemberXML(a ScalingActivity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<member><ActivityId>%s</ActivityId><AutoScalingGroupName>%s</AutoScalingGroupName><Description>%s</Description><Cause>%s</Cause><StartTime>%s</StartTime>",
		a.ActivityId, xmlEscape(a.AutoScalingGroupName), xmlEscape(a.Description), xmlEscape(a.Cause), a.StartTime)
	progress := 50
	if a.EndTime != "" {
		fmt.Fprintf(&b, "<EndTime>%s</EndTime>", a.EndTime)
		progress = 100
	}
	fmt.Fprintf(&b, "<StatusCode>%s</StatusCode>", a.StatusCode)
	if a.StatusMessage != "" {
		fmt.Fprintf(&b, "<StatusMessage>%s</StatusMessage>", xmlEscape(a.StatusMessage))
	}
	fmt.Fprintf(&b, "<Progress>%d</Progress></member>", progress)
	return b.String()
}

// asActivityTimeLayout carries milliseconds so activities started within one
// second still order newest first, as DescribeScalingActivities returns them.
const asActivityTimeLayout = "2006-01-02T15:04:05.000Z"

func asNewActivity(group, description, cause, status string) ScalingActivity {
	a := ScalingActivity{
		ActivityId:           sim.NewUUID(),
		AutoScalingGroupName: group,
		Description:          description,
		Cause:                cause,
		StartTime:            time.Now().UTC().Format(asActivityTimeLayout),
		StatusCode:           status,
	}
	scalingActivities.Put(a.ActivityId, a)
	return a
}

func asFinishActivity(activityID, status, message string) {
	scalingActivities.Update(activityID, func(a *ScalingActivity) {
		a.StatusCode = status
		a.StatusMessage = message
		a.EndTime = time.Now().UTC().Format(asActivityTimeLayout)
	})
}

// asInstanceLifecycleState reports a group member's lifecycle state from the
// EC2 instance behind it: Pending until the instance runs, then InService.
func asInstanceLifecycleState(group, instanceID string) string {
	if ex, ok := asGroupExtras.Get(group); ok && indexOfString(ex.StandbyInstances, instanceID) >= 0 {
		return "Standby"
	}
	inst, ok := ec2Instances.Get(instanceID)
	if !ok {
		return "InService"
	}
	if inst.State == "running" && asInstanceAwaitsLifecycleAction(group, instanceID) {
		return "Pending:Wait"
	}
	switch inst.State {
	case "pending":
		return "Pending"
	case "shutting-down":
		return "Terminating"
	case "terminated":
		return "Terminated"
	}
	return "InService"
}

// launchConfigurationARN builds the ARN AWS publishes for a launch
// configuration: an assigned identifier and then the name it is addressed by.
// The identifier slot held the name before, which made the ARN a restatement of
// the name rather than the resource's own — and a policy written against the
// real ARN matched nothing.
func launchConfigurationARN(name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:launchConfiguration:%s:launchConfigurationName/%s",
		awsRegion(), awsAccountID(), sim.NewUUID(), name)
}

func autoScalingGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:autoscaling:%s:%s:autoScalingGroup:%s:autoScalingGroupName/%s",
		awsRegion(), awsAccountID(), sim.NewUUID(), name)
}

func autoScalingGroupXML(asg AutoScalingGroup) string {
	var instances strings.Builder
	for _, id := range asg.InstanceIds {
		fmt.Fprintf(&instances, "<member><InstanceId>%s</InstanceId><LifecycleState>%s</LifecycleState><HealthStatus>Healthy</HealthStatus>%s</member>",
			id, asInstanceLifecycleState(asg.Name, id), asLaunchSourceXML(asMemberLaunchSource(asg, id)))
	}
	arn := asg.ARN
	if arn == "" {
		arn = autoScalingGroupARN(asg.Name)
	}
	healthCheckType := asg.HealthCheckType
	if healthCheckType == "" {
		healthCheckType = "EC2"
	}
	warmup := ""
	if asg.DefaultInstanceWarmup != nil {
		warmup = fmt.Sprintf("<DefaultInstanceWarmup>%d</DefaultInstanceWarmup>", *asg.DefaultInstanceWarmup)
	}
	return fmt.Sprintf(`<member><AutoScalingGroupName>%s</AutoScalingGroupName><AutoScalingGroupARN>%s</AutoScalingGroupARN>%s<MinSize>%d</MinSize><MaxSize>%d</MaxSize><DesiredCapacity>%d</DesiredCapacity><DefaultCooldown>300</DefaultCooldown><HealthCheckType>%s</HealthCheckType><HealthCheckGracePeriod>%d</HealthCheckGracePeriod><AvailabilityZones><member>%s</member></AvailabilityZones><VPCZoneIdentifier>%s</VPCZoneIdentifier><CreatedTime>%s</CreatedTime><Instances>%s</Instances><Tags>%s</Tags>%s</member>`,
		xmlEscape(asg.Name), xmlEscape(arn), asLaunchSourceXML(asg.launchSource()), asg.MinSize, asg.MaxSize, asg.DesiredCapacity, xmlEscape(healthCheckType), asg.HealthCheckGracePeriod, awsAvailabilityZone(), xmlEscape(asg.VPCZoneIdentifier), asg.CreatedTime, instances.String(), autoscalingTagXML(asg.Tags), warmup)
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
		action, sim.NewUUID(), action, body, action, action)
}

func asError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<ErrorResponse xmlns="https://autoscaling.amazonaws.com/doc/2011-01-01/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		code, xmlEscape(message), sim.NewUUID())
}
