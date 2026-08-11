package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const (
	defaultAzureDNSZones = "localhost,sockerless.azure.local"
	defaultAzureDNSTTL   = 60
)

type azureDNSConfig struct {
	ListenAddr string
	TargetIPv4 net.IP
	TargetIPv6 net.IP
	Zones      []string
	TTL        uint32
}

func azureDNSConfigFromEnv() (azureDNSConfig, bool, error) {
	listenAddr := strings.TrimSpace(os.Getenv("SIM_AZURE_DNS_LISTEN_ADDR"))
	if listenAddr == "" {
		return azureDNSConfig{}, false, nil
	}

	targetIPv4 := net.ParseIP(strings.TrimSpace(envOrDefault("SIM_AZURE_DNS_TARGET_IPV4", "127.0.0.1"))).To4()
	if targetIPv4 == nil {
		return azureDNSConfig{}, false, fmt.Errorf("SIM_AZURE_DNS_TARGET_IPV4 must be an IPv4 address")
	}

	var targetIPv6 net.IP
	if raw := strings.TrimSpace(os.Getenv("SIM_AZURE_DNS_TARGET_IPV6")); raw != "" {
		targetIPv6 = net.ParseIP(raw)
		if targetIPv6 == nil || targetIPv6.To4() != nil {
			return azureDNSConfig{}, false, fmt.Errorf("SIM_AZURE_DNS_TARGET_IPV6 must be an IPv6 address")
		}
	}

	zones, err := parseAzureDNSZones(envOrDefault("SIM_AZURE_DNS_ZONES", defaultAzureDNSZones))
	if err != nil {
		return azureDNSConfig{}, false, err
	}

	ttl := uint32(defaultAzureDNSTTL)
	if raw := strings.TrimSpace(os.Getenv("SIM_AZURE_DNS_TTL")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return azureDNSConfig{}, false, fmt.Errorf("SIM_AZURE_DNS_TTL must be a uint32: %w", err)
		}
		ttl = uint32(parsed)
	}

	return azureDNSConfig{
		ListenAddr: listenAddr,
		TargetIPv4: targetIPv4,
		TargetIPv6: targetIPv6,
		Zones:      zones,
		TTL:        ttl,
	}, true, nil
}

func parseAzureDNSZones(raw string) ([]string, error) {
	var zones []string
	for _, part := range strings.Split(raw, ",") {
		zone := strings.Trim(strings.ToLower(strings.TrimSpace(part)), ".")
		if zone == "" {
			continue
		}
		if _, ok := dns.IsDomainName(dns.Fqdn(zone)); !ok {
			return nil, fmt.Errorf("invalid DNS zone %q", part)
		}
		zones = append(zones, zone)
	}
	if len(zones) == 0 {
		return nil, fmt.Errorf("SIM_AZURE_DNS_ZONES must include at least one zone")
	}
	return zones, nil
}

func startAzureDNSFromEnv(ctx context.Context) (string, bool, error) {
	cfg, enabled, err := azureDNSConfigFromEnv()
	if err != nil || !enabled {
		return "", enabled, err
	}
	addr, err := startAzureDNSServer(ctx, cfg)
	return addr, true, err
}

func startAzureDNSServer(ctx context.Context, cfg azureDNSConfig) (string, error) {
	if cfg.ListenAddr == "" {
		return "", fmt.Errorf("DNS listen address is required")
	}
	if cfg.TargetIPv4 == nil && cfg.TargetIPv6 == nil {
		return "", fmt.Errorf("at least one DNS target IP is required")
	}
	if len(cfg.Zones) == 0 {
		return "", fmt.Errorf("at least one DNS zone is required")
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultAzureDNSTTL
	}

	packet, listener, actualAddr, err := listenAzureDNS(cfg.ListenAddr)
	if err != nil {
		return "", err
	}

	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		handleAzureDNSQuery(w, r, cfg)
	})

	udpServer := &dns.Server{PacketConn: packet, Handler: mux}
	tcpServer := &dns.Server{Listener: listener, Handler: mux}

	errs := make(chan error, 2)
	go func() {
		if err := udpServer.ActivateAndServe(); err != nil && ctx.Err() == nil {
			errs <- fmt.Errorf("serve UDP DNS: %w", err)
		}
	}()
	go func() {
		if err := tcpServer.ActivateAndServe(); err != nil && ctx.Err() == nil {
			errs <- fmt.Errorf("serve TCP DNS: %w", err)
		}
	}()

	select {
	case err := <-errs:
		_ = udpServer.Shutdown()
		_ = tcpServer.Shutdown()
		return "", err
	case <-time.After(50 * time.Millisecond):
	}

	go func() {
		<-ctx.Done()
		_ = udpServer.Shutdown()
		_ = tcpServer.Shutdown()
	}()

	return actualAddr, nil
}

func listenAzureDNS(listenAddr string) (net.PacketConn, net.Listener, string, error) {
	_, requestedPort, splitErr := net.SplitHostPort(listenAddr)
	dynamicPort := splitErr == nil && requestedPort == "0"
	const dynamicPortAttempts = 16
	var lastTCPError error
	for attempt := 0; attempt < dynamicPortAttempts; attempt++ {
		packet, err := net.ListenPacket("udp", listenAddr)
		if err != nil {
			return nil, nil, "", fmt.Errorf("listen UDP DNS %s: %w", listenAddr, err)
		}
		actualAddr := packet.LocalAddr().String()
		tcpAddr := actualAddr
		if host, port, err := net.SplitHostPort(actualAddr); err == nil {
			if parsed := net.ParseIP(host); parsed != nil && parsed.IsUnspecified() {
				tcpAddr = net.JoinHostPort("", port)
			}
		}
		listener, err := net.Listen("tcp", tcpAddr)
		if err == nil {
			return packet, listener, actualAddr, nil
		}
		lastTCPError = err
		_ = packet.Close()
		if !dynamicPort {
			return nil, nil, "", fmt.Errorf("listen TCP DNS %s: %w", tcpAddr, err)
		}
	}
	return nil, nil, "", fmt.Errorf(
		"listen TCP and UDP DNS %s: no shared port available after %d attempts: %w",
		listenAddr,
		dynamicPortAttempts,
		lastTCPError,
	)
}

func handleAzureDNSQuery(w dns.ResponseWriter, r *dns.Msg, cfg azureDNSConfig) {
	resp := new(dns.Msg)
	resp.SetReply(r)
	resp.Authoritative = true

	for _, q := range r.Question {
		name := strings.Trim(strings.ToLower(q.Name), ".")
		if !azureDNSMatchesZone(name, cfg.Zones) {
			resp.Rcode = dns.RcodeNameError
			continue
		}
		header := dns.RR_Header{
			Name:   q.Name,
			Class:  dns.ClassINET,
			Ttl:    cfg.TTL,
			Rrtype: q.Qtype,
		}
		switch q.Qtype {
		case dns.TypeA:
			if cfg.TargetIPv4 != nil {
				resp.Answer = append(resp.Answer, &dns.A{Hdr: header, A: cfg.TargetIPv4})
			}
		case dns.TypeAAAA:
			if cfg.TargetIPv6 != nil {
				resp.Answer = append(resp.Answer, &dns.AAAA{Hdr: header, AAAA: cfg.TargetIPv6})
			}
		}
	}

	if err := w.WriteMsg(resp); err != nil {
		log.Printf("write Azure DNS response: %v", err)
	}
}

func azureDNSMatchesZone(name string, zones []string) bool {
	for _, zone := range zones {
		if name == zone || strings.HasSuffix(name, "."+zone) {
			return true
		}
	}
	return false
}
