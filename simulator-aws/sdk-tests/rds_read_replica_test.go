package aws_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsReplicationStatus is the read replication status DescribeDBInstances
// reports for a replica, or nil.
func rdsReplicationStatus(instance types.DBInstance) *types.DBInstanceStatusInfo {
	for i := range instance.StatusInfos {
		if aws.ToString(instance.StatusInfos[i].StatusType) == "read replication" {
			return &instance.StatusInfos[i]
		}
	}
	return nil
}

// rdsCreateReplicationSource creates an instance with automated backups, which
// a read replica's source needs, holding a ledger with one row.
func rdsCreateReplicationSource(f rdsInstanceFixture, prefix, engine string) (string, auroraSnapshotClient) {
	f.t.Helper()
	sourceID := uniqueName(prefix + "-" + engine)
	_, err := f.client.CreateDBInstance(f.ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(sourceID),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String(engine),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		DBName:                aws.String(restoreSourceDatabase),
		BackupRetentionPeriod: aws.Int32(1),
	})
	require.NoError(f.t, err)
	f.cleanup(sourceID)
	source := f.connect(sourceID)
	source.exec(f.t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
	source.exec(f.t, `INSERT INTO ledger VALUES ('before-replica')`)
	return sourceID, source
}

// A read replica runs an engine of its own holding its source's data, serves
// it read-only, and replicates every later write on the source; it reports
// its read replication status and its ReplicaLag metric. Its source cannot be
// stopped while it replicates. PromoteReadReplica ends the replication and
// reopens the replica as a standalone instance that accepts writes.
func TestRDS_ReadReplicaReplicatesItsSource(t *testing.T) {
	for _, engine := range []string{"mysql", "postgres", "mariadb"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: engine}
			c := f.client
			sourceID, source := rdsCreateReplicationSource(f, "sdk-rr-src", engine)

			replicaID := uniqueName("sdk-rr-" + engine)
			created, err := c.CreateDBInstanceReadReplica(ctx, &rds.CreateDBInstanceReadReplicaInput{
				DBInstanceIdentifier:       aws.String(replicaID),
				SourceDBInstanceIdentifier: aws.String(sourceID),
			})
			require.NoError(t, err)
			f.cleanup(replicaID)
			assert.Equal(t, "creating", aws.ToString(created.DBInstance.DBInstanceStatus))
			assert.Equal(t, sourceID, aws.ToString(created.DBInstance.ReadReplicaSourceDBInstanceIdentifier))

			replica := f.waitAvailable(replicaID)
			status := rdsReplicationStatus(replica)
			require.NotNil(t, status, "a replica reports its read replication status")
			assert.Equal(t, "replicating", aws.ToString(status.Status))
			assert.True(t, aws.ToBool(status.Normal))
			assert.Contains(t, rdsDescribeInstance(t, ctx, c, sourceID).ReadReplicaDBInstanceIdentifiers, replicaID)

			replicaDB := f.connect(replicaID)
			assert.Equal(t, []string{"before-replica"}, replicaDB.entries(t), "the replica holds its source's data")
			assert.Error(t, rdsWriteFails(t, replicaDB, `INSERT INTO ledger VALUES ('written-to-replica')`),
				"the replica serves its sessions read-only")
			source.exec(t, `INSERT INTO ledger VALUES ('after-replica')`)
			require.Eventually(t, func() bool {
				entries := replicaDB.entries(t)
				return len(entries) == 2 && entries[0] == "after-replica"
			}, 2*time.Minute, 100*time.Millisecond, "the replica applies the source's later writes")

			lag, err := cloudwatchClient().GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
				Namespace:  aws.String("AWS/RDS"),
				MetricName: aws.String("ReplicaLag"),
				Dimensions: []cwtypes.Dimension{{Name: aws.String("DBInstanceIdentifier"), Value: aws.String(replicaID)}},
				StartTime:  aws.Time(time.Now().Add(-10 * time.Minute)),
				EndTime:    aws.Time(time.Now().Add(time.Minute)),
				Period:     aws.Int32(60),
				Statistics: []cwtypes.Statistic{cwtypes.StatisticMinimum},
			})
			require.NoError(t, err)
			require.NotEmpty(t, lag.Datapoints, "the replica publishes ReplicaLag")
			assert.GreaterOrEqual(t, aws.ToFloat64(lag.Datapoints[0].Minimum), float64(0))

			_, err = c.StopDBInstance(ctx, &rds.StopDBInstanceInput{DBInstanceIdentifier: aws.String(sourceID)})
			assertAWSAPIErrorCode(t, err, "InvalidDBInstanceState")

			promoted, err := c.PromoteReadReplica(ctx, &rds.PromoteReadReplicaInput{DBInstanceIdentifier: aws.String(replicaID)})
			require.NoError(t, err)
			assert.Equal(t, "modifying", aws.ToString(promoted.DBInstance.DBInstanceStatus))
			standalone := f.waitAvailable(replicaID)
			assert.Empty(t, aws.ToString(standalone.ReadReplicaSourceDBInstanceIdentifier))
			assert.Nil(t, rdsReplicationStatus(standalone))
			assert.Equal(t, int32(1), aws.ToInt32(standalone.BackupRetentionPeriod))
			assert.NotContains(t, rdsDescribeInstance(t, ctx, c, sourceID).ReadReplicaDBInstanceIdentifiers, replicaID)
			promotedDB := f.connect(replicaID)
			promotedDB.exec(t, `INSERT INTO ledger VALUES ('written-to-promoted')`)
			assert.Equal(t, []string{"after-replica", "before-replica", "written-to-promoted"}, promotedDB.entries(t))
		})
	}
}

// Deleting a read replica's source promotes the replica to a standalone
// instance, and a source without automated backups takes no replica.
func TestRDS_DeletingASourcePromotesItsReadReplica(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: "postgres"}
	c := f.client

	unbackedID := uniqueName("sdk-rr-nobackup")
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(unbackedID),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("postgres"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		BackupRetentionPeriod: aws.Int32(0),
	})
	require.NoError(t, err)
	f.cleanup(unbackedID)
	f.waitAvailable(unbackedID)
	_, err = c.CreateDBInstanceReadReplica(ctx, &rds.CreateDBInstanceReadReplicaInput{
		DBInstanceIdentifier:       aws.String(uniqueName("sdk-rr-refused")),
		SourceDBInstanceIdentifier: aws.String(unbackedID),
	})
	assertAWSAPIErrorCode(t, err, "InvalidDBInstanceState")

	sourceID, _ := rdsCreateReplicationSource(f, "sdk-rr-gone", "postgres")
	replicaID := uniqueName("sdk-rr-orphan")
	_, err = c.CreateDBInstanceReadReplica(ctx, &rds.CreateDBInstanceReadReplicaInput{
		DBInstanceIdentifier:       aws.String(replicaID),
		SourceDBInstanceIdentifier: aws.String(sourceID),
	})
	require.NoError(t, err)
	f.cleanup(replicaID)
	f.waitAvailable(replicaID)

	_, err = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(sourceID), SkipFinalSnapshot: aws.Bool(true)})
	require.NoError(t, err)
	require.NoError(t, rds.NewDBInstanceDeletedWaiter(c, func(o *rds.DBInstanceDeletedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(sourceID)}, 5*time.Minute))
	require.Eventually(t, func() bool {
		replica := rdsDescribeInstance(t, ctx, c, replicaID)
		return aws.ToString(replica.ReadReplicaSourceDBInstanceIdentifier) == "" && aws.ToString(replica.DBInstanceStatus) == "available"
	}, 5*time.Minute, 250*time.Millisecond, "the replica of a deleted source becomes a standalone instance")
	promoted := f.connect(replicaID)
	promoted.exec(t, `INSERT INTO ledger VALUES ('written-to-promoted')`)
	assert.Equal(t, []string{"before-replica", "written-to-promoted"}, promoted.entries(t))
}
