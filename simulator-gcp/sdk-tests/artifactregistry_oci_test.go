package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	artifactregistry "google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/option"
)

func TestArtifactRegistryDockerHubRemoteRepositorySDKAndOCI(t *testing.T) {
	service, err := artifactregistry.NewService(ctx,
		option.WithEndpoint(baseURL+"/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	parent := "projects/test-project/locations/us-central1"
	repo := &artifactregistry.Repository{
		Format: "DOCKER",
		Mode:   "REMOTE_REPOSITORY",
		RemoteRepositoryConfig: &artifactregistry.RemoteRepositoryConfig{
			Description: "Proxies docker.io / Docker Hub",
			DockerRepository: &artifactregistry.DockerRepository{
				PublicRepository: "DOCKER_HUB",
			},
		},
	}
	op, err := service.Projects.Locations.Repositories.Create(parent, repo).RepositoryId("docker-hub").Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	var created artifactregistry.Repository
	require.NoError(t, json.Unmarshal(op.Response, &created))
	require.Equal(t, "REMOTE_REPOSITORY", created.Mode)
	require.NotNil(t, created.RemoteRepositoryConfig)
	require.NotNil(t, created.RemoteRepositoryConfig.DockerRepository)
	require.Equal(t, "DOCKER_HUB", created.RemoteRepositoryConfig.DockerRepository.PublicRepository)

	imageName := "test-project/docker-hub/sockerless-eval-arithmetic"
	manifestURL := baseURL + "/v2/" + imageName + "/manifests/test"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	require.NoError(t, json.Unmarshal(body, &manifest))
	require.NotEmpty(t, manifest.Config.Digest)
	require.NotEmpty(t, manifest.Layers)

	blobURL := baseURL + "/v2/" + imageName + "/blobs/" + manifest.Config.Digest
	blobResp, err := http.Get(blobURL)
	require.NoError(t, err)
	defer blobResp.Body.Close()
	require.Equal(t, http.StatusOK, blobResp.StatusCode)

	var cfg struct {
		Config struct {
			Entrypoint []string `json:"Entrypoint"`
		} `json:"config"`
	}
	require.NoError(t, json.NewDecoder(blobResp.Body).Decode(&cfg))
	require.Equal(t, []string{"/usr/local/bin/eval-arithmetic"}, cfg.Config.Entrypoint)

	images, err := service.Projects.Locations.Repositories.DockerImages.List(parent + "/repositories/docker-hub").Do()
	require.NoError(t, err)
	require.Len(t, images.DockerImages, 1)
	require.Contains(t, images.DockerImages[0].Name, "projects/test-project/locations/us-central1/repositories/docker-hub/dockerImages/sockerless-eval-arithmetic@sha256:")
	require.Equal(t, []string{"test"}, images.DockerImages[0].Tags)
}
