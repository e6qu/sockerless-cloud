package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_VerifiedAccessLifecycle drives the Amazon EC2 Verified Access
// control plane through the aws CLI: an instance, a standalone trust provider
// that attaches/detaches, a group with a Cedar policy document, and an endpoint
// with its own policy document, with read-backs of each resource.
func TestEC2CLI_VerifiedAccessLifecycle(t *testing.T) {
	instID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-verified-access-instance",
		"--description", "cli-vai",
		"--tag-specifications", "ResourceType=verified-access-instance,Tags=[{Key=Name,Value=cli-vai}]",
		"--query", "VerifiedAccessInstance.VerifiedAccessInstanceId", "--output", "text")))
	if instID == "" {
		t.Fatal("no instance id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-verified-access-instance", "--verified-access-instance-id", instID).Run()
	}()

	out := runCLI(t, awsCLI("ec2", "describe-verified-access-instances", "--verified-access-instance-ids", instID,
		"--query", "VerifiedAccessInstances[0].Description", "--output", "text"))
	if strings.TrimSpace(out) != "cli-vai" {
		t.Fatalf("describe-verified-access-instances: got %q", strings.TrimSpace(out))
	}

	runCLI(t, awsCLI("ec2", "modify-verified-access-instance", "--verified-access-instance-id", instID,
		"--description", "cli-vai-updated"))

	// ---- Trust provider ----
	tpID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-verified-access-trust-provider",
		"--trust-provider-type", "user",
		"--user-trust-provider-type", "iam-identity-center",
		"--policy-reference-name", "idc",
		"--description", "cli-tp",
		"--query", "VerifiedAccessTrustProvider.VerifiedAccessTrustProviderId", "--output", "text")))
	if tpID == "" {
		t.Fatal("no trust provider id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-verified-access-trust-provider", "--verified-access-trust-provider-id", tpID).Run()
	}()

	out = runCLI(t, awsCLI("ec2", "attach-verified-access-trust-provider",
		"--verified-access-instance-id", instID,
		"--verified-access-trust-provider-id", tpID,
		"--query", "VerifiedAccessInstance.VerifiedAccessTrustProviders[0].VerifiedAccessTrustProviderId", "--output", "text"))
	if strings.TrimSpace(out) != tpID {
		t.Fatalf("attach-verified-access-trust-provider: got %q", strings.TrimSpace(out))
	}

	out = runCLI(t, awsCLI("ec2", "describe-verified-access-trust-providers", "--verified-access-trust-provider-ids", tpID,
		"--query", "VerifiedAccessTrustProviders[0].PolicyReferenceName", "--output", "text"))
	if strings.TrimSpace(out) != "idc" {
		t.Fatalf("describe-verified-access-trust-providers: got %q", strings.TrimSpace(out))
	}

	runCLI(t, awsCLI("ec2", "modify-verified-access-trust-provider", "--verified-access-trust-provider-id", tpID,
		"--description", "cli-tp-updated"))
	runCLI(t, awsCLI("ec2", "detach-verified-access-trust-provider",
		"--verified-access-instance-id", instID, "--verified-access-trust-provider-id", tpID))

	// ---- Group ----
	const groupPolicy = "permit(principal, action, resource);"
	grpID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-verified-access-group",
		"--verified-access-instance-id", instID,
		"--description", "cli-grp",
		"--policy-document", groupPolicy,
		"--query", "VerifiedAccessGroup.VerifiedAccessGroupId", "--output", "text")))
	if grpID == "" {
		t.Fatal("no group id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-verified-access-group", "--verified-access-group-id", grpID).Run()
	}()

	out = runCLI(t, awsCLI("ec2", "describe-verified-access-groups", "--verified-access-group-ids", grpID,
		"--query", "VerifiedAccessGroups[0].Description", "--output", "text"))
	if strings.TrimSpace(out) != "cli-grp" {
		t.Fatalf("describe-verified-access-groups: got %q", strings.TrimSpace(out))
	}

	out = runCLI(t, awsCLI("ec2", "get-verified-access-group-policy", "--verified-access-group-id", grpID,
		"--query", "[PolicyEnabled,PolicyDocument]", "--output", "text"))
	if f := strings.Fields(out); len(f) < 2 || f[0] != "True" {
		t.Fatalf("get-verified-access-group-policy: got %q", strings.TrimSpace(out))
	}

	runCLI(t, awsCLI("ec2", "modify-verified-access-group-policy", "--verified-access-group-id", grpID,
		"--policy-enabled", "--policy-document", "forbid(principal, action, resource);"))
	runCLI(t, awsCLI("ec2", "modify-verified-access-group", "--verified-access-group-id", grpID,
		"--description", "cli-grp-updated"))

	// ---- Endpoint ----
	epID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-verified-access-endpoint",
		"--verified-access-group-id", grpID,
		"--endpoint-type", "load-balancer",
		"--attachment-type", "vpc",
		"--application-domain", "app.example.com",
		"--endpoint-domain-prefix", "my-app",
		"--domain-certificate-arn", "arn:aws:acm:us-east-1:000000000000:certificate/abc",
		"--security-group-ids", "sg-1234567890abcdef0",
		"--description", "cli-ep",
		"--policy-document", "permit(principal, action, resource);",
		"--query", "VerifiedAccessEndpoint.VerifiedAccessEndpointId", "--output", "text")))
	if epID == "" {
		t.Fatal("no endpoint id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-verified-access-endpoint", "--verified-access-endpoint-id", epID).Run()
	}()

	out = runCLI(t, awsCLI("ec2", "describe-verified-access-endpoints", "--verified-access-endpoint-ids", epID,
		"--query", "VerifiedAccessEndpoints[0].EndpointDomain", "--output", "text"))
	if strings.TrimSpace(out) != "my-app.app.example.com" {
		t.Fatalf("describe-verified-access-endpoints: got %q", strings.TrimSpace(out))
	}

	out = runCLI(t, awsCLI("ec2", "get-verified-access-endpoint-policy", "--verified-access-endpoint-id", epID,
		"--query", "PolicyEnabled", "--output", "text"))
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("get-verified-access-endpoint-policy: got %q", strings.TrimSpace(out))
	}

	runCLI(t, awsCLI("ec2", "modify-verified-access-endpoint-policy", "--verified-access-endpoint-id", epID,
		"--policy-enabled", "--policy-document", "forbid(principal, action, resource);"))
	runCLI(t, awsCLI("ec2", "modify-verified-access-endpoint", "--verified-access-endpoint-id", epID,
		"--description", "cli-ep-updated"))

	out = runCLI(t, awsCLI("ec2", "get-verified-access-endpoint-targets", "--verified-access-endpoint-id", epID,
		"--query", "VerifiedAccessEndpointTargets[0].VerifiedAccessEndpointId", "--output", "text"))
	if strings.TrimSpace(out) != epID {
		t.Fatalf("get-verified-access-endpoint-targets: got %q", strings.TrimSpace(out))
	}
}

// TestEC2CLI_VerifiedAccessLogging covers the per-instance access-log
// configuration and the client-config export command.
func TestEC2CLI_VerifiedAccessLogging(t *testing.T) {
	instID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-verified-access-instance",
		"--description", "cli-vai-logging",
		"--query", "VerifiedAccessInstance.VerifiedAccessInstanceId", "--output", "text")))
	if instID == "" {
		t.Fatal("no instance id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-verified-access-instance", "--verified-access-instance-id", instID).Run()
	}()

	out := runCLI(t, awsCLI("ec2", "modify-verified-access-instance-logging-configuration",
		"--verified-access-instance-id", instID,
		"--access-logs", "S3={Enabled=true,BucketName=va-logs,Prefix=logs/},IncludeTrustContext=true",
		"--query", "LoggingConfiguration.AccessLogs.S3.[Enabled,BucketName]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != "True" || f[1] != "va-logs" {
		t.Fatalf("modify-logging: got %q", strings.TrimSpace(out))
	}

	out = runCLI(t, awsCLI("ec2", "describe-verified-access-instance-logging-configurations",
		"--verified-access-instance-ids", instID,
		"--query", "LoggingConfigurations[0].AccessLogs.S3.Enabled", "--output", "text"))
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("describe-logging: got %q", strings.TrimSpace(out))
	}

	out = runCLI(t, awsCLI("ec2", "export-verified-access-instance-client-configuration",
		"--verified-access-instance-id", instID,
		"--query", "VerifiedAccessInstanceId", "--output", "text"))
	if strings.TrimSpace(out) != instID {
		t.Fatalf("export-client-config: got %q", strings.TrimSpace(out))
	}
}

// TestEC2CLI_TrafficMirrorLifecycle drives EC2 Traffic Mirroring through the
// aws CLI: a target, a filter with an ingress rule + mirrored network services,
// and a session binding a source ENI to the target through the filter.
func TestEC2CLI_TrafficMirrorLifecycle(t *testing.T) {
	tgtID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-traffic-mirror-target",
		"--network-interface-id", "eni-1234567890abcdef0",
		"--description", "cli-tmt",
		"--tag-specifications", "ResourceType=traffic-mirror-target,Tags=[{Key=Name,Value=cli-tmt}]",
		"--query", "TrafficMirrorTarget.TrafficMirrorTargetId", "--output", "text")))
	if tgtID == "" {
		t.Fatal("no target id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-traffic-mirror-target", "--traffic-mirror-target-id", tgtID).Run()
	}()

	out := runCLI(t, awsCLI("ec2", "describe-traffic-mirror-targets", "--traffic-mirror-target-ids", tgtID,
		"--query", "TrafficMirrorTargets[0].[Type,NetworkInterfaceId]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != "network-interface" || f[1] != "eni-1234567890abcdef0" {
		t.Fatalf("describe-traffic-mirror-targets: got %q", strings.TrimSpace(out))
	}

	// ---- Filter + rule + network services ----
	fltID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-traffic-mirror-filter",
		"--description", "cli-tmf",
		"--query", "TrafficMirrorFilter.TrafficMirrorFilterId", "--output", "text")))
	if fltID == "" {
		t.Fatal("no filter id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-traffic-mirror-filter", "--traffic-mirror-filter-id", fltID).Run()
	}()

	ruleID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-traffic-mirror-filter-rule",
		"--traffic-mirror-filter-id", fltID,
		"--traffic-direction", "ingress",
		"--rule-number", "100",
		"--rule-action", "accept",
		"--protocol", "6",
		"--destination-cidr-block", "10.0.0.0/24",
		"--source-cidr-block", "0.0.0.0/0",
		"--destination-port-range", "FromPort=80,ToPort=80",
		"--description", "cli-rule",
		"--query", "TrafficMirrorFilterRule.TrafficMirrorFilterRuleId", "--output", "text")))
	if ruleID == "" {
		t.Fatal("no rule id")
	}

	runCLI(t, awsCLI("ec2", "modify-traffic-mirror-filter-rule", "--traffic-mirror-filter-rule-id", ruleID,
		"--rule-number", "200", "--description", "cli-rule-updated"))

	out = runCLI(t, awsCLI("ec2", "describe-traffic-mirror-filter-rules", "--traffic-mirror-filter-id", fltID,
		"--query", "TrafficMirrorFilterRules[0].RuleNumber", "--output", "text"))
	if strings.TrimSpace(out) != "200" {
		t.Fatalf("describe-traffic-mirror-filter-rules: got %q", strings.TrimSpace(out))
	}

	out = runCLI(t, awsCLI("ec2", "modify-traffic-mirror-filter-network-services",
		"--traffic-mirror-filter-id", fltID,
		"--add-network-services", "amazon-dns",
		"--query", "TrafficMirrorFilter.NetworkServices[0]", "--output", "text"))
	if strings.TrimSpace(out) != "amazon-dns" {
		t.Fatalf("modify-network-services: got %q", strings.TrimSpace(out))
	}

	out = runCLI(t, awsCLI("ec2", "describe-traffic-mirror-filters", "--traffic-mirror-filter-ids", fltID,
		"--query", "TrafficMirrorFilters[0].IngressFilterRules[0].RuleAction", "--output", "text"))
	if strings.TrimSpace(out) != "accept" {
		t.Fatalf("describe-traffic-mirror-filters: got %q", strings.TrimSpace(out))
	}

	// ---- Session ----
	sessID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-traffic-mirror-session",
		"--traffic-mirror-target-id", tgtID,
		"--traffic-mirror-filter-id", fltID,
		"--network-interface-id", "eni-aaaaaaaaaaaaaaaaa",
		"--session-number", "1",
		"--packet-length", "8500",
		"--virtual-network-id", "42",
		"--description", "cli-tms",
		"--query", "TrafficMirrorSession.TrafficMirrorSessionId", "--output", "text")))
	if sessID == "" {
		t.Fatal("no session id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-traffic-mirror-session", "--traffic-mirror-session-id", sessID).Run()
	}()

	out = runCLI(t, awsCLI("ec2", "describe-traffic-mirror-sessions", "--traffic-mirror-session-ids", sessID,
		"--query", "TrafficMirrorSessions[0].[TrafficMirrorTargetId,VirtualNetworkId]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != tgtID || f[1] != "42" {
		t.Fatalf("describe-traffic-mirror-sessions: got %q", strings.TrimSpace(out))
	}

	runCLI(t, awsCLI("ec2", "modify-traffic-mirror-session", "--traffic-mirror-session-id", sessID,
		"--session-number", "2", "--description", "cli-tms-updated"))

	runCLI(t, awsCLI("ec2", "delete-traffic-mirror-filter-rule", "--traffic-mirror-filter-rule-id", ruleID))
}
