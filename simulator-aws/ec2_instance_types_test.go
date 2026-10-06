package main

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestEC2InstanceTypesVendoredCatalog locks the vendored catalog: its
// provenance is written down, every entry converts, and the count moves only
// when scripts/fetch-aws-ec2-instance-types.go regenerates it, so a partial
// vendor fails here rather than serving a short catalog.
func TestEC2InstanceTypesVendoredCatalog(t *testing.T) {
	var header struct {
		Source, Region, Version, PublicationDate, SHA256, Retrieved string
	}
	if err := json.Unmarshal(ec2InstanceTypesJSON, &header); err != nil {
		t.Fatal(err)
	}
	if header.Source == "" || header.Version == "" || len(header.SHA256) != 64 || header.Retrieved == "" {
		t.Fatalf("the vendored catalog does not record its source, offer version, checksum and retrieval: %+v", header)
	}
	list, byName := ec2InstanceTypeCatalog()
	if got, want := len(list), 1410; got != want {
		t.Errorf("catalog holds %d instance types, want %d — regenerate with "+
			"go run scripts/fetch-aws-ec2-instance-types.go and move this number in the same commit", got, want)
	}
	for _, facts := range list {
		if facts.VCpus <= 0 || facts.MemoryMiB <= 0 || len(facts.Architectures) == 0 {
			t.Errorf("%s converts to %+v", facts.Name, facts)
		}
	}
	for name, want := range map[string]ec2InstanceTypeFacts{
		"t3.micro":    {VCpus: 2, MemoryMiB: 1024, Architectures: []string{"x86_64"}},
		"m5.24xlarge": {VCpus: 96, MemoryMiB: 393216, Architectures: []string{"x86_64"}},
		"t4g.nano":    {VCpus: 2, MemoryMiB: 512, Architectures: []string{"arm64"}},
		"t1.micro":    {VCpus: 1, MemoryMiB: 627, Architectures: []string{"i386", "x86_64"}},
		"m1.small":    {VCpus: 1, MemoryMiB: 1740, Architectures: []string{"i386", "x86_64"}},
		"mac1.metal":  {VCpus: 12, MemoryMiB: 32768, Architectures: []string{"x86_64_mac"}},
		"mac2.metal":  {VCpus: 8, MemoryMiB: 16384, Architectures: []string{"arm64_mac"}},
	} {
		got, ok := byName[name]
		if !ok {
			t.Errorf("%s is missing from the catalog", name)
			continue
		}
		if got.VCpus != want.VCpus || got.MemoryMiB != want.MemoryMiB || !slices.Equal(got.Architectures, want.Architectures) {
			t.Errorf("%s = %d vCPUs, %d MiB, %v; want %d, %d, %v", name,
				got.VCpus, got.MemoryMiB, got.Architectures, want.VCpus, want.MemoryMiB, want.Architectures)
		}
	}
}
