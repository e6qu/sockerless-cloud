package main

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon RDS records an event for each change of a resource's state that the
// "Amazon RDS event categories and event messages" table lists, and
// DescribeEvents returns those of the past 14 days.

// RDSEvent is one recorded Amazon RDS event.
type RDSEvent struct {
	SourceIdentifier string
	SourceType       string
	SourceArn        string
	Message          string
	EventCategories  []string
	// Recorded is when the event happened, in nanoseconds since the epoch,
	// which orders events recorded within one millisecond.
	Recorded int64
}

const (
	rdsEventRetention = 14 * 24 * time.Hour
	// rdsEventDefaultDuration is the DescribeEvents Duration default, in
	// minutes.
	rdsEventDefaultDuration = 60
	rdsEventDefaultRecords  = 100
	rdsEventTimeLayout      = "2006-01-02T15:04:05.000Z"
)

// The db-instance event messages the Multi-AZ deployment emits.
const (
	rdsEventMultiAZConversionStarted    = "Applying modification to convert to a Multi-AZ DB instance."
	rdsEventMultiAZConversionFinished   = "Finished applying modification to convert to a Multi-AZ DB instance."
	rdsEventSingleAZConversionStarted   = "Applying modification to convert to a standard (Single-AZ) DB instance."
	rdsEventSingleAZConversionFinished  = "Finished applying modification to convert to a standard (Single-AZ) DB instance."
	rdsEventFailoverStarted             = "A Multi-AZ failover that resulted in the promotion of a standby instance has started."
	rdsEventFailoverFinished            = "A Multi-AZ failover that resulted in the promotion of a standby instance is complete. It may take several minutes for the DNS to transfer to the new primary DB instance."
	rdsEventCategoryConfigurationChange = "configuration change"
	rdsEventCategoryFailover            = "failover"
)

var (
	rdsEvents        sim.Store[RDSEvent]
	rdsEventSequence atomic.Int64
)

// rdsRecordInstanceEvent records an event of a DB instance.
func rdsRecordInstanceEvent(instance RDSInstance, category, message string) {
	rdsRecordEvent(RDSEvent{
		SourceIdentifier: instance.DBInstanceIdentifier,
		SourceType:       "db-instance",
		SourceArn:        instance.ARN,
		Message:          message,
		EventCategories:  []string{category},
	})
}

func rdsRecordEvent(event RDSEvent) {
	now := time.Now().UTC()
	event.Recorded = now.UnixNano()
	rdsEvents.Put(fmt.Sprintf("%020d-%d", event.Recorded, rdsEventSequence.Add(1)), event)
	horizon := now.Add(-rdsEventRetention).UnixNano()
	rdsEvents.Prune(func(recorded RDSEvent) bool { return recorded.Recorded < horizon })
}

// rdsEventWindow is the interval a DescribeEvents request reads: StartTime to
// EndTime, either defaulting to Duration minutes from the other, and both to
// the Duration minutes before now.
func rdsEventWindow(r *http.Request, now time.Time) (time.Time, time.Time, string) {
	duration := rdsEventDefaultDuration
	if value := r.FormValue("Duration"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return time.Time{}, time.Time{}, fmt.Sprintf("Invalid value %s for Duration.", value)
		}
		duration = parsed
	}
	span := time.Duration(duration) * time.Minute
	parse := func(name string) (time.Time, bool, string) {
		value := r.FormValue(name)
		if value == "" {
			return time.Time{}, false, ""
		}
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, false, fmt.Sprintf("Invalid value %s for %s.", value, name)
		}
		return at, true, ""
	}
	start, hasStart, problem := parse("StartTime")
	if problem != "" {
		return start, start, problem
	}
	end, hasEnd, problem := parse("EndTime")
	if problem != "" {
		return start, end, problem
	}
	switch {
	case hasStart && hasEnd:
	case hasStart:
		end = now
	case hasEnd:
		start = end.Add(-span)
	default:
		start, end = now.Add(-span), now
	}
	return start, end, ""
}

// rdsInstanceCreationEvents are the creation events of the DB instances,
// dated by each instance's InstanceCreateTime.
func rdsInstanceCreationEvents() []RDSEvent {
	var events []RDSEvent
	for _, instance := range rdsInstances.List() {
		created, err := time.Parse(time.RFC3339, instance.InstanceCreateTime)
		if err != nil {
			continue
		}
		events = append(events, RDSEvent{
			SourceIdentifier: instance.DBInstanceIdentifier,
			SourceType:       "db-instance",
			SourceArn:        instance.ARN,
			Message:          "DB instance created",
			EventCategories:  []string{"creation"},
			Recorded:         created.UnixNano(),
		})
	}
	return events
}

func handleRDSDescribeEvents(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	start, end, problem := rdsEventWindow(r, time.Now().UTC())
	if problem != "" {
		rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, requestID)
		return
	}
	sourceID, sourceType := r.FormValue("SourceIdentifier"), r.FormValue("SourceType")
	categories := parseRDSIndexedStringList(r, "EventCategories.EventCategory")
	var matched []RDSEvent
	for _, event := range append(rdsEvents.List(), rdsInstanceCreationEvents()...) {
		at := time.Unix(0, event.Recorded)
		if at.Before(start) || at.After(end) ||
			(sourceID != "" && event.SourceIdentifier != sourceID) || (sourceType != "" && event.SourceType != sourceType) {
			continue
		}
		if len(categories) > 0 && !slices.ContainsFunc(event.EventCategories, func(category string) bool {
			return slices.Contains(categories, category)
		}) {
			continue
		}
		matched = append(matched, event)
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].Recorded < matched[j].Recorded })
	offset := atoiOrZero(r.FormValue("Marker"))
	if offset > len(matched) {
		offset = len(matched)
	}
	limit := rdsEventDefaultRecords
	if value := atoiOrZero(r.FormValue("MaxRecords")); value > 0 {
		limit = value
	}
	page := matched[offset:]
	marker := ""
	if len(page) > limit {
		page = page[:limit]
		marker = strconv.Itoa(offset + limit)
	}
	var b strings.Builder
	b.WriteString("<Events>")
	for _, event := range page {
		b.WriteString("<Event>")
		fmt.Fprintf(&b, "<SourceIdentifier>%s</SourceIdentifier>", xmlEscape(event.SourceIdentifier))
		fmt.Fprintf(&b, "<SourceType>%s</SourceType>", xmlEscape(event.SourceType))
		fmt.Fprintf(&b, "<Message>%s</Message>", xmlEscape(event.Message))
		b.WriteString("<EventCategories>")
		for _, category := range event.EventCategories {
			fmt.Fprintf(&b, "<EventCategory>%s</EventCategory>", xmlEscape(category))
		}
		b.WriteString("</EventCategories>")
		fmt.Fprintf(&b, "<Date>%s</Date>", time.Unix(0, event.Recorded).UTC().Format(rdsEventTimeLayout))
		fmt.Fprintf(&b, "<SourceArn>%s</SourceArn>", xmlEscape(event.SourceArn))
		b.WriteString("</Event>")
	}
	b.WriteString("</Events>")
	if marker != "" {
		fmt.Fprintf(&b, "<Marker>%s</Marker>", marker)
	}
	rdsXMLResponse(w, "DescribeEvents", b.String(), requestID)
}
