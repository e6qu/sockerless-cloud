package gcp_cli_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcloud billing drives the account collection and the project link end to
// end: the account gcloud creates is the one it lists and describes, the
// link gcloud writes is the one the project describe reads back, and
// unlinking disables billing.
func TestCloudBillingCLI_AccountsAndProjectLink(t *testing.T) {
	out := runCLI(t, gcloudCLI("billing", "accounts", "list", "--format=json"))
	var before []struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &before)

	// gcloud has no accounts-create surface (Google provisions accounts), so
	// the account arrives the way an operator's tooling makes it: through
	// the API the CLI itself then reads.
	created := billingCreateAccountViaAPI(t, "cli-spend")

	out = runCLI(t, gcloudCLI("billing", "accounts", "list", "--format=json"))
	var after []struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Open        bool   `json:"open"`
	}
	parseJSON(t, out, &after)
	require.Len(t, after, len(before)+1)
	found := false
	for _, account := range after {
		if account.Name == created {
			found = true
			assert.Equal(t, "cli-spend", account.DisplayName)
			assert.True(t, account.Open)
		}
	}
	require.True(t, found, "gcloud must list the account the API created")

	out = runCLI(t, gcloudCLI("billing", "accounts", "describe", created, "--format=json"))
	var described struct {
		DisplayName string `json:"displayName"`
	}
	parseJSON(t, out, &described)
	assert.Equal(t, "cli-spend", described.DisplayName)

	const projectID = "cli-billing-proj"
	runCLI(t, gcloudCLI("projects", "create", projectID, "--format=json"))
	t.Cleanup(func() {
		_ = gcloudCLI("projects", "delete", projectID, "--quiet").Run()
	})

	runCLI(t, gcloudCLI("billing", "projects", "link", projectID,
		"--billing-account", created, "--format=json"))
	out = runCLI(t, gcloudCLI("billing", "projects", "describe", projectID, "--format=json"))
	var info struct {
		BillingAccountName string `json:"billingAccountName"`
		BillingEnabled     bool   `json:"billingEnabled"`
	}
	parseJSON(t, out, &info)
	assert.True(t, info.BillingEnabled, "the link gcloud wrote must read back enabled")
	assert.Equal(t, created, info.BillingAccountName)

	out = runCLI(t, gcloudCLI("billing", "projects", "list",
		"--billing-account", created, "--format=json"))
	var linked []struct {
		ProjectId string `json:"projectId"`
	}
	parseJSON(t, out, &linked)
	require.Len(t, linked, 1)
	assert.Equal(t, projectID, linked[0].ProjectId)

	runCLI(t, gcloudCLI("billing", "projects", "unlink", projectID, "--format=json"))
	out = runCLI(t, gcloudCLI("billing", "projects", "describe", projectID, "--format=json"))
	parseJSON(t, out, &info)
	assert.False(t, info.BillingEnabled, "unlinking must disable billing")
}

// billingCreateAccountViaAPI provisions a billing account through the API —
// the way an account exists before gcloud ever sees one — and returns its
// resource name.
func billingCreateAccountViaAPI(t *testing.T, displayName string) string {
	t.Helper()
	body := strings.NewReader(`{"displayName":"` + displayName + `"}`)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/billingAccounts", body)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var account struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&account))
	require.NotEmpty(t, account.Name)
	return account.Name
}
