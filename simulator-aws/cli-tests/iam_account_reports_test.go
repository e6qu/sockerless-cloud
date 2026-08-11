package aws_cli_test

import (
	"strings"
	"testing"
)

// IAM account-reports / service-last-accessed / Organizations-root families
// driven through the aws CLI (appdata shard).
//
// CLI coverage note: the cross-account delegation family
// (create/get/list/accept/reject/associate/update-delegation-request,
// send-delegation-token), the outbound web-identity-federation family
// (enable/disable/get-outbound-web-identity-federation*), and
// get-human-readable-summary are absent from aws CLI 2.26.6, so they are
// exercised through the SDK (sdk-tests/iam_account_reports_test.go) which
// covers the simulator-test contract hook for those operations. Everything the
// CLI exposes is covered here.

// TestIAMAccountReportsCLI_SummaryAndAuthDetails drives get-account-summary and
// get-account-authorization-details against the real IAM stores.
func TestIAMAccountReportsCLI_SummaryAndAuthDetails(t *testing.T) {
	user := "iamar-cli-user"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-user-policy", "--user-name", user, "--policy-name", "iamar-cli-inline"))
		runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", user))
	})

	runCLI(t, awsCLI("iam", "put-user-policy",
		"--user-name", user,
		"--policy-name", "iamar-cli-inline",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`))

	usersCount := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-account-summary",
		"--query", "SummaryMap.Users", "--output", "text")))
	if usersCount == "" || usersCount == "None" {
		t.Fatalf("get-account-summary returned no Users count: %q", usersCount)
	}
	quota := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-account-summary",
		"--query", "SummaryMap.UsersQuota", "--output", "text")))
	if quota != "5000" {
		t.Fatalf("UsersQuota = %q, want 5000", quota)
	}

	inline := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-account-authorization-details",
		"--query", "UserDetailList[?UserName=='"+user+"'].UserPolicyList[0].PolicyName", "--output", "text")))
	if !strings.Contains(inline, "iamar-cli-inline") {
		t.Fatalf("get-account-authorization-details missing inline policy: %q", inline)
	}
}

// TestIAMAccountReportsCLI_CredentialReport drives the generate→get credential
// report flow and asserts the CSV enumerates the real users.
func TestIAMAccountReportsCLI_CredentialReport(t *testing.T) {
	user := "iamar-cli-credrep"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", user)) })

	runCLI(t, awsCLI("iam", "generate-credential-report"))
	runCLI(t, awsCLI("iam", "generate-credential-report"))

	// The CLI base64-decodes Content automatically into the CSV text.
	report := runCLI(t, awsCLI("iam", "get-credential-report",
		"--query", "Content", "--output", "text"))
	// --output text leaves Content base64-encoded; assert the format field too.
	format := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-credential-report",
		"--query", "ReportFormat", "--output", "text")))
	if format != "text/csv" {
		t.Fatalf("ReportFormat = %q, want text/csv", format)
	}
	if strings.TrimSpace(report) == "" {
		t.Fatalf("get-credential-report returned empty Content")
	}
}

// TestIAMAccountReportsCLI_ServiceLastAccessed drives the generate→get service-
// last-accessed flow plus the with-entities variant and the Organizations
// access report.
func TestIAMAccountReportsCLI_ServiceLastAccessed(t *testing.T) {
	role := "iamar-cli-sla-role"
	runCLI(t, awsCLI("iam", "create-role",
		"--role-name", role,
		"--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-role-policy", "--role-name", role, "--policy-name", "sla-inline"))
		runCLIIgnore(awsCLI("iam", "delete-role", "--role-name", role))
	})
	runCLI(t, awsCLI("iam", "put-role-policy",
		"--role-name", role,
		"--policy-name", "sla-inline",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","ec2:DescribeInstances"],"Resource":"*"}]}`))

	roleArn := "arn:aws:iam::123456789012:role/" + role
	jobID := strings.TrimSpace(runCLI(t, awsCLI("iam", "generate-service-last-accessed-details",
		"--arn", roleArn, "--query", "JobId", "--output", "text")))
	if jobID == "" {
		t.Fatalf("generate-service-last-accessed-details returned no JobId")
	}

	status := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-service-last-accessed-details",
		"--job-id", jobID, "--query", "JobStatus", "--output", "text")))
	if status != "COMPLETED" {
		t.Fatalf("get-service-last-accessed-details JobStatus = %q, want COMPLETED", status)
	}
	namespaces := runCLI(t, awsCLI("iam", "get-service-last-accessed-details",
		"--job-id", jobID, "--query", "ServicesLastAccessed[].ServiceNamespace", "--output", "text"))
	if !strings.Contains(namespaces, "s3") || !strings.Contains(namespaces, "ec2") {
		t.Fatalf("service-last-accessed namespaces missing s3/ec2: %q", namespaces)
	}

	entStatus := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-service-last-accessed-details-with-entities",
		"--job-id", jobID, "--service-namespace", "s3", "--query", "JobStatus", "--output", "text")))
	if entStatus != "COMPLETED" {
		t.Fatalf("with-entities JobStatus = %q, want COMPLETED", entStatus)
	}

	orgJob := strings.TrimSpace(runCLI(t, awsCLI("iam", "generate-organizations-access-report",
		"--entity-path", "o-abc123/r-root/123456789012", "--query", "JobId", "--output", "text")))
	if orgJob == "" {
		t.Fatalf("generate-organizations-access-report returned no JobId")
	}
	orgStatus := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-organizations-access-report",
		"--job-id", orgJob, "--query", "JobStatus", "--output", "text")))
	if orgStatus != "COMPLETED" {
		t.Fatalf("get-organizations-access-report JobStatus = %q, want COMPLETED", orgStatus)
	}
}

// TestIAMAccountReportsCLI_OrganizationsRootAndSTS drives the Organizations-root
// credential-management + sessions toggles, list-organizations-features, and
// set-security-token-service-preferences.
func TestIAMAccountReportsCLI_OrganizationsRootAndSTS(t *testing.T) {
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "disable-organizations-root-sessions"))
		runCLIIgnore(awsCLI("iam", "disable-organizations-root-credentials-management"))
	})

	feats := runCLI(t, awsCLI("iam", "enable-organizations-root-credentials-management",
		"--query", "EnabledFeatures", "--output", "text"))
	if !strings.Contains(feats, "RootCredentialsManagement") {
		t.Fatalf("enable-organizations-root-credentials-management missing feature: %q", feats)
	}

	runCLI(t, awsCLI("iam", "enable-organizations-root-sessions"))
	listed := runCLI(t, awsCLI("iam", "list-organizations-features",
		"--query", "EnabledFeatures", "--output", "text"))
	if !strings.Contains(listed, "RootCredentialsManagement") || !strings.Contains(listed, "RootSessions") {
		t.Fatalf("list-organizations-features missing features: %q", listed)
	}

	disabled := runCLI(t, awsCLI("iam", "disable-organizations-root-sessions",
		"--query", "EnabledFeatures", "--output", "text"))
	if strings.Contains(disabled, "RootSessions") {
		t.Fatalf("disable-organizations-root-sessions still lists RootSessions: %q", disabled)
	}

	// set-security-token-service-preferences has no output; success = no error.
	runCLI(t, awsCLI("iam", "set-security-token-service-preferences",
		"--global-endpoint-token-version", "v2Token"))
}
