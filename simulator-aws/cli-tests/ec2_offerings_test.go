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
