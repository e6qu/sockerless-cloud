package main

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
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

// paginateListParam pages items by a decimal offset token. It pages only when
// the client names a positive size under sizeParam; otherwise it returns the
// whole remaining list.
func paginateListParam[T any](w http.ResponseWriter, r *http.Request, items []T, sizeParam string) ([]T, string, bool) {
	size, ok := gcpPageSizeParam(w, r, sizeParam)
	if !ok {
		return nil, "", false
	}
	return gcpOffsetPage(w, items, r.URL.Query().Get("pageToken"), size, 0)
}

// gcpPageSizeParam reads a non-negative page size from the query string.
func gcpPageSizeParam(w http.ResponseWriter, r *http.Request, param string) (int, bool) {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s %q", param, raw)
		return 0, false
	}
	return n, true
}

// gcpOffsetPage pages items by a decimal offset token, capping the page at max
// when max is positive, and answers INVALID_ARGUMENT for a token it never issued.
func gcpOffsetPage[T any](w http.ResponseWriter, items []T, token string, size, max int) ([]T, string, bool) {
	page, next, err := listq.TokenPage(listq.Decimal.Strictly(), items, token, size, 0, max)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageToken %q", token)
		return nil, "", false
	}
	return page, next, true
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
	return base64.StdEncoding.EncodeToString([]byte(sim.NewUUID()))
}
