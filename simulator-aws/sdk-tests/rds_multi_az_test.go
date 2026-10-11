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

// rdsInstanceEventMessages lists the messages of a DB instance's events in
// category.
func rdsInstanceEventMessages(t *testing.T, ctx context.Context, c *rds.Client, instanceID, category string) []string {
	t.Helper()
	out, err := c.DescribeEvents(ctx, &rds.DescribeEventsInput{
		SourceIdentifier: aws.String(instanceID),
		SourceType:       types.SourceTypeDbInstance,
		EventCategories:  []string{category},
	})
	require.NoError(t, err)
	var messages []string
	for _, event := range out.Events {
		assert.Equal(t, instanceID, aws.ToString(event.SourceIdentifier))
		assert.Contains(t, event.EventCategories, category)
		messages = append(messages, aws.ToString(event.Message))
	}
	return messages
}

// A Multi-AZ DB instance keeps a synchronous standby in another Availability
// Zone of the Region. RebootDBInstance with ForceFailover promotes it: the
// endpoint then serves every row committed before the failover, the primary
// and secondary Availability Zones swap, and a new standby built from the new
// primary takes the next failover. ForceFailover of a Single-AZ instance is
// refused, and ModifyDBInstance converts an instance either way.
func TestRDS_MultiAZFailoverPromotesTheStandby(t *testing.T) {
	for _, engine := range []string{"postgres", "mysql", "mariadb"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: engine}
			c := f.client
			id := uniqueName("sdk-maz-" + engine)
			_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
				DBInstanceIdentifier: aws.String(id),
				DBInstanceClass:      aws.String("db.t3.micro"),
				Engine:               aws.String(engine),
				AllocatedStorage:     aws.Int32(20),
				MasterUsername:       aws.String(restoreSourceUsername),
				MasterUserPassword:   aws.String(restoreSourcePassword),
				DBName:               aws.String(restoreSourceDatabase),
			})
			require.NoError(t, err)
			f.cleanup(id)
			single := f.waitAvailable(id)
			assert.False(t, aws.ToBool(single.MultiAZ))
			assert.Empty(t, aws.ToString(single.SecondaryAvailabilityZone))
			_, err = c.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{DBInstanceIdentifier: aws.String(id), ForceFailover: aws.Bool(true)})
			assertAWSAPIErrorCode(t, err, "InvalidDBInstanceState")

			converting, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
				DBInstanceIdentifier: aws.String(id), MultiAZ: aws.Bool(true), ApplyImmediately: aws.Bool(true),
			})
			require.NoError(t, err)
			assert.Equal(t, "modifying", aws.ToString(converting.DBInstance.DBInstanceStatus))
			multiAZ := f.waitAvailable(id)
			require.True(t, aws.ToBool(multiAZ.MultiAZ))
			primaryZone, standbyZone := aws.ToString(multiAZ.AvailabilityZone), aws.ToString(multiAZ.SecondaryAvailabilityZone)
			require.NotEmpty(t, standbyZone, "a Multi-AZ instance reports its standby's Availability Zone")
			assert.NotEqual(t, primaryZone, standbyZone)
			assert.True(t, strings.HasPrefix(standbyZone, "us-east-1"), "the standby runs in the instance's Region: %s", standbyZone)

			f.connect(id).exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
			f.connect(id).exec(t, `INSERT INTO ledger VALUES ('before-failover')`)

			rebooting, err := c.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{DBInstanceIdentifier: aws.String(id), ForceFailover: aws.Bool(true)})
			require.NoError(t, err)
			assert.Equal(t, "rebooting", aws.ToString(rebooting.DBInstance.DBInstanceStatus))
			failedOver := f.waitAvailable(id)
			assert.Equal(t, standbyZone, aws.ToString(failedOver.AvailabilityZone), "the promoted standby's zone is the primary's")
			assert.Equal(t, primaryZone, aws.ToString(failedOver.SecondaryAvailabilityZone), "the new standby runs in the old primary's zone")
			promoted := f.connect(id)
			assert.Equal(t, []string{"before-failover"}, promoted.entries(t), "the promoted standby holds every committed row")
			promoted.exec(t, `INSERT INTO ledger VALUES ('after-failover')`)

			_, err = c.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{DBInstanceIdentifier: aws.String(id), ForceFailover: aws.Bool(true)})
			require.NoError(t, err)
			failedBack := f.waitAvailable(id)
			assert.Equal(t, primaryZone, aws.ToString(failedBack.AvailabilityZone))
			assert.Equal(t, standbyZone, aws.ToString(failedBack.SecondaryAvailabilityZone))
			assert.Equal(t, []string{"after-failover", "before-failover"}, f.connect(id).entries(t),
				"the standby built from the promoted primary holds its rows")

			_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
				DBInstanceIdentifier: aws.String(id), MultiAZ: aws.Bool(false), ApplyImmediately: aws.Bool(true),
			})
			require.NoError(t, err)
			reverted := f.waitAvailable(id)
			assert.False(t, aws.ToBool(reverted.MultiAZ))
			assert.Empty(t, aws.ToString(reverted.SecondaryAvailabilityZone))
			f.connect(id).exec(t, `INSERT INTO ledger VALUES ('single-az')`)

			assert.Equal(t, []string{
				"A Multi-AZ failover that resulted in the promotion of a standby instance has started.",
				"A Multi-AZ failover that resulted in the promotion of a standby instance is complete. It may take several minutes for the DNS to transfer to the new primary DB instance.",
				"A Multi-AZ failover that resulted in the promotion of a standby instance has started.",
				"A Multi-AZ failover that resulted in the promotion of a standby instance is complete. It may take several minutes for the DNS to transfer to the new primary DB instance.",
			}, rdsInstanceEventMessages(t, ctx, c, id, "failover"))
			assert.Equal(t, []string{
				"Applying modification to convert to a Multi-AZ DB instance.",
				"Finished applying modification to convert to a Multi-AZ DB instance.",
				"Applying modification to convert to a standard (Single-AZ) DB instance.",
				"Finished applying modification to convert to a standard (Single-AZ) DB instance.",
			}, rdsInstanceEventMessages(t, ctx, c, id, "configuration change"))
		})
	}
}
