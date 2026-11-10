package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The account's CloudWatch Logs resource policies apply to every log group
// and log stream in the Region, so the gate reads all of them for any such
// ARN; their Resource elements decide which they grant.

const cwResourcePoliciesAll = "account"

var cwResourcePoliciesIndex sim.GenerationIndex[CWResourcePolicy]

func cwResourcePolicyDocs() []iamPolicyDoc {
	if cwResourcePolicies == nil {
		return nil
	}
	rows := cwResourcePoliciesIndex.LookupAll(cwResourcePolicies, cwResourcePoliciesAll, func(CWResourcePolicy) []string {
		return []string{cwResourcePoliciesAll}
	})
	docs := make([]iamPolicyDoc, 0, len(rows))
	for _, p := range rows {
		if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

// cwLogGroupOfARN returns the log group a log group or log stream ARN names.
func cwLogGroupOfARN(arn string) (string, bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[2] != "logs" {
		return "", false
	}
	rest, ok := strings.CutPrefix(parts[5], "log-group:")
	if !ok {
		return "", false
	}
	group, _, _ := strings.Cut(rest, ":log-stream:")
	group = strings.TrimSuffix(group, ":*")
	return group, group != ""
}
