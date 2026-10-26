package main

import (
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// rbacCaller is the security principal a request authenticates as: the
// token's object ID and the directory groups it belongs to, through which
// Azure RBAC also reaches it.
type rbacCaller struct {
	oid, appid, tid string
	// name is how Azure Resource Manager names the client in a refusal: a
	// user's principal name, otherwise the object ID.
	name       string
	principals []string
}

func rbacCallerFromClaims(claims map[string]any) rbacCaller {
	c := rbacCaller{}
	c.oid, _ = claims["oid"].(string)
	c.appid, _ = claims["appid"].(string)
	c.tid, _ = claims["tid"].(string)
	c.name = c.oid
	for _, claim := range []string{"upn", "unique_name"} {
		if v, _ := claims[claim].(string); v != "" {
			c.name = v
			break
		}
	}
	return rbacPrincipal(c)
}

// rbacCallerForObject is the principal with object ID oid, as Storage
// evaluates the owner of a user delegation key.
func rbacCallerForObject(oid string) rbacCaller {
	return rbacPrincipal(rbacCaller{oid: oid, name: oid})
}

func rbacPrincipal(c rbacCaller) rbacCaller {
	if c.oid != "" {
		c.principals = append([]string{c.oid}, entraTransitiveGroupIDs(c.oid)...)
	}
	return c
}

var azureRoleAssignmentsByPrincipal sim.GenerationIndex[RoleAssignment]

func rbacAssignmentsOf(c rbacCaller) []RoleAssignment {
	if azureRoleAssignments == nil {
		return nil
	}
	var out []RoleAssignment
	for _, principal := range c.principals {
		out = append(out, azureRoleAssignmentsByPrincipal.LookupAll(azureRoleAssignments, strings.ToLower(principal),
			func(ra RoleAssignment) []string { return []string{strings.ToLower(ra.Properties.PrincipalId)} })...)
	}
	return out
}

// rbacAllows reports whether Azure RBAC grants the caller an action (data
// false) or a data action (data true) at scope: some role assigned to the
// caller or one of its groups, at scope or a scope above it, lists the
// operation in one permission entry's actions (dataActions) and not in that
// entry's notActions (notDataActions).
func rbacAllows(c rbacCaller, scope, action string, data bool) bool {
	for _, ra := range rbacAssignmentsOf(c) {
		if !rbacScopeCovers(ra.Properties.Scope, scope) {
			continue
		}
		for _, p := range rbacRolePermissions(roleDefinitionGUID(ra.Properties.RoleDefinitionId)) {
			allow, deny := p.Actions, p.NotActions
			if data {
				allow, deny = p.DataActions, p.NotDataActions
			}
			if rbacActionMatchesAny(allow, action) && !rbacActionMatchesAny(deny, action) {
				return true
			}
		}
	}
	return false
}

// rbacReaches reports whether the caller holds any role assignment at scope,
// above it or beneath it.
func rbacReaches(c rbacCaller, scope string) bool {
	for _, ra := range rbacAssignmentsOf(c) {
		if rbacScopeCovers(ra.Properties.Scope, scope) || rbacScopeCovers(scope, ra.Properties.Scope) {
			return true
		}
	}
	return false
}

// rbacHoldsBeneath reports whether the caller holds a role assignment strictly
// beneath scope.
func rbacHoldsBeneath(c rbacCaller, scope string) bool {
	for _, ra := range rbacAssignmentsOf(c) {
		if rbacScopeCovers(scope, ra.Properties.Scope) && !rbacScopeCovers(ra.Properties.Scope, scope) {
			return true
		}
	}
	return false
}

const (
	ownerRoleDefinitionGUID = "8e3af657-a8ff-443c-a75c-2fe8c4bcb635"
	// bootstrapOwnerAssignmentName names the Owner assignment that privileges
	// the bootstrap service principal on the seeded subscription.
	bootstrapOwnerAssignmentName = "00000000-0000-0000-0000-0000000000b4"
)

// rbacAssignOwner records an Owner role assignment for principalID at a
// subscription, as Azure records one for the principal that creates a
// subscription or accepts its ownership.
func rbacAssignOwner(subscription, name, principalID, principalType string) {
	scope := armSubscriptionID(subscription)
	id := scope + "/providers/Microsoft.Authorization/roleAssignments/" + name
	now := time.Now().UTC().Format(time.RFC3339Nano)
	azureRoleAssignments.Put(id, RoleAssignment{
		ID:   id,
		Name: name,
		Type: "Microsoft.Authorization/roleAssignments",
		Properties: RoleAssignmentProperties{
			RoleDefinitionId: scope + "/providers/Microsoft.Authorization/roleDefinitions/" + ownerRoleDefinitionGUID,
			PrincipalId:      principalID,
			PrincipalType:    principalType,
			Scope:            scope,
			CreatedOn:        now,
			UpdatedOn:        now,
		},
	})
}

// rbacTenantRootGroup is the scope of a tenant's root management group,
// whose ID is the tenant ID.
func rbacTenantRootGroup(tenant string) string {
	return "/providers/Microsoft.Management/managementGroups/" + tenant
}

// rbacSeedBootstrapOwner assigns the bootstrap service principal Owner on the
// tenant root management group, through which it manages every management
// group and subscription of the directory.
func rbacSeedBootstrapOwner() {
	scope := rbacTenantRootGroup(simTenantID)
	id := scope + "/providers/Microsoft.Authorization/roleAssignments/" + bootstrapOwnerAssignmentName
	if _, ok := azureRoleAssignments.Get(id); ok {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	azureRoleAssignments.Put(id, RoleAssignment{
		ID:   id,
		Name: bootstrapOwnerAssignmentName,
		Type: "Microsoft.Authorization/roleAssignments",
		Properties: RoleAssignmentProperties{
			RoleDefinitionId: "/providers/Microsoft.Authorization/roleDefinitions/" + ownerRoleDefinitionGUID,
			PrincipalId:      entraBootstrapSPObjectID,
			PrincipalType:    "ServicePrincipal",
			Scope:            scope,
			CreatedOn:        now,
			UpdatedOn:        now,
		},
	})
}

// azureSubscriptionTenant is the directory a subscription belongs to.
func azureSubscriptionTenant(sub string) string {
	if azureSubscriptionRecords == nil {
		return simTenantID
	}
	_, _, tenant := azureSubscriptionView(sub)
	return tenant
}

// rbacPrincipalType names the principalType a role assignment records for a
// directory object.
func rbacPrincipalType(objectID string) string {
	if _, ok := entraLookupUser(objectID); ok {
		return "User"
	}
	return "ServicePrincipal"
}

// rbacRemoveAssignmentsWithin deletes every role assignment at or beneath
// scope, as Azure does when a subscription moves to another directory.
func rbacRemoveAssignmentsWithin(scope string) {
	for _, ra := range azureRoleAssignmentsAll.LookupAll(azureRoleAssignments, "all", azureRoleAssignmentAllKeys) {
		if rbacScopeCovers(scope, ra.Properties.Scope) {
			azureRoleAssignments.Delete(ra.ID)
		}
	}
}
