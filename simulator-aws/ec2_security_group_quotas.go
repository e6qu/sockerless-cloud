package main

import (
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Check against the Amazon VPC quotas AWS documents: rules per security group
// and security groups per network interface. Both are adjustable defaults, so
// an account that raised them gets a stricter answer here than from AWS.
// Only authorized AWS services may call the operation; the IAM path enforces
// that.
const (
	// Inbound or outbound rules per security group.
	ec2RulesPerSecurityGroupQuota = 60
	// Security groups per network interface.
	ec2SecurityGroupsPerInterfaceQuota = 5
)

// AWS counts one rule per source or destination, so a permission naming three
// CIDRs is three rules.
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
</ValidateSecurityGroupQuotasForInterfaceResponse>`, ec2Xmlns(), sim.NewUUID(), valid)
}
