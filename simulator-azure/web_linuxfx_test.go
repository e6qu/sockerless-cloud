package main

import "testing"

// "DOCKER|" and its siblings name a container image; a built-in stack such as
// "PHP|8.2" names a version of a platform image App Service supplies, never an
// image called "8.2".
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

	// A stack's version is never mistaken for an image name.
	if got := siteContainerImage(site("PHP|8.2")); got == "8.2" {
		t.Fatal(`"PHP|8.2" resolved to the image "8.2"; a stack version is not an image`)
	}
}

// A web app whose linuxFxVersion names a catalogued stack, in any case, runs
// the stack's image; a function app runs the Functions host, and an
// uncatalogued stack has no image.
func TestSiteBuiltInStackRunsOnlyWebAppsOnCataloguedStacks(t *testing.T) {
	site := func(kind, fx string) *Site {
		return &Site{Kind: kind, Properties: SiteProperties{SiteConfig: &SiteConfig{LinuxFxVersion: fx}}}
	}
	for _, tc := range []struct {
		kind, fx string
		image    string
	}{
		{kind: "app,linux", fx: "NODE|20-lts", image: "mcr.microsoft.com/appsvc/node:20-lts_20260904.5.tuxprod"},
		{kind: "app,linux", fx: "node|22-LTS", image: "mcr.microsoft.com/appsvc/node:22-lts_20260904.5.tuxprod"},
		{kind: "app,linux", fx: "PYTHON|3.12", image: "mcr.microsoft.com/appsvc/python:3.12_20260910.5.tuxprod"},
		{kind: "app,linux", fx: "PHP|8.2"},
		{kind: "app,linux", fx: "DOCKER|nginx:1.27"},
		{kind: "app,linux", fx: ""},
		{kind: "functionapp,linux", fx: "NODE|20-lts"},
	} {
		s := site(tc.kind, tc.fx)
		stack, ok := siteBuiltInStack(s)
		if got := stack.Image; got != tc.image || ok != (tc.image != "") {
			t.Errorf("siteBuiltInStack(%s, %q) = %q, %v; want %q", tc.kind, tc.fx, got, ok, tc.image)
		}
	}
}

// A web app runs its stack's platform image; a function app runs its runtime's
// Functions host image.
func TestSitePlatformImageRunsFunctionAppsOnTheFunctionsHost(t *testing.T) {
	site := func(kind, fx string) *Site {
		return &Site{Kind: kind, Properties: SiteProperties{SiteConfig: &SiteConfig{LinuxFxVersion: fx}}}
	}
	for _, tc := range []struct {
		kind, fx string
		image    string
	}{
		{kind: "functionapp,linux", fx: "Node|22", image: "mcr.microsoft.com/azure-functions/node:4.1054.250-4-node22-appservice"},
		{kind: "functionapp,linux", fx: "NODE|22", image: "mcr.microsoft.com/azure-functions/node:4.1054.250-4-node22-appservice"},
		{kind: "app,linux", fx: "NODE|20-lts", image: "mcr.microsoft.com/appsvc/node:20-lts_20260904.5.tuxprod"},
		{kind: "app,linux", fx: "Node|22"},
		{kind: "functionapp,linux", fx: "NODE|20-lts"},
		{kind: "functionapp,linux", fx: "Python|3.12"},
		{kind: "functionapp,linux", fx: ""},
	} {
		image, ok := sitePlatformImage(site(tc.kind, tc.fx))
		if image != tc.image || ok != (tc.image != "") {
			t.Errorf("sitePlatformImage(%s, %q) = %q, %v; want %q", tc.kind, tc.fx, image, ok, tc.image)
		}
	}
}
