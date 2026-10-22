package main

import (
	"fmt"
	"net"
	"strconv"

	"github.com/miekg/dns"
)

// elbv2DNSTTL is the TTL of a load balancer's DNS record, 60 seconds as the
// Elastic Load Balancing user guides' target group health pages state. Route
// 53 answers an alias to a load balancer with it: an alias to an AWS resource
// takes the resource's TTL.
const elbv2DNSTTL = 60

// elbv2DNSName is the DNS name Elastic Load Balancing gives a load balancer.
// An Application Load Balancer's is "<name>-<number>.<region>.elb.amazonaws.com",
// prefixed "internal-" when internal; a Network or Gateway Load Balancer's is
// "<name>-<id>.elb.<region>.amazonaws.com", where id ends its ARN.
func elbv2DNSName(name, id, lbType, scheme string) string {
	if lbType != "application" {
		return fmt.Sprintf("%s-%s.elb.%s.amazonaws.com", name, id, awsRegion())
	}
	number, _ := strconv.ParseUint(id[:8], 16, 32)
	prefix := ""
	if scheme == "internal" {
		prefix = "internal-"
	}
	return fmt.Sprintf("%s%s-%d.%s.elb.amazonaws.com", prefix, name, number, awsRegion())
}

// elbv2DNSRecords are the A records that answer each load balancer's DNS name
// with the address its listeners are bound at.
func elbv2DNSRecords() []dns.RR {
	var out []dns.RR
	for _, entry := range elbv2NLBHostEntries() {
		ip := net.ParseIP(entry.IP).To4()
		if ip == nil {
			continue
		}
		out = append(out, &dns.A{
			Hdr: dns.RR_Header{Name: dns.CanonicalName(entry.Name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: elbv2DNSTTL},
			A:   ip,
		})
	}
	return append(out, ecsGatewayRouteDNSRecords()...)
}

// ecsGatewayRouteDNSRecords answer each Amazon ECS gateway route's hostname
// with the address of the load balancer that carries it, as the on.aws names
// Amazon ECS manages do, until the route is torn down.
func ecsGatewayRouteDNSRecords() []dns.RR {
	if ecsGatewayRoutes == nil {
		return nil
	}
	var out []dns.RR
	for _, route := range ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool {
		return route.Hostname != "" && route.LoadBalancerArn != "" && route.Status != ecsGatewayRouteDeleteInProgress
	}) {
		ip := net.ParseIP(elbv2NLBHostForReporting(route.LoadBalancerArn)).To4()
		if ip == nil {
			continue
		}
		out = append(out, &dns.A{
			Hdr: dns.RR_Header{Name: dns.CanonicalName(route.Hostname), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: elbv2DNSTTL},
			A:   ip,
		})
	}
	return out
}
