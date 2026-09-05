package sim

import (
	"errors"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// testSandbox is a profile with every restriction on, so the deny rules and
// the settings Apply writes are exercised without naming any cloud's profile.
var testSandbox = SandboxProfile{
	ReadonlyRootfs:   true,
	User:             "1051:1051",
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"NET_BIND_SERVICE"},
	NoNewPrivileges:  true,
	TmpfsSize:        "size=512m",
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// permissiveUserSandbox leaves User empty, the way the profiles of platforms
// that let the image's USER win do.
var permissiveUserSandbox = SandboxProfile{
	CapDrop:          []string{"ALL"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

func TestSandboxApplies(t *testing.T) {
	hc := &container.HostConfig{}
	cc := &container.Config{}
	if err := testSandbox.Apply(hc, cc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if hc.Privileged {
		t.Error("Privileged must stay off")
	}
	if !hc.ReadonlyRootfs {
		t.Error("ReadonlyRootfs = false, want true")
	}
	if cc.User != "1051:1051" {
		t.Errorf("User = %q, want 1051:1051", cc.User)
	}
	if !slices.Contains(hc.CapDrop, "ALL") {
		t.Errorf("CapDrop = %v, want ALL", hc.CapDrop)
	}
	if !slices.Contains(hc.CapAdd, "NET_BIND_SERVICE") {
		t.Errorf("CapAdd = %v, want NET_BIND_SERVICE", hc.CapAdd)
	}
	if !slices.Contains(hc.SecurityOpt, "no-new-privileges") {
		t.Errorf("SecurityOpt = %v, want no-new-privileges", hc.SecurityOpt)
	}
	if got := hc.Tmpfs["/tmp"]; got != "size=512m" {
		t.Errorf("Tmpfs[/tmp] = %q, want size=512m", got)
	}
}

func TestSandboxDenyHostNetwork(t *testing.T) {
	hc := &container.HostConfig{NetworkMode: "host"}
	err := testSandbox.Apply(hc, &container.Config{})
	if err == nil {
		t.Fatal("Apply must reject NetworkMode=host")
	}
	if !errors.Is(err, errSandboxHostNet) {
		t.Errorf("err = %v, want errSandboxHostNet", err)
	}
}

func TestSandboxDenyDockerSocket(t *testing.T) {
	cases := []string{
		"/var/run/docker.sock:/var/run/docker.sock",
		"/var/run/docker.sock:/host/docker.sock:ro",
		"/run/docker.sock:/run/docker.sock",
	}
	for _, bind := range cases {
		hc := &container.HostConfig{Binds: []string{bind}}
		err := testSandbox.Apply(hc, &container.Config{})
		if err == nil {
			t.Errorf("Apply must reject bind %q", bind)
			continue
		}
		if !errors.Is(err, errSandboxDockerSock) {
			t.Errorf("bind %q: err = %v, want errSandboxDockerSock", bind, err)
		}
	}
}

// TestSandboxDenyDockerSocketBypasses guards the hardened bind matcher against
// path-traversal and parent-directory-mount bypasses a naive substring check
// would miss.
func TestSandboxDenyDockerSocketBypasses(t *testing.T) {
	deny := []string{
		"/var/run/../run/docker.sock:/x",        // traversal back to /run
		"/var/run/./docker.sock:/x",             // dot segment
		"/var//run//docker.sock:/x",             // duplicate slashes
		"/var/run:/host/var/run",                // parent-dir mount exposes socket inside
		"/run:/host/run",                        // parent-dir mount
		"/:/host",                               // whole-root mount
		"/run/podman/podman.sock:/run/p.sock",   // podman socket
		"/var/run/podman/../podman/podman.sock", // podman traversal
	}
	for _, bind := range deny {
		hc := &container.HostConfig{Binds: []string{bind}}
		if err := testSandbox.Apply(hc, &container.Config{}); !errors.Is(err, errSandboxDockerSock) {
			t.Errorf("bind %q: must be denied, got err=%v", bind, err)
		}
	}

	allow := []string{
		"/var/run2/docker.sock-not:/x", // /var/run2 is not /var/run
		"/home/user/data:/data",        // unrelated bind
		"myvolume:/data",               // named volume
		"/var/log:/var/log:ro",         // unrelated host dir
	}
	for _, bind := range allow {
		hc := &container.HostConfig{Binds: []string{bind}}
		if err := testSandbox.Apply(hc, &container.Config{}); errors.Is(err, errSandboxDockerSock) {
			t.Errorf("bind %q: must be allowed, got socket-deny error", bind)
		}
	}
}

func TestSandboxPreservesExistingUser(t *testing.T) {
	// If the caller already set User, the profile shouldn't override
	// (Fargate, Cloud Run, ACA all let the image's USER win).
	hc := &container.HostConfig{}
	cc := &container.Config{User: "appuser:appgroup"}
	if err := permissiveUserSandbox.Apply(hc, cc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cc.User != "appuser:appgroup" {
		t.Errorf("User overridden to %q, want preserved", cc.User)
	}
}
