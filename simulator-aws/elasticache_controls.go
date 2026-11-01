package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A cache created with an AUTH token requires it; one created without
// accepts every client.
func ecInitialTokens(token string) []string {
	if token == "" {
		return []string{}
	}
	return []string{token}
}

// DeleteCacheCluster, DeleteReplicationGroup and DeleteServerlessCache take a
// cache that is available, or one whose creation failed.
func ecDeletable(status string) bool {
	return status == "available" || status == "create-failed"
}

// ecUpgradeVersion resolves the EngineVersion a modify names. A cache
// upgrades to a newer engine version but never downgrades; the result names
// no image when the version does not change.
func ecUpgradeVersion(engine, current, requested string) (ecEngineVersion, string, bool) {
	if requested == "" {
		return ecEngineVersion{Version: current}, "", true
	}
	resolved, known := ecResolveEngineVersion(engine, requested)
	if !known {
		return ecEngineVersion{}, fmt.Sprintf("Cannot find version %s for %s", requested, engine), false
	}
	switch compareECVersions(resolved.Version, current) {
	case 0:
		return ecEngineVersion{Version: current}, "", true
	case -1:
		return ecEngineVersion{}, fmt.Sprintf("Cannot downgrade %s from %s to %s; create the cache anew with the earlier version.", engine, current, resolved.Version), false
	}
	return resolved, "", true
}

func compareECVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(left), len(right)); i++ {
		var x, y int
		if i < len(left) {
			x, _ = strconv.Atoi(left[i])
		}
		if i < len(right) {
			y, _ = strconv.Atoi(right[i])
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// ecAutoDiscovery answers ElastiCache's config get cluster at every endpoint
// of a Memcached cluster: the configuration's version on one line, then each
// available node as hostname|ip-address|port in order of hostname. The node
// endpoint's address is both its hostname and its address.
func ecAutoDiscovery(id string) func([]string) ([]byte, bool) {
	return func(command []string) ([]byte, bool) {
		if len(command) != 3 || command[0] != "config" || command[1] != "get" || command[2] != "cluster" {
			return nil, false
		}
		c, _ := ecClusters.Get(id)
		var nodes []string
		for _, node := range c.CacheNodes {
			if node.CacheNodeStatus == "available" && node.Address != "" {
				nodes = append(nodes, fmt.Sprintf("%s|%s|%d", node.Address, node.Address, node.Port))
			}
		}
		sort.Strings(nodes)
		body := fmt.Sprintf("%d\n%s\n", c.ConfigVersion, strings.Join(nodes, " "))
		return fmt.Appendf(nil, "CONFIG cluster 0 %d\r\n%s\r\nEND\r\n", len(body), body), true
	}
}
