package aws_cli_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsAccountAttributes runs `aws rds describe-account-attributes` and indexes
// the reported quotas by name, returning used and max per quota.
func rdsAccountAttributes(t *testing.T) map[string][2]int64 {
	t.Helper()
	out := runCLI(t, awsCLI("rds", "describe-account-attributes"))
	var resp struct {
		AccountQuotas []struct {
			AccountQuotaName string `json:"AccountQuotaName"`
			Used             int64  `json:"Used"`
			Max              int64  `json:"Max"`
		} `json:"AccountQuotas"`
	}
	parseJSON(t, out, &resp)
	require.NotEmpty(t, resp.AccountQuotas, "describe-account-attributes returned no quotas")
	byName := map[string][2]int64{}
	for _, q := range resp.AccountQuotas {
		byName[q.AccountQuotaName] = [2]int64{q.Used, q.Max}
	}
	return byName
}

// TestRDSCLI_AccountAttributes drives DescribeAccountAttributes through the real
// aws CLI: the account quota report must list every documented Amazon RDS quota
// and track the account's real resources, so the test measures a baseline,
// creates a DB instance, and asserts the exact usage deltas.
func TestRDSCLI_AccountAttributes(t *testing.T) {
	base := rdsAccountAttributes(t)
	for _, name := range []string{
		"DBInstances", "ReservedDBInstances", "AllocatedStorage", "DBSecurityGroups",
		"AuthorizationsPerDBSecurityGroup", "DBParameterGroups", "ManualSnapshots",
		"EventSubscriptions", "DBSubnetGroups", "OptionGroups", "SubnetsPerDBSubnetGroup",
		"ReadReplicasPerMaster", "DBClusters", "DBClusterParameterGroups", "DBClusterRoles",
		"DBInstanceRoles", "ManualClusterSnapshots", "CustomEndpointsPerDBCluster",
	} {
		q, ok := base[name]
		require.True(t, ok, "describe-account-attributes omitted the %s quota", name)
		assert.Greater(t, q[1], int64(0), "%s quota reported a zero maximum", name)
	}
	assert.Equal(t, int64(40), base["DBInstances"][1])
	assert.Equal(t, int64(100000), base["AllocatedStorage"][1])

	// A reservation of one DB instance in each of two Availability Zones
	// counts two against the DBInstances quota while nothing uses it.
	reservation := "cli-acct-attr-reservation"
	t.Cleanup(func() {
		_ = awsCLI("rds", "cancel-db-capacity-reservation", "--db-capacity-reservation-identifier", reservation).Run()
	})
	runCLI(t, awsCLI("rds", "create-db-capacity-reservation",
		"--db-capacity-reservation-name", reservation,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--capacity-per-availability-zone", "1",
		"--availability-zones", "us-east-1a", "us-east-1b"))
	assert.Equal(t, base["DBInstances"][0]+2, rdsAccountAttributes(t)["DBInstances"][0],
		"DBInstances usage must count the unused capacity of the reservation")

	instID := "cli-acct-attr-db"
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", instID, "--skip-final-snapshot").Run()
	})
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instID,
		"--engine", "postgres",
		"--db-instance-class", "db.t3.micro",
		"--allocated-storage", "45",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--db-capacity-reservation-identifier", reservation))

	now := rdsAccountAttributes(t)
	assert.Equal(t, base["DBInstances"][0]+2, now["DBInstances"][0],
		"a DB instance that takes a unit of reserved capacity leaves DBInstances usage where it was")
	assert.Equal(t, base["AllocatedStorage"][0]+45, now["AllocatedStorage"][0],
		"AllocatedStorage usage must add the new instance's allocated storage")

	// A reservation whose capacity would exceed the remaining quota is refused.
	remaining := now["DBInstances"][1] - now["DBInstances"][0]
	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "create-db-capacity-reservation",
		"--db-capacity-reservation-name", "cli-acct-attr-over-quota",
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--capacity-per-availability-zone", strconv.FormatInt(remaining+1, 10),
		"--availability-zones", "us-east-1c")), "InstanceQuotaExceeded")

	runCLI(t, awsCLI("rds", "cancel-db-capacity-reservation", "--db-capacity-reservation-identifier", reservation))
	assert.Equal(t, base["DBInstances"][0]+1, rdsAccountAttributes(t)["DBInstances"][0],
		"a cancelled reservation no longer counts; the DB instance does")
}
