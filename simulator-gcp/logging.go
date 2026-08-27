package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	monitoredres "google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Cloud Logging types (internal representation)

// LogEntry represents a Cloud Logging log entry.
type LogEntry struct {
	LogName     string             `json:"logName"`
	Resource    *MonitoredResource `json:"resource,omitempty"`
	Timestamp   string             `json:"timestamp,omitempty"`
	Severity    string             `json:"severity,omitempty"`
	TextPayload string             `json:"textPayload,omitempty"`
	JsonPayload map[string]any     `json:"jsonPayload,omitempty"`
	InsertID    string             `json:"insertId,omitempty"`
	Labels      map[string]string  `json:"labels,omitempty"`
}

// MonitoredResource represents the monitored resource that produced a log entry.
type MonitoredResource struct {
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels,omitempty"`
}

// MonitoredResourceDescriptor describes a monitored resource type log entries
// may name. The JSON tags are the REST spelling
// (monitoredResourceDescriptors.list).
type MonitoredResourceDescriptor struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	DisplayName string `json:"displayName,omitempty"`
}

// loggingMonitoredResourceDescriptors is the descriptor set this simulator
// serves, and the single source both doors read: the REST
// GET /v2/monitoredResourceDescriptors handler and the gRPC
// ListMonitoredResourceDescriptors method.
var loggingMonitoredResourceDescriptors = []MonitoredResourceDescriptor{
	{Name: "monitoredResourceDescriptors/global", Type: "global", DisplayName: "Global"},
	{Name: "monitoredResourceDescriptors/gce_instance", Type: "gce_instance", DisplayName: "GCE VM Instance"},
	{Name: "monitoredResourceDescriptors/cloud_run_revision", Type: "cloud_run_revision", DisplayName: "Cloud Run Revision"},
}

// Package-level state store shared between HTTP and gRPC handlers.
var logEntries sim.Store[[]LogEntry]
var logSinks sim.Store[LoggingSink]
var logMetrics sim.Store[LoggingMetric]
var logEntriesMu sync.Mutex

type LoggingSink struct {
	Name string `json:"name"`
	// ResourceName is the output-only full path projects/{p}/sinks/{name}.
	// Real Cloud Logging returns the short identifier in Name and the full
	// path here as a distinct field (logging/v2 LogSink.resourceName).
	ResourceName         string             `json:"resourceName,omitempty"`
	Destination          string             `json:"destination"`
	Filter               string             `json:"filter,omitempty"`
	Description          string             `json:"description,omitempty"`
	Disabled             bool               `json:"disabled,omitempty"`
	WriterIdentity       string             `json:"writerIdentity,omitempty"`
	UniqueWriterIdentity bool               `json:"uniqueWriterIdentity,omitempty"`
	Exclusions           []LoggingExclusion `json:"exclusions,omitempty"`
}

type LoggingExclusion struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Filter      string `json:"filter"`
	Disabled    bool   `json:"disabled,omitempty"`
}

type LoggingMetric struct {
	Name             string            `json:"name"`
	Description      string            `json:"description,omitempty"`
	Filter           string            `json:"filter"`
	Disabled         bool              `json:"disabled,omitempty"`
	ValueExtractor   string            `json:"valueExtractor,omitempty"`
	Version          string            `json:"version,omitempty"`
	LabelExtractors  map[string]string `json:"labelExtractors,omitempty"`
	MetricDescriptor map[string]any    `json:"metricDescriptor,omitempty"`
	// BucketOptions is a nested writable object the sim persists verbatim
	// so create→get round-trips byte-exact for distribution metrics.
	BucketOptions json.RawMessage `json:"bucketOptions,omitempty"`
	BucketName    string          `json:"bucketName,omitempty"`
}

// listLogEntries is the shared implementation for listing log entries,
// used by both the REST handler and the gRPC server. It returns the requested
// page plus an opaque numeric next-page token (empty when no more entries
// remain). The token is a start index into the deterministically-ordered,
// filtered entry set; pageSize/pageToken only take effect when pageSize > 0.
func listLogEntries(filter string, resourceNames []string, pageSize int, pageToken string, orderBy string) ([]LogEntry, string) {
	var allEntries []LogEntry
	all := logEntries.List()
	for _, entries := range all {
		allEntries = append(allEntries, entries...)
	}

	// Apply structured filter
	if filter != "" {
		var filtered []LogEntry
		for _, entry := range allEntries {
			if matchesFilter(entry, filter) {
				filtered = append(filtered, entry)
			}
		}
		allEntries = filtered
	}

	// Filter by resource names
	if len(resourceNames) > 0 {
		var filtered []LogEntry
		for _, entry := range allEntries {
			for _, rn := range resourceNames {
				if strings.HasPrefix(entry.LogName, rn) || strings.Contains(entry.LogName, rn) {
					filtered = append(filtered, entry)
					break
				}
			}
		}
		allEntries = filtered
	}

	// Deterministic ordering so page tokens are stable across calls. orderBy
	// "timestamp desc" returns newest-first; the default ("timestamp asc") is
	// oldest-first. Real Cloud Logging only orders on timestamp.
	desc := strings.Contains(strings.ToLower(orderBy), "desc")
	sort.SliceStable(allEntries, func(i, j int) bool {
		if allEntries[i].Timestamp != allEntries[j].Timestamp {
			if desc {
				return allEntries[i].Timestamp > allEntries[j].Timestamp
			}
			return allEntries[i].Timestamp < allEntries[j].Timestamp
		}
		return allEntries[i].InsertID < allEntries[j].InsertID
	})

	start := 0
	if pageToken != "" {
		if n, err := strconv.Atoi(pageToken); err == nil && n >= 0 && n <= len(allEntries) {
			start = n
		}
	}
	allEntries = allEntries[start:]

	next := ""
	if pageSize > 0 && len(allEntries) > pageSize {
		next = strconv.Itoa(start + pageSize)
		allEntries = allEntries[:pageSize]
	}

	if allEntries == nil {
		allEntries = []LogEntry{}
	}
	return allEntries, next
}

// writeLogEntries is the shared implementation for writing log entries,
// used by both the REST handler and the gRPC server.
func writeLogEntries(logName string, resource *MonitoredResource, labels map[string]string, entries []LogEntry) {
	for i := range entries {
		entry := &entries[i]
		if entry.LogName == "" {
			entry.LogName = logName
		}
		if entry.Resource == nil {
			entry.Resource = resource
		}
		if entry.Timestamp == "" {
			entry.Timestamp = nowTimestamp()
		}
		if entry.InsertID == "" {
			entry.InsertID = generateUUID()
		}
		if len(labels) > 0 && entry.Labels == nil {
			entry.Labels = make(map[string]string)
		}
		for k, v := range labels {
			if _, exists := entry.Labels[k]; !exists {
				entry.Labels[k] = v
			}
		}
	}

	logEntriesMu.Lock()
	defer logEntriesMu.Unlock()
	for _, entry := range entries {
		ln := entry.LogName
		prev, _ := logEntries.Get(ln)
		// Build a fresh slice rather than append-in-place: a concurrent reader
		// (listLogEntries → logEntries.List()) holds the stored slice header by
		// value and shares its backing array. Appending into spare capacity
		// would race that reader. A fresh allocation each write is the safe RMW.
		next := make([]LogEntry, 0, len(prev)+1)
		next = append(next, prev...)
		next = append(next, entry)
		// Bound retention per log. Real Cloud Logging ages entries out by a
		// retention period; without a cap this store grows forever (a memory
		// leak in a long-running sim) and listLogEntries re-sorts an ever-
		// larger corpus. Reads filter+sort+paginate (never by index), so
		// dropping the oldest entries is safe.
		if len(next) > maxRetainedLogEntries {
			next = next[len(next)-maxRetainedLogEntries:]
		}
		logEntries.Put(ln, next)
	}
}

// maxRetainedLogEntries bounds the in-memory entries kept per log name.
const maxRetainedLogEntries = 10000

// REST request/response types

// ListLogEntriesRESTRequest is the request body for listing log entries via REST.
type ListLogEntriesRESTRequest struct {
	ResourceNames []string `json:"resourceNames"`
	Filter        string   `json:"filter"`
	OrderBy       string   `json:"orderBy"`
	PageSize      int      `json:"pageSize"`
	PageToken     string   `json:"pageToken"`
}

// ListLogEntriesRESTResponse is the response body for listing log entries via REST.
type ListLogEntriesRESTResponse struct {
	Entries       []LogEntry `json:"entries"`
	NextPageToken string     `json:"nextPageToken,omitempty"`
}

// WriteLogEntriesRESTRequest is the request body for writing log entries via REST.
type WriteLogEntriesRESTRequest struct {
	LogName  string             `json:"logName"`
	Resource *MonitoredResource `json:"resource,omitempty"`
	Labels   map[string]string  `json:"labels,omitempty"`
	Entries  []LogEntry         `json:"entries"`
}

func registerCloudLogging(srv *sim.Server) {
	logEntries = sim.MakeStore[[]LogEntry](srv.DB(), "logging_entries")
	logSinks = sim.MakeStore[LoggingSink](srv.DB(), "logging_sinks")
	logMetrics = sim.MakeStore[LoggingMetric](srv.DB(), "logging_metrics")

	// List log entries (REST)
	srv.HandleFunc("POST /v2/entries:list", func(w http.ResponseWriter, r *http.Request) {
		var req ListLogEntriesRESTRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		entries, next := listLogEntries(req.Filter, req.ResourceNames, req.PageSize, req.PageToken, req.OrderBy)
		sim.WriteJSON(w, http.StatusOK, ListLogEntriesRESTResponse{
			Entries:       entries,
			NextPageToken: next,
		})
	})

	// Write log entries (REST)
	srv.HandleFunc("POST /v2/entries:write", func(w http.ResponseWriter, r *http.Request) {
		var req WriteLogEntriesRESTRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		writeLogEntries(req.LogName, req.Resource, req.Labels, req.Entries)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	srv.HandleFunc("POST /v2/projects/{project}/sinks", handleCreateLoggingSink)
	srv.HandleFunc("GET /v2/projects/{project}/sinks", handleListLoggingSinks)
	srv.HandleFunc("GET /v2/projects/{project}/sinks/{sink}", handleGetLoggingSink)
	srv.HandleFunc("PUT /v2/projects/{project}/sinks/{sink}", handleUpdateLoggingSink)
	srv.HandleFunc("PATCH /v2/projects/{project}/sinks/{sink}", handleUpdateLoggingSink)
	srv.HandleFunc("DELETE /v2/projects/{project}/sinks/{sink}", handleDeleteLoggingSink)

	srv.HandleFunc("POST /v2/projects/{project}/metrics", handleCreateLoggingMetric)
	srv.HandleFunc("GET /v2/projects/{project}/metrics", handleListLoggingMetrics)
	srv.HandleFunc("GET /v2/projects/{project}/metrics/{metric}", handleGetLoggingMetric)
	srv.HandleFunc("PUT /v2/projects/{project}/metrics/{metric}", handleUpdateLoggingMetric)
	srv.HandleFunc("PATCH /v2/projects/{project}/metrics/{metric}", handleUpdateLoggingMetric)
	srv.HandleFunc("DELETE /v2/projects/{project}/metrics/{metric}", handleDeleteLoggingMetric)

	registerCloudLoggingAdmin(srv)
}

func loggingSinkKey(project, sink string) string {
	return fmt.Sprintf("projects/%s/sinks/%s", project, sink)
}

func loggingSinkRequestKey(project, sink string) string {
	if strings.HasPrefix(sink, "projects/") {
		return sink
	}
	return loggingSinkKey(project, sink)
}

func loggingMetricKey(project, metric string) string {
	return fmt.Sprintf("projects/%s/metrics/%s", project, metric)
}

func loggingMetricRequestKey(project, metric string) string {
	if strings.HasPrefix(metric, "projects/") {
		return metric
	}
	return loggingMetricKey(project, metric)
}

func normalizeLoggingSink(project string, sink LoggingSink, uniqueWriter bool) LoggingSink {
	short := strings.TrimPrefix(sink.Name, fmt.Sprintf("projects/%s/sinks/", project))
	if short == "" {
		short = generateUUID()
	}
	sink.Name = loggingSinkKey(project, short)
	if sink.WriterIdentity == "" {
		if uniqueWriter {
			// uniqueWriterIdentity=true: real Cloud Logging mints a dedicated
			// per-sink service account so two sinks never share a writer
			// identity (terraform-provider-google's unique_writer_identity).
			sink.WriterIdentity = fmt.Sprintf("serviceAccount:service-%s@gcp-sa-logging.iam.gserviceaccount.com", short)
		} else {
			sink.WriterIdentity = fmt.Sprintf("serviceAccount:cloud-logs@%s.iam.gserviceaccount.com", project)
		}
	}
	return sink
}

// loggingUniqueWriter reads the uniqueWriterIdentity request flag, which real
// Cloud Logging accepts as a query param on sinks.create / sinks.update (the
// LogSink body field is a sim convenience fallback).
func loggingUniqueWriter(r *http.Request, sink LoggingSink) bool {
	return r.URL.Query().Get("uniqueWriterIdentity") == "true" || sink.UniqueWriterIdentity
}

func normalizeLoggingMetric(project string, metric LoggingMetric) LoggingMetric {
	short := strings.TrimPrefix(metric.Name, fmt.Sprintf("projects/%s/metrics/", project))
	if short == "" {
		short = generateUUID()
	}
	metric.Name = loggingMetricKey(project, short)
	return metric
}

// loggingSinkResponse / loggingMetricResponse strip the stored full-path name
// down to the short resource name. Real Cloud Logging returns the SHORT name in
// LogSink.name / LogMetric.name (e.g. "tf-log-sink"); the sim keys by full path
// internally but must respond with the short name or terraform-provider-google
// plans an in-place/replacement change on every refresh.
func loggingSinkResponse(project string, s LoggingSink) LoggingSink {
	s.Name = strings.TrimPrefix(s.Name, fmt.Sprintf("projects/%s/sinks/", project))
	s.ResourceName = fmt.Sprintf("projects/%s/sinks/%s", project, s.Name)
	return s
}

func loggingMetricResponse(project string, m LoggingMetric) LoggingMetric {
	m.Name = strings.TrimPrefix(m.Name, fmt.Sprintf("projects/%s/metrics/", project))
	return m
}

func handleCreateLoggingSink(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var sink LoggingSink
	if err := sim.ReadJSON(r, &sink); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid sink body: %v", err)
		return
	}
	sink = normalizeLoggingSink(project, sink, loggingUniqueWriter(r, sink))
	logSinks.Put(sink.Name, sink)
	sim.WriteJSON(w, http.StatusOK, loggingSinkResponse(project, sink))
}

func handleListLoggingSinks(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	prefix := fmt.Sprintf("projects/%s/sinks/", project)
	sinks := logSinks.Filter(func(s LoggingSink) bool {
		return strings.HasPrefix(s.Name, prefix)
	})
	sort.Slice(sinks, func(i, j int) bool { return sinks[i].Name < sinks[j].Name })
	page, next, ok := paginateList(w, r, sinks)
	if !ok {
		return
	}
	for i := range page {
		page[i] = loggingSinkResponse(project, page[i])
	}
	resp := map[string]any{"sinks": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleGetLoggingSink(w http.ResponseWriter, r *http.Request) {
	key := loggingSinkRequestKey(sim.PathParam(r, "project"), sim.PathParam(r, "sink"))
	sink, ok := logSinks.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sink %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, loggingSinkResponse(sim.PathParam(r, "project"), sink))
}

func handleUpdateLoggingSink(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	key := loggingSinkRequestKey(project, sim.PathParam(r, "sink"))
	var sink LoggingSink
	if err := sim.ReadJSON(r, &sink); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid sink body: %v", err)
		return
	}
	sink.Name = key
	sink = normalizeLoggingSink(project, sink, loggingUniqueWriter(r, sink))
	logSinks.Put(key, sink)
	sim.WriteJSON(w, http.StatusOK, loggingSinkResponse(project, sink))
}

func handleDeleteLoggingSink(w http.ResponseWriter, r *http.Request) {
	sink := sim.PathParam(r, "sink")
	if !logSinks.Delete(loggingSinkRequestKey(sim.PathParam(r, "project"), sink)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sink %q not found", sink)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCreateLoggingMetric(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var metric LoggingMetric
	if err := sim.ReadJSON(r, &metric); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid metric body: %v", err)
		return
	}
	metric = normalizeLoggingMetric(project, metric)
	logMetrics.Put(metric.Name, metric)
	sim.WriteJSON(w, http.StatusOK, loggingMetricResponse(project, metric))
}

func handleListLoggingMetrics(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	prefix := fmt.Sprintf("projects/%s/metrics/", project)
	metrics := logMetrics.Filter(func(m LoggingMetric) bool {
		return strings.HasPrefix(m.Name, prefix)
	})
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Name < metrics[j].Name })
	page, next, ok := paginateList(w, r, metrics)
	if !ok {
		return
	}
	for i := range page {
		page[i] = loggingMetricResponse(project, page[i])
	}
	resp := map[string]any{"metrics": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleGetLoggingMetric(w http.ResponseWriter, r *http.Request) {
	key := loggingMetricRequestKey(sim.PathParam(r, "project"), sim.PathParam(r, "metric"))
	metric, ok := logMetrics.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "metric %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, loggingMetricResponse(sim.PathParam(r, "project"), metric))
}

func handleUpdateLoggingMetric(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	key := loggingMetricRequestKey(project, sim.PathParam(r, "metric"))
	var metric LoggingMetric
	if err := sim.ReadJSON(r, &metric); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid metric body: %v", err)
		return
	}
	metric.Name = key
	metric = normalizeLoggingMetric(project, metric)
	logMetrics.Put(key, metric)
	sim.WriteJSON(w, http.StatusOK, loggingMetricResponse(project, metric))
}

func handleDeleteLoggingMetric(w http.ResponseWriter, r *http.Request) {
	metric := sim.PathParam(r, "metric")
	if !logMetrics.Delete(loggingMetricRequestKey(sim.PathParam(r, "project"), metric)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "metric %q not found", metric)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// gRPC Cloud Logging server

type loggingServer struct {
	loggingpb.UnimplementedLoggingServiceV2Server
}

func (s *loggingServer) WriteLogEntries(_ context.Context, req *loggingpb.WriteLogEntriesRequest) (*loggingpb.WriteLogEntriesResponse, error) {
	var resource *MonitoredResource
	if req.Resource != nil {
		resource = &MonitoredResource{
			Type:   req.Resource.Type,
			Labels: req.Resource.Labels,
		}
	}

	var entries []LogEntry
	for _, pe := range req.Entries {
		entry := protoToLogEntry(pe)
		entries = append(entries, entry)
	}

	writeLogEntries(req.LogName, resource, req.Labels, entries)
	return &loggingpb.WriteLogEntriesResponse{}, nil
}

func (s *loggingServer) ListLogEntries(_ context.Context, req *loggingpb.ListLogEntriesRequest) (*loggingpb.ListLogEntriesResponse, error) {
	entries, next := listLogEntries(req.Filter, req.ResourceNames, int(req.PageSize), req.PageToken, req.OrderBy)

	var pbEntries []*loggingpb.LogEntry
	for _, e := range entries {
		pbEntries = append(pbEntries, logEntryToProto(e))
	}

	return &loggingpb.ListLogEntriesResponse{
		Entries:       pbEntries,
		NextPageToken: next,
	}, nil
}

// DeleteLog removes every entry a log holds, over the same store the REST
// DELETE .../logs/{log} handler deletes from. A log with no entries simply
// stops existing: Cloud Logging has no separate log resource to miss, and the
// log reappears the moment an entry is written to that name again.
func (s *loggingServer) DeleteLog(_ context.Context, req *loggingpb.DeleteLogRequest) (*emptypb.Empty, error) {
	if req.GetLogName() == "" {
		return nil, status.Error(codes.InvalidArgument, "log_name is required")
	}
	logEntries.Delete(req.GetLogName())
	return &emptypb.Empty{}, nil
}

// ListLogs names the logs under a parent that hold at least one entry — the
// same derivation, over the same store, the REST GET .../logs handler serves.
func (s *loggingServer) ListLogs(_ context.Context, req *loggingpb.ListLogsRequest) (*loggingpb.ListLogsResponse, error) {
	if req.GetParent() == "" {
		return nil, status.Error(codes.InvalidArgument, "parent is required")
	}
	names := loggingListLogsScopes(req.GetParent(), req.GetResourceNames())
	page, next, err := loggingPageOfNames(names, int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	return &loggingpb.ListLogsResponse{LogNames: page, NextPageToken: next}, nil
}

// ListMonitoredResourceDescriptors serves the descriptor set in
// loggingMonitoredResourceDescriptors, which the REST
// monitoredResourceDescriptors.list handler serves too. The set is small enough
// that the REST spelling returns it whole; this door does the same.
func (s *loggingServer) ListMonitoredResourceDescriptors(_ context.Context, req *loggingpb.ListMonitoredResourceDescriptorsRequest) (*loggingpb.ListMonitoredResourceDescriptorsResponse, error) {
	descriptors := make([]*monitoredres.MonitoredResourceDescriptor, 0, len(loggingMonitoredResourceDescriptors))
	for _, d := range loggingMonitoredResourceDescriptors {
		descriptors = append(descriptors, &monitoredres.MonitoredResourceDescriptor{
			Name:        d.Name,
			Type:        d.Type,
			DisplayName: d.DisplayName,
		})
	}
	return &loggingpb.ListMonitoredResourceDescriptorsResponse{ResourceDescriptors: descriptors}, nil
}

// Tail flush bounds. The buffer window is the client's, straight from the
// request: Cloud Logging holds entries that long before returning them so late
// arrivals are not reported out of order, and defaults to two seconds. The
// floor keeps a zero window from spinning; it is the same cadence the Pub/Sub
// streaming pull uses to observe its queues.
const (
	loggingTailDefaultBufferWindow = 2 * time.Second
	loggingTailMaxBufferWindow     = 60 * time.Second
	loggingTailMinFlushInterval    = 50 * time.Millisecond
)

// TailLogEntries streams the entries the log store holds for the tailed
// resources, then keeps streaming whatever is written to them until the client
// goes away. Nothing is synthesised: every entry it sends was written through
// WriteLogEntries (either door), and a tail of a store nothing writes to
// legitimately falls silent after its backlog.
func (s *loggingServer) TailLogEntries(stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	window, err := loggingTailBufferWindow(first)
	if err != nil {
		return err
	}
	flush := max(window, loggingTailMinFlushInterval)

	// The client may re-send its parameters on the same stream; a Recv error is
	// also how the tail learns the client is finished.
	var (
		paramsMu sync.Mutex
		params   = first
	)
	clientErr := make(chan error, 1)
	go func() {
		for {
			next, recvErr := stream.Recv()
			if recvErr != nil {
				clientErr <- recvErr
				return
			}
			paramsMu.Lock()
			params = next
			paramsMu.Unlock()
		}
	}()

	// Entries already streamed are tracked by insert ID rather than by a
	// timestamp cursor, so an entry that lands out of order is still delivered
	// exactly once instead of being skipped for being older than the last send.
	sent := map[string]bool{}
	ticker := time.NewTicker(flush)
	defer ticker.Stop()
	for {
		paramsMu.Lock()
		filter, resourceNames := params.GetFilter(), params.GetResourceNames()
		paramsMu.Unlock()

		entries, _ := listLogEntries(filter, resourceNames, 0, "", "")
		var fresh []*loggingpb.LogEntry
		for _, e := range entries {
			if sent[e.InsertID] {
				continue
			}
			sent[e.InsertID] = true
			fresh = append(fresh, logEntryToProto(e))
		}
		if len(fresh) > 0 {
			if err := stream.Send(&loggingpb.TailLogEntriesResponse{Entries: fresh}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-clientErr:
			if errors.Is(e, io.EOF) {
				return nil
			}
			return e
		case <-ticker.C:
		}
	}
}

// loggingTailBufferWindow reads the tail's buffer window, holding it to the
// 0-60000ms range the API documents.
func loggingTailBufferWindow(req *loggingpb.TailLogEntriesRequest) (time.Duration, error) {
	if req.GetBufferWindow() == nil {
		return loggingTailDefaultBufferWindow, nil
	}
	window := req.GetBufferWindow().AsDuration()
	if window < 0 || window > loggingTailMaxBufferWindow {
		return 0, status.Errorf(codes.InvalidArgument, "buffer_window must be between 0 and %s, got %s", loggingTailMaxBufferWindow, window)
	}
	return window, nil
}

// loggingPageOfNames slices a sorted name list by the numeric offset page token
// the REST list handlers use, so both doors read and mint the same tokens.
func loggingPageOfNames(names []string, pageSize int, pageToken string) ([]string, string, error) {
	start := 0
	if pageToken != "" {
		n, err := strconv.Atoi(pageToken)
		if err != nil || n < 0 || n > len(names) {
			return nil, "", status.Errorf(codes.InvalidArgument, "invalid page_token %q", pageToken)
		}
		start = n
	}
	if pageSize < 0 {
		return nil, "", status.Errorf(codes.InvalidArgument, "invalid page_size %d", pageSize)
	}
	page := names[start:]
	next := ""
	if pageSize > 0 && len(page) > pageSize {
		next = strconv.Itoa(start + pageSize)
		page = page[:pageSize]
	}
	return page, next, nil
}

func registerCloudLoggingGRPC(gs *grpc.Server) {
	loggingpb.RegisterLoggingServiceV2Server(gs, &loggingServer{})
}

// Conversion helpers

func protoToLogEntry(pe *loggingpb.LogEntry) LogEntry {
	entry := LogEntry{
		LogName:  pe.LogName,
		InsertID: pe.InsertId,
		Labels:   pe.Labels,
	}

	if pe.Resource != nil {
		entry.Resource = &MonitoredResource{
			Type:   pe.Resource.Type,
			Labels: pe.Resource.Labels,
		}
	}

	if pe.Timestamp != nil {
		entry.Timestamp = pe.Timestamp.AsTime().Format("2006-01-02T15:04:05.999999999Z07:00")
	}

	if pe.Severity != 0 {
		entry.Severity = pe.Severity.String()
	}

	switch p := pe.Payload.(type) {
	case *loggingpb.LogEntry_TextPayload:
		entry.TextPayload = p.TextPayload
	case *loggingpb.LogEntry_JsonPayload:
		if p.JsonPayload != nil {
			entry.JsonPayload = p.JsonPayload.AsMap()
		}
	}

	return entry
}

func logEntryToProto(e LogEntry) *loggingpb.LogEntry {
	pe := &loggingpb.LogEntry{
		LogName:  e.LogName,
		InsertId: e.InsertID,
		Labels:   e.Labels,
	}

	if e.Resource != nil {
		pe.Resource = &monitoredres.MonitoredResource{
			Type:   e.Resource.Type,
			Labels: e.Resource.Labels,
		}
	}

	if e.Timestamp != "" {
		t, err := parseTimestamp(e.Timestamp)
		if err == nil {
			pe.Timestamp = timestamppb.New(t)
		}
	}

	if e.TextPayload != "" {
		pe.Payload = &loggingpb.LogEntry_TextPayload{TextPayload: e.TextPayload}
	} else if len(e.JsonPayload) > 0 {
		s, err := structpb.NewStruct(e.JsonPayload)
		if err == nil {
			pe.Payload = &loggingpb.LogEntry_JsonPayload{JsonPayload: s}
		}
	}

	return pe
}
