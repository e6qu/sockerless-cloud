package azure_cli_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the App Service backup, restore and snapshot family. The
// archive is written into a real blob container and read back with native
// `az storage blob` commands, so the container the backup names is the same
// container the CLI browses:
//
//	PUT+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/backup
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/backup/list
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backup
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backups
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/listbackups
//	GET+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backups/{backupId}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backups/{backupId}/list
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backups/{backupId}/restore
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/discoverbackup
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/restoreFromBackupBlob
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/restoreSnapshot
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/restoreFromDeletedApp
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/snapshots
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/snapshotsdr
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/deletedSites
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/deletedSites/{deletedSiteId}

const (
	backupCLIAPIVersion = "2025-03-01"
	backupCLIAccount    = "clibackupacct"
	backupCLIContainer  = "websitebackups"
)

// azBackupStorage builds an `az storage` invocation against the simulator's
// path-style Blob endpoint for the backup account.
func azBackupStorage(group string, args ...string) *exec.Cmd {
	baseArgs := append([]string{"storage", group}, args...)
	baseArgs = append(baseArgs,
		"--auth-mode", "key",
		"--account-name", backupCLIAccount,
		"--account-key", cliStorageAccountKey(backupCLIAccount),
		"--blob-endpoint", baseURL+"/"+backupCLIAccount+"/",
		"--only-show-errors",
		"--output", "json",
	)
	cmd := exec.Command("az", baseArgs...)
	cmd.Env = append(os.Environ(),
		"AZURE_CONFIG_DIR="+filepath.Join(tmpDir, "azure-config"),
		"AZURE_CORE_NO_COLOR=1",
	)
	return cmd
}

func backupCLIURL(resourcePath string) string {
	return armURL("Microsoft.Web", resourcePath, backupCLIAPIVersion)
}

// TestWebAppBackupRestore_CLI drives the whole family: configure custom
// backups against a SAS-addressed container, take a backup, see the archive in
// the container, change the app's content, restore it back, and confirm the
// restore removed what the backup did not carry.
func TestWebAppBackupRestore_CLI(t *testing.T) {
	site := "cli-backup-app"

	// The container the backup writes into, and a genuine container SAS signed
	// with the account key — the coordinate Microsoft documents App Service
	// backup as requiring.
	runCLI(t, azBackupStorage("container", "create", "--name", backupCLIContainer))
	t.Cleanup(func() {
		_ = azBackupStorage("container", "delete", "--name", backupCLIContainer).Run()
	})
	sasToken := strings.Trim(strings.TrimSpace(
		runCLI(t, azBackupStorage("container", "generate-sas",
			"--name", backupCLIContainer,
			"--permissions", "rwdl",
			"--expiry", time.Now().UTC().Add(24*time.Hour).Format("2006-01-02T15:04Z")))), `"`)
	require.Contains(t, sasToken, "sig=", "az must sign the container SAS with the account key")
	containerURL := fmt.Sprintf("https://%s.blob.core.windows.net/%s?%s",
		backupCLIAccount, backupCLIContainer, sasToken)

	runCLI(t, azRest("PUT", backupCLIURL("sites/"+site),
		`{"location":"eastus","kind":"functionapp"}`))
	t.Cleanup(func() { _ = azRest("DELETE", backupCLIURL("sites/"+site), "").Run() })

	// Content the backup can be recognized by: two triggered webjobs.
	backupCLIDeploy(t, site, map[string]string{
		"App_Data/jobs/triggered/report/run.sh": "#!/bin/sh\nexit 0\n",
		"App_Data/jobs/triggered/keepme/run.sh": "#!/bin/sh\nexit 0\n",
	})
	assert.ElementsMatch(t, []string{"keepme", "report"}, backupCLIWebJobs(t, site))

	runCLI(t, azRest("PUT", backupCLIURL("sites/"+site+"/config/backup"),
		fmt.Sprintf(`{"properties":{"backupName":"clinightly","enabled":true,"storageAccountUrl":%q,
		 "backupSchedule":{"frequencyInterval":1,"frequencyUnit":"Day","keepAtLeastOneBackup":true,"retentionPeriodInDays":7}}}`,
			containerURL)))
	var cfg struct {
		Properties struct {
			BackupName        string `json:"backupName"`
			StorageAccountURL string `json:"storageAccountUrl"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("POST", backupCLIURL("sites/"+site+"/config/backup/list"), "")), &cfg)
	assert.Equal(t, "clinightly", cfg.Properties.BackupName)
	assert.Equal(t, containerURL, cfg.Properties.StorageAccountURL)

	var item struct {
		Properties struct {
			BackupID    int    `json:"id"`
			BlobName    string `json:"blobName"`
			Status      string `json:"status"`
			SizeInBytes int64  `json:"sizeInBytes"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("POST", backupCLIURL("sites/"+site+"/backup"),
		fmt.Sprintf(`{"properties":{"backupName":"clinightly","storageAccountUrl":%q}}`, containerURL))), &item)
	require.Equal(t, "Succeeded", item.Properties.Status)
	require.NotEmpty(t, item.Properties.BlobName)
	require.Greater(t, item.Properties.SizeInBytes, int64(0))

	// The archive and its manifest really landed in the container.
	var blobs []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, azBackupStorage("blob", "list", "--container-name", backupCLIContainer)), &blobs)
	names := make([]string, 0, len(blobs))
	for _, b := range blobs {
		names = append(names, b.Name)
	}
	assert.Contains(t, names, item.Properties.BlobName,
		"the backup archive must be browsable in the storage container")
	assert.Contains(t, names, strings.TrimSuffix(item.Properties.BlobName, ".zip")+".xml",
		"Microsoft documents an XML manifest beside every backup ZIP")

	backupID := fmt.Sprintf("%d", item.Properties.BackupID)
	var listed struct {
		Value []struct {
			Properties struct {
				BlobName          string `json:"blobName"`
				StorageAccountURL string `json:"storageAccountUrl"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", backupCLIURL("sites/"+site+"/backups"), "")), &listed)
	require.Len(t, listed.Value, 1)
	assert.Empty(t, listed.Value[0].Properties.StorageAccountURL,
		"a backup list must not carry the storage SAS secret")

	var siteListed struct {
		Value []map[string]any `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("POST", backupCLIURL("sites/"+site+"/listbackups"), "")), &siteListed)
	assert.Len(t, siteListed.Value, 1)

	runCLI(t, azRest("GET", backupCLIURL("sites/"+site+"/backups/"+backupID), ""))

	var secrets struct {
		Properties struct {
			StorageAccountURL string `json:"storageAccountUrl"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("POST", backupCLIURL("sites/"+site+"/backups/"+backupID+"/list"),
		`{"properties":{}}`)), &secrets)
	assert.Equal(t, containerURL, secrets.Properties.StorageAccountURL,
		"only ListBackupStatusSecrets carries the SAS")

	var discovered struct {
		Properties struct {
			SiteName string `json:"siteName"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("POST", backupCLIURL("sites/"+site+"/discoverbackup"),
		fmt.Sprintf(`{"properties":{"storageAccountUrl":%q,"blobName":%q,"overwrite":true}}`,
			containerURL, item.Properties.BlobName))), &discovered)
	assert.Equal(t, site, discovered.Properties.SiteName)

	backupCLIDeploy(t, site, map[string]string{
		"App_Data/jobs/triggered/extra/run.sh": "#!/bin/sh\nexit 0\n",
	})
	require.ElementsMatch(t, []string{"extra", "keepme", "report"}, backupCLIWebJobs(t, site),
		"a deployment merges: it does not remove files it does not carry")

	runCLI(t, azRest("POST", backupCLIURL("sites/"+site+"/backups/"+backupID+"/restore"),
		fmt.Sprintf(`{"properties":{"storageAccountUrl":%q,"blobName":%q,"overwrite":true}}`,
			containerURL, item.Properties.BlobName)))
	backupCLIAwait(t, func() bool {
		return len(backupCLIWebJobs(t, site)) == 2
	}, "the restore must reinstate exactly the backed-up file set")
	assert.ElementsMatch(t, []string{"keepme", "report"}, backupCLIWebJobs(t, site))

	// RestoreFromBackupBlob into a second app.
	target := "cli-backup-target"
	runCLI(t, azRest("PUT", backupCLIURL("sites/"+target), `{"location":"eastus","kind":"functionapp"}`))
	t.Cleanup(func() { _ = azRest("DELETE", backupCLIURL("sites/"+target), "").Run() })
	runCLI(t, azRest("POST", backupCLIURL("sites/"+target+"/restoreFromBackupBlob"),
		fmt.Sprintf(`{"properties":{"storageAccountUrl":%q,"blobName":%q,"overwrite":true}}`,
			containerURL, item.Properties.BlobName)))
	backupCLIAwait(t, func() bool {
		return len(backupCLIWebJobs(t, target)) == 2
	}, "the archive must restore into a different app")

	var snaps struct {
		Value []struct {
			Properties struct {
				Time string `json:"time"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", backupCLIURL("sites/"+site+"/snapshots"), "")), &snaps)
	require.NotEmpty(t, snaps.Value, "a real content change produces a platform snapshot")
	var drSnaps struct {
		Value []map[string]any `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", backupCLIURL("sites/"+site+"/snapshotsdr"), "")), &drSnaps)
	assert.Len(t, drSnaps.Value, len(snaps.Value),
		"the geo-redundant secondary holds the replicated snapshots")
	runCLI(t, azRest("POST", backupCLIURL("sites/"+site+"/restoreSnapshot"),
		`{"properties":{"overwrite":true}}`))

	runCLI(t, azRest("DELETE", backupCLIURL("sites/"+site+"/backups/"+backupID), ""))
	var afterDelete []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, azBackupStorage("blob", "list", "--container-name", backupCLIContainer)), &afterDelete)
	for _, b := range afterDelete {
		assert.NotEqual(t, item.Properties.BlobName, b.Name,
			"deleting a backup must remove its archive from the container")
	}

	runCLI(t, azRest("DELETE", backupCLIURL("sites/"+site+"/config/backup"), ""))

	runCLI(t, azRest("DELETE", backupCLIURL("sites/"+target), ""))
	var deleted struct {
		Value []struct {
			Properties struct {
				DeletedSiteID   int    `json:"deletedSiteId"`
				DeletedSiteName string `json:"deletedSiteName"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET",
		fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/deletedSites?api-version=%s",
			baseURL, subscriptionID, backupCLIAPIVersion), "")), &deleted)
	deletedID := 0
	for _, row := range deleted.Value {
		if row.Properties.DeletedSiteName == target {
			deletedID = row.Properties.DeletedSiteID
		}
	}
	require.NotZero(t, deletedID, "the deleted app must be retained and listed")

	// DeletedWebApps_GetDeletedWebAppByLocation — the same retained app
	// addressed through the region it ran in.
	var byLocation struct {
		Properties struct {
			DeletedSiteName string `json:"deletedSiteName"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET",
		fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/locations/eastus/deletedSites/%d?api-version=%s",
			baseURL, subscriptionID, deletedID, backupCLIAPIVersion), "")), &byLocation)
	assert.Equal(t, target, byLocation.Properties.DeletedSiteName,
		"the location-scoped read must find the app deleted in that region")

	recovered := "cli-backup-recovered"
	runCLI(t, azRest("PUT", backupCLIURL("sites/"+recovered), `{"location":"eastus","kind":"functionapp"}`))
	t.Cleanup(func() { _ = azRest("DELETE", backupCLIURL("sites/"+recovered), "").Run() })
	runCLI(t, azRest("POST", backupCLIURL("sites/"+recovered+"/restoreFromDeletedApp"),
		fmt.Sprintf(`{"properties":{"deletedSiteId":"%d","recoverConfiguration":true}}`, deletedID)))
	backupCLIAwait(t, func() bool {
		return len(backupCLIWebJobs(t, recovered)) == 2
	}, "the deleted app's content must be restored into the recovery app")
}

// backupCLIDeploy publishes a package through the MSDeploy extension and waits
// for the long-running operation to succeed.
func backupCLIDeploy(t *testing.T, site string, files map[string]string) {
	t.Helper()
	pkgURL := stage3ServeZip(t, stage3Zip(t, files))
	msDeploy := backupCLIURL("sites/" + site + "/extensions/MSDeploy")
	runCLI(t, azRest("PUT", msDeploy, fmt.Sprintf(`{"properties":{"packageUri":%q}}`, pkgURL)))
	backupCLIAwait(t, func() bool {
		var status struct {
			Properties struct {
				ProvisioningState string `json:"provisioningState"`
			} `json:"properties"`
		}
		parseJSON(t, runCLI(t, azRest("GET", msDeploy, "")), &status)
		return status.Properties.ProvisioningState == "succeeded"
	}, "MSDeploy provisioningState")
}

// backupCLIWebJobs lists the site's discovered webjobs, the observable
// projection of its deployed file system.
func backupCLIWebJobs(t *testing.T, site string) []string {
	t.Helper()
	var jobs struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", backupCLIURL("sites/"+site+"/webjobs"), "")), &jobs)
	out := make([]string, 0, len(jobs.Value))
	for _, j := range jobs.Value {
		name := j.Name
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		out = append(out, name)
	}
	return out
}

// backupCLIAwait polls a long-running operation's observable effect. The
// restores are declared long-running, so the caller sees the accepted response
// before the work lands.
func backupCLIAwait(t *testing.T, done func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if done() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
