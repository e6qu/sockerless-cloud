package main

import (
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/realexec/fabric"
)

// ecsCreateTaskNetworkInterface creates the elastic network interface an
// awsvpc task owns, in its subnet, at RunTask: the task's attachment reports
// PRECREATED with the interface already in the subnet, so EC2 refuses to delete
// the subnet under a task that is still starting.
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
			{Name: "macAddress", Value: fabric.DeriveMAC(ec2MACPrefix, taskID)},
			{Name: "privateDnsName", Value: fmt.Sprintf("ip-%s.%s.compute.internal", strings.ReplaceAll(privateIP, ".", "-"), awsRegion())},
			{Name: "privateIPv4Address", Value: privateIP},
		},
	}, nil
}

// ecsDeleteTaskNetworkInterfaces deletes a stopping task's elastic network
// interfaces and reports their attachments DELETED, as Amazon ECS does while
// the task deprovisions and before it reports STOPPED.
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

// ecsReleaseTaskAttachments releases what a task holds as it stops: its
// network interfaces and its managed Amazon EBS volumes. It returns the
// container-engine volumes to remove once the task's store update returns.
func ecsReleaseTaskAttachments(task *ECSTask) []string {
	ecsDeleteTaskNetworkInterfaces(task)
	return ecsCleanupTaskManagedEBS(task)
}
