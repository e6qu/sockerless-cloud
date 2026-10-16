package main

import (
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// cmServicePrincipal is the LinkedService principal of the hosted zones Cloud
// Map creates.
const cmServicePrincipal = "servicediscovery.amazonaws.com"

// cmCreateHostedZone creates the Route 53 hosted zone a DNS namespace owns: a
// private zone associated with the namespace's VPC, or a public one.
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

// cmSetSOATTL sets the TTL of the apex SOA record, which resolvers use for
// negative caching.
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

// cmSyncNamespaceRecords rewrites the record sets Cloud Map keeps in a
// namespace's hosted zone from the namespace's services and their registered
// instances, leaving the apex NS and SOA as they are.
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

// cmServiceRecords are the record sets Cloud Map writes for a service's
// instances: one per instance and record type under the service's name, with
// the service's routing policy and the instance ID as SetIdentifier, plus the
// A and AAAA records an SRV target names.
func cmServiceRecords(ns CMNamespace, svc CMService) []R53ResourceRecordSet {
	if svc.DnsConfig == nil || len(svc.DnsConfig.DnsRecords) == 0 || strings.EqualFold(svc.Type, "HTTP") {
		return nil
	}
	name := r53NormalizeName(svc.Name + "." + ns.Name)
	weighted := strings.EqualFold(svc.DnsConfig.RoutingPolicy, "WEIGHTED")
	instances := cmServiceInstances(svc.Id)
	sort.Slice(instances, func(i, j int) bool { return instances[i].Id < instances[j].Id })

	var out []R53ResourceRecordSet
	routed := func(rr R53ResourceRecordSet, instanceID string) R53ResourceRecordSet {
		rr.SetIdentifier = instanceID
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
					}}, inst.Id))
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
			out = append(out, routed(cmRecordSet(name, rec.Type, rec.TTL, value), inst.Id))
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

// cmAliasHostedZoneID is the canonical hosted zone of the Elastic Load
// Balancing load balancer an alias names.
func cmAliasHostedZoneID(dnsName string) string {
	if lb, ok := elbv2LoadBalancerFromDataPlaneHost(strings.TrimSuffix(dnsName, ".")); ok {
		return lb.CanonicalZone
	}
	return ""
}
