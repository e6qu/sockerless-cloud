package main

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ECEvent is one event ElastiCache logs about a resource.
type ECEvent struct {
	Key              string
	SourceIdentifier string
	SourceType       string
	Message          string
	Date             time.Time
}

var (
	ecEvents   sim.Store[ECEvent]
	ecEventSeq atomic.Uint64
)

// The SourceType values DescribeEvents takes.
var ecEventSourceTypes = []string{
	"cache-cluster", "cache-parameter-group", "cache-security-group", "cache-subnet-group",
	"replication-group", "serverless-cache", "serverless-cache-snapshot", "user", "user-group",
}

const (
	// "you can retrieve up to 14 days' worth of events"
	ecEventRetention = 14 * 24 * time.Hour
	// "By default, only the events occurring within the last hour are returned"
	ecEventDefaultDuration = time.Hour
)

func ecOpenEventStore(srv *sim.Server) {
	ecEvents = sim.MakeStore[ECEvent](srv.DB(), "elasticache_events")
}

// ecRecordEvent logs an event about the resource of sourceType named id.
func ecRecordEvent(sourceType, id, message string) {
	now := time.Now().UTC()
	key := fmt.Sprintf("%020d-%08d", now.UnixNano(), ecEventSeq.Add(1)%100_000_000)
	ecEvents.Put(key, ECEvent{Key: key, SourceIdentifier: id, SourceType: sourceType, Message: message, Date: now})
}

func ecParseEventTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func handleECDescribeEvents(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	invalid := func(message string) {
		ecErrorXML(w, "InvalidParameterValue", message, http.StatusBadRequest, requestID)
	}
	sourceType := r.FormValue("SourceType")
	if sourceType != "" && !slices.Contains(ecEventSourceTypes, sourceType) {
		invalid(fmt.Sprintf("Invalid SourceType %q. Valid values: %s.", sourceType, strings.Join(ecEventSourceTypes, " | ")))
		return
	}
	srcID := r.FormValue("SourceIdentifier")
	now := time.Now().UTC()
	end := now
	if v := r.FormValue("EndTime"); v != "" {
		t, ok := ecParseEventTime(v)
		if !ok {
			invalid(fmt.Sprintf("Invalid EndTime %q.", v))
			return
		}
		end = t
	}
	start := end.Add(-ecEventDefaultDuration)
	if v := r.FormValue("Duration"); v != "" {
		minutes, err := strconv.Atoi(v)
		if err != nil || minutes < 0 {
			invalid(fmt.Sprintf("Invalid Duration %q.", v))
			return
		}
		start = end.Add(-time.Duration(minutes) * time.Minute)
	}
	if v := r.FormValue("StartTime"); v != "" {
		t, ok := ecParseEventTime(v)
		if !ok {
			invalid(fmt.Sprintf("Invalid StartTime %q.", v))
			return
		}
		start = t
	}
	maxRecords := 100
	if v := r.FormValue("MaxRecords"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 20 || n > 100 {
			invalid("MaxRecords must be between 20 and 100.")
			return
		}
		maxRecords = n
	}
	marker := r.FormValue("Marker")

	var events []ECEvent
	for _, e := range ecEvents.List() {
		if now.Sub(e.Date) > ecEventRetention {
			ecEvents.Delete(e.Key)
			continue
		}
		if sourceType != "" && e.SourceType != sourceType {
			continue
		}
		if srcID != "" && e.SourceIdentifier != srcID {
			continue
		}
		if e.Date.Before(start) || e.Date.After(end) {
			continue
		}
		events = append(events, e)
	}
	slices.SortFunc(events, func(a, b ECEvent) int { return strings.Compare(b.Key, a.Key) })
	if marker != "" {
		i := slices.IndexFunc(events, func(e ECEvent) bool { return e.Key == marker })
		if i < 0 {
			invalid(fmt.Sprintf("Invalid Marker %q.", marker))
			return
		}
		events = events[i:]
	}
	var b strings.Builder
	if len(events) > maxRecords {
		fmt.Fprintf(&b, "<Marker>%s</Marker>", xmlEscape(events[maxRecords].Key))
		events = events[:maxRecords]
	}
	b.WriteString("<Events>")
	for _, e := range events {
		b.WriteString("<Event>")
		fmt.Fprintf(&b, "<SourceIdentifier>%s</SourceIdentifier>", xmlEscape(e.SourceIdentifier))
		fmt.Fprintf(&b, "<SourceType>%s</SourceType>", xmlEscape(e.SourceType))
		fmt.Fprintf(&b, "<Message>%s</Message>", xmlEscape(e.Message))
		fmt.Fprintf(&b, "<Date>%s</Date>", e.Date.Format("2006-01-02T15:04:05.000Z"))
		b.WriteString("</Event>")
	}
	b.WriteString("</Events>")
	ecXMLResponse(w, "DescribeEvents", b.String(), requestID)
}
