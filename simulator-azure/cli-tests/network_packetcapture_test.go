package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const packetCaptureAPIVersion = "2025-03-01"

// A capture records the machine's frames into the local path it names, once
// the Network Watcher agent runs on the machine, and stops on request.
func TestNetworkWatcher_PacketCaptureRecordsToTheMachinesLocalPath(t *testing.T) {
	t.Parallel()
	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-pcap-vnet", "2024-05-01")
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.111.0.0/16"]}}}`))
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-pcap-vnet/subnets/cli-pcap-subnet", "2024-05-01")
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.111.1.0/24"}}`))
	resourceID := func(provider, path string) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s",
			subscriptionID, resourceGroup, provider, path)
	}
	nicURL := armURL("Microsoft.Network", "networkInterfaces/cli-pcap-nic", "2024-05-01")
	runCLI(t, azRest("PUT", nicURL, fmt.Sprintf(
		`{"location":"eastus","properties":{"ipConfigurations":[{"name":"ipconfig1","properties":{"subnet":{"id":%q},"privateIPAllocationMethod":"Dynamic","primary":true}}]}}`,
		resourceID("Microsoft.Network", "virtualNetworks/cli-pcap-vnet/subnets/cli-pcap-subnet"))))
	vmID := resourceID("Microsoft.Compute", "virtualMachines/cli-pcap-vm")
	vmURL := baseURL + vmID
	azRestLongRunning(t, "PUT", vmURL+"?api-version=2022-03-01", fmt.Sprintf(
		`{"location":"eastus","properties":{"hardwareProfile":{"vmSize":"Standard_B1s"},`+
			`"storageProfile":{"imageReference":{"publisher":"Canonical","offer":"ubuntu-24_04-lts","sku":"server","version":"latest"},"osDisk":{"name":"cli-pcap-osdisk","createOption":"FromImage"}},`+
			`"osProfile":{"computerName":"cli-pcap","adminUsername":"azureuser","adminPassword":"Str0ng-password-12345!"},`+
			`"networkProfile":{"networkInterfaces":[{"id":%q,"properties":{"primary":true}}]}}}`,
		resourceID("Microsoft.Network", "networkInterfaces/cli-pcap-nic")))
	defer runCLI(t, azRest("DELETE", vmURL+"?api-version=2022-03-01", ""))

	watcherURL := armURL("Microsoft.Network", "networkWatchers/cli-pcap-watcher", packetCaptureAPIVersion)
	runCLI(t, azRest("PUT", watcherURL, `{"location":"eastus","properties":{}}`))
	defer runCLI(t, azRest("DELETE", watcherURL, ""))

	captureURL := armURL("Microsoft.Network",
		"networkWatchers/cli-pcap-watcher/packetCaptures/cli-pcap", packetCaptureAPIVersion)
	const localPath = "/var/captures/cli-pcap.cap"
	body := fmt.Sprintf(`{"properties":{"target":%q,"storageLocation":{"filePath":%q},`+
		`"filters":[{"protocol":"UDP","remotePort":"53"}]}}`, vmID, localPath)

	out, err := azRest("PUT", captureURL, body).CombinedOutput()
	require.Error(t, err, "a machine without the Network Watcher agent cannot be captured: %s", out)
	assert.Contains(t, string(out), "NetworkWatcherAgentLinux")

	runCLI(t, azRest("PUT", vmURL+"/extensions/AzureNetworkWatcherExtension?api-version=2022-03-01",
		`{"location":"eastus","properties":{"publisher":"Microsoft.Azure.NetworkWatcher","type":"NetworkWatcherAgentLinux","typeHandlerVersion":"1.4"}}`))

	created := runCLI(t, azRest("PUT", captureURL, body))
	assert.Contains(t, created, `"provisioningState": "Succeeded"`)

	runScript := func(name, command string) string {
		return runCLI(t, azRest("PUT", vmURL+"/extensions/"+name+"?api-version=2022-03-01", fmt.Sprintf(
			`{"location":"eastus","properties":{"publisher":"Microsoft.Azure.Extensions","type":"CustomScript","typeHandlerVersion":"2.1","settings":{"commandToExecute":%q}}}`,
			command)))
	}
	runScript("traffic", `bash -c 'echo probe > /dev/udp/168.63.129.16/53'`)

	action := func(name string) string {
		return armURL("Microsoft.Network",
			"networkWatchers/cli-pcap-watcher/packetCaptures/cli-pcap/"+name, packetCaptureAPIVersion)
	}
	runCLI(t, azRest("POST", action("stop"), ""))
	statusOut := runCLI(t, azRest("POST", action("queryStatus"), ""))
	var status struct {
		PacketCaptureStatus string   `json:"packetCaptureStatus"`
		PacketCaptureError  []string `json:"packetCaptureError"`
	}
	parseJSON(t, statusOut, &status)
	assert.Equal(t, "Stopped", status.PacketCaptureStatus)
	assert.Empty(t, status.PacketCaptureError)

	// The file in the machine is a libpcap capture (magic a1b2c3d4, little
	// endian) holding the datagram the machine sent.
	header := runScript("inspect", `bash -c 'od -An -tx1 -N4 `+localPath+`; echo datagrams=$(grep -ac probe `+localPath+`)'`)
	assert.Contains(t, header, "d4 c3 b2 a1")
	assert.Contains(t, header, "datagrams=1")
}

// TestNetworkWatcher_PacketCaptureListIsEmptyBeforeAnyCapture: a watcher with
// no captures lists none, and a missing capture reads as not found.
func TestNetworkWatcher_PacketCaptureListIsEmptyBeforeAnyCapture(t *testing.T) {
	t.Parallel()
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
