package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// sigv4Service returns the service segment of a SigV4-signed request's
// credential scope ("Credential=AKID/20261105/us-east-1/apigateway/aws4_request"),
// or "" for an unsigned or presigned request.
func sigv4Service(req *http.Request) string {
	auth := req.Header.Get("Authorization")
	_, cred, ok := strings.Cut(auth, "Credential=")
	if !ok {
		return ""
	}
	cred, _, _ = strings.Cut(cred, ",")
	parts := strings.Split(strings.TrimSpace(cred), "/")
	if len(parts) != 5 {
		return ""
	}
	return parts[3]
}

func (st *specValidatorState) validateRestJSON(req *http.Request, service string, status int, respHeader http.Header, respBody []byte) []sim.SpecViolation {
	key, ok := st.restXMLKey(req)
	if !ok {
		return nil
	}
	ops := st.spec.restJSONOps[service]
	cands := ops[key]
	if len(cands) == 0 {
		cands = ops[strings.ReplaceAll(key, "{+}", "{}")]
	}
	if len(cands) == 0 {
		return nil
	}
	op := selectRestXMLOp(cands, req.URL.Query(), req.Header)
	if op == nil {
		st.skip(key, "$", "request query disambiguates none (or several) of the candidate restJson1 operations")
		return nil
	}
	name := op.idx.serviceShort + "." + op.name
	var out []sim.SpecViolation
	def := op.idx.ops[op.name]
	if declared := def.httpCodes; len(declared) > 0 && !slices.Contains(declared, status) && !restJSONBindsStatus(op.idx, def) {
		codes := make([]string, 0, len(declared))
		for _, c := range declared {
			codes = append(codes, strconv.Itoa(c))
		}
		out = append(out, sim.SpecViolation{
			Op: name, Kind: "undeclared-status", Field: "$",
			Detail: fmt.Sprintf("spec declares %s, response has %d", strings.Join(codes, "|"), status),
		})
	}
	if len(respBody) == 0 {
		return out
	}
	st.restJSONBody(op.idx, name, def, respBody, &out)
	return out
}

// restJSONBody checks a restJson1 body: the output structure without its
// header-, prefix-header- and status-bound members, or, when a member carries
// httpPayload, that member's value.
func (st *specValidatorState) restJSONBody(idx *smithyModelIndex, op string, def smithyOpDef, body []byte, out *[]sim.SpecViolation) {
	if def.output == "" || def.output == "smithy.api#Unit" {
		if strings.TrimSpace(string(body)) != "{}" {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "unknown-field", Field: "$", Detail: "operation has no modeled output, response carries a body"})
		}
		return
	}
	shape, ok := idx.shapes[def.output]
	if !ok || shape.Type != "structure" {
		st.skip(op, "$", "output target "+def.output+" is not a modeled structure")
		return
	}
	for _, ref := range shape.Members {
		if !hasTrait(ref.Traits, "smithy.api#httpPayload") {
			continue
		}
		target, ok := idx.shapes[ref.Target]
		if !ok || (target.Type != "structure" && target.Type != "union" && target.Type != "document") ||
			hasTrait(target.Traits, "smithy.api#streaming") {
			// A blob or string payload is raw bytes and an event stream is
			// framed messages, neither JSON to judge.
			return
		}
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			*out = append(*out, sim.SpecViolation{Op: op, Kind: "malformed-json", Field: "$", Detail: err.Error()})
			return
		}
		validateSmithyValue(idx, op, ref.Target, "$", v, out)
		return
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		*out = append(*out, sim.SpecViolation{Op: op, Kind: "malformed-json", Field: "$", Detail: err.Error()})
		return
	}
	validateSmithyValue(idx, op, restJSONBodyShapeID(def.output), "$", v, out)
}

func restJSONBodyShapeID(output string) string { return output + "$body" }

// addRestJSONBodyShape registers, beside an operation's output structure, the
// structure its restJson1 body serializes: the output without its header-,
// prefix-header- and status-bound members.
func addRestJSONBodyShape(shapes map[string]smithyShapeDef, output string) {
	shape, ok := shapes[output]
	if !ok || shape.Type != "structure" {
		return
	}
	body := smithyShapeDef{Type: "structure", Members: map[string]smithyMemberRef{}, Traits: shape.Traits}
	for name, ref := range shape.Members {
		if hasTrait(ref.Traits, "smithy.api#httpHeader") || hasTrait(ref.Traits, "smithy.api#httpPrefixHeaders") ||
			hasTrait(ref.Traits, "smithy.api#httpResponseCode") {
			continue
		}
		body.Members[name] = ref
	}
	shapes[restJSONBodyShapeID(output)] = body
}

// restJSONBindsStatus reports whether the operation's output binds the
// response status to a member (smithy.api#httpResponseCode), as Lambda's Invoke
// StatusCode does: the status then varies with the request, and the http
// trait's code names only one of its values.
func restJSONBindsStatus(idx *smithyModelIndex, def smithyOpDef) bool {
	for _, ref := range idx.shapes[def.output].Members {
		if hasTrait(ref.Traits, "smithy.api#httpResponseCode") {
			return true
		}
	}
	return false
}
