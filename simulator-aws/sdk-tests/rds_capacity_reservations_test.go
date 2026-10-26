package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsAvailableCapacity maps each Availability Zone of the reservation to the
// capacity it reports available.
func rdsAvailableCapacity(res *rdstypes.DBCapacityReservation) map[string]int32 {
	available := map[string]int32{}
	for _, zone := range res.AvailableCapacityPerAvailabilityZoneList {
		available[aws.ToString(zone.AvailabilityZone)] = aws.ToInt32(zone.AvailableCapacity)
	}
	return available
}

func describeRDSCapacityReservation(t *testing.T, c *rds.Client, id string) *rdstypes.DBCapacityReservation {
	t.Helper()
	out, err := c.DescribeDBCapacityReservations(ctx, &rds.DescribeDBCapacityReservationsInput{DBCapacityReservationIdentifier: aws.String(id)})
	require.NoError(t, err)
	require.Len(t, out.DBCapacityReservations, 1)
	return &out.DBCapacityReservations[0]
}

// TestRDS_DBCapacityReservations reserves RDS for Oracle capacity in two
// Availability Zones, launches a DB instance into it, and covers the capacity
// the instance takes, returns when it stops and takes again when it starts.
func TestRDS_DBCapacityReservations(t *testing.T) {
	t.Parallel()
	c := rdsClient()
	name := uniquename.New("sdk-dbcr")
	created, err := c.CreateDBCapacityReservation(ctx, &rds.CreateDBCapacityReservationInput{
		DBCapacityReservationName:   aws.String(name),
		DBInstanceClass:             aws.String("db.m5.large"),
		Engine:                      aws.String("oracle-ee"),
		CapacityPerAvailabilityZone: aws.Int32(1),
		AvailabilityZones:           []string{"us-east-1a", "us-east-1b"},
		Tags:                        []rdstypes.Tag{{Key: aws.String("team"), Value: aws.String("data")}},
	})
	require.NoError(t, err)
	res := created.DBCapacityReservation
	require.NotNil(t, res)
	id := aws.ToString(res.DBCapacityReservationIdentifier)
	assert.True(t, strings.HasPrefix(id, "dbcr-"), "identifier %s carries the dbcr- prefix", id)
	arn := aws.ToString(res.DBCapacityReservationArn)
	assert.True(t, strings.HasPrefix(arn, "arn:aws:rds:us-east-1:") && strings.HasSuffix(arn, ":db-capacity-reservation:"+id), "ARN %s", arn)
	assert.Equal(t, rdstypes.DBCapacityReservationStateActive, res.State)
	assert.Equal(t, "bring-your-own-license", aws.ToString(res.LicenseModel), "RDS for Oracle Enterprise Edition reserves bring-your-own-license")
	assert.Equal(t, int32(1), aws.ToInt32(res.ReservedCapacityPerAvailabilityZone))
	assert.Equal(t, map[string]int32{"us-east-1a": 1, "us-east-1b": 1}, rdsAvailableCapacity(res))
	assert.Nil(t, res.CommitmentInfo, "a reservation that starts at once carries no commitment")

	tags, err := c.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{ResourceName: res.DBCapacityReservationArn})
	require.NoError(t, err)
	require.Len(t, tags.TagList, 1)
	assert.Equal(t, "data", aws.ToString(tags.TagList[0].Value))

	_, err = c.CreateDBCapacityReservation(ctx, &rds.CreateDBCapacityReservationInput{
		DBCapacityReservationName:   aws.String(name),
		DBInstanceClass:             aws.String("db.m5.large"),
		Engine:                      aws.String("oracle-ee"),
		CapacityPerAvailabilityZone: aws.Int32(1),
		AvailabilityZones:           []string{"us-east-1a"},
	})
	assertAWSAPIErrorCode(t, err, "DBCapacityReservationAlreadyExists")

	instID := uniquename.New("sdk-dbcr-db")
	launched, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:            aws.String(instID),
		Engine:                          aws.String("oracle-ee"),
		DBInstanceClass:                 aws.String("db.m5.large"),
		AllocatedStorage:                aws.Int32(20),
		MasterUsername:                  aws.String("admin"),
		MasterUserPassword:              aws.String("password123!"),
		AvailabilityZone:                aws.String("us-east-1b"),
		DBCapacityReservationIdentifier: aws.String(name),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(instID), SkipFinalSnapshot: aws.Bool(true)})
	})
	assert.Equal(t, id, aws.ToString(launched.DBInstance.DBCapacityReservationIdentifier))
	assert.Equal(t, "us-east-1b", aws.ToString(launched.DBInstance.AvailabilityZone))
	assert.Equal(t, map[string]int32{"us-east-1a": 1, "us-east-1b": 0}, rdsAvailableCapacity(describeRDSCapacityReservation(t, c, id)))

	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:            aws.String(uniquename.New("sdk-dbcr-full")),
		Engine:                          aws.String("oracle-ee"),
		DBInstanceClass:                 aws.String("db.m5.large"),
		AllocatedStorage:                aws.Int32(20),
		MasterUsername:                  aws.String("admin"),
		MasterUserPassword:              aws.String("password123!"),
		AvailabilityZone:                aws.String("us-east-1b"),
		DBCapacityReservationIdentifier: aws.String(id),
	})
	assertAWSAPIErrorCode(t, err, "InsufficientDBInstanceCapacity")
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:            aws.String(uniquename.New("sdk-dbcr-class")),
		Engine:                          aws.String("oracle-ee"),
		DBInstanceClass:                 aws.String("db.r5.large"),
		AllocatedStorage:                aws.Int32(20),
		MasterUsername:                  aws.String("admin"),
		MasterUserPassword:              aws.String("password123!"),
		DBCapacityReservationIdentifier: aws.String(id),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")

	end := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
	modified, err := c.ModifyDBCapacityReservation(ctx, &rds.ModifyDBCapacityReservationInput{
		DBCapacityReservationIdentifier: aws.String(id),
		CapacityPerAvailabilityZone:     aws.Int32(2),
		EndDateType:                     rdstypes.EndDateTypeLimited,
		EndDate:                         aws.Time(end),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), aws.ToInt32(modified.DBCapacityReservation.ReservedCapacityPerAvailabilityZone))
	assert.True(t, end.Equal(aws.ToTime(modified.DBCapacityReservation.EndDate)), "EndDate %v, want %v", modified.DBCapacityReservation.EndDate, end)
	assert.Equal(t, map[string]int32{"us-east-1a": 2, "us-east-1b": 1}, rdsAvailableCapacity(modified.DBCapacityReservation))
	_, err = c.ModifyDBCapacityReservation(ctx, &rds.ModifyDBCapacityReservationInput{DBCapacityReservationIdentifier: aws.String(id)})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")

	var listed []string
	pages := rds.NewDescribeDBCapacityReservationsPaginator(c, &rds.DescribeDBCapacityReservationsInput{
		MaxRecords: aws.Int32(20),
		Filters: []rdstypes.Filter{
			{Name: aws.String("db-capacity-reservation-name"), Values: []string{name}},
			{Name: aws.String("state"), Values: []string{"active"}},
		},
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		require.NoError(t, err)
		for _, listedRes := range page.DBCapacityReservations {
			listed = append(listed, aws.ToString(listedRes.DBCapacityReservationIdentifier))
		}
	}
	assert.Equal(t, []string{id}, listed)

	// Each stop returns the instance's unit; the instance keeps its reservation and
	// takes a unit again on start.
	waitForRDSInstanceAvailable(t, c, ctx, instID)
	_, err = c.StopDBInstance(ctx, &rds.StopDBInstanceInput{DBInstanceIdentifier: aws.String(instID)})
	require.NoError(t, err)
	waitForRDSInstanceStatus(t, c, ctx, instID, "stopped")
	assert.Equal(t, int32(2), rdsAvailableCapacity(describeRDSCapacityReservation(t, c, id))["us-east-1b"])
	started, err := c.StartDBInstance(ctx, &rds.StartDBInstanceInput{DBInstanceIdentifier: aws.String(instID)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(started.DBInstance.DBCapacityReservationIdentifier))
	assert.Equal(t, int32(1), rdsAvailableCapacity(describeRDSCapacityReservation(t, c, id))["us-east-1b"])

	cancelled, err := c.CancelDBCapacityReservation(ctx, &rds.CancelDBCapacityReservationInput{DBCapacityReservationIdentifier: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, rdstypes.DBCapacityReservationStateCancelled, cancelled.DBCapacityReservation.State)
	assert.Equal(t, map[string]int32{"us-east-1a": 0, "us-east-1b": 0}, rdsAvailableCapacity(cancelled.DBCapacityReservation))
	_, err = c.CancelDBCapacityReservation(ctx, &rds.CancelDBCapacityReservationInput{DBCapacityReservationIdentifier: aws.String(id)})
	assertAWSAPIErrorCode(t, err, "InvalidDBCapacityReservationState")

	desc, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instID)})
	require.NoError(t, err)
	assert.Nil(t, desc.DBInstances[0].DBCapacityReservationIdentifier, "an instance of a cancelled reservation runs on unreserved capacity")

	_, err = c.DescribeDBCapacityReservations(ctx, &rds.DescribeDBCapacityReservationsInput{DBCapacityReservationIdentifier: aws.String("dbcr-00000000000000000000000000000000")})
	assertAWSAPIErrorCode(t, err, "DBCapacityReservationNotFound")
}

// TestRDS_FutureDatedDBCapacityReservation expects a later-starting reservation
// scheduled with its capacity commitment, unreachable by modification or
// cancellation before it starts.
func TestRDS_FutureDatedDBCapacityReservation(t *testing.T) {
	t.Parallel()
	c := rdsClient()
	start := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Second)
	created, err := c.CreateDBCapacityReservation(ctx, &rds.CreateDBCapacityReservationInput{
		DBCapacityReservationName:   aws.String(uniquename.New("sdk-dbcr-future")),
		DBInstanceClass:             aws.String("db.m7g.xlarge"),
		Engine:                      aws.String("postgres"),
		CapacityPerAvailabilityZone: aws.Int32(8),
		AvailabilityZones:           []string{"us-east-1c"},
		StartDate:                   aws.Time(start),
		CommitmentDuration:          aws.Int64(14 * 24 * 60 * 60),
	})
	require.NoError(t, err)
	res := created.DBCapacityReservation
	assert.Equal(t, rdstypes.DBCapacityReservationStateScheduled, res.State)
	assert.Equal(t, map[string]int32{"us-east-1c": 0}, rdsAvailableCapacity(res), "a reservation that hasn't started reports no available capacity")
	require.NotNil(t, res.CommitmentInfo)
	assert.Equal(t, int32(8), aws.ToInt32(res.CommitmentInfo.CommittedCapacityPerAvailabilityZone))
	assert.True(t, start.Add(14*24*time.Hour).Equal(aws.ToTime(res.CommitmentInfo.CommitmentEndDate)))

	_, err = c.ModifyDBCapacityReservation(ctx, &rds.ModifyDBCapacityReservationInput{
		DBCapacityReservationIdentifier: res.DBCapacityReservationIdentifier,
		CapacityPerAvailabilityZone:     aws.Int32(9),
	})
	assertAWSAPIErrorCode(t, err, "InvalidDBCapacityReservationState")
	_, err = c.CancelDBCapacityReservation(ctx, &rds.CancelDBCapacityReservationInput{DBCapacityReservationIdentifier: res.DBCapacityReservationIdentifier})
	assertAWSAPIErrorCode(t, err, "InvalidDBCapacityReservationState")

	_, err = c.CreateDBCapacityReservation(ctx, &rds.CreateDBCapacityReservationInput{
		DBCapacityReservationName:   aws.String(uniquename.New("sdk-dbcr-small")),
		DBInstanceClass:             aws.String("db.m7g.xlarge"),
		Engine:                      aws.String("postgres"),
		CapacityPerAvailabilityZone: aws.Int32(7),
		AvailabilityZones:           []string{"us-east-1c"},
		StartDate:                   aws.Time(start),
		CommitmentDuration:          aws.Int64(14 * 24 * 60 * 60),
	})
	assertAWSAPIErrorCode(t, err, "DBCapacityReservationUnsupported")
}
