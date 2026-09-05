package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// This file implements the EC2 Site-to-Site VPN family (customer gateways, VPN
// gateways, VPN connections with two IPsec tunnels) and the Client VPN family
// (Client VPN endpoints, routes, authorization rules, target-network
// associations, connections). Every operation is faithful CRUD on real
// sim.Stores, with ec2Query XML responses matching the EC2 wire shapes the AWS
// SDK Go v2 and aws CLI parse.

// EC2CustomerGateway models a customer gateway: the on-premises side of a
// Site-to-Site VPN connection, identified by a public IP and a BGP ASN.
type EC2CustomerGateway struct {
	CustomerGatewayId string
	BgpAsn            string
	BgpAsnExtended    string
	IpAddress         string
	Type              string
	State             string
	DeviceName        string
	CertificateArn    string
	Tags              []EC2Tag
}

// EC2VpnGateway models a virtual private gateway: the AWS side of a
// Site-to-Site VPN connection, attachable to a VPC.
type EC2VpnGateway struct {
	VpnGatewayId     string
	Type             string
	State            string
	AvailabilityZone string
	AmazonSideAsn    int64
	VpcAttachments   []EC2VpcAttachment
	Tags             []EC2Tag
}

// EC2VpcAttachment records a VPN gateway's attachment to a VPC.
type EC2VpcAttachment struct {
	VpcId string
	State string
}

// EC2VpnTunnel models one of the two IPsec tunnels of a VPN connection.
type EC2VpnTunnel struct {
	OutsideIpAddress string
	TunnelInsideCidr string
	PreSharedKey     string
	Status           string
}

// EC2VpnStaticRoute models a static route programmed onto a VPN connection by
// CreateVpnConnectionRoute (only valid when StaticRoutesOnly is set).
type EC2VpnStaticRoute struct {
	DestinationCidrBlock string
	Source               string
	State                string
}

// EC2VpnConnection models a Site-to-Site VPN connection between a customer
// gateway and a VPN gateway (or transit gateway), with two IPsec tunnels.
type EC2VpnConnection struct {
	VpnConnectionId   string
	CustomerGatewayId string
	VpnGatewayId      string
	TransitGatewayId  string
	Type              string
	State             string
	Category          string
	StaticRoutesOnly  bool
	LocalIpv4Cidr     string
	RemoteIpv4Cidr    string
	Tunnels           []EC2VpnTunnel
	Routes            []EC2VpnStaticRoute
	Tags              []EC2Tag
}

// EC2ClientVpnAuth models one authentication method of a Client VPN endpoint.
type EC2ClientVpnAuth struct {
	Type                       string
	ClientRootCertificateChain string
	DirectoryId                string
	SamlProviderArn            string
	SelfServiceSamlProviderArn string
}

// EC2ClientVpnEndpoint models a Client VPN endpoint: the managed endpoint
// remote clients connect to, with a client CIDR and authentication options.
type EC2ClientVpnEndpoint struct {
	ClientVpnEndpointId  string
	Description          string
	StatusCode           string
	ClientCidrBlock      string
	DnsName              string
	DnsServers           []string
	SplitTunnel          bool
	VpnProtocol          string
	TransportProtocol    string
	VpnPort              int
	ServerCertificateArn string
	SecurityGroupIds     []string
	VpcId                string
	SessionTimeoutHours  int
	SelfServicePortalURL string
	AuthenticationOpts   []EC2ClientVpnAuth
	ConnLogEnabled       bool
	ConnLogGroup         string
	ConnLogStream        string
	CreationTime         string
	Tags                 []EC2Tag
}

// EC2ClientVpnRoute models a route on a Client VPN endpoint.
type EC2ClientVpnRoute struct {
	ClientVpnEndpointId string
	DestinationCidr     string
	TargetSubnet        string
	Type                string
	Origin              string
	StatusCode          string
	Description         string
}

// EC2ClientVpnAuthRule models an authorization (ingress) rule on a Client VPN
// endpoint, granting access to a destination CIDR for a group or for all users.
type EC2ClientVpnAuthRule struct {
	ClientVpnEndpointId string
	Description         string
	GroupId             string
	AccessAll           bool
	DestinationCidr     string
	StatusCode          string
}

// EC2ClientVpnAssoc models a target-network association: the subnet a Client
// VPN endpoint is wired into.
type EC2ClientVpnAssoc struct {
	AssociationId       string
	ClientVpnEndpointId string
	TargetNetworkId     string
	VpcId               string
	SecurityGroups      []string
	StatusCode          string
}

var (
	ec2CustomerGateways  sim.Store[EC2CustomerGateway]
	ec2VpnGateways       sim.Store[EC2VpnGateway]
	ec2VpnConnections    sim.Store[EC2VpnConnection]
	ec2ClientVpnEndpoint sim.Store[EC2ClientVpnEndpoint]
	ec2ClientVpnRoutes   sim.Store[EC2ClientVpnRoute]
	ec2ClientVpnAuth     sim.Store[EC2ClientVpnAuthRule]
	ec2ClientVpnAssocs   sim.Store[EC2ClientVpnAssoc]
)

// registerEC2VPN registers the Site-to-Site VPN and Client VPN ec2Query actions.
func registerEC2VPN(r *AWSQueryRouter, srv *sim.Server) {
	ec2CustomerGateways = sim.MakeStore[EC2CustomerGateway](srv.DB(), "ec2_customer_gateways")
	ec2VpnGateways = sim.MakeStore[EC2VpnGateway](srv.DB(), "ec2_vpn_gateways")
	ec2VpnConnections = sim.MakeStore[EC2VpnConnection](srv.DB(), "ec2_vpn_connections")
	ec2ClientVpnEndpoint = sim.MakeStore[EC2ClientVpnEndpoint](srv.DB(), "ec2_client_vpn_endpoints")
	ec2ClientVpnRoutes = sim.MakeStore[EC2ClientVpnRoute](srv.DB(), "ec2_client_vpn_routes")
	ec2ClientVpnAuth = sim.MakeStore[EC2ClientVpnAuthRule](srv.DB(), "ec2_client_vpn_auth_rules")
	ec2ClientVpnAssocs = sim.MakeStore[EC2ClientVpnAssoc](srv.DB(), "ec2_client_vpn_assocs")

	// Customer gateways
	r.Register("CreateCustomerGateway", handleCreateCustomerGateway)
	r.Register("DescribeCustomerGateways", handleDescribeCustomerGateways)
	r.Register("DeleteCustomerGateway", handleDeleteCustomerGateway)

	// VPN gateways
	r.Register("CreateVpnGateway", handleCreateVpnGateway)
	r.Register("DescribeVpnGateways", handleDescribeVpnGateways)
	r.Register("AttachVpnGateway", handleAttachVpnGateway)
	r.Register("DetachVpnGateway", handleDetachVpnGateway)
	r.Register("DeleteVpnGateway", handleDeleteVpnGateway)

	// VPN connections
	r.Register("CreateVpnConnection", handleCreateVpnConnection)
	r.Register("DescribeVpnConnections", handleDescribeVpnConnections)
	r.Register("ModifyVpnConnection", handleModifyVpnConnection)
	r.Register("ModifyVpnConnectionOptions", handleModifyVpnConnectionOptions)
	r.Register("DeleteVpnConnection", handleDeleteVpnConnection)
	r.Register("CreateVpnConnectionRoute", handleCreateVpnConnectionRoute)
	r.Register("DeleteVpnConnectionRoute", handleDeleteVpnConnectionRoute)
	r.Register("GetVpnConnectionDeviceTypes", handleGetVpnConnectionDeviceTypes)
	r.Register("GetVpnConnectionDeviceSampleConfiguration", handleGetVpnConnectionDeviceSampleConfiguration)
	r.Register("ModifyVpnTunnelOptions", handleModifyVpnTunnelOptions)
	r.Register("GetActiveVpnTunnelStatus", handleGetActiveVpnTunnelStatus)

	// Client VPN
	r.Register("CreateClientVpnEndpoint", handleCreateClientVpnEndpoint)
	r.Register("DescribeClientVpnEndpoints", handleDescribeClientVpnEndpoints)
	r.Register("ModifyClientVpnEndpoint", handleModifyClientVpnEndpoint)
	r.Register("DeleteClientVpnEndpoint", handleDeleteClientVpnEndpoint)
	r.Register("CreateClientVpnRoute", handleCreateClientVpnRoute)
	r.Register("DescribeClientVpnRoutes", handleDescribeClientVpnRoutes)
	r.Register("DeleteClientVpnRoute", handleDeleteClientVpnRoute)
	r.Register("AuthorizeClientVpnIngress", handleAuthorizeClientVpnIngress)
	r.Register("RevokeClientVpnIngress", handleRevokeClientVpnIngress)
	r.Register("DescribeClientVpnAuthorizationRules", handleDescribeClientVpnAuthorizationRules)
	r.Register("AssociateClientVpnTargetNetwork", handleAssociateClientVpnTargetNetwork)
	r.Register("DisassociateClientVpnTargetNetwork", handleDisassociateClientVpnTargetNetwork)
	r.Register("DescribeClientVpnTargetNetworks", handleDescribeClientVpnTargetNetworks)
	r.Register("DescribeClientVpnConnections", handleDescribeClientVpnConnections)
	r.Register("TerminateClientVpnConnections", handleTerminateClientVpnConnections)
	r.Register("ApplySecurityGroupsToClientVpnTargetNetwork", handleApplySecurityGroupsToClientVpnTargetNetwork)
}

func handleCreateCustomerGateway(w http.ResponseWriter, r *http.Request) {
	ip := r.FormValue("IpAddress")
	if ip == "" {
		// Older SDKs/CLIs use PublicIp; AWS accepts both, IpAddress is canonical.
		ip = r.FormValue("PublicIp")
	}
	gwType := r.FormValue("Type")
	if gwType == "" {
		gwType = "ipsec.1"
	}
	bgpAsn := r.FormValue("BgpAsn")
	if bgpAsn == "" {
		bgpAsn = "65000"
	}
	cgw := EC2CustomerGateway{
		CustomerGatewayId: ec2ID("cgw"),
		BgpAsn:            bgpAsn,
		BgpAsnExtended:    r.FormValue("BgpAsnExtended"),
		IpAddress:         ip,
		Type:              gwType,
		State:             "available",
		DeviceName:        r.FormValue("DeviceName"),
		CertificateArn:    r.FormValue("CertificateArn"),
		Tags:              parseTags(r),
	}
	ec2CustomerGateways.Put(cgw.CustomerGatewayId, cgw)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateCustomerGatewayResponse %s><requestId>%s</requestId><customerGateway>%s</customerGateway></CreateCustomerGatewayResponse>`,
		ec2Xmlns(), generateUUID(), ec2CustomerGatewayFieldsXML(cgw))
}

func ec2CustomerGatewayFieldsXML(cgw EC2CustomerGateway) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<customerGatewayId>%s</customerGatewayId><state>%s</state><type>%s</type><ipAddress>%s</ipAddress><bgpAsn>%s</bgpAsn>",
		cgw.CustomerGatewayId, cgw.State, cgw.Type, xmlEscape(cgw.IpAddress), xmlEscape(cgw.BgpAsn))
	if cgw.BgpAsnExtended != "" {
		fmt.Fprintf(&b, "<bgpAsnExtended>%s</bgpAsnExtended>", xmlEscape(cgw.BgpAsnExtended))
	}
	if cgw.DeviceName != "" {
		fmt.Fprintf(&b, "<deviceName>%s</deviceName>", xmlEscape(cgw.DeviceName))
	}
	if cgw.CertificateArn != "" {
		fmt.Fprintf(&b, "<certificateArn>%s</certificateArn>", xmlEscape(cgw.CertificateArn))
	}
	b.WriteString(writeTagSetXML(cgw.Tags))
	return b.String()
}

func handleDescribeCustomerGateways(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "CustomerGatewayId")
	for _, id := range ids {
		if _, ok := ec2CustomerGateways.Get(id); !ok {
			ec2ErrorXML(w, "InvalidCustomerGatewayID.NotFound", fmt.Sprintf("The customer gateway ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	var items strings.Builder
	list := ec2CustomerGateways.List()
	sort.Slice(list, func(i, j int) bool { return list[i].CustomerGatewayId < list[j].CustomerGatewayId })
	for _, cgw := range list {
		if len(ids) > 0 && !ec2StrInValues(cgw.CustomerGatewayId, ids) {
			continue
		}
		if !ec2CustomerGatewayMatchesFilters(cgw, filters) {
			continue
		}
		items.WriteString("<item>")
		items.WriteString(ec2CustomerGatewayFieldsXML(cgw))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeCustomerGatewaysResponse %s><requestId>%s</requestId><customerGatewaySet>%s</customerGatewaySet></DescribeCustomerGatewaysResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2CustomerGatewayMatchesFilters(cgw EC2CustomerGateway, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "customer-gateway-id":
			if !ec2StrInValues(cgw.CustomerGatewayId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(cgw.State, vals) {
				return false
			}
		case "type":
			if !ec2StrInValues(cgw.Type, vals) {
				return false
			}
		case "ip-address":
			if !ec2StrInValues(cgw.IpAddress, vals) {
				return false
			}
		case "bgp-asn":
			if !ec2StrInValues(cgw.BgpAsn, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, cgw.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteCustomerGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CustomerGatewayId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter CustomerGatewayId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2CustomerGateways.Get(id); !ok {
		ec2ErrorXML(w, "InvalidCustomerGatewayID.NotFound", fmt.Sprintf("The customer gateway ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2CustomerGateways.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteCustomerGatewayResponse %s><requestId>%s</requestId><return>true</return></DeleteCustomerGatewayResponse>`, ec2Xmlns(), generateUUID())
}

func handleCreateVpnGateway(w http.ResponseWriter, r *http.Request) {
	gwType := r.FormValue("Type")
	if gwType == "" {
		gwType = "ipsec.1"
	}
	asn := int64(64512)
	if v := r.FormValue("AmazonSideAsn"); v != "" {
		asn = int64(ec2AtoiOr(v, 64512))
	}
	vgw := EC2VpnGateway{
		VpnGatewayId:     ec2ID("vgw"),
		Type:             gwType,
		State:            "available",
		AvailabilityZone: r.FormValue("AvailabilityZone"),
		AmazonSideAsn:    asn,
		Tags:             parseTags(r),
	}
	ec2VpnGateways.Put(vgw.VpnGatewayId, vgw)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpnGatewayResponse %s><requestId>%s</requestId><vpnGateway>%s</vpnGateway></CreateVpnGatewayResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpnGatewayFieldsXML(vgw))
}

func ec2VpnGatewayFieldsXML(vgw EC2VpnGateway) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<vpnGatewayId>%s</vpnGatewayId><state>%s</state><type>%s</type>", vgw.VpnGatewayId, vgw.State, vgw.Type)
	if vgw.AvailabilityZone != "" {
		fmt.Fprintf(&b, "<availabilityZone>%s</availabilityZone>", xmlEscape(vgw.AvailabilityZone))
	}
	b.WriteString("<attachments>")
	for _, a := range vgw.VpcAttachments {
		fmt.Fprintf(&b, "<item><vpcId>%s</vpcId><state>%s</state></item>", a.VpcId, a.State)
	}
	b.WriteString("</attachments>")
	fmt.Fprintf(&b, "<amazonSideAsn>%d</amazonSideAsn>", vgw.AmazonSideAsn)
	b.WriteString(writeTagSetXML(vgw.Tags))
	return b.String()
}

func handleDescribeVpnGateways(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpnGatewayId")
	for _, id := range ids {
		if _, ok := ec2VpnGateways.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVpnGatewayID.NotFound", fmt.Sprintf("The VPN gateway ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	var items strings.Builder
	list := ec2VpnGateways.List()
	sort.Slice(list, func(i, j int) bool { return list[i].VpnGatewayId < list[j].VpnGatewayId })
	for _, vgw := range list {
		if len(ids) > 0 && !ec2StrInValues(vgw.VpnGatewayId, ids) {
			continue
		}
		if !ec2VpnGatewayMatchesFilters(vgw, filters) {
			continue
		}
		items.WriteString("<item>")
		items.WriteString(ec2VpnGatewayFieldsXML(vgw))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpnGatewaysResponse %s><requestId>%s</requestId><vpnGatewaySet>%s</vpnGatewaySet></DescribeVpnGatewaysResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2VpnGatewayMatchesFilters(vgw EC2VpnGateway, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpn-gateway-id":
			if !ec2StrInValues(vgw.VpnGatewayId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(vgw.State, vals) {
				return false
			}
		case "type":
			if !ec2StrInValues(vgw.Type, vals) {
				return false
			}
		case "availability-zone":
			if !ec2StrInValues(vgw.AvailabilityZone, vals) {
				return false
			}
		case "attachment.vpc-id":
			ok := false
			for _, a := range vgw.VpcAttachments {
				if ec2StrInValues(a.VpcId, vals) {
					ok = true
				}
			}
			if !ok {
				return false
			}
		case "attachment.state":
			ok := false
			for _, a := range vgw.VpcAttachments {
				if ec2StrInValues(a.State, vals) {
					ok = true
				}
			}
			if !ok {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, vgw.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleAttachVpnGateway(w http.ResponseWriter, r *http.Request) {
	vgwID := r.FormValue("VpnGatewayId")
	vpcID := r.FormValue("VpcId")
	vgw, ok := ec2VpnGateways.Get(vgwID)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnGatewayID.NotFound", fmt.Sprintf("The VPN gateway ID '%s' does not exist", vgwID), http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcID); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcID), http.StatusBadRequest)
		return
	}
	att := EC2VpcAttachment{VpcId: vpcID, State: "attached"}
	// Replace any existing attachment to the same VPC; a VGW attaches to one VPC.
	updated := false
	for i, a := range vgw.VpcAttachments {
		if a.VpcId == vpcID {
			vgw.VpcAttachments[i] = att
			updated = true
		}
	}
	if !updated {
		vgw.VpcAttachments = append(vgw.VpcAttachments, att)
	}
	ec2VpnGateways.Put(vgwID, vgw)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AttachVpnGatewayResponse %s><requestId>%s</requestId><attachment><vpcId>%s</vpcId><state>%s</state></attachment></AttachVpnGatewayResponse>`,
		ec2Xmlns(), generateUUID(), att.VpcId, att.State)
}

func handleDetachVpnGateway(w http.ResponseWriter, r *http.Request) {
	vgwID := r.FormValue("VpnGatewayId")
	vpcID := r.FormValue("VpcId")
	vgw, ok := ec2VpnGateways.Get(vgwID)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnGatewayID.NotFound", fmt.Sprintf("The VPN gateway ID '%s' does not exist", vgwID), http.StatusBadRequest)
		return
	}
	var remaining []EC2VpcAttachment
	for _, a := range vgw.VpcAttachments {
		if a.VpcId != vpcID {
			remaining = append(remaining, a)
		}
	}
	vgw.VpcAttachments = remaining
	ec2VpnGateways.Put(vgwID, vgw)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DetachVpnGatewayResponse %s><requestId>%s</requestId><return>true</return></DetachVpnGatewayResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteVpnGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnGatewayId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpnGatewayId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2VpnGateways.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpnGatewayID.NotFound", fmt.Sprintf("The VPN gateway ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VpnGateways.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpnGatewayResponse %s><requestId>%s</requestId><return>true</return></DeleteVpnGatewayResponse>`, ec2Xmlns(), generateUUID())
}

func handleCreateVpnConnection(w http.ResponseWriter, r *http.Request) {
	cgwID := r.FormValue("CustomerGatewayId")
	if cgwID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter CustomerGatewayId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2CustomerGateways.Get(cgwID); !ok {
		ec2ErrorXML(w, "InvalidCustomerGatewayID.NotFound", fmt.Sprintf("The customer gateway ID '%s' does not exist", cgwID), http.StatusBadRequest)
		return
	}
	vgwID := r.FormValue("VpnGatewayId")
	tgwID := r.FormValue("TransitGatewayId")
	if vgwID == "" && tgwID == "" {
		ec2ErrorXML(w, "MissingParameter", "Either VpnGatewayId or TransitGatewayId must be specified", http.StatusBadRequest)
		return
	}
	if vgwID != "" {
		if _, ok := ec2VpnGateways.Get(vgwID); !ok {
			ec2ErrorXML(w, "InvalidVpnGatewayID.NotFound", fmt.Sprintf("The VPN gateway ID '%s' does not exist", vgwID), http.StatusBadRequest)
			return
		}
	}
	gwType := r.FormValue("Type")
	if gwType == "" {
		gwType = "ipsec.1"
	}
	static := r.FormValue("Options.StaticRoutesOnly") == "true"
	conn := EC2VpnConnection{
		VpnConnectionId:   ec2ID("vpn"),
		CustomerGatewayId: cgwID,
		VpnGatewayId:      vgwID,
		TransitGatewayId:  tgwID,
		Type:              gwType,
		State:             "available",
		Category:          "VPN",
		StaticRoutesOnly:  static,
		LocalIpv4Cidr:     r.FormValue("Options.LocalIpv4NetworkCidr"),
		RemoteIpv4Cidr:    r.FormValue("Options.RemoteIpv4NetworkCidr"),
		Tunnels: []EC2VpnTunnel{
			{OutsideIpAddress: "203.0.113.1", TunnelInsideCidr: "169.254.10.0/30", PreSharedKey: "sim-psk-tunnel-1", Status: "UP"},
			{OutsideIpAddress: "203.0.113.2", TunnelInsideCidr: "169.254.11.0/30", PreSharedKey: "sim-psk-tunnel-2", Status: "UP"},
		},
		Tags: parseTags(r),
	}
	ec2VpnConnections.Put(conn.VpnConnectionId, conn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpnConnectionResponse %s><requestId>%s</requestId><vpnConnection>%s</vpnConnection></CreateVpnConnectionResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpnConnectionFieldsXML(conn))
}

// ec2VpnConnectionConfig builds the customerGatewayConfiguration payload: a
// self-contained XML document describing the tunnels, returned to the caller as
// an escaped string (matching real EC2, which emits a full IKE/IPsec config).
func ec2VpnConnectionConfig(conn EC2VpnConnection) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><vpn_connection id="`)
	b.WriteString(conn.VpnConnectionId)
	b.WriteString(`"><customer_gateway_id>`)
	b.WriteString(conn.CustomerGatewayId)
	b.WriteString(`</customer_gateway_id>`)
	for _, t := range conn.Tunnels {
		fmt.Fprintf(&b, `<ipsec_tunnel><vpn_gateway><tunnel_outside_address><ip_address>%s</ip_address></tunnel_outside_address></vpn_gateway><ike><pre_shared_key>%s</pre_shared_key></ike></ipsec_tunnel>`,
			t.OutsideIpAddress, t.PreSharedKey)
	}
	b.WriteString(`</vpn_connection>`)
	return b.String()
}

func ec2VpnConnectionFieldsXML(conn EC2VpnConnection) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<vpnConnectionId>%s</vpnConnectionId><state>%s</state>", conn.VpnConnectionId, conn.State)
	fmt.Fprintf(&b, "<customerGatewayConfiguration>%s</customerGatewayConfiguration>", xmlEscape(ec2VpnConnectionConfig(conn)))
	fmt.Fprintf(&b, "<type>%s</type><customerGatewayId>%s</customerGatewayId>", conn.Type, conn.CustomerGatewayId)
	if conn.VpnGatewayId != "" {
		fmt.Fprintf(&b, "<vpnGatewayId>%s</vpnGatewayId>", conn.VpnGatewayId)
	}
	if conn.TransitGatewayId != "" {
		fmt.Fprintf(&b, "<transitGatewayId>%s</transitGatewayId>", conn.TransitGatewayId)
	}
	if conn.Category != "" {
		fmt.Fprintf(&b, "<category>%s</category>", conn.Category)
	}
	// options
	b.WriteString("<options>")
	fmt.Fprintf(&b, "<enableAcceleration>false</enableAcceleration><staticRoutesOnly>%t</staticRoutesOnly>", conn.StaticRoutesOnly)
	if conn.LocalIpv4Cidr != "" {
		fmt.Fprintf(&b, "<localIpv4NetworkCidr>%s</localIpv4NetworkCidr>", xmlEscape(conn.LocalIpv4Cidr))
	}
	if conn.RemoteIpv4Cidr != "" {
		fmt.Fprintf(&b, "<remoteIpv4NetworkCidr>%s</remoteIpv4NetworkCidr>", xmlEscape(conn.RemoteIpv4Cidr))
	}
	b.WriteString("<tunnelInsideIpVersion>ipv4</tunnelInsideIpVersion>")
	b.WriteString("<tunnelOptionSet>")
	for _, t := range conn.Tunnels {
		fmt.Fprintf(&b, "<item><outsideIpAddress>%s</outsideIpAddress><tunnelInsideCidr>%s</tunnelInsideCidr></item>", t.OutsideIpAddress, t.TunnelInsideCidr)
	}
	b.WriteString("</tunnelOptionSet>")
	b.WriteString("</options>")
	// routes
	b.WriteString("<routes>")
	for _, rt := range conn.Routes {
		fmt.Fprintf(&b, "<item><destinationCidrBlock>%s</destinationCidrBlock><source>%s</source><state>%s</state></item>",
			xmlEscape(rt.DestinationCidrBlock), rt.Source, rt.State)
	}
	b.WriteString("</routes>")
	// vgwTelemetry: one entry per tunnel
	b.WriteString("<vgwTelemetry>")
	for _, t := range conn.Tunnels {
		fmt.Fprintf(&b, "<item><outsideIpAddress>%s</outsideIpAddress><status>%s</status><acceptedRouteCount>0</acceptedRouteCount><lastStatusChange>%s</lastStatusChange><statusMessage>%s</statusMessage></item>",
			t.OutsideIpAddress, statusToTelemetry(t.Status), ec2NowRFC3339Milli(), statusToTelemetry(t.Status))
	}
	b.WriteString("</vgwTelemetry>")
	b.WriteString(writeTagSetXML(conn.Tags))
	return b.String()
}

// statusToTelemetry maps a tunnel UP/DOWN status to the vgwTelemetry status enum.
func statusToTelemetry(status string) string {
	if status == "UP" {
		return "UP"
	}
	return "DOWN"
}

func handleDescribeVpnConnections(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpnConnectionId")
	for _, id := range ids {
		if _, ok := ec2VpnConnections.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	var items strings.Builder
	list := ec2VpnConnections.List()
	sort.Slice(list, func(i, j int) bool { return list[i].VpnConnectionId < list[j].VpnConnectionId })
	for _, conn := range list {
		if len(ids) > 0 && !ec2StrInValues(conn.VpnConnectionId, ids) {
			continue
		}
		if !ec2VpnConnectionMatchesFilters(conn, filters) {
			continue
		}
		items.WriteString("<item>")
		items.WriteString(ec2VpnConnectionFieldsXML(conn))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpnConnectionsResponse %s><requestId>%s</requestId><vpnConnectionSet>%s</vpnConnectionSet></DescribeVpnConnectionsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2VpnConnectionMatchesFilters(conn EC2VpnConnection, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpn-connection-id":
			if !ec2StrInValues(conn.VpnConnectionId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(conn.State, vals) {
				return false
			}
		case "type":
			if !ec2StrInValues(conn.Type, vals) {
				return false
			}
		case "customer-gateway-id":
			if !ec2StrInValues(conn.CustomerGatewayId, vals) {
				return false
			}
		case "vpn-gateway-id":
			if !ec2StrInValues(conn.VpnGatewayId, vals) {
				return false
			}
		case "transit-gateway-id":
			if !ec2StrInValues(conn.TransitGatewayId, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, conn.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleModifyVpnConnection(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnConnectionId")
	conn, ok := ec2VpnConnections.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("VpnGatewayId"); v != "" {
		conn.VpnGatewayId = v
		conn.TransitGatewayId = ""
	}
	if v := r.FormValue("TransitGatewayId"); v != "" {
		conn.TransitGatewayId = v
		conn.VpnGatewayId = ""
	}
	if v := r.FormValue("CustomerGatewayId"); v != "" {
		conn.CustomerGatewayId = v
	}
	ec2VpnConnections.Put(id, conn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpnConnectionResponse %s><requestId>%s</requestId><vpnConnection>%s</vpnConnection></ModifyVpnConnectionResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpnConnectionFieldsXML(conn))
}

func handleModifyVpnConnectionOptions(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnConnectionId")
	conn, ok := ec2VpnConnections.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("LocalIpv4NetworkCidr"); v != "" {
		conn.LocalIpv4Cidr = v
	}
	if v := r.FormValue("RemoteIpv4NetworkCidr"); v != "" {
		conn.RemoteIpv4Cidr = v
	}
	ec2VpnConnections.Put(id, conn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpnConnectionOptionsResponse %s><requestId>%s</requestId><vpnConnection>%s</vpnConnection></ModifyVpnConnectionOptionsResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpnConnectionFieldsXML(conn))
}

func handleModifyVpnTunnelOptions(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnConnectionId")
	conn, ok := ec2VpnConnections.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// The targeted tunnel is identified by its outside IP address.
	outside := r.FormValue("VpnTunnelOutsideIpAddress")
	if cidr := r.FormValue("TunnelOptions.TunnelInsideCidr"); cidr != "" {
		for i := range conn.Tunnels {
			if outside == "" || conn.Tunnels[i].OutsideIpAddress == outside {
				conn.Tunnels[i].TunnelInsideCidr = cidr
			}
		}
	}
	if psk := r.FormValue("TunnelOptions.PreSharedKey"); psk != "" {
		for i := range conn.Tunnels {
			if outside == "" || conn.Tunnels[i].OutsideIpAddress == outside {
				conn.Tunnels[i].PreSharedKey = psk
			}
		}
	}
	ec2VpnConnections.Put(id, conn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpnTunnelOptionsResponse %s><requestId>%s</requestId><vpnConnection>%s</vpnConnection></ModifyVpnTunnelOptionsResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpnConnectionFieldsXML(conn))
}

func handleGetActiveVpnTunnelStatus(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnConnectionId")
	if _, ok := ec2VpnConnections.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetActiveVpnTunnelStatusResponse %s><requestId>%s</requestId><activeVpnTunnelStatus><phase1EncryptionAlgorithm>AES256</phase1EncryptionAlgorithm><phase2EncryptionAlgorithm>AES256</phase2EncryptionAlgorithm><phase1IntegrityAlgorithm>SHA2-256</phase1IntegrityAlgorithm><phase2IntegrityAlgorithm>SHA2-256</phase2IntegrityAlgorithm><phase1DHGroup>14</phase1DHGroup><phase2DHGroup>14</phase2DHGroup><ikeVersion>ikev2</ikeVersion><provisioningStatus>provisioned</provisioningStatus></activeVpnTunnelStatus></GetActiveVpnTunnelStatusResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDeleteVpnConnection(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnConnectionId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpnConnectionId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2VpnConnections.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VpnConnections.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpnConnectionResponse %s><requestId>%s</requestId><return>true</return></DeleteVpnConnectionResponse>`, ec2Xmlns(), generateUUID())
}

func handleCreateVpnConnectionRoute(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnConnectionId")
	cidr := r.FormValue("DestinationCidrBlock")
	conn, ok := ec2VpnConnections.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if cidr == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter DestinationCidrBlock", http.StatusBadRequest)
		return
	}
	exists := false
	for _, rt := range conn.Routes {
		if rt.DestinationCidrBlock == cidr {
			exists = true
		}
	}
	if !exists {
		conn.Routes = append(conn.Routes, EC2VpnStaticRoute{DestinationCidrBlock: cidr, Source: "Static", State: "available"})
		ec2VpnConnections.Put(id, conn)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpnConnectionRouteResponse %s><requestId>%s</requestId><return>true</return></CreateVpnConnectionRouteResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteVpnConnectionRoute(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpnConnectionId")
	cidr := r.FormValue("DestinationCidrBlock")
	conn, ok := ec2VpnConnections.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	var remaining []EC2VpnStaticRoute
	for _, rt := range conn.Routes {
		if rt.DestinationCidrBlock != cidr {
			remaining = append(remaining, rt)
		}
	}
	conn.Routes = remaining
	ec2VpnConnections.Put(id, conn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpnConnectionRouteResponse %s><requestId>%s</requestId><return>true</return></DeleteVpnConnectionRouteResponse>`, ec2Xmlns(), generateUUID())
}

// ec2VpnDeviceTypes is the real-shaped list of supported customer gateway
// devices GetVpnConnectionDeviceTypes returns (vendor / platform / software
// triples, each with a stable device-type id).
var ec2VpnDeviceTypes = []struct {
	ID, Vendor, Platform, Software string
}{
	{"754a6372", "Cisco", "ASA 5500 Series", "ASA 9.7+ VTI"},
	{"234dbcb6", "Cisco", "ISR Series Routers", "IOS 12.4+"},
	{"06bf6f3e", "Juniper", "J-Series Routers", "JunOS 9.5+"},
	{"55a2dc8e", "Fortinet", "Fortigate 40+ Series", "FortiOS 5.6+"},
	{"5fb13b0a", "Palo Alto Networks", "PA Series", "PANOS 7.0+"},
	{"9612a4ee", "Generic", "Generic", "Vendor Agnostic"},
}

func handleGetVpnConnectionDeviceTypes(w http.ResponseWriter, r *http.Request) {
	var items strings.Builder
	for _, d := range ec2VpnDeviceTypes {
		fmt.Fprintf(&items, "<item><vpnConnectionDeviceTypeId>%s</vpnConnectionDeviceTypeId><vendor>%s</vendor><platform>%s</platform><software>%s</software></item>",
			d.ID, xmlEscape(d.Vendor), xmlEscape(d.Platform), xmlEscape(d.Software))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetVpnConnectionDeviceTypesResponse %s><requestId>%s</requestId><vpnConnectionDeviceTypeSet>%s</vpnConnectionDeviceTypeSet></GetVpnConnectionDeviceTypesResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleGetVpnConnectionDeviceSampleConfiguration(w http.ResponseWriter, r *http.Request) {
	connID := r.FormValue("VpnConnectionId")
	conn, ok := ec2VpnConnections.Get(connID)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", connID), http.StatusBadRequest)
		return
	}
	devType := r.FormValue("VpnConnectionDeviceTypeId")
	sample := fmt.Sprintf("! Amazon Web Services\n! Site-to-Site VPN sample configuration\n! VPN Connection: %s\n! Device Type: %s\n%s\n", conn.VpnConnectionId, devType, ec2VpnConnectionConfig(conn))
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetVpnConnectionDeviceSampleConfigurationResponse %s><requestId>%s</requestId><vpnConnectionDeviceSampleConfiguration>%s</vpnConnectionDeviceSampleConfiguration></GetVpnConnectionDeviceSampleConfigurationResponse>`,
		ec2Xmlns(), generateUUID(), xmlEscape(sample))
}

func handleCreateClientVpnEndpoint(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("ClientCidrBlock")
	if cidr == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ClientCidrBlock", http.StatusBadRequest)
		return
	}
	serverCert := r.FormValue("ServerCertificateArn")
	if serverCert == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ServerCertificateArn", http.StatusBadRequest)
		return
	}
	transport := r.FormValue("TransportProtocol")
	if transport == "" {
		transport = "udp"
	}
	port := ec2AtoiOr(r.FormValue("VpnPort"), 443)
	id := "cvpn-endpoint-" + generateUUID()[:17]
	ep := EC2ClientVpnEndpoint{
		ClientVpnEndpointId:  id,
		Description:          r.FormValue("Description"),
		StatusCode:           "available",
		ClientCidrBlock:      cidr,
		DnsName:              "*." + generateUUID()[:8] + ".prod.clientvpn." + awsRegion() + ".amazonaws.com",
		DnsServers:           ec2ParamList(r, "DnsServers"),
		SplitTunnel:          r.FormValue("SplitTunnel") == "true",
		VpnProtocol:          "openvpn",
		TransportProtocol:    transport,
		VpnPort:              port,
		ServerCertificateArn: serverCert,
		SecurityGroupIds:     ec2ParamList(r, "SecurityGroupId"),
		VpcId:                r.FormValue("VpcId"),
		SessionTimeoutHours:  ec2AtoiOr(r.FormValue("SessionTimeoutHours"), 24),
		AuthenticationOpts:   ec2ParseClientVpnAuth(r),
		ConnLogEnabled:       r.FormValue("ConnectionLogOptions.Enabled") == "true",
		ConnLogGroup:         r.FormValue("ConnectionLogOptions.CloudwatchLogGroup"),
		ConnLogStream:        r.FormValue("ConnectionLogOptions.CloudwatchLogStream"),
		CreationTime:         ec2NowRFC3339Milli(),
		Tags:                 parseTags(r),
	}
	ec2ClientVpnEndpoint.Put(id, ep)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateClientVpnEndpointResponse %s><requestId>%s</requestId><clientVpnEndpointId>%s</clientVpnEndpointId><status><code>%s</code></status><dnsName>%s</dnsName></CreateClientVpnEndpointResponse>`,
		ec2Xmlns(), generateUUID(), ep.ClientVpnEndpointId, ep.StatusCode, ep.DnsName)
}

// ec2ParseClientVpnAuth reads the indexed Authentication.N request params.
func ec2ParseClientVpnAuth(r *http.Request) []EC2ClientVpnAuth {
	var auths []EC2ClientVpnAuth
	for i := 1; ; i++ {
		t := r.FormValue(fmt.Sprintf("Authentication.%d.Type", i))
		if t == "" {
			break
		}
		auths = append(auths, EC2ClientVpnAuth{
			Type:                       t,
			ClientRootCertificateChain: r.FormValue(fmt.Sprintf("Authentication.%d.MutualAuthentication.ClientRootCertificateChainArn", i)),
			DirectoryId:                r.FormValue(fmt.Sprintf("Authentication.%d.ActiveDirectory.DirectoryId", i)),
			SamlProviderArn:            r.FormValue(fmt.Sprintf("Authentication.%d.FederatedAuthentication.SAMLProviderArn", i)),
			SelfServiceSamlProviderArn: r.FormValue(fmt.Sprintf("Authentication.%d.FederatedAuthentication.SelfServiceSAMLProviderArn", i)),
		})
	}
	return auths
}

func ec2ClientVpnEndpointFieldsXML(ep EC2ClientVpnEndpoint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<clientVpnEndpointId>%s</clientVpnEndpointId>", ep.ClientVpnEndpointId)
	if ep.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(ep.Description))
	}
	fmt.Fprintf(&b, "<status><code>%s</code></status>", ep.StatusCode)
	if ep.CreationTime != "" {
		fmt.Fprintf(&b, "<creationTime>%s</creationTime>", ep.CreationTime)
	}
	if ep.DnsName != "" {
		fmt.Fprintf(&b, "<dnsName>%s</dnsName>", xmlEscape(ep.DnsName))
	}
	fmt.Fprintf(&b, "<clientCidrBlock>%s</clientCidrBlock>", xmlEscape(ep.ClientCidrBlock))
	if len(ep.DnsServers) > 0 {
		b.WriteString("<dnsServer>")
		for _, d := range ep.DnsServers {
			fmt.Fprintf(&b, "<item>%s</item>", xmlEscape(d))
		}
		b.WriteString("</dnsServer>")
	}
	fmt.Fprintf(&b, "<splitTunnel>%t</splitTunnel>", ep.SplitTunnel)
	fmt.Fprintf(&b, "<vpnProtocol>%s</vpnProtocol><transportProtocol>%s</transportProtocol><vpnPort>%d</vpnPort>", ep.VpnProtocol, ep.TransportProtocol, ep.VpnPort)
	fmt.Fprintf(&b, "<serverCertificateArn>%s</serverCertificateArn>", xmlEscape(ep.ServerCertificateArn))
	// authenticationOptions
	b.WriteString("<authenticationOptions>")
	for _, a := range ep.AuthenticationOpts {
		b.WriteString("<item>")
		fmt.Fprintf(&b, "<type>%s</type>", a.Type)
		if a.ClientRootCertificateChain != "" {
			fmt.Fprintf(&b, "<mutualAuthentication><clientRootCertificateChain>%s</clientRootCertificateChain></mutualAuthentication>", xmlEscape(a.ClientRootCertificateChain))
		}
		if a.DirectoryId != "" {
			fmt.Fprintf(&b, "<activeDirectory><directoryId>%s</directoryId></activeDirectory>", xmlEscape(a.DirectoryId))
		}
		if a.SamlProviderArn != "" {
			fmt.Fprintf(&b, "<federatedAuthentication><samlProviderArn>%s</samlProviderArn></federatedAuthentication>", xmlEscape(a.SamlProviderArn))
		}
		b.WriteString("</item>")
	}
	b.WriteString("</authenticationOptions>")
	fmt.Fprintf(&b, "<connectionLogOptions><Enabled>%t</Enabled>", ep.ConnLogEnabled)
	if ep.ConnLogGroup != "" {
		fmt.Fprintf(&b, "<CloudwatchLogGroup>%s</CloudwatchLogGroup>", xmlEscape(ep.ConnLogGroup))
	}
	if ep.ConnLogStream != "" {
		fmt.Fprintf(&b, "<CloudwatchLogStream>%s</CloudwatchLogStream>", xmlEscape(ep.ConnLogStream))
	}
	b.WriteString("</connectionLogOptions>")
	if len(ep.SecurityGroupIds) > 0 {
		b.WriteString("<securityGroupIdSet>")
		for _, sg := range ep.SecurityGroupIds {
			fmt.Fprintf(&b, "<item>%s</item>", sg)
		}
		b.WriteString("</securityGroupIdSet>")
	}
	if ep.VpcId != "" {
		fmt.Fprintf(&b, "<vpcId>%s</vpcId>", ep.VpcId)
	}
	fmt.Fprintf(&b, "<sessionTimeoutHours>%d</sessionTimeoutHours>", ep.SessionTimeoutHours)
	b.WriteString(writeTagSetXML(ep.Tags))
	return b.String()
}

func handleDescribeClientVpnEndpoints(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ClientVpnEndpointId")
	for _, id := range ids {
		if _, ok := ec2ClientVpnEndpoint.Get(id); !ok {
			ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	var items strings.Builder
	list := ec2ClientVpnEndpoint.List()
	sort.Slice(list, func(i, j int) bool { return list[i].ClientVpnEndpointId < list[j].ClientVpnEndpointId })
	for _, ep := range list {
		if len(ids) > 0 && !ec2StrInValues(ep.ClientVpnEndpointId, ids) {
			continue
		}
		if !ec2ClientVpnEndpointMatchesFilters(ep, filters) {
			continue
		}
		items.WriteString("<item>")
		items.WriteString(ec2ClientVpnEndpointFieldsXML(ep))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeClientVpnEndpointsResponse %s><requestId>%s</requestId><clientVpnEndpoint>%s</clientVpnEndpoint></DescribeClientVpnEndpointsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2ClientVpnEndpointMatchesFilters(ep EC2ClientVpnEndpoint, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "endpoint-id":
			if !ec2StrInValues(ep.ClientVpnEndpointId, vals) {
				return false
			}
		case "transport-protocol":
			if !ec2StrInValues(ep.TransportProtocol, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, ep.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleModifyClientVpnEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ClientVpnEndpointId")
	ep, ok := ec2ClientVpnEndpoint.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if r.FormValue("Description") != "" {
		ep.Description = r.FormValue("Description")
	}
	if v := r.FormValue("ServerCertificateArn"); v != "" {
		ep.ServerCertificateArn = v
	}
	if v := r.FormValue("SplitTunnel"); v != "" {
		ep.SplitTunnel = v == "true"
	}
	if v := r.FormValue("VpnPort"); v != "" {
		ep.VpnPort = ec2AtoiOr(v, ep.VpnPort)
	}
	if v := r.FormValue("SessionTimeoutHours"); v != "" {
		ep.SessionTimeoutHours = ec2AtoiOr(v, ep.SessionTimeoutHours)
	}
	if sgs := ec2ParamList(r, "SecurityGroupId"); len(sgs) > 0 {
		ep.SecurityGroupIds = sgs
	}
	if r.FormValue("ConnectionLogOptions.Enabled") != "" {
		ep.ConnLogEnabled = r.FormValue("ConnectionLogOptions.Enabled") == "true"
		ep.ConnLogGroup = r.FormValue("ConnectionLogOptions.CloudwatchLogGroup")
		ep.ConnLogStream = r.FormValue("ConnectionLogOptions.CloudwatchLogStream")
	}
	ec2ClientVpnEndpoint.Put(id, ep)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyClientVpnEndpointResponse %s><requestId>%s</requestId><return>true</return></ModifyClientVpnEndpointResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteClientVpnEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ClientVpnEndpointId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ClientVpnEndpointId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2ClientVpnEndpoint.Get(id); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2ClientVpnEndpoint.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteClientVpnEndpointResponse %s><requestId>%s</requestId><status><code>deleting</code></status></DeleteClientVpnEndpointResponse>`, ec2Xmlns(), generateUUID())
}

func handleCreateClientVpnRoute(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("DestinationCidrBlock")
	subnet := r.FormValue("TargetVpcSubnetId")
	if cidr == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter DestinationCidrBlock", http.StatusBadRequest)
		return
	}
	rt := EC2ClientVpnRoute{
		ClientVpnEndpointId: epID,
		DestinationCidr:     cidr,
		TargetSubnet:        subnet,
		Type:                "Nat",
		Origin:              "add-route",
		StatusCode:          "active",
		Description:         r.FormValue("Description"),
	}
	ec2ClientVpnRoutes.Put(epID+"|"+cidr+"|"+subnet, rt)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateClientVpnRouteResponse %s><requestId>%s</requestId><status><code>%s</code></status></CreateClientVpnRouteResponse>`,
		ec2Xmlns(), generateUUID(), rt.StatusCode)
}

func handleDescribeClientVpnRoutes(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	var items strings.Builder
	list := ec2ClientVpnRoutes.List()
	sort.Slice(list, func(i, j int) bool { return list[i].DestinationCidr < list[j].DestinationCidr })
	for _, rt := range list {
		if rt.ClientVpnEndpointId != epID {
			continue
		}
		items.WriteString("<item>")
		fmt.Fprintf(&items, "<clientVpnEndpointId>%s</clientVpnEndpointId><destinationCidr>%s</destinationCidr><targetSubnet>%s</targetSubnet><type>%s</type><origin>%s</origin><status><code>%s</code></status>",
			rt.ClientVpnEndpointId, xmlEscape(rt.DestinationCidr), rt.TargetSubnet, rt.Type, rt.Origin, rt.StatusCode)
		if rt.Description != "" {
			fmt.Fprintf(&items, "<description>%s</description>", xmlEscape(rt.Description))
		}
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeClientVpnRoutesResponse %s><requestId>%s</requestId><routes>%s</routes></DescribeClientVpnRoutesResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteClientVpnRoute(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	cidr := r.FormValue("DestinationCidrBlock")
	subnet := r.FormValue("TargetVpcSubnetId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	// Delete every matching route regardless of which subnet recorded it.
	for _, rt := range ec2ClientVpnRoutes.List() {
		if rt.ClientVpnEndpointId == epID && rt.DestinationCidr == cidr && (subnet == "" || rt.TargetSubnet == subnet) {
			ec2ClientVpnRoutes.Delete(epID + "|" + rt.DestinationCidr + "|" + rt.TargetSubnet)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteClientVpnRouteResponse %s><requestId>%s</requestId><status><code>deleting</code></status></DeleteClientVpnRouteResponse>`, ec2Xmlns(), generateUUID())
}

func handleAuthorizeClientVpnIngress(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("TargetNetworkCidr")
	if cidr == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter TargetNetworkCidr", http.StatusBadRequest)
		return
	}
	rule := EC2ClientVpnAuthRule{
		ClientVpnEndpointId: epID,
		Description:         r.FormValue("Description"),
		GroupId:             r.FormValue("AccessGroupId"),
		AccessAll:           r.FormValue("AuthorizeAllGroups") == "true",
		DestinationCidr:     cidr,
		StatusCode:          "active",
	}
	ec2ClientVpnAuth.Put(epID+"|"+cidr+"|"+rule.GroupId, rule)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AuthorizeClientVpnIngressResponse %s><requestId>%s</requestId><status><code>%s</code></status></AuthorizeClientVpnIngressResponse>`,
		ec2Xmlns(), generateUUID(), rule.StatusCode)
}

func handleRevokeClientVpnIngress(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	cidr := r.FormValue("TargetNetworkCidr")
	groupID := r.FormValue("AccessGroupId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	for _, rule := range ec2ClientVpnAuth.List() {
		if rule.ClientVpnEndpointId == epID && rule.DestinationCidr == cidr && (groupID == "" || rule.GroupId == groupID) {
			ec2ClientVpnAuth.Delete(epID + "|" + rule.DestinationCidr + "|" + rule.GroupId)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RevokeClientVpnIngressResponse %s><requestId>%s</requestId><status><code>revoking</code></status></RevokeClientVpnIngressResponse>`, ec2Xmlns(), generateUUID())
}

func handleDescribeClientVpnAuthorizationRules(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	var items strings.Builder
	list := ec2ClientVpnAuth.List()
	sort.Slice(list, func(i, j int) bool { return list[i].DestinationCidr < list[j].DestinationCidr })
	for _, rule := range list {
		if rule.ClientVpnEndpointId != epID {
			continue
		}
		items.WriteString("<item>")
		fmt.Fprintf(&items, "<clientVpnEndpointId>%s</clientVpnEndpointId><accessAll>%t</accessAll><destinationCidr>%s</destinationCidr><status><code>%s</code></status>",
			rule.ClientVpnEndpointId, rule.AccessAll, xmlEscape(rule.DestinationCidr), rule.StatusCode)
		if rule.GroupId != "" {
			fmt.Fprintf(&items, "<groupId>%s</groupId>", xmlEscape(rule.GroupId))
		}
		if rule.Description != "" {
			fmt.Fprintf(&items, "<description>%s</description>", xmlEscape(rule.Description))
		}
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeClientVpnAuthorizationRulesResponse %s><requestId>%s</requestId><authorizationRule>%s</authorizationRule></DescribeClientVpnAuthorizationRulesResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleAssociateClientVpnTargetNetwork(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	subnetID := r.FormValue("SubnetId")
	ep, ok := ec2ClientVpnEndpoint.Get(epID)
	if !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	if subnetID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SubnetId", http.StatusBadRequest)
		return
	}
	vpcID := ep.VpcId
	if subnet, ok := ec2Subnets.Get(subnetID); ok {
		vpcID = subnet.VpcId
		// Associating the first target network sets the endpoint's VPC.
		if ep.VpcId == "" {
			ep.VpcId = subnet.VpcId
			ec2ClientVpnEndpoint.Put(epID, ep)
		}
	}
	assoc := EC2ClientVpnAssoc{
		AssociationId:       "cvpn-assoc-" + generateUUID()[:17],
		ClientVpnEndpointId: epID,
		TargetNetworkId:     subnetID,
		VpcId:               vpcID,
		StatusCode:          "associating",
	}
	ec2ClientVpnAssocs.Put(assoc.AssociationId, assoc)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateClientVpnTargetNetworkResponse %s><requestId>%s</requestId><associationId>%s</associationId><status><code>%s</code></status></AssociateClientVpnTargetNetworkResponse>`,
		ec2Xmlns(), generateUUID(), assoc.AssociationId, assoc.StatusCode)
}

func handleDisassociateClientVpnTargetNetwork(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	assocID := r.FormValue("AssociationId")
	assoc, ok := ec2ClientVpnAssocs.Get(assocID)
	if !ok || assoc.ClientVpnEndpointId != epID {
		ec2ErrorXML(w, "InvalidClientVpnAssociationId.NotFound", fmt.Sprintf("The target network association '%s' does not exist", assocID), http.StatusBadRequest)
		return
	}
	ec2ClientVpnAssocs.Delete(assocID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateClientVpnTargetNetworkResponse %s><requestId>%s</requestId><associationId>%s</associationId><status><code>disassociating</code></status></DisassociateClientVpnTargetNetworkResponse>`,
		ec2Xmlns(), generateUUID(), assocID)
}

func handleDescribeClientVpnTargetNetworks(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	assocIDs := ec2ParamList(r, "AssociationIds")
	var items strings.Builder
	list := ec2ClientVpnAssocs.List()
	sort.Slice(list, func(i, j int) bool { return list[i].AssociationId < list[j].AssociationId })
	for _, assoc := range list {
		if assoc.ClientVpnEndpointId != epID {
			continue
		}
		if len(assocIDs) > 0 && !ec2StrInValues(assoc.AssociationId, assocIDs) {
			continue
		}
		items.WriteString("<item>")
		fmt.Fprintf(&items, "<associationId>%s</associationId><vpcId>%s</vpcId><targetNetworkId>%s</targetNetworkId><clientVpnEndpointId>%s</clientVpnEndpointId><status><code>%s</code></status>",
			assoc.AssociationId, assoc.VpcId, assoc.TargetNetworkId, assoc.ClientVpnEndpointId, assoc.StatusCode)
		if len(assoc.SecurityGroups) > 0 {
			items.WriteString("<securityGroups>")
			for _, sg := range assoc.SecurityGroups {
				fmt.Fprintf(&items, "<item>%s</item>", sg)
			}
			items.WriteString("</securityGroups>")
		}
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeClientVpnTargetNetworksResponse %s><requestId>%s</requestId><clientVpnTargetNetworks>%s</clientVpnTargetNetworks></DescribeClientVpnTargetNetworksResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleApplySecurityGroupsToClientVpnTargetNetwork(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	vpcID := r.FormValue("VpcId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	sgs := ec2ParamList(r, "SecurityGroupId")
	// Record the applied groups on every target-network association in this VPC.
	for _, assoc := range ec2ClientVpnAssocs.List() {
		if assoc.ClientVpnEndpointId == epID && (vpcID == "" || assoc.VpcId == vpcID) {
			assoc.SecurityGroups = sgs
			ec2ClientVpnAssocs.Put(assoc.AssociationId, assoc)
		}
	}
	var items strings.Builder
	for _, sg := range sgs {
		fmt.Fprintf(&items, "<item>%s</item>", sg)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ApplySecurityGroupsToClientVpnTargetNetworkResponse %s><requestId>%s</requestId><securityGroupIds>%s</securityGroupIds></ApplySecurityGroupsToClientVpnTargetNetworkResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func handleDescribeClientVpnConnections(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	// No clients connect to the simulated endpoint, so the connection set is
	// empty — matching a real endpoint with no active sessions.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeClientVpnConnectionsResponse %s><requestId>%s</requestId><connections/></DescribeClientVpnConnectionsResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleTerminateClientVpnConnections(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	username := r.FormValue("Username")
	connID := r.FormValue("ConnectionId")
	var statuses strings.Builder
	if connID != "" {
		fmt.Fprintf(&statuses, "<item><connectionId>%s</connectionId><previousStatus><code>active</code></previousStatus><currentStatus><code>terminating</code></currentStatus></item>", connID)
	}
	usernameXML := ""
	if username != "" {
		usernameXML = fmt.Sprintf("<username>%s</username>", xmlEscape(username))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<TerminateClientVpnConnectionsResponse %s><requestId>%s</requestId><clientVpnEndpointId>%s</clientVpnEndpointId>%s<connectionStatuses>%s</connectionStatuses></TerminateClientVpnConnectionsResponse>`,
		ec2Xmlns(), generateUUID(), epID, usernameXML, statuses.String())
}
