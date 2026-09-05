package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Object IAM — objects.getIamPolicy / setIamPolicy / testIamPermissions, on
// the same shared policy store the bucket and managed-folder policies use.
//
// The routes take a single-segment {object} so they beat the `{object...}`
// catch-all serving objects.get, which otherwise swallows `/o/{object}/iam`
// and answers `object "doc.txt/iam" not found` — a handler's structured 404,
// indistinguishable from a served read of an absent object.

func gcsObjectPolicyKey(bucket, object string) string {
	return "object/" + bucket + "\x00" + object
}

func registerGCSObjectIAM(srv *sim.Server, buckets sim.Store[Bucket], objects sim.Store[GCSObject]) {
	resourceID := func(bucket, object string) string {
		return "projects/_/buckets/" + bucket + "/objects/" + object
	}
	resolve := func(w http.ResponseWriter, r *http.Request) (bucket, object string, ok bool) {
		bucket, object = sim.PathParam(r, "bucket"), sim.PathParam(r, "object")
		if _, found := buckets.Get(bucket); !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return "", "", false
		}
		if _, found := objects.Get(bucket + "/" + object); !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", object, bucket)
			return "", "", false
		}
		return bucket, object, true
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/o/{object}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket, object, ok := resolve(w, r)
		if !ok {
			return
		}
		key := gcsObjectPolicyKey(bucket, object)
		policy, found := gcpResourcePolicies.Get(key)
		if !found {
			policy = IAMPolicy{Bindings: []IAMBinding{}, Etag: gcpPolicyETag(), Version: 1}
			gcpResourcePolicies.Put(key, policy)
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = resourceID(bucket, object)
		sim.WriteJSON(w, http.StatusOK, policy)
	})

	srv.HandleFunc("PUT /storage/v1/b/{bucket}/o/{object}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket, object, ok := resolve(w, r)
		if !ok {
			return
		}
		var policy IAMPolicy
		if err := sim.ReadJSON(r, &policy); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		policy.Etag = gcpPolicyETag()
		if policy.Version == 0 {
			policy.Version = 1
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = resourceID(bucket, object)
		gcpResourcePolicies.Put(gcsObjectPolicyKey(bucket, object), policy)
		sim.WriteJSON(w, http.StatusOK, policy)
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/o/{object}/iam/testPermissions", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := resolve(w, r); !ok {
			return
		}
		gcsWriteTestPermissions(w, r)
	})
}
