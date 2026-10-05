package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// The request a client sends for one operation, rendered from the vendored
// Smithy model in the operation's own protocol, with every member the input
// declares filled in. A condition key is only ever built from something the
// request carries, so the probe that asks "does the gate build this key for
// this action" has to carry everything a request of that action can carry:
// the tags a create takes, the encryption context a KMS call takes, the
// headers an Amazon S3 write takes.

// iamKeyProbeDepth bounds how deep the filler descends into nested
// structures. Every condition key the gate reads sits within a few levels of
// the input, and Amazon EC2's recursive filter shapes would otherwise expand
// without end.
const iamKeyProbeDepth = 7

// iamKeyProbeModel is one vendored model as the probe renders it.
type iamKeyProbeModel struct {
	idx *smithyModelIndex
	// service is the IAM service prefix the model's actions are authorized
	// under: the SigV4 signing name, except for Amazon CloudWatch, which signs
	// as monitoring and authorizes as cloudwatch.
	service     string
	signingName string
	inputs      map[string]string
	xmlns       string
}

func newIAMKeyProbeModel(idx *smithyModelIndex) *iamKeyProbeModel {
	m := &iamKeyProbeModel{idx: idx, inputs: map[string]string{}}
	for id, shape := range idx.shapes {
		short := id[strings.Index(id, "#")+1:]
		switch shape.Type {
		case "operation":
			if shape.Input != nil {
				m.inputs[short] = shape.Input.Target
			}
		case "service":
			var sigv4 struct {
				Name string `json:"name"`
			}
			if raw, ok := shape.Traits["aws.auth#sigv4"]; ok && json.Unmarshal(raw, &sigv4) == nil {
				m.signingName = sigv4.Name
			}
			m.xmlns = iamKeyProbeXMLNamespace(shape.Traits)
		}
	}
	m.service = m.signingName
	if m.service == "monitoring" {
		m.service = "cloudwatch"
	}
	return m
}

func iamKeyProbeXMLNamespace(traits map[string]json.RawMessage) string {
	var ns struct {
		URI string `json:"uri"`
	}
	if raw, ok := traits["smithy.api#xmlNamespace"]; ok && json.Unmarshal(raw, &ns) == nil {
		return ns.URI
	}
	return ""
}

// iamKeyProbeFill fills one operation's members.
type iamKeyProbeFill struct {
	model     *iamKeyProbeModel
	operation string
	arn       string
	// fixtures names a resource the simulator holds, by lower-cased member
	// name, so a probe of an action whose keys describe its target resource
	// addresses one that exists. A key "<Operation>:<member>" answers for that
	// operation alone, where one member name means different resources.
	fixtures map[string]string
}

func (f *iamKeyProbeFill) shape(target string) (smithyShapeDef, string) {
	if shape, ok := f.model.idx.shapes[target]; ok {
		return shape, shape.Type
	}
	name := strings.ToLower(target[strings.Index(target, "#")+1:])
	name = strings.TrimPrefix(name, "primitive")
	switch name {
	case "unit":
		return smithyShapeDef{}, "structure"
	case "bigdecimal", "biginteger":
		return smithyShapeDef{}, "long"
	}
	return smithyShapeDef{}, name
}

// str is what a client puts in a string member: a resource the simulator
// holds where the probe was given one, a role where the member passes a role,
// and otherwise the value the resource-derivation probe sends.
func (f *iamKeyProbeFill) str(member string) string {
	lower := strings.ToLower(member)
	if value, ok := f.fixtures[f.operation+":"+lower]; ok {
		return value
	}
	if value, ok := f.fixtures[lower]; ok {
		return value
	}
	if strings.Contains(lower, "role") && (strings.HasSuffix(lower, "arn") || strings.HasSuffix(lower, "arns")) {
		return "arn:aws:iam::" + iamProbeAccount + ":role/probe"
	}
	return iamProbeMemberValue(f.model.service, member, f.arn)
}

// iamKeyProbeEnumValue is the first value an enum declares, in either spelling Smithy
// has for one.
func iamKeyProbeEnumValue(shape smithyShapeDef) (string, bool) {
	if shape.Type == "enum" || shape.Type == "intEnum" {
		values := smithyEnumValues(shape)
		if shape.Type == "intEnum" {
			values = values[:0]
			for _, ref := range shape.Members {
				values = append(values, string(ref.Traits["smithy.api#enumValue"]))
			}
			sort.Strings(values)
		}
		if len(values) > 0 {
			return values[0], true
		}
	}
	if raw, ok := shape.Traits["smithy.api#enum"]; ok {
		var entries []struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(raw, &entries) == nil && len(entries) > 0 {
			return entries[0].Value, true
		}
	}
	return "", false
}

// scalar renders a simple member as text, in the timestamp format the
// position calls for.
func (f *iamKeyProbeFill) scalar(target, member, timestampFormat string) string {
	shape, kind := f.shape(target)
	if value, ok := iamKeyProbeEnumValue(shape); ok {
		return value
	}
	switch kind {
	case "boolean":
		return "true"
	case "byte", "short", "integer", "long", "float", "double":
		return "1"
	case "timestamp":
		return iamKeyProbeTimestamp(timestampFormat)
	case "blob":
		return base64.StdEncoding.EncodeToString([]byte("probe"))
	}
	return f.str(member)
}

// iamKeyProbeTime is the instant every probe timestamp names: a day ahead, so
// a retention date or a session expiry reads as one in the future.
var iamKeyProbeTime = time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

func iamKeyProbeTimestamp(format string) string {
	switch format {
	case "epoch-seconds":
		return strconv.FormatInt(iamKeyProbeTime.Unix(), 10)
	case "http-date":
		return iamKeyProbeTime.Format(http.TimeFormat)
	}
	return iamKeyProbeTime.Format(time.RFC3339)
}

func iamKeyProbeTraitString(traits map[string]json.RawMessage, name string) string {
	var s string
	if raw, ok := traits[name]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func iamKeyProbeHasTrait(traits map[string]json.RawMessage, name string) bool {
	_, ok := traits[name]
	return ok
}

func iamKeyProbeSortedMembers(members map[string]smithyMemberRef) []string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// document builds a JSON (or CBOR) value for a shape. jsonName selects the
// member spelling: restJson1 honours smithy.api#jsonName, awsJson does not.
func (f *iamKeyProbeFill) document(target, member string, depth int, seen map[string]bool, jsonName, useCBOR bool) (any, bool) {
	shape, kind := f.shape(target)
	if value, ok := iamKeyProbeEnumValue(shape); ok {
		if kind == "intEnum" {
			n, _ := strconv.Atoi(value)
			return n, true
		}
		return value, true
	}
	switch kind {
	case "structure", "union":
		if depth > iamKeyProbeDepth || seen[target] {
			return nil, false
		}
		seen[target] = true
		defer delete(seen, target)
		object := map[string]any{}
		for _, name := range iamKeyProbeSortedMembers(shape.Members) {
			ref := shape.Members[name]
			value, ok := f.document(ref.Target, name, depth+1, seen, jsonName, useCBOR)
			if !ok {
				continue
			}
			wire := name
			if jsonName {
				if n := iamKeyProbeTraitString(ref.Traits, "smithy.api#jsonName"); n != "" {
					wire = n
				}
			}
			object[wire] = value
			if kind == "union" {
				break
			}
		}
		return object, true
	case "list", "set":
		if shape.Member == nil {
			return nil, false
		}
		element, ok := f.document(shape.Member.Target, member, depth+1, seen, jsonName, useCBOR)
		if !ok {
			return []any{}, true
		}
		return []any{element}, true
	case "map":
		if shape.Key == nil || shape.Value == nil {
			return nil, false
		}
		key := f.scalar(shape.Key.Target, "key", "")
		value, ok := f.document(shape.Value.Target, member, depth+1, seen, jsonName, useCBOR)
		if !ok {
			return map[string]any{}, true
		}
		return map[string]any{key: value}, true
	case "document":
		return map[string]any{}, true
	case "boolean":
		return true, true
	case "byte", "short", "integer", "long":
		return 1, true
	case "float", "double":
		return 1.0, true
	case "timestamp":
		if useCBOR {
			return cbor.Tag{Number: 1, Content: iamKeyProbeTime.Unix()}, true
		}
		return iamKeyProbeTime.Unix(), true
	case "blob":
		if useCBOR {
			return []byte("probe"), true
		}
		return base64.StdEncoding.EncodeToString([]byte("probe")), true
	}
	return f.str(member), true
}

// queryMember writes one member as awsQuery or ec2Query form parameters.
func (f *iamKeyProbeFill) queryMember(form url.Values, prefix string, ref smithyMemberRef, member string, depth int, seen map[string]bool, ec2 bool) {
	shape, kind := f.shape(ref.Target)
	if _, isEnum := iamKeyProbeEnumValue(shape); isEnum {
		kind = "string"
	}
	switch kind {
	case "structure", "union":
		if depth > iamKeyProbeDepth || seen[ref.Target] {
			return
		}
		seen[ref.Target] = true
		defer delete(seen, ref.Target)
		for _, name := range iamKeyProbeSortedMembers(shape.Members) {
			inner := shape.Members[name]
			f.queryMember(form, prefix+"."+iamKeyProbeQueryName(name, inner, ec2), inner, name, depth+1, seen, ec2)
			if kind == "union" {
				break
			}
		}
	case "list", "set":
		if shape.Member == nil {
			return
		}
		element := prefix + ".1"
		if !ec2 && !iamKeyProbeHasTrait(ref.Traits, "smithy.api#xmlFlattened") {
			name := iamKeyProbeTraitString(shape.Member.Traits, "smithy.api#xmlName")
			if name == "" {
				name = "member"
			}
			element = prefix + "." + name + ".1"
		}
		f.queryMember(form, element, *shape.Member, member, depth+1, seen, ec2)
	case "map":
		if shape.Key == nil || shape.Value == nil {
			return
		}
		entry := prefix + ".entry.1"
		if iamKeyProbeHasTrait(ref.Traits, "smithy.api#xmlFlattened") {
			entry = prefix + ".1"
		}
		keyName := iamKeyProbeTraitString(shape.Key.Traits, "smithy.api#xmlName")
		if keyName == "" {
			keyName = "key"
		}
		valueName := iamKeyProbeTraitString(shape.Value.Traits, "smithy.api#xmlName")
		if valueName == "" {
			valueName = "value"
		}
		form.Set(entry+"."+keyName, f.scalar(shape.Key.Target, "key", ""))
		f.queryMember(form, entry+"."+valueName, *shape.Value, member, depth+1, seen, ec2)
	default:
		form.Set(prefix, f.scalar(ref.Target, member, ""))
	}
}

// iamKeyProbeQueryName is the parameter name a member travels under: ec2Query
// prefers its own trait and capitalizes, awsQuery honours xmlName.
func iamKeyProbeQueryName(member string, ref smithyMemberRef, ec2 bool) string {
	if ec2 {
		if n := iamKeyProbeTraitString(ref.Traits, "aws.protocols#ec2QueryName"); n != "" {
			return n
		}
		if n := iamKeyProbeTraitString(ref.Traits, "smithy.api#xmlName"); n != "" {
			return strings.ToUpper(n[:1]) + n[1:]
		}
		return strings.ToUpper(member[:1]) + member[1:]
	}
	if n := iamKeyProbeTraitString(ref.Traits, "smithy.api#xmlName"); n != "" {
		return n
	}
	return member
}

// xmlElement writes one member as restXml.
func (f *iamKeyProbeFill) xmlElement(b *strings.Builder, name string, ref smithyMemberRef, member string, depth int, seen map[string]bool, xmlns string) {
	shape, kind := f.shape(ref.Target)
	if _, isEnum := iamKeyProbeEnumValue(shape); isEnum {
		kind = "string"
	}
	open := "<" + name
	if xmlns != "" {
		open += ` xmlns="` + xmlns + `"`
	}
	open += ">"
	switch kind {
	case "structure", "union":
		if depth > iamKeyProbeDepth || seen[ref.Target] {
			return
		}
		seen[ref.Target] = true
		defer delete(seen, ref.Target)
		b.WriteString(open)
		for _, inner := range iamKeyProbeSortedMembers(shape.Members) {
			innerRef := shape.Members[inner]
			if iamKeyProbeHasTrait(innerRef.Traits, "smithy.api#xmlAttribute") {
				continue
			}
			f.xmlElement(b, iamKeyProbeQueryName(inner, innerRef, false), innerRef, inner, depth+1, seen, "")
			if kind == "union" {
				break
			}
		}
		b.WriteString("</" + name + ">")
	case "list", "set":
		if shape.Member == nil {
			return
		}
		if iamKeyProbeHasTrait(ref.Traits, "smithy.api#xmlFlattened") {
			f.xmlElement(b, name, *shape.Member, member, depth+1, seen, "")
			return
		}
		element := iamKeyProbeTraitString(shape.Member.Traits, "smithy.api#xmlName")
		if element == "" {
			element = "member"
		}
		b.WriteString(open)
		f.xmlElement(b, element, *shape.Member, member, depth+1, seen, "")
		b.WriteString("</" + name + ">")
	case "map":
		if shape.Key == nil || shape.Value == nil {
			return
		}
		b.WriteString(open)
		b.WriteString("<entry><key>" + html.EscapeString(f.scalar(shape.Key.Target, "key", "")) + "</key>")
		f.xmlElement(b, "value", *shape.Value, member, depth+1, seen, "")
		b.WriteString("</entry></" + name + ">")
	default:
		b.WriteString(open + html.EscapeString(f.scalar(ref.Target, member, "")) + "</" + name + ">")
	}
}

// iamKeyProbeEnvelope signs a request the way an SDK does, as the probe
// principal, over TLS from a fixed client address.
func iamKeyProbeEnvelope(r *http.Request, signingName string) {
	r.Header.Set("User-Agent", "aws-sdk-go-v2/1.0 os/linux")
	r.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+iamProbeAccessKeyID+"/"+
		time.Now().UTC().Format("20060102")+"/us-east-1/"+signingName+
		"/aws4_request, SignedHeaders=host;x-amz-date, Signature=00")
	r.RemoteAddr = "10.1.2.3:52000"
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
}

// jsonRequest is an awsJson request: the whole input as the body, the
// operation in X-Amz-Target.
func (f *iamKeyProbeFill) jsonRequest(target, contentType string) *http.Request {
	body, _ := f.document(f.model.inputs[f.operation], "", 0, map[string]bool{}, false, false)
	if body == nil {
		body = map[string]any{}
	}
	payload, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "https://sim.local/", bytes.NewReader(payload))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-Amz-Target", target)
	iamKeyProbeEnvelope(r, f.model.signingName)
	return r
}

// queryRequest is an awsQuery or ec2Query request.
func (f *iamKeyProbeFill) queryRequest(ec2 bool) *http.Request {
	form := url.Values{}
	input, _ := f.shape(f.model.inputs[f.operation])
	for _, name := range iamKeyProbeSortedMembers(input.Members) {
		ref := input.Members[name]
		f.queryMember(form, iamKeyProbeQueryName(name, ref, ec2), ref, name, 1, map[string]bool{}, ec2)
	}
	form.Set("Action", f.operation)
	form.Set("Version", f.model.idx.version)
	r := httptest.NewRequest(http.MethodPost, "https://sim.local/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	iamKeyProbeEnvelope(r, f.model.signingName)
	return r
}

// cborRequest is a Smithy RPC v2 CBOR request.
func (f *iamKeyProbeFill) cborRequest() *http.Request {
	body, _ := f.document(f.model.inputs[f.operation], "", 0, map[string]bool{}, false, true)
	if body == nil {
		body = map[string]any{}
	}
	payload, _ := cbor.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "https://sim.local/service/"+f.model.idx.serviceShort+
		"/operation/"+f.operation, bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/cbor")
	r.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	iamKeyProbeEnvelope(r, f.model.signingName)
	return r
}

// restRequest binds each member where its HTTP trait puts it: the path, the
// query string, a header, or the body.
func (f *iamKeyProbeFill) restRequest(def smithyOpDef, useXML bool) *http.Request {
	input, _ := f.shape(f.model.inputs[f.operation])
	path, rawQuery, _ := strings.Cut(def.httpURI, "?")
	query := url.Values{}
	for _, literal := range strings.Split(rawQuery, "&") {
		if literal == "" {
			continue
		}
		key, value, _ := strings.Cut(literal, "=")
		query.Set(key, value)
	}
	header := make(http.Header)
	var body []byte
	bodyMembers := map[string]smithyMemberRef{}
	for _, name := range iamKeyProbeSortedMembers(input.Members) {
		ref := input.Members[name]
		shape, kind := f.shape(ref.Target)
		switch {
		case iamKeyProbeHasTrait(ref.Traits, "smithy.api#httpLabel"):
			value := f.scalar(ref.Target, name, "")
			if strings.Contains(path, "{"+name+"+}") {
				segments := strings.Split(value, "/")
				for i := range segments {
					segments[i] = url.PathEscape(segments[i])
				}
				path = strings.ReplaceAll(path, "{"+name+"+}", strings.Join(segments, "/"))
			} else {
				path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
			}
		case iamKeyProbeHasTrait(ref.Traits, "smithy.api#httpQuery"):
			key := iamKeyProbeTraitString(ref.Traits, "smithy.api#httpQuery")
			if (kind == "list" || kind == "set") && shape.Member != nil {
				query.Add(key, f.scalar(shape.Member.Target, name, ""))
			} else {
				query.Set(key, f.scalar(ref.Target, name, ""))
			}
		case iamKeyProbeHasTrait(ref.Traits, "smithy.api#httpQueryParams"):
			if shape.Value != nil {
				query.Set("probe", f.scalar(shape.Value.Target, name, ""))
			}
		case iamKeyProbeHasTrait(ref.Traits, "smithy.api#httpHeader"):
			key := iamKeyProbeTraitString(ref.Traits, "smithy.api#httpHeader")
			format := iamKeyProbeTraitString(ref.Traits, "smithy.api#timestampFormat")
			if format == "" {
				format = iamKeyProbeTraitString(shape.Traits, "smithy.api#timestampFormat")
			}
			if format == "" {
				format = "http-date"
			}
			if (kind == "list" || kind == "set") && shape.Member != nil {
				header.Set(key, f.scalar(shape.Member.Target, name, format))
			} else {
				header.Set(key, f.scalar(ref.Target, name, format))
			}
		case iamKeyProbeHasTrait(ref.Traits, "smithy.api#httpPrefixHeaders"):
			if shape.Value != nil {
				header.Set(iamKeyProbeTraitString(ref.Traits, "smithy.api#httpPrefixHeaders")+"probe",
					f.scalar(shape.Value.Target, name, ""))
			}
		case iamKeyProbeHasTrait(ref.Traits, "smithy.api#httpPayload"):
			switch kind {
			case "structure", "union":
				if useXML {
					var b strings.Builder
					root := iamKeyProbeTraitString(ref.Traits, "smithy.api#xmlName")
					if root == "" {
						root = iamKeyProbeTraitString(shape.Traits, "smithy.api#xmlName")
					}
					if root == "" {
						root = ref.Target[strings.Index(ref.Target, "#")+1:]
					}
					f.xmlElement(&b, root, ref, name, 1, map[string]bool{}, f.model.xmlns)
					body = []byte(b.String())
				} else {
					value, _ := f.document(ref.Target, name, 1, map[string]bool{}, true, false)
					body, _ = json.Marshal(value)
				}
			default:
				body = []byte("probe")
			}
		default:
			bodyMembers[name] = ref
		}
	}
	if body == nil && len(bodyMembers) > 0 {
		if useXML {
			var b strings.Builder
			root := iamKeyProbeTraitString(input.Traits, "smithy.api#xmlName")
			if root == "" {
				root = f.model.inputs[f.operation][strings.Index(f.model.inputs[f.operation], "#")+1:]
			}
			ns := iamKeyProbeXMLNamespace(input.Traits)
			if ns == "" {
				ns = f.model.xmlns
			}
			b.WriteString("<" + root + ` xmlns="` + ns + `">`)
			for _, name := range iamKeyProbeSortedMembers(bodyMembers) {
				ref := bodyMembers[name]
				f.xmlElement(&b, iamKeyProbeQueryName(name, ref, false), ref, name, 1, map[string]bool{}, "")
			}
			b.WriteString("</" + root + ">")
			body = []byte(b.String())
		} else {
			object := map[string]any{}
			for name, ref := range bodyMembers {
				value, ok := f.document(ref.Target, name, 1, map[string]bool{}, true, false)
				if !ok {
					continue
				}
				wire := name
				if n := iamKeyProbeTraitString(ref.Traits, "smithy.api#jsonName"); n != "" {
					wire = n
				}
				object[wire] = value
			}
			body, _ = json.Marshal(object)
		}
	}
	target := "https://sim.local" + path
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	r := httptest.NewRequest(def.httpMethod, target, bytes.NewReader(body))
	for key, values := range header {
		r.Header[key] = values
	}
	if len(body) > 0 && r.Header.Get("Content-Type") == "" {
		if useXML {
			r.Header.Set("Content-Type", "application/xml")
		} else {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	if useXML {
		digest := sha256.Sum256(body)
		r.Header.Set("x-amz-content-sha256", hex.EncodeToString(digest[:]))
	}
	iamKeyProbeEnvelope(r, f.model.signingName)
	return r
}
