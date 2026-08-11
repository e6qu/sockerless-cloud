package azure_sdk_test

import (
	"net/url"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK-driven Azure managed-identity test. Validates
// azidentity.NewManagedIdentityCredential routes via IDENTITY_ENDPOINT +
// IDENTITY_HEADER (App Service / Container Apps style) and accepts the
// sim's response shape end-to-end (external-validation principle).

func TestAzureSDK_ManagedIdentityToken(t *testing.T) {
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	t.Setenv("IDENTITY_ENDPOINT", "http://"+u.Host+"/msi/token")
	t.Setenv("IDENTITY_HEADER", "sim-identity-header")

	cred, err := azidentity.NewManagedIdentityCredential(nil)
	require.NoError(t, err)

	tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, tok.Token)
	assert.False(t, tok.ExpiresOn.IsZero())
}
