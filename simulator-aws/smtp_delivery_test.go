package main

import (
	"net"
	"reflect"
	"testing"
)

func TestSMTPRelayHostsHonoursNullMX(t *testing.T) {
	t.Parallel()
	if _, err := smtpRelayHosts("example.com", []*net.MX{{Host: ".", Pref: 0}}); err == nil {
		t.Fatal("a null MX record must refuse delivery, not dial the local host")
	}
	got, err := smtpRelayHosts("example.test", []*net.MX{{Host: "mx2.example.test.", Pref: 20}, {Host: "mx1.example.test.", Pref: 10}})
	if err != nil || !reflect.DeepEqual(got, []string{"mx1.example.test", "mx2.example.test"}) {
		t.Fatalf("MX hosts = %v, %v; want them by preference", got, err)
	}
	got, err = smtpRelayHosts("localhost", nil)
	if err != nil || !reflect.DeepEqual(got, []string{"localhost"}) {
		t.Fatalf("implicit MX = %v, %v; want the domain itself", got, err)
	}
}
