package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestImageOnGoogleRegistry(t *testing.T) {
	previous := simListenAddr
	simListenAddr = ":4567"
	t.Cleanup(func() { simListenAddr = previous })

	on := []string{
		"us-central1-docker.pkg.dev/p/docker-hub/library/alpine:3.20",
		"europe-docker.pkg.dev/p/repo/app@sha256:0000",
		"gcr.io/p/app:1",
		"eu.gcr.io/p/app:1",
		"127.0.0.1:4567/p/sockerless-overlay/cloudrun:tag",
		"host.docker.internal:4567/p/docker-hub/library/alpine:latest",
	}
	for _, image := range on {
		if !imageOnGoogleRegistry(image) {
			t.Errorf("%s: not recognised as a Google registry image", image)
		}
	}
	off := []string{
		"alpine:3.20",
		"public.ecr.aws/docker/library/alpine:3.20",
		"ghcr.io/owner/app:1",
		"127.0.0.1:5000/p/repo/app:1",
		"docker.pkg.dev.example.com/p/app:1",
	}
	for _, image := range off {
		if imageOnGoogleRegistry(image) {
			t.Errorf("%s: recognised as a Google registry image", image)
		}
	}
}

func TestWorkloadRegistryAuthIsTheServiceAgentsAccessToken(t *testing.T) {
	previous := simListenAddr
	simListenAddr = ":4567"
	t.Cleanup(func() { simListenAddr = previous })
	arTestServer(t)

	if got := workloadRegistryAuth("p", "alpine:3.20"); got != "" {
		t.Errorf("a Docker Hub image is pulled anonymously, got %q", got)
	}
	credential := workloadRegistryAuth("my-project", "us-central1-docker.pkg.dev/my-project/repo/app:1")
	raw, err := base64.URLEncoding.DecodeString(credential)
	if err != nil {
		t.Fatalf("credential is not the engine's base64: %v", err)
	}
	var auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Fatalf("credential is not an AuthConfig: %v", err)
	}
	if auth.Username != arUserAccessToken {
		t.Errorf("username = %q, want %q", auth.Username, arUserAccessToken)
	}
	principal, err := arPrincipalFromAccessToken(auth.Password)
	if err != nil {
		t.Fatalf("the registry does not accept the token: %v", err)
	}
	want := "service-" + projectNumber("my-project") + "@serverless-robot-prod.iam.gserviceaccount.com"
	if principal.subject != want {
		t.Errorf("token subject = %q, want the Cloud Run service agent %q", principal.subject, want)
	}
	if !strings.HasSuffix(cloudRunServiceAgent("my-project"), "@serverless-robot-prod.iam.gserviceaccount.com") {
		t.Errorf("service agent = %q", cloudRunServiceAgent("my-project"))
	}
}

func TestResourceProject(t *testing.T) {
	cases := map[string]string{
		"projects/p1/locations/us-central1/jobs/j/executions/e": "p1",
		"projects/p2/locations/europe-west1/services/s":         "p2",
		"projects/p3":                  "p3",
		"locations/us-central1/jobs/j": "",
	}
	for name, want := range cases {
		if got := resourceProject(name); got != want {
			t.Errorf("resourceProject(%q) = %q, want %q", name, got, want)
		}
	}
}
