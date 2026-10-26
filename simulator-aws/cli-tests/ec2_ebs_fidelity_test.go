package aws_cli_test

import (
	"strings"
	"testing"
)

func TestEC2EBSFidelityCLI(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	// gp3 defaults to 3000 IOPS and 125 MiB/s throughput.
	gp3 := q("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "20",
		"--volume-type", "gp3", "--query", "VolumeId", "--output", "text")
	out := q("ec2", "describe-volumes", "--volume-ids", gp3,
		"--query", "Volumes[0].[Iops,Throughput]", "--output", "text")
	if f := strings.Fields(out); len(f) != 2 || f[0] != "3000" || f[1] != "125" {
		t.Fatalf("gp3 iops/throughput: got %q, want '3000 125'", out)
	}

	io1 := q("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "10",
		"--volume-type", "io1", "--iops", "1500", "--query", "VolumeId", "--output", "text")
	if v := q("ec2", "describe-volumes", "--volume-ids", io1,
		"--query", "Volumes[0].Iops", "--output", "text"); v != "1500" {
		t.Fatalf("io1 iops: got %q, want 1500", v)
	}
	runCLI(t, awsCLI("ec2", "modify-volume", "--volume-id", io1, "--size", "30", "--iops", "2000"))
	state := q("ec2", "describe-volumes-modifications", "--volume-ids", io1,
		"--query", "VolumesModifications[0].[ModificationState,TargetSize,TargetIops]", "--output", "text")
	if f := strings.Fields(state); len(f) != 3 || f[0] != "completed" || f[1] != "30" || f[2] != "2000" {
		t.Fatalf("volume modification: got %q, want 'completed 30 2000'", state)
	}

	n := q("ec2", "describe-volumes", "--filters", "Name=volume-type,Values=io1",
		"--query", "length(Volumes[?VolumeId=='"+io1+"'])", "--output", "text")
	if n != "1" {
		t.Fatalf("volume-type filter must return the io1 volume, got count %q", n)
	}
}
