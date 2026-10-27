package main

import "testing"

// ResourceMetadata carries the canonical AIP-123 type name, derived from the
// resource's own kind, and only on the three schemas that declare it: an
// accelerator type, a reservation and a future reservation.
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
			// apiVersion stays absent: the Discovery document shows it only by example.
			if _, invented := got["apiVersion"]; invented {
				t.Error("apiVersion was stamped with a value the document does not state")
			}
		})
	}
}
