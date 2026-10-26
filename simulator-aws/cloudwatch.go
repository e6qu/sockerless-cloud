package main

import (
	"compress/gzip"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
	"github.com/e6qu/sockerless-cloud/sim/listq"
	"github.com/e6qu/sockerless-cloud/sim/logstore"
)

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
	// CompressedBytes is the stream's share of storedBytes, measured at
	// CompressedThrough (-1 once retention has removed events since).
	CompressedBytes     int64  `json:"-"`
	CompressedThrough   int64  `json:"-"`
	Arn                 string `json:"arn"`
	UploadSequenceToken string `json:"uploadSequenceToken"`
}

type CWLogEvent struct {
	Timestamp     int64  `json:"timestamp"`
	Message       string `json:"message"`
	IngestionTime int64  `json:"ingestionTime"`
}

var (
	cwLogGroups  sim.Store[CWLogGroup]
	cwLogStreams sim.Store[CWLogStream]
	cwLogEvents  *logstore.Log[CWLogEvent]
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

func cwNewLogEvents(db *sql.DB) *logstore.Log[CWLogEvent] {
	return logstore.New(
		sim.MakeStore[logstore.Entry[CWLogEvent]](db, "cw_log_stream_events"),
		sim.MakeStore[logstore.Head](db, "cw_log_stream_heads"))
}

// Returns the retained events in arrival order.
func cwStreamEvents(key string) ([]CWLogEvent, bool) {
	if !cwLogEvents.Exists(key) {
		return nil, false
	}
	return cwLogEvents.Values(key), true
}

// A container log driver is an ordinary CloudWatch producer: timestamps
// advance, EMF documents are extracted and metric filters fire. writtenAt
// becomes the event timestamp, apart from the ingestion time; zero dates the
// line on arrival.
func cwIngestWorkloadLogLine(logGroup, logStream, message string, writtenAt time.Time) {
	key := cwEventsKey(logGroup, logStream)
	nowMs := time.Now().UnixMilli()
	eventMs := nowMs
	if !writtenAt.IsZero() {
		eventMs = writtenAt.UnixMilli()
	}
	event := CWLogEvent{Timestamp: eventMs, Message: message, IngestionTime: nowMs}
	nextSequenceToken := cwNextSequenceToken()
	cwAppendLogEvents(key, []CWLogEvent{event}, func(stream *CWLogStream) {
		stream.UploadSequenceToken = nextSequenceToken
	})
	for _, datum := range extractEMFMetrics(message, eventMs) {
		cwStoreDatum(datum)
	}
	cwEvaluateMetricFilters(logGroup, []CWLogEvent{event})
}

// Create the stream before the workload starts, as the writing service does,
// so it exists even when the workload prints nothing.
func cwEnsureLogStream(group, stream string) {
	nowMs := time.Now().UnixMilli()
	if _, exists := cwLogGroups.Get(group); !exists {
		cwLogGroups.Put(group, CWLogGroup{
			LogGroupName: group,
			Arn:          cwLogGroupArn(group),
			CreationTime: nowMs,
		})
	}
	key := cwEventsKey(group, stream)
	if _, exists := cwLogStreams.Get(key); !exists {
		cwLogStreams.Put(key, CWLogStream{
			LogStreamName:       stream,
			LogGroupName:        group,
			CreationTime:        nowMs,
			Arn:                 cwLogStreamArn(group, stream),
			UploadSequenceToken: "1",
		})
		cwLogEvents.Open(key)
	}
}

// Keep the stream's first and last event timestamps and last ingestion time in
// step, whichever path delivered; touch updates the stream in the same write.
func cwAppendLogEvents(key string, events []CWLogEvent, touch func(*CWLogStream)) {
	if len(events) == 0 {
		return
	}
	items := make([]logstore.Item[CWLogEvent], 0, len(events))
	for _, e := range events {
		items = append(items, logstore.Item[CWLogEvent]{Time: e.Timestamp, Value: e})
	}
	cwLogEvents.Append(key, time.Now(), items...)
	if group, stream, ok := strings.Cut(key, ":"); ok {
		cwLiveTailPublish(group, stream, events)
	}
	first, last, ingested := events[0].Timestamp, events[0].Timestamp, events[0].IngestionTime
	for _, e := range events[1:] {
		first, last, ingested = min(first, e.Timestamp), max(last, e.Timestamp), max(ingested, e.IngestionTime)
	}
	cwLogStreams.Update(key, func(stream *CWLogStream) {
		if stream.FirstEventTimestamp == 0 || first < stream.FirstEventTimestamp {
			stream.FirstEventTimestamp = first
		}
		stream.LastEventTimestamp = max(stream.LastEventTimestamp, last)
		stream.LastIngestionTime = max(stream.LastIngestionTime, ingested)
		if touch != nil {
			touch(stream)
		}
	})
}

// CloudWatch Logs deletes an event within about 72 hours of expiry, so an
// hourly pass is well inside that.
const cwRetentionSweepInterval = time.Hour

func cwSweepLogs(now time.Time) int {
	deleted := cwSweepExpiredLogEvents(now)
	cwRefreshStoredBytes()
	return deleted
}

// Recompress only streams that changed since last measured, so the figure
// lags ingestion as the service's does.
func cwRefreshStoredBytes() {
	totals := map[string]int64{}
	for _, stream := range cwLogStreams.List() {
		size := stream.CompressedBytes
		if stream.CompressedThrough != stream.LastIngestionTime {
			key := cwEventsKey(stream.LogGroupName, stream.LogStreamName)
			size = cwCompressedSize(cwLogEvents.Values(key))
			through := stream.LastIngestionTime
			cwLogStreams.Update(key, func(s *CWLogStream) {
				s.CompressedBytes = size
				s.CompressedThrough = through
			})
		}
		totals[stream.LogGroupName] += size
	}
	for _, group := range cwLogGroups.List() {
		if total := totals[group.LogGroupName]; total != group.StoredBytes {
			cwLogGroups.Update(group.LogGroupName, func(g *CWLogGroup) { g.StoredBytes = total })
		}
	}
}

func cwCompressedSize(events []CWLogEvent) int64 {
	if len(events) == 0 {
		return 0
	}
	var counted byteCounter
	zw := gzip.NewWriter(&counted)
	for _, e := range events {
		_, _ = zw.Write([]byte(e.Message))
		_, _ = zw.Write([]byte{'\n'})
	}
	_ = zw.Close()
	return int64(counted)
}

type byteCounter int64

func (c *byteCounter) Write(p []byte) (int, error) {
	*c += byteCounter(len(p))
	return len(p), nil
}

// Retention removes events, not streams.
func cwSweepExpiredLogEvents(now time.Time) int {
	cutoffs := map[string]int64{}
	for _, group := range cwLogGroups.List() {
		if group.RetentionInDays > 0 {
			cutoffs[group.LogGroupName] = now.Add(-time.Duration(group.RetentionInDays) * 24 * time.Hour).UnixMilli()
		}
	}
	if len(cutoffs) == 0 {
		return 0
	}
	deleted := 0
	for _, stream := range cwLogStreams.List() {
		cutoff, ok := cutoffs[stream.LogGroupName]
		// A stream stored without a first timestamp is read rather than assumed
		// young.
		if !ok || (stream.FirstEventTimestamp != 0 && stream.FirstEventTimestamp >= cutoff) {
			continue
		}
		key := cwEventsKey(stream.LogGroupName, stream.LogStreamName)
		n := cwLogEvents.Expire(key, time.UnixMilli(cutoff))
		if n == 0 {
			continue
		}
		deleted += n
		cwLogStreams.Update(key, func(s *CWLogStream) { s.CompressedThrough = -1 })
	}
	return deleted
}

func registerCloudWatchLogs(r *AWSRouter, srv *sim.Server) {
	cwLogGroups = sim.MakeStore[CWLogGroup](srv.DB(), "cw_log_groups")
	cwLogStreams = sim.MakeStore[CWLogStream](srv.DB(), "cw_log_streams")
	cwLogEvents = cwNewLogEvents(srv.DB())
	if err := cwMigrateLogEvents(srv.DB()); err != nil {
		log.Fatalf("cloudwatch logs: %v", err)
	}
	cwSequences = sim.MakeStore[int64](srv.DB(), "cw_sequences")
	cwQueries = sim.MakeStore[CWQuery](srv.DB(), "cw_insights_queries")
	kvstore.StartSweeper(srv, "CloudWatch Logs retention", cwRetentionSweepInterval, func(now time.Time) { cwSweepLogs(now) })
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

// aws_cloudwatch_log_group takes this path when kms_key_id changes after
// create.
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

	page, next, pageOK := awsKeyPage(w, logsBadToken, groups, func(it CWLogGroup) string { return it.LogGroupName }, req.NextToken, req.Limit, 50)
	if !pageOK {
		return
	}
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

	streams := cwLogStreams.Filter(func(s CWLogStream) bool {
		return s.LogGroupName == req.LogGroupName
	})
	for _, s := range streams {
		key := cwEventsKey(req.LogGroupName, s.LogStreamName)
		cwLogStreams.Delete(key)
		cwLogEvents.Drop(key)
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
	cwLogEvents.Open(key)

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
	// A missing group is a ResourceNotFoundException, not an empty list.
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

	desc := req.Descending != nil && *req.Descending
	key := func(s CWLogStream) string {
		k := s.LogStreamName
		if req.OrderBy == "LastEventTime" {
			k = listq.IntKey(s.LastEventTimestamp) + "\x00" + k
		}
		if desc {
			return awsDescKey(k)
		}
		return k
	}

	page, next, pageOK := awsKeyPage(w, logsBadToken, streams, key, req.NextToken, req.Limit, 50)
	if !pageOK {
		return
	}
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

	nextSequenceToken := cwNextSequenceToken()
	cwAppendLogEvents(key, newEvents, func(s *CWLogStream) {
		s.UploadSequenceToken = nextSequenceToken
	})

	// CloudWatch extracts metrics from any EMF event.
	for _, e := range req.LogEvents {
		for _, datum := range extractEMFMetrics(e.Message, e.Timestamp) {
			cwStoreDatum(datum)
		}
	}

	cwEvaluateMetricFilters(req.LogGroupName, newEvents)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"nextSequenceToken": nextSequenceToken,
	})
}

// GetLogEvents' default bounds: 10,000 events and 1 MB, each event costing
// its message bytes plus 26 bytes of overhead.
const (
	cwGetLogEventsMaxEvents = 10000
	cwGetLogEventsMaxBytes  = 1024 * 1024
	cwLogEventOverheadBytes = 26
)

// Measure from the tail, where the default page is anchored.
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
		count = 1 // A single event larger than the cap is still returned.
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
	// Check the group before the stream, as GetLogEvents does.
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}

	key := cwEventsKey(req.LogGroupName, req.LogStreamName)
	events, ok := cwStreamEvents(key)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log stream does not exist: %s", req.LogStreamName)
		return
	}

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

	// AWS returns "as many log events as can fit in a response size of 1 MB, up
	// to 10,000 log events" when no limit is given.
	page := req.Limit
	if page == 0 {
		page = cwGetLogEventsDefaultPage(filtered)
	}

	// NextToken is "f/{offset}" or "b/{offset}". Without a token, startFromHead
	// picks the head; unset, the documented default, picks the tail. Within a page
	// events ascend by timestamp.
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
	// logStreamNames and logStreamNamePrefix are mutually exclusive.
	if len(req.LogStreamNames) > 0 && req.LogStreamNamePrefix != "" {
		AWSError(w, "InvalidParameterException",
			"LogStreamNames and LogStreamNamePrefix are mutually exclusive.", http.StatusBadRequest)
		return
	}
	// A missing group is a ResourceNotFoundException, not an empty result.
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}

	// A malformed pattern is an InvalidParameterException.
	pattern, err := cwCompileLogPattern(req.FilterPattern)
	if err != nil {
		AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
		return
	}

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
		events, ok := cwStreamEvents(key)
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
				"eventId":       sim.NewUUID(),
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

// A log-group ARN may end in ":*"; the provider tags by the bare ARN.
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
