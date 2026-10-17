package sim

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// A seeded file is in the container's filesystem when its first process runs,
// with the mode asked for, and the container carries the hostname given.
func TestSeedFilesAndHostnameReachTheContainerBeforeItStarts(t *testing.T) {
	InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := PullImage(t.Context(), publishTestImage, platform); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	handle, err := StartContainerSyncContext(t.Context(), ContainerConfig{
		Image:        publishTestImage,
		Architecture: platform,
		Command:      []string{"/sbin/sockerless-init"},
		Hostname:     "ip-10-0-0-7",
		Seed: []SeedFile{
			{Path: "/sbin/sockerless-init", Mode: 0o755, Data: []byte("#!/bin/sh\ncat /var/lib/cloud/seed/user-data; hostname\n")},
			{Path: "/var/lib/cloud/seed/user-data", Data: []byte("#!/bin/sh\necho hello from user data\n")},
		},
		Name:    "sockerless-sim-seed-test-" + time.Now().Format("150405.000000"),
		Timeout: time.Minute,
		Sandbox: SandboxProfile{CapDrop: []string{"ALL"}, NoNewPrivileges: true},
	}, FuncSink(func(line LogLine) {
		mu.Lock()
		lines = append(lines, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	result := handle.Wait()
	mu.Lock()
	out := strings.Join(lines, "\n")
	mu.Unlock()
	if result.ExitCode != 0 || !strings.Contains(out, "hello from user data") || !strings.Contains(out, "ip-10-0-0-7") {
		t.Fatalf("exit %d, output:\n%s", result.ExitCode, out)
	}
}
