package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Instance Metadata Service as a machine reaches it: api-version checks,
// route parameters and the text format, the public address in the network
// section, load balancer metadata, and Scheduled Events a restart and a Spot
// eviction raise and the guest approves.

type imdsExchange struct {
	status int
	body   string
}

func (x imdsExchange) json(t *testing.T) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(x.body), &body), x.body)
	return body
}

var imdsExchangePattern = regexp.MustCompile(`(?s)HTTP/1\.[01] (\d{3})[^\n]*\n.*?\r?\n\r?\n(.*?)\n__IMDS_END__`)

// imdsGuestRequest is a shell script that sends one request from the guest
// over plain TCP and prints the response, then an end marker.
func imdsGuestRequest(method, path, body string) string {
	request := method + " " + strings.ReplaceAll(path, "%", "%%") + ` HTTP/1.0\r\nHost: 169.254.169.254\r\nMetadata: true\r\n`
	if body != "" {
		request += `Content-Type: application/json\r\nContent-Length: ` + strconv.Itoa(len(body)) + `\r\n\r\n` + strings.ReplaceAll(body, `"`, `\"`)
	} else {
		request += `\r\n`
	}
	return `host=${AZURE_INSTANCE_METADATA_ENDPOINT#http://}; host=${host%%/*}; ` +
		`exec 3<>/dev/tcp/$host/80; printf "` + request + `" >&3; cat <&3; echo; echo __IMDS_END__`
}

func parseIMDSExchange(t *testing.T, what, out string) imdsExchange {
	t.Helper()
	match := imdsExchangePattern.FindStringSubmatch(out)
	require.Len(t, match, 3, "the guest got no HTTP response for %s: %s", what, out)
	status, err := strconv.Atoi(match[1])
	require.NoError(t, err)
	return imdsExchange{status: status, body: strings.TrimRight(match[2], "\r\n")}
}

// imdsGuestExchange sends one request from the guest and returns the status
// and the body, JSON or text.
func imdsGuestExchange(t *testing.T, rg, vmName, step, method, path, body string) imdsExchange {
	t.Helper()
	out := runInGuest(t, rg, vmName, step, `bash -c '`+imdsGuestRequest(method, path, body)+`'`)
	return parseIMDSExchange(t, method+" "+path, out)
}

// imdsGuestApproveDetached approves a scheduled event from a process the
// command leaves behind, writing the response to file: the approved operation
// stops the guest, and with it a command still running.
func imdsGuestApproveDetached(t *testing.T, rg, vmName, step, eventID, file string) {
	t.Helper()
	request := imdsGuestRequest(http.MethodPost, "/metadata/scheduledevents?api-version=2020-07-01",
		`{"StartRequests":[{"EventId":"`+eventID+`"}]}`)
	runInGuest(t, rg, vmName, step, `bash -c '(`+request+`) > `+file+` 2>&1 < /dev/null &'`)
}

func TestIMDS_GuestReadsPathsVersionsLoadBalancerAndScheduledEvents(t *testing.T) {
	t.Parallel()
	const rg = "vm-imds-rg"
	const vmName = "vm-imds"
	nicID := vmOperationsFixture(t, rg, "vm-imds", "10.131.0.0/16")
	networkBase := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network"

	pipClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	publicIP := func(name string) armnetwork.PublicIPAddress {
		poller, err := pipClient.BeginCreateOrUpdate(ctx, rg, name, armnetwork.PublicIPAddress{
			Location: to.Ptr("eastus"),
			SKU:      &armnetwork.PublicIPAddressSKU{Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard)},
			Properties: &armnetwork.PublicIPAddressPropertiesFormat{
				PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodStatic),
			},
		}, nil)
		require.NoError(t, err)
		created, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		require.NotEmpty(t, derefString(created.Properties.IPAddress))
		return created.PublicIPAddress
	}
	instancePIP := publicIP("vm-imds-pip")
	frontendPIP := publicIP("vm-imds-lb-pip")

	lbID := networkBase + "/loadBalancers/vm-imds-lb"
	lbClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	lbPoller, err := lbClient.BeginCreateOrUpdate(ctx, rg, "vm-imds-lb", armnetwork.LoadBalancer{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.LoadBalancerSKU{Name: to.Ptr(armnetwork.LoadBalancerSKUNameStandard)},
		Properties: &armnetwork.LoadBalancerPropertiesFormat{
			FrontendIPConfigurations: []*armnetwork.FrontendIPConfiguration{{
				Name:       to.Ptr("fe"),
				Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{PublicIPAddress: &armnetwork.PublicIPAddress{ID: frontendPIP.ID}},
			}},
			BackendAddressPools: []*armnetwork.BackendAddressPool{{Name: to.Ptr("pool")}},
			LoadBalancingRules: []*armnetwork.LoadBalancingRule{{
				Name: to.Ptr("http"),
				Properties: &armnetwork.LoadBalancingRulePropertiesFormat{
					Protocol:                to.Ptr(armnetwork.TransportProtocolTCP),
					FrontendPort:            to.Ptr[int32](80),
					BackendPort:             to.Ptr[int32](8080),
					FrontendIPConfiguration: &armnetwork.SubResource{ID: to.Ptr(lbID + "/frontendIPConfigurations/fe")},
					BackendAddressPool:      &armnetwork.SubResource{ID: to.Ptr(lbID + "/backendAddressPools/pool")},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = lbPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// The interface's IP configuration takes the instance public address and
	// joins the load balancer's backend pool.
	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nic, err := nicClient.Get(ctx, rg, "vm-imds-nic", nil)
	require.NoError(t, err)
	ipcfg := nic.Properties.IPConfigurations[0].Properties
	ipcfg.PublicIPAddress = &armnetwork.PublicIPAddress{ID: instancePIP.ID}
	ipcfg.LoadBalancerBackendAddressPools = []*armnetwork.BackendAddressPool{{ID: to.Ptr(lbID + "/backendAddressPools/pool")}}
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, "vm-imds-nic", nic.Interface, nil)
	require.NoError(t, err)
	_, err = nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, nicID, derefString(nic.ID))

	vmClient := createOperationsVM(t, rg, vmName, nicID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.Priority = to.Ptr(armcompute.VirtualMachinePriorityTypesSpot)
		vm.Properties.EvictionPolicy = to.Ptr(armcompute.VirtualMachineEvictionPolicyTypesDeallocate)
	})
	vm, err := vmClient.Get(ctx, rg, vmName, nil)
	require.NoError(t, err)
	private := derefString(ipcfg.PrivateIPAddress)
	require.NotEmpty(t, private)
	get := func(step, path string) imdsExchange {
		return imdsGuestExchange(t, rg, vmName, step, http.MethodGet, path, "")
	}

	t.Run("an absent api-version answers the newest versions", func(t *testing.T) {
		missing := get("no-version", "/metadata/instance")
		require.Equal(t, http.StatusBadRequest, missing.status, missing.body)
		body := missing.json(t)
		assert.Equal(t, "Bad request. api-version is invalid or was not specified in the request. For more information refer to aka.ms/azureimds", body["error"])
		assert.Equal(t, []any{"2025-04-07", "2024-07-17", "2024-03-15"}, body["newest-versions"])
		versions := get("versions", "/metadata/versions")
		require.Equal(t, http.StatusOK, versions.status, versions.body)
		assert.Contains(t, versions.json(t)["apiVersions"], "2021-02-01")
	})

	t.Run("route parameters reach a leaf as text", func(t *testing.T) {
		vmID := get("vmid", "/metadata/instance/compute/vmId?api-version=2025-04-07&format=text")
		require.Equal(t, http.StatusOK, vmID.status, vmID.body)
		assert.Equal(t, derefString(vm.Properties.VMID), vmID.body)
		leafAsJSON := get("vmid-json", "/metadata/instance/compute/vmId?api-version=2025-04-07")
		assert.Equal(t, http.StatusBadRequest, leafAsJSON.status, leafAsJSON.body)
		absent := get("absent", "/metadata/instance/compute/nosuch?api-version=2025-04-07&format=text")
		assert.Equal(t, http.StatusNotFound, absent.status, absent.body)
		publicAddress := get("public-ip", "/metadata/instance/network/interface/0/ipv4/ipAddress/0/publicIpAddress?api-version=2025-04-07&format=text")
		require.Equal(t, http.StatusOK, publicAddress.status, publicAddress.body)
		assert.Equal(t, derefString(instancePIP.Properties.IPAddress), publicAddress.body)
	})

	t.Run("load balancer metadata lists the rules that reach the machine", func(t *testing.T) {
		lb := get("loadbalancer", "/metadata/loadbalancer?api-version=2020-10-01")
		require.Equal(t, http.StatusOK, lb.status, lb.body)
		var got struct {
			LoadBalancer struct {
				PublicIPAddresses []map[string]any `json:"publicIpAddresses"`
				InboundRules      []map[string]any `json:"inboundRules"`
				OutboundRules     []map[string]any `json:"outboundRules"`
			} `json:"loadbalancer"`
		}
		require.NoError(t, json.Unmarshal([]byte(lb.body), &got), lb.body)
		assert.Equal(t, []map[string]any{{
			"frontendIpAddress": derefString(instancePIP.Properties.IPAddress), "privateIpAddress": private,
		}}, got.LoadBalancer.PublicIPAddresses)
		assert.Equal(t, []map[string]any{{
			"frontendIpAddress": derefString(frontendPIP.Properties.IPAddress), "protocol": "tcp",
			"frontendPort": float64(80), "backendPort": float64(8080), "privateIpAddress": private,
		}}, got.LoadBalancer.InboundRules)
		assert.Empty(t, got.LoadBalancer.OutboundRules)
	})

	scheduledEvents := func(step string) (int, []map[string]any) {
		x := get(step, "/metadata/scheduledevents?api-version=2020-07-01")
		require.Equal(t, http.StatusOK, x.status, x.body)
		var doc struct {
			DocumentIncarnation int
			Events              []map[string]any
		}
		require.NoError(t, json.Unmarshal([]byte(x.body), &doc), x.body)
		return doc.DocumentIncarnation, doc.Events
	}
	// The approval's response survives on the OS disk, which a restart and a
	// deallocation keep.
	approvalStatus := func(step, file string) int {
		return parseIMDSExchange(t, "the approval", runInGuest(t, rg, vmName, step, "cat "+file)).status
	}

	t.Run("a restart waits for the guest to approve its Reboot event", func(t *testing.T) {
		first, events := scheduledEvents("se-first")
		require.Empty(t, events)
		poller, err := vmClient.BeginRestart(ctx, rg, vmName, nil)
		require.NoError(t, err)
		scheduled, events := scheduledEvents("se-scheduled")
		require.Len(t, events, 1)
		assert.Equal(t, first+1, scheduled)
		event := events[0]
		assert.Equal(t, "Reboot", event["EventType"])
		assert.Equal(t, "Scheduled", event["EventStatus"])
		assert.Equal(t, "User", event["EventSource"])
		assert.Equal(t, []any{vmName}, event["Resources"])
		notBefore, err := time.Parse(http.TimeFormat, event["NotBefore"].(string))
		require.NoError(t, err)
		assert.True(t, notBefore.After(time.Now().Add(10*time.Minute)), "NotBefore %v gives less than the 15 minutes' notice a Reboot gets", notBefore)
		imdsGuestApproveDetached(t, rg, vmName, "se-approve", event["EventId"].(string), "/var/tmp/se-reboot-approval")
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, approvalStatus("se-reboot-approval", "/var/tmp/se-reboot-approval"))
		done, events := scheduledEvents("se-done")
		assert.Empty(t, events)
		assert.Equal(t, first+3, done, "scheduling, starting and ending the event each move the incarnation")
	})

	t.Run("a Spot eviction raises a Preempt event", func(t *testing.T) {
		_, err := vmClient.SimulateEviction(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, events := scheduledEvents("se-preempt")
		require.Len(t, events, 1)
		assert.Equal(t, "Preempt", events[0]["EventType"])
		assert.Equal(t, "Platform", events[0]["EventSource"])
		imdsGuestApproveDetached(t, rg, vmName, "se-preempt-approve", events[0]["EventId"].(string), "/var/tmp/se-preempt-approval")
		deadline := time.Now().Add(5 * time.Minute)
		for {
			view, err := vmClient.InstanceView(ctx, rg, vmName, nil)
			require.NoError(t, err)
			if containsVMStatus(view.Statuses, "PowerState/deallocated") {
				break
			}
			require.True(t, time.Now().Before(deadline), "the evicted machine was never deallocated")
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
		start, err := vmClient.BeginStart(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, err = start.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, approvalStatus("se-preempt-approval", "/var/tmp/se-preempt-approval"))
		_, events = scheduledEvents("se-after-preempt")
		assert.Empty(t, events)
	})
}
