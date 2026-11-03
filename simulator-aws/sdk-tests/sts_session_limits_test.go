package aws_sdk_test

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const accountTrust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`

// limitsRole creates a role trusted by the account whose own policy allows
// iam:ListRoles, iam:ListUsers and sqs:ListQueues, and returns its name and
// ARN.
func limitsRole(t *testing.T, prefix, trust string) (string, string) {
	t.Helper()
	admin := iamClient()
	name := uniquename.New(prefix)
	role, err := admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(trust)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}) })
	_, err = admin.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("lists"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":["iam:ListRoles","iam:ListUsers","sqs:ListQueues","sts:AssumeRole"],"Resource":"*"}]}`)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("lists")})
	})
	return name, aws.ToString(role.Role.Arn)
}

func managedPolicy(t *testing.T, prefix, document string) string {
	t.Helper()
	admin := iamClient()
	out, err := admin.CreatePolicy(ctx, &iam.CreatePolicyInput{PolicyName: aws.String(uniquename.New(prefix)), PolicyDocument: aws.String(document)})
	require.NoError(t, err)
	arn := aws.ToString(out.Policy.Arn)
	t.Cleanup(func() { _, _ = admin.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)}) })
	return arn
}

// assertDenied asserts an authorization refusal, which a query-protocol
// service reports as AccessDenied and a JSON-protocol one as
// AccessDeniedException.
func assertDenied(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	assert.Contains(t, []string{"AccessDenied", "AccessDeniedException"}, errCodeOf(err), msgAndArgs...)
}

func sessionConfig(creds *ststypes.Credentials) aws.Config {
	return aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(creds.AccessKeyId), aws.ToString(creds.SecretAccessKey), aws.ToString(creds.SessionToken))}
}

// sessionAllows reports which of iam:ListRoles, iam:ListUsers and
// sqs:ListQueues a session may call.
func sessionAllows(t *testing.T, creds *ststypes.Credentials) (listRoles, listUsers, listQueues bool) {
	t.Helper()
	cfg := sessionConfig(creds)
	iamc := iam.NewFromConfig(cfg, func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) })
	sqsc := sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, roles := iamc.ListRoles(ctx, &iam.ListRolesInput{})
	_, users := iamc.ListUsers(ctx, &iam.ListUsersInput{})
	_, queues := sqsc.ListQueues(ctx, &sqs.ListQueuesInput{})
	for _, err := range []error{roles, users, queues} {
		if err != nil {
			require.Contains(t, []string{"AccessDenied", "AccessDeniedException"}, errCodeOf(err), err)
		}
	}
	return roles == nil, users == nil, queues == nil
}

// TestSTS_RoleBoundaryCapsAssumedSession sets a permissions boundary on a role:
// its sessions keep only what both the role's policy and the boundary allow,
// including a session opened before the boundary was set.
func TestSTS_RoleBoundaryCapsAssumedSession(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	name, arn := limitsRole(t, "boundary-role", accountTrust)
	out, err := stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(arn), RoleSessionName: aws.String("before")})
	require.NoError(t, err)
	roles, users, queues := sessionAllows(t, out.Credentials)
	assert.True(t, roles && users && queues, "without a boundary the session has the role's permissions")

	boundary := managedPolicy(t, "iam-lists-boundary",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:List*","sts:AssumeRole"],"Resource":"*"}]}`)
	_, err = admin.PutRolePermissionsBoundary(ctx, &iam.PutRolePermissionsBoundaryInput{RoleName: aws.String(name), PermissionsBoundary: aws.String(boundary)})
	require.NoError(t, err)
	roles, users, queues = sessionAllows(t, out.Credentials)
	assert.True(t, roles && users, "the boundary allows iam:List*")
	assert.False(t, queues, "the boundary caps sqs:ListQueues, which the role's policy allows")

	fresh, err := stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(arn), RoleSessionName: aws.String("after")})
	require.NoError(t, err)
	_, _, queues = sessionAllows(t, fresh.Credentials)
	assert.False(t, queues)

	denying := managedPolicy(t, "deny-users-boundary", `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"iam:ListUsers","Resource":"*"}]}`)
	_, err = admin.PutRolePermissionsBoundary(ctx, &iam.PutRolePermissionsBoundaryInput{RoleName: aws.String(name), PermissionsBoundary: aws.String(denying)})
	require.NoError(t, err)
	roles, users, queues = sessionAllows(t, fresh.Credentials)
	assert.True(t, roles && queues)
	assert.False(t, users, "an explicit deny in the boundary refuses the action")

	_, err = admin.DeleteRolePermissionsBoundary(ctx, &iam.DeleteRolePermissionsBoundaryInput{RoleName: aws.String(name)})
	require.NoError(t, err)
	roles, users, queues = sessionAllows(t, fresh.Credentials)
	assert.True(t, roles && users && queues, "removing the boundary restores the role's permissions")
}

// TestSTS_SessionPoliciesNarrowAssumedRole passes an inline session policy and
// managed session policies: a session keeps what the role's policy and some
// session policy both allow, an explicit deny in a session policy refuses, and
// the response reports the packed size of what was passed.
func TestSTS_SessionPoliciesNarrowAssumedRole(t *testing.T) {
	t.Parallel()
	_, arn := limitsRole(t, "session-policy-role", accountTrust)
	assume := func(in *sts.AssumeRoleInput) (*sts.AssumeRoleOutput, error) {
		in.RoleArn, in.RoleSessionName = aws.String(arn), aws.String("narrowed")
		return stsClient().AssumeRole(ctx, in)
	}

	plain, err := assume(&sts.AssumeRoleInput{})
	require.NoError(t, err)
	assert.Nil(t, plain.PackedPolicySize, "a request that passes no session policy or tag packs nothing")

	inline, err := assume(&sts.AssumeRoleInput{Policy: aws.String(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:ListRoles","iam:ListGroups"],"Resource":"*"}]}`)})
	require.NoError(t, err)
	require.NotNil(t, inline.PackedPolicySize)
	assert.Positive(t, aws.ToInt32(inline.PackedPolicySize))
	assert.LessOrEqual(t, aws.ToInt32(inline.PackedPolicySize), int32(100))
	roles, users, queues := sessionAllows(t, inline.Credentials)
	assert.True(t, roles)
	assert.False(t, users || queues, "the inline session policy allows only iam:ListRoles of the role's grants")
	groups := iam.NewFromConfig(sessionConfig(inline.Credentials), func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = groups.ListGroups(ctx, &iam.ListGroupsInput{})
	assertDenied(t, err, "a session policy grants nothing the role's policy does not")

	usersPolicy := managedPolicy(t, "session-users",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*"}]}`)
	queuesPolicy := managedPolicy(t, "session-queues",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*"}]}`)
	managed, err := assume(&sts.AssumeRoleInput{PolicyArns: []ststypes.PolicyDescriptorType{{Arn: aws.String(usersPolicy)}, {Arn: aws.String(queuesPolicy)}}})
	require.NoError(t, err)
	require.NotNil(t, managed.PackedPolicySize)
	roles, users, queues = sessionAllows(t, managed.Credentials)
	assert.False(t, roles)
	assert.True(t, users && queues, "each managed session policy allows its own action")

	both, err := assume(&sts.AssumeRoleInput{
		Policy:     aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"sqs:*","Resource":"*"}]}`),
		PolicyArns: []ststypes.PolicyDescriptorType{{Arn: aws.String(queuesPolicy)}},
	})
	require.NoError(t, err)
	roles, users, queues = sessionAllows(t, both.Credentials)
	assert.True(t, roles && users)
	assert.False(t, queues, "an explicit deny in one session policy wins over an allow in another")

	_, err = assume(&sts.AssumeRoleInput{Policy: aws.String(`{"Version":"2012-10-17","Statement":[`)})
	var malformed *ststypes.MalformedPolicyDocumentException
	assert.ErrorAs(t, err, &malformed)

	var tags []ststypes.Tag
	for i := range 50 {
		value := make([]byte, 128)
		_, _ = rand.Read(value)
		tags = append(tags, ststypes.Tag{Key: aws.String(fmt.Sprintf("tag%02d", i)), Value: aws.String(hex.EncodeToString(value))})
	}
	_, err = assume(&sts.AssumeRoleInput{Tags: tags})
	var tooLarge *ststypes.PackedPolicyTooLargeException
	assert.ErrorAs(t, err, &tooLarge, "fifty incompressible tags exceed the packed limit")
}

// TestSTS_ResourcePolicyNamingTheSession grants a queue to a session's own ARN
// and to its role's ARN: a grant to the session holds past the session policy,
// and a grant to the role does not.
func TestSTS_ResourcePolicyNamingTheSession(t *testing.T) {
	t.Parallel()
	_, roleArn := limitsRole(t, "resource-session-role", accountTrust)
	out, err := stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(roleArn), RoleSessionName: aws.String("named"),
		Policy: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListRoles","Resource":"*"}]}`)})
	require.NoError(t, err)
	sessionArn := aws.ToString(out.AssumedRoleUser.Arn)

	admin := sqsClient()
	grant := func(principal string) (*string, string) {
		created, err := admin.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("session-grant"))})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = admin.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl}) })
		attrs, err := admin.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: created.QueueUrl,
			AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
		require.NoError(t, err)
		queueArn := attrs.Attributes["QueueArn"]
		_, err = admin.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: created.QueueUrl, Attributes: map[string]string{
			"Policy": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + principal + `"},"Action":"sqs:SendMessage","Resource":"` + queueArn + `"}]}`,
		}})
		require.NoError(t, err)
		return created.QueueUrl, queueArn
	}
	session := sqs.NewFromConfig(sessionConfig(out.Credentials), func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	toSession, _ := grant(sessionArn)
	_, err = session.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: toSession, MessageBody: aws.String("hello")})
	assert.NoError(t, err, "a queue policy naming the session grants past its session policy")

	toRole, _ := grant(roleArn)
	_, err = session.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: toRole, MessageBody: aws.String("hello")})
	assertDenied(t, err, "a queue policy naming the role stays within the session policy")
}

// TestSTS_GetFederationTokenSessionPolicies federates an IAM user: the session
// has no permissions without a session policy, the intersection of the user's
// policies and the session policies with one, and no IAM or AWS STS operation
// other than GetCallerIdentity either way.
func TestSTS_GetFederationTokenSessionPolicies(t *testing.T) {
	t.Parallel()
	akid, secret := restrictedCredential(t, "federator", `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":["sts:GetFederationToken","sqs:ListQueues","sns:ListTopics","iam:ListRoles"],"Resource":"*"}]}`)
	federator := sts.NewFromConfig(keyConfig(akid, secret), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	probe := func(creds *ststypes.Credentials) (queues, topics, roles, identity error) {
		cfg := sessionConfig(creds)
		_, queues = sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) }).ListQueues(ctx, &sqs.ListQueuesInput{})
		_, topics = sns.NewFromConfig(cfg, func(o *sns.Options) { o.BaseEndpoint = aws.String(baseURL) }).ListTopics(ctx, &sns.ListTopicsInput{})
		_, roles = iam.NewFromConfig(cfg, func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) }).ListRoles(ctx, &iam.ListRolesInput{})
		_, identity = sts.NewFromConfig(cfg, func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) }).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		return
	}

	bare, err := federator.GetFederationToken(ctx, &sts.GetFederationTokenInput{Name: aws.String("bare")})
	require.NoError(t, err)
	assert.Nil(t, bare.PackedPolicySize)
	assert.WithinDuration(t, time.Now().Add(12*time.Hour), aws.ToTime(bare.Credentials.Expiration), time.Minute,
		"a federation token lasts twelve hours by default")
	queues, topics, roles, identity := probe(bare.Credentials)
	assertDenied(t, queues, "a federated session without a session policy has no permissions")
	assertDenied(t, topics)
	assertDenied(t, roles)
	assert.NoError(t, identity)

	scoped, err := federator.GetFederationToken(ctx, &sts.GetFederationTokenInput{Name: aws.String("scoped"),
		Policy: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:ListQueues","iam:ListRoles","ec2:DescribeVolumes"],"Resource":"*"}]}`)})
	require.NoError(t, err)
	require.NotNil(t, scoped.PackedPolicySize)
	assert.Positive(t, aws.ToInt32(scoped.PackedPolicySize))
	queues, topics, roles, identity = probe(scoped.Credentials)
	assert.NoError(t, queues, "the user's policy and the session policy both allow sqs:ListQueues")
	assertDenied(t, topics, "the session policy does not allow sns:ListTopics")
	assertDenied(t, roles, "a federated session calls no IAM operation")
	assert.NoError(t, identity)
	_, err = sts.NewFromConfig(sessionConfig(scoped.Credentials), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) }).
		GetFederationToken(ctx, &sts.GetFederationTokenInput{Name: aws.String("again")})
	assertDenied(t, err, "a federated session calls no AWS STS operation but GetCallerIdentity")

	_, err = federator.GetFederationToken(ctx, &sts.GetFederationTokenInput{Name: aws.String("long"), DurationSeconds: aws.Int32(129601)})
	assert.Equal(t, "ValidationError", errCodeOf(err))

	_, roleArn := limitsRole(t, "federation-caller-role", accountTrust)
	assumed, err := stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(roleArn), RoleSessionName: aws.String("federates")})
	require.NoError(t, err)
	_, err = sts.NewFromConfig(sessionConfig(assumed.Credentials), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) }).
		GetFederationToken(ctx, &sts.GetFederationTokenInput{Name: aws.String("from-role")})
	assertDenied(t, err, "a role session cannot call GetFederationToken")
}

// enableRootSessions turns on trusted access for IAM in the organization and
// lets its management account open root sessions on member accounts.
func enableRootSessions(t *testing.T) {
	t.Helper()
	_, err := orgClient().EnableAWSServiceAccess(ctx, &organizations.EnableAWSServiceAccessInput{ServicePrincipal: aws.String("iam.amazonaws.com")})
	require.NoError(t, err)
	_, err = iamClient().EnableOrganizationsRootSessions(ctx, &iam.EnableOrganizationsRootSessionsInput{})
	require.NoError(t, err)
}

// rootAccessPrincipal creates a member account of the organization and an
// IAM user of the management account allowed sts:AssumeRoot, and returns an
// AWS STS client signed by the user and the member account's ID.
func rootAccessPrincipal(t *testing.T, prefix string) (*sts.Client, string) {
	t.Helper()
	org := orgClient()
	name := uniquename.New(prefix)
	created, err := org.CreateAccount(ctx, &organizations.CreateAccountInput{
		AccountName: aws.String(name), Email: aws.String(name + "@sim.invalid")})
	require.NoError(t, err)
	member := aws.ToString(created.CreateAccountStatus.AccountId)
	t.Cleanup(func() {
		_, _ = org.RemoveAccountFromOrganization(ctx, &organizations.RemoveAccountFromOrganizationInput{AccountId: aws.String(member)})
	})
	akid, secret := restrictedCredential(t, prefix,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRoot","Resource":"*"}]}`)
	return sts.NewFromConfig(keyConfig(akid, secret), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) }), member
}

// TestSTS_AssumeRootTaskPolicy opens a root session on a member account,
// scoped by the S3UnlockBucketPolicy task policy: it lists buckets and does
// nothing else, and reaches no bucket of the management account, whose policy
// grants the member account nothing. A root user, a target outside the
// organization's member accounts, a policy outside the root-task policies,
// and a session past 900 seconds open no session.
func TestSTS_AssumeRootTaskPolicy(t *testing.T) {
	t.Parallel()
	enableRootSessions(t)
	caller, member := rootAccessPrincipal(t, "root-unlock")
	bucket := uniquename.New("root-unlock")
	admin := s3Client()
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}) })
	_, err = admin.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(bucket)}) })

	unlock := &ststypes.PolicyDescriptorType{Arn: aws.String("arn:aws:iam::aws:policy/root-task/S3UnlockBucketPolicy")}
	_, err = caller.AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String("123456789012"), TaskPolicyArn: unlock})
	assert.Equal(t, "AccessDenied", errCodeOf(err), "the management account is not a member account")
	_, err = caller.AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String("arn:aws:iam::000011112222:root"), TaskPolicyArn: unlock})
	assert.Equal(t, "AccessDenied", errCodeOf(err), "an account outside the organization is not a member account")
	_, err = caller.AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String(member),
		TaskPolicyArn: &ststypes.PolicyDescriptorType{Arn: aws.String("arn:aws:iam::aws:policy/ReadOnlyAccess")}})
	assert.Equal(t, "ValidationError", errCodeOf(err), "only a root-task policy scopes a root session")
	_, err = caller.AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String(member), DurationSeconds: aws.Int32(901), TaskPolicyArn: unlock})
	assert.Equal(t, "ValidationError", errCodeOf(err), "a root session lasts at most 900 seconds")

	out, err := caller.AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String("arn:aws:iam::" + member + ":root"), TaskPolicyArn: unlock})
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), aws.ToTime(out.Credentials.Expiration), time.Minute)
	rootSTS := sts.NewFromConfig(sessionConfig(out.Credentials), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	identity, err := rootSTS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::"+member+":root", aws.ToString(identity.Arn))
	assert.Equal(t, member, aws.ToString(identity.UserId))
	_, err = rootSTS.AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String(member), TaskPolicyArn: unlock})
	assertDenied(t, err, "a root user cannot call AssumeRoot")
	root := s3.NewFromConfig(sessionConfig(out.Credentials), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.UsePathStyle = true
	})
	_, err = root.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err, "the task policy allows s3:ListAllMyBuckets")
	_, err = root.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(bucket)})
	assertDenied(t, err, "another account's bucket policy grants the member account's root user nothing")
	_, err = root.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(bucket)})
	assertDenied(t, err)
	_, err = root.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	assertDenied(t, err, "the task policy denies every other action")
	_, err = sqs.NewFromConfig(sessionConfig(out.Credentials), func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) }).
		ListQueues(ctx, &sqs.ListQueuesInput{})
	assertDenied(t, err)
}

// TestSTS_GetSessionTokenRestrictions opens GetSessionToken sessions. An IAM
// user's session keeps the user's permissions, but calls IAM only when MFA
// authenticated it, calls AWS STS only for AssumeRole and GetCallerIdentity,
// and opens no further session; an MFA code authenticates once.
func TestSTS_GetSessionTokenRestrictions(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	user := uniquename.New("session-token")
	iamMFAUser(t, admin, user)
	_, err := admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String("session"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Action":["iam:ListUsers","sqs:ListQueues","sts:AssumeRole","sts:GetFederationToken"],"Resource":"*"}]}`)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String("session")})
	})
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	caller := sts.NewFromConfig(keyConfig(aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey)),
		func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	userIdentity, err := caller.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	_, roleArn := limitsRole(t, "session-token-role", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"AWS":"`+aws.ToString(userIdentity.Arn)+`"},"Action":"sts:AssumeRole"}]}`)

	_, err = caller.GetSessionToken(ctx, &sts.GetSessionTokenInput{DurationSeconds: aws.Int32(899)})
	assert.Equal(t, "ValidationError", errCodeOf(err))
	plain, err := caller.GetSessionToken(ctx, &sts.GetSessionTokenInput{})
	require.NoError(t, err)
	cfg := sessionConfig(plain.Credentials)
	session := sts.NewFromConfig(cfg, func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	identity, err := session.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(userIdentity.Arn), aws.ToString(identity.Arn))
	_, err = sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) }).ListQueues(ctx, &sqs.ListQueuesInput{})
	assert.NoError(t, err, "the session keeps the user's permissions")
	sessionIAM := iam.NewFromConfig(cfg, func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = sessionIAM.ListUsers(ctx, &iam.ListUsersInput{})
	assertDenied(t, err, "a session MFA did not authenticate calls no IAM operation")
	_, err = session.GetFederationToken(ctx, &sts.GetFederationTokenInput{Name: aws.String("from-session")})
	assertDenied(t, err, "a session calls no AWS STS operation but AssumeRole and GetCallerIdentity")
	_, err = session.GetSessionToken(ctx, &sts.GetSessionTokenInput{})
	assertDenied(t, err, "session credentials cannot call GetSessionToken")
	_, err = session.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(roleArn), RoleSessionName: aws.String("from-session")})
	assert.NoError(t, err)

	device, err := admin.CreateVirtualMFADevice(ctx, &iam.CreateVirtualMFADeviceInput{VirtualMFADeviceName: aws.String(user)})
	require.NoError(t, err)
	serial := aws.ToString(device.VirtualMFADevice.SerialNumber)
	seed := device.VirtualMFADevice.Base32StringSeed
	t.Cleanup(func() {
		_, _ = admin.DeactivateMFADevice(ctx, &iam.DeactivateMFADeviceInput{UserName: aws.String(user), SerialNumber: aws.String(serial)})
		_, _ = admin.DeleteVirtualMFADevice(ctx, &iam.DeleteVirtualMFADeviceInput{SerialNumber: aws.String(serial)})
	})
	enabledAt := time.Now()
	enableMFADevice(t, admin, user, serial, seed, enabledAt)
	code := totpCode(t, seed, enabledAt.Add(30*time.Second))
	authenticated, err := caller.GetSessionToken(ctx, &sts.GetSessionTokenInput{SerialNumber: aws.String(serial), TokenCode: aws.String(code)})
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(12*time.Hour), aws.ToTime(authenticated.Credentials.Expiration), time.Minute,
		"an IAM user's session token lasts twelve hours by default")
	_, err = iam.NewFromConfig(sessionConfig(authenticated.Credentials), func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) }).
		ListUsers(ctx, &iam.ListUsersInput{})
	assert.NoError(t, err, "a session MFA authenticated calls IAM")
	_, err = caller.GetSessionToken(ctx, &sts.GetSessionTokenInput{SerialNumber: aws.String(serial), TokenCode: aws.String(code)})
	assertDenied(t, err, "an MFA code authenticates once")
}

// TestSTS_ResourcePolicyNamingTheFederatingUser grants a queue to the IAM
// user that federates: the user's federated session gets the grant, but only
// within the session policy, as a role's sessions do.
func TestSTS_ResourcePolicyNamingTheFederatingUser(t *testing.T) {
	t.Parallel()
	akid, secret := restrictedCredential(t, "federating-owner",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:GetFederationToken","Resource":"*"}]}`)
	federator := sts.NewFromConfig(keyConfig(akid, secret), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	userIdentity, err := federator.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)

	admin := sqsClient()
	created, err := admin.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("federating-grant"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl}) })
	attrs, err := admin.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: created.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
	require.NoError(t, err)
	queueArn := attrs.Attributes["QueueArn"]
	_, err = admin.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: created.QueueUrl, Attributes: map[string]string{
		"Policy": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + aws.ToString(userIdentity.Arn) + `"},"Action":"sqs:SendMessage","Resource":"` + queueArn + `"}]}`,
	}})
	require.NoError(t, err)

	send := func(policy string) error {
		out, err := federator.GetFederationToken(ctx, &sts.GetFederationTokenInput{Name: aws.String("grantee"), Policy: aws.String(policy)})
		require.NoError(t, err)
		_, err = sqs.NewFromConfig(sessionConfig(out.Credentials), func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) }).
			SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: created.QueueUrl, MessageBody: aws.String("hello")})
		return err
	}
	assert.NoError(t, send(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`),
		"the queue policy naming the federating user grants its session what the session policy allows")
	assertDenied(t, send(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*"}]}`),
		"the session policy still caps a grant to the federating user")
}

// TestIAM_PrincipalArnOfARoleSession reads aws:PrincipalArn for an assumed
// role: a queue policy conditioned on the role's ARN admits its session, and
// one conditioned on the session's ARN does not.
func TestIAM_PrincipalArnOfARoleSession(t *testing.T) {
	t.Parallel()
	_, roleArn := limitsRole(t, "principal-arn-role", accountTrust)
	out, err := stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(roleArn), RoleSessionName: aws.String("principal-arn")})
	require.NoError(t, err)
	sessionArn := aws.ToString(out.AssumedRoleUser.Arn)

	admin := sqsClient()
	grantOn := func(principalArn string) *string {
		created, err := admin.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("principal-arn"))})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = admin.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl}) })
		attrs, err := admin.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: created.QueueUrl,
			AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
		require.NoError(t, err)
		_, err = admin.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: created.QueueUrl, Attributes: map[string]string{
			"Policy": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"sqs:SendMessage",` +
				`"Resource":"` + attrs.Attributes["QueueArn"] + `","Condition":{"ArnEquals":{"aws:PrincipalArn":"` + principalArn + `"}}}]}`,
		}})
		require.NoError(t, err)
		return created.QueueUrl
	}
	session := sqs.NewFromConfig(sessionConfig(out.Credentials), func(o *sqs.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = session.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: grantOn(roleArn), MessageBody: aws.String("hello")})
	assert.NoError(t, err, "aws:PrincipalArn of a role session is the role's ARN")
	_, err = session.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: grantOn(sessionArn), MessageBody: aws.String("hello")})
	assertDenied(t, err, "aws:PrincipalArn of a role session is not the session's ARN")
}

// TestSTS_AssumeRoleDurationSeconds asks for sessions of several durations: up
// to the role's MaxSessionDuration succeeds, beyond it fails, and a chained
// role session lasts at most an hour.
func TestSTS_AssumeRoleDurationSeconds(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	name, arn := limitsRole(t, "duration-role", accountTrust)
	assume := func(client *sts.Client, seconds int32) (*sts.AssumeRoleOutput, error) {
		return client.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(arn), RoleSessionName: aws.String("timed"), DurationSeconds: aws.Int32(seconds)})
	}

	_, err := assume(stsClient(), 3601)
	assert.Equal(t, "ValidationError", errCodeOf(err), "a role without MaxSessionDuration allows an hour")
	_, err = assume(stsClient(), 899)
	assert.Equal(t, "ValidationError", errCodeOf(err))

	_, err = admin.UpdateRole(ctx, &iam.UpdateRoleInput{RoleName: aws.String(name), MaxSessionDuration: aws.Int32(100)})
	assert.Equal(t, "ValidationError", errCodeOf(err), "MaxSessionDuration ranges from an hour to twelve")
	_, err = admin.UpdateRole(ctx, &iam.UpdateRoleInput{RoleName: aws.String(name), MaxSessionDuration: aws.Int32(7200)})
	require.NoError(t, err)
	got, err := admin.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, int32(7200), aws.ToInt32(got.Role.MaxSessionDuration))

	out, err := assume(stsClient(), 7200)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), aws.ToTime(out.Credentials.Expiration), time.Minute)
	_, err = assume(stsClient(), 7201)
	assert.Equal(t, "ValidationError", errCodeOf(err))
	assert.Contains(t, err.Error(), "MaxSessionDuration")

	chained := sts.NewFromConfig(sessionConfig(out.Credentials), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = assume(chained, 3601)
	assert.Equal(t, "ValidationError", errCodeOf(err), "role chaining caps a session at an hour")
	assert.Contains(t, err.Error(), "role chaining")
	hour, err := assume(chained, 3600)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(time.Hour), aws.ToTime(hour.Credentials.Expiration), time.Minute)
}

// TestSTS_AssumeRoleMFA assumes a role whose trust policy requires MFA with
// codes computed from the virtual MFA device's seed: the device's next code
// succeeds once, and a wrong one, a used one or none is refused.
func TestSTS_AssumeRoleMFA(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	user := uniquename.New("mfa-assumer")
	iamMFAUser(t, admin, user)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	caller := sts.NewFromConfig(keyConfig(aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey)),
		func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	identity, err := caller.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)

	device, err := admin.CreateVirtualMFADevice(ctx, &iam.CreateVirtualMFADeviceInput{VirtualMFADeviceName: aws.String(user)})
	require.NoError(t, err)
	serial := aws.ToString(device.VirtualMFADevice.SerialNumber)
	seed := device.VirtualMFADevice.Base32StringSeed
	t.Cleanup(func() {
		_, _ = admin.DeactivateMFADevice(ctx, &iam.DeactivateMFADeviceInput{UserName: aws.String(user), SerialNumber: aws.String(serial)})
		_, _ = admin.DeleteVirtualMFADevice(ctx, &iam.DeleteVirtualMFADeviceInput{SerialNumber: aws.String(serial)})
	})

	_, roleArn := limitsRole(t, "mfa-role", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"AWS":"`+aws.ToString(identity.Arn)+`"},"Action":"sts:AssumeRole",
		"Condition":{"Bool":{"aws:MultiFactorAuthPresent":"true"}}}]}`)
	assume := func(code *string) error {
		in := &sts.AssumeRoleInput{RoleArn: aws.String(roleArn), RoleSessionName: aws.String("mfa")}
		if code != nil {
			in.SerialNumber, in.TokenCode = aws.String(serial), code
		}
		_, err := caller.AssumeRole(ctx, in)
		return err
	}

	assert.Equal(t, "AccessDenied", errCodeOf(assume(aws.String(totpCode(t, seed, time.Now())))),
		"a device not yet enabled for the caller authenticates nothing")
	enabledAt := time.Now()
	enableMFADevice(t, admin, user, serial, seed, enabledAt)

	assert.Equal(t, "AccessDenied", errCodeOf(assume(nil)), "the trust policy requires MFA")
	assert.Equal(t, "AccessDenied", errCodeOf(assume(aws.String(totpWrongCode(t, seed)))), "a code the device did not show is refused")
	assert.Equal(t, "AccessDenied", errCodeOf(assume(aws.String(totpCode(t, seed, enabledAt)))),
		"the code that enabled the device authenticates nothing again")
	next := totpCode(t, seed, enabledAt.Add(30*time.Second))
	require.NoError(t, assume(aws.String(next)), "the device's next code authenticates the call")
	assert.Equal(t, "AccessDenied", errCodeOf(assume(aws.String(next))), "a code authenticates once")
}
