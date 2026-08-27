package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file completes four Amazon EC2 control-plane families:
//
//   - Local Gateway (AWS Outposts): the local gateway (lgw-…) is a pre-seeded
//     Outpost resource; its route tables (lgw-rtb-…), virtual interfaces
//     (lgw-vif-…) and groups (lgw-vif-grp-…), and the VPC / virtual-interface-
//     group associations are real CRUD with state.
//   - Capacity Manager: an account-level enable/disable plus monitored-tag-keys,
//     data exports to S3, and metric reads computed from the existing
//     capacity-reservation store.
//   - Declarative Policies: org-account reports delivered to S3, transitioning
//     running → complete.
//   - AWS Network Performance: source/destination region metric subscriptions
//     plus GetAwsNetworkPerformanceData returning real-shaped metric points.

// Resource types

// EC2LocalGateway is an AWS Outposts local gateway (lgw-…). Real accounts get
// one per Outpost; the sim seeds a single deterministic gateway so the
// route-table / virtual-interface CRUD has a parent to hang off.
type EC2LocalGateway struct {
	LocalGatewayId string
	OutpostArn     string
	OwnerId        string
	State          string
	Tags           []EC2Tag
}

// EC2LocalGatewayRouteTable is a local gateway route table (lgw-rtb-…).
type EC2LocalGatewayRouteTable struct {
	LocalGatewayRouteTableId string
	LocalGatewayId           string
	OutpostArn               string
	OwnerId                  string
	State                    string
	Mode                     string
	Tags                     []EC2Tag
}

// EC2LocalGatewayVirtualInterface is a local gateway virtual interface
// (lgw-vif-…).
type EC2LocalGatewayVirtualInterface struct {
	LocalGatewayVirtualInterfaceId      string
	LocalGatewayId                      string
	LocalGatewayVirtualInterfaceGroupId string
	OutpostLagId                        string
	Vlan                                int
	LocalAddress                        string
	PeerAddress                         string
	LocalBgpAsn                         int
	PeerBgpAsn                          int
	OwnerId                             string
	ConfigurationState                  string
	Tags                                []EC2Tag
}

// EC2LocalGatewayVirtualInterfaceGroup is a local gateway virtual interface
// group (lgw-vif-grp-…).
type EC2LocalGatewayVirtualInterfaceGroup struct {
	LocalGatewayVirtualInterfaceGroupId string
	LocalGatewayVirtualInterfaceIds     []string
	LocalGatewayId                      string
	OwnerId                             string
	LocalBgpAsn                         int
	ConfigurationState                  string
	Tags                                []EC2Tag
}

// EC2LocalGatewayRouteTableVigAssociation records a route-table ↔ virtual-
// interface-group association (lgw-vif-grp-assoc-…).
type EC2LocalGatewayRouteTableVigAssociation struct {
	AssociationId                       string
	LocalGatewayVirtualInterfaceGroupId string
	LocalGatewayId                      string
	LocalGatewayRouteTableId            string
	OwnerId                             string
	State                               string
	Tags                                []EC2Tag
}

// EC2LocalGatewayRouteTableVpcAssociation records a route-table ↔ VPC
// association (lgw-vpc-assoc-…).
type EC2LocalGatewayRouteTableVpcAssociation struct {
	AssociationId            string
	LocalGatewayRouteTableId string
	LocalGatewayId           string
	VpcId                    string
	OwnerId                  string
	State                    string
	Tags                     []EC2Tag
}

// EC2CapacityManagerState is the single account-level Capacity Manager record.
type EC2CapacityManagerState struct {
	Status              string // enabled | disabled
	OrganizationsAccess bool
	EnabledAt           string
}

// EC2CapacityManagerDataExport is a Capacity Manager data export to S3
// (cmde-…).
type EC2CapacityManagerDataExport struct {
	CapacityManagerDataExportId string
	S3BucketName                string
	S3BucketPrefix              string
	Schedule                    string
	OutputFormat                string
	CreateTime                  string
	Tags                        []EC2Tag
}

// EC2CapacityManagerMonitoredTagKey is a monitored tag key registered for
// Capacity Manager metric grouping.
type EC2CapacityManagerMonitoredTagKey struct {
	TagKey                  string
	Status                  string // activating | activated | deactivating | suspended
	CapacityManagerProvided bool
}

// EC2DeclarativePoliciesReport is a declarative-policies compliance report
// delivered to S3 over an organization target.
type EC2DeclarativePoliciesReport struct {
	ReportId  string
	S3Bucket  string
	S3Prefix  string
	TargetId  string
	StartTime string
	EndTime   string
	Status    string // running | cancelled | complete | error
	Tags      []EC2Tag
}

// EC2NetworkPerformanceSubscription is a source/destination region metric
// subscription, keyed by source/destination/metric/statistic.
type EC2NetworkPerformanceSubscription struct {
	Source      string
	Destination string
	Metric      string
	Statistic   string
	Period      string
}

var (
	ec2LocalGateways                 sim.Store[EC2LocalGateway]
	ec2LocalGatewayRouteTables       sim.Store[EC2LocalGatewayRouteTable]
	ec2LocalGatewayVifs              sim.Store[EC2LocalGatewayVirtualInterface]
	ec2LocalGatewayVifGroups         sim.Store[EC2LocalGatewayVirtualInterfaceGroup]
	ec2LocalGatewayVigAssocs         sim.Store[EC2LocalGatewayRouteTableVigAssociation]
	ec2LocalGatewayVpcAssocs         sim.Store[EC2LocalGatewayRouteTableVpcAssociation]
	ec2CapacityManager               sim.Store[EC2CapacityManagerState]
	ec2CapacityManagerDataExports    sim.Store[EC2CapacityManagerDataExport]
	ec2CapacityManagerMonitoredTags  sim.Store[EC2CapacityManagerMonitoredTagKey]
	ec2DeclarativePoliciesReports    sim.Store[EC2DeclarativePoliciesReport]
	ec2NetworkPerformanceSubscriptns sim.Store[EC2NetworkPerformanceSubscription]
)

// ec2CapacityManagerKey is the single-record key for the account-level Capacity
// Manager state (real AWS keeps one per account-region).
const ec2CapacityManagerKey = "account"

func registerEC2LgwCapacity(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2LocalGateways = sim.MakeStore[EC2LocalGateway](srv.DB(), "ec2_local_gateways")
	ec2LocalGatewayRouteTables = sim.MakeStore[EC2LocalGatewayRouteTable](srv.DB(), "ec2_local_gateway_route_tables")
	ec2LocalGatewayVifs = sim.MakeStore[EC2LocalGatewayVirtualInterface](srv.DB(), "ec2_local_gateway_vifs")
	ec2LocalGatewayVifGroups = sim.MakeStore[EC2LocalGatewayVirtualInterfaceGroup](srv.DB(), "ec2_local_gateway_vif_groups")
	ec2LocalGatewayVigAssocs = sim.MakeStore[EC2LocalGatewayRouteTableVigAssociation](srv.DB(), "ec2_local_gateway_vig_associations")
	ec2LocalGatewayVpcAssocs = sim.MakeStore[EC2LocalGatewayRouteTableVpcAssociation](srv.DB(), "ec2_local_gateway_vpc_associations")
	ec2CapacityManager = sim.MakeStore[EC2CapacityManagerState](srv.DB(), "ec2_capacity_manager")
	ec2CapacityManagerDataExports = sim.MakeStore[EC2CapacityManagerDataExport](srv.DB(), "ec2_capacity_manager_data_exports")
	ec2CapacityManagerMonitoredTags = sim.MakeStore[EC2CapacityManagerMonitoredTagKey](srv.DB(), "ec2_capacity_manager_monitored_tags")
	ec2DeclarativePoliciesReports = sim.MakeStore[EC2DeclarativePoliciesReport](srv.DB(), "ec2_declarative_policies_reports")
	ec2NetworkPerformanceSubscriptns = sim.MakeStore[EC2NetworkPerformanceSubscription](srv.DB(), "ec2_network_performance_subscriptions")

	for action, h := range map[string]http.HandlerFunc{
		// Local Gateway (Outposts).
		"DescribeLocalGateways":                                           handleDescribeLocalGateways,
		"CreateLocalGatewayRouteTable":                                    handleCreateLocalGatewayRouteTable,
		"DeleteLocalGatewayRouteTable":                                    handleDeleteLocalGatewayRouteTable,
		"DescribeLocalGatewayRouteTables":                                 handleDescribeLocalGatewayRouteTables,
		"CreateLocalGatewayVirtualInterface":                              handleCreateLocalGatewayVirtualInterface,
		"DeleteLocalGatewayVirtualInterface":                              handleDeleteLocalGatewayVirtualInterface,
		"DescribeLocalGatewayVirtualInterfaces":                           handleDescribeLocalGatewayVirtualInterfaces,
		"CreateLocalGatewayVirtualInterfaceGroup":                         handleCreateLocalGatewayVirtualInterfaceGroup,
		"DeleteLocalGatewayVirtualInterfaceGroup":                         handleDeleteLocalGatewayVirtualInterfaceGroup,
		"DescribeLocalGatewayVirtualInterfaceGroups":                      handleDescribeLocalGatewayVirtualInterfaceGroups,
		"CreateLocalGatewayRouteTableVirtualInterfaceGroupAssociation":    handleCreateLocalGatewayVigAssociation,
		"DeleteLocalGatewayRouteTableVirtualInterfaceGroupAssociation":    handleDeleteLocalGatewayVigAssociation,
		"DescribeLocalGatewayRouteTableVirtualInterfaceGroupAssociations": handleDescribeLocalGatewayVigAssociations,
		"CreateLocalGatewayRouteTableVpcAssociation":                      handleCreateLocalGatewayVpcAssociation,
		"DeleteLocalGatewayRouteTableVpcAssociation":                      handleDeleteLocalGatewayVpcAssociation,
		"DescribeLocalGatewayRouteTableVpcAssociations":                   handleDescribeLocalGatewayVpcAssociations,

		// Capacity Manager.
		"EnableCapacityManager":                    handleEnableCapacityManager,
		"DisableCapacityManager":                   handleDisableCapacityManager,
		"GetCapacityManagerAttributes":             handleGetCapacityManagerAttributes,
		"CreateCapacityManagerDataExport":          handleCreateCapacityManagerDataExport,
		"DeleteCapacityManagerDataExport":          handleDeleteCapacityManagerDataExport,
		"DescribeCapacityManagerDataExports":       handleDescribeCapacityManagerDataExports,
		"GetCapacityManagerMetricData":             handleGetCapacityManagerMetricData,
		"GetCapacityManagerMetricDimensions":       handleGetCapacityManagerMetricDimensions,
		"GetCapacityManagerMonitoredTagKeys":       handleGetCapacityManagerMonitoredTagKeys,
		"UpdateCapacityManagerMonitoredTagKeys":    handleUpdateCapacityManagerMonitoredTagKeys,
		"UpdateCapacityManagerOrganizationsAccess": handleUpdateCapacityManagerOrganizationsAccess,

		// Declarative Policies.
		"StartDeclarativePoliciesReport":      handleStartDeclarativePoliciesReport,
		"CancelDeclarativePoliciesReport":     handleCancelDeclarativePoliciesReport,
		"DescribeDeclarativePoliciesReports":  handleDescribeDeclarativePoliciesReports,
		"GetDeclarativePoliciesReportSummary": handleGetDeclarativePoliciesReportSummary,

		// AWS Network Performance.
		"EnableAwsNetworkPerformanceMetricSubscription":    handleEnableAwsNetworkPerformanceMetricSubscription,
		"DisableAwsNetworkPerformanceMetricSubscription":   handleDisableAwsNetworkPerformanceMetricSubscription,
		"DescribeAwsNetworkPerformanceMetricSubscriptions": handleDescribeAwsNetworkPerformanceMetricSubscriptions,
		"GetAwsNetworkPerformanceData":                     handleGetAwsNetworkPerformanceData,
	} {
		r.Register(action, h)
	}
}

// Helpers

// ensureSeedLocalGateway returns the account's deterministic seeded local
// gateway, creating it on first use. Real accounts get one local gateway per
// Outpost; the sim provides a single one so the route-table / virtual-interface
// CRUD has a parent. Mirrors ensureSimDefaults' deterministic-ID convention.
func ensureSeedLocalGateway() EC2LocalGateway {
	const id = "lgw-sim00000000000000"
	if g, ok := ec2LocalGateways.Get(id); ok {
		return g
	}
	g := EC2LocalGateway{
		LocalGatewayId: id,
		OutpostArn:     fmt.Sprintf("arn:aws:outposts:%s:%s:outpost/op-sim00000000000000", awsRegion(), ec2Owner()),
		OwnerId:        ec2Owner(),
		State:          "available",
	}
	ec2LocalGateways.Put(id, g)

	// A real Outpost local gateway ships with its virtual interface group and
	// virtual interfaces already provisioned by AWS (they are not customer-
	// created — only the route table and the associations are). Seed one of
	// each deterministically so describe / association flows have them.
	const vigID = "lgw-vif-grp-sim000000"
	const vifID = "lgw-vif-sim0000000000"
	if _, ok := ec2LocalGatewayVifGroups.Get(vigID); !ok {
		ec2LocalGatewayVifGroups.Put(vigID, EC2LocalGatewayVirtualInterfaceGroup{
			LocalGatewayVirtualInterfaceGroupId: vigID,
			LocalGatewayVirtualInterfaceIds:     []string{vifID},
			LocalGatewayId:                      id,
			OwnerId:                             ec2Owner(),
			LocalBgpAsn:                         64512,
			ConfigurationState:                  "available",
		})
		ec2LocalGatewayVifs.Put(vifID, EC2LocalGatewayVirtualInterface{
			LocalGatewayVirtualInterfaceId:      vifID,
			LocalGatewayId:                      id,
			LocalGatewayVirtualInterfaceGroupId: vigID,
			OutpostLagId:                        "ola-sim00000000000000",
			Vlan:                                100,
			LocalAddress:                        "10.0.0.1/30",
			PeerAddress:                         "10.0.0.2/30",
			LocalBgpAsn:                         64512,
			PeerBgpAsn:                          64513,
			OwnerId:                             ec2Owner(),
			ConfigurationState:                  "available",
		})
	}
	return g
}

// ec2ArnFor builds an EC2 resource ARN of the given type.
func ec2ArnFor(resType, id string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:%s/%s", awsRegion(), ec2Owner(), resType, id)
}

func lgwStateReasonXML() string {
	return "<stateReason><code>resource-available</code><message>The resource is available</message></stateReason>"
}

// Local Gateway

func lgwBodyXML(g EC2LocalGateway) string {
	return fmt.Sprintf("<localGatewayId>%s</localGatewayId><outpostArn>%s</outpostArn><ownerId>%s</ownerId><state>%s</state>%s",
		g.LocalGatewayId, g.OutpostArn, g.OwnerId, g.State, writeTagSetXML(g.Tags))
}

func handleDescribeLocalGateways(w http.ResponseWriter, r *http.Request) {
	ensureSeedLocalGateway()
	var gateways []EC2LocalGateway
	if id := r.FormValue("LocalGatewayId.1"); id != "" {
		if g, ok := ec2LocalGateways.Get(id); ok {
			gateways = append(gateways, g)
		}
	} else {
		gateways = ec2LocalGateways.List()
	}
	var items strings.Builder
	for _, g := range gateways {
		fmt.Fprintf(&items, "<item>%s</item>", lgwBodyXML(g))
	}
	tgwResponse(w, "DescribeLocalGateways", fmt.Sprintf("<localGatewaySet>%s</localGatewaySet>", items.String()))
}

func lgwRouteTableBodyXML(rt EC2LocalGatewayRouteTable) string {
	return fmt.Sprintf("<localGatewayRouteTableId>%s</localGatewayRouteTableId><localGatewayRouteTableArn>%s</localGatewayRouteTableArn><localGatewayId>%s</localGatewayId><outpostArn>%s</outpostArn><ownerId>%s</ownerId><state>%s</state>%s<mode>%s</mode>%s",
		rt.LocalGatewayRouteTableId, ec2ArnFor("local-gateway-route-table", rt.LocalGatewayRouteTableId),
		rt.LocalGatewayId, rt.OutpostArn, rt.OwnerId, rt.State, writeTagSetXML(rt.Tags), rt.Mode, lgwStateReasonXML())
}

func handleCreateLocalGatewayRouteTable(w http.ResponseWriter, r *http.Request) {
	lgwID := r.FormValue("LocalGatewayId")
	g, ok := ec2LocalGateways.Get(lgwID)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayID.NotFound", "The local gateway ID '"+lgwID+"' does not exist", http.StatusBadRequest)
		return
	}
	mode := r.FormValue("Mode")
	if mode == "" {
		mode = "direct-vpc-routing"
	}
	id := ec2ID("lgw-rtb")
	rt := EC2LocalGatewayRouteTable{
		LocalGatewayRouteTableId: id,
		LocalGatewayId:           lgwID,
		OutpostArn:               g.OutpostArn,
		OwnerId:                  ec2Owner(),
		State:                    "available",
		Mode:                     mode,
		Tags:                     parseTags(r),
	}
	ec2LocalGatewayRouteTables.Put(id, rt)
	tgwResponse(w, "CreateLocalGatewayRouteTable", fmt.Sprintf("<localGatewayRouteTable>%s</localGatewayRouteTable>", lgwRouteTableBodyXML(rt)))
}

func handleDeleteLocalGatewayRouteTable(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("LocalGatewayRouteTableId")
	rt, ok := ec2LocalGatewayRouteTables.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayRouteTableID.NotFound", "The local gateway route table ID '"+id+"' does not exist", http.StatusBadRequest)
		return
	}
	rt.State = "deleted"
	ec2LocalGatewayRouteTables.Delete(id)
	tgwResponse(w, "DeleteLocalGatewayRouteTable", fmt.Sprintf("<localGatewayRouteTable>%s</localGatewayRouteTable>", lgwRouteTableBodyXML(rt)))
}

func handleDescribeLocalGatewayRouteTables(w http.ResponseWriter, r *http.Request) {
	var tables []EC2LocalGatewayRouteTable
	if id := r.FormValue("LocalGatewayRouteTableId.1"); id != "" {
		if rt, ok := ec2LocalGatewayRouteTables.Get(id); ok {
			tables = append(tables, rt)
		}
	} else {
		tables = ec2LocalGatewayRouteTables.List()
	}
	var items strings.Builder
	for _, rt := range tables {
		fmt.Fprintf(&items, "<item>%s</item>", lgwRouteTableBodyXML(rt))
	}
	tgwResponse(w, "DescribeLocalGatewayRouteTables", fmt.Sprintf("<localGatewayRouteTableSet>%s</localGatewayRouteTableSet>", items.String()))
}

func lgwVifBodyXML(v EC2LocalGatewayVirtualInterface) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<localGatewayVirtualInterfaceId>%s</localGatewayVirtualInterfaceId><localGatewayId>%s</localGatewayId><localGatewayVirtualInterfaceGroupId>%s</localGatewayVirtualInterfaceGroupId><localGatewayVirtualInterfaceArn>%s</localGatewayVirtualInterfaceArn><vlan>%d</vlan><localAddress>%s</localAddress><peerAddress>%s</peerAddress><localBgpAsn>%d</localBgpAsn><peerBgpAsn>%d</peerBgpAsn><ownerId>%s</ownerId>%s<configurationState>%s</configurationState>",
		v.LocalGatewayVirtualInterfaceId, v.LocalGatewayId, v.LocalGatewayVirtualInterfaceGroupId,
		ec2ArnFor("local-gateway-virtual-interface", v.LocalGatewayVirtualInterfaceId), v.Vlan,
		v.LocalAddress, v.PeerAddress, v.LocalBgpAsn, v.PeerBgpAsn, v.OwnerId,
		writeTagSetXML(v.Tags), v.ConfigurationState)
	return b.String()
}

func handleCreateLocalGatewayVirtualInterface(w http.ResponseWriter, r *http.Request) {
	g := ensureSeedLocalGateway()
	grpID := r.FormValue("LocalGatewayVirtualInterfaceGroupId")
	if grpID != "" {
		if grp, ok := ec2LocalGatewayVifGroups.Get(grpID); ok {
			g, _ = ec2LocalGateways.Get(grp.LocalGatewayId)
		} else {
			ec2ErrorXML(w, "InvalidLocalGatewayVirtualInterfaceGroupID.NotFound", "The local gateway virtual interface group ID '"+grpID+"' does not exist", http.StatusBadRequest)
			return
		}
	}
	id := ec2ID("lgw-vif")
	vif := EC2LocalGatewayVirtualInterface{
		LocalGatewayVirtualInterfaceId:      id,
		LocalGatewayId:                      g.LocalGatewayId,
		LocalGatewayVirtualInterfaceGroupId: grpID,
		OutpostLagId:                        r.FormValue("OutpostLagId"),
		Vlan:                                ec2AtoiOr(r.FormValue("Vlan"), 0),
		LocalAddress:                        r.FormValue("LocalAddress"),
		PeerAddress:                         r.FormValue("PeerAddress"),
		PeerBgpAsn:                          ec2AtoiOr(r.FormValue("PeerBgpAsn"), 0),
		LocalBgpAsn:                         64512,
		OwnerId:                             ec2Owner(),
		ConfigurationState:                  "available",
		Tags:                                parseTags(r),
	}
	ec2LocalGatewayVifs.Put(id, vif)
	// Attach the new VIF to its group's id list, if one was named.
	if grpID != "" {
		if grp, ok := ec2LocalGatewayVifGroups.Get(grpID); ok {
			grp.LocalGatewayVirtualInterfaceIds = append(grp.LocalGatewayVirtualInterfaceIds, id)
			ec2LocalGatewayVifGroups.Put(grpID, grp)
		}
	}
	tgwResponse(w, "CreateLocalGatewayVirtualInterface", fmt.Sprintf("<localGatewayVirtualInterface>%s</localGatewayVirtualInterface>", lgwVifBodyXML(vif)))
}

func handleDeleteLocalGatewayVirtualInterface(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("LocalGatewayVirtualInterfaceId")
	vif, ok := ec2LocalGatewayVifs.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayVirtualInterfaceID.NotFound", "The local gateway virtual interface ID '"+id+"' does not exist", http.StatusBadRequest)
		return
	}
	vif.ConfigurationState = "deleted"
	ec2LocalGatewayVifs.Delete(id)
	tgwResponse(w, "DeleteLocalGatewayVirtualInterface", fmt.Sprintf("<localGatewayVirtualInterface>%s</localGatewayVirtualInterface>", lgwVifBodyXML(vif)))
}

func handleDescribeLocalGatewayVirtualInterfaces(w http.ResponseWriter, r *http.Request) {
	ensureSeedLocalGateway()
	var vifs []EC2LocalGatewayVirtualInterface
	if id := r.FormValue("LocalGatewayVirtualInterfaceId.1"); id != "" {
		if v, ok := ec2LocalGatewayVifs.Get(id); ok {
			vifs = append(vifs, v)
		}
	} else {
		vifs = ec2LocalGatewayVifs.List()
	}
	var items strings.Builder
	for _, v := range vifs {
		fmt.Fprintf(&items, "<item>%s</item>", lgwVifBodyXML(v))
	}
	tgwResponse(w, "DescribeLocalGatewayVirtualInterfaces", fmt.Sprintf("<localGatewayVirtualInterfaceSet>%s</localGatewayVirtualInterfaceSet>", items.String()))
}

func lgwVifGroupBodyXML(grp EC2LocalGatewayVirtualInterfaceGroup) string {
	var ids strings.Builder
	ids.WriteString("<localGatewayVirtualInterfaceIdSet>")
	for _, vid := range grp.LocalGatewayVirtualInterfaceIds {
		fmt.Fprintf(&ids, "<item>%s</item>", vid)
	}
	ids.WriteString("</localGatewayVirtualInterfaceIdSet>")
	return fmt.Sprintf("<localGatewayVirtualInterfaceGroupId>%s</localGatewayVirtualInterfaceGroupId>%s<localGatewayId>%s</localGatewayId><ownerId>%s</ownerId><localBgpAsn>%d</localBgpAsn><localGatewayVirtualInterfaceGroupArn>%s</localGatewayVirtualInterfaceGroupArn>%s<configurationState>%s</configurationState>",
		grp.LocalGatewayVirtualInterfaceGroupId, ids.String(), grp.LocalGatewayId, grp.OwnerId,
		grp.LocalBgpAsn, ec2ArnFor("local-gateway-virtual-interface-group", grp.LocalGatewayVirtualInterfaceGroupId),
		writeTagSetXML(grp.Tags), grp.ConfigurationState)
}

func handleCreateLocalGatewayVirtualInterfaceGroup(w http.ResponseWriter, r *http.Request) {
	lgwID := r.FormValue("LocalGatewayId")
	if _, ok := ec2LocalGateways.Get(lgwID); !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayID.NotFound", "The local gateway ID '"+lgwID+"' does not exist", http.StatusBadRequest)
		return
	}
	id := ec2ID("lgw-vif-grp")
	grp := EC2LocalGatewayVirtualInterfaceGroup{
		LocalGatewayVirtualInterfaceGroupId: id,
		LocalGatewayId:                      lgwID,
		OwnerId:                             ec2Owner(),
		LocalBgpAsn:                         ec2AtoiOr(r.FormValue("LocalBgpAsn"), 64512),
		ConfigurationState:                  "available",
		Tags:                                parseTags(r),
	}
	ec2LocalGatewayVifGroups.Put(id, grp)
	tgwResponse(w, "CreateLocalGatewayVirtualInterfaceGroup", fmt.Sprintf("<localGatewayVirtualInterfaceGroup>%s</localGatewayVirtualInterfaceGroup>", lgwVifGroupBodyXML(grp)))
}

func handleDeleteLocalGatewayVirtualInterfaceGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("LocalGatewayVirtualInterfaceGroupId")
	grp, ok := ec2LocalGatewayVifGroups.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayVirtualInterfaceGroupID.NotFound", "The local gateway virtual interface group ID '"+id+"' does not exist", http.StatusBadRequest)
		return
	}
	grp.ConfigurationState = "deleted"
	ec2LocalGatewayVifGroups.Delete(id)
	tgwResponse(w, "DeleteLocalGatewayVirtualInterfaceGroup", fmt.Sprintf("<localGatewayVirtualInterfaceGroup>%s</localGatewayVirtualInterfaceGroup>", lgwVifGroupBodyXML(grp)))
}

func handleDescribeLocalGatewayVirtualInterfaceGroups(w http.ResponseWriter, r *http.Request) {
	ensureSeedLocalGateway()
	var groups []EC2LocalGatewayVirtualInterfaceGroup
	if id := r.FormValue("LocalGatewayVirtualInterfaceGroupId.1"); id != "" {
		if grp, ok := ec2LocalGatewayVifGroups.Get(id); ok {
			groups = append(groups, grp)
		}
	} else {
		groups = ec2LocalGatewayVifGroups.List()
	}
	var items strings.Builder
	for _, grp := range groups {
		fmt.Fprintf(&items, "<item>%s</item>", lgwVifGroupBodyXML(grp))
	}
	tgwResponse(w, "DescribeLocalGatewayVirtualInterfaceGroups", fmt.Sprintf("<localGatewayVirtualInterfaceGroupSet>%s</localGatewayVirtualInterfaceGroupSet>", items.String()))
}

func lgwVigAssocBodyXML(a EC2LocalGatewayRouteTableVigAssociation) string {
	return fmt.Sprintf("<localGatewayRouteTableVirtualInterfaceGroupAssociationId>%s</localGatewayRouteTableVirtualInterfaceGroupAssociationId><localGatewayVirtualInterfaceGroupId>%s</localGatewayVirtualInterfaceGroupId><localGatewayId>%s</localGatewayId><localGatewayRouteTableId>%s</localGatewayRouteTableId><localGatewayRouteTableArn>%s</localGatewayRouteTableArn><ownerId>%s</ownerId><state>%s</state>%s",
		a.AssociationId, a.LocalGatewayVirtualInterfaceGroupId, a.LocalGatewayId, a.LocalGatewayRouteTableId,
		ec2ArnFor("local-gateway-route-table", a.LocalGatewayRouteTableId), a.OwnerId, a.State, writeTagSetXML(a.Tags))
}

func handleCreateLocalGatewayVigAssociation(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("LocalGatewayRouteTableId")
	rt, ok := ec2LocalGatewayRouteTables.Get(rtID)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayRouteTableID.NotFound", "The local gateway route table ID '"+rtID+"' does not exist", http.StatusBadRequest)
		return
	}
	grpID := r.FormValue("LocalGatewayVirtualInterfaceGroupId")
	if _, ok := ec2LocalGatewayVifGroups.Get(grpID); !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayVirtualInterfaceGroupID.NotFound", "The local gateway virtual interface group ID '"+grpID+"' does not exist", http.StatusBadRequest)
		return
	}
	id := ec2ID("lgw-vif-grp-assoc")
	assoc := EC2LocalGatewayRouteTableVigAssociation{
		AssociationId:                       id,
		LocalGatewayVirtualInterfaceGroupId: grpID,
		LocalGatewayId:                      rt.LocalGatewayId,
		LocalGatewayRouteTableId:            rtID,
		OwnerId:                             ec2Owner(),
		State:                               "associated",
		Tags:                                parseTags(r),
	}
	ec2LocalGatewayVigAssocs.Put(id, assoc)
	tgwResponse(w, "CreateLocalGatewayRouteTableVirtualInterfaceGroupAssociation", fmt.Sprintf("<localGatewayRouteTableVirtualInterfaceGroupAssociation>%s</localGatewayRouteTableVirtualInterfaceGroupAssociation>", lgwVigAssocBodyXML(assoc)))
}

func handleDeleteLocalGatewayVigAssociation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("LocalGatewayRouteTableVirtualInterfaceGroupAssociationId")
	assoc, ok := ec2LocalGatewayVigAssocs.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayRouteTableVirtualInterfaceGroupAssociationID.NotFound", "The association ID '"+id+"' does not exist", http.StatusBadRequest)
		return
	}
	assoc.State = "disassociated"
	ec2LocalGatewayVigAssocs.Delete(id)
	tgwResponse(w, "DeleteLocalGatewayRouteTableVirtualInterfaceGroupAssociation", fmt.Sprintf("<localGatewayRouteTableVirtualInterfaceGroupAssociation>%s</localGatewayRouteTableVirtualInterfaceGroupAssociation>", lgwVigAssocBodyXML(assoc)))
}

func handleDescribeLocalGatewayVigAssociations(w http.ResponseWriter, r *http.Request) {
	var assocs []EC2LocalGatewayRouteTableVigAssociation
	if id := r.FormValue("LocalGatewayRouteTableVirtualInterfaceGroupAssociationId.1"); id != "" {
		if a, ok := ec2LocalGatewayVigAssocs.Get(id); ok {
			assocs = append(assocs, a)
		}
	} else {
		assocs = ec2LocalGatewayVigAssocs.List()
	}
	var items strings.Builder
	for _, a := range assocs {
		fmt.Fprintf(&items, "<item>%s</item>", lgwVigAssocBodyXML(a))
	}
	tgwResponse(w, "DescribeLocalGatewayRouteTableVirtualInterfaceGroupAssociations", fmt.Sprintf("<localGatewayRouteTableVirtualInterfaceGroupAssociationSet>%s</localGatewayRouteTableVirtualInterfaceGroupAssociationSet>", items.String()))
}

func lgwVpcAssocBodyXML(a EC2LocalGatewayRouteTableVpcAssociation) string {
	return fmt.Sprintf("<localGatewayRouteTableVpcAssociationId>%s</localGatewayRouteTableVpcAssociationId><localGatewayRouteTableId>%s</localGatewayRouteTableId><localGatewayRouteTableArn>%s</localGatewayRouteTableArn><localGatewayId>%s</localGatewayId><vpcId>%s</vpcId><ownerId>%s</ownerId><state>%s</state>%s",
		a.AssociationId, a.LocalGatewayRouteTableId, ec2ArnFor("local-gateway-route-table", a.LocalGatewayRouteTableId),
		a.LocalGatewayId, a.VpcId, a.OwnerId, a.State, writeTagSetXML(a.Tags))
}

func handleCreateLocalGatewayVpcAssociation(w http.ResponseWriter, r *http.Request) {
	rtID := r.FormValue("LocalGatewayRouteTableId")
	rt, ok := ec2LocalGatewayRouteTables.Get(rtID)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayRouteTableID.NotFound", "The local gateway route table ID '"+rtID+"' does not exist", http.StatusBadRequest)
		return
	}
	vpcID := r.FormValue("VpcId")
	if _, ok := ec2Vpcs.Get(vpcID); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", "The vpc ID '"+vpcID+"' does not exist", http.StatusBadRequest)
		return
	}
	id := ec2ID("lgw-vpc-assoc")
	assoc := EC2LocalGatewayRouteTableVpcAssociation{
		AssociationId:            id,
		LocalGatewayRouteTableId: rtID,
		LocalGatewayId:           rt.LocalGatewayId,
		VpcId:                    vpcID,
		OwnerId:                  ec2Owner(),
		State:                    "associated",
		Tags:                     parseTags(r),
	}
	ec2LocalGatewayVpcAssocs.Put(id, assoc)
	tgwResponse(w, "CreateLocalGatewayRouteTableVpcAssociation", fmt.Sprintf("<localGatewayRouteTableVpcAssociation>%s</localGatewayRouteTableVpcAssociation>", lgwVpcAssocBodyXML(assoc)))
}

func handleDeleteLocalGatewayVpcAssociation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("LocalGatewayRouteTableVpcAssociationId")
	assoc, ok := ec2LocalGatewayVpcAssocs.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidLocalGatewayRouteTableVpcAssociationID.NotFound", "The association ID '"+id+"' does not exist", http.StatusBadRequest)
		return
	}
	assoc.State = "disassociated"
	ec2LocalGatewayVpcAssocs.Delete(id)
	tgwResponse(w, "DeleteLocalGatewayRouteTableVpcAssociation", fmt.Sprintf("<localGatewayRouteTableVpcAssociation>%s</localGatewayRouteTableVpcAssociation>", lgwVpcAssocBodyXML(assoc)))
}

func handleDescribeLocalGatewayVpcAssociations(w http.ResponseWriter, r *http.Request) {
	var assocs []EC2LocalGatewayRouteTableVpcAssociation
	if id := r.FormValue("LocalGatewayRouteTableVpcAssociationId.1"); id != "" {
		if a, ok := ec2LocalGatewayVpcAssocs.Get(id); ok {
			assocs = append(assocs, a)
		}
	} else {
		assocs = ec2LocalGatewayVpcAssocs.List()
	}
	var items strings.Builder
	for _, a := range assocs {
		fmt.Fprintf(&items, "<item>%s</item>", lgwVpcAssocBodyXML(a))
	}
	tgwResponse(w, "DescribeLocalGatewayRouteTableVpcAssociations", fmt.Sprintf("<localGatewayRouteTableVpcAssociationSet>%s</localGatewayRouteTableVpcAssociationSet>", items.String()))
}

// Capacity Manager

func capacityManagerState() EC2CapacityManagerState {
	if s, ok := ec2CapacityManager.Get(ec2CapacityManagerKey); ok {
		return s
	}
	return EC2CapacityManagerState{Status: "disabled"}
}

func handleEnableCapacityManager(w http.ResponseWriter, r *http.Request) {
	s := capacityManagerState()
	s.Status = "enabled"
	if s.EnabledAt == "" {
		s.EnabledAt = time.Now().UTC().Format(time.RFC3339)
	}
	if v := r.FormValue("OrganizationsAccess"); v != "" {
		s.OrganizationsAccess = v == "true"
	}
	ec2CapacityManager.Put(ec2CapacityManagerKey, s)
	tgwResponse(w, "EnableCapacityManager", fmt.Sprintf("<capacityManagerStatus>%s</capacityManagerStatus><organizationsAccess>%t</organizationsAccess>", s.Status, s.OrganizationsAccess))
}

func handleDisableCapacityManager(w http.ResponseWriter, r *http.Request) {
	s := capacityManagerState()
	s.Status = "disabled"
	ec2CapacityManager.Put(ec2CapacityManagerKey, s)
	tgwResponse(w, "DisableCapacityManager", fmt.Sprintf("<capacityManagerStatus>%s</capacityManagerStatus><organizationsAccess>%t</organizationsAccess>", s.Status, s.OrganizationsAccess))
}

func handleGetCapacityManagerAttributes(w http.ResponseWriter, r *http.Request) {
	s := capacityManagerState()
	ingestionStatus := "ingestion-complete"
	if s.Status != "enabled" {
		ingestionStatus = "initial-ingestion-in-progress"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<capacityManagerStatus>%s</capacityManagerStatus><organizationsAccess>%t</organizationsAccess><dataExportCount>%d</dataExportCount><ingestionStatus>%s</ingestionStatus>",
		s.Status, s.OrganizationsAccess, len(ec2CapacityManagerDataExports.List()), ingestionStatus)
	// Datapoint timestamps reflect the real reservation data window when enabled.
	if s.Status == "enabled" {
		earliest, latest := capacityReservationWindow()
		fmt.Fprintf(&b, "<earliestDatapointTimestamp>%s</earliestDatapointTimestamp><latestDatapointTimestamp>%s</latestDatapointTimestamp>", earliest, latest)
	}
	tgwResponse(w, "GetCapacityManagerAttributes", b.String())
}

// capacityReservationWindow returns the earliest CreateDate across the existing
// capacity reservations and the current time, as the honest datapoint window.
func capacityReservationWindow() (earliest, latest string) {
	now := time.Now().UTC()
	earliestT := now
	for _, cr := range ec2CapacityReservations.List() {
		if cr.CreateDate == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, cr.CreateDate); err == nil && t.Before(earliestT) {
			earliestT = t
		}
	}
	return earliestT.Format(time.RFC3339), now.Format(time.RFC3339)
}

func capacityManagerDataExportBodyXML(e EC2CapacityManagerDataExport) string {
	return fmt.Sprintf("<capacityManagerDataExportId>%s</capacityManagerDataExportId><s3BucketName>%s</s3BucketName><s3BucketPrefix>%s</s3BucketPrefix><schedule>%s</schedule><outputFormat>%s</outputFormat><createTime>%s</createTime><latestDeliveryStatus>successful</latestDeliveryStatus><latestDeliveryS3LocationUri>s3://%s/%s</latestDeliveryS3LocationUri><latestDeliveryTime>%s</latestDeliveryTime>%s",
		e.CapacityManagerDataExportId, e.S3BucketName, e.S3BucketPrefix, e.Schedule, e.OutputFormat,
		e.CreateTime, e.S3BucketName, e.S3BucketPrefix, e.CreateTime, writeTagSetXML(e.Tags))
}

func handleCreateCapacityManagerDataExport(w http.ResponseWriter, r *http.Request) {
	schedule := r.FormValue("Schedule")
	if schedule == "" {
		schedule = "hourly"
	}
	format := r.FormValue("OutputFormat")
	if format == "" {
		format = "csv"
	}
	id := ec2ID("cmde")
	e := EC2CapacityManagerDataExport{
		CapacityManagerDataExportId: id,
		S3BucketName:                r.FormValue("S3BucketName"),
		S3BucketPrefix:              r.FormValue("S3BucketPrefix"),
		Schedule:                    schedule,
		OutputFormat:                format,
		CreateTime:                  time.Now().UTC().Format(time.RFC3339),
		Tags:                        parseTags(r),
	}
	ec2CapacityManagerDataExports.Put(id, e)
	tgwResponse(w, "CreateCapacityManagerDataExport", fmt.Sprintf("<capacityManagerDataExportId>%s</capacityManagerDataExportId>", id))
}

func handleDeleteCapacityManagerDataExport(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityManagerDataExportId")
	if _, ok := ec2CapacityManagerDataExports.Get(id); !ok {
		ec2ErrorXML(w, "InvalidCapacityManagerDataExportId.NotFound", "The data export ID '"+id+"' does not exist", http.StatusBadRequest)
		return
	}
	ec2CapacityManagerDataExports.Delete(id)
	tgwResponse(w, "DeleteCapacityManagerDataExport", fmt.Sprintf("<capacityManagerDataExportId>%s</capacityManagerDataExportId>", id))
}

func handleDescribeCapacityManagerDataExports(w http.ResponseWriter, r *http.Request) {
	var exports []EC2CapacityManagerDataExport
	if id := r.FormValue("CapacityManagerDataExportId.1"); id != "" {
		if e, ok := ec2CapacityManagerDataExports.Get(id); ok {
			exports = append(exports, e)
		}
	} else {
		exports = ec2CapacityManagerDataExports.List()
	}
	var items strings.Builder
	for _, e := range exports {
		fmt.Fprintf(&items, "<item>%s</item>", capacityManagerDataExportBodyXML(e))
	}
	tgwResponse(w, "DescribeCapacityManagerDataExports", fmt.Sprintf("<capacityManagerDataExportSet>%s</capacityManagerDataExportSet>", items.String()))
}

// ec2CapacityMetricValue computes an honest value for a Capacity Manager metric
// from the existing reservation store. Count/size/utilization metrics are
// derived from real reservation totals; cost/spot metrics for which the sim has
// no underlying data return 0.
func ec2CapacityMetricValue(metric string, totalInst, availInst, totalVcpu int) float64 {
	used := totalInst - availInst
	switch metric {
	case "RESERVATION_TOTAL_COUNT":
		return float64(totalInst)
	case "RESERVATION_MAX_SIZE_INST", "RESERVATION_AVG_COMMITTED_SIZE_INST", "RESERVATION_MAX_COMMITTED_SIZE_INST", "RESERVATION_MIN_COMMITTED_SIZE_INST":
		return float64(totalInst)
	case "RESERVATION_MAX_SIZE_VCPU", "RESERVATION_AVG_COMMITTED_SIZE_VCPU", "RESERVATION_MAX_COMMITTED_SIZE_VCPU", "RESERVATION_MIN_COMMITTED_SIZE_VCPU":
		return float64(totalVcpu)
	case "RESERVATION_MAX_UNUSED_SIZE_INST", "RESERVATION_MIN_UNUSED_SIZE_INST":
		return float64(availInst)
	case "RESERVATION_AVG_UTILIZATION_INST", "RESERVATION_MAX_UTILIZATION", "RESERVATION_MIN_UTILIZATION":
		if totalInst == 0 {
			return 0
		}
		return float64(used) / float64(totalInst) * 100
	default:
		return 0
	}
}

// vcpusForInstanceType returns a real vCPU count for the common instance
// families the sim deals with, defaulting to 2.
func vcpusForInstanceType(it string) int {
	switch {
	case strings.HasSuffix(it, ".nano"), strings.HasSuffix(it, ".micro"), strings.HasSuffix(it, ".small"):
		return 1
	case strings.HasSuffix(it, ".medium"), strings.HasSuffix(it, ".large"):
		return 2
	case strings.HasSuffix(it, ".xlarge"):
		return 4
	case strings.HasSuffix(it, ".2xlarge"):
		return 8
	default:
		return 2
	}
}

func handleGetCapacityManagerMetricData(w http.ResponseWriter, r *http.Request) {
	metrics := ec2NumberedList(r, "MetricName")
	if len(metrics) == 0 {
		metrics = []string{"RESERVATION_TOTAL_COUNT"}
	}
	ts := time.Now().UTC().Format(time.RFC3339)

	// Aggregate the real reservation store into per-reservation dimensions so the
	// metric points carry honest counts/sizes/utilization.
	var results strings.Builder
	reservations := ec2CapacityReservations.List()
	for _, cr := range reservations {
		vcpu := vcpusForInstanceType(cr.InstanceType) * cr.TotalInstanceCount
		var values strings.Builder
		for _, m := range metrics {
			v := ec2CapacityMetricValue(m, cr.TotalInstanceCount, cr.AvailableInstanceCount, vcpu)
			fmt.Fprintf(&values, "<item><metric>%s</metric><value>%s</value></item>", m, strconv.FormatFloat(v, 'f', -1, 64))
		}
		dim := fmt.Sprintf("<resourceRegion>%s</resourceRegion><availabilityZoneId>%s-az1</availabilityZoneId><accountId>%s</accountId><instanceType>%s</instanceType><instancePlatform>%s</instancePlatform><reservationId>%s</reservationId><reservationArn>%s</reservationArn><reservationType>capacity-block</reservationType><tenancy>%s</tenancy><reservationState>%s</reservationState>",
			awsRegion(), awsRegion(), cr.OwnerId, cr.InstanceType, cr.InstancePlatform,
			cr.CapacityReservationId, ec2ArnFor("capacity-reservation", cr.CapacityReservationId), cr.Tenancy, cr.State)
		fmt.Fprintf(&results, "<item><dimension>%s</dimension><timestamp>%s</timestamp><metricValueSet>%s</metricValueSet></item>", dim, ts, values.String())
	}
	tgwResponse(w, "GetCapacityManagerMetricData", fmt.Sprintf("<metricDataResultSet>%s</metricDataResultSet>", results.String()))
}

func handleGetCapacityManagerMetricDimensions(w http.ResponseWriter, r *http.Request) {
	var items strings.Builder
	for _, cr := range ec2CapacityReservations.List() {
		dim := fmt.Sprintf("<resourceRegion>%s</resourceRegion><availabilityZoneId>%s-az1</availabilityZoneId><accountId>%s</accountId><instanceType>%s</instanceType><instancePlatform>%s</instancePlatform><reservationId>%s</reservationId><reservationArn>%s</reservationArn><reservationType>capacity-block</reservationType><tenancy>%s</tenancy><reservationState>%s</reservationState>",
			awsRegion(), awsRegion(), cr.OwnerId, cr.InstanceType, cr.InstancePlatform,
			cr.CapacityReservationId, ec2ArnFor("capacity-reservation", cr.CapacityReservationId), cr.Tenancy, cr.State)
		fmt.Fprintf(&items, "<item>%s</item>", dim)
	}
	tgwResponse(w, "GetCapacityManagerMetricDimensions", fmt.Sprintf("<metricDimensionResultSet>%s</metricDimensionResultSet>", items.String()))
}

func monitoredTagKeyBodyXML(k EC2CapacityManagerMonitoredTagKey) string {
	return fmt.Sprintf("<tagKey>%s</tagKey><status>%s</status><capacityManagerProvided>%t</capacityManagerProvided>", k.TagKey, k.Status, k.CapacityManagerProvided)
}

func handleGetCapacityManagerMonitoredTagKeys(w http.ResponseWriter, r *http.Request) {
	keys := ec2CapacityManagerMonitoredTags.List()
	sort.Slice(keys, func(i, j int) bool { return keys[i].TagKey < keys[j].TagKey })
	var items strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&items, "<item>%s</item>", monitoredTagKeyBodyXML(k))
	}
	tgwResponse(w, "GetCapacityManagerMonitoredTagKeys", fmt.Sprintf("<capacityManagerTagKeySet>%s</capacityManagerTagKeySet>", items.String()))
}

func handleUpdateCapacityManagerMonitoredTagKeys(w http.ResponseWriter, r *http.Request) {
	for _, k := range ec2NumberedList(r, "ActivateTagKey") {
		ec2CapacityManagerMonitoredTags.Put(k, EC2CapacityManagerMonitoredTagKey{TagKey: k, Status: "activated"})
	}
	for _, k := range ec2NumberedList(r, "DeactivateTagKey") {
		ec2CapacityManagerMonitoredTags.Delete(k)
	}
	keys := ec2CapacityManagerMonitoredTags.List()
	sort.Slice(keys, func(i, j int) bool { return keys[i].TagKey < keys[j].TagKey })
	var items strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&items, "<item>%s</item>", monitoredTagKeyBodyXML(k))
	}
	tgwResponse(w, "UpdateCapacityManagerMonitoredTagKeys", fmt.Sprintf("<capacityManagerTagKeySet>%s</capacityManagerTagKeySet>", items.String()))
}

func handleUpdateCapacityManagerOrganizationsAccess(w http.ResponseWriter, r *http.Request) {
	s := capacityManagerState()
	if v := r.FormValue("OrganizationsAccess"); v != "" {
		s.OrganizationsAccess = v == "true"
	}
	ec2CapacityManager.Put(ec2CapacityManagerKey, s)
	tgwResponse(w, "UpdateCapacityManagerOrganizationsAccess", fmt.Sprintf("<capacityManagerStatus>%s</capacityManagerStatus><organizationsAccess>%t</organizationsAccess>", s.Status, s.OrganizationsAccess))
}

// Declarative Policies

func declarativePoliciesReportBodyXML(rp EC2DeclarativePoliciesReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<reportId>%s</reportId><s3Bucket>%s</s3Bucket><s3Prefix>%s</s3Prefix><targetId>%s</targetId><startTime>%s</startTime>",
		rp.ReportId, rp.S3Bucket, rp.S3Prefix, rp.TargetId, rp.StartTime)
	if rp.EndTime != "" {
		fmt.Fprintf(&b, "<endTime>%s</endTime>", rp.EndTime)
	}
	fmt.Fprintf(&b, "<status>%s</status>%s", rp.Status, writeTagSetXML(rp.Tags))
	return b.String()
}

func handleStartDeclarativePoliciesReport(w http.ResponseWriter, r *http.Request) {
	id := "p-" + generateUUID()[:17]
	rp := EC2DeclarativePoliciesReport{
		ReportId:  id,
		S3Bucket:  r.FormValue("S3Bucket"),
		S3Prefix:  r.FormValue("S3Prefix"),
		TargetId:  r.FormValue("TargetId"),
		StartTime: time.Now().UTC().Format(time.RFC3339),
		Status:    "running",
		Tags:      parseTags(r),
	}
	ec2DeclarativePoliciesReports.Put(id, rp)
	tgwResponse(w, "StartDeclarativePoliciesReport", fmt.Sprintf("<reportId>%s</reportId>", id))
}

func handleCancelDeclarativePoliciesReport(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReportId")
	rp, ok := ec2DeclarativePoliciesReports.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidDeclarativePoliciesReportId.NotFound", "The report ID '"+id+"' does not exist", http.StatusBadRequest)
		return
	}
	rp.Status = "cancelled"
	rp.EndTime = time.Now().UTC().Format(time.RFC3339)
	ec2DeclarativePoliciesReports.Put(id, rp)
	tgwResponse(w, "CancelDeclarativePoliciesReport", "<return>true</return>")
}

func handleDescribeDeclarativePoliciesReports(w http.ResponseWriter, r *http.Request) {
	var reports []EC2DeclarativePoliciesReport
	if id := r.FormValue("ReportId.1"); id != "" {
		if rp, ok := ec2DeclarativePoliciesReports.Get(id); ok {
			reports = append(reports, rp)
		}
	} else {
		reports = ec2DeclarativePoliciesReports.List()
	}
	// A running report transitions to complete on the next read, mirroring the
	// real async report's running → complete lifecycle.
	var items strings.Builder
	for i := range reports {
		if reports[i].Status == "running" {
			reports[i].Status = "complete"
			reports[i].EndTime = time.Now().UTC().Format(time.RFC3339)
			ec2DeclarativePoliciesReports.Put(reports[i].ReportId, reports[i])
		}
		fmt.Fprintf(&items, "<item>%s</item>", declarativePoliciesReportBodyXML(reports[i]))
	}
	tgwResponse(w, "DescribeDeclarativePoliciesReports", fmt.Sprintf("<reportSet>%s</reportSet>", items.String()))
}

func handleGetDeclarativePoliciesReportSummary(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReportId")
	rp, ok := ec2DeclarativePoliciesReports.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidDeclarativePoliciesReportId.NotFound", "The report ID '"+id+"' does not exist", http.StatusBadRequest)
		return
	}
	endTime := rp.EndTime
	if endTime == "" {
		endTime = time.Now().UTC().Format(time.RFC3339)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<reportId>%s</reportId><s3Bucket>%s</s3Bucket><s3Prefix>%s</s3Prefix><targetId>%s</targetId><startTime>%s</startTime><endTime>%s</endTime><numberOfAccounts>1</numberOfAccounts><numberOfFailedAccounts>0</numberOfFailedAccounts>",
		rp.ReportId, rp.S3Bucket, rp.S3Prefix, rp.TargetId, rp.StartTime, endTime)
	b.WriteString("<attributeSummarySet><item><attributeName>image-block-public-access</attributeName><mostFrequentValue>block-new-sharing</mostFrequentValue><numberOfMatchedAccounts>1</numberOfMatchedAccounts><numberOfUnmatchedAccounts>0</numberOfUnmatchedAccounts><regionalSummarySet><item><regionName>")
	b.WriteString(awsRegion())
	b.WriteString("</regionName><numberOfMatchedAccounts>1</numberOfMatchedAccounts><numberOfUnmatchedAccounts>0</numberOfUnmatchedAccounts></item></regionalSummarySet></item></attributeSummarySet>")
	tgwResponse(w, "GetDeclarativePoliciesReportSummary", b.String())
}

// AWS Network Performance

func networkPerfSubKey(src, dst, metric, stat string) string {
	return src + "/" + dst + "/" + metric + "/" + stat
}

func handleEnableAwsNetworkPerformanceMetricSubscription(w http.ResponseWriter, r *http.Request) {
	src := r.FormValue("Source")
	dst := r.FormValue("Destination")
	metric := r.FormValue("Metric")
	if metric == "" {
		metric = "aggregate-latency"
	}
	stat := r.FormValue("Statistic")
	if stat == "" {
		stat = "p50"
	}
	sub := EC2NetworkPerformanceSubscription{
		Source:      src,
		Destination: dst,
		Metric:      metric,
		Statistic:   stat,
		Period:      "five-minutes",
	}
	ec2NetworkPerformanceSubscriptns.Put(networkPerfSubKey(src, dst, metric, stat), sub)
	tgwResponse(w, "EnableAwsNetworkPerformanceMetricSubscription", "<output>true</output>")
}

func handleDisableAwsNetworkPerformanceMetricSubscription(w http.ResponseWriter, r *http.Request) {
	src := r.FormValue("Source")
	dst := r.FormValue("Destination")
	metric := r.FormValue("Metric")
	if metric == "" {
		metric = "aggregate-latency"
	}
	stat := r.FormValue("Statistic")
	if stat == "" {
		stat = "p50"
	}
	ec2NetworkPerformanceSubscriptns.Delete(networkPerfSubKey(src, dst, metric, stat))
	tgwResponse(w, "DisableAwsNetworkPerformanceMetricSubscription", "<output>true</output>")
}

func handleDescribeAwsNetworkPerformanceMetricSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs := ec2NetworkPerformanceSubscriptns.List()
	sort.Slice(subs, func(i, j int) bool {
		return networkPerfSubKey(subs[i].Source, subs[i].Destination, subs[i].Metric, subs[i].Statistic) <
			networkPerfSubKey(subs[j].Source, subs[j].Destination, subs[j].Metric, subs[j].Statistic)
	})
	var items strings.Builder
	for _, s := range subs {
		fmt.Fprintf(&items, "<item><source>%s</source><destination>%s</destination><metric>%s</metric><statistic>%s</statistic><period>%s</period></item>",
			s.Source, s.Destination, s.Metric, s.Statistic, s.Period)
	}
	tgwResponse(w, "DescribeAwsNetworkPerformanceMetricSubscriptions", fmt.Sprintf("<subscriptionSet>%s</subscriptionSet>", items.String()))
}

func handleGetAwsNetworkPerformanceData(w http.ResponseWriter, r *http.Request) {
	startStr := r.FormValue("StartTime")
	endStr := r.FormValue("EndTime")
	start := parseEC2Time(startStr, time.Now().UTC().Add(-time.Hour))
	end := parseEC2Time(endStr, time.Now().UTC())

	var responses strings.Builder
	for i := 1; ; i++ {
		base := fmt.Sprintf("DataQuery.%d.", i)
		src := r.FormValue(base + "Source")
		dst := r.FormValue(base + "Destination")
		if src == "" && dst == "" && r.FormValue(base+"Id") == "" {
			break
		}
		id := r.FormValue(base + "Id")
		metric := r.FormValue(base + "Metric")
		if metric == "" {
			metric = "aggregate-latency"
		}
		stat := r.FormValue(base + "Statistic")
		if stat == "" {
			stat = "p50"
		}
		period := r.FormValue(base + "Period")
		if period == "" {
			period = "five-minutes"
		}
		var points strings.Builder
		// One metric point per period bucket across the requested window. The
		// value is the deterministic round-trip-latency for the region pair, in
		// milliseconds — derived from the source/destination strings so it is
		// stable and honest for the simulated topology.
		step := networkPerfPeriodDuration(period)
		val := networkPerfLatencyMs(src, dst)
		for t := start; t.Before(end); t = t.Add(step) {
			fmt.Fprintf(&points, "<item><startDate>%s</startDate><endDate>%s</endDate><value>%s</value><status>OK</status></item>",
				t.UTC().Format(time.RFC3339), t.Add(step).UTC().Format(time.RFC3339), strconv.FormatFloat(val, 'f', -1, 32))
		}
		fmt.Fprintf(&responses, "<item><id>%s</id><source>%s</source><destination>%s</destination><metric>%s</metric><statistic>%s</statistic><period>%s</period><metricPointSet>%s</metricPointSet></item>",
			id, src, dst, metric, stat, period, points.String())
	}
	tgwResponse(w, "GetAwsNetworkPerformanceData", fmt.Sprintf("<dataResponseSet>%s</dataResponseSet>", responses.String()))
}

// networkPerfPeriodDuration maps the PeriodType enum to a time.Duration.
func networkPerfPeriodDuration(period string) time.Duration {
	switch period {
	case "fifteen-minutes":
		return 15 * time.Minute
	case "one-hour":
		return time.Hour
	case "three-hours":
		return 3 * time.Hour
	case "one-day":
		return 24 * time.Hour
	case "one-week":
		return 7 * 24 * time.Hour
	default:
		return 5 * time.Minute
	}
}

// networkPerfLatencyMs returns a stable round-trip latency (ms) for a region
// pair: 0 when same region, otherwise a deterministic value from the string
// distance between the two region names. Real-shaped, deterministic, honest for
// the simulated topology.
func networkPerfLatencyMs(src, dst string) float64 {
	if src == dst {
		return 0.5
	}
	sum := 0
	for i := 0; i < len(src) || i < len(dst); i++ {
		var a, b byte
		if i < len(src) {
			a = src[i]
		}
		if i < len(dst) {
			b = dst[i]
		}
		if a > b {
			sum += int(a - b)
		} else {
			sum += int(b - a)
		}
	}
	return 10 + float64(sum%140)
}

// parseEC2Time parses an EC2 query timestamp (RFC3339), falling back to def.
func parseEC2Time(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", s); err == nil {
		return t
	}
	return def
}

// ec2NumberedList collects a 1-based numbered query list (prefix.1, prefix.2…).
func ec2NumberedList(r *http.Request, prefix string) []string {
	var out []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.%d", prefix, i))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}
