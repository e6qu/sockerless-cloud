package gcp_sdk_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// arPushImage pushes a one-layer image to the Docker data plane the way an
// engine does — one monolithic blob write, then the manifest by tag — and
// returns the manifest's digest.
func arPushImage(t *testing.T, repo, tag, content string) string {
	t.Helper()
	layer := []byte(content)
	digest := arDigest(layer)
	resp := arDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	location := resp.Header.Get("Location")
	resp.Body.Close()
	resp = arDo(t, http.MethodPut, baseURL+location+"?digest="+digest, layer, "application/octet-stream")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"%s","size":%d},"layers":[{"digest":"%s","size":%d}]}`,
		digest, len(layer), digest, len(layer)))
	resp = arDo(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/"+tag, manifest, "application/vnd.oci.image.manifest.v1+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	return arDigest(manifest)
}

// Deleting an image's manifest over the Docker data plane deletes the version
// and tags the control plane shows for it, and deleting a package deletes its
// Docker images and their manifests.
func TestArtifactRegistry_DeletesCascadeAcrossPlanes(t *testing.T) {
	repoID := uniqueName("cascade-repo")
	arCreateRepository(t, "test-project", "us-central1", repoID)
	svc := arAdminService(t)
	repoName := "projects/test-project/locations/us-central1/repositories/" + repoID
	pkgName := repoName + "/packages/app"
	image := "test-project/" + repoID + "/app"

	imageCount := func() int {
		t.Helper()
		images, err := svc.Projects.Locations.Repositories.DockerImages.List(repoName).Do()
		require.NoError(t, err)
		return len(images.DockerImages)
	}

	digest := arPushImage(t, image, "v1", "first-layer")
	versions, err := svc.Projects.Locations.Repositories.Packages.Versions.List(pkgName).Do()
	require.NoError(t, err)
	require.Len(t, versions.Versions, 1)
	assert.Equal(t, pkgName+"/versions/"+digest, versions.Versions[0].Name)
	require.Equal(t, 1, imageCount())

	resp := arDo(t, http.MethodDelete, baseURL+"/v2/"+image+"/manifests/"+digest, nil, "")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	versions, err = svc.Projects.Locations.Repositories.Packages.Versions.List(pkgName).Do()
	require.NoError(t, err)
	assert.Empty(t, versions.Versions, "the manifest delete removed its version")
	tags, err := svc.Projects.Locations.Repositories.Packages.Tags.List(pkgName).Do()
	require.NoError(t, err)
	assert.Empty(t, tags.Tags, "the manifest delete removed the tag that pointed at it")
	assert.Zero(t, imageCount(), "the manifest delete removed its Docker image")

	arPushImage(t, image, "v2", "second-layer")
	require.Equal(t, 1, imageCount())
	op, err := svc.Projects.Locations.Repositories.Packages.Delete(pkgName).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	assert.Zero(t, imageCount(), "the package delete removed its Docker images")
	resp = arDo(t, http.MethodGet, baseURL+"/v2/"+image+"/manifests/v2", nil, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "the package delete removed its manifests")
	resp.Body.Close()
}
