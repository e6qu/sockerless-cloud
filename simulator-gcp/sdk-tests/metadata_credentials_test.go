package gcp_sdk_test

import (
	"net/http"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
)

// TestOAuth2_LegacyTokenEndpoint proves the original
// `accounts.google.com/o/oauth2/token` endpoint serves the same
// service-account JWT-bearer grant as the current one: a credential file whose
// token_uri names /o/oauth2/token mints a token that the data plane accepts.
// Clients pinned to that host, and `gcloud`'s auth/token_host override, still
// name it.
func TestOAuth2_LegacyTokenEndpoint(t *testing.T) {
	t.Parallel()
	_, _, keyFile := mintServiceAccountKeyFile(t, "legacy-token-endpoint-sa")
	grantOnProject(t, "test-project", "roles/iam.serviceAccountViewer", "serviceAccount:legacy-token-endpoint-sa@test-project.iam.gserviceaccount.com")
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

// A Cloud Run container reads its own identity from the metadata server: the
// project its service runs in, that project's number from Cloud Resource
// Manager, and the service account the revision runs as — the one it names,
// or the project's Compute Engine default service account.
func TestMetadataSDK_CloudRunWorkloadIdentity(t *testing.T) {
	t.Parallel()
	client := newServicesClient(t)
	runner := createServiceAccount(t, "test-project", uniquename.New("md-runner"))
	for _, tc := range []struct {
		prefix  string
		account string
		want    string
	}{
		{"v2-svc-md-named", runner, runner},
		{"v2-svc-md-default", "", harnessProjectNumber + "-compute@developer.gserviceaccount.com"},
	} {
		svc := createInvokableService(t, client, tc.prefix, &runpb.Service{
			Template: &runpb.RevisionTemplate{
				ServiceAccount: tc.account,
				Containers:     []*runpb.Container{{Image: httpProbeImageName, Args: []string{"metadata"}}},
			},
		})
		token := invokerIDToken(t, svc.Uri)
		for path, want := range map[string]string{
			"/project/project-id":                              "test-project",
			"/project/numeric-project-id":                      harnessProjectNumber,
			"/instance/service-accounts/default/email":         tc.want,
			"/instance/service-accounts/" + tc.want + "/email": tc.want,
		} {
			status, _, body := invokeService(t, token, svc.Uri, http.MethodGet, path, "")
			require.Equal(t, http.StatusOK, status, "%s %s: %q", tc.prefix, path, body)
			assert.Equal(t, want, body, "%s %s", tc.prefix, path)
		}
		// A Cloud Run instance reports its own id and its region at
		// /computeMetadata/v1/instance/id and /computeMetadata/v1/instance/region,
		// which the container reads; two reads from one instance name the same
		// instance.
		status, _, region := invokeService(t, token, svc.Uri, http.MethodGet, "/instance/region", "")
		require.Equal(t, http.StatusOK, status, "%s /instance/region: %q", tc.prefix, region)
		assert.Equal(t, "projects/"+harnessProjectNumber+"/regions/us-central1", region)
		// Cloud Run names no zone; its container contract answers the region's
		// "-1" zone.
		status, _, zone := invokeService(t, token, svc.Uri, http.MethodGet, "/instance/zone", "")
		require.Equal(t, http.StatusOK, status, "%s /instance/zone: %q", tc.prefix, zone)
		assert.Equal(t, "projects/"+harnessProjectNumber+"/zones/us-central1-1", zone)
		_, _, first := invokeService(t, token, svc.Uri, http.MethodGet, "/instance/id", "")
		_, _, second := invokeService(t, token, svc.Uri, http.MethodGet, "/instance/id", "")
		assert.Regexp(t, `^[0-9a-f]{32,}$`, first, "%s /instance/id", tc.prefix)
		assert.Equal(t, first, second, "the instance reports one id")
	}
}
