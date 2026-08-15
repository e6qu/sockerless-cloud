package main

import (
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Cross-resource-group move for Microsoft.Cache/redis. The hook table in
// resource_move.go dispatches Resources_MoveResources here.
//
// An Azure Cache for Redis instance keys its ARM record — and its firewall
// rules, access policies, access-policy assignments, linked servers, patch
// schedules and private endpoint connections — by resource ID, so the whole
// subtree re-keys onto the destination group. The cache's hostname is derived
// from its globally unique name rather than its resource group, so the address
// a Redis client dials is unchanged by the move.
//
// The cache's two access keys are derived from the resource ID, which embeds
// the resource group, so a naive re-key would silently rotate the password
// every Redis client authenticates with. Real Azure never rotates a cache's
// keys on a move, so the material listKeys serves is pinned onto the moved ID;
// the next regenerateKey clears the pin and derives fresh material.

// moveRedisCacheARM re-homes one Azure Cache for Redis instance's ARM plane
// onto a new resource ID, pinning its access keys so the credential an
// operator holds keeps working.
func moveRedisCacheARM(oldID, newID string) {
	cache, ok := redisCaches.Get(oldID)
	if !ok {
		return
	}

	pinAzureKeySlots(oldID, newID, azureKeyMaterial32, "primary", "secondary")

	redisCaches.Delete(oldID)
	cache.ID = newID
	redisCaches.Put(cache.ID, cache)

	oldSub, newSub := oldID+"/", newID+"/"
	rekeyRowsByPrefix(redisFirewallRules, oldSub, newSub, func(f *RedisFirewallRule) *string { return &f.ID })
	for _, store := range []sim.Store[RedisSubResource]{
		redisAccessPolicies,
		redisAccessPolicyAssignments,
		redisLinkedServers,
		redisPatchSchedules,
		redisPrivateConns,
	} {
		rekeyRowsByPrefix(store, oldSub, newSub, func(c *RedisSubResource) *string { return &c.ID })
	}
}
