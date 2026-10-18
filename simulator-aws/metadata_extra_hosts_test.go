package main

import (
	"testing"
)

func TestRewriteHostDockerInternalEnv(t *testing.T) {
	env := map[string]string{
		"AWS_ENDPOINT_URL": "http://host.docker.internal:4566",
		"UNCHANGED":        "http://example.test",
	}

	got := rewriteHostDockerInternalEnvWithGateway(env, "192.0.2.1")
	if got["UNCHANGED"] != "http://example.test" {
		t.Fatalf("UNCHANGED = %q", got["UNCHANGED"])
	}
	if got["AWS_ENDPOINT_URL"] != "http://192.0.2.1:4566" {
		t.Fatalf("AWS_ENDPOINT_URL = %q", got["AWS_ENDPOINT_URL"])
	}
	if env["AWS_ENDPOINT_URL"] != "http://host.docker.internal:4566" {
		t.Fatalf("input env was mutated: %q", env["AWS_ENDPOINT_URL"])
	}
}

func TestRewriteHostDockerInternalEnvLeavesNativeHostAlias(t *testing.T) {
	env := map[string]string{
		"AWS_ENDPOINT_URL": "http://host.docker.internal:4566",
	}

	got := rewriteHostDockerInternalEnvForRuntime(env, false, "10.0.0.1")

	if got["AWS_ENDPOINT_URL"] != env["AWS_ENDPOINT_URL"] {
		t.Fatalf("native-host endpoint = %q, want %q", got["AWS_ENDPOINT_URL"], env["AWS_ENDPOINT_URL"])
	}
}
