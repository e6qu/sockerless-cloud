package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waitForRDSClusterSnapshotAvailable(t *testing.T, c *rds.Client, ctx context.Context, snapshotID string) {
	t.Helper()
	require.NoError(t, rds.NewDBClusterSnapshotAvailableWaiter(c, func(o *rds.DBClusterSnapshotAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBClusterSnapshotsInput{DBClusterSnapshotIdentifier: aws.String(snapshotID)}, 3*time.Minute),
		"DB cluster snapshot %s must settle once its capture completes", snapshotID)
}

func waitForRDSClusterAvailable(t *testing.T, c *rds.Client, ctx context.Context, clusterID string) {
	t.Helper()
	require.NoError(t, rds.NewDBClusterAvailableWaiter(c, func(o *rds.DBClusterAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)}, 3*time.Minute),
		"DB cluster %s must become available", clusterID)
}

// auroraSnapshotClient reads and writes one Aurora cluster through its
// cluster endpoint with a stock driver.
type auroraSnapshotClient interface {
	exec(t *testing.T, statement string)
	entries(t *testing.T) []string
}

type auroraPostgres struct {
	ctx  context.Context
	conn *pgx.Conn
}

func (a auroraPostgres) exec(t *testing.T, statement string) {
	t.Helper()
	_, err := a.conn.Exec(a.ctx, statement)
	require.NoError(t, err, statement)
}

func (a auroraPostgres) entries(t *testing.T) []string {
	t.Helper()
	rows, err := a.conn.Query(a.ctx, `SELECT entry FROM ledger ORDER BY entry`)
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

type auroraMySQL struct {
	ctx context.Context
	db  *sql.DB
}

func (a auroraMySQL) exec(t *testing.T, statement string) {
	t.Helper()
	_, err := a.db.ExecContext(a.ctx, statement)
	require.NoError(t, err, statement)
}

func (a auroraMySQL) entries(t *testing.T) []string {
	t.Helper()
	rows, err := a.db.QueryContext(a.ctx, `SELECT entry FROM ledger ORDER BY entry`)
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

// An Aurora DB cluster snapshot carries the cluster volume's data, and every
// cluster restore returns to the data its source held: a restore from a
// snapshot (here, from a copy of it) has the rows written before the snapshot
// and none written after; a point-in-time restore to the latest restorable
// time has every committed row; and the final snapshot DeleteDBCluster takes
// restores the cluster as it was deleted. Each restored cluster serves its
// data under the source's master credential through a stock driver.
func TestRDS_AuroraClusterSnapshotsCaptureAndRestoreTheClusterVolume(t *testing.T) {
	const (
		username = "dbadmin"
		password = "MasterPassword-123!"
		database = "application"
	)
	for _, engine := range []string{"aurora-postgresql", "aurora-mysql"} {
		t.Run(engine, func(t *testing.T) {
			testContext, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			c := rdsClient()
			prefix := "sdk-snapdata-" + engine[len("aurora-"):]

			addWriter := func(clusterID string) {
				t.Helper()
				instanceID := clusterID + "-1"
				_, err := c.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
					DBInstanceIdentifier: aws.String(instanceID),
					DBClusterIdentifier:  aws.String(clusterID),
					Engine:               aws.String(engine),
					DBInstanceClass:      aws.String("db.r6g.large"),
				})
				require.NoError(t, err)
				t.Cleanup(func() {
					_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
						DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
					})
				})
				require.NoError(t, rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
					o.MinDelay = waiterMinDelay
					o.MaxDelay = waiterMaxDelay
				}).Wait(testContext, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute))
			}
			cleanupCluster := func(clusterID string) {
				t.Cleanup(func() {
					_, _ = c.DeleteDBCluster(context.Background(), &rds.DeleteDBClusterInput{
						DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true),
					})
				})
			}
			cleanupSnapshot := func(snapshotID string) {
				t.Cleanup(func() {
					_, _ = c.DeleteDBClusterSnapshot(context.Background(), &rds.DeleteDBClusterSnapshotInput{
						DBClusterSnapshotIdentifier: aws.String(snapshotID),
					})
				})
			}
			connect := func(clusterID string) auroraSnapshotClient {
				t.Helper()
				described, err := c.DescribeDBClusters(testContext, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
				require.NoError(t, err)
				endpoint := fmt.Sprintf("%s:%d", aws.ToString(described.DBClusters[0].Endpoint), aws.ToInt32(described.DBClusters[0].Port))
				if engine == "aurora-postgresql" {
					config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require", username, endpoint, database))
					require.NoError(t, err)
					config.Password = password
					config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
					conn, err := pgx.ConnectConfig(testContext, config)
					require.NoError(t, err)
					t.Cleanup(func() { _ = conn.Close(context.Background()) })
					return auroraPostgres{ctx: testContext, conn: conn}
				}
				config := mysql.Config{
					User: username, Passwd: password, Net: "tcp", Addr: endpoint, DBName: database,
					TLSConfig: "skip-verify", AllowCleartextPasswords: true,
				}
				db, err := sql.Open("mysql", config.FormatDSN())
				require.NoError(t, err)
				db.SetMaxIdleConns(0)
				t.Cleanup(func() { _ = db.Close() })
				return auroraMySQL{ctx: testContext, db: db}
			}

			sourceID := prefix + "-source"
			_, err := c.CreateDBCluster(testContext, &rds.CreateDBClusterInput{
				DBClusterIdentifier: aws.String(sourceID),
				Engine:              aws.String(engine),
				MasterUsername:      aws.String(username),
				MasterUserPassword:  aws.String(password),
				DatabaseName:        aws.String(database),
			})
			require.NoError(t, err)
			cleanupCluster(sourceID)
			addWriter(sourceID)
			source := connect(sourceID)
			source.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
			source.exec(t, `INSERT INTO ledger VALUES ('before-snapshot')`)

			snapshotID := prefix + "-snap"
			created, err := c.CreateDBClusterSnapshot(testContext, &rds.CreateDBClusterSnapshotInput{
				DBClusterSnapshotIdentifier: aws.String(snapshotID),
				DBClusterIdentifier:         aws.String(sourceID),
			})
			require.NoError(t, err)
			cleanupSnapshot(snapshotID)
			assert.Equal(t, "creating", aws.ToString(created.DBClusterSnapshot.Status))
			waitForRDSClusterSnapshotAvailable(t, c, testContext, snapshotID)
			source.exec(t, `INSERT INTO ledger VALUES ('after-snapshot')`)

			copyID := prefix + "-copy"
			copied, err := c.CopyDBClusterSnapshot(testContext, &rds.CopyDBClusterSnapshotInput{
				SourceDBClusterSnapshotIdentifier: aws.String(snapshotID),
				TargetDBClusterSnapshotIdentifier: aws.String(copyID),
			})
			require.NoError(t, err)
			cleanupSnapshot(copyID)
			assert.Equal(t, "copying", aws.ToString(copied.DBClusterSnapshot.Status))
			waitForRDSClusterSnapshotAvailable(t, c, testContext, copyID)

			fromSnapshotID := prefix + "-from-snap"
			restored, err := c.RestoreDBClusterFromSnapshot(testContext, &rds.RestoreDBClusterFromSnapshotInput{
				DBClusterIdentifier: aws.String(fromSnapshotID),
				SnapshotIdentifier:  aws.String(copyID),
				Engine:              aws.String(engine),
			})
			require.NoError(t, err)
			cleanupCluster(fromSnapshotID)
			assert.Equal(t, "creating", aws.ToString(restored.DBCluster.Status))
			assert.Equal(t, username, aws.ToString(restored.DBCluster.MasterUsername))
			assert.Equal(t, database, aws.ToString(restored.DBCluster.DatabaseName))
			waitForRDSClusterAvailable(t, c, testContext, fromSnapshotID)
			addWriter(fromSnapshotID)
			assert.Equal(t, []string{"before-snapshot"}, connect(fromSnapshotID).entries(t),
				"a restore from the snapshot has the rows from before it and none from after it")

			pointInTimeID := prefix + "-pit"
			pointInTime, err := c.RestoreDBClusterToPointInTime(testContext, &rds.RestoreDBClusterToPointInTimeInput{
				DBClusterIdentifier:       aws.String(pointInTimeID),
				SourceDBClusterIdentifier: aws.String(sourceID),
				UseLatestRestorableTime:   aws.Bool(true),
			})
			require.NoError(t, err)
			cleanupCluster(pointInTimeID)
			assert.Equal(t, "creating", aws.ToString(pointInTime.DBCluster.Status))
			waitForRDSClusterAvailable(t, c, testContext, pointInTimeID)
			addWriter(pointInTimeID)
			assert.Equal(t, []string{"after-snapshot", "before-snapshot"}, connect(pointInTimeID).entries(t),
				"a restore to the latest restorable time has every committed row")

			source.exec(t, `INSERT INTO ledger VALUES ('before-delete')`)
			_, err = c.DeleteDBInstance(testContext, &rds.DeleteDBInstanceInput{
				DBInstanceIdentifier: aws.String(sourceID + "-1"), SkipFinalSnapshot: aws.Bool(true),
			})
			require.NoError(t, err)
			finalID := prefix + "-final"
			_, err = c.DeleteDBCluster(testContext, &rds.DeleteDBClusterInput{
				DBClusterIdentifier:       aws.String(sourceID),
				FinalDBSnapshotIdentifier: aws.String(finalID),
			})
			require.NoError(t, err)
			cleanupSnapshot(finalID)
			waitForRDSClusterSnapshotAvailable(t, c, testContext, finalID)
			fromFinalID := prefix + "-from-final"
			_, err = c.RestoreDBClusterFromSnapshot(testContext, &rds.RestoreDBClusterFromSnapshotInput{
				DBClusterIdentifier: aws.String(fromFinalID),
				SnapshotIdentifier:  aws.String(finalID),
				Engine:              aws.String(engine),
			})
			require.NoError(t, err)
			cleanupCluster(fromFinalID)
			waitForRDSClusterAvailable(t, c, testContext, fromFinalID)
			addWriter(fromFinalID)
			assert.Equal(t, []string{"after-snapshot", "before-delete", "before-snapshot"}, connect(fromFinalID).entries(t),
				"the final snapshot restores the cluster as it was deleted")
		})
	}
}
