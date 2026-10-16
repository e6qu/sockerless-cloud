package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

type loggingDestinationKind int

const (
	loggingToBucket loggingDestinationKind = iota
	loggingToProject
	loggingToStorage
	loggingToPubSub
	loggingToBigQuery
)

// loggingDestination parses a sink destination into its kind and target: a
// log bucket's or project's resource name, a Cloud Storage bucket, or a
// Pub/Sub topic's or BigQuery dataset's resource name.
func loggingDestination(dest string) (loggingDestinationKind, string, bool) {
	if rest, ok := strings.CutPrefix(dest, "logging.googleapis.com/"); ok {
		if strings.Contains(rest, "/buckets/") {
			return loggingToBucket, rest, true
		}
		if collection, id, found := strings.Cut(rest, "/"); found && collection == "projects" && id != "" && !strings.Contains(id, "/") {
			return loggingToProject, rest, true
		}
		return 0, "", false
	}
	if bucket, ok := strings.CutPrefix(dest, "storage.googleapis.com/"); ok && bucket != "" {
		return loggingToStorage, bucket, true
	}
	if topic, ok := strings.CutPrefix(dest, "pubsub.googleapis.com/"); ok && strings.Contains(topic, "/topics/") {
		return loggingToPubSub, topic, true
	}
	if dataset, ok := strings.CutPrefix(dest, "bigquery.googleapis.com/"); ok && strings.Contains(dataset, "/datasets/") {
		return loggingToBigQuery, dataset, true
	}
	return 0, "", false
}

// loggingAncestors lists the folders and organization above a resource
// container, nearest first, as Cloud Resource Manager holds them.
func loggingAncestors(scope string) []string {
	var out []string
	collection, id, _ := strings.Cut(scope, "/")
	parent := ""
	switch collection {
	case "projects":
		if p, ok := crmResolveProject(id); ok {
			parent = p.Parent
		}
	case "folders":
		parent = loggingFolderParent(scope)
	}
	for parent != "" && len(out) < 32 && !slices.Contains(out, parent) {
		out = append(out, parent)
		if !strings.HasPrefix(parent, "folders/") {
			break
		}
		parent = loggingFolderParent(parent)
	}
	return out
}

func loggingFolderParent(folder string) string {
	if crmFolders == nil {
		return ""
	}
	f, ok := crmFolders.Get(folder)
	if !ok {
		return ""
	}
	return f.Parent
}

type loggingRoute struct {
	sink       string
	kind       loggingDestinationKind
	target     string
	filter     listq.Node
	exclusions []listq.Node
	// depth is 0 for the entry's own resource container and n for its n-th
	// ancestor.
	depth     int
	required  bool
	intercept bool
}

func (rt loggingRoute) matches(doc listq.Doc) bool {
	return rt.filter.Eval(doc) && !slices.ContainsFunc(rt.exclusions, func(ex listq.Node) bool { return ex.Eval(doc) })
}

// loggingScopeSinks parses the enabled sinks scope owns, and with
// childrenOnly, only those that include its children's entries.
func loggingScopeSinks(scope string, depth int, childrenOnly bool) ([]loggingRoute, error) {
	var routes []loggingRoute
	for _, sink := range logSinks.ListPrefix(scope + "/sinks/") {
		s := sink.Item
		if s.Disabled || (childrenOnly && !s.IncludeChildren) {
			continue
		}
		kind, target, ok := loggingDestination(s.Destination)
		if !ok {
			continue
		}
		if kind == loggingToBucket {
			if b, exists := logBuckets.Get(target); !exists || b.LifecycleState != "ACTIVE" {
				continue
			}
		}
		f, err := parseLogFilter(s.Filter)
		if err != nil {
			return nil, fmt.Errorf("sink %s: %w", sink.ID, err)
		}
		rt := loggingRoute{sink: sink.ID, kind: kind, target: target, filter: f, depth: depth,
			required: lastSegment(sink.ID) == loggingRequiredID, intercept: childrenOnly && s.InterceptChildren}
		for _, ex := range s.Exclusions {
			if ex.Disabled {
				continue
			}
			exf, err := parseLogFilter(ex.Filter)
			if err != nil {
				return nil, fmt.Errorf("sink %s exclusion %s: %w", sink.ID, ex.Name, err)
			}
			rt.exclusions = append(rt.exclusions, exf)
		}
		routes = append(routes, rt)
	}
	return routes, nil
}

// loggingRouter routes the entries of one write: it resolves each resource
// container's sinks once.
type loggingRouter struct {
	now     time.Time
	cutoffs map[string]int64
	// export sends what sinks to Cloud Storage and Pub/Sub match; a migration
	// routes entries already exported and leaves it off.
	export bool
	scopes map[string][]loggingRoute
	own    map[string][]loggingRoute
}

func newLoggingRouter(now time.Time, export bool) *loggingRouter {
	return &loggingRouter{now: now, cutoffs: loggingBucketCutoffs(now), export: export,
		scopes: map[string][]loggingRoute{}, own: map[string][]loggingRoute{}}
}

// routesFor returns the sinks that see an entry of scope: scope's own, and
// those of its ancestors that include their children's entries.
func (r *loggingRouter) routesFor(scope string) ([]loggingRoute, error) {
	if routes, ok := r.scopes[scope]; ok {
		return routes, nil
	}
	loggingEnsureDefaults(scope)
	routes, err := loggingScopeSinks(scope, 0, false)
	if err != nil {
		return nil, err
	}
	for i, ancestor := range loggingAncestors(scope) {
		above, err := loggingScopeSinks(ancestor, i+1, true)
		if err != nil {
			return nil, err
		}
		routes = append(routes, above...)
	}
	r.scopes[scope] = routes
	return routes, nil
}

func (r *loggingRouter) ownRoutes(scope string) ([]loggingRoute, error) {
	if routes, ok := r.own[scope]; ok {
		return routes, nil
	}
	loggingEnsureDefaults(scope)
	routes, err := loggingScopeSinks(scope, 0, false)
	if err != nil {
		return nil, err
	}
	r.own[scope] = routes
	return routes, nil
}

// route returns the log buckets that keep a copy of entry, and sends it to
// the Cloud Storage and Pub/Sub sinks that match it. A sink of an ancestor
// that intercepts its children's entries keeps a matching entry from every
// sink below it but the _Required sink. A sink whose destination is a project
// hands the entry to that project's own sinks. An entry stamped more than a
// day ahead goes nowhere.
func (r *loggingRouter) route(entry LogEntry, at int64) ([]string, error) {
	if at > r.now.Add(loggingFutureWindow).UnixMilli() {
		return nil, nil
	}
	doc, err := listq.ToDoc(entry)
	if err != nil {
		return nil, err
	}
	routes, err := r.routesFor(loggingScopeOf(entry.LogName))
	if err != nil {
		return nil, err
	}
	interceptDepth := 0
	for _, rt := range routes {
		if rt.intercept && rt.depth > interceptDepth && rt.matches(doc) {
			interceptDepth = rt.depth
		}
	}
	var buckets []string
	for _, rt := range routes {
		if rt.depth < interceptDepth && !rt.required {
			continue
		}
		if !rt.matches(doc) {
			continue
		}
		if rt.kind == loggingToProject {
			hop, err := r.ownRoutes(rt.target)
			if err != nil {
				return nil, err
			}
			for _, next := range hop {
				if next.kind != loggingToProject && next.matches(doc) {
					buckets = r.deliver(next, entry, at, buckets)
				}
			}
			continue
		}
		buckets = r.deliver(rt, entry, at, buckets)
	}
	return buckets, nil
}

// cutoff is the oldest entry time a bucket's retention keeps, read when the
// router first needs it, since a write can create the bucket it routes to.
func (r *loggingRouter) cutoff(bucket string) int64 {
	if c, ok := r.cutoffs[bucket]; ok {
		return c
	}
	b, ok := logBuckets.Get(bucket)
	if !ok {
		return r.now.UnixMilli()
	}
	r.cutoffs[bucket] = loggingRetentionCutoff(b, r.now)
	return r.cutoffs[bucket]
}

func (r *loggingRouter) deliver(rt loggingRoute, entry LogEntry, at int64, buckets []string) []string {
	switch rt.kind {
	case loggingToBucket:
		if !slices.Contains(buckets, rt.target) && at >= r.cutoff(rt.target) {
			buckets = append(buckets, rt.target)
		}
	case loggingToPubSub:
		if r.export {
			loggingExportToPubSub(rt, entry)
		}
	case loggingToStorage:
		if r.export {
			loggingQueueStorageExport(rt, entry, r.now)
		}
	case loggingToBigQuery:
		if r.export {
			if err := loggingExportToBigQuery(rt, entry); err != nil {
				log.Printf("cloud logging sink %s: %v", rt.sink, err)
			}
		}
	}
	return buckets
}

// loggingExportToPubSub publishes entry to a sink's topic as Cloud Logging
// does: the message data is the LogEntry as JSON, and the
// logging.googleapis.com/timestamp attribute carries its timestamp.
func loggingExportToPubSub(rt loggingRoute, entry LogEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("cloud logging sink %s: %v", rt.sink, err)
		return
	}
	if _, err := psPublishMessages(rt.target, []PSMessage{{
		Data:       base64.StdEncoding.EncodeToString(data),
		Attributes: map[string]string{"logging.googleapis.com/timestamp": entry.Timestamp},
	}}); err != nil {
		log.Printf("cloud logging sink %s: %v", rt.sink, err)
	}
}

// loggingStorageBatch holds the entries a sink routed to one Cloud Storage
// object's hour of one log since the last delivery.
type loggingStorageBatch struct {
	Bucket string            `json:"bucket"`
	Prefix string            `json:"prefix"`
	Lines  []json.RawMessage `json:"lines"`
	Queued int64             `json:"queued"`
}

var logStorageBatches sim.Store[loggingStorageBatch]

// loggingStorageObjectPrefix names the hourly object of an entry's log, less
// its shard suffix: {log id}/{YYYY}/{MM}/{DD}/{HH}:00:00_{HH}:59:59.
func loggingStorageObjectPrefix(entry LogEntry) (string, error) {
	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return "", fmt.Errorf("entry %s has timestamp %q: %w", entry.InsertID, entry.Timestamp, err)
	}
	_, logID, _ := strings.Cut(entry.LogName, "/logs/")
	if decoded, err := url.PathUnescape(logID); err == nil {
		logID = decoded
	}
	ts = ts.UTC()
	return fmt.Sprintf("%s/%s/%02d:00:00_%02d:59:59", logID, ts.Format("2006/01/02"), ts.Hour(), ts.Hour()), nil
}

func loggingQueueStorageExport(rt loggingRoute, entry LogEntry, now time.Time) {
	prefix, err := loggingStorageObjectPrefix(entry)
	if err != nil {
		log.Printf("cloud logging sink %s: %v", rt.sink, err)
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		log.Printf("cloud logging sink %s: %v", rt.sink, err)
		return
	}
	logStorageBatches.Upsert(rt.target+"\x00"+prefix, func(b *loggingStorageBatch) {
		b.Bucket, b.Prefix = rt.target, prefix
		b.Lines = append(b.Lines, line)
		b.Queued = now.UnixMilli()
	})
}

// loggingStorageFlushInterval is how often batches whose hour of ingestion
// has passed are checked for delivery.
const loggingStorageFlushInterval = time.Minute

// loggingFlushStorageExports delivers, as Cloud Logging does in hourly
// batches, every batch last added to before the hour now falls in. Each
// delivery writes a new shard of the hour's object, _S0.json first, holding
// the entries as newline-delimited JSON. It returns how many objects it wrote.
func loggingFlushStorageExports(now time.Time) int {
	hour := now.UTC().Truncate(time.Hour).UnixMilli()
	written := 0
	for _, row := range logStorageBatches.List() {
		if row.Queued >= hour {
			continue
		}
		var body []byte
		for _, line := range row.Lines {
			body = append(body, line...)
			body = append(body, '\n')
		}
		var object string
		for shard := 0; ; shard++ {
			object = fmt.Sprintf("%s_S%d.json", row.Prefix, shard)
			if _, err := GCSObjectBytes(row.Bucket, object); err != nil {
				break
			}
		}
		if _, err := persistGCSObjectBytes(row.Bucket, object, body, GCSObject{ContentType: "application/json"}, gcsPreconditions{}); err != nil {
			log.Printf("cloud logging export to gs://%s/%s: %v", row.Bucket, object, err)
		} else {
			written++
		}
		logStorageBatches.Delete(row.Bucket + "\x00" + row.Prefix)
	}
	return written
}

// loggingDefaultSinkUpdateMask is the mask Cloud Logging applies to a sink
// update that names none.
const loggingDefaultSinkUpdateMask = "destination,filter,includeChildren"

// loggingApplySinkMask overwrites the fields of cur that mask names with
// upd's, and reports a field the mask names that a sink update cannot set.
func loggingApplySinkMask(cur, upd LoggingSink, mask string) (LoggingSink, string) {
	if strings.TrimSpace(mask) == "" {
		mask = loggingDefaultSinkUpdateMask
	}
	for _, field := range strings.Split(mask, ",") {
		switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(field)), "_", "") {
		case "destination":
			cur.Destination = upd.Destination
		case "filter":
			cur.Filter = upd.Filter
		case "description":
			cur.Description = upd.Description
		case "disabled":
			cur.Disabled = upd.Disabled
		case "exclusions":
			cur.Exclusions = upd.Exclusions
		case "includechildren":
			cur.IncludeChildren = upd.IncludeChildren
		case "interceptchildren":
			cur.InterceptChildren = upd.InterceptChildren
		case "bigqueryoptions", "bigqueryoptions.usepartitionedtables":
			cur.BigqueryOptions = upd.BigqueryOptions
		case "":
		default:
			return cur, field
		}
	}
	return cur, ""
}

// loggingUpdateSink reads the sink an update names and applies the request's
// update mask to it, answering the error Cloud Logging returns when it cannot.
func loggingUpdateSink(w http.ResponseWriter, r *http.Request, key string) (LoggingSink, bool) {
	var upd LoggingSink
	if err := sim.ReadJSON(r, &upd); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid sink body: %v", err)
		return LoggingSink{}, false
	}
	cur, ok := logSinks.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sink %s not found", key)
		return LoggingSink{}, false
	}
	sink, bad := loggingApplySinkMask(cur, upd, r.URL.Query().Get("updateMask"))
	if bad != "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "updateMask names %q, which a sink update cannot set", bad)
		return LoggingSink{}, false
	}
	return sink, true
}
