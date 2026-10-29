package main

import (
	"strings"
)

// gcpPolicyChain is every policy that governs a resource named by its
// relative name, outermost first: its organization's, its folders', its
// project's, then the policy of each resource on the path down to it, as
// the policy of a key ring governs the keys it holds.
func gcpPolicyChain(resource string) []IAMPolicy {
	if full, ok := strings.CutPrefix(resource, "//"); ok {
		// A full resource name, //cloudresourcemanager.googleapis.com/projects/p.
		_, resource, _ = strings.Cut(full, "/")
	}
	segments := strings.Split(resource, "/")
	var policies []IAMPolicy
	root := 2
	switch segments[0] {
	case "projects", "namespaces":
		if len(segments) < 2 {
			return gcpNodePolicies(crmDefaultOrganization)
		}
		project := segments[1]
		if project == "-" && len(segments) >= 4 && segments[2] == "serviceAccounts" {
			if sa, ok := auditLookupServiceAccount(resource); ok {
				project = sa.ProjectId
			} else if strings.Contains(segments[3], "@") {
				// An account that no longer exists still names its project.
				project = gcpProjectFromEmail(segments[3])
			}
		}
		policies = gcpHierarchyPolicies(project)
	case "organizations", "folders":
		if len(segments) < 2 {
			return nil
		}
		policies = gcpNodePolicies(segments[0] + "/" + segments[1])
	case "tagKeys", "tagValues":
		policies = gcpNodePolicies(gateTagParent(resource))
		root = 0
	default:
		policies = gcpNodePolicies(crmDefaultOrganization)
		root = 0
	}
	for i := root + 2; i <= len(segments); i += 2 {
		policies = append(policies, gateResourcePolicies(strings.Join(segments[:i], "/"))...)
	}
	return policies
}

// gateTagParent is the hierarchy node a tag key or value belongs under.
func gateTagParent(resource string) string {
	segments := strings.Split(resource, "/")
	if len(segments) < 2 {
		return crmDefaultOrganization
	}
	name := strings.Join(segments[:2], "/")
	if strings.HasPrefix(name, "tagValues/") && crmTagValues != nil {
		if value, ok := crmTagValues.Get(name); ok {
			name = value.Parent
		}
	}
	if strings.HasPrefix(name, "tagKeys/") && crmTagKeys != nil {
		if key, ok := crmTagKeys.Get(name); ok {
			return key.Parent
		}
	}
	return crmDefaultOrganization
}

// gateResourcePolicies are the policies the services store for one resource,
// under the keys each keeps them by.
func gateResourcePolicies(name string) []IAMPolicy {
	var policies []IAMPolicy
	keys := []string{name, "compute/" + name}
	segments := strings.Split(name, "/")
	if len(segments) >= 2 && segments[0] == "projects" {
		if p, ok := crmResolveProject(segments[1]); ok {
			for _, spelling := range []string{p.ProjectId, strings.TrimPrefix(p.Name, "projects/")} {
				if spelling != segments[1] {
					alias := "projects/" + spelling + "/" + strings.Join(segments[2:], "/")
					keys = append(keys, alias, "compute/"+alias)
				}
			}
		}
	}
	switch {
	case len(segments) == 4 && segments[2] == "serviceAccounts":
		if sa, ok := auditLookupServiceAccount(name); ok {
			keys = append(keys, "serviceAccount/"+sa.Email)
		}
	case len(segments) == 4 && segments[2] == "managedZones":
		keys = append(keys, "dnsManagedZone/"+segments[1]+"/"+segments[3])
	case len(segments) == 6 && segments[2] == "datasets" && segments[4] == "tables":
		keys = append(keys, "bigquery:"+bqTableKey(segments[1], segments[3], segments[5]))
	case len(segments) == 6 && segments[2] == "datasets" && segments[4] == "routines":
		keys = append(keys, "bigquery:"+bqRoutineKey(segments[1], segments[3], segments[5]))
	}
	if gcpResourcePolicies != nil {
		for _, key := range keys {
			if policy, ok := gcpResourcePolicies.Get(key); ok {
				policies = append(policies, policy)
			}
		}
	}
	if kmsIamPolicies != nil {
		if stored, ok := kmsIamPolicies.Get(name); ok {
			policy := IAMPolicy{}
			for _, b := range stored.Bindings {
				policy.Bindings = append(policy.Bindings, IAMBinding(b))
			}
			policies = append(policies, policy)
		}
	}
	if apigwIamPolicies != nil && len(segments) == 6 && segments[4] == "gateways" {
		if stored, ok := apigwIamPolicies.Get(apigwIamPolicyKey(segments[1], segments[3], segments[5])); ok {
			policy := IAMPolicy{}
			for _, b := range stored.Bindings {
				policy.Bindings = append(policy.Bindings, IAMBinding(b))
			}
			policies = append(policies, policy)
		}
	}
	return policies
}
