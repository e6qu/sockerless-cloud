package gcp_cli_test

import (
	"strings"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

// requireNetworkHost gates a test on the real-execution network fabric, with
// the same detection the simulator gates its Compute and Network endpoints on.
func requireNetworkHost(t *testing.T) {
	t.Helper()
	requireHost(t, realexec.DetectNetworkCapabilities(), "the real-execution network fabric")
}

// requireMicroVMHost gates a test that boots a virtual machine, which the
// simulator runs as a Firecracker microVM on the network fabric.
func requireMicroVMHost(t *testing.T) {
	t.Helper()
	requireNetworkHost(t)
	requireHost(t, realexec.DetectFirecrackerCapabilities(), "a Firecracker microVM")
}

// requireHost skips only for what a run cannot be given — a kernel other than
// Linux, /dev/kvm, CAP_NET_ADMIN — and fails on a Linux host that lacks a
// command, since skipping there would report the suite green without its
// Compute coverage.
func requireHost(t *testing.T, report realexec.CapabilityReport, what string) {
	t.Helper()
	err := report.Require()
	if err == nil {
		return
	}
	var tools []string
	if report.GOOS == "linux" {
		for _, missing := range report.Missing {
			if name, ok := strings.CutPrefix(missing, "command:"); ok {
				tools = append(tools, name)
			}
		}
	}
	if len(tools) > 0 {
		t.Fatalf("this Linux host is missing %s, which %s needs: install iproute2, nftables, "+
			"e2fsprogs and squashfs-tools, and Firecracker with scripts/install-firecracker.sh",
			strings.Join(tools, ", "), what)
	}
	t.Skipf("platform gate: %s needs a kernel capability this host does not provide: %v", what, err)
}
