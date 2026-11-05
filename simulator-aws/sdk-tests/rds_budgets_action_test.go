package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	budgetstypes "github.com/aws/aws-sdk-go-v2/service/budgets/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/require"
)

// TestRDS_BudgetsActionStopsInstance expects an AWS Budgets action of subtype
// STOP_RDS_INSTANCES to stop the DB instance it names, read back through
// Amazon RDS.
func TestRDS_BudgetsActionStopsInstance(t *testing.T) {
	t.Parallel()
	c := budgetsClient()
	rdsC := rdsClient()
	account := budgetsAccountID
	const budgetName, instanceID = "rds-stop-budget", "budgets-stop-pg"

	_, err := rdsC.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("dbadmin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = rdsC.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true)})
	})

	_, err = c.CreateBudget(ctx, &budgets.CreateBudgetInput{
		AccountId: aws.String(account),
		Budget: &budgetstypes.Budget{
			BudgetName:  aws.String(budgetName),
			BudgetType:  budgetstypes.BudgetTypeCost,
			TimeUnit:    budgetstypes.TimeUnitMonthly,
			BudgetLimit: &budgetstypes.Spend{Amount: aws.String("100"), Unit: aws.String("USD")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteBudget(ctx, &budgets.DeleteBudgetInput{AccountId: aws.String(account), BudgetName: aws.String(budgetName)})
	})
	created, err := c.CreateBudgetAction(ctx, &budgets.CreateBudgetActionInput{
		AccountId:        aws.String(account),
		BudgetName:       aws.String(budgetName),
		NotificationType: budgetstypes.NotificationTypeActual,
		ActionType:       budgetstypes.ActionTypeSsm,
		ActionThreshold: &budgetstypes.ActionThreshold{
			ActionThresholdType:  budgetstypes.ThresholdTypePercentage,
			ActionThresholdValue: 90,
		},
		Definition: &budgetstypes.Definition{
			SsmActionDefinition: &budgetstypes.SsmActionDefinition{
				ActionSubType: budgetstypes.ActionSubTypeStopRds,
				Region:        aws.String("us-east-1"),
				InstanceIds:   []string{instanceID},
			},
		},
		ExecutionRoleArn: aws.String("arn:aws:iam::" + account + ":role/budgets-exec"),
		ApprovalModel:    budgetstypes.ApprovalModelManual,
		Subscribers: []budgetstypes.Subscriber{{
			SubscriptionType: budgetstypes.SubscriptionTypeEmail,
			Address:          aws.String("ops@example.com"),
		}},
	})
	require.NoError(t, err)

	waitForRDSInstanceAvailable(t, rdsC, ctx, instanceID)
	_, err = c.ExecuteBudgetAction(ctx, &budgets.ExecuteBudgetActionInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		ActionId: created.ActionId, ExecutionType: budgetstypes.ExecutionTypeApproveBudgetAction,
	})
	require.NoError(t, err)
	waitForRDSInstanceStatus(t, rdsC, ctx, instanceID, "stopped")
}
