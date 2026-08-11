package gcp_sdk_test

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	secretmanager "google.golang.org/api/secretmanager/v1"
)

func secretManagerService(t *testing.T) *secretmanager.Service {
	t.Helper()
	svc, err := secretmanager.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestSecretManagerSecretLifecycleSDK(t *testing.T) {
	svc := secretManagerService(t)
	parent := "projects/sdk-secret-project"
	secretID := "sdk-lifecycle-secret"
	secretName := parent + "/secrets/" + secretID

	created, err := svc.Projects.Secrets.Create(parent, &secretmanager.Secret{
		Labels: map[string]string{"env": "dev"},
	}).SecretId(secretID).Do()
	require.NoError(t, err)
	require.Equal(t, secretName, created.Name)
	require.Equal(t, map[string]string{"env": "dev"}, created.Labels)

	v1, err := svc.Projects.Secrets.AddVersion(secretName, &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{
			Data: base64.StdEncoding.EncodeToString([]byte("first")),
		},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, secretName+"/versions/1", v1.Name)

	v2, err := svc.Projects.Secrets.AddVersion(secretName, &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{
			Data: base64.StdEncoding.EncodeToString([]byte("second")),
		},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, secretName+"/versions/2", v2.Name)

	latest, err := svc.Projects.Secrets.Versions.Access(secretName + "/versions/latest").Do()
	require.NoError(t, err)
	require.Equal(t, secretName+"/versions/2", latest.Name)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("second")), latest.Payload.Data)

	versions, err := svc.Projects.Secrets.Versions.List(secretName).Do()
	require.NoError(t, err)
	require.Equal(t, int64(2), versions.TotalSize)
	require.Len(t, versions.Versions, 2)
	require.Equal(t, secretName+"/versions/2", versions.Versions[0].Name)
	require.Equal(t, secretName+"/versions/1", versions.Versions[1].Name)

	patched, err := svc.Projects.Secrets.Patch(secretName, &secretmanager.Secret{
		Labels: map[string]string{"env": "prod", "owner": "sdk"},
	}).UpdateMask("labels").Do()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"env": "prod", "owner": "sdk"}, patched.Labels)

	_, err = svc.Projects.Secrets.Delete(secretName).Do()
	require.NoError(t, err)

	_, err = svc.Projects.Secrets.Get(secretName).Do()
	require.Error(t, err)
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr), "expected googleapi.Error, got %T: %v", err, err)
	require.Equal(t, 404, apiErr.Code)
}

func TestSecretManager_ListPagination(t *testing.T) {
	svc := secretManagerService(t)
	proj := "pagination-sm-project"

	// Create 3 secrets.
	names := []string{
		"projects/" + proj + "/secrets/alpha",
		"projects/" + proj + "/secrets/beta",
		"projects/" + proj + "/secrets/gamma",
	}
	for _, n := range names {
		_, err := svc.Projects.Secrets.Create("projects/"+proj, &secretmanager.Secret{
			Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
		}).SecretId(n[len("projects/"+proj+"/secrets/"):]).Do()
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		for _, n := range names {
			_, _ = svc.Projects.Secrets.Delete(n).Do()
		}
	})

	// Page through with pageSize=1 and collect all secrets.
	var got []string
	pageToken := ""
	for {
		call := svc.Projects.Secrets.List("projects/" + proj).PageSize(1)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		require.NoError(t, err)
		require.Len(t, resp.Secrets, 1, "each page must have exactly 1 secret with pageSize=1")
		got = append(got, resp.Secrets[0].Name)
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}
	require.Len(t, got, 3, "all 3 secrets must be returned across pages")
	// All 3 names must appear (order may vary by store iteration).
	for _, n := range names {
		require.Contains(t, got, n)
	}
}

func TestSecretManager_GetSecret_NotFound_ErrorClassification(t *testing.T) {
	svc := secretManagerService(t)
	_, err := svc.Projects.Secrets.Get("projects/test-project/secrets/nonexistent-secret").Do()
	require.Error(t, err)
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *googleapi.Error; got %T: %v", err, err)
	}
	if apiErr.Code != 404 {
		t.Errorf("expected HTTP 404, got %d", apiErr.Code)
	}
}

// TestSecretManager_RegionalLifecycleSDK exercises the location-scoped
// (regional) secret surface: create/get/list secrets and add/access/list
// versions under projects/{p}/locations/{loc}/secrets, the regional analogue
// of the global surface.
func TestSecretManager_RegionalLifecycleSDK(t *testing.T) {
	svc := secretManagerService(t)
	location := "us-central1"
	parent := "projects/sdk-regional-project/locations/" + location
	secretID := "sdk-regional-secret"
	secretName := parent + "/secrets/" + secretID

	created, err := svc.Projects.Locations.Secrets.Create(parent, &secretmanager.Secret{
		Labels: map[string]string{"env": "dev"},
	}).SecretId(secretID).Do()
	require.NoError(t, err)
	require.Equal(t, secretName, created.Name)
	require.Equal(t, map[string]string{"env": "dev"}, created.Labels)

	got, err := svc.Projects.Locations.Secrets.Get(secretName).Do()
	require.NoError(t, err)
	require.Equal(t, secretName, got.Name)

	listed, err := svc.Projects.Locations.Secrets.List(parent).Do()
	require.NoError(t, err)
	require.Len(t, listed.Secrets, 1)
	require.Equal(t, secretName, listed.Secrets[0].Name)

	v1, err := svc.Projects.Locations.Secrets.AddVersion(secretName, &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{
			Data: base64.StdEncoding.EncodeToString([]byte("regional-first")),
		},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, secretName+"/versions/1", v1.Name)
	require.Equal(t, "ENABLED", v1.State)

	access, err := svc.Projects.Locations.Secrets.Versions.Access(secretName + "/versions/latest").Do()
	require.NoError(t, err)
	require.Equal(t, secretName+"/versions/1", access.Name)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("regional-first")), access.Payload.Data)

	getVer, err := svc.Projects.Locations.Secrets.Versions.Get(secretName + "/versions/1").Do()
	require.NoError(t, err)
	require.Equal(t, secretName+"/versions/1", getVer.Name)

	versions, err := svc.Projects.Locations.Secrets.Versions.List(secretName).Do()
	require.NoError(t, err)
	require.Equal(t, int64(1), versions.TotalSize)

	// Disable then re-enable round-trips through the regional version actions.
	_, err = svc.Projects.Locations.Secrets.Versions.Disable(secretName+"/versions/1",
		&secretmanager.DisableSecretVersionRequest{}).Do()
	require.NoError(t, err)
	disabled, err := svc.Projects.Locations.Secrets.Versions.Get(secretName + "/versions/1").Do()
	require.NoError(t, err)
	require.Equal(t, "DISABLED", disabled.State)
	_, err = svc.Projects.Locations.Secrets.Versions.Enable(secretName+"/versions/1",
		&secretmanager.EnableSecretVersionRequest{}).Do()
	require.NoError(t, err)

	patched, err := svc.Projects.Locations.Secrets.Patch(secretName, &secretmanager.Secret{
		Labels: map[string]string{"env": "prod"},
	}).UpdateMask("labels").Do()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"env": "prod"}, patched.Labels)

	_, err = svc.Projects.Locations.Secrets.Delete(secretName).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.Secrets.Get(secretName).Do()
	require.Error(t, err)
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, 404, apiErr.Code)
}

// TestSecretManager_IamPolicySDK exercises the IAM policy surface on a secret
// resource: getIamPolicy returns an empty default, setIamPolicy persists a
// binding read back by getIamPolicy, and testIamPermissions echoes the
// caller's requested permissions.
func TestSecretManager_IamPolicySDK(t *testing.T) {
	svc := secretManagerService(t)
	parent := "projects/sdk-iam-project"
	secretID := "sdk-iam-secret"
	secretName := parent + "/secrets/" + secretID

	_, err := svc.Projects.Secrets.Create(parent, &secretmanager.Secret{}).SecretId(secretID).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Secrets.Delete(secretName).Do() })

	// Default policy: empty bindings, version 1, with a stable etag.
	pol, err := svc.Projects.Secrets.GetIamPolicy(secretName).Do()
	require.NoError(t, err)
	require.Empty(t, pol.Bindings)
	require.NotEmpty(t, pol.Etag)

	// Set a binding, supplying the etag from the read for optimistic concurrency.
	want := &secretmanager.Policy{
		Etag: pol.Etag,
		Bindings: []*secretmanager.Binding{{
			Role:    "roles/secretmanager.secretAccessor",
			Members: []string{"user:alice@example.com"},
		}},
	}
	set, err := svc.Projects.Secrets.SetIamPolicy(secretName, &secretmanager.SetIamPolicyRequest{
		Policy: want,
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	require.Equal(t, "roles/secretmanager.secretAccessor", set.Bindings[0].Role)
	require.Equal(t, []string{"user:alice@example.com"}, set.Bindings[0].Members)

	// Read it back.
	got, err := svc.Projects.Secrets.GetIamPolicy(secretName).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	require.Equal(t, "user:alice@example.com", got.Bindings[0].Members[0])

	// testIamPermissions echoes the requested permission set.
	perms := []string{"secretmanager.versions.access", "secretmanager.secrets.get"}
	tested, err := svc.Projects.Secrets.TestIamPermissions(secretName, &secretmanager.TestIamPermissionsRequest{
		Permissions: perms,
	}).Do()
	require.NoError(t, err)
	require.ElementsMatch(t, perms, tested.Permissions)
}

// TestSecretManager_LocationsSDK exercises the Locations meta-API
// (ListLocations + GetLocation) the Secret Manager Discovery document
// defines for region discovery.
func TestSecretManager_LocationsSDK(t *testing.T) {
	svc := secretManagerService(t)
	project := "projects/sdk-loc-project"

	list, err := svc.Projects.Locations.List(project).Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Locations)
	var someLoc string
	for _, l := range list.Locations {
		require.NotEmpty(t, l.LocationId)
		require.Equal(t, project+"/locations/"+l.LocationId, l.Name)
		someLoc = l.LocationId
	}

	got, err := svc.Projects.Locations.Get(project + "/locations/" + someLoc).Do()
	require.NoError(t, err)
	require.Equal(t, someLoc, got.LocationId)
}
