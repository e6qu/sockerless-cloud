package main

import (
	"path/filepath"
	"testing"
)

func TestCloudBuildStepDirStaysInsideTheWorkspace(t *testing.T) {
	work := t.TempDir()
	for dir, want := range map[string]string{
		"":                   work,
		"app":                filepath.Join(work, "app"),
		"app/./sub":          filepath.Join(work, "app", "sub"),
		"/workspace":         work,
		"/workspace/":        work,
		"/workspace/app/sub": filepath.Join(work, "app", "sub"),
	} {
		got, err := cloudBuildStepDir(work, dir)
		if err != nil || got != want {
			t.Errorf("dir %q: %q, %v; want %q", dir, got, err, want)
		}
	}
	for _, dir := range []string{"..", "../outside", "app/../../outside", "/etc", "/workspace2", "/workspace/../etc", "/"} {
		if got, err := cloudBuildStepDir(work, dir); err == nil {
			t.Errorf("dir %q resolved to %q on the host outside the workspace", dir, got)
		}
	}
}
