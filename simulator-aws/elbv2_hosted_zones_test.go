package main

import (
	"compress/gzip"
	"io"
	"os"
	"regexp"
	"testing"
)

// The generated table holds exactly the zones the vendored provider file
// lists, so a refresh that changes one without the other fails here.
func TestELBv2HostedZonesMatchTheVendoredTable(t *testing.T) {
	f, err := os.Open("../specs/cloud-api/aws/elbv2-hosted-zone-ids.go.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	src, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	for kind, variable := range map[string]string{"application": "hostedZoneIDPerRegionALBMap", "network": "hostedZoneIDPerRegionNLBMap"} {
		block := regexp.MustCompile(`(?s)var ` + variable + ` = map\[string\]string\{(.*?)\n\}`).FindSubmatch(src)
		if block == nil {
			t.Fatalf("%s is not in the vendored file", variable)
		}
		zones := regexp.MustCompile(`"(Z[A-Z0-9]+)"`).FindAllSubmatch(block[1], -1)
		if len(zones) != len(elbv2HostedZones[kind]) {
			t.Errorf("%s: vendored file lists %d zones, generated table %d", kind, len(zones), len(elbv2HostedZones[kind]))
		}
		generated := map[string]bool{}
		for _, zone := range elbv2HostedZones[kind] {
			generated[zone] = true
		}
		for _, zone := range zones {
			if !generated[string(zone[1])] {
				t.Errorf("%s: zone %s is not in the generated table", kind, zone[1])
			}
		}
	}
}
