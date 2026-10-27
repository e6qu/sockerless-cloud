package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

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
	if _, attached := ec2NetworkInterfaces.Get(inst.NetworkInterfaceId); attached && inst.NetworkInterfaceId != "" {
		fmt.Fprintf(&ni, `<networkInterfaceSet><item>
      <networkInterfaceId>%s</networkInterfaceId>
      <subnetId>%s</subnetId>
      <vpcId>%s</vpcId>
      <description/>
      <ownerId>%s</ownerId>
      <status>in-use</status>
      <macAddress>%s</macAddress>
      <privateIpAddress>%s</privateIpAddress>
      <privateDnsName>%s</privateDnsName>
      <sourceDestCheck>true</sourceDestCheck>
      <groupSet>%s</groupSet>
      <attachment><attachmentId>eni-attach-%s</attachmentId><deviceIndex>0</deviceIndex><status>attached</status><attachTime>%s</attachTime><deleteOnTermination>true</deleteOnTermination></attachment>
    </item></networkInterfaceSet>`,
			inst.NetworkInterfaceId, inst.SubnetId, inst.VpcId, ec2Owner(), ec2InterfaceMAC(inst.NetworkInterfaceId), inst.PrivateIpAddress,
			ec2PrivateDNSName(inst.PrivateIpAddress), groups.String(), inst.NetworkInterfaceId, inst.LaunchTime)
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
	metaXML := fmt.Sprintf("<metadataOptions><state>applied</state><httpTokens>%s</httpTokens><httpPutResponseHopLimit>%d</httpPutResponseHopLimit><httpEndpoint>%s</httpEndpoint><httpProtocolIpv6>%s</httpProtocolIpv6><instanceMetadataTags>%s</instanceMetadataTags></metadataOptions>",
		metaTokens, metaHop, metaEndpoint, ec2MetadataHttpProtocolIpv6(inst), metaTags)
	publicXML := ""
	if inst.PublicIpAddress != "" {
		publicXML = fmt.Sprintf("<ipAddress>%s</ipAddress>", inst.PublicIpAddress)
	}
	dnsNameXML := "<dnsName/>"
	if name := ec2PublicDNSName(inst); name != "" {
		dnsNameXML = "<dnsName>" + name + "</dnsName>"
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
    <privateDnsName>%s</privateDnsName>
    %s
    %s
    %s
    <amiLaunchIndex>%d</amiLaunchIndex>
    <productCodes/>
    <instanceType>%s</instanceType>
    <launchTime>%s</launchTime>
    %s
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
    <clientToken/>%s
    <ebsOptimized>%t</ebsOptimized>
    %s%s%s
    %s
    %s
  </item>`,
		inst.InstanceId, inst.ImageId, instanceStateCode(inst.State), inst.State,
		ec2PrivateDNSName(inst.PrivateIpAddress), dnsNameXML, reasonXML, keyNameXML, inst.AmiLaunchIndex, inst.InstanceType, inst.LaunchTime,
		ec2PlacementXML(inst), monitoringState, inst.SubnetId, inst.VpcId, inst.PrivateIpAddress, publicXML, inst.SourceDestCheck, groups.String(), stateReasonXML,
		inst.Architecture, inst.RootDeviceName, inst.RootDeviceName, "vol-"+strings.TrimPrefix(inst.InstanceId, "i-"), inst.LaunchTime,
		ec2InstanceLifecycleXML(inst), inst.EbsOptimized, iamXML, metaXML, cpuXML,
		writeTagSetXML(inst.Tags), ni.String())
}

func handleDescribeInstances(w http.ResponseWriter, r *http.Request) {
	instanceIDs := ec2ParamList(r, "InstanceId")
	var instances []EC2Instance
	if len(instanceIDs) > 0 {
		for _, id := range instanceIDs {
			inst, ok := ec2Instances.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", id), http.StatusBadRequest)
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
	// Paginate only the list form, not explicit InstanceIds; sort by id for a
	// stable cursor.
	nextToken := ""
	if len(instanceIDs) == 0 {
		paged, pageNext, pageOK := awsKeyPage(w, ec2BadToken, instances, func(it EC2Instance) string { return it.InstanceId }, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0), 0)
		if !pageOK {
			return
		}
		instances, nextToken = paged, pageNext
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
</DescribeInstancesResponse>`, ec2Xmlns(), sim.NewUUID(), reservations.String(), nextTokenXML)
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

func handleTerminateInstances(w http.ResponseWriter, r *http.Request) {
	writeInstanceStateChange(w, r, "terminated", true)
}

func handleStopInstances(w http.ResponseWriter, r *http.Request) {
	writeInstanceStateChange(w, r, "stopped", false)
}

func handleStartInstances(w http.ResponseWriter, r *http.Request) {
	writeInstanceStateChange(w, r, "running", false)
}

// A boot in progress is cancelled. An AWS Budgets SSM action halts instances
// too, so the semantics live here once.
func ec2HaltInstance(ctx context.Context, id, next string) error {
	inst, ok := ec2Instances.Get(id)
	if !ok {
		return fmt.Errorf("the instance ID %q does not exist", id)
	}
	ec2CancelBoot(id)
	inst.State = ec2HaltTransition(next)
	inst.StateTransitionReason = "User initiated (" + time.Now().UTC().Format("2006-01-02 15:04:05") + " GMT)"
	inst.StateReasonCode = "Client.UserInitiatedShutdown"
	inst.StateReasonMessage = "Client.UserInitiatedShutdown: User initiated shutdown"
	ec2Instances.Put(id, inst)
	if next == "stopped" {
		ec2UpdateVolumeAttachmentsForInstance(id, "attached", "in-use")
	}
	ec2FinishHalt(id, next)
	return nil
}

// stopping ahead of stopped, shutting-down ahead of terminated.
func ec2HaltTransition(next string) string {
	if next == "terminated" {
		return "shutting-down"
	}
	return "stopping"
}

func writeInstanceStateChange(w http.ResponseWriter, r *http.Request, next string, deleteENI bool) {
	instanceIDs := ec2ParamList(r, "InstanceId")
	var items strings.Builder
	for _, id := range instanceIDs {
		inst, ok := ec2Instances.Get(id)
		if !ok {
			ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
		prev := inst.State
		var current string
		switch {
		case next == "terminated" && (prev == "terminated" || prev == "shutting-down"),
			next == "stopped" && (prev == "stopped" || prev == "stopping"),
			next == "running" && (prev == "running" || prev == "pending"):
			// Already in the target state.
			current = prev
		case next == "stopped" && (prev == "terminated" || prev == "shutting-down"),
			next == "running" && prev != "stopped":
			ec2ErrorXML(w, "IncorrectInstanceState", fmt.Sprintf("The instance '%s' is not in a state from which it can be %s.", id, map[string]string{"stopped": "stopped", "running": "started"}[next]), http.StatusBadRequest)
			return
		case next == "stopped" || next == "terminated":
			if err := ec2HaltInstance(r.Context(), id, next); err != nil {
				ec2ErrorXML(w, "IncorrectInstanceState", fmt.Sprintf("failed to stop real EC2 instance: %v", err), http.StatusServiceUnavailable)
				return
			}
			inst, _ = ec2Instances.Get(id)
			current = inst.State
		default:
			// pending until the init is ready, then running.
			inst.State = "pending"
			inst.StateTransitionReason = ""
			inst.StateReasonCode = ""
			inst.StateReasonMessage = ""
			ec2Instances.Put(id, inst)
			ec2UpdateVolumeAttachmentsForInstance(id, "attached", "in-use")
			ec2StartInstance(id)
			current = inst.State
		}
		if deleteENI && inst.NetworkInterfaceId != "" {
			ec2NetworkInterfaces.Delete(inst.NetworkInterfaceId)
			ec2DeleteOnTerminationVolumes(id)
		}
		fmt.Fprintf(&items, `<item><instanceId>%s</instanceId><currentState><code>%d</code><name>%s</name></currentState><previousState><code>%d</code><name>%s</name></previousState></item>`,
			id, instanceStateCode(current), current, instanceStateCode(prev), prev)
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
</%sResponse>`, action, ec2Xmlns(), sim.NewUUID(), items.String(), action)
}

// Decide attachment inside Update, under the write lock: a listed copy could
// write back a volume detached meanwhile, and the following DeleteVolume would
// fail VolumeInUse.
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

// Read attachments inside Update for the same reason. Remove the backing
// store outside the lock; the row is gone either way.
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
</DescribeInstanceStatusResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

func handleDescribeInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	inst, ok := ec2Instances.Get(instanceID)
	if !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", instanceID), http.StatusBadRequest)
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
		body = fmt.Sprintf("<instanceInitiatedShutdownBehavior><value>%s</value></instanceInitiatedShutdownBehavior>", ec2ShutdownBehavior(inst))
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
</DescribeInstanceAttributeResponse>`, ec2Xmlns(), sim.NewUUID(), instanceID, body)
}

func ec2ValidShutdownBehavior(w http.ResponseWriter, value string) bool {
	if value == "stop" || value == "terminate" {
		return true
	}
	ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter instanceInitiatedShutdownBehavior is invalid. Valid values are 'stop' and 'terminate'.", value), http.StatusBadRequest)
	return false
}

// A record without one has Amazon EC2's default.
func ec2ShutdownBehavior(inst EC2Instance) string {
	if inst.ShutdownBehavior == "" {
		return "stop"
	}
	return inst.ShutdownBehavior
}

func handleModifyInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2Instances.Get(instanceID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", instanceID), http.StatusBadRequest)
		return
	}
	shutdownBehavior := r.FormValue("InstanceInitiatedShutdownBehavior.Value")
	if shutdownBehavior != "" && !ec2ValidShutdownBehavior(w, shutdownBehavior) {
		return
	}
	// Persist the modified attributes.
	var regrouped string
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
		if shutdownBehavior != "" {
			inst.ShutdownBehavior = shutdownBehavior
		}
		if groups := ec2ParamList(r, "GroupId"); len(groups) > 0 {
			inst.SecurityGroupIds = groups
			regrouped = inst.VpcId
		}
	})
	if regrouped != "" {
		ec2ReapplyIngress(regrouped)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceAttributeResponse %s><requestId>%s</requestId><return>true</return></ModifyInstanceAttributeResponse>`, ec2Xmlns(), sim.NewUUID())
}

// The provider's path for an aws_instance.metadata_options change.
func handleModifyInstanceMetadataOptions(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2Instances.Get(instanceID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", instanceID), http.StatusBadRequest)
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
		if v := r.FormValue("HttpProtocolIpv6"); v != "" {
			inst.MetadataHttpProtocolIpv6 = v
		}
	})
	inst, _ := ec2Instances.Get(instanceID)
	hop := inst.MetadataHopLimit
	if hop == 0 {
		hop = 1
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceMetadataOptionsResponse %s><requestId>%s</requestId><instanceId>%s</instanceId>`+
		`<instanceMetadataOptions><state>applied</state><httpTokens>%s</httpTokens><httpPutResponseHopLimit>%d</httpPutResponseHopLimit><httpEndpoint>%s</httpEndpoint><httpProtocolIpv6>%s</httpProtocolIpv6><instanceMetadataTags>%s</instanceMetadataTags></instanceMetadataOptions>`+
		`</ModifyInstanceMetadataOptionsResponse>`,
		ec2Xmlns(), sim.NewUUID(), instanceID, inst.MetadataHttpTokens, hop, inst.MetadataHttpEndpoint, ec2MetadataHttpProtocolIpv6(inst), inst.MetadataInstanceTags)
}

// Published AMIs plus those the account registered, created, copied or
// imported, narrowed by ImageIds, Owners and filters.
func handleDescribeImages(w http.ResponseWriter, r *http.Request) {
	imageIDs := ec2ParamList(r, "ImageId")
	owners := ec2ParamList(r, "Owner")
	filters := ec2Filters(r)
	for _, id := range imageIDs {
		if _, ok := ec2LookupImage(id); !ok {
			ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id '[%s]' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	var items strings.Builder
	for _, img := range ec2AllImages() {
		if len(imageIDs) > 0 && !ec2StrInValues(img.ImageId, imageIDs) {
			continue
		}
		if !ec2ImageOwnedBy(img, owners) || !ec2ImageMatchesFilters(img, filters) {
			continue
		}
		items.WriteString(ec2StoredImageXML(img))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImagesResponse %s><requestId>%s</requestId><imagesSet>%s</imagesSet></DescribeImagesResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

// An AMI the launch path cannot find reports x86_64 until the boot refuses
// it.
func ec2ImageArchitecture(imageID string) string {
	if img, ok := ec2LookupImage(imageID); ok && img.Architecture != "" {
		return img.Architecture
	}
	return "x86_64"
}

// Every catalog type is offered in every requested location. Filters:
// `instance-type` with wildcards, and `location`.
func handleDescribeInstanceTypeOfferings(w http.ResponseWriter, r *http.Request) {
	locationType := r.FormValue("LocationType")
	if locationType == "" {
		locationType = "region"
	}
	filters := ec2Filters(r)
	var types []string
	catalog, _ := ec2InstanceTypeCatalog()
	for _, t := range catalog {
		if patterns := filters["instance-type"]; len(patterns) > 0 && !slices.ContainsFunc(patterns, func(p string) bool { return ec2FilterWildcard.Match(p, t.Name) }) {
			continue
		}
		types = append(types, t.Name)
	}
	locations := filters["location"]
	if len(locations) == 0 {
		switch locationType {
		case "availability-zone":
			locations = []string{awsAvailabilityZone()}
		case "availability-zone-id":
			locations = []string{awsRegion() + "-az1"}
		default:
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
</DescribeInstanceTypeOfferingsResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}
