package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
)

// azureKeyGenRow tracks how many times one key slot of a resource has been
// regenerated, so a regenerate call yields genuinely new key material and a
// later listKeys observes the rotation — exactly what real Azure does. The
// store is keyed "<resourceID>|<slot>" (mirroring the Cosmos DB and Event Grid
// key-generation stores) and durable, so a rotation is not silently undone by
// a SIM_PERSIST restart. An explicit key value (the optional "key" field of
// the Event Hubs / Service Bus RegenerateKeys actions) is stored verbatim and
// served until the slot is next auto-rotated.
type azureKeyGenRow struct {
	N   int    `json:"n"`
	Key string `json:"key,omitempty"`
}

// azureKeyGens is shared by the Microsoft.Storage, Azure Cache for Redis,
// Event Hubs and Service Bus key surfaces; full ARM resource IDs keep the
// rows disjoint. Every register function of those surfaces assigns it from
// the same table so registration order does not matter.
var azureKeyGens sim.Store[azureKeyGenRow]

func makeAzureKeyGens(srv *sim.Server) {
	azureKeyGens = sim.MakeStore[azureKeyGenRow](srv.DB(), "azure_key_generations")
}

// azureKeyGenSeed returns the derivation seed for a key slot: the bare slot
// name before any rotation, "<slot>-gen<N>" after N rotations.
func azureKeyGenSeed(id, slot string) (seed string, explicit string) {
	g, _ := azureKeyGens.Get(id + "|" + slot)
	if g.Key != "" {
		return "", g.Key
	}
	if g.N == 0 {
		return slot, ""
	}
	return fmt.Sprintf("%s-gen%d", slot, g.N), ""
}

// azureKeyMaterial32 returns the current 44-character base64 key for a
// resource's key slot (the SAS-key shape Redis, Event Hubs and Service Bus
// use), reflecting every rotation performed so far.
func azureKeyMaterial32(id, slot string) string {
	seed, explicit := azureKeyGenSeed(id, slot)
	if explicit != "" {
		return explicit
	}
	return simListKey32(id, seed)
}

// azureKeyMaterial64 returns the current 88-character base64 key for a
// resource's key slot (the 512-bit Storage account access-key shape),
// reflecting every rotation performed so far.
func azureKeyMaterial64(id, slot string) string {
	seed, explicit := azureKeyGenSeed(id, slot)
	if explicit != "" {
		return explicit
	}
	return simListKey64(id, seed)
}

// azureBumpKeyGen advances a key slot's rotation counter. A non-empty
// explicit value pins the slot to caller-supplied key material (the Event
// Hubs / Service Bus RegenerateKeys "key" field); an empty value clears any
// previous pin so the slot returns to derived material.
func azureBumpKeyGen(id, slot, explicit string) {
	k := id + "|" + slot
	g, _ := azureKeyGens.Get(k)
	g.N++
	g.Key = explicit
	azureKeyGens.Put(k, g)
}

// azureDropKeyGens removes a deleted resource's rotation counters so a later
// resource created under the same ID starts from fresh key material.
func azureDropKeyGens(id string, slots ...string) {
	for _, slot := range slots {
		azureKeyGens.Delete(id + "|" + slot)
	}
}

// pinAzureKeySlots carries a resource's current key material onto the resource
// ID a cross-resource-group move is about to create. Derived material is
// seeded by the resource ID, which embeds the resource group, so a move that
// only re-keyed the record would silently rotate every credential an operator
// holds — which real Azure never does. Pinning writes the value the slot
// serves today as the moved slot's explicit material; the next regenerateKey
// clears the pin and derives fresh material, so the rotation contract is
// unchanged by the move. material reads the slot in the width its surface
// serves (azureKeyMaterial32 for the SAS-key shape, azureKeyMaterial64 for the
// Storage account-key shape).
func pinAzureKeySlots(oldID, newID string, material func(id, slot string) string, slots ...string) {
	for _, slot := range slots {
		pinned := material(oldID, slot)
		gen, _ := azureKeyGens.Get(oldID + "|" + slot)
		azureKeyGens.Delete(oldID + "|" + slot)
		gen.Key = pinned
		azureKeyGens.Put(newID+"|"+slot, gen)
	}
}
