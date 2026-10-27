package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerGCSFolders(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, name string) string { return bucket + "\x00" + name }
	build := func(r *http.Request, bucket, name string) GCSFolder {
		now := gcsTimestamp()
		return GCSFolder{
			Kind:           "storage#folder",
			ID:             bucket + "/" + name,
			SelfLink:       gcpSelfLink(r, "/storage/v1/b/"+bucket+"/folders/"+name),
			Bucket:         bucket,
			Name:           name,
			Metageneration: "1",
			CreateTime:     now,
			UpdateTime:     now,
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/folders", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		prefix := r.URL.Query().Get("prefix")
		items := gcsFolders.Filter(func(f GCSFolder) bool {
			return f.Bucket == bucket && (prefix == "" || strings.HasPrefix(f.Name, prefix))
		})
		gcsWriteMapList(w, r, items, "storage#folders", func(v GCSFolder) string { return v.Name })
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "folder")
		f, ok := gcsFolders.Get(key(bucket, name))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %q not found in bucket %q", name, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/folders", func(w http.ResponseWriter, r *http.Request) {
		gcsCreateFolder(w, r, gcsFolders, bucketExists, "folder", func(f *GCSFolder) string { return f.Name }, key, build)
	})

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "folder")
		if !gcsFolders.Delete(key(bucket, name)) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %q not found in bucket %q", name, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// deleteRecursive and renameTo answer an operation; the simulator does the
	// work in the request and answers it done.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/folders/{folder}/deleteRecursive", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "folder")
		removed := false
		for _, f := range gcsFolders.Filter(func(f GCSFolder) bool { return f.Bucket == bucket && strings.HasPrefix(f.Name, name) }) {
			if gcsFolders.Delete(key(bucket, f.Name)) {
				removed = true
			}
		}
		if !removed {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %q not found in bucket %q", name, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, nil))
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/folders/{sourceFolder}/renameTo/folders/{destinationFolder}", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		src, dst := sim.PathParam(r, "sourceFolder"), sim.PathParam(r, "destinationFolder")
		f, ok := gcsFolders.Get(key(bucket, src))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %q not found in bucket %q", src, bucket)
			return
		}
		gcsFolders.Delete(key(bucket, src))
		f.Name = dst
		f.ID = bucket + "/" + dst
		f.SelfLink = gcpSelfLink(r, "/storage/v1/b/"+bucket+"/folders/"+dst)
		f.UpdateTime = gcsTimestamp()
		gcsFolders.Put(key(bucket, dst), f)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(f)))
	})
}

func registerGCSManagedFolders(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, name string) string { return bucket + "\x00" + name }
	build := func(r *http.Request, bucket, name string) GCSManagedFolder {
		now := gcsTimestamp()
		return GCSManagedFolder{
			Kind:           "storage#managedFolder",
			ID:             bucket + "/" + name,
			SelfLink:       gcpSelfLink(r, "/storage/v1/b/"+bucket+"/managedFolders/"+name),
			Bucket:         bucket,
			Name:           name,
			Metageneration: "1",
			CreateTime:     now,
			UpdateTime:     now,
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/managedFolders", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		prefix := r.URL.Query().Get("prefix")
		items := gcsManagedFolders.Filter(func(f GCSManagedFolder) bool {
			return f.Bucket == bucket && (prefix == "" || strings.HasPrefix(f.Name, prefix))
		})
		gcsWriteMapList(w, r, items, "storage#managedFolders", func(v GCSManagedFolder) string { return v.Name })
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		f, ok := gcsManagedFolders.Get(key(bucket, name))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed folder %q not found in bucket %q", name, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/managedFolders", func(w http.ResponseWriter, r *http.Request) {
		gcsCreateFolder(w, r, gcsManagedFolders, bucketExists, "managed folder", func(f *GCSManagedFolder) string { return f.Name }, key, build)
	})

	// managedFolders.update changes rapidCacheConfig, the one member not
	// output-only or fixed: present replaces, null clears, absent leaves it.
	// The write bumps metageneration.
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/managedFolders/{managedFolder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		f, ok := gcsManagedFolders.Get(key(bucket, name))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed folder %q not found in bucket %q", name, bucket)
			return
		}
		var in struct {
			// json.RawMessage distinguishes the three PATCH states: nil for
			// absent, the literal "null" for an explicit clear, JSON otherwise.
			RapidCacheConfig json.RawMessage `json:"rapidCacheConfig"`
		}
		if err := sim.ReadJSON(r, &in); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		switch {
		case in.RapidCacheConfig == nil:
			// absent: unchanged
		case string(in.RapidCacheConfig) == "null":
			f.RapidCacheConfig = nil
		default:
			f.RapidCacheConfig = in.RapidCacheConfig
		}
		if metageneration, err := strconv.ParseInt(f.Metageneration, 10, 64); err == nil {
			f.Metageneration = strconv.FormatInt(metageneration+1, 10)
		}
		f.UpdateTime = gcsTimestamp()
		gcsManagedFolders.Put(key(bucket, name), f)
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/managedFolders/{managedFolder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		if !gcsManagedFolders.Delete(key(bucket, name)) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed folder %q not found in bucket %q", name, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Managed-folder IAM uses the policy store bucket and object IAM share.
	resource := func(bucket, name string) string {
		return "projects/_/buckets/" + bucket + "/managedFolders/" + name
	}
	srv.HandleFunc("GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		if _, exists := gcsManagedFolders.Get(key(bucket, name)); !exists {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"managed folder %q not found in bucket %q", name, bucket)
			return
		}
		policy, ok := gcpResourcePolicies.Get("managedFolder/" + bucket + "/" + name)
		if !ok {
			policy = IAMPolicy{Bindings: []IAMBinding{}, Etag: gcpPolicyETag(), Version: 1}
			gcpResourcePolicies.Put("managedFolder/"+bucket+"/"+name, policy)
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = resource(bucket, name)
		sim.WriteJSON(w, http.StatusOK, policy)
	})
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		if _, exists := gcsManagedFolders.Get(key(bucket, name)); !exists {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"managed folder %q not found in bucket %q", name, bucket)
			return
		}
		var policy IAMPolicy
		if err := sim.ReadJSON(r, &policy); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// The document requires bindings on this method, so a set with none is
		// refused.
		if len(policy.Bindings) == 0 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"the policy must carry at least one binding")
			return
		}
		if err := validateIAMMembers(policy.Bindings); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		policy.Etag = gcpPolicyETag()
		if policy.Version == 0 {
			policy.Version = 1
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = resource(bucket, name)
		gcpResourcePolicies.Put("managedFolder/"+bucket+"/"+name, policy)
		sim.WriteJSON(w, http.StatusOK, policy)
	})
	srv.HandleFunc("GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam/testPermissions", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		if _, exists := gcsManagedFolders.Get(key(bucket, name)); !exists {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"managed folder %q not found in bucket %q", name, bucket)
			return
		}
		policies := gcsBucketPolicies(bucket)
		if own, ok := gcpResourcePolicies.Get("managedFolder/" + bucket + "/" + name); ok {
			policies = append(policies, own)
		}
		gcsWriteTestPermissions(w, r, policies,
			gcsIAMResource(bucket, "projects/_/buckets/"+bucket+"/managedFolders/"+name, "storage.googleapis.com/ManagedFolder"))
	})
}

// gcsCreateFolder answers the insert of a folder or managed folder of store
// in the bucket the path names, built by build and kept under key.
func gcsCreateFolder[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], bucketExists func(http.ResponseWriter, string) bool,
	noun string, name func(*T) string, key func(bucket, name string) string, build func(r *http.Request, bucket, name string) T) {
	bucket := sim.PathParam(r, "bucket")
	if !bucketExists(w, bucket) {
		return
	}
	var in T
	if err := sim.ReadJSON(r, &in); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	folder := name(&in)
	if folder == "" {
		GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
		return
	}
	if _, exists := store.Get(key(bucket, folder)); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "%s %q already exists", noun, folder)
		return
	}
	f := build(r, bucket, folder)
	store.Put(key(bucket, folder), f)
	sim.WriteJSON(w, http.StatusOK, f)
}
