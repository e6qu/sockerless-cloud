package gcp_sdk_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	sppb "cloud.google.com/go/spanner/apiv1/spannerpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	spanneradmin "google.golang.org/api/spanner/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestSpanner_GRPC exercises the Spanner gRPC data plane against the simulator
// using the high-level cloud.google.com/go/spanner client — the gRPC-only
// surface that previously could not target the simulator at all. Provisioning
// (instance / database / DDL) goes through the REST admin SDK; every read and
// write below rides the gRPC data plane through SPANNER_EMULATOR_HOST, the same
// coordinate the real Spanner emulator honors.
func TestSpanner_GRPC(t *testing.T) {
	ctx := context.Background()

	const (
		project  = "test-project"
		instance = "sp-grpc"
		dbID     = "db1"
		database = "projects/" + project + "/instances/" + instance + "/databases/" + dbID
	)

	spannerProvision(t, ctx, project, instance, dbID, []string{
		"CREATE TABLE Singers (SingerId INT64 NOT NULL, FirstName STRING(100)) PRIMARY KEY (SingerId)",
		"CREATE TABLE Albums (SingerId INT64 NOT NULL, AlbumId INT64 NOT NULL, Title STRING(100)) PRIMARY KEY (SingerId, AlbumId)",
	})

	// Point the high-level client at the simulator's gRPC port — the same env
	// var the real Spanner emulator uses.
	t.Setenv("SPANNER_EMULATOR_HOST", grpcAddr)

	client, err := spanner.NewClient(ctx, database)
	require.NoError(t, err)
	defer client.Close()

	type singer struct {
		SingerId  int64
		FirstName string
	}

	// INSERT via Apply → real INSERT mutation committed through a single-use
	// read-write transaction.
	_, err = client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("Singers", []string{"SingerId", "FirstName"}, []interface{}{int64(1), "Alice"}),
	})
	require.NoError(t, err)

	// Read the row back via a single-use read-only StreamingRead and assert the
	// values the SQLite engine persisted — not a synthetic result set.
	got := spannerReadAll[singer](t, client.Single().Read(ctx, "Singers", spanner.AllKeys(), []string{"SingerId", "FirstName"}))
	require.Len(t, got, 1)
	assert.Equal(t, int64(1), got[0].SingerId)
	assert.Equal(t, "Alice", got[0].FirstName)

	// Query via ExecuteStreamingSql — the row typed back through Go structs.
	got2 := spannerReadAll[singer](t, client.Single().Query(ctx, spanner.Statement{
		SQL: "SELECT SingerId, FirstName FROM Singers",
	}))
	require.Len(t, got2, 1)
	assert.Equal(t, "Alice", got2[0].FirstName)

	// A second INSERT + SELECT confirms multiple rows survive in the backing
	// store and come back in key order.
	_, err = client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("Singers", []string{"SingerId", "FirstName"}, []interface{}{int64(2), "Bob"}),
	})
	require.NoError(t, err)
	got = spannerReadAll[singer](t, client.Single().Read(ctx, "Singers", spanner.AllKeys(), []string{"SingerId", "FirstName"}))
	require.Len(t, got, 2)
	assert.Equal(t, int64(1), got[0].SingerId)
	assert.Equal(t, int64(2), got[1].SingerId)

	// UPDATE mutation + SELECT confirms the change persisted to the real
	// backing engine.
	_, err = client.Apply(ctx, []*spanner.Mutation{
		spanner.Update("Singers", []string{"SingerId", "FirstName"}, []interface{}{int64(1), "Alicia"}),
	})
	require.NoError(t, err)
	got = spannerReadAll[singer](t, client.Single().Read(ctx, "Singers", spanner.KeySets(spanner.Key{int64(1)}), []string{"SingerId", "FirstName"}))
	require.Len(t, got, 1)
	assert.Equal(t, "Alicia", got[0].FirstName)

	// DELETE mutation + SELECT confirms the row is gone from the real store.
	_, err = client.Apply(ctx, []*spanner.Mutation{
		spanner.Delete("Singers", spanner.Key{int64(2)}),
	})
	require.NoError(t, err)
	got = spannerReadAll[singer](t, client.Single().Read(ctx, "Singers", spanner.AllKeys(), []string{"SingerId", "FirstName"}))
	require.Len(t, got, 1)
	assert.Equal(t, int64(1), got[0].SingerId)

	// ReadWriteTransaction: BeginTransaction → read → buffered write → Commit,
	// all on one session and one begun transaction id.
	_, err = client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		row, rerr := txn.ReadRow(ctx, "Singers", spanner.Key{int64(1)}, []string{"FirstName"})
		require.NoError(t, rerr)
		var name string
		require.NoError(t, row.Column(0, &name))
		assert.Equal(t, "Alicia", name)
		return txn.BufferWrite([]*spanner.Mutation{
			spanner.Update("Singers", []string{"SingerId", "FirstName"}, []interface{}{int64(1), "Alicia2"}),
		})
	})
	require.NoError(t, err)
	got = spannerReadAll[singer](t, client.Single().Read(ctx, "Singers", spanner.KeySets(spanner.Key{int64(1)}), []string{"FirstName"}))
	require.Len(t, got, 1)
	assert.Equal(t, "Alicia2", got[0].FirstName)

	type album struct {
		SingerId int64
		AlbumId  int64
		Title    string
	}
	_, err = client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("Albums", []string{"SingerId", "AlbumId", "Title"}, []interface{}{int64(1), int64(1), "A"}),
		spanner.Insert("Albums", []string{"SingerId", "AlbumId", "Title"}, []interface{}{int64(1), int64(2), "B"}),
		spanner.Insert("Albums", []string{"SingerId", "AlbumId", "Title"}, []interface{}{int64(2), int64(1), "C"}),
		spanner.Insert("Albums", []string{"SingerId", "AlbumId", "Title"}, []interface{}{int64(3), int64(1), "D"}),
	})
	require.NoError(t, err)

	// Composite primary-key ranges use Cloud Spanner's lexicographic ordering,
	// including prefix endpoints and multiple disjoint ranges.
	compositeRange := spanner.KeyRange{
		Start: spanner.Key{int64(1), int64(2)},
		End:   spanner.Key{int64(3), int64(1)},
		Kind:  spanner.ClosedOpen,
	}
	albums := spannerReadAll[album](t, client.Single().Read(ctx, "Albums", compositeRange, []string{"SingerId", "AlbumId", "Title"}))
	require.Len(t, albums, 2)
	assert.Equal(t, []string{"B", "C"}, []string{albums[0].Title, albums[1].Title})

	disjoint := spanner.KeySets(
		spanner.KeyRange{Start: spanner.Key{int64(1), int64(1)}, End: spanner.Key{int64(1), int64(1)}, Kind: spanner.ClosedClosed},
		spanner.KeyRange{Start: spanner.Key{int64(3), int64(1)}, End: spanner.Key{int64(3), int64(1)}, Kind: spanner.ClosedClosed},
	)
	albums = spannerReadAll[album](t, client.Single().Read(ctx, "Albums", disjoint, []string{"SingerId", "AlbumId", "Title"}))
	require.Len(t, albums, 2)
	assert.Equal(t, []string{"A", "D"}, []string{albums[0].Title, albums[1].Title})

	_, err = client.Apply(ctx, []*spanner.Mutation{
		spanner.Delete("Albums", spanner.KeyRange{
			Start: spanner.Key{int64(1)},
			End:   spanner.Key{int64(2)},
			Kind:  spanner.ClosedOpen,
		}),
	})
	require.NoError(t, err)
	albums = spannerReadAll[album](t, client.Single().Read(ctx, "Albums", spanner.AllKeys(), []string{"SingerId", "AlbumId", "Title"}))
	require.Len(t, albums, 2)
	assert.Equal(t, []string{"C", "D"}, []string{albums[0].Title, albums[1].Title})

	// Concurrent reads exercise the session-pool path (BatchCreateSessions) —
	// each goroutine borrows its own session and runs a StreamingRead.
	const n = 8
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			it := client.Single().Query(ctx, spanner.Statement{SQL: "SELECT SingerId FROM Singers"})
			defer it.Stop()
			cnt := 0
			for {
				_, err := it.Next()
				if err == iterator.Done {
					done <- nil
					return
				}
				if err != nil {
					done <- err
					return
				}
				cnt++
				if cnt > 1000 {
					done <- fmt.Errorf("too many rows")
					return
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-done, "concurrent read failed")
	}
}

func TestSpanner_GRPCBatchDMLAndBatchWrite(t *testing.T) {
	ctx := context.Background()
	const (
		project  = "test-project"
		instance = "sp-grpc-batch"
		dbID     = "db1"
		database = "projects/" + project + "/instances/" + instance + "/databases/" + dbID
	)
	spannerProvision(t, ctx, project, instance, dbID, []string{
		"CREATE TABLE Accounts (AccountId STRING(36) NOT NULL, Balance INT64 NOT NULL) PRIMARY KEY (AccountId)",
	})

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	client := sppb.NewSpannerClient(conn)
	session, err := client.CreateSession(ctx, &sppb.CreateSessionRequest{Database: database})
	require.NoError(t, err)

	txn, err := client.BeginTransaction(ctx, &sppb.BeginTransactionRequest{
		Session: session.GetName(),
		Options: &sppb.TransactionOptions{
			Mode: &sppb.TransactionOptions_ReadWrite_{ReadWrite: &sppb.TransactionOptions_ReadWrite{}},
		},
	})
	require.NoError(t, err)
	selector := &sppb.TransactionSelector{Selector: &sppb.TransactionSelector_Id{Id: txn.GetId()}}
	insertRequest := &sppb.ExecuteSqlRequest{
		Session:     session.GetName(),
		Transaction: selector,
		Sql:         "INSERT INTO Accounts (AccountId, Balance) VALUES ('a1', 10)",
		Seqno:       1,
	}
	insert, err := client.ExecuteSql(ctx, insertRequest)
	require.NoError(t, err)
	assert.Equal(t, int64(1), insert.GetStats().GetRowCountExact())
	replayedInsert, err := client.ExecuteSql(ctx, insertRequest)
	require.NoError(t, err)
	assert.Equal(t, insert.GetStats().GetRowCountExact(), replayedInsert.GetStats().GetRowCountExact())

	batchRequest := &sppb.ExecuteBatchDmlRequest{
		Session:     session.GetName(),
		Transaction: selector,
		Seqno:       2,
		Statements: []*sppb.ExecuteBatchDmlRequest_Statement{
			{Sql: "UPDATE Accounts SET Balance = 20 WHERE AccountId = 'a1'"},
			{Sql: "INSERT INTO Accounts (AccountId, Balance) VALUES ('a2', 30)"},
		},
	}
	batch, err := client.ExecuteBatchDml(ctx, batchRequest)
	require.NoError(t, err)
	require.Len(t, batch.GetResultSets(), 2)
	assert.Equal(t, int32(codes.OK), batch.GetStatus().GetCode())
	replayedBatch, err := client.ExecuteBatchDml(ctx, batchRequest)
	require.NoError(t, err)
	require.Len(t, replayedBatch.GetResultSets(), 2)

	_, err = client.ExecuteSql(ctx, &sppb.ExecuteSqlRequest{
		Session:     session.GetName(),
		Transaction: selector,
		Sql:         "UPDATE Accounts SET Balance = 99 WHERE AccountId = 'a1'",
		Seqno:       2,
	})
	require.Error(t, err)
	assert.Equal(t, codes.Aborted, status.Code(err))
	_, err = client.Commit(ctx, &sppb.CommitRequest{
		Session: session.GetName(),
		Transaction: &sppb.CommitRequest_TransactionId{
			TransactionId: txn.GetId(),
		},
	})
	require.NoError(t, err)

	batchWrite, err := client.BatchWrite(ctx, &sppb.BatchWriteRequest{
		Session: session.GetName(),
		MutationGroups: []*sppb.BatchWriteRequest_MutationGroup{
			{Mutations: []*sppb.Mutation{spannerInsertMutation("Accounts", []string{"AccountId", "Balance"}, "a3", "40")}},
			{Mutations: []*sppb.Mutation{spannerInsertMutation("Accounts", []string{"AccountId", "Balance"}, "a1", "50")}},
		},
	})
	require.NoError(t, err)
	var batchWriteResponses []*sppb.BatchWriteResponse
	for {
		response, recvErr := batchWrite.Recv()
		if recvErr == io.EOF {
			break
		}
		require.NoError(t, recvErr)
		batchWriteResponses = append(batchWriteResponses, response)
	}
	require.Len(t, batchWriteResponses, 2)
	assert.Equal(t, int32(codes.OK), batchWriteResponses[0].GetStatus().GetCode())
	assert.NotEqual(t, int32(codes.OK), batchWriteResponses[1].GetStatus().GetCode())

	finalTxn, err := client.BeginTransaction(ctx, &sppb.BeginTransactionRequest{
		Session: session.GetName(),
		Options: &sppb.TransactionOptions{
			Mode: &sppb.TransactionOptions_ReadWrite_{ReadWrite: &sppb.TransactionOptions_ReadWrite{}},
		},
	})
	require.NoError(t, err)
	finalSelector := &sppb.TransactionSelector{Selector: &sppb.TransactionSelector_Id{Id: finalTxn.GetId()}}
	_, err = client.ExecuteSql(ctx, &sppb.ExecuteSqlRequest{
		Session:       session.GetName(),
		Transaction:   finalSelector,
		Sql:           "UPDATE Accounts SET Balance = 21 WHERE AccountId = 'a1'",
		Seqno:         1,
		LastStatement: true,
	})
	require.NoError(t, err)
	_, err = client.Read(ctx, &sppb.ReadRequest{
		Session:     session.GetName(),
		Transaction: finalSelector,
		Table:       "Accounts",
		Columns:     []string{"AccountId"},
		KeySet:      &sppb.KeySet{All: true},
	})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = client.Commit(ctx, &sppb.CommitRequest{
		Session: session.GetName(),
		Transaction: &sppb.CommitRequest_TransactionId{
			TransactionId: finalTxn.GetId(),
		},
	})
	require.NoError(t, err)

	query, err := client.ExecuteSql(ctx, &sppb.ExecuteSqlRequest{
		Session: session.GetName(),
		Sql:     "SELECT AccountId, Balance FROM Accounts ORDER BY AccountId",
	})
	require.NoError(t, err)
	require.Len(t, query.GetRows(), 3)
	assert.Equal(t, "21", query.GetRows()[0].GetValues()[1].GetStringValue())
}

func spannerInsertMutation(table string, columns []string, values ...string) *sppb.Mutation {
	protoValues := make([]*structpb.Value, len(values))
	for i, value := range values {
		protoValues[i] = structpb.NewStringValue(value)
	}
	return &sppb.Mutation{
		Operation: &sppb.Mutation_Insert{
			Insert: &sppb.Mutation_Write{
				Table:   table,
				Columns: columns,
				Values:  []*structpb.ListValue{{Values: protoValues}},
			},
		},
	}
}

// spannerReadAll drains a RowIterator into a slice of T using row.ToStruct,
// asserting the read returned real rows from the backing engine.
func spannerReadAll[T any](t *testing.T, it *spanner.RowIterator) []T {
	t.Helper()
	defer it.Stop()
	var out []T
	for {
		row, err := it.Next()
		if err == iterator.Done {
			return out
		}
		require.NoError(t, err, "unexpected iterator error")
		var v T
		require.NoError(t, row.ToStruct(&v))
		out = append(out, v)
	}
}

// spannerProvision provisions an instance + database + DDL through the REST
// admin SDK so the gRPC data plane has a real schema to execute against.
func spannerProvision(t *testing.T, ctx context.Context, project, instance, dbID string, ddl []string) {
	t.Helper()
	svc, err := spanneradmin.NewService(ctx, option.WithEndpoint(baseURL+"/spanner/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	op, err := svc.Projects.Instances.Create("projects/"+project, &spanneradmin.CreateInstanceRequest{
		InstanceId: instance,
		Instance: &spanneradmin.Instance{
			DisplayName: instance,
			NodeCount:   1,
		},
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	dbOp, err := svc.Projects.Instances.Databases.Create(fmt.Sprintf("projects/%s/instances/%s", project, instance), &spanneradmin.CreateDatabaseRequest{
		CreateStatement: "CREATE DATABASE `" + dbID + "`",
	}).Do()
	require.NoError(t, err)
	require.True(t, dbOp.Done)

	dbName := fmt.Sprintf("projects/%s/instances/%s/databases/%s", project, instance, dbID)
	if len(ddl) > 0 {
		ddlOp, err := svc.Projects.Instances.Databases.UpdateDdl(dbName, &spanneradmin.UpdateDatabaseDdlRequest{Statements: ddl}).Do()
		require.NoError(t, err)
		require.True(t, ddlOp.Done)
	}

	// Poll until the admin slice confirms the database is queryable — fixed
	// sleeps are banned by AGENTS.md, so poll on the real readiness signal.
	require.Eventually(t, func() bool {
		_, err := svc.Projects.Instances.Databases.Get(dbName).Do()
		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "database never became queryable")
}
