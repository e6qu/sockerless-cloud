package gcp_sdk_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	arv1 "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"cloud.google.com/go/storage"
	artifactregistry "google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/option"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The artifact publish and export surface, driven through both Go clients:
//
//	POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create
//	POST /v1/projects/{project}/locations/{location}/repositories/{repo}/aptArtifacts:import
//	POST /v1/projects/{project}/locations/{location}/repositories/{repo}:exportArtifact
//	GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}:download

func arClient(t *testing.T) *arv1.Client {
	t.Helper()
	client, err := arv1.NewRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func arCreateFormatRepository(t *testing.T, svc *artifactregistry.Service, parent, repoID, format string) string {
	t.Helper()
	op, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: format}).
		RepositoryId(repoID).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	name := parent + "/repositories/" + repoID
	arDeleteRepositoryOnCleanup(t, svc, name)
	return name
}

func arCreateBucket(t *testing.T, client *storage.Client, bucket string) {
	t.Helper()
	require.NoError(t, client.Bucket(bucket).Create(ctx, "test-project", nil))
}

func arReadObject(t *testing.T, client *storage.Client, bucket, object string) []byte {
	t.Helper()
	reader, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	require.NoError(t, err, "read gs://%s/%s", bucket, object)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return data
}

func arDownloadFile(t *testing.T, svc *artifactregistry.Service, name string) []byte {
	t.Helper()
	resp, err := svc.Projects.Locations.Repositories.Files.Download(name).Download()
	require.NoError(t, err, "download %s", name)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return data
}

// A generic upload stores the bytes as the named File under the named Package
// and Version; exportArtifact writes that file into Cloud Storage.
func TestArtifactRegistry_GenericUploadDownloadAndExport(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-generic/locations/us-central1"
	repo := arCreateFormatRepository(t, svc, parent, uniqueName("generic-repo"), "GENERIC")
	content := []byte("generic release payload\n")

	uploaded, err := svc.Projects.Locations.Repositories.GenericArtifacts.Upload(repo,
		&artifactregistry.UploadGenericArtifactRequest{PackageId: "tools", VersionId: "1.2.0", Filename: "tools.tar.gz"}).
		Media(bytes.NewReader(content)).Do()
	require.NoError(t, err)
	require.True(t, uploaded.Operation.Done)
	assert.Contains(t, string(uploaded.Operation.Response), `"name":"`+repo+`/genericArtifacts/tools:1.2.0"`)

	version := repo + "/packages/tools/versions/1.2.0"
	_, err = svc.Projects.Locations.Repositories.Packages.Versions.Get(version).Do()
	require.NoError(t, err)
	fileName := repo + "/files/tools:1.2.0:tools.tar.gz"
	file, err := svc.Projects.Locations.Repositories.Files.Get(fileName).Do()
	require.NoError(t, err)
	assert.Equal(t, version, file.Owner)
	assert.Equal(t, int64(len(content)), file.SizeBytes)
	assert.Equal(t, content, arDownloadFile(t, svc, fileName))

	_, err = svc.Projects.Locations.Repositories.GenericArtifacts.Upload(repo,
		&artifactregistry.UploadGenericArtifactRequest{PackageId: "tools", VersionId: "1.2.0", Filename: "tools.tar.gz"}).
		Media(bytes.NewReader(content)).Do()
	require.Error(t, err, "a second upload of the same file conflicts")
	assert.Contains(t, err.Error(), "already exists")

	storageClient := storageClient(t)
	defer storageClient.Close()
	bucket := uniqueName("ar-export")
	arCreateBucket(t, storageClient, bucket)

	op, err := arClient(t).ExportArtifact(ctx, &artifactregistrypb.ExportArtifactRequest{
		Repository:     repo,
		SourceArtifact: &artifactregistrypb.ExportArtifactRequest_SourceVersion{SourceVersion: version},
		Destination:    &artifactregistrypb.ExportArtifactRequest_GcsPath{GcsPath: bucket + "/releases"},
	})
	require.NoError(t, err)
	exported, err := op.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, version, exported.GetExportedVersion().GetName())
	metadata, err := op.Metadata()
	require.NoError(t, err)
	require.Len(t, metadata.GetExportedFiles(), 1)
	assert.Equal(t, fileName, metadata.GetExportedFiles()[0].GetName())
	assert.Equal(t, bucket+"/releases/tools:1.2.0:tools.tar.gz", metadata.GetExportedFiles()[0].GetGcsObjectPath())
	assert.Equal(t, content, arReadObject(t, storageClient, bucket, "releases/tools:1.2.0:tools.tar.gz"))
}

// A pushed Docker image's version is its manifest digest; exporting it by tag
// writes the manifest and every blob the manifest references.
func TestArtifactRegistry_ExportPushedDockerImage(t *testing.T) {
	project, repoID := "ar-export-docker", uniqueName("docker-repo")
	arCreateRepository(t, project, "us-central1", repoID)
	repo := "projects/" + project + "/locations/us-central1/repositories/" + repoID
	image := project + "/" + repoID + "/app"

	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	layer := []byte("docker layer bytes for export")
	pushBlob := func(data []byte) string {
		digest := arDigest(data)
		resp := arDo(t, http.MethodPost, baseURL+"/v2/"+image+"/blobs/uploads/", nil, "")
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		resp = arDo(t, http.MethodPut, baseURL+loc+"?digest="+digest, data, "application/octet-stream")
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
		return digest
	}
	configDigest, layerDigest := pushBlob(config), pushBlob(layer)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":%d}]}`,
		configDigest, len(config), layerDigest, len(layer)))
	resp := arDo(t, http.MethodPut, baseURL+"/v2/"+image+"/manifests/v1", manifest, "application/vnd.oci.image.manifest.v1+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	manifestDigest := arDigest(manifest)

	storageClient := storageClient(t)
	defer storageClient.Close()
	bucket := uniqueName("ar-docker-export")
	arCreateBucket(t, storageClient, bucket)

	op, err := arClient(t).ExportArtifact(ctx, &artifactregistrypb.ExportArtifactRequest{
		Repository:     repo,
		SourceArtifact: &artifactregistrypb.ExportArtifactRequest_SourceTag{SourceTag: repo + "/packages/app/tags/v1"},
		Destination:    &artifactregistrypb.ExportArtifactRequest_GcsPath{GcsPath: bucket},
	})
	require.NoError(t, err)
	exported, err := op.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, repo+"/packages/app/versions/"+manifestDigest, exported.GetExportedVersion().GetName())
	metadata, err := op.Metadata()
	require.NoError(t, err)

	want := map[string][]byte{manifestDigest: manifest, configDigest: config, layerDigest: layer}
	require.Len(t, metadata.GetExportedFiles(), len(want))
	for _, f := range metadata.GetExportedFiles() {
		digest := strings.TrimPrefix(f.GetGcsObjectPath(), bucket+"/")
		require.Contains(t, want, digest, "exported file %v", f)
		assert.Equal(t, want[digest], arReadObject(t, storageClient, bucket, digest))
	}
}

func arTestDebPackage(t *testing.T, pkg, version, arch string) []byte {
	t.Helper()
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Test <test@example.com>\nDescription: test\n", pkg, version, arch)
	tarball := func(name, content string) []byte {
		var tb bytes.Buffer
		tw := tar.NewWriter(&tb)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
		require.NoError(t, tw.Close())
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, err = zw.Write(tb.Bytes())
		require.NoError(t, err)
		require.NoError(t, zw.Close())
		return gz.Bytes()
	}
	var deb bytes.Buffer
	deb.WriteString("!<arch>\n")
	for _, member := range []struct {
		name string
		data []byte
	}{
		{"debian-binary", []byte("2.0\n")},
		{"control.tar.gz", tarball("./control", control)},
		{"data.tar.gz", tarball("./usr/share/doc/"+pkg+"/README", "readme\n")},
	} {
		fmt.Fprintf(&deb, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", member.name, 0, 0, 0, "100644", len(member.data))
		deb.Write(member.data)
		if len(member.data)%2 == 1 {
			deb.WriteByte('\n')
		}
	}
	return deb.Bytes()
}

// importAptArtifacts reads the Debian packages a Cloud Storage URI names and
// publishes each under the package and version its control file declares.
func TestArtifactRegistry_ImportAptArtifactsFromCloudStorage(t *testing.T) {
	svc := arAdminService(t)
	parent := "projects/ar-apt-import/locations/us-central1"
	repo := arCreateFormatRepository(t, svc, parent, uniqueName("apt-repo"), "APT")

	storageClient := storageClient(t)
	defer storageClient.Close()
	bucket := uniqueName("ar-debs")
	arCreateBucket(t, storageClient, bucket)
	deb := arTestDebPackage(t, "hello", "2.10-2", "amd64")
	writer := storageClient.Bucket(bucket).Object("pool/hello_2.10-2_amd64.deb").NewWriter(ctx)
	writer.ContentType = "application/vnd.debian.binary-package"
	_, err := writer.Write(deb)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	op, err := arClient(t).ImportAptArtifacts(ctx, &artifactregistrypb.ImportAptArtifactsRequest{
		Parent: repo,
		Source: &artifactregistrypb.ImportAptArtifactsRequest_GcsSource{GcsSource: &artifactregistrypb.ImportAptArtifactsGcsSource{
			Uris: []string{"gs://" + bucket + "/pool/*.deb"}, UseWildcards: true,
		}},
	})
	require.NoError(t, err)
	imported, err := op.Wait(ctx)
	require.NoError(t, err)
	require.Empty(t, imported.GetErrors())
	require.Len(t, imported.GetAptArtifacts(), 1)
	artifact := imported.GetAptArtifacts()[0]
	assert.Equal(t, "hello", artifact.GetPackageName())
	assert.Equal(t, "amd64", artifact.GetArchitecture())
	assert.Equal(t, artifactregistrypb.AptArtifact_BINARY, artifact.GetPackageType())
	assert.Contains(t, string(artifact.GetControlFile()), "Package: hello\n")

	versions, err := svc.Projects.Locations.Repositories.Packages.Versions.List(repo + "/packages/hello").Do()
	require.NoError(t, err)
	require.Len(t, versions.Versions, 1)
	assert.Equal(t, repo+"/packages/hello/versions/2.10-2", versions.Versions[0].Name)
	assert.Equal(t, deb, arDownloadFile(t, svc, repo+"/files/hello_2.10-2_amd64.deb"))

	// An import naming an object that does not exist completes and reports it.
	op, err = arClient(t).ImportAptArtifacts(ctx, &artifactregistrypb.ImportAptArtifactsRequest{
		Parent: repo,
		Source: &artifactregistrypb.ImportAptArtifactsRequest_GcsSource{GcsSource: &artifactregistrypb.ImportAptArtifactsGcsSource{
			Uris: []string{"gs://" + bucket + "/absent.deb"},
		}},
	})
	require.NoError(t, err)
	imported, err = op.Wait(ctx)
	require.NoError(t, err)
	assert.Empty(t, imported.GetAptArtifacts())
	require.Len(t, imported.GetErrors(), 1)
	assert.Equal(t, []string{"gs://" + bucket + "/absent.deb"}, imported.GetErrors()[0].GetGcsSource().GetUris())
}
