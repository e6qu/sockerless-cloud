package main

import (
	"context"
	"net/http"
	"strings"

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
	if err != nil || claims.Sub == "" || claims.Sub == gcpDefaultPrincipal {
		return "", true
	}
	// A service account's subject is its email, and its policy member spells
	// it with the serviceAccount: prefix.
	if strings.Contains(claims.Sub, "@") {
		return "serviceAccount:" + claims.Sub, false
	}
	return claims.Sub, false
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

// gcpMemberMatches reports whether a binding member covers the caller.
// allUsers covers everyone and allAuthenticatedUsers covers everyone the
// simulator issued a token to, which is every principal that reaches here.
func gcpMemberMatches(member, principal string) bool {
	switch member {
	case "allUsers", "allAuthenticatedUsers":
		return true
	}
	return member == principal
}

// gcpPermissionsHeldBy returns the subset of the requested permissions the
// principal holds under the policy, in the order the request asked for them —
// which is the order real Google answers in.
func gcpPermissionsHeldBy(policy IAMPolicy, principal string, requested []string) []string {
	held := map[string]bool{}
	for _, binding := range policy.Bindings {
		bound := false
		for _, member := range binding.Members {
			if gcpMemberMatches(member, principal) {
				bound = true
				break
			}
		}
		if !bound {
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
// caller holds, out of the ones it asked about.
func gcpAnswerTestIamPermissions(r *http.Request, policy IAMPolicy, requested []string) []string {
	principal, owner := gcpRequestPrincipal(r)
	return gcpAnswerForPrincipal(principal, owner, policy, requested)
}

// gcpAnswerTestIamPermissionsForContext is the same answer for a gRPC call.
func gcpAnswerTestIamPermissionsForContext(
	ctx context.Context, policy IAMPolicy, requested []string,
) []string {
	principal, owner := gcpContextPrincipal(ctx)
	return gcpAnswerForPrincipal(principal, owner, policy, requested)
}

func gcpAnswerForPrincipal(principal string, owner bool, policy IAMPolicy, requested []string) []string {
	if owner {
		// The account's operator holds what it asks about.
		answer := make([]string, len(requested))
		copy(answer, requested)
		return answer
	}
	return gcpPermissionsHeldBy(policy, principal, requested)
}
