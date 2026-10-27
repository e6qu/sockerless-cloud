package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Secondary IPv4 associations, by association id.
func ec2VpcIpv4Assocs(vpcID string) []EC2VpcCidrAssoc {
	return ec2VpcCidrAssocsOf(vpcID, func(assoc EC2VpcCidrAssoc) bool { return assoc.Ipv6CidrBlock == "" })
}

// By association id.
func ec2VpcIpv6Assocs(vpcID string) []EC2VpcCidrAssoc {
	return ec2VpcCidrAssocsOf(vpcID, func(assoc EC2VpcCidrAssoc) bool { return assoc.Ipv6CidrBlock != "" })
}

func ec2VpcCidrAssocsOf(vpcID string, keep func(EC2VpcCidrAssoc) bool) []EC2VpcCidrAssoc {
	assocs := ec2VpcCidrAssocs.Filter(func(assoc EC2VpcCidrAssoc) bool { return assoc.VpcId == vpcID && keep(assoc) })
	slices.SortFunc(assocs, func(a, b EC2VpcCidrAssoc) int { return strings.Compare(a.AssociationId, b.AssociationId) })
	return assocs
}

// The primary block and every secondary association.
func ec2VpcIpv4Blocks(vpc EC2Vpc) []netip.Prefix {
	var blocks []netip.Prefix
	if block, err := netip.ParsePrefix(vpc.CidrBlock); err == nil {
		blocks = append(blocks, block)
	}
	for _, assoc := range ec2VpcIpv4Assocs(vpc.VpcId) {
		if block, err := netip.ParsePrefix(assoc.CidrBlock); err == nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func ec2VpcIpv6Blocks(vpcID string) []netip.Prefix {
	var blocks []netip.Prefix
	for _, assoc := range ec2VpcIpv6Assocs(vpcID) {
		if block, err := netip.ParsePrefix(assoc.Ipv6CidrBlock); err == nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// Picks "an Amazon-provided IPv6 CIDR block with a /56 prefix length" no VPC
// holds yet.
func ec2AmazonProvidedIpv6Block() string {
	for {
		random := sim.NewUUID()[:6]
		block := fmt.Sprintf("2600:1f00:%s:%s00::/56", random[:4], random[4:])
		held := ec2VpcCidrAssocs.Filter(func(assoc EC2VpcCidrAssoc) bool { return assoc.Ipv6CidrBlock == block })
		if len(held) == 0 {
			return block
		}
	}
}

func ec2AssociateVpcIpv6Block(vpcID, block string) EC2VpcCidrAssoc {
	assoc := EC2VpcCidrAssoc{
		AssociationId:      ec2ID("vpc-cidr-assoc"),
		VpcId:              vpcID,
		Ipv6CidrBlock:      block,
		State:              "associated",
		NetworkBorderGroup: awsRegion(),
		Ipv6Pool:           "Amazon",
	}
	ec2VpcCidrAssocs.Put(assoc.AssociationId, assoc)
	return assoc
}

// Returns the block in canonical form. "The allowed size for an IPv4 subnet is
// between a /28 netmask (16 IP addresses) and a /16 netmask (65,536 IP
// addresses)"; it must lie within the VPC's blocks; and "A subnet CIDR block
// must not overlap the CIDR block of an existing subnet in the VPC."
func ec2SubnetIpv4BlockRefusal(vpc EC2Vpc, cidr string) (canonical, code, message string) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() {
		return "", "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter cidrBlock is invalid. This is not a valid CIDR block.", cidr)
	}
	prefix = prefix.Masked()
	if prefix.Bits() < 16 || prefix.Bits() > 28 {
		return "", "InvalidSubnet.Range", "The CIDR block you've specified for the subnet is not valid. The allowed block size is between a /28 netmask and /16 netmask."
	}
	if !slices.ContainsFunc(ec2VpcIpv4Blocks(vpc), func(block netip.Prefix) bool { return block.Contains(prefix.Addr()) && block.Bits() <= prefix.Bits() }) {
		return "", "InvalidSubnet.Range", fmt.Sprintf("The CIDR '%s' is not within the CIDR blocks of VPC '%s'", prefix, vpc.VpcId)
	}
	for _, other := range ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == vpc.VpcId && s.CidrBlock != "" }) {
		if otherPrefix, err := netip.ParsePrefix(other.CidrBlock); err == nil && otherPrefix.Overlaps(prefix) {
			return "", "InvalidSubnet.Conflict", fmt.Sprintf("The CIDR '%s' conflicts with that of subnet '%s'", prefix, other.SubnetId)
		}
	}
	return prefix.String(), "", ""
}

// A /64 of one of the VPC's IPv6 blocks that no other subnet holds.
func ec2SubnetIpv6BlockRefusal(vpcID, subnetID, block string) (code, message string) {
	prefix, err := netip.ParsePrefix(block)
	if err != nil || !prefix.Addr().Is6() || prefix.Bits() != 64 ||
		!slices.ContainsFunc(ec2VpcIpv6Blocks(vpcID), func(vpcBlock netip.Prefix) bool { return vpcBlock.Contains(prefix.Addr()) }) {
		return "InvalidSubnet.Range", fmt.Sprintf("The IPv6 CIDR block '%s' is not a /64 of an IPv6 CIDR block associated with VPC '%s'", block, vpcID)
	}
	for _, other := range ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == vpcID && s.SubnetId != subnetID && s.Ipv6CidrBlock != "" }) {
		if otherPrefix, err := netip.ParsePrefix(other.Ipv6CidrBlock); err == nil && otherPrefix.Overlaps(prefix) {
			return "InvalidSubnet.Conflict", fmt.Sprintf("The IPv6 CIDR block '%s' conflicts with that of subnet '%s'", block, other.SubnetId)
		}
	}
	return "", ""
}

// "Either AvailabilityZone or AvailabilityZoneId must be specified, but not
// both."
func ec2RequestZone(az, azID string) (zone, zoneID, code, message string) {
	switch {
	case az != "" && azID != "":
		return "", "", "InvalidParameterCombination", "Either AvailabilityZone or AvailabilityZoneId must be specified, but not both"
	case az == "" && azID == "":
		return "", "", "MissingParameter", "Either AvailabilityZone or AvailabilityZoneId must be specified"
	}
	return ec2KnownZone(az, azID)
}

func ec2KnownZone(az, azID string) (zone, zoneID, code, message string) {
	zones := awsAvailabilityZones()
	for _, zone := range zones {
		if zone == az || (az == "" && ec2AvailabilityZoneId(zone) == azID) {
			return zone, ec2AvailabilityZoneId(zone), "", ""
		}
	}
	if az != "" {
		return "", "", "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter availabilityZone is invalid.", az)
	}
	return "", "", "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter availabilityZoneId is invalid.", azID)
}

// A default subnet is "a size /20 IPv4 CIDR block"; take the first free one.
func ec2FreeDefaultSubnetBlock(vpc EC2Vpc) (string, bool) {
	block, err := netip.ParsePrefix(vpc.CidrBlock)
	if err != nil || !block.Addr().Is4() || block.Bits() > 20 {
		return "", false
	}
	held := ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == vpc.VpcId && s.CidrBlock != "" })
	for candidate := netip.PrefixFrom(block.Masked().Addr(), 20); block.Contains(candidate.Addr()); candidate = ec2NextPrefix(candidate) {
		if !slices.ContainsFunc(held, func(s EC2Subnet) bool {
			other, err := netip.ParsePrefix(s.CidrBlock)
			return err == nil && other.Overlaps(candidate)
		}) {
			return candidate.String(), true
		}
	}
	return "", false
}

func ec2NextPrefix(prefix netip.Prefix) netip.Prefix {
	addr := prefix.Masked().Addr().As4()
	next := binary.BigEndian.Uint32(addr[:]) + 1<<(32-prefix.Bits())
	binary.BigEndian.PutUint32(addr[:], next)
	return netip.PrefixFrom(netip.AddrFrom4(addr), prefix.Bits())
}

// "the default subnet for the Availability Zone".
func ec2NewDefaultSubnet(id, vpcID, zone, cidr string, mapPublicIPOnLaunch bool) EC2Subnet {
	return EC2Subnet{
		SubnetId:                       id,
		VpcId:                          vpcID,
		CidrBlock:                      cidr,
		AvailabilityZone:               zone,
		AvailabilityZoneId:             ec2AvailabilityZoneId(zone),
		DefaultForAz:                   true,
		State:                          "available",
		OwnerId:                        ec2Owner(),
		MapPublicIpOnLaunch:            mapPublicIPOnLaunch,
		PrivateDnsHostnameTypeOnLaunch: "ip-name",
	}
}

// "default subnet in each Availability Zone", each the next free /20.
func ec2DefaultVpcSubnets(vpc EC2Vpc) []EC2Subnet {
	var subnets []EC2Subnet
	for _, zone := range awsAvailabilityZones() {
		cidr, ok := ec2FreeDefaultSubnetBlock(vpc)
		if !ok {
			break
		}
		subnet := ec2NewDefaultSubnet(ec2ID("subnet"), vpc.VpcId, zone, cidr, true)
		ec2Subnets.Put(subnet.SubnetId, subnet)
		subnets = append(subnets, subnet)
	}
	return subnets
}
