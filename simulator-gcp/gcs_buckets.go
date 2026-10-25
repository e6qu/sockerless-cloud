package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// registerGCSBuckets mounts the bucket resource of the JSON API.
func registerGCSBuckets(srv *sim.Server, buckets sim.Store[Bucket]) {
	// Create bucket
	srv.HandleFunc("POST /storage/v1/b", func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if err := sim.ReadJSON(r, &data); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		name, _ := data["name"].(string)
		if name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		project := r.URL.Query().Get("project")
		if project == "" {
			GCPError(w, http.StatusBadRequest, "Required parameter: project", "INVALID_ARGUMENT")
			return
		}
		owner, ok := gcsResolveProject(w, project)
		if !ok {
			return
		}

		if _, exists := buckets.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "bucket %q already exists", name)
			return
		}

		now := gcsTimestamp()
		data["id"] = name
		data["kind"] = "storage#bucket"
		data["selfLink"] = gcpSelfLink(r, fmt.Sprintf("/storage/v1/b/%s", name))
		data["projectNumber"] = strings.TrimPrefix(owner.Name, "projects/")
		data["metageneration"] = "1"
		data["etag"] = "CAE="
		data["timeCreated"] = now
		data["updated"] = now
		// GCS normalizes bucket locations to upper-case on read; echo that or
		// terraform-provider-google replaces the bucket (location is ForceNew).
		location := "US"
		if loc, ok := data["location"].(string); ok && loc != "" {
			location = strings.ToUpper(loc)
		}
		data["location"] = location
		data["locationType"] = gcsLocationType(location)
		if data["storageClass"] == nil {
			data["storageClass"] = "STANDARD"
		}
		gcsApplyDefaultSoftDeletePolicy(data)

		// A new bucket holds no objects, so its mount directory starts empty
		// whatever a deleted bucket of the same name left in it.
		if err := gcsResetBucketHostDir(name); err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "bucket %q storage: %v", name, err)
			return
		}
		bucket := Bucket{Data: data, Project: owner.ProjectId}
		buckets.Put(name, bucket)
		gcsSeedDefaultObjectACL(name, bucket)
		gcsSeedDefaultBucketPolicy(name)
		sim.WriteJSON(w, http.StatusOK, data)
	})

	// Get bucket
	srv.HandleFunc("GET /storage/v1/b/{bucket}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")

		// Don't match if the path continues with /o (objects)
		if strings.Contains(r.URL.Path, "/o/") || strings.HasSuffix(r.URL.Path, "/o") {
			return
		}

		bucket, ok := buckets.Get(bucketName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, bucket.Data)
	})

	// Patch / Update bucket. The Go storage client's BucketHandle.Update
	// and terraform-provider-google send PATCH (PUT for full Update) to
	// mutate versioning/lifecycle/labels/iamConfiguration/retentionPolicy.
	// The bucket stores its JSON verbatim, so apply the patch body as a
	// top-level field merge into that map. `labels` honours GCS null-value-
	// deletes semantics (a null label value removes the key).
	patchBucket := func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		if strings.Contains(r.URL.Path, "/o/") || strings.HasSuffix(r.URL.Path, "/o") {
			return
		}
		bucket, ok := buckets.Get(bucketName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		var patch map[string]any
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		applyBucketPatch(bucket.Data, patch)
		bucket.Data["updated"] = gcsTimestamp()
		buckets.Put(bucketName, bucket)
		sim.WriteJSON(w, http.StatusOK, bucket.Data)
	}

	srv.HandleFunc("PATCH /storage/v1/b/{bucket}", patchBucket)

	srv.HandleFunc("PUT /storage/v1/b/{bucket}", patchBucket)

	// Get bucket storage layout
	srv.HandleFunc("GET /storage/v1/b/{bucket}/storageLayout", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		bucket, ok := buckets.Get(bucketName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}

		location, _ := bucket.Data["location"].(string)
		if location == "" {
			location = "US"
		}
		enabled := false
		if hns, ok := bucket.Data["hierarchicalNamespace"].(map[string]any); ok {
			enabled, _ = hns["enabled"].(bool)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":         "storage#storageLayout",
			"bucket":       bucketName,
			"location":     location,
			"locationType": gcsLocationType(location),
			"hierarchicalNamespace": map[string]any{
				"enabled": enabled,
			},
		})
	})

	// Delete bucket
	srv.HandleFunc("DELETE /storage/v1/b/{bucket}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")

		if _, ok := buckets.Get(bucketName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		// Live objects hold a bucket and soft-deleted ones do not; the client
		// deletes the objects itself (gcloud storage rm -r, force_destroy).
		if len(gcsBucketObjects(bucketName, "")) > 0 {
			writeGCSJSONError(w, http.StatusConflict, "conflict", "The bucket you tried to delete is not empty.")
			return
		}
		if !buckets.Delete(bucketName) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// List buckets
	srv.HandleFunc("GET /storage/v1/b", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			GCPError(w, http.StatusBadRequest, "Required parameter: project", "INVALID_ARGUMENT")
			return
		}
		owner, ok := gcsResolveProject(w, project)
		if !ok {
			return
		}
		prefix := r.URL.Query().Get("prefix")
		all := buckets.Filter(func(b Bucket) bool {
			return b.Project == owner.ProjectId && strings.HasPrefix(gcsBucketName(b), prefix)
		})
		sort.Slice(all, func(i, j int) bool { return gcsBucketName(all[i]) < gcsBucketName(all[j]) })
		listed, next, ok := gcsPageAfter(w, r, all, gcsBucketName)
		if !ok {
			return
		}
		page := make([]map[string]any, 0, len(listed))
		for _, b := range listed {
			page = append(page, b.Data)
		}
		resp := map[string]any{"kind": "storage#buckets", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
}

func registerGCSBucketACLs(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, entity string) string { return bucket + "\x00" + entity }
	build := func(r *http.Request, bucket, entity, role string) GCSBucketACL {
		email, team := gcsACLEmailFor(entity)
		return GCSBucketACL{
			Kind:        "storage#bucketAccessControl",
			ID:          bucket + "/" + entity,
			SelfLink:    gcpSelfLink(r, "/storage/v1/b/"+bucket+"/acl/"+entity),
			Bucket:      bucket,
			Entity:      entity,
			Role:        role,
			Email:       email,
			ProjectTeam: team,
			Etag:        "CAE=",
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/acl", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := gcsBucketACLs.Filter(func(a GCSBucketACL) bool { return a.Bucket == bucket })
		sort.Slice(items, func(i, j int) bool { return items[i].Entity < items[j].Entity })
		if items == nil {
			items = []GCSBucketACL{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#bucketAccessControls", "items": items})
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/acl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		acl, ok := gcsBucketACLs.Get(key(bucket, entity))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acl)
	})

	insert := func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSBucketACL
		if err := sim.ReadJSON(r, &in); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Entity == "" || in.Role == "" {
			GCPError(w, http.StatusBadRequest, "entity and role are required", "INVALID_ARGUMENT")
			return
		}
		acl := build(r, bucket, in.Entity, in.Role)
		gcsBucketACLs.Put(key(bucket, in.Entity), acl)
		sim.WriteJSON(w, http.StatusOK, acl)
	}
	srv.HandleFunc("POST /storage/v1/b/{bucket}/acl", insert)

	update := func(w http.ResponseWriter, r *http.Request) {
		gcsUpdateACL(w, r, gcsBucketACLs, bucketExists, "ACL", func(in GCSBucketACL) string { return in.Role }, key, build)
	}
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/acl/{entity}", update)
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/acl/{entity}", update)

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/acl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		if !gcsBucketACLs.Delete(key(bucket, entity)) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func registerGCSDefaultObjectACLs(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, entity string) string { return bucket + "\x00" + entity }
	build := func(r *http.Request, bucket, entity, role string) GCSObjectACL {
		email, team := gcsACLEmailFor(entity)
		return GCSObjectACL{
			Kind:        "storage#objectAccessControl",
			ID:          bucket + "/" + entity,
			SelfLink:    gcpSelfLink(r, "/storage/v1/b/"+bucket+"/defaultObjectAcl/"+entity),
			Bucket:      bucket,
			Entity:      entity,
			Role:        role,
			Email:       email,
			ProjectTeam: team,
			Etag:        "CAE=",
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/defaultObjectAcl", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := gcsObjectDefACLs.Filter(func(a GCSObjectACL) bool { return a.Bucket == bucket })
		sort.Slice(items, func(i, j int) bool { return items[i].Entity < items[j].Entity })
		if items == nil {
			items = []GCSObjectACL{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#objectAccessControls", "items": items})
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/defaultObjectAcl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		acl, ok := gcsObjectDefACLs.Get(key(bucket, entity))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no default object ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acl)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/defaultObjectAcl", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
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
		acl := build(r, bucket, in.Entity, in.Role)
		gcsObjectDefACLs.Put(key(bucket, in.Entity), acl)
		sim.WriteJSON(w, http.StatusOK, acl)
	})

	update := func(w http.ResponseWriter, r *http.Request) {
		gcsUpdateACL(w, r, gcsObjectDefACLs, bucketExists, "default object ACL", func(in GCSObjectACL) string { return in.Role }, key, build)
	}
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/defaultObjectAcl/{entity}", update)
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/defaultObjectAcl/{entity}", update)

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/defaultObjectAcl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		if !gcsObjectDefACLs.Delete(key(bucket, entity)) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no default object ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func registerGCSBucketLifecycle(srv *sim.Server, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject], bucketExists func(http.ResponseWriter, string) bool) {
	// bucket IAM testPermissions (getIamPolicy/setIamPolicy already live in iam.go)
	srv.HandleFunc("GET /storage/v1/b/{bucket}/iam/testPermissions", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		gcsWriteTestPermissions(w, r, gcsBucketPolicies(bucket),
			gcsIAMResource(bucket, "projects/_/buckets/"+bucket, "storage.googleapis.com/Bucket"))
	})

	// lockRetentionPolicy / restore both return the Bucket resource.
	returnBucket := func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		b, ok := buckets.Get(bucket)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, b.Data)
	}
	srv.HandleFunc("POST /storage/v1/b/{bucket}/lockRetentionPolicy", returnBucket)
	srv.HandleFunc("POST /storage/v1/b/{bucket}/restore", returnBucket)

	// buckets.relocate moves the bucket to the requested location and answers
	// with the operation that did it. validateOnly checks the request and
	// leaves the bucket where it is, which is what the member says it does.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/relocate", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "bucket")
		bucket, ok := buckets.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", name)
			return
		}
		var request GCSRelocateBucketRequest
		if err := sim.ReadJSON(r, &request); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		destination := request.DestinationLocation
		if destination == "" {
			// "If no location is provided, Cloud Storage will use the default
			// location, which is us" — the same default the create path
			// applies to a bucket that names none.
			destination = "US"
		}
		if !request.ValidateOnly {
			// GCS normalizes a bucket's location to upper case, the same way
			// the create path does.
			bucket.Data["location"] = strings.ToUpper(destination)
			bucket.Data["locationType"] = gcsLocationType(destination)
			if request.DestinationCustomPlacementConfig != nil {
				bucket.Data["customPlacementConfig"] = map[string]any{
					"dataLocations": request.DestinationCustomPlacementConfig.DataLocations,
				}
			}
			if request.DestinationKmsKeyName != "" {
				bucket.Data["encryption"] = map[string]any{
					"defaultKmsKeyName": request.DestinationKmsKeyName,
				}
			}
			bucket.Data["updated"] = gcsTimestamp()
			buckets.Put(name, bucket)
		}
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, name, nil))
	})

	// Bucket operations: list / get / cancel / advanceRelocateBucket. Every
	// answer here is about a record the bucket's long-running methods wrote —
	// an identifier no method minted is NOT_FOUND, as it is in real Cloud
	// Storage.
	srv.HandleFunc("GET /storage/v1/b/{bucket}/operations", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		done, filtered, ok := gcsOperationFilter(w, r)
		if !ok {
			return
		}
		prefix := gcsOperationName(bucket, "")
		items := crOperations.Filter(func(op Operation) bool {
			if !strings.HasPrefix(op.Name, prefix) {
				return false
			}
			return !filtered || op.Done == done
		})
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		page, next, ok := paginateList(w, r, items, gcpAsc(func(v Operation) string { return v.Name }))
		if !ok {
			return
		}
		response := map[string]any{"kind": "storage#operations", "operations": page}
		if next != "" {
			response["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, response)
	})
	srv.HandleFunc("GET /storage/v1/b/{bucket}/operations/{operationId}", func(w http.ResponseWriter, r *http.Request) {
		op, ok := gcsLookupOperation(w, r, bucketExists)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, op)
	})
	// The method's own description is that cancellation is best effort and
	// that a client polls the operation to learn whether it succeeded or the
	// operation completed anyway. Every operation this simulator records is
	// already complete when its name reaches a client, so the completed
	// outcome is the honest one: the record stands and the empty body the
	// document declares comes back.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/operations/{operationId}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := gcsLookupOperation(w, r, bucketExists); !ok {
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	// Advancing a relocation is best effort for the same reason: the write
	// downtime it schedules belongs to a relocation still in flight, and this
	// simulator's relocations finish inside the request that starts them.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/operations/{operationId}/advanceRelocateBucket", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := gcsLookupOperation(w, r, bucketExists); !ok {
			return
		}
		var request GCSAdvanceRelocateBucketOperationRequest
		if err := sim.ReadJSON(r, &request); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Metadata-only object insert (objects.insert with no media upload).
	// Creates a zero-length object from the supplied resource fields.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/o", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		name := r.URL.Query().Get("name")
		var res gcsObjectResource
		if r.ContentLength != 0 {
			if err := sim.ReadJSON(r, &res); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
		}
		if name == "" {
			name = res.Name
		}
		if name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		attrs := res.applyTo(GCSObject{Name: name})
		attrs.CustomContexts = res.Contexts.replacement(nil, "")
		pre, err := parseGCSPreconditions(r.URL.Query(), false)
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return
		}
		obj, err := persistGCSObject(objects, bucket, name, "", blobstore.Digests{}, attrs, pre, gcsMirror)
		if err != nil {
			writeGCSPersistError(w, "insert object", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, obj))
	})
}

// gcsUpdateACL answers the update of an entity's entry in a bucket's ACL of
// store, which holds what; the entry takes the role the body names.
func gcsUpdateACL[I, A any](w http.ResponseWriter, r *http.Request, store sim.Store[A], bucketExists func(http.ResponseWriter, string) bool,
	what string, role func(I) string, key func(bucket, entity string) string, build func(r *http.Request, bucket, entity, role string) A) {
	bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
	if _, ok := store.Get(key(bucket, entity)); !ok {
		if !bucketExists(w, bucket) {
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no %s entry for entity %q on bucket %q", what, entity, bucket)
		return
	}
	var in I
	if err := sim.ReadJSON(r, &in); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	acl := build(r, bucket, entity, role(in))
	store.Put(key(bucket, entity), acl)
	sim.WriteJSON(w, http.StatusOK, acl)
}
