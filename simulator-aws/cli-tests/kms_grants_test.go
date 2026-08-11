package aws_cli_test

import (
	"strings"
	"testing"
)

// TestKMSCLI_Grants drives the KMS grant ops via the aws CLI:
// create-grant → list-grants → revoke-grant, plus the encrypted-only data key.
func TestKMSCLI_Grants(t *testing.T) {
	keyID := strings.TrimSpace(runCLI(t, awsCLI("kms", "create-key",
		"--description", "cli-grant", "--query", "KeyMetadata.KeyId", "--output", "text")))
	if keyID == "" {
		t.Fatal("expected a key id")
	}

	grantID := strings.TrimSpace(runCLI(t, awsCLI("kms", "create-grant",
		"--key-id", keyID,
		"--grantee-principal", "arn:aws:iam::000000000000:role/test",
		"--operations", "Decrypt", "GenerateDataKey",
		"--query", "GrantId", "--output", "text")))
	if grantID == "" {
		t.Fatal("expected a grant id")
	}

	list := runCLI(t, awsCLI("kms", "list-grants", "--key-id", keyID))
	if !strings.Contains(list, grantID) {
		t.Fatalf("list-grants did not include %q: %s", grantID, list)
	}

	runCLI(t, awsCLI("kms", "revoke-grant", "--key-id", keyID, "--grant-id", grantID))

	after := runCLI(t, awsCLI("kms", "list-grants", "--key-id", keyID))
	if strings.Contains(after, grantID) {
		t.Fatalf("grant %q still present after revoke: %s", grantID, after)
	}

	blob := strings.TrimSpace(runCLI(t, awsCLI("kms", "generate-data-key-without-plaintext",
		"--key-id", keyID, "--key-spec", "AES_256", "--query", "CiphertextBlob", "--output", "text")))
	if blob == "" {
		t.Fatal("generate-data-key-without-plaintext returned no ciphertext")
	}
}
