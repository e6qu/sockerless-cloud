package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_AccountSummary drives GetAccountSummary and asserts the SummaryMap
// reflects the real entity counts computed from the IAM stores.
func TestIAM_AccountSummary(t *testing.T) {
	c := iamClient()

	// Seed a user so the Users count is non-zero and deterministic-relative.
	user := "acctsummary-user"
	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	out, err := c.GetAccountSummary(ctx, &iam.GetAccountSummaryInput{})
	require.NoError(t, err)
	require.NotEmpty(t, out.SummaryMap)
	assert.GreaterOrEqual(t, out.SummaryMap["Users"], int32(1))
	// Fixed quotas are present.
	assert.Equal(t, int32(5000), out.SummaryMap["UsersQuota"])
	assert.Contains(t, out.SummaryMap, "Policies")
	assert.Contains(t, out.SummaryMap, "Roles")
}

// TestIAM_AccountAuthorizationDetails drives GetAccountAuthorizationDetails and
// asserts the enumerated user carries its inline + attached managed policies.
func TestIAM_AccountAuthorizationDetails(t *testing.T) {
	c := iamClient()
	user := "aad-user"
	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	_, err = c.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String("aad-inline"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		c.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String("aad-inline")})
	})

	out, err := c.GetAccountAuthorizationDetails(ctx, &iam.GetAccountAuthorizationDetailsInput{})
	require.NoError(t, err)

	var found *types.UserDetail
	for i := range out.UserDetailList {
		if aws.ToString(out.UserDetailList[i].UserName) == user {
			found = &out.UserDetailList[i]
			break
		}
	}
	require.NotNil(t, found, "user %s missing from UserDetailList", user)
	require.Len(t, found.UserPolicyList, 1)
	assert.Equal(t, "aad-inline", aws.ToString(found.UserPolicyList[0].PolicyName))
	assert.Contains(t, aws.ToString(found.UserPolicyList[0].PolicyDocument), "s3")
}

// TestIAM_CredentialReport drives the GenerateCredentialReport →
// GetCredentialReport flow and asserts the CSV report enumerates the real users.
func TestIAM_CredentialReport(t *testing.T) {
	c := iamClient()
	user := "credreport-user"
	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	// GenerateCredentialReport reports STARTED first, COMPLETE on the second call.
	gen, err := c.GenerateCredentialReport(ctx, &iam.GenerateCredentialReportInput{})
	require.NoError(t, err)
	assert.Contains(t, []types.ReportStateType{types.ReportStateTypeStarted, types.ReportStateTypeComplete}, gen.State)
	gen2, err := c.GenerateCredentialReport(ctx, &iam.GenerateCredentialReportInput{})
	require.NoError(t, err)
	assert.Equal(t, types.ReportStateTypeComplete, gen2.State)

	rep, err := c.GetCredentialReport(ctx, &iam.GetCredentialReportInput{})
	require.NoError(t, err)
	assert.Equal(t, types.ReportFormatTypeTextCsv, rep.ReportFormat)
	csv := string(rep.Content) // SDK already base64-decodes Content.
	assert.Contains(t, csv, "user,arn,user_creation_time")
	assert.Contains(t, csv, "<root_account>")
	assert.Contains(t, csv, user)
}

// TestIAM_ServiceLastAccessed drives the GenerateServiceLastAccessedDetails →
// GetServiceLastAccessedDetails(/WithEntities) flow plus the Organizations
// access report. The rows are derived from the principal's policy namespaces.
func TestIAM_ServiceLastAccessed(t *testing.T) {
	c := iamClient()
	role := "sla-role"
	_, err := c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(role),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)}) })

	_, err = c.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(role),
		PolicyName:     aws.String("sla-inline"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","sqs:SendMessage"],"Resource":"*"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		c.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String("sla-inline")})
	})

	roleArn := "arn:aws:iam::123456789012:role/" + role

	job, err := c.GenerateServiceLastAccessedDetails(ctx, &iam.GenerateServiceLastAccessedDetailsInput{
		Arn:         aws.String(roleArn),
		Granularity: types.AccessAdvisorUsageGranularityTypeServiceLevel,
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(job.JobId))

	details, err := c.GetServiceLastAccessedDetails(ctx, &iam.GetServiceLastAccessedDetailsInput{
		JobId: job.JobId,
	})
	require.NoError(t, err)
	assert.Equal(t, types.JobStatusTypeCompleted, details.JobStatus)
	var namespaces []string
	for _, s := range details.ServicesLastAccessed {
		namespaces = append(namespaces, aws.ToString(s.ServiceNamespace))
	}
	assert.Contains(t, namespaces, "s3")
	assert.Contains(t, namespaces, "sqs")

	withEnt, err := c.GetServiceLastAccessedDetailsWithEntities(ctx, &iam.GetServiceLastAccessedDetailsWithEntitiesInput{
		JobId:            job.JobId,
		ServiceNamespace: aws.String("s3"),
	})
	require.NoError(t, err)
	assert.Equal(t, types.JobStatusTypeCompleted, withEnt.JobStatus)
	require.NotEmpty(t, withEnt.EntityDetailsList)
	assert.Equal(t, role, aws.ToString(withEnt.EntityDetailsList[0].EntityInfo.Name))

	// Organizations access report — Generate returns a JobId, Get settles it.
	orgJob, err := c.GenerateOrganizationsAccessReport(ctx, &iam.GenerateOrganizationsAccessReportInput{
		EntityPath: aws.String("o-abc123/r-root/000000000000"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(orgJob.JobId))
	orgRep, err := c.GetOrganizationsAccessReport(ctx, &iam.GetOrganizationsAccessReportInput{
		JobId: orgJob.JobId,
	})
	require.NoError(t, err)
	assert.Equal(t, types.JobStatusTypeCompleted, orgRep.JobStatus)
}

// TestIAM_OrganizationsRoot drives the Organizations-root credential management
// + sessions toggles and ListOrganizationsFeatures, plus the STS token-version
// preference and the human-readable summary.
func TestIAM_OrganizationsRoot(t *testing.T) {
	c := iamClient()

	en, err := c.EnableOrganizationsRootCredentialsManagement(ctx, &iam.EnableOrganizationsRootCredentialsManagementInput{})
	require.NoError(t, err)
	assert.Contains(t, en.EnabledFeatures, types.FeatureTypeRootCredentialsManagement)
	assert.NotEmpty(t, aws.ToString(en.OrganizationId))

	es, err := c.EnableOrganizationsRootSessions(ctx, &iam.EnableOrganizationsRootSessionsInput{})
	require.NoError(t, err)
	assert.Contains(t, es.EnabledFeatures, types.FeatureTypeRootSessions)

	feats, err := c.ListOrganizationsFeatures(ctx, &iam.ListOrganizationsFeaturesInput{})
	require.NoError(t, err)
	assert.Contains(t, feats.EnabledFeatures, types.FeatureTypeRootCredentialsManagement)
	assert.Contains(t, feats.EnabledFeatures, types.FeatureTypeRootSessions)

	_, err = c.DisableOrganizationsRootSessions(ctx, &iam.DisableOrganizationsRootSessionsInput{})
	require.NoError(t, err)
	dc, err := c.DisableOrganizationsRootCredentialsManagement(ctx, &iam.DisableOrganizationsRootCredentialsManagementInput{})
	require.NoError(t, err)
	assert.NotContains(t, dc.EnabledFeatures, types.FeatureTypeRootCredentialsManagement)

	// STS preference + outbound web-identity federation.
	_, err = c.SetSecurityTokenServicePreferences(ctx, &iam.SetSecurityTokenServicePreferencesInput{
		GlobalEndpointTokenVersion: types.GlobalEndpointTokenVersionV2Token,
	})
	require.NoError(t, err)

	fed, err := c.EnableOutboundWebIdentityFederation(ctx, &iam.EnableOutboundWebIdentityFederationInput{})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(fed.IssuerIdentifier), "oidc.iam")
	info, err := c.GetOutboundWebIdentityFederationInfo(ctx, &iam.GetOutboundWebIdentityFederationInfoInput{})
	require.NoError(t, err)
	assert.True(t, info.JwtVendingEnabled)
	_, err = c.DisableOutboundWebIdentityFederation(ctx, &iam.DisableOutboundWebIdentityFederationInput{})
	require.NoError(t, err)
	info2, err := c.GetOutboundWebIdentityFederationInfo(ctx, &iam.GetOutboundWebIdentityFederationInfoInput{})
	require.NoError(t, err)
	assert.False(t, info2.JwtVendingEnabled)

	// GetHumanReadableSummary over a role's effective namespaces.
	role := "summary-role"
	_, err = c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(role),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)}) })
	_, err = c.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(role),
		PolicyName:     aws.String("summary-inline"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeInstances","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		c.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String("summary-inline")})
	})
	sum, err := c.GetHumanReadableSummary(ctx, &iam.GetHumanReadableSummaryInput{
		EntityArn: aws.String("arn:aws:iam::123456789012:role/" + role),
	})
	require.NoError(t, err)
	assert.Contains(t, strings.ToLower(aws.ToString(sum.SummaryContent)), "ec2")
}

// TestIAM_DelegationRequest drives the cross-account delegation lifecycle:
// CreateDelegationRequest → GetDelegationRequest → ListDelegationRequests →
// AssociateDelegationRequest → UpdateDelegationRequest → SendDelegationToken,
// plus a second request that is rejected.
func TestIAM_DelegationRequest(t *testing.T) {
	c := iamClient()

	created, err := c.CreateDelegationRequest(ctx, &iam.CreateDelegationRequestInput{
		OwnerAccountId:      aws.String("111111111111"),
		Description:         aws.String("grant ECS read"),
		RequestMessage:      aws.String("please approve"),
		RequestorWorkflowId: aws.String("wf-001"),
		NotificationChannel: aws.String("arn:aws:sns:us-east-1:111111111111:delegations"),
		SessionDuration:     aws.Int32(3600),
		Permissions: &types.DelegationPermission{
			PolicyTemplateArn: aws.String("arn:aws:iam::aws:policy/template/ECSReadOnly"),
		},
	})
	require.NoError(t, err)
	id := aws.ToString(created.DelegationRequestId)
	require.NotEmpty(t, id)
	assert.Contains(t, aws.ToString(created.ConsoleDeepLink), id)

	got, err := c.GetDelegationRequest(ctx, &iam.GetDelegationRequestInput{
		DelegationRequestId: aws.String(id),
	})
	require.NoError(t, err)
	require.NotNil(t, got.DelegationRequest)
	assert.Equal(t, types.StateTypePendingApproval, got.DelegationRequest.State)
	assert.Equal(t, "grant ECS read", aws.ToString(got.DelegationRequest.Description))

	list, err := c.ListDelegationRequests(ctx, &iam.ListDelegationRequestsInput{
		OwnerId: aws.String("111111111111"),
	})
	require.NoError(t, err)
	var ids []string
	for _, d := range list.DelegationRequests {
		ids = append(ids, aws.ToString(d.DelegationRequestId))
	}
	assert.Contains(t, ids, id)

	_, err = c.AssociateDelegationRequest(ctx, &iam.AssociateDelegationRequestInput{
		DelegationRequestId: aws.String(id),
	})
	require.NoError(t, err)
	_, err = c.UpdateDelegationRequest(ctx, &iam.UpdateDelegationRequestInput{
		DelegationRequestId: aws.String(id),
		Notes:               aws.String("scheduled"),
	})
	require.NoError(t, err)
	_, err = c.SendDelegationToken(ctx, &iam.SendDelegationTokenInput{
		DelegationRequestId: aws.String(id),
	})
	require.NoError(t, err)
	fin, err := c.GetDelegationRequest(ctx, &iam.GetDelegationRequestInput{DelegationRequestId: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, types.StateTypeFinalized, fin.DelegationRequest.State)

	// Accept on one request, reject on another.
	created2, err := c.CreateDelegationRequest(ctx, &iam.CreateDelegationRequestInput{
		OwnerAccountId:      aws.String("222222222222"),
		Description:         aws.String("second"),
		RequestorWorkflowId: aws.String("wf-002"),
		NotificationChannel: aws.String("arn:aws:sns:us-east-1:222222222222:delegations"),
		SessionDuration:     aws.Int32(3600),
		Permissions: &types.DelegationPermission{
			PolicyTemplateArn: aws.String("arn:aws:iam::aws:policy/template/ECSReadOnly"),
		},
	})
	require.NoError(t, err)
	id2 := aws.ToString(created2.DelegationRequestId)
	_, err = c.AcceptDelegationRequest(ctx, &iam.AcceptDelegationRequestInput{DelegationRequestId: aws.String(id2)})
	require.NoError(t, err)
	acc, err := c.GetDelegationRequest(ctx, &iam.GetDelegationRequestInput{DelegationRequestId: aws.String(id2)})
	require.NoError(t, err)
	assert.Equal(t, types.StateTypeAccepted, acc.DelegationRequest.State)

	created3, err := c.CreateDelegationRequest(ctx, &iam.CreateDelegationRequestInput{
		OwnerAccountId:      aws.String("333333333333"),
		Description:         aws.String("third"),
		RequestorWorkflowId: aws.String("wf-003"),
		NotificationChannel: aws.String("arn:aws:sns:us-east-1:333333333333:delegations"),
		SessionDuration:     aws.Int32(3600),
		Permissions: &types.DelegationPermission{
			PolicyTemplateArn: aws.String("arn:aws:iam::aws:policy/template/ECSReadOnly"),
		},
	})
	require.NoError(t, err)
	id3 := aws.ToString(created3.DelegationRequestId)
	_, err = c.RejectDelegationRequest(ctx, &iam.RejectDelegationRequestInput{
		DelegationRequestId: aws.String(id3),
		Notes:               aws.String("denied"),
	})
	require.NoError(t, err)
	rej, err := c.GetDelegationRequest(ctx, &iam.GetDelegationRequestInput{DelegationRequestId: aws.String(id3)})
	require.NoError(t, err)
	assert.Equal(t, types.StateTypeRejected, rej.DelegationRequest.State)
}
