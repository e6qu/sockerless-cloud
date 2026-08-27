package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements five EC2 (ec2Query) control-plane families that front the
// VPC endpoint-service / CIDR / secondary-network / encryption stack:
//
//   - VPC endpoint services (vpce-svc-…): a service configuration that fronts a
//     network/gateway load balancer, with an allowed-principal permission set
//     and a private-DNS verification state.
//   - VPC/Subnet CIDR-block associations: a secondary CIDR added to an existing
//     VPC/subnet, with an association id and an "associated" state.
//   - Subnet CIDR reservations (subnet-cidr-res-…).
//   - Secondary networks (sn-…) / secondary subnets (sns-…).
//   - Security-group ↔ VPC associations (state "associated").
//   - VPC encryption controls (vpce-…), one per VPC, with a mode + per-resource
//     exclusion state.
//
// All element/list names match the EC2 smithy model (ec2.smithy.json) xmlName
// traits exactly — the runtime spec-shape validator rejects any divergence. The
// VPC/subnet/security-group stores are the existing ec2.go ones; these ops add
// the secondary metadata around them.

// EC2VpcEndpointServiceConfiguration models a VpcEndpointServiceConfiguration:
// the provider side of a PrivateLink service. ServiceName is the
// com.amazonaws.vpce.<region>.<id> DNS name consumers target.
type EC2VpcEndpointServiceConfiguration struct {
	ServiceId               string
	ServiceName             string
	ServiceState            string
	AcceptanceRequired      bool
	ManagesVpcEndpoints     bool
	NetworkLoadBalancerArns []string
	GatewayLoadBalancerArns []string
	SupportedIpAddressTypes []string
	BaseEndpointDnsNames    []string
	PrivateDnsName          string
	PrivateDnsNameState     string
	PayerResponsibility     string
	Tags                    []EC2Tag
	// AllowedPrincipals is the permission set ModifyVpcEndpointServicePermissions
	// edits and DescribeVpcEndpointServicePermissions reads back.
	AllowedPrincipals []EC2AllowedPrincipal
}

// EC2AllowedPrincipal is one entry of a service's allowed-principal set.
type EC2AllowedPrincipal struct {
	PrincipalType       string
	Principal           string
	ServicePermissionId string
	ServiceId           string
}

// EC2VpcCidrAssoc records a secondary IPv4 CIDR association on a VPC.
type EC2VpcCidrAssoc struct {
	AssociationId string
	VpcId         string
	CidrBlock     string
	State         string
}

type EC2SubnetCidrReservation struct {
	SubnetCidrReservationId string
	SubnetId                string
	Cidr                    string
	ReservationType         string
	OwnerId                 string
	Description             string
	Ipv6                    bool
	Tags                    []EC2Tag
}

// EC2SecondaryNetwork models a secondary network (an RDMA-class network fabric).
type EC2SecondaryNetwork struct {
	SecondaryNetworkId string
	OwnerId            string
	Type               string
	State              string
	Ipv4CidrBlocks     []EC2SecondaryCidrAssoc
	Tags               []EC2Tag
}

// EC2SecondarySubnet models a secondary subnet within a secondary network.
type EC2SecondarySubnet struct {
	SecondarySubnetId    string
	SecondaryNetworkId   string
	SecondaryNetworkType string
	OwnerId              string
	AvailabilityZone     string
	AvailabilityZoneId   string
	Ipv4CidrBlocks       []EC2SecondaryCidrAssoc
	State                string
	Tags                 []EC2Tag
}

// EC2SecondaryCidrAssoc is one CIDR-block association of a secondary
// network/subnet.
type EC2SecondaryCidrAssoc struct {
	AssociationId string
	CidrBlock     string
	State         string
}

type EC2SecurityGroupVpcAssociation struct {
	GroupId      string
	VpcId        string
	VpcOwnerId   string
	State        string
	GroupOwnerId string
}

// EC2VpcEncryptionControl models a VPC encryption control: a per-VPC enforce/
// monitor mode plus per-resource-type exclusion state.
type EC2VpcEncryptionControl struct {
	VpcEncryptionControlId string
	VpcId                  string
	Mode                   string
	State                  string
	Exclusions             map[string]string // resource xmlName -> exclusion state
	Tags                   []EC2Tag
}

// EC2AccountVpcEncryptionControl is the Region-scoped account policy that
// drives VPC Encryption Controls for every VPC in the account.
type EC2AccountVpcEncryptionControl struct {
	State               string
	Mode                string
	Exclusions          map[string]string
	ManagedBy           string
	LastUpdateTimestamp string
}

// State stores for the families above.
var (
	ec2VpcEndpointServices          sim.Store[EC2VpcEndpointServiceConfiguration]
	ec2VpcCidrAssocs                sim.Store[EC2VpcCidrAssoc]
	ec2SubnetCidrReservations       sim.Store[EC2SubnetCidrReservation]
	ec2SecondaryNetworks            sim.Store[EC2SecondaryNetwork]
	ec2SecondarySubnets             sim.Store[EC2SecondarySubnet]
	ec2SgVpcAssociations            sim.Store[EC2SecurityGroupVpcAssociation]
	ec2VpcEncryptionControls        sim.Store[EC2VpcEncryptionControl]
	ec2AccountVpcEncryptionControls sim.Store[EC2AccountVpcEncryptionControl]
)

// registerEC2VpcEndpointSvc registers this EC2 sub-service's ec2Query actions.
func registerEC2VpcEndpointSvc(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2VpcEndpointServices = sim.MakeStore[EC2VpcEndpointServiceConfiguration](srv.DB(), "ec2_vpc_endpoint_services")
	ec2VpcCidrAssocs = sim.MakeStore[EC2VpcCidrAssoc](srv.DB(), "ec2_vpc_cidr_assocs")
	ec2SubnetCidrReservations = sim.MakeStore[EC2SubnetCidrReservation](srv.DB(), "ec2_subnet_cidr_reservations")
	ec2SecondaryNetworks = sim.MakeStore[EC2SecondaryNetwork](srv.DB(), "ec2_secondary_networks")
	ec2SecondarySubnets = sim.MakeStore[EC2SecondarySubnet](srv.DB(), "ec2_secondary_subnets")
	ec2SgVpcAssociations = sim.MakeStore[EC2SecurityGroupVpcAssociation](srv.DB(), "ec2_sg_vpc_associations")
	ec2VpcEncryptionControls = sim.MakeStore[EC2VpcEncryptionControl](srv.DB(), "ec2_vpc_encryption_controls")
	ec2AccountVpcEncryptionControls = sim.MakeStore[EC2AccountVpcEncryptionControl](srv.DB(), "ec2_account_vpc_encryption_controls")

	// VPC endpoint services
	r.Register("CreateVpcEndpointServiceConfiguration", handleCreateVpcEndpointServiceConfiguration)
	r.Register("DescribeVpcEndpointServiceConfigurations", handleDescribeVpcEndpointServiceConfigurations)
	r.Register("ModifyVpcEndpointServiceConfiguration", handleModifyVpcEndpointServiceConfiguration)
	r.Register("DeleteVpcEndpointServiceConfigurations", handleDeleteVpcEndpointServiceConfigurations)
	r.Register("DescribeVpcEndpointServicePermissions", handleDescribeVpcEndpointServicePermissions)
	r.Register("ModifyVpcEndpointServicePermissions", handleModifyVpcEndpointServicePermissions)
	r.Register("ModifyVpcEndpointServicePayerResponsibility", handleModifyVpcEndpointServicePayerResponsibility)
	r.Register("StartVpcEndpointServicePrivateDnsVerification", handleStartVpcEndpointServicePrivateDnsVerification)
	r.Register("DescribeVpcEndpointServices", handleDescribeVpcEndpointServices)
	r.Register("ModifyVpcEndpointPayerResponsibility", handleModifyVpcEndpointPayerResponsibility)

	// VPC/Subnet CIDR
	r.Register("AssociateVpcCidrBlock", handleAssociateVpcCidrBlock)
	r.Register("DisassociateVpcCidrBlock", handleDisassociateVpcCidrBlock)
	r.Register("AssociateSubnetCidrBlock", handleAssociateSubnetCidrBlock)
	r.Register("DisassociateSubnetCidrBlock", handleDisassociateSubnetCidrBlock)
	r.Register("CreateSubnetCidrReservation", handleCreateSubnetCidrReservation)
	r.Register("DeleteSubnetCidrReservation", handleDeleteSubnetCidrReservation)
	r.Register("GetSubnetCidrReservations", handleGetSubnetCidrReservations)

	// Secondary networks/subnets
	r.Register("CreateSecondaryNetwork", handleCreateSecondaryNetwork)
	r.Register("DeleteSecondaryNetwork", handleDeleteSecondaryNetwork)
	r.Register("DescribeSecondaryNetworks", handleDescribeSecondaryNetworks)
	r.Register("CreateSecondarySubnet", handleCreateSecondarySubnet)
	r.Register("DeleteSecondarySubnet", handleDeleteSecondarySubnet)
	r.Register("DescribeSecondarySubnets", handleDescribeSecondarySubnets)

	// Security-group VPC associations
	r.Register("AssociateSecurityGroupVpc", handleAssociateSecurityGroupVpc)
	r.Register("DisassociateSecurityGroupVpc", handleDisassociateSecurityGroupVpc)
	r.Register("DescribeSecurityGroupVpcAssociations", handleDescribeSecurityGroupVpcAssociations)

	// VPC encryption control
	r.Register("CreateVpcEncryptionControl", handleCreateVpcEncryptionControl)
	r.Register("DeleteVpcEncryptionControl", handleDeleteVpcEncryptionControl)
	r.Register("ModifyVpcEncryptionControl", handleModifyVpcEncryptionControl)
	r.Register("DescribeVpcEncryptionControls", handleDescribeVpcEncryptionControls)
	r.Register("DescribeAccountVpcEncryptionControl", handleDescribeAccountVpcEncryptionControl)
	r.Register("ModifyAccountVpcEncryptionControl", handleModifyAccountVpcEncryptionControl)
}

func handleCreateVpcEndpointServiceConfiguration(w http.ResponseWriter, r *http.Request) {
	nlbArns := ec2ParamList(r, "NetworkLoadBalancerArn")
	gwlbArns := ec2ParamList(r, "GatewayLoadBalancerArn")
	if len(nlbArns) == 0 && len(gwlbArns) == 0 {
		ec2ErrorXML(w, "InvalidParameter", "A service configuration must front at least one Network or Gateway Load Balancer", http.StatusBadRequest)
		return
	}
	ipTypes := ec2ParamList(r, "SupportedIpAddressType")
	if len(ipTypes) == 0 {
		ipTypes = []string{"ipv4"}
	}
	id := ec2ID("vpce-svc")
	// The consumer-facing service name AWS assigns: com.amazonaws.vpce.<region>.<id>.
	serviceName := fmt.Sprintf("com.amazonaws.vpce.%s.%s", awsRegion(), id)
	privateDns := r.FormValue("PrivateDnsName")
	cfg := EC2VpcEndpointServiceConfiguration{
		ServiceId:               id,
		ServiceName:             serviceName,
		ServiceState:            "Available",
		AcceptanceRequired:      r.FormValue("AcceptanceRequired") != "false",
		ManagesVpcEndpoints:     false,
		NetworkLoadBalancerArns: nlbArns,
		GatewayLoadBalancerArns: gwlbArns,
		SupportedIpAddressTypes: ipTypes,
		BaseEndpointDnsNames:    []string{fmt.Sprintf("%s.%s.vpce.amazonaws.com", id, awsRegion())},
		PrivateDnsName:          privateDns,
		PayerResponsibility:     "ServiceOwner",
		Tags:                    parseTags(r),
	}
	if privateDns != "" {
		cfg.PrivateDnsNameState = "pendingVerification"
	}
	ec2VpcEndpointServices.Put(id, cfg)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpcEndpointServiceConfigurationResponse %s>
  <requestId>%s</requestId>
  <serviceConfiguration>%s</serviceConfiguration>
  <clientToken>%s</clientToken>
</CreateVpcEndpointServiceConfigurationResponse>`, ec2Xmlns(), generateUUID(), serviceConfigurationXML(cfg), r.FormValue("ClientToken"))
}

func handleDescribeVpcEndpointServiceConfigurations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ServiceId")
	var cfgs []EC2VpcEndpointServiceConfiguration
	if len(ids) > 0 {
		for _, id := range ids {
			cfg, ok := ec2VpcEndpointServices.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidVpcEndpointServiceId.NotFound", fmt.Sprintf("The VpcEndpointService Id %q does not exist", id), http.StatusBadRequest)
				return
			}
			cfgs = append(cfgs, cfg)
		}
	} else {
		cfgs = ec2VpcEndpointServices.List()
	}
	var items strings.Builder
	for _, cfg := range cfgs {
		items.WriteString("<item>" + serviceConfigurationXML(cfg) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcEndpointServiceConfigurationsResponse %s>
  <requestId>%s</requestId>
  <serviceConfigurationSet>%s</serviceConfigurationSet>
</DescribeVpcEndpointServiceConfigurationsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyVpcEndpointServiceConfiguration(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ServiceId")
	if _, ok := ec2VpcEndpointServices.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpcEndpointServiceId.NotFound", fmt.Sprintf("The VpcEndpointService Id %q does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VpcEndpointServices.Update(id, func(cfg *EC2VpcEndpointServiceConfiguration) {
		if v := r.FormValue("AcceptanceRequired"); v != "" {
			cfg.AcceptanceRequired = v == "true"
		}
		if r.FormValue("RemovePrivateDnsName") == "true" {
			cfg.PrivateDnsName = ""
			cfg.PrivateDnsNameState = ""
		}
		if v := r.FormValue("PrivateDnsName"); v != "" {
			cfg.PrivateDnsName = v
			cfg.PrivateDnsNameState = "pendingVerification"
		}
		cfg.NetworkLoadBalancerArns = ec2ApplyAddRemove(cfg.NetworkLoadBalancerArns, ec2ParamList(r, "AddNetworkLoadBalancerArn"), ec2ParamList(r, "RemoveNetworkLoadBalancerArn"))
		cfg.GatewayLoadBalancerArns = ec2ApplyAddRemove(cfg.GatewayLoadBalancerArns, ec2ParamList(r, "AddGatewayLoadBalancerArn"), ec2ParamList(r, "RemoveGatewayLoadBalancerArn"))
		cfg.SupportedIpAddressTypes = ec2ApplyAddRemove(cfg.SupportedIpAddressTypes, ec2ParamList(r, "AddSupportedIpAddressType"), ec2ParamList(r, "RemoveSupportedIpAddressType"))
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcEndpointServiceConfigurationResponse %s>
  <requestId>%s</requestId>
  <return>true</return>
</ModifyVpcEndpointServiceConfigurationResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteVpcEndpointServiceConfigurations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ServiceId")
	var unsuccessful strings.Builder
	for _, id := range ids {
		if _, ok := ec2VpcEndpointServices.Get(id); !ok {
			fmt.Fprintf(&unsuccessful, `<item><resourceId>%s</resourceId><error><code>InvalidVpcEndpointServiceId.NotFound</code><message>The VpcEndpointService Id %s does not exist</message></error></item>`, id, id)
			continue
		}
		ec2VpcEndpointServices.Delete(id)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpcEndpointServiceConfigurationsResponse %s>
  <requestId>%s</requestId>
  <unsuccessful>%s</unsuccessful>
</DeleteVpcEndpointServiceConfigurationsResponse>`, ec2Xmlns(), generateUUID(), unsuccessful.String())
}

func handleDescribeVpcEndpointServicePermissions(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ServiceId")
	cfg, ok := ec2VpcEndpointServices.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcEndpointServiceId.NotFound", fmt.Sprintf("The VpcEndpointService Id %q does not exist", id), http.StatusBadRequest)
		return
	}
	var principals strings.Builder
	for _, p := range cfg.AllowedPrincipals {
		fmt.Fprintf(&principals, "<item><principalType>%s</principalType><principal>%s</principal><servicePermissionId>%s</servicePermissionId><serviceId>%s</serviceId></item>",
			p.PrincipalType, p.Principal, p.ServicePermissionId, p.ServiceId)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcEndpointServicePermissionsResponse %s>
  <requestId>%s</requestId>
  <allowedPrincipals>%s</allowedPrincipals>
</DescribeVpcEndpointServicePermissionsResponse>`, ec2Xmlns(), generateUUID(), principals.String())
}

func handleModifyVpcEndpointServicePermissions(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ServiceId")
	if _, ok := ec2VpcEndpointServices.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpcEndpointServiceId.NotFound", fmt.Sprintf("The VpcEndpointService Id %q does not exist", id), http.StatusBadRequest)
		return
	}
	add := ec2ParamList(r, "AddAllowedPrincipals")
	remove := ec2ParamList(r, "RemoveAllowedPrincipals")
	var added []EC2AllowedPrincipal
	ec2VpcEndpointServices.Update(id, func(cfg *EC2VpcEndpointServiceConfiguration) {
		for _, principal := range add {
			already := false
			for _, p := range cfg.AllowedPrincipals {
				if p.Principal == principal {
					already = true
					break
				}
			}
			if already {
				continue
			}
			p := EC2AllowedPrincipal{
				PrincipalType:       ec2PrincipalType(principal),
				Principal:           principal,
				ServicePermissionId: ec2ID("vpce-perm"),
				ServiceId:           id,
			}
			cfg.AllowedPrincipals = append(cfg.AllowedPrincipals, p)
			added = append(added, p)
		}
		if len(remove) > 0 {
			kept := cfg.AllowedPrincipals[:0:0]
			for _, p := range cfg.AllowedPrincipals {
				if ec2StrInValues(p.Principal, remove) {
					continue
				}
				kept = append(kept, p)
			}
			cfg.AllowedPrincipals = kept
		}
	})
	var addedSet strings.Builder
	for _, p := range added {
		fmt.Fprintf(&addedSet, "<item><principalType>%s</principalType><principal>%s</principal><servicePermissionId>%s</servicePermissionId><serviceId>%s</serviceId></item>",
			p.PrincipalType, p.Principal, p.ServicePermissionId, p.ServiceId)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcEndpointServicePermissionsResponse %s>
  <requestId>%s</requestId>
  <addedPrincipalSet>%s</addedPrincipalSet>
  <return>true</return>
</ModifyVpcEndpointServicePermissionsResponse>`, ec2Xmlns(), generateUUID(), addedSet.String())
}

func handleModifyVpcEndpointServicePayerResponsibility(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ServiceId")
	if _, ok := ec2VpcEndpointServices.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpcEndpointServiceId.NotFound", fmt.Sprintf("The VpcEndpointService Id %q does not exist", id), http.StatusBadRequest)
		return
	}
	if pr := r.FormValue("PayerResponsibility"); pr != "" {
		ec2VpcEndpointServices.Update(id, func(cfg *EC2VpcEndpointServiceConfiguration) {
			cfg.PayerResponsibility = pr
		})
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcEndpointServicePayerResponsibilityResponse %s>
  <requestId>%s</requestId>
  <return>true</return>
</ModifyVpcEndpointServicePayerResponsibilityResponse>`, ec2Xmlns(), generateUUID())
}

func handleModifyVpcEndpointPayerResponsibility(w http.ResponseWriter, r *http.Request) {
	endpointID := r.FormValue("VpcEndpointId")
	if endpointID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpcEndpointId", http.StatusBadRequest)
		return
	}
	scope := r.FormValue("Scope")
	if scope == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Scope", http.StatusBadRequest)
		return
	}
	if scope != "vpc-endpoint-charges" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value %q at 'scope' failed to satisfy constraint", scope), http.StatusBadRequest)
		return
	}
	payer := r.FormValue("PayerResponsibility")
	if payer == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter PayerResponsibility", http.StatusBadRequest)
		return
	}
	if payer != "vpc-endpoint-account" && payer != "vpc-endpoint-service-account" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value %q at 'payerResponsibility' failed to satisfy constraint", payer), http.StatusBadRequest)
		return
	}
	ep, ok := ec2VpcEndpoints.Get(endpointID)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcEndpointId.NotFound", fmt.Sprintf("The Vpc Endpoint Id %q does not exist", endpointID), http.StatusBadRequest)
		return
	}

	serviceID := r.FormValue("ServiceId")
	var matchedConnectionID string
	var matchedConnection EC2VpcEndpointConnection
	for _, connection := range ec2VpcEndpointConns.List() {
		if connection.VpcEndpointId != endpointID {
			continue
		}
		if serviceID != "" && connection.ServiceId != serviceID {
			continue
		}
		matchedConnectionID = connection.VpcEndpointConnectionId
		matchedConnection = connection
		break
	}
	if serviceID != "" && matchedConnectionID == "" {
		ec2ErrorXML(w, "InvalidVpcEndpointId.NotFound", fmt.Sprintf("The VPC endpoint %q is not connected to service %q", endpointID, serviceID), http.StatusBadRequest)
		return
	}
	if r.FormValue("DryRun") == "true" {
		ec2ErrorXML(w, "DryRunOperation", "Request would have succeeded, but DryRun flag is set.", http.StatusPreconditionFailed)
		return
	}

	entries := []EC2PayerResponsibilityEntry{{
		Scope:                   scope,
		PayerResponsibilityType: payer,
	}}
	ep.PayerResponsibilities = entries
	ec2VpcEndpoints.Put(endpointID, ep)
	if matchedConnectionID != "" {
		matchedConnection.PayerResponsibilities = append([]EC2PayerResponsibilityEntry(nil), entries...)
		ec2VpcEndpointConns.Put(matchedConnectionID, matchedConnection)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcEndpointPayerResponsibilityResponse %s>
  <requestId>%s</requestId>
  <vpcEndpointId>%s</vpcEndpointId>
  %s
</ModifyVpcEndpointPayerResponsibilityResponse>`, ec2Xmlns(), generateUUID(), endpointID, vpcePayerResponsibilitiesXML(entries))
}

func handleStartVpcEndpointServicePrivateDnsVerification(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ServiceId")
	cfg, ok := ec2VpcEndpointServices.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcEndpointServiceId.NotFound", fmt.Sprintf("The VpcEndpointService Id %q does not exist", id), http.StatusBadRequest)
		return
	}
	if cfg.PrivateDnsName == "" {
		ec2ErrorXML(w, "InvalidParameter", "The service has no private DNS name to verify", http.StatusBadRequest)
		return
	}
	ec2VpcEndpointServices.Update(id, func(c *EC2VpcEndpointServiceConfiguration) {
		c.PrivateDnsNameState = "PendingVerification"
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<StartVpcEndpointServicePrivateDnsVerificationResponse %s>
  <requestId>%s</requestId>
  <return>true</return>
</StartVpcEndpointServicePrivateDnsVerificationResponse>`, ec2Xmlns(), generateUUID())
}

func handleDescribeVpcEndpointServices(w http.ResponseWriter, r *http.Request) {
	wanted := ec2ParamList(r, "ServiceName")
	var names []string
	var details strings.Builder
	for _, cfg := range ec2VpcEndpointServices.List() {
		if len(wanted) > 0 && !ec2StrInValues(cfg.ServiceName, wanted) {
			continue
		}
		names = append(names, cfg.ServiceName)
		details.WriteString("<item>" + serviceDetailXML(cfg) + "</item>")
	}
	var nameSet strings.Builder
	for _, n := range names {
		fmt.Fprintf(&nameSet, "<item>%s</item>", n)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcEndpointServicesResponse %s>
  <requestId>%s</requestId>
  <serviceNameSet>%s</serviceNameSet>
  <serviceDetailSet>%s</serviceDetailSet>
</DescribeVpcEndpointServicesResponse>`, ec2Xmlns(), generateUUID(), nameSet.String(), details.String())
}

// serviceConfigurationXML renders the ServiceConfiguration shape (CreateVpcEndpointServiceConfiguration
// + DescribeVpcEndpointServiceConfigurations).
func serviceConfigurationXML(cfg EC2VpcEndpointServiceConfiguration) string {
	var b strings.Builder
	b.WriteString("<serviceType>")
	b.WriteString("<item><serviceType>" + ec2ServiceTypeForCfg(cfg) + "</serviceType></item>")
	b.WriteString("</serviceType>")
	fmt.Fprintf(&b, "<serviceId>%s</serviceId>", cfg.ServiceId)
	fmt.Fprintf(&b, "<serviceName>%s</serviceName>", cfg.ServiceName)
	fmt.Fprintf(&b, "<serviceState>%s</serviceState>", cfg.ServiceState)
	fmt.Fprintf(&b, "<acceptanceRequired>%t</acceptanceRequired>", cfg.AcceptanceRequired)
	fmt.Fprintf(&b, "<managesVpcEndpoints>%t</managesVpcEndpoints>", cfg.ManagesVpcEndpoints)
	b.WriteString(ec2StringSetXML("networkLoadBalancerArnSet", cfg.NetworkLoadBalancerArns))
	b.WriteString(ec2StringSetXML("gatewayLoadBalancerArnSet", cfg.GatewayLoadBalancerArns))
	b.WriteString(ec2StringSetXML("supportedIpAddressTypeSet", cfg.SupportedIpAddressTypes))
	b.WriteString(ec2StringSetXML("baseEndpointDnsNameSet", cfg.BaseEndpointDnsNames))
	if cfg.PrivateDnsName != "" {
		fmt.Fprintf(&b, "<privateDnsName>%s</privateDnsName>", cfg.PrivateDnsName)
	}
	if cfg.PrivateDnsNameState != "" {
		fmt.Fprintf(&b, "<privateDnsNameConfiguration><state>%s</state><type>TXT</type><value>%s</value><name>_amazonaws</name></privateDnsNameConfiguration>", cfg.PrivateDnsNameState, generateUUID()[:8])
	}
	fmt.Fprintf(&b, "<payerResponsibility>%s</payerResponsibility>", cfg.PayerResponsibility)
	b.WriteString(writeTagSetXML(cfg.Tags))
	return b.String()
}

// serviceDetailXML renders the ServiceDetail shape (DescribeVpcEndpointServices).
func serviceDetailXML(cfg EC2VpcEndpointServiceConfiguration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<serviceName>%s</serviceName>", cfg.ServiceName)
	fmt.Fprintf(&b, "<serviceId>%s</serviceId>", cfg.ServiceId)
	b.WriteString("<serviceType>")
	b.WriteString("<item><serviceType>" + ec2ServiceTypeForCfg(cfg) + "</serviceType></item>")
	b.WriteString("</serviceType>")
	fmt.Fprintf(&b, "<owner>%s</owner>", ec2Owner())
	b.WriteString(ec2StringSetXML("baseEndpointDnsNameSet", cfg.BaseEndpointDnsNames))
	if cfg.PrivateDnsName != "" {
		fmt.Fprintf(&b, "<privateDnsName>%s</privateDnsName>", cfg.PrivateDnsName)
	}
	fmt.Fprintf(&b, "<vpcEndpointPolicySupported>%t</vpcEndpointPolicySupported>", true)
	fmt.Fprintf(&b, "<acceptanceRequired>%t</acceptanceRequired>", cfg.AcceptanceRequired)
	fmt.Fprintf(&b, "<managesVpcEndpoints>%t</managesVpcEndpoints>", cfg.ManagesVpcEndpoints)
	fmt.Fprintf(&b, "<payerResponsibility>%s</payerResponsibility>", cfg.PayerResponsibility)
	b.WriteString(writeTagSetXML(cfg.Tags))
	b.WriteString(ec2StringSetXML("supportedIpAddressTypeSet", cfg.SupportedIpAddressTypes))
	return b.String()
}

// ec2ServiceTypeForCfg classifies a service configuration by the load-balancer
// kind it fronts (Gateway when it fronts a Gateway Load Balancer, else
// Interface).
func ec2ServiceTypeForCfg(cfg EC2VpcEndpointServiceConfiguration) string {
	if len(cfg.GatewayLoadBalancerArns) > 0 {
		return "Gateway"
	}
	return "Interface"
}

// ec2PrincipalType classifies a principal ARN for the AllowedPrincipal shape.
func ec2PrincipalType(principal string) string {
	switch {
	case principal == "*":
		return "All"
	case strings.Contains(principal, ":role/"):
		return "Role"
	case strings.Contains(principal, ":user/"):
		return "User"
	case strings.HasPrefix(principal, "arn:aws:iam::") && strings.HasSuffix(principal, ":root"):
		return "Account"
	default:
		return "Account"
	}
}

func handleAssociateVpcCidrBlock(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	if _, ok := ec2Vpcs.Get(vpcId); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID %q does not exist", vpcId), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("CidrBlock")
	ipv6 := r.FormValue("Ipv6CidrBlock")
	amazonIpv6 := r.FormValue("AmazonProvidedIpv6CidrBlock") == "true"

	var cidrXML, ipv6XML string
	if cidr != "" {
		assocID := ec2ID("vpc-cidr-assoc")
		ec2VpcCidrAssocs.Put(assocID, EC2VpcCidrAssoc{
			AssociationId: assocID, VpcId: vpcId, CidrBlock: cidr, State: "associated",
		})
		cidrXML = fmt.Sprintf("<cidrBlockAssociation><associationId>%s</associationId><cidrBlock>%s</cidrBlock><cidrBlockState><state>associated</state></cidrBlockState></cidrBlockAssociation>", assocID, cidr)
	}
	if ipv6 != "" || amazonIpv6 {
		if ipv6 == "" {
			// Amazon hands out a /56 from its pool.
			ipv6 = "2600:1f00:" + generateUUID()[:4] + "::/56"
		}
		assocID := ec2ID("vpc-cidr-assoc")
		ipv6XML = fmt.Sprintf("<ipv6CidrBlockAssociation><associationId>%s</associationId><ipv6CidrBlock>%s</ipv6CidrBlock><ipv6CidrBlockState><state>associated</state></ipv6CidrBlockState><networkBorderGroup>%s</networkBorderGroup><ipv6Pool>Amazon</ipv6Pool></ipv6CidrBlockAssociation>", assocID, ipv6, awsRegion())
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateVpcCidrBlockResponse %s>
  <requestId>%s</requestId>
  %s%s<vpcId>%s</vpcId>
</AssociateVpcCidrBlockResponse>`, ec2Xmlns(), generateUUID(), cidrXML, ipv6XML, vpcId)
}

func handleDisassociateVpcCidrBlock(w http.ResponseWriter, r *http.Request) {
	assocID := r.FormValue("AssociationId")
	assoc, ok := ec2VpcCidrAssocs.Get(assocID)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcCidrBlockAssociationID.NotFound", fmt.Sprintf("The vpc CIDR block association ID %q does not exist", assocID), http.StatusBadRequest)
		return
	}
	ec2VpcCidrAssocs.Delete(assocID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateVpcCidrBlockResponse %s>
  <requestId>%s</requestId>
  <cidrBlockAssociation><associationId>%s</associationId><cidrBlock>%s</cidrBlock><cidrBlockState><state>disassociating</state></cidrBlockState></cidrBlockAssociation>
  <vpcId>%s</vpcId>
</DisassociateVpcCidrBlockResponse>`, ec2Xmlns(), generateUUID(), assoc.AssociationId, assoc.CidrBlock, assoc.VpcId)
}

func handleAssociateSubnetCidrBlock(w http.ResponseWriter, r *http.Request) {
	subnetId := r.FormValue("SubnetId")
	if _, ok := ec2Subnets.Get(subnetId); !ok {
		ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID %q does not exist", subnetId), http.StatusBadRequest)
		return
	}
	// AssociateSubnetCidrBlock only adds an IPv6 CIDR (the IPv4 secondary CIDR
	// of a subnet is set at create time); its result carries only the IPv6
	// association.
	ipv6 := r.FormValue("Ipv6CidrBlock")
	if ipv6 == "" {
		ipv6 = "2600:1f00:" + generateUUID()[:4] + "::/64"
	}
	assocID := ec2ID("subnet-cidr-assoc")
	ec2VpcCidrAssocs.Put(assocID, EC2VpcCidrAssoc{AssociationId: assocID, VpcId: subnetId, CidrBlock: ipv6, State: "associated"})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateSubnetCidrBlockResponse %s>
  <requestId>%s</requestId>
  <ipv6CidrBlockAssociation><associationId>%s</associationId><ipv6CidrBlock>%s</ipv6CidrBlock><ipv6CidrBlockState><state>associated</state></ipv6CidrBlockState></ipv6CidrBlockAssociation>
  <subnetId>%s</subnetId>
</AssociateSubnetCidrBlockResponse>`, ec2Xmlns(), generateUUID(), assocID, ipv6, subnetId)
}

func handleDisassociateSubnetCidrBlock(w http.ResponseWriter, r *http.Request) {
	assocID := r.FormValue("AssociationId")
	assoc, ok := ec2VpcCidrAssocs.Get(assocID)
	if !ok {
		ec2ErrorXML(w, "InvalidSubnetCidrBlockAssociationID.NotFound", fmt.Sprintf("The subnet CIDR block association ID %q does not exist", assocID), http.StatusBadRequest)
		return
	}
	ec2VpcCidrAssocs.Delete(assocID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateSubnetCidrBlockResponse %s>
  <requestId>%s</requestId>
  <ipv6CidrBlockAssociation><associationId>%s</associationId><ipv6CidrBlock>%s</ipv6CidrBlock><ipv6CidrBlockState><state>disassociating</state></ipv6CidrBlockState></ipv6CidrBlockAssociation>
  <subnetId>%s</subnetId>
</DisassociateSubnetCidrBlockResponse>`, ec2Xmlns(), generateUUID(), assoc.AssociationId, assoc.CidrBlock, assoc.VpcId)
}

func handleCreateSubnetCidrReservation(w http.ResponseWriter, r *http.Request) {
	subnetId := r.FormValue("SubnetId")
	if _, ok := ec2Subnets.Get(subnetId); !ok {
		ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID %q does not exist", subnetId), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("Cidr")
	resType := r.FormValue("ReservationType")
	id := ec2ID("subnet-cidr-res")
	res := EC2SubnetCidrReservation{
		SubnetCidrReservationId: id,
		SubnetId:                subnetId,
		Cidr:                    cidr,
		ReservationType:         resType,
		OwnerId:                 ec2Owner(),
		Description:             r.FormValue("Description"),
		Ipv6:                    strings.Contains(cidr, ":"),
		Tags:                    parseTags(r),
	}
	ec2SubnetCidrReservations.Put(id, res)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSubnetCidrReservationResponse %s>
  <requestId>%s</requestId>
  <subnetCidrReservation>%s</subnetCidrReservation>
</CreateSubnetCidrReservationResponse>`, ec2Xmlns(), generateUUID(), subnetCidrReservationXML(res))
}

func handleDeleteSubnetCidrReservation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("SubnetCidrReservationId")
	res, ok := ec2SubnetCidrReservations.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidSubnetCidrReservationID.NotFound", fmt.Sprintf("The subnet CIDR reservation ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	ec2SubnetCidrReservations.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSubnetCidrReservationResponse %s>
  <requestId>%s</requestId>
  <deletedSubnetCidrReservation>%s</deletedSubnetCidrReservation>
</DeleteSubnetCidrReservationResponse>`, ec2Xmlns(), generateUUID(), subnetCidrReservationXML(res))
}

func handleGetSubnetCidrReservations(w http.ResponseWriter, r *http.Request) {
	subnetId := r.FormValue("SubnetId")
	var ipv4, ipv6 strings.Builder
	for _, res := range ec2SubnetCidrReservations.List() {
		if subnetId != "" && res.SubnetId != subnetId {
			continue
		}
		if res.Ipv6 {
			ipv6.WriteString("<item>" + subnetCidrReservationXML(res) + "</item>")
		} else {
			ipv4.WriteString("<item>" + subnetCidrReservationXML(res) + "</item>")
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetSubnetCidrReservationsResponse %s>
  <requestId>%s</requestId>
  <subnetIpv4CidrReservationSet>%s</subnetIpv4CidrReservationSet>
  <subnetIpv6CidrReservationSet>%s</subnetIpv6CidrReservationSet>
</GetSubnetCidrReservationsResponse>`, ec2Xmlns(), generateUUID(), ipv4.String(), ipv6.String())
}

func subnetCidrReservationXML(res EC2SubnetCidrReservation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<subnetCidrReservationId>%s</subnetCidrReservationId>", res.SubnetCidrReservationId)
	fmt.Fprintf(&b, "<subnetId>%s</subnetId>", res.SubnetId)
	fmt.Fprintf(&b, "<cidr>%s</cidr>", res.Cidr)
	if res.ReservationType != "" {
		fmt.Fprintf(&b, "<reservationType>%s</reservationType>", res.ReservationType)
	}
	fmt.Fprintf(&b, "<ownerId>%s</ownerId>", res.OwnerId)
	if res.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", res.Description)
	}
	b.WriteString(writeTagSetXML(res.Tags))
	return b.String()
}

func handleCreateSecondaryNetwork(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Ipv4CidrBlock")
	networkType := r.FormValue("NetworkType")
	if networkType == "" {
		networkType = "rdma"
	}
	id := ec2ID("sn")
	sn := EC2SecondaryNetwork{
		SecondaryNetworkId: id,
		OwnerId:            ec2Owner(),
		Type:               networkType,
		State:              "create_complete",
		Tags:               parseTags(r),
	}
	if cidr != "" {
		sn.Ipv4CidrBlocks = []EC2SecondaryCidrAssoc{{AssociationId: ec2ID("sn-cidr-assoc"), CidrBlock: cidr, State: "associated"}}
	}
	ec2SecondaryNetworks.Put(id, sn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSecondaryNetworkResponse %s>
  <requestId>%s</requestId>
  <secondaryNetwork>%s</secondaryNetwork>
  <clientToken>%s</clientToken>
</CreateSecondaryNetworkResponse>`, ec2Xmlns(), generateUUID(), secondaryNetworkXML(sn), r.FormValue("ClientToken"))
}

func handleDeleteSecondaryNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("SecondaryNetworkId")
	sn, ok := ec2SecondaryNetworks.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidSecondaryNetworkID.NotFound", fmt.Sprintf("The secondary network ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	sn.State = "delete_complete"
	ec2SecondaryNetworks.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSecondaryNetworkResponse %s>
  <requestId>%s</requestId>
  <secondaryNetwork>%s</secondaryNetwork>
  <clientToken>%s</clientToken>
</DeleteSecondaryNetworkResponse>`, ec2Xmlns(), generateUUID(), secondaryNetworkXML(sn), r.FormValue("ClientToken"))
}

func handleDescribeSecondaryNetworks(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SecondaryNetworkId")
	var items strings.Builder
	for _, sn := range ec2SecondaryNetworks.List() {
		if len(ids) > 0 && !ec2StrInValues(sn.SecondaryNetworkId, ids) {
			continue
		}
		items.WriteString("<item>" + secondaryNetworkXML(sn) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSecondaryNetworksResponse %s>
  <requestId>%s</requestId>
  <secondaryNetworkSet>%s</secondaryNetworkSet>
</DescribeSecondaryNetworksResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func secondaryNetworkXML(sn EC2SecondaryNetwork) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<secondaryNetworkId>%s</secondaryNetworkId>", sn.SecondaryNetworkId)
	fmt.Fprintf(&b, "<secondaryNetworkArn>arn:aws:ec2:%s:%s:secondary-network/%s</secondaryNetworkArn>", awsRegion(), sn.OwnerId, sn.SecondaryNetworkId)
	fmt.Fprintf(&b, "<ownerId>%s</ownerId>", sn.OwnerId)
	fmt.Fprintf(&b, "<type>%s</type>", sn.Type)
	fmt.Fprintf(&b, "<state>%s</state>", sn.State)
	b.WriteString(secondaryCidrAssocSetXML(sn.Ipv4CidrBlocks))
	b.WriteString(writeTagSetXML(sn.Tags))
	return b.String()
}

func handleCreateSecondarySubnet(w http.ResponseWriter, r *http.Request) {
	networkId := r.FormValue("SecondaryNetworkId")
	sn, ok := ec2SecondaryNetworks.Get(networkId)
	if !ok {
		ec2ErrorXML(w, "InvalidSecondaryNetworkID.NotFound", fmt.Sprintf("The secondary network ID %q does not exist", networkId), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("Ipv4CidrBlock")
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		az = awsAvailabilityZone()
	}
	id := ec2ID("sns")
	ss := EC2SecondarySubnet{
		SecondarySubnetId:    id,
		SecondaryNetworkId:   networkId,
		SecondaryNetworkType: sn.Type,
		OwnerId:              ec2Owner(),
		AvailabilityZone:     az,
		AvailabilityZoneId:   r.FormValue("AvailabilityZoneId"),
		State:                "create_complete",
		Tags:                 parseTags(r),
	}
	if cidr != "" {
		ss.Ipv4CidrBlocks = []EC2SecondaryCidrAssoc{{AssociationId: ec2ID("sns-cidr-assoc"), CidrBlock: cidr, State: "associated"}}
	}
	ec2SecondarySubnets.Put(id, ss)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSecondarySubnetResponse %s>
  <requestId>%s</requestId>
  <secondarySubnet>%s</secondarySubnet>
  <clientToken>%s</clientToken>
</CreateSecondarySubnetResponse>`, ec2Xmlns(), generateUUID(), secondarySubnetXML(ss), r.FormValue("ClientToken"))
}

func handleDeleteSecondarySubnet(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("SecondarySubnetId")
	ss, ok := ec2SecondarySubnets.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidSecondarySubnetID.NotFound", fmt.Sprintf("The secondary subnet ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	ss.State = "delete_complete"
	ec2SecondarySubnets.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSecondarySubnetResponse %s>
  <requestId>%s</requestId>
  <secondarySubnet>%s</secondarySubnet>
  <clientToken>%s</clientToken>
</DeleteSecondarySubnetResponse>`, ec2Xmlns(), generateUUID(), secondarySubnetXML(ss), r.FormValue("ClientToken"))
}

func handleDescribeSecondarySubnets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SecondarySubnetId")
	var items strings.Builder
	for _, ss := range ec2SecondarySubnets.List() {
		if len(ids) > 0 && !ec2StrInValues(ss.SecondarySubnetId, ids) {
			continue
		}
		items.WriteString("<item>" + secondarySubnetXML(ss) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSecondarySubnetsResponse %s>
  <requestId>%s</requestId>
  <secondarySubnetSet>%s</secondarySubnetSet>
</DescribeSecondarySubnetsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func secondarySubnetXML(ss EC2SecondarySubnet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<secondarySubnetId>%s</secondarySubnetId>", ss.SecondarySubnetId)
	fmt.Fprintf(&b, "<secondarySubnetArn>arn:aws:ec2:%s:%s:secondary-subnet/%s</secondarySubnetArn>", awsRegion(), ss.OwnerId, ss.SecondarySubnetId)
	fmt.Fprintf(&b, "<secondaryNetworkId>%s</secondaryNetworkId>", ss.SecondaryNetworkId)
	if ss.SecondaryNetworkType != "" {
		fmt.Fprintf(&b, "<secondaryNetworkType>%s</secondaryNetworkType>", ss.SecondaryNetworkType)
	}
	fmt.Fprintf(&b, "<ownerId>%s</ownerId>", ss.OwnerId)
	if ss.AvailabilityZoneId != "" {
		fmt.Fprintf(&b, "<availabilityZoneId>%s</availabilityZoneId>", ss.AvailabilityZoneId)
	}
	fmt.Fprintf(&b, "<availabilityZone>%s</availabilityZone>", ss.AvailabilityZone)
	b.WriteString(secondaryCidrAssocSetXML(ss.Ipv4CidrBlocks))
	fmt.Fprintf(&b, "<state>%s</state>", ss.State)
	b.WriteString(writeTagSetXML(ss.Tags))
	return b.String()
}

func secondaryCidrAssocSetXML(assocs []EC2SecondaryCidrAssoc) string {
	var b strings.Builder
	b.WriteString("<ipv4CidrBlockAssociationSet>")
	for _, a := range assocs {
		fmt.Fprintf(&b, "<item><associationId>%s</associationId><cidrBlock>%s</cidrBlock><state>%s</state></item>", a.AssociationId, a.CidrBlock, a.State)
	}
	b.WriteString("</ipv4CidrBlockAssociationSet>")
	return b.String()
}

func handleAssociateSecurityGroupVpc(w http.ResponseWriter, r *http.Request) {
	groupId := r.FormValue("GroupId")
	vpcId := r.FormValue("VpcId")
	if _, ok := ec2SecurityGroups.Get(groupId); !ok {
		ec2ErrorXML(w, "InvalidGroup.NotFound", fmt.Sprintf("The security group %q does not exist", groupId), http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcId); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID %q does not exist", vpcId), http.StatusBadRequest)
		return
	}
	key := groupId + "|" + vpcId
	ec2SgVpcAssociations.Put(key, EC2SecurityGroupVpcAssociation{
		GroupId: groupId, VpcId: vpcId, VpcOwnerId: ec2Owner(), State: "associated", GroupOwnerId: ec2Owner(),
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateSecurityGroupVpcResponse %s>
  <requestId>%s</requestId>
  <state>associated</state>
</AssociateSecurityGroupVpcResponse>`, ec2Xmlns(), generateUUID())
}

func handleDisassociateSecurityGroupVpc(w http.ResponseWriter, r *http.Request) {
	groupId := r.FormValue("GroupId")
	vpcId := r.FormValue("VpcId")
	key := groupId + "|" + vpcId
	if _, ok := ec2SgVpcAssociations.Get(key); !ok {
		ec2ErrorXML(w, "InvalidSecurityGroupVpcAssociation.NotFound", fmt.Sprintf("No association between security group %q and vpc %q", groupId, vpcId), http.StatusBadRequest)
		return
	}
	ec2SgVpcAssociations.Delete(key)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateSecurityGroupVpcResponse %s>
  <requestId>%s</requestId>
  <state>disassociating</state>
</DisassociateSecurityGroupVpcResponse>`, ec2Xmlns(), generateUUID())
}

func handleDescribeSecurityGroupVpcAssociations(w http.ResponseWriter, r *http.Request) {
	filters := ec2Filters(r)
	var items strings.Builder
	for _, a := range ec2SgVpcAssociations.List() {
		if !ec2SgVpcAssocMatchesFilters(a, filters) {
			continue
		}
		fmt.Fprintf(&items, "<item><groupId>%s</groupId><vpcId>%s</vpcId><vpcOwnerId>%s</vpcOwnerId><state>%s</state><groupOwnerId>%s</groupOwnerId></item>",
			a.GroupId, a.VpcId, a.VpcOwnerId, a.State, a.GroupOwnerId)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSecurityGroupVpcAssociationsResponse %s>
  <requestId>%s</requestId>
  <securityGroupVpcAssociationSet>%s</securityGroupVpcAssociationSet>
</DescribeSecurityGroupVpcAssociationsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func ec2SgVpcAssocMatchesFilters(a EC2SecurityGroupVpcAssociation, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "group-id":
			if !ec2StrInValues(a.GroupId, vals) {
				return false
			}
		case "vpc-id":
			if !ec2StrInValues(a.VpcId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(a.State, vals) {
				return false
			}
		}
	}
	return true
}

// ec2EncryptionExclusionResources is the fixed set of resource exclusion fields
// a VpcEncryptionControl carries, in their VpcEncryptionControlExclusions
// xmlName form.
var ec2EncryptionExclusionResources = []struct{ field, xmlName string }{
	{"InternetGatewayExclusion", "internetGateway"},
	{"EgressOnlyInternetGatewayExclusion", "egressOnlyInternetGateway"},
	{"NatGatewayExclusion", "natGateway"},
	{"VirtualPrivateGatewayExclusion", "virtualPrivateGateway"},
	{"VpcPeeringExclusion", "vpcPeering"},
	{"LambdaExclusion", "lambda"},
	{"VpcLatticeExclusion", "vpcLattice"},
	{"ElasticFileSystemExclusion", "elasticFileSystem"},
}

var ec2AccountEncryptionExclusionResources = []struct{ field, xmlName string }{
	{"InternetGateway", "internetGateway"},
	{"EgressOnlyInternetGateway", "egressOnlyInternetGateway"},
	{"NatGateway", "natGateway"},
	{"VirtualPrivateGateway", "virtualPrivateGateway"},
	{"VpcPeering", "vpcPeering"},
	{"Lambda", "lambda"},
	{"VpcLattice", "vpcLattice"},
	{"ElasticFileSystem", "elasticFileSystem"},
}

func defaultAccountVpcEncryptionControl() EC2AccountVpcEncryptionControl {
	exclusions := make(map[string]string, len(ec2AccountEncryptionExclusionResources))
	for _, resource := range ec2AccountEncryptionExclusionResources {
		exclusions[resource.xmlName] = "disabled"
	}
	return EC2AccountVpcEncryptionControl{
		State:      "default-state",
		Mode:       "unmanaged",
		Exclusions: exclusions,
		ManagedBy:  "account",
	}
}

func currentAccountVpcEncryptionControl() EC2AccountVpcEncryptionControl {
	control, ok := ec2AccountVpcEncryptionControls.Get(awsRegion())
	if !ok {
		return defaultAccountVpcEncryptionControl()
	}
	if control.Exclusions == nil {
		control.Exclusions = defaultAccountVpcEncryptionControl().Exclusions
	}
	if control.ManagedBy == "" {
		control.ManagedBy = "account"
	}
	return control
}

func handleDescribeAccountVpcEncryptionControl(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("DryRun") == "true" {
		ec2ErrorXML(w, "DryRunOperation", "Request would have succeeded, but DryRun flag is set.", http.StatusPreconditionFailed)
		return
	}
	control := currentAccountVpcEncryptionControl()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeAccountVpcEncryptionControlResponse %s>
  <requestId>%s</requestId>
  <accountVpcEncryptionControl>%s</accountVpcEncryptionControl>
</DescribeAccountVpcEncryptionControlResponse>`, ec2Xmlns(), generateUUID(), accountVpcEncryptionControlXML(control))
}

func handleModifyAccountVpcEncryptionControl(w http.ResponseWriter, r *http.Request) {
	control := currentAccountVpcEncryptionControl()
	exclusions := make(map[string]string, len(control.Exclusions))
	for resource, state := range control.Exclusions {
		exclusions[resource] = state
	}
	mode := r.FormValue("Mode")
	if mode != "" && mode != "unmanaged" && mode != "attempt-monitor" && mode != "attempt-enforce" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value %q at 'mode' failed to satisfy constraint", mode), http.StatusBadRequest)
		return
	}

	changed := mode != ""
	for _, resource := range ec2AccountEncryptionExclusionResources {
		value := r.FormValue(resource.field)
		if value == "" {
			continue
		}
		changed = true
		switch value {
		case "enable":
			exclusions[resource.xmlName] = "enabled"
		case "disable":
			exclusions[resource.xmlName] = "disabled"
		default:
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value %q at %q failed to satisfy constraint", value, resource.field), http.StatusBadRequest)
			return
		}
	}
	if !changed {
		ec2ErrorXML(w, "InvalidParameterCombination", "At least one account VPC encryption control setting must be specified", http.StatusBadRequest)
		return
	}
	if r.FormValue("DryRun") == "true" {
		ec2ErrorXML(w, "DryRunOperation", "Request would have succeeded, but DryRun flag is set.", http.StatusPreconditionFailed)
		return
	}

	if mode != "" {
		control.Mode = mode
	}
	control.Exclusions = exclusions
	control.State = "transitions-successful"
	control.ManagedBy = "account"
	control.LastUpdateTimestamp = time.Now().UTC().Format(time.RFC3339Nano)
	ec2AccountVpcEncryptionControls.Put(awsRegion(), control)
	reconcileAccountVpcEncryptionControl(control)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyAccountVpcEncryptionControlResponse %s>
  <requestId>%s</requestId>
  <accountVpcEncryptionControl>%s</accountVpcEncryptionControl>
</ModifyAccountVpcEncryptionControlResponse>`, ec2Xmlns(), generateUUID(), accountVpcEncryptionControlXML(control))
}

func vpcEncryptionConfigurationFromCreateRequest(r *http.Request) (string, map[string]string, bool, error) {
	const prefix = "VpcEncryptionControl."
	mode := r.FormValue(prefix + "Mode")
	present := mode != ""
	exclusions := make(map[string]string, len(ec2EncryptionExclusionResources))
	for _, resource := range ec2EncryptionExclusionResources {
		exclusions[resource.xmlName] = "disabled"
		value := r.FormValue(prefix + resource.field)
		if value == "" {
			continue
		}
		present = true
		switch value {
		case "enable":
			exclusions[resource.xmlName] = "enabled"
		case "disable":
			exclusions[resource.xmlName] = "disabled"
		default:
			return "", nil, false, ec2ParameterConstraintError{value: value, field: prefix + resource.field}
		}
	}
	if !present {
		return "", nil, false, nil
	}
	if mode != "monitor" && mode != "enforce" {
		return "", nil, false, ec2ParameterConstraintError{value: mode, field: prefix + "Mode"}
	}
	return mode, exclusions, true, nil
}

type ec2ParameterConstraintError struct {
	value string
	field string
}

func (e ec2ParameterConstraintError) Error() string {
	return fmt.Sprintf("Value %q at %q failed to satisfy constraint", e.value, e.field)
}

func vpcEncryptionControlForVPC(vpcID string) (EC2VpcEncryptionControl, bool) {
	for _, control := range ec2VpcEncryptionControls.List() {
		if control.VpcId == vpcID {
			return control, true
		}
	}
	return EC2VpcEncryptionControl{}, false
}

func reconcileAccountVpcEncryptionControl(account EC2AccountVpcEncryptionControl) {
	mode := ""
	switch account.Mode {
	case "attempt-monitor":
		mode = "monitor"
	case "attempt-enforce":
		mode = "enforce"
	}
	if mode == "" {
		return
	}
	for _, vpc := range ec2Vpcs.List() {
		applyAccountVpcEncryptionControl(vpc.VpcId, mode, account.Exclusions)
	}
}

func applyAccountVpcEncryptionControl(vpcID, mode string, exclusions map[string]string) {
	var existingID string
	var control EC2VpcEncryptionControl
	for _, candidate := range ec2VpcEncryptionControls.List() {
		if candidate.VpcId == vpcID {
			existingID = candidate.VpcEncryptionControlId
			control = candidate
			break
		}
	}
	if existingID == "" {
		existingID = ec2ID("vpce")
		control = EC2VpcEncryptionControl{
			VpcEncryptionControlId: existingID,
			VpcId:                  vpcID,
		}
	}
	control.Mode = mode
	control.State = "available"
	control.Exclusions = make(map[string]string, len(exclusions))
	for name, state := range exclusions {
		control.Exclusions[name] = state
	}
	ec2VpcEncryptionControls.Put(existingID, control)
}

func accountVpcEncryptionControlXML(control EC2AccountVpcEncryptionControl) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<state>%s</state>", control.State)
	fmt.Fprintf(&b, "<mode>%s</mode>", control.Mode)
	b.WriteString("<exclusions>")
	for _, resource := range ec2AccountEncryptionExclusionResources {
		state := control.Exclusions[resource.xmlName]
		if state == "" {
			state = "disabled"
		}
		fmt.Fprintf(&b, "<%s>%s</%s>", resource.xmlName, state, resource.xmlName)
	}
	b.WriteString("</exclusions>")
	fmt.Fprintf(&b, "<managedBy>%s</managedBy>", control.ManagedBy)
	if control.LastUpdateTimestamp != "" {
		fmt.Fprintf(&b, "<lastUpdateTimestamp>%s</lastUpdateTimestamp>", control.LastUpdateTimestamp)
	}
	return b.String()
}

func handleCreateVpcEncryptionControl(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	if _, ok := ec2Vpcs.Get(vpcId); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID %q does not exist", vpcId), http.StatusBadRequest)
		return
	}
	id := ec2ID("vpce")
	exclusions := map[string]string{}
	for _, res := range ec2EncryptionExclusionResources {
		exclusions[res.xmlName] = "disabled"
	}
	ctrl := EC2VpcEncryptionControl{
		VpcEncryptionControlId: id,
		VpcId:                  vpcId,
		Mode:                   "monitor",
		State:                  "available",
		Exclusions:             exclusions,
		Tags:                   parseTags(r),
	}
	ec2VpcEncryptionControls.Put(id, ctrl)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpcEncryptionControlResponse %s>
  <requestId>%s</requestId>
  <vpcEncryptionControl>%s</vpcEncryptionControl>
</CreateVpcEncryptionControlResponse>`, ec2Xmlns(), generateUUID(), vpcEncryptionControlXML(ctrl))
}

func handleDeleteVpcEncryptionControl(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpcEncryptionControlId")
	ctrl, ok := ec2VpcEncryptionControls.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcEncryptionControlID.NotFound", fmt.Sprintf("The VPC encryption control ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	ctrl.State = "deleting"
	ec2VpcEncryptionControls.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpcEncryptionControlResponse %s>
  <requestId>%s</requestId>
  <vpcEncryptionControl>%s</vpcEncryptionControl>
</DeleteVpcEncryptionControlResponse>`, ec2Xmlns(), generateUUID(), vpcEncryptionControlXML(ctrl))
}

func handleModifyVpcEncryptionControl(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpcEncryptionControlId")
	if _, ok := ec2VpcEncryptionControls.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpcEncryptionControlID.NotFound", fmt.Sprintf("The VPC encryption control ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VpcEncryptionControls.Update(id, func(ctrl *EC2VpcEncryptionControl) {
		if mode := r.FormValue("Mode"); mode != "" {
			ctrl.Mode = mode
		}
		if ctrl.Exclusions == nil {
			ctrl.Exclusions = map[string]string{}
		}
		for _, res := range ec2EncryptionExclusionResources {
			if v := r.FormValue(res.field); v != "" {
				// The Modify input carries the enable/disable verb; the read-back
				// state is the corresponding enabled/disabled steady state.
				switch v {
				case "enable":
					ctrl.Exclusions[res.xmlName] = "enabled"
				case "disable":
					ctrl.Exclusions[res.xmlName] = "disabled"
				default:
					ctrl.Exclusions[res.xmlName] = v
				}
			}
		}
	})
	ctrl, _ := ec2VpcEncryptionControls.Get(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcEncryptionControlResponse %s>
  <requestId>%s</requestId>
  <vpcEncryptionControl>%s</vpcEncryptionControl>
</ModifyVpcEncryptionControlResponse>`, ec2Xmlns(), generateUUID(), vpcEncryptionControlXML(ctrl))
}

func handleDescribeVpcEncryptionControls(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpcEncryptionControlId")
	vpcIds := ec2ParamList(r, "VpcId")
	var items strings.Builder
	for _, ctrl := range ec2VpcEncryptionControls.List() {
		if len(ids) > 0 && !ec2StrInValues(ctrl.VpcEncryptionControlId, ids) {
			continue
		}
		if len(vpcIds) > 0 && !ec2StrInValues(ctrl.VpcId, vpcIds) {
			continue
		}
		items.WriteString("<item>" + vpcEncryptionControlXML(ctrl) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcEncryptionControlsResponse %s>
  <requestId>%s</requestId>
  <vpcEncryptionControlSet>%s</vpcEncryptionControlSet>
</DescribeVpcEncryptionControlsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func vpcEncryptionControlXML(ctrl EC2VpcEncryptionControl) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<vpcId>%s</vpcId>", ctrl.VpcId)
	fmt.Fprintf(&b, "<vpcEncryptionControlId>%s</vpcEncryptionControlId>", ctrl.VpcEncryptionControlId)
	fmt.Fprintf(&b, "<mode>%s</mode>", ctrl.Mode)
	fmt.Fprintf(&b, "<state>%s</state>", ctrl.State)
	if ctrl.Mode == "enforce" {
		b.WriteString("<resourceExclusions>")
		for _, res := range ec2EncryptionExclusionResources {
			state := ctrl.Exclusions[res.xmlName]
			if state == "" {
				state = "disabled"
			}
			fmt.Fprintf(&b, "<%s><state>%s</state></%s>", res.xmlName, state, res.xmlName)
		}
		b.WriteString("</resourceExclusions>")
	}
	b.WriteString(writeTagSetXML(ctrl.Tags))
	return b.String()
}

// ec2StringSetXML renders a ValueStringList under the given set element with
// <item> entries.
func ec2StringSetXML(elem string, vals []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", elem)
	for _, v := range vals {
		fmt.Fprintf(&b, "<item>%s</item>", v)
	}
	fmt.Fprintf(&b, "</%s>", elem)
	return b.String()
}

// ec2ApplyAddRemove applies an Add/Remove pair to a string slice (idempotent
// add, set-subtract remove), the Modify* semantics of the endpoint-service
// configuration.
func ec2ApplyAddRemove(cur, add, remove []string) []string {
	out := append([]string(nil), cur...)
	for _, a := range add {
		found := false
		for _, c := range out {
			if c == a {
				found = true
				break
			}
		}
		if !found {
			out = append(out, a)
		}
	}
	if len(remove) > 0 {
		kept := out[:0:0]
		for _, c := range out {
			if ec2StrInValues(c, remove) {
				continue
			}
			kept = append(kept, c)
		}
		out = kept
	}
	return out
}
