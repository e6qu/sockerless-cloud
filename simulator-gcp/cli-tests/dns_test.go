package gcp_cli_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDNS_CreateAndDescribeZone(t *testing.T) {
	runCLI(t, gcloudCLI("dns", "managed-zones", "create", "cli-test-zone",
		"--dns-name=cli-test.example.com.",
		"--description=CLI test zone",
		"--visibility=private",
		"--networks=",
	))

	// Describe (returns a single JSON object)
	out := runCLI(t, gcloudCLI("dns", "managed-zones", "describe", "cli-test-zone",
		"--format=json",
	))
	var zone struct {
		Name    string `json:"name"`
		DnsName string `json:"dnsName"`
	}
	parseJSON(t, out, &zone)
	assert.Equal(t, "cli-test-zone", zone.Name)
	assert.Equal(t, "cli-test.example.com.", zone.DnsName)

	// Cleanup
	runCLI(t, gcloudCLI("dns", "managed-zones", "delete", "cli-test-zone"))
}

func TestDNS_CreateAndListRecordSets(t *testing.T) {
	// Create zone first
	runCLI(t, gcloudCLI("dns", "managed-zones", "create", "record-test-zone",
		"--dns-name=records.example.com.",
		"--description=Record test zone",
		"--visibility=private",
		"--networks=",
	))

	// Create a record set via direct HTTP since gcloud record-sets create
	// may not support endpoint overrides consistently
	url := fmt.Sprintf("%s/dns/v1/projects/%s/managedZones/record-test-zone/rrsets",
		baseURL, project)
	out := httpDoJSON(t, "POST", url,
		`{"name":"test.records.example.com.","type":"A","ttl":300,"rrdatas":["10.0.0.1"]}`)

	var record struct {
		Name    string   `json:"name"`
		Type    string   `json:"type"`
		TTL     int      `json:"ttl"`
		Rrdatas []string `json:"rrdatas"`
	}
	parseJSON(t, out, &record)
	assert.Equal(t, "test.records.example.com.", record.Name)
	assert.Equal(t, "A", record.Type)
	require.Len(t, record.Rrdatas, 1)
	assert.Equal(t, "10.0.0.1", record.Rrdatas[0])

	// List record sets
	out = httpDoJSON(t, "GET", url, "")
	var listResult struct {
		Rrsets []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"rrsets"`
	}
	parseJSON(t, out, &listResult)
	assert.NotEmpty(t, listResult.Rrsets)

	// Cleanup
	runCLI(t, gcloudCLI("dns", "managed-zones", "delete", "record-test-zone"))
}

func TestDNS_RecordSetTransactionAndUpdateCLI(t *testing.T) {
	zone := "cli-change-zone"
	recordName := "www.cli-change.example.com."
	transactionFile := filepath.Join(tmpDir, "dns-change-transaction.yaml")

	runCLI(t, gcloudCLI("dns", "managed-zones", "create", zone,
		"--dns-name=cli-change.example.com.",
		"--description=Change API test zone",
		"--visibility=public",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("dns", "managed-zones", "delete", zone, "--quiet").Run()
	})

	runCLI(t, gcloudCLI("dns", "record-sets", "transaction", "start",
		"--zone", zone,
		"--transaction-file", transactionFile,
		"--skip-soa-update",
	))
	runCLI(t, gcloudCLI("dns", "record-sets", "transaction", "add",
		"203.0.113.20",
		"--name", recordName,
		"--ttl", "300",
		"--type", "A",
		"--zone", zone,
		"--transaction-file", transactionFile,
	))
	runCLI(t, gcloudCLI("dns", "record-sets", "transaction", "execute",
		"--zone", zone,
		"--transaction-file", transactionFile,
		"--format=json",
	))

	updateOut := runCLI(t, gcloudCLI("dns", "record-sets", "update", recordName,
		"--zone", zone,
		"--type", "A",
		"--ttl", "600",
		"--rrdatas", "203.0.113.21",
		"--format=json",
	))
	var updated struct {
		Name    string   `json:"name"`
		Type    string   `json:"type"`
		TTL     int      `json:"ttl"`
		Rrdatas []string `json:"rrdatas"`
	}
	parseJSON(t, updateOut, &updated)
	require.Equal(t, recordName, updated.Name)
	require.Equal(t, "A", updated.Type)
	require.Equal(t, 600, updated.TTL)
	require.Equal(t, []string{"203.0.113.21"}, updated.Rrdatas)
}

func TestDNS_DeleteZone(t *testing.T) {
	runCLI(t, gcloudCLI("dns", "managed-zones", "create", "delete-test-zone",
		"--dns-name=delete.example.com.",
		"--description=Delete test zone",
		"--visibility=private",
		"--networks=",
	))

	runCLI(t, gcloudCLI("dns", "managed-zones", "delete", "delete-test-zone"))

	// Verify it's gone - describe should fail
	cmd := gcloudCLI("dns", "managed-zones", "describe", "delete-test-zone", "--format=json")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "Expected describe to fail after deletion")
}
