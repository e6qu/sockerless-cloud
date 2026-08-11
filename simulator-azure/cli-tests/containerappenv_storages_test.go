package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// containerappenv_storages_test.go covers the
// managedEnvironments/{envName}/storages sub-resource — the Azure Files mount
// definition an ACA app or job references by name — through the real az CLI.
// It is the control-plane request `az containerapp env storage set / show /
// list / remove` sends.
//
//	PUT    .../Microsoft.App/managedEnvironments/{envName}/storages/{storageName}
//	GET    .../Microsoft.App/managedEnvironments/{envName}/storages/{storageName}
//	DELETE .../Microsoft.App/managedEnvironments/{envName}/storages/{storageName}
//	GET    .../Microsoft.App/managedEnvironments/{envName}/storages
func TestContainerAppEnvStorages_CRUD(t *testing.T) {
	envURL := caeURL("managedEnvironments/cli-storage-env")
	runCLI(t, azRest("PUT", envURL, `{"location":"eastus","properties":{}}`))
	defer runCLI(t, azRest("DELETE", envURL, ""))

	storageURL := caeURL("managedEnvironments/cli-storage-env/storages/cli-share")
	listURL := caeURL("managedEnvironments/cli-storage-env/storages")

	type storageShape struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Properties struct {
			AzureFile struct {
				AccountName string `json:"accountName"`
				ShareName   string `json:"shareName"`
				AccessMode  string `json:"accessMode"`
			} `json:"azureFile"`
		} `json:"properties"`
	}

	body := `{"properties":{"azureFile":{"accountName":"simstorageacct",` +
		`"shareName":"simshare","accountKey":"c2ltdWxhdG9yLWFjY291bnQta2V5",` +
		`"accessMode":"ReadWrite"}}}`
	out := runCLI(t, azRest("PUT", storageURL, body))
	var created storageShape
	parseJSON(t, out, &created)
	assert.Equal(t, "cli-share", created.Name)
	assert.Equal(t, "Microsoft.App/managedEnvironments/storages", created.Type)
	assert.Contains(t, created.ID, "/managedEnvironments/cli-storage-env/storages/cli-share")
	assert.Equal(t, "simshare", created.Properties.AzureFile.ShareName)
	assert.Equal(t, "ReadWrite", created.Properties.AzureFile.AccessMode)

	// GET the single storage.
	out = runCLI(t, azRest("GET", storageURL, ""))
	var got storageShape
	parseJSON(t, out, &got)
	assert.Equal(t, "simstorageacct", got.Properties.AzureFile.AccountName)
	assert.Equal(t, "simshare", got.Properties.AzureFile.ShareName)

	// LIST storages under the environment.
	out = runCLI(t, azRest("GET", listURL, ""))
	var list struct {
		Value []storageShape `json:"value"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, s := range list.Value {
		if s.Name == "cli-share" {
			found = true
			assert.Equal(t, "simshare", s.Properties.AzureFile.ShareName)
		}
	}
	require.True(t, found, "list must include the storage created above")

	// DELETE, then confirm it is gone from both GET and LIST.
	runCLI(t, azRest("DELETE", storageURL, ""))

	cmd := azRest("GET", storageURL, "")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "GET after DELETE must fail")

	out = runCLI(t, azRest("GET", listURL, ""))
	var afterList struct {
		Value []storageShape `json:"value"`
	}
	parseJSON(t, out, &afterList)
	for _, s := range afterList.Value {
		assert.NotEqual(t, "cli-share", s.Name, "deleted storage must not be listed")
	}
}

// TestContainerAppEnvStorages_ParentValidation checks a storage write under a
// missing environment is rejected the way real ARM rejects it, rather than
// silently creating an orphan row.
func TestContainerAppEnvStorages_ParentValidation(t *testing.T) {
	orphanURL := caeURL("managedEnvironments/cli-no-such-env/storages/orphan")
	body := `{"properties":{"azureFile":{"accountName":"simstorageacct",` +
		`"shareName":"simshare","accessMode":"ReadOnly"}}}`
	cmd := azRest("PUT", orphanURL, body)
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "storage write under a missing environment must fail")
}
