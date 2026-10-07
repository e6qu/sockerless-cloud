package aws_cli_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliBlueGreenDeployment struct {
	BlueGreenDeploymentIdentifier string `json:"BlueGreenDeploymentIdentifier"`
	Source                        string `json:"Source"`
	Target                        string `json:"Target"`
	Status                        string `json:"Status"`
}

// cliAwaitBlueGreenStatus polls describe-blue-green-deployments, for which
// the aws CLI carries no waiter, until the deployment reports status.
func cliAwaitBlueGreenStatus(t *testing.T, id, status string) cliBlueGreenDeployment {
	t.Helper()
	var deployment cliBlueGreenDeployment
	require.Eventually(t, func() bool {
		out, err := awsCLI("rds", "describe-blue-green-deployments", "--blue-green-deployment-identifier", id).Output()
		if err != nil {
			return false
		}
		var described struct {
			BlueGreenDeployments []cliBlueGreenDeployment `json:"BlueGreenDeployments"`
		}
		parseJSON(t, string(out), &described)
		if len(described.BlueGreenDeployments) != 1 {
			return false
		}
		deployment = described.BlueGreenDeployments[0]
		return deployment.Status == status
	}, 5*time.Minute, time.Second, "blue/green deployment %s must reach %s", id, status)
	return deployment
}

// aws rds create-blue-green-deployment provisions a green RDS for MySQL
// instance holding the blue instance's rows, read-only, with the target
// engine version; switchover-blue-green-deployment moves the green instance
// onto the blue identifier and endpoint with every row the blue instance
// committed and keeps the blue instance as -old1; delete-blue-green-deployment
// then ends the deployment and leaves both instances.
func TestRDSCLI_BlueGreenSwitchover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	blueID := fmt.Sprintf("cli-bg-%d", time.Now().UnixNano())
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", blueID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--backup-retention-period", "1"))
	cliCleanupDBInstance(t, blueID)
	cliCleanupDBInstance(t, blueID+"-old1")
	blue := cliAvailableDBInstance(t, blueID)
	blueDB := cliConnectMySQLAs(t, blue.Endpoint.Address, blue.Endpoint.Port, cliRestoreUsername, cliRestorePassword)
	for _, statement := range []string{
		`CREATE TABLE ledger (entry varchar(64) NOT NULL)`,
		`INSERT INTO ledger VALUES ('before-green')`,
	} {
		_, err := blueDB.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	var arn struct {
		DBInstances []struct {
			DBInstanceArn string `json:"DBInstanceArn"`
		} `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", blueID)), &arn)
	blueARN := arn.DBInstances[0].DBInstanceArn

	var created struct {
		BlueGreenDeployment cliBlueGreenDeployment `json:"BlueGreenDeployment"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "create-blue-green-deployment",
		"--blue-green-deployment-name", blueID,
		"--source", blueARN,
		"--target-engine-version", "8.0.41")), &created)
	deploymentID := created.BlueGreenDeployment.BlueGreenDeploymentIdentifier
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-blue-green-deployment", "--blue-green-deployment-identifier", deploymentID, "--delete-target").Run()
	})
	assert.Equal(t, "PROVISIONING", created.BlueGreenDeployment.Status)
	greenID := created.BlueGreenDeployment.Target[strings.LastIndex(created.BlueGreenDeployment.Target, ":")+1:]
	assert.True(t, strings.HasPrefix(greenID, blueID+"-green-"), greenID)

	cliAwaitBlueGreenStatus(t, deploymentID, "AVAILABLE")
	green := cliAvailableDBInstance(t, greenID)
	greenDB := cliConnectMySQLAs(t, green.Endpoint.Address, green.Endpoint.Port, cliRestoreUsername, cliRestorePassword)
	assert.Equal(t, []string{"before-green"}, cliMySQLLedger(t, ctx, greenDB))
	_, err := greenDB.ExecContext(ctx, `INSERT INTO ledger VALUES ('written-to-green')`)
	assert.Error(t, err, "the green instance serves its sessions read-only")
	_, err = blueDB.ExecContext(ctx, `INSERT INTO ledger VALUES ('during-deployment')`)
	require.NoError(t, err)

	var switched struct {
		BlueGreenDeployment cliBlueGreenDeployment `json:"BlueGreenDeployment"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "switchover-blue-green-deployment",
		"--blue-green-deployment-identifier", deploymentID, "--switchover-timeout", "300")), &switched)
	assert.Equal(t, "SWITCHOVER_IN_PROGRESS", switched.BlueGreenDeployment.Status)
	completed := cliAwaitBlueGreenStatus(t, deploymentID, "SWITCHOVER_COMPLETED")
	assert.Equal(t, blueARN, completed.Target)
	assert.True(t, strings.HasSuffix(completed.Source, ":db:"+blueID+"-old1"), completed.Source)

	production := cliAvailableDBInstance(t, blueID)
	assert.Equal(t, blue.Endpoint, production.Endpoint, "the green instance took over the blue endpoint")
	productionDB := cliConnectMySQLAs(t, production.Endpoint.Address, production.Endpoint.Port, cliRestoreUsername, cliRestorePassword)
	assert.Equal(t, []string{"before-green", "during-deployment"}, cliMySQLLedger(t, ctx, productionDB))
	_, err = productionDB.ExecContext(ctx, `INSERT INTO ledger VALUES ('after-switchover')`)
	require.NoError(t, err)
	retired := cliAvailableDBInstance(t, blueID+"-old1")
	assert.NotEqual(t, blue.Endpoint, retired.Endpoint)

	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "delete-blue-green-deployment",
		"--blue-green-deployment-identifier", deploymentID, "--delete-target")), "InvalidBlueGreenDeploymentStateFault")
	var deleted struct {
		BlueGreenDeployment cliBlueGreenDeployment `json:"BlueGreenDeployment"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "delete-blue-green-deployment",
		"--blue-green-deployment-identifier", deploymentID)), &deleted)
	assert.Equal(t, "DELETING", deleted.BlueGreenDeployment.Status)
	require.Eventually(t, func() bool {
		out, err := awsCLI("rds", "describe-blue-green-deployments", "--blue-green-deployment-identifier", deploymentID).CombinedOutput()
		return err != nil && strings.Contains(string(out), "BlueGreenDeploymentNotFoundFault")
	}, 2*time.Minute, time.Second)
}
