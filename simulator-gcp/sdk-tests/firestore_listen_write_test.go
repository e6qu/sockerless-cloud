package gcp_sdk_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	firestorepb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// The Firestore Write and Listen streams have no REST spelling, so they are
// exercised through the generated gRPC client — the same client the high-level
// cloud.google.com/go/firestore library builds on. The high-level watch API
// (DocumentRef.Snapshots) rides Listen and is exercised below too, which is
// what proves the target/CURRENT/NO_CHANGE bookkeeping is the one a real
// client expects.

// fsRawGRPCClient dials the simulator's Firestore gRPC data plane and returns
// the generated client.
func fsRawGRPCClient(t *testing.T) firestorepb.FirestoreClient {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return firestorepb.NewFirestoreClient(conn)
}

func fsStringValue(s string) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: s}}
}

// TestFirestore_GRPC_WriteStream drives the bidirectional write stream: the
// handshake that mints a stream id and token, writes that land in the same
// store the unary RPCs read, a delete, and a resume from the last token.
func TestFirestore_GRPC_WriteStream(t *testing.T) {
	c := fsRawGRPCClient(t)
	const database = "projects/fs-write-stream/databases/(default)"
	const docName = database + "/documents/inbox/msg1"

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := c.Write(streamCtx)
	require.NoError(t, err)

	// The first request opens the stream and carries no writes.
	require.NoError(t, stream.Send(&firestorepb.WriteRequest{Database: database}))
	opened, err := stream.Recv()
	require.NoError(t, err)
	require.NotEmpty(t, opened.GetStreamId(), "a new stream reports its id")
	require.NotEmpty(t, opened.GetStreamToken(), "every response carries a stream token")
	assert.Empty(t, opened.GetWriteResults(), "the handshake applies no writes")

	// A write on the stream creates the document.
	require.NoError(t, stream.Send(&firestorepb.WriteRequest{
		StreamToken: opened.GetStreamToken(),
		Writes: []*firestorepb.Write{{
			Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{
				Name:   docName,
				Fields: map[string]*firestorepb.Value{"body": fsStringValue("hello")},
			}},
		}},
	}))
	written, err := stream.Recv()
	require.NoError(t, err)
	require.Len(t, written.GetWriteResults(), 1)
	assert.NotNil(t, written.GetWriteResults()[0].GetUpdateTime(), "the write result carries the update time")
	assert.NotNil(t, written.GetCommitTime(), "the response carries the commit time")
	require.NotEmpty(t, written.GetStreamToken())
	assert.Empty(t, written.GetStreamId(), "the stream id is reported only when the stream is created")

	// The unary read path sees it: the stream writes the store the rest of the
	// service reads.
	got, err := c.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: docName})
	require.NoError(t, err)
	assert.Equal(t, "hello", got.GetFields()["body"].GetStringValue())

	// A delete on the stream removes it.
	require.NoError(t, stream.Send(&firestorepb.WriteRequest{
		StreamToken: written.GetStreamToken(),
		Writes:      []*firestorepb.Write{{Operation: &firestorepb.Write_Delete{Delete: docName}}},
	}))
	deleted, err := stream.Recv()
	require.NoError(t, err)
	require.Len(t, deleted.GetWriteResults(), 1)
	_, err = c.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: docName})
	requireGRPCStatus(t, err, codes.NotFound)
	require.NoError(t, stream.CloseSend())

	// A second stream resumes the first from the token it last issued.
	resumeCtx, cancelResume := context.WithTimeout(ctx, 30*time.Second)
	defer cancelResume()
	resumed, err := c.Write(resumeCtx)
	require.NoError(t, err)
	require.NoError(t, resumed.Send(&firestorepb.WriteRequest{
		Database:    database,
		StreamId:    opened.GetStreamId(),
		StreamToken: deleted.GetStreamToken(),
	}))
	resumeResp, err := resumed.Recv()
	require.NoError(t, err)
	assert.Empty(t, resumeResp.GetStreamId(), "a resumed stream is not a new stream")
	assert.NotEmpty(t, resumeResp.GetStreamToken())
	require.NoError(t, resumed.CloseSend())
}

// TestFirestore_GRPC_WriteStreamRejections covers the handshake contract: the
// first request carries no writes, and an unknown stream cannot be resumed.
func TestFirestore_GRPC_WriteStreamRejections(t *testing.T) {
	c := fsRawGRPCClient(t)
	const database = "projects/fs-write-stream-bad/databases/(default)"

	withWrites, err := c.Write(ctx)
	require.NoError(t, err)
	require.NoError(t, withWrites.Send(&firestorepb.WriteRequest{
		Database: database,
		Writes: []*firestorepb.Write{{Operation: &firestorepb.Write_Delete{
			Delete: database + "/documents/inbox/nope",
		}}},
	}))
	_, err = withWrites.Recv()
	requireGRPCStatus(t, err, codes.InvalidArgument)

	unknown, err := c.Write(ctx)
	require.NoError(t, err)
	require.NoError(t, unknown.Send(&firestorepb.WriteRequest{
		Database:    database,
		StreamId:    "no-such-stream",
		StreamToken: []byte("stale"),
	}))
	_, err = unknown.Recv()
	requireGRPCStatus(t, err, codes.InvalidArgument)

	noDatabase, err := c.Write(ctx)
	require.NoError(t, err)
	require.NoError(t, noDatabase.Send(&firestorepb.WriteRequest{}))
	_, err = noDatabase.Recv()
	requireGRPCStatus(t, err, codes.InvalidArgument)
}

// TestFirestore_GRPC_ListenStream drives the watch stream directly: adding a
// documents target reports the document that exists, later writes and deletes
// arrive as changes, and removing the target closes it out.
func TestFirestore_GRPC_ListenStream(t *testing.T) {
	c := fsRawGRPCClient(t)
	const database = "projects/fs-listen-raw/databases/(default)"
	const parent = database + "/documents"
	const docName = parent + "/watched/d1"

	// The document exists before the stream opens; the target must report it.
	_, err := c.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent:       parent,
		CollectionId: "watched",
		DocumentId:   "d1",
		Document:     &firestorepb.Document{Fields: map[string]*firestorepb.Value{"state": fsStringValue("initial")}},
	})
	require.NoError(t, err)

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := c.Listen(streamCtx)
	require.NoError(t, err)

	const targetID = int32(7)
	require.NoError(t, stream.Send(&firestorepb.ListenRequest{
		Database: database,
		TargetChange: &firestorepb.ListenRequest_AddTarget{AddTarget: &firestorepb.Target{
			TargetId: targetID,
			TargetType: &firestorepb.Target_Documents{Documents: &firestorepb.Target_DocumentsTarget{
				Documents: []string{docName},
			}},
		}},
	}))

	added, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, firestorepb.TargetChange_ADD, added.GetTargetChange().GetTargetChangeType())
	assert.Equal(t, []int32{targetID}, added.GetTargetChange().GetTargetIds())

	change, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, change.GetDocumentChange(), "the target reports the document that exists")
	assert.Equal(t, docName, change.GetDocumentChange().GetDocument().GetName())
	assert.Equal(t, "initial", change.GetDocumentChange().GetDocument().GetFields()["state"].GetStringValue())
	assert.Equal(t, []int32{targetID}, change.GetDocumentChange().GetTargetIds())

	current, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, firestorepb.TargetChange_CURRENT, current.GetTargetChange().GetTargetChangeType())

	noChange, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, firestorepb.TargetChange_NO_CHANGE, noChange.GetTargetChange().GetTargetChangeType())
	assert.Empty(t, noChange.GetTargetChange().GetTargetIds(), "a stream-wide snapshot names no targets")
	assert.NotNil(t, noChange.GetTargetChange().GetReadTime())
	assert.NotEmpty(t, noChange.GetTargetChange().GetResumeToken())

	// A write through the unary path reaches the open stream.
	_, err = c.Commit(ctx, &firestorepb.CommitRequest{
		Database: database,
		Writes: []*firestorepb.Write{{
			Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{
				Name:   docName,
				Fields: map[string]*firestorepb.Value{"state": fsStringValue("updated")},
			}},
		}},
	})
	require.NoError(t, err)

	updated := fsAwaitDocumentChange(t, stream, docName)
	assert.Equal(t, "updated", updated.GetFields()["state"].GetStringValue())

	// So does a delete.
	_, err = c.DeleteDocument(ctx, &firestorepb.DeleteDocumentRequest{Name: docName})
	require.NoError(t, err)
	deleted := fsAwaitDocumentDelete(t, stream)
	assert.Equal(t, docName, deleted.GetDocument())
	assert.Equal(t, []int32{targetID}, deleted.GetRemovedTargetIds())

	require.NoError(t, stream.Send(&firestorepb.ListenRequest{
		Database:     database,
		TargetChange: &firestorepb.ListenRequest_RemoveTarget{RemoveTarget: targetID},
	}))
	removed := fsAwaitTargetChange(t, stream, firestorepb.TargetChange_REMOVE)
	assert.Equal(t, []int32{targetID}, removed.GetTargetIds())
}

// TestFirestore_GRPC_ListenQueryTarget watches a collection: documents that
// start matching arrive as changes and documents that stop matching are
// reported as removed from the target's view.
func TestFirestore_GRPC_ListenQueryTarget(t *testing.T) {
	c := fsRawGRPCClient(t)
	const database = "projects/fs-listen-query/databases/(default)"
	const parent = database + "/documents"
	const docName = parent + "/tasks/t1"

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := c.Listen(streamCtx)
	require.NoError(t, err)

	const targetID = int32(11)
	require.NoError(t, stream.Send(&firestorepb.ListenRequest{
		Database: database,
		TargetChange: &firestorepb.ListenRequest_AddTarget{AddTarget: &firestorepb.Target{
			TargetId: targetID,
			TargetType: &firestorepb.Target_Query{Query: &firestorepb.Target_QueryTarget{
				Parent: parent,
				QueryType: &firestorepb.Target_QueryTarget_StructuredQuery{StructuredQuery: &firestorepb.StructuredQuery{
					From: []*firestorepb.StructuredQuery_CollectionSelector{{CollectionId: "tasks"}},
					Where: &firestorepb.StructuredQuery_Filter{FilterType: &firestorepb.StructuredQuery_Filter_FieldFilter{
						FieldFilter: &firestorepb.StructuredQuery_FieldFilter{
							Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: "state"},
							Op:    firestorepb.StructuredQuery_FieldFilter_EQUAL,
							Value: fsStringValue("open"),
						},
					}},
				}},
			}},
		}},
	}))
	fsAwaitTargetChange(t, stream, firestorepb.TargetChange_CURRENT)

	// A matching document appears.
	_, err = c.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent:       parent,
		CollectionId: "tasks",
		DocumentId:   "t1",
		Document:     &firestorepb.Document{Fields: map[string]*firestorepb.Value{"state": fsStringValue("open")}},
	})
	require.NoError(t, err)
	matched := fsAwaitDocumentChange(t, stream, docName)
	assert.Equal(t, "open", matched.GetFields()["state"].GetStringValue())

	// It stops matching without being deleted: the target reports it removed
	// from view, and the document is still readable.
	_, err = c.Commit(ctx, &firestorepb.CommitRequest{
		Database: database,
		Writes: []*firestorepb.Write{{
			Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{
				Name:   docName,
				Fields: map[string]*firestorepb.Value{"state": fsStringValue("done")},
			}},
		}},
	})
	require.NoError(t, err)

	removed := fsAwaitDocumentRemove(t, stream)
	assert.Equal(t, docName, removed.GetDocument())
	assert.Equal(t, []int32{targetID}, removed.GetRemovedTargetIds())
	still, err := c.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: docName})
	require.NoError(t, err)
	assert.Equal(t, "done", still.GetFields()["state"].GetStringValue())
}

// TestFirestore_Snapshots_HighLevelClient runs the watch stream through the
// cloud.google.com/go/firestore client's own state machine, which consumes the
// ADD / CURRENT / NO_CHANGE bookkeeping and only yields a snapshot once the
// stream reports a consistent one.
//
// The update case is covered by TestFirestore_GRPC_ListenStream rather than
// here: the client drops an update whose update time equals the one it already
// holds, and the simulator stamps document update times with millisecond
// granularity, so a write that follows another inside the same millisecond
// carries the same update time and the client cannot see it. That granularity
// is the simulator's, not this stream's — the stream reports every write the
// store records, which is what the raw test asserts.
func TestFirestore_Snapshots_HighLevelClient(t *testing.T) {
	c := newFSGRPCClient(t, "fs-listen-hl")
	doc := c.Collection("live").Doc("d1")
	_, err := doc.Create(ctx, map[string]any{"n": int64(1)})
	require.NoError(t, err)

	watchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	snapshots := doc.Snapshots(watchCtx)
	defer snapshots.Stop()

	first, err := snapshots.Next()
	require.NoError(t, err)
	require.True(t, first.Exists())
	assert.Equal(t, int64(1), first.Data()["n"])

	_, err = doc.Delete(ctx)
	require.NoError(t, err)
	second, err := snapshots.Next()
	require.NoError(t, err)
	assert.False(t, second.Exists(), "the deleted document is reported as gone")
}

// TestFirestore_QuerySnapshots_HighLevelClient watches a query through the
// high-level client and reads the change list it derives from the stream.
func TestFirestore_QuerySnapshots_HighLevelClient(t *testing.T) {
	c := newFSGRPCClient(t, "fs-listen-hl-query")
	collection := c.Collection("queue")

	watchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	snapshots := collection.Where("state", "==", "open").Snapshots(watchCtx)
	defer snapshots.Stop()

	first, err := snapshots.Next()
	require.NoError(t, err)
	assert.Equal(t, 0, first.Size, "the collection starts empty")

	_, err = collection.Doc("q1").Create(ctx, map[string]any{"state": "open"})
	require.NoError(t, err)
	second, err := snapshots.Next()
	require.NoError(t, err)
	require.Equal(t, 1, second.Size)
	require.Len(t, second.Changes, 1)
	assert.Equal(t, firestore.DocumentAdded, second.Changes[0].Kind)
	assert.Equal(t, "q1", second.Changes[0].Doc.Ref.ID)
}

// fsAwaitDocumentChange reads the stream until the named document changes.
func fsAwaitDocumentChange(t *testing.T, stream firestorepb.Firestore_ListenClient, name string) *firestorepb.Document {
	t.Helper()
	for {
		resp, err := stream.Recv()
		require.NoError(t, err)
		if dc := resp.GetDocumentChange(); dc != nil && dc.GetDocument().GetName() == name {
			return dc.GetDocument()
		}
	}
}

func fsAwaitDocumentDelete(t *testing.T, stream firestorepb.Firestore_ListenClient) *firestorepb.DocumentDelete {
	t.Helper()
	for {
		resp, err := stream.Recv()
		require.NoError(t, err)
		if dd := resp.GetDocumentDelete(); dd != nil {
			return dd
		}
	}
}

func fsAwaitDocumentRemove(t *testing.T, stream firestorepb.Firestore_ListenClient) *firestorepb.DocumentRemove {
	t.Helper()
	for {
		resp, err := stream.Recv()
		require.NoError(t, err)
		if dr := resp.GetDocumentRemove(); dr != nil {
			return dr
		}
	}
}

func fsAwaitTargetChange(t *testing.T, stream firestorepb.Firestore_ListenClient, kind firestorepb.TargetChange_TargetChangeType) *firestorepb.TargetChange {
	t.Helper()
	for {
		resp, err := stream.Recv()
		require.NoError(t, err)
		if tc := resp.GetTargetChange(); tc != nil && tc.GetTargetChangeType() == kind {
			return tc
		}
	}
}

// requireGRPCStatus asserts a call failed with the given gRPC code.
func requireGRPCStatus(t *testing.T, err error, want codes.Code) {
	t.Helper()
	require.Error(t, err)
	if errors.Is(err, io.EOF) {
		t.Fatalf("expected %s, got EOF", want)
	}
	st, ok := status.FromError(err)
	require.True(t, ok, "expected a gRPC status, got %v", err)
	assert.Equal(t, want, st.Code(), "status message: %s", st.Message())
}
