package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// A composite alarm is ALARM while its AlarmRule holds and OK otherwise; it is
// INSUFFICIENT_DATA only between creation and its first evaluation
// (PutCompositeAlarm in the CloudWatch API reference). A SetAlarmState on it
// holds until one of its children changes state or its configuration is
// updated (SetAlarmState); a child that changes state and changes back ends the
// hold all the same.

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
// derived state, a composite alarm's recorded one, INSUFFICIENT_DATA before
// its first evaluation.
func cwAlarmCurrentState(name string) string {
	if a, ok := cwAlarms.Get(name); ok {
		state, _ := cwAlarmEffectiveState(a)
		return state
	}
	if la, ok := cwLogAlarms.Get(name); ok {
		state, _, _ := cwLogAlarmEffectiveState(la)
		return state
	}
	if ca, ok := cwCompositeAlarms.Get(name); ok {
		return cwStateOrInsufficient(ca.StateValue)
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

// cwValidateAlarmRule answers PutCompositeAlarm's ValidationError for a rule
// with surrounding whitespace, one that does not parse, or one naming an alarm
// the Region does not hold; an update may name the composite alarm itself.
func cwValidateAlarmRule(rule string) (string, bool) {
	if strings.TrimSpace(rule) != rule {
		return "AlarmRule must not contain leading or trailing whitespace or be null", false
	}
	parsed, err := cwParseAlarmRule(rule)
	if err != nil {
		return err.Error(), false
	}
	var missing []string
	seen := map[string]bool{}
	for _, ref := range parsed.refsOf() {
		name := cwAlarmRefName(ref)
		arn := cwAlarmArn(name)
		if strings.HasPrefix(ref, "arn:") && ref != arn {
			arn = ref
		} else if cwAlarmExists(name) {
			continue
		}
		if !seen[arn] {
			seen[arn] = true
			missing = append(missing, arn)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("Could not save the composite alarm as alarms [%s] in the alarm rule do not exist", strings.Join(missing, ", ")), false
	}
	return "", true
}

// cwValidateDeleteAlarms answers DeleteAlarms' ValidationError for a request
// naming more than one composite alarm, or naming an alarm a composite alarm's
// rule references, the composite alarm itself among the request or not.
func cwValidateDeleteAlarms(names []string) (string, bool) {
	composites := 0
	for _, n := range names {
		if _, ok := cwCompositeAlarms.Get(n); ok {
			composites++
		}
	}
	if composites > 1 {
		return fmt.Sprintf("Cannot delete [%d] Composite Alarms as only one composite alarm delete is allowed in single request", composites), false
	}
	referenced := map[string]bool{}
	for _, ca := range cwCompositeAlarms.List() {
		rule, err := cwParseAlarmRule(ca.AlarmRule)
		if err != nil {
			continue
		}
		for _, ref := range rule.refsOf() {
			referenced[cwAlarmRefName(ref)] = true
		}
	}
	var blocked []string
	for _, n := range names {
		if referenced[n] && !slices.Contains(blocked, n) {
			blocked = append(blocked, n)
		}
	}
	switch len(blocked) {
	case 0:
		return "", true
	case 1:
		return fmt.Sprintf("Cannot delete %s as there are composite alarm(s) depending on it.", blocked[0]), false
	}
	return fmt.Sprintf("Cannot delete %s as there are composite alarm(s) depending on them.", strings.Join(blocked, ",")), false
}

// cwNoteChildTransition records an alarm's change of state on every composite
// alarm whose rule references it, for that composite's next evaluation.
func cwNoteChildTransition(name, state string, at time.Time) {
	for _, ca := range cwCompositeAlarms.List() {
		rule, err := cwParseAlarmRule(ca.AlarmRule)
		if err != nil || !slices.ContainsFunc(rule.refsOf(), func(ref string) bool { return cwAlarmRefName(ref) == name }) {
			continue
		}
		cwCompositeAlarms.Update(ca.AlarmName, func(x *CWCompositeAlarm) {
			x.ChildTransitions = append(x.ChildTransitions, cwChildTransition{Name: name, State: state, At: at})
		})
	}
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
	var prev, newState, reason, reasonData string
	var triggering []string
	var before CWCompositeAlarm
	transitioned := false
	cwCompositeAlarms.Update(name, func(x *CWCompositeAlarm) {
		if x.Held && len(x.ChildTransitions) == 0 && !x.Updated {
			return
		}
		latest := map[string]cwChildTransition{}
		for _, ct := range x.ChildTransitions {
			latest[ct.Name] = ct
		}
		for child, state := range children {
			if _, noted := latest[child]; !noted && x.ChildStates[child] != state {
				latest[child] = cwChildTransition{Name: child, State: state, At: now}
			}
		}
		updated := x.Updated
		x.Held, x.Updated, x.ChildTransitions, x.ChildStates = false, false, nil, children
		newState = "OK"
		if rule.eval(func(ref string) string { return children[cwAlarmRefName(ref)] }) {
			newState = "ALARM"
		}
		prev = cwStateOrInsufficient(x.StateValue)
		if prev == newState {
			return
		}
		switch {
		case x.StateValue == "":
			reason = fmt.Sprintf("%s was created and its alarm rule evaluates to %s", x.AlarmArn, newState)
		case updated:
			reason = fmt.Sprintf("%s was updated and its new alarm rule evaluates to %s", x.AlarmArn, newState)
		default:
			var triggers []cwChildTransition
			for _, ref := range rule.refsOf() {
				if ct, ok := latest[cwAlarmRefName(ref)]; ok && !slices.Contains(triggering, cwAlarmArn(ct.Name)) {
					triggers = append(triggers, ct)
					triggering = append(triggering, cwAlarmArn(ct.Name))
				}
			}
			if len(triggers) == 0 {
				return
			}
			reason = cwCompositeTransitionReason(triggers[0].Name, triggers[0].State, triggers[0].At)
			reasonData = cwTriggeringAlarmsReasonData(triggers)
		}
		before = *x
		x.StateValue = newState
		x.StateReason = reason
		x.StateReasonData = reasonData
		x.UpdatedTimestamp = now.Unix()
		transitioned = true
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
	cwCompositeAlarmTransition(before, prev, newState, reason, reasonData, triggering, now).run()
}

// The reasonData the alarm events carry for a composite alarm:
// {"triggeringAlarms":[{"arn":…,"state":{"value":…,"timestamp":…}}]}.
func cwTriggeringAlarmsReasonData(triggers []cwChildTransition) string {
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
	for _, ct := range triggers {
		out.TriggeringAlarms = append(out.TriggeringAlarms, trigger{Arn: cwAlarmArn(ct.Name),
			State: childState{Value: ct.State, Timestamp: cwAlarmEventTime(ct.At)}})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// cwCompositeAlarmTransition describes a composite alarm, as it stood before,
// moving from prev to newState.
func cwCompositeAlarmTransition(before CWCompositeAlarm, prev, newState, reason, reasonData string, triggering []string, at time.Time) cwAlarmTransition {
	if triggering == nil {
		triggering = []string{}
	}
	payload := map[string]any{
		"AlarmName":               before.AlarmName,
		"AlarmDescription":        before.AlarmDescription,
		"AWSAccountId":            awsAccountID(),
		"NewStateValue":           newState,
		"NewStateReason":          reason,
		"StateChangeTime":         at.UTC().Format("2006-01-02T15:04:05.000Z"),
		"Region":                  awsRegion(),
		"AlarmArn":                before.AlarmArn,
		"OKActions":               before.OKActions,
		"AlarmActions":            before.AlarmActions,
		"InsufficientDataActions": before.InsufficientDataActions,
		"OldStateValue":           prev,
		"AlarmRule":               before.AlarmRule,
		"TriggeringChildren":      triggering,
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
		Configuration:  map[string]any{"alarmRule": before.AlarmRule},
		Previous:       cwAlarmEventState{Value: prev, Reason: before.StateReason, Timestamp: cwAlarmEventUnixTime(before.UpdatedTimestamp, at)},
		State:          cwAlarmEventState{Value: newState, Reason: reason, ReasonData: reasonData, Timestamp: cwAlarmEventTime(at)},
		ActionConfig:   before.ActionConfiguration,
		Tags:           before.Tags,
	}
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
