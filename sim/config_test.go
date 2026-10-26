package sim

import "testing"

func TestConfigFromEnvIncludesApplicationReleaseRevision(t *testing.T) {
	t.Setenv("APPLICATION_RELEASE_REVISION", "0123456789ab")
	t.Setenv("SIM_MONITORING_TOKEN", "aws-monitoring-token-00000000000000000000")
	config := ConfigFromEnv("aws", "4566")
	if got := config.ApplicationReleaseRevision; got != "0123456789ab" {
		t.Fatalf("application release revision = %q", got)
	}
	if config.ApplicationMonitoringToken != "aws-monitoring-token-00000000000000000000" {
		t.Fatal("application monitoring token was not read from the environment")
	}
}

func TestBannerEndpoint(t *testing.T) {
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{ListenAddr: ":4566"}, "http://localhost:4566"},
		{Config{ListenAddr: "127.0.0.1:44569"}, "http://127.0.0.1:44569"},
		{Config{ListenAddr: "[::]:4568", TLSCert: "cert.pem"}, "https://localhost:4568"},
		{Config{ListenAddr: "[::1]:4567"}, "http://[::1]:4567"},
	} {
		if got := tc.cfg.bannerEndpoint(); got != tc.want {
			t.Errorf("bannerEndpoint(%q, TLS %v) = %q, want %q", tc.cfg.ListenAddr, tc.cfg.TLSCert != "", got, tc.want)
		}
	}
}
