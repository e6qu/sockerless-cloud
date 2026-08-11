package aws_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// iamMFACLIUser creates a throwaway IAM user via the CLI with a tolerant cleanup.
func iamMFACLIUser(t *testing.T, name string) {
	t.Helper()
	runCLI(t, awsCLI("iam", "create-user", "--user-name", name))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", name)) })
}

// TestIAMCLI_VirtualMFADevice drives the MFA-device family via the aws CLI:
// create-virtual-mfa-device (the CLI customization decodes the Base32StringSeed
// blob to --outfile), enable-mfa-device, get-mfa-device, list-mfa-devices,
// list-virtual-mfa-devices, the tag ops, resync, deactivate, and delete.
func TestIAMCLI_VirtualMFADevice(t *testing.T) {
	user := "cli-mfa-user"
	iamMFACLIUser(t, user)

	outfile := filepath.Join(t.TempDir(), "seed.txt")
	serial := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-virtual-mfa-device",
		"--virtual-mfa-device-name", "cli-dev1",
		"--outfile", outfile,
		"--bootstrap-method", "Base32StringSeed",
		"--query", "VirtualMFADevice.SerialNumber", "--output", "text")))
	if !strings.Contains(serial, ":mfa/cli-dev1") {
		t.Fatalf("create-virtual-mfa-device serial = %q", serial)
	}
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-virtual-mfa-device", "--serial-number", serial)) })

	// The CLI wrote the real base32 seed to the outfile.
	seed, err := os.ReadFile(outfile)
	if err != nil || len(strings.TrimSpace(string(seed))) == 0 {
		t.Fatalf("create-virtual-mfa-device did not write a seed to outfile: err=%v len=%d", err, len(seed))
	}

	runCLI(t, awsCLI("iam", "enable-mfa-device",
		"--user-name", user, "--serial-number", serial,
		"--authentication-code1", "123456", "--authentication-code2", "234567"))

	got := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-mfa-device",
		"--serial-number", serial, "--query", "SerialNumber", "--output", "text")))
	if got != serial {
		t.Fatalf("get-mfa-device = %q, want %q", got, serial)
	}

	devs := runCLI(t, awsCLI("iam", "list-mfa-devices", "--user-name", user,
		"--query", "MFADevices[].SerialNumber", "--output", "text"))
	if !strings.Contains(devs, serial) {
		t.Fatalf("list-mfa-devices missing %q: %s", serial, devs)
	}

	vmfa := runCLI(t, awsCLI("iam", "list-virtual-mfa-devices", "--assignment-status", "Assigned",
		"--query", "VirtualMFADevices[].SerialNumber", "--output", "text"))
	if !strings.Contains(vmfa, serial) {
		t.Fatalf("list-virtual-mfa-devices (Assigned) missing %q: %s", serial, vmfa)
	}

	runCLI(t, awsCLI("iam", "tag-mfa-device", "--serial-number", serial,
		"--tags", "Key=env,Value=test"))
	tags := runCLI(t, awsCLI("iam", "list-mfa-device-tags", "--serial-number", serial,
		"--query", "Tags[?Key=='env'].Value", "--output", "text"))
	if !strings.Contains(tags, "test") {
		t.Fatalf("list-mfa-device-tags missing env=test: %s", tags)
	}
	runCLI(t, awsCLI("iam", "untag-mfa-device", "--serial-number", serial, "--tag-keys", "env"))

	runCLI(t, awsCLI("iam", "resync-mfa-device",
		"--user-name", user, "--serial-number", serial,
		"--authentication-code1", "345678", "--authentication-code2", "456789"))

	runCLI(t, awsCLI("iam", "deactivate-mfa-device", "--user-name", user, "--serial-number", serial))
	runCLI(t, awsCLI("iam", "delete-virtual-mfa-device", "--serial-number", serial))
}

// TestIAMCLI_SSHPublicKey drives upload/get/list/update/delete-ssh-public-key.
func TestIAMCLI_SSHPublicKey(t *testing.T) {
	user := "cli-ssh-user"
	iamMFACLIUser(t, user)

	const body = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDcliexample sockerless@cli"
	keyID := strings.TrimSpace(runCLI(t, awsCLI("iam", "upload-ssh-public-key",
		"--user-name", user, "--ssh-public-key-body", body,
		"--query", "SSHPublicKey.SSHPublicKeyId", "--output", "text")))
	if !strings.HasPrefix(keyID, "APKA") {
		t.Fatalf("upload-ssh-public-key id = %q", keyID)
	}

	gotBody := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-ssh-public-key",
		"--user-name", user, "--ssh-public-key-id", keyID, "--encoding", "SSH",
		"--query", "SSHPublicKey.SSHPublicKeyBody", "--output", "text")))
	if gotBody != body {
		t.Fatalf("get-ssh-public-key body = %q, want %q", gotBody, body)
	}

	keys := runCLI(t, awsCLI("iam", "list-ssh-public-keys", "--user-name", user,
		"--query", "SSHPublicKeys[].SSHPublicKeyId", "--output", "text"))
	if !strings.Contains(keys, keyID) {
		t.Fatalf("list-ssh-public-keys missing %q: %s", keyID, keys)
	}

	runCLI(t, awsCLI("iam", "update-ssh-public-key",
		"--user-name", user, "--ssh-public-key-id", keyID, "--status", "Inactive"))
	status := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-ssh-public-key",
		"--user-name", user, "--ssh-public-key-id", keyID, "--encoding", "SSH",
		"--query", "SSHPublicKey.Status", "--output", "text")))
	if status != "Inactive" {
		t.Fatalf("update-ssh-public-key status = %q, want Inactive", status)
	}

	runCLI(t, awsCLI("iam", "delete-ssh-public-key", "--user-name", user, "--ssh-public-key-id", keyID))
}

// TestIAMCLI_SigningCertificate drives upload/list/update/delete-signing-certificate.
func TestIAMCLI_SigningCertificate(t *testing.T) {
	user := "cli-cert-user"
	iamMFACLIUser(t, user)

	const pem = "-----BEGIN CERTIFICATE-----\nMIIBclisigningcert==\n-----END CERTIFICATE-----"
	certID := strings.TrimSpace(runCLI(t, awsCLI("iam", "upload-signing-certificate",
		"--user-name", user, "--certificate-body", pem,
		"--query", "Certificate.CertificateId", "--output", "text")))
	if certID == "" {
		t.Fatal("upload-signing-certificate returned empty CertificateId")
	}

	certs := runCLI(t, awsCLI("iam", "list-signing-certificates", "--user-name", user,
		"--query", "Certificates[].CertificateId", "--output", "text"))
	if !strings.Contains(certs, certID) {
		t.Fatalf("list-signing-certificates missing %q: %s", certID, certs)
	}

	runCLI(t, awsCLI("iam", "update-signing-certificate",
		"--user-name", user, "--certificate-id", certID, "--status", "Inactive"))
	status := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-signing-certificates", "--user-name", user,
		"--query", "Certificates[0].Status", "--output", "text")))
	if status != "Inactive" {
		t.Fatalf("update-signing-certificate status = %q, want Inactive", status)
	}

	runCLI(t, awsCLI("iam", "delete-signing-certificate", "--user-name", user, "--certificate-id", certID))
}

// TestIAMCLI_ServiceSpecificCredential drives create/list/reset/update/delete-service-specific-credential.
func TestIAMCLI_ServiceSpecificCredential(t *testing.T) {
	user := "cli-svccred-user"
	iamMFACLIUser(t, user)

	credID := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-service-specific-credential",
		"--user-name", user, "--service-name", "codecommit.amazonaws.com",
		"--query", "ServiceSpecificCredential.ServiceSpecificCredentialId", "--output", "text")))
	if credID == "" {
		t.Fatal("create-service-specific-credential returned empty id")
	}

	creds := runCLI(t, awsCLI("iam", "list-service-specific-credentials", "--user-name", user,
		"--query", "ServiceSpecificCredentials[].ServiceSpecificCredentialId", "--output", "text"))
	if !strings.Contains(creds, credID) {
		t.Fatalf("list-service-specific-credentials missing %q: %s", credID, creds)
	}

	pw := strings.TrimSpace(runCLI(t, awsCLI("iam", "reset-service-specific-credential",
		"--user-name", user, "--service-specific-credential-id", credID,
		"--query", "ServiceSpecificCredential.ServicePassword", "--output", "text")))
	if pw == "" {
		t.Fatal("reset-service-specific-credential returned empty password")
	}

	runCLI(t, awsCLI("iam", "update-service-specific-credential",
		"--user-name", user, "--service-specific-credential-id", credID, "--status", "Inactive"))
	status := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-service-specific-credentials", "--user-name", user,
		"--query", "ServiceSpecificCredentials[0].Status", "--output", "text")))
	if status != "Inactive" {
		t.Fatalf("update-service-specific-credential status = %q, want Inactive", status)
	}

	runCLI(t, awsCLI("iam", "delete-service-specific-credential",
		"--user-name", user, "--service-specific-credential-id", credID))
}
