package main

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon S3 serves a request that carries no signature as the anonymous
// principal, and grants that principal only what a bucket grants everyone: a
// bucket policy statement whose principal is "*", or an ACL grant to the
// AllUsers group. S3 Block Public Access withdraws either grant, and Object
// Ownership's BucketOwnerEnforced setting disables ACLs.

const (
	s3AllUsersGroup           = "http://acs.amazonaws.com/groups/global/AllUsers"
	s3AuthenticatedUsersGroup = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
	s3LogDeliveryGroup        = "http://acs.amazonaws.com/groups/s3/LogDelivery"
)

// s3AnonymousAccountOperations act on the caller's account, which an anonymous
// caller has none of, so no bucket can grant them.
var s3AnonymousAccountOperations = map[string]bool{
	"ListBuckets":          true,
	"CreateBucket":         true,
	"ListDirectoryBuckets": true,
}

// s3RefuseAnonymous answers AccessDenied for an anonymous request no bucket
// grant covers, and reports whether it did. A refused entry of a DeleteObjects
// request is carried to the handler, which reports it among the errors.
func s3RefuseAnonymous(w http.ResponseWriter, r *http.Request, op string, targets []iamAuthorizationTarget) (*http.Request, bool) {
	if s3AnonymousAccountOperations[op] {
		s3WriteAnonymousDeny(w, r)
		return r, true
	}
	var refused []s3ObjectIdentifier
	for _, target := range targets {
		if s3AnonymousAllowed(r, target) {
			continue
		}
		if target.entry != nil {
			refused = append(refused, *target.entry)
			continue
		}
		s3WriteAnonymousDeny(w, r)
		return r, true
	}
	if len(refused) > 0 {
		r = r.WithContext(context.WithValue(r.Context(), s3RefusedEntriesKey{}, refused))
	}
	return r, false
}

func s3WriteAnonymousDeny(w http.ResponseWriter, r *http.Request) {
	S3ErrorXML(w, "AccessDenied", "Access Denied",
		strings.TrimPrefix(r.URL.Path, "/"), sim.RequestID(r.Context()), http.StatusForbidden)
}

// s3AnonymousAllowed reports whether the bucket a target names grants the
// anonymous principal its action. A bucket that does not exist grants
// nothing and refuses nothing: S3 answers NoSuchBucket.
func s3AnonymousAllowed(r *http.Request, target iamAuthorizationTarget) bool {
	path, general := strings.CutPrefix(target.resource, "arn:aws:s3:::")
	if !general {
		return false
	}
	bucket, key, _ := strings.Cut(path, "/")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		return true
	}
	block := s3BucketPublicAccessBlockOf(bucket)
	ctx := iamRequestConditionContext(r, "", "", "", target.action)
	for name, values := range target.context {
		ctx[name] = values
	}
	switch decision, _ := iamEvalDecisionForPrincipal(iamResourcePolicyDocsForARN(s3BucketARN(bucket)), target.action, target.resource, "anonymous", ctx); decision {
	case "explicitDeny":
		return false
	case "allowed":
		if !block.RestrictPublicBuckets {
			return true
		}
	}
	if block.IgnorePublicAcls || s3BucketOwnerEnforced(bucket) {
		return false
	}
	permission, onObject := s3ACLPermissionFor(target.action)
	if permission == "" {
		return false
	}
	if !onObject {
		acl, _, _, _ := getStoredBucketSubresource(bucket, "acl")
		return s3ACLGrants(acl, s3AllUsersGroup, permission)
	}
	object, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	return ok && s3ACLGrants(object.ACL, s3AllUsersGroup, permission)
}

// s3ACLPermissionFor is the ACL permission that grants an action, and whether
// the object's ACL holds it rather than the bucket's, as the Amazon S3 User
// Guide maps ACL permissions to policy actions.
func s3ACLPermissionFor(action string) (string, bool) {
	switch strings.TrimPrefix(action, "s3:") {
	case "ListBucket", "ListBucketVersions", "ListBucketMultipartUploads":
		return "READ", false
	case "PutObject", "DeleteObject", "DeleteObjectVersion":
		return "WRITE", false
	case "GetBucketAcl":
		return "READ_ACP", false
	case "PutBucketAcl":
		return "WRITE_ACP", false
	case "GetObject", "GetObjectVersion", "GetObjectTorrent":
		return "READ", true
	case "GetObjectAcl", "GetObjectVersionAcl":
		return "READ_ACP", true
	case "PutObjectAcl", "PutObjectVersionAcl":
		return "WRITE_ACP", true
	}
	return "", false
}

type s3AccessControlPolicy struct {
	Grants []struct {
		Grantee struct {
			URI string `xml:"URI"`
		} `xml:"Grantee"`
		Permission string `xml:"Permission"`
	} `xml:"AccessControlList>Grant"`
}

// s3ACLGrants reports whether an AccessControlPolicy grants a group a
// permission, directly or through FULL_CONTROL.
func s3ACLGrants(acl []byte, group, permission string) bool {
	if len(acl) == 0 {
		return false
	}
	var policy s3AccessControlPolicy
	if xml.Unmarshal(acl, &policy) != nil {
		return false
	}
	for _, grant := range policy.Grants {
		if grant.Grantee.URI == group && (grant.Permission == permission || grant.Permission == "FULL_CONTROL") {
			return true
		}
	}
	return false
}

func s3BucketPublicAccessBlockOf(bucket string) s3PublicAccessBlock {
	var block s3PublicAccessBlock
	if body, _, _, ok := getStoredBucketSubresource(bucket, "publicAccessBlock"); ok {
		_ = xml.Unmarshal(body, &block)
	}
	return block
}

func s3BucketOwnerEnforced(bucket string) bool {
	body, _, _, ok := getStoredBucketSubresource(bucket, "ownershipControls")
	if !ok {
		return false
	}
	var controls struct {
		Rules []struct {
			ObjectOwnership string `xml:"ObjectOwnership"`
		} `xml:"Rule"`
	}
	if xml.Unmarshal(body, &controls) != nil {
		return false
	}
	for _, rule := range controls.Rules {
		if rule.ObjectOwnership == "BucketOwnerEnforced" {
			return true
		}
	}
	return false
}

// s3RequestACL is the AccessControlPolicy a PutBucketAcl or PutObjectAcl
// request sets: its body, or the policy its x-amz-acl canned ACL stands for.
// It answers InvalidArgument for a canned ACL S3 does not define.
func s3RequestACL(w http.ResponseWriter, r *http.Request, body []byte) ([]byte, bool) {
	canned := r.Header.Get("x-amz-acl")
	if len(body) > 0 || canned == "" {
		return body, true
	}
	policy, ok := s3CannedACLPolicy(canned)
	if !ok {
		S3ErrorXML(w, "InvalidArgument", "", strings.TrimPrefix(r.URL.Path, "/"), sim.RequestID(r.Context()), http.StatusBadRequest)
	}
	return policy, ok
}

// s3CannedACLPolicy is the AccessControlPolicy a canned ACL stands for, as
// the Amazon S3 User Guide's canned ACL table grants it; ok is false for a
// name S3 does not define.
func s3CannedACLPolicy(canned string) (policy []byte, ok bool) {
	type grant struct{ group, permission string }
	var grants []grant
	switch canned {
	case "private", "bucket-owner-read", "bucket-owner-full-control", "aws-exec-read":
	case "public-read":
		grants = []grant{{s3AllUsersGroup, "READ"}}
	case "public-read-write":
		grants = []grant{{s3AllUsersGroup, "READ"}, {s3AllUsersGroup, "WRITE"}}
	case "authenticated-read":
		grants = []grant{{s3AuthenticatedUsersGroup, "READ"}}
	case "log-delivery-write":
		grants = []grant{{s3LogDeliveryGroup, "WRITE"}, {s3LogDeliveryGroup, "READ_ACP"}}
	default:
		return nil, false
	}
	owner := awsAccountID()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>` + owner + `</ID><DisplayName>simulator</DisplayName></Owner><AccessControlList>`)
	b.WriteString(`<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>` + owner + `</ID><DisplayName>simulator</DisplayName></Grantee><Permission>FULL_CONTROL</Permission></Grant>`)
	for _, g := range grants {
		b.WriteString(`<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>` + g.group + `</URI></Grantee><Permission>` + g.permission + `</Permission></Grant>`)
	}
	b.WriteString(`</AccessControlList></AccessControlPolicy>`)
	return []byte(b.String()), true
}
