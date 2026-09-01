package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	run "google.golang.org/api/run/v2"
)

func cloudRunV2Service(t *testing.T) *run.Service {
	t.Helper()
	svc, err := run.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// Cloud Run's source uploads and image exports. A client deploying from source
// asks where to put it; going the other way it asks Cloud Run to export a
// service's image and then polls for how that went.
func TestCloudRunV2_UploadSourceAndExportImage(t *testing.T) {
	svc := cloudRunV2Service(t)
	const project, location = "run-export", "us-central1"
	parent := "projects/" + project + "/locations/" + location

	// Where to upload source before deploying from it.
	upload, err := svc.Projects.Locations.SourceUploads.Upload(parent,
		&run.GoogleCloudRunV2UploadSourceRequest{Service: parent + "/services/web"}).Do()
	require.NoError(t, err)
	require.NotNil(t, upload.CloudStorageSource)
	assert.Contains(t, upload.CloudStorageSource.Bucket, project)
	assert.NotEmpty(t, upload.CloudStorageSource.Object)

	// A location's metadata export answers for the location it names.
	metadata, err := svc.Projects.Locations.ExportProjectMetadata(parent).Do()
	require.NoError(t, err)
	require.NotNil(t, metadata)

	// An image export hands back the operation id the caller polls with.
	exported, err := svc.Projects.Locations.ExportImage(parent+"/services/web",
		&run.GoogleCloudRunV2ExportImageRequest{
			DestinationRepo: "us-docker.pkg.dev/" + project + "/exports",
		}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, exported.OperationId)

	// An export with nowhere to go is not a request.
	_, err = svc.Projects.Locations.ExportImage(parent+"/services/web",
		&run.GoogleCloudRunV2ExportImageRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destinationRepo")

	// The status is read back through the resource the export ran against,
	// and answers from what was actually asked for.
	status, err := svc.Projects.Locations.Services.Revisions.ExportStatus(
		parent+"/services/web/revisions/web-00001", exported.OperationId).Do()
	require.NoError(t, err)
	assert.Equal(t, exported.OperationId, status.OperationId)
	assert.Equal(t, "FINISHED", status.OperationState)
	require.Len(t, status.ImageExportStatuses, 1)

	// An execution's export status is read the same way, through the execution
	// the export ran against.
	fromJob, err := svc.Projects.Locations.Jobs.Executions.ExportStatus(
		parent+"/jobs/batch/executions/batch-00001", exported.OperationId).Do()
	require.NoError(t, err)
	assert.Equal(t, exported.OperationId, fromJob.OperationId)

	// An operation id nobody was given reports no export.
	_, err = svc.Projects.Locations.Services.Revisions.ExportStatus(
		parent+"/services/web/revisions/web-00001", "export-never").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no image export")
}
