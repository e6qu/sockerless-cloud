package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/metadata"
)

// testIamPermissions, answered from the policy rather than from the question.
//
// The operation asks which of a set of permissions the caller holds on a
// resource, and every one of its implementations used to return the set it was
// given, unchanged. That is the question, not an answer: a caller bound to
// nothing got the same reply as a project owner, so the operation could not be
// used for what callers use it for — deciding whether to offer an action before
// attempting it.
//
// Everything it needs is already here. The policy the caller set through
// setIamPolicy is stored; a role's permissions are its includedPermissions,
// which the simulator holds for both the curated roles it serves at roles.get
// and the custom roles a tenant defines; and the bearer token the request
// carries names the principal, because the simulator minted and signed it.
//
// The one caller the simulator cannot look up is the one holding the
// credentials it was configured with rather than a token it issued to a service
// account. That caller is the owner of the account the simulator serves — it is
// the identity everything here was created under — and an owner holds whatever
// it asks about, which is what real Google answers for one. This mirrors the
// AWS slice, where a credential no IAM user registered is the account itself.

// gcpDefaultPrincipal is the subject the token endpoint mints for a caller that
// presents no service-account assertion: the account's own operator.
const gcpDefaultPrincipal = "sockerless-sim"

// gcpRequestPrincipal is the IAM member the request's bearer token names, and
// whether the caller is the account's owner rather than a bound principal.
func gcpRequestPrincipal(r *http.Request) (member string, owner bool) {
	return gcpBearerPrincipal(r.Header.Get("Authorization"))
}

// gcpContextPrincipal is gcpRequestPrincipal for a gRPC call, whose credential
// travels in the call's metadata rather than in an HTTP header.
func gcpContextPrincipal(ctx context.Context) (member string, owner bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", true
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", true
	}
	return gcpBearerPrincipal(values[0])
}

// gcpBearerPrincipal resolves one Authorization value to the member it names.
func gcpBearerPrincipal(authorization string) (member string, owner bool) {
	raw := strings.TrimSpace(authorization)
	if !strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		// No token to resolve: the caller is the operator of the account.
		return "", true
	}
	claims, err := verifiedAccessTokenClaims(strings.TrimSpace(raw[len("bearer "):]))
	if err != nil {
		return "", true
	}
	return gcpSubjectPrincipal(claims.Sub)
}

// gcpSubjectPrincipal is the IAM member a token's subject names, and whether
// the subject is the account's operator rather than a bound principal.
func gcpSubjectPrincipal(subject string) (member string, owner bool) {
	if subject == "" || subject == gcpDefaultPrincipal {
		return "", true
	}
	// A service account's subject is its email, and its policy member spells
	// it with the serviceAccount: prefix.
	if strings.Contains(subject, "@") {
		return "serviceAccount:" + subject, false
	}
	return subject, false
}

// gcpRoleIncludedPermissions is the permission set a role name resolves to,
// from the curated roles the simulator serves and the custom roles a tenant
// defined. A role the simulator does not define resolves to nothing, which is
// what a binding to a role that does not exist grants.
func gcpRoleIncludedPermissions(role string) []string {
	for _, curated := range gcpPredefinedRoles() {
		if curated.Name == role {
			return curated.IncludedPermissions
		}
	}
	// A custom role is named projects/{p}/roles/{id} or
	// organizations/{o}/roles/{id}, and the store holds it under its id.
	if i := strings.LastIndex(role, "/roles/"); i >= 0 {
		if custom, ok := iamCustomRoles.Get(role[i+len("/roles/"):]); ok {
			return custom.IncludedPermissions
		}
	}
	return nil
}

// gcpMemberMatches reports whether a binding member covers the caller, where
// an empty principal is a caller that presented no credential. allUsers covers
// everyone and allAuthenticatedUsers everyone who presented a credential.
func gcpMemberMatches(member, principal string, resource gcpIAMResource, at time.Time) bool {
	switch member {
	case "allUsers":
		return true
	case "allAuthenticatedUsers":
		return principal != ""
	}
	if kind, project, ok := strings.Cut(member, ":"); ok {
		if role, convenience := gcpConvenienceRoles[kind]; convenience {
			return gcpProjectGrantsRole(project, role, principal, resource, at)
		}
	}
	return member == principal
}

// gcpConvenienceRoles maps the project convenience members Cloud Storage binds
// in a bucket's default policy to the basic role whose holders they cover.
var gcpConvenienceRoles = map[string]string{
	"projectOwner":  "roles/owner",
	"projectEditor": "roles/editor",
	"projectViewer": "roles/viewer",
}

// gcpProjectPolicies holds each project's IAM policy under project/{projectId}.
var gcpProjectPolicies sim.Store[IAMPolicy]

func gcpProjectPolicy(project string) IAMPolicy {
	if gcpProjectPolicies == nil {
		return IAMPolicy{}
	}
	policy, _ := gcpProjectPolicies.Get("project/" + project)
	return policy
}

// gcpHierarchyPolicies are the policies a resource in the project inherits:
// the project's own and those of every folder and the organization above it.
func gcpHierarchyPolicies(project string) []IAMPolicy {
	var policies []IAMPolicy
	for _, node := range crmOrgPolicyAncestry("projects/" + project) {
		kind, id, _ := strings.Cut(node, "/")
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

func gcpProjectGrantsRole(project, role, principal string, resource gcpIAMResource, at time.Time) bool {
	for _, binding := range gcpProjectPolicy(project).Bindings {
		if binding.Role != role || !gcpConditionHolds(binding.Condition, resource, at) {
			continue
		}
		for _, member := range binding.Members {
			if member == "allUsers" || (principal != "" && (member == principal || member == "allAuthenticatedUsers")) {
				return true
			}
		}
	}
	return false
}

// gcpPermissionsHeldUnder answers which of the requested permissions the
// caller holds on the resource under policies that apply together, as a
// resource's own policy and its ancestors' do.
func gcpPermissionsHeldUnder(principal string, owner bool, policies []IAMPolicy, requested []string, resource gcpIAMResource) []string {
	var effective IAMPolicy
	for _, policy := range policies {
		effective.Bindings = append(effective.Bindings, policy.Bindings...)
	}
	return gcpAnswerForPrincipal(principal, owner, effective, requested, resource)
}

// gcpPermissionsHeldBy returns the subset of the requested permissions the
// principal holds on the resource under the policy at the given time, in the
// order the request asked for them — which is the order real Google answers
// in. A conditional binding grants its role only while its condition holds.
func gcpPermissionsHeldBy(policy IAMPolicy, principal string, requested []string, resource gcpIAMResource, at time.Time) []string {
	held := map[string]bool{}
	for _, binding := range policy.Bindings {
		bound := false
		for _, member := range binding.Members {
			if gcpMemberMatches(member, principal, resource, at) {
				bound = true
				break
			}
		}
		if !bound || !gcpConditionHolds(binding.Condition, resource, at) {
			continue
		}
		for _, permission := range gcpRoleIncludedPermissions(binding.Role) {
			held[permission] = true
		}
	}
	answer := make([]string, 0, len(requested))
	for _, permission := range requested {
		if held[permission] {
			answer = append(answer, permission)
		}
	}
	return answer
}

// gcpAnswerTestIamPermissions is the whole operation: the permissions the
// caller holds on the resource, out of the ones it asked about.
func gcpAnswerTestIamPermissions(r *http.Request, policy IAMPolicy, requested []string, resource gcpIAMResource) []string {
	principal, owner := gcpRequestPrincipal(r)
	return gcpAnswerForPrincipal(principal, owner, policy, requested, resource)
}

// gcpAnswerTestIamPermissionsForContext is the same answer for a gRPC call.
func gcpAnswerTestIamPermissionsForContext(
	ctx context.Context, policy IAMPolicy, requested []string, resource gcpIAMResource,
) []string {
	principal, owner := gcpContextPrincipal(ctx)
	return gcpAnswerForPrincipal(principal, owner, policy, requested, resource)
}

func gcpAnswerForPrincipal(principal string, owner bool, policy IAMPolicy, requested []string, resource gcpIAMResource) []string {
	if owner {
		// The account's operator holds what it asks about.
		answer := make([]string, len(requested))
		copy(answer, requested)
		return answer
	}
	return gcpPermissionsHeldBy(policy, principal, requested, resource, time.Now())
}
