package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseDefaultRouteGatewayIPv4(t *testing.T) {
	route := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t011EA90A\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth0\t001EA90A\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"

	got := parseDefaultRouteGatewayIPv4(route)
	if got != "10.169.30.1" {
		t.Fatalf("default gateway = %q, want 10.169.30.1", got)
	}
}

func TestParseDefaultRouteGatewayIPv4Missing(t *testing.T) {
	got := parseDefaultRouteGatewayIPv4("Iface\tDestination\tGateway\neth0\t001EA90A\t00000000\n")
	if got != "" {
		t.Fatalf("default gateway = %q, want empty", got)
	}
}

func TestRewriteHostDockerInternalEnv(t *testing.T) {
	env := map[string]string{
		"AWS_ENDPOINT_URL": "http://host.docker.internal:4566",
		"UNCHANGED":        "http://example.test",
	}

	got := rewriteHostDockerInternalEnvWithGateway(env, "10.89.30.1")
	if got["UNCHANGED"] != "http://example.test" {
		t.Fatalf("UNCHANGED = %q", got["UNCHANGED"])
	}
	if got["AWS_ENDPOINT_URL"] != "http://10.89.30.1:4566" {
		t.Fatalf("AWS_ENDPOINT_URL = %q", got["AWS_ENDPOINT_URL"])
	}
	if env["AWS_ENDPOINT_URL"] != "http://host.docker.internal:4566" {
		t.Fatalf("input env was mutated: %q", env["AWS_ENDPOINT_URL"])
	}
}

func TestRewriteHostDockerInternalEnvLeavesNativeHostAlias(t *testing.T) {
	env := map[string]string{
		"AWS_ENDPOINT_URL": "http://host.docker.internal:4566",
	}

	got := rewriteHostDockerInternalEnvForRuntime(env, false, "10.0.0.1")

	if got["AWS_ENDPOINT_URL"] != env["AWS_ENDPOINT_URL"] {
		t.Fatalf("native-host endpoint = %q, want %q", got["AWS_ENDPOINT_URL"], env["AWS_ENDPOINT_URL"])
	}
}

func TestRewriteSimulatorEndpointForRealVPC(t *testing.T) {
	env := map[string]string{
		"AWS_ENDPOINT_URL": "http://host.docker.internal:4566",
		"QUEUE_URL":        "http://host.containers.internal:4566/123456789012/proof",
		"OTHER_HOST_PORT":  "http://host.docker.internal:8080/health",
	}

	got := rewriteSimulatorEndpointForRealVPC(env, 4566)

	if got["AWS_ENDPOINT_URL"] != "http://169.254.170.2" {
		t.Fatalf("AWS_ENDPOINT_URL = %q", got["AWS_ENDPOINT_URL"])
	}
	if got["QUEUE_URL"] != "http://169.254.170.2/123456789012/proof" {
		t.Fatalf("QUEUE_URL = %q", got["QUEUE_URL"])
	}
	if got["OTHER_HOST_PORT"] != env["OTHER_HOST_PORT"] {
		t.Fatalf("OTHER_HOST_PORT = %q, want %q", got["OTHER_HOST_PORT"], env["OTHER_HOST_PORT"])
	}
	if env["AWS_ENDPOINT_URL"] != "http://host.docker.internal:4566" {
		t.Fatalf("input env was mutated: %q", env["AWS_ENDPOINT_URL"])
	}
}

func TestWorkloadHostGatewayIPv4PrefersDockerHostAlias(t *testing.T) {
	lookups := make([]string, 0, 1)
	got := workloadHostGatewayIPv4(func(host string) ([]string, error) {
		lookups = append(lookups, host)
		if host != "host.docker.internal" {
			t.Fatalf("unexpected lookup %q", host)
		}
		return []string{"192.168.127.254"}, nil
	}, func() string {
		t.Fatal("route fallback was used despite a Docker host alias")
		return ""
	})

	if got != "192.168.127.254" {
		t.Fatalf("gateway = %q, want outer container host alias 192.168.127.254", got)
	}
	if !reflect.DeepEqual(lookups, []string{"host.docker.internal"}) {
		t.Fatalf("lookups = %v", lookups)
	}
}

func TestWorkloadHostGatewayIPv4UsesContainersAliasBeforeRoute(t *testing.T) {
	lookups := make([]string, 0, 2)
	got := workloadHostGatewayIPv4(func(host string) ([]string, error) {
		lookups = append(lookups, host)
		if host == "host.docker.internal" {
			return nil, errors.New("not found")
		}
		return []string{"192.168.127.253"}, nil
	}, func() string {
		t.Fatal("route fallback was used despite a Podman host alias")
		return ""
	})

	if got != "192.168.127.253" {
		t.Fatalf("gateway = %q, want outer container host alias 192.168.127.253", got)
	}
	if !reflect.DeepEqual(lookups, []string{"host.docker.internal", "host.containers.internal"}) {
		t.Fatalf("lookups = %v", lookups)
	}
}

func TestWorkloadHostGatewayIPv4FallsBackToRoute(t *testing.T) {
	got := workloadHostGatewayIPv4(func(string) ([]string, error) {
		return []string{"127.0.0.1", "::1", "invalid"}, nil
	}, func() string {
		return "10.88.0.1"
	})

	if got != "10.88.0.1" {
		t.Fatalf("gateway = %q, want route fallback 10.88.0.1", got)
	}
}
