package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// EC2VpcEndpoint models an AWS VPC endpoint (gateway, interface, or
// gatewayLoadBalancer). Gateway endpoints are synchronously available.
// Interface endpoints for a locally registered PrivateLink service retain the
// provider-side pending-acceptance lifecycle in both endpoint views.
type EC2VpcEndpoint struct {
	VpcEndpointId         string
	VpcEndpointType       string
	VpcId                 string
	ServiceName           string
	State                 string
	PolicyDocument        string
	RouteTableIds         []string
	SubnetIds             []string
	SecurityGroupIds      []string
	PrivateDnsEnabled     bool
	RequesterManaged      bool
	IpAddressType         string
	NetworkInterfaceIds   []string
	DnsEntries            []EC2DnsEntry
	OwnerId               string
	CreationTimestamp     string
	PayerResponsibilities []EC2PayerResponsibilityEntry
	Tags                  []EC2Tag
}

// EC2PayerResponsibilityEntry records which account pays one supported class
// of VPC endpoint charges.
type EC2PayerResponsibilityEntry struct {
	Scope                   string
	PayerResponsibilityType string
}

// EC2DnsEntry is a (dnsName, hostedZoneId) pair an interface endpoint advertises.
type EC2DnsEntry struct {
	DnsName      string
	HostedZoneId string
}

func handleCreateVpcEndpoint(w http.ResponseWriter, r *http.Request) {
	serviceName := r.FormValue("ServiceName")
	if serviceName == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ServiceName", http.StatusBadRequest)
		return
	}
	vpcId := r.FormValue("VpcId")
	if vpcId == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpcId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcId); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID %q does not exist", vpcId), http.StatusBadRequest)
		return
	}

	endpointType := r.FormValue("VpcEndpointType")
	if endpointType == "" {
		// Real AWS defaults VpcEndpointType to Gateway when omitted.
		endpointType = "Gateway"
	}

	id := ec2ID("vpce")
	subnetIds := ec2ParamList(r, "SubnetId")
	routeTableIds := ec2ParamList(r, "RouteTableId")
	sgIds := ec2ParamList(r, "SecurityGroupId")
	var endpointService *EC2VpcEndpointServiceConfiguration
	for _, cfg := range ec2VpcEndpointServices.List() {
		if cfg.ServiceName == serviceName {
			matched := cfg
			endpointService = &matched
			break
		}
	}
	state := "Available"
	if endpointService != nil && endpointService.AcceptanceRequired {
		state = "PendingAcceptance"
	}

	ep := EC2VpcEndpoint{
		VpcEndpointId:     id,
		VpcEndpointType:   endpointType,
		VpcId:             vpcId,
		ServiceName:       serviceName,
		State:             state,
		PolicyDocument:    r.FormValue("PolicyDocument"),
		RouteTableIds:     routeTableIds,
		SubnetIds:         subnetIds,
		SecurityGroupIds:  sgIds,
		PrivateDnsEnabled: r.FormValue("PrivateDnsEnabled") == "true",
		RequesterManaged:  false,
		IpAddressType:     r.FormValue("IpAddressType"),
		OwnerId:           ec2Owner(),
		CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
		PayerResponsibilities: []EC2PayerResponsibilityEntry{{
			Scope:                   "vpc-endpoint-charges",
			PayerResponsibilityType: "vpc-endpoint-account",
		}},
		Tags: parseTags(r),
	}

	// Interface endpoints get one ENI per subnet plus a DNS entry; gateway
	// endpoints get neither (they're programmed into route tables). This mirrors
	// the shapes the SDK and aws_vpc_endpoint provider read back.
	if endpointType == "Interface" || endpointType == "GatewayLoadBalancer" {
		for range subnetIds {
			ep.NetworkInterfaceIds = append(ep.NetworkInterfaceIds, ec2ID("eni"))
		}
		ep.DnsEntries = []EC2DnsEntry{{
			DnsName:      fmt.Sprintf("%s-%s.%s.vpce.amazonaws.com", id, generateUUID()[:8], serviceName),
			HostedZoneId: "Z" + strings.ToUpper(generateUUID()[:13]),
		}}
	}

	ec2VpcEndpoints.Put(id, ep)
	if endpointService != nil {
		connectionID := ec2ID("vpce-conn")
		ec2VpcEndpointConns.Put(connectionID, EC2VpcEndpointConnection{
			VpcEndpointConnectionId: connectionID,
			ServiceId:               endpointService.ServiceId,
			VpcEndpointId:           id,
			VpcEndpointOwner:        ec2Owner(),
			VpcEndpointState:        state,
			IpAddressType:           ep.IpAddressType,
			VpcEndpointRegion:       awsRegion(),
			CreationTimestamp:       ep.CreationTimestamp,
			PayerResponsibilities:   append([]EC2PayerResponsibilityEntry(nil), ep.PayerResponsibilities...),
			Tags:                    append([]EC2Tag(nil), ep.Tags...),
		})
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpcEndpointResponse %s>
  <requestId>%s</requestId>
  <vpcEndpoint>%s</vpcEndpoint>
</CreateVpcEndpointResponse>`, ec2Xmlns(), generateUUID(), vpcEndpointFieldsXML(ep))
}

func handleDescribeVpcEndpoints(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpcEndpointId")
	var eps []EC2VpcEndpoint
	if len(ids) > 0 {
		for _, id := range ids {
			ep, ok := ec2VpcEndpoints.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidVpcEndpointId.NotFound", fmt.Sprintf("The Vpc Endpoint Id %q does not exist", id), http.StatusBadRequest)
				return
			}
			eps = append(eps, ep)
		}
	} else {
		filters := ec2Filters(r)
		for _, ep := range ec2VpcEndpoints.List() {
			if ec2VpcEndpointMatchesFilters(ep, filters) {
				eps = append(eps, ep)
			}
		}
	}

	var items strings.Builder
	for _, ep := range eps {
		items.WriteString("<item>" + vpcEndpointFieldsXML(ep) + "</item>")
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcEndpointsResponse %s>
  <requestId>%s</requestId>
  <vpcEndpointSet>%s</vpcEndpointSet>
</DescribeVpcEndpointsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteVpcEndpoints(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpcEndpointId")
	var unsuccessful strings.Builder
	for _, id := range ids {
		if _, ok := ec2VpcEndpoints.Get(id); !ok {
			// DeleteVpcEndpoints never errors at the top level for an unknown id;
			// it returns the failure in the unsuccessful set, mirroring real AWS.
			fmt.Fprintf(&unsuccessful, `<item><resourceId>%s</resourceId><error><code>InvalidVpcEndpointId.NotFound</code><message>The Vpc Endpoint Id %s does not exist</message></error></item>`, id, id)
			continue
		}
		ec2VpcEndpoints.Delete(id)
		for _, connection := range ec2VpcEndpointConns.List() {
			if connection.VpcEndpointId == id {
				ec2VpcEndpointConns.Delete(connection.VpcEndpointConnectionId)
			}
		}
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpcEndpointsResponse %s>
  <requestId>%s</requestId>
  <unsuccessful>%s</unsuccessful>
</DeleteVpcEndpointsResponse>`, ec2Xmlns(), generateUUID(), unsuccessful.String())
}

// vpcEndpointFieldsXML renders the VpcEndpoint shape shared by
// CreateVpcEndpoint and DescribeVpcEndpoints, matching the ec2Query xmlNames
// in ec2.smithy.json#VpcEndpoint.
func vpcEndpointFieldsXML(ep EC2VpcEndpoint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<vpcEndpointId>%s</vpcEndpointId>", ep.VpcEndpointId)
	fmt.Fprintf(&b, "<vpcEndpointType>%s</vpcEndpointType>", ep.VpcEndpointType)
	fmt.Fprintf(&b, "<vpcId>%s</vpcId>", ep.VpcId)
	fmt.Fprintf(&b, "<serviceName>%s</serviceName>", ep.ServiceName)
	fmt.Fprintf(&b, "<state>%s</state>", ep.State)
	if ep.PolicyDocument != "" {
		fmt.Fprintf(&b, "<policyDocument>%s</policyDocument>", vpceXMLEscape(ep.PolicyDocument))
	}
	b.WriteString(vpceIDSetXML("routeTableIdSet", ep.RouteTableIds))
	b.WriteString(vpceIDSetXML("subnetIdSet", ep.SubnetIds))
	b.WriteString(vpceGroupSetXML(ep.SecurityGroupIds))
	if ep.IpAddressType != "" {
		fmt.Fprintf(&b, "<ipAddressType>%s</ipAddressType>", ep.IpAddressType)
	}
	fmt.Fprintf(&b, "<privateDnsEnabled>%t</privateDnsEnabled>", ep.PrivateDnsEnabled)
	fmt.Fprintf(&b, "<requesterManaged>%t</requesterManaged>", ep.RequesterManaged)
	b.WriteString(vpceIDSetXML("networkInterfaceIdSet", ep.NetworkInterfaceIds))
	b.WriteString(vpceDnsEntrySetXML(ep.DnsEntries))
	fmt.Fprintf(&b, "<creationTimestamp>%s</creationTimestamp>", ep.CreationTimestamp)
	b.WriteString(vpcePayerResponsibilitiesXML(ep.PayerResponsibilities))
	b.WriteString(writeTagSetXML(ep.Tags))
	fmt.Fprintf(&b, "<ownerId>%s</ownerId>", ep.OwnerId)
	return b.String()
}

func vpcePayerResponsibilitiesXML(entries []EC2PayerResponsibilityEntry) string {
	var b strings.Builder
	b.WriteString("<payerResponsibilitySet>")
	for _, entry := range entries {
		fmt.Fprintf(&b, "<item><scope>%s</scope><payerResponsibilityType>%s</payerResponsibilityType></item>",
			entry.Scope, entry.PayerResponsibilityType)
	}
	b.WriteString("</payerResponsibilitySet>")
	return b.String()
}

func vpceIDSetXML(elem string, ids []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", elem)
	for _, id := range ids {
		fmt.Fprintf(&b, "<item>%s</item>", id)
	}
	fmt.Fprintf(&b, "</%s>", elem)
	return b.String()
}

func vpceGroupSetXML(sgIds []string) string {
	var b strings.Builder
	b.WriteString("<groupSet>")
	for _, id := range sgIds {
		name := id
		if sg, ok := ec2SecurityGroups.Get(id); ok {
			name = sg.GroupName
		}
		fmt.Fprintf(&b, "<item><groupId>%s</groupId><groupName>%s</groupName></item>", id, name)
	}
	b.WriteString("</groupSet>")
	return b.String()
}

func vpceDnsEntrySetXML(entries []EC2DnsEntry) string {
	var b strings.Builder
	b.WriteString("<dnsEntrySet>")
	for _, e := range entries {
		fmt.Fprintf(&b, "<item><dnsName>%s</dnsName><hostedZoneId>%s</hostedZoneId></item>", e.DnsName, e.HostedZoneId)
	}
	b.WriteString("</dnsEntrySet>")
	return b.String()
}

func ec2VpcEndpointMatchesFilters(ep EC2VpcEndpoint, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-endpoint-id":
			if !ec2StrInValues(ep.VpcEndpointId, vals) {
				return false
			}
		case "vpc-id":
			if !ec2StrInValues(ep.VpcId, vals) {
				return false
			}
		case "service-name":
			if !ec2StrInValues(ep.ServiceName, vals) {
				return false
			}
		case "vpc-endpoint-state":
			if !ec2StrInValues(ep.State, vals) {
				return false
			}
		case "vpc-endpoint-type":
			if !ec2StrInValues(ep.VpcEndpointType, vals) {
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

// vpceXMLEscape escapes a free-form policy document for safe inclusion in the
// XML response body (the only VPC-endpoint field that can carry XML-special
// characters).
func vpceXMLEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
