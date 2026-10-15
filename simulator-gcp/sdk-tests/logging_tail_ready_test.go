package gcp_sdk_test

import (
	"context"
	"testing"
	"time"

	vkit "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/protobuf/types/known/durationpb"
)

// openReadyTail opens a TailLogEntries stream on project for filter and
// returns it once it streams, with the matching entries it streamed first.
// A tail streams what is ingested after Cloud Logging reads its request, which
// can be after the caller's next call, so the caller cannot tell from Send
// returning that the tail has started. The tail also follows a log of its own
// and waits for a marker written there to arrive: everything ingested after
// that marker reaches the stream, and a listing made after this returns sees
// everything ingested before it.
func openReadyTail(t *testing.T, tailCtx context.Context, client *vkit.Client, project, filter string) (loggingpb.LoggingServiceV2_TailLogEntriesClient, []*loggingpb.LogEntry) {
	t.Helper()
	markerLog := "projects/" + project + "/logs/tail-ready-" + uniqueName("t")
	stream, err := client.TailLogEntries(tailCtx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&loggingpb.TailLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        "(" + filter + `) OR logName="` + markerLog + `"`,
		BufferWindow:  durationpb.New(0),
	}))
	type received struct {
		resp *loggingpb.TailLogEntriesResponse
		err  error
	}
	responses := make(chan received, 1)
	recv := func() {
		resp, err := stream.Recv()
		responses <- received{resp, err}
	}
	writeMarker := func() {
		_, err := client.WriteLogEntries(tailCtx, &loggingpb.WriteLogEntriesRequest{
			LogName:  markerLog,
			Resource: &monitoredres.MonitoredResource{Type: "global"},
			Entries:  []*loggingpb.LogEntry{{Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "tail ready"}}},
		})
		require.NoError(t, err)
	}
	// Nothing reports when the tail starts streaming, and a marker written
	// before it starts never reaches it, so write a new one until one does.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	writeMarker()
	go recv()
	var early []*loggingpb.LogEntry
	for {
		select {
		case <-ticker.C:
			writeMarker()
		case r := <-responses:
			require.NoError(t, r.err, "the tail on %s never streamed its marker", filter)
			ready := false
			for _, entry := range r.resp.GetEntries() {
				if entry.GetLogName() == markerLog {
					ready = true
					continue
				}
				early = append(early, entry)
			}
			if ready {
				return markerFreeTail{stream, markerLog}, early
			}
			go recv()
		}
	}
}

// markerFreeTail is a tail stream that drops the entries of its marker log.
type markerFreeTail struct {
	loggingpb.LoggingServiceV2_TailLogEntriesClient
	markerLog string
}

func (m markerFreeTail) Recv() (*loggingpb.TailLogEntriesResponse, error) {
	for {
		resp, err := m.LoggingServiceV2_TailLogEntriesClient.Recv()
		if err != nil {
			return resp, err
		}
		var kept []*loggingpb.LogEntry
		for _, entry := range resp.GetEntries() {
			if entry.GetLogName() != m.markerLog {
				kept = append(kept, entry)
			}
		}
		if len(kept) > 0 || len(resp.GetEntries()) == 0 {
			resp.Entries = kept
			return resp, nil
		}
	}
}
