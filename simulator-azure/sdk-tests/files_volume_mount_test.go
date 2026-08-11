package azure_sdk_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/file"
	fileservice "github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Azure Files share reached through the Files data plane and the same share
// mounted into a Container Apps job are one resource: `az storage file upload`
// puts a file where the job's Volume{StorageType: AzureFile} reads it. This
// test drives both ends with their real clients — the azfile SDK writes, an
// alpine container mounting the share reads — so nothing but agreement on the
// bytes can make it pass.
func TestStorageSDK_FilesDataPlaneBytesReachAContainerAppsMount(t *testing.T) {
	const (
		account     = "mountfileacct"
		share       = "mounted-share"
		fileName    = "hello.txt"
		rg          = "sdk-files-mount-rg"
		env         = "files-mount-env"
		storageName = "mounted-files"
		jobName     = "files-mount-job"
	)
	payload := []byte("bytes the mounted container must read")

	serviceClient, err := fileservice.NewClientWithNoCredential(storageSDKURL(t, account, "file"),
		&fileservice.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	fileClient, err := file.NewClientWithNoCredential(storageSDKURL(t, account, "file")+share+"/"+fileName,
		&file.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))

	// The share materializes at <files data dir>/<account>/<share>, which is
	// the host path the simulator's Container Apps executor bind-mounts.
	hostPath := filepath.Join(azureFilesDataDir, account, share, fileName)
	onDisk, err := os.ReadFile(hostPath)
	require.NoError(t, err, "the uploaded file must exist in the directory a volume mount binds: %s", hostPath)
	assert.Equal(t, payload, onDisk, "the mounted share must hold the uploaded bytes")

	// Now mount it: the environment storage names the account and share, the
	// job's volume references the storage, and the container reads the file.
	ensureRG(t, rg)
	ensureManagedEnv(t, rg, env)

	cred := &fakeCredential{}
	storagesClient, err := armappcontainers.NewManagedEnvironmentsStoragesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	_, err = storagesClient.CreateOrUpdate(ctx, rg, env, storageName, armappcontainers.ManagedEnvironmentStorage{
		Properties: &armappcontainers.ManagedEnvironmentStorageProperties{
			AzureFile: &armappcontainers.AzureFileProperties{
				AccountName: to.Ptr(account),
				ShareName:   to.Ptr(share),
				AccountKey:  to.Ptr("c2ltdWxhdG9yLWFjY291bnQta2V5"),
				AccessMode:  to.Ptr(armappcontainers.AccessModeReadWrite),
			},
		},
	}, nil)
	require.NoError(t, err)

	envID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s",
		subscriptionID, rg, env)

	jobsClient, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	// The container exits 0 only when the mounted file holds exactly the bytes
	// the data plane uploaded, so the execution status is the assertion.
	readBack := fmt.Sprintf(`test "$(cat /mnt/share/%s)" = "%s"`, fileName, payload)
	poller, err := jobsClient.BeginCreateOrUpdate(ctx, rg, jobName, armappcontainers.Job{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.JobProperties{
			EnvironmentID: to.Ptr(envID),
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:    to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout: to.Ptr[int32](60),
			},
			Template: &armappcontainers.JobTemplate{
				Volumes: []*armappcontainers.Volume{{
					Name:        to.Ptr("share"),
					StorageType: to.Ptr(armappcontainers.StorageTypeAzureFile),
					StorageName: to.Ptr(storageName),
				}},
				Containers: []*armappcontainers.Container{{
					Name:    to.Ptr("reader"),
					Image:   to.Ptr("public.ecr.aws/docker/library/alpine:latest"),
					Command: []*string{to.Ptr("sh"), to.Ptr("-c"), to.Ptr(readBack)},
					VolumeMounts: []*armappcontainers.VolumeMount{{
						VolumeName: to.Ptr("share"),
						MountPath:  to.Ptr("/mnt/share"),
					}},
				}},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	startPoller, err := jobsClient.BeginStart(ctx, rg, jobName, nil)
	require.NoError(t, err)
	execResp, err := startPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, execResp.Name)
	execName := *execResp.Name

	execClient, err := armappcontainers.NewJobsExecutionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	status := ""
	require.Eventually(t, func() bool {
		pager := execClient.NewListPager(rg, jobName, nil)
		for pager.More() {
			page, perr := pager.NextPage(ctx)
			if perr != nil {
				return false
			}
			for _, e := range page.Value {
				if e.Name == nil || *e.Name != execName || e.Properties == nil || e.Properties.Status == nil {
					continue
				}
				status = string(*e.Properties.Status)
				return status != "Running"
			}
		}
		return false
	}, 120*time.Second, 250*time.Millisecond, "the mounting execution never reached a terminal state")
	assert.Equal(t, "Succeeded", status,
		"the job mounting the share must read back the bytes uploaded through the Files data plane")
}
