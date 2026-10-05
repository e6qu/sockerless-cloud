package gcp_tf_test

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// iamUndelete calls projects.serviceAccounts.undelete the way gcloud does,
// on projects/-/serviceAccounts/{uniqueId}:undelete; the provider has no
// resource for it.
func iamUndelete(t *testing.T, uniqueID string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		baseURL+"/v1/projects/-/serviceAccounts/"+uniqueID+":undelete", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	body := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &body), "undelete answered %d with %s", resp.StatusCode, raw)
	return resp.StatusCode, body
}

// TestTerraformServiceAccountRecreateAfterDestroy destroys a
// google_service_account and applies it again: the provider's create makes a
// new account under the same email with a new unique ID, the destroyed one
// stays restorable by its unique ID only once the email is free again, and the
// restored account carries its original unique ID.
func TestTerraformServiceAccountRecreateAfterDestroy(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "service-account-recreate")
	cleanTerraformFixture(t, fixtureDir)

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	apply := func() (string, string) {
		t.Helper()
		out, err := runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
		require.NoError(t, err, "terraform apply failed:\n%s", out)
		outputs := readOutputsInDir(t, fixtureDir)
		return outputs.must(t, "email"), outputs.must(t, "unique_id")
	}
	destroy := func() {
		t.Helper()
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	}

	email, firstID := apply()
	require.Equal(t, "tf-recreated-sa@test-project.iam.gserviceaccount.com", email)
	destroy()

	secondEmail, secondID := apply()
	require.Equal(t, email, secondEmail)
	require.NotEqual(t, firstID, secondID, "the recreated account must get a new unique ID")

	code, body := iamUndelete(t, firstID)
	require.Equal(t, http.StatusBadRequest, code,
		"the destroyed account cannot be restored while another account holds its email: %v", body)
	destroy()

	code, body = iamUndelete(t, firstID)
	require.Equal(t, http.StatusOK, code, "undelete answered %v", body)
	restored, _ := body["restoredAccount"].(map[string]any)
	require.Equal(t, firstID, restored["uniqueId"], "undelete keeps the unique ID")
	require.Equal(t, email, restored["email"])

	req, err := http.NewRequest(http.MethodDelete,
		baseURL+"/v1/projects/test-project/serviceAccounts/"+email, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the restored account must delete cleanly")
}
