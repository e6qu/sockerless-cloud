package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

func startAppScheduledActions(srv *sim.Server) {
	actions, targets := appScheduledActions, appScalableTargets
	records := sim.MakeStore[cron.Record](srv.DB(), "app_scheduled_action_runs")
	cron.NewTicker(records, func() []cron.Entry { return appScheduledActionEntries(actions, targets) }).
		Start(srv, "Application Auto Scaling scheduled actions", time.Second)
}

// appScheduledActionEntries runs every scheduled action on its Schedule, read
// in its Timezone, between StartTime and EndTime.
func appScheduledActionEntries(actions sim.Store[AppScheduledAction], targets sim.Store[AppScalableTarget]) []cron.Entry {
	var entries []cron.Entry
	for _, action := range actions.List() {
		plan, err := parseAWSSchedule(action.Schedule, action.Timezone, true)
		if err != nil {
			continue
		}
		within := func(t time.Time, ok bool) (time.Time, bool) {
			if !ok || (action.EndTime > 0 && t.After(schedulerDate(action.EndTime))) {
				return time.Time{}, false
			}
			return t, true
		}
		entry := cron.Entry{
			Key:  appScheduledActionKey(action.ServiceNamespace, action.ResourceId, action.ScheduledActionName),
			Spec: fmt.Sprintf("%s|%s|%v|%v|%s", action.Schedule, action.Timezone, action.StartTime, action.EndTime, action.ScalableTargetAction),
			First: func(now time.Time) (time.Time, bool) {
				start := now
				if plan.rate > 0 && action.CreationTime > 0 {
					start = schedulerDate(action.CreationTime)
				}
				if action.StartTime > 0 && schedulerDate(action.StartTime).After(start) {
					start = schedulerDate(action.StartTime)
				}
				return within(plan.first(start))
			},
			Fire: func(at time.Time) { appRunScheduledAction(action, targets, at) },
		}
		if plan.recurring() {
			entry.Next = func(after time.Time) (time.Time, bool) { return within(plan.next(after)) }
		}
		entries = append(entries, entry)
	}
	return entries
}

// appRunScheduledAction sets the scalable target's MinCapacity and
// MaxCapacity to the action's ScalableTargetAction and moves the current
// capacity into the new range, unless the target suspends scheduled scaling.
func appRunScheduledAction(action AppScheduledAction, targets sim.Store[AppScalableTarget], at time.Time) {
	var change struct {
		MinCapacity *int `json:"MinCapacity"`
		MaxCapacity *int `json:"MaxCapacity"`
	}
	if len(action.ScalableTargetAction) == 0 || json.Unmarshal(action.ScalableTargetAction, &change) != nil {
		return
	}
	key := appScalableTargetKey(action.ServiceNamespace, action.ResourceId, action.ScalableDimension)
	target, ok := targets.Get(key)
	if !ok {
		return
	}
	var suspended struct {
		ScheduledScalingSuspended bool `json:"ScheduledScalingSuspended"`
	}
	if len(target.SuspendedState) > 0 {
		_ = json.Unmarshal(target.SuspendedState, &suspended)
	}
	if suspended.ScheduledScalingSuspended {
		return
	}
	if change.MinCapacity != nil {
		target.MinCapacity = *change.MinCapacity
	}
	if change.MaxCapacity != nil {
		target.MaxCapacity = *change.MaxCapacity
	}
	targets.Put(key, target)

	message := fmt.Sprintf("Setting min capacity to %d and max capacity to %d", target.MinCapacity, target.MaxCapacity)
	if controller := appScalingControllers[target.ScalableDimension]; controller != nil {
		if current, ok := controller.Read(target); ok {
			desired := min(max(current, target.MinCapacity), target.MaxCapacity)
			if desired != current && controller.Apply(target, desired) {
				message += " and desired capacity to " + strconv.Itoa(desired)
			}
		}
	}
	activity := AppScalingActivity{
		ActivityId:        appScalingActivityID(at),
		ServiceNamespace:  action.ServiceNamespace,
		ResourceId:        action.ResourceId,
		ScalableDimension: action.ScalableDimension,
		Description:       message,
		Cause:             "scheduled action name " + action.ScheduledActionName + " was triggered",
		StartTime:         float64(at.Unix()),
		EndTime:           float64(time.Now().Unix()),
		StatusCode:        "Successful",
		StatusMessage:     "Successfully set min capacity to " + strconv.Itoa(target.MinCapacity) + " and max capacity to " + strconv.Itoa(target.MaxCapacity),
	}
	appScalingActivities.Put(activity.ActivityId, activity)
}
