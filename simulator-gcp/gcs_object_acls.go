package main

import (
	"net/http"
	"sort"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Per-object access controls (storage#objectAccessControl).
//
// The bucket-level and default-object surfaces were already served; the
// per-object one was not, and it did not present as missing. `/o/{object}/acl`
// matched the `{object...}` catch-all that serves objects.get, so a request
// for an object's ACLs was answered "object \"<name>/acl\" not found" — a
// plausible 404 for a request the simulator never understood. The route
// coverage probe reads any handler answer as served, so all five reads and
// writes counted as covered while no handler for them existed.
//
// The routes below take a single-segment {object}, which beats the catch-all
// in the mux and matches the real service's contract: an object name is
// percent-encoded in the path, so a name containing "/" arrives as one
// segment. A name sent unencoded still reaches objects.get, exactly as it
// does against Google Cloud Storage.

var gcsObjectACLs sim.Store[GCSObjectACL]

// gcsObjectACLKey keys one entry by its object and entity. Object names carry
// "/" freely, so the separator is a byte no name can hold.
func gcsObjectACLKey(bucket, object, entity string) string {
	return bucket + "\x00" + object + "\x00" + entity
}

// gcsUniformBucketLevelAccess reports whether the bucket has uniform
// bucket-level access enabled, which disables the legacy ACL surface.
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

// gcsProjectPrivateObjectACL is the predefined ACL Cloud Storage applies to a
// bucket that declares no default object ACL of its own: the project's owners
// and editors as OWNER, its viewers as READER. The entities are built from the
// bucket's own project number rather than invented — an object's ACL is never
// empty in the real service, which is what lets `gcloud storage objects update
// --remove-acl-grant` work at all. The CLI computes the remaining list and
// omits the member entirely when it comes out empty, so an object whose ACL
// held only the grant being removed would keep it forever.
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

// gcsSeedDefaultObjectACL gives a new bucket the predefined projectPrivate
// default object ACL when its create request declared none, which is what the
// service does. Objects then inherit it through the ordinary seed path.
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

// gcsSeedObjectACL copies the bucket's default object ACL onto a newly created
// object, which is what the real service does at creation time. Later edits to
// the bucket default do not reach objects already written, so the copy has to
// happen on the write rather than being projected on read.
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

// gcsReplaceObjectACL sets an object's entries to exactly those given. This is
// the objects.patch door onto the same state the objectAccessControls
// collection serves: the resource carries the whole ACL, so a patch that names
// it replaces the set rather than merging into it.
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

// gcsObjectACLEntries returns an object's entries, entity order, for the
// resource projection that carries them.
func gcsObjectACLEntries(bucket, object string) []GCSObjectACL {
	items := gcsObjectACLs.Filter(func(a GCSObjectACL) bool {
		return a.Bucket == bucket && a.Object == object
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Entity < items[j].Entity })
	return items
}

// gcsDropObjectACL removes an object's entries when the object goes away, so a
// name written again does not inherit the previous object's grants.
func gcsDropObjectACL(bucket, object string) {
	for _, entry := range gcsObjectACLs.Filter(func(a GCSObjectACL) bool {
		return a.Bucket == bucket && a.Object == object
	}) {
		gcsObjectACLs.Delete(gcsObjectACLKey(bucket, object, entry.Entity))
	}
}

func registerGCSObjectACLs(srv *sim.Server, buckets sim.Store[Bucket], objects sim.Store[GCSObject]) {
	// resolve answers the two questions every route here asks first: does the
	// object exist, and does this bucket still expose legacy ACLs at all.
	resolve := func(w http.ResponseWriter, r *http.Request) (bucket, object string, obj GCSObject, ok bool) {
		bucket, object = sim.PathParam(r, "bucket"), sim.PathParam(r, "object")
		b, found := buckets.Get(bucket)
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return "", "", GCSObject{}, false
		}
		if gcsUniformBucketLevelAccess(b) {
			sim.GCPError(w, http.StatusBadRequest,
				"Cannot get legacy ACL for an object when uniform bucket-level access is enabled. "+
					"Read the object's IAM policy instead.", "INVALID_ARGUMENT")
			return "", "", GCSObject{}, false
		}
		obj, found = objects.Get(bucket + "/" + object)
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", object, bucket)
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
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
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Entity == "" || in.Role == "" {
			sim.GCPError(w, http.StatusBadRequest, "entity and role are required", "INVALID_ARGUMENT")
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no ACL entry for entity %q on object %q in bucket %q", entity, object, bucket)
			return
		}
		var in GCSObjectACL
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Role == "" {
			sim.GCPError(w, http.StatusBadRequest, "role is required", "INVALID_ARGUMENT")
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no ACL entry for entity %q on object %q in bucket %q", entity, object, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
