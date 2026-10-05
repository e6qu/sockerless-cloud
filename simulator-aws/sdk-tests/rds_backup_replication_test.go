package aws_sdk_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// StartDBInstanceAutomatedBackupsReplication, called in another Region,
// copies a DB instance's automated snapshots and log into a replicated
// automated backup there. DescribeDBInstanceAutomatedBackups lists it in that
// Region only, pending until it holds an automated snapshot and replicating
// after, and the source names it among its DBInstanceAutomatedBackupsReplications.
// RestoreDBInstanceToPointInTime restores through its ARN every row the
// source committed, before StopDBInstanceAutomatedBackupsReplication leaves it
// retained and after the source is deleted. A retained replicated backup is
// deleted like any other.
func TestRDS_AutomatedBackupsReplicateToAnotherRegion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: "postgres"}
	destination := rds.NewFromConfig(sdkConfig(), func(o *rds.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.Region = "us-west-2"
	})
	sourceID := "sdk-replicated-backups"
	created, err := f.client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(sourceID),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("postgres"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		DBName:                aws.String(restoreSourceDatabase),
		BackupRetentionPeriod: aws.Int32(1),
	})
	require.NoError(t, err)
	f.cleanup(sourceID)
	sourceARN := created.DBInstance.DBInstanceArn

	_, err = f.client.StartDBInstanceAutomatedBackupsReplication(ctx, &rds.StartDBInstanceAutomatedBackupsReplicationInput{
		SourceDBInstanceArn: sourceARN,
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")

	source := f.connect(sourceID)
	source.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
	source.exec(t, `INSERT INTO ledger VALUES ('before-replication')`)

	started, err := destination.StartDBInstanceAutomatedBackupsReplication(ctx, &rds.StartDBInstanceAutomatedBackupsReplicationInput{
		SourceDBInstanceArn:   sourceARN,
		BackupRetentionPeriod: aws.Int32(3),
		Tags:                  []types.Tag{{Key: aws.String("copy"), Value: aws.String("us-west-2")}},
	})
	require.NoError(t, err)
	backupARN := started.DBInstanceAutomatedBackup.DBInstanceAutomatedBackupsArn
	t.Cleanup(func() {
		_, _ = destination.StopDBInstanceAutomatedBackupsReplication(context.Background(), &rds.StopDBInstanceAutomatedBackupsReplicationInput{
			SourceDBInstanceArn: sourceARN,
		})
		_, _ = destination.DeleteDBInstanceAutomatedBackup(context.Background(), &rds.DeleteDBInstanceAutomatedBackupInput{
			DBInstanceAutomatedBackupsArn: backupARN,
		})
	})
	assert.True(t, strings.HasPrefix(aws.ToString(backupARN), "arn:aws:rds:us-west-2:123456789012:auto-backup:ab-"),
		"the replicated automated backup lives in the destination Region, got %s", aws.ToString(backupARN))
	assert.Equal(t, "us-east-1", aws.ToString(started.DBInstanceAutomatedBackup.Region), "Region names the source's Region")
	assert.Equal(t, aws.ToString(sourceARN), aws.ToString(started.DBInstanceAutomatedBackup.DBInstanceArn))
	assert.Equal(t, "pending", aws.ToString(started.DBInstanceAutomatedBackup.Status))
	assert.Equal(t, int32(3), aws.ToInt32(started.DBInstanceAutomatedBackup.BackupRetentionPeriod))

	_, err = destination.StartDBInstanceAutomatedBackupsReplication(ctx, &rds.StartDBInstanceAutomatedBackupsReplicationInput{
		SourceDBInstanceArn: sourceARN,
	})
	assertAWSAPIErrorCode(t, err, "InvalidDBInstanceAutomatedBackupState")

	// Amazon RDS offers no waiter for an automated backup; its Status is what
	// the console and the CLI poll.
	require.Eventually(t, func() bool {
		listed, err := destination.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{
			DBInstanceAutomatedBackupsArn: backupARN,
		})
		return err == nil && len(listed.DBInstanceAutomatedBackups) == 1 &&
			aws.ToString(listed.DBInstanceAutomatedBackups[0].Status) == "replicating"
	}, 2*time.Minute, waiterMinDelay, "the replicated automated backup copies the source's automated snapshot")
	inHome, err := f.client.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{
		DBInstanceAutomatedBackupsArn: backupARN,
	})
	require.NoError(t, err)
	assert.Empty(t, inHome.DBInstanceAutomatedBackups, "the source's Region holds no replicated automated backup")
	described := f.waitAvailable(sourceID)
	require.Len(t, described.DBInstanceAutomatedBackupsReplications, 1)
	assert.Equal(t, aws.ToString(backupARN), aws.ToString(described.DBInstanceAutomatedBackupsReplications[0].DBInstanceAutomatedBackupsArn))
	tags, err := destination.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{ResourceName: backupARN})
	require.NoError(t, err)
	require.Len(t, tags.TagList, 1)
	assert.Equal(t, "us-west-2", aws.ToString(tags.TagList[0].Value))

	source.exec(t, `INSERT INTO ledger VALUES ('after-replication')`)
	replicatingID := "sdk-replicated-backups-replicating"
	_, err = destination.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
		SourceDBInstanceAutomatedBackupsArn: backupARN,
		TargetDBInstanceIdentifier:          aws.String(replicatingID),
		UseLatestRestorableTime:             aws.Bool(true),
	})
	require.NoError(t, err)
	f.cleanup(replicatingID)
	assert.Equal(t, []string{"after-replication", "before-replication"}, f.connect(replicatingID).entries(t),
		"a restore from the replicating backup holds every row the source committed")

	_, err = destination.DeleteDBInstanceAutomatedBackup(ctx, &rds.DeleteDBInstanceAutomatedBackupInput{
		DBInstanceAutomatedBackupsArn: backupARN,
	})
	assertAWSAPIErrorCode(t, err, "InvalidDBInstanceAutomatedBackupState")

	source.exec(t, `INSERT INTO ledger VALUES ('before-stop')`)
	stopped, err := destination.StopDBInstanceAutomatedBackupsReplication(ctx, &rds.StopDBInstanceAutomatedBackupsReplicationInput{
		SourceDBInstanceArn: sourceARN,
	})
	require.NoError(t, err)
	assert.Equal(t, "retained", aws.ToString(stopped.DBInstanceAutomatedBackup.Status))
	assert.Empty(t, f.waitAvailable(sourceID).DBInstanceAutomatedBackupsReplications)
	source.exec(t, `INSERT INTO ledger VALUES ('after-stop')`)

	_, err = f.client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(sourceID), SkipFinalSnapshot: aws.Bool(true),
	})
	require.NoError(t, err)
	require.NoError(t, rds.NewDBInstanceDeletedWaiter(f.client, func(o *rds.DBInstanceDeletedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(sourceID)}, 3*time.Minute))

	retainedID := "sdk-replicated-backups-retained"
	_, err = destination.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
		SourceDBInstanceAutomatedBackupsArn: backupARN,
		TargetDBInstanceIdentifier:          aws.String(retainedID),
		UseLatestRestorableTime:             aws.Bool(true),
	})
	require.NoError(t, err)
	f.cleanup(retainedID)
	assert.Equal(t, []string{"after-replication", "before-replication", "before-stop"}, f.connect(retainedID).entries(t),
		"the retained replicated backup restores what it copied before the replication stopped, after its source is gone")

	deleted, err := destination.DeleteDBInstanceAutomatedBackup(ctx, &rds.DeleteDBInstanceAutomatedBackupInput{
		DBInstanceAutomatedBackupsArn: backupARN,
	})
	require.NoError(t, err)
	assert.Equal(t, "deleting", aws.ToString(deleted.DBInstanceAutomatedBackup.Status))
	_, err = destination.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
		SourceDBInstanceAutomatedBackupsArn: backupARN,
		TargetDBInstanceIdentifier:          aws.String("sdk-replicated-backups-gone"),
		UseLatestRestorableTime:             aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "DBInstanceAutomatedBackupNotFound")
}

// A replication started while its new source, which no client has connected
// to, still takes its first automated backup replicates that backup once
// Amazon RDS has taken it, as the Terraform provider's create waiter expects.
func TestRDS_AutomatedBackupsReplicationTakesTheSourcesFirstBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: "postgres"}
	destination := rds.NewFromConfig(sdkConfig(), func(o *rds.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.Region = "us-west-2"
	})
	sourceID := "sdk-replicated-unconnected"
	created, err := f.client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(sourceID),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("postgres"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		BackupRetentionPeriod: aws.Int32(1),
	})
	require.NoError(t, err)
	f.cleanup(sourceID)
	f.waitAvailable(sourceID)

	started, err := destination.StartDBInstanceAutomatedBackupsReplication(ctx, &rds.StartDBInstanceAutomatedBackupsReplicationInput{
		SourceDBInstanceArn:   created.DBInstance.DBInstanceArn,
		BackupRetentionPeriod: aws.Int32(3),
	})
	require.NoError(t, err)
	backupARN := started.DBInstanceAutomatedBackup.DBInstanceAutomatedBackupsArn
	t.Cleanup(func() {
		_, _ = destination.StopDBInstanceAutomatedBackupsReplication(context.Background(), &rds.StopDBInstanceAutomatedBackupsReplicationInput{
			SourceDBInstanceArn: created.DBInstance.DBInstanceArn,
		})
		_, _ = destination.DeleteDBInstanceAutomatedBackup(context.Background(), &rds.DeleteDBInstanceAutomatedBackupInput{
			DBInstanceAutomatedBackupsArn: backupARN,
		})
	})
	require.Eventually(t, func() bool {
		listed, err := destination.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{
			DBInstanceAutomatedBackupsArn: backupARN,
		})
		return err == nil && len(listed.DBInstanceAutomatedBackups) == 1 &&
			aws.ToString(listed.DBInstanceAutomatedBackups[0].Status) == "replicating"
	}, 2*time.Minute, waiterMinDelay, "the replicated automated backup copies the source's first automated snapshot")
}
