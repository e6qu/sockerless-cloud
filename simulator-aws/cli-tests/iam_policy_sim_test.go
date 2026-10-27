package aws_cli_test

import (
	"strings"
	"testing"
)

// TestIAMSimulateCustomPolicyCLI checks least privilege locally, as a
// consumer does with simulate-custom-policy.
func TestIAMSimulateCustomPolicyCLI(t *testing.T) {
	t.Parallel()
	denyPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"ec2:DeleteVolume","Resource":"*"}]}`
	decision := strings.TrimSpace(runCLI(t, awsCLI("iam", "simulate-custom-policy",
		"--policy-input-list", denyPolicy,
		"--action-names", "ec2:DeleteVolume",
		"--query", "EvaluationResults[0].EvalDecision", "--output", "text")))
	if decision != "explicitDeny" {
		t.Fatalf("expected explicitDeny, got %q", decision)
	}

	allowPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:Get*","Resource":"*"}]}`
	decision = strings.TrimSpace(runCLI(t, awsCLI("iam", "simulate-custom-policy",
		"--policy-input-list", allowPolicy,
		"--action-names", "s3:GetObject",
		"--query", "EvaluationResults[0].EvalDecision", "--output", "text")))
	if decision != "allowed" {
		t.Fatalf("expected allowed, got %q", decision)
	}

	// An unmatched action is an implicit deny.
	decision = strings.TrimSpace(runCLI(t, awsCLI("iam", "simulate-custom-policy",
		"--policy-input-list", allowPolicy,
		"--action-names", "s3:PutObject",
		"--query", "EvaluationResults[0].EvalDecision", "--output", "text")))
	if decision != "implicitDeny" {
		t.Fatalf("expected implicitDeny, got %q", decision)
	}
}
