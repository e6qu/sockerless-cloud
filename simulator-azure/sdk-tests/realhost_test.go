package azure_sdk_test

import (
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

// requireNetworkHost skips a test when the host lacks the real-execution
// networking capabilities (Linux + ip/nft/sysctl + CAP_NET_ADMIN) that the Azure
// Compute/Network simulator requires. It uses the SAME detection the simulator
// uses to gate those endpoints, so the test runs for real wherever the
// simulator can (e.g. the sudo + iproute2/nftables CI Linux runner) and skips
// cleanly elsewhere (e.g. a macOS dev box) instead of failing with the
// simulator's 503 "missing real-execution host capabilities".
func requireNetworkHost(t *testing.T) {
	t.Helper()
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		t.Skipf("skipping: real Compute/Network requires host capabilities the simulator can't provide here: %v", err)
	}
}
