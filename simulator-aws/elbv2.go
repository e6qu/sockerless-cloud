package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

const elbv2APIVersion = "2015-12-01"

type ELBv2LoadBalancer struct {
	Arn                       string
	Name                      string
	DNSName                   string
	CanonicalZone             string
	Scheme                    string
	Type                      string
	State                     string
	VpcID                     string
	Subnets                   []string
	SecurityGroups            []string
	IpAddressType             string
	CustomerOwnedIpv4Pool     string
	EnforceSGInboundOnPrivate string
	CreatedTime               string
	Tags                      map[string]string
	Attributes                map[string]string
	// MinimumCapacityUnits is the reserved minimum capacity configured via
	// ModifyCapacityReservation (empty = none reserved).
	MinimumCapacityUnits string
	// Ipv4IpamPoolId is the IPAM pool assigned via ModifyIpPools.
	Ipv4IpamPoolId string
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
	Certificates    []string // the default certificate(s) set at create / ModifyListener
	SNICertificates []string // extra SNI certs added via AddListenerCertificates
	SslPolicy       string
	AlpnPolicy      []string
	MutualAuth      *ELBv2MutualAuth
	Attributes      map[string]string
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

// ELBv2ForwardConfig is the weighted multi-target-group forward (the
// `forward {}` block on aws_lb_listener / aws_lb_listener_rule). Distinct from
// the single top-level TargetGroupArn shorthand.
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

// ELBv2AuthOidcConfig backs the `authenticate-oidc` action (the Pomerium/IAP
// proxy ALB shape). ClientSecret is intentionally never stored or echoed — real
// ELBv2 treats it as write-only and never returns it from Describe*.
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

// ELBv2AuthCognitoConfig backs the `authenticate-cognito` action.
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
}

var (
	elbv2LoadBalancers sim.Store[ELBv2LoadBalancer]
	elbv2TargetGroups  sim.Store[ELBv2TargetGroup]
	elbv2Listeners     sim.Store[ELBv2Listener]
)

func registerELBv2(r *sim.AWSQueryRouter, srv *sim.Server) {
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
		if err := elbv2StartNLBProxy(listener); err != nil {
			return fmt.Errorf("restore stream listener %s: %w", listener.Arn, err)
		}
		if err := elbv2StartTLSProxy(listener); err != nil {
			return fmt.Errorf("restore TLS listener %s: %w", listener.Arn, err)
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
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w,
		`<ErrorResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		xmlEscape(code), xmlEscape(message), xmlEscape(requestID))
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
	for _, existing := range elbv2LoadBalancers.List() {
		if existing.Name == name {
			elbv2ErrorXML(w, "DuplicateLoadBalancerName",
				fmt.Sprintf("A load balancer with the same name '%s' exists, but with different settings.", name),
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
	}
	id := generateUUID()[:12]
	resourceKind := "app"
	if lbType == "network" {
		resourceKind = "net"
	}
	arn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:loadbalancer/%s/%s/%s", awsRegion(), awsAccountID(), resourceKind, name, id)
	lb := ELBv2LoadBalancer{
		Arn:            arn,
		Name:           name,
		DNSName:        fmt.Sprintf("%s-%s.elb.%s.amazonaws.com", name, id[:8], awsRegion()),
		CanonicalZone:  "Z35SXDOTRQ7X7K",
		Scheme:         scheme,
		Type:           lbType,
		State:          "active",
		VpcID:          elbv2VPCFromSubnets(queryList(r, "Subnets")),
		Subnets:        queryList(r, "Subnets"),
		SecurityGroups: queryList(r, "SecurityGroups"),
		IpAddressType:  ipType,
		// NLBs default this to "on"; ALBs don't carry it. Honour the request.
		EnforceSGInboundOnPrivate: elbv2DefaultEnforceSG(lbType, r.FormValue("EnforceSecurityGroupInboundRulesOnPrivateLinkTraffic")),
		CustomerOwnedIpv4Pool:     r.FormValue("CustomerOwnedIpv4Pool"),
		CreatedTime:               time.Now().UTC().Format(time.RFC3339),
		Tags:                      parseELBv2Tags(r, "Tags"),
		Attributes:                defaultELBv2LoadBalancerAttributes(),
	}
	elbv2LoadBalancers.Put(arn, lb)
	elbv2XMLResponse(w, "CreateLoadBalancer", "<LoadBalancers>"+elbv2LoadBalancerXML(lb)+"</LoadBalancers>", sim.RequestID(r.Context()))
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
	elbv2LoadBalancers.Delete(arn)
	for _, listener := range elbv2Listeners.Filter(func(l ELBv2Listener) bool { return l.LoadBalancerArn == arn }) {
		elbv2Listeners.Delete(listener.Arn)
		elbv2StopNLBProxy(listener.Arn)
		elbv2StopTLSProxy(listener.Arn)
	}
	for _, tg := range elbv2TargetGroups.List() {
		tg.LoadBalancerArns = removeString(tg.LoadBalancerArns, arn)
		elbv2TargetGroups.Put(tg.Arn, tg)
	}
	elbv2XMLResponse(w, "DeleteLoadBalancer", "", sim.RequestID(r.Context()))
}

func handleELBv2ModifyLoadBalancerAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	if !elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		for k, v := range parseELBv2Attributes(r, "Attributes") {
			lb.Attributes[k] = v
		}
	}) {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	attrs := defaultELBv2LoadBalancerAttributes()
	if lb, ok := elbv2LoadBalancers.Get(arn); ok {
		attrs = lb.Attributes
	}
	elbv2XMLResponse(w, "ModifyLoadBalancerAttributes", elbv2AttributesXML("Attributes", attrs), sim.RequestID(r.Context()))
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
	// MinimumLoadBalancerCapacity is omitted unless a minimum was actually
	// configured (via ModifyCapacityReservation, which the sim doesn't model).
	// Emitting CapacityUnits=0 makes the provider read a configured 0 and plan
	// "capacity_units = 0 -> null" on every idempotency check.
	body := fmt.Sprintf("<LastModifiedTime>%s</LastModifiedTime><DecreaseRequestsRemaining>10</DecreaseRequestsRemaining>%s",
		xmlEscape(lb.CreatedTime), states.String())
	elbv2XMLResponse(w, "DescribeCapacityReservation", body, sim.RequestID(r.Context()))
}

func handleELBv2SetSecurityGroups(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	groups := queryList(r, "SecurityGroups")
	if !elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		lb.SecurityGroups = groups
	}) {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "SetSecurityGroups", elbv2StringMembersXML("SecurityGroupIds", groups), sim.RequestID(r.Context()))
}

func handleELBv2SetSubnets(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	subnets := queryList(r, "Subnets")
	if !elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		lb.Subnets = subnets
		lb.VpcID = elbv2VPCFromSubnets(subnets)
	}) {
		elbv2ErrorXML(w, "LoadBalancerNotFound", "Load balancer not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "SetSubnets", "<AvailabilityZones>"+elbv2AvailabilityZonesXML(subnets)+"</AvailabilityZones>", sim.RequestID(r.Context()))
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
	id := generateUUID()[:12]
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

func handleELBv2DeleteTargetGroup(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	elbv2TargetGroups.Delete(arn)
	for _, listener := range elbv2Listeners.List() {
		changed := false
		for i := range listener.DefaultActions {
			if listener.DefaultActions[i].TargetGroupArn == arn {
				listener.DefaultActions[i].TargetGroupArn = ""
				changed = true
			}
		}
		if changed {
			elbv2Listeners.Put(listener.Arn, listener)
		}
	}
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
		// Keep the HTTP-only attributes consistent with the (possibly changed)
		// health-check protocol. HTTP/HTTPS: AWS supplies the "200" Matcher and "/"
		// path defaults when none were set. TCP/etc.: neither attribute applies, so
		// clear any value carried over from a prior HTTP health check (otherwise a
		// stale Matcher/HealthCheckPath leaks and breaks terraform idempotency).
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

func handleELBv2DeregisterTargets(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TargetGroupArn")
	targets := parseELBv2Targets(r)
	if !elbv2TargetGroups.Update(arn, func(tg *ELBv2TargetGroup) {
		filtered := tg.Targets[:0]
		for _, existing := range tg.Targets {
			remove := false
			for _, t := range targets {
				if existing.ID == t.ID && (t.Port == 0 || existing.Port == t.Port) {
					remove = true
					break
				}
			}
			if !remove {
				filtered = append(filtered, existing)
			}
		}
		tg.Targets = filtered
	}) {
		elbv2ErrorXML(w, "TargetGroupNotFound", "Target group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
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
	id := generateUUID()[:12]
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
	}
	elbv2Listeners.Put(arn, listener)
	if err := elbv2StartListenerDataPlane(listener); err != nil {
		elbv2Listeners.Delete(arn)
		elbv2StopNLBProxy(arn)
		elbv2StopTLSProxy(arn)
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

func elbv2StartListenerDataPlane(listener ELBv2Listener) error {
	if err := elbv2StartNLBProxy(listener); err != nil {
		return err
	}
	if err := elbv2StartTLSProxy(listener); err != nil {
		elbv2StopNLBProxy(listener.Arn)
		return err
	}
	return nil
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
	arn := r.FormValue("ListenerArn")
	elbv2Listeners.Delete(arn)
	elbv2StopNLBProxy(arn)
	elbv2StopTLSProxy(arn)
	elbv2XMLResponse(w, "DeleteListener", "", sim.RequestID(r.Context()))
}

func handleELBv2AddTags(w http.ResponseWriter, r *http.Request) {
	tags := parseELBv2Tags(r, "Tags")
	for _, arn := range queryList(r, "ResourceArns") {
		elbv2SetResourceTags(arn, tags, false)
	}
	elbv2XMLResponse(w, "AddTags", "", sim.RequestID(r.Context()))
}

func handleELBv2RemoveTags(w http.ResponseWriter, r *http.Request) {
	keys := queryList(r, "TagKeys")
	for _, arn := range queryList(r, "ResourceArns") {
		elbv2SetResourceTags(arn, keysToMap(keys), true)
	}
	elbv2XMLResponse(w, "RemoveTags", "", sim.RequestID(r.Context()))
}

func handleELBv2DescribeTags(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("<TagDescriptions>")
	for _, arn := range queryList(r, "ResourceArns") {
		fmt.Fprintf(&b, "<member><ResourceArn>%s</ResourceArn><Tags>", xmlEscape(arn))
		for k, v := range elbv2ResourceTags(arn) {
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

// elbv2NotFound names the typed error a Describe must raise when an explicit
// identifier in the request is absent from the store.
type elbv2NotFound struct {
	code    string
	message string
}

func filterELBv2LoadBalancers(r *http.Request) ([]ELBv2LoadBalancer, *elbv2NotFound) {
	if arns := queryList(r, "LoadBalancerArns"); len(arns) > 0 {
		var out []ELBv2LoadBalancer
		for _, arn := range arns {
			lb, ok := elbv2LoadBalancers.Get(arn)
			if !ok {
				return nil, &elbv2NotFound{"LoadBalancerNotFound", fmt.Sprintf("Load balancers '[%s]' not found", arn)}
			}
			out = append(out, lb)
		}
		return out, nil
	}
	if names := queryList(r, "Names"); len(names) > 0 {
		byName := make(map[string]ELBv2LoadBalancer)
		for _, lb := range elbv2LoadBalancers.List() {
			byName[lb.Name] = lb
		}
		var out []ELBv2LoadBalancer
		for _, n := range names {
			lb, ok := byName[n]
			if !ok {
				return nil, &elbv2NotFound{"LoadBalancerNotFound", fmt.Sprintf("Load balancers '[%s]' not found", n)}
			}
			out = append(out, lb)
		}
		return out, nil
	}
	return elbv2LoadBalancers.List(), nil
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

// elbv2DefaultEnforceSG: NLBs carry
// enforce_security_group_inbound_rules_on_private_link_traffic (default "on");
// ALBs don't expose it.
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
	return fmt.Sprintf(`<member><LoadBalancerArn>%s</LoadBalancerArn><DNSName>%s</DNSName><CanonicalHostedZoneId>%s</CanonicalHostedZoneId><CreatedTime>%s</CreatedTime><LoadBalancerName>%s</LoadBalancerName><Scheme>%s</Scheme><VpcId>%s</VpcId><State><Code>%s</Code></State><Type>%s</Type><AvailabilityZones>%s</AvailabilityZones><SecurityGroups>%s</SecurityGroups><IpAddressType>%s</IpAddressType>%s</member>`,
		xmlEscape(lb.Arn), xmlEscape(lb.DNSName), xmlEscape(lb.CanonicalZone), xmlEscape(lb.CreatedTime), xmlEscape(lb.Name),
		xmlEscape(lb.Scheme), xmlEscape(lb.VpcID), xmlEscape(lb.State), xmlEscape(lb.Type), elbv2AvailabilityZonesXML(lb.Subnets),
		elbv2StringMembersXMLInner(lb.SecurityGroups), xmlEscape(lb.IpAddressType), extra)
}

// handleELBv2SetIpAddressType changes an existing load balancer's ip_address_type
// in place (aws_lb in-place update; previously unregistered → 404).
func handleELBv2SetIpAddressType(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("LoadBalancerArn")
	ipType := r.FormValue("IpAddressType")
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

func elbv2AvailabilityZonesXML(subnets []string) string {
	var b strings.Builder
	for _, subnet := range subnets {
		az := awsAvailabilityZone()
		if s, ok := ec2Subnets.Get(subnet); ok && s.AvailabilityZone != "" {
			az = s.AvailabilityZone
		}
		fmt.Fprintf(&b, "<member><ZoneName>%s</ZoneName><SubnetId>%s</SubnetId></member>", xmlEscape(az), xmlEscape(subnet))
	}
	return b.String()
}

// elbv2HTTPHealthCheck reports whether the target group's health check is
// HTTP/HTTPS. The HTTP-only health-check attributes — Matcher (response codes)
// and HealthCheckPath — are part of the target group only when this is true;
// for TCP/TLS/UDP/TCP_UDP/GENEVE health checks AWS omits both (the API rejects
// them on those protocols, and Describe* returns neither), so emitting them
// breaks terraform-provider-aws idempotency.
func elbv2HTTPHealthCheck(healthCheckProtocol string) bool {
	switch healthCheckProtocol {
	case "HTTP", "HTTPS":
		return true
	default:
		return false
	}
}

// elbv2DefaultMatcher returns the AWS default health-check success codes for an
// HTTP/HTTPS health check ("200"). It is only meaningful when the health check
// is HTTP/HTTPS (see elbv2HTTPHealthCheck); callers must gate on the protocol.
func elbv2DefaultMatcher() string {
	return "200"
}

// elbv2DefaultedMatcher returns the stored Matcher.HttpCode for a target group:
// the requested value if supplied, otherwise the AWS default for an HTTP/HTTPS
// health check. For TCP/UDP/etc. health checks no Matcher applies, so it stays
// empty unless the caller explicitly supplied one.
func elbv2DefaultedMatcher(healthCheckProtocol, requested string) string {
	if requested != "" {
		return requested
	}
	if elbv2HTTPHealthCheck(healthCheckProtocol) {
		return elbv2DefaultMatcher()
	}
	return ""
}

// elbv2DefaultedHealthCheckPath returns the stored HealthCheckPath for a target
// group: the requested value if supplied, otherwise the AWS default ("/") for an
// HTTP/HTTPS health check. For TCP/UDP/etc. health checks no path applies, so it
// stays empty unless the caller explicitly supplied one — defaulting it to "/"
// there is what leaked a HealthCheckPath onto TCP target groups and broke
// terraform-provider-aws idempotency.
func elbv2DefaultedHealthCheckPath(healthCheckProtocol, requested string) string {
	if requested != "" {
		return requested
	}
	if elbv2HTTPHealthCheck(healthCheckProtocol) {
		return "/"
	}
	return ""
}

// elbv2DefaultProtocolVersion: HTTP/HTTPS target groups default protocol_version
// to HTTP1 (unless requested); TCP/etc. carry none.
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
	// Real ELBv2 returns a Matcher only for HTTP/HTTPS health checks (or a gRPC
	// health check carrying GrpcCode). For TCP/TLS/UDP/TCP_UDP/GENEVE health
	// checks AWS omits Matcher entirely; emitting one makes
	// terraform-provider-aws plan a perpetual "matcher" diff.
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
	// HealthCheckPath, like Matcher, is an HTTP-only attribute: real ELBv2 returns
	// it only for HTTP/HTTPS health checks and omits the element for TCP/TLS/UDP/
	// TCP_UDP/GENEVE. Emitting it on a TCP target group made terraform-provider-aws
	// plan a perpetual "health_check.path" diff.
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

// parseELBv2MutualAuth reads the listener's mutual_authentication block. The
// `off` mode (the ELBv2 default) carries no trust store; only model it when a
// non-empty mode or trust store is actually supplied.
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

// parseELBv2ActionsPrefix parses an action list flattened under prefix
// (`DefaultActions` for listeners, `Actions` for rules), including the typed
// fixed-response / redirect configs so they round-trip back to Terraform and
// the SDK.
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

// elbv2ActionsXML renders an action list under the given wrapper element
// (DefaultActions / Actions), emitting the typed fixed-response / redirect
// configs when present.
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

// parseELBv2ForwardConfig parses the weighted multi-target-group forward block.
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

// parseELBv2ExtraParams reads the query-protocol map (`.entry.N.key/.value`).
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

// elbv2ExtraParamsXML renders the auth extra-params map in deterministic key
// order as `<entry><key/><value/></entry>` members.
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

func defaultELBv2LoadBalancerAttributes() map[string]string {
	return map[string]string{
		"deletion_protection.enabled":                     "false",
		"load_balancing.cross_zone.enabled":               "false",
		"access_logs.s3.enabled":                          "false",
		"idle_timeout.timeout_seconds":                    "60",
		"routing.http2.enabled":                           "true",
		"routing.http.drop_invalid_header_fields.enabled": "false",
		"routing.http.preserve_host_header.enabled":       "false",
	}
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
	if lb.Type == "network" {
		return "net"
	}
	return "app"
}

func elbv2LoadBalancerID(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) == 0 {
		return generateUUID()[:12]
	}
	return parts[len(parts)-1]
}

func elbv2SetResourceTags(arn string, entries map[string]string, remove bool) {
	if elbv2LoadBalancers.Update(arn, func(lb *ELBv2LoadBalancer) {
		if lb.Tags == nil {
			lb.Tags = map[string]string{}
		}
		for k, v := range entries {
			if remove {
				delete(lb.Tags, k)
			} else {
				lb.Tags[k] = v
			}
		}
	}) {
		return
	}
	if elbv2TargetGroups.Update(arn, func(tg *ELBv2TargetGroup) {
		if tg.Tags == nil {
			tg.Tags = map[string]string{}
		}
		for k, v := range entries {
			if remove {
				delete(tg.Tags, k)
			} else {
				tg.Tags[k] = v
			}
		}
	}) {
		return
	}
}

func elbv2ResourceTags(arn string) map[string]string {
	if lb, ok := elbv2LoadBalancers.Get(arn); ok {
		return lb.Tags
	}
	if tg, ok := elbv2TargetGroups.Get(arn); ok {
		return tg.Tags
	}
	return map[string]string{}
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
