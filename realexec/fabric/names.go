package fabric

import (
	"fmt"
	"hash/fnv"
	"net"
	"strconv"
	"strings"
)

// LinuxName derives the host-side name of a namespace, bridge, veth, tap or
// nft table from the resource it realizes. Linux caps an interface name at 15
// characters, far shorter than a resource identifier, so the name is the
// prefix plus a hash of the whole identifier: two resources that differ
// anywhere get different host objects, which truncating the identifier could
// not guarantee. Identifiers compare case-insensitively, as Azure's do.
func LinuxName(prefix, id string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(id)))
	name := prefix + strconv.FormatUint(h.Sum64(), 36)
	if len(name) > 15 {
		return name[:15]
	}
	return name
}

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
