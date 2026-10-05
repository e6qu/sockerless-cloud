package aws_sdk_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Aurora cluster takes an automated DB cluster snapshot when its engine
// first serves and another at the start of each PreferredBackupWindow, which
// cannot be deleted by hand. A restore to a time after the window's snapshot
// starts from it and replays the log written since, rows committed while the
// snapshot was taken included, exactly once.
func TestRDS_AuroraAutomatedBackups(t *testing.T) {
	for _, engine := range []string{"aurora-postgresql", "aurora-mysql"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			family := "postgres"
			if engine == "aurora-mysql" {
				family = "mysql"
			}
			f := auroraClusterFixture{t: t, ctx: ctx, client: rdsClient(), engine: engine, family: family}
			sourceID := "sdk-auto-backup-" + family
			_, err := f.client.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
				DBClusterIdentifier:   aws.String(sourceID),
				Engine:                aws.String(f.engine),
				MasterUsername:        aws.String(restoreSourceUsername),
				MasterUserPassword:    aws.String(restoreSourcePassword),
				DatabaseName:          aws.String(restoreSourceDatabase),
				BackupRetentionPeriod: aws.Int32(1),
			})
			require.NoError(t, err)
			f.cleanupCluster(sourceID)
			f.addWriter(sourceID)
			source := f.connect(sourceID)
			source.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL PRIMARY KEY)`)
			committed := []string{"before-window"}
			source.exec(t, `INSERT INTO ledger VALUES ('before-window')`)

			automated := func() []types.DBClusterSnapshot {
				t.Helper()
				out, err := f.client.DescribeDBClusterSnapshots(ctx, &rds.DescribeDBClusterSnapshotsInput{
					DBClusterIdentifier: aws.String(sourceID), SnapshotType: aws.String("automated"),
				})
				require.NoError(t, err)
				return out.DBClusterSnapshots
			}
			first := automated()
			require.Len(t, first, 1, "the engine's first start takes an automated snapshot")
			assert.Equal(t, "available", aws.ToString(first[0].Status))
			assert.Equal(t, "automated", aws.ToString(first[0].SnapshotType))
			assert.True(t, strings.HasPrefix(aws.ToString(first[0].DBClusterSnapshotIdentifier), "rds:"+sourceID+"-"))
			_, err = f.client.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{DBClusterSnapshotIdentifier: first[0].DBClusterSnapshotIdentifier})
			var apiErr smithy.APIError
			require.True(t, errors.As(err, &apiErr), "an automated snapshot cannot be deleted, got %v", err)
			assert.Equal(t, "InvalidDBClusterSnapshotStateFault", apiErr.ErrorCode())

			_, err = f.client.ModifyDBCluster(ctx, &rds.ModifyDBClusterInput{
				DBClusterIdentifier: aws.String(sourceID), PreferredBackupWindow: aws.String("7am-8am"),
			})
			require.True(t, errors.As(err, &apiErr), "a malformed backup window is refused, got %v", err)
			assert.Equal(t, "InvalidParameterValue", apiErr.ErrorCode())
			windowStart := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
			window := windowStart.Format("15:04") + "-" + windowStart.Add(30*time.Minute).Format("15:04")
			modified, err := f.client.ModifyDBCluster(ctx, &rds.ModifyDBClusterInput{
				DBClusterIdentifier: aws.String(sourceID), PreferredBackupWindow: aws.String(window), ApplyImmediately: aws.Bool(true),
			})
			require.NoError(t, err)
			assert.Equal(t, window, aws.ToString(modified.DBCluster.PreferredBackupWindow))

			// Rows keep committing while the window's snapshot is taken; RDS
			// lists no event to wait on, so the loop reads the snapshot list
			// between writes.
			var windowSnapshot types.DBClusterSnapshot
			for n := 0; windowSnapshot.DBClusterSnapshotIdentifier == nil; n++ {
				entry := fmt.Sprintf("during-%05d", n)
				source.exec(t, fmt.Sprintf(`INSERT INTO ledger VALUES ('%s')`, entry))
				committed = append(committed, entry)
				for _, snapshot := range automated() {
					if aws.ToString(snapshot.DBClusterSnapshotIdentifier) != aws.ToString(first[0].DBClusterSnapshotIdentifier) &&
						aws.ToString(snapshot.Status) == "available" {
						windowSnapshot = snapshot
					}
				}
				time.Sleep(waiterMinDelay)
			}
			require.NotNil(t, windowSnapshot.SnapshotCreateTime)
			assert.False(t, windowSnapshot.SnapshotCreateTime.Before(windowStart), "the window's snapshot is taken once the window opens")
			assert.Equal(t, "rds:"+sourceID+"-"+windowStart.Format("2006-01-02-15-04"), aws.ToString(windowSnapshot.DBClusterSnapshotIdentifier))

			source.exec(t, `INSERT INTO ledger VALUES ('after-window')`)
			committed = append(committed, "after-window")
			restoreTo := nextMillisecondThenWait(t, ctx, source)
			source.exec(t, `INSERT INTO ledger VALUES ('too-late')`)

			restoredID := "sdk-auto-backup-restored-" + family
			_, err = f.client.RestoreDBClusterToPointInTime(ctx, &rds.RestoreDBClusterToPointInTimeInput{
				DBClusterIdentifier:       aws.String(restoredID),
				SourceDBClusterIdentifier: aws.String(sourceID),
				RestoreToTime:             aws.Time(restoreTo),
			})
			require.NoError(t, err)
			f.cleanupCluster(restoredID)
			waitForRDSClusterAvailable(t, f.client, ctx, restoredID)
			f.addWriter(restoredID)
			restored := f.connect(restoredID).entries(t)
			assert.ElementsMatch(t, committed, restored,
				"the restore holds every row committed by RestoreToTime, each once, and none after it")
		})
	}
}

// Amazon Aurora takes a DB cluster's first automated backup when it creates
// the cluster, whether or not a client ever connects, so the cluster restores
// to a time from the start; the restored cluster takes its own first
// automated backup the same way.
func TestRDS_AuroraClusterTakesItsFirstAutomatedBackupWithoutAClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	f := auroraClusterFixture{t: t, ctx: ctx, client: rdsClient(), engine: "aurora-postgresql", family: "postgres"}
	sourceID, restoredID := "sdk-aurora-first-backup", "sdk-aurora-first-backup-restored"
	_, err := f.client.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier:   aws.String(sourceID),
		Engine:                aws.String(f.engine),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		DatabaseName:          aws.String(restoreSourceDatabase),
		BackupRetentionPeriod: aws.Int32(1),
	})
	require.NoError(t, err)
	f.cleanupCluster(sourceID)

	awaitFirstBackup := func(clusterID, why string) {
		t.Helper()
		automated := &rds.DescribeDBClusterSnapshotsInput{
			DBClusterIdentifier: aws.String(clusterID), SnapshotType: aws.String("automated"),
		}
		require.NoError(t, rds.NewDBClusterSnapshotAvailableWaiter(f.client, func(o *rds.DBClusterSnapshotAvailableWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).Wait(ctx, automated, 3*time.Minute), why)
		snapshots, err := f.client.DescribeDBClusterSnapshots(ctx, automated)
		require.NoError(t, err)
		require.Len(t, snapshots.DBClusterSnapshots, 1, why)
		assert.True(t, strings.HasPrefix(aws.ToString(snapshots.DBClusterSnapshots[0].DBClusterSnapshotIdentifier), "rds:"+clusterID+"-"))
		view := f.describe(clusterID)
		require.NotNil(t, view.earliest, "a cluster with an automated backup reports EarliestRestorableTime")
		require.NotNil(t, view.latest)
	}
	awaitFirstBackup(sourceID, "Amazon Aurora takes the first automated backup when it creates the cluster")

	_, err = f.client.RestoreDBClusterToPointInTime(ctx, &rds.RestoreDBClusterToPointInTimeInput{
		DBClusterIdentifier:       aws.String(restoredID),
		SourceDBClusterIdentifier: aws.String(sourceID),
		UseLatestRestorableTime:   aws.Bool(true),
	})
	require.NoError(t, err, "a cluster no client has connected to restores to a time")
	f.cleanupCluster(restoredID)
	waitForRDSClusterAvailable(t, f.client, ctx, restoredID)
	awaitFirstBackup(restoredID, "Amazon Aurora takes the first automated backup of a restored cluster")
}
