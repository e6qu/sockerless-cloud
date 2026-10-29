package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// The IAM catalog the simulator evaluates policies against, compiled by
// scripts/gen-gcp-iam-catalog.go from the pages vendored under
// specs/cloud-api/gcp/: the predefined roles, every permission the served
// services define, the method tables Cloud Storage and Pub/Sub publish, the
// Cloud Storage permissions basic roles hold intrinsically, and the service
// agents Google grants a role on their project.

//go:embed iam_catalog_vendored.json
var iamCatalogVendored []byte

type gcpCatalogRole struct {
	Name                string   `json:"name"`
	Title               string   `json:"title"`
	Description         string   `json:"description"`
	Stage               string   `json:"stage"`
	IncludedPermissions []string `json:"includedPermissions"`
}

// gcpPermissionRequirement is one permission a method checks. On names what
// it is checked on when that is not the resource the method addresses:
// "project", "source", "destination", the resource a request field names
// ("field:topic"), or the topic a subscription is attached to
// ("attached:topic").
type gcpPermissionRequirement struct {
	Permission string `json:"permission"`
	On         string `json:"on,omitempty"`
	Optional   bool   `json:"optional,omitempty"`
}

type gcpCatalogServiceAgent struct {
	Service string `json:"service"`
	Email   string `json:"email"`
	Role    string `json:"role"`
}

type gcpIAMCatalog struct {
	roles       []gcpCatalogRole
	roleByName  map[string]*gcpCatalogRole
	roleGrants  map[string]map[string]bool
	permissions []string
	// canonical spells each permission the way the role pages do, keyed by
	// its lower case: a method table may spell storage.managedFolders.get
	// as storage.managedfolders.get.
	canonical    map[string]string
	methods      map[string][]gcpPermissionRequirement
	storageBasic map[string]map[string]bool
	agents       []gcpCatalogServiceAgent
}

var gcpCatalog = sync.OnceValue(func() *gcpIAMCatalog {
	var payload struct {
		Roles             []gcpCatalogRole                      `json:"roles"`
		Permissions       []string                              `json:"permissions"`
		Methods           map[string][]gcpPermissionRequirement `json:"methods"`
		StorageBasicRoles map[string][]string                   `json:"storageBasicRoles"`
		ServiceAgents     []gcpCatalogServiceAgent              `json:"serviceAgents"`
	}
	if err := json.Unmarshal(iamCatalogVendored, &payload); err != nil {
		panic(fmt.Sprintf("iam_catalog_vendored.json: %v", err))
	}
	c := &gcpIAMCatalog{
		roles:        payload.Roles,
		roleByName:   map[string]*gcpCatalogRole{},
		roleGrants:   map[string]map[string]bool{},
		canonical:    map[string]string{},
		methods:      map[string][]gcpPermissionRequirement{},
		storageBasic: map[string]map[string]bool{},
		agents:       payload.ServiceAgents,
	}
	add := func(p string) {
		if _, ok := c.canonical[strings.ToLower(p)]; !ok {
			c.canonical[strings.ToLower(p)] = p
			c.permissions = append(c.permissions, p)
		}
	}
	for _, p := range payload.Permissions {
		add(p)
	}
	for i := range c.roles {
		r := &c.roles[i]
		c.roleByName[r.Name] = r
		grants := map[string]bool{}
		for _, p := range r.IncludedPermissions {
			grants[p] = true
			add(p)
		}
		c.roleGrants[r.Name] = grants
	}
	sort.Strings(c.permissions)
	for id, reqs := range payload.Methods {
		canonical := make([]gcpPermissionRequirement, 0, len(reqs))
		for _, r := range reqs {
			if p, ok := c.canonical[strings.ToLower(r.Permission)]; ok {
				r.Permission = p
			}
			canonical = append(canonical, r)
		}
		c.methods[id] = canonical
	}
	for role, perms := range payload.StorageBasicRoles {
		set := map[string]bool{}
		for _, p := range perms {
			if prefix, wildcard := strings.CutSuffix(p, "*"); wildcard {
				for _, known := range c.permissions {
					if strings.HasPrefix(known, prefix) {
						set[known] = true
					}
				}
				continue
			}
			set[p] = true
		}
		c.storageBasic[role] = set
	}
	return c
})

// gcpCanonicalPermission is the permission as the catalog spells it, and
// whether a served service defines it at all.
func gcpCanonicalPermission(permission string) (string, bool) {
	p, ok := gcpCatalog().canonical[strings.ToLower(permission)]
	return p, ok
}

const (
	gcpRoleOwner  = "roles/owner"
	gcpRoleEditor = "roles/editor"
	gcpRoleViewer = "roles/viewer"
)

// gcpBasicRoleGrants decides the legacy basic roles, whose permission lists
// Google publishes only in its console. Cloud Storage publishes the
// permissions each holds intrinsically, and they hold nothing else there. For
// every other service the roles follow the roles overview: Viewer holds the
// read-only permissions, Editor everything that changes state except managing
// access, tag bindings and billing, and Owner everything; neither holds the
// service account credential permissions.
func gcpBasicRoleGrants(role, permission string) bool {
	if strings.HasPrefix(permission, "storage.") {
		return gcpCatalog().storageBasic[role][permission]
	}
	switch role {
	case gcpRoleOwner:
		return !slices.Contains(gcpCredentialPermissions, permission)
	case gcpRoleEditor:
		return !gcpEditorWithholds(permission)
	case gcpRoleViewer:
		return gcpViewerHolds(permission)
	}
	return false
}

// gcpEditorWithheldVerbs are the permission verbs that manage access, tag
// bindings and billing, which the roles overview reserves to Owner.
var gcpEditorWithheldVerbs = []string{
	"setIamPolicy", "createTagBinding", "deleteTagBinding",
	"createBillingAssignment", "deleteBillingAssignment",
}

// gcpCredentialPermissions are the service account credential
// permissions, which Google grants through the Service Account Token Creator
// role rather than a basic role.
var gcpCredentialPermissions = []string{
	"iam.serviceAccounts.getAccessToken", "iam.serviceAccounts.getOpenIdToken",
	"iam.serviceAccounts.signBlob", "iam.serviceAccounts.signJwt",
	"iam.serviceAccounts.implicitDelegation",
}

func gcpEditorWithholds(permission string) bool {
	parts := strings.Split(permission, ".")
	verb := parts[len(parts)-1]
	switch {
	case slices.Contains(gcpEditorWithheldVerbs, verb), slices.Contains(gcpCredentialPermissions, permission):
		return true
	case strings.HasPrefix(permission, "iam.roles.") && verb != "get" && verb != "list":
		return true
	case strings.HasPrefix(permission, "billing."), strings.HasPrefix(permission, "resourcemanager.tagValueBindings."),
		strings.HasPrefix(permission, "resourcemanager.tagHolds."):
		return true
	}
	return false
}

// gcpViewerVerbPrefixes are the read-only permission verbs: a get or list of
// any kind, and the data reads a service documents for its viewer.
var gcpViewerVerbPrefixes = []string{"get", "list", "search", "read", "select", "query", "lookup"}

func gcpViewerHolds(permission string) bool {
	parts := strings.Split(permission, ".")
	if len(parts) < 3 {
		return false
	}
	verb := parts[len(parts)-1]
	if strings.Contains(parts[len(parts)-2], "private") || verb == "getAccessToken" || verb == "getOpenIdToken" {
		return false
	}
	for _, prefix := range gcpViewerVerbPrefixes {
		if verb == prefix || (strings.HasPrefix(verb, prefix) && len(verb) > len(prefix) && verb[len(prefix)] >= 'A' && verb[len(prefix)] <= 'Z') {
			return true
		}
	}
	return false
}

// gcpRoleGrants reports whether a role includes the permission: a basic
// role, a predefined role the catalog holds, or a custom role that exists and
// is not deleted or disabled.
func gcpRoleGrants(role, permission string) bool {
	switch role {
	case gcpRoleOwner, gcpRoleEditor, gcpRoleViewer:
		return gcpBasicRoleGrants(role, permission)
	}
	if grants, ok := gcpCatalog().roleGrants[role]; ok {
		return grants[permission]
	}
	if iamCustomRoles != nil && strings.Contains(role, "/roles/") {
		if custom, ok := iamCustomRoles.Get(role); ok && !custom.Deleted && custom.Stage != "DISABLED" {
			return slices.Contains(custom.IncludedPermissions, permission)
		}
	}
	return false
}

// gcpBasicRoleIncludedPermissions lists a basic role's permissions out of the
// catalog's, for roles.get.
func gcpBasicRoleIncludedPermissions(role string) []string {
	var out []string
	for _, p := range gcpCatalog().permissions {
		if gcpBasicRoleGrants(role, p) {
			out = append(out, p)
		}
	}
	return out
}

// gcpProjectServiceAgents are the service agents of the served services that
// Google grants a role on their project, spelled for one project number.
func gcpProjectServiceAgents(projectNumber string) []IAMBinding {
	var bindings []IAMBinding
	byRole := map[string]int{}
	for _, agent := range gcpCatalog().agents {
		if !slices.Contains(gcpAuditableServices, agent.Service) {
			continue
		}
		member := "serviceAccount:" + strings.Replace(agent.Email, "PROJECT_NUMBER", projectNumber, 1)
		if i, ok := byRole[agent.Role]; ok {
			bindings[i].Members = append(bindings[i].Members, member)
			continue
		}
		byRole[agent.Role] = len(bindings)
		bindings = append(bindings, IAMBinding{Role: agent.Role, Members: []string{member}})
	}
	return bindings
}
