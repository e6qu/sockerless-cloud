# Sim surface — aws-cloudwatch

Surface registered in `simulator-aws/cloudwatch.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did. It does not follow that the answer is built from what it read: a handler that looks its parent up and then answers a fixed body reaches state and is marked ✓
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action Logs_20140328.CreateLogGroup` | ✓ `simulator-aws/cloudwatch.go:262::handleCWCreateLogGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeLogGroups` | ✓ `simulator-aws/cloudwatch.go:263::handleCWDescribeLogGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteLogGroup` | ✓ `simulator-aws/cloudwatch.go:264::handleCWDeleteLogGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.CreateLogStream` | ✓ `simulator-aws/cloudwatch.go:265::handleCWCreateLogStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeLogStreams` | ✓ `simulator-aws/cloudwatch.go:266::handleCWDescribeLogStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutLogEvents` | ✓ `simulator-aws/cloudwatch.go:267::handleCWPutLogEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetLogEvents` | ✓ `simulator-aws/cloudwatch.go:268::handleCWGetLogEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.FilterLogEvents` | ✓ `simulator-aws/cloudwatch.go:269::handleCWFilterLogEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutRetentionPolicy` | ✓ `simulator-aws/cloudwatch.go:270::handleCWPutRetentionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.ListTagsForResource` | ✓ `simulator-aws/cloudwatch.go:271::handleCWListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.TagResource` | ✓ `simulator-aws/cloudwatch.go:272::handleCWTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.AssociateKmsKey` | ✓ `simulator-aws/cloudwatch.go:273::handleCWAssociateKmsKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DisassociateKmsKey` | ✓ `simulator-aws/cloudwatch.go:274::handleCWDisassociateKmsKey` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.EnableAlarmActions` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:156::handleCWJSONEnableAlarmActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DisableAlarmActions` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:157::handleCWJSONDisableAlarmActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.SetAlarmState` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:158::handleCWJSONSetAlarmState` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DescribeAlarmHistory` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:159::handleCWJSONDescribeAlarmHistory` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DescribeAlarmsForMetric` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:160::handleCWJSONDescribeAlarmsForMetric` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutCompositeAlarm` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:161::handleCWJSONPutCompositeAlarm` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DeleteAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DeleteAlarms` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DeleteAnomalyDetector` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DeleteDashboards` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DeleteInsightRules` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DeleteMetricStream` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DescribeAlarmHistory` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DescribeAlarms` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DescribeAlarmsForMetric` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DescribeAnomalyDetectors` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DescribeInsightRules` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DisableAlarmActions` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/DisableInsightRules` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/EnableAlarmActions` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/EnableInsightRules` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/GetAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/GetDashboard` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/GetMetricData` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/GetMetricStream` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/ListAlarmMuteRules` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/ListDashboards` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/ListMetricStreams` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/ListTagsForResource` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutAnomalyDetector` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutCompositeAlarm` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutDashboard` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutInsightRule` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutLogAlarm` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutMetricAlarm` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutMetricData` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/PutMetricStream` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/SetAlarmState` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/StartMetricStreams` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/StopMetricStreams` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/TagResource` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /service/GraniteServiceVersion20100801/operation/UntagResource` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:381::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action EnableAlarmActions` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:579::handleCWQueryEnableAlarmActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DisableAlarmActions` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:580::handleCWQueryDisableAlarmActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SetAlarmState` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:581::handleCWQuerySetAlarmState` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeAlarmHistory` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:582::handleCWQueryDescribeAlarmHistory` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeAlarmsForMetric` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:583::handleCWQueryDescribeAlarmsForMetric` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutCompositeAlarm` | ✓ `simulator-aws/cloudwatch_alarm_ops.go:584::handleCWQueryPutCompositeAlarm` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutMetricAlarm` | ✓ `simulator-aws/cloudwatch_alarms.go:230::handleCWJSONPutMetricAlarm` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DescribeAlarms` | ✓ `simulator-aws/cloudwatch_alarms.go:231::handleCWJSONDescribeAlarms` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DeleteAlarms` | ✓ `simulator-aws/cloudwatch_alarms.go:232::handleCWJSONDeleteAlarms` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutMetricAlarm` | ✓ `simulator-aws/cloudwatch_alarms.go:811::handleCWQueryPutMetricAlarm` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeAlarms` | ✓ `simulator-aws/cloudwatch_alarms.go:812::handleCWQueryDescribeAlarms` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteAlarms` | ✓ `simulator-aws/cloudwatch_alarms.go:813::handleCWQueryDeleteAlarms` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutAnomalyDetector` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:498::handleCWQueryPutAnomalyDetector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeAnomalyDetectors` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:499::handleCWQueryDescribeAnomalyDetectors` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteAnomalyDetector` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:500::handleCWQueryDeleteAnomalyDetector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutInsightRule` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:501::handleCWQueryPutInsightRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeInsightRules` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:502::handleCWQueryDescribeInsightRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action EnableInsightRules` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:503::handleCWQueryEnableInsightRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DisableInsightRules` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:504::handleCWQueryDisableInsightRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteInsightRules` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:505::handleCWQueryDeleteInsightRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutAnomalyDetector` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:85::handleCWJSONPutAnomalyDetector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DescribeAnomalyDetectors` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:86::handleCWJSONDescribeAnomalyDetectors` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DeleteAnomalyDetector` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:87::handleCWJSONDeleteAnomalyDetector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutInsightRule` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:88::handleCWJSONPutInsightRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DescribeInsightRules` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:89::handleCWJSONDescribeInsightRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.EnableInsightRules` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:90::handleCWJSONEnableInsightRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DisableInsightRules` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:91::handleCWJSONDisableInsightRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DeleteInsightRules` | ✓ `simulator-aws/cloudwatch_anomaly_insight.go:92::handleCWJSONDeleteInsightRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutDashboard` | ✓ `simulator-aws/cloudwatch_dashboards.go:231::handleCWQueryPutDashboard` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetDashboard` | ✓ `simulator-aws/cloudwatch_dashboards.go:232::handleCWQueryGetDashboard` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListDashboards` | ✓ `simulator-aws/cloudwatch_dashboards.go:233::handleCWQueryListDashboards` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDashboards` | ✓ `simulator-aws/cloudwatch_dashboards.go:234::handleCWQueryDeleteDashboards` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutDashboard` | ✓ `simulator-aws/cloudwatch_dashboards.go:61::handleCWJSONPutDashboard` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.GetDashboard` | ✓ `simulator-aws/cloudwatch_dashboards.go:62::handleCWJSONGetDashboard` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.ListDashboards` | ✓ `simulator-aws/cloudwatch_dashboards.go:63::handleCWJSONListDashboards` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DeleteDashboards` | ✓ `simulator-aws/cloudwatch_dashboards.go:64::handleCWJSONDeleteDashboards` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.StartQuery` | ✓ `simulator-aws/cloudwatch_insights.go:34::handleCWStartQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetQueryResults` | ✓ `simulator-aws/cloudwatch_insights.go:35::handleCWGetQueryResults` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.StopQuery` | ✓ `simulator-aws/cloudwatch_insights.go:36::handleCWStopQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeQueries` | ✓ `simulator-aws/cloudwatch_insights.go:37::handleCWDescribeQueries` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutLogAlarm` | ✓ `simulator-aws/cloudwatch_log_alarms.go:59::handleCWJSONPutLogAlarm` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutAccountPolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:153::handleCWPutAccountPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeAccountPolicies` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:154::handleCWDescribeAccountPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteAccountPolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:155::handleCWDeleteAccountPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutQueryDefinition` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:157::handleCWPutQueryDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeQueryDefinitions` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:158::handleCWDescribeQueryDefinitions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteQueryDefinition` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:159::handleCWDeleteQueryDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutResourcePolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:161::handleCWPutResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeResourcePolicies` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:162::handleCWDescribeResourcePolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteResourcePolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:163::handleCWDeleteResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutDestination` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:165::handleCWPutDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeDestinations` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:166::handleCWDescribeDestinations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteDestination` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:167::handleCWDeleteDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutDestinationPolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:168::handleCWPutDestinationPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.CreateDelivery` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:170::handleCWCreateDelivery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetDelivery` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:171::handleCWGetDelivery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteDelivery` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:172::handleCWDeleteDelivery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeDeliveries` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:173::handleCWDescribeDeliveries` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutDeliverySource` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:175::handleCWPutDeliverySource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetDeliverySource` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:176::handleCWGetDeliverySource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeDeliverySources` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:177::handleCWDescribeDeliverySources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteDeliverySource` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:178::handleCWDeleteDeliverySource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutDeliveryDestination` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:180::handleCWPutDeliveryDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetDeliveryDestination` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:181::handleCWGetDeliveryDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeDeliveryDestinations` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:182::handleCWDescribeDeliveryDestinations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteDeliveryDestination` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:183::handleCWDeleteDeliveryDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutDeliveryDestinationPolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:184::handleCWPutDeliveryDestinationPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetDeliveryDestinationPolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:185::handleCWGetDeliveryDestinationPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteDeliveryDestinationPolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:186::handleCWDeleteDeliveryDestinationPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.CreateLogAnomalyDetector` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:188::handleCWCreateLogAnomalyDetector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetLogAnomalyDetector` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:189::handleCWGetLogAnomalyDetector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.ListLogAnomalyDetectors` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:190::handleCWListLogAnomalyDetectors` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteLogAnomalyDetector` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:191::handleCWDeleteLogAnomalyDetector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutIndexPolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:193::handleCWPutIndexPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteIndexPolicy` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:194::handleCWDeleteIndexPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeIndexPolicies` | ✓ `simulator-aws/cloudwatch_logs_extra2.go:195::handleCWDescribeIndexPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeFieldIndexes` | ○ `simulator-aws/cloudwatch_logs_extra2.go:196::handleCWDescribeFieldIndexes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeConfigurationTemplates` | ○ `simulator-aws/cloudwatch_logs_extra2.go:199::handleCWDescribeConfigurationTemplates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.StartLiveTail` | ✓ `simulator-aws/cloudwatch_logs_extra5.go:20::handleCWStartLiveTail` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetLogObject` | ✓ `simulator-aws/cloudwatch_logs_extra5.go:21::handleCWGetLogObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetDataProtectionPolicy` | ✓ `simulator-aws/cloudwatch_logs_ops.go:100::handleCWGetDataProtectionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteDataProtectionPolicy` | ✓ `simulator-aws/cloudwatch_logs_ops.go:101::handleCWDeleteDataProtectionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteLogStream` | ✓ `simulator-aws/cloudwatch_logs_ops.go:83::handleCWDeleteLogStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutMetricFilter` | ✓ `simulator-aws/cloudwatch_logs_ops.go:84::handleCWPutMetricFilter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeMetricFilters` | ✓ `simulator-aws/cloudwatch_logs_ops.go:85::handleCWDescribeMetricFilters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteMetricFilter` | ✓ `simulator-aws/cloudwatch_logs_ops.go:86::handleCWDeleteMetricFilter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.TestMetricFilter` | ✓ `simulator-aws/cloudwatch_logs_ops.go:87::handleCWTestMetricFilter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutSubscriptionFilter` | ✓ `simulator-aws/cloudwatch_logs_ops.go:88::handleCWPutSubscriptionFilter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeSubscriptionFilters` | ✓ `simulator-aws/cloudwatch_logs_ops.go:89::handleCWDescribeSubscriptionFilters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteSubscriptionFilter` | ✓ `simulator-aws/cloudwatch_logs_ops.go:90::handleCWDeleteSubscriptionFilter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteRetentionPolicy` | ✓ `simulator-aws/cloudwatch_logs_ops.go:91::handleCWDeleteRetentionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.TagLogGroup` | ✓ `simulator-aws/cloudwatch_logs_ops.go:92::handleCWTagLogGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.UntagLogGroup` | ✓ `simulator-aws/cloudwatch_logs_ops.go:93::handleCWUntagLogGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.ListTagsLogGroup` | ✓ `simulator-aws/cloudwatch_logs_ops.go:94::handleCWListTagsLogGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.UntagResource` | ✓ `simulator-aws/cloudwatch_logs_ops.go:95::handleCWUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.CreateExportTask` | ✓ `simulator-aws/cloudwatch_logs_ops.go:96::handleCWCreateExportTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DescribeExportTasks` | ✓ `simulator-aws/cloudwatch_logs_ops.go:97::handleCWDescribeExportTasks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.CancelExportTask` | ✓ `simulator-aws/cloudwatch_logs_ops.go:98::handleCWCancelExportTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutDataProtectionPolicy` | ✓ `simulator-aws/cloudwatch_logs_ops.go:99::handleCWPutDataProtectionPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutStorageTierPolicy` | ✓ `simulator-aws/cloudwatch_logs_syslog.go:34::handleCWPutStorageTierPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.GetStorageTierPolicy` | ✓ `simulator-aws/cloudwatch_logs_syslog.go:35::handleCWGetStorageTierPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.PutSyslogConfiguration` | ✓ `simulator-aws/cloudwatch_logs_syslog.go:36::handleCWPutSyslogConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.ListSyslogConfigurations` | ✓ `simulator-aws/cloudwatch_logs_syslog.go:37::handleCWListSyslogConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action Logs_20140328.DeleteSyslogConfiguration` | ✓ `simulator-aws/cloudwatch_logs_syslog.go:38::handleCWDeleteSyslogConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutMetricStream` | ✓ `simulator-aws/cloudwatch_metric_streams.go:205::handleCWJSONPutMetricStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.GetMetricStream` | ✓ `simulator-aws/cloudwatch_metric_streams.go:206::handleCWJSONGetMetricStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DeleteMetricStream` | ✓ `simulator-aws/cloudwatch_metric_streams.go:207::handleCWJSONDeleteMetricStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.ListMetricStreams` | ✓ `simulator-aws/cloudwatch_metric_streams.go:208::handleCWJSONListMetricStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.StartMetricStreams` | ✓ `simulator-aws/cloudwatch_metric_streams.go:209::handleCWJSONStartMetricStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.StopMetricStreams` | ✓ `simulator-aws/cloudwatch_metric_streams.go:210::handleCWJSONStopMetricStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutMetricStream` | ✓ `simulator-aws/cloudwatch_metric_streams.go:477::handleCWQueryPutMetricStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetMetricStream` | ✓ `simulator-aws/cloudwatch_metric_streams.go:478::handleCWQueryGetMetricStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteMetricStream` | ✓ `simulator-aws/cloudwatch_metric_streams.go:479::handleCWQueryDeleteMetricStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListMetricStreams` | ✓ `simulator-aws/cloudwatch_metric_streams.go:480::handleCWQueryListMetricStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartMetricStreams` | ✓ `simulator-aws/cloudwatch_metric_streams.go:481::handleCWQueryStartMetricStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StopMetricStreams` | ✓ `simulator-aws/cloudwatch_metric_streams.go:482::handleCWQueryStopMetricStreams` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutMetricData` | ✓ `simulator-aws/cloudwatch_metrics_json.go:29::handleCWJSONPutMetricData` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.GetMetricStatistics` | ✓ `simulator-aws/cloudwatch_metrics_json.go:30::handleCWJSONGetMetricStatistics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.ListMetrics` | ✓ `simulator-aws/cloudwatch_metrics_json.go:31::handleCWJSONListMetrics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutMetricData` | ✓ `simulator-aws/cloudwatch_metrics_query.go:23::handleCWQueryPutMetricData` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetMetricStatistics` | ✓ `simulator-aws/cloudwatch_metrics_query.go:24::handleCWQueryGetMetricStatistics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListMetrics` | ✓ `simulator-aws/cloudwatch_metrics_query.go:25::handleCWQueryListMetrics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetMetricData` | ✓ `simulator-aws/cloudwatch_misc_ops.go:509::handleCWQueryGetMetricData` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PutAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_misc_ops.go:510::handleCWQueryPutAlarmMuteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GetAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_misc_ops.go:511::handleCWQueryGetAlarmMuteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_misc_ops.go:512::handleCWQueryDeleteAlarmMuteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListAlarmMuteRules` | ✓ `simulator-aws/cloudwatch_misc_ops.go:513::handleCWQueryListAlarmMuteRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TagResource` | ✓ `simulator-aws/cloudwatch_misc_ops.go:514::handleCWQueryTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action UntagResource` | ✓ `simulator-aws/cloudwatch_misc_ops.go:515::handleCWQueryUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListTagsForResource` | ✓ `simulator-aws/cloudwatch_misc_ops.go:516::handleCWQueryListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.GetMetricData` | ✓ `simulator-aws/cloudwatch_misc_ops.go:72::handleCWJSONGetMetricData` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.PutAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_misc_ops.go:73::handleCWJSONPutAlarmMuteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.GetAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_misc_ops.go:74::handleCWJSONGetAlarmMuteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.DeleteAlarmMuteRule` | ✓ `simulator-aws/cloudwatch_misc_ops.go:75::handleCWJSONDeleteAlarmMuteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.ListAlarmMuteRules` | ✓ `simulator-aws/cloudwatch_misc_ops.go:76::handleCWJSONListAlarmMuteRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.TagResource` | ✓ `simulator-aws/cloudwatch_misc_ops.go:77::handleCWJSONTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.UntagResource` | ✓ `simulator-aws/cloudwatch_misc_ops.go:78::handleCWJSONUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action GraniteServiceVersion20100801.ListTagsForResource` | ✓ `simulator-aws/cloudwatch_misc_ops.go:79::handleCWJSONListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
