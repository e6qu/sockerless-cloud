package rds_cluster_restore_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const (
	username = "dbadmin"
	password = "MasterPassword-123!"
	database = "application"
)

// terraform-provider-aws restores one Aurora PostgreSQL cluster to a point in
// time through restore_to_point_in_time and migrates an RDS for PostgreSQL DB
// snapshot into another through snapshot_identifier; each restored cluster
// serves the data its source held.
func TestRDSClusterRestoreTerraform(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	env := tfsim.Start(t, ".")
	client := rds.New(rds.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})

	sourceID := "tf-aurora-pitr-source"
	_, err := client.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(sourceID),
		Engine:              aws.String("aurora-postgresql"),
		MasterUsername:      aws.String(username),
		MasterUserPassword:  aws.String(password),
		DatabaseName:        aws.String(database),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteDBCluster(context.Background(), &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(sourceID), SkipFinalSnapshot: aws.Bool(true),
		})
	})
	addWriter(t, ctx, client, sourceID)
	source := connect(t, ctx, clusterEndpoint(t, ctx, client, sourceID))
	exec(t, ctx, source, `CREATE TABLE ledger (entry text NOT NULL)`)
	exec(t, ctx, source, `INSERT INTO ledger VALUES ('before-restore-time')`)
	// RDS takes the restore time to the millisecond: restore to the engine's
	// next millisecond, and commit the later row once its clock has passed it.
	var restoreTo time.Time
	require.NoError(t, source.QueryRow(ctx,
		`SELECT date_trunc('milliseconds', clock_timestamp()) + interval '1 millisecond'`).Scan(&restoreTo))
	exec(t, ctx, source, fmt.Sprintf(`SELECT pg_sleep_until('%s')`, restoreTo.UTC().Format(time.RFC3339Nano)))
	exec(t, ctx, source, `INSERT INTO ledger VALUES ('after-restore-time')`)

	instanceID, snapshotID := "tf-aurora-migrate-source", "tf-aurora-migrate-snap"
	_, err = client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String(username),
		MasterUserPassword:   aws.String(password),
		DBName:               aws.String(database),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
		})
	})
	instance, err := rds.NewDBInstanceAvailableWaiter(client, waiterDelays).WaitForOutput(ctx,
		&rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute)
	require.NoError(t, err)
	endpoint := instance.DBInstances[0].Endpoint
	instanceConn := connect(t, ctx, fmt.Sprintf("%s:%d", aws.ToString(endpoint.Address), aws.ToInt32(endpoint.Port)))
	exec(t, ctx, instanceConn, `CREATE TABLE ledger (entry text NOT NULL)`)
	exec(t, ctx, instanceConn, `INSERT INTO ledger VALUES ('in-the-instance')`)
	snapshot, err := client.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBSnapshotIdentifier: aws.String(snapshotID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteDBSnapshot(context.Background(), &rds.DeleteDBSnapshotInput{DBSnapshotIdentifier: aws.String(snapshotID)})
	})
	require.NoError(t, rds.NewDBSnapshotAvailableWaiter(client, func(o *rds.DBSnapshotAvailableWaiterOptions) {
		o.MinDelay = 250 * time.Millisecond
		o.MaxDelay = 2 * time.Second
	}).Wait(ctx, &rds.DescribeDBSnapshotsInput{DBSnapshotIdentifier: aws.String(snapshotID)}, 3*time.Minute))

	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve",
		"-var", "source_cluster_identifier="+sourceID,
		"-var", "restore_to_time="+restoreTo.UTC().Format("2006-01-02T15:04:05.000Z"),
		"-var", "db_snapshot_arn="+aws.ToString(snapshot.DBSnapshot.DBSnapshotArn))

	outputs := readOutputs(t, env)
	require.Contains(t, outputs.must(t, "point_in_time_arn"), ":cluster:tf-aurora-pitr-restored")
	require.Equal(t, username, outputs.must(t, "point_in_time_master_username"))
	require.Contains(t, outputs.must(t, "migrated_arn"), ":cluster:tf-aurora-migrated")
	require.Equal(t, "aurora-postgresql", outputs.must(t, "migrated_engine"))
	require.Equal(t, username, outputs.must(t, "migrated_master_username"))

	for clusterID, want := range map[string][]string{
		"tf-aurora-pitr-restored": {"before-restore-time"},
		"tf-aurora-migrated":      {"in-the-instance"},
	} {
		addWriter(t, ctx, client, clusterID)
		require.Equal(t, want, ledger(t, ctx, connect(t, ctx, clusterEndpoint(t, ctx, client, clusterID))),
			"cluster %s serves the data its restore source held", clusterID)
		_, err := client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(clusterID + "-1"), SkipFinalSnapshot: aws.Bool(true),
		})
		require.NoError(t, err)
	}

	env.Terraform(t, "destroy", "-auto-approve",
		"-var", "source_cluster_identifier="+sourceID,
		"-var", "restore_to_time="+restoreTo.UTC().Format("2006-01-02T15:04:05.000Z"),
		"-var", "db_snapshot_arn="+aws.ToString(snapshot.DBSnapshot.DBSnapshotArn))
	_, err = client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(sourceID + "-1"), SkipFinalSnapshot: aws.Bool(true),
	})
	require.NoError(t, err)
}

func waiterDelays(o *rds.DBInstanceAvailableWaiterOptions) {
	o.MinDelay = 250 * time.Millisecond
	o.MaxDelay = 2 * time.Second
}

func addWriter(t *testing.T, ctx context.Context, client *rds.Client, clusterID string) {
	t.Helper()
	instanceID := clusterID + "-1"
	_, err := client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBClusterIdentifier:  aws.String(clusterID),
		Engine:               aws.String("aurora-postgresql"),
		DBInstanceClass:      aws.String("db.r6g.large"),
	})
	require.NoError(t, err)
	require.NoError(t, rds.NewDBInstanceAvailableWaiter(client, waiterDelays).Wait(ctx,
		&rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute))
}

func clusterEndpoint(t *testing.T, ctx context.Context, client *rds.Client, clusterID string) string {
	t.Helper()
	described, err := client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
	require.NoError(t, err)
	return fmt.Sprintf("%s:%d", aws.ToString(described.DBClusters[0].Endpoint), aws.ToInt32(described.DBClusters[0].Port))
}

func connect(t *testing.T, ctx context.Context, endpoint string) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require", username, endpoint, database))
	require.NoError(t, err)
	config.Password = password
	config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func exec(t *testing.T, ctx context.Context, conn *pgx.Conn, statement string) {
	t.Helper()
	_, err := conn.Exec(ctx, statement)
	require.NoError(t, err, statement)
}

func ledger(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT entry FROM ledger ORDER BY entry`)
	require.NoError(t, err)
	defer rows.Close()
	var entries []string
	for rows.Next() {
		var entry string
		require.NoError(t, rows.Scan(&entry))
		entries = append(entries, entry)
	}
	require.NoError(t, rows.Err())
	return entries
}

type tfOutputs map[string]struct {
	Value any `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, ok := v.Value.(string)
	require.True(t, ok, "output %q is not a string (got %T)", key, v.Value)
	require.NotEmpty(t, s, "output %q is empty", key)
	return s
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	return outputs
}
