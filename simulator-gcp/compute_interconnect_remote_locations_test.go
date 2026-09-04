package main

import "testing"

// TestInterconnectRemoteLocationsVendoredCatalog locks the vendored catalogue,
// and locks the associations rather than only the count.
//
// The enumeration is the easy half. What a rowspan-laden source gets wrong is
// which city and which permitted connections belong to which remote location,
// and a count check cannot see that at all: the first parse of these pages
// recovered every entry and still filed aws-lgknx under no city, because Seoul
// is rowspanned from the entry above it. So the two entries that broke it are
// asserted by hand here.
func TestInterconnectRemoteLocationsVendoredCatalog(t *testing.T) {
	catalog := interconnectRemoteLocations()

	if len(catalog.Sources) != 4 {
		t.Errorf("catalogue cites %d source pages, want 4 — one per cloud provider", len(catalog.Sources))
	}
	if catalog.Retrieved == "" {
		t.Error("the catalogue does not record when it was retrieved")
	}
	if got, want := len(catalog.RemoteLocations), 74; got != want {
		t.Errorf("catalogue holds %d remote locations, want %d — regenerate with "+
			"scripts/fetch-gcp-interconnect-remote-locations.sh and move this number in the same commit", got, want)
	}

	byName := map[string]map[string]any{}
	providers := map[string]int{}
	for _, location := range catalog.RemoteLocations {
		name, _ := location["name"].(string)
		if name == "" {
			t.Fatal("a remote location has no name")
		}
		byName[name] = location
		if city, _ := location["city"].(string); city == "" {
			t.Errorf("%s has no metropolitan area; every entry on the pages states one, "+
				"and an empty one means the rowspan carry-forward is broken", name)
		}
		for _, prefix := range []string{"aws-", "azure-", "oci-", "alibaba-"} {
			if len(name) > len(prefix) && name[:len(prefix)] == prefix {
				providers[prefix]++
			}
		}
	}
	if len(providers) != 4 {
		t.Errorf("the catalogue covers %v, and all four provider pages should be in it", providers)
	}

	// The two entries a row-counting parser gets wrong.
	seoul, ok := byName["aws-lgknx"]
	if !ok {
		t.Fatal("aws-lgknx is missing")
	}
	if got := seoul["city"]; got != "Seoul" {
		t.Errorf("aws-lgknx is filed under %v; its city is rowspanned from the entry above and is Seoul", got)
	}
	if _, ok := byName["aws-eqse2-eq"]; !ok {
		t.Error("aws-eqse2-eq is missing; its name carries a sublocation suffix that a " +
			"simpler name pattern rejects")
	}

	// The fields the sources do not state are absent rather than invented.
	for name, location := range byName {
		for _, field := range []string{"continent", "address", "facilityProvider", "remoteService", "lacp"} {
			if _, present := location[field]; present {
				t.Errorf("%s carries %s, which the source pages do not state", name, field)
			}
		}
	}
}
