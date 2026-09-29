package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/realexec/fabric"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

var (
	// ec2Fabric realizes VPCs, subnets, the taps behind instance ENIs, the
	// namespaces behind NAT gateways, and the instances' Firecracker machines.
	ec2Fabric = fabric.New[string](fabric.Options{
		NetworkPrefix: "avn",
		SubnetPrefix:  "asb",
		Reserved:      ec2SubnetReservation,
		OnTapClosed:   func(tap *realexec.TapNIC) { imdsInstancesByIP.Delete(tap.PrivateIP.String()) },
	})
	// ec2RealMu guards the attachments only AWS makes. Reads — resolving a
	// security group's member addresses, listing the attachments to reapply —
	// exclude nothing but a writer.
	ec2RealMu         sync.RWMutex
	ec2RealEBSSlots   = map[string]map[string]string{}
	ec2RealECSNICs    = map[string]*realexec.NamespaceNIC{} // taskID -> veth into the container netns
	ec2RealLambdaNICs = map[string]ec2RealLambdaNIC{}       // invocation ID -> Hyperplane ENI realization
)

// ec2SubnetReservation is what Amazon VPC keeps in every subnet: the network
// address, the VPC router, the DNS server, one address for future use, and
// the broadcast address.
var ec2SubnetReservation = realexec.HostReservation{First: 4, Last: 1}

const ec2MACPrefix = "02:0a:ec"

type ec2RealLambdaNIC struct {
	NIC              *realexec.NamespaceNIC
	SubnetID         string
	PrivateIP        string
	SecurityGroupIDs []string
	RuntimeDNATTable string
}

// ec2ECSRealNetAvailable reports whether ECS tasks can be plumbed into real VPC
// network namespaces — Linux network capabilities plus nsenter (to configure the
// container's netns). When false the sim uses the cross-platform Docker-network
// tier instead.
func ec2ECSRealNetAvailable() bool {
	return realexec.DetectExternalNamespaceCapabilities().Require() == nil
}

// ec2AttachRealECSTaskNIC plumbs a veth from the task's VPC subnet bridge into
// the container's network namespace, giving it eth0 at the ENI IP. Because each
// VPC is its own netns, overlapping VPC CIDRs work natively — no remapping, the
// ENI IP is the container's real address. After the L2 path is up it programs
// the task's security group rules into the netns nftables ingress chain, so the
// SG is enforced at the packet layer on Linux + CAP_NET_ADMIN hosts. On hosts
// without real-exec capabilities, ec2ApplyRealECSTaskSecurityGroups is a no-op
// and SG rules remain metadata-only — enforced faithfully by the API surface
// (validation, DescribeSecurityGroups) but not at the host firewall level.
// mark, when non-nil, receives each step's name after the step, so the
// caller's phase timer attributes the attach instead of reporting it as one
// opaque window.
func ec2AttachRealECSTaskNIC(
	ctx context.Context,
	taskID, subnetID string,
	pid int,
	eniIP string,
	securityGroupIDs []string,
	mark func(string),
) error {
	step := func(name string) {
		if mark != nil {
			mark(name)
		}
	}
	ctx = realexec.WithMark(ctx, mark)
	sn, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return fmt.Errorf("subnet %s not found", subnetID)
	}
	defer ec2Fabric.HoldNetwork(sn.VpcId)()
	subnet, err := ec2CreateRealSubnet(ctx, sn)
	if err != nil {
		return err
	}
	step("vpc:subnet")
	nic, err := subnet.AttachExternalNamespaceNIC(ctx, realexec.ExternalNamespaceNICSpec{
		PID:           pid,
		HostVethName:  fabric.LinuxName("eh", taskID),
		GuestVethName: fabric.LinuxName("eg", taskID),
		GuestIfName:   "eth0",
		MAC:           fabric.DeriveMAC(ec2MACPrefix, taskID),
		PrivateIP:     net.ParseIP(eniIP),
	})
	if err != nil {
		return err
	}
	step("vpc:veth")
	metadataPort, err := workloadhost.ListenPort(simListenAddr)
	if err != nil {
		_ = nic.Close(context.Background())
		return err
	}
	if err := subnet.ConfigureAddressDNAT(ctx, realexec.ECSTaskMetadataIPv4, metadataPort, fabric.LinuxName("emd", sn.VpcId)); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure ECS task metadata routing for %s: %w", taskID, err)
	}
	step("vpc:task-metadata")
	if err := subnet.ConfigureMetadataDNAT(ctx, metadataPort, fabric.LinuxName("imd", sn.VpcId)); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure ECS IMDS routing for %s: %w", taskID, err)
	}
	step("vpc:imds")
	// The task's namespace holds only its own interface, so the resolver its
	// image was configured with — Docker's embedded 127.0.0.11, written before
	// the pause container was detached from Docker's networks — answers nothing.
	// Every lookup inside then blocks until it times out instead of failing,
	// which surfaces as a workload that binds its ports minutes late with
	// nothing logged in between. The VPC serves DNS at its own base address plus
	// two, exactly as AmazonProvidedDNS does, and that is where the task asks.
	if err := ec2ConfigureTaskResolver(ctx, subnet, sn.VpcId, taskID); err != nil {
		_ = nic.Close(context.Background())
		return err
	}
	step("vpc:resolver")
	if err := ec2ApplyRealVPCEgressPolicy(ctx, sn.VpcId); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure VPC egress policy for %s: %w", taskID, err)
	}
	step("vpc:egress")
	ec2RealMu.Lock()
	ec2RealECSNICs[taskID] = nic
	ec2RealMu.Unlock()
	if err := ec2ApplyRealECSTaskSecurityGroups(ctx, taskID, securityGroupIDs); err != nil {
		return fmt.Errorf("apply security groups for %s: %w", taskID, err)
	}
	step("vpc:security-groups")
	return nil
}

// ec2DetachRealECSTaskNIC tears down a task's VPC veth when the task stops.
func ec2DetachRealECSTaskNIC(ctx context.Context, taskID string) {
	ec2RealMu.Lock()
	nic := ec2RealECSNICs[taskID]
	delete(ec2RealECSNICs, taskID)
	ec2RealMu.Unlock()
	if nic != nil {
		_ = nic.Close(ctx)
	}
}

// ec2AttachRealLambdaNIC realizes the customer-VPC side of an AWS Lambda
// Hyperplane elastic network interface inside the invocation's network
// namespace. The Runtime API remains a Lambda-service endpoint: a dedicated
// link-local destination is DNATed to the per-invocation listener on the host,
// independently of the customer subnet's internet routes.
func ec2AttachRealLambdaNIC(
	ctx context.Context,
	invocationID, subnetID string,
	pid int,
	eniIP string,
	securityGroupIDs []string,
	runtimeIPv4 string,
	runtimePort int,
) error {
	sn, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return fmt.Errorf("subnet %s not found", subnetID)
	}
	defer ec2Fabric.HoldNetwork(sn.VpcId)()
	subnet, err := ec2CreateRealSubnet(ctx, sn)
	if err != nil {
		return err
	}
	nic, err := subnet.AttachExternalNamespaceNIC(ctx, realexec.ExternalNamespaceNICSpec{
		PID:           pid,
		HostVethName:  fabric.LinuxName("lh", invocationID),
		GuestVethName: fabric.LinuxName("lg", invocationID),
		GuestIfName:   "eth0",
		MAC:           fabric.DeriveMAC(ec2MACPrefix, invocationID),
		PrivateIP:     net.ParseIP(eniIP),
	})
	if err != nil {
		return err
	}
	metadataPort, err := workloadhost.ListenPort(simListenAddr)
	if err != nil {
		_ = nic.Close(context.Background())
		return err
	}
	if err := subnet.ConfigureMetadataDNAT(ctx, metadataPort, fabric.LinuxName("imd", sn.VpcId)); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure AWS Lambda instance metadata routing for %s: %w", invocationID, err)
	}
	runtimeTable := fabric.LinuxName("lrd", invocationID)
	if err := subnet.ConfigureAddressDNAT(ctx, runtimeIPv4, runtimePort, runtimeTable); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure AWS Lambda Runtime API routing for %s: %w", invocationID, err)
	}
	if err := ec2ApplyRealVPCEgressPolicy(ctx, sn.VpcId); err != nil {
		_ = subnet.RemoveAddressDNAT(context.Background(), runtimeTable)
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure VPC egress policy for AWS Lambda invocation %s: %w", invocationID, err)
	}
	attachment := ec2RealLambdaNIC{
		NIC:              nic,
		SubnetID:         subnetID,
		PrivateIP:        eniIP,
		SecurityGroupIDs: append([]string(nil), securityGroupIDs...),
		RuntimeDNATTable: runtimeTable,
	}
	ec2RealMu.Lock()
	ec2RealLambdaNICs[invocationID] = attachment
	ec2RealMu.Unlock()
	if err := ec2ApplyRealLambdaSecurityGroups(ctx, invocationID); err != nil {
		ec2DetachRealLambdaNIC(context.Background(), invocationID)
		return fmt.Errorf("apply security groups for AWS Lambda invocation %s: %w", invocationID, err)
	}
	return nil
}

func ec2DetachRealLambdaNIC(ctx context.Context, invocationID string) {
	ec2RealMu.Lock()
	attachment, ok := ec2RealLambdaNICs[invocationID]
	delete(ec2RealLambdaNICs, invocationID)
	ec2RealMu.Unlock()
	if !ok {
		return
	}
	if subnet := ec2Fabric.Subnet(attachment.SubnetID); subnet != nil {
		_ = subnet.RemoveAddressDNAT(ctx, attachment.RuntimeDNATTable)
	}
	if attachment.NIC != nil {
		_ = attachment.NIC.Close(ctx)
	}
}

const ec2RealEBSMaxSlots = 15

// ec2RealNetHostAvailable reports whether the host can build real EC2 network
// fabric (namespaces, bridges, veth, nftables). ec2RealVMHostAvailable reports
// whether it can run real Firecracker VMs. When false, the sim is in the
// API-only tier: the corresponding operations are modeled at the
// control plane without real execution, so IaC/control-plane testing works on
// hosts lacking CAP_NET_ADMIN/nft/KVM.
func ec2RealNetHostAvailable() bool {
	return realexec.DetectNetworkCapabilities().Require() == nil
}

func ec2RealVMHostAvailable() bool {
	return realexec.DetectFirecrackerCapabilities().Require() == nil
}

// ec2DeleteRealVPC tears the VPC's namespace down with everything in it,
// closing the Amazon ECS task and AWS Lambda interfaces that joined it first.
func ec2DeleteRealVPC(ctx context.Context, vpcID string) error {
	return ec2Fabric.TeardownNetwork(ctx, vpcID, func(ctx context.Context) {
		ec2RealMu.Lock()
		defer ec2RealMu.Unlock()
		for taskID, nic := range ec2RealECSNICs {
			if ec2ECSTaskVPCID(taskID) == vpcID {
				delete(ec2RealECSNICs, taskID)
				_ = nic.Close(ctx)
			}
		}
		for invocationID, attachment := range ec2RealLambdaNICs {
			if subnet, ok := ec2Subnets.Get(attachment.SubnetID); ok && subnet.VpcId == vpcID {
				delete(ec2RealLambdaNICs, invocationID)
				if attachment.NIC != nil {
					_ = attachment.NIC.Close(ctx)
				}
			}
		}
		for instanceID := range ec2RealEBSSlots {
			if inst, ok := ec2Instances.Get(instanceID); ok && inst.VpcId == vpcID {
				delete(ec2RealEBSSlots, instanceID)
			}
		}
	})
}

func ec2ECSTaskVPCID(taskID string) string {
	task, ok := ecsTasks.Get(taskID)
	if !ok {
		return ""
	}
	for _, att := range task.Attachments {
		if att.Type != "ElasticNetworkInterface" {
			continue
		}
		for _, d := range att.Details {
			if d.Name != "subnetId" {
				continue
			}
			if subnet, ok := ec2Subnets.Get(d.Value); ok {
				return subnet.VpcId
			}
		}
	}
	return ""
}

// ec2CreateRealSubnet realizes the subnet's bridge, and its VPC's namespace
// when that is not realized yet.
func ec2CreateRealSubnet(ctx context.Context, subnet EC2Subnet) (*realexec.Subnet, error) {
	if s := ec2Fabric.Subnet(subnet.SubnetId); s != nil {
		return s, nil
	}
	if _, ok := ec2Vpcs.Get(subnet.VpcId); !ok {
		return nil, fmt.Errorf("VPC %s not found", subnet.VpcId)
	}
	return ec2Fabric.EnsureSubnet(ctx, subnet.VpcId, subnet.SubnetId, subnet.CidrBlock, fabric.FirstHostGateway(subnet.CidrBlock))
}

// ec2DeleteRealNIC stops the instance the ENI is attached to and closes the
// ENI's fabric.
func ec2DeleteRealNIC(ctx context.Context, eniID string) error {
	var errs []error
	for _, inst := range ec2Instances.List() {
		if inst.NetworkInterfaceId == eniID {
			errs = append(errs, ec2StopRealVM(ctx, inst.InstanceId))
			break
		}
	}
	errs = append(errs, ec2Fabric.DeleteNIC(ctx, eniID))
	return errors.Join(errs...)
}

// ec2BuildIngressPacketRules materializes the nftables-facing packet rules for
// the ingress side of the supplied security groups. It expands every IpPermission
// into one PacketRule per source (IPv4 / IPv6 / SG reference, with no CIDR
// treated as 0.0.0.0/0 to match real AWS' "all sources" semantics). Referenced
// security groups expand to their member CIDRs at apply time, since the nftables
// tier operates on IP prefixes rather than SG ids.
func ec2BuildIngressPacketRules(securityGroupIDs []string) []realexec.PacketRule {
	var rules []realexec.PacketRule
	// A group's members are read once per attach. Every rule that names the
	// group as its source walked the whole ENI, instance and task stores again
	// (three rules, three scans, on the workspace task that led to #139).
	members := map[string][]string{}
	membersOf := func(groupID string) []string {
		if cidrs, ok := members[groupID]; ok {
			return cidrs
		}
		cidrs := ec2SGMemberCIDRs(groupID)
		members[groupID] = cidrs
		return cidrs
	}
	for _, groupID := range securityGroupIDs {
		sg, ok := ec2SecurityGroups.Get(groupID)
		if !ok {
			continue
		}
		for _, perm := range sg.IpPermissions {
			if len(perm.IpRanges) == 0 && len(perm.Ipv6Ranges) == 0 && len(perm.UserIdGroupPairs) == 0 {
				rules = append(rules, realexec.PacketRule{
					Protocol:   perm.IpProtocol,
					SourceCIDR: "0.0.0.0/0",
					FromPort:   perm.FromPort,
					ToPort:     perm.ToPort,
				})
				continue
			}
			for _, ipRange := range perm.IpRanges {
				rules = append(rules, realexec.PacketRule{
					Protocol:   perm.IpProtocol,
					SourceCIDR: ipRange.CidrIp,
					FromPort:   perm.FromPort,
					ToPort:     perm.ToPort,
				})
			}
			for _, ipRange := range perm.Ipv6Ranges {
				rules = append(rules, realexec.PacketRule{
					Protocol:   perm.IpProtocol,
					SourceCIDR: ipRange.CidrIpv6,
					FromPort:   perm.FromPort,
					ToPort:     perm.ToPort,
				})
			}
			for _, gp := range perm.UserIdGroupPairs {
				for _, src := range membersOf(gp.GroupId) {
					rules = append(rules, realexec.PacketRule{
						Protocol:   perm.IpProtocol,
						SourceCIDR: src,
						FromPort:   perm.FromPort,
						ToPort:     perm.ToPort,
					})
				}
			}
		}
	}
	return rules
}

// ec2SGMemberCIDRs returns the set of IPv4 /32 prefixes currently attached to
// the supplied security group — every ENI (EC2 instance or standalone), Amazon
// ECS task, and active AWS Lambda Hyperplane ENI whose SG list contains groupID
// contributes its private IP. Security group references resolve to live member
// IPs at apply time, since nftables matches on prefixes, not on SG ids.
func ec2SGMemberCIDRs(groupID string) []string {
	seen := map[string]bool{}
	add := func(ip string) {
		if ip == "" {
			return
		}
		seen[ip+"/32"] = true
	}
	for _, eni := range ec2NetworkInterfaces.List() {
		for _, id := range eni.SecurityGroupIds {
			if id == groupID {
				add(eni.PrivateIpAddress)
				break
			}
		}
	}
	for _, inst := range ec2Instances.List() {
		for _, id := range inst.SecurityGroupIds {
			if id == groupID {
				add(inst.PrivateIpAddress)
				break
			}
		}
	}
	for _, task := range ecsTasks.List() {
		// A stopped task holds no address any more: its ENI went with it, and
		// the store keeps the task for an hour for DescribeTasks. Counting it
		// tripled a group's member list on a deployment whose scheduled task
		// runs every five minutes, and every member is a packet rule.
		if task.LastStatus == ECSTaskStatusStopped {
			continue
		}
		if !ecsTaskUsesSecurityGroup(task, groupID) {
			continue
		}
		for _, att := range task.Attachments {
			if att.Type != "ElasticNetworkInterface" {
				continue
			}
			for _, d := range att.Details {
				if d.Name == "privateIPv4Address" {
					add(d.Value)
				}
			}
		}
	}
	ec2RealMu.RLock()
	for _, attachment := range ec2RealLambdaNICs {
		if stringInSlice(groupID, attachment.SecurityGroupIDs) {
			add(attachment.PrivateIP)
		}
	}
	ec2RealMu.RUnlock()
	var out []string
	for cidr := range seen {
		out = append(out, cidr)
	}
	sort.Strings(out)
	return out
}

func ecsTaskUsesSecurityGroup(task ECSTask, groupID string) bool {
	if task.NetworkConfiguration == nil || task.NetworkConfiguration.AwsvpcConfiguration == nil {
		return false
	}
	for _, id := range task.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups {
		if id == groupID {
			return true
		}
	}
	return false
}

// ec2ApplyRealNICSecurityGroups filters the ENI's tap. No security groups
// leaves the ENI open: AWS would assign the VPC's default group, which the
// simulator does not model.
func ec2ApplyRealNICSecurityGroups(ctx context.Context, eniID string, securityGroupIDs []string) error {
	if !ec2Fabric.Realized(eniID) {
		return nil
	}
	var stages [][]realexec.PacketRule
	if len(securityGroupIDs) > 0 {
		stages = [][]realexec.PacketRule{ec2BuildIngressPacketRules(securityGroupIDs)}
	}
	return ec2Fabric.ApplyIngress(ctx, eniID, stages)
}

// ec2ApplyRealECSTaskSecurityGroups programs the nftables ingress filter for an
// attached ECS task NIC, enforcing the task's security-group rules at the packet
// layer. Called both at task attach (the first time the NIC exists) and on every
// Authorize/Revoke that touches one of the task's security groups — via
// ec2ReapplyRealSecurityGroup — so adding a port to a running task's SG opens it
// immediately without restarting the task.
func ec2ApplyRealECSTaskSecurityGroups(ctx context.Context, taskID string, securityGroupIDs []string) error {
	ec2RealMu.Lock()
	nic := ec2RealECSNICs[taskID]
	ec2RealMu.Unlock()
	if nic == nil {
		return nil
	}
	// No security groups means default-allow. An empty ruleset would install a
	// deny-all filter (the realexec layer ends every filter with a drop rule),
	// breaking tasks that rely on the previous default-allow behaviour.
	if len(securityGroupIDs) == 0 {
		return nic.ClearIngressFilter(ctx)
	}
	// Building the rules resolves every referenced group's members from the
	// stores, which is its own cost apart from rendering and committing them.
	rules := ec2BuildIngressPacketRules(securityGroupIDs)
	realexec.MarkFrom(ctx)("sg:rules")
	return nic.ConfigureIngressFilter(ctx, rules)
}

func ec2ApplyRealLambdaSecurityGroups(ctx context.Context, invocationID string) error {
	ec2RealMu.Lock()
	attachment, ok := ec2RealLambdaNICs[invocationID]
	ec2RealMu.Unlock()
	if !ok || attachment.NIC == nil {
		return nil
	}
	if len(attachment.SecurityGroupIDs) == 0 {
		return attachment.NIC.ClearIngressFilter(ctx)
	}
	return attachment.NIC.ConfigureIngressFilter(ctx, ec2BuildIngressPacketRules(attachment.SecurityGroupIDs))
}

func ec2StartRealVM(ctx context.Context, inst EC2Instance) error {
	if inst.NetworkInterfaceId == "" {
		return fmt.Errorf("instance %s has no network interface", inst.InstanceId)
	}
	metadataPort, err := workloadhost.ListenPort(simListenAddr)
	if err != nil {
		return err
	}
	vcpus, memMiB := ec2InstanceMachineShape(inst.InstanceType)
	var slots map[string]string
	_, _, started, err := ec2Fabric.StartVM(ctx, fabric.VMSpec[string]{
		Key:     inst.InstanceId,
		Network: inst.VpcId,
		Subnet:  inst.SubnetId,
		NIC:     inst.NetworkInterfaceId,
		EnsureSubnet: func(ctx context.Context) error {
			sn, ok := ec2Subnets.Get(inst.SubnetId)
			if !ok {
				return fmt.Errorf("subnet %s not found", inst.SubnetId)
			}
			_, err := ec2CreateRealSubnet(ctx, sn)
			return err
		},
		Tap: realexec.TapNICSpec{
			TapName:   fabric.LinuxName("at", inst.NetworkInterfaceId),
			PrivateIP: net.ParseIP(inst.PrivateIpAddress),
			MAC:       fabric.DeriveMAC(ec2MACPrefix, inst.NetworkInterfaceId),
		},
		MetadataPort:  metadataPort,
		MetadataTable: fabric.LinuxName("amd", inst.VpcId),
		Machine: realexec.FirecrackerVMConfig{
			ID:        "aws-" + inst.InstanceId,
			VCPUCount: vcpus,
			MemoryMiB: memMiB,
		},
		BeforeBoot: func(tap *realexec.TapNIC, machine *realexec.FirecrackerVMConfig) error {
			imdsInstancesByIP.Store(tap.PrivateIP.String(), inst)
			drives, driveSlots, err := ec2RealEBSBlockDrives(inst)
			if err != nil {
				return err
			}
			machine.BlockDrives = drives
			slots = driveSlots
			return nil
		},
	})
	if err != nil || !started {
		return err
	}
	ec2RealMu.Lock()
	ec2RealEBSSlots[inst.InstanceId] = slots
	ec2RealMu.Unlock()
	return ec2ApplyRealNICSecurityGroups(ctx, inst.NetworkInterfaceId, inst.SecurityGroupIds)
}

// ec2InstanceMachineShape sizes the instance's machine from the instance-type
// catalog DescribeInstanceTypes and the requirements matcher serve. A type the
// catalog does not carry boots at the substrate's smallest shape.
func ec2InstanceMachineShape(instanceType string) (vcpus, memMiB int) {
	for _, entry := range ec2InstanceTypeCatalog() {
		if entry.name == instanceType {
			return entry.vcpus, entry.memMiB
		}
	}
	return 1, 512
}

func ec2StopRealVM(ctx context.Context, instanceID string) error {
	ec2RealMu.Lock()
	delete(ec2RealEBSSlots, instanceID)
	ec2RealMu.Unlock()
	return ec2Fabric.StopVM(ctx, instanceID, nil)
}

func ec2RealEBSBlockDrives(inst EC2Instance) ([]realexec.FirecrackerBlockDrive, map[string]string, error) {
	attachments := ec2RealEBSAttachments(inst.InstanceId, inst.RootDeviceName)
	if len(attachments) > ec2RealEBSMaxSlots {
		return nil, nil, fmt.Errorf("instance %s has %d EBS data volumes attached, maximum supported by the Firecracker substrate is %d", inst.InstanceId, len(attachments), ec2RealEBSMaxSlots)
	}
	slots := map[string]string{}
	drives := make([]realexec.FirecrackerBlockDrive, 0, ec2RealEBSMaxSlots)
	for i := 1; i <= ec2RealEBSMaxSlots; i++ {
		slot := ec2RealEBSSlotID(i)
		path := ec2RealEBSSlotPlaceholderPath(inst.InstanceId, slot)
		if i <= len(attachments) {
			vol, ok := ec2Volumes.Get(attachments[i-1].VolumeId)
			if !ok {
				return nil, nil, fmt.Errorf("attached volume %s not found", attachments[i-1].VolumeId)
			}
			blockPath, err := ebsEnsureVolumeBlockImage(&vol)
			if err != nil {
				return nil, nil, fmt.Errorf("prepare block image for %s: %w", vol.VolumeId, err)
			}
			ec2Volumes.Put(vol.VolumeId, vol)
			path = blockPath
			slots[vol.VolumeId] = slot
		} else if err := ec2PrepareRealEBSSlotPlaceholder(path); err != nil {
			return nil, nil, err
		}
		drives = append(drives, realexec.FirecrackerBlockDrive{
			ID:   slot,
			Path: path,
		})
	}
	return drives, slots, nil
}

func ec2RealEBSAttachments(instanceID, rootDeviceName string) []EC2VolumeAttachment {
	var attachments []EC2VolumeAttachment
	for _, vol := range ec2Volumes.List() {
		if len(vol.Attachments) == 0 {
			continue
		}
		att := vol.Attachments[0]
		if att.InstanceId != instanceID || att.Device == rootDeviceName {
			continue
		}
		attachments = append(attachments, att)
	}
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].Device < attachments[j].Device
	})
	return attachments
}

func ec2AttachRealVolume(ctx context.Context, instanceID string, vol *EC2Volume) error {
	inst, ok := ec2Instances.Get(instanceID)
	if !ok || inst.State != "running" {
		return nil
	}
	blockPath, err := ebsEnsureVolumeBlockImage(vol)
	if err != nil {
		return err
	}
	ec2RealMu.Lock()
	vm := ec2Fabric.VM(instanceID)
	slots := ec2RealEBSSlots[instanceID]
	if slots == nil {
		slots = map[string]string{}
		ec2RealEBSSlots[instanceID] = slots
	}
	slot := slots[vol.VolumeId]
	if slot == "" {
		slot = ec2FirstFreeRealEBSSlot(slots)
	}
	ec2RealMu.Unlock()
	if vm == nil || !vm.Alive() {
		return fmt.Errorf("instance %s is running without a live Firecracker VM", instanceID)
	}
	if slot == "" {
		return fmt.Errorf("AttachmentLimitExceeded: no Firecracker EBS drive slots are available for instance %s", instanceID)
	}
	if err := vm.PatchBlockDrivePath(ctx, slot, blockPath); err != nil {
		return err
	}
	ec2RealMu.Lock()
	if ec2RealEBSSlots[instanceID] == nil {
		ec2RealEBSSlots[instanceID] = map[string]string{}
	}
	ec2RealEBSSlots[instanceID][vol.VolumeId] = slot
	ec2RealMu.Unlock()
	return nil
}

func ec2DetachRealVolume(ctx context.Context, instanceID, volumeID string) error {
	inst, ok := ec2Instances.Get(instanceID)
	if !ok || inst.State != "running" {
		return nil
	}
	ec2RealMu.Lock()
	vm := ec2Fabric.VM(instanceID)
	slot := ""
	if slots := ec2RealEBSSlots[instanceID]; slots != nil {
		slot = slots[volumeID]
	}
	ec2RealMu.Unlock()
	if slot == "" {
		return nil
	}
	if vm == nil || !vm.Alive() {
		return fmt.Errorf("instance %s is running without a live Firecracker VM", instanceID)
	}
	placeholder := ec2RealEBSSlotPlaceholderPath(instanceID, slot)
	if err := ec2PrepareRealEBSSlotPlaceholder(placeholder); err != nil {
		return err
	}
	if err := vm.PatchBlockDrivePath(ctx, slot, placeholder); err != nil {
		return err
	}
	ec2RealMu.Lock()
	if slots := ec2RealEBSSlots[instanceID]; slots != nil {
		delete(slots, volumeID)
	}
	ec2RealMu.Unlock()
	return nil
}

func ec2RefreshRealVolume(ctx context.Context, vol EC2Volume) error {
	if len(vol.Attachments) == 0 {
		return nil
	}
	inst, ok := ec2Instances.Get(vol.Attachments[0].InstanceId)
	if !ok || inst.State != "running" {
		return nil
	}
	ec2RealMu.Lock()
	vm := ec2Fabric.VM(inst.InstanceId)
	slot := ""
	if slots := ec2RealEBSSlots[inst.InstanceId]; slots != nil {
		slot = slots[vol.VolumeId]
	}
	ec2RealMu.Unlock()
	if vm == nil || !vm.Alive() {
		return fmt.Errorf("instance %s is running without a live Firecracker VM", inst.InstanceId)
	}
	if slot == "" {
		return fmt.Errorf("volume %s is attached to %s without a Firecracker drive slot", vol.VolumeId, inst.InstanceId)
	}
	blockPath, err := ebsEnsureVolumeBlockImage(&vol)
	if err != nil {
		return err
	}
	return vm.PatchBlockDrivePath(ctx, slot, blockPath)
}

func ec2FirstFreeRealEBSSlot(slots map[string]string) string {
	used := map[string]bool{}
	for _, slot := range slots {
		used[slot] = true
	}
	for i := 1; i <= ec2RealEBSMaxSlots; i++ {
		slot := ec2RealEBSSlotID(i)
		if !used[slot] {
			return slot
		}
	}
	return ""
}

func ec2RealEBSSlotID(index int) string {
	return fmt.Sprintf("ebs%d", index)
}

func ec2RealEBSSlotPlaceholderPath(instanceID, slot string) string {
	return filepath.Join(ebsHostRoot(), "firecracker-slots", instanceID, slot+".raw")
}

func ec2PrepareRealEBSSlotPlaceholder(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return err
	}
	return f.Close()
}

// ec2ReapplyRealSecurityGroup reprograms the nftables ingress filter on every
// network path currently bound to groupID — ENIs attached to EC2 instances,
// Amazon ECS task NICs, and AWS Lambda Hyperplane ENIs in the real network
// namespace tier — so an Authorize/Revoke on a running workload takes effect
// immediately. Hosts without real-exec capabilities skip the call (the per-NIC
// apply is a no-op when no real NIC exists), and SG rules there remain
// metadata-only.
func ec2ReapplyRealSecurityGroup(ctx context.Context, groupID string) error {
	for _, eni := range ec2NetworkInterfaces.List() {
		for _, attachedGroupID := range eni.SecurityGroupIds {
			if attachedGroupID != groupID {
				continue
			}
			if err := ec2ApplyRealNICSecurityGroups(ctx, eni.NetworkInterfaceId, eni.SecurityGroupIds); err != nil {
				return err
			}
			break
		}
	}
	for _, task := range ecsTasks.List() {
		if !ecsTaskUsesSecurityGroup(task, groupID) {
			continue
		}
		var sgIDs []string
		if task.NetworkConfiguration != nil && task.NetworkConfiguration.AwsvpcConfiguration != nil {
			sgIDs = task.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups
		}
		if err := ec2ApplyRealECSTaskSecurityGroups(ctx, task.TaskID(), sgIDs); err != nil {
			return err
		}
	}
	ec2RealMu.RLock()
	lambdaAttachments := make(map[string]ec2RealLambdaNIC, len(ec2RealLambdaNICs))
	for invocationID, attachment := range ec2RealLambdaNICs {
		lambdaAttachments[invocationID] = attachment
	}
	ec2RealMu.RUnlock()
	for invocationID, attachment := range lambdaAttachments {
		if !stringInSlice(groupID, attachment.SecurityGroupIDs) {
			continue
		}
		if err := ec2ApplyRealLambdaSecurityGroups(ctx, invocationID); err != nil {
			return err
		}
	}
	return nil
}

func ec2CreateRealNATGateway(ctx context.Context, nat EC2NatGateway) error {
	if ec2Fabric.NIC(nat.NatGatewayId) != nil {
		return nil
	}
	sn, ok := ec2Subnets.Get(nat.SubnetId)
	if !ok {
		return fmt.Errorf("subnet %s not found", nat.SubnetId)
	}
	if _, err := ec2CreateRealSubnet(ctx, sn); err != nil {
		return err
	}
	if len(nat.NatGatewayAddresses) == 0 {
		return fmt.Errorf("NAT gateway %s has no address attachment", nat.NatGatewayId)
	}
	addr := nat.NatGatewayAddresses[0]
	_, err := ec2Fabric.AttachNamespaceNIC(ctx, nat.SubnetId, nat.NatGatewayId, realexec.NamespaceNICSpec{
		NamespaceName: fabric.LinuxName("an", nat.NatGatewayId),
		HostVethName:  fabric.LinuxName("nh", nat.NatGatewayId),
		GuestVethName: fabric.LinuxName("ng", nat.NatGatewayId),
		PrivateIP:     net.ParseIP(addr.PrivateIp),
		MAC:           fabric.DeriveMAC(ec2MACPrefix, addr.NetworkInterfaceId),
	})
	return err
}

// ec2DeleteRealNATGateway closes the gateway's interface and withdraws the
// translation of every route through it: AWS turns those routes into
// blackholes, and a blackhole translates nothing.
func ec2DeleteRealNATGateway(ctx context.Context, natID string) error {
	var errs []error
	for _, rt := range ec2RouteTables.List() {
		for _, route := range rt.Routes {
			if route.NatGatewayId == natID {
				errs = append(errs, ec2ReleaseRealNATRoute(ctx, rt.RouteTableId, route.DestinationCidrBlock))
			}
		}
	}
	errs = append(errs, ec2Fabric.DeleteNIC(ctx, natID))
	return errors.Join(errs...)
}

func ec2NATRouteOwner(routeTableID, destinationCIDR string) string {
	return routeTableID + "|" + destinationCIDR
}

// ec2ConfigureRealNATRoute translates the traffic of every subnet associated
// with the route table to the NAT gateway's public address.
func ec2ConfigureRealNATRoute(ctx context.Context, routeTableID, destinationCIDR, natID string) error {
	nat, ok := ec2NatGateways.Get(natID)
	if !ok {
		return fmt.Errorf("NAT gateway %s not found", natID)
	}
	if len(nat.NatGatewayAddresses) == 0 || nat.NatGatewayAddresses[0].PublicIp == "" {
		return fmt.Errorf("NAT gateway %s has no public IPv4 address", natID)
	}
	rt, ok := ec2RouteTables.Get(routeTableID)
	if !ok {
		return fmt.Errorf("route table %s not found", routeTableID)
	}
	if _, ok := ec2Vpcs.Get(rt.VpcId); !ok {
		return fmt.Errorf("VPC %s not found", rt.VpcId)
	}
	if _, err := ec2Fabric.EnsureNetwork(ctx, rt.VpcId); err != nil {
		return err
	}
	var sources []string
	for _, assoc := range rt.Associations {
		if subnet, ok := ec2Subnets.Get(assoc.SubnetId); ok {
			sources = append(sources, subnet.CidrBlock)
		}
	}
	if len(sources) == 0 {
		if subnet, ok := ec2Subnets.Get(nat.SubnetId); ok {
			sources = append(sources, subnet.CidrBlock)
		}
	}
	if len(sources) == 0 {
		return fmt.Errorf("route table %s has no subnet CIDR for NAT source", routeTableID)
	}
	return ec2Fabric.ConfigureSNAT(ctx, rt.VpcId, ec2NATRouteOwner(routeTableID, destinationCIDR), sources, net.ParseIP(nat.NatGatewayAddresses[0].PublicIp))
}

func ec2ReleaseRealNATRoute(ctx context.Context, routeTableID, destinationCIDR string) error {
	return ec2Fabric.ReleaseOwned(ctx, ec2NATRouteOwner(routeTableID, destinationCIDR))
}

func ec2ApplyRealVPCEgressPolicy(ctx context.Context, vpcID string) error {
	network := ec2Fabric.Network(vpcID)
	if network == nil {
		return nil
	}
	allowed, err := ec2AllowedRealEgressSources(vpcID)
	if err != nil {
		return err
	}
	realexec.MarkFrom(ctx)("egress:sources")
	return network.ConfigureEgressPolicy(ctx, allowed, fabric.LinuxName("eg", vpcID))
}

func ec2ApplyRealRouteTableEgressPolicy(ctx context.Context, routeTableID string) error {
	rt, ok := ec2RouteTables.Get(routeTableID)
	if !ok {
		return nil
	}
	return ec2ApplyRealVPCEgressPolicy(ctx, rt.VpcId)
}

func ec2AllowedRealEgressSources(vpcID string) ([]string, error) {
	allowed := map[string]bool{}
	var tasks []ECSTask
	if ecsTasks != nil {
		tasks = ecsTasks.List()
	}
	var instances []EC2Instance
	if ec2Instances != nil {
		instances = ec2Instances.List()
	}
	for _, subnet := range ec2Subnets.List() {
		if subnet.VpcId != vpcID {
			continue
		}
		rt, ok := ec2EffectiveRouteTableForSubnet(subnet.SubnetId, vpcID)
		if !ok || !ec2RouteTableHasDefaultExternalRoute(rt, vpcID) {
			continue
		}
		if ec2RouteTableHasDefaultNATRoute(rt, vpcID) {
			allowed[subnet.CidrBlock] = true
			continue
		}
		if ec2RouteTableHasDefaultIGWRoute(rt, vpcID) {
			for _, src := range ecsPublicEgressSourcesForSubnet(tasks, subnet.SubnetId) {
				allowed[src] = true
			}
			for _, src := range ec2PublicInstanceSourcesForSubnet(instances, subnet.SubnetId) {
				allowed[src] = true
			}
		}
	}
	var out []string
	for cidr := range allowed {
		out = append(out, cidr)
	}
	sort.Strings(out)
	return out, nil
}

func ec2EffectiveRouteTableForSubnet(subnetID, vpcID string) (EC2RouteTable, bool) {
	var main *EC2RouteTable
	for _, rt := range ec2RouteTables.List() {
		if rt.VpcId != vpcID {
			continue
		}
		for _, assoc := range rt.Associations {
			if assoc.SubnetId == subnetID {
				return rt, true
			}
			if assoc.Main {
				copy := rt
				main = &copy
			}
		}
	}
	if main != nil {
		return *main, true
	}
	return EC2RouteTable{}, false
}

func ec2RouteTableHasDefaultExternalRoute(rt EC2RouteTable, vpcID string) bool {
	return ec2RouteTableHasDefaultNATRoute(rt, vpcID) || ec2RouteTableHasDefaultIGWRoute(rt, vpcID)
}

func ec2RouteTableHasDefaultNATRoute(rt EC2RouteTable, vpcID string) bool {
	if ec2NatGateways == nil {
		return false
	}
	for _, route := range rt.Routes {
		if route.DestinationCidrBlock != "0.0.0.0/0" || route.NatGatewayId == "" || route.State != "active" {
			continue
		}
		if nat, ok := ec2NatGateways.Get(route.NatGatewayId); ok && nat.VpcId == vpcID && nat.State == "available" {
			return true
		}
	}
	return false
}

func ec2RouteTableHasDefaultIGWRoute(rt EC2RouteTable, vpcID string) bool {
	for _, route := range rt.Routes {
		if route.DestinationCidrBlock != "0.0.0.0/0" || route.GatewayId == "" || route.State != "active" {
			continue
		}
		if !strings.HasPrefix(route.GatewayId, "igw-") {
			continue
		}
		if ec2InternetGatewayAttachedToVPC(route.GatewayId, vpcID) {
			return true
		}
	}
	return false
}

func ec2InternetGatewayAttachedToVPC(igwID, vpcID string) bool {
	if ec2InternetGateways == nil {
		return false
	}
	igw, ok := ec2InternetGateways.Get(igwID)
	if !ok {
		return false
	}
	for _, att := range igw.Attachments {
		if att.VpcId == vpcID && att.State == "available" {
			return true
		}
	}
	return false
}

// ecsPublicEgressSourcesForSubnet returns the addresses of the tasks in subnetID
// that were given a public IP. A stopped task holds no address any more.
func ecsPublicEgressSourcesForSubnet(tasks []ECSTask, subnetID string) []string {
	var out []string
	for _, task := range tasks {
		if task.LastStatus == ECSTaskStatusStopped {
			continue
		}
		cfg := task.NetworkConfiguration
		if cfg == nil || cfg.AwsvpcConfiguration == nil || !strings.EqualFold(cfg.AwsvpcConfiguration.AssignPublicIp, "ENABLED") {
			continue
		}
		for _, subnet := range cfg.AwsvpcConfiguration.Subnets {
			if subnet != subnetID {
				continue
			}
			for _, att := range task.Attachments {
				if att.Type != "ElasticNetworkInterface" {
					continue
				}
				for _, detail := range att.Details {
					if detail.Name == "privateIPv4Address" && detail.Value != "" {
						out = append(out, detail.Value+"/32")
					}
				}
			}
		}
	}
	return out
}

func ec2PublicInstanceSourcesForSubnet(instances []EC2Instance, subnetID string) []string {
	var out []string
	for _, inst := range instances {
		if inst.SubnetId == subnetID && inst.PrivateIpAddress != "" && inst.PublicIpAddress != "" {
			out = append(out, inst.PrivateIpAddress+"/32")
		}
	}
	return out
}

// ec2ConfigureTaskResolver points the task namespace's DNS at the simulator's
// own resolver, reached at the link-local address AWS answers on from inside
// any VPC.
func ec2ConfigureTaskResolver(ctx context.Context, subnet *realexec.Subnet, vpcID, taskID string) error {
	port, err := route53DNSPort()
	if err != nil {
		return fmt.Errorf("configure task resolver for %s: %w", taskID, err)
	}
	if err := subnet.ConfigureResolverDNAT(ctx, port, fabric.LinuxName("dns", vpcID)); err != nil {
		return fmt.Errorf("configure task resolver for %s: %w", taskID, err)
	}
	return nil
}
