package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const packetCaptureAPIVersion = "2025-03-01"

// The az CLI's `network watcher packet-capture` group drives the same six
// operations the SDK does. A capture reads frames off a running machine's
// interface, so the create path needs a host that can run one; what this
// asserts unconditionally is the contract that keeps a capture honest — a
// target the simulator cannot record is refused, with a reason, rather than
// answered with an empty capture file a caller could not tell from a real one.
func TestNetworkWatcher_PacketCaptureRefusesAnUnrunnableTarget(t *testing.T) {
	watcherURL := armURL("Microsoft.Network", "networkWatchers/cli-pcap-watcher", packetCaptureAPIVersion)
	runCLI(t, azRest("PUT", watcherURL, `{"location":"eastus","properties":{}}`))
	defer runCLI(t, azRest("DELETE", watcherURL, ""))

	captureURL := armURL("Microsoft.Network",
		"networkWatchers/cli-pcap-watcher/packetCaptures/cli-pcap", packetCaptureAPIVersion)
	resourceID := func(provider, path string) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s",
			subscriptionID, resourceGroup, provider, path)
	}
	body := fmt.Sprintf(`{"properties":{"target":%q,"storageLocation":{"storageId":%q}}}`,
		resourceID("Microsoft.Compute", "virtualMachines/cli-pcap-no-such-vm"),
		resourceID("Microsoft.Storage", "storageAccounts/clipcapst"))
	out, err := azRest("PUT", captureURL, body).CombinedOutput()
	require.Error(t, err,
		"a capture against a machine that cannot be recorded must fail, not produce an empty capture: %s", out)
	assert.Contains(t, string(out), "not found",
		"the refusal names why the capture cannot run, got: %s", out)
}

// TestNetworkWatcher_PacketCaptureListIsEmptyBeforeAnyCapture pins the read
// surface: a watcher with no captures lists none, and reading a capture that
// does not exist is a not-found rather than an invented session.
func TestNetworkWatcher_PacketCaptureListIsEmptyBeforeAnyCapture(t *testing.T) {
	watcherURL := armURL("Microsoft.Network", "networkWatchers/cli-pcap-empty", packetCaptureAPIVersion)
	runCLI(t, azRest("PUT", watcherURL, `{"location":"eastus","properties":{}}`))
	defer runCLI(t, azRest("DELETE", watcherURL, ""))

	listURL := armURL("Microsoft.Network",
		"networkWatchers/cli-pcap-empty/packetCaptures", packetCaptureAPIVersion)
	out := runCLI(t, azRest("GET", listURL, ""))
	var listed struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &listed)
	assert.Empty(t, listed.Value, "a watcher with no captures lists none")

	missingURL := armURL("Microsoft.Network",
		"networkWatchers/cli-pcap-empty/packetCaptures/never-created", packetCaptureAPIVersion)
	missingOut, err := azRest("GET", missingURL, "").CombinedOutput()
	require.Error(t, err, "reading a capture that does not exist must fail: %s", missingOut)
	assert.Contains(t, string(missingOut), "not found")

	statusURL := armURL("Microsoft.Network",
		"networkWatchers/cli-pcap-empty/packetCaptures/never-created/queryStatus", packetCaptureAPIVersion)
	statusOut, err := azRest("POST", statusURL, "").CombinedOutput()
	require.Error(t, err,
		"querying the status of a capture that does not exist must fail rather than report a session: %s", statusOut)
}
