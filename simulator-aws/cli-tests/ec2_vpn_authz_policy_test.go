package aws_cli_test

import (
	"strings"
	"testing"
	"time"
)

// TestEC2CLI_ClientVpnEndpointAuthorizationPolicy drives a Client VPN
// endpoint's Cedar authorization policy through the aws CLI: modify creates it,
// a second modify merges a change into it, and delete removes it.
func TestEC2CLI_ClientVpnEndpointAuthorizationPolicy(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	ep := q("ec2", "create-client-vpn-endpoint",
		"--client-cidr-block", "10.231.0.0/22",
		"--server-certificate-arn", "arn:aws:acm:us-east-1:123456789012:certificate/srv",
		"--authentication-options", "Type=certificate-authentication,MutualAuthentication={ClientRootCertificateChainArn=arn:aws:acm:us-east-1:123456789012:certificate/root}",
		"--connection-log-options", "Enabled=false",
		"--query", "ClientVpnEndpointId", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("ec2", "delete-client-vpn-endpoint", "--client-vpn-endpoint-id", ep).Run() })

	const policy = `permit (principal, action, resource) when { context.posture.compliant == true };`
	if out := runCLIExpectError(t, awsCLI("ec2", "modify-client-vpn-endpoint-authorization-policy",
		"--client-vpn-endpoint-id", ep, "--policy-document", policy, "--dry-run")); !strings.Contains(out, "DryRunOperation") {
		t.Fatalf("--dry-run did not answer DryRunOperation: %s", out)
	}
	if status := q("ec2", "modify-client-vpn-endpoint-authorization-policy", "--client-vpn-endpoint-id", ep,
		"--policy-document", policy, "--description", "posture",
		"--query", "Status", "--output", "text"); status != "creating" {
		t.Fatalf("creating modify answered status %q, want creating", status)
	}
	waitForClientVpnAuthorizationPolicyCLI(t, ep, "active")
	if status := q("ec2", "modify-client-vpn-endpoint-authorization-policy", "--client-vpn-endpoint-id", ep,
		"--shadow-mode", "enabled", "--query", "Status", "--output", "text"); status != "updating" {
		t.Fatalf("updating modify answered status %q, want updating", status)
	}
	waitForClientVpnAuthorizationPolicyCLI(t, ep, "active")
	got := q("ec2", "get-client-vpn-endpoint-authorization-policy", "--client-vpn-endpoint-id", ep,
		"--query", "[PolicyDocument,Description,ShadowMode]", "--output", "text")
	if got != policy+"\tposture\tenabled" {
		t.Fatalf("get-client-vpn-endpoint-authorization-policy: %q", got)
	}

	if status := q("ec2", "delete-client-vpn-endpoint-authorization-policy", "--client-vpn-endpoint-id", ep,
		"--query", "Status", "--output", "text"); status != "deleting" {
		t.Fatalf("delete answered status %q, want deleting", status)
	}
	waitForClientVpnAuthorizationPolicyCLI(t, ep, "None")
}

// waitForClientVpnAuthorizationPolicyCLI polls
// get-client-vpn-endpoint-authorization-policy until the policy's status is
// want ("None" once it is gone): the CLI has no waiter for the policy.
func waitForClientVpnAuthorizationPolicyCLI(t *testing.T, ep, want string) {
	t.Helper()
	delay := 250 * time.Millisecond
	deadline := time.Now().Add(time.Minute)
	observed := ""
	for time.Now().Before(deadline) {
		observed = strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-client-vpn-endpoint-authorization-policy",
			"--client-vpn-endpoint-id", ep, "--query", "Status", "--output", "text")))
		if observed == want {
			return
		}
		time.Sleep(delay)
		delay = min(delay*2, 2*time.Second)
	}
	t.Fatalf("authorization policy of %s is %q, want %q", ep, observed, want)
}
