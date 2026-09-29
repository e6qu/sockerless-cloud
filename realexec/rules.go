package realexec

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// PortRange parses a destination port specification the way AWS, Google Cloud
// and Azure write one: "" or "*" for every port (0, 0), "N", or "N-M".
func PortRange(spec string) (from, to int, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "*" {
		return 0, 0, nil
	}
	lo, hi, isRange := strings.Cut(spec, "-")
	if !isRange {
		hi = lo
	}
	if from, err = parsePort(lo); err != nil {
		return 0, 0, fmt.Errorf("invalid port range %q: %w", spec, err)
	}
	if to, err = parsePort(hi); err != nil {
		return 0, 0, fmt.Errorf("invalid port range %q: %w", spec, err)
	}
	if from > to {
		return 0, 0, fmt.Errorf("invalid port range %q: start exceeds end", spec)
	}
	return from, to, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%q is not a port number", s)
	}
	if n < 0 || n > 65535 {
		return 0, fmt.Errorf("port %d is outside 0-65535", n)
	}
	return n, nil
}

// ExpandRules crosses every source prefix with every port specification into
// one PacketRule each. No port specifications means every port.
func ExpandRules(protocol string, sources, ports []string, action string) ([]PacketRule, error) {
	if len(ports) == 0 {
		ports = []string{""}
	}
	ranges := make([][2]int, 0, len(ports))
	for _, port := range ports {
		from, to, err := PortRange(port)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, [2]int{from, to})
	}
	rules := make([]PacketRule, 0, len(sources)*len(ranges))
	for _, source := range sources {
		for _, r := range ranges {
			rules = append(rules, PacketRule{
				Protocol:   protocol,
				SourceCIDR: source,
				FromPort:   r[0],
				ToPort:     r[1],
				Action:     action,
			})
		}
	}
	return rules, nil
}

// PrioritizedRule is a packet rule from a rule set evaluated in priority
// order, the lower value first.
type PrioritizedRule struct {
	Priority int
	Rule     PacketRule
}

// FlattenByPriority orders a rule set for first-match evaluation. At equal
// priority a drop precedes an accept, which is how Google Cloud resolves a
// deny and an allow firewall rule of the same priority; otherwise the input
// order stands.
func FlattenByPriority(rules []PrioritizedRule) []PacketRule {
	ordered := append([]PrioritizedRule(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority < ordered[j].Priority
		}
		return isDrop(ordered[i].Rule) && !isDrop(ordered[j].Rule)
	})
	out := make([]PacketRule, len(ordered))
	for i, r := range ordered {
		out[i] = r.Rule
	}
	return out
}

func isDrop(rule PacketRule) bool {
	return strings.EqualFold(rule.Action, "drop")
}
