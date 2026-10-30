package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// The Instance Metadata Service rules from the vendored Learn pages, read by a
// machine registered at a guest address the way a booted machine is.

const imdsTestNetwork = "/subscriptions/" + vmOpsSubscription + "/resourceGroups/" + vmOpsResourceGroup + "/providers/Microsoft.Network"

// putIMDSMachine stores a running machine with one interface and registers it
// at address, as azureVMBooting does.
func putIMDSMachine(t *testing.T, name, address string, mutateVM func(*VirtualMachine), mutateNIC func(*NetworkInterface)) VirtualMachine {
	t.Helper()
	nicID := imdsTestNetwork + "/networkInterfaces/" + name + "-nic"
	nic := NetworkInterface{
		ID:   nicID,
		Name: name + "-nic",
		Type: "Microsoft.Network/networkInterfaces",
		Properties: NetworkInterfaceProperties{
			MacAddress: "00-0D-3A-00-00-01",
			IPConfigurations: []NetworkInterfaceIPConfiguration{{
				Name: "ipconfig1",
				Properties: NetworkInterfaceIPConfigurationProperties{
					PrivateIPAddress: address,
					Primary:          true,
				},
			}},
		},
	}
	if mutateNIC != nil {
		mutateNIC(&nic)
	}
	azureNICs.Put(nicID, nic)
	vm := putVMOpsMachine(t, name, "eastus", func(vm *VirtualMachine) {
		vm.Properties.VMID = sim.NewUUID()
		vm.Properties.UserData = "dXNlci1kYXRh"
		vm.Tags = map[string]string{"Department": "IT", "TestStatus": "Pending"}
		vm.Properties.NetworkProfile.NetworkInterfaces = []VMNetworkInterfaceRef{{ID: nicID}}
		if mutateVM != nil {
			mutateVM(vm)
		}
	})
	azureVMStates.Put(vm.ID, "PowerState/running")
	azureMetadataVMsByIP.Store(address, azureMetadataVM{VM: vm, NIC: nic})
	t.Cleanup(func() {
		azureMetadataVMsByIP.Delete(address)
		azureNICs.Delete(nicID)
		azureForgetScheduledEvents(vm.ID)
	})
	return vm
}

func imdsRequest(t *testing.T, srv *sim.Server, address, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = address + ":49152"
	req.Header.Set("Metadata", "true")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func imdsJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestIMDSRequiresAServedAPIVersion(t *testing.T) {
	srv := vmOpsSimulator(t)
	const address = "192.0.2.41"
	putIMDSMachine(t, "imds-version-vm", address, nil, nil)

	for _, tc := range []struct {
		target string
		newest []any
	}{
		{"/metadata/instance", []any{"2025-04-07", "2024-07-17", "2024-03-15"}},
		{"/metadata/instance/compute?api-version=2016-01-01", []any{"2025-04-07", "2024-07-17", "2024-03-15"}},
		{"/metadata/attested/document?api-version=2018-04-02", []any{"2025-04-07", "2024-07-17", "2024-03-15"}},
		{"/metadata/loadbalancer?api-version=2020-09-01", []any{"2025-04-07", "2024-07-17", "2024-03-15"}},
		{"/metadata/scheduledevents", []any{"2020-07-01", "2019-08-01", "2019-04-01"}},
	} {
		rec := imdsRequest(t, srv, address, http.MethodGet, tc.target, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400: %s", tc.target, rec.Code, rec.Body.String())
		}
		body := imdsJSON(t, rec)
		if body["error"] != "Bad request. api-version is invalid or was not specified in the request. For more information refer to aka.ms/azureimds" {
			t.Errorf("%s: error = %v", tc.target, body["error"])
		}
		if !reflect.DeepEqual(body["newest-versions"], tc.newest) {
			t.Errorf("%s: newest-versions = %v, want %v", tc.target, body["newest-versions"], tc.newest)
		}
	}

	if rec := imdsRequest(t, srv, address, http.MethodGet, "/metadata/instance?api-version=2021-02-01&api-version=2021-02-01", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("a repeated query parameter: status %d, want 400", rec.Code)
	}

	versions := imdsRequest(t, srv, address, http.MethodGet, "/metadata/versions", "")
	if versions.Code != http.StatusOK {
		t.Fatalf("versions: status %d: %s", versions.Code, versions.Body.String())
	}
	list, _ := imdsJSON(t, versions)["apiVersions"].([]any)
	if len(list) != len(azureIMDSVersions) || list[0] != "2017-03-01" || list[len(list)-1] != "2025-04-07" {
		t.Errorf("apiVersions = %v, want the supported versions oldest first", list)
	}
}

func TestIMDSRouteParametersAndTextFormat(t *testing.T) {
	srv := vmOpsSimulator(t)
	const address = "192.0.2.42"
	pipID := imdsTestNetwork + "/publicIPAddresses/imds-route-pip"
	azurePublicIPs.Put(pipID, PublicIPAddress{ID: pipID, Name: "imds-route-pip", Properties: PublicIPAddressProperties{PublicIPAddress: "198.51.100.42"}})
	t.Cleanup(func() { azurePublicIPs.Delete(pipID) })
	vm := putIMDSMachine(t, "imds-route-vm", address, nil, func(nic *NetworkInterface) {
		nic.Properties.IPConfigurations[0].Properties.PublicIPAddress = &SubResource{ID: pipID}
		nic.Properties.IPConfigurations = append(nic.Properties.IPConfigurations, NetworkInterfaceIPConfiguration{
			Name:       "ipconfig2",
			Properties: NetworkInterfaceIPConfigurationProperties{PrivateIPAddress: "192.0.2.142"},
		})
	})

	get := func(target string) *httptest.ResponseRecorder {
		return imdsRequest(t, srv, address, http.MethodGet, target, "")
	}
	if rec := get("/metadata/instance/compute/vmId?api-version=2025-04-07&format=text"); rec.Code != http.StatusOK || rec.Body.String() != vm.Properties.VMID ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("vmId as text: %d %q (%s), want the machine's vmId", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	if rec := get("/metadata/instance/compute/tags?api-version=2025-04-07&format=text"); rec.Body.String() != "Department:IT;TestStatus:Pending" {
		t.Errorf("tags as text = %q", rec.Body.String())
	}
	if rec := get("/metadata/instance/compute/vmId?api-version=2025-04-07"); rec.Code != http.StatusBadRequest {
		t.Errorf("a leaf without format=text: status %d, want 400", rec.Code)
	}
	if rec := get("/metadata/instance/compute/vmId?api-version=2025-04-07&format=xml"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown format: status %d, want 400", rec.Code)
	}
	tagsList := get("/metadata/instance/compute/tagsList?api-version=2025-04-07")
	var tags []map[string]string
	if err := json.Unmarshal(tagsList.Body.Bytes(), &tags); err != nil || len(tags) != 2 || tags[0]["name"] != "Department" {
		t.Errorf("tagsList = %s (%v)", tagsList.Body.String(), err)
	}
	if rec := get("/metadata/instance/network/interface/0/ipv4/ipAddress/0/publicIpAddress?api-version=2025-04-07&format=text"); rec.Body.String() != "198.51.100.42" {
		t.Errorf("the primary configuration's public address = %d %q, want 198.51.100.42", rec.Code, rec.Body.String())
	}
	if rec := get("/metadata/instance/network/interface/0/ipv4/ipAddress/1/privateIpAddress?api-version=2025-04-07&format=text"); rec.Body.String() != "192.0.2.142" {
		t.Errorf("the secondary configuration's private address = %q", rec.Body.String())
	}
	if rec := get("/metadata/instance/network/interface/0/ipv4/ipAddress/1/publicIpAddress?api-version=2025-04-07&format=text"); rec.Code != http.StatusOK || rec.Body.String() != "" {
		t.Errorf("a configuration without a public address: %d %q, want 200 and empty", rec.Code, rec.Body.String())
	}
	for _, missing := range []string{"/metadata/instance/compute/nosuch", "/metadata/instance/network/interface/3", "/metadata/instance/compute/vmId/more"} {
		if rec := get(missing + "?api-version=2025-04-07&format=text"); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", missing, rec.Code)
		}
	}
	if rec := get("/metadata/instance?api-version=2025-04-07&format=text"); rec.Body.String() != "compute/\nnetwork/" {
		t.Errorf("the document as text = %q", rec.Body.String())
	}

	older := imdsJSON(t, get("/metadata/instance/compute?api-version=2017-08-01"))
	for _, absent := range []string{"userData", "resourceId", "azEnvironment", "osProfile", "tagsList"} {
		if _, ok := older[absent]; ok {
			t.Errorf("2017-08-01 answers %s, which a later version introduced", absent)
		}
	}
	if older["subscriptionId"] != vmOpsSubscription {
		t.Errorf("2017-08-01 subscriptionId = %v", older["subscriptionId"])
	}
}

func TestIMDSLoadBalancerMetadata(t *testing.T) {
	srv := vmOpsSimulator(t)
	const address = "192.0.2.43"
	lbID := imdsTestNetwork + "/loadBalancers/imds-lb"
	basicID := imdsTestNetwork + "/loadBalancers/imds-basic-lb"
	frontendPIP := imdsTestNetwork + "/publicIPAddresses/imds-lb-pip"
	instancePIP := imdsTestNetwork + "/publicIPAddresses/imds-instance-pip"
	azurePublicIPs.Put(frontendPIP, PublicIPAddress{ID: frontendPIP, Properties: PublicIPAddressProperties{PublicIPAddress: "203.0.113.10"}})
	azurePublicIPs.Put(instancePIP, PublicIPAddress{ID: instancePIP, Properties: PublicIPAddressProperties{PublicIPAddress: "203.0.113.43"}})
	lb := func(id, sku string) LoadBalancer {
		return LoadBalancer{
			ID:  id,
			Sku: &SkuName{Name: sku},
			Properties: LoadBalancerProperties{
				FrontendIPConfigurations: []LoadBalancerChild{{
					ID:         id + "/frontendIPConfigurations/fe",
					Properties: map[string]any{"publicIPAddress": map[string]any{"id": frontendPIP}},
				}},
				BackendAddressPools: []LoadBalancerChild{{ID: id + "/backendAddressPools/pool"}},
				LoadBalancingRules: []LoadBalancerChild{{
					ID: id + "/loadBalancingRules/http",
					Properties: map[string]any{
						"frontendIPConfiguration": map[string]any{"id": id + "/frontendIPConfigurations/fe"},
						"backendAddressPool":      map[string]any{"id": id + "/backendAddressPools/pool"},
						"protocol":                "Tcp",
						"frontendPort":            float64(80),
						"backendPort":             float64(8080),
					},
				}},
			},
		}
	}
	azureLBs.Put(lbID, lb(lbID, "Standard"))
	azureLBs.Put(basicID, lb(basicID, "Basic"))
	t.Cleanup(func() {
		azureLBs.Delete(lbID)
		azureLBs.Delete(basicID)
		azurePublicIPs.Delete(frontendPIP)
		azurePublicIPs.Delete(instancePIP)
	})
	putIMDSMachine(t, "imds-lb-vm", address, nil, func(nic *NetworkInterface) {
		nic.Properties.IPConfigurations[0].Properties.PublicIPAddress = &SubResource{ID: instancePIP}
		nic.Properties.IPConfigurations[0].Properties.LoadBalancerBackendAddressPools = []LoadBalancerChild{{ID: lbID + "/backendAddressPools/pool"}}
	})
	putIMDSMachine(t, "imds-basic-vm", "192.0.2.44", nil, func(nic *NetworkInterface) {
		nic.Properties.IPConfigurations[0].Properties.LoadBalancerBackendAddressPools = []LoadBalancerChild{{ID: basicID + "/backendAddressPools/pool"}}
	})

	rec := imdsRequest(t, srv, address, http.MethodGet, "/metadata/loadbalancer?api-version=2020-10-01", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("loadbalancer: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		LoadBalancer azureIMDSLoadBalancerMetadata `json:"loadbalancer"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := azureIMDSLoadBalancerMetadata{
		PublicIPAddresses: []azureIMDSLoadBalancerAddress{{FrontendIPAddress: "203.0.113.43", PrivateIPAddress: address}},
		InboundRules: []azureIMDSLoadBalancerInboundRule{{
			FrontendIPAddress: "203.0.113.10", Protocol: "tcp", FrontendPort: 80, BackendPort: 8080, PrivateIPAddress: address,
		}},
		OutboundRules: []azureIMDSLoadBalancerAddress{},
	}
	if !reflect.DeepEqual(got.LoadBalancer, want) {
		t.Errorf("loadbalancer = %+v, want %+v", got.LoadBalancer, want)
	}
	if port := imdsRequest(t, srv, address, http.MethodGet, "/metadata/loadbalancer/loadbalancer/inboundRules/0/frontendPort?api-version=2020-10-01&format=text", ""); port.Body.String() != "80" {
		t.Errorf("the rule's frontend port as text = %q", port.Body.String())
	}

	basic := imdsRequest(t, srv, "192.0.2.44", http.MethodGet, "/metadata/loadbalancer?api-version=2020-10-01", "")
	if !strings.Contains(basic.Body.String(), `"inboundRules":[]`) {
		t.Errorf("a machine behind a Basic load balancer reads %s, want no rules", basic.Body.String())
	}
}

func scheduledEventsDocument(t *testing.T, srv *sim.Server, address, version string) (int, []map[string]any) {
	t.Helper()
	rec := imdsRequest(t, srv, address, http.MethodGet, "/metadata/scheduledevents?api-version="+version, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("scheduledevents: %d %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		DocumentIncarnation int
		Events              []map[string]any
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.DocumentIncarnation, doc.Events
}

func approveScheduledEvent(t *testing.T, srv *sim.Server, address, eventID string) {
	t.Helper()
	rec := imdsRequest(t, srv, address, http.MethodPost, "/metadata/scheduledevents?api-version=2020-07-01",
		`{"StartRequests":[{"EventId":"`+eventID+`"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve %s: %d %s", eventID, rec.Code, rec.Body.String())
	}
}

// A restart of a machine that asked for its events schedules a Reboot event
// and waits for the guest's approval; the event reads Started while the
// machine restarts and leaves the document when it is back.
func TestScheduledEventsHoldARestartUntilApproved(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	const address = "192.0.2.45"
	vm := putIMDSMachine(t, "se-restart-vm", address, nil, nil)

	incarnation, events := scheduledEventsDocument(t, srv, address, "2020-07-01")
	if incarnation != 1 || len(events) != 0 {
		t.Fatalf("a first read = %d %v, want incarnation 1 and no events", incarnation, events)
	}

	before := time.Now().UTC()
	if rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("se-restart-vm", "restart"), ""); rec.Code != http.StatusAccepted {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body.String())
	}
	incarnation, events = scheduledEventsDocument(t, srv, address, "2020-07-01")
	if incarnation != 2 || len(events) != 1 {
		t.Fatalf("after the restart request: %d %v, want incarnation 2 and one event", incarnation, events)
	}
	event := events[0]
	for key, want := range map[string]any{
		"EventType": "Reboot", "ResourceType": "VirtualMachine", "EventStatus": "Scheduled", "EventSource": "User",
		"Description":       "Virtual machine is going to be restarted as requested by authorized user.",
		"DurationInSeconds": float64(-1), "Resources": []any{"se-restart-vm"},
	} {
		if !reflect.DeepEqual(event[key], want) {
			t.Errorf("%s = %v, want %v", key, event[key], want)
		}
	}
	notBefore, err := time.Parse(http.TimeFormat, event["NotBefore"].(string))
	if err != nil || notBefore.Before(before.Add(15*time.Minute-time.Second)) {
		t.Errorf("NotBefore = %v (%v), want at least 15 minutes ahead", event["NotBefore"], err)
	}
	if _, power := vmLROInstanceView(t, srv, "se-restart-vm"); power.Code != "PowerState/running" {
		t.Errorf("the machine reads %q while its event is scheduled, want running", power.Code)
	}
	select {
	case id := <-hooks.halting:
		t.Fatalf("%s halted before its event was approved", id)
	default:
	}

	if rec := imdsRequest(t, srv, address, http.MethodPost, "/metadata/scheduledevents?api-version=2020-07-01",
		`{"StartRequests":[{"EventId":"`+sim.NewUUID()+`"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("approving an unknown event: %d, want 400", rec.Code)
	}
	if rec := imdsRequest(t, srv, address, http.MethodPost, "/metadata/scheduledevents?api-version=2020-07-01", `{"StartRequests":`); rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed payload: %d, want 400", rec.Code)
	}
	approveScheduledEvent(t, srv, address, event["EventId"].(string))
	hooks.awaitHalt(t, vm.ID)
	approveScheduledEvent(t, srv, address, event["EventId"].(string))

	incarnation, events = scheduledEventsDocument(t, srv, address, "2020-07-01")
	if incarnation != 3 || len(events) != 1 || events[0]["EventStatus"] != "Started" || events[0]["NotBefore"] != "" {
		t.Fatalf("while restarting: %d %v, want incarnation 3 and the event Started with no NotBefore", incarnation, events)
	}
	hooks.halt <- nil
	hooks.awaitBoot(t, vm.ID)
	hooks.boot <- nil
	bg.Await()

	incarnation, events = scheduledEventsDocument(t, srv, address, "2020-07-01")
	if incarnation != 4 || len(events) != 0 {
		t.Fatalf("after the restart: %d %v, want incarnation 4 and no events", incarnation, events)
	}
	requireVMLROPowerState(t, vm.ID, "PowerState/running")
}

// A machine that never asked for its events restarts at once.
func TestScheduledEventsOffLeaveARestartImmediate(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	const address = "192.0.2.46"
	vm := putIMDSMachine(t, "se-off-vm", address, nil, nil)
	if rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("se-off-vm", "restart"), ""); rec.Code != http.StatusAccepted {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body.String())
	}
	hooks.awaitHalt(t, vm.ID)
	hooks.halt <- nil
	hooks.awaitBoot(t, vm.ID)
	hooks.boot <- nil
	bg.Await()
	if azureScheduledEventsEnabled(vm.ID) {
		t.Error("Scheduled Events turned on without a request from the machine")
	}
}

// A version shows the event members it introduced, and an older one omits the
// rest.
func TestScheduledEventsRedeployAtOlderVersion(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	const address = "192.0.2.47"
	vm := putIMDSMachine(t, "se-redeploy-vm", address, nil, nil)
	scheduledEventsDocument(t, srv, address, "2017-08-01")
	if rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("se-redeploy-vm", "redeploy"), ""); rec.Code != http.StatusAccepted {
		t.Fatalf("redeploy: %d %s", rec.Code, rec.Body.String())
	}
	_, events := scheduledEventsDocument(t, srv, address, "2017-08-01")
	if len(events) != 1 || events[0]["EventType"] != "Redeploy" {
		t.Fatalf("events = %v, want one Redeploy", events)
	}
	for _, absent := range []string{"Description", "EventSource", "DurationInSeconds"} {
		if _, ok := events[0][absent]; ok {
			t.Errorf("2017-08-01 shows %s", absent)
		}
	}
	_, current := scheduledEventsDocument(t, srv, address, "2020-07-01")
	if current[0]["EventSource"] != "User" {
		t.Errorf("EventSource = %v, want User", current[0]["EventSource"])
	}
	approveScheduledEvent(t, srv, address, events[0]["EventId"].(string))
	hooks.awaitHalt(t, vm.ID)
	hooks.halt <- nil
	hooks.awaitBoot(t, vm.ID)
	hooks.boot <- nil
	bg.Await()
}

// A Spot eviction raises a Preempt event with 30 seconds' notice.
func TestScheduledEventsPreemptASpotEviction(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	const address = "192.0.2.48"
	vm := putIMDSMachine(t, "se-spot-vm", address, func(vm *VirtualMachine) {
		vm.Properties.Priority = "Spot"
		vm.Properties.EvictionPolicy = "Deallocate"
	}, nil)
	scheduledEventsDocument(t, srv, address, "2020-07-01")
	before := time.Now().UTC()
	if rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("se-spot-vm", "simulateEviction"), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("simulateEviction: %d %s", rec.Code, rec.Body.String())
	}
	_, events := scheduledEventsDocument(t, srv, address, "2020-07-01")
	if len(events) != 1 || events[0]["EventType"] != "Preempt" || events[0]["EventSource"] != "Platform" {
		t.Fatalf("events = %v, want one Platform Preempt", events)
	}
	notBefore, err := time.Parse(http.TimeFormat, events[0]["NotBefore"].(string))
	if err != nil || notBefore.Before(before.Add(29*time.Second)) || notBefore.After(before.Add(31*time.Second)) {
		t.Errorf("NotBefore = %v, want 30 seconds ahead", events[0]["NotBefore"])
	}
	if _, older := scheduledEventsDocument(t, srv, address, "2017-08-01"); len(older) != 0 {
		t.Errorf("2017-08-01 shows %v; Preempt arrived in 2017-11-01", older)
	}
	approveScheduledEvent(t, srv, address, events[0]["EventId"].(string))
	hooks.awaitHalt(t, vm.ID)
	hooks.halt <- nil
	bg.Await()
	requireVMLROPowerState(t, vm.ID, "PowerState/deallocated")
	if _, events := scheduledEventsDocument(t, srv, address, "2020-07-01"); len(events) != 0 {
		t.Errorf("after the eviction: %v, want no events", events)
	}
}
