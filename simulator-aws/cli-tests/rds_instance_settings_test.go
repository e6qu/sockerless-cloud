package aws_cli_test

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliInstanceSettings struct {
	DBInstanceIdentifier    string `json:"DBInstanceIdentifier"`
	DBInstanceArn           string `json:"DBInstanceArn"`
	DBInstanceStatus        string `json:"DBInstanceStatus"`
	MultiAZ                 bool   `json:"MultiAZ"`
	StorageType             string `json:"StorageType"`
	Iops                    int    `json:"Iops"`
	StorageThroughput       int    `json:"StorageThroughput"`
	CACertificateIdentifier string `json:"CACertificateIdentifier"`
	LicenseModel            string `json:"LicenseModel"`
	DBSubnetGroup           struct {
		DBSubnetGroupName string `json:"DBSubnetGroupName"`
	} `json:"DBSubnetGroup"`
	OptionGroupMemberships []struct {
		OptionGroupName string `json:"OptionGroupName"`
		Status          string `json:"Status"`
	} `json:"OptionGroupMemberships"`
	PendingModifiedValues struct {
		MultiAZ              *bool  `json:"MultiAZ"`
		StorageType          string `json:"StorageType"`
		Iops                 int    `json:"Iops"`
		StorageThroughput    int    `json:"StorageThroughput"`
		DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
		Port                 int    `json:"Port"`
	} `json:"PendingModifiedValues"`
	Endpoint struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"Endpoint"`
}

func cliDescribeInstanceSettings(t *testing.T, instanceID string) cliInstanceSettings {
	t.Helper()
	var described struct {
		DBInstances []cliInstanceSettings `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", instanceID)), &described)
	require.Len(t, described.DBInstances, 1)
	return described.DBInstances[0]
}

// create-db-instance takes the storage type, Provisioned IOPS, DB subnet group,
// option group and CA certificate; modify-db-instance holds MultiAZ and a
// storage change for the maintenance window and refuses a storage combination
// the reference rules out. --apply-immediately applies them, moves the
// endpoint to --db-port-number and renames the instance with
// --new-db-instance-identifier, keeping its data.
func TestRDSCLI_ModifyDBInstanceSettingsPortAndRename(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	id, newID := "cli-settings-"+suffix, "cli-renamed-"+suffix
	subnetGroup, optionGroup := "cli-settings-subnets-"+suffix, "cli-settings-options-"+suffix
	_, subnets := createCLIVPCSubnets(t, "10.102.0.0/16", 2)
	runCLI(t, awsCLI("rds", "create-db-subnet-group", "--db-subnet-group-name", subnetGroup,
		"--db-subnet-group-description", "instance settings", "--subnet-ids", subnets[0], subnets[1]))
	runCLI(t, awsCLI("rds", "create-option-group", "--option-group-name", optionGroup, "--engine-name", "postgres",
		"--major-engine-version", "16", "--option-group-description", "instance settings"))
	t.Cleanup(func() {
		for _, instanceID := range []string{id, newID} {
			if awsCLI("rds", "delete-db-instance", "--db-instance-identifier", instanceID, "--skip-final-snapshot").Run() == nil {
				_ = awsCLI("rds", "wait", "db-instance-deleted", "--db-instance-identifier", instanceID).Run()
			}
		}
		_ = awsCLI("rds", "delete-option-group", "--option-group-name", optionGroup).Run()
		_ = awsCLI("rds", "delete-db-subnet-group", "--db-subnet-group-name", subnetGroup).Run()
	})
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", id,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--engine-version", "16.15",
		"--allocated-storage", "100",
		"--storage-type", "io1",
		"--iops", "1000",
		"--db-subnet-group-name", subnetGroup,
		"--option-group-name", optionGroup,
		"--ca-certificate-identifier", "rds-ca-rsa4096-g1",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--backup-retention-period", "0"))
	cliWaitDBInstanceAvailable(t, id)
	created := cliDescribeInstanceSettings(t, id)
	assert.Equal(t, "io1", created.StorageType)
	assert.Equal(t, 1000, created.Iops)
	assert.Equal(t, subnetGroup, created.DBSubnetGroup.DBSubnetGroupName)
	require.Len(t, created.OptionGroupMemberships, 1)
	assert.Equal(t, optionGroup, created.OptionGroupMemberships[0].OptionGroupName)
	assert.Equal(t, "in-sync", created.OptionGroupMemberships[0].Status)
	assert.Equal(t, "rds-ca-rsa4096-g1", created.CACertificateIdentifier)
	assert.Equal(t, "postgresql-license", created.LicenseModel, "RDS for PostgreSQL has one license model")
	conn := cliConnectPostgres(t, ctx, created.Endpoint.Address, created.Endpoint.Port)
	_, err := conn.Exec(ctx, `CREATE TABLE ledger (entry text); INSERT INTO ledger VALUES ('before the rename')`)
	require.NoError(t, err)

	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "modify-db-instance", "--db-instance-identifier", id,
		"--storage-type", "gp2", "--apply-immediately")), "InvalidParameterCombination")
	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "modify-db-instance", "--db-instance-identifier", id,
		"--option-group-name", "cli-settings-no-such-group")), "OptionGroupNotFoundFault")
	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "delete-db-subnet-group", "--db-subnet-group-name", subnetGroup)),
		"InvalidDBSubnetGroupStateFault")

	var held struct {
		DBInstance cliInstanceSettings `json:"DBInstance"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "modify-db-instance", "--db-instance-identifier", id,
		"--multi-az", "--storage-type", "gp3", "--iops", "3000", "--storage-throughput", "250", "--allocated-storage", "100",
		"--no-apply-immediately")), &held)
	assert.False(t, held.DBInstance.MultiAZ)
	assert.Equal(t, "io1", held.DBInstance.StorageType)
	require.NotNil(t, held.DBInstance.PendingModifiedValues.MultiAZ)
	assert.True(t, *held.DBInstance.PendingModifiedValues.MultiAZ)
	assert.Equal(t, "gp3", held.DBInstance.PendingModifiedValues.StorageType)
	assert.Equal(t, 3000, held.DBInstance.PendingModifiedValues.Iops)
	assert.Equal(t, 250, held.DBInstance.PendingModifiedValues.StorageThroughput)

	free, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := free.Addr().(*net.TCPAddr).Port
	require.NoError(t, free.Close())
	var applied struct {
		DBInstance cliInstanceSettings `json:"DBInstance"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "modify-db-instance", "--db-instance-identifier", id,
		"--new-db-instance-identifier", newID, "--db-port-number", strconv.Itoa(port), "--apply-immediately")), &applied)
	assert.Equal(t, newID, applied.DBInstance.DBInstanceIdentifier, "the instance answers to its new identifier at once")
	assert.Equal(t, "modifying", applied.DBInstance.DBInstanceStatus)
	assert.Equal(t, port, applied.DBInstance.PendingModifiedValues.Port)

	cliWaitDBInstanceAvailable(t, newID)
	renamed := cliDescribeInstanceSettings(t, newID)
	assert.Equal(t, fmt.Sprintf("%s%s", created.DBInstanceArn[:len(created.DBInstanceArn)-len(id)], newID), renamed.DBInstanceArn)
	assert.True(t, renamed.MultiAZ)
	assert.Equal(t, "gp3", renamed.StorageType)
	assert.Equal(t, 3000, renamed.Iops)
	assert.Equal(t, 250, renamed.StorageThroughput)
	assert.Equal(t, port, renamed.Endpoint.Port)
	assert.Zero(t, renamed.PendingModifiedValues.Port)
	assert.Empty(t, renamed.PendingModifiedValues.DBInstanceIdentifier)
	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", id)), "DBInstanceNotFound")
	moved := cliConnectPostgres(t, ctx, renamed.Endpoint.Address, renamed.Endpoint.Port)
	assert.Equal(t, []string{"before the rename"}, cliLedger(t, ctx, moved))
}
