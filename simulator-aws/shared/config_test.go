package simulator

import "testing"

func TestConfigFromEnvIncludesApplicationReleaseRevision(t *testing.T) {
	t.Setenv("APPLICATION_RELEASE_REVISION", "0123456789ab")
	t.Setenv("SIM_MONITORING_TOKEN", "aws-monitoring-token-00000000000000000000")
	config := ConfigFromEnv("aws")
	if got := config.ApplicationReleaseRevision; got != "0123456789ab" {
		t.Fatalf("application release revision = %q", got)
	}
	if config.ApplicationMonitoringToken != "aws-monitoring-token-00000000000000000000" {
		t.Fatal("application monitoring token was not read from the environment")
	}
}
