package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createMemberAccount creates a member account of the organization, with
// roleName as its access role when set, and returns its ID.
func createMemberAccount(t *testing.T, prefix, roleName string) string {
	t.Helper()
	org := orgClient()
	name := uniquename.New(prefix)
	in := &organizations.CreateAccountInput{AccountName: aws.String(name), Email: aws.String(name + "@sim.invalid")}
	if roleName != "" {
		in.RoleName = aws.String(roleName)
	}
	created, err := org.CreateAccount(ctx, in)
	require.NoError(t, err)
	account := aws.ToString(created.CreateAccountStatus.AccountId)
	t.Cleanup(func() {
		_, _ = org.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: aws.String(account)})
	})
	return account
}

// memberAdmin assumes a member account's OrganizationAccountAccessRole as the
// management account's administrator and returns the session's credentials.
func memberAdmin(t *testing.T, account, roleName string) *ststypes.Credentials {
	t.Helper()
	out, err := stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         aws.String("arn:aws:iam::" + account + ":role/" + roleName),
		RoleSessionName: aws.String("admin"),
	})
	require.NoError(t, err)
	return out.Credentials
}

func iamWith(cfg aws.Config) *iam.Client {
	return iam.NewFromConfig(cfg, func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) })
}

func stsWith(cfg aws.Config) *sts.Client {
	return sts.NewFromConfig(cfg, func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
}

func sqsWith(cfg aws.Config) *sqs.Client {
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) })
}

// TestOrganizations_AccountAccessRole creates a member account and reaches
// it through the OrganizationAccountAccessRole that CreateAccount puts in it:
// the role trusts the management account, administers the member account,
// and what it creates there belongs to the member account alone.
func TestOrganizations_AccountAccessRole(t *testing.T) {
	t.Parallel()
	member := createMemberAccount(t, "access-role", "")
	creds := memberAdmin(t, member, "OrganizationAccountAccessRole")
	cfg := sessionConfig(creds)

	identity, err := stsWith(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, member, aws.ToString(identity.Account))
	assert.Equal(t, "arn:aws:sts::"+member+":assumed-role/OrganizationAccountAccessRole/admin", aws.ToString(identity.Arn))

	memberIAM := iamWith(cfg)
	role, err := memberIAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String("OrganizationAccountAccessRole")})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::"+member+":role/OrganizationAccountAccessRole", aws.ToString(role.Role.Arn))
	assert.Contains(t, aws.ToString(role.Role.AssumeRolePolicyDocument), "123456789012%3Aroot")

	user := uniquename.New("member-user")
	created, err := memberIAM.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::"+member+":user/"+user, aws.ToString(created.User.Arn))
	_, err = iamClient().GetUser(ctx, &iam.GetUserInput{UserName: aws.String(user)})
	assert.Equal(t, "NoSuchEntity", errCodeOf(err), "the member account's user is not the management account's")

	_, err = memberIAM.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String("lists"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*"}]}`)})
	require.NoError(t, err)
	key, err := memberIAM.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	userCfg := keyConfig(aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey))
	userIdentity, err := stsWith(userCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, member, aws.ToString(userIdentity.Account))
	users, err := iamWith(userCfg).ListUsers(ctx, &iam.ListUsersInput{})
	require.NoError(t, err)
	require.Len(t, users.Users, 1, "the member account holds its own users only")
	assert.Equal(t, user, aws.ToString(users.Users[0].UserName))
	info, err := stsClient().GetAccessKeyInfo(ctx, &sts.GetAccessKeyInfoInput{AccessKeyId: key.AccessKey.AccessKeyId})
	require.NoError(t, err)
	assert.Equal(t, member, aws.ToString(info.Account))

	roles, err := memberIAM.ListRoles(ctx, &iam.ListRolesInput{})
	require.NoError(t, err)
	require.Len(t, roles.Roles, 1)
	assert.Equal(t, "OrganizationAccountAccessRole", aws.ToString(roles.Roles[0].RoleName))

	// The management account's identities need their own policies to allow
	// the cross-account assumption, which the trust policy alone does not
	// grant.
	akid, secret := restrictedCredential(t, "no-assume", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*"}]}`)
	_, err = stsWith(keyConfig(akid, secret)).AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn: aws.String("arn:aws:iam::" + member + ":role/OrganizationAccountAccessRole"), RoleSessionName: aws.String("denied")})
	assertDenied(t, err)

	named := createMemberAccount(t, "named-role", "OrgAdmin")
	_, err = stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn: aws.String("arn:aws:iam::" + named + ":role/OrganizationAccountAccessRole"), RoleSessionName: aws.String("absent")})
	assertDenied(t, err, "CreateAccount names the role RoleName says")
	memberAdmin(t, named, "OrgAdmin")
}

// TestOrganizations_ServiceControlPoliciesGovernMembers attaches service
// control policies along a member account's path: a deny on its OU refuses
// the account's principals, an allow list on the account replaces
// FullAWSAccess there, and neither touches the management account.
func TestOrganizations_ServiceControlPoliciesGovernMembers(t *testing.T) {
	t.Parallel()
	org := orgClient()
	root := orgRootID(t, org)
	ou, err := org.CreateOrganizationalUnit(ctx, &organizations.CreateOrganizationalUnitInput{ParentId: aws.String(root), Name: aws.String(uniquename.New("scp-ou"))})
	require.NoError(t, err)
	ouID := aws.ToString(ou.OrganizationalUnit.Id)
	t.Cleanup(func() {
		_, _ = org.DeleteOrganizationalUnit(ctx, &organizations.DeleteOrganizationalUnitInput{OrganizationalUnitId: aws.String(ouID)})
	})
	member := createMemberAccount(t, "scp-member", "")
	_, err = org.MoveAccount(ctx, &organizations.MoveAccountInput{AccountId: aws.String(member), SourceParentId: aws.String(root), DestinationParentId: aws.String(ouID)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = org.MoveAccount(ctx, &organizations.MoveAccountInput{AccountId: aws.String(member), SourceParentId: aws.String(ouID), DestinationParentId: aws.String(root)})
	})
	for _, target := range []string{ouID, member} {
		attached, err := org.ListPoliciesForTarget(ctx, &organizations.ListPoliciesForTargetInput{TargetId: aws.String(target), Filter: orgtypes.PolicyTypeServiceControlPolicy})
		require.NoError(t, err)
		require.Len(t, attached.Policies, 1, "AWS Organizations attaches FullAWSAccess to %s", target)
		assert.Equal(t, "FullAWSAccess", aws.ToString(attached.Policies[0].Name))
	}

	cfg := sessionConfig(memberAdmin(t, member, "OrganizationAccountAccessRole"))
	_, err = sqsWith(cfg).ListQueues(ctx, &sqs.ListQueuesInput{})
	require.NoError(t, err, "FullAWSAccess at every level allows the member account's administrator")

	createSCP := func(name, content string) string {
		p, err := org.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New(name)), Description: aws.String(name),
			Type: orgtypes.PolicyTypeServiceControlPolicy, Content: aws.String(content)})
		require.NoError(t, err)
		id := aws.ToString(p.Policy.PolicySummary.Id)
		t.Cleanup(func() { _, _ = org.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: aws.String(id)}) })
		return id
	}
	_, err = org.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New("bad")), Description: aws.String("bad"),
		Type: orgtypes.PolicyTypeServiceControlPolicy, Content: aws.String(`{"Statement":`)})
	assert.Equal(t, "MalformedPolicyDocumentException", errCodeOf(err))

	deny := createSCP("deny-sqs", `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sqs:ListQueues","Resource":"*"}]}`)
	_, err = org.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(deny), TargetId: aws.String(ouID)})
	require.NoError(t, err)
	_, err = sqsWith(cfg).ListQueues(ctx, &sqs.ListQueuesInput{})
	assertDenied(t, err, "a deny on the OU refuses the member account's principals")
	_, err = iamWith(cfg).ListRoles(ctx, &iam.ListRolesInput{})
	require.NoError(t, err, "the deny names sqs:ListQueues alone")
	_, err = sqsClient().ListQueues(ctx, &sqs.ListQueuesInput{})
	require.NoError(t, err, "service control policies do not govern the management account")
	_, err = org.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String(deny), TargetId: aws.String(ouID)})
	require.NoError(t, err)
	_, err = sqsWith(cfg).ListQueues(ctx, &sqs.ListQueuesInput{})
	require.NoError(t, err)

	allowIAM := createSCP("allow-iam", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:*","sts:*"],"Resource":"*"}]}`)
	fullAccess := "p-FullAWSAccess"
	_, err = org.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String(fullAccess), TargetId: aws.String(member)})
	assert.Equal(t, "ConstraintViolationException", errCodeOf(err), "an entity keeps at least one service control policy")
	_, err = org.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(allowIAM), TargetId: aws.String(member)})
	require.NoError(t, err)
	_, err = org.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String(fullAccess), TargetId: aws.String(member)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = org.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(fullAccess), TargetId: aws.String(member)})
		_, _ = org.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String(allowIAM), TargetId: aws.String(member)})
	})
	_, err = sqsWith(cfg).ListQueues(ctx, &sqs.ListQueuesInput{})
	assertDenied(t, err, "a level that allows the action nowhere refuses it")
	_, err = iamWith(cfg).ListRoles(ctx, &iam.ListRolesInput{})
	require.NoError(t, err, "every level allows IAM")
}

// TestSTS_SourceIdentity sets a source identity on a role session: the trust
// policy must allow sts:SetSourceIdentity, the session reports the value as
// aws:SourceIdentity, and a session chained from it keeps the value and
// cannot change it.
func TestSTS_SourceIdentity(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	trusting := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":["sts:AssumeRole","sts:SetSourceIdentity"]}]}`
	newRole := func(prefix, trust string) string {
		name := uniquename.New(prefix)
		out, err := admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(trust)})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = admin.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("source")})
			_, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
		})
		_, err = admin.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("source"),
			PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[
			{"Effect":"Allow","Action":["sts:AssumeRole","sts:SetSourceIdentity"],"Resource":"*"},
			{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*","Condition":{"StringEquals":{"aws:SourceIdentity":"alice"}}}]}`)})
		require.NoError(t, err)
		return aws.ToString(out.Role.Arn)
	}
	first := newRole("source-first", trusting)
	second := newRole("source-second", trusting)
	untrusting := newRole("source-untrusting", accountTrust)

	caller := stsClient()
	_, err := caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(first), RoleSessionName: aws.String("s"), SourceIdentity: aws.String("a")})
	assert.Equal(t, "ValidationError", errCodeOf(err), "a source identity is at least two characters")
	_, err = caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(first), RoleSessionName: aws.String("s"), SourceIdentity: aws.String("al ice")})
	assert.Equal(t, "ValidationError", errCodeOf(err), "a source identity matches [\\w+=,.@-]*")
	_, err = caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(untrusting), RoleSessionName: aws.String("s"), SourceIdentity: aws.String("alice")})
	assertDenied(t, err, "the trust policy must allow sts:SetSourceIdentity")

	plain, err := caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(first), RoleSessionName: aws.String("plain")})
	require.NoError(t, err)
	assert.Nil(t, plain.SourceIdentity)
	_, err = sqsWith(sessionConfig(plain.Credentials)).ListQueues(ctx, &sqs.ListQueuesInput{})
	assertDenied(t, err, "a session without a source identity has no aws:SourceIdentity")

	set, err := caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(first), RoleSessionName: aws.String("set"), SourceIdentity: aws.String("alice")})
	require.NoError(t, err)
	assert.Equal(t, "alice", aws.ToString(set.SourceIdentity))
	session := sessionConfig(set.Credentials)
	_, err = sqsWith(session).ListQueues(ctx, &sqs.ListQueuesInput{})
	require.NoError(t, err, "the session's requests carry aws:SourceIdentity")

	chained := stsWith(session)
	kept, err := chained.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(second), RoleSessionName: aws.String("chained")})
	require.NoError(t, err)
	assert.Equal(t, "alice", aws.ToString(kept.SourceIdentity), "a chained session keeps the source identity")
	_, err = sqsWith(sessionConfig(kept.Credentials)).ListQueues(ctx, &sqs.ListQueuesInput{})
	require.NoError(t, err)
	_, err = chained.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(second), RoleSessionName: aws.String("changed"), SourceIdentity: aws.String("bob")})
	assertDenied(t, err, "a chained session cannot change the source identity")
	_, err = chained.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(untrusting), RoleSessionName: aws.String("untrusted")})
	assertDenied(t, err, "a role chained to must allow sts:SetSourceIdentity for the identity the session carries")
}

// TestSTS_MemberRootCredentials works on a member account's root user
// through AssumeRoot sessions: one task policy creates the root user's
// password, another reads its credentials, a third deletes the password,
// and the account's service control policies cap the root user too.
func TestSTS_MemberRootCredentials(t *testing.T) {
	t.Parallel()
	enableRootSessions(t)
	caller, member := rootAccessPrincipal(t, "root-creds")
	root := func(task string) aws.Config {
		t.Helper()
		out, err := caller.AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String(member),
			TaskPolicyArn: &ststypes.PolicyDescriptorType{Arn: aws.String("arn:aws:iam::aws:policy/root-task/" + task)}})
		require.NoError(t, err)
		return sessionConfig(out.Credentials)
	}

	audit := iamWith(root("IAMAuditRootUserCredentials"))
	_, err := audit.GetLoginProfile(ctx, &iam.GetLoginProfileInput{})
	assert.Equal(t, "NoSuchEntity", errCodeOf(err), "a created member account's root user has no password")
	user, err := audit.GetUser(ctx, &iam.GetUserInput{})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::"+member+":root", aws.ToString(user.User.Arn))
	assert.Equal(t, member, aws.ToString(user.User.UserId))
	keys, err := audit.ListAccessKeys(ctx, &iam.ListAccessKeysInput{})
	require.NoError(t, err)
	assert.Empty(t, keys.AccessKeyMetadata)
	devices, err := audit.ListMFADevices(ctx, &iam.ListMFADevicesInput{})
	require.NoError(t, err)
	assert.Empty(t, devices.MFADevices)
	certs, err := audit.ListSigningCertificates(ctx, &iam.ListSigningCertificatesInput{})
	require.NoError(t, err)
	assert.Empty(t, certs.Certificates)
	_, err = audit.CreateLoginProfile(ctx, &iam.CreateLoginProfileInput{Password: aws.String("Root-Passw0rd!")})
	assertDenied(t, err, "the audit task policy creates no password")

	password := iamWith(root("IAMCreateRootUserPassword"))
	created, err := password.CreateLoginProfile(ctx, &iam.CreateLoginProfileInput{Password: aws.String("Root-Passw0rd!")})
	require.NoError(t, err)
	assert.Nil(t, created.LoginProfile.UserName, "the root user's login profile names no user")
	_, err = password.CreateLoginProfile(ctx, &iam.CreateLoginProfileInput{Password: aws.String("Root-Passw0rd!")})
	assert.Equal(t, "EntityAlreadyExists", errCodeOf(err))
	_, err = audit.GetLoginProfile(ctx, &iam.GetLoginProfileInput{})
	require.NoError(t, err, "the audit session reads the password the other session created")

	org := orgClient()
	deny, err := org.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New("deny-login")), Description: aws.String("deny"),
		Type:    orgtypes.PolicyTypeServiceControlPolicy,
		Content: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"iam:GetLoginProfile","Resource":"*"}]}`)})
	require.NoError(t, err)
	denyID := aws.ToString(deny.Policy.PolicySummary.Id)
	_, err = org.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(denyID), TargetId: aws.String(member)})
	require.NoError(t, err)
	_, err = audit.GetLoginProfile(ctx, &iam.GetLoginProfileInput{})
	assertDenied(t, err, "service control policies cap a member account's root user")
	_, err = org.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String(denyID), TargetId: aws.String(member)})
	require.NoError(t, err)
	_, _ = org.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: aws.String(denyID)})

	remove := iamWith(root("IAMDeleteRootUserCredentials"))
	_, err = remove.DeleteLoginProfile(ctx, &iam.DeleteLoginProfileInput{})
	require.NoError(t, err)
	_, err = remove.GetLoginProfile(ctx, &iam.GetLoginProfileInput{})
	assert.Equal(t, "NoSuchEntity", errCodeOf(err))
	_, err = remove.CreateLoginProfile(ctx, &iam.CreateLoginProfileInput{Password: aws.String("Root-Passw0rd!")})
	assertDenied(t, err)
}

// TestSTS_AssumeRootCarriesSourceIdentity opens a root session from a role
// session that carries a source identity: the root session carries it on,
// which a member account's service control policy reads.
func TestSTS_AssumeRootCarriesSourceIdentity(t *testing.T) {
	t.Parallel()
	enableRootSessions(t)
	member := createMemberAccount(t, "root-source", "")
	admin := iamClient()
	name := uniquename.New("root-opener")
	role, err := admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":["sts:AssumeRole","sts:SetSourceIdentity"]}]}`)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("root")})
		_, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})
	_, err = admin.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("root"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRoot","Resource":"*"}]}`)})
	require.NoError(t, err)

	org := orgClient()
	deny, err := org.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New("deny-source")), Description: aws.String("deny"),
		Type: orgtypes.PolicyTypeServiceControlPolicy,
		Content: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"iam:GetUser","Resource":"*",
		  "Condition":{"StringEquals":{"aws:SourceIdentity":"blocked"}}}]}`)})
	require.NoError(t, err)
	denyID := aws.ToString(deny.Policy.PolicySummary.Id)
	_, err = org.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(denyID), TargetId: aws.String(member)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = org.DetachPolicy(ctx, &organizations.DetachPolicyInput{PolicyId: aws.String(denyID), TargetId: aws.String(member)})
		_, _ = org.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: aws.String(denyID)})
	})

	audit := func(sourceIdentity string) error {
		t.Helper()
		in := &sts.AssumeRoleInput{RoleArn: role.Role.Arn, RoleSessionName: aws.String("opener")}
		if sourceIdentity != "" {
			in.SourceIdentity = aws.String(sourceIdentity)
		}
		opener, err := stsClient().AssumeRole(ctx, in)
		require.NoError(t, err)
		session, err := stsWith(sessionConfig(opener.Credentials)).AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String(member),
			TaskPolicyArn: &ststypes.PolicyDescriptorType{Arn: aws.String("arn:aws:iam::aws:policy/root-task/IAMAuditRootUserCredentials")}})
		require.NoError(t, err)
		_, err = iamWith(sessionConfig(session.Credentials)).GetUser(ctx, &iam.GetUserInput{})
		return err
	}
	assert.NoError(t, audit("allowed"))
	assertDenied(t, audit("blocked"), "the root session carries the opening session's source identity")
}

// TestOrganizations_DisableServiceControlPolicies disables service control
// policies in the root, which detaches every one and stops governing member
// accounts, and re-enables them, which attaches FullAWSAccess everywhere.
func TestOrganizations_DisableServiceControlPolicies(t *testing.T) {
	org := orgClient()
	root := orgRootID(t, org)
	member := createMemberAccount(t, "scp-toggle", "")
	cfg := sessionConfig(memberAdmin(t, member, "OrganizationAccountAccessRole"))
	deny, err := org.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(uniquename.New("deny-roles")), Description: aws.String("deny"),
		Type:    orgtypes.PolicyTypeServiceControlPolicy,
		Content: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"iam:ListRoles","Resource":"*"}]}`)})
	require.NoError(t, err)
	denyID := aws.ToString(deny.Policy.PolicySummary.Id)
	t.Cleanup(func() { _, _ = org.DeletePolicy(ctx, &organizations.DeletePolicyInput{PolicyId: aws.String(denyID)}) })
	_, err = org.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(denyID), TargetId: aws.String(member)})
	require.NoError(t, err)
	_, err = iamWith(cfg).ListRoles(ctx, &iam.ListRolesInput{})
	assertDenied(t, err)

	_, err = org.EnablePolicyType(ctx, &organizations.EnablePolicyTypeInput{RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeServiceControlPolicy})
	assert.Equal(t, "PolicyTypeAlreadyEnabledException", errCodeOf(err))
	disabled, err := org.DisablePolicyType(ctx, &organizations.DisablePolicyTypeInput{RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeServiceControlPolicy})
	require.NoError(t, err)
	for _, pt := range disabled.Root.PolicyTypes {
		assert.NotEqual(t, orgtypes.PolicyTypeServiceControlPolicy, pt.Type)
	}
	t.Cleanup(func() {
		_, _ = org.EnablePolicyType(ctx, &organizations.EnablePolicyTypeInput{RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeServiceControlPolicy})
	})
	roots, err := org.ListRoots(ctx, &organizations.ListRootsInput{})
	require.NoError(t, err)
	for _, pt := range roots.Roots[0].PolicyTypes {
		assert.NotEqual(t, orgtypes.PolicyTypeServiceControlPolicy, pt.Type, "ListRoots lists the policy types the root enables")
	}
	described, err := org.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err)
	assert.Contains(t, described.Organization.AvailablePolicyTypes,
		orgtypes.PolicyTypeSummary{Type: orgtypes.PolicyTypeServiceControlPolicy, Status: orgtypes.PolicyTypeStatusEnabled},
		"a policy type the root disables still appears enabled for an organization with all features")
	_, err = iamWith(cfg).ListRoles(ctx, &iam.ListRolesInput{})
	require.NoError(t, err, "no service control policy governs while the root disables them")
	_, err = org.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(denyID), TargetId: aws.String(member)})
	assert.Equal(t, "PolicyTypeNotEnabledException", errCodeOf(err))

	enabled, err := org.EnablePolicyType(ctx, &organizations.EnablePolicyTypeInput{RootId: aws.String(root), PolicyType: orgtypes.PolicyTypeServiceControlPolicy})
	require.NoError(t, err)
	var scp bool
	for _, pt := range enabled.Root.PolicyTypes {
		scp = scp || pt.Type == orgtypes.PolicyTypeServiceControlPolicy && pt.Status == orgtypes.PolicyTypeStatusEnabled
	}
	assert.True(t, scp)
	for _, target := range []string{root, member} {
		attached, err := org.ListPoliciesForTarget(ctx, &organizations.ListPoliciesForTargetInput{TargetId: aws.String(target), Filter: orgtypes.PolicyTypeServiceControlPolicy})
		require.NoError(t, err)
		var names []string
		for _, p := range attached.Policies {
			names = append(names, aws.ToString(p.Name))
		}
		assert.Equal(t, []string{"FullAWSAccess"}, names, "re-enabling leaves FullAWSAccess alone on %s", target)
	}
	_, err = iamWith(cfg).ListRoles(ctx, &iam.ListRolesInput{})
	require.NoError(t, err, "the deny attached before disabling is gone")
}
