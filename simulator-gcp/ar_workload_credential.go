package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// cloudRunServiceAgent is the Google-managed identity Cloud Run pulls
// container images with — `service-PROJECT_NUMBER@serverless-robot-prod.iam.
// gserviceaccount.com` — for Cloud Run jobs and services and for Cloud
// Functions (2nd gen), which Cloud Run serves.
func cloudRunServiceAgent(project string) string {
	return fmt.Sprintf("service-%s@serverless-robot-prod.iam.gserviceaccount.com", projectNumber(project))
}

// workloadRegistryAuth is the credential a Google Cloud workload host presents
// when it pulls image for project: an OAuth 2.0 access token of the project's
// Cloud Run service agent, as the `oauth2accesstoken` password of a Basic
// credential, for an image on Artifact Registry or Container Registry — and
// nothing for any other registry, which the host reaches anonymously.
func workloadRegistryAuth(project, image string) string {
	if !imageOnGoogleRegistry(image) {
		return ""
	}
	now := time.Now()
	return sim.RegistryCredential(arUserAccessToken, signAccessToken(cloudRunServiceAgent(project), now, now.Add(time.Hour)))
}

// imageOnGoogleRegistry reports whether an image reference names Artifact
// Registry or Container Registry: by their hosts, or by this simulator's own
// port, at which a coordinate that relocates the registry reaches it.
func imageOnGoogleRegistry(image string) bool {
	host, _, _ := strings.Cut(image, "/")
	if !strings.Contains(image, "/") {
		return false
	}
	hostname := host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		hostname = host[:i]
		if port, err := hostMetadataPort(); err == nil && host[i+1:] == fmt.Sprint(port) {
			return true
		}
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
