package main

import (
	"testing"
)

// TestInterconnectLocationsVendoredCatalog locks the vendored catalogue so a
// partial vendor fails loudly rather than serving a short list a client cannot
// tell from a complete one.
//
// The counts come from Google's published colocation-facility page as it stood
// on the retrieval date recorded in the file. Regenerating with
// scripts/fetch-gcp-interconnect-locations.sh after Google adds or closes a
// facility moves them; that is a real change and it should be visible in the
// diff, which is what this test forces.
func TestInterconnectLocationsVendoredCatalog(t *testing.T) {
	catalog := interconnectLocations()

	if catalog.Source == "" || catalog.Retrieved == "" {
		t.Fatal("the vendored catalogue records neither where it came from nor when; " +
			"a catalogue whose provenance is not written down cannot be checked against its source")
	}
	if got, want := len(catalog.Locations), 321; got != want {
		t.Errorf("catalogue holds %d facilities, want %d — regenerate with "+
			"scripts/fetch-gcp-interconnect-locations.sh and move this number in the same commit", got, want)
	}

	// Every entry carries what the source states for it. The peeringdb id and
	// the region are absent for the facilities the page leaves them off, and
	// the counts of those are locked too: a parse that silently stopped
	// populating them would otherwise look like Google dropping them.
	var withoutPeeringdb, withoutRegion int
	zones := map[string]int{}
	for _, location := range catalog.Locations {
		for _, field := range []string{"name", "city", "description", "availabilityZone"} {
			if value, _ := location[field].(string); value == "" {
				t.Fatalf("%v: %s is empty, and every facility on the page states one",
					location["name"], field)
			}
		}
		if _, ok := location["peeringdbFacilityId"]; !ok {
			withoutPeeringdb++
		}
		if _, ok := location["regionInfos"]; !ok {
			withoutRegion++
		}
		zone, _ := location["availabilityZone"].(string)
		zones[zone]++
	}
	if got, want := withoutPeeringdb, 18; got != want {
		t.Errorf("%d facilities carry no peeringdb id, want %d", got, want)
	}
	if got, want := withoutRegion, 126; got != want {
		t.Errorf("%d facilities carry no low-latency region, want %d", got, want)
	}
	if len(zones) < 2 {
		t.Errorf("every facility is in %v; the catalogue should span both availability zones", zones)
	}

	// The fields the source does not state are absent rather than invented.
	for _, location := range catalog.Locations {
		for _, field := range []string{"address", "facilityProvider", "continent", "availableLinkTypes"} {
			if _, present := location[field]; present {
				t.Errorf("%v carries %s, which Google's page does not state — "+
					"a field the source does not give is left absent, not inferred",
					location["name"], field)
			}
		}
	}
}
