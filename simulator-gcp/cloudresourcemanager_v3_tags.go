package main

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerCRMTags mounts tag keys, tag values, tag holds, tag bindings and
// effective tags.
func registerCRMTags(srv *sim.Server, resourcePolicies sim.Store[IAMPolicy], tagBindings sim.Store[CRMTagBinding], tagHolds sim.Store[CRMTagHold], tagKeys sim.Store[CRMTagKey], tagValues sim.Store[CRMTagValue]) {
	srv.HandleFunc("GET /v3/tagKeys/namespaced", func(w http.ResponseWriter, r *http.Request) {
		ns := r.URL.Query().Get("name")
		for _, k := range tagKeys.Filter(func(CRMTagKey) bool { return true }) {
			if k.NamespacedName == ns {
				sim.WriteJSON(w, http.StatusOK, k)
				return
			}
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag key %q not found", ns)
	})

	srv.HandleFunc("GET /v3/tagKeys", func(w http.ResponseWriter, r *http.Request) {
		crmWriteChildren(w, r, tagKeys, "tagKeys", func(v CRMTagKey) (string, string) { return v.Parent, v.Name })
	})

	srv.HandleFunc("POST /v3/tagKeys", func(w http.ResponseWriter, r *http.Request) {
		var req CRMTagKey
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		k := CRMTagKey{
			Name:               "tagKeys/" + gcpNumericID(18),
			Parent:             req.Parent,
			ShortName:          req.ShortName,
			NamespacedName:     crmNamespaced(req.Parent, req.ShortName),
			Description:        req.Description,
			Purpose:            req.Purpose,
			AllowedValuesRegex: req.AllowedValuesRegex,
			CreateTime:         nowTimestamp(),
			UpdateTime:         nowTimestamp(),
			Etag:               crmEtag(),
		}
		tagKeys.Put(k.Name, k)
		sim.WriteJSON(w, http.StatusOK, crmLRO(k, typeTagKey, crmMetaCreateTagKey))
	})

	srv.HandleFunc("GET /v3/tagKeys/{key}", func(w http.ResponseWriter, r *http.Request) {
		k, ok := tagKeys.Get("tagKeys/" + sim.PathParam(r, "key"))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag key not found")
			return
		}
		sim.WriteJSON(w, http.StatusOK, k)
	})

	srv.HandleFunc("PATCH /v3/tagKeys/{key}", func(w http.ResponseWriter, r *http.Request) {
		crmPatchTagResource(w, r, tagKeys, "tagKeys/"+sim.PathParam(r, "key"), "tag key", typeTagKey, crmMetaUpdateTagKey,
			func(stored *CRMTagKey, req CRMTagKey, description bool, now, etag string) {
				if description {
					stored.Description = req.Description
				}
				stored.UpdateTime, stored.Etag = now, etag
			})
	})

	srv.HandleFunc("DELETE /v3/tagKeys/{key}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagKeys/" + sim.PathParam(r, "key")
		k, ok := tagKeys.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag key not found")
			return
		}
		tagKeys.Delete(name)
		sim.WriteJSON(w, http.StatusOK, crmLRO(k, typeTagKey, crmMetaDeleteTagKey))
	})

	srv.HandleFunc("POST /v3/tagKeys/{keyAction}", func(w http.ResponseWriter, r *http.Request) {
		if crmIamVerb(w, r, resourcePolicies, sim.PathParam(r, "keyAction"), "tagKey") {
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported tagKey action")
	})

	srv.HandleFunc("GET /v3/tagValues/namespaced", func(w http.ResponseWriter, r *http.Request) {
		ns := r.URL.Query().Get("name")
		for _, v := range tagValues.Filter(func(CRMTagValue) bool { return true }) {
			if v.NamespacedName == ns {
				sim.WriteJSON(w, http.StatusOK, v)
				return
			}
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag value %q not found", ns)
	})

	srv.HandleFunc("GET /v3/tagValues", func(w http.ResponseWriter, r *http.Request) {
		crmWriteChildren(w, r, tagValues, "tagValues", func(v CRMTagValue) (string, string) { return v.Parent, v.Name })
	})

	srv.HandleFunc("POST /v3/tagValues", func(w http.ResponseWriter, r *http.Request) {
		var req CRMTagValue
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// A value's namespaced name is its key's followed by the value's
		// short name: 123456789012/env/prod.
		namespaced := crmNamespaced(req.Parent, req.ShortName)
		if key, ok := tagKeys.Get(req.Parent); ok && key.NamespacedName != "" {
			namespaced = key.NamespacedName + "/" + req.ShortName
		}
		v := CRMTagValue{
			Name:           "tagValues/" + gcpNumericID(18),
			Parent:         req.Parent,
			ShortName:      req.ShortName,
			NamespacedName: namespaced,
			Description:    req.Description,
			CreateTime:     nowTimestamp(),
			UpdateTime:     nowTimestamp(),
			Etag:           crmEtag(),
		}
		tagValues.Put(v.Name, v)
		sim.WriteJSON(w, http.StatusOK, crmLRO(v, typeTagValue, crmMetaCreateTagValue))
	})

	srv.HandleFunc("GET /v3/tagValues/{val}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := tagValues.Get("tagValues/" + sim.PathParam(r, "val"))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag value not found")
			return
		}
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("PATCH /v3/tagValues/{val}", func(w http.ResponseWriter, r *http.Request) {
		crmPatchTagResource(w, r, tagValues, "tagValues/"+sim.PathParam(r, "val"), "tag value", typeTagValue, crmMetaUpdateTagValue,
			func(stored *CRMTagValue, req CRMTagValue, description bool, now, etag string) {
				if description {
					stored.Description = req.Description
				}
				stored.UpdateTime, stored.Etag = now, etag
			})
	})

	srv.HandleFunc("DELETE /v3/tagValues/{val}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagValues/" + sim.PathParam(r, "val")
		v, ok := tagValues.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag value not found")
			return
		}
		tagValues.Delete(name)
		sim.WriteJSON(w, http.StatusOK, crmLRO(v, typeTagValue, crmMetaDeleteTagValue))
	})

	// tagValues tagHolds list/create live under tagValues/{val}/tagHolds.
	srv.HandleFunc("GET /v3/tagValues/{val}/tagHolds", func(w http.ResponseWriter, r *http.Request) {
		prefix := "tagValues/" + sim.PathParam(r, "val") + "/tagHolds/"
		rows := tagHolds.Filter(func(h CRMTagHold) bool { return strings.HasPrefix(h.Name, prefix) })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		page, next, ok := paginateList(w, r, rows, gcpAsc(func(v CRMTagHold) string { return v.Name }))
		if !ok {
			return
		}
		resp := map[string]any{"tagHolds": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("POST /v3/tagValues/{val}/tagHolds", func(w http.ResponseWriter, r *http.Request) {
		var req CRMTagHold
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		h := CRMTagHold{
			Name:       "tagValues/" + sim.PathParam(r, "val") + "/tagHolds/" + gcpNumericID(16),
			Holder:     req.Holder,
			Origin:     req.Origin,
			HelpLink:   req.HelpLink,
			CreateTime: nowTimestamp(),
		}
		tagHolds.Put(h.Name, h)
		sim.WriteJSON(w, http.StatusOK, crmLRO(h, typeTagHold, crmMetaCreateTagHold))
	})

	srv.HandleFunc("DELETE /v3/tagValues/{val}/tagHolds/{hold}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagValues/" + sim.PathParam(r, "val") + "/tagHolds/" + sim.PathParam(r, "hold")
		if _, ok := tagHolds.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag hold not found")
			return
		}
		tagHolds.Delete(name)
		sim.WriteJSON(w, http.StatusOK, crmLRO(map[string]any{}, typeEmpty, crmMetaDeleteTagHold))
	})

	srv.HandleFunc("POST /v3/tagValues/{valAction}", func(w http.ResponseWriter, r *http.Request) {
		if crmIamVerb(w, r, resourcePolicies, sim.PathParam(r, "valAction"), "tagValue") {
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported tagValue action")
	})

	srv.HandleFunc("POST /v3/tagBindings", func(w http.ResponseWriter, r *http.Request) {
		var req CRMTagBinding
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		b := CRMTagBinding{
			Name:                   "tagBindings/" + gcpNumericID(16),
			Parent:                 req.Parent,
			TagValue:               req.TagValue,
			TagValueNamespacedName: req.TagValueNamespacedName,
		}
		tagBindings.Put(b.Name, b)
		sim.WriteJSON(w, http.StatusOK, crmLRO(b, typeTagBind, crmMetaCreateTagBinding))
	})

	srv.HandleFunc("GET /v3/tagBindings", func(w http.ResponseWriter, r *http.Request) {
		crmWriteChildren(w, r, tagBindings, "tagBindings", func(v CRMTagBinding) (string, string) { return v.Parent, v.Name })
	})

	srv.HandleFunc("DELETE /v3/tagBindings/{binding...}", func(w http.ResponseWriter, r *http.Request) {
		name := "tagBindings/" + sim.PathParam(r, "binding")
		tagBindings.Delete(name)
		sim.WriteJSON(w, http.StatusOK, crmLRO(map[string]any{}, typeEmpty, crmMetaDeleteTagBinding))
	})

	srv.HandleFunc("GET /v3/effectiveTags", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"effectiveTags": crmEffectiveTags(r.URL.Query().Get("parent")),
		})
	})
}

// registerCRMCapabilitiesAndCollections mounts folder capabilities and tag
// binding collections.
func registerCRMCapabilitiesAndCollections(srv *sim.Server, folderCapabilities sim.Store[CRMCapability], tagBindingCollections sim.Store[CRMTagBindingCollection], tagValues sim.Store[CRMTagValue]) {
	// A folder capability is a boolean feature toggle on the folder; the only
	// modeled one is the management capability. GET reads it, PATCH returns an
	// Operation (the API models the mutation as long-running).
	capabilityName := func(r *http.Request) string {
		return "folders/" + sim.PathParam(r, "folder") +
			"/capabilities/" + sim.PathParam(r, "capability")
	}

	srv.HandleFunc("GET /v3/folders/{folder}/capabilities/{capability}", func(w http.ResponseWriter, r *http.Request) {
		name := capabilityName(r)
		// Off is the default, and the value a PATCH set is what a read has to
		// report: a toggle whose read always says false is a toggle nothing
		// can turn on.
		row, set := folderCapabilities.Get(name)
		if !set {
			row = CRMCapability{Name: name}
		}
		sim.WriteJSON(w, http.StatusOK, row)
	})

	srv.HandleFunc("PATCH /v3/folders/{folder}/capabilities/{capability}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Value bool `json:"value"`
		}
		_ = sim.ReadJSON(r, &req)
		row := CRMCapability{Name: capabilityName(r), Value: req.Value}
		folderCapabilities.Put(row.Name, row)
		sim.WriteJSON(w, http.StatusOK, crmLRO(row, "type.googleapis.com/google.cloud.resourcemanager.v3.Capability", crmMetaUpdateCapability))
	})

	// A tagBindingCollection is the full set of direct tag bindings on a
	// resource, keyed by its full-resource-name and addressable in a location.
	// GET reads it; PATCH (the only mutator) returns an Operation.
	// A collection is addressed by the resource it belongs to, percent-encoded
	// into the last path segment, so the resource never has to be guessed: it
	// is decoded from the name the caller used. The tags a PATCH sets are the
	// tags a GET reports — a collection whose read is always empty is one
	// nothing can be written to.
	collectionResource := func(r *http.Request) string {
		decoded, err := url.PathUnescape(sim.PathParam(r, "collection"))
		if err != nil {
			return sim.PathParam(r, "collection")
		}
		return decoded
	}

	srv.HandleFunc("GET /v3/locations/{location}/tagBindingCollections/{collection}", func(w http.ResponseWriter, r *http.Request) {
		name := "locations/" + sim.PathParam(r, "location") +
			"/tagBindingCollections/" + sim.PathParam(r, "collection")
		row, set := tagBindingCollections.Get(name)
		if !set {
			row = CRMTagBindingCollection{
				Name:             name,
				FullResourceName: collectionResource(r),
				Etag:             crmEtag(),
			}
		}
		if row.Tags == nil {
			row.Tags = map[string]string{}
		}
		sim.WriteJSON(w, http.StatusOK, row)
	})

	srv.HandleFunc("PATCH /v3/locations/{location}/tagBindingCollections/{collection}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FullResourceName string            `json:"fullResourceName"`
			Tags             map[string]string `json:"tags"`
		}
		_ = sim.ReadJSON(r, &req)
		resource := req.FullResourceName
		if resource == "" {
			resource = collectionResource(r)
		}
		if req.Tags == nil {
			req.Tags = map[string]string{}
		}
		coll := CRMTagBindingCollection{
			Name: "locations/" + sim.PathParam(r, "location") +
				"/tagBindingCollections/" + sim.PathParam(r, "collection"),
			FullResourceName: resource,
			Tags:             req.Tags,
			Etag:             crmEtag(),
		}
		tagBindingCollections.Put(coll.Name, coll)
		sim.WriteJSON(w, http.StatusOK, crmLRO(coll, "type.googleapis.com/google.cloud.resourcemanager.v3.TagBindingCollection", crmMetaUpdateTagBindingCollection))
	})

	srv.HandleFunc("GET /v3/locations/{location}/effectiveTagBindingCollections/{collection}", func(w http.ResponseWriter, r *http.Request) {
		resource := collectionResource(r)
		// Effective tags are the ones in force: what the collection holds
		// directly, plus what a tagBindings create bound to the resource and
		// what it inherits from its ancestors.
		effective := map[string]string{}
		direct, _ := tagBindingCollections.Get("locations/" + sim.PathParam(r, "location") +
			"/tagBindingCollections/" + sim.PathParam(r, "collection"))
		for key, value := range direct.Tags {
			effective[key] = value
		}
		for _, tag := range crmEffectiveTags(resource) {
			value, ok := tagValues.Get(tag.TagValue)
			if tag.NamespacedTagKey == "" || !ok || value.ShortName == "" {
				continue
			}
			if _, bound := effective[tag.NamespacedTagKey]; bound && tag.Inherited {
				continue
			}
			effective[tag.NamespacedTagKey] = value.ShortName
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"name": "locations/" + sim.PathParam(r, "location") +
				"/effectiveTagBindingCollections/" + sim.PathParam(r, "collection"),
			"fullResourceName": resource,
			"effectiveTags":    effective,
		})
	})
}

// crmPatchTagResource answers the patch of the tag key or tag value of store
// called name: its description, when the update mask names it or names
// nothing, a new update time and etag, and a settled operation carrying it.
func crmPatchTagResource[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], name, noun, typeURL, metadata string,
	apply func(stored *T, req T, description bool, now, etag string)) {
	stored, ok := store.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", noun)
		return
	}
	var req T
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	mask := r.URL.Query().Get("updateMask")
	apply(&stored, req, mask == "" || strings.Contains(mask, "description"), nowTimestamp(), crmEtag())
	store.Put(name, stored)
	sim.WriteJSON(w, http.StatusOK, crmLRO(stored, typeURL, metadata))
}
