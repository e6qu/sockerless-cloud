package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

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

func handleDescribeNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	enis, ok := ec2NamedOrMatching(w, r, ec2NetworkInterfaces, "NetworkInterfaceId", "InvalidNetworkInterfaceID.NotFound",
		"The networkInterface ID '%s' does not exist", ec2NetworkInterfaceMatchesFilters)
	if !ok {
		return
	}
	var items strings.Builder
	for _, eni := range enis {
		items.WriteString("<item>" + eniFieldsXML(eni) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeNetworkInterfacesResponse %s>
  <requestId>%s</requestId>
  <networkInterfaceSet>%s</networkInterfaceSet>
</DescribeNetworkInterfacesResponse>`, ec2Xmlns(), sim.NewUUID(), items.String())
}

// The attachment block appears only when attached; sourceDestCheck reflects
// SourceDestDisabled.
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
    <macAddress>%s</macAddress>
    <privateIpAddress>%s</privateIpAddress>
    <privateDnsName>%s</privateDnsName>
    <sourceDestCheck>%s</sourceDestCheck>
    <interfaceType>%s</interfaceType>
    <groupSet>%s</groupSet>
    <privateIpAddressesSet>%s</privateIpAddressesSet>
    %s
    %s`,
		eni.NetworkInterfaceId, eni.SubnetId, eni.VpcId, awsAvailabilityZone(), eni.Description, eni.OwnerId, status,
		ec2InterfaceMAC(eni.NetworkInterfaceId), eni.PrivateIpAddress, ec2PrivateDNSName(eni.PrivateIpAddress), sourceDest, ifaceType, groups.String(),
		privateIPs.String(), attachment, writeTagSetXML(eni.Tags))
}

// A standalone network interface starts available with source/dest check on.
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
</CreateNetworkInterfaceResponse>`, ec2Xmlns(), sim.NewUUID(), eniFieldsXML(eni))
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
</AttachNetworkInterfaceResponse>`, ec2Xmlns(), sim.NewUUID(), attachID)
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
	regrouped := false
	if groups := ec2ParamList(r, "SecurityGroupId"); len(groups) > 0 {
		eni.SecurityGroupIds = groups
		regrouped = true
	}
	if v := r.FormValue("Attachment.DeleteOnTermination"); v != "" {
		eni.DeleteOnTermination = v == "true"
	}
	ec2NetworkInterfaces.Put(eniID, eni)
	if regrouped {
		ec2ReapplyIngress(eni.VpcId)
	}
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
</AssignPrivateIpAddressesResponse>`, ec2Xmlns(), sim.NewUUID(), eniID, ipItems.String())
}
