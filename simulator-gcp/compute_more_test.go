package main

import "testing"

// Compute Discovery revision 20260729 added a standardized ResourceMetadata
// beside three resources — an accelerator type, a reservation and a future
// reservation. A client reading it gets the canonical AIP-123 type name, which
// is derivable from the resource's own kind and so cannot drift from it.
//
// The member is stamped only where the schema declares it. Emitting it beside a
// resource whose schema has no such member would be an invented field, which is
// the same defect as omitting a real one, facing the other way.
func TestComputeResourceMetadataNamesTheCanonicalType(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{"compute#acceleratorType", "compute.googleapis.com/AcceleratorType"},
		{"compute#reservation", "compute.googleapis.com/Reservation"},
		{"compute#futureReservation", "compute.googleapis.com/FutureReservation"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			got := computeResourceMetadata(tc.kind)
			if got["resourceType"] != tc.want {
				t.Errorf("resourceType = %v, want %q", got["resourceType"], tc.want)
			}
			// apiVersion is left absent rather than guessed: the Discovery
			// document gives its shape by example without stating what Compute
			// v1 answers with.
			if _, invented := got["apiVersion"]; invented {
				t.Error("apiVersion was stamped with a value the document does not state")
			}
		})
	}
}
