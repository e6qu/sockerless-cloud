package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/metadata"
)

// IAM policy evaluation: which permissions a principal holds on a resource
// under the policies that govern it, the resource's own and those it inherits
// from its project, folders and organization. Every role is resolved through
// the catalog (iam_catalog.go) and the tenant's custom roles; nobody holds a
// permission no binding grants.

// gcpRequestPrincipal is the IAM member the request's bearer token names, ""
// for a request without one.
func gcpRequestPrincipal(r *http.Request) string {
	return gcpBearerPrincipal(r.Header.Get("Authorization"))
}

// gcpContextPrincipal is gcpRequestPrincipal for a gRPC call, whose credential
// travels in the call's metadata rather than in an HTTP header.
func gcpContextPrincipal(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return gcpBearerPrincipal(values[0])
}

// gcpBearerPrincipal resolves one Authorization value to the member it names.
func gcpBearerPrincipal(authorization string) string {
	raw := strings.TrimSpace(authorization)
	if !strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		return ""
	}
	claims, err := verifiedAccessTokenClaims(strings.TrimSpace(raw[len("bearer "):]))
	if err != nil {
		return ""
	}
	return gcpSubjectPrincipal(claims.Sub)
}

// gcpSubjectPrincipal is the IAM member a token's subject names: a service
// account by its email, a workforce identity by its principal identifier.
func gcpSubjectPrincipal(subject string) string {
	switch {
	case subject == "":
		return ""
	case strings.Contains(subject, "@"):
		return "serviceAccount:" + subject
	case strings.HasPrefix(subject, "locations/"):
		return "principal://iam.googleapis.com/" + subject
	}
	return subject
}

// gcpMemberMatches reports whether a binding member covers the caller, where
// an empty principal is a caller that presented no credential. allUsers covers
// everyone and allAuthenticatedUsers everyone who presented a credential.
func gcpMemberMatches(member, principal string, at time.Time) bool {
	switch member {
	case "allUsers":
		return true
	case "allAuthenticatedUsers":
		return principal != ""
	}
	if kind, project, ok := strings.Cut(member, ":"); ok {
		if role, convenience := gcpConvenienceRoles[kind]; convenience {
			return principal != "" && gcpProjectGrantsRole(project, role, principal, at)
		}
	}
	if pool, ok := strings.CutSuffix(member, "/*"); ok && strings.HasPrefix(pool, "principalSet://iam.googleapis.com/") {
		// All identities in a workforce or workload identity pool.
		return strings.HasPrefix(principal, "principal://"+strings.TrimPrefix(pool, "principalSet://")+"/subject/")
	}
	return principal != "" && member == principal
}

// gcpConvenienceRoles maps the project convenience members Cloud Storage binds
// in a bucket's default policy to the basic role whose holders they cover.
var gcpConvenienceRoles = map[string]string{
	"projectOwner":  gcpRoleOwner,
	"projectEditor": gcpRoleEditor,
	"projectViewer": gcpRoleViewer,
}

// gcpProjectPolicies holds each project's IAM policy under project/{projectId}.
var gcpProjectPolicies sim.Store[IAMPolicy]

func gcpProjectPolicy(project string) IAMPolicy {
	if gcpProjectPolicies == nil {
		return IAMPolicy{}
	}
	if p, ok := crmResolveProject(project); ok {
		project = p.ProjectId
	}
	policy, _ := gcpProjectPolicies.Get("project/" + project)
	return policy
}

// gcpHierarchyPolicies are the policies a resource in the project inherits:
// the project's own and those of every folder and the organization above it.
func gcpHierarchyPolicies(project string) []IAMPolicy {
	return gcpNodePolicies("projects/" + project)
}

// gcpNodePolicies are the policies of a hierarchy node and its ancestors,
// outermost first.
func gcpNodePolicies(node string) []IAMPolicy {
	var policies []IAMPolicy
	for _, n := range crmOrgPolicyAncestry(node) {
		kind, id, _ := strings.Cut(n, "/")
		switch kind {
		case "projects":
			policies = append(policies, gcpProjectPolicy(id))
		case "folders", "organizations":
			if gcpResourcePolicies == nil {
				continue
			}
			// crmIamVerb stores a folder's and an organization's policy
			// under the singular kind.
			if policy, ok := gcpResourcePolicies.Get(strings.TrimSuffix(kind, "s") + "/" + id); ok {
				policies = append(policies, policy)
			}
		}
	}
	return policies
}

// gcpProjectGrantsRole reports whether the principal holds a basic role on
// the project, granted there or on an ancestor, as a convenience member
// covers.
func gcpProjectGrantsRole(project, role, principal string, at time.Time) bool {
	resource := gcpIAMResource{project: project}
	for _, policy := range gcpHierarchyPolicies(project) {
		for _, binding := range policy.Bindings {
			if binding.Role != role || !gcpConditionHolds(binding.Condition, resource, at) {
				continue
			}
			for _, member := range binding.Members {
				if member == "allUsers" || member == principal || member == "allAuthenticatedUsers" {
					return true
				}
			}
		}
	}
	return false
}

// gcpPermissionsHeldUnder answers which of the requested permissions the
// caller holds on the resource under policies that apply together, as a
// resource's own policy and its ancestors' do.
func gcpPermissionsHeldUnder(principal string, policies []IAMPolicy, requested []string, resource gcpIAMResource) []string {
	var effective IAMPolicy
	for _, policy := range policies {
		effective.Bindings = append(effective.Bindings, policy.Bindings...)
	}
	return gcpPermissionsHeldBy(effective, principal, requested, resource, time.Now())
}

// gcpPermissionsHeldBy returns the subset of the requested permissions the
// principal holds on the resource under the policy at the given time, in the
// order the request asked for them — which is the order real Google answers
// in. A conditional binding grants its role only while its condition holds.
func gcpPermissionsHeldBy(policy IAMPolicy, principal string, requested []string, resource gcpIAMResource, at time.Time) []string {
	var roles []string
	for _, binding := range policy.Bindings {
		bound := false
		for _, member := range binding.Members {
			if gcpMemberMatches(member, principal, at) {
				bound = true
				break
			}
		}
		if bound && gcpConditionHolds(binding.Condition, resource, at) {
			roles = append(roles, binding.Role)
		}
	}
	answer := make([]string, 0, len(requested))
	for _, permission := range requested {
		for _, role := range roles {
			if gcpRoleGrants(role, permission) {
				answer = append(answer, permission)
				break
			}
		}
	}
	return answer
}

// gcpPrincipalHoldsRole reports whether a binding of the role covers the
// principal under the policies.
func gcpPrincipalHoldsRole(principal, role string, policies []IAMPolicy, resource gcpIAMResource, at time.Time) bool {
	for _, policy := range policies {
		for _, binding := range policy.Bindings {
			if binding.Role != role || !gcpConditionHolds(binding.Condition, resource, at) {
				continue
			}
			for _, member := range binding.Members {
				if gcpMemberMatches(member, principal, at) {
					return true
				}
			}
		}
	}
	return false
}

// gcpInheritedPolicies are the policies a resource inherits, for the key its
// policy is stored under: a project, folder or organization its ancestors',
// any other resource the policies of its project's chain and of the
// resources it lies within.
func gcpInheritedPolicies(key string) []IAMPolicy {
	kind, rest, _ := strings.Cut(key, "/")
	switch kind {
	case "project":
		return gcpHierarchyPolicies(rest)
	case "organization", "folder":
		return gcpNodePolicies(kind + "s/" + rest)
	case "serviceAccount":
		return gcpHierarchyPolicies(gcpProjectFromEmail(rest))
	case "dnsManagedZone":
		project, _, _ := strings.Cut(rest, "/")
		return gcpHierarchyPolicies(project)
	case "compute":
		return gcpPolicyChain(rest)
	}
	if bq, ok := strings.CutPrefix(key, "bigquery:"); ok {
		project, _, _ := strings.Cut(bq, "/")
		return gcpHierarchyPolicies(project)
	}
	return gcpPolicyChain(key)
}

// gcpAnswerTestIamPermissions is the whole operation: the permissions the
// caller holds on the resource stored under key, out of the ones it asked
// about, under the resource's policy and the policies it inherits.
func gcpAnswerTestIamPermissions(r *http.Request, key string, policy IAMPolicy, requested []string, resource gcpIAMResource) []string {
	return gcpAnswerForPrincipal(gcpRequestPrincipal(r), key, policy, requested, resource)
}

// gcpAnswerTestIamPermissionsForContext is the same answer for a gRPC call.
func gcpAnswerTestIamPermissionsForContext(
	ctx context.Context, key string, policy IAMPolicy, requested []string, resource gcpIAMResource,
) []string {
	return gcpAnswerForPrincipal(gcpContextPrincipal(ctx), key, policy, requested, resource)
}

func gcpAnswerForPrincipal(principal, key string, policy IAMPolicy, requested []string, resource gcpIAMResource) []string {
	policies := append(gcpInheritedPolicies(key), policy)
	return gcpPermissionsHeldUnder(principal, policies, requested, resource)
}
