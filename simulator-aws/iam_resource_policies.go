package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// Several services attach a policy to a resource (an Amazon S3 bucket policy, an
// AWS Lambda function policy, an Amazon SNS topic or Amazon SQS queue policy).
// Each service keeps its own storage and read-back shape and mirrors the policy
// JSON here, keyed by resource ARN, so the enforcement gate finds it in one
// place through iamResourcePolicyDocsForARN.

type IAMResourcePolicy struct {
	ARN    string `json:"arn"`
	Policy string `json:"policy"`
}

var iamResourcePolicies sim.Store[IAMResourcePolicy]

func registerIAMResourcePolicies(srv *sim.Server) {
	iamResourcePolicies = sim.MakeStore[IAMResourcePolicy](srv.DB(), "iam_resource_policies")
}

func iamPutResourcePolicy(arn, policyJSON string) {
	iamResourcePolicies.Put(arn, IAMResourcePolicy{ARN: arn, Policy: policyJSON})
}

func iamGetResourcePolicy(arn string) (string, bool) {
	rp, ok := iamResourcePolicies.Get(arn)
	if !ok {
		return "", false
	}
	return rp.Policy, true
}

func iamDeleteResourcePolicy(arn string) {
	iamResourcePolicies.Delete(arn)
}

// iamResourcePolicyDocsForARN returns the resource-based policy documents
// attached to arn; the enforcement gate calls it. A stored document that fails
// to parse contributes no statements.
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
