package main

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestLogRetentionDeletesExpiredEventsAndKeepsStreams(t *testing.T) {
	AwaitSimulatorBackground()
	groups, streams, events := cwLogGroups, cwLogStreams, cwLogEvents
	cwLogGroups = sim.MakeStore[CWLogGroup](nil, "cw_log_groups")
	cwLogStreams = sim.MakeStore[CWLogStream](nil, "cw_log_streams")
	cwLogEvents = sim.MakeStore[[]CWLogEvent](nil, "cw_log_events")
	t.Cleanup(func() { cwLogGroups, cwLogStreams, cwLogEvents = groups, streams, events })

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
