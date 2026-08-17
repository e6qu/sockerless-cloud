//go:build realexec_host && linux

package main

import (
	"strings"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

// requireRealNetworkFabric gates a test on the real-execution network fabric,
// distinguishing the two reasons the fabric can be unavailable, because only
// one of them is a legitimate reason not to run.
//
//   - A kernel capability this host cannot be given — no CAP_NET_ADMIN, no
//     CAP_SYS_ADMIN, no /dev/kvm — is a platform gate: there is no way to
//     install a capability into a foreign kernel, so the test cannot run here
//     and says so.
//   - A missing command is not. `ip`, `nft` and `sysctl` are packages a host
//     installs, and a test that quietly disappears when one is absent reports
//     green having proved nothing — the very thing this repository forbids. So
//     a host that has the capabilities but not the tools fails loudly, naming
//     what to install.
func requireRealNetworkFabric(t *testing.T) {
	t.Helper()
	report := realexec.DetectNetworkCapabilities()
	if err := report.Require(); err == nil {
		return
	}
	var commands, platform []string
	for _, missing := range report.Missing {
		if name, ok := strings.CutPrefix(missing, "command:"); ok {
			commands = append(commands, name)
			continue
		}
		platform = append(platform, missing)
	}
	if len(commands) > 0 {
		t.Fatalf("the real EC2 network fabric needs these commands on the host, which must be installed rather than skipped over: %s",
			strings.Join(commands, ", "))
	}
	t.Skipf("platform gate: the host kernel does not provide %s", strings.Join(platform, ", "))
}
