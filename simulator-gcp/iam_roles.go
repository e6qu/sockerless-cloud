package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerIAMCatalog mounts the read-only catalogs: allowed locations,
// testable permissions, auditable services, grantable and predefined roles, and
// the policy linter.
func registerIAMCatalog(srv *sim.Server, customRoles sim.Store[GCPCustomRole]) {
	// iamcredentials.serviceAccounts.getAllowedLocations — returns the
	// trust-boundary locations a short-lived credential minted for this SA
	// may be used in. The sim models an unrestricted boundary ("0x0" is the
	// real wire's "all locations" sentinel encodedLocations value).
	srv.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{email}/allowedLocations", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"locations":        []string{},
			"encodedLocations": "0x0",
		})
	})

	// iamcredentials.projects.locations.workloadIdentityPools.getAllowedLocations
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/workloadIdentityPools/{pool}/allowedLocations", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"locations":        []string{},
			"encodedLocations": "0x0",
		})
	})

	// iamcredentials.locations.workforcePools.getAllowedLocations
	srv.HandleFunc("GET /v1/locations/{location}/workforcePools/{pool}/allowedLocations", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"locations":        []string{},
			"encodedLocations": "0x0",
		})
	})

	// QueryTestablePermissions — the catalog of permissions that can be tested
	// (and thus included in a custom role) on a given resource. `gcloud iam
	// roles create/update` calls this to validate the --permissions flag before
	// issuing CreateRole/UpdateRole. Real GCP returns a paginated list scoped to
	// the resource's service surface; the sim returns the representative catalog
	// it knows about (the union of permissions its predefined roles reference,
	// plus the common service-prefixed permissions the repo exercises).
	srv.HandleFunc("POST /v1/permissions:queryTestablePermissions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FullResourceName string `json:"fullResourceName"`
			PageSize         int    `json:"pageSize"`
			PageToken        string `json:"pageToken"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		perms := gcpTestablePermissions()
		out := make([]map[string]any, 0, len(perms))
		for _, p := range perms {
			out = append(out, map[string]any{
				"name":                    p,
				"stage":                   "GA",
				"customRolesSupportLevel": "SUPPORTED",
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"permissions": out})
	})

	// iamPolicies.lintPolicy — lints the condition object the request itself
	// carries ("The resource name is not used to read a policy from IAM. Only
	// the data in the request object is linted", per the Discovery method).
	// A request with no condition has nothing to lint and truthfully gets
	// empty findings; a condition is run through iamLintCondition's
	// structural checks.
	srv.HandleFunc("POST /v1/iamPolicies:lintPolicy", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FullResourceName string `json:"fullResourceName"`
			Condition        *struct {
				Expression  string `json:"expression"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Location    string `json:"location"`
			} `json:"condition"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		results := []map[string]any{}
		if req.Condition != nil {
			results = append(results, iamLintCondition(req.Condition.Expression)...)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"lintResults": results})
	})

	// iamPolicies.queryAuditableServices — the services whose audit logging
	// can be configured on the resource. The catalog is this installation's
	// own, the same argument as Cloud Billing's services.list: it names the
	// services this simulator hosts, because those are the services a policy
	// set here could ever audit.
	srv.HandleFunc("POST /v1/iamPolicies:queryAuditableServices", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FullResourceName string `json:"fullResourceName"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if _, ok := gcpFullResourceHost(req.FullResourceName); !ok {
			GCPError(w, http.StatusBadRequest,
				"fullResourceName must be a full resource name, e.g. //cloudresourcemanager.googleapis.com/projects/my-project", "INVALID_ARGUMENT")
			return
		}
		services := make([]map[string]any, 0, len(gcpAuditableServices))
		for _, name := range gcpAuditableServices {
			services = append(services, map[string]any{"name": name})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"services": services})
	})

	// roles.queryGrantableRoles — the roles that can be granted on a given
	// resource. The catalog queried is the one this simulator holds: the
	// bounded predefined set plus the tenant's custom roles in the resource's
	// project/organization scope. A Cloud Resource Manager resource (project,
	// folder, organization) accepts every role; any other resource accepts
	// the roles that carry at least one permission of its service — the same
	// service-prefix modeling gcpTestablePermissions uses.
	srv.HandleFunc("POST /v1/roles:queryGrantableRoles", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FullResourceName string `json:"fullResourceName"`
			View             string `json:"view"`
			PageSize         int    `json:"pageSize"`
			PageToken        string `json:"pageToken"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		host, ok := gcpFullResourceHost(req.FullResourceName)
		if !ok {
			GCPError(w, http.StatusBadRequest,
				"fullResourceName must be a full resource name, e.g. //cloudresourcemanager.googleapis.com/projects/my-project", "INVALID_ARGUMENT")
			return
		}
		service := strings.TrimSuffix(host, ".googleapis.com")
		full := req.View == "FULL"

		roles := []map[string]any{}
		for _, role := range gcpPredefinedRoles() {
			if gcpRoleGrantableOnService(role.IncludedPermissions, service) {
				roles = append(roles, gcpRoleJSON(role, full))
			}
		}
		// Custom roles defined in the resource's own project/organization
		// scope are grantable there too, under the same service filter.
		if scope := gcpFullResourceScope(req.FullResourceName); scope != "" {
			prefix := scope + "/roles/"
			for _, role := range customRoles.Filter(func(cr GCPCustomRole) bool {
				return strings.HasPrefix(cr.Name, prefix) && !cr.Deleted
			}) {
				if gcpRoleGrantableOnService(role.IncludedPermissions, service) {
					roles = append(roles, gcpCustomRoleJSON(role, full))
				}
			}
		}
		if req.PageSize < 0 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageSize %d", req.PageSize)
			return
		}
		// The documented default page size is 300, the ceiling 2,000.
		page, next, ok := gcpKeyPage(w, roles, []gcpSortKey[map[string]any]{gcpAsc(gcpField("name"))},
			req.PageToken, req.PageSize, 300, 2000)
		if !ok {
			return
		}
		resp := map[string]any{"roles": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Predefined roles — roles.list / roles.get. The catalog of curated
	// (Google-managed) roles. The sim carries a bounded representative set
	// (the basic roles plus the IAM/storage roles the repo references), not
	// the full ~1500-role catalog.
	srv.HandleFunc("GET /v1/roles", func(w http.ResponseWriter, r *http.Request) {
		roles := gcpPredefinedRoles()
		// roles.list omits includedPermissions unless view=FULL.
		full := r.URL.Query().Get("view") == "FULL"
		out := make([]map[string]any, 0, len(roles))
		for _, role := range roles {
			out = append(out, gcpRoleJSON(role, full))
		}
		iamWriteRolesPage(w, r, out)
	})

	srv.HandleFunc("GET /v1/roles/{role...}", func(w http.ResponseWriter, r *http.Request) {
		name := "roles/" + sim.PathParam(r, "role")
		for _, role := range gcpPredefinedRoles() {
			if role.Name == name {
				// roles.get returns the full role including includedPermissions.
				sim.WriteJSON(w, http.StatusOK, gcpRoleJSON(role, true))
				return
			}
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
	})
}

// registerCustomRoles mounts the custom-role CRUD surface for one scope
// (projects or organizations). The route patterns differ only in the parent
// segment, so both scopes share these handlers. `scope` is "project" or
// "organization"; `parentPrefix` derives the resource-name prefix
// (projects/{p} / organizations/{o}).
func registerCustomRoles(srv *sim.Server, store sim.Store[GCPCustomRole], scope, listPat, getPat, createPat, actionPat, patchPat, deletePat string) {
	parentPrefix := func(parent string) string {
		if scope == "organization" {
			return "organizations/" + parent
		}
		return "projects/" + parent
	}

	// ListRoles
	srv.HandleFunc(listPat, func(w http.ResponseWriter, r *http.Request) {
		prefix := parentPrefix(sim.PathParam(r, "parent")) + "/roles/"
		// roles.list defaults to BASIC view (no includedPermissions);
		// view=FULL returns permissions. showDeleted controls whether
		// soft-deleted roles are returned.
		full := r.URL.Query().Get("view") == "FULL"
		showDeleted := r.URL.Query().Get("showDeleted") == "true"
		roles := store.Filter(func(role GCPCustomRole) bool {
			if !strings.HasPrefix(role.Name, prefix) {
				return false
			}
			return showDeleted || !role.Deleted
		})
		out := make([]map[string]any, 0, len(roles))
		for _, role := range roles {
			out = append(out, gcpCustomRoleJSON(role, full))
		}
		iamWriteRolesPage(w, r, out)
	})

	// GetRole
	srv.HandleFunc(getPat, func(w http.ResponseWriter, r *http.Request) {
		name := parentPrefix(sim.PathParam(r, "parent")) + "/roles/" + sim.PathParam(r, "role")
		role, ok := store.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})

	// CreateRole
	srv.HandleFunc(createPat, func(w http.ResponseWriter, r *http.Request) {
		parent := parentPrefix(sim.PathParam(r, "parent"))
		var req struct {
			RoleId string `json:"roleId"`
			Role   struct {
				Title               string   `json:"title"`
				Description         string   `json:"description"`
				IncludedPermissions []string `json:"includedPermissions"`
				Stage               string   `json:"stage"`
			} `json:"role"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.RoleId == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "roleId is required")
			return
		}
		name := parent + "/roles/" + req.RoleId
		if _, exists := store.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "A role named %s already exists.", name)
			return
		}
		stage := req.Role.Stage
		if stage == "" {
			stage = "ALPHA"
		}
		role := GCPCustomRole{
			Name:                name,
			Title:               req.Role.Title,
			Description:         req.Role.Description,
			IncludedPermissions: req.Role.IncludedPermissions,
			Stage:               stage,
			Etag:                gcpPolicyETag(),
		}
		store.Put(name, role)
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})

	// UndeleteRole — POST .../roles/{role}:undelete
	srv.HandleFunc(actionPat, func(w http.ResponseWriter, r *http.Request) {
		roleAction := sim.PathParam(r, "roleAction")
		roleID, action, found := strings.Cut(roleAction, ":")
		if !found || action != "undelete" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported role action %q", roleAction)
			return
		}
		name := parentPrefix(sim.PathParam(r, "parent")) + "/roles/" + roleID
		role, ok := store.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
			return
		}
		role.Deleted = false
		role.Etag = gcpPolicyETag()
		store.Put(name, role)
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})

	// UpdateRole — PATCH with an updateMask over the mutable fields.
	srv.HandleFunc(patchPat, func(w http.ResponseWriter, r *http.Request) {
		name := parentPrefix(sim.PathParam(r, "parent")) + "/roles/" + sim.PathParam(r, "role")
		role, ok := store.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
			return
		}
		var req GCPCustomRole
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// Honor optimistic concurrency: a stale etag on the request body is
		// rejected with 409 ABORTED (same contract as setIamPolicy).
		if gcpIAMETagConflict(w, req.Etag, role.Etag, true) {
			return
		}
		// An empty updateMask updates every mutable field present in the body
		// (matches real GCP, which treats a missing mask as "full update").
		mask := r.URL.Query().Get("updateMask")
		fields := strings.Split(mask, ",")
		if mask == "" {
			fields = []string{"title", "description", "includedPermissions", "stage"}
		}
		for _, field := range fields {
			switch strings.TrimSpace(field) {
			case "title":
				role.Title = req.Title
			case "description":
				role.Description = req.Description
			case "includedPermissions":
				role.IncludedPermissions = req.IncludedPermissions
			case "stage":
				role.Stage = req.Stage
			}
		}
		role.Etag = gcpPolicyETag()
		store.Put(name, role)
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})

	// DeleteRole — soft-delete: set deleted=true and return the role.
	srv.HandleFunc(deletePat, func(w http.ResponseWriter, r *http.Request) {
		name := parentPrefix(sim.PathParam(r, "parent")) + "/roles/" + sim.PathParam(r, "role")
		role, ok := store.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The role named %s was not found.", name)
			return
		}
		role.Deleted = true
		role.Etag = gcpPolicyETag()
		store.Put(name, role)
		sim.WriteJSON(w, http.StatusOK, gcpCustomRoleJSON(role, true))
	})
}

// gcpTestablePermissions is the catalog queryTestablePermissions serves:
// every permission the served services define.
func gcpTestablePermissions() []string {
	return gcpCatalog().permissions
}

// gcpAuditableServices is the catalog iamPolicies.queryAuditableServices
// serves: the Google Cloud services this simulator hosts, under their public
// API hostnames — the same installation's-own-catalog rule Cloud Billing's
// services.list follows. A service not hosted here cannot produce audit logs
// here, so it does not belong in the answer.
var gcpAuditableServices = []string{
	"apigateway.googleapis.com",
	"artifactregistry.googleapis.com",
	"bigquery.googleapis.com",
	"bigtableadmin.googleapis.com",
	"cloudbilling.googleapis.com",
	"cloudbuild.googleapis.com",
	"cloudfunctions.googleapis.com",
	"cloudkms.googleapis.com",
	"cloudresourcemanager.googleapis.com",
	"cloudscheduler.googleapis.com",
	"cloudtasks.googleapis.com",
	"compute.googleapis.com",
	"dataflow.googleapis.com",
	"dns.googleapis.com",
	"eventarc.googleapis.com",
	"firestore.googleapis.com",
	"iam.googleapis.com",
	"iamcredentials.googleapis.com",
	"logging.googleapis.com",
	"pubsub.googleapis.com",
	"redis.googleapis.com",
	"run.googleapis.com",
	"secretmanager.googleapis.com",
	"serviceusage.googleapis.com",
	"spanner.googleapis.com",
	"sqladmin.googleapis.com",
	"storage.googleapis.com",
	"vpcaccess.googleapis.com",
}

// gcpFullResourceHost splits a full resource name
// ("//cloudresourcemanager.googleapis.com/projects/my-project") into its
// service host, reporting false for anything that is not a full resource name
// (missing "//", missing host, or missing resource path).
func gcpFullResourceHost(name string) (string, bool) {
	trimmed, ok := strings.CutPrefix(name, "//")
	if !ok {
		return "", false
	}
	host, path, found := strings.Cut(trimmed, "/")
	if !found || host == "" || path == "" {
		return "", false
	}
	return host, true
}

// gcpFullResourceScope returns the project/organization node a full resource
// name sits under ("projects/my-project", "organizations/123") — the scope
// whose custom roles are grantable on the resource — or "" when the name
// carries neither collection.
func gcpFullResourceScope(name string) string {
	trimmed, ok := strings.CutPrefix(name, "//")
	if !ok {
		return ""
	}
	_, path, found := strings.Cut(trimmed, "/")
	if !found {
		return ""
	}
	segs := strings.Split(path, "/")
	if len(segs) >= 2 && (segs[0] == "projects" || segs[0] == "organizations") && segs[1] != "" {
		return segs[0] + "/" + segs[1]
	}
	return ""
}

// gcpRoleGrantableOnService reports whether a role belongs in a
// queryGrantableRoles answer for a resource of the given service (the API
// hostname's first label). A Cloud Resource Manager resource — a project,
// folder or organization — is the umbrella every role can be granted on; any
// other resource takes the roles that carry at least one of its service's
// permissions, whose prefix is the service's permission family
// ("storage." for storage.googleapis.com resources).
func gcpRoleGrantableOnService(permissions []string, service string) bool {
	if service == "cloudresourcemanager" {
		return true
	}
	prefix := service + "."
	for _, p := range permissions {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// iamLintCondition runs lintPolicy's checks over one condition expression and
// returns the findings. The full Common Expression Language compile real
// Cloud IAM runs is not reimplemented here; these are the structural checks
// whose verdicts are certain either way — an empty expression, an
// unterminated string literal, or unbalanced brackets can never compile, and
// an expression free of those defects earns no finding from this validation
// unit. Each finding carries the CONDITION level and the
// ConditionCompileCheck unit name the real linter reports for a condition
// that fails to compile.
func iamLintCondition(expression string) []map[string]any {
	finding := func(offset int, message string) []map[string]any {
		return []map[string]any{{
			"level":              "CONDITION",
			"validationUnitName": "LintValidationUnits/ConditionCompileCheck",
			"fieldName":          "condition.expression",
			"locationOffset":     offset,
			"severity":           "ERROR",
			"debugMessage":       message,
		}}
	}
	if strings.TrimSpace(expression) == "" {
		return finding(0, "The condition expression is empty.")
	}

	// Bracket pairing and string-literal termination: track the open-bracket
	// stack outside string literals, and the quote that opened the literal
	// (with backslash escapes) inside one.
	type open struct {
		char   byte
		offset int
	}
	var stack []open
	inString := byte(0)
	stringStart := 0
	for i := 0; i < len(expression); i++ {
		c := expression[i]
		if inString != 0 {
			switch c {
			case '\\':
				i++
			case inString:
				inString = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inString = c
			stringStart = i
		case '(', '[', '{':
			stack = append(stack, open{char: c, offset: i})
		case ')', ']', '}':
			expected := map[byte]byte{')': '(', ']': '[', '}': '{'}[c]
			if len(stack) == 0 || stack[len(stack)-1].char != expected {
				return finding(i, fmt.Sprintf("Syntax error: unmatched %q.", string(c)))
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inString != 0 {
		return finding(stringStart, "Syntax error: unterminated string literal.")
	}
	if len(stack) > 0 {
		last := stack[len(stack)-1]
		return finding(last.offset, fmt.Sprintf("Syntax error: unclosed %q.", string(last.char)))
	}
	return nil
}

// gcpCustomRoleJSON renders a custom role into the roles.get / roles.list wire
// shape. includedPermissions is omitted unless full (roles.list BASIC view
// carries no permissions; roles.get and create/update return FULL).
func gcpCustomRoleJSON(role GCPCustomRole, full bool) map[string]any {
	out := map[string]any{
		"name":  role.Name,
		"title": role.Title,
		"etag":  role.Etag,
	}
	if role.Description != "" {
		out["description"] = role.Description
	}
	if role.Stage != "" {
		out["stage"] = role.Stage
	}
	if role.Deleted {
		out["deleted"] = true
	}
	if full {
		out["includedPermissions"] = role.IncludedPermissions
	}
	return out
}

// gcpPredefinedRole is a Google-managed IAM role as roles.get and roles.list
// return it.
type gcpPredefinedRole struct {
	Name                string
	Title               string
	Description         string
	Stage               string
	IncludedPermissions []string
}

// gcpBasicRoles are the legacy basic roles, described as roles.get
// describes them.
var gcpBasicRoles = []gcpPredefinedRole{
	{Name: gcpRoleViewer, Title: "Viewer", Description: "View most Google Cloud resources. See the list of included permissions.", Stage: "GA"},
	{Name: gcpRoleEditor, Title: "Editor", Description: "View, create, update, and delete most Google Cloud resources. See the list of included permissions.", Stage: "GA"},
	{Name: gcpRoleOwner, Title: "Owner", Description: "Full access to most Google Cloud resources. See the list of included permissions.", Stage: "GA"},
}

// gcpPredefinedRoles are the roles roles.list serves: the basic roles and
// every predefined role of the served services.
func gcpPredefinedRoles() []gcpPredefinedRole {
	roles := make([]gcpPredefinedRole, 0, len(gcpBasicRoles)+len(gcpCatalog().roles))
	for _, basic := range gcpBasicRoles {
		basic.IncludedPermissions = gcpBasicRoleIncludedPermissions(basic.Name)
		roles = append(roles, basic)
	}
	for _, r := range gcpCatalog().roles {
		roles = append(roles, gcpPredefinedRole(r))
	}
	return roles
}

// gcpRoleJSON renders a predefined role into the roles.get / roles.list wire
// shape. includedPermissions is omitted unless full (roles.list defaults to
// BASIC view, which carries no permissions; roles.get returns FULL). Basic
// and predefined roles always carry the etag AA==.
func gcpRoleJSON(role gcpPredefinedRole, full bool) map[string]any {
	out := map[string]any{
		"name":        role.Name,
		"title":       role.Title,
		"description": role.Description,
		"stage":       role.Stage,
		"etag":        "AA==",
	}
	if full {
		out["includedPermissions"] = role.IncludedPermissions
	}
	return out
}

// iamWriteRolesPage writes one page of a roles.list response in role name
// order. The method's documented default page size is 300, its maximum 1,000.
func iamWriteRolesPage(w http.ResponseWriter, r *http.Request, roles []map[string]any) {
	size, ok := gcpPageSizeParam(w, r, "pageSize")
	if !ok {
		return
	}
	page, next, ok := gcpKeyPage(w, roles, []gcpSortKey[map[string]any]{gcpAsc(gcpField("name"))},
		r.URL.Query().Get("pageToken"), size, 300, 1000)
	if !ok {
		return
	}
	resp := map[string]any{"roles": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}
