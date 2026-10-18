package main

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A member's program resolves on PATH inside the member's own root, where the
// kernel will find it once the process has entered that root, never on the
// guest's.
func TestLookPathResolvesInsideTheRoot(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"bin", "usr/local/bin"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "usr/local/bin/serve"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin/notes"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := "/usr/local/bin:/bin"
	if got, err := lookPath("serve", path, root); err != nil || got != "/usr/local/bin/serve" {
		t.Fatalf("lookPath(serve) = %q, %v", got, err)
	}
	if got, err := lookPath("/usr/local/bin/serve", path, root); err != nil || got != "/usr/local/bin/serve" {
		t.Fatalf("lookPath(absolute) = %q, %v", got, err)
	}
	if _, err := lookPath("notes", path, root); err == nil {
		t.Fatal("a file without the execute bit resolved")
	}
	if _, err := lookPath("sh", path, root); err == nil {
		t.Fatal("the guest's own /bin/sh resolved inside a root that has none")
	}
}

func TestUeventNamesTheDevice(t *testing.T) {
	message := []byte("change@/devices/virtio-mmio-cmdline/virtio-mmio.1/virtio1/block/vdb\x00ACTION=change\x00DEVNAME=vdb\x00SUBSYSTEM=block\x00")
	if !ueventNames(message, "vdb") {
		t.Fatal("the event names vdb")
	}
	if ueventNames(message, "vdc") {
		t.Fatal("the event does not name vdc")
	}
}

// A process's exit ends its tree: a child it left behind holding its output
// would otherwise keep the process from being reported as exited.
func TestProcessExitEndsTheChildrenItLeftBehind(t *testing.T) {
	children := make(chan os.Signal, 1)
	signal.Notify(children, syscall.SIGCHLD)
	defer signal.Stop(children)
	go exits.reap(children)
	p := newProcess("", "", processSpec{Command: []string{"sh", "-c", "sleep 60 & echo started; exit 3"}}, newLogBook())
	if err := p.start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the process is not reported as exited while its orphan holds its output")
	}
	if p.result.code != 3 {
		t.Fatalf("exit code %d, want 3", p.result.code)
	}
}
