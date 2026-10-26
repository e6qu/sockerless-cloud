package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon CloudWatch re-evaluates each metric alarm against incoming datapoints
// and, on a transition, publishes the alarm notification to the action topics.
// The evaluator re-derives every alarm's state each tick and publishes through
// the real Amazon SNS path (snsFanout).

// PutMetricAlarm replaces the alarm record, so a new or updated alarm starts
// with an empty StateValue, which the evaluator treats as INSUFFICIENT_DATA.

func startCWAlarmEvaluator(srv *sim.Server) {
	srv.StartBackground("CloudWatch alarm evaluator", func(ctx context.Context) {
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

// A read derives state itself, so this cadence governs only how promptly
// actions fire.
const cwAlarmEvalInterval = 250 * time.Millisecond

// A manual SetAlarmState does not dispatch actions here. Recover a panic per
// alarm so one bad alarm cannot stop delivery for the rest.
func cwEvaluateAlarmsOnce() {
	for _, a := range cwAlarms.List() {
		cwEvaluateAlarm(a)
	}
}

// Only the background sweep calls this: dispatching from a read would let
// every DescribeAlarms fire alarm actions and the Amazon ECS reconciliation
// behind them.
func cwEvaluateAlarm(a CWAlarm) {
	func(alarm CWAlarm) {
		defer func() {
			if r := recover(); r != nil {
				cwEvalLogger.Error().Str("alarmName", alarm.AlarmName).Interface("recover", r).Msg("CloudWatch alarm evaluator panic recovered")
			}
		}()
		// Hold the alarm store lock so a concurrent PutMetricAlarm cannot leave a
		// recreated alarm with a stale StateValue that never dispatches.
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

// Skip a topic ARN that no longer resolves, as CloudWatch drops a deleted
// action target.
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
		snsFanout(topicARN, sim.NewUUID(), subject, message, nil)
	}
}

// The SNS Message an Amazon SQS subscriber parses.
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

// "<STATE>: \"<AlarmName>\" in <Region>", as CloudWatch words it.
func cwAlarmNotificationSubject(state, name string) string {
	return fmt.Sprintf("%s: %q in %s", state, name, awsRegion())
}
