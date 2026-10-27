package main

import (
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// cmServicePrincipal is the LinkedService principal of the hosted zones Cloud
// Map creates.
const cmServicePrincipal = "servicediscovery.amazonaws.com"

// A private zone is associated with the namespace's VPC.
func cmCreateHostedZone(ns CMNamespace, vpcID string, soa *cmSOAProperties) *CMDnsProperties {
	id := r53RandomID()
	name := r53NormalizeName(ns.Name)
	records := r53ApexRecords(name)
	dns := &CMDnsProperties{HostedZoneId: id}
	if soa != nil && soa.DnsProperties != nil && soa.DnsProperties.SOA != nil {
		dns.SOA = &struct {
			TTL int64 `json:"TTL"`
		}{TTL: soa.DnsProperties.SOA.TTL}
		cmSetSOATTL(records, soa.DnsProperties.SOA.TTL)
	}
	r53Zones.Put(id, r53StoredZone{
		Zone: R53HostedZone{
			Xmlns:                  r53Namespace,
			Id:                     "/hostedzone/" + id,
			Name:                   name,
			CallerReference:        sim.NewUUID(),
			Config:                 &R53HostedZoneConfig{PrivateZone: vpcID != ""},
			ResourceRecordSetCount: len(records),
			LinkedService:          &R53LinkedService{ServicePrincipal: cmServicePrincipal, Description: ns.Arn},
		},
		Records: records,
	})
	if vpcID != "" {
		r53ZoneVPCs.Put(id, []R53VPC{{VPCId: vpcID, VPCRegion: awsRegion()}})
	}
	return dns
}

// Resolvers use the SOA TTL for negative caching.
func cmSetSOATTL(records []R53ResourceRecordSet, ttl int64) {
	for i := range records {
		if records[i].Type == "SOA" {
			records[i].TTL = ptrInt64(ttl)
		}
	}
}

func cmHostedZoneID(ns CMNamespace) string {
	if ns.Properties == nil || ns.Properties.DnsProperties == nil {
		return ""
	}
	return ns.Properties.DnsProperties.HostedZoneId
}

// cmDeleteHostedZone deletes the hosted zone a namespace owns along with
// everything Route 53 keeps for it.
func cmDeleteHostedZone(ns CMNamespace) {
	id := cmHostedZoneID(ns)
	if id == "" {
		return
	}
	r53Zones.Delete(id)
	r53Tags.Delete(r53TagKey("hostedzone", id))
	r53ZoneVPCs.Delete(id)
	r53VPCAuthz.Delete(id)
	r53DNSSEC.Delete(id)
}

// Leave the apex NS and SOA as they are.
func cmSyncNamespaceRecords(namespaceID string) {
	ns, ok := cmNamespaces.Get(namespaceID)
	if !ok {
		return
	}
	zoneID := cmHostedZoneID(ns)
	if zoneID == "" {
		return
	}
	var records []R53ResourceRecordSet
	for _, svc := range cmServices.List() {
		if svc.NamespaceId == namespaceID {
			records = append(records, cmServiceRecords(ns, svc)...)
		}
	}
	r53Mu.Lock()
	defer r53Mu.Unlock()
	r53Zones.Update(zoneID, func(stored *r53StoredZone) {
		apex := r53NormalizeName(ns.Name)
		var kept []R53ResourceRecordSet
		for _, rr := range stored.Records {
			if (rr.Type == "NS" || rr.Type == "SOA") && r53NormalizeName(rr.Name) == apex {
				if rr.Type == "SOA" && ns.Properties.DnsProperties.SOA != nil {
					rr.TTL = ptrInt64(ns.Properties.DnsProperties.SOA.TTL)
				}
				kept = append(kept, rr)
			}
		}
		stored.Records = append(kept, records...)
		stored.Zone.ResourceRecordSetCount = len(stored.Records)
	})
}

// One record per instance and type under the service's name, with the
// instance ID as SetIdentifier, plus the A and AAAA records an SRV target
// names.
func cmServiceRecords(ns CMNamespace, svc CMService) []R53ResourceRecordSet {
	if svc.DnsConfig == nil || len(svc.DnsConfig.DnsRecords) == 0 || strings.EqualFold(svc.Type, "HTTP") {
		return nil
	}
	name := r53NormalizeName(svc.Name + "." + ns.Name)
	weighted := strings.EqualFold(svc.DnsConfig.RoutingPolicy, "WEIGHTED")
	instances := cmServiceInstances(svc.Id)
	sort.Slice(instances, func(i, j int) bool { return instances[i].Id < instances[j].Id })

	var out []R53ResourceRecordSet
	routed := func(rr R53ResourceRecordSet, inst CMInstance) R53ResourceRecordSet {
		rr.SetIdentifier = inst.Id
		if rr.AliasTarget == nil {
			rr.HealthCheckId = inst.HealthCheckId
		}
		if weighted {
			rr.Weight = ptrInt64(1)
		} else {
			yes := true
			rr.MultiValueAnswer = &yes
		}
		return rr
	}
	for _, inst := range instances {
		attrs := inst.Attributes
		if alias := attrs["AWS_ALIAS_DNS_NAME"]; alias != "" {
			for _, rec := range svc.DnsConfig.DnsRecords {
				if rec.Type == "A" || rec.Type == "AAAA" {
					out = append(out, routed(R53ResourceRecordSet{Name: name, Type: rec.Type, AliasTarget: &R53AliasTarget{
						HostedZoneId:         cmAliasHostedZoneID(alias),
						DNSName:              r53NormalizeName(alias),
						EvaluateTargetHealth: true,
					}}, inst))
				}
			}
			continue
		}
		for _, rec := range svc.DnsConfig.DnsRecords {
			value := ""
			switch rec.Type {
			case "A":
				value = attrs["AWS_INSTANCE_IPV4"]
			case "AAAA":
				value = attrs["AWS_INSTANCE_IPV6"]
			case "CNAME":
				value = attrs["AWS_INSTANCE_CNAME"]
			case "SRV":
				if port := attrs["AWS_INSTANCE_PORT"]; port != "" {
					value = "1 1 " + port + " " + inst.Id + "." + name
				}
			}
			if value == "" {
				continue
			}
			out = append(out, routed(cmRecordSet(name, rec.Type, rec.TTL, value), inst))
			if rec.Type == "SRV" {
				target := r53NormalizeName(inst.Id + "." + name)
				if v4 := attrs["AWS_INSTANCE_IPV4"]; v4 != "" {
					out = append(out, cmRecordSet(target, "A", rec.TTL, v4))
				}
				if v6 := attrs["AWS_INSTANCE_IPV6"]; v6 != "" {
					out = append(out, cmRecordSet(target, "AAAA", rec.TTL, v6))
				}
			}
		}
	}
	return out
}

func cmRecordSet(name, rtype string, ttl int64, value string) R53ResourceRecordSet {
	return R53ResourceRecordSet{
		Name:            name,
		Type:            rtype,
		TTL:             ptrInt64(ttl),
		ResourceRecords: &R53ResourceRecords{Items: []R53ResourceRecord{{Value: value}}},
	}
}

// The canonical hosted zone of the load balancer an alias names.
func cmAliasHostedZoneID(dnsName string) string {
	if lb, ok := elbv2LoadBalancerFromDataPlaneHost(strings.TrimSuffix(dnsName, ".")); ok {
		return lb.CanonicalZone
	}
	return ""
}

// An instance of a service with a custom health check answers only while
// healthy. A zone AWS Cloud Map does not own has no such health.
func r53LinkedServiceHealth(zone R53HostedZone) func(R53ResourceRecordSet) bool {
	if zone.LinkedService == nil || zone.LinkedService.ServicePrincipal != cmServicePrincipal {
		return nil
	}
	nsID := zone.LinkedService.Description[strings.LastIndex(zone.LinkedService.Description, "/")+1:]
	ns, ok := cmNamespaces.Get(nsID)
	if !ok {
		return nil
	}
	type owner struct{ name, instance string }
	unhealthy := map[owner]bool{}
	for _, svc := range cmServices.Filter(func(s CMService) bool {
		return s.NamespaceId == nsID && s.HealthCheckCustomConfig != nil
	}) {
		name := r53NormalizeName(svc.Name + "." + ns.Name)
		for _, inst := range cmServiceInstances(svc.Id) {
			if cmInstanceHealth(inst) == "UNHEALTHY" {
				unhealthy[owner{name, inst.Id}] = true
			}
		}
	}
	return func(set R53ResourceRecordSet) bool {
		return !unhealthy[owner{r53NormalizeName(set.Name), set.SetIdentifier}]
	}
}

// Probe the instance's IPv4 address and port; an instance without IPv4 gets
// no health check.
func cmCreateInstanceHealthCheck(svc CMService, attrs map[string]string) string {
	cfg := svc.HealthCheckConfig
	if cfg == nil || attrs["AWS_INSTANCE_IPV4"] == "" {
		return ""
	}
	check := R53HealthCheckConfig{IPAddress: attrs["AWS_INSTANCE_IPV4"], Type: cfg.Type}
	if port, err := strconv.Atoi(attrs["AWS_INSTANCE_PORT"]); err == nil {
		check.Port = &port
	}
	if cfg.Type != "TCP" {
		check.ResourcePath = cfg.ResourcePath
		if check.ResourcePath == "" {
			check.ResourcePath = "/"
		}
	}
	if cfg.FailureThreshold > 0 {
		threshold := cfg.FailureThreshold
		check.FailureThreshold = &threshold
	}
	id := r53ChildID()
	r53HealthChecks.Put(id, r53StoredHealthCheck{HealthCheck: R53HealthCheck{
		Xmlns:              r53Namespace,
		Id:                 id,
		CallerReference:    sim.NewUUID(),
		LinkedService:      &R53LinkedService{ServicePrincipal: cmServicePrincipal, Description: svc.Arn},
		HealthCheckConfig:  check,
		HealthCheckVersion: 1,
	}})
	return id
}

func cmDeleteInstanceHealthCheck(inst CMInstance) {
	if inst.HealthCheckId == "" {
		return
	}
	r53HealthChecks.Delete(inst.HealthCheckId)
	r53Health.Delete(inst.HealthCheckId)
}
