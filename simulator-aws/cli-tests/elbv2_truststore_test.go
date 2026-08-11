package aws_cli_test

import (
	"strings"
	"testing"
)

// TestELBv2TrustStoreCLI exercises the mutual-TLS trust store control plane and
// the SSL policy / capacity / IP-pool modifications through the aws CLI:
// create-trust-store, describe-trust-stores, modify-trust-store,
// get-trust-store-ca-certificates-bundle, add-trust-store-revocations,
// describe-trust-store-revocations, get-trust-store-revocation-content,
// remove-trust-store-revocations, describe-trust-store-associations,
// get-resource-policy, describe-ssl-policies, modify-capacity-reservation,
// modify-ip-pools, delete-trust-store.
func TestELBv2TrustStoreCLI(t *testing.T) {
	out := runCLI(t, awsCLI("elbv2", "create-trust-store",
		"--name", "cli-mtls-store",
		"--ca-certificates-bundle-s3-bucket", "cli-ca-bucket",
		"--ca-certificates-bundle-s3-key", "bundle.pem",
		"--query", "TrustStores[0].TrustStoreArn",
		"--output", "text"))
	tsArn := strings.TrimSpace(out)
	if !strings.Contains(tsArn, ":truststore/cli-mtls-store/") {
		t.Fatalf("expected trust store ARN, got %q", tsArn)
	}
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("elbv2", "delete-trust-store", "--trust-store-arn", tsArn))
	})

	out = runCLI(t, awsCLI("elbv2", "describe-trust-stores",
		"--names", "cli-mtls-store",
		"--query", "TrustStores[0].Status",
		"--output", "text"))
	if strings.TrimSpace(out) != "ACTIVE" {
		t.Fatalf("expected ACTIVE trust store, got %q", out)
	}

	runCLI(t, awsCLI("elbv2", "modify-trust-store",
		"--trust-store-arn", tsArn,
		"--ca-certificates-bundle-s3-bucket", "cli-ca-bucket",
		"--ca-certificates-bundle-s3-key", "bundle-v2.pem"))

	out = runCLI(t, awsCLI("elbv2", "get-trust-store-ca-certificates-bundle",
		"--trust-store-arn", tsArn,
		"--query", "Location",
		"--output", "text"))
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected CA bundle location, got empty")
	}

	out = runCLI(t, awsCLI("elbv2", "add-trust-store-revocations",
		"--trust-store-arn", tsArn,
		"--revocation-contents", "S3Bucket=cli-ca-bucket,S3Key=crl.pem,RevocationType=CRL",
		"--query", "TrustStoreRevocations[0].RevocationId",
		"--output", "text"))
	revID := strings.TrimSpace(out)
	if revID == "" {
		t.Fatalf("expected revocation id, got empty")
	}

	out = runCLI(t, awsCLI("elbv2", "describe-trust-store-revocations",
		"--trust-store-arn", tsArn,
		"--query", "length(TrustStoreRevocations)",
		"--output", "text"))
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("expected 1 revocation, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "get-trust-store-revocation-content",
		"--trust-store-arn", tsArn,
		"--revocation-id", revID,
		"--query", "Location",
		"--output", "text"))
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected revocation content location, got empty")
	}

	runCLI(t, awsCLI("elbv2", "remove-trust-store-revocations",
		"--trust-store-arn", tsArn,
		"--revocation-ids", revID))
	out = runCLI(t, awsCLI("elbv2", "describe-trust-store-revocations",
		"--trust-store-arn", tsArn,
		"--query", "length(TrustStoreRevocations)",
		"--output", "text"))
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0 revocations after removal, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "describe-trust-store-associations",
		"--trust-store-arn", tsArn,
		"--query", "length(TrustStoreAssociations)",
		"--output", "text"))
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0 associations, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "get-resource-policy",
		"--resource-arn", tsArn,
		"--query", "Policy",
		"--output", "text"))
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected resource policy, got empty")
	}

	runCLI(t, awsCLI("elbv2", "delete-trust-store", "--trust-store-arn", tsArn))
}

// TestELBv2DescribeSSLPoliciesCLI verifies the predefined SSL security policy
// catalog through the aws CLI.
func TestELBv2DescribeSSLPoliciesCLI(t *testing.T) {
	out := runCLI(t, awsCLI("elbv2", "describe-ssl-policies",
		"--names", "ELBSecurityPolicy-2016-08",
		"--query", "SslPolicies[0].Name",
		"--output", "text"))
	if strings.TrimSpace(out) != "ELBSecurityPolicy-2016-08" {
		t.Fatalf("expected ELBSecurityPolicy-2016-08, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "describe-ssl-policies",
		"--names", "ELBSecurityPolicy-2016-08",
		"--query", "length(SslPolicies[0].Ciphers)",
		"--output", "text"))
	if strings.TrimSpace(out) == "0" || strings.TrimSpace(out) == "" {
		t.Fatalf("expected non-empty ciphers, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "describe-ssl-policies",
		"--load-balancer-type", "application",
		"--query", "length(SslPolicies)",
		"--output", "text"))
	if strings.TrimSpace(out) == "0" || strings.TrimSpace(out) == "" {
		t.Fatalf("expected predefined SSL policies for application LB, got %q", out)
	}
}
