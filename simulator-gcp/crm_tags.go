package main

import (
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

var (
	crmTagKeys     sim.Store[CRMTagKey]
	crmTagValues   sim.Store[CRMTagValue]
	crmTagBindings sim.Store[CRMTagBinding]
)

// CRMEffectiveTag mirrors the cloudresourcemanager#EffectiveTag (v3) resource.
type CRMEffectiveTag struct {
	TagValue           string `json:"tagValue,omitempty"`
	NamespacedTagValue string `json:"namespacedTagValue,omitempty"`
	TagKey             string `json:"tagKey,omitempty"`
	NamespacedTagKey   string `json:"namespacedTagKey,omitempty"`
	TagKeyParentName   string `json:"tagKeyParentName,omitempty"`
	Inherited          bool   `json:"inherited"`
}

// crmTagTarget is the hierarchy node a tag binding's parent names, spelled the
// way the hierarchy is walked: projects/{projectId} for a project named by
// either its ID or its number, folders/{id} and organizations/{id} as they
// are, and any other resource by its full resource name.
func crmTagTarget(fullResourceName string) string {
	node, ok := strings.CutPrefix(fullResourceName, "//cloudresourcemanager.googleapis.com/")
	if !ok {
		return fullResourceName
	}
	return crmCanonicalHierarchyNode(node)
}

// crmTagAncestry is the resource a full resource name denotes and its
// ancestors, nearest first: the resource itself, then the project it lives in
// when its name places it in one, then that project's folders and
// organization.
func crmTagAncestry(fullResourceName string) []string {
	target := crmTagTarget(fullResourceName)
	nodes := []string{target}
	hierarchyRoot := target
	if !strings.HasPrefix(target, "projects/") && !strings.HasPrefix(target, "folders/") && !strings.HasPrefix(target, "organizations/") {
		_, path, ok := strings.Cut(strings.TrimPrefix(fullResourceName, "//"), "/")
		if !ok {
			return nodes
		}
		project := resourceProject(path)
		if project == "" || project == "_" {
			return nodes
		}
		hierarchyRoot = crmCanonicalHierarchyNode("projects/" + project)
		nodes = append(nodes, hierarchyRoot)
	} else if strings.Contains(strings.TrimPrefix(target, "projects/"), "/") {
		return nodes
	}
	ancestry := crmOrgPolicyAncestry(hierarchyRoot)
	for i := len(ancestry) - 2; i >= 0; i-- {
		nodes = append(nodes, ancestry[i])
	}
	return nodes
}

// crmTagBindingsByTarget and crmTagValuesByNamespacedName answer the lookups
// IAM condition evaluation makes on every request without decoding each store.
var (
	crmTagBindingsByTarget       sim.GenerationIndex[CRMTagBinding]
	crmTagValuesByNamespacedName sim.GenerationIndex[CRMTagValue]
)

// crmEffectiveTags are the tags in force on a resource: those bound to it
// and those it inherits from its ancestors in the resource hierarchy. A value
// bound nearer the resource overrides an inherited value of the same key.
func crmEffectiveTags(fullResourceName string) []CRMEffectiveTag {
	out := []CRMEffectiveTag{}
	if fullResourceName == "" || crmTagBindings == nil {
		return out
	}
	keysSeen := map[string]bool{}
	for depth, node := range crmTagAncestry(fullResourceName) {
		bound := crmTagBindingsByTarget.LookupAll(crmTagBindings, node, func(b CRMTagBinding) []string { return []string{crmTagTarget(b.Parent)} })
		sort.Slice(bound, func(i, j int) bool { return bound[i].Name < bound[j].Name })
		for _, b := range bound {
			tag := CRMEffectiveTag{TagValue: b.TagValue, NamespacedTagValue: b.TagValueNamespacedName, Inherited: depth > 0}
			value, ok := crmTagValues.Get(b.TagValue)
			if !ok && b.TagValueNamespacedName != "" {
				for _, candidate := range crmTagValuesByNamespacedName.LookupAll(crmTagValues, b.TagValueNamespacedName, func(v CRMTagValue) []string { return []string{v.NamespacedName} }) {
					value, ok = candidate, true
					tag.TagValue = candidate.Name
				}
			}
			if ok {
				tag.TagKey = value.Parent
				if tag.NamespacedTagValue == "" {
					tag.NamespacedTagValue = value.NamespacedName
				}
				if key, found := crmTagKeys.Get(value.Parent); found {
					tag.NamespacedTagKey = key.NamespacedName
					tag.TagKeyParentName = key.Parent
				}
			}
			keyID := tag.TagKey
			if keyID == "" {
				keyID = b.TagValue
			}
			if keysSeen[keyID] {
				continue
			}
			keysSeen[keyID] = true
			out = append(out, tag)
		}
	}
	return out
}
