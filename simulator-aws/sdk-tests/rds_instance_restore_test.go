package aws_sdk_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsInstanceFixture creates DB instances, waits for them, and connects to
// their endpoints as the master user.
type rdsInstanceFixture struct {
	t      *testing.T
	ctx    context.Context
	client *rds.Client
	family string
}

func (f rdsInstanceFixture) cleanup(instanceID string) {
	f.t.Cleanup(func() {
		_, _ = f.client.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
		})
	})
}

func (f rdsInstanceFixture) waitAvailable(instanceID string) types.DBInstance {
	f.t.Helper()
	out, err := rds.NewDBInstanceAvailableWaiter(f.client, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(f.ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 5*time.Minute)
	require.NoError(f.t, err, "DB instance %s must become available", instanceID)
	return out.DBInstances[0]
}

func (f rdsInstanceFixture) connect(instanceID string) auroraSnapshotClient {
	f.t.Helper()
	instance := f.waitAvailable(instanceID)
	endpoint := fmt.Sprintf("%s:%d", aws.ToString(instance.Endpoint.Address), aws.ToInt32(instance.Endpoint.Port))
	return connectRDSDatabase(f.t, f.ctx, f.family, endpoint)
}

// A DB instance restores to any time in its restorable window: the restored
// instance holds every row committed by RestoreTime and none committed after
// it, and accepts writes of its own; a restore to the latest restorable time
// holds every committed row. The instance takes an automated DB snapshot when
// it is created, which cannot be deleted by hand, and an instance with no
// backup retention does not restore to a time.
func TestRDS_InstanceRestoresToAPointInTime(t *testing.T) {
	for _, engine := range []string{"postgres", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: engine}
			sourceID := "sdk-instance-pitr-" + engine
			create := func(instanceID string, retention int32) {
				_, err := f.client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
					DBInstanceIdentifier:  aws.String(instanceID),
					DBInstanceClass:       aws.String("db.t3.micro"),
					Engine:                aws.String(engine),
					AllocatedStorage:      aws.Int32(20),
					MasterUsername:        aws.String(restoreSourceUsername),
					MasterUserPassword:    aws.String(restoreSourcePassword),
					DBName:                aws.String(restoreSourceDatabase),
					BackupRetentionPeriod: aws.Int32(retention),
				})
				require.NoError(t, err)
				f.cleanup(instanceID)
			}
			create(sourceID, 1)
			source := f.connect(sourceID)
			source.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
			source.exec(t, `INSERT INTO ledger VALUES ('before-restore-time')`)
			restoreTo := nextMillisecondThenWait(t, ctx, source)
			source.exec(t, `INSERT INTO ledger VALUES ('after-restore-time')`)

			described := f.waitAvailable(sourceID)
			assert.Equal(t, int32(1), aws.ToInt32(described.BackupRetentionPeriod))
			require.NotNil(t, described.LatestRestorableTime, "an instance whose engine has served reports LatestRestorableTime")
			assert.False(t, restoreTo.After(*described.LatestRestorableTime), "the restore time lies before LatestRestorableTime")
			snapshots, err := f.client.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{
				DBInstanceIdentifier: aws.String(sourceID), SnapshotType: aws.String("automated"),
			})
			require.NoError(t, err)
			require.Len(t, snapshots.DBSnapshots, 1, "the instance's creation takes one automated snapshot")
			automated := snapshots.DBSnapshots[0]
			assert.True(t, strings.HasPrefix(aws.ToString(automated.DBSnapshotIdentifier), "rds:"+sourceID+"-"))
			_, err = f.client.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{DBSnapshotIdentifier: automated.DBSnapshotIdentifier})
			var apiErr smithy.APIError
			require.True(t, errors.As(err, &apiErr), "an automated snapshot cannot be deleted, got %v", err)
			assert.Equal(t, "InvalidDBSnapshotState", apiErr.ErrorCode())

			_, err = f.client.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
				SourceDBInstanceIdentifier: aws.String(sourceID),
				TargetDBInstanceIdentifier: aws.String("sdk-instance-pitr-too-early-" + engine),
				RestoreTime:                aws.Time(automated.SnapshotCreateTime.Add(-time.Hour)),
			})
			require.True(t, errors.As(err, &apiErr), "a restore time before the window is refused, got %v", err)
			assert.Equal(t, "InvalidRestoreFault", apiErr.ErrorCode())

			unbacked := "sdk-instance-no-backups-" + engine
			create(unbacked, 0)
			_, err = f.client.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
				SourceDBInstanceIdentifier: aws.String(unbacked),
				TargetDBInstanceIdentifier: aws.String(unbacked + "-restored"),
				UseLatestRestorableTime:    aws.Bool(true),
			})
			require.True(t, errors.As(err, &apiErr), "an instance without backup retention does not restore, got %v", err)
			assert.Equal(t, "PointInTimeRestoreNotEnabled", apiErr.ErrorCode())

			restoredID := "sdk-instance-pitr-restored-" + engine
			restored, err := f.client.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
				SourceDBInstanceIdentifier: aws.String(sourceID),
				TargetDBInstanceIdentifier: aws.String(restoredID),
				RestoreTime:                aws.Time(restoreTo),
			})
			require.NoError(t, err)
			f.cleanup(restoredID)
			assert.Equal(t, "creating", aws.ToString(restored.DBInstance.DBInstanceStatus))
			target := f.connect(restoredID)
			assert.Equal(t, []string{"before-restore-time"}, target.entries(t),
				"the restored instance holds the rows committed by RestoreTime and none after it")
			target.exec(t, `INSERT INTO ledger VALUES ('restored-write')`)
			assert.Equal(t, []string{"before-restore-time", "restored-write"}, target.entries(t),
				"the restored instance accepts writes")

			latestID := "sdk-instance-pitr-latest-" + engine
			_, err = f.client.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
				SourceDBInstanceIdentifier: aws.String(sourceID),
				TargetDBInstanceIdentifier: aws.String(latestID),
				UseLatestRestorableTime:    aws.Bool(true),
			})
			require.NoError(t, err)
			f.cleanup(latestID)
			assert.Equal(t, []string{"after-restore-time", "before-restore-time"}, f.connect(latestID).entries(t),
				"a restore to the latest restorable time holds every committed row")
		})
	}
}

// Amazon RDS takes a DB instance's first automated backup when it creates the
// instance, whether or not a client ever connects, and takes one again when
// ModifyDBInstance turns automated backups back on.
func TestRDS_InstanceTakesItsFirstAutomatedBackupWithoutAClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: "postgres"}
	instanceID := "sdk-instance-first-backup"
	_, err := f.client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(instanceID),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("postgres"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		BackupRetentionPeriod: aws.Int32(1),
	})
	require.NoError(t, err)
	f.cleanup(instanceID)

	automatedSnapshots := &rds.DescribeDBSnapshotsInput{
		DBInstanceIdentifier: aws.String(instanceID), SnapshotType: aws.String("automated"),
	}
	awaitActiveBackup := func(why string) {
		t.Helper()
		require.NoError(t, rds.NewDBSnapshotAvailableWaiter(f.client, func(o *rds.DBSnapshotAvailableWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).Wait(ctx, automatedSnapshots, 3*time.Minute), why)
		require.Eventually(t, func() bool {
			listed, err := f.client.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{
				DBInstanceIdentifier: aws.String(instanceID),
			})
			return err == nil && len(listed.DBInstanceAutomatedBackups) == 1 &&
				aws.ToString(listed.DBInstanceAutomatedBackups[0].Status) == "active"
		}, time.Minute, waiterMinDelay, why)
		snapshots, err := f.client.DescribeDBSnapshots(ctx, automatedSnapshots)
		require.NoError(t, err)
		require.Len(t, snapshots.DBSnapshots, 1, why)
		assert.True(t, strings.HasPrefix(aws.ToString(snapshots.DBSnapshots[0].DBSnapshotIdentifier), "rds:"+instanceID+"-"))
		described := f.waitAvailable(instanceID)
		require.NotNil(t, described.LatestRestorableTime, "an instance with an automated backup reports LatestRestorableTime")
	}
	awaitActiveBackup("Amazon RDS takes the first automated backup when it creates the instance")

	_, err = f.client.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID), BackupRetentionPeriod: aws.Int32(0), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	require.NoError(t, rds.NewDBSnapshotDeletedWaiter(f.client, func(o *rds.DBSnapshotDeletedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, automatedSnapshots, 3*time.Minute), "turning automated backups off deletes the automated snapshots")
	listed, err := f.client.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{
		DBInstanceIdentifier: aws.String(instanceID),
	})
	require.NoError(t, err)
	assert.Empty(t, listed.DBInstanceAutomatedBackups, "an instance without backup retention keeps no automated backup")

	_, err = f.client.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID), BackupRetentionPeriod: aws.Int32(1), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	awaitActiveBackup("turning automated backups back on takes an automated backup")
}

// RestoreDBInstanceFromS3 imports a Percona XtraBackup of a MySQL 8.0 server
// from Amazon S3 into a new RDS for MySQL instance, which serves the backup's
// data to the master user the request names. A bucket the ingestion role
// cannot read is refused.
func TestRDS_InstanceRestoresFromS3XtraBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	backup := takeXtraBackup(t, "sdk-instance-xtrabackup-source",
		"CREATE DATABASE shop",
		"CREATE TABLE shop.orders (id INT PRIMARY KEY, item VARCHAR(32) NOT NULL)",
		"INSERT INTO shop.orders VALUES (1, 'kettle'), (2, 'teapot')")

	bucket := "sdk-instance-s3-import"
	s3c := s3Client()
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("backups/shop.xbstream"), Body: bytes.NewReader(backup),
	})
	require.NoError(t, err)

	iamc := iamClient()
	roleName := uniqueName("rds-instance-s3-import")
	role, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"rds.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(roleName), PolicyName: aws.String("list"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::` + bucket + `"}]}`),
	})
	require.NoError(t, err)

	f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: "mysql"}
	request := func(instanceID string) *rds.RestoreDBInstanceFromS3Input {
		return &rds.RestoreDBInstanceFromS3Input{
			DBInstanceIdentifier: aws.String(instanceID),
			DBInstanceClass:      aws.String("db.t3.micro"),
			Engine:               aws.String("mysql"),
			AllocatedStorage:     aws.Int32(20),
			MasterUsername:       aws.String(restoreSourceUsername),
			MasterUserPassword:   aws.String(restoreSourcePassword),
			DBName:               aws.String(restoreSourceDatabase),
			SourceEngine:         aws.String("mysql"),
			SourceEngineVersion:  aws.String("8.0.40"),
			S3BucketName:         aws.String(bucket),
			S3Prefix:             aws.String("backups/"),
			S3IngestionRoleArn:   role.Role.Arn,
		}
	}
	_, err = f.client.RestoreDBInstanceFromS3(ctx, request("sdk-instance-s3-import-denied"))
	assertAWSAPIErrorCode(t, err, "InvalidS3BucketFault")

	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(roleName), PolicyName: aws.String("read"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`),
	})
	require.NoError(t, err)
	instanceID := "sdk-instance-s3-import"
	restored, err := f.client.RestoreDBInstanceFromS3(ctx, request(instanceID))
	require.NoError(t, err)
	f.cleanup(instanceID)
	assert.Equal(t, "creating", aws.ToString(restored.DBInstance.DBInstanceStatus))
	database := f.connect(instanceID).(auroraMySQL)
	rows, err := database.db.QueryContext(ctx, `SELECT item FROM shop.orders ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var items []string
	for rows.Next() {
		var item string
		require.NoError(t, rows.Scan(&item))
		items = append(items, item)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"kettle", "teapot"}, items, "the instance serves the backup's data")
	database.exec(t, `INSERT INTO shop.orders VALUES (3, 'cosy')`)
}

// DeleteDBInstance and DeleteDBCluster with DeleteAutomatedBackups=false keep
// the resource's automated backup as retained, with a restore window that
// ends at the deletion. RestoreDBInstanceToPointInTime restores the deleted
// instance through SourceDbiResourceId or SourceDBInstanceAutomatedBackupsArn,
// and RestoreDBClusterToPointInTime the deleted cluster through
// SourceDbClusterResourceId, each holding the rows committed by the restore
// time. A deleted retained backup no longer restores.
func TestRDS_RetainedAutomatedBackupsRestoreDeletedResources(t *testing.T) {
	t.Run("db-instance", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: "postgres"}
		sourceID := "sdk-retained-instance"
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
		resourceID := created.DBInstance.DbiResourceId
		t.Cleanup(func() {
			_, _ = f.client.DeleteDBInstanceAutomatedBackup(context.Background(), &rds.DeleteDBInstanceAutomatedBackupInput{DbiResourceId: resourceID})
		})
		source := f.connect(sourceID)
		source.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
		source.exec(t, `INSERT INTO ledger VALUES ('before-restore-time')`)
		restoreTo := nextMillisecondThenWait(t, ctx, source)
		source.exec(t, `INSERT INTO ledger VALUES ('after-restore-time')`)

		_, err = f.client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier:   aws.String(sourceID),
			SkipFinalSnapshot:      aws.Bool(true),
			DeleteAutomatedBackups: aws.Bool(false),
		})
		require.NoError(t, err)
		require.NoError(t, rds.NewDBInstanceDeletedWaiter(f.client, func(o *rds.DBInstanceDeletedWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(sourceID)}, 3*time.Minute))

		listed, err := f.client.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{
			Filters: []types.Filter{{Name: aws.String("status"), Values: []string{"retained"}}, {Name: aws.String("dbi-resource-id"), Values: []string{aws.ToString(resourceID)}}},
		})
		require.NoError(t, err)
		require.Len(t, listed.DBInstanceAutomatedBackups, 1)
		retained := listed.DBInstanceAutomatedBackups[0]
		assert.Equal(t, sourceID, aws.ToString(retained.DBInstanceIdentifier))
		require.NotNil(t, retained.RestoreWindow)
		assert.False(t, restoreTo.Before(*retained.RestoreWindow.EarliestTime), "the restore time lies in the retained window")
		assert.False(t, restoreTo.After(*retained.RestoreWindow.LatestTime), "the retained window ends at the deletion")

		atTimeID := "sdk-retained-instance-at-time"
		_, err = f.client.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
			SourceDbiResourceId:        resourceID,
			TargetDBInstanceIdentifier: aws.String(atTimeID),
			RestoreTime:                aws.Time(restoreTo),
		})
		require.NoError(t, err)
		f.cleanup(atTimeID)
		assert.Equal(t, []string{"before-restore-time"}, f.connect(atTimeID).entries(t),
			"the instance restored from the retained backup holds the rows committed by RestoreTime")

		latestID := "sdk-retained-instance-latest"
		_, err = f.client.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
			SourceDBInstanceAutomatedBackupsArn: retained.DBInstanceAutomatedBackupsArn,
			TargetDBInstanceIdentifier:          aws.String(latestID),
			UseLatestRestorableTime:             aws.Bool(true),
		})
		require.NoError(t, err)
		f.cleanup(latestID)
		assert.Equal(t, []string{"after-restore-time", "before-restore-time"}, f.connect(latestID).entries(t),
			"a restore to the latest restorable time holds every row committed before the deletion")

		_, err = f.client.DeleteDBInstanceAutomatedBackup(ctx, &rds.DeleteDBInstanceAutomatedBackupInput{DbiResourceId: resourceID})
		require.NoError(t, err)
		_, err = f.client.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
			SourceDbiResourceId:        resourceID,
			TargetDBInstanceIdentifier: aws.String("sdk-retained-instance-gone"),
			UseLatestRestorableTime:    aws.Bool(true),
		})
		assertAWSAPIErrorCode(t, err, "DBInstanceAutomatedBackupNotFound")
	})

	t.Run("aurora-mysql", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		f := auroraClusterFixture{t: t, ctx: ctx, client: rdsClient(), engine: "aurora-mysql", family: "mysql"}
		sourceID := "sdk-retained-cluster"
		created, err := f.client.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
			DBClusterIdentifier: aws.String(sourceID),
			Engine:              aws.String(f.engine),
			MasterUsername:      aws.String(restoreSourceUsername),
			MasterUserPassword:  aws.String(restoreSourcePassword),
			DatabaseName:        aws.String(restoreSourceDatabase),
		})
		require.NoError(t, err)
		f.cleanupCluster(sourceID)
		resourceID := created.DBCluster.DbClusterResourceId
		t.Cleanup(func() {
			_, _ = f.client.DeleteDBClusterAutomatedBackup(context.Background(), &rds.DeleteDBClusterAutomatedBackupInput{DbClusterResourceId: resourceID})
		})
		f.addWriter(sourceID)
		source := f.connect(sourceID)
		source.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
		source.exec(t, `INSERT INTO ledger VALUES ('before-restore-time')`)
		restoreTo := nextMillisecondThenWait(t, ctx, source)
		source.exec(t, `INSERT INTO ledger VALUES ('after-restore-time')`)

		_, err = f.client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(sourceID + "-1"), SkipFinalSnapshot: aws.Bool(true),
		})
		require.NoError(t, err)
		require.NoError(t, rds.NewDBInstanceDeletedWaiter(f.client, func(o *rds.DBInstanceDeletedWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(sourceID + "-1")}, 3*time.Minute))
		_, err = f.client.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier:    aws.String(sourceID),
			SkipFinalSnapshot:      aws.Bool(true),
			DeleteAutomatedBackups: aws.Bool(false),
		})
		require.NoError(t, err)
		require.NoError(t, rds.NewDBClusterDeletedWaiter(f.client, func(o *rds.DBClusterDeletedWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).Wait(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(sourceID)}, 3*time.Minute))

		listed, err := f.client.DescribeDBClusterAutomatedBackups(ctx, &rds.DescribeDBClusterAutomatedBackupsInput{
			DbClusterResourceId: resourceID,
		})
		require.NoError(t, err)
		require.Len(t, listed.DBClusterAutomatedBackups, 1)
		retained := listed.DBClusterAutomatedBackups[0]
		assert.Equal(t, "retained", aws.ToString(retained.Status))
		require.NotNil(t, retained.RestoreWindow)
		assert.False(t, restoreTo.After(*retained.RestoreWindow.LatestTime), "the retained window ends at the deletion")

		restoredID := "sdk-retained-cluster-restored"
		_, err = f.client.RestoreDBClusterToPointInTime(ctx, &rds.RestoreDBClusterToPointInTimeInput{
			DBClusterIdentifier:       aws.String(restoredID),
			SourceDbClusterResourceId: resourceID,
			RestoreToTime:             aws.Time(restoreTo),
		})
		require.NoError(t, err)
		f.cleanupCluster(restoredID)
		waitForRDSClusterAvailable(t, f.client, ctx, restoredID)
		f.addWriter(restoredID)
		assert.Equal(t, []string{"before-restore-time"}, f.connect(restoredID).entries(t),
			"the cluster restored from the retained backup holds the rows committed by RestoreToTime")

		_, err = f.client.DeleteDBClusterAutomatedBackup(ctx, &rds.DeleteDBClusterAutomatedBackupInput{DbClusterResourceId: resourceID})
		require.NoError(t, err)
		_, err = f.client.RestoreDBClusterToPointInTime(ctx, &rds.RestoreDBClusterToPointInTimeInput{
			DBClusterIdentifier:       aws.String("sdk-retained-cluster-gone"),
			SourceDbClusterResourceId: resourceID,
			UseLatestRestorableTime:   aws.Bool(true),
		})
		assertAWSAPIErrorCode(t, err, "DBClusterAutomatedBackupNotFoundFault")
	})
}
