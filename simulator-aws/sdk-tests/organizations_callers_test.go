package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func orgWith(cfg aws.Config) *organizations.Client {
	return organizations.NewFromConfig(cfg, func(o *organizations.Options) { o.BaseEndpoint = aws.String(baseURL) })
}

func errMessageOf(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorMessage()
	}
	return ""
}

const orgAccessDeniedMessage = "You don't have permissions to perform the requested operation."

// assertOrgAccessDenied expects AWS Organizations to refuse a member account
// an operation the management account alone, or a delegated administrator,
// can call.
func assertOrgAccessDenied(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	assert.Equal(t, "AccessDeniedException", errCodeOf(err), msgAndArgs...)
	assert.Equal(t, orgAccessDeniedMessage, errMessageOf(err), msgAndArgs...)
}

// TestOrganizations_MemberAccountCallers calls AWS Organizations as a member
// account's administrator: it describes its organization, is refused the
// management account's operations, reads the organization while it is a
// delegated administrator for a service, and manages the service control
// policies the organization's delegation policy opens to it. The delegation
// policy is the organization's only one, so the test runs alone.
func TestOrganizations_MemberAccountCallers(t *testing.T) {
	mgmt := orgClient()
	root := orgRootID(t, mgmt)
	member := createMemberAccount(t, "org-callers", "")
	memberOrg := orgWith(sessionConfig(memberAdmin(t, member, "OrganizationAccountAccessRole")))

	described, err := memberOrg.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err, "any account in the organization describes it")
	assert.Equal(t, "123456789012", aws.ToString(described.Organization.MasterAccountId))

	_, err = memberOrg.ListAccounts(ctx, &organizations.ListAccountsInput{})
	assertOrgAccessDenied(t, err, "ListAccounts needs the management account or a delegated administrator")
	_, err = memberOrg.CreateAccount(ctx, &organizations.CreateAccountInput{AccountName: aws.String("nested"), Email: aws.String("nested@sim.invalid")})
	assertOrgAccessDenied(t, err, "CreateAccount is the management account's")
	_, err = memberOrg.CreateOrganizationalUnit(ctx, &organizations.CreateOrganizationalUnitInput{ParentId: aws.String(root), Name: aws.String("nested")})
	assertOrgAccessDenied(t, err, "CreateOrganizationalUnit is the management account's")
	_, err = memberOrg.EnablePolicyType(ctx, &organizations.EnablePolicyTypeInput{RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeTagPolicy})
	assertOrgAccessDenied(t, err, "EnablePolicyType needs the delegation policy")
	_, err = memberOrg.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String("p-FullAWSAccess"), TargetId: aws.String(member)})
	assertOrgAccessDenied(t, err, "AttachPolicy needs the delegation policy")
	_, err = memberOrg.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: aws.String(member)})
	assertOrgAccessDenied(t, err, "a member account leaves with LeaveOrganization")

	const service = "config.amazonaws.com"
	_, err = mgmt.RegisterDelegatedAdministrator(ctx, &organizations.RegisterDelegatedAdministratorInput{AccountId: aws.String(member), ServicePrincipal: aws.String(service)})
	require.NoError(t, err)
	accounts, err := memberOrg.ListAccounts(ctx, &organizations.ListAccountsInput{})
	require.NoError(t, err, "a delegated administrator reads the organization")
	var listed bool
	for _, a := range accounts.Accounts {
		listed = listed || aws.ToString(a.Id) == member
	}
	assert.True(t, listed)
	_, err = memberOrg.ListRoots(ctx, &organizations.ListRootsInput{})
	require.NoError(t, err)
	_, err = memberOrg.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String("da-write"), Description: aws.String("d"),
		Type: orgtypes.PolicyTypeServiceControlPolicy, Content: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)})
	assertOrgAccessDenied(t, err, "a delegated administrator for a service reads and does not write")
	_, err = mgmt.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: aws.String(member)})
	assert.Equal(t, "ConstraintViolationException", errCodeOf(err), "a delegated administrator stays until deregistered")
	_, err = mgmt.DeregisterDelegatedAdministrator(ctx, &organizations.DeregisterDelegatedAdministratorInput{AccountId: aws.String(member), ServicePrincipal: aws.String(service)})
	require.NoError(t, err)
	_, err = memberOrg.ListAccounts(ctx, &organizations.ListAccountsInput{})
	assertOrgAccessDenied(t, err, "deregistering ends the read access")

	delegation := `{"Version":"2012-10-17","Statement":[{"Sid":"ManageSCPs","Effect":"Allow","Principal":{"AWS":"` + member + `"},
		"Action":["organizations:CreatePolicy","organizations:DescribePolicy","organizations:DeletePolicy"],
		"Resource":"arn:aws:organizations::123456789012:policy/o-sim0000000/service_control_policy/*",
		"Condition":{"StringEquals":{"organizations:PolicyType":"SERVICE_CONTROL_POLICY"}}}]}`
	_, err = mgmt.PutResourcePolicy(ctx, &organizations.PutResourcePolicyInput{Content: aws.String(delegation)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = mgmt.DeleteResourcePolicy(ctx, &organizations.DeleteResourcePolicyInput{}) })
	_, err = memberOrg.PutResourcePolicy(ctx, &organizations.PutResourcePolicyInput{Content: aws.String(delegation)})
	assertOrgAccessDenied(t, err, "PutResourcePolicy is the management account's")

	created, err := memberOrg.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New("delegated")), Description: aws.String("d"),
		Type: orgtypes.PolicyTypeServiceControlPolicy, Content: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sqs:*","Resource":"*"}]}`)})
	require.NoError(t, err, "the delegation policy opens CreatePolicy to the member account")
	policyID := created.Policy.PolicySummary.Id
	_, err = memberOrg.DescribePolicy(ctx, &organizations.DescribePolicyInput{PolicyId: policyID})
	require.NoError(t, err)
	_, err = memberOrg.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: policyID, TargetId: aws.String(member)})
	assertOrgAccessDenied(t, err, "the delegation policy names no AttachPolicy")
	_, err = memberOrg.ListAccounts(ctx, &organizations.ListAccountsInput{})
	assertOrgAccessDenied(t, err, "the delegation policy names no ListAccounts")
	_, err = memberOrg.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New("tag")), Description: aws.String("d"),
		Type: orgtypes.PolicyTypeTagPolicy, Content: aws.String(`{"tags":{}}`)})
	assertOrgAccessDenied(t, err, "the delegation policy opens service control policies alone")
	_, err = memberOrg.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: policyID})
	require.NoError(t, err)
}

// TestOrganizations_RemovedAccountRunsStandalone removes a member account
// from the organization: it leaves the organization's listings, its IAM user
// keeps working as a standalone account's, and the service control policy
// that refused it no longer applies.
func TestOrganizations_RemovedAccountRunsStandalone(t *testing.T) {
	t.Parallel()
	mgmt := orgClient()
	member := createMemberAccount(t, "standalone", "")
	memberIAM := iamWith(sessionConfig(memberAdmin(t, member, "OrganizationAccountAccessRole")))
	user := uniquename.New("standalone-user")
	_, err := memberIAM.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	_, err = memberIAM.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String("all"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)})
	require.NoError(t, err)
	key, err := memberIAM.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	userCfg := keyConfig(aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey))
	userArn := "arn:aws:iam::" + member + ":user/" + user

	deny, err := mgmt.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New("deny-queues")), Description: aws.String("deny"),
		Type: orgtypes.PolicyTypeServiceControlPolicy, Content: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sqs:ListQueues","Resource":"*"}]}`)})
	require.NoError(t, err)
	denyID := deny.Policy.PolicySummary.Id
	t.Cleanup(func() { _, _ = mgmt.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: denyID}) })
	_, err = mgmt.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: denyID, TargetId: aws.String(member)})
	require.NoError(t, err)
	_, err = sqsWith(userCfg).ListQueues(ctx, &sqs.ListQueuesInput{})
	assertDenied(t, err)
	assert.Equal(t, "User: "+userArn+" is not authorized to perform: sqs:ListQueues with an explicit deny in a service control policy", errMessageOf(err))

	_, err = mgmt.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: aws.String(member)})
	require.NoError(t, err)
	_, err = mgmt.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(member)})
	assert.Equal(t, "AccountNotFoundException", errCodeOf(err))
	accounts, err := mgmt.ListAccounts(ctx, &organizations.ListAccountsInput{})
	require.NoError(t, err)
	for _, a := range accounts.Accounts {
		assert.NotEqual(t, member, aws.ToString(a.Id), "a removed account leaves the organization's listing")
	}

	identity, err := stsWith(userCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err, "the standalone account's user keeps its access key")
	assert.Equal(t, userArn, aws.ToString(identity.Arn))
	_, err = sqsWith(userCfg).ListQueues(ctx, &sqs.ListQueuesInput{})
	require.NoError(t, err, "the organization's service control policies no longer govern the account")
	_, err = orgWith(userCfg).DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	assert.Equal(t, "AWSOrganizationsNotInUseException", errCodeOf(err), "the standalone account belongs to no organization")
	_, err = stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn: aws.String("arn:aws:iam::" + member + ":role/OrganizationAccountAccessRole"), RoleSessionName: aws.String("after")})
	require.NoError(t, err, "the access role stays in the account, trusting the former management account")
}

// TestOrganizations_MemberLeavesOrganization leaves the organization from a
// member account, which then runs standalone.
func TestOrganizations_MemberLeavesOrganization(t *testing.T) {
	t.Parallel()
	mgmt := orgClient()
	member := createMemberAccount(t, "leaver", "")
	cfg := sessionConfig(memberAdmin(t, member, "OrganizationAccountAccessRole"))
	_, err := orgWith(cfg).LeaveOrganization(ctx, &organizations.LeaveOrganizationInput{})
	require.NoError(t, err)
	_, err = mgmt.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(member)})
	assert.Equal(t, "AccountNotFoundException", errCodeOf(err))
	_, err = iamWith(cfg).ListRoles(ctx, &iam.ListRolesInput{})
	require.NoError(t, err, "the account's identities stay")
	_, err = orgWith(cfg).LeaveOrganization(ctx, &organizations.LeaveOrganizationInput{})
	assert.Equal(t, "AWSOrganizationsNotInUseException", errCodeOf(err))
}

// TestOrganizations_ServiceControlPolicyImplicitDenial refuses a member
// account's user an action no service control policy on its path allows, in
// the words AWS uses for that.
func TestOrganizations_ServiceControlPolicyImplicitDenial(t *testing.T) {
	t.Parallel()
	mgmt := orgClient()
	member := createMemberAccount(t, "scp-implicit", "")
	cfg := sessionConfig(memberAdmin(t, member, "OrganizationAccountAccessRole"))
	identity, err := stsWith(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)

	allow, err := mgmt.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New("allow-iam")), Description: aws.String("allow"),
		Type: orgtypes.PolicyTypeServiceControlPolicy, Content: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:*","sts:*"],"Resource":"*"}]}`)})
	require.NoError(t, err)
	allowID := allow.Policy.PolicySummary.Id
	_, err = mgmt.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: allowID, TargetId: aws.String(member)})
	require.NoError(t, err)
	_, err = mgmt.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String("p-FullAWSAccess"), TargetId: aws.String(member)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = mgmt.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String("p-FullAWSAccess"), TargetId: aws.String(member)})
		_, _ = mgmt.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: allowID, TargetId: aws.String(member)})
		_, _ = mgmt.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: allowID})
	})
	_, err = sqsWith(cfg).ListQueues(ctx, &sqs.ListQueuesInput{})
	assertDenied(t, err)
	assert.Equal(t, "User: "+aws.ToString(identity.Arn)+" is not authorized to perform: sqs:ListQueues because no service control policy allows the sqs:ListQueues action", errMessageOf(err))
}

// TestOrganizations_CloseAccountState closes a member account, which reports
// SUSPENDED in both Status and State, and refuses closing it again.
func TestOrganizations_CloseAccountState(t *testing.T) {
	t.Parallel()
	mgmt := orgClient()
	member := createMemberAccount(t, "closing", "")
	described, err := mgmt.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(member)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.AccountStateActive, described.Account.State)
	_, err = mgmt.CloseAccount(ctx, &organizations.CloseAccountInput{AccountId: aws.String(member)})
	require.NoError(t, err)
	described, err = mgmt.DescribeAccount(ctx, &organizations.DescribeAccountInput{AccountId: aws.String(member)})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.AccountStatusSuspended, described.Account.Status)
	assert.Equal(t, orgtypes.AccountStateSuspended, described.Account.State)
	_, err = mgmt.CloseAccount(ctx, &organizations.CloseAccountInput{AccountId: aws.String(member)})
	assert.Equal(t, "AccountAlreadyClosedException", errCodeOf(err))
	_, err = mgmt.CloseAccount(ctx, &organizations.CloseAccountInput{AccountId: aws.String("123456789012")})
	var violation *orgtypes.ConstraintViolationException
	require.ErrorAs(t, err, &violation)
	assert.Equal(t, orgtypes.ConstraintViolationExceptionReasonCannotCloseManagementAccount, violation.Reason)
}
