package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

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

// ecsTaskENI is one running task's elastic-network-interface attachment,
// reduced to the two fields that resolving a load-balancer target needs.
type ecsTaskENI struct {
	taskID   string
	subnetID string
}

// ecsRunningTaskENIIndex maps a running awsvpc task's ENI address to the tasks
// that own it, so the data plane does not decode the whole ECS task table once
// per proxied request.
//
// It used to. A CPU profile of the deployed simulator, taken while twelve
// concurrent requests were in flight against one ALB-fronted application,
// attributed 84.8% of all simulator CPU to ecsPublishedTargetPort, and 99.7%
// of that to ecsTasks.List() — overwhelmingly encoding/json decoding every
// stored task, stopped ones included, once for every request. The guest has
// two vCPUs, so the whole data plane ran at an effective concurrency of two: a
// static JSON health endpoint behind the load balancer answered in 1.3s where
// the same kind of endpoint on a directly-proxied application answered in
// 0.13s, and the cost grew linearly from there — 4.6s at eight concurrent
// requests, 9.3s at sixteen. That is why this surfaced as browser and
// Playwright timeouts rather than as ordinary slowness: one page load fans out
// over dozens of subresources, and every one of them paid the scan.
//
// The index is rebuilt whenever the task store's generation moves, so it can
// never answer for a task that has since stopped, changed address or moved
// subnet. The Docker published-port lookup deliberately stays live per
// request: it was 0.06% of the profile, and a cached port would eventually be
// a port that a recreated container is no longer listening on.
var ecsRunningTaskENIIndex struct {
	mu         sync.RWMutex
	generation uint64
	built      bool
	byAddress  map[string][]ecsTaskENI
}

func ecsRunningTaskENIs(privateIP string) []ecsTaskENI {
	// Read the generation before the contents. A write that lands during the
	// scan below therefore leaves the index recorded at the older generation,
	// and the next caller rebuilds rather than trusting a partial view.
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
	// Another request may have rebuilt the index while this one waited for the
	// write lock. Rebuilding again would be correct but is the expense this
	// exists to avoid.
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

// ecsPublishedTargetPort resolves the Docker Desktop transport mapping for an
// awsvpc task's real elastic-network-interface address. Linux task netns and
// bridge addresses remain directly routable and therefore have no mapping.
// A target group is VPC-scoped in real Elastic Load Balancing, and two live
// VPCs sharing a CIDR can hold tasks with identical ENI addresses, so the
// address match is confined to the target group's VPC when it names one.
func ecsPublishedTargetPort(privateIP string, containerPort int, vpcID string) (int, bool) {
	for _, eni := range ecsRunningTaskENIs(privateIP) {
		if vpcID != "" {
			subnet, ok := ec2Subnets.Get(eni.subnetID)
			if !ok || subnet.VpcId != vpcID {
				continue
			}
		}
		containers, err := sim.FindExistingContainers(map[string]string{
			"sockerless-sim-task": eni.taskID,
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
