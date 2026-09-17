package main

import (
	"fmt"
	"net/http"
)

// Elastic Load Balancing's request condition keys: the scheme, networks,
// security groups, listener protocols and TLS policies a request asks a load
// balancer to have, which a policy holds to an approved set.

func init() {
	registerIAMRequestConditionPopulator("elasticloadbalancing", iamPopulateELBRequestConditionKeys)
}

func iamPopulateELBRequestConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
	switch operation {
	case "CreateLoadBalancer":
		ecSetString(ctx, "elasticloadbalancing:Scheme", r.FormValue("Scheme"))
		asSetList(ctx, "elasticloadbalancing:SecurityGroup", iamQueryList(r, "SecurityGroups"))
		asSetList(ctx, "elasticloadbalancing:Subnet", elbRequestSubnets(r))
	case "SetSecurityGroups":
		asSetList(ctx, "elasticloadbalancing:SecurityGroup", iamQueryList(r, "SecurityGroups"))
	case "SetSubnets":
		asSetList(ctx, "elasticloadbalancing:Subnet", elbRequestSubnets(r))
	case "CreateListener", "ModifyListener":
		ecSetString(ctx, "elasticloadbalancing:ListenerProtocol", r.FormValue("Protocol"))
		ecSetString(ctx, "elasticloadbalancing:SecurityPolicy", r.FormValue("SslPolicy"))
	}
}

// elbRequestSubnets returns the subnets a request names, whether as a plain
// Subnets list or through SubnetMappings.
func elbRequestSubnets(r *http.Request) []string {
	subnets := iamQueryList(r, "Subnets")
	for i := 1; ; i++ {
		prefix := fmt.Sprintf("SubnetMappings.member.%d", i)
		if !ec2HasFormPrefix(r, prefix) {
			return subnets
		}
		if subnet := r.FormValue(prefix + ".SubnetId"); subnet != "" {
			subnets = append(subnets, subnet)
		}
	}
}
