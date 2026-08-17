package main

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// IAM conformance: a spec-derived, mechanical measure of how completely the IAM
// policy engine implements the real AWS policy grammar — so gaps are visible and
// tracked rather than discovered later by a consumer. Three layers:
//
//  1. iamOperatorCatalog        — every real condition operator, each marked
//     supported + carrying a probe vector. TestIAMConformance_Operators asserts
//     every "supported" operator actually evaluates, and every "unsupported" one
//     safely defaults to no-match (never a silent spurious grant).
//  2. iamConditionKeyRegistry   — the condition keys the enforcement gate feeds
//     into the request context, each carrying the probe that builds the gate's
//     own context and looks the key up in it. A row whose probe finds nothing
//     IS a known non-conformity (a policy conditioned on it can't enforce yet).
//  3. the golden corpus (testdata/iam_conformance_vectors.json) — end-to-end
//     (policy, context, action, resource) → expected decision vectors, run
//     through iamEvalDecision here and through the REAL AWS SimulateCustomPolicy
//     oracle in sdk-tests (gated). External ground truth, not self-grading.
//
// The catalogs are the authoritative checklist; TestIAMConformance_Ratchet locks
// the unsupported set so a regression or a silently-added spec item fails CI.
// Process: docs/SERVICE_CONFORMANCE.md.

type iamOperatorSpec struct {
	name      string
	supported bool
	// probe: iamEvalConditionOp(name, {ctx}, {want}) must equal want-match.
	ctx, want string
	match     bool
}

// iamOperatorCatalog is the complete real-AWS condition-operator set
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html).
var iamOperatorCatalog = []iamOperatorSpec{
	{"StringEquals", true, "a", "a", true},
	{"StringNotEquals", true, "a", "b", true},
	{"StringEqualsIgnoreCase", true, "ABC", "abc", true},
	{"StringNotEqualsIgnoreCase", true, "abc", "xyz", true},
	{"StringLike", true, "abcdef", "abc*", true},
	{"StringNotLike", true, "abc", "xyz*", true},
	{"NumericEquals", true, "5", "5", true},
	{"NumericNotEquals", true, "5", "6", true},
	{"NumericLessThan", true, "4", "5", true},
	{"NumericLessThanEquals", true, "5", "5", true},
	{"NumericGreaterThan", true, "6", "5", true},
	{"NumericGreaterThanEquals", true, "5", "5", true},
	{"DateEquals", true, "2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z", true},
	{"DateNotEquals", true, "2020-01-01T00:00:00Z", "2021-01-01T00:00:00Z", true},
	{"DateLessThan", true, "2020-01-01T00:00:00Z", "2021-01-01T00:00:00Z", true},
	{"DateLessThanEquals", true, "2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z", true},
	{"DateGreaterThan", true, "2021-01-01T00:00:00Z", "2020-01-01T00:00:00Z", true},
	{"DateGreaterThanEquals", true, "2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z", true},
	{"Bool", true, "true", "true", true},
	{"IpAddress", true, "10.1.2.3", "10.0.0.0/8", true},
	{"NotIpAddress", true, "192.168.0.1", "10.0.0.0/8", true},
	{"ArnEquals", true, "arn:aws:iam::1:role/x", "arn:aws:iam::1:role/x", true},
	{"ArnLike", true, "arn:aws:iam::1:role/x", "arn:aws:iam::1:role/*", true},
	{"ArnNotEquals", true, "arn:aws:iam::1:role/x", "arn:aws:iam::1:role/y", true},
	{"ArnNotLike", true, "arn:aws:iam::1:role/x", "arn:aws:iam::2:role/*", true},
	{"BinaryEquals", true, "QUJD", "QUJD", true},
}

// iamConditionKeySpec is one condition key the enforcement gate is expected to
// resolve, together with the probe that proves it does. populated is not
// written down: it is measured by running the probe and looking for probeKey in
// the context the gate built, so a key that stops being resolved becomes a
// failing row rather than a stale "true".
type iamConditionKeySpec struct {
	key string
	// probeKey is the concrete key the probe must produce. For a wildcard row
	// it is the key the probe's own tag produces.
	probeKey string
	note     string
	probe    func(t *testing.T) map[string][]string
}

// iamConditionKeyRegistry is the map of which condition keys the gate feeds.
// Add a row with its probe when a new key becomes relevant; a row with no probe
// cannot claim anything.
var iamConditionKeyRegistry = []iamConditionKeySpec{
	{"aws:username", "aws:username", "the caller's IAM user name", iamProbeUserRequestContext},
	{"aws:userid", "aws:userid", "the caller's unique id", iamProbeUserRequestContext},
	{"aws:SourceIp", "aws:SourceIp", "the request's remote IP", iamProbeUserRequestContext},
	{"aws:RequestedRegion", "aws:RequestedRegion", "the SigV4 credential scope region", iamProbeUserRequestContext},
	{"aws:ResourceTag/*", "aws:ResourceTag/owner", "the target resource's tags (EC2)", iamProbeEC2ResourceContext},
	{"ec2:ResourceTag/*", "ec2:ResourceTag/owner", "the target EC2 resource's tags", iamProbeEC2ResourceContext},
	{"ecs:cluster", "ecs:cluster", "the cluster ARN an ECS task op targets", iamProbeECSClusterContext},
	{"aws:RequestTag/*", "aws:RequestTag/team", "tags supplied on a tag-on-create / CreateTags request", iamProbeEC2ResourceContext},
	{"aws:TagKeys", "aws:TagKeys", "keys supplied on a tag-on-create request", iamProbeEC2ResourceContext},
	{"aws:CurrentTime", "aws:CurrentTime", "the request timestamp (RFC3339)", iamProbeUserRequestContext},
	{"aws:EpochTime", "aws:EpochTime", "the request timestamp (epoch)", iamProbeUserRequestContext},
	{"aws:SecureTransport", "aws:SecureTransport", "whether the request connection used TLS", iamProbeUserRequestContext},
	{"aws:UserAgent", "aws:UserAgent", "the request User-Agent header", iamProbeUserRequestContext},
	{"aws:PrincipalArn", "aws:PrincipalArn", "the calling principal's ARN", iamProbeUserRequestContext},
	{"aws:PrincipalTag/*", "aws:PrincipalTag/team", "the caller's principal (user/role) tags", iamProbeUserRequestContext},
	{"aws:ResourceAccount", "aws:ResourceAccount", "the target resource's owning account", iamProbeUserRequestContext},
	{"aws:MultiFactorAuthPresent", "aws:MultiFactorAuthPresent", "MFA on the session (STS MFA-authenticated session)", iamProbeUserRequestContext},
	{"aws:MultiFactorAuthAge", "aws:MultiFactorAuthAge", "age in seconds of an MFA-authenticated session", iamProbeMFASessionContext},
	{"aws:PrincipalOrgID", "aws:PrincipalOrgID", "the account's AWS Organizations id (the Organizations slice)", iamProbeUserRequestContext},
	{"aws:ViaAWSService", "aws:ViaAWSService", "false on a direct call; true for a sim service-initiated delivery", iamProbeUserRequestContext},
	{"aws:SourceArn", "aws:SourceArn", "the originating resource ARN on a service-initiated delivery", iamProbeServiceDeliveryContext},
	{"aws:SourceAccount", "aws:SourceAccount", "the originating account on a service-initiated delivery", iamProbeServiceDeliveryContext},
	{"aws:CalledVia", "aws:CalledVia", "the calling service on a service-initiated delivery", iamProbeServiceDeliveryContext},
	{"<service>:ResourceTag/*", "lambda:ResourceTag/owner", "lambda/sqs/sns/rds/dynamodb/s3/... resource tags", iamProbeServiceResourceTagContext},
}

// iamProbeAccessKeyID and iamProbeSessionKeyID are the two credentials the
// condition-key probes call with: an IAM user's long-term key and an
// MFA-authenticated assumed-role session.
const (
	iamProbeAccessKeyID  = "AKIACONFORMANCEPROBE"
	iamProbeSessionKeyID = "ASIACONFORMANCEPROBE"
	iamProbeUserName     = "conformance-probe"
)

// iamProbeSignedRequest is the envelope a signed AWS request arrives with: the
// credential scope the region comes out of, a User-Agent, a remote address, and
// TLS.
func iamProbeSignedRequest(accessKeyID, service, target, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "https://sim.local/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	if target != "" {
		r.Header.Set("X-Amz-Target", target)
	}
	r.Header.Set("User-Agent", "aws-sdk-go-v2/1.0 os/linux")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKeyID+
		"/20260801/us-east-1/"+service+"/aws4_request, SignedHeaders=host;x-amz-target, Signature=00")
	r.RemoteAddr = "10.1.2.3:52000"
	r.TLS = &tls.ConnectionState{}
	return r
}

// iamProbeSeedUser registers the IAM user the probes call as, with a tag so the
// principal-tag key has something to resolve.
func iamProbeSeedUser(t *testing.T) {
	t.Helper()
	iamUsers.Put(iamProbeUserName, IAMUser{
		UserName: iamProbeUserName,
		UserId:   "AIDACONFORMANCEPROBE",
		Arn:      "arn:aws:iam::" + awsAccountID() + ":user/" + iamProbeUserName,
		Tags:     []IAMTag{{Key: "team", Value: "platform"}},
	})
	iamAccessKeys.Put(iamProbeAccessKeyID, IAMAccessKey{
		AccessKeyId: iamProbeAccessKeyID, SecretAccessKey: "probe", UserName: iamProbeUserName, Status: "Active",
	})
	t.Cleanup(func() {
		iamUsers.Delete(iamProbeUserName)
		iamAccessKeys.Delete(iamProbeAccessKeyID)
	})
}

// iamProbeContextFor runs the gate's own context builder for a request, which
// is what iamAuthorizeWithContext evaluates every policy against.
func iamProbeContextFor(t *testing.T, r *http.Request, accessKeyID, action string) map[string][]string {
	t.Helper()
	principalArn, _, userName, ok := iamPrincipalForAccessKey(accessKeyID)
	if !ok {
		t.Fatalf("probe credential %s is not registered", accessKeyID)
	}
	return iamRequestConditionContext(r, accessKeyID, principalArn, userName, action)
}

// iamProbeUserRequestContext is an ordinary signed call by an IAM user.
func iamProbeUserRequestContext(t *testing.T) map[string][]string {
	t.Helper()
	iamProbeSeedUser(t)
	r := iamProbeSignedRequest(iamProbeAccessKeyID, "ecs",
		"AmazonEC2ContainerServiceV20141113.ListTasks", `{"cluster":"probe"}`)
	return iamProbeContextFor(t, r, iamProbeAccessKeyID, "ecs:ListTasks")
}

// iamProbeECSClusterContext is an Amazon ECS task operation, which is where
// ecs:cluster comes from.
func iamProbeECSClusterContext(t *testing.T) map[string][]string {
	t.Helper()
	return iamProbeUserRequestContext(t)
}

// iamProbeMFASessionContext is a call made with an MFA-authenticated assumed-
// role session, the only credential that carries an MFA age.
func iamProbeMFASessionContext(t *testing.T) map[string][]string {
	t.Helper()
	iamRoles.Put("conformance-probe-role", IAMRole{
		RoleName: "conformance-probe-role",
		Arn:      "arn:aws:iam::" + awsAccountID() + ":role/conformance-probe-role",
		Tags:     []IAMTag{{Key: "team", Value: "platform"}},
	})
	iamTempCreds.Put(iamProbeSessionKeyID, IAMTempCred{
		AccessKeyID:  iamProbeSessionKeyID,
		RoleName:     "conformance-probe-role",
		PrincipalArn: "arn:aws:sts::" + awsAccountID() + ":assumed-role/conformance-probe-role/probe",
		Expiration:   time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		MFA:          true,
		CreatedAt:    time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
	})
	t.Cleanup(func() {
		iamRoles.Delete("conformance-probe-role")
		iamTempCreds.Delete(iamProbeSessionKeyID)
	})
	r := iamProbeSignedRequest(iamProbeSessionKeyID, "ecs",
		"AmazonEC2ContainerServiceV20141113.ListTasks", `{"cluster":"probe"}`)
	return iamProbeContextFor(t, r, iamProbeSessionKeyID, "ecs:ListTasks")
}

// iamProbeEC2ResourceContext is an Amazon EC2 CreateTags call naming a tagged
// volume: the resource tags come off the volume, the request tags off the call.
func iamProbeEC2ResourceContext(t *testing.T) map[string][]string {
	t.Helper()
	iamProbeSeedUser(t)
	ec2Volumes.Put("vol-conformanceprobe", EC2Volume{
		VolumeId: "vol-conformanceprobe",
		Tags:     []EC2Tag{{Key: "owner", Value: "platform"}},
	})
	t.Cleanup(func() { ec2Volumes.Delete("vol-conformanceprobe") })
	form := "Action=CreateTags&Version=2016-11-15&ResourceId.1=vol-conformanceprobe" +
		"&Tag.1.Key=team&Tag.1.Value=platform"
	r := httptest.NewRequest(http.MethodPost, "https://sim.local/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+iamProbeAccessKeyID+
		"/20260801/us-east-1/ec2/aws4_request, SignedHeaders=host, Signature=00")
	return iamProbeContextFor(t, r, iamProbeAccessKeyID, "ec2:CreateTags")
}

// iamProbeServiceResourceTagContext is a call against a tagged resource of a
// service outside EC2 and ECS, which is where <service>:ResourceTag/<k> comes
// from.
func iamProbeServiceResourceTagContext(t *testing.T) map[string][]string {
	t.Helper()
	iamProbeSeedUser(t)
	lambdaFunctions.Put("conformance-probe-fn", LambdaFunction{
		FunctionName: "conformance-probe-fn",
		FunctionArn:  "arn:aws:lambda:us-east-1:" + awsAccountID() + ":function:conformance-probe-fn",
		Tags:         map[string]string{"owner": "platform"},
	})
	t.Cleanup(func() { lambdaFunctions.Delete("conformance-probe-fn") })
	// AWS Lambda is a REST service: GetFunction names its function in the path
	// (/2015-03-31/functions/{name}), which is where the resolver reads it.
	r := iamProbeSignedRequest(iamProbeAccessKeyID, "lambda", "", "")
	r.SetPathValue("name", "conformance-probe-fn")
	return iamProbeContextFor(t, r, iamProbeAccessKeyID, "lambda:GetFunction")
}

// iamProbeServiceDeliveryContext is the context a service-initiated delivery
// evaluates a target's resource policy against. The three source keys live only
// there: a direct client call is not a service, so the request path is
// correctly without them.
func iamProbeServiceDeliveryContext(t *testing.T) map[string][]string {
	t.Helper()
	return iamServiceInitiatedConditionContext(iamServiceSource{
		Service:       "sns.amazonaws.com",
		SourceArn:     "arn:aws:sns:us-east-1:" + awsAccountID() + ":probe-topic",
		SourceAccount: awsAccountID(),
	})
}

// iamUnpopulatedConditionKeys runs every registry probe and returns the rows
// whose key the gate did not resolve.
func iamUnpopulatedConditionKeys(t *testing.T) []string {
	t.Helper()
	buildConformanceSimulator(t)
	var unpopulated []string
	for _, spec := range iamConditionKeyRegistry {
		ctx := spec.probe(t)
		if len(ctx[spec.probeKey]) == 0 {
			unpopulated = append(unpopulated, spec.key)
		}
	}
	sort.Strings(unpopulated)
	return unpopulated
}

// TestIAMConformance_ConditionKeys proves the gate resolves every condition key
// the registry claims, by building the context the gate builds and looking the
// key up in it. Before this, the registry's populated column was a hand-written
// boolean that nothing checked: every row said true and no code had to agree.
func TestIAMConformance_ConditionKeys(t *testing.T) {
	// The gate reads the simulator's own stores, so the probes run against a
	// built simulator rather than against nil maps.
	buildConformanceSimulator(t)
	for _, spec := range iamConditionKeyRegistry {
		t.Run(spec.key, func(t *testing.T) {
			ctx := spec.probe(t)
			if len(ctx[spec.probeKey]) == 0 {
				t.Errorf("%s (%s): the gate built no %s — the key claims to be populated and is not",
					spec.key, spec.note, spec.probeKey)
			}
		})
	}
}

// iamConformanceVector is one golden corpus entry (shared JSON with sdk-tests).
type iamConformanceVector struct {
	Name      string            `json:"name"`
	Policy    json.RawMessage   `json:"policy"`
	Action    string            `json:"action"`
	Resource  string            `json:"resource"`
	Context   map[string]string `json:"context"`
	CallerArn string            `json:"callerArn"`
	Expect    string            `json:"expect"`
}

func loadIAMConformanceVectors(t *testing.T) []iamConformanceVector {
	t.Helper()
	data, err := os.ReadFile("testdata/iam_conformance_vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v []iamConformanceVector
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return v
}

// TestIAMConformance_Operators asserts each supported operator evaluates and each
// unsupported operator safely no-matches (never a spurious grant).
func TestIAMConformance_Operators(t *testing.T) {
	for _, op := range iamOperatorCatalog {
		got := iamEvalConditionOp(op.name, []string{op.ctx}, []string{op.want})
		if op.supported {
			if got != op.match {
				t.Errorf("operator %s: probe got %v, want %v — claimed supported but the evaluator disagrees", op.name, got, op.match)
			}
		} else if got {
			t.Errorf("operator %s: unsupported but returned true — an unmodeled operator must default to no-match, not a silent grant", op.name)
		}
	}
}

// TestIAMConformance_GoldenCorpus runs every shared vector through the evaluator
// and asserts the documented decision (the same vectors the gated real-AWS
// SimulateCustomPolicy differential in sdk-tests checks against the oracle).
func TestIAMConformance_GoldenCorpus(t *testing.T) {
	for _, vec := range loadIAMConformanceVectors(t) {
		doc, err := parseIAMPolicy(string(vec.Policy))
		if err != nil {
			t.Fatalf("%s: parse policy: %v", vec.Name, err)
		}
		ctx := map[string][]string{}
		for k, v := range vec.Context {
			ctx[k] = []string{v}
		}
		got, _ := iamEvalDecisionForPrincipal([]iamPolicyDoc{doc}, vec.Action, vec.Resource, vec.CallerArn, ctx)
		if got != vec.Expect {
			t.Errorf("vector %q: decision = %q, want %q", vec.Name, got, vec.Expect)
		}
	}
}

// TestIAMConformance_Ratchet locks the set of known non-conformities. When you
// implement one, decrement the expected count + drop its row; when a new spec
// item is added unsupported, this fails until it's consciously classified. The
// failure message is the live non-conformity report.
//
// Both counts stand against something measured: an operator row is proved by
// TestIAMConformance_Operators running the evaluator, and a condition-key row
// by its probe building the gate's own context and finding the key in it. A
// literal asserted against a hand-written column would only be restating the
// column.
func TestIAMConformance_Ratchet(t *testing.T) {
	const wantUnsupportedOperators = 0
	const wantUnpopulatedConditionKeys = 0

	var unsupportedOps []string
	for _, op := range iamOperatorCatalog {
		if !op.supported {
			unsupportedOps = append(unsupportedOps, op.name)
		}
	}
	sort.Strings(unsupportedOps)
	unpopulatedKeys := iamUnpopulatedConditionKeys(t)

	if len(unsupportedOps) != wantUnsupportedOperators {
		t.Errorf("unsupported operators = %d %v, ratchet expects %d — update the count when you implement or add one",
			len(unsupportedOps), unsupportedOps, wantUnsupportedOperators)
	}
	if len(unpopulatedKeys) != wantUnpopulatedConditionKeys {
		t.Errorf("unpopulated condition keys = %d %v, ratchet expects %d — update the count when you wire or add one",
			len(unpopulatedKeys), unpopulatedKeys, wantUnpopulatedConditionKeys)
	}
	t.Logf("IAM conformance: %d/%d operators supported, %d/%d condition keys populated; gaps: ops=%v keys=%v",
		len(iamOperatorCatalog)-len(unsupportedOps), len(iamOperatorCatalog),
		len(iamConditionKeyRegistry)-len(unpopulatedKeys), len(iamConditionKeyRegistry),
		unsupportedOps, unpopulatedKeys)
}
