package main

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Routes take a single-segment {object}, so they beat the `{object...}`
// catch-all serving objects.get. Object names arrive percent-encoded, so a
// name holding "/" is still one segment.

var gcsObjectACLs sim.Store[GCSObjectACL]

// Separate on a byte an object name cannot hold; names carry "/" freely.
func gcsObjectACLKey(bucket, object, entity string) string {
	return bucket + "\x00" + object + "\x00" + entity
}

func gcsUniformBucketLevelAccess(bucket Bucket) bool {
	iam, ok := bucket.Data["iamConfiguration"].(map[string]any)
	if !ok {
		return false
	}
	ubla, ok := iam["uniformBucketLevelAccess"].(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := ubla["enabled"].(bool)
	return enabled
}

// Cloud Storage never leaves an object's ACL empty, which matters: `gcloud
// storage objects update --remove-acl-grant` omits an empty acl member, so a
// lone grant would be unremovable.
func gcsProjectPrivateObjectACL(bucket Bucket) []GCSObjectACL {
	projectNumber, _ := bucket.Data["projectNumber"].(string)
	if projectNumber == "" {
		return nil
	}
	entries := make([]GCSObjectACL, 0, 3)
	for _, grant := range []struct{ team, role string }{
		{"owners", "OWNER"},
		{"editors", "OWNER"},
		{"viewers", "READER"},
	} {
		entity := "project-" + grant.team + "-" + projectNumber
		_, team := gcsACLEmailFor(entity)
		entries = append(entries, GCSObjectACL{
			Kind:        "storage#objectAccessControl",
			Entity:      entity,
			Role:        grant.role,
			ProjectTeam: team,
			Etag:        "CAE=",
		})
	}
	return entries
}

func gcsSeedDefaultObjectACL(name string, bucket Bucket) {
	if _, declared := bucket.Data["defaultObjectAcl"]; declared {
		return
	}
	for _, entry := range gcsProjectPrivateObjectACL(bucket) {
		entry.Bucket = name
		entry.ID = name + "/" + entry.Entity
		gcsObjectDefACLs.Put(name+"\x00"+entry.Entity, entry)
	}
}

type gcsObjectOwner struct {
	Entity string `json:"entity"`
}

// gcsCreateACL is what a write names for the new object's ACL: a predefined
// ACL (predefinedAcl, destinationPredefinedAcl) or the resource's own acl.
type gcsCreateACL struct {
	Predefined string         `json:"predefined,omitempty"`
	Entries    []GCSObjectACL `json:"entries,omitempty"`
}

// gcsUploader is the owner an authenticated write gives the object it
// creates.
func gcsUploader(r *http.Request) *gcsObjectOwner {
	principal := gcpRequestPrincipal(r)
	_, email, ok := strings.Cut(principal, ":")
	if !ok || email == "" {
		return nil
	}
	return &gcsObjectOwner{Entity: "user-" + email}
}

// gcsWriteCreateACL reads the ACL a write asks for from the predefined-ACL
// query parameter its method takes and the resource it carries.
func gcsWriteCreateACL(r *http.Request, param string, entries *[]GCSObjectACL) (*gcsCreateACL, error) {
	predefined := r.URL.Query().Get(param)
	if predefined != "" {
		if _, known := gcsPredefinedObjectACLs[predefined]; !known {
			return nil, gcpInvalidValue(fmt.Sprintf("Invalid value for %s: %s", param, predefined))
		}
		return &gcsCreateACL{Predefined: predefined}, nil
	}
	if entries != nil {
		return &gcsCreateACL{Entries: *entries}, nil
	}
	return nil, nil
}

// gcsPredefinedObjectACLs are the grants each predefined object ACL adds to
// the owner's OWNER, as storage-v1.discovery.json.gz's predefinedAcl
// enumDescriptions and storage-access-control-lists.md.gz state them; a
// "project-<team>" entity stands for the bucket's project team.
var gcsPredefinedObjectACLs = map[string][][2]string{
	"private":                {},
	"projectPrivate":         {{"project-owners", "OWNER"}, {"project-editors", "OWNER"}, {"project-viewers", "READER"}},
	"authenticatedRead":      {{"allAuthenticatedUsers", "READER"}},
	"publicRead":             {{"allUsers", "READER"}},
	"bucketOwnerFullControl": {{"project-owners", "OWNER"}},
	"bucketOwnerRead":        {{"project-owners", "READER"}},
}

// gcsSeedObjectACL gives a new object its ACL at creation: the predefined or
// explicit ACL the write named, else the bucket's default object ACL, with
// the owner always holding OWNER. Copy at creation, not on read: later edits
// to the bucket default must not reach objects already written.
func gcsSeedObjectACL(bucket, object, generation string, owner *gcsObjectOwner, create *gcsCreateACL) {
	b, _ := gcsBuckets.Get(bucket)
	projectNumber, _ := b.Data["projectNumber"].(string)
	ownerEntity := "project-owners-" + projectNumber
	if owner != nil {
		ownerEntity = owner.Entity
	}
	grants := map[string]string{}
	switch {
	case create != nil && create.Predefined != "":
		for _, grant := range gcsPredefinedObjectACLs[create.Predefined] {
			entity := grant[0]
			if strings.HasPrefix(entity, "project-") {
				entity += "-" + projectNumber
			}
			grants[entity] = grant[1]
		}
	case create != nil:
		for _, entry := range create.Entries {
			if entry.Entity != "" && entry.Role != "" {
				grants[entry.Entity] = entry.Role
			}
		}
	default:
		for _, entry := range gcsObjectDefACLs.Filter(func(a GCSObjectACL) bool { return a.Bucket == bucket }) {
			grants[entry.Entity] = entry.Role
		}
	}
	grants[ownerEntity] = "OWNER"
	for entity, role := range grants {
		email, team := gcsACLEmailFor(entity)
		gcsObjectACLs.Put(gcsObjectACLKey(bucket, object, entity), GCSObjectACL{
			Kind:        "storage#objectAccessControl",
			ID:          bucket + "/" + object + "/" + generation + "/" + entity,
			Bucket:      bucket,
			Object:      object,
			Generation:  generation,
			Entity:      entity,
			Role:        role,
			Email:       email,
			ProjectTeam: team,
			Etag:        "CAE=",
		})
	}
}

var gcsObjectACLsByObject sim.GenerationIndex[GCSObjectACL]

func gcsObjectACLEntries(bucket, object string) []GCSObjectACL {
	items := slices.Clone(gcsObjectACLsByObject.LookupAll(gcsObjectACLs, bucket+"/"+object,
		func(a GCSObjectACL) []string { return []string{a.Bucket + "/" + a.Object} }))
	sort.Slice(items, func(i, j int) bool { return items[i].Entity < items[j].Entity })
	return items
}

// Drop on delete so a name written again inherits no earlier grants.
func gcsDropObjectACL(bucket, object string) {
	for _, entry := range gcsObjectACLs.Filter(func(a GCSObjectACL) bool {
		return a.Bucket == bucket && a.Object == object
	}) {
		gcsObjectACLs.Delete(gcsObjectACLKey(bucket, object, entry.Entity))
	}
}

func registerGCSObjectACLs(srv *sim.Server, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject]) {
	resolve := func(w http.ResponseWriter, r *http.Request) (bucket, object string, obj GCSObject, ok bool) {
		bucket, object = sim.PathParam(r, "bucket"), sim.PathParam(r, "object")
		b, found := buckets.Get(bucket)
		if !found {
			gcsErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return "", "", GCSObject{}, false
		}
		if gcsUniformBucketLevelAccess(b) {
			gcsError(w, http.StatusBadRequest,
				"Cannot get legacy ACL for an object when uniform bucket-level access is enabled. "+
					"Read the object's IAM policy instead.", "INVALID_ARGUMENT")
			return "", "", GCSObject{}, false
		}
		obj, found = objects.Get(bucket + "/" + object)
		if !found {
			gcsErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", object, bucket)
			return "", "", GCSObject{}, false
		}
		return bucket, object, obj, true
	}

	build := func(r *http.Request, bucket, object, generation, entity, role string) GCSObjectACL {
		email, team := gcsACLEmailFor(entity)
		return GCSObjectACL{
			Kind:        "storage#objectAccessControl",
			ID:          bucket + "/" + object + "/" + generation + "/" + entity,
			SelfLink:    gcpSelfLink(r, "/storage/v1/b/"+bucket+"/o/"+object+"/acl/"+entity),
			Bucket:      bucket,
			Object:      object,
			Generation:  generation,
			Entity:      entity,
			Role:        role,
			Email:       email,
			ProjectTeam: team,
			Etag:        "CAE=",
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/o/{object}/acl", func(w http.ResponseWriter, r *http.Request) {
		bucket, object, _, ok := resolve(w, r)
		if !ok {
			return
		}
		items := gcsObjectACLs.Filter(func(a GCSObjectACL) bool {
			return a.Bucket == bucket && a.Object == object
		})
		sort.Slice(items, func(i, j int) bool { return items[i].Entity < items[j].Entity })
		if items == nil {
			items = []GCSObjectACL{}
		}
		for i := range items {
			items[i].SelfLink = gcpSelfLink(r, "/storage/v1/b/"+bucket+"/o/"+object+"/acl/"+items[i].Entity)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#objectAccessControls", "items": items})
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/o/{object}/acl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, object, _, ok := resolve(w, r)
		if !ok {
			return
		}
		entity := sim.PathParam(r, "entity")
		acl, found := gcsObjectACLs.Get(gcsObjectACLKey(bucket, object, entity))
		if !found {
			gcsErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no ACL entry for entity %q on object %q in bucket %q", entity, object, bucket)
			return
		}
		acl.SelfLink = gcpSelfLink(r, "/storage/v1/b/"+bucket+"/o/"+object+"/acl/"+entity)
		sim.WriteJSON(w, http.StatusOK, acl)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/o/{object}/acl", func(w http.ResponseWriter, r *http.Request) {
		bucket, object, obj, ok := resolve(w, r)
		if !ok {
			return
		}
		var in GCSObjectACL
		if err := sim.ReadJSON(r, &in); err != nil {
			gcsErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Entity == "" || in.Role == "" {
			gcsRequired(w, "entity and role are required")
			return
		}
		acl := build(r, bucket, object, obj.Generation, in.Entity, in.Role)
		gcsObjectACLs.Put(gcsObjectACLKey(bucket, object, in.Entity), acl)
		sim.WriteJSON(w, http.StatusOK, acl)
	})

	update := func(w http.ResponseWriter, r *http.Request) {
		bucket, object, obj, ok := resolve(w, r)
		if !ok {
			return
		}
		entity := sim.PathParam(r, "entity")
		if _, found := gcsObjectACLs.Get(gcsObjectACLKey(bucket, object, entity)); !found {
			gcsErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no ACL entry for entity %q on object %q in bucket %q", entity, object, bucket)
			return
		}
		var in GCSObjectACL
		if err := sim.ReadJSON(r, &in); err != nil {
			gcsErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Role == "" {
			gcsRequired(w, "role is required")
			return
		}
		acl := build(r, bucket, object, obj.Generation, entity, in.Role)
		gcsObjectACLs.Put(gcsObjectACLKey(bucket, object, entity), acl)
		sim.WriteJSON(w, http.StatusOK, acl)
	}
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/o/{object}/acl/{entity}", update)
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/o/{object}/acl/{entity}", update)

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/o/{object}/acl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, object, _, ok := resolve(w, r)
		if !ok {
			return
		}
		entity := sim.PathParam(r, "entity")
		if !gcsObjectACLs.Delete(gcsObjectACLKey(bucket, object, entity)) {
			gcsErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no ACL entry for entity %q on object %q in bucket %q", entity, object, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
