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
	// elasticloadbalancing:CreateAction is the create behind the tags an AddTags
	// authorization covers: Elastic Load Balancing authorizes the tags a create
	// request carries as AddTags, and this key names that create, so a grant can
	// allow tagging on creation without allowing a caller to retag an existing
	// load balancer. A plain AddTags call leaves it unset.
	iamSetConditionValues(ctx, "elasticloadbalancing:CreateAction",
		iamTagOnCreateOperation(r, operation, "AddTags", len(parseELBv2Tags(r, "Tags")) > 0))

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
