package gcp_cli_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDNS_CreateAndDescribeZone(t *testing.T) {
	t.Parallel()
	runCLI(t, gcloudCLI("dns", "managed-zones", "create", "cli-test-zone",
		"--dns-name=cli-test.example.com.",
		"--description=CLI test zone",
		"--visibility=private",
		"--networks=",
	))

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

	runCLI(t, gcloudCLI("dns", "managed-zones", "delete", "cli-test-zone"))
}

func TestDNS_CreateAndListRecordSets(t *testing.T) {
	t.Parallel()
	runCLI(t, gcloudCLI("dns", "managed-zones", "create", "record-test-zone",
		"--dns-name=records.example.com.",
		"--description=Record test zone",
		"--visibility=private",
		"--networks=",
	))

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

	// A managed zone starts with its own SOA and NS records, so find the created A
	// record by name and type.
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

	// Cloud DNS deletes a zone only once its own records are gone.
	runCLI(t, gcloudCLI("dns", "record-sets", "delete", "test.records.example.com.",
		"--zone=record-test-zone", "--type=A"))
	runCLI(t, gcloudCLI("dns", "managed-zones", "delete", "record-test-zone"))
}

func TestDNS_RecordSetTransactionAndUpdateCLI(t *testing.T) {
	t.Parallel()
	zone := "cli-change-zone"
	recordName := "www.cli-change.example.com."
	transactionFile := filepath.Join(tmpDir, "dns-change-transaction.yaml")

	runCLI(t, gcloudCLI("dns", "managed-zones", "create", zone,
		"--dns-name=cli-change.example.com.",
		"--description=Change API test zone",
		"--visibility=public",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("dns", "record-sets", "delete", recordName, "--zone", zone, "--type", "A", "--quiet").Run()
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

	// Read the added record before the update overwrites it, so a transaction that
	// changed nothing fails.
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
	t.Parallel()
	runCLI(t, gcloudCLI("dns", "managed-zones", "create", "delete-test-zone",
		"--dns-name=delete.example.com.",
		"--description=Delete test zone",
		"--visibility=private",
		"--networks=",
	))

	runCLI(t, gcloudCLI("dns", "managed-zones", "delete", "delete-test-zone"))

	failure := gcloudCLIFails(t, gcloudCLI("dns", "managed-zones", "describe",
		"delete-test-zone", "--format=json"))
	// gcloud renders the 404 as NOT_FOUND for this command group; `gcloud storage`
	// renders the bare status code.
	assert.Contains(t, failure, "NOT_FOUND",
		"describing a deleted managed zone must be refused as not found, got: %s", failure)
}
