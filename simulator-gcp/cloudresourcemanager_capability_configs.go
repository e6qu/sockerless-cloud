package main

// Cloud Resource Manager v3 capabilityConfigs.
//
// A CapabilityConfig declares which capabilities — AppHub application
// management, Agent Registry agent management — are enabled over a node of the
// resource hierarchy and its sub-tree, and names the management project that
// holds the capability's own resources. The collection is published three
// times, once under each hierarchy node (organizations, folders, projects),
// and all three address one store: the config's name carries its parent, so a
// folder's config and an organization's config never collide.
//
// It is the same kind of record as the `folders/{folder}/capabilities/{name}`
// toggle beside it — configuration the caller writes and reads back — with one
// piece of real state behind it: the management project. Google creates one
// when the caller supplies none, and a project is a resource this simulator
// models, so the created project is a project in the same store every other
// Cloud Resource Manager read serves rather than a name pointing at nothing.
//
// The capability itself (an AppHub application, an Agent Registry agent) is
// not modelled here, and nothing in this simulator reads a CapabilityConfig
// back: it is the resource-hierarchy configuration, which is what Cloud
// Resource Manager owns and all this API writes.

import (
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CRMCapabilityConfig mirrors the cloudresourcemanager#CapabilityConfig (v3)
// resource.
type CRMCapabilityConfig struct {
	Name              string   `json:"name"`
	DisplayName       string   `json:"displayName,omitempty"`
	Types             []string `json:"types,omitempty"`
	Boundaries        []string `json:"boundaries,omitempty"`
	ManagementProject string   `json:"managementProject,omitempty"`
	State             string   `json:"state,omitempty"`
	CreateTime        string   `json:"createTime,omitempty"`
	UpdateTime        string   `json:"updateTime,omitempty"`
	Etag              string   `json:"etag,omitempty"`
}

// crmCapabilityConfigs holds every CapabilityConfig, keyed by its full
// resource name ("folders/123/capabilityConfigs/my-config").
var crmCapabilityConfigs sim.Store[CRMCapabilityConfig]

// Fully-qualified Any types of the three capabilityConfigs verbs the document
// models as long-running. Each metadata message is empty, which is what the
// document says of all three.
const (
	crmTypeCapabilityConfig       = "type.googleapis.com/google.cloud.resourcemanager.v3.CapabilityConfig"
	crmMetaCreateCapabilityConfig = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateCapabilityConfigMetadata"
	crmMetaUpdateCapabilityConfig = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateCapabilityConfigMetadata"
	crmMetaDeleteCapabilityConfig = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteCapabilityConfigMetadata"
)

// crmCapabilityConfigIDPattern is the documented capabilityConfigId shape: 6
// to 30 lowercase letters, digits or hyphens, starting with a letter, with
// trailing hyphens prohibited.
var crmCapabilityConfigIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// crmCapabilityDisplayNamePattern is the documented displayName shape: 4 to 30
// characters drawn from letters, digits, hyphen, single-quote, double-quote,
// space and exclamation point.
var crmCapabilityDisplayNamePattern = regexp.MustCompile(`^[A-Za-z0-9\-'" !]{4,30}$`)

// crmBoundaryPattern is the documented format of a boundary reference:
// "<hierarchy node>/boundaries/<boundary>". Cloud Resource Manager publishes
// no boundaries collection, so a reference is stored as the caller wrote it
// and only its shape is judged.
var crmBoundaryPattern = regexp.MustCompile(`^(organizations|folders|projects)/[^/]+/boundaries/[^/]+$`)

// crmManagementProjectPattern is the documented management-project format.
var crmManagementProjectPattern = regexp.MustCompile(`^projects/[^/]+$`)

// crmCapabilityConfigTypes are the capabilities a config can enable.
// TYPE_UNSPECIFIED is the enum's zero value, not a capability, so a request
// naming it is refused the way any other undefined type is.
var crmCapabilityConfigTypes = map[string]bool{
	"APP_MANAGEMENT":   true,
	"AGENT_MANAGEMENT": true,
}

// crmCapabilityParent resolves the hierarchy node a capabilityConfigs request
// is addressed under, and answers with that node's own not-found response when
// the deployment has no such node. The canonical name comes back — a project
// addressed by its id resolves to "projects/{number}" — so one config is
// reachable under every spelling of its parent instead of one config per
// spelling.
func crmCapabilityParent(w http.ResponseWriter, r *http.Request, collection string) (string, bool) {
	id := sim.PathParam(r, "parent")
	switch collection {
	case "projects":
		p, ok := crmResolveProject(id)
		if !ok {
			crmProjectPermissionDenied(w)
			return "", false
		}
		return p.Name, true
	case "folders":
		f, ok := crmFolders.Get("folders/" + id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return "", false
		}
		return f.Name, true
	default:
		org, ok := crmOrganizations.Get("organizations/" + id)
		if !ok {
			crmOrganizationPermissionDenied(w)
			return "", false
		}
		return org.Name, true
	}
}

// crmValidateCapabilityConfig judges the members a caller may write. It
// returns the INVALID_ARGUMENT message to answer with, empty when the config
// is well formed.
func crmValidateCapabilityConfig(cfg CRMCapabilityConfig, requireTypes bool) string {
	if cfg.DisplayName != "" && !crmCapabilityDisplayNamePattern.MatchString(cfg.DisplayName) {
		return "Invalid display_name: must be 4 to 30 letters, digits, hyphens, quotes, spaces or exclamation points."
	}
	if requireTypes && len(cfg.Types) == 0 {
		return "Field [types] is required and must name at least one capability."
	}
	for _, t := range cfg.Types {
		if !crmCapabilityConfigTypes[t] {
			return "Invalid type " + t + ": must be one of APP_MANAGEMENT, AGENT_MANAGEMENT."
		}
	}
	for _, b := range cfg.Boundaries {
		if !crmBoundaryPattern.MatchString(b) {
			return "Invalid boundary " + b + ": must be organizations/{organization}/boundaries/{boundary}, folders/{folder}/boundaries/{boundary} or projects/{project}/boundaries/{boundary}."
		}
	}
	return ""
}

// crmCapabilityManagementProject resolves the management project a create
// names, or creates one. The API documents both halves: the field is optional
// and immutable, an unsupplied one "triggers the creation of a Management
// Project", and a project-scoped config must name its own. The created project
// is a real project in the store the rest of Cloud Resource Manager serves, so
// the name the config reports resolves.
func crmCapabilityManagementProject(w http.ResponseWriter, parent, supplied string) (string, bool) {
	if supplied != "" {
		if !crmManagementProjectPattern.MatchString(supplied) {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"Invalid management_project %q: must be projects/{project_number}.", supplied)
			return "", false
		}
		p, ok := crmResolveProject(strings.TrimPrefix(supplied, "projects/"))
		if !ok {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"Management project %q was not found.", supplied)
			return "", false
		}
		return p.Name, true
	}
	if strings.HasPrefix(parent, "projects/") {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"Field [management_project] is required for a project-scoped CapabilityConfig.")
		return "", false
	}
	p := CRMProject{
		Name:        "projects/" + gcpNumericID(12),
		Parent:      parent,
		ProjectId:   "capability-mgmt-" + gcpNumericID(10),
		State:       "ACTIVE",
		DisplayName: "Capability management project",
		CreateTime:  nowTimestamp(),
		UpdateTime:  nowTimestamp(),
		Etag:        crmEtag(),
	}
	crmProjects.Put(p.ProjectId, p)
	return p.Name, true
}

// crmCapabilityConfigName reads the addressed config's full resource name.
func crmCapabilityConfigName(w http.ResponseWriter, r *http.Request, collection string) (string, bool) {
	parent, ok := crmCapabilityParent(w, r, collection)
	if !ok {
		return "", false
	}
	return parent + "/capabilityConfigs/" + sim.PathParam(r, "capabilityConfig"), true
}

// crmGetStoredCapabilityConfig reads the addressed config, answering NOT_FOUND
// when the parent holds no such config.
func crmGetStoredCapabilityConfig(w http.ResponseWriter, r *http.Request, collection string) (CRMCapabilityConfig, bool) {
	name, ok := crmCapabilityConfigName(w, r, collection)
	if !ok {
		return CRMCapabilityConfig{}, false
	}
	cfg, found := crmCapabilityConfigs.Get(name)
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CapabilityConfig %q was not found.", name)
		return CRMCapabilityConfig{}, false
	}
	return cfg, true
}

func crmCreateCapabilityConfig(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parent, ok := crmCapabilityParent(w, r, collection)
		if !ok {
			return
		}
		var req CRMCapabilityConfig
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		id := r.URL.Query().Get("capabilityConfigId")
		if !crmCapabilityConfigIDPattern.MatchString(id) {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"Invalid capabilityConfigId %q: must be 6 to 30 lowercase letters, digits or hyphens, start with a letter and not end with a hyphen.", id)
			return
		}
		if msg := crmValidateCapabilityConfig(req, true); msg != "" {
			GCPError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
			return
		}
		name := parent + "/capabilityConfigs/" + id
		if _, exists := crmCapabilityConfigs.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "CapabilityConfig %q already exists.", name)
			return
		}
		management, ok := crmCapabilityManagementProject(w, parent, req.ManagementProject)
		if !ok {
			return
		}
		cfg := CRMCapabilityConfig{
			Name:              name,
			DisplayName:       req.DisplayName,
			Types:             req.Types,
			Boundaries:        req.Boundaries,
			ManagementProject: management,
			// The management project is created and active by the time the
			// operation settles, which is what ACTIVE reports.
			State:      "ACTIVE",
			CreateTime: nowTimestamp(),
			UpdateTime: nowTimestamp(),
			Etag:       crmEtag(),
		}
		crmCapabilityConfigs.Put(name, cfg)
		sim.WriteJSON(w, http.StatusOK, crmLRO(cfg, crmTypeCapabilityConfig, crmMetaCreateCapabilityConfig))
	}
}

func crmListCapabilityConfigs(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parent, ok := crmCapabilityParent(w, r, collection)
		if !ok {
			return
		}
		// The method lists the direct children of the named node: a config
		// under a folder of this organization is that folder's, not this
		// organization's.
		prefix := parent + "/capabilityConfigs/"
		rows := crmCapabilityConfigs.Filter(func(cfg CRMCapabilityConfig) bool {
			return strings.HasPrefix(cfg.Name, prefix)
		})
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		resp := map[string]any{"capabilityConfigs": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	}
}

func crmReadCapabilityConfig(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, ok := crmGetStoredCapabilityConfig(w, r, collection)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, cfg)
	}
}

// crmMaskField normalizes a field-mask path to the member it names, so the
// proto spelling ("display_name") and the JSON one ("displayName") compare
// equal.
func crmMaskField(path string) string {
	return strings.ToLower(strings.ReplaceAll(path, "_", ""))
}

// crmCapabilityConfigMutableFields are the members patch updates, which the
// method's own description names: display_name, types and boundaries.
var crmCapabilityConfigMutableFields = map[string]bool{
	"displayname": true,
	"types":       true,
	"boundaries":  true,
}

func crmUpdateCapabilityConfig(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, ok := crmGetStoredCapabilityConfig(w, r, collection)
		if !ok {
			return
		}
		var req CRMCapabilityConfig
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// The etag is the caller's statement about which version it read; an
		// empty one writes unconditionally, a stale one is refused so the
		// caller re-reads rather than overwriting a change it never saw.
		if req.Etag != "" && req.Etag != cfg.Etag {
			GCPErrorf(w, http.StatusConflict, "ABORTED",
				"CapabilityConfig etag does not match the current CapabilityConfig; read it again and retry.")
			return
		}
		mask := r.URL.Query().Get("updateMask")
		var fields []string
		for _, field := range strings.Split(mask, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			if !crmCapabilityConfigMutableFields[crmMaskField(field)] {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"Invalid update_mask path %q: only display_name, types and boundaries can be updated.", field)
				return
			}
			fields = append(fields, field)
		}
		// An omitted mask updates every mutable member, so a request that omits
		// one writes the whole mutable record — including types, which the
		// resource requires and so cannot be emptied that way.
		//
		// A mask names its member in either spelling, the proto one
		// ("display_name") or the JSON one ("displayName").
		updates := func(field string) bool {
			if len(fields) == 0 {
				return true
			}
			for _, named := range fields {
				if crmMaskField(named) == crmMaskField(field) {
					return true
				}
			}
			return false
		}
		if msg := crmValidateCapabilityConfig(req, updates("types")); msg != "" {
			GCPError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
			return
		}
		if updates("displayName") {
			cfg.DisplayName = req.DisplayName
		}
		if updates("types") {
			cfg.Types = req.Types
		}
		if updates("boundaries") {
			cfg.Boundaries = req.Boundaries
		}
		cfg.UpdateTime = nowTimestamp()
		cfg.Etag = crmEtag()
		crmCapabilityConfigs.Put(cfg.Name, cfg)
		sim.WriteJSON(w, http.StatusOK, crmLRO(cfg, crmTypeCapabilityConfig, crmMetaUpdateCapabilityConfig))
	}
}

func crmDeleteCapabilityConfig(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, ok := crmGetStoredCapabilityConfig(w, r, collection)
		if !ok {
			return
		}
		crmCapabilityConfigs.Delete(cfg.Name)
		// The management project outlives the config: deleting a project is
		// DeleteProject's job, and Cloud Resource Manager does not delete one
		// out from under whatever the capability put in it.
		sim.WriteJSON(w, http.StatusOK, crmLRO(cfg, crmTypeCapabilityConfig, crmMetaDeleteCapabilityConfig))
	}
}

// registerCRMCapabilityConfigs mounts the capabilityConfigs collection on each
// of the three hierarchy nodes that publishes it. The three sets of routes
// share one set of handlers and one store; the collection they are mounted
// under is what tells a handler which kind of parent to resolve.
func registerCRMCapabilityConfigs(srv *sim.Server) {
	crmCapabilityConfigs = sim.MakeStore[CRMCapabilityConfig](srv.DB(), "crm_capability_configs")

	srv.HandleFunc("POST /v3/folders/{parent}/capabilityConfigs", crmCreateCapabilityConfig("folders"))
	srv.HandleFunc("GET /v3/folders/{parent}/capabilityConfigs", crmListCapabilityConfigs("folders"))
	srv.HandleFunc("GET /v3/folders/{parent}/capabilityConfigs/{capabilityConfig}", crmReadCapabilityConfig("folders"))
	srv.HandleFunc("PATCH /v3/folders/{parent}/capabilityConfigs/{capabilityConfig}", crmUpdateCapabilityConfig("folders"))
	srv.HandleFunc("DELETE /v3/folders/{parent}/capabilityConfigs/{capabilityConfig}", crmDeleteCapabilityConfig("folders"))

	srv.HandleFunc("POST /v3/organizations/{parent}/capabilityConfigs", crmCreateCapabilityConfig("organizations"))
	srv.HandleFunc("GET /v3/organizations/{parent}/capabilityConfigs", crmListCapabilityConfigs("organizations"))
	srv.HandleFunc("GET /v3/organizations/{parent}/capabilityConfigs/{capabilityConfig}", crmReadCapabilityConfig("organizations"))
	srv.HandleFunc("PATCH /v3/organizations/{parent}/capabilityConfigs/{capabilityConfig}", crmUpdateCapabilityConfig("organizations"))
	srv.HandleFunc("DELETE /v3/organizations/{parent}/capabilityConfigs/{capabilityConfig}", crmDeleteCapabilityConfig("organizations"))

	srv.HandleFunc("POST /v3/projects/{parent}/capabilityConfigs", crmCreateCapabilityConfig("projects"))
	srv.HandleFunc("GET /v3/projects/{parent}/capabilityConfigs", crmListCapabilityConfigs("projects"))
	srv.HandleFunc("GET /v3/projects/{parent}/capabilityConfigs/{capabilityConfig}", crmReadCapabilityConfig("projects"))
	srv.HandleFunc("PATCH /v3/projects/{parent}/capabilityConfigs/{capabilityConfig}", crmUpdateCapabilityConfig("projects"))
	srv.HandleFunc("DELETE /v3/projects/{parent}/capabilityConfigs/{capabilityConfig}", crmDeleteCapabilityConfig("projects"))
}
