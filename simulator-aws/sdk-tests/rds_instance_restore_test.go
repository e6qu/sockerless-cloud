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
// its engine first serves, which cannot be deleted by hand, and an instance
// with no backup retention does not restore to a time.
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
			require.Len(t, snapshots.DBSnapshots, 1, "the engine's first start takes an automated snapshot")
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
