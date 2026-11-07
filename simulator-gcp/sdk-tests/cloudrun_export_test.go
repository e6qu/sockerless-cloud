package gcp_sdk_test

import (
	"net/http"
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

func TestCloudRunV2_UploadSourceAndExportImage(t *testing.T) {
	t.Parallel()
	svc := cloudRunV2Service(t)
	const project, location = "run-export", "us-central1"
	ensureProject(t, project)
	parent := "projects/" + project + "/locations/" + location

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

	// The service runs an image this project's Artifact Registry holds.
	arCreateRepository(t, project, location, "src")
	arCreateRepository(t, project, location, "exports")
	digest := arPushImage(t, project+"/src/app", "v1", "export-layer")
	createOp, err := svc.Projects.Locations.Services.Create(parent, &run.GoogleCloudRunV2Service{
		Template: &run.GoogleCloudRunV2RevisionTemplate{
			Containers: []*run.GoogleCloudRunV2Container{{Image: location + "-docker.pkg.dev/" + project + "/src/app:v1"}},
		},
	}).ServiceId("web").Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, createOp)

	// An image export copies the image into the destination repository and
	// hands back the operation id the caller polls with.
	exported, err := svc.Projects.Locations.ExportImage(parent+"/services/web",
		&run.GoogleCloudRunV2ExportImageRequest{
			DestinationRepo: location + "-docker.pkg.dev/" + project + "/exports/app:copied",
		}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, exported.OperationId)

	// An export with nowhere to go is not a request.
	_, err = svc.Projects.Locations.ExportImage(parent+"/services/web",
		&run.GoogleCloudRunV2ExportImageRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destinationRepo")

	// The status is read back through the resource the export ran against.
	status, err := svc.Projects.Locations.Services.Revisions.ExportStatus(
		parent+"/services/web/revisions/web-00001", exported.OperationId).Do()
	require.NoError(t, err)
	assert.Equal(t, exported.OperationId, status.OperationId)
	assert.Equal(t, "FINISHED", status.OperationState)
	require.Len(t, status.ImageExportStatuses, 1)
	assert.Equal(t, digest, status.ImageExportStatuses[0].ExportedImageDigest)
	resp := arDo(t, http.MethodGet, baseURL+"/v2/"+project+"/exports/app/manifests/copied", nil, "")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the exported tag serves the image")
	resp.Body.Close()

	// An execution's export status is read the same way, through the execution
	// the export ran against.
	fromJob, err := svc.Projects.Locations.Jobs.Executions.ExportStatus(
		parent+"/jobs/batch/executions/batch-00001", exported.OperationId).Do()
	require.NoError(t, err)
	assert.Equal(t, exported.OperationId, fromJob.OperationId)

	// A destination repository that does not exist fails the export, which
	// still finishes.
	missing, err := svc.Projects.Locations.ExportImage(parent+"/services/web",
		&run.GoogleCloudRunV2ExportImageRequest{
			DestinationRepo: location + "-docker.pkg.dev/" + project + "/nowhere/app",
		}).Do()
	require.NoError(t, err)
	failed, err := svc.Projects.Locations.Services.Revisions.ExportStatus(
		parent+"/services/web/revisions/web-00001", missing.OperationId).Do()
	require.NoError(t, err)
	require.Len(t, failed.ImageExportStatuses, 1)
	require.NotNil(t, failed.ImageExportStatuses[0].Status)
	assert.NotZero(t, failed.ImageExportStatuses[0].Status.Code)

	// An operation id nobody was given reports no export.
	_, err = svc.Projects.Locations.Services.Revisions.ExportStatus(
		parent+"/services/web/revisions/web-00001", "export-never").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no image export")
}
