package main

import (
	"net"
	"net/http"
	"strings"
)

// Host-based service resolution for path shapes two Google Cloud services
// share.
//
// Real Google Cloud gives every service its own hostname, so two services may
// publish the identical URI path without ambiguity:
// `run.googleapis.com/v1/projects/{p}/locations/{l}/instances/{id}:getIamPolicy`
// is the Cloud Run Admin v1 instances IAM alias, while
// `redis.googleapis.com/v1/projects/{p}/locations/{l}/instances/{id}` is a
// Memorystore for Redis instance. The simulator serves every service from one
// origin, so the owner is resolved here instead: by the `Host` header a client
// resolving a real Google host sends, and — when one origin's address is the
// configured coordinate for every service — by the AIP-136 custom method the
// request URI already carries.

// gcpServiceFromHost returns the Google Cloud service label a request's `Host`
// header names: "run" for `run.googleapis.com`, "redis" for
// `redis.googleapis.com`. The regional (`us-central1-run.googleapis.com`) and
// mutual-TLS (`run.mtls.googleapis.com`) spellings of a Google API endpoint
// resolve to the same service. A Host outside the Google API domain — a bare
// address:port coordinate — names no service and yields "".
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
// Admin v1 instances collection serves, per HTTP method:
// run.projects.locations.instances.getIamPolicy is a GET, setIamPolicy and
// testIamPermissions are POSTs. Memorystore for Redis publishes none of them,
// and its own instance verbs (export, failover, import, upgrade,
// rescheduleMaintenance) appear on neither list — so the custom method in the
// URI identifies the owning service on its own.
var cloudRunAdminV1InstanceIAMVerbs = map[string]map[string]bool{
	http.MethodGet:  {"getIamPolicy": true},
	http.MethodPost: {"setIamPolicy": true, "testIamPermissions": true},
}

// gcpV1InstancesIsCloudRun reports whether a request to the shared
// `/v1/projects/{p}/locations/{l}/instances/…` prefix addresses Cloud Run
// Admin v1 rather than Memorystore for Redis. verb is the AIP-136 custom
// method the request URI carries after the resource id, "" when it carries
// none. A client that resolved a real Google host names its service in the
// `Host` header and that answer is authoritative — a bare
// `GET run.googleapis.com/v1/.../instances/{id}` is a Cloud Run request for a
// method Cloud Run does not publish, never a Redis lookup.
func gcpV1InstancesIsCloudRun(r *http.Request, verb string) bool {
	switch gcpServiceFromHost(r) {
	case gcpServiceCloudRun:
		return true
	case gcpServiceMemorystore:
		return false
	}
	return cloudRunAdminV1InstanceIAMVerbs[r.Method][verb]
}
