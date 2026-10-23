package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

func TestCompute_DefaultNetworkCarriesGooglesDefaultFirewallRules(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	project := requireProject(t, "default-network-rules")
	const zone = "asia-southeast3-a"

	op, err := svc.Instances.Insert(project, zone, &compute.Instance{
		Name:        "on-default",
		MachineType: "e2-micro",
		Disks: []*compute.AttachedDisk{{
			Boot: true, AutoDelete: true,
			InitializeParams: &compute.AttachedDiskInitializeParams{SourceImage: "projects/debian-cloud/global/images/family/" + debianFamily()},
		}},
		NetworkInterfaces: []*compute.NetworkInterface{{}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, zone, "on-default").Do() })
	done := awaitZoneOperation(t, svc, project, zone, op.Name)
	require.Nil(t, done.Error, "the insert failed: %+v", done.Error)

	got, err := svc.Instances.Get(project, zone, "on-default").Do()
	require.NoError(t, err)
	assert.Contains(t, got.NetworkInterfaces[0].Network, "/global/networks/default")

	rules, err := svc.Firewalls.List(project).Do()
	require.NoError(t, err)
	byName := map[string]*compute.Firewall{}
	for _, fw := range rules.Items {
		byName[fw.Name] = fw
	}
	want := map[string]struct {
		source  string
		allowed []string
	}{
		"default-allow-internal": {"10.128.0.0/9", []string{"tcp:0-65535", "udp:0-65535", "icmp"}},
		"default-allow-ssh":      {"0.0.0.0/0", []string{"tcp:22"}},
		"default-allow-rdp":      {"0.0.0.0/0", []string{"tcp:3389"}},
		"default-allow-icmp":     {"0.0.0.0/0", []string{"icmp"}},
	}
	for name, rule := range want {
		fw, ok := byName[name]
		if !assert.True(t, ok, "the default network comes with %s", name) {
			continue
		}
		assert.Contains(t, fw.Network, "/global/networks/default")
		assert.Equal(t, "INGRESS", fw.Direction)
		assert.Equal(t, int64(65534), fw.Priority)
		assert.Equal(t, []string{rule.source}, fw.SourceRanges)
		var allowed []string
		for _, a := range fw.Allowed {
			entry := a.IPProtocol
			for _, p := range a.Ports {
				entry += ":" + p
			}
			allowed = append(allowed, entry)
		}
		assert.Equal(t, rule.allowed, allowed, name)
	}
}
