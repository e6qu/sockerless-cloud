package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	"github.com/miekg/dns"
)

// azureDNSRegisteredNames holds per-name A records service slices register —
// a PostgreSQL flexible server's fullyQualifiedDomainName pointing at the
// loopback listener its data plane owns. A registered name answers ahead of
// every zone: it is the coordinate the control plane advertised.
var azureDNSRegisteredNames sync.Map // fqdn (lowercase, no trailing dot) -> ipv4 string

// RegisterAzureDNSName maps a fully-qualified name to the IPv4 address the
// simulator serves it at. Replaces any earlier registration for the name.
func RegisterAzureDNSName(fqdn string, ipv4 string) {
	azureDNSRegisteredNames.Store(strings.Trim(strings.ToLower(fqdn), "."), ipv4)
}

// UnregisterAzureDNSName removes a name's registration.
func UnregisterAzureDNSName(fqdn string) {
	azureDNSRegisteredNames.Delete(strings.Trim(strings.ToLower(fqdn), "."))
}

const (
	defaultAzureDNSZones = "localhost,sockerless.azure.local"
	defaultAzureDNSTTL   = 60
)

// azureDNSConfig configures the DNS listener. Zones are the custom cloud's
// endpoint suffixes: every host under one — a storage account's blob
// endpoint, a Service Bus namespace — is the simulator, as every host under
// core.windows.net is an Azure front end.
type azureDNSConfig struct {
	ListenAddr string
	TargetIPv4 net.IP
	TargetIPv6 net.IP
	Zones      []string
	TTL        uint32
	// Upstreams recurse for names outside every zone (SIM_DNS_UPSTREAM);
	// empty reads the host's resolv.conf.
	Upstreams string
}

func azureDNSConfigFromEnv() (azureDNSConfig, error) {
	listenAddr := strings.TrimSpace(os.Getenv("SIM_AZURE_DNS_LISTEN_ADDR"))

	targetIPv4 := net.ParseIP(strings.TrimSpace(sim.EnvOr("SIM_AZURE_DNS_TARGET_IPV4", "127.0.0.1"))).To4()
	if targetIPv4 == nil {
		return azureDNSConfig{}, fmt.Errorf("SIM_AZURE_DNS_TARGET_IPV4 must be an IPv4 address")
	}

	var targetIPv6 net.IP
	if raw := strings.TrimSpace(os.Getenv("SIM_AZURE_DNS_TARGET_IPV6")); raw != "" {
		targetIPv6 = net.ParseIP(raw)
		if targetIPv6 == nil || targetIPv6.To4() != nil {
			return azureDNSConfig{}, fmt.Errorf("SIM_AZURE_DNS_TARGET_IPV6 must be an IPv6 address")
		}
	}

	zones, err := parseAzureDNSZones(sim.EnvOr("SIM_AZURE_DNS_ZONES", defaultAzureDNSZones))
	if err != nil {
		return azureDNSConfig{}, err
	}

	ttl := uint32(defaultAzureDNSTTL)
	if raw := strings.TrimSpace(os.Getenv("SIM_AZURE_DNS_TTL")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return azureDNSConfig{}, fmt.Errorf("SIM_AZURE_DNS_TTL must be a uint32: %w", err)
		}
		ttl = uint32(parsed)
	}

	return azureDNSConfig{
		ListenAddr: listenAddr,
		TargetIPv4: targetIPv4,
		TargetIPv6: targetIPv6,
		Zones:      zones,
		TTL:        ttl,
		Upstreams:  os.Getenv("SIM_DNS_UPSTREAM"),
	}, nil
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

// networkSidecars runs one sidecar per engine network workloads land on,
// forwarding DNS to azureDNS and metadata to the simulator.
var networkSidecars *sidecar.Manager

// azureDNS is the listener the endpoint suffixes, Azure DNS and Azure Private
// DNS answer on, and the resolver a virtual network's machines ask at
// 168.63.129.16.
var azureDNS *dnsserver.Server

// startAzureDNSFromEnv starts the resolver a virtual network's queries are
// redirected to, on every address so the redirect can reach it, and the
// listener an operator configured at SIM_AZURE_DNS_LISTEN_ADDR. Both answer
// from the same catalog.
func startAzureDNSFromEnv() ([]*dnsserver.Server, error) {
	cfg, err := azureDNSConfigFromEnv()
	if err != nil {
		return nil, err
	}
	base, err := azureDNSServerConfig(cfg)
	if err != nil {
		return nil, err
	}
	resolver := base
	resolver.Port = os.Getenv("SIM_DNS_PORT")
	resolver.Name = "azure resolver"
	vnetSrv, err := dnsserver.Start(resolver)
	if err != nil {
		return nil, err
	}
	azureDNS = vnetSrv
	// An API-only simulator places no workload, so it runs no sidecar and
	// needs no callback address, which only a container engine can give.
	if sim.RequireContainerRuntime("starting network sidecars") == nil {
		origin, err := workloadhost.CallbackAddr(simListenAddr)
		if err != nil {
			_ = vnetSrv.Close()
			return nil, fmt.Errorf("workload callback: %w", err)
		}
		networkSidecars = sidecar.NewManager(azureDNS, "http://"+origin, sim.NoopSink{})
	}
	servers := []*dnsserver.Server{vnetSrv}
	if cfg.ListenAddr == "" {
		return servers, nil
	}
	operator, err := startAzureDNSServer(cfg)
	if err != nil {
		return nil, errors.Join(err, vnetSrv.Close())
	}
	return append(servers, operator), nil
}

// azureResolverIPv4 is Azure's recursive resolver and health-probe source,
// which every virtual network reaches at the same address; inside an engine
// network the sidecar stands in for it at the address Azure reserves in the
// subnet.
const azureResolverIPv4 = "168.63.129.16"

// azureQuerierVNet is the virtual network a query comes from: the one whose
// engine network's sidecar forwarded it, or, for a query at the operator's
// listener, the one whose address space holds the querier's address. It
// decides the private zones the query sees.
func azureQuerierVNet(q dnsserver.Query) string {
	if vnet := azureNetworkVNet(q.Network); vnet != "" {
		return vnet
	}
	return azureQuerierVNetByAddress(q.Remote)
}

func azureQuerierVNetByAddress(remote netip.Addr) string {
	if azureVnets == nil || !remote.IsValid() {
		return ""
	}
	remote = remote.Unmap()
	for _, vnet := range azureVnets.List() {
		for _, raw := range vnet.Properties.AddressSpace.AddressPrefixes {
			if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Contains(remote) {
				return strings.ToLower(vnet.ID)
			}
		}
	}
	return ""
}

func startAzureDNSServer(cfg azureDNSConfig) (*dnsserver.Server, error) {
	host, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("DNS listen address %q: %w", cfg.ListenAddr, err)
	}
	server, err := azureDNSServerConfig(cfg)
	if err != nil {
		return nil, err
	}
	server.Host, server.Port = host, port
	return dnsserver.Start(server)
}

// azureDNSServerConfig is the catalog and recursion every Azure listener
// answers with; the caller supplies where it listens.
func azureDNSServerConfig(cfg azureDNSConfig) (dnsserver.Config, error) {
	if cfg.TargetIPv4 == nil && cfg.TargetIPv6 == nil {
		return dnsserver.Config{}, fmt.Errorf("at least one DNS target IP is required")
	}
	if len(cfg.Zones) == 0 {
		return dnsserver.Config{}, fmt.Errorf("at least one DNS zone is required")
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultAzureDNSTTL
	}
	endpoints, err := azureEndpointZones(cfg)
	if err != nil {
		return dnsserver.Config{}, err
	}
	// A workload on an engine network reaches the simulator's front end at
	// the address its containers dial, not at the operator's target.
	workloadEndpoints := endpoints
	if host, err := workloadhost.CallbackHost(); err == nil {
		if ip := net.ParseIP(host).To4(); ip != nil && !ip.Equal(cfg.TargetIPv4) {
			workloadCfg := cfg
			workloadCfg.TargetIPv4 = ip
			if workloadEndpoints, err = azureEndpointZones(workloadCfg); err != nil {
				return dnsserver.Config{}, err
			}
		}
	}
	return dnsserver.Config{
		Name: "azure",
		Catalog: func(q dnsserver.Query) dnsserver.View {
			// A private zone linked to the querier's virtual network takes
			// precedence over a public zone of the same name.
			zones := append(azurePrivateDNSCatalogZones(azureQuerierVNet(q)), azurePublicDNSCatalogZones()...)
			if q.Network != "" {
				zones = append(zones, workloadEndpoints...)
			} else {
				zones = append(zones, endpoints...)
			}
			return dnsserver.View{Names: azureRegisteredNameRecords(cfg.TTL), Zones: zones}
		},
		Recursion: &dnsserver.Recursion{
			Upstreams: cfg.Upstreams,
		},
	}, nil
}

// azureEndpointZones serves each endpoint suffix: the suffix itself and every
// name under it answer with the simulator's address.
func azureEndpointZones(cfg azureDNSConfig) ([]dnsserver.Zone, error) {
	var zones []dnsserver.Zone
	for _, suffix := range cfg.Zones {
		apex := dns.Fqdn(suffix)
		soa, err := dns.NewRR(fmt.Sprintf("%s %d IN SOA ns.%s hostmaster.%s 1 3600 300 2419200 %d", apex, cfg.TTL, apex, apex, cfg.TTL))
		if err != nil {
			return nil, fmt.Errorf("endpoint zone %s: %w", suffix, err)
		}
		records := []dns.RR{soa}
		for _, owner := range []string{apex, "*." + apex} {
			hdr := func(t uint16) dns.RR_Header {
				return dns.RR_Header{Name: owner, Rrtype: t, Class: dns.ClassINET, Ttl: cfg.TTL}
			}
			if cfg.TargetIPv4 != nil {
				records = append(records, &dns.A{Hdr: hdr(dns.TypeA), A: cfg.TargetIPv4})
			}
			if cfg.TargetIPv6 != nil {
				records = append(records, &dns.AAAA{Hdr: hdr(dns.TypeAAAA), AAAA: cfg.TargetIPv6})
			}
		}
		zones = append(zones, dnsserver.Zone{Apex: apex, Records: records})
	}
	return zones, nil
}

func azureRegisteredNameRecords(ttl uint32) []dns.RR {
	var out []dns.RR
	azureDNSRegisteredNames.Range(func(key, value any) bool {
		name, _ := key.(string)
		address, _ := value.(string)
		if ip := net.ParseIP(address).To4(); ip != nil {
			out = append(out, &dns.A{Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl}, A: ip})
		}
		return true
	})
	return out
}

// azurePublicDNSCatalogZones serves every Azure DNS public zone: Azure's name
// servers answer them for any querier.
func azurePublicDNSCatalogZones() []dnsserver.Zone {
	if azurePublicDNSZones == nil {
		return nil
	}
	var zones []dnsserver.Zone
	for _, zone := range azurePublicDNSZones.List() {
		if strings.EqualFold(zone.Properties.ZoneType, "Private") {
			continue
		}
		apex := dns.CanonicalName(zone.Name)
		var records []dns.RR
		for _, set := range azurePublicDNSRecordSets.ListPrefix(zone.ID + "/") {
			records = append(records, azurePublicRecordSetRRs(apex, set.Item)...)
		}
		zones = append(zones, dnsserver.Zone{Apex: apex, Records: records})
	}
	return zones
}

// azurePrivateDNSCatalogZones serves the Azure Private DNS zones linked to
// vnetID: a private zone answers only the virtual networks it is linked to.
func azurePrivateDNSCatalogZones(vnetID string) []dnsserver.Zone {
	if vnetID == "" || azurePrivateDNSZones == nil {
		return nil
	}
	linked := map[string]bool{}
	for _, link := range azurePrivateDNSVNetLinks.List() {
		if link.Properties.VirtualNetwork == nil || !strings.EqualFold(link.Properties.VirtualNetwork.ID, vnetID) {
			continue
		}
		if zoneID, _, ok := strings.Cut(link.ID, "/virtualNetworkLinks/"); ok {
			linked[strings.ToLower(zoneID)] = true
		}
	}
	var zones []dnsserver.Zone
	for _, zone := range azurePrivateDNSZones.List() {
		if !linked[strings.ToLower(zone.ID)] {
			continue
		}
		apex := dns.CanonicalName(zone.Name)
		prefix := strings.ToLower(zone.ID) + "/"
		var records []dns.RR
		for _, set := range azurePrivateDNSRecordSets.List() {
			if strings.HasPrefix(strings.ToLower(set.ID), prefix) {
				records = append(records, azurePrivateRecordSetRRs(apex, set)...)
			}
		}
		zones = append(zones, dnsserver.Zone{Apex: apex, Records: records})
	}
	return zones
}

func azurePrivateRecordSetRRs(apex string, set RecordSet) []dns.RR {
	p := set.Properties
	public := PublicRecordSet{Name: set.Name, Properties: PublicRecordSetProperties{
		TTL:         int64(p.TTL),
		ARecords:    p.ARecords,
		AAAARecords: p.AAAARecords,
		CNAMERecord: p.CNAMERecord,
		MXRecords:   p.MXRecords,
		PTRRecords:  p.PTRRecords,
		SRVRecords:  p.SRVRecords,
		TXTRecords:  p.TXTRecords,
	}}
	if soa := p.SOARecord; soa != nil {
		public.Properties.SOARecord = &PublicSOARecord{
			Host: soa.Host, Email: soa.Email, SerialNumber: soa.SerialNumber,
			RefreshTime: soa.RefreshTime, RetryTime: soa.RetryTime, ExpireTime: soa.ExpireTime, MinimumTTL: soa.MinimumTTL,
		}
	}
	return azurePublicRecordSetRRs(apex, public)
}

func azurePublicRecordSetRRs(apex string, set PublicRecordSet) []dns.RR {
	owner := apex
	if set.Name != "@" {
		owner = dns.CanonicalName(set.Name + "." + apex)
	}
	p := set.Properties
	var values []string
	for _, r := range p.ARecords {
		values = append(values, "A "+r.IPv4Address)
	}
	for _, r := range p.AAAARecords {
		values = append(values, "AAAA "+r.IPv6Address)
	}
	for _, r := range p.CAARecords {
		values = append(values, fmt.Sprintf("CAA %d %s %q", r.Flags, r.Tag, r.Value))
	}
	if p.CNAMERecord != nil {
		values = append(values, "CNAME "+dns.Fqdn(p.CNAMERecord.CName))
	}
	for _, r := range p.MXRecords {
		values = append(values, fmt.Sprintf("MX %d %s", r.Preference, dns.Fqdn(r.Exchange)))
	}
	for _, r := range p.NSRecords {
		values = append(values, "NS "+dns.Fqdn(r.NSDName))
	}
	for _, r := range p.PTRRecords {
		values = append(values, "PTR "+dns.Fqdn(r.PTRDName))
	}
	for _, r := range p.SRVRecords {
		values = append(values, fmt.Sprintf("SRV %d %d %d %s", r.Priority, r.Weight, r.Port, dns.Fqdn(r.Target)))
	}
	for _, r := range p.TXTRecords {
		quoted := make([]string, 0, len(r.Value))
		for _, chunk := range r.Value {
			quoted = append(quoted, strconv.Quote(chunk))
		}
		values = append(values, "TXT "+strings.Join(quoted, " "))
	}
	if s := p.SOARecord; s != nil {
		values = append(values, fmt.Sprintf("SOA %s %s %d %d %d %d %d",
			dns.Fqdn(s.Host), dns.Fqdn(strings.Replace(s.Email, "@", ".", 1)), s.SerialNumber, s.RefreshTime, s.RetryTime, s.ExpireTime, s.MinimumTTL))
	}
	var out []dns.RR
	for _, v := range values {
		rr, err := dns.NewRR(fmt.Sprintf("%s %d IN %s", owner, p.TTL, v))
		if err != nil || rr == nil {
			log.Printf("azure dns: serve %s %q: %v", owner, v, err)
			continue
		}
		out = append(out, rr)
	}
	return out
}
