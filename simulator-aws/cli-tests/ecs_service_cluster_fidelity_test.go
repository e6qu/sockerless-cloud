package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECSServiceClusterFidelityCLI covers the service knobs that round-trip on
// create + update and the cluster-settings update path via the aws CLI.
func TestECSServiceClusterFidelityCLI(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	q("ecs", "create-cluster", "--cluster-name", "cli-svc-cluster", "--query", "cluster.clusterName", "--output", "text")
	tdArn := q("ecs", "register-task-definition", "--family", "cli-svc-td",
		"--container-definitions", `[{"name":"app","image":"`+containerCommandImage+`","command":["hold"]}]`,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")

	q("ecs", "create-service", "--cluster", "cli-svc-cluster", "--service-name", "cli-svc",
		"--task-definition", tdArn, "--desired-count", "2",
		"--enable-ecs-managed-tags", "--health-check-grace-period-seconds", "90",
		"--placement-constraints", "type=memberOf,expression=attribute:ecs.instance-type == t3.micro",
		"--placement-strategy", "type=spread,field=attribute:ecs.availability-zone",
		"--query", "service.serviceName", "--output", "text")
	cleanupCLIService(t, "cli-svc-cluster", "cli-svc")

	out := q("ecs", "describe-services", "--cluster", "cli-svc-cluster", "--services", "cli-svc",
		"--query", "services[0].[enableECSManagedTags,healthCheckGracePeriodSeconds,placementConstraints[0].expression,placementStrategy[0].type]", "--output", "text")
	// Tab-separated; the placement-constraint expression itself contains spaces.
	f := strings.Split(out, "\t")
	if len(f) != 4 || f[0] != "True" || f[1] != "90" ||
		!strings.Contains(f[2], "t3.micro") || f[3] != "spread" {
		t.Fatalf("service knobs round-trip: got %q", out)
	}

	// UpdateService persists more than desiredCount/taskDefinition.
	q("ecs", "update-service", "--cluster", "cli-svc-cluster", "--service", "cli-svc",
		"--desired-count", "3", "--health-check-grace-period-seconds", "240", "--enable-execute-command",
		"--query", "service.serviceName", "--output", "text")
	upd := q("ecs", "describe-services", "--cluster", "cli-svc-cluster", "--services", "cli-svc",
		"--query", "services[0].[desiredCount,healthCheckGracePeriodSeconds,enableExecuteCommand]", "--output", "text")
	if g := strings.Fields(upd); len(g) != 3 || g[0] != "3" || g[1] != "240" || g[2] != "True" {
		t.Fatalf("update-service round-trip: got %q", upd)
	}

	// UpdateClusterSettings flips containerInsights.
	q("ecs", "update-cluster-settings", "--cluster", "cli-svc-cluster",
		"--settings", "name=containerInsights,value=enabled", "--query", "cluster.clusterName", "--output", "text")
	ci := q("ecs", "describe-clusters", "--clusters", "cli-svc-cluster", "--include", "SETTINGS",
		"--query", "clusters[0].settings[0].value", "--output", "text")
	if ci != "enabled" {
		t.Fatalf("update-cluster-settings: got %q, want enabled", ci)
	}
}
