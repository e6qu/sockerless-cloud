package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// Resource-scoped + service-specific IAM condition keys. The policy evaluator
// supports the condition operators; this feeds the request's target resource
// into the condition context so tag- and cluster-conditioned grants enforce
// faithfully:
//
//   - aws:ResourceTag/<k> (and the service-prefixed ec2:ResourceTag/<k>) — the
//     tags of the resource the request targets (e.g. the volume DeleteVolume
//     acts on), so a policy allowing the action only on resources carrying a
//     given tag matches when, and only when, the resource carries it.
//   - ecs:cluster — the cluster ARN an ECS task operation targets.
//   - aws:RequestTag/<k> + aws:TagKeys — the tags supplied on a tag-on-create /
//     CreateTags request.

// iamPopulateGlobalConditionKeys adds the global (`aws:`) condition keys that
// derive from the request envelope and the calling principal: the request time,
// transport, user-agent, the principal ARN + its tags, the resource account, and
// MFA state. Service-to-service keys (aws:SourceArn/SourceAccount/CalledVia/
// ViaAWSService) are intentionally absent — the sim originates direct client
// calls, not service-initiated ones, so in real AWS those keys are absent here
// too. aws:PrincipalOrgID is absent because the sim models a single account with
// no Organizations slice.
func iamPopulateGlobalConditionKeys(r *http.Request, akid, principalArn, userName string, ctx map[string][]string) {
	now := time.Now().UTC()
	ctx["aws:CurrentTime"] = []string{now.Format(time.RFC3339)}
	ctx["aws:EpochTime"] = []string{strconv.FormatInt(now.Unix(), 10)}
	ctx["aws:SecureTransport"] = []string{strconv.FormatBool(r.TLS != nil)}
	if ua := r.Header.Get("User-Agent"); ua != "" {
		ctx["aws:UserAgent"] = []string{ua}
	}
	if principalArn != "" {
		ctx["aws:PrincipalArn"] = []string{principalArn}
	}
	ctx["aws:ResourceAccount"] = []string{awsAccountID()}

	// Principal tags (a user's tags, or an assumed role's tags).
	for _, t := range iamPrincipalTags(akid, userName) {
		ctx["aws:PrincipalTag/"+t.Key] = []string{t.Value}
	}

	// MFA: present only for an MFA-authenticated temporary session.
	mfa, age := iamSessionMFA(akid)
	ctx["aws:MultiFactorAuthPresent"] = []string{strconv.FormatBool(mfa)}
	if mfa {
		ctx["aws:MultiFactorAuthAge"] = []string{strconv.FormatInt(age, 10)}
	}
}

// iamPrincipalTags returns the calling principal's tags (user tags for an AKIA
// key; the assumed role's tags for an ASIA session).
func iamPrincipalTags(akid, userName string) []IAMTag {
	if userName != "" {
		if u, ok := iamUsers.Get(userName); ok {
			return u.Tags
		}
	}
	if tc, ok := iamTempCreds.Get(akid); ok && tc.RoleName != "" {
		if role, rok := iamRoles.Get(tc.RoleName); rok {
			return role.Tags
		}
	}
	return nil
}

// iamSessionMFA reports whether the credential is an MFA-authenticated session
// and, if so, its age in seconds.
func iamSessionMFA(akid string) (present bool, ageSeconds int64) {
	tc, ok := iamTempCreds.Get(akid)
	if !ok || !tc.MFA {
		return false, 0
	}
	if created, err := time.Parse(time.RFC3339, tc.CreatedAt); err == nil {
		return true, int64(time.Since(created).Seconds())
	}
	return true, 0
}

// iamPopulateResourceConditionKeys augments ctx with the resource-scoped and
// service-specific condition keys implied by the request.
func iamPopulateResourceConditionKeys(r *http.Request, action string, ctx map[string][]string) {
	service := strings.SplitN(action, ":", 2)[0]
	switch service {
	case "ec2":
		iamPopulateEC2ResourceTags(r, ctx)
	case "ecs":
		iamPopulateECSCluster(r, ctx)
	default:
		// Every other tag-storing sim service resolves the request's target
		// resource into aws:ResourceTag/<k> + <service>:ResourceTag/<k>.
		iamPopulateServiceResourceTags(r, service, ctx)
	}
	iamPopulateRequestTags(r, ctx)
}

// iamPopulateEC2ResourceTags resolves the tags of the EC2 resource the request
// targets and exposes them as aws:ResourceTag/<k> and ec2:ResourceTag/<k>.
func iamPopulateEC2ResourceTags(r *http.Request, ctx map[string][]string) {
	tags, ok := iamEC2RequestResourceTags(r)
	if !ok {
		return
	}
	for _, t := range tags {
		ctx["aws:ResourceTag/"+t.Key] = []string{t.Value}
		ctx["ec2:ResourceTag/"+t.Key] = []string{t.Value}
	}
}

// iamEC2RequestResourceTags returns the tags of the first EC2 resource the
// request references by id (volume / snapshot / instance / network interface).
func iamEC2RequestResourceTags(r *http.Request) ([]EC2Tag, bool) {
	for _, param := range []string{"VolumeId", "SnapshotId", "InstanceId", "InstanceId.1", "NetworkInterfaceId", "ResourceId", "ResourceId.1"} {
		id := r.FormValue(param)
		if id == "" {
			continue
		}
		switch {
		case strings.HasPrefix(id, "vol-"):
			if v, ok := ec2Volumes.Get(id); ok {
				return v.Tags, true
			}
		case strings.HasPrefix(id, "snap-"):
			if s, ok := ec2Snapshots.Get(id); ok {
				return s.Tags, true
			}
		case strings.HasPrefix(id, "i-"):
			if i, ok := ec2Instances.Get(id); ok {
				return i.Tags, true
			}
		case strings.HasPrefix(id, "eni-"):
			if e, ok := ec2NetworkInterfaces.Get(id); ok {
				return e.Tags, true
			}
		}
	}
	return nil, false
}

// iamPopulateECSCluster exposes ecs:cluster (the targeted cluster's ARN) for an
// ECS task operation. ECS is awsJson, so the cluster lives in the request body;
// the body is read and restored so the downstream handler still sees it.
func iamPopulateECSCluster(r *http.Request, ctx map[string][]string) {
	if r.Body == nil {
		return
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) == 0 {
		return
	}
	var req struct {
		Cluster string `json:"cluster"`
	}
	if json.Unmarshal(body, &req) != nil {
		return
	}
	name := req.Cluster
	if name == "" {
		name = "default"
	}
	arn := name
	if !strings.HasPrefix(name, "arn:") {
		arn = ecsArn("cluster", name)
	}
	ctx["ecs:cluster"] = []string{arn}
}

// iamPopulateRequestTags exposes aws:RequestTag/<k> + aws:TagKeys from the tags
// supplied on a tag-on-create / CreateTags request (Tag.N.Key/Value form).
func iamPopulateRequestTags(r *http.Request, ctx map[string][]string) {
	tags := parseIndexedTags(r, "Tag")
	if len(tags) == 0 {
		return
	}
	var keys []string
	for _, t := range tags {
		ctx["aws:RequestTag/"+t.Key] = []string{t.Value}
		keys = append(keys, t.Key)
	}
	ctx["aws:TagKeys"] = keys
}

// iamPopulateServiceConditionKeys adds the service-prefixed condition keys the
// request itself determines.
//
// A service condition key is how AWS scopes an action whose resource the
// request does not name: a policy allows cloudwatch:PutMetricData only where
// `cloudwatch:namespace` equals one value, or an Amazon EC2 action only in one
// `ec2:Region`. The evaluator matches conditions by key name and the simulator
// populated only the global `aws:` keys, so every such policy was evaluated
// against a context missing the key it tests — and a StringEquals on a key that
// is not there does not match, so the policy denied the very request it was
// written to allow.
//
// Only keys the request itself settles are added. A key whose value is a fact
// about a resource the simulator would have to look up, or about a caller
// relationship it does not model, is left absent, which is what AWS does with a
// key that does not apply.
func iamPopulateServiceConditionKeys(r *http.Request, action string, body []byte, ctx map[string][]string) {
	service, name, _ := strings.Cut(action, ":")

	// ec2:Region is the region the request was made in, which is the same fact
	// aws:RequestedRegion carries. It is the most-declared action condition key
	// in the vendored service references by a wide margin.
	if region := iamRequestedRegion(r); region != "" {
		switch service {
		case "ec2":
			ctx["ec2:Region"] = []string{region}
		}
	}

	// The Amazon S3 keys below describe how the request was signed and carried,
	// which the request states outright.
	if service == "s3" {
		if r.TLS != nil {
			ctx["s3:TlsVersion"] = []string{iamTLSVersionName(r.TLS.Version)}
		}
		authorization := r.Header.Get("Authorization")
		switch {
		case strings.HasPrefix(authorization, "AWS4-HMAC-SHA256"):
			ctx["s3:authType"] = []string{"REST-HEADER"}
			ctx["s3:signatureversion"] = []string{"AWS4-HMAC-SHA256"}
		case r.URL.Query().Get("X-Amz-Signature") != "":
			ctx["s3:authType"] = []string{"REST-QUERY-STRING"}
			ctx["s3:signatureversion"] = []string{"AWS4-HMAC-SHA256"}
		}
		if digest := r.Header.Get("x-amz-content-sha256"); digest != "" {
			ctx["s3:x-amz-content-sha256"] = []string{digest}
		}
		// s3:signatureAge is how long ago the request was signed, in
		// milliseconds — the fact a policy tests to refuse a long-lived
		// presigned URL.
		if signed := iamSigV4SigningTime(r); !signed.IsZero() {
			age := time.Since(signed).Milliseconds()
			if age < 0 {
				age = 0
			}
			ctx["s3:signatureAge"] = []string{strconv.FormatInt(age, 10)}
		}
		ctx["s3:ResourceAccount"] = []string{awsAccountID()}
	}

	// cloudwatch:namespace is the namespace the metrics in the request are
	// written to, and it is the documented way to scope PutMetricData — an
	// action whose resource type the request never names.
	if service == "cloudwatch" && name == "PutMetricData" {
		if namespace := cloudWatchRequestNamespace(r, body); namespace != "" {
			ctx["cloudwatch:namespace"] = []string{namespace}
		}
	}

	// kms:CallerAccount is the account the caller belongs to, which for this
	// simulator is the account it serves.
	if service == "kms" {
		ctx["kms:CallerAccount"] = []string{awsAccountID()}
	}
}

// iamTLSVersionName spells a TLS version the way s3:TlsVersion does.
func iamTLSVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS10:
		return "1.0"
	}
	return ""
}

// cloudWatchRequestNamespace reads the namespace a PutMetricData call writes
// its metrics to. Amazon CloudWatch serves that operation over two protocols:
// the Query protocol, where the namespace is a form parameter net/http has
// already parsed, and Smithy RPC v2 CBOR, where it is a request member the SDK
// additionally gzip-compresses.
func cloudWatchRequestNamespace(r *http.Request, body []byte) string {
	if namespace := r.FormValue("Namespace"); namespace != "" {
		return namespace
	}
	raw, err := cwDecompress(body)
	if err != nil {
		return ""
	}
	var request struct {
		Namespace string `cbor:"Namespace"`
	}
	if cbor.Unmarshal(raw, &request) != nil {
		return ""
	}
	return request.Namespace
}

// iamSigV4SigningTime reads the instant a SigV4 request was signed. Both forms
// of the signature carry it: the X-Amz-Date header on a header-signed request
// and the X-Amz-Date query parameter on a presigned URL.
func iamSigV4SigningTime(r *http.Request) time.Time {
	stamp := r.Header.Get("X-Amz-Date")
	if stamp == "" {
		stamp = r.URL.Query().Get("X-Amz-Date")
	}
	signed, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		return time.Time{}
	}
	return signed
}
