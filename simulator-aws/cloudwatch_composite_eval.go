package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"
)

// A composite alarm is ALARM while its AlarmRule holds and OK otherwise; it is
// INSUFFICIENT_DATA only between creation and its first evaluation
// (PutCompositeAlarm in the CloudWatch API reference). A SetAlarmState on it
// holds until one of its children changes state or its configuration is
// updated (SetAlarmState).

// cwAlarmRefName turns an AlarmRule reference, a name or an alarm ARN, into the
// alarm's name.
func cwAlarmRefName(ref string) string {
	if strings.HasPrefix(ref, "arn:") {
		if i := strings.Index(ref, ":alarm:"); i >= 0 {
			return ref[i+len(":alarm:"):]
		}
	}
	return ref
}

// cwAlarmCurrentState answers a child alarm's state: a metric or log alarm's
// derived state, a composite alarm's recorded one. A child the account does
// not hold reads as INSUFFICIENT_DATA.
func cwAlarmCurrentState(name string) string {
	if a, ok := cwAlarms.Get(name); ok {
		state, _ := cwAlarmEffectiveState(a)
		return state
	}
	if la, ok := cwLogAlarms.Get(name); ok {
		state, _, _ := cwEvaluateLogAlarmState(la)
		return state
	}
	if ca, ok := cwCompositeAlarms.Get(name); ok && ca.StateValue != "" {
		return ca.StateValue
	}
	return "INSUFFICIENT_DATA"
}

func cwCompositeChildStates(rule *cwRuleNode) map[string]string {
	states := map[string]string{}
	for _, ref := range rule.refsOf() {
		name := cwAlarmRefName(ref)
		if _, seen := states[name]; !seen {
			states[name] = cwAlarmCurrentState(name)
		}
	}
	return states
}

// The state-change reason, as the CloudWatch User Guide's alarm events show
// it: "<child ARN> transitioned to ALARM at Friday 22 July, 2022 15:57:45 UTC".
func cwCompositeTransitionReason(child, state string, at time.Time) string {
	return fmt.Sprintf("%s transitioned to %s at %s", cwAlarmArn(child), state, at.UTC().Format("Monday 2 January, 2006 15:04:05 MST"))
}

func cwEvaluateCompositeAlarms(now time.Time) {
	for _, ca := range cwCompositeAlarms.List() {
		cwEvaluateCompositeAlarm(ca.AlarmName, now)
	}
}

func cwEvaluateCompositeAlarm(name string, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			cwEvalLogger.Error().Str("alarmName", name).Interface("recover", r).Msg("CloudWatch composite alarm evaluator panic recovered")
		}
	}()
	current, ok := cwCompositeAlarms.Get(name)
	if !ok {
		return
	}
	rule, err := cwParseAlarmRule(current.AlarmRule)
	if err != nil {
		return
	}
	children := cwCompositeChildStates(rule)
	var prev, newState, reason string
	var triggering []string
	var before CWCompositeAlarm
	transitioned := false
	cwCompositeAlarms.Update(name, func(x *CWCompositeAlarm) {
		if x.Held {
			if maps.Equal(x.ManualChildStates, children) {
				return
			}
			x.Held, x.ManualChildStates = false, nil
		}
		lastSeen := x.ChildStates
		x.ChildStates = children
		newState = "OK"
		if rule.eval(func(ref string) string { return children[cwAlarmRefName(ref)] }) {
			newState = "ALARM"
		}
		prev = x.StateValue
		if prev == "" {
			prev = "INSUFFICIENT_DATA"
		}
		if prev == newState {
			return
		}
		if x.StateValue == "" {
			reason = fmt.Sprintf("%s was created and its alarm rule evaluates to %s", x.AlarmArn, newState)
		} else {
			for _, ref := range rule.refsOf() {
				child := cwAlarmRefName(ref)
				if lastSeen[child] != children[child] {
					triggering = append(triggering, cwAlarmArn(child))
					if reason == "" {
						reason = cwCompositeTransitionReason(child, children[child], now)
					}
				}
			}
			if reason == "" {
				reason = fmt.Sprintf("%s alarm rule evaluates to %s", x.AlarmArn, newState)
			}
		}
		before = *x
		x.StateValue = newState
		x.StateReason = reason
		x.UpdatedTimestamp = now.Unix()
		transitioned = true
		cwDispatchCompositeAlarmActions(*x, prev, newState, reason, triggering, now)
	})
	if !transitioned {
		return
	}
	historyData, _ := json.Marshal(map[string]string{
		"previousState": prev,
		"newState":      newState,
		"stateReason":   reason,
	})
	cwRecordAlarmHistory(name, "CompositeAlarm", "StateUpdate",
		fmt.Sprintf("Alarm updated from %s to %s", prev, newState), string(historyData))
	cwPutCompositeAlarmStateChangeEvent(before, prev, newState, reason, cwTriggeringAlarmsReasonData(triggering, children, now), now)
}

// The reasonData the alarm events carry for a composite alarm:
// {"triggeringAlarms":[{"arn":…,"state":{"value":…,"timestamp":…}}]}.
func cwTriggeringAlarmsReasonData(triggering []string, children map[string]string, at time.Time) string {
	if len(triggering) == 0 {
		return ""
	}
	type childState struct {
		Value     string `json:"value"`
		Timestamp string `json:"timestamp"`
	}
	type trigger struct {
		Arn   string     `json:"arn"`
		State childState `json:"state"`
	}
	out := struct {
		TriggeringAlarms []trigger `json:"triggeringAlarms"`
	}{}
	for _, arn := range triggering {
		out.TriggeringAlarms = append(out.TriggeringAlarms, trigger{Arn: arn,
			State: childState{Value: children[cwAlarmRefName(arn)], Timestamp: cwAlarmEventTime(at)}})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

func cwPutCompositeAlarmStateChangeEvent(before CWCompositeAlarm, prev, newState, reason, reasonData string, at time.Time) {
	previous := cwAlarmEventState{Value: prev, Reason: before.StateReason, Timestamp: cwAlarmEventTime(at)}
	if before.UpdatedTimestamp > 0 {
		previous.Timestamp = cwAlarmEventTime(time.Unix(before.UpdatedTimestamp, 0))
	}
	cwPutAlarmStateChangeEvent(before.AlarmName, before.AlarmArn, map[string]any{"alarmRule": before.AlarmRule}, previous,
		cwAlarmEventState{Value: newState, Reason: reason, ReasonData: reasonData, Timestamp: cwAlarmEventTime(at)},
		before.ActionConfiguration, before.Tags)
}

func cwDispatchCompositeAlarmActions(ca CWCompositeAlarm, oldState, newState, reason string, triggering []string, now time.Time) {
	if !ca.ActionsEnabled {
		return
	}
	targets := cwActionsForState(newState, ca.AlarmActions, ca.OKActions, ca.InsufficientDataActions)
	if len(targets) == 0 {
		return
	}
	if triggering == nil {
		triggering = []string{}
	}
	payload := map[string]any{
		"AlarmName":               ca.AlarmName,
		"AlarmDescription":        ca.AlarmDescription,
		"AWSAccountId":            awsAccountID(),
		"NewStateValue":           newState,
		"NewStateReason":          reason,
		"StateChangeTime":         now.UTC().Format("2006-01-02T15:04:05.000Z"),
		"Region":                  awsRegion(),
		"AlarmArn":                ca.AlarmArn,
		"OKActions":               ca.OKActions,
		"AlarmActions":            ca.AlarmActions,
		"InsufficientDataActions": ca.InsufficientDataActions,
		"OldStateValue":           oldState,
		"AlarmRule":               ca.AlarmRule,
		"TriggeringChildren":      triggering,
	}
	cwAddNotificationTags(payload, ca.ActionConfiguration, ca.Tags)
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	cwPublishAlarmNotification(ca.AlarmName, targets, cwAlarmNotificationSubject(newState, ca.AlarmName), string(body))
}

func cwActionsForState(state string, alarm, ok, insufficient []string) []string {
	switch state {
	case "ALARM":
		return alarm
	case "OK":
		return ok
	case "INSUFFICIENT_DATA":
		return insufficient
	}
	return nil
}
