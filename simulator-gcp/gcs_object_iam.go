package main

import (
	"net/http"
	"strings"

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

func registerGCSObjectIAM(srv *sim.Server, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject]) {
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
		bucket, object, ok := resolve(w, r)
		if !ok {
			return
		}
		gcsWriteTestPermissions(w, r, gcsObjectPolicies(bucket, object))
	})
}

var (
	gcsBucketACLRoles = map[string]string{
		"OWNER":  "roles/storage.legacyBucketOwner",
		"WRITER": "roles/storage.legacyBucketWriter",
		"READER": "roles/storage.legacyBucketReader",
	}
	gcsObjectACLRoles = map[string]string{
		"OWNER":  "roles/storage.legacyObjectOwner",
		"READER": "roles/storage.legacyObjectReader",
	}
	gcsProjectTeamMembers = map[string]string{
		"owners":  "projectOwner",
		"editors": "projectEditor",
		"viewers": "projectViewer",
	}
)

// gcsBucketPolicies are the policies that govern a bucket and what it holds:
// its project's, its own, and, while uniform bucket-level access is off, its
// ACL read as the legacy role Cloud Storage maps each ACL role to.
func gcsBucketPolicies(name string) []IAMPolicy {
	bucket, _ := gcsBuckets.Get(name)
	own, ok := gcpResourcePolicies.Get("bucket/" + name)
	if !ok {
		own = gcsDefaultBucketPolicy(bucket.Project)
	}
	policies := []IAMPolicy{gcpProjectPolicy(bucket.Project), own}
	if gcsUniformBucketLevelAccess(bucket) {
		return policies
	}
	var acl IAMPolicy
	for _, entry := range gcsBucketACLs.Filter(func(a GCSBucketACL) bool { return a.Bucket == name }) {
		acl.Bindings = append(acl.Bindings, IAMBinding{
			Role: gcsBucketACLRoles[entry.Role], Members: gcsACLMembers(bucket, entry.Entity)})
	}
	return append(policies, acl)
}

// gcsObjectPolicies adds to the bucket's policies the object's own policy and,
// while uniform bucket-level access is off, the object's ACL.
func gcsObjectPolicies(bucketName, object string) []IAMPolicy {
	policies := gcsBucketPolicies(bucketName)
	if own, ok := gcpResourcePolicies.Get(gcsObjectPolicyKey(bucketName, object)); ok {
		policies = append(policies, own)
	}
	bucket, _ := gcsBuckets.Get(bucketName)
	if gcsUniformBucketLevelAccess(bucket) {
		return policies
	}
	var acl IAMPolicy
	for _, entry := range gcsObjectACLEntries(bucketName, object) {
		acl.Bindings = append(acl.Bindings, IAMBinding{
			Role: gcsObjectACLRoles[entry.Role], Members: gcsACLMembers(bucket, entry.Entity)})
	}
	return append(policies, acl)
}

// gcsACLMembers are the IAM members an ACL entity names. A user- entity names
// a service account as well as a user, since ACLs spell both the same way.
func gcsACLMembers(bucket Bucket, entity string) []string {
	switch {
	case entity == "allUsers" || entity == "allAuthenticatedUsers":
		return []string{entity}
	case strings.HasPrefix(entity, "user-"):
		email := strings.TrimPrefix(entity, "user-")
		return []string{"user:" + email, "serviceAccount:" + email}
	case strings.HasPrefix(entity, "group-"):
		return []string{"group:" + strings.TrimPrefix(entity, "group-")}
	case strings.HasPrefix(entity, "domain-"):
		return []string{"domain:" + strings.TrimPrefix(entity, "domain-")}
	}
	_, team := gcsACLEmailFor(entity)
	if team == nil {
		return nil
	}
	number, _ := bucket.Data["projectNumber"].(string)
	if kind, known := gcsProjectTeamMembers[team.Team]; known && team.ProjectNumber == number {
		return []string{kind + ":" + bucket.Project}
	}
	return nil
}
