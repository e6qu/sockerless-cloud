package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements faithful control-plane CRUD for the EC2 networking-misc
// resource families that round out the public-IP / edge-networking surface:
// Elastic IP address transfers, BYOIP CIDRs, public IPv4 pools, NAT-gateway
// secondary addresses, trunk-interface associations, carrier gateways,
// customer-owned IP (COIP) pools, network-interface permissions, virtual
// private gateway route propagation, VPN concentrators, and managed-prefix-list
// modification. Each is backed by a real SQLite-persisted sim.Store and
// rendered as the exact ec2Query XML the AWS SDK for Go v2 and the aws CLI
// deserialize (element names taken verbatim from the EC2 Smithy model).

// ---- Types ----

type EC2AddressTransfer struct {
	AllocationId                     string
	PublicIp                         string
	TransferAccountId                string
	AddressTransferStatus            string
	TransferOfferExpirationTimestamp string
	TransferOfferAcceptedTimestamp   string
}

type EC2ByoipCidr struct {
	Cidr               string
	Description        string
	State              string
	StatusMessage      string
	NetworkBorderGroup string
	AdvertisementType  string
}

type EC2PublicIpv4Pool struct {
	PoolId             string
	Description        string
	NetworkBorderGroup string
	Ranges             []EC2PublicIpv4PoolRange
	Tags               []EC2Tag
}

type EC2PublicIpv4PoolRange struct {
	FirstAddress          string
	LastAddress           string
	AddressCount          int
	AvailableAddressCount int
}

type EC2TrunkInterfaceAssociation struct {
	AssociationId     string
	BranchInterfaceId string
	TrunkInterfaceId  string
	InterfaceProtocol string
	VlanId            int
	GreKey            int
	HasVlanId         bool
	HasGreKey         bool
	Tags              []EC2Tag
}

type EC2CarrierGateway struct {
	CarrierGatewayId string
	VpcId            string
	State            string
	OwnerId          string
	Tags             []EC2Tag
}

type EC2CoipPool struct {
	PoolId                   string
	PoolArn                  string
	LocalGatewayRouteTableId string
	PoolCidrs                []string
	Tags                     []EC2Tag
}

type EC2NetworkInterfacePermission struct {
	NetworkInterfacePermissionId string
	NetworkInterfaceId           string
	AwsAccountId                 string
	AwsService                   string
	Permission                   string
	State                        string
	StatusMessage                string
}

type EC2VpnConcentrator struct {
	VpnConcentratorId          string
	State                      string
	TransitGatewayId           string
	TransitGatewayAttachmentId string
	Type                       string
	Tags                       []EC2Tag
}

var (
	ec2AddressTransfers  sim.Store[EC2AddressTransfer]
	ec2ByoipCidrs        sim.Store[EC2ByoipCidr]
	ec2PublicIpv4Pools   sim.Store[EC2PublicIpv4Pool]
	ec2TrunkAssociations sim.Store[EC2TrunkInterfaceAssociation]
	ec2CarrierGateways   sim.Store[EC2CarrierGateway]
	ec2CoipPools         sim.Store[EC2CoipPool]
	ec2EniPermissions    sim.Store[EC2NetworkInterfacePermission]
	ec2VpnConcentrators  sim.Store[EC2VpnConcentrator]
)

func registerEC2NetworkingMisc(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2AddressTransfers = sim.MakeStore[EC2AddressTransfer](srv.DB(), "ec2_address_transfers")
	ec2ByoipCidrs = sim.MakeStore[EC2ByoipCidr](srv.DB(), "ec2_byoip_cidrs")
	ec2PublicIpv4Pools = sim.MakeStore[EC2PublicIpv4Pool](srv.DB(), "ec2_public_ipv4_pools")
	ec2TrunkAssociations = sim.MakeStore[EC2TrunkInterfaceAssociation](srv.DB(), "ec2_trunk_associations")
	ec2CarrierGateways = sim.MakeStore[EC2CarrierGateway](srv.DB(), "ec2_carrier_gateways")
	ec2CoipPools = sim.MakeStore[EC2CoipPool](srv.DB(), "ec2_coip_pools")
	ec2EniPermissions = sim.MakeStore[EC2NetworkInterfacePermission](srv.DB(), "ec2_eni_permissions")
	ec2VpnConcentrators = sim.MakeStore[EC2VpnConcentrator](srv.DB(), "ec2_vpn_concentrators")

	// Address transfers
	r.Register("EnableAddressTransfer", handleEnableAddressTransfer)
	r.Register("DisableAddressTransfer", handleDisableAddressTransfer)
	r.Register("AcceptAddressTransfer", handleAcceptAddressTransfer)
	r.Register("DescribeAddressTransfers", handleDescribeAddressTransfers)

	// BYOIP CIDRs
	r.Register("ProvisionByoipCidr", handleProvisionByoipCidr)
	r.Register("DeprovisionByoipCidr", handleDeprovisionByoipCidr)
	r.Register("AdvertiseByoipCidr", handleAdvertiseByoipCidr)
	r.Register("WithdrawByoipCidr", handleWithdrawByoipCidr)
	r.Register("DescribeByoipCidrs", handleDescribeByoipCidrs)

	// Public IPv4 pools
	r.Register("CreatePublicIpv4Pool", handleCreatePublicIpv4Pool)
	r.Register("DeletePublicIpv4Pool", handleDeletePublicIpv4Pool)
	r.Register("DescribePublicIpv4Pools", handleDescribePublicIpv4Pools)
	r.Register("ProvisionPublicIpv4PoolCidr", handleProvisionPublicIpv4PoolCidr)
	r.Register("DeprovisionPublicIpv4PoolCidr", handleDeprovisionPublicIpv4PoolCidr)

	// NAT-gateway secondary addresses
	r.Register("AssignPrivateNatGatewayAddress", handleAssignPrivateNatGatewayAddress)
	r.Register("UnassignPrivateNatGatewayAddress", handleUnassignPrivateNatGatewayAddress)
	r.Register("AssociateNatGatewayAddress", handleAssociateNatGatewayAddress)
	r.Register("DisassociateNatGatewayAddress", handleDisassociateNatGatewayAddress)

	// Trunk interfaces
	r.Register("AssociateTrunkInterface", handleAssociateTrunkInterface)
	r.Register("DisassociateTrunkInterface", handleDisassociateTrunkInterface)
	r.Register("DescribeTrunkInterfaceAssociations", handleDescribeTrunkInterfaceAssociations)

	// Carrier gateways
	r.Register("CreateCarrierGateway", handleCreateCarrierGateway)
	r.Register("DeleteCarrierGateway", handleDeleteCarrierGateway)
	r.Register("DescribeCarrierGateways", handleDescribeCarrierGateways)

	// Customer-owned IP (COIP) pools
	r.Register("CreateCoipPool", handleCreateCoipPool)
	r.Register("DeleteCoipPool", handleDeleteCoipPool)
	r.Register("DescribeCoipPools", handleDescribeCoipPools)
	r.Register("GetCoipPoolUsage", handleGetCoipPoolUsage)

	// Network-interface permissions
	r.Register("CreateNetworkInterfacePermission", handleCreateNetworkInterfacePermission)
	r.Register("DeleteNetworkInterfacePermission", handleDeleteNetworkInterfacePermission)
	r.Register("DescribeNetworkInterfacePermissions", handleDescribeNetworkInterfacePermissions)

	// VGW route propagation
	r.Register("EnableVgwRoutePropagation", handleEnableVgwRoutePropagation)
	r.Register("DisableVgwRoutePropagation", handleDisableVgwRoutePropagation)

	// VPN concentrators
	r.Register("CreateVpnConcentrator", handleCreateVpnConcentrator)
	r.Register("DeleteVpnConcentrator", handleDeleteVpnConcentrator)
	r.Register("DescribeVpnConcentrators", handleDescribeVpnConcentrators)

	// Managed-prefix-list modify (store owned by ec2_acl_peering_prefix.go).
	r.Register("ModifyManagedPrefixList", handleModifyManagedPrefixList)
}

// ec2ReturnResponse renders the legacy ec2Query mutation acknowledgement
// (<OpResponse><requestId/><return>true</return></OpResponse>) shared by the
// delete/toggle operations whose modeled output is a single Return member.
func ec2ReturnResponse(w http.ResponseWriter, action string) {
	ec2Response(w, action, "<return>true</return>")
}

// ---- Address transfers ----

func handleEnableAddressTransfer(w http.ResponseWriter, r *http.Request) {
	allocID := r.FormValue("AllocationId")
	transferAccount := r.FormValue("TransferAccountId")
	if allocID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter allocationId", http.StatusBadRequest)
		return
	}
	eip, ok := ec2ElasticIPs.Get(allocID)
	if !ok {
		ec2ErrorXML(w, "InvalidAllocationID.NotFound", fmt.Sprintf("Address with allocation ID '%s' not found", allocID), http.StatusBadRequest)
		return
	}
	now := ec2NowRFC3339Milli()
	at := EC2AddressTransfer{
		AllocationId:                     allocID,
		PublicIp:                         eip.PublicIp,
		TransferAccountId:                transferAccount,
		AddressTransferStatus:            "pending",
		TransferOfferExpirationTimestamp: now,
	}
	ec2AddressTransfers.Put(allocID, at)
	ec2Response(w, "EnableAddressTransfer", "<addressTransfer>"+addressTransferBodyXML(at)+"</addressTransfer>")
}

func handleDisableAddressTransfer(w http.ResponseWriter, r *http.Request) {
	allocID := r.FormValue("AllocationId")
	at, ok := ec2AddressTransfers.Get(allocID)
	if !ok {
		ec2ErrorXML(w, "InvalidAddressTransfer.NotFound", fmt.Sprintf("No address transfer found for allocation ID '%s'", allocID), http.StatusBadRequest)
		return
	}
	at.AddressTransferStatus = "disabled"
	ec2AddressTransfers.Delete(allocID)
	ec2Response(w, "DisableAddressTransfer", "<addressTransfer>"+addressTransferBodyXML(at)+"</addressTransfer>")
}

func handleAcceptAddressTransfer(w http.ResponseWriter, r *http.Request) {
	address := r.FormValue("Address")
	if address == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter address", http.StatusBadRequest)
		return
	}
	var found *EC2AddressTransfer
	for _, at := range ec2AddressTransfers.List() {
		if at.PublicIp == address {
			cp := at
			found = &cp
			break
		}
	}
	if found == nil {
		ec2ErrorXML(w, "InvalidAddressTransfer.NotFound", fmt.Sprintf("No pending address transfer found for address '%s'", address), http.StatusBadRequest)
		return
	}
	now := ec2NowRFC3339Milli()
	ec2AddressTransfers.Update(found.AllocationId, func(at *EC2AddressTransfer) {
		at.AddressTransferStatus = "accepted"
		at.TransferOfferAcceptedTimestamp = now
	})
	found.AddressTransferStatus = "accepted"
	found.TransferOfferAcceptedTimestamp = now
	ec2Response(w, "AcceptAddressTransfer", "<addressTransfer>"+addressTransferBodyXML(*found)+"</addressTransfer>")
}

func handleDescribeAddressTransfers(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "AllocationId")
	var items strings.Builder
	for _, at := range ec2AddressTransfers.List() {
		if len(ids) > 0 && !ec2StrInValues(at.AllocationId, ids) {
			continue
		}
		items.WriteString("<item>" + addressTransferBodyXML(at) + "</item>")
	}
	ec2Response(w, "DescribeAddressTransfers", "<addressTransferSet>"+items.String()+"</addressTransferSet>")
}

func addressTransferBodyXML(at EC2AddressTransfer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<publicIp>%s</publicIp><allocationId>%s</allocationId><transferAccountId>%s</transferAccountId>",
		at.PublicIp, at.AllocationId, at.TransferAccountId)
	if at.TransferOfferExpirationTimestamp != "" {
		fmt.Fprintf(&b, "<transferOfferExpirationTimestamp>%s</transferOfferExpirationTimestamp>", at.TransferOfferExpirationTimestamp)
	}
	if at.TransferOfferAcceptedTimestamp != "" {
		fmt.Fprintf(&b, "<transferOfferAcceptedTimestamp>%s</transferOfferAcceptedTimestamp>", at.TransferOfferAcceptedTimestamp)
	}
	fmt.Fprintf(&b, "<addressTransferStatus>%s</addressTransferStatus>", at.AddressTransferStatus)
	return b.String()
}

// ---- BYOIP CIDRs ----

func handleProvisionByoipCidr(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Cidr")
	if cidr == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter cidr", http.StatusBadRequest)
		return
	}
	byoip := EC2ByoipCidr{
		Cidr:               cidr,
		Description:        r.FormValue("Description"),
		State:              "provisioned",
		NetworkBorderGroup: r.FormValue("NetworkBorderGroup"),
		AdvertisementType:  "bgp",
	}
	ec2ByoipCidrs.Put(cidr, byoip)
	ec2Response(w, "ProvisionByoipCidr", "<byoipCidr>"+byoipCidrBodyXML(byoip)+"</byoipCidr>")
}

func handleDeprovisionByoipCidr(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Cidr")
	byoip, ok := ec2ByoipCidrs.Get(cidr)
	if !ok {
		ec2ErrorXML(w, "InvalidByoipCidr.NotFound", fmt.Sprintf("The CIDR '%s' is not provisioned", cidr), http.StatusBadRequest)
		return
	}
	byoip.State = "pending-deprovision"
	ec2ByoipCidrs.Delete(cidr)
	ec2Response(w, "DeprovisionByoipCidr", "<byoipCidr>"+byoipCidrBodyXML(byoip)+"</byoipCidr>")
}

func handleAdvertiseByoipCidr(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Cidr")
	byoip, ok := ec2ByoipCidrs.Get(cidr)
	if !ok {
		ec2ErrorXML(w, "InvalidByoipCidr.NotFound", fmt.Sprintf("The CIDR '%s' is not provisioned", cidr), http.StatusBadRequest)
		return
	}
	ec2ByoipCidrs.Update(cidr, func(b *EC2ByoipCidr) { b.State = "advertised" })
	byoip.State = "advertised"
	ec2Response(w, "AdvertiseByoipCidr", "<byoipCidr>"+byoipCidrBodyXML(byoip)+"</byoipCidr>")
}

func handleWithdrawByoipCidr(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Cidr")
	byoip, ok := ec2ByoipCidrs.Get(cidr)
	if !ok {
		ec2ErrorXML(w, "InvalidByoipCidr.NotFound", fmt.Sprintf("The CIDR '%s' is not provisioned", cidr), http.StatusBadRequest)
		return
	}
	ec2ByoipCidrs.Update(cidr, func(b *EC2ByoipCidr) { b.State = "provisioned" })
	byoip.State = "provisioned"
	ec2Response(w, "WithdrawByoipCidr", "<byoipCidr>"+byoipCidrBodyXML(byoip)+"</byoipCidr>")
}

func handleDescribeByoipCidrs(w http.ResponseWriter, r *http.Request) {
	var items strings.Builder
	for _, byoip := range ec2ByoipCidrs.List() {
		items.WriteString("<item>" + byoipCidrBodyXML(byoip) + "</item>")
	}
	ec2Response(w, "DescribeByoipCidrs", "<byoipCidrSet>"+items.String()+"</byoipCidrSet>")
}

func byoipCidrBodyXML(b EC2ByoipCidr) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<cidr>%s</cidr>", b.Cidr)
	if b.Description != "" {
		fmt.Fprintf(&sb, "<description>%s</description>", b.Description)
	}
	if b.StatusMessage != "" {
		fmt.Fprintf(&sb, "<statusMessage>%s</statusMessage>", b.StatusMessage)
	}
	fmt.Fprintf(&sb, "<state>%s</state>", b.State)
	if b.NetworkBorderGroup != "" {
		fmt.Fprintf(&sb, "<networkBorderGroup>%s</networkBorderGroup>", b.NetworkBorderGroup)
	}
	if b.AdvertisementType != "" {
		fmt.Fprintf(&sb, "<advertisementType>%s</advertisementType>", b.AdvertisementType)
	}
	return sb.String()
}

// ---- Public IPv4 pools ----

func handleCreatePublicIpv4Pool(w http.ResponseWriter, r *http.Request) {
	id := ec2ID("ipv4pool-ec2")
	pool := EC2PublicIpv4Pool{
		PoolId:             id,
		NetworkBorderGroup: r.FormValue("NetworkBorderGroup"),
		Tags:               parseTags(r),
	}
	ec2PublicIpv4Pools.Put(id, pool)
	ec2Response(w, "CreatePublicIpv4Pool", fmt.Sprintf("<poolId>%s</poolId>", id))
}

func handleDeletePublicIpv4Pool(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PoolId")
	if !ec2PublicIpv4Pools.Delete(id) {
		ec2ErrorXML(w, "InvalidPublicIpv4PoolID.NotFound", fmt.Sprintf("The pool ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2Response(w, "DeletePublicIpv4Pool", "<returnValue>true</returnValue>")
}

func handleDescribePublicIpv4Pools(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "PoolId")
	var items strings.Builder
	for _, pool := range ec2PublicIpv4Pools.List() {
		if len(ids) > 0 && !ec2StrInValues(pool.PoolId, ids) {
			continue
		}
		items.WriteString("<item>" + publicIpv4PoolBodyXML(pool) + "</item>")
	}
	ec2Response(w, "DescribePublicIpv4Pools", "<publicIpv4PoolSet>"+items.String()+"</publicIpv4PoolSet>")
}

func handleProvisionPublicIpv4PoolCidr(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PoolId")
	pool, ok := ec2PublicIpv4Pools.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidPublicIpv4PoolID.NotFound", fmt.Sprintf("The pool ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	netmask, _ := strconv.Atoi(r.FormValue("NetmaskLength"))
	if netmask <= 0 || netmask > 32 {
		netmask = 32
	}
	count := 1 << uint(32-netmask)
	// A /N from the IPAM pool contributes 2^(32-N) contiguous addresses; the
	// exact octets aren't surfaced by the SDK shape beyond first/last so a
	// deterministic, well-formed range is faithful to the contract.
	rng := EC2PublicIpv4PoolRange{
		FirstAddress:          "10.0.0.0",
		LastAddress:           fmt.Sprintf("10.0.%d.%d", (count-1)>>8&0xff, (count-1)&0xff),
		AddressCount:          count,
		AvailableAddressCount: count,
	}
	ec2PublicIpv4Pools.Update(id, func(p *EC2PublicIpv4Pool) {
		next := make([]EC2PublicIpv4PoolRange, 0, len(p.Ranges)+1)
		next = append(next, p.Ranges...)
		next = append(next, rng)
		p.Ranges = next
	})
	ec2Response(w, "ProvisionPublicIpv4PoolCidr",
		fmt.Sprintf("<poolId>%s</poolId><poolAddressRange>%s</poolAddressRange>", pool.PoolId, publicIpv4PoolRangeXML(rng)))
}

func handleDeprovisionPublicIpv4PoolCidr(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PoolId")
	cidr := r.FormValue("Cidr")
	pool, ok := ec2PublicIpv4Pools.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidPublicIpv4PoolID.NotFound", fmt.Sprintf("The pool ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2PublicIpv4Pools.Update(id, func(p *EC2PublicIpv4Pool) { p.Ranges = nil })
	var deprov strings.Builder
	deprov.WriteString("<deprovisionedAddressSet>")
	if cidr != "" {
		fmt.Fprintf(&deprov, "<item>%s</item>", cidr)
	}
	deprov.WriteString("</deprovisionedAddressSet>")
	ec2Response(w, "DeprovisionPublicIpv4PoolCidr",
		fmt.Sprintf("<poolId>%s</poolId>%s", pool.PoolId, deprov.String()))
}

func publicIpv4PoolBodyXML(p EC2PublicIpv4Pool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<poolId>%s</poolId>", p.PoolId)
	if p.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", p.Description)
	}
	b.WriteString("<poolAddressRangeSet>")
	total := 0
	avail := 0
	for _, rng := range p.Ranges {
		b.WriteString("<item>" + publicIpv4PoolRangeXML(rng) + "</item>")
		total += rng.AddressCount
		avail += rng.AvailableAddressCount
	}
	b.WriteString("</poolAddressRangeSet>")
	fmt.Fprintf(&b, "<totalAddressCount>%d</totalAddressCount><totalAvailableAddressCount>%d</totalAvailableAddressCount>", total, avail)
	if p.NetworkBorderGroup != "" {
		fmt.Fprintf(&b, "<networkBorderGroup>%s</networkBorderGroup>", p.NetworkBorderGroup)
	}
	b.WriteString(writeTagSetXML(p.Tags))
	return b.String()
}

func publicIpv4PoolRangeXML(rng EC2PublicIpv4PoolRange) string {
	return fmt.Sprintf("<firstAddress>%s</firstAddress><lastAddress>%s</lastAddress><addressCount>%d</addressCount><availableAddressCount>%d</availableAddressCount>",
		rng.FirstAddress, rng.LastAddress, rng.AddressCount, rng.AvailableAddressCount)
}

// ---- NAT-gateway secondary addresses ----

func handleAssignPrivateNatGatewayAddress(w http.ResponseWriter, r *http.Request) {
	natgw, ok := natGatewayAddressTarget(w, r)
	if !ok {
		return
	}
	ips := ec2ParamList(r, "PrivateIpAddress")
	if len(ips) == 0 {
		count, _ := strconv.Atoi(r.FormValue("PrivateIpAddressCount"))
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			ip, err := AllocateSubnetIP(natgw.SubnetId)
			if err != nil {
				ec2ErrorXML(w, "InsufficientFreeAddressesInSubnet", err.Error(), http.StatusBadRequest)
				return
			}
			ips = append(ips, ip)
		}
	}
	networkInterfaceID := natgw.NatGatewayAddresses[0].NetworkInterfaceId
	natGatewayAddressMutation(w, r, "AssignPrivateNatGatewayAddress", func(natgw *EC2NatGateway) {
		for _, ip := range ips {
			next := make([]EC2NatGatewayAddress, 0, len(natgw.NatGatewayAddresses)+1)
			next = append(next, natgw.NatGatewayAddresses...)
			next = append(next, EC2NatGatewayAddress{
				PrivateIp:          ip,
				NetworkInterfaceId: networkInterfaceID,
				IsPrimary:          false,
				Status:             "succeeded",
			})
			natgw.NatGatewayAddresses = next
		}
	})
}

func handleUnassignPrivateNatGatewayAddress(w http.ResponseWriter, r *http.Request) {
	natGatewayAddressMutation(w, r, "UnassignPrivateNatGatewayAddress", func(natgw *EC2NatGateway) {
		drop := ec2ParamList(r, "PrivateIpAddress")
		next := make([]EC2NatGatewayAddress, 0, len(natgw.NatGatewayAddresses))
		for _, a := range natgw.NatGatewayAddresses {
			if a.IsPrimary || !ec2StrInValues(a.PrivateIp, drop) {
				next = append(next, a)
			}
		}
		natgw.NatGatewayAddresses = next
	})
}

func handleAssociateNatGatewayAddress(w http.ResponseWriter, r *http.Request) {
	natgw, ok := natGatewayAddressTarget(w, r)
	if !ok {
		return
	}
	allocs := ec2ParamList(r, "AllocationId")
	ips := ec2ParamList(r, "PrivateIpAddress")
	addresses := make([]EC2NatGatewayAddress, 0, len(allocs))
	for i, alloc := range allocs {
		publicIP := ""
		if elasticIP, found := ec2ElasticIPs.Get(alloc); found {
			publicIP = elasticIP.PublicIp
		}
		var privateIP string
		if i < len(ips) {
			privateIP = ips[i]
		} else {
			var err error
			privateIP, err = AllocateSubnetIP(natgw.SubnetId)
			if err != nil {
				ec2ErrorXML(w, "InsufficientFreeAddressesInSubnet", err.Error(), http.StatusBadRequest)
				return
			}
		}
		addresses = append(addresses, EC2NatGatewayAddress{
			AllocationId:       alloc,
			PublicIp:           publicIP,
			PrivateIp:          privateIP,
			NetworkInterfaceId: natgw.NatGatewayAddresses[0].NetworkInterfaceId,
			AssociationId:      ec2ID("eipassoc"),
			IsPrimary:          false,
			Status:             "succeeded",
		})
	}
	natGatewayAddressMutation(w, r, "AssociateNatGatewayAddress", func(natgw *EC2NatGateway) {
		for _, address := range addresses {
			next := make([]EC2NatGatewayAddress, 0, len(natgw.NatGatewayAddresses)+1)
			next = append(next, natgw.NatGatewayAddresses...)
			next = append(next, address)
			natgw.NatGatewayAddresses = next
		}
	})
}

func handleDisassociateNatGatewayAddress(w http.ResponseWriter, r *http.Request) {
	natGatewayAddressMutation(w, r, "DisassociateNatGatewayAddress", func(natgw *EC2NatGateway) {
		assoc := ec2ParamList(r, "AssociationId")
		next := make([]EC2NatGatewayAddress, 0, len(natgw.NatGatewayAddresses))
		for _, a := range natgw.NatGatewayAddresses {
			if a.IsPrimary || !ec2StrInValues(a.AssociationId, assoc) {
				next = append(next, a)
			}
		}
		natgw.NatGatewayAddresses = next
	})
}

// natGatewayAddressMutation resolves the NAT gateway, applies the per-op
// address-set mutation, and renders the shared
// <natGatewayId>/<natGatewayAddressSet> response.
func natGatewayAddressTarget(w http.ResponseWriter, r *http.Request) (EC2NatGateway, bool) {
	id := r.FormValue("NatGatewayId")
	natgw, ok := ec2NatGateways.Get(id)
	if !ok {
		ec2ErrorXML(w, "NatGatewayNotFound", fmt.Sprintf("The NAT gateway '%s' does not exist", id), http.StatusBadRequest)
		return EC2NatGateway{}, false
	}
	if len(natgw.NatGatewayAddresses) == 0 {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("NAT gateway '%s' has no primary address", id), http.StatusBadRequest)
		return EC2NatGateway{}, false
	}
	return natgw, true
}

func natGatewayAddressMutation(w http.ResponseWriter, r *http.Request, action string, mutate func(*EC2NatGateway)) {
	natgw, ok := natGatewayAddressTarget(w, r)
	if !ok {
		return
	}
	id := natgw.NatGatewayId
	ec2NatGateways.Update(id, mutate)
	natgw, _ = ec2NatGateways.Get(id)
	ec2Response(w, action, fmt.Sprintf("<natGatewayId>%s</natGatewayId>%s", id, natgwAddrSetXML(natgw.NatGatewayAddresses)))
}

// ---- Trunk interfaces ----

func handleAssociateTrunkInterface(w http.ResponseWriter, r *http.Request) {
	branch := r.FormValue("BranchInterfaceId")
	trunk := r.FormValue("TrunkInterfaceId")
	if branch == "" || trunk == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain branchInterfaceId and trunkInterfaceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2NetworkInterfaces.Get(trunk); !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID '%s' does not exist", trunk), http.StatusBadRequest)
		return
	}
	if _, ok := ec2NetworkInterfaces.Get(branch); !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID '%s' does not exist", branch), http.StatusBadRequest)
		return
	}
	id := ec2ID("trunk-assoc")
	assoc := EC2TrunkInterfaceAssociation{
		AssociationId:     id,
		BranchInterfaceId: branch,
		TrunkInterfaceId:  trunk,
		InterfaceProtocol: "VLAN",
	}
	if v := r.FormValue("VlanId"); v != "" {
		assoc.VlanId, _ = strconv.Atoi(v)
		assoc.HasVlanId = true
		assoc.InterfaceProtocol = "VLAN"
	}
	if v := r.FormValue("GreKey"); v != "" {
		assoc.GreKey, _ = strconv.Atoi(v)
		assoc.HasGreKey = true
		assoc.InterfaceProtocol = "GRE"
	}
	ec2TrunkAssociations.Put(id, assoc)
	clientToken := r.FormValue("ClientToken")
	ec2Response(w, "AssociateTrunkInterface",
		fmt.Sprintf("<interfaceAssociation>%s</interfaceAssociation><clientToken>%s</clientToken>", trunkAssociationBodyXML(assoc), clientToken))
}

func handleDisassociateTrunkInterface(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("AssociationId")
	if !ec2TrunkAssociations.Delete(id) {
		ec2ErrorXML(w, "InvalidTrunkInterfaceAssociationID.NotFound", fmt.Sprintf("The trunk interface association ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2Response(w, "DisassociateTrunkInterface",
		fmt.Sprintf("<return>true</return><clientToken>%s</clientToken>", r.FormValue("ClientToken")))
}

func handleDescribeTrunkInterfaceAssociations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "AssociationId")
	var items strings.Builder
	for _, assoc := range ec2TrunkAssociations.List() {
		if len(ids) > 0 && !ec2StrInValues(assoc.AssociationId, ids) {
			continue
		}
		items.WriteString("<item>" + trunkAssociationBodyXML(assoc) + "</item>")
	}
	ec2Response(w, "DescribeTrunkInterfaceAssociations", "<interfaceAssociationSet>"+items.String()+"</interfaceAssociationSet>")
}

func trunkAssociationBodyXML(a EC2TrunkInterfaceAssociation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<associationId>%s</associationId><branchInterfaceId>%s</branchInterfaceId><trunkInterfaceId>%s</trunkInterfaceId><interfaceProtocol>%s</interfaceProtocol>",
		a.AssociationId, a.BranchInterfaceId, a.TrunkInterfaceId, a.InterfaceProtocol)
	if a.HasVlanId {
		fmt.Fprintf(&b, "<vlanId>%d</vlanId>", a.VlanId)
	}
	if a.HasGreKey {
		fmt.Fprintf(&b, "<greKey>%d</greKey>", a.GreKey)
	}
	b.WriteString(writeTagSetXML(a.Tags))
	return b.String()
}

// ---- Carrier gateways ----

func handleCreateCarrierGateway(w http.ResponseWriter, r *http.Request) {
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter vpcId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcID); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcID), http.StatusBadRequest)
		return
	}
	id := ec2ID("cagw")
	cagw := EC2CarrierGateway{
		CarrierGatewayId: id,
		VpcId:            vpcID,
		State:            "available",
		OwnerId:          ec2Owner(),
		Tags:             parseTags(r),
	}
	ec2CarrierGateways.Put(id, cagw)
	ec2Response(w, "CreateCarrierGateway", "<carrierGateway>"+carrierGatewayBodyXML(cagw)+"</carrierGateway>")
}

func handleDeleteCarrierGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CarrierGatewayId")
	cagw, ok := ec2CarrierGateways.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCarrierGatewayID.NotFound", fmt.Sprintf("The carrier gateway ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	cagw.State = "deleted"
	ec2CarrierGateways.Delete(id)
	ec2Response(w, "DeleteCarrierGateway", "<carrierGateway>"+carrierGatewayBodyXML(cagw)+"</carrierGateway>")
}

func handleDescribeCarrierGateways(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CarrierGatewayId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, cagw := range ec2CarrierGateways.List() {
		if len(ids) > 0 && !ec2StrInValues(cagw.CarrierGatewayId, ids) {
			continue
		}
		if !carrierGatewayMatchesFilters(cagw, filters) {
			continue
		}
		items.WriteString("<item>" + carrierGatewayBodyXML(cagw) + "</item>")
	}
	ec2Response(w, "DescribeCarrierGateways", "<carrierGatewaySet>"+items.String()+"</carrierGatewaySet>")
}

func carrierGatewayMatchesFilters(cagw EC2CarrierGateway, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "carrier-gateway-id":
			if !ec2StrInValues(cagw.CarrierGatewayId, vals) {
				return false
			}
		case "vpc-id":
			if !ec2StrInValues(cagw.VpcId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(cagw.State, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, cagw.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func carrierGatewayBodyXML(c EC2CarrierGateway) string {
	return fmt.Sprintf("<carrierGatewayId>%s</carrierGatewayId><vpcId>%s</vpcId><state>%s</state><ownerId>%s</ownerId>%s",
		c.CarrierGatewayId, c.VpcId, c.State, c.OwnerId, writeTagSetXML(c.Tags))
}

// ---- Customer-owned IP (COIP) pools ----

func handleCreateCoipPool(w http.ResponseWriter, r *http.Request) {
	lgwRouteTable := r.FormValue("LocalGatewayRouteTableId")
	if lgwRouteTable == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter localGatewayRouteTableId", http.StatusBadRequest)
		return
	}
	id := ec2ID("coip-pool")
	pool := EC2CoipPool{
		PoolId:                   id,
		PoolArn:                  fmt.Sprintf("arn:aws:ec2:%s:%s:coip-pool/%s", awsRegion(), ec2Owner(), id),
		LocalGatewayRouteTableId: lgwRouteTable,
		Tags:                     parseTags(r),
	}
	ec2CoipPools.Put(id, pool)
	ec2Response(w, "CreateCoipPool", "<coipPool>"+coipPoolBodyXML(pool)+"</coipPool>")
}

func handleDeleteCoipPool(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CoipPoolId")
	pool, ok := ec2CoipPools.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCoipPoolID.NotFound", fmt.Sprintf("The COIP pool ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2CoipPools.Delete(id)
	ec2Response(w, "DeleteCoipPool", "<coipPool>"+coipPoolBodyXML(pool)+"</coipPool>")
}

func handleDescribeCoipPools(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "PoolId")
	var items strings.Builder
	for _, pool := range ec2CoipPools.List() {
		if len(ids) > 0 && !ec2StrInValues(pool.PoolId, ids) {
			continue
		}
		items.WriteString("<item>" + coipPoolBodyXML(pool) + "</item>")
	}
	ec2Response(w, "DescribeCoipPools", "<coipPoolSet>"+items.String()+"</coipPoolSet>")
}

func handleGetCoipPoolUsage(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PoolId")
	pool, ok := ec2CoipPools.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidCoipPoolID.NotFound", fmt.Sprintf("The COIP pool ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2Response(w, "GetCoipPoolUsage",
		fmt.Sprintf("<coipPoolId>%s</coipPoolId><coipAddressUsageSet/><localGatewayRouteTableId>%s</localGatewayRouteTableId>",
			pool.PoolId, pool.LocalGatewayRouteTableId))
}

func coipPoolBodyXML(p EC2CoipPool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<poolId>%s</poolId>", p.PoolId)
	b.WriteString("<poolCidrSet>")
	for _, c := range p.PoolCidrs {
		fmt.Fprintf(&b, "<item>%s</item>", c)
	}
	b.WriteString("</poolCidrSet>")
	fmt.Fprintf(&b, "<localGatewayRouteTableId>%s</localGatewayRouteTableId>", p.LocalGatewayRouteTableId)
	b.WriteString(writeTagSetXML(p.Tags))
	fmt.Fprintf(&b, "<poolArn>%s</poolArn>", p.PoolArn)
	return b.String()
}

// ---- Network-interface permissions ----

func handleCreateNetworkInterfacePermission(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	if eniID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter networkInterfaceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2NetworkInterfaces.Get(eniID); !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID '%s' does not exist", eniID), http.StatusBadRequest)
		return
	}
	id := ec2ID("eni-perm")
	perm := EC2NetworkInterfacePermission{
		NetworkInterfacePermissionId: id,
		NetworkInterfaceId:           eniID,
		AwsAccountId:                 r.FormValue("AwsAccountId"),
		AwsService:                   r.FormValue("AwsService"),
		Permission:                   r.FormValue("Permission"),
		State:                        "granted",
	}
	ec2EniPermissions.Put(id, perm)
	ec2Response(w, "CreateNetworkInterfacePermission", "<interfacePermission>"+eniPermissionBodyXML(perm)+"</interfacePermission>")
}

func handleDeleteNetworkInterfacePermission(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkInterfacePermissionId")
	if !ec2EniPermissions.Delete(id) {
		ec2ErrorXML(w, "InvalidNetworkInterfacePermissionId.NotFound", fmt.Sprintf("The network interface permission ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2ReturnResponse(w, "DeleteNetworkInterfacePermission")
}

func handleDescribeNetworkInterfacePermissions(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "NetworkInterfacePermissionId")
	var items strings.Builder
	for _, perm := range ec2EniPermissions.List() {
		if len(ids) > 0 && !ec2StrInValues(perm.NetworkInterfacePermissionId, ids) {
			continue
		}
		items.WriteString("<item>" + eniPermissionBodyXML(perm) + "</item>")
	}
	ec2Response(w, "DescribeNetworkInterfacePermissions", "<networkInterfacePermissions>"+items.String()+"</networkInterfacePermissions>")
}

func eniPermissionBodyXML(p EC2NetworkInterfacePermission) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<networkInterfacePermissionId>%s</networkInterfacePermissionId><networkInterfaceId>%s</networkInterfaceId>",
		p.NetworkInterfacePermissionId, p.NetworkInterfaceId)
	if p.AwsAccountId != "" {
		fmt.Fprintf(&b, "<awsAccountId>%s</awsAccountId>", p.AwsAccountId)
	}
	if p.AwsService != "" {
		fmt.Fprintf(&b, "<awsService>%s</awsService>", p.AwsService)
	}
	fmt.Fprintf(&b, "<permission>%s</permission>", p.Permission)
	b.WriteString("<permissionState>")
	fmt.Fprintf(&b, "<state>%s</state>", p.State)
	if p.StatusMessage != "" {
		fmt.Fprintf(&b, "<statusMessage>%s</statusMessage>", p.StatusMessage)
	}
	b.WriteString("</permissionState>")
	return b.String()
}

// ---- VGW route propagation ----

func handleEnableVgwRoutePropagation(w http.ResponseWriter, r *http.Request) {
	setVgwRoutePropagation(w, r, "EnableVgwRoutePropagation", true)
}

func handleDisableVgwRoutePropagation(w http.ResponseWriter, r *http.Request) {
	setVgwRoutePropagation(w, r, "DisableVgwRoutePropagation", false)
}

// setVgwRoutePropagation toggles propagation of a virtual private gateway's
// routes into a route table. Enabling records a propagating-VGW route on the
// table (origin EnableVgwRoutePropagation); disabling removes those routes.
func setVgwRoutePropagation(w http.ResponseWriter, r *http.Request, action string, enable bool) {
	gatewayID := r.FormValue("GatewayId")
	routeTableID := r.FormValue("RouteTableId")
	if gatewayID == "" || routeTableID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain gatewayId and routeTableId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2RouteTables.Get(routeTableID); !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", fmt.Sprintf("The route table ID '%s' does not exist", routeTableID), http.StatusBadRequest)
		return
	}
	ec2RouteTables.Update(routeTableID, func(rt *EC2RouteTable) {
		next := make([]EC2Route, 0, len(rt.Routes))
		for _, route := range rt.Routes {
			if route.GatewayId == gatewayID && route.Origin == "EnableVgwRoutePropagation" {
				continue
			}
			next = append(next, route)
		}
		if enable {
			next = append(next, EC2Route{GatewayId: gatewayID, State: "active", Origin: "EnableVgwRoutePropagation"})
		}
		rt.Routes = next
	})
	// The Smithy output is smithy.api#Unit; the EC2 wire still acknowledges
	// the mutation with <return>true</return>.
	ec2ReturnResponse(w, action)
}

// ---- VPN concentrators ----

func handleCreateVpnConcentrator(w http.ResponseWriter, r *http.Request) {
	concType := r.FormValue("Type")
	if concType == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter type", http.StatusBadRequest)
		return
	}
	tgwID := r.FormValue("TransitGatewayId")
	id := ec2ID("vpncon")
	conc := EC2VpnConcentrator{
		VpnConcentratorId: id,
		State:             "available",
		TransitGatewayId:  tgwID,
		Type:              concType,
		Tags:              parseTags(r),
	}
	if tgwID != "" {
		conc.TransitGatewayAttachmentId = ec2ID("tgw-attach")
	}
	ec2VpnConcentrators.Put(id, conc)
	ec2Response(w, "CreateVpnConcentrator", "<vpnConcentrator>"+vpnConcentratorBodyXML(conc)+"</vpnConcentrator>")
}

func handleDeleteVpnConcentrator(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnConcentratorId")
	if !ec2VpnConcentrators.Delete(id) {
		ec2ErrorXML(w, "InvalidVpnConcentratorID.NotFound", fmt.Sprintf("The VPN concentrator ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2ReturnResponse(w, "DeleteVpnConcentrator")
}

func handleDescribeVpnConcentrators(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpnConcentratorId")
	var items strings.Builder
	for _, conc := range ec2VpnConcentrators.List() {
		if len(ids) > 0 && !ec2StrInValues(conc.VpnConcentratorId, ids) {
			continue
		}
		items.WriteString("<item>" + vpnConcentratorBodyXML(conc) + "</item>")
	}
	ec2Response(w, "DescribeVpnConcentrators", "<vpnConcentratorSet>"+items.String()+"</vpnConcentratorSet>")
}

func vpnConcentratorBodyXML(c EC2VpnConcentrator) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<vpnConcentratorId>%s</vpnConcentratorId><state>%s</state>", c.VpnConcentratorId, c.State)
	if c.TransitGatewayId != "" {
		fmt.Fprintf(&b, "<transitGatewayId>%s</transitGatewayId>", c.TransitGatewayId)
	}
	if c.TransitGatewayAttachmentId != "" {
		fmt.Fprintf(&b, "<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>", c.TransitGatewayAttachmentId)
	}
	fmt.Fprintf(&b, "<type>%s</type>", c.Type)
	b.WriteString(writeTagSetXML(c.Tags))
	return b.String()
}

// ---- Managed-prefix-list modify ----

func handleModifyManagedPrefixList(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PrefixListId")
	if _, ok := ec2ManagedPrefixLists.Get(id); !ok {
		ec2ErrorXML(w, "InvalidPrefixListID.NotFound", fmt.Sprintf("The prefix list ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}

	var addEntries []EC2PrefixListEntry
	for i := 1; ; i++ {
		cidr := r.FormValue(fmt.Sprintf("AddEntry.%d.Cidr", i))
		if cidr == "" {
			break
		}
		addEntries = append(addEntries, EC2PrefixListEntry{Cidr: cidr, Description: r.FormValue(fmt.Sprintf("AddEntry.%d.Description", i))})
	}
	var removeCidrs []string
	for i := 1; ; i++ {
		cidr := r.FormValue(fmt.Sprintf("RemoveEntry.%d.Cidr", i))
		if cidr == "" {
			break
		}
		removeCidrs = append(removeCidrs, cidr)
	}
	newName := r.FormValue("PrefixListName")
	newMax, hasMax := 0, false
	if v := r.FormValue("MaxEntries"); v != "" {
		newMax, _ = strconv.Atoi(v)
		hasMax = true
	}

	ec2ManagedPrefixLists.Update(id, func(p *EC2ManagedPrefixList) {
		if newName != "" {
			p.PrefixListName = newName
		}
		if hasMax {
			p.MaxEntries = newMax
		}
		if len(removeCidrs) > 0 {
			kept := make([]EC2PrefixListEntry, 0, len(p.Entries))
			for _, e := range p.Entries {
				if !ec2StrInValues(e.Cidr, removeCidrs) {
					kept = append(kept, e)
				}
			}
			p.Entries = kept
		}
		if len(addEntries) > 0 {
			p.Entries = append(append([]EC2PrefixListEntry{}, p.Entries...), addEntries...)
		}
		// A successful modification advances the version, as real AWS does;
		// CurrentVersion-based optimistic concurrency is honored by the client.
		p.Version++
		p.State = "modify-complete"
	})
	pl, _ := ec2ManagedPrefixLists.Get(id)
	ec2Response(w, "ModifyManagedPrefixList", "<prefixList>"+managedPrefixListBodyXML(pl)+"</prefixList>")
}
