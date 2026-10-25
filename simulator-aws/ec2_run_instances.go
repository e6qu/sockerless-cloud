package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

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

// ec2InstanceLifecycleXML marks a Spot Instance and names its request; an
// On-Demand instance carries neither.
func ec2InstanceLifecycleXML(inst EC2Instance) string {
	if inst.InstanceLifecycle == "" {
		return ""
	}
	return "\n    <instanceLifecycle>" + inst.InstanceLifecycle + "</instanceLifecycle><spotInstanceRequestId>" + inst.SpotInstanceRequestId + "</spotInstanceRequestId>"
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
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return
	}
	image, ok := ec2LookupImage(imageID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id '[%s]' does not exist", imageID), http.StatusBadRequest)
		return
	}
	if _, err := ec2ImageRootFS(image); errors.Is(err, errEC2ImageNotBootable) {
		ec2ErrorXML(w, "InvalidAMIID.Unavailable", fmt.Sprintf("The image id '[%s]' is not available", imageID), http.StatusBadRequest)
		return
	}
	instanceType := r.FormValue("InstanceType")
	if instanceType == "" {
		instanceType = ltData.InstanceType
	}
	if instanceType == "" {
		instanceType = "t3.micro"
	}
	if facts, ok := ec2LookupInstanceType(instanceType); ok && !slices.Contains(facts.Architectures, image.Architecture) {
		ec2ErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("The architecture '%s' of the specified instance type does not match the architecture '%s' of the specified AMI. Specify an instance type and an AMI that have matching architectures, and try again. You can use 'describe-instance-types' or 'describe-images' to discover the architecture of the instance type or AMI.", facts.Architectures[0], image.Architecture),
			http.StatusBadRequest)
		return
	}
	// The AMI's instance type specification, when it has one, decides whether
	// this instance type may launch it.
	if !ec2ImageAllowsInstanceType(imageID, instanceType) {
		ec2ErrorXML(w, "InvalidParameterCombination",
			fmt.Sprintf("The instance type %q is not supported by the image %q", instanceType, imageID),
			http.StatusBadRequest)
		return
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
		ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID '%s' does not exist", subnetID), http.StatusBadRequest)
		return
	}
	// The instance is always modeled at the control plane — like
	// VPC/subnet/NAT; ec2LaunchInstance places its workload.
	placement := EC2Placement{
		Tenancy:         r.FormValue("Placement.Tenancy"),
		HostId:          r.FormValue("Placement.HostId"),
		Affinity:        r.FormValue("Placement.Affinity"),
		GroupName:       r.FormValue("Placement.GroupName"),
		GroupId:         r.FormValue("Placement.GroupId"),
		PartitionNumber: ec2AtoiOr(r.FormValue("Placement.PartitionNumber"), 0),
	}
	if lt := ltData.Placement; lt != nil {
		if placement.Tenancy == "" {
			placement.Tenancy = lt.Tenancy
		}
		if placement.GroupName == "" {
			placement.GroupName = lt.GroupName
		}
	}
	var host EC2Host
	if placement.HostId != "" {
		var found bool
		if host, found = ec2Hosts.Get(placement.HostId); !found {
			ec2ErrorXML(w, "InvalidHostID.NotFound", fmt.Sprintf("The host ID %q does not exist", placement.HostId), http.StatusBadRequest)
			return
		}
		if placement.Tenancy == "" {
			placement.Tenancy = "host"
		}
		// The capacity check and the placements it admits hold the lock
		// together, so concurrent launches cannot share the last slot.
		ec2HostPlacementMu.Lock()
		defer ec2HostPlacementMu.Unlock()
	}
	reservationID := ec2ID("r")
	sgIDs := runInstancesSecurityGroups(r)
	if len(sgIDs) == 0 {
		sgIDs = ec2DefaultSecurityGroupIDs(subnet.VpcId)
	}
	launchTime := time.Now().UTC().Format(time.RFC3339)
	tags := parseTags(r)

	// Instance knobs: request value, else launch-template value, else the AWS
	// default the API documents (metadata options). These are the fields
	// aws_instance reads back; dropping them forces drift every plan.
	shutdownBehavior := ec2FormOrLT(r, "InstanceInitiatedShutdownBehavior", ltData.InstanceInitiatedShutdownBehavior)
	if shutdownBehavior == "" {
		shutdownBehavior = "stop"
	}
	if !ec2ValidShutdownBehavior(w, shutdownBehavior) {
		return
	}
	var ltMetaTokens, ltMetaEndpoint, ltMetaHop, ltMetaTags, ltMetaIpv6 string
	if m := ltData.MetadataOptions; m != nil {
		ltMetaTokens, ltMetaEndpoint, ltMetaHop, ltMetaTags, ltMetaIpv6 = m.HttpTokens, m.HttpEndpoint, m.HttpPutResponseHopLimit, m.InstanceMetadataTags, m.HttpProtocolIpv6
	}
	metaTokens := ec2FormOrLT(r, "MetadataOptions.HttpTokens", ltMetaTokens)
	if metaTokens == "" {
		// An AMI registered with ImdsSupport v2.0 launches instances that
		// require IMDSv2.
		metaTokens = "optional"
		if img, ok := ec2LookupImage(imageID); ok && img.ImdsSupport == "v2.0" {
			metaTokens = "required"
		}
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
		Context:                  r.Context(),
		ReservationId:            reservationID,
		ImageId:                  imageID,
		InstanceType:             instanceType,
		Subnet:                   subnet,
		SubnetId:                 subnetID,
		SecurityGroupIds:         sgIDs,
		Tags:                     tags,
		LaunchTime:               launchTime,
		State:                    "pending",
		KeyName:                  ec2FormOrLT(r, "KeyName", ltData.KeyName),
		IamInstanceProfileArn:    ec2FormOrLT(r, "IamInstanceProfile.Arn", ltData.IamInstanceProfileArn),
		IamInstanceProfileName:   ec2FormOrLT(r, "IamInstanceProfile.Name", ltData.IamInstanceProfileName),
		EbsOptimized:             ec2BoolStr(ec2FormOrLT(r, "EbsOptimized", ltData.EbsOptimized)),
		Monitoring:               ec2BoolStr(ec2FormOrLT(r, "Monitoring.Enabled", ltData.MonitoringEnabled)),
		UserData:                 ec2FormOrLT(r, "UserData", ltData.UserData),
		DisableApiTermination:    ec2BoolStr(ec2FormOrLT(r, "DisableApiTermination", ltData.DisableApiTermination)),
		ShutdownBehavior:         shutdownBehavior,
		SourceDestCheck:          true, // AWS default; ModifyInstanceAttribute can disable it
		CpuCoreCount:             ec2AtoiOr(r.FormValue("CpuOptions.CoreCount"), 0),
		CpuThreadsPerCore:        ec2AtoiOr(r.FormValue("CpuOptions.ThreadsPerCore"), 0),
		RootVolumeSize:           rootSize,
		RootVolumeType:           rootType,
		MetadataHttpTokens:       metaTokens,
		MetadataHttpEndpoint:     metaEndpoint,
		MetadataHopLimit:         ec2AtoiOr(ec2FormOrLT(r, "MetadataOptions.HttpPutResponseHopLimit", ltMetaHop), 1),
		MetadataInstanceTags:     metaTags,
		MetadataHttpProtocolIpv6: ec2FormOrLT(r, "MetadataOptions.HttpProtocolIpv6", ltMetaIpv6),
		Placement:                placement,
	}

	var spotOptions *EC2LTInstanceMarketOptions
	if ec2FormOrLT(r, "InstanceMarketOptions.MarketType", ec2LTMarketType(ltData)) == "spot" {
		spotOptions = &EC2LTInstanceMarketOptions{MarketType: "spot"}
		if lt := ltData.InstanceMarketOptions; lt != nil {
			*spotOptions = *lt
		}
		spotOptions.MaxPrice = ec2FormOrLT(r, "InstanceMarketOptions.SpotOptions.MaxPrice", spotOptions.MaxPrice)
		spotOptions.SpotInstanceType = ec2FormOrLT(r, "InstanceMarketOptions.SpotOptions.SpotInstanceType", spotOptions.SpotInstanceType)
		spotOptions.InstanceInterruptionBehavior = ec2FormOrLT(r, "InstanceMarketOptions.SpotOptions.InstanceInterruptionBehavior", spotOptions.InstanceInterruptionBehavior)
	}

	var instances []EC2Instance
	for i := 0; i < maxCount; i++ {
		if placement.HostId != "" {
			if refusal := ec2HostRefusal(host, instanceType); refusal != "" {
				if i < minCount {
					ec2ErrorXML(w, "InsufficientHostCapacity", refusal, http.StatusBadRequest)
					return
				}
				break
			}
		}
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
					ec2ErrorXML(w, "InsufficientFreeAddressesInSubnet", err.Error(), http.StatusBadRequest)
					return
				}
				break
			}
			privateIP = ip
		}
		spec := base
		spec.PrivateIP = privateIP
		spec.AmiLaunchIndex = i
		inst, err := ec2CreateInstance(spec)
		if err != nil {
			if i < minCount {
				ec2ErrorXML(w, "InsufficientFreeAddressesInSubnet", fmt.Sprintf("failed to attach real EC2 network interface: %v", err), http.StatusServiceUnavailable)
				return
			}
			break
		}
		if spotOptions != nil {
			inst = ec2FulfilSpotLaunch(inst, *spotOptions, tags)
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
		instanceID := inst.InstanceId
		bg.Go(func() {
			if _, err := ec2LaunchInstance(instanceID, nil); err != nil {
				fmt.Fprintf(os.Stderr, "failed to boot real EC2 instance %s: %v\n", instanceID, err)
			}
		})
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
</RunInstancesResponse>`, ec2Xmlns(), sim.NewUUID(), reservationID, ec2Owner(), instanceItems.String())
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
	Context       context.Context
	ReservationId string
	// AmiLaunchIndex is the instance's position among those its request
	// launched.
	AmiLaunchIndex         int
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
	// ShutdownBehavior is InstanceInitiatedShutdownBehavior: "stop" or
	// "terminate".
	ShutdownBehavior         string
	MetadataHttpTokens       string
	MetadataHttpEndpoint     string
	MetadataHopLimit         int
	MetadataInstanceTags     string
	MetadataHttpProtocolIpv6 string
	Placement                EC2Placement
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
		InstanceId:               instanceID,
		ReservationId:            spec.ReservationId,
		AmiLaunchIndex:           spec.AmiLaunchIndex,
		ImageId:                  spec.ImageId,
		InstanceType:             spec.InstanceType,
		SubnetId:                 spec.SubnetId,
		VpcId:                    spec.Subnet.VpcId,
		State:                    spec.State,
		PrivateIpAddress:         spec.PrivateIP,
		SecurityGroupIds:         spec.SecurityGroupIds,
		Tags:                     spec.Tags,
		LaunchTime:               spec.LaunchTime,
		KeyName:                  spec.KeyName,
		Architecture:             ec2ImageArchitecture(spec.ImageId),
		RootDeviceName:           rootDevice,
		NetworkInterfaceId:       eniID,
		IamInstanceProfileArn:    spec.IamInstanceProfileArn,
		IamInstanceProfileName:   spec.IamInstanceProfileName,
		EbsOptimized:             spec.EbsOptimized,
		Monitoring:               spec.Monitoring,
		UserData:                 spec.UserData,
		DisableApiTermination:    spec.DisableApiTermination,
		ShutdownBehavior:         spec.ShutdownBehavior,
		SourceDestCheck:          spec.SourceDestCheck,
		CpuCoreCount:             spec.CpuCoreCount,
		CpuThreadsPerCore:        spec.CpuThreadsPerCore,
		RootVolumeSize:           rootVolSize,
		RootVolumeType:           rootVolType,
		MetadataHttpTokens:       spec.MetadataHttpTokens,
		MetadataHttpEndpoint:     spec.MetadataHttpEndpoint,
		MetadataHopLimit:         spec.MetadataHopLimit,
		MetadataInstanceTags:     spec.MetadataInstanceTags,
		MetadataHttpProtocolIpv6: spec.MetadataHttpProtocolIpv6,
		Placement:                spec.Placement,
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
	}
	rootVolume.HostPath = EBSVolumeHostDir(rootVolumeID)
	ec2Volumes.Put(rootVolumeID, rootVolume)
	return inst, nil
}
