package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A guest ID of the longest length the agent admits still leaves Firecracker
// a socket path it can bind under the default state directory, and a state
// directory too deep for that is refused at start.
func TestGuestSocketsFitUnderTheDefaultStateDirectory(t *testing.T) {
	id := strings.Repeat("a", 64)
	if !validID(id) {
		t.Fatalf("a %d-character ID is refused", len(id))
	}
	socket := filepath.Join(guestDir("/var/lib/sockerless", id), longestSocketName)
	if len(socket) > maxSocketPath {
		t.Fatalf("socket path %s is %d bytes, over %d", socket, len(socket), maxSocketPath)
	}
	if guestDir("/s", "a") == guestDir("/s", "b") {
		t.Fatal("two guests share a directory")
	}
	if _, err := newAgent(config{stateDir: "/" + strings.Repeat("d", 80)}); err == nil {
		t.Fatal("a state directory that leaves no room for the sockets was accepted")
	}
}

// waitForSocket returns Firecracker's own output as soon as the process ends
// without opening its socket, rather than waiting out its timeout.
func TestWaitForSocketReportsAnEndedProcess(t *testing.T) {
	dir := t.TempDir()
	console := filepath.Join(dir, "console.log")
	if err := os.WriteFile(console, []byte("Failed to bind the API socket\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	close(ended)
	began := time.Now()
	err := waitForSocket(context.Background(), filepath.Join(dir, "firecracker.socket"), time.Minute, ended, console)
	if err == nil || !strings.Contains(err.Error(), "Failed to bind the API socket") {
		t.Fatalf("err = %v, want Firecracker's output", err)
	}
	if waited := time.Since(began); waited > time.Second {
		t.Fatalf("waited %s after the process ended", waited)
	}
}

// A socket Firecracker has bound but not yet listens on refuses connections,
// and waitForSocket does not report it open until it listens.
func TestWaitForSocketWaitsForTheListener(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "firecracker.socket")
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	if err := waitForSocket(context.Background(), path, 100*time.Millisecond, nil, ""); err == nil {
		t.Fatal("waitForSocket reported a socket that does not listen as open")
	}
	if err := syscall.Listen(fd, 1); err != nil {
		t.Fatal(err)
	}
	if err := waitForSocket(context.Background(), path, time.Minute, nil, ""); err != nil {
		t.Fatal(err)
	}
}
