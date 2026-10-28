package vminstance

import (
	"context"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

const testImage = "mirror.gcr.io/library/busybox:latest"

// cloudInitImage is an image whose distribution's cloud images carry
// cloud-init.
const cloudInitImage = "mirror.gcr.io/library/ubuntu:24.04@sha256:24737cef17f4e22b1977df7d9a05e77c4e28b7da5b8911f8d8ccda136bf3573c"

type lineSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *lineSink) WriteLog(line sim.LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line.Text)
}

func (s *lineSink) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}

func TestInstanceRunsItsUserDataOnTheConsoleAndKeepsItsDiskAcrossAStop(t *testing.T) {
	if _, err := sim.InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(ctx, cloudInitImage, platform); err != nil {
		t.Fatal(err)
	}
	network := "sockerless-sim-test-instances-" + sim.RandomHex(4)
	netID, cidr, err := sim.EnsurePlatformNetwork(ctx, network, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sim.DockerClient().NetworkRemove(ctx, netID, client.NetworkRemoveOptions{}) })
	address := cidr.Masked().Addr()
	for range 10 {
		address = address.Next()
	}
	backend := placement.ContainerBackend{}
	sink := &lineSink{}
	spec := Spec{
		Name:      "sockerless-sim-test-instance-" + sim.RandomHex(4),
		ID:        "i-test",
		Hostname:  "ip-10-0-0-7",
		Image:     Image{OCI: cloudInitImage, Architecture: platform},
		Network:   network,
		Address:   address.String(),
		DNS:       []string{netip.MustParseAddr("100.64.0.2").String()},
		UserData:  []byte("#!/bin/sh\necho \"user data ran on $(hostname)\"\necho boot >> /var/boots\nsleep 2147483647 &\n"),
		CloudInit: &CloudInit{Platform: "ec2", Datasource: "DataSourceEc2Local", InstanceID: "i-test"},
		Env:       map[string]string{"AWS_EC2_METADATA_SERVICE_ENDPOINT": "http://100.64.0.2/"},
		VCPUs:     1,
		MemoryMiB: 64,
		StopGrace: time.Second,
	}
	handle, err := Launch(ctx, backend, spec, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		handle.Cancel()
		_ = backend.Remove(ctx, handle.ID())
	})
	waitReady(t, ctx, handle, sink)
	if !strings.Contains(sink.text(), "user data ran on ip-10-0-0-7") {
		t.Fatalf("the console does not carry the user data's output:\n%s", sink.text())
	}
	logged, err := handle.Exec(ctx, []string{"cat", OutputLogPath})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.Stdout, "user data ran on ip-10-0-0-7") ||
		!strings.Contains(logged.Stdout, "Cloud-init v. 24.1.3-0ubuntu3 finished at ") ||
		!strings.Contains(logged.Stdout, "Datasource DataSourceEc2Local.") {
		t.Fatalf("cloud-init's output log does not carry the user data's output and the final message: %q", logged.Stdout)
	}
	handle.Cancel()
	<-handle.Exited()
	again, err := backend.Restart(ctx, handle.ID(), mustWorkload(t, spec), sink)
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, again, sink)
	boots, err := again.Exec(ctx, []string{"cat", "/var/boots"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(boots.Stdout, "boot") != 1 {
		t.Fatalf("a restarted instance keeps its disk and runs its user data once per instance: /var/boots = %q", boots.Stdout)
	}
	marker, err := again.Exec(ctx, []string{"test", "-e", OnceMarkerPath})
	if err != nil || marker.ExitCode != 0 {
		t.Fatalf("the once marker is missing after the first run: %v %+v", err, marker)
	}
}

func TestInstanceRunsItsStartupScriptAtEveryBoot(t *testing.T) {
	if _, err := sim.InitDocker("gcp", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(ctx, testImage, platform); err != nil {
		t.Fatal(err)
	}
	network := "sockerless-sim-test-instances-" + sim.RandomHex(4)
	netID, cidr, err := sim.EnsurePlatformNetwork(ctx, network, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sim.DockerClient().NetworkRemove(ctx, netID, client.NetworkRemoveOptions{}) })
	backend := placement.ContainerBackend{}
	sink := &lineSink{}
	spec := Spec{
		Name:      "sockerless-sim-test-instance-" + sim.RandomHex(4),
		ID:        "every-boot",
		Hostname:  "instance-1",
		Image:     Image{OCI: testImage, Architecture: platform},
		Network:   network,
		Address:   cidr.Masked().Addr().Next().Next().Next().Next().String(),
		Startup:   &Startup{Path: "/var/lib/google-guest-agent/startup-script", Script: []byte("#!/bin/sh\necho boot >> /var/boots\n")},
		VCPUs:     1,
		MemoryMiB: 64,
		StopGrace: time.Second,
	}
	handle, err := Launch(ctx, backend, spec, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		handle.Cancel()
		_ = backend.Remove(ctx, handle.ID())
	})
	waitReady(t, ctx, handle, sink)
	handle.Cancel()
	<-handle.Exited()
	again, err := backend.Restart(ctx, handle.ID(), mustWorkload(t, spec), sink)
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, again, sink)
	boots, err := again.Exec(ctx, []string{"cat", "/var/boots"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(boots.Stdout, "boot") != 2 {
		t.Fatalf("a startup script runs at every boot: /var/boots = %q", boots.Stdout)
	}
}

func mustWorkload(t *testing.T, spec Spec) placement.Workload {
	t.Helper()
	w, err := Workload(spec)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func waitReady(t *testing.T, ctx context.Context, handle placement.Handle, sink *lineSink) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := handle.Exec(ctx, []string{"test", "-e", "/run/sockerless-instance-ready"})
		if err == nil && res.ExitCode == 0 {
			return
		}
		if time.Now().After(deadline) {
			ps, _ := handle.Exec(ctx, []string{"sh", "-c", "ps; cat /var/log/cloud-init-output.log; ls -la /sbin/sockerless-instance-init /var/lib/cloud/instance"})
			t.Fatalf("the instance's init never reported ready: %v %+v\nconsole:\n%s\ninside:\n%s%s", err, res, sink.text(), ps.Stdout, ps.Stderr)
		}
		select {
		case <-handle.Exited():
			t.Fatal("the instance exited before its init reported ready")
		case <-time.After(200 * time.Millisecond):
		}
	}
}
