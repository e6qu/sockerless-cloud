package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestELBv2LoadBalancerAttributeDefaultsPerType(t *testing.T) {
	gateway := defaultELBv2LoadBalancerAttributes("gateway", "")
	if want := []string{"deletion_protection.enabled", "load_balancing.cross_zone.enabled"}; !slices.Equal(slices.Sorted(maps.Keys(gateway)), want) {
		t.Errorf("gateway keys = %v, want %v", slices.Sorted(maps.Keys(gateway)), want)
	}

	network := defaultELBv2LoadBalancerAttributes("network", "internal")
	for key, want := range map[string]string{
		"load_balancing.cross_zone.enabled":      "false",
		"dns_record.client_routing_policy":       "any_availability_zone",
		"secondary_ips.auto_assigned.per_subnet": "0",
		"ipv6.deny_all_igw_traffic":              "true",
		"access_logs.s3.enabled":                 "false",
		"zonal_shift.config.enabled":             "false",
	} {
		if got, ok := network[key]; !ok || got != want {
			t.Errorf("network %s = %q (present %v), want %q", key, got, ok, want)
		}
	}
	for key := range network {
		if key == "idle_timeout.timeout_seconds" || strings.HasPrefix(key, "routing.http") {
			t.Errorf("network load balancer reports Application Load Balancer key %s", key)
		}
	}

	application := defaultELBv2LoadBalancerAttributes("application", "internet-facing")
	for key, want := range map[string]string{
		"load_balancing.cross_zone.enabled":       "true",
		"idle_timeout.timeout_seconds":            "60",
		"client_keep_alive.seconds":               "3600",
		"routing.http.desync_mitigation_mode":     "defensive",
		"routing.http.xff_header_processing.mode": "append",
		"ipv6.deny_all_igw_traffic":               "false",
		"waf.fail_open.enabled":                   "false",
	} {
		if got := application[key]; got != want {
			t.Errorf("application %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"dns_record.client_routing_policy", "secondary_ips.auto_assigned.per_subnet"} {
		if _, ok := application[key]; ok {
			t.Errorf("application load balancer reports Network Load Balancer key %s", key)
		}
	}
}

func TestELBv2InvalidLoadBalancerAttribute(t *testing.T) {
	for _, tc := range []struct {
		lbType   string
		modified map[string]string
		want     string
	}{
		{"application", map[string]string{"idle_timeout.timeout_seconds": "30", "routing.http2.enabled": "false"}, ""},
		{"network", map[string]string{"dns_record.client_routing_policy": "availability_zone_affinity"}, ""},
		{"gateway", map[string]string{"load_balancing.cross_zone.enabled": "true"}, ""},
		{"network", map[string]string{"routing.http.xff_header_processing.mode": "remove"}, "Key routing.http.xff_header_processing.mode not valid"},
		{"gateway", map[string]string{"access_logs.s3.enabled": "true"}, "Key access_logs.s3.enabled not valid"},
		{"application", map[string]string{"secondary_ips.auto_assigned.per_subnet": "1"}, "Key secondary_ips.auto_assigned.per_subnet not valid"},
		{"application", map[string]string{"no.such.key": "x"}, "Key no.such.key not valid"},
		{"application", map[string]string{"idle_timeout.timeout_seconds": "0"}, "Load balancer attribute value '0' for key 'idle_timeout.timeout_seconds' is not valid; it must be an integer from 1 to 4000"},
		{"application", map[string]string{"client_keep_alive.seconds": "59"}, "Load balancer attribute value '59' for key 'client_keep_alive.seconds' is not valid; it must be an integer from 60 to 604800"},
		{"network", map[string]string{"secondary_ips.auto_assigned.per_subnet": "8"}, "Load balancer attribute value '8' for key 'secondary_ips.auto_assigned.per_subnet' is not valid; it must be an integer from 0 to 7"},
		{"application", map[string]string{"routing.http.desync_mitigation_mode": "lenient"}, "Load balancer attribute value 'lenient' for key 'routing.http.desync_mitigation_mode' is not valid; it must be one of 'monitor', 'defensive', 'strictest'"},
		{"network", map[string]string{"deletion_protection.enabled": "yes"}, "Load balancer attribute value 'yes' for key 'deletion_protection.enabled' is not valid; it must be one of 'true', 'false'"},
	} {
		if got := elbv2InvalidLoadBalancerAttribute(tc.lbType, tc.modified); got != tc.want {
			t.Errorf("%s %v: got %q, want %q", tc.lbType, tc.modified, got, tc.want)
		}
	}
}
