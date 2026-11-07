package main

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
)

// s3EndpointHost matches the hosts a general-purpose bucket is addressed
// under virtual-hosted style, `<bucket>.<host>`: the global and Regional
// endpoints, their dual-stack forms and the legacy dash-Region form, as the
// S3 endpoint rules build them, plus the `s3.localhost` coordinate a client
// pointed at this simulator by service hostname uses.
var s3EndpointHost = regexp.MustCompile(`^(s3\.amazonaws\.com|s3\.(dualstack\.)?[a-z0-9-]+\.amazonaws\.com|s3-[a-z0-9-]+\.amazonaws\.com|s3\.localhost)$`)

// s3VirtualHostBucket returns the bucket a virtual-hosted request names, or ""
// for a path-style one. A client given an endpoint URL puts the bucket in
// front of that URL's host, so `http://localhost:4566` becomes
// `<bucket>.localhost:4566`; other services share the `.localhost` family by
// service name, so a bucket in front of a bare `localhost` counts only when
// the request is signed for S3, or is anonymous and the bucket exists.
func s3VirtualHostBucket(r *http.Request) string {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	if net.ParseIP(host) != nil {
		return ""
	}
	// An Object Lambda access point's alias and WriteGetObjectResponse's
	// request route sit in front of the endpoint host too; the alias names an
	// access point, and the route is signed for s3-object-lambda.
	if name := awsRequestSigningName(r); name != "" && name != "s3" {
		return ""
	}
	if _, ok := s3ObjectLambdaHostAccessPoint(r.Host); ok {
		return ""
	}
	for i := 0; i < len(host); i++ {
		if host[i] != '.' {
			continue
		}
		bucket, endpoint := host[:i], host[i+1:]
		if s3EndpointHost.MatchString(endpoint) {
			return bucket
		}
		if endpoint != "localhost" || s3ServiceLabel(bucket) {
			continue
		}
		switch awsRequestSigningName(r) {
		case "s3":
			return bucket
		case "":
			if _, ok := s3Buckets_.Get(bucket); ok {
				return bucket
			}
		}
		return ""
	}
	return ""
}

// s3RewriteVirtualHostRequest maps a virtual-hosted request on a
// general-purpose bucket onto the path the router works in, recording the path
// the client signed so the signature is verified against what was sent.
func s3RewriteVirtualHostRequest(r *http.Request) bool {
	bucket := s3VirtualHostBucket(r)
	if bucket == "" {
		return false
	}
	signed := r.URL.Path
	if signed == "" {
		signed = "/"
	}
	*r = *r.WithContext(context.WithValue(r.Context(), s3SignedPathContextKey{}, signed))
	r.URL.Path = s3HostedPath(bucket, signed)
	if r.URL.RawPath != "" {
		r.URL.RawPath = s3HostedPath(bucket, r.URL.RawPath)
	}
	return true
}

// s3HostedPath is the path-style form of a virtual-hosted request's path: the
// bucket's root for `/`, and the key with any trailing slash kept otherwise,
// since `photos/` is a key of its own.
func s3HostedPath(bucket, path string) string {
	if path == "" || path == "/" {
		return "/" + bucket
	}
	return "/" + bucket + path
}

var s3GeneralPurposeBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// s3ValidGeneralPurposeBucketName applies the general purpose bucket naming
// rules (bucketnamingrules): 3 to 63 lowercase letters, numbers, periods and
// hyphens, a letter or number at each end, no two adjacent periods, not an IP
// address, and none of the reserved prefixes and suffixes.
func s3ValidGeneralPurposeBucketName(name string) bool {
	if !s3GeneralPurposeBucketName.MatchString(name) || strings.Contains(name, "..") {
		return false
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return false
	}
	for _, prefix := range []string{"xn--", "sthree-", "amzn-s3-demo-"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	for _, suffix := range []string{"-s3alias", "--ol-s3", ".mrap", "--x-s3", "--table-s3"} {
		if strings.HasSuffix(name, suffix) {
			return false
		}
	}
	return true
}

// s3BucketOwner is the account that owns a bucket.
func s3BucketOwner(b S3Bucket) string {
	if b.Owner == "" {
		return awsAccountID()
	}
	return b.Owner
}

// s3RequestRegion is the Region a request was sent to, which a signed request
// names in its credential scope.
func s3RequestRegion(r *http.Request) string {
	credential := ""
	if auth := r.Header.Get("Authorization"); auth != "" {
		if i := strings.Index(auth, "Credential="); i >= 0 {
			credential, _, _ = strings.Cut(auth[i+len("Credential="):], ",")
		}
	} else {
		credential = r.URL.Query().Get("X-Amz-Credential")
	}
	if scope, ok := parseCredScope(strings.TrimSpace(credential)); ok {
		return scope.region
	}
	return awsRegion()
}

// s3ResetBucketACL returns a bucket to the private ACL it was created with.
func s3ResetBucketACL(bucket string) {
	s3BucketConfigs.Delete(bucket + "/acl")
}

// s3ServiceLabel reports whether a name in front of `localhost` is another S3
// endpoint's host rather than a bucket: S3 Control and S3 Object Lambda put
// the account or request route in front of their own service label.
func s3ServiceLabel(name string) bool {
	last := name[strings.LastIndexByte(name, '.')+1:]
	switch last {
	case "s3", "s3-control", "s3-object-lambda", "s3express-control":
		return true
	}
	return false
}
