package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Runtime wire-shape validation against the vendored Azure Swagger 2.0
// specs (specs/cloud-api/azure/). Armed when SOCKERLESS_SPEC_VALIDATE
// (report file) is set; SOCKERLESS_SPEC_DIR must then point at the
// vendored spec directory. Every JSON success response whose
// method+path matches a swagger path template is validated
// member-by-member against the operation's response schema: members the
// spec doesn't define and primitive type mismatches are violations.
// Exchanges with no matching swagger path (allowlisted non-spec
// surfaces, Graph, Cosmos SQL, ...), error responses
// (status >= 400), empty bodies, and non-JSON bodies are not validated
// — the static conformance gate owns the route-table surface.

// azureSpecDoc is one vendored swagger file plus the upstream-repo path
// SOURCES.md records for it; relative cross-file $refs resolve against
// the upstream directory layout, never the flat local one.
type azureSpecDoc struct {
	file     string // local file name (foo.swagger.json.gz)
	upstream string // path inside Azure/azure-rest-api-specs
	root     map[string]any
}

// azureRefSchema pairs a schema fragment with the doc it physically
// lives in, so relative $refs inside the fragment resolve correctly.
type azureRefSchema struct {
	doc *azureSpecDoc
	s   map[string]any
}

// azureSpecOp is one method+path operation extracted from paths /
// x-ms-paths (basePath joined, segments normalized for matching).
type azureSpecOp struct {
	method   string
	template string // basePath-joined swagger key (the Op label)
	segs     []string
	literals int
	doc      *azureSpecDoc
	op       map[string]any
}

type azureSpecIndex struct {
	docs map[string]*azureSpecDoc // upstream path -> doc
	// ops sorted most-literal-first so the most specific template is
	// preferred when several match one request path.
	ops []azureSpecOp
	// subtypes maps "<base canonical id>\x00<discriminator value>" to
	// the polymorphic subtype definition (x-ms-discriminator-value or
	// the definition name).
	subtypes map[string]azureRefSchema
}

var azureParamSegment = regexp.MustCompile(`\{[^}]+\}`)

// splitAzureSegs normalizes one path for comparison: parameters collapse
// to {} ({...}-style greedy mux labels to {+}), everything lowercases
// (ARM routing is case-insensitive and AzurePathNormalizationMiddleware
// lowercases action verbs), and empty segments drop (the validator runs
// ahead of CleanPathMiddleware, so go-azure-sdk's double-slash paths
// reach it uncleaned).
func splitAzureSegs(p string) []string {
	p = azureParamSegment.ReplaceAllStringFunc(p, func(s string) string {
		if strings.HasSuffix(s[1:len(s)-1], "...") {
			return "{+}"
		}
		return "{}"
	})
	p = strings.ToLower(strings.Trim(p, "/"))
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	segs := parts[:0]
	for _, s := range parts {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// matchAzureSegs reports whether a simulator path (a mux pattern or a
// concrete request path) is a valid spelling of a Swagger path. Rules:
//   - a spec "{}" in FIRST position is a scope parameter
//     (x-ms-skip-url-encoding resource IDs like
//     "/{scope}/providers/Microsoft.Authorization/...") and consumes one
//     or more simulator segments;
//   - elsewhere spec "{}" consumes exactly one simulator segment of any
//     kind (a simulator literal narrows the parameter — valid);
//   - a spec literal requires the identical (lowercased) simulator
//     literal;
//   - a simulator trailing "{+}" consumes any remaining spec segments
//     (greedy fan-in accepts a superset).
func matchAzureSegs(simSegs, spec []string, specFirst bool) bool {
	if len(spec) == 0 {
		return len(simSegs) == 0
	}
	if len(simSegs) == 0 {
		return false
	}
	if simSegs[0] == "{+}" && len(simSegs) == 1 {
		return true
	}
	// Scope expansion applies only to ARM scope paths
	// ("/{scope}/providers/Microsoft.X/..."): the scope resource ID
	// spans several segments. A leading parameter in a data-plane spec
	// ("/{containerName}/{blob}") stays single-segment — expanding it
	// would let almost any route match.
	if spec[0] == "{}" && specFirst && len(spec) > 1 && spec[1] == "providers" {
		for i := 1; i <= len(simSegs); i++ {
			if matchAzureSegs(simSegs[i:], spec[1:], false) {
				return true
			}
		}
		return false
	}
	if spec[0] == "{}" || spec[0] == simSegs[0] {
		return matchAzureSegs(simSegs[1:], spec[1:], false)
	}
	return false
}

// parseAzureSpecSources reads SOURCES.md and returns local file name ->
// upstream repo path. The table is the single source of truth for ref
// resolution; a vendored file missing from it cannot resolve refs.
func parseAzureSpecSources(dir string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "SOURCES.md"))
	if err != nil {
		return nil, fmt.Errorf("spec-validate: read SOURCES.md: %w", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 5 {
			continue
		}
		local := strings.Trim(strings.TrimSpace(cells[1]), "`")
		upstream := strings.Trim(strings.TrimSpace(cells[3]), "`")
		if strings.HasSuffix(local, ".swagger.json.gz") && upstream != "" {
			out[local] = upstream
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("spec-validate: SOURCES.md in %s contains no spec rows", dir)
	}
	return out, nil
}

// loadAzureSpecIndex loads every vendored swagger file, verifies the
// $ref closure is complete (every referenced file is vendored and every
// pointer resolves — a miss is a vendor gap, not a skippable condition),
// and builds the path-matching and discriminator indices.
func loadAzureSpecIndex(dir string) (*azureSpecIndex, error) {
	sources, err := parseAzureSpecSources(dir)
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.swagger.json.gz"))
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("spec-validate: no swagger specs under %s (glob err: %v)", dir, err)
	}
	sort.Strings(paths)

	idx := &azureSpecIndex{docs: map[string]*azureSpecDoc{}, subtypes: map[string]azureRefSchema{}}
	for _, p := range paths {
		file := filepath.Base(p)
		upstream, ok := sources[file]
		if !ok {
			return nil, fmt.Errorf("spec-validate: %s has no SOURCES.md row — upstream path unknown, refs cannot resolve", file)
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("spec-validate: %s: %w", file, err)
		}
		var root map[string]any
		err = json.NewDecoder(gz).Decode(&root)
		_ = gz.Close()
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("spec-validate: %s: %w", file, err)
		}
		idx.docs[upstream] = &azureSpecDoc{file: file, upstream: upstream, root: root}
	}

	var missing []string
	seen := map[string]bool{}
	for _, doc := range idx.docs {
		collectUnresolvableRefs(idx, doc, doc.root, seen, &missing)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("spec-validate: %d unresolvable $ref target(s) — vendor the missing files with scripts/fetch-azure-spec.sh:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}

	for _, doc := range idx.docs {
		idx.addDocOps(doc)
		idx.addDocSubtypes(doc)
	}
	sort.Slice(idx.ops, func(i, j int) bool {
		a, b := idx.ops[i], idx.ops[j]
		if a.literals != b.literals {
			return a.literals > b.literals
		}
		if a.template != b.template {
			return a.template < b.template
		}
		return a.doc.file < b.doc.file
	})
	return idx, nil
}

// collectUnresolvableRefs walks a document tree recording every "$ref"
// whose target file is not vendored or whose pointer does not resolve.
// x-ms-examples subtrees are excluded: example payload files are not
// part of the schema closure and are never vendored.
func collectUnresolvableRefs(idx *azureSpecIndex, doc *azureSpecDoc, node any, seen map[string]bool, missing *[]string) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if k == "x-ms-examples" {
				continue
			}
			if k == "$ref" {
				if ref, ok := v.(string); ok {
					if _, _, ok := idx.refTarget(doc, ref); !ok {
						key := ref + " (referenced from " + doc.file + ")"
						if !seen[key] {
							seen[key] = true
							*missing = append(*missing, key)
						}
					}
					continue
				}
			}
			collectUnresolvableRefs(idx, doc, v, seen, missing)
		}
	case []any:
		for _, v := range n {
			collectUnresolvableRefs(idx, doc, v, seen, missing)
		}
	}
}

// addDocOps extracts every operation from paths + x-ms-paths, applying
// the same basePath conventions as the static conformance gate.
func (idx *azureSpecIndex) addDocOps(doc *azureSpecDoc) {
	basePath, _ := doc.root["basePath"].(string)
	// The Log Analytics query data plane is served under the /v1
	// endpoint prefix (https://api.loganalytics.io/v1/...), which its
	// swagger expresses via the host version rather than basePath.
	if basePath == "" && strings.HasPrefix(doc.file, "monitor-dataplane-operationalinsights-v1") {
		basePath = "/v1"
	}
	// The EventGrid publish data plane's swagger folds the whole publish
	// URL into its x-ms-parameterized-host template; its x-ms-paths keys
	// are pure query-string overloads ("?overload=cloudEvent"). The
	// documented publish URL is
	// https://<topic>.<region>.eventgrid.azure.net/api/events.
	if strings.HasPrefix(doc.file, "eventgrid-dataplane-") {
		basePath = "/api/events"
	}
	add := func(rawPath string, methods map[string]any) {
		// x-ms-paths keys may carry a query discriminator
		// ("/path?comp=list"); matching uses the path part, the Op label
		// keeps the full template.
		matchPath := rawPath
		if i := strings.Index(matchPath, "?"); i >= 0 {
			matchPath = matchPath[:i]
		}
		template := rawPath
		if basePath != "" && basePath != "/" {
			matchPath = strings.TrimSuffix(basePath, "/") + "/" + strings.TrimPrefix(matchPath, "/")
			template = strings.TrimSuffix(basePath, "/") + "/" + strings.TrimPrefix(rawPath, "/")
		}
		segs := splitAzureSegs(matchPath)
		literals := 0
		for _, s := range segs {
			if s != "{}" && s != "{+}" {
				literals++
			}
		}
		for method, rawOp := range methods {
			op, ok := rawOp.(map[string]any)
			if !ok {
				continue
			}
			switch method {
			case "get", "put", "post", "patch", "delete", "head", "options":
				idx.ops = append(idx.ops, azureSpecOp{
					method:   strings.ToUpper(method),
					template: template,
					segs:     segs,
					literals: literals,
					doc:      doc,
					op:       op,
				})
			}
		}
	}
	if paths, ok := doc.root["paths"].(map[string]any); ok {
		for rawPath, methods := range paths {
			if m, ok := methods.(map[string]any); ok {
				add(rawPath, m)
			}
		}
	}
	if paths, ok := doc.root["x-ms-paths"].(map[string]any); ok {
		for rawPath, methods := range paths {
			if m, ok := methods.(map[string]any); ok {
				add(rawPath, m)
			}
		}
	}
}

// addDocSubtypes indexes polymorphic subtypes: a definition whose allOf
// references a base schema carrying "discriminator" registers under the
// base's canonical id + its discriminator value (x-ms-discriminator-value
// when present, the definition name otherwise).
func (idx *azureSpecIndex) addDocSubtypes(doc *azureSpecDoc) {
	defs, _ := doc.root["definitions"].(map[string]any)
	for name, raw := range defs {
		def, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		allOf, _ := def["allOf"].([]any)
		for _, b := range allOf {
			branch, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if _, isRef := branch["$ref"].(string); !isRef {
				continue
			}
			base, baseID := idx.deref(azureRefSchema{doc: doc, s: branch})
			if base.s == nil || baseID == "" {
				continue
			}
			if d, ok := base.s["discriminator"].(string); ok && d != "" {
				value := name
				if x, ok := def["x-ms-discriminator-value"].(string); ok && x != "" {
					value = x
				}
				idx.subtypes[baseID+"\x00"+value] = azureRefSchema{doc: doc, s: def}
			}
		}
	}
}

// refTarget resolves one "$ref" string from doc: the file part (if any)
// resolves relative to the doc's upstream directory, the fragment is a
// JSON pointer into the target document. Returns the canonical id
// "<upstream>#<pointer>" used for cycle guards and subtype lookup.
func (idx *azureSpecIndex) refTarget(doc *azureSpecDoc, ref string) (azureRefSchema, string, bool) {
	filePart, pointer, _ := strings.Cut(ref, "#")
	target := doc
	if filePart != "" {
		up := path.Clean(path.Join(path.Dir(doc.upstream), filePart))
		target = idx.docs[up]
		if target == nil {
			return azureRefSchema{}, "", false
		}
	}
	node := any(target.root)
	if pointer != "" {
		for _, seg := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
			seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
			m, ok := node.(map[string]any)
			if !ok {
				return azureRefSchema{}, "", false
			}
			node, ok = m[seg]
			if !ok {
				return azureRefSchema{}, "", false
			}
		}
	}
	m, ok := node.(map[string]any)
	if !ok {
		return azureRefSchema{}, "", false
	}
	return azureRefSchema{doc: target, s: m}, target.upstream + "#" + pointer, true
}

// deref follows a $ref chain to a concrete schema. The load-time closure
// check guarantees every target resolves; the depth bound only guards
// degenerate ref cycles.
func (idx *azureSpecIndex) deref(rs azureRefSchema) (azureRefSchema, string) {
	id := ""
	for range 32 {
		ref, ok := rs.s["$ref"].(string)
		if !ok {
			return rs, id
		}
		next, nextID, ok := idx.refTarget(rs.doc, ref)
		if !ok {
			return azureRefSchema{}, ""
		}
		rs, id = next, nextID
	}
	return azureRefSchema{}, ""
}

// azureCompiledPattern returns the compiled form of a declared pattern, or nil
// when there is none or the expression cannot be compiled — the validator
// refuses to judge what it cannot read. Compilation is memoized because the
// same schemas are re-reached on every response.
var azurePatternCache sync.Map

func azureCompiledPattern(pattern string) *regexp.Regexp {
	if pattern == "" {
		return nil
	}
	if cached, ok := azurePatternCache.Load(pattern); ok {
		re, _ := cached.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		re = nil
	}
	azurePatternCache.Store(pattern, re)
	return re
}

// azureMergedSchema is the flattened view of a schema and its whole
// allOf chain (ARM models inheritance via allOf: Resource ->
// TrackedResource -> ConcreteType).
type azureMergedSchema struct {
	typ       string
	props     map[string]azureRefSchema
	items     *azureRefSchema
	addl      *azureRefSchema // additionalProperties schema (map values)
	addlAny   bool            // additionalProperties: true
	required  map[string]bool // properties the schema declares required
	disc      string          // discriminator property name
	discOwner string          // canonical id of the declaring schema
	pattern   string          // declared regular expression for a string value
}

// merge folds one schema (and recursively its allOf branches) into m.
// First writer wins per property so a subtype's narrower redeclaration
// shadows the base's.
func (idx *azureSpecIndex) merge(rs azureRefSchema, id string, visited map[string]bool, m *azureMergedSchema) {
	if id != "" {
		if visited[id] {
			return
		}
		visited[id] = true
	}
	if t, ok := rs.s["type"].(string); ok && m.typ == "" {
		m.typ = t
	}
	if p, ok := rs.s["pattern"].(string); ok && p != "" && m.pattern == "" {
		m.pattern = p
	}
	if req, ok := rs.s["required"].([]any); ok {
		for _, raw := range req {
			if name, ok := raw.(string); ok && name != "" {
				if m.required == nil {
					m.required = map[string]bool{}
				}
				m.required[name] = true
			}
		}
	}
	if props, ok := rs.s["properties"].(map[string]any); ok {
		for name, raw := range props {
			if ps, ok := raw.(map[string]any); ok {
				if _, exists := m.props[name]; !exists {
					m.props[name] = azureRefSchema{doc: rs.doc, s: ps}
				}
			}
		}
	}
	switch ap := rs.s["additionalProperties"].(type) {
	case bool:
		if ap {
			m.addlAny = true
		}
	case map[string]any:
		if m.addl == nil {
			m.addl = &azureRefSchema{doc: rs.doc, s: ap}
		}
	}
	if items, ok := rs.s["items"].(map[string]any); ok && m.items == nil {
		m.items = &azureRefSchema{doc: rs.doc, s: items}
	}
	if d, ok := rs.s["discriminator"].(string); ok && d != "" && m.disc == "" {
		m.disc = d
		m.discOwner = id
	}
	if allOf, ok := rs.s["allOf"].([]any); ok {
		for _, b := range allOf {
			branch, ok := b.(map[string]any)
			if !ok {
				continue
			}
			brs, bid := idx.deref(azureRefSchema{doc: rs.doc, s: branch})
			if brs.s != nil {
				idx.merge(brs, bid, visited, m)
			}
		}
	}
}

// responseSchema picks the schema for the operation's response at the
// given status (exact code, else "default"). A response defined without
// a schema declares no body shape — there is nothing to validate
// against, so such candidates report ok=false.
func (idx *azureSpecIndex) responseSchema(c *azureSpecOp, status int) (azureRefSchema, bool) {
	responses, _ := c.op["responses"].(map[string]any)
	raw, ok := responses[strconv.Itoa(status)]
	if !ok {
		raw, ok = responses["default"]
	}
	if !ok {
		return azureRefSchema{}, false
	}
	resp, ok := raw.(map[string]any)
	if !ok {
		return azureRefSchema{}, false
	}
	// Response-level $ref targets a shared response object
	// (#/responses/...), not a schema.
	if _, isRef := resp["$ref"].(string); isRef {
		rrs, _ := idx.deref(azureRefSchema{doc: c.doc, s: resp})
		if rrs.s == nil {
			return azureRefSchema{}, false
		}
		resp = rrs.s
	}
	schema, ok := resp["schema"].(map[string]any)
	if !ok {
		return azureRefSchema{}, false
	}
	return azureRefSchema{doc: c.doc, s: schema}, true
}

// schemaName labels a schema for violation details.
func schemaName(id string, rs azureRefSchema) string {
	if id != "" {
		if i := strings.LastIndex(id, "/"); i >= 0 {
			return id[i+1:] + " (" + rs.doc.file + ")"
		}
		return id
	}
	return "inline schema (" + rs.doc.file + ")"
}

// validate walks a decoded JSON value against a schema, reporting
// members the spec doesn't define ("unknown-field") and primitive type
// mismatches ("type-mismatch"). Null is always acceptable (an omitted
// member); enum value checking is out of scope. dispatched marks that a
// discriminator subtype was already selected for this value, so the
// base's discriminator (re-merged via the subtype's allOf) does not
// re-dispatch.
func (idx *azureSpecIndex) validate(op string, rs azureRefSchema, fieldPath string, v any, dispatched bool, out *[]sim.SpecViolation) {
	if v == nil || rs.s == nil {
		return
	}
	rs, id := idx.deref(rs)
	if rs.s == nil {
		return
	}
	m := &azureMergedSchema{props: map[string]azureRefSchema{}}
	idx.merge(rs, id, map[string]bool{}, m)

	mismatch := func(want string) {
		*out = append(*out, sim.SpecViolation{
			Op: op, Kind: "type-mismatch", Field: fieldPath,
			Detail: fmt.Sprintf("spec %s is %s, response has %T", schemaName(id, rs), want, v),
		})
	}

	switch m.typ {
	case "object", "":
		open := len(m.props) == 0 && m.addl == nil && !m.addlAny && m.disc == ""
		if m.typ == "" && open {
			// No type and no member contract: an open schema accepts
			// any JSON value.
			return
		}
		obj, ok := v.(map[string]any)
		if !ok {
			mismatch("an object")
			return
		}
		if m.disc != "" && !dispatched {
			if dv, ok := obj[m.disc].(string); ok {
				if sub, found := idx.subtypes[m.discOwner+"\x00"+dv]; found {
					idx.validate(op, sub, fieldPath, v, true, out)
					return
				}
				// Unknown discriminator value: fall through to the base
				// schema, whose property set flags the subtype members.
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			val := obj[key]
			if p, exists := m.props[key]; exists {
				idx.validate(op, p, fieldPath+"."+key, val, false, out)
				continue
			}
			if m.addlAny || open {
				continue // map with unconstrained values / open object
			}
			if m.addl != nil {
				idx.validate(op, *m.addl, fieldPath+"."+key, val, false, out)
				continue
			}
			*out = append(*out, sim.SpecViolation{
				Op: op, Kind: "unknown-field", Field: fieldPath + "." + key,
				Detail: "property not defined by " + schemaName(id, rs),
			})
		}
		// A property the schema declares required and the response omits. The
		// loop above can only look at keys that are there, so this is the one
		// omission it cannot see — and a generated client reads a required
		// property without checking whether it arrived.
		for name := range m.required {
			if _, present := obj[name]; present {
				continue
			}
			*out = append(*out, sim.SpecViolation{
				Op: op, Kind: "missing-required", Field: fieldPath + "." + name,
				Detail: schemaName(id, rs) + " declares this property required",
			})
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			mismatch("an array")
			return
		}
		if m.items == nil {
			return
		}
		for i, item := range arr {
			idx.validate(op, *m.items, fmt.Sprintf("%s[%d]", fieldPath, i), item, false, out)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			mismatch("a string")
			return
		}
		// A declared pattern is part of the contract, and it is where the
		// identity-bearing strings are pinned. Swagger patterns anchor
		// themselves when they mean to — most in the vendored corpus are
		// written ^...$ — so the expression is matched as written rather than
		// anchored for the author.
		if re := azureCompiledPattern(m.pattern); re != nil && !re.MatchString(str) {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "pattern-mismatch", Field: fieldPath,
				Detail: fmt.Sprintf("spec (%s) requires %s, response has %q", schemaName(id, rs), m.pattern, str)})
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			mismatch("a boolean")
		}
	case "integer", "number":
		if _, ok := v.(float64); !ok {
			mismatch("numeric (" + m.typ + ")")
		}
	case "file":
		// Raw payload responses are not JSON-shaped; nothing to check.
	}
}

// match returns every swagger operation the request method+path is a
// spelling of, most-literal template first.
func (idx *azureSpecIndex) match(method, reqPath string) []*azureSpecOp {
	reqSegs := splitAzureSegs(reqPath)
	var out []*azureSpecOp
	for i := range idx.ops {
		op := &idx.ops[i]
		if op.method == method && matchAzureSegs(reqSegs, op.segs, true) {
			out = append(out, op)
		}
	}
	return out
}

// azureNonSpecRequestPrefixes lists request-path families that are
// real, documented Azure wire surfaces with NO swagger in
// Azure/azure-rest-api-specs (the runtime counterpart of the static
// gate's allowedNonSpecAzurePrefixes). They must be excluded from
// matching because their short paths are otherwise valid spellings of
// leading-parameter data-plane templates — Graph's "POST /v1.0/groups"
// and its beta spelling "POST /beta/groups" both unify with the blob
// "POST /{containerName}/{blob}" template, and a Functions invoke body is
// workload-defined, not a swagger shape.
// /metadata/identity/ is deliberately NOT here: the vendored IMDS
// swagger describes it, so it validates like any other surface.
var azureNonSpecRequestPrefixes = []string{
	"/dbs",                // Cosmos DB SQL data plane (documented REST API, no upstream swagger)
	"/v1.0/",              // Microsoft Graph v1.0 surface (spec lives in msgraph-metadata)
	"/beta/",              // Microsoft Graph beta surface (spec lives in msgraph-metadata)
	"/api/function",       // Functions host invoke endpoint (body is workload output)
	"/msi/token",          // App Service MSI token endpoint
	"/metadata/endpoints", // ARM cloud-environment metadata bootstrap (terraform metadata_host)
}

// azureNonSpecRequestPath reports whether a request path belongs to a
// documented non-spec surface and therefore has no swagger shape to
// validate against.
func azureNonSpecRequestPath(p string) bool {
	lp := strings.ToLower(p)
	for _, prefix := range azureNonSpecRequestPrefixes {
		if strings.HasPrefix(lp, prefix) {
			return true
		}
	}
	// ServiceBus HTTP message send/receive/lock ("/{entity}/messages...")
	// is documented but unswaggered — the vendored 2021-05 data-plane
	// swagger covers only the ATOM entity-management surface.
	if segs := splitAzureSegs(lp); len(segs) >= 2 && segs[1] == "messages" {
		return true
	}
	return false
}

// armSpecValidator wires runtime shape validation onto the server when
// SOCKERLESS_SPEC_VALIDATE is set. Hard failure when the spec dir is
// missing or its $ref closure is incomplete: the operator asked for
// validation.
func armSpecValidator(srv *sim.Server) error {
	if os.Getenv("SOCKERLESS_SPEC_VALIDATE") == "" {
		return nil
	}
	dir := os.Getenv("SOCKERLESS_SPEC_DIR")
	if dir == "" {
		return fmt.Errorf("SOCKERLESS_SPEC_VALIDATE is set but SOCKERLESS_SPEC_DIR is not")
	}
	idx, err := loadAzureSpecIndex(dir)
	if err != nil {
		return err
	}
	srv.SetSpecValidator(func(req *http.Request, _ []byte, status int, respHeader http.Header, respBody []byte) []sim.SpecViolation {
		if status >= 400 || len(respBody) == 0 {
			return nil // error/empty response
		}
		ct := respHeader.Get("Content-Type")
		if ct != "" && !strings.Contains(ct, "json") {
			return nil // XML / binary / text data planes
		}
		if azureNonSpecRequestPath(req.URL.Path) {
			return nil // documented non-spec surface; no swagger shape exists
		}
		if cosmosIsDataPlaneRequest(req) {
			// The Cosmos DB SQL data plane (incl. the account-discovery GET /)
			// has no vendored Azure swagger; without this its account-properties
			// response is mis-matched to the storage account-root operation.
			return nil
		}
		cands := idx.match(req.Method, req.URL.Path)
		if len(cands) == 0 {
			return nil // non-spec surface; the static route gate owns it
		}
		var body any
		if err := json.Unmarshal(respBody, &body); err != nil {
			if strings.Contains(ct, "json") {
				return []sim.SpecViolation{{
					Op:   cands[0].method + " " + cands[0].template,
					Kind: "malformed-json", Field: "$", Detail: err.Error(),
				}}
			}
			return nil // no declared content type and not JSON
		}
		// Several templates can match one path (api-version variants,
		// x-ms-paths overloads, the generic-resource paths). Only the
		// MOST SPECIFIC templates (highest literal-segment count) that
		// declare a body shape for this status are consulted — the
		// generic-resource template ("/{provider}/{type}/{name}")
		// otherwise out-bids a specific schema's findings with a single
		// vacuous violation. The response conforms if any consulted
		// candidate accepts it; otherwise the fewest-violation candidate
		// reports.
		// A status the operation does not declare. The candidate loop below
		// falls back to the "default" response — the error shape — so an
		// undeclared success code was neither reported nor its body checked:
		// the response went out unjudged. A client generated from the document
		// switches on the codes it declares, so one it does not is a code the
		// caller has no branch for.
		if status < 400 {
			declared := false
			for _, c := range cands {
				if responses, ok := c.op["responses"].(map[string]any); ok {
					if _, listed := responses[strconv.Itoa(status)]; listed {
						declared = true
						break
					}
				}
			}
			if !declared && len(cands) > 0 {
				c := cands[0]
				listed := make([]string, 0, 4)
				if responses, ok := c.op["responses"].(map[string]any); ok {
					for code := range responses {
						listed = append(listed, code)
					}
				}
				sort.Strings(listed)
				return []sim.SpecViolation{{
					Op: c.method + " " + c.template, Kind: "undeclared-status", Field: "$",
					Detail: fmt.Sprintf("spec declares %s, response has %d",
						strings.Join(listed, "|"), status),
				}}
			}
		}
		var best []sim.SpecViolation
		bestSet := false
		maxLiterals := -1
		for _, c := range cands {
			if maxLiterals >= 0 && c.literals < maxLiterals {
				break // cands sorted most-literal-first
			}
			rs, ok := idx.responseSchema(c, status)
			if !ok {
				continue // spec declares no body shape for this status
			}
			maxLiterals = c.literals
			var out []sim.SpecViolation
			idx.validate(c.method+" "+c.template, rs, "$", body, false, &out)
			if len(out) == 0 {
				return nil
			}
			if !bestSet || len(out) < len(best) {
				best, bestSet = out, true
			}
		}
		return best
	})
	return nil
}
