package main

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CRMProject mirrors the cloudresourcemanager#Project (v3) resource.
type CRMProject struct {
	Name        string            `json:"name"`
	Parent      string            `json:"parent,omitempty"`
	ProjectId   string            `json:"projectId"`
	State       string            `json:"state"`
	DisplayName string            `json:"displayName,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
	DeleteTime  string            `json:"deleteTime,omitempty"`
	Etag        string            `json:"etag,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// CRMFolder mirrors the cloudresourcemanager#Folder (v3) resource.
type CRMFolder struct {
	Name        string `json:"name"`
	Parent      string `json:"parent,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	State       string `json:"state"`
	CreateTime  string `json:"createTime,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty"`
	DeleteTime  string `json:"deleteTime,omitempty"`
	Etag        string `json:"etag,omitempty"`
}

var crmFolders sim.Store[CRMFolder]

// CRMLien mirrors the cloudresourcemanager#Lien (v3) resource.
type CRMLien struct {
	Name         string   `json:"name"`
	Parent       string   `json:"parent,omitempty"`
	Restrictions []string `json:"restrictions,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Origin       string   `json:"origin,omitempty"`
	CreateTime   string   `json:"createTime,omitempty"`
}

// CRMTagKey mirrors the cloudresourcemanager#TagKey (v3) resource.
type CRMTagKey struct {
	Name               string `json:"name"`
	Parent             string `json:"parent,omitempty"`
	ShortName          string `json:"shortName,omitempty"`
	NamespacedName     string `json:"namespacedName,omitempty"`
	Description        string `json:"description,omitempty"`
	CreateTime         string `json:"createTime,omitempty"`
	UpdateTime         string `json:"updateTime,omitempty"`
	Etag               string `json:"etag,omitempty"`
	Purpose            string `json:"purpose,omitempty"`
	AllowedValuesRegex string `json:"allowedValuesRegex,omitempty"`
}

// CRMTagValue mirrors the cloudresourcemanager#TagValue (v3) resource.
type CRMTagValue struct {
	Name           string `json:"name"`
	Parent         string `json:"parent,omitempty"`
	ShortName      string `json:"shortName,omitempty"`
	NamespacedName string `json:"namespacedName,omitempty"`
	Description    string `json:"description,omitempty"`
	CreateTime     string `json:"createTime,omitempty"`
	UpdateTime     string `json:"updateTime,omitempty"`
	Etag           string `json:"etag,omitempty"`
}

// CRMTagBinding mirrors the cloudresourcemanager#TagBinding (v3) resource.
type CRMTagBinding struct {
	Name                   string `json:"name"`
	Parent                 string `json:"parent,omitempty"`
	TagValue               string `json:"tagValue,omitempty"`
	TagValueNamespacedName string `json:"tagValueNamespacedName,omitempty"`
}

// CRMTagBindingCollection mirrors the cloudresourcemanager#TagBindingCollection
// (v3) resource: the tags bound directly to one resource, addressed by that
// resource's percent-encoded name.
type CRMTagBindingCollection struct {
	Name             string            `json:"name"`
	FullResourceName string            `json:"fullResourceName,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	Etag             string            `json:"etag,omitempty"`
}

// CRMCapability mirrors the cloudresourcemanager#Capability (v3) resource: a
// boolean toggle on a folder, whose configured value a read reports back.
type CRMCapability struct {
	Name  string `json:"name"`
	Value bool   `json:"value"`
}

// CRMTagHold mirrors the cloudresourcemanager#TagHold (v3) resource.
type CRMTagHold struct {
	Name       string `json:"name"`
	Holder     string `json:"holder,omitempty"`
	Origin     string `json:"origin,omitempty"`
	HelpLink   string `json:"helpLink,omitempty"`
	CreateTime string `json:"createTime,omitempty"`
}

// Fully-qualified Any types of the per-verb Cloud Resource Manager v3
// operation-metadata messages. The GAPIC clients unmarshal the metadata
// against their registered proto types, so an invented type name fails the
// client-side resolve — each operation must carry the verb's real message.
const (
	crmMetaCreateProject              = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateProjectMetadata"
	crmMetaUpdateProject              = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateProjectMetadata"
	crmMetaDeleteProject              = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteProjectMetadata"
	crmMetaMoveProject                = "type.googleapis.com/google.cloud.resourcemanager.v3.MoveProjectMetadata"
	crmMetaUndeleteProject            = "type.googleapis.com/google.cloud.resourcemanager.v3.UndeleteProjectMetadata"
	crmMetaCreateFolder               = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateFolderMetadata"
	crmMetaUpdateFolder               = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateFolderMetadata"
	crmMetaDeleteFolder               = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteFolderMetadata"
	crmMetaMoveFolder                 = "type.googleapis.com/google.cloud.resourcemanager.v3.MoveFolderMetadata"
	crmMetaUndeleteFolder             = "type.googleapis.com/google.cloud.resourcemanager.v3.UndeleteFolderMetadata"
	crmMetaCreateTagKey               = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateTagKeyMetadata"
	crmMetaUpdateTagKey               = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateTagKeyMetadata"
	crmMetaDeleteTagKey               = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteTagKeyMetadata"
	crmMetaCreateTagValue             = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateTagValueMetadata"
	crmMetaUpdateTagValue             = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateTagValueMetadata"
	crmMetaDeleteTagValue             = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteTagValueMetadata"
	crmMetaCreateTagHold              = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateTagHoldMetadata"
	crmMetaDeleteTagHold              = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteTagHoldMetadata"
	crmMetaCreateTagBinding           = "type.googleapis.com/google.cloud.resourcemanager.v3.CreateTagBindingMetadata"
	crmMetaDeleteTagBinding           = "type.googleapis.com/google.cloud.resourcemanager.v3.DeleteTagBindingMetadata"
	crmMetaUpdateCapability           = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateCapabilityMetadata"
	crmMetaUpdateTagBindingCollection = "type.googleapis.com/google.cloud.resourcemanager.v3.UpdateTagBindingCollectionMetadata"
)

// crmLRO builds a settled (done=true) long-running Operation whose response
// embeds the supplied resource and whose metadata carries the verb's real
// per-operation metadata message type. The Cloud Resource Manager LRO
// mutations (project/folder/tagKey/tagValue create/patch/delete/move/
// undelete) return an Operation; the sim settles it synchronously, matching
// how every other IAM-admin collection in this file resolves its LROs. The
// operation is persisted so a client resuming it by name
// (GET /v3/operations/{op}, the path the resourcemanager GAPIC LRO poller
// uses) reads the same record.
func crmLRO(resource any, typeName, metadataType string) Operation {
	return crmLROWithMetadata(resource, typeName, map[string]any{"@type": metadataType})
}

func crmLROWithMetadata(resource any, typeName string, metadata map[string]any) Operation {
	resp := cloneAnyMap(anyToMap(resource))
	resp["@type"] = typeName
	op := Operation{
		Name:     "operations/cp." + gcpNumericID(19),
		Metadata: metadata,
		Done:     true,
		Response: resp,
	}
	if crOperations != nil {
		crOperations.Put(op.Name, op)
	}
	return op
}

// crmEtag mints an etag for a Cloud Resource Manager resource.
func crmEtag() string {
	return base64.StdEncoding.EncodeToString([]byte(sim.NewUUID()))
}

// crmIamVerb dispatches a Cloud Resource Manager v3 IAM colon-verb captured in
// an "{id}:verb" path parameter. It resolves the resource-scoped policy key and
// reuses the shared handleResourceIAM so etag / member-validation / optimistic-
// concurrency behaviour matches every other GCP resource's IAM surface.
func crmIamVerb(w http.ResponseWriter, r *http.Request, store sim.Store[IAMPolicy], idAction, kind string) bool {
	id, action, found := strings.Cut(idAction, ":")
	if !found {
		return false
	}
	switch action {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, store, kind+"/"+id, action)
		return true
	}
	return false
}

// registerCRMv3 mounts the Cloud Resource Manager v3 resource-hierarchy and
// tagging surface plus the Cloud Resource Manager v1 projects surface
// (cloudresourcemanager.go) that gcloud's `projects` commands and
// terraform-provider-google's google_project resource speak. Every collection
// is a real CRUD store; mutating ops that the API models as long-running
// return a settled Operation, and IAM verbs reuse the shared policy store.
func registerCRMv3(srv *sim.Server, projectPolicies, resourcePolicies sim.Store[IAMPolicy]) {
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}
	projects := sim.MakeStore[CRMProject](srv.DB(), "crm_projects")
	crmProjects = projects
	crmEnsureDefaultProject()
	crmOrganizations = sim.MakeStore[CRMOrganization](srv.DB(), "crm_organizations")
	crmEnsureDefaultOrganization()
	folders := sim.MakeStore[CRMFolder](srv.DB(), "crm_folders")
	crmFolders = folders
	crmLiens = sim.MakeStore[CRMLien](srv.DB(), "crm_liens")
	crmOrgPolicies = sim.MakeStore[CRMOrgPolicyRow](srv.DB(), "crm_org_policies")
	registerCloudResourceManagerV1(srv, projectPolicies, resourcePolicies)
	registerCloudBilling(srv, resourcePolicies)
	registerCloudResourceManagerV2(srv, resourcePolicies)
	registerCRMCapabilityConfigs(srv)
	registerCRMBoundaries(srv)
	crmTagKeys = sim.MakeStore[CRMTagKey](srv.DB(), "crm_tag_keys")
	crmTagValues = sim.MakeStore[CRMTagValue](srv.DB(), "crm_tag_values")
	crmTagBindings = sim.MakeStore[CRMTagBinding](srv.DB(), "crm_tag_bindings")
	tagKeys, tagValues, tagBindings := crmTagKeys, crmTagValues, crmTagBindings
	tagHolds := sim.MakeStore[CRMTagHold](srv.DB(), "crm_tag_holds")
	folderCapabilities := sim.MakeStore[CRMCapability](srv.DB(), "crm_folder_capabilities")
	tagBindingCollections := sim.MakeStore[CRMTagBindingCollection](srv.DB(), "crm_tag_binding_collections")

	registerCRMProjects(srv, projectPolicies, projects)
	registerCRMFolders(srv, folders, resourcePolicies)
	registerCRMTags(srv, resourcePolicies, tagBindings, tagHolds, tagKeys, tagValues)
	registerCRMCapabilitiesAndCollections(srv, folderCapabilities, tagBindingCollections, tagValues)
	// The v3 operations.get read lives in cloudresourcemanager.go
	// (crmGetOperation) — every CRM LRO is persisted, so a poll returns
	// the stored record and an unknown name is a real NOT_FOUND.
}

// crmNamespaced renders a tagKey/tagValue namespaced name from its parent and
// short name (e.g. parent "organizations/123" + shortName "env" → "123/env").
func crmNamespaced(parent, shortName string) string {
	if parent == "" || shortName == "" {
		return ""
	}
	id := parent
	if i := strings.LastIndex(parent, "/"); i >= 0 {
		id = parent[i+1:]
	}
	return id + "/" + shortName
}

// registerCRMProjects mounts the v3 projects collection, the v1 project read
// and resource semantics.
func registerCRMProjects(srv *sim.Server, projectPolicies sim.Store[IAMPolicy], projects sim.Store[CRMProject]) {
	// Resource semantics are optional cloud metadata. Resources with no
	// assigned semantics return the requested full resource name and an empty
	// map; this simulator currently exposes no API that assigns semantics.
	srv.HandleFunc("GET /v3:fetchResourceSemantics", func(w http.ResponseWriter, r *http.Request) {
		fullName := r.URL.Query().Get("fullResourceName")
		trimmed := strings.TrimPrefix(fullName, "//")
		service, resourcePath, found := strings.Cut(trimmed, "/")
		if fullName == "" || !strings.HasPrefix(fullName, "//") || !found || service == "" || resourcePath == "" {
			GCPError(w, http.StatusBadRequest, "fullResourceName must be a full Google Cloud resource name", "INVALID_ARGUMENT")
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"fullResourceName": fullName,
			"semantics":        map[string]string{},
		})
	})

	// gcloud projects describe and google_project's terraform Read speak
	// the v1 read. A project the sim has never seen is a real 403 — the
	// API never discloses whether an inaccessible project exists.
	srv.HandleFunc("GET /v1/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		project, action, isCustomMethod := gcpCustomMethod(sim.PathParam(r, "project"))
		if isCustomMethod {
			switch action {
			case "showEffectiveAutokeyConfig":
				kmsHandleShowEffectiveAutokeyConfig(w, "projects/"+project)
				return
			case "showEffectiveKeyAccessJustificationsPolicyConfig":
				kmsHandleShowEffectiveKajPolicyConfig(w, project)
				return
			case "showEffectiveKeyAccessJustificationsEnrollmentConfig":
				kmsHandleShowEffectiveKajEnrollmentConfig(w, project)
				return
			}
			gcpMethodNotFound(w)
			return
		}
		p, ok := crmResolveProject(project)
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, crmV1Project(p))
	})

	srv.HandleFunc("GET /v3/projects:search", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		rows := projects.Filter(func(p CRMProject) bool { return crmSearchMatch(p, query) })
		sort.Slice(rows, func(i, j int) bool { return rows[i].ProjectId < rows[j].ProjectId })
		page, next, ok := paginateList(w, r, rows, gcpAsc(func(v CRMProject) string { return v.ProjectId }))
		if !ok {
			return
		}
		resp := map[string]any{"projects": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v3/projects", func(w http.ResponseWriter, r *http.Request) {
		// v3 ListProjects requires a parent and returns only ACTIVE
		// projects unless showDeleted is set; project-wide discovery is
		// SearchProjects' job.
		parent := r.URL.Query().Get("parent")
		if parent == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Request contains an invalid argument.")
			return
		}
		showDeleted := r.URL.Query().Get("showDeleted") == "true"
		rows := projects.Filter(func(p CRMProject) bool {
			return p.Parent == parent && (showDeleted || p.State == "ACTIVE")
		})
		sort.Slice(rows, func(i, j int) bool { return rows[i].ProjectId < rows[j].ProjectId })
		page, next, ok := paginateList(w, r, rows, gcpAsc(func(v CRMProject) string { return v.ProjectId }))
		if !ok {
			return
		}
		resp := map[string]any{"projects": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("POST /v3/projects", func(w http.ResponseWriter, r *http.Request) {
		var req CRMProject
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if err := crmValidateProjectID(req.ProjectId); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		if _, exists := projects.Get(req.ProjectId); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Requested entity already exists")
			return
		}
		p := CRMProject{
			Name:        "projects/" + gcpNumericID(12),
			Parent:      req.Parent,
			ProjectId:   req.ProjectId,
			State:       "ACTIVE",
			DisplayName: req.DisplayName,
			Labels:      req.Labels,
			CreateTime:  nowTimestamp(),
			UpdateTime:  nowTimestamp(),
			Etag:        crmEtag(),
		}
		projects.Put(p.ProjectId, p)
		iamEnsureComputeDefaultServiceAccount(p)
		crmGrantCreatorOwner(p, gcpRequestPrincipal(r))
		// The project is gettable and ready when the create returns, since the
		// operation completes inside the request.
		sim.WriteJSON(w, http.StatusOK, crmLROWithMetadata(p, typeProject, map[string]any{
			"@type": crmMetaCreateProject, "createTime": p.CreateTime, "gettable": true, "ready": true,
		}))
	})

	srv.HandleFunc("GET /v3/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, p)
	})

	srv.HandleFunc("PATCH /v3/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		var req CRMProject
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		mask := r.URL.Query().Get("updateMask")
		applyMask := func(field string) bool { return mask == "" || strings.Contains(mask, field) }
		if applyMask("displayName") {
			p.DisplayName = req.DisplayName
		}
		if applyMask("labels") {
			p.Labels = req.Labels
		}
		p.UpdateTime = nowTimestamp()
		p.Etag = crmEtag()
		projects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaUpdateProject))
	})

	srv.HandleFunc("DELETE /v3/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		// Real DeleteProject soft-deletes: the project enters
		// DELETE_REQUESTED (30-day pending deletion) and only an ACTIVE
		// project may be deleted.
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
		projects.Put(p.ProjectId, p)
		sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaDeleteProject))
	})

	// projects colon-verbs: move / undelete / IAM triple.
	srv.HandleFunc("POST /v3/projects/{projectAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "projectAction")
		id, action, found := gcpCustomMethod(idAction)
		// Resolve the method before the project, the way Google's frontend
		// does, so a method this simulator does not serve is answered as an
		// unrouted method rather than with the project's 403 — which would
		// claim the project is inaccessible when the method simply is not
		// mounted here.
		if !found || !crmV3ProjectPOSTMethods[action] {
			gcpMethodNotFound(w)
			return
		}
		p, ok := crmResolveProject(id)
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		// Normalize a project-number reference to the project ID before
		// the IAM verbs so both spellings address one policy.
		if crmIamVerb(w, r, projectPolicies, p.ProjectId+":"+action, "project") {
			return
		}
		switch action {
		case "move":
			var req struct {
				DestinationParent string `json:"destinationParent"`
			}
			_ = sim.ReadJSON(r, &req)
			p.Parent = req.DestinationParent
			p.UpdateTime = nowTimestamp()
			projects.Put(p.ProjectId, p)
			sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaMoveProject))
		case "undelete":
			if p.State != "DELETE_REQUESTED" {
				GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "Project \"%s\" has lifecycle state %s; undelete requires DELETE_REQUESTED", p.ProjectId, p.State)
				return
			}
			p.State = "ACTIVE"
			p.DeleteTime = ""
			p.UpdateTime = nowTimestamp()
			projects.Put(p.ProjectId, p)
			sim.WriteJSON(w, http.StatusOK, crmLRO(p, typeProject, crmMetaUndeleteProject))
		default:
			// crmIamVerb served the IAM triple, so move and undelete above are
			// the only methods crmV3ProjectPOSTMethods still admits here.
			gcpMethodNotFound(w)
		}
	})
}

// registerCRMFolders mounts folders, organizations and liens.
func registerCRMFolders(srv *sim.Server, folders sim.Store[CRMFolder], resourcePolicies sim.Store[IAMPolicy]) {
	srv.HandleFunc("GET /v3/folders:search", func(w http.ResponseWriter, r *http.Request) {
		rows := folders.Filter(func(CRMFolder) bool { return true })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows, gcpAsc(func(v CRMFolder) string { return v.Name }))
		if !ok {
			return
		}
		resp := map[string]any{"folders": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v3/folders", func(w http.ResponseWriter, r *http.Request) {
		crmWriteChildren(w, r, folders, "folders", func(v CRMFolder) (string, string) { return v.Parent, v.Name })
	})

	srv.HandleFunc("POST /v3/folders", func(w http.ResponseWriter, r *http.Request) {
		var req CRMFolder
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		f := CRMFolder{
			Name:        "folders/" + gcpNumericID(12),
			Parent:      req.Parent,
			DisplayName: req.DisplayName,
			State:       "ACTIVE",
			CreateTime:  nowTimestamp(),
			UpdateTime:  nowTimestamp(),
			Etag:        crmEtag(),
		}
		folders.Put(f.Name, f)
		sim.WriteJSON(w, http.StatusOK, crmLROWithMetadata(f, typeFolder, map[string]any{
			"@type": crmMetaCreateFolder, "displayName": f.DisplayName, "parent": f.Parent,
		}))
	})

	srv.HandleFunc("GET /v3/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		f, ok := folders.Get("folders/" + sim.PathParam(r, "folder"))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("PATCH /v3/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		name := "folders/" + sim.PathParam(r, "folder")
		f, ok := folders.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return
		}
		var req CRMFolder
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.DisplayName != "" {
			f.DisplayName = req.DisplayName
		}
		f.UpdateTime = nowTimestamp()
		f.Etag = crmEtag()
		folders.Put(name, f)
		sim.WriteJSON(w, http.StatusOK, crmLRO(f, typeFolder, crmMetaUpdateFolder))
	})

	srv.HandleFunc("DELETE /v3/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		name := "folders/" + sim.PathParam(r, "folder")
		f, ok := folders.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return
		}
		f.State = "DELETE_REQUESTED"
		f.UpdateTime = nowTimestamp()
		folders.Put(name, f)
		sim.WriteJSON(w, http.StatusOK, crmLRO(f, typeFolder, crmMetaDeleteFolder))
	})

	srv.HandleFunc("POST /v3/folders/{folderAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "folderAction")
		if crmIamVerb(w, r, resourcePolicies, idAction, "folder") {
			return
		}
		id, action, _ := strings.Cut(idAction, ":")
		name := "folders/" + id
		f, ok := folders.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
			return
		}
		switch action {
		case "move":
			var req struct {
				DestinationParent string `json:"destinationParent"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			sourceParent := f.Parent
			f.Parent = req.DestinationParent
			f.UpdateTime = nowTimestamp()
			folders.Put(name, f)
			sim.WriteJSON(w, http.StatusOK, crmLROWithMetadata(f, typeFolder, map[string]any{
				"@type": crmMetaMoveFolder, "displayName": f.DisplayName,
				"sourceParent": sourceParent, "destinationParent": f.Parent,
			}))
		case "undelete":
			f.State = "ACTIVE"
			f.UpdateTime = nowTimestamp()
			folders.Put(name, f)
			sim.WriteJSON(w, http.StatusOK, crmLRO(f, typeFolder, crmMetaUndeleteFolder))
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported folder action %q", action)
		}
	})

	// The organization store is shared with the v1 reads; an organization the
	// caller cannot see is a 403, never a 404.
	srv.HandleFunc("GET /v3/organizations:search", func(w http.ResponseWriter, r *http.Request) {
		rows := crmOrganizations.Filter(func(o CRMOrganization) bool {
			return crmOrganizationMatch(o, r.URL.Query().Get("query"))
		})
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows, gcpAsc(func(v CRMOrganization) string { return v.Name }))
		if !ok {
			return
		}
		resp := map[string]any{"organizations": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v3/organizations/{org}", func(w http.ResponseWriter, r *http.Request) {
		org, ok := crmOrganizations.Get("organizations/" + sim.PathParam(r, "org"))
		if !ok {
			crmOrganizationPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, org)
	})

	srv.HandleFunc("POST /v3/organizations/{orgAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "orgAction")
		if crmIamVerb(w, r, resourcePolicies, idAction, "organization") {
			return
		}
		gcpMethodNotFound(w)
	})

	// One lien collection under two spellings: the handlers are shared with
	// the v1 routes in cloudresourcemanager.go.
	srv.HandleFunc("POST /v3/liens", crmCreateLien)

	srv.HandleFunc("GET /v3/liens", crmListLiens)

	srv.HandleFunc("GET /v3/liens/{lien}", crmGetLien)

	srv.HandleFunc("DELETE /v3/liens/{lien}", crmDeleteLien)
}

const (
	typeProject  = "type.googleapis.com/google.cloud.resourcemanager.v3.Project"
	typeFolder   = "type.googleapis.com/google.cloud.resourcemanager.v3.Folder"
	typeTagKey   = "type.googleapis.com/google.cloud.resourcemanager.v3.TagKey"
	typeTagValue = "type.googleapis.com/google.cloud.resourcemanager.v3.TagValue"
	typeTagBind  = "type.googleapis.com/google.cloud.resourcemanager.v3.TagBinding"
	typeTagHold  = "type.googleapis.com/google.cloud.resourcemanager.v3.TagHold"
	typeEmpty    = "type.googleapis.com/google.protobuf.Empty"
)
