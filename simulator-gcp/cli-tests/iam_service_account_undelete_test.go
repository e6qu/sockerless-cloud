package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAMServiceAccountUndeleteCLI drives `gcloud iam service-accounts
// undelete <unique-id>`, which calls projects.serviceAccounts.undelete on
// projects/-/serviceAccounts/{uniqueId}:undelete.
func TestIAMServiceAccountUndeleteCLI(t *testing.T) {
	const accountID = "cli-undelete-sa"
	email := accountID + "@" + project + ".iam.gserviceaccount.com"

	var created struct {
		UniqueID string `json:"uniqueId"`
	}
	parseJSONObject(t, runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID,
		"--display-name=Undelete via gcloud", "--format=json")), &created)
	require.NotEmpty(t, created.UniqueID)

	runCLI(t, gcloudCLI("iam", "service-accounts", "delete", email))
	out := gcloudCLIFails(t, gcloudCLI("iam", "service-accounts", "describe", email, "--format=json"))
	assert.Contains(t, out, "NOT_FOUND", "a deleted account is not found")

	var undeleted struct {
		RestoredAccount struct {
			UniqueID    string `json:"uniqueId"`
			Email       string `json:"email"`
			DisplayName string `json:"displayName"`
		} `json:"restoredAccount"`
	}
	parseJSONObject(t, runCLI(t, gcloudCLI("iam", "service-accounts", "undelete", created.UniqueID, "--format=json")), &undeleted)
	assert.Equal(t, created.UniqueID, undeleted.RestoredAccount.UniqueID, "undelete keeps the unique ID")
	assert.Equal(t, email, undeleted.RestoredAccount.Email)
	assert.Equal(t, "Undelete via gcloud", undeleted.RestoredAccount.DisplayName)

	var described struct {
		UniqueID string `json:"uniqueId"`
	}
	parseJSONObject(t, runCLI(t, gcloudCLI("iam", "service-accounts", "describe", email, "--format=json")), &described)
	assert.Equal(t, created.UniqueID, described.UniqueID)

	out = gcloudCLIFails(t, gcloudCLI("iam", "service-accounts", "undelete", "100000000000000000001", "--format=json"))
	assert.Contains(t, out, "PERMISSION_DENIED", "an unknown unique ID answers PERMISSION_DENIED through the - wildcard")
}
