package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/gcpbootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/option"
)

// TestOAuth2_BootstrapServiceAccount reads the seeded bootstrap account and its
// key back through IAM, then authenticates with the key's credential file
// through the client library's own service-account flow and holds what the
// account's owner holds.
func TestOAuth2_BootstrapServiceAccount(t *testing.T) {
	t.Parallel()
	svc := iamService(t)
	saName := "projects/" + gcpbootstrap.ProjectID + "/serviceAccounts/" + gcpbootstrap.Email

	sa, err := svc.Projects.ServiceAccounts.Get(saName).Do()
	require.NoError(t, err)
	assert.Equal(t, gcpbootstrap.Email, sa.Email)
	assert.Equal(t, gcpbootstrap.ProjectID, sa.ProjectId)
	assert.Equal(t, gcpbootstrap.ClientID, sa.UniqueId)
	assert.False(t, sa.Disabled)

	keys, err := svc.Projects.ServiceAccounts.Keys.List(saName).KeyTypes("USER_MANAGED").Do()
	require.NoError(t, err)
	require.Len(t, keys.Keys, 1)
	key := keys.Keys[0]
	assert.Equal(t, saName+"/keys/"+gcpbootstrap.KeyID, key.Name)
	assert.Equal(t, "USER_MANAGED", key.KeyType)
	assert.Equal(t, "USER_PROVIDED", key.KeyOrigin)
	assert.Equal(t, "KEY_ALG_RSA_2048", key.KeyAlgorithm)

	creds, err := google.CredentialsFromJSON(ctx, gcpbootstrap.CredentialJSON(baseURL+"/token"),
		"https://www.googleapis.com/auth/cloud-platform")
	require.NoError(t, err)
	crm, err := cloudresourcemanager.NewService(ctx, option.WithEndpoint(baseURL), option.WithCredentials(creds))
	require.NoError(t, err)
	ensureV1Project(t, crm, "bootstrap-owner")
	requested := []string{"resourcemanager.projects.get", "resourcemanager.projects.setIamPolicy", "iam.serviceAccounts.create"}
	held, err := crm.Projects.TestIamPermissions("bootstrap-owner",
		&cloudresourcemanager.TestIamPermissionsRequest{Permissions: requested}).Do()
	require.NoError(t, err)
	assert.Equal(t, requested, held.Permissions)
}

// TestOAuth2_TokenEndpointRefusesGrantWithoutAssertion holds each of Google's
// three token endpoints to the RFC 6749 refusal of a JWT-bearer grant that
// carries no assertion.
func TestOAuth2_TokenEndpointRefusesGrantWithoutAssertion(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/token", "/oauth2/v4/token", "/o/oauth2/token"} {
		resp, err := http.PostForm(baseURL+path, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		})
		require.NoError(t, err)
		var body map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, path)
		assert.Equal(t, map[string]string{
			"error":             "invalid_request",
			"error_description": "Missing required parameter: assertion",
		}, body, path)
	}
}

func TestOAuth2_TokenEndpointRefusesUnsupportedGrantType(t *testing.T) {
	t.Parallel()
	resp, err := http.PostForm(baseURL+"/token", url.Values{"grant_type": {"client_credentials"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, map[string]string{
		"error":             "unsupported_grant_type",
		"error_description": "Invalid grant_type: client_credentials",
	}, body)
}
