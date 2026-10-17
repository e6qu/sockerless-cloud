package ipam

import (
	"fmt"
	"hash/fnv"
	"net"
	"strings"
)

// DeriveMAC derives a stable interface MAC from a resource identifier under a
// three-octet prefix such as "02:0a:ec". The low three octets hash the whole
// identifier, so two interfaces on one bridge do not share an address.
func DeriveMAC(oui, id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(id)))
	sum := h.Sum32()
	return fmt.Sprintf("%s:%02x:%02x:%02x", oui, byte(sum>>16), byte(sum>>8), byte(sum))
}

// FirstHostGateway is the address after a subnet's network address, where
// Amazon VPC, Google Cloud VPC and Azure Virtual Network all put the subnet
// router. It is nil for anything but an IPv4 CIDR with a host after the
// network address.
func FirstHostGateway(cidr string) net.IP {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	base := network.IP.To4()
	if base == nil {
		return nil
	}
	if ones, _ := network.Mask.Size(); ones >= 32 {
		return nil
	}
	out := append(net.IP(nil), base...)
	out[3]++
	return out
}
