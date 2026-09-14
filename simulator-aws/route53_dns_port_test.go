package main

import "testing"

func TestRoute53DNSBindPortDefaultsToAKernelChosenPort(t *testing.T) {
	t.Setenv("SIM_DNS_PORT", "")
	if got := route53DNSBindPort(); got != "0" {
		t.Fatalf("unset SIM_DNS_PORT resolved to %q; want the kernel-chosen port \"0\", not mDNS's 5353", got)
	}
	t.Setenv("SIM_DNS_PORT", "15353")
	if got := route53DNSBindPort(); got != "15353" {
		t.Fatalf("SIM_DNS_PORT=15353 resolved to %q", got)
	}
}
