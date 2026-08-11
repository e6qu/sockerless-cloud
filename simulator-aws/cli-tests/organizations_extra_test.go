package aws_cli_test

import (
	"strings"
	"testing"
)

// TestOrganizationsCLI_GovCloudAccount exercises create-gov-cloud-account over
// the aws CLI: the CreateAccountStatus settles to SUCCEEDED carrying both
// AccountId (commercial) and GovCloudAccountId, and the status is readable via
// describe-create-account-status.
func TestOrganizationsCLI_GovCloudAccount(t *testing.T) {
	out := runCLI(t, awsCLI("organizations", "create-gov-cloud-account",
		"--email", "cli-govcloud@sim.invalid", "--account-name", "CLIGovCloud", "--output", "json"))
	var resp struct {
		CreateAccountStatus struct {
			Id                string `json:"Id"`
			State             string `json:"State"`
			AccountId         string `json:"AccountId"`
			GovCloudAccountId string `json:"GovCloudAccountId"`
		} `json:"CreateAccountStatus"`
	}
	parseJSON(t, out, &resp)
	st := resp.CreateAccountStatus
	if st.State != "SUCCEEDED" {
		t.Fatalf("create-gov-cloud-account State = %q, want SUCCEEDED: %s", st.State, out)
	}
	if st.AccountId == "" || st.GovCloudAccountId == "" {
		t.Fatalf("create-gov-cloud-account missing AccountId/GovCloudAccountId: %s", out)
	}
	if st.AccountId == st.GovCloudAccountId {
		t.Fatalf("commercial and GovCloud account ids must differ: %s", out)
	}

	desc := runCLI(t, awsCLI("organizations", "describe-create-account-status",
		"--create-account-request-id", st.Id, "--output", "json"))
	if !strings.Contains(desc, st.AccountId) {
		t.Fatalf("describe-create-account-status missing AccountId %q: %s", st.AccountId, desc)
	}
}

// TestOrganizationsCLI_LeaveOrganization asserts the management account cannot
// leave its own organization — leave-organization fails with
// MasterCannotLeaveOrganizationException for the sim's caller.
func TestOrganizationsCLI_LeaveOrganization(t *testing.T) {
	out := runCLIExpectError(t, awsCLI("organizations", "leave-organization", "--output", "json"))
	if !strings.Contains(out, "MasterCannotLeaveOrganization") {
		t.Fatalf("leave-organization error = %q, want MasterCannotLeaveOrganizationException", out)
	}
}

// The remaining new Organizations operations — list-accounts-with-invalid-effective-policy,
// list-effective-policy-validation-errors, and the responsibility-transfer family
// (invite/describe/update/terminate + inbound/outbound lists) — are not present in
// aws CLI 2.26.6 (the pinned local CLI), so they are covered by the SDK tests in
// sdk-tests/organizations_extra_test.go, which exercise the same handlers and the
// contract hook. They gain CLI coverage automatically once the CLI ships them.
