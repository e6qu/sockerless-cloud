package main

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func useLogMemoryStores(t *testing.T) {
	t.Helper()
	AwaitSimulatorBackground()
	groups, streams, events := cwLogGroups, cwLogStreams, cwLogEvents
	cwLogGroups = sim.MakeStore[CWLogGroup](nil, "cw_log_groups")
	cwLogStreams = sim.MakeStore[CWLogStream](nil, "cw_log_streams")
	cwLogEvents = sim.MakeStore[[]CWLogEvent](nil, "cw_log_events")
	t.Cleanup(func() { cwLogGroups, cwLogStreams, cwLogEvents = groups, streams, events })
}

func TestLogRetentionDeletesExpiredEventsAndKeepsStreams(t *testing.T) {
	useLogMemoryStores(t)

	now := time.Now()
	old := now.Add(-20 * 24 * time.Hour).UnixMilli()
	recent := now.Add(-time.Hour).UnixMilli()
	put := func(group string, retention int, stream string, timestamps ...int64) string {
		cwLogGroups.Put(group, CWLogGroup{LogGroupName: group, RetentionInDays: retention})
		key := cwEventsKey(group, stream)
		cwLogStreams.Put(key, CWLogStream{LogStreamName: stream, LogGroupName: group,
			FirstEventTimestamp: timestamps[0], LastEventTimestamp: timestamps[len(timestamps)-1]})
		var list []CWLogEvent
		for _, ts := range timestamps {
			list = append(list, CWLogEvent{Timestamp: ts, Message: "m"})
		}
		cwLogEvents.Put(key, list)
		return key
	}
	spanning := put("/kept-14-days", 14, "spanning", old, recent)
	expired := put("/kept-14-days", 14, "expired", old, old+1)
	forever := put("/kept-forever", 0, "old", old)

	if deleted := cwSweepExpiredLogEvents(now); deleted != 3 {
		t.Fatalf("sweep deleted %d events, want 3", deleted)
	}
	for key, want := range map[string]int{spanning: 1, expired: 0, forever: 1} {
		got, ok := cwLogEvents.Get(key)
		if !ok {
			t.Fatalf("stream %s lost its event row", key)
		}
		if len(got) != want {
			t.Errorf("stream %s holds %d events, want %d", key, len(got), want)
		}
		if _, ok := cwLogStreams.Get(key); !ok {
			t.Errorf("stream %s was deleted; retention removes events only", key)
		}
	}
	if deleted := cwSweepExpiredLogEvents(now); deleted != 0 {
		t.Errorf("a second sweep deleted %d events, want 0", deleted)
	}
}

// A stream stored before its first event timestamp was recorded still has its
// expired events removed.
func TestLogRetentionReadsAStreamWithNoFirstTimestamp(t *testing.T) {
	useLogMemoryStores(t)
	now := time.Now()
	cwLogGroups.Put("/g", CWLogGroup{LogGroupName: "/g", RetentionInDays: 1})
	key := cwEventsKey("/g", "s")
	cwLogStreams.Put(key, CWLogStream{LogStreamName: "s", LogGroupName: "/g"})
	cwLogEvents.Put(key, []CWLogEvent{{Timestamp: now.Add(-48 * time.Hour).UnixMilli(), Message: "old"}})
	if deleted := cwSweepExpiredLogEvents(now); deleted != 1 {
		t.Fatalf("sweep deleted %d events, want 1", deleted)
	}
}

func TestAppendedEventsKeepTheStreamRecordInStep(t *testing.T) {
	useLogMemoryStores(t)
	key := cwEventsKey("/g", "s")
	cwLogStreams.Put(key, CWLogStream{LogStreamName: "s", LogGroupName: "/g"})
	cwLogEvents.Put(key, []CWLogEvent{})
	cwAppendLogEvents(key, []CWLogEvent{{Timestamp: 200, IngestionTime: 300}, {Timestamp: 100, IngestionTime: 310}}, nil)
	cwAppendLogEvents(key, []CWLogEvent{{Timestamp: 150, IngestionTime: 305}}, nil)
	got, _ := cwLogStreams.Get(key)
	if got.FirstEventTimestamp != 100 || got.LastEventTimestamp != 200 || got.LastIngestionTime != 310 {
		t.Fatalf("stream record first=%d last=%d ingested=%d, want 100, 200, 310",
			got.FirstEventTimestamp, got.LastEventTimestamp, got.LastIngestionTime)
	}
}

// storedBytes is the compressed size of what a group holds, refreshed from the
// streams that changed.
func TestStoredBytesIsTheCompressedSizeOfTheGroupsEvents(t *testing.T) {
	useLogMemoryStores(t)
	cwLogGroups.Put("/g", CWLogGroup{LogGroupName: "/g"})
	cwLogGroups.Put("/empty", CWLogGroup{LogGroupName: "/empty"})
	key := cwEventsKey("/g", "s")
	cwLogStreams.Put(key, CWLogStream{LogStreamName: "s", LogGroupName: "/g"})
	cwLogEvents.Put(key, []CWLogEvent{})
	var batch []CWLogEvent
	raw := 0
	for i := 0; i < 500; i++ {
		msg := "GET /api/health 200 3ms request-id=abcdef"
		raw += len(msg)
		batch = append(batch, CWLogEvent{Timestamp: int64(i + 1), IngestionTime: 1000, Message: msg})
	}
	cwAppendLogEvents(key, batch, nil)

	cwRefreshStoredBytes()
	group, _ := cwLogGroups.Get("/g")
	if group.StoredBytes <= 0 || group.StoredBytes >= int64(raw) {
		t.Fatalf("storedBytes %d, want a compressed size below the %d bytes ingested", group.StoredBytes, raw)
	}
	if empty, _ := cwLogGroups.Get("/empty"); empty.StoredBytes != 0 {
		t.Errorf("a group with no events stores %d bytes", empty.StoredBytes)
	}
	stream, _ := cwLogStreams.Get(key)
	if stream.CompressedThrough != stream.LastIngestionTime {
		t.Errorf("stream measured through %d, last ingestion %d", stream.CompressedThrough, stream.LastIngestionTime)
	}

	before := group.StoredBytes
	cwAppendLogEvents(key, []CWLogEvent{{Timestamp: 999, IngestionTime: 2000, Message: "a different line entirely, 0123456789"}}, nil)
	cwRefreshStoredBytes()
	if after, _ := cwLogGroups.Get("/g"); after.StoredBytes <= before {
		t.Errorf("storedBytes %d did not grow past %d after new events", after.StoredBytes, before)
	}
}
