package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/logstore"
)

// Every data-plane request an account serves leaves a sample with its
// container, status and request charge, and every write leaves a snapshot of
// its container's documents. The metrics Microsoft.DocumentDB reports
// aggregate those samples per time grain; the longest retention it advertises
// is 60 days.

type cosmosMetricSample struct {
	DB         string  `json:"db,omitempty"`
	Coll       string  `json:"coll,omitempty"`
	Status     int     `json:"status,omitempty"`
	Charge     float64 `json:"charge,omitempty"`
	Snapshot   bool    `json:"snapshot,omitempty"`
	Documents  int64   `json:"documents,omitempty"`
	DataBytes  int64   `json:"dataBytes,omitempty"`
	IndexBytes int64   `json:"indexBytes,omitempty"`
}

var cosmosMetricLog *logstore.Log[cosmosMetricSample]

const cosmosMetricRetention = 60 * 24 * time.Hour

type cosmosStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *cosmosStatusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *cosmosStatusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *cosmosStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// cosmosRecorded records the request as a sample once it is served, and a
// snapshot of the container after a write.
func cosmosRecorded(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &cosmosStatusWriter{ResponseWriter: w}
		next(sw, r)
		account := cosmosDataAccount(r)
		if account == "" || cosmosMetricLog == nil {
			return
		}
		db, coll := sim.PathParam(r, "database"), sim.PathParam(r, "container")
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		charge, _ := strconv.ParseFloat(w.Header().Get("x-ms-request-charge"), 64)
		now := time.Now().UTC()
		items := []logstore.Item[cosmosMetricSample]{{Time: now.UnixMilli(), Value: cosmosMetricSample{DB: db, Coll: coll, Status: status, Charge: charge}}}
		if coll != "" && r.Method != http.MethodGet && status < 300 {
			snap := cosmosContainerSnapshot(account, db, coll)
			snap.DB, snap.Coll, snap.Snapshot = db, coll, true
			items = append(items, logstore.Item[cosmosMetricSample]{Time: now.UnixMilli(), Value: snap})
		}
		cosmosMetricLog.Append(account, now, items...)
	}
}

// cosmosContainerSnapshot counts a container's documents, the bytes their
// JSON holds, and the bytes of the path-value entries the default consistent
// indexing policy keeps for them.
func cosmosContainerSnapshot(account, db, coll string) cosmosMetricSample {
	var s cosmosMetricSample
	for _, entry := range cosmosDocs.ListPrefix(cosmosDataCollKey(account, db, coll) + "/") {
		data, err := json.Marshal(entry.Item.Body)
		if err != nil {
			continue
		}
		s.Documents++
		s.DataBytes += int64(len(data))
		s.IndexBytes += cosmosIndexBytes("", entry.Item.Body)
	}
	return s
}

func cosmosIndexBytes(path string, v any) int64 {
	switch t := v.(type) {
	case map[string]any:
		var n int64
		for k, child := range t {
			n += cosmosIndexBytes(path+"/"+k, child)
		}
		return n
	case []any:
		var n int64
		for _, child := range t {
			n += cosmosIndexBytes(path+"/[]", child)
		}
		return n
	default:
		data, _ := json.Marshal(t)
		return int64(len(path) + len(data))
	}
}

func cosmosSweepMetricSamples(now time.Time) {
	if cosmosMetricLog == nil {
		return
	}
	for _, scope := range cosmosMetricLog.Scopes("") {
		cosmosMetricLog.Expire(scope, now.Add(-cosmosMetricRetention))
	}
}

// cosmosMetricScope narrows samples to a database and container named by their
// resource ids in the metrics path; empty ids leave the account whole.
type cosmosMetricScope struct {
	account, db, coll string
}

func cosmosMetricScopeOf(r *http.Request) cosmosMetricScope {
	scope := cosmosMetricScope{account: sim.PathParam(r, "account")}
	name := func(ridText string) string {
		rid, ok := cosmosDecodeRID(ridText)
		if !ok {
			return ridText
		}
		rec, ok := cosmosRIDRecordByRID(rid)
		if !ok {
			return ridText
		}
		return rec.Key[strings.LastIndex(rec.Key, "/")+1:]
	}
	if v := sim.PathParam(r, "databaseRid"); v != "" {
		scope.db = name(v)
	}
	if v := sim.PathParam(r, "collectionRid"); v != "" {
		scope.coll = name(v)
	}
	return scope
}

func (s cosmosMetricScope) samples() []logstore.Entry[cosmosMetricSample] {
	if cosmosMetricLog == nil {
		return nil
	}
	var out []logstore.Entry[cosmosMetricSample]
	for _, e := range cosmosMetricLog.Entries(s.account) {
		if s.db != "" && e.Value.DB != s.db {
			continue
		}
		if s.coll != "" && e.Value.Coll != s.coll {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

// cosmosMetricValue aggregates the named metric over [from, to).
func cosmosMetricValue(name string, samples []logstore.Entry[cosmosMetricSample], from, to time.Time) map[string]any {
	lo, hi := from.UnixMilli(), to.UnixMilli()
	count := 0
	var total, minimum, maximum float64
	observe := func(v float64) {
		if count == 0 || v < minimum {
			minimum = v
		}
		if count == 0 || v > maximum {
			maximum = v
		}
		total += v
		count++
	}
	switch name {
	case "Document Count", "Data Usage", "Index Usage":
		// A container's size holds from one snapshot to the next, so a grain
		// with no write carries the last snapshot before it.
		latest := map[string]cosmosMetricSample{}
		for _, e := range samples {
			if !e.Value.Snapshot || e.Time >= hi {
				continue
			}
			latest[e.Value.DB+"/"+e.Value.Coll] = e.Value
		}
		var sum float64
		for _, s := range latest {
			switch name {
			case "Document Count":
				sum += float64(s.Documents)
			case "Data Usage":
				sum += float64(s.DataBytes)
			default:
				sum += float64(s.IndexBytes)
			}
		}
		if len(latest) > 0 {
			observe(sum)
		}
	case "Service Availability":
		served, failed := 0, 0
		for _, e := range samples {
			if e.Value.Snapshot || e.Time < lo || e.Time >= hi {
				continue
			}
			served++
			if e.Value.Status >= 500 {
				failed++
			}
		}
		if served > 0 {
			observe(100 * float64(served-failed) / float64(served))
		}
	default:
		for _, e := range samples {
			if e.Value.Snapshot || e.Time < lo || e.Time >= hi {
				continue
			}
			if name == "Total Request Units" {
				observe(e.Value.Charge)
			} else {
				observe(1)
			}
		}
	}
	average := 0.0
	if count > 0 {
		average = total / float64(count)
	}
	return map[string]any{
		"timestamp": from.UTC().Format(time.RFC3339),
		"_count":    count,
		"average":   average,
		"minimum":   minimum,
		"maximum":   maximum,
		"total":     total,
	}
}
