package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// cloudBuildServiceAccount is the identity a build runs as: the service
// account the build names, otherwise the project's Cloud Build service
// account, PROJECT_NUMBER@cloudbuild.gserviceaccount.com.
func cloudBuildServiceAccount(b Build) string {
	if sa := b.ServiceAccount; sa != "" {
		// A build names it as projects/{project}/serviceAccounts/{email}.
		if i := strings.LastIndex(sa, "/serviceAccounts/"); i >= 0 {
			return sa[i+len("/serviceAccounts/"):]
		}
		return sa
	}
	return projectNumber(b.ProjectID) + "@cloudbuild.gserviceaccount.com"
}

// cloudBuildDockerConfig writes the Docker client configuration a build's
// docker steps run with: a credential helper that answers Artifact Registry
// and Container Registry — by their hosts, or by this simulator's own port,
// at which a coordinate that relocates the registry reaches it — with an
// access token of the build's service account as the `oauth2accesstoken`
// password, and hands any other registry to the helper the host
// configured, which the step otherwise reaches anonymously. Real Cloud
// Build's docker builder carries the same credential through the gcloud
// helper. The directory is DOCKER_CONFIG for the steps; the caller removes
// it after the build.
func cloudBuildDockerConfig(b Build) (string, error) {
	now := time.Now()
	token := signAccessToken(cloudBuildServiceAccount(b), now, now.Add(time.Hour))
	// The registry hosts imageOnGoogleRegistry names.
	patterns := []string{"*-docker.pkg.dev", "gcr.io", "*.gcr.io"}
	if port, err := hostMetadataPort(); err == nil {
		patterns = append(patterns, fmt.Sprintf("*:%d", port))
	}
	return sim.WriteDockerConfig(patterns, sim.DockerCredential{Username: arUserAccessToken, Secret: token})
}
