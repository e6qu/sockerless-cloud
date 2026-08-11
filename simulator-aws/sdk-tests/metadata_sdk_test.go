package aws_sdk_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK-driven IMDS client tests. Validates our handlers match what
// aws-sdk-go-v2/feature/ec2/imds expects (IMDSv2 token dance, metadata
// header echoes, PUT vs GET routing). Raw-HTTP coverage lives in
// metadata_test.go; this file proves the real AWS SDK client accepts our
// responses end-to-end (external-validation principle).

func imdsSDKClient(t *testing.T) *imds.Client {
	t.Helper()
	return imds.New(imds.Options{
		Endpoint:     baseURL,
		EndpointMode: imds.EndpointModeStateIPv4,
	})
}

func TestIMDSSDK_GetInstanceID(t *testing.T) {
	c := imdsSDKClient(t)
	out, err := c.GetMetadata(ctx, &imds.GetMetadataInput{Path: "instance-id"})
	require.NoError(t, err)
	defer out.Content.Close()
	body, _ := io.ReadAll(out.Content)
	assert.True(t, strings.HasPrefix(string(body), "i-"),
		"expected i-* prefix, got %q", string(body))
}

func TestIMDSSDK_GetRegion(t *testing.T) {
	c := imdsSDKClient(t)
	out, err := c.GetRegion(ctx, &imds.GetRegionInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, out.Region)
}

func TestIMDSSDK_GetIAMSecurityCredentials(t *testing.T) {
	c := imdsSDKClient(t)

	// First list role names.
	listOut, err := c.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "iam/security-credentials/",
	})
	require.NoError(t, err)
	defer listOut.Content.Close()
	roleBytes, _ := io.ReadAll(listOut.Content)
	role := strings.TrimSpace(string(roleBytes))
	require.NotEmpty(t, role)

	// Then fetch the credentials JSON.
	credsOut, err := c.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "iam/security-credentials/" + role,
	})
	require.NoError(t, err)
	defer credsOut.Content.Close()

	var creds struct {
		Code            string
		AccessKeyId     string
		SecretAccessKey string
		Token           string
		Expiration      string
	}
	require.NoError(t, json.NewDecoder(credsOut.Content).Decode(&creds))
	assert.Equal(t, "Success", creds.Code)
	assert.NotEmpty(t, creds.AccessKeyId)
	assert.NotEmpty(t, creds.SecretAccessKey)
	assert.NotEmpty(t, creds.Token)
}

func TestIMDSSDK_TokenAutomaticRefresh(t *testing.T) {
	// The SDK manages the token TTL internally. We make two back-to-back
	// reads and verify both succeed (proves token is reused/refreshed).
	c := imdsSDKClient(t)
	for i := 0; i < 2; i++ {
		out, err := c.GetMetadata(ctx, &imds.GetMetadataInput{Path: "ami-id"})
		require.NoError(t, err)
		body, _ := io.ReadAll(out.Content)
		out.Content.Close()
		assert.NotEmpty(t, string(body))
	}
}
