package main

import (
	"fmt"
	"net"
	"strconv"

	"github.com/miekg/dns"
)

// 60 seconds, per the Elastic Load Balancing guides; a Route 53 alias to the
// load balancer takes this TTL.
const elbv2DNSTTL = 60

// Application: "<name>-<number>.<region>.elb.amazonaws.com", prefixed
// "internal-" when internal. Network and Gateway:
// "<name>-<id>.elb.<region>.amazonaws.com", id ending the ARN.
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

// Each load balancer's name answers with its listeners' bound address.
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

// Each gateway route's and Express service's on.aws hostname answers with its
// load balancer's address until torn down.
func ecsGatewayRouteDNSRecords() []dns.RR {
	if ecsGatewayRoutes == nil {
		return nil
	}
	var out []dns.RR
	hostnames := map[string]string{}
	for _, route := range ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool {
		return route.Hostname != "" && route.LoadBalancerArn != "" && route.Status != ecsGatewayRouteDeleteInProgress
	}) {
		hostnames[route.Hostname] = route.LoadBalancerArn
	}
	for hostname, lbArn := range ecsExpressHostnameAddresses() {
		hostnames[hostname] = lbArn
	}
	for hostname, lbArn := range hostnames {
		ip := net.ParseIP(elbv2NLBHostForReporting(lbArn)).To4()
		if ip == nil {
			continue
		}
		out = append(out, &dns.A{
			Hdr: dns.RR_Header{Name: dns.CanonicalName(hostname), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: elbv2DNSTTL},
			A:   ip,
		})
	}
	return out
}
