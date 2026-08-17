package gcp_cli_test

import (
	"strings"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

// requireNetworkHost gates a test on the real-execution networking the GCP
// Compute/Network simulator needs, using the SAME detection the simulator uses
// to gate those endpoints — so the test runs for real wherever the simulator
// can, rather than failing with the simulator's 503 "missing real-execution
// host capabilities".
//
// The report is split by what the host can be given. A foreign kernel and an
// absent CAP_NET_ADMIN cannot be installed into a run, so those skip. A missing
// `ip`, `nft` or `sysctl` on a Linux host is a tool the image is supposed to
// carry: skipping there would silently delete the whole Compute/Network suite
// from a CI run and report green, so it fails loudly and names the package to
// install instead.
func requireNetworkHost(t *testing.T) {
	t.Helper()
	report := realexec.DetectNetworkCapabilities()
	if err := report.Require(); err == nil {
		return
	}
	if tools := missingHostTools(report); len(tools) > 0 {
		t.Fatalf("this Linux host is missing %s, which the real-execution network "+
			"fabric needs — install iproute2 and nftables rather than running without "+
			"Compute/Network coverage", strings.Join(tools, ", "))
	}
	t.Skipf("platform gate: real Compute/Network needs a kernel this host does not "+
		"provide: %v", report.Require())
}

// missingHostTools returns the executables a Linux host is missing from a
// capability report. On a non-Linux host the absent commands are a consequence
// of the kernel gate rather than a gap in the image, so none are reported.
func missingHostTools(report realexec.CapabilityReport) []string {
	if report.GOOS != "linux" {
		return nil
	}
	var tools []string
	for _, missing := range report.Missing {
		if name, ok := strings.CutPrefix(missing, "command:"); ok {
			tools = append(tools, name)
		}
	}
	return tools
}
