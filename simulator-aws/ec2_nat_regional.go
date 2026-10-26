package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

// A regional NAT gateway belongs to a VPC rather than to a subnet. Its
// addresses each serve one Availability Zone. In automatic mode, the
// default, it "associates EIPs in all AZs where your VPC has subnets ...,
// expands to new AZs when you create subnets there, and retracts from AZs
// where you've removed all subnets"; with AvailabilityZoneAddresses given
// "auto-expansion is disabled and you must manually manage AZ coverage".
// The simulator places the gateway's presence in each covered zone at a
// private address of one of the zone's subnets, where the zone's egress
// sidecar runs.

// ec2NatRegionalMu serializes changes to regional gateways' zones and
// addresses, which subnet, gateway and address operations all make.
var ec2NatRegionalMu sync.Mutex

func handleCreateRegionalNatGateway(w http.ResponseWriter, r *http.Request, connectivityType string) {
	for _, param := range []string{"SubnetId", "AllocationId", "PrivateIpAddress", "SecondaryAllocationId.1", "SecondaryPrivateIpAddress.1", "SecondaryPrivateIpAddressCount"} {
		if r.FormValue(param) != "" {
			ec2ErrorXML(w, "InvalidParameterCombination", fmt.Sprintf("The parameter %s is not supported for a regional NAT gateway", param), http.StatusBadRequest)
			return
		}
	}
	if connectivityType != "public" {
		ec2ErrorXML(w, "InvalidParameterCombination", "A regional NAT gateway uses Elastic IP addresses and supports only public connectivity", http.StatusBadRequest)
		return
	}
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpcId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcID); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcID), http.StatusBadRequest)
		return
	}
	addresses, code, message := ec2RegionalZoneAddressParams(r)
	if code != "" {
		ec2ErrorXML(w, code, message, http.StatusBadRequest)
		return
	}
	natgw := EC2NatGateway{
		NatGatewayId:       ec2ID("nat"),
		VpcId:              vpcID,
		State:              "available",
		ConnectivityType:   connectivityType,
		AvailabilityMode:   "regional",
		AutoProvisionZones: "enabled",
		AutoScalingIps:     "enabled",
		Tags:               parseTags(r),
		CreateTime:         time.Now().UTC().Format(time.RFC3339),
	}
	if len(addresses) > 0 {
		natgw.AutoProvisionZones, natgw.AutoScalingIps = "disabled", "disabled"
	}
	ec2NatRegionalMu.Lock()
	for _, address := range addresses {
		eip, ok := ec2ElasticIPs.Get(address.AllocationId)
		switch {
		case !ok:
			natgw.FailureCode = "InvalidAllocationID.NotFound"
			natgw.FailureMessage = fmt.Sprintf("Elastic IP address %s could not be associated with this NAT gateway", address.AllocationId)
		case ec2ElasticIPAssociated(eip):
			natgw.FailureCode = "Resource.AlreadyAssociated"
			natgw.FailureMessage = fmt.Sprintf("Elastic IP address %s is already associated", address.AllocationId)
		default:
			address.PublicIp = eip.PublicIp
			natgw.NatGatewayAddresses = append(natgw.NatGatewayAddresses, address)
			continue
		}
		natgw.State = "failed"
		natgw.NatGatewayAddresses = nil
		break
	}
	ec2NatGateways.Put(natgw.NatGatewayId, natgw)
	ec2NatRegionalMu.Unlock()
	if natgw.State == "available" {
		if err := ec2ReconcileRegionalNatGateways(r.Context(), vpcID); err != nil {
			ec2RemoveNatGatewayEgress(natgw.NatGatewayId)
			if stored, ok := ec2NatGateways.Get(natgw.NatGatewayId); ok {
				ec2ReleaseAmazonProvidedNatAddresses(stored.NatGatewayAddresses)
			}
			ec2NatGateways.Delete(natgw.NatGatewayId)
			ec2ReapplyRoutes(vpcID)
			ec2ErrorXML(w, "InternalError", fmt.Sprintf("failed to start the NAT gateway: %v", err), http.StatusInternalServerError)
			return
		}
		natgw, _ = ec2NatGateways.Get(natgw.NatGatewayId)
	}
	writeCreateNatGatewayResponse(w, natgw)
}

// ec2RegionalZoneAddressParams reads CreateNatGateway's
// AvailabilityZoneAddress.N members, each naming its zone by name or id and
// the Elastic IP addresses that serve it.
func ec2RegionalZoneAddressParams(r *http.Request) ([]EC2NatGatewayAddress, string, string) {
	var addresses []EC2NatGatewayAddress
	for i := 1; ; i++ {
		prefix := fmt.Sprintf("AvailabilityZoneAddress.%d.", i)
		az, azID := r.FormValue(prefix+"AvailabilityZone"), r.FormValue(prefix+"AvailabilityZoneId")
		allocations := ec2ParamList(r, prefix+"AllocationId")
		if az == "" && azID == "" && len(allocations) == 0 {
			return addresses, "", ""
		}
		zone, zoneID, code, message := ec2RequestZone(az, azID)
		if code != "" {
			return nil, code, message
		}
		if len(allocations) == 0 {
			return nil, "MissingParameter", fmt.Sprintf("The request must contain the parameter %sAllocationId", prefix)
		}
		for _, allocation := range allocations {
			addresses = append(addresses, EC2NatGatewayAddress{
				AllocationId:       allocation,
				AssociationId:      ec2ID("eipassoc"),
				Status:             "succeeded",
				AvailabilityZone:   zone,
				AvailabilityZoneId: zoneID,
			})
		}
	}
}

// ec2ElasticIPAssociated reports whether the Elastic IP address is already
// associated, with an instance or network interface or with a NAT gateway.
func ec2ElasticIPAssociated(eip EC2ElasticIP) bool {
	if eip.AssociationId != "" {
		return true
	}
	for _, natgw := range ec2NatGateways.List() {
		for _, address := range natgw.NatGatewayAddresses {
			if address.AllocationId == eip.AllocationId {
				return true
			}
		}
	}
	return false
}

// ec2NatGatewayZoneOf is the gateway's presence in the zone, when the
// gateway covers it.
func ec2NatGatewayZoneOf(natgw EC2NatGateway, zone string) (EC2NatGatewayZone, bool) {
	for _, z := range natgw.Zones {
		if z.AvailabilityZone == zone {
			return z, true
		}
	}
	return EC2NatGatewayZone{}, false
}

// ec2ReleaseAmazonProvidedNatAddresses returns the public addresses a
// regional gateway allocated itself to the pool.
func ec2ReleaseAmazonProvidedNatAddresses(addresses []EC2NatGatewayAddress) {
	for _, address := range addresses {
		if address.AmazonProvided {
			ipam.ReleasePublicIPv4(net.ParseIP(address.PublicIp))
		}
	}
}

// ec2RefuseRegionalPrivateAddresses refuses the private-address operations
// on a regional gateway, whose addresses are Elastic IP addresses per zone.
func ec2RefuseRegionalPrivateAddresses(w http.ResponseWriter, natgw EC2NatGateway) bool {
	if ec2NatAvailabilityMode(natgw) != "regional" {
		return false
	}
	ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("NAT gateway '%s' is a regional NAT gateway, which has no secondary private IPv4 addresses", natgw.NatGatewayId), http.StatusBadRequest)
	return true
}

// ec2AssociateRegionalNatGatewayAddress associates Elastic IP addresses with
// a regional gateway in the zone the request names: "The regional NAT
// gateway uses a separate EIP in each AZ to handle outbound NAT traffic from
// that AZ."
func ec2AssociateRegionalNatGatewayAddress(w http.ResponseWriter, r *http.Request, natgw EC2NatGateway) {
	if len(ec2ParamList(r, "PrivateIpAddress")) > 0 {
		ec2ErrorXML(w, "InvalidParameterCombination", "PrivateIpAddress is not supported for a regional NAT gateway", http.StatusBadRequest)
		return
	}
	zone, zoneID, code, message := ec2RequestZone(r.FormValue("AvailabilityZone"), r.FormValue("AvailabilityZoneId"))
	if code != "" {
		ec2ErrorXML(w, code, message, http.StatusBadRequest)
		return
	}
	allocations := ec2ParamList(r, "AllocationId")
	if len(allocations) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter AllocationId", http.StatusBadRequest)
		return
	}
	ec2NatRegionalMu.Lock()
	addresses := make([]EC2NatGatewayAddress, 0, len(allocations))
	for _, allocation := range allocations {
		eip, ok := ec2ElasticIPs.Get(allocation)
		if !ok {
			ec2NatRegionalMu.Unlock()
			ec2ErrorXML(w, "InvalidAllocationID.NotFound", fmt.Sprintf("The allocation ID '%s' does not exist", allocation), http.StatusBadRequest)
			return
		}
		if ec2ElasticIPAssociated(eip) {
			ec2NatRegionalMu.Unlock()
			ec2ErrorXML(w, "Resource.AlreadyAssociated", fmt.Sprintf("Elastic IP address %s is already associated", allocation), http.StatusBadRequest)
			return
		}
		addresses = append(addresses, EC2NatGatewayAddress{
			AllocationId:       allocation,
			PublicIp:           eip.PublicIp,
			AssociationId:      ec2ID("eipassoc"),
			Status:             "succeeded",
			AvailabilityZone:   zone,
			AvailabilityZoneId: zoneID,
		})
	}
	ec2NatGateways.Update(natgw.NatGatewayId, func(n *EC2NatGateway) {
		n.NatGatewayAddresses = append(slices.Clone(n.NatGatewayAddresses), addresses...)
	})
	ec2NatRegionalMu.Unlock()
	ec2ReconcileRegionalNatGatewaysLogged(r.Context(), natgw.VpcId)
	natgw, _ = ec2NatGateways.Get(natgw.NatGatewayId)
	writeNatGatewayAddressResponse(w, "AssociateNatGatewayAddress", natgw)
}

// ec2DisassociateRegionalNatGatewayAddress disassociates addresses from a
// regional gateway, whose every address, the only one of a zone included,
// may go: a zone left without an address leaves the gateway's coverage.
func ec2DisassociateRegionalNatGatewayAddress(w http.ResponseWriter, r *http.Request, natgw EC2NatGateway) {
	associations := ec2ParamList(r, "AssociationId")
	if len(associations) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter AssociationId", http.StatusBadRequest)
		return
	}
	ec2NatRegionalMu.Lock()
	var released []EC2NatGatewayAddress
	ec2NatGateways.Update(natgw.NatGatewayId, func(n *EC2NatGateway) {
		kept := make([]EC2NatGatewayAddress, 0, len(n.NatGatewayAddresses))
		for _, address := range n.NatGatewayAddresses {
			if ec2StrInValues(address.AssociationId, associations) {
				released = append(released, address)
				continue
			}
			kept = append(kept, address)
		}
		n.NatGatewayAddresses = kept
	})
	ec2ReleaseAmazonProvidedNatAddresses(released)
	ec2NatRegionalMu.Unlock()
	ec2ReconcileRegionalNatGatewaysLogged(r.Context(), natgw.VpcId)
	natgw, _ = ec2NatGateways.Get(natgw.NatGatewayId)
	writeNatGatewayAddressResponse(w, "DisassociateNatGatewayAddress", natgw)
}

// ec2ReconcileRegionalNatGatewaysLogged reconciles the VPC's regional
// gateways for an operation that has already taken effect and cannot fail
// on the gateways' data plane.
func ec2ReconcileRegionalNatGatewaysLogged(ctx context.Context, vpcID string) {
	if err := ec2ReconcileRegionalNatGateways(ctx, vpcID); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-ec2] vpc %s: regional NAT gateways: %v\n", vpcID, err)
	}
}

// ec2ReconcileRegionalNatGateways brings every available regional gateway of
// the VPC to the VPC's subnets: an automatic gateway covers the zones the
// subnets use and no other, each covered zone with an IPv4 subnet holds the
// gateway at an address of one of them, and the zone's sidecar runs there
// while the VPC's network is up. Every workload of the VPC then follows its
// route table again.
func ec2ReconcileRegionalNatGateways(ctx context.Context, vpcID string) error {
	if vpcID == "" || ec2NatGateways == nil {
		return nil
	}
	ec2NatRegionalMu.Lock()
	gateways := ec2NatGateways.Filter(func(n EC2NatGateway) bool {
		return n.VpcId == vpcID && n.State == "available" && ec2NatAvailabilityMode(n) == "regional"
	})
	if len(gateways) == 0 {
		ec2NatRegionalMu.Unlock()
		return nil
	}
	subnets := ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == vpcID })
	slices.SortFunc(subnets, func(a, b EC2Subnet) int {
		if a.SubnetId < b.SubnetId {
			return -1
		}
		if a.SubnetId > b.SubnetId {
			return 1
		}
		return 0
	})
	zonesWithSubnets := map[string]bool{}
	ipv4Subnets := map[string][]EC2Subnet{}
	for _, subnet := range subnets {
		zonesWithSubnets[subnet.AvailabilityZone] = true
		if subnet.CidrBlock != "" {
			ipv4Subnets[subnet.AvailabilityZone] = append(ipv4Subnets[subnet.AvailabilityZone], subnet)
		}
	}
	var failures []error
	var stale []string
	var serving []EC2NatGateway
	for _, natgw := range gateways {
		addresses := slices.Clone(natgw.NatGatewayAddresses)
		if natgw.AutoProvisionZones == "enabled" {
			var released []EC2NatGatewayAddress
			addresses = slices.DeleteFunc(addresses, func(a EC2NatGatewayAddress) bool {
				if zonesWithSubnets[a.AvailabilityZone] {
					return false
				}
				released = append(released, a)
				return true
			})
			ec2ReleaseAmazonProvidedNatAddresses(released)
			for _, zone := range awsAvailabilityZones() {
				if !zonesWithSubnets[zone] || slices.ContainsFunc(addresses, func(a EC2NatGatewayAddress) bool { return a.AvailabilityZone == zone }) {
					continue
				}
				allocation := ec2ID("eipalloc")
				ip, err := ipam.ReserveAWSPublicIPv4(allocation, nil)
				if err != nil {
					failures = append(failures, fmt.Errorf("NAT gateway %s: an address in %s: %w", natgw.NatGatewayId, zone, err))
					continue
				}
				addresses = append(addresses, EC2NatGatewayAddress{
					AllocationId:       allocation,
					PublicIp:           ip.String(),
					AssociationId:      ec2ID("eipassoc"),
					Status:             "succeeded",
					AvailabilityZone:   zone,
					AvailabilityZoneId: ec2AvailabilityZoneId(zone),
					AmazonProvided:     true,
				})
			}
		}
		var zones []EC2NatGatewayZone
		for _, zone := range awsAvailabilityZones() {
			if !slices.ContainsFunc(addresses, func(a EC2NatGatewayAddress) bool { return a.AvailabilityZone == zone && a.Status == "succeeded" }) {
				continue
			}
			candidates := ipv4Subnets[zone]
			if len(candidates) == 0 {
				continue
			}
			current, ok := ec2NatGatewayZoneOf(natgw, zone)
			if ok && slices.ContainsFunc(candidates, func(s EC2Subnet) bool { return s.SubnetId == current.SubnetId }) {
				zones = append(zones, current)
				continue
			}
			privateIP, err := AllocateSubnetIP(candidates[0].SubnetId)
			if err != nil {
				failures = append(failures, fmt.Errorf("NAT gateway %s: an address in %s: %w", natgw.NatGatewayId, candidates[0].SubnetId, err))
				continue
			}
			networkInterfaceID := current.NetworkInterfaceId
			if networkInterfaceID == "" {
				networkInterfaceID = ec2ID("eni")
			}
			zones = append(zones, EC2NatGatewayZone{
				AvailabilityZone:   zone,
				SubnetId:           candidates[0].SubnetId,
				PrivateIp:          privateIP,
				NetworkInterfaceId: networkInterfaceID,
			})
		}
		for _, old := range natgw.Zones {
			if _, ok := ec2NatGatewayZoneOf(EC2NatGateway{Zones: zones}, old.AvailabilityZone); !ok {
				stale = append(stale, ec2NatEgressKey(natgw.NatGatewayId, old.AvailabilityZone))
			}
		}
		ec2NatGateways.Update(natgw.NatGatewayId, func(n *EC2NatGateway) {
			n.NatGatewayAddresses = addresses
			n.Zones = zones
		})
		natgw.NatGatewayAddresses, natgw.Zones = addresses, zones
		serving = append(serving, natgw)
	}
	ec2NatRegionalMu.Unlock()
	for _, key := range stale {
		ec2StopNatEgress(key)
	}
	if network, ok := ec2VPCNetworkUp(vpcID); ok {
		for _, natgw := range serving {
			if err := ec2EnsureNatGatewayEgress(ctx, natgw, network); err != nil {
				failures = append(failures, err)
			}
		}
	}
	ec2ReapplyRoutes(vpcID)
	return errors.Join(failures...)
}
