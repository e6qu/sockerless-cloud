package gcp_sdk_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	dns "google.golang.org/api/dns/v1"
	iamv1 "google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
	serviceusage "google.golang.org/api/serviceusage/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// The methods that used to be mux misses across Cloud DNS, IAM, Cloud SQL,
// Cloud Storage and Service Usage, each driven here by the client a real
// caller uses.

// A managed zone carries an IAM policy like every other AIP-141 resource:
// the policy set on it is the policy read back, and testIamPermissions
// answers against it.
func TestDNS_ManagedZoneIAM(t *testing.T) {
	ctx := context.Background()
	svc, err := dns.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const project = "test-project"

	zone := &dns.ManagedZone{
		Name:        "iam-zone",
		DnsName:     "iam-zone.example.com.",
		Description: "managed zone IAM",
	}
	_, err = svc.ManagedZones.Create(project, zone).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.ManagedZones.Delete(project, zone.Name).Do() })

	set, err := svc.ManagedZones.SetIamPolicy(
		fmt.Sprintf("projects/%s/managedZones/%s", project, zone.Name),
		&dns.GoogleIamV1SetIamPolicyRequest{
			Policy: &dns.GoogleIamV1Policy{Bindings: []*dns.GoogleIamV1Binding{{
				Role: "roles/dns.reader", Members: []string{"user:zone@example.com"},
			}}},
		}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	got, err := svc.ManagedZones.GetIamPolicy(
		fmt.Sprintf("projects/%s/managedZones/%s", project, zone.Name),
		&dns.GoogleIamV1GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	assert.Equal(t, "roles/dns.reader", got.Bindings[0].Role)

	tested, err := svc.ManagedZones.TestIamPermissions(
		fmt.Sprintf("projects/%s/managedZones/%s", project, zone.Name),
		&dns.GoogleIamV1TestIamPermissionsRequest{
			Permissions: []string{"dns.managedZones.get"},
		}).Do()
	require.NoError(t, err)
	assert.NotNil(t, tested)
}

// The IAM tail: a caller-supplied public key becomes a real user-managed
// key, disable and enable flip the stored key's state, and the three
// catalog reads answer from what this installation actually holds.
func TestIAM_ServiceAccountKeyUploadEnableDisableAndCatalogs(t *testing.T) {
	ctx := context.Background()
	svc, err := iamv1.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const project = "test-project"

	account, err := svc.Projects.ServiceAccounts.Create("projects/"+project,
		&iamv1.CreateServiceAccountRequest{
			AccountId:      "key-tail-sa",
			ServiceAccount: &iamv1.ServiceAccount{DisplayName: "key tail"},
		}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.ServiceAccounts.Delete(account.Name).Do() })

	// A user-managed key the caller supplies: the API stores the public key
	// it was handed rather than minting a private one.
	uploaded, err := svc.Projects.ServiceAccounts.Keys.Upload(account.Name,
		&iamv1.UploadServiceAccountKeyRequest{
			PublicKeyData: iamUploadCertificatePEM(t),
		}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, uploaded.Name)
	assert.Equal(t, "USER_MANAGED", uploaded.KeyType)

	_, err = svc.Projects.ServiceAccounts.Keys.Disable(uploaded.Name,
		&iamv1.DisableServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)
	got, err := svc.Projects.ServiceAccounts.Keys.Get(uploaded.Name).Do()
	require.NoError(t, err)
	assert.True(t, got.Disabled, "a disabled key reads back disabled")

	_, err = svc.Projects.ServiceAccounts.Keys.Enable(uploaded.Name,
		&iamv1.EnableServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)
	got, err = svc.Projects.ServiceAccounts.Keys.Get(uploaded.Name).Do()
	require.NoError(t, err)
	assert.False(t, got.Disabled, "enabling clears the state disabling set")

	// A policy with no condition has nothing to lint; the empty finding list
	// is the honest answer.
	lint, err := svc.IamPolicies.LintPolicy(&iamv1.LintPolicyRequest{
		FullResourceName: "//cloudresourcemanager.googleapis.com/projects/" + project,
	}).Do()
	require.NoError(t, err)
	assert.Empty(t, lint.LintResults)

	auditable, err := svc.IamPolicies.QueryAuditableServices(&iamv1.QueryAuditableServicesRequest{
		FullResourceName: "//cloudresourcemanager.googleapis.com/projects/" + project,
	}).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, auditable.Services, "the auditable services are this installation's own")

	grantable, err := svc.Roles.QueryGrantableRoles(&iamv1.QueryGrantableRolesRequest{
		FullResourceName: "//cloudresourcemanager.googleapis.com/projects/" + project,
	}).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, grantable.Roles, "the grantable roles come from the role catalog the simulator holds")
}

// iamUploadCertificatePEM builds what the API demands of an uploaded
// user-managed key: an RSA public key wrapped in an X.509 v3 certificate,
// PEM-encoded, base64 on the wire.
func iamUploadCertificatePEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "uploaded service account key"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return base64.StdEncoding.EncodeToString(pemBytes)
}

// Cloud SQL's two remaining methods: the connect-settings resolve a
// connector performs before dialling, and the point-in-time restore that
// creates a new instance from the backup covering the requested moment.
func TestCloudSQL_ResolveConnectSettingsAndPointInTimeRestore(t *testing.T) {
	ctx := context.Background()
	svc, err := sqladmin.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const project = "test-project"
	const source = "pitr-source"

	_, err = svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            source,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, source).Do() })

	// A backup must exist for a restore to have something to restore from.
	backupOp, err := svc.BackupRuns.Insert(project, source, &sqladmin.BackupRun{}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, backupOp.Name)
	runs, err := svc.BackupRuns.List(project, source).Do()
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	require.Equal(t, "SUCCESSFUL", runs.Items[0].Status)
	// A restore names a moment the backups cover: the instant this backup
	// finished is the earliest such moment, and the one a caller restoring
	// "to the last backup" asks for.
	restorePoint := runs.Items[0].EndTime

	restoreOp, err := svc.Instances.PointInTimeRestore("projects/"+project,
		&sqladmin.PointInTimeRestoreContext{
			Datasource:     fmt.Sprintf("projects/%s/locations/us-central1/backupVaults/default/dataSources/%s", project, source),
			PointInTime:    restorePoint,
			TargetInstance: fmt.Sprintf("projects/%s/instances/pitr-target", project),
		}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, restoreOp.Name)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, "pitr-target").Do() })
	waitSQLOperationDone(t, svc, project, restoreOp.Name)

	restored, err := svc.Instances.Get(project, "pitr-target").Do()
	require.NoError(t, err)
	assert.Equal(t, "POSTGRES_15", restored.DatabaseVersion,
		"the restored instance carries the source's engine")

	// The connector's resolve, addressed by the DNS name the instance's
	// connect settings advertise — the coordinate a connector reads before
	// it dials.
	connect, err := svc.Connect.Get(project, source).Do()
	require.NoError(t, err)
	require.NotEmpty(t, connect.DnsName, "connect settings must advertise the instance's DNS name")
	resolved := gcpRawJSON(t, http.MethodGet, fmt.Sprintf(
		"/sql/v1beta4/locations/us-central1/dns/%s:resolveConnectSettings",
		connect.DnsName), nil, http.StatusOK)
	assert.Equal(t, connect.DatabaseVersion, resolved["databaseVersion"],
		"the resolve answers from the same instance state connect settings read")
}

// Cloud Storage's rapid caches are bucket-scoped control-plane state, and a
// managed folder's rapid-cache configuration is patchable. The generated Go
// client has no rapidCaches collection yet, so these speak the JSON API
// directly — the same wire a client will speak once it does.
func TestGCS_RapidCachesAndManagedFolderPatch(t *testing.T) {
	const bucket = "rapid-cache-bucket"
	gcpRawJSON(t, http.MethodPost, "/storage/v1/b?project=test-project",
		map[string]any{"name": bucket}, http.StatusOK)

	// insert, update and disable each answer with a long-running operation,
	// exactly as the Discovery document declares their response type.
	created := gcpRawJSON(t, http.MethodPost, "/storage/v1/b/"+bucket+"/rapidCaches",
		map[string]any{"rapidCacheId": "cache-1", "zone": "us-central1-a"}, http.StatusOK)
	assert.Equal(t, "storage#operation", created["kind"])
	assert.Equal(t, true, created["done"], "the operation completes with the cache")

	listed := gcpRawJSON(t, http.MethodGet, "/storage/v1/b/"+bucket+"/rapidCaches", nil, http.StatusOK)
	assert.Equal(t, "storage#rapidCaches", listed["kind"])
	items, _ := listed["items"].([]any)
	require.Len(t, items, 1)

	got := gcpRawJSON(t, http.MethodGet, "/storage/v1/b/"+bucket+"/rapidCaches/cache-1", nil, http.StatusOK)
	assert.Equal(t, "cache-1", got["rapidCacheId"])

	patched := gcpRawJSON(t, http.MethodPatch, "/storage/v1/b/"+bucket+"/rapidCaches/cache-1",
		map[string]any{"ttl": "7200s"}, http.StatusOK)
	assert.Equal(t, "storage#operation", patched["kind"])
	afterPatch := gcpRawJSON(t, http.MethodGet, "/storage/v1/b/"+bucket+"/rapidCaches/cache-1", nil, http.StatusOK)
	assert.Equal(t, "7200s", afterPatch["ttl"], "the patch is what the read returns")

	disabled := gcpRawJSON(t, http.MethodPost, "/storage/v1/b/"+bucket+"/rapidCaches/cache-1/disable",
		map[string]any{}, http.StatusOK)
	assert.Equal(t, "storage#operation", disabled["kind"])
	afterDisable := gcpRawJSON(t, http.MethodGet, "/storage/v1/b/"+bucket+"/rapidCaches/cache-1", nil, http.StatusOK)
	assert.NotEqual(t, afterPatch["state"], afterDisable["state"], "disabling moves the cache's state")

	// A managed folder's rapid-cache configuration round-trips through PATCH.
	gcpRawJSON(t, http.MethodPost, "/storage/v1/b/"+bucket+"/managedFolders",
		map[string]any{"name": "folder-1/"}, http.StatusOK)
	folder := gcpRawJSON(t, http.MethodPatch, "/storage/v1/b/"+bucket+"/managedFolders/folder-1%2F",
		map[string]any{"rapidCacheConfig": map[string]any{
			"policies": map[string]any{"cache-1": map[string]any{}},
		}}, http.StatusOK)
	assert.NotNil(t, folder["rapidCacheConfig"], "the patched configuration is the one read back")
}

// Service Usage's batch read answers from the same store the single get
// serves.
func TestServiceUsage_BatchGet(t *testing.T) {
	ctx := context.Background()
	svc, err := serviceusage.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const parent = "projects/test-project"

	_, err = svc.Services.Enable(parent+"/services/compute.googleapis.com",
		&serviceusage.EnableServiceRequest{}).Do()
	require.NoError(t, err)

	batch, err := svc.Services.BatchGet(parent).
		Names(parent+"/services/compute.googleapis.com", parent+"/services/dns.googleapis.com").Do()
	require.NoError(t, err)
	require.Len(t, batch.Services, 2)
	byName := map[string]string{}
	for _, service := range batch.Services {
		byName[service.Name] = service.State
	}
	assert.Equal(t, "ENABLED", byName[parent+"/services/compute.googleapis.com"],
		"the batch read must see what the single enable wrote")
}

// gcpRawJSON issues a JSON API request the generated client cannot express
// and returns the decoded body.
func gcpRawJSON(t *testing.T, method, path string, body any, wantStatus int) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+simBearerToken(t))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, wantStatus, resp.StatusCode, "%s %s: %s", method, path, raw)
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		require.NoError(t, json.Unmarshal(raw, &out), "%s", raw)
	}
	return out
}

// simBearerToken mints the access token a raw request carries, from the
// same token source the generated clients use.
func simBearerToken(t *testing.T) string {
	t.Helper()
	token, err := simTokenSource().Token()
	require.NoError(t, err)
	return token.AccessToken
}
