package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/require"
)

// ssmCloudConnectorAzureConfig builds the Azure member of the
// CloudConnectorConfiguration union with one subscription target.
func ssmCloudConnectorAzureConfig(tenant, subscription string) ssmtypes.CloudConnectorConfiguration {
	return &ssmtypes.CloudConnectorConfigurationMemberAzureConfiguration{
		Value: ssmtypes.AzureConfiguration{
			TenantId:               aws.String(tenant),
			TenantDisplayName:      aws.String("sockerless-tenant"),
			ApplicationId:          aws.String("11111111-2222-3333-4444-555555555555"),
			ApplicationDisplayName: aws.String("sockerless-app"),
			Targets: &ssmtypes.ConfigurationTargetsMemberSubscriptions{
				Value: []ssmtypes.AzureSubscription{{
					Id:          aws.String(subscription),
					DisplayName: aws.String("sockerless-subscription"),
				}},
			},
		},
	}
}

// TestSSM_CloudConnectorLifecycle covers CreateCloudConnector →
// GetCloudConnector → ListCloudConnectors (with the SubscriptionId filter) →
// UpdateCloudConnector → DeleteCloudConnector.
func TestSSM_CloudConnectorLifecycle(t *testing.T) {
	c := ssmClient()
	iamc := iamClient()

	roleName := "SSMCloudConnectorRole"
	_, _ = iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ssm.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	role, err := iamc.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	require.NoError(t, err)
	roleArn := aws.ToString(role.Role.Arn)

	const tenant = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	const subscription = "99999999-8888-7777-6666-555555555555"

	created, err := c.CreateCloudConnector(ctx, &ssm.CreateCloudConnectorInput{
		DisplayName:        aws.String("sdk-azure-connector"),
		Description:        aws.String("Azure nodes for Systems Manager"),
		RoleArn:            aws.String(roleArn),
		ConfigConnectorArn: aws.String("arn:aws:config:us-east-1:000000000000:connector/azure"),
		Configuration:      ssmCloudConnectorAzureConfig(tenant, subscription),
	})
	require.NoError(t, err)
	id := aws.ToString(created.CloudConnectorId)
	require.NotEmpty(t, id)
	defer func() {
		_, _ = c.DeleteCloudConnector(ctx, &ssm.DeleteCloudConnectorInput{CloudConnectorId: aws.String(id)})
	}()

	got, err := c.GetCloudConnector(ctx, &ssm.GetCloudConnectorInput{CloudConnectorId: aws.String(id)})
	require.NoError(t, err)
	require.Equal(t, "sdk-azure-connector", aws.ToString(got.DisplayName))
	require.Equal(t, roleArn, aws.ToString(got.RoleArn))
	require.Contains(t, aws.ToString(got.CloudConnectorArn), ":cloud-connector/"+id)
	azure, ok := got.Configuration.(*ssmtypes.CloudConnectorConfigurationMemberAzureConfiguration)
	require.True(t, ok, "Configuration must round-trip as the Azure union member")
	require.Equal(t, tenant, aws.ToString(azure.Value.TenantId))

	listed, err := c.ListCloudConnectors(ctx, &ssm.ListCloudConnectorsInput{
		Filters: []ssmtypes.CloudConnectorFilter{{
			FilterKey:    ssmtypes.CloudConnectorFilterKeySubscriptionId,
			FilterValues: []string{subscription},
		}},
	})
	require.NoError(t, err)
	require.Len(t, listed.CloudConnectors, 1)
	require.Equal(t, id, aws.ToString(listed.CloudConnectors[0].CloudConnectorId))

	// A filter no connector matches returns an empty list, not everything.
	empty, err := c.ListCloudConnectors(ctx, &ssm.ListCloudConnectorsInput{
		Filters: []ssmtypes.CloudConnectorFilter{{
			FilterKey:    ssmtypes.CloudConnectorFilterKeyTenantId,
			FilterValues: []string{"00000000-0000-0000-0000-000000000000"},
		}},
	})
	require.NoError(t, err)
	require.Empty(t, empty.CloudConnectors)

	_, err = c.UpdateCloudConnector(ctx, &ssm.UpdateCloudConnectorInput{
		CloudConnectorId: aws.String(id),
		Description:      aws.String("updated description"),
	})
	require.NoError(t, err)
	got, err = c.GetCloudConnector(ctx, &ssm.GetCloudConnectorInput{CloudConnectorId: aws.String(id)})
	require.NoError(t, err)
	require.Equal(t, "updated description", aws.ToString(got.Description))

	_, err = c.DeleteCloudConnector(ctx, &ssm.DeleteCloudConnectorInput{CloudConnectorId: aws.String(id)})
	require.NoError(t, err)
	_, err = c.GetCloudConnector(ctx, &ssm.GetCloudConnectorInput{CloudConnectorId: aws.String(id)})
	require.Error(t, err, "a deleted connector must not resolve")
}

// TestSSM_ValidateCloudConnector covers ValidateCloudConnector: a connector
// whose role does not exist reports AWS_ROLE_ASSUMPTION_FAILED, and one whose
// role trusts Systems Manager reports only the informational tenant /
// subscription findings.
func TestSSM_ValidateCloudConnector(t *testing.T) {
	c := ssmClient()
	iamc := iamClient()

	const tenant = "12121212-3434-5656-7878-909090909090"
	const subscription = "44444444-3333-2222-1111-000000000000"

	broken, err := c.CreateCloudConnector(ctx, &ssm.CreateCloudConnectorInput{
		DisplayName:        aws.String("sdk-connector-missing-role"),
		RoleArn:            aws.String("arn:aws:iam::000000000000:role/NoSuchCloudConnectorRole"),
		ConfigConnectorArn: aws.String("arn:aws:config:us-east-1:000000000000:connector/azure"),
		Configuration:      ssmCloudConnectorAzureConfig(tenant, subscription),
	})
	require.NoError(t, err)
	brokenID := aws.ToString(broken.CloudConnectorId)
	defer func() {
		_, _ = c.DeleteCloudConnector(ctx, &ssm.DeleteCloudConnectorInput{CloudConnectorId: aws.String(brokenID)})
	}()

	res, err := c.ValidateCloudConnector(ctx, &ssm.ValidateCloudConnectorInput{
		CloudConnectorId: aws.String(brokenID),
	})
	require.NoError(t, err)
	var sawRoleFailure bool
	for _, f := range res.ValidationFindings {
		if f.Code == ssmtypes.ValidationFindingCodeAwsRoleAssumptionFailed {
			sawRoleFailure = true
			require.Equal(t, ssmtypes.ValidationFindingTypeError, f.Type)
		}
	}
	require.True(t, sawRoleFailure, "a missing role must be reported, not assumed valid")

	roleName := "SSMCloudConnectorValidRole"
	_, _ = iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ssm.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	role, err := iamc.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	require.NoError(t, err)

	good, err := c.CreateCloudConnector(ctx, &ssm.CreateCloudConnectorInput{
		DisplayName:        aws.String("sdk-connector-valid-role"),
		RoleArn:            role.Role.Arn,
		ConfigConnectorArn: aws.String("arn:aws:config:us-east-1:000000000000:connector/azure"),
		Configuration:      ssmCloudConnectorAzureConfig(tenant, subscription),
	})
	require.NoError(t, err)
	goodID := aws.ToString(good.CloudConnectorId)
	defer func() {
		_, _ = c.DeleteCloudConnector(ctx, &ssm.DeleteCloudConnectorInput{CloudConnectorId: aws.String(goodID)})
	}()

	res, err = c.ValidateCloudConnector(ctx, &ssm.ValidateCloudConnectorInput{
		CloudConnectorId: aws.String(goodID),
	})
	require.NoError(t, err)
	var sawTenant, sawSubscription bool
	for _, f := range res.ValidationFindings {
		require.NotEqual(t, ssmtypes.ValidationFindingTypeError, f.Type)
		switch f.Code {
		case ssmtypes.ValidationFindingCodeTenantSummary:
			sawTenant = true
			require.NotNil(t, f.Scope)
			require.Equal(t, tenant, aws.ToString(f.Scope.Id))
		case ssmtypes.ValidationFindingCodeSubscriptionAccessible:
			sawSubscription = true
			require.NotNil(t, f.Scope)
			require.Equal(t, subscription, aws.ToString(f.Scope.Id))
		}
	}
	require.True(t, sawTenant)
	require.True(t, sawSubscription)
}
