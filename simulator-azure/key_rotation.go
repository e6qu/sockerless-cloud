package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
)

// azureKeyGenRow counts a key slot's regenerations, so a regenerate yields new
// material and listKeys observes it. Keyed "<resourceID>|<slot>" and durable
// across a SIM_PERSIST restart. An explicit value (the optional "key" of Event
// Hubs and Service Bus RegenerateKeys) is served verbatim until the next
// automatic rotation.
type azureKeyGenRow struct {
	N   int    `json:"n"`
	Key string `json:"key,omitempty"`
}

// Shared by the Storage, Azure Cache for Redis, Event Hubs and Service Bus key
// surfaces; full resource IDs keep rows disjoint. Each register function
// assigns it from the same table, so registration order does not matter.
var azureKeyGens sim.Store[azureKeyGenRow]

func makeAzureKeyGens(srv *sim.Server) {
	azureKeyGens = sim.MakeStore[azureKeyGenRow](srv.DB(), "azure_key_generations")
}

// The bare slot name before any rotation, "<slot>-gen<N>" after N.
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

// The 44-character base64 SAS-key shape Redis, Event Hubs and Service Bus use.
func azureKeyMaterial32(id, slot string) string {
	seed, explicit := azureKeyGenSeed(id, slot)
	if explicit != "" {
		return explicit
	}
	return simListKey32(id, seed)
}

// The 88-character base64 Storage account-key shape.
func azureKeyMaterial64(id, slot string) string {
	seed, explicit := azureKeyGenSeed(id, slot)
	if explicit != "" {
		return explicit
	}
	return simListKey64(id, seed)
}

// A non-empty explicit value pins the slot; an empty one returns it to
// derived material.
func azureBumpKeyGen(id, slot, explicit string) {
	k := id + "|" + slot
	g, _ := azureKeyGens.Get(k)
	g.N++
	g.Key = explicit
	azureKeyGens.Put(k, g)
}

// A later resource with the same ID starts from fresh keys.
func azureDropKeyGens(id string, slots ...string) {
	for _, slot := range slots {
		azureKeyGens.Delete(id + "|" + slot)
	}
}

// pinAzureKeySlots carries current key material onto the resource ID a move
// creates. Derived material is seeded by the resource ID, which embeds the
// resource group, yet Azure never rotates keys on a move; the next
// regenerateKey clears the pin. material reads the slot in its surface's
// width (azureKeyMaterial32 or azureKeyMaterial64).
func pinAzureKeySlots(oldID, newID string, material func(id, slot string) string, slots ...string) {
	for _, slot := range slots {
		pinned := material(oldID, slot)
		gen, _ := azureKeyGens.Get(oldID + "|" + slot)
		azureKeyGens.Delete(oldID + "|" + slot)
		gen.Key = pinned
		azureKeyGens.Put(newID+"|"+slot, gen)
	}
}
