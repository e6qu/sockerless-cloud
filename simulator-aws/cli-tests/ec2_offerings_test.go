package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_InstanceTypeOfferings drives DescribeInstanceTypeOfferings via the
// aws CLI — the fck-nat AZ-availability pre-flight.
func TestEC2CLI_InstanceTypeOfferings(t *testing.T) {
	it := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-instance-type-offerings",
		"--location-type", "availability-zone",
		"--filters", "Name=instance-type,Values=t4g.nano",
		"--query", "InstanceTypeOfferings[0].InstanceType", "--output", "text")))
	if it != "t4g.nano" {
		t.Fatalf("describe-instance-type-offerings InstanceType = %q, want t4g.nano", it)
	}
}

// TestEC2CLI_DescribeInstanceTypes reads an instance type's published vCPUs,
// memory and architecture through describe-instance-types, and refuses an
// instance type that does not exist.
func TestEC2CLI_DescribeInstanceTypes(t *testing.T) {
	got := strings.Fields(runCLI(t, awsCLI("ec2", "describe-instance-types",
		"--instance-types", "c5.2xlarge",
		"--query", "InstanceTypes[0].[VCpuInfo.DefaultVCpus,MemoryInfo.SizeInMiB,ProcessorInfo.SupportedArchitectures[0]]",
		"--output", "text")))
	if strings.Join(got, " ") != "8 16384 x86_64" {
		t.Fatalf("describe-instance-types c5.2xlarge = %q, want 8 vCPUs, 16384 MiB, x86_64", got)
	}
	got = strings.Fields(runCLI(t, awsCLI("ec2", "describe-instance-types",
		"--filters", "Name=instance-type,Values=r6g.*", "Name=memory-info.size-in-mib,Values=65536",
		"--query", "InstanceTypes[].InstanceType", "--output", "text")))
	if strings.Join(got, " ") != "r6g.2xlarge" {
		t.Fatalf("r6g instance types with 65536 MiB = %q, want r6g.2xlarge", got)
	}
	out := runCLIExpectError(t, awsCLI("ec2", "describe-instance-types", "--instance-types", "t9.huge"))
	if !strings.Contains(out, "InvalidInstanceType") {
		t.Fatalf("expected InvalidInstanceType for an instance type that does not exist, got %s", out)
	}
}
