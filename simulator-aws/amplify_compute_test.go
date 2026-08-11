package main

import (
	"testing"
)

func TestAmplifyParseDeployManifest(t *testing.T) {
	manifest, err := amplifyParseDeployManifest([]byte(`{
		"version": 1,
		"framework": {"name": "express", "version": "4.18.2"},
		"routes": [
			{"path": "/*.*", "target": {"kind": "Static"}, "fallback": {"kind": "Compute", "src": "default"}},
			{"path": "/*", "target": {"kind": "Compute", "src": "default"}}
		],
		"computeResources": [{"name": "default", "runtime": "nodejs18.x", "entrypoint": "index.js"}]
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if manifest.Version != 1 || manifest.Framework.Name != "express" {
		t.Fatalf("manifest header: %+v", manifest)
	}
	if len(manifest.Routes) != 2 || manifest.Routes[0].Fallback == nil || manifest.Routes[0].Fallback.Kind != "Compute" {
		t.Fatalf("routes: %+v", manifest.Routes)
	}
	compute, ok := manifest.computeResource("default")
	if !ok || compute.Runtime != "nodejs18.x" || compute.Entrypoint != "index.js" {
		t.Fatalf("compute resource: %+v %v", compute, ok)
	}
	// An empty src picks the first compute resource.
	if _, ok := manifest.computeResource(""); !ok {
		t.Fatal("empty src must resolve to the first compute resource")
	}
	if _, ok := manifest.computeResource("nope"); ok {
		t.Fatal("unknown src must not resolve")
	}

	for name, text := range map[string]string{
		"bad json":        `{`,
		"wrong version":   `{"version": 2, "routes": [{"path": "/*", "target": {"kind": "Compute"}}]}`,
		"no routes":       `{"version": 1, "routes": []}`,
		"missing routes":  `{"version": 1}`,
		"version omitted": `{"routes": [{"path": "/*", "target": {"kind": "Static"}}]}`,
	} {
		if _, err := amplifyParseDeployManifest([]byte(text)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestAmplifyManifestRouteMatch(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"/*", "/", true},
		{"/*", "/anything/deep", true},
		{"/*.*", "/style.css", true},
		{"/*.*", "/assets/app.js", true},
		{"/*.*", "/api/users", false},
		{"/api/*", "/api/users", true},
		{"/api/*", "/apiusers", false},
		{"/_amplify/image", "/_amplify/image", true},
		{"/_amplify/image", "/_amplify/images", false},
	}
	for _, tc := range cases {
		if got := amplifyCompileRoutePattern(tc.pattern).MatchString(tc.path); got != tc.want {
			t.Fatalf("match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestAmplifyComputeRuntimeImage(t *testing.T) {
	image, err := amplifyComputeRuntimeImage("nodejs18.x")
	if err != nil || image != "public.ecr.aws/docker/library/node:18-alpine" {
		t.Fatalf("nodejs18.x: %q %v", image, err)
	}
	image, err = amplifyComputeRuntimeImage("nodejs20.x")
	if err != nil || image != "public.ecr.aws/docker/library/node:20-alpine" {
		t.Fatalf("nodejs20.x: %q %v", image, err)
	}
	for _, bad := range []string{"", "python3.12", "nodejs", "nodejsx.x"} {
		if _, err := amplifyComputeRuntimeImage(bad); err == nil {
			t.Fatalf("%q: expected error", bad)
		}
	}
}
