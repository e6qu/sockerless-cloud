package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
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

// The two fields resolving a target needs.
type ecsTaskENI struct {
	taskID   string
	subnetID string
}

// Maps an ENI address to the running awsvpc tasks owning it, so proxied
// requests do not decode the whole task table; per-request scans dominated
// the simulator's CPU under load. Rebuilt when the task store's generation
// moves. The Docker published-port lookup stays live, since a cached port
// could outlive its container.
var ecsRunningTaskENIIndex struct {
	mu         sync.RWMutex
	generation uint64
	built      bool
	byAddress  map[string][]ecsTaskENI
}

func ecsRunningTaskENIs(privateIP string) []ecsTaskENI {
	// Read the generation first, so a write during the scan leaves the index at
	// the older generation and the next caller rebuilds.
	generation := ecsTasks.Generation()

	ecsRunningTaskENIIndex.mu.RLock()
	if ecsRunningTaskENIIndex.built && ecsRunningTaskENIIndex.generation == generation {
		found := ecsRunningTaskENIIndex.byAddress[privateIP]
		ecsRunningTaskENIIndex.mu.RUnlock()
		return found
	}
	ecsRunningTaskENIIndex.mu.RUnlock()

	ecsRunningTaskENIIndex.mu.Lock()
	defer ecsRunningTaskENIIndex.mu.Unlock()
	// Another request may have rebuilt it while this one waited.
	if !ecsRunningTaskENIIndex.built || ecsRunningTaskENIIndex.generation != generation {
		byAddress := map[string][]ecsTaskENI{}
		for _, task := range ecsTasks.List() {
			if task.LastStatus != ECSTaskStatusRunning {
				continue
			}
			for _, attachment := range task.Attachments {
				if attachment.Type != "ElasticNetworkInterface" {
					continue
				}
				address := ecsTaskDetail(attachment.Details, "privateIPv4Address")
				if address == "" {
					continue
				}
				byAddress[address] = append(byAddress[address], ecsTaskENI{
					taskID:   task.TaskID(),
					subnetID: ecsTaskDetail(attachment.Details, "subnetId"),
				})
			}
		}
		ecsRunningTaskENIIndex.byAddress = byAddress
		ecsRunningTaskENIIndex.generation = generation
		ecsRunningTaskENIIndex.built = true
	}
	return ecsRunningTaskENIIndex.byAddress[privateIP]
}

// A directly routable address has no mapping. Target groups are VPC-scoped and
// VPCs may share a CIDR, so match within the group's VPC when it names one.
func ecsPublishedTargetPort(privateIP string, containerPort int, vpcID string) (int, bool) {
	for _, eni := range ecsRunningTaskENIs(privateIP) {
		if vpcID != "" {
			subnet, ok := ec2Subnets.Get(eni.subnetID)
			if !ok || subnet.VpcId != vpcID {
				continue
			}
		}
		held, ok := ecsProcessHandles.Load(eni.taskID)
		if !ok {
			continue
		}
		processes, ok := held.(*ecsTaskProcesses)
		if !ok {
			panic("Amazon ECS task process registry contained a non-process value")
		}
		for _, handle := range processes.Handles {
			if port, err := handle.PublishedPort(context.Background(), containerPort); err == nil && port > 0 {
				return port, true
			}
		}
	}
	return 0, false
}
