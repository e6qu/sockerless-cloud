package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// guestInterface is a further interface of the guest, eth1 onwards, with
// the address it takes; the kernel's command line configured eth0.
type guestInterface struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// configureInterfaces gives each further interface its address and brings
// it up over rtnetlink, with no route out through it: the guest's default
// route stays on eth0, as a container's stays on its first network.
func configureInterfaces(ifaces []guestInterface) error {
	for _, i := range ifaces {
		prefix, err := netip.ParsePrefix(i.Address)
		if err != nil || !prefix.Addr().Is4() {
			return fmt.Errorf("interface %s: address %q: an IPv4 prefix is required", i.Name, i.Address)
		}
		link, err := net.InterfaceByName(i.Name)
		if err != nil {
			return fmt.Errorf("interface %s: %w", i.Name, err)
		}
		if err := rtnetlink(newAddrMessage(link.Index, prefix)); err != nil {
			return fmt.Errorf("interface %s: add %s: %w", i.Name, prefix, err)
		}
		if err := rtnetlink(linkUpMessage(link.Index)); err != nil {
			return fmt.Errorf("interface %s: up: %w", i.Name, err)
		}
	}
	return nil
}

// rtnetlink sends one request and reads its acknowledgement.
func rtnetlink(msg []byte) error {
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
	msgs, err := syscall.ParseNetlinkMessage(buf[:n])
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if m.Header.Type == unix.NLMSG_ERROR && len(m.Data) >= 4 {
			if code := int32(binary.NativeEndian.Uint32(m.Data[:4])); code != 0 {
				return unix.Errno(-code)
			}
		}
	}
	return nil
}

// newAddrMessage is RTM_NEWADDR for an IPv4 prefix on the interface.
func newAddrMessage(index int, prefix netip.Prefix) []byte {
	addr := prefix.Addr().As4()
	payload := make([]byte, unix.SizeofIfAddrmsg)
	payload[0] = unix.AF_INET
	payload[1] = byte(prefix.Bits())
	payload[3] = unix.RT_SCOPE_UNIVERSE
	binary.NativeEndian.PutUint32(payload[4:], uint32(index))
	payload = append(payload, rtattr(unix.IFA_LOCAL, addr[:])...)
	payload = append(payload, rtattr(unix.IFA_ADDRESS, addr[:])...)
	return nlmsg(unix.RTM_NEWADDR, unix.NLM_F_REQUEST|unix.NLM_F_CREATE|unix.NLM_F_EXCL|unix.NLM_F_ACK, payload)
}

// linkUpMessage is RTM_NEWLINK setting the interface's IFF_UP.
func linkUpMessage(index int) []byte {
	payload := make([]byte, unix.SizeofIfInfomsg)
	payload[0] = unix.AF_UNSPEC
	binary.NativeEndian.PutUint32(payload[4:], uint32(index))
	binary.NativeEndian.PutUint32(payload[8:], unix.IFF_UP)
	binary.NativeEndian.PutUint32(payload[12:], unix.IFF_UP)
	return nlmsg(unix.RTM_NEWLINK, unix.NLM_F_REQUEST|unix.NLM_F_ACK, payload)
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
	b := make([]byte, unix.NLMSG_HDRLEN, unix.NLMSG_HDRLEN+len(payload))
	binary.NativeEndian.PutUint32(b, uint32(unix.NLMSG_HDRLEN+len(payload)))
	binary.NativeEndian.PutUint16(b[4:], typ)
	binary.NativeEndian.PutUint16(b[6:], flags)
	binary.NativeEndian.PutUint32(b[8:], 1)
	return append(b, payload...)
}
