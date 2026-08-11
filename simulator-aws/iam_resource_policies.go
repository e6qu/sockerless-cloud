package main

import (
	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Resource-based IAM policies. Several AWS services attach a policy document
// directly to a resource (an S3 bucket policy, a Lambda function policy, an SNS
// topic policy, an SQS queue policy) rather than to a principal. The call-time
// enforcement gate (iam_enforcement.go) consults these alongside the caller's
// identity-based policies via iamResourcePolicyDocsForARN.
//
// Each service still owns its native storage + read-back shape (S3 round-trips
// the raw bucket-policy bytes, Lambda assembles a policy document from
// AddPermission statements, SNS/SQS echo the Policy attribute). On every set,
// the service ALSO mirrors the policy JSON into this central store keyed by the
// resource ARN, so the resolver has one uniform place to find it regardless of
// which service set it.

// IAMResourcePolicy holds the policy JSON document attached to a single
// resource ARN.
type IAMResourcePolicy struct {
	ARN    string `json:"arn"`
	Policy string `json:"policy"`
}

var iamResourcePolicies sim.Store[IAMResourcePolicy]

func registerIAMResourcePolicies(srv *sim.Server) {
	iamResourcePolicies = sim.MakeStore[IAMResourcePolicy](srv.DB(), "iam_resource_policies")
}

// iamPutResourcePolicy stores (or replaces) the policy JSON attached to arn.
func iamPutResourcePolicy(arn, policyJSON string) {
	iamResourcePolicies.Put(arn, IAMResourcePolicy{ARN: arn, Policy: policyJSON})
}

// iamGetResourcePolicy returns the policy JSON attached to arn, if any.
func iamGetResourcePolicy(arn string) (string, bool) {
	rp, ok := iamResourcePolicies.Get(arn)
	if !ok {
		return "", false
	}
	return rp.Policy, true
}

// iamDeleteResourcePolicy removes any policy attached to arn.
func iamDeleteResourcePolicy(arn string) {
	iamResourcePolicies.Delete(arn)
}

// iamResourcePolicyDocsForARN returns the parsed resource-based policy
// document(s) attached to the given resource ARN. Returns nil if none is
// attached (or the stored document fails to parse — a malformed resource
// policy contributes no statements rather than blocking evaluation). This is
// the entry point the enforcement gate calls.
func iamResourcePolicyDocsForARN(arn string) []iamPolicyDoc {
	if arn == "" || iamResourcePolicies == nil {
		return nil
	}
	policy, ok := iamGetResourcePolicy(arn)
	if !ok || policy == "" {
		return nil
	}
	doc, err := parseIAMPolicy(policy)
	if err != nil {
		return nil
	}
	return []iamPolicyDoc{doc}
}
