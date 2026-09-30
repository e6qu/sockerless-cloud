package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// gcsCustomContext is one user-defined object context as the store keeps it.
type gcsCustomContext struct {
	Value      string `json:"value"`
	CreateTime string `json:"createTime,omitempty"`
	UpdateTime string `json:"updateTime,omitempty"`
}

// gcsContextsInput is the contexts member of an object resource a request
// carries. A nil entry in custom removes that key.
type gcsContextsInput struct {
	present bool
	// clearAll is set by `"contexts": null` and `"custom": null`.
	clearAll bool
	custom   map[string]*string
}

func (in *gcsContextsInput) UnmarshalJSON(b []byte) error {
	in.present = true
	if string(b) == "null" {
		in.clearAll = true
		return nil
	}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(b, &outer); err != nil {
		return fmt.Errorf("contexts: %w", err)
	}
	raw, ok := outer["custom"]
	if !ok {
		return nil
	}
	if string(raw) == "null" {
		in.clearAll = true
		return nil
	}
	var payloads map[string]*struct {
		Value *string `json:"value"`
	}
	if err := json.Unmarshal(raw, &payloads); err != nil {
		return fmt.Errorf("contexts.custom: %w", err)
	}
	in.custom = make(map[string]*string, len(payloads))
	for key, payload := range payloads {
		// The Go client deletes a key by sending its payload without a value.
		if payload == nil || payload.Value == nil {
			in.custom[key] = nil
			continue
		}
		value := *payload.Value
		in.custom[key] = &value
	}
	return nil
}

// replacement is the context set the input defines on its own, as objects.insert
// and objects.update take it. A key the object already held keeps its
// createTime; now stamps every key written.
func (in gcsContextsInput) replacement(existing map[string]gcsCustomContext, now string) map[string]gcsCustomContext {
	if in.clearAll {
		return nil
	}
	out := map[string]gcsCustomContext{}
	for key, value := range in.custom {
		if value == nil {
			continue
		}
		out[key] = gcsWrittenContext(existing, key, *value, now)
	}
	return nonEmptyContexts(out)
}

// merged is existing with the input's keys written over it, as objects.patch
// takes it.
func (in gcsContextsInput) merged(existing map[string]gcsCustomContext, now string) map[string]gcsCustomContext {
	if in.clearAll {
		return nil
	}
	out := make(map[string]gcsCustomContext, len(existing)+len(in.custom))
	for key, ctx := range existing {
		out[key] = ctx
	}
	for key, value := range in.custom {
		if value == nil {
			delete(out, key)
			continue
		}
		out[key] = gcsWrittenContext(existing, key, *value, now)
	}
	return nonEmptyContexts(out)
}

func gcsWrittenContext(existing map[string]gcsCustomContext, key, value, now string) gcsCustomContext {
	created := now
	if prior, ok := existing[key]; ok && prior.CreateTime != "" {
		created = prior.CreateTime
	}
	return gcsCustomContext{Value: value, CreateTime: created, UpdateTime: now}
}

func nonEmptyContexts(in map[string]gcsCustomContext) map[string]gcsCustomContext {
	if len(in) == 0 {
		return nil
	}
	return in
}

// gcsFreshContexts copies contexts onto a new object with their timestamps
// cleared, so the write that creates the object stamps them.
func gcsFreshContexts(in map[string]gcsCustomContext) map[string]gcsCustomContext {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]gcsCustomContext, len(in))
	for key, ctx := range in {
		out[key] = gcsCustomContext{Value: ctx.Value}
	}
	return out
}

// gcsStampContexts fills the timestamps a write left for its own time.
func gcsStampContexts(in map[string]gcsCustomContext, now string) map[string]gcsCustomContext {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]gcsCustomContext, len(in))
	for key, ctx := range in {
		if ctx.CreateTime == "" {
			ctx.CreateTime = now
		}
		if ctx.UpdateTime == "" {
			ctx.UpdateTime = now
		}
		out[key] = ctx
	}
	return out
}

func gcsContextsResource(in map[string]gcsCustomContext) map[string]any {
	custom := make(map[string]any, len(in))
	for key, ctx := range in {
		custom[key] = map[string]any{
			"value":      ctx.Value,
			"createTime": ctx.CreateTime,
			"updateTime": ctx.UpdateTime,
		}
	}
	return map[string]any{"custom": custom}
}

// gcsDestinationContexts decides the contexts a copy, rewrite or compose
// gives its destination: the request body's contexts override the sources',
// and without them the sources' carry over unless dropContextGroups names
// the custom group.
func gcsDestinationContexts(r *http.Request, body *gcsObjectResource, sources ...GCSObject) (map[string]gcsCustomContext, error) {
	drop := false
	for _, group := range r.URL.Query()["dropContextGroups"] {
		if group != "custom" {
			return nil, fmt.Errorf("invalid dropContextGroups value %q: the accepted value is 'custom'", group)
		}
		drop = true
	}
	if body != nil && body.Contexts.present {
		return body.Contexts.replacement(nil, ""), nil
	}
	if drop {
		return nil, nil
	}
	carried := map[string]gcsCustomContext{}
	for _, src := range sources {
		for key, ctx := range src.CustomContexts {
			carried[key] = ctx
		}
	}
	return gcsFreshContexts(carried), nil
}

// gcsContextFilterTerm matches an object holding the context key, with value
// when value is set.
type gcsContextFilterTerm struct {
	key   string
	value *string
}

func (t gcsContextFilterTerm) Eval(d listq.Doc) bool {
	contexts, _ := d["contexts"].(map[string]gcsCustomContext)
	ctx, ok := contexts[t.key]
	if !ok {
		return false
	}
	return t.value == nil || ctx.Value == *t.value
}

// gcsParseContextsFilter reads objects.list's filter, which Cloud Storage
// supports only over the contexts field: `contexts."KEY"="VALUE"` and
// `contexts."KEY":*`, each negatable and combinable under the AIP-160 grammar.
func gcsParseContextsFilter(s string) (listq.Node, error) {
	return gcpParseFilter(s, func(field, op, value string) (listq.Node, error) {
		rest, ok := strings.CutPrefix(field, "contexts.")
		if !ok {
			return nil, fmt.Errorf("filtering is supported only on the contexts field, not %q", field)
		}
		key := rest
		if strings.HasPrefix(rest, `"`) {
			unquoted, err := strconv.Unquote(rest)
			if err != nil {
				return nil, fmt.Errorf("malformed context key %s", rest)
			}
			key = unquoted
		}
		if key == "" {
			return nil, fmt.Errorf("a contexts filter names a key: %q", field)
		}
		switch {
		case op == ":" && value == "*":
			return gcsContextFilterTerm{key: key}, nil
		case op == "=":
			v := value
			return gcsContextFilterTerm{key: key, value: &v}, nil
		}
		return nil, fmt.Errorf("unsupported contexts filter operator %q", op+value)
	})
}

func gcsContextsFilterDoc(obj GCSObject) listq.Doc {
	return listq.Doc{"contexts": obj.CustomContexts}
}

// registerGCSObjectContexts serves objects.viewFullContext. The single-segment
// {object} beats the `{object...}` catch-all serving objects.get.
func registerGCSObjectContexts(srv *sim.Server, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject]) {
	srv.HandleFunc("GET /storage/v1/b/{bucket}/o/{object}/viewFullContext", func(w http.ResponseWriter, r *http.Request) {
		bucket, object := sim.PathParam(r, "bucket"), sim.PathParam(r, "object")
		key := r.URL.Query().Get("contextKey")
		if key == "" {
			GCPError(w, http.StatusBadRequest, "Required parameter: contextKey", "INVALID_ARGUMENT")
			return
		}
		if _, found := buckets.Get(bucket); !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return
		}
		obj, found := objects.Get(bucket + "/" + object)
		if generation := r.URL.Query().Get("generation"); found && generation != "" && generation != obj.Generation {
			found = false
		}
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", object, bucket)
			return
		}
		ctx, found := obj.CustomContexts[key]
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q has no context %q", object, key)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":       "storage#objectFullContext",
			"type":       "CUSTOM",
			"key":        key,
			"value":      ctx.Value,
			"createTime": ctx.CreateTime,
			"updateTime": ctx.UpdateTime,
		})
	})
}
