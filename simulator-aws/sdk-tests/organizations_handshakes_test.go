package aws_sdk_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// standaloneAccount creates a member account, has it leave the organization,
// and returns its ID, its owner's email address and an administrator's
// configuration in it.
func standaloneAccount(t *testing.T, prefix string) (string, string, aws.Config) {
	t.Helper()
	account := createMemberAccount(t, prefix, "")
	described, err := orgClient().DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(account)})
	require.NoError(t, err)
	cfg := sessionConfig(memberAdmin(t, account, "OrganizationAccountAccessRole"))
	_, err = orgWith(cfg).LeaveOrganization(ctx, &organizations.LeaveOrganizationInput{})
	require.NoError(t, err)
	return account, aws.ToString(described.Account.Email), cfg
}

func assertHandshakeConstraint(t *testing.T, err error, reason orgtypes.HandshakeConstraintViolationExceptionReason) {
	t.Helper()
	var violation *orgtypes.HandshakeConstraintViolationException
	require.ErrorAs(t, err, &violation)
	assert.Equal(t, reason, violation.Reason)
}

func handshakeIDs(hs []orgtypes.Handshake) []string {
	ids := []string{}
	for _, h := range hs {
		ids = append(ids, aws.ToString(h.Id))
	}
	return ids
}

// TestOrganizations_InvitationJoinsAccount invites a standalone account,
// which alone may accept the invitation and joins the organization's root
// when it does.
func TestOrganizations_InvitationJoinsAccount(t *testing.T) {
	t.Parallel()
	mgmt := orgClient()
	invitee, _, inviteeCfg := standaloneAccount(t, "invitee")
	invitee2 := orgWith(inviteeCfg)
	bystander := orgWith(sessionConfig(memberAdmin(t, createMemberAccount(t, "bystander", ""), "OrganizationAccountAccessRole")))

	inv, err := mgmt.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String(invitee), Type: orgtypes.HandshakePartyTypeAccount},
		Notes:  aws.String("join us"),
	})
	require.NoError(t, err)
	h := inv.Handshake
	hid := aws.ToString(h.Id)
	assert.Equal(t, orgtypes.HandshakeStateOpen, h.State)
	assert.Equal(t, orgtypes.ActionTypeInviteAccountToOrganization, h.Action)
	assert.Contains(t, aws.ToString(h.Arn), "/invite/"+hid)
	require.Len(t, h.Parties, 2)
	assert.Equal(t, orgtypes.HandshakePartyTypeOrganization, h.Parties[0].Type)
	assert.Equal(t, invitee, aws.ToString(h.Parties[1].Id))
	require.Len(t, h.Resources, 3)
	assert.Equal(t, orgtypes.HandshakeResourceTypeOrganization, h.Resources[0].Type)
	require.Len(t, h.Resources[0].Resources, 2)
	assert.Equal(t, orgtypes.HandshakeResourceTypeMasterEmail, h.Resources[0].Resources[0].Type)
	assert.Equal(t, "management@sim.invalid", aws.ToString(h.Resources[0].Resources[0].Value))
	assert.Equal(t, orgtypes.HandshakeResourceTypeAccount, h.Resources[1].Type)
	assert.Equal(t, invitee, aws.ToString(h.Resources[1].Value))
	assert.Equal(t, orgtypes.HandshakeResourceTypeNotes, h.Resources[2].Type)
	assert.Equal(t, "join us", aws.ToString(h.Resources[2].Value))

	_, err = mgmt.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String(invitee), Type: orgtypes.HandshakePartyTypeAccount},
	})
	assert.Equal(t, "DuplicateHandshakeException", errCodeOf(err))

	_, err = mgmt.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: aws.String(hid)})
	assertOrgAccessDenied(t, err, "the sender cannot accept its own invitation")
	_, err = invitee2.CancelHandshake(ctx, &organizations.CancelHandshakeInput{HandshakeId: aws.String(hid)})
	assertOrgAccessDenied(t, err, "the recipient cannot cancel an invitation")
	_, err = bystander.DescribeHandshake(ctx, &organizations.DescribeHandshakeInput{HandshakeId: aws.String(hid)})
	assert.Equal(t, "HandshakeNotFoundException", errCodeOf(err), "an account that is no party sees no handshake")
	_, err = bystander.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: aws.String(hid)})
	assert.Equal(t, "HandshakeNotFoundException", errCodeOf(err))

	received, err := invitee2.ListHandshakesForAccount(ctx, &organizations.ListHandshakesForAccountInput{})
	require.NoError(t, err)
	assert.Equal(t, []string{hid}, handshakeIDs(received.Handshakes))
	described, err := invitee2.DescribeHandshake(ctx, &organizations.DescribeHandshakeInput{HandshakeId: aws.String(hid)})
	require.NoError(t, err)
	assert.Equal(t, hid, aws.ToString(described.Handshake.Id))
	others, err := bystander.ListHandshakesForAccount(ctx, &organizations.ListHandshakesForAccountInput{})
	require.NoError(t, err)
	assert.NotContains(t, handshakeIDs(others.Handshakes), hid)
	ownReceived, err := mgmt.ListHandshakesForAccount(ctx, &organizations.ListHandshakesForAccountInput{})
	require.NoError(t, err)
	assert.NotContains(t, handshakeIDs(ownReceived.Handshakes), hid, "the management account sent the invitation")

	sent, err := mgmt.ListHandshakesForOrganization(ctx, &organizations.ListHandshakesForOrganizationInput{
		Filter: &orgtypes.HandshakeFilter{ActionType: orgtypes.ActionTypeInviteAccountToOrganization}})
	require.NoError(t, err)
	assert.Contains(t, handshakeIDs(sent.Handshakes), hid)
	featureRequests, err := mgmt.ListHandshakesForOrganization(ctx, &organizations.ListHandshakesForOrganizationInput{
		Filter: &orgtypes.HandshakeFilter{ActionType: orgtypes.ActionTypeEnableAllFeatures}})
	require.NoError(t, err)
	assert.NotContains(t, handshakeIDs(featureRequests.Handshakes), hid)

	accepted, err := invitee2.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: aws.String(hid)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateAccepted, accepted.Handshake.State)
	joined, err := mgmt.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(invitee)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.AccountJoinedMethodInvited, joined.Account.JoinedMethod)
	assert.Equal(t, orgtypes.AccountStatusActive, joined.Account.Status)
	parents, err := mgmt.ListParents(ctx, &organizations.ListParentsInput{ChildId: aws.String(invitee)})
	require.NoError(t, err)
	require.Len(t, parents.Parents, 1)
	assert.Equal(t, orgtypes.ParentTypeRoot, parents.Parents[0].Type)
	policies, err := mgmt.ListPoliciesForTarget(ctx, &organizations.ListPoliciesForTargetInput{
		TargetId: aws.String(invitee), Filter: orgtypes.PolicyTypeServiceControlPolicy})
	require.NoError(t, err)
	assert.True(t, slices.ContainsFunc(policies.Policies, func(p orgtypes.PolicySummary) bool { return aws.ToString(p.Name) == "FullAWSAccess" }))
	org, err := invitee2.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err, "the invited account belongs to the organization")
	assert.Equal(t, "123456789012", aws.ToString(org.Organization.MasterAccountId))

	_, err = invitee2.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: aws.String(hid)})
	assert.Equal(t, "HandshakeAlreadyInStateException", errCodeOf(err))
	_, err = invitee2.DeclineHandshake(ctx, &organizations.DeclineHandshakeInput{HandshakeId: aws.String(hid)})
	assert.Equal(t, "InvalidHandshakeTransitionException", errCodeOf(err))

	_, err = mgmt.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String(invitee), Type: orgtypes.HandshakePartyTypeAccount},
	})
	var violation *orgtypes.HandshakeConstraintViolationException
	require.True(t, errors.As(err, &violation), "got %v", err)
	assert.Equal(t, orgtypes.HandshakeConstraintViolationExceptionReasonAlreadyInAnOrganization, violation.Reason)
}

// TestOrganizations_InvitationDeclinedAndCanceled declines an invitation
// addressed to the owner's email address from the invited account, and
// cancels another from the management account.
func TestOrganizations_InvitationDeclinedAndCanceled(t *testing.T) {
	t.Parallel()
	mgmt := orgClient()
	invitee, email, cfg := standaloneAccount(t, "decliner")
	invitee2 := orgWith(cfg)

	inv, err := mgmt.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String(email), Type: orgtypes.HandshakePartyTypeEmail},
	})
	require.NoError(t, err)
	hid := inv.Handshake.Id
	received, err := invitee2.ListHandshakesForAccount(ctx, &organizations.ListHandshakesForAccountInput{})
	require.NoError(t, err)
	assert.Contains(t, handshakeIDs(received.Handshakes), aws.ToString(hid), "an invitation to the owner's email address reaches the account")
	declined, err := invitee2.DeclineHandshake(ctx, &organizations.DeclineHandshakeInput{HandshakeId: hid})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateDeclined, declined.Handshake.State)
	_, err = mgmt.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(invitee)})
	assert.Equal(t, "AccountNotFoundException", errCodeOf(err), "a declined invitation joins nothing")
	_, err = mgmt.CancelHandshake(ctx, &organizations.CancelHandshakeInput{HandshakeId: hid})
	assert.Equal(t, "InvalidHandshakeTransitionException", errCodeOf(err))

	again, err := mgmt.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String(invitee), Type: orgtypes.HandshakePartyTypeAccount},
	})
	require.NoError(t, err, "a declined invitation is no duplicate")
	canceled, err := mgmt.CancelHandshake(ctx, &organizations.CancelHandshakeInput{HandshakeId: again.Handshake.Id})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateCanceled, canceled.Handshake.State)
	_, err = mgmt.CancelHandshake(ctx, &organizations.CancelHandshakeInput{HandshakeId: again.Handshake.Id})
	assert.Equal(t, "HandshakeAlreadyInStateException", errCodeOf(err))
	_, err = invitee2.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: again.Handshake.Id})
	assert.Equal(t, "InvalidHandshakeTransitionException", errCodeOf(err), "a canceled invitation can no longer be answered")

	_, err = mgmt.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String("o-exampleorgid"), Type: orgtypes.HandshakePartyTypeOrganization},
	})
	assert.Equal(t, "InvalidInputException", errCodeOf(err))
}

func parentHandshake(h orgtypes.Handshake) string {
	for _, r := range h.Resources {
		if r.Type == orgtypes.HandshakeResourceTypeParentHandshake {
			return aws.ToString(r.Value)
		}
	}
	return ""
}

// migrateToAllFeatures takes the account's organization, which has
// consolidated billing alone, through the User Guide's standard migration: an
// invited account approves, and the management account finalizes.
func migrateToAllFeatures(t *testing.T) {
	t.Helper()
	mgmt := orgClient()
	invite := func(account string) string {
		out, err := mgmt.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
			Target: &orgtypes.HandshakeParty{Id: aws.String(account), Type: orgtypes.HandshakePartyTypeAccount}})
		require.NoError(t, err)
		return aws.ToString(out.Handshake.Id)
	}
	invited, _, invitedCfg := standaloneAccount(t, "approver")
	member := orgWith(invitedCfg)
	_, err := member.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: aws.String(invite(invited))})
	require.NoError(t, err)
	createMemberAccount(t, "created", "")
	pending, _, _ := standaloneAccount(t, "pending")
	unanswered := invite(pending)
	roots, err := mgmt.ListRoots(ctx, &organizations.ListRootsInput{})
	require.NoError(t, err)
	require.Len(t, roots.Roots, 1)
	root := roots.Roots[0].Id
	assert.Empty(t, roots.Roots[0].PolicyTypes, "SCPs aren't available with consolidated billing alone")
	enableSCPs := &organizations.EnablePolicyTypeInput{RootId: root, PolicyType: orgtypes.PolicyTypeServiceControlPolicy}
	_, err = mgmt.EnablePolicyType(ctx, enableSCPs)
	var unavailable *orgtypes.PolicyTypeNotAvailableForOrganizationException
	require.ErrorAs(t, err, &unavailable)

	started, err := mgmt.EnableAllFeatures(ctx, &organizations.EnableAllFeaturesInput{})
	require.NoError(t, err)
	parent := started.Handshake
	parentID := aws.ToString(parent.Id)
	assert.Equal(t, orgtypes.ActionTypeEnableAllFeatures, parent.Action)
	assert.Equal(t, orgtypes.HandshakeStateRequested, parent.State, "an invited account has yet to approve")
	assert.Contains(t, aws.ToString(parent.Arn), "/enable_all_features/"+parentID)
	assert.Equal(t, 90*24*time.Hour, aws.ToTime(parent.ExpirationTimestamp).Sub(aws.ToTime(parent.RequestedTimestamp)))

	canceled, err := mgmt.DescribeHandshake(ctx, &organizations.DescribeHandshakeInput{HandshakeId: aws.String(unanswered)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateCanceled, canceled.Handshake.State, "the migration cancels the open invitation")

	_, err = mgmt.EnableAllFeatures(ctx, &organizations.EnableAllFeaturesInput{})
	assertHandshakeConstraint(t, err, orgtypes.HandshakeConstraintViolationExceptionReasonOrganizationIsAlreadyPendingAllFeaturesMigration)
	_, err = mgmt.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: aws.String(parentID)})
	var transition *orgtypes.InvalidHandshakeTransitionException
	require.ErrorAs(t, err, &transition, "the management account finalizes only once every invited account approved")

	received, err := member.ListHandshakesForAccount(ctx, &organizations.ListHandshakesForAccountInput{
		Filter: &orgtypes.HandshakeFilter{ActionType: orgtypes.ActionTypeApproveAllFeatures}})
	require.NoError(t, err)
	require.Len(t, received.Handshakes, 1, "the created account receives no approval request")
	approval := received.Handshakes[0]
	assert.Equal(t, orgtypes.HandshakeStateOpen, approval.State)
	assert.Equal(t, parentID, parentHandshake(approval))
	require.Len(t, approval.Parties, 2)
	assert.Equal(t, invited, aws.ToString(approval.Parties[1].Id))

	children, err := mgmt.ListHandshakesForOrganization(ctx, &organizations.ListHandshakesForOrganizationInput{
		Filter: &orgtypes.HandshakeFilter{ParentHandshakeId: aws.String(parentID)}})
	require.NoError(t, err)
	assert.Equal(t, []string{aws.ToString(approval.Id)}, handshakeIDs(children.Handshakes))

	_, err = member.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: approval.Id})
	require.NoError(t, err)
	described, err := mgmt.DescribeHandshake(ctx, &organizations.DescribeHandshakeInput{HandshakeId: aws.String(parentID)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateOpen, described.Handshake.State, "the last approval opens the request to finalize")
	org, err := mgmt.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.OrganizationFeatureSetConsolidatedBilling, org.Organization.FeatureSet, "approval alone changes no feature set")

	finalized, err := mgmt.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: aws.String(parentID)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateAccepted, finalized.Handshake.State)
	org, err = mgmt.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.OrganizationFeatureSetAll, org.Organization.FeatureSet)
	_, err = mgmt.EnableAllFeatures(ctx, &organizations.EnableAllFeaturesInput{})
	assertHandshakeConstraint(t, err, orgtypes.HandshakeConstraintViolationExceptionReasonOrganizationAlreadyHasAllFeatures)
	// The default organization the other tests read enables SCPs.
	_, err = mgmt.EnablePolicyType(ctx, enableSCPs)
	require.NoError(t, err)
}

// TestOrganizations_HandshakeListingsPage pages the organization's handshakes
// one at a time through the SDK's paginator and refuses a page larger than
// the model allows.
func TestOrganizations_HandshakeListingsPage(t *testing.T) {
	t.Parallel()
	mgmt := orgClient()
	sent := []string{}
	for range 3 {
		account, _, _ := standaloneAccount(t, "paged")
		out, err := mgmt.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
			Target: &orgtypes.HandshakeParty{Id: aws.String(account), Type: orgtypes.HandshakePartyTypeAccount}})
		require.NoError(t, err)
		sent = append(sent, aws.ToString(out.Handshake.Id))
	}
	listed := []string{}
	pages := 0
	paginator := organizations.NewListHandshakesForOrganizationPaginator(mgmt, &organizations.ListHandshakesForOrganizationInput{
		MaxResults: aws.Int32(1), Filter: &orgtypes.HandshakeFilter{ActionType: orgtypes.ActionTypeInviteAccountToOrganization}})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Handshakes), 1)
		listed = append(listed, handshakeIDs(page.Handshakes)...)
		pages++
	}
	assert.Subset(t, listed, sent)
	assert.Equal(t, len(listed), pages, "every page holds one handshake")
	assert.Len(t, slices.Compact(slices.Sorted(slices.Values(listed))), len(listed), "no handshake repeats across pages")

	_, err := mgmt.ListHandshakesForOrganization(ctx, &organizations.ListHandshakesForOrganizationInput{MaxResults: aws.Int32(21)})
	var invalid *orgtypes.InvalidInputException
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, orgtypes.InvalidInputExceptionReasonMaxValueExceeded, invalid.Reason)
	_, err = mgmt.ListHandshakesForOrganization(ctx, &organizations.ListHandshakesForOrganizationInput{NextToken: aws.String("not-a-token")})
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, orgtypes.InvalidInputExceptionReasonInvalidPaginationToken, invalid.Reason)
}
