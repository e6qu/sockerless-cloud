package simulator

import "testing"

func TestConfigFromEnvIncludesApplicationReleaseRevision(t *testing.T) {
	t.Setenv("APPLICATION_RELEASE_REVISION", "0123456789ab")
	if got := ConfigFromEnv("azure").ApplicationReleaseRevision; got != "0123456789ab" {
		t.Fatalf("application release revision = %q", got)
	}
}
