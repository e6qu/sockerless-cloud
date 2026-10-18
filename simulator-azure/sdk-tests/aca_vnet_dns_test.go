package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Container Apps job in an environment on a virtual network's infrastructure
// subnet resolves the Azure Private DNS zones linked to that virtual network
// through the resolver its network carries, and holds an address in the
// subnet.
func TestContainerApps_JobOnAVirtualNetworkResolvesPrivateDNS(t *testing.T) {
	t.Parallel()
	const rg = "aca-vnet-dns-rg"
	const zone = "corp.internal"
	ensureRG(t, rg)
	subnetID := stage5Subnet(t, rg, "aca-vnet", "10.103.0.0/16", "infrastructure", "10.103.0.0/23")

	zonesClient, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zonePoller, err := zonesClient.BeginCreateOrUpdate(ctx, rg, zone, armprivatedns.PrivateZone{Location: to.Ptr("global")}, nil)
	require.NoError(t, err)
	_, err = zonePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	vnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/aca-vnet", subscriptionID, rg)
	linksClient, err := armprivatedns.NewVirtualNetworkLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	linkPoller, err := linksClient.BeginCreateOrUpdate(ctx, rg, zone, "aca-vnet-link", armprivatedns.VirtualNetworkLink{
		Location:   to.Ptr("global"),
		Properties: &armprivatedns.VirtualNetworkLinkProperties{VirtualNetwork: &armprivatedns.SubResource{ID: to.Ptr(vnetID)}},
	}, nil)
	require.NoError(t, err)
	_, err = linkPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	recordSets, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = recordSets.CreateOrUpdate(ctx, rg, zone, armprivatedns.RecordTypeA, "db", armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL:      to.Ptr[int64](10),
			ARecords: []*armprivatedns.ARecord{{IPv4Address: to.Ptr("10.103.9.9")}},
		},
	}, nil)
	require.NoError(t, err)

	ws := createLogWorkspace(t, rg, "aca-vnet-ws")
	keys, err := armoperationalinsights.NewSharedKeysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	shared, err := keys.GetSharedKeys(ctx, ws.rg, ws.name, nil)
	require.NoError(t, err)
	envID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/aca-vnet-env", subscriptionID, rg)
	doPUT(t, fmt.Sprintf("%s%s?api-version=2024-03-01", baseURL, envID),
		fmt.Sprintf(`{"location":"eastus","properties":{"vnetConfiguration":{"infrastructureSubnetId":%q,"internal":true},`+
			`"appLogsConfiguration":{"destination":"log-analytics","logAnalyticsConfiguration":{"customerId":%q,"sharedKey":%q}}}}`,
			subnetID, ws.customerID, derefString(shared.PrimarySharedKey)))

	// The execution succeeds only when the zone's record resolves to the
	// address the record holds and the job's own address lies in the
	// infrastructure subnet.
	job := map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"environmentId": envID,
			"configuration": map[string]any{"triggerType": "Manual", "replicaTimeout": 60},
			"template": map[string]any{
				"containers": []map[string]any{{
					"name":    "resolver",
					"image":   "mirror.gcr.io/library/alpine:latest",
					"command": []string{"sh", "-c"},
					"args": []string{"echo \"resolved=$(getent hosts db." + zone + ")\"; echo \"address=$(hostname -i)\"; " +
						"getent hosts db." + zone + " | grep -q '^10.103.9.9 ' && hostname -i | grep -q '^10.103.[01]\\.'"},
				}},
			},
		},
	}
	raw, err := json.Marshal(job)
	require.NoError(t, err)
	doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/aca-vnet-job?api-version=2024-03-01", baseURL, subscriptionID, rg), string(raw))
	execName := acaStartExecution(t, rg, "aca-vnet-job")
	exec := acaWaitExecution(t, rg, "aca-vnet-job", execName)
	props := exec["properties"].(map[string]any)

	// The execution's console lines land in the workspace; they carry the
	// record's address and the job's own.
	var logs []string
	require.Eventually(t, func() bool {
		logs = logs[:0]
		result := queryWorkspace(t, ws.customerID, `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "aca-vnet-job"`)
		if len(result.Tables) != 1 {
			return false
		}
		logIdx := -1
		for i, col := range result.Tables[0].Columns {
			if col.Name == "Log_s" {
				logIdx = i
			}
		}
		if logIdx < 0 {
			return false
		}
		for _, row := range result.Tables[0].Rows {
			if line, ok := row[logIdx].(string); ok {
				logs = append(logs, line)
			}
		}
		return strings.Contains(strings.Join(logs, "\n"), "address=")
	}, 60*time.Second, 200*time.Millisecond)
	assert.Equal(t, "Succeeded", props["status"], "the job did not resolve the private zone through its network's resolver: %v", logs)
	assert.Contains(t, strings.Join(logs, "\n"), "resolved=10.103.9.9", "the zone's record did not resolve through the network's resolver: %v", logs)
}
