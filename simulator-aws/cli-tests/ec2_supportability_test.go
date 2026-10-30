package aws_cli_test

import (
	"strings"
	"testing"
)

// The scores read the capacity the simulator's host has left, so the test
// asks for an amount no host holds, and for zones that share one host.
func TestEC2CLI_CapacityManagerSupportabilityScores(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	zones := strings.Fields(q("ec2", "describe-availability-zones",
		"--query", "AvailabilityZones[0:2].ZoneId", "--output", "text"))
	if len(zones) != 2 {
		t.Fatalf("describe-availability-zones: got %v, want two zone IDs", zones)
	}

	scores := q("ec2", "get-capacity-manager-supportability-scores",
		"--capacity-amount", "1000000", "--capacity-unit", "vcpus", "--market-type", "on-demand",
		"--capacity-availability-zone-ids", zones[0], zones[1],
		"--instance-types", "t3.micro",
		"--query", "[AggregateScore, CapacityManagerSupportabilityScores[].Score]", "--output", "text")
	if got := strings.Fields(scores); len(got) != 3 || got[0] != "low" || got[1] != "low" || got[2] != "low" {
		t.Fatalf("get-capacity-manager-supportability-scores: got %q, want low for the aggregate and both zones", scores)
	}

	one := strings.Fields(q("ec2", "get-capacity-manager-supportability-scores",
		"--capacity-amount", "1", "--capacity-unit", "instances", "--market-type", "spot",
		"--capacity-availability-zone-ids", zones[0], zones[1],
		"--instance-types", "t3.nano",
		"--query", "[AggregateScore, CapacityManagerSupportabilityScores[].Score]", "--output", "text"))
	if len(one) != 3 || one[1] != one[2] || one[0] != one[1] {
		t.Fatalf("get-capacity-manager-supportability-scores: got %v, want zones on one host to score alike", one)
	}

	out, err := awsCLI("ec2", "get-capacity-manager-supportability-scores",
		"--capacity-amount", "1", "--capacity-unit", "instances", "--market-type", "on-demand",
		"--capacity-availability-zone-ids", "zzz9-az9", "--instance-types", "t3.micro").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "InvalidParameterValue") {
		t.Fatalf("an unknown zone ID: got err=%v, output %s; want InvalidParameterValue", err, out)
	}
}

// A launch no host can hold, and Spot placement scores past any host's
// capacity, read through the CLI.
func TestEC2CLI_HostCapacityRefusesALaunchAndScoresSpot(t *testing.T) {
	t.Parallel()
	out, err := awsCLI("ec2", "run-instances", "--image-id", ec2AmazonLinuxAMI(t),
		"--instance-type", "u7in-32tb.224xlarge", "--count", "1").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "InsufficientInstanceCapacity") {
		t.Fatalf("a 32 TiB launch: got err=%v, output %s; want InsufficientInstanceCapacity", err, out)
	}

	scores := strings.Fields(strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-spot-placement-scores",
		"--instance-types", "t3.micro", "--target-capacity", "1000000",
		"--region-names", "us-east-1", "ap-southeast-7",
		"--query", "SpotPlacementScores[].Score", "--output", "text"))))
	if len(scores) != 2 || scores[0] != "1" || scores[1] != "1" {
		t.Fatalf("get-spot-placement-scores: got %v, want 1 for both Regions", scores)
	}
}
