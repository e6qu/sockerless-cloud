package aws_sdk_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIAMServiceLinkedRoleLifecycle(t *testing.T) {
	c := iamClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// CreateServiceLinkedRole for CloudFront logger
	suffix := "test" + time.Now().Format("150405")
	createOut, err := c.CreateServiceLinkedRole(ctx, &iam.CreateServiceLinkedRoleInput{
		AWSServiceName: aws.String("cloudfront.amazonaws.com"),
		CustomSuffix:   aws.String(suffix),
		Description:    aws.String("SDK test CloudFront SLR"),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.Role)
	roleName := aws.ToString(createOut.Role.RoleName)
	assert.Equal(t, "AWSServiceRoleForCloudFrontLogger_"+suffix, roleName)
	assert.Contains(t, aws.ToString(createOut.Role.Arn), ":role/aws-service-role/cloudfront.amazonaws.com/")

	// DeleteServiceLinkedRole
	delOut, err := c.DeleteServiceLinkedRole(ctx, &iam.DeleteServiceLinkedRoleInput{
		RoleName: aws.String(roleName),
	})
	require.NoError(t, err)
	taskID := aws.ToString(delOut.DeletionTaskId)
	require.NotEmpty(t, taskID)

	// GetServiceLinkedRoleDeletionStatus
	statusOut, err := c.GetServiceLinkedRoleDeletionStatus(ctx, &iam.GetServiceLinkedRoleDeletionStatusInput{
		DeletionTaskId: aws.String(taskID),
	})
	require.NoError(t, err)
	assert.Equal(t, iamtypes.DeletionTaskStatusTypeSucceeded, statusOut.Status)
}

func TestIAMServiceLinkedRoleForAmplify(t *testing.T) {
	c := iamClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	suffix := time.Now().Format("150405")
	createOut, err := c.CreateServiceLinkedRole(ctx, &iam.CreateServiceLinkedRoleInput{
		AWSServiceName: aws.String("amplify.amazonaws.com"),
		CustomSuffix:   aws.String(suffix),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.Role)
	roleName := aws.ToString(createOut.Role.RoleName)
	assert.True(t, strings.HasPrefix(roleName, "AWSServiceRoleForAmplify"))
	defer func() {
		_, _ = c.DeleteServiceLinkedRole(ctx, &iam.DeleteServiceLinkedRoleInput{RoleName: aws.String(roleName)})
	}()
}

func TestIAMOpenIDConnectProviderLifecycle(t *testing.T) {
	c := iamClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := "https://oidc.eks.us-east-1.amazonaws.com/id/sdk" + time.Now().Format("150405")
	createOut, err := c.CreateOpenIDConnectProvider(ctx, &iam.CreateOpenIDConnectProviderInput{
		Url:            aws.String(url),
		ClientIDList:   []string{"sts.amazonaws.com"},
		ThumbprintList: []string{"9e99a48a9960b14926bb7f3b02e22da2b0ab7280"},
	})
	require.NoError(t, err)
	arn := aws.ToString(createOut.OpenIDConnectProviderArn)
	require.NotEmpty(t, arn)
	assert.Contains(t, arn, "oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/sdk")

	getOut, err := c.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Equal(t, url, aws.ToString(getOut.Url))
	require.Len(t, getOut.ClientIDList, 1)
	require.Len(t, getOut.ThumbprintList, 1)

	// Add a client ID
	_, err = c.AddClientIDToOpenIDConnectProvider(ctx, &iam.AddClientIDToOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
		ClientID:                 aws.String("system:serviceaccount:default:default"),
	})
	require.NoError(t, err)

	getOut2, err := c.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.Len(t, getOut2.ClientIDList, 2)

	// Remove the client ID
	_, err = c.RemoveClientIDFromOpenIDConnectProvider(ctx, &iam.RemoveClientIDFromOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
		ClientID:                 aws.String("system:serviceaccount:default:default"),
	})
	require.NoError(t, err)

	// Update thumbprints
	_, err = c.UpdateOpenIDConnectProviderThumbprint(ctx, &iam.UpdateOpenIDConnectProviderThumbprintInput{
		OpenIDConnectProviderArn: aws.String(arn),
		ThumbprintList:           []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
	})
	require.NoError(t, err)

	getOut3, err := c.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.Len(t, getOut3.ThumbprintList, 1)
	assert.Equal(t, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", getOut3.ThumbprintList[0])

	// List should include ours
	listOut, err := c.ListOpenIDConnectProviders(ctx, &iam.ListOpenIDConnectProvidersInput{})
	require.NoError(t, err)
	found := false
	for _, p := range listOut.OpenIDConnectProviderList {
		if aws.ToString(p.Arn) == arn {
			found = true
		}
	}
	assert.True(t, found)

	// Delete
	_, err = c.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
	})
	require.NoError(t, err)

	// Verify gone
	_, err = c.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
	})
	require.Error(t, err)
}
