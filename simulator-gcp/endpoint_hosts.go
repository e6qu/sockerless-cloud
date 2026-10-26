package main

import (
	"net"
	"net/http"
	"strings"
)

// Google Cloud gives every service its own host, so two services may publish
// the same URI path: `run.googleapis.com/v1/projects/{p}/locations/{l}/
// instances/{id}:getIamPolicy` is Cloud Run Admin v1, and
// `redis.googleapis.com/v1/projects/{p}/locations/{l}/instances/{id}` is
// Memorystore for Redis. The simulator serves both from one origin, so it
// resolves the owner by the `Host` header and, at a bare coordinate, by the
// AIP-136 custom method in the URI.

// gcpServiceFromHost returns the service label the `Host` header names ("run"
// for `run.googleapis.com`), including the regional and mutual-TLS spellings,
// or "" for a bare address:port.
func gcpServiceFromHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	const googleAPIDomain = ".googleapis.com"
	if !strings.HasSuffix(host, googleAPIDomain) {
		return ""
	}
	label := strings.TrimSuffix(strings.TrimSuffix(host, googleAPIDomain), ".mtls")
	if i := strings.LastIndex(label, "."); i >= 0 {
		label = label[i+1:]
	}
	// Regional endpoints prefix the service with the region.
	if i := strings.LastIndex(label, "-"); i >= 0 {
		label = label[i+1:]
	}
	return label
}

// Service labels of the two APIs that publish
// `/v1/projects/{p}/locations/{l}/instances/…`.
const (
	gcpServiceCloudRun    = "run"
	gcpServiceMemorystore = "redis"
)

// cloudRunAdminV1InstanceIAMVerbs are the AIP-136 custom methods the Cloud Run
// Admin v1 instances collection serves, per HTTP method. Memorystore for Redis
// publishes none of them, so the verb alone identifies the owner.
var cloudRunAdminV1InstanceIAMVerbs = map[string]map[string]bool{
	http.MethodGet:  {"getIamPolicy": true},
	http.MethodPost: {"setIamPolicy": true, "testIamPermissions": true},
}

// gcpV1InstancesIsCloudRun reports whether a request to the shared
// `/v1/projects/{p}/locations/{l}/instances/…` prefix addresses Cloud Run
// Admin v1 rather than Memorystore for Redis. A Google host in `Host` is
// authoritative; otherwise verb, the URI's custom method, decides.
func gcpV1InstancesIsCloudRun(r *http.Request, verb string) bool {
	switch gcpServiceFromHost(r) {
	case gcpServiceCloudRun:
		return true
	case gcpServiceMemorystore:
		return false
	}
	return cloudRunAdminV1InstanceIAMVerbs[r.Method][verb]
}
