package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestELBv2ListenerRuleFidelityCLI covers the authenticate-oidc default action,
// the weighted forward rule, SetRulePriorities, and the listener-certificate
// lifecycle via the aws CLI.
func TestELBv2ListenerRuleFidelityCLI(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.162.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.162.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")
	tg1 := q("elbv2", "create-target-group", "--name", "cli-lr-tg1", "--protocol", "HTTP", "--port", "80",
		"--vpc-id", vpcID, "--target-type", "ip", "--query", "TargetGroups[0].TargetGroupArn", "--output", "text")
	tg2 := q("elbv2", "create-target-group", "--name", "cli-lr-tg2", "--protocol", "HTTP", "--port", "80",
		"--vpc-id", vpcID, "--target-type", "ip", "--query", "TargetGroups[0].TargetGroupArn", "--output", "text")
	lbArn := q("elbv2", "create-load-balancer", "--name", "cli-lr-alb", "--type", "application",
		"--subnets", subnetID, "--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")
	defCert := importELBv2CertificateCLI(t, "cli-default.example.test")
	sniCert := importELBv2CertificateCLI(t, "cli-sni.example.test")
	port := availableELBv2ListenerPortCLI(t)

	defActions, _ := json.Marshal([]map[string]any{
		{"Type": "authenticate-oidc", "Order": 1, "AuthenticateOidcConfig": map[string]any{
			"Issuer":                "https://idp.example.test",
			"AuthorizationEndpoint": "https://idp.example.test/authorize",
			"TokenEndpoint":         "https://idp.example.test/token",
			"UserInfoEndpoint":      "https://idp.example.test/userinfo",
			"ClientId":              "cli-client",
			"ClientSecret":          "cli-secret",
			"Scope":                 "openid email",
		}},
		{"Type": "forward", "Order": 2, "TargetGroupArn": tg1},
	})
	listenerArn := q("elbv2", "create-listener", "--load-balancer-arn", lbArn, "--protocol", "HTTPS",
		"--port", port, "--certificates", "CertificateArn="+defCert, "--ssl-policy", "ELBSecurityPolicy-2016-08",
		"--default-actions", string(defActions), "--query", "Listeners[0].ListenerArn", "--output", "text")

	// authenticate-oidc round-trips; ClientSecret is never echoed.
	out := q("elbv2", "describe-listeners", "--listener-arns", listenerArn,
		"--query", "Listeners[0].DefaultActions[0].AuthenticateOidcConfig.[Issuer,UserInfoEndpoint,ClientId]", "--output", "text")
	if f := strings.Fields(out); len(f) != 3 || f[0] != "https://idp.example.test" || f[1] != "https://idp.example.test/userinfo" || f[2] != "cli-client" {
		t.Fatalf("authenticate-oidc round-trip: got %q", out)
	}
	if sec := q("elbv2", "describe-listeners", "--listener-arns", listenerArn,
		"--query", "Listeners[0].DefaultActions[0].AuthenticateOidcConfig.ClientSecret", "--output", "text"); sec != "None" && sec != "" {
		t.Fatalf("ClientSecret must NOT be echoed, got %q", sec)
	}

	// Weighted forward rule.
	conditions := `[{"Field":"path-pattern","PathPatternConfig":{"Values":["/api/*"]}}]`
	actions := fmt.Sprintf(`[{"Type":"forward","ForwardConfig":{"TargetGroups":[{"TargetGroupArn":%q,"Weight":70},{"TargetGroupArn":%q,"Weight":30}],"TargetGroupStickinessConfig":{"Enabled":true,"DurationSeconds":600}}}]`, tg1, tg2)
	ruleArn := q("elbv2", "create-rule", "--listener-arn", listenerArn, "--priority", "10",
		"--conditions", conditions, "--actions", actions, "--query", "Rules[0].RuleArn", "--output", "text")
	weights := q("elbv2", "describe-rules", "--rule-arns", ruleArn,
		"--query", "Rules[0].Actions[0].ForwardConfig.TargetGroups[].Weight", "--output", "text")
	if f := strings.Fields(weights); len(f) != 2 || f[0] != "70" || f[1] != "30" {
		t.Fatalf("forward weights round-trip: got %q", weights)
	}

	// SetRulePriorities.
	runCLI(t, awsCLI("elbv2", "set-rule-priorities", "--rule-priorities",
		fmt.Sprintf("RuleArn=%s,Priority=20", ruleArn)))
	if p := q("elbv2", "describe-rules", "--rule-arns", ruleArn,
		"--query", "Rules[0].Priority", "--output", "text"); p != "20" {
		t.Fatalf("set-rule-priorities: got %q, want 20", p)
	}

	// Listener-certificate lifecycle.
	runCLI(t, awsCLI("elbv2", "add-listener-certificates", "--listener-arn", listenerArn,
		"--certificates", "CertificateArn="+sniCert))
	sniSeen := q("elbv2", "describe-listener-certificates", "--listener-arn", listenerArn,
		"--query", "Certificates[?IsDefault==`false`].CertificateArn", "--output", "text")
	if !strings.Contains(sniSeen, sniCert) {
		t.Fatalf("SNI cert not listed after add: got %q", sniSeen)
	}
	runCLI(t, awsCLI("elbv2", "remove-listener-certificates", "--listener-arn", listenerArn,
		"--certificates", "CertificateArn="+sniCert))
	after := q("elbv2", "describe-listener-certificates", "--listener-arn", listenerArn,
		"--query", "Certificates[?IsDefault==`false`].CertificateArn", "--output", "text")
	if strings.Contains(after, sniCert) {
		t.Fatalf("SNI cert still listed after remove: got %q", after)
	}
}
