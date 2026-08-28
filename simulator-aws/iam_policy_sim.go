package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// IAM policy simulation: SimulateCustomPolicy / SimulatePrincipalPolicy.
// Implements a real (if compact) policy-evaluation engine over the IAM policy
// JSON the consumer renders from terraform, so least-privilege IAM can be
// asserted against the sim. Real API:
// https://docs.aws.amazon.com/IAM/latest/APIReference/API_SimulateCustomPolicy.html

// iamStringOrList unmarshals an IAM field that may be a single string or a
// JSON array of strings (Action/Resource/condition values all allow both).
type iamStringOrList []string

func (s *iamStringOrList) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*s = []string{one}
	return nil
}

type iamStatement struct {
	Sid          string                                `json:"Sid"`
	Effect       string                                `json:"Effect"`
	Action       iamStringOrList                       `json:"Action"`
	NotAction    iamStringOrList                       `json:"NotAction"`
	Resource     iamStringOrList                       `json:"Resource"`
	NotResource  iamStringOrList                       `json:"NotResource"`
	Principal    iamPrincipal                          `json:"Principal"`
	NotPrincipal iamPrincipal                          `json:"NotPrincipal"`
	Condition    map[string]map[string]iamStringOrList `json:"Condition"`
}

// iamPrincipal models a resource-based / trust policy Principal: either the
// wildcard string "*" or an object mapping a type (AWS, Service, Federated, …)
// to one or more values.
type iamPrincipal struct {
	Wildcard bool
	AWS      []string
	Service  []string
	Other    []string
	set      bool
}

func (p *iamPrincipal) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	p.set = true
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "*" {
			p.Wildcard = true
		} else {
			p.AWS = []string{s}
		}
		return nil
	}
	var obj map[string]iamStringOrList
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	for k, v := range obj {
		switch k {
		case "AWS":
			p.AWS = append(p.AWS, v...)
		case "Service":
			p.Service = append(p.Service, v...)
		default:
			p.Other = append(p.Other, v...)
		}
	}
	return nil
}

// iamPrincipalMatches reports whether a statement's Principal admits the caller.
// A statement with no Principal (identity-based) always matches; "*" matches
// everyone; an AWS principal matches by ARN glob or by the account id of an
// account-root principal; a Service principal matches when an AWS service made
// the call on the principal's behalf (the calling services arrive via
// ctx["aws:CalledVia"]).
func iamPrincipalMatches(stmt iamStatement, callerArn string, ctx map[string][]string) bool {
	matchOne := func(p iamPrincipal) bool {
		if p.Wildcard {
			return true
		}
		for _, want := range p.AWS {
			if want == "*" || iamGlobMatch(want, callerArn) {
				return true
			}
			if acct := iamAccountFromArn(want); acct != "" && strings.Contains(callerArn, ":"+acct+":") {
				return true
			}
			// A role principal (…:role/NAME) matches a session assumed from
			// that role (…:assumed-role/NAME/SESSION).
			if strings.Contains(want, ":role/") {
				if rn := iamRoleNameFromArn(want); rn != "" && strings.Contains(callerArn, ":assumed-role/"+rn+"/") {
					return true
				}
			}
		}
		for _, want := range p.Service {
			for _, svc := range ctx["aws:CalledVia"] {
				if want == svc {
					return true
				}
			}
		}
		return false
	}
	if stmt.NotPrincipal.set && matchOne(stmt.NotPrincipal) {
		return false
	}
	if !stmt.Principal.set {
		return true
	}
	return matchOne(stmt.Principal)
}

// iamAccountFromArn returns the account id of an account-principal — a bare
// 12-digit account id or an arn:aws:iam::ACCT:root ARN — and "" for any
// resource-specific principal (a role/user ARN binds only that principal, not
// the whole account).
func iamAccountFromArn(s string) string {
	if !strings.Contains(s, ":") {
		if len(s) == 12 && strings.Trim(s, "0123456789") == "" {
			return s
		}
		return ""
	}
	if !strings.HasSuffix(s, ":root") {
		return ""
	}
	parts := strings.Split(s, ":")
	if len(parts) >= 5 && len(parts[4]) == 12 {
		return parts[4]
	}
	return ""
}

type iamPolicyDoc struct {
	Version   string
	Statement []iamStatement
}

// parseIAMPolicy parses a policy document whose `Statement` may be a single
// object or an array.
func parseIAMPolicy(s string) (iamPolicyDoc, error) {
	var raw struct {
		Version   string          `json:"Version"`
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return iamPolicyDoc{}, err
	}
	doc := iamPolicyDoc{Version: raw.Version}
	trimmed := strings.TrimSpace(string(raw.Statement))
	if trimmed == "" || trimmed == "null" {
		return doc, nil
	}
	if trimmed[0] == '[' {
		if err := json.Unmarshal(raw.Statement, &doc.Statement); err != nil {
			return doc, err
		}
	} else {
		var one iamStatement
		if err := json.Unmarshal(raw.Statement, &one); err != nil {
			return doc, err
		}
		doc.Statement = []iamStatement{one}
	}
	return doc, nil
}

// iamGlobMatch matches an IAM pattern (with `*` and `?`) against value.
func iamGlobMatch(pattern, value string) bool {
	// Classic two-pointer glob with backtracking on `*`.
	var p, v, star, mark int
	star = -1
	for v < len(value) {
		if p < len(pattern) && (pattern[p] == '?' || pattern[p] == value[v]) {
			p++
			v++
		} else if p < len(pattern) && pattern[p] == '*' {
			star = p
			mark = v
			p++
		} else if star != -1 {
			p = star + 1
			mark++
			v = mark
		} else {
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func iamActionMatches(stmt iamStatement, action string) bool {
	lower := strings.ToLower(action)
	if len(stmt.NotAction) > 0 {
		for _, p := range stmt.NotAction {
			if iamGlobMatch(strings.ToLower(p), lower) {
				return false
			}
		}
		return true
	}
	for _, p := range stmt.Action {
		if iamGlobMatch(strings.ToLower(p), lower) {
			return true
		}
	}
	return false
}

func iamResourceMatches(stmt iamStatement, resource string) bool {
	match := func(pattern string) bool {
		return iamGlobMatch(pattern, resource) || iamLogGroupPatternMatches(pattern, resource)
	}
	if len(stmt.NotResource) > 0 {
		for _, p := range stmt.NotResource {
			if match(p) {
				return false
			}
		}
		return true
	}
	if len(stmt.Resource) == 0 {
		return true // no resource constraint (e.g. an action with "*" resource)
	}
	for _, p := range stmt.Resource {
		if match(p) {
			return true
		}
	}
	return false
}

// iamLogGroupPatternMatches covers the one place CloudWatch Logs spells a log
// group's ARN two ways and authorizes against both.
//
// The AWS Service Reference declares the log-group resource format with no
// suffix — "…:log-group:${LogGroupName}" — and that is the form a request
// derives. But DescribeLogGroups RETURNS the ARN with a trailing ":*", and that
// is the form policies are written in: AWS's own documentation examples, the
// policies the console generates, and Terraform modules. Matching only the
// declared form denies a role holding exactly the grant it needs, and because
// it is the RESOURCE that failed to match, the denial reads "no identity-based
// policy allows the logs:FilterLogEvents action" — sending the reader to hunt
// for a missing action that is plainly present in the policy.
//
// Only log-group-level resources qualify. A log-stream ARN carries its own
// segment and keeps matching exactly, so a grant on one stream cannot widen to
// the whole group.
func iamLogGroupPatternMatches(pattern, resource string) bool {
	if !strings.HasSuffix(pattern, ":*") {
		return false
	}
	if !strings.Contains(resource, ":log-group:") || strings.Contains(resource, ":log-stream:") {
		return false
	}
	return iamGlobMatch(strings.TrimSuffix(pattern, ":*"), resource)
}

func iamAnyMatch(ctxVals, wantVals []string, eq func(ctx, want string) bool) bool {
	for _, c := range ctxVals {
		for _, want := range wantVals {
			if eq(c, want) {
				return true
			}
		}
	}
	return false
}

// iamEvalConditionOp evaluates one (base) condition operator against a set of
// context values and a set of wanted values, with the default ForAnyValue
// semantics (the condition holds if any context value satisfies the operator
// against any wanted value). The full real-AWS operator set is implemented;
// genuinely unknown operators return false (the gated statement doesn't apply)
// so an Allow can't spuriously grant on a condition the sim can't evaluate.
func iamEvalConditionOp(op string, ctxVals, wantVals []string) bool {
	switch op {
	case "StringEquals", "ArnEquals":
		return iamAnyMatch(ctxVals, wantVals, func(c, w string) bool { return c == w })
	case "StringNotEquals", "ArnNotEquals":
		return !iamAnyMatch(ctxVals, wantVals, func(c, w string) bool { return c == w })
	case "StringEqualsIgnoreCase":
		return iamAnyMatch(ctxVals, wantVals, strings.EqualFold)
	case "StringNotEqualsIgnoreCase":
		return !iamAnyMatch(ctxVals, wantVals, strings.EqualFold)
	case "StringLike", "ArnLike":
		return iamAnyMatch(ctxVals, wantVals, func(c, w string) bool { return iamGlobMatch(w, c) })
	case "StringNotLike", "ArnNotLike":
		return !iamAnyMatch(ctxVals, wantVals, func(c, w string) bool { return iamGlobMatch(w, c) })
	case "Bool":
		return iamAnyMatch(ctxVals, wantVals, strings.EqualFold)
	case "NumericEquals":
		return iamAnyMatch(ctxVals, wantVals, iamNumCmp("=="))
	case "NumericNotEquals":
		return !iamAnyMatch(ctxVals, wantVals, iamNumCmp("=="))
	case "NumericLessThan":
		return iamAnyMatch(ctxVals, wantVals, iamNumCmp("<"))
	case "NumericLessThanEquals":
		return iamAnyMatch(ctxVals, wantVals, iamNumCmp("<="))
	case "NumericGreaterThan":
		return iamAnyMatch(ctxVals, wantVals, iamNumCmp(">"))
	case "NumericGreaterThanEquals":
		return iamAnyMatch(ctxVals, wantVals, iamNumCmp(">="))
	case "DateEquals":
		return iamAnyMatch(ctxVals, wantVals, iamDateCmp("=="))
	case "DateNotEquals":
		return !iamAnyMatch(ctxVals, wantVals, iamDateCmp("=="))
	case "DateLessThan":
		return iamAnyMatch(ctxVals, wantVals, iamDateCmp("<"))
	case "DateLessThanEquals":
		return iamAnyMatch(ctxVals, wantVals, iamDateCmp("<="))
	case "DateGreaterThan":
		return iamAnyMatch(ctxVals, wantVals, iamDateCmp(">"))
	case "DateGreaterThanEquals":
		return iamAnyMatch(ctxVals, wantVals, iamDateCmp(">="))
	case "IpAddress":
		return iamAnyMatch(ctxVals, wantVals, iamIPInCIDR)
	case "NotIpAddress":
		return !iamAnyMatch(ctxVals, wantVals, iamIPInCIDR)
	case "BinaryEquals":
		return iamAnyMatch(ctxVals, wantVals, iamBinaryEqual)
	default:
		return false
	}
}

// iamBinaryEqual compares two base64-encoded binary values byte-for-byte.
func iamBinaryEqual(c, w string) bool {
	cb, err1 := base64.StdEncoding.DecodeString(strings.TrimSpace(c))
	wb, err2 := base64.StdEncoding.DecodeString(strings.TrimSpace(w))
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(cb, wb)
}

// iamNumCmp returns an equality/ordering comparator for the numeric operators.
func iamNumCmp(cmp string) func(c, w string) bool {
	return func(c, w string) bool {
		cf, err1 := strconv.ParseFloat(strings.TrimSpace(c), 64)
		wf, err2 := strconv.ParseFloat(strings.TrimSpace(w), 64)
		if err1 != nil || err2 != nil {
			return false
		}
		switch cmp {
		case "==":
			return cf == wf
		case "<":
			return cf < wf
		case "<=":
			return cf <= wf
		case ">":
			return cf > wf
		case ">=":
			return cf >= wf
		}
		return false
	}
}

func iamParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	// Epoch seconds are also accepted by IAM date conditions.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}

func iamDateCmp(cmp string) func(c, w string) bool {
	return func(c, w string) bool {
		ct, ok1 := iamParseDate(c)
		wt, ok2 := iamParseDate(w)
		if !ok1 || !ok2 {
			return false
		}
		switch cmp {
		case "==":
			return ct.Equal(wt)
		case "<":
			return ct.Before(wt)
		case "<=":
			return !ct.After(wt)
		case ">":
			return ct.After(wt)
		case ">=":
			return !ct.Before(wt)
		}
		return false
	}
}

// iamIPInCIDR reports whether the context IP falls within the wanted CIDR (or
// equals a bare IP).
func iamIPInCIDR(c, w string) bool {
	ip := net.ParseIP(strings.TrimSpace(c))
	if ip == nil {
		return false
	}
	w = strings.TrimSpace(w)
	if !strings.Contains(w, "/") {
		return ip.Equal(net.ParseIP(w))
	}
	_, cidr, err := net.ParseCIDR(w)
	if err != nil {
		return false
	}
	return cidr.Contains(ip)
}

// iamCtxLookup resolves a condition-context key, case-insensitively on the key
// NAME — IAM condition key names are not case-sensitive, so a policy written
// with AWS:SourceArn matches the gate's aws:SourceArn (and vice versa).
func iamCtxLookup(ctx map[string][]string, key string) ([]string, bool) {
	if v, ok := ctx[key]; ok {
		return v, true
	}
	for k, v := range ctx {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// iamConditionMatches evaluates a statement's Condition block, honoring the
// IfExists suffix, the ForAllValues/ForAnyValue set qualifiers, and the Null
// operator. Returns whether it is satisfied plus any context keys referenced
// but not supplied.
func iamConditionMatches(stmt iamStatement, ctx map[string][]string) (bool, []string) {
	var missing []string
	for op, kv := range stmt.Condition {
		setOp := ""
		base := op
		if strings.HasPrefix(base, "ForAllValues:") {
			setOp, base = "all", strings.TrimPrefix(base, "ForAllValues:")
		} else if strings.HasPrefix(base, "ForAnyValue:") {
			setOp, base = "any", strings.TrimPrefix(base, "ForAnyValue:")
		}
		ifExists := strings.HasSuffix(base, "IfExists")
		base = strings.TrimSuffix(base, "IfExists")
		for key, wantVals := range kv {
			if base == "Null" {
				// {"Null":{"key":"true"}} requires the key be ABSENT; "false"
				// requires it be present.
				_, present := iamCtxLookup(ctx, key)
				wantAbsent := len(wantVals) > 0 && strings.EqualFold(wantVals[0], "true")
				if wantAbsent == present {
					return false, missing
				}
				continue
			}
			ctxVals, present := iamCtxLookup(ctx, key)
			if !present {
				if ifExists {
					continue
				}
				missing = append(missing, key)
				return false, missing
			}
			if !iamEvalConditionSet(setOp, base, ctxVals, wantVals) {
				return false, missing
			}
		}
	}
	return true, missing
}

// iamEvalConditionSet applies the ForAllValues / ForAnyValue (default) set
// semantics around a base operator.
func iamEvalConditionSet(setOp, base string, ctxVals, wantVals []string) bool {
	if setOp == "all" {
		for _, c := range ctxVals {
			if !iamEvalConditionOp(base, []string{c}, wantVals) {
				return false
			}
		}
		return true
	}
	return iamEvalConditionOp(base, ctxVals, wantVals)
}

// iamSubstituteVars replaces IAM policy variables (e.g. ${aws:username}) in a
// string with the first value of the matching condition-context key, so a
// resource like arn:aws:s3:::bucket/${aws:username}/* scopes to the caller.
func iamSubstituteVars(s string, ctx map[string][]string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "${") {
			end := strings.Index(s[i:], "}")
			if end > 0 {
				key := s[i+2 : i+end]
				if vals, ok := ctx[key]; ok && len(vals) > 0 {
					b.WriteString(vals[0])
				} else if key == "*" || key == "?" || key == "$" {
					b.WriteString(key) // ${*}/${?}/${$} are literal escapes
				}
				i += end + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// iamSubstituteVarsStmt returns a copy of stmt with policy variables in its
// Resource/NotResource patterns resolved against the request context.
func iamSubstituteVarsStmt(stmt iamStatement, ctx map[string][]string) iamStatement {
	subst := func(in iamStringOrList) iamStringOrList {
		if len(in) == 0 {
			return in
		}
		out := make(iamStringOrList, len(in))
		for i, p := range in {
			out[i] = iamSubstituteVars(p, ctx)
		}
		return out
	}
	stmt.Resource = subst(stmt.Resource)
	stmt.NotResource = subst(stmt.NotResource)
	return stmt
}

// iamEvalDecision evaluates an action/resource against the policies. Explicit
// deny always wins; otherwise any matching allow grants; otherwise implicit
// deny.
func iamEvalDecision(docs []iamPolicyDoc, action, resource string, ctx map[string][]string) (decision string, missing []string) {
	return iamEvalDecisionForPrincipal(docs, action, resource, "", ctx)
}

// iamEvalDecisionForPrincipal is iamEvalDecision plus a calling-principal ARN,
// used to evaluate the Principal element of resource-based / trust policies.
func iamEvalDecisionForPrincipal(docs []iamPolicyDoc, action, resource, callerArn string, ctx map[string][]string) (decision string, missing []string) {
	allowed := false
	for _, doc := range docs {
		for _, stmt := range doc.Statement {
			if !iamPrincipalMatches(stmt, callerArn, ctx) {
				continue
			}
			if !iamActionMatches(stmt, action) || !iamResourceMatches(iamSubstituteVarsStmt(stmt, ctx), resource) {
				continue
			}
			ok, miss := iamConditionMatches(stmt, ctx)
			missing = append(missing, miss...)
			if !ok {
				continue
			}
			switch strings.ToLower(stmt.Effect) {
			case "deny":
				return "explicitDeny", missing
			case "allow":
				allowed = true
			}
		}
	}
	if allowed {
		return "allowed", missing
	}
	return "implicitDeny", missing
}

func iamQueryList(r *http.Request, key string) []string {
	var out []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.member.%d", key, i))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

func iamParseContextEntries(r *http.Request) map[string][]string {
	ctx := map[string][]string{}
	for i := 1; ; i++ {
		name := r.FormValue(fmt.Sprintf("ContextEntries.member.%d.ContextKeyName", i))
		if name == "" {
			break
		}
		var vals []string
		for j := 1; ; j++ {
			v := r.FormValue(fmt.Sprintf("ContextEntries.member.%d.ContextKeyValues.member.%d", i, j))
			if v == "" {
				break
			}
			vals = append(vals, v)
		}
		ctx[name] = vals
	}
	return ctx
}

func handleIAMSimulateCustomPolicy(w http.ResponseWriter, r *http.Request) {
	docs := make([]iamPolicyDoc, 0)
	for _, p := range iamQueryList(r, "PolicyInputList") {
		doc, err := parseIAMPolicy(p)
		if err != nil {
			iamErrorXML(w, "InvalidInput", "Invalid policy document: "+err.Error(), http.StatusBadRequest)
			return
		}
		docs = append(docs, doc)
	}
	iamWriteSimulationResponse(w, "SimulateCustomPolicy", docs,
		iamQueryList(r, "ActionNames"), iamQueryList(r, "ResourceArns"), iamParseContextEntries(r))
}

func handleIAMSimulatePrincipalPolicy(w http.ResponseWriter, r *http.Request) {
	roleName := iamRoleNameFromArn(r.FormValue("PolicySourceArn"))
	docs := make([]iamPolicyDoc, 0)
	for _, rp := range iamRolePolicies.List() {
		if rp.RoleName == roleName {
			if doc, err := parseIAMPolicy(rp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	for _, ap := range iamAttachedPolicies.List() {
		if ap.RoleName != roleName {
			continue
		}
		if mp, ok := iamPolicies.Get(ap.PolicyArn); ok {
			if doc, err := parseIAMPolicy(mp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	// Additional inline policies to evaluate alongside the principal's.
	for _, p := range iamQueryList(r, "PolicyInputList") {
		if doc, err := parseIAMPolicy(p); err == nil {
			docs = append(docs, doc)
		}
	}
	iamWriteSimulationResponse(w, "SimulatePrincipalPolicy", docs,
		iamQueryList(r, "ActionNames"), iamQueryList(r, "ResourceArns"), iamParseContextEntries(r))
}

func iamWriteSimulationResponse(w http.ResponseWriter, op string, docs []iamPolicyDoc, actions, resources []string, ctx map[string][]string) {
	if len(resources) == 0 {
		resources = []string{"*"}
	}
	var members strings.Builder
	for _, action := range actions {
		for _, resource := range resources {
			decision, missing := iamEvalDecision(docs, action, resource, ctx)
			var missingXML strings.Builder
			for _, m := range iamUniqueStrings(missing) {
				fmt.Fprintf(&missingXML, "<member>%s</member>", iamXMLEscape(m))
			}
			fmt.Fprintf(&members, `<member>
        <EvalActionName>%s</EvalActionName>
        <EvalResourceName>%s</EvalResourceName>
        <EvalDecision>%s</EvalDecision>
        <MatchedStatements/>
        <MissingContextValues>%s</MissingContextValues>
      </member>`, iamXMLEscape(action), iamXMLEscape(resource), decision, missingXML.String())
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <%sResult>
    <IsTruncated>false</IsTruncated>
    <EvaluationResults>%s</EvaluationResults>
  </%sResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</%sResponse>`, op, op, members.String(), op, generateUUID(), op)
}

// iamRoleNameFromArn extracts the role name from an arn:aws:iam::acct:role/Name
// (or role/path/Name) source ARN.
func iamRoleNameFromArn(arn string) string {
	const marker = ":role/"
	i := strings.Index(arn, marker)
	if i < 0 {
		return arn
	}
	rest := arn[i+len(marker):]
	// A path may precede the name; the name is the final segment.
	if slash := strings.LastIndex(rest, "/"); slash >= 0 {
		return rest[slash+1:]
	}
	return rest
}

func iamUniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func iamXMLEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func registerIAMPolicySimulation(r *sim.AWSQueryRouter) {
	r.Register("SimulateCustomPolicy", handleIAMSimulateCustomPolicy)
	r.Register("SimulatePrincipalPolicy", handleIAMSimulatePrincipalPolicy)
}
