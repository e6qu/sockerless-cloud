package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestAzureDNSServerAnswersConfiguredZones(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, err := startAzureDNSServer(ctx, azureDNSConfig{
		ListenAddr: "127.0.0.1:0",
		TargetIPv4: net.ParseIP("127.0.0.1"),
		TargetIPv6: net.ParseIP("::1"),
		Zones:      []string{"sockerless.azure.local", "localhost"},
		TTL:        30,
	})
	if err != nil {
		t.Fatalf("start DNS server: %v", err)
	}

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn("account.blob.sockerless.azure.local"), dns.TypeA)
	resp, _, err := (&dns.Client{Net: "udp", Timeout: time.Second}).Exchange(msg, addr)
	if err != nil {
		t.Fatalf("query A over UDP: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("A query rcode = %s", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("A answer count = %d", len(resp.Answer))
	}
	if got := resp.Answer[0].(*dns.A).A.String(); got != "127.0.0.1" {
		t.Fatalf("A answer = %s", got)
	}

	msg = new(dns.Msg)
	msg.SetQuestion(dns.Fqdn("namespace.servicebus.localhost"), dns.TypeAAAA)
	resp, _, err = (&dns.Client{Net: "tcp", Timeout: time.Second}).Exchange(msg, addr)
	if err != nil {
		t.Fatalf("query AAAA over TCP: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("AAAA query rcode = %s", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("AAAA answer count = %d", len(resp.Answer))
	}
	if got := resp.Answer[0].(*dns.AAAA).AAAA.String(); got != "::1" {
		t.Fatalf("AAAA answer = %s", got)
	}
}

func TestAzureDNSServerReturnsNXDOMAINOutsideConfiguredZones(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, err := startAzureDNSServer(ctx, azureDNSConfig{
		ListenAddr: "127.0.0.1:0",
		TargetIPv4: net.ParseIP("127.0.0.1"),
		Zones:      []string{"sockerless.azure.local"},
		TTL:        30,
	})
	if err != nil {
		t.Fatalf("start DNS server: %v", err)
	}

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn("account.blob.example.com"), dns.TypeA)
	resp, _, err := (&dns.Client{Net: "udp", Timeout: time.Second}).Exchange(msg, addr)
	if err != nil {
		t.Fatalf("query outside zone: %v", err)
	}
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("outside-zone rcode = %s", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 0 {
		t.Fatalf("outside-zone answer count = %d", len(resp.Answer))
	}
}

func TestParseAzureDNSZonesRejectsEmptyConfig(t *testing.T) {
	if _, err := parseAzureDNSZones(" , "); err == nil {
		t.Fatal("expected empty zone config to fail")
	}
}
