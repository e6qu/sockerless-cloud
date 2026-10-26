package azure_sdk_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outsideListener listens on the host's own address, which a machine reaches
// only by leaving its virtual network, and records who connected.
func outsideListener(t *testing.T) (address string, lastRemote func() string) {
	t.Helper()
	probe, err := net.Dial("udp4", "192.0.2.1:9")
	require.NoError(t, err)
	host := probe.LocalAddr().(*net.UDPAddr).IP.String()
	_ = probe.Close()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var remote string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remote, _, _ = net.SplitHostPort(r.RemoteAddr)
		mu.Unlock()
		_, _ = io.WriteString(w, "outside\n")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return net.JoinHostPort(host, port), func() string {
		mu.Lock()
		defer mu.Unlock()
		return remote
	}
}

// A machine on a subnet attached to a NAT gateway leaves its virtual network
// through the gateway: its connections arrive from the gateway, and the
// gateway's Azure Monitor metrics count them and their bytes. Detached, the
// subnet's machines leave those metrics alone.
func TestCompute_NatGatewayCarriesAMachinesConnections(t *testing.T) {
	t.Parallel()
	const rg = "vm-nat-rg"
	nicID := vmOperationsFixture(t, rg, "vm-nat", "10.254.0.0/16")

	pipClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pipPoller, err := pipClient.BeginCreateOrUpdate(ctx, rg, "vm-nat-pip", armnetwork.PublicIPAddress{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.PublicIPAddressSKU{Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard)},
		Properties: &armnetwork.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodStatic),
		},
	}, nil)
	require.NoError(t, err)
	pip, err := pipPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	natClient, err := armnetwork.NewNatGatewaysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	natPoller, err := natClient.BeginCreateOrUpdate(ctx, rg, "vm-nat-gw", armnetwork.NatGateway{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.NatGatewaySKU{Name: to.Ptr(armnetwork.NatGatewaySKUNameStandard)},
		Properties: &armnetwork.NatGatewayPropertiesFormat{
			IdleTimeoutInMinutes: to.Ptr[int32](4),
			PublicIPAddresses:    []*armnetwork.SubResource{{ID: pip.ID}},
		},
	}, nil)
	require.NoError(t, err)
	nat, err := natPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	natID := *nat.ID

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	attach := func(gateway *armnetwork.SubResource) {
		t.Helper()
		subnet, err := subnetClient.Get(ctx, rg, "vm-nat-vnet", "vm-nat-subnet", nil)
		require.NoError(t, err)
		subnet.Properties.NatGateway = gateway
		poller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, "vm-nat-vnet", "vm-nat-subnet", subnet.Subnet, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}
	attach(&armnetwork.SubResource{ID: to.Ptr(natID)})

	outside, lastRemote := outsideListener(t)
	start := time.Now().UTC().Add(-time.Minute)
	createOperationsVM(t, rg, "vm-nat", nicID, nil)
	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nic, err := nicClient.Get(ctx, rg, "vm-nat-nic", nil)
	require.NoError(t, err)
	machineIP := *nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress

	// The image has bash and no curl, so use bash's own TCP device.
	get := fmt.Sprintf(`bash -c 'if timeout 5 bash -c "exec 3<>/dev/tcp/%s && printf \"GET / HTTP/1.0\\r\\n\\r\\n\" >&3 && cat <&3 >/dev/null"; then echo reach=ok; else echo reach=failed; fi'`, strings.Replace(outside, ":", "/", 1))
	require.Contains(t, runInGuest(t, rg, "vm-nat", "reach-1", get), "reach=ok", "the machine does not reach the outside through the NAT gateway")
	assert.NotEqual(t, machineIP, lastRemote(), "the connection arrived from the machine's own address, not the gateway's")

	metrics, err := armmonitor.NewMetricsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	total := func(metric string) float64 {
		resp, err := metrics.List(ctx, natID, &armmonitor.MetricsClientListOptions{
			Metricnames: to.Ptr(metric),
			Aggregation: to.Ptr("Total"),
			Interval:    to.Ptr("PT1M"),
			Timespan:    to.Ptr(start.Format(time.RFC3339) + "/" + time.Now().UTC().Add(time.Minute).Format(time.RFC3339)),
		})
		require.NoError(t, err)
		require.Len(t, resp.Value, 1)
		require.Equal(t, metric, *resp.Value[0].Name.Value)
		sum := 0.0
		for _, series := range resp.Value[0].Timeseries {
			for _, point := range series.Data {
				if point.Total != nil {
					sum += *point.Total
				}
			}
		}
		return sum
	}
	// The gateway reports a connection's bytes when it ends, which the request's
	// close brings about; the metric follows on the cloud's own cadence.
	require.Eventually(t, func() bool { return total("ByteCount") > 0 }, time.Minute, 2*time.Second, "the gateway counted no bytes")
	assert.GreaterOrEqual(t, total("SNATConnectionCount"), 1.0)
	attempts := total("SNATConnectionCount")

	attach(nil)
	runInGuest(t, rg, "vm-nat", "reach-2", get)
	assert.Equal(t, attempts, total("SNATConnectionCount"), "a subnet detached from the gateway still counted on it")

	definitions, err := armmonitor.NewMetricDefinitionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pager := definitions.NewListPager(natID, nil)
	var names []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			names = append(names, *d.Name.Value)
		}
	}
	assert.Contains(t, names, "ByteCount")
	assert.Contains(t, names, "SNATConnectionCount")

	namespaces, err := armmonitor.NewMetricNamespacesClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	var namespaceNames []string
	for nsPager := namespaces.NewListPager(natID, nil); nsPager.More(); {
		page, err := nsPager.NextPage(ctx)
		require.NoError(t, err)
		for _, ns := range page.Value {
			namespaceNames = append(namespaceNames, *ns.Properties.MetricNamespaceName)
		}
	}
	assert.Equal(t, []string{"Microsoft.Network/natGateways"}, namespaceNames)

	// A subscription-scoped read has one series per gateway of the region, named
	// by its resource ID.
	gatewayTotal := func(resp armmonitor.Response) float64 {
		t.Helper()
		require.Len(t, resp.Value, 1)
		sum, found := 0.0, false
		for _, series := range resp.Value[0].Timeseries {
			for _, meta := range series.Metadatavalues {
				if *meta.Name.Value == "Microsoft.ResourceId" && strings.EqualFold(*meta.Value, natID) {
					found = true
					for _, point := range series.Data {
						if point.Total != nil {
							sum += *point.Total
						}
					}
				}
			}
		}
		require.True(t, found, "the subscription-scoped read has no series for %s", natID)
		return sum
	}
	span := to.Ptr(start.Format(time.RFC3339) + "/" + time.Now().UTC().Add(time.Minute).Format(time.RFC3339))
	viaGet, err := metrics.ListAtSubscriptionScope(ctx, "eastus", &armmonitor.MetricsClientListAtSubscriptionScopeOptions{
		Metricnamespace: to.Ptr("Microsoft.Network/natGateways"),
		Metricnames:     to.Ptr("ByteCount"),
		Aggregation:     to.Ptr("Total"),
		Timespan:        span,
	})
	require.NoError(t, err)
	assert.Equal(t, total("ByteCount"), gatewayTotal(viaGet.Response))
	viaPost, err := metrics.ListAtSubscriptionScopePost(ctx, "eastus", &armmonitor.MetricsClientListAtSubscriptionScopePostOptions{
		Body: &armmonitor.SubscriptionScopeMetricsRequestBodyParameters{
			MetricNamespace: to.Ptr("Microsoft.Network/natGateways"),
			MetricNames:     to.Ptr("ByteCount"),
			Aggregation:     to.Ptr("Total"),
			Timespan:        span,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, total("ByteCount"), gatewayTotal(viaPost.Response))

	var subscriptionNames []string
	for subPager := definitions.NewListAtSubscriptionScopePager("eastus", &armmonitor.MetricDefinitionsClientListAtSubscriptionScopeOptions{
		Metricnamespace: to.Ptr("Microsoft.Network/natGateways"),
	}); subPager.More(); {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			subscriptionNames = append(subscriptionNames, *d.Name.Value)
		}
	}
	assert.ElementsMatch(t, names, subscriptionNames)
}
