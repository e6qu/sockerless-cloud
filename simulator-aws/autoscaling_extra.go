package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ASGroupExtras holds the per-group attachment / configuration state introduced
// by the extended Auto Scaling operations (attach/detach of load balancers,
// target groups, traffic sources; warm pool config; notification configs;
// suspended processes; enabled metrics; standby/protection per-instance flags).
// It is keyed by Auto Scaling group name in its own store so the core
// AutoScalingGroup shape stays the wire-faithful subset autoscaling.go emits.
type ASGroupExtras struct {
	GroupName           string
	LoadBalancerNames   []string
	TargetGroupARNs     []string
	TrafficSources      []ASTrafficSource
	SuspendedProcesses  []string
	EnabledMetrics      []string
	MetricsGranularity  string
	Notifications       []ASNotificationConfig
	HasWarmPool         bool
	WarmPoolMinSize     int
	WarmPoolMaxPrepared int
	WarmPoolState       string
	StandbyInstances    []string
	ProtectedInstances  []string
}

type ASTrafficSource struct {
	Identifier string
	Type       string
}

type ASNotificationConfig struct {
	NotificationType string
	TopicARN         string
}

// ASInstanceRefresh records an instance-refresh request. The sim settles a
// refresh Successful synchronously (single-machine, no rolling delay), the same
// way reconcileAutoScalingGroup settles scaling synchronously.
type ASInstanceRefresh struct {
	InstanceRefreshId    string
	AutoScalingGroupName string
	Status               string
	StartTime            string
	EndTime              string
	StatusReason         string
	Rollbackable         bool
}

// ASLifecycleAction tracks a pending lifecycle action created when an instance
// enters a Pending:Wait / Terminating:Wait transition gated by a lifecycle
// hook. CompleteLifecycleAction / RecordLifecycleActionHeartbeat advance it.
type ASLifecycleAction struct {
	Token                string
	AutoScalingGroupName string
	LifecycleHookName    string
	InstanceId           string
	HeartbeatTime        string
	Result               string
	Completed            bool
}

var (
	asGroupExtras       sim.Store[ASGroupExtras]
	asInstanceRefreshes sim.Store[ASInstanceRefresh]
	asLifecycleActions  sim.Store[ASLifecycleAction]
)

func registerAutoScalingExtra(r *sim.AWSQueryRouter, srv *sim.Server) {
	asGroupExtras = sim.MakeStore[ASGroupExtras](srv.DB(), "autoscaling_group_extras")
	asInstanceRefreshes = sim.MakeStore[ASInstanceRefresh](srv.DB(), "autoscaling_instance_refreshes")
	asLifecycleActions = sim.MakeStore[ASLifecycleAction](srv.DB(), "autoscaling_lifecycle_actions")

	reg := func(action string, h http.HandlerFunc) {
		r.RegisterVersioned("2011-01-01", action, h)
	}

	// Instance attach / standby / protection
	reg("AttachInstances", handleASXAttachInstances)
	reg("DetachInstances", handleASXDetachInstances)
	reg("EnterStandby", handleASXEnterStandby)
	reg("ExitStandby", handleASXExitStandby)
	reg("SetInstanceProtection", handleASXSetInstanceProtection)

	// Load balancers / target groups / traffic sources
	reg("AttachLoadBalancers", handleASXAttachLoadBalancers)
	reg("DetachLoadBalancers", handleASXDetachLoadBalancers)
	reg("DescribeLoadBalancers", handleASXDescribeLoadBalancers)
	reg("AttachLoadBalancerTargetGroups", handleASXAttachTargetGroups)
	reg("DetachLoadBalancerTargetGroups", handleASXDetachTargetGroups)
	reg("DescribeLoadBalancerTargetGroups", handleASXDescribeTargetGroups)
	reg("AttachTrafficSources", handleASXAttachTrafficSources)
	reg("DetachTrafficSources", handleASXDetachTrafficSources)
	reg("DescribeTrafficSources", handleASXDescribeTrafficSources)

	// Instance refreshes
	reg("StartInstanceRefresh", handleASXStartInstanceRefresh)
	reg("CancelInstanceRefresh", handleASXCancelInstanceRefresh)
	reg("RollbackInstanceRefresh", handleASXRollbackInstanceRefresh)
	reg("DescribeInstanceRefreshes", handleASXDescribeInstanceRefreshes)

	// Warm pools
	reg("PutWarmPool", handleASXPutWarmPool)
	reg("DeleteWarmPool", handleASXDeleteWarmPool)
	reg("DescribeWarmPool", handleASXDescribeWarmPool)

	// Notifications
	reg("PutNotificationConfiguration", handleASXPutNotificationConfiguration)
	reg("DeleteNotificationConfiguration", handleASXDeleteNotificationConfiguration)
	reg("DescribeNotificationConfigurations", handleASXDescribeNotificationConfigurations)

	// Metrics collection
	reg("EnableMetricsCollection", handleASXEnableMetricsCollection)
	reg("DisableMetricsCollection", handleASXDisableMetricsCollection)

	// Process suspension
	reg("SuspendProcesses", handleASXSuspendProcesses)
	reg("ResumeProcesses", handleASXResumeProcesses)

	// Lifecycle actions
	reg("CompleteLifecycleAction", handleASXCompleteLifecycleAction)
	reg("RecordLifecycleActionHeartbeat", handleASXRecordLifecycleActionHeartbeat)

	// Batch scheduled actions
	reg("BatchPutScheduledUpdateGroupAction", handleASXBatchPutScheduledAction)
	reg("BatchDeleteScheduledAction", handleASXBatchDeleteScheduledAction)

	// Predictive scaling + instance launch
	reg("GetPredictiveScalingForecast", handleASXGetPredictiveScalingForecast)
	reg("LaunchInstances", handleASXLaunchInstances)

	// Static description / enumeration operations
	reg("DescribeAccountLimits", handleASXDescribeAccountLimits)
	reg("DescribeAdjustmentTypes", handleASXDescribeAdjustmentTypes)
	reg("DescribeAutoScalingNotificationTypes", handleASXDescribeNotificationTypes)
	reg("DescribeLifecycleHookTypes", handleASXDescribeLifecycleHookTypes)
	reg("DescribeMetricCollectionTypes", handleASXDescribeMetricCollectionTypes)
	reg("DescribeScalingProcessTypes", handleASXDescribeScalingProcessTypes)
	reg("DescribeTerminationPolicyTypes", handleASXDescribeTerminationPolicyTypes)
}

// asxExtras returns the extras row for a group, creating an empty one if absent.
func asxExtras(group string) ASGroupExtras {
	if e, ok := asGroupExtras.Get(group); ok {
		return e
	}
	return ASGroupExtras{GroupName: group}
}

func asxRequireGroup(w http.ResponseWriter, group string) (AutoScalingGroup, bool) {
	asg, ok := autoScalingGroups.Get(group)
	if !ok {
		asError(w, "ValidationError", fmt.Sprintf("AutoScalingGroup name not found - %s", group), http.StatusBadRequest)
		return AutoScalingGroup{}, false
	}
	return asg, true
}

// asxActivity builds and stores a scaling activity, returning its XML <member>.
func asxActivity(group, description, cause string) ScalingActivity {
	now := time.Now().UTC().Format(time.RFC3339)
	a := ScalingActivity{
		ActivityId:           generateUUID(),
		AutoScalingGroupName: group,
		Description:          description,
		Cause:                cause,
		StartTime:            now,
		EndTime:              now,
		StatusCode:           "Successful",
	}
	scalingActivities.Put(a.ActivityId, a)
	return a
}

func asxActivityMemberXML(a ScalingActivity) string {
	return fmt.Sprintf(`<member><ActivityId>%s</ActivityId><AutoScalingGroupName>%s</AutoScalingGroupName><Description>%s</Description><Cause>%s</Cause><StartTime>%s</StartTime><EndTime>%s</EndTime><StatusCode>%s</StatusCode><Progress>100</Progress></member>`,
		a.ActivityId, xmlEscape(a.AutoScalingGroupName), xmlEscape(a.Description), xmlEscape(a.Cause), a.StartTime, a.EndTime, a.StatusCode)
}

// ---- Instance attach / standby / protection ----

func handleASXAttachInstances(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
		return
	}
	ids := autoscalingParamList(r, "InstanceIds.member")
	for _, id := range ids {
		if indexOfString(asg.InstanceIds, id) < 0 {
			asg.InstanceIds = append(asg.InstanceIds, id)
			if asg.DesiredCapacity < len(asg.InstanceIds) {
				asg.DesiredCapacity = len(asg.InstanceIds)
			}
			if asg.MaxSize < asg.DesiredCapacity {
				asg.MaxSize = asg.DesiredCapacity
			}
		}
	}
	autoScalingGroups.Put(group, asg)
	asxActivity(group, "Attaching EC2 instances", fmt.Sprintf("Attaching instances %s", strings.Join(ids, ",")))
	asEmptyResponse(w, "AttachInstances")
}

func handleASXDetachInstances(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
		return
	}
	decrement := r.FormValue("ShouldDecrementDesiredCapacity") == "true"
	ids := autoscalingParamList(r, "InstanceIds.member")
	var acts []ScalingActivity
	for _, id := range ids {
		if idx := indexOfString(asg.InstanceIds, id); idx >= 0 {
			asg.InstanceIds = append(asg.InstanceIds[:idx], asg.InstanceIds[idx+1:]...)
			if decrement && asg.DesiredCapacity > 0 {
				asg.DesiredCapacity--
			}
			acts = append(acts, asxActivity(group, "Detaching EC2 instance", fmt.Sprintf("Detaching instance %s", id)))
		}
	}
	autoScalingGroups.Put(group, asg)
	asResponse(w, "DetachInstances", asxActivitiesXML(acts))
}

func handleASXEnterStandby(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
		return
	}
	decrement := r.FormValue("ShouldDecrementDesiredCapacity") == "true"
	ids := autoscalingParamList(r, "InstanceIds.member")
	ex := asxExtras(group)
	var acts []ScalingActivity
	for _, id := range ids {
		if indexOfString(ex.StandbyInstances, id) < 0 {
			ex.StandbyInstances = append(ex.StandbyInstances, id)
		}
		if decrement && asg.DesiredCapacity > 0 {
			asg.DesiredCapacity--
		}
		acts = append(acts, asxActivity(group, "Moving EC2 instance to Standby", fmt.Sprintf("Moving instance %s to Standby", id)))
	}
	asGroupExtras.Put(group, ex)
	autoScalingGroups.Put(group, asg)
	asResponse(w, "EnterStandby", asxActivitiesXML(acts))
}

func handleASXExitStandby(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
		return
	}
	ids := autoscalingParamList(r, "InstanceIds.member")
	ex := asxExtras(group)
	var acts []ScalingActivity
	for _, id := range ids {
		if idx := indexOfString(ex.StandbyInstances, id); idx >= 0 {
			ex.StandbyInstances = append(ex.StandbyInstances[:idx], ex.StandbyInstances[idx+1:]...)
		}
		asg.DesiredCapacity++
		acts = append(acts, asxActivity(group, "Moving EC2 instance out of Standby", fmt.Sprintf("Moving instance %s out of Standby", id)))
	}
	if asg.MaxSize < asg.DesiredCapacity {
		asg.MaxSize = asg.DesiredCapacity
	}
	asGroupExtras.Put(group, ex)
	autoScalingGroups.Put(group, asg)
	asResponse(w, "ExitStandby", asxActivitiesXML(acts))
}

func handleASXSetInstanceProtection(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
		return
	}
	protected := r.FormValue("ProtectedFromScaleIn") == "true"
	ids := autoscalingParamList(r, "InstanceIds.member")
	ex := asxExtras(group)
	for _, id := range ids {
		if indexOfString(asg.InstanceIds, id) < 0 {
			asError(w, "ValidationError", fmt.Sprintf("Instance %s is not part of Auto Scaling group %s.", id, group), http.StatusBadRequest)
			return
		}
		idx := indexOfString(ex.ProtectedInstances, id)
		if protected && idx < 0 {
			ex.ProtectedInstances = append(ex.ProtectedInstances, id)
		} else if !protected && idx >= 0 {
			ex.ProtectedInstances = append(ex.ProtectedInstances[:idx], ex.ProtectedInstances[idx+1:]...)
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "SetInstanceProtection")
}

func asxActivitiesXML(acts []ScalingActivity) string {
	var b strings.Builder
	b.WriteString("<Activities>")
	for _, a := range acts {
		b.WriteString(asxActivityMemberXML(a))
	}
	b.WriteString("</Activities>")
	return b.String()
}

// ---- Load balancers ----

func handleASXAttachLoadBalancers(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	names := autoscalingParamList(r, "LoadBalancerNames.member")
	ex := asxExtras(group)
	for _, n := range names {
		if indexOfString(ex.LoadBalancerNames, n) < 0 {
			ex.LoadBalancerNames = append(ex.LoadBalancerNames, n)
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "AttachLoadBalancers")
}

func handleASXDetachLoadBalancers(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	names := autoscalingParamList(r, "LoadBalancerNames.member")
	ex := asxExtras(group)
	for _, n := range names {
		if idx := indexOfString(ex.LoadBalancerNames, n); idx >= 0 {
			ex.LoadBalancerNames = append(ex.LoadBalancerNames[:idx], ex.LoadBalancerNames[idx+1:]...)
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "DetachLoadBalancers")
}

func handleASXDescribeLoadBalancers(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	ex := asxExtras(group)
	var items strings.Builder
	for _, n := range ex.LoadBalancerNames {
		fmt.Fprintf(&items, "<member><LoadBalancerName>%s</LoadBalancerName><State>InService</State></member>", xmlEscape(n))
	}
	asResponse(w, "DescribeLoadBalancers", fmt.Sprintf("<LoadBalancers>%s</LoadBalancers>", items.String()))
}

func handleASXAttachTargetGroups(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	arns := autoscalingParamList(r, "TargetGroupARNs.member")
	ex := asxExtras(group)
	for _, a := range arns {
		if indexOfString(ex.TargetGroupARNs, a) < 0 {
			ex.TargetGroupARNs = append(ex.TargetGroupARNs, a)
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "AttachLoadBalancerTargetGroups")
}

func handleASXDetachTargetGroups(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	arns := autoscalingParamList(r, "TargetGroupARNs.member")
	ex := asxExtras(group)
	for _, a := range arns {
		if idx := indexOfString(ex.TargetGroupARNs, a); idx >= 0 {
			ex.TargetGroupARNs = append(ex.TargetGroupARNs[:idx], ex.TargetGroupARNs[idx+1:]...)
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "DetachLoadBalancerTargetGroups")
}

func handleASXDescribeTargetGroups(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	ex := asxExtras(group)
	var items strings.Builder
	for _, a := range ex.TargetGroupARNs {
		fmt.Fprintf(&items, "<member><LoadBalancerTargetGroupARN>%s</LoadBalancerTargetGroupARN><State>InService</State></member>", xmlEscape(a))
	}
	asResponse(w, "DescribeLoadBalancerTargetGroups", fmt.Sprintf("<LoadBalancerTargetGroups>%s</LoadBalancerTargetGroups>", items.String()))
}

func handleASXAttachTrafficSources(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	srcs := asxParseTrafficSources(r)
	ex := asxExtras(group)
	for _, s := range srcs {
		found := false
		for _, e := range ex.TrafficSources {
			if e.Identifier == s.Identifier {
				found = true
				break
			}
		}
		if !found {
			ex.TrafficSources = append(ex.TrafficSources, s)
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "AttachTrafficSources")
}

func handleASXDetachTrafficSources(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	srcs := asxParseTrafficSources(r)
	ex := asxExtras(group)
	for _, s := range srcs {
		kept := ex.TrafficSources[:0]
		for _, e := range ex.TrafficSources {
			if e.Identifier != s.Identifier {
				kept = append(kept, e)
			}
		}
		ex.TrafficSources = kept
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "DetachTrafficSources")
}

func handleASXDescribeTrafficSources(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	wantType := r.FormValue("TrafficSourceType")
	ex := asxExtras(group)
	var items strings.Builder
	for _, s := range ex.TrafficSources {
		if wantType != "" && s.Type != wantType {
			continue
		}
		fmt.Fprintf(&items, "<member><Identifier>%s</Identifier><Type>%s</Type><State>InService</State><TrafficSource>%s</TrafficSource></member>",
			xmlEscape(s.Identifier), xmlEscape(s.Type), xmlEscape(s.Identifier))
	}
	asResponse(w, "DescribeTrafficSources", fmt.Sprintf("<TrafficSources>%s</TrafficSources>", items.String()))
}

func asxParseTrafficSources(r *http.Request) []ASTrafficSource {
	var out []ASTrafficSource
	for i := 1; ; i++ {
		id := r.FormValue(fmt.Sprintf("TrafficSources.member.%d.Identifier", i))
		if id == "" {
			break
		}
		out = append(out, ASTrafficSource{
			Identifier: id,
			Type:       firstNonEmpty(r.FormValue(fmt.Sprintf("TrafficSources.member.%d.Type", i)), "elbv2"),
		})
	}
	return out
}

// ---- Instance refreshes ----

func handleASXStartInstanceRefresh(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	ref := ASInstanceRefresh{
		InstanceRefreshId:    generateUUID(),
		AutoScalingGroupName: group,
		Status:               "Successful",
		StartTime:            now,
		EndTime:              now,
		StatusReason:         "Instance refresh has completed successfully.",
		Rollbackable:         true,
	}
	asInstanceRefreshes.Put(ref.InstanceRefreshId, ref)
	asResponse(w, "StartInstanceRefresh", fmt.Sprintf("<InstanceRefreshId>%s</InstanceRefreshId>", xmlEscape(ref.InstanceRefreshId)))
}

func handleASXCancelInstanceRefresh(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	// CancelInstanceRefresh only acts on an in-progress (Pending/InProgress)
	// refresh. The sim settles every refresh Successful synchronously, so no
	// in-progress refresh ever exists to cancel — matching the real API, which
	// raises ActiveInstanceRefreshNotFound when there is none.
	var active *ASInstanceRefresh
	for _, ref := range asInstanceRefreshes.List() {
		if ref.AutoScalingGroupName != group {
			continue
		}
		if ref.Status == "Pending" || ref.Status == "InProgress" {
			c := ref
			active = &c
			break
		}
	}
	if active == nil {
		asError(w, "ActiveInstanceRefreshNotFound", fmt.Sprintf("No in-progress instance refresh found for Auto Scaling group %s.", group), http.StatusBadRequest)
		return
	}
	active.Status = "Cancelling"
	asInstanceRefreshes.Put(active.InstanceRefreshId, *active)
	asResponse(w, "CancelInstanceRefresh", fmt.Sprintf("<InstanceRefreshId>%s</InstanceRefreshId>", xmlEscape(active.InstanceRefreshId)))
}

func handleASXRollbackInstanceRefresh(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	latest := asxLatestRefresh(group)
	if latest == nil || !latest.Rollbackable {
		asError(w, "IrreversibleInstanceRefresh", fmt.Sprintf("No rollbackable instance refresh found for Auto Scaling group %s.", group), http.StatusBadRequest)
		return
	}
	latest.Status = "RollbackSuccessful"
	latest.EndTime = time.Now().UTC().Format(time.RFC3339)
	latest.StatusReason = "Instance refresh rollback completed successfully."
	asInstanceRefreshes.Put(latest.InstanceRefreshId, *latest)
	asResponse(w, "RollbackInstanceRefresh", fmt.Sprintf("<InstanceRefreshId>%s</InstanceRefreshId>", xmlEscape(latest.InstanceRefreshId)))
}

func handleASXDescribeInstanceRefreshes(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	wantIDs := autoscalingParamList(r, "InstanceRefreshIds.member")
	refs := make([]ASInstanceRefresh, 0)
	for _, ref := range asInstanceRefreshes.List() {
		if ref.AutoScalingGroupName != group {
			continue
		}
		if len(wantIDs) > 0 && !containsString(wantIDs, ref.InstanceRefreshId) {
			continue
		}
		refs = append(refs, ref)
	}
	// Sorted by creation timestamp descending, matching the real API contract.
	sort.Slice(refs, func(i, j int) bool { return refs[i].StartTime > refs[j].StartTime })
	page, next := awsPageExplicit(refs, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0))
	var items strings.Builder
	for _, ref := range page {
		fmt.Fprintf(&items, "<member><InstanceRefreshId>%s</InstanceRefreshId><AutoScalingGroupName>%s</AutoScalingGroupName><Status>%s</Status><StatusReason>%s</StatusReason><StartTime>%s</StartTime><EndTime>%s</EndTime><PercentageComplete>100</PercentageComplete><InstancesToUpdate>0</InstancesToUpdate></member>",
			xmlEscape(ref.InstanceRefreshId), xmlEscape(ref.AutoScalingGroupName), xmlEscape(ref.Status), xmlEscape(ref.StatusReason), ref.StartTime, ref.EndTime)
	}
	body := fmt.Sprintf("<InstanceRefreshes>%s</InstanceRefreshes>", items.String())
	if next != "" {
		body += "<NextToken>" + xmlEscape(next) + "</NextToken>"
	}
	asResponse(w, "DescribeInstanceRefreshes", body)
}

func asxLatestRefresh(group string) *ASInstanceRefresh {
	var latest *ASInstanceRefresh
	for _, ref := range asInstanceRefreshes.List() {
		if ref.AutoScalingGroupName != group {
			continue
		}
		if latest == nil || ref.StartTime > latest.StartTime {
			c := ref
			latest = &c
		}
	}
	return latest
}

// ---- Warm pools ----

func handleASXPutWarmPool(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	ex := asxExtras(group)
	ex.HasWarmPool = true
	ex.WarmPoolMinSize = asAtoiDefault(r.FormValue("MinSize"), 0)
	ex.WarmPoolMaxPrepared = asAtoiDefault(r.FormValue("MaxGroupPreparedCapacity"), -1)
	ex.WarmPoolState = firstNonEmpty(r.FormValue("PoolState"), "Stopped")
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "PutWarmPool")
}

func handleASXDeleteWarmPool(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	ex := asxExtras(group)
	ex.HasWarmPool = false
	ex.WarmPoolMinSize = 0
	ex.WarmPoolMaxPrepared = 0
	ex.WarmPoolState = ""
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "DeleteWarmPool")
}

func handleASXDescribeWarmPool(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	ex := asxExtras(group)
	var body strings.Builder
	if ex.HasWarmPool {
		body.WriteString("<WarmPoolConfiguration>")
		if ex.WarmPoolMaxPrepared >= 0 {
			fmt.Fprintf(&body, "<MaxGroupPreparedCapacity>%d</MaxGroupPreparedCapacity>", ex.WarmPoolMaxPrepared)
		}
		fmt.Fprintf(&body, "<MinSize>%d</MinSize>", ex.WarmPoolMinSize)
		fmt.Fprintf(&body, "<PoolState>%s</PoolState>", xmlEscape(ex.WarmPoolState))
		body.WriteString("</WarmPoolConfiguration>")
	}
	body.WriteString("<Instances/>")
	asResponse(w, "DescribeWarmPool", body.String())
}

// ---- Notifications ----

func handleASXPutNotificationConfiguration(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	topic := r.FormValue("TopicARN")
	types := autoscalingParamList(r, "NotificationTypes.member")
	ex := asxExtras(group)
	// PutNotificationConfiguration replaces the set of notification types for the
	// given topic, matching the real API (one topic, the listed types).
	kept := ex.Notifications[:0]
	for _, n := range ex.Notifications {
		if n.TopicARN != topic {
			kept = append(kept, n)
		}
	}
	ex.Notifications = kept
	for _, t := range types {
		ex.Notifications = append(ex.Notifications, ASNotificationConfig{NotificationType: t, TopicARN: topic})
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "PutNotificationConfiguration")
}

func handleASXDeleteNotificationConfiguration(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	topic := r.FormValue("TopicARN")
	ex := asxExtras(group)
	kept := ex.Notifications[:0]
	for _, n := range ex.Notifications {
		if n.TopicARN != topic {
			kept = append(kept, n)
		}
	}
	ex.Notifications = kept
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "DeleteNotificationConfiguration")
}

func handleASXDescribeNotificationConfigurations(w http.ResponseWriter, r *http.Request) {
	wantGroups := autoscalingParamList(r, "AutoScalingGroupNames.member")
	type nc struct {
		group string
		cfg   ASNotificationConfig
	}
	configs := make([]nc, 0)
	extras := asGroupExtras.List()
	sort.Slice(extras, func(i, j int) bool { return extras[i].GroupName < extras[j].GroupName })
	for _, ex := range extras {
		if len(wantGroups) > 0 && !containsString(wantGroups, ex.GroupName) {
			continue
		}
		for _, n := range ex.Notifications {
			configs = append(configs, nc{group: ex.GroupName, cfg: n})
		}
	}
	page, next := awsPageExplicit(configs, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0))
	var items strings.Builder
	for _, c := range page {
		fmt.Fprintf(&items, "<member><AutoScalingGroupName>%s</AutoScalingGroupName><NotificationType>%s</NotificationType><TopicARN>%s</TopicARN></member>",
			xmlEscape(c.group), xmlEscape(c.cfg.NotificationType), xmlEscape(c.cfg.TopicARN))
	}
	body := fmt.Sprintf("<NotificationConfigurations>%s</NotificationConfigurations>", items.String())
	if next != "" {
		body += "<NextToken>" + xmlEscape(next) + "</NextToken>"
	}
	asResponse(w, "DescribeNotificationConfigurations", body)
}

// ---- Metrics collection ----

func handleASXEnableMetricsCollection(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	ex := asxExtras(group)
	ex.MetricsGranularity = firstNonEmpty(r.FormValue("Granularity"), "1Minute")
	metrics := autoscalingParamList(r, "Metrics.member")
	if len(metrics) == 0 {
		metrics = asxAllGroupMetrics()
	}
	for _, m := range metrics {
		if indexOfString(ex.EnabledMetrics, m) < 0 {
			ex.EnabledMetrics = append(ex.EnabledMetrics, m)
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "EnableMetricsCollection")
}

func handleASXDisableMetricsCollection(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	ex := asxExtras(group)
	metrics := autoscalingParamList(r, "Metrics.member")
	if len(metrics) == 0 {
		ex.EnabledMetrics = nil
	} else {
		for _, m := range metrics {
			if idx := indexOfString(ex.EnabledMetrics, m); idx >= 0 {
				ex.EnabledMetrics = append(ex.EnabledMetrics[:idx], ex.EnabledMetrics[idx+1:]...)
			}
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "DisableMetricsCollection")
}

// ---- Process suspension ----

func handleASXSuspendProcesses(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	procs := autoscalingParamList(r, "ScalingProcesses.member")
	if len(procs) == 0 {
		procs = asxAllProcessNames()
	}
	ex := asxExtras(group)
	for _, p := range procs {
		if indexOfString(ex.SuspendedProcesses, p) < 0 {
			ex.SuspendedProcesses = append(ex.SuspendedProcesses, p)
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "SuspendProcesses")
}

func handleASXResumeProcesses(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	procs := autoscalingParamList(r, "ScalingProcesses.member")
	ex := asxExtras(group)
	if len(procs) == 0 {
		ex.SuspendedProcesses = nil
	} else {
		for _, p := range procs {
			if idx := indexOfString(ex.SuspendedProcesses, p); idx >= 0 {
				ex.SuspendedProcesses = append(ex.SuspendedProcesses[:idx], ex.SuspendedProcesses[idx+1:]...)
			}
		}
	}
	asGroupExtras.Put(group, ex)
	asEmptyResponse(w, "ResumeProcesses")
}

// ---- Lifecycle actions ----

func handleASXCompleteLifecycleAction(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	hook := r.FormValue("LifecycleHookName")
	if hook == "" {
		asError(w, "ValidationError", "LifecycleHookName is required", http.StatusBadRequest)
		return
	}
	token := r.FormValue("LifecycleActionToken")
	instanceID := r.FormValue("InstanceId")
	result := firstNonEmpty(r.FormValue("LifecycleActionResult"), "CONTINUE")
	key := asxLifecycleKey(group, hook, token, instanceID)
	action, ok := asLifecycleActions.Get(key)
	if !ok {
		action = ASLifecycleAction{
			Token:                token,
			AutoScalingGroupName: group,
			LifecycleHookName:    hook,
			InstanceId:           instanceID,
		}
	}
	action.Result = result
	action.Completed = true
	action.HeartbeatTime = time.Now().UTC().Format(time.RFC3339)
	asLifecycleActions.Put(key, action)
	asEmptyResponse(w, "CompleteLifecycleAction")
}

func handleASXRecordLifecycleActionHeartbeat(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	hook := r.FormValue("LifecycleHookName")
	if hook == "" {
		asError(w, "ValidationError", "LifecycleHookName is required", http.StatusBadRequest)
		return
	}
	token := r.FormValue("LifecycleActionToken")
	instanceID := r.FormValue("InstanceId")
	key := asxLifecycleKey(group, hook, token, instanceID)
	action, ok := asLifecycleActions.Get(key)
	if !ok {
		action = ASLifecycleAction{
			Token:                token,
			AutoScalingGroupName: group,
			LifecycleHookName:    hook,
			InstanceId:           instanceID,
		}
	}
	action.HeartbeatTime = time.Now().UTC().Format(time.RFC3339)
	asLifecycleActions.Put(key, action)
	asEmptyResponse(w, "RecordLifecycleActionHeartbeat")
}

func asxLifecycleKey(group, hook, token, instanceID string) string {
	id := token
	if id == "" {
		id = instanceID
	}
	return strings.Join([]string{group, hook, id}, "|")
}

// ---- Batch scheduled actions ----

func handleASXBatchPutScheduledAction(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	for i := 1; ; i++ {
		name := r.FormValue(fmt.Sprintf("ScheduledUpdateGroupActions.member.%d.ScheduledActionName", i))
		if name == "" {
			break
		}
		key := asResourceKey(group, name)
		action := ASScheduledAction{
			Name:                 name,
			ARN:                  scheduledActionARN(group, name),
			AutoScalingGroupName: group,
			Recurrence:           r.FormValue(fmt.Sprintf("ScheduledUpdateGroupActions.member.%d.Recurrence", i)),
			StartTime:            r.FormValue(fmt.Sprintf("ScheduledUpdateGroupActions.member.%d.StartTime", i)),
			EndTime:              r.FormValue(fmt.Sprintf("ScheduledUpdateGroupActions.member.%d.EndTime", i)),
			TimeZone:             r.FormValue(fmt.Sprintf("ScheduledUpdateGroupActions.member.%d.TimeZone", i)),
		}
		if existing, ok := asScheduledActions.Get(key); ok {
			action.ARN = existing.ARN
		}
		if v := r.FormValue(fmt.Sprintf("ScheduledUpdateGroupActions.member.%d.MinSize", i)); v != "" {
			action.MinSize = asAtoiDefault(v, 0)
			action.HasMinSize = true
		}
		if v := r.FormValue(fmt.Sprintf("ScheduledUpdateGroupActions.member.%d.MaxSize", i)); v != "" {
			action.MaxSize = asAtoiDefault(v, 0)
			action.HasMaxSize = true
		}
		if v := r.FormValue(fmt.Sprintf("ScheduledUpdateGroupActions.member.%d.DesiredCapacity", i)); v != "" {
			action.DesiredCapacity = asAtoiDefault(v, 0)
			action.HasDesiredCapacity = true
		}
		asScheduledActions.Put(key, action)
	}
	asResponse(w, "BatchPutScheduledUpdateGroupAction", "<FailedScheduledUpdateGroupActions/>")
}

func handleASXBatchDeleteScheduledAction(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	names := autoscalingParamList(r, "ScheduledActionNames.member")
	for _, n := range names {
		asScheduledActions.Delete(asResourceKey(group, n))
	}
	asResponse(w, "BatchDeleteScheduledAction", "<FailedScheduledActions/>")
}

// ---- Predictive scaling + launch ----

func handleASXGetPredictiveScalingForecast(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`<LoadForecast><member><Timestamps><member>%s</member></Timestamps><Values><member>0.0</member></Values><MetricSpecification><TargetValue>50.0</TargetValue></MetricSpecification></member></LoadForecast><CapacityForecast><Timestamps><member>%s</member></Timestamps><Values><member>0.0</member></Values></CapacityForecast><UpdateTime>%s</UpdateTime>`, now, now, now)
	asResponse(w, "GetPredictiveScalingForecast", body)
}

func handleASXLaunchInstances(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
		return
	}
	requested := asAtoiDefault(r.FormValue("RequestedCapacity"), 1)
	asg.DesiredCapacity += requested
	if asg.MaxSize < asg.DesiredCapacity {
		asg.MaxSize = asg.DesiredCapacity
	}
	if err := reconcileAutoScalingGroup(&asg, "Launching instances"); err != nil {
		asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
	autoScalingGroups.Put(group, asg)
	// LaunchInstancesResult.Instances is a list of InstanceCollection, each
	// grouping the launched instance IDs by AZ/subnet/type. The sim launches a
	// single collection (one subnet) so we emit all IDs under one member.
	var ids strings.Builder
	for _, id := range asg.InstanceIds {
		fmt.Fprintf(&ids, "<member>%s</member>", xmlEscape(id))
	}
	instances := ""
	if asg.InstanceIds != nil {
		instances = fmt.Sprintf("<member><InstanceIds>%s</InstanceIds><AvailabilityZone>%s</AvailabilityZone></member>", ids.String(), xmlEscape(awsAvailabilityZone()))
	}
	clientToken := firstNonEmpty(r.FormValue("ClientToken"), generateUUID())
	body := fmt.Sprintf("<AutoScalingGroupName>%s</AutoScalingGroupName><ClientToken>%s</ClientToken><Instances>%s</Instances><Errors/>",
		xmlEscape(group), xmlEscape(clientToken), instances)
	asResponse(w, "LaunchInstances", body)
}

// ---- Static description / enumeration operations ----

func handleASXDescribeAccountLimits(w http.ResponseWriter, _ *http.Request) {
	numGroups := len(autoScalingGroups.List())
	numLCs := len(asLaunchConfigurations.List())
	body := fmt.Sprintf("<MaxNumberOfAutoScalingGroups>500</MaxNumberOfAutoScalingGroups><MaxNumberOfLaunchConfigurations>200</MaxNumberOfLaunchConfigurations><NumberOfAutoScalingGroups>%d</NumberOfAutoScalingGroups><NumberOfLaunchConfigurations>%d</NumberOfLaunchConfigurations>",
		numGroups, numLCs)
	asResponse(w, "DescribeAccountLimits", body)
}

func handleASXDescribeAdjustmentTypes(w http.ResponseWriter, _ *http.Request) {
	var items strings.Builder
	for _, t := range []string{"ChangeInCapacity", "ExactCapacity", "PercentChangeInCapacity"} {
		fmt.Fprintf(&items, "<member><AdjustmentType>%s</AdjustmentType></member>", t)
	}
	asResponse(w, "DescribeAdjustmentTypes", fmt.Sprintf("<AdjustmentTypes>%s</AdjustmentTypes>", items.String()))
}

func handleASXDescribeNotificationTypes(w http.ResponseWriter, _ *http.Request) {
	var items strings.Builder
	for _, t := range []string{
		"autoscaling:EC2_INSTANCE_LAUNCH",
		"autoscaling:EC2_INSTANCE_LAUNCH_ERROR",
		"autoscaling:EC2_INSTANCE_TERMINATE",
		"autoscaling:EC2_INSTANCE_TERMINATE_ERROR",
		"autoscaling:TEST_NOTIFICATION",
	} {
		fmt.Fprintf(&items, "<member>%s</member>", t)
	}
	asResponse(w, "DescribeAutoScalingNotificationTypes", fmt.Sprintf("<AutoScalingNotificationTypes>%s</AutoScalingNotificationTypes>", items.String()))
}

func handleASXDescribeLifecycleHookTypes(w http.ResponseWriter, _ *http.Request) {
	var items strings.Builder
	for _, t := range []string{"autoscaling:EC2_INSTANCE_LAUNCHING", "autoscaling:EC2_INSTANCE_TERMINATING"} {
		fmt.Fprintf(&items, "<member>%s</member>", t)
	}
	asResponse(w, "DescribeLifecycleHookTypes", fmt.Sprintf("<LifecycleHookTypes>%s</LifecycleHookTypes>", items.String()))
}

func handleASXDescribeMetricCollectionTypes(w http.ResponseWriter, _ *http.Request) {
	var metrics strings.Builder
	for _, m := range asxAllGroupMetrics() {
		fmt.Fprintf(&metrics, "<member><Metric>%s</Metric></member>", m)
	}
	granularities := "<member><Granularity>1Minute</Granularity></member>"
	body := fmt.Sprintf("<Metrics>%s</Metrics><Granularities>%s</Granularities>", metrics.String(), granularities)
	asResponse(w, "DescribeMetricCollectionTypes", body)
}

func handleASXDescribeScalingProcessTypes(w http.ResponseWriter, _ *http.Request) {
	var items strings.Builder
	for _, p := range asxAllProcessNames() {
		fmt.Fprintf(&items, "<member><ProcessName>%s</ProcessName></member>", p)
	}
	asResponse(w, "DescribeScalingProcessTypes", fmt.Sprintf("<Processes>%s</Processes>", items.String()))
}

func handleASXDescribeTerminationPolicyTypes(w http.ResponseWriter, _ *http.Request) {
	var items strings.Builder
	for _, t := range []string{
		"AllocationStrategy",
		"ClosestToNextInstanceHour",
		"Default",
		"NewestInstance",
		"OldestInstance",
		"OldestLaunchConfiguration",
		"OldestLaunchTemplate",
	} {
		fmt.Fprintf(&items, "<member>%s</member>", t)
	}
	asResponse(w, "DescribeTerminationPolicyTypes", fmt.Sprintf("<TerminationPolicyTypes>%s</TerminationPolicyTypes>", items.String()))
}

func asxAllGroupMetrics() []string {
	return []string{
		"GroupMinSize",
		"GroupMaxSize",
		"GroupDesiredCapacity",
		"GroupInServiceInstances",
		"GroupPendingInstances",
		"GroupStandbyInstances",
		"GroupTerminatingInstances",
		"GroupTotalInstances",
		"GroupInServiceCapacity",
		"GroupPendingCapacity",
		"GroupStandbyCapacity",
		"GroupTerminatingCapacity",
		"GroupTotalCapacity",
		"WarmPoolDesiredCapacity",
		"WarmPoolWarmedCapacity",
		"WarmPoolPendingCapacity",
		"WarmPoolTerminatingCapacity",
		"WarmPoolTotalCapacity",
		"GroupAndWarmPoolDesiredCapacity",
		"GroupAndWarmPoolTotalCapacity",
	}
}

func asxAllProcessNames() []string {
	return []string{
		"Launch",
		"Terminate",
		"AddToLoadBalancer",
		"AlarmNotification",
		"AZRebalance",
		"HealthCheck",
		"InstanceRefresh",
		"ReplaceUnhealthy",
		"ScheduledActions",
	}
}
