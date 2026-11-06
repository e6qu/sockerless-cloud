package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// As `sidecar route <gateway>` the program joins a workload's network
// namespace for one change and exits: it points the namespace's IPv4 default
// route at the gateway, the next hop the cloud's route table names, or
// removes the default route for `none`, a subnet whose routing names no way
// out. It holds NET_ADMIN and nothing else.
func serveRoute(out io.Writer, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sidecar route <gateway>|none")
	}
	if args[0] == "none" {
		err := routeRequest(routeMessage(unix.RTM_DELROUTE, unix.NLM_F_REQUEST|unix.NLM_F_ACK, netip.Addr{}))
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
	msg := routeMessage(unix.RTM_NEWROUTE, unix.NLM_F_REQUEST|unix.NLM_F_CREATE|unix.NLM_F_REPLACE|unix.NLM_F_ACK, gateway)
	if err := routeRequest(msg); err != nil {
		return fmt.Errorf("route the default route through %s: %w", gateway, err)
	}
	fmt.Fprintf(out, "sidecar: route %s\n", gateway)
	return nil
}

// routeMessage is a request on the IPv4 default route of the main table,
// through gateway when it is valid.
func routeMessage(typ, flags uint16, gateway netip.Addr) []byte {
	rt := make([]byte, unix.SizeofRtMsg)
	rt[0] = unix.AF_INET
	rt[4] = unix.RT_TABLE_MAIN
	rt[5] = unix.RTPROT_BOOT
	rt[6] = unix.RT_SCOPE_UNIVERSE
	rt[7] = unix.RTN_UNICAST
	if gateway.IsValid() {
		addr := gateway.As4()
		attr := make([]byte, unix.SizeofRtAttr+4)
		binary.NativeEndian.PutUint16(attr, uint16(len(attr)))
		binary.NativeEndian.PutUint16(attr[2:], unix.RTA_GATEWAY)
		copy(attr[unix.SizeofRtAttr:], addr[:])
		rt = append(rt, attr...)
	}
	msg := make([]byte, unix.NLMSG_HDRLEN, unix.NLMSG_HDRLEN+len(rt))
	binary.NativeEndian.PutUint32(msg, uint32(unix.NLMSG_HDRLEN+len(rt)))
	binary.NativeEndian.PutUint16(msg[4:], typ)
	binary.NativeEndian.PutUint16(msg[6:], flags)
	binary.NativeEndian.PutUint32(msg[8:], 1)
	return append(msg, rt...)
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
