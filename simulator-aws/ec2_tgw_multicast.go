package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// This file completes the Amazon EC2 Transit Gateway multicast, policy-table,
// metering-policy, and route-table-announcement families. The transit gateway
// core (gateways, route tables, attachments, multicast-domain CRUD) lives in
// ec2_transit_gateway.go; the stores declared there (ec2TransitGateways,
// ec2TGWVpcAttachments, ec2TGWMulticastDomains) are referenced read-only here.

// EC2TGWMulticastAssociation records a transit gateway multicast domain
// association: a TGW attachment plus the subnets associated with the domain.
type EC2TGWMulticastAssociation struct {
	TransitGatewayMulticastDomainId string
	TransitGatewayAttachmentId      string
	ResourceId                      string
	ResourceType                    string
	ResourceOwnerId                 string
	SubnetIds                       []string
	State                           string
}

// EC2TGWMulticastGroup records a registered multicast group member or source,
// keyed by group IP address + network interface.
type EC2TGWMulticastGroup struct {
	TransitGatewayMulticastDomainId string
	GroupIpAddress                  string
	NetworkInterfaceId              string
	TransitGatewayAttachmentId      string
	SubnetId                        string
	ResourceId                      string
	ResourceType                    string
	ResourceOwnerId                 string
	GroupMember                     bool
	GroupSource                     bool
	MemberType                      string
	SourceType                      string
}

// EC2TGWPolicyTable records a transit gateway policy table (tgw-pt-…).
type EC2TGWPolicyTable struct {
	TransitGatewayPolicyTableId string
	TransitGatewayId            string
	State                       string
	CreationTime                string
	Tags                        []EC2Tag
}

// EC2TGWPolicyTableAssociation records a policy-table↔attachment association.
type EC2TGWPolicyTableAssociation struct {
	TransitGatewayPolicyTableId string
	TransitGatewayAttachmentId  string
	ResourceId                  string
	ResourceType                string
	State                       string
}

// EC2TGWMeteringPolicy records a transit gateway metering policy (tgw-mp-…).
type EC2TGWMeteringPolicy struct {
	TransitGatewayMeteringPolicyId string
	TransitGatewayId               string
	MiddleboxAttachmentIds         []string
	State                          string
	UpdateEffectiveAt              string
	Tags                           []EC2Tag
}

// EC2TGWPolicyTableEntry records one policy rule in a transit gateway policy
// table. A rule matches traffic on the five-tuple and carries metadata that
// selects the route table the matched traffic is forwarded to; the rule number
// identifies the entry within its table.
type EC2TGWPolicyTableEntry struct {
	TransitGatewayPolicyTableId string
	PolicyRuleNumber            string
	TargetRouteTableId          string
	State                       string
	// Policy rule criteria.
	SourceCidrBlock      string
	SourcePortRange      string
	DestinationCidrBlock string
	DestinationPortRange string
	Protocol             string
	MetaDataKey          string
	MetaDataValue        string
}

// EC2TGWMeteringPolicyEntry records a metering policy entry (a metering rule).
type EC2TGWMeteringPolicyEntry struct {
	TransitGatewayMeteringPolicyId string
	PolicyRuleNumber               string
	MeteredAccount                 string
	State                          string
	UpdatedAt                      string
	UpdateEffectiveAt              string
	// Metering policy rule criteria.
	SourceCidrBlock                       string
	DestinationCidrBlock                  string
	SourceTransitGatewayAttachmentId      string
	DestinationTransitGatewayAttachmentId string
	Protocol                              string
	SourcePortRange                       string
	DestinationPortRange                  string
}

// EC2TGWRouteTableAnnouncement records a route table announcement (tgw-rta-…).
type EC2TGWRouteTableAnnouncement struct {
	TransitGatewayRouteTableAnnouncementId string
	TransitGatewayId                       string
	TransitGatewayRouteTableId             string
	PeeringAttachmentId                    string
	AnnouncementDirection                  string
	State                                  string
	CreationTime                           string
	Tags                                   []EC2Tag
}

var (
	ec2TGWMulticastAssociations  sim.Store[EC2TGWMulticastAssociation]
	ec2TGWMulticastGroups        sim.Store[EC2TGWMulticastGroup]
	ec2TGWPolicyTables           sim.Store[EC2TGWPolicyTable]
	ec2TGWPolicyTableAssocs      sim.Store[EC2TGWPolicyTableAssociation]
	ec2TGWPolicyTableEntries     sim.Store[EC2TGWPolicyTableEntry]
	ec2TGWMeteringPolicies       sim.Store[EC2TGWMeteringPolicy]
	ec2TGWMeteringPolicyEntries  sim.Store[EC2TGWMeteringPolicyEntry]
	ec2TGWRouteTableAnnouncement sim.Store[EC2TGWRouteTableAnnouncement]
)

func registerEC2TGWMulticast(r *AWSQueryRouter, srv *sim.Server) {
	ec2TGWMulticastAssociations = sim.MakeStore[EC2TGWMulticastAssociation](srv.DB(), "ec2_tgw_multicast_associations")
	ec2TGWMulticastGroups = sim.MakeStore[EC2TGWMulticastGroup](srv.DB(), "ec2_tgw_multicast_groups")
	ec2TGWPolicyTables = sim.MakeStore[EC2TGWPolicyTable](srv.DB(), "ec2_tgw_policy_tables")
	ec2TGWPolicyTableAssocs = sim.MakeStore[EC2TGWPolicyTableAssociation](srv.DB(), "ec2_tgw_policy_table_associations")
	ec2TGWPolicyTableEntries = sim.MakeStore[EC2TGWPolicyTableEntry](srv.DB(), "ec2_tgw_policy_table_entries")
	ec2TGWMeteringPolicies = sim.MakeStore[EC2TGWMeteringPolicy](srv.DB(), "ec2_tgw_metering_policies")
	ec2TGWMeteringPolicyEntries = sim.MakeStore[EC2TGWMeteringPolicyEntry](srv.DB(), "ec2_tgw_metering_policy_entries")
	ec2TGWRouteTableAnnouncement = sim.MakeStore[EC2TGWRouteTableAnnouncement](srv.DB(), "ec2_tgw_route_table_announcements")

	// Multicast domain associations + groups.
	r.Register("AssociateTransitGatewayMulticastDomain", handleAssociateTransitGatewayMulticastDomain)
	r.Register("DisassociateTransitGatewayMulticastDomain", handleDisassociateTransitGatewayMulticastDomain)
	r.Register("AcceptTransitGatewayMulticastDomainAssociations", handleAcceptTransitGatewayMulticastDomainAssociations)
	r.Register("RejectTransitGatewayMulticastDomainAssociations", handleRejectTransitGatewayMulticastDomainAssociations)
	r.Register("GetTransitGatewayMulticastDomainAssociations", handleGetTransitGatewayMulticastDomainAssociations)
	r.Register("RegisterTransitGatewayMulticastGroupMembers", handleRegisterTransitGatewayMulticastGroupMembers)
	r.Register("DeregisterTransitGatewayMulticastGroupMembers", handleDeregisterTransitGatewayMulticastGroupMembers)
	r.Register("RegisterTransitGatewayMulticastGroupSources", handleRegisterTransitGatewayMulticastGroupSources)
	r.Register("DeregisterTransitGatewayMulticastGroupSources", handleDeregisterTransitGatewayMulticastGroupSources)
	r.Register("SearchTransitGatewayMulticastGroups", handleSearchTransitGatewayMulticastGroups)

	// Policy tables.
	r.Register("CreateTransitGatewayPolicyTable", handleCreateTransitGatewayPolicyTable)
	r.Register("DescribeTransitGatewayPolicyTables", handleDescribeTransitGatewayPolicyTables)
	r.Register("DeleteTransitGatewayPolicyTable", handleDeleteTransitGatewayPolicyTable)
	r.Register("AssociateTransitGatewayPolicyTable", handleAssociateTransitGatewayPolicyTable)
	r.Register("DisassociateTransitGatewayPolicyTable", handleDisassociateTransitGatewayPolicyTable)
	r.Register("GetTransitGatewayPolicyTableAssociations", handleGetTransitGatewayPolicyTableAssociations)
	r.Register("GetTransitGatewayPolicyTableEntries", handleGetTransitGatewayPolicyTableEntries)
	r.Register("CreateTransitGatewayPolicyTableEntry", handleCreateTransitGatewayPolicyTableEntry)
	r.Register("ModifyTransitGatewayPolicyTableEntry", handleModifyTransitGatewayPolicyTableEntry)
	r.Register("DeleteTransitGatewayPolicyTableEntry", handleDeleteTransitGatewayPolicyTableEntry)

	// Metering policies.
	r.Register("CreateTransitGatewayMeteringPolicy", handleCreateTransitGatewayMeteringPolicy)
	r.Register("ModifyTransitGatewayMeteringPolicy", handleModifyTransitGatewayMeteringPolicy)
	r.Register("DeleteTransitGatewayMeteringPolicy", handleDeleteTransitGatewayMeteringPolicy)
	r.Register("CreateTransitGatewayMeteringPolicyEntry", handleCreateTransitGatewayMeteringPolicyEntry)
	r.Register("DeleteTransitGatewayMeteringPolicyEntry", handleDeleteTransitGatewayMeteringPolicyEntry)
	r.Register("GetTransitGatewayMeteringPolicyEntries", handleGetTransitGatewayMeteringPolicyEntries)

	// Route table announcements.
	r.Register("CreateTransitGatewayRouteTableAnnouncement", handleCreateTransitGatewayRouteTableAnnouncement)
	r.Register("DescribeTransitGatewayRouteTableAnnouncements", handleDescribeTransitGatewayRouteTableAnnouncements)
	r.Register("DeleteTransitGatewayRouteTableAnnouncement", handleDeleteTransitGatewayRouteTableAnnouncement)
}

// Multicast domain associations

func handleAssociateTransitGatewayMulticastDomain(w http.ResponseWriter, r *http.Request) {
	domainID := r.FormValue("TransitGatewayMulticastDomainId")
	if _, ok := ec2TGWMulticastDomains.Get(domainID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMulticastDomainId.NotFound", "multicast domain not found: "+domainID, http.StatusBadRequest)
		return
	}
	attID := r.FormValue("TransitGatewayAttachmentId")
	att, ok := ec2TGWVpcAttachments.Get(attID)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "transit gateway attachment not found: "+attID, http.StatusBadRequest)
		return
	}
	subnets := ec2ParamList(r, "SubnetIds")

	assoc := EC2TGWMulticastAssociation{
		TransitGatewayMulticastDomainId: domainID,
		TransitGatewayAttachmentId:      attID,
		ResourceId:                      att.VpcId,
		ResourceType:                    "vpc",
		ResourceOwnerId:                 ec2Owner(),
		SubnetIds:                       subnets,
		State:                           "associated",
	}
	ec2TGWMulticastAssociations.Put(domainID+"/"+attID, assoc)
	tgwResponse(w, "AssociateTransitGatewayMulticastDomain",
		"<associations>"+tgwMulticastAssociationsBodyXML(assoc)+"</associations>")
}

func handleDisassociateTransitGatewayMulticastDomain(w http.ResponseWriter, r *http.Request) {
	domainID := r.FormValue("TransitGatewayMulticastDomainId")
	attID := r.FormValue("TransitGatewayAttachmentId")
	key := domainID + "/" + attID
	assoc, ok := ec2TGWMulticastAssociations.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMulticastDomainAssociation.NotFound",
			"association not found for domain "+domainID+" attachment "+attID, http.StatusBadRequest)
		return
	}
	// Restrict to the subnets named on the disassociate call, if any.
	if reqSubnets := ec2ParamList(r, "SubnetIds"); len(reqSubnets) > 0 {
		assoc.SubnetIds = reqSubnets
	}
	assoc.State = "disassociated"
	ec2TGWMulticastAssociations.Delete(key)
	tgwResponse(w, "DisassociateTransitGatewayMulticastDomain",
		"<associations>"+tgwMulticastAssociationsBodyXML(assoc)+"</associations>")
}

func handleAcceptTransitGatewayMulticastDomainAssociations(w http.ResponseWriter, r *http.Request) {
	domainID := r.FormValue("TransitGatewayMulticastDomainId")
	attID := r.FormValue("TransitGatewayAttachmentId")
	assoc, ok := ec2TGWMulticastAssociations.Get(domainID + "/" + attID)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMulticastDomainAssociation.NotFound",
			"association not found for domain "+domainID+" attachment "+attID, http.StatusBadRequest)
		return
	}
	assoc.State = "associated"
	ec2TGWMulticastAssociations.Put(domainID+"/"+attID, assoc)
	tgwResponse(w, "AcceptTransitGatewayMulticastDomainAssociations",
		"<associations>"+tgwMulticastAssociationsBodyXML(assoc)+"</associations>")
}

func handleRejectTransitGatewayMulticastDomainAssociations(w http.ResponseWriter, r *http.Request) {
	domainID := r.FormValue("TransitGatewayMulticastDomainId")
	attID := r.FormValue("TransitGatewayAttachmentId")
	key := domainID + "/" + attID
	assoc, ok := ec2TGWMulticastAssociations.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMulticastDomainAssociation.NotFound",
			"association not found for domain "+domainID+" attachment "+attID, http.StatusBadRequest)
		return
	}
	assoc.State = "disassociated"
	ec2TGWMulticastAssociations.Delete(key)
	tgwResponse(w, "RejectTransitGatewayMulticastDomainAssociations",
		"<associations>"+tgwMulticastAssociationsBodyXML(assoc)+"</associations>")
}

func handleGetTransitGatewayMulticastDomainAssociations(w http.ResponseWriter, r *http.Request) {
	domainID := r.FormValue("TransitGatewayMulticastDomainId")
	var b strings.Builder
	b.WriteString("<multicastDomainAssociations>")
	for _, a := range ec2TGWMulticastAssociations.List() {
		if domainID != "" && a.TransitGatewayMulticastDomainId != domainID {
			continue
		}
		// Each subnet is its own item in the flattened association list.
		for _, sn := range a.SubnetIds {
			fmt.Fprintf(&b, "<item>%s</item>", tgwMulticastDomainAssociationItemXML(a, sn))
		}
		if len(a.SubnetIds) == 0 {
			fmt.Fprintf(&b, "<item>%s</item>", tgwMulticastDomainAssociationItemXML(a, ""))
		}
	}
	b.WriteString("</multicastDomainAssociations>")
	tgwResponse(w, "GetTransitGatewayMulticastDomainAssociations", b.String())
}

func handleRegisterTransitGatewayMulticastGroupMembers(w http.ResponseWriter, r *http.Request) {
	registerMulticastGroup(w, r, true,
		"RegisterTransitGatewayMulticastGroupMembers", "registeredMulticastGroupMembers", "registeredNetworkInterfaceIds")
}

func handleRegisterTransitGatewayMulticastGroupSources(w http.ResponseWriter, r *http.Request) {
	registerMulticastGroup(w, r, false,
		"RegisterTransitGatewayMulticastGroupSources", "registeredMulticastGroupSources", "registeredNetworkInterfaceIds")
}

func registerMulticastGroup(w http.ResponseWriter, r *http.Request, member bool, action, wrapper, idsTag string) {
	domainID := r.FormValue("TransitGatewayMulticastDomainId")
	if _, ok := ec2TGWMulticastDomains.Get(domainID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMulticastDomainId.NotFound", "multicast domain not found: "+domainID, http.StatusBadRequest)
		return
	}
	groupIP := r.FormValue("GroupIpAddress")
	enis := ec2ParamList(r, "NetworkInterfaceIds")
	for _, eni := range enis {
		g := EC2TGWMulticastGroup{
			TransitGatewayMulticastDomainId: domainID,
			GroupIpAddress:                  groupIP,
			NetworkInterfaceId:              eni,
			ResourceType:                    "vpc",
			ResourceOwnerId:                 ec2Owner(),
			GroupMember:                     member,
			GroupSource:                     !member,
			MemberType:                      "static",
			SourceType:                      "static",
		}
		ec2TGWMulticastGroups.Put(multicastGroupKey(domainID, groupIP, eni, member), g)
	}
	tgwResponse(w, action, fmt.Sprintf("<%s>%s</%s>", wrapper,
		tgwRegisteredGroupBodyXML(domainID, groupIP, enis, idsTag), wrapper))
}

func handleDeregisterTransitGatewayMulticastGroupMembers(w http.ResponseWriter, r *http.Request) {
	deregisterMulticastGroup(w, r, true,
		"DeregisterTransitGatewayMulticastGroupMembers", "deregisteredMulticastGroupMembers", "deregisteredNetworkInterfaceIds")
}

func handleDeregisterTransitGatewayMulticastGroupSources(w http.ResponseWriter, r *http.Request) {
	deregisterMulticastGroup(w, r, false,
		"DeregisterTransitGatewayMulticastGroupSources", "deregisteredMulticastGroupSources", "deregisteredNetworkInterfaceIds")
}

func deregisterMulticastGroup(w http.ResponseWriter, r *http.Request, member bool, action, wrapper, idsTag string) {
	domainID := r.FormValue("TransitGatewayMulticastDomainId")
	if _, ok := ec2TGWMulticastDomains.Get(domainID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMulticastDomainId.NotFound", "multicast domain not found: "+domainID, http.StatusBadRequest)
		return
	}
	groupIP := r.FormValue("GroupIpAddress")
	enis := ec2ParamList(r, "NetworkInterfaceIds")
	for _, eni := range enis {
		ec2TGWMulticastGroups.Delete(multicastGroupKey(domainID, groupIP, eni, member))
	}
	tgwResponse(w, action, fmt.Sprintf("<%s>%s</%s>", wrapper,
		tgwRegisteredGroupBodyXML(domainID, groupIP, enis, idsTag), wrapper))
}

func handleSearchTransitGatewayMulticastGroups(w http.ResponseWriter, r *http.Request) {
	domainID := r.FormValue("TransitGatewayMulticastDomainId")
	if _, ok := ec2TGWMulticastDomains.Get(domainID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMulticastDomainId.NotFound", "multicast domain not found: "+domainID, http.StatusBadRequest)
		return
	}
	var b strings.Builder
	b.WriteString("<multicastGroups>")
	for _, g := range ec2TGWMulticastGroups.List() {
		if g.TransitGatewayMulticastDomainId != domainID {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwMulticastGroupItemXML(g))
	}
	b.WriteString("</multicastGroups>")
	tgwResponse(w, "SearchTransitGatewayMulticastGroups", b.String())
}

func multicastGroupKey(domainID, groupIP, eni string, member bool) string {
	role := "src"
	if member {
		role = "mem"
	}
	return strings.Join([]string{domainID, groupIP, eni, role}, "/")
}

// Policy tables

func handleCreateTransitGatewayPolicyTable(w http.ResponseWriter, r *http.Request) {
	tgwID := r.FormValue("TransitGatewayId")
	if _, ok := ec2TransitGateways.Get(tgwID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayID.NotFound", "transit gateway not found: "+tgwID, http.StatusBadRequest)
		return
	}
	id := ec2ID("tgw-pt")
	pt := EC2TGWPolicyTable{
		TransitGatewayPolicyTableId: id,
		TransitGatewayId:            tgwID,
		State:                       "available",
		CreationTime:                ec2NowRFC3339Milli(),
		Tags:                        parseTagsTGW(r),
	}
	ec2TGWPolicyTables.Put(id, pt)
	tgwResponse(w, "CreateTransitGatewayPolicyTable",
		"<transitGatewayPolicyTable>"+tgwPolicyTableBodyXML(pt)+"</transitGatewayPolicyTable>")
}

func handleDescribeTransitGatewayPolicyTables(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayPolicyTableIds")
	var b strings.Builder
	b.WriteString("<transitGatewayPolicyTables>")
	for _, pt := range ec2TGWPolicyTables.List() {
		if len(ids) > 0 && !ec2StrInValues(pt.TransitGatewayPolicyTableId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwPolicyTableBodyXML(pt))
	}
	b.WriteString("</transitGatewayPolicyTables>")
	tgwResponse(w, "DescribeTransitGatewayPolicyTables", b.String())
}

func handleDeleteTransitGatewayPolicyTable(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayPolicyTableId")
	pt, ok := ec2TGWPolicyTables.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayPolicyTableId.NotFound", "policy table not found: "+id, http.StatusBadRequest)
		return
	}
	pt.State = "deleted"
	ec2TGWPolicyTables.Delete(id)
	tgwResponse(w, "DeleteTransitGatewayPolicyTable",
		"<transitGatewayPolicyTable>"+tgwPolicyTableBodyXML(pt)+"</transitGatewayPolicyTable>")
}

func handleAssociateTransitGatewayPolicyTable(w http.ResponseWriter, r *http.Request) {
	ptID := r.FormValue("TransitGatewayPolicyTableId")
	if _, ok := ec2TGWPolicyTables.Get(ptID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayPolicyTableId.NotFound", "policy table not found: "+ptID, http.StatusBadRequest)
		return
	}
	attID := r.FormValue("TransitGatewayAttachmentId")
	att, ok := ec2TGWVpcAttachments.Get(attID)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "transit gateway attachment not found: "+attID, http.StatusBadRequest)
		return
	}
	assoc := EC2TGWPolicyTableAssociation{
		TransitGatewayPolicyTableId: ptID,
		TransitGatewayAttachmentId:  attID,
		ResourceId:                  att.VpcId,
		ResourceType:                "vpc",
		State:                       "associated",
	}
	ec2TGWPolicyTableAssocs.Put(ptID+"/"+attID, assoc)
	tgwResponse(w, "AssociateTransitGatewayPolicyTable",
		"<association>"+tgwPolicyTableAssociationBodyXML(assoc)+"</association>")
}

func handleDisassociateTransitGatewayPolicyTable(w http.ResponseWriter, r *http.Request) {
	ptID := r.FormValue("TransitGatewayPolicyTableId")
	attID := r.FormValue("TransitGatewayAttachmentId")
	key := ptID + "/" + attID
	assoc, ok := ec2TGWPolicyTableAssocs.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayPolicyTableAssociation.NotFound",
			"association not found for policy table "+ptID+" attachment "+attID, http.StatusBadRequest)
		return
	}
	assoc.State = "disassociated"
	ec2TGWPolicyTableAssocs.Delete(key)
	tgwResponse(w, "DisassociateTransitGatewayPolicyTable",
		"<association>"+tgwPolicyTableAssociationBodyXML(assoc)+"</association>")
}

func handleGetTransitGatewayPolicyTableAssociations(w http.ResponseWriter, r *http.Request) {
	ptID := r.FormValue("TransitGatewayPolicyTableId")
	var b strings.Builder
	b.WriteString("<associations>")
	for _, a := range ec2TGWPolicyTableAssocs.List() {
		if ptID != "" && a.TransitGatewayPolicyTableId != ptID {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwPolicyTableAssociationBodyXML(a))
	}
	b.WriteString("</associations>")
	tgwResponse(w, "GetTransitGatewayPolicyTableAssociations", b.String())
}

func handleGetTransitGatewayPolicyTableEntries(w http.ResponseWriter, r *http.Request) {
	ptID := r.FormValue("TransitGatewayPolicyTableId")
	if _, ok := ec2TGWPolicyTables.Get(ptID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayPolicyTableId.NotFound", "policy table not found: "+ptID, http.StatusBadRequest)
		return
	}
	entries := ec2TGWPolicyTableEntries.Filter(func(e EC2TGWPolicyTableEntry) bool {
		return e.TransitGatewayPolicyTableId == ptID
	})
	sort.Slice(entries, func(i, j int) bool {
		return policyRuleOrder(entries[i].PolicyRuleNumber, entries[j].PolicyRuleNumber)
	})
	var b strings.Builder
	b.WriteString("<transitGatewayPolicyTableEntries>")
	for _, e := range entries {
		fmt.Fprintf(&b, "<item>%s</item>", tgwPolicyTableEntryBodyXML(e))
	}
	b.WriteString("</transitGatewayPolicyTableEntries>")
	tgwResponse(w, "GetTransitGatewayPolicyTableEntries", b.String())
}

// policyRuleOrder compares two rule numbers the way a policy table evaluates
// them: numerically, since a rule number is a decimal string and "10" follows
// "9" rather than preceding it. A value that is not a number sorts after every
// number, by its own string order.
func policyRuleOrder(a, b string) bool {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return an < bn
	case aErr == nil:
		return true
	case bErr == nil:
		return false
	}
	return a < b
}

// policyTableEntryRule reads the rule criteria a create or modify request
// carries. The wire flattens TransitGatewayRequestPolicyRule's members under
// the "PolicyRule." prefix.
func policyTableEntryRule(r *http.Request, e *EC2TGWPolicyTableEntry) {
	e.SourceCidrBlock = r.FormValue("PolicyRule.SourceCidrBlock")
	e.SourcePortRange = r.FormValue("PolicyRule.SourcePortRange")
	e.DestinationCidrBlock = r.FormValue("PolicyRule.DestinationCidrBlock")
	e.DestinationPortRange = r.FormValue("PolicyRule.DestinationPortRange")
	e.Protocol = r.FormValue("PolicyRule.Protocol")
	e.MetaDataKey = r.FormValue("PolicyRule.MetaData.MetaDataKey")
	e.MetaDataValue = r.FormValue("PolicyRule.MetaData.MetaDataValue")
}

func handleCreateTransitGatewayPolicyTableEntry(w http.ResponseWriter, r *http.Request) {
	ptID := r.FormValue("TransitGatewayPolicyTableId")
	if _, ok := ec2TGWPolicyTables.Get(ptID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayPolicyTableId.NotFound", "policy table not found: "+ptID, http.StatusBadRequest)
		return
	}
	ruleNum := r.FormValue("PolicyRuleNumber")
	key := policyTableEntryKey(ptID, ruleNum)
	if _, exists := ec2TGWPolicyTableEntries.Get(key); exists {
		ec2ErrorXML(w, "TransitGatewayPolicyTableEntry.Duplicate",
			"policy rule "+ruleNum+" already exists in policy table "+ptID, http.StatusBadRequest)
		return
	}
	targetRT := r.FormValue("TargetRouteTableId")
	if _, ok := ec2TransitGatewayRouteTables.Get(targetRT); !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+targetRT, http.StatusBadRequest)
		return
	}
	e := EC2TGWPolicyTableEntry{
		TransitGatewayPolicyTableId: ptID,
		PolicyRuleNumber:            ruleNum,
		TargetRouteTableId:          targetRT,
		State:                       "active",
	}
	policyTableEntryRule(r, &e)
	ec2TGWPolicyTableEntries.Put(key, e)
	tgwResponse(w, "CreateTransitGatewayPolicyTableEntry",
		"<transitGatewayPolicyTableEntry>"+tgwPolicyTableEntryBodyXML(e)+"</transitGatewayPolicyTableEntry>")
}

func handleModifyTransitGatewayPolicyTableEntry(w http.ResponseWriter, r *http.Request) {
	ptID := r.FormValue("TransitGatewayPolicyTableId")
	ruleNum := r.FormValue("PolicyRuleNumber")
	key := policyTableEntryKey(ptID, ruleNum)
	e, ok := ec2TGWPolicyTableEntries.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayPolicyTableEntry.NotFound",
			"policy table entry not found: "+ptID+" rule "+ruleNum, http.StatusBadRequest)
		return
	}
	// TargetRouteTableId is optional on modify: an omitted value leaves the
	// entry pointing where it already pointed.
	if targetRT := r.FormValue("TargetRouteTableId"); targetRT != "" {
		if _, exists := ec2TransitGatewayRouteTables.Get(targetRT); !exists {
			ec2ErrorXML(w, "InvalidRouteTableID.NotFound", "transit gateway route table not found: "+targetRT, http.StatusBadRequest)
			return
		}
		e.TargetRouteTableId = targetRT
	}
	policyTableEntryRule(r, &e)
	ec2TGWPolicyTableEntries.Put(key, e)
	tgwResponse(w, "ModifyTransitGatewayPolicyTableEntry",
		"<transitGatewayPolicyTableEntry>"+tgwPolicyTableEntryBodyXML(e)+"</transitGatewayPolicyTableEntry>")
}

func handleDeleteTransitGatewayPolicyTableEntry(w http.ResponseWriter, r *http.Request) {
	ptID := r.FormValue("TransitGatewayPolicyTableId")
	ruleNum := r.FormValue("PolicyRuleNumber")
	key := policyTableEntryKey(ptID, ruleNum)
	e, ok := ec2TGWPolicyTableEntries.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayPolicyTableEntry.NotFound",
			"policy table entry not found: "+ptID+" rule "+ruleNum, http.StatusBadRequest)
		return
	}
	ec2TGWPolicyTableEntries.Delete(key)
	e.State = "deleted"
	tgwResponse(w, "DeleteTransitGatewayPolicyTableEntry",
		"<transitGatewayPolicyTableEntry>"+tgwPolicyTableEntryBodyXML(e)+"</transitGatewayPolicyTableEntry>")
}

func policyTableEntryKey(ptID, ruleNum string) string { return ptID + "/" + ruleNum }

// Metering policies

func handleCreateTransitGatewayMeteringPolicy(w http.ResponseWriter, r *http.Request) {
	tgwID := r.FormValue("TransitGatewayId")
	if _, ok := ec2TransitGateways.Get(tgwID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayID.NotFound", "transit gateway not found: "+tgwID, http.StatusBadRequest)
		return
	}
	id := ec2ID("tgw-mp")
	mp := EC2TGWMeteringPolicy{
		TransitGatewayMeteringPolicyId: id,
		TransitGatewayId:               tgwID,
		MiddleboxAttachmentIds:         ec2ParamList(r, "MiddleboxAttachmentId"),
		State:                          "available",
		UpdateEffectiveAt:              ec2NowRFC3339Milli(),
		Tags:                           parseTagsTGW(r),
	}
	ec2TGWMeteringPolicies.Put(id, mp)
	tgwResponse(w, "CreateTransitGatewayMeteringPolicy",
		"<transitGatewayMeteringPolicy>"+tgwMeteringPolicyBodyXML(mp)+"</transitGatewayMeteringPolicy>")
}

func handleModifyTransitGatewayMeteringPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayMeteringPolicyId")
	mp, ok := ec2TGWMeteringPolicies.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMeteringPolicyId.NotFound", "metering policy not found: "+id, http.StatusBadRequest)
		return
	}
	add := ec2ParamList(r, "AddMiddleboxAttachmentId")
	remove := ec2ParamList(r, "RemoveMiddleboxAttachmentId")
	if len(add) > 0 || len(remove) > 0 {
		var kept []string
		for _, m := range mp.MiddleboxAttachmentIds {
			if !ec2StrInValues(m, remove) {
				kept = append(kept, m)
			}
		}
		for _, m := range add {
			if !ec2StrInValues(m, kept) {
				kept = append(kept, m)
			}
		}
		mp.MiddleboxAttachmentIds = kept
	}
	mp.UpdateEffectiveAt = ec2NowRFC3339Milli()
	ec2TGWMeteringPolicies.Put(id, mp)
	tgwResponse(w, "ModifyTransitGatewayMeteringPolicy",
		"<transitGatewayMeteringPolicy>"+tgwMeteringPolicyBodyXML(mp)+"</transitGatewayMeteringPolicy>")
}

func handleDeleteTransitGatewayMeteringPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayMeteringPolicyId")
	mp, ok := ec2TGWMeteringPolicies.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMeteringPolicyId.NotFound", "metering policy not found: "+id, http.StatusBadRequest)
		return
	}
	mp.State = "deleted"
	ec2TGWMeteringPolicies.Delete(id)
	// Cascade-delete its entries.
	for _, e := range ec2TGWMeteringPolicyEntries.List() {
		if e.TransitGatewayMeteringPolicyId == id {
			ec2TGWMeteringPolicyEntries.Delete(meteringEntryKey(id, e.PolicyRuleNumber))
		}
	}
	tgwResponse(w, "DeleteTransitGatewayMeteringPolicy",
		"<transitGatewayMeteringPolicy>"+tgwMeteringPolicyBodyXML(mp)+"</transitGatewayMeteringPolicy>")
}

func handleCreateTransitGatewayMeteringPolicyEntry(w http.ResponseWriter, r *http.Request) {
	mpID := r.FormValue("TransitGatewayMeteringPolicyId")
	if _, ok := ec2TGWMeteringPolicies.Get(mpID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMeteringPolicyId.NotFound", "metering policy not found: "+mpID, http.StatusBadRequest)
		return
	}
	now := ec2NowRFC3339Milli()
	e := EC2TGWMeteringPolicyEntry{
		TransitGatewayMeteringPolicyId:        mpID,
		PolicyRuleNumber:                      r.FormValue("PolicyRuleNumber"),
		MeteredAccount:                        r.FormValue("MeteredAccount"),
		State:                                 "available",
		UpdatedAt:                             now,
		UpdateEffectiveAt:                     now,
		SourceCidrBlock:                       r.FormValue("SourceCidrBlock"),
		DestinationCidrBlock:                  r.FormValue("DestinationCidrBlock"),
		SourceTransitGatewayAttachmentId:      r.FormValue("SourceTransitGatewayAttachmentId"),
		DestinationTransitGatewayAttachmentId: r.FormValue("DestinationTransitGatewayAttachmentId"),
		Protocol:                              r.FormValue("Protocol"),
		SourcePortRange:                       r.FormValue("SourcePortRange"),
		DestinationPortRange:                  r.FormValue("DestinationPortRange"),
	}
	ec2TGWMeteringPolicyEntries.Put(meteringEntryKey(mpID, e.PolicyRuleNumber), e)
	tgwResponse(w, "CreateTransitGatewayMeteringPolicyEntry",
		"<transitGatewayMeteringPolicyEntry>"+tgwMeteringPolicyEntryBodyXML(e)+"</transitGatewayMeteringPolicyEntry>")
}

func handleDeleteTransitGatewayMeteringPolicyEntry(w http.ResponseWriter, r *http.Request) {
	mpID := r.FormValue("TransitGatewayMeteringPolicyId")
	ruleNum := r.FormValue("PolicyRuleNumber")
	key := meteringEntryKey(mpID, ruleNum)
	e, ok := ec2TGWMeteringPolicyEntries.Get(key)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMeteringPolicyEntry.NotFound",
			"metering policy entry not found: "+mpID+" rule "+ruleNum, http.StatusBadRequest)
		return
	}
	e.State = "deleted"
	ec2TGWMeteringPolicyEntries.Delete(key)
	tgwResponse(w, "DeleteTransitGatewayMeteringPolicyEntry",
		"<transitGatewayMeteringPolicyEntry>"+tgwMeteringPolicyEntryBodyXML(e)+"</transitGatewayMeteringPolicyEntry>")
}

func handleGetTransitGatewayMeteringPolicyEntries(w http.ResponseWriter, r *http.Request) {
	mpID := r.FormValue("TransitGatewayMeteringPolicyId")
	if _, ok := ec2TGWMeteringPolicies.Get(mpID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayMeteringPolicyId.NotFound", "metering policy not found: "+mpID, http.StatusBadRequest)
		return
	}
	var b strings.Builder
	b.WriteString("<transitGatewayMeteringPolicyEntries>")
	for _, e := range ec2TGWMeteringPolicyEntries.List() {
		if e.TransitGatewayMeteringPolicyId != mpID {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwMeteringPolicyEntryBodyXML(e))
	}
	b.WriteString("</transitGatewayMeteringPolicyEntries>")
	tgwResponse(w, "GetTransitGatewayMeteringPolicyEntries", b.String())
}

func meteringEntryKey(mpID, ruleNum string) string { return mpID + "/" + ruleNum }

// parseTagsTGW reads TagSpecifications from the request. Most EC2 ops flatten
// the tag-specification list under the wire key "TagSpecification" (singular),
// which the shared parseTags handles; the newer transit gateway metering-policy
// and policy-table create ops flatten it under "TagSpecifications" (plural), so
// fall back to that when the singular form yields nothing.
func parseTagsTGW(r *http.Request) []EC2Tag {
	if tags := parseTags(r); len(tags) > 0 {
		return tags
	}
	var tags []EC2Tag
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("TagSpecifications.1.Tag.%d.Key", i))
		if key == "" {
			break
		}
		value := r.FormValue(fmt.Sprintf("TagSpecifications.1.Tag.%d.Value", i))
		tags = append(tags, EC2Tag{Key: key, Value: value})
	}
	return tags
}

// Route table announcements

func handleCreateTransitGatewayRouteTableAnnouncement(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("TransitGatewayRouteTableId")
	rt, ok := ec2TransitGatewayRouteTables.Get(rtID)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayRouteTableId.NotFound", "transit gateway route table not found: "+rtID, http.StatusBadRequest)
		return
	}
	peeringID := r.FormValue("PeeringAttachmentId")
	if _, ok := ec2TGWPeeringAttachments.Get(peeringID); !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", "peering attachment not found: "+peeringID, http.StatusBadRequest)
		return
	}
	id := ec2ID("tgw-rta")
	ann := EC2TGWRouteTableAnnouncement{
		TransitGatewayRouteTableAnnouncementId: id,
		TransitGatewayId:                       rt.TransitGatewayId,
		TransitGatewayRouteTableId:             rtID,
		PeeringAttachmentId:                    peeringID,
		AnnouncementDirection:                  "outgoing",
		State:                                  "available",
		CreationTime:                           ec2NowRFC3339Milli(),
		Tags:                                   parseTagsTGW(r),
	}
	ec2TGWRouteTableAnnouncement.Put(id, ann)
	tgwResponse(w, "CreateTransitGatewayRouteTableAnnouncement",
		"<transitGatewayRouteTableAnnouncement>"+tgwRouteTableAnnouncementBodyXML(ann)+"</transitGatewayRouteTableAnnouncement>")
}

func handleDescribeTransitGatewayRouteTableAnnouncements(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayRouteTableAnnouncementIds")
	var b strings.Builder
	b.WriteString("<transitGatewayRouteTableAnnouncements>")
	for _, ann := range ec2TGWRouteTableAnnouncement.List() {
		if len(ids) > 0 && !ec2StrInValues(ann.TransitGatewayRouteTableAnnouncementId, ids) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwRouteTableAnnouncementBodyXML(ann))
	}
	b.WriteString("</transitGatewayRouteTableAnnouncements>")
	tgwResponse(w, "DescribeTransitGatewayRouteTableAnnouncements", b.String())
}

func handleDeleteTransitGatewayRouteTableAnnouncement(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayRouteTableAnnouncementId")
	ann, ok := ec2TGWRouteTableAnnouncement.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayRouteTableAnnouncementId.NotFound", "route table announcement not found: "+id, http.StatusBadRequest)
		return
	}
	ann.State = "deleted"
	ec2TGWRouteTableAnnouncement.Delete(id)
	tgwResponse(w, "DeleteTransitGatewayRouteTableAnnouncement",
		"<transitGatewayRouteTableAnnouncement>"+tgwRouteTableAnnouncementBodyXML(ann)+"</transitGatewayRouteTableAnnouncement>")
}

// XML rendering

// tgwMulticastAssociationsBodyXML renders TransitGatewayMulticastDomainAssociations
// (the Associate/Disassociate/Accept/Reject response shape).
func tgwMulticastAssociationsBodyXML(a EC2TGWMulticastAssociation) string {
	var sub strings.Builder
	sub.WriteString("<subnets>")
	for _, sn := range a.SubnetIds {
		fmt.Fprintf(&sub, "<item><subnetId>%s</subnetId><state>associated</state></item>", sn)
	}
	sub.WriteString("</subnets>")
	return fmt.Sprintf(
		"<transitGatewayMulticastDomainId>%s</transitGatewayMulticastDomainId>"+
			"<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>"+
			"<resourceId>%s</resourceId><resourceType>%s</resourceType>"+
			"<resourceOwnerId>%s</resourceOwnerId>%s",
		a.TransitGatewayMulticastDomainId, a.TransitGatewayAttachmentId,
		a.ResourceId, a.ResourceType, a.ResourceOwnerId, sub.String())
}

// tgwMulticastDomainAssociationItemXML renders one TransitGatewayMulticastDomainAssociation
// (the flattened per-subnet item returned by GetTransitGatewayMulticastDomainAssociations).
func tgwMulticastDomainAssociationItemXML(a EC2TGWMulticastAssociation, subnetID string) string {
	subnet := ""
	if subnetID != "" {
		subnet = fmt.Sprintf("<subnet><subnetId>%s</subnetId><state>associated</state></subnet>", subnetID)
	}
	return fmt.Sprintf(
		"<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>"+
			"<resourceId>%s</resourceId><resourceType>%s</resourceType>"+
			"<resourceOwnerId>%s</resourceOwnerId>%s",
		a.TransitGatewayAttachmentId, a.ResourceId, a.ResourceType, a.ResourceOwnerId, subnet)
}

// tgwRegisteredGroupBodyXML renders the registered/deregistered group members or
// sources shape (shared between Register/Deregister Members/Sources).
func tgwRegisteredGroupBodyXML(domainID, groupIP string, enis []string, idsTag string) string {
	var ids strings.Builder
	fmt.Fprintf(&ids, "<%s>", idsTag)
	for _, e := range enis {
		fmt.Fprintf(&ids, "<item>%s</item>", e)
	}
	fmt.Fprintf(&ids, "</%s>", idsTag)
	return fmt.Sprintf(
		"<transitGatewayMulticastDomainId>%s</transitGatewayMulticastDomainId>"+
			"%s<groupIpAddress>%s</groupIpAddress>",
		domainID, ids.String(), groupIP)
}

// tgwMulticastGroupItemXML renders a TransitGatewayMulticastGroup item.
func tgwMulticastGroupItemXML(g EC2TGWMulticastGroup) string {
	return fmt.Sprintf(
		"<groupIpAddress>%s</groupIpAddress>"+
			"<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>"+
			"<subnetId>%s</subnetId><resourceId>%s</resourceId>"+
			"<resourceType>%s</resourceType><resourceOwnerId>%s</resourceOwnerId>"+
			"<networkInterfaceId>%s</networkInterfaceId>"+
			"<groupMember>%t</groupMember><groupSource>%t</groupSource>"+
			"<memberType>%s</memberType><sourceType>%s</sourceType>",
		g.GroupIpAddress, g.TransitGatewayAttachmentId, g.SubnetId, g.ResourceId,
		g.ResourceType, g.ResourceOwnerId, g.NetworkInterfaceId,
		g.GroupMember, g.GroupSource, g.MemberType, g.SourceType)
}

func tgwPolicyTableBodyXML(pt EC2TGWPolicyTable) string {
	return fmt.Sprintf(
		"<transitGatewayPolicyTableId>%s</transitGatewayPolicyTableId>"+
			"<transitGatewayId>%s</transitGatewayId><state>%s</state>"+
			"<creationTime>%s</creationTime>%s",
		pt.TransitGatewayPolicyTableId, pt.TransitGatewayId, pt.State,
		pt.CreationTime, writeTagSetXML(pt.Tags))
}

func tgwPolicyTableEntryBodyXML(e EC2TGWPolicyTableEntry) string {
	var rule strings.Builder
	rule.WriteString("<policyRule>")
	if e.SourceCidrBlock != "" {
		fmt.Fprintf(&rule, "<sourceCidrBlock>%s</sourceCidrBlock>", e.SourceCidrBlock)
	}
	if e.SourcePortRange != "" {
		fmt.Fprintf(&rule, "<sourcePortRange>%s</sourcePortRange>", e.SourcePortRange)
	}
	if e.DestinationCidrBlock != "" {
		fmt.Fprintf(&rule, "<destinationCidrBlock>%s</destinationCidrBlock>", e.DestinationCidrBlock)
	}
	if e.DestinationPortRange != "" {
		fmt.Fprintf(&rule, "<destinationPortRange>%s</destinationPortRange>", e.DestinationPortRange)
	}
	if e.Protocol != "" {
		fmt.Fprintf(&rule, "<protocol>%s</protocol>", e.Protocol)
	}
	if e.MetaDataKey != "" || e.MetaDataValue != "" {
		fmt.Fprintf(&rule, "<metaData><metaDataKey>%s</metaDataKey><metaDataValue>%s</metaDataValue></metaData>",
			e.MetaDataKey, e.MetaDataValue)
	}
	rule.WriteString("</policyRule>")
	return fmt.Sprintf(
		"<policyRuleNumber>%s</policyRuleNumber>%s"+
			"<targetRouteTableId>%s</targetRouteTableId><state>%s</state>",
		e.PolicyRuleNumber, rule.String(), e.TargetRouteTableId, e.State)
}

func tgwPolicyTableAssociationBodyXML(a EC2TGWPolicyTableAssociation) string {
	return fmt.Sprintf(
		"<transitGatewayPolicyTableId>%s</transitGatewayPolicyTableId>"+
			"<transitGatewayAttachmentId>%s</transitGatewayAttachmentId>"+
			"<resourceId>%s</resourceId><resourceType>%s</resourceType>"+
			"<state>%s</state>",
		a.TransitGatewayPolicyTableId, a.TransitGatewayAttachmentId,
		a.ResourceId, a.ResourceType, a.State)
}

func tgwMeteringPolicyBodyXML(mp EC2TGWMeteringPolicy) string {
	var mids strings.Builder
	mids.WriteString("<middleboxAttachmentIdSet>")
	for _, m := range mp.MiddleboxAttachmentIds {
		fmt.Fprintf(&mids, "<item>%s</item>", m)
	}
	mids.WriteString("</middleboxAttachmentIdSet>")
	return fmt.Sprintf(
		"<transitGatewayMeteringPolicyId>%s</transitGatewayMeteringPolicyId>"+
			"<transitGatewayId>%s</transitGatewayId>%s<state>%s</state>"+
			"<updateEffectiveAt>%s</updateEffectiveAt>%s",
		mp.TransitGatewayMeteringPolicyId, mp.TransitGatewayId, mids.String(),
		mp.State, mp.UpdateEffectiveAt, writeTagSetXML(mp.Tags))
}

func tgwMeteringPolicyEntryBodyXML(e EC2TGWMeteringPolicyEntry) string {
	var rule strings.Builder
	rule.WriteString("<meteringPolicyRule>")
	if e.SourceCidrBlock != "" {
		fmt.Fprintf(&rule, "<sourceCidrBlock>%s</sourceCidrBlock>", e.SourceCidrBlock)
	}
	if e.DestinationCidrBlock != "" {
		fmt.Fprintf(&rule, "<destinationCidrBlock>%s</destinationCidrBlock>", e.DestinationCidrBlock)
	}
	if e.SourceTransitGatewayAttachmentId != "" {
		fmt.Fprintf(&rule, "<sourceTransitGatewayAttachmentId>%s</sourceTransitGatewayAttachmentId>", e.SourceTransitGatewayAttachmentId)
	}
	if e.DestinationTransitGatewayAttachmentId != "" {
		fmt.Fprintf(&rule, "<destinationTransitGatewayAttachmentId>%s</destinationTransitGatewayAttachmentId>", e.DestinationTransitGatewayAttachmentId)
	}
	if e.Protocol != "" {
		fmt.Fprintf(&rule, "<protocol>%s</protocol>", e.Protocol)
	}
	if e.SourcePortRange != "" {
		fmt.Fprintf(&rule, "<sourcePortRange>%s</sourcePortRange>", e.SourcePortRange)
	}
	if e.DestinationPortRange != "" {
		fmt.Fprintf(&rule, "<destinationPortRange>%s</destinationPortRange>", e.DestinationPortRange)
	}
	rule.WriteString("</meteringPolicyRule>")
	return fmt.Sprintf(
		"<policyRuleNumber>%s</policyRuleNumber><meteredAccount>%s</meteredAccount>"+
			"<state>%s</state><updatedAt>%s</updatedAt>"+
			"<updateEffectiveAt>%s</updateEffectiveAt>%s",
		e.PolicyRuleNumber, e.MeteredAccount, e.State, e.UpdatedAt,
		e.UpdateEffectiveAt, rule.String())
}

func tgwRouteTableAnnouncementBodyXML(ann EC2TGWRouteTableAnnouncement) string {
	return fmt.Sprintf(
		"<transitGatewayRouteTableAnnouncementId>%s</transitGatewayRouteTableAnnouncementId>"+
			"<transitGatewayId>%s</transitGatewayId>"+
			"<peeringAttachmentId>%s</peeringAttachmentId>"+
			"<announcementDirection>%s</announcementDirection>"+
			"<transitGatewayRouteTableId>%s</transitGatewayRouteTableId>"+
			"<state>%s</state><creationTime>%s</creationTime>%s",
		ann.TransitGatewayRouteTableAnnouncementId, ann.TransitGatewayId,
		ann.PeeringAttachmentId, ann.AnnouncementDirection,
		ann.TransitGatewayRouteTableId, ann.State, ann.CreationTime,
		writeTagSetXML(ann.Tags))
}
