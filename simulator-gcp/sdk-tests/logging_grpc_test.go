package gcp_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	vkit "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"
)

// The Cloud Logging gRPC methods beside WriteLogEntries and ListLogEntries
// (DeleteLog, ListLogs, ListMonitoredResourceDescriptors and TailLogEntries),
// through cloud.google.com/go/logging/apiv2 and, where it has one, logadmin.

// newLoggingV2Client supplies the connection directly: the Cloud Logging SDK
// reads no *_EMULATOR_HOST coordinate.
func newLoggingV2Client(t *testing.T) *vkit.Client {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, simGRPCDialOptions(simTokenSource())...)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	client, err := vkit.NewClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func loggingWriteTextEntry(t *testing.T, c *vkit.Client, logName, text string) {
	t.Helper()
	_, err := c.WriteLogEntries(ctx, &loggingpb.WriteLogEntriesRequest{
		LogName:  logName,
		Resource: &mrpb.MonitoredResource{Type: "global"},
		Entries: []*loggingpb.LogEntry{
			{Payload: &loggingpb.LogEntry_TextPayload{TextPayload: text}},
		},
	})
	require.NoError(t, err)
}

func loggingListLogNames(t *testing.T, c *vkit.Client, parent string) []string {
	t.Helper()
	it := c.ListLogs(ctx, &loggingpb.ListLogsRequest{Parent: parent})
	var names []string
	for {
		name, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		names = append(names, name)
	}
	return names
}

func TestCloudLogging_GRPC_ListLogsAndDeleteLog(t *testing.T) {
	t.Parallel()
	ensureProject(t, "logging-grpc-listlogs")
	c := newLoggingV2Client(t)
	parent := "projects/logging-grpc-listlogs"
	kept := parent + "/logs/kept"
	doomed := parent + "/logs/doomed"
	// The project's creation wrote its Admin Activity audit entry.
	audit := parent + "/logs/cloudaudit.googleapis.com%2Factivity"

	loggingWriteTextEntry(t, c, kept, "kept entry")
	loggingWriteTextEntry(t, c, doomed, "doomed entry")

	// Only logs that hold entries are listed, and the entry that was written is
	// the one the log reports.
	require.Equal(t, []string{audit, doomed, kept}, loggingListLogNames(t, c, parent))

	entry, err := c.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{parent},
		Filter:        `logName="` + doomed + `"`,
	}).Next()
	require.NoError(t, err)
	require.Equal(t, doomed, entry.GetLogName())
	require.Equal(t, "doomed entry", entry.GetTextPayload())

	// DeleteLog removes the log's entries, so the log stops being listed, and
	// writes its own Admin Activity audit entry, so the audit log stays.
	require.NoError(t, c.DeleteLog(ctx, &loggingpb.DeleteLogRequest{LogName: doomed}))
	require.Equal(t, []string{audit, kept}, loggingListLogNames(t, c, parent))

	_, err = c.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{parent},
		Filter:        `logName="` + doomed + `"`,
	}).Next()
	require.ErrorIs(t, err, iterator.Done, "a deleted log holds no entries")

	// The log reappears when it receives a new entry, as Cloud Logging documents.
	loggingWriteTextEntry(t, c, doomed, "written again")
	require.Equal(t, []string{audit, doomed, kept}, loggingListLogNames(t, c, parent))

	// Deleting a log that holds nothing is no error: a log with no entries does
	// not exist to be missed.
	require.NoError(t, c.DeleteLog(ctx, &loggingpb.DeleteLogRequest{LogName: parent + "/logs/never-written"}))

	requireGRPCCode(t, c.DeleteLog(ctx, &loggingpb.DeleteLogRequest{}), codes.InvalidArgument)
}

func TestCloudLogging_GRPC_ListLogsPaginates(t *testing.T) {
	t.Parallel()
	ensureProject(t, "logging-grpc-paging")
	c := newLoggingV2Client(t)
	parent := "projects/logging-grpc-paging"
	written := []string{parent + "/logs/a", parent + "/logs/b", parent + "/logs/c"}
	for _, name := range written {
		loggingWriteTextEntry(t, c, name, "entry")
	}
	// The project's creation wrote its Admin Activity audit entry.
	want := append(written, parent+"/logs/cloudaudit.googleapis.com%2Factivity")

	// PageSize 1 makes the client walk three pages with the listing's tokens.
	it := c.ListLogs(ctx, &loggingpb.ListLogsRequest{Parent: parent, PageSize: 1})
	var got []string
	for {
		name, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		got = append(got, name)
	}
	require.Equal(t, want, got)

	_, err := c.ListLogs(ctx, &loggingpb.ListLogsRequest{Parent: parent, PageToken: "not-a-number"}).Next()
	requireGRPCCode(t, err, codes.InvalidArgument)

	_, err = c.ListLogs(ctx, &loggingpb.ListLogsRequest{}).Next()
	requireGRPCCode(t, err, codes.InvalidArgument)
}

func TestCloudLogging_GRPC_ListMonitoredResourceDescriptors(t *testing.T) {
	t.Parallel()
	c := newLoggingV2Client(t)

	it := c.ListMonitoredResourceDescriptors(ctx, &loggingpb.ListMonitoredResourceDescriptorsRequest{})
	byType := map[string]*mrpb.MonitoredResourceDescriptor{}
	for {
		d, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		byType[d.GetType()] = d
	}
	require.Len(t, byType, 3)
	require.Equal(t, "Global", byType["global"].GetDisplayName())
	require.Equal(t, "monitoredResourceDescriptors/global", byType["global"].GetName())
	require.Equal(t, "GCE VM Instance", byType["gce_instance"].GetDisplayName())
	require.Equal(t, "Cloud Run Revision", byType["cloud_run_revision"].GetDisplayName())

	// The high-level wrapper reads the same set.
	adminTypes := map[string]bool{}
	adminIt := logadminClient(t).ResourceDescriptors(ctx)
	for {
		d, err := adminIt.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		adminTypes[d.GetType()] = true
	}
	require.Equal(t, map[string]bool{"global": true, "gce_instance": true, "cloud_run_revision": true}, adminTypes)

	// REST serves the same descriptors from the same list.
	resp, err := http.Get(baseURL + "/v2/monitoredResourceDescriptors")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var rest struct {
		ResourceDescriptors []struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			DisplayName string `json:"displayName"`
		} `json:"resourceDescriptors"`
	}
	require.NoError(t, json.Unmarshal(body, &rest))
	require.Len(t, rest.ResourceDescriptors, len(byType))
	for _, d := range rest.ResourceDescriptors {
		grpcDescriptor, ok := byType[d.Type]
		require.True(t, ok, "REST serves descriptor %q that gRPC does not", d.Type)
		require.Equal(t, grpcDescriptor.GetName(), d.Name)
		require.Equal(t, grpcDescriptor.GetDisplayName(), d.DisplayName)
	}
}

// TestCloudLogging_GRPC_TailLogEntries: the tail streams entries as they are
// ingested, so what the log held before it opened never arrives, later writes
// do, and a write the filter excludes does not.
func TestCloudLogging_GRPC_TailLogEntries(t *testing.T) {
	t.Parallel()
	c := newLoggingV2Client(t)
	project := uniquename.New("logging-grpc-tail")
	ensureProject(t, project)
	parent := "projects/" + project
	logName := parent + "/logs/tailed"
	other := parent + "/logs/not-tailed"

	loggingWriteTextEntry(t, c, logName, "backlog entry")
	loggingWriteTextEntry(t, c, other, "entry the filter excludes")

	tailCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := c.TailLogEntries(tailCtx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&loggingpb.TailLogEntriesRequest{
		ResourceNames: []string{parent},
		Filter:        `logName="` + logName + `"`,
		// A short window keeps the flush cadence tight; Cloud Logging documents a
		// two-second default.
		BufferWindow: durationpb.New(50 * time.Millisecond),
	}))

	received := make(chan *loggingpb.LogEntry, 64)
	recvErr := make(chan error, 1)
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			for _, entry := range resp.GetEntries() {
				received <- entry
			}
		}
	}()
	// awaitPayload reads the stream up to the entry carrying want. Probes may
	// precede it; nothing else may.
	awaitPayload := func(want string) {
		t.Helper()
		for {
			select {
			case entry := <-received:
				require.Equal(t, logName, entry.GetLogName())
				if entry.GetTextPayload() == want {
					return
				}
				require.Regexp(t, `^probe \d+$`, entry.GetTextPayload(), "the tail carried an entry nothing wrote after it opened")
			case err := <-recvErr:
				t.Fatalf("tail ended while waiting for %q: %v", want, err)
			case <-tailCtx.Done():
				t.Fatalf("tail never carried %q", want)
			}
		}
	}

	// Nothing reports when the service has opened the tail, so write probes on a
	// cadence until one arrives.
	cadence := time.NewTicker(100 * time.Millisecond)
	defer cadence.Stop()
probing:
	for probe := 0; ; probe++ {
		loggingWriteTextEntry(t, c, logName, fmt.Sprintf("probe %d", probe))
		select {
		case entry := <-received:
			require.Regexp(t, `^probe \d+$`, entry.GetTextPayload(), "the tail replayed what the log held before it opened")
			break probing
		case err := <-recvErr:
			t.Fatalf("tail ended before any write reached it: %v", err)
		case <-cadence.C:
		}
	}

	loggingWriteTextEntry(t, c, logName, "live entry")
	awaitPayload("live entry")

	loggingWriteTextEntry(t, c, other, "still excluded")
	loggingWriteTextEntry(t, c, logName, "third entry")
	awaitPayload("third entry")

	require.NoError(t, stream.CloseSend())
	cancel()
}

func TestCloudLogging_GRPC_TailLogEntriesRejectsAnOutOfRangeBufferWindow(t *testing.T) {
	t.Parallel()
	ensureProject(t, "logging-grpc-tail")
	c := newLoggingV2Client(t)
	tailCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	stream, err := c.TailLogEntries(tailCtx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&loggingpb.TailLogEntriesRequest{
		ResourceNames: []string{"projects/logging-grpc-tail"},
		BufferWindow:  durationpb.New(90 * time.Second),
	}))
	_, err = stream.Recv()
	requireGRPCCode(t, err, codes.InvalidArgument)
}
