package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Shared query-protocol (legacy aws CLI / botocore) response helpers for the
// CloudWatch monitoring surface. The query envelope is
// <OpResponse><OpResult>…</OpResult><ResponseMetadata/></OpResponse>; empty
// operations omit the result element.

// cwQueryEmptyResponse writes the metadata-only response for an operation with
// no modeled output (EnableAlarmActions, SetAlarmState, …).
func cwQueryEmptyResponse(w http.ResponseWriter, op string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, cwQueryXmlns, generateUUID(), op)
}

// cwQueryResult writes the metadata-plus-result envelope for an operation with
// output; resultXML is the inner content of <OpResult>.
func cwQueryResult(w http.ResponseWriter, op, resultXML string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s><%sResult>%s</%sResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, cwQueryXmlns, op, resultXML, op, generateUUID(), op)
}

// cwQueryAppendf is a fmt.Sprintf-into-a-byte-slice helper for building XML
// member lists without a strings.Builder per call site.
func cwQueryAppendf(b []byte, format string, args ...any) []byte {
	return append(b, []byte(fmt.Sprintf(format, args...))...)
}

// cwQueryMetricAlarmsXML renders a list of metric alarms as the <member>…
// elements of a <MetricAlarms> list, deriving each alarm's state.
func cwQueryMetricAlarmsXML(alarms []CWAlarm, now time.Time) string {
	var members strings.Builder
	for _, a := range alarms {
		state, reason := cwAlarmEffectiveState(a)
		members.WriteString("<member>")
		fmt.Fprintf(&members, "<AlarmName>%s</AlarmName><AlarmArn>%s</AlarmArn>", xmlEscape(a.AlarmName), xmlEscape(a.AlarmArn))
		if a.AlarmDescription != "" {
			fmt.Fprintf(&members, "<AlarmDescription>%s</AlarmDescription>", xmlEscape(a.AlarmDescription))
		}
		fmt.Fprintf(&members, "<Namespace>%s</Namespace><MetricName>%s</MetricName>", xmlEscape(a.Namespace), xmlEscape(a.MetricName))
		members.WriteString("<Dimensions>")
		for _, dim := range a.Dimensions {
			fmt.Fprintf(&members, "<member><Name>%s</Name><Value>%s</Value></member>", xmlEscape(dim.Name), xmlEscape(dim.Value))
		}
		members.WriteString("</Dimensions>")
		if a.ExtendedStatistic != "" {
			fmt.Fprintf(&members, "<ExtendedStatistic>%s</ExtendedStatistic>", xmlEscape(a.ExtendedStatistic))
		} else if a.Statistic != "" {
			fmt.Fprintf(&members, "<Statistic>%s</Statistic>", xmlEscape(a.Statistic))
		}
		fmt.Fprintf(&members, "<Period>%d</Period><EvaluationPeriods>%d</EvaluationPeriods>", a.Period, a.EvaluationPeriods)
		fmt.Fprintf(&members, "<Threshold>%s</Threshold><ComparisonOperator>%s</ComparisonOperator>",
			cwFormatFloat(a.Threshold), xmlEscape(a.ComparisonOperator))
		if a.TreatMissingData != "" {
			fmt.Fprintf(&members, "<TreatMissingData>%s</TreatMissingData>", xmlEscape(a.TreatMissingData))
		}
		fmt.Fprintf(&members, "<ActionsEnabled>%t</ActionsEnabled>", a.ActionsEnabled)
		members.WriteString("<AlarmActions>")
		for _, act := range a.AlarmActions {
			fmt.Fprintf(&members, "<member>%s</member>", xmlEscape(act))
		}
		members.WriteString("</AlarmActions>")
		fmt.Fprintf(&members, "<StateValue>%s</StateValue><StateReason>%s</StateReason><StateUpdatedTimestamp>%s</StateUpdatedTimestamp>",
			state, xmlEscape(reason), now.Format(time.RFC3339))
		members.WriteString("</member>")
	}
	return members.String()
}
