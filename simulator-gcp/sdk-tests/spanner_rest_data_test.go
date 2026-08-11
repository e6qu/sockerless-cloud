package gcp_sdk_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/api/spanner/v1"
)

func TestSpanner_RESTDataPlane(t *testing.T) {
	svc, err := spanner.NewService(ctx, option.WithEndpoint(baseURL+"/spanner/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const instance = "rest-data"
	instanceName := "projects/test-project/instances/" + instance
	databaseName := instanceName + "/databases/app"

	op, err := svc.Projects.Instances.Create("projects/test-project", &spanner.CreateInstanceRequest{
		InstanceId: instance,
		Instance:   &spanner.Instance{DisplayName: "REST data plane", NodeCount: 1},
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	op, err = svc.Projects.Instances.Databases.Create(instanceName, &spanner.CreateDatabaseRequest{
		CreateStatement: "CREATE DATABASE `app`",
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	op, err = svc.Projects.Instances.Databases.UpdateDdl(databaseName, &spanner.UpdateDatabaseDdlRequest{
		Statements: []string{"CREATE TABLE Users (UserId STRING(36) NOT NULL, Name STRING(100) NOT NULL) PRIMARY KEY (UserId)"},
	}).Do()
	require.NoError(t, err)
	require.Nil(t, op.Error)

	session, err := svc.Projects.Instances.Databases.Sessions.Create(databaseName, &spanner.CreateSessionRequest{
		Session: &spanner.Session{Labels: map[string]string{"transport": "rest"}},
	}).Do()
	require.NoError(t, err)

	batchSessions, err := svc.Projects.Instances.Databases.Sessions.BatchCreate(databaseName, &spanner.BatchCreateSessionsRequest{
		SessionCount: 2,
		SessionTemplate: &spanner.Session{
			Labels: map[string]string{"pool": "rest"},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, batchSessions.Session, 2)

	txn, err := svc.Projects.Instances.Databases.Sessions.BeginTransaction(session.Name, &spanner.BeginTransactionRequest{
		Options: &spanner.TransactionOptions{ReadWrite: &spanner.ReadWrite{}},
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, txn.Id)

	insert, err := svc.Projects.Instances.Databases.Sessions.ExecuteSql(session.Name, &spanner.ExecuteSqlRequest{
		Sql:         "INSERT INTO Users (UserId, Name) VALUES ('u1', 'Alice')",
		Seqno:       1,
		Transaction: &spanner.TransactionSelector{Id: txn.Id},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, insert.Stats)
	assert.Equal(t, int64(1), insert.Stats.RowCountExact)

	batchDML, err := svc.Projects.Instances.Databases.Sessions.ExecuteBatchDml(session.Name, &spanner.ExecuteBatchDmlRequest{
		Seqno:       2,
		Transaction: &spanner.TransactionSelector{Id: txn.Id},
		Statements: []*spanner.Statement{
			{Sql: "UPDATE Users SET Name = 'Alicia' WHERE UserId = 'u1'"},
			{Sql: "INSERT INTO Users (UserId, Name) VALUES ('u2', 'Bob')"},
		},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, batchDML.Status)
	assert.Zero(t, batchDML.Status.Code)
	require.Len(t, batchDML.ResultSets, 2)
	assert.Equal(t, int64(1), batchDML.ResultSets[0].Stats.RowCountExact)

	inside, err := svc.Projects.Instances.Databases.Sessions.Read(session.Name, &spanner.ReadRequest{
		Table:       "Users",
		Columns:     []string{"UserId", "Name"},
		KeySet:      &spanner.KeySet{All: true},
		Transaction: &spanner.TransactionSelector{Id: txn.Id},
	}).Do()
	require.NoError(t, err)
	require.Len(t, inside.Rows, 2, "the transaction read observed its uncommitted DML")

	commit, err := svc.Projects.Instances.Databases.Sessions.Commit(session.Name, &spanner.CommitRequest{TransactionId: txn.Id}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, commit.CommitTimestamp)

	query, err := svc.Projects.Instances.Databases.Sessions.ExecuteSql(session.Name, &spanner.ExecuteSqlRequest{
		Sql: "SELECT UserId, Name FROM Users ORDER BY UserId",
	}).Do()
	require.NoError(t, err)
	require.Len(t, query.Rows, 2)
	assert.Equal(t, "u1", query.Rows[0][0])
	assert.Equal(t, "Alicia", query.Rows[0][1])

	streamedQuery, err := svc.Projects.Instances.Databases.Sessions.ExecuteStreamingSql(session.Name, &spanner.ExecuteSqlRequest{
		Sql: "SELECT UserId, Name FROM Users ORDER BY UserId",
	}).Do()
	require.NoError(t, err)
	require.Len(t, streamedQuery.Values, 4)
	assert.True(t, streamedQuery.Last)

	streamedRead, err := svc.Projects.Instances.Databases.Sessions.StreamingRead(session.Name, &spanner.ReadRequest{
		Table:   "Users",
		Columns: []string{"UserId", "Name"},
		KeySet:  &spanner.KeySet{All: true},
	}).Do()
	require.NoError(t, err)
	require.Len(t, streamedRead.Values, 4)

	partitionTxn, err := svc.Projects.Instances.Databases.Sessions.BeginTransaction(session.Name, &spanner.BeginTransactionRequest{
		Options: &spanner.TransactionOptions{ReadOnly: &spanner.ReadOnly{Strong: true}},
	}).Do()
	require.NoError(t, err)

	queryPartitions, err := svc.Projects.Instances.Databases.Sessions.PartitionQuery(session.Name, &spanner.PartitionQueryRequest{
		Sql:         "SELECT UserId FROM Users",
		Transaction: &spanner.TransactionSelector{Id: partitionTxn.Id},
	}).Do()
	require.NoError(t, err)
	require.Len(t, queryPartitions.Partitions, 1)
	partitionedQuery, err := svc.Projects.Instances.Databases.Sessions.ExecuteSql(session.Name, &spanner.ExecuteSqlRequest{
		Sql:            "SELECT UserId FROM Users",
		Transaction:    &spanner.TransactionSelector{Id: partitionTxn.Id},
		PartitionToken: queryPartitions.Partitions[0].PartitionToken,
	}).Do()
	require.NoError(t, err)
	require.Len(t, partitionedQuery.Rows, 2)

	readPartitions, err := svc.Projects.Instances.Databases.Sessions.PartitionRead(session.Name, &spanner.PartitionReadRequest{
		Table:       "Users",
		Columns:     []string{"UserId"},
		KeySet:      &spanner.KeySet{All: true},
		Transaction: &spanner.TransactionSelector{Id: partitionTxn.Id},
	}).Do()
	require.NoError(t, err)
	require.Len(t, readPartitions.Partitions, 1)
	partitionedRead, err := svc.Projects.Instances.Databases.Sessions.Read(session.Name, &spanner.ReadRequest{
		Table:          "Users",
		Columns:        []string{"UserId"},
		KeySet:         &spanner.KeySet{All: true},
		Transaction:    &spanner.TransactionSelector{Id: partitionTxn.Id},
		PartitionToken: readPartitions.Partitions[0].PartitionToken,
	}).Do()
	require.NoError(t, err)
	require.Len(t, partitionedRead.Rows, 2)
	_, err = svc.Projects.Instances.Databases.Sessions.Rollback(session.Name, &spanner.RollbackRequest{
		TransactionId: partitionTxn.Id,
	}).Do()
	require.NoError(t, err)

	batchWrite, err := svc.Projects.Instances.Databases.Sessions.BatchWrite(session.Name, &spanner.BatchWriteRequest{
		MutationGroups: []*spanner.MutationGroup{{
			Mutations: []*spanner.Mutation{{
				Insert: &spanner.Write{
					Table:   "Users",
					Columns: []string{"UserId", "Name"},
					Values:  [][]interface{}{{"u3", "Carol"}},
				},
			}},
		}},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, batchWrite.Status)
	assert.Zero(t, batchWrite.Status.Code)
	assert.Equal(t, []int64{0}, batchWrite.Indexes)

	rollbackTxn, err := svc.Projects.Instances.Databases.Sessions.BeginTransaction(session.Name, &spanner.BeginTransactionRequest{
		Options: &spanner.TransactionOptions{ReadWrite: &spanner.ReadWrite{}},
	}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Databases.Sessions.ExecuteSql(session.Name, &spanner.ExecuteSqlRequest{
		Sql:         "DELETE FROM Users WHERE UserId = 'u1'",
		Seqno:       1,
		Transaction: &spanner.TransactionSelector{Id: rollbackTxn.Id},
	}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Databases.Sessions.Rollback(session.Name, &spanner.RollbackRequest{TransactionId: rollbackTxn.Id}).Do()
	require.NoError(t, err)

	afterRollback, err := svc.Projects.Instances.Databases.Sessions.ExecuteSql(session.Name, &spanner.ExecuteSqlRequest{
		Sql: "SELECT UserId FROM Users ORDER BY UserId",
	}).Do()
	require.NoError(t, err)
	require.Len(t, afterRollback.Rows, 3)
	assert.Equal(t, "u1", afterRollback.Rows[0][0], "rollback preserved the previously committed row")

	badDDL, err := svc.Projects.Instances.Databases.UpdateDdl(databaseName, &spanner.UpdateDatabaseDdlRequest{
		Statements:  []string{"ALTER TABLE Users ALTER COLUMN Name STRING(50)"},
		OperationId: "unsupported_ddl",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, badDDL.Error)
	assert.Equal(t, int64(12), badDDL.Error.Code)
	assert.Contains(t, badDDL.Error.Message, "not supported")

	_, err = svc.Projects.Instances.Databases.Sessions.Delete(session.Name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Databases.Sessions.Get(session.Name).Do()
	require.Error(t, err)

	t.Logf("validated REST data plane for %s", fmt.Sprintf("%s through %d session calls", databaseName, 14))
}
