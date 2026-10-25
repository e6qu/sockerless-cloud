package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleCreateRouteTable(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	tags := parseTags(r)
	id := ec2ID("rtb")

	// The local route's CIDR is the VPC's own CIDR; a missing VPC is a real
	// InvalidVpcID.NotFound, not a fabricated default block.
	v, ok := ec2Vpcs.Get(vpcId)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcId), http.StatusBadRequest)
		return
	}
	localCidr := v.CidrBlock

	rt := EC2RouteTable{
		RouteTableId: id,
		VpcId:        vpcId,
		Routes: []EC2Route{{
			DestinationCidrBlock: localCidr,
			GatewayId:            "local",
			State:                "active",
			Origin:               "CreateRouteTable",
		}},
		Tags:    tags,
		OwnerId: ec2Owner(),
	}
	ec2RouteTables.Put(id, rt)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateRouteTableResponse %s>
  <requestId>%s</requestId>
  <routeTable>
    <routeTableId>%s</routeTableId><vpcId>%s</vpcId>
    %s
    <associationSet/>
    %s
  </routeTable>
</CreateRouteTableResponse>`, ec2Xmlns(), sim.NewUUID(), id, vpcId, routeSetXML(rt.Routes), writeTagSetXML(tags))
}

func routeSetXML(routes []EC2Route) string {
	var b strings.Builder
	b.WriteString("<routeSet>")
	for _, route := range routes {
		b.WriteString("<item>")
		if route.DestinationCidrBlock != "" {
			fmt.Fprintf(&b, "<destinationCidrBlock>%s</destinationCidrBlock>", route.DestinationCidrBlock)
		}
		if route.DestinationIpv6CidrBlock != "" {
			fmt.Fprintf(&b, "<destinationIpv6CidrBlock>%s</destinationIpv6CidrBlock>", route.DestinationIpv6CidrBlock)
		}
		if route.DestinationPrefixListId != "" {
			fmt.Fprintf(&b, "<destinationPrefixListId>%s</destinationPrefixListId>", route.DestinationPrefixListId)
		}
		for _, tv := range []struct{ tag, val string }{
			{"gatewayId", route.GatewayId},
			{"natGatewayId", route.NatGatewayId},
			{"networkInterfaceId", route.NetworkInterfaceId},
			{"instanceId", route.InstanceId},
			{"vpcPeeringConnectionId", route.VpcPeeringConnectionId},
			{"transitGatewayId", route.TransitGatewayId},
			{"egressOnlyInternetGatewayId", route.EgressOnlyInternetGatewayId},
			{"localGatewayId", route.LocalGatewayId},
			{"carrierGatewayId", route.CarrierGatewayId},
			{"vpcEndpointId", route.VpcEndpointId},
			{"coreNetworkArn", route.CoreNetworkArn},
		} {
			if tv.val != "" {
				fmt.Fprintf(&b, "<%s>%s</%s>", tv.tag, tv.val, tv.tag)
			}
		}
		fmt.Fprintf(&b, "<state>%s</state><origin>%s</origin>", route.State, route.Origin)
		b.WriteString("</item>")
	}
	b.WriteString("</routeSet>")
	return b.String()
}

func assocSetXML(rtId string, assocs []EC2RouteTableAssociation) string {
	var filtered []EC2RouteTableAssociation
	for _, a := range assocs {
		if a.RouteTableId == rtId {
			filtered = append(filtered, a)
		}
	}
	if len(filtered) == 0 {
		return "<associationSet/>"
	}
	var b strings.Builder
	b.WriteString("<associationSet>")
	for _, a := range filtered {
		b.WriteString("<item>")
		fmt.Fprintf(&b, "<routeTableAssociationId>%s</routeTableAssociationId><routeTableId>%s</routeTableId>", a.AssociationId, a.RouteTableId)
		if a.SubnetId != "" {
			fmt.Fprintf(&b, "<subnetId>%s</subnetId>", a.SubnetId)
		}
		if a.GatewayId != "" {
			fmt.Fprintf(&b, "<gatewayId>%s</gatewayId>", a.GatewayId)
		}
		fmt.Fprintf(&b, "<main>%t</main><associationState><state>associated</state></associationState>", a.Main)
		b.WriteString("</item>")
	}
	b.WriteString("</associationSet>")
	return b.String()
}

func rtItemXML(rt EC2RouteTable) string {
	return fmt.Sprintf(`<item>
    <routeTableId>%s</routeTableId><vpcId>%s</vpcId>
    %s
    %s
    <ownerId>%s</ownerId>
    %s
  </item>`, rt.RouteTableId, rt.VpcId, routeSetXML(rt.Routes), assocSetXML(rt.RouteTableId, rt.Associations), rt.OwnerId, writeTagSetXML(rt.Tags))
}

func ec2RouteTableMatchesFilters(rt EC2RouteTable, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-id":
			if !ec2StrInValues(rt.VpcId, vals) {
				return false
			}
		case "route-table-id":
			if !ec2StrInValues(rt.RouteTableId, vals) {
				return false
			}
		case "association.route-table-association-id":
			if !ec2RouteTableHasAssoc(rt, func(a EC2RouteTableAssociation) bool {
				return ec2StrInValues(a.AssociationId, vals)
			}) {
				return false
			}
		case "association.subnet-id":
			if !ec2RouteTableHasAssoc(rt, func(a EC2RouteTableAssociation) bool {
				return ec2StrInValues(a.SubnetId, vals)
			}) {
				return false
			}
		case "association.main":
			want := ec2StrInValues("true", vals)
			if !ec2RouteTableHasAssoc(rt, func(a EC2RouteTableAssociation) bool { return a.Main == want }) {
				return false
			}
		case "owner-id":
			if !ec2StrInValues(rt.OwnerId, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, rt.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func ec2RouteTableHasAssoc(rt EC2RouteTable, pred func(EC2RouteTableAssociation) bool) bool {
	for _, a := range rt.Associations {
		if pred(a) {
			return true
		}
	}
	return false
}

func handleDescribeRouteTables(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "RouteTableId")
	filters := ec2Filters(r)

	var rts []EC2RouteTable
	for _, rt := range ec2RouteTables.List() {
		if len(ids) > 0 && !ec2StrInValues(rt.RouteTableId, ids) {
			continue
		}
		if !ec2RouteTableMatchesFilters(rt, filters) {
			continue
		}
		rts = append(rts, rt)
	}

	var items strings.Builder
	for _, rt := range rts {
		items.WriteString(rtItemXML(rt))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeRouteTablesResponse %s>
  <requestId>%s</requestId>
  <routeTableSet>%s</routeTableSet>
</DescribeRouteTablesResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func handleDeleteRouteTable(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteTableId")
	rt, ok := ec2RouteTables.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", fmt.Sprintf("The routeTable ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// EC2 refuses to delete the main route table or one still associated with a
	// subnet or gateway.
	if len(rt.Associations) > 0 {
		ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("The routeTable '%s' has dependencies and cannot be deleted.", id), http.StatusBadRequest)
		return
	}
	ec2RouteTables.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteRouteTableResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteRouteTableResponse>`, ec2Xmlns(), sim.NewUUID())
}

// parseRouteFromRequest builds an EC2Route from a CreateRoute/ReplaceRoute
// request, covering every target type the EC2 API accepts (not just
// gateway/nat/eni — peering, TGW, prefix-list, IPv6, egress-only, etc.).
func parseRouteFromRequest(r *http.Request) EC2Route {
	return EC2Route{
		DestinationCidrBlock:        r.FormValue("DestinationCidrBlock"),
		DestinationIpv6CidrBlock:    r.FormValue("DestinationIpv6CidrBlock"),
		DestinationPrefixListId:     r.FormValue("DestinationPrefixListId"),
		GatewayId:                   r.FormValue("GatewayId"),
		NatGatewayId:                r.FormValue("NatGatewayId"),
		NetworkInterfaceId:          r.FormValue("NetworkInterfaceId"),
		InstanceId:                  r.FormValue("InstanceId"),
		VpcPeeringConnectionId:      r.FormValue("VpcPeeringConnectionId"),
		TransitGatewayId:            r.FormValue("TransitGatewayId"),
		EgressOnlyInternetGatewayId: r.FormValue("EgressOnlyInternetGatewayId"),
		LocalGatewayId:              r.FormValue("LocalGatewayId"),
		CarrierGatewayId:            r.FormValue("CarrierGatewayId"),
		VpcEndpointId:               r.FormValue("VpcEndpointId"),
		CoreNetworkArn:              r.FormValue("CoreNetworkArn"),
		State:                       "active",
		Origin:                      "CreateRoute",
	}
}

// routeDestMatches reports whether a stored route has the destination the
// request addresses (IPv4 CIDR, IPv6 CIDR, or prefix list).
func routeDestMatches(route EC2Route, cidr, ipv6, prefix string) bool {
	switch {
	case cidr != "":
		return route.DestinationCidrBlock == cidr
	case ipv6 != "":
		return route.DestinationIpv6CidrBlock == ipv6
	case prefix != "":
		return route.DestinationPrefixListId == prefix
	}
	return false
}

func handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	rtId := r.FormValue("RouteTableId")
	route := parseRouteFromRequest(r)

	ec2RouteTables.Update(rtId, func(rt *EC2RouteTable) {
		rt.Routes = append(rt.Routes, route)
	})
	ec2ReapplyRoutes(ec2RouteTableVPC(rtId))

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateRouteResponse %s>
  <requestId>%s</requestId><return>true</return>
</CreateRouteResponse>`, ec2Xmlns(), sim.NewUUID())
}

// handleReplaceRoute updates the target of an existing route in place (the
// terraform-provider-aws path for any aws_route target change that keeps the
// same destination).
func handleReplaceRoute(w http.ResponseWriter, r *http.Request) {
	rtId := r.FormValue("RouteTableId")
	newRoute := parseRouteFromRequest(r)
	cidr, ipv6, prefix := newRoute.DestinationCidrBlock, newRoute.DestinationIpv6CidrBlock, newRoute.DestinationPrefixListId

	rt, ok := ec2RouteTables.Get(rtId)
	if !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", fmt.Sprintf("The route table ID '%s' does not exist", rtId), http.StatusBadRequest)
		return
	}
	found := false
	for _, route := range rt.Routes {
		if routeDestMatches(route, cidr, ipv6, prefix) {
			found = true
			break
		}
	}
	if !found {
		ec2ErrorXML(w, "InvalidRoute.NotFound", "no route with the specified destination exists", http.StatusBadRequest)
		return
	}
	ec2RouteTables.Update(rtId, func(rt *EC2RouteTable) {
		for i := range rt.Routes {
			if routeDestMatches(rt.Routes[i], cidr, ipv6, prefix) {
				rt.Routes[i] = newRoute
				return
			}
		}
	})
	ec2ReapplyRoutes(rt.VpcId)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceRouteResponse %s>
  <requestId>%s</requestId><return>true</return>
</ReplaceRouteResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	rtId := r.FormValue("RouteTableId")
	destCidr := r.FormValue("DestinationCidrBlock")
	destIpv6 := r.FormValue("DestinationIpv6CidrBlock")
	destPrefix := r.FormValue("DestinationPrefixListId")

	ec2RouteTables.Update(rtId, func(rt *EC2RouteTable) {
		var filtered []EC2Route
		for _, route := range rt.Routes {
			if routeDestMatches(route, destCidr, destIpv6, destPrefix) {
				continue
			}
			filtered = append(filtered, route)
		}
		rt.Routes = filtered
	})
	ec2ReapplyRoutes(ec2RouteTableVPC(rtId))

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteRouteResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteRouteResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleAssociateRouteTable(w http.ResponseWriter, r *http.Request) {
	rtId := r.FormValue("RouteTableId")
	subnetId := r.FormValue("SubnetId")
	assocId := ec2ID("rtbassoc")

	ec2RouteTables.Update(rtId, func(rt *EC2RouteTable) {
		rt.Associations = append(rt.Associations, EC2RouteTableAssociation{
			AssociationId: assocId,
			RouteTableId:  rtId,
			SubnetId:      subnetId,
			Main:          false,
		})
	})
	ec2ReapplyRoutes(ec2RouteTableVPC(rtId))

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateRouteTableResponse %s>
  <requestId>%s</requestId>
  <associationId>%s</associationId>
</AssociateRouteTableResponse>`, ec2Xmlns(), sim.NewUUID(), assocId)
}

func handleDisassociateRouteTable(w http.ResponseWriter, r *http.Request) {
	assocId := r.FormValue("AssociationId")

	// Find and remove association from its route table
	for _, rt := range ec2RouteTables.List() {
		for _, a := range rt.Associations {
			if a.AssociationId == assocId {
				ec2RouteTables.Update(rt.RouteTableId, func(rt *EC2RouteTable) {
					var filtered []EC2RouteTableAssociation
					for _, a := range rt.Associations {
						if a.AssociationId != assocId {
							filtered = append(filtered, a)
						}
					}
					rt.Associations = filtered
				})
				ec2ReapplyRoutes(rt.VpcId)
				break
			}
		}
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateRouteTableResponse %s>
  <requestId>%s</requestId><return>true</return>
</DisassociateRouteTableResponse>`, ec2Xmlns(), sim.NewUUID())
}
