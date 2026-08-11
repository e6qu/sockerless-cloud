package realexec

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestShortPathNameKeepsFirecrackerSocketPathShort(t *testing.T) {
	id := "gcp-projects-test-project-zones-us-central1-a-instances-sdk-vm-1"
	name := shortPathName(id)
	if strings.Contains(name, "/") {
		t.Fatalf("short path name contains path separator: %q", name)
	}
	socketPath := filepath.Join("/tmp", "sockerless-firecracker", name+"-123456", "firecracker.socket")
	if len(socketPath) >= 100 {
		t.Fatalf("Firecracker socket path is too long: len=%d path=%s", len(socketPath), socketPath)
	}
}

func TestFirecrackerKernelAssetSelectionIgnoresSidecars(t *testing.T) {
	prefix := "firecracker-ci/v1.15/x86_64/"
	keys := []string{
		prefix + "debug/vmlinux-6.1.155",
		prefix + "debug/vmlinux-6.1.155.debug.gz",
		prefix + "vmlinux-5.10.245",
		prefix + "vmlinux-5.10.245.config",
		prefix + "vmlinux-6.1.155",
		prefix + "vmlinux-6.1.155.config",
	}
	var kernels []string
	for _, key := range keys {
		if isFirecrackerKernelAsset(prefix, key) {
			kernels = append(kernels, key)
		}
	}
	sort.Slice(kernels, func(i, j int) bool { return firecrackerAssetKeyLess(kernels[i], kernels[j]) })
	if len(kernels) == 0 {
		t.Fatal("no kernels selected")
	}
	if got, want := kernels[len(kernels)-1], prefix+"vmlinux-6.1.155"; got != want {
		t.Fatalf("selected kernel = %q, want %q", got, want)
	}
}

// The kernel image format Firecracker boots is architecture-specific: an
// uncompressed ELF vmlinux on x86_64, and the arm64 Linux Image — a PE file
// beginning "MZ" — on aarch64. The magic each host must accept is therefore
// the one its own Firecracker takes, and the other must be rejected: accepting
// both would let a kernel for the wrong architecture through, and accepting
// only ELF is what made every arm64 host report Firecracker's own published
// kernel as a corrupted download.
func TestVerifyKernelAcceptsThisArchitecturesImageFormat(t *testing.T) {
	elf := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01}
	pe := []byte{'M', 'Z', 0x40, 0xfa, 0x26, 0x99}

	valid, foreign := elf, pe
	if runtime.GOARCH == "arm64" {
		valid, foreign = pe, elf
	}

	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "vmlinux-6.1.155")
	if err := os.WriteFile(kernelPath, valid, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyELFKernel(kernelPath); err != nil {
		t.Fatalf("the kernel format this architecture boots was rejected: %v", err)
	}

	foreignPath := filepath.Join(dir, "vmlinux-foreign")
	if err := os.WriteFile(foreignPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyELFKernel(foreignPath); err == nil {
		t.Fatal("a kernel for another architecture was accepted")
	}

	configPath := filepath.Join(dir, "vmlinux-6.1.155.config")
	if err := os.WriteFile(configPath, []byte("CONFIG_X86_64=y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyELFKernel(configPath); err == nil {
		t.Fatal("config file accepted as Firecracker kernel")
	}
}

func TestEnsureRootFSInitLinksSystemdWhenKernelInitIsMissing(t *testing.T) {
	dir := t.TempDir()
	systemdPath := filepath.Join(dir, "usr", "lib", "systemd", "systemd")
	if err := os.MkdirAll(filepath.Dir(systemdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(systemdPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := ensureRootFSInit(dir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dir, "sbin", "init"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "/usr/lib/systemd/systemd" {
		t.Fatalf("/sbin/init target = %q, want /usr/lib/systemd/systemd", target)
	}
}

func TestEnsureRootFSInitFailsWithoutInitOrSystemd(t *testing.T) {
	if err := ensureRootFSInit(t.TempDir()); err == nil {
		t.Fatal("rootfs without init candidate or systemd was accepted")
	}
}

func TestCopyRootFSCopiesContentsIntoDestination(t *testing.T) {
	src := t.TempDir()
	systemdPath := filepath.Join(src, "usr", "lib", "systemd", "systemd")
	if err := os.MkdirAll(filepath.Dir(systemdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(systemdPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sbinDir := filepath.Join(src, "sbin")
	if err := os.MkdirAll(sbinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../usr/lib/systemd/systemd", filepath.Join(sbinDir, "init")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "rootfs")
	if err := copyRootFS(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dst, "sbin", "init"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "../usr/lib/systemd/systemd" {
		t.Fatalf("copied init target = %q, want ../usr/lib/systemd/systemd", target)
	}
	if _, err := os.Stat(filepath.Join(dst, "usr", "lib", "systemd", "systemd")); err != nil {
		t.Fatalf("copied rootfs missing systemd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, filepath.Base(src), "sbin", "init")); !os.IsNotExist(err) {
		t.Fatalf("copy nested the source directory under destination: %v", err)
	}
}

func TestConfigureRootFSNetworkInstallsBootConfigurator(t *testing.T) {
	dir := t.TempDir()
	netplanDir := filepath.Join(dir, "etc", "netplan")
	if err := os.MkdirAll(netplanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(netplanDir, "50-cloud-init.yaml"), []byte("network: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"etc/init.d/fcnet",
		"etc/network/interfaces.d/10-fcnet",
		"etc/systemd/system/fcnet.service",
		"lib/systemd/system/fcnet.service",
		"usr/local/bin/fcnet-setup.sh",
	} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("stock firecracker networking\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{
		"etc/rc2.d/S01fcnet",
		"etc/systemd/system/multi-user.target.wants/fcnet.service",
	} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../fcnet.service", path); err != nil {
			t.Fatal(err)
		}
	}
	if err := configureRootFSNetwork(dir, net.ParseIP("10.26.0.2"), net.ParseIP("10.26.0.1"), 24, []string{"metadata.google.internal", "metadata"}); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(dir, "usr", "local", "sbin", "sockerless-network")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(scriptPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("script mode = %o, want 755", info.Mode().Perm())
	}
	for _, want := range []string{
		"ip addr replace 10.26.0.2/24 dev \"$dev\"",
		"ip route replace default via 10.26.0.1 dev \"$dev\"",
		"printf 'nameserver 1.1.1.1\\n' > /etc/resolv.conf",
	} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("boot configurator missing %q:\n%s", want, script)
		}
	}

	servicePath := filepath.Join(dir, "etc", "systemd", "system", "sockerless-network.service")
	service, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(service), "ExecStart=/usr/local/sbin/sockerless-network") {
		t.Fatalf("service does not execute network configurator:\n%s", service)
	}
	for _, rel := range []string{
		"etc/init.d/fcnet",
		"etc/network/interfaces.d/10-fcnet",
		"etc/rc2.d/S01fcnet",
		"etc/systemd/system/multi-user.target.wants/fcnet.service",
		"lib/systemd/system/fcnet.service",
		"usr/local/bin/fcnet-setup.sh",
	} {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("stock Firecracker networking path still exists at %s: %v", rel, err)
		}
	}
	for _, service := range []string{"fcnet.service", "fcnet-setup.service"} {
		target, err := os.Readlink(filepath.Join(dir, "etc", "systemd", "system", service))
		if err != nil {
			t.Fatalf("stock Firecracker network service %s was not masked: %v", service, err)
		}
		if target != "/dev/null" {
			t.Fatalf("stock Firecracker network service %s mask = %q, want /dev/null", service, target)
		}
	}
	if _, err := os.Stat(filepath.Join(netplanDir, "50-cloud-init.yaml")); !os.IsNotExist(err) {
		t.Fatalf("stale netplan config still exists: %v", err)
	}
	ifupdown, err := os.ReadFile(filepath.Join(dir, "etc", "network", "interfaces.d", "sockerless-eth0"))
	if err != nil {
		t.Fatal(err)
	}
	mainIfupdown, err := os.ReadFile(filepath.Join(dir, "etc", "network", "interfaces"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mainIfupdown) != string(ifupdown) {
		t.Fatalf("main interfaces config and interfaces.d config differ:\nmain:\n%s\ninterfaces.d:\n%s", mainIfupdown, ifupdown)
	}
	for _, want := range []string{
		"address 10.26.0.2",
		"netmask 255.255.255.0",
		"gateway 10.26.0.1",
	} {
		if !strings.Contains(string(ifupdown), want) {
			t.Fatalf("ifupdown config missing %q:\n%s", want, ifupdown)
		}
	}
	resolv, err := os.ReadFile(filepath.Join(dir, "etc", "resolv.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(resolv) != "nameserver 1.1.1.1\n" {
		t.Fatalf("resolv.conf = %q", resolv)
	}
	hosts, err := os.ReadFile(filepath.Join(dir, "etc", "hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hosts), "169.254.169.254 metadata.google.internal metadata") {
		t.Fatalf("hosts does not contain provider metadata aliases:\n%s", hosts)
	}

	link := filepath.Join(dir, "etc", "systemd", "system", "multi-user.target.wants", "sockerless-network.service")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != "../sockerless-network.service" {
		t.Fatalf("service symlink = %q, want ../sockerless-network.service", target)
	}
}

func TestFirecrackerFailureLogsIncludeConsoleTail(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "firecracker-console.log"), []byte("boot line\nnetwork failed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := firecrackerFailureLogs(dir)
	if !strings.Contains(got, "----- firecracker-console.log -----") {
		t.Fatalf("missing console header: %q", got)
	}
	if !strings.Contains(got, "network failed") {
		t.Fatalf("missing console body: %q", got)
	}
}

func TestExt4RootFSImageSizeUsesMeasuredPayload(t *testing.T) {
	const mib = uint64(1024 * 1024)
	tests := []struct {
		name   string
		usedKB uint64
		want   uint64
	}{
		{name: "small payload keeps one gigabyte floor", usedKB: 256 * 1024, want: 1024 * mib},
		{name: "larger payload doubles data and adds headroom", usedKB: 700 * 1024, want: 1664 * mib},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ext4RootFSImageSizeBytes(tt.usedKB); got != tt.want {
				t.Fatalf("size = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSweepStaleFirecrackerWorkspacesRemovesOnlyAbandonedOnes(t *testing.T) {
	base := t.TempDir()

	// A workspace whose machine is still running. os.Getpid is a process that
	// certainly exists, which is what the sweep tests for.
	live := filepath.Join(base, "live-1")
	writeWorkspace(t, live, strconv.Itoa(os.Getpid()))

	// A workspace whose machine is gone. Kernel PIDs are bounded by
	// /proc/sys/kernel/pid_max, well under this value, so it cannot be live.
	abandoned := filepath.Join(base, "abandoned-1")
	writeWorkspace(t, abandoned, "2147483646")

	// A workspace that recorded nothing. Old enough that its machine cannot
	// still be on its way to starting.
	stale := filepath.Join(base, "stale-1")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * firecrackerWorkspaceGrace)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	// A workspace created moments ago, before its machine could record itself.
	// Sweeping it would delete a workspace that is about to be used.
	young := filepath.Join(base, "young-1")
	if err := os.MkdirAll(young, 0o755); err != nil {
		t.Fatal(err)
	}

	sweepStaleFirecrackerWorkspaces(base)

	for _, kept := range []string{live, young} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("sweep removed a workspace that was still in use: %s", filepath.Base(kept))
		}
	}
	for _, removed := range []string{abandoned, stale} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Errorf("sweep kept an abandoned workspace: %s", filepath.Base(removed))
		}
	}
}

func writeWorkspace(t *testing.T, dir, pid string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A workspace holds a root filesystem image; the sweep must reclaim it.
	if err := os.WriteFile(filepath.Join(dir, "rootfs.ext4"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, firecrackerOwnerFile), []byte(pid), 0o644); err != nil {
		t.Fatal(err)
	}
}
