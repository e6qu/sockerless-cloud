package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The worked examples of the Cloud Run minimum-instances, maximum-instances
// and manual-scaling pages.
func TestCloudRunApportionFollowsTheDocumentedExamples(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []int{6, 4}, cloudRunApportion(10, []int32{60, 40}), "10 over 60/40")
	assert.Equal(t, []int{5, 5}, cloudRunApportion(10, []int32{50, 50}), "10 over 50/50")
	assert.Equal(t, []int{2, 1}, cloudRunApportion(3, []int32{50, 50}), "3 over 50/50: the first-listed revision takes the rounded-up value")
	assert.Equal(t, []int{10, 10, 80}, cloudRunApportion(100, []int32{10, 10, 80}), "100 over 10/10/80")
	assert.Equal(t, []int{1, 1, 0}, cloudRunApportion(2, []int32{34, 33, 33}), "fewer instances than revisions leave the last without one")
	assert.Equal(t, []int{0, 0}, cloudRunApportion(0, []int32{50, 50}))
}

func TestCloudRunEffectiveScaleCombinesServiceAndRevisionBounds(t *testing.T) {
	t.Parallel()
	service := "projects/p/locations/us-central1/services/svc"
	rev := func(name string, scaling *RevisionScaling) RevisionV2 {
		return RevisionV2{Name: service + "/revisions/" + name, Scaling: scaling}
	}
	svc := func(scaling *ServiceScaling, traffic ...TrafficTarget) ServiceV2 {
		return ServiceV2{Name: service, Scaling: scaling, Traffic: traffic}
	}
	split := []TrafficTarget{
		{Type: cloudRunTrafficRevision, Revision: "a", Percent: 50},
		{Type: cloudRunTrafficRevision, Revision: "b", Percent: 50},
		{Type: cloudRunTrafficRevision, Revision: "tagged", Tag: "canary"},
	}

	// "If service min is 10 and revision min is 5, the revision runs 10".
	bounds := cloudRunEffectiveScale(svc(&ServiceScaling{MinInstanceCount: 10}, TrafficTarget{Type: cloudRunTrafficRevision, Revision: "a", Percent: 100}), rev("a", &RevisionScaling{MinInstanceCount: 5}))
	assert.Equal(t, 10, bounds.min)
	// "If service min is 10 but revision max is 5, the revision is strictly capped at 5".
	bounds = cloudRunEffectiveScale(svc(&ServiceScaling{MinInstanceCount: 10}, TrafficTarget{Type: cloudRunTrafficRevision, Revision: "a", Percent: 100}), rev("a", &RevisionScaling{MaxInstanceCount: 5}))
	assert.Equal(t, 5, bounds.min)
	assert.Equal(t, 5, bounds.max)
	// "If service max is 5 and revision max is 10, the revision is capped at 5".
	bounds = cloudRunEffectiveScale(svc(&ServiceScaling{MaxInstanceCount: 5}, TrafficTarget{Type: cloudRunTrafficRevision, Revision: "a", Percent: 100}), rev("a", &RevisionScaling{MaxInstanceCount: 10}))
	assert.Equal(t, 5, bounds.max)

	// Service min 10 at 50/50 with revision A min 6: A runs 6, B 5.
	bounds = cloudRunEffectiveScale(svc(&ServiceScaling{MinInstanceCount: 10}, split...), rev("a", &RevisionScaling{MinInstanceCount: 6}))
	assert.Equal(t, 6, bounds.min)
	bounds = cloudRunEffectiveScale(svc(&ServiceScaling{MinInstanceCount: 10}, split...), rev("b", nil))
	assert.Equal(t, 5, bounds.min)
	// A tag-only revision is not in the split and keeps its own bounds.
	bounds = cloudRunEffectiveScale(svc(&ServiceScaling{MinInstanceCount: 10}, split...), rev("tagged", &RevisionScaling{MinInstanceCount: 1}))
	assert.Equal(t, 1, bounds.min)
	assert.Equal(t, cloudRunDefaultMaxInstances, bounds.max)

	// Manual scaling at 3 over 50/50: the first-listed revision runs exactly
	// 2, the other exactly 1, whatever their own bounds; a tag-only revision
	// without a minimum scales to at most one instance.
	manual := &ServiceScaling{ScalingMode: "MANUAL", ManualInstanceCount: 3}
	bounds = cloudRunEffectiveScale(svc(manual, split...), rev("a", &RevisionScaling{MinInstanceCount: 5, MaxInstanceCount: 1}))
	assert.Equal(t, cloudRunRevisionScaleBounds{min: 2, max: 2, exact: true, concurrency: 1}, bounds)
	bounds = cloudRunEffectiveScale(svc(manual, split...), rev("b", nil))
	assert.Equal(t, cloudRunRevisionScaleBounds{min: 1, max: 1, exact: true, concurrency: 1}, bounds)
	bounds = cloudRunEffectiveScale(svc(manual, split...), rev("tagged", nil))
	assert.Equal(t, cloudRunRevisionScaleBounds{min: 0, max: 1, concurrency: 1}, bounds)
	bounds = cloudRunEffectiveScale(svc(manual, split...), rev("tagged", &RevisionScaling{MinInstanceCount: 2}))
	assert.Equal(t, cloudRunRevisionScaleBounds{min: 2, max: 2, exact: true, concurrency: 1}, bounds)
}

// The special fields of the Logging agent's table leave the payload for the
// entry; the message and every other field stay in it, and the time fields
// are searched in the documented order.
func TestCloudRunLogEntryLiftsTheSpecialFields(t *testing.T) {
	t.Parallel()
	written := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	entry := cloudRunLogEntry(sim.LogLine{Stream: "stdout", Timestamp: written, Text: "plain text"})
	assert.Equal(t, "plain text", entry.TextPayload)
	assert.Nil(t, entry.JsonPayload)
	assert.Equal(t, "2026-03-04T05:06:07Z", entry.Timestamp)

	entry = cloudRunLogEntry(sim.LogLine{Stream: "stderr", Timestamp: written, Text: `{"severity":"error","message":"boom","detail":{"code":7},` +
		`"logging.googleapis.com/labels":{"team":"sim"},"logging.googleapis.com/insertId":"42",` +
		`"logging.googleapis.com/operation":{"id":"get_data","producer":"app","first":"true"},` +
		`"logging.googleapis.com/sourceLocation":{"file":"get_data.py","line":"142","function":"getData"},` +
		`"logging.googleapis.com/spanId":"000000000000004a","logging.googleapis.com/trace":"projects/p/traces/t",` +
		`"logging.googleapis.com/trace_sampled":false,"httpRequest":{"requestMethod":"GET","status":200,"latency":"0.25s","requestSize":"12"},` +
		`"timestampSeconds":1700000000,"timestampNanos":5,"time":"2026-01-01T00:00:00Z"}`})
	assert.Equal(t, "ERROR", entry.Severity)
	assert.Equal(t, map[string]any{"message": "boom", "detail": map[string]any{"code": float64(7)}, "time": "2026-01-01T00:00:00Z"}, entry.JsonPayload,
		"the message stays, and so does the time field the timestampSeconds pair took precedence over")
	assert.Equal(t, map[string]string{"team": "sim"}, entry.Labels)
	assert.Equal(t, "42", entry.InsertID)
	assert.Equal(t, &LogEntryOperation{ID: "get_data", Producer: "app", First: true}, entry.Operation)
	assert.Equal(t, &LogEntrySourceLocation{File: "get_data.py", Line: 142, Function: "getData"}, entry.SourceLocation)
	assert.Equal(t, "000000000000004a", entry.SpanID)
	assert.Equal(t, "projects/p/traces/t", entry.Trace)
	assert.False(t, entry.TraceSampled)
	require.NotNil(t, entry.HttpRequest)
	assert.Equal(t, "GET", entry.HttpRequest.RequestMethod)
	assert.Equal(t, int32(200), entry.HttpRequest.Status)
	assert.Equal(t, "0.25s", entry.HttpRequest.Latency)
	assert.Equal(t, int64(12), entry.HttpRequest.RequestSize)
	assert.Equal(t, time.Unix(1700000000, 5).UTC().Format(time.RFC3339Nano), entry.Timestamp)

	// A severity the API does not list, labels that are not strings and a
	// JSON array are not lifted.
	entry = cloudRunLogEntry(sim.LogLine{Text: `{"severity":"loud","logging.googleapis.com/labels":{"n":1},"timestamp":{"seconds":1700000000,"nanos":0}}`})
	assert.Empty(t, entry.Severity)
	assert.Nil(t, entry.Labels)
	assert.Equal(t, map[string]any{"severity": "loud", "logging.googleapis.com/labels": map[string]any{"n": float64(1)}}, entry.JsonPayload)
	assert.Equal(t, time.Unix(1700000000, 0).UTC().Format(time.RFC3339Nano), entry.Timestamp)
	entry = cloudRunLogEntry(sim.LogLine{Text: `[1, 2]`})
	assert.Equal(t, `[1, 2]`, entry.TextPayload)
}
