package main

import "testing"

// TestSiteContainerImageDistinguishesAStackFromAnImage pins the difference
// linuxFxVersion's prefix makes.
//
// The field names two different things. "DOCKER|" and its siblings name a
// container image; a built-in runtime stack names a version of a platform
// image App Service supplies. Reading whatever follows the bar as an image
// treated the second as the first, so a site configured the ordinary way —
// "PHP|8.2" — became an attempt to pull an image called "8.2", and the failure
// read as a missing image rather than as a stack this simulator does not run.
func TestSiteContainerImageDistinguishesAStackFromAnImage(t *testing.T) {
	site := func(fx string) *Site {
		return &Site{Properties: SiteProperties{SiteConfig: &SiteConfig{LinuxFxVersion: fx}}}
	}

	for _, tc := range []struct {
		fx    string
		image string
		stack string
	}{
		{fx: "DOCKER|nginx:1.27", image: "nginx:1.27"},
		{fx: "docker|ghcr.io/acme/api@sha256:abc", image: "ghcr.io/acme/api@sha256:abc"},
		{fx: "COMPOSE|encoded", image: "encoded"},
		{fx: "PHP|8.2", stack: "PHP|8.2"},
		{fx: "NODE|20-lts", stack: "NODE|20-lts"},
		{fx: "DOTNETCORE|8.0", stack: "DOTNETCORE|8.0"},
		{fx: "TOMCAT|10.0-java17", stack: "TOMCAT|10.0-java17"},
		{fx: "", image: "", stack: ""},
	} {
		if got := siteContainerImage(site(tc.fx)); got != tc.image {
			t.Errorf("siteContainerImage(%q) = %q, want %q", tc.fx, got, tc.image)
		}
		if got := siteRuntimeStack(site(tc.fx)); got != tc.stack {
			t.Errorf("siteRuntimeStack(%q) = %q, want %q", tc.fx, got, tc.stack)
		}
	}

	// The specific regression: a stack's version is never mistaken for an
	// image name.
	if got := siteContainerImage(site("PHP|8.2")); got == "8.2" {
		t.Fatal(`"PHP|8.2" resolved to the image "8.2"; a stack version is not an image`)
	}
}
