package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cloud.google.com/go/compute/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
)

// The metadata server is one of the two official credential paths a Google
// client has when it cannot run an interactive login — the other being a
// service-account key file (oauth2_sa_key_test.go). A client reaches its
// credentials by walking the server's directory listings, not only by reading
// leaves: it enumerates the instance's identities at
// `instance/service-accounts/`, reads one identity's keys with
// `?recursive=true`, and only then fetches a token. These tests drive that walk
// through cloud.google.com/go/compute/metadata — the official Go client for the
// metadata server — and finish by proving Application Default Credentials
// resolved through it authenticate a real data-plane call.

// metadataDirectoryClient points the official metadata client at the simulator.
// GCE_METADATA_HOST is the coordinate the client itself honours; nothing else
// about the client differs from an on-instance one.
func metadataDirectoryClient(t *testing.T) *metadata.Client {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	t.Setenv("GCE_METADATA_HOST", u.Host)
	t.Setenv("GCE_METADATA_IP", u.Host)
	return metadata.NewClient(nil)
}

// TestMetadataSDK_ServiceAccountsDirectoryListing walks the listings a client
// uses to discover which identities the instance carries. Each entry is a
// directory, so real Google marks it with a trailing slash.
func TestMetadataSDK_ServiceAccountsDirectoryListing(t *testing.T) {
	c := metadataDirectoryClient(t)

	listing, err := c.GetWithContext(ctx, "instance/service-accounts/")
	require.NoError(t, err, "a client must be able to enumerate the instance's identities")
	entries := strings.Fields(listing)
	assert.Contains(t, entries, "default/",
		"the default alias must be listed as a directory: %q", listing)

	var email string
	for _, entry := range entries {
		if strings.Contains(entry, "@") {
			email = strings.TrimSuffix(entry, "/")
		}
	}
	require.NotEmpty(t, email, "the listing must name the identity's own email: %q", listing)

	// Every identity the listing names must resolve to its own listing.
	accountKeys, err := c.GetWithContext(ctx, "instance/service-accounts/"+email+"/")
	require.NoError(t, err)
	for _, key := range []string{"aliases", "email", "identity", "scopes", "token"} {
		assert.Contains(t, strings.Fields(accountKeys), key,
			"the identity's listing must name %q: %q", key, accountKeys)
	}

	// The top-level listings the walk descends through.
	v1Listing, err := c.GetWithContext(ctx, "")
	require.NoError(t, err)
	assert.Contains(t, strings.Fields(v1Listing), "instance/")
	assert.Contains(t, strings.Fields(v1Listing), "project/")

	instanceListing, err := c.GetWithContext(ctx, "instance/")
	require.NoError(t, err)
	assert.Contains(t, strings.Fields(instanceListing), "service-accounts/")
}

// TestMetadataSDK_RecursiveDirectoryRead reads an identity the way a client
// does before it will mint a token: one recursive request that returns the
// stored keys as JSON. The minted-per-request keys (`token`, `identity`) are
// absent, as they are on real Google.
func TestMetadataSDK_RecursiveDirectoryRead(t *testing.T) {
	c := metadataDirectoryClient(t)

	raw, err := c.GetWithContext(ctx, "instance/service-accounts/default/?recursive=true")
	require.NoError(t, err)

	var account struct {
		Aliases []string `json:"aliases"`
		Email   string   `json:"email"`
		Scopes  []string `json:"scopes"`
		Token   any      `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &account), "recursive read must be JSON: %q", raw)
	assert.Equal(t, []string{"default"}, account.Aliases)
	assert.Contains(t, account.Email, "iam.gserviceaccount.com")
	assert.Contains(t, account.Scopes, "https://www.googleapis.com/auth/cloud-platform")
	assert.Nil(t, account.Token, "recursive output must omit the minted token leaf")

	// Recursive output spells kebab-case keys camelCase, and carries the
	// project number as a JSON number.
	rawProject, err := c.GetWithContext(ctx, "project/?recursive=true")
	require.NoError(t, err)
	var project struct {
		NumericProjectID int64  `json:"numericProjectId"`
		ProjectID        string `json:"projectId"`
	}
	require.NoError(t, json.Unmarshal([]byte(rawProject), &project), "recursive read must be JSON: %q", rawProject)
	assert.NotZero(t, project.NumericProjectID)
	assert.NotEmpty(t, project.ProjectID)
}

// TestMetadataSDK_UniverseDomain reads /computeMetadata/v1/universe/universe-domain,
// the key every Google auth library consults before it chooses an API host. The
// simulator implements the public Google Cloud universe.
func TestMetadataSDK_UniverseDomain(t *testing.T) {
	c := metadataDirectoryClient(t)
	got, err := c.GetWithContext(ctx, "universe/universe-domain")
	require.NoError(t, err)
	assert.Equal(t, "googleapis.com", strings.TrimSpace(got))

	// Every metadata read is gated by the `Metadata-Flavor: Google` header,
	// which is what tells the server the request was meant for it rather than
	// forwarded by a confused deputy. Without the header real Google answers
	// 403, and so does the simulator.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/computeMetadata/v1/universe/universe-domain", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a metadata read without Metadata-Flavor: Google must be refused")
}

// TestMetadataSDK_ApplicationDefaultCredentials proves the whole point of the
// metadata server: a client with no credential file and no interactive login
// resolves Application Default Credentials through it and authenticates a real
// data-plane call with the resulting token.
func TestMetadataSDK_ApplicationDefaultCredentials(t *testing.T) {
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	t.Setenv("GCE_METADATA_HOST", u.Host)
	t.Setenv("GCE_METADATA_IP", u.Host)
	// No credential file may shadow the metadata server: clear the explicit
	// pointer and move the well-known ADC file's directory to an empty one.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())

	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	require.NoError(t, err, "Application Default Credentials must resolve through the metadata server")

	tok, err := creds.TokenSource.Token()
	require.NoError(t, err)
	require.NotEmpty(t, tok.AccessToken)

	svc, err := iam.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(creds.TokenSource))
	require.NoError(t, err)
	_, err = svc.Projects.ServiceAccounts.List("projects/test-project").Do()
	require.NoError(t, err, "the metadata-server token must be accepted by the data plane")
}

// TestOAuth2_LegacyTokenEndpoint proves the original
// `accounts.google.com/o/oauth2/token` endpoint serves the same
// service-account JWT-bearer grant as the current one: a credential file whose
// token_uri names /o/oauth2/token mints a token that the data plane accepts.
// Clients pinned to that host, and `gcloud`'s auth/token_host override, still
// name it.
func TestOAuth2_LegacyTokenEndpoint(t *testing.T) {
	_, _, keyFile := mintServiceAccountKeyFile(t, "legacy-token-endpoint-sa")
	keyFile["token_uri"] = baseURL + "/o/oauth2/token"

	creds := tokenSourceFromKeyFile(t, keyFile)
	tok, err := creds.TokenSource.Token()
	require.NoError(t, err, "the legacy token endpoint must serve the JWT-bearer grant")
	require.NotEmpty(t, tok.AccessToken)

	svc, err := iam.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(creds.TokenSource))
	require.NoError(t, err)
	_, err = svc.Projects.ServiceAccounts.List("projects/test-project").Do()
	require.NoError(t, err, "a token minted at the legacy endpoint must be accepted by the data plane")
}
