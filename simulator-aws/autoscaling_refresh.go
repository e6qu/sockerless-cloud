package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

const (
	asRefreshPending            = "Pending"
	asRefreshInProgress         = "InProgress"
	asRefreshSuccessful         = "Successful"
	asRefreshFailed             = "Failed"
	asRefreshCancelling         = "Cancelling"
	asRefreshCancelled          = "Cancelled"
	asRefreshRollbackInProgress = "RollbackInProgress"
	asRefreshRollbackFailed     = "RollbackFailed"
	asRefreshRollbackSuccessful = "RollbackSuccessful"
	asRefreshBaking             = "Baking"

	asRefreshTimerWarmup     = "warmup"
	asRefreshTimerCheckpoint = "checkpoint"
	asRefreshTimerBake       = "bake"
	asRefreshTimerWait       = "wait"

	asRefreshDefaultMinHealthy       = 90
	asRefreshDefaultCheckpointDelay  = 3600
	asRefreshStandbyProtectedTimeout = time.Hour
)

// ASRefreshPreferences holds an instance refresh's RefreshPreferences; a nil
// pointer is a preference the request left out.
type ASRefreshPreferences struct {
	MinHealthyPercentage      int
	MaxHealthyPercentage      *int
	InstanceWarmup            *int
	SkipMatching              bool
	AutoRollback              bool
	ScaleInProtectedInstances string
	StandbyInstances          string
	CheckpointPercentages     []int
	CheckpointDelay           *int
	BakeTime                  *int
}

type ASRefreshRollback struct {
	// Reason carries the failure that started an automatic rollback.
	Reason                       string
	StartTime                    string
	PercentageCompleteOnRollback int
	InstancesToUpdateOnRollback  int
}

// ASInstanceRefresh is an instance refresh and the progress of its rolling
// replacement. Forward, it replaces ToReplace with instances launched from
// Desired (or the group's configuration); rolling back, it replaces the
// instances it launched with ones from Previous.
type ASInstanceRefresh struct {
	InstanceRefreshId    string
	AutoScalingGroupName string
	Status               string
	StatusReason         string
	StartTime            string
	EndTime              string
	Strategy             string
	Preferences          ASRefreshPreferences
	Desired              *ASLaunchSource
	Previous             ASLaunchSource
	Total                int
	Updated              int
	ToReplace            []string
	Launched             []string
	Batch                []string
	InFlight             []string
	Deferred             []string
	NextCheckpoint       int
	Baked                bool
	TimerKind            string
	TimerUntil           string
	Rollback             *ASRefreshRollback
}

var (
	// asRefreshMu serializes every change to a refresh, whether a request, a
	// launch settling or a timer firing drives it.
	asRefreshMu sync.Mutex
	// asRefreshStored hears every change stored to a refresh.
	asRefreshStored = func(ASInstanceRefresh) {}
)

func asRefreshActive(status string) bool {
	switch status {
	case asRefreshPending, asRefreshInProgress, asRefreshCancelling, asRefreshRollbackInProgress, asRefreshBaking:
		return true
	}
	return false
}

func asActiveRefresh(group string) *ASInstanceRefresh {
	for _, ref := range asInstanceRefreshes.List() {
		if ref.AutoScalingGroupName == group && asRefreshActive(ref.Status) {
			return &ref
		}
	}
	return nil
}

func handleASXStartInstanceRefresh(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
		return
	}
	asRefreshMu.Lock()
	defer asRefreshMu.Unlock()
	if asActiveRefresh(group) != nil {
		asError(w, "InstanceRefreshInProgress", "The request failed because an active instance refresh already exists for the specified Auto Scaling group.", http.StatusBadRequest)
		return
	}
	prefs, msg := asParseRefreshPreferences(r)
	if msg != "" {
		asError(w, "ValidationError", msg, http.StatusBadRequest)
		return
	}
	var desired *ASLaunchSource
	if id, name := r.FormValue("DesiredConfiguration.LaunchTemplate.LaunchTemplateId"), r.FormValue("DesiredConfiguration.LaunchTemplate.LaunchTemplateName"); id != "" || name != "" {
		desired = &ASLaunchSource{LaunchTemplate: ASLaunchTemplateSpec{
			LaunchTemplateId:   id,
			LaunchTemplateName: name,
			Version:            r.FormValue("DesiredConfiguration.LaunchTemplate.Version"),
		}}
		if _, err := asResolveLaunchSource(*desired); err != nil {
			asError(w, "ValidationError", err.Error(), http.StatusBadRequest)
			return
		}
	}
	previous := asg.launchSource()
	if prefs.AutoRollback && !asRefreshReversible(desired, previous) {
		asIrreversibleRefresh(w)
		return
	}
	target := previous
	if desired != nil {
		target = *desired
	}
	if prefs.SkipMatching && !target.LaunchTemplate.set() {
		asError(w, "ValidationError", "Using skip matching with a launch configuration is not supported.", http.StatusBadRequest)
		return
	}
	toReplace := asRefreshCandidates(asg, prefs, target)
	ref := ASInstanceRefresh{
		InstanceRefreshId:    sim.NewUUID(),
		AutoScalingGroupName: group,
		Status:               asRefreshPending,
		StartTime:            time.Now().UTC().Format(asActivityTimeLayout),
		Strategy:             firstNonEmpty(r.FormValue("Strategy"), "Rolling"),
		Preferences:          prefs,
		Desired:              desired,
		Previous:             previous,
		Total:                len(toReplace),
		ToReplace:            toReplace,
	}
	asInstanceRefreshes.Put(ref.InstanceRefreshId, ref)
	asRefreshStored(ref)
	id := ref.InstanceRefreshId
	bg.Go(func() { asRefreshAdvance(id) })
	asResponse(w, "StartInstanceRefresh", fmt.Sprintf("<InstanceRefreshId>%s</InstanceRefreshId>", xmlEscape(id)))
}

func asParseRefreshPreferences(r *http.Request) (ASRefreshPreferences, string) {
	optional := func(name string) *int {
		raw := r.FormValue("Preferences." + name)
		if raw == "" {
			return nil
		}
		v := asAtoiDefault(raw, 0)
		return &v
	}
	prefs := ASRefreshPreferences{
		MinHealthyPercentage:      asRefreshDefaultMinHealthy,
		MaxHealthyPercentage:      optional("MaxHealthyPercentage"),
		InstanceWarmup:            optional("InstanceWarmup"),
		SkipMatching:              r.FormValue("Preferences.SkipMatching") == "true",
		AutoRollback:              r.FormValue("Preferences.AutoRollback") == "true",
		ScaleInProtectedInstances: firstNonEmpty(r.FormValue("Preferences.ScaleInProtectedInstances"), "Wait"),
		StandbyInstances:          firstNonEmpty(r.FormValue("Preferences.StandbyInstances"), "Wait"),
		CheckpointDelay:           optional("CheckpointDelay"),
		BakeTime:                  optional("BakeTime"),
	}
	minHealthy := optional("MinHealthyPercentage")
	if minHealthy != nil {
		prefs.MinHealthyPercentage = *minHealthy
	}
	for _, raw := range autoscalingParamList(r, "Preferences.CheckpointPercentages.member") {
		prefs.CheckpointPercentages = append(prefs.CheckpointPercentages, asAtoiDefault(raw, 0))
	}
	if maxHealthy := prefs.MaxHealthyPercentage; maxHealthy != nil && (minHealthy == nil || *maxHealthy-*minHealthy > 100) {
		return prefs, "If you specify MaxHealthyPercentage, you must also specify MinHealthyPercentage, and the difference between them cannot be greater than 100."
	}
	return prefs, ""
}

// asRefreshReversible reports whether a refresh can be rolled back: it needs a
// desired configuration, and a group configuration to return to that names a
// launch template version rather than $Latest or $Default.
func asRefreshReversible(desired *ASLaunchSource, previous ASLaunchSource) bool {
	if desired == nil {
		return false
	}
	switch previous.LaunchTemplate.Version {
	case "$Latest", "$Default":
		return false
	case "":
		return !previous.LaunchTemplate.set()
	}
	return true
}

func asIrreversibleRefresh(w http.ResponseWriter) {
	asError(w, "IrreversibleInstanceRefresh", "The request failed because a desired configuration was not found or an incompatible launch template (uses a Systems Manager parameter instead of an AMI ID) or launch template version ($Latest or $Default) is present on the Auto Scaling group.", http.StatusBadRequest)
}

// asRefreshCandidates lists the members a refresh replaces: all of them except
// the Standby and scale-in protected members its preferences ignore, and, with
// skip matching, those that already launched from the target configuration.
func asRefreshCandidates(asg AutoScalingGroup, prefs ASRefreshPreferences, target ASLaunchSource) []string {
	ex := asxExtras(asg.Name)
	var matching *ASLaunchSource
	if prefs.SkipMatching {
		if spec, err := asResolveLaunchSource(target); err == nil {
			matching = &spec.source
		}
	}
	var out []string
	for _, id := range asg.InstanceIds {
		if prefs.StandbyInstances == "Ignore" && indexOfString(ex.StandbyInstances, id) >= 0 {
			continue
		}
		if prefs.ScaleInProtectedInstances == "Ignore" && indexOfString(ex.ProtectedInstances, id) >= 0 {
			continue
		}
		if matching != nil && asMemberLaunchSource(asg, id) == *matching {
			continue
		}
		out = append(out, id)
	}
	return out
}

func handleASXCancelInstanceRefresh(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	if _, ok := asxRequireGroup(w, group); !ok {
		return
	}
	asRefreshMu.Lock()
	defer asRefreshMu.Unlock()
	ref := asActiveRefresh(group)
	if ref == nil || ref.Status == asRefreshCancelling {
		asActiveRefreshNotFound(w)
		return
	}
	ref.Status = asRefreshCancelling
	ref.StatusReason = ""
	ref.TimerKind, ref.TimerUntil = "", ""
	if r.FormValue("WaitForTransitioningInstances") == "false" {
		ref.InFlight = nil
	}
	asInstanceRefreshes.Put(ref.InstanceRefreshId, *ref)
	asRefreshStored(*ref)
	id := ref.InstanceRefreshId
	bg.Go(func() { asRefreshAdvance(id) })
	asResponse(w, "CancelInstanceRefresh", fmt.Sprintf("<InstanceRefreshId>%s</InstanceRefreshId>", xmlEscape(id)))
}

func asActiveRefreshNotFound(w http.ResponseWriter) {
	asError(w, "ActiveInstanceRefreshNotFound", "The request failed because an active instance refresh or rollback for the specified Auto Scaling group was not found.", http.StatusBadRequest)
}

func handleASXRollbackInstanceRefresh(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
		return
	}
	asRefreshMu.Lock()
	defer asRefreshMu.Unlock()
	ref := asActiveRefresh(group)
	if ref == nil || (ref.Status != asRefreshPending && ref.Status != asRefreshInProgress && ref.Status != asRefreshBaking) {
		asActiveRefreshNotFound(w)
		return
	}
	if !asRefreshReversible(ref.Desired, ref.Previous) {
		asIrreversibleRefresh(w)
		return
	}
	asRefreshBeginRollback(ref, asg)
	asInstanceRefreshes.Put(ref.InstanceRefreshId, *ref)
	asRefreshStored(*ref)
	id := ref.InstanceRefreshId
	bg.Go(func() { asRefreshAdvance(id) })
	asResponse(w, "RollbackInstanceRefresh", fmt.Sprintf("<InstanceRefreshId>%s</InstanceRefreshId>", xmlEscape(id)))
}

// asRefreshBeginRollback turns a refresh around: the instances it launched so
// far become the ones to replace, from the configuration the group had before.
func asRefreshBeginRollback(ref *ASInstanceRefresh, asg AutoScalingGroup) {
	ref.Rollback = &ASRefreshRollback{
		StartTime:                    time.Now().UTC().Format(asActivityTimeLayout),
		PercentageCompleteOnRollback: asRefreshPercentage(*ref, asg),
		InstancesToUpdateOnRollback:  ref.Total - ref.Updated,
	}
	ref.Status = asRefreshRollbackInProgress
	ref.StatusReason = ""
	ref.TimerKind, ref.TimerUntil = "", ""
	ref.Batch, ref.Deferred = nil, nil
	ref.ToReplace = nil
	for _, id := range ref.Launched {
		if indexOfString(asg.InstanceIds, id) >= 0 {
			ref.ToReplace = append(ref.ToReplace, id)
		}
	}
}

// asRefreshKick lets a refresh waiting on Standby or scale-in protected
// members look again once they changed.
func asRefreshKick(group string) {
	if ref := asActiveRefresh(group); ref != nil {
		id := ref.InstanceRefreshId
		bg.Go(func() { asRefreshAdvance(id) })
	}
}

func asRefreshAdvance(id string) {
	asRefreshMu.Lock()
	defer asRefreshMu.Unlock()
	ref, ok := asInstanceRefreshes.Get(id)
	if !ok {
		return
	}
	asRefreshStep(&ref)
	asRefreshPut(ref)
}

// asRefreshPut stores a refresh unless its group, and with it the refresh, was
// deleted meanwhile.
func asRefreshPut(ref ASInstanceRefresh) {
	if _, ok := asInstanceRefreshes.Get(ref.InstanceRefreshId); ok {
		asInstanceRefreshes.Put(ref.InstanceRefreshId, ref)
		asRefreshStored(ref)
	}
}

// asRefreshStep moves a refresh on as far as it can without waiting: it ends
// the refresh, or replaces the next batch, or leaves it waiting on launches in
// flight or on a timer.
func asRefreshStep(ref *ASInstanceRefresh) {
	asg, ok := autoScalingGroups.Get(ref.AutoScalingGroupName)
	if !ok {
		return
	}
	switch ref.Status {
	case asRefreshPending:
		ref.Status = asRefreshInProgress
	case asRefreshInProgress, asRefreshRollbackInProgress:
	case asRefreshCancelling:
		if len(ref.InFlight) == 0 {
			asRefreshEnd(ref, asRefreshCancelled, "")
		}
		return
	default:
		return
	}
	// The wait on Standby and protected members is a deadline, not a pause:
	// a member leaving either state lets the refresh go on at once.
	if len(ref.InFlight) > 0 || (ref.TimerKind != "" && ref.TimerKind != asRefreshTimerWait) {
		return
	}
	eligible := asRefreshEligible(ref, asg)
	if len(ref.ToReplace) == 0 {
		asRefreshComplete(ref, &asg)
		return
	}
	if len(eligible) == 0 {
		if ref.TimerKind == "" {
			asRefreshArm(ref, asRefreshTimerWait, asRefreshStandbyProtectedTimeout)
		}
		return
	}
	ref.TimerKind, ref.TimerUntil = "", ""
	launch, terminateFirst := asRefreshBatch(ref.Preferences, asg.DesiredCapacity)
	launch = min(launch, len(eligible))
	terminateFirst = min(terminateFirst, launch)
	batch := eligible[:launch]
	cause := fmt.Sprintf("At %s an instance was taken out of service in response to an instance refresh.", time.Now().UTC().Format(time.RFC3339))
	for _, id := range batch[:terminateFirst] {
		asTerminateMember(&asg, id, cause)
	}
	ref.Deferred = append([]string(nil), batch[terminateFirst:]...)
	before := len(asg.InstanceIds)
	source := asg.launchSource()
	switch {
	case ref.Rollback != nil:
		source = ref.Previous
	case ref.Desired != nil:
		source = *ref.Desired
	}
	launchCause := fmt.Sprintf("At %s an instance was started in response to a difference between desired and actual capacity, increasing the capacity from %d to %d.",
		time.Now().UTC().Format(time.RFC3339), before, before+launch)
	launches, err := asLaunchMembers(&asg, source, launch, launchCause)
	autoScalingGroups.Put(asg.Name, asg)
	ref.Batch = nil
	for _, l := range launches {
		ref.Batch = append(ref.Batch, l.instanceID)
		ref.InFlight = append(ref.InFlight, l.instanceID)
		if ref.Rollback == nil {
			ref.Launched = append(ref.Launched, l.instanceID)
		}
	}
	asStartLaunches(asg.Name, launches)
	if err != nil {
		asRefreshFail(ref, asg, err.Error())
	}
}

// asRefreshEligible drops the members that left the group from those left to
// replace, and returns the ones it can replace now: all but the Standby and
// scale-in protected members it waits for.
func asRefreshEligible(ref *ASInstanceRefresh, asg AutoScalingGroup) []string {
	remaining := ref.ToReplace[:0]
	for _, id := range ref.ToReplace {
		if indexOfString(asg.InstanceIds, id) >= 0 {
			remaining = append(remaining, id)
		}
	}
	ref.ToReplace = remaining
	ex := asxExtras(asg.Name)
	var eligible []string
	for _, id := range ref.ToReplace {
		if ref.Preferences.StandbyInstances == "Wait" && indexOfString(ex.StandbyInstances, id) >= 0 {
			continue
		}
		if ref.Preferences.ScaleInProtectedInstances == "Wait" && indexOfString(ex.ProtectedInstances, id) >= 0 {
			continue
		}
		eligible = append(eligible, id)
	}
	return eligible
}

// asRefreshBatch sizes a replacement batch from the healthy-percentage bounds
// on the desired capacity: how many instances to launch, and how many of the
// instances they replace to terminate before launching. Without
// MaxHealthyPercentage the service terminates and launches together; with it,
// it launches first whatever the minimum does not allow it to take out of
// service, up to the maximum. A batch replaces at least one instance.
func asRefreshBatch(prefs ASRefreshPreferences, desired int) (launch, terminateFirst int) {
	minInService := int(math.Ceil(float64(desired*prefs.MinHealthyPercentage) / 100))
	if prefs.MaxHealthyPercentage == nil {
		launch = max(1, desired-minInService)
		return launch, launch
	}
	maxCapacity := desired * *prefs.MaxHealthyPercentage / 100
	launch = max(1, maxCapacity-minInService)
	return launch, min(launch, max(0, desired-minInService))
}

// asRefreshWarmup is the instance warmup the refresh waits after a batch is
// InService: the preference, else the group's DefaultInstanceWarmup, else its
// HealthCheckGracePeriod.
func asRefreshWarmup(ref ASInstanceRefresh, asg AutoScalingGroup) time.Duration {
	seconds := asg.HealthCheckGracePeriod
	switch {
	case ref.Preferences.InstanceWarmup != nil:
		seconds = *ref.Preferences.InstanceWarmup
	case asg.DefaultInstanceWarmup != nil:
		seconds = *asg.DefaultInstanceWarmup
	}
	return time.Duration(seconds) * time.Second
}

func asRefreshArm(ref *ASInstanceRefresh, kind string, d time.Duration) {
	until := time.Now().UTC().Add(d).Format(time.RFC3339Nano)
	ref.TimerKind, ref.TimerUntil = kind, until
	id := ref.InstanceRefreshId
	bg.AfterFunc(d, func() { asRefreshTimerFired(id, kind, until) })
}

func asRefreshTimerFired(id, kind, until string) {
	asRefreshMu.Lock()
	defer asRefreshMu.Unlock()
	ref, ok := asInstanceRefreshes.Get(id)
	if !ok || ref.TimerKind != kind || ref.TimerUntil != until {
		return
	}
	ref.TimerKind, ref.TimerUntil = "", ""
	asg, ok := autoScalingGroups.Get(ref.AutoScalingGroupName)
	if !ok {
		return
	}
	switch kind {
	case asRefreshTimerWarmup:
		ref.StatusReason = ""
		asRefreshBatchWarmed(&ref, &asg)
	case asRefreshTimerBake:
		ref.Baked = true
		asRefreshComplete(&ref, &asg)
		asRefreshPut(ref)
		return
	case asRefreshTimerWait:
		if len(ref.ToReplace) > 0 && len(asRefreshEligible(&ref, asg)) == 0 {
			asRefreshFail(&ref, asg, "")
			asRefreshPut(ref)
			return
		}
	}
	asRefreshStep(&ref)
	asRefreshPut(ref)
}

// asRefreshBatchWarmed finishes a batch once its instances have warmed up: the
// instances it launched ahead of their replacements take those out of
// service, the batch counts as updated, and a checkpoint reached pauses the
// refresh for its delay.
func asRefreshBatchWarmed(ref *ASInstanceRefresh, asg *AutoScalingGroup) {
	if len(ref.Deferred) > 0 {
		cause := fmt.Sprintf("At %s an instance was taken out of service in response to an instance refresh.", time.Now().UTC().Format(time.RFC3339))
		for _, id := range ref.Deferred {
			if indexOfString(asg.InstanceIds, id) >= 0 {
				asTerminateMember(asg, id, cause)
			}
		}
		ref.Deferred = nil
		autoScalingGroups.Put(asg.Name, *asg)
	}
	if ref.Rollback != nil {
		ref.Batch = nil
		return
	}
	ref.Updated = min(ref.Total, ref.Updated+len(ref.Batch))
	ref.Batch = nil
	pct := asRefreshPercentage(*ref, *asg)
	cps := ref.Preferences.CheckpointPercentages
	if ref.NextCheckpoint < len(cps) && pct >= cps[ref.NextCheckpoint] {
		for ref.NextCheckpoint < len(cps) && pct >= cps[ref.NextCheckpoint] {
			ref.NextCheckpoint++
		}
		if pct < 100 {
			delay := asRefreshDefaultCheckpointDelay
			if ref.Preferences.CheckpointDelay != nil {
				delay = *ref.Preferences.CheckpointDelay
			}
			asRefreshArm(ref, asRefreshTimerCheckpoint, time.Duration(delay)*time.Second)
		}
	}
}

// asRefreshComplete ends a refresh with nothing left to replace. Forward, it
// waits out BakeTime and then gives the group the desired configuration.
func asRefreshComplete(ref *ASInstanceRefresh, asg *AutoScalingGroup) {
	if ref.Rollback != nil {
		asRefreshEnd(ref, asRefreshRollbackSuccessful, "")
		return
	}
	if bake := ref.Preferences.BakeTime; bake != nil && *bake > 0 && !ref.Baked {
		ref.Status = asRefreshBaking
		asRefreshArm(ref, asRefreshTimerBake, time.Duration(*bake)*time.Second)
		return
	}
	if ref.Desired != nil {
		asg.LaunchTemplate = ref.Desired.LaunchTemplate
		if lt, ok := lookupLaunchTemplate(asg.LaunchTemplate.LaunchTemplateId, asg.LaunchTemplate.LaunchTemplateName); ok {
			asg.LaunchTemplate.LaunchTemplateId, asg.LaunchTemplate.LaunchTemplateName = lt.LaunchTemplateId, lt.LaunchTemplateName
		}
		asg.LaunchConfigurationName = ""
		autoScalingGroups.Put(asg.Name, *asg)
	}
	asRefreshEnd(ref, asRefreshSuccessful, "")
}

// asRefreshFail ends a refresh that cannot go on, or rolls it back when it
// asked for auto rollback.
func asRefreshFail(ref *ASInstanceRefresh, asg AutoScalingGroup, reason string) {
	switch {
	case ref.Rollback != nil:
		asRefreshEnd(ref, asRefreshRollbackFailed, reason)
	case ref.Preferences.AutoRollback:
		asRefreshBeginRollback(ref, asg)
		ref.Rollback.Reason = reason
		id := ref.InstanceRefreshId
		bg.Go(func() { asRefreshAdvance(id) })
	default:
		asRefreshEnd(ref, asRefreshFailed, reason)
	}
}

func asRefreshEnd(ref *ASInstanceRefresh, status, reason string) {
	ref.Status = status
	ref.StatusReason = reason
	ref.TimerKind, ref.TimerUntil = "", ""
	ref.EndTime = time.Now().UTC().Format(asActivityTimeLayout)
}

// asRefreshInstanceSettled hears that an instance a refresh launched entered
// service, or failed to (reason says why). The last of a batch to enter
// service starts its warmup; a failure fails the refresh.
func asRefreshInstanceSettled(group, instanceID, reason string) {
	asRefreshMu.Lock()
	defer asRefreshMu.Unlock()
	ref := asActiveRefresh(group)
	if ref == nil {
		return
	}
	idx := indexOfString(ref.InFlight, instanceID)
	if idx < 0 {
		return
	}
	ref.InFlight = append(ref.InFlight[:idx], ref.InFlight[idx+1:]...)
	asg, ok := autoScalingGroups.Get(group)
	if !ok {
		return
	}
	switch {
	case ref.Status == asRefreshCancelling:
		asRefreshStep(ref)
	case reason != "":
		ref.InFlight = nil
		asRefreshFail(ref, asg, reason)
	case len(ref.InFlight) == 0 && len(ref.Batch) == 0:
		asRefreshStep(ref)
	case len(ref.InFlight) == 0:
		ref.StatusReason = fmt.Sprintf("Waiting for instances to warm up before continuing. For example: %s is warming up.", ref.Batch[0])
		asRefreshArm(ref, asRefreshTimerWarmup, asRefreshWarmup(*ref, asg))
	}
	asRefreshPut(*ref)
}

// asRefreshPercentage is the share of the instances to update that the
// refresh has replaced and warmed up; rolling back, the share of its instances
// still in the group, which falls back to zero.
func asRefreshPercentage(ref ASInstanceRefresh, asg AutoScalingGroup) int {
	if ref.Total == 0 {
		if ref.Status == asRefreshSuccessful {
			return 100
		}
		return 0
	}
	if ref.Rollback == nil {
		return ref.Updated * 100 / ref.Total
	}
	remaining := 0
	for _, id := range ref.Launched {
		if indexOfString(asg.InstanceIds, id) >= 0 {
			remaining++
		}
	}
	return min(100, remaining*100/ref.Total)
}

func asRefreshInstancesToUpdate(ref ASInstanceRefresh) int {
	if ref.Rollback != nil {
		return ref.Rollback.InstancesToUpdateOnRollback
	}
	return ref.Total - ref.Updated
}

// asResumeInstanceRefreshes picks up the refreshes a previous run left under
// way: it re-arms their timers and settles launches that finished meanwhile.
func asResumeInstanceRefreshes() {
	for _, ref := range asInstanceRefreshes.List() {
		if !asRefreshActive(ref.Status) {
			continue
		}
		for _, instanceID := range ref.InFlight {
			inst, ok := ec2Instances.Get(instanceID)
			switch {
			case ok && inst.State == "running" && !asInstanceAwaitsLifecycleAction(ref.AutoScalingGroupName, instanceID):
				bg.Go(func() { asRefreshInstanceSettled(ref.AutoScalingGroupName, instanceID, "") })
			case !ok || inst.State == "terminated":
				bg.Go(func() {
					asRefreshInstanceSettled(ref.AutoScalingGroupName, instanceID, "The instance was terminated before it entered service.")
				})
			}
		}
		if ref.TimerKind != "" {
			until, err := time.Parse(time.RFC3339Nano, ref.TimerUntil)
			if err != nil {
				panic(fmt.Sprintf("instance refresh %s timer %q: %v", ref.InstanceRefreshId, ref.TimerUntil, err))
			}
			id, kind, raw := ref.InstanceRefreshId, ref.TimerKind, ref.TimerUntil
			bg.AfterFunc(max(0, time.Until(until)), func() { asRefreshTimerFired(id, kind, raw) })
			continue
		}
		id := ref.InstanceRefreshId
		bg.Go(func() { asRefreshAdvance(id) })
	}
}

func handleASXDescribeInstanceRefreshes(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("AutoScalingGroupName")
	asg, ok := asxRequireGroup(w, group)
	if !ok {
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
	sort.Slice(refs, func(i, j int) bool { return refs[i].StartTime > refs[j].StartTime })
	page, next, pageOK := awsPage(w, asBadToken, refs, r.FormValue("NextToken"), asAtoiDefault(r.FormValue("MaxRecords"), 0), 0)
	if !pageOK {
		return
	}
	var items strings.Builder
	for _, ref := range page {
		items.WriteString(asInstanceRefreshXML(ref, asg))
	}
	body := fmt.Sprintf("<InstanceRefreshes>%s</InstanceRefreshes>", items.String())
	if next != "" {
		body += "<NextToken>" + xmlEscape(next) + "</NextToken>"
	}
	asResponse(w, "DescribeInstanceRefreshes", body)
}

func asInstanceRefreshXML(ref ASInstanceRefresh, asg AutoScalingGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<member><InstanceRefreshId>%s</InstanceRefreshId><AutoScalingGroupName>%s</AutoScalingGroupName><Status>%s</Status>",
		xmlEscape(ref.InstanceRefreshId), xmlEscape(ref.AutoScalingGroupName), ref.Status)
	if ref.StatusReason != "" {
		fmt.Fprintf(&b, "<StatusReason>%s</StatusReason>", xmlEscape(ref.StatusReason))
	}
	fmt.Fprintf(&b, "<StartTime>%s</StartTime>", ref.StartTime)
	if ref.EndTime != "" {
		fmt.Fprintf(&b, "<EndTime>%s</EndTime>", ref.EndTime)
	}
	fmt.Fprintf(&b, "<PercentageComplete>%d</PercentageComplete><InstancesToUpdate>%d</InstancesToUpdate>",
		asRefreshPercentage(ref, asg), asRefreshInstancesToUpdate(ref))
	p := ref.Preferences
	fmt.Fprintf(&b, "<Preferences><MinHealthyPercentage>%d</MinHealthyPercentage>", p.MinHealthyPercentage)
	if p.MaxHealthyPercentage != nil {
		fmt.Fprintf(&b, "<MaxHealthyPercentage>%d</MaxHealthyPercentage>", *p.MaxHealthyPercentage)
	}
	if p.InstanceWarmup != nil {
		fmt.Fprintf(&b, "<InstanceWarmup>%d</InstanceWarmup>", *p.InstanceWarmup)
	}
	if len(p.CheckpointPercentages) > 0 {
		b.WriteString("<CheckpointPercentages>")
		for _, cp := range p.CheckpointPercentages {
			fmt.Fprintf(&b, "<member>%d</member>", cp)
		}
		b.WriteString("</CheckpointPercentages>")
	}
	if p.CheckpointDelay != nil {
		fmt.Fprintf(&b, "<CheckpointDelay>%d</CheckpointDelay>", *p.CheckpointDelay)
	}
	fmt.Fprintf(&b, "<SkipMatching>%t</SkipMatching><AutoRollback>%t</AutoRollback><ScaleInProtectedInstances>%s</ScaleInProtectedInstances><StandbyInstances>%s</StandbyInstances>",
		p.SkipMatching, p.AutoRollback, xmlEscape(p.ScaleInProtectedInstances), xmlEscape(p.StandbyInstances))
	if p.BakeTime != nil {
		fmt.Fprintf(&b, "<BakeTime>%d</BakeTime>", *p.BakeTime)
	}
	b.WriteString("</Preferences>")
	if ref.Desired != nil {
		fmt.Fprintf(&b, "<DesiredConfiguration><LaunchTemplate>%s</LaunchTemplate></DesiredConfiguration>", asLaunchTemplateSpecXML(ref.Desired.LaunchTemplate))
	}
	if rb := ref.Rollback; rb != nil {
		b.WriteString("<RollbackDetails>")
		if rb.Reason != "" {
			fmt.Fprintf(&b, "<RollbackReason>%s</RollbackReason>", xmlEscape(rb.Reason))
		}
		fmt.Fprintf(&b, "<RollbackStartTime>%s</RollbackStartTime><PercentageCompleteOnRollback>%d</PercentageCompleteOnRollback><InstancesToUpdateOnRollback>%d</InstancesToUpdateOnRollback></RollbackDetails>",
			rb.StartTime, rb.PercentageCompleteOnRollback, rb.InstancesToUpdateOnRollback)
	}
	fmt.Fprintf(&b, "<Strategy>%s</Strategy></member>", xmlEscape(ref.Strategy))
	return b.String()
}
