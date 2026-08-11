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

// TestOrganizations_GovCloudAccount covers CreateGovCloudAccount: the linked
// commercial + GovCloud account pair, the CreateAccountStatus that settles to
// SUCCEEDED carrying both AccountId and GovCloudAccountId, and the status being
// readable via DescribeCreateAccountStatus.
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

	// The status is durable and readable by id.
	desc, err := c.DescribeCreateAccountStatus(ctx, &organizations.DescribeCreateAccountStatusInput{
		CreateAccountRequestId: st.Id,
	})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.CreateAccountStateSucceeded, desc.CreateAccountStatus.State)
	assert.Equal(t, aws.ToString(st.AccountId), aws.ToString(desc.CreateAccountStatus.AccountId))

	// The commercial account is a real member account in the org tree.
	acct, err := c.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: st.AccountId})
	require.NoError(t, err)
	assert.Equal(t, "GovCloudPair", aws.ToString(acct.Account.Name))
}

// TestOrganizations_LeaveOrganization asserts the management account cannot leave
// its own organization — the faithful outcome for the sim's caller, which is
// always the organization's management account.
func TestOrganizations_LeaveOrganization(t *testing.T) {
	c := orgClient()
	_, err := c.LeaveOrganization(ctx, &organizations.LeaveOrganizationInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MasterCannotLeaveOrganization")
}

// TestOrganizations_EffectivePolicyValidation covers the effective-policy
// validation reads: ListAccountsWithInvalidEffectivePolicy (honest-empty over the
// account store) and ListEffectivePolicyValidationErrors (honest-empty error list
// for a real account, with the account id / policy type / path echoed).
func TestOrganizations_EffectivePolicyValidation(t *testing.T) {
	c := orgClient()

	inv, err := c.ListAccountsWithInvalidEffectivePolicy(ctx, &organizations.ListAccountsWithInvalidEffectivePolicyInput{
		PolicyType: orgtypes.EffectivePolicyType("BACKUP_POLICY"),
	})
	require.NoError(t, err)
	assert.Empty(t, inv.Accounts)
	assert.Equal(t, "BACKUP_POLICY", string(inv.PolicyType))

	// Resolve the management account id to validate against a real account.
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

	// An unknown account is rejected like the real service.
	_, err = c.ListEffectivePolicyValidationErrors(ctx, &organizations.ListEffectivePolicyValidationErrorsInput{
		AccountId:  aws.String("999999999999"),
		PolicyType: orgtypes.EffectivePolicyType("BACKUP_POLICY"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AccountNotFound")
}

// TestOrganizations_ResponsibilityTransfer drives the responsibility-transfer
// family end to end: Invite creates a transfer (status REQUESTED) plus a
// handshake, Describe reads it, Update renames it, the outbound list enumerates
// it, the inbound list omits it, and Terminate settles it to WITHDRAWN.
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

	// Find the created transfer via the outbound list.
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

	// Describe reads the transfer.
	desc, err := c.DescribeResponsibilityTransfer(ctx, &organizations.DescribeResponsibilityTransferInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(aws.ToString(desc.ResponsibilityTransfer.Arn), "arn:aws:organizations::"))
	assert.Equal(t, aws.ToString(inv.Handshake.Id), aws.ToString(desc.ResponsibilityTransfer.ActiveHandshakeId))

	// Update renames it.
	upd, err := c.UpdateResponsibilityTransfer(ctx, &organizations.UpdateResponsibilityTransferInput{
		Id:   aws.String(id),
		Name: aws.String("billing-handoff-renamed"),
	})
	require.NoError(t, err)
	assert.Equal(t, "billing-handoff-renamed", aws.ToString(upd.ResponsibilityTransfer.Name))

	// The inbound list does not contain an outbound-direction transfer.
	inbound, err := c.ListInboundResponsibilityTransfers(ctx, &organizations.ListInboundResponsibilityTransfersInput{
		Type: orgtypes.ResponsibilityTransferTypeBilling,
	})
	require.NoError(t, err)
	for _, rt := range inbound.ResponsibilityTransfers {
		assert.NotEqual(t, id, aws.ToString(rt.Id), "outbound transfer must not appear inbound")
	}

	// Terminate settles it to WITHDRAWN; a second terminate is rejected.
	term, err := c.TerminateResponsibilityTransfer(ctx, &organizations.TerminateResponsibilityTransferInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.ResponsibilityTransferStatusWithdrawn, term.ResponsibilityTransfer.Status)
	assert.NotNil(t, term.ResponsibilityTransfer.EndTimestamp)

	_, err = c.TerminateResponsibilityTransfer(ctx, &organizations.TerminateResponsibilityTransferInput{Id: aws.String(id)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AlreadyInStatus")
}
