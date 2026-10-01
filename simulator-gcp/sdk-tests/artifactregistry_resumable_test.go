package gcp_sdk_test

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	artifactregistry "google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/googleapi"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The media methods past one chunk, which the Go client sends through the
// resumable upload protocol: it begins the session on the /upload path and
// POSTs each chunk to the session URI the Location header names.
//
//	POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload
//	POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create

// arChunkedMedia is larger than three minimum-size chunks, so ChunkSize
// splits it into four.
func arChunkedMedia() []byte {
	return bytes.Repeat([]byte("resumable chunk payload\n"), 3*googleapi.MinUploadChunkSize/24+100)
}

func TestArtifactRegistry_ResumableFileUpload(t *testing.T) {
	svc := arAdminService(t)
	repo := arCreateFormatRepository(t, svc, "projects/ar-resumable/locations/us-central1", uniqueName("files-repo"), "GENERIC")
	content := arChunkedMedia()

	var progress []int64
	uploaded, err := svc.Projects.Locations.Repositories.Files.Upload(repo, &artifactregistry.UploadFileRequest{FileId: "big.bin"}).
		Media(bytes.NewReader(content), googleapi.ChunkSize(googleapi.MinUploadChunkSize), googleapi.ContentType("application/octet-stream")).
		ProgressUpdater(func(current, _ int64) { progress = append(progress, current) }).
		Do()
	require.NoError(t, err)
	require.True(t, uploaded.Operation.Done)
	assert.Contains(t, string(uploaded.Operation.Response), `"name":"`+repo+`/files/big.bin"`)
	require.GreaterOrEqual(t, len(progress), 4, "the client sent the media in chunks")
	assert.Equal(t, int64(len(content)), progress[len(progress)-1])

	file, err := svc.Projects.Locations.Repositories.Files.Get(repo + "/files/big.bin").Do()
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), file.SizeBytes)
	assert.Equal(t, content, arDownloadFile(t, svc, repo+"/files/big.bin"))

	_, err = svc.Projects.Locations.Repositories.Files.Delete(repo + "/files/big.bin").Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.Repositories.Files.Get(repo + "/files/big.bin").Do()
	require.Error(t, err)
}

func TestArtifactRegistry_ResumableGenericUpload(t *testing.T) {
	svc := arAdminService(t)
	repo := arCreateFormatRepository(t, svc, "projects/ar-resumable/locations/us-central1", uniqueName("generic-repo"), "GENERIC")
	content := arChunkedMedia()

	uploaded, err := svc.Projects.Locations.Repositories.GenericArtifacts.Upload(repo,
		&artifactregistry.UploadGenericArtifactRequest{PackageId: "bundle", VersionId: "1.0.0", Filename: "bundle.tar"}).
		Media(bytes.NewReader(content), googleapi.ChunkSize(googleapi.MinUploadChunkSize)).Do()
	require.NoError(t, err)
	require.True(t, uploaded.Operation.Done)
	assert.Contains(t, string(uploaded.Operation.Response), `"name":"`+repo+`/genericArtifacts/bundle:1.0.0"`)
	assert.Equal(t, content, arDownloadFile(t, svc, repo+"/files/bundle:1.0.0:bundle.tar"))

	_, err = svc.Projects.Locations.Repositories.GenericArtifacts.Upload(repo,
		&artifactregistry.UploadGenericArtifactRequest{PackageId: "bundle", VersionId: "1.0.0", Filename: "bundle.tar"}).
		Media(bytes.NewReader(content), googleapi.ChunkSize(googleapi.MinUploadChunkSize)).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr, "a resumable upload of an existing file fails")
	assert.Equal(t, http.StatusConflict, apiErr.Code)
}

// A Docker push records each manifest and blob as a File named by its digest;
// files.delete is refused outside generic repositories.
func TestArtifactRegistry_DockerPushFilesAndRegistryURI(t *testing.T) {
	svc := arAdminService(t)
	project, repoID := "ar-push-files", uniqueName("docker-repo")
	arCreateRepository(t, project, "us-central1", repoID)
	repo := "projects/" + project + "/locations/us-central1/repositories/" + repoID
	image := project + "/" + repoID + "/app"

	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	layer := []byte("docker layer recorded as a file")
	for _, blob := range [][]byte{config, layer} {
		resp := arDo(t, http.MethodPost, baseURL+"/v2/"+image+"/blobs/uploads/", nil, "")
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		resp = arDo(t, http.MethodPut, baseURL+loc+"?digest="+arDigest(blob), blob, "application/octet-stream")
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":%d}]}`,
		arDigest(config), len(config), arDigest(layer), len(layer)))
	resp := arDo(t, http.MethodPut, baseURL+"/v2/"+image+"/manifests/v1", manifest, "application/vnd.oci.image.manifest.v1+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	files, err := svc.Projects.Locations.Repositories.Files.List(repo).Do()
	require.NoError(t, err)
	assert.Len(t, files.Files, 3)
	layerFile, err := svc.Projects.Locations.Repositories.Files.Get(repo + "/files/" + arDigest(layer)).Do()
	require.NoError(t, err)
	assert.Equal(t, repo+"/packages/app/versions/"+arDigest(manifest), layerFile.Owner)
	assert.Equal(t, int64(len(layer)), layerFile.SizeBytes)
	assert.Equal(t, layer, arDownloadFile(t, svc, repo+"/files/"+arDigest(layer)))

	_, err = svc.Projects.Locations.Repositories.Files.Delete(repo + "/files/" + arDigest(layer)).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr, "files.delete outside a generic repository fails")
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)

	got, err := svc.Projects.Locations.Repositories.Get(repo).Do()
	require.NoError(t, err)
	assert.Equal(t, "us-central1-docker.pkg.dev/"+project+"/"+repoID, got.RegistryUri)
	mavenID := uniqueName("maven-repo")
	maven := arCreateFormatRepository(t, svc, "projects/"+project+"/locations/us-central1", mavenID, "MAVEN")
	got, err = svc.Projects.Locations.Repositories.Get(maven).Do()
	require.NoError(t, err)
	assert.Equal(t, "us-central1-maven.pkg.dev/"+project+"/"+mavenID, got.RegistryUri)
}
