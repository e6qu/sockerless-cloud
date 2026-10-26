package gcp_cli_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArtifactRegistryDockerHubRemoteRepositoryCLIAndOCI(t *testing.T) {
	t.Parallel()
	out := runCLI(t, gcloudCLI("artifacts", "repositories", "create", "docker-hub",
		"--location="+location,
		"--repository-format=docker",
		"--mode=remote-repository",
		"--remote-docker-repo=docker-hub",
		"--disable-remote-validation",
		"--format=json",
	))

	jsonStart := strings.Index(out, "{")
	require.NotEqual(t, -1, jsonStart, "gcloud output did not contain JSON object: %s", out)
	var created struct {
		Mode                   string `json:"mode"`
		RemoteRepositoryConfig struct {
			DockerRepository struct {
				PublicRepository string `json:"publicRepository"`
			} `json:"dockerRepository"`
		} `json:"remoteRepositoryConfig"`
	}
	require.NoError(t, json.Unmarshal([]byte(out[jsonStart:]), &created), "gcloud output: %s", out)
	require.Equal(t, "REMOTE_REPOSITORY", created.Mode)
	require.Equal(t, "DOCKER_HUB", created.RemoteRepositoryConfig.DockerRepository.PublicRepository)

	// The remote repository hydrates from the local Docker daemon, so the pull
	// names the harness's own workload image.
	localRepo, localTag, _ := strings.Cut(evalImageName, ":")
	imageName := project + "/docker-hub/" + localRepo
	resp, err := httpDo(http.MethodGet, baseURL+"/v2/"+imageName+"/manifests/"+localTag, "")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out = runCLI(t, gcloudCLI("artifacts", "docker", "images", "list",
		location+"-docker.pkg.dev/"+project+"/docker-hub",
		"--include-tags",
		"--format=json",
	))

	jsonStart = strings.Index(out, "[")
	require.NotEqual(t, -1, jsonStart, "gcloud output did not contain JSON array: %s", out)
	var images []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Tags []string `json:"tags"`
	}
	require.NoError(t, json.Unmarshal([]byte(out[jsonStart:]), &images), "gcloud output: %s", out)
	require.Len(t, images, 1)
	require.Contains(t, images[0].Metadata.Name,
		"projects/test-project/locations/us-central1/repositories/docker-hub/dockerImages/"+localRepo+"@sha256:")
	require.Equal(t, []string{localTag}, images[0].Tags)
}
