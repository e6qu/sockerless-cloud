package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure CLI coverage for App Service Environments
// (Microsoft.Web/hostingEnvironments) and Kubernetes Environments
// (Microsoft.Web/kubeEnvironments). The environment is driven the way
// Microsoft's own App Service Environment maintenance documentation drives
// it — `az rest` against the resource's own ARM paths, which is also how every
// other control-plane test in this suite talks to the simulator.
//
//	PUT|GET|DELETE .../hostingEnvironments/{name}
//	GET  .../hostingEnvironments/{name}/capacities/virtualip
//	GET  .../hostingEnvironments/{name}/capacities/compute
//	GET  .../hostingEnvironments/{name}/inboundNetworkDependenciesEndpoints
//	GET  .../hostingEnvironments/{name}/outboundNetworkDependenciesEndpoints (declared gap)
//	PUT  .../hostingEnvironments/{name}/workerPools/{workerPoolName}
//	GET  .../hostingEnvironments/{name}/multiRolePools/default/metricdefinitions (declared gap)
//	POST .../hostingEnvironments/{name}/suspend
//	POST .../hostingEnvironments/{name}/resume
//	POST .../hostingEnvironments/{name}/changeVirtualNetwork
//	POST .../hostingEnvironments/{name}/testUpgradeAvailableNotification
//	POST .../hostingEnvironments/{name}/upgrade
//	PUT|GET|DELETE .../kubeEnvironments/{name}

const aseCLIAPIVersion = "2025-03-01"

func aseCLIURL(path string) string {
	return armURL("Microsoft.Web", path, aseCLIAPIVersion)
}

// aseCLIOperationResult drives one of the App Service Environment operations
// whose final state comes via Location and returns the body the Location poll
// answered with. The operation answers 202 with no body, so a client that read
// only the response to the POST would see nothing and could conclude the
// environment moved no apps at all; the collection lives at the Location.
func aseCLIOperationResult(t *testing.T, resource, body string) string {
	t.Helper()
	location := azRestResponseHeader(t, azRest("POST", aseCLIURL(resource), body, "--debug"), "Location")
	require.NotEmpty(t, location,
		"a long-running operation whose final state comes via Location must send one")
	var out string
	require.Eventually(t, func() bool {
		out = runCLI(t, azRest("GET", location, ""))
		return strings.Contains(out, `"value"`)
	}, 30*time.Second, 250*time.Millisecond,
		"the Location poll never produced the operation's result")
	return out
}

// TestAppServiceEnvironmentCLI drives one App Service Environment through the
// Azure CLI: it is placed in a subnet the simulator holds, reports the address
// it derived from that subnet, scales a worker pool, is suspended, resumed and
// moved into another subnet, refuses an upgrade until one is announced, and
// names the two families it does not implement instead of answering them.
func TestAppServiceEnvironmentCLI(t *testing.T) {
	requireNetworkHost(t)

	runCLI(t, azRest("PUT", armURL("Microsoft.Network", "virtualNetworks/cli-ase-vnet", "2024-05-01"),
		`{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.80.0.0/16"]}}}`))
	runCLI(t, azRest("PUT", armURL("Microsoft.Network", "virtualNetworks/cli-ase-vnet/subnets/cli-ase-subnet", "2024-05-01"),
		`{"properties":{"addressPrefix":"10.80.2.0/24"}}`))
	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/cli-ase-vnet/subnets/cli-ase-subnet",
		subscriptionID, resourceGroup)

	aseURL := aseCLIURL("hostingEnvironments/cli-ase")
	out := runCLI(t, azRest("PUT", aseURL, fmt.Sprintf(
		`{"location":"eastus","kind":"ASEV3","properties":{"virtualNetwork":{"id":%q},"internalLoadBalancingMode":"Web, Publishing"}}`,
		subnetID)))
	assert.Contains(t, out, "appserviceenvironment.net",
		"an internally load-balanced environment publishes the internal domain suffix")
	// The address comes out of the subnet: Azure reserves a subnet's first four
	// addresses, so 10.80.2.4 is the first the environment can take.
	assert.Contains(t, out, "10.80.2.4")

	vip := runCLI(t, azRest("GET", aseCLIURL("hostingEnvironments/cli-ase/capacities/virtualip"), ""))
	assert.Contains(t, vip, `"internalIpAddress": "10.80.2.4"`)

	// A worker pool, and the stamp capacity computed from it.
	runCLI(t, azRest("PUT", aseCLIURL("hostingEnvironments/cli-ase/workerPools/1"),
		`{"properties":{"workerCount":2,"workerSize":"Medium","workerSizeId":1}}`))
	capacities := runCLI(t, azRest("GET", aseCLIURL("hostingEnvironments/cli-ase/capacities/compute"), ""))
	assert.Contains(t, capacities, `"totalCapacity": 2`)
	assert.Contains(t, capacities, `"availableCapacity": 2`)

	inbound := runCLI(t, azRest("GET", aseCLIURL("hostingEnvironments/cli-ase/inboundNetworkDependenciesEndpoints"), ""))
	assert.Contains(t, inbound, "App Service Environment VIP")
	assert.Contains(t, inbound, "10.80.2.0/24", "the environment's own subnet is an inbound dependency")

	// Suspend, resume and move. All three are long-running with their final
	// state via Location, so each answers 202 and the collection of apps it
	// moved is read from the Location poll — this environment hosts none, so
	// the empty collection Microsoft's own examples for these three operations
	// show. Each also leaves its mark: the suspended flag, and an internal
	// address re-derived from the subnet the environment was moved into.
	assert.Contains(t, aseCLIOperationResult(t, "hostingEnvironments/cli-ase/suspend", ""),
		`"value": []`)
	afterSuspend := runCLI(t, azRest("GET", aseURL, ""))
	assert.Contains(t, afterSuspend, `"suspended": true`)

	assert.Contains(t, aseCLIOperationResult(t, "hostingEnvironments/cli-ase/resume", ""),
		`"value": []`)

	runCLI(t, azRest("PUT", armURL("Microsoft.Network", "virtualNetworks/cli-ase-vnet/subnets/cli-ase-subnet-2", "2024-05-01"),
		`{"properties":{"addressPrefix":"10.80.6.0/24"}}`))
	movedSubnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/cli-ase-vnet/subnets/cli-ase-subnet-2",
		subscriptionID, resourceGroup)
	assert.Contains(t, aseCLIOperationResult(t, "hostingEnvironments/cli-ase/changeVirtualNetwork",
		fmt.Sprintf(`{"id":%q}`, movedSubnetID)), `"value": []`)
	afterMove := runCLI(t, azRest("GET", aseURL, ""))
	assert.Contains(t, afterMove, `"suspended": false`, "resuming cleared the suspended flag")
	assert.Contains(t, afterMove, "10.80.6.4", "the move re-derived the address from the new subnet")

	// The upgrade pair: refused until the notification announces one.
	refusal := runStorageCLIExpectFailure(t, azRest("POST", aseCLIURL("hostingEnvironments/cli-ase/upgrade"), ""))
	assert.Contains(t, refusal, "No upgrade is available")
	runCLI(t, azRest("POST", aseCLIURL("hostingEnvironments/cli-ase/testUpgradeAvailableNotification"), ""))
	announced := runCLI(t, azRest("GET", aseURL, ""))
	assert.Contains(t, announced, `"upgradeAvailability": "Ready"`)
	runCLI(t, azRest("POST", aseCLIURL("hostingEnvironments/cli-ase/upgrade"), ""))

	// The two declared gaps name themselves rather than 404ing silently.
	for _, path := range []string{
		"hostingEnvironments/cli-ase/multiRolePools/default/metricdefinitions",
		"hostingEnvironments/cli-ase/outboundNetworkDependenciesEndpoints",
	} {
		gap := runStorageCLIExpectFailure(t, azRest("GET", aseCLIURL(path), ""))
		assert.Contains(t, gap, "not implemented by the simulator", path)
	}

	runCLI(t, azRest("DELETE", aseURL, ""))
	gone := runStorageCLIExpectFailure(t, azRest("GET", aseURL, ""))
	assert.Contains(t, gone, "ResourceNotFound")
}

// TestKubeEnvironmentCLI drives a Kubernetes Environment through the CLI.
func TestKubeEnvironmentCLI(t *testing.T) {
	kubeURL := aseCLIURL("kubeEnvironments/cli-kube")
	out := runCLI(t, azRest("PUT", kubeURL,
		`{"location":"eastus","properties":{"staticIp":"10.81.0.9","internalLoadBalancerEnabled":true,"arcConfiguration":{"artifactStorageClassName":"default","kubeConfig":"apiVersion: v1"}}}`))
	assert.Contains(t, out, `"defaultDomain": "cli-kube.10-81-0-9.k4apps.io"`)
	assert.NotContains(t, out, "kubeConfig",
		"the Arc kubeConfig is a secret the service does not hand back")

	listed := runCLI(t, azRest("GET",
		fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/kubeEnvironments?api-version=%s",
			baseURL, subscriptionID, aseCLIAPIVersion), ""))
	assert.Contains(t, listed, "cli-kube")

	runCLI(t, azRest("DELETE", kubeURL, ""))
	gone := runStorageCLIExpectFailure(t, azRest("GET", kubeURL, ""))
	assert.Contains(t, gone, "ResourceNotFound")
}

// TestSiteDiagnosticsCLI drives the site diagnostics family through the CLI:
// the one category the simulator publishes, the detectors in it, a detector
// run on a site with no workload container, and the declared gap a detector
// the simulator cannot measure answers with.
func TestSiteDiagnosticsCLI(t *testing.T) {
	siteURL := aseCLIURL("sites/cli-diag-app")
	runCLI(t, azRest("PUT", siteURL,
		`{"location":"eastus","kind":"functionapp,linux,container","properties":{}}`))

	categories := runCLI(t, azRest("GET", aseCLIURL("sites/cli-diag-app/diagnostics"), ""))
	assert.Contains(t, categories, `"name": "availability"`)

	detectors := runCLI(t, azRest("GET", aseCLIURL("sites/cli-diag-app/diagnostics/availability/detectors"), ""))
	assert.Contains(t, detectors, `"name": "sitecrashes"`)
	assert.Contains(t, detectors, `"displayName": "Site Crash Events"`)

	// The site has no workload container, so the crash detector reports that
	// it measured nothing rather than reporting a healthy container.
	executed := runCLI(t, azRest("POST",
		aseCLIURL("sites/cli-diag-app/diagnostics/availability/detectors/sitecrashes/execute"), ""))
	assert.Contains(t, executed, `"issueDetected": false`)

	responses := runCLI(t, azRest("GET", aseCLIURL("sites/cli-diag-app/detectors"), ""))
	assert.Contains(t, responses, "has no workload container")

	gap := runStorageCLIExpectFailure(t, azRest("GET",
		aseCLIURL("sites/cli-diag-app/detectors/servicehealth"), ""))
	assert.Contains(t, gap, "it would need")

	runCLI(t, azRest("DELETE", siteURL, ""))
}
