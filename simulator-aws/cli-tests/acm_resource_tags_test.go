package aws_cli_test

import (
	"strings"
	"testing"
)

// TestACMCLI_ResourceTagging covers the ARN-addressed tagging API AWS
// Certificate Manager exposes alongside the certificate-scoped
// add-tags-to-certificate family: tag-resource, list-tags-for-resource and
// untag-resource.
func TestACMCLI_ResourceTagging(t *testing.T) {
	arn := strings.TrimSpace(runCLI(t, awsCLI("acm", "request-certificate",
		"--domain-name", "cli-tagging.example.com",
		"--validation-method", "DNS",
		"--query", "CertificateArn", "--output", "text")))
	if arn == "" {
		t.Fatal("request-certificate returned no CertificateArn")
	}
	defer func() { _ = awsCLI("acm", "delete-certificate", "--certificate-arn", arn).Run() }()

	runCLI(t, awsCLI("acm", "tag-resource", "--resource-arn", arn,
		"--tags", "Key=env,Value=prod", "Key=owner,Value=platform"))

	count := strings.TrimSpace(runCLI(t, awsCLI("acm", "list-tags-for-resource",
		"--resource-arn", arn, "--query", "length(Tags)", "--output", "text")))
	if count != "2" {
		t.Fatalf("after tagging, Tags length = %q, want 2", count)
	}

	runCLI(t, awsCLI("acm", "untag-resource", "--resource-arn", arn, "--tag-keys", "owner"))
	remaining := strings.TrimSpace(runCLI(t, awsCLI("acm", "list-tags-for-resource",
		"--resource-arn", arn, "--query", "Tags[0].Key", "--output", "text")))
	if remaining != "env" {
		t.Fatalf("after untag, remaining tag key = %q, want env", remaining)
	}

	// An ARN naming no ACM resource fails loudly.
	if err := awsCLI("acm", "list-tags-for-resource",
		"--resource-arn", "arn:aws:acm:us-east-1:000000000000:certificate/does-not-exist").Run(); err == nil {
		t.Fatal("list-tags-for-resource should fail for an unknown resource ARN")
	}
}
