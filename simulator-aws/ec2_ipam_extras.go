package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements faithful control-plane CRUD for the Amazon EC2 IP
// Address Manager (IPAM) families that complement the core IPAM slice in
// ec2_ipam.go: IPAM policies (allocation rules + organization targets, with an
// enabled/disabled toggle), BYOASN (bring-your-own-ASN) entries provisioned
// into an IPAM and associated to CIDRs, prefix-list resolvers and their
// targets (with versioned rule/entry reads), external-resource verification
// tokens, and the read-only discovered-resource probes. Every resource is
// backed by a real SQLite-persisted store and rendered as the exact ec2Query
// XML the AWS SDK for Go v2 and the aws CLI deserialize (member casing taken
// from ec2.smithy.json).

// ---- Types ----

type EC2IpamPolicyAllocationRule struct {
	SourceIpamPoolId string
}

// EC2IpamPolicyDocument is a (locale, resourceType) bucket of allocation rules
// inside a policy. Real AWS keys allocation rules by locale + resource type.
type EC2IpamPolicyDocument struct {
	IpamPolicyId    string
	Locale          string
	ResourceType    string
	AllocationRules []EC2IpamPolicyAllocationRule
}

type EC2IpamPolicy struct {
	OwnerId          string
	IpamPolicyId     string
	IpamPolicyArn    string
	IpamPolicyRegion string
	State            string
	StateMessage     string
	IpamId           string
	Enabled          bool
	Documents        []EC2IpamPolicyDocument
	Tags             []EC2Tag
}

type EC2IpamByoasn struct {
	Asn           string
	IpamId        string
	StatusMessage string
	State         string
}

// EC2IpamAsnAssociation records an ASN associated to a BYOIP CIDR.
type EC2IpamAsnAssociation struct {
	Asn           string
	Cidr          string
	StatusMessage string
	State         string
}

type EC2IpamByoipCidr struct {
	Cidr               string
	Description        string
	StatusMessage      string
	State              string
	NetworkBorderGroup string
	AdvertisementType  string
	Asn                string
}

type EC2IpamPrefixListResolverRuleCondition struct {
	Operation      string
	IpamPoolId     string
	ResourceId     string
	ResourceOwner  string
	ResourceRegion string
	Cidr           string
}

type EC2IpamPrefixListResolverRule struct {
	RuleType     string
	StaticCidr   string
	IpamScopeId  string
	ResourceType string
	Conditions   []EC2IpamPrefixListResolverRuleCondition
}

type EC2IpamPrefixListResolver struct {
	OwnerId                          string
	IpamPrefixListResolverId         string
	IpamPrefixListResolverArn        string
	IpamArn                          string
	IpamRegion                       string
	Description                      string
	AddressFamily                    string
	State                            string
	LastVersionCreationStatus        string
	LastVersionCreationStatusMessage string
	Rules                            []EC2IpamPrefixListResolverRule
	Tags                             []EC2Tag
}

type EC2IpamPrefixListResolverTarget struct {
	IpamPrefixListResolverTargetId  string
	IpamPrefixListResolverTargetArn string
	IpamPrefixListResolverId        string
	OwnerId                         string
	PrefixListId                    string
	PrefixListRegion                string
	DesiredVersion                  int64
	HasDesiredVersion               bool
	LastSyncedVersion               int64
	HasLastSyncedVersion            bool
	TrackLatestVersion              bool
	StateMessage                    string
	State                           string
	Tags                            []EC2Tag
}

type EC2IpamExternalResourceVerificationToken struct {
	IpamExternalResourceVerificationTokenId  string
	IpamExternalResourceVerificationTokenArn string
	IpamId                                   string
	IpamArn                                  string
	IpamRegion                               string
	TokenValue                               string
	TokenName                                string
	NotAfter                                 string
	Status                                   string
	State                                    string
	Tags                                     []EC2Tag
}

var (
	ec2IpamPolicies       sim.Store[EC2IpamPolicy]
	ec2IpamByoasns        sim.Store[EC2IpamByoasn]
	ec2IpamByoipCidrs     sim.Store[EC2IpamByoipCidr]
	ec2IpamPLResolvers    sim.Store[EC2IpamPrefixListResolver]
	ec2IpamPLResolverTgts sim.Store[EC2IpamPrefixListResolverTarget]
	ec2IpamExtResTokens   sim.Store[EC2IpamExternalResourceVerificationToken]
)

func registerEC2IPAMExtras(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2IpamPolicies = sim.MakeStore[EC2IpamPolicy](srv.DB(), "ec2_ipam_policies")
	ec2IpamByoasns = sim.MakeStore[EC2IpamByoasn](srv.DB(), "ec2_ipam_byoasns")
	ec2IpamByoipCidrs = sim.MakeStore[EC2IpamByoipCidr](srv.DB(), "ec2_ipam_byoip_cidrs")
	ec2IpamPLResolvers = sim.MakeStore[EC2IpamPrefixListResolver](srv.DB(), "ec2_ipam_pl_resolvers")
	ec2IpamPLResolverTgts = sim.MakeStore[EC2IpamPrefixListResolverTarget](srv.DB(), "ec2_ipam_pl_resolver_targets")
	ec2IpamExtResTokens = sim.MakeStore[EC2IpamExternalResourceVerificationToken](srv.DB(), "ec2_ipam_ext_res_tokens")

	// IPAM policies
	r.Register("CreateIpamPolicy", handleCreateIpamPolicy)
	r.Register("DescribeIpamPolicies", handleDescribeIpamPolicies)
	r.Register("DeleteIpamPolicy", handleDeleteIpamPolicy)
	r.Register("EnableIpamPolicy", handleEnableIpamPolicy)
	r.Register("DisableIpamPolicy", handleDisableIpamPolicy)
	r.Register("GetEnabledIpamPolicy", handleGetEnabledIpamPolicy)
	r.Register("GetIpamPolicyAllocationRules", handleGetIpamPolicyAllocationRules)
	r.Register("ModifyIpamPolicyAllocationRules", handleModifyIpamPolicyAllocationRules)
	r.Register("GetIpamPolicyOrganizationTargets", handleGetIpamPolicyOrganizationTargets)

	// BYOASN
	r.Register("ProvisionIpamByoasn", handleProvisionIpamByoasn)
	r.Register("DeprovisionIpamByoasn", handleDeprovisionIpamByoasn)
	r.Register("DescribeIpamByoasn", handleDescribeIpamByoasn)
	r.Register("AssociateIpamByoasn", handleAssociateIpamByoasn)
	r.Register("DisassociateIpamByoasn", handleDisassociateIpamByoasn)
	r.Register("MoveByoipCidrToIpam", handleMoveByoipCidrToIpam)

	// Prefix-list resolvers + targets
	r.Register("CreateIpamPrefixListResolver", handleCreateIpamPrefixListResolver)
	r.Register("DescribeIpamPrefixListResolvers", handleDescribeIpamPrefixListResolvers)
	r.Register("ModifyIpamPrefixListResolver", handleModifyIpamPrefixListResolver)
	r.Register("DeleteIpamPrefixListResolver", handleDeleteIpamPrefixListResolver)
	r.Register("CreateIpamPrefixListResolverTarget", handleCreateIpamPrefixListResolverTarget)
	r.Register("DescribeIpamPrefixListResolverTargets", handleDescribeIpamPrefixListResolverTargets)
	r.Register("ModifyIpamPrefixListResolverTarget", handleModifyIpamPrefixListResolverTarget)
	r.Register("DeleteIpamPrefixListResolverTarget", handleDeleteIpamPrefixListResolverTarget)
	r.Register("GetIpamPrefixListResolverRules", handleGetIpamPrefixListResolverRules)
	r.Register("GetIpamPrefixListResolverVersions", handleGetIpamPrefixListResolverVersions)
	r.Register("GetIpamPrefixListResolverVersionEntries", handleGetIpamPrefixListResolverVersionEntries)

	// External-resource verification tokens
	r.Register("CreateIpamExternalResourceVerificationToken", handleCreateIpamExternalResourceVerificationToken)
	r.Register("DescribeIpamExternalResourceVerificationTokens", handleDescribeIpamExternalResourceVerificationTokens)
	r.Register("DeleteIpamExternalResourceVerificationToken", handleDeleteIpamExternalResourceVerificationToken)

	// Discovered resources (read-only honest-empty)
	r.Register("GetIpamDiscoveredAccounts", handleGetIpamDiscoveredAccounts)
	r.Register("GetIpamDiscoveredPublicAddresses", handleGetIpamDiscoveredPublicAddresses)
	r.Register("GetIpamDiscoveredResourceCidrs", handleGetIpamDiscoveredResourceCidrs)

	// Pool allocation mutation
	r.Register("ModifyIpamPoolAllocation", handleModifyIpamPoolAllocation)
}

// ec2ParseIpamPolicyAllocationRules reads AllocationRule.N.SourceIpamPoolId.
func ec2ParseIpamPolicyAllocationRules(r *http.Request, prefix string) []EC2IpamPolicyAllocationRule {
	var rules []EC2IpamPolicyAllocationRule
	for i := 1; ; i++ {
		pool := r.FormValue(fmt.Sprintf("%s.%d.SourceIpamPoolId", prefix, i))
		if pool == "" {
			break
		}
		rules = append(rules, EC2IpamPolicyAllocationRule{SourceIpamPoolId: pool})
	}
	return rules
}

// ---- IPAM policies ----

func handleCreateIpamPolicy(w http.ResponseWriter, r *http.Request) {
	ipamID := r.FormValue("IpamId")
	if _, ok := ec2Ipams.Get(ipamID); !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", ipamID), http.StatusBadRequest)
		return
	}
	region := awsRegion()
	id := ec2ID("ipam-policy")
	pol := EC2IpamPolicy{
		OwnerId:          ec2Owner(),
		IpamPolicyId:     id,
		IpamPolicyArn:    ipamArn("ipam-policy/" + id),
		IpamPolicyRegion: region,
		State:            "create-complete",
		IpamId:           ipamID,
		Tags:             parseTags(r),
	}
	ec2IpamPolicies.Put(id, pol)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamPolicyResponse %s>
  <requestId>%s</requestId>
  <ipamPolicy>%s</ipamPolicy>
</CreateIpamPolicyResponse>`, ec2Xmlns(), generateUUID(), ipamPolicyBodyXML(pol))
}

func ipamPolicyBodyXML(p EC2IpamPolicy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ownerId>%s</ownerId><ipamPolicyId>%s</ipamPolicyId><ipamPolicyArn>%s</ipamPolicyArn><ipamPolicyRegion>%s</ipamPolicyRegion>",
		p.OwnerId, p.IpamPolicyId, p.IpamPolicyArn, p.IpamPolicyRegion)
	fmt.Fprintf(&b, "<state>%s</state>", p.State)
	if p.StateMessage != "" {
		fmt.Fprintf(&b, "<stateMessage>%s</stateMessage>", p.StateMessage)
	}
	b.WriteString(writeTagSetXML(p.Tags))
	fmt.Fprintf(&b, "<ipamId>%s</ipamId>", p.IpamId)
	return b.String()
}

func handleDescribeIpamPolicies(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamPolicyId")
	var items strings.Builder
	for _, p := range ec2IpamPolicies.List() {
		if len(ids) > 0 && !ec2StrInValues(p.IpamPolicyId, ids) {
			continue
		}
		items.WriteString("<item>" + ipamPolicyBodyXML(p) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamPoliciesResponse %s>
  <requestId>%s</requestId>
  <ipamPolicySet>%s</ipamPolicySet>
</DescribeIpamPoliciesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteIpamPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPolicyId")
	pol, ok := ec2IpamPolicies.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPolicyId.NotFound", fmt.Sprintf("The ipam policy ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	pol.State = "delete-complete"
	ec2IpamPolicies.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamPolicyResponse %s>
  <requestId>%s</requestId>
  <ipamPolicy>%s</ipamPolicy>
</DeleteIpamPolicyResponse>`, ec2Xmlns(), generateUUID(), ipamPolicyBodyXML(pol))
}

func handleEnableIpamPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPolicyId")
	if _, ok := ec2IpamPolicies.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamPolicyId.NotFound", fmt.Sprintf("The ipam policy ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// Only one policy is enabled at a time per account, like real AWS.
	for _, p := range ec2IpamPolicies.List() {
		if p.Enabled && p.IpamPolicyId != id {
			ec2IpamPolicies.Update(p.IpamPolicyId, func(pp *EC2IpamPolicy) { pp.Enabled = false })
		}
	}
	ec2IpamPolicies.Update(id, func(p *EC2IpamPolicy) { p.Enabled = true })
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableIpamPolicyResponse %s>
  <requestId>%s</requestId>
  <ipamPolicyId>%s</ipamPolicyId>
</EnableIpamPolicyResponse>`, ec2Xmlns(), generateUUID(), id)
}

func handleDisableIpamPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPolicyId")
	if _, ok := ec2IpamPolicies.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamPolicyId.NotFound", fmt.Sprintf("The ipam policy ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2IpamPolicies.Update(id, func(p *EC2IpamPolicy) { p.Enabled = false })
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableIpamPolicyResponse %s>
  <requestId>%s</requestId>
  <return>true</return>
</DisableIpamPolicyResponse>`, ec2Xmlns(), generateUUID())
}

func handleGetEnabledIpamPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	var enabled *EC2IpamPolicy
	for _, p := range ec2IpamPolicies.List() {
		if p.Enabled {
			cp := p
			enabled = &cp
			break
		}
	}
	if enabled == nil {
		fmt.Fprintf(w, `<GetEnabledIpamPolicyResponse %s>
  <requestId>%s</requestId>
  <ipamPolicyEnabled>false</ipamPolicyEnabled>
</GetEnabledIpamPolicyResponse>`, ec2Xmlns(), generateUUID())
		return
	}
	fmt.Fprintf(w, `<GetEnabledIpamPolicyResponse %s>
  <requestId>%s</requestId>
  <ipamPolicyEnabled>true</ipamPolicyEnabled>
  <ipamPolicyId>%s</ipamPolicyId>
  <managedBy>account</managedBy>
</GetEnabledIpamPolicyResponse>`, ec2Xmlns(), generateUUID(), enabled.IpamPolicyId)
}

func ipamPolicyDocumentXML(d EC2IpamPolicyDocument) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ipamPolicyId>%s</ipamPolicyId>", d.IpamPolicyId)
	if d.Locale != "" {
		fmt.Fprintf(&b, "<locale>%s</locale>", d.Locale)
	}
	if d.ResourceType != "" {
		fmt.Fprintf(&b, "<resourceType>%s</resourceType>", d.ResourceType)
	}
	b.WriteString("<allocationRuleSet>")
	for _, rule := range d.AllocationRules {
		fmt.Fprintf(&b, "<item><sourceIpamPoolId>%s</sourceIpamPoolId></item>", rule.SourceIpamPoolId)
	}
	b.WriteString("</allocationRuleSet>")
	return b.String()
}

func handleGetIpamPolicyAllocationRules(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPolicyId")
	pol, ok := ec2IpamPolicies.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPolicyId.NotFound", fmt.Sprintf("The ipam policy ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	locale := r.FormValue("Locale")
	resType := r.FormValue("ResourceType")
	var items strings.Builder
	for _, d := range pol.Documents {
		if locale != "" && d.Locale != locale {
			continue
		}
		if resType != "" && d.ResourceType != resType {
			continue
		}
		items.WriteString("<item>" + ipamPolicyDocumentXML(d) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamPolicyAllocationRulesResponse %s>
  <requestId>%s</requestId>
  <ipamPolicyDocumentSet>%s</ipamPolicyDocumentSet>
</GetIpamPolicyAllocationRulesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyIpamPolicyAllocationRules(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPolicyId")
	if _, ok := ec2IpamPolicies.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamPolicyId.NotFound", fmt.Sprintf("The ipam policy ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	doc := EC2IpamPolicyDocument{
		IpamPolicyId:    id,
		Locale:          r.FormValue("Locale"),
		ResourceType:    r.FormValue("ResourceType"),
		AllocationRules: ec2ParseIpamPolicyAllocationRules(r, "AllocationRule"),
	}
	ec2IpamPolicies.Update(id, func(p *EC2IpamPolicy) {
		next := p.Documents[:0:0]
		for _, d := range p.Documents {
			if d.Locale == doc.Locale && d.ResourceType == doc.ResourceType {
				continue
			}
			next = append(next, d)
		}
		p.Documents = append(next, doc)
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamPolicyAllocationRulesResponse %s>
  <requestId>%s</requestId>
  <ipamPolicyDocument>%s</ipamPolicyDocument>
</ModifyIpamPolicyAllocationRulesResponse>`, ec2Xmlns(), generateUUID(), ipamPolicyDocumentXML(doc))
}

func handleGetIpamPolicyOrganizationTargets(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPolicyId")
	if _, ok := ec2IpamPolicies.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamPolicyId.NotFound", fmt.Sprintf("The ipam policy ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// An account-managed policy has no Organizations targets until one applies
	// it to an entity; honest-empty here.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamPolicyOrganizationTargetsResponse %s>
  <requestId>%s</requestId>
  <organizationTargetSet/>
</GetIpamPolicyOrganizationTargetsResponse>`, ec2Xmlns(), generateUUID())
}

// ---- BYOASN ----

func ipamByoasnBodyXML(b EC2IpamByoasn) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<asn>%s</asn><ipamId>%s</ipamId>", b.Asn, b.IpamId)
	if b.StatusMessage != "" {
		fmt.Fprintf(&sb, "<statusMessage>%s</statusMessage>", b.StatusMessage)
	}
	fmt.Fprintf(&sb, "<state>%s</state>", b.State)
	return sb.String()
}

func handleProvisionIpamByoasn(w http.ResponseWriter, r *http.Request) {
	ipamID := r.FormValue("IpamId")
	if _, ok := ec2Ipams.Get(ipamID); !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", ipamID), http.StatusBadRequest)
		return
	}
	asn := r.FormValue("Asn")
	byo := EC2IpamByoasn{
		Asn:    asn,
		IpamId: ipamID,
		State:  "provisioned",
	}
	ec2IpamByoasns.Put(asn, byo)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ProvisionIpamByoasnResponse %s>
  <requestId>%s</requestId>
  <byoasn>%s</byoasn>
</ProvisionIpamByoasnResponse>`, ec2Xmlns(), generateUUID(), ipamByoasnBodyXML(byo))
}

func handleDeprovisionIpamByoasn(w http.ResponseWriter, r *http.Request) {
	asn := r.FormValue("Asn")
	byo, ok := ec2IpamByoasns.Get(asn)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamByoasn.NotFound", fmt.Sprintf("The ASN '%s' is not provisioned", asn), http.StatusBadRequest)
		return
	}
	byo.State = "deprovisioned"
	ec2IpamByoasns.Delete(asn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeprovisionIpamByoasnResponse %s>
  <requestId>%s</requestId>
  <byoasn>%s</byoasn>
</DeprovisionIpamByoasnResponse>`, ec2Xmlns(), generateUUID(), ipamByoasnBodyXML(byo))
}

func handleDescribeIpamByoasn(w http.ResponseWriter, r *http.Request) {
	var items strings.Builder
	for _, b := range ec2IpamByoasns.List() {
		items.WriteString("<item>" + ipamByoasnBodyXML(b) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamByoasnResponse %s>
  <requestId>%s</requestId>
  <byoasnSet>%s</byoasnSet>
</DescribeIpamByoasnResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func ipamAsnAssociationXML(a EC2IpamAsnAssociation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<asn>%s</asn><cidr>%s</cidr>", a.Asn, a.Cidr)
	if a.StatusMessage != "" {
		fmt.Fprintf(&b, "<statusMessage>%s</statusMessage>", a.StatusMessage)
	}
	fmt.Fprintf(&b, "<state>%s</state>", a.State)
	return b.String()
}

func handleAssociateIpamByoasn(w http.ResponseWriter, r *http.Request) {
	asn := r.FormValue("Asn")
	cidr := r.FormValue("Cidr")
	if _, ok := ec2IpamByoasns.Get(asn); !ok {
		ec2ErrorXML(w, "InvalidIpamByoasn.NotFound", fmt.Sprintf("The ASN '%s' is not provisioned", asn), http.StatusBadRequest)
		return
	}
	assoc := EC2IpamAsnAssociation{Asn: asn, Cidr: cidr, State: "associated"}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateIpamByoasnResponse %s>
  <requestId>%s</requestId>
  <asnAssociation>%s</asnAssociation>
</AssociateIpamByoasnResponse>`, ec2Xmlns(), generateUUID(), ipamAsnAssociationXML(assoc))
}

func handleDisassociateIpamByoasn(w http.ResponseWriter, r *http.Request) {
	asn := r.FormValue("Asn")
	cidr := r.FormValue("Cidr")
	if _, ok := ec2IpamByoasns.Get(asn); !ok {
		ec2ErrorXML(w, "InvalidIpamByoasn.NotFound", fmt.Sprintf("The ASN '%s' is not provisioned", asn), http.StatusBadRequest)
		return
	}
	assoc := EC2IpamAsnAssociation{Asn: asn, Cidr: cidr, State: "disassociated"}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateIpamByoasnResponse %s>
  <requestId>%s</requestId>
  <asnAssociation>%s</asnAssociation>
</DisassociateIpamByoasnResponse>`, ec2Xmlns(), generateUUID(), ipamAsnAssociationXML(assoc))
}

func handleMoveByoipCidrToIpam(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Cidr")
	if _, ok := ec2IpamPools.Get(r.FormValue("IpamPoolId")); !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", r.FormValue("IpamPoolId")), http.StatusBadRequest)
		return
	}
	byo := EC2IpamByoipCidr{
		Cidr:              cidr,
		State:             "provisioned",
		AdvertisementType: "manual",
	}
	ec2IpamByoipCidrs.Put(cidr, byo)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<MoveByoipCidrToIpamResponse %s>
  <requestId>%s</requestId>
  <byoipCidr>%s</byoipCidr>
</MoveByoipCidrToIpamResponse>`, ec2Xmlns(), generateUUID(), ipamByoipCidrXML(byo))
}

func ipamByoipCidrXML(b EC2IpamByoipCidr) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<cidr>%s</cidr>", b.Cidr)
	if b.Description != "" {
		fmt.Fprintf(&sb, "<description>%s</description>", b.Description)
	}
	if b.StatusMessage != "" {
		fmt.Fprintf(&sb, "<statusMessage>%s</statusMessage>", b.StatusMessage)
	}
	fmt.Fprintf(&sb, "<state>%s</state>", b.State)
	if b.NetworkBorderGroup != "" {
		fmt.Fprintf(&sb, "<networkBorderGroup>%s</networkBorderGroup>", b.NetworkBorderGroup)
	}
	if b.AdvertisementType != "" {
		fmt.Fprintf(&sb, "<advertisementType>%s</advertisementType>", b.AdvertisementType)
	}
	return sb.String()
}

// ---- Prefix-list resolvers ----

// ec2ParseIpamPLResolverRules reads Rule.N.{RuleType,StaticCidr,IpamScopeId,
// ResourceType} request params.
func ec2ParseIpamPLResolverRules(r *http.Request, prefix string) []EC2IpamPrefixListResolverRule {
	var rules []EC2IpamPrefixListResolverRule
	for i := 1; ; i++ {
		base := fmt.Sprintf("%s.%d", prefix, i)
		ruleType := r.FormValue(base + ".RuleType")
		staticCidr := r.FormValue(base + ".StaticCidr")
		scope := r.FormValue(base + ".IpamScopeId")
		resType := r.FormValue(base + ".ResourceType")
		if ruleType == "" && staticCidr == "" && scope == "" && resType == "" {
			break
		}
		rules = append(rules, EC2IpamPrefixListResolverRule{
			RuleType:     ruleType,
			StaticCidr:   staticCidr,
			IpamScopeId:  scope,
			ResourceType: resType,
		})
	}
	return rules
}

func ipamPLResolverBodyXML(p EC2IpamPrefixListResolver) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ownerId>%s</ownerId><ipamPrefixListResolverId>%s</ipamPrefixListResolverId><ipamPrefixListResolverArn>%s</ipamPrefixListResolverArn>",
		p.OwnerId, p.IpamPrefixListResolverId, p.IpamPrefixListResolverArn)
	fmt.Fprintf(&b, "<ipamArn>%s</ipamArn><ipamRegion>%s</ipamRegion>", p.IpamArn, p.IpamRegion)
	if p.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", p.Description)
	}
	fmt.Fprintf(&b, "<addressFamily>%s</addressFamily><state>%s</state>", p.AddressFamily, p.State)
	b.WriteString(writeTagSetXML(p.Tags))
	if p.LastVersionCreationStatus != "" {
		fmt.Fprintf(&b, "<lastVersionCreationStatus>%s</lastVersionCreationStatus>", p.LastVersionCreationStatus)
	}
	if p.LastVersionCreationStatusMessage != "" {
		fmt.Fprintf(&b, "<lastVersionCreationStatusMessage>%s</lastVersionCreationStatusMessage>", p.LastVersionCreationStatusMessage)
	}
	return b.String()
}

func handleCreateIpamPrefixListResolver(w http.ResponseWriter, r *http.Request) {
	ipamID := r.FormValue("IpamId")
	ipam, ok := ec2Ipams.Get(ipamID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", ipamID), http.StatusBadRequest)
		return
	}
	af := r.FormValue("AddressFamily")
	if af == "" {
		af = "ipv4"
	}
	id := ec2ID("ipam-pl-res")
	res := EC2IpamPrefixListResolver{
		OwnerId:                   ec2Owner(),
		IpamPrefixListResolverId:  id,
		IpamPrefixListResolverArn: ipamArn("ipam-prefix-list-resolver/" + id),
		IpamArn:                   ipam.IpamArn,
		IpamRegion:                ipam.IpamRegion,
		Description:               r.FormValue("Description"),
		AddressFamily:             af,
		State:                     "create-complete",
		LastVersionCreationStatus: "success",
		Rules:                     ec2ParseIpamPLResolverRules(r, "Rule"),
		Tags:                      parseTags(r),
	}
	ec2IpamPLResolvers.Put(id, res)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamPrefixListResolverResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolver>%s</ipamPrefixListResolver>
</CreateIpamPrefixListResolverResponse>`, ec2Xmlns(), generateUUID(), ipamPLResolverBodyXML(res))
}

func handleDescribeIpamPrefixListResolvers(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamPrefixListResolverId")
	var items strings.Builder
	for _, p := range ec2IpamPLResolvers.List() {
		if len(ids) > 0 && !ec2StrInValues(p.IpamPrefixListResolverId, ids) {
			continue
		}
		items.WriteString("<item>" + ipamPLResolverBodyXML(p) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamPrefixListResolversResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolverSet>%s</ipamPrefixListResolverSet>
</DescribeIpamPrefixListResolversResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyIpamPrefixListResolver(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPrefixListResolverId")
	if _, ok := ec2IpamPLResolvers.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamPrefixListResolverId.NotFound", fmt.Sprintf("The ipam prefix list resolver ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	rules := ec2ParseIpamPLResolverRules(r, "Rule")
	ec2IpamPLResolvers.Update(id, func(p *EC2IpamPrefixListResolver) {
		if _, has := r.Form["Description"]; has {
			p.Description = r.FormValue("Description")
		}
		if len(rules) > 0 {
			p.Rules = rules
		}
		p.State = "modify-complete"
	})
	res, _ := ec2IpamPLResolvers.Get(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamPrefixListResolverResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolver>%s</ipamPrefixListResolver>
</ModifyIpamPrefixListResolverResponse>`, ec2Xmlns(), generateUUID(), ipamPLResolverBodyXML(res))
}

func handleDeleteIpamPrefixListResolver(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPrefixListResolverId")
	res, ok := ec2IpamPLResolvers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPrefixListResolverId.NotFound", fmt.Sprintf("The ipam prefix list resolver ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	res.State = "delete-complete"
	// Targets under the resolver go away with it.
	for _, t := range ec2IpamPLResolverTgts.List() {
		if t.IpamPrefixListResolverId == id {
			ec2IpamPLResolverTgts.Delete(t.IpamPrefixListResolverTargetId)
		}
	}
	ec2IpamPLResolvers.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamPrefixListResolverResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolver>%s</ipamPrefixListResolver>
</DeleteIpamPrefixListResolverResponse>`, ec2Xmlns(), generateUUID(), ipamPLResolverBodyXML(res))
}

func ipamPLResolverTargetXML(t EC2IpamPrefixListResolverTarget) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ipamPrefixListResolverTargetId>%s</ipamPrefixListResolverTargetId><ipamPrefixListResolverTargetArn>%s</ipamPrefixListResolverTargetArn>",
		t.IpamPrefixListResolverTargetId, t.IpamPrefixListResolverTargetArn)
	fmt.Fprintf(&b, "<ipamPrefixListResolverId>%s</ipamPrefixListResolverId><ownerId>%s</ownerId>", t.IpamPrefixListResolverId, t.OwnerId)
	fmt.Fprintf(&b, "<prefixListId>%s</prefixListId>", t.PrefixListId)
	if t.PrefixListRegion != "" {
		fmt.Fprintf(&b, "<prefixListRegion>%s</prefixListRegion>", t.PrefixListRegion)
	}
	if t.HasDesiredVersion {
		fmt.Fprintf(&b, "<desiredVersion>%d</desiredVersion>", t.DesiredVersion)
	}
	if t.HasLastSyncedVersion {
		fmt.Fprintf(&b, "<lastSyncedVersion>%d</lastSyncedVersion>", t.LastSyncedVersion)
	}
	fmt.Fprintf(&b, "<trackLatestVersion>%t</trackLatestVersion>", t.TrackLatestVersion)
	if t.StateMessage != "" {
		fmt.Fprintf(&b, "<stateMessage>%s</stateMessage>", t.StateMessage)
	}
	fmt.Fprintf(&b, "<state>%s</state>", t.State)
	b.WriteString(writeTagSetXML(t.Tags))
	return b.String()
}

func handleCreateIpamPrefixListResolverTarget(w http.ResponseWriter, r *http.Request) {
	resID := r.FormValue("IpamPrefixListResolverId")
	if _, ok := ec2IpamPLResolvers.Get(resID); !ok {
		ec2ErrorXML(w, "InvalidIpamPrefixListResolverId.NotFound", fmt.Sprintf("The ipam prefix list resolver ID '%s' does not exist", resID), http.StatusBadRequest)
		return
	}
	id := ec2ID("ipam-pl-res-target")
	track := r.FormValue("TrackLatestVersion") == "true"
	tgt := EC2IpamPrefixListResolverTarget{
		IpamPrefixListResolverTargetId:  id,
		IpamPrefixListResolverTargetArn: ipamArn("ipam-prefix-list-resolver-target/" + id),
		IpamPrefixListResolverId:        resID,
		OwnerId:                         ec2Owner(),
		PrefixListId:                    r.FormValue("PrefixListId"),
		PrefixListRegion:                r.FormValue("PrefixListRegion"),
		TrackLatestVersion:              track,
		State:                           "create-complete",
		Tags:                            parseTags(r),
	}
	if v := r.FormValue("DesiredVersion"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			tgt.DesiredVersion = n
			tgt.HasDesiredVersion = true
		}
	}
	ec2IpamPLResolverTgts.Put(id, tgt)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamPrefixListResolverTargetResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolverTarget>%s</ipamPrefixListResolverTarget>
</CreateIpamPrefixListResolverTargetResponse>`, ec2Xmlns(), generateUUID(), ipamPLResolverTargetXML(tgt))
}

func handleDescribeIpamPrefixListResolverTargets(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamPrefixListResolverTargetId")
	resID := r.FormValue("IpamPrefixListResolverId")
	var items strings.Builder
	for _, t := range ec2IpamPLResolverTgts.List() {
		if len(ids) > 0 && !ec2StrInValues(t.IpamPrefixListResolverTargetId, ids) {
			continue
		}
		if resID != "" && t.IpamPrefixListResolverId != resID {
			continue
		}
		items.WriteString("<item>" + ipamPLResolverTargetXML(t) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamPrefixListResolverTargetsResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolverTargetSet>%s</ipamPrefixListResolverTargetSet>
</DescribeIpamPrefixListResolverTargetsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyIpamPrefixListResolverTarget(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPrefixListResolverTargetId")
	if _, ok := ec2IpamPLResolverTgts.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamPrefixListResolverTargetId.NotFound", fmt.Sprintf("The ipam prefix list resolver target ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2IpamPLResolverTgts.Update(id, func(t *EC2IpamPrefixListResolverTarget) {
		if v := r.FormValue("DesiredVersion"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				t.DesiredVersion = n
				t.HasDesiredVersion = true
			}
		}
		if v := r.FormValue("TrackLatestVersion"); v != "" {
			t.TrackLatestVersion = v == "true"
		}
		t.State = "modify-complete"
	})
	tgt, _ := ec2IpamPLResolverTgts.Get(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamPrefixListResolverTargetResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolverTarget>%s</ipamPrefixListResolverTarget>
</ModifyIpamPrefixListResolverTargetResponse>`, ec2Xmlns(), generateUUID(), ipamPLResolverTargetXML(tgt))
}

func handleDeleteIpamPrefixListResolverTarget(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPrefixListResolverTargetId")
	tgt, ok := ec2IpamPLResolverTgts.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPrefixListResolverTargetId.NotFound", fmt.Sprintf("The ipam prefix list resolver target ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	tgt.State = "delete-complete"
	ec2IpamPLResolverTgts.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamPrefixListResolverTargetResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolverTarget>%s</ipamPrefixListResolverTarget>
</DeleteIpamPrefixListResolverTargetResponse>`, ec2Xmlns(), generateUUID(), ipamPLResolverTargetXML(tgt))
}

func ipamPLResolverRuleXML(rule EC2IpamPrefixListResolverRule) string {
	var b strings.Builder
	if rule.RuleType != "" {
		fmt.Fprintf(&b, "<ruleType>%s</ruleType>", rule.RuleType)
	}
	if rule.StaticCidr != "" {
		fmt.Fprintf(&b, "<staticCidr>%s</staticCidr>", rule.StaticCidr)
	}
	if rule.IpamScopeId != "" {
		fmt.Fprintf(&b, "<ipamScopeId>%s</ipamScopeId>", rule.IpamScopeId)
	}
	if rule.ResourceType != "" {
		fmt.Fprintf(&b, "<resourceType>%s</resourceType>", rule.ResourceType)
	}
	b.WriteString("<conditionSet>")
	for _, c := range rule.Conditions {
		b.WriteString("<item>")
		if c.Operation != "" {
			fmt.Fprintf(&b, "<operation>%s</operation>", c.Operation)
		}
		if c.IpamPoolId != "" {
			fmt.Fprintf(&b, "<ipamPoolId>%s</ipamPoolId>", c.IpamPoolId)
		}
		if c.Cidr != "" {
			fmt.Fprintf(&b, "<cidr>%s</cidr>", c.Cidr)
		}
		b.WriteString("</item>")
	}
	b.WriteString("</conditionSet>")
	return b.String()
}

func handleGetIpamPrefixListResolverRules(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPrefixListResolverId")
	res, ok := ec2IpamPLResolvers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPrefixListResolverId.NotFound", fmt.Sprintf("The ipam prefix list resolver ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	var items strings.Builder
	for _, rule := range res.Rules {
		items.WriteString("<item>" + ipamPLResolverRuleXML(rule) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamPrefixListResolverRulesResponse %s>
  <requestId>%s</requestId>
  <ruleSet>%s</ruleSet>
</GetIpamPrefixListResolverRulesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleGetIpamPrefixListResolverVersions(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPrefixListResolverId")
	if _, ok := ec2IpamPLResolvers.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamPrefixListResolverId.NotFound", fmt.Sprintf("The ipam prefix list resolver ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// A freshly created resolver has version 1.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamPrefixListResolverVersionsResponse %s>
  <requestId>%s</requestId>
  <ipamPrefixListResolverVersionSet><item><version>1</version></item></ipamPrefixListResolverVersionSet>
</GetIpamPrefixListResolverVersionsResponse>`, ec2Xmlns(), generateUUID())
}

func handleGetIpamPrefixListResolverVersionEntries(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPrefixListResolverId")
	res, ok := ec2IpamPLResolvers.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPrefixListResolverId.NotFound", fmt.Sprintf("The ipam prefix list resolver ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// Static-CIDR rules resolve directly to entries; pool/resource rules resolve
	// from live discovery the sim has no backend for, so they yield no entries.
	var items strings.Builder
	for _, rule := range res.Rules {
		if rule.StaticCidr != "" {
			fmt.Fprintf(&items, "<item><cidr>%s</cidr></item>", rule.StaticCidr)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamPrefixListResolverVersionEntriesResponse %s>
  <requestId>%s</requestId>
  <entrySet>%s</entrySet>
</GetIpamPrefixListResolverVersionEntriesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

// ---- External-resource verification tokens ----

func ipamExtResTokenXML(t EC2IpamExternalResourceVerificationToken) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ipamExternalResourceVerificationTokenId>%s</ipamExternalResourceVerificationTokenId><ipamExternalResourceVerificationTokenArn>%s</ipamExternalResourceVerificationTokenArn>",
		t.IpamExternalResourceVerificationTokenId, t.IpamExternalResourceVerificationTokenArn)
	fmt.Fprintf(&b, "<ipamId>%s</ipamId><ipamArn>%s</ipamArn><ipamRegion>%s</ipamRegion>", t.IpamId, t.IpamArn, t.IpamRegion)
	fmt.Fprintf(&b, "<tokenValue>%s</tokenValue><tokenName>%s</tokenName>", t.TokenValue, t.TokenName)
	if t.NotAfter != "" {
		fmt.Fprintf(&b, "<notAfter>%s</notAfter>", t.NotAfter)
	}
	fmt.Fprintf(&b, "<status>%s</status>", t.Status)
	b.WriteString(writeTagSetXML(t.Tags))
	fmt.Fprintf(&b, "<state>%s</state>", t.State)
	return b.String()
}

func handleCreateIpamExternalResourceVerificationToken(w http.ResponseWriter, r *http.Request) {
	ipamID := r.FormValue("IpamId")
	ipam, ok := ec2Ipams.Get(ipamID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", ipamID), http.StatusBadRequest)
		return
	}
	id := ec2ID("ipam-ext-res")
	tok := EC2IpamExternalResourceVerificationToken{
		IpamExternalResourceVerificationTokenId:  id,
		IpamExternalResourceVerificationTokenArn: ipamArn("ipam-external-resource-verification-token/" + id),
		IpamId:                                   ipamID,
		IpamArn:                                  ipam.IpamArn,
		IpamRegion:                               ipam.IpamRegion,
		TokenValue:                               generateUUID(),
		TokenName:                                id,
		NotAfter:                                 ec2NowRFC3339Milli(),
		Status:                                   "valid",
		State:                                    "create-complete",
		Tags:                                     parseTags(r),
	}
	ec2IpamExtResTokens.Put(id, tok)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamExternalResourceVerificationTokenResponse %s>
  <requestId>%s</requestId>
  <ipamExternalResourceVerificationToken>%s</ipamExternalResourceVerificationToken>
</CreateIpamExternalResourceVerificationTokenResponse>`, ec2Xmlns(), generateUUID(), ipamExtResTokenXML(tok))
}

func handleDescribeIpamExternalResourceVerificationTokens(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamExternalResourceVerificationTokenId")
	var items strings.Builder
	for _, t := range ec2IpamExtResTokens.List() {
		if len(ids) > 0 && !ec2StrInValues(t.IpamExternalResourceVerificationTokenId, ids) {
			continue
		}
		items.WriteString("<item>" + ipamExtResTokenXML(t) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamExternalResourceVerificationTokensResponse %s>
  <requestId>%s</requestId>
  <ipamExternalResourceVerificationTokenSet>%s</ipamExternalResourceVerificationTokenSet>
</DescribeIpamExternalResourceVerificationTokensResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteIpamExternalResourceVerificationToken(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamExternalResourceVerificationTokenId")
	tok, ok := ec2IpamExtResTokens.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamExternalResourceVerificationTokenId.NotFound", fmt.Sprintf("The token ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	tok.State = "delete-complete"
	ec2IpamExtResTokens.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamExternalResourceVerificationTokenResponse %s>
  <requestId>%s</requestId>
  <ipamExternalResourceVerificationToken>%s</ipamExternalResourceVerificationToken>
</DeleteIpamExternalResourceVerificationTokenResponse>`, ec2Xmlns(), generateUUID(), ipamExtResTokenXML(tok))
}

// ---- Discovered resources ----
//
// The sim has no live cross-account discovery backend, so these return
// honest-empty result sets — exactly the shape a real IPAM resource discovery
// returns before it has scanned any monitored account.

func handleGetIpamDiscoveredAccounts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamDiscoveredAccountsResponse %s>
  <requestId>%s</requestId>
  <ipamDiscoveredAccountSet/>
</GetIpamDiscoveredAccountsResponse>`, ec2Xmlns(), generateUUID())
}

func handleGetIpamDiscoveredPublicAddresses(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamDiscoveredPublicAddressesResponse %s>
  <requestId>%s</requestId>
  <ipamDiscoveredPublicAddressSet/>
</GetIpamDiscoveredPublicAddressesResponse>`, ec2Xmlns(), generateUUID())
}

func handleGetIpamDiscoveredResourceCidrs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamDiscoveredResourceCidrsResponse %s>
  <requestId>%s</requestId>
  <ipamDiscoveredResourceCidrSet/>
</GetIpamDiscoveredResourceCidrsResponse>`, ec2Xmlns(), generateUUID())
}

// ---- Pool allocation mutation ----

func handleModifyIpamPoolAllocation(w http.ResponseWriter, r *http.Request) {
	allocID := r.FormValue("IpamPoolAllocationId")
	_, hasDesc := r.Form["Description"]
	desc := r.FormValue("Description")
	var found *EC2IpamPoolAllocation
	for _, pool := range ec2IpamPools.List() {
		for i := range pool.Allocations {
			if pool.Allocations[i].IpamPoolAllocationId == allocID {
				ec2IpamPools.Update(pool.IpamPoolId, func(p *EC2IpamPool) {
					for j := range p.Allocations {
						if p.Allocations[j].IpamPoolAllocationId == allocID && hasDesc {
							p.Allocations[j].Description = desc
						}
					}
				})
				updated, _ := ec2IpamPools.Get(pool.IpamPoolId)
				for j := range updated.Allocations {
					if updated.Allocations[j].IpamPoolAllocationId == allocID {
						cp := updated.Allocations[j]
						found = &cp
					}
				}
				break
			}
		}
		if found != nil {
			break
		}
	}
	if found == nil {
		ec2ErrorXML(w, "InvalidIpamPoolAllocationId.NotFound", fmt.Sprintf("The allocation ID '%s' does not exist", allocID), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamPoolAllocationResponse %s>
  <requestId>%s</requestId>
  <ipamPoolAllocation>%s</ipamPoolAllocation>
</ModifyIpamPoolAllocationResponse>`, ec2Xmlns(), generateUUID(), ipamPoolAllocationBodyXML(*found))
}
