package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

func handleCreateInternetGateway(w http.ResponseWriter, r *http.Request) {
	tags := parseTags(r)
	id := ec2ID("igw")

	igw := EC2InternetGateway{
		InternetGatewayId: id,
		Tags:              tags,
		OwnerId:           ec2Owner(),
	}
	ec2InternetGateways.Put(id, igw)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateInternetGatewayResponse %s>
  <requestId>%s</requestId>
  <internetGateway>
    <internetGatewayId>%s</internetGatewayId>
    <attachmentSet/>
    <ownerId>%s</ownerId>
    %s
  </internetGateway>
</CreateInternetGatewayResponse>`, ec2Xmlns(), sim.NewUUID(), id, ec2Owner(), writeTagSetXML(tags))
}

func handleAttachInternetGateway(w http.ResponseWriter, r *http.Request) {
	igwId := r.FormValue("InternetGatewayId")
	vpcId := r.FormValue("VpcId")

	ec2InternetGateways.Update(igwId, func(igw *EC2InternetGateway) {
		igw.Attachments = append(igw.Attachments, EC2IGWAttachment{VpcId: vpcId, State: "available"})
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AttachInternetGatewayResponse %s>
  <requestId>%s</requestId><return>true</return>
</AttachInternetGatewayResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleDetachInternetGateway(w http.ResponseWriter, r *http.Request) {
	igwId := r.FormValue("InternetGatewayId")
	vpcId := r.FormValue("VpcId")

	ec2InternetGateways.Update(igwId, func(igw *EC2InternetGateway) {
		var filtered []EC2IGWAttachment
		for _, a := range igw.Attachments {
			if a.VpcId != vpcId {
				filtered = append(filtered, a)
			}
		}
		igw.Attachments = filtered
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DetachInternetGatewayResponse %s>
  <requestId>%s</requestId><return>true</return>
</DetachInternetGatewayResponse>`, ec2Xmlns(), sim.NewUUID())
}

func igwItemXML(igw EC2InternetGateway) string {
	var attachments strings.Builder
	if len(igw.Attachments) == 0 {
		attachments.WriteString("<attachmentSet/>")
	} else {
		attachments.WriteString("<attachmentSet>")
		for _, a := range igw.Attachments {
			fmt.Fprintf(&attachments, "<item><vpcId>%s</vpcId><state>%s</state></item>", a.VpcId, a.State)
		}
		attachments.WriteString("</attachmentSet>")
	}
	return fmt.Sprintf(`<item>
    <internetGatewayId>%s</internetGatewayId>
    %s<ownerId>%s</ownerId>
    %s
  </item>`, igw.InternetGatewayId, attachments.String(), igw.OwnerId, writeTagSetXML(igw.Tags))
}

func handleDescribeInternetGateways(w http.ResponseWriter, r *http.Request) {
	var igws []EC2InternetGateway
	if id := r.FormValue("InternetGatewayId.1"); id != "" {
		if g, ok := ec2InternetGateways.Get(id); ok {
			igws = append(igws, g)
		}
	} else {
		igws = ec2InternetGateways.List()
	}

	var items strings.Builder
	for _, g := range igws {
		items.WriteString(igwItemXML(g))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInternetGatewaysResponse %s>
  <requestId>%s</requestId>
  <internetGatewaySet>%s</internetGatewaySet>
</DescribeInternetGatewaysResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func handleDeleteInternetGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InternetGatewayId")
	ec2InternetGateways.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteInternetGatewayResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteInternetGatewayResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleAllocateAddress(w http.ResponseWriter, r *http.Request) {
	domain := r.FormValue("Domain")
	if domain == "" {
		domain = "vpc"
	}
	tags := parseTags(r)
	id := ec2ID("eipalloc")
	ip, err := ipam.ReserveAWSPublicIPv4(id, nil)
	if err != nil {
		ec2ErrorXML(w, "AddressLimitExceeded", fmt.Sprintf("failed to reserve real public IPv4 lease: %v", err), http.StatusServiceUnavailable)
		return
	}

	pool := r.FormValue("PublicIpv4Pool")
	if pool == "" {
		pool = "amazon"
	}
	nbg := r.FormValue("NetworkBorderGroup")
	if nbg == "" {
		nbg = awsRegion()
	}
	eip := EC2ElasticIP{
		AllocationId:       id,
		PublicIp:           ip.String(),
		Domain:             domain,
		NetworkBorderGroup: nbg,
		PublicIpv4Pool:     pool,
		Tags:               tags,
	}
	ec2ElasticIPs.Put(id, eip)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AllocateAddressResponse %s>
  <requestId>%s</requestId>
  <allocationId>%s</allocationId><publicIp>%s</publicIp><domain>%s</domain>
  <networkBorderGroup>%s</networkBorderGroup><publicIpv4Pool>%s</publicIpv4Pool>
</AllocateAddressResponse>`, ec2Xmlns(), sim.NewUUID(), id, ip.String(), domain, nbg, pool)
}

func handleAssociateAddress(w http.ResponseWriter, r *http.Request) {
	allocId := r.FormValue("AllocationId")
	instanceId := r.FormValue("InstanceId")
	eniId := r.FormValue("NetworkInterfaceId")
	privateIp := r.FormValue("PrivateIpAddress")
	if _, ok := ec2ElasticIPs.Get(allocId); !ok {
		ec2ErrorXML(w, "InvalidAllocationID.NotFound", fmt.Sprintf("The allocation ID '%s' does not exist", allocId), http.StatusBadRequest)
		return
	}
	// When associating to an instance without an explicit private IP, real AWS
	// uses the instance's primary private address — read it back for fidelity.
	// A supplied-but-unknown instance is a real InvalidInstanceID.NotFound, not
	// a silently-accepted association to a non-existent instance.
	if instanceId != "" {
		inst, ok := ec2Instances.Get(instanceId)
		if !ok {
			ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", instanceId), http.StatusBadRequest)
			return
		}
		if privateIp == "" {
			privateIp = inst.PrivateIpAddress
		}
	}
	assocId := ec2ID("eipassoc")
	ec2ElasticIPs.Update(allocId, func(e *EC2ElasticIP) {
		e.AssociationId = assocId
		e.InstanceId = instanceId
		e.NetworkInterfaceId = eniId
		e.PrivateIpAddress = privateIp
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateAddressResponse %s>
  <requestId>%s</requestId><return>true</return><associationId>%s</associationId>
</AssociateAddressResponse>`, ec2Xmlns(), sim.NewUUID(), assocId)
}

func handleDisassociateAddress(w http.ResponseWriter, r *http.Request) {
	assocId := r.FormValue("AssociationId")
	allocId := r.FormValue("AllocationId")
	for _, e := range ec2ElasticIPs.List() {
		if (assocId != "" && e.AssociationId == assocId) || (allocId != "" && e.AllocationId == allocId) {
			ec2ElasticIPs.Update(e.AllocationId, func(e *EC2ElasticIP) {
				e.AssociationId = ""
				e.InstanceId = ""
				e.NetworkInterfaceId = ""
				e.PrivateIpAddress = ""
			})
		}
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateAddressResponse %s>
  <requestId>%s</requestId><return>true</return>
</DisassociateAddressResponse>`, ec2Xmlns(), sim.NewUUID())
}

func eipItemXML(e EC2ElasticIP) string {
	var b strings.Builder
	b.WriteString("<item>")
	fmt.Fprintf(&b, "<allocationId>%s</allocationId><publicIp>%s</publicIp><domain>%s</domain>",
		e.AllocationId, e.PublicIp, e.Domain)
	if e.AssociationId != "" {
		fmt.Fprintf(&b, "<associationId>%s</associationId>", e.AssociationId)
	}
	if e.InstanceId != "" {
		fmt.Fprintf(&b, "<instanceId>%s</instanceId>", e.InstanceId)
	}
	if e.NetworkInterfaceId != "" {
		fmt.Fprintf(&b, "<networkInterfaceId>%s</networkInterfaceId><networkInterfaceOwnerId>%s</networkInterfaceOwnerId>", e.NetworkInterfaceId, ec2Owner())
	}
	if e.PrivateIpAddress != "" {
		fmt.Fprintf(&b, "<privateIpAddress>%s</privateIpAddress>", e.PrivateIpAddress)
	}
	if e.NetworkBorderGroup != "" {
		fmt.Fprintf(&b, "<networkBorderGroup>%s</networkBorderGroup>", e.NetworkBorderGroup)
	}
	if e.PublicIpv4Pool != "" {
		fmt.Fprintf(&b, "<publicIpv4Pool>%s</publicIpv4Pool>", e.PublicIpv4Pool)
	}
	b.WriteString(writeTagSetXML(e.Tags))
	b.WriteString("</item>")
	return b.String()
}

func ec2ElasticIPMatchesFilters(e EC2ElasticIP, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "allocation-id":
			if !ec2StrInValues(e.AllocationId, vals) {
				return false
			}
		case "public-ip":
			if !ec2StrInValues(e.PublicIp, vals) {
				return false
			}
		case "instance-id":
			if !ec2StrInValues(e.InstanceId, vals) {
				return false
			}
		case "network-interface-id":
			if !ec2StrInValues(e.NetworkInterfaceId, vals) {
				return false
			}
		case "association-id":
			if !ec2StrInValues(e.AssociationId, vals) {
				return false
			}
		case "domain":
			if !ec2StrInValues(e.Domain, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, e.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDescribeAddresses(w http.ResponseWriter, r *http.Request) {
	allocIDs := ec2ParamList(r, "AllocationId")
	publicIPs := ec2ParamList(r, "PublicIp")
	filters := ec2Filters(r)

	var items strings.Builder
	for _, e := range ec2ElasticIPs.List() {
		if len(allocIDs) > 0 && !ec2StrInValues(e.AllocationId, allocIDs) {
			continue
		}
		if len(publicIPs) > 0 && !ec2StrInValues(e.PublicIp, publicIPs) {
			continue
		}
		if !ec2ElasticIPMatchesFilters(e, filters) {
			continue
		}
		items.WriteString(eipItemXML(e))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeAddressesResponse %s>
  <requestId>%s</requestId>
  <addressesSet>%s</addressesSet>
</DescribeAddressesResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func handleReleaseAddress(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("AllocationId")
	if eip, ok := ec2ElasticIPs.Get(id); ok {
		ipam.ReleasePublicIPv4(net.ParseIP(eip.PublicIp))
	}
	ec2ElasticIPs.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReleaseAddressResponse %s>
  <requestId>%s</requestId><return>true</return>
</ReleaseAddressResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleDescribeAddressesAttribute(w http.ResponseWriter, r *http.Request) {
	allocId := r.FormValue("AllocationId.1")

	w.Header().Set("Content-Type", "text/xml")
	if allocId != "" {
		fmt.Fprintf(w, `<DescribeAddressesAttributeResponse %s>
  <requestId>%s</requestId>
  <addressSet>
    <item>
      <allocationId>%s</allocationId>
    </item>
  </addressSet>
</DescribeAddressesAttributeResponse>`, ec2Xmlns(), sim.NewUUID(), allocId)
	} else {
		fmt.Fprintf(w, `<DescribeAddressesAttributeResponse %s>
  <requestId>%s</requestId>
  <addressSet/>
</DescribeAddressesAttributeResponse>`, ec2Xmlns(), sim.NewUUID())
	}
}

func handleCreateNatGateway(w http.ResponseWriter, r *http.Request) {
	subnetId := r.FormValue("SubnetId")
	allocId := r.FormValue("AllocationId")
	// connectivity_type is ForceNew in the provider; it defaults to "public"
	// when omitted (matching real AWS) and must round-trip through Describe.
	connectivityType := r.FormValue("ConnectivityType")
	if connectivityType == "" {
		connectivityType = "public"
	}
	tags := parseTags(r)
	id := ec2ID("nat")

	// A missing subnet is a real InvalidSubnetID.NotFound (not the downstream
	// InsufficientFreeAddressesInSubnet that AllocateSubnetIP would otherwise
	// surface), and the NAT gateway's VpcId comes from the subnet.
	s, ok := ec2Subnets.Get(subnetId)
	if !ok {
		ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID '%s' does not exist", subnetId), http.StatusBadRequest)
		return
	}
	vpcId := s.VpcId
	publicIp := ""
	if e, ok := ec2ElasticIPs.Get(allocId); ok {
		publicIp = e.PublicIp
	}
	privateIP, err := AllocateSubnetIP(subnetId)
	if err != nil {
		ec2ErrorXML(w, "InsufficientFreeAddressesInSubnet", fmt.Sprintf("failed to allocate NAT gateway private IP: %v", err), http.StatusBadRequest)
		return
	}
	eniID := ec2ID("eni")

	natgw := EC2NatGateway{
		NatGatewayId:     id,
		SubnetId:         subnetId,
		AllocationId:     allocId,
		VpcId:            vpcId,
		State:            "available",
		ConnectivityType: connectivityType,
		Tags:             tags,
		NatGatewayAddresses: []EC2NatGatewayAddress{{
			AllocationId:       allocId,
			PublicIp:           publicIp,
			PrivateIp:          privateIP,
			NetworkInterfaceId: eniID,
			AssociationId:      ec2ID("eipassoc"),
			IsPrimary:          true,
			Status:             "succeeded",
		}},
		CreateTime: time.Now().UTC().Format(time.RFC3339),
	}
	ec2NatGateways.Put(id, natgw)
	// The gateway's data plane runs on the VPC's network, from now on when
	// the network is up and with the network otherwise, and a subnet already
	// routed to the gateway's id leaves through it.
	if network, ok := ec2VPCNetworkUp(vpcId); ok {
		if err := ec2EnsureNatGatewayEgress(r.Context(), natgw, network); err != nil {
			ec2NatGateways.Delete(id)
			ec2ErrorXML(w, "InternalError", fmt.Sprintf("failed to start the NAT gateway: %v", err), http.StatusInternalServerError)
			return
		}
	}
	ec2ReapplyRoutes(vpcId)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateNatGatewayResponse %s>
  <requestId>%s</requestId>
  <natGateway>
    <natGatewayId>%s</natGatewayId><subnetId>%s</subnetId>
    <vpcId>%s</vpcId><state>available</state>
    <connectivityType>%s</connectivityType>
    %s
    <createTime>%s</createTime>
    %s
  </natGateway>
</CreateNatGatewayResponse>`, ec2Xmlns(), sim.NewUUID(), id, subnetId, vpcId, connectivityType, natgwAddrSetXML(natgw.NatGatewayAddresses), natgw.CreateTime, writeTagSetXML(tags))
}

// natgwAddrSetXML renders the natGatewayAddressSet shared by CreateNatGateway
// and DescribeNatGateways — including associationId, networkInterfaceId,
// isPrimary, and status (the fields aws_nat_gateway reads back as
// association_id / network_interface_id).
func natgwAddrSetXML(addrs []EC2NatGatewayAddress) string {
	var b strings.Builder
	b.WriteString("<natGatewayAddressSet>")
	for _, a := range addrs {
		b.WriteString("<item>")
		fmt.Fprintf(&b, "<allocationId>%s</allocationId><publicIp>%s</publicIp><privateIp>%s</privateIp>",
			a.AllocationId, a.PublicIp, a.PrivateIp)
		if a.NetworkInterfaceId != "" {
			fmt.Fprintf(&b, "<networkInterfaceId>%s</networkInterfaceId>", a.NetworkInterfaceId)
		}
		if a.AssociationId != "" {
			fmt.Fprintf(&b, "<associationId>%s</associationId>", a.AssociationId)
		}
		status := a.Status
		if status == "" {
			status = "succeeded"
		}
		fmt.Fprintf(&b, "<isPrimary>%t</isPrimary><status>%s</status>", a.IsPrimary, status)
		b.WriteString("</item>")
	}
	b.WriteString("</natGatewayAddressSet>")
	return b.String()
}

// ec2NatConnectivityType defaults to "public" for gateways stored before the
// field existed, matching real AWS's default for omitted connectivity_type.
func ec2NatConnectivityType(n EC2NatGateway) string {
	if n.ConnectivityType == "" {
		return "public"
	}
	return n.ConnectivityType
}

func natgwItemXML(n EC2NatGateway) string {
	return fmt.Sprintf(`<item>
    <natGatewayId>%s</natGatewayId><subnetId>%s</subnetId><vpcId>%s</vpcId>
    <state>%s</state><connectivityType>%s</connectivityType>%s<createTime>%s</createTime>
    %s
  </item>`, n.NatGatewayId, n.SubnetId, n.VpcId, n.State, ec2NatConnectivityType(n), natgwAddrSetXML(n.NatGatewayAddresses), n.CreateTime, writeTagSetXML(n.Tags))
}

func handleDescribeNatGateways(w http.ResponseWriter, r *http.Request) {
	var nats []EC2NatGateway
	if id := r.FormValue("NatGatewayId.1"); id != "" {
		if n, ok := ec2NatGateways.Get(id); ok {
			nats = append(nats, n)
		}
	} else {
		filters := ec2Filters(r)
		for _, n := range ec2NatGateways.List() {
			if ec2NatGatewayMatchesFilters(n, filters) {
				nats = append(nats, n)
			}
		}
	}

	var items strings.Builder
	for _, n := range nats {
		items.WriteString(natgwItemXML(n))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeNatGatewaysResponse %s>
  <requestId>%s</requestId>
  <natGatewaySet>%s</natGatewaySet>
</DescribeNatGatewaysResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func handleDeleteNatGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NatGatewayId")
	natgw, _ := ec2NatGateways.Get(id)
	ec2NatGateways.Delete(id)
	ec2RemoveNatGatewayEgress(id)
	ec2ReapplyRoutes(natgw.VpcId)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteNatGatewayResponse %s>
  <requestId>%s</requestId>
  <natGatewayId>%s</natGatewayId>
</DeleteNatGatewayResponse>`, ec2Xmlns(), sim.NewUUID(), id)
}

// ec2NatGatewayMatchesFilters applies the DescribeNatGateways filter set
// (previously only vpc-id was honored).
func ec2NatGatewayMatchesFilters(n EC2NatGateway, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "nat-gateway-id":
			if !ec2StrInValues(n.NatGatewayId, vals) {
				return false
			}
		case "vpc-id":
			if !ec2StrInValues(n.VpcId, vals) {
				return false
			}
		case "subnet-id":
			if !ec2StrInValues(n.SubnetId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(n.State, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, n.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}
