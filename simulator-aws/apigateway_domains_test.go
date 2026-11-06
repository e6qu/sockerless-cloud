package main

import (
	"compress/gzip"
	"io"
	"os"
	"regexp"
	"testing"
)

func TestAPIGatewayRegionalHostedZonesMatchTheVendoredTable(t *testing.T) {
	f, err := os.Open("../specs/cloud-api/aws/api-gateway-endpoints.md.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	page, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile(`\|\s*([a-z]{2}(?:-gov)?-[a-z]+-\d)\s*\|\s*execute-api\.[a-z0-9-]+\.amazonaws\.com\s*\|\s*HTTPS\s*\|\s*(Z[A-Z0-9]+)\s*\|`).FindAllSubmatch(page, -1)
	if len(rows) == 0 {
		t.Fatal("the vendored page lists no execute-api hosted zones")
	}
	if len(rows) != len(apigwRegionalHostedZones) {
		t.Errorf("vendored page lists %d Regions, table %d", len(rows), len(apigwRegionalHostedZones))
	}
	for _, row := range rows {
		if got := apigwRegionalHostedZones[string(row[1])]; got != string(row[2]) {
			t.Errorf("%s: table has %q, vendored page %q", row[1], got, row[2])
		}
	}
}
