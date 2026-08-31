package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Runtime wire-shape validation against the vendored Smithy models
// (specs/cloud-api/aws/). Armed when SOCKERLESS_SPEC_VALIDATE (report
// file) is set; SOCKERLESS_SPEC_DIR must then point at the vendored spec
// directory. Coverage:
//
//   - awsJson1.0 / awsJson1.1 (X-Amz-Target routing): success responses
//     are validated member-by-member against the operation's output
//     shape — members the spec doesn't define and primitive type
//     mismatches are violations.
//   - awsQuery / ec2Query (POST / with Action+Version form params):
//     success XML is validated against the protocol envelope
//     (<OpResponse><OpResult>…</OpResult><ResponseMetadata/> for
//     awsQuery; flat <OpResponse> + <requestId> for ec2Query) and the
//     output shape, honoring xmlName / xmlFlattened traits. See
//     spec_validator_xml.go.
//   - restXml (S3 / CloudFront / Route 53 REST routes): the operation is
//     identified by matching the served mux pattern + query-string
//     literals against the models' smithy.api#http traits; the XML body
//     is validated against the output shape with httpHeader /
//     httpPrefixHeaders / httpResponseCode members excluded and
//     httpPayload members serving as the document root.
//
// restJson1 path operations are exercised by the static surface gate +
// SDK suites; their shape validation rides the same report mechanism
// when added.

type smithyShapeDef struct {
	Type    string                     `json:"type"`
	Version string                     `json:"version"`
	Member  *smithyMemberRef           `json:"member"`  // list
	Key     *smithyMemberRef           `json:"key"`     // map
	Value   *smithyMemberRef           `json:"value"`   // map
	Members map[string]smithyMemberRef `json:"members"` // structure/union/enum
	Input   *smithyMemberRef           `json:"input"`   // operation
	Output  *smithyMemberRef           `json:"output"`  // operation
	Traits  map[string]json.RawMessage `json:"traits"`
}

type smithyMemberRef struct {
	Target string                     `json:"target"`
	Traits map[string]json.RawMessage `json:"traits"`
}

// smithyOpDef is one operation's validation-relevant surface: its output
// shape and, for REST protocols, the smithy.api#http binding.
type smithyOpDef struct {
	// output is the output shape ID; "smithy.api#Unit" (or "") when the
	// operation has no modeled output.
	output     string
	httpMethod string
	httpURI    string
}

type smithyModelIndex struct {
	shapes map[string]smithyShapeDef
	// patterns memoizes compiled smithy.api#pattern traits by shape id; a nil
	// entry records "this shape declares no usable pattern".
	patterns   map[string]*regexp.Regexp
	patternsMu sync.Mutex
	// ops maps short operation name -> operation definition.
	ops map[string]smithyOpDef
	// serviceShort is the service shape's short name (the Go SDK's
	// X-Amz-Target prefix; botocore may prefix it with a java-style
	// namespace).
	serviceShort string
	// version is the service shape version (the query-protocol Version
	// form parameter).
	version string
	// protocols holds the service's aws.protocols#* / smithy.protocols#*
	// trait short names (awsQuery, ec2Query, restXml, awsJson1_0, ...).
	protocols map[string]bool
}

// smithySpecSet is every vendored model, indexed for the three wire
// protocols the validator covers.
type smithySpecSet struct {
	// byShort indexes models by service shape short name (awsJson
	// X-Amz-Target prefix).
	byShort map[string]*smithyModelIndex
	// queryModels lists models speaking awsQuery or ec2Query;
	// queryByVersion groups them by the Version form parameter.
	queryModels    []*smithyModelIndex
	queryByVersion map[string][]*smithyModelIndex
	// restXMLOps indexes restXml operations by "METHOD <normalized-uri>"
	// (see normalizeAWSPath); several operations can share a key and are
	// disambiguated by their URI query-string literals.
	restXMLOps map[string][]restXMLOp
}

// restXMLOp is one restXml operation candidate for a method+path key.
type restXMLOp struct {
	idx      *smithyModelIndex
	name     string
	literals []uriQueryLiteral
	// requiredQuery lists the input members bound to the query string
	// (smithy.api#httpQuery) that are smithy.api#required — every wire
	// request for the operation carries them, so they disambiguate
	// candidates sharing a path (ListParts/UploadPart vs Get/PutObject).
	requiredQuery []string
	// requiredHeader lists the input members bound to a request header
	// (smithy.api#httpHeader) that are smithy.api#required — the
	// header-borne analogue of requiredQuery. Disambiguates candidates
	// that share a path AND the same required query members but differ
	// by a required header (UploadPart vs UploadPartCopy, the latter
	// requiring x-amz-copy-source).
	requiredHeader []string
	// staticContext marks operations carrying
	// smithy.rules#staticContextParams — endpoint-rule variants served
	// from dedicated endpoints (S3 Express ListDirectoryBuckets), never
	// the plain regional surface the simulator hosts.
	staticContext bool
}

// uriQueryLiteral is one query-string literal from a smithy.api#http uri
// ("?list-type=2" -> {list-type, 2, true}; "?delete" -> {delete, "", false}).
type uriQueryLiteral struct {
	key      string
	value    string
	hasValue bool
}

// applySmithySupplement corrects a model's shapes from the supplement beside
// it, if it has one.
//
// A correction exists where a shape's declared pattern is stricter than the
// service it describes, so no value AWS returns for a member of that shape can
// match it. Correcting the pattern keeps the member validated against what the
// service really returns; the alternative — listing the field as an accepted
// violation — stops checking the value at all, and would let the simulator
// return anything there.
//
// Each correction pins the exact upstream pattern it replaces. A re-vendor that
// changes that text fails here rather than silently applying a correction
// written against something else — which is how a correction that upstream has
// already made gets noticed and deleted.
func applySmithySupplement(modelPath string, shapes map[string]smithyShapeDef) error {
	path := strings.TrimSuffix(modelPath, ".smithy.json.gz") + ".supplement.json"
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var supplement struct {
		Shapes map[string]struct {
			Pattern *struct {
				Replaces string `json:"replaces"`
				With     string `json:"with"`
			} `json:"pattern"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(body, &supplement); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for id, correction := range supplement.Shapes {
		shape, ok := shapes[id]
		if !ok {
			return fmt.Errorf("%s: corrects shape %q, which the model does not define", path, id)
		}
		if correction.Pattern == nil {
			continue
		}
		raw, ok := shape.Traits["smithy.api#pattern"]
		if !ok {
			return fmt.Errorf("%s: corrects the pattern of %s, which declares none", path, id)
		}
		var declared string
		if err := json.Unmarshal(raw, &declared); err != nil {
			return fmt.Errorf("%s: %s declares an unreadable pattern: %w", path, id, err)
		}
		if declared != correction.Pattern.Replaces {
			return fmt.Errorf("%s: corrects the pattern of %s, but the model now declares %q rather than the %q the correction was written against — recheck whether it is still needed and update or delete it",
				path, id, declared, correction.Pattern.Replaces)
		}
		if _, err := regexp.Compile(correction.Pattern.With); err != nil {
			return fmt.Errorf("%s: the corrected pattern for %s does not compile: %w", path, id, err)
		}
		corrected, err := json.Marshal(correction.Pattern.With)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		shape.Traits["smithy.api#pattern"] = corrected
		shapes[id] = shape
	}
	return nil
}

func loadSmithySpecSet(dir string) (*smithySpecSet, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.smithy.json.gz"))
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("no Smithy models under %s (glob err: %v)", dir, err)
	}
	set := &smithySpecSet{
		byShort:        map[string]*smithyModelIndex{},
		queryByVersion: map[string][]*smithyModelIndex{},
		restXMLOps:     map[string][]restXMLOp{},
	}
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
		var doc struct {
			Shapes map[string]smithyShapeDef `json:"shapes"`
		}
		err = json.NewDecoder(gz).Decode(&doc)
		_ = gz.Close()
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if err := applySmithySupplement(p, doc.Shapes); err != nil {
			return nil, err
		}
		idx := &smithyModelIndex{
			shapes:    doc.Shapes,
			ops:       map[string]smithyOpDef{},
			protocols: map[string]bool{},
		}
		opShapes := map[string]smithyShapeDef{}
		for id, shape := range doc.Shapes {
			short := id
			if i := strings.Index(id, "#"); i >= 0 {
				short = id[i+1:]
			}
			switch shape.Type {
			case "service":
				idx.serviceShort = short
				idx.version = shape.Version
				for trait := range shape.Traits {
					if strings.HasPrefix(trait, "aws.protocols#") || strings.HasPrefix(trait, "smithy.protocols#") {
						idx.protocols[strings.SplitN(trait, "#", 2)[1]] = true
					}
				}
			case "operation":
				def := smithyOpDef{}
				if shape.Output != nil {
					def.output = shape.Output.Target
				}
				if raw, ok := shape.Traits["smithy.api#http"]; ok {
					var h struct {
						Method string `json:"method"`
						URI    string `json:"uri"`
					}
					if err := json.Unmarshal(raw, &h); err != nil {
						return nil, fmt.Errorf("%s: bad smithy.api#http on %s: %w", p, id, err)
					}
					def.httpMethod = h.Method
					def.httpURI = h.URI
				}
				idx.ops[short] = def
				opShapes[short] = shape
			}
		}
		if idx.serviceShort == "" {
			return nil, fmt.Errorf("%s: no service shape", p)
		}
		set.byShort[idx.serviceShort] = idx
		if idx.protocols["awsQuery"] || idx.protocols["ec2Query"] {
			set.queryModels = append(set.queryModels, idx)
			set.queryByVersion[idx.version] = append(set.queryByVersion[idx.version], idx)
		}
		if idx.protocols["restXml"] {
			for name, def := range idx.ops {
				if def.httpMethod == "" {
					continue
				}
				opShape := opShapes[name]
				key := def.httpMethod + " " + normalizeAWSPath(def.httpURI)
				set.restXMLOps[key] = append(set.restXMLOps[key], restXMLOp{
					idx:            idx,
					name:           name,
					literals:       parseURIQueryLiterals(def.httpURI),
					requiredQuery:  requiredQueryMembers(doc.Shapes, opShape),
					requiredHeader: requiredHeaderMembers(doc.Shapes, opShape),
					staticContext:  hasTrait(opShape.Traits, "smithy.rules#staticContextParams"),
				})
			}
		}
	}
	return set, nil
}

// requiredQueryMembers lists an operation input's required
// query-string-bound member keys (smithy.api#httpQuery +
// smithy.api#required).
func requiredQueryMembers(shapes map[string]smithyShapeDef, op smithyShapeDef) []string {
	if op.Input == nil {
		return nil
	}
	input, ok := shapes[op.Input.Target]
	if !ok {
		return nil
	}
	var out []string
	for _, ref := range input.Members {
		if !hasTrait(ref.Traits, "smithy.api#required") {
			continue
		}
		if q := traitString(ref.Traits, "smithy.api#httpQuery"); q != "" {
			out = append(out, q)
		}
	}
	return out
}

// requiredHeaderMembers lists an operation input's required
// header-bound member names (smithy.api#httpHeader +
// smithy.api#required), each normalized to its lowercase header name.
func requiredHeaderMembers(shapes map[string]smithyShapeDef, op smithyShapeDef) []string {
	if op.Input == nil {
		return nil
	}
	input, ok := shapes[op.Input.Target]
	if !ok {
		return nil
	}
	var out []string
	for _, ref := range input.Members {
		if !hasTrait(ref.Traits, "smithy.api#required") {
			continue
		}
		if h := traitString(ref.Traits, "smithy.api#httpHeader"); h != "" {
			out = append(out, strings.ToLower(h))
		}
	}
	return out
}

// parseURIQueryLiterals extracts the query-string literals from a
// smithy.api#http uri ("/{Bucket}?list-type=2&x-id=ListObjectsV2").
func parseURIQueryLiterals(uri string) []uriQueryLiteral {
	_, q, ok := strings.Cut(uri, "?")
	if !ok || q == "" {
		return nil
	}
	var out []uriQueryLiteral
	for _, pair := range strings.Split(q, "&") {
		if pair == "" {
			continue
		}
		k, v, hasValue := strings.Cut(pair, "=")
		out = append(out, uriQueryLiteral{key: k, value: v, hasValue: hasValue})
	}
	return out
}

// armSpecValidator wires runtime shape validation onto the server when
// SOCKERLESS_SPEC_VALIDATE is set. Hard failure when the spec dir is
// missing: the operator asked for validation. Must run after every
// service registered its routes — restXml operation identification
// resolves the served mux pattern.
func armSpecValidator(srv *sim.Server) error {
	if os.Getenv("SOCKERLESS_SPEC_VALIDATE") == "" {
		return nil
	}
	dir := os.Getenv("SOCKERLESS_SPEC_DIR")
	if dir == "" {
		return fmt.Errorf("SOCKERLESS_SPEC_VALIDATE is set but SOCKERLESS_SPEC_DIR is not")
	}
	set, err := loadSmithySpecSet(dir)
	if err != nil {
		return err
	}
	st := &specValidatorState{spec: set, mux: srv.Mux(), logger: srv.Logger()}
	srv.SetSpecValidator(st.validate)
	return nil
}

// validate inspects one exchange and dispatches to the protocol-specific
// validators. Error responses (status >= 400) and empty bodies are
// skipped on every protocol — the spec gate covers success shapes.
func (st *specValidatorState) validate(req *http.Request, reqBody []byte, status int, respHeader http.Header, respBody []byte) []sim.SpecViolation {
	if target := req.Header.Get("X-Amz-Target"); target != "" {
		return st.validateJSONTarget(target, status, respHeader, respBody)
	}
	if status >= 400 || len(respBody) == 0 {
		return nil
	}
	if req.Method == http.MethodPost && req.URL.Path == "/" {
		return st.validateQueryAction(reqBody, respHeader, respBody)
	}
	return st.validateRestXML(req, respHeader, respBody)
}

// validateJSONTarget covers the awsJson1.0 / awsJson1.1 protocols.
func (st *specValidatorState) validateJSONTarget(target string, status int, respHeader http.Header, respBody []byte) []sim.SpecViolation {
	if status >= 400 || len(respBody) == 0 {
		return nil // error/empty response
	}
	ct := respHeader.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "json") {
		return nil
	}
	i := strings.LastIndex(target, ".")
	if i < 0 {
		return nil
	}
	prefix, op := target[:i], target[i+1:]
	idx, ok := st.spec.byShort[prefix]
	if !ok {
		// botocore-style dotted prefix: last component is the shape name.
		if j := strings.LastIndex(prefix, "."); j >= 0 {
			idx, ok = st.spec.byShort[prefix[j+1:]]
		}
	}
	if !ok {
		return nil // surface gate owns unknown targets
	}
	def, ok := idx.ops[op]
	if !ok || def.output == "" {
		return nil
	}
	var body any
	if err := json.Unmarshal(respBody, &body); err != nil {
		return []sim.SpecViolation{{Op: target, Kind: "malformed-json", Field: "$", Detail: err.Error()}}
	}
	var out []sim.SpecViolation
	validateSmithyValue(idx, target, def.output, "$", body, &out)
	return out
}

// validateSmithyValue walks a decoded JSON value against a Smithy shape,
// reporting members the spec doesn't define and primitive type
// mismatches. Null values are always acceptable (omitted members).
func validateSmithyValue(idx *smithyModelIndex, op, shapeID, path string, v any, out *[]sim.SpecViolation) {
	if v == nil {
		return
	}
	shape, ok := idx.shapes[shapeID]
	if !ok {
		// Prelude primitives (smithy.api#String etc.) are not in the
		// model's shape map.
		validateSmithyPrimitive(op, shapeID, path, v, out)
		return
	}
	switch shape.Type {
	case "structure", "union":
		obj, ok := v.(map[string]any)
		if !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s is a %s, response has %T", shapeID, shape.Type, v)})
			return
		}
		// A response field is valid under either the member name or its
		// jsonName. The `jsonName` trait only governs the wire key for the
		// REST protocols (restJson1/restXml); awsJson1.0/1.1 codegen ignores
		// it and uses the member name verbatim — verified against the
		// aws-sdk-go-v2 Glue deserializer, which reads `case "SparkConnect"`
		// for the `GetSessionEndpointResponse.SparkConnect` member despite its
		// `jsonName: "SPARK_CONNECT"`. Accepting both keys validates either
		// protocol's wire form while still flagging genuinely-unknown fields.
		byJSONName := make(map[string]smithyMemberRef, len(shape.Members))
		for name, ref := range shape.Members {
			byJSONName[name] = ref
			if raw, ok := ref.Traits["smithy.api#jsonName"]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil && s != "" {
					byJSONName[s] = ref
				}
			}
		}
		for key, val := range obj {
			ref, ok := byJSONName[key]
			if !ok {
				*out = append(*out, sim.SpecViolation{Op: op, Kind: "unknown-field", Field: path + "." + key, Detail: "member not defined by " + shapeID})
				continue
			}
			validateSmithyValue(idx, op, ref.Target, path+"."+key, val, out)
		}
	case "list", "set":
		arr, ok := v.([]any)
		if !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s is a list, response has %T", shapeID, v)})
			return
		}
		if shape.Member != nil {
			for i, item := range arr {
				validateSmithyValue(idx, op, shape.Member.Target, fmt.Sprintf("%s[%d]", path, i), item, out)
			}
		}
	case "map":
		obj, ok := v.(map[string]any)
		if !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s is a map, response has %T", shapeID, v)})
			return
		}
		if shape.Value != nil {
			for key, val := range obj {
				validateSmithyValue(idx, op, shape.Value.Target, path+"."+key, val, out)
			}
		}
	case "string", "enum", "blob":
		s, ok := v.(string)
		if !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s is a %s, response has %T", shapeID, shape.Type, v)})
			return
		}
		// A smithy.api#pattern is as much a part of the wire contract as the
		// type is, and it is where identity-bearing strings are pinned: an ARN
		// or an instance id whose shape carries a pattern is not merely "a
		// string", and a value well-formed enough to deserialize can still name
		// something that does not exist. Checking the type alone accepts those
		// silently, which is how a malformed ARN reached every AWS
		// Organizations response.
		if re := idx.pattern(shapeID, shape); re != nil && !re.MatchString(s) {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "pattern-mismatch", Field: path,
				Detail: fmt.Sprintf("spec %s requires %s, response has %q", shapeID, re.String(), s)})
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s is a boolean, response has %T", shapeID, v)})
		}
	case "byte", "short", "integer", "long", "intEnum", "float", "double", "bigInteger", "bigDecimal":
		if _, ok := v.(float64); !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s is numeric (%s), response has %T", shapeID, shape.Type, v)})
		}
	case "timestamp":
		// awsJson default is epoch-seconds (number); timestampFormat
		// traits allow string encodings.
		switch v.(type) {
		case float64, string:
		default:
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s is a timestamp, response has %T", shapeID, v)})
		}
	case "document":
		// any JSON
	}
}

// pattern returns the compiled smithy.api#pattern of a string shape, or nil
// when the shape declares none. Compilation is memoized because the validator
// re-reaches the same shapes on every response, and a pattern the Go regexp
// engine cannot compile is treated as no constraint rather than as a
// violation — the validator refuses to judge what it cannot read.
//
// Smithy patterns are unanchored unless the expression anchors itself, which
// is exactly MatchString's semantics.
func (idx *smithyModelIndex) pattern(shapeID string, shape smithyShapeDef) *regexp.Regexp {
	idx.patternsMu.Lock()
	defer idx.patternsMu.Unlock()
	if re, done := idx.patterns[shapeID]; done {
		return re
	}
	var re *regexp.Regexp
	if raw, ok := shape.Traits["smithy.api#pattern"]; ok {
		var expr string
		if json.Unmarshal(raw, &expr) == nil && expr != "" {
			re, _ = regexp.Compile(expr)
		}
	}
	if idx.patterns == nil {
		idx.patterns = map[string]*regexp.Regexp{}
	}
	idx.patterns[shapeID] = re
	return re
}

// validateSmithyPrimitive covers smithy.api# prelude targets.
func validateSmithyPrimitive(op, shapeID, path string, v any, out *[]sim.SpecViolation) {
	short := shapeID
	if i := strings.Index(shapeID, "#"); i >= 0 {
		short = shapeID[i+1:]
	}
	switch short {
	case "String", "Blob":
		if _, ok := v.(string); !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s, response has %T", shapeID, v)})
		}
	case "Boolean", "PrimitiveBoolean":
		if _, ok := v.(bool); !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s, response has %T", shapeID, v)})
		}
	case "Byte", "Short", "Integer", "Long", "Float", "Double", "PrimitiveByte", "PrimitiveShort", "PrimitiveInteger", "PrimitiveLong", "PrimitiveFloat", "PrimitiveDouble", "BigInteger", "BigDecimal":
		if _, ok := v.(float64); !ok {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s, response has %T", shapeID, v)})
		}
	case "Timestamp":
		switch v.(type) {
		case float64, string:
		default:
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "type-mismatch", Field: path, Detail: fmt.Sprintf("spec %s, response has %T", shapeID, v)})
		}
	case "Document", "Unit":
		// any JSON / no value
	}
}
