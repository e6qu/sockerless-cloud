package azure_sdk_test

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the App Service backup, restore and snapshot family. Every
// case drives the official armappservice.WebAppsClient and reads the archive
// back through the official azblob clients, so the backup blob is asserted on
// the same surface a customer downloads it from:
//
//	PUT+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/backup (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/backup/list (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backup (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backups (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/listbackups (+ slot)
//	GET+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backups/{backupId} (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backups/{backupId}/list (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/backups/{backupId}/restore (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/discoverbackup (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/restoreFromBackupBlob (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/restoreFromDeletedApp (+ slot)
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/restoreSnapshot (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/snapshots (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/snapshotsdr (+ slot)
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites/{deletedSiteId}
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites/{deletedSiteId}/snapshots

// backupStorage is a storage account, a container in it, and the container SAS
// URL an App Service backup is configured with — the coordinate Microsoft
// documents as required ("Custom backups for Azure App Service require an
// Azure Storage account that supports Shared Access Signature (SAS)–based
// authorization").
type backupStorage struct {
	account   string
	container string
	sasURL    string
	client    *container.Client
}

// newBackupStorage provisions the account through Microsoft.Storage, reads its
// real access key, creates the container, and signs a container SAS with that
// key — the same sequence an operator performs before configuring a backup.
func newBackupStorage(t *testing.T, rg, account, containerName string) backupStorage {
	t.Helper()
	accounts := createStorageAccountForARM(t, rg, account)
	keys, err := accounts.ListKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	key1, _ := storageAccountKeyValues(t, keys.Keys)

	cred, err := azblob.NewSharedKeyCredential(account, key1)
	require.NoError(t, err)
	svcURL := storageSDKURL(t, account, "blob")
	client, err := container.NewClientWithSharedKeyCredential(svcURL+containerName, cred,
		&container.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = client.Create(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.Delete(ctx, nil) })

	sasURL, err := client.GetSASURL(
		sas.ContainerPermissions{Read: true, Add: true, Create: true, Write: true, Delete: true, List: true},
		time.Now().UTC().Add(24*time.Hour), nil)
	require.NoError(t, err)
	return backupStorage{account: account, container: containerName, sasURL: sasURL, client: client}
}

// download reads one blob back through the Blob data plane.
func (s backupStorage) download(t *testing.T, name string) ([]byte, bool) {
	t.Helper()
	resp, err := s.client.NewBlobClient(name).DownloadStream(ctx, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.ErrorCode == "BlobNotFound" {
			return nil, false
		}
		require.NoError(t, err)
	}
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return body, true
}

// zipEntries unpacks an archive into path → contents.
func zipEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err, "the backup archive must be a readable ZIP file")
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		out[f.Name] = string(body)
	}
	return out
}

// webJobNames lists the site's discovered webjobs, which is the observable
// projection of its deployed file system: a job exists exactly while its
// App_Data/jobs/... script does.
func webJobNames(t *testing.T, client *armappservice.WebAppsClient, rg, name string) []string {
	t.Helper()
	var out []string
	pager := client.NewListWebJobsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, job := range page.Value {
			require.NotNil(t, job.Name)
			// A child resource's name is "<site>/<job>".
			n := *job.Name
			if idx := strings.LastIndex(n, "/"); idx >= 0 {
				n = n[idx+1:]
			}
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// triggeredJob renders one triggered webjob's run script, so a deployed file
// set is identifiable by the jobs it produces.
func triggeredJob(name, body string) map[string]string {
	return map[string]string{"App_Data/jobs/triggered/" + name + "/run.sh": body}
}

func mergeFiles(sets ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// slotWebJobNames is webJobNames at the deployment-slot scope.
func slotWebJobNames(t *testing.T, client *armappservice.WebAppsClient, rg, name, slot string) []string {
	t.Helper()
	var out []string
	pager := client.NewListWebJobsSlotPager(rg, name, slot, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, job := range page.Value {
			n := *job.Name
			if idx := strings.LastIndex(n, "/"); idx >= 0 {
				n = n[idx+1:]
			}
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// snapshotTimes lists the app's platform snapshots, from the primary region or
// from the geo-redundant secondary.
func snapshotTimes(t *testing.T, client *armappservice.WebAppsClient, rg, name string, dr bool) []string {
	t.Helper()
	var out []string
	collect := func(rows []*armappservice.Snapshot) {
		for _, snap := range rows {
			require.NotNil(t, snap.Properties)
			require.NotNil(t, snap.Properties.Time)
			out = append(out, *snap.Properties.Time)
		}
	}
	if dr {
		pager := client.NewListSnapshotsFromDRSecondaryPager(rg, name, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			collect(page.Value)
		}
		return out
	}
	pager := client.NewListSnapshotsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		collect(page.Value)
	}
	return out
}

// slotSnapshotTimes is snapshotTimes at the deployment-slot scope.
func slotSnapshotTimes(t *testing.T, client *armappservice.WebAppsClient, rg, name, slot string, dr bool) []string {
	t.Helper()
	var out []string
	collect := func(rows []*armappservice.Snapshot) {
		for _, snap := range rows {
			out = append(out, *snap.Properties.Time)
		}
	}
	if dr {
		pager := client.NewListSnapshotsFromDRSecondarySlotPager(rg, name, slot, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			collect(page.Value)
		}
		return out
	}
	pager := client.NewListSnapshotsSlotPager(rg, name, slot, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		collect(page.Value)
	}
	return out
}

// listDeletedSites enumerates the subscription's retained deleted apps.
func listDeletedSites(t *testing.T) []*armappservice.DeletedSite {
	t.Helper()
	client, err := armappservice.NewDeletedWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	var out []*armappservice.DeletedSite
	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		out = append(out, page.Value...)
	}
	return out
}

// requireAzureErrorCode asserts the service refused a request with a specific
// Azure Resource Manager error code, which is what a caller branches on.
func requireAzureErrorCode(t *testing.T, err error, code, what string) {
	t.Helper()
	require.Error(t, err, "%s: expected the service to refuse the request", what)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "%s: want *azcore.ResponseError, got %T: %v", what, err, err)
	require.Equal(t, code, respErr.ErrorCode, "%s: %v", what, err)
}

// deployPackage publishes a package through the MSDeploy long-running
// operation, which is how content reaches an App Service app.
func deployPackage(t *testing.T, client *armappservice.WebAppsClient, rg, name string, files map[string]string) {
	t.Helper()
	poller, err := client.BeginCreateMSDeployOperation(ctx, rg, name, armappservice.MSDeploy{
		Properties: &armappservice.MSDeployCore{PackageURI: to.Ptr(servePackage(t, makeJobsZip(t, files)))},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestSDK_WebApps_BackupPrerequisites covers the two service behaviours an App
// Service backup depends on and that a client cannot work around:
//
//   - An App Service plan's pricing tier is derived from its SKU name. A client
//     that sends only the name — which is all terraform-provider-azurerm's
//     azurerm_service_plan sends — reads the derived tier back, and consumers
//     branch on it (backup is offered on Basic and above, never on Dynamic).
//   - A blob container is one object with two management surfaces. A container
//     created through Microsoft.Storage is the same container the account's
//     blob endpoint serves, so a backup addressed at it can be written and read.
func TestSDK_WebApps_BackupPrerequisites(t *testing.T) {
	rg := "sdk-backup-prereq-rg"

	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := plans.BeginCreateOrUpdate(ctx, rg, "sdk-backup-prereq-plan", armappservice.Plan{
		Location: to.Ptr("eastus"),
		// Only the SKU name, exactly as a client that lets the service decide
		// the rest sends it.
		SKU: &armappservice.SKUDescription{Name: to.Ptr("S1")},
	}, nil)
	require.NoError(t, err)
	plan, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, plan.SKU)
	require.NotNil(t, plan.SKU.Tier)
	assert.Equal(t, "Standard", *plan.SKU.Tier,
		"the service derives an S1 plan's tier; a plan reported as Dynamic is refused a backup configuration")
	require.NotNil(t, plan.SKU.Size)
	assert.Equal(t, "S1", *plan.SKU.Size)

	// A container created through the Azure Resource Manager plane.
	account := "sdkbackupprereq"
	createStorageAccountForARM(t, rg, account)
	containers, err := armstorage.NewBlobContainersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	const containerName = "armmadecontainer"
	_, err = containers.Create(ctx, rg, account, containerName, armstorage.BlobContainer{
		ContainerProperties: &armstorage.ContainerProperties{PublicAccess: to.Ptr(armstorage.PublicAccessNone)},
	}, nil)
	require.NoError(t, err)

	// The same container, on the account's blob endpoint.
	blobs := newBlobTestClient(t, account)
	_, err = blobs.UploadBuffer(ctx, containerName, "hello.txt", []byte("one container, two surfaces"), nil)
	require.NoError(t, err, "a container created through Microsoft.Storage must be usable on the Blob data plane")
	stream, err := blobs.DownloadStream(ctx, containerName, "hello.txt", nil)
	require.NoError(t, err)
	body, err := io.ReadAll(stream.Body)
	require.NoError(t, err)
	require.NoError(t, stream.Body.Close())
	assert.Equal(t, "one container, two surfaces", string(body))
}

// TestSDK_WebApps_BackupRestoreBlobRoundTrip is the end-to-end proof: an app
// whose content is identifiable is backed up into a real storage container,
// the archive is read back through the Blob API and asserted to hold that
// content, the app's content is genuinely changed, the restore puts it back,
// and deleting the archive blob makes the restore fail — which is what proves
// the blob IS the backup rather than a copy the service kept.
func TestSDK_WebApps_BackupRestoreBlobRoundTrip(t *testing.T) {
	rg, name := "sdk-backup-rg", "sdk-backup-app"
	azureCreateSite(t, rg, name, nil)
	defer azureDeleteSite(rg, name)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	store := newBackupStorage(t, rg, "sdkbackupacct", "websitebackups")

	// --- The app's identifiable content -------------------------------------
	original := mergeFiles(
		triggeredJob("report", "#!/bin/sh\necho original report\n"),
		triggeredJob("keepme", "#!/bin/sh\necho keepme\n"),
	)
	deployPackage(t, client, rg, name, original)
	require.Equal(t, []string{"keepme", "report"}, webJobNames(t, client, rg, name),
		"the deployed content must be observable as its webjobs")

	// A custom domain, which Microsoft documents the manifest listing and the
	// restore reconciling.
	const customDomain = "backup.sockerless.test"
	_, err = client.CreateOrUpdateHostNameBinding(ctx, rg, name, customDomain,
		armappservice.HostNameBinding{Properties: &armappservice.HostNameBindingProperties{
			SiteName: to.Ptr(name), HostNameType: to.Ptr(armappservice.HostNameTypeVerified),
		}}, nil)
	require.NoError(t, err)

	// --- Configure custom backups -------------------------------------------
	cfg, err := client.UpdateBackupConfiguration(ctx, rg, name, armappservice.BackupRequest{
		Properties: &armappservice.BackupRequestProperties{
			BackupName:        to.Ptr("nightly"),
			Enabled:           to.Ptr(true),
			StorageAccountURL: to.Ptr(store.sasURL),
			BackupSchedule: &armappservice.BackupSchedule{
				FrequencyInterval:     to.Ptr[int32](1),
				FrequencyUnit:         to.Ptr(armappservice.FrequencyUnitDay),
				KeepAtLeastOneBackup:  to.Ptr(true),
				RetentionPeriodInDays: to.Ptr[int32](7),
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, cfg.Properties)
	assert.Equal(t, "nightly", *cfg.Properties.BackupName)

	read, err := client.GetBackupConfiguration(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, read.Properties)
	assert.Equal(t, store.sasURL, *read.Properties.StorageAccountURL,
		"the configured SAS URL must survive the round trip")
	require.NotNil(t, read.Properties.BackupSchedule)
	assert.EqualValues(t, 7, *read.Properties.BackupSchedule.RetentionPeriodInDays)

	// --- Take the backup ------------------------------------------------------
	item, err := client.Backup(ctx, rg, name, armappservice.BackupRequest{
		Properties: &armappservice.BackupRequestProperties{
			BackupName:        to.Ptr("nightly"),
			StorageAccountURL: to.Ptr(store.sasURL),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, item.Properties)
	require.NotNil(t, item.Properties.Status)
	require.Equal(t, armappservice.BackupItemStatusSucceeded, *item.Properties.Status,
		"log: %v", to.Ptr(item.Properties.Log))
	blobName := *item.Properties.BlobName
	backupID := *item.Properties.BackupID
	assert.Greater(t, *item.Properties.SizeInBytes, int64(0))
	assert.Greater(t, *item.Properties.WebsiteSizeInBytes, int64(0))
	assert.Nil(t, item.Properties.StorageAccountURL,
		"GetBackupStatus-shaped reads must not carry the storage SAS secret")

	// --- The archive really exists in the storage account --------------------
	archive, ok := store.download(t, blobName)
	require.True(t, ok, "the backup archive %q must exist in container %q", blobName, store.container)
	entries := zipEntries(t, archive)
	assert.Equal(t, "#!/bin/sh\necho original report\n",
		entries["site/wwwroot/App_Data/jobs/triggered/report/run.sh"],
		"the archive must hold the app's real content, keyed under site/wwwroot")
	assert.Contains(t, entries, "site/wwwroot/App_Data/jobs/triggered/keepme/run.sh")

	manifestName := strings.TrimSuffix(blobName, ".zip") + ".xml"
	manifestBody, ok := store.download(t, manifestName)
	require.True(t, ok, "Microsoft documents an XML manifest beside every backup ZIP")
	var manifest struct {
		SiteName  string   `xml:"siteName,attr"`
		HostNames []string `xml:"hostNames>hostName"`
		Files     []struct {
			Path string `xml:"path,attr"`
			Size int    `xml:"size,attr"`
		} `xml:"files>file"`
	}
	require.NoError(t, xml.Unmarshal(manifestBody, &manifest))
	assert.Equal(t, name, manifest.SiteName)
	assert.Len(t, manifest.Files, 2, "the manifest lists the ZIP's contents")
	assert.Equal(t, []string{customDomain}, manifest.HostNames,
		"the manifest lists the app's custom domains, not its platform hostnames")

	// Remove the custom domain: the restore must put it back, because the
	// manifest lists it and nothing else binds it.
	_, err = client.DeleteHostNameBinding(ctx, rg, name, customDomain, nil)
	require.NoError(t, err)
	_, err = client.GetHostNameBinding(ctx, rg, name, customDomain, nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "the custom domain must really be unbound")

	// --- The backup is listed and addressable ---------------------------------
	var listed []*armappservice.BackupItem
	pager := client.NewListBackupsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		listed = append(listed, page.Value...)
	}
	require.Len(t, listed, 1)
	assert.Equal(t, backupID, *listed[0].Properties.BackupID)

	var siteListed []*armappservice.BackupItem
	sitePager := client.NewListSiteBackupsPager(rg, name, nil)
	for sitePager.More() {
		page, err := sitePager.NextPage(ctx)
		require.NoError(t, err)
		siteListed = append(siteListed, page.Value...)
	}
	require.Len(t, siteListed, 1, "ListSiteBackups returns the same collection as ListBackups")

	status, err := client.GetBackupStatus(ctx, rg, name, itoa(int(backupID)), nil)
	require.NoError(t, err)
	assert.Equal(t, blobName, *status.Properties.BlobName)

	// ListBackupStatusSecrets is the only read that carries the SAS.
	secrets, err := client.ListBackupStatusSecrets(ctx, rg, name, itoa(int(backupID)),
		armappservice.BackupRequest{Properties: &armappservice.BackupRequestProperties{}}, nil)
	require.NoError(t, err)
	require.NotNil(t, secrets.Properties.StorageAccountURL)
	assert.Equal(t, store.sasURL, *secrets.Properties.StorageAccountURL)

	// --- DiscoverBackup reads the archive's own manifest ----------------------
	discovered, err := client.DiscoverBackup(ctx, rg, name, armappservice.RestoreRequest{
		Properties: &armappservice.RestoreRequestProperties{
			StorageAccountURL: to.Ptr(store.sasURL),
			BlobName:          to.Ptr(blobName),
			Overwrite:         to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, discovered.Properties)
	assert.Equal(t, name, *discovered.Properties.SiteName)

	// --- Genuinely change the content -----------------------------------------
	// A Web Deploy package creates and overwrites, and leaves files it does not
	// carry alone (the DoNotDeleteRule default the "remove additional files at
	// destination" switch exists to turn off), so this MERGES.
	deployPackage(t, client, rg, name, mergeFiles(
		triggeredJob("report", "#!/bin/sh\necho replaced report\n"),
		triggeredJob("extra", "#!/bin/sh\necho extra\n"),
	))
	require.Equal(t, []string{"extra", "keepme", "report"}, webJobNames(t, client, rg, name),
		"a deployment must merge: keepme survives a package that does not carry it")

	// --- Restore, which is the operation that DOES delete ----------------------
	// Microsoft: "Without _backup.filter, restoring a backup deletes all
	// existing files in the app and replaces them with the files in the backup."
	restorePoller, err := client.BeginRestore(ctx, rg, name, itoa(int(backupID)), armappservice.RestoreRequest{
		Properties: &armappservice.RestoreRequestProperties{
			StorageAccountURL: to.Ptr(store.sasURL),
			BlobName:          to.Ptr(blobName),
			Overwrite:         to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = restorePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"keepme", "report"}, webJobNames(t, client, rg, name),
		"the restore must reinstate the backed-up file set exactly — extra must be gone")

	rebound, err := client.GetHostNameBinding(ctx, rg, name, customDomain, nil)
	require.NoError(t, err, "the restore must re-add the manifest's custom domain")
	assert.Equal(t, name+"/"+customDomain, *rebound.Name)

	afterRestore, err := client.GetBackupStatus(ctx, rg, name, itoa(int(backupID)), nil)
	require.NoError(t, err)
	require.NotNil(t, afterRestore.Properties.LastRestoreTimeStamp,
		"a restore stamps the backup item it used")

	// --- Negative control: the blob IS the backup ------------------------------
	// Delete the archive through the Blob API and the very same restore must
	// fail. A restore that still succeeded would prove the service kept its own
	// copy and the blob round trip was decorative.
	_, err = store.client.NewBlobClient(blobName).Delete(ctx, nil)
	require.NoError(t, err)
	gone, exists := store.download(t, blobName)
	require.False(t, exists, "the archive must really be gone: %d bytes", len(gone))

	_, err = client.BeginRestore(ctx, rg, name, itoa(int(backupID)), armappservice.RestoreRequest{
		Properties: &armappservice.RestoreRequestProperties{
			StorageAccountURL: to.Ptr(store.sasURL),
			BlobName:          to.Ptr(blobName),
			Overwrite:         to.Ptr(true),
		},
	}, nil)
	requireAzureErrorCode(t, err, "ResourceNotFound",
		"restoring from a deleted archive blob must fail")
}

// TestSDK_WebApps_BackupRestoreIntoAnotherApp proves the archive is portable:
// a second app restores the first app's backup out of the same container, and
// the second app's own content is replaced by it.
func TestSDK_WebApps_BackupRestoreIntoAnotherApp(t *testing.T) {
	rg := "sdk-backup-xfer-rg"
	source, dest := "sdk-backup-src", "sdk-backup-dst"
	azureCreateSite(t, rg, source, nil)
	defer azureDeleteSite(rg, source)
	azureCreateSite(t, rg, dest, nil)
	defer azureDeleteSite(rg, dest)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	store := newBackupStorage(t, rg, "sdkbackupxferacct", "websitebackups")

	deployPackage(t, client, rg, source, triggeredJob("sourcejob", "#!/bin/sh\necho source\n"))
	deployPackage(t, client, rg, dest, triggeredJob("destjob", "#!/bin/sh\necho dest\n"))
	require.Equal(t, []string{"destjob"}, webJobNames(t, client, rg, dest))

	item, err := client.Backup(ctx, rg, source, armappservice.BackupRequest{
		Properties: &armappservice.BackupRequestProperties{
			BackupName: to.Ptr("xfer"), StorageAccountURL: to.Ptr(store.sasURL),
		},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, armappservice.BackupItemStatusSucceeded, *item.Properties.Status)

	poller, err := client.BeginRestoreFromBackupBlob(ctx, rg, dest, armappservice.RestoreRequest{
		Properties: &armappservice.RestoreRequestProperties{
			StorageAccountURL: to.Ptr(store.sasURL),
			BlobName:          item.Properties.BlobName,
			Overwrite:         to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"sourcejob"}, webJobNames(t, client, rg, dest),
		"restoring the source app's archive must replace the destination's content")
}

// TestSDK_WebApps_BackupRefusals covers the three documented refusals: a
// storageAccountUrl without the mandatory SAS parameters, a URL whose host
// names no Blob endpoint, and a container that does not exist.
func TestSDK_WebApps_BackupRefusals(t *testing.T) {
	rg, name := "sdk-backup-refuse-rg", "sdk-backup-refuse-app"
	azureCreateSite(t, rg, name, nil)
	defer azureDeleteSite(rg, name)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	store := newBackupStorage(t, rg, "sdkbackuprefuse", "websitebackups")

	backupTo := func(url string) error {
		_, err := client.Backup(ctx, rg, name, armappservice.BackupRequest{
			Properties: &armappservice.BackupRequestProperties{StorageAccountURL: to.Ptr(url)},
		}, nil)
		return err
	}

	// A container URL with no SAS at all.
	plain := strings.Split(store.sasURL, "?")[0]
	requireAzureErrorCode(t, backupTo(plain), "InvalidRequestContent",
		"a storageAccountUrl without a SAS must be refused")

	// A well-formed SAS against a container that was never created.
	missing := strings.Replace(store.sasURL, "/"+store.container+"?", "/nosuchcontainer?", 1)
	requireAzureErrorCode(t, backupTo(missing), "StorageAccessFailed",
		"a SAS naming a container that does not exist must be refused")

	// A SAS whose host is not a Blob service endpoint.
	notBlob := strings.Replace(store.sasURL, ".blob.", ".queue.", 1)
	requireAzureErrorCode(t, backupTo(notBlob), "CannotResolveStorageAccount",
		"a storageAccountUrl that does not address the Blob service must be refused")

	// No backup was recorded by any of the three refusals.
	pager := client.NewListBackupsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		require.Empty(t, page.Value, "a refused backup must not appear in the backup list")
	}
}

// TestSDK_WebApps_BackupDeleteRemovesTheBlobs proves DeleteBackup removes the
// archive and its manifest from the storage account, not just the ARM record.
func TestSDK_WebApps_BackupDeleteRemovesTheBlobs(t *testing.T) {
	rg, name := "sdk-backup-del-rg", "sdk-backup-del-app"
	azureCreateSite(t, rg, name, nil)
	defer azureDeleteSite(rg, name)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	store := newBackupStorage(t, rg, "sdkbackupdelacct", "websitebackups")
	deployPackage(t, client, rg, name, triggeredJob("job", "#!/bin/sh\nexit 0\n"))

	item, err := client.Backup(ctx, rg, name, armappservice.BackupRequest{
		Properties: &armappservice.BackupRequestProperties{
			BackupName: to.Ptr("doomed"), StorageAccountURL: to.Ptr(store.sasURL),
		},
	}, nil)
	require.NoError(t, err)
	blobName := *item.Properties.BlobName
	_, present := store.download(t, blobName)
	require.True(t, present)

	_, err = client.DeleteBackup(ctx, rg, name, itoa(int(*item.Properties.BackupID)), nil)
	require.NoError(t, err)

	_, present = store.download(t, blobName)
	assert.False(t, present, "deleting a backup must remove its archive from the container")
	_, present = store.download(t, strings.TrimSuffix(blobName, ".zip")+".xml")
	assert.False(t, present, "deleting a backup must remove its manifest too")

	_, err = client.GetBackupStatus(ctx, rg, name, itoa(int(*item.Properties.BackupID)), nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "a deleted backup must be gone from ARM too")
}

// TestSDK_WebApps_SnapshotsAndDeletedAppRestore covers the platform-snapshot
// half: the snapshots an app's real state changes produce, the geo-redundant
// secondary they replicate to, a snapshot restore, and the deleted-app
// retention that WebApps_RestoreFromDeletedApp replays.
func TestSDK_WebApps_SnapshotsAndDeletedAppRestore(t *testing.T) {
	rg, name := "sdk-snapshot-rg", "sdk-snapshot-app"
	azureCreateSite(t, rg, name, nil)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	deployPackage(t, client, rg, name, triggeredJob("first", "#!/bin/sh\necho first\n"))
	firstSnapshots := snapshotTimes(t, client, rg, name, false)
	require.NotEmpty(t, firstSnapshots, "a content change must produce a platform snapshot")

	deployPackage(t, client, rg, name, triggeredJob("second", "#!/bin/sh\necho second\n"))
	laterSnapshots := snapshotTimes(t, client, rg, name, false)
	require.Greater(t, len(laterSnapshots), len(firstSnapshots),
		"each real state change adds a snapshot")
	assert.Equal(t, []string{"first", "second"}, webJobNames(t, client, rg, name))

	// The geo-redundant secondary holds the same replicated set.
	assert.Equal(t, laterSnapshots, snapshotTimes(t, client, rg, name, true),
		"the DR secondary must hold the replicated snapshots")

	// Restoring the first snapshot removes the second deployment's job.
	snapPoller, err := client.BeginRestoreSnapshot(ctx, rg, name, armappservice.SnapshotRestoreRequest{
		Properties: &armappservice.SnapshotRestoreRequestProperties{
			SnapshotTime: to.Ptr(firstSnapshots[len(firstSnapshots)-1]),
			Overwrite:    to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = snapPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"first"}, webJobNames(t, client, rg, name),
		"a snapshot restore reinstates exactly the state that snapshot captured")

	// A snapshot the app never had is refused rather than silently substituted.
	_, err = client.BeginRestoreSnapshot(ctx, rg, name, armappservice.SnapshotRestoreRequest{
		Properties: &armappservice.SnapshotRestoreRequestProperties{
			SnapshotTime: to.Ptr("2001-01-01T00:00:00.0000000Z"),
			Overwrite:    to.Ptr(true),
		},
	}, nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "an unknown snapshotTime must be refused")

	// --- Delete the app and restore it into a fresh one ------------------------
	azureDeleteSite(rg, name)

	deleted, err := armappservice.NewGlobalClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	deletedList := listDeletedSites(t)
	require.NotEmpty(t, deletedList, "a deleted app must be retained")
	var retained *armappservice.DeletedSite
	for _, row := range deletedList {
		if row.Properties != nil && row.Properties.DeletedSiteName != nil && *row.Properties.DeletedSiteName == name {
			retained = row
		}
	}
	require.NotNil(t, retained, "the deleted app %q must be listed", name)
	deletedID := itoa(int(*retained.Properties.DeletedSiteID))

	got, err := deleted.GetDeletedWebApp(ctx, deletedID, nil)
	require.NoError(t, err)
	assert.Equal(t, name, *got.Properties.DeletedSiteName)

	snaps, err := deleted.GetDeletedWebAppSnapshots(ctx, deletedID, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, snaps.SnapshotArray, "the deleted app keeps its snapshot history")

	target := "sdk-snapshot-restored"
	azureCreateSite(t, rg, target, nil)
	defer azureDeleteSite(rg, target)
	require.Empty(t, webJobNames(t, client, rg, target))

	restorePoller, err := client.BeginRestoreFromDeletedApp(ctx, rg, target, armappservice.DeletedAppRestoreRequest{
		Properties: &armappservice.DeletedAppRestoreRequestProperties{
			DeletedSiteID:        to.Ptr(deletedID),
			RecoverConfiguration: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = restorePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"first"}, webJobNames(t, client, rg, target),
		"the deleted app's content must land in the target app")

	_, err = client.BeginRestoreFromDeletedApp(ctx, rg, target, armappservice.DeletedAppRestoreRequest{
		Properties: &armappservice.DeletedAppRestoreRequestProperties{
			DeletedSiteID: to.Ptr("999999"),
		},
	}, nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "an unknown deletedSiteId must be refused")
}

// TestSDK_WebApps_BackupRestoreOnASlot runs the same contract at the
// deployment-slot scope, where every operation has its own spelling.
func TestSDK_WebApps_BackupRestoreOnASlot(t *testing.T) {
	rg, name, slot := "sdk-backup-slot-rg", "sdk-backup-slot-app", "staging"
	azureCreateSite(t, rg, name, nil)
	defer azureDeleteSite(rg, name)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	slotPoller, err := client.BeginCreateOrUpdateSlot(ctx, rg, name, slot, armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	store := newBackupStorage(t, rg, "sdkbackupslotacct", "websitebackups")

	_, err = client.UpdateBackupConfigurationSlot(ctx, rg, name, slot, armappservice.BackupRequest{
		Properties: &armappservice.BackupRequestProperties{
			BackupName: to.Ptr("slotnightly"), Enabled: to.Ptr(true),
			StorageAccountURL: to.Ptr(store.sasURL),
		},
	}, nil)
	require.NoError(t, err)
	slotCfg, err := client.GetBackupConfigurationSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	assert.Equal(t, "slotnightly", *slotCfg.Properties.BackupName)

	slotDeploy, err := client.BeginCreateMSDeployOperationSlot(ctx, rg, name, slot, armappservice.MSDeploy{
		Properties: &armappservice.MSDeployCore{PackageURI: to.Ptr(
			servePackage(t, makeJobsZip(t, triggeredJob("slotjob", "#!/bin/sh\nexit 0\n"))))},
	}, nil)
	require.NoError(t, err)
	_, err = slotDeploy.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// A backup that names no storage takes the slot's configured storage, which
	// is how a scheduled backup runs.
	item, err := client.BackupSlot(ctx, rg, name, slot, armappservice.BackupRequest{
		Properties: &armappservice.BackupRequestProperties{},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, armappservice.BackupItemStatusSucceeded, *item.Properties.Status)
	require.NotNil(t, item.Properties.Scheduled)
	assert.True(t, *item.Properties.Scheduled,
		"a backup run off the app's configuration is a scheduled backup")

	archive, ok := store.download(t, *item.Properties.BlobName)
	require.True(t, ok)
	assert.Contains(t, zipEntries(t, archive), "site/wwwroot/App_Data/jobs/triggered/slotjob/run.sh")

	var listed []*armappservice.BackupItem
	pager := client.NewListBackupsSlotPager(rg, name, slot, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		listed = append(listed, page.Value...)
	}
	require.Len(t, listed, 1)

	var siteListed []*armappservice.BackupItem
	sitePager := client.NewListSiteBackupsSlotPager(rg, name, slot, nil)
	for sitePager.More() {
		page, err := sitePager.NextPage(ctx)
		require.NoError(t, err)
		siteListed = append(siteListed, page.Value...)
	}
	require.Len(t, siteListed, 1)

	backupID := itoa(int(*item.Properties.BackupID))
	_, err = client.GetBackupStatusSlot(ctx, rg, name, backupID, slot, nil)
	require.NoError(t, err)
	secrets, err := client.ListBackupStatusSecretsSlot(ctx, rg, name, backupID, slot,
		armappservice.BackupRequest{Properties: &armappservice.BackupRequestProperties{}}, nil)
	require.NoError(t, err)
	assert.Equal(t, store.sasURL, *secrets.Properties.StorageAccountURL)

	_, err = client.DiscoverBackupSlot(ctx, rg, name, slot, armappservice.RestoreRequest{
		Properties: &armappservice.RestoreRequestProperties{
			StorageAccountURL: to.Ptr(store.sasURL), BlobName: item.Properties.BlobName,
			Overwrite: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)

	// Merge in another job, then restore the slot's backup and see it go.
	slotDeploy2, err := client.BeginCreateMSDeployOperationSlot(ctx, rg, name, slot, armappservice.MSDeploy{
		Properties: &armappservice.MSDeployCore{PackageURI: to.Ptr(
			servePackage(t, makeJobsZip(t, triggeredJob("slotextra", "#!/bin/sh\nexit 0\n"))))},
	}, nil)
	require.NoError(t, err)
	_, err = slotDeploy2.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"slotextra", "slotjob"}, slotWebJobNames(t, client, rg, name, slot))

	restore, err := client.BeginRestoreSlot(ctx, rg, name, backupID, slot, armappservice.RestoreRequest{
		Properties: &armappservice.RestoreRequestProperties{
			StorageAccountURL: to.Ptr(store.sasURL), BlobName: item.Properties.BlobName,
			Overwrite: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = restore.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"slotjob"}, slotWebJobNames(t, client, rg, name, slot))

	// Slot snapshots and the slot's DR secondary.
	assert.NotEmpty(t, slotSnapshotTimes(t, client, rg, name, slot, false))
	assert.Equal(t, slotSnapshotTimes(t, client, rg, name, slot, false),
		slotSnapshotTimes(t, client, rg, name, slot, true))

	slotSnap, err := client.BeginRestoreSnapshotSlot(ctx, rg, name, slot, armappservice.SnapshotRestoreRequest{
		Properties: &armappservice.SnapshotRestoreRequestProperties{Overwrite: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	_, err = slotSnap.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// The slot spellings of the two blob-sourced restores.
	blobSlot, err := client.BeginRestoreFromBackupBlobSlot(ctx, rg, name, slot, armappservice.RestoreRequest{
		Properties: &armappservice.RestoreRequestProperties{
			StorageAccountURL: to.Ptr(store.sasURL), BlobName: item.Properties.BlobName,
			Overwrite: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = blobSlot.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"slotjob"}, slotWebJobNames(t, client, rg, name, slot))

	_, err = client.BeginRestoreFromDeletedAppSlot(ctx, rg, name, slot,
		armappservice.DeletedAppRestoreRequest{
			Properties: &armappservice.DeletedAppRestoreRequestProperties{
				DeletedSiteID: to.Ptr("999999"),
			},
		}, nil)
	requireAzureErrorCode(t, err, "ResourceNotFound",
		"an unknown deletedSiteId must be refused at the slot scope too")

	_, err = client.DeleteBackupSlot(ctx, rg, name, backupID, slot, nil)
	require.NoError(t, err)
	_, err = client.DeleteBackupConfigurationSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	_, err = client.GetBackupConfigurationSlot(ctx, rg, name, slot, nil)
	requireAzureErrorCode(t, err, "NotFound",
		"an app with no backup configuration has no such sub-resource")

	_, err = client.DeleteBackupConfiguration(ctx, rg, name, nil)
	require.NoError(t, err)
}
