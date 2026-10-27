package main

import (
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

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
	DefaultForAz                            bool
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
	AvailabilityMode    string
	Tags                []EC2Tag
	NatGatewayAddresses []EC2NatGatewayAddress
	CreateTime          string
	FailureCode         string
	FailureMessage      string
	AutoProvisionZones  string
	AutoScalingIps      string
	// Zones holds where a regional gateway carries each covered zone's traffic.
	Zones []EC2NatGatewayZone
}

type EC2NatGatewayAddress struct {
	AllocationId       string
	PublicIp           string
	PrivateIp          string
	NetworkInterfaceId string
	AssociationId      string
	IsPrimary          bool
	Status             string
	AvailabilityZone   string
	AvailabilityZoneId string
	// AmazonProvided marks an address a regional gateway in automatic mode
	// allocated itself; the gateway releases it when it leaves the zone.
	AmazonProvided bool
}

// A regional NAT gateway's network interface in one zone, where the zone's
// traffic leaves the VPC.
type EC2NatGatewayZone struct {
	AvailabilityZone   string
	SubnetId           string
	PrivateIp          string
	NetworkInterfaceId string
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
	AmiLaunchIndex         int
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
	// ShutdownBehavior is InstanceInitiatedShutdownBehavior: "stop" or
	// "terminate".
	ShutdownBehavior     string
	MetadataHttpTokens   string
	MetadataHttpEndpoint string
	MetadataHopLimit     int
	MetadataInstanceTags string
	// MetadataHttpProtocolIpv6 is HttpProtocolIpv6: "enabled" or "disabled", the
	// default.
	MetadataHttpProtocolIpv6 string
	// InstanceLifecycle is "spot" for a Spot Instance and empty for On-Demand.
	InstanceLifecycle     string
	SpotInstanceRequestId string
	Placement             EC2Placement
	// StateTransitionReason is empty while the instance runs.
	StateTransitionReason string
	// The structured stateReason (e.g. Client.UserInitiatedShutdown)
	// DescribeInstances returns for stopped and terminated instances.
	StateReasonCode    string
	StateReasonMessage string
}

type EC2Placement struct {
	Tenancy         string
	HostId          string
	Affinity        string
	GroupName       string
	GroupId         string
	PartitionNumber int
}

// An instance launched without a tenancy runs on shared hardware, reported as
// `default`.
func ec2PlacementXML(inst EC2Instance) string {
	az := awsAvailabilityZone()
	if subnet, ok := ec2Subnets.Get(inst.SubnetId); ok && subnet.AvailabilityZone != "" {
		az = subnet.AvailabilityZone
	}
	p := inst.Placement
	tenancy := p.Tenancy
	if tenancy == "" {
		tenancy = "default"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<placement><availabilityZone>%s</availabilityZone>", xmlEscape(az))
	if p.Affinity != "" {
		fmt.Fprintf(&b, "<affinity>%s</affinity>", xmlEscape(p.Affinity))
	}
	if p.GroupName != "" {
		fmt.Fprintf(&b, "<groupName>%s</groupName>", xmlEscape(p.GroupName))
	} else {
		b.WriteString("<groupName/>")
	}
	if p.GroupId != "" {
		fmt.Fprintf(&b, "<groupId>%s</groupId>", xmlEscape(p.GroupId))
	}
	if p.PartitionNumber > 0 {
		fmt.Fprintf(&b, "<partitionNumber>%d</partitionNumber>", p.PartitionNumber)
	}
	if p.HostId != "" {
		fmt.Fprintf(&b, "<hostId>%s</hostId>", xmlEscape(p.HostId))
	}
	fmt.Fprintf(&b, "<tenancy>%s</tenancy></placement>", xmlEscape(tenancy))
	return b.String()
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
}

// A retried RunInstances with the same ClientToken replays LaunchInstances, the
// original response verbatim, including the "pending" state every instance
// reported at launch.
type EC2RunInstancesToken struct {
	Token           string
	ReservationId   string
	InstanceIds     []string
	LaunchInstances []EC2Instance
}

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
	// The SDK re-sends ClientToken on every retry, and Amazon EC2 replays the
	// original instances instead of launching a duplicate batch.
	ec2RunTokens sim.Store[EC2RunInstancesToken]
	// Persist the cursor so a restarted simulator never hands out an address in
	// use.
	ec2SubnetIPCursor sim.Store[uint32]
	// The resource stores own addresses; the cursor only picks where the next
	// scan starts.
	ec2SubnetIPAllocationMu sync.Mutex
)

// Active resources are the allocation ledger, so an address returns to the
// pool when its network interface, NAT gateway or Amazon ECS task goes; the
// cursor only picks where the circular search begins. An unknown subnet is
// InvalidSubnetID.NotFound. The first four addresses and the last are
// reserved.
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
		// AWS reserves .0 network, .1 router, .2 DNS and .3 future use.
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

// A stopped Amazon ECS task no longer owns its network interface address,
// although the task stays describable.
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
			for _, zone := range gateway.Zones {
				if zone.SubnetId == subnetID {
					add(zone.PrivateIp)
				}
			}
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

// Follows awsAccountID(), so an account override reaches every OwnerId.
func ec2Owner() string { return awsAccountID() }

// Returns "" when no default VPC exists.
func defaultVPCSubnetID() string {
	var subnets []string
	for _, s := range ec2Subnets.List() {
		if v, ok := ec2Vpcs.Get(s.VpcId); ok && v.IsDefault {
			subnets = append(subnets, s.SubnetId)
		}
	}
	if len(subnets) == 0 {
		return ""
	}
	slices.Sort(subnets)
	return subnets[0]
}

// Seed the default VPC and one default subnet per Availability Zone, as AWS
// provisions an account. The IDs are deterministic, like the account ID, so
// callers can reference them. Idempotent.
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
	// A default subnet is "a size /20 IPv4 CIDR block in the specified
	// Availability Zone in your default VPC".
	for i, zone := range awsAvailabilityZones() {
		id := fmt.Sprintf("subnet-0123456789abcdef%d", i)
		if _, ok := ec2Subnets.Get(id); ok {
			continue
		}
		ec2Subnets.Put(id, ec2NewDefaultSubnet(id, "vpc-sim", zone, fmt.Sprintf("172.31.%d.0/20", 16*i), false))
	}
}

func registerEC2(r *AWSQueryRouter, srv *sim.Server) {
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

	r.Register("DescribeAccountAttributes", handleDescribeAccountAttributes)
	r.Register("DescribeAvailabilityZones", handleDescribeAvailabilityZones)
	r.Register("DescribeRegions", handleDescribeRegions)
	r.Register("CreateVpc", handleCreateVpc)
	r.Register("DescribeVpcs", handleDescribeVpcs)
	r.Register("DeleteVpc", handleDeleteVpc)
	r.Register("DescribeVpcAttribute", handleDescribeVpcAttribute)
	r.Register("ModifyVpcAttribute", handleModifyVpcAttribute)

	r.Register("CreateSubnet", handleCreateSubnet)
	r.Register("DescribeSubnets", handleDescribeSubnets)
	r.Register("DeleteSubnet", handleDeleteSubnet)
	r.Register("ModifySubnetAttribute", handleModifySubnetAttribute)

	r.Register("CreateInternetGateway", handleCreateInternetGateway)
	r.Register("AttachInternetGateway", handleAttachInternetGateway)
	r.Register("DetachInternetGateway", handleDetachInternetGateway)
	r.Register("DescribeInternetGateways", handleDescribeInternetGateways)
	r.Register("DeleteInternetGateway", handleDeleteInternetGateway)

	r.Register("AllocateAddress", handleAllocateAddress)
	r.Register("AssociateAddress", handleAssociateAddress)
	r.Register("DisassociateAddress", handleDisassociateAddress)
	r.Register("DescribeAddresses", handleDescribeAddresses)
	r.Register("DescribeAddressesAttribute", handleDescribeAddressesAttribute)
	r.Register("ReleaseAddress", handleReleaseAddress)

	r.Register("CreateNatGateway", handleCreateNatGateway)
	r.Register("DescribeNatGateways", handleDescribeNatGateways)
	r.Register("DeleteNatGateway", handleDeleteNatGateway)

	r.Register("CreateVpcEndpoint", handleCreateVpcEndpoint)
	r.Register("DescribeVpcEndpoints", handleDescribeVpcEndpoints)
	r.Register("DeleteVpcEndpoints", handleDeleteVpcEndpoints)

	r.Register("CreateRouteTable", handleCreateRouteTable)
	r.Register("DescribeRouteTables", handleDescribeRouteTables)
	r.Register("DeleteRouteTable", handleDeleteRouteTable)
	r.Register("CreateRoute", handleCreateRoute)
	r.Register("ReplaceRoute", handleReplaceRoute)
	r.Register("DeleteRoute", handleDeleteRoute)
	r.Register("AssociateRouteTable", handleAssociateRouteTable)
	r.Register("DisassociateRouteTable", handleDisassociateRouteTable)

	r.Register("CreateSecurityGroup", handleCreateSecurityGroup)
	r.Register("DescribeSecurityGroups", handleDescribeSecurityGroups)
	r.Register("ValidateSecurityGroupQuotasForInterface", handleValidateSecurityGroupQuotasForInterface)
	r.Register("DescribeSecurityGroupRules", handleDescribeSecurityGroupRules)
	r.Register("ModifySecurityGroupRules", handleModifySecurityGroupRules)
	r.Register("DeleteSecurityGroup", handleDeleteSecurityGroup)
	r.Register("AuthorizeSecurityGroupIngress", handleAuthorizeSecurityGroupIngress)
	r.Register("AuthorizeSecurityGroupEgress", handleAuthorizeSecurityGroupEgress)
	r.Register("RevokeSecurityGroupIngress", handleRevokeSecurityGroupIngress)
	r.Register("RevokeSecurityGroupEgress", handleRevokeSecurityGroupEgress)
	r.Register("UpdateSecurityGroupRuleDescriptionsIngress", handleUpdateSecurityGroupRuleDescriptionsIngress)
	r.Register("UpdateSecurityGroupRuleDescriptionsEgress", handleUpdateSecurityGroupRuleDescriptionsEgress)

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

	ensureSimDefaults()

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
	registerEC2ImageInstanceTypeSpec(r, srv)
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
	return prefix + "-" + sim.NewUUID()[:8]
}

// The millisecond-precision UTC form AMIs use for creationDate.
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
</DescribeAccountAttributesResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleDescribeAvailabilityZones(w http.ResponseWriter, r *http.Request) {
	region := awsRegion()
	zones := awsAvailabilityZones()
	names := ec2ParamList(r, "ZoneName")
	ids := ec2ParamList(r, "ZoneId")
	for _, name := range names {
		if !slices.Contains(zones, name) {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter ZoneName is invalid.", name), http.StatusBadRequest)
			return
		}
	}
	for _, id := range ids {
		if !slices.ContainsFunc(zones, func(zone string) bool { return ec2AvailabilityZoneId(zone) == id }) {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter ZoneId is invalid.", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	for name := range filters {
		switch name {
		case "zone-name", "zone-id", "state", "region-name", "zone-type", "group-name":
		default:
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("The filter '%s' is invalid", name), http.StatusBadRequest)
			return
		}
	}
	var items strings.Builder
	for _, zone := range zones {
		id := ec2AvailabilityZoneId(zone)
		if len(names) > 0 && !ec2StrInValues(zone, names) || len(ids) > 0 && !ec2StrInValues(id, ids) {
			continue
		}
		fields := map[string]string{"zone-name": zone, "zone-id": id, "state": "available", "region-name": region, "zone-type": "availability-zone", "group-name": region}
		matches := true
		for name, values := range filters {
			matches = matches && ec2StrInValues(fields[name], values)
		}
		if !matches {
			continue
		}
		fmt.Fprintf(&items, `<item><zoneName>%s</zoneName><zoneId>%s</zoneId><zoneType>availability-zone</zoneType><regionName>%s</regionName><zoneState>available</zoneState><groupName>%s</groupName><networkBorderGroup>%s</networkBorderGroup></item>`,
			zone, id, region, region, region)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeAvailabilityZonesResponse %s>
  <requestId>%s</requestId>
  <availabilityZoneInfo>%s</availabilityZoneInfo>
</DescribeAvailabilityZonesResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func handleDescribeRegions(w http.ResponseWriter, r *http.Request) {
	region := awsRegion()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeRegionsResponse %s>
  <requestId>%s</requestId>
  <regionInfo><item><regionName>%s</regionName><regionEndpoint>ec2.%s.amazonaws.com</regionEndpoint><optInStatus>opt-in-not-required</optInStatus></item></regionInfo>
</DescribeRegionsResponse>`, ec2Xmlns(), sim.NewUUID(), region, region)
}

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
	awsWriteError(w, awsErrorEnvelope{Protocol: awsEC2Query}, status, code, message, "")
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

// A filter's values OR together.
func ec2StrInValues(s string, vals []string) bool {
	for _, v := range vals {
		if v == s {
			return true
		}
	}
	return false
}

func ec2AnyStrInValues(items, vals []string) bool {
	for _, it := range items {
		if ec2StrInValues(it, vals) {
			return true
		}
	}
	return false
}

// For mutations whose response is only `<return>true</return>`.
func ec2WriteSimpleResponse(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%s %s>
  <requestId>%s</requestId><return>true</return>
</%s>`, name, ec2Xmlns(), sim.NewUUID(), name)
}
