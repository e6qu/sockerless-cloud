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

	// List record sets. A managed zone is created with its own SOA and NS
	// records, so a non-empty list proves nothing — the created A record has to
	// be found by name and type, carrying the address it was created with.
	out = httpDoJSON(t, "GET", url, "")
	var listResult struct {
		Rrsets []struct {
			Name    string   `json:"name"`
			Type    string   `json:"type"`
			TTL     int      `json:"ttl"`
			Rrdatas []string `json:"rrdatas"`
		} `json:"rrsets"`
	}
	parseJSON(t, out, &listResult)
	found := false
	for _, rr := range listResult.Rrsets {
		if rr.Name != "test.records.example.com." || rr.Type != "A" {
			continue
		}
		found = true
		assert.Equal(t, 300, rr.TTL)
		assert.Equal(t, []string{"10.0.0.1"}, rr.Rrdatas)
	}
	assert.True(t, found, "the created A record must be in the zone's record sets: %s", out)

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

	// The transaction is only real if the record it added is in the zone. Read
	// it back before the update below overwrites it, otherwise a transaction
	// that changed nothing is indistinguishable from one that worked.
	added := runCLI(t, gcloudCLI("dns", "record-sets", "describe", recordName,
		"--zone", zone,
		"--type", "A",
		"--format=json",
	))
	var committed struct {
		Name    string   `json:"name"`
		Type    string   `json:"type"`
		TTL     int      `json:"ttl"`
		Rrdatas []string `json:"rrdatas"`
	}
	parseJSONObject(t, added, &committed)
	require.Equal(t, recordName, committed.Name)
	require.Equal(t, "A", committed.Type)
	require.Equal(t, 300, committed.TTL)
	require.Equal(t, []string{"203.0.113.20"}, committed.Rrdatas,
		"the executed transaction added the record it described")

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

	// And the zone holds the updated record, not just the response the update
	// echoed.
	var reread struct {
		TTL     int      `json:"ttl"`
		Rrdatas []string `json:"rrdatas"`
	}
	parseJSONObject(t, runCLI(t, gcloudCLI("dns", "record-sets", "describe", recordName,
		"--zone", zone, "--type", "A", "--format=json")), &reread)
	require.Equal(t, 600, reread.TTL)
	require.Equal(t, []string{"203.0.113.21"}, reread.Rrdatas)
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
	failure := gcloudCLIFails(t, gcloudCLI("dns", "managed-zones", "describe",
		"delete-test-zone", "--format=json"))
	assert.Contains(t, failure, "404",
		"describing a deleted managed zone must answer 404, got: %s", failure)
}
