package main

import (
	"net/http"
	"sort"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Routes take a single-segment {object} so they beat the `{object...}`
// catch-all serving objects.get, which otherwise swallows `/o/{object}/acl`
// and answers "object \"<name>/acl\" not found". Object names arrive
// percent-encoded, so a name holding "/" is still one segment.

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

// An object's ACL is never empty in the real service, and that matters:
// `gcloud storage objects update --remove-acl-grant` omits the acl member
// entirely when its computed list comes out empty, so a lone grant would
// otherwise be unremovable.
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

// Copy at creation, not on read: later edits to the bucket default must not
// reach objects already written.
func gcsSeedObjectACL(bucket, object, generation string) {
	defaults := gcsObjectDefACLs.Filter(func(a GCSObjectACL) bool { return a.Bucket == bucket })
	for _, entry := range defaults {
		acl := entry
		acl.Object = object
		acl.Generation = generation
		acl.ID = bucket + "/" + object + "/" + generation + "/" + entry.Entity
		acl.SelfLink = ""
		gcsObjectACLs.Put(gcsObjectACLKey(bucket, object, entry.Entity), acl)
	}
}

// The objects.patch door onto the collection's state. The resource carries
// the whole ACL, so naming it replaces the set rather than merging into it.
func gcsReplaceObjectACL(bucket, object, generation string, entries []GCSObjectACL) {
	gcsDropObjectACL(bucket, object)
	for _, entry := range entries {
		if entry.Entity == "" || entry.Role == "" {
			continue
		}
		email, team := gcsACLEmailFor(entry.Entity)
		gcsObjectACLs.Put(gcsObjectACLKey(bucket, object, entry.Entity), GCSObjectACL{
			Kind:        "storage#objectAccessControl",
			ID:          bucket + "/" + object + "/" + generation + "/" + entry.Entity,
			Bucket:      bucket,
			Object:      object,
			Generation:  generation,
			Entity:      entry.Entity,
			Role:        entry.Role,
			Email:       email,
			ProjectTeam: team,
			Etag:        "CAE=",
		})
	}
}

func gcsObjectACLEntries(bucket, object string) []GCSObjectACL {
	items := gcsObjectACLs.Filter(func(a GCSObjectACL) bool {
		return a.Bucket == bucket && a.Object == object
	})
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

func registerGCSObjectACLs(srv *sim.Server, buckets sim.Store[Bucket], objects sim.Store[GCSObject]) {
	resolve := func(w http.ResponseWriter, r *http.Request) (bucket, object string, obj GCSObject, ok bool) {
		bucket, object = sim.PathParam(r, "bucket"), sim.PathParam(r, "object")
		b, found := buckets.Get(bucket)
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return "", "", GCSObject{}, false
		}
		if gcsUniformBucketLevelAccess(b) {
			GCPError(w, http.StatusBadRequest,
				"Cannot get legacy ACL for an object when uniform bucket-level access is enabled. "+
					"Read the object's IAM policy instead.", "INVALID_ARGUMENT")
			return "", "", GCSObject{}, false
		}
		obj, found = objects.Get(bucket + "/" + object)
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", object, bucket)
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
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Entity == "" || in.Role == "" {
			GCPError(w, http.StatusBadRequest, "entity and role are required", "INVALID_ARGUMENT")
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
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no ACL entry for entity %q on object %q in bucket %q", entity, object, bucket)
			return
		}
		var in GCSObjectACL
		if err := sim.ReadJSON(r, &in); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Role == "" {
			GCPError(w, http.StatusBadRequest, "role is required", "INVALID_ARGUMENT")
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
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no ACL entry for entity %q on object %q in bucket %q", entity, object, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
