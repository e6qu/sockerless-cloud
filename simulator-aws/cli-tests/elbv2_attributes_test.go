package aws_cli_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestELBv2CLI_LoadBalancerAttributesPerType reads each load balancer type's
// attributes with `aws elbv2 describe-load-balancer-attributes`: a Network and
// a Gateway Load Balancer report only their own type's attributes, and
// `modify-load-balancer-attributes` refuses a key that belongs to another type.
func TestELBv2CLI_LoadBalancerAttributesPerType(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.88.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnet1 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.88.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")))
	subnet2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.88.2.0/24",
		"--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")))
	create := func(name, lbType string) string {
		t.Helper()
		arn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-load-balancer",
			"--name", name, "--type", lbType, "--subnets", subnet1, subnet2,
			"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")))
		t.Cleanup(func() { runCLI(t, awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", arn)) })
		return arn
	}
	describe := func(arn string) map[string]string {
		t.Helper()
		var out struct {
			Attributes []struct{ Key, Value string }
		}
		raw := runCLI(t, awsCLI("elbv2", "describe-load-balancer-attributes", "--load-balancer-arn", arn, "--output", "json"))
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		attrs := map[string]string{}
		for _, a := range out.Attributes {
			attrs[a.Key] = a.Value
		}
		return attrs
	}

	nlbArn := create("cli-attrs-nlb", "network")
	nlb := describe(nlbArn)
	var keys []string
	for k := range nlb {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{
		"access_logs.s3.bucket", "access_logs.s3.enabled", "access_logs.s3.prefix",
		"deletion_protection.enabled", "dns_record.client_routing_policy", "ipv6.deny_all_igw_traffic",
		"load_balancing.cross_zone.enabled", "secondary_ips.auto_assigned.per_subnet", "zonal_shift.config.enabled",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("network load balancer attributes = %v, want %v", keys, want)
	}
	if nlb["dns_record.client_routing_policy"] != "any_availability_zone" || nlb["load_balancing.cross_zone.enabled"] != "false" {
		t.Fatalf("network load balancer defaults = %v", nlb)
	}

	gwlbArn := create("cli-attrs-gwlb", "gateway")
	if got := describe(gwlbArn); len(got) != 2 || got["deletion_protection.enabled"] != "false" || got["load_balancing.cross_zone.enabled"] != "false" {
		t.Fatalf("gateway load balancer attributes = %v", got)
	}

	errOut := runCLIExpectError(t, awsCLI("elbv2", "modify-load-balancer-attributes",
		"--load-balancer-arn", nlbArn,
		"--attributes", "Key=routing.http.xff_header_processing.mode,Value=remove"))
	if !strings.Contains(errOut, "InvalidConfigurationRequest") || !strings.Contains(errOut, "Key routing.http.xff_header_processing.mode not valid") {
		t.Fatalf("an Application Load Balancer key on a Network Load Balancer was not refused: %s", errOut)
	}
	out := runCLI(t, awsCLI("elbv2", "modify-load-balancer-attributes",
		"--load-balancer-arn", nlbArn,
		"--attributes", "Key=dns_record.client_routing_policy,Value=partial_availability_zone_affinity",
		"--query", "Attributes[?Key=='dns_record.client_routing_policy'].Value", "--output", "text"))
	if strings.TrimSpace(out) != "partial_availability_zone_affinity" {
		t.Fatalf("modify-load-balancer-attributes answered %q", out)
	}
}
