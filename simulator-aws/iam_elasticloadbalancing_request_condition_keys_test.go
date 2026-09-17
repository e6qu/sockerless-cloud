package main

import (
	"net/url"
	"testing"
)

func TestElasticLoadBalancingConditionKeysReadTheRequest(t *testing.T) {
	cases := []struct {
		operation string
		form      url.Values
		want      map[string][]string
	}{
		{"CreateLoadBalancer", url.Values{
			"Name":                                 {"web"},
			"Scheme":                               {"internal"},
			"SecurityGroups.member.1":              {"sg-1"},
			"SecurityGroups.member.2":              {"sg-2"},
			"Subnets.member.1":                     {"subnet-a"},
			"SubnetMappings.member.1.SubnetId":     {"subnet-b"},
			"SubnetMappings.member.1.AllocationId": {"eipalloc-1"},
		}, map[string][]string{
			"elasticloadbalancing:Scheme":        {"internal"},
			"elasticloadbalancing:SecurityGroup": {"sg-1", "sg-2"},
			"elasticloadbalancing:Subnet":        {"subnet-a", "subnet-b"},
		}},
		{"SetSecurityGroups", url.Values{"LoadBalancerArn": {"arn:lb"}, "SecurityGroups.member.1": {"sg-3"}},
			map[string][]string{"elasticloadbalancing:SecurityGroup": {"sg-3"}}},
		{"SetSubnets", url.Values{"LoadBalancerArn": {"arn:lb"}, "SubnetMappings.member.1.SubnetId": {"subnet-c"}},
			map[string][]string{"elasticloadbalancing:Subnet": {"subnet-c"}}},
		{"CreateListener", url.Values{
			"LoadBalancerArn": {"arn:lb"},
			"Protocol":        {"HTTPS"},
			"Port":            {"443"},
			"SslPolicy":       {"ELBSecurityPolicy-TLS13-1-2-2021-06"},
		}, map[string][]string{
			"elasticloadbalancing:ListenerProtocol": {"HTTPS"},
			"elasticloadbalancing:SecurityPolicy":   {"ELBSecurityPolicy-TLS13-1-2-2021-06"},
		}},
		{"ModifyListener", url.Values{"ListenerArn": {"arn:l"}, "Protocol": {"HTTP"}},
			map[string][]string{
				"elasticloadbalancing:ListenerProtocol": {"HTTP"},
				"elasticloadbalancing:SecurityPolicy":   nil,
			}},
	}
	for _, c := range cases {
		t.Run(c.operation, func(t *testing.T) {
			ctx := queryServiceConditionContext(t, "elasticloadbalancing", c.operation, c.form)
			assertServiceConditionContext(t, ctx, c.want)
		})
	}
}

func TestElasticLoadBalancingConditionKeysAreAbsentForAbsentMembers(t *testing.T) {
	ctx := queryServiceConditionContext(t, "elasticloadbalancing", "CreateLoadBalancer", url.Values{
		"Name": {"web"},
	})
	if len(ctx) != 0 {
		t.Errorf("a load balancer naming only itself settled %v", ctx)
	}
	// SetIpAddressType declares none of these keys.
	ctx = queryServiceConditionContext(t, "elasticloadbalancing", "SetIpAddressType", url.Values{
		"LoadBalancerArn":  {"arn:lb"},
		"Subnets.member.1": {"subnet-a"},
	})
	if len(ctx) != 0 {
		t.Errorf("SetIpAddressType settled %v", ctx)
	}
}
