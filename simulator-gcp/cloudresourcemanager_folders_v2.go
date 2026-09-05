package main

// Cloud Resource Manager v2 folders surface.
//
// gcloud's `resource-manager folders` command group speaks v2 — the API
// version whose only collection is folders — while the v3 collection in
// iam.go serves cloud.google.com/go/resourcemanager/apiv3 and the console.
// Both address one folder store, so a folder created over either wire is
// visible on both; they differ in the wire shape (v2 spells the state
// `lifecycleState` and carries no etag) and in v2's operation metadata
// messages, which the v2 clients resolve against their own proto types.
//
// v2 folders.create returns a long-running Operation whose poll path is
// `v1/operations/{op}` — the v2 document says so — which crmGetOperation
// already serves.

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// crmV2FolderMsg mirrors the cloudresourcemanager#Folder (v2) resource.
type crmV2FolderMsg struct {
	Name           string `json:"name"`
	Parent         string `json:"parent,omitempty"`
	DisplayName    string `json:"displayName,omitempty"`
	LifecycleState string `json:"lifecycleState,omitempty"`
	CreateTime     string `json:"createTime,omitempty"`
}

// crmV2Folder renders the stored folder in the v2 wire shape.
func crmV2Folder(f CRMFolder) crmV2FolderMsg {
	return crmV2FolderMsg{
		Name:           f.Name,
		Parent:         f.Parent,
		DisplayName:    f.DisplayName,
		LifecycleState: f.State,
		CreateTime:     f.CreateTime,
	}
}

// Fully-qualified Any types of the two v2 folder verbs the document models as
// long-running — create and move. patch, delete and undelete return the Folder
// itself.
const (
	crmV2TypeFolder       = "type.googleapis.com/google.cloud.resourcemanager.v2.Folder"
	crmV2MetaCreateFolder = "type.googleapis.com/google.cloud.resourcemanager.v2.CreateFolderMetadata"
	crmV2MetaMoveFolder   = "type.googleapis.com/google.cloud.resourcemanager.v2.MoveFolderMetadata"
)

// crmV2FolderPOSTMethods are the POST custom methods v2 serves on a folder.
var crmV2FolderPOSTMethods = map[string]bool{
	"move":               true,
	"undelete":           true,
	"getIamPolicy":       true,
	"setIamPolicy":       true,
	"testIamPermissions": true,
}

// registerCloudResourceManagerV2 mounts the v2 folders collection over the
// shared folder store.
func registerCloudResourceManagerV2(srv *sim.Server, resourcePolicies sim.Store[IAMPolicy]) {
	srv.HandleFunc("POST /v2/folders", func(w http.ResponseWriter, r *http.Request) {
		var req crmV2FolderMsg
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// v2 takes the parent as a query parameter, not a body field.
		parent := r.URL.Query().Get("parent")
		if parent == "" {
			parent = req.Parent
		}
		if req.DisplayName == "" {
			GCPError(w, http.StatusBadRequest, "Folder display name is required.", "INVALID_ARGUMENT")
			return
		}
		f := CRMFolder{
			Name:        "folders/" + gcpNumericID(12),
			Parent:      parent,
			DisplayName: req.DisplayName,
			State:       "ACTIVE",
			CreateTime:  nowTimestamp(),
			UpdateTime:  nowTimestamp(),
			Etag:        crmEtag(),
		}
		crmFolders.Put(f.Name, f)
		sim.WriteJSON(w, http.StatusOK, crmLRO(crmV2Folder(f), crmV2TypeFolder, crmV2MetaCreateFolder))
	})

	srv.HandleFunc("GET /v2/folders", func(w http.ResponseWriter, r *http.Request) {
		parent := r.URL.Query().Get("parent")
		showDeleted := r.URL.Query().Get("showDeleted") == "true"
		rows := crmFolders.Filter(func(f CRMFolder) bool {
			if parent != "" && f.Parent != parent {
				return false
			}
			return showDeleted || f.State == "ACTIVE"
		})
		crmWriteV2FolderPage(w, r, rows)
	})

	srv.HandleFunc("POST /v2/folders:search", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string `json:"query"`
			PageSize  int    `json:"pageSize"`
			PageToken string `json:"pageToken"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		rows := crmFolders.Filter(func(f CRMFolder) bool { return crmV2FolderMatch(f, req.Query) })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		out := make([]crmV2FolderMsg, 0, len(rows))
		for _, f := range rows {
			out = append(out, crmV2Folder(f))
		}
		page, next, ok := crmOrgPolicyPage(w, out, req.PageSize, req.PageToken)
		if !ok {
			return
		}
		resp := map[string]any{"folders": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v2/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		f, ok := crmFolders.Get("folders/" + sim.PathParam(r, "folder"))
		if !ok {
			crmFolderNotFound(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, crmV2Folder(f))
	})

	srv.HandleFunc("PATCH /v2/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		name := "folders/" + sim.PathParam(r, "folder")
		f, ok := crmFolders.Get(name)
		if !ok {
			crmFolderNotFound(w)
			return
		}
		var req crmV2FolderMsg
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// displayName is the only field v2 documents as updatable.
		for _, path := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
			switch strings.TrimSpace(path) {
			case "", "displayName", "display_name":
			default:
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"Field %q is not updatable; only display_name can be updated.", strings.TrimSpace(path))
				return
			}
		}
		if req.DisplayName != "" {
			f.DisplayName = req.DisplayName
		}
		f.UpdateTime = nowTimestamp()
		f.Etag = crmEtag()
		crmFolders.Put(name, f)
		// v2 spells patch, delete and undelete as immediate writes returning
		// the Folder; only create and move are long-running.
		sim.WriteJSON(w, http.StatusOK, crmV2Folder(f))
	})

	srv.HandleFunc("DELETE /v2/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		name := "folders/" + sim.PathParam(r, "folder")
		f, ok := crmFolders.Get(name)
		if !ok {
			crmFolderNotFound(w)
			return
		}
		f.State = "DELETE_REQUESTED"
		f.UpdateTime = nowTimestamp()
		crmFolders.Put(name, f)
		sim.WriteJSON(w, http.StatusOK, crmV2Folder(f))
	})

	srv.HandleFunc("POST /v2/folders/{folderAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "folderAction")
		id, action, found := gcpCustomMethod(idAction)
		if !found || !crmV2FolderPOSTMethods[action] {
			gcpMethodNotFound(w)
			return
		}
		if crmIamVerb(w, r, resourcePolicies, idAction, "folder") {
			return
		}
		name := "folders/" + id
		f, ok := crmFolders.Get(name)
		if !ok {
			crmFolderNotFound(w)
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
			f.Parent = req.DestinationParent
			f.UpdateTime = nowTimestamp()
			crmFolders.Put(name, f)
			sim.WriteJSON(w, http.StatusOK, crmLRO(crmV2Folder(f), crmV2TypeFolder, crmV2MetaMoveFolder))
		case "undelete":
			f.State = "ACTIVE"
			f.UpdateTime = nowTimestamp()
			crmFolders.Put(name, f)
			sim.WriteJSON(w, http.StatusOK, crmV2Folder(f))
		default:
			gcpMethodNotFound(w)
		}
	})
}

// crmFolderNotFound writes the response Cloud Resource Manager returns for a
// folder the caller cannot read.
func crmFolderNotFound(w http.ResponseWriter) {
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder not found")
}

// crmWriteV2FolderPage paginates and writes a v2 folder collection.
func crmWriteV2FolderPage(w http.ResponseWriter, r *http.Request, rows []CRMFolder) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	page, next, ok := paginateList(w, r, rows)
	if !ok {
		return
	}
	out := make([]crmV2FolderMsg, 0, len(page))
	for _, f := range page {
		out = append(out, crmV2Folder(f))
	}
	resp := map[string]any{"folders": out}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// crmV2FolderMatch evaluates a SearchFolders query against a folder. The
// documented fields are displayName, parent and lifecycleState, joined by
// whitespace or AND; a bare term matches the display name. An empty query
// matches every folder the caller can see.
func crmV2FolderMatch(f CRMFolder, query string) bool {
	for _, term := range strings.Fields(query) {
		if strings.EqualFold(term, "AND") {
			continue
		}
		key, val, found := strings.Cut(term, ":")
		val = strings.Trim(val, `"`)
		switch {
		case !found:
			if !crmFieldMatch("*"+strings.Trim(term, `"`)+"*", f.DisplayName) {
				return false
			}
		case key == "displayName":
			if !crmFieldMatch(val, f.DisplayName) {
				return false
			}
		case key == "parent":
			if !crmFieldMatch(val, f.Parent) {
				return false
			}
		case key == "lifecycleState":
			if !crmFieldMatch(val, f.State) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
