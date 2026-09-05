package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch Logs types

type CWLogGroup struct {
	LogGroupName    string            `json:"logGroupName"`
	Arn             string            `json:"arn"`
	CreationTime    int64             `json:"creationTime"`
	RetentionInDays int               `json:"retentionInDays,omitempty"`
	StoredBytes     int64             `json:"storedBytes"`
	KmsKeyId        string            `json:"kmsKeyId,omitempty"`
	Tags            map[string]string `json:"-"`
}

type CWLogStream struct {
	LogStreamName       string `json:"logStreamName"`
	LogGroupName        string `json:"-"`
	CreationTime        int64  `json:"creationTime"`
	FirstEventTimestamp int64  `json:"firstEventTimestamp,omitempty"`
	LastEventTimestamp  int64  `json:"lastEventTimestamp,omitempty"`
	LastIngestionTime   int64  `json:"lastIngestionTime,omitempty"`
	Arn                 string `json:"arn"`
	UploadSequenceToken string `json:"uploadSequenceToken"`
}

type CWLogEvent struct {
	Timestamp     int64  `json:"timestamp"`
	Message       string `json:"message"`
	IngestionTime int64  `json:"ingestionTime"`
}

// State stores
var (
	cwLogGroups  sim.Store[CWLogGroup]
	cwLogStreams sim.Store[CWLogStream]
	cwLogEvents  sim.Store[[]CWLogEvent]
	cwSequences  sim.Store[int64]
)

func cwLogGroupArn(name string) string {
	return fmt.Sprintf("arn:aws:logs:"+awsRegion()+":"+awsAccountID()+":log-group:%s", name)
}

func cwLogStreamArn(group, stream string) string {
	return fmt.Sprintf("arn:aws:logs:"+awsRegion()+":"+awsAccountID()+":log-group:%s:log-stream:%s", group, stream)
}

func cwEventsKey(group, stream string) string {
	return group + ":" + stream
}

// cwIngestWorkloadLogLine records one line a workload wrote to stdout/stderr
// through the same ingestion path PutLogEvents uses. In real AWS a container
// log driver is an ordinary CloudWatch producer: the stream's event timestamps
// advance, embedded-metric documents are extracted, and the group's metric
// filters fire. Appending straight into the event slice left an actively
// logging stream reporting the instant it was created, so an operator ordering
// a service's streams by LastEventTime could not tell a task that is still
// writing from one that has said nothing since it started.
func cwIngestWorkloadLogLine(logGroup, logStream, message string) {
	key := cwEventsKey(logGroup, logStream)
	nowMs := time.Now().UnixMilli()
	event := CWLogEvent{Timestamp: nowMs, Message: message, IngestionTime: nowMs}
	cwLogEvents.Update(key, func(events *[]CWLogEvent) {
		*events = append(*events, event)
	})
	for _, datum := range extractEMFMetrics(message, nowMs) {
		cwStoreDatum(datum)
	}
	cwEvaluateMetricFilters(logGroup, []CWLogEvent{event})
	nextSequenceToken := cwNextSequenceToken()
	cwLogStreams.Update(key, func(stream *CWLogStream) {
		stream.LastIngestionTime = nowMs
		stream.UploadSequenceToken = nextSequenceToken
		if stream.FirstEventTimestamp == 0 {
			stream.FirstEventTimestamp = nowMs
		}
		stream.LastEventTimestamp = nowMs
	})
}

func registerCloudWatchLogs(r *AWSRouter, srv *sim.Server) {
	cwLogGroups = sim.MakeStore[CWLogGroup](srv.DB(), "cw_log_groups")
	cwLogStreams = sim.MakeStore[CWLogStream](srv.DB(), "cw_log_streams")
	cwLogEvents = sim.MakeStore[[]CWLogEvent](srv.DB(), "cw_log_events")
	cwSequences = sim.MakeStore[int64](srv.DB(), "cw_sequences")
	cwQueries = sim.MakeStore[CWQuery](srv.DB(), "cw_insights_queries")
	registerCloudWatchInsights(r)
	registerCloudWatchLogsOps(r, srv)
	registerCloudWatchLogsExtra2(r, srv)
	registerCloudWatchLogsExtra3(r, srv)
	registerCloudWatchLogsExtra4(r, srv)
	registerCloudWatchLogsExtra5(r, srv)
	registerCloudWatchLogsSyslog(r, srv)

	r.Register("Logs_20140328.CreateLogGroup", handleCWCreateLogGroup)
	r.Register("Logs_20140328.DescribeLogGroups", handleCWDescribeLogGroups)
	r.Register("Logs_20140328.DeleteLogGroup", handleCWDeleteLogGroup)
	r.Register("Logs_20140328.CreateLogStream", handleCWCreateLogStream)
	r.Register("Logs_20140328.DescribeLogStreams", handleCWDescribeLogStreams)
	r.Register("Logs_20140328.PutLogEvents", handleCWPutLogEvents)
	r.Register("Logs_20140328.GetLogEvents", handleCWGetLogEvents)
	r.Register("Logs_20140328.FilterLogEvents", handleCWFilterLogEvents)
	r.Register("Logs_20140328.PutRetentionPolicy", handleCWPutRetentionPolicy)
	r.Register("Logs_20140328.ListTagsForResource", handleCWListTagsForResource)
	r.Register("Logs_20140328.TagResource", handleCWTagResource)
	r.Register("Logs_20140328.AssociateKmsKey", handleCWAssociateKmsKey)
	r.Register("Logs_20140328.DisassociateKmsKey", handleCWDisassociateKmsKey)
}

func cwNextSequenceToken() string {
	var sequence int64
	cwSequences.Upsert("logs", func(current *int64) {
		*current++
		sequence = *current
	})
	return fmt.Sprintf("%016d", sequence)
}

// handleCWAssociateKmsKey sets the KMS key on an existing log group (the path
// for aws_cloudwatch_log_group when kms_key_id is changed after create).
func handleCWAssociateKmsKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
		KmsKeyId     string `json:"kmsKeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwLogGroups.Update(req.LogGroupName, func(lg *CWLogGroup) { lg.KmsKeyId = req.KmsKeyId }) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDisassociateKmsKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwLogGroups.Update(req.LogGroupName, func(lg *CWLogGroup) { lg.KmsKeyId = "" }) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWCreateLogGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName    string            `json:"logGroupName"`
		RetentionInDays int               `json:"retentionInDays"`
		KmsKeyId        string            `json:"kmsKeyId"`
		Tags            map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName is required", http.StatusBadRequest)
		return
	}

	if _, exists := cwLogGroups.Get(req.LogGroupName); exists {
		AWSErrorf(w, "ResourceAlreadyExistsException", http.StatusBadRequest,
			"The specified log group already exists: %s", req.LogGroupName)
		return
	}

	lg := CWLogGroup{
		LogGroupName:    req.LogGroupName,
		Arn:             cwLogGroupArn(req.LogGroupName),
		CreationTime:    time.Now().UnixMilli(),
		RetentionInDays: req.RetentionInDays,
		KmsKeyId:        req.KmsKeyId,
		Tags:            req.Tags,
	}
	cwLogGroups.Put(req.LogGroupName, lg)

	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeLogGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupNamePrefix string `json:"logGroupNamePrefix"`
		Limit              int    `json:"limit"`
		NextToken          string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	var groups []CWLogGroup
	if req.LogGroupNamePrefix != "" {
		groups = cwLogGroups.Filter(func(lg CWLogGroup) bool {
			return strings.HasPrefix(lg.LogGroupName, req.LogGroupNamePrefix)
		})
	} else {
		groups = cwLogGroups.List()
	}
	if groups == nil {
		groups = []CWLogGroup{}
	}
	sortBy(groups, func(g CWLogGroup) string { return g.LogGroupName })

	page, next := awsPage(groups, req.NextToken, req.Limit, 50)
	out := map[string]any{"logGroups": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWDeleteLogGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName is required", http.StatusBadRequest)
		return
	}

	if !cwLogGroups.Delete(req.LogGroupName) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}

	// Clean up streams and events for this group
	streams := cwLogStreams.Filter(func(s CWLogStream) bool {
		return s.LogGroupName == req.LogGroupName
	})
	for _, s := range streams {
		key := cwEventsKey(req.LogGroupName, s.LogStreamName)
		cwLogStreams.Delete(key)
		cwLogEvents.Delete(key)
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWCreateLogStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.LogStreamName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName and logStreamName are required", http.StatusBadRequest)
		return
	}

	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}

	key := cwEventsKey(req.LogGroupName, req.LogStreamName)
	if _, exists := cwLogStreams.Get(key); exists {
		AWSErrorf(w, "ResourceAlreadyExistsException", http.StatusBadRequest,
			"The specified log stream already exists: %s", req.LogStreamName)
		return
	}

	ls := CWLogStream{
		LogStreamName:       req.LogStreamName,
		LogGroupName:        req.LogGroupName,
		CreationTime:        time.Now().UnixMilli(),
		Arn:                 cwLogStreamArn(req.LogGroupName, req.LogStreamName),
		UploadSequenceToken: cwNextSequenceToken(),
	}
	cwLogStreams.Put(key, ls)
	cwLogEvents.Put(key, []CWLogEvent{})

	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeLogStreams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName        string `json:"logGroupName"`
		LogStreamNamePrefix string `json:"logStreamNamePrefix"`
		OrderBy             string `json:"orderBy"`
		Descending          *bool  `json:"descending"`
		Limit               int    `json:"limit"`
		NextToken           string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName is required", http.StatusBadRequest)
		return
	}
	// Real DescribeLogStreams returns ResourceNotFoundException for a missing
	// group rather than an empty list (RNFE is a declared error for the op).
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}

	streams := cwLogStreams.Filter(func(s CWLogStream) bool {
		if s.LogGroupName != req.LogGroupName {
			return false
		}
		if req.LogStreamNamePrefix != "" {
			return strings.HasPrefix(s.LogStreamName, req.LogStreamNamePrefix)
		}
		return true
	})
	if streams == nil {
		streams = []CWLogStream{}
	}

	if req.OrderBy == "LastEventTime" {
		desc := req.Descending != nil && *req.Descending
		sort.Slice(streams, func(i, j int) bool {
			if desc {
				return streams[i].LastEventTimestamp > streams[j].LastEventTimestamp
			}
			return streams[i].LastEventTimestamp < streams[j].LastEventTimestamp
		})
	} else {
		desc := req.Descending != nil && *req.Descending
		sort.Slice(streams, func(i, j int) bool {
			if desc {
				return streams[i].LogStreamName > streams[j].LogStreamName
			}
			return streams[i].LogStreamName < streams[j].LogStreamName
		})
	}

	page, next := awsPage(streams, req.NextToken, req.Limit, 50)
	out := map[string]any{"logStreams": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWPutLogEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
		LogEvents     []struct {
			Timestamp int64  `json:"timestamp"`
			Message   string `json:"message"`
		} `json:"logEvents"`
		SequenceToken string `json:"sequenceToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.LogStreamName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName and logStreamName are required", http.StatusBadRequest)
		return
	}

	key := cwEventsKey(req.LogGroupName, req.LogStreamName)
	if _, ok := cwLogStreams.Get(key); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log stream does not exist: %s", req.LogStreamName)
		return
	}

	now := time.Now().UnixMilli()
	var newEvents []CWLogEvent
	for _, e := range req.LogEvents {
		newEvents = append(newEvents, CWLogEvent{
			Timestamp:     e.Timestamp,
			Message:       e.Message,
			IngestionTime: now,
		})
	}

	// Append events
	cwLogEvents.Update(key, func(events *[]CWLogEvent) {
		*events = append(*events, newEvents...)
	})

	// CloudWatch auto-extracts metrics from any EMF-formatted event into the
	// metric store, queryable through the metric APIs with no PutMetricData call.
	for _, e := range req.LogEvents {
		for _, datum := range extractEMFMetrics(e.Message, e.Timestamp) {
			cwStoreDatum(datum)
		}
	}

	cwEvaluateMetricFilters(req.LogGroupName, newEvents)

	// Update stream timestamps
	nextSequenceToken := cwNextSequenceToken()
	cwLogStreams.Update(key, func(s *CWLogStream) {
		s.LastIngestionTime = now
		s.UploadSequenceToken = nextSequenceToken
		if len(newEvents) > 0 {
			if s.FirstEventTimestamp == 0 {
				s.FirstEventTimestamp = newEvents[0].Timestamp
			}
			s.LastEventTimestamp = newEvents[len(newEvents)-1].Timestamp
		}
	})

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"nextSequenceToken": nextSequenceToken,
	})
}

// The bounds GetLogEvents applies when the caller names no limit: at most
// 10,000 events, and at most 1 MB of payload, where each event costs its
// message's bytes plus the 26-byte per-event overhead CloudWatch Logs counts.
const (
	cwGetLogEventsMaxEvents = 10000
	cwGetLogEventsMaxBytes  = 1024 * 1024
	cwLogEventOverheadBytes = 26
)

// cwGetLogEventsDefaultPage returns how many of the newest events fit in a
// default page. It measures from the tail because that is the end the default
// page is anchored at, so the count answers "how many of the latest events are
// returned", not "how many of the oldest".
func cwGetLogEventsDefaultPage(events []CWLogEvent) int {
	total, count := 0, 0
	for i := len(events) - 1; i >= 0 && count < cwGetLogEventsMaxEvents; i-- {
		total += len(events[i].Message) + cwLogEventOverheadBytes
		if total > cwGetLogEventsMaxBytes && count > 0 {
			break
		}
		count++
	}
	if count == 0 {
		count = 1 // a single event larger than the cap is still returned
	}
	return count
}

func handleCWGetLogEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
		StartTime     int64  `json:"startTime"`
		EndTime       int64  `json:"endTime"`
		Limit         int    `json:"limit"`
		StartFromHead *bool  `json:"startFromHead"`
		NextToken     string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.LogStreamName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName and logStreamName are required", http.StatusBadRequest)
		return
	}
	// Real GetLogEvents checks the group before the stream, so a missing group
	// reports "log group does not exist", not "log stream does not exist".
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}

	key := cwEventsKey(req.LogGroupName, req.LogStreamName)
	events, ok := cwLogEvents.Get(key)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log stream does not exist: %s", req.LogStreamName)
		return
	}

	// Apply time range filter
	var filtered []CWLogEvent
	for _, e := range events {
		if req.StartTime > 0 && e.Timestamp < req.StartTime {
			continue
		}
		if req.EndTime > 0 && e.Timestamp > req.EndTime {
			continue
		}
		filtered = append(filtered, e)
	}

	if req.Limit < 0 || req.Limit > cwGetLogEventsMaxEvents {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"1 validation error detected: Value '%d' at 'limit' failed to satisfy constraint: "+
				"Member must have value less than or equal to %d", req.Limit, cwGetLogEventsMaxEvents)
		return
	}

	// A page is bounded whether or not the caller asked for a limit: AWS
	// returns "as many log events as can fit in a response size of 1 MB, up to
	// 10,000 log events" by default. An unbounded page is not a harmless
	// superset — with startFromHead unset the page is anchored at the tail, so
	// serving every event from the oldest is how a reader of a busy stream sees
	// the beginning of history where the service would have shown them the
	// latest lines.
	page := req.Limit
	if page == 0 {
		page = cwGetLogEventsDefaultPage(filtered)
	}

	// Parse offset from NextToken (format: "f/{offset}" or "b/{offset}").
	// On the first call (no token), startFromHead chooses the window: true →
	// earliest events first (offset 0); false/unset (the documented default) →
	// the latest events first, i.e. the tail of the stream. Within-page order is
	// always ascending by timestamp either way.
	offset := 0
	if req.NextToken != "" {
		parts := strings.SplitN(req.NextToken, "/", 2)
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil && n >= 0 {
				offset = n
			}
		}
	} else {
		fromHead := req.StartFromHead != nil && *req.StartFromHead
		if !fromHead && len(filtered) > page {
			offset = len(filtered) - page
		}
	}

	// Apply offset — skip events already consumed
	if offset > 0 && offset <= len(filtered) {
		filtered = filtered[offset:]
	} else if offset > len(filtered) {
		filtered = nil
	}

	if len(filtered) > page {
		filtered = filtered[:page]
	}
	if filtered == nil {
		filtered = []CWLogEvent{}
	}

	// New forward token = offset + events returned
	newForwardOffset := offset + len(filtered)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"events":            filtered,
		"nextForwardToken":  fmt.Sprintf("f/%d", newForwardOffset),
		"nextBackwardToken": fmt.Sprintf("b/%d", offset),
	})
}

func handleCWFilterLogEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName        string   `json:"logGroupName"`
		LogStreamNames      []string `json:"logStreamNames"`
		LogStreamNamePrefix string   `json:"logStreamNamePrefix"`
		FilterPattern       string   `json:"filterPattern"`
		StartTime           int64    `json:"startTime"`
		EndTime             int64    `json:"endTime"`
		Limit               int      `json:"limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName is required", http.StatusBadRequest)
		return
	}
	// logStreamNames and logStreamNamePrefix are mutually exclusive, exactly as
	// real CloudWatch Logs rejects them.
	if len(req.LogStreamNames) > 0 && req.LogStreamNamePrefix != "" {
		AWSError(w, "InvalidParameterException",
			"LogStreamNames and LogStreamNamePrefix are mutually exclusive.", http.StatusBadRequest)
		return
	}
	// Real CloudWatch Logs FilterLogEvents validates the group exists first
	// (ResourceNotFoundException is a declared error for the op); without this
	// a missing group returns an empty event list, masking misconfiguration.
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}

	// Compile the filter pattern once. A malformed pattern is an
	// InvalidParameterException (as real CloudWatch Logs returns), not a silent
	// empty result set.
	pattern, err := cwCompileLogPattern(req.FilterPattern)
	if err != nil {
		AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
		return
	}

	// Find all streams for this group
	streams := cwLogStreams.Filter(func(s CWLogStream) bool {
		if s.LogGroupName != req.LogGroupName {
			return false
		}
		if len(req.LogStreamNames) > 0 {
			for _, name := range req.LogStreamNames {
				if s.LogStreamName == name {
					return true
				}
			}
			return false
		}
		if req.LogStreamNamePrefix != "" {
			return strings.HasPrefix(s.LogStreamName, req.LogStreamNamePrefix)
		}
		return true
	})

	var results []map[string]any
	for _, stream := range streams {
		key := cwEventsKey(req.LogGroupName, stream.LogStreamName)
		events, ok := cwLogEvents.Get(key)
		if !ok {
			continue
		}

		for _, e := range events {
			if req.StartTime > 0 && e.Timestamp < req.StartTime {
				continue
			}
			if req.EndTime > 0 && e.Timestamp > req.EndTime {
				continue
			}
			if !pattern.match(e.Message) {
				continue
			}
			results = append(results, map[string]any{
				"logStreamName": stream.LogStreamName,
				"timestamp":     e.Timestamp,
				"message":       e.Message,
				"ingestionTime": e.IngestionTime,
				"eventId":       generateUUID(),
			})
		}
	}

	if req.Limit > 0 && len(results) > req.Limit {
		results = results[:req.Limit]
	}
	if results == nil {
		results = []map[string]any{}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"events":             results,
		"searchedLogStreams": []any{},
	})
}

func handleCWPutRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName    string `json:"logGroupName"`
		RetentionInDays int    `json:"retentionInDays"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName is required", http.StatusBadRequest)
		return
	}

	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}

	cwLogGroups.Update(req.LogGroupName, func(lg *CWLogGroup) {
		lg.RetentionInDays = req.RetentionInDays
	})

	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// cwLogGroupByArn resolves a log-group ARN to its stored name. Real log-group
// ARNs may carry a trailing ":*" (stream wildcard); the provider tags by the
// bare group ARN, so accept both.
func cwLogGroupByArn(arn string) (string, bool) {
	const sep = ":log-group:"
	idx := strings.Index(arn, sep)
	if idx < 0 {
		return "", false
	}
	name := strings.TrimSuffix(arn[idx+len(sep):], ":*")
	_, ok := cwLogGroups.Get(name)
	return name, ok
}

func handleCWListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	tags := map[string]string{}
	if name, ok := cwLogGroupByArn(req.ResourceArn); ok {
		if lg, ok := cwLogGroups.Get(name); ok && lg.Tags != nil {
			tags = lg.Tags
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func handleCWTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string            `json:"resourceArn"`
		Tags        map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	name, ok := cwLogGroupByArn(req.ResourceArn)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "log group not found: %s", req.ResourceArn)
		return
	}
	cwLogGroups.Update(name, func(lg *CWLogGroup) {
		if lg.Tags == nil {
			lg.Tags = map[string]string{}
		}
		for k, v := range req.Tags {
			lg.Tags[k] = v
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
