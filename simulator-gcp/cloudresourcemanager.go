package main

// Cloud Resource Manager v1 projects surface + the Cloud Billing project
// billing-info read.
//
// gcloud's `projects` command group speaks the v1 API (create / list /
// describe / delete / undelete / update, polling create through
// GET /v1/operations/{op}), and terraform-provider-google's google_project
// resource creates and reads through the same v1 wire, additionally reading
// GET /v1/projects/{project}/billingInfo (Cloud Billing) on every Read. The
// v3 surface — the one cloud.google.com/go/resourcemanager/apiv3 and the
// console SPA speak — lives in iam.go (registerCRMv3); both share one
// project store, so a project created over either wire is visible on both.

import (
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// crmProjects is the Cloud Resource Manager project store, keyed by project
// ID; the row's Name carries the "projects/{projectNumber}" resource name.
// registerCRMv3 initialises it before mounting either API version's routes.
var crmProjects sim.Store[CRMProject]

// crmDefaultOrganization is the simulator's organization — the parent every
// bootstrap resource hangs off, mirroring how the AWS simulator materializes
// its management account's organization.
const crmDefaultOrganization = "organizations/123456789012"

// crmEnsureDefaultProject materializes the org's pre-provisioned projects the
// way the AWS simulator materializes its management account: "sockerless" is
// the default project every deployment coordinate assumes (console, backends;
// its fixed project number matches the number the GCS slice stamps on bucket
// metadata), and "test-project" is the project the SDK/CLI/terraform
// harnesses are configured with. Idempotent across restarts.
func crmEnsureDefaultProject() {
	for _, seed := range []struct{ id, number string }{
		{"sockerless", "123456789012"},
		{"test-project", "735298346210"},
	} {
		if _, ok := crmProjects.Get(seed.id); ok {
			continue
		}
		crmProjects.Put(seed.id, CRMProject{
			Name:        "projects/" + seed.number,
			ProjectId:   seed.id,
			State:       "ACTIVE",
			DisplayName: seed.id,
			Parent:      crmDefaultOrganization,
			CreateTime:  nowTimestamp(),
			UpdateTime:  nowTimestamp(),
			Etag:        crmEtag(),
		})
	}
}

// crmResolveProject resolves a {project} path segment — project ID or project
// number, both of which every Cloud Resource Manager method accepts — to the
// stored row.
func crmResolveProject(ref string) (CRMProject, bool) {
	if p, ok := crmProjects.Get(ref); ok {
		return p, true
	}
	rows := crmProjects.Filter(func(p CRMProject) bool { return p.Name == "projects/"+ref })
	if len(rows) == 1 {
		return rows[0], true
	}
	return CRMProject{}, false
}

// crmProjectPermissionDenied writes the real Cloud Resource Manager response
// for a project the caller cannot see: 403 PERMISSION_DENIED, never 404 —
// the API does not disclose whether an inaccessible project exists.
func crmProjectPermissionDenied(w http.ResponseWriter) {
	GCPErrorf(w, http.StatusForbidden, "PERMISSION_DENIED", "The caller does not have permission")
}

// crmProjectIDPattern is the documented project-ID shape: 6–30 characters,
// lowercase letters, digits and hyphens, starting with a letter and not
// ending with a hyphen.
var crmProjectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

func crmValidateProjectID(id string) error {
	if id == "" {
		return fmt.Errorf("field [project_id] is required")
	}
	if !crmProjectIDPattern.MatchString(id) {
		return fmt.Errorf("field [project_id] has issue [project_id must be 6 to 30 lowercase letters, digits, or hyphens; it must start with a letter and cannot end with a hyphen]")
	}
	return nil
}

// crmFieldMatch compares one filter/query value (case-insensitive, '*'
// wildcards) against a field value.
func crmFieldMatch(pattern, value string) bool {
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(value))
	return err == nil && ok
}

// crmSearchMatch evaluates a v3 SearchProjects query against a project. The
// implemented subset covers the query fields real callers send —
// whitespace-separated key:value terms over id, name/displayName, state,
// parent and labels.<key>, plus bare terms matched against the project ID
// and display name — with '*' wildcards. An empty query matches everything.
func crmSearchMatch(p CRMProject, query string) bool {
	for _, term := range strings.Fields(query) {
		if strings.EqualFold(term, "AND") {
			continue
		}
		key, val, found := strings.Cut(term, ":")
		val = strings.Trim(val, `"`)
		if !found {
			bare := strings.Trim(term, `"`)
			if !crmFieldMatch("*"+bare+"*", p.ProjectId) && !crmFieldMatch("*"+bare+"*", p.DisplayName) {
				return false
			}
			continue
		}
		switch {
		case key == "id" || key == "projectId":
			if !crmFieldMatch(val, p.ProjectId) {
				return false
			}
		case key == "name" || key == "displayName":
			if !crmFieldMatch(val, p.DisplayName) {
				return false
			}
		case key == "state":
			if !crmFieldMatch(val, p.State) {
				return false
			}
		case key == "parent":
			if !crmFieldMatch(val, p.Parent) {
				return false
			}
		case strings.HasPrefix(key, "labels."):
			if !crmFieldMatch(val, p.Labels[strings.TrimPrefix(key, "labels.")]) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// crmV1ResourceID mirrors the cloudresourcemanager#ResourceId (v1) message.
type crmV1ResourceID struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
}

// crmV1ProjectMsg mirrors the cloudresourcemanager#Project (v1) resource:
// name is the display name, projectNumber is an int64 serialized as a string,
// and lifecycleState replaces v3's state.
type crmV1ProjectMsg struct {
	ProjectNumber  string            `json:"projectNumber,omitempty"`
	ProjectID      string            `json:"projectId"`
	LifecycleState string            `json:"lifecycleState,omitempty"`
	Name           string            `json:"name,omitempty"`
	CreateTime     string            `json:"createTime,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Parent         *crmV1ResourceID  `json:"parent,omitempty"`
}

// crmParentToV1 converts a v3 parent resource name ("organizations/123",
// "folders/456") to the v1 ResourceId form.
func crmParentToV1(parent string) *crmV1ResourceID {
	kind, id, found := strings.Cut(parent, "/")
	if !found {
		return nil
	}
	switch kind {
	case "organizations":
		return &crmV1ResourceID{Type: "organization", ID: id}
	case "folders":
		return &crmV1ResourceID{Type: "folder", ID: id}
	}
	return nil
}

// crmParentFromV1 converts a v1 ResourceId to the v3 parent resource name.
func crmParentFromV1(rid *crmV1ResourceID) string {
	if rid == nil || rid.ID == "" {
		return ""
	}
	switch rid.Type {
	case "organization":
		return "organizations/" + rid.ID
	case "folder":
		return "folders/" + rid.ID
	}
	return ""
}

// crmV1Project renders the stored project in the v1 wire shape.
func crmV1Project(p CRMProject) crmV1ProjectMsg {
	return crmV1ProjectMsg{
		ProjectNumber:  strings.TrimPrefix(p.Name, "projects/"),
		ProjectID:      p.ProjectId,
		LifecycleState: p.State,
		Name:           p.DisplayName,
		CreateTime:     p.CreateTime,
		Labels:         p.Labels,
		Parent:         crmParentToV1(p.Parent),
	}
}

// crmV1FilterMatch evaluates the v1 projects.list filter subset real clients
// send — whitespace/AND-joined key:value terms over id, name, lifecycleState,
// parent.type, parent.id and labels.<key>, with '*' wildcards; comparisons
// are case-insensitive. gcloud projects list always sends at least
// lifecycleState:ACTIVE. A malformed term is an error (real v1 400s).
func crmV1FilterMatch(p CRMProject, filter string) (bool, error) {
	v1 := crmV1Project(p)
	cleaned := strings.NewReplacer("(", " ", ")", " ").Replace(filter)
	for _, term := range strings.Fields(cleaned) {
		if strings.EqualFold(term, "AND") {
			continue
		}
		key, val, found := strings.Cut(term, ":")
		if !found {
			return false, fmt.Errorf("invalid filter expression %q", term)
		}
		val = strings.Trim(val, `"`)
		switch {
		case key == "id" || key == "projectId":
			if !crmFieldMatch(val, v1.ProjectID) {
				return false, nil
			}
		case key == "name":
			if !crmFieldMatch(val, v1.Name) {
				return false, nil
			}
		case key == "lifecycleState":
			if !crmFieldMatch(val, v1.LifecycleState) {
				return false, nil
			}
		case key == "parent.type":
			if v1.Parent == nil || !crmFieldMatch(val, v1.Parent.Type) {
				return false, nil
			}
		case key == "parent.id":
			if v1.Parent == nil || !crmFieldMatch(val, v1.Parent.ID) {
				return false, nil
			}
		case strings.HasPrefix(key, "labels."):
			if !crmFieldMatch(val, v1.Labels[strings.TrimPrefix(key, "labels.")]) {
				return false, nil
			}
		default:
			return false, fmt.Errorf("unsupported filter field %q", key)
		}
	}
	return true, nil
}

// crmV1LRO builds and persists the settled v1 create Operation: the response
// embeds the v1-shaped project, and the metadata is the real
// ProjectCreationStatus message gcloud's operation waiter reads.
func crmV1LRO(p CRMProject) Operation {
	resp := map[string]any{
		"@type":          "type.googleapis.com/google.cloudresourcemanager.v1.Project",
		"projectNumber":  strings.TrimPrefix(p.Name, "projects/"),
		"projectId":      p.ProjectId,
		"lifecycleState": p.State,
		"createTime":     p.CreateTime,
	}
	if p.DisplayName != "" {
		resp["name"] = p.DisplayName
	}
	if len(p.Labels) > 0 {
		resp["labels"] = p.Labels
	}
	if rid := crmParentToV1(p.Parent); rid != nil {
		resp["parent"] = rid
	}
	op := Operation{
		Name: "operations/cp." + gcpNumericID(19),
		Metadata: map[string]any{
			"@type":      "type.googleapis.com/google.cloudresourcemanager.v1.ProjectCreationStatus",
			"createTime": nowTimestamp(),
			"gettable":   true,
			"ready":      true,
		},
		Done:     true,
		Response: resp,
	}
	crOperations.Put(op.Name, op)
	return op
}

// crmLiens holds every lien. The v1 and v3 collections are the same resource
// under two spellings, so one store serves both.
var crmLiens sim.Store[CRMLien]

// crmProjectDeleteLien reports the lien blocking a project's deletion, if any.
// A lien whose restrictions name resourcemanager.projects.delete is exactly
// what the collection exists for: real DeleteProject refuses while one is in
// place, and the caller removes the lien before retrying. The parent may name
// the project by id or by number, both of which the API accepts.
func crmProjectDeleteLien(p CRMProject) (CRMLien, bool) {
	parents := []string{"projects/" + p.ProjectId, p.Name}
	for _, l := range crmLiens.List() {
		if !slices.Contains(parents, l.Parent) {
			continue
		}
		if slices.Contains(l.Restrictions, "resourcemanager.projects.delete") {
			return l, true
		}
	}
	return CRMLien{}, false
}

// crmProjectDeleteBlocked writes the response DeleteProject returns while a
// lien holds the project, and reports whether it wrote one.
func crmProjectDeleteBlocked(w http.ResponseWriter, p CRMProject) bool {
	if _, held := crmProjectDeleteLien(p); !held {
		return false
	}
	GCPError(w, http.StatusBadRequest, "Precondition check failed.", "FAILED_PRECONDITION")
	return true
}

// crmCreateLien serves liens.create for both API versions.
func crmCreateLien(w http.ResponseWriter, r *http.Request) {
	var req CRMLien
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	l := CRMLien{
		Name:         "liens/" + gcpNumericID(16),
		Parent:       req.Parent,
		Restrictions: req.Restrictions,
		Reason:       req.Reason,
		Origin:       req.Origin,
		CreateTime:   nowTimestamp(),
	}
	crmLiens.Put(l.Name, l)
	sim.WriteJSON(w, http.StatusOK, l)
}

// crmListLiens serves liens.list for both API versions.
func crmListLiens(w http.ResponseWriter, r *http.Request) {
	parent := r.URL.Query().Get("parent")
	rows := crmLiens.Filter(func(l CRMLien) bool { return parent == "" || l.Parent == parent })
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	page, next, ok := paginateList(w, r, rows)
	if !ok {
		return
	}
	resp := map[string]any{"liens": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// crmGetLien serves liens.get for both API versions.
func crmGetLien(w http.ResponseWriter, r *http.Request) {
	l, ok := crmLiens.Get("liens/" + sim.PathParam(r, "lien"))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "lien not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, l)
}

// crmDeleteLien serves liens.delete for both API versions.
func crmDeleteLien(w http.ResponseWriter, r *http.Request) {
	name := "liens/" + sim.PathParam(r, "lien")
	if !crmLiens.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "lien not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// CRMOrganization mirrors the cloudresourcemanager#Organization (v3)
// resource. The v1 wire renders the same record through crmV1Organization.
type CRMOrganization struct {
	Name                string `json:"name"`
	DisplayName         string `json:"displayName,omitempty"`
	DirectoryCustomerId string `json:"directoryCustomerId,omitempty"`
	State               string `json:"state,omitempty"`
	CreateTime          string `json:"createTime,omitempty"`
	UpdateTime          string `json:"updateTime,omitempty"`
	Etag                string `json:"etag,omitempty"`
}

// crmOrganizations holds the organizations the caller can see. An
// organization is created by Cloud Identity, not by Cloud Resource Manager,
// so the store has no create method: crmEnsureDefaultOrganization
// materializes the deployment's one organization the way
// crmEnsureDefaultProject materializes its projects.
var crmOrganizations sim.Store[CRMOrganization]

// crmEnsureDefaultOrganization materializes the organization every bootstrap
// resource hangs off — the parent crmDefaultOrganization names. Idempotent
// across restarts.
func crmEnsureDefaultOrganization() {
	if _, ok := crmOrganizations.Get(crmDefaultOrganization); ok {
		return
	}
	crmOrganizations.Put(crmDefaultOrganization, CRMOrganization{
		Name:                crmDefaultOrganization,
		DisplayName:         "example.com",
		DirectoryCustomerId: "C0xxxxxxx",
		State:               "ACTIVE",
		CreateTime:          nowTimestamp(),
		UpdateTime:          nowTimestamp(),
		Etag:                crmEtag(),
	})
}

// crmOrganizationPermissionDenied writes the response Cloud Resource Manager
// returns for an organization the caller cannot see: 403 PERMISSION_DENIED,
// never 404 — as with projects, the API does not disclose existence.
func crmOrganizationPermissionDenied(w http.ResponseWriter) {
	crmProjectPermissionDenied(w)
}

// crmV1OrganizationOwner mirrors the cloudresourcemanager#OrganizationOwner
// (v1) message.
type crmV1OrganizationOwner struct {
	DirectoryCustomerId string `json:"directoryCustomerId,omitempty"`
}

// crmV1OrganizationMsg mirrors the cloudresourcemanager#Organization (v1)
// resource: the customer id moves under owner, and lifecycleState replaces
// v3's state.
type crmV1OrganizationMsg struct {
	Name           string                  `json:"name"`
	DisplayName    string                  `json:"displayName,omitempty"`
	Owner          *crmV1OrganizationOwner `json:"owner,omitempty"`
	CreationTime   string                  `json:"creationTime,omitempty"`
	LifecycleState string                  `json:"lifecycleState,omitempty"`
}

// crmV1Organization renders the stored organization in the v1 wire shape.
func crmV1Organization(o CRMOrganization) crmV1OrganizationMsg {
	msg := crmV1OrganizationMsg{
		Name:           o.Name,
		DisplayName:    o.DisplayName,
		CreationTime:   o.CreateTime,
		LifecycleState: o.State,
	}
	if o.DirectoryCustomerId != "" {
		msg.Owner = &crmV1OrganizationOwner{DirectoryCustomerId: o.DirectoryCustomerId}
	}
	return msg
}

// crmOrganizationMatch evaluates a SearchOrganizations filter against an
// organization. The documented fields are domain and owner.directoryCustomerId,
// joined by whitespace or AND; a bare term matches the display name. An empty
// filter matches every organization the caller can see.
func crmOrganizationMatch(o CRMOrganization, filter string) bool {
	for _, term := range strings.Fields(filter) {
		if strings.EqualFold(term, "AND") {
			continue
		}
		key, val, found := strings.Cut(term, ":")
		val = strings.Trim(val, `"`)
		switch {
		case !found:
			if !crmFieldMatch("*"+strings.Trim(term, `"`)+"*", o.DisplayName) {
				return false
			}
		case key == "domain" || key == "displayName":
			if !crmFieldMatch(val, o.DisplayName) {
				return false
			}
		case key == "owner.directoryCustomerId" || key == "directoryCustomerId":
			if !crmFieldMatch(val, o.DirectoryCustomerId) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// crmProjectAncestry renders the projects.getAncestry (v1) answer: the project
// itself first — identified by its project number, the way the v1 ResourceId
// spells a project — then each ancestor up to the organization.
func crmProjectAncestry(p CRMProject) []map[string]any {
	ancestors := []map[string]any{{"resourceId": crmV1ResourceID{
		Type: "project",
		ID:   strings.TrimPrefix(p.Name, "projects/"),
	}}}
	chain := crmOrgPolicyAncestry("projects/" + p.ProjectId)
	// crmOrgPolicyAncestry runs root-first and includes the project itself;
	// getAncestry runs leaf-first over the ancestors only.
	for i := len(chain) - 2; i >= 0; i-- {
		if rid := crmParentToV1(chain[i]); rid != nil {
			ancestors = append(ancestors, map[string]any{"resourceId": *rid})
		}
	}
	return ancestors
}

// crmGetOperation serves the Cloud Resource Manager operations.get read for
// both API versions; the operation store holds every settled CRM operation.
func crmGetOperation(w http.ResponseWriter, r *http.Request) {
	name := "operations/" + sim.PathParam(r, "operation")
	if op, ok := crOperations.Get(name); ok {
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
}

// crmV1ProjectPOSTMethods are the POST custom methods the simulator serves on
// a Cloud Resource Manager v1 project: the lifecycle verb, the IAM triple, the
// hierarchy read, and the org-policy family every hierarchy node exposes.
var crmV1ProjectPOSTMethods = crmWithOrgPolicyVerbs(map[string]bool{
	"undelete":           true,
	"getAncestry":        true,
	"getIamPolicy":       true,
	"setIamPolicy":       true,
	"testIamPermissions": true,
})

// crmV1FolderPOSTMethods are the POST custom methods Cloud Resource Manager v1
// serves on a folder. v1 models no folder lifecycle — folders are created,
// read and moved through v3 — so the org-policy family is the whole set.
var crmV1FolderPOSTMethods = crmWithOrgPolicyVerbs(nil)

// crmV1OrganizationPOSTMethods are the POST custom methods Cloud Resource
// Manager v1 serves on an organization.
var crmV1OrganizationPOSTMethods = crmWithOrgPolicyVerbs(map[string]bool{
	"getIamPolicy":       true,
	"setIamPolicy":       true,
	"testIamPermissions": true,
})

// crmWithOrgPolicyVerbs adds the six org-policy verbs to a resource's own
// served set.
func crmWithOrgPolicyVerbs(own map[string]bool) map[string]bool {
	out := map[string]bool{}
	for verb := range own {
		out[verb] = true
	}
	for verb := range crmOrgPolicyVerbs {
		out[verb] = true
	}
	return out
}

// The v3 sibling set. v3 adds projects.move to the methods v1 serves.
var crmV3ProjectPOSTMethods = map[string]bool{
	"move":               true,
	"undelete":           true,
	"getIamPolicy":       true,
	"setIamPolicy":       true,
	"testIamPermissions": true,
}

// registerCloudResourceManagerV1 mounts the v1 projects lifecycle surface,
// the operations.get polls of both API versions, and the Cloud Billing
// project billing-info read. The v1 GetProject read stays in registerCRMv3
// next to its v3 sibling.
func registerCloudResourceManagerV1(srv *sim.Server, projectPolicies, resourcePolicies sim.Store[IAMPolicy]) {
	// projects.create (v1): gcloud projects create POSTs the v1 Project
	// and waits on the returned operation.
	srv.HandleFunc("POST /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		var req crmV1ProjectMsg
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if err := crmValidateProjectID(req.ProjectID); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		if _, exists := crmProjects.Get(req.ProjectID); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Requested entity already exists")
			return
		}
		p := CRMProject{
			Name:        "projects/" + gcpNumericID(12),
			ProjectId:   req.ProjectID,
			State:       "ACTIVE",
			DisplayName: req.Name,
			Labels:      req.Labels,
			Parent:      crmParentFromV1(req.Parent),
			CreateTime:  nowTimestamp(),
			UpdateTime:  nowTimestamp(),
			Etag:        crmEtag(),
		}
		crmProjects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, crmV1LRO(p))
	})

	// projects.list (v1): gcloud projects list, with its always-sent
	// lifecycleState filter.
	srv.HandleFunc("GET /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		var filterErr error
		rows := crmProjects.Filter(func(p CRMProject) bool {
			ok, err := crmV1FilterMatch(p, filter)
			if err != nil {
				filterErr = err
			}
			return ok
		})
		if filterErr != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", filterErr)
			return
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ProjectId < rows[j].ProjectId })
		page, next, ok := paginateList(w, r, rows)
		if !ok {
			return
		}
		out := make([]crmV1ProjectMsg, 0, len(page))
		for _, p := range page {
			out = append(out, crmV1Project(p))
		}
		resp := map[string]any{"projects": out}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// projects.delete (v1): soft-delete into DELETE_REQUESTED (the 30-day
	// pending-deletion window); only an ACTIVE project may be deleted.
	srv.HandleFunc("DELETE /v1/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		if p.State != "ACTIVE" {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "Project \"%s\" has lifecycle state %s; delete requires ACTIVE", p.ProjectId, p.State)
			return
		}
		if crmProjectDeleteBlocked(w, p) {
			return
		}
		p.State = "DELETE_REQUESTED"
		p.DeleteTime = nowTimestamp()
		p.UpdateTime = nowTimestamp()
		crmProjects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// projects.update (v1): full-resource PUT; gcloud projects update does
	// a read-modify-write of name and labels through it.
	srv.HandleFunc("PUT /v1/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		var req crmV1ProjectMsg
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		p.DisplayName = req.Name
		p.Labels = req.Labels
		if parent := crmParentFromV1(req.Parent); parent != "" {
			p.Parent = parent
		}
		p.UpdateTime = nowTimestamp()
		p.Etag = crmEtag()
		crmProjects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, crmV1Project(p))
	})

	// projects colon-verbs (v1): undelete, getAncestry, the org-policy family
	// and the IAM triple gcloud projects get-iam-policy / set-iam-policy
	// speak. The policy store is shared with the v3 verbs, so both API
	// versions address one policy.
	srv.HandleFunc("POST /v1/projects/{projectAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "projectAction")
		id, action, found := gcpCustomMethod(idAction)
		// Resolve the method before the project, the way Google's frontend
		// does. This pattern receives every POST custom method addressed to a
		// project — including other services': real Google separates services
		// by hostname, so Cloud SQL Admin's `projects/{p}:pointInTimeRestore`
		// shares this URI shape, and on the collapsed single-port mux this
		// dispatcher receives it. That verb identifies its owner (Cloud
		// Resource Manager publishes no such method) and is forwarded there;
		// a verb neither service publishes is answered as unrouted rather
		// than with the project's 403, which would claim the project is
		// inaccessible when the method is simply not routed.
		if found && action == "pointInTimeRestore" {
			handleSQLPointInTimeRestore(w, r, id)
			return
		}
		if !found || !crmV1ProjectPOSTMethods[action] {
			gcpMethodNotFound(w)
			return
		}
		p, ok := crmResolveProject(id)
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		if crmOrgPolicyVerb(w, r, "projects/"+p.ProjectId, action) {
			return
		}
		if crmIamVerb(w, r, projectPolicies, p.ProjectId+":"+action, "project") {
			return
		}
		if action == "getAncestry" {
			sim.WriteJSON(w, http.StatusOK, map[string]any{"ancestor": crmProjectAncestry(p)})
			return
		}
		// The org-policy family, the IAM triple and getAncestry are served
		// above, so undelete is the only method crmV1ProjectPOSTMethods still
		// admits here.
		if p.State != "DELETE_REQUESTED" {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "Project \"%s\" has lifecycle state %s; undelete requires DELETE_REQUESTED", p.ProjectId, p.State)
			return
		}
		p.State = "ACTIVE"
		p.DeleteTime = ""
		p.UpdateTime = nowTimestamp()
		crmProjects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// folders colon-verbs (v1): the org-policy family
	// terraform-provider-google's google_folder_organization_policy speaks.
	// The folder itself lives in the v3 store both API versions read.
	srv.HandleFunc("POST /v1/folders/{folderAction}", func(w http.ResponseWriter, r *http.Request) {
		id, action, found := gcpCustomMethod(sim.PathParam(r, "folderAction"))
		if !found || !crmV1FolderPOSTMethods[action] {
			gcpMethodNotFound(w)
			return
		}
		name := "folders/" + id
		if _, ok := crmFolders.Get(name); !ok {
			crmFolderNotFound(w)
			return
		}
		if !crmOrgPolicyVerb(w, r, name, action) {
			gcpMethodNotFound(w)
		}
	})

	// organizations (v1): the get and search reads gcloud organizations
	// describe / list speak, the IAM triple, and the org-policy family
	// terraform-provider-google's google_organization_policy speaks.
	srv.HandleFunc("GET /v1/organizations/{org}", func(w http.ResponseWriter, r *http.Request) {
		org, ok := crmOrganizations.Get("organizations/" + sim.PathParam(r, "org"))
		if !ok {
			crmOrganizationPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, crmV1Organization(org))
	})
	srv.HandleFunc("POST /v1/organizations:search", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Filter    string `json:"filter"`
			PageSize  int    `json:"pageSize"`
			PageToken string `json:"pageToken"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		rows := crmOrganizations.Filter(func(o CRMOrganization) bool { return crmOrganizationMatch(o, req.Filter) })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		out := make([]crmV1OrganizationMsg, 0, len(rows))
		for _, o := range rows {
			out = append(out, crmV1Organization(o))
		}
		page, next, ok := crmOrgPolicyPage(w, out, req.PageSize, req.PageToken)
		if !ok {
			return
		}
		resp := map[string]any{"organizations": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("POST /v1/organizations/{orgAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "orgAction")
		id, action, found := gcpCustomMethod(idAction)
		if !found || !crmV1OrganizationPOSTMethods[action] {
			gcpMethodNotFound(w)
			return
		}
		name := "organizations/" + id
		if _, ok := crmOrganizations.Get(name); !ok {
			crmOrganizationPermissionDenied(w)
			return
		}
		if crmOrgPolicyVerb(w, r, name, action) {
			return
		}
		if !crmIamVerb(w, r, resourcePolicies, idAction, "organization") {
			gcpMethodNotFound(w)
		}
	})

	// liens (v1): the same lien store the v3 collection serves.
	// terraform-provider-google's google_resource_manager_lien speaks v1.
	srv.HandleFunc("POST /v1/liens", crmCreateLien)
	srv.HandleFunc("GET /v1/liens", crmListLiens)
	srv.HandleFunc("GET /v1/liens/{lien}", crmGetLien)
	srv.HandleFunc("DELETE /v1/liens/{lien}", crmDeleteLien)

	// operations.get — v1 (gcloud's create waiter, terraform's
	// ResourceManagerOperationWaiter) and v3 (the resourcemanager GAPIC
	// LRO poller).
	srv.HandleFunc("GET /v1/operations/{operation}", crmGetOperation)
	srv.HandleFunc("GET /v3/operations/{operation}", crmGetOperation)

	// Cloud Billing projects.getBillingInfo: google_project's terraform
	// Read calls it unconditionally. The answer comes from the same link
	// store projects.updateBillingInfo writes, so the two halves of the
	// surface never disagree; a project with no link returns an empty
	// billingAccountName with billing disabled.
	srv.HandleFunc("GET /v1/projects/{project}/billingInfo", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, billingProjectInfo(p.ProjectId))
	})
}
