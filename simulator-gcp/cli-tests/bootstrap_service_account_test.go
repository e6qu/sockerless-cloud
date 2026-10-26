package gcp_cli_test

import (
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/gcpbootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBootstrapServiceAccountCLI reads the account gcloud is signed in as back
// through IAM: the seeded bootstrap account and the key whose credential file
// TestMain activated.
func TestBootstrapServiceAccountCLI(t *testing.T) {
	t.Parallel()
	assert.Equal(t, gcpbootstrap.Email, strings.TrimSpace(runCLI(t, gcloudCLI("config", "get", "account"))))

	var sa struct {
		Email     string `json:"email"`
		ProjectID string `json:"projectId"`
		UniqueID  string `json:"uniqueId"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("iam", "service-accounts", "describe", gcpbootstrap.Email,
		"--project="+gcpbootstrap.ProjectID, "--format=json")), &sa)
	assert.Equal(t, gcpbootstrap.Email, sa.Email)
	assert.Equal(t, gcpbootstrap.ProjectID, sa.ProjectID)
	assert.Equal(t, gcpbootstrap.ClientID, sa.UniqueID)

	var keys []struct {
		Name      string `json:"name"`
		KeyType   string `json:"keyType"`
		KeyOrigin string `json:"keyOrigin"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "list",
		"--iam-account="+gcpbootstrap.Email, "--project="+gcpbootstrap.ProjectID,
		"--managed-by=user", "--format=json")), &keys)
	require.Len(t, keys, 1)
	assert.True(t, strings.HasSuffix(keys[0].Name, "/keys/"+gcpbootstrap.KeyID), keys[0].Name)
	assert.Equal(t, "USER_MANAGED", keys[0].KeyType)
	assert.Equal(t, "USER_PROVIDED", keys[0].KeyOrigin)
}
