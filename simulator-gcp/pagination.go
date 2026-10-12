package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// protoJSONTimestamp renders t as protojson renders a google.protobuf.Timestamp:
// UTC, with the fraction at full precision in 0, 3, 6 or 9 digits.
func protoJSONTimestamp(t time.Time) string {
	t = t.UTC()
	nanos := t.Nanosecond()
	fraction := ""
	switch {
	case nanos == 0:
	case nanos%1_000_000 == 0:
		fraction = fmt.Sprintf(".%03d", nanos/1_000_000)
	case nanos%1_000 == 0:
		fraction = fmt.Sprintf(".%06d", nanos/1_000)
	default:
		fraction = fmt.Sprintf(".%09d", nanos)
	}
	return t.Format("2006-01-02T15:04:05") + fraction + "Z"
}

// gcpSortKey is one term of a list's order: the value an item sorts by, how two
// values compare, and whether the term runs descending.
type gcpSortKey[T any] struct {
	value func(T) string
	cmp   func(a, b string) int
	desc  bool
}

// gcpAsc orders a list ascending by value, compared as text.
func gcpAsc[T any](value func(T) string) gcpSortKey[T] {
	return gcpSortKey[T]{value: value, cmp: strings.Compare}
}

// gcpDesc orders a list descending by value, compared as text.
func gcpDesc[T any](value func(T) string) gcpSortKey[T] {
	return gcpSortKey[T]{value: value, cmp: strings.Compare, desc: true}
}

// gcpAscNumber orders a list ascending by a decimal value.
func gcpAscNumber[T any](value func(T) string) gcpSortKey[T] {
	return gcpSortKey[T]{value: value, cmp: listq.CompareOrdered}
}

// gcpDescNumber orders a list descending by a decimal value.
func gcpDescNumber[T any](value func(T) string) gcpSortKey[T] {
	return gcpSortKey[T]{value: value, cmp: listq.CompareOrdered, desc: true}
}

// gcpField reads a string member of a resource held as a JSON object.
func gcpField(member string) func(map[string]any) string {
	return func(resource map[string]any) string {
		value, _ := resource[member].(string)
		return value
	}
}

// paginateList pages items by pageSize and pageToken in the order first and
// then rest give, the last term naming each item uniquely.
func paginateList[T any](w http.ResponseWriter, r *http.Request, items []T, first gcpSortKey[T], rest ...gcpSortKey[T]) ([]T, string, bool) {
	return paginateListParam(w, r, items, "pageSize", append([]gcpSortKey[T]{first}, rest...))
}

// paginateListCompute paginates using the Compute API page-size parameter name
// ("maxResults"), which the Compute REST API + Go SDK send instead of "pageSize".
func paginateListCompute[T any](w http.ResponseWriter, r *http.Request, items []T, first gcpSortKey[T], rest ...gcpSortKey[T]) ([]T, string, bool) {
	return paginateListParam(w, r, items, "maxResults", append([]gcpSortKey[T]{first}, rest...))
}

// paginateListParam pages items by the size sizeParam names, in the client's
// orderBy terms followed by keys. It pages only when the client names a
// positive size; otherwise it returns everything after the token.
func paginateListParam[T any](w http.ResponseWriter, r *http.Request, items []T, sizeParam string, keys []gcpSortKey[T]) ([]T, string, bool) {
	size, ok := gcpPageSizeParam(w, r, sizeParam)
	if !ok {
		return nil, "", false
	}
	order, err := listq.ParseOrderBy(r.URL.Query().Get("orderBy"), false)
	if err != nil {
		GCPError(w, http.StatusBadRequest, "invalid orderBy: "+err.Error(), "INVALID_ARGUMENT")
		return nil, "", false
	}
	var columns []gcpPageColumn
	if len(order) > 0 {
		docs := make([]listq.Doc, len(items))
		for i, item := range items {
			if docs[i], err = listq.ToDoc(item); err != nil {
				GCPError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
				return nil, "", false
			}
		}
		for _, term := range order {
			path := term.Path
			columns = append(columns, gcpPageColumn{cmp: listq.CompareOrdered, desc: term.Desc, value: func(i int) string {
				value, _ := listq.Field(docs[i], path, ".")
				return value
			}})
		}
	}
	return gcpColumnPage(w, items, append(columns, gcpKeyColumns(items, keys)...), r.URL.Query().Get("pageToken"), size, 0)
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

// gcpPageTag marks a page token as one this simulator issued.
const gcpPageTag = "after\x00"

// gcpKeyPage pages items in the order keys give, capping the page at max when
// max is positive.
func gcpKeyPage[T any](w http.ResponseWriter, items []T, keys []gcpSortKey[T], token string, size, max int) ([]T, string, bool) {
	return gcpColumnPage(w, items, gcpKeyColumns(items, keys), token, size, max)
}

// gcpPageColumn is one term of a list's order over the items by index.
type gcpPageColumn struct {
	value func(i int) string
	cmp   func(a, b string) int
	desc  bool
}

func gcpKeyColumns[T any](items []T, keys []gcpSortKey[T]) []gcpPageColumn {
	columns := make([]gcpPageColumn, len(keys))
	for k, key := range keys {
		columns[k] = gcpPageColumn{cmp: key.cmp, desc: key.desc, value: func(i int) string { return key.value(items[i]) }}
	}
	return columns
}

// gcpColumnPage pages items in the order columns give, capping the page at max
// when max is positive. The token carries the sort values of the last item a
// page returned, and the next page resumes strictly after them, so an item
// created or deleted between two reads moves no other item across a page
// boundary. It answers INVALID_ARGUMENT for a token it never issued.
func gcpColumnPage[T any](w http.ResponseWriter, items []T, columns []gcpPageColumn, token string, size, max int) ([]T, string, bool) {
	if max > 0 && (size == 0 || size > max) {
		size = max
	}
	rows := make([][]string, len(items))
	for i := range items {
		rows[i] = make([]string, len(columns))
		for c, column := range columns {
			rows[i][c] = column.value(i)
		}
	}
	compare := func(a, b []string) int {
		for c, column := range columns {
			order := column.cmp(a[c], b[c])
			if column.desc {
				order = -order
			}
			if order != 0 {
				return order
			}
		}
		return 0
	}
	index := make([]int, len(items))
	for i := range index {
		index[i] = i
	}
	sort.SliceStable(index, func(i, j int) bool { return compare(rows[index[i]], rows[index[j]]) < 0 })
	start := 0
	if token != "" {
		after, ok := gcpPageMarker(token, len(columns))
		if !ok {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageToken %q", token)
			return nil, "", false
		}
		start = sort.Search(len(index), func(i int) bool { return compare(rows[index[i]], after) > 0 })
	}
	end := len(index)
	next := ""
	if size > 0 && start+size < end {
		end = start + size
		encoded, err := json.Marshal(rows[index[end-1]])
		if err != nil {
			GCPError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
			return nil, "", false
		}
		next = base64.RawURLEncoding.EncodeToString(append([]byte(gcpPageTag), encoded...))
	}
	page := make([]T, 0, end-start)
	for _, i := range index[start:end] {
		page = append(page, items[i])
	}
	return page, next, true
}

func gcpPageMarker(token string, terms int) ([]string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, false
	}
	body, ok := strings.CutPrefix(string(decoded), gcpPageTag)
	if !ok {
		return nil, false
	}
	var after []string
	if json.Unmarshal([]byte(body), &after) != nil || len(after) != terms {
		return nil, false
	}
	return after, true
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
