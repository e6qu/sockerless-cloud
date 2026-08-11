package main

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// nowTimestamp returns a protobuf JSON Timestamp-compatible UTC value.
// Protobuf JSON canonicalizes fractional seconds to 0, 3, 6, or 9 digits;
// millisecond precision is enough for the simulator and avoids RFC3339Nano's
// variable-width fractions.
func nowTimestamp() string {
	return time.Now().UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func paginateList[T any](w http.ResponseWriter, r *http.Request, items []T) ([]T, string, bool) {
	return paginateListParam(w, r, items, "pageSize")
}

// paginateListCompute paginates using the Compute API page-size parameter name
// ("maxResults"), which the Compute REST API + Go SDK send instead of "pageSize".
func paginateListCompute[T any](w http.ResponseWriter, r *http.Request, items []T) ([]T, string, bool) {
	return paginateListParam(w, r, items, "maxResults")
}

// paginateListGCS paginates using the GCS JSON API page-size parameter name
// ("maxResults"). buckets.list / objects.list (and the Go storage client's
// BucketIterator / ObjectIterator PageSize) send "maxResults", not "pageSize".
func paginateListGCS[T any](w http.ResponseWriter, r *http.Request, items []T) ([]T, string, bool) {
	return paginateListParam(w, r, items, "maxResults")
}

// paginateListParam slices items by an opaque numeric index page token. It only
// paginates when the client supplies an explicit positive page size under
// sizeParam; an unset/zero size returns the full list with no token.
func paginateListParam[T any](w http.ResponseWriter, r *http.Request, items []T, sizeParam string) ([]T, string, bool) {
	start := 0
	if token := r.URL.Query().Get("pageToken"); token != "" {
		n, err := strconv.Atoi(token)
		if err != nil || n < 0 || n > len(items) {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageToken %q", token)
			return nil, "", false
		}
		start = n
	}

	pageSize := len(items)
	if raw := r.URL.Query().Get(sizeParam); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s %q", sizeParam, raw)
			return nil, "", false
		}
		if n > 0 && n < pageSize {
			pageSize = n
		}
	}
	if start > len(items) {
		start = len(items)
	}
	end := len(items)
	if pageSize > 0 && start+pageSize < end {
		end = start + pageSize
	}
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return items[start:end], next, true
}

func sortCloudRunJobs(items []Job) {
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}

func sortCloudRunServices(items []ServiceV2) {
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}

func sortCloudRunExecutions(items []Execution) {
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}

func sortCloudFunctions(items []Function) {
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}

func gcpOperationMetadataType(responseType string) string {
	switch responseType {
	case "type.googleapis.com/google.cloud.functions.v2.Function":
		return "type.googleapis.com/google.cloud.functions.v2.OperationMetadata"
	case "type.googleapis.com/google.cloud.run.v2.Job",
		"type.googleapis.com/google.cloud.run.v2.Execution",
		"type.googleapis.com/google.cloud.run.v2.Service":
		return "type.googleapis.com/google.cloud.run.v2.OperationMetadata"
	case "type.googleapis.com/google.cloud.apigateway.v1.Api",
		"type.googleapis.com/google.cloud.apigateway.v1.ApiConfig",
		"type.googleapis.com/google.cloud.apigateway.v1.Gateway":
		return "type.googleapis.com/google.cloud.apigateway.v1.OperationMetadata"
	case "type.googleapis.com/google.cloud.eventarc.v1.Trigger",
		"type.googleapis.com/google.cloud.eventarc.v1.Channel",
		"type.googleapis.com/google.cloud.eventarc.v1.ChannelConnection",
		"type.googleapis.com/google.cloud.eventarc.v1.Enrollment",
		"type.googleapis.com/google.cloud.eventarc.v1.MessageBus",
		"type.googleapis.com/google.cloud.eventarc.v1.Pipeline",
		"type.googleapis.com/google.cloud.eventarc.v1.GoogleApiSource":
		return "type.googleapis.com/google.cloud.eventarc.v1.OperationMetadata"
	default:
		return "type.googleapis.com/google.longrunning.OperationMetadata"
	}
}

func gcpPolicyETag() string {
	return base64.StdEncoding.EncodeToString([]byte(generateUUID()))
}
