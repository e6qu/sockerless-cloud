package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// cloudRunServiceAgent is the Google-managed identity Cloud Run pulls
// container images with — `service-PROJECT_NUMBER@serverless-robot-prod.iam.
// gserviceaccount.com` — for Cloud Run jobs and services and for Cloud
// Functions (2nd gen), which Cloud Run serves, named for the number Cloud
// Resource Manager holds.
func cloudRunServiceAgent(project string) (string, error) {
	number, ok := crmProjectNumber(project)
	if !ok {
		return "", fmt.Errorf("project %s is not an active project, so it has no Cloud Run service agent", project)
	}
	return "service-" + number + "@serverless-robot-prod.iam.gserviceaccount.com", nil
}

// workloadRegistryAuth is the credential a Google Cloud workload host presents
// when it pulls image for project: an OAuth 2.0 access token of the project's
// Cloud Run service agent, as the `oauth2accesstoken` password of a Basic
// credential, for an image on Artifact Registry or Container Registry — and
// nothing for any other registry, which the host reaches anonymously.
func workloadRegistryAuth(project, image string) (string, error) {
	if !imageOnGoogleRegistry(image) {
		return "", nil
	}
	agent, err := cloudRunServiceAgent(project)
	if err != nil {
		return "", err
	}
	now := time.Now()
	return sim.RegistryCredential(arUserAccessToken, signAccessToken(agent, now, now.Add(time.Hour))), nil
}

// imageOnGoogleRegistry reports whether an image reference names Artifact
// Registry or Container Registry: by their hosts, or by this simulator's own
// port, at which a coordinate that relocates the registry reaches it.
func imageOnGoogleRegistry(image string) bool {
	host, _, found := strings.Cut(image, "/")
	if !found {
		return false
	}
	if port, err := workloadhost.ListenPort(simListenAddr); err == nil && sim.ImageOnPort(image, port) {
		return true
	}
	hostname := host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		hostname = host[:i]
	}
	return strings.HasSuffix(hostname, "-docker.pkg.dev") || hostname == "gcr.io" || strings.HasSuffix(hostname, ".gcr.io")
}

// resourceProject returns the project a `projects/{project}/…` resource name
// belongs to.
func resourceProject(name string) string {
	rest, ok := strings.CutPrefix(name, "projects/")
	if !ok {
		return ""
	}
	project, _, _ := strings.Cut(rest, "/")
	return project
}
