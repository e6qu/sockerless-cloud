package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsAccountQuotaUsage indexes a DescribeAccountAttributes response by quota
// name, returning the used and max values per quota.
func rdsAccountQuotaUsage(t *testing.T, out *rds.DescribeAccountAttributesOutput) map[string][2]int64 {
	t.Helper()
	byName := map[string][2]int64{}
	for _, q := range out.AccountQuotas {
		name := aws.ToString(q.AccountQuotaName)
		require.NotEmpty(t, name, "AccountQuota entry without AccountQuotaName")
		byName[name] = [2]int64{aws.ToInt64(q.Used), aws.ToInt64(q.Max)}
	}
	return byName
}

func rdsAccountQuotas(t *testing.T, c *rds.Client) map[string][2]int64 {
	t.Helper()
	out, err := c.DescribeAccountAttributes(ctx, &rds.DescribeAccountAttributesInput{})
	require.NoError(t, err)
	return rdsAccountQuotaUsage(t, out)
}

// TestRDS_DescribeAccountAttributes covers DescribeAccountAttributes: it takes
// no parameters and reports each Amazon RDS account quota with the account's
// current usage. The usage must track the account's real resources, so the test
// measures a baseline, reserves capacity, creates a DB instance in it and a
// subnet group, and asserts the exact deltas the new resources produce. The
// DBInstances quota counts the DB instances and the unused capacity of the
// reservations, and a reservation that would exceed it is refused.
//
// The Terraform AWS provider exposes no resource or data source that reads RDS
// account quotas, so this operation's client coverage is SDK + CLI
// (cli-tests/rds_account_attributes_test.go).
func TestRDS_DescribeAccountAttributes(t *testing.T) {
	c := rdsClient()
	base := rdsAccountQuotas(t, c)

	// Every quota Amazon RDS documents for an account must be present, with a
	// nonzero maximum.
	for _, name := range []string{
		"DBInstances", "ReservedDBInstances", "AllocatedStorage", "DBSecurityGroups",
		"AuthorizationsPerDBSecurityGroup", "DBParameterGroups", "ManualSnapshots",
		"EventSubscriptions", "DBSubnetGroups", "OptionGroups", "SubnetsPerDBSubnetGroup",
		"ReadReplicasPerMaster", "DBClusters", "DBClusterParameterGroups", "DBClusterRoles",
		"DBInstanceRoles", "ManualClusterSnapshots", "CustomEndpointsPerDBCluster",
	} {
		q, ok := base[name]
		require.True(t, ok, "DescribeAccountAttributes omitted the %s quota", name)
		assert.Greater(t, q[1], int64(0), "%s quota reported a zero maximum", name)
		assert.GreaterOrEqual(t, q[1], q[0], "%s reports usage above its maximum", name)
	}
	assert.Equal(t, int64(40), base["DBInstances"][1])
	assert.Equal(t, int64(100000), base["AllocatedStorage"][1])

	// A reservation of two DB instances in each of two Availability Zones
	// counts four against the DBInstances quota while nothing uses it.
	reservation := "acct-attr-reservation"
	reserved, err := c.CreateDBCapacityReservation(ctx, &rds.CreateDBCapacityReservationInput{
		DBCapacityReservationName:   aws.String(reservation),
		DBInstanceClass:             aws.String("db.t3.micro"),
		Engine:                      aws.String("postgres"),
		CapacityPerAvailabilityZone: aws.Int32(2),
		AvailabilityZones:           []string{"us-east-1a", "us-east-1b"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.CancelDBCapacityReservation(ctx, &rds.CancelDBCapacityReservationInput{
			DBCapacityReservationIdentifier: reserved.DBCapacityReservation.DBCapacityReservationIdentifier})
	})
	assert.Equal(t, base["DBInstances"][0]+4, rdsAccountQuotas(t, c)["DBInstances"][0],
		"DBInstances usage must count the unused capacity of the reservation")

	instID := "acct-attr-pg-db"
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:            aws.String(instID),
		Engine:                          aws.String("postgres"),
		DBInstanceClass:                 aws.String("db.t3.micro"),
		AllocatedStorage:                aws.Int32(35),
		MasterUsername:                  aws.String("admin"),
		MasterUserPassword:              aws.String("password123!"),
		DBCapacityReservationIdentifier: aws.String(reservation),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID), SkipFinalSnapshot: aws.Bool(true)})
	})

	subnetGroup := "acct-attr-subnets"
	_, subnets := createECSTestVPCSubnets(t, subnetGroup, 3)
	_, err = c.CreateDBSubnetGroup(ctx, &rds.CreateDBSubnetGroupInput{
		DBSubnetGroupName:        aws.String(subnetGroup),
		DBSubnetGroupDescription: aws.String("account attribute quota check"),
		SubnetIds:                subnets,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSubnetGroup(ctx, &rds.DeleteDBSubnetGroupInput{
			DBSubnetGroupName: aws.String(subnetGroup)})
	})

	now := rdsAccountQuotas(t, c)
	assert.Equal(t, base["DBInstances"][0]+4, now["DBInstances"][0],
		"a DB instance that takes a unit of reserved capacity leaves DBInstances usage where it was")
	assert.Equal(t, base["AllocatedStorage"][0]+35, now["AllocatedStorage"][0],
		"AllocatedStorage usage must add the new instance's allocated storage")
	assert.Equal(t, base["DBSubnetGroups"][0]+1, now["DBSubnetGroups"][0],
		"DBSubnetGroups usage must count the new subnet group")
	assert.GreaterOrEqual(t, now["SubnetsPerDBSubnetGroup"][0], int64(3),
		"SubnetsPerDBSubnetGroup reports the highest subnet count of any subnet group")

	// A reservation whose capacity would exceed the remaining quota is refused,
	// and so is an increase of an existing reservation past it; a decrease is
	// not checked.
	remaining := now["DBInstances"][1] - now["DBInstances"][0]
	_, err = c.CreateDBCapacityReservation(ctx, &rds.CreateDBCapacityReservationInput{
		DBCapacityReservationName:   aws.String("acct-attr-over-quota"),
		DBInstanceClass:             aws.String("db.t3.micro"),
		Engine:                      aws.String("postgres"),
		CapacityPerAvailabilityZone: aws.Int32(int32(remaining) + 1),
		AvailabilityZones:           []string{"us-east-1c"},
	})
	assertAWSAPIErrorCode(t, err, "InstanceQuotaExceeded")
	_, err = c.ModifyDBCapacityReservation(ctx, &rds.ModifyDBCapacityReservationInput{
		DBCapacityReservationIdentifier: aws.String(reservation),
		CapacityPerAvailabilityZone:     aws.Int32(2 + int32(remaining)),
	})
	assertAWSAPIErrorCode(t, err, "InstanceQuotaExceeded")
	_, err = c.ModifyDBCapacityReservation(ctx, &rds.ModifyDBCapacityReservationInput{
		DBCapacityReservationIdentifier: aws.String(reservation),
		CapacityPerAvailabilityZone:     aws.Int32(1),
	})
	require.NoError(t, err)
	assert.Equal(t, base["DBInstances"][0]+2, rdsAccountQuotas(t, c)["DBInstances"][0],
		"one unit of the reservation's two in each zone remains unused after the decrease")

	_, err = c.CancelDBCapacityReservation(ctx, &rds.CancelDBCapacityReservationInput{DBCapacityReservationIdentifier: aws.String(reservation)})
	require.NoError(t, err)
	assert.Equal(t, base["DBInstances"][0]+1, rdsAccountQuotas(t, c)["DBInstances"][0],
		"a cancelled reservation no longer counts; the DB instance does")
}
