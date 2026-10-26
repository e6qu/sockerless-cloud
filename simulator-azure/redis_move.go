package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// Cross-resource-group move for Microsoft.Cache/redis (dispatched from
// resource_move.go). The cache and its children key by resource ID and re-key
// onto the destination group; the hostname derives from the globally unique
// name, so it is unchanged. The access keys derive from the resource ID, yet
// Azure never rotates them on a move: pin the served material until the next
// regenerateKey. The engine follows the cache onto the new ID.

func moveRedisCacheARM(oldID, newID string) {
	cache, ok := redisCaches.Get(oldID)
	if !ok {
		return
	}

	pinAzureKeySlots(oldID, newID, azureKeyMaterial32, "primary", "secondary")

	redisCaches.Delete(oldID)
	cache.ID = newID
	redisCaches.Put(cache.ID, cache)
	redisMoveEngine(oldID, newID)

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
