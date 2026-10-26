package main

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleCreateVpc(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("CidrBlock")
	requestedEncryptionMode, requestedExclusions, hasRequestedEncryption, err := vpcEncryptionConfigurationFromCreateRequest(r)
	if err != nil {
		ec2ErrorXML(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return
	}
	tenancy := r.FormValue("InstanceTenancy")
	if tenancy == "" {
		tenancy = "default"
	}
	tags := parseTags(r)
	id := ec2ID("vpc")

	vpc := EC2Vpc{
		VpcId:              id,
		CidrBlock:          cidr,
		State:              "available",
		Tags:               tags,
		OwnerId:            ec2Owner(),
		IsDefault:          false,
		InstanceTenancy:    tenancy,
		EnableDnsSupport:   true,
		EnableDnsHostnames: false,
	}
	ec2Vpcs.Put(id, vpc)
	if r.FormValue("AmazonProvidedIpv6CidrBlock") == "true" {
		ec2AssociateVpcIpv6Block(id, ec2AmazonProvidedIpv6Block())
	}
	ec2CreateDefaultSecurityGroup(id)
	accountControl := currentAccountVpcEncryptionControl()
	switch accountControl.Mode {
	case "attempt-monitor":
		applyAccountVpcEncryptionControl(id, "monitor", accountControl.Exclusions)
	case "attempt-enforce":
		applyAccountVpcEncryptionControl(id, "enforce", accountControl.Exclusions)
	default:
		if hasRequestedEncryption {
			applyAccountVpcEncryptionControl(id, requestedEncryptionMode, requestedExclusions)
		}
	}
	// Real AWS auto-creates a main route table per VPC (local route + a main
	// association with no subnet). aws_vpc.main_route_table_id /
	// default_route_table_id and aws_default_route_table read it back.
	mainRTID := ec2ID("rtb")
	ec2RouteTables.Put(mainRTID, EC2RouteTable{
		RouteTableId: mainRTID,
		VpcId:        id,
		Routes: []EC2Route{{
			DestinationCidrBlock: cidr,
			GatewayId:            "local",
			State:                "active",
			Origin:               "CreateRouteTable",
		}},
		OwnerId: ec2Owner(),
		Associations: []EC2RouteTableAssociation{{
			AssociationId: ec2ID("rtbassoc"),
			RouteTableId:  mainRTID,
			Main:          true,
		}},
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpcResponse %s>
  <requestId>%s</requestId>
  <vpc>%s</vpc>
</CreateVpcResponse>`, ec2Xmlns(), sim.NewUUID(), vpcItemBodyXML(vpc))
}

// vpcItemBodyXML renders the inner <vpc>/<item> fields shared by CreateVpc and
// DescribeVpcs. dhcpOptionsId is always "default" (real AWS associates the
// account's default DHCP option set on create).
func vpcItemBodyXML(vpc EC2Vpc) string {
	// Real DescribeVpcs lists the primary CIDR in cidrBlockAssociationSet as
	// well as in cidrBlock; data.aws_vpc reads cidr_block_associations from it.
	// The sim synthesizes a stable association id from the VPC id.
	assocID := "vpc-cidr-assoc-" + strings.TrimPrefix(vpc.VpcId, "vpc-")
	var cidrAssoc strings.Builder
	fmt.Fprintf(&cidrAssoc, `<cidrBlockAssociationSet><item><associationId>%s</associationId><cidrBlock>%s</cidrBlock><cidrBlockState><state>associated</state></cidrBlockState></item>`,
		assocID, vpc.CidrBlock)
	for _, assoc := range ec2VpcIpv4Assocs(vpc.VpcId) {
		fmt.Fprintf(&cidrAssoc, `<item><associationId>%s</associationId><cidrBlock>%s</cidrBlock><cidrBlockState><state>%s</state></cidrBlockState></item>`,
			assoc.AssociationId, assoc.CidrBlock, assoc.State)
	}
	cidrAssoc.WriteString("</cidrBlockAssociationSet>")
	if ipv6 := ec2VpcIpv6Assocs(vpc.VpcId); len(ipv6) > 0 {
		cidrAssoc.WriteString("<ipv6CidrBlockAssociationSet>")
		for _, assoc := range ipv6 {
			cidrAssoc.WriteString("<item>" + ec2VpcIpv6AssocBodyXML(assoc) + "</item>")
		}
		cidrAssoc.WriteString("</ipv6CidrBlockAssociationSet>")
	}
	tenancy := vpc.InstanceTenancy
	if tenancy == "" {
		tenancy = "default"
	}
	dhcpOptionsID := vpc.DhcpOptionsId
	if dhcpOptionsID == "" {
		dhcpOptionsID = "default"
	}
	encryptionControl := ""
	if control, ok := vpcEncryptionControlForVPC(vpc.VpcId); ok {
		encryptionControl = "<encryptionControl>" + vpcEncryptionControlXML(control) + "</encryptionControl>"
	}
	return fmt.Sprintf(`<vpcId>%s</vpcId><cidrBlock>%s</cidrBlock><state>%s</state><ownerId>%s</ownerId><isDefault>%t</isDefault><instanceTenancy>%s</instanceTenancy><dhcpOptionsId>%s</dhcpOptionsId>%s%s%s`,
		vpc.VpcId, vpc.CidrBlock, vpc.State, vpc.OwnerId, vpc.IsDefault, tenancy, dhcpOptionsID, cidrAssoc.String(), encryptionControl, writeTagSetXML(vpc.Tags))
}

// ec2VpcIpv6AssocBodyXML renders a VPC's IPv6 CIDR block association as
// DescribeVpcs, AssociateVpcCidrBlock and DisassociateVpcCidrBlock list it.
func ec2VpcIpv6AssocBodyXML(assoc EC2VpcCidrAssoc) string {
	return fmt.Sprintf(`<associationId>%s</associationId><ipv6CidrBlock>%s</ipv6CidrBlock><ipv6CidrBlockState><state>%s</state></ipv6CidrBlockState><networkBorderGroup>%s</networkBorderGroup><ipv6Pool>%s</ipv6Pool>`,
		assoc.AssociationId, assoc.Ipv6CidrBlock, assoc.State, assoc.NetworkBorderGroup, assoc.Ipv6Pool)
}

func vpcItemXML(vpc EC2Vpc) string {
	return "<item>" + vpcItemBodyXML(vpc) + "</item>"
}

func handleDescribeVpcs(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpcId")
	for _, id := range ids {
		if _, ok := ec2Vpcs.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)

	var items strings.Builder
	for _, v := range ec2Vpcs.List() {
		if len(ids) > 0 && !ec2StrInValues(v.VpcId, ids) {
			continue
		}
		if !ec2VpcMatchesFilters(v, filters) {
			continue
		}
		items.WriteString(vpcItemXML(v))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcsResponse %s>
  <requestId>%s</requestId>
  <vpcSet>%s</vpcSet>
</DescribeVpcsResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func ec2VpcMatchesFilters(v EC2Vpc, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-id":
			if !ec2StrInValues(v.VpcId, vals) {
				return false
			}
		case "cidr":
			if !ec2StrInValues(v.CidrBlock, vals) {
				return false
			}
		case "cidr-block-association.cidr-block":
			if !slices.ContainsFunc(ec2VpcIpv4Blocks(v), func(block netip.Prefix) bool { return ec2StrInValues(block.String(), vals) }) {
				return false
			}
		case "cidr-block-association.association-id":
			primary := "vpc-cidr-assoc-" + strings.TrimPrefix(v.VpcId, "vpc-")
			if !ec2StrInValues(primary, vals) && !slices.ContainsFunc(ec2VpcIpv4Assocs(v.VpcId), func(assoc EC2VpcCidrAssoc) bool { return ec2StrInValues(assoc.AssociationId, vals) }) {
				return false
			}
		case "cidr-block-association.state":
			if !ec2StrInValues("associated", vals) && !slices.ContainsFunc(ec2VpcIpv4Assocs(v.VpcId), func(assoc EC2VpcCidrAssoc) bool { return ec2StrInValues(assoc.State, vals) }) {
				return false
			}
		case "ipv6-cidr-block-association.ipv6-cidr-block":
			if !slices.ContainsFunc(ec2VpcIpv6Blocks(v.VpcId), func(block netip.Prefix) bool { return ec2StrInValues(block.String(), vals) }) {
				return false
			}
		case "ipv6-cidr-block-association.association-id":
			if !slices.ContainsFunc(ec2VpcIpv6Assocs(v.VpcId), func(assoc EC2VpcCidrAssoc) bool { return ec2StrInValues(assoc.AssociationId, vals) }) {
				return false
			}
		case "ipv6-cidr-block-association.state":
			if !slices.ContainsFunc(ec2VpcIpv6Assocs(v.VpcId), func(assoc EC2VpcCidrAssoc) bool { return ec2StrInValues(assoc.State, vals) }) {
				return false
			}
		case "state":
			if !ec2StrInValues(v.State, vals) {
				return false
			}
		case "is-default", "isDefault":
			if v.IsDefault != ec2StrInValues("true", vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, v.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

// ec2TagFilterMatch evaluates the EC2 tag filter forms (`tag:<Key>` and
// `tag-key`). Returns (handled, match): handled=false means the filter name
// isn't a tag filter and the caller should decide.
func ec2TagFilterMatch(name string, vals []string, tags []EC2Tag) (handled, match bool) {
	switch {
	case strings.HasPrefix(name, "tag:"):
		key := strings.TrimPrefix(name, "tag:")
		for _, t := range tags {
			if t.Key == key && ec2StrInValues(t.Value, vals) {
				return true, true
			}
		}
		return true, false
	case name == "tag-key":
		for _, t := range tags {
			if ec2StrInValues(t.Key, vals) {
				return true, true
			}
		}
		return true, false
	}
	return false, false
}

func handleDeleteVpc(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpcId")
	// A deleted VPC's engine network goes with it; left behind, it would hold
	// the range a re-created VPC with the same CIDR needs.
	if err := ec2RemoveVPCNetwork(r.Context(), id); err != nil {
		ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to delete the VPC's network: %v", err), http.StatusServiceUnavailable)
		return
	}
	for _, rt := range ec2RouteTables.Filter(func(rt EC2RouteTable) bool { return rt.VpcId == id }) {
		ec2RouteTables.Delete(rt.RouteTableId)
	}
	for _, s := range ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == id }) {
		ec2SubnetIPCursor.Delete(s.SubnetId)
	}
	for _, groupID := range ec2DefaultSecurityGroupIDs(id) {
		for _, rule := range ec2SecurityGroupRules.Filter(func(rule EC2SecurityGroupRule) bool { return rule.GroupId == groupID }) {
			ec2SecurityGroupRules.Delete(rule.RuleId)
		}
		ec2SecurityGroups.Delete(groupID)
	}
	for _, control := range ec2VpcEncryptionControls.Filter(func(control EC2VpcEncryptionControl) bool {
		return control.VpcId == id
	}) {
		ec2VpcEncryptionControls.Delete(control.VpcEncryptionControlId)
	}
	for _, assoc := range ec2VpcCidrAssocs.Filter(func(assoc EC2VpcCidrAssoc) bool { return assoc.VpcId == id }) {
		ec2VpcCidrAssocs.Delete(assoc.AssociationId)
	}
	ec2Vpcs.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpcResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteVpcResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleDescribeVpcAttribute(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	attr := r.FormValue("Attribute")
	vpc, _ := ec2Vpcs.Get(vpcId)

	w.Header().Set("Content-Type", "text/xml")
	switch attr {
	case "enableDnsSupport":
		fmt.Fprintf(w, `<DescribeVpcAttributeResponse %s>
  <requestId>%s</requestId><vpcId>%s</vpcId>
  <enableDnsSupport><value>%t</value></enableDnsSupport>
</DescribeVpcAttributeResponse>`, ec2Xmlns(), sim.NewUUID(), vpcId, vpc.EnableDnsSupport)
	case "enableDnsHostnames":
		fmt.Fprintf(w, `<DescribeVpcAttributeResponse %s>
  <requestId>%s</requestId><vpcId>%s</vpcId>
  <enableDnsHostnames><value>%t</value></enableDnsHostnames>
</DescribeVpcAttributeResponse>`, ec2Xmlns(), sim.NewUUID(), vpcId, vpc.EnableDnsHostnames)
	case "enableNetworkAddressUsageMetrics":
		fmt.Fprintf(w, `<DescribeVpcAttributeResponse %s>
  <requestId>%s</requestId><vpcId>%s</vpcId>
  <enableNetworkAddressUsageMetrics><value>%t</value></enableNetworkAddressUsageMetrics>
</DescribeVpcAttributeResponse>`, ec2Xmlns(), sim.NewUUID(), vpcId, vpc.EnableNetworkAddressUsageMetrics)
	default:
		fmt.Fprintf(w, `<DescribeVpcAttributeResponse %s>
  <requestId>%s</requestId><vpcId>%s</vpcId>
</DescribeVpcAttributeResponse>`, ec2Xmlns(), sim.NewUUID(), vpcId)
	}
}

func handleModifyVpcAttribute(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	ec2Vpcs.Update(vpcId, func(v *EC2Vpc) {
		if val := r.FormValue("EnableDnsSupport.Value"); val != "" {
			v.EnableDnsSupport = val == "true"
		}
		if val := r.FormValue("EnableDnsHostnames.Value"); val != "" {
			v.EnableDnsHostnames = val == "true"
		}
		if val := r.FormValue("EnableNetworkAddressUsageMetrics.Value"); val != "" {
			v.EnableNetworkAddressUsageMetrics = val == "true"
		}
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcAttributeResponse %s>
  <requestId>%s</requestId><return>true</return>
</ModifyVpcAttributeResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleCreateSubnet(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	vpc, ok := ec2Vpcs.Get(vpcId)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcId), http.StatusBadRequest)
		return
	}
	// "Default: Amazon Web Services selects one for you."
	az, azID := r.FormValue("AvailabilityZone"), r.FormValue("AvailabilityZoneId")
	if az == "" && azID == "" {
		az = awsAvailabilityZone()
	}
	az, azID, code, message := ec2KnownZone(az, azID)
	if code != "" {
		ec2ErrorXML(w, code, message, http.StatusBadRequest)
		return
	}
	ipv6Native := r.FormValue("Ipv6Native") == "true"
	cidr := r.FormValue("CidrBlock")
	ipv6 := r.FormValue("Ipv6CidrBlock")
	// "For an IPv4 only subnet, specify an IPv4 CIDR block. ... For an IPv6
	// only subnet, specify an IPv6 CIDR block. For a dual stack subnet,
	// specify both."
	switch {
	case ipv6Native && cidr != "":
		ec2ErrorXML(w, "InvalidParameterCombination", "CidrBlock is not supported for an IPv6 only subnet", http.StatusBadRequest)
		return
	case ipv6Native && ipv6 == "":
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Ipv6CidrBlock for an IPv6 only subnet", http.StatusBadRequest)
		return
	case !ipv6Native && cidr == "":
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter CidrBlock", http.StatusBadRequest)
		return
	}
	if cidr != "" {
		if cidr, code, message = ec2SubnetIpv4BlockRefusal(vpc, cidr); code != "" {
			ec2ErrorXML(w, code, message, http.StatusBadRequest)
			return
		}
	}
	if ipv6 != "" {
		if code, message := ec2SubnetIpv6BlockRefusal(vpcId, "", ipv6); code != "" {
			ec2ErrorXML(w, code, message, http.StatusBadRequest)
			return
		}
	}
	tags := parseTags(r)
	id := ec2ID("subnet")

	subnet := EC2Subnet{
		SubnetId:                       id,
		VpcId:                          vpcId,
		CidrBlock:                      cidr,
		Ipv6CidrBlock:                  ipv6,
		AvailabilityZone:               az,
		AvailabilityZoneId:             azID,
		State:                          "available",
		Tags:                           tags,
		OwnerId:                        ec2Owner(),
		Ipv6Native:                     ipv6Native,
		PrivateDnsHostnameTypeOnLaunch: "ip-name",
	}
	ec2Subnets.Put(id, subnet)
	ec2ReconcileRegionalNatGatewaysLogged(r.Context(), vpcId)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSubnetResponse %s>
  <requestId>%s</requestId>
  <subnet>%s</subnet>
</CreateSubnetResponse>`, ec2Xmlns(), sim.NewUUID(), subnetItemBodyXML(subnet))
}

// ec2AvailabilityZoneId synthesizes a stable AZ-id from an AZ name. Real AWS
// AZ-ids (e.g. use1-az1) are randomized per account and not derivable from the
// name, so we emit a deterministic stable value — availability_zone_id is a
// computed attribute, so this does not drift.
func ec2AvailabilityZoneId(az string) string {
	if az == "" {
		return ""
	}
	region := az
	letter := ""
	if n := len(az); n > 0 && az[n-1] >= 'a' && az[n-1] <= 'z' {
		region = az[:n-1]
		letter = string(az[n-1])
	}
	idx := 1
	if letter != "" {
		idx = int(letter[0]-'a') + 1
	}
	return fmt.Sprintf("%s-az%d", strings.ReplaceAll(region, "-", ""), idx)
}

// ec2SubnetAvailableIPs returns the usable host count for a subnet CIDR. AWS
// reserves 5 addresses per subnet (network, VPC router, DNS, future, broadcast).
func ec2SubnetAvailableIPs(cidr string) int {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0
	}
	ones, bits := ipnet.Mask.Size()
	hostBits := bits - ones
	if hostBits <= 0 {
		return 0
	}
	total := 1 << uint(hostBits)
	if total <= 5 {
		return 0
	}
	return total - 5
}

func subnetArn(s EC2Subnet) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:subnet/%s", awsRegion(), s.OwnerId, s.SubnetId)
}

// ec2SubnetIpv6AssociationID names the association of a subnet's one IPv6 CIDR
// block.
func ec2SubnetIpv6AssociationID(subnetID string) string {
	return "subnet-cidr-assoc-" + strings.TrimPrefix(subnetID, "subnet-")
}

// ec2SubnetOfIpv6Association returns the subnet an IPv6 CIDR block association
// names, while that subnet holds the block.
func ec2SubnetOfIpv6Association(assocID string) (string, bool) {
	suffix, ok := strings.CutPrefix(assocID, "subnet-cidr-assoc-")
	if !ok || ec2Subnets == nil {
		return "", false
	}
	subnet, ok := ec2Subnets.Get("subnet-" + suffix)
	if !ok || subnet.Ipv6CidrBlock == "" {
		return "", false
	}
	return subnet.SubnetId, true
}

// subnetItemBodyXML renders the inner subnet fields shared by CreateSubnet and
// DescribeSubnets.
func subnetItemBodyXML(s EC2Subnet) string {
	hostnameType := s.PrivateDnsHostnameTypeOnLaunch
	if hostnameType == "" {
		hostnameType = "ip-name"
	}
	ipv6 := ""
	if s.Ipv6CidrBlock != "" {
		ipv6 = fmt.Sprintf(`<ipv6CidrBlockAssociationSet><item><associationId>%s</associationId><ipv6CidrBlock>%s</ipv6CidrBlock><ipv6CidrBlockState><state>associated</state></ipv6CidrBlockState></item></ipv6CidrBlockAssociationSet>`,
			ec2SubnetIpv6AssociationID(s.SubnetId), s.Ipv6CidrBlock)
	}
	return fmt.Sprintf(`<subnetId>%s</subnetId><vpcId>%s</vpcId><cidrBlock>%s</cidrBlock>`+
		`<availabilityZone>%s</availabilityZone><availabilityZoneId>%s</availabilityZoneId>`+
		`<availableIpAddressCount>%d</availableIpAddressCount><state>%s</state>`+
		`<mapPublicIpOnLaunch>%t</mapPublicIpOnLaunch><mapCustomerOwnedIpOnLaunch>%t</mapCustomerOwnedIpOnLaunch>`+
		`<assignIpv6AddressOnCreation>%t</assignIpv6AddressOnCreation><enableDns64>%t</enableDns64>`+
		`<ipv6Native>%t</ipv6Native><defaultForAz>%t</defaultForAz>`+
		`<privateDnsNameOptionsOnLaunch><hostnameType>%s</hostnameType><enableResourceNameDnsARecord>%t</enableResourceNameDnsARecord><enableResourceNameDnsAAAARecord>%t</enableResourceNameDnsAAAARecord></privateDnsNameOptionsOnLaunch>`+
		`<subnetArn>%s</subnetArn><ownerId>%s</ownerId>%s%s`,
		s.SubnetId, s.VpcId, s.CidrBlock,
		s.AvailabilityZone, s.AvailabilityZoneId,
		ec2SubnetAvailableIPs(s.CidrBlock), s.State,
		s.MapPublicIpOnLaunch, s.MapCustomerOwnedIpOnLaunch,
		s.AssignIpv6AddressOnCreation, s.EnableDns64,
		s.Ipv6Native, s.DefaultForAz,
		hostnameType, s.EnableResourceNameDnsARecordOnLaunch, s.EnableResourceNameDnsAAAARecordOnLaunch,
		subnetArn(s), s.OwnerId, ipv6, writeTagSetXML(s.Tags))
}

func subnetItemXML(s EC2Subnet) string {
	return "<item>" + subnetItemBodyXML(s) + "</item>"
}

func ec2SubnetMatchesFilters(s EC2Subnet, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-id":
			if !ec2StrInValues(s.VpcId, vals) {
				return false
			}
		case "subnet-id":
			if !ec2StrInValues(s.SubnetId, vals) {
				return false
			}
		case "cidr", "cidr-block", "cidrBlock":
			if !ec2StrInValues(s.CidrBlock, vals) {
				return false
			}
		case "availability-zone", "availabilityZone":
			if !ec2StrInValues(s.AvailabilityZone, vals) {
				return false
			}
		case "availability-zone-id":
			if !ec2StrInValues(s.AvailabilityZoneId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(s.State, vals) {
				return false
			}
		case "owner-id":
			if !ec2StrInValues(s.OwnerId, vals) {
				return false
			}
		case "default-for-az", "defaultForAz":
			if !ec2StrInValues(strconv.FormatBool(s.DefaultForAz), vals) {
				return false
			}
		case "ipv6-cidr-block-association.ipv6-cidr-block":
			if s.Ipv6CidrBlock == "" || !ec2StrInValues(s.Ipv6CidrBlock, vals) {
				return false
			}
		case "ipv6-cidr-block-association.association-id":
			if s.Ipv6CidrBlock == "" || !ec2StrInValues(ec2SubnetIpv6AssociationID(s.SubnetId), vals) {
				return false
			}
		case "ipv6-cidr-block-association.state":
			if s.Ipv6CidrBlock == "" || !ec2StrInValues("associated", vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, s.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDescribeSubnets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SubnetId")
	for _, id := range ids {
		if _, ok := ec2Subnets.Get(id); !ok {
			ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)

	var items strings.Builder
	for _, s := range ec2Subnets.List() {
		if len(ids) > 0 && !ec2StrInValues(s.SubnetId, ids) {
			continue
		}
		if !ec2SubnetMatchesFilters(s, filters) {
			continue
		}
		items.WriteString(subnetItemXML(s))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSubnetsResponse %s>
  <requestId>%s</requestId>
  <subnetSet>%s</subnetSet>
</DescribeSubnetsResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func handleModifySubnetAttribute(w http.ResponseWriter, r *http.Request) {
	subnetId := r.FormValue("SubnetId")
	ec2Subnets.Update(subnetId, func(s *EC2Subnet) {
		if val := r.FormValue("MapPublicIpOnLaunch.Value"); val != "" {
			s.MapPublicIpOnLaunch = val == "true"
		}
		if val := r.FormValue("MapCustomerOwnedIpOnLaunch.Value"); val != "" {
			s.MapCustomerOwnedIpOnLaunch = val == "true"
		}
		if val := r.FormValue("AssignIpv6AddressOnCreation.Value"); val != "" {
			s.AssignIpv6AddressOnCreation = val == "true"
		}
		if val := r.FormValue("EnableDns64.Value"); val != "" {
			s.EnableDns64 = val == "true"
		}
		if val := r.FormValue("EnableResourceNameDnsARecordOnLaunch.Value"); val != "" {
			s.EnableResourceNameDnsARecordOnLaunch = val == "true"
		}
		if val := r.FormValue("EnableResourceNameDnsAAAARecordOnLaunch.Value"); val != "" {
			s.EnableResourceNameDnsAAAARecordOnLaunch = val == "true"
		}
		if val := r.FormValue("PrivateDnsHostnameTypeOnLaunch"); val != "" {
			s.PrivateDnsHostnameTypeOnLaunch = val
		}
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifySubnetAttributeResponse %s>
  <requestId>%s</requestId><return>true</return>
</ModifySubnetAttributeResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleDeleteSubnet(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("SubnetId")
	deleted, ok := ec2Subnets.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	for _, networkInterface := range ec2NetworkInterfaces.List() {
		if networkInterface.SubnetId == id {
			ec2ErrorXML(w, "DependencyViolation",
				fmt.Sprintf("The subnet '%s' has dependencies and cannot be deleted.", id),
				http.StatusBadRequest)
			return
		}
	}
	ec2Subnets.Delete(id)
	// Deleting a subnet ends its explicit route table association.
	for _, rt := range ec2RouteTables.List() {
		for _, assoc := range rt.Associations {
			if assoc.SubnetId == id {
				ec2RouteTables.Update(rt.RouteTableId, func(rt *EC2RouteTable) {
					var kept []EC2RouteTableAssociation
					for _, a := range rt.Associations {
						if a.SubnetId != id {
							kept = append(kept, a)
						}
					}
					rt.Associations = kept
				})
				break
			}
		}
	}
	ec2ReconcileRegionalNatGatewaysLogged(r.Context(), deleted.VpcId)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSubnetResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteSubnetResponse>`, ec2Xmlns(), sim.NewUUID())
}
