package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_InstanceConnectEndpoint covers the EC2 Instance Connect Endpoint
// control plane via the aws CLI: create-instance-connect-endpoint in a subnet,
// describe-instance-connect-endpoints read-back, modify, and delete.
func TestEC2CLI_InstanceConnectEndpoint(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.182.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.182.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")

	eice := q("ec2", "create-instance-connect-endpoint", "--subnet-id", subnet,
		"--preserve-client-ip",
		"--query", "InstanceConnectEndpoint.InstanceConnectEndpointId", "--output", "text")
	if eice == "" {
		t.Fatal("create-instance-connect-endpoint returned empty id")
	}
	got := q("ec2", "describe-instance-connect-endpoints", "--instance-connect-endpoint-ids", eice,
		"--query", "InstanceConnectEndpoints[0].[State,SubnetId,PreserveClientIp]", "--output", "text")
	if f := strings.Fields(got); len(f) != 3 || f[0] != "create-complete" || f[1] != subnet || f[2] != "True" {
		t.Fatalf("describe-instance-connect-endpoints: got %q, want 'create-complete %s True'", got, subnet)
	}

	// ModifyInstanceConnectEndpoint is exercised via the SDK test; older aws CLI
	// builds lack the subcommand, so it is not invoked here.

	runCLI(t, awsCLI("ec2", "delete-instance-connect-endpoint", "--instance-connect-endpoint-id", eice))
	gone := q("ec2", "describe-instance-connect-endpoints",
		"--query", "length(InstanceConnectEndpoints[?InstanceConnectEndpointId=='"+eice+"'])", "--output", "text")
	if gone != "0" {
		t.Fatalf("deleted endpoint must be gone, got count %q", gone)
	}
}

// TestEC2CLI_SerialConsoleAndMetadataDefaults covers the account-level
// serial-console flag (enable/get/disable) and the IMDS account defaults
// (modify/get) via the aws CLI.
func TestEC2CLI_SerialConsoleAndMetadataDefaults(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	if v := q("ec2", "enable-serial-console-access",
		"--query", "SerialConsoleAccessEnabled", "--output", "text"); v != "True" {
		t.Fatalf("enable-serial-console-access: got %q, want True", v)
	}
	if v := q("ec2", "get-serial-console-access-status",
		"--query", "SerialConsoleAccessEnabled", "--output", "text"); v != "True" {
		t.Fatalf("get-serial-console-access-status after enable: got %q, want True", v)
	}
	if v := q("ec2", "disable-serial-console-access",
		"--query", "SerialConsoleAccessEnabled", "--output", "text"); v != "False" {
		t.Fatalf("disable-serial-console-access: got %q, want False", v)
	}

	runCLI(t, awsCLI("ec2", "modify-instance-metadata-defaults",
		"--http-tokens", "required", "--http-put-response-hop-limit", "2", "--http-endpoint", "enabled"))
	got := q("ec2", "get-instance-metadata-defaults",
		"--query", "AccountLevel.[HttpTokens,HttpPutResponseHopLimit,HttpEndpoint]", "--output", "text")
	if f := strings.Fields(got); len(f) != 3 || f[0] != "required" || f[1] != "2" || f[2] != "enabled" {
		t.Fatalf("get-instance-metadata-defaults: got %q, want 'required 2 enabled'", got)
	}
}

// TestEC2CLI_InstanceEventNotificationAttributes covers the account-level set
// of tag keys registered for instance-event notifications via the aws CLI:
// register, describe, deregister.
func TestEC2CLI_InstanceEventNotificationAttributes(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	runCLI(t, awsCLI("ec2", "register-instance-event-notification-attributes",
		"--instance-tag-attribute", "InstanceTagKeys=Owner,Project"))
	desc := q("ec2", "describe-instance-event-notification-attributes",
		"--query", "InstanceTagAttribute.InstanceTagKeys", "--output", "text")
	if !strings.Contains(desc, "Owner") || !strings.Contains(desc, "Project") {
		t.Fatalf("describe: got %q, want both Owner and Project", desc)
	}

	runCLI(t, awsCLI("ec2", "deregister-instance-event-notification-attributes",
		"--instance-tag-attribute", "InstanceTagKeys=Project"))
	after := q("ec2", "describe-instance-event-notification-attributes",
		"--query", "InstanceTagAttribute.InstanceTagKeys", "--output", "text")
	if strings.Contains(after, "Project") {
		t.Fatalf("deregistered key must be gone, got %q", after)
	}
	if !strings.Contains(after, "Owner") {
		t.Fatalf("Owner must remain registered, got %q", after)
	}
}

// TestEC2CLI_MonitorAndInstanceOps covers detailed-monitoring toggling plus the
// reboot/report-status/reset-attribute instance-management ops on a launched
// instance, and the honest-empty describe-classic-link-instances.
func TestEC2CLI_MonitorAndInstanceOps(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.183.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.183.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	inst := q("ec2", "run-instances", "--image-id", "ami-12345678", "--instance-type", "t3.micro",
		"--subnet-id", subnet, "--query", "Instances[0].InstanceId", "--output", "text")

	mon := q("ec2", "monitor-instances", "--instance-ids", inst,
		"--query", "InstanceMonitorings[0].Monitoring.State", "--output", "text")
	if mon != "enabled" {
		t.Fatalf("monitor-instances: got %q, want enabled", mon)
	}
	unmon := q("ec2", "unmonitor-instances", "--instance-ids", inst,
		"--query", "InstanceMonitorings[0].Monitoring.State", "--output", "text")
	if unmon != "disabled" {
		t.Fatalf("unmonitor-instances: got %q, want disabled", unmon)
	}

	runCLI(t, awsCLI("ec2", "reboot-instances", "--instance-ids", inst))
	runCLI(t, awsCLI("ec2", "report-instance-status", "--instances", inst,
		"--status", "impaired", "--reason-codes", "unresponsive"))

	// Disable then reset sourceDestCheck; it must return to the default (true).
	runCLI(t, awsCLI("ec2", "modify-instance-attribute", "--instance-id", inst, "--no-source-dest-check"))
	runCLI(t, awsCLI("ec2", "reset-instance-attribute", "--instance-id", inst, "--attribute", "sourceDestCheck"))
	sdc := q("ec2", "describe-instance-attribute", "--instance-id", inst, "--attribute", "sourceDestCheck",
		"--query", "SourceDestCheck.Value", "--output", "text")
	if sdc != "True" {
		t.Fatalf("reset-instance-attribute: sourceDestCheck got %q, want True", sdc)
	}

	cl := q("ec2", "describe-classic-link-instances",
		"--query", "length(Instances)", "--output", "text")
	if cl != "0" {
		t.Fatalf("describe-classic-link-instances must be empty, got count %q", cl)
	}
}
