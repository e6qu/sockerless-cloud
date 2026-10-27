package main

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A pool runs as many runs at once as it has agents; the rest wait Queued.
var (
	acrPoolMu      sync.Mutex
	acrPoolBusy    = map[string]int{}
	acrPoolChanged = make(chan struct{})
)

// acrAgentPoolCapacity is the pool's agent `count`, one when it names none.
func acrAgentPoolCapacity(poolID string) int {
	pool, ok := acrAgentPools.Get(poolID)
	if !ok {
		return 0
	}
	if count, ok := pool.Properties["count"].(float64); ok {
		return int(count)
	}
	return 1
}

func acrAcquireAgent(ctx context.Context, poolID string) error {
	for {
		acrPoolMu.Lock()
		if acrPoolBusy[poolID] < acrAgentPoolCapacity(poolID) {
			acrPoolBusy[poolID]++
			acrPoolMu.Unlock()
			return nil
		}
		changed := acrPoolChanged
		acrPoolMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func acrReleaseAgent(poolID string) {
	acrPoolMu.Lock()
	defer acrPoolMu.Unlock()
	acrPoolBusy[poolID]--
	acrPoolNotify()
}

// acrPoolNotify requires the caller to hold acrPoolMu.
func acrPoolNotify() {
	close(acrPoolChanged)
	acrPoolChanged = make(chan struct{})
}

func handleACRAgentPoolQueueStatus(w http.ResponseWriter, r *http.Request) {
	regID, ok := acrRegistryID(r)
	if !ok {
		acrRegistryNotFound(w, r)
		return
	}
	pool := sim.PathParam(r, "agentPoolName")
	if _, ok := acrAgentPools.Get(regID + "/agentPools/" + pool); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.ContainerRegistry/registries/agentPools/%s' under registry '%s' was not found.",
			pool, sim.PathParam(r, "registryName"))
		return
	}
	prefix := regID + "/runs/"
	queued := acrRuns.Filter(func(run acrRun) bool {
		return strings.HasPrefix(run.ID, prefix) && run.Properties.AgentPoolName == pool && run.Properties.Status == "Queued"
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"count": len(queued)})
}
