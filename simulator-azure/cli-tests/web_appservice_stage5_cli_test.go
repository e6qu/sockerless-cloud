package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the App Service networking surfaces. The VNet integration
// commands the Azure CLI actually ships — `az webapp vnet-integration
// add/list/remove` and `az functionapp vnet-integration add/list/remove` —
// run natively against a TLS simulator through the CLI's own login flow;
// `add` PUTs the swift networkConfig/virtualNetwork spelling while `list`
// reads the classic virtualNetworkConnections spelling, so the commands only
// work because the simulator keeps the two spellings coherent. The surfaces
// no az command wraps go through az rest:
//
//	GET  /sites/{siteName}/virtualNetworkConnections (+ slot)
//	GET+PUT+PATCH+DELETE /sites/{siteName}/virtualNetworkConnections/{vnetName}
//	GET+PUT+PATCH /sites/{siteName}/virtualNetworkConnections/{vnetName}/gateways/{gatewayName}
//	GET+PUT /sites/{siteName}/privateAccess/virtualNetworks
//	GET  /sites/{siteName}/networkFeatures/{view}
//	GET+PUT+PATCH+DELETE /sites/{siteName}/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}
//	GET  /sites/{siteName}/hybridConnectionRelays
//	GET  /sites/{siteName}/hybridconnection
//	GET+PUT+PATCH+DELETE /sites/{siteName}/hybridconnection/{entityName}
//	GET  /sites/{siteName}/privateEndpointConnections
//	GET  /sites/{siteName}/privateLinkResources
//	GET  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections/{vnetName}
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections/{vnetName}/gateways/{gatewayName}
//	GET  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections/{vnetName}/routes
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections/{vnetName}/routes/{routeName}
//	GET  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionRelays
//	GET+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}/listKeys
//	GET  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}/sites
//	GET  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionPlanLimits/limit
//	GET  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/capabilities
//	GET  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/skus
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/listinstances
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/workers/{workerName}/reboot
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/workers/{workerName}/recycleinstance
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/getrdppassword

const stage5CLIAPIVersion = "2024-04-01"

func TestWebAppStage5_VnetIntegrationAndNetworkingTail(t *testing.T) {
	// The full workload runtime: a Microsoft.Web/serverFarms-delegated subnet
	// is realized as a real Docker network, which SIM_RUNTIME=process cannot do.
	env := startAzTLSSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-stage5",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-stage5"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	rg := "stage5-cli-rg"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))

	arm := func(resourcePath string) string {
		return fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/%s?api-version=%s",
			env.baseURL, subscriptionID, rg, resourcePath, stage5CLIAPIVersion)
	}
	rest := func(method, url, body string) string {
		args := []string{"rest", "--method", method, "--url", url, "-o", "json"}
		if body != "" {
			args = append(args, "--body", body)
		}
		return runCLI(t, env.command(args...))
	}

	// Plan + apps. Regional VNet integration needs a Standard plan, exactly
	// what `az webapp vnet-integration add` expects to find.
	planID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/stage5-cli-plan", subscriptionID, rg)
	rest("PUT", arm("Microsoft.Web/serverfarms/stage5-cli-plan"),
		`{"location":"eastus","kind":"linux","sku":{"name":"S1","tier":"Standard"},"properties":{"reserved":true}}`)
	rest("PUT", arm("Microsoft.Web/sites/stage5-cli-app"), fmt.Sprintf(
		`{"location":"eastus","kind":"app,linux,container","properties":{"serverFarmId":"%s","siteConfig":{"linuxFxVersion":"DOCKER|registry.example/sockerless-overlay/azf:test"}}}`, planID))
	rest("PUT", arm("Microsoft.Web/sites/stage5-cli-func"), fmt.Sprintf(
		`{"location":"eastus","kind":"functionapp,linux,container","properties":{"serverFarmId":"%s","siteConfig":{"linuxFxVersion":"DOCKER|registry.example/sockerless-overlay/azf:test"}}}`, planID))

	// A real VNet + Microsoft.Web/serverFarms-delegated subnet in the
	// Microsoft.Network store `az … vnet-integration add` resolves against.
	rest("PUT", arm("Microsoft.Network/virtualNetworks/stage5-cli-vnet"),
		`{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.70.0.0/16"]}}}`)
	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/stage5-cli-vnet/subnets/appsvc", subscriptionID, rg)
	rest("PUT", arm("Microsoft.Network/virtualNetworks/stage5-cli-vnet/subnets/appsvc"),
		`{"properties":{"addressPrefix":"10.70.1.0/24","delegations":[{"name":"appservice","properties":{"serviceName":"Microsoft.Web/serverFarms"}}]}}`)

	// `add` PUTs the swift spelling; `list` reads the classic spelling. The
	// entry the CLI shows is the proof the two spellings describe one state.
	runCLI(t, env.command("webapp", "vnet-integration", "add",
		"-g", rg, "-n", "stage5-cli-app", "--vnet", "stage5-cli-vnet", "--subnet", "appsvc", "-o", "json"))

	// The CLI reformats each entry client-side: name = the part after the
	// '_' in the wire name (the subnet), vnetResourceId lifted to the top.
	var listed []struct {
		Name           string `json:"name"`
		VnetResourceID string `json:"vnetResourceId"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "vnet-integration", "list",
		"-g", rg, "-n", "stage5-cli-app", "-o", "json")), &listed)
	require.Len(t, listed, 1)
	assert.Equal(t, "appsvc", listed[0].Name)
	assert.True(t, strings.EqualFold(subnetID, listed[0].VnetResourceID))

	// The wire entry underneath carries the canonical <vnet>_<subnet> name
	// and the isSwift marker — the coherent classic view of the swift write.
	var wireList []struct {
		Name       string `json:"name"`
		Properties struct {
			VnetResourceID string `json:"vnetResourceId"`
			IsSwift        bool   `json:"isSwift"`
		} `json:"properties"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-app/virtualNetworkConnections"), ""), &wireList)
	require.Len(t, wireList, 1)
	assert.Equal(t, "stage5-cli-vnet_appsvc", wireList[0].Name)
	assert.True(t, strings.EqualFold(subnetID, wireList[0].Properties.VnetResourceID))
	assert.True(t, wireList[0].Properties.IsSwift)

	runCLI(t, env.command("webapp", "vnet-integration", "remove",
		"-g", rg, "-n", "stage5-cli-app"))
	listed = nil
	parseJSON(t, runCLI(t, env.command("webapp", "vnet-integration", "list",
		"-g", rg, "-n", "stage5-cli-app", "-o", "json")), &listed)
	assert.Empty(t, listed)

	runCLI(t, env.command("functionapp", "vnet-integration", "add",
		"-g", rg, "-n", "stage5-cli-func", "--vnet", "stage5-cli-vnet", "--subnet", "appsvc", "-o", "json"))
	listed = nil
	parseJSON(t, runCLI(t, env.command("functionapp", "vnet-integration", "list",
		"-g", rg, "-n", "stage5-cli-func", "-o", "json")), &listed)
	require.Len(t, listed, 1)
	assert.True(t, strings.EqualFold(subnetID, listed[0].VnetResourceID))

	// The function app's live integration is what the plan view assembles.
	var planConn struct {
		ID         string `json:"id"`
		Properties struct {
			VnetResourceID string `json:"vnetResourceId"`
		} `json:"properties"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/virtualNetworkConnections/stage5-cli-vnet_appsvc"), ""), &planConn)
	assert.True(t, strings.EqualFold(subnetID, planConn.Properties.VnetResourceID))
	assert.Contains(t, planConn.ID, "/serverfarms/stage5-cli-plan/virtualNetworkConnections/")

	// Routes CRUD; the single-route GET answers an array per the contract.
	var route struct {
		Name       string `json:"name"`
		Properties struct {
			StartAddress string `json:"startAddress"`
			RouteType    string `json:"routeType"`
		} `json:"properties"`
	}
	parseJSON(t, rest("PUT", arm("Microsoft.Web/serverfarms/stage5-cli-plan/virtualNetworkConnections/stage5-cli-vnet_appsvc/routes/corp"),
		`{"properties":{"startAddress":"10.200.0.0","endAddress":"10.200.255.255","routeType":"STATIC"}}`), &route)
	assert.Equal(t, "corp", route.Name)
	var routeArr []struct {
		Properties struct {
			StartAddress string `json:"startAddress"`
		} `json:"properties"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/virtualNetworkConnections/stage5-cli-vnet_appsvc/routes/corp"), ""), &routeArr)
	require.Len(t, routeArr, 1)
	assert.Equal(t, "10.200.0.0", routeArr[0].Properties.StartAddress)
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/virtualNetworkConnections/stage5-cli-vnet_appsvc/routes"), ""), &routeArr)
	require.Len(t, routeArr, 1)
	parseJSON(t, rest("PATCH", arm("Microsoft.Web/serverfarms/stage5-cli-plan/virtualNetworkConnections/stage5-cli-vnet_appsvc/routes/corp"),
		`{"properties":{"startAddress":"10.201.0.0"}}`), &route)
	assert.Equal(t, "10.201.0.0", route.Properties.StartAddress)
	rest("DELETE", arm("Microsoft.Web/serverfarms/stage5-cli-plan/virtualNetworkConnections/stage5-cli-vnet_appsvc/routes/corp"), "")

	// Plan connection gateway.
	var gw struct {
		Name       string `json:"name"`
		Properties struct {
			VnetName      string `json:"vnetName"`
			VpnPackageURI string `json:"vpnPackageUri"`
		} `json:"properties"`
	}
	parseJSON(t, rest("PUT", arm("Microsoft.Web/serverfarms/stage5-cli-plan/virtualNetworkConnections/stage5-cli-vnet_appsvc/gateways/primary"),
		`{"properties":{"vpnPackageUri":"https://vpn.example/cli.zip"}}`), &gw)
	assert.Equal(t, "primary", gw.Name)
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/virtualNetworkConnections/stage5-cli-vnet_appsvc/gateways/primary"), ""), &gw)
	assert.Equal(t, "https://vpn.example/cli.zip", gw.Properties.VpnPackageURI)

	// Site connection gateway (classic spelling of the same connection).
	parseJSON(t, rest("PUT", arm("Microsoft.Web/sites/stage5-cli-func/virtualNetworkConnections/stage5-cli-vnet_appsvc/gateways/primary"),
		`{"properties":{"vpnPackageUri":"https://vpn.example/site.zip"}}`), &gw)
	assert.Equal(t, "stage5-cli-vnet_appsvc", gw.Properties.VnetName)
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/virtualNetworkConnections/stage5-cli-vnet_appsvc/gateways/primary"), ""), &gw)
	assert.Equal(t, "https://vpn.example/site.zip", gw.Properties.VpnPackageURI)
	parseJSON(t, rest("PATCH", arm("Microsoft.Web/sites/stage5-cli-func/virtualNetworkConnections/stage5-cli-vnet_appsvc/gateways/primary"),
		`{"properties":{"vpnPackageUri":"https://vpn.example/site2.zip"}}`), &gw)
	assert.Equal(t, "https://vpn.example/site2.zip", gw.Properties.VpnPackageURI)

	// Classic PUT + PATCH + GET on the site connection itself.
	var conn struct {
		Properties struct {
			VnetResourceID string `json:"vnetResourceId"`
			DNSServers     string `json:"dnsServers"`
		} `json:"properties"`
	}
	parseJSON(t, rest("PATCH", arm("Microsoft.Web/sites/stage5-cli-func/virtualNetworkConnections/stage5-cli-vnet_appsvc"),
		`{"properties":{"dnsServers":"10.70.0.53"}}`), &conn)
	assert.Equal(t, "10.70.0.53", conn.Properties.DNSServers)
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/virtualNetworkConnections/stage5-cli-vnet_appsvc"), ""), &conn)
	assert.True(t, strings.EqualFold(subnetID, conn.Properties.VnetResourceID))

	var relay struct {
		Properties struct {
			ServiceBusNamespace string `json:"serviceBusNamespace"`
			RelayName           string `json:"relayName"`
			Hostname            string `json:"hostname"`
			Port                int    `json:"port"`
			SendKeyValue        string `json:"sendKeyValue"`
		} `json:"properties"`
	}
	parseJSON(t, rest("PUT", arm("Microsoft.Web/sites/stage5-cli-func/hybridConnectionNamespaces/cli-relay-ns/relays/cli-relay"),
		`{"properties":{"hostname":"onprem.internal","port":1433,"sendKeyName":"defaultSender","sendKeyValue":"cli-send-key"}}`), &relay)
	assert.Equal(t, "cli-relay-ns", relay.Properties.ServiceBusNamespace)
	assert.Empty(t, relay.Properties.SendKeyValue, "sendKeyValue must not ride an ARM response")
	parseJSON(t, rest("PATCH", arm("Microsoft.Web/sites/stage5-cli-func/hybridConnectionNamespaces/cli-relay-ns/relays/cli-relay"),
		`{"properties":{"port":1434}}`), &relay)
	assert.Equal(t, 1434, relay.Properties.Port)
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/hybridConnectionNamespaces/cli-relay-ns/relays/cli-relay"), ""), &relay)
	assert.Equal(t, "onprem.internal", relay.Properties.Hostname)
	var relayView struct {
		ID string `json:"id"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/hybridConnectionRelays"), ""), &relayView)
	assert.True(t, strings.HasSuffix(relayView.ID, "/hybridConnectionNamespaces/cli-relay-ns/relays/cli-relay"))

	// Plan-level hybrid views assemble from the site's connection.
	var planRelays struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/hybridConnectionRelays"), ""), &planRelays)
	require.Len(t, planRelays.Value, 1)
	assert.Contains(t, planRelays.Value[0].ID, "/serverfarms/stage5-cli-plan/hybridConnectionNamespaces/")
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/hybridConnectionNamespaces/cli-relay-ns/relays/cli-relay"), ""), &relay)
	assert.Equal(t, 1434, relay.Properties.Port)

	var keys struct {
		Properties struct {
			SendKeyName  string `json:"sendKeyName"`
			SendKeyValue string `json:"sendKeyValue"`
		} `json:"properties"`
	}
	parseJSON(t, rest("POST", arm("Microsoft.Web/serverfarms/stage5-cli-plan/hybridConnectionNamespaces/cli-relay-ns/relays/cli-relay/listKeys"), ""), &keys)
	assert.Equal(t, "defaultSender", keys.Properties.SendKeyName)
	assert.Equal(t, "cli-send-key", keys.Properties.SendKeyValue)

	var apps struct {
		Value []string `json:"value"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/hybridConnectionNamespaces/cli-relay-ns/relays/cli-relay/sites"), ""), &apps)
	require.Len(t, apps.Value, 1)
	assert.Contains(t, apps.Value[0], "/sites/stage5-cli-func")

	var limit struct {
		Properties struct {
			Current int `json:"current"`
			Maximum int `json:"maximum"`
		} `json:"properties"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/hybridConnectionPlanLimits/limit"), ""), &limit)
	assert.Equal(t, 1, limit.Properties.Current)
	assert.Equal(t, 25, limit.Properties.Maximum)

	// Classic (BizTalk) relay service connection.
	var entity struct {
		Name       string `json:"name"`
		Properties struct {
			Hostname string `json:"hostname"`
			Port     int    `json:"port"`
		} `json:"properties"`
	}
	parseJSON(t, rest("PUT", arm("Microsoft.Web/sites/stage5-cli-func/hybridconnection/cli-biztalk"),
		`{"properties":{"hostname":"legacy.internal","port":8080}}`), &entity)
	assert.Equal(t, "cli-biztalk", entity.Name)
	parseJSON(t, rest("PATCH", arm("Microsoft.Web/sites/stage5-cli-func/hybridconnection/cli-biztalk"),
		`{"properties":{"hostname":"legacy2.internal","port":8080}}`), &entity)
	assert.Equal(t, "legacy2.internal", entity.Properties.Hostname)
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/hybridconnection/cli-biztalk"), ""), &entity)
	assert.Equal(t, "legacy2.internal", entity.Properties.Hostname)
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/hybridconnection"), ""), &entity)
	assert.Equal(t, "cli-biztalk", entity.Name)

	// --- networkFeatures: the assembled view names what the site really has ----
	var features struct {
		Properties struct {
			VirtualNetworkName       string `json:"virtualNetworkName"`
			VirtualNetworkConnection struct {
				VnetResourceID string `json:"vnetResourceId"`
			} `json:"virtualNetworkConnection"`
			HybridConnections   []any `json:"hybridConnections"`
			HybridConnectionsV2 []any `json:"hybridConnectionsV2"`
		} `json:"properties"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/networkFeatures/summary"), ""), &features)
	assert.Equal(t, "stage5-cli-vnet", features.Properties.VirtualNetworkName)
	assert.True(t, strings.EqualFold(subnetID, features.Properties.VirtualNetworkConnection.VnetResourceID))
	assert.Len(t, features.Properties.HybridConnections, 1)
	assert.Len(t, features.Properties.HybridConnectionsV2, 1)

	var pa struct {
		Properties struct {
			Enabled         bool `json:"enabled"`
			VirtualNetworks []struct {
				Name string `json:"name"`
			} `json:"virtualNetworks"`
		} `json:"properties"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/privateAccess/virtualNetworks"), ""), &pa)
	assert.False(t, pa.Properties.Enabled)
	parseJSON(t, rest("PUT", arm("Microsoft.Web/sites/stage5-cli-func/privateAccess/virtualNetworks"),
		`{"properties":{"enabled":true,"virtualNetworks":[{"name":"stage5-cli-vnet","key":1,"resourceId":"`+
			fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/stage5-cli-vnet", subscriptionID, rg)+
			`","subnets":[{"name":"appsvc","key":1}]}]}}`), &pa)
	assert.True(t, pa.Properties.Enabled)
	require.Len(t, pa.Properties.VirtualNetworks, 1)

	// --- Site private endpoint surfaces (no endpoint targets the site here) ----
	var pecs struct {
		Value []any `json:"value"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/privateEndpointConnections"), ""), &pecs)
	assert.Empty(t, pecs.Value)
	var plr struct {
		Value []struct {
			Properties struct {
				GroupID string `json:"groupId"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/sites/stage5-cli-func/privateLinkResources"), ""), &plr)
	require.Len(t, plr.Value, 1)
	assert.Equal(t, "sites", plr.Value[0].Properties.GroupID)

	var caps []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/capabilities"), ""), &caps)
	capByName := map[string]string{}
	for _, c := range caps {
		capByName[c.Name] = c.Value
	}
	assert.Equal(t, "true", capByName["LinuxWorkers"])

	var skus struct {
		ResourceType string `json:"resourceType"`
		Skus         []struct {
			Name string `json:"name"`
		} `json:"skus"`
	}
	parseJSON(t, rest("GET", arm("Microsoft.Web/serverfarms/stage5-cli-plan/skus"), ""), &skus)
	assert.Equal(t, "Microsoft.Web/serverfarms", skus.ResourceType)
	require.Len(t, skus.Skus, 1)
	assert.Equal(t, "S1", skus.Skus[0].Name)

	// No site on the plan runs a container in this ARM-only cell, so the
	// instance surface answers the honest empty set.
	var details struct {
		ServerFarmName string `json:"serverFarmName"`
		InstanceCount  int    `json:"instanceCount"`
		Instances      []any  `json:"instances"`
	}
	parseJSON(t, rest("POST", arm("Microsoft.Web/serverfarms/stage5-cli-plan/listinstances"), ""), &details)
	assert.Equal(t, "stage5-cli-plan", details.ServerFarmName)
	assert.Equal(t, 0, details.InstanceCount)
	assert.Empty(t, details.Instances)

	// Worker actions on a worker that does not exist, and RDP on Linux
	// workers, answer real ARM errors (404 / 400) — az rest surfaces them as
	// command failures.
	rebootOut, err := env.command("rest", "--method", "POST",
		"--url", arm("Microsoft.Web/serverfarms/stage5-cli-plan/workers/deadbeef0000/reboot"), "-o", "json").CombinedOutput()
	require.Error(t, err, "reboot of a non-existent worker must fail")
	assert.Contains(t, string(rebootOut), "ResourceNotFound")
	recycleOut, err := env.command("rest", "--method", "POST",
		"--url", arm("Microsoft.Web/serverfarms/stage5-cli-plan/workers/deadbeef0000/recycleinstance"), "-o", "json").CombinedOutput()
	require.Error(t, err, "recycle of a non-existent worker must fail")
	assert.Contains(t, string(recycleOut), "ResourceNotFound")
	rdpOut, err := env.command("rest", "--method", "POST",
		"--url", arm("Microsoft.Web/serverfarms/stage5-cli-plan/getrdppassword"), "-o", "json").CombinedOutput()
	require.Error(t, err, "RDP password on a Linux-worker plan must fail")
	assert.Contains(t, string(rdpOut), "Windows Container")

	rest("DELETE", arm("Microsoft.Web/serverfarms/stage5-cli-plan/hybridConnectionNamespaces/cli-relay-ns/relays/cli-relay"), "")
	rest("DELETE", arm("Microsoft.Web/sites/stage5-cli-func/hybridconnection/cli-biztalk"), "")
	rest("DELETE", arm("Microsoft.Web/sites/stage5-cli-func/virtualNetworkConnections/stage5-cli-vnet_appsvc"), "")
	listed = nil
	parseJSON(t, runCLI(t, env.command("functionapp", "vnet-integration", "list",
		"-g", rg, "-n", "stage5-cli-func", "-o", "json")), &listed)
	assert.Empty(t, listed)
}
