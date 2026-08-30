package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// A reservation's resize and the blocks its capacity is held in. The blocks are
// derived from the reservation's own count, so a resize is visible in them —
// which is what keeps the two from disagreeing.
func TestCompute_ReservationResizeAndBlocks(t *testing.T) {
	svc := computeService(t)
	const project, zone, name = "reservation-project", "us-central1-a", "batch"

	_, err := svc.Reservations.Insert(project, zone, &compute.Reservation{
		Name: name,
		SpecificReservation: &compute.AllocationSpecificSKUReservation{
			Count: 4,
			InstanceProperties: &compute.AllocationSpecificSKUAllocationReservedInstanceProperties{
				MachineType: "n1-standard-1",
			},
		},
	}).Do()
	require.NoError(t, err)

	blocks, err := svc.ReservationBlocks.List(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, blocks.Items, 1, "four machines fit in one block")
	assert.Equal(t, int64(4), blocks.Items[0].Count)

	// A resize is reflected in the blocks, because they are read from the count.
	_, err = svc.Reservations.Resize(project, zone, name,
		&compute.ReservationsResizeRequest{SpecificSkuCount: 40}).Do()
	require.NoError(t, err)

	blocks, err = svc.ReservationBlocks.List(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, blocks.Items, 3, "forty machines need three sixteen-machine blocks")
	assert.Equal(t, int64(16), blocks.Items[0].Count)
	assert.Equal(t, int64(8), blocks.Items[2].Count, "the last block holds the remainder")

	got, err := svc.ReservationBlocks.Get(project, zone, name, blocks.Items[0].Name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.Resource)

	// A block the reservation does not hold reports itself.
	_, err = svc.ReservationBlocks.Get(project, zone, name, "absent-block").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// A resize to nothing is refused.
	_, err = svc.Reservations.Resize(project, zone, name,
		&compute.ReservationsResizeRequest{SpecificSkuCount: 0}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "greater than zero")
}
