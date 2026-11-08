package main

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CloudWatch sends every alarm state change and every alarm create, update and
// delete to the default event bus, runs the new state's Amazon SNS and AWS
// Lambda actions, and with tag propagation enabled carries the alarm's tags
// in all three (the CloudWatch User Guide's "Alarm events and EventBridge",
// "Invoke a Lambda function from an alarm" and "Alarm tags in action
// payloads").

// cwAlarmEventTime is the timestamp shape the alarm payloads carry:
// 2019-10-02T17:04:40.989+0000.
func cwAlarmEventTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000-0700")
}

func cwAlarmEventUnixTime(sec int64, fallback time.Time) string {
	if sec > 0 {
		return cwAlarmEventTime(time.Unix(sec, 0))
	}
	return cwAlarmEventTime(fallback)
}

// cwPropagatedAlarmTags answers the tags a payload carries and their status,
// or ok false when the alarm does not propagate its tags. Keys beginning with
// aws: are never propagated.
func cwPropagatedAlarmTags(ac *cwActionConfiguration, tags map[string]string) (out map[string]string, status string, ok bool) {
	if ac == nil || ac.AlarmTagsPropagation == nil || ac.AlarmTagsPropagation.Enabled == nil || !*ac.AlarmTagsPropagation.Enabled {
		return nil, "", false
	}
	if len(tags) == 0 {
		return map[string]string{}, "NO_TAGS", true
	}
	p := ac.AlarmTagsPropagation
	include := map[string]bool{}
	for _, k := range p.IncludedTagKeys {
		include[k] = true
	}
	exclude := map[string]bool{}
	for _, k := range p.ExcludedTagKeys {
		exclude[k] = true
	}
	out = map[string]string{}
	for k, v := range tags {
		if strings.HasPrefix(k, "aws:") || exclude[k] {
			continue
		}
		if len(include) > 0 && !include[k] {
			continue
		}
		out[k] = v
	}
	return out, "SUCCESS", true
}

// cwAlarmTagsList is the tag list Amazon SNS (Key, Value) and AWS Lambda (key,
// value) payloads carry, ordered by key.
func cwAlarmTagsList(tags map[string]string, keyField, valueField string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]string{keyField: k, valueField: tags[k]})
	}
	return out
}

// cwAddNotificationTags adds AlarmTags and AlarmTagsStatus to an Amazon SNS
// notification payload when the alarm propagates its tags.
func cwAddNotificationTags(payload map[string]any, ac *cwActionConfiguration, tags map[string]string) {
	if out, status, ok := cwPropagatedAlarmTags(ac, tags); ok {
		payload["AlarmTags"] = cwAlarmTagsList(out, "Key", "Value")
		payload["AlarmTagsStatus"] = status
	}
}

type cwAlarmEventState struct {
	Value      string `json:"value"`
	Reason     string `json:"reason,omitempty"`
	ReasonData string `json:"reasonData,omitempty"`
	Timestamp  string `json:"timestamp"`
}

// cwAlarmTransition is one alarm's change of state and what CloudWatch sends
// for it.
type cwAlarmTransition struct {
	Name, Arn      string
	ActionsEnabled bool
	Targets        []string
	Subject        string
	SNSMessage     string
	// The configuration the state-change event and the Lambda payload carry.
	Configuration   map[string]any
	Previous, State cwAlarmEventState
	ActionConfig    *cwActionConfiguration
	Tags            map[string]string
}

// run sends the state-change event and, with actions enabled, runs every
// target of the new state: an Amazon SNS topic receives SNSMessage, an AWS
// Lambda function that grants lambda.alarms.cloudwatch.amazonaws.com
// lambda:InvokeFunction is invoked asynchronously with the alarm payload.
func (t cwAlarmTransition) run() {
	detail := map[string]any{
		"alarmName":     t.Name,
		"configuration": t.Configuration,
		"previousState": t.Previous,
		"state":         t.State,
	}
	tags, tagsStatus, propagate := cwPropagatedAlarmTags(t.ActionConfig, t.Tags)
	if propagate {
		detail["alarmTags"] = tags
		detail["alarmTagsStatus"] = tagsStatus
	}
	if body, err := json.Marshal(detail); err == nil {
		ebPutServiceEvent("aws.cloudwatch", "CloudWatch Alarm State Change", string(body), []string{t.Arn})
	}
	if !t.ActionsEnabled || len(t.Targets) == 0 {
		return
	}
	alarmData := map[string]any{
		"alarmName":     t.Name,
		"state":         t.State,
		"previousState": t.Previous,
		"configuration": t.Configuration,
	}
	if propagate {
		alarmData["alarmTags"] = cwAlarmTagsList(tags, "key", "value")
		alarmData["alarmTagsStatus"] = tagsStatus
	}
	lambdaPayload, err := json.Marshal(map[string]any{
		"source":    "aws.cloudwatch",
		"alarmArn":  t.Arn,
		"accountId": awsAccountID(),
		"time":      t.State.Timestamp,
		"region":    awsRegion(),
		"alarmData": alarmData,
	})
	if err != nil {
		return
	}
	for _, target := range t.Targets {
		switch {
		case strings.HasPrefix(target, "arn:aws:sns:"):
			cwPublishAlarmNotification(t.Name, target, t.Subject, t.SNSMessage)
		case strings.HasPrefix(target, "arn:aws:lambda:"):
			cwInvokeAlarmLambda(t.Name, t.Arn, target, lambdaPayload)
		}
	}
}

func cwInvokeAlarmLambda(alarmName, alarmArn, functionARN string, payload []byte) {
	src := iamServiceSource{Service: "lambda.alarms.cloudwatch.amazonaws.com", SourceArn: alarmArn, SourceAccount: awsAccountID()}
	if !iamAuthorizeServiceDelivery(functionARN, "lambda:InvokeFunction", src) {
		cwEvalLogger.Info().Str("alarmName", alarmName).Str("functionARN", functionARN).Msg("CloudWatch alarm action function does not grant lambda:InvokeFunction")
		return
	}
	fn, qualifier, ok := lambdaResolveInvocationTarget(functionARN, "")
	if !ok {
		cwEvalLogger.Info().Str("alarmName", alarmName).Str("functionARN", functionARN).Msg("CloudWatch alarm action function not found")
		return
	}
	lambdaInvokeAsynchronously(fn, payload, qualifier)
}

// cwPutAlarmConfigurationChangeEvent sends "CloudWatch Alarm Configuration
// Change" for a create, update or delete; an update carries the previous
// configuration too.
func cwPutAlarmConfigurationChangeEvent(alarmName, alarmArn, operation string, state cwAlarmEventState, configuration, previous map[string]any) {
	detail := map[string]any{
		"alarmName":     alarmName,
		"operation":     operation,
		"state":         map[string]string{"value": state.Value, "timestamp": state.Timestamp},
		"configuration": configuration,
	}
	if previous != nil {
		detail["previousConfiguration"] = previous
	}
	if body, err := json.Marshal(detail); err == nil {
		ebPutServiceEvent("aws.cloudwatch", "CloudWatch Alarm Configuration Change", string(body), []string{alarmArn})
	}
}

// The metric alarm's metrics as a metricStat query.
func cwMetricAlarmEventMetrics(a CWAlarm) []map[string]any {
	dims := map[string]string{}
	for _, d := range a.Dimensions {
		dims[d.Name] = d.Value
	}
	stat := a.Statistic
	if a.ExtendedStatistic != "" {
		stat = a.ExtendedStatistic
	}
	if stat == "" {
		stat = "Average"
	}
	return []map[string]any{{
		"id": cwAlarmEventMetricID(a.AlarmArn),
		"metricStat": map[string]any{
			"metric": map[string]any{
				"dimensions": dims,
				"name":       a.MetricName,
				"namespace":  a.Namespace,
			},
			"period": a.Period,
			"stat":   stat,
		},
		"returnData": true,
	}}
}

// The metric alarm's configuration as its state-change event shows it.
func cwMetricAlarmEventConfiguration(a CWAlarm) map[string]any {
	return map[string]any{"description": a.AlarmDescription, "metrics": cwMetricAlarmEventMetrics(a)}
}

// The metric alarm's configuration as its configuration change event shows
// it.
func cwMetricAlarmConfigurationDetail(a CWAlarm, updated time.Time) map[string]any {
	cfg := map[string]any{
		"evaluationPeriods":       a.EvaluationPeriods,
		"threshold":               a.Threshold,
		"comparisonOperator":      a.ComparisonOperator,
		"metrics":                 cwMetricAlarmEventMetrics(a),
		"alarmName":               a.AlarmName,
		"description":             a.AlarmDescription,
		"actionsEnabled":          a.ActionsEnabled,
		"timestamp":               cwAlarmEventTime(updated),
		"okActions":               cwNonNilStrings(a.OKActions),
		"alarmActions":            cwNonNilStrings(a.AlarmActions),
		"insufficientDataActions": cwNonNilStrings(a.InsufficientDataActions),
	}
	if a.TreatMissingData != "" {
		cfg["treatMissingData"] = a.TreatMissingData
	}
	return cfg
}

func cwCompositeAlarmConfigurationDetail(ca CWCompositeAlarm, updated time.Time) map[string]any {
	return map[string]any{
		"alarmRule":               ca.AlarmRule,
		"alarmName":               ca.AlarmName,
		"description":             ca.AlarmDescription,
		"actionsEnabled":          ca.ActionsEnabled,
		"timestamp":               cwAlarmEventTime(updated),
		"okActions":               cwNonNilStrings(ca.OKActions),
		"alarmActions":            cwNonNilStrings(ca.AlarmActions),
		"insufficientDataActions": cwNonNilStrings(ca.InsufficientDataActions),
	}
}

// The log alarm's configuration as its state-change and configuration change
// events show it.
func cwLogAlarmConfigurationDetail(a CWLogAlarm, updated time.Time) map[string]any {
	cfg := map[string]any{
		"logGroupIdentifiers":     cwNonNilStrings(a.LogGroupIdentifiers),
		"queryString":             a.QueryString,
		"aggregationExpression":   a.AggregationExpression,
		"scheduledQueryRoleARN":   a.ScheduledQueryRoleARN,
		"schedule":                map[string]any{"expression": a.ScheduleExpression, "startTimeOffset": a.StartTimeOffset, "endTimeOffset": a.EndTimeOffset},
		"threshold":               a.Threshold,
		"comparisonOperator":      a.ComparisonOperator,
		"queryResultsToEvaluate":  a.QueryResultsToEvaluate,
		"queryResultsToAlarm":     a.QueryResultsToAlarm,
		"alarmName":               a.AlarmName,
		"description":             a.AlarmDescription,
		"actionsEnabled":          a.ActionsEnabled,
		"timestamp":               cwAlarmEventTime(updated),
		"okActions":               cwNonNilStrings(a.OKActions),
		"alarmActions":            cwNonNilStrings(a.AlarmActions),
		"insufficientDataActions": cwNonNilStrings(a.InsufficientDataActions),
	}
	if a.TreatMissingData != "" {
		cfg["treatMissingData"] = a.TreatMissingData
	}
	if a.ActionLogLineRoleArn != "" {
		cfg["actionLogLineRoleArn"] = a.ActionLogLineRoleArn
	}
	if a.ActionLogLineCount != nil {
		cfg["actionLogLineCount"] = *a.ActionLogLineCount
	}
	return cfg
}

func cwNonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// The query's id, an opaque UUID in CloudWatch's events, is stable per alarm.
func cwAlarmEventMetricID(alarmArn string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(alarmArn)).String()
}
