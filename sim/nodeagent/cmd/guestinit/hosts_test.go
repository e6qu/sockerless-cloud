package main

import "testing"

func TestHostsFileCarriesTheGuestAndItsEntries(t *testing.T) {
	got := hostsFile(guestConfig{
		Hostname: "ip-10-0-1-7",
		Address:  "10.0.1.7",
		Hosts:    []string{"metadata.google.internal:10.0.1.2", "malformed", "host.docker.internal:10.0.1.1"},
	})
	want := "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n" +
		"10.0.1.7\tip-10-0-1-7\n10.0.1.2\tmetadata.google.internal\n10.0.1.1\thost.docker.internal\n"
	if got != want {
		t.Fatalf("hosts file:\n%s\nwant:\n%s", got, want)
	}
}
