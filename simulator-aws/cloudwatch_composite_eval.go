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
	payload, err := json.Marshal(map[string]any{
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
	})
	if err != nil {
		return
	}
	cwPublishAlarmNotification(ca.AlarmName, targets, cwAlarmNotificationSubject(newState, ca.AlarmName), string(payload))
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
