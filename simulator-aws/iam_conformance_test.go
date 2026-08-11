package main

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
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
//     into the request context. The `populated:false` rows ARE the known
//     non-conformities (a policy conditioned on them can't enforce yet).
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

// iamConditionKeySpec records whether the enforcement gate resolves a condition
// key into the request context. populated:false rows are known gaps.
type iamConditionKeySpec struct {
	key       string
	populated bool
	note      string
}

// iamConditionKeyRegistry is the map of which condition keys the gate feeds.
// Add a row when a new key becomes relevant; flip populated when it's wired.
var iamConditionKeyRegistry = []iamConditionKeySpec{
	{"aws:username", true, "the caller's IAM user name"},
	{"aws:userid", true, "the caller's unique id"},
	{"aws:SourceIp", true, "the request's remote IP"},
	{"aws:RequestedRegion", true, "the SigV4 credential scope region"},
	{"aws:ResourceTag/*", true, "the target resource's tags (EC2)"},
	{"ec2:ResourceTag/*", true, "the target EC2 resource's tags"},
	{"ecs:cluster", true, "the cluster ARN an ECS task op targets"},
	{"aws:RequestTag/*", true, "tags supplied on a tag-on-create / CreateTags request"},
	{"aws:TagKeys", true, "keys supplied on a tag-on-create request"},
	{"aws:CurrentTime", true, "the request timestamp (RFC3339)"},
	{"aws:EpochTime", true, "the request timestamp (epoch)"},
	{"aws:SecureTransport", true, "whether the request connection used TLS"},
	{"aws:UserAgent", true, "the request User-Agent header"},
	{"aws:PrincipalArn", true, "the calling principal's ARN"},
	{"aws:PrincipalTag/*", true, "the caller's principal (user/role) tags"},
	{"aws:ResourceAccount", true, "the target resource's owning account"},
	{"aws:MultiFactorAuthPresent", true, "MFA on the session (STS MFA-authenticated session)"},
	{"aws:MultiFactorAuthAge", true, "age in seconds of an MFA-authenticated session"},
	{"aws:PrincipalOrgID", true, "the account's AWS Organizations id (the Organizations slice)"},
	{"aws:ViaAWSService", true, "false on a direct call; true for a sim service-initiated delivery"},
	{"aws:SourceArn", true, "the originating resource ARN on a service-initiated delivery"},
	{"aws:SourceAccount", true, "the originating account on a service-initiated delivery"},
	{"aws:CalledVia", true, "the calling service on a service-initiated delivery"},
	{"<service>:ResourceTag/* (16 services beyond EC2)", true, "lambda/sqs/sns/rds/dynamodb/s3/... resource tags"},
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
func TestIAMConformance_Ratchet(t *testing.T) {
	const wantUnsupportedOperators = 0
	const wantUnpopulatedConditionKeys = 0

	var unsupportedOps, unpopulatedKeys []string
	for _, op := range iamOperatorCatalog {
		if !op.supported {
			unsupportedOps = append(unsupportedOps, op.name)
		}
	}
	for _, k := range iamConditionKeyRegistry {
		if !k.populated {
			unpopulatedKeys = append(unpopulatedKeys, k.key)
		}
	}
	sort.Strings(unsupportedOps)
	sort.Strings(unpopulatedKeys)

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
