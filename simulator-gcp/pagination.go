package main

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"
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

const gcpEmptyType = "type.googleapis.com/google.protobuf.Empty"

// gcpOperationMetadata builds a finished operation's metadata message from the
// response it finished with.
type gcpOperationMetadata func(response map[string]any) map[string]any

// gcpStandardOperationMetadata is the OperationMetadata message the Cloud
// Functions, API Gateway, Eventarc and Memorystore APIs each declare in their
// own package: when the operation was created and ended, the method it ran,
// and the resource it acted on.
func gcpStandardOperationMetadata(typeURL, verb, target string) gcpOperationMetadata {
	return func(map[string]any) map[string]any {
		now := nowTimestamp()
		return map[string]any{"@type": typeURL, "createTime": now, "endTime": now, "verb": verb, "target": target}
	}
}

// gcpOperationVerb names the method a request runs the way OperationMetadata.verb
// spells it: a custom method by its own name, a standard method as create,
// update or delete.
func gcpOperationVerb(r *http.Request) string {
	segment := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if _, verb, found := gcpCustomMethod(segment); found {
		return verb
	}
	switch r.Method {
	case http.MethodDelete:
		return "delete"
	case http.MethodPatch, http.MethodPut:
		return "update"
	default:
		return "create"
	}
}

// gcpEmptyOperationMetadata is a metadata message that declares no fields.
func gcpEmptyOperationMetadata(typeURL string) gcpOperationMetadata {
	return func(map[string]any) map[string]any {
		return map[string]any{"@type": typeURL}
	}
}

// gcpFixedOperationMetadata is a metadata message its caller built whole.
func gcpFixedOperationMetadata(metadata map[string]any) gcpOperationMetadata {
	return func(map[string]any) map[string]any { return metadata }
}

// gcpResourceOperationMetadata is Cloud Run's rule: each Cloud Run Admin API v2
// method declares the resource it acts on as its operation's metadata.
func gcpResourceOperationMetadata(response map[string]any) map[string]any {
	return cloneAnyMap(response)
}

func gcpPolicyETag() string {
	return base64.StdEncoding.EncodeToString([]byte(sim.NewUUID()))
}
