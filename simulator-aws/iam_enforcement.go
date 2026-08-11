package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Call-time IAM enforcement (GitHub issue #657). The policy evaluator
// (iamEvalDecision) already existed but was wired only into the diagnostic
// SimulatePrincipalPolicy endpoint. This gate runs it on real API calls: it
// resolves the caller's SigV4 access-key id to a registered IAM user, evaluates
// the user's effective policies for the request's action, and denies with the
// correct per-service error shape when the action isn't allowed.
//
// Enforcement applies ONLY to credentials that resolve to a registered IAM
// principal: a long-term user access key (AKIA…) or a temporary STS credential
// (ASIA… from AssumeRole / GetSessionToken). Unknown / static test credentials
// are treated as permissive (the sim's existing default) — so a consumer proving
// least-privilege mints a real key, while every other test keeps working.
//
// The decision is the full identity-based evaluation (the principal's own
// policies, its group policies, and — for assumed roles — the role's policies),
// combined with any resource-based policy on the target resource (S3 bucket /
// Lambda / SNS / SQS) and capped by the user's permission boundary. The request
// condition context carries aws:username/userid/SourceIp/RequestedRegion. The
// target resource ARN is derived where the request makes it unambiguous (e.g.
// sns:TopicArn, sqs:QueueUrl) and is "*" otherwise.

// iamEnforce returns true if the request is authorized (the handler should run)
// and false if it was denied (a response has already been written).
func iamEnforce(w http.ResponseWriter, r *http.Request) bool {
	if iamAccessKeyIDFromRequest(r) == "" {
		return true // unsigned request — permissive (matches AuthPassthrough)
	}
	action, ok := iamActionForRequest(r)
	if !ok {
		return true // operation we can't classify — don't block on an unknown
	}
	if iamPermissionlessAction(action) {
		return true // calls AWS authorizes for every caller regardless of policy
	}
	for _, resource := range iamResourceARNsForRequest(r, action) {
		allowed, principalArn, registered := iamAuthorize(r, action, resource)
		if !registered {
			return true // unknown/test credential — permissive
		}
		if !allowed {
			iamWriteDeny(w, r, principalArn, action)
			return false
		}
	}
	return iamEnforcePassRole(w, r, action)
}

// iamEnforcePassRole runs the second authorization AWS performs when a request
// hands a role to a service: iam:PassRole against the role's own ARN, with
// iam:PassedToService set to the service principal that will assume it. A
// caller allowed to create the resource but not to pass that role is denied,
// which is what makes a PassRole statement scoped to specific roles mean
// anything. Operations that pass no role, and requests that name none, are
// unaffected.
func iamEnforcePassRole(w http.ResponseWriter, r *http.Request, action string) bool {
	principals, ok := iamPassRoleOperations[action]
	if !ok {
		return true
	}
	roles := iamPassedRoleARNs(r)
	if len(roles) == 0 {
		return true
	}
	extra := map[string][]string{}
	if len(principals) > 0 {
		extra["iam:PassedToService"] = principals
	}
	for _, role := range roles {
		allowed, principalArn, registered := iamAuthorizeWithContext(r, "iam:PassRole", role, extra)
		if !registered {
			return true
		}
		if !allowed {
			iamWriteDeny(w, r, principalArn, "iam:PassRole")
			return false
		}
	}
	return true
}

// iamPassedRoleARNs returns the distinct IAM role ARNs a request carries.
// Services name the field differently and nest it at different depths (Amazon
// ECS sends taskRoleArn and executionRoleArn at the top level and again under
// overrides; AWS CodeBuild sends serviceRole; AWS Lambda sends Role), so the
// request is scanned for values that are role ARNs rather than matched against
// a per-service list of field names that would silently miss the next service.
func iamPassedRoleARNs(r *http.Request) []string {
	var roles []string
	seen := map[string]struct{}{}
	add := func(v string) {
		if !iamIsRoleARN(v) {
			return
		}
		if _, dup := seen[v]; dup {
			return
		}
		seen[v] = struct{}{}
		roles = append(roles, v)
	}
	if body := iamRequestBody(r); len(body) > 0 {
		var doc any
		if json.Unmarshal(body, &doc) == nil {
			iamWalkJSONStrings(doc, add)
		}
	}
	// Query-protocol services carry the role as a form parameter.
	_ = r.ParseForm()
	for _, values := range r.Form {
		for _, v := range values {
			add(v)
		}
	}
	sort.Strings(roles)
	return roles
}

func iamIsRoleARN(v string) bool {
	return strings.HasPrefix(v, "arn:aws:iam::") && strings.Contains(v, ":role/")
}

// iamWalkJSONStrings calls visit for every string anywhere in a decoded JSON
// document.
func iamWalkJSONStrings(node any, visit func(string)) {
	switch v := node.(type) {
	case string:
		visit(v)
	case []any:
		for _, item := range v {
			iamWalkJSONStrings(item, visit)
		}
	case map[string]any:
		for _, item := range v {
			iamWalkJSONStrings(item, visit)
		}
	}
}

// iamAuthorize is the shared authorization core used by both the control-plane
// (POST /) gate and the S3 REST gate. It resolves the caller, evaluates the
// identity-based policies (own + group + assumed-role), combines them with any
// resource-based policy on the target resource (granting if either allows,
// denying on an explicit Deny in either), and caps the result by the user's
// permission boundary. registered is false for unknown/test credentials (the
// permissive default — the caller should allow).
func iamAuthorize(r *http.Request, action, resource string) (allowed bool, principalArn string, registered bool) {
	return iamAuthorizeWithContext(r, action, resource, nil)
}

// iamAuthorizeWithContext is iamAuthorize with additional condition-context
// keys the caller supplies — the keys AWS derives from what the request is
// doing rather than from its envelope, such as iam:PassedToService on the
// PassRole check.
func iamAuthorizeWithContext(r *http.Request, action, resource string, extra map[string][]string) (allowed bool, principalArn string, registered bool) {
	akid := iamAccessKeyIDFromRequest(r)
	principalArn, docs, userName, ok := iamPrincipalForAccessKey(akid)
	if !ok {
		return false, "", false
	}

	ctx := map[string][]string{}
	if userName != "" {
		ctx["aws:username"] = []string{userName}
		if u, uok := iamUsers.Get(userName); uok {
			ctx["aws:userid"] = []string{u.UserId}
		}
	}
	if ip := iamSourceIP(r); ip != "" {
		ctx["aws:SourceIp"] = []string{ip}
	}
	if region := iamRequestedRegion(r); region != "" {
		ctx["aws:RequestedRegion"] = []string{region}
	}
	ctx["aws:PrincipalOrgID"] = []string{awsOrgID()}
	// Global keys from the request envelope + principal (time, transport,
	// user-agent, principal ARN + tags, resource account, MFA).
	iamPopulateGlobalConditionKeys(r, akid, principalArn, userName, ctx)
	// Service-initiation keys (aws:ViaAWSService=false for a direct client call).
	iamPopulateServiceContext(r, ctx)
	// Resource-scoped / service-specific keys (aws:ResourceTag/*, ecs:cluster,
	// aws:RequestTag/*, aws:TagKeys) from the request's target resource.
	iamPopulateResourceConditionKeys(r, action, ctx)
	for key, values := range extra {
		ctx[key] = values
	}

	decision, _ := iamEvalDecision(docs, action, resource, ctx)
	if decision == "explicitDeny" {
		return false, principalArn, true
	}
	allowed = decision == "allowed"

	if resource != "*" {
		// An S3 bucket policy is attached at the bucket and governs its objects,
		// so look the policy up under the bucket ARN while still evaluating it
		// against the object ARN.
		policyARN := resource
		if strings.HasPrefix(resource, "arn:aws:s3:::") {
			if i := strings.IndexByte(resource[len("arn:aws:s3:::"):], '/'); i >= 0 {
				policyARN = resource[:len("arn:aws:s3:::")+i]
			}
		}
		if rdocs := iamResourcePolicyDocsForARN(policyARN); len(rdocs) > 0 {
			rdec, _ := iamEvalDecisionForPrincipal(rdocs, action, resource, principalArn, ctx)
			if rdec == "explicitDeny" {
				return false, principalArn, true
			}
			if rdec == "allowed" {
				allowed = true
			}
		}
	}

	if bdocs := iamPermissionBoundaryDocs(userName); len(bdocs) > 0 {
		if bdec, _ := iamEvalDecision(bdocs, action, resource, ctx); bdec != "allowed" {
			allowed = false
		}
	}
	return allowed, principalArn, true
}

// iamEnforceREST gates a non-POST-/ (REST) service request: it authorizes the
// pre-derived action + resource ARN and, on denial, writes the service-specific
// error via deny. Returns true when the handler should run.
func iamEnforceREST(w http.ResponseWriter, r *http.Request, action, resource string, deny func(http.ResponseWriter, *http.Request, string, string)) bool {
	if action == "" {
		return true
	}
	allowed, principalArn, registered := iamAuthorize(r, action, resource)
	if !registered || allowed {
		return true
	}
	deny(w, r, principalArn, action)
	return false
}

// iamAccessKeyIDFromRequest extracts the SigV4 access-key id from the
// Authorization header (Credential=AKID/date/region/service/aws4_request).
func iamAccessKeyIDFromRequest(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
		return ""
	}
	i := strings.Index(auth, "Credential=")
	if i < 0 {
		return ""
	}
	cred := auth[i+len("Credential="):]
	if s := strings.IndexAny(cred, "/,"); s > 0 {
		return cred[:s]
	}
	return ""
}

func iamRequestedRegion(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	i := strings.Index(auth, "Credential=")
	if i < 0 {
		return ""
	}
	parts := strings.Split(auth[i+len("Credential="):], "/")
	if len(parts) >= 3 {
		return parts[2] // AKID / date / region / service / aws4_request
	}
	return ""
}

func iamSourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// iamActionForRequest derives the IAM action string (e.g. "ec2:CreateVolume",
// "ecs:RunTask") from an awsJson X-Amz-Target or an awsQuery Action, reusing the
// service-source mapping CloudTrail already maintains.
func iamActionForRequest(r *http.Request) (string, bool) {
	src, ok := awsEventSource(r)
	if !ok {
		return "", false
	}
	service := strings.SplitN(src, ".", 2)[0] // "ecs.amazonaws.com" → "ecs"
	// Amazon CloudWatch records monitoring.amazonaws.com as its CloudTrail
	// event source, but its IAM service prefix is cloudwatch. CloudTrail source
	// names and IAM namespaces are separate AWS contracts.
	if service == "monitoring" {
		service = "cloudwatch"
	}
	var op string
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		if i := strings.LastIndex(target, "."); i >= 0 {
			op = target[i+1:]
		}
	} else {
		op = r.FormValue("Action")
	}
	if service == "" || op == "" {
		return "", false
	}
	return service + ":" + op, true
}

// iamPermissionlessAction reports whether an action is one AWS authorizes for
// every caller regardless of policy (e.g. STS identity self-inspection and the
// web-identity/SAML assume-role calls, which carry their own token-based trust).
func iamPermissionlessAction(action string) bool {
	switch action {
	case "sts:GetCallerIdentity", "sts:GetSessionToken",
		"sts:AssumeRoleWithWebIdentity", "sts:AssumeRoleWithSAML":
		return true
	}
	return false
}

// iamResourceARNsForRequest derives the request's target resource ARNs where
// they are unambiguously available from the request parameters (so
// resource-scoped policies and resource-based policies evaluate against the
// real resources). Most operations target one resource; DynamoDB transactions
// and batches target every referenced table, and AWS authorizes each item
// against its own table ARN, so all of them must be allowed. It returns ["*"]
// when the resource can't be determined from the request alone, which is not a
// neutral default: a literal "*" matches only a policy whose Resource is itself
// "*", so a service missing from the switch below denies every resource-scoped
// grant written against it. A service whose requests name their target belongs
// here.
func iamResourceARNsForRequest(r *http.Request, action string) []string {
	service := strings.SplitN(action, ":", 2)[0]
	region := iamRequestedRegion(r)
	if region == "" {
		region = awsRegion()
	}
	acct := awsAccountID()
	arn := func(svc, resource string) string {
		return "arn:aws:" + svc + ":" + region + ":" + acct + ":" + resource
	}
	one := func(s string) []string { return []string{s} }
	switch service {
	case "dynamodb":
		// A transaction carries its table references per item rather than at
		// the top level, and the AWS Service Reference declares no resource
		// type for TransactWriteItems or TransactGetItems at all — it lists
		// neither action — so the table-driven derivation never sees them.
		// AWS authorizes every table a transaction touches, and deriving none
		// would deny a single-table transaction its own table (GitHub issue
		// #870), so they are read here, before the reference is consulted.
		if tables := iamDynamoDBRequestTables(r); len(tables) > 0 {
			arns := make([]string, len(tables))
			for i, name := range tables {
				arns[i] = arn("dynamodb", "table/"+name)
			}
			return arns
		}
	case "lambda":
		if name := iamLambdaResourceName(r); name != "" {
			if strings.HasPrefix(name, "arn:") {
				return one(name)
			}
			return one(arn("lambda", "function:"+name))
		}
	}
	// Services whose target resource is derived from the resource types AWS
	// declares for the action rather than from a hand-written case above; see
	// iam_resource_arns.go.
	if arns := iamDerivedResourceARNs(r, service, strings.TrimPrefix(action, service+":"), region, acct); len(arns) > 0 {
		return arns
	}
	return one("*")
}

// iamDynamoDBRequestTables returns the distinct table names referenced by a
// DynamoDB transaction (TransactItems[i].Put/Update/Delete/ConditionCheck/Get
// .TableName) or batch (RequestItems keyed by table name), sorted for a
// deterministic evaluation order.
func iamDynamoDBRequestTables(r *http.Request) []string {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return nil
	}
	var req struct {
		TransactItems []map[string]struct {
			TableName string `json:"TableName"`
		} `json:"TransactItems"`
		RequestItems map[string]json.RawMessage `json:"RequestItems"`
	}
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	set := map[string]struct{}{}
	for _, item := range req.TransactItems {
		for _, op := range item {
			if op.TableName != "" {
				set[op.TableName] = struct{}{}
			}
		}
	}
	for name := range req.RequestItems {
		set[name] = struct{}{}
	}
	tables := make([]string, 0, len(set))
	for name := range set {
		tables = append(tables, name)
	}
	sort.Strings(tables)
	return tables
}

// iamLambdaResourceName extracts the Lambda function name/ARN from the REST path
// (/2015-03-31/functions/{name}/...) or the request body (FunctionName).
func iamLambdaResourceName(r *http.Request) string {
	if name := iamJSONBodyField(r, "FunctionName"); name != "" {
		return name
	}
	const marker = "/functions/"
	if i := strings.Index(r.URL.Path, marker); i >= 0 {
		rest := r.URL.Path[i+len(marker):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return ""
}

// iamRequestBody reads the full request body, restoring it so the downstream
// handler still sees it.
func iamRequestBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	return body
}

// iamJSONBodyField reads a top-level string field from an awsJson request body,
// restoring the body so the downstream handler still sees it.
func iamJSONBodyField(r *http.Request, field string) string {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// iamWriteDeny emits the deny error in the shape the calling service uses: EC2's
// query protocol returns UnauthorizedOperation (XML 403); other query services
// return AccessDenied (XML 403); awsJson services return AccessDeniedException
// (JSON 403).
func iamWriteDeny(w http.ResponseWriter, r *http.Request, principalArn, action string) {
	msg := "User: " + principalArn + " is not authorized to perform: " + action +
		" because no identity-based policy allows the " + action + " action"
	if r.Header.Get("X-Amz-Target") != "" {
		sim.AWSError(w, "AccessDeniedException", msg, http.StatusForbidden)
		return
	}
	if strings.HasPrefix(action, "ec2:") {
		ec2ErrorXML(w, "UnauthorizedOperation", msg, http.StatusForbidden)
		return
	}
	iamErrorXML(w, "AccessDenied", msg, http.StatusForbidden)
}
