package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"
)

// runCLIIgnore runs a cleanup CLI command and ignores any error — used in
// deferred teardown where the resource may already be gone.
func runCLIIgnore(cmd *exec.Cmd) {
	_ = cmd.Run()
}

// TestOrganizationsCLI_DescribeAndList exercises the Organizations slice over the
// aws CLI (describe-organization / list-accounts / list-roots).
func TestOrganizationsCLI_DescribeAndList(t *testing.T) {
	out := runCLI(t, awsCLI("organizations", "describe-organization", "--output", "json"))
	var desc struct {
		Organization struct {
			Id              string `json:"Id"`
			FeatureSet      string `json:"FeatureSet"`
			MasterAccountId string `json:"MasterAccountId"`
		} `json:"Organization"`
	}
	parseJSON(t, out, &desc)
	if !strings.HasPrefix(desc.Organization.Id, "o-") {
		t.Fatalf("describe-organization Id = %q, want o-…", desc.Organization.Id)
	}

	la := runCLI(t, awsCLI("organizations", "list-accounts", "--output", "json"))
	if !strings.Contains(la, desc.Organization.MasterAccountId) {
		t.Fatalf("list-accounts missing the management account %q: %s", desc.Organization.MasterAccountId, la)
	}
}

func orgCLIRootID(t *testing.T) string {
	t.Helper()
	out := runCLI(t, awsCLI("organizations", "list-roots", "--output", "json"))
	var roots struct {
		Roots []struct {
			Id string `json:"Id"`
		} `json:"Roots"`
	}
	parseJSON(t, out, &roots)
	if len(roots.Roots) == 0 {
		t.Fatalf("list-roots returned no roots: %s", out)
	}
	return roots.Roots[0].Id
}

// TestOrganizationsCLI_OUTreeAndAccounts covers create/describe/update/list/delete
// OUs, create-account + status, describe-account, list-accounts-for-parent,
// list-children, list-parents, move-account, close-account,
// remove-account-from-organization.
func TestOrganizationsCLI_OUTreeAndAccounts(t *testing.T) {
	root := orgCLIRootID(t)

	out := runCLI(t, awsCLI("organizations", "create-organizational-unit", "--parent-id", root, "--name", "CLIEng", "--output", "json"))
	var ou struct {
		OrganizationalUnit struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"OrganizationalUnit"`
	}
	parseJSON(t, out, &ou)
	ouID := ou.OrganizationalUnit.Id
	if !strings.HasPrefix(ouID, "ou-") {
		t.Fatalf("create-organizational-unit Id = %q, want ou-…", ouID)
	}
	defer runCLIIgnore(awsCLI("organizations", "delete-organizational-unit", "--organizational-unit-id", ouID))

	dOut := runCLI(t, awsCLI("organizations", "describe-organizational-unit", "--organizational-unit-id", ouID, "--output", "json"))
	if !strings.Contains(dOut, "CLIEng") {
		t.Fatalf("describe-organizational-unit missing name: %s", dOut)
	}

	uOut := runCLI(t, awsCLI("organizations", "update-organizational-unit", "--organizational-unit-id", ouID, "--name", "CLIPlat", "--output", "json"))
	if !strings.Contains(uOut, "CLIPlat") {
		t.Fatalf("update-organizational-unit didn't rename: %s", uOut)
	}

	lOut := runCLI(t, awsCLI("organizations", "list-organizational-units-for-parent", "--parent-id", root, "--output", "json"))
	if !strings.Contains(lOut, ouID) {
		t.Fatalf("list-organizational-units-for-parent missing %q: %s", ouID, lOut)
	}

	caOut := runCLI(t, awsCLI("organizations", "create-account", "--account-name", "CLISandbox", "--email", "cli-sandbox@sim.invalid", "--output", "json"))
	var ca struct {
		CreateAccountStatus struct {
			Id        string `json:"Id"`
			State     string `json:"State"`
			AccountId string `json:"AccountId"`
		} `json:"CreateAccountStatus"`
	}
	parseJSON(t, caOut, &ca)
	acctID := ca.CreateAccountStatus.AccountId
	if acctID == "" || ca.CreateAccountStatus.State != "SUCCEEDED" {
		t.Fatalf("create-account unexpected status: %s", caOut)
	}
	defer runCLIIgnore(awsCLI("organizations", "remove-account-from-organization", "--account-id", acctID))

	stOut := runCLI(t, awsCLI("organizations", "describe-create-account-status", "--create-account-request-id", ca.CreateAccountStatus.Id, "--output", "json"))
	if !strings.Contains(stOut, "SUCCEEDED") {
		t.Fatalf("describe-create-account-status not SUCCEEDED: %s", stOut)
	}

	lcsOut := runCLI(t, awsCLI("organizations", "list-create-account-status", "--output", "json"))
	if !strings.Contains(lcsOut, ca.CreateAccountStatus.Id) {
		t.Fatalf("list-create-account-status missing the request: %s", lcsOut)
	}

	daOut := runCLI(t, awsCLI("organizations", "describe-account", "--account-id", acctID, "--output", "json"))
	if !strings.Contains(daOut, "CLISandbox") {
		t.Fatalf("describe-account missing name: %s", daOut)
	}

	runCLI(t, awsCLI("organizations", "move-account", "--account-id", acctID, "--source-parent-id", root, "--destination-parent-id", ouID, "--output", "json"))

	fpOut := runCLI(t, awsCLI("organizations", "list-accounts-for-parent", "--parent-id", ouID, "--output", "json"))
	if !strings.Contains(fpOut, acctID) {
		t.Fatalf("list-accounts-for-parent missing moved account: %s", fpOut)
	}

	chOut := runCLI(t, awsCLI("organizations", "list-children", "--parent-id", ouID, "--child-type", "ACCOUNT", "--output", "json"))
	if !strings.Contains(chOut, acctID) {
		t.Fatalf("list-children missing the account: %s", chOut)
	}

	lpOut := runCLI(t, awsCLI("organizations", "list-parents", "--child-id", acctID, "--output", "json"))
	if !strings.Contains(lpOut, ouID) {
		t.Fatalf("list-parents missing the OU parent: %s", lpOut)
	}

	runCLI(t, awsCLI("organizations", "close-account", "--account-id", acctID, "--output", "json"))
	runCLI(t, awsCLI("organizations", "move-account", "--account-id", acctID, "--source-parent-id", ouID, "--destination-parent-id", root, "--output", "json"))
	runCLI(t, awsCLI("organizations", "remove-account-from-organization", "--account-id", acctID, "--output", "json"))
}

// TestOrganizationsCLI_Policies covers create/describe/update/list policy,
// attach/detach, list-policies-for-target, list-targets-for-policy,
// enable/disable-policy-type, describe-effective-policy, delete-policy.
func TestOrganizationsCLI_Policies(t *testing.T) {
	root := orgCLIRootID(t)
	content := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"ec2:*","Resource":"*"}]}`

	cpOut := runCLI(t, awsCLI("organizations", "create-policy", "--name", "CLIDenyEC2", "--description", "deny ec2", "--type", "SERVICE_CONTROL_POLICY", "--content", content, "--output", "json"))
	var cp struct {
		Policy struct {
			PolicySummary struct {
				Id string `json:"Id"`
			} `json:"PolicySummary"`
		} `json:"Policy"`
	}
	parseJSON(t, cpOut, &cp)
	pid := cp.Policy.PolicySummary.Id
	if !strings.HasPrefix(pid, "p-") {
		t.Fatalf("create-policy Id = %q, want p-…", pid)
	}

	dpOut := runCLI(t, awsCLI("organizations", "describe-policy", "--policy-id", pid, "--output", "json"))
	if !strings.Contains(dpOut, "ec2:*") {
		t.Fatalf("describe-policy missing content: %s", dpOut)
	}

	upOut := runCLI(t, awsCLI("organizations", "update-policy", "--policy-id", pid, "--description", "deny ec2 v2", "--output", "json"))
	if !strings.Contains(upOut, "deny ec2 v2") {
		t.Fatalf("update-policy didn't change description: %s", upOut)
	}

	lpOut := runCLI(t, awsCLI("organizations", "list-policies", "--filter", "SERVICE_CONTROL_POLICY", "--output", "json"))
	if !strings.Contains(lpOut, pid) {
		t.Fatalf("list-policies missing the policy: %s", lpOut)
	}

	runCLI(t, awsCLI("organizations", "attach-policy", "--policy-id", pid, "--target-id", root))

	pftOut := runCLI(t, awsCLI("organizations", "list-policies-for-target", "--target-id", root, "--filter", "SERVICE_CONTROL_POLICY", "--output", "json"))
	if !strings.Contains(pftOut, pid) {
		t.Fatalf("list-policies-for-target missing attached policy: %s", pftOut)
	}

	tfpOut := runCLI(t, awsCLI("organizations", "list-targets-for-policy", "--policy-id", pid, "--output", "json"))
	if !strings.Contains(tfpOut, root) {
		t.Fatalf("list-targets-for-policy missing root target: %s", tfpOut)
	}

	// Effective SCP at the root.
	runCLI(t, awsCLI("organizations", "describe-effective-policy", "--policy-type", "SERVICE_CONTROL_POLICY", "--target-id", root, "--output", "json"))

	runCLI(t, awsCLI("organizations", "detach-policy", "--policy-id", pid, "--target-id", root))
	runCLI(t, awsCLI("organizations", "delete-policy", "--policy-id", pid))

	epOut := runCLI(t, awsCLI("organizations", "enable-policy-type", "--root-id", root, "--policy-type", "TAG_POLICY", "--output", "json"))
	if !strings.Contains(epOut, "TAG_POLICY") {
		t.Fatalf("enable-policy-type didn't reflect TAG_POLICY: %s", epOut)
	}
	runCLI(t, awsCLI("organizations", "disable-policy-type", "--root-id", root, "--policy-type", "TAG_POLICY", "--output", "json"))
}

// TestOrganizationsCLI_Handshakes covers invite/describe/list/accept/decline/cancel.
func TestOrganizationsCLI_Handshakes(t *testing.T) {
	invOut := runCLI(t, awsCLI("organizations", "invite-account-to-organization", "--target", "Id=cli-invitee@sim.invalid,Type=EMAIL", "--output", "json"))
	var inv struct {
		Handshake struct {
			Id    string `json:"Id"`
			State string `json:"State"`
		} `json:"Handshake"`
	}
	parseJSON(t, invOut, &inv)
	hid := inv.Handshake.Id
	if !strings.HasPrefix(hid, "h-") {
		t.Fatalf("invite-account-to-organization Id = %q, want h-…", hid)
	}

	dhOut := runCLI(t, awsCLI("organizations", "describe-handshake", "--handshake-id", hid, "--output", "json"))
	if !strings.Contains(dhOut, hid) {
		t.Fatalf("describe-handshake missing id: %s", dhOut)
	}

	if foOut := runCLI(t, awsCLI("organizations", "list-handshakes-for-organization", "--output", "json")); !strings.Contains(foOut, hid) {
		t.Fatalf("list-handshakes-for-organization missing handshake: %s", foOut)
	}
	if faOut := runCLI(t, awsCLI("organizations", "list-handshakes-for-account", "--output", "json")); !strings.Contains(faOut, hid) {
		t.Fatalf("list-handshakes-for-account missing handshake: %s", faOut)
	}

	accOut := runCLI(t, awsCLI("organizations", "accept-handshake", "--handshake-id", hid, "--output", "json"))
	if !strings.Contains(accOut, "ACCEPTED") {
		t.Fatalf("accept-handshake not ACCEPTED: %s", accOut)
	}

	inv2 := runCLI(t, awsCLI("organizations", "invite-account-to-organization", "--target", "Id=111122223333,Type=ACCOUNT", "--output", "json"))
	var h2 struct {
		Handshake struct{ Id string } `json:"Handshake"`
	}
	parseJSON(t, inv2, &h2)
	if decOut := runCLI(t, awsCLI("organizations", "decline-handshake", "--handshake-id", h2.Handshake.Id, "--output", "json")); !strings.Contains(decOut, "DECLINED") {
		t.Fatalf("decline-handshake not DECLINED: %s", decOut)
	}

	inv3 := runCLI(t, awsCLI("organizations", "invite-account-to-organization", "--target", "Id=444455556666,Type=ACCOUNT", "--output", "json"))
	var h3 struct {
		Handshake struct{ Id string } `json:"Handshake"`
	}
	parseJSON(t, inv3, &h3)
	if canOut := runCLI(t, awsCLI("organizations", "cancel-handshake", "--handshake-id", h3.Handshake.Id, "--output", "json")); !strings.Contains(canOut, "CANCELED") {
		t.Fatalf("cancel-handshake not CANCELED: %s", canOut)
	}
}

// TestOrganizationsCLI_DelegatedAdminAndServiceAccess covers
// enable/disable/list AWS service access and register/deregister/list delegated
// administrators + list-delegated-services-for-account.
func TestOrganizationsCLI_DelegatedAdminAndServiceAccess(t *testing.T) {
	sp := "guardduty.amazonaws.com"

	caOut := runCLI(t, awsCLI("organizations", "create-account", "--account-name", "CLIAudit", "--email", "cli-audit@sim.invalid", "--output", "json"))
	var ca struct {
		CreateAccountStatus struct {
			AccountId string `json:"AccountId"`
		} `json:"CreateAccountStatus"`
	}
	parseJSON(t, caOut, &ca)
	acctID := ca.CreateAccountStatus.AccountId
	defer runCLIIgnore(awsCLI("organizations", "remove-account-from-organization", "--account-id", acctID))

	runCLI(t, awsCLI("organizations", "enable-aws-service-access", "--service-principal", sp))
	if lOut := runCLI(t, awsCLI("organizations", "list-aws-service-access-for-organization", "--output", "json")); !strings.Contains(lOut, sp) {
		t.Fatalf("list-aws-service-access missing %q: %s", sp, lOut)
	}

	runCLI(t, awsCLI("organizations", "register-delegated-administrator", "--account-id", acctID, "--service-principal", sp))
	if ldaOut := runCLI(t, awsCLI("organizations", "list-delegated-administrators", "--service-principal", sp, "--output", "json")); !strings.Contains(ldaOut, acctID) {
		t.Fatalf("list-delegated-administrators missing %q: %s", acctID, ldaOut)
	}
	if ldsOut := runCLI(t, awsCLI("organizations", "list-delegated-services-for-account", "--account-id", acctID, "--output", "json")); !strings.Contains(ldsOut, sp) {
		t.Fatalf("list-delegated-services-for-account missing %q: %s", sp, ldsOut)
	}

	runCLI(t, awsCLI("organizations", "deregister-delegated-administrator", "--account-id", acctID, "--service-principal", sp))
	runCLI(t, awsCLI("organizations", "disable-aws-service-access", "--service-principal", sp))
}

// TestOrganizationsCLI_ResourcePolicy covers put/describe/delete-resource-policy.
func TestOrganizationsCLI_ResourcePolicy(t *testing.T) {
	content := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"organizations:DescribeOrganization","Resource":"*"}]}`
	putOut := runCLI(t, awsCLI("organizations", "put-resource-policy", "--content", content, "--output", "json"))
	if !strings.Contains(putOut, "rp-") {
		t.Fatalf("put-resource-policy missing resource policy id: %s", putOut)
	}

	if dOut := runCLI(t, awsCLI("organizations", "describe-resource-policy", "--output", "json")); !strings.Contains(dOut, "DescribeOrganization") {
		t.Fatalf("describe-resource-policy missing content: %s", dOut)
	}

	runCLI(t, awsCLI("organizations", "delete-resource-policy"))
	runCLIExpectError(t, awsCLI("organizations", "describe-resource-policy", "--output", "json"))
}

// TestOrganizationsCLI_Tags covers tag-resource, list-tags-for-resource, untag-resource.
func TestOrganizationsCLI_Tags(t *testing.T) {
	cpOut := runCLI(t, awsCLI("organizations", "create-policy", "--name", "CLITagged", "--description", "tagged", "--type", "SERVICE_CONTROL_POLICY",
		"--content", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`, "--output", "json"))
	var cp struct {
		Policy struct {
			PolicySummary struct{ Id string } `json:"PolicySummary"`
		} `json:"Policy"`
	}
	parseJSON(t, cpOut, &cp)
	pid := cp.Policy.PolicySummary.Id
	defer runCLIIgnore(awsCLI("organizations", "delete-policy", "--policy-id", pid))

	runCLI(t, awsCLI("organizations", "tag-resource", "--resource-id", pid, "--tags", "Key=env,Value=test", "Key=team,Value=plat"))
	ltOut := runCLI(t, awsCLI("organizations", "list-tags-for-resource", "--resource-id", pid, "--output", "json"))
	if !strings.Contains(ltOut, "env") || !strings.Contains(ltOut, "team") {
		t.Fatalf("list-tags-for-resource missing tags: %s", ltOut)
	}

	runCLI(t, awsCLI("organizations", "untag-resource", "--resource-id", pid, "--tag-keys", "team"))
	lt2Out := runCLI(t, awsCLI("organizations", "list-tags-for-resource", "--resource-id", pid, "--output", "json"))
	if strings.Contains(lt2Out, "team") {
		t.Fatalf("untag-resource didn't remove team: %s", lt2Out)
	}
}

// TestOrganizationsCLI_EnableAllFeatures covers enable-all-features.
func TestOrganizationsCLI_EnableAllFeatures(t *testing.T) {
	out := runCLI(t, awsCLI("organizations", "enable-all-features", "--output", "json"))
	if !strings.Contains(out, "ENABLE_ALL_FEATURES") {
		t.Fatalf("enable-all-features missing the handshake action: %s", out)
	}
}

// TestOrganizationsCLI_zzCreateDeleteOrganization covers delete-organization and
// create-organization, restoring the default org before returning so reruns and
// other suites still see it. In between it pins the organization-less account's
// contract — the sequence the console's AWS accounts page drives: list-accounts
// answers AWSOrganizationsNotInUseException until create-organization runs, then
// lists the management account again.
func TestOrganizationsCLI_zzCreateDeleteOrganization(t *testing.T) {
	runCLI(t, awsCLI("organizations", "delete-organization"))
	runCLIExpectError(t, awsCLI("organizations", "describe-organization", "--output", "json"))
	notInUse := runCLIExpectError(t, awsCLI("organizations", "list-accounts", "--output", "json"))
	if !strings.Contains(notInUse, "AWSOrganizationsNotInUseException") {
		t.Fatalf("list-accounts without an organization didn't answer AWSOrganizationsNotInUseException: %s", notInUse)
	}

	out := runCLI(t, awsCLI("organizations", "create-organization", "--feature-set", "ALL", "--output", "json"))
	if !strings.Contains(out, "\"FeatureSet\": \"ALL\"") && !strings.Contains(out, "\"FeatureSet\":\"ALL\"") {
		t.Fatalf("create-organization didn't return ALL feature set: %s", out)
	}
	la := runCLI(t, awsCLI("organizations", "list-accounts", "--output", "json"))
	if !strings.Contains(la, "\"Accounts\"") {
		t.Fatalf("list-accounts after create-organization returned no account list: %s", la)
	}
}
