package main

import (
	"encoding/binary"
	"net/netip"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNetlinkMessagesCarryTheAddressAndTheLinkFlags(t *testing.T) {
	msg := newAddrMessage(3, netip.MustParsePrefix("10.62.1.20/24"))
	parsed, err := syscall.ParseNetlinkMessage(msg)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("parse: %v (%d messages)", err, len(parsed))
	}
	if parsed[0].Header.Type != unix.RTM_NEWADDR || parsed[0].Header.Flags&unix.NLM_F_ACK == 0 {
		t.Fatalf("header %+v", parsed[0].Header)
	}
	data := parsed[0].Data
	if data[0] != unix.AF_INET || data[1] != 24 || binary.NativeEndian.Uint32(data[4:]) != 3 {
		t.Fatalf("ifaddrmsg %v", data[:unix.SizeofIfAddrmsg])
	}
	attrs, err := syscall.ParseNetlinkRouteAttr(&parsed[0])
	if err != nil || len(attrs) != 2 {
		t.Fatalf("attributes: %v (%d)", err, len(attrs))
	}
	for _, a := range attrs {
		if string(a.Value) != string([]byte{10, 62, 1, 20}) || (a.Attr.Type != unix.IFA_LOCAL && a.Attr.Type != unix.IFA_ADDRESS) {
			t.Fatalf("attribute %+v", a)
		}
	}
	up := linkUpMessage(3)
	parsed, err = syscall.ParseNetlinkMessage(up)
	if err != nil || len(parsed) != 1 || parsed[0].Header.Type != unix.RTM_NEWLINK {
		t.Fatalf("link up: %v %+v", err, parsed)
	}
	info := parsed[0].Data
	if binary.NativeEndian.Uint32(info[4:]) != 3 || binary.NativeEndian.Uint32(info[8:])&unix.IFF_UP == 0 || binary.NativeEndian.Uint32(info[12:])&unix.IFF_UP == 0 {
		t.Fatalf("ifinfomsg %v", info)
	}
}
