package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Runtime wire-shape validation against the vendored Discovery documents
// (specs/cloud-api/gcp/). Armed when SOCKERLESS_SPEC_VALIDATE (report
// file) is set; SOCKERLESS_SPEC_DIR must then point at the vendored spec
// directory. Every JSON success response whose method+path matches a
// Discovery REST method is validated member-by-member against the
// method's response schema: members the schema doesn't define and JSON
// type mismatches are violations. Exchanges that match no Discovery
// method are skipped — the static conformance gate
// (spec_conformance_test.go) owns route-shape fidelity.

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

// discoveryEnumIsExhaustive reports whether a field's declared enum lists every
// value the service uses. Most do. Cloud Run's condition reasons do not, and
// the evidence is the real client: gcloud's cancellation poller reads
// condition["reason"] and compares it to the literal "Cancelled" — and
// "Stopped" for a stop — neither of which the document lists. Judging that
// field against the document would fail a response the client requires, so it
// is left unjudged rather than judged wrongly.
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

// patternRE returns the compiled form of a schema's declared pattern, or nil
// when it declares none or the expression cannot be compiled — the validator
// refuses to judge what it cannot read.
//
// Discovery patterns are written unanchored but mean a whole value: every one
// in the vendored corpus is a format for the entire field — a Compute resource
// name, a forty-character fingerprint, an IPv4 address, a tagValues id — and
// as a substring search an address pattern would admit any text that merely
// contains an address. So the expression is anchored here. This is the one
// place Discovery and Smithy differ: a Smithy pattern anchors itself when it
// means to, and is matched as written.
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

// specTemplate is one compiled URI spelling of a method (flatPath, path,
// and media-download/upload variants are all real spellings). score
// counts literal characters — when several templates match a request,
// the most literal (most specific) one wins.
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

// discoverySpecMountPrefixes maps simulator-local mount prefixes to the
// Discovery document they disambiguate (same surface as gcpMountPrefixes
// in spec_conformance_test.go): the collapsed single-port simulator
// serves Spanner under /spanner so its paths don't collide with Cloud
// SQL's. Requests under a mount prefix match only that document's
// methods (after stripping the prefix); other requests never match it.
var discoverySpecMountPrefixes = map[string]string{
	"/spanner": "spanner-v1.discovery.json.gz",
}

// discoveryIndex is the request-time matcher over every vendored method.
type discoveryIndex struct {
	defaultMethods []*discoverySpecMethod
	mounted        map[string][]*discoverySpecMethod
}

// applyDiscoverySupplement merges a document's supplement, if it has one, into
// the schemas the validator checks against.
//
// A supplement declares members the API serves that its published Discovery
// document does not, each with the evidence for it recorded beside it in the
// file. Declaring them is what keeps them validated: the alternative — listing
// each as an accepted violation — tolerates any value at all for the member,
// including one the real API would never return.
//
// A supplement may only ADD. Supplementing a member the document already
// declares is refused, because that would let a stale supplement quietly
// override the published truth; when Google publishes a member, the supplement
// entry has to go, and this is what makes it go loudly.
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
			// Both the expanded flatPath and the {+param} template path
			// are real, equivalent spellings of the method URI.
			for _, rel := range []string{m.FlatPath, m.Path} {
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
			// Media-upload variant rides its own absolute path
			// (/upload/storage/v1/b/{bucket}/o); its response is the
			// resource JSON, validated like any other.
			if m.MediaUpload != nil && m.MediaUpload.Protocols.Simple.Path != "" {
				addTemplate("/"+strings.TrimPrefix(m.MediaUpload.Protocols.Simple.Path, "/"), false)
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

// splitSpecSegs compiles a joined method URI into per-segment matchers.
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

// matchSpecSegs reports whether the request path segments are one
// spelling of the template. Greedy params consume one or more segments,
// anchoring any literal suffix on the last consumed segment. A ":" in
// the request marks a Discovery custom method (AIP-136 verb): only a
// template that spells the verb literally may consume it — otherwise
// ":listCollectionIds" requests would key to the sibling method whose
// plain {param} sits in that position.
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

// match resolves a request to the most specific Discovery methods.
// Several methods can tie: the collapsed single-port simulator serves
// services that real GCP separates by hostname, and distinct services
// describe identical URIs (eventarc and cloudbuild both own
// "v1/projects/{p}/locations/{l}/triggers"). All equally-specific
// candidates are returned; the response conforms when it satisfies any
// of them. Paths under a simulator mount prefix match only the mounted
// document, mirroring the simulator's routing.
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

// armSpecValidator wires runtime shape validation onto the server when
// SOCKERLESS_SPEC_VALIDATE is set. Hard failure when the spec dir is
// missing: the operator asked for validation.
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
		// The Docker/OCI distribution data plane is not a Discovery surface, and
		// Cloud Logging v2's `GET v2/{+name}` template matches any /v2/ path, so
		// an unskipped registry response would be validated against a
		// LogExclusion. The same predicate the bearer middleware uses to exempt
		// the registry decides it here, so the two cannot drift apart.
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
		if err := json.Unmarshal(respBody, &body); err != nil {
			return []sim.SpecViolation{{Op: matches[0].method.op, Kind: "malformed-json", Field: "$", Detail: err.Error()}}
		}
		// The response conforms when it satisfies ANY equally-specific
		// candidate; otherwise report the divergences of the closest one
		// (fewest violations — the method the handler most plausibly
		// implements).
		var closest []sim.SpecViolation
		for _, c := range matches {
			if c.method.responseRef == "" {
				// A tied method without a response schema constrains
				// nothing, so the exchange cannot be called divergent.
				return nil
			}
			var out []sim.SpecViolation
			if arr, ok := body.([]any); ok && isStreamingResponseOp(c.method.op) {
				// Server-streaming REST method (Firestore runQuery / batchGet /
				// runAggregationQuery): the wire body is a JSON array of stream
				// elements — real GCP responds identically — while the Discovery
				// schema describes a single element. Validate each element against
				// the element schema rather than the whole array.
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

// isStreamingResponseOp reports whether op is a Firestore server-streaming REST
// method. These return a JSON array of stream elements on the wire (matching
// real GCP); each element conforms to the Discovery response schema, so the
// validator checks elements individually rather than rejecting the array.
func isStreamingResponseOp(op string) bool {
	return strings.HasSuffix(op, ":runQuery") ||
		strings.HasSuffix(op, ":batchGet") ||
		strings.HasSuffix(op, ":runAggregationQuery")
}

// validateDiscoveryValue walks a decoded JSON value against a Discovery
// schema, reporting members the schema doesn't define and JSON type
// mismatches. owner names the nearest $ref-resolved schema for messages.
// Rules: null is always acceptable (omitted member); "any" accepts every
// value; int64/uint64-formatted fields are declared "type": "string" and
// must be JSON strings; additionalProperties marks a map — values are
// validated against it and keys are never unknown-field violations.
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
		// any JSON value
	case "object", "":
		// A node with no type and no $ref constrains nothing beyond its
		// declared properties (Discovery emits "object" everywhere, but
		// stay permissive for typeless nodes).
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
				// map schema: arbitrary keys, value-validated
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
		// A declared pattern is part of the contract, and it is where the
		// identity-bearing strings are pinned — a resource name, a fingerprint,
		// an address. Checking the type alone accepts a value no client could
		// send back.
		if re := sch.patternRE(); re != nil && !re.MatchString(str) {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "pattern-mismatch", Field: path,
				Detail: fmt.Sprintf("spec (%s) requires %s, response has %q", owner, sch.Pattern, str)})
		}
		// An enum names every value the service uses for that field, and
		// Discovery lists them all — including the *_UNSPECIFIED zero value.
		// A response outside the set is a value the service does not have: a
		// state invented to fill the field, or a spelling nobody defined. The
		// type check cannot see it, because an invented value is still a
		// string.
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
