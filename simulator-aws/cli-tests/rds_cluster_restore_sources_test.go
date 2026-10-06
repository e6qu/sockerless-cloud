package aws_cli_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cliRestoreUsername = "dbadmin"
	cliRestorePassword = "MasterPassword-123!"
	cliRestoreDatabase = "application"
)

type cliDBCluster struct {
	Status                 string `json:"Status"`
	Engine                 string `json:"Engine"`
	MasterUsername         string `json:"MasterUsername"`
	DatabaseName           string `json:"DatabaseName"`
	Endpoint               string `json:"Endpoint"`
	Port                   int    `json:"Port"`
	EarliestRestorableTime string `json:"EarliestRestorableTime"`
	LatestRestorableTime   string `json:"LatestRestorableTime"`
}

func cliDescribeDBCluster(t *testing.T, clusterID string) cliDBCluster {
	t.Helper()
	var described struct {
		DBClusters []cliDBCluster `json:"DBClusters"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-clusters", "--db-cluster-identifier", clusterID)), &described)
	require.Len(t, described.DBClusters, 1)
	return described.DBClusters[0]
}

// cliAddAuroraWriter creates the cluster's writer instance and waits for it.
func cliAddAuroraWriter(t *testing.T, clusterID, engine string) {
	t.Helper()
	instanceID := clusterID + "-1"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instanceID,
		"--db-cluster-identifier", clusterID,
		"--engine", engine,
		"--db-instance-class", "db.r6g.large"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance", "--db-instance-identifier", instanceID, "--skip-final-snapshot").Run()
	})
	runCLI(t, awsCLI("rds", "wait", "db-instance-available", "--db-instance-identifier", instanceID))
}

func cliCleanupDBCluster(t *testing.T, clusterID string) {
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster", "--db-cluster-identifier", clusterID, "--skip-final-snapshot").Run()
	})
}

func cliConnectPostgres(t *testing.T, ctx context.Context, host string, port int) *pgx.Conn {
	t.Helper()
	return cliConnectPostgresAs(t, ctx, host, port, cliRestoreUsername, cliRestorePassword)
}

func cliConnectPostgresAs(t *testing.T, ctx context.Context, host string, port int, user, password string) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=require", user, host, port, cliRestoreDatabase))
	require.NoError(t, err)
	config.Password = password
	config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func cliLedger(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
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

// aws rds restore-db-cluster-to-point-in-time --restore-to-time returns an
// Aurora PostgreSQL cluster to the rows committed by that time, inside the
// window describe-db-clusters reports, and refuses a time before it.
func TestRDSCLI_AuroraClusterRestoresToAPointInTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sourceID, restoredID := "cli-pitr-source", "cli-pitr-restored"
	runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", sourceID,
		"--engine", "aurora-postgresql",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--database-name", cliRestoreDatabase))
	cliCleanupDBCluster(t, sourceID)
	cliAddAuroraWriter(t, sourceID, "aurora-postgresql")
	source := cliDescribeDBCluster(t, sourceID)
	conn := cliConnectPostgres(t, ctx, source.Endpoint, source.Port)
	_, err := conn.Exec(ctx, `CREATE TABLE ledger (entry text NOT NULL)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO ledger VALUES ('before-restore-time')`)
	require.NoError(t, err)
	// RDS takes the restore time to the millisecond: restore to the engine's
	// next millisecond, and commit the later row once its clock has passed it.
	var restoreTo time.Time
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT date_trunc('milliseconds', clock_timestamp()) + interval '1 millisecond'`).Scan(&restoreTo))
	_, err = conn.Exec(ctx, fmt.Sprintf(`SELECT pg_sleep_until('%s')`, restoreTo.UTC().Format(time.RFC3339Nano)))
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO ledger VALUES ('after-restore-time')`)
	require.NoError(t, err)

	window := cliDescribeDBCluster(t, sourceID)
	require.NotEmpty(t, window.EarliestRestorableTime, "a cluster whose engine has served reports EarliestRestorableTime")
	require.NotEmpty(t, window.LatestRestorableTime, "a cluster whose engine has served reports LatestRestorableTime")
	earliest, err := time.Parse(time.RFC3339Nano, window.EarliestRestorableTime)
	require.NoError(t, err)

	refused := runCLIExpectError(t, awsCLI("rds", "restore-db-cluster-to-point-in-time",
		"--db-cluster-identifier", "cli-pitr-too-early",
		"--source-db-cluster-identifier", sourceID,
		"--restore-to-time", earliest.Add(-time.Hour).Format(time.RFC3339)))
	assert.Contains(t, refused, "InvalidRestoreFault")

	out := runCLI(t, awsCLI("rds", "restore-db-cluster-to-point-in-time",
		"--db-cluster-identifier", restoredID,
		"--source-db-cluster-identifier", sourceID,
		"--restore-to-time", restoreTo.UTC().Format("2006-01-02T15:04:05.000Z")))
	cliCleanupDBCluster(t, restoredID)
	var restored struct {
		DBCluster cliDBCluster `json:"DBCluster"`
	}
	parseJSON(t, out, &restored)
	assert.Equal(t, "creating", restored.DBCluster.Status)
	runCLI(t, awsCLI("rds", "wait", "db-cluster-available", "--db-cluster-identifier", restoredID))
	cliAddAuroraWriter(t, restoredID, "aurora-postgresql")
	target := cliDescribeDBCluster(t, restoredID)
	assert.Equal(t, []string{"before-restore-time"}, cliLedger(t, ctx, cliConnectPostgres(t, ctx, target.Endpoint, target.Port)),
		"the restored cluster holds the rows committed by the restore time and none after it")
}

// aws rds restore-db-cluster-from-snapshot migrates an RDS for PostgreSQL DB
// snapshot, named by its ARN, into an Aurora PostgreSQL cluster that serves
// the instance's data.
func TestRDSCLI_AuroraClusterRestoresFromADBInstanceSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	instanceID, snapshotID, clusterID := "cli-migrate-source", "cli-migrate-snap", "cli-migrated"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instanceID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance", "--db-instance-identifier", instanceID, "--skip-final-snapshot").Run()
	})
	runCLI(t, awsCLI("rds", "wait", "db-instance-available", "--db-instance-identifier", instanceID))
	var instance struct {
		DBInstances []struct {
			Endpoint struct {
				Address string `json:"Address"`
				Port    int    `json:"Port"`
			} `json:"Endpoint"`
		} `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", instanceID)), &instance)
	require.Len(t, instance.DBInstances, 1)
	conn := cliConnectPostgres(t, ctx, instance.DBInstances[0].Endpoint.Address, instance.DBInstances[0].Endpoint.Port)
	_, err := conn.Exec(ctx, `CREATE TABLE ledger (entry text NOT NULL)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO ledger VALUES ('in-the-instance')`)
	require.NoError(t, err)

	var snapshot struct {
		DBSnapshot struct {
			DBSnapshotArn string `json:"DBSnapshotArn"`
		} `json:"DBSnapshot"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "create-db-snapshot",
		"--db-instance-identifier", instanceID,
		"--db-snapshot-identifier", snapshotID)), &snapshot)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-snapshot", "--db-snapshot-identifier", snapshotID).Run()
	})
	require.True(t, strings.HasPrefix(snapshot.DBSnapshot.DBSnapshotArn, "arn:aws:rds:"))
	runCLI(t, awsCLI("rds", "wait", "db-snapshot-available", "--db-snapshot-identifier", snapshotID))

	out := runCLI(t, awsCLI("rds", "restore-db-cluster-from-snapshot",
		"--db-cluster-identifier", clusterID,
		"--snapshot-identifier", snapshot.DBSnapshot.DBSnapshotArn,
		"--engine", "aurora-postgresql"))
	cliCleanupDBCluster(t, clusterID)
	var restored struct {
		DBCluster cliDBCluster `json:"DBCluster"`
	}
	parseJSON(t, out, &restored)
	assert.Equal(t, "creating", restored.DBCluster.Status)
	assert.Equal(t, "aurora-postgresql", restored.DBCluster.Engine)
	assert.Equal(t, cliRestoreUsername, restored.DBCluster.MasterUsername)
	assert.Equal(t, cliRestoreDatabase, restored.DBCluster.DatabaseName)
	runCLI(t, awsCLI("rds", "wait", "db-cluster-available", "--db-cluster-identifier", clusterID))
	cliAddAuroraWriter(t, clusterID, "aurora-postgresql")
	cluster := cliDescribeDBCluster(t, clusterID)
	assert.Equal(t, []string{"in-the-instance"}, cliLedger(t, ctx, cliConnectPostgres(t, ctx, cluster.Endpoint, cluster.Port)),
		"the Aurora cluster serves the instance snapshot's data")
}
