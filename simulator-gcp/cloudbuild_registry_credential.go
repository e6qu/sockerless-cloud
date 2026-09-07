package main

import (
	"fmt"
	"os"
	"path/filepath"
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
func cloudBuildDockerConfig(b Build, workDir string) (string, error) {
	now := time.Now()
	token := signAccessToken(cloudBuildServiceAccount(b), now, now.Add(time.Hour))
	// The registry hosts imageOnGoogleRegistry names.
	patterns := []string{"*-docker.pkg.dev", "gcr.io", "*.gcr.io"}
	if port, err := hostMetadataPort(); err == nil {
		patterns = append(patterns, fmt.Sprintf("*:%d", port))
	}
	return sim.WriteDockerConfig(sim.DockerConfigSpec{
		HostPatterns: patterns,
		Hosts:        cloudBuildRegistryHosts(b, workDir),
		Credential:   sim.DockerCredential{Username: arUserAccessToken, Secret: token},
	})
}

// cloudBuildRegistryHosts names the Google registries a build reaches
// outright: the hosts of the images its steps tag and the build lists, and
// of the base images the Dockerfiles in its source name — what a client
// that enumerates configured registries before it starts must find.
func cloudBuildRegistryHosts(b Build, workDir string) []string {
	seen := map[string]bool{}
	var hosts []string
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || !imageOnGoogleRegistry(ref) {
			return
		}
		host, _, _ := strings.Cut(ref, "/")
		if !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	for _, image := range b.Images {
		add(image)
	}
	dockerfiles := map[string]bool{"Dockerfile": true}
	for _, step := range b.Steps {
		if step == nil {
			continue
		}
		for i, arg := range step.Args {
			if (arg == "-t" || arg == "--tag") && i+1 < len(step.Args) {
				add(step.Args[i+1])
			}
			if (arg == "-f" || arg == "--file") && i+1 < len(step.Args) {
				dockerfiles[filepath.Join(step.Dir, step.Args[i+1])] = true
			}
		}
	}
	for name := range dockerfiles {
		data, err := os.ReadFile(filepath.Join(workDir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
				continue
			}
			ref := fields[1]
			if strings.HasPrefix(ref, "--platform=") && len(fields) > 2 {
				ref = fields[2]
			}
			add(ref)
		}
	}
	return hosts
}
