package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

func elbv2TargetAddress(tg ELBv2TargetGroup, target ELBv2TargetDescription) (string, error) {
	host := target.ID
	if strings.EqualFold(tg.TargetType, "instance") {
		inst, ok := ec2Instances.Get(target.ID)
		if !ok {
			return "", fmt.Errorf("instance target %s not found", target.ID)
		}
		host = inst.PrivateIpAddress
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("target %s does not resolve to an IP address", target.ID)
	}
	port := target.Port
	if port == 0 {
		port = tg.Port
	}
	if port == 0 {
		return "", fmt.Errorf("target %s has no port", target.ID)
	}
	if publishedPort, ok := ecsPublishedTargetPort(host, port, tg.VpcID); ok {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(publishedPort)), nil
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// ecsPublishedTargetPort resolves the Docker Desktop transport mapping for an
// awsvpc task's real elastic-network-interface address. Linux task netns and
// bridge addresses remain directly routable and therefore have no mapping.
// A target group is VPC-scoped in real Elastic Load Balancing, and two live
// VPCs sharing a CIDR can hold tasks with identical ENI addresses, so the
// address match is confined to the target group's VPC when it names one.
func ecsPublishedTargetPort(privateIP string, containerPort int, vpcID string) (int, bool) {
	for _, task := range ecsTasks.List() {
		if task.LastStatus != ECSTaskStatusRunning {
			continue
		}
		matches := false
		for _, attachment := range task.Attachments {
			if attachment.Type != "ElasticNetworkInterface" ||
				ecsTaskDetail(attachment.Details, "privateIPv4Address") != privateIP {
				continue
			}
			if vpcID != "" {
				subnet, ok := ec2Subnets.Get(ecsTaskDetail(attachment.Details, "subnetId"))
				if !ok || subnet.VpcId != vpcID {
					continue
				}
			}
			matches = true
			break
		}
		if !matches {
			continue
		}
		containers, err := sim.FindExistingContainers(map[string]string{
			"sockerless-sim-task": task.TaskID(),
		})
		if err != nil {
			return 0, false
		}
		for _, container := range containers {
			if port := container.PublishedPorts[containerPort]; port > 0 {
				return port, true
			}
		}
	}
	return 0, false
}
