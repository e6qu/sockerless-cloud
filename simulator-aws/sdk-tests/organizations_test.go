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

// TestOrganizations_DescribeAndList covers the organization behind the
// aws:PrincipalOrgID IAM condition key; the management account and root exist
// from startup.
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
	// ListAccounts orders by account id, and other tests' accounts may sort before
	// the management account, so assert membership rather than position.
	var ids []string
	for _, a := range accts.Accounts {
		ids = append(ids, aws.ToString(a.Id))
	}
	assert.Contains(t, ids, aws.ToString(org.Organization.MasterAccountId))
}

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

	// An account starts under the root.
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

	// CloseAccount suspends the account; moving it back to the root and removing
	// it cleans up.
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

	// A policy's ARN names its owner: a customer policy is scoped to the account
	// and organization, while an AWS managed one carries "aws" in the account
	// position.
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

	// AWS evaluates an SCP as an intersection along the path rather than merging
	// it, so EffectivePolicyType omits SCPs and the request fails validation.
	_, err = c.DescribeEffectivePolicy(ctx, &organizations.DescribeEffectivePolicyInput{
		PolicyType: orgtypes.EffectivePolicyType("SERVICE_CONTROL_POLICY"),
		TargetId:   aws.String(root),
	})
	assert.Equal(t, "InvalidInputException", errCode(t, err),
		"SERVICE_CONTROL_POLICY is not an EffectivePolicyType")

	// A tag policy has an effective form, resolved from the content attached to
	// this target.
	tagContent := `{"tags":{"CostCenter":{"tag_key":{"@@assign":"CostCenter"}}}}`
	tagPolicy, err := c.CreatePolicy(ctx, &organizations.CreatePolicyInput{
		Name:        aws.String("TagCostCenter"),
		Description: aws.String("requires a cost centre tag key"),
		Type:        orgtypes.PolicyTypeTagPolicy,
		Content:     aws.String(tagContent),
	})
	require.NoError(t, err)
	tagPolicyID := aws.ToString(tagPolicy.Policy.PolicySummary.Id)

	// A policy governs a target only once its type is enabled in the root. SCPs
	// are enabled in every root; a tag policy is not until enabled, so attaching
	// one first is refused.
	_, err = c.AttachPolicy(ctx, &organizations.AttachPolicyInput{
		PolicyId: aws.String(tagPolicyID), TargetId: aws.String(root),
	})
	assert.Equal(t, "PolicyTypeNotEnabledException", errCode(t, err),
		"a tag policy cannot be attached before tag policies are enabled in the root")
	_, err = c.EnablePolicyType(ctx, &organizations.EnablePolicyTypeInput{
		RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeTagPolicy,
	})
	require.NoError(t, err)
	_, err = c.AttachPolicy(ctx, &organizations.AttachPolicyInput{
		PolicyId: aws.String(tagPolicyID), TargetId: aws.String(root),
	})
	require.NoError(t, err)

	eff, err := c.DescribeEffectivePolicy(ctx, &organizations.DescribeEffectivePolicyInput{
		PolicyType: orgtypes.EffectivePolicyTypeTagPolicy,
		TargetId:   aws.String(root),
	})
	require.NoError(t, err)
	require.NotNil(t, eff.EffectivePolicy)
	assert.JSONEq(t, tagContent, aws.ToString(eff.EffectivePolicy.PolicyContent),
		"the effective policy must be the content attached to this target")
	assert.Equal(t, root, aws.ToString(eff.EffectivePolicy.TargetId))
	assert.Equal(t, orgtypes.EffectivePolicyTypeTagPolicy, eff.EffectivePolicy.PolicyType)

	// Detached, the target has no effective tag policy, which the service reports
	// as its own error.
	_, err = c.DetachPolicy(ctx, &organizations.DetachPolicyInput{
		PolicyId: aws.String(tagPolicyID), TargetId: aws.String(root),
	})
	require.NoError(t, err)
	_, err = c.DescribeEffectivePolicy(ctx, &organizations.DescribeEffectivePolicyInput{
		PolicyType: orgtypes.EffectivePolicyTypeTagPolicy,
		TargetId:   aws.String(root),
	})
	assert.Equal(t, "EffectivePolicyNotFoundException", errCode(t, err))
	_, err = c.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: aws.String(tagPolicyID)})
	require.NoError(t, err)

	_, err = c.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String(pid), TargetId: aws.String(root)})
	require.NoError(t, err)

	_, err = c.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: aws.String(pid)})
	require.NoError(t, err)

	// Disabling the type takes it off the root.
	_, err = c.DisablePolicyType(ctx, &organizations.DisablePolicyTypeInput{RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeTagPolicy})
	require.NoError(t, err)
	roots, err := c.ListRoots(ctx, &organizations.ListRootsInput{})
	require.NoError(t, err)
	require.Len(t, roots.Roots, 1)
	for _, enabled := range roots.Roots[0].PolicyTypes {
		assert.NotEqual(t, orgtypes.PolicyTypeTagPolicy, enabled.Type,
			"the disabled policy type must be gone from the root")
	}
}

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
	// Only the ARN shows a handshake's purpose, so an invitation and an
	// enable-all-features handshake differ there.
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

func TestOrganizations_DelegatedAdminAndServiceAccess(t *testing.T) {
	c := orgClient()
	sp := "config.amazonaws.com"

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

// TestOrganizations_EnableAllFeatures covers EnableAllFeatures;
// TestOrganizations_zzCreateDeleteOrganization covers the organization's
// deletion and creation without disturbing the default one other tests use.
func TestOrganizations_EnableAllFeatures(t *testing.T) {
	c := orgClient()
	out, err := c.EnableAllFeatures(ctx, &organizations.EnableAllFeaturesInput{})
	require.NoError(t, err)
	require.NotNil(t, out.Handshake)
	assert.Equal(t, orgtypes.ActionTypeEnableAllFeatures, out.Handshake.Action)
	assert.Contains(t, aws.ToString(out.Handshake.Arn), "/enable_all_features/")
}

// TestOrganizations_zzCreateDeleteOrganization deletes the default
// organization and recreates it, restoring it for other suites and reruns; the
// name sorts it last.
func TestOrganizations_zzCreateDeleteOrganization(t *testing.T) {
	c := orgClient()

	_, err := c.DeleteOrganization(ctx, &organizations.DeleteOrganizationInput{})
	require.NoError(t, err)

	// The account belongs to no organization now.
	_, err = c.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.Error(t, err)

	// Recreate the default organization, which aws:PrincipalOrgID needs.
	created, err := c.CreateOrganization(ctx, &organizations.CreateOrganizationInput{FeatureSet: orgtypes.OrganizationFeatureSetAll})
	require.NoError(t, err)
	require.NotNil(t, created.Organization)
	assert.Equal(t, orgtypes.OrganizationFeatureSetAll, created.Organization.FeatureSet)
}
