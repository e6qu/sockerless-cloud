package main

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The limits AWS STS puts on the sessions it opens: session policies that
// narrow a session, the packed size those policies and the session tags take,
// the duration a session may last, and the MFA code that authenticates it.

// stsSessionPolicies are the session policies an AssumeRole, AssumeRoleWith*
// or GetFederationToken request passes.
type stsSessionPolicies struct {
	Inline string
	Arns   []string
}

func (p stsSessionPolicies) passed() bool {
	return p.Inline != "" || len(p.Arns) > 0
}

// stsRequestError is an AWS STS error a request validation produced.
type stsRequestError struct {
	code    string
	message string
	status  int
}

func (e *stsRequestError) write(w http.ResponseWriter) {
	stsErrorXML(w, e.code, e.message, e.status)
}

func stsValidationError(format string, args ...any) *stsRequestError {
	return &stsRequestError{code: "ValidationError", message: fmt.Sprintf(format, args...), status: http.StatusBadRequest}
}

func stsAccessDenied(format string, args ...any) *stsRequestError {
	return &stsRequestError{code: "AccessDenied", message: fmt.Sprintf(format, args...), status: http.StatusForbidden}
}

// stsSessionPolicyCharacters is the character set the AWS STS model allows in
// a session policy document.
var stsSessionPolicyCharacters = regexp.MustCompile("^[\u0009\u000A\u000D -ÿ]+$")

const (
	stsSessionPolicyMaxLength = 2048
	stsSessionPolicyMaxArns   = 10
)

// stsRequestSessionPolicies reads and validates the Policy and PolicyArns a
// request passes.
func stsRequestSessionPolicies(r *http.Request) (stsSessionPolicies, *stsRequestError) {
	p := stsSessionPolicies{Inline: r.FormValue("Policy")}
	for i := 1; ; i++ {
		arn := r.FormValue(fmt.Sprintf("PolicyArns.member.%d.arn", i))
		if arn == "" {
			break
		}
		p.Arns = append(p.Arns, arn)
	}
	if len(p.Arns) > stsSessionPolicyMaxArns {
		return p, stsValidationError("1 validation error detected: Value '%v' at 'policyArns' failed to satisfy constraint: Member must have length less than or equal to %d",
			p.Arns, stsSessionPolicyMaxArns)
	}
	for _, arn := range p.Arns {
		if len(arn) < 20 || len(arn) > 2048 {
			return p, stsValidationError("1 validation error detected: Value '%s' at 'policyArns.member.arn' failed to satisfy constraint: Member must have length greater than or equal to 20", arn)
		}
	}
	if p.Inline == "" {
		return p, nil
	}
	if len(p.Inline) > stsSessionPolicyMaxLength {
		return p, stsValidationError("1 validation error detected: Value at 'policy' failed to satisfy constraint: Member must have length less than or equal to %d", stsSessionPolicyMaxLength)
	}
	if !stsSessionPolicyCharacters.MatchString(p.Inline) {
		return p, stsValidationError("1 validation error detected: Value at 'policy' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\u0009\\u000A\\u000D\\u0020-\\u00FF]+")
	}
	if _, err := parseIAMPolicy(p.Inline); err != nil {
		return p, &stsRequestError{code: "MalformedPolicyDocument", message: "The policy is not in the valid JSON format.", status: http.StatusBadRequest}
	}
	return p, nil
}

// stsPackedPolicyLimit is the packed size, in compressed bytes, that the
// session policies and session tags of one request may take. AWS documents
// that it packs them into a compressed binary form with a separate limit but
// publishes neither the form nor the limit.
const stsPackedPolicyLimit = 2048

// stsPackedPolicySize is the packed size of a request's session policies and
// session tags as a percentage of stsPackedPolicyLimit, rounded up.
func stsPackedPolicySize(policies stsSessionPolicies, tags []IAMTag) int {
	var packed bytes.Buffer
	zw, err := flate.NewWriter(&packed, flate.BestCompression)
	if err != nil {
		panic(fmt.Sprintf("flate writer at a valid level: %v", err))
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(policies.Inline)) == nil {
		_, _ = zw.Write(compact.Bytes())
	} else {
		_, _ = zw.Write([]byte(policies.Inline))
	}
	for _, arn := range policies.Arns {
		_, _ = zw.Write([]byte(arn))
	}
	for _, tag := range tags {
		_, _ = zw.Write([]byte(tag.Key + "=" + tag.Value))
	}
	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("flate into a buffer: %v", err))
	}
	return (packed.Len()*100 + stsPackedPolicyLimit - 1) / stsPackedPolicyLimit
}

// stsCheckPackedPolicySize returns the PackedPolicySize a response reports,
// and -1 when the request passed no session policy or tag. A packed size over
// the limit refuses the request.
func stsCheckPackedPolicySize(policies stsSessionPolicies, tags []IAMTag) (int, *stsRequestError) {
	if !policies.passed() && len(tags) == 0 {
		return -1, nil
	}
	size := stsPackedPolicySize(policies, tags)
	if size > 100 {
		return size, &stsRequestError{code: "PackedPolicyTooLarge",
			message: fmt.Sprintf("Packed size of consumed provided policies exceeds limit by %d%%.", size-100),
			status:  http.StatusBadRequest}
	}
	return size, nil
}

// stsPackedPolicySizeXML is the PackedPolicySize element of a response, empty
// when the request packed nothing.
func stsPackedPolicySizeXML(size int) string {
	if size < 0 {
		return ""
	}
	return "<PackedPolicySize>" + strconv.Itoa(size) + "</PackedPolicySize>"
}

// stsSessionPolicyDocs reads a session's session policies. A managed policy
// ARN that names no policy contributes no statement, so it allows nothing.
func stsSessionPolicyDocs(tc IAMTempCred) []iamPolicyDoc {
	var docs []iamPolicyDoc
	if tc.SessionPolicy != "" {
		if doc, err := parseIAMPolicy(tc.SessionPolicy); err == nil {
			docs = append(docs, doc)
		}
	}
	for _, arn := range tc.SessionPolicyArns {
		if mp, ok := iamPolicies.Get(arn); ok {
			if doc, err := parseIAMPolicy(mp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	return docs
}

// stsOnBehalfOf is the principal and limits of a credential minted on behalf
// of the holder of akid: the holder's own session, with every limit it
// carries, or the IAM user its long-term key belongs to.
func stsOnBehalfOf(akid string) IAMTempCred {
	if tc, ok := iamTempCreds.Get(akid); ok {
		return IAMTempCred{
			PrincipalArn: tc.PrincipalArn, UserName: tc.UserName, RoleName: tc.RoleName,
			FederatedClaims: tc.FederatedClaims, SessionTags: tc.SessionTags, TransitiveTagKeys: tc.TransitiveTagKeys,
			SessionPolicy: tc.SessionPolicy, SessionPolicyArns: tc.SessionPolicyArns, SessionPolicyCaps: tc.SessionPolicyCaps,
			FederatingUser: tc.FederatingUser, UnrestrictedIdentity: tc.UnrestrictedIdentity, TaskPolicyArn: tc.TaskPolicyArn,
		}
	}
	if key, ok := iamAccessKeys.Get(akid); ok {
		if u, uok := iamUsers.Get(key.UserName); uok {
			return IAMTempCred{PrincipalArn: u.Arn, UserName: u.UserName}
		}
	}
	return IAMTempCred{}
}

// stsFederatedSessionRefuses reports whether a federated user session may not
// call an action: no IAM operation and no AWS STS operation other than
// GetCallerIdentity, which iamPermissionlessAction admits first.
func stsFederatedSessionRefuses(action string) bool {
	service, _, _ := strings.Cut(action, ":")
	return service == "iam" || service == "sts"
}

// stsRequestDuration reads DurationSeconds within the range the operation's
// model declares, defaulting to def.
func stsRequestDuration(r *http.Request, min, max, def int) (int, *stsRequestError) {
	raw := r.FormValue("DurationSeconds")
	if raw == "" {
		return def, nil
	}
	d, err := strconv.Atoi(raw)
	if err != nil {
		return 0, stsValidationError("1 validation error detected: Value '%s' at 'durationSeconds' failed to satisfy constraint: Member must be an integer", raw)
	}
	if d < min {
		return 0, stsValidationError("1 validation error detected: Value '%d' at 'durationSeconds' failed to satisfy constraint: Member must have value greater than or equal to %d", d, min)
	}
	if d > max {
		return 0, stsValidationError("1 validation error detected: Value '%d' at 'durationSeconds' failed to satisfy constraint: Member must have value less than or equal to %d", d, max)
	}
	return d, nil
}

const (
	stsRoleDurationMin     = 900
	stsRoleDurationMax     = 43200
	stsRoleDurationDefault = 3600
	// stsRoleChainingMaxDuration caps a role session that a role session
	// assumed.
	stsRoleChainingMaxDuration = 3600
)

// stsRoleSessionDuration is the duration of a session assumed from role:
// DurationSeconds up to the role's maximum session duration, and up to an
// hour for a role chained from another role session.
func stsRoleSessionDuration(r *http.Request, role IAMRole, chained bool) (time.Duration, *stsRequestError) {
	d, err := stsRequestDuration(r, stsRoleDurationMin, stsRoleDurationMax, stsRoleDurationDefault)
	if err != nil {
		return 0, err
	}
	if chained && d > stsRoleChainingMaxDuration {
		return 0, stsValidationError("The requested DurationSeconds exceeds the 1 hour session limit for roles assumed by role chaining.")
	}
	if d > iamRoleMaxSessionDuration(role) {
		return 0, stsValidationError("The requested DurationSeconds exceeds the MaxSessionDuration set for this role.")
	}
	return time.Duration(d) * time.Second, nil
}

// iamRoleMaxSessionDuration is a role's maximum session duration in seconds,
// an hour unless the role sets one.
func iamRoleMaxSessionDuration(role IAMRole) int {
	if role.MaxSessionDuration == 0 {
		return 3600
	}
	return role.MaxSessionDuration
}

// stsCallerIsRoleSession reports whether r is signed by an assumed-role
// session, which makes an AssumeRole it calls role chaining.
func stsCallerIsRoleSession(r *http.Request) bool {
	tc, ok := iamTempCreds.Get(iamAccessKeyIDFromRequest(r))
	return ok && tc.RoleName != ""
}

var stsTokenCodePattern = regexp.MustCompile(`^\d{6}$`)

// stsRequestMFA verifies the SerialNumber and TokenCode a request presents
// against the virtual MFA device the serial number names, which must be
// enabled for the calling user. present is false for a request that presents
// neither.
func stsRequestMFA(r *http.Request) (present bool, err *stsRequestError) {
	serial, code := r.FormValue("SerialNumber"), r.FormValue("TokenCode")
	if serial == "" && code == "" {
		return false, nil
	}
	if code != "" && !stsTokenCodePattern.MatchString(code) {
		return false, stsValidationError("1 validation error detected: Value '%s' at 'tokenCode' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\d]*", code)
	}
	if serial == "" || code == "" {
		return false, stsAccessDenied("MultiFactorAuthentication failed, unable to validate MFA code.")
	}
	dev, ok := iamVirtualMFADevices.Get(serial)
	if !ok || dev.UserName == "" {
		return false, stsAccessDenied("MultiFactorAuthentication failed, unable to validate MFA code.")
	}
	if caller, registered := iamCallerForAccessKey(iamAccessKeyIDFromRequest(r)); registered && caller.UserName != dev.UserName {
		return false, stsAccessDenied("MultiFactorAuthentication failed, unable to validate MFA code.")
	}
	if !dev.verifiesCode(code, time.Now()) {
		return false, stsAccessDenied("MultiFactorAuthentication failed with invalid MFA one time pass code.")
	}
	return true, nil
}

// stsRootTaskPolicies are the AWS managed root-task policies that scope an
// AssumeRoot session, as the IAM User Guide publishes them. The guide lists
// SQSUnlockQueuePolicy's statements and the operations it serves but not its
// document; its document here grants those operations to the root user only.
var stsRootTaskPolicies = map[string]string{
	"arn:aws:iam::aws:policy/root-task/IAMAuditRootUserCredentials": `{"Version":"2012-10-17","Statement":[
		{"Sid":"DenyAllOtherActionsOnAnyResource","Effect":"Deny","NotAction":["iam:ListAccessKeys","iam:ListSigningCertificates","iam:GetLoginProfile","iam:ListMFADevices","iam:GetAccountSummary","iam:GetUser","iam:GetAccessKeyLastUsed"],"Resource":"*"},
		{"Sid":"DenyAuditingCredentialsOnNonRootUserResource","Effect":"Deny","Action":["iam:ListAccessKeys","iam:ListSigningCertificates","iam:GetLoginProfile","iam:ListMFADevices","iam:GetUser","iam:GetAccessKeyLastUsed"],"NotResource":"arn:aws:iam::*:root"}]}`,
	"arn:aws:iam::aws:policy/root-task/IAMCreateRootUserPassword": `{"Version":"2012-10-17","Statement":[
		{"Sid":"DenyAllOtherActionsOnAnyResource","Effect":"Deny","NotAction":["iam:CreateLoginProfile","iam:GetLoginProfile"],"Resource":"*"},
		{"Sid":"DenyCreatingPasswordOnNonRootUserResource","Effect":"Deny","Action":["iam:CreateLoginProfile","iam:GetLoginProfile"],"NotResource":"arn:aws:iam::*:root"}]}`,
	"arn:aws:iam::aws:policy/root-task/IAMDeleteRootUserCredentials": `{"Version":"2012-10-17","Statement":[
		{"Sid":"DenyAllOtherActionsOnAnyResource","Effect":"Deny","NotAction":["iam:DeleteAccessKey","iam:DeleteSigningCertificate","iam:DeleteLoginProfile","iam:DeactivateMFADevice","iam:ListAccessKeys","iam:ListSigningCertificates","iam:GetLoginProfile","iam:ListMFADevices","iam:GetUser","iam:GetAccessKeyLastUsed"],"Resource":"*"},
		{"Sid":"DenyDeletingRootUserCredentialsOnNonRootUserResource","Effect":"Deny","Action":["iam:DeleteAccessKey","iam:DeleteSigningCertificate","iam:DeleteLoginProfile","iam:DeactivateMFADevice","iam:ListAccessKeys","iam:ListSigningCertificates","iam:GetLoginProfile","iam:ListMFADevices","iam:GetUser","iam:GetAccessKeyLastUsed"],"NotResource":"arn:aws:iam::*:root"}]}`,
	"arn:aws:iam::aws:policy/root-task/S3UnlockBucketPolicy": `{"Version":"2012-10-17","Statement":[
		{"Sid":"DenyAllOtherActionsOnAnyResource","Effect":"Deny","NotAction":["s3:DeleteBucketPolicy","s3:PutBucketPolicy","s3:GetBucketPolicy","s3:ListAllMyBuckets"],"Resource":"*"},
		{"Sid":"DenyManagingBucketPolicyForNonRootCallers","Effect":"Deny","Action":["s3:DeleteBucketPolicy","s3:PutBucketPolicy","s3:GetBucketPolicy","s3:ListAllMyBuckets"],"Resource":"*","Condition":{"ArnNotLike":{"aws:PrincipalArn":"arn:aws:iam::*:root"}}}]}`,
	"arn:aws:iam::aws:policy/root-task/SQSUnlockQueuePolicy": `{"Version":"2012-10-17","Statement":[
		{"Sid":"DenyAllOtherActionsOnAnyResource","Effect":"Deny","NotAction":["sqs:ListQueues","sqs:GetQueueUrl","sqs:GetQueueAttributes","sqs:SetQueueAttributes"],"Resource":"*"},
		{"Sid":"DenyActionsForNonRootUser","Effect":"Deny","Action":["sqs:ListQueues","sqs:GetQueueUrl","sqs:GetQueueAttributes","sqs:SetQueueAttributes"],"Resource":"*","Condition":{"ArnNotLike":{"aws:PrincipalArn":"arn:aws:iam::*:root"}}}]}`,
}

func stsRootTaskPolicy(arn string) (iamPolicyDoc, bool) {
	raw, ok := stsRootTaskPolicies[arn]
	if !ok {
		return iamPolicyDoc{}, false
	}
	doc, err := parseIAMPolicy(raw)
	if err != nil {
		panic(fmt.Sprintf("root-task policy %s does not parse: %v", arn, err))
	}
	return doc, true
}
