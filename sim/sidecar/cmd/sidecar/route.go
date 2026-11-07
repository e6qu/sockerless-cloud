package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// As `sidecar route <gateway> [<prefix>=<via>...]` the program joins a
// workload's network namespace for one change and exits: it points the
// namespace's IPv4 default route at the gateway, the next hop the cloud's
// route table names, removes the default route for `none`, a subnet whose
// routing names no way out, or leaves it alone for `-`; and it routes each
// prefix through its next hop, as the cloud routes its fixed link-local
// addresses to its metadata service. It holds NET_ADMIN and nothing else.
func serveRoute(out io.Writer, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sidecar route <gateway>|none|- [<prefix>=<via>...]")
	}
	for _, arg := range args[1:] {
		prefixText, viaText, ok := strings.Cut(arg, "=")
		prefix, perr := netip.ParsePrefix(prefixText)
		via, verr := netip.ParseAddr(viaText)
		if !ok || perr != nil || verr != nil || !prefix.Addr().Is4() || !via.Is4() {
			return fmt.Errorf("route %q: want <IPv4 prefix>=<IPv4 next hop>", arg)
		}
		msg := routeMessage(unix.RTM_NEWROUTE, unix.NLM_F_REQUEST|unix.NLM_F_CREATE|unix.NLM_F_REPLACE|unix.NLM_F_ACK, prefix.Masked(), via)
		if err := routeRequest(msg); err != nil {
			return fmt.Errorf("route %s through %s: %w", prefix, via, err)
		}
		fmt.Fprintf(out, "sidecar: route %s via %s\n", prefix, via)
	}
	switch args[0] {
	case "-":
		return nil
	case "none":
		err := routeRequest(routeMessage(unix.RTM_DELROUTE, unix.NLM_F_REQUEST|unix.NLM_F_ACK, netip.Prefix{}, netip.Addr{}))
		if err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("delete the default route: %w", err)
		}
		fmt.Fprintln(out, "sidecar: route none")
		return nil
	}
	gateway, err := netip.ParseAddr(args[0])
	if err != nil || !gateway.Is4() {
		return fmt.Errorf("gateway %q: an IPv4 address is required", args[0])
	}
	// The kernel finds the interface the gateway is on-link through, so the
	// message names no interface: a workload's interface order is the
	// engine's, not the cloud's.
	msg := routeMessage(unix.RTM_NEWROUTE, unix.NLM_F_REQUEST|unix.NLM_F_CREATE|unix.NLM_F_REPLACE|unix.NLM_F_ACK, netip.Prefix{}, gateway)
	if err := routeRequest(msg); err != nil {
		return fmt.Errorf("route the default route through %s: %w", gateway, err)
	}
	fmt.Fprintf(out, "sidecar: route %s\n", gateway)
	return nil
}

// As `sidecar address <address>...` the program joins a network sidecar's
// namespace and adds each address to its loopback, so the sidecar's
// listeners answer at the cloud's fixed link-local addresses too.
func serveAddress(out io.Writer, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sidecar address <IPv4 address> [<IPv4 address>]")
	}
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		return err
	}
	for _, arg := range args {
		addr, err := netip.ParseAddr(arg)
		if err != nil || !addr.Is4() {
			return fmt.Errorf("address %q: an IPv4 address is required", arg)
		}
		if err := routeRequest(addressMessage(lo.Index, addr)); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("add %s to the loopback: %w", addr, err)
		}
		fmt.Fprintf(out, "sidecar: address %s\n", addr)
	}
	return nil
}

// addressMessage is RTM_NEWADDR for addr/32 on the interface.
func addressMessage(index int, addr netip.Addr) []byte {
	ifa := make([]byte, unix.SizeofIfAddrmsg)
	ifa[0] = unix.AF_INET
	ifa[1] = 32
	ifa[3] = unix.RT_SCOPE_HOST
	binary.NativeEndian.PutUint32(ifa[4:], uint32(index))
	a := addr.As4()
	ifa = append(ifa, rtattr(unix.IFA_LOCAL, a[:])...)
	ifa = append(ifa, rtattr(unix.IFA_ADDRESS, a[:])...)
	return nlmsg(unix.RTM_NEWADDR, unix.NLM_F_REQUEST|unix.NLM_F_CREATE|unix.NLM_F_EXCL|unix.NLM_F_ACK, ifa)
}

// routeMessage is a request on an IPv4 route of the main table to
// destination (the default route when it is the zero prefix), through
// gateway when it is valid.
func routeMessage(typ, flags uint16, destination netip.Prefix, gateway netip.Addr) []byte {
	rt := make([]byte, unix.SizeofRtMsg)
	rt[0] = unix.AF_INET
	rt[4] = unix.RT_TABLE_MAIN
	rt[5] = unix.RTPROT_BOOT
	rt[6] = unix.RT_SCOPE_UNIVERSE
	rt[7] = unix.RTN_UNICAST
	if destination.IsValid() {
		rt[1] = byte(destination.Bits())
		d := destination.Addr().As4()
		rt = append(rt, rtattr(unix.RTA_DST, d[:])...)
	}
	if gateway.IsValid() {
		g := gateway.As4()
		rt = append(rt, rtattr(unix.RTA_GATEWAY, g[:])...)
	}
	return nlmsg(typ, flags, rt)
}

func rtattr(typ uint16, data []byte) []byte {
	length := unix.SizeofRtAttr + len(data)
	b := make([]byte, (length+3)&^3)
	binary.NativeEndian.PutUint16(b, uint16(length))
	binary.NativeEndian.PutUint16(b[2:], typ)
	copy(b[unix.SizeofRtAttr:], data)
	return b
}

func nlmsg(typ, flags uint16, payload []byte) []byte {
	msg := make([]byte, unix.NLMSG_HDRLEN, unix.NLMSG_HDRLEN+len(payload))
	binary.NativeEndian.PutUint32(msg, uint32(unix.NLMSG_HDRLEN+len(payload)))
	binary.NativeEndian.PutUint16(msg[4:], typ)
	binary.NativeEndian.PutUint16(msg[6:], flags)
	binary.NativeEndian.PutUint32(msg[8:], 1)
	return append(msg, payload...)
}

// routeRequest sends one rtnetlink request and reads its acknowledgement.
func routeRequest(msg []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return err
	}
	replies, err := syscall.ParseNetlinkMessage(buf[:n])
	if err != nil {
		return err
	}
	for _, reply := range replies {
		if reply.Header.Type == unix.NLMSG_ERROR && len(reply.Data) >= 4 {
			if code := int32(binary.NativeEndian.Uint32(reply.Data[:4])); code != 0 {
				return unix.Errno(-code)
			}
		}
	}
	return nil
}
