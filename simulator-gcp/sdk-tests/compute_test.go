package gcp_sdk_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func computeService(t *testing.T) *compute.Service {
	t.Helper()
	svc, err := compute.NewService(ctx,
		option.WithEndpoint(baseURL+"/compute/v1/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestCompute_CreateNetwork(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)

	network := &compute.Network{
		Name:                  "test-network",
		AutoCreateSubnetworks: false,
	}
	_, err := svc.Networks.Insert("test-project", network).Do()
	require.NoError(t, err)

	got, err := svc.Networks.Get("test-project", "test-network").Do()
	require.NoError(t, err)
	assert.Equal(t, "test-network", got.Name)
}

func TestCompute_CreateSubnetwork(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)

	// Create network first
	network := &compute.Network{
		Name:                  "subnet-test-net",
		AutoCreateSubnetworks: false,
	}
	_, err := svc.Networks.Insert("test-project", network).Do()
	require.NoError(t, err)

	subnet := &compute.Subnetwork{
		Name:        "test-subnet",
		IpCidrRange: "10.0.0.0/24",
		Network:     "projects/test-project/global/networks/subnet-test-net",
		Region:      "us-central1",
	}
	_, err = svc.Subnetworks.Insert("test-project", "us-central1", subnet).Do()
	require.NoError(t, err)

	got, err := svc.Subnetworks.Get("test-project", "us-central1", "test-subnet").Do()
	require.NoError(t, err)
	assert.Equal(t, "test-subnet", got.Name)
}

func TestCompute_ListNetworks(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)

	network := &compute.Network{
		Name:                  "list-net",
		AutoCreateSubnetworks: false,
	}
	_, err := svc.Networks.Insert("test-project", network).Do()
	require.NoError(t, err)

	resp, err := svc.Networks.List("test-project").Do()
	require.NoError(t, err)
	assertComputeListHasName(t, resp.Items, func(n *compute.Network) string { return n.Name }, "list-net")
}

// computeTestNetwork provisions a VPC network the calling test owns and returns
// the reference a resource attaches to it with, deleting it when the test ends.
// Compute Engine builds a real network fabric behind a VPC, which needs the
// host's Linux networking capabilities, so a host without them cannot hold the
// network a resource points at.
func computeTestNetwork(t *testing.T, svc *compute.Service, project, name string) string {
	t.Helper()
	requireNetworkHost(t)
	_, err := svc.Networks.Insert(project, &compute.Network{Name: name, AutoCreateSubnetworks: false}).Context(ctx).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Networks.Delete(project, name).Context(ctx).Do() })
	got, err := svc.Networks.Get(project, name).Context(ctx).Do()
	require.NoError(t, err)
	require.Equal(t, name, got.Name)
	return "projects/" + project + "/global/networks/" + name
}

func TestCompute_RegionalAddressAndManualRouterNAT(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project = "test-project"
	const region = "us-central1"

	network := &compute.Network{
		Name:                  "sdk-nat-network",
		AutoCreateSubnetworks: false,
	}
	_, err := svc.Networks.Insert(project, network).Context(ctx).Do()
	require.NoError(t, err)
	subnet := &compute.Subnetwork{
		Name:        "sdk-nat-subnet",
		IpCidrRange: "10.25.0.0/24",
		Network:     "projects/test-project/global/networks/sdk-nat-network",
		Region:      region,
	}
	_, err = svc.Subnetworks.Insert(project, region, subnet).Context(ctx).Do()
	require.NoError(t, err)

	addr := &compute.Address{
		Name:        "sdk-nat-address",
		AddressType: "EXTERNAL",
		IpVersion:   "IPV4",
		NetworkTier: "PREMIUM",
	}
	_, err = svc.Addresses.Insert(project, region, addr).Context(ctx).Do()
	require.NoError(t, err)

	gotAddr, err := svc.Addresses.Get(project, region, addr.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, addr.Name, gotAddr.Name)
	assert.Equal(t, "RESERVED", gotAddr.Status)
	assert.NotEmpty(t, gotAddr.Address)

	addrList, err := svc.Addresses.List(project, region).Context(ctx).Do()
	require.NoError(t, err)
	require.NotEmpty(t, addrList.Items)

	router := &compute.Router{
		Name:    "sdk-nat-router",
		Network: "projects/test-project/global/networks/sdk-nat-network",
		Nats: []*compute.RouterNat{{
			Name:                          "sdk-manual-nat",
			NatIpAllocateOption:           "MANUAL_ONLY",
			NatIps:                        []string{gotAddr.SelfLink},
			SourceSubnetworkIpRangesToNat: "ALL_SUBNETWORKS_ALL_IP_RANGES",
		}},
	}
	_, err = svc.Routers.Insert(project, region, router).Context(ctx).Do()
	require.NoError(t, err)

	gotRouter, err := svc.Routers.Get(project, region, router.Name).Context(ctx).Do()
	require.NoError(t, err)
	require.Len(t, gotRouter.Nats, 1)
	assert.Equal(t, "MANUAL_ONLY", gotRouter.Nats[0].NatIpAllocateOption)
	assert.Equal(t, gotAddr.SelfLink, gotRouter.Nats[0].NatIps[0])

	status, err := svc.Routers.GetRouterStatus(project, region, router.Name).Context(ctx).Do()
	require.NoError(t, err)
	require.NotNil(t, status.Result)

	_, err = svc.Routers.Delete(project, region, router.Name).Context(ctx).Do()
	require.NoError(t, err)
	_, err = svc.Addresses.Delete(project, region, addr.Name).Context(ctx).Do()
	require.NoError(t, err)
}

// TestCompute_Firewall_CreateGetListDelete pins the firewall rule
// surface that runner setup flows hit. Real GCP rejects unknown
// directions / negative priorities; the sim defaults to INGRESS +
// priority=1000 like real GCP. The rule is created against a VPC network
// this test provisions, so it never rides on a network another test left
// behind.
func TestCompute_Firewall_CreateGetListDelete(t *testing.T) {
	svc := computeService(t)
	network := computeTestNetwork(t, svc, "test-project", "fw-crud-network")

	rule := &compute.Firewall{
		Name:         "fw-allow-runner-ingress",
		Network:      network,
		Direction:    "INGRESS",
		Priority:     900,
		SourceRanges: []string{"10.0.0.0/8"},
		Allowed: []*compute.FirewallAllowed{
			{IPProtocol: "tcp", Ports: []string{"22", "80", "443"}},
		},
	}

	op, err := svc.Firewalls.Insert("test-project", rule).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "DONE", op.Status)

	got, err := svc.Firewalls.Get("test-project", rule.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, rule.Name, got.Name)
	assert.Contains(t, got.Network, "fw-crud-network",
		"the rule must stay attached to the network it was created against")
	assert.Equal(t, "INGRESS", got.Direction)
	assert.Equal(t, int64(900), got.Priority)
	require.Len(t, got.Allowed, 1)
	assert.Equal(t, "tcp", got.Allowed[0].IPProtocol)
	assert.ElementsMatch(t, []string{"22", "80", "443"}, got.Allowed[0].Ports)

	listOut, err := svc.Firewalls.List("test-project").Context(ctx).Do()
	require.NoError(t, err)
	found := false
	for _, fw := range listOut.Items {
		if fw.Name == rule.Name {
			found = true
			break
		}
	}
	assert.True(t, found, "firewall must show up in List")

	_, err = svc.Firewalls.Delete("test-project", rule.Name).Context(ctx).Do()
	require.NoError(t, err)

	_, err = svc.Firewalls.Get("test-project", rule.Name).Context(ctx).Do()
	require.Error(t, err, "Get after Delete must 404")
}

func TestCompute_Firewall_DefaultsToIngressPriority1000(t *testing.T) {
	svc := computeService(t)
	network := computeTestNetwork(t, svc, "test-project", "fw-defaults-network")

	rule := &compute.Firewall{
		Name:    "fw-defaults",
		Network: network,
		Allowed: []*compute.FirewallAllowed{
			{IPProtocol: "icmp"},
		},
	}
	_, err := svc.Firewalls.Insert("test-project", rule).Context(ctx).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Firewalls.Delete("test-project", rule.Name).Context(ctx).Do() })

	got, err := svc.Firewalls.Get("test-project", rule.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Contains(t, got.Network, "fw-defaults-network")
	assert.Equal(t, "INGRESS", got.Direction, "default direction must match real GCP")
	assert.Equal(t, int64(1000), got.Priority, "default priority must match real GCP")
}

// TestCompute_Disks_CRUD covers the GCP `pd-ephemeral` storage-driver
// prereq: zonal Compute Disks insert / get / list /
// resize / setLabels / delete + aggregated list across zones. Real
// GCP returns zonal operations for every mutation; a disk write completes
// inside its request, so its operation is DONE when the client receives it
// and the SDK's polling loop finishes in one round.
func TestCompute_Disks_CRUD(t *testing.T) {
	svc := computeService(t)
	const project = "test-project"
	const zone = "us-central1-a"

	d := &compute.Disk{
		Name:        "ephemeral-1",
		SizeGb:      20,
		Description: "phase-127 pd-ephemeral test disk",
	}
	_, err := svc.Disks.Insert(project, zone, d).Context(ctx).Do()
	require.NoError(t, err)

	got, err := svc.Disks.Get(project, zone, "ephemeral-1").Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "ephemeral-1", got.Name)
	assert.Equal(t, int64(20), got.SizeGb)
	assert.Equal(t, "READY", got.Status)
	assert.Contains(t, got.Type, "diskTypes/pd-standard", "default type when unset")

	list, err := svc.Disks.List(project, zone).Context(ctx).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(d *compute.Disk) string { return d.Name }, "ephemeral-1")

	_, err = svc.Disks.Resize(project, zone, "ephemeral-1",
		&compute.DisksResizeRequest{SizeGb: 50}).Context(ctx).Do()
	require.NoError(t, err)
	resized, err := svc.Disks.Get(project, zone, "ephemeral-1").Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(50), resized.SizeGb, "resize must update size")

	_, err = svc.Disks.SetLabels(project, zone, "ephemeral-1",
		&compute.ZoneSetLabelsRequest{
			Labels:           map[string]string{"sockerless_session": "abc123"},
			LabelFingerprint: resized.LabelFingerprint,
		}).Context(ctx).Do()
	require.NoError(t, err)
	labelled, err := svc.Disks.Get(project, zone, "ephemeral-1").Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "abc123", labelled.Labels["sockerless_session"], "setLabels round-trip")

	_, err = svc.Disks.Delete(project, zone, "ephemeral-1").Context(ctx).Do()
	require.NoError(t, err)
	_, err = svc.Disks.Get(project, zone, "ephemeral-1").Context(ctx).Do()
	require.Error(t, err, "get after delete must fail")
}

// TestCompute_Disks_AggregatedList covers the cross-zone aggregated
// surface terraform's `data "google_compute_disks"` uses. Real GCP
// groups by `zones/<zone>` keys; sim mirrors that shape.
func TestCompute_Disks_AggregatedList(t *testing.T) {
	svc := computeService(t)
	const project = "test-project"

	for _, z := range []string{"us-central1-a", "us-east1-b"} {
		_, err := svc.Disks.Insert(project, z, &compute.Disk{
			Name:   "agg-" + z,
			SizeGb: 10,
		}).Context(ctx).Do()
		require.NoError(t, err)
	}

	agg, err := svc.Disks.AggregatedList(project).Context(ctx).Do()
	require.NoError(t, err)
	require.NotEmpty(t, agg.Items)

	foundCentral := false
	foundEast := false
	for key, scoped := range agg.Items {
		for _, d := range scoped.Disks {
			if d.Name == "agg-us-central1-a" {
				assert.Equal(t, "zones/us-central1-a", key)
				foundCentral = true
			}
			if d.Name == "agg-us-east1-b" {
				assert.Equal(t, "zones/us-east1-b", key)
				foundEast = true
			}
		}
	}
	assert.True(t, foundCentral, "us-central1-a disk must appear in aggregated list")
	assert.True(t, foundEast, "us-east1-b disk must appear in aggregated list")
}

func TestCompute_Disks_Get_NotFound(t *testing.T) {
	svc := computeService(t)
	_, err := svc.Disks.Get("test-project", "us-central1-a", "does-not-exist").Context(ctx).Do()
	require.Error(t, err, "get on missing disk must 404")
}

// TestCompute_GlobalHTTPLoadBalancerChain exercises the public Compute
// Engine global HTTP load-balancing API surface used by SDKs and
// Terraform: Insert/Get/List/Delete for HealthChecks, BackendServices,
// UrlMaps, TargetHttpProxies, and GlobalForwardingRules.
func TestCompute_GlobalHTTPLoadBalancerChain(t *testing.T) {
	svc := computeService(t)
	const project = "test-project"

	hc := &compute.HealthCheck{
		Name:             "sdk-lb-hc",
		Type:             "HTTP",
		CheckIntervalSec: 5,
		TimeoutSec:       5,
		HttpHealthCheck: &compute.HTTPHealthCheck{
			Port:        80,
			RequestPath: "/healthz",
		},
	}
	_, err := svc.HealthChecks.Insert(project, hc).Context(ctx).Do()
	require.NoError(t, err)
	gotHC, err := svc.HealthChecks.Get(project, hc.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "HTTP", gotHC.Type)
	assert.Equal(t, "/healthz", gotHC.HttpHealthCheck.RequestPath)

	bs := &compute.BackendService{
		Name:         "sdk-lb-backend",
		Protocol:     "HTTP",
		PortName:     "http",
		TimeoutSec:   10,
		HealthChecks: []string{gotHC.SelfLink},
	}
	_, err = svc.BackendServices.Insert(project, bs).Context(ctx).Do()
	require.NoError(t, err)
	gotBS, err := svc.BackendServices.Get(project, bs.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{gotHC.SelfLink}, gotBS.HealthChecks)

	um := &compute.UrlMap{
		Name:           "sdk-lb-url-map",
		DefaultService: gotBS.SelfLink,
	}
	_, err = svc.UrlMaps.Insert(project, um).Context(ctx).Do()
	require.NoError(t, err)
	gotUM, err := svc.UrlMaps.Get(project, um.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, gotBS.SelfLink, gotUM.DefaultService)

	proxy := &compute.TargetHttpProxy{
		Name:   "sdk-lb-http-proxy",
		UrlMap: gotUM.SelfLink,
	}
	_, err = svc.TargetHttpProxies.Insert(project, proxy).Context(ctx).Do()
	require.NoError(t, err)
	gotProxy, err := svc.TargetHttpProxies.Get(project, proxy.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, gotUM.SelfLink, gotProxy.UrlMap)

	fr := &compute.ForwardingRule{
		Name:       "sdk-lb-fr",
		Target:     gotProxy.SelfLink,
		PortRange:  "80",
		IPProtocol: "TCP",
	}
	_, err = svc.GlobalForwardingRules.Insert(project, fr).Context(ctx).Do()
	require.NoError(t, err)
	gotFR, err := svc.GlobalForwardingRules.Get(project, fr.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, gotProxy.SelfLink, gotFR.Target)
	assert.Equal(t, "80", gotFR.PortRange)
	assert.NotEmpty(t, gotFR.IPAddress)

	hcList, err := svc.HealthChecks.List(project).Context(ctx).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, hcList.Items, func(h *compute.HealthCheck) string { return h.Name }, hc.Name)
	frList, err := svc.GlobalForwardingRules.List(project).Context(ctx).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, frList.Items, func(f *compute.ForwardingRule) string { return f.Name }, fr.Name)

	_, err = svc.GlobalForwardingRules.Delete(project, fr.Name).Context(ctx).Do()
	require.NoError(t, err)
	_, err = svc.TargetHttpProxies.Delete(project, proxy.Name).Context(ctx).Do()
	require.NoError(t, err)
	_, err = svc.UrlMaps.Delete(project, um.Name).Context(ctx).Do()
	require.NoError(t, err)
	_, err = svc.BackendServices.Delete(project, bs.Name).Context(ctx).Do()
	require.NoError(t, err)
	_, err = svc.HealthChecks.Delete(project, hc.Name).Context(ctx).Do()
	require.NoError(t, err)
}

func TestCompute_InstanceGroups_Lifecycle(t *testing.T) {
	svc := computeService(t)
	const project = "test-project"
	const zone = "us-central1-a"

	group := &compute.InstanceGroup{
		Name:    "sdk-ig",
		Network: "projects/test-project/global/networks/default",
	}
	_, err := svc.InstanceGroups.Insert(project, zone, group).Context(ctx).Do()
	require.NoError(t, err)
	got, err := svc.InstanceGroups.Get(project, zone, group.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, group.Name, got.Name)

	_, err = svc.InstanceGroups.SetNamedPorts(project, zone, group.Name, &compute.InstanceGroupsSetNamedPortsRequest{
		NamedPorts: []*compute.NamedPort{{Name: "http", Port: 8080}},
	}).Context(ctx).Do()
	require.NoError(t, err)
	got, err = svc.InstanceGroups.Get(project, zone, group.Name).Context(ctx).Do()
	require.NoError(t, err)
	require.Len(t, got.NamedPorts, 1)
	assert.Equal(t, "http", got.NamedPorts[0].Name)
	assert.EqualValues(t, 8080, got.NamedPorts[0].Port)

	member := "projects/test-project/zones/us-central1-a/instances/sdk-member"
	_, err = svc.InstanceGroups.AddInstances(project, zone, group.Name, &compute.InstanceGroupsAddInstancesRequest{
		Instances: []*compute.InstanceReference{{Instance: member}},
	}).Context(ctx).Do()
	require.NoError(t, err)
	members, err := svc.InstanceGroups.ListInstances(project, zone, group.Name, &compute.InstanceGroupsListInstancesRequest{}).Context(ctx).Do()
	require.NoError(t, err)
	require.Len(t, members.Items, 1)
	assert.Equal(t, member, members.Items[0].Instance)

	// Membership rides listInstances; the InstanceGroup resource itself
	// summarizes it via the output-only size member.
	got, err = svc.InstanceGroups.Get(project, zone, group.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Size)

	list, err := svc.InstanceGroups.List(project, zone).Context(ctx).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(g *compute.InstanceGroup) string { return g.Name }, group.Name)

	_, err = svc.InstanceGroups.RemoveInstances(project, zone, group.Name, &compute.InstanceGroupsRemoveInstancesRequest{
		Instances: []*compute.InstanceReference{{Instance: member}},
	}).Context(ctx).Do()
	require.NoError(t, err)
	members, err = svc.InstanceGroups.ListInstances(project, zone, group.Name, &compute.InstanceGroupsListInstancesRequest{}).Context(ctx).Do()
	require.NoError(t, err)
	assert.Empty(t, members.Items)

	_, err = svc.InstanceGroups.Delete(project, zone, group.Name).Context(ctx).Do()
	require.NoError(t, err)
}

func TestCompute_Instances_Lifecycle(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project = "test-project"
	const zone = "us-central1-a"
	const region = "us-central1"

	network := &compute.Network{Name: "sdk-vm-network", AutoCreateSubnetworks: false}
	_, err := svc.Networks.Insert(project, network).Context(ctx).Do()
	require.NoError(t, err)
	subnet := &compute.Subnetwork{
		Name:        "sdk-vm-subnet",
		IpCidrRange: "10.26.0.0/24",
		Network:     "projects/test-project/global/networks/sdk-vm-network",
		Region:      region,
	}
	_, err = svc.Subnetworks.Insert(project, region, subnet).Context(ctx).Do()
	require.NoError(t, err)

	inst := &compute.Instance{
		Name:        "sdk-vm-1",
		MachineType: "e2-micro",
		Disks: []*compute.AttachedDisk{{
			Boot:       true,
			AutoDelete: true,
			InitializeParams: &compute.AttachedDiskInitializeParams{
				SourceImage: "projects/debian-cloud/global/images/debian-12",
				DiskSizeGb:  10,
			},
		}},
		NetworkInterfaces: []*compute.NetworkInterface{{
			Network:    "projects/test-project/global/networks/sdk-vm-network",
			Subnetwork: "projects/test-project/regions/us-central1/subnetworks/sdk-vm-subnet",
		}},
		Labels: map[string]string{"env": "sdk"},
		Tags:   &compute.Tags{Items: []string{"runner"}},
	}

	// Compute Engine answers instances.insert with a zone operation that is
	// still RUNNING and boots the machine behind it; the client polls
	// zoneOperations for the verdict. That poll is the whole reason the boot is
	// not bounded by this one call's request timeout.
	op, err := svc.Instances.Insert(project, zone, inst).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", op.Status,
		"insert must answer with a running operation, not a finished one")
	assert.Equal(t, "insert", op.OperationType)
	assert.Empty(t, op.EndTime, "an operation still running carries no end time")

	done := awaitZoneOperation(t, svc, project, zone, op.Name)
	require.Nil(t, done.Error, "the insert operation failed: %+v", done.Error)
	assert.Equal(t, int64(100), done.Progress)
	assert.NotEmpty(t, done.EndTime, "a finished operation carries its end time")

	got, err := svc.Instances.Get(project, zone, "sdk-vm-1").Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-vm-1", got.Name)
	assert.Equal(t, "RUNNING", got.Status)
	require.Len(t, got.NetworkInterfaces, 1)
	assert.NotEmpty(t, got.NetworkInterfaces[0].NetworkIP)

	list, err := svc.Instances.List(project, zone).Context(ctx).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(i *compute.Instance) string { return i.Name }, "sdk-vm-1")

	_, err = svc.Instances.Stop(project, zone, "sdk-vm-1").Context(ctx).Do()
	require.NoError(t, err)
	stopped, err := svc.Instances.Get(project, zone, "sdk-vm-1").Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "TERMINATED", stopped.Status)

	_, err = svc.Instances.Start(project, zone, "sdk-vm-1").Context(ctx).Do()
	require.NoError(t, err)
	running, err := svc.Instances.Get(project, zone, "sdk-vm-1").Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", running.Status)

	_, err = svc.Instances.Delete(project, zone, "sdk-vm-1").Context(ctx).Do()
	require.NoError(t, err)
	_, err = svc.Instances.Get(project, zone, "sdk-vm-1").Context(ctx).Do()
	require.Error(t, err, "get after delete must fail")
}

func TestCompute_InstanceTemplateCRUD(t *testing.T) {
	svc := computeService(t)
	project := "tmpl-test-project"

	tmpl := &compute.InstanceTemplate{
		Name: "sdk-instance-tmpl",
		Properties: &compute.InstanceProperties{
			MachineType: "n1-standard-2",
			Disks: []*compute.AttachedDisk{
				{
					Boot:             true,
					AutoDelete:       true,
					InitializeParams: &compute.AttachedDiskInitializeParams{DiskSizeGb: 20},
				},
			},
		},
	}

	// Insert (create) instance template — returns an Operation.
	op, err := svc.InstanceTemplates.Insert(project, tmpl).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "DONE", op.Status)

	// Get instance template.
	got, err := svc.InstanceTemplates.Get(project, "sdk-instance-tmpl").Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-instance-tmpl", got.Name)
	assert.Equal(t, "compute#instanceTemplate", got.Kind)
	require.NotNil(t, got.Properties)

	// List instance templates — must contain created template.
	list, err := svc.InstanceTemplates.List(project).Context(ctx).Do()
	require.NoError(t, err)
	found := false
	for _, t := range list.Items {
		if t.Name == "sdk-instance-tmpl" {
			found = true
		}
	}
	assert.True(t, found, "created template must appear in list")

	// Delete instance template.
	_, err = svc.InstanceTemplates.Delete(project, "sdk-instance-tmpl").Context(ctx).Do()
	require.NoError(t, err)

	// Get after delete must fail.
	_, err = svc.InstanceTemplates.Get(project, "sdk-instance-tmpl").Context(ctx).Do()
	require.Error(t, err, "get after delete must fail")
}

func TestCompute_ListNetworks_Pagination(t *testing.T) {
	requireNetworkHost(t)
	const project = "test-project"
	svc := computeService(t)
	names := []string{"pag-net-a", "pag-net-b", "pag-net-c"}
	for _, n := range names {
		_, err := svc.Networks.Insert(project, &compute.Network{Name: n, AutoCreateSubnetworks: false}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { svc.Networks.Delete(project, n).Do() })
	}

	seen := map[string]bool{}
	var pageToken string
	for {
		call := svc.Networks.List(project).MaxResults(1)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		page, err := call.Do()
		require.NoError(t, err)
		for _, n := range page.Items {
			seen[n.Name] = true
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
	}
	for _, n := range names {
		assert.True(t, seen[n], "network %s should appear via pagination", n)
	}
}

// TestCompute_ListSubnetworks exercises compute.subnetworks.list — the
// regional list method, distinct from subnetworks.aggregatedList:
//
//	GET /compute/v1/projects/{project}/regions/{region}/subnetworks
//
// It answers a SubnetworkList (`kind` plus `items`) rather than the
// scope-keyed map the aggregated form returns, and it is the read behind
// `gcloud compute networks subnets list --region` and the console's VPC
// network detail page.
func TestCompute_ListSubnetworks(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project = "test-project"
	const region = "us-central1"

	network := &compute.Network{Name: "subnet-list-net", AutoCreateSubnetworks: false}
	_, err := svc.Networks.Insert(project, network).Context(ctx).Do()
	require.NoError(t, err)

	subnet := &compute.Subnetwork{
		Name:        "subnet-list-a",
		IpCidrRange: "10.61.0.0/24",
		Network:     "projects/test-project/global/networks/subnet-list-net",
		Region:      region,
	}
	_, err = svc.Subnetworks.Insert(project, region, subnet).Context(ctx).Do()
	require.NoError(t, err)

	resp, err := svc.Subnetworks.List(project, region).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#subnetworkList", resp.Kind)

	var found *compute.Subnetwork
	for _, item := range resp.Items {
		if item.Name == subnet.Name {
			found = item
		}
	}
	require.NotNil(t, found, "subnetworks.list did not return the inserted subnet")
	assert.Equal(t, subnet.IpCidrRange, found.IpCidrRange)
	assert.Contains(t, found.Network, "subnet-list-net")

}

// TestCompute_ListSubnetworks_EmptyRegion proves the same regional list route
//
//	GET /compute/v1/projects/{project}/regions/{region}/subnetworks
//
// answers a well-formed, empty SubnetworkList for a region that holds no
// subnets rather than a 404. The read touches no real-execution host
// capability, so unlike the round trip above it runs on every host.
func TestCompute_ListSubnetworks_EmptyRegion(t *testing.T) {
	svc := computeService(t)
	resp, err := svc.Subnetworks.List("test-project", "europe-west4").Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#subnetworkList", resp.Kind)
	assert.Empty(t, resp.Items)
}

// awaitZoneOperation polls a zone operation to DONE the way a Compute Engine
// client does, through zoneOperations.wait — which Compute Engine documents as
// waiting "for the specified Operation resource to return as DONE or for the
// request to approach the 2 minute deadline", so a boot longer than one wait
// takes another wait rather than failing.
func awaitZoneOperation(t *testing.T, svc *compute.Service, project, zone, name string) *compute.Operation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		op, err := svc.ZoneOperations.Wait(project, zone, name).Context(ctx).Do()
		require.NoError(t, err)
		if op.Status == "DONE" {
			return op
		}
		require.True(t, time.Now().Before(deadline),
			"zone operation %s never reached DONE (last status %s)", name, op.Status)
	}
}

// TestCompute_InsertReturnsARunningOperation is the asynchronous contract on
// its own: instances.insert answers immediately with a RUNNING zone operation
// naming the instance it is bringing up, the instance is already visible in a
// pre-RUNNING state while the boot proceeds, and only the operation reaching
// DONE means the machine is up. A simulator that booted inside the request
// could not show the instance in PROVISIONING or STAGING at all, because the
// response would not exist until the boot had finished.
func TestCompute_InsertReturnsARunningOperation(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project = "test-project"
	const zone = "us-central1-a"
	const region = "us-central1"

	_, err := svc.Networks.Insert(project,
		&compute.Network{Name: "sdk-async-network", AutoCreateSubnetworks: false}).Context(ctx).Do()
	require.NoError(t, err)
	_, err = svc.Subnetworks.Insert(project, region, &compute.Subnetwork{
		Name:        "sdk-async-subnet",
		IpCidrRange: "10.29.0.0/24",
		Network:     "projects/test-project/global/networks/sdk-async-network",
		Region:      region,
	}).Context(ctx).Do()
	require.NoError(t, err)

	inst := &compute.Instance{
		Name:        "sdk-async-vm",
		MachineType: "e2-micro",
		Disks: []*compute.AttachedDisk{{
			Boot:       true,
			AutoDelete: true,
			InitializeParams: &compute.AttachedDiskInitializeParams{
				SourceImage: "projects/debian-cloud/global/images/debian-12",
				DiskSizeGb:  10,
			},
		}},
		NetworkInterfaces: []*compute.NetworkInterface{{
			Network:    "projects/test-project/global/networks/sdk-async-network",
			Subnetwork: "projects/test-project/regions/us-central1/subnetworks/sdk-async-subnet",
		}},
	}
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, zone, inst.Name).Do() })

	started := time.Now()
	op, err := svc.Instances.Insert(project, zone, inst).Context(ctx).Do()
	require.NoError(t, err)
	insertTook := time.Since(started)

	assert.Equal(t, "RUNNING", op.Status)
	assert.Equal(t, "insert", op.OperationType)
	assert.Contains(t, op.TargetLink, "/zones/"+zone+"/instances/"+inst.Name)
	assert.NotEmpty(t, op.InsertTime)
	assert.NotEmpty(t, op.StartTime)
	assert.Empty(t, op.EndTime)
	// The insert answers as soon as the instance is recorded; booting a machine
	// takes orders of magnitude longer than that, so a response that waited for
	// the boot could not come back inside this budget.
	assert.Less(t, insertTook, 2*time.Second,
		"the insert must return before the boot finishes, not after it")

	// The instance exists while its operation is still running, in a state that
	// precedes RUNNING. Sampling from the moment the insert answered until the
	// operation settles is how a client watches a boot; a simulator that booted
	// inside the insert request would have no pre-RUNNING state left to show by
	// the time it answered, so nothing would ever be sampled in one.
	deadline := time.Now().Add(5 * time.Minute)
	sawPreRunning := false
	var lastInstanceStatus string
	for {
		// A GET of the operation reports the same operation, not a fabricated
		// verdict.
		polled, err := svc.ZoneOperations.Get(project, zone, op.Name).Context(ctx).Do()
		require.NoError(t, err)
		require.Equal(t, op.Name, polled.Name)
		require.Equal(t, "insert", polled.OperationType)

		booting, err := svc.Instances.Get(project, zone, inst.Name).Context(ctx).Do()
		require.NoError(t, err, "the instance must be visible while its insert operation runs")
		lastInstanceStatus = booting.Status
		if polled.Status == "RUNNING" &&
			(booting.Status == "PROVISIONING" || booting.Status == "STAGING") {
			sawPreRunning = true
			break
		}
		if polled.Status == "DONE" {
			break
		}
		require.True(t, time.Now().Before(deadline),
			"the insert operation never settled (last instance status %s)", lastInstanceStatus)
	}
	require.True(t, sawPreRunning,
		"the instance was never observed before RUNNING while its insert operation ran; last status %s",
		lastInstanceStatus)

	// The operation eventually settles DONE.
	done := awaitZoneOperation(t, svc, project, zone, op.Name)
	require.Nil(t, done.Error, "the insert operation failed: %+v", done.Error)
	assert.Equal(t, int64(100), done.Progress)

	up, err := svc.Instances.Get(project, zone, inst.Name).Context(ctx).Do()
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", up.Status)
}

// TestCompute_ZoneOperationGetIsNotFoundForAnUnknownName is the negative
// control for the operation registry: a name the simulator never handed out is
// not answered with an invented DONE.
func TestCompute_ZoneOperationGetIsNotFoundForAnUnknownName(t *testing.T) {
	svc := computeService(t)
	_, err := svc.ZoneOperations.Get("test-project", "us-central1-a", "operation-never-issued").Context(ctx).Do()
	require.Error(t, err)
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}
