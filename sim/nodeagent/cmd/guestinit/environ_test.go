package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A workload's HOME comes from its user's passwd entry when its image set
// none, as a container runtime gives it: a build cache or a config file the
// workload keeps under HOME has somewhere to go.
func TestEnvironGivesTheWorkloadItsUsersHome(t *testing.T) {
	passwd := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(passwd, []byte("root:x:0:0:root:/root:/bin/sh\napp:x:1000:1000::/home/app:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ user, want string }{
		{"", "/root"}, {"0", "/root"}, {"root", "/root"}, {"app", "/home/app"}, {"1000:1000", "/home/app"}, {"1001", "/"},
	} {
		if got := homeDir(tc.user, passwd); got != tc.want {
			t.Errorf("homeDir(%q) = %q, want %q", tc.user, got, tc.want)
		}
	}
	env := environ(map[string]string{"HOME": "/work", "GOFLAGS": "-mod=mod"}, "")
	if !slices.Contains(env, "HOME=/work") || slices.Contains(env, "HOME=/root") {
		t.Fatalf("an image's own HOME must stand: %v", env)
	}
	if env := environ(map[string]string{}, "root"); !slices.Contains(env, "HOME=/root") {
		t.Fatalf("a workload without HOME takes its user's: %v", env)
	}
}
