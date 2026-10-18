package main

import (
	"os"
	"path/filepath"
	"testing"
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
