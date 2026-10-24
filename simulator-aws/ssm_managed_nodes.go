package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ssmNodeRegistrationRole is the role of the instance's profile when it lets
// SSM Agent register the instance as a managed node: the role allows
// ssm:UpdateInstanceInformation, which the agent calls to register and to
// report its health.
func ssmNodeRegistrationRole(inst EC2Instance) (string, bool) {
	_, roleName, ok := ec2InstanceProfileRole(inst)
	if !ok {
		return "", false
	}
	decision, _ := iamEvalDecision(iamPolicyDocsForRole(roleName), "ssm:UpdateInstanceInformation", "*", nil)
	return roleName, decision == "allowed"
}

// ssmRegisterManagedNode registers a running instance as a managed node when
// its profile's role lets SSM Agent register it, reading the node's platform
// from its /etc/os-release as the agent does. An instance whose role no longer
// allows the agent to report loses its connection.
func ssmRegisterManagedNode(instanceID string) {
	inst := ec2InstanceRecord(instanceID)
	roleName, allowed := ssmNodeRegistrationRole(inst)
	now := float64(time.Now().Unix())
	if !allowed {
		ssmManagedInstances.Update(instanceID, func(node *SSMManagedInstance) {
			node.PingStatus = "ConnectionLost"
		})
		return
	}
	handle, running := ec2WorkloadHandle(instanceID)
	if !running {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := handle.Exec(ctx, []string{"/bin/sh", "-c", "cat /etc/os-release"})
	if err != nil || result.ExitCode != 0 {
		return
	}
	name, version := ssmOSRelease(result.Stdout)
	node, registered := ssmManagedInstances.Get(instanceID)
	if !registered {
		node = SSMManagedInstance{InstanceId: instanceID, ResourceType: "EC2Instance", RegistrationDate: now}
	}
	node.PingStatus = "Online"
	node.LastPingDateTime = now
	node.PlatformType = "Linux"
	node.PlatformName, node.PlatformVersion = name, version
	node.IamRole = roleName
	node.IPAddress = inst.PrivateIpAddress
	node.ComputerName = ec2PrivateDNSName(inst.PrivateIpAddress)
	ssmManagedInstances.Put(instanceID, node)
}

// ssmOSRelease reads the NAME and VERSION_ID of an os-release file.
func ssmOSRelease(content string) (name, version string) {
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch key {
		case "NAME":
			name = value
		case "VERSION_ID":
			version = value
		}
	}
	return name, version
}

// ssmDeregisterTerminatedNode forgets a terminated instance's managed node.
func ssmDeregisterTerminatedNode(instanceID string) {
	ssmManagedInstances.Delete(instanceID)
}

// ssmManagedNodeOnline reports whether id is a managed node SSM Agent reports
// from, which a command can reach.
func ssmManagedNodeOnline(id string) bool {
	node, ok := ssmManagedInstances.Get(id)
	return ok && node.PingStatus == "Online"
}

// ssmNodeListed reports whether DescribeInstanceInformation lists the node:
// it leaves out an Amazon EC2 instance that is stopped or terminated.
func ssmNodeListed(node SSMManagedInstance) bool {
	if node.ResourceType != "EC2Instance" {
		return true
	}
	inst, ok := ec2Instances.Get(node.InstanceId)
	if !ok {
		return false
	}
	switch inst.State {
	case "stopping", "stopped", "shutting-down", "terminated":
		return false
	}
	return true
}

// ssmInstanceInformationFilter is one filter of DescribeInstanceInformation,
// from either of its two filter lists.
type ssmInstanceInformationFilter struct {
	Key    string
	Values []string
}

var ssmInstanceInformationFilterKeys = map[string]bool{
	"InstanceIds": true, "AgentVersion": true, "PingStatus": true, "PlatformTypes": true,
	"ActivationIds": true, "IamRole": true, "ResourceType": true, "AssociationStatus": true,
	"SourceIds": true, "SourceTypes": true, "tag-key": true,
}

// ssmInstanceInformationFilters checks the filters' keys and values, as
// DescribeInstanceInformation refuses an unknown key or a filter without
// values.
func ssmInstanceInformationFilters(filters []ssmInstanceInformationFilter) (code, message string) {
	for _, f := range filters {
		if !ssmInstanceInformationFilterKeys[f.Key] && !strings.HasPrefix(f.Key, "tag:") {
			return "InvalidFilterKey", fmt.Sprintf("The filter key %s isn't valid.", f.Key)
		}
		if len(f.Values) == 0 {
			return "InvalidFilterValue", fmt.Sprintf("The filter %s needs one or more values.", f.Key)
		}
	}
	return "", ""
}

// ssmNodeMatchesFilters reports whether node satisfies every filter.
func ssmNodeMatchesFilters(node SSMManagedInstance, filters []ssmInstanceInformationFilter) bool {
	for _, f := range filters {
		var value string
		switch f.Key {
		case "InstanceIds":
			value = node.InstanceId
		case "AgentVersion":
			value = node.AgentVersion
		case "PingStatus":
			value = node.PingStatus
		case "PlatformTypes":
			value = node.PlatformType
		case "ActivationIds":
			value = node.ActivationId
		case "IamRole":
			value = node.IamRole
		case "ResourceType":
			value = node.ResourceType
		case "tag-key":
			if !ssmNodeHasTag(node, f.Values, nil) {
				return false
			}
			continue
		default:
			if key, ok := strings.CutPrefix(f.Key, "tag:"); ok {
				if !ssmNodeHasTag(node, []string{key}, f.Values) {
					return false
				}
				continue
			}
			return false
		}
		matched := false
		for _, want := range f.Values {
			if want == value {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// ssmNodeHasTag reports whether the Amazon EC2 instance behind node carries a
// tag with one of keys and, when values is set, one of values.
func ssmNodeHasTag(node SSMManagedInstance, keys, values []string) bool {
	inst, ok := ec2Instances.Get(node.InstanceId)
	if !ok {
		return false
	}
	for _, tag := range inst.Tags {
		for _, key := range keys {
			if tag.Key != key {
				continue
			}
			if values == nil {
				return true
			}
			for _, value := range values {
				if tag.Value == value {
					return true
				}
			}
		}
	}
	return false
}
