package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Amazon EC2 Transit Gateway family — faithful ec2Query (XML) CRUD.
//
// Resources modeled, each persisted in its own sim.Store:
//   - Transit gateways (tgw-…), with two implicit default route tables
//     (association + propagation), as real AWS auto-creates.
//   - Transit gateway route tables (tgw-rtb-…).
//   - VPC attachments (tgw-attach-…): pending → available, accept/reject.
//   - Peering attachments (tgw-attach-…): pendingAcceptance → available.
//   - Connect attachments (tgw-attach-…) over a transport VPC attachment.
//   - Multicast domains (tgw-mcast-domain-…).
//   - Route-table associations and propagations, keyed per
//     (route-table, attachment).
//   - Static routes and prefix-list references on a route table.
//
// XML element names match the ec2QueryName/xmlName traits in
// specs/cloud-api/aws/ec2.smithy.json.gz exactly (the spec-shape validator
// rejects any unknown or mis-cased field).

type EC2TransitGateway struct {
	TransitGatewayId               string
	TransitGatewayArn              string
	State                          string
	OwnerId                        string
	Description                    string
	CreationTime                   string
	AmazonSideAsn                  int64
	AutoAcceptSharedAttachments    string
	DefaultRouteTableAssociation   string
	AssociationDefaultRouteTableId string
	DefaultRouteTablePropagation   string
	PropagationDefaultRouteTableId string
	VpnEcmpSupport                 string
	DnsSupport                     string
	MulticastSupport               string
	TransitGatewayCidrBlocks       []string
	Tags                           []EC2Tag
}

type EC2TransitGatewayRouteTable struct {
	TransitGatewayRouteTableId   string
	TransitGatewayId             string
	State                        string
	DefaultAssociationRouteTable bool
	DefaultPropagationRouteTable bool
	CreationTime                 string
	Tags                         []EC2Tag
	// Routes are the static + propagated routes held by this route table.
	Routes []EC2TransitGatewayRoute
	// Associations and Propagations are keyed by attachment id.
	Associations []EC2TGWRouteTableAssociation
	Propagations []EC2TGWRouteTablePropagation
	// PrefixListRefs holds prefix-list references keyed by prefix-list id.
	PrefixListRefs []EC2TGWPrefixListReference
}

type EC2TransitGatewayRoute struct {
	DestinationCidrBlock string
	PrefixListId         string
	Type                 string // static | propagated
	State                string // active | blackhole | pending | deleted
	AttachmentIds        []string
	ResourceId           string
	ResourceType         string
}

type EC2TGWRouteTableAssociation struct {
	TransitGatewayAttachmentId string
	ResourceId                 string
	ResourceType               string
	State                      string
}

type EC2TGWRouteTablePropagation struct {
	TransitGatewayAttachmentId string
	ResourceId                 string
	ResourceType               string
	State                      string
}

type EC2TGWPrefixListReference struct {
	PrefixListId               string
	PrefixListOwnerId          string
	State                      string
	Blackhole                  bool
	TransitGatewayAttachmentId string
	ResourceId                 string
	ResourceType               string
}

type EC2TransitGatewayVpcAttachment struct {
	TransitGatewayAttachmentId      string
	TransitGatewayId                string
	VpcId                           string
	VpcOwnerId                      string
	State                           string
	SubnetIds                       []string
	CreationTime                    string
	DnsSupport                      string
	Ipv6Support                     string
	ApplianceModeSupport            string
	SecurityGroupReferencingSupport string
	Tags                            []EC2Tag
	// Association tracks the route table this attachment is associated with,
	// surfaced via DescribeTransitGatewayAttachments.
	AssociationRouteTableId string
	AssociationState        string
}

type EC2TransitGatewayPeeringAttachment struct {
	TransitGatewayAttachmentId         string
	AccepterTransitGatewayAttachmentId string
	RequesterTgwId                     string
	RequesterRegion                    string
	RequesterOwnerId                   string
	AccepterTgwId                      string
	AccepterRegion                     string
	AccepterOwnerId                    string
	DynamicRouting                     string
	StatusCode                         string
	State                              string
	CreationTime                       string
	Tags                               []EC2Tag
}

type EC2TransitGatewayConnect struct {
	TransitGatewayAttachmentId          string
	TransportTransitGatewayAttachmentId string
	TransitGatewayId                    string
	State                               string
	CreationTime                        string
	Protocol                            string
	Tags                                []EC2Tag
}

type EC2TransitGatewayMulticastDomain struct {
	TransitGatewayMulticastDomainId  string
	TransitGatewayId                 string
	TransitGatewayMulticastDomainArn string
	OwnerId                          string
	Igmpv2Support                    string
	StaticSourcesSupport             string
	AutoAcceptSharedAssociations     string
	State                            string
	CreationTime                     string
	Tags                             []EC2Tag
}

var (
	ec2TransitGateways           sim.Store[EC2TransitGateway]
	ec2TransitGatewayRouteTables sim.Store[EC2TransitGatewayRouteTable]
	ec2TGWVpcAttachments         sim.Store[EC2TransitGatewayVpcAttachment]
	ec2TGWPeeringAttachments     sim.Store[EC2TransitGatewayPeeringAttachment]
	ec2TGWConnects               sim.Store[EC2TransitGatewayConnect]
	ec2TGWMulticastDomains       sim.Store[EC2TransitGatewayMulticastDomain]
)

func registerEC2TransitGateway(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2TransitGateways = sim.MakeStore[EC2TransitGateway](srv.DB(), "ec2_transit_gateways")
	ec2TransitGatewayRouteTables = sim.MakeStore[EC2TransitGatewayRouteTable](srv.DB(), "ec2_transit_gateway_route_tables")
	ec2TGWVpcAttachments = sim.MakeStore[EC2TransitGatewayVpcAttachment](srv.DB(), "ec2_tgw_vpc_attachments")
	ec2TGWPeeringAttachments = sim.MakeStore[EC2TransitGatewayPeeringAttachment](srv.DB(), "ec2_tgw_peering_attachments")
	ec2TGWConnects = sim.MakeStore[EC2TransitGatewayConnect](srv.DB(), "ec2_tgw_connects")
	ec2TGWMulticastDomains = sim.MakeStore[EC2TransitGatewayMulticastDomain](srv.DB(), "ec2_tgw_multicast_domains")

	// Transit gateway
	r.Register("CreateTransitGateway", handleCreateTransitGateway)
	r.Register("DescribeTransitGateways", handleDescribeTransitGateways)
	r.Register("ModifyTransitGateway", handleModifyTransitGateway)
	r.Register("DeleteTransitGateway", handleDeleteTransitGateway)

	// Route tables
	r.Register("CreateTransitGatewayRouteTable", handleCreateTransitGatewayRouteTable)
	r.Register("DescribeTransitGatewayRouteTables", handleDescribeTransitGatewayRouteTables)
	r.Register("DeleteTransitGatewayRouteTable", handleDeleteTransitGatewayRouteTable)

	// VPC attachments
	r.Register("CreateTransitGatewayVpcAttachment", handleCreateTransitGatewayVpcAttachment)
	r.Register("DescribeTransitGatewayVpcAttachments", handleDescribeTransitGatewayVpcAttachments)
	r.Register("ModifyTransitGatewayVpcAttachment", handleModifyTransitGatewayVpcAttachment)
	r.Register("DeleteTransitGatewayVpcAttachment", handleDeleteTransitGatewayVpcAttachment)
	r.Register("AcceptTransitGatewayVpcAttachment", handleAcceptTransitGatewayVpcAttachment)
	r.Register("RejectTransitGatewayVpcAttachment", handleRejectTransitGatewayVpcAttachment)
	r.Register("DescribeTransitGatewayAttachments", handleDescribeTransitGatewayAttachments)

	// Associations + propagations
	r.Register("AssociateTransitGatewayRouteTable", handleAssociateTransitGatewayRouteTable)
	r.Register("DisassociateTransitGatewayRouteTable", handleDisassociateTransitGatewayRouteTable)
	r.Register("GetTransitGatewayRouteTableAssociations", handleGetTransitGatewayRouteTableAssociations)
	r.Register("EnableTransitGatewayRouteTablePropagation", handleEnableTransitGatewayRouteTablePropagation)
	r.Register("DisableTransitGatewayRouteTablePropagation", handleDisableTransitGatewayRouteTablePropagation)
	r.Register("GetTransitGatewayRouteTablePropagations", handleGetTransitGatewayRouteTablePropagations)
	r.Register("GetTransitGatewayAttachmentPropagations", handleGetTransitGatewayAttachmentPropagations)

	// Routes
	r.Register("CreateTransitGatewayRoute", handleCreateTransitGatewayRoute)
	r.Register("DeleteTransitGatewayRoute", handleDeleteTransitGatewayRoute)
	r.Register("ReplaceTransitGatewayRoute", handleReplaceTransitGatewayRoute)
	r.Register("SearchTransitGatewayRoutes", handleSearchTransitGatewayRoutes)
	r.Register("ExportTransitGatewayRoutes", handleExportTransitGatewayRoutes)

	// Prefix list references
	r.Register("CreateTransitGatewayPrefixListReference", handleCreateTransitGatewayPrefixListReference)
	r.Register("GetTransitGatewayPrefixListReferences", handleGetTransitGatewayPrefixListReferences)
	r.Register("ModifyTransitGatewayPrefixListReference", handleModifyTransitGatewayPrefixListReference)
	r.Register("DeleteTransitGatewayPrefixListReference", handleDeleteTransitGatewayPrefixListReference)

	// Peering attachments
	r.Register("CreateTransitGatewayPeeringAttachment", handleCreateTransitGatewayPeeringAttachment)
	r.Register("DescribeTransitGatewayPeeringAttachments", handleDescribeTransitGatewayPeeringAttachments)
	r.Register("AcceptTransitGatewayPeeringAttachment", handleAcceptTransitGatewayPeeringAttachment)
	r.Register("RejectTransitGatewayPeeringAttachment", handleRejectTransitGatewayPeeringAttachment)
	r.Register("DeleteTransitGatewayPeeringAttachment", handleDeleteTransitGatewayPeeringAttachment)

	// Multicast domains
	r.Register("CreateTransitGatewayMulticastDomain", handleCreateTransitGatewayMulticastDomain)
	r.Register("DescribeTransitGatewayMulticastDomains", handleDescribeTransitGatewayMulticastDomains)
	r.Register("DeleteTransitGatewayMulticastDomain", handleDeleteTransitGatewayMulticastDomain)

	// Connect attachments
	r.Register("CreateTransitGatewayConnect", handleCreateTransitGatewayConnect)
	r.Register("DescribeTransitGatewayConnects", handleDescribeTransitGatewayConnects)
	r.Register("DeleteTransitGatewayConnect", handleDeleteTransitGatewayConnect)
}

// ---- helpers ----

func tgwArn(id string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:transit-gateway/%s", awsRegion(), ec2Owner(), id)
}

func tgwMcastArn(id string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:transit-gateway-multicast-domain/%s", awsRegion(), ec2Owner(), id)
}

func tgwOptOrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func tgwResponse(w http.ResponseWriter, action, body string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, "<%sResponse %s><requestId>%s</requestId>%s</%sResponse>",
		action, ec2Xmlns(), generateUUID(), body, action)
}

// ---- XML rendering ----

func tgwBodyXML(tgw EC2TransitGateway) string {
	var cidrs strings.Builder
	if len(tgw.TransitGatewayCidrBlocks) > 0 {
		cidrs.WriteString("<transitGatewayCidrBlocks>")
		for _, c := range tgw.TransitGatewayCidrBlocks {
			fmt.Fprintf(&cidrs, "<item>%s</item>", c)
		}
		cidrs.WriteString("</transitGatewayCidrBlocks>")
	}
	desc := ""
	if tgw.Description != "" {
		desc = fmt.Sprintf("<description>%s</description>", tgw.Description)
	}
	return fmt.Sprintf(`<transitGatewayId>%s</transitGatewayId>`+
		`<transitGatewayArn>%s</transitGatewayArn><state>%s</state><ownerId>%s</ownerId>%s`+
		`<creationTime>%s</creationTime><options><amazonSideAsn>%d</amazonSideAsn>%s`+
		`<autoAcceptSharedAttachments>%s</autoAcceptSharedAttachments>`+
		`<defaultRouteTableAssociation>%s</defaultRouteTableAssociation>`+
		`<associationDefaultRouteTableId>%s</associationDefaultRouteTableId>`+
		`<defaultRouteTablePropagation>%s</defaultRouteTablePropagation>`+
		`<propagationDefaultRouteTableId>%s</propagationDefaultRouteTableId>`+
		`<vpnEcmpSupport>%s</vpnEcmpSupport><dnsSupport>%s</dnsSupport>`+
		`<multicastSupport>%s</multicastSupport></options>%s`,
		tgw.TransitGatewayId, tgw.TransitGatewayArn, tgw.State, tgw.OwnerId, desc,
		tgw.CreationTime, tgw.AmazonSideAsn, cidrs.String(),
		tgw.AutoAcceptSharedAttachments, tgw.DefaultRouteTableAssociation,
		tgw.AssociationDefaultRouteTableId, tgw.DefaultRouteTablePropagation,
		tgw.PropagationDefaultRouteTableId, tgw.VpnEcmpSupport, tgw.DnsSupport,
		tgw.MulticastSupport, writeTagSetXML(tgw.Tags))
}

func tgwRouteTableBodyXML(rt EC2TransitGatewayRouteTable) string {
	return fmt.Sprintf(`<transitGatewayRouteTableId>%s</transitGatewayRouteTableId>`+
		`<transitGatewayId>%s</transitGatewayId><state>%s</state>`+
		`<defaultAssociationRouteTable>%t</defaultAssociationRouteTable>`+
		`<defaultPropagationRouteTable>%t</defaultPropagationRouteTable>`+
		`<creationTime>%s</creationTime>%s`,
		rt.TransitGatewayRouteTableId, rt.TransitGatewayId, rt.State,
		rt.DefaultAssociationRouteTable, rt.DefaultPropagationRouteTable,
		rt.CreationTime, writeTagSetXML(rt.Tags))
}

func tgwVpcAttachmentBodyXML(a EC2TransitGatewayVpcAttachment) string {
	var subnets strings.Builder
	subnets.WriteString("<subnetIds>")
	for _, s := range a.SubnetIds {
		fmt.Fprintf(&subnets, "<item>%s</item>", s)
	}
	subnets.WriteString("</subnetIds>")
	return fmt.Sprintf(`<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
		`<transitGatewayId>%s</transitGatewayId><vpcId>%s</vpcId>`+
		`<vpcOwnerId>%s</vpcOwnerId><state>%s</state>%s<creationTime>%s</creationTime>`+
		`<options><dnsSupport>%s</dnsSupport><ipv6Support>%s</ipv6Support>`+
		`<applianceModeSupport>%s</applianceModeSupport></options>%s`,
		a.TransitGatewayAttachmentId, a.TransitGatewayId, a.VpcId, a.VpcOwnerId,
		a.State, subnets.String(), a.CreationTime, a.DnsSupport, a.Ipv6Support,
		a.ApplianceModeSupport, writeTagSetXML(a.Tags))
}

// tgwGenericAttachmentItemXML renders a TransitGatewayAttachment item for
// DescribeTransitGatewayAttachments (the cross-type attachment listing).
func tgwGenericAttachmentItemXML(attachID, tgwID, resourceType, resourceID, state, assocRtID, assocState, creationTime string, tags []EC2Tag) string {
	assoc := ""
	if assocRtID != "" {
		assoc = fmt.Sprintf(`<association><transitGatewayRouteTableId>%s</transitGatewayRouteTableId><state>%s</state></association>`,
			assocRtID, assocState)
	}
	return fmt.Sprintf(`<item><transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
		`<transitGatewayId>%s</transitGatewayId><transitGatewayOwnerId>%s</transitGatewayOwnerId>`+
		`<resourceOwnerId>%s</resourceOwnerId><resourceType>%s</resourceType>`+
		`<resourceId>%s</resourceId><state>%s</state>%s<creationTime>%s</creationTime>%s</item>`,
		attachID, tgwID, ec2Owner(), ec2Owner(), resourceType, resourceID, state,
		assoc, creationTime, writeTagSetXML(tags))
}

func tgwRouteItemXML(rt EC2TransitGatewayRoute) string {
	var atts strings.Builder
	atts.WriteString("<transitGatewayAttachments>")
	for _, id := range rt.AttachmentIds {
		fmt.Fprintf(&atts, `<item><resourceId>%s</resourceId>`+
			`<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
			`<resourceType>%s</resourceType></item>`, rt.ResourceId, id, tgwOptOrDefault(rt.ResourceType, "vpc"))
	}
	atts.WriteString("</transitGatewayAttachments>")
	dest := ""
	if rt.DestinationCidrBlock != "" {
		dest = fmt.Sprintf("<destinationCidrBlock>%s</destinationCidrBlock>", rt.DestinationCidrBlock)
	}
	pfx := ""
	if rt.PrefixListId != "" {
		pfx = fmt.Sprintf("<prefixListId>%s</prefixListId>", rt.PrefixListId)
	}
	return fmt.Sprintf(`<item>%s%s%s<type>%s</type><state>%s</state></item>`,
		dest, pfx, atts.String(), rt.Type, rt.State)
}

func tgwPrefixListRefBodyXML(ref EC2TGWPrefixListReference, rtID string) string {
	attach := ""
	if ref.TransitGatewayAttachmentId != "" {
		attach = fmt.Sprintf(`<transitGatewayAttachment>`+
			`<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
			`<resourceType>%s</resourceType><resourceId>%s</resourceId>`+
			`</transitGatewayAttachment>`,
			ref.TransitGatewayAttachmentId, tgwOptOrDefault(ref.ResourceType, "vpc"), ref.ResourceId)
	}
	return fmt.Sprintf(`<transitGatewayRouteTableId>%s</transitGatewayRouteTableId>`+
		`<prefixListId>%s</prefixListId><prefixListOwnerId>%s</prefixListOwnerId>`+
		`<state>%s</state><blackhole>%t</blackhole>%s`,
		rtID, ref.PrefixListId, ref.PrefixListOwnerId, ref.State, ref.Blackhole, attach)
}

func tgwPeeringBodyXML(p EC2TransitGatewayPeeringAttachment) string {
	return fmt.Sprintf(`<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
		`<accepterTransitGatewayAttachmentId>%s</accepterTransitGatewayAttachmentId>`+
		`<requesterTgwInfo><transitGatewayId>%s</transitGatewayId><ownerId>%s</ownerId><region>%s</region></requesterTgwInfo>`+
		`<accepterTgwInfo><transitGatewayId>%s</transitGatewayId><ownerId>%s</ownerId><region>%s</region></accepterTgwInfo>`+
		`<options><dynamicRouting>%s</dynamicRouting></options>`+
		`<status><code>%s</code></status><state>%s</state><creationTime>%s</creationTime>%s`,
		p.TransitGatewayAttachmentId, p.AccepterTransitGatewayAttachmentId,
		p.RequesterTgwId, p.RequesterOwnerId, p.RequesterRegion,
		p.AccepterTgwId, p.AccepterOwnerId, p.AccepterRegion,
		tgwOptOrDefault(p.DynamicRouting, "enable"), p.StatusCode, p.State,
		p.CreationTime, writeTagSetXML(p.Tags))
}

func tgwConnectBodyXML(c EC2TransitGatewayConnect) string {
	return fmt.Sprintf(`<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
		`<transportTransitGatewayAttachmentId>%s</transportTransitGatewayAttachmentId>`+
		`<transitGatewayId>%s</transitGatewayId><state>%s</state>`+
		`<creationTime>%s</creationTime><options><protocol>%s</protocol></options>%s`,
		c.TransitGatewayAttachmentId, c.TransportTransitGatewayAttachmentId,
		c.TransitGatewayId, c.State, c.CreationTime, c.Protocol, writeTagSetXML(c.Tags))
}

func tgwMulticastBodyXML(m EC2TransitGatewayMulticastDomain) string {
	return fmt.Sprintf(`<transitGatewayMulticastDomainId>%s</transitGatewayMulticastDomainId>`+
		`<transitGatewayId>%s</transitGatewayId>`+
		`<transitGatewayMulticastDomainArn>%s</transitGatewayMulticastDomainArn>`+
		`<ownerId>%s</ownerId><options><igmpv2Support>%s</igmpv2Support>`+
		`<staticSourcesSupport>%s</staticSourcesSupport>`+
		`<autoAcceptSharedAssociations>%s</autoAcceptSharedAssociations></options>`+
		`<state>%s</state><creationTime>%s</creationTime>%s`,
		m.TransitGatewayMulticastDomainId, m.TransitGatewayId,
		m.TransitGatewayMulticastDomainArn, m.OwnerId, m.Igmpv2Support,
		m.StaticSourcesSupport, m.AutoAcceptSharedAssociations, m.State,
		m.CreationTime, writeTagSetXML(m.Tags))
}

// ---- Transit gateway ----

func handleCreateTransitGateway(w http.ResponseWriter, r *http.Request) {
	now := ec2NowRFC3339Milli()
	id := ec2ID("tgw")
	asn := int64(64512)
	if v := r.FormValue("Options.AmazonSideAsn"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			asn = parsed
		}
	}
	// Real AWS auto-creates the default association + propagation route tables
	// (the same one by default) when DefaultRouteTableAssociation/Propagation
	// are enabled. Model that so the IDs round-trip through Describe.
	assoc := tgwOptOrDefault(r.FormValue("Options.DefaultRouteTableAssociation"), "enable")
	prop := tgwOptOrDefault(r.FormValue("Options.DefaultRouteTablePropagation"), "enable")
	tgw := EC2TransitGateway{
		TransitGatewayId:             id,
		TransitGatewayArn:            tgwArn(id),
		State:                        "available",
		OwnerId:                      ec2Owner(),
		Description:                  r.FormValue("Description"),
		CreationTime:                 now,
		AmazonSideAsn:                asn,
		AutoAcceptSharedAttachments:  tgwOptOrDefault(r.FormValue("Options.AutoAcceptSharedAttachments"), "disable"),
		DefaultRouteTableAssociation: assoc,
		DefaultRouteTablePropagation: prop,
		VpnEcmpSupport:               tgwOptOrDefault(r.FormValue("Options.VpnEcmpSupport"), "enable"),
		DnsSupport:                   tgwOptOrDefault(r.FormValue("Options.DnsSupport"), "enable"),
		MulticastSupport:             tgwOptOrDefault(r.FormValue("Options.MulticastSupport"), "disable"),
		Tags:                         parseTags(r),
	}
	if assoc == "enable" || prop == "enable" {
		rtID := ec2ID("tgw-rtb")
		defaultRT := EC2TransitGatewayRouteTable{
			TransitGatewayRouteTableId:   rtID,
			TransitGatewayId:             id,
			State:                        "available",
			DefaultAssociationRouteTable: assoc == "enable",
			DefaultPropagationRouteTable: prop == "enable",
			CreationTime:                 now,
		}
		ec2TransitGatewayRouteTables.Put(rtID, defaultRT)
		if assoc == "enable" {
			tgw.AssociationDefaultRouteTableId = rtID
		}
		if prop == "enable" {
			tgw.PropagationDefaultRouteTableId = rtID
		}
	}
	ec2TransitGateways.Put(id, tgw)
	tgwResponse(w, "CreateTransitGateway", "<transitGateway>"+tgwBodyXML(tgw)+"</transitGateway>")
}

func handleDescribeTransitGateways(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayIds")
	var b strings.Builder
	b.WriteString("<transitGatewaySet>")
	for _, tgw := range ec2TransitGateways.List() {
		if len(ids) > 0 && !ec2StrInValues(tgw.TransitGatewayId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwBodyXML(tgw))
	}
	b.WriteString("</transitGatewaySet>")
	tgwResponse(w, "DescribeTransitGateways", b.String())
}

func handleModifyTransitGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayId")
	tgw, ok := ec2TransitGateways.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayID.NotFound", "transit gateway not found: "+id, http.StatusBadRequest)
		return
	}
	if v := r.FormValue("Description"); v != "" {
		tgw.Description = v
	}
	if v := r.FormValue("Options.DnsSupport"); v != "" {
		tgw.DnsSupport = v
	}
	if v := r.FormValue("Options.VpnEcmpSupport"); v != "" {
		tgw.VpnEcmpSupport = v
	}
	if v := r.FormValue("Options.AutoAcceptSharedAttachments"); v != "" {
		tgw.AutoAcceptSharedAttachments = v
	}
	ec2TransitGateways.Put(id, tgw)
	tgwResponse(w, "ModifyTransitGateway", "<transitGateway>"+tgwBodyXML(tgw)+"</transitGateway>")
}

func handleDeleteTransitGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayId")
	tgw, ok := ec2TransitGateways.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayID.NotFound", "transit gateway not found: "+id, http.StatusBadRequest)
		return
	}
	tgw.State = "deleted"
	// Cascade: drop the transit gateway's owned default route tables.
	for _, rt := range ec2TransitGatewayRouteTables.List() {
		if rt.TransitGatewayId == id {
			ec2TransitGatewayRouteTables.Delete(rt.TransitGatewayRouteTableId)
		}
	}
	ec2TransitGateways.Delete(id)
	tgwResponse(w, "DeleteTransitGateway", "<transitGateway>"+tgwBodyXML(tgw)+"</transitGateway>")
}

// ---- Route tables ----

func handleCreateTransitGatewayRouteTable(w http.ResponseWriter, r *http.Request) {
	tgwID := r.FormValue("TransitGatewayId")
	if _, ok := ec2TransitGateways.Get(tgwID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayID.NotFound", "transit gateway not found: "+tgwID, http.StatusBadRequest)
		return
	}
	id := ec2ID("tgw-rtb")
	rt := EC2TransitGatewayRouteTable{
		TransitGatewayRouteTableId: id,
		TransitGatewayId:           tgwID,
		State:                      "available",
		CreationTime:               ec2NowRFC3339Milli(),
		Tags:                       parseTags(r),
	}
	ec2TransitGatewayRouteTables.Put(id, rt)
	tgwResponse(w, "CreateTransitGatewayRouteTable",
		"<transitGatewayRouteTable>"+tgwRouteTableBodyXML(rt)+"</transitGatewayRouteTable>")
}

func handleDescribeTransitGatewayRouteTables(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayRouteTableIds")
	filters := ec2Filters(r)
	var b strings.Builder
	b.WriteString("<transitGatewayRouteTables>")
	for _, rt := range ec2TransitGatewayRouteTables.List() {
		if len(ids) > 0 && !ec2StrInValues(rt.TransitGatewayRouteTableId, ids) {
			continue
		}
		if v, ok := filters["transit-gateway-id"]; ok && !ec2StrInValues(rt.TransitGatewayId, v) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwRouteTableBodyXML(rt))
	}
	b.WriteString("</transitGatewayRouteTables>")
	tgwResponse(w, "DescribeTransitGatewayRouteTables", b.String())
}

func handleDeleteTransitGatewayRouteTable(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayRouteTableId")
	rt, ok := ec2TransitGatewayRouteTables.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+id, http.StatusBadRequest)
		return
	}
	rt.State = "deleted"
	ec2TransitGatewayRouteTables.Delete(id)
	tgwResponse(w, "DeleteTransitGatewayRouteTable",
		"<transitGatewayRouteTable>"+tgwRouteTableBodyXML(rt)+"</transitGatewayRouteTable>")
}

// ---- VPC attachments ----

func handleCreateTransitGatewayVpcAttachment(w http.ResponseWriter, r *http.Request) {
	tgwID := r.FormValue("TransitGatewayId")
	tgw, ok := ec2TransitGateways.Get(tgwID)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayID.NotFound", "transit gateway not found: "+tgwID, http.StatusBadRequest)
		return
	}
	id := ec2ID("tgw-attach")
	// Real AWS auto-accepts attachments to a transit gateway you own; the
	// attachment goes straight to "available". Cross-account attachments to a
	// gateway with AutoAcceptSharedAttachments=disable land in
	// "pendingAcceptance" — modeled via AcceptTransitGatewayVpcAttachment.
	state := "available"
	att := EC2TransitGatewayVpcAttachment{
		TransitGatewayAttachmentId: id,
		TransitGatewayId:           tgwID,
		VpcId:                      r.FormValue("VpcId"),
		VpcOwnerId:                 ec2Owner(),
		State:                      state,
		SubnetIds:                  ec2ParamList(r, "SubnetIds"),
		CreationTime:               ec2NowRFC3339Milli(),
		DnsSupport:                 tgwOptOrDefault(r.FormValue("Options.DnsSupport"), "enable"),
		Ipv6Support:                tgwOptOrDefault(r.FormValue("Options.Ipv6Support"), "disable"),
		ApplianceModeSupport:       tgwOptOrDefault(r.FormValue("Options.ApplianceModeSupport"), "disable"),
		Tags:                       parseTags(r),
	}
	// Default route table association + propagation, as the gateway dictates.
	if tgw.DefaultRouteTableAssociation == "enable" && tgw.AssociationDefaultRouteTableId != "" {
		att.AssociationRouteTableId = tgw.AssociationDefaultRouteTableId
		att.AssociationState = "associated"
		tgwAddAssociation(tgw.AssociationDefaultRouteTableId, id, att.VpcId, "vpc")
	}
	if tgw.DefaultRouteTablePropagation == "enable" && tgw.PropagationDefaultRouteTableId != "" {
		tgwAddPropagation(tgw.PropagationDefaultRouteTableId, id, att.VpcId, "vpc")
	}
	ec2TGWVpcAttachments.Put(id, att)
	tgwResponse(w, "CreateTransitGatewayVpcAttachment",
		"<transitGatewayVpcAttachment>"+tgwVpcAttachmentBodyXML(att)+"</transitGatewayVpcAttachment>")
}

func handleDescribeTransitGatewayVpcAttachments(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayAttachmentIds")
	filters := ec2Filters(r)
	var b strings.Builder
	b.WriteString("<transitGatewayVpcAttachments>")
	for _, a := range ec2TGWVpcAttachments.List() {
		if len(ids) > 0 && !ec2StrInValues(a.TransitGatewayAttachmentId, ids) {
			continue
		}
		if v, ok := filters["transit-gateway-id"]; ok && !ec2StrInValues(a.TransitGatewayId, v) {
			continue
		}
		if v, ok := filters["vpc-id"]; ok && !ec2StrInValues(a.VpcId, v) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwVpcAttachmentBodyXML(a))
	}
	b.WriteString("</transitGatewayVpcAttachments>")
	tgwResponse(w, "DescribeTransitGatewayVpcAttachments", b.String())
}

func handleModifyTransitGatewayVpcAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayAttachmentId")
	a, ok := ec2TGWVpcAttachments.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "attachment not found: "+id, http.StatusBadRequest)
		return
	}
	if add := ec2ParamList(r, "AddSubnetIds"); len(add) > 0 {
		a.SubnetIds = append(a.SubnetIds, add...)
	}
	if rm := ec2ParamList(r, "RemoveSubnetIds"); len(rm) > 0 {
		var kept []string
		for _, s := range a.SubnetIds {
			if !ec2StrInValues(s, rm) {
				kept = append(kept, s)
			}
		}
		a.SubnetIds = kept
	}
	if v := r.FormValue("Options.DnsSupport"); v != "" {
		a.DnsSupport = v
	}
	if v := r.FormValue("Options.Ipv6Support"); v != "" {
		a.Ipv6Support = v
	}
	if v := r.FormValue("Options.ApplianceModeSupport"); v != "" {
		a.ApplianceModeSupport = v
	}
	ec2TGWVpcAttachments.Put(id, a)
	tgwResponse(w, "ModifyTransitGatewayVpcAttachment",
		"<transitGatewayVpcAttachment>"+tgwVpcAttachmentBodyXML(a)+"</transitGatewayVpcAttachment>")
}

func handleDeleteTransitGatewayVpcAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayAttachmentId")
	a, ok := ec2TGWVpcAttachments.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "attachment not found: "+id, http.StatusBadRequest)
		return
	}
	a.State = "deleted"
	tgwRemoveAttachmentRefs(id)
	ec2TGWVpcAttachments.Delete(id)
	tgwResponse(w, "DeleteTransitGatewayVpcAttachment",
		"<transitGatewayVpcAttachment>"+tgwVpcAttachmentBodyXML(a)+"</transitGatewayVpcAttachment>")
}

func handleAcceptTransitGatewayVpcAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayAttachmentId")
	a, ok := ec2TGWVpcAttachments.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "attachment not found: "+id, http.StatusBadRequest)
		return
	}
	a.State = "available"
	ec2TGWVpcAttachments.Put(id, a)
	tgwResponse(w, "AcceptTransitGatewayVpcAttachment",
		"<transitGatewayVpcAttachment>"+tgwVpcAttachmentBodyXML(a)+"</transitGatewayVpcAttachment>")
}

func handleRejectTransitGatewayVpcAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayAttachmentId")
	a, ok := ec2TGWVpcAttachments.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "attachment not found: "+id, http.StatusBadRequest)
		return
	}
	a.State = "rejected"
	tgwRemoveAttachmentRefs(id)
	ec2TGWVpcAttachments.Put(id, a)
	tgwResponse(w, "RejectTransitGatewayVpcAttachment",
		"<transitGatewayVpcAttachment>"+tgwVpcAttachmentBodyXML(a)+"</transitGatewayVpcAttachment>")
}

func handleDescribeTransitGatewayAttachments(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayAttachmentIds")
	filters := ec2Filters(r)
	match := func(attachID, tgwID string) bool {
		if len(ids) > 0 && !ec2StrInValues(attachID, ids) {
			return false
		}
		if v, ok := filters["transit-gateway-id"]; ok && !ec2StrInValues(tgwID, v) {
			return false
		}
		return true
	}
	var b strings.Builder
	b.WriteString("<transitGatewayAttachments>")
	for _, a := range ec2TGWVpcAttachments.List() {
		if !match(a.TransitGatewayAttachmentId, a.TransitGatewayId) {
			continue
		}
		b.WriteString(tgwGenericAttachmentItemXML(a.TransitGatewayAttachmentId, a.TransitGatewayId,
			"vpc", a.VpcId, a.State, a.AssociationRouteTableId, a.AssociationState, a.CreationTime, a.Tags))
	}
	for _, p := range ec2TGWPeeringAttachments.List() {
		if !match(p.TransitGatewayAttachmentId, p.RequesterTgwId) {
			continue
		}
		b.WriteString(tgwGenericAttachmentItemXML(p.TransitGatewayAttachmentId, p.RequesterTgwId,
			"peering", p.AccepterTgwId, p.State, "", "", p.CreationTime, p.Tags))
	}
	for _, c := range ec2TGWConnects.List() {
		if !match(c.TransitGatewayAttachmentId, c.TransitGatewayId) {
			continue
		}
		b.WriteString(tgwGenericAttachmentItemXML(c.TransitGatewayAttachmentId, c.TransitGatewayId,
			"connect", c.TransportTransitGatewayAttachmentId, c.State, "", "", c.CreationTime, c.Tags))
	}
	b.WriteString("</transitGatewayAttachments>")
	tgwResponse(w, "DescribeTransitGatewayAttachments", b.String())
}

// ---- Associations + propagations ----

func tgwAddAssociation(rtID, attachID, resourceID, resourceType string) {
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		for i := range rt.Associations {
			if rt.Associations[i].TransitGatewayAttachmentId == attachID {
				rt.Associations[i].State = "associated"
				return
			}
		}
		rt.Associations = append(rt.Associations, EC2TGWRouteTableAssociation{
			TransitGatewayAttachmentId: attachID,
			ResourceId:                 resourceID,
			ResourceType:               resourceType,
			State:                      "associated",
		})
	})
}

func tgwAddPropagation(rtID, attachID, resourceID, resourceType string) {
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		for i := range rt.Propagations {
			if rt.Propagations[i].TransitGatewayAttachmentId == attachID {
				rt.Propagations[i].State = "enabled"
				return
			}
		}
		rt.Propagations = append(rt.Propagations, EC2TGWRouteTablePropagation{
			TransitGatewayAttachmentId: attachID,
			ResourceId:                 resourceID,
			ResourceType:               resourceType,
			State:                      "enabled",
		})
	})
}

// tgwRemoveAttachmentRefs drops every association/propagation that references a
// now-deleted attachment, across all route tables.
func tgwRemoveAttachmentRefs(attachID string) {
	for _, rt := range ec2TransitGatewayRouteTables.List() {
		ec2TransitGatewayRouteTables.Update(rt.TransitGatewayRouteTableId, func(rt *EC2TransitGatewayRouteTable) {
			var as []EC2TGWRouteTableAssociation
			for _, a := range rt.Associations {
				if a.TransitGatewayAttachmentId != attachID {
					as = append(as, a)
				}
			}
			rt.Associations = as
			var ps []EC2TGWRouteTablePropagation
			for _, p := range rt.Propagations {
				if p.TransitGatewayAttachmentId != attachID {
					ps = append(ps, p)
				}
			}
			rt.Propagations = ps
		})
	}
}

func tgwAttachmentResource(attachID string) (resourceID, resourceType string) {
	if a, ok := ec2TGWVpcAttachments.Get(attachID); ok {
		return a.VpcId, "vpc"
	}
	if _, ok := ec2TGWPeeringAttachments.Get(attachID); ok {
		return "", "peering"
	}
	if c, ok := ec2TGWConnects.Get(attachID); ok {
		return c.TransportTransitGatewayAttachmentId, "connect"
	}
	return "", "vpc"
}

func handleAssociateTransitGatewayRouteTable(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	attachID := r.FormValue("TransitGatewayAttachmentId")
	if _, ok := ec2TransitGatewayRouteTables.Get(rtID); !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	resID, resType := tgwAttachmentResource(attachID)
	tgwAddAssociation(rtID, attachID, resID, resType)
	if a, ok := ec2TGWVpcAttachments.Get(attachID); ok {
		a.AssociationRouteTableId = rtID
		a.AssociationState = "associated"
		ec2TGWVpcAttachments.Put(attachID, a)
	}
	body := fmt.Sprintf(`<association><transitGatewayRouteTableId>%s</transitGatewayRouteTableId>`+
		`<transitGatewayAttachmentId>%s</transitGatewayAttachmentId><resourceId>%s</resourceId>`+
		`<resourceType>%s</resourceType><state>associated</state></association>`,
		rtID, attachID, resID, resType)
	tgwResponse(w, "AssociateTransitGatewayRouteTable", body)
}

func handleDisassociateTransitGatewayRouteTable(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	attachID := r.FormValue("TransitGatewayAttachmentId")
	resID, resType := tgwAttachmentResource(attachID)
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		var as []EC2TGWRouteTableAssociation
		for _, a := range rt.Associations {
			if a.TransitGatewayAttachmentId != attachID {
				as = append(as, a)
			}
		}
		rt.Associations = as
	})
	if a, ok := ec2TGWVpcAttachments.Get(attachID); ok {
		a.AssociationRouteTableId = ""
		a.AssociationState = ""
		ec2TGWVpcAttachments.Put(attachID, a)
	}
	body := fmt.Sprintf(`<association><transitGatewayRouteTableId>%s</transitGatewayRouteTableId>`+
		`<transitGatewayAttachmentId>%s</transitGatewayAttachmentId><resourceId>%s</resourceId>`+
		`<resourceType>%s</resourceType><state>disassociated</state></association>`,
		rtID, attachID, resID, resType)
	tgwResponse(w, "DisassociateTransitGatewayRouteTable", body)
}

func handleGetTransitGatewayRouteTableAssociations(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	rt, ok := ec2TransitGatewayRouteTables.Get(rtID)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	var b strings.Builder
	b.WriteString("<associations>")
	for _, a := range rt.Associations {
		fmt.Fprintf(&b, `<item><transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
			`<resourceId>%s</resourceId><resourceType>%s</resourceType><state>%s</state></item>`,
			a.TransitGatewayAttachmentId, a.ResourceId, tgwOptOrDefault(a.ResourceType, "vpc"), a.State)
	}
	b.WriteString("</associations>")
	tgwResponse(w, "GetTransitGatewayRouteTableAssociations", b.String())
}

func handleEnableTransitGatewayRouteTablePropagation(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	attachID := r.FormValue("TransitGatewayAttachmentId")
	if _, ok := ec2TransitGatewayRouteTables.Get(rtID); !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	resID, resType := tgwAttachmentResource(attachID)
	tgwAddPropagation(rtID, attachID, resID, resType)
	body := fmt.Sprintf(`<propagation><transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
		`<resourceId>%s</resourceId><resourceType>%s</resourceType>`+
		`<transitGatewayRouteTableId>%s</transitGatewayRouteTableId><state>enabled</state></propagation>`,
		attachID, resID, resType, rtID)
	tgwResponse(w, "EnableTransitGatewayRouteTablePropagation", body)
}

func handleDisableTransitGatewayRouteTablePropagation(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	attachID := r.FormValue("TransitGatewayAttachmentId")
	resID, resType := tgwAttachmentResource(attachID)
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		var ps []EC2TGWRouteTablePropagation
		for _, p := range rt.Propagations {
			if p.TransitGatewayAttachmentId != attachID {
				ps = append(ps, p)
			}
		}
		rt.Propagations = ps
	})
	body := fmt.Sprintf(`<propagation><transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
		`<resourceId>%s</resourceId><resourceType>%s</resourceType>`+
		`<transitGatewayRouteTableId>%s</transitGatewayRouteTableId><state>disabled</state></propagation>`,
		attachID, resID, resType, rtID)
	tgwResponse(w, "DisableTransitGatewayRouteTablePropagation", body)
}

func handleGetTransitGatewayRouteTablePropagations(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	rt, ok := ec2TransitGatewayRouteTables.Get(rtID)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	var b strings.Builder
	b.WriteString("<transitGatewayRouteTablePropagations>")
	for _, p := range rt.Propagations {
		fmt.Fprintf(&b, `<item><transitGatewayAttachmentId>%s</transitGatewayAttachmentId>`+
			`<resourceId>%s</resourceId><resourceType>%s</resourceType><state>%s</state></item>`,
			p.TransitGatewayAttachmentId, p.ResourceId, tgwOptOrDefault(p.ResourceType, "vpc"), p.State)
	}
	b.WriteString("</transitGatewayRouteTablePropagations>")
	tgwResponse(w, "GetTransitGatewayRouteTablePropagations", b.String())
}

func handleGetTransitGatewayAttachmentPropagations(w http.ResponseWriter, r *http.Request) {
	attachID := r.FormValue("TransitGatewayAttachmentId")
	var b strings.Builder
	b.WriteString("<transitGatewayAttachmentPropagations>")
	for _, rt := range ec2TransitGatewayRouteTables.List() {
		for _, p := range rt.Propagations {
			if p.TransitGatewayAttachmentId == attachID {
				fmt.Fprintf(&b, `<item><transitGatewayRouteTableId>%s</transitGatewayRouteTableId>`+
					`<state>%s</state></item>`, rt.TransitGatewayRouteTableId, p.State)
			}
		}
	}
	b.WriteString("</transitGatewayAttachmentPropagations>")
	tgwResponse(w, "GetTransitGatewayAttachmentPropagations", b.String())
}

// ---- Routes ----

func handleCreateTransitGatewayRoute(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	if _, ok := ec2TransitGatewayRouteTables.Get(rtID); !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	attachID := r.FormValue("TransitGatewayAttachmentId")
	blackhole := ec2BoolStr(r.FormValue("Blackhole"))
	resID, resType := tgwAttachmentResource(attachID)
	route := EC2TransitGatewayRoute{
		DestinationCidrBlock: r.FormValue("DestinationCidrBlock"),
		Type:                 "static",
		State:                "active",
		ResourceId:           resID,
		ResourceType:         resType,
	}
	if blackhole {
		route.State = "blackhole"
	} else if attachID != "" {
		route.AttachmentIds = []string{attachID}
	}
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		rt.Routes = append(rt.Routes, route)
	})
	tgwResponse(w, "CreateTransitGatewayRoute", "<route>"+tgwRouteItemInnerXML(route)+"</route>")
}

func handleDeleteTransitGatewayRoute(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	cidr := r.FormValue("DestinationCidrBlock")
	var deleted EC2TransitGatewayRoute
	found := false
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		var kept []EC2TransitGatewayRoute
		for _, rte := range rt.Routes {
			if rte.DestinationCidrBlock == cidr && !found {
				deleted = rte
				deleted.State = "deleted"
				found = true
				continue
			}
			kept = append(kept, rte)
		}
		rt.Routes = kept
	})
	if !found {
		ec2ErrorXML(w, "InvalidRoute.NotFound", "route not found: "+cidr, http.StatusBadRequest)
		return
	}
	tgwResponse(w, "DeleteTransitGatewayRoute", "<route>"+tgwRouteItemInnerXML(deleted)+"</route>")
}

func handleReplaceTransitGatewayRoute(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	cidr := r.FormValue("DestinationCidrBlock")
	attachID := r.FormValue("TransitGatewayAttachmentId")
	blackhole := ec2BoolStr(r.FormValue("Blackhole"))
	resID, resType := tgwAttachmentResource(attachID)
	var replaced EC2TransitGatewayRoute
	found := false
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		for i := range rt.Routes {
			if rt.Routes[i].DestinationCidrBlock == cidr {
				rt.Routes[i].ResourceId = resID
				rt.Routes[i].ResourceType = resType
				if blackhole {
					rt.Routes[i].State = "blackhole"
					rt.Routes[i].AttachmentIds = nil
				} else {
					rt.Routes[i].State = "active"
					if attachID != "" {
						rt.Routes[i].AttachmentIds = []string{attachID}
					}
				}
				replaced = rt.Routes[i]
				found = true
				return
			}
		}
	})
	if !found {
		ec2ErrorXML(w, "InvalidRoute.NotFound", "route not found: "+cidr, http.StatusBadRequest)
		return
	}
	tgwResponse(w, "ReplaceTransitGatewayRoute", "<route>"+tgwRouteItemInnerXML(replaced)+"</route>")
}

func handleSearchTransitGatewayRoutes(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	rt, ok := ec2TransitGatewayRouteTables.Get(rtID)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	filters := ec2Filters(r)
	var b strings.Builder
	b.WriteString("<routeSet>")
	for _, rte := range rt.Routes {
		if v, ok := filters["type"]; ok && !ec2StrInValues(rte.Type, v) {
			continue
		}
		if v, ok := filters["state"]; ok && !ec2StrInValues(rte.State, v) {
			continue
		}
		b.WriteString(tgwRouteItemXML(rte))
	}
	b.WriteString("</routeSet>")
	b.WriteString("<additionalRoutesAvailable>false</additionalRoutesAvailable>")
	tgwResponse(w, "SearchTransitGatewayRoutes", b.String())
}

// tgwRouteItemInnerXML renders the inner body of a single <route> element (no
// wrapping <item>), used by Create/Delete/ReplaceTransitGatewayRoute.
func tgwRouteItemInnerXML(rt EC2TransitGatewayRoute) string {
	inner := tgwRouteItemXML(rt)
	inner = strings.TrimPrefix(inner, "<item>")
	inner = strings.TrimSuffix(inner, "</item>")
	return inner
}

func handleExportTransitGatewayRoutes(w http.ResponseWriter, r *http.Request) {
	bucket := r.FormValue("S3Bucket")
	rtID := r.FormValue("TransitGatewayRouteTableId")
	loc := fmt.Sprintf("s3://%s/vpc-transit-gateway/%s_%d.json", bucket, rtID, time.Now().Unix())
	tgwResponse(w, "ExportTransitGatewayRoutes", fmt.Sprintf("<s3Location>%s</s3Location>", loc))
}

// ---- Prefix list references ----

func handleCreateTransitGatewayPrefixListReference(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	if _, ok := ec2TransitGatewayRouteTables.Get(rtID); !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	attachID := r.FormValue("TransitGatewayAttachmentId")
	resID, resType := tgwAttachmentResource(attachID)
	ref := EC2TGWPrefixListReference{
		PrefixListId:               r.FormValue("PrefixListId"),
		PrefixListOwnerId:          ec2Owner(),
		State:                      "available",
		Blackhole:                  ec2BoolStr(r.FormValue("Blackhole")),
		TransitGatewayAttachmentId: attachID,
		ResourceId:                 resID,
		ResourceType:               resType,
	}
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		rt.PrefixListRefs = append(rt.PrefixListRefs, ref)
	})
	tgwResponse(w, "CreateTransitGatewayPrefixListReference",
		"<transitGatewayPrefixListReference>"+tgwPrefixListRefBodyXML(ref, rtID)+"</transitGatewayPrefixListReference>")
}

func handleGetTransitGatewayPrefixListReferences(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	rt, ok := ec2TransitGatewayRouteTables.Get(rtID)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	var b strings.Builder
	b.WriteString("<transitGatewayPrefixListReferenceSet>")
	for _, ref := range rt.PrefixListRefs {
		fmt.Fprintf(&b, "<item>%s</item>", tgwPrefixListRefBodyXML(ref, rtID))
	}
	b.WriteString("</transitGatewayPrefixListReferenceSet>")
	tgwResponse(w, "GetTransitGatewayPrefixListReferences", b.String())
}

func handleModifyTransitGatewayPrefixListReference(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	pfxID := r.FormValue("PrefixListId")
	attachID := r.FormValue("TransitGatewayAttachmentId")
	var modified EC2TGWPrefixListReference
	found := false
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		for i := range rt.PrefixListRefs {
			if rt.PrefixListRefs[i].PrefixListId == pfxID {
				if attachID != "" {
					rt.PrefixListRefs[i].TransitGatewayAttachmentId = attachID
					rt.PrefixListRefs[i].ResourceId, rt.PrefixListRefs[i].ResourceType = tgwAttachmentResource(attachID)
				}
				if r.FormValue("Blackhole") != "" {
					rt.PrefixListRefs[i].Blackhole = ec2BoolStr(r.FormValue("Blackhole"))
				}
				modified = rt.PrefixListRefs[i]
				found = true
				return
			}
		}
	})
	if !found {
		ec2ErrorXML(w, "InvalidPrefixListReference.NotFound", "prefix list reference not found: "+pfxID, http.StatusBadRequest)
		return
	}
	tgwResponse(w, "ModifyTransitGatewayPrefixListReference",
		"<transitGatewayPrefixListReference>"+tgwPrefixListRefBodyXML(modified, rtID)+"</transitGatewayPrefixListReference>")
}

func handleDeleteTransitGatewayPrefixListReference(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	pfxID := r.FormValue("PrefixListId")
	var deleted EC2TGWPrefixListReference
	found := false
	ec2TransitGatewayRouteTables.Update(rtID, func(rt *EC2TransitGatewayRouteTable) {
		var kept []EC2TGWPrefixListReference
		for _, ref := range rt.PrefixListRefs {
			if ref.PrefixListId == pfxID && !found {
				deleted = ref
				deleted.State = "deleting"
				found = true
				continue
			}
			kept = append(kept, ref)
		}
		rt.PrefixListRefs = kept
	})
	if !found {
		ec2ErrorXML(w, "InvalidPrefixListReference.NotFound", "prefix list reference not found: "+pfxID, http.StatusBadRequest)
		return
	}
	tgwResponse(w, "DeleteTransitGatewayPrefixListReference",
		"<transitGatewayPrefixListReference>"+tgwPrefixListRefBodyXML(deleted, rtID)+"</transitGatewayPrefixListReference>")
}

// ---- Peering attachments ----

func handleCreateTransitGatewayPeeringAttachment(w http.ResponseWriter, r *http.Request) {
	tgwID := r.FormValue("TransitGatewayId")
	if _, ok := ec2TransitGateways.Get(tgwID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayID.NotFound", "transit gateway not found: "+tgwID, http.StatusBadRequest)
		return
	}
	peerRegion := r.FormValue("PeerRegion")
	if peerRegion == "" {
		peerRegion = awsRegion()
	}
	peerOwner := r.FormValue("PeerAccountId")
	if peerOwner == "" {
		peerOwner = ec2Owner()
	}
	id := ec2ID("tgw-attach")
	p := EC2TransitGatewayPeeringAttachment{
		TransitGatewayAttachmentId: id,
		RequesterTgwId:             tgwID,
		RequesterRegion:            awsRegion(),
		RequesterOwnerId:           ec2Owner(),
		AccepterTgwId:              r.FormValue("PeerTransitGatewayId"),
		AccepterRegion:             peerRegion,
		AccepterOwnerId:            peerOwner,
		DynamicRouting:             tgwOptOrDefault(r.FormValue("Options.DynamicRouting"), "disable"),
		StatusCode:                 "pendingAcceptance",
		State:                      "pendingAcceptance",
		CreationTime:               ec2NowRFC3339Milli(),
		Tags:                       parseTags(r),
	}
	ec2TGWPeeringAttachments.Put(id, p)
	tgwResponse(w, "CreateTransitGatewayPeeringAttachment",
		"<transitGatewayPeeringAttachment>"+tgwPeeringBodyXML(p)+"</transitGatewayPeeringAttachment>")
}

func handleDescribeTransitGatewayPeeringAttachments(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayAttachmentIds")
	var b strings.Builder
	b.WriteString("<transitGatewayPeeringAttachments>")
	for _, p := range ec2TGWPeeringAttachments.List() {
		if len(ids) > 0 && !ec2StrInValues(p.TransitGatewayAttachmentId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwPeeringBodyXML(p))
	}
	b.WriteString("</transitGatewayPeeringAttachments>")
	tgwResponse(w, "DescribeTransitGatewayPeeringAttachments", b.String())
}

func handleAcceptTransitGatewayPeeringAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayAttachmentId")
	p, ok := ec2TGWPeeringAttachments.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "peering attachment not found: "+id, http.StatusBadRequest)
		return
	}
	p.State = "available"
	p.StatusCode = "available"
	ec2TGWPeeringAttachments.Put(id, p)
	tgwResponse(w, "AcceptTransitGatewayPeeringAttachment",
		"<transitGatewayPeeringAttachment>"+tgwPeeringBodyXML(p)+"</transitGatewayPeeringAttachment>")
}

func handleRejectTransitGatewayPeeringAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayAttachmentId")
	p, ok := ec2TGWPeeringAttachments.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "peering attachment not found: "+id, http.StatusBadRequest)
		return
	}
	p.State = "rejected"
	p.StatusCode = "rejected"
	ec2TGWPeeringAttachments.Put(id, p)
	tgwResponse(w, "RejectTransitGatewayPeeringAttachment",
		"<transitGatewayPeeringAttachment>"+tgwPeeringBodyXML(p)+"</transitGatewayPeeringAttachment>")
}

func handleDeleteTransitGatewayPeeringAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayAttachmentId")
	p, ok := ec2TGWPeeringAttachments.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "peering attachment not found: "+id, http.StatusBadRequest)
		return
	}
	p.State = "deleting"
	ec2TGWPeeringAttachments.Delete(id)
	tgwResponse(w, "DeleteTransitGatewayPeeringAttachment",
		"<transitGatewayPeeringAttachment>"+tgwPeeringBodyXML(p)+"</transitGatewayPeeringAttachment>")
}

// ---- Multicast domains ----

func handleCreateTransitGatewayMulticastDomain(w http.ResponseWriter, r *http.Request) {
	tgwID := r.FormValue("TransitGatewayId")
	if _, ok := ec2TransitGateways.Get(tgwID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayID.NotFound", "transit gateway not found: "+tgwID, http.StatusBadRequest)
		return
	}
	id := ec2ID("tgw-mcast-domain")
	m := EC2TransitGatewayMulticastDomain{
		TransitGatewayMulticastDomainId:  id,
		TransitGatewayId:                 tgwID,
		TransitGatewayMulticastDomainArn: tgwMcastArn(id),
		OwnerId:                          ec2Owner(),
		Igmpv2Support:                    tgwOptOrDefault(r.FormValue("Options.Igmpv2Support"), "disable"),
		StaticSourcesSupport:             tgwOptOrDefault(r.FormValue("Options.StaticSourcesSupport"), "disable"),
		AutoAcceptSharedAssociations:     tgwOptOrDefault(r.FormValue("Options.AutoAcceptSharedAssociations"), "disable"),
		State:                            "available",
		CreationTime:                     ec2NowRFC3339Milli(),
		Tags:                             parseTags(r),
	}
	ec2TGWMulticastDomains.Put(id, m)
	tgwResponse(w, "CreateTransitGatewayMulticastDomain",
		"<transitGatewayMulticastDomain>"+tgwMulticastBodyXML(m)+"</transitGatewayMulticastDomain>")
}

func handleDescribeTransitGatewayMulticastDomains(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayMulticastDomainIds")
	var b strings.Builder
	b.WriteString("<transitGatewayMulticastDomains>")
	for _, m := range ec2TGWMulticastDomains.List() {
		if len(ids) > 0 && !ec2StrInValues(m.TransitGatewayMulticastDomainId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwMulticastBodyXML(m))
	}
	b.WriteString("</transitGatewayMulticastDomains>")
	tgwResponse(w, "DescribeTransitGatewayMulticastDomains", b.String())
}

func handleDeleteTransitGatewayMulticastDomain(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayMulticastDomainId")
	m, ok := ec2TGWMulticastDomains.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMulticastDomainId.NotFound", "multicast domain not found: "+id, http.StatusBadRequest)
		return
	}
	m.State = "deleted"
	ec2TGWMulticastDomains.Delete(id)
	tgwResponse(w, "DeleteTransitGatewayMulticastDomain",
		"<transitGatewayMulticastDomain>"+tgwMulticastBodyXML(m)+"</transitGatewayMulticastDomain>")
}

// ---- Connect attachments ----

func handleCreateTransitGatewayConnect(w http.ResponseWriter, r *http.Request) {
	transportID := r.FormValue("TransportTransitGatewayAttachmentId")
	transport, ok := ec2TGWVpcAttachments.Get(transportID)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "transport attachment not found: "+transportID, http.StatusBadRequest)
		return
	}
	id := ec2ID("tgw-attach")
	c := EC2TransitGatewayConnect{
		TransitGatewayAttachmentId:          id,
		TransportTransitGatewayAttachmentId: transportID,
		TransitGatewayId:                    transport.TransitGatewayId,
		State:                               "available",
		CreationTime:                        ec2NowRFC3339Milli(),
		Protocol:                            tgwOptOrDefault(r.FormValue("Options.Protocol"), "gre"),
		Tags:                                parseTags(r),
	}
	ec2TGWConnects.Put(id, c)
	tgwResponse(w, "CreateTransitGatewayConnect",
		"<transitGatewayConnect>"+tgwConnectBodyXML(c)+"</transitGatewayConnect>")
}

func handleDescribeTransitGatewayConnects(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayAttachmentIds")
	var b strings.Builder
	b.WriteString("<transitGatewayConnectSet>")
	for _, c := range ec2TGWConnects.List() {
		if len(ids) > 0 && !ec2StrInValues(c.TransitGatewayAttachmentId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwConnectBodyXML(c))
	}
	b.WriteString("</transitGatewayConnectSet>")
	tgwResponse(w, "DescribeTransitGatewayConnects", b.String())
}

func handleDeleteTransitGatewayConnect(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayAttachmentId")
	c, ok := ec2TGWConnects.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "connect attachment not found: "+id, http.StatusBadRequest)
		return
	}
	c.State = "deleted"
	tgwRemoveAttachmentRefs(id)
	ec2TGWConnects.Delete(id)
	tgwResponse(w, "DeleteTransitGatewayConnect",
		"<transitGatewayConnect>"+tgwConnectBodyXML(c)+"</transitGatewayConnect>")
}
