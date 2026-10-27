package main

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleCreateSecurityGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	desc := r.FormValue("GroupDescription")
	vpcId := r.FormValue("VpcId")
	tags := parseTags(r)
	if name == "default" {
		ec2ErrorXML(w, "InvalidParameterValue", "Cannot use reserved security group name: default", http.StatusBadRequest)
		return
	}

	// A group name is unique per VPC. The Amazon ECS backend's idempotent network
	// create relies on InvalidGroup.Duplicate to reuse a group by name and VPC.
	for _, existing := range ec2SecurityGroups.List() {
		if existing.GroupName == name && existing.VpcId == vpcId {
			ec2ErrorXML(w, "InvalidGroup.Duplicate",
				fmt.Sprintf("The security group '%s' already exists for VPC '%s'", name, vpcId),
				http.StatusBadRequest)
			return
		}
	}

	id := ec2ID("sg")

	sg := EC2SecurityGroup{
		GroupId:     id,
		GroupName:   name,
		Description: desc,
		VpcId:       vpcId,
		Tags:        tags,
		OwnerId:     ec2Owner(),
	}
	// A VPC security group starts with an allow-all egress rule;
	// terraform-provider-aws revokes it before applying the configured rules.
	if vpcId != "" {
		sg.IpPermissionsEgress = []EC2IpPermission{{
			IpProtocol: "-1",
			IpRanges:   []EC2IpRange{{CidrIp: "0.0.0.0/0"}},
		}}
	}
	ec2SecurityGroups.Put(id, sg)
	// Store the default egress rule as rule rows so DescribeSecurityGroupRules
	// lists it and it can be revoked by rule ID.
	if vpcId != "" {
		createSecurityGroupRules(id, sg.IpPermissionsEgress[0], true)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSecurityGroupResponse %s>
  <requestId>%s</requestId>
  <groupId>%s</groupId>
  <return>true</return>
</CreateSecurityGroupResponse>`, ec2Xmlns(), sim.NewUUID(), id)
}

func sgItemXML(sg EC2SecurityGroup) string {
	return fmt.Sprintf(`<item>
    <groupId>%s</groupId><groupName>%s</groupName><groupDescription>%s</groupDescription>
    <vpcId>%s</vpcId><ownerId>%s</ownerId>
    %s%s
    %s
  </item>`, sg.GroupId, sg.GroupName, sg.Description, sg.VpcId, sg.OwnerId,
		ipPermsXML("ipPermissions", sg.IpPermissions),
		ipPermsXML("ipPermissionsEgress", sg.IpPermissionsEgress),
		writeTagSetXML(sg.Tags))
}

func ipPermsXML(element string, perms []EC2IpPermission) string {
	if len(perms) == 0 {
		return fmt.Sprintf("<%s/>", element)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", element)
	for _, p := range perms {
		b.WriteString("<item>")
		fmt.Fprintf(&b, "<ipProtocol>%s</ipProtocol>", p.IpProtocol)
		// All-traffic rules carry no ports; emitting 0 would make an inline rule
		// drift "0 -> null".
		if p.IpProtocol != "-1" {
			fmt.Fprintf(&b, "<fromPort>%d</fromPort><toPort>%d</toPort>", p.FromPort, p.ToPort)
		}
		if len(p.IpRanges) > 0 {
			b.WriteString("<ipRanges>")
			for _, r := range p.IpRanges {
				fmt.Fprintf(&b, "<item><cidrIp>%s</cidrIp>", r.CidrIp)
				if r.Description != "" {
					fmt.Fprintf(&b, "<description>%s</description>", r.Description)
				}
				b.WriteString("</item>")
			}
			b.WriteString("</ipRanges>")
		} else {
			b.WriteString("<ipRanges/>")
		}
		if len(p.Ipv6Ranges) > 0 {
			b.WriteString("<ipv6Ranges>")
			for _, r := range p.Ipv6Ranges {
				fmt.Fprintf(&b, "<item><cidrIpv6>%s</cidrIpv6>", r.CidrIpv6)
				if r.Description != "" {
					fmt.Fprintf(&b, "<description>%s</description>", r.Description)
				}
				b.WriteString("</item>")
			}
			b.WriteString("</ipv6Ranges>")
		} else {
			b.WriteString("<ipv6Ranges/>")
		}
		if len(p.PrefixListIds) > 0 {
			b.WriteString("<prefixListIds>")
			for _, pl := range p.PrefixListIds {
				fmt.Fprintf(&b, "<item><prefixListId>%s</prefixListId>", pl.PrefixListId)
				if pl.Description != "" {
					fmt.Fprintf(&b, "<description>%s</description>", pl.Description)
				}
				b.WriteString("</item>")
			}
			b.WriteString("</prefixListIds>")
		} else {
			b.WriteString("<prefixListIds/>")
		}
		if len(p.UserIdGroupPairs) > 0 {
			b.WriteString("<groups>")
			for _, g := range p.UserIdGroupPairs {
				fmt.Fprintf(&b, "<item><groupId>%s</groupId>", g.GroupId)
				if g.Description != "" {
					fmt.Fprintf(&b, "<description>%s</description>", g.Description)
				}
				b.WriteString("</item>")
			}
			b.WriteString("</groups>")
		} else {
			b.WriteString("<groups/>")
		}
		b.WriteString("</item>")
	}
	fmt.Fprintf(&b, "</%s>", element)
	return b.String()
}

func handleDescribeSecurityGroups(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "GroupId")
	names := ec2ParamList(r, "GroupName")
	filters := ec2Filters(r)

	var items strings.Builder
	for _, sg := range ec2SecurityGroups.List() {
		if len(ids) > 0 && !ec2StrInValues(sg.GroupId, ids) {
			continue
		}
		if len(names) > 0 && !ec2StrInValues(sg.GroupName, names) {
			continue
		}
		if !ec2SecurityGroupMatchesFilters(sg, filters) {
			continue
		}
		items.WriteString(sgItemXML(sg))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSecurityGroupsResponse %s>
  <requestId>%s</requestId>
  <securityGroupInfo>%s</securityGroupInfo>
</DescribeSecurityGroupsResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func ec2SecurityGroupMatchesFilters(sg EC2SecurityGroup, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-id":
			if !ec2StrInValues(sg.VpcId, vals) {
				return false
			}
		case "group-id":
			if !ec2StrInValues(sg.GroupId, vals) {
				return false
			}
		case "group-name":
			if !ec2StrInValues(sg.GroupName, vals) {
				return false
			}
		case "description":
			if !ec2StrInValues(sg.Description, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, sg.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GroupId")
	sg, ok := ec2SecurityGroups.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidGroup.NotFound", fmt.Sprintf("The security group '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if sg.GroupName == "default" && sg.VpcId != "" {
		ec2ErrorXML(w, "CannotDelete", fmt.Sprintf("the specified group: %q name: %q cannot be deleted by a user", id, sg.GroupName), http.StatusBadRequest)
		return
	}
	ec2SecurityGroups.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSecurityGroupResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteSecurityGroupResponse>`, ec2Xmlns(), sim.NewUUID())
}

func parseIpPermission(r *http.Request, prefix string) EC2IpPermission {
	perm := EC2IpPermission{
		IpProtocol: r.FormValue(prefix + ".IpProtocol"),
	}
	if v := r.FormValue(prefix + ".FromPort"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &perm.FromPort); err != nil {
			perm.FromPort = 0
		}
	}
	if v := r.FormValue(prefix + ".ToPort"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &perm.ToPort); err != nil {
			perm.ToPort = 0
		}
	}

	for i := 1; ; i++ {
		cidr := r.FormValue(fmt.Sprintf("%s.IpRanges.%d.CidrIp", prefix, i))
		if cidr == "" {
			break
		}
		desc := r.FormValue(fmt.Sprintf("%s.IpRanges.%d.Description", prefix, i))
		perm.IpRanges = append(perm.IpRanges, EC2IpRange{CidrIp: cidr, Description: desc})
	}

	for i := 1; ; i++ {
		cidr := r.FormValue(fmt.Sprintf("%s.Ipv6Ranges.%d.CidrIpv6", prefix, i))
		if cidr == "" {
			break
		}
		desc := r.FormValue(fmt.Sprintf("%s.Ipv6Ranges.%d.Description", prefix, i))
		perm.Ipv6Ranges = append(perm.Ipv6Ranges, EC2Ipv6Range{CidrIpv6: cidr, Description: desc})
	}

	for i := 1; ; i++ {
		plid := r.FormValue(fmt.Sprintf("%s.PrefixListIds.%d.PrefixListId", prefix, i))
		if plid == "" {
			break
		}
		desc := r.FormValue(fmt.Sprintf("%s.PrefixListIds.%d.Description", prefix, i))
		perm.PrefixListIds = append(perm.PrefixListIds, EC2PrefixListId{PrefixListId: plid, Description: desc})
	}

	// Classic clients send UserIdGroupPairs, SDK v2 sends Groups.
	for i := 1; ; i++ {
		gid := r.FormValue(fmt.Sprintf("%s.UserIdGroupPairs.%d.GroupId", prefix, i))
		if gid == "" {
			gid = r.FormValue(fmt.Sprintf("%s.Groups.%d.GroupId", prefix, i))
		}
		if gid == "" {
			break
		}
		desc := r.FormValue(fmt.Sprintf("%s.UserIdGroupPairs.%d.Description", prefix, i))
		if desc == "" {
			desc = r.FormValue(fmt.Sprintf("%s.Groups.%d.Description", prefix, i))
		}
		perm.UserIdGroupPairs = append(perm.UserIdGroupPairs, EC2UserIdGroupPair{GroupId: gid, Description: desc})
	}
	return perm
}

func sgrItemXML(rule EC2SecurityGroupRule) string {
	var b strings.Builder
	b.WriteString("<item>")
	fmt.Fprintf(&b, "<securityGroupRuleId>%s</securityGroupRuleId>", rule.RuleId)
	fmt.Fprintf(&b, "<groupId>%s</groupId>", rule.GroupId)
	fmt.Fprintf(&b, "<groupOwnerId>%s</groupOwnerId>", rule.GroupOwner)
	fmt.Fprintf(&b, "<isEgress>%t</isEgress>", rule.IsEgress)
	fmt.Fprintf(&b, "<ipProtocol>%s</ipProtocol>", rule.IpProtocol)
	// All-traffic rules carry no ports; emitting 0 would make every plan drift
	// "0 -> null".
	if rule.IpProtocol != "-1" {
		fmt.Fprintf(&b, "<fromPort>%d</fromPort>", rule.FromPort)
		fmt.Fprintf(&b, "<toPort>%d</toPort>", rule.ToPort)
	}
	if rule.CidrIpv4 != "" {
		fmt.Fprintf(&b, "<cidrIpv4>%s</cidrIpv4>", rule.CidrIpv4)
	}
	if rule.CidrIpv6 != "" {
		fmt.Fprintf(&b, "<cidrIpv6>%s</cidrIpv6>", rule.CidrIpv6)
	}
	if rule.PrefixListId != "" {
		fmt.Fprintf(&b, "<prefixListId>%s</prefixListId>", rule.PrefixListId)
	}
	if rule.RefGroupId != "" {
		// Omit userId for same-account references: the provider renders
		// "<account>/sg-id" when it is present, drifting against the bare sg-id. The
		// simulator is single-account.
		fmt.Fprintf(&b, "<referencedGroupInfo><groupId>%s</groupId></referencedGroupInfo>", rule.RefGroupId)
	}
	if rule.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", rule.Description)
	}
	fmt.Fprintf(&b, "<securityGroupRuleArn>arn:aws:ec2:%s:%s:security-group-rule/%s</securityGroupRuleArn>",
		awsRegion(), rule.GroupOwner, rule.RuleId)
	// The SDK reads rule tags from <tagSet>, not <tags>.
	b.WriteString(writeTagSetXML(rule.Tags))
	b.WriteString("</item>")
	return b.String()
}

// Amazon EC2 rejects an authorize duplicating an expanded rule (protocol, port
// range and one target) with InvalidPermission.Duplicate; match protocol,
// ports and any shared target.
func ec2PermissionDuplicate(existing []EC2IpPermission, incoming EC2IpPermission) bool {
	for _, e := range existing {
		if e.IpProtocol != incoming.IpProtocol || e.FromPort != incoming.FromPort || e.ToPort != incoming.ToPort {
			continue
		}
		for _, in := range incoming.IpRanges {
			for _, ex := range e.IpRanges {
				if ex.CidrIp == in.CidrIp {
					return true
				}
			}
		}
		for _, in := range incoming.Ipv6Ranges {
			for _, ex := range e.Ipv6Ranges {
				if ex.CidrIpv6 == in.CidrIpv6 {
					return true
				}
			}
		}
		for _, in := range incoming.PrefixListIds {
			for _, ex := range e.PrefixListIds {
				if ex.PrefixListId == in.PrefixListId {
					return true
				}
			}
		}
		for _, in := range incoming.UserIdGroupPairs {
			for _, ex := range e.UserIdGroupPairs {
				if ex.GroupId == in.GroupId {
					return true
				}
			}
		}
	}
	return false
}

// One rule row per source: IPv4 CIDR, IPv6 CIDR, prefix list and referenced
// group. The standalone aws_vpc_security_group_*_rule resources read each back
// by SecurityGroupRuleId, so a missing row recreates the rule every plan.
func createSecurityGroupRules(groupId string, perm EC2IpPermission, isEgress bool) []EC2SecurityGroupRule {
	sg, _ := ec2SecurityGroups.Get(groupId)
	base := EC2SecurityGroupRule{
		GroupId:    groupId,
		GroupOwner: sg.OwnerId,
		IsEgress:   isEgress,
		IpProtocol: perm.IpProtocol,
		FromPort:   perm.FromPort,
		ToPort:     perm.ToPort,
	}
	var rules []EC2SecurityGroupRule
	add := func(mut func(*EC2SecurityGroupRule)) {
		rule := base
		rule.RuleId = ec2ID("sgr")
		mut(&rule)
		ec2SecurityGroupRules.Put(rule.RuleId, rule)
		rules = append(rules, rule)
	}
	for _, ipr := range perm.IpRanges {
		add(func(r *EC2SecurityGroupRule) { r.CidrIpv4 = ipr.CidrIp; r.Description = ipr.Description })
	}
	for _, ipr := range perm.Ipv6Ranges {
		add(func(r *EC2SecurityGroupRule) { r.CidrIpv6 = ipr.CidrIpv6; r.Description = ipr.Description })
	}
	for _, pl := range perm.PrefixListIds {
		add(func(r *EC2SecurityGroupRule) { r.PrefixListId = pl.PrefixListId; r.Description = pl.Description })
	}
	for _, gp := range perm.UserIdGroupPairs {
		add(func(r *EC2SecurityGroupRule) { r.RefGroupId = gp.GroupId; r.Description = gp.Description })
	}
	return rules
}

// Removes the rule rows matching a revoked permission: same group, direction,
// protocol and ports, and one of its sources.
func deleteSecurityGroupRules(groupId string, perm EC2IpPermission, isEgress bool) {
	for _, rule := range ec2SecurityGroupRules.List() {
		if rule.GroupId != groupId || rule.IsEgress != isEgress ||
			rule.IpProtocol != perm.IpProtocol || rule.FromPort != perm.FromPort || rule.ToPort != perm.ToPort {
			continue
		}
		matched := false
		switch {
		case rule.CidrIpv4 != "":
			matched = ec2IPRangeHasCidr(perm.IpRanges, rule.CidrIpv4)
		case rule.CidrIpv6 != "":
			for _, ipr := range perm.Ipv6Ranges {
				if ipr.CidrIpv6 == rule.CidrIpv6 {
					matched = true
				}
			}
		case rule.PrefixListId != "":
			for _, pl := range perm.PrefixListIds {
				if pl.PrefixListId == rule.PrefixListId {
					matched = true
				}
			}
		case rule.RefGroupId != "":
			for _, gp := range perm.UserIdGroupPairs {
				if gp.GroupId == rule.RefGroupId {
					matched = true
				}
			}
		}
		if matched {
			ec2SecurityGroupRules.Delete(rule.RuleId)
		}
	}
}

func ec2IPRangeHasCidr(ranges []EC2IpRange, cidr string) bool {
	for _, r := range ranges {
		if r.CidrIp == cidr {
			return true
		}
	}
	return false
}

// Code is the AWS error code callers see.
type ec2SGValidationError struct {
	Code    string
	Message string
}

// Mirrors the rejection at the AuthorizeSecurityGroup{Ingress,Egress}
// boundary. An empty Code means well-formed.
func validateIpPermission(perm EC2IpPermission) ec2SGValidationError {
	proto := strings.ToLower(strings.TrimSpace(perm.IpProtocol))
	switch proto {
	case "-1", "all":
	case "tcp", "udp":
		if perm.FromPort < 0 || perm.FromPort > 65535 || perm.ToPort < 0 || perm.ToPort > 65535 {
			return ec2SGValidationError{Code: "InvalidPortRange.Malformed",
				Message: fmt.Sprintf("Invalid port range (%d-%d) for protocol %s; ports must be 0-65535", perm.FromPort, perm.ToPort, proto)}
		}
		if perm.FromPort != 0 && perm.ToPort != 0 && perm.FromPort > perm.ToPort {
			return ec2SGValidationError{Code: "InvalidPortRange.Malformed",
				Message: fmt.Sprintf("Invalid port range (%d-%d) for protocol %s; fromPort must be less than or equal to toPort", perm.FromPort, perm.ToPort, proto)}
		}
	case "icmp", "icmpv6":
		// ICMP values encode type and code: -1 (all), or 0-255 each.
		for _, p := range []int{perm.FromPort, perm.ToPort} {
			if p != -1 && (p < 0 || p > 255) {
				return ec2SGValidationError{Code: "InvalidPortRange.Malformed",
					Message: fmt.Sprintf("Invalid ICMP type/code value %d for protocol %s; must be -1 or 0-255", p, proto)}
			}
		}
	default:
		return ec2SGValidationError{Code: "InvalidPermission.Malformed",
			Message: fmt.Sprintf("Unsupported protocol %q; supported: -1, tcp, udp, icmp, icmpv6", perm.IpProtocol)}
	}

	for _, ipr := range perm.IpRanges {
		if _, _, err := net.ParseCIDR(ipr.CidrIp); err != nil {
			return ec2SGValidationError{Code: "InvalidPermission.Malformed",
				Message: fmt.Sprintf("Invalid CIDR %q: %v", ipr.CidrIp, err)}
		}
	}
	for _, ipr := range perm.Ipv6Ranges {
		if _, _, err := net.ParseCIDR(ipr.CidrIpv6); err != nil {
			return ec2SGValidationError{Code: "InvalidPermission.Malformed",
				Message: fmt.Sprintf("Invalid IPv6 CIDR %q: %v", ipr.CidrIpv6, err)}
		}
	}
	for _, gp := range perm.UserIdGroupPairs {
		if gp.GroupId == "" {
			return ec2SGValidationError{Code: "InvalidGroup.NotFound",
				Message: "UserIdGroupPairs.GroupId is required"}
		}
		if _, ok := ec2SecurityGroups.Get(gp.GroupId); !ok {
			return ec2SGValidationError{Code: "InvalidGroup.NotFound",
				Message: fmt.Sprintf("The security group %q does not exist", gp.GroupId)}
		}
	}
	return ec2SGValidationError{}
}

func handleAuthorizeSecurityGroupIngress(w http.ResponseWriter, r *http.Request) {
	handleAuthorizeSecurityGroupRule(w, r, false)
}

func handleAuthorizeSecurityGroupEgress(w http.ResponseWriter, r *http.Request) {
	handleAuthorizeSecurityGroupRule(w, r, true)
}

func handleAuthorizeSecurityGroupRule(w http.ResponseWriter, r *http.Request, egress bool) {
	groupId := r.FormValue("GroupId")
	if _, ok := ec2SecurityGroups.Get(groupId); !ok {
		ec2ErrorXML(w, "InvalidGroup.NotFound", fmt.Sprintf("The security group '%s' does not exist", groupId), http.StatusBadRequest)
		return
	}
	perm := parseIpPermission(r, "IpPermissions.1")

	if verr := validateIpPermission(perm); verr.Code != "" {
		ec2ErrorXML(w, verr.Code, verr.Message, http.StatusBadRequest)
		return
	}

	var existing []EC2IpPermission
	if egress {
		if sg, ok := ec2SecurityGroups.Get(groupId); ok {
			existing = sg.IpPermissionsEgress
		}
	} else {
		if sg, ok := ec2SecurityGroups.Get(groupId); ok {
			existing = sg.IpPermissions
		}
	}
	if ec2PermissionDuplicate(existing, perm) {
		ec2ErrorXML(w, "InvalidPermission.Duplicate",
			"the specified rule already exists", http.StatusBadRequest)
		return
	}

	var vpcID string
	ec2SecurityGroups.Update(groupId, func(sg *EC2SecurityGroup) {
		vpcID = sg.VpcId
		if egress {
			sg.IpPermissionsEgress = append(sg.IpPermissionsEgress, perm)
		} else {
			sg.IpPermissions = append(sg.IpPermissions, perm)
		}
	})
	if !egress {
		ec2ReapplyIngress(vpcID)
	}

	rules := createSecurityGroupRules(groupId, perm, egress)
	var ruleSetXML strings.Builder
	for _, rule := range rules {
		ruleSetXML.WriteString(sgrItemXML(rule))
	}

	action := "AuthorizeSecurityGroupIngress"
	if egress {
		action = "AuthorizeSecurityGroupEgress"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s>
  <requestId>%s</requestId><return>true</return>
  <securityGroupRuleSet>%s</securityGroupRuleSet>
</%sResponse>`, action, ec2Xmlns(), sim.NewUUID(), ruleSetXML.String(), action)
}

// A CIDR, prefix list or security group reference.
func ec2PermissionHasAnySource(p EC2IpPermission) bool {
	return len(p.IpRanges) > 0 || len(p.Ipv6Ranges) > 0 || len(p.PrefixListIds) > 0 || len(p.UserIdGroupPairs) > 0
}

// Drops only the revoked source, or the whole permission when it was the last;
// match on protocol, ports and one of the four source types.
func ec2RemoveRuleSource(perms []EC2IpPermission, rule EC2SecurityGroupRule) []EC2IpPermission {
	var result []EC2IpPermission
	for _, p := range perms {
		if p.IpProtocol != rule.IpProtocol || p.FromPort != rule.FromPort || p.ToPort != rule.ToPort {
			result = append(result, p)
			continue
		}
		var newPerm EC2IpPermission
		newPerm.IpProtocol = p.IpProtocol
		newPerm.FromPort = p.FromPort
		newPerm.ToPort = p.ToPort
		removed := false
		for _, r := range p.IpRanges {
			if rule.CidrIpv4 != "" && r.CidrIp == rule.CidrIpv4 {
				removed = true
				continue
			}
			newPerm.IpRanges = append(newPerm.IpRanges, r)
		}
		for _, r := range p.Ipv6Ranges {
			if rule.CidrIpv6 != "" && r.CidrIpv6 == rule.CidrIpv6 {
				removed = true
				continue
			}
			newPerm.Ipv6Ranges = append(newPerm.Ipv6Ranges, r)
		}
		for _, r := range p.PrefixListIds {
			if rule.PrefixListId != "" && r.PrefixListId == rule.PrefixListId {
				removed = true
				continue
			}
			newPerm.PrefixListIds = append(newPerm.PrefixListIds, r)
		}
		for _, r := range p.UserIdGroupPairs {
			if rule.RefGroupId != "" && r.GroupId == rule.RefGroupId {
				removed = true
				continue
			}
			newPerm.UserIdGroupPairs = append(newPerm.UserIdGroupPairs, r)
		}
		switch {
		case removed && ec2PermissionHasAnySource(newPerm):
			result = append(result, newPerm)
		case !removed:
			result = append(result, p)
		}
	}
	return result
}

// Unknown IDs are ignored, as AWS's revoke-by-id is idempotent; IDs of another
// group or direction are ignored too, so the call cannot touch another group.
func ec2RevokeByRuleIDs(groupId string, ruleIDs []string, isEgress bool) {
	var vpcID string
	ec2SecurityGroups.Update(groupId, func(sg *EC2SecurityGroup) {
		vpcID = sg.VpcId
		for _, ruleID := range ruleIDs {
			rule, ok := ec2SecurityGroupRules.Get(ruleID)
			if !ok || rule.GroupId != groupId || rule.IsEgress != isEgress {
				continue
			}
			ec2SecurityGroupRules.Delete(ruleID)
			if isEgress {
				sg.IpPermissionsEgress = ec2RemoveRuleSource(sg.IpPermissionsEgress, rule)
			} else {
				sg.IpPermissions = ec2RemoveRuleSource(sg.IpPermissions, rule)
			}
		}
	})
	if !isEgress {
		ec2ReapplyIngress(vpcID)
	}
}

// Revokes by specification or by rule ID; the latter ignores missing IDs.
func ec2RevokeSecurityGroup(w http.ResponseWriter, r *http.Request, isEgress bool) {
	groupId := r.FormValue("GroupId")

	responseTag := "RevokeSecurityGroupIngressResponse"
	if isEgress {
		responseTag = "RevokeSecurityGroupEgressResponse"
	}

	ruleIDs := ec2ParamList(r, "SecurityGroupRuleId")
	if len(ruleIDs) > 0 {
		ec2RevokeByRuleIDs(groupId, ruleIDs, isEgress)
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<%s %s>
  <requestId>%s</requestId><return>true</return>
</%s>`, responseTag, ec2Xmlns(), sim.NewUUID(), responseTag)
		return
	}

	perm := parseIpPermission(r, "IpPermissions.1")

	var found bool
	var vpcID string
	ec2SecurityGroups.Update(groupId, func(sg *EC2SecurityGroup) {
		vpcID = sg.VpcId
		if isEgress {
			found = ec2PermissionExists(sg.IpPermissionsEgress, perm)
			if found {
				sg.IpPermissionsEgress = removePermission(sg.IpPermissionsEgress, perm)
			}
		} else {
			found = ec2PermissionExists(sg.IpPermissions, perm)
			if found {
				sg.IpPermissions = removePermission(sg.IpPermissions, perm)
			}
		}
	})
	if found && !isEgress {
		ec2ReapplyIngress(vpcID)
	}
	if !found {
		ec2ErrorXML(w, "InvalidPermission.NotFound",
			"The specified rule does not exist in this security group.",
			http.StatusBadRequest)
		return
	}
	deleteSecurityGroupRules(groupId, perm, isEgress)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%s %s>
  <requestId>%s</requestId><return>true</return>
</%s>`, responseTag, ec2Xmlns(), sim.NewUUID(), responseTag)
}

func handleRevokeSecurityGroupIngress(w http.ResponseWriter, r *http.Request) {
	ec2RevokeSecurityGroup(w, r, false)
}

func handleRevokeSecurityGroupEgress(w http.ResponseWriter, r *http.Request) {
	ec2RevokeSecurityGroup(w, r, true)
}

func handleDescribeSecurityGroupRules(w http.ResponseWriter, r *http.Request) {
	var ruleIds []string
	for i := 1; ; i++ {
		id := r.FormValue(fmt.Sprintf("SecurityGroupRuleId.%d", i))
		if id == "" {
			break
		}
		ruleIds = append(ruleIds, id)
	}

	// Apply the documented filters.
	filters := ec2Filters(r)

	var rules []EC2SecurityGroupRule
	if len(ruleIds) > 0 {
		for _, id := range ruleIds {
			if rule, ok := ec2SecurityGroupRules.Get(id); ok {
				rules = append(rules, rule)
			}
		}
	} else {
		for _, rule := range ec2SecurityGroupRules.List() {
			if ec2SecurityGroupRuleMatchesFilters(rule, filters) {
				rules = append(rules, rule)
			}
		}
	}

	var items strings.Builder
	for _, rule := range rules {
		items.WriteString(sgrItemXML(rule))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSecurityGroupRulesResponse %s>
  <requestId>%s</requestId>
  <securityGroupRuleSet>%s</securityGroupRuleSet>
</DescribeSecurityGroupRulesResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

// group-id, security-group-rule-id, is-egress, group-owner-id, cidr,
// description, tag:<key> and tag-key.
func ec2SecurityGroupRuleMatchesFilters(rule EC2SecurityGroupRule, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "group-id":
			if !ec2StrInValues(rule.GroupId, vals) {
				return false
			}
		case "security-group-rule-id":
			if !ec2StrInValues(rule.RuleId, vals) {
				return false
			}
		case "is-egress":
			if ec2StrInValues("true", vals) != rule.IsEgress {
				return false
			}
		case "group-owner-id":
			if !ec2StrInValues(rule.GroupOwner, vals) {
				return false
			}
		case "cidr":
			if !ec2StrInValues(rule.CidrIpv4, vals) && !ec2StrInValues(rule.CidrIpv6, vals) {
				return false
			}
		case "description":
			if !ec2StrInValues(rule.Description, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, rule.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

// terraform-provider-aws v6 uses this for an in-place rule change instead of
// revoke and authorize.
func handleModifySecurityGroupRules(w http.ResponseWriter, r *http.Request) {
	for i := 1; ; i++ {
		base := fmt.Sprintf("SecurityGroupRule.%d", i)
		ruleID := r.FormValue(base + ".SecurityGroupRuleId")
		if ruleID == "" {
			break
		}
		rule, ok := ec2SecurityGroupRules.Get(ruleID)
		if !ok {
			ec2ErrorXML(w, "InvalidSecurityGroupRuleId.NotFound",
				fmt.Sprintf("The security group rule ID %q does not exist", ruleID), http.StatusBadRequest)
			return
		}
		sr := base + ".SecurityGroupRule"
		if v := r.FormValue(sr + ".Description"); v != "" {
			rule.Description = v
		}
		if v := r.FormValue(sr + ".IpProtocol"); v != "" {
			rule.IpProtocol = v
		}
		if v := r.FormValue(sr + ".FromPort"); v != "" {
			if _, err := fmt.Sscanf(v, "%d", &rule.FromPort); err != nil {
				rule.FromPort = 0
			}
		}
		if v := r.FormValue(sr + ".ToPort"); v != "" {
			if _, err := fmt.Sscanf(v, "%d", &rule.ToPort); err != nil {
				rule.ToPort = 0
			}
		}
		if v := r.FormValue(sr + ".CidrIpv4"); v != "" {
			rule.CidrIpv4 = v
		}
		if v := r.FormValue(sr + ".ReferencedGroupId"); v != "" {
			rule.RefGroupId = v
		}
		ec2SecurityGroupRules.Put(ruleID, rule)
	}
	ec2WriteSimpleResponse(w, "ModifySecurityGroupRulesResponse")
}

// The legacy aws_security_group inline-block path.
func updateSecurityGroupRuleDescriptions(groupId string, perm EC2IpPermission, isEgress bool) {
	descFor := func(rule EC2SecurityGroupRule) (string, bool) {
		switch {
		case rule.CidrIpv4 != "":
			for _, ipr := range perm.IpRanges {
				if ipr.CidrIp == rule.CidrIpv4 {
					return ipr.Description, true
				}
			}
		case rule.CidrIpv6 != "":
			for _, ipr := range perm.Ipv6Ranges {
				if ipr.CidrIpv6 == rule.CidrIpv6 {
					return ipr.Description, true
				}
			}
		case rule.RefGroupId != "":
			for _, gp := range perm.UserIdGroupPairs {
				if gp.GroupId == rule.RefGroupId {
					return gp.Description, true
				}
			}
		}
		return "", false
	}
	for _, rule := range ec2SecurityGroupRules.List() {
		if rule.GroupId != groupId || rule.IsEgress != isEgress ||
			rule.IpProtocol != perm.IpProtocol || rule.FromPort != perm.FromPort || rule.ToPort != perm.ToPort {
			continue
		}
		if desc, ok := descFor(rule); ok {
			rule.Description = desc
			ec2SecurityGroupRules.Put(rule.RuleId, rule)
		}
	}
}

func handleUpdateSecurityGroupRuleDescriptionsIngress(w http.ResponseWriter, r *http.Request) {
	groupId := r.FormValue("GroupId")
	updateSecurityGroupRuleDescriptions(groupId, parseIpPermission(r, "IpPermissions.1"), false)
	ec2WriteSimpleResponse(w, "UpdateSecurityGroupRuleDescriptionsIngressResponse")
}

func handleUpdateSecurityGroupRuleDescriptionsEgress(w http.ResponseWriter, r *http.Request) {
	groupId := r.FormValue("GroupId")
	updateSecurityGroupRuleDescriptions(groupId, parseIpPermission(r, "IpPermissions.1"), true)
	ec2WriteSimpleResponse(w, "UpdateSecurityGroupRuleDescriptionsEgressResponse")
}

// Match protocol, ports and the source set: two :443 rules from different
// CIDRs are different rules.
func removePermission(perms []EC2IpPermission, target EC2IpPermission) []EC2IpPermission {
	targetKey := permSourceKey(target)
	var result []EC2IpPermission
	for _, p := range perms {
		if p.IpProtocol == target.IpProtocol && p.FromPort == target.FromPort &&
			p.ToPort == target.ToPort && permSourceKey(p) == targetKey {
			continue
		}
		result = append(result, p)
	}
	return result
}

// The revoke handlers answer InvalidPermission.NotFound for a missing rule, as
// AWS does in non-default VPCs.
func ec2PermissionExists(perms []EC2IpPermission, target EC2IpPermission) bool {
	targetKey := permSourceKey(target)
	for _, p := range perms {
		if p.IpProtocol == target.IpProtocol && p.FromPort == target.FromPort &&
			p.ToPort == target.ToPort && permSourceKey(p) == targetKey {
			return true
		}
	}
	return false
}

// Order-independent, so a revoke matches the exact authorized rule.
func permSourceKey(p EC2IpPermission) string {
	var parts []string
	for _, r := range p.IpRanges {
		parts = append(parts, "v4:"+r.CidrIp)
	}
	for _, r := range p.Ipv6Ranges {
		parts = append(parts, "v6:"+r.CidrIpv6)
	}
	for _, r := range p.PrefixListIds {
		parts = append(parts, "pl:"+r.PrefixListId)
	}
	for _, g := range p.UserIdGroupPairs {
		parts = append(parts, "sg:"+g.GroupId)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
