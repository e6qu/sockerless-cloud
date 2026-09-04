package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// The access point data plane.
//
// An access point is an addressable front door onto one bucket, reached at
// `<name>-<account>.s3-accesspoint.<region>.amazonaws.com`, and what makes it
// more than an alias is that it narrows what can be done through it: its scope
// names the key prefixes it reaches and the operations it allows, and a request
// outside either is refused however the caller's own policies read.
//
// The bucket arrives in the hostname, so the request is mapped onto the path
// the router works in the same way a directory bucket's zonal request is, and
// the signature is still verified against the path the client sent.

// s3AccessPointContextKey carries the access point a request was addressed
// through, for the condition keys that describe it.
type s3AccessPointContextKey struct{}

// s3AccessPointHost reads the access point a hostname addresses. Amazon S3
// spells the alias `<name>-<account>`, so the account is the last dash-separated
// field and the name is everything before it.
func s3AccessPointHost(host string) (S3AccessPoint, bool) {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	alias, rest, found := strings.Cut(host, ".")
	if !found || !strings.HasPrefix(rest, "s3-accesspoint.") {
		return S3AccessPoint{}, false
	}
	cut := strings.LastIndexByte(alias, '-')
	if cut <= 0 {
		return S3AccessPoint{}, false
	}
	name, account := alias[:cut], alias[cut+1:]
	return s3AccessPoints.Get(s3AccessPointKey(account, name))
}

// s3RewriteAccessPointRequest maps a request addressed to an access point onto
// the bucket it fronts, recording the access point for the enforcement and the
// condition keys that follow, and the path the client signed.
func s3RewriteAccessPointRequest(r *http.Request) bool {
	ap, ok := s3AccessPointHost(r.Host)
	if !ok {
		return false
	}
	signed := r.URL.Path
	if signed == "" {
		signed = "/"
	}
	ctx := context.WithValue(r.Context(), s3SignedPathContextKey{}, signed)
	ctx = context.WithValue(ctx, s3AccessPointContextKey{}, ap)
	*r = *r.WithContext(ctx)
	r.URL.Path = "/" + ap.Bucket + strings.TrimSuffix(signed, "/")
	if r.URL.RawPath != "" {
		r.URL.RawPath = "/" + ap.Bucket + strings.TrimSuffix(r.URL.RawPath, "/")
	}
	return true
}

// s3RequestAccessPoint returns the access point a request was addressed
// through, if it was.
func s3RequestAccessPoint(r *http.Request) (S3AccessPoint, bool) {
	ap, ok := r.Context().Value(s3AccessPointContextKey{}).(S3AccessPoint)
	return ap, ok
}

// s3EnforceAccessPointScope refuses a request the access point's scope does not
// admit, and reports whether it wrote the refusal. A scope names the key
// prefixes the access point reaches and the operations it allows; an access
// point with no scope put on it admits everything its bucket does.
func s3EnforceAccessPointScope(w http.ResponseWriter, r *http.Request, operation string) bool {
	ap, addressed := s3RequestAccessPoint(r)
	if !addressed || ap.Scope == nil {
		return false
	}
	if len(ap.Scope.Permissions) > 0 && !s3ScopeAdmitsOperation(ap.Scope.Permissions, operation) {
		s3AccessPointDenied(w, r, ap.Bucket,
			fmt.Sprintf("The access point's scope does not permit %s.", operation))
		return true
	}
	if len(ap.Scope.Prefixes) > 0 {
		key := strings.TrimPrefix(sim.PathParam(r, "key"), "/")
		if key != "" && !s3ScopeAdmitsPrefix(ap.Scope.Prefixes, key) {
			s3AccessPointDenied(w, r, ap.Bucket,
				fmt.Sprintf("The access point's scope reaches no key beginning %q.", key))
			return true
		}
	}
	return false
}

// s3ScopeAdmitsOperation reports whether a scope's permission list covers an
// operation. A permission is spelled either as the operation itself
// ("GetObject") or as a family with a wildcard ("Get*"), which is how Amazon S3
// writes them.
func s3ScopeAdmitsOperation(permissions []string, operation string) bool {
	for _, permission := range permissions {
		if permission == operation || permission == "*" {
			return true
		}
		if stem, wild := strings.CutSuffix(permission, "*"); wild && strings.HasPrefix(operation, stem) {
			return true
		}
	}
	return false
}

// s3ScopeAdmitsPrefix reports whether a key begins with one of the scope's
// prefixes.
func s3ScopeAdmitsPrefix(prefixes []string, key string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, strings.TrimSuffix(prefix, "*")) {
			return true
		}
	}
	return false
}

func s3AccessPointDenied(w http.ResponseWriter, r *http.Request, bucket, message string) {
	sim.S3ErrorXML(w, "AccessDenied", message, bucket, sim.RequestID(r.Context()), http.StatusForbidden)
}
