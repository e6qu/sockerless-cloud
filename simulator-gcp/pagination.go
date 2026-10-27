package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// nowTimestamp returns a protobuf JSON Timestamp at millisecond precision,
// one of the 0, 3, 6 or 9 fractional digits protobuf JSON canonicalizes to.
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

// paginateListCompute pages by "maxResults", the page-size parameter Compute
// Engine uses instead of "pageSize".
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
	var columns []listq.Column
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
			columns = append(columns, listq.Column{Compare: listq.CompareOrdered, Desc: term.Desc, Value: func(i int) string {
				value, _ := listq.Field(docs[i], path, ".")
				return value
			}})
		}
	}
	return gcpColumnPage(w, items, append(columns, gcpKeyColumns(items, keys)...), r.URL.Query().Get("pageToken"), size, 0, 0)
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

// gcpKeyPage pages items in the order keys give. A request naming no size gets
// def items; one naming more than max gets max. Zero leaves either unbounded.
func gcpKeyPage[T any](w http.ResponseWriter, items []T, keys []gcpSortKey[T], token string, size, def, max int) ([]T, string, bool) {
	return gcpColumnPage(w, items, gcpKeyColumns(items, keys), token, size, def, max)
}

// gcpKeyPageOf is gcpKeyPage for a caller that answers errors itself, such as
// a gRPC method: it returns listq.ErrBadToken for a token it never issued.
func gcpKeyPageOf[T any](items []T, keys []gcpSortKey[T], token string, size, def, max int) ([]T, string, error) {
	return listq.ColumnPage(items, gcpKeyColumns(items, keys), token, size, def, max)
}

func gcpKeyColumns[T any](items []T, keys []gcpSortKey[T]) []listq.Column {
	columns := make([]listq.Column, len(keys))
	for k, key := range keys {
		columns[k] = listq.Column{Compare: key.cmp, Desc: key.desc, Value: func(i int) string { return key.value(items[i]) }}
	}
	return columns
}

// gcpColumnPage writes INVALID_ARGUMENT for a token listq.ColumnPage refuses.
func gcpColumnPage[T any](w http.ResponseWriter, items []T, columns []listq.Column, token string, size, def, max int) ([]T, string, bool) {
	page, next, err := listq.ColumnPage(items, columns, token, size, def, max)
	switch {
	case errors.Is(err, listq.ErrBadToken):
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageToken %q", token)
		return nil, "", false
	case err != nil:
		GCPError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return nil, "", false
	}
	return page, next, true
}

// grpcKeyPage pages a gRPC list in the order keys give, answering
// INVALID_ARGUMENT for a negative size or a token it never issued.
func grpcKeyPage[T any](items []T, pageSize int32, pageToken string, first gcpSortKey[T], rest ...gcpSortKey[T]) ([]T, string, error) {
	if pageSize < 0 {
		return nil, "", status.Errorf(codes.InvalidArgument, "page_size must not be negative, got %d", pageSize)
	}
	page, next, err := gcpKeyPageOf(items, append([]gcpSortKey[T]{first}, rest...), pageToken, int(pageSize), 0, 0)
	switch {
	case errors.Is(err, listq.ErrBadToken):
		return nil, "", status.Errorf(codes.InvalidArgument, "invalid page_token %q", pageToken)
	case err != nil:
		return nil, "", status.Error(codes.Internal, err.Error())
	}
	return page, next, nil
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

// gcpStandardOperationMetadata is the OperationMetadata the Cloud Functions,
// API Gateway, Eventarc and Memorystore APIs each declare: when the operation
// was created and ended, the method it ran, and the resource it acted on.
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

// gcpProtoList is the resources of store named under prefix, in name order,
// each converted by toProto: a gRPC list the API does not page.
func gcpProtoList[T, P any](store sim.Store[T], prefix string, name func(T) string, toProto func(T) P) []P {
	found := store.Filter(func(v T) bool { return strings.HasPrefix(name(v), prefix) })
	sort.Slice(found, func(i, j int) bool { return name(found[i]) < name(found[j]) })
	out := make([]P, 0, len(found))
	for _, v := range found {
		out = append(out, toProto(v))
	}
	return out
}
