package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/smithy-go"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	restoreSourceUsername = "dbadmin"
	restoreSourcePassword = "MasterPassword-123!"
	restoreSourceDatabase = "application"
)

// connectRDSDatabase opens a stock driver on endpoint as the master user.
func connectRDSDatabase(t *testing.T, ctx context.Context, family, endpoint string) auroraSnapshotClient {
	t.Helper()
	if family == "postgres" {
		config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require", restoreSourceUsername, endpoint, restoreSourceDatabase))
		require.NoError(t, err)
		config.Password = restoreSourcePassword
		config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
		conn, err := pgx.ConnectConfig(ctx, config)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return auroraPostgres{ctx: ctx, conn: conn}
	}
	config := mysql.Config{
		User: restoreSourceUsername, Passwd: restoreSourcePassword, Net: "tcp", Addr: endpoint, DBName: restoreSourceDatabase,
		TLSConfig: "skip-verify", AllowCleartextPasswords: true,
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })
	return auroraMySQL{ctx: ctx, db: db}
}

// auroraClusterFixture creates Aurora clusters and their writers and connects
// to their cluster endpoints, deleting everything it made when the test ends.
type auroraClusterFixture struct {
	t      *testing.T
	ctx    context.Context
	client *rds.Client
	engine string
	family string
}

func (f auroraClusterFixture) cleanupCluster(clusterID string) {
	f.t.Cleanup(func() {
		_, _ = f.client.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(clusterID + "-1"), SkipFinalSnapshot: aws.Bool(true),
		})
		_, _ = f.client.DeleteDBCluster(context.Background(), &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true),
		})
	})
}

func (f auroraClusterFixture) addWriter(clusterID string) {
	f.t.Helper()
	instanceID := clusterID + "-1"
	_, err := f.client.CreateDBInstance(f.ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBClusterIdentifier:  aws.String(clusterID),
		Engine:               aws.String(f.engine),
		DBInstanceClass:      aws.String("db.r6g.large"),
	})
	require.NoError(f.t, err)
	require.NoError(f.t, rds.NewDBInstanceAvailableWaiter(f.client, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(f.ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute))
}

func (f auroraClusterFixture) describe(clusterID string) rdsClusterView {
	f.t.Helper()
	described, err := f.client.DescribeDBClusters(f.ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
	require.NoError(f.t, err)
	cluster := described.DBClusters[0]
	return rdsClusterView{
		endpoint: fmt.Sprintf("%s:%d", aws.ToString(cluster.Endpoint), aws.ToInt32(cluster.Port)),
		earliest: cluster.EarliestRestorableTime,
		latest:   cluster.LatestRestorableTime,
	}
}

func (f auroraClusterFixture) connect(clusterID string) auroraSnapshotClient {
	f.t.Helper()
	return connectRDSDatabase(f.t, f.ctx, f.family, f.describe(clusterID).endpoint)
}

type rdsClusterView struct {
	endpoint         string
	earliest, latest *time.Time
}

// nextMillisecondThenWait reads the engine's clock, returns the next
// millisecond — RDS takes RestoreToTime to the millisecond — and returns only
// once the engine's clock has passed it, so every row committed before the
// call commits by that time and every row committed after it commits later.
func nextMillisecondThenWait(t *testing.T, ctx context.Context, database auroraSnapshotClient) time.Time {
	t.Helper()
	switch engine := database.(type) {
	case auroraPostgres:
		var next time.Time
		require.NoError(t, engine.conn.QueryRow(ctx,
			`SELECT date_trunc('milliseconds', clock_timestamp()) + interval '1 millisecond'`).Scan(&next))
		engine.exec(t, fmt.Sprintf(`SELECT pg_sleep_until('%s')`, next.UTC().Format(time.RFC3339Nano)))
		return next
	case auroraMySQL:
		var next string
		require.NoError(t, engine.db.QueryRowContext(ctx,
			`SELECT DATE_FORMAT(NOW(3) + INTERVAL 1000 MICROSECOND, '%Y-%m-%d %H:%i:%s.%f')`).Scan(&next))
		engine.exec(t, fmt.Sprintf(`DO SLEEP(GREATEST(0, TIMESTAMPDIFF(MICROSECOND, NOW(6), '%s')) / 1000000)`, next))
		parsed, err := time.Parse("2006-01-02 15:04:05.000000", next)
		require.NoError(t, err)
		return parsed
	}
	t.Fatalf("unknown database client %T", database)
	return time.Time{}
}

// An Aurora cluster restores to any time in its restorable window: the
// restored cluster holds every row committed by RestoreToTime and none
// committed after it, and accepts writes of its own. The window opens when
// the cluster's engine first serves, and a time outside it is refused.
func TestRDS_AuroraClusterRestoresToAPointInTime(t *testing.T) {
	for _, engine := range []string{"aurora-postgresql", "aurora-mysql"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			family := "postgres"
			if engine == "aurora-mysql" {
				family = "mysql"
			}
			f := auroraClusterFixture{t: t, ctx: ctx, client: rdsClient(), engine: engine, family: family}
			sourceID := "sdk-pitr-source-" + family

			_, err := f.client.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
				DBClusterIdentifier: aws.String(sourceID),
				Engine:              aws.String(f.engine),
				MasterUsername:      aws.String(restoreSourceUsername),
				MasterUserPassword:  aws.String(restoreSourcePassword),
				DatabaseName:        aws.String(restoreSourceDatabase),
			})
			require.NoError(t, err)
			f.cleanupCluster(sourceID)
			f.addWriter(sourceID)
			source := f.connect(sourceID)
			source.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
			source.exec(t, `INSERT INTO ledger VALUES ('before-restore-time')`)
			restoreTo := nextMillisecondThenWait(t, ctx, source)
			source.exec(t, `INSERT INTO ledger VALUES ('after-restore-time')`)

			window := f.describe(sourceID)
			require.NotNil(t, window.earliest, "a cluster whose engine has served reports EarliestRestorableTime")
			require.NotNil(t, window.latest, "a cluster whose engine has served reports LatestRestorableTime")
			assert.False(t, restoreTo.Before(*window.earliest), "the restore time lies after EarliestRestorableTime")
			assert.False(t, restoreTo.After(*window.latest), "the restore time lies before LatestRestorableTime")

			_, err = f.client.RestoreDBClusterToPointInTime(ctx, &rds.RestoreDBClusterToPointInTimeInput{
				DBClusterIdentifier:       aws.String("sdk-pitr-too-early-" + family),
				SourceDBClusterIdentifier: aws.String(sourceID),
				RestoreToTime:             aws.Time(window.earliest.Add(-time.Hour)),
			})
			var apiErr smithy.APIError
			require.True(t, errors.As(err, &apiErr), "a restore time before the window is refused, got %v", err)
			assert.Equal(t, "InvalidRestoreFault", apiErr.ErrorCode())

			restoredID := "sdk-pitr-restored-" + family
			restored, err := f.client.RestoreDBClusterToPointInTime(ctx, &rds.RestoreDBClusterToPointInTimeInput{
				DBClusterIdentifier:       aws.String(restoredID),
				SourceDBClusterIdentifier: aws.String(sourceID),
				RestoreToTime:             aws.Time(restoreTo),
			})
			require.NoError(t, err)
			f.cleanupCluster(restoredID)
			assert.Equal(t, "creating", aws.ToString(restored.DBCluster.Status))
			waitForRDSClusterAvailable(t, f.client, ctx, restoredID)
			f.addWriter(restoredID)
			target := f.connect(restoredID)
			assert.Equal(t, []string{"before-restore-time"}, target.entries(t),
				"the restored cluster holds the rows committed by RestoreToTime and none after it")
			target.exec(t, `INSERT INTO ledger VALUES ('restored-write')`)
			assert.Equal(t, []string{"before-restore-time", "restored-write"}, target.entries(t),
				"the restored cluster accepts writes")
		})
	}
}

// RestoreDBClusterFromSnapshot migrates an RDS for PostgreSQL or RDS for MySQL
// DB snapshot, named by its ARN, into a new Aurora cluster that serves the
// instance's data under its master credential.
func TestRDS_AuroraClusterRestoresFromADBInstanceSnapshot(t *testing.T) {
	for _, engine := range []string{"postgres", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			f := auroraClusterFixture{t: t, ctx: ctx, client: rdsClient(), engine: "aurora-" + engine, family: engine}
			if engine == "postgres" {
				f.engine = "aurora-postgresql"
			}
			instanceID := "sdk-migrate-" + engine
			snapshotID := instanceID + "-snap"

			_, err := f.client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
				DBInstanceIdentifier: aws.String(instanceID),
				DBInstanceClass:      aws.String("db.t3.micro"),
				Engine:               aws.String(engine),
				AllocatedStorage:     aws.Int32(20),
				MasterUsername:       aws.String(restoreSourceUsername),
				MasterUserPassword:   aws.String(restoreSourcePassword),
				DBName:               aws.String(restoreSourceDatabase),
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = f.client.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
					DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
				})
			})
			instance, err := rds.NewDBInstanceAvailableWaiter(f.client, func(o *rds.DBInstanceAvailableWaiterOptions) {
				o.MinDelay = waiterMinDelay
				o.MaxDelay = waiterMaxDelay
			}).WaitForOutput(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute)
			require.NoError(t, err)
			endpoint := instance.DBInstances[0].Endpoint
			source := connectRDSDatabase(t, ctx, engine, fmt.Sprintf("%s:%d", aws.ToString(endpoint.Address), aws.ToInt32(endpoint.Port)))
			source.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
			source.exec(t, `INSERT INTO ledger VALUES ('in-the-instance')`)

			created, err := f.client.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
				DBInstanceIdentifier: aws.String(instanceID),
				DBSnapshotIdentifier: aws.String(snapshotID),
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = f.client.DeleteDBSnapshot(context.Background(), &rds.DeleteDBSnapshotInput{DBSnapshotIdentifier: aws.String(snapshotID)})
			})
			require.NoError(t, rds.NewDBSnapshotAvailableWaiter(f.client, func(o *rds.DBSnapshotAvailableWaiterOptions) {
				o.MinDelay = waiterMinDelay
				o.MaxDelay = waiterMaxDelay
			}).Wait(ctx, &rds.DescribeDBSnapshotsInput{DBSnapshotIdentifier: aws.String(snapshotID)}, 3*time.Minute))

			clusterID := "sdk-migrated-" + engine
			restored, err := f.client.RestoreDBClusterFromSnapshot(ctx, &rds.RestoreDBClusterFromSnapshotInput{
				DBClusterIdentifier: aws.String(clusterID),
				SnapshotIdentifier:  created.DBSnapshot.DBSnapshotArn,
				Engine:              aws.String(f.engine),
			})
			require.NoError(t, err)
			f.cleanupCluster(clusterID)
			assert.Equal(t, "creating", aws.ToString(restored.DBCluster.Status))
			assert.Equal(t, f.engine, aws.ToString(restored.DBCluster.Engine))
			assert.Equal(t, restoreSourceUsername, aws.ToString(restored.DBCluster.MasterUsername))
			assert.Equal(t, restoreSourceDatabase, aws.ToString(restored.DBCluster.DatabaseName))
			waitForRDSClusterAvailable(t, f.client, ctx, clusterID)
			f.addWriter(clusterID)
			assert.Equal(t, []string{"in-the-instance"}, f.connect(clusterID).entries(t),
				"the Aurora cluster serves the instance snapshot's data")
		})
	}
}
