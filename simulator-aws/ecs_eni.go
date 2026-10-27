package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

// A locally administered OUI.
const ec2MACPrefix = "02:0a:ec"

// Stable for the interface's life.
func ec2InterfaceMAC(networkInterfaceID string) string {
	return ipam.DeriveMAC(ec2MACPrefix, networkInterfaceID)
}

// Created at RunTask, reported PRECREATED, so Amazon EC2 refuses to delete the
// subnet under a starting task.
func ecsCreateTaskNetworkInterface(taskID, attachmentID, subnetID, privateIP string, securityGroupIDs []string) (ECSAttachment, error) {
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return ECSAttachment{}, fmt.Errorf("subnet %s does not exist", subnetID)
	}
	eni := EC2NetworkInterface{
		NetworkInterfaceId: ec2ID("eni"),
		SubnetId:           subnetID,
		VpcId:              subnet.VpcId,
		PrivateIpAddress:   privateIP,
		Status:             "in-use",
		AttachmentId:       ec2ID("ela-attach"),
		DeviceIndex:        1,
		Description:        ecsArn("attachment", attachmentID),
		SecurityGroupIds:   append([]string(nil), securityGroupIDs...),
		InterfaceType:      "interface",
		OwnerId:            ec2Owner(),
	}
	ec2NetworkInterfaces.Put(eni.NetworkInterfaceId, eni)
	return ECSAttachment{
		Id:     attachmentID,
		Type:   "ElasticNetworkInterface",
		Status: "PRECREATED",
		Details: []ECSKeyValuePair{
			{Name: "subnetId", Value: subnetID},
			{Name: "networkInterfaceId", Value: eni.NetworkInterfaceId},
			{Name: "macAddress", Value: ec2InterfaceMAC(eni.NetworkInterfaceId)},
			{Name: "privateDnsName", Value: ec2PrivateDNSName(privateIP)},
			{Name: "privateIPv4Address", Value: privateIP},
		},
	}, nil
}

// Amazon ECS deletes the interfaces and reports the attachments DELETED before
// the task reports STOPPED.
func ecsDeleteTaskNetworkInterfaces(task *ECSTask) {
	for i := range task.Attachments {
		att := &task.Attachments[i]
		if att.Type != "ElasticNetworkInterface" {
			continue
		}
		if eniID := ecsTaskDetail(att.Details, "networkInterfaceId"); eniID != "" {
			ec2NetworkInterfaces.Delete(eniID)
		}
		att.Status = "DELETED"
	}
}

// Returns the engine volumes to remove once the task's store update returns.
func ecsReleaseTaskAttachments(task *ECSTask) []string {
	ecsDeleteTaskNetworkInterfaces(task)
	return ecsCleanupTaskManagedEBS(task)
}
