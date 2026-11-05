package main

import (
	"github.com/e6qu/sockerless-cloud/sim"

	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
)

// Call-time IAM enforcement: resolve the caller's SigV4 access key to a
// registered IAM principal, evaluate its effective policies for the request's
// action, and deny in the service's own error shape.
//
// Enforce only for a credential that resolves to a registered principal: a
// long-term user key (AKIA…) or an AWS STS temporary credential (ASIA…). Allow an
// unknown credential.
//
// The decision combines the identity-based policies (own, group and, for an
// assumed role, the role's) with any resource-based policy on the target, capped
// by the permissions boundary of the user or role and by a session's session
// policies, as the IAM User Guide's policy evaluation logic describes. Derive
// the target ARN where the request makes it unambiguous, and "*" otherwise.

// iamEnforce returns true if the request is authorized (the handler should run)
// and false if it was denied (a response has already been written).
func iamEnforce(w http.ResponseWriter, r *http.Request) bool {
	if iamAccessKeyIDFromRequest(r) == "" {
		if src := iamDeliverySource(r); src != nil && sim.InProcess(r) {
			return iamEnforceServicePrincipal(w, r, *src)
		}
		return true // unsigned request — permissive (matches AuthPassthrough)
	}
	action, ok := iamActionForRequest(r)
	if !ok {
		return true // operation we can't classify — don't block on an unknown
	}
	if iamPermissionlessAction(action) {
		return true // calls AWS authorizes for every caller regardless of policy
	}
	if caller, ok := iamCallerForAccessKey(iamAccessKeyIDFromRequest(r)); ok && caller.refuses(action) {
		iamWriteDeny(w, r, iamDenial{Principal: caller.Arn, Action: action, Policy: iamIdentityBasedPolicy})
		return false
	}
	for _, target := range iamAuthorizationTargets(r, action) {
		denial, allowed, registered := iamAuthorizeDenial(r, target.action, target.resource, target.context)
		if !registered {
			return true // unknown/test credential — permissive
		}
		if !allowed {
			iamWriteDeny(w, r, denial)
			return false
		}
	}
	return iamEnforcePassRole(w, r, action)
}

// iamEnforceServicePrincipal authorizes a call an AWS service makes as its own
// principal, such as Amazon EventBridge delivering to a target as
// events.amazonaws.com: the target's resource policy must admit the service
// for each action the call performs, with the source the service acts for.
func iamEnforceServicePrincipal(w http.ResponseWriter, r *http.Request, src iamServiceSource) bool {
	action, ok := iamActionForRequest(r)
	if !ok || iamPermissionlessAction(action) {
		return true
	}
	if denied, ok := iamServicePrincipalAllowed(src, iamAuthorizationTargets(r, action)); !ok {
		iamWriteDeny(w, r, iamServicePrincipalDenial(src, denied))
		return false
	}
	return true
}

// iamResourceExists answers, for each service whose resources a resource
// policy guards, whether the resource an ARN names exists. A service answers
// a call on a resource it does not hold with its own not-found error before
// any policy is read.
var iamResourceExists = map[string]func(arn string) bool{
	"sqs": func(arn string) bool {
		_, ok := sqsQueues.Get(arn[strings.LastIndex(arn, ":")+1:])
		return ok
	},
	"sns": func(arn string) bool {
		_, ok := snsTopics.Get(snsTopicNameFromARN(arn))
		return ok
	},
	"lambda": func(arn string) bool {
		_, _, ok := lambdaResolveInvocationTarget(arn, "")
		return ok
	},
	"logs": func(arn string) bool {
		group, ok := strings.CutPrefix(arn[strings.Index(arn, ":log-group:")+1:], "log-group:")
		if !ok {
			return false
		}
		_, exists := cwLogGroups.Get(strings.TrimSuffix(group, ":*"))
		return exists
	},
}

// iamResourceMissing reports whether the resource an ARN names belongs to a
// service that answers for it and does not exist.
func iamResourceMissing(arn string) bool {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return false
	}
	exists, ok := iamResourceExists[parts[2]]
	return ok && !exists(arn)
}

// iamServicePrincipalAllowed reports whether each target's resource policy
// admits the service src names, and the first action one refuses.
func iamServicePrincipalAllowed(src iamServiceSource, targets []iamAuthorizationTarget) (string, bool) {
	for _, target := range targets {
		if iamResourceMissing(target.resource) {
			continue
		}
		if !iamAuthorizeServiceDelivery(target.resource, target.action, src) {
			return target.action, false
		}
	}
	return "", true
}

type iamAuthorizationTarget struct {
	action   string
	resource string
	// context carries the keys that differ between the targets of one
	// request, such as the version each entry of a DeleteObjects names.
	context map[string][]string
	// entry is the DeleteObjects entry the target authorizes. S3 refuses such
	// an entry on its own, in the DeleteResult, rather than the request.
	entry *s3ObjectIdentifier
}

func iamAuthorizationTargets(r *http.Request, action string) []iamAuthorizationTarget {
	service, operation, _ := strings.Cut(action, ":")
	return iamOperationTargets(r, service, operation, iamResourceARNsForRequest(r, action))
}

func iamKMSKeyARNOrAny(keyID string) string {
	if key, ok := kmsKeys.Get(keyID); ok && key.Arn != "" {
		return key.Arn
	}
	return "*"
}

// iamEnforcePassRole runs the second authorization AWS performs when a request
// hands a role to a service: iam:PassRole against the role's own ARN, with
// iam:PassedToService set to the service principal that will assume it. A
// caller allowed to create the resource but not to pass that role is denied,
// which is what makes a PassRole statement scoped to specific roles mean
// anything. Operations that pass no role, and requests that name none, are
// unaffected.
func iamEnforcePassRole(w http.ResponseWriter, r *http.Request, action string) bool {
	return iamEnforcePassRoleWith(w, r, action, iamWriteDeny)
}

// iamUnreadableRoleARN stands for a role the gate could not read from an
// oversized body. No policy names it, so the PassRole check denies.
const iamUnreadableRoleARN = "arn:aws:iam::unreadable-request-body:role/unreadable"

func iamEnforcePassRoleWith(w http.ResponseWriter, r *http.Request, action string,
	deny func(http.ResponseWriter, *http.Request, iamDenial)) bool {
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
		denial, allowed, registered := iamAuthorizeDenial(r, "iam:PassRole", role, extra)
		if !registered {
			return true
		}
		if !allowed {
			deny(w, r, denial)
			return false
		}
	}
	return true
}

// iamPassedRoleARNs returns the distinct IAM role ARNs a request carries.
// Services name and nest the member differently (Amazon ECS taskRoleArn and
// executionRoleArn, AWS CodeBuild serviceRole, AWS Lambda Role), so scan every
// value for a role ARN rather than match a per-service field list that would
// miss the next service.
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
	body, within := iamRequestBodyWithin(r)
	if len(body) > 0 {
		var doc any
		if json.Unmarshal(body, &doc) == nil {
			iamWalkJSONStrings(doc, add)
		} else {
			// The S3 control plane composes its requests as XML documents, and
			// registering an Access Grants location names the role there.
			iamWalkXMLText(body, add)
		}
	}
	if !within && len(roles) == 0 {
		// The body did not fit, so the scan cannot rule out a role. No operation that
		// passes a role carries an upload, so treat an oversized body as naming a role
		// no policy grants.
		roles = append(roles, iamUnreadableRoleARN)
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

func iamWalkXMLText(body []byte, visit func(string)) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			return
		}
		if data, ok := token.(xml.CharData); ok {
			if text := strings.TrimSpace(string(data)); text != "" {
				visit(text)
			}
		}
	}
}

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

// iamAuthorize is the authorization core shared by the control-plane (POST /)
// gate and the Amazon S3 REST gate. It allows when an identity-based or
// resource-based policy allows and neither explicitly denies, capped by the
// permissions boundary. registered is false for an unknown credential, which the
// caller allows.
func iamAuthorize(r *http.Request, action, resource string) (allowed bool, principalArn string, registered bool) {
	return iamAuthorizeWithContext(r, action, resource, nil)
}

// iamRequestConditionContext is the condition context the gate evaluates a
// policy against: every key it can resolve from the request, the principal
// behind the access key, and the resource the request targets.
func iamRequestConditionContext(
	r *http.Request, akid, principalArn, userName, action string,
) map[string][]string {
	acct := iamRequestAccount(r)
	ctx := map[string][]string{}
	if userName != "" {
		ctx["aws:username"] = []string{userName}
		if u, uok := iamUsersIn(acct).Get(userName); uok {
			ctx["aws:userid"] = []string{u.UserId}
		}
	}
	if ip := iamSourceIP(r); ip != "" {
		ctx["aws:SourceIp"] = []string{ip}
	}
	if region := iamRequestedRegion(r); region != "" {
		ctx["aws:RequestedRegion"] = []string{region}
	}
	if iamOrganizationMember(acct) {
		ctx["aws:PrincipalOrgID"] = []string{awsOrgID()}
	}
	if tc, ok := iamTempCreds.Get(akid); ok && tc.SourceIdentity != "" {
		ctx["aws:SourceIdentity"] = []string{tc.SourceIdentity}
	}
	iamPopulateGlobalConditionKeys(r, akid, principalArn, userName, ctx)
	// Service-initiation keys (aws:ViaAWSService=false for a direct client call).
	iamPopulateServiceContext(r, ctx)
	iamPopulateResourceConditionKeys(r, action, ctx)
	iamPopulateServiceConditionKeys(r, action, iamRequestBody(r), ctx)
	return ctx
}

// iamAuthorizeWithContext is iamAuthorize plus keys AWS derives from what the
// request does rather than its envelope, such as iam:PassedToService on the
// PassRole check.
func iamAuthorizeWithContext(r *http.Request, action, resource string, extra map[string][]string) (allowed bool, principalArn string, registered bool) {
	denial, allowed, registered := iamAuthorizeDenial(r, action, resource, extra)
	return allowed, denial.Principal, registered
}

// iamAuthorizeDenial is iamAuthorizeWithContext reporting, for a refusal, the
// policy that decided it.
func iamAuthorizeDenial(r *http.Request, action, resource string, extra map[string][]string) (denial iamDenial, allowed, registered bool) {
	akid := iamAccessKeyIDFromRequest(r)
	caller, ok := iamCallerForAccessKey(akid)
	if !ok {
		return iamDenial{Action: action}, false, false
	}
	principalArn := caller.Arn
	ctx := iamRequestConditionContext(r, akid, principalArn, caller.UserName, action)
	for key, values := range extra {
		ctx[key] = values
	}
	refuse := func(policy iamPolicyKind, explicit bool) (iamDenial, bool, bool) {
		return iamDenial{Principal: principalArn, Action: action, Policy: policy, Explicit: explicit, Resource: resource, Context: ctx}, false, true
	}
	permit := iamDenial{Principal: principalArn, Action: action, Resource: resource, Context: ctx}

	// The service control policies of a member account's organization cap
	// every principal of the account, its root user included, before any of
	// the account's own policies is read.
	if scp := orgSCPDecision(caller, action, resource, ctx); scp != "allowed" {
		return refuse(iamServiceControlPolicy, scp == "explicitDeny")
	}

	decision, _ := iamEvalDecision(caller.Identity, action, resource, ctx)
	if decision == "explicitDeny" {
		return refuse(iamIdentityBasedPolicy, true)
	}
	identityAllows := decision == "allowed" || caller.Unrestricted

	// A resource-based policy that names the caller itself grants past an
	// implicit deny anywhere else; one that names the role a session was
	// assumed from grants only within the session's boundary and session
	// policies.
	resourceAllows, resourceDirect := false, false
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
		rdocs := iamResourcePolicyDocsForARN(policyARN)
		// The owning account's root user manages a resource's policy even when
		// the policy denies it, so a policy that denies everyone cannot lock
		// the account out.
		if iamRootManagesOwnPolicy(r, action, resource, principalArn) {
			rdocs = nil
		}
		// A role's trust policy is the resource policy of assuming it, and of
		// tagging the session the assumption opens: naming the caller there
		// grants either without an identity policy.
		if iamTrustGatedAction(action) {
			rdocs = append(rdocs, iamRoleTrustDocs(resource)...)
		}
		if len(rdocs) > 0 {
			rdec, _, direct := iamEvalResourceDecision(rdocs, action, resource, principalArn, caller.EntityArn, ctx)
			if rdec == "explicitDeny" {
				return refuse(iamResourcePolicyKindFor(action), true)
			}
			resourceAllows, resourceDirect = rdec == "allowed", direct
			// Across accounts, a statement that names the caller's account
			// admits it, and the caller's own policies decide.
			if !resourceAllows && iamCrossAccount(principalArn, resource) {
				resourceAllows = iamResourcePolicyAdmitsAccount(rdocs, action, resource, principalArn, ctx)
			}
		}
	}

	boundaryAllows := true
	if len(caller.Boundary) > 0 {
		bdec, _ := iamEvalDecision(caller.Boundary, action, resource, ctx)
		if bdec == "explicitDeny" {
			return refuse(iamPermissionsBoundary, true)
		}
		boundaryAllows = bdec == "allowed"
	}
	sessionAllows := true
	if caller.SessionCaps {
		sdec, _ := iamEvalDecision(caller.Session, action, resource, ctx)
		if sdec == "explicitDeny" {
			return refuse(iamSessionPolicy, true)
		}
		sessionAllows = sdec == "allowed"
	}
	// A principal of another account needs both its own account's policies
	// and the resource's policy to allow the request. AssumeRoot names a
	// member account's root user, which the organization rather than a
	// resource policy lets the management account reach.
	crossAccount := action != "sts:AssumeRoot" && iamCrossAccount(principalArn, resource)
	switch {
	case crossAccount && !identityAllows:
		return refuse(iamIdentityBasedPolicy, false)
	case crossAccount && !resourceAllows:
		return refuse(iamResourcePolicyKindFor(action), false)
	case !crossAccount && resourceDirect:
		return permit, true, true
	case !identityAllows && !resourceAllows:
		return refuse(iamIdentityBasedPolicy, false)
	case !boundaryAllows:
		return refuse(iamPermissionsBoundary, false)
	case !sessionAllows:
		return refuse(iamSessionPolicy, false)
	}
	return permit, true, true
}

// iamArnAccount is the account an ARN names, empty for an ARN that names
// none.
func iamArnAccount(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return ""
	}
	return parts[4]
}

// iamResourceOwnerAccount is the account that owns the resource an ARN
// names: the account the ARN carries, or for an Amazon S3 bucket and its
// objects, whose ARNs carry none, the bucket's owner. It is empty when no
// account owns the resource, or no bucket of that name exists.
func iamResourceOwnerAccount(arn string) string {
	if acct := iamArnAccount(arn); acct != "" {
		return acct
	}
	bucket, ok := strings.CutPrefix(arn, "arn:aws:s3:::")
	if !ok {
		return ""
	}
	bucket, _, _ = strings.Cut(bucket, "/")
	b, exists := s3Buckets_.Get(bucket)
	switch {
	case !exists:
		return ""
	case b.Owner != "":
		return b.Owner
	}
	return awsAccountID()
}

// iamCrossAccount reports whether a principal acts on a resource another
// account owns. A management account principal naming an account the
// simulator does not host acts on nothing another account holds.
func iamCrossAccount(principalArn, resource string) bool {
	caller := iamArnAccount(principalArn)
	if caller == "" {
		return false
	}
	// AWS Organizations admits a member account to its organization's
	// resources, and a handshake's parties to the handshake, by its own rules
	// (orgAdmitCaller, orgHandshakeRespond), not as a cross-account request.
	if organizations, ok := strings.CutPrefix(resource, "arn:aws:organizations::"); ok &&
		(iamOrganizationMember(caller) || strings.Contains(organizations, ":handshake/")) {
		return false
	}
	owner := iamResourceOwnerAccount(resource)
	if owner == "" || owner == caller || owner == "aws" {
		return false
	}
	if iamIsManagementAccount(caller) {
		return !iamIsManagementAccount(owner) && iamHostedAccount(owner)
	}
	return true
}

// iamResourcePolicyAdmitsAccount reports whether a resource policy has an
// Allow statement that names the caller's account for action on resource.
func iamResourcePolicyAdmitsAccount(docs []iamPolicyDoc, action, resource, callerArn string, ctx map[string][]string) bool {
	for _, doc := range docs {
		for _, stmt := range doc.Statement {
			if !strings.EqualFold(stmt.Effect, "allow") || iamPrincipalMatchKind(stmt, callerArn, "", ctx) != iamPrincipalDelegated {
				continue
			}
			if !iamActionMatches(stmt, action) || !iamResourceMatches(iamSubstituteVarsStmt(stmt, ctx), resource) {
				continue
			}
			if ok, _ := iamConditionMatches(stmt, ctx); ok {
				return true
			}
		}
	}
	return false
}

// iamRootManagesOwnPolicy reports whether the request is the root user of
// the account that owns a resource reading or removing that resource's
// policy: Amazon S3's GetBucketPolicy, PutBucketPolicy and DeleteBucketPolicy,
// and Amazon SQS reading a queue's attributes or deleting its Policy, the
// tasks the S3UnlockBucketPolicy and SQSUnlockQueuePolicy root-task policies
// serve.
func iamRootManagesOwnPolicy(r *http.Request, action, resource, principalArn string) bool {
	owner := iamResourceOwnerAccount(resource)
	if owner == "" || principalArn != "arn:aws:iam::"+owner+":root" {
		return false
	}
	switch action {
	case "s3:GetBucketPolicy", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy",
		"sqs:GetQueueUrl", "sqs:GetQueueAttributes":
		return true
	case "sqs:SetQueueAttributes":
		var req struct {
			Attributes map[string]string `json:"Attributes"`
		}
		if json.Unmarshal(iamRequestBody(r), &req) != nil {
			return false
		}
		policy, deletes := req.Attributes["Policy"]
		return len(req.Attributes) == 1 && deletes && policy == ""
	}
	return false
}

// iamEnforceREST authorizes a REST request's pre-derived action and resource
// ARN, writing the service's own error via deny. It returns true when the
// handler should run.
func iamEnforceREST(w http.ResponseWriter, r *http.Request, target iamAuthorizationTarget, deny func(http.ResponseWriter, *http.Request, iamDenial)) bool {
	if target.action == "" {
		return true
	}
	denial, allowed, registered := iamAuthorizeDenial(r, target.action, target.resource, target.context)
	if !registered || allowed {
		return true
	}
	deny(w, r, denial)
	return false
}

// iamSigV4CredentialScope returns the credential scope
// (AKID/date/region/service/aws4_request) from the Authorization header or, for
// a presigned URL, the X-Amz-Credential parameter. A presigned request is made by
// its signer, whose policies govern it like a header-signed call.
func iamSigV4CredentialScope(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
		if i := strings.Index(auth, "Credential="); i >= 0 {
			scope := auth[i+len("Credential="):]
			if end := strings.IndexByte(scope, ','); end >= 0 {
				scope = scope[:end]
			}
			return strings.TrimSpace(scope)
		}
	}
	return r.URL.Query().Get("X-Amz-Credential")
}

func iamAccessKeyIDFromRequest(r *http.Request) string {
	scope := iamSigV4CredentialScope(r)
	if s := strings.IndexByte(scope, '/'); s > 0 {
		return scope[:s]
	}
	return ""
}

func iamRequestedRegion(r *http.Request) string {
	parts := strings.Split(iamSigV4CredentialScope(r), "/")
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

// iamActionForRequest derives the IAM action ("ecs:RunTask") from an awsJson
// X-Amz-Target or an awsQuery Action through the CloudTrail event-source mapping.
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
	op := iamRequestWireOperation(r)
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

// iamResourceARNsForRequest derives the target resource ARNs the request
// parameters name. A DynamoDB transaction or batch targets every referenced
// table, and AWS authorizes each item against its own table. It returns ["*"]
// when the request alone cannot say, which is not neutral: "*" matches only a
// policy whose Resource is "*", so a service missing here fails every
// resource-scoped grant.
func iamResourceARNsForRequest(r *http.Request, action string) []string {
	service := strings.SplitN(action, ":", 2)[0]
	region := iamRequestedRegion(r)
	if region == "" {
		region = awsRegion()
	}
	acct := iamRequestAccount(r)
	arn := func(svc, resource string) string {
		return "arn:aws:" + svc + ":" + region + ":" + acct + ":" + resource
	}
	one := func(s string) []string { return []string{s} }
	switch service {
	case "dynamodb":
		// A transaction carries its tables per item, and the AWS Service Reference lists
		// neither TransactWriteItems nor TransactGetItems, so the table-driven derivation
		// never sees them. AWS authorizes every table a transaction touches; read them
		// here, before the reference.
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
	// Derive the remaining services' targets from the resource types AWS declares
	// for the action; see iam_resource_arns.go.
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

// iamConditionBodyLimit bounds what the gate holds of a request body. Every body
// whose members the gate reads is far below it (AWS caps a DynamoDB request at
// 16 MB), while an Amazon S3 upload has no bound.
const iamConditionBodyLimit = 16 << 20

// iamRequestBody returns the request body for the gate to read, and leaves the
// request carrying all of it for the handler. A body past the limit is not
// returned: half a document parses as nothing, or worse as something.
func iamRequestBody(r *http.Request) []byte {
	body, _ := iamRequestBodyWithin(r)
	return body
}

// iamRequestBodyWithin also reports whether the body fit. The iam:PassRole check
// must tell "no role in this body" from "too large to look at": the first
// grants, the second would bypass.
func iamRequestBodyWithin(r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	held, err := io.ReadAll(io.LimitReader(r.Body, iamConditionBodyLimit+1))
	if err != nil {
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(held))
		return nil, false
	}
	if len(held) <= iamConditionBodyLimit {
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(held))
		return held, true
	}
	rest := r.Body
	r.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.MultiReader(bytes.NewReader(held), rest), Closer: rest}
	return nil, false
}

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
func iamWriteDeny(w http.ResponseWriter, r *http.Request, d iamDenial) {
	msg, action := d.message(), d.Action
	if r.Header.Get("X-Amz-Target") != "" {
		service, _, _ := strings.Cut(action, ":")
		iamWriteJSONDeny(w, service, "AccessDeniedException", msg)
		return
	}
	if strings.HasPrefix(action, "ec2:") {
		ec2ErrorXML(w, "UnauthorizedOperation", msg+" Encoded authorization failure message: "+stsEncodeAuthorizationMessage(r, d), http.StatusForbidden)
		return
	}
	iamErrorXML(w, "AccessDenied", msg, http.StatusForbidden)
}
