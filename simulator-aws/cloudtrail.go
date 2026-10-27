package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

type CloudTrailTrail struct {
	Name             string
	S3BucketName     string
	S3KeyPrefix      string
	ARN              string
	HomeRegion       string
	Logging          bool
	CreatedAt        string
	Tags             []EC2Tag
	EventSelectors   []map[string]any
	InsightSelectors []map[string]any
}

// Store delivery apart from the trail: it changes on every request, and
// writing the trail each time would invalidate the trail index.
type CloudTrailDelivery struct {
	LatestDelivery string
	// LatestDeliveryError is empty when the attempt succeeded.
	LatestDeliveryAttempt string `json:",omitempty"`
	LatestDeliveryError   string `json:",omitempty"`
}

// A channel routes events from a partner or custom Source into event data
// stores.
type CloudTrailChannel struct {
	ARN          string
	Name         string
	Source       string
	Destinations []map[string]any
	Tags         []EC2Tag
}

type CloudTrailEvent struct {
	EventId      string
	EventName    string
	EventSource  string
	EventTime    string
	Username     string
	AccessKeyId  string
	ReadOnly     bool
	InvokedBy    string
	Resources    []CloudTrailResource
	ErrorCode    string
	ErrorMessage string
	// Seq breaks ties among events sharing a second-granularity EventTime so
	// LookupEvents pages newest-first deterministically. It never reaches the
	// wire.
	Seq int64
}

var cloudTrailSeq int64

// Never fall below the clock in nanoseconds, so a restart needs no scan to
// resume above earlier sequences.
func cloudTrailNextSeq() int64 {
	for {
		last := atomic.LoadInt64(&cloudTrailSeq)
		next := max(last+1, time.Now().UnixNano())
		if atomic.CompareAndSwapInt64(&cloudTrailSeq, last, next) {
			return next
		}
	}
}

const cloudTrailHistoryRetention = 90 * 24 * time.Hour

func cloudTrailSweepHistory(now time.Time) int {
	return cloudTrailEvents.Prune(func(event CloudTrailEvent) bool {
		return !cloudTrailInHistory(event, now)
	})
}

func cloudTrailInHistory(event CloudTrailEvent, now time.Time) bool {
	at, err := time.Parse(time.RFC3339, event.EventTime)
	return err == nil && !at.Before(now.Add(-cloudTrailHistoryRetention))
}

type CloudTrailResource struct {
	ResourceName string
	ResourceType string
}

var (
	cloudTrailTrails     sim.Store[CloudTrailTrail]
	cloudTrailDeliveries sim.Store[CloudTrailDelivery]
	cloudTrailEvents     sim.Store[CloudTrailEvent]
	cloudTrailChannels   sim.Store[CloudTrailChannel]
)

type cloudTrailStatusRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *cloudTrailStatusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *cloudTrailStatusRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_, _ = w.body.Write(p)
	return w.ResponseWriter.Write(p)
}

// AWSError finds the service model through Unwrap.
func (w *cloudTrailStatusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *cloudTrailStatusRecorder) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func registerCloudTrail(r *AWSRouter, srv *sim.Server) {
	cloudTrailTrails = sim.MakeStore[CloudTrailTrail](srv.DB(), "cloudtrail_trails")
	cloudTrailDeliveries = sim.MakeStore[CloudTrailDelivery](srv.DB(), "cloudtrail_deliveries")
	cloudTrailEvents = sim.MakeStore[CloudTrailEvent](srv.DB(), "cloudtrail_events")
	cloudTrailChannels = sim.MakeStore[CloudTrailChannel](srv.DB(), "cloudtrail_channels")
	kvstore.StartSweeper(srv, "CloudTrail event history retention", storeSweepInterval, func(now time.Time) { cloudTrailSweepHistory(now) })

	for _, op := range []string{
		"CreateTrail", "DescribeTrails", "GetTrail", "UpdateTrail", "GetTrailStatus",
		"StartLogging", "StopLogging", "LookupEvents", "DeleteTrail", "ListTrails",
		"AddTags", "RemoveTags", "ListTags", "PutEventSelectors", "GetEventSelectors",
		"PutInsightSelectors", "GetInsightSelectors",
		"CreateChannel", "GetChannel", "ListChannels", "DeleteChannel",
	} {
		handler := handleCloudTrail(op)
		r.Register("CloudTrail_20131101."+op, handler)
		r.Register("com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101."+op, handler)
	}
}

func handleCloudTrail(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch op {
		case "CreateTrail":
			handleCloudTrailCreateTrail(w, r)
		case "DescribeTrails":
			handleCloudTrailDescribeTrails(w, r)
		case "GetTrail":
			handleCloudTrailGetTrail(w, r)
		case "UpdateTrail":
			handleCloudTrailUpdateTrail(w, r)
		case "GetTrailStatus":
			handleCloudTrailGetTrailStatus(w, r)
		case "StartLogging":
			handleCloudTrailStartLogging(w, r)
		case "StopLogging":
			handleCloudTrailStopLogging(w, r)
		case "LookupEvents":
			handleCloudTrailLookupEvents(w, r)
		case "DeleteTrail":
			handleCloudTrailDeleteTrail(w, r)
		case "ListTrails":
			handleCloudTrailListTrails(w, r)
		case "PutInsightSelectors":
			handleCloudTrailPutInsightSelectors(w, r)
		case "GetInsightSelectors":
			handleCloudTrailGetInsightSelectors(w, r)
		case "CreateChannel":
			handleCloudTrailCreateChannel(w, r)
		case "GetChannel":
			handleCloudTrailGetChannel(w, r)
		case "ListChannels":
			handleCloudTrailListChannels(w, r)
		case "DeleteChannel":
			handleCloudTrailDeleteChannel(w, r)
		case "AddTags":
			handleCloudTrailAddTags(w, r)
		case "RemoveTags":
			handleCloudTrailRemoveTags(w, r)
		case "ListTags":
			handleCloudTrailListTags(w, r)
		case "PutEventSelectors":
			handleCloudTrailPutEventSelectors(w, r)
		case "GetEventSelectors":
			handleCloudTrailGetEventSelectors(w, r)
		default:
			cloudTrailError(w, "UnsupportedOperationException", "unsupported CloudTrail operation", http.StatusBadRequest)
		}
	}
}

func handleCloudTrailCreateTrail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string
		S3BucketName string
		S3KeyPrefix  string
		TagsList     []EC2Tag
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.S3BucketName == "" {
		cloudTrailError(w, "InvalidParameterException", "Name and S3BucketName are required", http.StatusBadRequest)
		return
	}
	if _, ok := s3Buckets_.Get(req.S3BucketName); !ok {
		cloudTrailError(w, "S3BucketDoesNotExistException", "S3 bucket does not exist", http.StatusBadRequest)
		return
	}
	trail := CloudTrailTrail{
		Name:         req.Name,
		S3BucketName: req.S3BucketName,
		S3KeyPrefix:  strings.Trim(req.S3KeyPrefix, "/"),
		ARN:          cloudTrailARN(req.Name),
		HomeRegion:   awsRegion(),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		Tags:         req.TagsList,
	}
	cloudTrailTrails.Put(req.Name, trail)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"Name":         trail.Name,
		"S3BucketName": trail.S3BucketName,
		"S3KeyPrefix":  trail.S3KeyPrefix,
		"TrailARN":     trail.ARN,
	})
}

func handleCloudTrailDescribeTrails(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrailNameList []string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	trails := make([]map[string]any, 0)
	if len(req.TrailNameList) > 0 {
		for _, name := range req.TrailNameList {
			if trail, ok := findCloudTrail(name); ok {
				trails = append(trails, cloudTrailSummary(trail))
			}
		}
	} else {
		for _, trail := range cloudTrailTrails.List() {
			trails = append(trails, cloudTrailSummary(trail))
		}
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"trailList": trails})
}

func handleCloudTrailGetTrail(w http.ResponseWriter, r *http.Request) {
	trail, ok := cloudTrailTrailFromJSON(w, r)
	if !ok {
		return
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"Trail": cloudTrailSummary(trail)})
}

func handleCloudTrailUpdateTrail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string
		S3BucketName string
		S3KeyPrefix  string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	trail, ok := findCloudTrail(req.Name)
	if !ok {
		cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
		return
	}
	if req.S3BucketName != "" {
		if _, ok := s3Buckets_.Get(req.S3BucketName); !ok {
			cloudTrailError(w, "S3BucketDoesNotExistException", "S3 bucket does not exist", http.StatusBadRequest)
			return
		}
		trail.S3BucketName = req.S3BucketName
	}
	if req.S3KeyPrefix != "" {
		trail.S3KeyPrefix = strings.Trim(req.S3KeyPrefix, "/")
	}
	cloudTrailTrails.Put(trail.Name, trail)
	// UpdateTrailResponse has no HomeRegion member.
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"Name":         trail.Name,
		"S3BucketName": trail.S3BucketName,
		"S3KeyPrefix":  trail.S3KeyPrefix,
		"TrailARN":     trail.ARN,
	})
}

func handleCloudTrailGetTrailStatus(w http.ResponseWriter, r *http.Request) {
	trail, ok := cloudTrailTrailFromJSON(w, r)
	if !ok {
		return
	}
	resp := map[string]any{"IsLogging": trail.Logging}
	if delivery, ok := cloudTrailDeliveries.Get(trail.Name); ok {
		attempt := delivery.LatestDeliveryAttempt
		if attempt == "" {
			attempt = delivery.LatestDelivery
		}
		if attempt != "" {
			resp["LatestDeliveryAttemptTime"] = attempt
		}
		if delivery.LatestDelivery != "" {
			resp["LatestDeliveryTime"] = cloudTrailEpochSeconds(delivery.LatestDelivery)
			resp["LatestDeliveryAttemptSucceeded"] = delivery.LatestDelivery
		}
		if delivery.LatestDeliveryError != "" {
			resp["LatestDeliveryError"] = delivery.LatestDeliveryError
		}
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailStartLogging(w http.ResponseWriter, r *http.Request) {
	trail, ok := cloudTrailTrailFromJSON(w, r)
	if !ok {
		return
	}
	trail.Logging = true
	cloudTrailTrails.Put(trail.Name, trail)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailStopLogging(w http.ResponseWriter, r *http.Request) {
	trail, ok := cloudTrailTrailFromJSON(w, r)
	if !ok {
		return
	}
	trail.Logging = false
	cloudTrailTrails.Put(trail.Name, trail)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailLookupEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LookupAttributes []cloudTrailLookupAttribute
		MaxResults       int
		NextToken        string
		StartTime        *float64
		EndTime          *float64
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	for _, attr := range req.LookupAttributes {
		if !cloudTrailValidLookupKeys[attr.AttributeKey] {
			cloudTrailError(w, "InvalidLookupAttributesException",
				"Invalid lookup attribute key: "+attr.AttributeKey, http.StatusBadRequest)
			return
		}
	}
	// The cursor resumes within the ordered list, so an event recorded between
	// pages is neither duplicated nor skipped.
	now := time.Now()
	history := cloudTrailEvents.Filter(func(event CloudTrailEvent) bool { return cloudTrailInHistory(event, now) })
	matched := cloudTrailMatchedOrdered(history, req.LookupAttributes)
	// The window is inclusive, as in LookupEvents.
	matched = cloudTrailFilterTimeWindow(matched, req.StartTime, req.EndTime)
	pageSize := req.MaxResults
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 50
	}

	start := 0
	if req.NextToken != "" {
		if curTime, curSeq, ok := cloudTrailDecodeToken(req.NextToken); ok {
			start = len(matched)
			for i, ev := range matched {
				if cloudTrailAfterCursor(ev, curTime, curSeq) {
					start = i
					break
				}
			}
		}
	}
	end := start + pageSize
	if end > len(matched) {
		end = len(matched)
	}

	page := make([]map[string]any, 0, end-start)
	for _, ev := range matched[start:end] {
		page = append(page, cloudTrailEventJSON(ev))
	}
	resp := map[string]any{"Events": page}
	if end < len(matched) && end > start {
		resp["NextToken"] = cloudTrailEncodeToken(matched[end-1])
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

type cloudTrailLookupAttribute struct {
	AttributeKey   string
	AttributeValue string
}

// Order by descending (EventTime, Seq), a unique total order pagination
// cursors resume against.
func cloudTrailMatchedOrdered(events []CloudTrailEvent, attrs []cloudTrailLookupAttribute) []CloudTrailEvent {
	ordered := append([]CloudTrailEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339, ordered[i].EventTime)
		right, rightErr := time.Parse(time.RFC3339, ordered[j].EventTime)
		if leftErr == nil && rightErr == nil && !left.Equal(right) {
			return left.After(right)
		}
		if ordered[i].EventTime != ordered[j].EventTime {
			return ordered[i].EventTime > ordered[j].EventTime
		}
		// Same second: newest record first.
		return ordered[i].Seq > ordered[j].Seq
	})
	matched := make([]CloudTrailEvent, 0, len(ordered))
	for _, ev := range ordered {
		if cloudTrailEventMatches(ev, attrs) {
			matched = append(matched, ev)
		}
	}
	return matched
}

// Either bound may be nil; bounds arrive as epoch seconds.
func cloudTrailFilterTimeWindow(events []CloudTrailEvent, start, end *float64) []CloudTrailEvent {
	if start == nil && end == nil {
		return events
	}
	out := make([]CloudTrailEvent, 0, len(events))
	for _, ev := range events {
		t, err := time.Parse(time.RFC3339, ev.EventTime)
		if err != nil {
			continue
		}
		secs := float64(t.Unix())
		if start != nil && secs < *start {
			continue
		}
		if end != nil && secs > *end {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// NextToken names the last-returned event by (EventTime, Seq). Events
// recorded mid-pagination sort before the cursor, so none is revisited or
// skipped.
func cloudTrailEncodeToken(ev CloudTrailEvent) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ev.EventTime + "\x00" + strconv.FormatInt(ev.Seq, 10)))
}

func cloudTrailDecodeToken(token string) (eventTime string, seq int64, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", 0, false
	}
	parts := strings.SplitN(string(raw), "\x00", 2)
	if len(parts) != 2 {
		return "", 0, false
	}
	seq, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return parts[0], seq, true
}

func cloudTrailAfterCursor(ev CloudTrailEvent, curTime string, curSeq int64) bool {
	if ev.EventTime != curTime {
		return ev.EventTime < curTime
	}
	return ev.Seq < curSeq
}

func handleCloudTrailDeleteTrail(w http.ResponseWriter, r *http.Request) {
	trail, ok := cloudTrailTrailFromJSON(w, r)
	if !ok {
		return
	}
	cloudTrailTrails.Delete(trail.Name)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailListTrails(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	trails := make([]map[string]any, 0)
	for _, trail := range cloudTrailTrails.List() {
		trails = append(trails, map[string]any{
			"TrailARN":   trail.ARN,
			"Name":       trail.Name,
			"HomeRegion": trail.HomeRegion,
		})
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"Trails": trails})
}

func handleCloudTrailPutInsightSelectors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrailName        string
		InsightSelectors []map[string]any
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	trail, ok := findCloudTrail(req.TrailName)
	if !ok {
		cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
		return
	}
	for _, sel := range req.InsightSelectors {
		insightType, _ := sel["InsightType"].(string)
		if insightType != "ApiCallRateInsight" && insightType != "ApiErrorRateInsight" {
			cloudTrailError(w, "InvalidInsightSelectorsException",
				"Invalid InsightType: "+insightType, http.StatusBadRequest)
			return
		}
	}
	trail.InsightSelectors = req.InsightSelectors
	cloudTrailTrails.Put(trail.Name, trail)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"TrailARN":         trail.ARN,
		"InsightSelectors": trail.InsightSelectors,
	})
}

func handleCloudTrailGetInsightSelectors(w http.ResponseWriter, r *http.Request) {
	trail, ok := cloudTrailTrailByTrailName(w, r)
	if !ok {
		return
	}
	resp := map[string]any{"TrailARN": trail.ARN}
	if len(trail.InsightSelectors) > 0 {
		resp["InsightSelectors"] = trail.InsightSelectors
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string
		Source       string
		Destinations []map[string]any
		Tags         []EC2Tag
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.Source == "" || len(req.Destinations) == 0 {
		cloudTrailError(w, "InvalidParameterException",
			"Name, Source, and Destinations are required", http.StatusBadRequest)
		return
	}
	for _, ch := range cloudTrailChannels.List() {
		if ch.Name == req.Name {
			cloudTrailError(w, "ChannelAlreadyExistsException",
				"A channel with the specified name already exists", http.StatusBadRequest)
			return
		}
	}
	channel := CloudTrailChannel{
		ARN:          cloudTrailChannelARN(sim.NewUUID()),
		Name:         req.Name,
		Source:       req.Source,
		Destinations: req.Destinations,
		Tags:         req.Tags,
	}
	cloudTrailChannels.Put(channel.ARN, channel)
	resp := map[string]any{
		"ChannelArn":   channel.ARN,
		"Name":         channel.Name,
		"Source":       channel.Source,
		"Destinations": channel.Destinations,
	}
	if len(channel.Tags) > 0 {
		resp["Tags"] = channel.Tags
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleCloudTrailGetChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	channel, ok := findCloudTrailChannel(req.Channel)
	if !ok {
		cloudTrailError(w, "ChannelNotFoundException", "Channel not found", http.StatusNotFound)
		return
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"ChannelArn":   channel.ARN,
		"Name":         channel.Name,
		"Source":       channel.Source,
		"Destinations": channel.Destinations,
		"SourceConfig": map[string]any{"ApplyToAllRegions": true},
	})
}

func handleCloudTrailListChannels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int
		NextToken  string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	channels := make([]map[string]any, 0)
	for _, ch := range cloudTrailChannels.List() {
		channels = append(channels, map[string]any{
			"ChannelArn": ch.ARN,
			"Name":       ch.Name,
		})
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"Channels": channels})
}

func handleCloudTrailDeleteChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	channel, ok := findCloudTrailChannel(req.Channel)
	if !ok {
		cloudTrailError(w, "ChannelNotFoundException", "Channel not found", http.StatusNotFound)
		return
	}
	cloudTrailChannels.Delete(channel.ARN)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func findCloudTrailChannel(nameOrARN string) (CloudTrailChannel, bool) {
	if ch, ok := cloudTrailChannels.Get(nameOrARN); ok {
		return ch, true
	}
	for _, ch := range cloudTrailChannels.List() {
		if ch.Name == nameOrARN {
			return ch, true
		}
	}
	return CloudTrailChannel{}, false
}

func cloudTrailChannelARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudtrail:%s:%s:channel/%s", awsRegion(), awsAccountID(), id)
}

func handleCloudTrailAddTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceId string
		TagsList   []EC2Tag
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	trail, ok := findCloudTrail(req.ResourceId)
	if !ok {
		cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
		return
	}
	for _, tag := range req.TagsList {
		found := false
		for i := range trail.Tags {
			if trail.Tags[i].Key == tag.Key {
				trail.Tags[i].Value = tag.Value
				found = true
				break
			}
		}
		if !found {
			trail.Tags = append(trail.Tags, tag)
		}
	}
	cloudTrailTrails.Put(trail.Name, trail)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailRemoveTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceId string
		TagsList   []EC2Tag
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	trail, ok := findCloudTrail(req.ResourceId)
	if !ok {
		cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
		return
	}
	for _, remove := range req.TagsList {
		keep := trail.Tags[:0]
		for _, tag := range trail.Tags {
			if tag.Key != remove.Key {
				keep = append(keep, tag)
			}
		}
		trail.Tags = keep
	}
	cloudTrailTrails.Put(trail.Name, trail)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleCloudTrailListTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceIdList []string
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	var out []map[string]any
	for _, resourceID := range req.ResourceIdList {
		trail, ok := findCloudTrail(resourceID)
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"ResourceId": trail.ARN,
			"TagsList":   trail.Tags,
		})
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"ResourceTagList": out})
}

func handleCloudTrailPutEventSelectors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrailName      string
		EventSelectors []map[string]any
	}
	if !readAWSJSON(w, r, &req) {
		return
	}
	trail, ok := findCloudTrail(req.TrailName)
	if !ok {
		cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
		return
	}
	trail.EventSelectors = req.EventSelectors
	cloudTrailTrails.Put(trail.Name, trail)
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"TrailARN":       trail.ARN,
		"EventSelectors": trail.EventSelectors,
	})
}

func handleCloudTrailGetEventSelectors(w http.ResponseWriter, r *http.Request) {
	trail, ok := cloudTrailTrailByTrailName(w, r)
	if !ok {
		return
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{
		"TrailARN":       trail.ARN,
		"EventSelectors": trail.EventSelectors,
	})
}

// The selector operations key the trail by TrailName, not Name.
func cloudTrailTrailByTrailName(w http.ResponseWriter, r *http.Request) (CloudTrailTrail, bool) {
	var req struct {
		TrailName string
	}
	if !readAWSJSON(w, r, &req) {
		return CloudTrailTrail{}, false
	}
	trail, ok := findCloudTrail(req.TrailName)
	if !ok {
		cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
		return CloudTrailTrail{}, false
	}
	return trail, true
}

func cloudTrailTrailFromJSON(w http.ResponseWriter, r *http.Request) (CloudTrailTrail, bool) {
	var req struct {
		Name string
	}
	if !readAWSJSON(w, r, &req) {
		return CloudTrailTrail{}, false
	}
	trail, ok := findCloudTrail(req.Name)
	if !ok {
		cloudTrailError(w, "TrailNotFoundException", "Trail not found", http.StatusNotFound)
		return CloudTrailTrail{}, false
	}
	return trail, true
}

func findCloudTrail(nameOrARN string) (CloudTrailTrail, bool) {
	if trail, ok := cloudTrailTrails.Get(nameOrARN); ok {
		return trail, true
	}
	for _, trail := range cloudTrailTrails.List() {
		if trail.ARN == nameOrARN {
			return trail, true
		}
	}
	return CloudTrailTrail{}, false
}

func cloudTrailSummary(trail CloudTrailTrail) map[string]any {
	return map[string]any{
		"Name":         trail.Name,
		"S3BucketName": trail.S3BucketName,
		"S3KeyPrefix":  trail.S3KeyPrefix,
		"TrailARN":     trail.ARN,
		"HomeRegion":   trail.HomeRegion,
	}
}

func cloudTrailARN(name string) string {
	return fmt.Sprintf("arn:aws:cloudtrail:%s:%s:trail/%s", awsRegion(), awsAccountID(), name)
}

// The handler has consumed r.Body, so reqBody carries the buffered payload
// for extracting resources[].
func cloudTrailRecordAPICall(srv *sim.Server, r *http.Request, reqBody []byte, status int, respHeaders http.Header, respBody []byte) {
	if cloudTrailEvents == nil || status >= 500 {
		return
	}
	eventName := awsRequestOperationName(r)
	if eventName == "" {
		return
	}
	// CloudTrail does not log its own LookupEvents, which would grow the trail
	// under a paginating reader.
	if eventName == "LookupEvents" {
		return
	}
	source, ok := awsEventSource(r)
	if !ok {
		// An unmapped event source is a gap in awsEventSourceByTargetPrefix or
		// awsEventSourceByQueryVersion; log it rather than record a made-up source.
		logger := srv.Logger()
		logger.Warn().
			Str("event", eventName).
			Str("x-amz-target", r.Header.Get("X-Amz-Target")).
			Str("version", r.FormValue("Version")).
			Msg("cloudtrail: no eventSource mapping for service slice; event not recorded")
		return
	}
	if !cloudTrailShouldRecord(r, source, eventName) {
		return
	}
	errorCode, errorMessage := cloudTrailErrorFields(status, respHeaders, respBody)
	cloudTrailRecord(CloudTrailEvent{
		EventName:    eventName,
		EventSource:  source,
		AccessKeyId:  cloudTrailAccessKeyID(r),
		InvokedBy:    cloudTrailInvokedBy(r),
		ReadOnly:     cloudTrailReadOnly(eventName),
		Resources:    cloudTrailResources(source, eventName, reqBody, r),
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	})
}

type cloudTrailRESTEventNameFunc func(*http.Request, []byte) string
type cloudTrailRESTResourceFunc func(*http.Request, []byte) []CloudTrailResource

// The service-conformance gate reads this registry to measure REST services'
// operation coverage. Dynamic registrations are not captured.
var restRegisteredOps = map[string]map[string]bool{}

func restRegisterOp(source, op string) {
	if restRegisteredOps[source] == nil {
		restRegisteredOps[source] = map[string]bool{}
	}
	restRegisteredOps[source][op] = true
}

func cloudTrailRecordedREST(eventName, source string, resources cloudTrailRESTResourceFunc, handler http.HandlerFunc) http.HandlerFunc {
	restRegisterOp(source, eventName)
	return cloudTrailRecordedRESTDynamic(func(*http.Request, []byte) string { return eventName }, source, resources, handler)
}

func cloudTrailRecordedRESTDynamic(eventName cloudTrailRESTEventNameFunc, source string, resources cloudTrailRESTResourceFunc, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		// CloudTrail records no request the front end could not authenticate.
		if !awsRESTAuthenticate(w, r, source, body) {
			return
		}
		if source == "s3.amazonaws.com" {
			if _, serr := sigv4VerifyS3(r, nil); serr != nil {
				sigv4WriteS3Error(w, r, serr)
				return
			}
		}
		rec := &cloudTrailStatusRecorder{ResponseWriter: w}
		if iamEnforceRESTRoute(rec, r, source) {
			handler(rec, r)
		}
		if cloudTrailEvents == nil || rec.statusCode() >= 500 {
			return
		}
		name := eventName(r, body)
		if name == "" {
			return
		}
		if !cloudTrailShouldRecord(r, source, name) {
			return
		}
		errorCode, errorMessage := cloudTrailErrorFields(rec.statusCode(), rec.Header(), rec.body.Bytes())
		ev := CloudTrailEvent{
			EventName:    name,
			EventSource:  source,
			AccessKeyId:  cloudTrailAccessKeyID(r),
			InvokedBy:    cloudTrailInvokedBy(r),
			ReadOnly:     cloudTrailReadOnly(name),
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		}
		if resources != nil {
			ev.Resources = resources(r, body)
		}
		cloudTrailRecord(ev)
	}
}

func cloudTrailRESTResource(resourceType string, params ...string) cloudTrailRESTResourceFunc {
	return func(r *http.Request, _ []byte) []CloudTrailResource {
		for _, param := range params {
			if value := r.PathValue(param); value != "" {
				return []CloudTrailResource{{ResourceType: resourceType, ResourceName: cloudTrailShortName(value)}}
			}
		}
		return nil
	}
}

func cloudTrailErrorFields(status int, headers http.Header, body []byte) (string, string) {
	if status < 400 {
		return "", ""
	}
	code := cleanCloudTrailErrorCode(headers.Get("X-Amzn-Errortype"))
	var msg string
	if len(body) > 0 {
		var raw map[string]any
		if json.Unmarshal(body, &raw) == nil {
			if code == "" {
				code = cleanCloudTrailErrorCode(firstString(raw, "__type", "code", "Code"))
			}
			msg = firstString(raw, "message", "Message")
		}
		if code == "" || msg == "" {
			xmlCode, xmlMsg := cloudTrailXMLErrorFields(body)
			if code == "" {
				code = xmlCode
			}
			if msg == "" {
				msg = xmlMsg
			}
		}
	}
	return code, msg
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok {
			return value
		}
	}
	return ""
}

func cleanCloudTrailErrorCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if i := strings.Index(code, ":"); i >= 0 {
		code = code[:i]
	}
	if i := strings.LastIndex(code, "#"); i >= 0 {
		code = code[i+1:]
	}
	return code
}

func cloudTrailXMLErrorFields(body []byte) (string, string) {
	var envelope struct {
		Error struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(body, &envelope); err == nil && (envelope.Error.Code != "" || envelope.Error.Message != "") {
		return envelope.Error.Code, envelope.Error.Message
	}
	var direct struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(body, &direct); err == nil {
		return direct.Code, direct.Message
	}
	return "", ""
}

// Every recording path funnels through here so events share one shape.
func cloudTrailRecord(ev CloudTrailEvent) {
	if cloudTrailEvents == nil {
		return
	}
	ev.EventId = sim.NewUUID()
	ev.EventTime = time.Now().UTC().Format(time.RFC3339)
	ev.Seq = cloudTrailNextSeq()
	if ev.Username == "" && ev.InvokedBy == "" {
		ev.Username = "sockerless"
	}
	cloudTrailEvents.Put(ev.EventId, ev)
	cloudTrailDeliverEvent(ev)
	cloudTrailLakeIngest(ev)
}

// Empty for an unsigned request; CloudTrail omits AccessKeyId for anonymous
// and service-invoked calls too.
func cloudTrailAccessKeyID(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	i := strings.Index(auth, "Credential=")
	if i < 0 {
		return ""
	}
	cred := auth[i+len("Credential="):]
	if j := strings.IndexAny(cred, "/,"); j >= 0 {
		return cred[:j]
	}
	return cred
}

// AWS sets its own readOnly flag by the operation's verb.
func cloudTrailReadOnly(eventName string) bool {
	for _, p := range []string{
		"Describe", "Get", "List", "Lookup", "BatchGet", "Search", "Scan",
		"Query", "View", "Estimate", "Filter", "Head", "Check", "Validate",
	} {
		if strings.HasPrefix(eventName, p) {
			return true
		}
	}
	return false
}

// CloudTrail never returns data events from LookupEvents, so the recorder
// skips the operations each service declares as data events through
// cloudTrailDeclareDataEvents; everything else is a management event.
var cloudTrailDataEventOps = map[string]map[string]bool{}

// Call it from a service's register function, beside its r.Register calls.
func cloudTrailDeclareDataEvents(source string, ops ...string) {
	m := cloudTrailDataEventOps[source]
	if m == nil {
		m = map[string]bool{}
		cloudTrailDataEventOps[source] = m
	}
	for _, op := range ops {
		m[op] = true
	}
}

func cloudTrailIsDataEvent(source, eventName string) bool {
	return cloudTrailDataEventOps[source][eventName]
}

// LookupEvents serves only authenticated management events, so an
// unauthenticated request such as a bare healthcheck `GET /` records nothing.
func cloudTrailShouldRecord(r *http.Request, source, eventName string) bool {
	if cloudTrailAccessKeyID(r) == "" {
		return false
	}
	return !cloudTrailIsDataEvent(source, eventName)
}

func awsRequestOperationName(r *http.Request) string {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		if idx := strings.LastIndex(target, "."); idx >= 0 {
			return target[idx+1:]
		}
		return target
	}
	if err := r.ParseForm(); err == nil {
		return r.FormValue("Action")
	}
	return ""
}

// Every prefix is distinct past the point sibling services diverge, so order
// does not matter.
var awsEventSourceByTargetPrefix = []struct{ prefix, source string }{
	{"com.amazonaws.cloudtrail", "cloudtrail.amazonaws.com"},
	{"CloudTrail_", "cloudtrail.amazonaws.com"},
	{"AmazonEC2ContainerServiceV", "ecs.amazonaws.com"},
	{"AmazonEC2ContainerRegistry", "ecr.amazonaws.com"},
	{"AmazonSQS", "sqs.amazonaws.com"},
	{"AmazonSSM", "ssm.amazonaws.com"},
	{"AWSBudgetServiceGateway", "budgets.amazonaws.com"},
	{"AWSOrganizationsV", "organizations.amazonaws.com"},
	{"AnyScaleFrontendService", "application-autoscaling.amazonaws.com"},
	{"AWSEvents", "events.amazonaws.com"},
	{"AWSGlue", "glue.amazonaws.com"},
	{"AWSStepFunctions", "states.amazonaws.com"},
	{"AWSWAF_", "wafv2.amazonaws.com"},
	{"CertificateManager", "acm.amazonaws.com"},
	{"ACMPrivateCA", "acm-pca.amazonaws.com"},
	{"CodeBuild_", "codebuild.amazonaws.com"},
	{"DynamoDB_", "dynamodb.amazonaws.com"},
	{"Firehose_", "firehose.amazonaws.com"},
	{"GraniteServiceVersion", "monitoring.amazonaws.com"},
	{"Kinesis_", "kinesis.amazonaws.com"},
	{"Logs_", "logs.amazonaws.com"},
	{"Route53AutoNaming", "servicediscovery.amazonaws.com"},
	{"TrentService", "kms.amazonaws.com"},
	{"secretsmanager", "secretsmanager.amazonaws.com"},
}

// The SDK sends a Version unique per query-protocol service.
var awsEventSourceByQueryVersion = map[string]string{
	"2016-11-15": "ec2.amazonaws.com",
	"2011-01-01": "autoscaling.amazonaws.com",
	"2010-08-01": "monitoring.amazonaws.com",
	"2010-03-31": "sns.amazonaws.com",
	"2015-12-01": "elasticloadbalancing.amazonaws.com",
	"2014-10-31": "rds.amazonaws.com",
	"2010-05-08": "iam.amazonaws.com",
	"2011-06-15": "sts.amazonaws.com",
	"2015-02-02": "elasticache.amazonaws.com",
}

// Never guess: a wrong source silently breaks LookupEvents filtering, so an
// unmapped service returns ok=false.
func awsEventSource(r *http.Request) (string, bool) {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		for _, m := range awsEventSourceByTargetPrefix {
			if strings.HasPrefix(target, m.prefix) {
				return m.source, true
			}
		}
		return "", false
	}
	if src, ok := awsEventSourceByQueryVersion[r.FormValue("Version")]; ok {
		return src, true
	}
	return "", false
}

// The index rebuilds only when a trail is created, started, stopped or
// deleted.
var cloudTrailLoggingTrails sim.GenerationIndex[CloudTrailTrail]

func cloudTrailLoggingTrailKeys(trail CloudTrailTrail) []string {
	if !trail.Logging {
		return nil
	}
	return []string{"logging"}
}

func cloudTrailDeliverEvent(event CloudTrailEvent) {
	for _, trail := range cloudTrailLoggingTrails.LookupAll(cloudTrailTrails, "logging", cloudTrailLoggingTrailKeys) {
		if _, ok := s3Buckets_.Get(trail.S3BucketName); !ok {
			continue
		}
		err := cloudTrailDeliverTo(trail, event)
		cloudTrailDeliveries.Upsert(trail.Name, func(delivery *CloudTrailDelivery) {
			delivery.LatestDeliveryAttempt = event.EventTime
			delivery.LatestDeliveryError = ""
			if err != nil {
				delivery.LatestDeliveryError = err.Error()
				return
			}
			delivery.LatestDelivery = event.EventTime
		})
	}
}

func cloudTrailDeliverTo(trail CloudTrailTrail, event CloudTrailEvent) error {
	body, err := cloudTrailLogBody(event)
	if err != nil {
		return err
	}
	key := s3ObjectKey(trail.S3BucketName, cloudTrailObjectKey(trail, event))
	release := s3ObjectWriters.Lock(key)
	defer release()
	_, err = s3StoreObjectData(S3Object{
		Key:          key,
		ContentType:  "application/json",
		LastModified: time.Now().UTC(),
		Metadata:     map[string]string{"cloudtrail-event-id": event.EventId},
	}, body)
	return err
}

func cloudTrailLogBody(event CloudTrailEvent) ([]byte, error) {
	payload := map[string]any{"Records": []map[string]any{cloudTrailEventRecord(event)}}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		_ = gz.Close()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func cloudTrailObjectKey(trail CloudTrailTrail, event CloudTrailEvent) string {
	t, err := time.Parse(time.RFC3339, event.EventTime)
	if err != nil {
		// A malformed EventTime must not yield a year-0001 Amazon S3 key.
		t = time.Now().UTC()
	}
	prefix := strings.Trim(trail.S3KeyPrefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return fmt.Sprintf("%sAWSLogs/%s/CloudTrail/%s/%04d/%02d/%02d/%s_%s.json.gz",
		prefix, awsAccountID(), awsRegion(), t.Year(), t.Month(), t.Day(), trail.Name, event.EventId)
}

// Attributes AND together. An unsupported key never gets here
// (cloudTrailValidateLookupAttributes rejects it) and would match nothing.
func cloudTrailEventMatches(ev CloudTrailEvent, attrs []cloudTrailLookupAttribute) bool {
	for _, attr := range attrs {
		switch attr.AttributeKey {
		case "EventId":
			if ev.EventId != attr.AttributeValue {
				return false
			}
		case "EventName":
			if ev.EventName != attr.AttributeValue {
				return false
			}
		case "EventSource":
			if ev.EventSource != attr.AttributeValue {
				return false
			}
		case "Username":
			if ev.Username != attr.AttributeValue {
				return false
			}
		case "AccessKeyId":
			if ev.AccessKeyId != attr.AttributeValue {
				return false
			}
		case "ReadOnly":
			if (strings.EqualFold(attr.AttributeValue, "true")) != ev.ReadOnly {
				return false
			}
		case "ResourceName":
			if !cloudTrailHasResource(ev, func(r CloudTrailResource) bool {
				return r.ResourceName == attr.AttributeValue
			}) {
				return false
			}
		case "ResourceType":
			if !cloudTrailHasResource(ev, func(r CloudTrailResource) bool {
				return r.ResourceType == attr.AttributeValue
			}) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func cloudTrailHasResource(ev CloudTrailEvent, pred func(CloudTrailResource) bool) bool {
	for _, r := range ev.Resources {
		if pred(r) {
			return true
		}
	}
	return false
}

// Any other key is an InvalidLookupAttributesException.
var cloudTrailValidLookupKeys = map[string]bool{
	"EventId": true, "EventName": true, "ReadOnly": true, "Username": true,
	"ResourceType": true, "ResourceName": true, "EventSource": true, "AccessKeyId": true,
}

func cloudTrailEventJSON(ev CloudTrailEvent) map[string]any {
	resources := make([]map[string]any, 0, len(ev.Resources))
	for _, res := range ev.Resources {
		resources = append(resources, map[string]any{
			"ResourceType": res.ResourceType,
			"ResourceName": res.ResourceName,
		})
	}
	out := map[string]any{
		"EventId":     ev.EventId,
		"EventName":   ev.EventName,
		"EventSource": ev.EventSource,
		"EventTime":   cloudTrailEpochSeconds(ev.EventTime),
		"Username":    ev.Username,
		// Event.ReadOnly is a string on the wire.
		"ReadOnly":        strconv.FormatBool(ev.ReadOnly),
		"Resources":       resources,
		"CloudTrailEvent": cloudTrailEventRecordJSON(ev),
	}
	if ev.AccessKeyId != "" {
		out["AccessKeyId"] = ev.AccessKeyId
	}
	return out
}

// CloudTrail records the in-process caller as invokedBy and source address.
func cloudTrailInvokedBy(r *http.Request) string {
	if caller, ok := sim.CallerOf(r.Context()); ok && sim.InProcess(r) {
		return caller.Service
	}
	return ""
}

func cloudTrailEventRecordJSON(ev CloudTrailEvent) string {
	raw, _ := json.Marshal(cloudTrailEventRecord(ev))
	return string(raw)
}

func cloudTrailEventRecord(ev CloudTrailEvent) map[string]any {
	identity := map[string]any{}
	if cred, ok := iamTempCreds.Get(ev.AccessKeyId); ok && ev.AccessKeyId != "" && cred.RoleName != "" {
		// Record a role session as the assumed role, with the holding service when a
		// service made the call.
		identity["type"] = "AssumedRole"
		identity["arn"] = cred.PrincipalArn
		identity["accessKeyId"] = ev.AccessKeyId
		identity["accountId"] = awsAccountID()
		if role, ok := iamRoles.Get(cred.RoleName); ok {
			identity["sessionContext"] = map[string]any{"sessionIssuer": map[string]any{
				"type": "Role", "arn": role.Arn, "principalId": role.RoleId, "userName": role.RoleName, "accountId": awsAccountID(),
			}}
		}
		if ev.InvokedBy != "" {
			identity["invokedBy"] = ev.InvokedBy
		}
	} else if ev.InvokedBy != "" {
		identity["type"] = "AWSService"
		identity["invokedBy"] = ev.InvokedBy
	} else {
		identity["type"] = "IAMUser"
		identity["userName"] = ev.Username
		if ev.AccessKeyId != "" {
			identity["accessKeyId"] = ev.AccessKeyId
		}
	}
	resources := make([]map[string]any, 0, len(ev.Resources))
	for _, res := range ev.Resources {
		resources = append(resources, map[string]any{
			"resourceType": res.ResourceType,
			"resourceName": res.ResourceName,
		})
	}
	rec := map[string]any{
		"eventVersion": "1.08",
		"userIdentity": identity,
		"eventTime":    ev.EventTime,
		"eventSource":  ev.EventSource,
		"eventName":    ev.EventName,
		"awsRegion":    awsRegion(),
		"eventID":      ev.EventId,
		"eventType":    "AwsApiCall",
		"readOnly":     ev.ReadOnly,
		"resources":    resources,
	}
	if ev.InvokedBy != "" {
		rec["sourceIPAddress"] = ev.InvokedBy
	}
	// CloudTrail records a failed call's errorCode and errorMessage at the top
	// level.
	if ev.ErrorCode != "" {
		rec["errorCode"] = ev.ErrorCode
	}
	if ev.ErrorMessage != "" {
		rec["errorMessage"] = ev.ErrorMessage
	}
	return rec
}

func cloudTrailEpochSeconds(value string) float64 {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return float64(t.UnixNano()) / float64(time.Second)
}

func readAWSJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := sim.ReadJSON(r, out); err != nil {
		cloudTrailError(w, "InvalidRequestException", "invalid JSON request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeAWSJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func cloudTrailError(w http.ResponseWriter, code, message string, status int) {
	awsWriteError(w, awsErrorEnvelope{Protocol: awsJSON11, Model: "cloudtrail"}, status, code, message, "")
}
