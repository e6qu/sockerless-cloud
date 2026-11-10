package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// A scheduled query runs on its scheduleExpression (in its timezone, UTC by
// default) between scheduleStartTime and scheduleEndTime while ENABLED, as
// the CloudWatch Logs User Guide's scheduled queries chapter describes: it
// assumes executionRoleArn as logs.amazonaws.com, runs the query over the
// window startTimeOffset to endTimeOffset seconds before the trigger, delivers
// the rows to its Amazon S3 and lookup table destinations through their
// delivery roles, sends a "Scheduled Query Completed" event to the default
// event bus, and records the run in its history.

type CWScheduledQueryDestinationRun struct {
	DestinationType       string `json:"destinationType"`
	DestinationIdentifier string `json:"destinationIdentifier"`
	Status                string `json:"status"`
	ProcessedIdentifier   string `json:"processedIdentifier,omitempty"`
	ErrorMessage          string `json:"errorMessage,omitempty"`
}

type CWScheduledQueryRun struct {
	ScheduledQueryArn  string                           `json:"scheduledQueryArn"`
	QueryID            string                           `json:"queryId"`
	ExecutionStatus    string                           `json:"executionStatus"`
	TriggeredTimestamp int64                            `json:"triggeredTimestamp"`
	ErrorMessage       string                           `json:"errorMessage,omitempty"`
	Destinations       []CWScheduledQueryDestinationRun `json:"destinations,omitempty"`
}

var cwScheduledQueryRuns sim.Store[CWScheduledQueryRun]

type cwScheduledQueryDestinations struct {
	S3Configuration *struct {
		DestinationIdentifier string `json:"destinationIdentifier"`
		RoleArn               string `json:"roleArn"`
		OwnerAccountId        string `json:"ownerAccountId"`
		KmsKeyId              string `json:"kmsKeyId"`
	} `json:"s3Configuration"`
	LookupTableConfiguration *struct {
		TableName   string `json:"tableName"`
		RoleArn     string `json:"roleArn"`
		Description string `json:"description"`
		KmsKeyId    string `json:"kmsKeyId"`
	} `json:"lookupTableConfiguration"`
}

func registerCloudWatchScheduledQueryRuns(srv *sim.Server) {
	cwScheduledQueryRuns = sim.MakeStore[CWScheduledQueryRun](srv.DB(), "cw_scheduled_query_runs")
	ticks := sim.MakeStore[cron.Record](srv.DB(), "cw_scheduled_query_ticks")
	cron.NewTicker(ticks, cwScheduledQueryEntries).Start(srv, "CloudWatch Logs scheduled queries", time.Second)
}

func cwScheduledQueryEntries() []cron.Entry {
	var entries []cron.Entry
	for _, sq := range cwScheduledQ.List() {
		plan, err := parseAWSSchedule(sq.ScheduleExpression, sq.Timezone, false)
		if err != nil {
			continue
		}
		arn := sq.ScheduledQueryArn
		entries = append(entries, cron.Entry{
			Key:    arn,
			Spec:   sq.ScheduleExpression + "|" + sq.Timezone,
			Paused: sq.State != "ENABLED",
			Next:   plan.next,
			Fire:   func(scheduled time.Time) { cwRunScheduledQuery(arn, scheduled) },
		})
	}
	return entries
}

func cwRunScheduledQuery(arn string, scheduled time.Time) {
	sq, ok := cwScheduledQ.Get(arn)
	if !ok || sq.State != "ENABLED" {
		return
	}
	at := scheduled.UnixMilli()
	if sq.ScheduleStartTime != 0 && at < sq.ScheduleStartTime || sq.ScheduleEndTime != 0 && at > sq.ScheduleEndTime {
		return
	}
	run := CWScheduledQueryRun{ScheduledQueryArn: arn, QueryID: sim.NewUUID(), TriggeredTimestamp: at}
	stats, rows, err := cwExecuteScheduledQuery(sq, scheduled, run.QueryID)
	switch {
	case err != nil && err.invalid:
		run.ExecutionStatus, run.ErrorMessage = "InvalidQuery", err.Error()
	case err != nil:
		run.ExecutionStatus, run.ErrorMessage = "Failed", err.Error()
	default:
		run.ExecutionStatus = "Complete"
		run.Destinations = cwDeliverScheduledQuery(sq, run.QueryID, rows)
	}
	cwScheduledQueryRuns.Put(arn+"|"+run.QueryID, run)
	cwScheduledQ.Update(arn, func(s *CWScheduledQuery) {
		s.LastTriggeredTime = at
		s.LastExecutionStatus = run.ExecutionStatus
	})
	detail := map[string]any{
		"queryId":             run.QueryID,
		"queryString":         sq.QueryString,
		"logGroupIdentifiers": sq.LogGroupIdentifiers,
		"status":              run.ExecutionStatus,
		"startTime":           scheduled.Unix(),
	}
	if stats != nil {
		detail["statistics"] = stats
	}
	if body, err := json.Marshal(detail); err == nil {
		ebPutServiceEvent("aws.logs", "Scheduled Query Completed", string(body), []string{arn})
	}
}

type cwScheduledQueryError struct {
	msg     string
	invalid bool
}

func (e *cwScheduledQueryError) Error() string { return e.msg }

func cwExecuteScheduledQuery(sq CWScheduledQuery, scheduled time.Time, queryID string) (map[string]any, [][]map[string]any, *cwScheduledQueryError) {
	if sq.ExecutionRoleArn == "" {
		return nil, nil, &cwScheduledQueryError{msg: "The scheduled query has no execution role."}
	}
	var groups []string
	for _, id := range sq.LogGroupIdentifiers {
		name, ok := cwResolveLogGroupIdentifier(id)
		if !ok {
			return nil, nil, &cwScheduledQueryError{msg: "The specified log group does not exist: " + id}
		}
		groups = append(groups, name)
	}
	for _, name := range groups {
		if err := iamValidateServiceRole(sq.ExecutionRoleArn, "logs.amazonaws.com", map[string]string{
			"logs:StartQuery":      cwLogGroupArn(name) + ":*",
			"logs:GetQueryResults": cwLogGroupArn(name) + ":*",
		}); err != nil {
			return nil, nil, &cwScheduledQueryError{msg: err.Error()}
		}
	}
	end := scheduled.Unix() - sq.EndTimeOffset
	var start int64
	if sq.StartTimeOffset > 0 {
		start = scheduled.Unix() - sq.StartTimeOffset
	}
	records, scanned := cwGatherRecords(groups, start, end)
	rows, err := cwRunInsightsQuery(sq.QueryString, records, 0)
	if err != nil {
		return nil, nil, &cwScheduledQueryError{msg: err.Error(), invalid: true}
	}
	bytesScanned := 0
	for _, rec := range records {
		bytesScanned += len(rec["@message"])
	}
	cwQueries.Put(queryID, CWQuery{
		QueryID:    queryID,
		QueryStr:   sq.QueryString,
		Status:     "Complete",
		Results:    rows,
		MatchedN:   len(rows),
		ScannedN:   scanned,
		CreateTime: scheduled.Unix(),
		LogGroups:  groups,
	})
	stats := map[string]any{
		"recordsMatched":          len(rows),
		"recordsScanned":          scanned,
		"estimatedRecordsSkipped": 0,
		"bytesScanned":            bytesScanned,
		"estimatedBytesSkipped":   0,
		"logGroupsScanned":        len(groups),
		"resultCount":             len(rows),
	}
	return stats, rows, nil
}

// cwScheduledRowsJSON is the S3 payload: one object per row mapping each
// output field to its value, without @ptr.
func cwScheduledRowsJSON(rows [][]map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		obj := map[string]any{}
		for _, cell := range row {
			field, _ := cell["field"].(string)
			if field == "@ptr" {
				continue
			}
			obj[field] = cell["value"]
		}
		out = append(out, obj)
	}
	return out
}

func cwDeliverScheduledQuery(sq CWScheduledQuery, queryID string, rows [][]map[string]any) []CWScheduledQueryDestinationRun {
	if len(sq.DestinationConfiguration) == 0 {
		return nil
	}
	var dest cwScheduledQueryDestinations
	if err := json.Unmarshal(sq.DestinationConfiguration, &dest); err != nil {
		return []CWScheduledQueryDestinationRun{{Status: "CLIENT_ERROR", ErrorMessage: "The destination configuration is not valid: " + err.Error()}}
	}
	var out []CWScheduledQueryDestinationRun
	if s3 := dest.S3Configuration; s3 != nil {
		out = append(out, cwDeliverScheduledS3(s3.DestinationIdentifier, s3.RoleArn, queryID, rows))
	}
	if lt := dest.LookupTableConfiguration; lt != nil {
		out = append(out, cwDeliverScheduledLookupTable(lt.TableName, lt.RoleArn, lt.Description, lt.KmsKeyId, rows))
	}
	return out
}

func cwDeliverScheduledS3(uri, roleArn, queryID string, rows [][]map[string]any) CWScheduledQueryDestinationRun {
	run := CWScheduledQueryDestinationRun{DestinationType: "S3", DestinationIdentifier: uri}
	rest, ok := strings.CutPrefix(uri, "s3://")
	bucket, prefix, _ := strings.Cut(rest, "/")
	if !ok || bucket == "" {
		run.Status, run.ErrorMessage = "CLIENT_ERROR", "The destination is not an Amazon S3 URI: "+uri
		return run
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	key := prefix + queryID + ".json"
	if err := iamValidateServiceRole(roleArn, "logs.amazonaws.com", map[string]string{"s3:PutObject": "arn:aws:s3:::" + bucket + "/" + key}); err != nil {
		run.Status, run.ErrorMessage = "CLIENT_ERROR", err.Error()
		return run
	}
	if _, ok := s3Buckets_.Get(bucket); !ok {
		run.Status, run.ErrorMessage = "CLIENT_ERROR", "The specified bucket does not exist: "+bucket
		return run
	}
	body, err := json.Marshal(cwScheduledRowsJSON(rows))
	if err != nil {
		run.Status, run.ErrorMessage = "FAILED", err.Error()
		return run
	}
	objectKey := s3ObjectKey(bucket, key)
	release := s3ObjectWriters.Lock(objectKey)
	defer release()
	if _, err := s3StoreObjectData(S3Object{Key: objectKey, ContentType: "application/json", LastModified: time.Now().UTC()}, body); err != nil {
		run.Status, run.ErrorMessage = "FAILED", err.Error()
		return run
	}
	run.Status, run.ProcessedIdentifier = "COMPLETE", "s3://"+bucket+"/"+key
	return run
}

// cwDeliverScheduledLookupTable replaces the table's content with the rows as
// CSV, creating the table on the first successful run.
func cwDeliverScheduledLookupTable(name, roleArn, description, kmsKeyID string, rows [][]map[string]any) CWScheduledQueryDestinationRun {
	run := CWScheduledQueryDestinationRun{DestinationType: "LOOKUP_TABLE", DestinationIdentifier: name}
	arn := cwLookupTableArn(name)
	if err := iamValidateServiceRole(roleArn, "logs.amazonaws.com", map[string]string{
		"logs:CreateLookupTable": arn,
		"logs:UpdateLookupTable": arn,
	}); err != nil {
		run.Status, run.ErrorMessage = "CLIENT_ERROR", err.Error()
		return run
	}
	var fields []string
	seen := map[string]bool{}
	for _, row := range rows {
		for _, cell := range row {
			field, _ := cell["field"].(string)
			if field != "@ptr" && !seen[field] {
				seen[field] = true
				fields = append(fields, field)
			}
		}
	}
	var b strings.Builder
	cw := csv.NewWriter(&b)
	_ = cw.Write(fields)
	for _, obj := range cwScheduledRowsJSON(rows) {
		record := make([]string, 0, len(fields))
		for _, f := range fields {
			value := ""
			if v, ok := obj[f]; ok && v != nil {
				value = fmt.Sprint(v)
			}
			record = append(record, value)
		}
		_ = cw.Write(record)
	}
	cw.Flush()
	body := b.String()
	now := time.Now().UnixMilli()
	if !cwLookupTables.Update(arn, func(lt *CWLookupTable) {
		lt.TableBody, lt.SizeBytes, lt.LastUpdatedTime = body, int64(len(body)), now
	}) {
		cwLookupTables.Put(arn, CWLookupTable{
			LookupTableArn: arn, LookupTableName: name, Description: description, KmsKeyId: kmsKeyID,
			TableBody: body, SizeBytes: int64(len(body)), CreatedAt: now, LastUpdatedTime: now,
		})
	}
	run.Status, run.ProcessedIdentifier = "COMPLETE", arn
	return run
}

func (run CWScheduledQueryRun) wire() map[string]any {
	out := map[string]any{
		"queryId":            run.QueryID,
		"executionStatus":    run.ExecutionStatus,
		"triggeredTimestamp": run.TriggeredTimestamp,
	}
	if run.ErrorMessage != "" {
		out["errorMessage"] = run.ErrorMessage
	}
	if len(run.Destinations) > 0 {
		out["destinations"] = run.Destinations
	}
	return out
}

func handleCWGetScheduledQueryHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier        string   `json:"identifier"`
		StartTime         *int64   `json:"startTime"`
		EndTime           *int64   `json:"endTime"`
		ExecutionStatuses []string `json:"executionStatuses"`
		MaxResults        int      `json:"maxResults"`
		NextToken         string   `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.StartTime == nil || req.EndTime == nil {
		AWSError(w, "InvalidParameterException", "startTime and endTime are required", http.StatusBadRequest)
		return
	}
	maxResults := req.MaxResults
	if maxResults == 0 {
		maxResults = 1000
	}
	if maxResults < 1 || maxResults > 1000 {
		AWSError(w, "InvalidParameterException", "maxResults must be between 1 and 1000", http.StatusBadRequest)
		return
	}
	sq, ok := cwScheduledQueryByIdentifier(req.Identifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified scheduled query does not exist: %s", req.Identifier)
		return
	}
	runs := cwScheduledQueryRuns.Filter(func(run CWScheduledQueryRun) bool {
		return run.ScheduledQueryArn == sq.ScheduledQueryArn &&
			run.TriggeredTimestamp >= *req.StartTime && run.TriggeredTimestamp <= *req.EndTime &&
			(len(req.ExecutionStatuses) == 0 || cwStrIn(run.ExecutionStatus, req.ExecutionStatuses))
	})
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].TriggeredTimestamp != runs[j].TriggeredTimestamp {
			return runs[i].TriggeredTimestamp > runs[j].TriggeredTimestamp
		}
		return runs[i].QueryID < runs[j].QueryID
	})
	start := 0
	if req.NextToken != "" {
		n, err := strconv.Atoi(req.NextToken)
		if err != nil || n < 0 || n > len(runs) {
			AWSError(w, "InvalidParameterException", "The nextToken is not valid", http.StatusBadRequest)
			return
		}
		start = n
	}
	end := min(start+maxResults, len(runs))
	history := make([]map[string]any, 0, end-start)
	for _, run := range runs[start:end] {
		history = append(history, run.wire())
	}
	resp := map[string]any{
		"scheduledQueryArn": sq.ScheduledQueryArn,
		"name":              sq.Name,
		"triggerHistory":    history,
	}
	if end < len(runs) {
		resp["nextToken"] = strconv.Itoa(end)
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// cwDeleteScheduledQueryRuns drops a deleted scheduled query's history.
func cwDeleteScheduledQueryRuns(arn string) {
	cwScheduledQueryRuns.Prune(func(run CWScheduledQueryRun) bool { return run.ScheduledQueryArn == arn })
}
