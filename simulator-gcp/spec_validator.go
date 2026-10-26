package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Runtime wire-shape validation against the vendored Discovery documents,
// armed by SOCKERLESS_SPEC_VALIDATE (the report file) with
// SOCKERLESS_SPEC_DIR naming the specs. Every JSON success response matching a
// Discovery method is checked member by member against the response schema;
// spec_conformance_test.go owns route shape.

// discoverySchema is one node of a Discovery document schema: either a
// $ref to a named schema in the same document, or an inline constraint
// (type/format with items / properties / additionalProperties).
type discoverySchema struct {
	ID                   string                      `json:"id"`
	Type                 string                      `json:"type"`
	Format               string                      `json:"format"`
	Ref                  string                      `json:"$ref"`
	Items                *discoverySchema            `json:"items"`
	Properties           map[string]*discoverySchema `json:"properties"`
	AdditionalProperties *discoverySchema            `json:"additionalProperties"`
	Pattern              string                      `json:"pattern"`
	Enum                 []string                    `json:"enum"`

	// compiled memoizes Pattern so the validator does not recompile it on
	// every response; compileOnce guards it.
	compiled    *regexp.Regexp
	compileOnce sync.Once
}

// discoveryEnumIsExhaustive reports whether a field's enum lists every value
// the service uses. Cloud Run's condition reasons do not: gcloud's
// cancellation poller compares reason to "Cancelled" and "Stopped", which the
// document omits, so that field is not judged.
func discoveryEnumIsExhaustive(fieldPath string) bool {
	for _, incomplete := range []string{
		".reason", ".executionReason", ".revisionReason",
	} {
		if strings.HasSuffix(fieldPath, incomplete) {
			return false
		}
	}
	return true
}

// patternRE returns the compiled, anchored form of a schema's pattern, or nil
// when it declares none or cannot be compiled. Discovery patterns are written
// unanchored but describe a whole value (a resource name, a fingerprint, an
// address), so they are anchored here, unlike Smithy patterns, which anchor
// themselves when they mean to.
func (sch *discoverySchema) patternRE() *regexp.Regexp {
	sch.compileOnce.Do(func() {
		if sch.Pattern == "" {
			return
		}
		sch.compiled, _ = regexp.Compile(`^(?:` + sch.Pattern + `)$`)
	})
	return sch.compiled
}

// discoverySpecDoc carries one document's schema namespace; $refs resolve
// within the owning document only.
type discoverySpecDoc struct {
	file    string
	schemas map[string]*discoverySchema
}

// specSeg is one segment of a Discovery method URI template.
//   - literal: the request segment must be identical;
//   - param ({foo}, optionally with literal prefix/suffix in the same
//     segment, e.g. "{jobsId}:run"): exactly one request segment;
//   - greedy ({+foo}, reserved expansion): one or more request segments,
//     with any literal suffix anchored to the last consumed segment.
type specSeg struct {
	literal string
	isParam bool
	greedy  bool
	prefix  string
	suffix  string
}

// specTemplate is one URI spelling of a method (flatPath, path, media
// variants); score counts literal characters, and the most literal match wins.
type specTemplate struct {
	segs []specSeg
	// mediaDownload marks the alt=media /download service-path variant:
	// the response is the object payload, not the resource JSON.
	mediaDownload bool
	score         int
}

// discoverySpecMethod is one REST method from a Discovery document.
type discoverySpecMethod struct {
	httpMethod  string
	op          string // "METHOD <basePath-joined flatPath>" — stable violation Op
	responseRef string // response schema name; "" when the method has no response body
	doc         *discoverySpecDoc
	templates   []specTemplate
}

// discoverySpecMountPrefixes maps mount prefixes to the document they
// disambiguate, as gcpMountPrefixes does: a request under a prefix matches
// only that document's methods, after the prefix is stripped.
var discoverySpecMountPrefixes = map[string]string{
	"/spanner": "spanner-v1.discovery.json.gz",
}

// discoveryIndex is the request-time matcher over every vendored method.
type discoveryIndex struct {
	defaultMethods []*discoverySpecMethod
	mounted        map[string][]*discoverySpecMethod
}

// applyDiscoverySupplement merges a document's supplement into the schemas the
// validator checks. A supplement declares members the API serves but its
// document omits, with the evidence beside each, so they stay validated rather
// than allowlisted. It may only add: supplementing a member the document
// declares is refused, so a stale entry fails once Google publishes it.
func applyDiscoverySupplement(dir string, doc *discoverySpecDoc) error {
	path := filepath.Join(dir, strings.TrimSuffix(doc.file, ".discovery.json.gz")+".supplement.json")
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var supplement struct {
		Schemas map[string]struct {
			Properties map[string]*discoverySchema `json:"properties"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(body, &supplement); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for name, extra := range supplement.Schemas {
		sch, ok := doc.schemas[name]
		if !ok {
			return fmt.Errorf("%s: supplements schema %q, which %s does not define", path, name, doc.file)
		}
		if sch.Properties == nil {
			return fmt.Errorf("%s: supplements schema %q, which declares no members of its own — an opaque object needs no supplement", path, name)
		}
		for member, declared := range extra.Properties {
			if _, published := sch.Properties[member]; published {
				return fmt.Errorf("%s: supplements %s.%s, which %s now declares — delete the supplement entry",
					path, name, member, doc.file)
			}
			sch.Properties[member] = declared
		}
	}
	return nil
}

func loadDiscoveryIndex(dir string) (*discoveryIndex, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.discovery.json.gz"))
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("no Discovery documents under %s (glob err: %v)", dir, err)
	}
	mountByFile := map[string]string{}
	for prefix, file := range discoverySpecMountPrefixes {
		mountByFile[file] = prefix
	}

	type rawMethod struct {
		HTTPMethod              string           `json:"httpMethod"`
		Path                    string           `json:"path"`
		FlatPath                string           `json:"flatPath"`
		UseMediaDownloadService bool             `json:"useMediaDownloadService"`
		Response                *discoverySchema `json:"response"`
		MediaUpload             *struct {
			Protocols struct {
				Simple struct {
					Path string `json:"path"`
				} `json:"simple"`
				Resumable struct {
					Path string `json:"path"`
				} `json:"resumable"`
			} `json:"protocols"`
		} `json:"mediaUpload"`
	}
	type rawResource struct {
		Methods   map[string]rawMethod   `json:"methods"`
		Resources map[string]rawResource `json:"resources"`
	}

	idx := &discoveryIndex{mounted: map[string][]*discoverySpecMethod{}}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		var raw struct {
			BasePath  string                      `json:"basePath"`
			Schemas   map[string]*discoverySchema `json:"schemas"`
			Methods   map[string]rawMethod        `json:"methods"`
			Resources map[string]rawResource      `json:"resources"`
		}
		err = json.NewDecoder(gz).Decode(&raw)
		_ = gz.Close()
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		doc := &discoverySpecDoc{file: filepath.Base(p), schemas: raw.Schemas}
		if err := applyDiscoverySupplement(dir, doc); err != nil {
			return nil, err
		}
		for name, sch := range raw.Schemas {
			if err := verifySchemaRefs(doc, sch); err != nil {
				return nil, fmt.Errorf("%s: schema %s: %w", p, name, err)
			}
		}

		join := func(rel string) string {
			return "/" + strings.TrimPrefix(strings.TrimSuffix(raw.BasePath, "/")+"/"+strings.TrimPrefix(rel, "/"), "/")
		}
		var methods []*discoverySpecMethod
		addMethod := func(m rawMethod) error {
			if m.HTTPMethod == "" {
				return nil
			}
			canonical := m.FlatPath
			if canonical == "" {
				canonical = m.Path
			}
			if canonical == "" {
				return nil
			}
			sm := &discoverySpecMethod{
				httpMethod: m.HTTPMethod,
				op:         m.HTTPMethod + " " + join(canonical),
				doc:        doc,
			}
			if m.Response != nil {
				if _, ok := doc.schemas[m.Response.Ref]; !ok {
					return fmt.Errorf("%s: method %s: response $ref %q not in schemas", doc.file, sm.op, m.Response.Ref)
				}
				sm.responseRef = m.Response.Ref
			}
			seen := map[string]bool{}
			addTemplate := func(joined string, mediaDownload bool) {
				if seen[joined] {
					return
				}
				seen[joined] = true
				segs := splitSpecSegs(joined)
				sm.templates = append(sm.templates, specTemplate{
					segs:          segs,
					mediaDownload: mediaDownload,
					score:         templateScore(segs),
				})
			}
			// The flatPath and {+param} path are equivalent spellings, but a param segment
			// with no literal prefix or suffix consumes any request segment, so such a path
			// can match unrelated requests (cloudbilling's bare "v1/{+parent}" matched
			// POST /v1/token). flatPath is always at least as discriminating, so an
			// unconstrained-param path is kept only when it is the method's sole spelling.
			rels := []string{m.FlatPath, m.Path}
			if m.Path != "" && m.FlatPath != "" && m.Path != m.FlatPath && hasUnconstrainedParam(splitSpecSegs(m.Path)) {
				rels = []string{m.FlatPath}
			}
			for _, rel := range rels {
				if rel == "" {
					continue
				}
				addTemplate(join(rel), false)
				// Media-download variant: alt=media requests ride the
				// /download-prefixed service path.
				if m.UseMediaDownloadService {
					addTemplate("/download"+join(rel), true)
				}
			}
			// Each media-upload protocol rides its own absolute path
			// (/upload/storage/v1/b/{bucket}/o, /resumable/upload/...); its
			// response is the resource JSON, validated like any other.
			if m.MediaUpload != nil {
				for _, path := range []string{m.MediaUpload.Protocols.Simple.Path, m.MediaUpload.Protocols.Resumable.Path} {
					if path != "" {
						addTemplate("/"+strings.TrimPrefix(path, "/"), false)
					}
				}
			}
			methods = append(methods, sm)
			return nil
		}
		var walkErr error
		var walk func(res rawResource)
		walk = func(res rawResource) {
			for _, m := range res.Methods {
				if err := addMethod(m); err != nil && walkErr == nil {
					walkErr = err
				}
			}
			for _, sub := range res.Resources {
				walk(sub)
			}
		}
		for _, m := range raw.Methods {
			if err := addMethod(m); err != nil && walkErr == nil {
				walkErr = err
			}
		}
		for _, res := range raw.Resources {
			walk(res)
		}
		if walkErr != nil {
			return nil, walkErr
		}
		if len(methods) == 0 {
			return nil, fmt.Errorf("%s: no REST methods found", p)
		}
		if prefix, ok := mountByFile[doc.file]; ok {
			idx.mounted[prefix] = append(idx.mounted[prefix], methods...)
		} else {
			idx.defaultMethods = append(idx.defaultMethods, methods...)
		}
	}
	// Deterministic tie-break when several equally-literal templates
	// match a request: first method in op order wins.
	sortMethods := func(ms []*discoverySpecMethod) {
		sort.Slice(ms, func(i, j int) bool { return ms[i].op < ms[j].op })
	}
	sortMethods(idx.defaultMethods)
	for _, ms := range idx.mounted {
		sortMethods(ms)
	}
	return idx, nil
}

// verifySchemaRefs walks a schema definition and confirms every nested
// $ref resolves within the owning document, so request-time validation
// never has to skip an unresolved reference.
func verifySchemaRefs(doc *discoverySpecDoc, sch *discoverySchema) error {
	if sch == nil {
		return nil
	}
	if sch.Ref != "" {
		if _, ok := doc.schemas[sch.Ref]; !ok {
			return fmt.Errorf("$ref %q not in schemas", sch.Ref)
		}
	}
	if err := verifySchemaRefs(doc, sch.Items); err != nil {
		return err
	}
	if err := verifySchemaRefs(doc, sch.AdditionalProperties); err != nil {
		return err
	}
	for _, p := range sch.Properties {
		if err := verifySchemaRefs(doc, p); err != nil {
			return err
		}
	}
	return nil
}

func splitSpecSegs(p string) []specSeg {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	segs := make([]specSeg, 0, len(parts))
	for _, part := range parts {
		i := strings.Index(part, "{")
		j := strings.Index(part, "}")
		if i < 0 || j < i {
			segs = append(segs, specSeg{literal: part})
			continue
		}
		inner := part[i+1 : j]
		segs = append(segs, specSeg{
			isParam: true,
			greedy:  strings.HasPrefix(inner, "+"),
			prefix:  part[:i],
			suffix:  part[j+1:],
		})
	}
	return segs
}

// hasUnconstrainedParam reports whether segs holds a param segment with no
// literal prefix or suffix, which consumes any request segment whatever other
// literals the template has.
func hasUnconstrainedParam(segs []specSeg) bool {
	for _, s := range segs {
		if s.isParam && s.prefix == "" && s.suffix == "" {
			return true
		}
	}
	return false
}

// templateScore counts literal characters: when several templates match
// the same request, the most literal spelling identifies the method.
func templateScore(segs []specSeg) int {
	score := 0
	for _, s := range segs {
		if s.isParam {
			score += len(s.prefix) + len(s.suffix)
		} else {
			score += len(s.literal)
		}
	}
	return score
}

// matchSpecSegs reports whether the request segments spell the template.
// Greedy params consume one or more segments, anchoring a literal suffix on the
// last. A ":" in the request marks an AIP-136 custom method that only a template
// spelling the verb may consume, so ":listCollectionIds" never keys to a
// sibling method's plain {param}.
func matchSpecSegs(req []string, tmpl []specSeg) bool {
	if len(tmpl) == 0 {
		return len(req) == 0
	}
	if len(req) == 0 {
		return false
	}
	s := tmpl[0]
	if !s.isParam {
		if req[0] != s.literal {
			return false
		}
		return matchSpecSegs(req[1:], tmpl[1:])
	}
	if !s.greedy {
		if !paramSegMatch(req[0], s.prefix, s.suffix) {
			return false
		}
		return matchSpecSegs(req[1:], tmpl[1:])
	}
	if s.prefix != "" && !strings.HasPrefix(req[0], s.prefix) {
		return false
	}
	for i := 1; i <= len(req); i++ {
		last := req[i-1]
		if s.suffix != "" && !strings.HasSuffix(last, s.suffix) {
			continue
		}
		if strings.Contains(strings.TrimSuffix(last, s.suffix), ":") {
			continue // an unconsumed custom-method verb remains
		}
		if matchSpecSegs(req[i:], tmpl[1:]) {
			return true
		}
	}
	return false
}

func paramSegMatch(seg, prefix, suffix string) bool {
	if len(seg) <= len(prefix)+len(suffix) ||
		!strings.HasPrefix(seg, prefix) ||
		!strings.HasSuffix(seg, suffix) {
		return false
	}
	// The parameter value itself never carries a custom-method verb.
	return !strings.Contains(seg[len(prefix):len(seg)-len(suffix)], ":")
}

// specMatch is one Discovery method that describes a request at the
// best (most literal) specificity observed for that request.
type specMatch struct {
	method *discoverySpecMethod
	tmpl   *specTemplate
}

// match resolves a request to the most specific Discovery methods. Services
// Google separates by host can describe one URI (Eventarc and Cloud Build both
// own "v1/projects/{p}/locations/{l}/triggers"), so every equally specific
// candidate is returned, and the response conforms when it satisfies any.
// A path under a mount prefix matches only that document.
func (idx *discoveryIndex) match(httpMethod, escapedPath string) []specMatch {
	methods := idx.defaultMethods
	for prefix, ms := range idx.mounted {
		if strings.HasPrefix(escapedPath, prefix+"/") {
			methods = ms
			escapedPath = strings.TrimPrefix(escapedPath, prefix)
			break
		}
	}
	// Split the escaped path: percent-encoded separators inside a path
	// parameter (GCS object names) must stay one segment, exactly as the
	// mux saw them.
	var req []string
	if trimmed := strings.Trim(escapedPath, "/"); trimmed != "" {
		req = strings.Split(trimmed, "/")
	}
	var (
		best      []specMatch
		bestScore = -1
	)
	for _, m := range methods {
		if m.httpMethod != httpMethod {
			continue
		}
		for i := range m.templates {
			t := &m.templates[i]
			if t.score < bestScore {
				continue
			}
			if !matchSpecSegs(req, t.segs) {
				continue
			}
			if t.score > bestScore {
				best, bestScore = best[:0], t.score
			} else if len(best) > 0 && best[len(best)-1].method == m {
				continue // same method already matched via an equivalent spelling
			}
			best = append(best, specMatch{method: m, tmpl: t})
		}
	}
	return best
}

// armSpecValidator wires runtime validation onto the server when
// SOCKERLESS_SPEC_VALIDATE is set, failing hard when the spec directory is
// missing.
func armSpecValidator(srv *sim.Server) error {
	if os.Getenv("SOCKERLESS_SPEC_VALIDATE") == "" {
		return nil
	}
	dir := os.Getenv("SOCKERLESS_SPEC_DIR")
	if dir == "" {
		return fmt.Errorf("SOCKERLESS_SPEC_VALIDATE is set but SOCKERLESS_SPEC_DIR is not")
	}
	idx, err := loadDiscoveryIndex(dir)
	if err != nil {
		return err
	}
	srv.SetSpecValidator(func(req *http.Request, _ []byte, status int, respHeader http.Header, respBody []byte) []sim.SpecViolation {
		if status >= 400 || len(respBody) == 0 {
			return nil // error/empty responses carry no resource shape
		}
		ct := respHeader.Get("Content-Type")
		if ct != "" && !strings.Contains(ct, "json") {
			return nil // non-JSON payload (media, OCI blobs, ...)
		}
		// The Docker/OCI data plane is no Discovery surface, and Cloud Logging's
		// `GET v2/{+name}` matches any /v2/ path; skip registry paths with the
		// predicate the bearer middleware uses.
		if isOCIRegistryPath(req.URL.EscapedPath()) {
			return nil
		}
		matches := idx.match(req.Method, req.URL.EscapedPath())
		if len(matches) == 0 {
			return nil // not a Discovery method: the static conformance gate owns route shape
		}
		if req.URL.Query().Get("alt") == "media" {
			return nil // media download: the body is the object payload, not the resource JSON
		}
		for _, c := range matches {
			if c.tmpl.mediaDownload {
				return nil
			}
		}
		var body any
		streaming := false
		for _, c := range matches {
			streaming = streaming || isStreamingResponseOp(c.method.op)
		}
		trimmed := bytes.TrimLeft(respBody, " \t\r\n")
		if streaming && (len(trimmed) == 0 || trimmed[0] == '[') {
			elements, err := decodeStreamElements(respBody)
			if err != nil {
				return []sim.SpecViolation{{Op: matches[0].method.op, Kind: "malformed-json", Field: "$", Detail: err.Error()}}
			}
			body = elements
		} else if err := json.Unmarshal(respBody, &body); err != nil {
			return []sim.SpecViolation{{Op: matches[0].method.op, Kind: "malformed-json", Field: "$", Detail: err.Error()}}
		}
		// The response conforms when it satisfies any equally specific candidate;
		// otherwise report the closest one's divergences.
		var closest []sim.SpecViolation
		for _, c := range matches {
			if c.method.responseRef == "" {
				// A tied method without a response schema constrains
				// nothing, so the exchange cannot be called divergent.
				return nil
			}
			var out []sim.SpecViolation
			if arr, ok := body.([]any); ok && isStreamingResponseOp(c.method.op) {
				// A server-streaming REST method (Firestore runQuery, batchGet,
				// runAggregationQuery) answers a JSON array whose elements the Discovery
				// schema describes, so validate each element.
				for i, el := range arr {
					validateDiscoveryValue(c.method.doc, c.method.op, &discoverySchema{Ref: c.method.responseRef}, c.method.responseRef, fmt.Sprintf("$[%d]", i), el, &out)
				}
			} else {
				validateDiscoveryValue(c.method.doc, c.method.op, &discoverySchema{Ref: c.method.responseRef}, c.method.responseRef, "$", body, &out)
			}
			if len(out) == 0 {
				return nil
			}
			if closest == nil || len(out) < len(closest) {
				closest = out
			}
		}
		return closest
	})
	return nil
}

// decodeStreamElements reads a server-streaming REST body, a JSON array whose
// elements the server writes as they occur. A client that stops reading ends
// the stream mid-array, so the elements read before the input ran out are the
// whole response; any other decoding error is a malformed stream.
func decodeStreamElements(respBody []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(respBody))
	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return []any{}, nil
		}
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return nil, fmt.Errorf("stream body starts with %v, not an array", tok)
	}
	elements := []any{}
	for dec.More() {
		var el any
		if err := dec.Decode(&el); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return elements, nil
			}
			return nil, err
		}
		elements = append(elements, el)
	}
	return elements, nil
}

// isStreamingResponseOp reports whether op is a server-streaming REST method:
// Firestore's queries and batchGet, Cloud Logging's entries.tail.
func isStreamingResponseOp(op string) bool {
	return strings.HasSuffix(op, ":runQuery") ||
		strings.HasSuffix(op, ":batchGet") ||
		strings.HasSuffix(op, ":runAggregationQuery") ||
		strings.HasSuffix(op, "entries:tail")
}

// validateDiscoveryValue walks a decoded JSON value against a Discovery schema,
// reporting undefined members and type mismatches. null is always acceptable,
// "any" accepts everything, int64 and uint64 formats must be JSON strings, and
// additionalProperties marks a map whose values are validated and whose keys
// are never unknown.
func validateDiscoveryValue(doc *discoverySpecDoc, op string, sch *discoverySchema, owner, path string, v any, out *[]sim.SpecViolation) {
	if v == nil {
		return
	}
	if sch.Ref != "" {
		// Named schemas never alias another $ref at their root, so a
		// single dereference resolves; loadDiscoveryIndex verified it.
		owner = sch.Ref
		sch = doc.schemas[sch.Ref]
	}
	mismatch := func(want string) {
		*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path,
			Detail: fmt.Sprintf("spec (%s) declares %s, response has %T", owner, want, v)})
	}
	switch sch.Type {
	case "any":
		// Any JSON value.
	case "object", "":
		// A typeless node constrains only its declared properties.
		obj, ok := v.(map[string]any)
		if !ok {
			if sch.Type == "object" {
				mismatch("an object")
			}
			return
		}
		for key, val := range obj {
			if p, ok := sch.Properties[key]; ok {
				validateDiscoveryValue(doc, op, p, owner, path+"."+key, val, out)
				continue
			}
			if sch.AdditionalProperties != nil {
				// A map: arbitrary keys, validated values.
				validateDiscoveryValue(doc, op, sch.AdditionalProperties, owner, path+"."+key, val, out)
				continue
			}
			if sch.Properties == nil {
				continue // opaque object: no declared member set to check against
			}
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "unknown-field", Field: path + "." + key,
				Detail: "member not defined by " + owner})
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			mismatch("an array")
			return
		}
		if sch.Items != nil {
			for i, item := range arr {
				validateDiscoveryValue(doc, op, sch.Items, owner, fmt.Sprintf("%s[%d]", path, i), item, out)
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			want := "a string"
			if sch.Format != "" {
				want = fmt.Sprintf("a string (format %s)", sch.Format)
			}
			mismatch(want)
			return
		}
		// A declared pattern pins the identity-bearing strings: a type check alone
		// accepts a value no client could send back.
		if re := sch.patternRE(); re != nil && !re.MatchString(str) {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "pattern-mismatch", Field: path,
				Detail: fmt.Sprintf("spec (%s) requires %s, response has %q", owner, sch.Pattern, str)})
		}
		// Discovery lists every enum value the service uses, *_UNSPECIFIED included,
		// so a value outside the set is invented; the type check cannot see it.
		if len(sch.Enum) > 0 && discoveryEnumIsExhaustive(path) && !slices.Contains(sch.Enum, str) {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "enum-mismatch", Field: path,
				Detail: fmt.Sprintf("spec (%s) declares %s, response has %q",
					owner, strings.Join(sch.Enum, "|"), str)})
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			mismatch("a boolean")
		}
	case "integer", "number":
		if _, ok := v.(float64); !ok {
			mismatch(fmt.Sprintf("a JSON number (%s)", sch.Type))
		}
	}
}
