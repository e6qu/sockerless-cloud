package aws_cli_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliMultiAZInstance struct {
	DBInstanceStatus          string `json:"DBInstanceStatus"`
	MultiAZ                   bool   `json:"MultiAZ"`
	AvailabilityZone          string `json:"AvailabilityZone"`
	SecondaryAvailabilityZone string `json:"SecondaryAvailabilityZone"`
	Endpoint                  struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"Endpoint"`
}

func cliAvailableMultiAZInstance(t *testing.T, instanceID string) cliMultiAZInstance {
	t.Helper()
	cliWaitDBInstanceAvailable(t, instanceID)
	var described struct {
		DBInstances []cliMultiAZInstance `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", instanceID)), &described)
	require.Len(t, described.DBInstances, 1)
	return described.DBInstances[0]
}

// aws rds create-db-instance --multi-az runs an RDS for PostgreSQL instance
// with a synchronous standby in a second Availability Zone, and aws rds
// reboot-db-instance --force-failover promotes it: the endpoint then serves
// the rows committed before the failover, the Availability Zones swap, and
// aws rds describe-events lists the failover's start and completion.
func TestRDSCLI_MultiAZFailover(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	id := fmt.Sprintf("cli-maz-%d", time.Now().UnixNano())
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", id,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--multi-az"))
	cliCleanupDBInstance(t, id)
	created := cliAvailableMultiAZInstance(t, id)
	require.True(t, created.MultiAZ)
	require.NotEmpty(t, created.SecondaryAvailabilityZone, "a Multi-AZ instance reports its standby's Availability Zone")
	require.NotEqual(t, created.AvailabilityZone, created.SecondaryAvailabilityZone)

	primary := cliConnectPostgres(t, ctx, created.Endpoint.Address, created.Endpoint.Port)
	_, err := primary.Exec(ctx, `CREATE TABLE ledger (entry text NOT NULL); INSERT INTO ledger VALUES ('before-failover')`)
	require.NoError(t, err)

	var rebooted struct {
		DBInstance cliMultiAZInstance `json:"DBInstance"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "reboot-db-instance", "--db-instance-identifier", id, "--force-failover")), &rebooted)
	assert.Equal(t, "rebooting", rebooted.DBInstance.DBInstanceStatus)
	failedOver := cliAvailableMultiAZInstance(t, id)
	assert.Equal(t, created.SecondaryAvailabilityZone, failedOver.AvailabilityZone)
	assert.Equal(t, created.AvailabilityZone, failedOver.SecondaryAvailabilityZone)
	promoted := cliConnectPostgres(t, ctx, failedOver.Endpoint.Address, failedOver.Endpoint.Port)
	assert.Equal(t, []string{"before-failover"}, cliLedger(t, ctx, promoted), "the promoted standby holds every committed row")

	var events struct {
		Events []struct {
			SourceIdentifier string   `json:"SourceIdentifier"`
			Message          string   `json:"Message"`
			EventCategories  []string `json:"EventCategories"`
		} `json:"Events"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-events", "--source-type", "db-instance", "--source-identifier", id,
		"--event-categories", "failover")), &events)
	require.Len(t, events.Events, 2)
	assert.Equal(t, "A Multi-AZ failover that resulted in the promotion of a standby instance has started.", events.Events[0].Message)
	assert.Contains(t, events.Events[1].Message, "A Multi-AZ failover that resulted in the promotion of a standby instance is complete.")
}
