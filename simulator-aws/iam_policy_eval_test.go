package main

import "testing"

func mustDoc(t *testing.T, s string) iamPolicyDoc {
	t.Helper()
	d, err := parseIAMPolicy(s)
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	return d
}

// TestIAMEval_ConditionOperators covers the condition operators added for
// fidelity: Numeric*, Date*, IpAddress/NotIpAddress, Null, and the
// ForAllValues/ForAnyValue set qualifiers.
func TestIAMEval_ConditionOperators(t *testing.T) {
	cases := []struct {
		name   string
		policy string
		ctx    map[string][]string
		want   string
	}{
		{
			"IpAddress in CIDR allows",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}}]}`,
			map[string][]string{"aws:SourceIp": {"10.1.2.3"}}, "allowed",
		},
		{
			"IpAddress outside CIDR not allowed",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}}]}`,
			map[string][]string{"aws:SourceIp": {"192.168.0.1"}}, "implicitDeny",
		},
		{
			"NumericLessThan true",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"NumericLessThan":{"s3:max-keys":"100"}}}]}`,
			map[string][]string{"s3:max-keys": {"50"}}, "allowed",
		},
		{
			"NumericLessThan false",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"NumericLessThan":{"s3:max-keys":"100"}}}]}`,
			map[string][]string{"s3:max-keys": {"500"}}, "implicitDeny",
		},
		{
			"DateGreaterThan true",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"DateGreaterThan":{"aws:CurrentTime":"2020-01-01T00:00:00Z"}}}]}`,
			map[string][]string{"aws:CurrentTime": {"2025-06-01T00:00:00Z"}}, "allowed",
		},
		{
			"Null requires key absent",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"Null":{"aws:TokenIssueTime":"true"}}}]}`,
			map[string][]string{}, "allowed",
		},
		{
			"Null=true denies when key present",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"Null":{"aws:TokenIssueTime":"true"}}}]}`,
			map[string][]string{"aws:TokenIssueTime": {"2025-01-01T00:00:00Z"}}, "implicitDeny",
		},
		{
			"ForAllValues all match",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"ForAllValues:StringEquals":{"aws:TagKeys":["env","team"]}}}]}`,
			map[string][]string{"aws:TagKeys": {"env"}}, "allowed",
		},
		{
			"ForAllValues a value outside the set fails",
			`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"ForAllValues:StringEquals":{"aws:TagKeys":["env","team"]}}}]}`,
			map[string][]string{"aws:TagKeys": {"env", "secret"}}, "implicitDeny",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := iamEvalDecision([]iamPolicyDoc{mustDoc(t, tc.policy)}, "s3:GetObject", "*", tc.ctx)
			if got != tc.want {
				t.Fatalf("decision = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIAMEval_PolicyVariables checks that ${aws:username} in a Resource is
// substituted from context before matching.
func TestIAMEval_PolicyVariables(t *testing.T) {
	doc := mustDoc(t, `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::bucket/${aws:username}/*"}]}`)
	ctx := map[string][]string{"aws:username": {"alice"}}
	if got, _ := iamEvalDecision([]iamPolicyDoc{doc}, "s3:GetObject", "arn:aws:s3:::bucket/alice/file.txt", ctx); got != "allowed" {
		t.Fatalf("alice's own prefix: decision = %q, want allowed", got)
	}
	if got, _ := iamEvalDecision([]iamPolicyDoc{doc}, "s3:GetObject", "arn:aws:s3:::bucket/bob/file.txt", ctx); got != "implicitDeny" {
		t.Fatalf("bob's prefix: decision = %q, want implicitDeny", got)
	}
}

// TestIAMEval_Principal checks resource-based-policy Principal matching: a policy
// naming a specific principal must not grant a different caller.
func TestIAMEval_Principal(t *testing.T) {
	doc := mustDoc(t, `{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/app"},"Action":"sqs:SendMessage","Resource":"*"}]}`)
	if got, _ := iamEvalDecisionForPrincipal([]iamPolicyDoc{doc}, "sqs:SendMessage", "*", "arn:aws:sts::000000000000:assumed-role/app/sess", nil); got != "allowed" {
		t.Fatalf("named principal: decision = %q, want allowed", got)
	}
	if got, _ := iamEvalDecisionForPrincipal([]iamPolicyDoc{doc}, "sqs:SendMessage", "*", "arn:aws:iam::000000000000:user/other", nil); got != "implicitDeny" {
		t.Fatalf("other principal: decision = %q, want implicitDeny", got)
	}
	wild := mustDoc(t, `{"Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"*"}]}`)
	if got, _ := iamEvalDecisionForPrincipal([]iamPolicyDoc{wild}, "sqs:SendMessage", "*", "arn:aws:iam::000000000000:user/anyone", nil); got != "allowed" {
		t.Fatalf("wildcard principal: decision = %q, want allowed", got)
	}
}
