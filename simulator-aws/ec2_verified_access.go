package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements faithful control-plane CRUD for the Amazon EC2 Verified
// Access and EC2 Traffic Mirroring families. Every resource is backed by a real
// SQLite-persisted store and rendered as the exact ec2Query XML the AWS SDK for
// Go v2 and the aws CLI deserialize (member casing taken from ec2.smithy.json).
//
// Verified Access: an instance (vai-) is the top-level container; trust
// providers (vatp-) are created standalone and attach/detach to instances;
// groups (vagr-) live under an instance and hold a Cedar policy document;
// endpoints (vae-) live under a group and hold their own policy document.
//
// Traffic Mirroring: targets (tmt-) point at an ENI / NLB / GWLB endpoint;
// filters (tmf-) hold ingress/egress rules (tmfr-) and a set of mirrored
// network services; sessions (tms-) bind a source ENI to a target through a
// filter.

type EC2VerifiedAccessTrustProvider struct {
	VerifiedAccessTrustProviderId string
	Description                   string
	TrustProviderType             string
	UserTrustProviderType         string
	DeviceTrustProviderType       string
	PolicyReferenceName           string
	CreationTime                  string
	LastUpdatedTime               string
	Tags                          []EC2Tag
}

type EC2VerifiedAccessInstance struct {
	VerifiedAccessInstanceId string
	Description              string
	CreationTime             string
	LastUpdatedTime          string
	FipsEnabled              bool
	// TrustProviderIds holds the ids of the attached trust providers, in
	// attach order, so the verifiedAccessTrustProviderSet renders the condensed
	// view real AWS returns.
	TrustProviderIds []string
	Tags             []EC2Tag
}

type EC2VerifiedAccessGroup struct {
	VerifiedAccessGroupId    string
	VerifiedAccessInstanceId string
	Description              string
	Owner                    string
	VerifiedAccessGroupArn   string
	CreationTime             string
	LastUpdatedTime          string
	PolicyDocument           string
	PolicyEnabled            bool
	Tags                     []EC2Tag
}

type EC2VerifiedAccessEndpoint struct {
	VerifiedAccessInstanceId string
	VerifiedAccessGroupId    string
	VerifiedAccessEndpointId string
	ApplicationDomain        string
	EndpointType             string
	AttachmentType           string
	DomainCertificateArn     string
	EndpointDomain           string
	DeviceValidationDomain   string
	SecurityGroupIds         []string
	StatusCode               string
	Description              string
	CreationTime             string
	LastUpdatedTime          string
	PolicyDocument           string
	PolicyEnabled            bool
	Tags                     []EC2Tag
}

// EC2VerifiedAccessLogging is the per-instance access-log configuration set by
// ModifyVerifiedAccessInstanceLoggingConfiguration. Keyed by instance id.
type EC2VerifiedAccessLogging struct {
	VerifiedAccessInstanceId  string
	S3Enabled                 bool
	S3BucketName              string
	S3Prefix                  string
	CloudWatchEnabled         bool
	CloudWatchLogGroup        string
	KinesisEnabled            bool
	KinesisDeliveryStreamName string
	LogVersion                string
	IncludeTrustContext       bool
}

type EC2TrafficMirrorTarget struct {
	TrafficMirrorTargetId         string
	NetworkInterfaceId            string
	NetworkLoadBalancerArn        string
	GatewayLoadBalancerEndpointId string
	Type                          string
	Description                   string
	OwnerId                       string
	Tags                          []EC2Tag
}

type EC2TrafficMirrorPortRange struct {
	FromPort int
	ToPort   int
}

type EC2TrafficMirrorFilterRule struct {
	TrafficMirrorFilterRuleId string
	TrafficMirrorFilterId     string
	TrafficDirection          string
	RuleNumber                int
	RuleAction                string
	Protocol                  int
	HasProtocol               bool
	DestinationPortRange      *EC2TrafficMirrorPortRange
	SourcePortRange           *EC2TrafficMirrorPortRange
	DestinationCidrBlock      string
	SourceCidrBlock           string
	Description               string
}

type EC2TrafficMirrorFilter struct {
	TrafficMirrorFilterId string
	Description           string
	NetworkServices       []string
	Tags                  []EC2Tag
}

type EC2TrafficMirrorSession struct {
	TrafficMirrorSessionId string
	TrafficMirrorTargetId  string
	TrafficMirrorFilterId  string
	NetworkInterfaceId     string
	OwnerId                string
	PacketLength           int
	HasPacketLength        bool
	SessionNumber          int
	VirtualNetworkId       int
	Description            string
	Tags                   []EC2Tag
}

var (
	ec2VaInstances      sim.Store[EC2VerifiedAccessInstance]
	ec2VaTrustProviders sim.Store[EC2VerifiedAccessTrustProvider]
	ec2VaGroups         sim.Store[EC2VerifiedAccessGroup]
	ec2VaEndpoints      sim.Store[EC2VerifiedAccessEndpoint]
	ec2VaLogging        sim.Store[EC2VerifiedAccessLogging]

	ec2TmTargets     sim.Store[EC2TrafficMirrorTarget]
	ec2TmFilters     sim.Store[EC2TrafficMirrorFilter]
	ec2TmFilterRules sim.Store[EC2TrafficMirrorFilterRule]
	ec2TmSessions    sim.Store[EC2TrafficMirrorSession]
)

func registerEC2VerifiedAccess(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2VaInstances = sim.MakeStore[EC2VerifiedAccessInstance](srv.DB(), "ec2_va_instances")
	ec2VaTrustProviders = sim.MakeStore[EC2VerifiedAccessTrustProvider](srv.DB(), "ec2_va_trust_providers")
	ec2VaGroups = sim.MakeStore[EC2VerifiedAccessGroup](srv.DB(), "ec2_va_groups")
	ec2VaEndpoints = sim.MakeStore[EC2VerifiedAccessEndpoint](srv.DB(), "ec2_va_endpoints")
	ec2VaLogging = sim.MakeStore[EC2VerifiedAccessLogging](srv.DB(), "ec2_va_logging")

	ec2TmTargets = sim.MakeStore[EC2TrafficMirrorTarget](srv.DB(), "ec2_tm_targets")
	ec2TmFilters = sim.MakeStore[EC2TrafficMirrorFilter](srv.DB(), "ec2_tm_filters")
	ec2TmFilterRules = sim.MakeStore[EC2TrafficMirrorFilterRule](srv.DB(), "ec2_tm_filter_rules")
	ec2TmSessions = sim.MakeStore[EC2TrafficMirrorSession](srv.DB(), "ec2_tm_sessions")

	// Verified Access instances
	r.Register("CreateVerifiedAccessInstance", handleCreateVerifiedAccessInstance)
	r.Register("DescribeVerifiedAccessInstances", handleDescribeVerifiedAccessInstances)
	r.Register("ModifyVerifiedAccessInstance", handleModifyVerifiedAccessInstance)
	r.Register("DeleteVerifiedAccessInstance", handleDeleteVerifiedAccessInstance)

	// Verified Access trust providers
	r.Register("CreateVerifiedAccessTrustProvider", handleCreateVerifiedAccessTrustProvider)
	r.Register("DescribeVerifiedAccessTrustProviders", handleDescribeVerifiedAccessTrustProviders)
	r.Register("ModifyVerifiedAccessTrustProvider", handleModifyVerifiedAccessTrustProvider)
	r.Register("DeleteVerifiedAccessTrustProvider", handleDeleteVerifiedAccessTrustProvider)
	r.Register("AttachVerifiedAccessTrustProvider", handleAttachVerifiedAccessTrustProvider)
	r.Register("DetachVerifiedAccessTrustProvider", handleDetachVerifiedAccessTrustProvider)

	// Verified Access groups
	r.Register("CreateVerifiedAccessGroup", handleCreateVerifiedAccessGroup)
	r.Register("DescribeVerifiedAccessGroups", handleDescribeVerifiedAccessGroups)
	r.Register("ModifyVerifiedAccessGroup", handleModifyVerifiedAccessGroup)
	r.Register("DeleteVerifiedAccessGroup", handleDeleteVerifiedAccessGroup)
	r.Register("GetVerifiedAccessGroupPolicy", handleGetVerifiedAccessGroupPolicy)
	r.Register("ModifyVerifiedAccessGroupPolicy", handleModifyVerifiedAccessGroupPolicy)

	// Verified Access endpoints
	r.Register("CreateVerifiedAccessEndpoint", handleCreateVerifiedAccessEndpoint)
	r.Register("DescribeVerifiedAccessEndpoints", handleDescribeVerifiedAccessEndpoints)
	r.Register("ModifyVerifiedAccessEndpoint", handleModifyVerifiedAccessEndpoint)
	r.Register("DeleteVerifiedAccessEndpoint", handleDeleteVerifiedAccessEndpoint)
	r.Register("GetVerifiedAccessEndpointPolicy", handleGetVerifiedAccessEndpointPolicy)
	r.Register("ModifyVerifiedAccessEndpointPolicy", handleModifyVerifiedAccessEndpointPolicy)
	r.Register("GetVerifiedAccessEndpointTargets", handleGetVerifiedAccessEndpointTargets)

	// Verified Access logging + client config export
	r.Register("DescribeVerifiedAccessInstanceLoggingConfigurations", handleDescribeVerifiedAccessInstanceLoggingConfigurations)
	r.Register("ModifyVerifiedAccessInstanceLoggingConfiguration", handleModifyVerifiedAccessInstanceLoggingConfiguration)
	r.Register("ExportVerifiedAccessInstanceClientConfiguration", handleExportVerifiedAccessInstanceClientConfiguration)

	// Traffic Mirror targets
	r.Register("CreateTrafficMirrorTarget", handleCreateTrafficMirrorTarget)
	r.Register("DescribeTrafficMirrorTargets", handleDescribeTrafficMirrorTargets)
	r.Register("DeleteTrafficMirrorTarget", handleDeleteTrafficMirrorTarget)

	// Traffic Mirror filters + rules
	r.Register("CreateTrafficMirrorFilter", handleCreateTrafficMirrorFilter)
	r.Register("DescribeTrafficMirrorFilters", handleDescribeTrafficMirrorFilters)
	r.Register("DeleteTrafficMirrorFilter", handleDeleteTrafficMirrorFilter)
	r.Register("CreateTrafficMirrorFilterRule", handleCreateTrafficMirrorFilterRule)
	r.Register("DescribeTrafficMirrorFilterRules", handleDescribeTrafficMirrorFilterRules)
	r.Register("ModifyTrafficMirrorFilterRule", handleModifyTrafficMirrorFilterRule)
	r.Register("DeleteTrafficMirrorFilterRule", handleDeleteTrafficMirrorFilterRule)
	r.Register("ModifyTrafficMirrorFilterNetworkServices", handleModifyTrafficMirrorFilterNetworkServices)

	// Traffic Mirror sessions
	r.Register("CreateTrafficMirrorSession", handleCreateTrafficMirrorSession)
	r.Register("DescribeTrafficMirrorSessions", handleDescribeTrafficMirrorSessions)
	r.Register("ModifyTrafficMirrorSession", handleModifyTrafficMirrorSession)
	r.Register("DeleteTrafficMirrorSession", handleDeleteTrafficMirrorSession)
}

func vaArn(resource string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:%s", awsRegion(), awsAccountID(), resource)
}

// ec2FormInt reads an integer form value; ok is false when the param is absent.
func ec2FormInt(r *http.Request, key string) (int, bool) {
	v := r.FormValue(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func vaInstanceTrustProviderSetXML(inst EC2VerifiedAccessInstance) string {
	var b strings.Builder
	b.WriteString("<verifiedAccessTrustProviderSet>")
	for _, tpID := range inst.TrustProviderIds {
		tp, ok := ec2VaTrustProviders.Get(tpID)
		if !ok {
			continue
		}
		b.WriteString("<item>")
		fmt.Fprintf(&b, "<verifiedAccessTrustProviderId>%s</verifiedAccessTrustProviderId>", tp.VerifiedAccessTrustProviderId)
		if tp.Description != "" {
			fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(tp.Description))
		}
		fmt.Fprintf(&b, "<trustProviderType>%s</trustProviderType>", tp.TrustProviderType)
		if tp.UserTrustProviderType != "" {
			fmt.Fprintf(&b, "<userTrustProviderType>%s</userTrustProviderType>", tp.UserTrustProviderType)
		}
		if tp.DeviceTrustProviderType != "" {
			fmt.Fprintf(&b, "<deviceTrustProviderType>%s</deviceTrustProviderType>", tp.DeviceTrustProviderType)
		}
		b.WriteString("</item>")
	}
	b.WriteString("</verifiedAccessTrustProviderSet>")
	return b.String()
}

func vaInstanceBodyXML(inst EC2VerifiedAccessInstance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<verifiedAccessInstanceId>%s</verifiedAccessInstanceId>", inst.VerifiedAccessInstanceId)
	if inst.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(inst.Description))
	}
	b.WriteString(vaInstanceTrustProviderSetXML(inst))
	fmt.Fprintf(&b, "<creationTime>%s</creationTime>", inst.CreationTime)
	fmt.Fprintf(&b, "<lastUpdatedTime>%s</lastUpdatedTime>", inst.LastUpdatedTime)
	b.WriteString(writeTagSetXML(inst.Tags))
	fmt.Fprintf(&b, "<fipsEnabled>%t</fipsEnabled>", inst.FipsEnabled)
	return b.String()
}

func handleCreateVerifiedAccessInstance(w http.ResponseWriter, r *http.Request) {
	now := ec2NowRFC3339Milli()
	inst := EC2VerifiedAccessInstance{
		VerifiedAccessInstanceId: ec2ID("vai"),
		Description:              r.FormValue("Description"),
		CreationTime:             now,
		LastUpdatedTime:          now,
		FipsEnabled:              r.FormValue("FIPSEnabled") == "true",
		Tags:                     parseTags(r),
	}
	ec2VaInstances.Put(inst.VerifiedAccessInstanceId, inst)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVerifiedAccessInstanceResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessInstance>%s</verifiedAccessInstance>
</CreateVerifiedAccessInstanceResponse>`, ec2Xmlns(), generateUUID(), vaInstanceBodyXML(inst))
}

func handleDescribeVerifiedAccessInstances(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VerifiedAccessInstanceId")
	for _, id := range ids {
		if _, ok := ec2VaInstances.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	var items strings.Builder
	for _, inst := range ec2VaInstances.List() {
		if len(ids) > 0 && !ec2StrInValues(inst.VerifiedAccessInstanceId, ids) {
			continue
		}
		items.WriteString("<item>" + vaInstanceBodyXML(inst) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVerifiedAccessInstancesResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessInstanceSet>%s</verifiedAccessInstanceSet>
</DescribeVerifiedAccessInstancesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyVerifiedAccessInstance(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessInstanceId")
	inst, ok := ec2VaInstances.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("Description"); v != "" {
		inst.Description = v
	}
	inst.LastUpdatedTime = ec2NowRFC3339Milli()
	ec2VaInstances.Put(id, inst)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVerifiedAccessInstanceResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessInstance>%s</verifiedAccessInstance>
</ModifyVerifiedAccessInstanceResponse>`, ec2Xmlns(), generateUUID(), vaInstanceBodyXML(inst))
}

func handleDeleteVerifiedAccessInstance(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessInstanceId")
	inst, ok := ec2VaInstances.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VaInstances.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVerifiedAccessInstanceResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessInstance>%s</verifiedAccessInstance>
</DeleteVerifiedAccessInstanceResponse>`, ec2Xmlns(), generateUUID(), vaInstanceBodyXML(inst))
}

func vaTrustProviderBodyXML(tp EC2VerifiedAccessTrustProvider) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<verifiedAccessTrustProviderId>%s</verifiedAccessTrustProviderId>", tp.VerifiedAccessTrustProviderId)
	if tp.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(tp.Description))
	}
	fmt.Fprintf(&b, "<trustProviderType>%s</trustProviderType>", tp.TrustProviderType)
	if tp.UserTrustProviderType != "" {
		fmt.Fprintf(&b, "<userTrustProviderType>%s</userTrustProviderType>", tp.UserTrustProviderType)
	}
	if tp.DeviceTrustProviderType != "" {
		fmt.Fprintf(&b, "<deviceTrustProviderType>%s</deviceTrustProviderType>", tp.DeviceTrustProviderType)
	}
	if tp.PolicyReferenceName != "" {
		fmt.Fprintf(&b, "<policyReferenceName>%s</policyReferenceName>", xmlEscape(tp.PolicyReferenceName))
	}
	fmt.Fprintf(&b, "<creationTime>%s</creationTime>", tp.CreationTime)
	fmt.Fprintf(&b, "<lastUpdatedTime>%s</lastUpdatedTime>", tp.LastUpdatedTime)
	b.WriteString(writeTagSetXML(tp.Tags))
	return b.String()
}

func handleCreateVerifiedAccessTrustProvider(w http.ResponseWriter, r *http.Request) {
	now := ec2NowRFC3339Milli()
	tp := EC2VerifiedAccessTrustProvider{
		VerifiedAccessTrustProviderId: ec2ID("vatp"),
		Description:                   r.FormValue("Description"),
		TrustProviderType:             r.FormValue("TrustProviderType"),
		UserTrustProviderType:         r.FormValue("UserTrustProviderType"),
		DeviceTrustProviderType:       r.FormValue("DeviceTrustProviderType"),
		PolicyReferenceName:           r.FormValue("PolicyReferenceName"),
		CreationTime:                  now,
		LastUpdatedTime:               now,
		Tags:                          parseTags(r),
	}
	ec2VaTrustProviders.Put(tp.VerifiedAccessTrustProviderId, tp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVerifiedAccessTrustProviderResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessTrustProvider>%s</verifiedAccessTrustProvider>
</CreateVerifiedAccessTrustProviderResponse>`, ec2Xmlns(), generateUUID(), vaTrustProviderBodyXML(tp))
}

func handleDescribeVerifiedAccessTrustProviders(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VerifiedAccessTrustProviderId")
	for _, id := range ids {
		if _, ok := ec2VaTrustProviders.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVerifiedAccessTrustProviderId.NotFound", fmt.Sprintf("The Verified Access trust provider ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	var items strings.Builder
	for _, tp := range ec2VaTrustProviders.List() {
		if len(ids) > 0 && !ec2StrInValues(tp.VerifiedAccessTrustProviderId, ids) {
			continue
		}
		items.WriteString("<item>" + vaTrustProviderBodyXML(tp) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVerifiedAccessTrustProvidersResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessTrustProviderSet>%s</verifiedAccessTrustProviderSet>
</DescribeVerifiedAccessTrustProvidersResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyVerifiedAccessTrustProvider(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessTrustProviderId")
	tp, ok := ec2VaTrustProviders.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessTrustProviderId.NotFound", fmt.Sprintf("The Verified Access trust provider ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("Description"); v != "" {
		tp.Description = v
	}
	tp.LastUpdatedTime = ec2NowRFC3339Milli()
	ec2VaTrustProviders.Put(id, tp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVerifiedAccessTrustProviderResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessTrustProvider>%s</verifiedAccessTrustProvider>
</ModifyVerifiedAccessTrustProviderResponse>`, ec2Xmlns(), generateUUID(), vaTrustProviderBodyXML(tp))
}

func handleDeleteVerifiedAccessTrustProvider(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessTrustProviderId")
	tp, ok := ec2VaTrustProviders.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessTrustProviderId.NotFound", fmt.Sprintf("The Verified Access trust provider ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VaTrustProviders.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVerifiedAccessTrustProviderResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessTrustProvider>%s</verifiedAccessTrustProvider>
</DeleteVerifiedAccessTrustProviderResponse>`, ec2Xmlns(), generateUUID(), vaTrustProviderBodyXML(tp))
}

func handleAttachVerifiedAccessTrustProvider(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("VerifiedAccessInstanceId")
	tpID := r.FormValue("VerifiedAccessTrustProviderId")
	inst, ok := ec2VaInstances.Get(instID)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", instID), http.StatusBadRequest)
		return
	}
	tp, ok := ec2VaTrustProviders.Get(tpID)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessTrustProviderId.NotFound", fmt.Sprintf("The Verified Access trust provider ID '%s' does not exist", tpID), http.StatusBadRequest)
		return
	}
	if !ec2StrInValues(tpID, inst.TrustProviderIds) {
		inst.TrustProviderIds = append(inst.TrustProviderIds, tpID)
	}
	inst.LastUpdatedTime = ec2NowRFC3339Milli()
	tp.LastUpdatedTime = inst.LastUpdatedTime
	ec2VaInstances.Put(instID, inst)
	ec2VaTrustProviders.Put(tpID, tp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AttachVerifiedAccessTrustProviderResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessTrustProvider>%s</verifiedAccessTrustProvider>
  <verifiedAccessInstance>%s</verifiedAccessInstance>
</AttachVerifiedAccessTrustProviderResponse>`, ec2Xmlns(), generateUUID(), vaTrustProviderBodyXML(tp), vaInstanceBodyXML(inst))
}

func handleDetachVerifiedAccessTrustProvider(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("VerifiedAccessInstanceId")
	tpID := r.FormValue("VerifiedAccessTrustProviderId")
	inst, ok := ec2VaInstances.Get(instID)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", instID), http.StatusBadRequest)
		return
	}
	tp, ok := ec2VaTrustProviders.Get(tpID)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessTrustProviderId.NotFound", fmt.Sprintf("The Verified Access trust provider ID '%s' does not exist", tpID), http.StatusBadRequest)
		return
	}
	var kept []string
	for _, id := range inst.TrustProviderIds {
		if id != tpID {
			kept = append(kept, id)
		}
	}
	inst.TrustProviderIds = kept
	inst.LastUpdatedTime = ec2NowRFC3339Milli()
	tp.LastUpdatedTime = inst.LastUpdatedTime
	ec2VaInstances.Put(instID, inst)
	ec2VaTrustProviders.Put(tpID, tp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DetachVerifiedAccessTrustProviderResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessTrustProvider>%s</verifiedAccessTrustProvider>
  <verifiedAccessInstance>%s</verifiedAccessInstance>
</DetachVerifiedAccessTrustProviderResponse>`, ec2Xmlns(), generateUUID(), vaTrustProviderBodyXML(tp), vaInstanceBodyXML(inst))
}

func vaGroupBodyXML(g EC2VerifiedAccessGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<verifiedAccessGroupId>%s</verifiedAccessGroupId>", g.VerifiedAccessGroupId)
	fmt.Fprintf(&b, "<verifiedAccessInstanceId>%s</verifiedAccessInstanceId>", g.VerifiedAccessInstanceId)
	if g.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(g.Description))
	}
	fmt.Fprintf(&b, "<owner>%s</owner>", g.Owner)
	fmt.Fprintf(&b, "<verifiedAccessGroupArn>%s</verifiedAccessGroupArn>", g.VerifiedAccessGroupArn)
	fmt.Fprintf(&b, "<creationTime>%s</creationTime>", g.CreationTime)
	fmt.Fprintf(&b, "<lastUpdatedTime>%s</lastUpdatedTime>", g.LastUpdatedTime)
	b.WriteString(writeTagSetXML(g.Tags))
	return b.String()
}

func handleCreateVerifiedAccessGroup(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("VerifiedAccessInstanceId")
	if _, ok := ec2VaInstances.Get(instID); !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", instID), http.StatusBadRequest)
		return
	}
	now := ec2NowRFC3339Milli()
	id := ec2ID("vagr")
	policy := r.FormValue("PolicyDocument")
	g := EC2VerifiedAccessGroup{
		VerifiedAccessGroupId:    id,
		VerifiedAccessInstanceId: instID,
		Description:              r.FormValue("Description"),
		Owner:                    awsAccountID(),
		VerifiedAccessGroupArn:   vaArn("verified-access-group/" + id),
		CreationTime:             now,
		LastUpdatedTime:          now,
		PolicyDocument:           policy,
		PolicyEnabled:            policy != "",
		Tags:                     parseTags(r),
	}
	ec2VaGroups.Put(id, g)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVerifiedAccessGroupResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessGroup>%s</verifiedAccessGroup>
</CreateVerifiedAccessGroupResponse>`, ec2Xmlns(), generateUUID(), vaGroupBodyXML(g))
}

func handleDescribeVerifiedAccessGroups(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VerifiedAccessGroupId")
	for _, id := range ids {
		if _, ok := ec2VaGroups.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVerifiedAccessGroupId.NotFound", fmt.Sprintf("The Verified Access group ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	instFilter := r.FormValue("VerifiedAccessInstanceId")
	var items strings.Builder
	for _, g := range ec2VaGroups.List() {
		if len(ids) > 0 && !ec2StrInValues(g.VerifiedAccessGroupId, ids) {
			continue
		}
		if instFilter != "" && g.VerifiedAccessInstanceId != instFilter {
			continue
		}
		items.WriteString("<item>" + vaGroupBodyXML(g) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVerifiedAccessGroupsResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessGroupSet>%s</verifiedAccessGroupSet>
</DescribeVerifiedAccessGroupsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyVerifiedAccessGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessGroupId")
	g, ok := ec2VaGroups.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessGroupId.NotFound", fmt.Sprintf("The Verified Access group ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("Description"); v != "" {
		g.Description = v
	}
	if v := r.FormValue("VerifiedAccessInstanceId"); v != "" {
		g.VerifiedAccessInstanceId = v
	}
	g.LastUpdatedTime = ec2NowRFC3339Milli()
	ec2VaGroups.Put(id, g)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVerifiedAccessGroupResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessGroup>%s</verifiedAccessGroup>
</ModifyVerifiedAccessGroupResponse>`, ec2Xmlns(), generateUUID(), vaGroupBodyXML(g))
}

func handleDeleteVerifiedAccessGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessGroupId")
	g, ok := ec2VaGroups.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessGroupId.NotFound", fmt.Sprintf("The Verified Access group ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VaGroups.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVerifiedAccessGroupResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessGroup>%s</verifiedAccessGroup>
</DeleteVerifiedAccessGroupResponse>`, ec2Xmlns(), generateUUID(), vaGroupBodyXML(g))
}

func handleGetVerifiedAccessGroupPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessGroupId")
	g, ok := ec2VaGroups.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessGroupId.NotFound", fmt.Sprintf("The Verified Access group ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, "<policyEnabled>%t</policyEnabled>", g.PolicyEnabled)
	if g.PolicyDocument != "" {
		fmt.Fprintf(&b, "<policyDocument>%s</policyDocument>", xmlEscape(g.PolicyDocument))
	}
	fmt.Fprintf(w, `<GetVerifiedAccessGroupPolicyResponse %s>
  <requestId>%s</requestId>
  %s
</GetVerifiedAccessGroupPolicyResponse>`, ec2Xmlns(), generateUUID(), b.String())
}

func handleModifyVerifiedAccessGroupPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessGroupId")
	g, ok := ec2VaGroups.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessGroupId.NotFound", fmt.Sprintf("The Verified Access group ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("PolicyDocument"); v != "" {
		g.PolicyDocument = v
	}
	if v := r.FormValue("PolicyEnabled"); v != "" {
		g.PolicyEnabled = v == "true"
	}
	g.LastUpdatedTime = ec2NowRFC3339Milli()
	ec2VaGroups.Put(id, g)
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, "<policyEnabled>%t</policyEnabled>", g.PolicyEnabled)
	if g.PolicyDocument != "" {
		fmt.Fprintf(&b, "<policyDocument>%s</policyDocument>", xmlEscape(g.PolicyDocument))
	}
	fmt.Fprintf(w, `<ModifyVerifiedAccessGroupPolicyResponse %s>
  <requestId>%s</requestId>
  %s
</ModifyVerifiedAccessGroupPolicyResponse>`, ec2Xmlns(), generateUUID(), b.String())
}

func vaEndpointBodyXML(e EC2VerifiedAccessEndpoint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<verifiedAccessInstanceId>%s</verifiedAccessInstanceId>", e.VerifiedAccessInstanceId)
	fmt.Fprintf(&b, "<verifiedAccessGroupId>%s</verifiedAccessGroupId>", e.VerifiedAccessGroupId)
	fmt.Fprintf(&b, "<verifiedAccessEndpointId>%s</verifiedAccessEndpointId>", e.VerifiedAccessEndpointId)
	if e.ApplicationDomain != "" {
		fmt.Fprintf(&b, "<applicationDomain>%s</applicationDomain>", xmlEscape(e.ApplicationDomain))
	}
	if e.EndpointType != "" {
		fmt.Fprintf(&b, "<endpointType>%s</endpointType>", e.EndpointType)
	}
	if e.AttachmentType != "" {
		fmt.Fprintf(&b, "<attachmentType>%s</attachmentType>", e.AttachmentType)
	}
	if e.DomainCertificateArn != "" {
		fmt.Fprintf(&b, "<domainCertificateArn>%s</domainCertificateArn>", xmlEscape(e.DomainCertificateArn))
	}
	if e.EndpointDomain != "" {
		fmt.Fprintf(&b, "<endpointDomain>%s</endpointDomain>", xmlEscape(e.EndpointDomain))
	}
	if e.DeviceValidationDomain != "" {
		fmt.Fprintf(&b, "<deviceValidationDomain>%s</deviceValidationDomain>", xmlEscape(e.DeviceValidationDomain))
	}
	if len(e.SecurityGroupIds) > 0 {
		b.WriteString("<securityGroupIdSet>")
		for _, sg := range e.SecurityGroupIds {
			fmt.Fprintf(&b, "<item>%s</item>", sg)
		}
		b.WriteString("</securityGroupIdSet>")
	}
	if e.StatusCode != "" {
		fmt.Fprintf(&b, "<status><code>%s</code></status>", e.StatusCode)
	}
	if e.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(e.Description))
	}
	fmt.Fprintf(&b, "<creationTime>%s</creationTime>", e.CreationTime)
	fmt.Fprintf(&b, "<lastUpdatedTime>%s</lastUpdatedTime>", e.LastUpdatedTime)
	b.WriteString(writeTagSetXML(e.Tags))
	return b.String()
}

func handleCreateVerifiedAccessEndpoint(w http.ResponseWriter, r *http.Request) {
	groupID := r.FormValue("VerifiedAccessGroupId")
	g, ok := ec2VaGroups.Get(groupID)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessGroupId.NotFound", fmt.Sprintf("The Verified Access group ID '%s' does not exist", groupID), http.StatusBadRequest)
		return
	}
	now := ec2NowRFC3339Milli()
	id := ec2ID("vae")
	appDomain := r.FormValue("ApplicationDomain")
	endpointDomainPrefix := r.FormValue("EndpointDomainPrefix")
	endpointDomain := ""
	if appDomain != "" {
		if endpointDomainPrefix != "" {
			endpointDomain = endpointDomainPrefix + "." + appDomain
		} else {
			endpointDomain = appDomain
		}
	}
	policy := r.FormValue("PolicyDocument")
	e := EC2VerifiedAccessEndpoint{
		VerifiedAccessInstanceId: g.VerifiedAccessInstanceId,
		VerifiedAccessGroupId:    groupID,
		VerifiedAccessEndpointId: id,
		ApplicationDomain:        appDomain,
		EndpointType:             r.FormValue("EndpointType"),
		AttachmentType:           r.FormValue("AttachmentType"),
		DomainCertificateArn:     r.FormValue("DomainCertificateArn"),
		EndpointDomain:           endpointDomain,
		SecurityGroupIds:         ec2ParamList(r, "SecurityGroupId"),
		StatusCode:               "active",
		Description:              r.FormValue("Description"),
		CreationTime:             now,
		LastUpdatedTime:          now,
		PolicyDocument:           policy,
		PolicyEnabled:            policy != "",
		Tags:                     parseTags(r),
	}
	ec2VaEndpoints.Put(id, e)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVerifiedAccessEndpointResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessEndpoint>%s</verifiedAccessEndpoint>
</CreateVerifiedAccessEndpointResponse>`, ec2Xmlns(), generateUUID(), vaEndpointBodyXML(e))
}

func handleDescribeVerifiedAccessEndpoints(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VerifiedAccessEndpointId")
	for _, id := range ids {
		if _, ok := ec2VaEndpoints.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVerifiedAccessEndpointId.NotFound", fmt.Sprintf("The Verified Access endpoint ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	instFilter := r.FormValue("VerifiedAccessInstanceId")
	groupFilter := r.FormValue("VerifiedAccessGroupId")
	var items strings.Builder
	for _, e := range ec2VaEndpoints.List() {
		if len(ids) > 0 && !ec2StrInValues(e.VerifiedAccessEndpointId, ids) {
			continue
		}
		if instFilter != "" && e.VerifiedAccessInstanceId != instFilter {
			continue
		}
		if groupFilter != "" && e.VerifiedAccessGroupId != groupFilter {
			continue
		}
		items.WriteString("<item>" + vaEndpointBodyXML(e) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVerifiedAccessEndpointsResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessEndpointSet>%s</verifiedAccessEndpointSet>
</DescribeVerifiedAccessEndpointsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyVerifiedAccessEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessEndpointId")
	e, ok := ec2VaEndpoints.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessEndpointId.NotFound", fmt.Sprintf("The Verified Access endpoint ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("Description"); v != "" {
		e.Description = v
	}
	if v := r.FormValue("VerifiedAccessGroupId"); v != "" {
		e.VerifiedAccessGroupId = v
	}
	e.LastUpdatedTime = ec2NowRFC3339Milli()
	ec2VaEndpoints.Put(id, e)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVerifiedAccessEndpointResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessEndpoint>%s</verifiedAccessEndpoint>
</ModifyVerifiedAccessEndpointResponse>`, ec2Xmlns(), generateUUID(), vaEndpointBodyXML(e))
}

func handleDeleteVerifiedAccessEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessEndpointId")
	e, ok := ec2VaEndpoints.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessEndpointId.NotFound", fmt.Sprintf("The Verified Access endpoint ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	e.StatusCode = "deleting"
	ec2VaEndpoints.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVerifiedAccessEndpointResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessEndpoint>%s</verifiedAccessEndpoint>
</DeleteVerifiedAccessEndpointResponse>`, ec2Xmlns(), generateUUID(), vaEndpointBodyXML(e))
}

func handleGetVerifiedAccessEndpointPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessEndpointId")
	e, ok := ec2VaEndpoints.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessEndpointId.NotFound", fmt.Sprintf("The Verified Access endpoint ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, "<policyEnabled>%t</policyEnabled>", e.PolicyEnabled)
	if e.PolicyDocument != "" {
		fmt.Fprintf(&b, "<policyDocument>%s</policyDocument>", xmlEscape(e.PolicyDocument))
	}
	fmt.Fprintf(w, `<GetVerifiedAccessEndpointPolicyResponse %s>
  <requestId>%s</requestId>
  %s
</GetVerifiedAccessEndpointPolicyResponse>`, ec2Xmlns(), generateUUID(), b.String())
}

func handleModifyVerifiedAccessEndpointPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessEndpointId")
	e, ok := ec2VaEndpoints.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessEndpointId.NotFound", fmt.Sprintf("The Verified Access endpoint ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("PolicyDocument"); v != "" {
		e.PolicyDocument = v
	}
	if v := r.FormValue("PolicyEnabled"); v != "" {
		e.PolicyEnabled = v == "true"
	}
	e.LastUpdatedTime = ec2NowRFC3339Milli()
	ec2VaEndpoints.Put(id, e)
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, "<policyEnabled>%t</policyEnabled>", e.PolicyEnabled)
	if e.PolicyDocument != "" {
		fmt.Fprintf(&b, "<policyDocument>%s</policyDocument>", xmlEscape(e.PolicyDocument))
	}
	fmt.Fprintf(w, `<ModifyVerifiedAccessEndpointPolicyResponse %s>
  <requestId>%s</requestId>
  %s
</ModifyVerifiedAccessEndpointPolicyResponse>`, ec2Xmlns(), generateUUID(), b.String())
}

func handleGetVerifiedAccessEndpointTargets(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VerifiedAccessEndpointId")
	e, ok := ec2VaEndpoints.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessEndpointId.NotFound", fmt.Sprintf("The Verified Access endpoint ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	var items strings.Builder
	items.WriteString("<item>")
	fmt.Fprintf(&items, "<verifiedAccessEndpointId>%s</verifiedAccessEndpointId>", e.VerifiedAccessEndpointId)
	if e.EndpointDomain != "" {
		fmt.Fprintf(&items, "<verifiedAccessEndpointTargetDns>%s</verifiedAccessEndpointTargetDns>", xmlEscape(e.EndpointDomain))
	}
	items.WriteString("</item>")
	fmt.Fprintf(w, `<GetVerifiedAccessEndpointTargetsResponse %s>
  <requestId>%s</requestId>
  <verifiedAccessEndpointTargetSet>%s</verifiedAccessEndpointTargetSet>
</GetVerifiedAccessEndpointTargetsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func vaLoggingBodyXML(lc EC2VerifiedAccessLogging) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<verifiedAccessInstanceId>%s</verifiedAccessInstanceId>", lc.VerifiedAccessInstanceId)
	b.WriteString("<accessLogs>")
	b.WriteString("<s3><enabled>" + strconv.FormatBool(lc.S3Enabled) + "</enabled>")
	if lc.S3BucketName != "" {
		fmt.Fprintf(&b, "<bucketName>%s</bucketName>", xmlEscape(lc.S3BucketName))
	}
	if lc.S3Prefix != "" {
		fmt.Fprintf(&b, "<prefix>%s</prefix>", xmlEscape(lc.S3Prefix))
	}
	b.WriteString("</s3>")
	b.WriteString("<cloudWatchLogs><enabled>" + strconv.FormatBool(lc.CloudWatchEnabled) + "</enabled>")
	if lc.CloudWatchLogGroup != "" {
		fmt.Fprintf(&b, "<logGroup>%s</logGroup>", xmlEscape(lc.CloudWatchLogGroup))
	}
	b.WriteString("</cloudWatchLogs>")
	b.WriteString("<kinesisDataFirehose><enabled>" + strconv.FormatBool(lc.KinesisEnabled) + "</enabled>")
	if lc.KinesisDeliveryStreamName != "" {
		fmt.Fprintf(&b, "<deliveryStream>%s</deliveryStream>", xmlEscape(lc.KinesisDeliveryStreamName))
	}
	b.WriteString("</kinesisDataFirehose>")
	if lc.LogVersion != "" {
		fmt.Fprintf(&b, "<logVersion>%s</logVersion>", xmlEscape(lc.LogVersion))
	}
	fmt.Fprintf(&b, "<includeTrustContext>%t</includeTrustContext>", lc.IncludeTrustContext)
	b.WriteString("</accessLogs>")
	return b.String()
}

func vaLoggingFor(instID string) EC2VerifiedAccessLogging {
	if lc, ok := ec2VaLogging.Get(instID); ok {
		return lc
	}
	// Default: all destinations disabled, like a freshly created instance.
	return EC2VerifiedAccessLogging{VerifiedAccessInstanceId: instID}
}

func handleDescribeVerifiedAccessInstanceLoggingConfigurations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VerifiedAccessInstanceId")
	for _, id := range ids {
		if _, ok := ec2VaInstances.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	var items strings.Builder
	for _, inst := range ec2VaInstances.List() {
		if len(ids) > 0 && !ec2StrInValues(inst.VerifiedAccessInstanceId, ids) {
			continue
		}
		items.WriteString("<item>" + vaLoggingBodyXML(vaLoggingFor(inst.VerifiedAccessInstanceId)) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVerifiedAccessInstanceLoggingConfigurationsResponse %s>
  <requestId>%s</requestId>
  <loggingConfigurationSet>%s</loggingConfigurationSet>
</DescribeVerifiedAccessInstanceLoggingConfigurationsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyVerifiedAccessInstanceLoggingConfiguration(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("VerifiedAccessInstanceId")
	if _, ok := ec2VaInstances.Get(instID); !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", instID), http.StatusBadRequest)
		return
	}
	lc := vaLoggingFor(instID)
	if v := r.FormValue("AccessLogs.S3.Enabled"); v != "" {
		lc.S3Enabled = v == "true"
	}
	if v := r.FormValue("AccessLogs.S3.BucketName"); v != "" {
		lc.S3BucketName = v
	}
	if v := r.FormValue("AccessLogs.S3.Prefix"); v != "" {
		lc.S3Prefix = v
	}
	if v := r.FormValue("AccessLogs.CloudWatchLogs.Enabled"); v != "" {
		lc.CloudWatchEnabled = v == "true"
	}
	if v := r.FormValue("AccessLogs.CloudWatchLogs.LogGroup"); v != "" {
		lc.CloudWatchLogGroup = v
	}
	if v := r.FormValue("AccessLogs.KinesisDataFirehose.Enabled"); v != "" {
		lc.KinesisEnabled = v == "true"
	}
	if v := r.FormValue("AccessLogs.KinesisDataFirehose.DeliveryStream"); v != "" {
		lc.KinesisDeliveryStreamName = v
	}
	if v := r.FormValue("AccessLogs.LogVersion"); v != "" {
		lc.LogVersion = v
	}
	if v := r.FormValue("AccessLogs.IncludeTrustContext"); v != "" {
		lc.IncludeTrustContext = v == "true"
	}
	ec2VaLogging.Put(instID, lc)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVerifiedAccessInstanceLoggingConfigurationResponse %s>
  <requestId>%s</requestId>
  <loggingConfiguration>%s</loggingConfiguration>
</ModifyVerifiedAccessInstanceLoggingConfigurationResponse>`, ec2Xmlns(), generateUUID(), vaLoggingBodyXML(lc))
}

func handleExportVerifiedAccessInstanceClientConfiguration(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("VerifiedAccessInstanceId")
	if _, ok := ec2VaInstances.Get(instID); !ok {
		ec2ErrorXML(w, "InvalidVerifiedAccessInstanceId.NotFound", fmt.Sprintf("The Verified Access instance ID '%s' does not exist", instID), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ExportVerifiedAccessInstanceClientConfigurationResponse %s>
  <requestId>%s</requestId>
  <version>1.0</version>
  <verifiedAccessInstanceId>%s</verifiedAccessInstanceId>
  <region>%s</region>
</ExportVerifiedAccessInstanceClientConfigurationResponse>`, ec2Xmlns(), generateUUID(), instID, awsRegion())
}

func tmTargetBodyXML(t EC2TrafficMirrorTarget) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<trafficMirrorTargetId>%s</trafficMirrorTargetId>", t.TrafficMirrorTargetId)
	if t.NetworkInterfaceId != "" {
		fmt.Fprintf(&b, "<networkInterfaceId>%s</networkInterfaceId>", t.NetworkInterfaceId)
	}
	if t.NetworkLoadBalancerArn != "" {
		fmt.Fprintf(&b, "<networkLoadBalancerArn>%s</networkLoadBalancerArn>", xmlEscape(t.NetworkLoadBalancerArn))
	}
	if t.GatewayLoadBalancerEndpointId != "" {
		fmt.Fprintf(&b, "<gatewayLoadBalancerEndpointId>%s</gatewayLoadBalancerEndpointId>", t.GatewayLoadBalancerEndpointId)
	}
	fmt.Fprintf(&b, "<type>%s</type>", t.Type)
	if t.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(t.Description))
	}
	fmt.Fprintf(&b, "<ownerId>%s</ownerId>", t.OwnerId)
	b.WriteString(writeTagSetXML(t.Tags))
	return b.String()
}

func handleCreateTrafficMirrorTarget(w http.ResponseWriter, r *http.Request) {
	eni := r.FormValue("NetworkInterfaceId")
	nlb := r.FormValue("NetworkLoadBalancerArn")
	gwlb := r.FormValue("GatewayLoadBalancerEndpointId")
	typ := "network-interface"
	switch {
	case nlb != "":
		typ = "network-load-balancer"
	case gwlb != "":
		typ = "gateway-load-balancer-endpoint"
	}
	t := EC2TrafficMirrorTarget{
		TrafficMirrorTargetId:         ec2ID("tmt"),
		NetworkInterfaceId:            eni,
		NetworkLoadBalancerArn:        nlb,
		GatewayLoadBalancerEndpointId: gwlb,
		Type:                          typ,
		Description:                   r.FormValue("Description"),
		OwnerId:                       ec2Owner(),
		Tags:                          parseTags(r),
	}
	ec2TmTargets.Put(t.TrafficMirrorTargetId, t)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateTrafficMirrorTargetResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorTarget>%s</trafficMirrorTarget>
</CreateTrafficMirrorTargetResponse>`, ec2Xmlns(), generateUUID(), tmTargetBodyXML(t))
}

func handleDescribeTrafficMirrorTargets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TrafficMirrorTargetId")
	for _, id := range ids {
		if _, ok := ec2TmTargets.Get(id); !ok {
			ec2ErrorXML(w, "InvalidTrafficMirrorTargetId.NotFound", fmt.Sprintf("The Traffic Mirror target ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	var items strings.Builder
	for _, t := range ec2TmTargets.List() {
		if len(ids) > 0 && !ec2StrInValues(t.TrafficMirrorTargetId, ids) {
			continue
		}
		items.WriteString("<item>" + tmTargetBodyXML(t) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeTrafficMirrorTargetsResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorTargetSet>%s</trafficMirrorTargetSet>
</DescribeTrafficMirrorTargetsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteTrafficMirrorTarget(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TrafficMirrorTargetId")
	if _, ok := ec2TmTargets.Get(id); !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorTargetId.NotFound", fmt.Sprintf("The Traffic Mirror target ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2TmTargets.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteTrafficMirrorTargetResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorTargetId>%s</trafficMirrorTargetId>
</DeleteTrafficMirrorTargetResponse>`, ec2Xmlns(), generateUUID(), id)
}

func tmPortRangeXML(tag string, pr *EC2TrafficMirrorPortRange) string {
	if pr == nil {
		return ""
	}
	return fmt.Sprintf("<%s><fromPort>%d</fromPort><toPort>%d</toPort></%s>", tag, pr.FromPort, pr.ToPort, tag)
}

func tmFilterRuleBodyXML(rule EC2TrafficMirrorFilterRule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<trafficMirrorFilterRuleId>%s</trafficMirrorFilterRuleId>", rule.TrafficMirrorFilterRuleId)
	fmt.Fprintf(&b, "<trafficMirrorFilterId>%s</trafficMirrorFilterId>", rule.TrafficMirrorFilterId)
	fmt.Fprintf(&b, "<trafficDirection>%s</trafficDirection>", rule.TrafficDirection)
	fmt.Fprintf(&b, "<ruleNumber>%d</ruleNumber>", rule.RuleNumber)
	fmt.Fprintf(&b, "<ruleAction>%s</ruleAction>", rule.RuleAction)
	if rule.HasProtocol {
		fmt.Fprintf(&b, "<protocol>%d</protocol>", rule.Protocol)
	}
	b.WriteString(tmPortRangeXML("destinationPortRange", rule.DestinationPortRange))
	b.WriteString(tmPortRangeXML("sourcePortRange", rule.SourcePortRange))
	if rule.DestinationCidrBlock != "" {
		fmt.Fprintf(&b, "<destinationCidrBlock>%s</destinationCidrBlock>", rule.DestinationCidrBlock)
	}
	if rule.SourceCidrBlock != "" {
		fmt.Fprintf(&b, "<sourceCidrBlock>%s</sourceCidrBlock>", rule.SourceCidrBlock)
	}
	if rule.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(rule.Description))
	}
	return b.String()
}

func tmFilterRulesFor(filterID, direction string) []EC2TrafficMirrorFilterRule {
	var out []EC2TrafficMirrorFilterRule
	for _, rule := range ec2TmFilterRules.List() {
		if rule.TrafficMirrorFilterId == filterID && rule.TrafficDirection == direction {
			out = append(out, rule)
		}
	}
	return out
}

func tmFilterBodyXML(f EC2TrafficMirrorFilter) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<trafficMirrorFilterId>%s</trafficMirrorFilterId>", f.TrafficMirrorFilterId)
	b.WriteString("<ingressFilterRuleSet>")
	for _, rule := range tmFilterRulesFor(f.TrafficMirrorFilterId, "ingress") {
		b.WriteString("<item>" + tmFilterRuleBodyXML(rule) + "</item>")
	}
	b.WriteString("</ingressFilterRuleSet>")
	b.WriteString("<egressFilterRuleSet>")
	for _, rule := range tmFilterRulesFor(f.TrafficMirrorFilterId, "egress") {
		b.WriteString("<item>" + tmFilterRuleBodyXML(rule) + "</item>")
	}
	b.WriteString("</egressFilterRuleSet>")
	b.WriteString("<networkServiceSet>")
	for _, ns := range f.NetworkServices {
		fmt.Fprintf(&b, "<item>%s</item>", ns)
	}
	b.WriteString("</networkServiceSet>")
	if f.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(f.Description))
	}
	b.WriteString(writeTagSetXML(f.Tags))
	return b.String()
}

func handleCreateTrafficMirrorFilter(w http.ResponseWriter, r *http.Request) {
	f := EC2TrafficMirrorFilter{
		TrafficMirrorFilterId: ec2ID("tmf"),
		Description:           r.FormValue("Description"),
		Tags:                  parseTags(r),
	}
	ec2TmFilters.Put(f.TrafficMirrorFilterId, f)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateTrafficMirrorFilterResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorFilter>%s</trafficMirrorFilter>
</CreateTrafficMirrorFilterResponse>`, ec2Xmlns(), generateUUID(), tmFilterBodyXML(f))
}

func handleDescribeTrafficMirrorFilters(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TrafficMirrorFilterId")
	for _, id := range ids {
		if _, ok := ec2TmFilters.Get(id); !ok {
			ec2ErrorXML(w, "InvalidTrafficMirrorFilterId.NotFound", fmt.Sprintf("The Traffic Mirror filter ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	var items strings.Builder
	for _, f := range ec2TmFilters.List() {
		if len(ids) > 0 && !ec2StrInValues(f.TrafficMirrorFilterId, ids) {
			continue
		}
		items.WriteString("<item>" + tmFilterBodyXML(f) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeTrafficMirrorFiltersResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorFilterSet>%s</trafficMirrorFilterSet>
</DescribeTrafficMirrorFiltersResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteTrafficMirrorFilter(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TrafficMirrorFilterId")
	if _, ok := ec2TmFilters.Get(id); !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorFilterId.NotFound", fmt.Sprintf("The Traffic Mirror filter ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// Deleting a filter removes its rules too, as real AWS does.
	for _, rule := range ec2TmFilterRules.List() {
		if rule.TrafficMirrorFilterId == id {
			ec2TmFilterRules.Delete(rule.TrafficMirrorFilterRuleId)
		}
	}
	ec2TmFilters.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteTrafficMirrorFilterResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorFilterId>%s</trafficMirrorFilterId>
</DeleteTrafficMirrorFilterResponse>`, ec2Xmlns(), generateUUID(), id)
}

// ec2ParseTmPortRange reads a TrafficMirrorPortRange request param block.
func ec2ParseTmPortRange(r *http.Request, prefix string) *EC2TrafficMirrorPortRange {
	from, hasFrom := ec2FormInt(r, prefix+".FromPort")
	to, hasTo := ec2FormInt(r, prefix+".ToPort")
	if !hasFrom && !hasTo {
		return nil
	}
	return &EC2TrafficMirrorPortRange{FromPort: from, ToPort: to}
}

func handleCreateTrafficMirrorFilterRule(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("TrafficMirrorFilterId")
	if _, ok := ec2TmFilters.Get(filterID); !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorFilterId.NotFound", fmt.Sprintf("The Traffic Mirror filter ID '%s' does not exist", filterID), http.StatusBadRequest)
		return
	}
	ruleNumber, _ := ec2FormInt(r, "RuleNumber")
	protocol, hasProtocol := ec2FormInt(r, "Protocol")
	rule := EC2TrafficMirrorFilterRule{
		TrafficMirrorFilterRuleId: ec2ID("tmfr"),
		TrafficMirrorFilterId:     filterID,
		TrafficDirection:          r.FormValue("TrafficDirection"),
		RuleNumber:                ruleNumber,
		RuleAction:                r.FormValue("RuleAction"),
		Protocol:                  protocol,
		HasProtocol:               hasProtocol,
		DestinationPortRange:      ec2ParseTmPortRange(r, "DestinationPortRange"),
		SourcePortRange:           ec2ParseTmPortRange(r, "SourcePortRange"),
		DestinationCidrBlock:      r.FormValue("DestinationCidrBlock"),
		SourceCidrBlock:           r.FormValue("SourceCidrBlock"),
		Description:               r.FormValue("Description"),
	}
	ec2TmFilterRules.Put(rule.TrafficMirrorFilterRuleId, rule)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateTrafficMirrorFilterRuleResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorFilterRule>%s</trafficMirrorFilterRule>
</CreateTrafficMirrorFilterRuleResponse>`, ec2Xmlns(), generateUUID(), tmFilterRuleBodyXML(rule))
}

func handleDescribeTrafficMirrorFilterRules(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("TrafficMirrorFilterId")
	ids := ec2ParamList(r, "TrafficMirrorFilterRuleId")
	var items strings.Builder
	for _, rule := range ec2TmFilterRules.List() {
		if filterID != "" && rule.TrafficMirrorFilterId != filterID {
			continue
		}
		if len(ids) > 0 && !ec2StrInValues(rule.TrafficMirrorFilterRuleId, ids) {
			continue
		}
		items.WriteString("<item>" + tmFilterRuleBodyXML(rule) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeTrafficMirrorFilterRulesResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorFilterRuleSet>%s</trafficMirrorFilterRuleSet>
</DescribeTrafficMirrorFilterRulesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyTrafficMirrorFilterRule(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TrafficMirrorFilterRuleId")
	rule, ok := ec2TmFilterRules.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorFilterRuleId.NotFound", fmt.Sprintf("The Traffic Mirror filter rule ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v, ok := ec2FormInt(r, "RuleNumber"); ok {
		rule.RuleNumber = v
	}
	if v := r.FormValue("RuleAction"); v != "" {
		rule.RuleAction = v
	}
	if v := r.FormValue("TrafficDirection"); v != "" {
		rule.TrafficDirection = v
	}
	if v, ok := ec2FormInt(r, "Protocol"); ok {
		rule.Protocol = v
		rule.HasProtocol = true
	}
	if v := r.FormValue("DestinationCidrBlock"); v != "" {
		rule.DestinationCidrBlock = v
	}
	if v := r.FormValue("SourceCidrBlock"); v != "" {
		rule.SourceCidrBlock = v
	}
	if v := r.FormValue("Description"); v != "" {
		rule.Description = v
	}
	if pr := ec2ParseTmPortRange(r, "DestinationPortRange"); pr != nil {
		rule.DestinationPortRange = pr
	}
	if pr := ec2ParseTmPortRange(r, "SourcePortRange"); pr != nil {
		rule.SourcePortRange = pr
	}
	ec2TmFilterRules.Put(id, rule)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyTrafficMirrorFilterRuleResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorFilterRule>%s</trafficMirrorFilterRule>
</ModifyTrafficMirrorFilterRuleResponse>`, ec2Xmlns(), generateUUID(), tmFilterRuleBodyXML(rule))
}

func handleDeleteTrafficMirrorFilterRule(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TrafficMirrorFilterRuleId")
	if _, ok := ec2TmFilterRules.Get(id); !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorFilterRuleId.NotFound", fmt.Sprintf("The Traffic Mirror filter rule ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2TmFilterRules.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteTrafficMirrorFilterRuleResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorFilterRuleId>%s</trafficMirrorFilterRuleId>
</DeleteTrafficMirrorFilterRuleResponse>`, ec2Xmlns(), generateUUID(), id)
}

func handleModifyTrafficMirrorFilterNetworkServices(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TrafficMirrorFilterId")
	f, ok := ec2TmFilters.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorFilterId.NotFound", fmt.Sprintf("The Traffic Mirror filter ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	add := ec2ParamList(r, "AddNetworkService")
	remove := ec2ParamList(r, "RemoveNetworkService")
	for _, ns := range add {
		if !ec2StrInValues(ns, f.NetworkServices) {
			f.NetworkServices = append(f.NetworkServices, ns)
		}
	}
	if len(remove) > 0 {
		var kept []string
		for _, ns := range f.NetworkServices {
			if !ec2StrInValues(ns, remove) {
				kept = append(kept, ns)
			}
		}
		f.NetworkServices = kept
	}
	ec2TmFilters.Put(id, f)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyTrafficMirrorFilterNetworkServicesResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorFilter>%s</trafficMirrorFilter>
</ModifyTrafficMirrorFilterNetworkServicesResponse>`, ec2Xmlns(), generateUUID(), tmFilterBodyXML(f))
}

func tmSessionBodyXML(s EC2TrafficMirrorSession) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<trafficMirrorSessionId>%s</trafficMirrorSessionId>", s.TrafficMirrorSessionId)
	fmt.Fprintf(&b, "<trafficMirrorTargetId>%s</trafficMirrorTargetId>", s.TrafficMirrorTargetId)
	fmt.Fprintf(&b, "<trafficMirrorFilterId>%s</trafficMirrorFilterId>", s.TrafficMirrorFilterId)
	fmt.Fprintf(&b, "<networkInterfaceId>%s</networkInterfaceId>", s.NetworkInterfaceId)
	fmt.Fprintf(&b, "<ownerId>%s</ownerId>", s.OwnerId)
	if s.HasPacketLength {
		fmt.Fprintf(&b, "<packetLength>%d</packetLength>", s.PacketLength)
	}
	fmt.Fprintf(&b, "<sessionNumber>%d</sessionNumber>", s.SessionNumber)
	fmt.Fprintf(&b, "<virtualNetworkId>%d</virtualNetworkId>", s.VirtualNetworkId)
	if s.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(s.Description))
	}
	b.WriteString(writeTagSetXML(s.Tags))
	return b.String()
}

func handleCreateTrafficMirrorSession(w http.ResponseWriter, r *http.Request) {
	targetID := r.FormValue("TrafficMirrorTargetId")
	filterID := r.FormValue("TrafficMirrorFilterId")
	eni := r.FormValue("NetworkInterfaceId")
	if _, ok := ec2TmTargets.Get(targetID); !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorTargetId.NotFound", fmt.Sprintf("The Traffic Mirror target ID '%s' does not exist", targetID), http.StatusBadRequest)
		return
	}
	if _, ok := ec2TmFilters.Get(filterID); !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorFilterId.NotFound", fmt.Sprintf("The Traffic Mirror filter ID '%s' does not exist", filterID), http.StatusBadRequest)
		return
	}
	sessionNumber, _ := ec2FormInt(r, "SessionNumber")
	packetLength, hasPacketLength := ec2FormInt(r, "PacketLength")
	vni, hasVni := ec2FormInt(r, "VirtualNetworkId")
	if !hasVni {
		// Real AWS auto-assigns a VNI in 1..16777215 when omitted.
		vni = 1
	}
	s := EC2TrafficMirrorSession{
		TrafficMirrorSessionId: ec2ID("tms"),
		TrafficMirrorTargetId:  targetID,
		TrafficMirrorFilterId:  filterID,
		NetworkInterfaceId:     eni,
		OwnerId:                ec2Owner(),
		PacketLength:           packetLength,
		HasPacketLength:        hasPacketLength,
		SessionNumber:          sessionNumber,
		VirtualNetworkId:       vni,
		Description:            r.FormValue("Description"),
		Tags:                   parseTags(r),
	}
	ec2TmSessions.Put(s.TrafficMirrorSessionId, s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateTrafficMirrorSessionResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorSession>%s</trafficMirrorSession>
</CreateTrafficMirrorSessionResponse>`, ec2Xmlns(), generateUUID(), tmSessionBodyXML(s))
}

func handleDescribeTrafficMirrorSessions(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "TrafficMirrorSessionId")
	for _, id := range ids {
		if _, ok := ec2TmSessions.Get(id); !ok {
			ec2ErrorXML(w, "InvalidTrafficMirrorSessionId.NotFound", fmt.Sprintf("The Traffic Mirror session ID '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	var items strings.Builder
	for _, s := range ec2TmSessions.List() {
		if len(ids) > 0 && !ec2StrInValues(s.TrafficMirrorSessionId, ids) {
			continue
		}
		items.WriteString("<item>" + tmSessionBodyXML(s) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeTrafficMirrorSessionsResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorSessionSet>%s</trafficMirrorSessionSet>
</DescribeTrafficMirrorSessionsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyTrafficMirrorSession(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TrafficMirrorSessionId")
	s, ok := ec2TmSessions.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorSessionId.NotFound", fmt.Sprintf("The Traffic Mirror session ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("TrafficMirrorTargetId"); v != "" {
		s.TrafficMirrorTargetId = v
	}
	if v := r.FormValue("TrafficMirrorFilterId"); v != "" {
		s.TrafficMirrorFilterId = v
	}
	if v, ok := ec2FormInt(r, "SessionNumber"); ok {
		s.SessionNumber = v
	}
	if v, ok := ec2FormInt(r, "PacketLength"); ok {
		s.PacketLength = v
		s.HasPacketLength = true
	}
	if v, ok := ec2FormInt(r, "VirtualNetworkId"); ok {
		s.VirtualNetworkId = v
	}
	if v := r.FormValue("Description"); v != "" {
		s.Description = v
	}
	ec2TmSessions.Put(id, s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyTrafficMirrorSessionResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorSession>%s</trafficMirrorSession>
</ModifyTrafficMirrorSessionResponse>`, ec2Xmlns(), generateUUID(), tmSessionBodyXML(s))
}

func handleDeleteTrafficMirrorSession(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("TrafficMirrorSessionId")
	if _, ok := ec2TmSessions.Get(id); !ok {
		ec2ErrorXML(w, "InvalidTrafficMirrorSessionId.NotFound", fmt.Sprintf("The Traffic Mirror session ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2TmSessions.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteTrafficMirrorSessionResponse %s>
  <requestId>%s</requestId>
  <trafficMirrorSessionId>%s</trafficMirrorSessionId>
</DeleteTrafficMirrorSessionResponse>`, ec2Xmlns(), generateUUID(), id)
}
