package main

import (
	"fmt"
	"time"
)

// The PutLogEvents API reference's batch constraints: at most 10,000 events
// and 1,048,576 bytes, each event costing its UTF-8 message plus 26 bytes, no
// event over 1 MB, events in chronological order, and the valid events of a
// batch spanning at most 24 hours.
const (
	cwPutLogEventsMaxEvents = 10000
	cwPutLogEventsMaxBytes  = 1048576
	cwLogEventMaxBytes      = 1048576
	cwPutLogEventsMaxSpan   = 24 * time.Hour
	cwPutLogEventsMaxAge    = 14 * 24 * time.Hour
	cwPutLogEventsMaxAhead  = 2 * time.Hour
)

// cwCheckPutLogEventsBatch returns why the whole batch is refused, or "".
func cwCheckPutLogEventsBatch(events []CWLogEvent) string {
	if len(events) == 0 {
		return "1 validation error detected: Value '[]' at 'logEvents' failed to satisfy constraint: Member must have length greater than or equal to 1"
	}
	if len(events) > cwPutLogEventsMaxEvents {
		return fmt.Sprintf("1 validation error detected: Value at 'logEvents' failed to satisfy constraint: Member must have length less than or equal to %d", cwPutLogEventsMaxEvents)
	}
	total := 0
	for i, e := range events {
		if e.Message == "" {
			return fmt.Sprintf("1 validation error detected: Value '' at 'logEvents.%d.member.message' failed to satisfy constraint: Member must have length greater than or equal to 1", i+1)
		}
		size := len(e.Message) + cwLogEventOverheadBytes
		if size > cwLogEventMaxBytes {
			return fmt.Sprintf("Log event too large: %d bytes exceeds limit of %d", size, cwLogEventMaxBytes)
		}
		total += size
		if i > 0 && e.Timestamp < events[i-1].Timestamp {
			return "Log events in a single PutLogEvents request must be in chronological order."
		}
	}
	if total > cwPutLogEventsMaxBytes {
		return fmt.Sprintf("Upload too large: %d bytes exceeds limit of %d", total, cwPutLogEventsMaxBytes)
	}
	return ""
}

// cwPutLogEventsWindow partitions a chronological batch into the events
// older than the group's retention (expired) or than 14 days (too old), a
// prefix each, and the events more than two hours ahead (too new), a suffix.
// The end indexes are exclusive and the start index inclusive, as
// RejectedLogEventsInfo defines them.
type cwPutLogEventsWindow struct {
	expiredEnd, tooOldEnd, tooNewStart, count int
}

func cwClassifyPutLogEvents(events []CWLogEvent, now time.Time, retention cwRetention) cwPutLogEventsWindow {
	w := cwPutLogEventsWindow{tooNewStart: len(events), count: len(events)}
	oldest := now.Add(-cwPutLogEventsMaxAge).UnixMilli()
	expiry := int64(-1 << 63)
	if retention > 0 {
		expiry = now.AddDate(0, 0, -int(retention)).UnixMilli()
	}
	newest := now.Add(cwPutLogEventsMaxAhead).UnixMilli()
	for i, e := range events {
		if e.Timestamp < oldest {
			w.tooOldEnd = i + 1
		}
		if e.Timestamp < expiry {
			w.expiredEnd = i + 1
		}
		if e.Timestamp > newest && w.tooNewStart == len(events) {
			w.tooNewStart = i
		}
	}
	return w
}

func (w cwPutLogEventsWindow) accepts(i int) bool {
	return i >= w.tooOldEnd && i >= w.expiredEnd && i < w.tooNewStart
}

func (w cwPutLogEventsWindow) spanRefusal(events []CWLogEvent) string {
	first, last := -1, -1
	for i := range events {
		if w.accepts(i) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first >= 0 && time.Duration(events[last].Timestamp-events[first].Timestamp)*time.Millisecond > cwPutLogEventsMaxSpan {
		return "The batch of log events in a single PutLogEvents request cannot span more than 24 hours."
	}
	return ""
}

func (w cwPutLogEventsWindow) rejectedInfo() map[string]int {
	info := map[string]int{}
	if w.tooOldEnd > 0 {
		info["tooOldLogEventEndIndex"] = w.tooOldEnd
	}
	if w.expiredEnd > 0 {
		info["expiredLogEventEndIndex"] = w.expiredEnd
	}
	if w.tooNewStart < w.count {
		info["tooNewLogEventStartIndex"] = w.tooNewStart
	}
	if len(info) == 0 {
		return nil
	}
	return info
}
