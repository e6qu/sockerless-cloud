package gcp_sdk_test

import (
	"context"
	"encoding/base64"
	"io"
	"testing"
	"time"

	"cloud.google.com/go/bigtable"
	btpb "cloud.google.com/go/bigtable/apiv2/bigtablepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bigtableadmin "google.golang.org/api/bigtableadmin/v2"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The connection, session and change-stream entry points of the Cloud Bigtable
// data API are exercised through the generated gRPC client — the high-level
// cloud.google.com/go/bigtable client does not expose them — while the SQL
// surface goes through that client's PreparedStatement API, which is how a real
// caller reaches ExecuteQuery.

// btFixture provisions an instance, a table and a column family through the
// admin clients and returns the generated data client alongside the resource
// names, so every test below reads and writes state the admin surface really
// created.
type btFixture struct {
	data     btpb.BigtableClient
	client   *bigtable.Client
	instance string
	table    string
	family   string
}

func newBTFixture(t *testing.T, project, instanceID, tableID, family string) btFixture {
	t.Helper()
	t.Setenv("BIGTABLE_EMULATOR_HOST", grpcAddr)

	instanceAdmin, err := bigtable.NewInstanceAdminClient(ctx, project)
	require.NoError(t, err)
	t.Cleanup(func() { _ = instanceAdmin.Close() })
	require.NoError(t, instanceAdmin.CreateInstance(ctx, &bigtable.InstanceConf{
		InstanceId:  instanceID,
		DisplayName: instanceID,
		ClusterId:   "c1",
		Zone:        "us-east1-b",
		NumNodes:    1,
	}))

	tableAdmin, err := bigtable.NewAdminClient(ctx, project, instanceID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tableAdmin.Close() })
	require.NoError(t, tableAdmin.CreateTable(ctx, tableID))
	require.NoError(t, tableAdmin.CreateColumnFamily(ctx, tableID, family))

	client, err := bigtable.NewClient(ctx, project, instanceID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	instance := "projects/" + project + "/instances/" + instanceID
	return btFixture{
		data:     btpb.NewBigtableClient(conn),
		client:   client,
		instance: instance,
		table:    instance + "/tables/" + tableID,
		family:   family,
	}
}

func btSetCellMutation(family, qualifier, value string) *btpb.Mutation {
	return &btpb.Mutation{Mutation: &btpb.Mutation_SetCell_{SetCell: &btpb.Mutation_SetCell{
		FamilyName:      family,
		ColumnQualifier: []byte(qualifier),
		TimestampMicros: time.Now().UnixMicro() / 1000 * 1000,
		Value:           []byte(value),
	}}}
}

// TestBigtable_PingAndWarm warms a channel against a real instance and refuses
// one that does not exist.
func TestBigtable_PingAndWarm(t *testing.T) {
	f := newBTFixture(t, "bt-ping-proj", "bt-ping-inst", "events", "cf")

	_, err := f.data.PingAndWarm(ctx, &btpb.PingAndWarmRequest{Name: f.instance})
	require.NoError(t, err)

	_, err = f.data.PingAndWarm(ctx, &btpb.PingAndWarmRequest{Name: f.instance, AppProfileId: "default"})
	require.NoError(t, err)

	_, err = f.data.PingAndWarm(ctx, &btpb.PingAndWarmRequest{Name: "projects/bt-ping-proj/instances/absent"})
	requireGRPCStatus(t, err, codes.NotFound)

	_, err = f.data.PingAndWarm(ctx, &btpb.PingAndWarmRequest{Name: f.instance, AppProfileId: "no-such-profile"})
	requireGRPCStatus(t, err, codes.NotFound)
}

// TestBigtable_GetClientConfiguration reads the configuration the instance
// implies: the server serves the session API and its configuration never
// changes, so the client is told to route sessions and stop polling.
func TestBigtable_GetClientConfiguration(t *testing.T) {
	f := newBTFixture(t, "bt-config-proj", "bt-config-inst", "events", "cf")

	config, err := f.data.GetClientConfiguration(ctx, &btpb.GetClientConfigurationRequest{InstanceName: f.instance})
	require.NoError(t, err)
	require.NotNil(t, config.GetSessionConfiguration())
	assert.Equal(t, float32(1), config.GetSessionConfiguration().GetSessionLoad())
	assert.True(t, config.GetStopPolling())

	_, err = f.data.GetClientConfiguration(ctx, &btpb.GetClientConfigurationRequest{
		InstanceName: "projects/bt-config-proj/instances/absent",
	})
	requireGRPCStatus(t, err, codes.NotFound)
}

// TestBigtable_OpenTableSession runs the session protocol end to end: the open
// handshake, a mutation delivered as a virtual RPC, and a read that sees it.
// The high-level client reads the same row afterwards, which is what proves the
// session writes the store the rest of the data plane serves.
func TestBigtable_OpenTableSession(t *testing.T) {
	f := newBTFixture(t, "bt-session-proj", "bt-session-inst", "events", "cf")

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := f.data.OpenTable(streamCtx)
	require.NoError(t, err)
	btOpenSession(t, stream, &btpb.OpenTableRequest{
		TableName:  f.table,
		Permission: btpb.OpenTableRequest_PERMISSION_READ_WRITE,
	})

	// A mutation over the session.
	mutateResponse := &btpb.TableResponse{}
	btVirtualRPC(t, stream, 1, &btpb.TableRequest{Payload: &btpb.TableRequest_MutateRow{
		MutateRow: &btpb.SessionMutateRowRequest{
			Key:       []byte("user#1"),
			Mutations: []*btpb.Mutation{btSetCellMutation(f.family, "name", "alice")},
		},
	}}, mutateResponse)
	require.NotNil(t, mutateResponse.GetMutateRow())

	// The row is there for the unary data plane.
	row, err := f.client.Open("events").ReadRow(ctx, "user#1")
	require.NoError(t, err)
	require.Len(t, row[f.family], 1)
	assert.Equal(t, "alice", string(row[f.family][0].Value))

	// And for a read over the same session.
	readResponse := &btpb.TableResponse{}
	btVirtualRPC(t, stream, 2, &btpb.TableRequest{Payload: &btpb.TableRequest_ReadRow{
		ReadRow: &btpb.SessionReadRowRequest{Key: []byte("user#1")},
	}}, readResponse)
	returned := readResponse.GetReadRow().GetRow()
	require.NotNil(t, returned)
	assert.Equal(t, []byte("user#1"), returned.GetKey())
	require.Len(t, returned.GetFamilies(), 1)
	require.Len(t, returned.GetFamilies()[0].GetColumns(), 1)
	assert.Equal(t, []byte("alice"), returned.GetFamilies()[0].GetColumns()[0].GetCells()[0].GetValue())

	// A row that does not exist reads as no row.
	missing := &btpb.TableResponse{}
	btVirtualRPC(t, stream, 3, &btpb.TableRequest{Payload: &btpb.TableRequest_ReadRow{
		ReadRow: &btpb.SessionReadRowRequest{Key: []byte("user#absent")},
	}}, missing)
	assert.Nil(t, missing.GetReadRow().GetRow())

	// Closing the session ends the stream.
	require.NoError(t, stream.Send(&btpb.SessionRequest{Payload: &btpb.SessionRequest_CloseSession{
		CloseSession: &btpb.CloseSessionRequest{Reason: btpb.CloseSessionRequest_CLOSE_SESSION_REASON_USER},
	}}))
	_, err = stream.Recv()
	assert.ErrorIs(t, err, io.EOF)
}

// TestBigtable_OpenTableSessionPermissions holds a read-only session to reads
// and refuses a session on a table that does not exist.
func TestBigtable_OpenTableSessionPermissions(t *testing.T) {
	f := newBTFixture(t, "bt-session-perm-proj", "bt-session-perm-inst", "events", "cf")

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	readOnly, err := f.data.OpenTable(streamCtx)
	require.NoError(t, err)
	btOpenSession(t, readOnly, &btpb.OpenTableRequest{
		TableName:  f.table,
		Permission: btpb.OpenTableRequest_PERMISSION_READ,
	})
	require.NoError(t, readOnly.Send(btVirtualRPCRequest(t, 1, &btpb.TableRequest{
		Payload: &btpb.TableRequest_MutateRow{MutateRow: &btpb.SessionMutateRowRequest{
			Key:       []byte("user#1"),
			Mutations: []*btpb.Mutation{btSetCellMutation(f.family, "name", "alice")},
		}},
	})))
	refused, err := readOnly.Recv()
	require.NoError(t, err)
	require.NotNil(t, refused.GetError(), "a write on a read-only session is refused on the stream")
	assert.Equal(t, int32(codes.PermissionDenied), refused.GetError().GetStatus().GetCode())
	assert.Equal(t, int64(1), refused.GetError().GetRpcId())

	absentCtx, cancelAbsent := context.WithTimeout(ctx, 30*time.Second)
	defer cancelAbsent()
	absent, err := f.data.OpenTable(absentCtx)
	require.NoError(t, err)
	payload, err := proto.Marshal(&btpb.OpenTableRequest{TableName: f.instance + "/tables/absent"})
	require.NoError(t, err)
	require.NoError(t, absent.Send(&btpb.SessionRequest{Payload: &btpb.SessionRequest_OpenSession{
		OpenSession: &btpb.OpenSessionRequest{ProtocolVersion: 1, Payload: payload},
	}}))
	_, err = absent.Recv()
	requireGRPCStatus(t, err, codes.NotFound)
}

// TestBigtable_OpenAuthorizedViewSession opens a session on an authorized view
// and holds it to the subset the view was created with.
func TestBigtable_OpenAuthorizedViewSession(t *testing.T) {
	const project = "bt-view-proj"
	f := newBTFixture(t, project, "bt-view-inst", "events", "cf")

	// The view exposes rows prefixed "user#" and, within the family, every
	// qualifier prefixed "pub-".
	admin, err := bigtableadmin.NewService(ctx,
		option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	_, err = admin.Projects.Instances.Tables.AuthorizedViews.Create(f.table, &bigtableadmin.AuthorizedView{
		SubsetView: &bigtableadmin.GoogleBigtableAdminV2AuthorizedViewSubsetView{
			RowPrefixes: []string{base64.StdEncoding.EncodeToString([]byte("user#"))},
			FamilySubsets: map[string]bigtableadmin.GoogleBigtableAdminV2AuthorizedViewFamilySubsets{
				f.family: {QualifierPrefixes: []string{base64.StdEncoding.EncodeToString([]byte("pub-"))}},
			},
		},
	}).AuthorizedViewId("public").Do()
	require.NoError(t, err)

	// Two cells on a row the view covers: one inside the qualifier subset, one
	// outside it.
	table := f.client.Open("events")
	mutation := bigtable.NewMutation()
	mutation.Set(f.family, "pub-name", bigtable.Now(), []byte("alice"))
	mutation.Set(f.family, "secret", bigtable.Now(), []byte("hidden"))
	require.NoError(t, table.Apply(ctx, "user#1", mutation))
	other := bigtable.NewMutation()
	other.Set(f.family, "pub-name", bigtable.Now(), []byte("bob"))
	require.NoError(t, table.Apply(ctx, "admin#1", other))

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := f.data.OpenAuthorizedView(streamCtx)
	require.NoError(t, err)
	btOpenSession(t, stream, &btpb.OpenAuthorizedViewRequest{
		AuthorizedViewName: f.table + "/authorizedViews/public",
		Permission:         btpb.OpenAuthorizedViewRequest_PERMISSION_READ_WRITE,
	})

	// The view reports only the qualifiers it exposes.
	read := &btpb.AuthorizedViewResponse{}
	btVirtualRPC(t, stream, 1, &btpb.AuthorizedViewRequest{Payload: &btpb.AuthorizedViewRequest_ReadRow{
		ReadRow: &btpb.SessionReadRowRequest{Key: []byte("user#1")},
	}}, read)
	row := read.GetReadRow().GetRow()
	require.NotNil(t, row)
	require.Len(t, row.GetFamilies(), 1)
	require.Len(t, row.GetFamilies()[0].GetColumns(), 1)
	assert.Equal(t, []byte("pub-name"), row.GetFamilies()[0].GetColumns()[0].GetQualifier())

	// A row outside the view's prefixes is refused, not silently empty.
	require.NoError(t, stream.Send(btVirtualRPCRequest(t, 2, &btpb.AuthorizedViewRequest{
		Payload: &btpb.AuthorizedViewRequest_ReadRow{ReadRow: &btpb.SessionReadRowRequest{Key: []byte("admin#1")}},
	})))
	outsideRow, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, outsideRow.GetError())
	assert.Equal(t, int32(codes.PermissionDenied), outsideRow.GetError().GetStatus().GetCode())

	// So is a write to a column the view does not expose.
	require.NoError(t, stream.Send(btVirtualRPCRequest(t, 3, &btpb.AuthorizedViewRequest{
		Payload: &btpb.AuthorizedViewRequest_MutateRow{MutateRow: &btpb.SessionMutateRowRequest{
			Key:       []byte("user#1"),
			Mutations: []*btpb.Mutation{btSetCellMutation(f.family, "secret", "leak")},
		}},
	})))
	outsideColumn, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, outsideColumn.GetError())
	assert.Equal(t, int32(codes.PermissionDenied), outsideColumn.GetError().GetStatus().GetCode())

	// A write inside the view lands in the table.
	written := &btpb.AuthorizedViewResponse{}
	btVirtualRPC(t, stream, 4, &btpb.AuthorizedViewRequest{Payload: &btpb.AuthorizedViewRequest_MutateRow{
		MutateRow: &btpb.SessionMutateRowRequest{
			Key:       []byte("user#2"),
			Mutations: []*btpb.Mutation{btSetCellMutation(f.family, "pub-name", "carol")},
		},
	}}, written)
	require.NotNil(t, written.GetMutateRow())
	stored, err := table.ReadRow(ctx, "user#2")
	require.NoError(t, err)
	require.Len(t, stored[f.family], 1)
	assert.Equal(t, "carol", string(stored[f.family][0].Value))
}

// TestBigtable_ChangeStream reads the change log the data plane records: the
// initial partition covers the whole table, a mutation applied before the
// stream opened is delivered from its start time, a mutation applied while it
// is open follows, and a heartbeat carries a resumable position.
func TestBigtable_ChangeStream(t *testing.T) {
	f := newBTFixture(t, "bt-changes-proj", "bt-changes-inst", "events", "cf")
	table := f.client.Open("events")

	partitions, err := f.data.GenerateInitialChangeStreamPartitions(ctx,
		&btpb.GenerateInitialChangeStreamPartitionsRequest{TableName: f.table})
	require.NoError(t, err)
	first, err := partitions.Recv()
	require.NoError(t, err)
	partition := first.GetPartition()
	require.NotNil(t, partition)
	assert.Empty(t, partition.GetRowRange().GetStartKeyClosed(), "the partition starts at the first possible key")
	assert.Empty(t, partition.GetRowRange().GetEndKeyOpen(), "and runs to the last")
	_, err = partitions.Recv()
	assert.ErrorIs(t, err, io.EOF, "the simulator holds the table whole: one partition")

	start := time.Now().UTC()
	mutation := bigtable.NewMutation()
	mutation.Set(f.family, "name", bigtable.Now(), []byte("alice"))
	require.NoError(t, table.Apply(ctx, "user#1", mutation))

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := f.data.ReadChangeStream(streamCtx, &btpb.ReadChangeStreamRequest{
		TableName:         f.table,
		Partition:         partition,
		StartFrom:         &btpb.ReadChangeStreamRequest_StartTime{StartTime: timestamppb.New(start)},
		HeartbeatDuration: durationpb.New(200 * time.Millisecond),
	})
	require.NoError(t, err)

	change := btAwaitDataChange(t, stream)
	assert.Equal(t, btpb.ReadChangeStreamResponse_DataChange_USER, change.GetType())
	assert.Equal(t, "c1", change.GetSourceClusterId(), "the change names the cluster that served it")
	assert.Equal(t, []byte("user#1"), change.GetRowKey())
	assert.True(t, change.GetDone())
	assert.NotEmpty(t, change.GetToken())
	require.NotNil(t, change.GetCommitTimestamp())
	require.Len(t, change.GetChunks(), 1)
	setCell := change.GetChunks()[0].GetMutation().GetSetCell()
	require.NotNil(t, setCell)
	assert.Equal(t, f.family, setCell.GetFamilyName())
	assert.Equal(t, []byte("name"), setCell.GetColumnQualifier())
	assert.Equal(t, []byte("alice"), setCell.GetValue())
	resumeToken := change.GetToken()

	// A change made while the stream is open follows it.
	deletion := bigtable.NewMutation()
	deletion.DeleteRow()
	require.NoError(t, table.Apply(ctx, "user#1", deletion))
	deleted := btAwaitDataChange(t, stream)
	assert.Equal(t, []byte("user#1"), deleted.GetRowKey())
	require.Len(t, deleted.GetChunks(), 1)
	assert.NotNil(t, deleted.GetChunks()[0].GetMutation().GetDeleteFromRow())

	// With nothing happening, the stream heartbeats a resumable position.
	heartbeat := btAwaitHeartbeat(t, stream)
	require.NotNil(t, heartbeat.GetContinuationToken())
	assert.NotEmpty(t, heartbeat.GetContinuationToken().GetToken())
	cancel()

	// Resuming from the first change's token delivers only what followed it.
	resumeCtx, cancelResume := context.WithTimeout(ctx, 30*time.Second)
	defer cancelResume()
	resumed, err := f.data.ReadChangeStream(resumeCtx, &btpb.ReadChangeStreamRequest{
		TableName: f.table,
		Partition: partition,
		StartFrom: &btpb.ReadChangeStreamRequest_ContinuationTokens{
			ContinuationTokens: &btpb.StreamContinuationTokens{Tokens: []*btpb.StreamContinuationToken{
				{Partition: partition, Token: resumeToken},
			}},
		},
		HeartbeatDuration: durationpb.New(200 * time.Millisecond),
	})
	require.NoError(t, err)
	afterResume := btAwaitDataChange(t, resumed)
	require.Len(t, afterResume.GetChunks(), 1)
	assert.NotNil(t, afterResume.GetChunks()[0].GetMutation().GetDeleteFromRow(),
		"the resumed stream starts after the token, with the delete")
}

// TestBigtable_ChangeStreamEndTime closes a stream that has run past its end
// time, handing back the position to resume from.
func TestBigtable_ChangeStreamEndTime(t *testing.T) {
	f := newBTFixture(t, "bt-changes-end-proj", "bt-changes-end-inst", "events", "cf")

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := f.data.ReadChangeStream(streamCtx, &btpb.ReadChangeStreamRequest{
		TableName: f.table,
		EndTime:   timestamppb.New(time.Now().Add(-time.Second)),
	})
	require.NoError(t, err)
	resp, err := stream.Recv()
	require.NoError(t, err)
	closed := resp.GetCloseStream()
	require.NotNil(t, closed)
	assert.Equal(t, int32(codes.OK), closed.GetStatus().GetCode())
	require.Len(t, closed.GetContinuationTokens(), 1)
	_, err = stream.Recv()
	assert.ErrorIs(t, err, io.EOF)
}

// TestBigtable_ExecuteQuery runs the SQL surface through the high-level
// client's PreparedStatement API: the schema comes from the table's real column
// families and the rows come from the table's real cells.
func TestBigtable_ExecuteQuery(t *testing.T) {
	f := newBTFixture(t, "bt-sql-proj", "bt-sql-inst", "events", "cf")
	table := f.client.Open("events")

	alice := bigtable.NewMutation()
	alice.Set(f.family, "name", bigtable.Now(), []byte("alice"))
	alice.Set(f.family, "city", bigtable.Now(), []byte("lisbon"))
	require.NoError(t, table.Apply(ctx, "user#1", alice))
	bob := bigtable.NewMutation()
	bob.Set(f.family, "name", bigtable.Now(), []byte("bob"))
	require.NoError(t, table.Apply(ctx, "user#2", bob))

	statement, err := f.client.PrepareStatement(ctx, "SELECT * FROM events", nil)
	require.NoError(t, err)
	bound, err := statement.Bind(nil)
	require.NoError(t, err)

	rows := map[string]map[string][]byte{}
	require.NoError(t, bound.Execute(ctx, func(row bigtable.ResultRow) bool {
		var key []byte
		require.NoError(t, row.GetByName("_key", &key))
		cells := map[string][]byte{}
		require.NoError(t, row.GetByName(f.family, &cells))
		rows[string(key)] = cells
		return true
	}))

	require.Len(t, rows, 2)
	// A MAP<BYTES, BYTES> column reaches the caller with base64-encoded keys.
	nameKey := base64.StdEncoding.EncodeToString([]byte("name"))
	cityKey := base64.StdEncoding.EncodeToString([]byte("city"))
	assert.Equal(t, []byte("alice"), rows["user#1"][nameKey])
	assert.Equal(t, []byte("lisbon"), rows["user#1"][cityKey])
	assert.Equal(t, []byte("bob"), rows["user#2"][nameKey])
	assert.NotContains(t, rows["user#2"], cityKey, "bob has no city cell")

	// A query the simulator cannot plan is refused, never answered with an
	// empty result set.
	_, err = f.client.PrepareStatement(ctx, "SELECT name FROM events WHERE _key = @k", nil)
	requireGRPCStatus(t, err, codes.Unimplemented)

	// So is a query naming a table that does not exist.
	_, err = f.client.PrepareStatement(ctx, "SELECT * FROM absent", nil)
	requireGRPCStatus(t, err, codes.NotFound)
}

// btOpenSession performs the session open handshake with the given typed
// request as its payload.
func btOpenSession(t *testing.T, stream btSessionClient, open proto.Message) {
	t.Helper()
	payload, err := proto.Marshal(open)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&btpb.SessionRequest{Payload: &btpb.SessionRequest_OpenSession{
		OpenSession: &btpb.OpenSessionRequest{ProtocolVersion: 1, Payload: payload},
	}}))
	resp, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, resp.GetOpenSession(), "the handshake is answered with an open_session response")
}

// btVirtualRPCRequest wraps a typed session request as a virtual RPC.
func btVirtualRPCRequest(t *testing.T, rpcID int64, request proto.Message) *btpb.SessionRequest {
	t.Helper()
	payload, err := proto.Marshal(request)
	require.NoError(t, err)
	return &btpb.SessionRequest{Payload: &btpb.SessionRequest_VirtualRpc{VirtualRpc: &btpb.VirtualRpcRequest{
		RpcId:   rpcID,
		Payload: payload,
	}}}
}

// btVirtualRPC sends one virtual RPC and decodes its response payload.
func btVirtualRPC(t *testing.T, stream btSessionClient, rpcID int64, request, response proto.Message) {
	t.Helper()
	require.NoError(t, stream.Send(btVirtualRPCRequest(t, rpcID, request)))
	resp, err := stream.Recv()
	require.NoError(t, err)
	if e := resp.GetError(); e != nil {
		t.Fatalf("virtual RPC %d failed: %s", rpcID, e.GetStatus())
	}
	require.NotNil(t, resp.GetVirtualRpc())
	assert.Equal(t, rpcID, resp.GetVirtualRpc().GetRpcId())
	require.NotNil(t, resp.GetVirtualRpc().GetClusterInfo(), "a session response names the cluster that served it")
	require.NoError(t, proto.Unmarshal(resp.GetVirtualRpc().GetPayload(), response))
}

// btSessionClient is the shape the three generated session streams share.
type btSessionClient interface {
	Send(*btpb.SessionRequest) error
	Recv() (*btpb.SessionResponse, error)
}

// btChangeStream is the shape of the generated ReadChangeStream client.
type btChangeStream interface {
	Recv() (*btpb.ReadChangeStreamResponse, error)
}

func btAwaitDataChange(t *testing.T, stream btChangeStream) *btpb.ReadChangeStreamResponse_DataChange {
	t.Helper()
	for {
		resp, err := stream.Recv()
		require.NoError(t, err)
		if change := resp.GetDataChange(); change != nil {
			return change
		}
	}
}

func btAwaitHeartbeat(t *testing.T, stream btChangeStream) *btpb.ReadChangeStreamResponse_Heartbeat {
	t.Helper()
	for {
		resp, err := stream.Recv()
		require.NoError(t, err)
		if heartbeat := resp.GetHeartbeat(); heartbeat != nil {
			return heartbeat
		}
	}
}
