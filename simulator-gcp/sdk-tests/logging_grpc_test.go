package gcp_sdk_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	vkit "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/durationpb"
)

// These drive the Cloud Logging gRPC methods that sit beside WriteLogEntries
// and ListLogEntries — DeleteLog, ListLogs, ListMonitoredResourceDescriptors
// and the TailLogEntries stream — through the generated
// cloud.google.com/go/logging/apiv2 client and, where it has one, the
// high-level logadmin wrapper.

// newLoggingV2Client builds the generated logging v2 gRPC client against the
// simulator's gRPC port. The Cloud Logging SDK has no *_EMULATOR_HOST
// coordinate, so the connection is supplied directly, the same way the
// logadmin and KMS gRPC tests do it.
func newLoggingV2Client(t *testing.T) *vkit.Client {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	client, err := vkit.NewClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

// loggingWriteTextEntry writes one text-payload entry to a log and returns
// after the write has been accepted, so a subsequent read sees it.
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

// loggingListLogNames collects the log names ListLogs reports for a parent.
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
	c := newLoggingV2Client(t)
	parent := "projects/logging-grpc-listlogs"
	kept := parent + "/logs/kept"
	doomed := parent + "/logs/doomed"

	loggingWriteTextEntry(t, c, kept, "kept entry")
	loggingWriteTextEntry(t, c, doomed, "doomed entry")

	// Only logs that hold entries are listed, and the entry that was written is
	// the one the log reports.
	require.Equal(t, []string{doomed, kept}, loggingListLogNames(t, c, parent))

	entry, err := c.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{parent},
		Filter:        `logName="` + doomed + `"`,
	}).Next()
	require.NoError(t, err)
	require.Equal(t, doomed, entry.GetLogName())
	require.Equal(t, "doomed entry", entry.GetTextPayload())

	// DeleteLog removes the log's entries, so the log stops being listed.
	require.NoError(t, c.DeleteLog(ctx, &loggingpb.DeleteLogRequest{LogName: doomed}))
	require.Equal(t, []string{kept}, loggingListLogNames(t, c, parent))

	_, err = c.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{parent},
		Filter:        `logName="` + doomed + `"`,
	}).Next()
	require.ErrorIs(t, err, iterator.Done, "a deleted log holds no entries")

	// The log reappears when it receives a new entry, exactly as Cloud Logging
	// documents.
	loggingWriteTextEntry(t, c, doomed, "written again")
	require.Equal(t, []string{doomed, kept}, loggingListLogNames(t, c, parent))

	// Deleting a log that holds nothing is not an error; a log resource that
	// holds no entries does not exist to be missed.
	require.NoError(t, c.DeleteLog(ctx, &loggingpb.DeleteLogRequest{LogName: parent + "/logs/never-written"}))

	requireGRPCCode(t, c.DeleteLog(ctx, &loggingpb.DeleteLogRequest{}), codes.InvalidArgument)
}

func TestCloudLogging_GRPC_ListLogsPaginates(t *testing.T) {
	c := newLoggingV2Client(t)
	parent := "projects/logging-grpc-paging"
	want := []string{parent + "/logs/a", parent + "/logs/b", parent + "/logs/c"}
	for _, name := range want {
		loggingWriteTextEntry(t, c, name, "entry")
	}

	// PageSize 1 makes the client walk three pages, so each page token has to
	// be one it can come back with.
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

	// The REST door serves the same descriptors, because both doors read one
	// list.
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

// TestCloudLogging_GRPC_TailLogEntries holds the tail to real writes: the
// backlog it opens with is what the log already held, and the entry written
// afterwards arrives on the same stream. Nothing else is emitted, because
// nothing else was written.
func TestCloudLogging_GRPC_TailLogEntries(t *testing.T) {
	c := newLoggingV2Client(t)
	parent := "projects/logging-grpc-tail"
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
		// A short window keeps the flush cadence tight; the default is the
		// two seconds Cloud Logging documents.
		BufferWindow: durationpb.New(50 * time.Millisecond),
	}))

	backlog, err := stream.Recv()
	require.NoError(t, err)
	require.Len(t, backlog.GetEntries(), 1)
	require.Equal(t, logName, backlog.GetEntries()[0].GetLogName())
	require.Equal(t, "backlog entry", backlog.GetEntries()[0].GetTextPayload())

	// An entry written while the tail is open reaches it.
	loggingWriteTextEntry(t, c, logName, "live entry")
	live, err := stream.Recv()
	require.NoError(t, err)
	require.Len(t, live.GetEntries(), 1)
	require.Equal(t, "live entry", live.GetEntries()[0].GetTextPayload())

	// A write to a log the filter excludes is not tailed: the next thing the
	// stream carries is the next matching write, not the excluded one.
	loggingWriteTextEntry(t, c, other, "still excluded")
	loggingWriteTextEntry(t, c, logName, "third entry")
	third, err := stream.Recv()
	require.NoError(t, err)
	require.Len(t, third.GetEntries(), 1)
	require.Equal(t, "third entry", third.GetEntries()[0].GetTextPayload())

	require.NoError(t, stream.CloseSend())
	cancel()
}

// TestCloudLogging_GRPC_TailLogEntriesRejectsAnOutOfRangeBufferWindow holds the
// tail to the 0-60000ms buffer window the API documents.
func TestCloudLogging_GRPC_TailLogEntriesRejectsAnOutOfRangeBufferWindow(t *testing.T) {
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
