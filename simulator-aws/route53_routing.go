package main

import (
	"math/rand/v2"
	"net/netip"
	"sort"
	"strings"
)

// route53Querier is what a routing policy chooses by.
type route53Querier struct {
	region string
	addr   netip.Addr
	// healthy, when set, is the health the zone's owning service gives a
	// record set, on top of the record set's own Route 53 health check.
	healthy func(R53ResourceRecordSet) bool
}

func (q route53Querier) recordHealthy(set R53ResourceRecordSet) bool {
	return r53HealthCheckHealthy(set.HealthCheckId) && (q.healthy == nil || q.healthy(set))
}

// route53MaxMultivalue is how many healthy record sets a multivalue answer
// returns at most.
const route53MaxMultivalue = 8

// route53Route chooses, for each name and type, the record sets Route 53
// answers with under their routing policy, consulting the health checks
// associated with them. Record sets without a SetIdentifier are simple
// routing and are answered as they are.
func route53Route(sets []R53ResourceRecordSet, q route53Querier) []R53ResourceRecordSet {
	type key struct{ name, rtype string }
	groups := map[key][]R53ResourceRecordSet{}
	var order []key
	for _, set := range sets {
		k := key{route53OwnerName(set.Name), strings.ToUpper(set.Type)}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], set)
	}
	var out []R53ResourceRecordSet
	for _, k := range order {
		group := groups[k]
		if group[0].SetIdentifier == "" {
			out = append(out, group...)
			continue
		}
		out = append(out, route53Choose(group, q)...)
	}
	return out
}

func route53Choose(group []R53ResourceRecordSet, q route53Querier) []R53ResourceRecordSet {
	first := group[0]
	switch {
	case first.Failover != "":
		return route53Failover(group, q)
	case first.Weight != nil:
		return route53Weighted(group, q)
	case first.MultiValueAnswer != nil:
		return route53Multivalue(group, q)
	case first.Region != "":
		return route53Latency(group, q)
	case first.GeoLocation != nil:
		return route53Default(group, q, func(set R53ResourceRecordSet) bool {
			return set.GeoLocation != nil && set.GeoLocation.CountryCode == "*"
		})
	case first.CidrRoutingConfig != nil:
		return route53IPBased(group, q)
	}
	return group
}

// route53Healthy keeps the record sets whose health checks pass; when none
// does, Route 53 answers as if all were healthy.
func route53Healthy(group []R53ResourceRecordSet, q route53Querier) []R53ResourceRecordSet {
	var healthy []R53ResourceRecordSet
	for _, set := range group {
		if q.recordHealthy(set) {
			healthy = append(healthy, set)
		}
	}
	if len(healthy) == 0 {
		return group
	}
	return healthy
}

// route53Failover answers with the primary while it is healthy and the
// secondary otherwise; with both unhealthy, the primary.
func route53Failover(group []R53ResourceRecordSet, q route53Querier) []R53ResourceRecordSet {
	var primary, secondary []R53ResourceRecordSet
	for _, set := range group {
		if strings.EqualFold(set.Failover, "PRIMARY") {
			primary = append(primary, set)
		} else {
			secondary = append(secondary, set)
		}
	}
	if len(primary) > 0 && q.recordHealthy(primary[0]) {
		return primary[:1]
	}
	if len(secondary) > 0 && (len(primary) == 0 || q.recordHealthy(secondary[0])) {
		return secondary[:1]
	}
	return primary[:1]
}

// route53Weighted answers with one healthy record set, chosen in proportion
// to its weight; when every weight is zero, each is equally likely.
func route53Weighted(group []R53ResourceRecordSet, q route53Querier) []R53ResourceRecordSet {
	candidates := route53Healthy(group, q)
	var total int64
	for _, set := range candidates {
		total += *set.Weight
	}
	if total == 0 {
		return []R53ResourceRecordSet{candidates[rand.IntN(len(candidates))]} //nolint:gosec
	}
	pick := rand.Int64N(total) //nolint:gosec
	for _, set := range candidates {
		if pick < *set.Weight {
			return []R53ResourceRecordSet{set}
		}
		pick -= *set.Weight
	}
	return candidates[:1]
}

func route53Multivalue(group []R53ResourceRecordSet, q route53Querier) []R53ResourceRecordSet {
	candidates := route53Healthy(group, q)
	if len(candidates) > route53MaxMultivalue {
		candidates = candidates[:route53MaxMultivalue]
	}
	return candidates
}

// route53Latency answers with the healthy record set in the querier's Region,
// or the first healthy one by Region name when the querier's Region has none:
// every Region is equally near a single-host simulator.
func route53Latency(group []R53ResourceRecordSet, q route53Querier) []R53ResourceRecordSet {
	candidates := route53Healthy(group, q)
	for _, set := range candidates {
		if set.Region == q.region {
			return []R53ResourceRecordSet{set}
		}
	}
	sorted := append([]R53ResourceRecordSet(nil), candidates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Region < sorted[j].Region })
	return sorted[:1]
}

// route53Default answers with the default record set of a location-based
// policy — the querier's location is the simulator's host, which no record
// set names — or with nothing when the name has no default.
func route53Default(group []R53ResourceRecordSet, q route53Querier, isDefault func(R53ResourceRecordSet) bool) []R53ResourceRecordSet {
	for _, set := range route53Healthy(group, q) {
		if isDefault(set) {
			return []R53ResourceRecordSet{set}
		}
	}
	return nil
}

// route53IPBased answers with the record set whose CIDR location holds the
// querier's address, the most specific block winning, or the default
// location "*".
func route53IPBased(group []R53ResourceRecordSet, q route53Querier) []R53ResourceRecordSet {
	addr := q.addr
	candidates := route53Healthy(group, q)
	best, bestBits := -1, -1
	for i, set := range candidates {
		cfg := set.CidrRoutingConfig
		if cfg == nil || !addr.IsValid() {
			continue
		}
		stored, ok := r53CidrCollections.Get(cfg.CollectionId)
		if !ok {
			continue
		}
		for _, block := range stored.Blocks[cfg.LocationName] {
			prefix, err := netip.ParsePrefix(block)
			if err == nil && prefix.Contains(addr.Unmap()) && prefix.Bits() > bestBits {
				best, bestBits = i, prefix.Bits()
			}
		}
	}
	if best >= 0 {
		return []R53ResourceRecordSet{candidates[best]}
	}
	return route53Default(group, q, func(set R53ResourceRecordSet) bool {
		return set.CidrRoutingConfig != nil && set.CidrRoutingConfig.LocationName == "*"
	})
}
