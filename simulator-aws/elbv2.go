package main

import (
	"crypto/rand"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

const elbv2APIVersion = "2015-12-01"

type ELBv2LoadBalancer struct {
	Arn           string
	Name          string
	DNSName       string
	CanonicalZone string
	Scheme        string
	Type          string
	State         string
	VpcID         string
	Subnets       []string
	// Empty when the request named Subnets.
	SubnetMappings            []ELBv2SubnetMapping
	SecurityGroups            []string
	IpAddressType             string
	CustomerOwnedIpv4Pool     string
	EnforceSGInboundOnPrivate string
	CreatedTime               string
	Tags                      map[string]string
	Attributes                map[string]string
	// Set by ModifyCapacityReservation; empty means none reserved.
	MinimumCapacityUnits string
	// Set by ModifyIpPools.
	Ipv4IpamPoolId string
}

type ELBv2SubnetMapping struct {
	SubnetId            string
	AllocationId        string
	PrivateIPv4Address  string
	IPv6Address         string
	SourceNatIpv6Prefix string
}

type ELBv2TargetGroup struct {
	Arn                     string
	Name                    string
	Protocol                string
	ProtocolVersion         string
	Port                    int
	VpcID                   string
	TargetType              string
	IpAddressType           string
	HealthCheckProtocol     string
	HealthCheckPort         string
	HealthCheckPath         string
	HealthCheckEnabled      bool
	HealthCheckInterval     int
	HealthCheckTimeout      int
	HealthyThresholdCount   int
	UnhealthyThresholdCount int
	MatcherHttpCode         string
	MatcherGrpcCode         string
	LoadBalancerArns        []string
	Targets                 []ELBv2TargetDescription
	Tags                    map[string]string
	Attributes              map[string]string
}

type ELBv2Listener struct {
	Arn             string
	LoadBalancerArn string
	Protocol        string
	Port            int
	DefaultActions  []ELBv2Action
	Certificates    []string // Set at create and by ModifyListener.
	SNICertificates []string // Added by AddListenerCertificates.
	SslPolicy       string
	AlpnPolicy      []string
	MutualAuth      *ELBv2MutualAuth
	Attributes      map[string]string
	Tags            map[string]string
}

type ELBv2MutualAuth struct {
	Mode                          string
	TrustStoreArn                 string
	IgnoreClientCertificateExpiry *bool
	AdvertiseTrustStoreCaNames    string
}

type ELBv2Action struct {
	Type           string
	TargetGroupArn string
	Order          int
	FixedResponse  *ELBv2FixedResponseConfig
	Redirect       *ELBv2RedirectConfig
	Forward        *ELBv2ForwardConfig
	AuthOidc       *ELBv2AuthOidcConfig
	AuthCognito    *ELBv2AuthCognitoConfig
}

// The weighted `forward {}` block, distinct from the single TargetGroupArn
// shorthand.
type ELBv2ForwardConfig struct {
	TargetGroups []ELBv2TargetGroupTuple
	Stickiness   *ELBv2TargetGroupStickiness
}

type ELBv2TargetGroupTuple struct {
	TargetGroupArn string
	Weight         *int
}

type ELBv2TargetGroupStickiness struct {
	Enabled         bool
	DurationSeconds *int
}

// ClientSecret is write-only: never stored or returned by Describe*.
type ELBv2AuthOidcConfig struct {
	Issuer                           string
	AuthorizationEndpoint            string
	TokenEndpoint                    string
	UserInfoEndpoint                 string
	ClientId                         string
	Scope                            string
	SessionCookieName                string
	SessionTimeout                   *int64
	OnUnauthenticatedRequest         string
	AuthenticationRequestExtraParams map[string]string
}

type ELBv2AuthCognitoConfig struct {
	UserPoolArn                      string
	UserPoolClientId                 string
	UserPoolDomain                   string
	Scope                            string
	SessionCookieName                string
	SessionTimeout                   *int64
	OnUnauthenticatedRequest         string
	AuthenticationRequestExtraParams map[string]string
}

type ELBv2FixedResponseConfig struct {
	StatusCode  string
	ContentType string
	MessageBody string
}

type ELBv2RedirectConfig struct {
	Protocol   string
	Port       string
	Host       string
	Path       string
	Query      string
	StatusCode string
}

type ELBv2TargetDescription struct {
	ID               string
	Port             int
	AvailabilityZone string
	// The zero time for a registered target. "By default, Elastic Load Balancing
	// waits 300 seconds before completing the deregistration process", so the
	// group keeps a deregistering target, reporting `draining`, until the delay
	// elapses.
	DeregisteringAt time.Time
}

var (
	elbv2LoadBalancers sim.Store[ELBv2LoadBalancer]
	elbv2TargetGroups  sim.Store[ELBv2TargetGroup]
	elbv2Listeners     sim.Store[ELBv2Listener]
)

func registerELBv2(r *AWSQueryRouter, srv *sim.Server) {
	elbv2LoadBalancers = sim.MakeStore[ELBv2LoadBalancer](srv.DB(), "elbv2_load_balancers")
	elbv2TargetGroups = sim.MakeStore[ELBv2TargetGroup](srv.DB(), "elbv2_target_groups")
	elbv2Listeners = sim.MakeStore[ELBv2Listener](srv.DB(), "elbv2_listeners")

	r.RegisterVersioned(elbv2APIVersion, "CreateLoadBalancer", handleELBv2CreateLoadBalancer)
	r.RegisterVersioned(elbv2APIVersion, "DescribeLoadBalancers", handleELBv2DescribeLoadBalancers)
	r.RegisterVersioned(elbv2APIVersion, "DeleteLoadBalancer", handleELBv2DeleteLoadBalancer)
	r.RegisterVersioned(elbv2APIVersion, "ModifyLoadBalancerAttributes", handleELBv2ModifyLoadBalancerAttributes)
	r.RegisterVersioned(elbv2APIVersion, "DescribeLoadBalancerAttributes", handleELBv2DescribeLoadBalancerAttributes)
	r.RegisterVersioned(elbv2APIVersion, "DescribeCapacityReservation", handleELBv2DescribeCapacityReservation)
	r.RegisterVersioned(elbv2APIVersion, "SetSecurityGroups", handleELBv2SetSecurityGroups)
	r.RegisterVersioned(elbv2APIVersion, "SetSubnets", handleELBv2SetSubnets)
	r.RegisterVersioned(elbv2APIVersion, "SetIpAddressType", handleELBv2SetIpAddressType)

	r.RegisterVersioned(elbv2APIVersion, "CreateTargetGroup", handleELBv2CreateTargetGroup)
	r.RegisterVersioned(elbv2APIVersion, "DescribeTargetGroups", handleELBv2DescribeTargetGroups)
	r.RegisterVersioned(elbv2APIVersion, "DeleteTargetGroup", handleELBv2DeleteTargetGroup)
	r.RegisterVersioned(elbv2APIVersion, "ModifyTargetGroup", handleELBv2ModifyTargetGroup)
	r.RegisterVersioned(elbv2APIVersion, "ModifyTargetGroupAttributes", handleELBv2ModifyTargetGroupAttributes)
	r.RegisterVersioned(elbv2APIVersion, "DescribeTargetGroupAttributes", handleELBv2DescribeTargetGroupAttributes)
	r.RegisterVersioned(elbv2APIVersion, "RegisterTargets", handleELBv2RegisterTargets)
	r.RegisterVersioned(elbv2APIVersion, "DeregisterTargets", handleELBv2DeregisterTargets)
	r.RegisterVersioned(elbv2APIVersion, "DescribeTargetHealth", handleELBv2DescribeTargetHealth)

	r.RegisterVersioned(elbv2APIVersion, "CreateListener", handleELBv2CreateListener)
	r.RegisterVersioned(elbv2APIVersion, "DescribeListeners", handleELBv2DescribeListeners)
	r.RegisterVersioned(elbv2APIVersion, "DescribeListenerAttributes", handleELBv2DescribeListenerAttributes)
	r.RegisterVersioned(elbv2APIVersion, "ModifyListenerAttributes", handleELBv2ModifyListenerAttributes)
	r.RegisterVersioned(elbv2APIVersion, "DeleteListener", handleELBv2DeleteListener)

	r.RegisterVersioned(elbv2APIVersion, "AddTags", handleELBv2AddTags)
	r.RegisterVersioned(elbv2APIVersion, "RemoveTags", handleELBv2RemoveTags)
	r.RegisterVersioned(elbv2APIVersion, "DescribeTags", handleELBv2DescribeTags)
	r.RegisterVersioned(elbv2APIVersion, "DescribeAccountLimits", handleELBv2DescribeAccountLimits)

	registerELBv2Rules(r, srv)
	registerELBv2TrustStores(r, srv)
	startELBv2TargetHealthChecker(srv)
	if err := elbv2RecoverDataPlanes(); err != nil {
		panic(fmt.Sprintf("restore Elastic Load Balancing data planes: %v", err))
	}
}

func elbv2RecoverDataPlanes() error {
	for _, listener := range elbv2Listeners.List() {
		if err := elbv2StartListenerProxy(listener); err != nil {
			return fmt.Errorf("restore %s listener %s: %w", listener.Protocol, listener.Arn, err)
		}
	}
	return nil
}

func elbv2XMLResponse(w http.ResponseWriter, op string, body string, requestID string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w,
		`<%sResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"><%sResult>%s</%sResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, op, body, op, requestID, op)
}

func elbv2ErrorXML(w http.ResponseWriter, code, message string, status int, requestID string) {
	awsWriteError(w, awsErrorEnvelope{Protocol: awsQuery, Namespace: "http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"}, status, code, message, requestID)
}

func handleELBv2CreateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		elbv2ErrorXML(w, "ValidationError", "Name is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	lbType := r.FormValue("Type")
	if lbType == "" {
		lbType = "application"
	}
	scheme := r.FormValue("Scheme")
	if scheme == "" {
		scheme = "internet-facing"
	}
	ipType := r.FormValue("IpAddressType")
	if ipType == "" {
		ipType = "ipv4"
	}
	subnets, mappings, code, message := elbv2RequestSubnets(r, lbType, scheme, ipType)
	if code != "" {
		elbv2ErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	id := sim.RandomHex(16)
	arn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:loadbalancer/%s/%s/%s", awsRegion(), awsAccountID(), elbv2LoadBalancerKind(ELBv2LoadBalancer{Type: lbType}), name, id)
	lb := ELBv2LoadBalancer{
		Arn:            arn,
		Name:           name,
		DNSName:        elbv2DNSName(name, id, lbType, scheme),
		CanonicalZone:  elbv2HostedZones[lbType][awsRegion()],
		Scheme:         scheme,
		Type:           lbType,
		State:          "active",
		VpcID:          elbv2VPCFromSubnets(subnets),
		Subnets:        subnets,
		SubnetMappings: mappings,
		SecurityGroups: queryList(r, "SecurityGroups"),
		IpAddressType:  ipType,
		// Network Load Balancers default this to "on"; Application Load Balancers do
		// not carry it.
		EnforceSGInboundOnPrivate: elbv2DefaultEnforceSG(lbType, r.FormValue("EnforceSecurityGroupInboundRulesOnPrivateLinkTraffic")),
		CustomerOwnedIpv4Pool:     r.FormValue("CustomerOwnedIpv4Pool"),
		CreatedTime:               time.Now().UTC().Format(time.RFC3339),
		Tags:                      parseELBv2Tags(r, "Tags"),
		Attributes:                defaultELBv2LoadBalancerAttributes(lbType, scheme),
	}
	// An Application Load Balancer created without a group joins the VPC's
	// default group.
	if lbType == "application" && len(lb.SecurityGroups) == 0 {
		lb.SecurityGroups = ec2DefaultSecurityGroupIDs(lb.VpcID)
	}
	// "This operation is idempotent ... If you attempt to create multiple load
	// balancers with the same settings, each call succeeds."
	for _, existing := range elbv2LoadBalancers.List() {
		if existing.Name != name {
			continue
		}
		if elbv2SameLoadBalancerSettings(existing, lb) {
			elbv2XMLResponse(w, "CreateLoadBalancer", "<LoadBalancers>"+elbv2LoadBalancerXML(existing)+"</LoadBalancers>", sim.RequestID(r.Context()))
			return
		}
		elbv2ErrorXML(w, "DuplicateLoadBalancerName",
			fmt.Sprintf("A load balancer with the same name '%s' exists, but with different settings.", name),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	lb.SubnetMappings = elbv2AssignSourceNatPrefixes(lb.SubnetMappings)
	elbv2LoadBalancers.Put(arn, lb)
	ec2ReapplyIngress(lb.VpcID)
	elbv2XMLResponse(w, "CreateLoadBalancer", "<LoadBalancers>"+elbv2LoadBalancerXML(lb)+"</LoadBalancers>", sim.RequestID(r.Context()))
}

// A source NAT prefix the request left to the load balancer matches any
// prefix the existing one was assigned.
func elbv2SameLoadBalancerSettings(existing, candidate ELBv2LoadBalancer) bool {
	sorted := func(values []string) []string { return slices.Sorted(slices.Values(values)) }
	bySubnet := func(mappings []ELBv2SubnetMapping) []ELBv2SubnetMapping {
		return slices.SortedFunc(slices.Values(mappings), func(a, b ELBv2SubnetMapping) int {
			return strings.Compare(a.SubnetId, b.SubnetId)
		})
	}
	requested, held := bySubnet(candidate.SubnetMappings), bySubnet(existing.SubnetMappings)
	if len(requested) != len(held) {
		return false
	}
	for i := range requested {
		if requested[i].SourceNatIpv6Prefix == elbv2SourceNatPrefixAutoAssigned && held[i].SourceNatIpv6Prefix != "" {
			requested[i].SourceNatIpv6Prefix = held[i].SourceNatIpv6Prefix
		}
		if requested[i] != held[i] {
			return false
		}
	}
	return existing.Type == candidate.Type &&
		existing.Scheme == candidate.Scheme &&
		existing.IpAddressType == candidate.IpAddressType &&
		existing.CustomerOwnedIpv4Pool == candidate.CustomerOwnedIpv4Pool &&
		existing.EnforceSGInboundOnPrivate == candidate.EnforceSGInboundOnPrivate &&
		slices.Equal(sorted(existing.Subnets), sorted(candidate.Subnets)) &&
		slices.Equal(sorted(existing.SecurityGroups), sorted(candidate.SecurityGroups)) &&
		maps.Equal(existing.Tags, candidate.Tags)
}

func handleELBv2DescribeLoadBalancers(w http.ResponseWriter, r *http.Request) {
	lbs, nf := filterELBv2LoadBalancers(r)
	if nf != nil {
		elbv2ErrorXML(w, nf.code, nf.message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<LoadBalancers>")
	for _, lb := range lbs {
		b.WriteString(elbv2LoadBalancerXML(lb))
	}
	b.WriteString("</LoadBalancers>")
	elbv2XMLResponse(w, "DescribeLoadBalancers", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2DeleteLoadBalancer(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	deleted, exists := elbv2LoadBalancers.Get(arn)
	if exists && strings.EqualFold(elbv2LoadBalancerAttributes(arn)["deletion_protection.enabled"], "true") {
		elbv2ErrorXML(w, "OperationNotPermitted",
			fmt.Sprintf("Load balancer '%s' cannot be deleted because deletion protection is enabled", arn),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	elbv2LoadBalancers.Delete(arn)
	ec2ReapplyIngress(deleted.VpcID)
	for _, listener := range elbv2Listeners.Filter(func(l ELBv2Listener) bool { return l.LoadBalancerArn == arn }) {
		elbv2DeleteListener(listener.Arn)
	}
	for _, tg := range elbv2TargetGroups.List() {
		tg.LoadBalancerArns = removeString(tg.LoadBalancerArns, arn)
		elbv2TargetGroups.Put(tg.Arn, tg)
	}
	elbv2XMLResponse(w, "DeleteLoadBalancer", "", sim.RequestID(r.Context()))
}

func handleELBv2ModifyLoadBalancerAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	modified := parseELBv2Attributes(r, "Attributes")
	lb, ok := elbv2LoadBalancers.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if message := elbv2InvalidLoadBalancerAttribute(lb.Type, modified); message != "" {
		elbv2ErrorXML(w, "InvalidConfigurationRequest", message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var attrs map[string]string
	if !elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		for k, v := range modified {
			lb.Attributes[k] = v
		}
		attrs = maps.Clone(lb.Attributes)
	}) {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "ModifyLoadBalancerAttributes", elbv2AttributesXML("Attributes", attrs), sim.RequestID(r.Context()))
}

// Returns "" when every key belongs to the type and every value is accepted.
// An Application Load Balancer's cross-zone load balancing "is true, and can't
// be changed".
func elbv2InvalidLoadBalancerAttribute(lbType string, modified map[string]string) string {
	valid := defaultELBv2LoadBalancerAttributes(lbType, "")
	for _, key := range slices.Sorted(maps.Keys(modified)) {
		value := modified[key]
		if _, ok := valid[key]; !ok {
			return fmt.Sprintf("Key %s not valid", key)
		}
		if lbType == "application" && key == "load_balancing.cross_zone.enabled" && value != "true" {
			return "Cross-zone load balancing is always enabled for Application Load Balancers and cannot be disabled"
		}
		if bounds, ok := elbv2RangedLoadBalancerAttributes[key]; ok {
			if n, err := strconv.Atoi(value); err != nil || n < bounds[0] || n > bounds[1] {
				return fmt.Sprintf("Load balancer attribute value '%s' for key '%s' is not valid; it must be an integer from %d to %d",
					value, key, bounds[0], bounds[1])
			}
		}
		allowed, ok := elbv2EnumeratedLoadBalancerAttributes[key]
		if !ok && strings.HasSuffix(key, ".enabled") {
			allowed = []string{"true", "false"}
		}
		if allowed != nil && !slices.Contains(allowed, value) {
			return fmt.Sprintf("Load balancer attribute value '%s' for key '%s' is not valid; it must be one of '%s'",
				value, key, strings.Join(allowed, "', '"))
		}
	}
	return ""
}

func handleELBv2DescribeLoadBalancerAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	lb, ok := elbv2LoadBalancers.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "DescribeLoadBalancerAttributes", elbv2AttributesXML("Attributes", lb.Attributes), sim.RequestID(r.Context()))
}

func handleELBv2DescribeCapacityReservation(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	lb, ok := elbv2LoadBalancers.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var states strings.Builder
	states.WriteString("<CapacityReservationState>")
	for _, subnet := range lb.Subnets {
		az := awsAvailabilityZone()
		if s, ok := ec2Subnets.Get(subnet); ok && s.AvailabilityZone != "" {
			az = s.AvailabilityZone
		}
		fmt.Fprintf(&states, "<member><State><Code>provisioned</Code></State><AvailabilityZone>%s</AvailabilityZone><EffectiveCapacityUnits>0</EffectiveCapacityUnits></member>", xmlEscape(az))
	}
	states.WriteString("</CapacityReservationState>")
	// Omit MinimumLoadBalancerCapacity unless a minimum is configured; emitting 0
	// makes the provider plan "capacity_units = 0 -> null" every time.
	body := fmt.Sprintf("<LastModifiedTime>%s</LastModifiedTime><DecreaseRequestsRemaining>10</DecreaseRequestsRemaining>%s",
		xmlEscape(lb.CreatedTime), states.String())
	elbv2XMLResponse(w, "DescribeCapacityReservation", body, sim.RequestID(r.Context()))
}

func handleELBv2SetSecurityGroups(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	groups := queryList(r, "SecurityGroups")
	var vpcID string
	if !elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		lb.SecurityGroups = groups
		vpcID = lb.VpcID
	}) {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ec2ReapplyIngress(vpcID)
	elbv2XMLResponse(w, "SetSecurityGroups", elbv2StringMembersXML("SecurityGroupIds", groups), sim.RequestID(r.Context()))
}

func handleELBv2SetSubnets(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	current, ok := elbv2LoadBalancers.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ipType := firstNonEmpty(r.FormValue("IpAddressType"), current.IpAddressType)
	subnets, mappings, code, message := elbv2RequestSubnets(r, current.Type, current.Scheme, ipType)
	if code != "" {
		elbv2ErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	mappings = elbv2AssignSourceNatPrefixes(mappings)
	if !elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		lb.Subnets = subnets
		lb.SubnetMappings = mappings
		lb.VpcID = elbv2VPCFromSubnets(subnets)
		lb.IpAddressType = ipType
	}) {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	lb, _ := elbv2LoadBalancers.Get(arn)
	elbv2XMLResponse(w, "SetSubnets", "<AvailabilityZones>"+elbv2AvailabilityZonesXML(lb)+"</AvailabilityZones><IpAddressType>"+xmlEscape(ipType)+"</IpAddressType>", sim.RequestID(r.Context()))
}

// The SourceNatIpv6Prefix that leaves the prefix to the load balancer.
const elbv2SourceNatPrefixAutoAssigned = "auto_assigned"

// "You must specify either subnets or subnet mappings, but not both."
func elbv2RequestSubnets(r *http.Request, lbType, scheme, ipType string) (subnets []string, mappings []ELBv2SubnetMapping, code, message string) {
	subnets = queryList(r, "Subnets")
	for i := 1; ; i++ {
		base := fmt.Sprintf("SubnetMappings.member.%d", i)
		id := r.FormValue(base + ".SubnetId")
		if id == "" {
			break
		}
		mappings = append(mappings, ELBv2SubnetMapping{
			SubnetId:            id,
			AllocationId:        r.FormValue(base + ".AllocationId"),
			PrivateIPv4Address:  r.FormValue(base + ".PrivateIPv4Address"),
			IPv6Address:         r.FormValue(base + ".IPv6Address"),
			SourceNatIpv6Prefix: r.FormValue(base + ".SourceNatIpv6Prefix"),
		})
	}
	if len(subnets) > 0 && len(mappings) > 0 {
		return nil, nil, "InvalidConfigurationRequest", "You must specify either Subnets or SubnetMappings, but not both"
	}
	for _, mapping := range mappings {
		subnets = append(subnets, mapping.SubnetId)
	}
	if code, message = elbv2SubnetsRefusal(lbType, ipType, subnets); code != "" {
		return nil, nil, code, message
	}
	if code, message = elbv2SubnetMappingsRefusal(lbType, scheme, mappings); code != "" {
		return nil, nil, code, message
	}
	return subnets, mappings, "", ""
}

// The address members are "[Network Load Balancers]" only; an Elastic IP is
// "for an internet-facing load balancer", a private IPv4 address "for an
// internal load balancer" from "the IPv4 range of the subnet", and a source
// NAT prefix "an IPv6 prefix (/80 netmask) from the subnet CIDR block or
// auto_assigned".
func elbv2SubnetMappingsRefusal(lbType, scheme string, mappings []ELBv2SubnetMapping) (code, message string) {
	for _, mapping := range mappings {
		subnet, _ := ec2Subnets.Get(mapping.SubnetId)
		for _, member := range []struct{ name, value string }{
			{"AllocationId", mapping.AllocationId},
			{"PrivateIPv4Address", mapping.PrivateIPv4Address},
			{"IPv6Address", mapping.IPv6Address},
			{"SourceNatIpv6Prefix", mapping.SourceNatIpv6Prefix},
		} {
			if member.value != "" && lbType != "network" {
				return "InvalidConfigurationRequest", fmt.Sprintf("%s is supported only for Network Load Balancers", member.name)
			}
		}
		if mapping.AllocationId != "" {
			if scheme != "internet-facing" {
				return "InvalidConfigurationRequest", "AllocationId is supported only for an internet-facing load balancer"
			}
			if _, ok := ec2ElasticIPs.Get(mapping.AllocationId); !ok {
				return "AllocationIdNotFound", "The specified allocation ID does not exist."
			}
		}
		if mapping.PrivateIPv4Address != "" {
			if scheme != "internal" {
				return "InvalidConfigurationRequest", "PrivateIPv4Address is supported only for an internal load balancer"
			}
			if !elbv2AddressInBlock(mapping.PrivateIPv4Address, subnet.CidrBlock) {
				return "InvalidConfigurationRequest", fmt.Sprintf("PrivateIPv4Address '%s' is not in the IPv4 range of subnet '%s'", mapping.PrivateIPv4Address, mapping.SubnetId)
			}
		}
		if mapping.IPv6Address != "" && !elbv2AddressInBlock(mapping.IPv6Address, subnet.Ipv6CidrBlock) {
			return "InvalidConfigurationRequest", fmt.Sprintf("IPv6Address '%s' is not in the IPv6 CIDR block of subnet '%s'", mapping.IPv6Address, mapping.SubnetId)
		}
		if prefix := mapping.SourceNatIpv6Prefix; prefix != "" && prefix != elbv2SourceNatPrefixAutoAssigned {
			parsed, err := netip.ParsePrefix(prefix)
			block, blockErr := netip.ParsePrefix(subnet.Ipv6CidrBlock)
			if err != nil || blockErr != nil || parsed.Bits() != 80 || !block.Contains(parsed.Addr()) {
				return "InvalidConfigurationRequest", fmt.Sprintf("SourceNatIpv6Prefix '%s' is not a /80 prefix of the IPv6 CIDR block of subnet '%s'", prefix, mapping.SubnetId)
			}
		}
	}
	return "", ""
}

func elbv2AddressInBlock(address, block string) bool {
	addr, err := netip.ParseAddr(address)
	prefix, prefixErr := netip.ParsePrefix(block)
	return err == nil && prefixErr == nil && prefix.Contains(addr)
}

// Each auto_assigned prefix becomes "an IPv6 prefix selected at random from
// the subnet CIDR block".
func elbv2AssignSourceNatPrefixes(mappings []ELBv2SubnetMapping) []ELBv2SubnetMapping {
	assigned := slices.Clone(mappings)
	for i, mapping := range assigned {
		if mapping.SourceNatIpv6Prefix != elbv2SourceNatPrefixAutoAssigned {
			continue
		}
		subnet, _ := ec2Subnets.Get(mapping.SubnetId)
		block, err := netip.ParsePrefix(subnet.Ipv6CidrBlock)
		if err != nil {
			continue
		}
		addr := block.Masked().Addr().As16()
		var random [16]byte
		rand.Read(random[:])
		for bit := block.Bits(); bit < 80; bit++ {
			if random[bit/8]&(1<<(7-bit%8)) != 0 {
				addr[bit/8] |= 1 << (7 - bit%8)
			}
		}
		assigned[i].SourceNatIpv6Prefix = netip.PrefixFrom(netip.AddrFrom16(addr), 80).String()
	}
	return assigned
}

// One subnet per zone, an Application Load Balancer in at least two zones, and
// an IPv6 CIDR on every subnet of a dualstack one; an empty code means they
// fit.
func elbv2SubnetsRefusal(lbType, ipType string, subnets []string) (code, message string) {
	zones := map[string]bool{}
	for _, id := range subnets {
		subnet, ok := ec2Subnets.Get(id)
		if !ok {
			return "SubnetNotFound", fmt.Sprintf("The subnet ID '%s' is not valid", id)
		}
		if zones[subnet.AvailabilityZone] {
			return "InvalidConfigurationRequest", "A load balancer cannot be attached to multiple subnets in the same Availability Zone"
		}
		zones[subnet.AvailabilityZone] = true
	}
	if lbType == "application" && len(zones) < 2 {
		return "ValidationError", "At least two subnets in two different Availability Zones must be specified"
	}
	return elbv2IpAddressTypeRefusal(ipType, subnets)
}

// A dualstack load balancer needs an IPv6 CIDR on every subnet.
func elbv2IpAddressTypeRefusal(ipType string, subnets []string) (code, message string) {
	if !strings.HasPrefix(ipType, "dualstack") {
		return "", ""
	}
	for _, id := range subnets {
		if subnet, _ := ec2Subnets.Get(id); subnet.Ipv6CidrBlock == "" {
			return "InvalidSubnet", fmt.Sprintf("Subnet '%s' does not have an IPv6 CIDR block", id)
		}
	}
	return "", ""
}

func handleELBv2CreateTargetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		elbv2ErrorXML(w, "ValidationError", "Name is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	port := atoiDefault(r.FormValue("Port"), 80)
	protocol := r.FormValue("Protocol")
	if protocol == "" {
		protocol = "HTTP"
	}
	targetType := r.FormValue("TargetType")
	if targetType == "" {
		targetType = "instance"
	}
	for _, existing := range elbv2TargetGroups.List() {
		if existing.Name == name {
			elbv2ErrorXML(w, "DuplicateTargetGroupName",
				fmt.Sprintf("A target group with the same name '%s' exists, but with different settings.", name),
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
	}
	id := sim.RandomHex(16)
	arn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:targetgroup/%s/%s", awsRegion(), awsAccountID(), name, id)
	tg := ELBv2TargetGroup{
		Arn:                     arn,
		Name:                    name,
		Protocol:                protocol,
		ProtocolVersion:         elbv2DefaultProtocolVersion(protocol, r.FormValue("ProtocolVersion")),
		Port:                    port,
		VpcID:                   r.FormValue("VpcId"),
		TargetType:              targetType,
		IpAddressType:           firstNonEmpty(r.FormValue("IpAddressType"), "ipv4"),
		HealthCheckProtocol:     firstNonEmpty(r.FormValue("HealthCheckProtocol"), protocol),
		HealthCheckPort:         firstNonEmpty(r.FormValue("HealthCheckPort"), "traffic-port"),
		HealthCheckPath:         elbv2DefaultedHealthCheckPath(firstNonEmpty(r.FormValue("HealthCheckProtocol"), protocol), r.FormValue("HealthCheckPath")),
		HealthCheckEnabled:      true,
		HealthCheckInterval:     atoiDefault(r.FormValue("HealthCheckIntervalSeconds"), 30),
		HealthCheckTimeout:      atoiDefault(r.FormValue("HealthCheckTimeoutSeconds"), 5),
		HealthyThresholdCount:   atoiDefault(r.FormValue("HealthyThresholdCount"), 5),
		UnhealthyThresholdCount: atoiDefault(r.FormValue("UnhealthyThresholdCount"), 2),
		MatcherHttpCode:         elbv2DefaultedMatcher(firstNonEmpty(r.FormValue("HealthCheckProtocol"), protocol), r.FormValue("Matcher.HttpCode")),
		MatcherGrpcCode:         r.FormValue("Matcher.GrpcCode"),
		Tags:                    parseELBv2Tags(r, "Tags"),
		Attributes:              defaultELBv2TargetGroupAttributes(),
	}
	if r.FormValue("HealthCheckEnabled") == "false" {
		tg.HealthCheckEnabled = false
	}
	elbv2TargetGroups.Put(arn, tg)
	elbv2XMLResponse(w, "CreateTargetGroup", "<TargetGroups>"+elbv2TargetGroupXML(tg)+"</TargetGroups>", sim.RequestID(r.Context()))
}

func handleELBv2DescribeTargetGroups(w http.ResponseWriter, r *http.Request) {
	tgs, nf := filterELBv2TargetGroups(r)
	if nf != nil {
		elbv2ErrorXML(w, nf.code, nf.message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<TargetGroups>")
	for _, tg := range tgs {
		b.WriteString(elbv2TargetGroupXML(tg))
	}
	b.WriteString("</TargetGroups>")
	elbv2XMLResponse(w, "DescribeTargetGroups", b.String(), sim.RequestID(r.Context()))
}

func elbv2TargetGroupReferenced(arn string) bool {
	for _, listener := range elbv2Listeners.List() {
		if slices.Contains(elbv2ActionForwardTargets(listener.DefaultActions), arn) {
			return true
		}
	}
	for _, rule := range elbv2Rules.List() {
		if slices.Contains(elbv2ActionForwardTargets(rule.Actions), arn) {
			return true
		}
	}
	return false
}

func handleELBv2DeleteTargetGroup(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	if elbv2TargetGroupReferenced(arn) {
		elbv2ErrorXML(w, "ResourceInUse",
			fmt.Sprintf("Target group '%s' is currently in use by a listener or a rule", arn),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	elbv2TargetGroups.Delete(arn)
	elbv2XMLResponse(w, "DeleteTargetGroup", "", sim.RequestID(r.Context()))
}

func handleELBv2ModifyTargetGroup(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	if !elbv2TargetGroups.Update(arn, func(tg *ELBv2TargetGroup) {
		if v := r.FormValue("HealthCheckProtocol"); v != "" {
			tg.HealthCheckProtocol = v
		}
		if v := r.FormValue("HealthCheckPort"); v != "" {
			tg.HealthCheckPort = v
		}
		if v := r.FormValue("HealthCheckPath"); v != "" {
			tg.HealthCheckPath = v
		}
		if v := r.FormValue("HealthCheckIntervalSeconds"); v != "" {
			tg.HealthCheckInterval = atoiDefault(v, tg.HealthCheckInterval)
		}
		if v := r.FormValue("HealthCheckTimeoutSeconds"); v != "" {
			tg.HealthCheckTimeout = atoiDefault(v, tg.HealthCheckTimeout)
		}
		if v := r.FormValue("HealthyThresholdCount"); v != "" {
			tg.HealthyThresholdCount = atoiDefault(v, tg.HealthyThresholdCount)
		}
		if v := r.FormValue("UnhealthyThresholdCount"); v != "" {
			tg.UnhealthyThresholdCount = atoiDefault(v, tg.UnhealthyThresholdCount)
		}
		if v := r.FormValue("HealthCheckEnabled"); v != "" {
			tg.HealthCheckEnabled = v == "true"
		}
		if v := r.FormValue("Matcher.HttpCode"); v != "" {
			tg.MatcherHttpCode = v
		}
		if v := r.FormValue("Matcher.GrpcCode"); v != "" {
			tg.MatcherGrpcCode = v
		}
		// HTTP and HTTPS health checks default Matcher to "200" and the path to "/";
		// other protocols take neither, so clear values left from a prior HTTP check
		// or terraform drifts.
		if elbv2HTTPHealthCheck(tg.HealthCheckProtocol) {
			if tg.MatcherHttpCode == "" && tg.MatcherGrpcCode == "" {
				tg.MatcherHttpCode = elbv2DefaultMatcher()
			}
			if tg.HealthCheckPath == "" {
				tg.HealthCheckPath = "/"
			}
		} else {
			tg.MatcherHttpCode = ""
			tg.MatcherGrpcCode = ""
			tg.HealthCheckPath = ""
		}
	}) {
		elbv2ErrorXML(w, "TargetGroupNotFound", "Target group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	tg, _ := elbv2TargetGroups.Get(arn)
	elbv2XMLResponse(w, "ModifyTargetGroup", "<TargetGroups>"+elbv2TargetGroupXML(tg)+"</TargetGroups>", sim.RequestID(r.Context()))
}

func handleELBv2ModifyTargetGroupAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	if !elbv2TargetGroups.Update(arn, func(tg *ELBv2TargetGroup) {
		for k, v := range parseELBv2Attributes(r, "Attributes") {
			tg.Attributes[k] = v
		}
	}) {
		elbv2ErrorXML(w, "TargetGroupNotFound", "Target group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	tg, _ := elbv2TargetGroups.Get(arn)
	elbv2XMLResponse(w, "ModifyTargetGroupAttributes", elbv2AttributesXML("Attributes", tg.Attributes), sim.RequestID(r.Context()))
}

func handleELBv2DescribeTargetGroupAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	tg, ok := elbv2TargetGroups.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "TargetGroupNotFound", "Target group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "DescribeTargetGroupAttributes", elbv2AttributesXML("Attributes", tg.Attributes), sim.RequestID(r.Context()))
}

func handleELBv2RegisterTargets(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	targets := parseELBv2Targets(r)
	if !elbv2TargetGroups.Update(arn, func(tg *ELBv2TargetGroup) {
		for _, incoming := range targets {
			replaced := false
			for i := range tg.Targets {
				if tg.Targets[i].ID == incoming.ID && tg.Targets[i].Port == incoming.Port {
					tg.Targets[i] = incoming
					replaced = true
					break
				}
			}
			if !replaced {
				tg.Targets = append(tg.Targets, incoming)
			}
		}
	}) {
		elbv2ErrorXML(w, "TargetGroupNotFound", "Target group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "RegisterTargets", "", sim.RequestID(r.Context()))
}

// "The target enters the `draining` state until in-flight requests have
// completed." The group keeps the target for its deregistration delay; with a
// zero delay it goes before the call returns.
func handleELBv2DeregisterTargets(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	targets := parseELBv2Targets(r)
	deregisteringAt := time.Now()
	var draining []ELBv2TargetDescription
	if !elbv2TargetGroups.Update(arn, func(tg *ELBv2TargetGroup) {
		draining = nil
		for i := range tg.Targets {
			if !tg.Targets[i].DeregisteringAt.IsZero() {
				continue
			}
			for _, t := range targets {
				if tg.Targets[i].ID == t.ID && (t.Port == 0 || tg.Targets[i].Port == t.Port) {
					tg.Targets[i].DeregisteringAt = deregisteringAt
					draining = append(draining, tg.Targets[i])
					break
				}
			}
		}
	}) {
		elbv2ErrorXML(w, "TargetGroupNotFound", "Target group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	// A draining target loses its verdict at once.
	for _, target := range draining {
		elbv2ForgetTargetHealth(arn, target)
	}
	elbv2CompleteDueDeregistrations(deregisteringAt)
	elbv2XMLResponse(w, "DeregisterTargets", "", sim.RequestID(r.Context()))
}

func handleELBv2DescribeTargetHealth(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	tg, ok := elbv2TargetGroups.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "TargetGroupNotFound", "Target group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	filter := parseELBv2Targets(r)
	targets := tg.Targets
	if len(filter) > 0 {
		targets = nil
		for _, existing := range tg.Targets {
			for _, wanted := range filter {
				if existing.ID == wanted.ID && (wanted.Port == 0 || existing.Port == wanted.Port) {
					targets = append(targets, existing)
					break
				}
			}
		}
	}
	var b strings.Builder
	b.WriteString("<TargetHealthDescriptions>")
	for _, target := range targets {
		health := elbv2TargetHealthFor(tg, target)
		fmt.Fprintf(&b, `<member><Target>%s</Target><HealthCheckPort>%d</HealthCheckPort><TargetHealth><State>%s</State>`,
			elbv2TargetXML(target), elbv2EffectiveHealthCheckPort(tg, target), xmlEscape(health.State))
		// A healthy target carries neither a reason code nor a description.
		if health.Reason != "" {
			fmt.Fprintf(&b, "<Reason>%s</Reason>", xmlEscape(health.Reason))
		}
		if health.Description != "" {
			fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(health.Description))
		}
		b.WriteString("</TargetHealth></member>")
	}
	b.WriteString("</TargetHealthDescriptions>")
	elbv2XMLResponse(w, "DescribeTargetHealth", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2CreateListener(w http.ResponseWriter, r *http.Request) {
	lbArn := r.FormValue("LoadBalancerArn")
	lb, ok := elbv2LoadBalancers.Get(lbArn)
	if !ok {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	protocol := r.FormValue("Protocol")
	if protocol == "" {
		protocol = "HTTP"
	}
	port := atoiDefault(r.FormValue("Port"), 80)
	id := sim.RandomHex(16)
	arn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:listener/%s/%s/%s/%s", awsRegion(), awsAccountID(), elbv2LoadBalancerKind(lb), lb.Name, elbv2LoadBalancerID(lb.Arn), id)
	listener := ELBv2Listener{
		Arn:             arn,
		LoadBalancerArn: lbArn,
		Protocol:        protocol,
		Port:            port,
		DefaultActions:  parseELBv2Actions(r),
		Certificates:    parseELBv2Certificates(r),
		SslPolicy:       r.FormValue("SslPolicy"),
		AlpnPolicy:      queryList(r, "AlpnPolicy"),
		MutualAuth:      parseELBv2MutualAuth(r),
		Attributes:      defaultELBv2ListenerAttributes(lb.Type),
		Tags:            parseELBv2Tags(r, "Tags"),
	}
	if code, message := elbv2ActionsRefusal(listener.DefaultActions); code != "" {
		elbv2ErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	elbv2Listeners.Put(arn, listener)
	if err := elbv2StartListenerDataPlane(listener); err != nil {
		elbv2Listeners.Delete(arn)
		elbv2StopListenerProxy(arn)
		elbv2ErrorXML(w, "InvalidConfigurationRequest",
			"Could not provision the load balancer listener data plane: "+err.Error(),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	for _, action := range listener.DefaultActions {
		if action.TargetGroupArn != "" {
			elbv2TargetGroups.Update(action.TargetGroupArn, func(tg *ELBv2TargetGroup) {
				tg.LoadBalancerArns = appendUnique(tg.LoadBalancerArns, lbArn)
			})
		}
	}
	elbv2XMLResponse(w, "CreateListener", "<Listeners>"+elbv2ListenerXML(listener)+"</Listeners>", sim.RequestID(r.Context()))
}

// A bind the host refuses does not fail the call: Elastic Load Balancing never
// rejects CreateListener because another process holds a port, and a simulator
// sharing its host's network namespace cannot bind port 22 next to sshd. The
// listener serves its control plane and the log reports the lost bind.
func elbv2StartListenerDataPlane(listener ELBv2Listener) error {
	if err := elbv2StartListenerProxy(listener); err != nil {
		if !elbv2HostCannotOfferAddress(err) {
			return err
		}
		elbv2LogDataPlaneUnavailable(listener, err)
	}
	return nil
}

func elbv2LogDataPlaneUnavailable(listener ELBv2Listener, err error) {
	fmt.Fprintf(os.Stderr,
		"[sim-elbv2] listener %s (port %d): the host will not offer this address, so the listener has no data plane on this machine; its control plane is unaffected: %v\n",
		listener.Arn, listener.Port, err)
}

func handleELBv2DescribeListeners(w http.ResponseWriter, r *http.Request) {
	listeners, nf := filterELBv2Listeners(r)
	if nf != nil {
		elbv2ErrorXML(w, nf.code, nf.message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<Listeners>")
	for _, listener := range listeners {
		b.WriteString(elbv2ListenerXML(listener))
	}
	b.WriteString("</Listeners>")
	elbv2XMLResponse(w, "DescribeListeners", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2DescribeListenerAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ListenerArn")
	listener, ok := elbv2Listeners.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "ListenerNotFound", "Listener not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "DescribeListenerAttributes", elbv2AttributesXML("Attributes", elbv2ListenerAttributes(listener)), sim.RequestID(r.Context()))
}

func handleELBv2ModifyListenerAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ListenerArn")
	if !elbv2Listeners.Update(arn, func(listener *ELBv2Listener) {
		if listener.Attributes == nil {
			listener.Attributes = elbv2ListenerAttributes(*listener)
		}
		for k, v := range parseELBv2Attributes(r, "Attributes") {
			listener.Attributes[k] = v
		}
	}) {
		elbv2ErrorXML(w, "ListenerNotFound", "Listener not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	listener, _ := elbv2Listeners.Get(arn)
	elbv2XMLResponse(w, "ModifyListenerAttributes", elbv2AttributesXML("Attributes", elbv2ListenerAttributes(listener)), sim.RequestID(r.Context()))
}

func handleELBv2DeleteListener(w http.ResponseWriter, r *http.Request) {
	elbv2DeleteListener(r.FormValue("ListenerArn"))
	elbv2XMLResponse(w, "DeleteListener", "", sim.RequestID(r.Context()))
}

func elbv2DeleteListener(arn string) {
	elbv2Listeners.Delete(arn)
	for _, rule := range elbv2Rules.Filter(func(rule ELBv2Rule) bool { return rule.ListenerArn == arn }) {
		elbv2Rules.Delete(rule.Arn)
	}
	elbv2StopListenerProxy(arn)
}

// Writes the not-found error of the first ARN no resource holds.
func elbv2TaggedResourcesExist(w http.ResponseWriter, r *http.Request, arns []string) bool {
	for _, arn := range arns {
		if _, ok := elbv2ResourceTags(arn); ok {
			continue
		}
		code, message := "ValidationError", fmt.Sprintf("'%s' is not a valid resource ARN", arn)
		for _, kind := range []struct{ marker, code, noun string }{
			{":loadbalancer/", "LoadBalancerNotFound", "Load balancer"},
			{":targetgroup/", "TargetGroupNotFound", "Target group"},
			{":listener/", "ListenerNotFound", "Listener"},
			{":listener-rule/", "RuleNotFound", "Rule"},
			{":truststore/", "TrustStoreNotFound", "Trust store"},
		} {
			if strings.Contains(arn, kind.marker) {
				code, message = kind.code, fmt.Sprintf("%s '%s' not found", kind.noun, arn)
				break
			}
		}
		elbv2ErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return false
	}
	return true
}

func handleELBv2AddTags(w http.ResponseWriter, r *http.Request) {
	tags := parseELBv2Tags(r, "Tags")
	arns := queryList(r, "ResourceArns")
	if !elbv2TaggedResourcesExist(w, r, arns) {
		return
	}
	for _, arn := range arns {
		elbv2SetResourceTags(arn, tags, false)
	}
	elbv2XMLResponse(w, "AddTags", "", sim.RequestID(r.Context()))
}

func handleELBv2RemoveTags(w http.ResponseWriter, r *http.Request) {
	keys := queryList(r, "TagKeys")
	arns := queryList(r, "ResourceArns")
	if !elbv2TaggedResourcesExist(w, r, arns) {
		return
	}
	for _, arn := range arns {
		elbv2SetResourceTags(arn, keysToMap(keys), true)
	}
	elbv2XMLResponse(w, "RemoveTags", "", sim.RequestID(r.Context()))
}

func handleELBv2DescribeTags(w http.ResponseWriter, r *http.Request) {
	arns := queryList(r, "ResourceArns")
	if !elbv2TaggedResourcesExist(w, r, arns) {
		return
	}
	var b strings.Builder
	b.WriteString("<TagDescriptions>")
	for _, arn := range arns {
		fmt.Fprintf(&b, "<member><ResourceArn>%s</ResourceArn><Tags>", xmlEscape(arn))
		tags, _ := elbv2ResourceTags(arn)
		for k, v := range tags {
			fmt.Fprintf(&b, "<member><Key>%s</Key><Value>%s</Value></member>", xmlEscape(k), xmlEscape(v))
		}
		b.WriteString("</Tags></member>")
	}
	b.WriteString("</TagDescriptions>")
	elbv2XMLResponse(w, "DescribeTags", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2DescribeAccountLimits(w http.ResponseWriter, r *http.Request) {
	body := `<Limits><member><Name>application-load-balancers</Name><Max>50</Max></member><member><Name>target-groups</Name><Max>3000</Max></member><member><Name>listeners-per-application-load-balancer</Name><Max>50</Max></member></Limits>`
	elbv2XMLResponse(w, "DescribeAccountLimits", body, sim.RequestID(r.Context()))
}

// The typed error a Describe raises for an absent explicit identifier.
type elbv2NotFound struct {
	code    string
	message string
}

func filterELBv2LoadBalancers(r *http.Request) ([]ELBv2LoadBalancer, *elbv2NotFound) {
	return elbv2FindByARNOrName(r, "LoadBalancerArns", elbv2LoadBalancers, func(it ELBv2LoadBalancer) string { return it.Name }, "LoadBalancerNotFound", "Load balancers")
}

func filterELBv2TargetGroups(r *http.Request) ([]ELBv2TargetGroup, *elbv2NotFound) {
	if arns := queryList(r, "TargetGroupArns"); len(arns) > 0 {
		var out []ELBv2TargetGroup
		for _, arn := range arns {
			tg, ok := elbv2TargetGroups.Get(arn)
			if !ok {
				return nil, &elbv2NotFound{"TargetGroupNotFound", fmt.Sprintf("Target groups '[%s]' not found", arn)}
			}
			out = append(out, tg)
		}
		return out, nil
	}
	if names := queryList(r, "Names"); len(names) > 0 {
		byName := make(map[string]ELBv2TargetGroup)
		for _, tg := range elbv2TargetGroups.List() {
			byName[tg.Name] = tg
		}
		var out []ELBv2TargetGroup
		for _, n := range names {
			tg, ok := byName[n]
			if !ok {
				return nil, &elbv2NotFound{"TargetGroupNotFound", fmt.Sprintf("Target groups '[%s]' not found", n)}
			}
			out = append(out, tg)
		}
		return out, nil
	}
	if lbArn := r.FormValue("LoadBalancerArn"); lbArn != "" {
		return elbv2TargetGroups.Filter(func(tg ELBv2TargetGroup) bool {
			return containsString(tg.LoadBalancerArns, lbArn)
		}), nil
	}
	return elbv2TargetGroups.List(), nil
}

func filterELBv2Listeners(r *http.Request) ([]ELBv2Listener, *elbv2NotFound) {
	if arns := queryList(r, "ListenerArns"); len(arns) > 0 {
		var out []ELBv2Listener
		for _, arn := range arns {
			listener, ok := elbv2Listeners.Get(arn)
			if !ok {
				return nil, &elbv2NotFound{"ListenerNotFound", fmt.Sprintf("Listener '%s' not found", arn)}
			}
			out = append(out, listener)
		}
		return out, nil
	}
	if lbArn := r.FormValue("LoadBalancerArn"); lbArn != "" {
		return elbv2Listeners.Filter(func(l ELBv2Listener) bool { return l.LoadBalancerArn == lbArn }), nil
	}
	return elbv2Listeners.List(), nil
}

// Network Load Balancers carry the attribute (default "on");
// Application Load Balancers do not expose it.
func elbv2DefaultEnforceSG(lbType, requested string) string {
	if requested != "" {
		return requested
	}
	if lbType == "network" {
		return "on"
	}
	return ""
}

func elbv2LoadBalancerXML(lb ELBv2LoadBalancer) string {
	extra := ""
	if lb.EnforceSGInboundOnPrivate != "" {
		extra += fmt.Sprintf("<EnforceSecurityGroupInboundRulesOnPrivateLinkTraffic>%s</EnforceSecurityGroupInboundRulesOnPrivateLinkTraffic>", xmlEscape(lb.EnforceSGInboundOnPrivate))
	}
	if lb.CustomerOwnedIpv4Pool != "" {
		extra += fmt.Sprintf("<CustomerOwnedIpv4Pool>%s</CustomerOwnedIpv4Pool>", xmlEscape(lb.CustomerOwnedIpv4Pool))
	}
	zone := ""
	if lb.CanonicalZone != "" {
		zone = "<CanonicalHostedZoneId>" + xmlEscape(lb.CanonicalZone) + "</CanonicalHostedZoneId>"
	}
	return fmt.Sprintf(`<member><LoadBalancerArn>%s</LoadBalancerArn><DNSName>%s</DNSName>%s<CreatedTime>%s</CreatedTime><LoadBalancerName>%s</LoadBalancerName><Scheme>%s</Scheme><VpcId>%s</VpcId><State><Code>%s</Code></State><Type>%s</Type><AvailabilityZones>%s</AvailabilityZones><SecurityGroups>%s</SecurityGroups><IpAddressType>%s</IpAddressType>%s</member>`,
		xmlEscape(lb.Arn), xmlEscape(lb.DNSName), zone, xmlEscape(lb.CreatedTime), xmlEscape(lb.Name),
		xmlEscape(lb.Scheme), xmlEscape(lb.VpcID), xmlEscape(lb.State), xmlEscape(lb.Type), elbv2AvailabilityZonesXML(lb),
		elbv2StringMembersXMLInner(lb.SecurityGroups), xmlEscape(lb.IpAddressType), extra)
}

// In place.
func handleELBv2SetIpAddressType(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	ipType := r.FormValue("IpAddressType")
	current, ok := elbv2LoadBalancers.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if code, message := elbv2IpAddressTypeRefusal(ipType, current.Subnets); code != "" {
		elbv2ErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if !elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		if ipType != "" {
			lb.IpAddressType = ipType
		}
	}) {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	lb, _ := elbv2LoadBalancers.Get(arn)
	elbv2XMLResponse(w, "SetIpAddressType", fmt.Sprintf("<IpAddressType>%s</IpAddressType>", xmlEscape(lb.IpAddressType)), sim.RequestID(r.Context()))
}

// With each mapping's Elastic IP allocation, private IPv4, IPv6 address and
// source NAT prefix.
func elbv2AvailabilityZonesXML(lb ELBv2LoadBalancer) string {
	var b strings.Builder
	for _, subnet := range lb.Subnets {
		az := awsAvailabilityZone()
		if s, ok := ec2Subnets.Get(subnet); ok && s.AvailabilityZone != "" {
			az = s.AvailabilityZone
		}
		fmt.Fprintf(&b, "<member><ZoneName>%s</ZoneName><SubnetId>%s</SubnetId>", xmlEscape(az), xmlEscape(subnet))
		for _, mapping := range lb.SubnetMappings {
			if mapping.SubnetId != subnet {
				continue
			}
			if mapping.AllocationId != "" || mapping.PrivateIPv4Address != "" || mapping.IPv6Address != "" {
				b.WriteString("<LoadBalancerAddresses><member>")
				if mapping.AllocationId != "" {
					eip, _ := ec2ElasticIPs.Get(mapping.AllocationId)
					fmt.Fprintf(&b, "<IpAddress>%s</IpAddress><AllocationId>%s</AllocationId>", xmlEscape(eip.PublicIp), xmlEscape(mapping.AllocationId))
				}
				if mapping.PrivateIPv4Address != "" {
					fmt.Fprintf(&b, "<IpAddress>%s</IpAddress><PrivateIPv4Address>%s</PrivateIPv4Address>", xmlEscape(mapping.PrivateIPv4Address), xmlEscape(mapping.PrivateIPv4Address))
				}
				if mapping.IPv6Address != "" {
					fmt.Fprintf(&b, "<IPv6Address>%s</IPv6Address>", xmlEscape(mapping.IPv6Address))
				}
				b.WriteString("</member></LoadBalancerAddresses>")
			}
			if mapping.SourceNatIpv6Prefix != "" {
				fmt.Fprintf(&b, "<SourceNatIpv6Prefixes><member>%s</member></SourceNatIpv6Prefixes>", xmlEscape(mapping.SourceNatIpv6Prefix))
			}
		}
		b.WriteString("</member>")
	}
	return b.String()
}

// Matcher and HealthCheckPath belong to HTTP and HTTPS health checks only; AWS
// rejects and omits them for other protocols, and emitting them breaks
// terraform idempotency.
func elbv2HTTPHealthCheck(healthCheckProtocol string) bool {
	switch healthCheckProtocol {
	case "HTTP", "HTTPS":
		return true
	default:
		return false
	}
}

// Gate on elbv2HTTPHealthCheck.
func elbv2DefaultMatcher() string {
	return "200"
}

// For non-HTTP checks it stays empty unless supplied.
func elbv2DefaultedMatcher(healthCheckProtocol, requested string) string {
	if requested != "" {
		return requested
	}
	if elbv2HTTPHealthCheck(healthCheckProtocol) {
		return elbv2DefaultMatcher()
	}
	return ""
}

// For non-HTTP checks it stays empty unless supplied; a "/" default there
// breaks terraform idempotency.
func elbv2DefaultedHealthCheckPath(healthCheckProtocol, requested string) string {
	if requested != "" {
		return requested
	}
	if elbv2HTTPHealthCheck(healthCheckProtocol) {
		return "/"
	}
	return ""
}

// HTTP and HTTPS target groups default to HTTP1; others carry none.
func elbv2DefaultProtocolVersion(protocol, requested string) string {
	if requested != "" {
		return requested
	}
	if protocol == "HTTP" || protocol == "HTTPS" {
		return "HTTP1"
	}
	return ""
}

func elbv2TargetGroupXML(tg ELBv2TargetGroup) string {
	// Return a Matcher only for HTTP/HTTPS checks, or a gRPC check's GrpcCode;
	// otherwise terraform plans a perpetual diff.
	var matcher string
	switch {
	case tg.MatcherGrpcCode != "":
		matcher = fmt.Sprintf("<Matcher><GrpcCode>%s</GrpcCode></Matcher>", xmlEscape(tg.MatcherGrpcCode))
	case elbv2HTTPHealthCheck(tg.HealthCheckProtocol):
		code := tg.MatcherHttpCode
		if code == "" {
			code = elbv2DefaultMatcher()
		}
		matcher = fmt.Sprintf("<Matcher><HttpCode>%s</HttpCode></Matcher>", xmlEscape(code))
	}
	// HTTP-only, like Matcher.
	healthCheckPath := ""
	if elbv2HTTPHealthCheck(tg.HealthCheckProtocol) {
		p := tg.HealthCheckPath
		if p == "" {
			p = "/"
		}
		healthCheckPath = fmt.Sprintf("<HealthCheckPath>%s</HealthCheckPath>", xmlEscape(p))
	}
	protoVer := ""
	if tg.ProtocolVersion != "" {
		protoVer = fmt.Sprintf("<ProtocolVersion>%s</ProtocolVersion>", xmlEscape(tg.ProtocolVersion))
	}
	ipType := tg.IpAddressType
	if ipType == "" {
		ipType = "ipv4"
	}
	return fmt.Sprintf(`<member><TargetGroupArn>%s</TargetGroupArn><TargetGroupName>%s</TargetGroupName><Protocol>%s</Protocol>%s<Port>%d</Port><VpcId>%s</VpcId><HealthCheckProtocol>%s</HealthCheckProtocol><HealthCheckPort>%s</HealthCheckPort><HealthCheckEnabled>%t</HealthCheckEnabled><HealthCheckIntervalSeconds>%d</HealthCheckIntervalSeconds><HealthCheckTimeoutSeconds>%d</HealthCheckTimeoutSeconds><HealthyThresholdCount>%d</HealthyThresholdCount><UnhealthyThresholdCount>%d</UnhealthyThresholdCount>%s%s<LoadBalancerArns>%s</LoadBalancerArns><TargetType>%s</TargetType><IpAddressType>%s</IpAddressType></member>`,
		xmlEscape(tg.Arn), xmlEscape(tg.Name), xmlEscape(tg.Protocol), protoVer, tg.Port, xmlEscape(tg.VpcID),
		xmlEscape(tg.HealthCheckProtocol), xmlEscape(tg.HealthCheckPort), tg.HealthCheckEnabled,
		tg.HealthCheckInterval, tg.HealthCheckTimeout, tg.HealthyThresholdCount, tg.UnhealthyThresholdCount,
		healthCheckPath, matcher, elbv2StringMembersXMLInner(tg.LoadBalancerArns), xmlEscape(tg.TargetType), xmlEscape(ipType))
}

func elbv2ListenerXML(listener ELBv2Listener) string {
	var certs strings.Builder
	if len(listener.Certificates) > 0 {
		certs.WriteString("<Certificates>")
		for _, c := range listener.Certificates {
			fmt.Fprintf(&certs, "<member><CertificateArn>%s</CertificateArn></member>", xmlEscape(c))
		}
		certs.WriteString("</Certificates>")
	}
	var sslPolicy string
	if listener.SslPolicy != "" {
		sslPolicy = fmt.Sprintf("<SslPolicy>%s</SslPolicy>", xmlEscape(listener.SslPolicy))
	}
	var alpn string
	if len(listener.AlpnPolicy) > 0 {
		alpn = elbv2StringMembersXML("AlpnPolicy", listener.AlpnPolicy)
	}
	return fmt.Sprintf(`<member><ListenerArn>%s</ListenerArn><LoadBalancerArn>%s</LoadBalancerArn><Port>%d</Port><Protocol>%s</Protocol>%s%s%s%s%s</member>`,
		xmlEscape(listener.Arn), xmlEscape(listener.LoadBalancerArn), listener.Port, xmlEscape(listener.Protocol),
		elbv2ActionsXML("DefaultActions", listener.DefaultActions), certs.String(), sslPolicy, alpn,
		elbv2MutualAuthXML(listener.MutualAuth))
}

func elbv2MutualAuthXML(m *ELBv2MutualAuth) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("<MutualAuthentication>")
	if m.Mode != "" {
		fmt.Fprintf(&b, "<Mode>%s</Mode>", xmlEscape(m.Mode))
	}
	if m.TrustStoreArn != "" {
		fmt.Fprintf(&b, "<TrustStoreArn>%s</TrustStoreArn>", xmlEscape(m.TrustStoreArn))
	}
	if m.IgnoreClientCertificateExpiry != nil {
		fmt.Fprintf(&b, "<IgnoreClientCertificateExpiry>%t</IgnoreClientCertificateExpiry>", *m.IgnoreClientCertificateExpiry)
	}
	if m.AdvertiseTrustStoreCaNames != "" {
		fmt.Fprintf(&b, "<AdvertiseTrustStoreCaNames>%s</AdvertiseTrustStoreCaNames>", xmlEscape(m.AdvertiseTrustStoreCaNames))
	}
	b.WriteString("</MutualAuthentication>")
	return b.String()
}

// The default `off` mode carries no trust store; model the block only when a
// mode or trust store is supplied.
func parseELBv2MutualAuth(r *http.Request) *ELBv2MutualAuth {
	mode := r.FormValue("MutualAuthentication.Mode")
	trustStore := r.FormValue("MutualAuthentication.TrustStoreArn")
	if mode == "" && trustStore == "" {
		return nil
	}
	m := &ELBv2MutualAuth{
		Mode:                       mode,
		TrustStoreArn:              trustStore,
		AdvertiseTrustStoreCaNames: r.FormValue("MutualAuthentication.AdvertiseTrustStoreCaNames"),
	}
	if v := r.FormValue("MutualAuthentication.IgnoreClientCertificateExpiry"); v != "" {
		b := v == "true"
		m.IgnoreClientCertificateExpiry = &b
	}
	return m
}

func elbv2TargetXML(target ELBv2TargetDescription) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<Id>%s</Id>", xmlEscape(target.ID))
	if target.Port != 0 {
		fmt.Fprintf(&b, "<Port>%d</Port>", target.Port)
	}
	if target.AvailabilityZone != "" {
		fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(target.AvailabilityZone))
	}
	return b.String()
}

func elbv2AttributesXML(wrapper string, attrs map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", wrapper)
	for k, v := range attrs {
		fmt.Fprintf(&b, "<member><Key>%s</Key><Value>%s</Value></member>", xmlEscape(k), xmlEscape(v))
	}
	fmt.Fprintf(&b, "</%s>", wrapper)
	return b.String()
}

func parseELBv2Tags(r *http.Request, prefix string) map[string]string {
	tags := map[string]string{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("%s.member.%d.Key", prefix, i))
		if k == "" {
			break
		}
		tags[k] = r.FormValue(fmt.Sprintf("%s.member.%d.Value", prefix, i))
	}
	return tags
}

func parseELBv2Attributes(r *http.Request, prefix string) map[string]string {
	attrs := map[string]string{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("%s.member.%d.Key", prefix, i))
		if k == "" {
			break
		}
		attrs[k] = r.FormValue(fmt.Sprintf("%s.member.%d.Value", prefix, i))
	}
	return attrs
}

func parseELBv2Targets(r *http.Request) []ELBv2TargetDescription {
	var targets []ELBv2TargetDescription
	for i := 1; ; i++ {
		id := r.FormValue(fmt.Sprintf("Targets.member.%d.Id", i))
		if id == "" {
			break
		}
		targets = append(targets, ELBv2TargetDescription{
			ID:               id,
			Port:             atoiDefault(r.FormValue(fmt.Sprintf("Targets.member.%d.Port", i)), 0),
			AvailabilityZone: r.FormValue(fmt.Sprintf("Targets.member.%d.AvailabilityZone", i)),
		})
	}
	return targets
}

func parseELBv2Actions(r *http.Request) []ELBv2Action {
	return parseELBv2ActionsPrefix(r, "DefaultActions")
}

// prefix is DefaultActions for listeners and Actions for rules.
func parseELBv2ActionsPrefix(r *http.Request, prefix string) []ELBv2Action {
	var actions []ELBv2Action
	for i := 1; ; i++ {
		base := fmt.Sprintf("%s.member.%d", prefix, i)
		actionType := r.FormValue(base + ".Type")
		if actionType == "" {
			break
		}
		action := ELBv2Action{
			Type:           actionType,
			TargetGroupArn: r.FormValue(base + ".TargetGroupArn"),
			Order:          atoiDefault(r.FormValue(base+".Order"), 0),
		}
		if sc := r.FormValue(base + ".FixedResponseConfig.StatusCode"); sc != "" {
			action.FixedResponse = &ELBv2FixedResponseConfig{
				StatusCode:  sc,
				ContentType: r.FormValue(base + ".FixedResponseConfig.ContentType"),
				MessageBody: r.FormValue(base + ".FixedResponseConfig.MessageBody"),
			}
		}
		if r.FormValue(base+".RedirectConfig.StatusCode") != "" {
			action.Redirect = &ELBv2RedirectConfig{
				Protocol:   r.FormValue(base + ".RedirectConfig.Protocol"),
				Port:       r.FormValue(base + ".RedirectConfig.Port"),
				Host:       r.FormValue(base + ".RedirectConfig.Host"),
				Path:       r.FormValue(base + ".RedirectConfig.Path"),
				Query:      r.FormValue(base + ".RedirectConfig.Query"),
				StatusCode: r.FormValue(base + ".RedirectConfig.StatusCode"),
			}
		}
		if fwd := parseELBv2ForwardConfig(r, base+".ForwardConfig"); fwd != nil {
			action.Forward = fwd
		}
		if oidc := parseELBv2AuthOidc(r, base+".AuthenticateOidcConfig"); oidc != nil {
			action.AuthOidc = oidc
		}
		if cog := parseELBv2AuthCognito(r, base+".AuthenticateCognitoConfig"); cog != nil {
			action.AuthCognito = cog
		}
		actions = append(actions, action)
	}
	return actions
}

// Stickiness duration's "range is 1-604800 seconds (7 days). You must specify
// this value when enabling target group stickiness."
func elbv2ActionsRefusal(actions []ELBv2Action) (code, message string) {
	for _, action := range actions {
		if action.Forward == nil || action.Forward.Stickiness == nil || !action.Forward.Stickiness.Enabled {
			continue
		}
		duration := action.Forward.Stickiness.DurationSeconds
		if duration == nil {
			return "ValidationError", "TargetGroupStickinessConfig.DurationSeconds must be specified when target group stickiness is enabled"
		}
		if *duration < 1 || *duration > 604800 {
			return "ValidationError", fmt.Sprintf("TargetGroupStickinessConfig.DurationSeconds '%d' must be in the range 1-604800", *duration)
		}
	}
	return "", ""
}

func elbv2ActionsXML(wrapper string, actions []ELBv2Action) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", wrapper)
	for _, action := range actions {
		fmt.Fprintf(&b, "<member><Type>%s</Type>", xmlEscape(action.Type))
		if action.TargetGroupArn != "" {
			fmt.Fprintf(&b, "<TargetGroupArn>%s</TargetGroupArn>", xmlEscape(action.TargetGroupArn))
		}
		if action.Order != 0 {
			fmt.Fprintf(&b, "<Order>%d</Order>", action.Order)
		}
		if fr := action.FixedResponse; fr != nil {
			b.WriteString("<FixedResponseConfig>")
			fmt.Fprintf(&b, "<StatusCode>%s</StatusCode>", xmlEscape(fr.StatusCode))
			if fr.ContentType != "" {
				fmt.Fprintf(&b, "<ContentType>%s</ContentType>", xmlEscape(fr.ContentType))
			}
			if fr.MessageBody != "" {
				fmt.Fprintf(&b, "<MessageBody>%s</MessageBody>", xmlEscape(fr.MessageBody))
			}
			b.WriteString("</FixedResponseConfig>")
		}
		if rd := action.Redirect; rd != nil {
			b.WriteString("<RedirectConfig>")
			fmt.Fprintf(&b, "<StatusCode>%s</StatusCode>", xmlEscape(rd.StatusCode))
			for _, kv := range []struct{ name, val string }{
				{"Protocol", rd.Protocol}, {"Port", rd.Port}, {"Host", rd.Host},
				{"Path", rd.Path}, {"Query", rd.Query},
			} {
				if kv.val != "" {
					fmt.Fprintf(&b, "<%s>%s</%s>", kv.name, xmlEscape(kv.val), kv.name)
				}
			}
			b.WriteString("</RedirectConfig>")
		}
		if fwd := action.Forward; fwd != nil {
			b.WriteString(elbv2ForwardConfigXML(fwd))
		}
		if oidc := action.AuthOidc; oidc != nil {
			b.WriteString(elbv2AuthOidcXML(oidc))
		}
		if cog := action.AuthCognito; cog != nil {
			b.WriteString(elbv2AuthCognitoXML(cog))
		}
		b.WriteString("</member>")
	}
	fmt.Fprintf(&b, "</%s>", wrapper)
	return b.String()
}

func parseELBv2ForwardConfig(r *http.Request, base string) *ELBv2ForwardConfig {
	var tgs []ELBv2TargetGroupTuple
	for i := 1; ; i++ {
		m := fmt.Sprintf("%s.TargetGroups.member.%d", base, i)
		arn := r.FormValue(m + ".TargetGroupArn")
		if arn == "" {
			break
		}
		tuple := ELBv2TargetGroupTuple{TargetGroupArn: arn}
		if w := r.FormValue(m + ".Weight"); w != "" {
			weight := atoiDefault(w, 1)
			tuple.Weight = &weight
		}
		tgs = append(tgs, tuple)
	}
	var stickiness *ELBv2TargetGroupStickiness
	if e := r.FormValue(base + ".TargetGroupStickinessConfig.Enabled"); e != "" {
		st := &ELBv2TargetGroupStickiness{Enabled: e == "true"}
		if d := r.FormValue(base + ".TargetGroupStickinessConfig.DurationSeconds"); d != "" {
			dur := atoiDefault(d, 0)
			st.DurationSeconds = &dur
		}
		stickiness = st
	}
	if len(tgs) == 0 && stickiness == nil {
		return nil
	}
	return &ELBv2ForwardConfig{TargetGroups: tgs, Stickiness: stickiness}
}

func parseELBv2AuthOidc(r *http.Request, base string) *ELBv2AuthOidcConfig {
	issuer := r.FormValue(base + ".Issuer")
	clientID := r.FormValue(base + ".ClientId")
	if issuer == "" && clientID == "" {
		return nil
	}
	cfg := &ELBv2AuthOidcConfig{
		Issuer:                           issuer,
		AuthorizationEndpoint:            r.FormValue(base + ".AuthorizationEndpoint"),
		TokenEndpoint:                    r.FormValue(base + ".TokenEndpoint"),
		UserInfoEndpoint:                 r.FormValue(base + ".UserInfoEndpoint"),
		ClientId:                         clientID,
		Scope:                            r.FormValue(base + ".Scope"),
		SessionCookieName:                r.FormValue(base + ".SessionCookieName"),
		OnUnauthenticatedRequest:         r.FormValue(base + ".OnUnauthenticatedRequest"),
		AuthenticationRequestExtraParams: parseELBv2ExtraParams(r, base+".AuthenticationRequestExtraParams"),
	}
	if t := r.FormValue(base + ".SessionTimeout"); t != "" {
		v := int64(atoiDefault(t, 604800))
		cfg.SessionTimeout = &v
	}
	return cfg
}

func parseELBv2AuthCognito(r *http.Request, base string) *ELBv2AuthCognitoConfig {
	poolArn := r.FormValue(base + ".UserPoolArn")
	clientID := r.FormValue(base + ".UserPoolClientId")
	if poolArn == "" && clientID == "" {
		return nil
	}
	cfg := &ELBv2AuthCognitoConfig{
		UserPoolArn:                      poolArn,
		UserPoolClientId:                 clientID,
		UserPoolDomain:                   r.FormValue(base + ".UserPoolDomain"),
		Scope:                            r.FormValue(base + ".Scope"),
		SessionCookieName:                r.FormValue(base + ".SessionCookieName"),
		OnUnauthenticatedRequest:         r.FormValue(base + ".OnUnauthenticatedRequest"),
		AuthenticationRequestExtraParams: parseELBv2ExtraParams(r, base+".AuthenticationRequestExtraParams"),
	}
	if t := r.FormValue(base + ".SessionTimeout"); t != "" {
		v := int64(atoiDefault(t, 604800))
		cfg.SessionTimeout = &v
	}
	return cfg
}

// The query-protocol map `.entry.N.key/.value`.
func parseELBv2ExtraParams(r *http.Request, base string) map[string]string {
	out := map[string]string{}
	for i := 1; ; i++ {
		m := fmt.Sprintf("%s.entry.%d", base, i)
		k := r.FormValue(m + ".key")
		if k == "" {
			break
		}
		out[k] = r.FormValue(m + ".value")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func elbv2ForwardConfigXML(fwd *ELBv2ForwardConfig) string {
	var b strings.Builder
	b.WriteString("<ForwardConfig><TargetGroups>")
	for _, tg := range fwd.TargetGroups {
		fmt.Fprintf(&b, "<member><TargetGroupArn>%s</TargetGroupArn>", xmlEscape(tg.TargetGroupArn))
		if tg.Weight != nil {
			fmt.Fprintf(&b, "<Weight>%d</Weight>", *tg.Weight)
		}
		b.WriteString("</member>")
	}
	b.WriteString("</TargetGroups>")
	if st := fwd.Stickiness; st != nil {
		b.WriteString("<TargetGroupStickinessConfig>")
		fmt.Fprintf(&b, "<Enabled>%t</Enabled>", st.Enabled)
		if st.DurationSeconds != nil {
			fmt.Fprintf(&b, "<DurationSeconds>%d</DurationSeconds>", *st.DurationSeconds)
		}
		b.WriteString("</TargetGroupStickinessConfig>")
	}
	b.WriteString("</ForwardConfig>")
	return b.String()
}

func elbv2AuthOidcXML(o *ELBv2AuthOidcConfig) string {
	var b strings.Builder
	b.WriteString("<AuthenticateOidcConfig>")
	for _, kv := range []struct{ name, val string }{
		{"Issuer", o.Issuer}, {"AuthorizationEndpoint", o.AuthorizationEndpoint},
		{"TokenEndpoint", o.TokenEndpoint}, {"UserInfoEndpoint", o.UserInfoEndpoint},
		{"ClientId", o.ClientId}, {"Scope", o.Scope}, {"SessionCookieName", o.SessionCookieName},
		{"OnUnauthenticatedRequest", o.OnUnauthenticatedRequest},
	} {
		if kv.val != "" {
			fmt.Fprintf(&b, "<%s>%s</%s>", kv.name, xmlEscape(kv.val), kv.name)
		}
	}
	if o.SessionTimeout != nil {
		fmt.Fprintf(&b, "<SessionTimeout>%d</SessionTimeout>", *o.SessionTimeout)
	}
	b.WriteString(elbv2ExtraParamsXML(o.AuthenticationRequestExtraParams))
	b.WriteString("</AuthenticateOidcConfig>")
	return b.String()
}

func elbv2AuthCognitoXML(c *ELBv2AuthCognitoConfig) string {
	var b strings.Builder
	b.WriteString("<AuthenticateCognitoConfig>")
	for _, kv := range []struct{ name, val string }{
		{"UserPoolArn", c.UserPoolArn}, {"UserPoolClientId", c.UserPoolClientId},
		{"UserPoolDomain", c.UserPoolDomain}, {"Scope", c.Scope},
		{"SessionCookieName", c.SessionCookieName}, {"OnUnauthenticatedRequest", c.OnUnauthenticatedRequest},
	} {
		if kv.val != "" {
			fmt.Fprintf(&b, "<%s>%s</%s>", kv.name, xmlEscape(kv.val), kv.name)
		}
	}
	if c.SessionTimeout != nil {
		fmt.Fprintf(&b, "<SessionTimeout>%d</SessionTimeout>", *c.SessionTimeout)
	}
	b.WriteString(elbv2ExtraParamsXML(c.AuthenticationRequestExtraParams))
	b.WriteString("</AuthenticateCognitoConfig>")
	return b.String()
}

// In key order.
func elbv2ExtraParamsXML(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("<AuthenticationRequestExtraParams>")
	for _, k := range keys {
		fmt.Fprintf(&b, "<entry><key>%s</key><value>%s</value></entry>", xmlEscape(k), xmlEscape(params[k]))
	}
	b.WriteString("</AuthenticationRequestExtraParams>")
	return b.String()
}

func queryList(r *http.Request, name string) []string {
	var values []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.member.%d", name, i))
		if v == "" {
			break
		}
		values = append(values, v)
	}
	return values
}

func elbv2StringMembersXML(wrapper string, values []string) string {
	return fmt.Sprintf("<%s>%s</%s>", wrapper, elbv2StringMembersXMLInner(values), wrapper)
}

func elbv2StringMembersXMLInner(values []string) string {
	var b strings.Builder
	for _, v := range values {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(v))
	}
	return b.String()
}

// idle_timeout.timeout_seconds: "The valid range is 1-4000 seconds. The
// default is 60 seconds."
const (
	elbv2DefaultIdleTimeoutSeconds = 60
	elbv2MinIdleTimeoutSeconds     = 1
	elbv2MaxIdleTimeoutSeconds     = 4000
)

// Attributes with a closed value set beyond true and false.
var elbv2EnumeratedLoadBalancerAttributes = map[string][]string{
	"routing.http.xff_header_processing.mode": {"append", "preserve", "remove"},
	"routing.http.desync_mitigation_mode":     {"monitor", "defensive", "strictest"},
	"dns_record.client_routing_policy":        {"availability_zone_affinity", "partial_availability_zone_affinity", "any_availability_zone"},
}

// Inclusive ranges.
var elbv2RangedLoadBalancerAttributes = map[string][2]int{
	"idle_timeout.timeout_seconds":           {elbv2MinIdleTimeoutSeconds, elbv2MaxIdleTimeoutSeconds},
	"client_keep_alive.seconds":              {60, 604800},
	"secondary_ips.auto_assigned.per_subnet": {0, 7},
}

// Every attribute the reference lists for the type, at its default; a bucket
// or prefix without default is empty. ipv6.deny_all_igw_traffic is true only
// when internal.
func defaultELBv2LoadBalancerAttributes(lbType, scheme string) map[string]string {
	attrs := map[string]string{
		"deletion_protection.enabled":       "false",
		"load_balancing.cross_zone.enabled": "false",
	}
	if lbType == "gateway" {
		return attrs
	}
	maps.Copy(attrs, map[string]string{
		"access_logs.s3.enabled":     "false",
		"access_logs.s3.bucket":      "",
		"access_logs.s3.prefix":      "",
		"ipv6.deny_all_igw_traffic":  strconv.FormatBool(scheme == "internal"),
		"zonal_shift.config.enabled": "false",
	})
	if lbType == "network" {
		attrs["dns_record.client_routing_policy"] = "any_availability_zone"
		attrs["secondary_ips.auto_assigned.per_subnet"] = "0"
		return attrs
	}
	maps.Copy(attrs, map[string]string{
		"load_balancing.cross_zone.enabled":                        "true",
		"idle_timeout.timeout_seconds":                             strconv.Itoa(elbv2DefaultIdleTimeoutSeconds),
		"client_keep_alive.seconds":                                "3600",
		"connection_logs.s3.enabled":                               "false",
		"connection_logs.s3.bucket":                                "",
		"connection_logs.s3.prefix":                                "",
		"health_check_logs.s3.enabled":                             "false",
		"health_check_logs.s3.bucket":                              "",
		"health_check_logs.s3.prefix":                              "",
		"routing.http.desync_mitigation_mode":                      "defensive",
		"routing.http.drop_invalid_header_fields.enabled":          "false",
		"routing.http.preserve_host_header.enabled":                "false",
		"routing.http.x_amzn_tls_version_and_cipher_suite.enabled": "false",
		"routing.http.xff_client_port.enabled":                     "false",
		"routing.http.xff_header_processing.mode":                  "append",
		"routing.http2.enabled":                                    "true",
		"waf.fail_open.enabled":                                    "false",
	})
	return attrs
}

func defaultELBv2TargetGroupAttributes() map[string]string {
	return map[string]string{
		"deregistration_delay.timeout_seconds": "300",
		"stickiness.enabled":                   "false",
		"load_balancing.cross_zone.enabled":    "use_load_balancer_configuration",
	}
}

func defaultELBv2ListenerAttributes(lbType string) map[string]string {
	if lbType == "network" || lbType == "gateway" {
		return map[string]string{
			"tcp.idle_timeout.seconds": "350",
		}
	}
	return map[string]string{
		"routing.http.response.server.enabled": "true",
	}
}

func elbv2ListenerAttributes(listener ELBv2Listener) map[string]string {
	if listener.Attributes != nil {
		return listener.Attributes
	}
	if lb, ok := elbv2LoadBalancers.Get(listener.LoadBalancerArn); ok {
		return defaultELBv2ListenerAttributes(lb.Type)
	}
	return defaultELBv2ListenerAttributes("application")
}

func elbv2VPCFromSubnets(subnets []string) string {
	for _, subnet := range subnets {
		if s, ok := ec2Subnets.Get(subnet); ok {
			return s.VpcId
		}
	}
	return ""
}

func elbv2LoadBalancerKind(lb ELBv2LoadBalancer) string {
	switch lb.Type {
	case "network":
		return "net"
	case "gateway":
		return "gwy"
	}
	return "app"
}

func elbv2LoadBalancerID(arn string) string {
	return arn[strings.LastIndex(arn, "/")+1:]
}

// remove deletes the entries' keys instead.
func elbv2SetResourceTags(arn string, entries map[string]string, remove bool) {
	apply := func(tags *map[string]string) {
		if *tags == nil {
			*tags = map[string]string{}
		}
		for k, v := range entries {
			if remove {
				delete(*tags, k)
			} else {
				(*tags)[k] = v
			}
		}
	}
	_ = elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) { apply(&lb.Tags) }) ||
		elbv2TargetGroups.Update(arn, func(tg *ELBv2TargetGroup) { apply(&tg.Tags) }) ||
		elbv2Listeners.Update(arn, func(l *ELBv2Listener) { apply(&l.Tags) }) ||
		elbv2Rules.Update(arn, func(rule *ELBv2Rule) { apply(&rule.Tags) }) ||
		elbv2TrustStores.Update(arn, func(ts *ELBv2TrustStore) { apply(&ts.Tags) })
}

func elbv2ResourceTags(arn string) (map[string]string, bool) {
	if lb, ok := elbv2LoadBalancers.Get(arn); ok {
		return lb.Tags, true
	}
	if tg, ok := elbv2TargetGroups.Get(arn); ok {
		return tg.Tags, true
	}
	if listener, ok := elbv2Listeners.Get(arn); ok {
		return listener.Tags, true
	}
	if rule, ok := elbv2Rules.Get(arn); ok {
		return rule.Tags, true
	}
	if ts, ok := elbv2TrustStores.Get(arn); ok {
		return ts.Tags, true
	}
	return nil, false
}

func keysToMap(keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = ""
	}
	return out
}

func appendUnique(values []string, incoming string) []string {
	if incoming == "" || containsString(values, incoming) {
		return values
	}
	return append(values, incoming)
}

func containsString(values []string, wanted string) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}

func removeString(values []string, remove string) []string {
	filtered := values[:0]
	for _, v := range values {
		if v != remove {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// Refuse a named resource that does not exist; with none named, return all.
func elbv2FindByARNOrName[T any](r *http.Request, arnParam string, store sim.Store[T], name func(T) string, notFoundCode, what string) ([]T, *elbv2NotFound) {
	if arns := queryList(r, arnParam); len(arns) > 0 {
		var out []T
		for _, arn := range arns {
			item, ok := store.Get(arn)
			if !ok {
				return nil, &elbv2NotFound{notFoundCode, fmt.Sprintf("%s '[%s]' not found", what, arn)}
			}
			out = append(out, item)
		}
		return out, nil
	}
	if names := queryList(r, "Names"); len(names) > 0 {
		byName := make(map[string]T)
		for _, item := range store.List() {
			byName[name(item)] = item
		}
		var out []T
		for _, n := range names {
			item, ok := byName[n]
			if !ok {
				return nil, &elbv2NotFound{notFoundCode, fmt.Sprintf("%s '[%s]' not found", what, n)}
			}
			out = append(out, item)
		}
		return out, nil
	}
	return store.List(), nil
}
