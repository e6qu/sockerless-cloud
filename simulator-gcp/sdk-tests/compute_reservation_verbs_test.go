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

	// The sub-blocks a block is divided into, which is the unit maintenance is
	// performed on and a fault reported against.
	subs, err := svc.ReservationSubBlocks.List(project, zone,
		"reservations/"+name+"/reservationBlocks/"+blocks.Items[0].Name).Do()
	require.NoError(t, err)
	require.Len(t, subs.Items, 2, "a sixteen-machine block is two sub-blocks")
	assert.Equal(t, int64(8), subs.Items[0].Count)

	// A slot is one machine of a sub-block, and the level health is reported
	// at. The three levels agree because each is derived from the one above.
	subParent := "reservations/" + name + "/reservationBlocks/" + blocks.Items[0].Name
	slots, err := svc.ReservationSlots.List(project, zone, subParent+"/reservationSubBlocks/"+subs.Items[0].Name).Do()
	require.NoError(t, err)
	require.Len(t, slots.Items, 8, "an eight-machine sub-block is eight slots")
	assert.Equal(t, "ACTIVE", slots.Items[0].State)

	// getHealth answers with an Operation, which is what the document declares.
	op, err := svc.ReservationSlots.GetHealth(project, zone,
		subParent+"/reservationSubBlocks/"+subs.Items[0].Name, slots.Items[0].Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "getHealth", op.OperationType)

	// A slot the sub-block does not hold reports itself rather than answering.
	_, err = svc.ReservationSlots.GetHealth(project, zone,
		subParent+"/reservationSubBlocks/"+subs.Items[0].Name, "absent").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// A block's IAM policy goes to the same store every other Google IAM
	// surface reads, so setting it is visible to the read beside it.
	_, err = svc.ReservationBlocks.SetIamPolicy(project, zone, name, blocks.Items[0].Name,
		&compute.ZoneSetNestedPolicyRequest{Policy: &compute.Policy{
			Bindings: []*compute.Binding{{Role: "roles/compute.viewer", Members: []string{"user:a@example.com"}}},
		}}).Do()
	require.NoError(t, err)
	policy, err := svc.ReservationBlocks.GetIamPolicy(project, zone, name, blocks.Items[0].Name).Do()
	require.NoError(t, err)
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, "roles/compute.viewer", policy.Bindings[0].Role)

	// A resize to nothing is refused.
	_, err = svc.Reservations.Resize(project, zone, name,
		&compute.ReservationsResizeRequest{SpecificSkuCount: 0}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "greater than zero")
}
