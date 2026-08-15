package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	dockerclient "github.com/moby/moby/client"
)

// EC2 types

type EC2Vpc struct {
	VpcId                            string
	CidrBlock                        string
	State                            string
	Tags                             []EC2Tag
	OwnerId                          string
	IsDefault                        bool
	InstanceTenancy                  string
	DhcpOptionsId                    string
	EnableDnsSupport                 bool
	EnableDnsHostnames               bool
	EnableNetworkAddressUsageMetrics bool
}

type EC2Subnet struct {
	SubnetId                                string
	VpcId                                   string
	CidrBlock                               string
	Ipv6CidrBlock                           string
	AvailabilityZone                        string
	AvailabilityZoneId                      string
	State                                   string
	Tags                                    []EC2Tag
	MapPublicIpOnLaunch                     bool
	MapCustomerOwnedIpOnLaunch              bool
	AssignIpv6AddressOnCreation             bool
	EnableDns64                             bool
	Ipv6Native                              bool
	PrivateDnsHostnameTypeOnLaunch          string
	EnableResourceNameDnsARecordOnLaunch    bool
	EnableResourceNameDnsAAAARecordOnLaunch bool
	OwnerId                                 string
}

type EC2InternetGateway struct {
	InternetGatewayId string
	Attachments       []EC2IGWAttachment
	Tags              []EC2Tag
	OwnerId           string
}

type EC2IGWAttachment struct {
	VpcId string
	State string
}

type EC2NatGateway struct {
	NatGatewayId        string
	SubnetId            string
	AllocationId        string
	VpcId               string
	State               string
	ConnectivityType    string
	Tags                []EC2Tag
	NatGatewayAddresses []EC2NatGatewayAddress
	CreateTime          string
}

type EC2NatGatewayAddress struct {
	AllocationId       string
	PublicIp           string
	PrivateIp          string
	NetworkInterfaceId string
	AssociationId      string
	IsPrimary          bool
	Status             string
}

type EC2ElasticIP struct {
	AllocationId       string
	PublicIp           string
	Domain             string
	InstanceId         string
	NetworkInterfaceId string
	PrivateIpAddress   string
	AssociationId      string
	NetworkBorderGroup string
	PublicIpv4Pool     string
	Tags               []EC2Tag
}

type EC2RouteTable struct {
	RouteTableId string
	VpcId        string
	Routes       []EC2Route
	Tags         []EC2Tag
	OwnerId      string
	Associations []EC2RouteTableAssociation
}

type EC2Route struct {
	DestinationCidrBlock        string
	DestinationIpv6CidrBlock    string
	DestinationPrefixListId     string
	GatewayId                   string
	NatGatewayId                string
	NetworkInterfaceId          string
	InstanceId                  string
	VpcPeeringConnectionId      string
	TransitGatewayId            string
	EgressOnlyInternetGatewayId string
	LocalGatewayId              string
	CarrierGatewayId            string
	VpcEndpointId               string
	CoreNetworkArn              string
	State                       string
	Origin                      string
}

type EC2RouteTableAssociation struct {
	AssociationId string
	RouteTableId  string
	SubnetId      string
	GatewayId     string
	Main          bool
}

type EC2SecurityGroup struct {
	GroupId             string
	GroupName           string
	Description         string
	VpcId               string
	Tags                []EC2Tag
	OwnerId             string
	IpPermissions       []EC2IpPermission
	IpPermissionsEgress []EC2IpPermission
}

type EC2IpPermission struct {
	IpProtocol       string
	FromPort         int
	ToPort           int
	IpRanges         []EC2IpRange
	Ipv6Ranges       []EC2Ipv6Range
	PrefixListIds    []EC2PrefixListId
	UserIdGroupPairs []EC2UserIdGroupPair
}

type EC2PrefixListId struct {
	PrefixListId string
	Description  string
}

type EC2IpRange struct {
	CidrIp      string
	Description string
}

type EC2Ipv6Range struct {
	CidrIpv6    string
	Description string
}

type EC2UserIdGroupPair struct {
	GroupId     string
	Description string
}

type EC2SecurityGroupRule struct {
	RuleId       string
	GroupId      string
	GroupOwner   string
	IsEgress     bool
	IpProtocol   string
	FromPort     int
	ToPort       int
	CidrIpv4     string
	CidrIpv6     string
	PrefixListId string
	RefGroupId   string
	Description  string
	Tags         []EC2Tag
}

type EC2Tag struct {
	Key   string
	Value string
}

type EC2Instance struct {
	InstanceId             string
	ReservationId          string
	ImageId                string
	InstanceType           string
	SubnetId               string
	VpcId                  string
	State                  string
	PrivateIpAddress       string
	PublicIpAddress        string
	SecurityGroupIds       []string
	Tags                   []EC2Tag
	LaunchTime             string
	KeyName                string
	Architecture           string
	RootDeviceName         string
	NetworkInterfaceId     string
	IamInstanceProfileArn  string
	IamInstanceProfileName string
	EbsOptimized           bool
	Monitoring             bool
	UserData               string
	DisableApiTermination  bool
	SourceDestCheck        bool
	CpuCoreCount           int
	CpuThreadsPerCore      int
	RootVolumeSize         int
	RootVolumeType         string
	MetadataHttpTokens     string
	MetadataHttpEndpoint   string
	MetadataHopLimit       int
	MetadataInstanceTags   string
	// StateTransitionReason is the free-text reason for the most recent
	// state transition (the DescribeInstances <reason> element); empty
	// while the instance is running.
	StateTransitionReason string
	// StateReasonCode/StateReasonMessage carry the structured stateReason
	// (e.g. Client.UserInitiatedShutdown) a real DescribeInstances returns
	// for stopped and terminated instances.
	StateReasonCode    string
	StateReasonMessage string
}

type EC2NetworkInterface struct {
	NetworkInterfaceId  string
	SubnetId            string
	VpcId               string
	PrivateIpAddress    string
	Status              string
	AttachmentId        string
	InstanceId          string
	DeviceIndex         int
	DeleteOnTermination bool
	SourceDestDisabled  bool
	Description         string
	SecondaryPrivateIps []string
	SecurityGroupIds    []string
	InterfaceType       string
	Tags                []EC2Tag
	OwnerId             string
}

type EC2Volume struct {
	VolumeId           string
	Size               int
	SnapshotId         string
	AvailabilityZone   string
	State              string
	CreateTime         string
	VolumeType         string
	Iops               int
	Throughput         int
	KmsKeyId           string
	Encrypted          bool
	MultiAttachEnabled bool
	Tags               []EC2Tag
	Attachments        []EC2VolumeAttachment
	HostPath           string
	DockerVolumeName   string
	Data               []byte
}

type EC2VolumeModification struct {
	VolumeId           string
	ModificationState  string
	TargetSize         int
	TargetVolumeType   string
	TargetIops         int
	TargetThroughput   int
	OriginalSize       int
	OriginalVolumeType string
	OriginalIops       int
	OriginalThroughput int
	StartTime          string
	EndTime            string
}

type EC2VolumeAttachment struct {
	VolumeId            string
	InstanceId          string
	Device              string
	State               string
	AttachTime          string
	DeleteOnTermination bool
}

type EC2Snapshot struct {
	SnapshotId       string
	VolumeId         string
	VolumeSize       int
	State            string
	StartTime        string
	CompletionDue    string
	Progress         string
	Description      string
	OwnerId          string
	Encrypted        bool
	KmsKeyId         string
	Tags             []EC2Tag
	HostPath         string
	DockerVolumeName string
	VolumeData       []byte
}

// EC2RunInstancesToken records the reservation a RunInstances ClientToken
// produced, keyed by the token, so a retried call replays the same instances.
// LaunchInstances is the exact set the original call returned, captured at
// launch, so a retried RunInstances replays that original response verbatim —
// including the "pending" state every instance reported at launch — rather than
// re-reading the control plane, which may have since transitioned them to
// "running".
type EC2RunInstancesToken struct {
	Token           string
	ReservationId   string
	InstanceIds     []string
	LaunchInstances []EC2Instance
}

// State stores
var (
	ec2Vpcs               sim.Store[EC2Vpc]
	ec2Subnets            sim.Store[EC2Subnet]
	ec2InternetGateways   sim.Store[EC2InternetGateway]
	ec2NatGateways        sim.Store[EC2NatGateway]
	ec2VpcEndpoints       sim.Store[EC2VpcEndpoint]
	ec2ElasticIPs         sim.Store[EC2ElasticIP]
	ec2RouteTables        sim.Store[EC2RouteTable]
	ec2SecurityGroups     sim.Store[EC2SecurityGroup]
	ec2SecurityGroupRules sim.Store[EC2SecurityGroupRule]
	ec2Instances          sim.Store[EC2Instance]
	ec2NetworkInterfaces  sim.Store[EC2NetworkInterface]
	ec2Volumes            sim.Store[EC2Volume]
	ec2VolumeMods         sim.Store[EC2VolumeModification]
	ec2Snapshots          sim.Store[EC2Snapshot]
	// ec2RunTokens records the reservation each RunInstances ClientToken
	// created, so a retried RunInstances replays the original instances
	// instead of launching a duplicate batch (real EC2 ClientToken
	// idempotency; the aws-sdk-go-v2 auto-fills and re-sends ClientToken on
	// every retry).
	ec2RunTokens sim.Store[EC2RunInstancesToken]
	// ec2SubnetIPCursor durably tracks the next host offset to hand out per
	// subnet for AllocateSubnetIP. Amazon EC2 maintains this allocation state
	// across control-plane restarts; persisting the cursor prevents a restarted
	// simulator from assigning an address that is already in use.
	ec2SubnetIPCursor sim.Store[uint32]
	// ec2SubnetIPAllocationMu serializes cursor advancement with the live
	// allocation scan. Resource stores remain the durable source of truth for
	// address ownership, while the cursor only chooses where the next scan
	// starts.
	ec2SubnetIPAllocationMu sync.Mutex
)

// AllocateSubnetIP picks the next free host address from a subnet's CIDR
// block. Active cloud resources are the durable allocation ledger, so an
// address returns to the pool when its network interface, NAT gateway, or
// Amazon ECS task is deleted or stopped. The durable cursor only determines
// where the circular search begins.
//
// Returns an error if the subnet isn't registered (matches real AWS, where
// RunTask / CreateNetworkInterface against a non-existent subnet returns
// InvalidSubnetID.NotFound). The first four addresses (network + AWS-reserved
// router/DNS/future) and the last (broadcast) are skipped, mirroring AWS's
// reserved-host convention.
func AllocateSubnetIP(subnetID string) (string, error) {
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return "", fmt.Errorf("subnet %q not found", subnetID)
	}
	_, cidr, perr := net.ParseCIDR(subnet.CidrBlock)
	if perr != nil {
		return "", fmt.Errorf("subnet %q has invalid CidrBlock %q: %v", subnetID, subnet.CidrBlock, perr)
	}
	ones, bits := cidr.Mask.Size()
	hostBits := bits - ones
	if hostBits < 3 {
		return "", fmt.Errorf("subnet %q CIDR %q too small for AWS host reservations", subnetID, subnet.CidrBlock)
	}
	maxHosts := uint32(1) << uint32(hostBits)
	base := cidr.IP.To4()
	if base == nil {
		return "", fmt.Errorf("subnet %q CidrBlock %q is not IPv4", subnetID, subnet.CidrBlock)
	}

	ec2SubnetIPAllocationMu.Lock()
	defer ec2SubnetIPAllocationMu.Unlock()

	cursor, ok := ec2SubnetIPCursor.Get(subnetID)
	if !ok || cursor < 4 || cursor >= maxHosts-1 {
		// AWS reserves the first four host addresses in every subnet
		// (.0 network, .1 router, .2 DNS, .3 future use).
		cursor = 4
	}
	allocated := ec2AllocatedSubnetIPv4(subnetID)
	usableHosts := maxHosts - 5
	for checked := uint32(0); checked < usableHosts; checked++ {
		offset := cursor
		cursor++
		if cursor >= maxHosts-1 {
			cursor = 4
		}
		ip := subnetIPv4AtOffset(base, offset)
		if _, inUse := allocated[ip]; inUse {
			continue
		}
		ec2SubnetIPCursor.Put(subnetID, cursor)
		return ip, nil
	}
	return "", fmt.Errorf("subnet %q exhausted: no host addresses left in %q", subnetID, subnet.CidrBlock)
}

func subnetIPv4AtOffset(base net.IP, offset uint32) string {
	hostInt := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	hostInt += offset
	return net.IPv4(
		byte(hostInt>>24),
		byte(hostInt>>16),
		byte(hostInt>>8),
		byte(hostInt),
	).String()
}

// ec2AllocatedSubnetIPv4 derives current address ownership from the durable
// cloud resources that own the addresses. A stopped Amazon ECS task no longer
// owns its elastic-network-interface address, matching Fargate lifecycle
// semantics even though the stopped task record remains describable.
func ec2AllocatedSubnetIPv4(subnetID string) map[string]struct{} {
	allocated := make(map[string]struct{})
	add := func(ip string) {
		if ip != "" {
			allocated[ip] = struct{}{}
		}
	}
	if ec2NetworkInterfaces != nil {
		for _, eni := range ec2NetworkInterfaces.List() {
			if eni.SubnetId != subnetID {
				continue
			}
			add(eni.PrivateIpAddress)
			for _, ip := range eni.SecondaryPrivateIps {
				add(ip)
			}
		}
	}
	if ec2NatGateways != nil {
		for _, gateway := range ec2NatGateways.List() {
			if gateway.SubnetId != subnetID {
				continue
			}
			for _, address := range gateway.NatGatewayAddresses {
				add(address.PrivateIp)
			}
		}
	}
	if ecsTasks != nil {
		for _, task := range ecsTasks.List() {
			if task.LastStatus == ECSTaskStatusStopped {
				continue
			}
			var taskSubnet string
			var taskIP string
			for _, attachment := range task.Attachments {
				if attachment.Type != "ElasticNetworkInterface" {
					continue
				}
				for _, detail := range attachment.Details {
					switch detail.Name {
					case "subnetId":
						taskSubnet = detail.Value
					case "privateIPv4Address":
						taskIP = detail.Value
					}
				}
			}
			if taskSubnet == subnetID {
				add(taskIP)
			}
		}
	}
	return allocated
}

// ec2Owner() returns the EC2 resource owner — same as the AWS account
// ID. Tracks awsAccountID() so a SOCKERLESS_AWS_ACCOUNT_ID override
// propagates through every VPC/Subnet/SG OwnerId.
func ec2Owner() string { return awsAccountID() }

// defaultVPCSubnetID returns the subnet ID of the account's default VPC, or ""
// if none exists. Operations that launch "into the default VPC" when the caller
// omits a subnet (RunInstances, an ASG with no VPCZoneIdentifier) resolve the
// subnet through this — faithfully, by the IsDefault VPC — rather than a
// hardcoded ID.
func defaultVPCSubnetID() string {
	for _, s := range ec2Subnets.List() {
		if v, ok := ec2Vpcs.Get(s.VpcId); ok && v.IsDefault {
			return s.SubnetId
		}
	}
	return ""
}

// ensureSimDefaults seeds the account's default VPC + a default subnet, the same
// way a real AWS account is auto-provisioned with a default VPC and one default
// subnet per AZ (so DescribeVpcs/DescribeSubnets return them and a
// subnet-less RunInstances has somewhere to land). The IDs are deterministic — a
// simulator convention, like the fixed account ID — so coordinate-only callers
// can reference them; nothing in the sim is keyed on a sockerless-specific name.
// Idempotent; called on startup.
func ensureSimDefaults() {
	if _, ok := ec2Vpcs.Get("vpc-sim"); !ok {
		ec2Vpcs.Put("vpc-sim", EC2Vpc{
			VpcId:              "vpc-sim",
			CidrBlock:          "172.31.0.0/16",
			State:              "available",
			OwnerId:            ec2Owner(),
			IsDefault:          true,
			EnableDnsSupport:   true,
			EnableDnsHostnames: true,
		})
	}
	if _, ok := ec2Subnets.Get("subnet-0123456789abcdef0"); !ok {
		ec2Subnets.Put("subnet-0123456789abcdef0", EC2Subnet{
			SubnetId:            "subnet-0123456789abcdef0",
			VpcId:               "vpc-sim",
			CidrBlock:           "172.31.0.0/24",
			AvailabilityZone:    awsAvailabilityZone(),
			State:               "available",
			OwnerId:             ec2Owner(),
			MapPublicIpOnLaunch: false,
		})
	}
}

func registerEC2(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2Vpcs = sim.MakeStore[EC2Vpc](srv.DB(), "ec2_vpcs")
	ec2Subnets = sim.MakeStore[EC2Subnet](srv.DB(), "ec2_subnets")
	ec2InternetGateways = sim.MakeStore[EC2InternetGateway](srv.DB(), "ec2_internet_gateways")
	ec2NatGateways = sim.MakeStore[EC2NatGateway](srv.DB(), "ec2_nat_gateways")
	ec2VpcEndpoints = sim.MakeStore[EC2VpcEndpoint](srv.DB(), "ec2_vpc_endpoints")
	ec2ElasticIPs = sim.MakeStore[EC2ElasticIP](srv.DB(), "ec2_elastic_ips")
	ec2RouteTables = sim.MakeStore[EC2RouteTable](srv.DB(), "ec2_route_tables")
	ec2SecurityGroups = sim.MakeStore[EC2SecurityGroup](srv.DB(), "ec2_security_groups")
	ec2SecurityGroupRules = sim.MakeStore[EC2SecurityGroupRule](srv.DB(), "ec2_security_group_rules")
	ec2Instances = sim.MakeStore[EC2Instance](srv.DB(), "ec2_instances")
	ec2NetworkInterfaces = sim.MakeStore[EC2NetworkInterface](srv.DB(), "ec2_network_interfaces")
	ec2KeyPairs = sim.MakeStore[EC2KeyPair](srv.DB(), "ec2_key_pairs")
	ec2Volumes = sim.MakeStore[EC2Volume](srv.DB(), "ec2_volumes")
	ec2VolumeMods = sim.MakeStore[EC2VolumeModification](srv.DB(), "ec2_volume_modifications")
	ec2Snapshots = sim.MakeStore[EC2Snapshot](srv.DB(), "ec2_snapshots")
	ec2RunTokens = sim.MakeStore[EC2RunInstancesToken](srv.DB(), "ec2_run_instances_tokens")
	ec2SubnetIPCursor = sim.MakeStore[uint32](srv.DB(), "ec2_subnet_ip_cursors")

	recoverEC2Instances()

	// VPC
	r.Register("DescribeAccountAttributes", handleDescribeAccountAttributes)
	r.Register("DescribeAvailabilityZones", handleDescribeAvailabilityZones)
	r.Register("DescribeRegions", handleDescribeRegions)
	r.Register("CreateVpc", handleCreateVpc)
	r.Register("DescribeVpcs", handleDescribeVpcs)
	r.Register("DeleteVpc", handleDeleteVpc)
	r.Register("DescribeVpcAttribute", handleDescribeVpcAttribute)
	r.Register("ModifyVpcAttribute", handleModifyVpcAttribute)

	// Subnet
	r.Register("CreateSubnet", handleCreateSubnet)
	r.Register("DescribeSubnets", handleDescribeSubnets)
	r.Register("DeleteSubnet", handleDeleteSubnet)
	r.Register("ModifySubnetAttribute", handleModifySubnetAttribute)

	// Internet Gateway
	r.Register("CreateInternetGateway", handleCreateInternetGateway)
	r.Register("AttachInternetGateway", handleAttachInternetGateway)
	r.Register("DetachInternetGateway", handleDetachInternetGateway)
	r.Register("DescribeInternetGateways", handleDescribeInternetGateways)
	r.Register("DeleteInternetGateway", handleDeleteInternetGateway)

	// Elastic IP
	r.Register("AllocateAddress", handleAllocateAddress)
	r.Register("AssociateAddress", handleAssociateAddress)
	r.Register("DisassociateAddress", handleDisassociateAddress)
	r.Register("DescribeAddresses", handleDescribeAddresses)
	r.Register("DescribeAddressesAttribute", handleDescribeAddressesAttribute)
	r.Register("ReleaseAddress", handleReleaseAddress)

	// NAT Gateway
	r.Register("CreateNatGateway", handleCreateNatGateway)
	r.Register("DescribeNatGateways", handleDescribeNatGateways)
	r.Register("DeleteNatGateway", handleDeleteNatGateway)

	// VPC Endpoint
	r.Register("CreateVpcEndpoint", handleCreateVpcEndpoint)
	r.Register("DescribeVpcEndpoints", handleDescribeVpcEndpoints)
	r.Register("DeleteVpcEndpoints", handleDeleteVpcEndpoints)

	// Route Table
	r.Register("CreateRouteTable", handleCreateRouteTable)
	r.Register("DescribeRouteTables", handleDescribeRouteTables)
	r.Register("DeleteRouteTable", handleDeleteRouteTable)
	r.Register("CreateRoute", handleCreateRoute)
	r.Register("ReplaceRoute", handleReplaceRoute)
	r.Register("DeleteRoute", handleDeleteRoute)
	r.Register("AssociateRouteTable", handleAssociateRouteTable)
	r.Register("DisassociateRouteTable", handleDisassociateRouteTable)

	// Security Group
	r.Register("CreateSecurityGroup", handleCreateSecurityGroup)
	r.Register("DescribeSecurityGroups", handleDescribeSecurityGroups)
	r.Register("DescribeSecurityGroupRules", handleDescribeSecurityGroupRules)
	r.Register("ModifySecurityGroupRules", handleModifySecurityGroupRules)
	r.Register("DeleteSecurityGroup", handleDeleteSecurityGroup)
	r.Register("AuthorizeSecurityGroupIngress", handleAuthorizeSecurityGroupIngress)
	r.Register("AuthorizeSecurityGroupEgress", handleAuthorizeSecurityGroupEgress)
	r.Register("RevokeSecurityGroupIngress", handleRevokeSecurityGroupIngress)
	r.Register("RevokeSecurityGroupEgress", handleRevokeSecurityGroupEgress)
	r.Register("UpdateSecurityGroupRuleDescriptionsIngress", handleUpdateSecurityGroupRuleDescriptionsIngress)
	r.Register("UpdateSecurityGroupRuleDescriptionsEgress", handleUpdateSecurityGroupRuleDescriptionsEgress)

	// Instances
	r.Register("RunInstances", handleRunInstances)
	r.Register("DescribeInstances", handleDescribeInstances)
	r.Register("TerminateInstances", handleTerminateInstances)
	r.Register("StopInstances", handleStopInstances)
	r.Register("StartInstances", handleStartInstances)
	r.Register("DescribeInstanceStatus", handleDescribeInstanceStatus)
	r.Register("DescribeInstanceAttribute", handleDescribeInstanceAttribute)
	r.Register("ModifyInstanceAttribute", handleModifyInstanceAttribute)
	r.Register("CreateTags", handleCreateTags)
	r.Register("DeleteTags", handleDeleteTags)
	r.Register("DescribeTags", handleDescribeTags)
	r.Register("DescribeVolumes", handleDescribeVolumes)
	r.Register("CreateVolume", handleCreateVolume)
	r.Register("AttachVolume", handleAttachVolume)
	r.Register("DetachVolume", handleDetachVolume)
	r.Register("DeleteVolume", handleDeleteVolume)
	r.Register("ModifyVolume", handleModifyVolume)
	r.Register("DescribeVolumesModifications", handleDescribeVolumesModifications)
	r.Register("CreateSnapshot", handleCreateSnapshot)
	r.Register("CopySnapshot", handleCopySnapshot)
	r.Register("DescribeSnapshots", handleDescribeSnapshots)
	r.Register("DeleteSnapshot", handleDeleteSnapshot)
	r.Register("DescribeImages", handleDescribeImages)
	r.Register("DescribeInstanceTypes", handleDescribeInstanceTypes)
	r.Register("DescribeInstanceTypeOfferings", handleDescribeInstanceTypeOfferings)
	r.Register("DescribeKeyPairs", handleDescribeKeyPairs)
	r.Register("CreateKeyPair", handleCreateKeyPair)
	r.Register("ImportKeyPair", handleImportKeyPair)
	r.Register("DeleteKeyPair", handleDeleteKeyPair)
	r.Register("ModifyInstanceMetadataOptions", handleModifyInstanceMetadataOptions)

	// Seed the account's default VPC + default subnet, like a real AWS account
	// (auto-provisioned with a default VPC and a default subnet per AZ). See
	// ensureSimDefaults for the deterministic-ID rationale.
	ensureSimDefaults()

	// Network Interfaces (used during destroy to check ENIs before deleting SGs/subnets)
	r.Register("DescribeNetworkInterfaces", handleDescribeNetworkInterfaces)
	r.Register("CreateNetworkInterface", handleCreateNetworkInterface)
	r.Register("AttachNetworkInterface", handleAttachNetworkInterface)
	r.Register("DetachNetworkInterface", handleDetachNetworkInterface)
	r.Register("DeleteNetworkInterface", handleDeleteNetworkInterface)
	r.Register("ModifyNetworkInterfaceAttribute", handleModifyNetworkInterfaceAttribute)
	r.Register("AssignPrivateIpAddresses", handleAssignPrivateIpAddresses)

	registerEC2LaunchTemplates(r, srv)
	registerEC2AmiPlacementDhcp(r, srv)
	registerEC2AclPeeringPrefix(r, srv)
	registerEC2TransitGateway(r, srv)
	registerEC2TGWMulticast(r, srv)
	registerEC2IPAM(r, srv)
	registerEC2IPAMExtras(r, srv)
	registerEC2VPN(r, srv)
	registerEC2VerifiedAccess(r, srv)
	registerEC2CapacityFleet(r, srv)
	registerEC2HostsImagesVpc(r, srv)
	registerEC2NetworkInsights(r, srv)
	registerEC2EBSSnapshot(r, srv)
	registerEC2VpcEndpointSvc(r, srv)
	registerEC2InstanceMgmt(r, srv)
	registerEC2ReservedCapacity(r, srv)
	registerEC2ImagesFpga(r, srv)
	registerEC2NetworkingMisc(r, srv)
	registerEC2ImageMgmt(r, srv)
	registerEC2InstanceExtras(r, srv)
	registerEC2LgwCapacity(r, srv)
	registerEC2AccountMisc(r, srv)
	registerEC2VolumesMisc(r, srv)
}

// Tag helpers

func parseTags(r *http.Request) []EC2Tag {
	var tags []EC2Tag
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", i))
		if key == "" {
			break
		}
		value := r.FormValue(fmt.Sprintf("TagSpecification.1.Tag.%d.Value", i))
		tags = append(tags, EC2Tag{Key: key, Value: value})
	}
	return tags
}

func writeTagSetXML(tags []EC2Tag) string {
	if len(tags) == 0 {
		return "<tagSet/>"
	}
	var b strings.Builder
	b.WriteString("<tagSet>")
	for _, t := range tags {
		fmt.Fprintf(&b, "<item><key>%s</key><value>%s</value></item>", t.Key, t.Value)
	}
	b.WriteString("</tagSet>")
	return b.String()
}

func ec2ID(prefix string) string {
	return prefix + "-" + generateUUID()[:8]
}

// ec2NowRFC3339Milli formats the current time as the millisecond-precision UTC
// timestamp AMIs use for creationDate (e.g. 2024-01-01T00:00:00.000Z).
func ec2NowRFC3339Milli() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func ec2Xmlns() string {
	return `xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"`
}

func handleDescribeAccountAttributes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeAccountAttributesResponse %s>
  <requestId>%s</requestId>
  <accountAttributeSet>
    <item><attributeName>supported-platforms</attributeName><attributeValueSet><item><attributeValue>VPC</attributeValue></item></attributeValueSet></item>
    <item><attributeName>default-vpc</attributeName><attributeValueSet><item><attributeValue>vpc-sim</attributeValue></item></attributeValueSet></item>
  </accountAttributeSet>
</DescribeAccountAttributesResponse>`, ec2Xmlns(), generateUUID())
}

func handleDescribeAvailabilityZones(w http.ResponseWriter, r *http.Request) {
	region := awsRegion()
	zone := awsAvailabilityZone()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeAvailabilityZonesResponse %s>
  <requestId>%s</requestId>
  <availabilityZoneInfo><item><zoneName>%s</zoneName><zoneId>%s-az1</zoneId><zoneType>availability-zone</zoneType><regionName>%s</regionName><zoneState>available</zoneState><groupName>%s</groupName><networkBorderGroup>%s</networkBorderGroup></item></availabilityZoneInfo>
</DescribeAvailabilityZonesResponse>`, ec2Xmlns(), generateUUID(), zone, region, region, region, region)
}

func handleDescribeRegions(w http.ResponseWriter, r *http.Request) {
	region := awsRegion()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeRegionsResponse %s>
  <requestId>%s</requestId>
  <regionInfo><item><regionName>%s</regionName><regionEndpoint>ec2.%s.amazonaws.com</regionEndpoint><optInStatus>opt-in-not-required</optInStatus></item></regionInfo>
</DescribeRegionsResponse>`, ec2Xmlns(), generateUUID(), region, region)
}

// ---- VPC ----

func handleCreateVpc(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("CidrBlock")
	requestedEncryptionMode, requestedExclusions, hasRequestedEncryption, err := vpcEncryptionConfigurationFromCreateRequest(r)
	if err != nil {
		ec2ErrorXML(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return
	}
	tenancy := r.FormValue("InstanceTenancy")
	if tenancy == "" {
		tenancy = "default"
	}
	tags := parseTags(r)
	id := ec2ID("vpc")

	vpc := EC2Vpc{
		VpcId:              id,
		CidrBlock:          cidr,
		State:              "available",
		Tags:               tags,
		OwnerId:            ec2Owner(),
		IsDefault:          false,
		InstanceTenancy:    tenancy,
		EnableDnsSupport:   true,
		EnableDnsHostnames: false,
	}
	ec2Vpcs.Put(id, vpc)
	accountControl := currentAccountVpcEncryptionControl()
	switch accountControl.Mode {
	case "attempt-monitor":
		applyAccountVpcEncryptionControl(id, "monitor", accountControl.Exclusions)
	case "attempt-enforce":
		applyAccountVpcEncryptionControl(id, "enforce", accountControl.Exclusions)
	default:
		if hasRequestedEncryption {
			applyAccountVpcEncryptionControl(id, requestedEncryptionMode, requestedExclusions)
		}
	}
	// Real AWS auto-creates a main route table per VPC (local route + a main
	// association with no subnet). aws_vpc.main_route_table_id /
	// default_route_table_id and aws_default_route_table read it back.
	mainRTID := ec2ID("rtb")
	ec2RouteTables.Put(mainRTID, EC2RouteTable{
		RouteTableId: mainRTID,
		VpcId:        id,
		Routes: []EC2Route{{
			DestinationCidrBlock: cidr,
			GatewayId:            "local",
			State:                "active",
			Origin:               "CreateRouteTable",
		}},
		OwnerId: ec2Owner(),
		Associations: []EC2RouteTableAssociation{{
			AssociationId: ec2ID("rtbassoc"),
			RouteTableId:  mainRTID,
			Main:          true,
		}},
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpcResponse %s>
  <requestId>%s</requestId>
  <vpc>%s</vpc>
</CreateVpcResponse>`, ec2Xmlns(), generateUUID(), vpcItemBodyXML(vpc))
}

// vpcItemBodyXML renders the inner <vpc>/<item> fields shared by CreateVpc and
// DescribeVpcs. dhcpOptionsId is always "default" (real AWS associates the
// account's default DHCP option set on create).
func vpcItemBodyXML(vpc EC2Vpc) string {
	// Real DescribeVpcs lists the primary CIDR in cidrBlockAssociationSet as
	// well as in cidrBlock; data.aws_vpc reads cidr_block_associations from it.
	// The sim synthesizes a stable association id from the VPC id.
	assocID := "vpc-cidr-assoc-" + strings.TrimPrefix(vpc.VpcId, "vpc-")
	cidrAssoc := fmt.Sprintf(`<cidrBlockAssociationSet><item><associationId>%s</associationId><cidrBlock>%s</cidrBlock><cidrBlockState><state>associated</state></cidrBlockState></item></cidrBlockAssociationSet>`,
		assocID, vpc.CidrBlock)
	tenancy := vpc.InstanceTenancy
	if tenancy == "" {
		tenancy = "default"
	}
	dhcpOptionsID := vpc.DhcpOptionsId
	if dhcpOptionsID == "" {
		dhcpOptionsID = "default"
	}
	encryptionControl := ""
	if control, ok := vpcEncryptionControlForVPC(vpc.VpcId); ok {
		encryptionControl = "<encryptionControl>" + vpcEncryptionControlXML(control) + "</encryptionControl>"
	}
	return fmt.Sprintf(`<vpcId>%s</vpcId><cidrBlock>%s</cidrBlock><state>%s</state><ownerId>%s</ownerId><isDefault>%t</isDefault><instanceTenancy>%s</instanceTenancy><dhcpOptionsId>%s</dhcpOptionsId>%s%s%s`,
		vpc.VpcId, vpc.CidrBlock, vpc.State, vpc.OwnerId, vpc.IsDefault, tenancy, dhcpOptionsID, cidrAssoc, encryptionControl, writeTagSetXML(vpc.Tags))
}

func vpcItemXML(vpc EC2Vpc) string {
	return "<item>" + vpcItemBodyXML(vpc) + "</item>"
}

func handleDescribeVpcs(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpcId")
	for _, id := range ids {
		if _, ok := ec2Vpcs.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)

	var items strings.Builder
	for _, v := range ec2Vpcs.List() {
		if len(ids) > 0 && !ec2StrInValues(v.VpcId, ids) {
			continue
		}
		if !ec2VpcMatchesFilters(v, filters) {
			continue
		}
		items.WriteString(vpcItemXML(v))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcsResponse %s>
  <requestId>%s</requestId>
  <vpcSet>%s</vpcSet>
</DescribeVpcsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func ec2VpcMatchesFilters(v EC2Vpc, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-id":
			if !ec2StrInValues(v.VpcId, vals) {
				return false
			}
		case "cidr", "cidr-block-association.cidr-block":
			if !ec2StrInValues(v.CidrBlock, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(v.State, vals) {
				return false
			}
		case "is-default":
			if v.IsDefault != ec2StrInValues("true", vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, v.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

// ec2TagFilterMatch evaluates the EC2 tag filter forms (`tag:<Key>` and
// `tag-key`). Returns (handled, match): handled=false means the filter name
// isn't a tag filter and the caller should decide.
func ec2TagFilterMatch(name string, vals []string, tags []EC2Tag) (handled, match bool) {
	switch {
	case strings.HasPrefix(name, "tag:"):
		key := strings.TrimPrefix(name, "tag:")
		for _, t := range tags {
			if t.Key == key && ec2StrInValues(t.Value, vals) {
				return true, true
			}
		}
		return true, false
	case name == "tag-key":
		for _, t := range tags {
			if ec2StrInValues(t.Key, vals) {
				return true, true
			}
		}
		return true, false
	}
	return false, false
}

func handleDeleteVpc(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpcId")
	// Tear down the VPC's Docker-tier network (faithful: VPC gone → networking
	// gone; without this it leaks and blocks a re-created VPC reusing the CIDR).
	// The netns-tier fabric is torn down by ec2DeleteRealVPC below.
	if err := sim.RemoveDockerNetwork(ecsVPCNetworkName(id)); err != nil {
		ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to delete VPC Docker network fabric: %v", err), http.StatusServiceUnavailable)
		return
	}
	if err := ec2DeleteRealVPC(r.Context(), id); err != nil {
		ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to delete real VPC network fabric: %v", err), http.StatusServiceUnavailable)
		return
	}
	for _, rt := range ec2RouteTables.Filter(func(rt EC2RouteTable) bool { return rt.VpcId == id }) {
		ec2RouteTables.Delete(rt.RouteTableId)
	}
	for _, s := range ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == id }) {
		ec2SubnetIPCursor.Delete(s.SubnetId)
	}
	for _, control := range ec2VpcEncryptionControls.Filter(func(control EC2VpcEncryptionControl) bool {
		return control.VpcId == id
	}) {
		ec2VpcEncryptionControls.Delete(control.VpcEncryptionControlId)
	}
	ec2Vpcs.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpcResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteVpcResponse>`, ec2Xmlns(), generateUUID())
}

func handleDescribeVpcAttribute(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	attr := r.FormValue("Attribute")
	vpc, _ := ec2Vpcs.Get(vpcId)

	w.Header().Set("Content-Type", "text/xml")
	switch attr {
	case "enableDnsSupport":
		fmt.Fprintf(w, `<DescribeVpcAttributeResponse %s>
  <requestId>%s</requestId><vpcId>%s</vpcId>
  <enableDnsSupport><value>%t</value></enableDnsSupport>
</DescribeVpcAttributeResponse>`, ec2Xmlns(), generateUUID(), vpcId, vpc.EnableDnsSupport)
	case "enableDnsHostnames":
		fmt.Fprintf(w, `<DescribeVpcAttributeResponse %s>
  <requestId>%s</requestId><vpcId>%s</vpcId>
  <enableDnsHostnames><value>%t</value></enableDnsHostnames>
</DescribeVpcAttributeResponse>`, ec2Xmlns(), generateUUID(), vpcId, vpc.EnableDnsHostnames)
	case "enableNetworkAddressUsageMetrics":
		fmt.Fprintf(w, `<DescribeVpcAttributeResponse %s>
  <requestId>%s</requestId><vpcId>%s</vpcId>
  <enableNetworkAddressUsageMetrics><value>%t</value></enableNetworkAddressUsageMetrics>
</DescribeVpcAttributeResponse>`, ec2Xmlns(), generateUUID(), vpcId, vpc.EnableNetworkAddressUsageMetrics)
	default:
		fmt.Fprintf(w, `<DescribeVpcAttributeResponse %s>
  <requestId>%s</requestId><vpcId>%s</vpcId>
</DescribeVpcAttributeResponse>`, ec2Xmlns(), generateUUID(), vpcId)
	}
}

func handleModifyVpcAttribute(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	ec2Vpcs.Update(vpcId, func(v *EC2Vpc) {
		if val := r.FormValue("EnableDnsSupport.Value"); val != "" {
			v.EnableDnsSupport = val == "true"
		}
		if val := r.FormValue("EnableDnsHostnames.Value"); val != "" {
			v.EnableDnsHostnames = val == "true"
		}
		if val := r.FormValue("EnableNetworkAddressUsageMetrics.Value"); val != "" {
			v.EnableNetworkAddressUsageMetrics = val == "true"
		}
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcAttributeResponse %s>
  <requestId>%s</requestId><return>true</return>
</ModifyVpcAttributeResponse>`, ec2Xmlns(), generateUUID())
}

// ---- Subnet ----

func handleCreateSubnet(w http.ResponseWriter, r *http.Request) {
	vpcId := r.FormValue("VpcId")
	cidr := r.FormValue("CidrBlock")
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		az = awsAvailabilityZone()
	}
	azID := r.FormValue("AvailabilityZoneId")
	if azID == "" {
		azID = ec2AvailabilityZoneId(az)
	}
	tags := parseTags(r)
	id := ec2ID("subnet")

	subnet := EC2Subnet{
		SubnetId:                       id,
		VpcId:                          vpcId,
		CidrBlock:                      cidr,
		Ipv6CidrBlock:                  r.FormValue("Ipv6CidrBlock"),
		AvailabilityZone:               az,
		AvailabilityZoneId:             azID,
		State:                          "available",
		Tags:                           tags,
		OwnerId:                        ec2Owner(),
		Ipv6Native:                     r.FormValue("Ipv6Native") == "true",
		PrivateDnsHostnameTypeOnLaunch: "ip-name",
	}
	ec2Subnets.Put(id, subnet)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSubnetResponse %s>
  <requestId>%s</requestId>
  <subnet>%s</subnet>
</CreateSubnetResponse>`, ec2Xmlns(), generateUUID(), subnetItemBodyXML(subnet))
}

// ec2AvailabilityZoneId synthesizes a stable AZ-id from an AZ name. Real AWS
// AZ-ids (e.g. use1-az1) are randomized per account and not derivable from the
// name, so we emit a deterministic stable value — availability_zone_id is a
// computed attribute, so this does not drift.
func ec2AvailabilityZoneId(az string) string {
	if az == "" {
		return ""
	}
	region := az
	letter := ""
	if n := len(az); n > 0 && az[n-1] >= 'a' && az[n-1] <= 'z' {
		region = az[:n-1]
		letter = string(az[n-1])
	}
	idx := 1
	if letter != "" {
		idx = int(letter[0]-'a') + 1
	}
	return fmt.Sprintf("%s-az%d", strings.ReplaceAll(region, "-", ""), idx)
}

// ec2SubnetAvailableIPs returns the usable host count for a subnet CIDR. AWS
// reserves 5 addresses per subnet (network, VPC router, DNS, future, broadcast).
func ec2SubnetAvailableIPs(cidr string) int {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0
	}
	ones, bits := ipnet.Mask.Size()
	hostBits := bits - ones
	if hostBits <= 0 {
		return 0
	}
	total := 1 << uint(hostBits)
	if total <= 5 {
		return 0
	}
	return total - 5
}

func subnetArn(s EC2Subnet) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:subnet/%s", awsRegion(), s.OwnerId, s.SubnetId)
}

// subnetItemBodyXML renders the inner subnet fields shared by CreateSubnet and
// DescribeSubnets.
func subnetItemBodyXML(s EC2Subnet) string {
	hostnameType := s.PrivateDnsHostnameTypeOnLaunch
	if hostnameType == "" {
		hostnameType = "ip-name"
	}
	ipv6 := ""
	if s.Ipv6CidrBlock != "" {
		assocID := "subnet-cidr-assoc-" + strings.TrimPrefix(s.SubnetId, "subnet-")
		ipv6 = fmt.Sprintf(`<ipv6CidrBlockAssociationSet><item><associationId>%s</associationId><ipv6CidrBlock>%s</ipv6CidrBlock><ipv6CidrBlockState><state>associated</state></ipv6CidrBlockState></item></ipv6CidrBlockAssociationSet>`,
			assocID, s.Ipv6CidrBlock)
	}
	return fmt.Sprintf(`<subnetId>%s</subnetId><vpcId>%s</vpcId><cidrBlock>%s</cidrBlock>`+
		`<availabilityZone>%s</availabilityZone><availabilityZoneId>%s</availabilityZoneId>`+
		`<availableIpAddressCount>%d</availableIpAddressCount><state>%s</state>`+
		`<mapPublicIpOnLaunch>%t</mapPublicIpOnLaunch><mapCustomerOwnedIpOnLaunch>%t</mapCustomerOwnedIpOnLaunch>`+
		`<assignIpv6AddressOnCreation>%t</assignIpv6AddressOnCreation><enableDns64>%t</enableDns64>`+
		`<ipv6Native>%t</ipv6Native><defaultForAz>false</defaultForAz>`+
		`<privateDnsNameOptionsOnLaunch><hostnameType>%s</hostnameType><enableResourceNameDnsARecord>%t</enableResourceNameDnsARecord><enableResourceNameDnsAAAARecord>%t</enableResourceNameDnsAAAARecord></privateDnsNameOptionsOnLaunch>`+
		`<subnetArn>%s</subnetArn><ownerId>%s</ownerId>%s%s`,
		s.SubnetId, s.VpcId, s.CidrBlock,
		s.AvailabilityZone, s.AvailabilityZoneId,
		ec2SubnetAvailableIPs(s.CidrBlock), s.State,
		s.MapPublicIpOnLaunch, s.MapCustomerOwnedIpOnLaunch,
		s.AssignIpv6AddressOnCreation, s.EnableDns64,
		s.Ipv6Native,
		hostnameType, s.EnableResourceNameDnsARecordOnLaunch, s.EnableResourceNameDnsAAAARecordOnLaunch,
		subnetArn(s), s.OwnerId, ipv6, writeTagSetXML(s.Tags))
}

func subnetItemXML(s EC2Subnet) string {
	return "<item>" + subnetItemBodyXML(s) + "</item>"
}

func ec2SubnetMatchesFilters(s EC2Subnet, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-id":
			if !ec2StrInValues(s.VpcId, vals) {
				return false
			}
		case "subnet-id":
			if !ec2StrInValues(s.SubnetId, vals) {
				return false
			}
		case "cidr", "cidr-block", "cidrBlock":
			if !ec2StrInValues(s.CidrBlock, vals) {
				return false
			}
		case "availability-zone", "availabilityZone":
			if !ec2StrInValues(s.AvailabilityZone, vals) {
				return false
			}
		case "availability-zone-id":
			if !ec2StrInValues(s.AvailabilityZoneId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(s.State, vals) {
				return false
			}
		case "owner-id":
			if !ec2StrInValues(s.OwnerId, vals) {
				return false
			}
		case "default-for-az":
			if ec2StrInValues("true", vals) {
				return false // the sim creates no default-for-az subnets
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, s.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDescribeSubnets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SubnetId")
	for _, id := range ids {
		if _, ok := ec2Subnets.Get(id); !ok {
			ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)

	var items strings.Builder
	for _, s := range ec2Subnets.List() {
		if len(ids) > 0 && !ec2StrInValues(s.SubnetId, ids) {
			continue
		}
		if !ec2SubnetMatchesFilters(s, filters) {
			continue
		}
		items.WriteString(subnetItemXML(s))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSubnetsResponse %s>
  <requestId>%s</requestId>
  <subnetSet>%s</subnetSet>
</DescribeSubnetsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifySubnetAttribute(w http.ResponseWriter, r *http.Request) {
	subnetId := r.FormValue("SubnetId")
	ec2Subnets.Update(subnetId, func(s *EC2Subnet) {
		if val := r.FormValue("MapPublicIpOnLaunch.Value"); val != "" {
			s.MapPublicIpOnLaunch = val == "true"
		}
		if val := r.FormValue("MapCustomerOwnedIpOnLaunch.Value"); val != "" {
			s.MapCustomerOwnedIpOnLaunch = val == "true"
		}
		if val := r.FormValue("AssignIpv6AddressOnCreation.Value"); val != "" {
			s.AssignIpv6AddressOnCreation = val == "true"
		}
		if val := r.FormValue("EnableDns64.Value"); val != "" {
			s.EnableDns64 = val == "true"
		}
		if val := r.FormValue("EnableResourceNameDnsARecordOnLaunch.Value"); val != "" {
			s.EnableResourceNameDnsARecordOnLaunch = val == "true"
		}
		if val := r.FormValue("EnableResourceNameDnsAAAARecordOnLaunch.Value"); val != "" {
			s.EnableResourceNameDnsAAAARecordOnLaunch = val == "true"
		}
		if val := r.FormValue("PrivateDnsHostnameTypeOnLaunch"); val != "" {
			s.PrivateDnsHostnameTypeOnLaunch = val
		}
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifySubnetAttributeResponse %s>
  <requestId>%s</requestId><return>true</return>
</ModifySubnetAttributeResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteSubnet(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("SubnetId")
	if _, ok := ec2Subnets.Get(id); !ok {
		ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	for _, networkInterface := range ec2NetworkInterfaces.List() {
		if networkInterface.SubnetId == id {
			ec2ErrorXML(w, "DependencyViolation",
				fmt.Sprintf("The subnet '%s' has dependencies and cannot be deleted.", id),
				http.StatusBadRequest)
			return
		}
	}
	if err := ec2DeleteRealSubnet(r.Context(), id); err != nil {
		ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to delete real subnet network fabric: %v", err), http.StatusServiceUnavailable)
		return
	}
	ec2Subnets.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSubnetResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteSubnetResponse>`, ec2Xmlns(), generateUUID())
}

// ---- Internet Gateway ----

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
</CreateInternetGatewayResponse>`, ec2Xmlns(), generateUUID(), id, ec2Owner(), writeTagSetXML(tags))
}

func handleAttachInternetGateway(w http.ResponseWriter, r *http.Request) {
	igwId := r.FormValue("InternetGatewayId")
	vpcId := r.FormValue("VpcId")

	ec2InternetGateways.Update(igwId, func(igw *EC2InternetGateway) {
		igw.Attachments = append(igw.Attachments, EC2IGWAttachment{VpcId: vpcId, State: "available"})
	})
	if err := ec2ApplyRealVPCEgressPolicy(r.Context(), vpcId); err != nil {
		fmt.Fprintf(os.Stderr, "sim: real VPC egress policy for %s unavailable after IGW attach: %v\n", vpcId, err)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AttachInternetGatewayResponse %s>
  <requestId>%s</requestId><return>true</return>
</AttachInternetGatewayResponse>`, ec2Xmlns(), generateUUID())
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
	if err := ec2ApplyRealVPCEgressPolicy(r.Context(), vpcId); err != nil {
		fmt.Fprintf(os.Stderr, "sim: real VPC egress policy for %s unavailable after IGW detach: %v\n", vpcId, err)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DetachInternetGatewayResponse %s>
  <requestId>%s</requestId><return>true</return>
</DetachInternetGatewayResponse>`, ec2Xmlns(), generateUUID())
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
</DescribeInternetGatewaysResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteInternetGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InternetGatewayId")
	ec2InternetGateways.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteInternetGatewayResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteInternetGatewayResponse>`, ec2Xmlns(), generateUUID())
}

// ---- Elastic IP ----

func handleAllocateAddress(w http.ResponseWriter, r *http.Request) {
	domain := r.FormValue("Domain")
	if domain == "" {
		domain = "vpc"
	}
	tags := parseTags(r)
	id := ec2ID("eipalloc")
	ip, err := realexec.ReserveAWSPublicIPv4(id, nil)
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
</AllocateAddressResponse>`, ec2Xmlns(), generateUUID(), id, ip.String(), domain, nbg, pool)
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
</AssociateAddressResponse>`, ec2Xmlns(), generateUUID(), assocId)
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
</DisassociateAddressResponse>`, ec2Xmlns(), generateUUID())
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
</DescribeAddressesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleReleaseAddress(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("AllocationId")
	if eip, ok := ec2ElasticIPs.Get(id); ok {
		realexec.ReleasePublicIPv4(net.ParseIP(eip.PublicIp))
	}
	ec2ElasticIPs.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReleaseAddressResponse %s>
  <requestId>%s</requestId><return>true</return>
</ReleaseAddressResponse>`, ec2Xmlns(), generateUUID())
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
</DescribeAddressesAttributeResponse>`, ec2Xmlns(), generateUUID(), allocId)
	} else {
		fmt.Fprintf(w, `<DescribeAddressesAttributeResponse %s>
  <requestId>%s</requestId>
  <addressSet/>
</DescribeAddressesAttributeResponse>`, ec2Xmlns(), generateUUID())
	}
}

// ---- NAT Gateway ----

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
	// A NAT gateway is a pure control-plane object from the API's perspective,
	// so it is always modeled (State:"available", describable) — exactly like
	// handleCreateVpc. Real NAT fabric is programmed opportunistically, only
	// when the host actually has the network capabilities; its absence must not
	// fail the API call (IaC/control-plane testing in SIM_RUNTIME=process runs
	// on hosts without CAP_NET_ADMIN/nft).
	ec2NatGateways.Put(id, natgw)
	if err := realexec.DetectNetworkCapabilities().Require(); err == nil {
		if err2 := ec2CreateRealNATGateway(r.Context(), natgw); err2 != nil {
			fmt.Fprintf(os.Stderr, "sim: real NAT gateway %s network fabric unavailable: %v\n", id, err2)
		}
	}

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
</CreateNatGatewayResponse>`, ec2Xmlns(), generateUUID(), id, subnetId, vpcId, connectivityType, natgwAddrSetXML(natgw.NatGatewayAddresses), natgw.CreateTime, writeTagSetXML(tags))
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
</DescribeNatGatewaysResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteNatGateway(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("NatGatewayId")
	ec2NatGateways.Delete(id)
	if err := ec2DeleteRealNATGateway(r.Context(), id); err != nil {
		ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to delete real NAT gateway fabric: %v", err), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteNatGatewayResponse %s>
  <requestId>%s</requestId>
  <natGatewayId>%s</natGatewayId>
</DeleteNatGatewayResponse>`, ec2Xmlns(), generateUUID(), id)
}

// ---- Route Table ----

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
</CreateRouteTableResponse>`, ec2Xmlns(), generateUUID(), id, vpcId, routeSetXML(rt.Routes), writeTagSetXML(tags))
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
</DescribeRouteTablesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteRouteTable(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RouteTableId")
	ec2RouteTables.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteRouteTableResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteRouteTableResponse>`, ec2Xmlns(), generateUUID())
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
	if route.NatGatewayId != "" {
		// The route is always modeled below; programming the real NAT route is
		// opportunistic (only when the host has network capabilities) and must
		// not fail the API call. Mirrors handleCreateNatGateway.
		if err := realexec.DetectNetworkCapabilities().Require(); err == nil {
			if err2 := ec2ConfigureRealNATRoute(r.Context(), rtId, route.DestinationCidrBlock, route.NatGatewayId); err2 != nil {
				fmt.Fprintf(os.Stderr, "sim: real NAT route to %s unavailable: %v\n", route.NatGatewayId, err2)
			}
		}
	}

	ec2RouteTables.Update(rtId, func(rt *EC2RouteTable) {
		rt.Routes = append(rt.Routes, route)
	})
	if err := ec2ApplyRealRouteTableEgressPolicy(r.Context(), rtId); err != nil {
		fmt.Fprintf(os.Stderr, "sim: real route-table egress policy for %s unavailable after route create: %v\n", rtId, err)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateRouteResponse %s>
  <requestId>%s</requestId><return>true</return>
</CreateRouteResponse>`, ec2Xmlns(), generateUUID())
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
	if newRoute.NatGatewayId != "" {
		if err := realexec.DetectNetworkCapabilities().Require(); err == nil {
			if err2 := ec2ConfigureRealNATRoute(r.Context(), rtId, cidr, newRoute.NatGatewayId); err2 != nil {
				fmt.Fprintf(os.Stderr, "sim: real NAT route to %s unavailable: %v\n", newRoute.NatGatewayId, err2)
			}
		}
	}
	ec2RouteTables.Update(rtId, func(rt *EC2RouteTable) {
		for i := range rt.Routes {
			if routeDestMatches(rt.Routes[i], cidr, ipv6, prefix) {
				rt.Routes[i] = newRoute
				return
			}
		}
	})
	if err := ec2ApplyRealRouteTableEgressPolicy(r.Context(), rtId); err != nil {
		fmt.Fprintf(os.Stderr, "sim: real route-table egress policy for %s unavailable after route replace: %v\n", rtId, err)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceRouteResponse %s>
  <requestId>%s</requestId><return>true</return>
</ReplaceRouteResponse>`, ec2Xmlns(), generateUUID())
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
	if err := ec2ApplyRealRouteTableEgressPolicy(r.Context(), rtId); err != nil {
		fmt.Fprintf(os.Stderr, "sim: real route-table egress policy for %s unavailable after route delete: %v\n", rtId, err)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteRouteResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteRouteResponse>`, ec2Xmlns(), generateUUID())
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
	if err := ec2ApplyRealRouteTableEgressPolicy(r.Context(), rtId); err != nil {
		fmt.Fprintf(os.Stderr, "sim: real route-table egress policy for %s unavailable after association: %v\n", rtId, err)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateRouteTableResponse %s>
  <requestId>%s</requestId>
  <associationId>%s</associationId>
</AssociateRouteTableResponse>`, ec2Xmlns(), generateUUID(), assocId)
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
				if err := ec2ApplyRealRouteTableEgressPolicy(r.Context(), rt.RouteTableId); err != nil {
					fmt.Fprintf(os.Stderr, "sim: real route-table egress policy for %s unavailable after disassociation: %v\n", rt.RouteTableId, err)
				}
				break
			}
		}
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateRouteTableResponse %s>
  <requestId>%s</requestId><return>true</return>
</DisassociateRouteTableResponse>`, ec2Xmlns(), generateUUID())
}

// ---- Security Group ----

func handleCreateSecurityGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	desc := r.FormValue("GroupDescription")
	vpcId := r.FormValue("VpcId")
	tags := parseTags(r)

	// Real AWS rejects a duplicate group name within the same VPC
	// (different VPCs may reuse a name). The ECS backend's idempotent
	// network-create relies on InvalidGroup.Duplicate to reuse an
	// existing SG by name+VPC instead of leaking a second one.
	for _, existing := range ec2SecurityGroups.List() {
		if existing.GroupName == name && existing.VpcId == vpcId {
			ec2ErrorXML(w, "InvalidGroup.Duplicate",
				fmt.Sprintf("The security group '%s' already exists for VPC '%s'", name, vpcId),
				http.StatusBadRequest)
			return
		}
	}

	id := ec2ID("sg")

	sg := EC2SecurityGroup{
		GroupId:     id,
		GroupName:   name,
		Description: desc,
		VpcId:       vpcId,
		Tags:        tags,
		OwnerId:     ec2Owner(),
	}
	// Real AWS creates VPC security groups with a default ALLOW ALL egress rule.
	// Simulating it keeps terraform-provider-aws's `aws_security_group` resource
	// from failing when it revokes the default rule before applying user rules.
	if vpcId != "" {
		sg.IpPermissionsEgress = []EC2IpPermission{{
			IpProtocol: "-1",
			IpRanges:   []EC2IpRange{{CidrIp: "0.0.0.0/0"}},
		}}
	}
	ec2SecurityGroups.Put(id, sg)
	// Materialize the default egress rule as standalone SecurityGroupRule rows
	// so DescribeSecurityGroupRules sees it and it can be revoked by rule ID.
	if vpcId != "" {
		createSecurityGroupRules(id, sg.IpPermissionsEgress[0], true)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSecurityGroupResponse %s>
  <requestId>%s</requestId>
  <groupId>%s</groupId>
  <return>true</return>
</CreateSecurityGroupResponse>`, ec2Xmlns(), generateUUID(), id)
}

func sgItemXML(sg EC2SecurityGroup) string {
	return fmt.Sprintf(`<item>
    <groupId>%s</groupId><groupName>%s</groupName><groupDescription>%s</groupDescription>
    <vpcId>%s</vpcId><ownerId>%s</ownerId>
    %s%s
    %s
  </item>`, sg.GroupId, sg.GroupName, sg.Description, sg.VpcId, sg.OwnerId,
		ipPermsXML("ipPermissions", sg.IpPermissions),
		ipPermsXML("ipPermissionsEgress", sg.IpPermissionsEgress),
		writeTagSetXML(sg.Tags))
}

func ipPermsXML(element string, perms []EC2IpPermission) string {
	if len(perms) == 0 {
		return fmt.Sprintf("<%s/>", element)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", element)
	for _, p := range perms {
		b.WriteString("<item>")
		fmt.Fprintf(&b, "<ipProtocol>%s</ipProtocol>", p.IpProtocol)
		// All-traffic rules (ip_protocol="-1") carry no ports — real AWS omits
		// fromPort/toPort, so the provider reads them back as null. Emitting 0
		// would make an inline aws_security_group rule drift "0 -> null".
		if p.IpProtocol != "-1" {
			fmt.Fprintf(&b, "<fromPort>%d</fromPort><toPort>%d</toPort>", p.FromPort, p.ToPort)
		}
		if len(p.IpRanges) > 0 {
			b.WriteString("<ipRanges>")
			for _, r := range p.IpRanges {
				fmt.Fprintf(&b, "<item><cidrIp>%s</cidrIp>", r.CidrIp)
				if r.Description != "" {
					fmt.Fprintf(&b, "<description>%s</description>", r.Description)
				}
				b.WriteString("</item>")
			}
			b.WriteString("</ipRanges>")
		} else {
			b.WriteString("<ipRanges/>")
		}
		if len(p.Ipv6Ranges) > 0 {
			b.WriteString("<ipv6Ranges>")
			for _, r := range p.Ipv6Ranges {
				fmt.Fprintf(&b, "<item><cidrIpv6>%s</cidrIpv6>", r.CidrIpv6)
				if r.Description != "" {
					fmt.Fprintf(&b, "<description>%s</description>", r.Description)
				}
				b.WriteString("</item>")
			}
			b.WriteString("</ipv6Ranges>")
		} else {
			b.WriteString("<ipv6Ranges/>")
		}
		if len(p.PrefixListIds) > 0 {
			b.WriteString("<prefixListIds>")
			for _, pl := range p.PrefixListIds {
				fmt.Fprintf(&b, "<item><prefixListId>%s</prefixListId>", pl.PrefixListId)
				if pl.Description != "" {
					fmt.Fprintf(&b, "<description>%s</description>", pl.Description)
				}
				b.WriteString("</item>")
			}
			b.WriteString("</prefixListIds>")
		} else {
			b.WriteString("<prefixListIds/>")
		}
		if len(p.UserIdGroupPairs) > 0 {
			b.WriteString("<groups>")
			for _, g := range p.UserIdGroupPairs {
				fmt.Fprintf(&b, "<item><groupId>%s</groupId>", g.GroupId)
				if g.Description != "" {
					fmt.Fprintf(&b, "<description>%s</description>", g.Description)
				}
				b.WriteString("</item>")
			}
			b.WriteString("</groups>")
		} else {
			b.WriteString("<groups/>")
		}
		b.WriteString("</item>")
	}
	fmt.Fprintf(&b, "</%s>", element)
	return b.String()
}

func handleDescribeSecurityGroups(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "GroupId")
	names := ec2ParamList(r, "GroupName")
	filters := ec2Filters(r)

	var items strings.Builder
	for _, sg := range ec2SecurityGroups.List() {
		if len(ids) > 0 && !ec2StrInValues(sg.GroupId, ids) {
			continue
		}
		if len(names) > 0 && !ec2StrInValues(sg.GroupName, names) {
			continue
		}
		if !ec2SecurityGroupMatchesFilters(sg, filters) {
			continue
		}
		items.WriteString(sgItemXML(sg))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSecurityGroupsResponse %s>
  <requestId>%s</requestId>
  <securityGroupInfo>%s</securityGroupInfo>
</DescribeSecurityGroupsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func ec2SecurityGroupMatchesFilters(sg EC2SecurityGroup, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "vpc-id":
			if !ec2StrInValues(sg.VpcId, vals) {
				return false
			}
		case "group-id":
			if !ec2StrInValues(sg.GroupId, vals) {
				return false
			}
		case "group-name":
			if !ec2StrInValues(sg.GroupName, vals) {
				return false
			}
		case "description":
			if !ec2StrInValues(sg.Description, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, sg.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GroupId")
	if _, ok := ec2SecurityGroups.Get(id); !ok {
		ec2ErrorXML(w, "InvalidGroup.NotFound", fmt.Sprintf("The security group '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2SecurityGroups.Delete(id)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSecurityGroupResponse %s>
  <requestId>%s</requestId><return>true</return>
</DeleteSecurityGroupResponse>`, ec2Xmlns(), generateUUID())
}

func parseIpPermission(r *http.Request, prefix string) EC2IpPermission {
	perm := EC2IpPermission{
		IpProtocol: r.FormValue(prefix + ".IpProtocol"),
	}
	if v := r.FormValue(prefix + ".FromPort"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &perm.FromPort); err != nil {
			perm.FromPort = 0
		}
	}
	if v := r.FormValue(prefix + ".ToPort"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &perm.ToPort); err != nil {
			perm.ToPort = 0
		}
	}

	for i := 1; ; i++ {
		cidr := r.FormValue(fmt.Sprintf("%s.IpRanges.%d.CidrIp", prefix, i))
		if cidr == "" {
			break
		}
		desc := r.FormValue(fmt.Sprintf("%s.IpRanges.%d.Description", prefix, i))
		perm.IpRanges = append(perm.IpRanges, EC2IpRange{CidrIp: cidr, Description: desc})
	}

	for i := 1; ; i++ {
		cidr := r.FormValue(fmt.Sprintf("%s.Ipv6Ranges.%d.CidrIpv6", prefix, i))
		if cidr == "" {
			break
		}
		desc := r.FormValue(fmt.Sprintf("%s.Ipv6Ranges.%d.Description", prefix, i))
		perm.Ipv6Ranges = append(perm.Ipv6Ranges, EC2Ipv6Range{CidrIpv6: cidr, Description: desc})
	}

	for i := 1; ; i++ {
		plid := r.FormValue(fmt.Sprintf("%s.PrefixListIds.%d.PrefixListId", prefix, i))
		if plid == "" {
			break
		}
		desc := r.FormValue(fmt.Sprintf("%s.PrefixListIds.%d.Description", prefix, i))
		perm.PrefixListIds = append(perm.PrefixListIds, EC2PrefixListId{PrefixListId: plid, Description: desc})
	}

	// Try both "UserIdGroupPairs" (classic) and "Groups" (SDK v2) field names
	for i := 1; ; i++ {
		gid := r.FormValue(fmt.Sprintf("%s.UserIdGroupPairs.%d.GroupId", prefix, i))
		if gid == "" {
			gid = r.FormValue(fmt.Sprintf("%s.Groups.%d.GroupId", prefix, i))
		}
		if gid == "" {
			break
		}
		desc := r.FormValue(fmt.Sprintf("%s.UserIdGroupPairs.%d.Description", prefix, i))
		if desc == "" {
			desc = r.FormValue(fmt.Sprintf("%s.Groups.%d.Description", prefix, i))
		}
		perm.UserIdGroupPairs = append(perm.UserIdGroupPairs, EC2UserIdGroupPair{GroupId: gid, Description: desc})
	}
	return perm
}

func sgrItemXML(rule EC2SecurityGroupRule) string {
	var b strings.Builder
	b.WriteString("<item>")
	fmt.Fprintf(&b, "<securityGroupRuleId>%s</securityGroupRuleId>", rule.RuleId)
	fmt.Fprintf(&b, "<groupId>%s</groupId>", rule.GroupId)
	fmt.Fprintf(&b, "<groupOwnerId>%s</groupOwnerId>", rule.GroupOwner)
	fmt.Fprintf(&b, "<isEgress>%t</isEgress>", rule.IsEgress)
	fmt.Fprintf(&b, "<ipProtocol>%s</ipProtocol>", rule.IpProtocol)
	// All-traffic rules (ip_protocol="-1") carry no ports — real AWS omits
	// fromPort/toPort entirely, so the provider reads them back as null. Emitting
	// 0 would make every idempotency plan see "0 -> null" drift.
	if rule.IpProtocol != "-1" {
		fmt.Fprintf(&b, "<fromPort>%d</fromPort>", rule.FromPort)
		fmt.Fprintf(&b, "<toPort>%d</toPort>", rule.ToPort)
	}
	if rule.CidrIpv4 != "" {
		fmt.Fprintf(&b, "<cidrIpv4>%s</cidrIpv4>", rule.CidrIpv4)
	}
	if rule.CidrIpv6 != "" {
		fmt.Fprintf(&b, "<cidrIpv6>%s</cidrIpv6>", rule.CidrIpv6)
	}
	if rule.PrefixListId != "" {
		fmt.Fprintf(&b, "<prefixListId>%s</prefixListId>", rule.PrefixListId)
	}
	if rule.RefGroupId != "" {
		// Same-account references carry no userId — emitting the owner account
		// makes the provider render "<account>/sg-id" (it only prefixes when
		// ReferencedGroupInfo.UserId differs from its own account), which drifts
		// against the bare sg-id stored in config. The sim is single-account, so
		// every reference is same-account.
		fmt.Fprintf(&b, "<referencedGroupInfo><groupId>%s</groupId></referencedGroupInfo>", rule.RefGroupId)
	}
	if rule.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", rule.Description)
	}
	fmt.Fprintf(&b, "<securityGroupRuleArn>arn:aws:ec2:%s:%s:security-group-rule/%s</securityGroupRuleArn>",
		awsRegion(), rule.GroupOwner, rule.RuleId)
	// SDK reads rule tags from <tagSet>, not <tags> — an empty <tags/> never
	// surfaced and tagged standalone rules could not round-trip.
	b.WriteString(writeTagSetXML(rule.Tags))
	b.WriteString("</item>")
	return b.String()
}

// createSecurityGroupRules materializes one SecurityGroupRule row per source in
// the permission — IPv4 CIDR, IPv6 CIDR, prefix list, and referenced group. The
// standalone aws_vpc_security_group_{ingress,egress}_rule resources Read each
// back by SecurityGroupRuleId, so a missing row (the prior IPv6/prefix-list gap)
// makes such a rule drift/recreate every plan.
// ec2PermissionDuplicate reports whether `incoming` duplicates a rule
// already present in `existing`. Real AWS rejects an authorize when the
// expanded rule (protocol + port range + a single target: CIDR / IPv6 /
// prefix-list / SG-pair) already exists, with InvalidPermission.Duplicate.
// We match on protocol + ports + any shared target.
func ec2PermissionDuplicate(existing []EC2IpPermission, incoming EC2IpPermission) bool {
	for _, e := range existing {
		if e.IpProtocol != incoming.IpProtocol || e.FromPort != incoming.FromPort || e.ToPort != incoming.ToPort {
			continue
		}
		for _, in := range incoming.IpRanges {
			for _, ex := range e.IpRanges {
				if ex.CidrIp == in.CidrIp {
					return true
				}
			}
		}
		for _, in := range incoming.Ipv6Ranges {
			for _, ex := range e.Ipv6Ranges {
				if ex.CidrIpv6 == in.CidrIpv6 {
					return true
				}
			}
		}
		for _, in := range incoming.PrefixListIds {
			for _, ex := range e.PrefixListIds {
				if ex.PrefixListId == in.PrefixListId {
					return true
				}
			}
		}
		for _, in := range incoming.UserIdGroupPairs {
			for _, ex := range e.UserIdGroupPairs {
				if ex.GroupId == in.GroupId {
					return true
				}
			}
		}
	}
	return false
}

func createSecurityGroupRules(groupId string, perm EC2IpPermission, isEgress bool) []EC2SecurityGroupRule {
	sg, _ := ec2SecurityGroups.Get(groupId)
	base := EC2SecurityGroupRule{
		GroupId:    groupId,
		GroupOwner: sg.OwnerId,
		IsEgress:   isEgress,
		IpProtocol: perm.IpProtocol,
		FromPort:   perm.FromPort,
		ToPort:     perm.ToPort,
	}
	var rules []EC2SecurityGroupRule
	add := func(mut func(*EC2SecurityGroupRule)) {
		rule := base
		rule.RuleId = ec2ID("sgr")
		mut(&rule)
		ec2SecurityGroupRules.Put(rule.RuleId, rule)
		rules = append(rules, rule)
	}
	for _, ipr := range perm.IpRanges {
		add(func(r *EC2SecurityGroupRule) { r.CidrIpv4 = ipr.CidrIp; r.Description = ipr.Description })
	}
	for _, ipr := range perm.Ipv6Ranges {
		add(func(r *EC2SecurityGroupRule) { r.CidrIpv6 = ipr.CidrIpv6; r.Description = ipr.Description })
	}
	for _, pl := range perm.PrefixListIds {
		add(func(r *EC2SecurityGroupRule) { r.PrefixListId = pl.PrefixListId; r.Description = pl.Description })
	}
	for _, gp := range perm.UserIdGroupPairs {
		add(func(r *EC2SecurityGroupRule) { r.RefGroupId = gp.GroupId; r.Description = gp.Description })
	}
	return rules
}

// deleteSecurityGroupRules removes the SecurityGroupRule rows matching a revoked
// permission (same group/direction/protocol/ports and one of its sources), so a
// revoke no longer leaves orphan rows behind in DescribeSecurityGroupRules.
func deleteSecurityGroupRules(groupId string, perm EC2IpPermission, isEgress bool) {
	for _, rule := range ec2SecurityGroupRules.List() {
		if rule.GroupId != groupId || rule.IsEgress != isEgress ||
			rule.IpProtocol != perm.IpProtocol || rule.FromPort != perm.FromPort || rule.ToPort != perm.ToPort {
			continue
		}
		matched := false
		switch {
		case rule.CidrIpv4 != "":
			matched = ec2IPRangeHasCidr(perm.IpRanges, rule.CidrIpv4)
		case rule.CidrIpv6 != "":
			for _, ipr := range perm.Ipv6Ranges {
				if ipr.CidrIpv6 == rule.CidrIpv6 {
					matched = true
				}
			}
		case rule.PrefixListId != "":
			for _, pl := range perm.PrefixListIds {
				if pl.PrefixListId == rule.PrefixListId {
					matched = true
				}
			}
		case rule.RefGroupId != "":
			for _, gp := range perm.UserIdGroupPairs {
				if gp.GroupId == rule.RefGroupId {
					matched = true
				}
			}
		}
		if matched {
			ec2SecurityGroupRules.Delete(rule.RuleId)
		}
	}
}

func ec2IPRangeHasCidr(ranges []EC2IpRange, cidr string) bool {
	for _, r := range ranges {
		if r.CidrIp == cidr {
			return true
		}
	}
	return false
}

// ec2SGValidationError describes a syntactic failure of an IpPermission.
// Code is the real AWS error code returned to SDK/CLI/terraform callers, so a
// provider using the simulator sees the same rejection it would in real AWS.
type ec2SGValidationError struct {
	Code    string
	Message string
}

// validateIpPermission checks protocol, port range, CIDR syntax, and the
// existence of referenced security groups. It mirrors the rejection real AWS
// applies at the AuthorizeSecurityGroup{Ingress,Egress} boundary. An empty Code
// means the permission is well-formed.
func validateIpPermission(perm EC2IpPermission) ec2SGValidationError {
	proto := strings.ToLower(strings.TrimSpace(perm.IpProtocol))
	switch proto {
	case "-1", "all":
		// All-traffic rules carry no port semantics.
	case "tcp", "udp":
		if perm.FromPort < 0 || perm.FromPort > 65535 || perm.ToPort < 0 || perm.ToPort > 65535 {
			return ec2SGValidationError{Code: "InvalidPortRange.Malformed",
				Message: fmt.Sprintf("Invalid port range (%d-%d) for protocol %s; ports must be 0-65535", perm.FromPort, perm.ToPort, proto)}
		}
		if perm.FromPort != 0 && perm.ToPort != 0 && perm.FromPort > perm.ToPort {
			return ec2SGValidationError{Code: "InvalidPortRange.Malformed",
				Message: fmt.Sprintf("Invalid port range (%d-%d) for protocol %s; fromPort must be less than or equal to toPort", perm.FromPort, perm.ToPort, proto)}
		}
	case "icmp", "icmpv6":
		// For ICMP the values encode type/code; -1 (all) is accepted, otherwise
		// each value must fit 0-255.
		for _, p := range []int{perm.FromPort, perm.ToPort} {
			if p != -1 && (p < 0 || p > 255) {
				return ec2SGValidationError{Code: "InvalidPortRange.Malformed",
					Message: fmt.Sprintf("Invalid ICMP type/code value %d for protocol %s; must be -1 or 0-255", p, proto)}
			}
		}
	default:
		return ec2SGValidationError{Code: "InvalidPermission.Malformed",
			Message: fmt.Sprintf("Unsupported protocol %q; supported: -1, tcp, udp, icmp, icmpv6", perm.IpProtocol)}
	}

	for _, ipr := range perm.IpRanges {
		if _, _, err := net.ParseCIDR(ipr.CidrIp); err != nil {
			return ec2SGValidationError{Code: "InvalidPermission.Malformed",
				Message: fmt.Sprintf("Invalid CIDR %q: %v", ipr.CidrIp, err)}
		}
	}
	for _, ipr := range perm.Ipv6Ranges {
		if _, _, err := net.ParseCIDR(ipr.CidrIpv6); err != nil {
			return ec2SGValidationError{Code: "InvalidPermission.Malformed",
				Message: fmt.Sprintf("Invalid IPv6 CIDR %q: %v", ipr.CidrIpv6, err)}
		}
	}
	for _, gp := range perm.UserIdGroupPairs {
		if gp.GroupId == "" {
			return ec2SGValidationError{Code: "InvalidGroup.NotFound",
				Message: "UserIdGroupPairs.GroupId is required"}
		}
		if _, ok := ec2SecurityGroups.Get(gp.GroupId); !ok {
			return ec2SGValidationError{Code: "InvalidGroup.NotFound",
				Message: fmt.Sprintf("The security group %q does not exist", gp.GroupId)}
		}
	}
	return ec2SGValidationError{}
}

func handleAuthorizeSecurityGroupIngress(w http.ResponseWriter, r *http.Request) {
	handleAuthorizeSecurityGroupRule(w, r, false)
}

func handleAuthorizeSecurityGroupEgress(w http.ResponseWriter, r *http.Request) {
	handleAuthorizeSecurityGroupRule(w, r, true)
}

func handleAuthorizeSecurityGroupRule(w http.ResponseWriter, r *http.Request, egress bool) {
	groupId := r.FormValue("GroupId")
	if _, ok := ec2SecurityGroups.Get(groupId); !ok {
		ec2ErrorXML(w, "InvalidGroup.NotFound", fmt.Sprintf("The security group '%s' does not exist", groupId), http.StatusBadRequest)
		return
	}
	perm := parseIpPermission(r, "IpPermissions.1")

	if verr := validateIpPermission(perm); verr.Code != "" {
		ec2ErrorXML(w, verr.Code, verr.Message, http.StatusBadRequest)
		return
	}

	var existing []EC2IpPermission
	if egress {
		if sg, ok := ec2SecurityGroups.Get(groupId); ok {
			existing = sg.IpPermissionsEgress
		}
	} else {
		if sg, ok := ec2SecurityGroups.Get(groupId); ok {
			existing = sg.IpPermissions
		}
	}
	if ec2PermissionDuplicate(existing, perm) {
		ec2ErrorXML(w, "InvalidPermission.Duplicate",
			"the specified rule already exists", http.StatusBadRequest)
		return
	}

	ec2SecurityGroups.Update(groupId, func(sg *EC2SecurityGroup) {
		if egress {
			sg.IpPermissionsEgress = append(sg.IpPermissionsEgress, perm)
		} else {
			sg.IpPermissions = append(sg.IpPermissions, perm)
		}
	})

	rules := createSecurityGroupRules(groupId, perm, egress)
	if err := ec2ReapplyRealSecurityGroup(r.Context(), groupId); err != nil {
		direction := "ingress"
		if egress {
			direction = "egress"
		}
		ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to program real security group %s rules: %v", direction, err), http.StatusServiceUnavailable)
		return
	}
	var ruleSetXML strings.Builder
	for _, rule := range rules {
		ruleSetXML.WriteString(sgrItemXML(rule))
	}

	action := "AuthorizeSecurityGroupIngress"
	if egress {
		action = "AuthorizeSecurityGroupEgress"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s>
  <requestId>%s</requestId><return>true</return>
  <securityGroupRuleSet>%s</securityGroupRuleSet>
</%sResponse>`, action, ec2Xmlns(), generateUUID(), ruleSetXML.String(), action)
}

// ec2PermissionHasAnySource reports whether a permission still has at least one
// target (CIDR, prefix list, or security-group reference).
func ec2PermissionHasAnySource(p EC2IpPermission) bool {
	return len(p.IpRanges) > 0 || len(p.Ipv6Ranges) > 0 || len(p.PrefixListIds) > 0 || len(p.UserIdGroupPairs) > 0
}

// ec2RemoveRuleSource removes the source described by rule from the permission
// list. If the matching permission has other sources, only the revoked source is
// dropped; otherwise the whole permission is removed. The rule-to-permission
// match is on protocol + ports + one of the four source types.
func ec2RemoveRuleSource(perms []EC2IpPermission, rule EC2SecurityGroupRule) []EC2IpPermission {
	var result []EC2IpPermission
	for _, p := range perms {
		if p.IpProtocol != rule.IpProtocol || p.FromPort != rule.FromPort || p.ToPort != rule.ToPort {
			result = append(result, p)
			continue
		}
		var newPerm EC2IpPermission
		newPerm.IpProtocol = p.IpProtocol
		newPerm.FromPort = p.FromPort
		newPerm.ToPort = p.ToPort
		removed := false
		for _, r := range p.IpRanges {
			if rule.CidrIpv4 != "" && r.CidrIp == rule.CidrIpv4 {
				removed = true
				continue
			}
			newPerm.IpRanges = append(newPerm.IpRanges, r)
		}
		for _, r := range p.Ipv6Ranges {
			if rule.CidrIpv6 != "" && r.CidrIpv6 == rule.CidrIpv6 {
				removed = true
				continue
			}
			newPerm.Ipv6Ranges = append(newPerm.Ipv6Ranges, r)
		}
		for _, r := range p.PrefixListIds {
			if rule.PrefixListId != "" && r.PrefixListId == rule.PrefixListId {
				removed = true
				continue
			}
			newPerm.PrefixListIds = append(newPerm.PrefixListIds, r)
		}
		for _, r := range p.UserIdGroupPairs {
			if rule.RefGroupId != "" && r.GroupId == rule.RefGroupId {
				removed = true
				continue
			}
			newPerm.UserIdGroupPairs = append(newPerm.UserIdGroupPairs, r)
		}
		switch {
		case removed && ec2PermissionHasAnySource(newPerm):
			result = append(result, newPerm)
		case !removed:
			result = append(result, p)
		}
	}
	return result
}

// ec2RevokeByRuleIDs removes the standalone SecurityGroupRule rows identified by
// ruleIDs and drops their sources from the legacy IpPermissions list on the
// security group. Rule IDs that do not exist are ignored, matching AWS's
// idempotent behavior for revoke-by-id. Rule IDs that exist but belong to a
// different security group or direction are also ignored so the call cannot
// accidentally mutate another group's rules.
func ec2RevokeByRuleIDs(groupId string, ruleIDs []string, isEgress bool) {
	ec2SecurityGroups.Update(groupId, func(sg *EC2SecurityGroup) {
		for _, ruleID := range ruleIDs {
			rule, ok := ec2SecurityGroupRules.Get(ruleID)
			if !ok || rule.GroupId != groupId || rule.IsEgress != isEgress {
				continue
			}
			ec2SecurityGroupRules.Delete(ruleID)
			if isEgress {
				sg.IpPermissionsEgress = ec2RemoveRuleSource(sg.IpPermissionsEgress, rule)
			} else {
				sg.IpPermissions = ec2RemoveRuleSource(sg.IpPermissions, rule)
			}
		}
	})
}

// ec2RevokeSecurityGroup handles both RevokeSecurityGroupIngress and
// RevokeSecurityGroupEgress. It supports spec-based revocation (legacy) and
// rule-id-based revocation; the latter is idempotent for missing IDs.
func ec2RevokeSecurityGroup(w http.ResponseWriter, r *http.Request, isEgress bool) {
	groupId := r.FormValue("GroupId")

	responseTag := "RevokeSecurityGroupIngressResponse"
	direction := "ingress"
	if isEgress {
		responseTag = "RevokeSecurityGroupEgressResponse"
		direction = "egress"
	}

	ruleIDs := ec2ParamList(r, "SecurityGroupRuleId")
	if len(ruleIDs) > 0 {
		ec2RevokeByRuleIDs(groupId, ruleIDs, isEgress)
		if err := ec2ReapplyRealSecurityGroup(r.Context(), groupId); err != nil {
			ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to program real security group %s rules: %v", direction, err), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<%s %s>
  <requestId>%s</requestId><return>true</return>
</%s>`, responseTag, ec2Xmlns(), generateUUID(), responseTag)
		return
	}

	perm := parseIpPermission(r, "IpPermissions.1")

	var found bool
	ec2SecurityGroups.Update(groupId, func(sg *EC2SecurityGroup) {
		if isEgress {
			found = ec2PermissionExists(sg.IpPermissionsEgress, perm)
			if found {
				sg.IpPermissionsEgress = removePermission(sg.IpPermissionsEgress, perm)
			}
		} else {
			found = ec2PermissionExists(sg.IpPermissions, perm)
			if found {
				sg.IpPermissions = removePermission(sg.IpPermissions, perm)
			}
		}
	})
	if !found {
		ec2ErrorXML(w, "InvalidPermission.NotFound",
			"The specified rule does not exist in this security group.",
			http.StatusBadRequest)
		return
	}
	deleteSecurityGroupRules(groupId, perm, isEgress)
	if err := ec2ReapplyRealSecurityGroup(r.Context(), groupId); err != nil {
		ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to program real security group %s rules: %v", direction, err), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%s %s>
  <requestId>%s</requestId><return>true</return>
</%s>`, responseTag, ec2Xmlns(), generateUUID(), responseTag)
}

func handleRevokeSecurityGroupIngress(w http.ResponseWriter, r *http.Request) {
	ec2RevokeSecurityGroup(w, r, false)
}

func handleRevokeSecurityGroupEgress(w http.ResponseWriter, r *http.Request) {
	ec2RevokeSecurityGroup(w, r, true)
}

func handleDescribeSecurityGroupRules(w http.ResponseWriter, r *http.Request) {
	// Check for direct SecurityGroupRuleId params
	var ruleIds []string
	for i := 1; ; i++ {
		id := r.FormValue(fmt.Sprintf("SecurityGroupRuleId.%d", i))
		if id == "" {
			break
		}
		ruleIds = append(ruleIds, id)
	}

	// Apply the documented filters. Previously only group-id was honored, so a
	// query scoped by is-egress / security-group-rule-id / a tag returned every
	// rule in the account.
	filters := ec2Filters(r)

	var rules []EC2SecurityGroupRule
	if len(ruleIds) > 0 {
		for _, id := range ruleIds {
			if rule, ok := ec2SecurityGroupRules.Get(id); ok {
				rules = append(rules, rule)
			}
		}
	} else {
		for _, rule := range ec2SecurityGroupRules.List() {
			if ec2SecurityGroupRuleMatchesFilters(rule, filters) {
				rules = append(rules, rule)
			}
		}
	}

	var items strings.Builder
	for _, rule := range rules {
		items.WriteString(sgrItemXML(rule))
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSecurityGroupRulesResponse %s>
  <requestId>%s</requestId>
  <securityGroupRuleSet>%s</securityGroupRuleSet>
</DescribeSecurityGroupRulesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// ec2SecurityGroupRuleMatchesFilters applies the DescribeSecurityGroupRules
// filter set (group-id, security-group-rule-id, is-egress, group-owner-id,
// cidr, description, tag:<key>/tag-key) to one rule.
func ec2SecurityGroupRuleMatchesFilters(rule EC2SecurityGroupRule, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "group-id":
			if !ec2StrInValues(rule.GroupId, vals) {
				return false
			}
		case "security-group-rule-id":
			if !ec2StrInValues(rule.RuleId, vals) {
				return false
			}
		case "is-egress":
			if ec2StrInValues("true", vals) != rule.IsEgress {
				return false
			}
		case "group-owner-id":
			if !ec2StrInValues(rule.GroupOwner, vals) {
				return false
			}
		case "cidr":
			if !ec2StrInValues(rule.CidrIpv4, vals) && !ec2StrInValues(rule.CidrIpv6, vals) {
				return false
			}
		case "description":
			if !ec2StrInValues(rule.Description, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, rule.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

// handleModifySecurityGroupRules updates existing rule attributes in place.
// terraform-provider-aws v6 calls this for an in-place change to an
// aws_vpc_security_group_{ingress,egress}_rule instead of revoke + authorize.
func handleModifySecurityGroupRules(w http.ResponseWriter, r *http.Request) {
	for i := 1; ; i++ {
		base := fmt.Sprintf("SecurityGroupRule.%d", i)
		ruleID := r.FormValue(base + ".SecurityGroupRuleId")
		if ruleID == "" {
			break
		}
		rule, ok := ec2SecurityGroupRules.Get(ruleID)
		if !ok {
			ec2ErrorXML(w, "InvalidSecurityGroupRuleId.NotFound",
				fmt.Sprintf("The security group rule ID %q does not exist", ruleID), http.StatusBadRequest)
			return
		}
		sr := base + ".SecurityGroupRule"
		if v := r.FormValue(sr + ".Description"); v != "" {
			rule.Description = v
		}
		if v := r.FormValue(sr + ".IpProtocol"); v != "" {
			rule.IpProtocol = v
		}
		if v := r.FormValue(sr + ".FromPort"); v != "" {
			if _, err := fmt.Sscanf(v, "%d", &rule.FromPort); err != nil {
				rule.FromPort = 0
			}
		}
		if v := r.FormValue(sr + ".ToPort"); v != "" {
			if _, err := fmt.Sscanf(v, "%d", &rule.ToPort); err != nil {
				rule.ToPort = 0
			}
		}
		if v := r.FormValue(sr + ".CidrIpv4"); v != "" {
			rule.CidrIpv4 = v
		}
		if v := r.FormValue(sr + ".ReferencedGroupId"); v != "" {
			rule.RefGroupId = v
		}
		ec2SecurityGroupRules.Put(ruleID, rule)
	}
	ec2WriteSimpleResponse(w, "ModifySecurityGroupRulesResponse")
}

// updateSecurityGroupRuleDescriptions sets the description on the rule rows (and
// inline-permission sources) matching the given permission — the legacy
// aws_security_group inline-block path (UpdateSecurityGroupRuleDescriptions*).
func updateSecurityGroupRuleDescriptions(groupId string, perm EC2IpPermission, isEgress bool) {
	descFor := func(rule EC2SecurityGroupRule) (string, bool) {
		switch {
		case rule.CidrIpv4 != "":
			for _, ipr := range perm.IpRanges {
				if ipr.CidrIp == rule.CidrIpv4 {
					return ipr.Description, true
				}
			}
		case rule.CidrIpv6 != "":
			for _, ipr := range perm.Ipv6Ranges {
				if ipr.CidrIpv6 == rule.CidrIpv6 {
					return ipr.Description, true
				}
			}
		case rule.RefGroupId != "":
			for _, gp := range perm.UserIdGroupPairs {
				if gp.GroupId == rule.RefGroupId {
					return gp.Description, true
				}
			}
		}
		return "", false
	}
	for _, rule := range ec2SecurityGroupRules.List() {
		if rule.GroupId != groupId || rule.IsEgress != isEgress ||
			rule.IpProtocol != perm.IpProtocol || rule.FromPort != perm.FromPort || rule.ToPort != perm.ToPort {
			continue
		}
		if desc, ok := descFor(rule); ok {
			rule.Description = desc
			ec2SecurityGroupRules.Put(rule.RuleId, rule)
		}
	}
}

func handleUpdateSecurityGroupRuleDescriptionsIngress(w http.ResponseWriter, r *http.Request) {
	groupId := r.FormValue("GroupId")
	updateSecurityGroupRuleDescriptions(groupId, parseIpPermission(r, "IpPermissions.1"), false)
	ec2WriteSimpleResponse(w, "UpdateSecurityGroupRuleDescriptionsIngressResponse")
}

func handleUpdateSecurityGroupRuleDescriptionsEgress(w http.ResponseWriter, r *http.Request) {
	groupId := r.FormValue("GroupId")
	updateSecurityGroupRuleDescriptions(groupId, parseIpPermission(r, "IpPermissions.1"), true)
	ec2WriteSimpleResponse(w, "UpdateSecurityGroupRuleDescriptionsEgressResponse")
}

// ---- Instances ----

func ec2ParamList(r *http.Request, prefix string) []string {
	var values []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.%d", prefix, i))
		if v == "" {
			break
		}
		values = append(values, v)
	}
	return values
}

func ec2ErrorXML(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<Response><Errors><Error><Code>%s</Code><Message>%s</Message></Error></Errors><RequestID>%s</RequestID></Response>`,
		xmlEscape(code), xmlEscape(message), generateUUID())
}

func ec2Filters(r *http.Request) map[string][]string {
	filters := map[string][]string{}
	for i := 1; ; i++ {
		name := r.FormValue(fmt.Sprintf("Filter.%d.Name", i))
		if name == "" {
			break
		}
		for j := 1; ; j++ {
			value := r.FormValue(fmt.Sprintf("Filter.%d.Value.%d", i, j))
			if value == "" {
				break
			}
			filters[name] = append(filters[name], value)
		}
	}
	return filters
}

// ec2StrInValues reports whether s is one of vals (a filter's values are OR'd
// in real EC2).
func ec2StrInValues(s string, vals []string) bool {
	for _, v := range vals {
		if v == s {
			return true
		}
	}
	return false
}

func instanceStateCode(state string) int {
	switch state {
	case "pending":
		return 0
	case "running":
		return 16
	case "shutting-down":
		return 32
	case "terminated":
		return 48
	case "stopping":
		return 64
	case "stopped":
		return 80
	default:
		return 0
	}
}

func ec2InstanceXML(inst EC2Instance) string {
	var groups strings.Builder
	for _, groupID := range inst.SecurityGroupIds {
		name := groupID
		if sg, ok := ec2SecurityGroups.Get(groupID); ok {
			name = sg.GroupName
		}
		fmt.Fprintf(&groups, "<item><groupId>%s</groupId><groupName>%s</groupName></item>", groupID, name)
	}
	if groups.Len() == 0 {
		groups.WriteString("")
	}
	var ni strings.Builder
	if inst.NetworkInterfaceId != "" {
		fmt.Fprintf(&ni, `<networkInterfaceSet><item>
      <networkInterfaceId>%s</networkInterfaceId>
      <subnetId>%s</subnetId>
      <vpcId>%s</vpcId>
      <description/>
      <ownerId>%s</ownerId>
      <status>in-use</status>
      <macAddress>02:00:00:00:00:01</macAddress>
      <privateIpAddress>%s</privateIpAddress>
      <privateDnsName>ip-%s.%s.compute.internal</privateDnsName>
      <sourceDestCheck>true</sourceDestCheck>
      <groupSet>%s</groupSet>
      <attachment><attachmentId>eni-attach-%s</attachmentId><deviceIndex>0</deviceIndex><status>attached</status><attachTime>%s</attachTime><deleteOnTermination>true</deleteOnTermination></attachment>
    </item></networkInterfaceSet>`,
			inst.NetworkInterfaceId, inst.SubnetId, inst.VpcId, ec2Owner(), inst.PrivateIpAddress,
			strings.ReplaceAll(inst.PrivateIpAddress, ".", "-"), awsRegion(), groups.String(), inst.NetworkInterfaceId, inst.LaunchTime)
	} else {
		ni.WriteString("<networkInterfaceSet/>")
	}
	monitoringState := "disabled"
	if inst.Monitoring {
		monitoringState = "enabled"
	}
	keyNameXML := ""
	if inst.KeyName != "" {
		keyNameXML = fmt.Sprintf("<keyName>%s</keyName>", inst.KeyName)
	}
	iamXML := ""
	if inst.IamInstanceProfileArn != "" || inst.IamInstanceProfileName != "" {
		arn := inst.IamInstanceProfileArn
		if arn == "" {
			arn = fmt.Sprintf("arn:aws:iam::%s:instance-profile/%s", ec2Owner(), inst.IamInstanceProfileName)
		}
		iamXML = fmt.Sprintf("<iamInstanceProfile><arn>%s</arn><id>AIPA%s</id></iamInstanceProfile>",
			arn, strings.TrimPrefix(inst.InstanceId, "i-"))
	}
	cpuXML := ""
	if inst.CpuCoreCount > 0 {
		threads := inst.CpuThreadsPerCore
		if threads == 0 {
			threads = 1
		}
		cpuXML = fmt.Sprintf("<cpuOptions><coreCount>%d</coreCount><threadsPerCore>%d</threadsPerCore></cpuOptions>",
			inst.CpuCoreCount, threads)
	}
	metaTokens := inst.MetadataHttpTokens
	if metaTokens == "" {
		metaTokens = "optional"
	}
	metaEndpoint := inst.MetadataHttpEndpoint
	if metaEndpoint == "" {
		metaEndpoint = "enabled"
	}
	metaHop := inst.MetadataHopLimit
	if metaHop == 0 {
		metaHop = 1
	}
	metaTags := inst.MetadataInstanceTags
	if metaTags == "" {
		metaTags = "disabled"
	}
	metaXML := fmt.Sprintf("<metadataOptions><state>applied</state><httpTokens>%s</httpTokens><httpPutResponseHopLimit>%d</httpPutResponseHopLimit><httpEndpoint>%s</httpEndpoint><httpProtocolIpv6>disabled</httpProtocolIpv6><instanceMetadataTags>%s</instanceMetadataTags></metadataOptions>",
		metaTokens, metaHop, metaEndpoint, metaTags)
	publicXML := ""
	if inst.PublicIpAddress != "" {
		publicXML = fmt.Sprintf("<ipAddress>%s</ipAddress>", inst.PublicIpAddress)
	}
	reasonXML := "<reason/>"
	if inst.StateTransitionReason != "" {
		reasonXML = fmt.Sprintf("<reason>%s</reason>", inst.StateTransitionReason)
	}
	stateReasonXML := ""
	if inst.StateReasonCode != "" {
		stateReasonXML = fmt.Sprintf("\n    <stateReason><code>%s</code><message>%s</message></stateReason>",
			inst.StateReasonCode, inst.StateReasonMessage)
	}
	return fmt.Sprintf(`<item>
    <instanceId>%s</instanceId>
    <imageId>%s</imageId>
    <instanceState><code>%d</code><name>%s</name></instanceState>
    <privateDnsName>ip-%s.%s.compute.internal</privateDnsName>
    <dnsName/>
    %s
    %s
    <amiLaunchIndex>0</amiLaunchIndex>
    <productCodes/>
    <instanceType>%s</instanceType>
    <launchTime>%s</launchTime>
    <placement><availabilityZone>%s</availabilityZone><groupName/><tenancy>default</tenancy></placement>
    <monitoring><state>%s</state></monitoring>
    <subnetId>%s</subnetId>
    <vpcId>%s</vpcId>
    <privateIpAddress>%s</privateIpAddress>%s
    <sourceDestCheck>%t</sourceDestCheck>
    <groupSet>%s</groupSet>%s
    <architecture>%s</architecture>
    <rootDeviceType>ebs</rootDeviceType>
    <rootDeviceName>%s</rootDeviceName>
    <blockDeviceMapping><item><deviceName>%s</deviceName><ebs><volumeId>%s</volumeId><status>attached</status><attachTime>%s</attachTime><deleteOnTermination>true</deleteOnTermination></ebs></item></blockDeviceMapping>
    <virtualizationType>hvm</virtualizationType>
    <clientToken/>
    <ebsOptimized>%t</ebsOptimized>
    %s%s%s
    %s
    %s
  </item>`,
		inst.InstanceId, inst.ImageId, instanceStateCode(inst.State), inst.State,
		strings.ReplaceAll(inst.PrivateIpAddress, ".", "-"), awsRegion(), reasonXML, keyNameXML, inst.InstanceType, inst.LaunchTime,
		awsAvailabilityZone(), monitoringState, inst.SubnetId, inst.VpcId, inst.PrivateIpAddress, publicXML, inst.SourceDestCheck, groups.String(), stateReasonXML,
		inst.Architecture, inst.RootDeviceName, inst.RootDeviceName, "vol-"+strings.TrimPrefix(inst.InstanceId, "i-"), inst.LaunchTime,
		inst.EbsOptimized, iamXML, metaXML, cpuXML,
		writeTagSetXML(inst.Tags), ni.String())
}

// runInstancesLaunchTemplate resolves a RunInstances LaunchTemplate spec to the
// owning template's id/name, the resolved version, and the effective version
// data. Returns ok=false when no LaunchTemplate was supplied.
func runInstancesLaunchTemplate(r *http.Request) (id, name, version string, data EC2LaunchTemplateData, ok bool) {
	reqId := r.FormValue("LaunchTemplate.LaunchTemplateId")
	reqName := r.FormValue("LaunchTemplate.LaunchTemplateName")
	if reqId == "" && reqName == "" {
		return "", "", "", EC2LaunchTemplateData{}, false
	}
	lt, found := lookupLaunchTemplate(reqId, reqName)
	if !found {
		return "", "", "", EC2LaunchTemplateData{}, false
	}
	reqVersion := r.FormValue("LaunchTemplate.Version")
	verNum := lt.DefaultVersionNumber
	switch reqVersion {
	case "", "$Default":
		verNum = lt.DefaultVersionNumber
	case "$Latest":
		verNum = lt.LatestVersionNumber
	default:
		if _, err := fmt.Sscanf(reqVersion, "%d", &verNum); err != nil {
			verNum = lt.DefaultVersionNumber
		}
	}
	for _, v := range lt.Versions {
		if v.VersionNumber == verNum {
			data = v.Data
			break
		}
	}
	// DescribeInstances echoes the resolved numeric version (real AWS does too;
	// the provider's $Latest/$Default config is diff-suppressed against it).
	return lt.LaunchTemplateId, lt.LaunchTemplateName, fmt.Sprintf("%d", verNum), data, true
}

func runInstancesSecurityGroups(r *http.Request) []string {
	var groups []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("SecurityGroupId.%d", i))
		if v == "" {
			break
		}
		groups = append(groups, v)
	}
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("NetworkInterface.1.SecurityGroupId.%d", i))
		if v == "" {
			break
		}
		groups = append(groups, v)
	}
	return groups
}

// ec2FormOrLT returns the request value for key, falling back to the
// launch-template value (applying LT data to the instance is the documented
// behaviour, not a synthetic default).
func ec2FormOrLT(r *http.Request, key, ltVal string) string {
	if v := r.FormValue(key); v != "" {
		return v
	}
	return ltVal
}

func ec2BoolStr(s string) bool { return s == "true" || s == "1" }

func ec2AtoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

// runInstancesRootBlockDevice resolves the root volume size/type from the
// request's BlockDeviceMapping (matched by the sim's root device name, else the
// sole mapping), else the launch template's. Zero/"" means "use the AWS default"
// (resolved in ec2CreateInstance).
func runInstancesRootBlockDevice(r *http.Request, lt EC2LaunchTemplateData) (int, string) {
	isRoot := func(dn string) bool {
		return dn == "/dev/sda1" || dn == "/dev/xvda" || dn == "/dev/xvda1"
	}
	var reqDev []struct{ dn, size, vtype string }
	for i := 1; ; i++ {
		dn := r.FormValue(fmt.Sprintf("BlockDeviceMapping.%d.DeviceName", i))
		if dn == "" {
			break
		}
		reqDev = append(reqDev, struct{ dn, size, vtype string }{
			dn,
			r.FormValue(fmt.Sprintf("BlockDeviceMapping.%d.Ebs.VolumeSize", i)),
			r.FormValue(fmt.Sprintf("BlockDeviceMapping.%d.Ebs.VolumeType", i)),
		})
	}
	for _, d := range reqDev {
		if isRoot(d.dn) || len(reqDev) == 1 {
			return ec2AtoiOr(d.size, 0), d.vtype
		}
	}
	for _, b := range lt.BlockDeviceMappings {
		if b.Ebs != nil && (isRoot(b.DeviceName) || len(lt.BlockDeviceMappings) == 1) {
			return ec2AtoiOr(b.Ebs.VolumeSize, 0), b.Ebs.VolumeType
		}
	}
	return 0, ""
}

func handleRunInstances(w http.ResponseWriter, r *http.Request) {
	// ClientToken idempotency: the aws-sdk-go-v2 auto-fills ClientToken (it
	// carries the Smithy idempotencyToken trait) and re-sends the same value on
	// every retry. A retried RunInstances must replay the original reservation,
	// not launch a duplicate batch — matching real EC2 within its idempotency
	// window.
	clientToken := r.FormValue("ClientToken")
	if clientToken != "" {
		if prev, ok := ec2RunTokens.Get(clientToken); ok {
			writeRunInstancesReplay(w, prev)
			return
		}
	}
	minCount, maxCount, ok := runInstancesCounts(w, r)
	if !ok {
		return
	}
	// A LaunchTemplate spec supplies the instance's image/type (unless the
	// request overrides them) and is echoed back by DescribeInstances.
	ltID, _, ltVersion, ltData, hasLT := runInstancesLaunchTemplate(r)
	imageID := r.FormValue("ImageId")
	if imageID == "" {
		imageID = ltData.ImageId
	}
	if imageID == "" {
		imageID = "ami-simulated"
	}
	instanceType := r.FormValue("InstanceType")
	if instanceType == "" {
		instanceType = ltData.InstanceType
	}
	if instanceType == "" {
		instanceType = "t3.micro"
	}
	subnetID := r.FormValue("SubnetId")
	if subnetID == "" {
		subnetID = r.FormValue("NetworkInterface.1.SubnetId")
	}
	if subnetID == "" {
		// No subnet specified — launch into the account's default VPC subnet,
		// as real EC2 does.
		subnetID = defaultVPCSubnetID()
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		sim.AWSErrorf(w, "InvalidSubnetID.NotFound", http.StatusBadRequest, "The subnet ID %q does not exist", subnetID)
		return
	}
	// The instance is always modeled at the control plane (reaches "running",
	// describable) — like VPC/subnet/NAT. A real Firecracker VM is booted
	// opportunistically in ec2TransitionInstanceToRunning only when the host
	// has VM capabilities; their absence must not fail RunInstances, so
	// IaC/control-plane testing works in SIM_RUNTIME=process.
	reservationID := ec2ID("r")
	sgIDs := runInstancesSecurityGroups(r)
	if len(sgIDs) == 0 {
		for _, sg := range ec2SecurityGroups.Filter(func(sg EC2SecurityGroup) bool {
			return sg.VpcId == subnet.VpcId && sg.GroupName == "default"
		}) {
			sgIDs = append(sgIDs, sg.GroupId)
			break
		}
	}
	launchTime := time.Now().UTC().Format(time.RFC3339)
	tags := parseTags(r)

	// Instance knobs: request value, else launch-template value, else the AWS
	// default the API documents (metadata options). These are the fields
	// aws_instance reads back; dropping them forces drift every plan.
	var ltMetaTokens, ltMetaEndpoint, ltMetaHop, ltMetaTags string
	if m := ltData.MetadataOptions; m != nil {
		ltMetaTokens, ltMetaEndpoint, ltMetaHop, ltMetaTags = m.HttpTokens, m.HttpEndpoint, m.HttpPutResponseHopLimit, m.InstanceMetadataTags
	}
	metaTokens := ec2FormOrLT(r, "MetadataOptions.HttpTokens", ltMetaTokens)
	if metaTokens == "" {
		metaTokens = "optional"
	}
	metaEndpoint := ec2FormOrLT(r, "MetadataOptions.HttpEndpoint", ltMetaEndpoint)
	if metaEndpoint == "" {
		metaEndpoint = "enabled"
	}
	metaTags := ec2FormOrLT(r, "MetadataOptions.InstanceMetadataTags", ltMetaTags)
	if metaTags == "" {
		metaTags = "disabled"
	}
	rootSize, rootType := runInstancesRootBlockDevice(r, ltData)
	base := EC2InstanceCreateSpec{
		Context:                r.Context(),
		ReservationId:          reservationID,
		ImageId:                imageID,
		InstanceType:           instanceType,
		Subnet:                 subnet,
		SubnetId:               subnetID,
		SecurityGroupIds:       sgIDs,
		Tags:                   tags,
		LaunchTime:             launchTime,
		State:                  "pending",
		KeyName:                ec2FormOrLT(r, "KeyName", ltData.KeyName),
		IamInstanceProfileArn:  ec2FormOrLT(r, "IamInstanceProfile.Arn", ltData.IamInstanceProfileArn),
		IamInstanceProfileName: ec2FormOrLT(r, "IamInstanceProfile.Name", ltData.IamInstanceProfileName),
		EbsOptimized:           ec2BoolStr(ec2FormOrLT(r, "EbsOptimized", ltData.EbsOptimized)),
		Monitoring:             ec2BoolStr(ec2FormOrLT(r, "Monitoring.Enabled", ltData.MonitoringEnabled)),
		UserData:               ec2FormOrLT(r, "UserData", ltData.UserData),
		DisableApiTermination:  ec2BoolStr(ec2FormOrLT(r, "DisableApiTermination", ltData.DisableApiTermination)),
		SourceDestCheck:        true, // AWS default; ModifyInstanceAttribute can disable it
		CpuCoreCount:           ec2AtoiOr(r.FormValue("CpuOptions.CoreCount"), 0),
		CpuThreadsPerCore:      ec2AtoiOr(r.FormValue("CpuOptions.ThreadsPerCore"), 0),
		RootVolumeSize:         rootSize,
		RootVolumeType:         rootType,
		MetadataHttpTokens:     metaTokens,
		MetadataHttpEndpoint:   metaEndpoint,
		MetadataHopLimit:       ec2AtoiOr(ec2FormOrLT(r, "MetadataOptions.HttpPutResponseHopLimit", ltMetaHop), 1),
		MetadataInstanceTags:   metaTags,
	}

	var instances []EC2Instance
	for i := 0; i < maxCount; i++ {
		privateIP := ""
		if i == 0 {
			privateIP = r.FormValue("PrivateIpAddress")
			if privateIP == "" {
				privateIP = r.FormValue("NetworkInterface.1.PrivateIpAddress")
			}
		}
		if privateIP == "" {
			ip, err := AllocateSubnetIP(subnetID)
			if err != nil {
				if i < minCount {
					sim.AWSError(w, "InsufficientFreeAddressesInSubnet", err.Error(), http.StatusBadRequest)
					return
				}
				break
			}
			privateIP = ip
		}
		spec := base
		spec.PrivateIP = privateIP
		inst, err := ec2CreateInstance(spec)
		if err != nil {
			if i < minCount {
				ec2ErrorXML(w, "InsufficientFreeAddressesInSubnet", fmt.Sprintf("failed to attach real EC2 network interface: %v", err), http.StatusServiceUnavailable)
				return
			}
			break
		}
		if hasLT {
			// Real AWS records launch-template provenance as system tags on the
			// instance (DescribeInstances has no LaunchTemplate field); the
			// provider flattens aws_instance.launch_template from these, so their
			// absence forces a destroy+create every plan.
			inst.Tags = append(inst.Tags,
				EC2Tag{Key: "aws:ec2launchtemplate:id", Value: ltID},
				EC2Tag{Key: "aws:ec2launchtemplate:version", Value: ltVersion})
			ec2Instances.Put(inst.InstanceId, inst)
		}
		instances = append(instances, inst)
		go ec2TransitionInstanceToRunning(inst.InstanceId)
	}

	// Record the reservation under its ClientToken so a retried RunInstances
	// replays these exact instances instead of launching a duplicate batch.
	if clientToken != "" && len(instances) > 0 {
		ids := make([]string, 0, len(instances))
		for _, inst := range instances {
			ids = append(ids, inst.InstanceId)
		}
		ec2RunTokens.Put(clientToken, EC2RunInstancesToken{
			Token: clientToken, ReservationId: reservationID, InstanceIds: ids,
			LaunchInstances: instances,
		})
	}

	writeRunInstancesResponse(w, reservationID, instances)
}

// writeRunInstancesResponse renders the RunInstances XML for a reservation.
func writeRunInstancesResponse(w http.ResponseWriter, reservationID string, instances []EC2Instance) {
	var instanceItems strings.Builder
	for _, inst := range instances {
		instanceItems.WriteString(ec2InstanceXML(inst))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RunInstancesResponse %s>
  <requestId>%s</requestId>
  <reservationId>%s</reservationId>
  <ownerId>%s</ownerId>
  <groupSet/>
  <instancesSet>%s</instancesSet>
</RunInstancesResponse>`, ec2Xmlns(), generateUUID(), reservationID, ec2Owner(), instanceItems.String())
}

// writeRunInstancesReplay re-renders the original reservation for an idempotent
// RunInstances retry, re-fetching each instance's current state from the store.
func writeRunInstancesReplay(w http.ResponseWriter, tok EC2RunInstancesToken) {
	// Replay the exact response the original call returned (instances in their
	// launch-time "pending" state), captured in the token. Re-reading the live
	// control-plane state here raced the asynchronous pending->running
	// transition, so a retried RunInstances could report "running".
	instances := tok.LaunchInstances
	if instances == nil {
		// Tokens persisted before LaunchInstances was recorded fall back to the
		// instance ids; the launch state is still "pending" for the replay.
		for _, id := range tok.InstanceIds {
			if inst, ok := ec2Instances.Get(id); ok {
				inst.State = "pending"
				instances = append(instances, inst)
			}
		}
	}
	writeRunInstancesResponse(w, tok.ReservationId, instances)
}

func runInstancesCounts(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	minCount, maxCount := 1, 1
	if v := r.FormValue("MinCount"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &minCount); err != nil || minCount < 1 {
			ec2ErrorXML(w, "InvalidParameterValue", "MinCount must be greater than 0", http.StatusBadRequest)
			return 0, 0, false
		}
	}
	if v := r.FormValue("MaxCount"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &maxCount); err != nil || maxCount < 1 {
			ec2ErrorXML(w, "InvalidParameterValue", "MaxCount must be greater than 0", http.StatusBadRequest)
			return 0, 0, false
		}
	}
	if minCount > maxCount {
		ec2ErrorXML(w, "InvalidParameterCombination", "MinCount cannot be greater than MaxCount", http.StatusBadRequest)
		return 0, 0, false
	}
	return minCount, maxCount, true
}

type EC2InstanceCreateSpec struct {
	Context                context.Context
	ReservationId          string
	ImageId                string
	InstanceType           string
	Subnet                 EC2Subnet
	SubnetId               string
	PrivateIP              string
	SecurityGroupIds       []string
	Tags                   []EC2Tag
	LaunchTime             string
	KeyName                string
	State                  string
	IamInstanceProfileArn  string
	IamInstanceProfileName string
	EbsOptimized           bool
	Monitoring             bool
	UserData               string
	DisableApiTermination  bool
	SourceDestCheck        bool
	CpuCoreCount           int
	CpuThreadsPerCore      int
	RootVolumeSize         int
	RootVolumeType         string
	MetadataHttpTokens     string
	MetadataHttpEndpoint   string
	MetadataHopLimit       int
	MetadataInstanceTags   string
}

func ec2CreateInstance(spec EC2InstanceCreateSpec) (EC2Instance, error) {
	if spec.State == "" {
		spec.State = "pending"
	}
	if spec.Context == nil {
		spec.Context = context.Background()
	}
	instanceID := ec2ID("i")
	eniID := ec2ID("eni")
	rootDevice := "/dev/sda1"
	rootVolSize := spec.RootVolumeSize
	if rootVolSize == 0 {
		rootVolSize = 8
	}
	rootVolType := spec.RootVolumeType
	if rootVolType == "" {
		rootVolType = "gp3"
	}
	inst := EC2Instance{
		InstanceId:             instanceID,
		ReservationId:          spec.ReservationId,
		ImageId:                spec.ImageId,
		InstanceType:           spec.InstanceType,
		SubnetId:               spec.SubnetId,
		VpcId:                  spec.Subnet.VpcId,
		State:                  spec.State,
		PrivateIpAddress:       spec.PrivateIP,
		SecurityGroupIds:       spec.SecurityGroupIds,
		Tags:                   spec.Tags,
		LaunchTime:             spec.LaunchTime,
		KeyName:                spec.KeyName,
		Architecture:           "x86_64",
		RootDeviceName:         rootDevice,
		NetworkInterfaceId:     eniID,
		IamInstanceProfileArn:  spec.IamInstanceProfileArn,
		IamInstanceProfileName: spec.IamInstanceProfileName,
		EbsOptimized:           spec.EbsOptimized,
		Monitoring:             spec.Monitoring,
		UserData:               spec.UserData,
		DisableApiTermination:  spec.DisableApiTermination,
		SourceDestCheck:        spec.SourceDestCheck,
		CpuCoreCount:           spec.CpuCoreCount,
		CpuThreadsPerCore:      spec.CpuThreadsPerCore,
		RootVolumeSize:         rootVolSize,
		RootVolumeType:         rootVolType,
		MetadataHttpTokens:     spec.MetadataHttpTokens,
		MetadataHttpEndpoint:   spec.MetadataHttpEndpoint,
		MetadataHopLimit:       spec.MetadataHopLimit,
		MetadataInstanceTags:   spec.MetadataInstanceTags,
	}
	ec2Instances.Put(instanceID, inst)
	ec2NetworkInterfaces.Put(eniID, EC2NetworkInterface{
		NetworkInterfaceId:  eniID,
		SubnetId:            spec.SubnetId,
		VpcId:               spec.Subnet.VpcId,
		PrivateIpAddress:    spec.PrivateIP,
		Status:              "in-use",
		AttachmentId:        "eni-attach-" + eniID,
		InstanceId:          instanceID,
		DeviceIndex:         0,
		DeleteOnTermination: true,
		SecurityGroupIds:    spec.SecurityGroupIds,
		Tags:                spec.Tags,
		OwnerId:             ec2Owner(),
	})
	rootVolumeID := "vol-" + strings.TrimPrefix(instanceID, "i-")
	rootVolume := EC2Volume{
		VolumeId:         rootVolumeID,
		Size:             rootVolSize,
		SnapshotId:       "snap-" + strings.TrimPrefix(spec.ImageId, "ami-"),
		AvailabilityZone: spec.Subnet.AvailabilityZone,
		State:            "in-use",
		CreateTime:       spec.LaunchTime,
		VolumeType:       rootVolType,
		Tags:             spec.Tags,
		Attachments: []EC2VolumeAttachment{{
			VolumeId:            rootVolumeID,
			InstanceId:          instanceID,
			Device:              rootDevice,
			State:               "attached",
			AttachTime:          spec.LaunchTime,
			DeleteOnTermination: true,
		}},
		Data: []byte{},
	}
	rootVolume.HostPath = EBSVolumeHostDir(rootVolumeID)
	ec2Volumes.Put(rootVolumeID, rootVolume)
	return inst, nil
}

func ec2TransitionInstanceToRunning(instanceID string) {
	inst, ok := ec2Instances.Get(instanceID)
	if !ok {
		return
	}
	ec2Instances.Update(instanceID, func(inst *EC2Instance) {
		if inst.State == "pending" {
			inst.State = "running"
		}
	})
	// On a real-execution host, boot a real Firecracker VM after the EC2
	// control plane has converged to running. Host data-plane setup is not the
	// EC2 control plane, so a local Firecracker boot failure is reported to the
	// simulator logs without rewriting the instance's EC2 state.
	if ec2RealVMHostAvailable() {
		if err := ec2StartRealVM(context.Background(), inst); err != nil {
			fmt.Fprintf(os.Stderr, "failed to boot real EC2 instance %s: %v\n", instanceID, err)
			return
		}
	}
}

func handleDescribeInstances(w http.ResponseWriter, r *http.Request) {
	instanceIDs := ec2ParamList(r, "InstanceId")
	var instances []EC2Instance
	if len(instanceIDs) > 0 {
		for _, id := range instanceIDs {
			inst, ok := ec2Instances.Get(id)
			if !ok {
				sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", id)
				return
			}
			instances = append(instances, inst)
		}
	} else {
		instances = ec2Instances.List()
	}
	filters := ec2Filters(r)
	if len(filters) > 0 {
		var err error
		instances, err = filterEC2Instances(instances, filters)
		if err != nil {
			ec2ErrorXML(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
			return
		}
	}
	// MaxResults/NextToken pagination applies only to the list form (not when
	// explicit InstanceIds are requested), matching real EC2. Sort by id for a
	// stable offset cursor across pages.
	nextToken := ""
	if len(instanceIDs) == 0 {
		sort.Slice(instances, func(i, j int) bool { return instances[i].InstanceId < instances[j].InstanceId })
		instances, nextToken = awsPageExplicit(instances, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	}
	var reservations strings.Builder
	for _, inst := range instances {
		fmt.Fprintf(&reservations, `<item><reservationId>%s</reservationId><ownerId>%s</ownerId><groupSet/><instancesSet>%s</instancesSet></item>`,
			inst.ReservationId, ec2Owner(), ec2InstanceXML(inst))
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstancesResponse %s>
  <requestId>%s</requestId>
  <reservationSet>%s</reservationSet>%s
</DescribeInstancesResponse>`, ec2Xmlns(), generateUUID(), reservations.String(), nextTokenXML)
}

func filterEC2Instances(instances []EC2Instance, filters map[string][]string) ([]EC2Instance, error) {
	out := make([]EC2Instance, 0, len(instances))
	for _, inst := range instances {
		matches, err := ec2InstanceMatchesFilters(inst, filters)
		if err != nil {
			return nil, err
		}
		if matches {
			out = append(out, inst)
		}
	}
	return out, nil
}

func ec2InstanceMatchesFilters(inst EC2Instance, filters map[string][]string) (bool, error) {
	for name, values := range filters {
		matched := false
		for _, value := range values {
			switch {
			case name == "instance-id":
				matched = inst.InstanceId == value
			case name == "instance-state-name":
				matched = inst.State == value
			case name == "image-id":
				matched = inst.ImageId == value
			case name == "vpc-id":
				matched = inst.VpcId == value
			case name == "subnet-id":
				matched = inst.SubnetId == value
			case name == "private-ip-address":
				matched = inst.PrivateIpAddress == value
			case name == "network-interface.network-interface-id":
				matched = inst.NetworkInterfaceId == value
			case name == "instance-type":
				matched = inst.InstanceType == value
			case name == "key-name":
				matched = inst.KeyName == value
			case name == "availability-zone":
				matched = awsAvailabilityZone() == value
			case name == "group-id":
				matched = stringInSlice(value, inst.SecurityGroupIds)
			case name == "tag-key":
				matched = ec2HasTagKey(inst.Tags, value)
			case strings.HasPrefix(name, "tag:"):
				matched = ec2HasTagValue(inst.Tags, strings.TrimPrefix(name, "tag:"), value)
			default:
				return false, fmt.Errorf("the filter %q is invalid", name)
			}
			if matched {
				break
			}
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
}

func stringInSlice(needle string, values []string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func ec2HasTagKey(tags []EC2Tag, key string) bool {
	for _, tag := range tags {
		if tag.Key == key {
			return true
		}
	}
	return false
}

func ec2HasTagValue(tags []EC2Tag, key, value string) bool {
	for _, tag := range tags {
		if tag.Key == key && tag.Value == value {
			return true
		}
	}
	return false
}

// recoverEC2Instances reconciles persisted instance rows with the real
// workloads on the host after a control-plane restart. Firecracker VMs, their
// tap devices, and the IMDS bindings are process state and die with the
// simulator, so a persisted instance still marked running or pending has no
// backing workload; it transitions truthfully to stopped. VMs are never
// re-adopted — the processes are dead. Hosts in the API-only tier (no real VM
// execution) keep their purely modeled instance state unchanged, exactly as
// before the restart.
func recoverEC2Instances() {
	if !ec2RealVMHostAvailable() {
		return
	}
	recoverEC2InstancesWithVMLiveness(ec2RealVMAlive)
}

func recoverEC2InstancesWithVMLiveness(vmAlive func(instanceID string) bool) {
	for _, inst := range ec2Instances.List() {
		if inst.State != "running" && inst.State != "pending" {
			continue
		}
		if vmAlive(inst.InstanceId) {
			continue
		}
		transitioned := false
		ec2Instances.Update(inst.InstanceId, func(current *EC2Instance) {
			if current.State != "running" && current.State != "pending" {
				return
			}
			transitioned = true
			current.State = "stopped"
			current.StateTransitionReason = "Server.InternalError: Instance workload not found after control-plane restart"
			current.StateReasonCode = "Server.InternalError"
			current.StateReasonMessage = "Server.InternalError: Instance workload not found after control-plane restart"
		})
		if transitioned {
			fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: workload not found after control-plane restart; transitioned instance to stopped\n", inst.InstanceId)
		}
	}
}

func handleTerminateInstances(w http.ResponseWriter, r *http.Request) {
	writeInstanceStateChange(w, r, "terminated", true)
}

func handleStopInstances(w http.ResponseWriter, r *http.Request) {
	writeInstanceStateChange(w, r, "stopped", false)
}

func handleStartInstances(w http.ResponseWriter, r *http.Request) {
	writeInstanceStateChange(w, r, "running", false)
}

func writeInstanceStateChange(w http.ResponseWriter, r *http.Request, next string, deleteENI bool) {
	instanceIDs := ec2ParamList(r, "InstanceId")
	var items strings.Builder
	for _, id := range instanceIDs {
		inst, ok := ec2Instances.Get(id)
		if !ok {
			sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", id)
			return
		}
		prev := inst.State
		// Real VM start/stop only on a real-execution host; on an API-only host
		// the state change is purely modeled.
		if ec2RealVMHostAvailable() {
			if next == "running" {
				if err := ec2StartRealVM(r.Context(), inst); err != nil {
					fmt.Fprintf(os.Stderr, "failed to start real EC2 instance %s: %v\n", id, err)
					ec2ErrorXML(w, "IncorrectInstanceState", fmt.Sprintf("failed to start real EC2 instance: %v", err), http.StatusServiceUnavailable)
					return
				}
			}
			if next == "stopped" || next == "terminated" {
				if err := ec2StopRealVM(r.Context(), id); err != nil {
					ec2ErrorXML(w, "IncorrectInstanceState", fmt.Sprintf("failed to stop real EC2 instance: %v", err), http.StatusServiceUnavailable)
					return
				}
			}
		}
		inst.State = next
		switch next {
		case "running":
			inst.StateTransitionReason = ""
			inst.StateReasonCode = ""
			inst.StateReasonMessage = ""
		case "stopped", "terminated":
			inst.StateTransitionReason = "User initiated (" + time.Now().UTC().Format("2006-01-02 15:04:05") + " GMT)"
			inst.StateReasonCode = "Client.UserInitiatedShutdown"
			inst.StateReasonMessage = "Client.UserInitiatedShutdown: User initiated shutdown"
		}
		ec2Instances.Put(id, inst)
		if next == "stopped" {
			ec2UpdateVolumeAttachmentsForInstance(id, "attached", "in-use")
		}
		if next == "running" {
			ec2UpdateVolumeAttachmentsForInstance(id, "attached", "in-use")
		}
		if deleteENI && inst.NetworkInterfaceId != "" {
			ec2NetworkInterfaces.Delete(inst.NetworkInterfaceId)
			if ec2RealNetHostAvailable() {
				if err := ec2DeleteRealNIC(r.Context(), inst.NetworkInterfaceId); err != nil {
					ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("failed to delete real EC2 network interface: %v", err), http.StatusServiceUnavailable)
					return
				}
			}
			ec2DeleteOnTerminationVolumes(id)
		}
		fmt.Fprintf(&items, `<item><instanceId>%s</instanceId><currentState><code>%d</code><name>%s</name></currentState><previousState><code>%d</code><name>%s</name></previousState></item>`,
			id, instanceStateCode(next), next, instanceStateCode(prev), prev)
	}
	action := "StopInstances"
	if next == "running" {
		action = "StartInstances"
	} else if next == "terminated" {
		action = "TerminateInstances"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s>
  <requestId>%s</requestId>
  <instancesSet>%s</instancesSet>
</%sResponse>`, action, ec2Xmlns(), generateUUID(), items.String(), action)
}

// ec2UpdateVolumeAttachmentsForInstance restamps every volume attached to an
// instance when that instance reaches a new state.
//
// The listing only chooses which rows to visit; whether a row is attached to
// this instance is decided inside Update, under the store's write lock, against
// the value as it is then. Deciding from the listed copy instead would let a
// volume detached in the meantime be written back with its attachment intact —
// the API having already reported the detach — and the DeleteVolume that
// follows is then refused as VolumeInUse.
func ec2UpdateVolumeAttachmentsForInstance(instanceID, attachmentState, volumeState string) {
	for _, listed := range ec2Volumes.List() {
		ec2Volumes.Update(listed.VolumeId, func(vol *EC2Volume) {
			changed := false
			for i := range vol.Attachments {
				if vol.Attachments[i].InstanceId == instanceID {
					vol.Attachments[i].State = attachmentState
					changed = true
				}
			}
			if changed {
				vol.State = volumeState
			}
		})
	}
}

// ec2DeleteOnTerminationVolumes releases the volumes a terminating instance
// held, deleting the ones it was told to delete with it and detaching the rest.
//
// Which attachments a volume has is read inside Update, for the same reason the
// attachment restamp reads it there: a listed copy can already be out of date by
// the time it is written back. The backing store is removed outside the lock,
// since it is filesystem work and the row is gone by then either way.
func ec2DeleteOnTerminationVolumes(instanceID string) {
	for _, listed := range ec2Volumes.List() {
		var dockerVolume, hostPath string
		deleteVolume := false
		ec2Volumes.Update(listed.VolumeId, func(vol *EC2Volume) {
			keep := make([]EC2VolumeAttachment, 0, len(vol.Attachments))
			for _, att := range vol.Attachments {
				if att.InstanceId == instanceID && att.DeleteOnTermination {
					deleteVolume = true
					continue
				}
				keep = append(keep, att)
			}
			if deleteVolume {
				dockerVolume, hostPath = vol.DockerVolumeName, vol.HostPath
				return
			}
			if len(keep) != len(vol.Attachments) {
				vol.Attachments = keep
				if len(vol.Attachments) == 0 {
					vol.State = "available"
				}
			}
		})
		if !deleteVolume {
			continue
		}
		if dockerVolume != "" {
			ebsRemoveDockerVolume(dockerVolume)
		} else {
			_ = os.RemoveAll(hostPath)
		}
		ec2Volumes.Delete(listed.VolumeId)
	}
}

func handleDescribeInstanceStatus(w http.ResponseWriter, r *http.Request) {
	instanceIDs := ec2ParamList(r, "InstanceId")
	var instances []EC2Instance
	if len(instanceIDs) > 0 {
		for _, id := range instanceIDs {
			if inst, ok := ec2Instances.Get(id); ok && inst.State != "terminated" {
				instances = append(instances, inst)
			}
		}
	} else {
		instances = ec2Instances.Filter(func(inst EC2Instance) bool { return inst.State == "running" })
	}
	var items strings.Builder
	for _, inst := range instances {
		fmt.Fprintf(&items, `<item><instanceId>%s</instanceId><availabilityZone>%s</availabilityZone><instanceState><code>%d</code><name>%s</name></instanceState><systemStatus><status>ok</status><details><item><name>reachability</name><status>passed</status></item></details></systemStatus><instanceStatus><status>ok</status><details><item><name>reachability</name><status>passed</status></item></details></instanceStatus></item>`,
			inst.InstanceId, awsAvailabilityZone(), instanceStateCode(inst.State), inst.State)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceStatusResponse %s>
  <requestId>%s</requestId>
  <instanceStatusSet>%s</instanceStatusSet>
</DescribeInstanceStatusResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDescribeInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	inst, ok := ec2Instances.Get(instanceID)
	if !ok {
		sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", instanceID)
		return
	}
	attribute := r.FormValue("Attribute")
	if attribute == "" {
		attribute = "instanceType"
	}
	var body string
	switch attribute {
	case "instanceType":
		body = fmt.Sprintf("<instanceType><value>%s</value></instanceType>", inst.InstanceType)
	case "kernel":
		body = "<kernel><value/></kernel>"
	case "ramdisk":
		body = "<ramdisk><value/></ramdisk>"
	case "userData":
		body = fmt.Sprintf("<userData><value>%s</value></userData>", inst.UserData)
	case "disableApiTermination":
		body = fmt.Sprintf("<disableApiTermination><value>%t</value></disableApiTermination>", inst.DisableApiTermination)
	case "disableApiStop":
		body = "<disableApiStop><value>false</value></disableApiStop>"
	case "instanceInitiatedShutdownBehavior":
		body = "<instanceInitiatedShutdownBehavior><value>stop</value></instanceInitiatedShutdownBehavior>"
	case "rootDeviceName":
		body = fmt.Sprintf("<rootDeviceName><value>%s</value></rootDeviceName>", inst.RootDeviceName)
	case "sourceDestCheck":
		body = fmt.Sprintf("<sourceDestCheck><value>%t</value></sourceDestCheck>", inst.SourceDestCheck)
	case "ebsOptimized":
		body = fmt.Sprintf("<ebsOptimized><value>%t</value></ebsOptimized>", inst.EbsOptimized)
	default:
		body = fmt.Sprintf("<%s><value/></%s>", attribute, attribute)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceAttributeResponse %s>
  <requestId>%s</requestId>
  <instanceId>%s</instanceId>
  %s
</DescribeInstanceAttributeResponse>`, ec2Xmlns(), generateUUID(), instanceID, body)
}

func handleModifyInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2Instances.Get(instanceID); !ok {
		sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", instanceID)
		return
	}
	// Persist the modified attributes (previously a no-op: the set succeeded but
	// DescribeInstances/DescribeInstanceAttribute still showed the old value).
	ec2Instances.Update(instanceID, func(inst *EC2Instance) {
		if v := r.FormValue("SourceDestCheck.Value"); v != "" {
			inst.SourceDestCheck = v == "true"
		}
		if v := r.FormValue("DisableApiTermination.Value"); v != "" {
			inst.DisableApiTermination = v == "true"
		}
		if v := r.FormValue("EbsOptimized.Value"); v != "" {
			inst.EbsOptimized = v == "true"
		}
		if v := r.FormValue("InstanceType.Value"); v != "" {
			inst.InstanceType = v
		}
		if v := r.FormValue("UserData.Value"); v != "" {
			inst.UserData = v
		}
		if groups := ec2ParamList(r, "GroupId"); len(groups) > 0 {
			inst.SecurityGroupIds = groups
		}
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceAttributeResponse %s><requestId>%s</requestId><return>true</return></ModifyInstanceAttributeResponse>`, ec2Xmlns(), generateUUID())
}

// handleModifyInstanceMetadataOptions updates an instance's metadata options in
// place — the provider's path for an aws_instance.metadata_options change.
func handleModifyInstanceMetadataOptions(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2Instances.Get(instanceID); !ok {
		sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", instanceID)
		return
	}
	ec2Instances.Update(instanceID, func(inst *EC2Instance) {
		if v := r.FormValue("HttpTokens"); v != "" {
			inst.MetadataHttpTokens = v
		}
		if v := r.FormValue("HttpEndpoint"); v != "" {
			inst.MetadataHttpEndpoint = v
		}
		if v := r.FormValue("HttpPutResponseHopLimit"); v != "" {
			inst.MetadataHopLimit = ec2AtoiOr(v, inst.MetadataHopLimit)
		}
		if v := r.FormValue("InstanceMetadataTags"); v != "" {
			inst.MetadataInstanceTags = v
		}
	})
	inst, _ := ec2Instances.Get(instanceID)
	hop := inst.MetadataHopLimit
	if hop == 0 {
		hop = 1
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceMetadataOptionsResponse %s><requestId>%s</requestId><instanceId>%s</instanceId>`+
		`<instanceMetadataOptions><state>applied</state><httpTokens>%s</httpTokens><httpPutResponseHopLimit>%d</httpPutResponseHopLimit><httpEndpoint>%s</httpEndpoint><instanceMetadataTags>%s</instanceMetadataTags></instanceMetadataOptions>`+
		`</ModifyInstanceMetadataOptionsResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, inst.MetadataHttpTokens, hop, inst.MetadataHttpEndpoint, inst.MetadataInstanceTags)
}

func handleCreateTags(w http.ResponseWriter, r *http.Request) {
	resources := ec2ParamList(r, "ResourceId")
	tags := parseIndexedTags(r, "Tag")
	for _, id := range resources {
		if strings.HasPrefix(id, "i-") {
			ec2Instances.Update(id, func(inst *EC2Instance) { inst.Tags = mergeEC2Tags(inst.Tags, tags) })
		}
		if strings.HasPrefix(id, "eni-") {
			ec2NetworkInterfaces.Update(id, func(eni *EC2NetworkInterface) { eni.Tags = mergeEC2Tags(eni.Tags, tags) })
		}
		if strings.HasPrefix(id, "vol-") {
			ec2Volumes.Update(id, func(vol *EC2Volume) { vol.Tags = mergeEC2Tags(vol.Tags, tags) })
		}
		if strings.HasPrefix(id, "snap-") {
			ec2Snapshots.Update(id, func(snap *EC2Snapshot) { snap.Tags = mergeEC2Tags(snap.Tags, tags) })
		}
		if strings.HasPrefix(id, "acl-") {
			ec2NetworkAcls.Update(id, func(acl *EC2NetworkAcl) { acl.Tags = mergeEC2Tags(acl.Tags, tags) })
		}
		if strings.HasPrefix(id, "pcx-") {
			ec2VpcPeerings.Update(id, func(pcx *EC2VpcPeeringConnection) { pcx.Tags = mergeEC2Tags(pcx.Tags, tags) })
		}
		if strings.HasPrefix(id, "pl-") {
			ec2ManagedPrefixLists.Update(id, func(pl *EC2ManagedPrefixList) { pl.Tags = mergeEC2Tags(pl.Tags, tags) })
		}
		if strings.HasPrefix(id, "fl-") {
			ec2FlowLogs.Update(id, func(fl *EC2FlowLog) { fl.Tags = mergeEC2Tags(fl.Tags, tags) })
		}
		if strings.HasPrefix(id, "eigw-") {
			ec2EgressOnlyGateways.Update(id, func(eigw *EC2EgressOnlyInternetGateway) { eigw.Tags = mergeEC2Tags(eigw.Tags, tags) })
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateTagsResponse %s><requestId>%s</requestId><return>true</return></CreateTagsResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteTags(w http.ResponseWriter, r *http.Request) {
	resources := ec2ParamList(r, "ResourceId")
	keys := map[string]bool{}
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Tag.%d.Key", i))
		if key == "" {
			break
		}
		keys[key] = true
	}
	filter := func(tags []EC2Tag) []EC2Tag {
		var out []EC2Tag
		for _, tag := range tags {
			if !keys[tag.Key] {
				out = append(out, tag)
			}
		}
		return out
	}
	for _, id := range resources {
		if strings.HasPrefix(id, "i-") {
			ec2Instances.Update(id, func(inst *EC2Instance) { inst.Tags = filter(inst.Tags) })
		}
		if strings.HasPrefix(id, "eni-") {
			ec2NetworkInterfaces.Update(id, func(eni *EC2NetworkInterface) { eni.Tags = filter(eni.Tags) })
		}
		if strings.HasPrefix(id, "vol-") {
			ec2Volumes.Update(id, func(vol *EC2Volume) { vol.Tags = filter(vol.Tags) })
		}
		if strings.HasPrefix(id, "snap-") {
			ec2Snapshots.Update(id, func(snap *EC2Snapshot) { snap.Tags = filter(snap.Tags) })
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteTagsResponse %s><requestId>%s</requestId><return>true</return></DeleteTagsResponse>`, ec2Xmlns(), generateUUID())
}

func handleDescribeTags(w http.ResponseWriter, r *http.Request) {
	type tagEntry struct {
		resourceID   string
		resourceType string
		key          string
		value        string
	}
	filters := ec2Filters(r)
	matches := func(entry tagEntry) bool {
		for name, values := range filters {
			matched := false
			for _, value := range values {
				switch {
				case name == "resource-id" && entry.resourceID == value:
					matched = true
				case name == "resource-type" && entry.resourceType == value:
					matched = true
				case name == "key" && entry.key == value:
					matched = true
				case name == "value" && entry.value == value:
					matched = true
				case strings.HasPrefix(name, "tag:") && strings.TrimPrefix(name, "tag:") == entry.key && entry.value == value:
					matched = true
				}
			}
			if !matched {
				return false
			}
		}
		return true
	}
	entries := make([]tagEntry, 0)
	add := func(entry tagEntry) {
		if matches(entry) {
			entries = append(entries, entry)
		}
	}
	for _, inst := range ec2Instances.List() {
		for _, tag := range inst.Tags {
			add(tagEntry{resourceID: inst.InstanceId, resourceType: "instance", key: tag.Key, value: tag.Value})
		}
	}
	for _, eni := range ec2NetworkInterfaces.List() {
		for _, tag := range eni.Tags {
			add(tagEntry{resourceID: eni.NetworkInterfaceId, resourceType: "network-interface", key: tag.Key, value: tag.Value})
		}
	}
	for _, vol := range ec2Volumes.List() {
		for _, tag := range vol.Tags {
			add(tagEntry{resourceID: vol.VolumeId, resourceType: "volume", key: tag.Key, value: tag.Value})
		}
	}
	for _, snap := range ec2Snapshots.List() {
		for _, tag := range snap.Tags {
			add(tagEntry{resourceID: snap.SnapshotId, resourceType: "snapshot", key: tag.Key, value: tag.Value})
		}
	}
	// Stable order so the MaxResults/NextToken offset cursor is consistent.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].resourceID != entries[j].resourceID {
			return entries[i].resourceID < entries[j].resourceID
		}
		return entries[i].key < entries[j].key
	})
	entries, nextToken := awsPageExplicit(entries, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))

	var items strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&items, `<item><resourceId>%s</resourceId><resourceType>%s</resourceType><key>%s</key><value>%s</value></item>`,
			entry.resourceID, entry.resourceType, entry.key, entry.value)
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeTagsResponse %s>
  <requestId>%s</requestId>
  <tagSet>%s</tagSet>%s
</DescribeTagsResponse>`, ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func handleDescribeVolumes(w http.ResponseWriter, r *http.Request) {
	volumeIDs := ec2ParamList(r, "VolumeId")
	for _, id := range volumeIDs {
		if _, ok := ec2Volumes.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	volumes := make([]EC2Volume, 0)
	for _, vol := range ec2Volumes.List() {
		if len(volumeIDs) > 0 && !ec2StrInValues(vol.VolumeId, volumeIDs) {
			continue
		}
		if !ec2VolumeMatchesFilters(vol, filters) {
			continue
		}
		volumes = append(volumes, vol)
	}
	// MaxResults/NextToken pagination applies only to the list form (not when
	// explicit VolumeIds are requested), matching real EC2. Sort by id for a
	// stable offset cursor across pages.
	nextToken := ""
	if len(volumeIDs) == 0 {
		sort.Slice(volumes, func(i, j int) bool { return volumes[i].VolumeId < volumes[j].VolumeId })
		volumes, nextToken = awsPageExplicit(volumes, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	}
	var items strings.Builder
	for _, vol := range volumes {
		items.WriteString(ec2VolumeXML(vol))
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVolumesResponse %s>
  <requestId>%s</requestId>
  <volumeSet>%s</volumeSet>%s
</DescribeVolumesResponse>`, ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

// ebsECSDockerVolumeName returns the Docker named volume name for an ECS-managed EBS volume.
// ECS tasks use Docker named volumes so the data lives in the Docker daemon rather than on
// the sim process's own filesystem, making volumes accessible to sibling task containers
// regardless of whether the sim itself runs on the host or inside a container.
func ebsECSDockerVolumeName(volumeID string) string {
	return "sockerless-ebs-" + volumeID
}

// ebsSnapshotDockerVolumeName returns the Docker named volume name for a snapshot taken
// from an ECS-managed EBS volume.
func ebsSnapshotDockerVolumeName(snapshotID string) string {
	return "sockerless-snap-" + snapshotID
}

// ebsRemoveDockerVolume removes a Docker named volume created for an ECS EBS volume or
// snapshot. Errors are silently ignored (volume may already be absent).
func ebsRemoveDockerVolume(name string) {
	if name == "" {
		return
	}
	cli := sim.DockerClient()
	if cli == nil {
		// Process mode (SIM_RUNTIME=process): the managed-EBS volume is
		// host-path-backed, so no Docker volume exists to remove — never
		// dereference the nil client (the panic reported in #569).
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = cli.VolumeRemove(ctx, name, dockerclient.VolumeRemoveOptions{})
}

// ebsCopyDockerVolumes copies all content from srcVolume into dstVolume using a
// short-lived Alpine container. The destination volume is auto-created by Docker if
// it does not yet exist.
func ebsCopyDockerVolumes(ctx context.Context, srcVolume, dstVolume string) error {
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        "alpine:latest",
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"sh", "-c", "cp -a /src/. /dst/"},
		Binds: []string{
			srcVolume + ":/src:ro",
			dstVolume + ":/dst",
		},
		// No wall-clock cap: the copy's duration is a function of how much data
		// the volume holds, which the simulator does not get to choose. A fixed
		// timeout here (this was 60s) fails a large but perfectly healthy
		// restore -- an 8 GiB workspace copies in roughly 40s on an idle host
		// and longer under load -- and reports it as an error the caller cannot
		// act on. Real EBS has no equivalent deadline: a volume created from a
		// snapshot is usable immediately and hydrates lazily. Callers bound this
		// instead by the lifecycle they own (a task's transition, which can be
		// stopped), not by a guess made here.
		Timeout: 0,
	}, discardLogSink{})
	if err != nil {
		return fmt.Errorf("start volume copy container: %w", err)
	}
	res := handle.Wait()
	if res.Error != nil {
		return fmt.Errorf("volume copy: %w", res.Error)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("volume copy exited with code %d", res.ExitCode)
	}
	return nil
}

// ebsHostRoot returns the on-disk root for simulated EBS volume backing
// images and snapshot payloads. Resolution order: SIM_EBS_DATA_DIR
// (explicit override), then <SIM_DATA_DIR>/ebs (so volume contents survive
// a simulator restart alongside the SQLite control-plane state), then a
// temp directory.
func ebsHostRoot() string {
	return simScopedDataDir("SIM_EBS_DATA_DIR", "ebs", "sockerless-sim-ebs")
}

func ebsVolumeHostDirPath(volumeID string) string {
	return filepath.Join(ebsHostRoot(), "volumes", volumeID)
}

func ebsVolumeBlockImagePath(vol EC2Volume) string {
	hostPath := vol.HostPath
	if hostPath == "" {
		hostPath = ebsVolumeHostDirPath(vol.VolumeId)
	}
	return filepath.Join(hostPath, "ebs.raw")
}

func ebsSnapshotHostDirPath(snapshotID string) string {
	return filepath.Join(ebsHostRoot(), "snapshots", snapshotID)
}

func EBSVolumeHostDir(volumeID string) string {
	dir := ebsVolumeHostDirPath(volumeID)
	_ = os.MkdirAll(dir, 0o777)
	return dir
}

func ebsPrepareVolumeHostPath(vol *EC2Volume) error {
	if vol.HostPath == "" {
		vol.HostPath = ebsVolumeHostDirPath(vol.VolumeId)
	}
	return os.MkdirAll(vol.HostPath, 0o777)
}

func ebsEnsureVolumeBlockImage(vol *EC2Volume) (string, error) {
	if err := ebsPrepareVolumeHostPath(vol); err != nil {
		return "", err
	}
	path := ebsVolumeBlockImagePath(*vol)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o666)
		if err != nil {
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
	}
	sizeBytes := int64(vol.Size) * 1024 * 1024 * 1024
	if sizeBytes < 1024*1024 {
		sizeBytes = 1024 * 1024
	}
	if err := os.Truncate(path, sizeBytes); err != nil {
		return "", err
	}
	return path, nil
}

func ebsCopyDir(dst, src string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return ebsCopySparseFile(target, path, info.Mode().Perm())
	})
}

func ec2VolumeXML(vol EC2Volume) string {
	return "<item>" + ec2VolumeFieldsXML(vol) + "</item>"
}

func ec2VolumeFieldsXML(vol EC2Volume) string {
	var attachments strings.Builder
	if len(vol.Attachments) == 0 {
		attachments.WriteString("<attachmentSet/>")
	} else {
		attachments.WriteString("<attachmentSet>")
		for _, att := range vol.Attachments {
			fmt.Fprintf(&attachments, `<item><volumeId>%s</volumeId><instanceId>%s</instanceId><device>%s</device><status>%s</status><attachTime>%s</attachTime><deleteOnTermination>%t</deleteOnTermination></item>`,
				att.VolumeId, att.InstanceId, att.Device, att.State, att.AttachTime, att.DeleteOnTermination)
		}
		attachments.WriteString("</attachmentSet>")
	}
	perf := ""
	if vol.Iops > 0 {
		perf += fmt.Sprintf("<iops>%d</iops>", vol.Iops)
	}
	if vol.Throughput > 0 {
		perf += fmt.Sprintf("<throughput>%d</throughput>", vol.Throughput)
	}
	if vol.KmsKeyId != "" {
		perf += fmt.Sprintf("<kmsKeyId>%s</kmsKeyId>", vol.KmsKeyId)
	}
	return fmt.Sprintf(`
    <volumeId>%s</volumeId>
    <size>%d</size>
    <snapshotId>%s</snapshotId>
    <availabilityZone>%s</availabilityZone>
    <status>%s</status>
    <createTime>%s</createTime>
    %s
    <volumeType>%s</volumeType>%s
    <encrypted>%t</encrypted>
    <multiAttachEnabled>%t</multiAttachEnabled>
    %s
  `, vol.VolumeId, vol.Size, vol.SnapshotId, vol.AvailabilityZone, vol.State, vol.CreateTime,
		attachments.String(), vol.VolumeType, perf, vol.Encrypted, vol.MultiAttachEnabled, writeTagSetXML(vol.Tags))
}

// ec2ResolveVolumePerformance returns the effective iops/throughput for a
// volume, applying the AWS-documented per-type rules (gp3 defaults 3000/125;
// gp2 iops derived from size; io1/io2 use the requested iops; st1/sc1/standard
// have neither). The resolved values are stored so they round-trip.
func ec2ResolveVolumePerformance(volType string, size, reqIops, reqThroughput int) (iops, throughput int) {
	switch volType {
	case "gp3":
		iops = reqIops
		if iops == 0 {
			iops = 3000
		}
		throughput = reqThroughput
		if throughput == 0 {
			throughput = 125
		}
	case "gp2":
		iops = 3 * size
		if iops < 100 {
			iops = 100
		}
		if iops > 16000 {
			iops = 16000
		}
	case "io1", "io2":
		iops = reqIops
	}
	return iops, throughput
}

func ec2VolumeMatchesFilters(vol EC2Volume, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "volume-id":
			if !ec2StrInValues(vol.VolumeId, vals) {
				return false
			}
		case "volume-type":
			if !ec2StrInValues(vol.VolumeType, vals) {
				return false
			}
		case "status":
			if !ec2StrInValues(vol.State, vals) {
				return false
			}
		case "availability-zone":
			if !ec2StrInValues(vol.AvailabilityZone, vals) {
				return false
			}
		case "snapshot-id":
			if !ec2StrInValues(vol.SnapshotId, vals) {
				return false
			}
		case "encrypted":
			if (ec2StrInValues("true", vals)) != vol.Encrypted {
				return false
			}
		case "attachment.instance-id":
			if !ec2VolumeHasAttachment(vol, func(a EC2VolumeAttachment) bool { return ec2StrInValues(a.InstanceId, vals) }) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, vol.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func ec2VolumeHasAttachment(vol EC2Volume, pred func(EC2VolumeAttachment) bool) bool {
	for _, a := range vol.Attachments {
		if pred(a) {
			return true
		}
	}
	return false
}

func handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	// AvailabilityZone is a required CreateVolume parameter; real EC2 rejects a
	// request without it rather than defaulting the AZ.
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter availability_zone", http.StatusBadRequest)
		return
	}
	size := 8
	if v := r.FormValue("Size"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &size); err != nil || size < 1 {
			ec2ErrorXML(w, "InvalidParameterValue", "Size must be a positive integer", http.StatusBadRequest)
			return
		}
	}
	snapshotID := r.FormValue("SnapshotId")
	var data []byte
	var snapshotHostPath string
	if snapshotID != "" {
		ec2SettleSnapshot(snapshotID)
		snap, ok := ec2Snapshots.Get(snapshotID)
		if !ok {
			ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapshotID), http.StatusBadRequest)
			return
		}
		if snap.State != "completed" {
			ec2ErrorXML(w, "IncorrectState", fmt.Sprintf("The snapshot %q is not completed", snapshotID), http.StatusBadRequest)
			return
		}
		if size < snap.VolumeSize {
			size = snap.VolumeSize
		}
		data = append([]byte(nil), snap.VolumeData...)
		snapshotHostPath = snap.HostPath
	}
	volType := r.FormValue("VolumeType")
	if volType == "" {
		volType = "gp3"
	}
	iops, throughput := ec2ResolveVolumePerformance(volType, size,
		ec2AtoiOr(r.FormValue("Iops"), 0), ec2AtoiOr(r.FormValue("Throughput"), 0))
	vol := EC2Volume{
		VolumeId:           ec2ID("vol"),
		Size:               size,
		SnapshotId:         snapshotID,
		AvailabilityZone:   az,
		State:              "available",
		CreateTime:         time.Now().UTC().Format(time.RFC3339),
		VolumeType:         volType,
		Iops:               iops,
		Throughput:         throughput,
		KmsKeyId:           r.FormValue("KmsKeyId"),
		Encrypted:          r.FormValue("Encrypted") == "true" || r.FormValue("KmsKeyId") != "",
		MultiAttachEnabled: r.FormValue("MultiAttachEnabled") == "true",
		Tags:               parseTags(r),
		Data:               data,
	}
	if err := ebsPrepareVolumeHostPath(&vol); err != nil {
		ec2ErrorXML(w, "InternalError", fmt.Sprintf("could not create volume data path: %v", err), http.StatusInternalServerError)
		return
	}
	if snapshotHostPath != "" {
		if err := ebsCopyDir(vol.HostPath, snapshotHostPath); err != nil {
			ec2ErrorXML(w, "InternalError", fmt.Sprintf("could not restore snapshot data: %v", err), http.StatusInternalServerError)
			return
		}
	}
	ec2Volumes.Put(vol.VolumeId, vol)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVolumeResponse %s><requestId>%s</requestId>%s</CreateVolumeResponse>`,
		ec2Xmlns(), generateUUID(), ec2VolumeFieldsXML(vol))
}

func handleAttachVolume(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	instanceID := r.FormValue("InstanceId")
	device := r.FormValue("Device")
	if device == "" {
		device = "/dev/sdf"
	}
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	if _, ok := ec2Instances.Get(instanceID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID %q does not exist", instanceID), http.StatusBadRequest)
		return
	}
	if len(vol.Attachments) > 0 {
		ec2ErrorXML(w, "IncorrectState", "Volume is already attached", http.StatusBadRequest)
		return
	}
	if _, err := ebsEnsureVolumeBlockImage(&vol); err != nil {
		ec2ErrorXML(w, "InternalError", fmt.Sprintf("could not prepare volume block image: %v", err), http.StatusInternalServerError)
		return
	}
	// Real block-device attach only when a live Firecracker VM exists. EC2
	// attachment metadata remains authoritative at the control plane when the
	// host data-plane substrate is absent or failed.
	if ec2RealVMAlive(instanceID) {
		if err := ec2AttachRealVolume(r.Context(), instanceID, &vol); err != nil {
			ec2ErrorXML(w, "IncorrectInstanceState", fmt.Sprintf("failed to attach real EBS volume: %v", err), http.StatusServiceUnavailable)
			return
		}
	}
	att := EC2VolumeAttachment{
		VolumeId:   volID,
		InstanceId: instanceID,
		Device:     device,
		State:      "attached",
		AttachTime: time.Now().UTC().Format(time.RFC3339),
	}
	vol.State = "in-use"
	vol.Attachments = []EC2VolumeAttachment{att}
	ec2Volumes.Put(volID, vol)
	ec2AttachmentResponse(w, "AttachVolume", att)
}

func handleDetachVolume(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	if len(vol.Attachments) == 0 {
		ec2ErrorXML(w, "IncorrectState", "Volume is not attached", http.StatusBadRequest)
		return
	}
	att := vol.Attachments[0]
	if ec2RealVMAlive(att.InstanceId) {
		if err := ec2DetachRealVolume(r.Context(), att.InstanceId, volID); err != nil {
			ec2ErrorXML(w, "IncorrectInstanceState", fmt.Sprintf("failed to detach real EBS volume: %v", err), http.StatusServiceUnavailable)
			return
		}
	}
	att.State = "detached"
	vol.Attachments = nil
	vol.State = "available"
	ec2Volumes.Put(volID, vol)
	ec2AttachmentResponse(w, "DetachVolume", att)
}

func ec2AttachmentResponse(w http.ResponseWriter, action string, att EC2VolumeAttachment) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s><requestId>%s</requestId><volumeId>%s</volumeId><instanceId>%s</instanceId><device>%s</device><status>%s</status><attachTime>%s</attachTime></%sResponse>`,
		action, ec2Xmlns(), generateUUID(), att.VolumeId, att.InstanceId, att.Device, att.State, att.AttachTime, action)
}

func handleDeleteVolume(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	if len(vol.Attachments) > 0 {
		ec2ErrorXML(w, "VolumeInUse", "Volume is in-use", http.StatusBadRequest)
		return
	}
	if vol.DockerVolumeName != "" {
		ebsRemoveDockerVolume(vol.DockerVolumeName)
	} else {
		_ = os.RemoveAll(vol.HostPath)
	}
	ec2Volumes.Delete(volID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVolumeResponse %s><requestId>%s</requestId><return>true</return></DeleteVolumeResponse>`, ec2Xmlns(), generateUUID())
}

func handleModifyVolume(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	mod := EC2VolumeModification{
		VolumeId:           volID,
		ModificationState:  "completed",
		OriginalSize:       vol.Size,
		OriginalVolumeType: vol.VolumeType,
		OriginalIops:       vol.Iops,
		OriginalThroughput: vol.Throughput,
		StartTime:          time.Now().UTC().Format(time.RFC3339),
		EndTime:            time.Now().UTC().Format(time.RFC3339),
	}
	if v := r.FormValue("Size"); v != "" {
		var size int
		if _, err := fmt.Sscanf(v, "%d", &size); err != nil || size < vol.Size {
			ec2ErrorXML(w, "InvalidParameterValue", "Size must be an integer greater than or equal to the current volume size", http.StatusBadRequest)
			return
		}
		vol.Size = size
	}
	if v := r.FormValue("VolumeType"); v != "" {
		vol.VolumeType = v
	}
	if v := r.FormValue("Iops"); v != "" {
		vol.Iops = ec2AtoiOr(v, vol.Iops)
	}
	if v := r.FormValue("Throughput"); v != "" {
		vol.Throughput = ec2AtoiOr(v, vol.Throughput)
	}
	// Re-resolve performance for the (possibly new) type/size so gp2 iops and
	// gp3 defaults stay consistent after a type change.
	vol.Iops, vol.Throughput = ec2ResolveVolumePerformance(vol.VolumeType, vol.Size, vol.Iops, vol.Throughput)
	mod.TargetSize, mod.TargetVolumeType, mod.TargetIops, mod.TargetThroughput = vol.Size, vol.VolumeType, vol.Iops, vol.Throughput
	ec2VolumeMods.Put(volID, mod)
	if len(vol.Attachments) > 0 && ec2RealVMAlive(vol.Attachments[0].InstanceId) {
		if _, err := ebsEnsureVolumeBlockImage(&vol); err != nil {
			ec2ErrorXML(w, "InternalError", fmt.Sprintf("could not resize volume block image: %v", err), http.StatusInternalServerError)
			return
		}
		if err := ec2RefreshRealVolume(r.Context(), vol); err != nil {
			ec2ErrorXML(w, "IncorrectInstanceState", fmt.Sprintf("failed to refresh real EBS volume: %v", err), http.StatusServiceUnavailable)
			return
		}
	}
	ec2Volumes.Put(volID, vol)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVolumeResponse %s><requestId>%s</requestId><volumeModification>%s</volumeModification></ModifyVolumeResponse>`,
		ec2Xmlns(), generateUUID(), ec2VolumeModFieldsXML(mod))
}

func ec2VolumeModFieldsXML(m EC2VolumeModification) string {
	return fmt.Sprintf(`<volumeId>%s</volumeId><modificationState>%s</modificationState>`+
		`<targetSize>%d</targetSize><targetVolumeType>%s</targetVolumeType><targetIops>%d</targetIops><targetThroughput>%d</targetThroughput>`+
		`<originalSize>%d</originalSize><originalVolumeType>%s</originalVolumeType><originalIops>%d</originalIops><originalThroughput>%d</originalThroughput>`+
		`<progress>100</progress><startTime>%s</startTime><endTime>%s</endTime>`,
		m.VolumeId, m.ModificationState,
		m.TargetSize, m.TargetVolumeType, m.TargetIops, m.TargetThroughput,
		m.OriginalSize, m.OriginalVolumeType, m.OriginalIops, m.OriginalThroughput,
		m.StartTime, m.EndTime)
}

// handleDescribeVolumesModifications returns the recorded volume modifications.
// terraform-provider-aws polls this after a ModifyVolume to wait for the resize
// to reach `completed`; an unregistered op previously made aws_ebs_volume
// updates error with UnknownOperation.
func handleDescribeVolumesModifications(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VolumeId")
	mods := make([]EC2VolumeModification, 0)
	for _, mod := range ec2VolumeMods.List() {
		if len(ids) > 0 && !ec2StrInValues(mod.VolumeId, ids) {
			continue
		}
		mods = append(mods, mod)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].VolumeId < mods[j].VolumeId })
	mods, nextToken := awsPageExplicit(mods, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))

	var items strings.Builder
	items.WriteString("<volumeModificationSet>")
	for _, mod := range mods {
		items.WriteString("<item>")
		items.WriteString(ec2VolumeModFieldsXML(mod))
		items.WriteString("</item>")
	}
	items.WriteString("</volumeModificationSet>")
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVolumesModificationsResponse %s><requestId>%s</requestId>%s%s</DescribeVolumesModificationsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	snap := EC2Snapshot{
		SnapshotId:    ec2ID("snap"),
		VolumeId:      volID,
		VolumeSize:    vol.Size,
		State:         "pending",
		StartTime:     now.Format(time.RFC3339),
		CompletionDue: now.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
		Progress:      "0%",
		Description:   r.FormValue("Description"),
		OwnerId:       ec2Owner(),
		Encrypted:     vol.Encrypted,
		KmsKeyId:      vol.KmsKeyId,
		Tags:          parseTags(r),
		VolumeData:    append([]byte(nil), vol.Data...),
	}
	if vol.DockerVolumeName != "" {
		snap.DockerVolumeName = ebsSnapshotDockerVolumeName(snap.SnapshotId)
	} else {
		snap.HostPath = ebsSnapshotHostDirPath(snap.SnapshotId)
		if err := ebsPrepareVolumeHostPath(&vol); err != nil {
			ec2ErrorXML(w, "InternalError", fmt.Sprintf("could not access volume data path: %v", err), http.StatusInternalServerError)
			return
		}
	}
	// The data is captured behind the response, not inside it. CreateSnapshot
	// returns as soon as the snapshot is registered on real EC2 -- that is what
	// the pending state and the Progress field are for -- and callers poll
	// DescribeSnapshots until it reports completed. Copying in the handler
	// instead held the simulator's HTTP server for the length of the copy: an
	// EDD workspace volume took about eight minutes, and for that whole window
	// every request went unanswered, including /health, so callers timed out
	// against a simulator that looked dead but was only busy.
	//
	// Completion is driven by the capture goroutine rather than the elapsed
	// CompletionDue clock, so the due time is pushed out of reach: a lazy settle
	// from DescribeSnapshots must never report completed while bytes are still
	// being copied, or a restore would read a half-written snapshot.
	snap.CompletionDue = now.Add(ec2SnapshotCaptureNotBefore).Format(time.RFC3339Nano)
	ec2Volumes.Put(vol.VolumeId, vol)
	ec2Snapshots.Put(snap.SnapshotId, snap)
	go ec2CaptureSnapshotData(snap.SnapshotId, vol.DockerVolumeName, snap.DockerVolumeName, vol.HostPath, snap.HostPath)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSnapshotResponse %s><requestId>%s</requestId>%s</CreateSnapshotResponse>`,
		ec2Xmlns(), generateUUID(), ec2SnapshotFieldsXML(snap))
}

// handleCopySnapshot copies an existing snapshot into a new snapshot id,
// duplicating its backing data so a restore from the copy yields the source's
// bytes. Real CopySnapshot is the cross-region DR primitive (snapshot → copy
// to another region → restore); the destination-region endpoint receives the
// request with SourceRegion + SourceSnapshotId, and the sim — single-account,
// single-store — realizes the copy locally against the source snapshot. The
// response carries only the new snapshotId (not the full snapshot fields).
func handleCopySnapshot(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceSnapshotId")
	if srcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SourceSnapshotId", http.StatusBadRequest)
		return
	}
	ec2SettleSnapshot(srcID)
	src, ok := ec2Snapshots.Get(srcID)
	if !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", srcID), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	snap := EC2Snapshot{
		SnapshotId:    ec2ID("snap"),
		VolumeId:      src.VolumeId,
		VolumeSize:    src.VolumeSize,
		State:         "pending",
		StartTime:     now.Format(time.RFC3339),
		CompletionDue: now.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
		Progress:      "0%",
		Description:   r.FormValue("Description"),
		OwnerId:       ec2Owner(),
		Encrypted:     src.Encrypted,
		KmsKeyId:      src.KmsKeyId,
		Tags:          parseTags(r),
		VolumeData:    append([]byte(nil), src.VolumeData...),
	}
	if src.DockerVolumeName != "" {
		snap.DockerVolumeName = ebsSnapshotDockerVolumeName(snap.SnapshotId)
	} else if src.HostPath != "" {
		snap.HostPath = ebsSnapshotHostDirPath(snap.SnapshotId)
	}
	// Asynchronous for the same reason as CreateSnapshot: copying a snapshot's
	// bytes inside the handler stalls every other request for the duration.
	snap.CompletionDue = now.Add(ec2SnapshotCaptureNotBefore).Format(time.RFC3339Nano)
	ec2Snapshots.Put(snap.SnapshotId, snap)
	go ec2CaptureSnapshotData(snap.SnapshotId, src.DockerVolumeName, snap.DockerVolumeName, src.HostPath, snap.HostPath)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CopySnapshotResponse %s><requestId>%s</requestId><snapshotId>%s</snapshotId></CopySnapshotResponse>`,
		ec2Xmlns(), generateUUID(), snap.SnapshotId)
}

// ec2SnapshotCaptureNotBefore parks a capturing snapshot's CompletionDue far
// enough ahead that no lazy settle can declare it completed. The capture
// goroutine completes it explicitly once the bytes are actually there.
const ec2SnapshotCaptureNotBefore = 100 * 365 * 24 * time.Hour

// ec2CaptureSnapshotData copies a source's contents into a snapshot and then
// completes it. It runs off the request path: see handleCreateSnapshot for why
// the copy must not happen inside the handler. The source is a volume for
// CreateSnapshot and another snapshot for CopySnapshot; both are asynchronous
// on real EC2.
func ec2CaptureSnapshotData(snapshotID, dockerSrc, dockerDst, hostSrc, hostDst string) {
	var err error
	switch {
	case dockerDst != "":
		err = ebsCopyDockerVolumes(context.Background(), dockerSrc, dockerDst)
	case hostDst != "":
		err = ebsCopyDir(hostDst, hostSrc)
	}
	if err != nil {
		// Real EC2 reports a snapshot that could not be captured as error, and
		// leaves it discoverable; failing silently would let a later restore
		// mount an empty volume and call it success.
		fmt.Fprintf(os.Stderr, "[sim-ec2] snapshot %s: could not capture volume data: %v\n", snapshotID, err)
		ec2Snapshots.Update(snapshotID, func(s *EC2Snapshot) {
			s.State = "error"
			s.Progress = "0%"
		})
		return
	}
	ec2Snapshots.Update(snapshotID, func(s *EC2Snapshot) {
		if s.State == "pending" {
			s.State = "completed"
			s.Progress = "100%"
			s.CompletionDue = time.Now().UTC().Format(time.RFC3339Nano)
		}
	})
}

func ec2TransitionSnapshotToCompleted(snapshotID string) {
	time.Sleep(100 * time.Millisecond)
	ec2SettleSnapshot(snapshotID)
}

func ec2SettleSnapshot(snapshotID string) {
	ec2Snapshots.Update(snapshotID, func(snap *EC2Snapshot) {
		if snap.State == "pending" && !ec2SnapshotCompletionDue(*snap).After(time.Now().UTC()) {
			snap.State = "completed"
			snap.Progress = "100%"
		}
	})
}

func ec2SnapshotCompletionDue(snap EC2Snapshot) time.Time {
	if snap.CompletionDue != "" {
		if t, err := time.Parse(time.RFC3339Nano, snap.CompletionDue); err == nil {
			return t
		}
	}
	if t, err := time.Parse(time.RFC3339, snap.StartTime); err == nil {
		return t.Add(100 * time.Millisecond)
	}
	return time.Time{}
}

func handleDescribeSnapshots(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SnapshotId")
	snapshots := make([]EC2Snapshot, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			ec2SettleSnapshot(id)
			snap, ok := ec2Snapshots.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", id), http.StatusBadRequest)
				return
			}
			snapshots = append(snapshots, snap)
		}
	} else {
		for _, snap := range ec2Snapshots.List() {
			ec2SettleSnapshot(snap.SnapshotId)
		}
		filters := ec2Filters(r)
		// OwnerIds (`--owner-ids self`/<account>) scopes to the snapshot owner;
		// the sim is single-account so a literal account id or "self" matches.
		owners := ec2ParamList(r, "Owner")
		self := ec2Owner()
		for _, snap := range ec2Snapshots.List() {
			if len(owners) > 0 {
				matched := false
				for _, o := range owners {
					if o == self || o == "self" {
						matched = true
					}
				}
				if !matched {
					continue
				}
			}
			if !ec2SnapshotMatchesFilters(snap, filters) {
				continue
			}
			snapshots = append(snapshots, snap)
		}
	}
	// MaxResults/NextToken pagination applies only to the list form (not when
	// explicit SnapshotIds are requested), matching real EC2. Sort by id for a
	// stable offset cursor across pages.
	nextToken := ""
	if len(ids) == 0 {
		sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].SnapshotId < snapshots[j].SnapshotId })
		snapshots, nextToken = awsPageExplicit(snapshots, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	}
	var items strings.Builder
	for _, snap := range snapshots {
		items.WriteString(ec2SnapshotXML(snap))
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSnapshotsResponse %s><requestId>%s</requestId><snapshotSet>%s</snapshotSet>%s</DescribeSnapshotsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func ec2SnapshotXML(snap EC2Snapshot) string {
	return "<item>" + ec2SnapshotFieldsXML(snap) + "</item>"
}

func ec2SnapshotFieldsXML(snap EC2Snapshot) string {
	kms := ""
	if snap.KmsKeyId != "" {
		kms = fmt.Sprintf("<kmsKeyId>%s</kmsKeyId>", snap.KmsKeyId)
	}
	return fmt.Sprintf(`<snapshotId>%s</snapshotId><volumeId>%s</volumeId><status>%s</status><startTime>%s</startTime><progress>%s</progress><ownerId>%s</ownerId><volumeSize>%d</volumeSize><description>%s</description><encrypted>%t</encrypted>%s%s`,
		snap.SnapshotId, snap.VolumeId, snap.State, snap.StartTime, snap.Progress, snap.OwnerId, snap.VolumeSize, xmlEscape(snap.Description), snap.Encrypted, kms, writeTagSetXML(snap.Tags))
}

func ec2SnapshotMatchesFilters(snap EC2Snapshot, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "snapshot-id":
			if !ec2StrInValues(snap.SnapshotId, vals) {
				return false
			}
		case "volume-id":
			if !ec2StrInValues(snap.VolumeId, vals) {
				return false
			}
		case "status":
			if !ec2StrInValues(snap.State, vals) {
				return false
			}
		case "owner-id":
			if !ec2StrInValues(snap.OwnerId, vals) {
				return false
			}
		case "encrypted":
			if (ec2StrInValues("true", vals)) != snap.Encrypted {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, snap.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("SnapshotId")
	snap, ok := ec2Snapshots.Get(snapID)
	if !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapID), http.StatusBadRequest)
		return
	}
	ec2Snapshots.Delete(snapID)
	if snap.DockerVolumeName != "" {
		ebsRemoveDockerVolume(snap.DockerVolumeName)
	} else {
		_ = os.RemoveAll(snap.HostPath)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSnapshotResponse %s><requestId>%s</requestId><return>true</return></DeleteSnapshotResponse>`, ec2Xmlns(), generateUUID())
}

func parseIndexedTags(r *http.Request, prefix string) []EC2Tag {
	var tags []EC2Tag
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("%s.%d.Key", prefix, i))
		if key == "" {
			break
		}
		tags = append(tags, EC2Tag{Key: key, Value: r.FormValue(fmt.Sprintf("%s.%d.Value", prefix, i))})
	}
	return tags
}

func mergeEC2Tags(existing, updates []EC2Tag) []EC2Tag {
	byKey := map[string]string{}
	for _, t := range existing {
		byKey[t.Key] = t.Value
	}
	for _, t := range updates {
		byKey[t.Key] = t.Value
	}
	var out []EC2Tag
	for key, value := range byKey {
		out = append(out, EC2Tag{Key: key, Value: value})
	}
	return out
}

// handleDescribeImages serves AMIs. The sim has no AMI registry — AMIs are
// opaque to it — so it synthesizes a deterministic image for the request: an
// explicit ImageId echoes back, and a `data.aws_ami` filter lookup (by name /
// architecture / root-device-type / virtualization-type / owner-alias) returns
// one image whose attributes match the query so the lookup resolves
// deterministically. Filter values now flow into the synthesized image rather
// than being ignored (the prior handler returned a fixed x86_64 image regardless).
func handleDescribeImages(w http.ResponseWriter, r *http.Request) {
	imageIDs := ec2ParamList(r, "ImageId")
	filters := ec2Filters(r)
	firstFilter := func(name, def string) string {
		if vals := filters[name]; len(vals) > 0 {
			return vals[0]
		}
		return def
	}
	arch := firstFilter("architecture", "x86_64")
	rootType := firstFilter("root-device-type", "ebs")
	virtType := firstFilter("virtualization-type", "hvm")
	ownerAlias := firstFilter("owner-alias", "amazon")
	nameFilter := firstFilter("name", "")

	// First serve any user-registered AMIs (CreateImage / RegisterImage /
	// CopyImage). When an explicit ImageId resolves to a stored AMI, or the
	// filters match a stored AMI, return those records and skip the synthesized
	// fallback — the synthesized path exists only for `data.aws_ami` lookups of
	// vendor AMIs the sim has no registry for.
	var stored strings.Builder
	for _, img := range ec2Images.List() {
		if len(imageIDs) > 0 && !ec2StrInValues(img.ImageId, imageIDs) {
			continue
		}
		if !ec2ImageMatchesFilters(img, filters) {
			continue
		}
		stored.WriteString(ec2StoredImageXML(img))
	}
	if stored.Len() > 0 {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<DescribeImagesResponse %s><requestId>%s</requestId><imagesSet>%s</imagesSet></DescribeImagesResponse>`, ec2Xmlns(), generateUUID(), stored.String())
		return
	}

	var ids []string
	switch {
	case len(imageIDs) > 0:
		ids = imageIDs
	case len(filters["image-id"]) > 0:
		ids = filters["image-id"]
	case nameFilter != "":
		ids = []string{ec2AmiIDFromName(nameFilter)}
	default:
		ids = []string{"ami-simulated"}
	}

	var items strings.Builder
	for _, id := range ids {
		name := nameFilter
		if name == "" {
			name = id
		}
		fmt.Fprintf(&items, `<item><imageId>%s</imageId><imageLocation>%s/%s</imageLocation><imageState>available</imageState><imageOwnerId>%s</imageOwnerId><imageOwnerAlias>%s</imageOwnerAlias><isPublic>true</isPublic><architecture>%s</architecture><imageType>machine</imageType><rootDeviceType>%s</rootDeviceType><rootDeviceName>/dev/sda1</rootDeviceName><blockDeviceMapping><item><deviceName>/dev/sda1</deviceName><ebs><snapshotId>snap-%s</snapshotId><volumeSize>8</volumeSize><deleteOnTermination>true</deleteOnTermination><volumeType>gp3</volumeType></ebs></item></blockDeviceMapping><virtualizationType>%s</virtualizationType><name>%s</name><creationDate>2024-01-01T00:00:00.000Z</creationDate><hypervisor>xen</hypervisor></item>`,
			id, ownerAlias, name, ec2Owner(), ownerAlias, arch, rootType, strings.TrimPrefix(id, "ami-"), virtType, xmlEscape(name))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImagesResponse %s><requestId>%s</requestId><imagesSet>%s</imagesSet></DescribeImagesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDescribeInstanceTypes(w http.ResponseWriter, r *http.Request) {
	types := ec2ParamList(r, "InstanceType")
	if len(types) == 0 {
		types = []string{"t3.micro", "t3.small", "m6i.large"}
	}
	var items strings.Builder
	for _, name := range types {
		fmt.Fprintf(&items, `<item><instanceType>%s</instanceType><currentGeneration>true</currentGeneration><freeTierEligible>%t</freeTierEligible><supportedUsageClasses><item>on-demand</item><item>spot</item></supportedUsageClasses><supportedRootDeviceTypes><item>ebs</item></supportedRootDeviceTypes><supportedVirtualizationTypes><item>hvm</item></supportedVirtualizationTypes><vCpuInfo><defaultVCpus>2</defaultVCpus><defaultCores>1</defaultCores><defaultThreadsPerCore>2</defaultThreadsPerCore></vCpuInfo><memoryInfo><sizeInMiB>1024</sizeInMiB></memoryInfo><processorInfo><supportedArchitectures><item>x86_64</item></supportedArchitectures></processorInfo><networkInfo><networkPerformance>Up to 5 Gigabit</networkPerformance><maximumNetworkInterfaces>2</maximumNetworkInterfaces><ipv4AddressesPerInterface>2</ipv4AddressesPerInterface></networkInfo><ebsInfo><ebsOptimizedSupport>default</ebsOptimizedSupport><encryptionSupport>supported</encryptionSupport></ebsInfo></item>`,
			name, name == "t3.micro")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceTypesResponse %s>
  <requestId>%s</requestId>
  <instanceTypeSet>%s</instanceTypeSet>
</DescribeInstanceTypesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// handleDescribeInstanceTypeOfferings answers "is this instance type offered in
// these locations?" — the fck-nat module's pre-flight AZ validation. Like
// handleDescribeInstanceTypes, the API-only sim does not model real per-AZ
// capacity: it reports each requested instance type as offered in each
// requested (or default) location. Filters honoured: `instance-type` and
// `location`; LocationType selects region / availability-zone / -id scope.
func handleDescribeInstanceTypeOfferings(w http.ResponseWriter, r *http.Request) {
	locationType := r.FormValue("LocationType")
	if locationType == "" {
		locationType = "region"
	}
	filters := ec2Filters(r)
	types := filters["instance-type"]
	if len(types) == 0 {
		types = []string{"t3.micro", "t3.small", "t4g.nano", "m6i.large"}
	}
	locations := filters["location"]
	if len(locations) == 0 {
		switch locationType {
		case "availability-zone":
			locations = []string{awsAvailabilityZone()}
		case "availability-zone-id":
			locations = []string{awsRegion() + "-az1"}
		default: // region
			locations = []string{awsRegion()}
		}
	}
	var items strings.Builder
	for _, t := range types {
		for _, loc := range locations {
			fmt.Fprintf(&items, `<item><instanceType>%s</instanceType><location>%s</location><locationType>%s</locationType></item>`,
				xmlEscape(t), xmlEscape(loc), xmlEscape(locationType))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceTypeOfferingsResponse %s>
  <requestId>%s</requestId>
  <instanceTypeOfferingSet>%s</instanceTypeOfferingSet>
</DescribeInstanceTypeOfferingsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// ---- Network Interfaces ----

// ec2AnyStrInValues reports whether any of items matches any of vals.
func ec2AnyStrInValues(items, vals []string) bool {
	for _, it := range items {
		if ec2StrInValues(it, vals) {
			return true
		}
	}
	return false
}

// ec2NetworkInterfaceMatchesFilters applies the DescribeNetworkInterfaces filter
// set (previously all filters were ignored, so a scoped query returned every ENI).
func ec2NetworkInterfaceMatchesFilters(eni EC2NetworkInterface, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "network-interface-id":
			if !ec2StrInValues(eni.NetworkInterfaceId, vals) {
				return false
			}
		case "vpc-id":
			if !ec2StrInValues(eni.VpcId, vals) {
				return false
			}
		case "subnet-id":
			if !ec2StrInValues(eni.SubnetId, vals) {
				return false
			}
		case "status":
			if !ec2StrInValues(eni.Status, vals) {
				return false
			}
		case "attachment.instance-id":
			if !ec2StrInValues(eni.InstanceId, vals) {
				return false
			}
		case "description":
			if !ec2StrInValues(eni.Description, vals) {
				return false
			}
		case "interface-type":
			if !ec2StrInValues(eni.InterfaceType, vals) {
				return false
			}
		case "private-ip-address", "addresses.private-ip-address":
			if !ec2StrInValues(eni.PrivateIpAddress, vals) && !ec2AnyStrInValues(eni.SecondaryPrivateIps, vals) {
				return false
			}
		case "group-id":
			if !ec2AnyStrInValues(eni.SecurityGroupIds, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, eni.Tags); handled && !match {
				return false
			}
		}
	}
	return true
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

func handleDescribeNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "NetworkInterfaceId")
	var enis []EC2NetworkInterface
	if len(ids) > 0 {
		for _, id := range ids {
			eni, ok := ec2NetworkInterfaces.Get(id)
			if !ok {
				sim.AWSErrorf(w, "InvalidNetworkInterfaceID.NotFound", http.StatusBadRequest, "The networkInterface ID %q does not exist", id)
				return
			}
			enis = append(enis, eni)
		}
	} else {
		filters := ec2Filters(r)
		for _, eni := range ec2NetworkInterfaces.List() {
			if ec2NetworkInterfaceMatchesFilters(eni, filters) {
				enis = append(enis, eni)
			}
		}
	}
	var items strings.Builder
	for _, eni := range enis {
		items.WriteString("<item>" + eniFieldsXML(eni) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeNetworkInterfacesResponse %s>
  <requestId>%s</requestId>
  <networkInterfaceSet>%s</networkInterfaceSet>
</DescribeNetworkInterfacesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// ec2WriteSimpleResponse writes the `<NameResponse><requestId/><return>true</return></NameResponse>`
// shape used by EC2 mutation actions that have no payload.
func ec2WriteSimpleResponse(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%s %s>
  <requestId>%s</requestId><return>true</return>
</%s>`, name, ec2Xmlns(), generateUUID(), name)
}

// eniFieldsXML renders the inner ENI fields (no wrapper element), shared by
// DescribeNetworkInterfaces (wrapped in <item>) and CreateNetworkInterface
// (wrapped in <networkInterface>). The attachment block only appears when the
// ENI is attached, and sourceDestCheck reflects the (modifiable)
// SourceDestDisabled flag.
func eniFieldsXML(eni EC2NetworkInterface) string {
	var groups strings.Builder
	for _, groupID := range eni.SecurityGroupIds {
		name := groupID
		if sg, ok := ec2SecurityGroups.Get(groupID); ok {
			name = sg.GroupName
		}
		fmt.Fprintf(&groups, "<item><groupId>%s</groupId><groupName>%s</groupName></item>", groupID, name)
	}
	status := eni.Status
	if status == "" {
		status = "available"
	}
	ifaceType := eni.InterfaceType
	if ifaceType == "" {
		ifaceType = "interface"
	}
	sourceDest := "true"
	if eni.SourceDestDisabled {
		sourceDest = "false"
	}
	var privateIPs strings.Builder
	fmt.Fprintf(&privateIPs, "<item><privateIpAddress>%s</privateIpAddress><primary>true</primary></item>", eni.PrivateIpAddress)
	for _, ip := range eni.SecondaryPrivateIps {
		fmt.Fprintf(&privateIPs, "<item><privateIpAddress>%s</privateIpAddress><primary>false</primary></item>", ip)
	}
	attachment := ""
	if eni.AttachmentId != "" {
		attachment = fmt.Sprintf(`<attachment><attachmentId>%s</attachmentId><instanceId>%s</instanceId><deviceIndex>%d</deviceIndex><status>attached</status><deleteOnTermination>%t</deleteOnTermination></attachment>`,
			eni.AttachmentId, eni.InstanceId, eni.DeviceIndex, eni.DeleteOnTermination)
	}
	return fmt.Sprintf(`<networkInterfaceId>%s</networkInterfaceId>
    <subnetId>%s</subnetId>
    <vpcId>%s</vpcId>
    <availabilityZone>%s</availabilityZone>
    <description>%s</description>
    <ownerId>%s</ownerId>
    <requesterManaged>false</requesterManaged>
    <status>%s</status>
    <macAddress>02:00:00:00:00:01</macAddress>
    <privateIpAddress>%s</privateIpAddress>
    <privateDnsName>ip-%s.%s.compute.internal</privateDnsName>
    <sourceDestCheck>%s</sourceDestCheck>
    <interfaceType>%s</interfaceType>
    <groupSet>%s</groupSet>
    <privateIpAddressesSet>%s</privateIpAddressesSet>
    %s
    %s`,
		eni.NetworkInterfaceId, eni.SubnetId, eni.VpcId, awsAvailabilityZone(), eni.Description, eni.OwnerId, status,
		eni.PrivateIpAddress, strings.ReplaceAll(eni.PrivateIpAddress, ".", "-"), awsRegion(), sourceDest, ifaceType, groups.String(),
		privateIPs.String(), attachment, writeTagSetXML(eni.Tags))
}

// handleCreateNetworkInterface materializes a standalone ENI in a subnet
// (status "available", source/dest check on). Control-plane modeling like
// handleCreateNatGateway — no real fabric.
func handleCreateNetworkInterface(w http.ResponseWriter, r *http.Request) {
	subnetID := r.FormValue("SubnetId")
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID %q does not exist", subnetID), http.StatusBadRequest)
		return
	}
	privateIP := r.FormValue("PrivateIpAddress")
	if privateIP == "" {
		ip, err := AllocateSubnetIP(subnetID)
		if err != nil {
			ec2ErrorXML(w, "InsufficientFreeAddressesInSubnet", fmt.Sprintf("failed to allocate ENI private IP: %v", err), http.StatusBadRequest)
			return
		}
		privateIP = ip
	}
	eni := EC2NetworkInterface{
		NetworkInterfaceId: ec2ID("eni"),
		SubnetId:           subnetID,
		VpcId:              subnet.VpcId,
		PrivateIpAddress:   privateIP,
		Status:             "available",
		Description:        r.FormValue("Description"),
		SecurityGroupIds:   ec2ParamList(r, "SecurityGroupId"),
		InterfaceType:      r.FormValue("InterfaceType"),
		Tags:               parseTags(r),
		OwnerId:            ec2Owner(),
	}
	ec2NetworkInterfaces.Put(eni.NetworkInterfaceId, eni)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateNetworkInterfaceResponse %s>
  <requestId>%s</requestId>
  <networkInterface>%s</networkInterface>
</CreateNetworkInterfaceResponse>`, ec2Xmlns(), generateUUID(), eniFieldsXML(eni))
}

func handleAttachNetworkInterface(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	eni, ok := ec2NetworkInterfaces.Get(eniID)
	if !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The networkInterface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	if eni.AttachmentId != "" {
		ec2ErrorXML(w, "InvalidNetworkInterface.InUse", fmt.Sprintf("Interface %q is already attached", eniID), http.StatusBadRequest)
		return
	}
	attachID := ec2ID("eni-attach")
	eni.AttachmentId = attachID
	eni.InstanceId = r.FormValue("InstanceId")
	if di := r.FormValue("DeviceIndex"); di != "" {
		if _, err := fmt.Sscanf(di, "%d", &eni.DeviceIndex); err != nil {
			eni.DeviceIndex = 0
		}
	}
	eni.Status = "in-use"
	ec2NetworkInterfaces.Put(eniID, eni)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AttachNetworkInterfaceResponse %s>
  <requestId>%s</requestId>
  <attachmentId>%s</attachmentId>
</AttachNetworkInterfaceResponse>`, ec2Xmlns(), generateUUID(), attachID)
}

func handleDetachNetworkInterface(w http.ResponseWriter, r *http.Request) {
	attachID := r.FormValue("AttachmentId")
	for _, eni := range ec2NetworkInterfaces.List() {
		if eni.AttachmentId == attachID {
			eni.AttachmentId = ""
			eni.InstanceId = ""
			eni.DeviceIndex = 0
			eni.Status = "available"
			ec2NetworkInterfaces.Put(eni.NetworkInterfaceId, eni)
			ec2WriteSimpleResponse(w, "DetachNetworkInterfaceResponse")
			return
		}
	}
	ec2ErrorXML(w, "InvalidAttachmentID.NotFound", fmt.Sprintf("The attachment ID %q does not exist", attachID), http.StatusBadRequest)
}

func handleDeleteNetworkInterface(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	eni, ok := ec2NetworkInterfaces.Get(eniID)
	if !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The networkInterface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	if eni.AttachmentId != "" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Network interface %q is currently in use", eniID), http.StatusBadRequest)
		return
	}
	ec2NetworkInterfaces.Delete(eniID)
	ec2WriteSimpleResponse(w, "DeleteNetworkInterfaceResponse")
}

func handleModifyNetworkInterfaceAttribute(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	eni, ok := ec2NetworkInterfaces.Get(eniID)
	if !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The networkInterface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("SourceDestCheck.Value"); v != "" {
		eni.SourceDestDisabled = v == "false"
	}
	if v := r.FormValue("Description.Value"); v != "" {
		eni.Description = v
	}
	if groups := ec2ParamList(r, "SecurityGroupId"); len(groups) > 0 {
		eni.SecurityGroupIds = groups
	}
	if v := r.FormValue("Attachment.DeleteOnTermination"); v != "" {
		eni.DeleteOnTermination = v == "true"
	}
	ec2NetworkInterfaces.Put(eniID, eni)
	ec2WriteSimpleResponse(w, "ModifyNetworkInterfaceAttributeResponse")
}

func handleAssignPrivateIpAddresses(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	eni, ok := ec2NetworkInterfaces.Get(eniID)
	if !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The networkInterface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	assigned := ec2ParamList(r, "PrivateIpAddress")
	if n := r.FormValue("SecondaryPrivateIpAddressCount"); n != "" {
		var count int
		if _, err := fmt.Sscanf(n, "%d", &count); err != nil {
			count = 0
		}
		for i := 0; i < count; i++ {
			ip, err := AllocateSubnetIP(eni.SubnetId)
			if err != nil {
				break
			}
			assigned = append(assigned, ip)
		}
	}
	eni.SecondaryPrivateIps = append(eni.SecondaryPrivateIps, assigned...)
	ec2NetworkInterfaces.Put(eniID, eni)
	var ipItems strings.Builder
	for _, ip := range assigned {
		fmt.Fprintf(&ipItems, "<item><privateIpAddress>%s</privateIpAddress></item>", ip)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssignPrivateIpAddressesResponse %s>
  <requestId>%s</requestId>
  <networkInterfaceId>%s</networkInterfaceId>
  <assignedPrivateIpAddressesSet>%s</assignedPrivateIpAddressesSet>
</AssignPrivateIpAddressesResponse>`, ec2Xmlns(), generateUUID(), eniID, ipItems.String())
}

// removePermission drops the stored permission(s) whose protocol, ports AND
// source set match the revoke target. Matching on protocol+ports alone (the
// prior behaviour) removed every rule sharing a port range — e.g. revoking one
// of two :443 ingress rules from different CIDRs deleted both.
func removePermission(perms []EC2IpPermission, target EC2IpPermission) []EC2IpPermission {
	targetKey := permSourceKey(target)
	var result []EC2IpPermission
	for _, p := range perms {
		if p.IpProtocol == target.IpProtocol && p.FromPort == target.FromPort &&
			p.ToPort == target.ToPort && permSourceKey(p) == targetKey {
			continue
		}
		result = append(result, p)
	}
	return result
}

// ec2PermissionExists reports whether target matches a permission already
// present in perms. It is used by the revoke handlers to return
// InvalidPermission.NotFound for non-existent rules, matching real AWS behavior
// for security groups in non-default VPCs.
func ec2PermissionExists(perms []EC2IpPermission, target EC2IpPermission) bool {
	targetKey := permSourceKey(target)
	for _, p := range perms {
		if p.IpProtocol == target.IpProtocol && p.FromPort == target.FromPort &&
			p.ToPort == target.ToPort && permSourceKey(p) == targetKey {
			return true
		}
	}
	return false
}

// permSourceKey is a canonical, order-independent key over a permission's
// sources (IPv4/IPv6 CIDRs, prefix lists, referenced groups) used to match a
// revoke against the exact authorized rule.
func permSourceKey(p EC2IpPermission) string {
	var parts []string
	for _, r := range p.IpRanges {
		parts = append(parts, "v4:"+r.CidrIp)
	}
	for _, r := range p.Ipv6Ranges {
		parts = append(parts, "v6:"+r.CidrIpv6)
	}
	for _, r := range p.PrefixListIds {
		parts = append(parts, "pl:"+r.PrefixListId)
	}
	for _, g := range p.UserIdGroupPairs {
		parts = append(parts, "sg:"+g.GroupId)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
