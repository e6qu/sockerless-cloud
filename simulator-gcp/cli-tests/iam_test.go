package gcp_cli_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAMCustomRoleLifecycleCLI exercises `gcloud iam roles` against the sim:
// create → describe → list → update → delete → undelete. Custom-role CRUD
// rides the IAM endpoint override (CLOUDSDK_API_ENDPOINT_OVERRIDES_IAM).
func TestIAMCustomRoleLifecycleCLI(t *testing.T) {
	const roleID = "cliCustomRole"

	out := runCLI(t, gcloudCLI("iam", "roles", "create", roleID,
		"--project="+project,
		"--title=CLI Custom Role",
		"--description=created via cli",
		"--permissions=storage.objects.get,storage.objects.list",
		"--stage=GA",
		"--format=json",
	))
	var created struct {
		Name                string   `json:"name"`
		Title               string   `json:"title"`
		IncludedPermissions []string `json:"includedPermissions"`
		Etag                string   `json:"etag"`
	}
	parseJSONObject(t, out, &created)
	require.Equal(t, "projects/"+project+"/roles/"+roleID, created.Name)
	require.Equal(t, "CLI Custom Role", created.Title)
	require.ElementsMatch(t, []string{"storage.objects.get", "storage.objects.list"}, created.IncludedPermissions)

	out = runCLI(t, gcloudCLI("iam", "roles", "describe", roleID,
		"--project="+project, "--format=json"))
	var described struct {
		Name                string   `json:"name"`
		IncludedPermissions []string `json:"includedPermissions"`
	}
	parseJSONObject(t, out, &described)
	require.Equal(t, created.Name, described.Name)
	require.NotEmpty(t, described.IncludedPermissions, "describe returns FULL view")

	out = runCLI(t, gcloudCLI("iam", "roles", "list", "--project="+project, "--format=json"))
	jsonStart := strings.Index(out, "[")
	require.NotEqual(t, -1, jsonStart, "list output not a JSON array: %s", out)
	var listed []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(out[jsonStart:]), &listed), "output: %s", out)
	found := false
	for _, r := range listed {
		if r.Name == created.Name {
			found = true
		}
	}
	require.True(t, found, "created role must appear in list")

	out = runCLI(t, gcloudCLI("iam", "roles", "update", roleID,
		"--project="+project,
		"--title=Renamed CLI Role",
		"--format=json",
	))
	var updated struct {
		Title string `json:"title"`
		Etag  string `json:"etag"`
	}
	parseJSONObject(t, out, &updated)
	require.Equal(t, "Renamed CLI Role", updated.Title)
	require.NotEqual(t, created.Etag, updated.Etag, "etag must rotate on update")

	out = runCLI(t, gcloudCLI("iam", "roles", "delete", roleID,
		"--project="+project, "--format=json"))
	var deleted struct {
		Deleted bool `json:"deleted"`
	}
	parseJSONObject(t, out, &deleted)
	require.True(t, deleted.Deleted, "delete soft-deletes the role")

	out = runCLI(t, gcloudCLI("iam", "roles", "undelete", roleID,
		"--project="+project, "--format=json"))
	var revived struct {
		Deleted bool `json:"deleted"`
	}
	parseJSONObject(t, out, &revived)
	require.False(t, revived.Deleted, "undelete clears the deleted flag")
}

// TestIAMServiceAccountCreateDuplicateCLI pins that creating a second service
// account with an accountId already in use within the project fails with real
// IAM's 409 ALREADY_EXISTS, instead of gcloud silently reporting success over
// a silently overwritten account.
func TestIAMServiceAccountCreateDuplicateCLI(t *testing.T) {
	const accountID = "cli-dup-sa"
	email := accountID + "@" + project + ".iam.gserviceaccount.com"

	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID,
		"--display-name=Original", "--format=json"))

	out, err := gcloudCLI("iam", "service-accounts", "create", accountID,
		"--display-name=Duplicate", "--format=json").CombinedOutput()
	require.Error(t, err, "duplicate service-accounts create must fail: %s", out)
	assert.Contains(t, string(out), "already exists", "gcloud must report the real IAM conflict")

	// The rejected duplicate must not have overwritten the existing account.
	descOut := runCLI(t, gcloudCLI("iam", "service-accounts", "describe", email, "--format=json"))
	var described struct {
		DisplayName string `json:"displayName"`
	}
	parseJSONObject(t, descOut, &described)
	assert.Equal(t, "Original", described.DisplayName, "duplicate create must not overwrite the existing account")
}

// TestIAMServiceAccountAsResourceIAMCLI exercises get-iam-policy +
// add-iam-policy-binding on a service account (the SA-as-resource IAM triple),
// plus :testIamPermissions directly against the wire path.
func TestIAMServiceAccountAsResourceIAMCLI(t *testing.T) {
	const accountID = "cli-iam-sa"
	email := accountID + "@" + project + ".iam.gserviceaccount.com"

	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID,
		"--display-name=CLI IAM SA", "--format=json"))

	// get-iam-policy on a fresh SA → empty bindings, an etag present.
	out := runCLI(t, gcloudCLI("iam", "service-accounts", "get-iam-policy", email, "--format=json"))
	var pol struct {
		Etag string `json:"etag"`
	}
	parseJSONObject(t, out, &pol)
	require.NotEmpty(t, pol.Etag)

	// add-iam-policy-binding read-modify-writes the SA's policy (get+set).
	out = runCLI(t, gcloudCLI("iam", "service-accounts", "add-iam-policy-binding", email,
		"--member=user:dev@example.com",
		"--role=roles/iam.serviceAccountUser",
		"--format=json",
	))
	var bound struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	parseJSONObject(t, out, &bound)
	require.Len(t, bound.Bindings, 1)
	require.Equal(t, "roles/iam.serviceAccountUser", bound.Bindings[0].Role)
	require.Contains(t, bound.Bindings[0].Members, "user:dev@example.com")

	// get-iam-policy reflects the binding.
	out = runCLI(t, gcloudCLI("iam", "service-accounts", "get-iam-policy", email, "--format=json"))
	parseJSONObject(t, out, &bound)
	require.Len(t, bound.Bindings, 1)

	// testIamPermissions has no gcloud surface on service-accounts; hit the
	// wire path directly. The admin caller echoes the requested set.
	saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
	body := `{"permissions":["iam.serviceAccounts.actAs","iam.serviceAccounts.get"]}`
	respBody := httpDoJSON(t, "POST", baseURL+"/v1/"+saName+":testIamPermissions", body)
	var tip struct {
		Permissions []string `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal([]byte(respBody), &tip))
	require.ElementsMatch(t,
		[]string{"iam.serviceAccounts.actAs", "iam.serviceAccounts.get"}, tip.Permissions)
}

// TestIAMServiceAccountDisableEnableUpdateCLI exercises :disable, :enable, and
// PATCH (update) on a service account.
func TestIAMServiceAccountDisableEnableUpdateCLI(t *testing.T) {
	const accountID = "cli-toggle-sa"
	email := accountID + "@" + project + ".iam.gserviceaccount.com"

	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID,
		"--display-name=Toggle SA", "--format=json"))

	runCLI(t, gcloudCLI("iam", "service-accounts", "disable", email))
	out := runCLI(t, gcloudCLI("iam", "service-accounts", "describe", email, "--format=json"))
	var afterDisable struct {
		Disabled bool `json:"disabled"`
	}
	parseJSONObject(t, out, &afterDisable)
	require.True(t, afterDisable.Disabled, "disable sets disabled=true")

	runCLI(t, gcloudCLI("iam", "service-accounts", "enable", email))
	out = runCLI(t, gcloudCLI("iam", "service-accounts", "describe", email, "--format=json"))
	var afterEnable struct {
		Disabled bool `json:"disabled"`
	}
	parseJSONObject(t, out, &afterEnable)
	require.False(t, afterEnable.Disabled, "enable sets disabled=false")

	// PATCH displayName via the wire path (gcloud's `update` shells the same
	// PATCH with an updateMask).
	out = runCLI(t, gcloudCLI("iam", "service-accounts", "update", email,
		"--display-name=Renamed Toggle SA", "--format=json"))
	var updated struct {
		DisplayName string `json:"displayName"`
	}
	parseJSONObject(t, out, &updated)
	assert.Equal(t, "Renamed Toggle SA", updated.DisplayName)
}

// TestIAMServiceAccountKeysActivateCLI proves the console's credential loop
// with the vendor CLI end to end: create a service account, mint a key with
// `gcloud iam service-accounts keys create` (which writes the decoded
// credential file — the same file the console's one-time download saves),
// activate it with `gcloud auth activate-service-account --key-file`, and
// make an authenticated IAM call over the resulting credentials, with
// auth/token_host pointing the JWT-bearer exchange at the simulator. This is
// exactly the usage the console's post-mint CLI panel prints. A key file whose
// private key was swapped for one the IAM API never registered is rejected by
// the token endpoint with invalid_grant.
func TestIAMServiceAccountKeysActivateCLI(t *testing.T) {
	const accountID = "cli-keys-sa"
	email := accountID + "@" + project + ".iam.gserviceaccount.com"

	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID,
		"--display-name=CLI Keys SA", "--format=json"))

	// Mint a key; gcloud decodes privateKeyData and writes the credential file.
	keyFile := filepath.Join(tmpDir, "cli-keys-sa.json")
	runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "create", keyFile,
		"--iam-account="+email, "--format=json"))
	keyBytes, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	var keyJSON map[string]any
	require.NoError(t, json.Unmarshal(keyBytes, &keyJSON))
	require.Equal(t, "service_account", keyJSON["type"])
	require.Equal(t, email, keyJSON["client_email"])
	require.NotEmpty(t, keyJSON["private_key"])
	keyID, _ := keyJSON["private_key_id"].(string)
	require.NotEmpty(t, keyID)

	// keys list shows the minted key.
	out := runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "list",
		"--iam-account="+email, "--format=json"))
	jsonStart := strings.Index(out, "[")
	require.NotEqual(t, -1, jsonStart, "keys list output not a JSON array: %s", out)
	var listed []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(out[jsonStart:]), &listed), "output: %s", out)
	found := false
	for _, k := range listed {
		if strings.HasSuffix(k.Name, "/"+keyID) {
			found = true
		}
	}
	require.True(t, found, "minted key must appear in keys list")

	// Activate the minted key in an isolated gcloud config (so the activated
	// account cannot leak into the shared config other tests use) and make an
	// authenticated call with it. auth/token_host routes the credential's
	// JWT-bearer exchange to the simulator — the coordinate, nothing else.
	saConfig := filepath.Join(tmpDir, "gcloud-sa-config")
	runCLI(t, gcloudSAActivatedCLI(saConfig, "config", "set", "api_endpoint_overrides/iam", baseURL+"/"))
	runCLI(t, gcloudSAActivatedCLI(saConfig, "config", "set", "auth/token_host", baseURL+"/token"))
	runCLI(t, gcloudSAActivatedCLI(saConfig, "auth", "activate-service-account", "--key-file="+keyFile))
	out = runCLI(t, gcloudSAActivatedCLI(saConfig, "iam", "service-accounts", "list", "--format=json"))
	require.Contains(t, out, email, "authenticated list over the activated key must include the account")

	// Swap the private key for one the IAM API never registered; the token
	// endpoint must reject the assertion, so the authenticated call fails
	// with the real endpoint's invalid_grant.
	tamperedPEM := generateUnregisteredKeyPEM(t)
	keyJSON["private_key"] = tamperedPEM
	tamperedBytes, err := json.Marshal(keyJSON)
	require.NoError(t, err)
	tamperedFile := filepath.Join(tmpDir, "cli-keys-sa-tampered.json")
	require.NoError(t, os.WriteFile(tamperedFile, tamperedBytes, 0o600))

	tamperedConfig := filepath.Join(tmpDir, "gcloud-sa-config-tampered")
	runCLI(t, gcloudSAActivatedCLI(tamperedConfig, "config", "set", "api_endpoint_overrides/iam", baseURL+"/"))
	runCLI(t, gcloudSAActivatedCLI(tamperedConfig, "config", "set", "auth/token_host", baseURL+"/token"))
	// gcloud verifies the credential by minting during activation; whether it
	// fails there or on the first API call, the failure must be the token
	// endpoint's invalid_grant.
	activateOut, activateErr := gcloudSAActivatedCLI(tamperedConfig,
		"auth", "activate-service-account", "--key-file="+tamperedFile).CombinedOutput()
	if activateErr == nil {
		listOut, listErr := gcloudSAActivatedCLI(tamperedConfig,
			"iam", "service-accounts", "list", "--format=json").CombinedOutput()
		require.Error(t, listErr, "a tampered key must not authenticate: %s", listOut)
		assert.Contains(t, string(listOut), "invalid_grant")
	} else {
		assert.Contains(t, string(activateOut), "invalid_grant")
	}
}

// generateUnregisteredKeyPEM returns a PKCS#8 PEM private key that no IAM
// CreateServiceAccountKey call ever registered — signing material for the
// tampered-credential rejection path.
func generateUnregisteredKeyPEM(t *testing.T) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}
