package azure_cli_test

import (
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

// requireNetworkHost skips a test when the host lacks the real-execution
// networking capabilities (Linux + ip/nft/sysctl + CAP_NET_ADMIN) the Azure
// Compute/Network simulator requires — the same detection the simulator uses to
// gate those endpoints. So the test runs for real where the simulator can (the
// sudo + iproute2/nftables CI Linux runner) and skips cleanly elsewhere instead
// of failing with the simulator's 503 "missing real-execution host capabilities".
func requireNetworkHost(t *testing.T) {
	t.Helper()
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		t.Skipf("skipping: real Compute/Network requires host capabilities the simulator can't provide here: %v", err)
	}
}
