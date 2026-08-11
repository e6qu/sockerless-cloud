package azure_sdk_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure Network Watcher through the official armnetwork client. The diagnostics
// are asserted against a network the test builds itself — a virtual network, a
// subnet with a network security group and a route table, and a virtual machine
// on it — so every verdict the watcher gives is checked against the
// configuration that produced it rather than against a fixed expectation.

// TestNetworkWatcher_DiagnosticsFollowTheRealConfiguration builds a network,
// then makes the watcher answer for it: IP flow verify must follow the security
// rules, next hop must follow the route table, the security group view must
// report the groups actually attached, and the topology must report what
// contains what.
func TestNetworkWatcher_DiagnosticsFollowTheRealConfiguration(t *testing.T) {
	requireNetworkHost(t)
	rg := "netwatcher-rg"
	requireResourceGroup(t, rg)

	nsgClient, err := armnetwork.NewSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nsgPoller, err := nsgClient.BeginCreateOrUpdate(ctx, rg, "web-nsg", armnetwork.SecurityGroup{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.SecurityGroupPropertiesFormat{
			SecurityRules: []*armnetwork.SecurityRule{
				{
					Name: to.Ptr("allow-https"),
					Properties: &armnetwork.SecurityRulePropertiesFormat{
						Protocol:                 to.Ptr(armnetwork.SecurityRuleProtocolTCP),
						SourceAddressPrefix:      to.Ptr("*"),
						SourcePortRange:          to.Ptr("*"),
						DestinationAddressPrefix: to.Ptr("*"),
						DestinationPortRange:     to.Ptr("443"),
						Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
						Priority:                 to.Ptr[int32](200),
						Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
					},
				},
				{
					Name: to.Ptr("deny-ssh"),
					Properties: &armnetwork.SecurityRulePropertiesFormat{
						Protocol:                 to.Ptr(armnetwork.SecurityRuleProtocolTCP),
						SourceAddressPrefix:      to.Ptr("*"),
						SourcePortRange:          to.Ptr("*"),
						DestinationAddressPrefix: to.Ptr("*"),
						DestinationPortRange:     to.Ptr("22"),
						Access:                   to.Ptr(armnetwork.SecurityRuleAccessDeny),
						Priority:                 to.Ptr[int32](100),
						Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
					},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	nsg, err := nsgPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	routeClient, err := armnetwork.NewRouteTablesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	routePoller, err := routeClient.BeginCreateOrUpdate(ctx, rg, "egress-rt", armnetwork.RouteTable{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.RouteTablePropertiesFormat{
			Routes: []*armnetwork.Route{{
				Name: to.Ptr("through-firewall"),
				Properties: &armnetwork.RoutePropertiesFormat{
					AddressPrefix:    to.Ptr("203.0.113.0/24"),
					NextHopType:      to.Ptr(armnetwork.RouteNextHopTypeVirtualAppliance),
					NextHopIPAddress: to.Ptr("10.70.1.9"),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	routeTable, err := routePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, "watch-vnet", armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.70.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, "watch-vnet", "web", armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefix:        to.Ptr("10.70.1.0/24"),
			NetworkSecurityGroup: &armnetwork.SecurityGroup{ID: nsg.ID},
			RouteTable:           &armnetwork.RouteTable{ID: routeTable.ID},
		},
	}, nil)
	require.NoError(t, err)
	subnet, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, subnet.Properties.RouteTable, "the subnet must report the route table attached to it")

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, "web-nic", armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet: &armnetwork.Subnet{ID: subnet.ID},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	nic, err := nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	nicAddress := *nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress
	require.NotEmpty(t, nicAddress)

	watcherClient, err := armnetwork.NewWatchersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	watcher, err := watcherClient.CreateOrUpdate(ctx, rg, "NetworkWatcher_eastus", armnetwork.Watcher{
		Location:   to.Ptr("eastus"),
		Tags:       map[string]*string{"team": to.Ptr("network")},
		Properties: &armnetwork.WatcherPropertiesFormat{},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, armnetwork.ProvisioningStateSucceeded, *watcher.Properties.ProvisioningState)

	// --- IP flow verify --------------------------------------------------
	// The rule that denies SSH has the lower priority number, so it wins over
	// everything after it and the watcher must name it.
	denied := verifyIPFlow(t, watcherClient, rg, armnetwork.VerificationIPFlowParameters{
		TargetResourceID: nic.ID,
		Direction:        to.Ptr(armnetwork.DirectionInbound),
		Protocol:         to.Ptr(armnetwork.IPFlowProtocolTCP),
		LocalIPAddress:   to.Ptr(nicAddress),
		LocalPort:        to.Ptr("22"),
		RemoteIPAddress:  to.Ptr("198.51.100.7"),
		RemotePort:       to.Ptr("51000"),
	})
	assert.Equal(t, armnetwork.AccessDeny, *denied.Access)
	assert.Equal(t, "securityRules/deny-ssh", *denied.RuleName)

	allowed := verifyIPFlow(t, watcherClient, rg, armnetwork.VerificationIPFlowParameters{
		TargetResourceID: nic.ID,
		Direction:        to.Ptr(armnetwork.DirectionInbound),
		Protocol:         to.Ptr(armnetwork.IPFlowProtocolTCP),
		LocalIPAddress:   to.Ptr(nicAddress),
		LocalPort:        to.Ptr("443"),
		RemoteIPAddress:  to.Ptr("198.51.100.7"),
		RemotePort:       to.Ptr("51000"),
	})
	assert.Equal(t, armnetwork.AccessAllow, *allowed.Access)
	assert.Equal(t, "securityRules/allow-https", *allowed.RuleName)

	// A port no rule names falls through to the platform's deny-all default.
	fallthroughFlow := verifyIPFlow(t, watcherClient, rg, armnetwork.VerificationIPFlowParameters{
		TargetResourceID: nic.ID,
		Direction:        to.Ptr(armnetwork.DirectionInbound),
		Protocol:         to.Ptr(armnetwork.IPFlowProtocolTCP),
		LocalIPAddress:   to.Ptr(nicAddress),
		LocalPort:        to.Ptr("9000"),
		RemoteIPAddress:  to.Ptr("198.51.100.7"),
		RemotePort:       to.Ptr("51000"),
	})
	assert.Equal(t, armnetwork.AccessDeny, *fallthroughFlow.Access)
	assert.Equal(t, "defaultSecurityRules/DenyAllInBound", *fallthroughFlow.RuleName)

	// --- next hop --------------------------------------------------------
	// The route table covers 203.0.113.0/24 and sends it to a virtual appliance.
	viaAppliance := nextHop(t, watcherClient, rg, armnetwork.NextHopParameters{
		TargetResourceID:     nic.ID,
		SourceIPAddress:      to.Ptr(nicAddress),
		DestinationIPAddress: to.Ptr("203.0.113.10"),
	})
	assert.Equal(t, armnetwork.NextHopTypeVirtualAppliance, *viaAppliance.NextHopType)
	assert.Equal(t, "10.70.1.9", *viaAppliance.NextHopIPAddress)
	assert.Equal(t, *routeTable.ID, *viaAppliance.RouteTableID)

	// An address inside the virtual network is reached without leaving it, and a
	// system route carries no route table id.
	local := nextHop(t, watcherClient, rg, armnetwork.NextHopParameters{
		TargetResourceID:     nic.ID,
		SourceIPAddress:      to.Ptr(nicAddress),
		DestinationIPAddress: to.Ptr("10.70.2.5"),
	})
	assert.Equal(t, armnetwork.NextHopTypeVnetLocal, *local.NextHopType)
	assert.Nil(t, local.RouteTableID)

	internet := nextHop(t, watcherClient, rg, armnetwork.NextHopParameters{
		TargetResourceID:     nic.ID,
		SourceIPAddress:      to.Ptr(nicAddress),
		DestinationIPAddress: to.Ptr("8.8.8.8"),
	})
	assert.Equal(t, armnetwork.NextHopTypeInternet, *internet.NextHopType)

	// --- security group view ---------------------------------------------
	viewPoller, err := watcherClient.BeginGetVMSecurityRules(ctx, rg, "NetworkWatcher_eastus",
		armnetwork.SecurityGroupViewParameters{TargetResourceID: nic.ID}, nil)
	require.NoError(t, err)
	view, err := viewPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Len(t, view.NetworkInterfaces, 1)
	associations := view.NetworkInterfaces[0].SecurityRuleAssociations
	require.NotNil(t, associations)
	require.NotNil(t, associations.SubnetAssociation, "the subnet's group governs the interface")
	assert.Equal(t, *subnet.ID, *associations.SubnetAssociation.ID)
	require.Len(t, associations.SubnetAssociation.SecurityRules, 2)
	assert.NotEmpty(t, associations.DefaultSecurityRules)
	assert.NotEmpty(t, associations.EffectiveSecurityRules)

	// --- topology ---------------------------------------------------------
	topology, err := watcherClient.GetTopology(ctx, rg, "NetworkWatcher_eastus", armnetwork.TopologyParameters{
		TargetResourceGroupName: to.Ptr(rg),
	}, nil)
	require.NoError(t, err)
	byID := map[string]*armnetwork.TopologyResource{}
	for _, resource := range topology.Resources {
		byID[*resource.ID] = resource
	}
	vnetNode := byID[*subnet.ID]
	require.NotNil(t, vnetNode, "the subnet must appear in the topology")
	containsNIC := false
	for _, association := range vnetNode.Associations {
		if *association.ResourceID == *nic.ID && *association.AssociationType == armnetwork.AssociationTypeContains {
			containsNIC = true
		}
	}
	assert.True(t, containsNIC, "the subnet must be reported as containing the interface")

	// --- connectivity check ----------------------------------------------
	// A destination that answers is reachable; the check opens a real
	// connection and reports the latency it measured.
	reachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer reachable.Close()
	connectivityPoller, err := watcherClient.BeginCheckConnectivity(ctx, rg, "NetworkWatcher_eastus", armnetwork.ConnectivityParameters{
		Source:      &armnetwork.ConnectivitySource{ResourceID: nic.ID},
		Destination: &armnetwork.ConnectivityDestination{Address: to.Ptr("127.0.0.1"), Port: to.Ptr(testServerPort(t, reachable.URL))},
		Protocol:    to.Ptr(armnetwork.ProtocolTCP),
	}, nil)
	require.NoError(t, err)
	connectivity, err := connectivityPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armnetwork.ConnectionStatusConnected, *connectivity.ConnectionStatus)
	assert.Equal(t, int32(3), *connectivity.ProbesSent)
	assert.Equal(t, int32(0), *connectivity.ProbesFailed)
	require.Len(t, connectivity.Hops, 2)

	// A port nothing listens on is unreachable, and the failure is reported as
	// probes that failed rather than as a security rule.
	unreachablePort := freeLocalPort(t)
	unreachablePoller, err := watcherClient.BeginCheckConnectivity(ctx, rg, "NetworkWatcher_eastus", armnetwork.ConnectivityParameters{
		Source:      &armnetwork.ConnectivitySource{ResourceID: nic.ID},
		Destination: &armnetwork.ConnectivityDestination{Address: to.Ptr("127.0.0.1"), Port: to.Ptr(unreachablePort)},
		Protocol:    to.Ptr(armnetwork.ProtocolTCP),
	}, nil)
	require.NoError(t, err)
	unreachable, err := unreachablePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armnetwork.ConnectionStatusDisconnected, *unreachable.ConnectionStatus)
	assert.Equal(t, int32(3), *unreachable.ProbesFailed)

	// --- network configuration diagnostic --------------------------------
	diagnosticPoller, err := watcherClient.BeginGetNetworkConfigurationDiagnostic(ctx, rg, "NetworkWatcher_eastus",
		armnetwork.ConfigurationDiagnosticParameters{
			TargetResourceID: nic.ID,
			Profiles: []*armnetwork.ConfigurationDiagnosticProfile{{
				Direction:       to.Ptr(armnetwork.DirectionInbound),
				Protocol:        to.Ptr("TCP"),
				Source:          to.Ptr("198.51.100.7"),
				Destination:     to.Ptr(nicAddress),
				DestinationPort: to.Ptr("22"),
			}},
		}, nil)
	require.NoError(t, err)
	diagnostic, err := diagnosticPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Len(t, diagnostic.Results, 1)
	result := diagnostic.Results[0]
	require.NotNil(t, result.NetworkSecurityGroupResult)
	assert.Equal(t, armnetwork.SecurityRuleAccessDeny, *result.NetworkSecurityGroupResult.SecurityRuleAccessResult)
	require.Len(t, result.NetworkSecurityGroupResult.EvaluatedNetworkSecurityGroups, 1)
	evaluated := result.NetworkSecurityGroupResult.EvaluatedNetworkSecurityGroups[0]
	assert.Equal(t, *nsg.ID, *evaluated.NetworkSecurityGroupID)
	assert.Equal(t, *subnet.ID, *evaluated.AppliedTo)
	assert.Equal(t, "deny-ssh", *evaluated.MatchedRule.RuleName)
	assert.NotEmpty(t, evaluated.RulesEvaluationResult)

	// --- lifecycle --------------------------------------------------------
	tagged, err := watcherClient.UpdateTags(ctx, rg, "NetworkWatcher_eastus", armnetwork.TagsObject{
		Tags: map[string]*string{"team": to.Ptr("network"), "env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "test", *tagged.Tags["env"])
	assert.Contains(t, networkWatcherNames(t, watcherClient, rg), "NetworkWatcher_eastus")

	all := map[string]bool{}
	allPager := watcherClient.NewListAllPager(nil)
	for allPager.More() {
		page, err := allPager.NextPage(ctx)
		require.NoError(t, err)
		for _, w := range page.Value {
			all[*w.Name] = true
		}
	}
	assert.True(t, all["NetworkWatcher_eastus"])
}

// TestNetworkWatcher_FlowLogsAndConnectionMonitors covers the two configuration
// children of a watcher and the target-addressed spelling of the flow log
// configuration, which reads and writes the same record the flow log resource
// serves.
func TestNetworkWatcher_FlowLogsAndConnectionMonitors(t *testing.T) {
	rg := "netwatcher-config-rg"
	requireResourceGroup(t, rg)

	nsgClient, err := armnetwork.NewSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nsgPoller, err := nsgClient.BeginCreateOrUpdate(ctx, rg, "flow-nsg", armnetwork.SecurityGroup{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.SecurityGroupPropertiesFormat{},
	}, nil)
	require.NoError(t, err)
	nsg, err := nsgPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	watcherClient, err := armnetwork.NewWatchersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = watcherClient.CreateOrUpdate(ctx, rg, "NetworkWatcher_config", armnetwork.Watcher{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.WatcherPropertiesFormat{},
	}, nil)
	require.NoError(t, err)

	storageID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/flowlogs",
		subscriptionID, rg)

	flowClient, err := armnetwork.NewFlowLogsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	flowPoller, err := flowClient.BeginCreateOrUpdate(ctx, rg, "NetworkWatcher_config", "nsg-flows", armnetwork.FlowLog{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.FlowLogPropertiesFormat{
			TargetResourceID: nsg.ID,
			StorageID:        to.Ptr(storageID),
			Enabled:          to.Ptr(true),
			RetentionPolicy:  &armnetwork.RetentionPolicyParameters{Days: to.Ptr[int32](7), Enabled: to.Ptr(true)},
		},
	}, nil)
	require.NoError(t, err)
	flowLog, err := flowPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armnetwork.ProvisioningStateSucceeded, *flowLog.Properties.ProvisioningState)
	// The resource provider fills in the record shape a flow log is written in
	// when the request leaves it out.
	require.NotNil(t, flowLog.Properties.Format)
	assert.Equal(t, armnetwork.FlowLogFormatTypeJSON, *flowLog.Properties.Format.Type)

	// The target-addressed query reads the very record the resource wrote.
	statusPoller, err := watcherClient.BeginGetFlowLogStatus(ctx, rg, "NetworkWatcher_config",
		armnetwork.FlowLogStatusParameters{TargetResourceID: nsg.ID}, nil)
	require.NoError(t, err)
	status, err := statusPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, storageID, *status.Properties.StorageID)
	assert.True(t, *status.Properties.Enabled)

	// And the target-addressed write changes it, which the resource reads back.
	configurePoller, err := watcherClient.BeginSetFlowLogConfiguration(ctx, rg, "NetworkWatcher_config",
		armnetwork.FlowLogInformation{
			TargetResourceID: nsg.ID,
			Properties: &armnetwork.FlowLogProperties{
				StorageID: to.Ptr(storageID),
				Enabled:   to.Ptr(false),
			},
		}, nil)
	require.NoError(t, err)
	_, err = configurePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	reread, err := flowClient.Get(ctx, rg, "NetworkWatcher_config", "nsg-flows", nil)
	require.NoError(t, err)
	assert.False(t, *reread.Properties.Enabled, "the target-addressed write must land on the flow log resource")

	tagged, err := flowClient.UpdateTags(ctx, rg, "NetworkWatcher_config", "nsg-flows", armnetwork.TagsObject{
		Tags: map[string]*string{"retention": to.Ptr("short")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "short", *tagged.Tags["retention"])

	var flowLogNames []string
	flowPager := flowClient.NewListPager(rg, "NetworkWatcher_config", nil)
	for flowPager.More() {
		page, err := flowPager.NextPage(ctx)
		require.NoError(t, err)
		for _, f := range page.Value {
			flowLogNames = append(flowLogNames, *f.Name)
		}
	}
	assert.Equal(t, []string{"nsg-flows"}, flowLogNames)

	// A flow log whose target does not exist is refused.
	_, err = flowClient.BeginCreateOrUpdate(ctx, rg, "NetworkWatcher_config", "absent-target", armnetwork.FlowLog{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.FlowLogPropertiesFormat{
			TargetResourceID: to.Ptr(fmt.Sprintf(
				"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkSecurityGroups/absent",
				subscriptionID, rg)),
			StorageID: to.Ptr(storageID),
		},
	}, nil)
	require.Error(t, err, "a flow log whose target does not exist must be rejected")

	// --- connection monitors ---------------------------------------------
	monitorClient, err := armnetwork.NewConnectionMonitorsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	monitorPoller, err := monitorClient.BeginCreateOrUpdate(ctx, rg, "NetworkWatcher_config", "edge-probe",
		armnetwork.ConnectionMonitor{
			Location: to.Ptr("eastus"),
			Properties: &armnetwork.ConnectionMonitorParameters{
				Endpoints: []*armnetwork.ConnectionMonitorEndpoint{
					{Name: to.Ptr("source"), Address: to.Ptr("10.70.1.4")},
					{Name: to.Ptr("target"), Address: to.Ptr("example.internal")},
				},
				TestConfigurations: []*armnetwork.ConnectionMonitorTestConfiguration{{
					Name:               to.Ptr("tcp-443"),
					Protocol:           to.Ptr(armnetwork.ConnectionMonitorTestConfigurationProtocolTCP),
					TestFrequencySec:   to.Ptr[int32](30),
					TCPConfiguration:   &armnetwork.ConnectionMonitorTCPConfiguration{Port: to.Ptr[int32](443)},
					PreferredIPVersion: to.Ptr(armnetwork.PreferredIPVersionIPv4),
				}},
				TestGroups: []*armnetwork.ConnectionMonitorTestGroup{{
					Name:               to.Ptr("group1"),
					Sources:            []*string{to.Ptr("source")},
					Destinations:       []*string{to.Ptr("target")},
					TestConfigurations: []*string{to.Ptr("tcp-443")},
				}},
			},
		}, nil)
	require.NoError(t, err)
	monitor, err := monitorPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armnetwork.ProvisioningStateSucceeded, *monitor.Properties.ProvisioningState)
	assert.Equal(t, armnetwork.ConnectionMonitorTypeMultiEndpoint, *monitor.Properties.ConnectionMonitorType)
	assert.Equal(t, "Running", *monitor.Properties.MonitoringStatus)
	require.NotNil(t, monitor.Properties.StartTime)

	stopPoller, err := monitorClient.BeginStop(ctx, rg, "NetworkWatcher_config", "edge-probe", nil)
	require.NoError(t, err)
	_, err = stopPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	stopped, err := monitorClient.Get(ctx, rg, "NetworkWatcher_config", "edge-probe", nil)
	require.NoError(t, err)
	assert.Equal(t, "Stopped", *stopped.Properties.MonitoringStatus)

	monitorTagged, err := monitorClient.UpdateTags(ctx, rg, "NetworkWatcher_config", "edge-probe", armnetwork.TagsObject{
		Tags: map[string]*string{"owner": to.Ptr("network")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "network", *monitorTagged.Tags["owner"])

	var monitorNames []string
	monitorPager := monitorClient.NewListPager(rg, "NetworkWatcher_config", nil)
	for monitorPager.More() {
		page, err := monitorPager.NextPage(ctx)
		require.NoError(t, err)
		for _, m := range page.Value {
			monitorNames = append(monitorNames, *m.Name)
		}
	}
	assert.Equal(t, []string{"edge-probe"}, monitorNames)

	monitorDelete, err := monitorClient.BeginDelete(ctx, rg, "NetworkWatcher_config", "edge-probe", nil)
	require.NoError(t, err)
	_, err = monitorDelete.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = monitorClient.Get(ctx, rg, "NetworkWatcher_config", "edge-probe", nil)
	require.Error(t, err)

	flowDelete, err := flowClient.BeginDelete(ctx, rg, "NetworkWatcher_config", "nsg-flows", nil)
	require.NoError(t, err)
	_, err = flowDelete.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestNetworkWatcher_ReportsWhatItCannotObserve pins the two answers the
// watcher gives for state it genuinely has none of: troubleshooting a resource
// type this deployment does not hold, and the internet-provider measurements
// Azure collects with its own fleet.
func TestNetworkWatcher_ReportsWhatItCannotObserve(t *testing.T) {
	rg := "netwatcher-gaps-rg"
	requireResourceGroup(t, rg)
	watcherClient, err := armnetwork.NewWatchersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = watcherClient.CreateOrUpdate(ctx, rg, "NetworkWatcher_gaps", armnetwork.Watcher{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.WatcherPropertiesFormat{},
	}, nil)
	require.NoError(t, err)

	gatewayID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworkGateways/vpn",
		subscriptionID, rg)
	troubleshootPoller, err := watcherClient.BeginGetTroubleshooting(ctx, rg, "NetworkWatcher_gaps",
		armnetwork.TroubleshootingParameters{
			TargetResourceID: to.Ptr(gatewayID),
			Properties: &armnetwork.TroubleshootingProperties{
				StorageID:   to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Storage/storageAccounts/diag"),
				StoragePath: to.Ptr("https://diag.blob.core.windows.net/logs"),
			},
		}, nil)
	if err == nil {
		_, err = troubleshootPoller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "troubleshooting a resource this deployment does not hold must report it absent")
	assert.Contains(t, err.Error(), "ResourceNotFound")

	resultPoller, err := watcherClient.BeginGetTroubleshootingResult(ctx, rg, "NetworkWatcher_gaps",
		armnetwork.QueryTroubleshootingParameters{TargetResourceID: to.Ptr(gatewayID)}, nil)
	if err == nil {
		_, err = resultPoller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err)

	providersPoller, err := watcherClient.BeginListAvailableProviders(ctx, rg, "NetworkWatcher_gaps",
		armnetwork.AvailableProvidersListParameters{Country: to.Ptr("United States")}, nil)
	require.NoError(t, err)
	providers, err := providersPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, providers.Countries,
		"this deployment is not part of Azure's internet-measurement fleet, so it offers no providers")

	reportPoller, err := watcherClient.BeginGetAzureReachabilityReport(ctx, rg, "NetworkWatcher_gaps",
		armnetwork.AzureReachabilityReportParameters{
			ProviderLocation: &armnetwork.AzureReachabilityReportLocation{
				Country: to.Ptr("United States"),
				State:   to.Ptr("washington"),
			},
			StartTime: to.Ptr(mustParseTime(t, "2026-07-01T00:00:00Z")),
			EndTime:   to.Ptr(mustParseTime(t, "2026-07-02T00:00:00Z")),
		}, nil)
	require.NoError(t, err)
	report, err := reportPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	// The aggregation level follows from how precisely the request located the
	// providers, and it does even when there is nothing to report.
	assert.Equal(t, "State", *report.AggregationLevel)
	assert.Empty(t, report.ReachabilityReport)
}

// freeLocalPort returns a loopback port with nothing listening on it, so a
// connectivity check against it has to fail for the reason the test asserts.
func freeLocalPort(t *testing.T) int32 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return int32(port)
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return parsed
}

func verifyIPFlow(t *testing.T, client *armnetwork.WatchersClient, rg string, params armnetwork.VerificationIPFlowParameters) armnetwork.VerificationIPFlowResult {
	t.Helper()
	poller, err := client.BeginVerifyIPFlow(ctx, rg, "NetworkWatcher_eastus", params, nil)
	require.NoError(t, err)
	result, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return result.VerificationIPFlowResult
}

func nextHop(t *testing.T, client *armnetwork.WatchersClient, rg string, params armnetwork.NextHopParameters) armnetwork.NextHopResult {
	t.Helper()
	poller, err := client.BeginGetNextHop(ctx, rg, "NetworkWatcher_eastus", params, nil)
	require.NoError(t, err)
	result, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return result.NextHopResult
}

func networkWatcherNames(t *testing.T, client *armnetwork.WatchersClient, rg string) []string {
	t.Helper()
	var names []string
	pager := client.NewListPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, w := range page.Value {
			names = append(names, *w.Name)
		}
	}
	return names
}
