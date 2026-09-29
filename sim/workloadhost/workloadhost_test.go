package workloadhost

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParseDefaultRouteGatewayIPv4(t *testing.T) {
	route := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t011EA90A\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth0\t001EA90A\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"

	if got := parseDefaultRouteGatewayIPv4(route); got != "10.169.30.1" {
		t.Fatalf("default gateway = %q, want 10.169.30.1", got)
	}
}

func TestParseDefaultRouteGatewayIPv4Missing(t *testing.T) {
	if got := parseDefaultRouteGatewayIPv4("Iface\tDestination\tGateway\neth0\t001EA90A\t00000000\n"); got != "" {
		t.Fatalf("default gateway = %q, want empty", got)
	}
}

func TestOuterHostGatewayIPv4PrefersDockerHostAlias(t *testing.T) {
	var lookups []string
	got := outerHostGatewayIPv4(func(host string) ([]string, error) {
		lookups = append(lookups, host)
		if host != dockerHostAlias {
			t.Fatalf("unexpected lookup %q", host)
		}
		return []string{"192.168.127.254"}, nil
	}, func() string {
		t.Fatal("route fallback was used despite a Docker host alias")
		return ""
	})

	if got != "192.168.127.254" {
		t.Fatalf("gateway = %q, want 192.168.127.254", got)
	}
	if !reflect.DeepEqual(lookups, []string{dockerHostAlias}) {
		t.Fatalf("lookups = %v", lookups)
	}
}

func TestOuterHostGatewayIPv4UsesContainersAliasBeforeRoute(t *testing.T) {
	var lookups []string
	got := outerHostGatewayIPv4(func(host string) ([]string, error) {
		lookups = append(lookups, host)
		if host == dockerHostAlias {
			return nil, errors.New("not found")
		}
		return []string{"192.168.127.253"}, nil
	}, func() string {
		t.Fatal("route fallback was used despite a Podman host alias")
		return ""
	})

	if got != "192.168.127.253" {
		t.Fatalf("gateway = %q, want 192.168.127.253", got)
	}
	if !reflect.DeepEqual(lookups, []string{dockerHostAlias, containersHostAlias}) {
		t.Fatalf("lookups = %v", lookups)
	}
}

func TestOuterHostGatewayIPv4FallsBackToRoute(t *testing.T) {
	got := outerHostGatewayIPv4(func(string) ([]string, error) {
		return []string{"127.0.0.1", "::1", "invalid"}, nil
	}, func() string {
		return "10.88.0.1"
	})

	if got != "10.88.0.1" {
		t.Fatalf("gateway = %q, want route fallback 10.88.0.1", got)
	}
}

func TestParsePodmanMachineHostIPv4(t *testing.T) {
	route := "default via 192.168.127.1 dev enp0s1 proto dhcp src 192.168.127.2 metric 100\n"
	if got, want := parsePodmanMachineHostIPv4(route), "192.168.127.254"; got != want {
		t.Fatalf("parsePodmanMachineHostIPv4() = %q, want %q", got, want)
	}
}

func TestParsePodmanMachineHostIPv4NoSource(t *testing.T) {
	if got := parsePodmanMachineHostIPv4("default via 10.0.2.2 dev eth0\n"); got != "" {
		t.Fatalf("parsePodmanMachineHostIPv4() = %q, want empty", got)
	}
}

func TestListenPort(t *testing.T) {
	for addr, want := range map[string]int{":4566": 4566, "0.0.0.0:8080": 8080, "[::]:443": 443, "4567": 4567} {
		got, err := ListenPort(addr)
		if err != nil || got != want {
			t.Fatalf("ListenPort(%q) = %d, %v; want %d", addr, got, err, want)
		}
	}
	for _, addr := range []string{"", ":", ":0", ":65536", "host:http"} {
		if got, err := ListenPort(addr); err == nil {
			t.Fatalf("ListenPort(%q) = %d, want an error", addr, got)
		}
	}
}

func TestMergeEnvLaterMapsWin(t *testing.T) {
	base := map[string]string{"A": "1", "B": "1"}
	got := MergeEnv(base, nil, map[string]string{"B": "2", "C": "2"}, map[string]string{"C": "3"})
	want := map[string]string{"A": "1", "B": "2", "C": "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeEnv = %v, want %v", got, want)
	}
	if base["B"] != "1" {
		t.Fatalf("MergeEnv mutated its input: %v", base)
	}
}

func TestMetadataIndexForRequest(t *testing.T) {
	var index MetadataIndex[string]
	index.Store("10.0.1.5", "i-0abc")

	r := httptest.NewRequest("GET", "/latest/meta-data/instance-id", nil)
	r.RemoteAddr = "10.0.1.5:51234"
	if got, ok := index.ForRequest(r); !ok || got != "i-0abc" {
		t.Fatalf("ForRequest = %q, %v; want i-0abc", got, ok)
	}

	r.RemoteAddr = "10.0.1.6:51234"
	if got, ok := index.ForRequest(r); ok {
		t.Fatalf("ForRequest for an unknown address = %q, want no record", got)
	}

	index.Delete("10.0.1.5")
	r.RemoteAddr = "10.0.1.5:51234"
	if got, ok := index.ForRequest(r); ok {
		t.Fatalf("ForRequest after Delete = %q, want no record", got)
	}
}
