package gcp_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
)

// The metadata server answers the workloads Google Cloud runs and nothing
// else. These tests read it the way a client inside a Cloud Run container
// does — through cloud.google.com/go/compute/metadata, Application Default
// Credentials, and plain GETs — from a service of the metadata-client image.
// A Compute Engine instance's own reads run in its startup script, in
// TestCompute_InstanceBootsRunsItsStartupScriptAndKeepsItsDisk.

// metadataReader runs a Cloud Run service of the metadata-client image in
// test-project as its Compute Engine default service account, and returns a
// GET of the service with the given request header.
func metadataReader(t *testing.T) func(path string, header http.Header) serviceAnswer {
	t.Helper()
	svc := createInvokableService(t, newServicesClient(t), "metadata-reader", &runpb.Service{
		Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{Image: metadataClientImageName}}},
	})
	token := invokerIDToken(t, svc.Uri)
	return func(path string, header http.Header) serviceAnswer {
		t.Helper()
		sent := http.Header{"Authorization": {"Bearer " + token}}
		for name, values := range header {
			sent[name] = values
		}
		return invokeServiceRaw(t, svc.Uri, path, sent)
	}
}

// metadataRaw is the path of a plain GET of /computeMetadata/v1/<path>.
func metadataRaw(path string) string {
	return "/raw?path=" + url.QueryEscape(path)
}

var metadataFlavor = http.Header{"X-Metadata-Flavor": {"Google"}}

// A caller that is no workload reaches no metadata server: the simulator's
// listener answers it as an origin without one.
func TestMetadata_FromOutsideEveryWorkload(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/computeMetadata/v1/project/project-id",
		"/computeMetadata/v1/instance/service-accounts/default/token",
		"/computeMetadata/v1/instance/service-accounts/",
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := rawClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
		assert.Empty(t, resp.Header.Get("Metadata-Flavor"), "no metadata server answered %s", path)
	}
}

// A Cloud Run container reads its project, its identity and its credentials
// from the metadata server through the official Go client, walking the
// directory listings a client walks before it fetches a token.
func TestMetadata_CloudRunReadsThroughTheOfficialClient(t *testing.T) {
	t.Parallel()
	get := metadataReader(t)
	defaultAccount := harnessProjectNumber + "-compute@developer.gserviceaccount.com"
	read := func(t *testing.T, path string) string {
		t.Helper()
		answer := get(path, nil)
		require.Equal(t, http.StatusOK, answer.status, "%s: %s", path, answer.body)
		return answer.body
	}
	clientGet := func(t *testing.T, path string) string {
		t.Helper()
		return read(t, "/client/get?path="+url.QueryEscape(path))
	}

	t.Run("project and identity", func(t *testing.T) {
		assert.Equal(t, "test-project", read(t, "/client/project-id"))
		assert.Equal(t, harnessProjectNumber, read(t, "/client/numeric-project-id"), "the number Cloud Resource Manager holds for the project")
		assert.Equal(t, defaultAccount, read(t, "/client/email?account=default"))
		assert.Contains(t, clientGet(t, "instance/service-accounts/default/scopes"), "cloud-platform")
		// The universe domain is the key every Google auth library consults
		// before it chooses an API host.
		assert.Equal(t, "googleapis.com", strings.TrimSpace(clientGet(t, "universe/universe-domain")))
	})

	// A client discovers the identities it may act as through the
	// listings; each entry is a directory, so it ends in a slash.
	t.Run("directory listings", func(t *testing.T) {
		entries := strings.Fields(clientGet(t, "instance/service-accounts/"))
		assert.Contains(t, entries, "default/")
		assert.Contains(t, entries, defaultAccount+"/", "the listing names the identity's own email")
		accountKeys := strings.Fields(clientGet(t, "instance/service-accounts/"+defaultAccount+"/"))
		for _, key := range []string{"aliases", "email", "identity", "scopes", "token"} {
			assert.Contains(t, accountKeys, key)
		}
		assert.Contains(t, strings.Fields(clientGet(t, "")), "instance/")
		assert.Contains(t, strings.Fields(clientGet(t, "")), "project/")
		assert.Contains(t, strings.Fields(clientGet(t, "instance/")), "service-accounts/")
	})

	// One recursive read returns the stored keys as JSON in camelCase,
	// without the leaves the server mints per request.
	t.Run("recursive reads", func(t *testing.T) {
		var account struct {
			Aliases []string `json:"aliases"`
			Email   string   `json:"email"`
			Scopes  []string `json:"scopes"`
			Token   any      `json:"token"`
		}
		raw := clientGet(t, "instance/service-accounts/default/?recursive=true")
		require.NoError(t, json.Unmarshal([]byte(raw), &account), raw)
		assert.Equal(t, []string{"default"}, account.Aliases)
		assert.Equal(t, defaultAccount, account.Email)
		assert.Contains(t, account.Scopes, "https://www.googleapis.com/auth/cloud-platform")
		assert.Nil(t, account.Token)

		var project struct {
			NumericProjectID int64  `json:"numericProjectId"`
			ProjectID        string `json:"projectId"`
		}
		raw = clientGet(t, "project/?recursive=true")
		require.NoError(t, json.Unmarshal([]byte(raw), &project), raw)
		assert.Equal(t, harnessProjectNumber, strconv.FormatInt(project.NumericProjectID, 10))
		assert.Equal(t, "test-project", project.ProjectID)
	})

	// Every read requires Metadata-Flavor: Google, and the answer carries it
	// back.
	t.Run("flavor header", func(t *testing.T) {
		answer := get(metadataRaw("project/project-id"), nil)
		assert.Equal(t, http.StatusForbidden, answer.status, answer.body)
		answer = get(metadataRaw("project/project-id"), metadataFlavor)
		require.Equal(t, http.StatusOK, answer.status, answer.body)
		assert.Equal(t, "Google", answer.header.Get("X-Metadata-Flavor"))
	})

	// The residency probe a Google auth library sends before it resolves
	// Application Default Credentials: the root listing, with the flavor
	// header back.
	t.Run("residency probe", func(t *testing.T) {
		answer := get("/root", metadataFlavor)
		require.Equal(t, http.StatusOK, answer.status, answer.body)
		assert.Equal(t, "Google", answer.header.Get("X-Metadata-Flavor"))
		assert.Equal(t, []string{"computeMetadata/"}, strings.Fields(answer.body))
	})

	t.Run("access token", func(t *testing.T) {
		answer := get(metadataRaw("instance/service-accounts/default/token"), metadataFlavor)
		require.Equal(t, http.StatusOK, answer.status, answer.body)
		var tok struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			TokenType   string `json:"token_type"`
		}
		require.NoError(t, json.Unmarshal([]byte(answer.body), &tok), answer.body)
		assert.NotEmpty(t, tok.AccessToken)
		assert.Equal(t, "Bearer", tok.TokenType)
		assert.Greater(t, tok.ExpiresIn, 0)
	})

	t.Run("identity token", func(t *testing.T) {
		answer := get(metadataRaw("instance/service-accounts/default/identity"), metadataFlavor)
		assert.Equal(t, http.StatusBadRequest, answer.status, "an identity token needs an audience: %s", answer.body)

		answer = get(metadataRaw("instance/service-accounts/default/identity?audience=https://example.com"), metadataFlavor)
		require.Equal(t, http.StatusOK, answer.status, answer.body)
		jwt := strings.TrimSpace(answer.body)
		parts := strings.Split(jwt, ".")
		require.Len(t, parts, 3, "expected JWT shape header.payload.sig, got %q", jwt)
		header, err := base64.RawURLEncoding.DecodeString(parts[0])
		require.NoError(t, err)
		var hdr struct {
			Alg string `json:"alg"`
		}
		require.NoError(t, json.Unmarshal(header, &hdr))
		assert.Equal(t, "RS256", hdr.Alg)
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		require.NoError(t, err)
		var claims map[string]any
		require.NoError(t, json.Unmarshal(payload, &claims))
		assert.Equal(t, "https://example.com", claims["aud"])
		assert.Equal(t, defaultAccount, claims["email"])

		// An ID token is not an OAuth access token: a Google API refuses it.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			baseURL+"/v2/projects/test-project/locations/us-central1/services", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+jwt)
		resp, err := rawClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	// A Cloud Run container is no Compute Engine instance: it has no custom
	// metadata.
	t.Run("no instance attributes", func(t *testing.T) {
		answer := get(metadataRaw("instance/attributes/"), metadataFlavor)
		require.Equal(t, http.StatusOK, answer.status, answer.body)
		assert.Empty(t, strings.TrimSpace(answer.body))
		answer = get(metadataRaw("instance/attributes/startup-script"), metadataFlavor)
		assert.Equal(t, http.StatusNotFound, answer.status, answer.body)
	})

	// Application Default Credentials resolve through the metadata server
	// in a container with no credential file, and the token they mint
	// authenticates a data-plane call.
	t.Run("application default credentials", func(t *testing.T) {
		token := read(t, "/adc")
		require.NotEmpty(t, token)
		svc, err := iam.NewService(ctx, option.WithEndpoint(baseURL),
			option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"})))
		require.NoError(t, err)
		_, err = svc.Projects.ServiceAccounts.List("projects/test-project").Do()
		require.NoError(t, err, "the data plane accepts the token the workload's credentials minted")
	})
}
