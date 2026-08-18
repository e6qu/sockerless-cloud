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
)

var (
	ec2RealHost = realexec.NewHost()
	// ec2RealMu guards the real-execution fabric maps. Reads — resolving a
	// security group's member addresses, listing the attachments to reapply —
	// exclude nothing but a writer. Anything that creates, mutates or tears
	// down fabric keeps taking Lock; neither is reentrant, and no section here
	// calls another.
	ec2RealMu         sync.RWMutex
	ec2RealVPCs       = map[string]*realexec.Network{}
	ec2RealSubnets    = map[string]*realexec.Subnet{}
	ec2RealNICs       = map[string]*realexec.NamespaceNIC{}
	ec2RealVMNICs     = map[string]*realexec.TapNIC{}
	ec2RealVMs        = map[string]*realexec.FirecrackerVM{}
	ec2RealEBSSlots   = map[string]map[string]string{}
	ec2RealNATNICs    = map[string]*realexec.NamespaceNIC{}
	ec2RealECSNICs    = map[string]*realexec.NamespaceNIC{} // taskID -> veth into the container netns
	ec2RealLambdaNICs = map[string]ec2RealLambdaNIC{}       // invocation ID -> Hyperplane ENI realization

	// ec2RealVMStartLocks serializes ec2StartRealVM per instance id so two
	// concurrent starts of the same instance can't both pass the not-running
	// check and both AttachTapNIC the same tap (the second failing "File
	// exists" / "IP already leased").
	ec2RealVMStartLocks sync.Map // instanceID -> *sync.Mutex

	// ec2RealVPCLocks gates VPC teardown against in-flight attaches: an attach
	// (RLock) provisions netns resources and only records the NIC at the end, so
	// a concurrent ec2DeleteRealVPC (Lock) must wait for in-flight attaches —
	// otherwise it closes the netns mid-attach, orphaning the veth/tap and
	// leaking the NIC map entry. RLock allows parallel attaches in the same VPC.
	ec2RealVPCLocks sync.Map // vpcID -> *sync.RWMutex
)

type ec2RealLambdaNIC struct {
	NIC              *realexec.NamespaceNIC
	SubnetID         string
	PrivateIP        string
	SecurityGroupIDs []string
	RuntimeDNATTable string
}

func ec2RealVMStartLock(instanceID string) *sync.Mutex {
	m, _ := ec2RealVMStartLocks.LoadOrStore(instanceID, &sync.Mutex{})
	mu, ok := m.(*sync.Mutex)
	if !ok {
		mu = &sync.Mutex{}
		ec2RealVMStartLocks.Store(instanceID, mu)
	}
	return mu
}

func ec2RealVPCLock(vpcID string) *sync.RWMutex {
	m, _ := ec2RealVPCLocks.LoadOrStore(vpcID, &sync.RWMutex{})
	mu, ok := m.(*sync.RWMutex)
	if !ok {
		mu = &sync.RWMutex{}
		ec2RealVPCLocks.Store(vpcID, mu)
	}
	return mu
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
func ec2AttachRealECSTaskNIC(ctx context.Context, taskID, subnetID string, pid int, eniIP string, securityGroupIDs []string) error {
	sn, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return fmt.Errorf("subnet %s not found", subnetID)
	}
	// Hold the per-VPC read lock for the whole attach so a concurrent VPC
	// teardown waits for us instead of closing the netns mid-attach.
	vpcLk := ec2RealVPCLock(sn.VpcId)
	vpcLk.RLock()
	defer vpcLk.RUnlock()
	if err := ec2CreateRealSubnet(ctx, sn); err != nil {
		return err
	}
	ec2RealMu.Lock()
	subnet := ec2RealSubnets[subnetID]
	ec2RealMu.Unlock()
	if subnet == nil {
		return fmt.Errorf("real subnet %s not provisioned", subnetID)
	}
	nic, err := subnet.AttachExternalNamespaceNIC(ctx, realexec.ExternalNamespaceNICSpec{
		PID:           pid,
		HostVethName:  ec2RealName("eh", taskID),
		GuestVethName: ec2RealName("eg", taskID),
		GuestIfName:   "eth0",
		MAC:           ec2ENIMAC(taskID),
		PrivateIP:     net.ParseIP(eniIP),
	})
	if err != nil {
		return err
	}
	metadataPort, err := simHostMetadataPort()
	if err != nil {
		_ = nic.Close(context.Background())
		return err
	}
	if err := subnet.ConfigureAddressDNAT(ctx, realexec.ECSTaskMetadataIPv4, metadataPort, ec2RealName("emd", sn.VpcId)); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure ECS task metadata routing for %s: %w", taskID, err)
	}
	if err := subnet.ConfigureMetadataDNAT(ctx, metadataPort, ec2RealName("imd", sn.VpcId)); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure ECS IMDS routing for %s: %w", taskID, err)
	}
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
	if err := ec2ApplyRealVPCEgressPolicy(ctx, sn.VpcId); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure VPC egress policy for %s: %w", taskID, err)
	}
	ec2RealMu.Lock()
	ec2RealECSNICs[taskID] = nic
	ec2RealMu.Unlock()
	if err := ec2ApplyRealECSTaskSecurityGroups(ctx, taskID, securityGroupIDs); err != nil {
		return fmt.Errorf("apply security groups for %s: %w", taskID, err)
	}
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
	vpcLk := ec2RealVPCLock(sn.VpcId)
	vpcLk.RLock()
	defer vpcLk.RUnlock()
	if err := ec2CreateRealSubnet(ctx, sn); err != nil {
		return err
	}
	ec2RealMu.Lock()
	subnet := ec2RealSubnets[subnetID]
	ec2RealMu.Unlock()
	if subnet == nil {
		return fmt.Errorf("real subnet %s not provisioned", subnetID)
	}
	nic, err := subnet.AttachExternalNamespaceNIC(ctx, realexec.ExternalNamespaceNICSpec{
		PID:           pid,
		HostVethName:  ec2RealName("lh", invocationID),
		GuestVethName: ec2RealName("lg", invocationID),
		GuestIfName:   "eth0",
		MAC:           ec2ENIMAC(invocationID),
		PrivateIP:     net.ParseIP(eniIP),
	})
	if err != nil {
		return err
	}
	metadataPort, err := simHostMetadataPort()
	if err != nil {
		_ = nic.Close(context.Background())
		return err
	}
	if err := subnet.ConfigureMetadataDNAT(ctx, metadataPort, ec2RealName("imd", sn.VpcId)); err != nil {
		_ = nic.Close(context.Background())
		return fmt.Errorf("configure AWS Lambda instance metadata routing for %s: %w", invocationID, err)
	}
	runtimeTable := ec2RealName("lrd", invocationID)
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
	subnet := ec2RealSubnets[attachment.SubnetID]
	ec2RealMu.Unlock()
	if !ok {
		return
	}
	if subnet != nil {
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

func ec2RealName(prefix, id string) string {
	id = strings.NewReplacer("/", "", "-", "", "_", "", ".", "").Replace(id)
	if len(id) > 10 {
		id = id[len(id)-10:]
	}
	name := prefix + id
	if len(name) > 15 {
		return name[:15]
	}
	return name
}

func ec2CreateRealVPC(ctx context.Context, vpc EC2Vpc) error {
	ec2RealMu.Lock()
	defer ec2RealMu.Unlock()
	if _, ok := ec2RealVPCs[vpc.VpcId]; ok {
		return nil
	}

	network, err := ec2RealHost.CreateNetworkNamespace(ctx, ec2RealName("avn", vpc.VpcId))
	if err != nil {
		return err
	}
	ec2RealVPCs[vpc.VpcId] = network
	return nil
}

func ec2DeleteRealVPC(ctx context.Context, vpcID string) error {
	// Wait for in-flight attaches/starts in this VPC before tearing down the
	// netns, so we don't orphan a half-attached veth/tap.
	vpcLk := ec2RealVPCLock(vpcID)
	vpcLk.Lock()
	defer vpcLk.Unlock()
	ec2RealMu.Lock()
	network := ec2RealVPCs[vpcID]
	delete(ec2RealVPCs, vpcID)
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
	for natID, nic := range ec2RealNATNICs {
		if nat, ok := ec2NatGateways.Get(natID); ok && nat.VpcId == vpcID {
			delete(ec2RealNATNICs, natID)
			_ = nic.Close(ctx)
		}
	}
	for eniID, nic := range ec2RealNICs {
		if eni, ok := ec2NetworkInterfaces.Get(eniID); ok && eni.VpcId == vpcID {
			delete(ec2RealNICs, eniID)
			_ = nic.Close(ctx)
		}
	}
	for eniID, nic := range ec2RealVMNICs {
		if eni, ok := ec2NetworkInterfaces.Get(eniID); ok && eni.VpcId == vpcID {
			delete(ec2RealVMNICs, eniID)
			imdsInstancesByIP.Delete(nic.PrivateIP.String())
			_ = nic.Close(ctx)
		}
	}
	for instanceID, vm := range ec2RealVMs {
		if inst, ok := ec2Instances.Get(instanceID); ok && inst.VpcId == vpcID {
			delete(ec2RealVMs, instanceID)
			delete(ec2RealEBSSlots, instanceID)
			_ = vm.Stop(ctx)
		}
	}
	for subnetID, subnet := range ec2RealSubnets {
		if subnetForID, ok := ec2Subnets.Get(subnetID); ok && subnetForID.VpcId == vpcID {
			delete(ec2RealSubnets, subnetID)
			_ = subnet.Close(ctx)
		}
	}
	ec2RealMu.Unlock()
	if network == nil {
		return nil
	}
	return network.Close(ctx)
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

func ec2CreateRealSubnet(ctx context.Context, subnet EC2Subnet) error {
	ec2RealMu.Lock()
	if _, ok := ec2RealSubnets[subnet.SubnetId]; ok {
		ec2RealMu.Unlock()
		return nil
	}
	network := ec2RealVPCs[subnet.VpcId]
	ec2RealMu.Unlock()
	if network == nil {
		vpc, ok := ec2Vpcs.Get(subnet.VpcId)
		if !ok {
			return fmt.Errorf("VPC %s not found", subnet.VpcId)
		}
		if err := ec2CreateRealVPC(ctx, vpc); err != nil {
			return err
		}
	}
	ec2RealMu.Lock()
	defer ec2RealMu.Unlock()
	if _, ok := ec2RealSubnets[subnet.SubnetId]; ok {
		return nil
	}
	network = ec2RealVPCs[subnet.VpcId]
	if network == nil {
		return fmt.Errorf("real VPC %s not provisioned", subnet.VpcId)
	}
	realSubnet, err := network.CreateSubnet(ctx, realexec.SubnetSpec{
		Name:       subnet.SubnetId,
		BridgeName: ec2RealName("asb", subnet.SubnetId),
		CIDR:       subnet.CidrBlock,
		Gateway:    ec2AWSSubnetGateway(subnet.CidrBlock),
	})
	if err != nil {
		return err
	}
	ec2RealSubnets[subnet.SubnetId] = realSubnet
	return nil
}

func ec2DeleteRealSubnet(ctx context.Context, subnetID string) error {
	ec2RealMu.Lock()
	subnet := ec2RealSubnets[subnetID]
	delete(ec2RealSubnets, subnetID)
	ec2RealMu.Unlock()
	if subnet == nil {
		return nil
	}
	return subnet.Close(ctx)
}

func ec2DeleteRealNIC(ctx context.Context, eniID string) error {
	instanceIDForENI := ""
	for _, inst := range ec2Instances.List() {
		if inst.NetworkInterfaceId == eniID {
			instanceIDForENI = inst.InstanceId
			break
		}
	}
	ec2RealMu.Lock()
	nic := ec2RealNICs[eniID]
	delete(ec2RealNICs, eniID)
	tap := ec2RealVMNICs[eniID]
	delete(ec2RealVMNICs, eniID)
	var vm *realexec.FirecrackerVM
	if instanceIDForENI != "" {
		vm = ec2RealVMs[instanceIDForENI]
		delete(ec2RealVMs, instanceIDForENI)
		delete(ec2RealEBSSlots, instanceIDForENI)
	}
	ec2RealMu.Unlock()
	var errs []error
	if vm != nil {
		errs = append(errs, vm.Stop(ctx))
	}
	if nic == nil {
		if tap != nil {
			imdsInstancesByIP.Delete(tap.PrivateIP.String())
			errs = append(errs, tap.Close(ctx))
		}
		return errors.Join(errs...)
	}
	errs = append(errs, nic.Close(ctx))
	if tap != nil {
		imdsInstancesByIP.Delete(tap.PrivateIP.String())
		errs = append(errs, tap.Close(ctx))
	}
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
				for _, src := range ec2SGMemberCIDRs(gp.GroupId) {
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

func ec2ApplyRealNICSecurityGroups(ctx context.Context, eniID string, securityGroupIDs []string) error {
	ec2RealMu.Lock()
	nic := ec2RealNICs[eniID]
	tap := ec2RealVMNICs[eniID]
	ec2RealMu.Unlock()
	if nic == nil && tap == nil {
		return nil
	}
	// No security groups means default-allow (no host-level ingress filter).
	// This matches the pre-enforcement behaviour and avoids breaking tasks
	// launched without an explicit SG, which AWS would assign to the VPC's
	// default SG but the simulator does not model yet.
	if len(securityGroupIDs) == 0 {
		if nic != nil {
			if err := nic.ClearIngressFilter(ctx); err != nil {
				return err
			}
		}
		if tap != nil {
			if err := tap.ClearIngressFilter(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	rules := ec2BuildIngressPacketRules(securityGroupIDs)
	if nic != nil {
		if err := nic.ConfigureIngressFilter(ctx, rules); err != nil {
			return err
		}
	}
	if tap != nil {
		if err := tap.ConfigureIngressFilter(ctx, rules); err != nil {
			return err
		}
	}
	return nil
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
	return nic.ConfigureIngressFilter(ctx, ec2BuildIngressPacketRules(securityGroupIDs))
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
	// Hold the per-VPC read lock so a concurrent teardown waits for this start.
	vpcLk := ec2RealVPCLock(inst.VpcId)
	vpcLk.RLock()
	defer vpcLk.RUnlock()
	// Serialize concurrent starts of the same instance so the tap-NIC
	// check-then-create below can't double-attach.
	startLk := ec2RealVMStartLock(inst.InstanceId)
	startLk.Lock()
	defer startLk.Unlock()
	ec2RealMu.Lock()
	if vm := ec2RealVMs[inst.InstanceId]; vm != nil && vm.Alive() {
		ec2RealMu.Unlock()
		return nil
	}
	tap := ec2RealVMNICs[inst.NetworkInterfaceId]
	subnet := ec2RealSubnets[inst.SubnetId]
	ec2RealMu.Unlock()
	if subnet == nil {
		sn, ok := ec2Subnets.Get(inst.SubnetId)
		if !ok {
			return fmt.Errorf("subnet %s not found", inst.SubnetId)
		}
		if err := ec2CreateRealSubnet(ctx, sn); err != nil {
			return err
		}
		ec2RealMu.Lock()
		subnet = ec2RealSubnets[inst.SubnetId]
		ec2RealMu.Unlock()
	}
	if subnet == nil {
		// A concurrent VPC/subnet teardown removed it between the re-read above.
		return fmt.Errorf("subnet %s no longer exists", inst.SubnetId)
	}
	if tap == nil {
		created, err := subnet.AttachTapNIC(ctx, realexec.TapNICSpec{
			TapName:   ec2RealName("at", inst.NetworkInterfaceId),
			PrivateIP: net.ParseIP(inst.PrivateIpAddress),
			MAC:       ec2ENIMAC(inst.NetworkInterfaceId),
		})
		if err != nil {
			return err
		}
		tap = created
		ec2RealMu.Lock()
		ec2RealVMNICs[inst.NetworkInterfaceId] = tap
		ec2RealMu.Unlock()
	}
	imdsInstancesByIP.Store(tap.PrivateIP.String(), inst)
	metadataPort, err := simHostMetadataPort()
	if err != nil {
		return err
	}
	if err := subnet.ConfigureMetadataDNAT(ctx, metadataPort, ec2RealName("amd", inst.VpcId)); err != nil {
		return fmt.Errorf("configure EC2 IMDS routing for %s: %w", inst.InstanceId, err)
	}
	blockDrives, slots, err := ec2RealEBSBlockDrives(inst)
	if err != nil {
		return err
	}
	vm, err := realexec.StartFirecrackerVM(ctx, realexec.FirecrackerVMConfig{
		ID:          "aws-" + inst.InstanceId,
		Tap:         tap,
		MAC:         ec2ENIMAC(inst.NetworkInterfaceId),
		VCPUCount:   1,
		MemoryMiB:   512,
		BlockDrives: blockDrives,
	})
	if err != nil {
		return err
	}
	ec2RealMu.Lock()
	if old := ec2RealVMs[inst.InstanceId]; old != nil {
		_ = old.Stop(context.Background())
	}
	ec2RealVMs[inst.InstanceId] = vm
	ec2RealEBSSlots[inst.InstanceId] = slots
	ec2RealMu.Unlock()
	return ec2ApplyRealNICSecurityGroups(ctx, inst.NetworkInterfaceId, inst.SecurityGroupIds)
}

func ec2StopRealVM(ctx context.Context, instanceID string) error {
	ec2RealMu.Lock()
	vm := ec2RealVMs[instanceID]
	delete(ec2RealVMs, instanceID)
	delete(ec2RealEBSSlots, instanceID)
	ec2RealMu.Unlock()
	if vm == nil {
		return nil
	}
	return vm.Stop(ctx)
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
	vm := ec2RealVMs[instanceID]
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
	vm := ec2RealVMs[instanceID]
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
	vm := ec2RealVMs[inst.InstanceId]
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

func ec2RealVMAlive(instanceID string) bool {
	ec2RealMu.Lock()
	vm := ec2RealVMs[instanceID]
	ec2RealMu.Unlock()
	return vm != nil && vm.Alive()
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
	ec2RealMu.Lock()
	if _, ok := ec2RealNATNICs[nat.NatGatewayId]; ok {
		ec2RealMu.Unlock()
		return nil
	}
	subnet := ec2RealSubnets[nat.SubnetId]
	ec2RealMu.Unlock()
	if subnet == nil {
		sn, ok := ec2Subnets.Get(nat.SubnetId)
		if !ok {
			return fmt.Errorf("subnet %s not found", nat.SubnetId)
		}
		if err := ec2CreateRealSubnet(ctx, sn); err != nil {
			return err
		}
		ec2RealMu.Lock()
		subnet = ec2RealSubnets[nat.SubnetId]
		ec2RealMu.Unlock()
	}
	if len(nat.NatGatewayAddresses) == 0 {
		return fmt.Errorf("NAT gateway %s has no address attachment", nat.NatGatewayId)
	}
	addr := nat.NatGatewayAddresses[0]
	nic, err := subnet.AttachNamespaceNIC(ctx, realexec.NamespaceNICSpec{
		NamespaceName: ec2RealName("an", nat.NatGatewayId),
		HostVethName:  ec2RealName("nh", nat.NatGatewayId),
		GuestVethName: ec2RealName("ng", nat.NatGatewayId),
		PrivateIP:     net.ParseIP(addr.PrivateIp),
		MAC:           ec2ENIMAC(addr.NetworkInterfaceId),
	})
	if err != nil {
		return err
	}
	ec2RealMu.Lock()
	ec2RealNATNICs[nat.NatGatewayId] = nic
	ec2RealMu.Unlock()
	return nil
}

func ec2DeleteRealNATGateway(ctx context.Context, natID string) error {
	ec2RealMu.Lock()
	nic := ec2RealNATNICs[natID]
	delete(ec2RealNATNICs, natID)
	ec2RealMu.Unlock()
	if nic == nil {
		return nil
	}
	return nic.Close(ctx)
}

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
	var network *realexec.Network
	ec2RealMu.Lock()
	network = ec2RealVPCs[rt.VpcId]
	ec2RealMu.Unlock()
	if network == nil {
		vpc, ok := ec2Vpcs.Get(rt.VpcId)
		if !ok {
			return fmt.Errorf("VPC %s not found", rt.VpcId)
		}
		if err := ec2CreateRealVPC(ctx, vpc); err != nil {
			return err
		}
		ec2RealMu.Lock()
		network = ec2RealVPCs[rt.VpcId]
		ec2RealMu.Unlock()
	}
	sourceCIDR := ""
	for _, assoc := range rt.Associations {
		if subnet, ok := ec2Subnets.Get(assoc.SubnetId); ok {
			sourceCIDR = subnet.CidrBlock
			break
		}
	}
	if sourceCIDR == "" {
		if subnet, ok := ec2Subnets.Get(nat.SubnetId); ok {
			sourceCIDR = subnet.CidrBlock
		}
	}
	if sourceCIDR == "" {
		return fmt.Errorf("route table %s has no subnet CIDR for NAT source", routeTableID)
	}
	return network.ConfigureSNAT(ctx, sourceCIDR, net.ParseIP(nat.NatGatewayAddresses[0].PublicIp), ec2RealName("sn", routeTableID+destinationCIDR))
}

func ec2ApplyRealVPCEgressPolicy(ctx context.Context, vpcID string) error {
	ec2RealMu.Lock()
	network := ec2RealVPCs[vpcID]
	ec2RealMu.Unlock()
	if network == nil {
		return nil
	}
	allowed, err := ec2AllowedRealEgressSources(vpcID)
	if err != nil {
		return err
	}
	return network.ConfigureEgressPolicy(ctx, allowed, ec2RealName("eg", vpcID))
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
			for _, src := range ecsPublicEgressSourcesForSubnet(subnet.SubnetId) {
				allowed[src] = true
			}
			for _, src := range ec2PublicInstanceSourcesForSubnet(subnet.SubnetId) {
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

func ecsPublicEgressSourcesForSubnet(subnetID string) []string {
	if ecsTasks == nil {
		return nil
	}
	var out []string
	for _, task := range ecsTasks.List() {
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

func ec2PublicInstanceSourcesForSubnet(subnetID string) []string {
	if ec2Instances == nil {
		return nil
	}
	var out []string
	for _, inst := range ec2Instances.List() {
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
	if err := subnet.ConfigureResolverDNAT(ctx, port, ec2RealName("dns", vpcID)); err != nil {
		return fmt.Errorf("configure task resolver for %s: %w", taskID, err)
	}
	return nil
}

func ec2AWSSubnetGateway(cidr string) net.IP {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil || ip.To4() == nil {
		return nil
	}
	out := append(net.IP(nil), ip.To4()...)
	out[3]++
	return out
}

func ec2ENIMAC(id string) string {
	id = strings.NewReplacer("-", "", "_", "").Replace(id)
	var b [3]byte
	for i := range id {
		b[i%3] ^= id[i]
	}
	return fmt.Sprintf("02:0a:ec:%02x:%02x:%02x", b[0], b[1], b[2])
}
