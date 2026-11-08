package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// A log alarm evaluates when its scheduled query runs, on the query's
// ScheduleExpression, and on a change of state runs its actions and sends its
// state-change event as metric alarms do. A SetAlarmState holds until that
// next evaluation.

func cwLogAlarmEffectiveState(a CWLogAlarm) (state, reason, evaluationState string) {
	if a.ManualState != "" {
		return a.ManualState, a.ManualStateReason, ""
	}
	return cwEvaluateLogAlarmState(a)
}

func cwEvaluateLogAlarms(now time.Time) {
	for _, a := range cwLogAlarms.List() {
		cwEvaluateLogAlarm(a.AlarmName, now)
	}
}

func cwEvaluateLogAlarm(name string, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			cwEvalLogger.Error().Str("alarmName", name).Interface("recover", r).Msg("CloudWatch log alarm evaluator panic recovered")
		}
	}()
	current, ok := cwLogAlarms.Get(name)
	if !ok {
		return
	}
	if interval := cwScheduleIntervalSeconds(current.ScheduleExpression); current.LastEvaluated > 0 && interval > 0 && now.Unix()-current.LastEvaluated < interval {
		return
	}
	newState, reason, _ := cwEvaluateLogAlarmState(current)
	var prev string
	var before CWLogAlarm
	transitioned := false
	cwLogAlarms.Update(name, func(x *CWLogAlarm) {
		x.LastEvaluated = now.Unix()
		x.ManualState, x.ManualStateReason = "", ""
		prev = cwStateOrInsufficient(x.StateValue)
		if prev == newState {
			return
		}
		before = *x
		x.StateValue, x.StateReason, x.StateUpdatedTimestamp = newState, reason, now.Unix()
		transitioned = true
	})
	if !transitioned {
		return
	}
	cwRecordLogAlarmTransition(before, prev, newState, reason, "", now)
}

// cwSetLogAlarmState sets a log alarm's state until its next evaluation.
func cwSetLogAlarmState(name, state, reason, reasonData string) bool {
	var prev string
	var before CWLogAlarm
	if !cwLogAlarms.Update(name, func(a *CWLogAlarm) {
		prev, _, _ = cwLogAlarmEffectiveState(*a)
		before = *a
		a.ManualState, a.ManualStateReason = state, reason
		a.StateValue, a.StateReason, a.StateUpdatedTimestamp = state, reason, time.Now().UTC().Unix()
	}) {
		return false
	}
	if prev != state {
		cwRecordLogAlarmTransition(before, prev, state, reason, reasonData, time.Now())
	} else {
		cwRecordAlarmHistory(name, "LogAlarm", "StateUpdate", fmt.Sprintf("Alarm updated from %s to %s", prev, state), reasonData)
	}
	return true
}

func cwRecordLogAlarmTransition(before CWLogAlarm, prev, newState, reason, reasonData string, at time.Time) {
	historyData, _ := json.Marshal(map[string]string{"previousState": prev, "newState": newState, "stateReason": reason})
	if reasonData == "" {
		reasonData = string(historyData)
	}
	cwRecordAlarmHistory(before.AlarmName, "LogAlarm", "StateUpdate", fmt.Sprintf("Alarm updated from %s to %s", prev, newState), reasonData)
	cwLogAlarmTransition(before, prev, newState, reason, at).run()
}

func cwLogAlarmTransition(before CWLogAlarm, prev, newState, reason string, at time.Time) cwAlarmTransition {
	payload := map[string]any{
		"AlarmName":                          before.AlarmName,
		"AlarmDescription":                   before.AlarmDescription,
		"AWSAccountId":                       awsAccountID(),
		"AlarmConfigurationUpdatedTimestamp": cwAlarmEventUnixTime(before.ConfigurationUpdated, at),
		"NewStateValue":                      newState,
		"NewStateReason":                     reason,
		"StateChangeTime":                    at.UTC().Format("2006-01-02T15:04:05.000Z"),
		"Region":                             awsRegion(),
		"AlarmArn":                           before.AlarmArn,
		"OldStateValue":                      prev,
		"OKActions":                          cwNonNilStrings(before.OKActions),
		"AlarmActions":                       cwNonNilStrings(before.AlarmActions),
		"InsufficientDataActions":            cwNonNilStrings(before.InsufficientDataActions),
		"LogGroups":                          cwNonNilStrings(before.LogGroupIdentifiers),
		"QueryString":                        before.QueryString,
		"AggregationExpression":              before.AggregationExpression,
	}
	cwAddNotificationTags(payload, before.ActionConfiguration, before.Tags)
	message, _ := json.Marshal(payload)
	return cwAlarmTransition{
		Name:           before.AlarmName,
		Arn:            before.AlarmArn,
		ActionsEnabled: before.ActionsEnabled,
		Targets:        cwActionsForState(newState, before.AlarmActions, before.OKActions, before.InsufficientDataActions),
		Subject:        cwAlarmNotificationSubject(newState, before.AlarmName),
		SNSMessage:     string(message),
		Configuration:  cwLogAlarmConfigurationDetail(before, time.Unix(before.ConfigurationUpdated, 0)),
		Previous:       cwAlarmEventState{Value: prev, Reason: before.StateReason, Timestamp: cwAlarmEventUnixTime(before.StateUpdatedTimestamp, at)},
		State:          cwAlarmEventState{Value: newState, Reason: reason, Timestamp: cwAlarmEventTime(at)},
		ActionConfig:   before.ActionConfiguration,
		Tags:           before.Tags,
	}
}
