package main

import (
	"encoding/json"
	"fmt"
	"log"
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
// "Amazon RDS event categories and event messages" table lists
// (specs/cloud-api/aws/rds-event-messages.md.gz), DescribeEvents returns those
// of the past 14 days, and a DB instance's events go to the default Amazon
// EventBridge event bus in the shape rds-eventbridge-events.md.gz shows.

// RDSEvent is one recorded Amazon RDS event.
type RDSEvent struct {
	SourceIdentifier string
	SourceType       string
	SourceArn        string
	Message          string
	EventCategories  []string
	EventID          string `json:",omitempty"`
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

// rdsEventKind is one row of the event table: its event ID, category and
// message, with %s where the table names a value.
type rdsEventKind struct {
	id, category, message string
}

var (
	rdsEventInstanceCreated            = rdsEventKind{"RDS-EVENT-0005", "creation", "DB instance created."}
	rdsEventInstanceDeleted            = rdsEventKind{"RDS-EVENT-0003", "deletion", "DB instance deleted."}
	rdsEventInstanceRestarted          = rdsEventKind{"RDS-EVENT-0006", "availability", "DB instance restarted."}
	rdsEventInstanceStopped            = rdsEventKind{"RDS-EVENT-0087", "notification", "DB instance stopped."}
	rdsEventInstanceStarted            = rdsEventKind{"RDS-EVENT-0088", "notification", "DB instance started."}
	rdsEventBackupStarted              = rdsEventKind{"RDS-EVENT-0001", "backup", "Backing up DB instance."}
	rdsEventBackupFinished             = rdsEventKind{"RDS-EVENT-0002", "backup", "Finished DB instance backup."}
	rdsEventInstanceClassModified      = rdsEventKind{"RDS-EVENT-0014", "configuration change", "Finished applying modification to DB instance class."}
	rdsEventMultiAZConversionStarted   = rdsEventKind{"RDS-EVENT-0024", "configuration change", "Applying modification to convert to a Multi-AZ DB instance."}
	rdsEventMultiAZConversionFinished  = rdsEventKind{"RDS-EVENT-0025", "configuration change", "Finished applying modification to convert to a Multi-AZ DB instance."}
	rdsEventSingleAZConversionStarted  = rdsEventKind{"RDS-EVENT-0030", "configuration change", "Applying modification to convert to a standard (Single-AZ) DB instance."}
	rdsEventSingleAZConversionFinished = rdsEventKind{"RDS-EVENT-0029", "configuration change", "Finished applying modification to convert to a standard (Single-AZ) DB instance."}
	rdsEventFailoverStarted            = rdsEventKind{"RDS-EVENT-0013", "failover", "Multi-AZ instance failover started."}
	rdsEventFailoverFinished           = rdsEventKind{"RDS-EVENT-0015", "failover", "Multi-AZ failover to standby complete - DNS propagation may take a few minutes."}
	rdsEventManualSnapshotStarted      = rdsEventKind{"RDS-EVENT-0040", "creation", "Creating manual snapshot."}
	rdsEventManualSnapshotCreated      = rdsEventKind{"RDS-EVENT-0042", "creation", "Manual snapshot created."}
	rdsEventAutomatedSnapshotStarted   = rdsEventKind{"RDS-EVENT-0090", "creation", "Creating automated snapshot."}
	rdsEventAutomatedSnapshotCreated   = rdsEventKind{"RDS-EVENT-0091", "creation", "Automated snapshot created."}
	rdsEventSnapshotDeleted            = rdsEventKind{"RDS-EVENT-0041", "deletion", "Deleted user snapshot."}
	rdsEventParameterUpdated           = rdsEventKind{"RDS-EVENT-0037", "configuration change", "Updated parameter %s to %s with apply method %s."}
)

var (
	rdsEvents        sim.Store[RDSEvent]
	rdsEventSequence atomic.Int64
)

// rdsRecordInstanceEvent records an event of a DB instance.
func rdsRecordInstanceEvent(instance RDSInstance, kind rdsEventKind, args ...any) {
	rdsRecordEvent(RDSEvent{
		SourceIdentifier: instance.DBInstanceIdentifier,
		SourceType:       "db-instance",
		SourceArn:        instance.ARN,
	}, kind, args...)
}

func rdsRecordEvent(event RDSEvent, kind rdsEventKind, args ...any) {
	now := time.Now().UTC()
	event.Recorded = now.UnixNano()
	event.EventID = kind.id
	event.EventCategories = []string{kind.category}
	event.Message = kind.message
	if len(args) > 0 {
		event.Message = fmt.Sprintf(kind.message, args...)
	}
	rdsEvents.Put(fmt.Sprintf("%020d-%d", event.Recorded, rdsEventSequence.Add(1)), event)
	horizon := now.Add(-rdsEventRetention).UnixNano()
	rdsEvents.Prune(func(recorded RDSEvent) bool { return recorded.Recorded < horizon })
	if event.SourceType == "db-instance" {
		rdsPublishEventBridge(event)
	}
}

// rdsPublishEventBridge sends a DB instance event to the default event bus as
// an "RDS DB Instance Event" from aws.rds.
func rdsPublishEventBridge(event RDSEvent) {
	detail, err := json.Marshal(map[string]any{
		"EventCategories":  event.EventCategories,
		"SourceType":       "DB_INSTANCE",
		"SourceArn":        event.SourceArn,
		"Date":             time.Unix(0, event.Recorded).UTC().Format(rdsEventTimeLayout),
		"Message":          event.Message,
		"SourceIdentifier": event.SourceIdentifier,
		"EventID":          event.EventID,
	})
	if err != nil {
		log.Printf("Amazon RDS: encode the EventBridge event of %s: %v", event.SourceIdentifier, err)
		return
	}
	ebPutServiceEvent("aws.rds", "RDS DB Instance Event", string(detail), []string{event.SourceArn})
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
	for _, event := range rdsEvents.List() {
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
