package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrganizations_GovCloudAccount expects CreateGovCloudAccount to settle
// SUCCEEDED with both AccountId and GovCloudAccountId, readable through
// DescribeCreateAccountStatus, and the commercial account to join the tree.
func TestOrganizations_GovCloudAccount(t *testing.T) {
	c := orgClient()

	out, err := c.CreateGovCloudAccount(ctx, &organizations.CreateGovCloudAccountInput{
		Email:       aws.String("govcloud-pair@sim.invalid"),
		AccountName: aws.String("GovCloudPair"),
	})
	require.NoError(t, err)
	require.NotNil(t, out.CreateAccountStatus)
	st := out.CreateAccountStatus
	assert.Equal(t, orgtypes.CreateAccountStateSucceeded, st.State)
	assert.NotEmpty(t, aws.ToString(st.AccountId))
	assert.NotEmpty(t, aws.ToString(st.GovCloudAccountId))
	assert.NotEqual(t, aws.ToString(st.AccountId), aws.ToString(st.GovCloudAccountId))
	assert.True(t, strings.HasPrefix(aws.ToString(st.Id), "car-"))

	desc, err := c.DescribeCreateAccountStatus(ctx, &organizations.DescribeCreateAccountStatusInput{
		CreateAccountRequestId: st.Id,
	})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.CreateAccountStateSucceeded, desc.CreateAccountStatus.State)
	assert.Equal(t, aws.ToString(st.AccountId), aws.ToString(desc.CreateAccountStatus.AccountId))

	acct, err := c.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: st.AccountId})
	require.NoError(t, err)
	assert.Equal(t, "GovCloudPair", aws.ToString(acct.Account.Name))
}

// TestOrganizations_LeaveOrganization expects the caller, the management
// account, to be refused leaving its own organization.
func TestOrganizations_LeaveOrganization(t *testing.T) {
	c := orgClient()
	_, err := c.LeaveOrganization(ctx, &organizations.LeaveOrganizationInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MasterCannotLeaveOrganization")
}

// TestOrganizations_EffectivePolicyValidation expects no accounts with an
// invalid effective policy and no validation errors for a real account, with
// its id, policy type and path echoed.
func TestOrganizations_EffectivePolicyValidation(t *testing.T) {
	c := orgClient()

	inv, err := c.ListAccountsWithInvalidEffectivePolicy(ctx, &organizations.ListAccountsWithInvalidEffectivePolicyInput{
		PolicyType: orgtypes.EffectivePolicyType("BACKUP_POLICY"),
	})
	require.NoError(t, err)
	assert.Empty(t, inv.Accounts)
	assert.Equal(t, "BACKUP_POLICY", string(inv.PolicyType))

	org, err := c.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err)
	acctID := aws.ToString(org.Organization.MasterAccountId)

	errs, err := c.ListEffectivePolicyValidationErrors(ctx, &organizations.ListEffectivePolicyValidationErrorsInput{
		AccountId:  aws.String(acctID),
		PolicyType: orgtypes.EffectivePolicyType("BACKUP_POLICY"),
	})
	require.NoError(t, err)
	assert.Equal(t, acctID, aws.ToString(errs.AccountId))
	assert.Equal(t, "BACKUP_POLICY", string(errs.PolicyType))
	assert.NotEmpty(t, aws.ToString(errs.Path))
	assert.NotNil(t, errs.EvaluationTimestamp)
	assert.Empty(t, errs.EffectivePolicyValidationErrors)

	// An unknown account is rejected.
	_, err = c.ListEffectivePolicyValidationErrors(ctx, &organizations.ListEffectivePolicyValidationErrorsInput{
		AccountId:  aws.String("999999999999"),
		PolicyType: orgtypes.EffectivePolicyType("BACKUP_POLICY"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AccountNotFound")
}

// TestOrganizations_ResponsibilityTransfer expects Invite to create a REQUESTED
// transfer and a handshake, Update to rename it, the outbound list to hold it
// and the inbound list not, and Terminate to settle it WITHDRAWN.
func TestOrganizations_ResponsibilityTransfer(t *testing.T) {
	c := orgClient()

	inv, err := c.InviteOrganizationToTransferResponsibility(ctx, &organizations.InviteOrganizationToTransferResponsibilityInput{
		Type: orgtypes.ResponsibilityTransferTypeBilling,
		Target: &orgtypes.HandshakeParty{
			Id:   aws.String("o-target99999"),
			Type: orgtypes.HandshakePartyTypeOrganization,
		},
		SourceName:     aws.String("billing-handoff"),
		StartTimestamp: aws.Time(time.Now()),
	})
	require.NoError(t, err)
	require.NotNil(t, inv.Handshake)
	assert.True(t, strings.HasPrefix(aws.ToString(inv.Handshake.Id), "h-"))

	outbound, err := c.ListOutboundResponsibilityTransfers(ctx, &organizations.ListOutboundResponsibilityTransfersInput{
		Type: orgtypes.ResponsibilityTransferTypeBilling,
	})
	require.NoError(t, err)
	require.NotEmpty(t, outbound.ResponsibilityTransfers)
	var id string
	for _, rt := range outbound.ResponsibilityTransfers {
		if aws.ToString(rt.Name) == "billing-handoff" {
			id = aws.ToString(rt.Id)
			assert.Equal(t, orgtypes.ResponsibilityTransferStatusRequested, rt.Status)
			assert.Equal(t, orgtypes.ResponsibilityTransferTypeBilling, rt.Type)
			require.NotNil(t, rt.Source)
			assert.NotEmpty(t, aws.ToString(rt.Source.ManagementAccountId))
		}
	}
	require.NotEmpty(t, id, "created transfer should appear in the outbound list")
	defer func() {
		_, _ = c.TerminateResponsibilityTransfer(ctx, &organizations.TerminateResponsibilityTransferInput{Id: aws.String(id)})
	}()

	desc, err := c.DescribeResponsibilityTransfer(ctx, &organizations.DescribeResponsibilityTransferInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(aws.ToString(desc.ResponsibilityTransfer.Arn), "arn:aws:organizations::"))
	assert.Equal(t, aws.ToString(inv.Handshake.Id), aws.ToString(desc.ResponsibilityTransfer.ActiveHandshakeId))

	upd, err := c.UpdateResponsibilityTransfer(ctx, &organizations.UpdateResponsibilityTransferInput{
		Id:   aws.String(id),
		Name: aws.String("billing-handoff-renamed"),
	})
	require.NoError(t, err)
	assert.Equal(t, "billing-handoff-renamed", aws.ToString(upd.ResponsibilityTransfer.Name))

	inbound, err := c.ListInboundResponsibilityTransfers(ctx, &organizations.ListInboundResponsibilityTransfersInput{
		Type: orgtypes.ResponsibilityTransferTypeBilling,
	})
	require.NoError(t, err)
	for _, rt := range inbound.ResponsibilityTransfers {
		assert.NotEqual(t, id, aws.ToString(rt.Id), "outbound transfer must not appear inbound")
	}

	// A second terminate is rejected.
	term, err := c.TerminateResponsibilityTransfer(ctx, &organizations.TerminateResponsibilityTransferInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.ResponsibilityTransferStatusWithdrawn, term.ResponsibilityTransfer.Status)
	assert.NotNil(t, term.ResponsibilityTransfer.EndTimestamp)

	_, err = c.TerminateResponsibilityTransfer(ctx, &organizations.TerminateResponsibilityTransferInput{Id: aws.String(id)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AlreadyInStatus")
}
