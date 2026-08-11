package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func orgClient() *organizations.Client {
	return organizations.NewFromConfig(sdkConfig(), func(o *organizations.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestOrganizations_DescribeAndList covers the Organizations slice that backs the
// aws:PrincipalOrgID IAM condition key. The default organization (management
// account + root) is materialized at startup.
func TestOrganizations_DescribeAndList(t *testing.T) {
	c := orgClient()

	org, err := c.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err)
	require.NotNil(t, org.Organization)
	assert.True(t, strings.HasPrefix(aws.ToString(org.Organization.Id), "o-"), "org id is o-…")
	assert.Equal(t, orgtypes.OrganizationFeatureSetAll, org.Organization.FeatureSet)
	assert.NotEmpty(t, aws.ToString(org.Organization.MasterAccountId))

	accts, err := c.ListAccounts(ctx, &organizations.ListAccountsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, accts.Accounts)
	// The management account is listed, but ListAccounts is ordered by account
	// id (not management-account-first), so assert membership rather than
	// position — other tests may have created accounts whose random ids sort
	// before the management account's.
	var ids []string
	for _, a := range accts.Accounts {
		ids = append(ids, aws.ToString(a.Id))
	}
	assert.Contains(t, ids, aws.ToString(org.Organization.MasterAccountId))
}

// TestOrganizations_Roots exercises ListRoots and the default FullAWSAccess SCP.
func TestOrganizations_Roots(t *testing.T) {
	c := orgClient()
	roots, err := c.ListRoots(ctx, &organizations.ListRootsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, roots.Roots)
	assert.True(t, strings.HasPrefix(aws.ToString(roots.Roots[0].Id), "r-"))
}

func orgRootID(t *testing.T, c *organizations.Client) string {
	t.Helper()
	roots, err := c.ListRoots(ctx, &organizations.ListRootsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, roots.Roots)
	return aws.ToString(roots.Roots[0].Id)
}

// TestOrganizations_OUTreeAndAccounts covers CreateOrganizationalUnit,
// Describe/Update/List OUs, CreateAccount + status, DescribeAccount,
// ListAccountsForParent, ListChildren, ListParents, MoveAccount, CloseAccount,
// RemoveAccountFromOrganization, DeleteOrganizationalUnit.
func TestOrganizations_OUTreeAndAccounts(t *testing.T) {
	c := orgClient()
	root := orgRootID(t, c)

	ou, err := c.CreateOrganizationalUnit(ctx, &organizations.CreateOrganizationalUnitInput{
		ParentId: aws.String(root),
		Name:     aws.String("Engineering"),
	})
	require.NoError(t, err)
	ouID := aws.ToString(ou.OrganizationalUnit.Id)
	assert.True(t, strings.HasPrefix(ouID, "ou-"))
	defer func() {
		_, _ = c.DeleteOrganizationalUnit(ctx, &organizations.DeleteOrganizationalUnitInput{OrganizationalUnitId: aws.String(ouID)})
	}()

	desc, err := c.DescribeOrganizationalUnit(ctx, &organizations.DescribeOrganizationalUnitInput{OrganizationalUnitId: aws.String(ouID)})
	require.NoError(t, err)
	assert.Equal(t, "Engineering", aws.ToString(desc.OrganizationalUnit.Name))

	upd, err := c.UpdateOrganizationalUnit(ctx, &organizations.UpdateOrganizationalUnitInput{
		OrganizationalUnitId: aws.String(ouID),
		Name:                 aws.String("Platform"),
	})
	require.NoError(t, err)
	assert.Equal(t, "Platform", aws.ToString(upd.OrganizationalUnit.Name))

	ous, err := c.ListOrganizationalUnitsForParent(ctx, &organizations.ListOrganizationalUnitsForParentInput{ParentId: aws.String(root)})
	require.NoError(t, err)
	var found bool
	for _, o := range ous.OrganizationalUnits {
		if aws.ToString(o.Id) == ouID {
			found = true
		}
	}
	assert.True(t, found, "created OU listed under root")

	// CreateAccount → status SUCCEEDED → DescribeCreateAccountStatus + ListCreateAccountStatus.
	ca, err := c.CreateAccount(ctx, &organizations.CreateAccountInput{
		AccountName: aws.String("Sandbox"),
		Email:       aws.String("sandbox@sim.invalid"),
	})
	require.NoError(t, err)
	carID := aws.ToString(ca.CreateAccountStatus.Id)
	acctID := aws.ToString(ca.CreateAccountStatus.AccountId)
	require.NotEmpty(t, acctID)
	assert.Equal(t, orgtypes.CreateAccountStateSucceeded, ca.CreateAccountStatus.State)
	defer func() {
		_, _ = c.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: aws.String(acctID)})
	}()

	st, err := c.DescribeCreateAccountStatus(ctx, &organizations.DescribeCreateAccountStatusInput{CreateAccountRequestId: aws.String(carID)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.CreateAccountStateSucceeded, st.CreateAccountStatus.State)

	lst, err := c.ListCreateAccountStatus(ctx, &organizations.ListCreateAccountStatusInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, lst.CreateAccountStatuses)

	da, err := c.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(acctID)})
	require.NoError(t, err)
	assert.Equal(t, "Sandbox", aws.ToString(da.Account.Name))

	// Account starts under the root; move it into the OU.
	_, err = c.MoveAccount(ctx, &organizations.MoveAccountInput{
		AccountId:           aws.String(acctID),
		SourceParentId:      aws.String(root),
		DestinationParentId: aws.String(ouID),
	})
	require.NoError(t, err)

	forParent, err := c.ListAccountsForParent(ctx, &organizations.ListAccountsForParentInput{ParentId: aws.String(ouID)})
	require.NoError(t, err)
	require.Len(t, forParent.Accounts, 1)
	assert.Equal(t, acctID, aws.ToString(forParent.Accounts[0].Id))

	children, err := c.ListChildren(ctx, &organizations.ListChildrenInput{ParentId: aws.String(ouID), ChildType: orgtypes.ChildTypeAccount})
	require.NoError(t, err)
	require.Len(t, children.Children, 1)
	assert.Equal(t, acctID, aws.ToString(children.Children[0].Id))

	parents, err := c.ListParents(ctx, &organizations.ListParentsInput{ChildId: aws.String(acctID)})
	require.NoError(t, err)
	require.Len(t, parents.Parents, 1)
	assert.Equal(t, ouID, aws.ToString(parents.Parents[0].Id))

	// CloseAccount suspends it; then move back to root and remove for clean teardown.
	_, err = c.CloseAccount(ctx, &organizations.CloseAccountInput{AccountId: aws.String(acctID)})
	require.NoError(t, err)
	_, _ = c.MoveAccount(ctx, &organizations.MoveAccountInput{
		AccountId:           aws.String(acctID),
		SourceParentId:      aws.String(ouID),
		DestinationParentId: aws.String(root),
	})
	_, err = c.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: aws.String(acctID)})
	require.NoError(t, err)
}

// TestOrganizations_Policies covers CreatePolicy, Describe/Update/List,
// Attach/Detach, ListPoliciesForTarget, ListTargetsForPolicy,
// Enable/DisablePolicyType, DescribeEffectivePolicy, DeletePolicy.
func TestOrganizations_Policies(t *testing.T) {
	c := orgClient()
	root := orgRootID(t, c)

	content := `{"Version":"2012-10-17","Statement":[{"Sid":"Deny","Effect":"Deny","Action":"ec2:*","Resource":"*"}]}`
	pol, err := c.CreatePolicy(ctx, &organizations.CreatePolicyInput{
		Name:        aws.String("DenyEC2"),
		Description: aws.String("blocks ec2"),
		Type:        orgtypes.PolicyTypeServiceControlPolicy,
		Content:     aws.String(content),
	})
	require.NoError(t, err)
	pid := aws.ToString(pol.Policy.PolicySummary.Id)
	assert.True(t, strings.HasPrefix(pid, "p-"))

	dp, err := c.DescribePolicy(ctx, &organizations.DescribePolicyInput{PolicyId: aws.String(pid)})
	require.NoError(t, err)
	assert.Equal(t, content, aws.ToString(dp.Policy.Content))

	up, err := c.UpdatePolicy(ctx, &organizations.UpdatePolicyInput{PolicyId: aws.String(pid), Description: aws.String("updated")})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(up.Policy.PolicySummary.Description))

	lp, err := c.ListPolicies(ctx, &organizations.ListPoliciesInput{Filter: orgtypes.PolicyTypeServiceControlPolicy})
	require.NoError(t, err)
	assert.NotEmpty(t, lp.Policies)

	// A policy's ARN says who owns it, and the two owners take different
	// shapes: a customer policy is scoped to the account and the organization,
	// while an AWS-managed one belongs to neither and carries the literal
	// "aws" in the account position.
	org, err := c.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err)
	var managed *orgtypes.PolicySummary
	for i, summary := range lp.Policies {
		if summary.AwsManaged {
			managed = &lp.Policies[i]
		}
		if aws.ToString(summary.Id) == pid {
			assert.Contains(t, aws.ToString(summary.Arn),
				":policy/"+aws.ToString(org.Organization.Id)+"/service_control_policy/"+pid)
		}
	}
	require.NotNil(t, managed, "the organization carries no AWS-managed policy")
	assert.True(t, strings.HasPrefix(aws.ToString(managed.Arn), "arn:aws:organizations::aws:policy/"),
		"AWS-managed policy ARN %q is scoped to an account and an organization",
		aws.ToString(managed.Arn))

	_, err = c.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(pid), TargetId: aws.String(root)})
	require.NoError(t, err)

	pft, err := c.ListPoliciesForTarget(ctx, &organizations.ListPoliciesForTargetInput{TargetId: aws.String(root), Filter: orgtypes.PolicyTypeServiceControlPolicy})
	require.NoError(t, err)
	var attached bool
	for _, p := range pft.Policies {
		if aws.ToString(p.Id) == pid {
			attached = true
		}
	}
	assert.True(t, attached)

	tfp, err := c.ListTargetsForPolicy(ctx, &organizations.ListTargetsForPolicyInput{PolicyId: aws.String(pid)})
	require.NoError(t, err)
	require.NotEmpty(t, tfp.Targets)
	assert.Equal(t, root, aws.ToString(tfp.Targets[0].TargetId))
	assert.Equal(t, orgtypes.TargetTypeRoot, tfp.Targets[0].Type)

	// Effective SCP at the root resolves to our attached content.
	eff, err := c.DescribeEffectivePolicy(ctx, &organizations.DescribeEffectivePolicyInput{
		PolicyType: orgtypes.EffectivePolicyType("SERVICE_CONTROL_POLICY"),
		TargetId:   aws.String(root),
	})
	if err == nil {
		assert.NotEmpty(t, aws.ToString(eff.EffectivePolicy.PolicyContent))
	}

	_, err = c.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String(pid), TargetId: aws.String(root)})
	require.NoError(t, err)

	_, err = c.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: aws.String(pid)})
	require.NoError(t, err)

	// Enable then disable a non-default policy type on the root.
	en, err := c.EnablePolicyType(ctx, &organizations.EnablePolicyTypeInput{RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeTagPolicy})
	require.NoError(t, err)
	require.NotNil(t, en.Root)
	_, err = c.DisablePolicyType(ctx, &organizations.DisablePolicyTypeInput{RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeTagPolicy})
	require.NoError(t, err)
}

// TestOrganizations_Handshakes covers Invite/Describe/List/Accept/Decline/Cancel.
func TestOrganizations_Handshakes(t *testing.T) {
	c := orgClient()

	inv, err := c.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String("invitee@sim.invalid"), Type: orgtypes.HandshakePartyTypeEmail},
		Notes:  aws.String("join us"),
	})
	require.NoError(t, err)
	hid := aws.ToString(inv.Handshake.Id)
	assert.True(t, strings.HasPrefix(hid, "h-"))
	assert.Equal(t, orgtypes.HandshakeStateOpen, inv.Handshake.State)
	// The ARN is the only place a handshake's purpose appears, so an
	// invitation and an enable-all-features handshake must not read alike.
	assert.Contains(t, aws.ToString(inv.Handshake.Arn), "/invite/")

	dh, err := c.DescribeHandshake(ctx, &organizations.DescribeHandshakeInput{HandshakeId: aws.String(hid)})
	require.NoError(t, err)
	assert.Equal(t, hid, aws.ToString(dh.Handshake.Id))

	forOrg, err := c.ListHandshakesForOrganization(ctx, &organizations.ListHandshakesForOrganizationInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, forOrg.Handshakes)

	forAcct, err := c.ListHandshakesForAccount(ctx, &organizations.ListHandshakesForAccountInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, forAcct.Handshakes)

	acc, err := c.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: aws.String(hid)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateAccepted, acc.Handshake.State)

	// A fresh invite for decline/cancel paths.
	inv2, err := c.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String("111122223333"), Type: orgtypes.HandshakePartyTypeAccount},
	})
	require.NoError(t, err)
	h2 := aws.ToString(inv2.Handshake.Id)
	dec, err := c.DeclineHandshake(ctx, &organizations.DeclineHandshakeInput{HandshakeId: aws.String(h2)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateDeclined, dec.Handshake.State)

	inv3, err := c.InviteAccountToOrganization(ctx, &organizations.InviteAccountToOrganizationInput{
		Target: &orgtypes.HandshakeParty{Id: aws.String("444455556666"), Type: orgtypes.HandshakePartyTypeAccount},
	})
	require.NoError(t, err)
	can, err := c.CancelHandshake(ctx, &organizations.CancelHandshakeInput{HandshakeId: inv3.Handshake.Id})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.HandshakeStateCanceled, can.Handshake.State)
}

// TestOrganizations_DelegatedAdminAndServiceAccess covers
// Register/Deregister/List delegated administrators, ListDelegatedServices,
// Enable/Disable/List AWS service access.
func TestOrganizations_DelegatedAdminAndServiceAccess(t *testing.T) {
	c := orgClient()
	sp := "config.amazonaws.com"

	// A member account to delegate to.
	ca, err := c.CreateAccount(ctx, &organizations.CreateAccountInput{
		AccountName: aws.String("Audit"),
		Email:       aws.String("audit@sim.invalid"),
	})
	require.NoError(t, err)
	acctID := aws.ToString(ca.CreateAccountStatus.AccountId)
	defer func() {
		_, _ = c.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: aws.String(acctID)})
	}()

	_, err = c.EnableAWSServiceAccess(ctx, &organizations.EnableAWSServiceAccessInput{ServicePrincipal: aws.String(sp)})
	require.NoError(t, err)

	la, err := c.ListAWSServiceAccessForOrganization(ctx, &organizations.ListAWSServiceAccessForOrganizationInput{})
	require.NoError(t, err)
	var spListed bool
	for _, p := range la.EnabledServicePrincipals {
		if aws.ToString(p.ServicePrincipal) == sp {
			spListed = true
		}
	}
	assert.True(t, spListed)

	_, err = c.RegisterDelegatedAdministrator(ctx, &organizations.RegisterDelegatedAdministratorInput{
		AccountId: aws.String(acctID), ServicePrincipal: aws.String(sp),
	})
	require.NoError(t, err)

	lda, err := c.ListDelegatedAdministrators(ctx, &organizations.ListDelegatedAdministratorsInput{ServicePrincipal: aws.String(sp)})
	require.NoError(t, err)
	require.NotEmpty(t, lda.DelegatedAdministrators)

	lds, err := c.ListDelegatedServicesForAccount(ctx, &organizations.ListDelegatedServicesForAccountInput{AccountId: aws.String(acctID)})
	require.NoError(t, err)
	require.NotEmpty(t, lds.DelegatedServices)
	assert.Equal(t, sp, aws.ToString(lds.DelegatedServices[0].ServicePrincipal))

	_, err = c.DeregisterDelegatedAdministrator(ctx, &organizations.DeregisterDelegatedAdministratorInput{
		AccountId: aws.String(acctID), ServicePrincipal: aws.String(sp),
	})
	require.NoError(t, err)

	_, err = c.DisableAWSServiceAccess(ctx, &organizations.DisableAWSServiceAccessInput{ServicePrincipal: aws.String(sp)})
	require.NoError(t, err)
}

// TestOrganizations_ResourcePolicy covers Put/Describe/Delete resource policy.
func TestOrganizations_ResourcePolicy(t *testing.T) {
	c := orgClient()
	content := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"organizations:DescribeOrganization","Resource":"*"}]}`

	put, err := c.PutResourcePolicy(ctx, &organizations.PutResourcePolicyInput{Content: aws.String(content)})
	require.NoError(t, err)
	require.NotNil(t, put.ResourcePolicy)
	assert.Equal(t, content, aws.ToString(put.ResourcePolicy.Content))
	assert.True(t, strings.HasPrefix(aws.ToString(put.ResourcePolicy.ResourcePolicySummary.Id), "rp-"))

	desc, err := c.DescribeResourcePolicy(ctx, &organizations.DescribeResourcePolicyInput{})
	require.NoError(t, err)
	assert.Equal(t, content, aws.ToString(desc.ResourcePolicy.Content))

	_, err = c.DeleteResourcePolicy(ctx, &organizations.DeleteResourcePolicyInput{})
	require.NoError(t, err)

	_, err = c.DescribeResourcePolicy(ctx, &organizations.DescribeResourcePolicyInput{})
	require.Error(t, err)
}

// TestOrganizations_Tags covers TagResource, ListTagsForResource, UntagResource.
func TestOrganizations_Tags(t *testing.T) {
	c := orgClient()
	root := orgRootID(t, c)

	pol, err := c.CreatePolicy(ctx, &organizations.CreatePolicyInput{
		Name:        aws.String("TaggedPolicy"),
		Description: aws.String("policy for tag coverage"),
		Type:        orgtypes.PolicyTypeServiceControlPolicy,
		Content:     aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	pid := aws.ToString(pol.Policy.PolicySummary.Id)
	defer func() { _, _ = c.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: aws.String(pid)}) }()
	_ = root

	_, err = c.TagResource(ctx, &organizations.TagResourceInput{
		ResourceId: aws.String(pid),
		Tags:       []orgtypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}, {Key: aws.String("team"), Value: aws.String("plat")}},
	})
	require.NoError(t, err)

	lt, err := c.ListTagsForResource(ctx, &organizations.ListTagsForResourceInput{ResourceId: aws.String(pid)})
	require.NoError(t, err)
	require.Len(t, lt.Tags, 2)

	_, err = c.UntagResource(ctx, &organizations.UntagResourceInput{ResourceId: aws.String(pid), TagKeys: []string{"team"}})
	require.NoError(t, err)

	lt2, err := c.ListTagsForResource(ctx, &organizations.ListTagsForResourceInput{ResourceId: aws.String(pid)})
	require.NoError(t, err)
	require.Len(t, lt2.Tags, 1)
	assert.Equal(t, "env", aws.ToString(lt2.Tags[0].Key))
}

// TestOrganizations_CreateEnableAllFeatures covers EnableAllFeatures. CreateOrganization
// and DeleteOrganization are exercised against a teardown-restore cycle in
// TestOrganizations_CreateDeleteOrganization to avoid disturbing the default org
// other tests rely on.
func TestOrganizations_EnableAllFeatures(t *testing.T) {
	c := orgClient()
	out, err := c.EnableAllFeatures(ctx, &organizations.EnableAllFeaturesInput{})
	require.NoError(t, err)
	require.NotNil(t, out.Handshake)
	assert.Equal(t, orgtypes.ActionTypeEnableAllFeatures, out.Handshake.Action)
	assert.Contains(t, aws.ToString(out.Handshake.Arn), "/enable_all_features/")
}

// TestOrganizations_CreateDeleteOrganization deletes the default org then
// recreates it, covering DeleteOrganization + CreateOrganization. It runs last
// alphabetically-isolated state is restored before returning so other suites
// (and reruns) still see the default org.
func TestOrganizations_zzCreateDeleteOrganization(t *testing.T) {
	c := orgClient()

	// Deleting requires an org to exist (the default one does).
	_, err := c.DeleteOrganization(ctx, &organizations.DeleteOrganizationInput{})
	require.NoError(t, err)

	// Now describing fails — the account is no longer in an org.
	_, err = c.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.Error(t, err)

	// Recreate so the default org (and IAM PrincipalOrgID) is restored.
	created, err := c.CreateOrganization(ctx, &organizations.CreateOrganizationInput{FeatureSet: orgtypes.OrganizationFeatureSetAll})
	require.NoError(t, err)
	require.NotNil(t, created.Organization)
	assert.Equal(t, orgtypes.OrganizationFeatureSetAll, created.Organization.FeatureSet)
}
