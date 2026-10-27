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

	"github.com/e6qu/sockerless-cloud/sim"
)

// Runtime wire-shape validation against the vendored Swagger 2.0 specs, armed
// when SOCKERLESS_SPEC_VALIDATE names a report file (SOCKERLESS_SPEC_DIR must
// point at the specs). Every JSON success response matching a swagger path is
// checked member by member: undefined members and primitive type mismatches
// are violations. Unmatched exchanges, errors, empty and non-JSON bodies are
// not validated; the static conformance gate owns the route table.

// Relative cross-file $refs resolve against the upstream directory layout
// SOURCES.md records, never the flat local one.
type azureSpecDoc struct {
	file     string // local file name (foo.swagger.json.gz)
	upstream string // path inside Azure/azure-rest-api-specs
	root     map[string]any
}

// Pairs a fragment with the doc it lives in, so its relative $refs resolve.
type azureRefSchema struct {
	doc *azureSpecDoc
	s   map[string]any
}

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
	// Most literal first, so the most specific template wins.
	ops []azureSpecOp
	// "<base canonical id>\x00<discriminator value>" to the polymorphic subtype
	// (x-ms-discriminator-value or the definition name).
	subtypes map[string]azureRefSchema
}

var azureParamSegment = regexp.MustCompile(`\{[^}]+\}`)

// Parameters collapse to {} (greedy mux labels to {+}), everything lowercases
// (Azure Resource Manager routing is case-insensitive and the middleware
// lowercases action verbs), and empty segments drop: the validator runs ahead
// of CleanPathMiddleware, so go-azure-sdk's double slashes reach it.
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

// matchAzureSegs reports whether a simulator path (mux pattern or concrete
// path) spells a Swagger path:
//   - a spec "{}" first is a scope parameter
//     ("/{scope}/providers/Microsoft.Authorization/...") consuming one or more
//     segments;
//   - elsewhere a spec "{}" consumes exactly one segment, a simulator literal
//     narrowing it;
//   - a spec literal requires the identical lowercased literal;
//   - a simulator trailing "{+}" consumes any remaining spec segments.
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
	// Only Azure Resource Manager scope paths expand; a leading data-plane
	// parameter ("/{containerName}/{blob}") stays single-segment, or almost any
	// route would match.
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

// SOURCES.md is the single source of truth for ref resolution.
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

// A $ref whose file is not vendored or whose pointer does not resolve is a
// vendor gap, not a skippable condition.
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

// x-ms-examples subtrees are excluded: example payloads are not vendored.
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

// The same basePath conventions as the static conformance gate.
func (idx *azureSpecIndex) addDocOps(doc *azureSpecDoc) {
	basePath, _ := doc.root["basePath"].(string)
	// The Log Analytics query plane lives under /v1
	// (https://api.loganalytics.io/v1/...), which its swagger expresses through
	// the host version rather than basePath.
	if basePath == "" && strings.HasPrefix(doc.file, "monitor-dataplane-operationalinsights-v1") {
		basePath = "/v1"
	}
	// The Event Grid publish swagger folds the URL into
	// x-ms-parameterized-host and keys x-ms-paths by query overloads
	// ("?overload=cloudEvent"); the publish URL is
	// https://<topic>.<region>.eventgrid.azure.net/api/events.
	if strings.HasPrefix(doc.file, "eventgrid-dataplane-") {
		basePath = "/api/events"
	}
	add := func(rawPath string, methods map[string]any) {
		// Match the path part of a "/path?comp=list" key; the Op label keeps the full
		// template.
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

// A definition whose allOf references a discriminated base registers under the
// base's canonical id and its discriminator value (x-ms-discriminator-value,
// else the definition name).
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

// The file part resolves against the doc's upstream directory, the fragment is
// a JSON pointer. The canonical id "<upstream>#<pointer>" guards cycles and
// keys subtypes.
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

// The load-time closure check guarantees every target resolves; the depth bound
// guards degenerate cycles.
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

// Nil when there is no pattern or it does not compile: the validator refuses to
// judge what it cannot read. Memoized, as the same schemas recur.
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

// A schema flattened with its allOf chain (Resource -> TrackedResource ->
// ConcreteType).
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

// First writer wins per property, so a subtype's narrower redeclaration
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

// The exact code, else "default". A response with no schema declares no body
// shape, so it reports ok=false.
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
	// A response-level $ref targets #/responses/..., not a schema.
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

func schemaName(id string, rs azureRefSchema) string {
	if id != "" {
		if i := strings.LastIndex(id, "/"); i >= 0 {
			return id[i+1:] + " (" + rs.doc.file + ")"
		}
		return id
	}
	return "inline schema (" + rs.doc.file + ")"
}

// Reports undefined members ("unknown-field") and primitive mismatches
// ("type-mismatch"). Null is acceptable; enums are not checked. dispatched
// marks a discriminator subtype already selected, so the base's discriminator,
// re-merged through the subtype's allOf, does not re-dispatch.
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
			// No type and no member contract: an open schema accepts any value.
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
				// Unknown discriminator value: fall through to the base schema, whose
				// property set flags the subtype members.
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
		// The loop above sees only keys present; a generated client reads a required
		// property without checking it arrived.
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
		// Patterns pin the identity-bearing strings. Swagger patterns anchor
		// themselves when meant to, so match as written.
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
		// Raw payload responses are not JSON-shaped.
	}
}

// Most literal template first.
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

// Documented surfaces with no swagger (the runtime counterpart of
// allowedNonSpecAzurePrefixes). Their short paths would otherwise spell
// leading-parameter data-plane templates: Graph's "POST /v1.0/groups" unifies
// with blob's "POST /{containerName}/{blob}". /metadata/identity/ is absent
// because the vendored IMDS swagger describes it.
var azureNonSpecRequestPrefixes = []string{
	"/dbs",                // Cosmos DB SQL data plane (documented REST API, no upstream swagger)
	"/v1.0/",              // Microsoft Graph v1.0 surface (spec lives in msgraph-metadata)
	"/beta/",              // Microsoft Graph beta surface (spec lives in msgraph-metadata)
	"/msi/token",          // App Service MSI token endpoint
	"/metadata/endpoints", // ARM cloud-environment metadata bootstrap (terraform metadata_host)
	"/sockerless/",        // the simulator's own channels from its sidecars
}

func azureNonSpecRequestPath(p string) bool {
	lp := strings.ToLower(p)
	for _, prefix := range azureNonSpecRequestPrefixes {
		if strings.HasPrefix(lp, prefix) {
			return true
		}
	}
	// Service Bus message send/receive/lock is documented but unswaggered; the
	// 2021-05 data-plane swagger covers only ATOM entity management.
	if segs := splitAzureSegs(lp); len(segs) >= 2 && segs[1] == "messages" {
		return true
	}
	return false
}

// A missing spec dir or incomplete $ref closure fails hard: the operator asked
// for validation.
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
		if _, ok := appServiceSiteByHost(req.Host); ok {
			return nil // an App Service site's answer is its workload's
		}
		if _, ok := appServiceSiteByScmHost(req.Host); ok {
			return nil // the Kudu API on a site's SCM host has no vendored swagger
		}
		if acaIngressServesHost(req.Host) {
			return nil // a container app's answer is its workload's
		}
		if cosmosIsDataPlaneRequest(req) {
			// The Cosmos DB SQL data plane, account-discovery GET / included, has no
			// swagger; it would otherwise match the storage account-root operation.
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
		// A success status the operation does not declare is a code a generated client
		// has no branch for. The candidate loop would fall back to the "default" error
		// response and leave it unjudged.
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
		// Several templates can match one path (api-version variants, x-ms-paths
		// overloads, generic-resource paths). Consult only the most specific ones
		// that declare a body shape for this status, or the generic template
		// out-bids a specific schema with one vacuous violation. Any acceptance
		// conforms; otherwise the fewest-violation candidate reports.
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
