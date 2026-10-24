package aws_cli_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aws rds restore-db-instance-to-point-in-time --region us-west-2 from a
// replicated automated backup creates the DB instance in us-west-2: its ARN
// and Availability Zone name us-west-2, describe-db-instances there lists it,
// and describe-db-instances in us-east-1 does not.
func TestRDSCLI_ReplicatedBackupRestoresInTheDestinationRegion(t *testing.T) {
	t.Parallel()
	sourceID := cliUniqueName("cli-region-src")
	out := runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", sourceID,
		"--engine", "postgres",
		"--db-instance-class", "db.t3.micro",
		"--allocated-storage", "20",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--backup-retention-period", "1"))
	var created struct {
		DBInstance struct {
			DBInstanceArn string `json:"DBInstanceArn"`
		} `json:"DBInstance"`
	}
	parseJSON(t, out, &created)
	t.Cleanup(func() {
		_ = awsCLI("rds", "stop-db-instance-automated-backups-replication",
			"--region", "us-west-2", "--source-db-instance-arn", created.DBInstance.DBInstanceArn).Run()
		_ = awsCLI("rds", "delete-db-instance", "--db-instance-identifier", sourceID, "--skip-final-snapshot").Run()
	})
	cliWaitDBInstanceAvailable(t, sourceID)

	var started struct {
		DBInstanceAutomatedBackup struct {
			DBInstanceAutomatedBackupsArn string `json:"DBInstanceAutomatedBackupsArn"`
		} `json:"DBInstanceAutomatedBackup"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "start-db-instance-automated-backups-replication",
		"--region", "us-west-2", "--source-db-instance-arn", created.DBInstance.DBInstanceArn)), &started)
	backupARN := started.DBInstanceAutomatedBackup.DBInstanceAutomatedBackupsArn
	// Amazon RDS offers no waiter for an automated backup; its Status is what
	// the console and the CLI poll.
	require.Eventually(t, func() bool {
		var listed struct {
			DBInstanceAutomatedBackups []struct {
				Status string `json:"Status"`
			} `json:"DBInstanceAutomatedBackups"`
		}
		parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instance-automated-backups",
			"--region", "us-west-2", "--db-instance-automated-backups-arn", backupARN)), &listed)
		return len(listed.DBInstanceAutomatedBackups) == 1 && listed.DBInstanceAutomatedBackups[0].Status == "replicating"
	}, 2*time.Minute, time.Second)

	restoredID := cliUniqueName("cli-region-restored")
	var restored struct {
		DBInstance struct {
			DBInstanceArn    string `json:"DBInstanceArn"`
			AvailabilityZone string `json:"AvailabilityZone"`
		} `json:"DBInstance"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "restore-db-instance-to-point-in-time",
		"--region", "us-west-2",
		"--source-db-instance-automated-backups-arn", backupARN,
		"--target-db-instance-identifier", restoredID,
		"--use-latest-restorable-time")), &restored)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance", "--region", "us-west-2",
			"--db-instance-identifier", restoredID, "--skip-final-snapshot").Run()
	})
	assert.Equal(t, "arn:aws:rds:us-west-2:123456789012:db:"+restoredID, restored.DBInstance.DBInstanceArn)
	assert.Equal(t, "us-west-2a", restored.DBInstance.AvailabilityZone)

	runCLI(t, awsCLI("rds", "wait", "db-instance-available", "--region", "us-west-2", "--db-instance-identifier", restoredID))
	var inDestination struct {
		DBInstances []struct {
			DBInstanceArn string `json:"DBInstanceArn"`
		} `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--region", "us-west-2",
		"--db-instance-identifier", restoredID)), &inDestination)
	require.Len(t, inDestination.DBInstances, 1)
	assert.Equal(t, restored.DBInstance.DBInstanceArn, inDestination.DBInstances[0].DBInstanceArn)
	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", restoredID)),
		"DBInstanceNotFound")
}
