package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// This file implements faithful control-plane CRUD for the EC2 resources that
// round out the VPC networking surface: network ACLs, VPC peering connections,
// managed prefix lists, VPC Flow Logs, and egress-only internet gateways. Each
// is backed by a real SQLite-persisted store and rendered as the exact ec2Query
// XML the AWS SDK for Go v2 and the aws CLI deserialize.

type EC2NetworkAcl struct {
	NetworkAclId string
	VpcId        string
	IsDefault    bool
	OwnerId      string
	Entries      []EC2NetworkAclEntry
	Associations []EC2NetworkAclAssociation
	Tags         []EC2Tag
}

type EC2NetworkAclEntry struct {
	RuleNumber    int
	Protocol      string
	RuleAction    string
	Egress        bool
	CidrBlock     string
	Ipv6CidrBlock string
	PortRangeFrom int
	PortRangeTo   int
	HasPortRange  bool
	IcmpType      int
	IcmpCode      int
	HasIcmp       bool
}

type EC2NetworkAclAssociation struct {
	NetworkAclAssociationId string
	NetworkAclId            string
	SubnetId                string
}

type EC2VpcPeeringConnection struct {
	VpcPeeringConnectionId string
	RequesterVpcId         string
	RequesterCidrBlock     string
	RequesterOwnerId       string
	RequesterRegion        string
	AccepterVpcId          string
	AccepterCidrBlock      string
	AccepterOwnerId        string
	AccepterRegion         string
	StatusCode             string
	StatusMessage          string
	Tags                   []EC2Tag
}

type EC2ManagedPrefixList struct {
	PrefixListId   string
	PrefixListName string
	PrefixListArn  string
	AddressFamily  string
	State          string
	MaxEntries     int
	Version        int
	OwnerId        string
	Entries        []EC2PrefixListEntry
	Tags           []EC2Tag
}

type EC2PrefixListEntry struct {
	Cidr        string
	Description string
}

type EC2FlowLog struct {
	FlowLogId                string
	ResourceId               string
	TrafficType              string
	LogDestinationType       string
	LogDestination           string
	LogGroupName             string
	DeliverLogsPermissionArn string
	DeliverLogsStatus        string
	FlowLogStatus            string
	LogFormat                string
	MaxAggregationInterval   int
	CreationTime             string
	Tags                     []EC2Tag
}

type EC2EgressOnlyInternetGateway struct {
	EgressOnlyInternetGatewayId string
	Attachments                 []EC2IGWAttachment
	Tags                        []EC2Tag
}

var (
	ec2NetworkAcls        sim.Store[EC2NetworkAcl]
	ec2VpcPeerings        sim.Store[EC2VpcPeeringConnection]
	ec2ManagedPrefixLists sim.Store[EC2ManagedPrefixList]
	ec2FlowLogs           sim.Store[EC2FlowLog]
	ec2EgressOnlyGateways sim.Store[EC2EgressOnlyInternetGateway]
)

func registerEC2AclPeeringPrefix(r *AWSQueryRouter, srv *sim.Server) {
	ec2NetworkAcls = sim.MakeStore[EC2NetworkAcl](srv.DB(), "ec2_network_acls")
	ec2VpcPeerings = sim.MakeStore[EC2VpcPeeringConnection](srv.DB(), "ec2_vpc_peerings")
	ec2ManagedPrefixLists = sim.MakeStore[EC2ManagedPrefixList](srv.DB(), "ec2_managed_prefix_lists")
	ec2FlowLogs = sim.MakeStore[EC2FlowLog](srv.DB(), "ec2_flow_logs")
	ec2EgressOnlyGateways = sim.MakeStore[EC2EgressOnlyInternetGateway](srv.DB(), "ec2_egress_only_gateways")

	// Network ACLs
	r.Register("CreateNetworkAcl", handleCreateNetworkAcl)
	r.Register("DescribeNetworkAcls", handleDescribeNetworkAcls)
	r.Register("DeleteNetworkAcl", handleDeleteNetworkAcl)
	r.Register("CreateNetworkAclEntry", handleCreateNetworkAclEntry)
	r.Register("DeleteNetworkAclEntry", handleDeleteNetworkAclEntry)
	r.Register("ReplaceNetworkAclEntry", handleReplaceNetworkAclEntry)

	// VPC peering
	r.Register("CreateVpcPeeringConnection", handleCreateVpcPeeringConnection)
	r.Register("DescribeVpcPeeringConnections", handleDescribeVpcPeeringConnections)
	r.Register("AcceptVpcPeeringConnection", handleAcceptVpcPeeringConnection)
	r.Register("DeleteVpcPeeringConnection", handleDeleteVpcPeeringConnection)

	// Managed prefix lists
	r.Register("CreateManagedPrefixList", handleCreateManagedPrefixList)
	r.Register("DescribeManagedPrefixLists", handleDescribeManagedPrefixLists)
	r.Register("DeleteManagedPrefixList", handleDeleteManagedPrefixList)
	r.Register("GetManagedPrefixListEntries", handleGetManagedPrefixListEntries)

	// Flow logs
	r.Register("CreateFlowLogs", handleCreateFlowLogs)
	r.Register("DescribeFlowLogs", handleDescribeFlowLogs)
	r.Register("DeleteFlowLogs", handleDeleteFlowLogs)

	// Egress-only internet gateways
	r.Register("CreateEgressOnlyInternetGateway", handleCreateEgressOnlyInternetGateway)
	r.Register("DescribeEgressOnlyInternetGateways", handleDescribeEgressOnlyInternetGateways)
	r.Register("DeleteEgressOnlyInternetGateway", handleDeleteEgressOnlyInternetGateway)
}

func handleCreateNetworkAcl(w http.ResponseWriter, r *http.Request) {
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter vpcId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcID); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcID), http.StatusBadRequest)
		return
	}
	id := ec2ID("acl")
	acl := EC2NetworkAcl{
		NetworkAclId: id,
		VpcId:        vpcID,
		IsDefault:    false,
		OwnerId:      ec2Owner(),
		// Real AWS seeds every new network ACL with the two default deny-all
		// rules (rule 32767 / IPv4 0.0.0.0/0 and the implicit egress one).
		Entries: []EC2NetworkAclEntry{
			{RuleNumber: 32767, Protocol: "-1", RuleAction: "deny", Egress: false, CidrBlock: "0.0.0.0/0"},
			{RuleNumber: 32767, Protocol: "-1", RuleAction: "deny", Egress: true, CidrBlock: "0.0.0.0/0"},
		},
		Tags: parseTags(r),
	}
	ec2NetworkAcls.Put(id, acl)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateNetworkAclResponse %s>
  <requestId>%s</requestId>
  <networkAcl>%s</networkAcl>
</CreateNetworkAclResponse>`, ec2Xmlns(), generateUUID(), networkAclBodyXML(acl))
}

func networkAclBodyXML(acl EC2NetworkAcl) string {
	var entries strings.Builder
	entries.WriteString("<entrySet>")
	for _, e := range acl.Entries {
		entries.WriteString(networkAclEntryXML(e))
	}
	entries.WriteString("</entrySet>")

	var assocs strings.Builder
	assocs.WriteString("<associationSet>")
	for _, a := range acl.Associations {
		fmt.Fprintf(&assocs, "<item><networkAclAssociationId>%s</networkAclAssociationId><networkAclId>%s</networkAclId><subnetId>%s</subnetId></item>",
			a.NetworkAclAssociationId, a.NetworkAclId, a.SubnetId)
	}
	assocs.WriteString("</associationSet>")

	return fmt.Sprintf(`<networkAclId>%s</networkAclId><vpcId>%s</vpcId><default>%t</default><ownerId>%s</ownerId>%s%s%s`,
		acl.NetworkAclId, acl.VpcId, acl.IsDefault, acl.OwnerId, entries.String(), assocs.String(), writeTagSetXML(acl.Tags))
}

func networkAclEntryXML(e EC2NetworkAclEntry) string {
	var b strings.Builder
	b.WriteString("<item>")
	fmt.Fprintf(&b, "<ruleNumber>%d</ruleNumber><protocol>%s</protocol><ruleAction>%s</ruleAction><egress>%t</egress>",
		e.RuleNumber, e.Protocol, e.RuleAction, e.Egress)
	if e.CidrBlock != "" {
		fmt.Fprintf(&b, "<cidrBlock>%s</cidrBlock>", e.CidrBlock)
	}
	if e.Ipv6CidrBlock != "" {
		fmt.Fprintf(&b, "<ipv6CidrBlock>%s</ipv6CidrBlock>", e.Ipv6CidrBlock)
	}
	if e.HasPortRange {
		fmt.Fprintf(&b, "<portRange><from>%d</from><to>%d</to></portRange>", e.PortRangeFrom, e.PortRangeTo)
	}
	if e.HasIcmp {
		fmt.Fprintf(&b, "<icmpTypeCode><code>%d</code><type>%d</type></icmpTypeCode>", e.IcmpCode, e.IcmpType)
	}
	b.WriteString("</item>")
	return b.String()
}

func handleDescribeNetworkAcls(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "NetworkAclId")
	for _, id := range ids {
		if _, ok := ec2NetworkAcls.Get(id); !ok {
			ec2ErrorXML(w, "InvalidNetworkAclID.NotFound", fmt.Sprintf("The networkAcl ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	var items strings.Builder
	for _, acl := range ec2NetworkAcls.List() {
		if len(ids) > 0 && !ec2StrInValues(acl.NetworkAclId, ids) {
			continue
		}
		if !networkAclMatchesFilters(acl, filters) {
			continue
		}
		items.WriteString("<item>" + networkAclBodyXML(acl) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeNetworkAclsResponse %s>
  <requestId>%s</requestId>
  <networkAclSet>%s</networkAclSet>
</DescribeNetworkAclsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func networkAclMatchesFilters(acl EC2NetworkAcl, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "network-acl-id":
			if !ec2StrInValues(acl.NetworkAclId, vals) {
				return false
			}
		case "vpc-id":
			if !ec2StrInValues(acl.VpcId, vals) {
				return false
			}
		case "default":
			if acl.IsDefault != ec2StrInValues("true", vals) {
				return false
			}
		case "association.subnet-id":
			ok := false
			for _, a := range acl.Associations {
				if ec2StrInValues(a.SubnetId, vals) {
					ok = true
				}
			}
			if !ok {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, acl.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteNetworkAcl(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkAclId")
	if _, ok := ec2NetworkAcls.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkAclID.NotFound", fmt.Sprintf("The networkAcl ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2NetworkAcls.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteNetworkAclResponse %s><requestId>%s</requestId><return>true</return></DeleteNetworkAclResponse>`, ec2Xmlns(), generateUUID())
}

func parseNetworkAclEntry(r *http.Request) EC2NetworkAclEntry {
	ruleNumber, _ := strconv.Atoi(r.FormValue("RuleNumber"))
	entry := EC2NetworkAclEntry{
		RuleNumber:    ruleNumber,
		Protocol:      r.FormValue("Protocol"),
		RuleAction:    r.FormValue("RuleAction"),
		Egress:        r.FormValue("Egress") == "true",
		CidrBlock:     r.FormValue("CidrBlock"),
		Ipv6CidrBlock: r.FormValue("Ipv6CidrBlock"),
	}
	if from := r.FormValue("PortRange.From"); from != "" {
		entry.PortRangeFrom, _ = strconv.Atoi(from)
		entry.PortRangeTo, _ = strconv.Atoi(r.FormValue("PortRange.To"))
		entry.HasPortRange = true
	}
	if t := r.FormValue("Icmp.Type"); t != "" {
		entry.IcmpType, _ = strconv.Atoi(t)
		entry.IcmpCode, _ = strconv.Atoi(r.FormValue("Icmp.Code"))
		entry.HasIcmp = true
	}
	return entry
}

func handleCreateNetworkAclEntry(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkAclId")
	if _, ok := ec2NetworkAcls.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkAclID.NotFound", fmt.Sprintf("The networkAcl ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	entry := parseNetworkAclEntry(r)
	ec2NetworkAcls.Update(id, func(acl *EC2NetworkAcl) {
		// Copy then append so we don't mutate the shared backing slice in place.
		next := make([]EC2NetworkAclEntry, 0, len(acl.Entries)+1)
		next = append(next, acl.Entries...)
		next = append(next, entry)
		acl.Entries = next
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateNetworkAclEntryResponse %s><requestId>%s</requestId><return>true</return></CreateNetworkAclEntryResponse>`, ec2Xmlns(), generateUUID())
}

func handleReplaceNetworkAclEntry(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkAclId")
	if _, ok := ec2NetworkAcls.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkAclID.NotFound", fmt.Sprintf("The networkAcl ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	entry := parseNetworkAclEntry(r)
	ec2NetworkAcls.Update(id, func(acl *EC2NetworkAcl) {
		next := make([]EC2NetworkAclEntry, len(acl.Entries))
		copy(next, acl.Entries)
		for i := range next {
			if next[i].RuleNumber == entry.RuleNumber && next[i].Egress == entry.Egress {
				next[i] = entry
			}
		}
		acl.Entries = next
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceNetworkAclEntryResponse %s><requestId>%s</requestId><return>true</return></ReplaceNetworkAclEntryResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteNetworkAclEntry(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NetworkAclId")
	if _, ok := ec2NetworkAcls.Get(id); !ok {
		ec2ErrorXML(w, "InvalidNetworkAclID.NotFound", fmt.Sprintf("The networkAcl ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ruleNumber, _ := strconv.Atoi(r.FormValue("RuleNumber"))
	egress := r.FormValue("Egress") == "true"
	ec2NetworkAcls.Update(id, func(acl *EC2NetworkAcl) {
		next := make([]EC2NetworkAclEntry, 0, len(acl.Entries))
		for _, e := range acl.Entries {
			if e.RuleNumber == ruleNumber && e.Egress == egress {
				continue
			}
			next = append(next, e)
		}
		acl.Entries = next
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteNetworkAclEntryResponse %s><requestId>%s</requestId><return>true</return></DeleteNetworkAclEntryResponse>`, ec2Xmlns(), generateUUID())
}

func handleCreateVpcPeeringConnection(w http.ResponseWriter, r *http.Request) {
	requesterVpc := r.FormValue("VpcId")
	peerVpc := r.FormValue("PeerVpcId")
	if requesterVpc == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter vpcId", http.StatusBadRequest)
		return
	}
	reqVpc, ok := ec2Vpcs.Get(requesterVpc)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", requesterVpc), http.StatusBadRequest)
		return
	}
	reqCidr := reqVpc.CidrBlock
	accCidr := ""
	if v, ok := ec2Vpcs.Get(peerVpc); ok {
		accCidr = v.CidrBlock
	}
	peerRegion := r.FormValue("PeerRegion")
	if peerRegion == "" {
		peerRegion = awsRegion()
	}
	peerOwner := r.FormValue("PeerOwnerId")
	if peerOwner == "" {
		peerOwner = ec2Owner()
	}
	id := ec2ID("pcx")
	pcx := EC2VpcPeeringConnection{
		VpcPeeringConnectionId: id,
		RequesterVpcId:         requesterVpc,
		RequesterCidrBlock:     reqCidr,
		RequesterOwnerId:       ec2Owner(),
		RequesterRegion:        awsRegion(),
		AccepterVpcId:          peerVpc,
		AccepterCidrBlock:      accCidr,
		AccepterOwnerId:        peerOwner,
		AccepterRegion:         peerRegion,
		// Same-account peerings to a VPC that exists locally are
		// pending-acceptance until AcceptVpcPeeringConnection.
		StatusCode:    "pending-acceptance",
		StatusMessage: "Pending Acceptance by " + peerOwner,
		Tags:          parseTags(r),
	}
	ec2VpcPeerings.Put(id, pcx)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpcPeeringConnectionResponse %s>
  <requestId>%s</requestId>
  <vpcPeeringConnection>%s</vpcPeeringConnection>
</CreateVpcPeeringConnectionResponse>`, ec2Xmlns(), generateUUID(), vpcPeeringBodyXML(pcx))
}

func vpcPeeringBodyXML(pcx EC2VpcPeeringConnection) string {
	requester := fmt.Sprintf(`<requesterVpcInfo><vpcId>%s</vpcId><ownerId>%s</ownerId><cidrBlock>%s</cidrBlock><region>%s</region></requesterVpcInfo>`,
		pcx.RequesterVpcId, pcx.RequesterOwnerId, pcx.RequesterCidrBlock, pcx.RequesterRegion)
	accepter := fmt.Sprintf(`<accepterVpcInfo><vpcId>%s</vpcId><ownerId>%s</ownerId><cidrBlock>%s</cidrBlock><region>%s</region></accepterVpcInfo>`,
		pcx.AccepterVpcId, pcx.AccepterOwnerId, pcx.AccepterCidrBlock, pcx.AccepterRegion)
	status := fmt.Sprintf(`<status><code>%s</code><message>%s</message></status>`, pcx.StatusCode, pcx.StatusMessage)
	return fmt.Sprintf(`<vpcPeeringConnectionId>%s</vpcPeeringConnectionId>%s%s%s%s`,
		pcx.VpcPeeringConnectionId, requester, accepter, status, writeTagSetXML(pcx.Tags))
}

func handleDescribeVpcPeeringConnections(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpcPeeringConnectionId")
	for _, id := range ids {
		if _, ok := ec2VpcPeerings.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVpcPeeringConnectionID.NotFound", fmt.Sprintf("The vpcPeeringConnection ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	var items strings.Builder
	for _, pcx := range ec2VpcPeerings.List() {
		if len(ids) > 0 && !ec2StrInValues(pcx.VpcPeeringConnectionId, ids) {
			continue
		}
		if !vpcPeeringMatchesFilters(pcx, filters) {
			continue
		}
		items.WriteString("<item>" + vpcPeeringBodyXML(pcx) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcPeeringConnectionsResponse %s>
  <requestId>%s</requestId>
  <vpcPeeringConnectionSet>%s</vpcPeeringConnectionSet>
</DescribeVpcPeeringConnectionsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func vpcPeeringMatchesFilters(pcx EC2VpcPeeringConnection, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-peering-connection-id":
			if !ec2StrInValues(pcx.VpcPeeringConnectionId, vals) {
				return false
			}
		case "requester-vpc-info.vpc-id":
			if !ec2StrInValues(pcx.RequesterVpcId, vals) {
				return false
			}
		case "accepter-vpc-info.vpc-id":
			if !ec2StrInValues(pcx.AccepterVpcId, vals) {
				return false
			}
		case "status-code":
			if !ec2StrInValues(pcx.StatusCode, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, pcx.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleAcceptVpcPeeringConnection(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpcPeeringConnectionId")
	pcx, ok := ec2VpcPeerings.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcPeeringConnectionID.NotFound", fmt.Sprintf("The vpcPeeringConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VpcPeerings.Update(id, func(p *EC2VpcPeeringConnection) {
		p.StatusCode = "active"
		p.StatusMessage = "Active"
	})
	pcx.StatusCode = "active"
	pcx.StatusMessage = "Active"
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AcceptVpcPeeringConnectionResponse %s>
  <requestId>%s</requestId>
  <vpcPeeringConnection>%s</vpcPeeringConnection>
</AcceptVpcPeeringConnectionResponse>`, ec2Xmlns(), generateUUID(), vpcPeeringBodyXML(pcx))
}

func handleDeleteVpcPeeringConnection(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpcPeeringConnectionId")
	if _, ok := ec2VpcPeerings.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpcPeeringConnectionID.NotFound", fmt.Sprintf("The vpcPeeringConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VpcPeerings.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpcPeeringConnectionResponse %s><requestId>%s</requestId><return>true</return></DeleteVpcPeeringConnectionResponse>`, ec2Xmlns(), generateUUID())
}

func handleCreateManagedPrefixList(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("PrefixListName")
	af := r.FormValue("AddressFamily")
	maxEntries, _ := strconv.Atoi(r.FormValue("MaxEntries"))
	var entries []EC2PrefixListEntry
	for i := 1; ; i++ {
		cidr := r.FormValue(fmt.Sprintf("Entry.%d.Cidr", i))
		if cidr == "" {
			break
		}
		entries = append(entries, EC2PrefixListEntry{Cidr: cidr, Description: r.FormValue(fmt.Sprintf("Entry.%d.Description", i))})
	}
	id := ec2ID("pl")
	pl := EC2ManagedPrefixList{
		PrefixListId:   id,
		PrefixListName: name,
		PrefixListArn:  fmt.Sprintf("arn:aws:ec2:%s:%s:prefix-list/%s", awsRegion(), ec2Owner(), id),
		AddressFamily:  af,
		State:          "create-complete",
		MaxEntries:     maxEntries,
		Version:        1,
		OwnerId:        ec2Owner(),
		Entries:        entries,
		Tags:           parseTags(r),
	}
	ec2ManagedPrefixLists.Put(id, pl)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateManagedPrefixListResponse %s>
  <requestId>%s</requestId>
  <prefixList>%s</prefixList>
</CreateManagedPrefixListResponse>`, ec2Xmlns(), generateUUID(), managedPrefixListBodyXML(pl))
}

func managedPrefixListBodyXML(pl EC2ManagedPrefixList) string {
	return fmt.Sprintf(`<prefixListId>%s</prefixListId><addressFamily>%s</addressFamily><state>%s</state><prefixListArn>%s</prefixListArn><prefixListName>%s</prefixListName><maxEntries>%d</maxEntries><version>%d</version><ownerId>%s</ownerId>%s`,
		pl.PrefixListId, pl.AddressFamily, pl.State, pl.PrefixListArn, pl.PrefixListName, pl.MaxEntries, pl.Version, pl.OwnerId, writeTagSetXML(pl.Tags))
}

func handleDescribeManagedPrefixLists(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "PrefixListId")
	for _, id := range ids {
		if _, ok := ec2ManagedPrefixLists.Get(id); !ok {
			ec2ErrorXML(w, "InvalidPrefixListID.NotFound", fmt.Sprintf("The prefix list ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	var items strings.Builder
	for _, pl := range ec2ManagedPrefixLists.List() {
		if len(ids) > 0 && !ec2StrInValues(pl.PrefixListId, ids) {
			continue
		}
		if !managedPrefixListMatchesFilters(pl, filters) {
			continue
		}
		items.WriteString("<item>" + managedPrefixListBodyXML(pl) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeManagedPrefixListsResponse %s>
  <requestId>%s</requestId>
  <prefixListSet>%s</prefixListSet>
</DescribeManagedPrefixListsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func managedPrefixListMatchesFilters(pl EC2ManagedPrefixList, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "prefix-list-id":
			if !ec2StrInValues(pl.PrefixListId, vals) {
				return false
			}
		case "prefix-list-name":
			if !ec2StrInValues(pl.PrefixListName, vals) {
				return false
			}
		case "owner-id":
			if !ec2StrInValues(pl.OwnerId, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, pl.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteManagedPrefixList(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PrefixListId")
	pl, ok := ec2ManagedPrefixLists.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidPrefixListID.NotFound", fmt.Sprintf("The prefix list ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// Real AWS transitions the list to delete-complete; the resource is then
	// gone from subsequent describes. We delete it from the store but echo the
	// delete-complete state in the response, as the API does.
	pl.State = "delete-complete"
	ec2ManagedPrefixLists.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteManagedPrefixListResponse %s>
  <requestId>%s</requestId>
  <prefixList>%s</prefixList>
</DeleteManagedPrefixListResponse>`, ec2Xmlns(), generateUUID(), managedPrefixListBodyXML(pl))
}

func handleGetManagedPrefixListEntries(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PrefixListId")
	pl, ok := ec2ManagedPrefixLists.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidPrefixListID.NotFound", fmt.Sprintf("The prefix list ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	var items strings.Builder
	for _, e := range pl.Entries {
		fmt.Fprintf(&items, "<item><cidr>%s</cidr>", e.Cidr)
		if e.Description != "" {
			fmt.Fprintf(&items, "<description>%s</description>", e.Description)
		}
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetManagedPrefixListEntriesResponse %s>
  <requestId>%s</requestId>
  <entrySet>%s</entrySet>
</GetManagedPrefixListEntriesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleCreateFlowLogs(w http.ResponseWriter, r *http.Request) {
	resourceIDs := ec2ParamList(r, "ResourceId")
	trafficType := r.FormValue("TrafficType")
	logDestType := r.FormValue("LogDestinationType")
	if logDestType == "" {
		logDestType = "cloud-watch-logs"
	}
	logDest := r.FormValue("LogDestination")
	logGroup := r.FormValue("LogGroupName")
	permArn := r.FormValue("DeliverLogsPermissionArn")
	logFormat := r.FormValue("LogFormat")
	maxAgg, _ := strconv.Atoi(r.FormValue("MaxAggregationInterval"))
	if maxAgg == 0 {
		maxAgg = 600
	}
	tags := parseTags(r)

	var created []string
	for _, rid := range resourceIDs {
		id := ec2ID("fl")
		fl := EC2FlowLog{
			FlowLogId:                id,
			ResourceId:               rid,
			TrafficType:              trafficType,
			LogDestinationType:       logDestType,
			LogDestination:           logDest,
			LogGroupName:             logGroup,
			DeliverLogsPermissionArn: permArn,
			DeliverLogsStatus:        "SUCCESS",
			FlowLogStatus:            "ACTIVE",
			LogFormat:                logFormat,
			MaxAggregationInterval:   maxAgg,
			CreationTime:             ec2NowRFC3339Milli(),
			Tags:                     tags,
		}
		ec2FlowLogs.Put(id, fl)
		created = append(created, id)
	}

	var idSet strings.Builder
	idSet.WriteString("<flowLogIdSet>")
	for _, id := range created {
		fmt.Fprintf(&idSet, "<item>%s</item>", id)
	}
	idSet.WriteString("</flowLogIdSet>")

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateFlowLogsResponse %s>
  <requestId>%s</requestId>
  %s
  <unsuccessful/>
</CreateFlowLogsResponse>`, ec2Xmlns(), generateUUID(), idSet.String())
}

func flowLogBodyXML(fl EC2FlowLog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<flowLogId>%s</flowLogId><creationTime>%s</creationTime><resourceId>%s</resourceId><trafficType>%s</trafficType><logDestinationType>%s</logDestinationType><flowLogStatus>%s</flowLogStatus><deliverLogsStatus>%s</deliverLogsStatus><maxAggregationInterval>%d</maxAggregationInterval>",
		fl.FlowLogId, fl.CreationTime, fl.ResourceId, fl.TrafficType, fl.LogDestinationType, fl.FlowLogStatus, fl.DeliverLogsStatus, fl.MaxAggregationInterval)
	if fl.LogGroupName != "" {
		fmt.Fprintf(&b, "<logGroupName>%s</logGroupName>", fl.LogGroupName)
	}
	if fl.LogDestination != "" {
		fmt.Fprintf(&b, "<logDestination>%s</logDestination>", fl.LogDestination)
	}
	if fl.DeliverLogsPermissionArn != "" {
		fmt.Fprintf(&b, "<deliverLogsPermissionArn>%s</deliverLogsPermissionArn>", fl.DeliverLogsPermissionArn)
	}
	if fl.LogFormat != "" {
		fmt.Fprintf(&b, "<logFormat>%s</logFormat>", fl.LogFormat)
	}
	b.WriteString(writeTagSetXML(fl.Tags))
	return b.String()
}

func handleDescribeFlowLogs(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "FlowLogId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, fl := range ec2FlowLogs.List() {
		if len(ids) > 0 && !ec2StrInValues(fl.FlowLogId, ids) {
			continue
		}
		if !flowLogMatchesFilters(fl, filters) {
			continue
		}
		items.WriteString("<item>" + flowLogBodyXML(fl) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeFlowLogsResponse %s>
  <requestId>%s</requestId>
  <flowLogSet>%s</flowLogSet>
</DescribeFlowLogsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func flowLogMatchesFilters(fl EC2FlowLog, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "flow-log-id":
			if !ec2StrInValues(fl.FlowLogId, vals) {
				return false
			}
		case "resource-id":
			if !ec2StrInValues(fl.ResourceId, vals) {
				return false
			}
		case "traffic-type":
			if !ec2StrInValues(fl.TrafficType, vals) {
				return false
			}
		case "log-destination-type":
			if !ec2StrInValues(fl.LogDestinationType, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, fl.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteFlowLogs(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "FlowLogId")
	var unsuccessful strings.Builder
	unsuccessful.WriteString("<unsuccessful>")
	for _, id := range ids {
		if !ec2FlowLogs.Delete(id) {
			fmt.Fprintf(&unsuccessful, `<item><resourceId>%s</resourceId><error><code>InvalidFlowLogId.NotFound</code><message>These flow log ids do not exist: [%s]</message></error></item>`, id, id)
		}
	}
	unsuccessful.WriteString("</unsuccessful>")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteFlowLogsResponse %s>
  <requestId>%s</requestId>
  %s
</DeleteFlowLogsResponse>`, ec2Xmlns(), generateUUID(), unsuccessful.String())
}

func handleCreateEgressOnlyInternetGateway(w http.ResponseWriter, r *http.Request) {
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter vpcId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcID); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcID), http.StatusBadRequest)
		return
	}
	id := ec2ID("eigw")
	eigw := EC2EgressOnlyInternetGateway{
		EgressOnlyInternetGatewayId: id,
		Attachments:                 []EC2IGWAttachment{{VpcId: vpcID, State: "attached"}},
		Tags:                        parseTags(r),
	}
	ec2EgressOnlyGateways.Put(id, eigw)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateEgressOnlyInternetGatewayResponse %s>
  <requestId>%s</requestId>
  <egressOnlyInternetGateway>%s</egressOnlyInternetGateway>
</CreateEgressOnlyInternetGatewayResponse>`, ec2Xmlns(), generateUUID(), egressOnlyGatewayBodyXML(eigw))
}

func egressOnlyGatewayBodyXML(eigw EC2EgressOnlyInternetGateway) string {
	var attachments strings.Builder
	attachments.WriteString("<attachmentSet>")
	for _, a := range eigw.Attachments {
		fmt.Fprintf(&attachments, "<item><vpcId>%s</vpcId><state>%s</state></item>", a.VpcId, a.State)
	}
	attachments.WriteString("</attachmentSet>")
	return fmt.Sprintf(`<egressOnlyInternetGatewayId>%s</egressOnlyInternetGatewayId>%s%s`,
		eigw.EgressOnlyInternetGatewayId, attachments.String(), writeTagSetXML(eigw.Tags))
}

func handleDescribeEgressOnlyInternetGateways(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "EgressOnlyInternetGatewayId")
	for _, id := range ids {
		if _, ok := ec2EgressOnlyGateways.Get(id); !ok {
			ec2ErrorXML(w, "InvalidEgressOnlyInternetGatewayId.NotFound", fmt.Sprintf("The egress-only internet gateway ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	var items strings.Builder
	for _, eigw := range ec2EgressOnlyGateways.List() {
		if len(ids) > 0 && !ec2StrInValues(eigw.EgressOnlyInternetGatewayId, ids) {
			continue
		}
		if !egressOnlyGatewayMatchesFilters(eigw, filters) {
			continue
		}
		items.WriteString("<item>" + egressOnlyGatewayBodyXML(eigw) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeEgressOnlyInternetGatewaysResponse %s>
  <requestId>%s</requestId>
  <egressOnlyInternetGatewaySet>%s</egressOnlyInternetGatewaySet>
</DescribeEgressOnlyInternetGatewaysResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func egressOnlyGatewayMatchesFilters(eigw EC2EgressOnlyInternetGateway, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "egress-only-internet-gateway-id":
			if !ec2StrInValues(eigw.EgressOnlyInternetGatewayId, vals) {
				return false
			}
		case "attachment.vpc-id":
			ok := false
			for _, a := range eigw.Attachments {
				if ec2StrInValues(a.VpcId, vals) {
					ok = true
				}
			}
			if !ok {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, eigw.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteEgressOnlyInternetGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("EgressOnlyInternetGatewayId")
	if !ec2EgressOnlyGateways.Delete(id) {
		ec2ErrorXML(w, "InvalidEgressOnlyInternetGatewayId.NotFound", fmt.Sprintf("The egress-only internet gateway ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteEgressOnlyInternetGatewayResponse %s><requestId>%s</requestId><returnCode>true</returnCode></DeleteEgressOnlyInternetGatewayResponse>`, ec2Xmlns(), generateUUID())
}
