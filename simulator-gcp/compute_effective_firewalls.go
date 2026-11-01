package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The firewall policy stores getEffectiveFirewalls reads: hierarchical
// policies attach to an organization or folder, network firewall policies to a
// network, and regional network firewall policies to a network within one
// region.
var (
	gcpHierarchicalFirewallPolicies  sim.Store[map[string]any]
	gcpNetworkFirewallPolicies       sim.Store[map[string]any]
	gcpRegionNetworkFirewallPolicies sim.Store[map[string]any]
)

// computeObjectList reads a stored list of JSON objects, which a persisted
// store decodes as []any and an in-memory one keeps as written.
func computeObjectList(value any) []map[string]any {
	switch list := value.(type) {
	case []map[string]any:
		return list
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, entry := range list {
			if object, ok := entry.(map[string]any); ok {
				out = append(out, object)
			}
		}
		return out
	}
	return nil
}

// computeRelativeRef strips the API root from a Compute Engine URL, whatever
// endpoint the client addressed, so a full URL and a partial one name the same
// resource.
func computeRelativeRef(ref string) string {
	if at := strings.Index(ref, "/projects/"); at >= 0 {
		return ref[at+1:]
	}
	return ref
}

// computeNetworkRef is the relative name of the network a reference names; an
// empty reference is the project's default network.
func computeNetworkRef(project, ref string) string {
	return normalizeComputeGlobalNetworkRef(project, gcpDefaultString(ref, "default"))
}

// computeEffectiveTarget is what a firewall policy rule's targets are matched
// against: the network always, and the instance when one is asked about.
type computeEffectiveTarget struct {
	project string
	network string
	region  string
	// instance is nil for a network-wide read.
	instance *ComputeInstance
	zone     string
}

// computeEffectiveVPCFirewalls returns the VPC firewall rules of the target's
// network that apply to it. A rule with targetTags or targetServiceAccounts
// applies only to the instances that carry one of them.
func computeEffectiveVPCFirewalls(target computeEffectiveTarget) []ComputeFirewall {
	rules := []ComputeFirewall{}
	if gcpFirewalls == nil {
		return rules
	}
	for _, fw := range gcpFirewalls.List() {
		network := computeNetworkRef(gcpProjectFromSelfLink(fw.SelfLink), fw.Network)
		if network != target.network {
			continue
		}
		if target.instance != nil {
			if !gcpFirewallTargetsInstance(fw, *target.instance) {
				continue
			}
			if len(fw.TargetServiceAccounts) > 0 && !computeInstanceRunsAs(*target.instance, fw.TargetServiceAccounts) {
				continue
			}
		}
		rules = append(rules, fw)
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return rules[i].Name < rules[j].Name
	})
	return rules
}

func computeInstanceRunsAs(instance ComputeInstance, accounts []string) bool {
	for _, sa := range instance.ServiceAccounts {
		email, _ := sa["email"].(string)
		for _, want := range accounts {
			if email != "" && email == want {
				return true
			}
		}
	}
	return false
}

// computeEffectiveFirewallPolicies returns the firewall policies in force on
// the target: the hierarchical policies associated with the project's
// organization and folders, outermost first, then the network firewall
// policies associated with its network and, when region is set, the regional
// network firewall policies of that region, each in association-priority order.
func computeEffectiveFirewallPolicies(target computeEffectiveTarget) []map[string]any {
	out := []map[string]any{}
	if gcpHierarchicalFirewallPolicies != nil {
		for _, node := range crmOrgPolicyAncestry("projects/" + target.project) {
			if strings.HasPrefix(node, "projects/") {
				continue
			}
			var attached []map[string]any
			for _, policy := range gcpHierarchicalFirewallPolicies.List() {
				for _, association := range computeObjectList(policy["associations"]) {
					if at, _ := association["attachmentTarget"].(string); at == node {
						attached = append(attached, policy)
						break
					}
				}
			}
			sort.Slice(attached, func(i, j int) bool {
				a, _ := attached[i]["name"].(string)
				b, _ := attached[j]["name"].(string)
				return a < b
			})
			for _, policy := range attached {
				out = append(out, computeEffectivePolicyEntry(policy, "HIERARCHY", 0, target))
			}
		}
	}
	type networkAttachment struct {
		policy      map[string]any
		priority    int
		association string
		kind        string
	}
	var attachments []networkAttachment
	collect := func(store sim.Store[map[string]any], kind, prefix string) {
		if store == nil {
			return
		}
		for _, policy := range store.List() {
			link, _ := policy["selfLink"].(string)
			if !strings.HasPrefix(computeRelativeRef(link), prefix) {
				continue
			}
			for _, association := range computeObjectList(policy["associations"]) {
				at, _ := association["attachmentTarget"].(string)
				if at == "" || computeNetworkRef(target.project, at) != target.network {
					continue
				}
				priority := 1000
				if _, set := association["priority"]; set {
					priority = computeRulePriority(association)
				}
				name, _ := association["name"].(string)
				attachments = append(attachments, networkAttachment{policy: policy, priority: priority, association: name, kind: kind})
				break
			}
		}
	}
	collect(gcpNetworkFirewallPolicies, "NETWORK", "projects/"+target.project+"/global/firewallPolicies/")
	if target.region != "" {
		collect(gcpRegionNetworkFirewallPolicies, "NETWORK_REGIONAL",
			"projects/"+target.project+"/regions/"+target.region+"/firewallPolicies/")
	}
	sort.SliceStable(attachments, func(i, j int) bool {
		if attachments[i].priority != attachments[j].priority {
			return attachments[i].priority < attachments[j].priority
		}
		return attachments[i].association < attachments[j].association
	})
	for _, a := range attachments {
		out = append(out, computeEffectivePolicyEntry(a.policy, a.kind, a.priority, target))
	}
	return out
}

func computeEffectivePolicyEntry(policy map[string]any, kind string, priority int, target computeEffectiveTarget) map[string]any {
	entry := map[string]any{"type": kind}
	for _, field := range []string{"name", "shortName", "displayName"} {
		if value, _ := policy[field].(string); value != "" {
			entry[field] = value
		}
	}
	if kind != "HIERARCHY" {
		entry["priority"] = priority
	}
	entry["rules"] = computeEffectivePolicyRules(computeObjectList(policy["rules"]), target)
	if mirroring := computeObjectList(policy["packetMirroringRules"]); len(mirroring) > 0 {
		entry["packetMirroringRules"] = computeEffectivePolicyRules(mirroring, target)
	}
	return entry
}

// computeEffectivePolicyRules keeps the rules that reach the target. A rule
// naming targetResources reaches only those networks; for an instance, a rule
// naming targetServiceAccounts or targetSecureTags reaches only the instances
// running as one of those accounts or carrying one of those tags.
func computeEffectivePolicyRules(rules []map[string]any, target computeEffectiveTarget) []map[string]any {
	out := []map[string]any{}
	for _, rule := range rules {
		if resources := computeStringList(rule["targetResources"]); len(resources) > 0 {
			reached := false
			for _, resource := range resources {
				if computeNetworkRef(target.project, resource) == target.network {
					reached = true
					break
				}
			}
			if !reached {
				continue
			}
		}
		if target.instance != nil {
			if accounts := computeStringList(rule["targetServiceAccounts"]); len(accounts) > 0 && !computeInstanceRunsAs(*target.instance, accounts) {
				continue
			}
			if tags := computeObjectList(rule["targetSecureTags"]); len(tags) > 0 && !computeInstanceCarriesSecureTag(target, tags) {
				continue
			}
		}
		out = append(out, rule)
	}
	sort.SliceStable(out, func(i, j int) bool { return computeRulePriority(out[i]) < computeRulePriority(out[j]) })
	return out
}

func computeStringList(value any) []string {
	switch list := value.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, entry := range list {
			if s, ok := entry.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// computeInstanceCarriesSecureTag reports whether a tag value the rule names is
// bound to the instance through Resource Manager, by its full resource name
// spelled with the instance's id or its name.
func computeInstanceCarriesSecureTag(target computeEffectiveTarget, tags []map[string]any) bool {
	instance := target.instance
	base := "//compute.googleapis.com/projects/" + target.project + "/zones/" + target.zone + "/instances/"
	held := map[string]bool{}
	for _, ref := range []string{instance.Id, instance.Name} {
		if ref == "" {
			continue
		}
		for _, tag := range crmEffectiveTags(base + ref) {
			held[tag.TagValue] = true
			held[tag.NamespacedTagValue] = true
		}
	}
	for _, tag := range tags {
		if name, _ := tag["name"].(string); name != "" && held[name] {
			return true
		}
	}
	return false
}

func writeComputeEffectiveFirewalls(w http.ResponseWriter, target computeEffectiveTarget) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"firewalls":       computeEffectiveVPCFirewalls(target),
		"firewallPolicys": computeEffectiveFirewallPolicies(target),
	})
}
