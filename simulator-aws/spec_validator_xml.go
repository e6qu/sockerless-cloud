package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/rs/zerolog"
)

// XML wire-shape validation for the awsQuery, ec2Query, and restXml
// protocols. The ground truth is the vendored Smithy models: element
// names honor smithy.api#xmlName on members and shapes, list/map
// nesting honors smithy.api#xmlFlattened, and restXml HTTP bindings
// (httpPayload / httpHeader / httpPrefixHeaders / httpResponseCode)
// decide which members appear in the body at all.

// specValidatorState is the armed validator: the loaded spec set plus
// the server mux (restXml operations are identified by the pattern that
// served the request).
type specValidatorState struct {
	spec   *smithySpecSet
	mux    *http.ServeMux
	logger zerolog.Logger
	// skips counts subtrees the validator declined to judge because the
	// spec-trait combination was ambiguous. Each skip logs at warn —
	// skips must stay rare and visible, never a blanket pass.
	skips atomic.Int64
}

// skip records one declined judgement. Skipping silently would let a
// blanket pass hide behind "ambiguous"; logging keeps skips countable.
func (st *specValidatorState) skip(op, path, reason string) {
	n := st.skips.Add(1)
	st.logger.Warn().Str("op", op).Str("path", path).Str("reason", reason).
		Int64("totalSkips", n).Msg("spec-validate: subtree skipped (not judged)")
}

// validateQueryAction covers POST / exchanges routed by the
// (Action, Version) form parameter pair.
func (st *specValidatorState) validateQueryAction(reqBody []byte, respHeader http.Header, respBody []byte) []sim.SpecViolation {
	form, err := url.ParseQuery(string(reqBody))
	if err != nil {
		return nil // not a query-protocol form body
	}
	action := form.Get("Action")
	if action == "" {
		return nil
	}
	idx := st.lookupQueryModel(form.Get("Version"), action)
	if idx == nil {
		return nil // surface gate owns unknown actions
	}
	op := idx.serviceShort + "." + action
	if isEventStreamResponse(respHeader) {
		return nil // framed event-stream body — validated by the SDK decoder
	}
	if !looksLikeXML(respHeader, respBody) {
		return []sim.SpecViolation{{Op: op, Kind: "malformed-xml", Field: "$",
			Detail: fmt.Sprintf("query-protocol success response is not XML (Content-Type %q)", respHeader.Get("Content-Type"))}}
	}
	root, err := parseXMLTree(respBody)
	if err != nil {
		return []sim.SpecViolation{{Op: op, Kind: "malformed-xml", Field: "$", Detail: err.Error()}}
	}
	v := &xmlShapeValidator{st: st, idx: idx, op: op}
	v.queryEnvelope(action, root)
	return v.out
}

// lookupQueryModel resolves the query-protocol model serving an Action:
// the per-service Version form field is canonical; when it doesn't
// resolve, a globally-unique action name does (EC2 / IAM / STS).
func (st *specValidatorState) lookupQueryModel(version, action string) *smithyModelIndex {
	for _, m := range st.spec.queryByVersion[version] {
		if _, ok := m.ops[action]; ok {
			return m
		}
	}
	var found *smithyModelIndex
	for _, m := range st.spec.queryModels {
		if _, ok := m.ops[action]; ok {
			if found != nil {
				st.skip(action, "$", "action defined by multiple query-protocol models and Version "+version+" resolves neither")
				return nil
			}
			found = m
		}
	}
	return found
}

// queryEnvelope validates the protocol envelope and dispatches the
// output-shape walk.
//
//   - awsQuery: <{Action}Response> wrapping <{Action}Result> (members)
//     and <ResponseMetadata><RequestId/></ResponseMetadata> (always
//     legal). Operations without modeled output legally omit the Result
//     element (an empty one is also accepted — its members are what get
//     validated).
//   - ec2Query: flat <{Action}Response> carrying the output members
//     plus <requestId>. The legacy EC2 envelope acknowledges mutations
//     with <return>true</return> — the EC2 API Reference documents it
//     even on operations with modeled output (CreateSecurityGroup,
//     AssociateAddress) while the Smithy model elides it, so it is
//     always legal unless the output models a Return member itself.
func (v *xmlShapeValidator) queryEnvelope(action string, root *xmlNode) {
	wantRoot := action + "Response"
	if root.name != wantRoot {
		v.violate("envelope", "$", fmt.Sprintf("root element <%s>, spec requires <%s>", root.name, wantRoot))
		return
	}
	def := v.idx.ops[action]
	members, ok := v.outputMembers(def.output)
	if !ok {
		return // skip already counted
	}
	if v.idx.protocols["ec2Query"] {
		extra := map[string]bool{"requestId": true, "return": true}
		for name, ref := range members {
			if xmlElementName(name, ref) == "return" {
				delete(extra, "return")
			}
		}
		v.structureChildren(def.output, members, "$", root, extra)
		return
	}
	for _, child := range root.children {
		switch child.name {
		case "ResponseMetadata":
			for _, m := range child.children {
				if m.name != "RequestId" {
					v.violate("unknown-field", "$.ResponseMetadata."+m.name, "awsQuery ResponseMetadata carries only <RequestId>")
				}
			}
		case action + "Result":
			v.structureChildren(def.output, members, "$", child, nil)
		default:
			v.violate("unknown-field", "$."+child.name,
				fmt.Sprintf("awsQuery envelope allows <%sResult> and <ResponseMetadata>", action))
		}
	}
}

// outputMembers resolves an operation output target to its member set.
// Operations without modeled output validate as an empty structure.
func (v *xmlShapeValidator) outputMembers(target string) (map[string]smithyMemberRef, bool) {
	if target == "" || target == "smithy.api#Unit" {
		return nil, true
	}
	shape, ok := v.idx.shapes[target]
	if !ok || shape.Type != "structure" {
		v.st.skip(v.op, "$", "output target "+target+" is not a modeled structure")
		return nil, false
	}
	return shape.Members, true
}

// validateRestXML covers S3 / CloudFront / Route 53 REST routes. The
// operation is identified by the mux pattern that served the request
// plus the smithy URI's query-string literals; non-restXml routes pass
// through untouched.
func (st *specValidatorState) validateRestXML(req *http.Request, respHeader http.Header, respBody []byte) []sim.SpecViolation {
	_, pattern := st.mux.Handler(req)
	if pattern == "" {
		return nil
	}
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return nil // method-less or host-addressed pattern: not a restXml surface
	}
	key := method + " " + normalizeAWSPath(strings.TrimSuffix(path, "{$}"))
	cands := st.spec.restXMLOps[key]
	if len(cands) == 0 {
		// A greedy sim label where the spec has a plain label accepts a
		// superset of spec paths (mux needs greedy when the value can
		// contain separators).
		cands = st.spec.restXMLOps[strings.ReplaceAll(key, "{+}", "{}")]
	}
	if len(cands) == 0 {
		return nil
	}
	op := selectRestXMLOp(cands, req.URL.Query(), req.Header)
	if op == nil {
		st.skip(key, "$", "request query disambiguates none (or several) of the candidate restXml operations")
		return nil
	}
	v := &xmlShapeValidator{st: st, idx: op.idx, op: op.idx.serviceShort + "." + op.name}
	v.restXMLBody(op.idx.ops[op.name], respHeader, respBody)
	return v.out
}

// selectRestXMLOp picks the candidate whose URI query literals and
// required query-/header-bound input members the request satisfies,
// preferring the most matches. The x-id disambiguator is only required
// when the client sent one (the Go SDK does, botocore doesn't); a
// required header (e.g. x-amz-copy-source for UploadPartCopy) separates
// candidates that share a path and the same required query. Endpoint-rule
// variants (staticContextParams — S3 Express) lose ties to the plain
// regional operation. A remaining tie means the request genuinely fits
// two operations — the caller skips rather than guesses.
func selectRestXMLOp(cands []restXMLOp, query url.Values, header http.Header) *restXMLOp {
	best := -1
	var won *restXMLOp
	ambiguous := false
	score := func(c *restXMLOp) (int, bool) {
		n := 0
		for _, lit := range c.literals {
			if lit.key == "x-id" && !query.Has("x-id") {
				continue
			}
			if !query.Has(lit.key) || (lit.hasValue && query.Get(lit.key) != lit.value) {
				return 0, false
			}
			n++
		}
		for _, q := range c.requiredQuery {
			if !query.Has(q) {
				return 0, false
			}
			n++
		}
		// Required header-bound members (e.g. x-amz-copy-source for
		// UploadPartCopy) separate candidates that share a path and the
		// same required query members.
		for _, h := range c.requiredHeader {
			if header.Get(h) == "" {
				return 0, false
			}
			n++
		}
		return n, true
	}
	for i := range cands {
		n, ok := score(&cands[i])
		if !ok {
			continue
		}
		switch {
		case n > best:
			best, won, ambiguous = n, &cands[i], false
		case n == best:
			if won != nil && won.staticContext == cands[i].staticContext {
				ambiguous = true
			} else if won == nil || won.staticContext {
				won, ambiguous = &cands[i], false
			}
		}
	}
	if ambiguous {
		return nil
	}
	return won
}

// restXMLBody validates a restXml success body against the operation's
// output shape. Members bound to headers or the status code never
// appear in the body; a httpPayload member IS the body.
func (v *xmlShapeValidator) restXMLBody(def smithyOpDef, respHeader http.Header, respBody []byte) {
	if def.output == "" || def.output == "smithy.api#Unit" {
		v.violate("unknown-field", "$", "operation has no modeled output, response carries a body")
		return
	}
	shape, ok := v.idx.shapes[def.output]
	if !ok || shape.Type != "structure" {
		v.st.skip(v.op, "$", "output target "+def.output+" is not a modeled structure")
		return
	}

	// httpPayload member: the body is the payload itself.
	for name, ref := range shape.Members {
		if !hasTrait(ref.Traits, "smithy.api#httpPayload") {
			continue
		}
		target, kind := v.resolve(ref.Target)
		switch kind {
		case "structure", "union":
			root, ok := v.parseBody(respHeader, respBody)
			if !ok {
				return
			}
			if !payloadRootNames(name, ref, target.def, ref.Target)[root.name] {
				v.violate("envelope", "$", fmt.Sprintf("payload root element <%s>, spec serializes member %s as <%s>",
					root.name, name, payloadRootName(name, ref, target.def, ref.Target)))
				return
			}
			v.structureChildren(ref.Target, target.def.Members, "$", root, nil)
		default:
			// blob / string payloads (GetObject Body, ...) are raw bytes,
			// not XML — nothing to judge.
		}
		return
	}

	// No payload member: the body is the output structure minus its
	// header- and status-bound members.
	bodyMembers := map[string]smithyMemberRef{}
	for name, ref := range shape.Members {
		if hasTrait(ref.Traits, "smithy.api#httpHeader") ||
			hasTrait(ref.Traits, "smithy.api#httpPrefixHeaders") ||
			hasTrait(ref.Traits, "smithy.api#httpResponseCode") {
			continue
		}
		bodyMembers[name] = ref
	}
	root, ok := v.parseBody(respHeader, respBody)
	if !ok {
		return
	}
	wantRoot := shapeXMLName(shape, def.output)
	if root.name != wantRoot {
		v.violate("envelope", "$", fmt.Sprintf("root element <%s>, spec serializes %s as <%s>", root.name, def.output, wantRoot))
		return
	}
	v.structureChildren(def.output, bodyMembers, "$", root, nil)
}

func (v *xmlShapeValidator) parseBody(respHeader http.Header, respBody []byte) (*xmlNode, bool) {
	if isEventStreamResponse(respHeader) {
		// An event-stream output (e.g. S3 SelectObjectContent) is a framed
		// vnd.amazon.eventstream body, not an XML document — the SDK decoder
		// validates it, so there is no XML envelope to shape-check here.
		return nil, false
	}
	if !looksLikeXML(respHeader, respBody) {
		v.violate("malformed-xml", "$", fmt.Sprintf("restXml success response is not XML (Content-Type %q)", respHeader.Get("Content-Type")))
		return nil, false
	}
	root, err := parseXMLTree(respBody)
	if err != nil {
		v.violate("malformed-xml", "$", err.Error())
		return nil, false
	}
	return root, true
}

// payloadRootName is the element name a httpPayload structure member
// serializes as: the member's xmlName, else the target shape's xmlName,
// else the member name.
func payloadRootName(member string, ref smithyMemberRef, shape smithyShapeDef, target string) string {
	if n := traitString(ref.Traits, "smithy.api#xmlName"); n != "" {
		return n
	}
	if n := traitString(shape.Traits, "smithy.api#xmlName"); n != "" {
		return n
	}
	return member
}

// payloadRootNames additionally accepts the target shape's short name —
// real AWS serializers vary between the member name and the shape name
// when neither carries an xmlName trait.
func payloadRootNames(member string, ref smithyMemberRef, shape smithyShapeDef, target string) map[string]bool {
	names := map[string]bool{payloadRootName(member, ref, shape, target): true}
	if traitString(ref.Traits, "smithy.api#xmlName") == "" && traitString(shape.Traits, "smithy.api#xmlName") == "" {
		names[shortShapeName(target)] = true
	}
	return names
}

// shapeXMLName is the element name a shape serializes as when it is the
// document root: its xmlName trait, else its short shape name.
func shapeXMLName(shape smithyShapeDef, id string) string {
	if n := traitString(shape.Traits, "smithy.api#xmlName"); n != "" {
		return n
	}
	return shortShapeName(id)
}

func shortShapeName(id string) string {
	if i := strings.Index(id, "#"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// xmlShapeValidator walks an XML element tree against Smithy shapes.
type xmlShapeValidator struct {
	st  *specValidatorState
	idx *smithyModelIndex
	op  string
	out []sim.SpecViolation
}

func (v *xmlShapeValidator) violate(kind, field, detail string) {
	v.out = append(v.out, sim.SpecViolation{Op: v.op, Kind: kind, Field: field, Detail: detail})
}

// resolvedShape pairs a shape definition with its coarse validation kind.
type resolvedShape struct {
	def  smithyShapeDef
	kind string
}

// resolve classifies a target shape ID. Prelude primitives
// (smithy.api#String etc.) are not in the model's shape map.
func (v *xmlShapeValidator) resolve(target string) (resolvedShape, string) {
	if shape, ok := v.idx.shapes[target]; ok {
		switch shape.Type {
		case "structure":
			return resolvedShape{shape, "structure"}, "structure"
		case "union":
			return resolvedShape{shape, "union"}, "union"
		case "list", "set":
			return resolvedShape{shape, "list"}, "list"
		case "map":
			return resolvedShape{shape, "map"}, "map"
		case "string", "enum":
			return resolvedShape{shape, "string"}, "string"
		case "blob":
			return resolvedShape{shape, "blob"}, "blob"
		case "boolean":
			return resolvedShape{shape, "boolean"}, "boolean"
		case "byte", "short", "integer", "long", "intEnum", "float", "double", "bigInteger", "bigDecimal":
			return resolvedShape{shape, "number"}, "number"
		case "timestamp":
			return resolvedShape{shape, "timestamp"}, "timestamp"
		case "document":
			return resolvedShape{shape, "document"}, "document"
		}
		return resolvedShape{shape, ""}, ""
	}
	if strings.HasPrefix(target, "smithy.api#") {
		switch shortShapeName(target) {
		case "String":
			return resolvedShape{kind: "string"}, "string"
		case "Blob":
			return resolvedShape{kind: "blob"}, "blob"
		case "Boolean", "PrimitiveBoolean":
			return resolvedShape{kind: "boolean"}, "boolean"
		case "Byte", "Short", "Integer", "Long", "Float", "Double",
			"PrimitiveByte", "PrimitiveShort", "PrimitiveInteger", "PrimitiveLong",
			"PrimitiveFloat", "PrimitiveDouble", "BigInteger", "BigDecimal":
			return resolvedShape{kind: "number"}, "number"
		case "Timestamp":
			return resolvedShape{kind: "timestamp"}, "timestamp"
		case "Document":
			return resolvedShape{kind: "document"}, "document"
		case "Unit":
			return resolvedShape{kind: "unit"}, "unit"
		}
	}
	return resolvedShape{}, ""
}

// xmlBoundMember is a structure member with its declared name kept
// alongside the ref (the element-name map is keyed by xmlName).
type xmlBoundMember struct {
	name string
	ref  smithyMemberRef
}

// structureChildren validates an element whose content is a structure:
// every child element must be a declared member (by xmlName), and each
// member value is validated against its target. allowExtra lists
// protocol-envelope elements (requestId, return) that are always legal.
func (v *xmlShapeValidator) structureChildren(shapeLabel string, members map[string]smithyMemberRef, path string, el *xmlNode, allowExtra map[string]bool) {
	elems := map[string]xmlBoundMember{}
	attrs := map[string]xmlBoundMember{}
	for name, ref := range members {
		bound := xmlBoundMember{name: name, ref: ref}
		if hasTrait(ref.Traits, "smithy.api#xmlAttribute") {
			attrs[xmlElementName(name, ref)] = bound
		} else {
			elems[xmlElementName(name, ref)] = bound
		}
	}
	for _, a := range el.attrs {
		// Namespace declarations and unmodeled attributes are protocol
		// furniture (xmlns, xsi); only declared xmlAttribute members are
		// judged.
		if bm, ok := attrs[a.Name.Local]; ok {
			v.scalarValue(bm.ref.Target, path+".@"+bm.name, a.Value)
		}
	}
	if text := strings.TrimSpace(el.text); len(el.children) == 0 && text != "" {
		// Member fusion (S3 GetBucketLocation): a structure whose single
		// string member serializes under the structure's own element name
		// collapses to character data.
		if len(elems) == 1 {
			if bm, ok := elems[el.name]; ok {
				if _, kind := v.resolve(bm.ref.Target); kind == "string" || kind == "blob" {
					return
				}
			}
		}
		v.violate("type-mismatch", path, fmt.Sprintf("spec %s is a structure, response element carries character data %q", shapeLabel, truncateForDetail(text)))
		return
	}
	for _, child := range el.children {
		if allowExtra[child.name] {
			continue
		}
		bm, ok := elems[child.name]
		if !ok {
			v.violate("unknown-field", path+"."+child.name, "member not defined by "+shapeLabel)
			continue
		}
		v.member(bm, path, child)
	}
}

// member validates one member element, handling the list/map wrapper
// rules: query-protocol lists nest items in <member> (or the list
// member's xmlName — EC2 uses <item>) unless the member is
// xmlFlattened, in which case the member element repeats per item; maps
// nest <entry><key/><value/></entry> with the same flattening rule.
func (v *xmlShapeValidator) member(bm xmlBoundMember, parentPath string, el *xmlNode) {
	mpath := parentPath + "." + bm.name
	target, kind := v.resolve(bm.ref.Target)
	switch kind {
	case "list":
		if target.def.Member == nil {
			v.st.skip(v.op, mpath, "list shape "+bm.ref.Target+" has no member target")
			return
		}
		if hasTrait(bm.ref.Traits, "smithy.api#xmlFlattened") {
			v.value(target.def.Member.Target, mpath+"[]", el)
			return
		}
		itemName := xmlElementName("member", *target.def.Member)
		if text := strings.TrimSpace(el.text); len(el.children) == 0 && text != "" {
			v.violate("type-mismatch", mpath, fmt.Sprintf("spec %s is a list, response element carries character data %q", bm.ref.Target, truncateForDetail(text)))
			return
		}
		for _, item := range el.children {
			if item.name != itemName {
				v.violate("unknown-field", mpath+"."+item.name, fmt.Sprintf("items of %s serialize as <%s>", bm.ref.Target, itemName))
				continue
			}
			v.value(target.def.Member.Target, mpath+"[]", item)
		}
	case "map":
		if hasTrait(bm.ref.Traits, "smithy.api#xmlFlattened") {
			v.mapEntry(target.def, bm.ref.Target, mpath, el)
			return
		}
		for _, entry := range el.children {
			if entry.name != "entry" {
				v.violate("unknown-field", mpath+"."+entry.name, fmt.Sprintf("entries of map %s serialize as <entry>", bm.ref.Target))
				continue
			}
			v.mapEntry(target.def, bm.ref.Target, mpath, entry)
		}
	default:
		v.value(bm.ref.Target, mpath, el)
	}
}

// mapEntry validates one <entry> (or flattened member element) of a map.
func (v *xmlShapeValidator) mapEntry(shape smithyShapeDef, shapeID, mpath string, entry *xmlNode) {
	if shape.Key == nil || shape.Value == nil {
		v.st.skip(v.op, mpath, "map shape "+shapeID+" lacks key/value targets")
		return
	}
	keyName := xmlElementName("key", *shape.Key)
	valueName := xmlElementName("value", *shape.Value)
	for _, c := range entry.children {
		switch c.name {
		case keyName:
			v.value(shape.Key.Target, mpath+".entry.key", c)
		case valueName:
			v.value(shape.Value.Target, mpath+".entry.value", c)
		default:
			v.violate("unknown-field", mpath+".entry."+c.name, fmt.Sprintf("map %s entries carry <%s> and <%s>", shapeID, keyName, valueName))
		}
	}
}

// value validates an element whose content IS the target value.
func (v *xmlShapeValidator) value(target, path string, el *xmlNode) {
	shape, kind := v.resolve(target)
	switch kind {
	case "":
		v.st.skip(v.op, path, "target "+target+" is not in the model and not a prelude shape")
	case "structure", "union":
		v.structureChildren(target, shape.def.Members, path, el, nil)
	case "list":
		// A list value outside a structure member (nested lists). Items
		// nest under the list member's xmlName (default <member>).
		if shape.def.Member == nil {
			v.st.skip(v.op, path, "list shape "+target+" has no member target")
			return
		}
		itemName := xmlElementName("member", *shape.def.Member)
		for _, item := range el.children {
			if item.name != itemName {
				v.violate("unknown-field", path+"."+item.name, fmt.Sprintf("items of %s serialize as <%s>", target, itemName))
				continue
			}
			v.value(shape.def.Member.Target, path+"[]", item)
		}
	case "map":
		for _, entry := range el.children {
			if entry.name != "entry" {
				v.violate("unknown-field", path+"."+entry.name, fmt.Sprintf("entries of map %s serialize as <entry>", target))
				continue
			}
			v.mapEntry(shape.def, target, path, entry)
		}
	case "document", "unit":
		// any content / no content
	default: // scalar
		if len(el.children) > 0 {
			v.violate("type-mismatch", path, fmt.Sprintf("spec %s is a %s, response element has child elements", target, kind))
			return
		}
		v.scalarValue(target, path, strings.TrimSpace(el.text))
	}
}

// scalarValue type-checks scalar character data.
func (v *xmlShapeValidator) scalarValue(target, path, text string) {
	_, kind := v.resolve(target)
	switch kind {
	case "number":
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			v.violate("type-mismatch", path, fmt.Sprintf("spec %s is numeric, response has %q", target, truncateForDetail(text)))
		}
	case "boolean":
		if text != "true" && text != "false" {
			v.violate("type-mismatch", path, fmt.Sprintf("spec %s is a boolean, response has %q", target, truncateForDetail(text)))
		}
	case "timestamp":
		if !validXMLTimestamp(text) {
			v.violate("type-mismatch", path, fmt.Sprintf("spec %s is a timestamp, response has %q", target, truncateForDetail(text)))
		}
	}
}

// validXMLTimestamp accepts the XML timestamp encodings AWS emits:
// date-time (RFC 3339, the protocol default), http-date, and
// epoch-seconds (timestampFormat trait overrides).
func validXMLTimestamp(s string) bool {
	if _, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return true
	}
	if _, err := time.Parse(http.TimeFormat, s); err == nil {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil && s != "" {
		return true
	}
	return false
}

// xmlElementName is the wire element name of a member: its xmlName
// trait, else the declared name.
func xmlElementName(name string, ref smithyMemberRef) string {
	if n := traitString(ref.Traits, "smithy.api#xmlName"); n != "" {
		return n
	}
	return name
}

func traitString(traits map[string]json.RawMessage, name string) string {
	raw, ok := traits[name]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func hasTrait(traits map[string]json.RawMessage, name string) bool {
	_, ok := traits[name]
	return ok
}

func truncateForDetail(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// looksLikeXML accepts an XML Content-Type or, absent one, a body that
// starts with an XML declaration / element.
// isEventStreamResponse reports whether the response is an AWS event stream
// (application/vnd.amazon.eventstream) — a framed multi-message body the SDK's
// eventstream decoder consumes, which carries no single XML/JSON envelope to
// shape-validate (e.g. S3 SelectObjectContent, Logs StartLiveTail).
func isEventStreamResponse(respHeader http.Header) bool {
	return strings.Contains(respHeader.Get("Content-Type"), "vnd.amazon.eventstream")
}

func looksLikeXML(respHeader http.Header, respBody []byte) bool {
	if ct := respHeader.Get("Content-Type"); ct != "" {
		return strings.Contains(ct, "xml")
	}
	trimmed := bytes.TrimLeft(respBody, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '<'
}

type xmlNode struct {
	name     string
	attrs    []xml.Attr
	children []*xmlNode
	text     string
}

// parseXMLTree decodes a document into a generic element tree. Element
// names are namespace-local; processing instructions, comments, and
// directives are dropped.
func parseXMLTree(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var root *xmlNode
	var stack []*xmlNode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{name: t.Name.Local, attrs: t.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("multiple root elements")
				}
				root = n
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("no root element")
	}
	return root, nil
}

var awsParamSegment = regexp.MustCompile(`\{[^}]+\}`)

// normalizeAWSPath collapses path parameters so simulator mux patterns
// ({functionName}, {key...}) compare equal to Smithy URI templates
// ({FunctionName}, {Key+}) regardless of label naming. Greedy labels
// normalize to {+}, plain labels to {}; any ?query suffix on a Smithy
// URI (e.g. S3's "?x-id=PutObject") is dropped; a single trailing slash
// is dropped (mux subtree-registration nicety). Shared by the static
// spec-conformance gate and the runtime restXml validator.
func normalizeAWSPath(p string) string {
	if i := strings.Index(p, "?"); i >= 0 {
		p = p[:i]
	}
	p = awsParamSegment.ReplaceAllStringFunc(p, func(s string) string {
		inner := s[1 : len(s)-1]
		if strings.HasSuffix(inner, "+") || strings.HasSuffix(inner, "...") {
			return "{+}"
		}
		return "{}"
	})
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}
