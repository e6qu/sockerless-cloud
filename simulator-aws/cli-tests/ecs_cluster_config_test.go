package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECSCLI_ClusterSettingsAndConfig drives DescribeClusters --include
// SETTINGS CONFIGURATIONS via the aws CLI.
func TestECSCLI_ClusterSettingsAndConfig(t *testing.T) {
	cluster := "probe-cli-cluster-config"
	runCLI(t, awsCLI("ecs", "create-cluster",
		"--cluster-name", cluster,
		"--settings", "name=containerInsights,value=enabled",
		"--configuration", "executeCommandConfiguration={kmsKeyId=arn:aws:kms:us-east-1:123456789012:key/test-key,logging=DEFAULT}"))

	insights := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-clusters",
		"--clusters", cluster, "--include", "SETTINGS", "CONFIGURATIONS",
		"--query", "clusters[0].settings[?name=='containerInsights'].value | [0]", "--output", "text")))
	if insights != "enabled" {
		t.Fatalf("containerInsights setting = %q, want enabled", insights)
	}

	kms := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-clusters",
		"--clusters", cluster, "--include", "SETTINGS", "CONFIGURATIONS",
		"--query", "clusters[0].configuration.executeCommandConfiguration.kmsKeyId", "--output", "text")))
	if !strings.Contains(kms, "key/test-key") {
		t.Fatalf("executeCommand kmsKeyId = %q, want the test key", kms)
	}
}
