package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudWatch continuously re-evaluates each metric alarm against incoming
// datapoints and, on a state transition, invokes the configured action
// topics (AlarmActions / OKActions / InsufficientDataActions) with the
// canonical alarm notification JSON. The simulator mirrors that with a
// background evaluator started once during CloudWatch registration: every
// tick it re-derives every alarm's state from the live metric store, and
// when the derived state differs from the last dispatched state it fans
// the notification out through the real SNS publish path (snsFanout), so
// SQS / Lambda subscribers receive it exactly as a client-side Publish
// would deliver.

// The last dispatched state is stored on each alarm's StateValue field.
// PutMetricAlarm replaces the alarm record, so a new or updated alarm
// naturally starts with an empty StateValue that the evaluator treats as
// INSUFFICIENT_DATA. This is more robust than a separate map that must be
// manually reset on every PutMetricAlarm path.

// startCWAlarmEvaluator launches the background goroutine that re-derives
// every metric alarm's state every cwAlarmEvalInterval and dispatches the
// configured action topics on state transitions.
func startCWAlarmEvaluator(srv *sim.Server) {
	srv.StartBackground(func(ctx context.Context) {
		ticker := time.NewTicker(cwAlarmEvalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cwEvaluateAlarmsOnce()
			}
		}
	})
}

// How often the evaluator re-derives every alarm and dispatches the transitions
// it finds. A read does not wait on this — it derives the state itself — so the
// cadence governs only how promptly actions fire.
const cwAlarmEvalInterval = 250 * time.Millisecond

// cwEvaluateAlarmsOnce is one evaluator pass: snapshot every metric alarm,
// re-derive its state, and on a transition dispatch actions, record history,
// and persist the dispatched state. Manual SetAlarmState overrides do not
// dispatch actions here — SetAlarmState is a testing/admin knob that real
// CloudWatch does not route through AlarmActions.
//
// A panic while evaluating or dispatching a single alarm is recovered so
// one misbehaving alarm/resource cannot kill the background evaluator and
// silently break action delivery for all other alarms.
func cwEvaluateAlarmsOnce() {
	for _, a := range cwAlarms.List() {
		cwEvaluateAlarm(a)
	}
}

// cwEvaluateAlarm re-derives one alarm's state and, on a transition, dispatches
// its actions and records the new state. Only the background sweep calls it:
// dispatching from a read path would make every DescribeAlarms capable of
// firing alarm actions and the ECS reconciliation behind them.
func cwEvaluateAlarm(a CWAlarm) {
	func(alarm CWAlarm) {
		defer func() {
			if r := recover(); r != nil {
				cwEvalLogger.Error().Str("alarmName", alarm.AlarmName).Interface("recover", r).Msg("CloudWatch alarm evaluator panic recovered")
			}
		}()
		// Evaluate and dispatch under the alarm store lock so a
		// concurrent PutMetricAlarm replacement cannot race the
		// state read / dispatch / state write. Without this, a
		// freshly-recreated alarm could inherit a stale StateValue
		// from an in-flight evaluator tick and never dispatch.
		var prev, newState, reason string
		dispatched := false
		updated := cwAlarms.Update(alarm.AlarmName, func(x *CWAlarm) {
			newState, reason = cwEvaluateAlarmState(*x)
			prev = x.StateValue
			if prev == "" {
				prev = "INSUFFICIENT_DATA"
			}
			if prev == newState {
				return
			}
			cwDispatchAlarmActions(*x, prev, newState, reason)
			x.StateValue = newState
			x.StateReason = reason
			x.StateUpdatedTimestamp = time.Now().UTC().Unix()
			dispatched = true
		})
		if !updated || !dispatched {
			return
		}
		cwEvalLogger.Info().Str("alarmName", alarm.AlarmName).Str("oldState", prev).Str("newState", newState).Msg("CloudWatch alarm transitioned")
		historyData, _ := json.Marshal(map[string]string{
			"previousState": prev,
			"newState":      newState,
			"stateReason":   reason,
		})
		cwRecordAlarmHistory(alarm.AlarmName, "MetricAlarm", "StateUpdate",
			fmt.Sprintf("Alarm updated from %s to %s", prev, newState), string(historyData))
		ecsRequestServiceReconcileForAlarm(alarm.AlarmName)
	}(a)
}

// cwDispatchAlarmActions fans the alarm notification out to each configured
// action topic for the new state via the real SNS publish path. A topic
// ARN that doesn't resolve to a known SNS topic is skipped, matching real
// CloudWatch silently dropping a deleted action target.
func cwDispatchAlarmActions(a CWAlarm, oldState, newState, reason string) {
	if !a.ActionsEnabled {
		cwEvalLogger.Info().Str("alarmName", a.AlarmName).Str("newState", newState).Msg("CloudWatch alarm actions disabled")
		return
	}
	var targets []string
	switch newState {
	case "ALARM":
		targets = a.AlarmActions
	case "OK":
		targets = a.OKActions
	case "INSUFFICIENT_DATA":
		targets = a.InsufficientDataActions
	}
	if len(targets) == 0 {
		cwEvalLogger.Info().Str("alarmName", a.AlarmName).Str("newState", newState).Msg("CloudWatch alarm has no action targets for state")
		return
	}
	message := cwAlarmNotificationMessage(a, oldState, newState, reason)
	subject := cwAlarmNotificationSubject(newState, a.AlarmName)
	cwEvalLogger.Info().Str("alarmName", a.AlarmName).Str("newState", newState).Int("targetCount", len(targets)).Msg("CloudWatch alarm dispatching actions")
	for _, topicARN := range targets {
		name := snsTopicNameFromARN(topicARN)
		if _, ok := snsTopics.Get(name); !ok {
			cwEvalLogger.Info().Str("alarmName", a.AlarmName).Str("topicARN", topicARN).Msg("CloudWatch alarm action topic not found")
			continue
		}
		snsFanout(topicARN, generateUUID(), subject, message, nil)
	}
}

// cwAlarmNotificationMessage renders the canonical CloudWatch alarm
// notification JSON delivered as the SNS Message field — the shape an SQS
// subscriber parses to react to a threshold breach.
func cwAlarmNotificationMessage(a CWAlarm, oldState, newState, reason string) string {
	trigger := map[string]any{
		"Period":             a.Period,
		"EvaluationPeriods":  a.EvaluationPeriods,
		"Threshold":          a.Threshold,
		"ComparisonOperator": a.ComparisonOperator,
		"TreatMissingData":   a.TreatMissingData,
		"MetricName":         a.MetricName,
		"Namespace":          a.Namespace,
		"StatisticType":      "SingleStatistic",
	}
	if a.ExtendedStatistic != "" {
		trigger["ExtendedStatistic"] = a.ExtendedStatistic
	} else {
		stat := a.Statistic
		if stat == "" {
			stat = "Average"
		}
		trigger["Statistic"] = stat
	}
	if len(a.Dimensions) > 0 {
		dims := make([]map[string]string, 0, len(a.Dimensions))
		for _, d := range a.Dimensions {
			dims = append(dims, map[string]string{"Name": d.Name, "Value": d.Value})
		}
		trigger["Dimensions"] = dims
	}
	payload := map[string]any{
		"AlarmName":               a.AlarmName,
		"AlarmDescription":        a.AlarmDescription,
		"AWSAccountId":            awsAccountID(),
		"NewStateValue":           newState,
		"NewStateReason":          reason,
		"StateChangeTime":         time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"Region":                  awsRegion(),
		"OldStateValue":           oldState,
		"OKActions":               a.OKActions,
		"AlarmActions":            a.AlarmActions,
		"InsufficientDataActions": a.InsufficientDataActions,
		"Trigger":                 trigger,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"AlarmName":%q,"NewStateValue":%q,"OldStateValue":%q}`, a.AlarmName, newState, oldState)
	}
	return string(b)
}

// cwAlarmNotificationSubject renders the SNS Subject for an alarm
// notification — "<STATE>: \"<AlarmName>\" in <Region>" — matching the real
// CloudWatch subject line so topic subscribers can filter on it.
func cwAlarmNotificationSubject(state, name string) string {
	return fmt.Sprintf("%s: %q in %s", state, name, awsRegion())
}
