package aws_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEC2KeyPairsImagesMetadataCLI covers key pairs, DescribeImages filters, and
// ModifyInstanceMetadataOptions via the aws CLI.
func TestEC2KeyPairsImagesMetadataCLI(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	// Key pair create -> describe -> delete.
	fp := q("ec2", "create-key-pair", "--key-name", "cli-kp", "--query", "KeyFingerprint", "--output", "text")
	if fp == "" {
		t.Fatal("create-key-pair returned no fingerprint")
	}
	if n := q("ec2", "describe-key-pairs", "--key-names", "cli-kp",
		"--query", "length(KeyPairs)", "--output", "text"); n != "1" {
		t.Fatalf("describe-key-pairs after create: got %q, want 1", n)
	}
	runCLI(t, awsCLI("ec2", "delete-key-pair", "--key-name", "cli-kp"))
	if n := q("ec2", "describe-key-pairs", "--filters", "Name=key-name,Values=cli-kp",
		"--query", "length(KeyPairs)", "--output", "text"); n != "0" {
		t.Fatalf("describe-key-pairs after delete: got %q, want 0", n)
	}

	// An imported RSA key is fingerprinted by its public key's MD5 digest,
	// which ssh-keygen -l -E md5 reports for it, and an ED25519 pair by its
	// public key's base64 SHA-256 digest.
	keyFile := filepath.Join(t.TempDir(), "id_rsa.pub")
	if err := os.WriteFile(keyFile, []byte("ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTZERMdSlqD/Ot+7wjgU7BkrYryTecvQMxKvIVNkdzTuYr6DitxLO7j3mbGZT7ly8aNCcKWKXnRA0C1fp7q6GQ+rhttiBg0i6q5ArpUOIqrKt3BqjZPF+HEyWXb1/ip2flCP19PQHFmIhcceyineRE9pP5aeQ2fCBZOG9/JWFMUeSa269IxCs8XsJR1pQRVuI4wlsot0kuHTOYteMykgBjbwNnO7UhZTlJJVaoX0Prk1dFHjWnckgMYBA+ePIRoFWtHXcfhhM/bfQ5Got0JJET2umvr0Vb8Jh/el0rqNOBthz+uI+fAkLyvWmRwwRL1mkKe9G2dsltyDMYwYPV7WeN sockerless@test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fp := q("ec2", "import-key-pair", "--key-name", "cli-imported-kp", "--public-key-material", "fileb://"+keyFile,
		"--query", "KeyFingerprint", "--output", "text"); fp != "14:41:93:6c:46:cb:ab:57:36:8f:f6:91:57:b4:6a:09" {
		t.Fatalf("import-key-pair fingerprint: got %q", fp)
	}
	t.Cleanup(func() { runCLI(t, awsCLI("ec2", "delete-key-pair", "--key-name", "cli-imported-kp")) })
	if pub := q("ec2", "describe-key-pairs", "--key-names", "cli-imported-kp", "--include-public-key",
		"--query", "KeyPairs[0].PublicKey", "--output", "text"); pub != "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTZERMdSlqD/Ot+7wjgU7BkrYryTecvQMxKvIVNkdzTuYr6DitxLO7j3mbGZT7ly8aNCcKWKXnRA0C1fp7q6GQ+rhttiBg0i6q5ArpUOIqrKt3BqjZPF+HEyWXb1/ip2flCP19PQHFmIhcceyineRE9pP5aeQ2fCBZOG9/JWFMUeSa269IxCs8XsJR1pQRVuI4wlsot0kuHTOYteMykgBjbwNnO7UhZTlJJVaoX0Prk1dFHjWnckgMYBA+ePIRoFWtHXcfhhM/bfQ5Got0JJET2umvr0Vb8Jh/el0rqNOBthz+uI+fAkLyvWmRwwRL1mkKe9G2dsltyDMYwYPV7WeN sockerless@test" {
		t.Fatalf("describe-key-pairs --include-public-key: got %q", pub)
	}
	if fp := q("ec2", "create-key-pair", "--key-name", "cli-ed25519-kp", "--key-type", "ed25519",
		"--query", "KeyFingerprint", "--output", "text"); len(fp) != 44 || !strings.HasSuffix(fp, "=") {
		t.Fatalf("an ED25519 pair's fingerprint is a base64 SHA-256 digest: got %q", fp)
	}
	t.Cleanup(func() { runCLI(t, awsCLI("ec2", "delete-key-pair", "--key-name", "cli-ed25519-kp")) })

	// DescribeImages must honour the name + architecture filters.
	out := q("ec2", "describe-images", "--owners", "amazon",
		"--filters", "Name=name,Values=al2023-ami-2023*", "Name=architecture,Values=arm64",
		"--query", "Images[0].[Name,Architecture,ImageOwnerAlias]", "--output", "text")
	if f := strings.Fields(out); len(f) != 3 || !strings.HasPrefix(f[0], "al2023-ami-2023.") || !strings.HasSuffix(f[0], "-arm64") || f[1] != "arm64" || f[2] != "amazon" {
		t.Fatalf("describe-images filters: got %q, want the Amazon Linux 2023 arm64 AMI", out)
	}
	if n := q("ec2", "describe-images", "--owners", "136693071363",
		"--filters", "Name=name,Values=debian-12-*", "--query", "length(Images)", "--output", "text"); n != "2" {
		t.Fatalf("describe-images lists Debian 12 for amd64 and arm64, got %q", n)
	}

	// ModifyInstanceMetadataOptions in place.
	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.141.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.141.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	instID := q("ec2", "run-instances", "--image-id", ec2AmazonLinuxAMI(t), "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, instID)
	if v := q("ec2", "describe-instances", "--instance-ids", instID,
		"--query", "Reservations[0].Instances[0].MetadataOptions.HttpProtocolIpv6", "--output", "text"); v != "disabled" {
		t.Fatalf("metadata HttpProtocolIpv6 at launch: got %q, want disabled", v)
	}
	runCLI(t, awsCLI("ec2", "modify-instance-metadata-options", "--instance-id", instID,
		"--http-tokens", "required", "--http-put-response-hop-limit", "3", "--http-protocol-ipv6", "enabled"))
	if v := q("ec2", "describe-instances", "--instance-ids", instID,
		"--query", "Reservations[0].Instances[0].MetadataOptions.[HttpTokens,HttpProtocolIpv6]", "--output", "text"); v != "required\tenabled" {
		t.Fatalf("metadata options after modify: got %q, want required and enabled", v)
	}
}
