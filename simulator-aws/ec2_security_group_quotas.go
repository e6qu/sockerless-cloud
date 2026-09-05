package main

import (
	"fmt"
	"net/http"
)

// ValidateSecurityGroupQuotasForInterface answers whether a set of security
// groups could all be attached to one network interface without crossing the
// Amazon VPC quotas AWS documents: the rules a security group may carry, and
// the security groups a network interface may have.
//
// Both are published defaults rather than anything the simulator invents, and
// both are adjustable in a real account — a caller that has raised them will
// read a stricter answer here than AWS would give. Recording them as the
// documented defaults is the closest a reimplementation gets without the
// account's own Service Quotas state, and it is a real computation over the
// security groups the simulator holds rather than a canned Valid=true.
//
// AWS notes that only authorized AWS services may call this operation; the
// authorization gate is the simulator's IAM path, not this handler, which
// answers the question it is asked.
const (
	// Inbound or outbound rules per security group.
	ec2RulesPerSecurityGroupQuota = 60
	// Security groups per network interface.
	ec2SecurityGroupsPerInterfaceQuota = 5
)

// ec2SecurityGroupRuleCount counts rules the way AWS counts them against the
// quota: one per source or destination in a permission, not one per permission.
// A permission naming three CIDRs is three rules.
func ec2SecurityGroupRuleCount(permissions []EC2IpPermission) int {
	n := 0
	for _, p := range permissions {
		sources := len(p.IpRanges) + len(p.Ipv6Ranges) + len(p.PrefixListIds) + len(p.UserIdGroupPairs)
		if sources == 0 {
			// A permission naming no source still occupies a rule.
			sources = 1
		}
		n += sources
	}
	return n
}

func handleValidateSecurityGroupQuotasForInterface(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SecurityGroupId")

	valid := len(ids) <= ec2SecurityGroupsPerInterfaceQuota
	for _, id := range ids {
		sg, ok := ec2SecurityGroups.Get(id)
		if !ok {
			ec2ErrorXML(w, "InvalidGroup.NotFound",
				fmt.Sprintf("The security group '%s' does not exist", id), http.StatusBadRequest)
			return
		}
		if ec2SecurityGroupRuleCount(sg.IpPermissions) > ec2RulesPerSecurityGroupQuota ||
			ec2SecurityGroupRuleCount(sg.IpPermissionsEgress) > ec2RulesPerSecurityGroupQuota {
			valid = false
		}
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ValidateSecurityGroupQuotasForInterfaceResponse %s>
  <requestId>%s</requestId>
  <valid>%t</valid>
</ValidateSecurityGroupQuotasForInterfaceResponse>`, ec2Xmlns(), generateUUID(), valid)
}
