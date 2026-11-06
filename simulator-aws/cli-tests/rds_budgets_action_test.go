package aws_cli_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRDSCLI_StopByBudgetsAction expects budgets execute-budget-action on a
// STOP_RDS_INSTANCES action to stop the DB instance it names, read back
// through rds describe-db-instances.
func TestRDSCLI_StopByBudgetsAction(t *testing.T) {
	t.Parallel()
	const instanceID, budgetName, accountID = "cli-budgets-stop-pg", "cli-rds-stop-budget", "123456789012"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instanceID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--master-username", "dbadmin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("rds", "delete-db-instance", "--db-instance-identifier", instanceID, "--skip-final-snapshot"))
	})

	runCLI(t, awsCLI("budgets", "create-budget", "--account-id", accountID,
		"--budget", `{"BudgetName":"`+budgetName+`","BudgetLimit":{"Amount":"100","Unit":"USD"},"TimeUnit":"MONTHLY","BudgetType":"COST"}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("budgets", "delete-budget", "--account-id", accountID, "--budget-name", budgetName))
	})
	var created struct {
		ActionID string `json:"ActionId"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("budgets", "create-budget-action",
		"--account-id", accountID,
		"--budget-name", budgetName,
		"--notification-type", "ACTUAL",
		"--action-type", "RUN_SSM_DOCUMENTS",
		"--action-threshold", "ActionThresholdValue=90,ActionThresholdType=PERCENTAGE",
		"--definition", `{"SsmActionDefinition":{"ActionSubType":"STOP_RDS_INSTANCES","Region":"us-east-1","InstanceIds":["`+instanceID+`"]}}`,
		"--execution-role-arn", "arn:aws:iam::"+accountID+":role/budgets-exec",
		"--approval-model", "MANUAL",
		"--subscribers", "SubscriptionType=EMAIL,Address=ops@example.com"))), &created))

	cliWaitDBInstanceAvailable(t, instanceID)
	runCLI(t, awsCLI("budgets", "execute-budget-action",
		"--account-id", accountID, "--budget-name", budgetName,
		"--action-id", created.ActionID, "--execution-type", "APPROVE_BUDGET_ACTION"))
	require.Eventually(t, func() bool {
		described, err := awsCLI("rds", "describe-db-instances", "--db-instance-identifier", instanceID).Output()
		if err != nil {
			return false
		}
		var desc struct {
			DBInstances []struct {
				DBInstanceStatus string `json:"DBInstanceStatus"`
			} `json:"DBInstances"`
		}
		return json.Unmarshal(described, &desc) == nil && len(desc.DBInstances) == 1 && desc.DBInstances[0].DBInstanceStatus == "stopped"
	}, 2*time.Minute, 200*time.Millisecond, "the Budgets action stops the instance")
}
