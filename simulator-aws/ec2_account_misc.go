package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements the EC2 account-settings, ID-format, EIP
// address-attribute (PTR record), EC2-Classic↔VPC ClassicLink, Nitro Enclave
// certificate↔IAM-role, Client VPN export/import, Site-to-Site VPN
// tunnel-replacement, transit-gateway Connect-peer, VPC-peering-options, and
// network-ACL-association control-plane operations. Each backs onto a real
// SQLite-persisted store and renders the exact ec2Query XML the AWS SDK for Go
// v2 and the aws CLI deserialize, matching com.amazonaws.ec2.

// ---- Types ----

// EC2IdFormatSetting records the per-resource-type useLongIds preference at the
// account level. Real EC2 has long IDs permanently enabled for every resource
// type (the opt-in period closed years ago), so the honest default is true.
type EC2IdFormatSetting struct {
	Resource   string
	UseLongIds bool
}

// EC2AddressAttribute records the PTR (reverse-DNS) record configured on an
// Elastic IP, keyed by allocation ID. Real EC2 stores this separately from the
// address itself (ModifyAddressAttribute / ResetAddressAttribute mutate it).
type EC2AddressAttribute struct {
	AllocationId string
	PublicIp     string
	PtrRecord    string
}

// EC2ClassicLink records a ClassicLink attachment between an EC2-Classic
// instance and a VPC, with the security groups applied to the link.
type EC2ClassicLink struct {
	InstanceId string
	VpcId      string
	GroupIds   []string
}

// EC2EnclaveCertRole records an association between an ACM certificate and an
// IAM role for AWS Nitro Enclaves ACM integration, including the deterministic
// S3 bucket/key where the certificate bundle is uploaded.
type EC2EnclaveCertRole struct {
	CertificateArn          string
	RoleArn                 string
	CertificateS3BucketName string
	CertificateS3ObjectKey  string
	EncryptionKmsKeyId      string
}

// EC2ClientVpnCertRevocationList records the imported client certificate
// revocation list PEM for a Client VPN endpoint, keyed by endpoint ID.
type EC2ClientVpnCertRevocationList struct {
	ClientVpnEndpointId string
	Pem                 string
}

// EC2TransitGatewayConnectPeer models a Connect peer (GRE tunnel + BGP) on a
// transit-gateway Connect attachment.
type EC2TransitGatewayConnectPeer struct {
	TransitGatewayConnectPeerId string
	TransitGatewayAttachmentId  string
	State                       string
	CreationTime                string
	TransitGatewayAddress       string
	PeerAddress                 string
	InsideCidrBlocks            []string
	Protocol                    string
	PeerAsn                     int64
	TransitGatewayAsn           int64
	Tags                        []EC2Tag
}

var (
	ec2IdFormatSettings    sim.Store[EC2IdFormatSetting]
	ec2AddressAttributes   sim.Store[EC2AddressAttribute]
	ec2ClassicLinks        sim.Store[EC2ClassicLink]
	ec2EnclaveCertRoles    sim.Store[EC2EnclaveCertRole]
	ec2ClientVpnCertCRLs   sim.Store[EC2ClientVpnCertRevocationList]
	ec2TGWConnectPeers     sim.Store[EC2TransitGatewayConnectPeer]
	ec2ManagedResourceVis  sim.Store[EC2IdFormatSetting] // reused row shape: holds the single visibility setting
	ec2VpcPeeringOptionsDB sim.Store[EC2VpcPeeringOptions]
)

// EC2VpcPeeringOptions records the DNS/ClassicLink peering options per side of a
// VPC peering connection, keyed by the peering-connection ID.
type EC2VpcPeeringOptions struct {
	VpcPeeringConnectionId                          string
	AccepterAllowDnsResolutionFromRemoteVpc         bool
	AccepterAllowEgressLocalClassicLinkToRemoteVpc  bool
	AccepterAllowEgressLocalVpcToRemoteClassicLink  bool
	RequesterAllowDnsResolutionFromRemoteVpc        bool
	RequesterAllowEgressLocalClassicLinkToRemoteVpc bool
	RequesterAllowEgressLocalVpcToRemoteClassicLink bool
}

// ec2IdFormatResources is the canonical set of resource types EC2 reports
// ID-format status for. It mirrors the resource enumeration in the
// ModifyIdFormat documentation.
var ec2IdFormatResources = []string{
	"bundle", "conversion-task", "customer-gateway", "dhcp-options",
	"elastic-ip-allocation", "elastic-ip-association", "export-task",
	"flow-log", "image", "import-task", "internet-gateway", "network-acl",
	"network-acl-association", "network-interface", "network-interface-attachment",
	"prefix-list", "route-table", "route-table-association", "security-group",
	"subnet", "subnet-cidr-block-association", "vpc", "vpc-cidr-block-association",
	"vpc-endpoint", "vpc-peering-connection", "vpn-connection", "vpn-gateway",
}

func registerEC2AccountMisc(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2IdFormatSettings = sim.MakeStore[EC2IdFormatSetting](srv.DB(), "ec2_id_format_settings")
	ec2AddressAttributes = sim.MakeStore[EC2AddressAttribute](srv.DB(), "ec2_address_attributes")
	ec2ClassicLinks = sim.MakeStore[EC2ClassicLink](srv.DB(), "ec2_classic_links")
	ec2EnclaveCertRoles = sim.MakeStore[EC2EnclaveCertRole](srv.DB(), "ec2_enclave_cert_roles")
	ec2ClientVpnCertCRLs = sim.MakeStore[EC2ClientVpnCertRevocationList](srv.DB(), "ec2_client_vpn_crls")
	ec2TGWConnectPeers = sim.MakeStore[EC2TransitGatewayConnectPeer](srv.DB(), "ec2_tgw_connect_peers")
	ec2ManagedResourceVis = sim.MakeStore[EC2IdFormatSetting](srv.DB(), "ec2_managed_resource_visibility")
	ec2VpcPeeringOptionsDB = sim.MakeStore[EC2VpcPeeringOptions](srv.DB(), "ec2_vpc_peering_options")

	for action, h := range map[string]http.HandlerFunc{
		// ID format / account settings
		"DescribeIdFormat":          handleDescribeIdFormat,
		"ModifyIdFormat":            handleModifyIdFormat,
		"DescribeIdentityIdFormat":  handleDescribeIdentityIdFormat,
		"ModifyIdentityIdFormat":    handleModifyIdentityIdFormat,
		"DescribeAggregateIdFormat": handleDescribeAggregateIdFormat,
		"DescribePrincipalIdFormat": handleDescribePrincipalIdFormat,
		// EIP address attributes + domain transitions
		"ModifyAddressAttribute":  handleModifyAddressAttribute,
		"ResetAddressAttribute":   handleResetAddressAttribute,
		"MoveAddressToVpc":        handleMoveAddressToVpc,
		"RestoreAddressToClassic": handleRestoreAddressToClassic,
		"DescribeMovingAddresses": handleDescribeMovingAddresses,
		// ClassicLink
		"AttachClassicLinkVpc": handleAttachClassicLinkVpc,
		"DetachClassicLinkVpc": handleDetachClassicLinkVpc,
		// Nitro Enclave certificate ↔ IAM role
		"AssociateEnclaveCertificateIamRole":      handleAssociateEnclaveCertificateIamRole,
		"DisassociateEnclaveCertificateIamRole":   handleDisassociateEnclaveCertificateIamRole,
		"GetAssociatedEnclaveCertificateIamRoles": handleGetAssociatedEnclaveCertificateIamRoles,
		// Client VPN export/import
		"ExportClientVpnClientCertificateRevocationList": handleExportClientVpnClientCertificateRevocationList,
		"ImportClientVpnClientCertificateRevocationList": handleImportClientVpnClientCertificateRevocationList,
		"ExportClientVpnClientConfiguration":             handleExportClientVpnClientConfiguration,
		// Site-to-Site VPN tunnel replacement / certificate
		"GetVpnTunnelReplacementStatus": handleGetVpnTunnelReplacementStatus,
		"ModifyVpnTunnelCertificate":    handleModifyVpnTunnelCertificate,
		"ReplaceVpnTunnel":              handleReplaceVpnTunnel,
		// Transit gateway Connect peer
		"CreateTransitGatewayConnectPeer":    handleCreateTransitGatewayConnectPeer,
		"DeleteTransitGatewayConnectPeer":    handleDeleteTransitGatewayConnectPeer,
		"DescribeTransitGatewayConnectPeers": handleDescribeTransitGatewayConnectPeers,
		// Transit gateway Client VPN attachments
		"AcceptTransitGatewayClientVpnAttachment": handleAcceptTransitGatewayClientVpnAttachment,
		"RejectTransitGatewayClientVpnAttachment": handleRejectTransitGatewayClientVpnAttachment,
		"DeleteTransitGatewayClientVpnAttachment": handleDeleteTransitGatewayClientVpnAttachment,
		"DescribeTransitGatewayMeteringPolicies":  handleDescribeTransitGatewayMeteringPolicies,
		// VPC peering options / reject
		"ModifyVpcPeeringConnectionOptions": handleModifyVpcPeeringConnectionOptions,
		"RejectVpcPeeringConnection":        handleRejectVpcPeeringConnection,
		// Network ACL association
		"ReplaceNetworkAclAssociation": handleReplaceNetworkAclAssociation,
		// Honest-empty / template / org-sharing / visibility / encryption
		"DescribeVpcEndpointAssociations":               handleDescribeVpcEndpointAssociations,
		"GetFlowLogsIntegrationTemplate":                handleGetFlowLogsIntegrationTemplate,
		"EnableReachabilityAnalyzerOrganizationSharing": handleEnableReachabilityAnalyzerOrganizationSharing,
		"GetManagedResourceVisibility":                  handleGetManagedResourceVisibility,
		"ModifyManagedResourceVisibility":               handleModifyManagedResourceVisibility,
		"GetVpcResourcesBlockingEncryptionEnforcement":  handleGetVpcResourcesBlockingEncryptionEnforcement,
		"DescribeElasticGpus":                           handleDescribeElasticGpus,
		"DescribeOutpostLags":                           handleDescribeOutpostLags,
		"DescribeSecondaryInterfaces":                   handleDescribeSecondaryInterfaces,
		"DescribeServiceLinkVirtualInterfaces":          handleDescribeServiceLinkVirtualInterfaces,
	} {
		r.Register(action, h)
	}
}

// ec2IdFormatUseLongIds returns the stored useLongIds for a resource, defaulting
// to true (real AWS has long IDs permanently enabled for every resource type).
func ec2IdFormatUseLongIds(resource string) bool {
	if s, ok := ec2IdFormatSettings.Get(resource); ok {
		return s.UseLongIds
	}
	return true
}

// ---- ID format ----

func idFormatStatusSetXML(resources []string) string {
	var b strings.Builder
	b.WriteString("<statusSet>")
	for _, res := range resources {
		fmt.Fprintf(&b, "<item><resource>%s</resource><useLongIds>%t</useLongIds></item>", res, ec2IdFormatUseLongIds(res))
	}
	b.WriteString("</statusSet>")
	return b.String()
}

func handleDescribeIdFormat(w http.ResponseWriter, r *http.Request) {
	resources := ec2IdFormatResources
	if res := r.FormValue("Resource"); res != "" {
		resources = []string{res}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIdFormatResponse %s><requestId>%s</requestId>%s</DescribeIdFormatResponse>`,
		ec2Xmlns(), generateUUID(), idFormatStatusSetXML(resources))
}

func handleModifyIdFormat(w http.ResponseWriter, r *http.Request) {
	resource := r.FormValue("Resource")
	useLong := r.FormValue("UseLongIds") == "true"
	if resource == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter resource", http.StatusBadRequest)
		return
	}
	if resource == "all-current" {
		for _, res := range ec2IdFormatResources {
			ec2IdFormatSettings.Put(res, EC2IdFormatSetting{Resource: res, UseLongIds: useLong})
		}
	} else {
		ec2IdFormatSettings.Put(resource, EC2IdFormatSetting{Resource: resource, UseLongIds: useLong})
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIdFormatResponse %s><requestId>%s</requestId><return>true</return></ModifyIdFormatResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeIdentityIdFormat(w http.ResponseWriter, r *http.Request) {
	resources := ec2IdFormatResources
	if res := r.FormValue("Resource"); res != "" {
		resources = []string{res}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIdentityIdFormatResponse %s><requestId>%s</requestId>%s</DescribeIdentityIdFormatResponse>`,
		ec2Xmlns(), generateUUID(), idFormatStatusSetXML(resources))
}

func handleModifyIdentityIdFormat(w http.ResponseWriter, r *http.Request) {
	resource := r.FormValue("Resource")
	useLong := r.FormValue("UseLongIds") == "true"
	if resource == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter resource", http.StatusBadRequest)
		return
	}
	if resource == "all-current" {
		for _, res := range ec2IdFormatResources {
			ec2IdFormatSettings.Put(res, EC2IdFormatSetting{Resource: res, UseLongIds: useLong})
		}
	} else {
		ec2IdFormatSettings.Put(resource, EC2IdFormatSetting{Resource: resource, UseLongIds: useLong})
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIdentityIdFormatResponse %s><requestId>%s</requestId><return>true</return></ModifyIdentityIdFormatResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeAggregateIdFormat(w http.ResponseWriter, r *http.Request) {
	aggregated := true
	for _, res := range ec2IdFormatResources {
		if !ec2IdFormatUseLongIds(res) {
			aggregated = false
			break
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeAggregateIdFormatResponse %s><requestId>%s</requestId><useLongIdsAggregated>%t</useLongIdsAggregated>%s</DescribeAggregateIdFormatResponse>`,
		ec2Xmlns(), generateUUID(), aggregated, idFormatStatusSetXML(ec2IdFormatResources))
}

func handleDescribePrincipalIdFormat(w http.ResponseWriter, r *http.Request) {
	resources := ec2IdFormatResources
	if list := ec2ParamList(r, "Resource"); len(list) > 0 {
		resources = list
	}
	arn := fmt.Sprintf("arn:aws:iam::%s:root", awsAccountID())
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribePrincipalIdFormatResponse %s><requestId>%s</requestId><principalSet><item><arn>%s</arn>%s</item></principalSet></DescribePrincipalIdFormatResponse>`,
		ec2Xmlns(), generateUUID(), arn, idFormatStatusSetXML(resources))
}

// ---- EIP address attributes (PTR record) ----

// ec2AddrAttr loads the stored PTR attribute for an allocation, synthesizing one
// from the allocation's public IP if none has been set yet (real EC2 returns an
// AddressAttribute with a default reverse-DNS-shaped PtrRecord).
func ec2AddrAttr(allocID string) (EC2AddressAttribute, bool) {
	eip, ok := ec2ElasticIPs.Get(allocID)
	if !ok {
		return EC2AddressAttribute{}, false
	}
	if a, ok := ec2AddressAttributes.Get(allocID); ok {
		a.PublicIp = eip.PublicIp
		return a, true
	}
	return EC2AddressAttribute{AllocationId: allocID, PublicIp: eip.PublicIp}, true
}

func addressAttributeBodyXML(a EC2AddressAttribute, ptrUpdate string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<publicIp>%s</publicIp><allocationId>%s</allocationId>", a.PublicIp, a.AllocationId)
	if a.PtrRecord != "" {
		fmt.Fprintf(&b, "<ptrRecord>%s</ptrRecord>", a.PtrRecord)
	}
	if ptrUpdate != "" {
		fmt.Fprintf(&b, "<ptrRecordUpdate><value>%s</value><status>PENDING</status></ptrRecordUpdate>", ptrUpdate)
	}
	return b.String()
}

func handleModifyAddressAttribute(w http.ResponseWriter, r *http.Request) {
	allocID := r.FormValue("AllocationId")
	a, ok := ec2AddrAttr(allocID)
	if !ok {
		ec2ErrorXML(w, "InvalidAllocationID.NotFound", fmt.Sprintf("The allocation ID '%s' does not exist", allocID), http.StatusBadRequest)
		return
	}
	domainName := r.FormValue("DomainName")
	a.PtrRecord = domainName
	if domainName != "" && !strings.HasSuffix(domainName, ".") {
		// Real EC2 stores the PTR record with a trailing dot.
		a.PtrRecord = domainName + "."
	}
	ec2AddressAttributes.Put(allocID, a)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyAddressAttributeResponse %s><requestId>%s</requestId><address>%s</address></ModifyAddressAttributeResponse>`,
		ec2Xmlns(), generateUUID(), addressAttributeBodyXML(a, a.PtrRecord))
}

func handleResetAddressAttribute(w http.ResponseWriter, r *http.Request) {
	allocID := r.FormValue("AllocationId")
	a, ok := ec2AddrAttr(allocID)
	if !ok {
		ec2ErrorXML(w, "InvalidAllocationID.NotFound", fmt.Sprintf("The allocation ID '%s' does not exist", allocID), http.StatusBadRequest)
		return
	}
	ec2AddressAttributes.Delete(allocID)
	a.PtrRecord = ""
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ResetAddressAttributeResponse %s><requestId>%s</requestId><address>%s</address></ResetAddressAttributeResponse>`,
		ec2Xmlns(), generateUUID(), addressAttributeBodyXML(a, ""))
}

func handleMoveAddressToVpc(w http.ResponseWriter, r *http.Request) {
	publicIP := r.FormValue("PublicIp")
	var found *EC2ElasticIP
	for _, e := range ec2ElasticIPs.List() {
		if e.PublicIp == publicIP {
			ec := e
			found = &ec
			break
		}
	}
	if found == nil {
		ec2ErrorXML(w, "InvalidAddress.NotFound", fmt.Sprintf("Address '%s' not found.", publicIP), http.StatusBadRequest)
		return
	}
	ec2ElasticIPs.Update(found.AllocationId, func(e *EC2ElasticIP) { e.Domain = "vpc" })
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<MoveAddressToVpcResponse %s><requestId>%s</requestId><allocationId>%s</allocationId><status>InVpc</status></MoveAddressToVpcResponse>`,
		ec2Xmlns(), generateUUID(), found.AllocationId)
}

func handleRestoreAddressToClassic(w http.ResponseWriter, r *http.Request) {
	publicIP := r.FormValue("PublicIp")
	var found *EC2ElasticIP
	for _, e := range ec2ElasticIPs.List() {
		if e.PublicIp == publicIP {
			ec := e
			found = &ec
			break
		}
	}
	if found == nil {
		ec2ErrorXML(w, "InvalidAddress.NotFound", fmt.Sprintf("Address '%s' not found.", publicIP), http.StatusBadRequest)
		return
	}
	ec2ElasticIPs.Update(found.AllocationId, func(e *EC2ElasticIP) { e.Domain = "standard" })
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RestoreAddressToClassicResponse %s><requestId>%s</requestId><publicIp>%s</publicIp><status>InClassic</status></RestoreAddressToClassicResponse>`,
		ec2Xmlns(), generateUUID(), publicIP)
}

func handleDescribeMovingAddresses(w http.ResponseWriter, r *http.Request) {
	publicIPs := ec2ParamList(r, "PublicIp")
	var b strings.Builder
	b.WriteString("<movingAddressStatusSet>")
	for _, e := range ec2ElasticIPs.List() {
		// Only EIPs currently in the EC2-Classic domain are "moving"
		// (move-in-progress / in-classic). VPC EIPs are stable and omitted.
		if e.Domain != "standard" {
			continue
		}
		if len(publicIPs) > 0 && !ec2StrInValues(e.PublicIp, publicIPs) {
			continue
		}
		fmt.Fprintf(&b, "<item><publicIp>%s</publicIp><moveStatus>restoring-to-classic</moveStatus></item>", e.PublicIp)
	}
	b.WriteString("</movingAddressStatusSet>")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeMovingAddressesResponse %s><requestId>%s</requestId>%s</DescribeMovingAddressesResponse>`,
		ec2Xmlns(), generateUUID(), b.String())
}

// ---- ClassicLink ----

func handleAttachClassicLinkVpc(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	vpcID := r.FormValue("VpcId")
	if _, ok := ec2Instances.Get(instanceID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", instanceID), http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcID); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcID), http.StatusBadRequest)
		return
	}
	groups := ec2ParamList(r, "SecurityGroupId")
	if len(groups) == 0 {
		groups = ec2ParamList(r, "Groups")
	}
	ec2ClassicLinks.Put(instanceID, EC2ClassicLink{InstanceId: instanceID, VpcId: vpcID, GroupIds: groups})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AttachClassicLinkVpcResponse %s><requestId>%s</requestId><return>true</return></AttachClassicLinkVpcResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDetachClassicLinkVpc(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2Instances.Get(instanceID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", instanceID), http.StatusBadRequest)
		return
	}
	ec2ClassicLinks.Delete(instanceID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DetachClassicLinkVpcResponse %s><requestId>%s</requestId><return>true</return></DetachClassicLinkVpcResponse>`,
		ec2Xmlns(), generateUUID())
}

// ---- Nitro Enclave certificate ↔ IAM role ----

// enclaveCertKey is the composite store key: certificate ARN + role ARN, since
// a certificate may be associated with multiple roles.
func enclaveCertKey(certArn, roleArn string) string { return certArn + "|" + roleArn }

func handleAssociateEnclaveCertificateIamRole(w http.ResponseWriter, r *http.Request) {
	certArn := r.FormValue("CertificateArn")
	roleArn := r.FormValue("RoleArn")
	if certArn == "" || roleArn == "" {
		ec2ErrorXML(w, "MissingParameter", "CertificateArn and RoleArn are required", http.StatusBadRequest)
		return
	}
	// Real EC2 uploads the certificate bundle to an S3 bucket it manages,
	// under a deterministic role_arn/certificate_arn key, and encrypts the
	// private key with a KMS key.
	bucket := fmt.Sprintf("aws-ec2-enclave-certificate-%s", awsRegion())
	objectKey := roleArn + "/" + certArn
	kmsKey := fmt.Sprintf("arn:aws:kms:%s:%s:key/%s", awsRegion(), awsAccountID(), generateUUID())
	assoc := EC2EnclaveCertRole{
		CertificateArn:          certArn,
		RoleArn:                 roleArn,
		CertificateS3BucketName: bucket,
		CertificateS3ObjectKey:  objectKey,
		EncryptionKmsKeyId:      kmsKey,
	}
	ec2EnclaveCertRoles.Put(enclaveCertKey(certArn, roleArn), assoc)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateEnclaveCertificateIamRoleResponse %s><requestId>%s</requestId><certificateS3BucketName>%s</certificateS3BucketName><certificateS3ObjectKey>%s</certificateS3ObjectKey><encryptionKmsKeyId>%s</encryptionKmsKeyId></AssociateEnclaveCertificateIamRoleResponse>`,
		ec2Xmlns(), generateUUID(), bucket, objectKey, kmsKey)
}

func handleDisassociateEnclaveCertificateIamRole(w http.ResponseWriter, r *http.Request) {
	certArn := r.FormValue("CertificateArn")
	roleArn := r.FormValue("RoleArn")
	ec2EnclaveCertRoles.Delete(enclaveCertKey(certArn, roleArn))
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateEnclaveCertificateIamRoleResponse %s><requestId>%s</requestId><return>true</return></DisassociateEnclaveCertificateIamRoleResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleGetAssociatedEnclaveCertificateIamRoles(w http.ResponseWriter, r *http.Request) {
	certArn := r.FormValue("CertificateArn")
	var roles []EC2EnclaveCertRole
	for _, a := range ec2EnclaveCertRoles.List() {
		if a.CertificateArn == certArn {
			roles = append(roles, a)
		}
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].RoleArn < roles[j].RoleArn })
	var b strings.Builder
	b.WriteString("<associatedRoleSet>")
	for _, a := range roles {
		fmt.Fprintf(&b, "<item><associatedRoleArn>%s</associatedRoleArn><certificateS3BucketName>%s</certificateS3BucketName><certificateS3ObjectKey>%s</certificateS3ObjectKey><encryptionKmsKeyId>%s</encryptionKmsKeyId></item>",
			a.RoleArn, a.CertificateS3BucketName, a.CertificateS3ObjectKey, a.EncryptionKmsKeyId)
	}
	b.WriteString("</associatedRoleSet>")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAssociatedEnclaveCertificateIamRolesResponse %s><requestId>%s</requestId>%s</GetAssociatedEnclaveCertificateIamRolesResponse>`,
		ec2Xmlns(), generateUUID(), b.String())
}

// ---- Client VPN export/import ----

func handleExportClientVpnClientCertificateRevocationList(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	crl, ok := ec2ClientVpnCertCRLs.Get(epID)
	pem := ""
	statusCode := "pending"
	if ok && crl.Pem != "" {
		pem = crl.Pem
		statusCode = "active"
	}
	w.Header().Set("Content-Type", "text/xml")
	if pem != "" {
		fmt.Fprintf(w, `<ExportClientVpnClientCertificateRevocationListResponse %s><requestId>%s</requestId><certificateRevocationList>%s</certificateRevocationList><status><code>%s</code></status></ExportClientVpnClientCertificateRevocationListResponse>`,
			ec2Xmlns(), generateUUID(), pem, statusCode)
	} else {
		fmt.Fprintf(w, `<ExportClientVpnClientCertificateRevocationListResponse %s><requestId>%s</requestId><status><code>%s</code></status></ExportClientVpnClientCertificateRevocationListResponse>`,
			ec2Xmlns(), generateUUID(), statusCode)
	}
}

func handleImportClientVpnClientCertificateRevocationList(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	if _, ok := ec2ClientVpnEndpoint.Get(epID); !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	pem := r.FormValue("CertificateRevocationList")
	ec2ClientVpnCertCRLs.Put(epID, EC2ClientVpnCertRevocationList{ClientVpnEndpointId: epID, Pem: pem})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ImportClientVpnClientCertificateRevocationListResponse %s><requestId>%s</requestId><return>true</return></ImportClientVpnClientCertificateRevocationListResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleExportClientVpnClientConfiguration(w http.ResponseWriter, r *http.Request) {
	epID := r.FormValue("ClientVpnEndpointId")
	ep, ok := ec2ClientVpnEndpoint.Get(epID)
	if !ok {
		ec2ErrorXML(w, "InvalidClientVpnEndpointId.NotFound", fmt.Sprintf("The Client VPN endpoint '%s' does not exist", epID), http.StatusBadRequest)
		return
	}
	// Real EC2 returns an OpenVPN .ovpn config file referencing the endpoint's
	// DNS name, port, and transport protocol. Build a deterministic one from the
	// endpoint's stored fields.
	dnsName := ep.DnsName
	if dnsName == "" {
		dnsName = fmt.Sprintf("%s.clientvpn.%s.amazonaws.com", strings.TrimPrefix(epID, "cvpn-endpoint-"), awsRegion())
	}
	proto := ep.TransportProtocol
	if proto == "" {
		proto = "udp"
	}
	port := ep.VpnPort
	if port == 0 {
		port = 443
	}
	cfg := fmt.Sprintf("client\ndev tun\nproto %s\nremote %s %d\nremote-random-hostname\nresolv-retry infinite\nnobind\nremote-cert-tls server\ncipher AES-256-GCM\nverb 3\n", proto, dnsName, port)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ExportClientVpnClientConfigurationResponse %s><requestId>%s</requestId><clientConfiguration>%s</clientConfiguration></ExportClientVpnClientConfigurationResponse>`,
		ec2Xmlns(), generateUUID(), ec2EscapeXML(cfg))
}

// ec2EscapeXML escapes a string for embedding as XML character data.
func ec2EscapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// ---- Site-to-Site VPN tunnel replacement / certificate ----

func handleGetVpnTunnelReplacementStatus(w http.ResponseWriter, r *http.Request) {
	vpnID := r.FormValue("VpnConnectionId")
	conn, ok := ec2VpnConnections.Get(vpnID)
	if !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", vpnID), http.StatusBadRequest)
		return
	}
	outsideIP := r.FormValue("VpnTunnelOutsideIpAddress")
	if outsideIP == "" && len(conn.Tunnels) > 0 {
		outsideIP = conn.Tunnels[0].OutsideIpAddress
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<vpnConnectionId>%s</vpnConnectionId>", conn.VpnConnectionId)
	if conn.TransitGatewayId != "" {
		fmt.Fprintf(&b, "<transitGatewayId>%s</transitGatewayId>", conn.TransitGatewayId)
	}
	if conn.CustomerGatewayId != "" {
		fmt.Fprintf(&b, "<customerGatewayId>%s</customerGatewayId>", conn.CustomerGatewayId)
	}
	if conn.VpnGatewayId != "" {
		fmt.Fprintf(&b, "<vpnGatewayId>%s</vpnGatewayId>", conn.VpnGatewayId)
	}
	fmt.Fprintf(&b, "<vpnTunnelOutsideIpAddress>%s</vpnTunnelOutsideIpAddress>", outsideIP)
	b.WriteString("<maintenanceDetails></maintenanceDetails>")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetVpnTunnelReplacementStatusResponse %s><requestId>%s</requestId>%s</GetVpnTunnelReplacementStatusResponse>`,
		ec2Xmlns(), generateUUID(), b.String())
}

func handleModifyVpnTunnelCertificate(w http.ResponseWriter, r *http.Request) {
	vpnID := r.FormValue("VpnConnectionId")
	outsideIP := r.FormValue("VpnTunnelOutsideIpAddress")
	if _, ok := ec2VpnConnections.Get(vpnID); !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", vpnID), http.StatusBadRequest)
		return
	}
	// Rotating a tunnel certificate puts the matching tunnel into a transient
	// rekeying state; we model that by flipping its status to DOWN momentarily.
	ec2VpnConnections.Update(vpnID, func(c *EC2VpnConnection) {
		for i := range c.Tunnels {
			if c.Tunnels[i].OutsideIpAddress == outsideIP {
				c.Tunnels[i].Status = "DOWN"
			}
		}
	})
	conn, _ := ec2VpnConnections.Get(vpnID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpnTunnelCertificateResponse %s><requestId>%s</requestId><vpnConnection>%s</vpnConnection></ModifyVpnTunnelCertificateResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpnConnectionFieldsXML(conn))
}

func handleReplaceVpnTunnel(w http.ResponseWriter, r *http.Request) {
	vpnID := r.FormValue("VpnConnectionId")
	outsideIP := r.FormValue("VpnTunnelOutsideIpAddress")
	if _, ok := ec2VpnConnections.Get(vpnID); !ok {
		ec2ErrorXML(w, "InvalidVpnConnectionID.NotFound", fmt.Sprintf("The vpnConnection ID '%s' does not exist", vpnID), http.StatusBadRequest)
		return
	}
	ec2VpnConnections.Update(vpnID, func(c *EC2VpnConnection) {
		for i := range c.Tunnels {
			if c.Tunnels[i].OutsideIpAddress == outsideIP {
				c.Tunnels[i].Status = "DOWN"
			}
		}
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceVpnTunnelResponse %s><requestId>%s</requestId><return>true</return></ReplaceVpnTunnelResponse>`,
		ec2Xmlns(), generateUUID())
}

// ---- Transit gateway Connect peer ----

func tgwConnectPeerBodyXML(p EC2TransitGatewayConnectPeer) string {
	var inside strings.Builder
	inside.WriteString("<insideCidrBlocks>")
	for _, c := range p.InsideCidrBlocks {
		fmt.Fprintf(&inside, "<item>%s</item>", c)
	}
	inside.WriteString("</insideCidrBlocks>")
	cfg := fmt.Sprintf("<connectPeerConfiguration><transitGatewayAddress>%s</transitGatewayAddress><peerAddress>%s</peerAddress>%s<protocol>%s</protocol><bgpConfigurations><item><transitGatewayAsn>%d</transitGatewayAsn><peerAsn>%d</peerAsn><transitGatewayAddress>%s</transitGatewayAddress><peerAddress>%s</peerAddress><bgpStatus>up</bgpStatus></item></bgpConfigurations></connectPeerConfiguration>",
		p.TransitGatewayAddress, p.PeerAddress, inside.String(), p.Protocol, p.TransitGatewayAsn, p.PeerAsn, p.TransitGatewayAddress, p.PeerAddress)
	return fmt.Sprintf("<transitGatewayConnectPeerId>%s</transitGatewayConnectPeerId><transitGatewayAttachmentId>%s</transitGatewayAttachmentId><state>%s</state><creationTime>%s</creationTime>%s%s",
		p.TransitGatewayConnectPeerId, p.TransitGatewayAttachmentId, p.State, p.CreationTime, cfg, writeTagSetXML(p.Tags))
}

func handleCreateTransitGatewayConnectPeer(w http.ResponseWriter, r *http.Request) {
	attachID := r.FormValue("TransitGatewayAttachmentId")
	connect, ok := ec2TGWConnects.Get(attachID)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayAttachmentID.NotFound", fmt.Sprintf("The transitGatewayAttachment ID '%s' does not exist", attachID), http.StatusBadRequest)
		return
	}
	peerAddress := r.FormValue("PeerAddress")
	tgwAddress := r.FormValue("TransitGatewayAddress")
	if tgwAddress == "" {
		tgwAddress = "169.254.6.1"
	}
	inside := ec2ParamList(r, "InsideCidrBlocks")
	if len(inside) == 0 {
		inside = []string{"169.254.6.0/29"}
	}
	peerAsn := int64(64512)
	if v := r.FormValue("BgpOptions.PeerAsn"); v != "" {
		peerAsn, _ = strconv.ParseInt(v, 10, 64)
	}
	tgwAsn := int64(64512)
	if tgw, ok := ec2TransitGateways.Get(connect.TransitGatewayId); ok && tgw.AmazonSideAsn != 0 {
		tgwAsn = tgw.AmazonSideAsn
	}
	id := ec2ID("tgw-connect-peer")
	p := EC2TransitGatewayConnectPeer{
		TransitGatewayConnectPeerId: id,
		TransitGatewayAttachmentId:  attachID,
		State:                       "available",
		CreationTime:                ec2NowRFC3339Milli(),
		TransitGatewayAddress:       tgwAddress,
		PeerAddress:                 peerAddress,
		InsideCidrBlocks:            inside,
		Protocol:                    "gre",
		PeerAsn:                     peerAsn,
		TransitGatewayAsn:           tgwAsn,
		Tags:                        parseTags(r),
	}
	ec2TGWConnectPeers.Put(id, p)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateTransitGatewayConnectPeerResponse %s><requestId>%s</requestId><transitGatewayConnectPeer>%s</transitGatewayConnectPeer></CreateTransitGatewayConnectPeerResponse>`,
		ec2Xmlns(), generateUUID(), tgwConnectPeerBodyXML(p))
}

func handleDeleteTransitGatewayConnectPeer(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TransitGatewayConnectPeerId")
	p, ok := ec2TGWConnectPeers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTransitGatewayConnectPeerID.NotFound", fmt.Sprintf("The transitGatewayConnectPeer ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	p.State = "deleted"
	ec2TGWConnectPeers.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteTransitGatewayConnectPeerResponse %s><requestId>%s</requestId><transitGatewayConnectPeer>%s</transitGatewayConnectPeer></DeleteTransitGatewayConnectPeerResponse>`,
		ec2Xmlns(), generateUUID(), tgwConnectPeerBodyXML(p))
}

func handleDescribeTransitGatewayConnectPeers(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TransitGatewayConnectPeerIds")
	filters := ec2Filters(r)
	list := ec2TGWConnectPeers.List()
	sort.Slice(list, func(i, j int) bool {
		return list[i].TransitGatewayConnectPeerId < list[j].TransitGatewayConnectPeerId
	})
	var b strings.Builder
	b.WriteString("<transitGatewayConnectPeerSet>")
	for _, p := range list {
		if len(ids) > 0 && !ec2StrInValues(p.TransitGatewayConnectPeerId, ids) {
			continue
		}
		if !tgwConnectPeerMatchesFilters(p, filters) {
			continue
		}
		fmt.Fprintf(&b, "<item>%s</item>", tgwConnectPeerBodyXML(p))
	}
	b.WriteString("</transitGatewayConnectPeerSet>")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeTransitGatewayConnectPeersResponse %s><requestId>%s</requestId>%s</DescribeTransitGatewayConnectPeersResponse>`,
		ec2Xmlns(), generateUUID(), b.String())
}

func tgwConnectPeerMatchesFilters(p EC2TransitGatewayConnectPeer, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "transit-gateway-attachment-id":
			if !ec2StrInValues(p.TransitGatewayAttachmentId, vals) {
				return false
			}
		case "transit-gateway-connect-peer-id":
			if !ec2StrInValues(p.TransitGatewayConnectPeerId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(p.State, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, p.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

// ---- Transit gateway Client VPN attachments ----
//
// The sim does not model a separate transit-gateway client-VPN attachment
// store: a Client VPN endpoint associated to a transit gateway is represented
// by the endpoint itself. These accept/reject/delete/describe operations
// transition the requested attachment id and return the canonical
// TransitGatewayClientVpnAttachment shape.

func tgwClientVpnAttachmentXML(attachID, state string) string {
	return fmt.Sprintf("<transitGatewayAttachmentId>%s</transitGatewayAttachmentId><state>%s</state><creationTime>%s</creationTime>",
		attachID, state, ec2NowRFC3339Milli())
}

func handleAcceptTransitGatewayClientVpnAttachment(w http.ResponseWriter, r *http.Request) {
	attachID := r.FormValue("TransitGatewayAttachmentId")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AcceptTransitGatewayClientVpnAttachmentResponse %s><requestId>%s</requestId><transitGatewayClientVpnAttachment>%s</transitGatewayClientVpnAttachment></AcceptTransitGatewayClientVpnAttachmentResponse>`,
		ec2Xmlns(), generateUUID(), tgwClientVpnAttachmentXML(attachID, "available"))
}

func handleRejectTransitGatewayClientVpnAttachment(w http.ResponseWriter, r *http.Request) {
	attachID := r.FormValue("TransitGatewayAttachmentId")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RejectTransitGatewayClientVpnAttachmentResponse %s><requestId>%s</requestId><transitGatewayClientVpnAttachment>%s</transitGatewayClientVpnAttachment></RejectTransitGatewayClientVpnAttachmentResponse>`,
		ec2Xmlns(), generateUUID(), tgwClientVpnAttachmentXML(attachID, "rejected"))
}

func handleDeleteTransitGatewayClientVpnAttachment(w http.ResponseWriter, r *http.Request) {
	attachID := r.FormValue("TransitGatewayAttachmentId")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteTransitGatewayClientVpnAttachmentResponse %s><requestId>%s</requestId><transitGatewayClientVpnAttachment>%s</transitGatewayClientVpnAttachment></DeleteTransitGatewayClientVpnAttachmentResponse>`,
		ec2Xmlns(), generateUUID(), tgwClientVpnAttachmentXML(attachID, "deleted"))
}

func handleDescribeTransitGatewayMeteringPolicies(w http.ResponseWriter, r *http.Request) {
	// The sim creates no transit-gateway metering policies, so the set is
	// honestly empty.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeTransitGatewayMeteringPoliciesResponse %s><requestId>%s</requestId><transitGatewayMeteringPolicies/></DescribeTransitGatewayMeteringPoliciesResponse>`,
		ec2Xmlns(), generateUUID())
}

// ---- VPC peering options / reject ----

func parsePeeringOptions(r *http.Request, prefix string) (dns, classicOut, vpcToClassic bool, present bool) {
	if v := r.FormValue(prefix + ".AllowDnsResolutionFromRemoteVpc"); v != "" {
		dns = v == "true"
		present = true
	}
	if v := r.FormValue(prefix + ".AllowEgressFromLocalClassicLinkToRemoteVpc"); v != "" {
		classicOut = v == "true"
		present = true
	}
	if v := r.FormValue(prefix + ".AllowEgressFromLocalVpcToRemoteClassicLink"); v != "" {
		vpcToClassic = v == "true"
		present = true
	}
	return
}

func handleModifyVpcPeeringConnectionOptions(w http.ResponseWriter, r *http.Request) {
	pcxID := r.FormValue("VpcPeeringConnectionId")
	if _, ok := ec2VpcPeerings.Get(pcxID); !ok {
		ec2ErrorXML(w, "InvalidVpcPeeringConnectionID.NotFound", fmt.Sprintf("The vpcPeeringConnection ID '%s' does not exist", pcxID), http.StatusBadRequest)
		return
	}
	opts, _ := ec2VpcPeeringOptionsDB.Get(pcxID)
	opts.VpcPeeringConnectionId = pcxID
	if aDns, aClassic, aVpc, present := parsePeeringOptions(r, "AccepterPeeringConnectionOptions"); present {
		opts.AccepterAllowDnsResolutionFromRemoteVpc = aDns
		opts.AccepterAllowEgressLocalClassicLinkToRemoteVpc = aClassic
		opts.AccepterAllowEgressLocalVpcToRemoteClassicLink = aVpc
	}
	if rDns, rClassic, rVpc, present := parsePeeringOptions(r, "RequesterPeeringConnectionOptions"); present {
		opts.RequesterAllowDnsResolutionFromRemoteVpc = rDns
		opts.RequesterAllowEgressLocalClassicLinkToRemoteVpc = rClassic
		opts.RequesterAllowEgressLocalVpcToRemoteClassicLink = rVpc
	}
	ec2VpcPeeringOptionsDB.Put(pcxID, opts)
	accepter := fmt.Sprintf("<accepterPeeringConnectionOptions><allowDnsResolutionFromRemoteVpc>%t</allowDnsResolutionFromRemoteVpc><allowEgressFromLocalClassicLinkToRemoteVpc>%t</allowEgressFromLocalClassicLinkToRemoteVpc><allowEgressFromLocalVpcToRemoteClassicLink>%t</allowEgressFromLocalVpcToRemoteClassicLink></accepterPeeringConnectionOptions>",
		opts.AccepterAllowDnsResolutionFromRemoteVpc, opts.AccepterAllowEgressLocalClassicLinkToRemoteVpc, opts.AccepterAllowEgressLocalVpcToRemoteClassicLink)
	requester := fmt.Sprintf("<requesterPeeringConnectionOptions><allowDnsResolutionFromRemoteVpc>%t</allowDnsResolutionFromRemoteVpc><allowEgressFromLocalClassicLinkToRemoteVpc>%t</allowEgressFromLocalClassicLinkToRemoteVpc><allowEgressFromLocalVpcToRemoteClassicLink>%t</allowEgressFromLocalVpcToRemoteClassicLink></requesterPeeringConnectionOptions>",
		opts.RequesterAllowDnsResolutionFromRemoteVpc, opts.RequesterAllowEgressLocalClassicLinkToRemoteVpc, opts.RequesterAllowEgressLocalVpcToRemoteClassicLink)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcPeeringConnectionOptionsResponse %s><requestId>%s</requestId>%s%s</ModifyVpcPeeringConnectionOptionsResponse>`,
		ec2Xmlns(), generateUUID(), accepter, requester)
}

func handleRejectVpcPeeringConnection(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpcPeeringConnectionId")
	if _, ok := ec2VpcPeerings.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpcPeeringConnectionID.NotFound", fmt.Sprintf("The vpcPeeringConnection ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VpcPeerings.Update(id, func(p *EC2VpcPeeringConnection) {
		p.StatusCode = "rejected"
		p.StatusMessage = "Rejected"
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RejectVpcPeeringConnectionResponse %s><requestId>%s</requestId><return>true</return></RejectVpcPeeringConnectionResponse>`,
		ec2Xmlns(), generateUUID())
}

// ---- Network ACL association ----

func handleReplaceNetworkAclAssociation(w http.ResponseWriter, r *http.Request) {
	assocID := r.FormValue("AssociationId")
	newACLID := r.FormValue("NetworkAclId")
	if _, ok := ec2NetworkAcls.Get(newACLID); !ok {
		ec2ErrorXML(w, "InvalidNetworkAclID.NotFound", fmt.Sprintf("The networkAcl ID '%s' does not exist", newACLID), http.StatusBadRequest)
		return
	}
	// Find the ACL that currently owns the association, detach it, and re-attach
	// the underlying subnet to the new ACL under a fresh association id —
	// exactly the swap real EC2 performs.
	var subnetID string
	for _, acl := range ec2NetworkAcls.List() {
		for _, a := range acl.Associations {
			if a.NetworkAclAssociationId == assocID {
				subnetID = a.SubnetId
				ec2NetworkAcls.Update(acl.NetworkAclId, func(cur *EC2NetworkAcl) {
					next := make([]EC2NetworkAclAssociation, 0, len(cur.Associations))
					for _, x := range cur.Associations {
						if x.NetworkAclAssociationId != assocID {
							next = append(next, x)
						}
					}
					cur.Associations = next
				})
			}
		}
	}
	newAssocID := ec2ID("aclassoc")
	ec2NetworkAcls.Update(newACLID, func(cur *EC2NetworkAcl) {
		cur.Associations = append(cur.Associations, EC2NetworkAclAssociation{
			NetworkAclAssociationId: newAssocID,
			NetworkAclId:            newACLID,
			SubnetId:                subnetID,
		})
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceNetworkAclAssociationResponse %s><requestId>%s</requestId><newAssociationId>%s</newAssociationId></ReplaceNetworkAclAssociationResponse>`,
		ec2Xmlns(), generateUUID(), newAssocID)
}

// ---- Honest-empty / template / org-sharing / visibility / encryption ----

func handleDescribeVpcEndpointAssociations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcEndpointAssociationsResponse %s><requestId>%s</requestId><vpcEndpointAssociationSet/></DescribeVpcEndpointAssociationsResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleGetFlowLogsIntegrationTemplate(w http.ResponseWriter, r *http.Request) {
	// Real EC2 returns a CloudFormation template that wires the flow logs into
	// Athena. Build a minimal deterministic, valid CloudFormation document.
	tmpl := `{"AWSTemplateFormatVersion":"2010-09-09","Description":"VPC Flow Logs Athena integration","Resources":{}}`
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetFlowLogsIntegrationTemplateResponse %s><requestId>%s</requestId><result>%s</result></GetFlowLogsIntegrationTemplateResponse>`,
		ec2Xmlns(), generateUUID(), ec2EscapeXML(tmpl))
}

func handleEnableReachabilityAnalyzerOrganizationSharing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableReachabilityAnalyzerOrganizationSharingResponse %s><requestId>%s</requestId><returnValue>true</returnValue></EnableReachabilityAnalyzerOrganizationSharingResponse>`,
		ec2Xmlns(), generateUUID())
}

// ec2ManagedResourceVisibility loads the account's managed-resource default
// visibility, defaulting to "visible" (real EC2's default).
func ec2ManagedResourceVisibility() string {
	if s, ok := ec2ManagedResourceVis.Get("default"); ok && s.Resource != "" {
		return s.Resource
	}
	return "visible"
}

func handleGetManagedResourceVisibility(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetManagedResourceVisibilityResponse %s><requestId>%s</requestId><visibility><defaultVisibility>%s</defaultVisibility></visibility></GetManagedResourceVisibilityResponse>`,
		ec2Xmlns(), generateUUID(), ec2ManagedResourceVisibility())
}

func handleModifyManagedResourceVisibility(w http.ResponseWriter, r *http.Request) {
	vis := r.FormValue("DefaultVisibility")
	if vis != "hidden" && vis != "visible" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Invalid value '%s' for DefaultVisibility", vis), http.StatusBadRequest)
		return
	}
	ec2ManagedResourceVis.Put("default", EC2IdFormatSetting{Resource: vis})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyManagedResourceVisibilityResponse %s><requestId>%s</requestId><visibility><defaultVisibility>%s</defaultVisibility></visibility></ModifyManagedResourceVisibilityResponse>`,
		ec2Xmlns(), generateUUID(), vis)
}

func handleGetVpcResourcesBlockingEncryptionEnforcement(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetVpcResourcesBlockingEncryptionEnforcementResponse %s><requestId>%s</requestId><nonCompliantResourceSet/></GetVpcResourcesBlockingEncryptionEnforcementResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeElasticGpus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeElasticGpusResponse %s><requestId>%s</requestId><elasticGpuSet/></DescribeElasticGpusResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeOutpostLags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeOutpostLagsResponse %s><requestId>%s</requestId><outpostLagSet/></DescribeOutpostLagsResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeSecondaryInterfaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSecondaryInterfacesResponse %s><requestId>%s</requestId><secondaryInterfaceSet/></DescribeSecondaryInterfacesResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeServiceLinkVirtualInterfaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeServiceLinkVirtualInterfacesResponse %s><requestId>%s</requestId><serviceLinkVirtualInterfaceSet/></DescribeServiceLinkVirtualInterfacesResponse>`,
		ec2Xmlns(), generateUUID())
}
