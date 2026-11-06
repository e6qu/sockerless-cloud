package main

import "github.com/e6qu/sockerless-cloud/sim"

// What a workload sends through a NAT gateway reaches the simulator from the
// gateway's sidecar, at the gateway's private address; AWS sees it arrive
// from the gateway's public address.
var ec2NatByPrivateIP sim.GenerationIndex[EC2NatGateway]

// ec2NatPublicAddress is the public address a public NAT gateway sends from
// when private is one of its private addresses: a zonal gateway's address, or
// a regional gateway's address in the zone that private serves.
func ec2NatPublicAddress(private string) (string, bool) {
	if ec2NatGateways == nil {
		return "", false
	}
	natgw, ok := ec2NatByPrivateIP.Lookup(ec2NatGateways, private, func(n EC2NatGateway) []string {
		var keys []string
		for _, a := range n.NatGatewayAddresses {
			if a.PrivateIp != "" {
				keys = append(keys, a.PrivateIp)
			}
		}
		for _, z := range n.Zones {
			if z.PrivateIp != "" {
				keys = append(keys, z.PrivateIp)
			}
		}
		return keys
	})
	if !ok || natgw.State != "available" {
		return "", false
	}
	for _, a := range natgw.NatGatewayAddresses {
		if a.PrivateIp == private && a.PublicIp != "" {
			return a.PublicIp, true
		}
	}
	for _, z := range natgw.Zones {
		if z.PrivateIp != private {
			continue
		}
		for _, a := range natgw.NatGatewayAddresses {
			if a.AvailabilityZone == z.AvailabilityZone && a.PublicIp != "" {
				return a.PublicIp, true
			}
		}
	}
	return "", false
}
