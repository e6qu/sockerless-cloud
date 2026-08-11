package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func dnsService(t *testing.T) *dns.Service {
	t.Helper()
	svc, err := dns.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestDNS_CreateManagedZone(t *testing.T) {
	svc := dnsService(t)

	zone := &dns.ManagedZone{
		Name:    "test-zone",
		DnsName: "test.example.com.",
	}
	created, err := svc.ManagedZones.Create("test-project", zone).Do()
	require.NoError(t, err)
	assert.Equal(t, "test-zone", created.Name)
	assert.Equal(t, "test.example.com.", created.DnsName)
}

func TestDNS_GetManagedZone(t *testing.T) {
	svc := dnsService(t)

	zone := &dns.ManagedZone{
		Name:    "get-zone",
		DnsName: "get.example.com.",
	}
	_, err := svc.ManagedZones.Create("test-project", zone).Do()
	require.NoError(t, err)

	got, err := svc.ManagedZones.Get("test-project", "get-zone").Do()
	require.NoError(t, err)
	assert.Equal(t, "get-zone", got.Name)
}

func TestDNS_ListManagedZones(t *testing.T) {
	svc := dnsService(t)

	zone := &dns.ManagedZone{
		Name:    "list-zone",
		DnsName: "list.example.com.",
	}
	_, err := svc.ManagedZones.Create("test-project", zone).Do()
	require.NoError(t, err)

	resp, err := svc.ManagedZones.List("test-project").Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(resp.ManagedZones), 1)
}

func TestDNS_DeleteManagedZone(t *testing.T) {
	svc := dnsService(t)

	zone := &dns.ManagedZone{
		Name:    "del-zone",
		DnsName: "del.example.com.",
	}
	_, err := svc.ManagedZones.Create("test-project", zone).Do()
	require.NoError(t, err)

	err = svc.ManagedZones.Delete("test-project", "del-zone").Do()
	require.NoError(t, err)
}

func TestDNS_ChangesAndPatchRecordSet(t *testing.T) {
	svc := dnsService(t)
	project := "test-project"
	zoneName := "changes-zone"

	_, err := svc.ManagedZones.Create(project, &dns.ManagedZone{
		Name:    zoneName,
		DnsName: "changes.example.com.",
	}).Do()
	require.NoError(t, err)

	createChange, err := svc.Changes.Create(project, zoneName, &dns.Change{
		Additions: []*dns.ResourceRecordSet{{
			Name:    "www.changes.example.com.",
			Type:    "A",
			Ttl:     300,
			Rrdatas: []string{"203.0.113.10"},
		}},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, "done", createChange.Status)
	require.NotEmpty(t, createChange.Id)

	gotChange, err := svc.Changes.Get(project, zoneName, createChange.Id).Do()
	require.NoError(t, err)
	require.Equal(t, createChange.Id, gotChange.Id)

	changeList, err := svc.Changes.List(project, zoneName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, changeList.Changes)

	patched, err := svc.ResourceRecordSets.Patch(project, zoneName, "www.changes.example.com.", "A", &dns.ResourceRecordSet{
		Ttl:     600,
		Rrdatas: []string{"203.0.113.11"},
	}).Do()
	require.NoError(t, err)
	require.EqualValues(t, 600, patched.Ttl)
	require.Equal(t, []string{"203.0.113.11"}, patched.Rrdatas)

	replaceChange, err := svc.Changes.Create(project, zoneName, &dns.Change{
		Deletions: []*dns.ResourceRecordSet{{
			Name:    "www.changes.example.com.",
			Type:    "A",
			Ttl:     600,
			Rrdatas: []string{"203.0.113.11"},
		}},
		Additions: []*dns.ResourceRecordSet{{
			Name:    "www.changes.example.com.",
			Type:    "A",
			Ttl:     300,
			Rrdatas: []string{"203.0.113.12"},
		}},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, "done", replaceChange.Status)

	record, err := svc.ResourceRecordSets.Get(project, zoneName, "www.changes.example.com.", "A").Do()
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.12"}, record.Rrdatas)
}

func TestDNS_ChangesCreatePreconditionUsesCanonicalStatus(t *testing.T) {
	svc := dnsService(t)
	project := "test-project"
	zoneName := "precondition-zone"

	_, err := svc.ManagedZones.Create(project, &dns.ManagedZone{
		Name:    zoneName,
		DnsName: "precondition.example.com.",
	}).Do()
	require.NoError(t, err)

	_, err = svc.Changes.Create(project, zoneName, &dns.Change{
		Additions: []*dns.ResourceRecordSet{{
			Name:    "www.precondition.example.com.",
			Type:    "A",
			Ttl:     300,
			Rrdatas: []string{"203.0.113.10"},
		}},
	}).Do()
	require.NoError(t, err)

	_, err = svc.Changes.Create(project, zoneName, &dns.Change{
		Deletions: []*dns.ResourceRecordSet{{
			Name:    "www.precondition.example.com.",
			Type:    "A",
			Ttl:     300,
			Rrdatas: []string{"203.0.113.99"},
		}},
	}).Do()
	require.Error(t, err)
	gerr, ok := err.(*googleapi.Error)
	require.True(t, ok)
	require.Equal(t, 412, gerr.Code)
	require.NotEmpty(t, gerr.Errors)
	assert.Equal(t, "FAILED_PRECONDITION", gerr.Errors[0].Reason)
}
