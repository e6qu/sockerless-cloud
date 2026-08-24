package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	budgetstypes "github.com/aws/aws-sdk-go-v2/service/budgets/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The operations AWS added to the vendored models after the simulator's
// implementation, driven through the real SDK clients. Each family is proved
// by its effects: a budget action's execution is read back from the IAM
// attachment it created and the instance it stopped, not from its own status
// alone.

func TestGlue_DataCatalogExportConfigurationRoundTrips(t *testing.T) {
	c := glueClient()
	put, err := c.PutDataCatalogExportConfiguration(ctx, &glue.PutDataCatalogExportConfigurationInput{
		ExportSetting: gluetypes.ExportSettingEnabled,
	})
	require.NoError(t, err)
	assert.Equal(t, gluetypes.ExportSettingEnabled, put.ExportSetting)

	got, err := c.GetDataCatalogExportConfiguration(ctx, &glue.GetDataCatalogExportConfigurationInput{})
	require.NoError(t, err)
	assert.Equal(t, gluetypes.ExportSettingEnabled, got.ExportSetting,
		"the configuration that was put must be the one returned")
	assert.NotNil(t, got.CreatedAt)
}

func TestIAM_AccountPropertiesRoundTripAndTemplatesNameTheirCatalog(t *testing.T) {
	c := iamClient()
	_, err := c.PutAccountProperties(ctx, &iam.PutAccountPropertiesInput{
		Properties: map[string]string{"assumeRoleWithWebIdentityLimit": "extended"},
	})
	require.NoError(t, err)
	got, err := c.GetAccountProperties(ctx, &iam.GetAccountPropertiesInput{})
	require.NoError(t, err)
	assert.Equal(t, "extended", got.Properties["assumeRoleWithWebIdentityLimit"],
		"the property that was put must be the one returned")

	// Role templates are AWS's own catalog; both operations say so rather
	// than fabricating template content.
	_, err = c.GetRoleTemplateVersion(ctx, &iam.GetRoleTemplateVersionInput{
		TemplateArn: aws.String("arn:aws:iam::aws:role-template/lambda.amazonaws.com/basic:1"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "catalog",
		"the failure must name the missing catalog: %v", err)
	_, err = c.AcquireRole(ctx, &iam.AcquireRoleInput{
		TemplateArn: aws.String("arn:aws:iam::aws:role-template/lambda.amazonaws.com/basic:1"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CreateRole",
		"the failure must point at the equivalent that works: %v", err)
}

func TestBudgets_ActionExecutesThroughTheSimulatorsOwnServices(t *testing.T) {
	c := budgetsClient()
	iamC := iamClient()
	account := budgetsAccountID
	const budgetName = "drift-action-budget"

	_, err := c.CreateBudget(ctx, &budgets.CreateBudgetInput{
		AccountId: aws.String(account),
		Budget: &budgetstypes.Budget{
			BudgetName: aws.String(budgetName),
			BudgetType: budgetstypes.BudgetTypeCost,
			TimeUnit:   budgetstypes.TimeUnitMonthly,
			BudgetLimit: &budgetstypes.Spend{
				Amount: aws.String("100"), Unit: aws.String("USD"),
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteBudget(ctx, &budgets.DeleteBudgetInput{
			AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		})
	})

	// The action attaches a deny policy to a real role, so the role exists
	// first and the attachment is read back through IAM itself.
	const roleName = "drift-action-role"
	_, err = iamC.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamC.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
	})

	const denyPolicy = "arn:aws:iam::aws:policy/AWSDenyAll"
	created, err := c.CreateBudgetAction(ctx, &budgets.CreateBudgetActionInput{
		AccountId:        aws.String(account),
		BudgetName:       aws.String(budgetName),
		NotificationType: budgetstypes.NotificationTypeActual,
		ActionType:       budgetstypes.ActionTypeIam,
		ActionThreshold: &budgetstypes.ActionThreshold{
			ActionThresholdType:  budgetstypes.ThresholdTypePercentage,
			ActionThresholdValue: 90,
		},
		Definition: &budgetstypes.Definition{
			IamActionDefinition: &budgetstypes.IamActionDefinition{
				PolicyArn: aws.String(denyPolicy),
				Roles:     []string{roleName},
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
	actionID := aws.ToString(created.ActionId)

	described, err := c.DescribeBudgetAction(ctx, &budgets.DescribeBudgetActionInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName), ActionId: aws.String(actionID),
	})
	require.NoError(t, err)
	assert.Equal(t, budgetstypes.ActionStatusStandby, described.Action.Status)

	_, err = c.ExecuteBudgetAction(ctx, &budgets.ExecuteBudgetActionInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		ActionId: aws.String(actionID), ExecutionType: budgetstypes.ExecutionTypeApproveBudgetAction,
	})
	require.NoError(t, err)

	// The execution's effect is what proves it: the policy is attached to the
	// role, read back through IAM rather than through Budgets' own status.
	attached, err := iamC.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	})
	require.NoError(t, err)
	found := false
	for _, policy := range attached.AttachedPolicies {
		if aws.ToString(policy.PolicyArn) == denyPolicy {
			found = true
		}
	}
	assert.True(t, found, "executing the action must attach the policy through the simulator's own IAM")

	// Reversing detaches it again.
	_, err = c.ExecuteBudgetAction(ctx, &budgets.ExecuteBudgetActionInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		ActionId: aws.String(actionID), ExecutionType: budgetstypes.ExecutionTypeReverseBudgetAction,
	})
	require.NoError(t, err)
	attached, err = iamC.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	})
	require.NoError(t, err)
	for _, policy := range attached.AttachedPolicies {
		assert.NotEqual(t, denyPolicy, aws.ToString(policy.PolicyArn),
			"reversing the action must detach the policy it attached")
	}

	// The action's history recorded both executions.
	histories, err := c.DescribeBudgetActionHistories(ctx, &budgets.DescribeBudgetActionHistoriesInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName), ActionId: aws.String(actionID),
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(histories.ActionHistories), 3,
		"create and two executions must be in the history")

	// Updating the action changes what a later describe returns.
	updated, err := c.UpdateBudgetAction(ctx, &budgets.UpdateBudgetActionInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName), ActionId: aws.String(actionID),
		ActionThreshold: &budgetstypes.ActionThreshold{
			ActionThresholdType:  budgetstypes.ThresholdTypePercentage,
			ActionThresholdValue: 95,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, float64(95), updated.NewAction.ActionThreshold.ActionThresholdValue,
		"the update must be the one returned")

	// The budget-scoped listing carries the action.
	forBudget, err := c.DescribeBudgetActionsForBudget(ctx, &budgets.DescribeBudgetActionsForBudgetInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
	})
	require.NoError(t, err)
	require.Len(t, forBudget.Actions, 1)
	assert.Equal(t, actionID, aws.ToString(forBudget.Actions[0].ActionId))

	// Account-wide listing sees it; deleting removes it.
	forAccount, err := c.DescribeBudgetActionsForAccount(ctx, &budgets.DescribeBudgetActionsForAccountInput{
		AccountId: aws.String(account),
	})
	require.NoError(t, err)
	require.NotEmpty(t, forAccount.Actions)
	_, err = c.DeleteBudgetAction(ctx, &budgets.DeleteBudgetActionInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName), ActionId: aws.String(actionID),
	})
	require.NoError(t, err)
}

func TestEC2_IpamInternetRegistryIsRealStateAndNamesTheRIR(t *testing.T) {
	c := ec2Client()
	ipam, err := c.CreateIpam(ctx, &ec2.CreateIpamInput{})
	require.NoError(t, err)
	ipamID := aws.ToString(ipam.Ipam.IpamId)
	t.Cleanup(func() {
		_, _ = c.DeleteIpam(ctx, &ec2.DeleteIpamInput{IpamId: aws.String(ipamID), Cascade: aws.Bool(true)})
	})

	created, err := c.CreateIpamInternetRegistryAssociation(ctx, &ec2.CreateIpamInternetRegistryAssociationInput{
		IpamId:             aws.String(ipamID),
		Rir:                ec2types.RirArin,
		OrganizationHandle: aws.String("EXAMPLE-ORG"),
	})
	require.NoError(t, err)
	associationID := aws.ToString(created.IpamInternetRegistryAssociation.IpamInternetRegistryAssociationId)

	// The association is real state and round-trips.
	described, err := c.DescribeIpamInternetRegistryAssociations(ctx, &ec2.DescribeIpamInternetRegistryAssociationsInput{
		IpamInternetRegistryAssociationIds: []string{associationID},
	})
	require.NoError(t, err)
	require.Len(t, described.IpamInternetRegistryAssociations, 1)
	assert.Equal(t, "EXAMPLE-ORG", aws.ToString(described.IpamInternetRegistryAssociations[0].OrganizationHandle))

	// Enabling begins verification with the RIR, which is outside AWS: the
	// failure names the registry, not a vague unavailability.
	// The RPKI provisioning parameters are required by the model — they are
	// what a caller hands the registry — and the SDK validates them
	// client-side; the refusal under test is the server's.
	_, err = c.EnableIpamInternetRegistryAssociation(ctx, &ec2.EnableIpamInternetRegistryAssociationInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
		RpkiVersion:                       aws.String("1"),
		ServiceUri:                        aws.String("https://rpki.example.net/publication"),
		ChildHandle:                       aws.String("child-handle"),
		ParentHandle:                      aws.String("parent-handle"),
		ParentBpkiTa:                      aws.String("dGVzdA=="),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Regional Internet Registry",
		"the failure must name the registry: %v", err)

	// Registrations are the caller's own declarations and round-trip.
	_, err = c.CreateIpamRoutingPolicyRegistration(ctx, &ec2.CreateIpamRoutingPolicyRegistrationInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
		Cidr:                              aws.String("203.0.113.0/24"),
		Asns:                              []string{"64500"},
	})
	require.NoError(t, err)
	registrations, err := c.GetIpamRoutingPolicyRegistrations(ctx, &ec2.GetIpamRoutingPolicyRegistrationsInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.NoError(t, err)
	require.Len(t, registrations.IpamRoutingPolicyRegistrations, 1)
	assert.Equal(t, "203.0.113.0/24", aws.ToString(registrations.IpamRoutingPolicyRegistrations[0].Cidr))

	// Modifying a registration changes what the listing returns.
	_, err = c.ModifyIpamRoutingPolicyRegistration(ctx, &ec2.ModifyIpamRoutingPolicyRegistrationInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
		Cidr:                              aws.String("203.0.113.0/24"),
		Asns:                              []string{"64501"},
	})
	require.NoError(t, err)
	registrations, err = c.GetIpamRoutingPolicyRegistrations(ctx, &ec2.GetIpamRoutingPolicyRegistrationsInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.NoError(t, err)
	require.Len(t, registrations.IpamRoutingPolicyRegistrations, 1)
	require.Len(t, registrations.IpamRoutingPolicyRegistrations[0].Asns, 1)
	assert.Equal(t, "64501", registrations.IpamRoutingPolicyRegistrations[0].Asns[0],
		"the modification must be the one returned")

	// The batch form takes a caller-authored delta document, applies it, and
	// creates a delta record the deltas listing returns.
	batch, err := c.BatchModifyIpamRoutingPolicyRegistrations(ctx, &ec2.BatchModifyIpamRoutingPolicyRegistrationsInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
		DeltaJson:                         aws.String(`[{"Cidr":"203.0.113.0/24","Asns":["64502"]}]`),
	})
	require.NoError(t, err)
	require.NotNil(t, batch.IpamRoutingPolicyRegistrationDelta)
	deltaID := aws.ToString(batch.IpamRoutingPolicyRegistrationDelta.DeltaId)
	require.NotEmpty(t, deltaID)

	// The batch really applied: the registration carries the new ASN and
	// names the delta as its latest.
	registrations, err = c.GetIpamRoutingPolicyRegistrations(ctx, &ec2.GetIpamRoutingPolicyRegistrationsInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.NoError(t, err)
	require.Len(t, registrations.IpamRoutingPolicyRegistrations, 1)
	assert.Equal(t, "64502", registrations.IpamRoutingPolicyRegistrations[0].Asns[0],
		"the delta document must actually modify the registration")
	assert.Equal(t, deltaID, aws.ToString(registrations.IpamRoutingPolicyRegistrations[0].LatestDeltaId))

	deltas, err := c.GetIpamRoutingPolicyRegistrationDeltas(ctx, &ec2.GetIpamRoutingPolicyRegistrationDeltasInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.NoError(t, err)
	require.Len(t, deltas.IpamRoutingPolicyRegistrationDeltas, 1)
	assert.Equal(t, deltaID, aws.ToString(deltas.IpamRoutingPolicyRegistrationDeltas[0].DeltaId))

	// So are the imports the registry would have sent back.
	asns, err := c.GetIpamInternetRegistryAssociationAsns(ctx, &ec2.GetIpamInternetRegistryAssociationAsnsInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.NoError(t, err)
	assert.Empty(t, asns.IpamInternetRegistryAssociationAsns)
	cidrs, err := c.GetIpamInternetRegistryAssociationCidrs(ctx, &ec2.GetIpamInternetRegistryAssociationCidrsInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.NoError(t, err)
	assert.Empty(t, cidrs.IpamInternetRegistryAssociationCidrs)

	// Route protection findings compare announcements against ROAs; this
	// installation announces nothing and holds none.
	findings, err := c.GetIpamRouteProtectionFindings(ctx, &ec2.GetIpamRouteProtectionFindingsInput{
		IpamId: aws.String(ipamID),
	})
	require.NoError(t, err)
	assert.Empty(t, findings.RouteProtectionFindings)

	// Deleting the registration empties the listing.
	_, err = c.DeleteIpamRoutingPolicyRegistration(ctx, &ec2.DeleteIpamRoutingPolicyRegistrationInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
		Cidr:                              aws.String("203.0.113.0/24"),
	})
	require.NoError(t, err)
	registrations, err = c.GetIpamRoutingPolicyRegistrations(ctx, &ec2.GetIpamRoutingPolicyRegistrationsInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.NoError(t, err)
	assert.Empty(t, registrations.IpamRoutingPolicyRegistrations)

	// ROAs are the registries' RPKI data; the failure says so.
	_, err = c.GetIpamRouteOriginAuthorizations(ctx, &ec2.GetIpamRouteOriginAuthorizationsInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.Error(t, err)
	assert.Contains(t, strings.ToUpper(err.Error()), "RPKI",
		"the failure must name the RPKI repositories: %v", err)

	_, err = c.DeleteIpamInternetRegistryAssociation(ctx, &ec2.DeleteIpamInternetRegistryAssociationInput{
		IpamInternetRegistryAssociationId: aws.String(associationID),
	})
	require.NoError(t, err)
}

func TestEC2_ApplicationStatusIsMeasuredNotDeclared(t *testing.T) {
	c := ec2Client()

	instance := runDriftTestInstance(t, c)

	created, err := c.CreateApplicationStatusCheck(ctx, &ec2.CreateApplicationStatusCheckInput{
		Protocol: ec2types.NetworkProtocolEnumHttp,
		Port:     aws.Int32(59999),
	})
	require.NoError(t, err)
	checkID := aws.ToString(created.ApplicationStatusCheck.ApplicationStatusCheckId)
	t.Cleanup(func() {
		_, _ = c.DeleteApplicationStatusCheck(ctx, &ec2.DeleteApplicationStatusCheckInput{
			ApplicationStatusCheckId: aws.String(checkID),
		})
	})

	_, err = c.AssociateApplicationStatusCheck(ctx, &ec2.AssociateApplicationStatusCheckInput{
		ApplicationStatusCheckId: aws.String(checkID),
		InstanceIds:              []string{instance},
	})
	require.NoError(t, err)

	// Nothing listens on the check's port, so the probe really fails: the
	// check reports failed and the instance-level status is impaired — the
	// SDK's own vocabulary, deserialised by the SDK's own client.
	status, err := c.DescribeApplicationStatus(ctx, &ec2.DescribeApplicationStatusInput{
		InstanceIds: []string{instance},
	})
	require.NoError(t, err)
	require.NotNil(t, status.ApplicationStatuses)
	require.NotEmpty(t, status.ApplicationStatuses.Instances)
	measured := status.ApplicationStatuses.Instances[0]
	require.NotNil(t, measured.ApplicationStatus)
	assert.Equal(t, ec2types.ApplicationStatusEnumImpaired, measured.ApplicationStatus.Status)
	require.NotEmpty(t, measured.ApplicationStatus.Details)
	assert.Equal(t, ec2types.ApplicationStatusCheckEnumFailed, measured.ApplicationStatus.Details[0].Status,
		"the probe really ran and really failed")

	// The check round-trips through its own describe, and a modification is
	// the one returned.
	checks, err := c.DescribeApplicationStatusChecks(ctx, &ec2.DescribeApplicationStatusChecksInput{
		ApplicationStatusCheckIds: []string{checkID},
	})
	require.NoError(t, err)
	require.Len(t, checks.ApplicationStatusChecks, 1)
	_, err = c.ModifyApplicationStatusCheck(ctx, &ec2.ModifyApplicationStatusCheckInput{
		ApplicationStatusCheckId: aws.String(checkID),
		Port:                     aws.Int32(58888),
	})
	require.NoError(t, err)
	checks, err = c.DescribeApplicationStatusChecks(ctx, &ec2.DescribeApplicationStatusChecksInput{
		ApplicationStatusCheckIds: []string{checkID},
	})
	require.NoError(t, err)
	require.Len(t, checks.ApplicationStatusChecks, 1)
	assert.Equal(t, int32(58888), aws.ToInt32(checks.ApplicationStatusChecks[0].Port))

	// The association listing carries the binding.
	associations, err := c.DescribeApplicationStatusCheckAssociations(ctx, &ec2.DescribeApplicationStatusCheckAssociationsInput{
		ApplicationStatusCheckIds: []string{checkID},
	})
	require.NoError(t, err)
	require.Len(t, associations.Associations, 1)
	assert.Equal(t, checkID, aws.ToString(associations.Associations[0].ApplicationStatusCheckId))

	// Suppression is state of its own, and reports as itself.
	_, err = c.EnableApplicationStatusCheckSuppression(ctx, &ec2.EnableApplicationStatusCheckSuppressionInput{
		InstanceIds: []string{instance},
	})
	require.NoError(t, err)
	status, err = c.DescribeApplicationStatus(ctx, &ec2.DescribeApplicationStatusInput{
		InstanceIds: []string{instance},
	})
	require.NoError(t, err)
	require.NotEmpty(t, status.ApplicationStatuses.Instances)
	assert.Equal(t, ec2types.ApplicationStatusEnumSuppressed,
		status.ApplicationStatuses.Instances[0].ApplicationStatus.Status)

	// Lifting the suppression restores the measured verdict.
	_, err = c.DisableApplicationStatusCheckSuppression(ctx, &ec2.DisableApplicationStatusCheckSuppressionInput{
		InstanceIds: []string{instance},
	})
	require.NoError(t, err)
	status, err = c.DescribeApplicationStatus(ctx, &ec2.DescribeApplicationStatusInput{
		InstanceIds: []string{instance},
	})
	require.NoError(t, err)
	require.NotEmpty(t, status.ApplicationStatuses.Instances)
	assert.Equal(t, ec2types.ApplicationStatusEnumImpaired,
		status.ApplicationStatuses.Instances[0].ApplicationStatus.Status,
		"lifting suppression must restore the measured verdict")

	// Disassociating removes the binding, and with it the status.
	disassociated, err := c.DisassociateApplicationStatusCheck(ctx, &ec2.DisassociateApplicationStatusCheckInput{
		ApplicationStatusCheckId: aws.String(checkID),
		InstanceIds:              []string{instance},
	})
	require.NoError(t, err)
	require.Len(t, disassociated.SuccessfulResults, 1)
	associations, err = c.DescribeApplicationStatusCheckAssociations(ctx, &ec2.DescribeApplicationStatusCheckAssociationsInput{
		ApplicationStatusCheckIds: []string{checkID},
	})
	require.NoError(t, err)
	assert.Empty(t, associations.Associations)
}

// runDriftTestInstance launches one instance for the drift tests and stops it
// on cleanup.
func runDriftTestInstance(t *testing.T, c *ec2.Client) string {
	t.Helper()
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:  aws.String("ami-0drift0test0"),
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
	})
	require.NoError(t, err)
	require.NotEmpty(t, run.Instances)
	id := aws.ToString(run.Instances[0].InstanceId)
	t.Cleanup(func() {
		_, _ = c.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}})
	})
	return id
}

func TestBudgets_NotificationUpdatesMoveTheirSubscribers(t *testing.T) {
	c := budgetsClient()
	account := budgetsAccountID
	const budgetName = "drift-notification-budget"
	_, err := c.CreateBudget(ctx, &budgets.CreateBudgetInput{
		AccountId: aws.String(account),
		Budget: &budgetstypes.Budget{
			BudgetName: aws.String(budgetName),
			BudgetType: budgetstypes.BudgetTypeCost,
			TimeUnit:   budgetstypes.TimeUnitMonthly,
			BudgetLimit: &budgetstypes.Spend{
				Amount: aws.String("50"), Unit: aws.String("USD"),
			},
		},
		NotificationsWithSubscribers: []budgetstypes.NotificationWithSubscribers{{
			Notification: &budgetstypes.Notification{
				NotificationType:   budgetstypes.NotificationTypeActual,
				ComparisonOperator: budgetstypes.ComparisonOperatorGreaterThan,
				Threshold:          80,
			},
			Subscribers: []budgetstypes.Subscriber{{
				SubscriptionType: budgetstypes.SubscriptionTypeEmail,
				Address:          aws.String("before@example.com"),
			}},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteBudget(ctx, &budgets.DeleteBudgetInput{
			AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		})
	})

	oldNotification := &budgetstypes.Notification{
		NotificationType:   budgetstypes.NotificationTypeActual,
		ComparisonOperator: budgetstypes.ComparisonOperatorGreaterThan,
		Threshold:          80,
	}
	newNotification := &budgetstypes.Notification{
		NotificationType:   budgetstypes.NotificationTypeActual,
		ComparisonOperator: budgetstypes.ComparisonOperatorGreaterThan,
		Threshold:          90,
	}
	_, err = c.UpdateNotification(ctx, &budgets.UpdateNotificationInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		OldNotification: oldNotification, NewNotification: newNotification,
	})
	require.NoError(t, err)

	// The notification's identity is its field tuple, so the subscriber must
	// have moved to the new tuple rather than being orphaned under the old.
	subscribers, err := c.DescribeSubscribersForNotification(ctx, &budgets.DescribeSubscribersForNotificationInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		Notification: newNotification,
	})
	require.NoError(t, err)
	require.Len(t, subscribers.Subscribers, 1,
		"the subscriber must follow the notification it was subscribed to")
	assert.Equal(t, "before@example.com", aws.ToString(subscribers.Subscribers[0].Address))

	// Replacing the subscriber under the new tuple.
	_, err = c.UpdateSubscriber(ctx, &budgets.UpdateSubscriberInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		Notification: newNotification,
		OldSubscriber: &budgetstypes.Subscriber{
			SubscriptionType: budgetstypes.SubscriptionTypeEmail,
			Address:          aws.String("before@example.com"),
		},
		NewSubscriber: &budgetstypes.Subscriber{
			SubscriptionType: budgetstypes.SubscriptionTypeEmail,
			Address:          aws.String("after@example.com"),
		},
	})
	require.NoError(t, err)
	subscribers, err = c.DescribeSubscribersForNotification(ctx, &budgets.DescribeSubscribersForNotificationInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
		Notification: newNotification,
	})
	require.NoError(t, err)
	require.Len(t, subscribers.Subscribers, 1)
	assert.Equal(t, "after@example.com", aws.ToString(subscribers.Subscribers[0].Address))

	// The account-wide notification listing names the budget and its
	// notification at the updated threshold.
	forAccount, err := c.DescribeBudgetNotificationsForAccount(ctx, &budgets.DescribeBudgetNotificationsForAccountInput{
		AccountId: aws.String(account),
	})
	require.NoError(t, err)
	foundBudget := false
	for _, entry := range forAccount.BudgetNotificationsForAccount {
		if aws.ToString(entry.BudgetName) != budgetName {
			continue
		}
		foundBudget = true
		require.NotEmpty(t, entry.Notifications)
		assert.Equal(t, float64(90), entry.Notifications[0].Threshold)
	}
	assert.True(t, foundBudget, "the account-wide listing must carry the budget's notifications")

	// Performance history: the budgeted amount beside an actual of zero,
	// which is this simulator's truth — it accrues no cost.
	history, err := c.DescribeBudgetPerformanceHistory(ctx, &budgets.DescribeBudgetPerformanceHistoryInput{
		AccountId: aws.String(account), BudgetName: aws.String(budgetName),
	})
	require.NoError(t, err)
	require.NotNil(t, history.BudgetPerformanceHistory)
	assert.Equal(t, budgetName, aws.ToString(history.BudgetPerformanceHistory.BudgetName))
	require.NotEmpty(t, history.BudgetPerformanceHistory.BudgetedAndActualAmountsList)
	assert.Equal(t, "0",
		aws.ToString(history.BudgetPerformanceHistory.BudgetedAndActualAmountsList[0].ActualAmount.Amount),
		"the simulator accrues no cost, so the actual amount genuinely is zero")
}

func TestEC2_IpamDiscoveredRoutesComeFromTheAccountsRouteTables(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.99.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	t.Cleanup(func() {
		_, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
	})
	table, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	tableID := aws.ToString(table.RouteTable.RouteTableId)
	t.Cleanup(func() {
		_, _ = c.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{RouteTableId: aws.String(tableID)})
	})

	discovery, err := c.CreateIpamResourceDiscovery(ctx, &ec2.CreateIpamResourceDiscoveryInput{})
	require.NoError(t, err)
	discoveryID := aws.ToString(discovery.IpamResourceDiscovery.IpamResourceDiscoveryId)
	t.Cleanup(func() {
		_, _ = c.DeleteIpamResourceDiscovery(ctx, &ec2.DeleteIpamResourceDiscoveryInput{
			IpamResourceDiscoveryId: aws.String(discoveryID),
		})
	})

	routes, err := c.GetIpamDiscoveredRoutes(ctx, &ec2.GetIpamDiscoveredRoutesInput{
		IpamResourceDiscoveryId: aws.String(discoveryID),
		ResourceRegion:          aws.String("us-east-1"),
	})
	require.NoError(t, err)
	found := false
	for _, route := range routes.IpamDiscoveredRoutes {
		if aws.ToString(route.Cidr) == "10.99.0.0/16" {
			found = true
		}
	}
	assert.True(t, found,
		"the route table's own CIDR must appear among the discovered routes — they derive from real state")
}
