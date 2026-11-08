package main

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CloudWatch sends every alarm state change to the default event bus as a
// "CloudWatch Alarm State Change" event, and with tag propagation enabled
// carries the alarm's tags in the event and in its Amazon SNS notifications
// (the CloudWatch User Guide's "Alarm events and EventBridge" and "Alarm tags
// in action payloads").

// cwAlarmEventTime is the timestamp shape the alarm events carry:
// 2019-10-02T17:04:40.989+0000.
func cwAlarmEventTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000-0700")
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

// cwAlarmTagsList is the SNS notification's AlarmTags: a list of Key and Value
// objects, ordered by key.
func cwAlarmTagsList(tags map[string]string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]string{"Key": k, "Value": tags[k]})
	}
	return out
}

// cwAddNotificationTags adds AlarmTags and AlarmTagsStatus to an SNS
// notification payload when the alarm propagates its tags.
func cwAddNotificationTags(payload map[string]any, ac *cwActionConfiguration, tags map[string]string) {
	if out, status, ok := cwPropagatedAlarmTags(ac, tags); ok {
		payload["AlarmTags"] = cwAlarmTagsList(out)
		payload["AlarmTagsStatus"] = status
	}
}

type cwAlarmEventState struct {
	Value      string `json:"value"`
	Reason     string `json:"reason,omitempty"`
	ReasonData string `json:"reasonData,omitempty"`
	Timestamp  string `json:"timestamp"`
}

func cwPutAlarmStateChangeEvent(alarmName, alarmArn string, configuration map[string]any, previous, current cwAlarmEventState, ac *cwActionConfiguration, tags map[string]string) {
	detail := map[string]any{
		"alarmName":     alarmName,
		"configuration": configuration,
		"previousState": previous,
		"state":         current,
	}
	if out, status, ok := cwPropagatedAlarmTags(ac, tags); ok {
		detail["alarmTags"] = out
		detail["alarmTagsStatus"] = status
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return
	}
	ebPutServiceEvent("aws.cloudwatch", "CloudWatch Alarm State Change", string(body), []string{alarmArn})
}

// cwPutMetricAlarmStateChangeEvent sends the event for a metric alarm that moved
// from before's state to newState.
func cwPutMetricAlarmStateChangeEvent(before CWAlarm, prev, newState, reason string, at time.Time) {
	previous := cwAlarmEventState{Value: prev, Reason: before.StateReason, Timestamp: cwAlarmEventTime(at)}
	if before.StateUpdatedTimestamp > 0 {
		previous.Timestamp = cwAlarmEventTime(time.Unix(before.StateUpdatedTimestamp, 0))
	}
	cwPutAlarmStateChangeEvent(before.AlarmName, before.AlarmArn, cwMetricAlarmEventConfiguration(before), previous,
		cwAlarmEventState{Value: newState, Reason: reason, Timestamp: cwAlarmEventTime(at)},
		before.ActionConfiguration, before.Tags)
}

// The metric alarm's configuration as its state-change event shows it: the
// alarm's single metric as a metricStat query.
func cwMetricAlarmEventConfiguration(a CWAlarm) map[string]any {
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
	return map[string]any{
		"description": a.AlarmDescription,
		"metrics": []map[string]any{{
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
		}},
	}
}

// The query's id, an opaque UUID in CloudWatch's events, is stable per alarm.
func cwAlarmEventMetricID(alarmArn string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(alarmArn)).String()
}
