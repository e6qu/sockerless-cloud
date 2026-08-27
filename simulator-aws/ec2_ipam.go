package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements faithful control-plane CRUD for Amazon EC2 IP Address
// Manager (IPAM). An IPAM owns a public and a private default scope (created
// with it) plus a default resource discovery and its association; scopes hold
// pools; pools hold provisioned CIDRs and carved allocations. Every resource is
// backed by a real SQLite-persisted store and rendered as the exact ec2Query XML
// the AWS SDK for Go v2 and the aws CLI deserialize (member casing taken from
// ec2.smithy.json).

type EC2IpamOperatingRegion struct {
	RegionName string
}

type EC2Ipam struct {
	IpamId                                string
	OwnerId                               string
	IpamArn                               string
	IpamRegion                            string
	PublicDefaultScopeId                  string
	PrivateDefaultScopeId                 string
	ScopeCount                            int
	Description                           string
	OperatingRegions                      []EC2IpamOperatingRegion
	State                                 string
	DefaultResourceDiscoveryId            string
	DefaultResourceDiscoveryAssociationId string
	ResourceDiscoveryAssociationCount     int
	Tier                                  string
	EnablePrivateGua                      bool
	Tags                                  []EC2Tag
}

type EC2IpamScope struct {
	IpamScopeId   string
	OwnerId       string
	IpamScopeArn  string
	IpamArn       string
	IpamRegion    string
	IpamScopeType string
	IsDefault     bool
	Description   string
	PoolCount     int
	State         string
	Tags          []EC2Tag
}

type EC2IpamPool struct {
	IpamPoolId                     string
	OwnerId                        string
	SourceIpamPoolId               string
	IpamPoolArn                    string
	IpamScopeArn                   string
	IpamScopeType                  string
	IpamScopeId                    string
	IpamArn                        string
	IpamId                         string
	IpamRegion                     string
	Locale                         string
	PoolDepth                      int
	State                          string
	StateMessage                   string
	Description                    string
	AutoImport                     bool
	PubliclyAdvertisable           bool
	AddressFamily                  string
	AllocationMinNetmaskLength     int
	AllocationMaxNetmaskLength     int
	AllocationDefaultNetmaskLength int
	AllocationResourceTags         []EC2Tag
	PublicIpSource                 string
	Tags                           []EC2Tag
	// Provisioned CIDRs and carved allocations live with the pool.
	Cidrs       []EC2IpamPoolCidr
	Allocations []EC2IpamPoolAllocation
}

type EC2IpamPoolCidr struct {
	Cidr           string
	State          string
	FailureReason  string
	IpamPoolCidrId string
	NetmaskLength  int
}

type EC2IpamPoolAllocation struct {
	Cidr                 string
	IpamPoolAllocationId string
	Description          string
	ResourceId           string
	ResourceType         string
	ResourceRegion       string
	ResourceOwner        string
}

type EC2IpamResourceDiscovery struct {
	IpamResourceDiscoveryId     string
	OwnerId                     string
	IpamResourceDiscoveryArn    string
	IpamResourceDiscoveryRegion string
	Description                 string
	OperatingRegions            []EC2IpamOperatingRegion
	IsDefault                   bool
	State                       string
	Tags                        []EC2Tag
}

type EC2IpamResourceDiscoveryAssociation struct {
	IpamResourceDiscoveryAssociationId  string
	OwnerId                             string
	IpamResourceDiscoveryAssociationArn string
	IpamResourceDiscoveryId             string
	IpamId                              string
	IpamArn                             string
	IpamRegion                          string
	IsDefault                           bool
	ResourceDiscoveryStatus             string
	State                               string
	Tags                                []EC2Tag
}

var (
	ec2Ipams                 sim.Store[EC2Ipam]
	ec2IpamScopes            sim.Store[EC2IpamScope]
	ec2IpamPools             sim.Store[EC2IpamPool]
	ec2IpamResourceDiscos    sim.Store[EC2IpamResourceDiscovery]
	ec2IpamResourceDiscoAsns sim.Store[EC2IpamResourceDiscoveryAssociation]
	// Delegated IPAM Organizations admin account, set by EnableIpamOrganizationAdminAccount.
	ec2IpamAdminAccounts sim.Store[ec2IpamAdminAccount]
)

type ec2IpamAdminAccount struct {
	AccountId string
}

func registerEC2IPAM(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2Ipams = sim.MakeStore[EC2Ipam](srv.DB(), "ec2_ipams")
	ec2IpamScopes = sim.MakeStore[EC2IpamScope](srv.DB(), "ec2_ipam_scopes")
	ec2IpamPools = sim.MakeStore[EC2IpamPool](srv.DB(), "ec2_ipam_pools")
	ec2IpamResourceDiscos = sim.MakeStore[EC2IpamResourceDiscovery](srv.DB(), "ec2_ipam_resource_discoveries")
	ec2IpamResourceDiscoAsns = sim.MakeStore[EC2IpamResourceDiscoveryAssociation](srv.DB(), "ec2_ipam_resource_discovery_associations")
	ec2IpamAdminAccounts = sim.MakeStore[ec2IpamAdminAccount](srv.DB(), "ec2_ipam_admin_accounts")

	// IPAM
	r.Register("CreateIpam", handleCreateIpam)
	r.Register("DescribeIpams", handleDescribeIpams)
	r.Register("ModifyIpam", handleModifyIpam)
	r.Register("DeleteIpam", handleDeleteIpam)

	// Scopes
	r.Register("CreateIpamScope", handleCreateIpamScope)
	r.Register("DescribeIpamScopes", handleDescribeIpamScopes)
	r.Register("ModifyIpamScope", handleModifyIpamScope)
	r.Register("DeleteIpamScope", handleDeleteIpamScope)

	// Pools
	r.Register("CreateIpamPool", handleCreateIpamPool)
	r.Register("DescribeIpamPools", handleDescribeIpamPools)
	r.Register("ModifyIpamPool", handleModifyIpamPool)
	r.Register("DeleteIpamPool", handleDeleteIpamPool)

	// Pool CIDRs + allocations
	r.Register("ProvisionIpamPoolCidr", handleProvisionIpamPoolCidr)
	r.Register("DeprovisionIpamPoolCidr", handleDeprovisionIpamPoolCidr)
	r.Register("GetIpamPoolCidrs", handleGetIpamPoolCidrs)
	r.Register("AllocateIpamPoolCidr", handleAllocateIpamPoolCidr)
	r.Register("ReleaseIpamPoolAllocation", handleReleaseIpamPoolAllocation)
	r.Register("GetIpamPoolAllocations", handleGetIpamPoolAllocations)
	r.Register("DescribeIpamPoolAllocations", handleDescribeIpamPoolAllocations)

	// Resource discoveries + associations
	r.Register("CreateIpamResourceDiscovery", handleCreateIpamResourceDiscovery)
	r.Register("DescribeIpamResourceDiscoveries", handleDescribeIpamResourceDiscoveries)
	r.Register("ModifyIpamResourceDiscovery", handleModifyIpamResourceDiscovery)
	r.Register("DeleteIpamResourceDiscovery", handleDeleteIpamResourceDiscovery)
	r.Register("AssociateIpamResourceDiscovery", handleAssociateIpamResourceDiscovery)
	r.Register("DisassociateIpamResourceDiscovery", handleDisassociateIpamResourceDiscovery)
	r.Register("DescribeIpamResourceDiscoveryAssociations", handleDescribeIpamResourceDiscoveryAssociations)

	// Resource CIDRs + address history
	r.Register("GetIpamResourceCidrs", handleGetIpamResourceCidrs)
	r.Register("GetIpamAddressHistory", handleGetIpamAddressHistory)
	r.Register("ModifyIpamResourceCidr", handleModifyIpamResourceCidr)

	// Organizations delegated admin
	r.Register("EnableIpamOrganizationAdminAccount", handleEnableIpamOrganizationAdminAccount)
	r.Register("DisableIpamOrganizationAdminAccount", handleDisableIpamOrganizationAdminAccount)
}

func ipamArn(resource string) string {
	return fmt.Sprintf("arn:aws:ec2::%s:%s", awsAccountID(), resource)
}

// ec2ParseOperatingRegions reads OperatingRegion.N.RegionName request params.
func ec2ParseOperatingRegions(r *http.Request, prefix string) []EC2IpamOperatingRegion {
	var regions []EC2IpamOperatingRegion
	for i := 1; ; i++ {
		name := r.FormValue(fmt.Sprintf("%s.%d.RegionName", prefix, i))
		if name == "" {
			break
		}
		regions = append(regions, EC2IpamOperatingRegion{RegionName: name})
	}
	return regions
}

func operatingRegionSetXML(regions []EC2IpamOperatingRegion) string {
	var b strings.Builder
	b.WriteString("<operatingRegionSet>")
	for _, reg := range regions {
		fmt.Fprintf(&b, "<item><regionName>%s</regionName></item>", reg.RegionName)
	}
	b.WriteString("</operatingRegionSet>")
	return b.String()
}

func handleCreateIpam(w http.ResponseWriter, r *http.Request) {
	region := awsRegion()
	regions := ec2ParseOperatingRegions(r, "OperatingRegion")
	if len(regions) == 0 {
		regions = []EC2IpamOperatingRegion{{RegionName: region}}
	}
	id := ec2ID("ipam")
	tier := r.FormValue("Tier")
	if tier == "" {
		tier = "advanced"
	}

	// An IPAM is created together with its public + private default scopes and a
	// default resource discovery + association, exactly like real AWS.
	pubScope := EC2IpamScope{
		IpamScopeId:   ec2ID("ipam-scope"),
		OwnerId:       ec2Owner(),
		IpamArn:       ipamArn("ipam/" + id),
		IpamRegion:    region,
		IpamScopeType: "public",
		IsDefault:     true,
		PoolCount:     0,
		State:         "create-complete",
	}
	pubScope.IpamScopeArn = ipamArn("ipam-scope/" + pubScope.IpamScopeId)
	privScope := pubScope
	privScope.IpamScopeId = ec2ID("ipam-scope")
	privScope.IpamScopeType = "private"
	privScope.IpamScopeArn = ipamArn("ipam-scope/" + privScope.IpamScopeId)

	rd := EC2IpamResourceDiscovery{
		IpamResourceDiscoveryId:     ec2ID("ipam-res-disco"),
		OwnerId:                     ec2Owner(),
		IpamResourceDiscoveryRegion: region,
		OperatingRegions:            regions,
		IsDefault:                   true,
		State:                       "create-complete",
	}
	rd.IpamResourceDiscoveryArn = ipamArn("ipam-resource-discovery/" + rd.IpamResourceDiscoveryId)

	rda := EC2IpamResourceDiscoveryAssociation{
		IpamResourceDiscoveryAssociationId: ec2ID("ipam-res-disco-assoc"),
		OwnerId:                            ec2Owner(),
		IpamResourceDiscoveryId:            rd.IpamResourceDiscoveryId,
		IpamId:                             id,
		IpamArn:                            ipamArn("ipam/" + id),
		IpamRegion:                         region,
		IsDefault:                          true,
		ResourceDiscoveryStatus:            "active",
		State:                              "associate-complete",
	}
	rda.IpamResourceDiscoveryAssociationArn = ipamArn("ipam-resource-discovery-association/" + rda.IpamResourceDiscoveryAssociationId)

	ipam := EC2Ipam{
		IpamId:                                id,
		OwnerId:                               ec2Owner(),
		IpamArn:                               ipamArn("ipam/" + id),
		IpamRegion:                            region,
		PublicDefaultScopeId:                  pubScope.IpamScopeId,
		PrivateDefaultScopeId:                 privScope.IpamScopeId,
		ScopeCount:                            2,
		Description:                           r.FormValue("Description"),
		OperatingRegions:                      regions,
		State:                                 "create-complete",
		DefaultResourceDiscoveryId:            rd.IpamResourceDiscoveryId,
		DefaultResourceDiscoveryAssociationId: rda.IpamResourceDiscoveryAssociationId,
		ResourceDiscoveryAssociationCount:     1,
		Tier:                                  tier,
		EnablePrivateGua:                      r.FormValue("EnablePrivateGua") == "true",
		Tags:                                  parseTags(r),
	}

	ec2IpamScopes.Put(pubScope.IpamScopeId, pubScope)
	ec2IpamScopes.Put(privScope.IpamScopeId, privScope)
	ec2IpamResourceDiscos.Put(rd.IpamResourceDiscoveryId, rd)
	ec2IpamResourceDiscoAsns.Put(rda.IpamResourceDiscoveryAssociationId, rda)
	ec2Ipams.Put(id, ipam)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamResponse %s>
  <requestId>%s</requestId>
  <ipam>%s</ipam>
</CreateIpamResponse>`, ec2Xmlns(), generateUUID(), ipamBodyXML(ipam))
}

func ipamBodyXML(ipam EC2Ipam) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ownerId>%s</ownerId><ipamId>%s</ipamId><ipamArn>%s</ipamArn><ipamRegion>%s</ipamRegion>",
		ipam.OwnerId, ipam.IpamId, ipam.IpamArn, ipam.IpamRegion)
	fmt.Fprintf(&b, "<publicDefaultScopeId>%s</publicDefaultScopeId><privateDefaultScopeId>%s</privateDefaultScopeId><scopeCount>%d</scopeCount>",
		ipam.PublicDefaultScopeId, ipam.PrivateDefaultScopeId, ipam.ScopeCount)
	if ipam.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", ipam.Description)
	}
	b.WriteString(operatingRegionSetXML(ipam.OperatingRegions))
	fmt.Fprintf(&b, "<state>%s</state>", ipam.State)
	fmt.Fprintf(&b, "<defaultResourceDiscoveryId>%s</defaultResourceDiscoveryId><defaultResourceDiscoveryAssociationId>%s</defaultResourceDiscoveryAssociationId><resourceDiscoveryAssociationCount>%d</resourceDiscoveryAssociationCount>",
		ipam.DefaultResourceDiscoveryId, ipam.DefaultResourceDiscoveryAssociationId, ipam.ResourceDiscoveryAssociationCount)
	fmt.Fprintf(&b, "<tier>%s</tier><enablePrivateGua>%t</enablePrivateGua>", ipam.Tier, ipam.EnablePrivateGua)
	b.WriteString(writeTagSetXML(ipam.Tags))
	return b.String()
}

func ipamMatchesFilters(ipam EC2Ipam, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "ipam-id":
			if !ec2StrInValues(ipam.IpamId, vals) {
				return false
			}
		case "ipam-region":
			if !ec2StrInValues(ipam.IpamRegion, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(ipam.State, vals) {
				return false
			}
		case "tier":
			if !ec2StrInValues(ipam.Tier, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, ipam.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDescribeIpams(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, ipam := range ec2Ipams.List() {
		if len(ids) > 0 && !ec2StrInValues(ipam.IpamId, ids) {
			continue
		}
		if !ipamMatchesFilters(ipam, filters) {
			continue
		}
		items.WriteString("<item>" + ipamBodyXML(ipam) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamsResponse %s>
  <requestId>%s</requestId>
  <ipamSet>%s</ipamSet>
</DescribeIpamsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyIpam(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamId")
	if _, ok := ec2Ipams.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	add := ec2ParseOperatingRegions(r, "AddOperatingRegion")
	remove := ec2ParseOperatingRegions(r, "RemoveOperatingRegion")
	ec2Ipams.Update(id, func(ipam *EC2Ipam) {
		if _, has := r.Form["Description"]; has {
			ipam.Description = r.FormValue("Description")
		}
		if tier := r.FormValue("Tier"); tier != "" {
			ipam.Tier = tier
		}
		if v := r.FormValue("EnablePrivateGua"); v != "" {
			ipam.EnablePrivateGua = v == "true"
		}
		if len(remove) > 0 {
			next := ipam.OperatingRegions[:0:0]
			for _, reg := range ipam.OperatingRegions {
				drop := false
				for _, rm := range remove {
					if rm.RegionName == reg.RegionName {
						drop = true
					}
				}
				if !drop {
					next = append(next, reg)
				}
			}
			ipam.OperatingRegions = next
		}
		ipam.OperatingRegions = append(ipam.OperatingRegions, add...)
	})
	ipam, _ := ec2Ipams.Get(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamResponse %s>
  <requestId>%s</requestId>
  <ipam>%s</ipam>
</ModifyIpamResponse>`, ec2Xmlns(), generateUUID(), ipamBodyXML(ipam))
}

func handleDeleteIpam(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamId")
	ipam, ok := ec2Ipams.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// Cascade: the IPAM's default scopes, default resource discovery, its
	// association, and any pools under its scopes go away with it.
	scopeIDs := map[string]bool{}
	for _, s := range ec2IpamScopes.List() {
		if s.IpamArn == ipam.IpamArn {
			scopeIDs[s.IpamScopeId] = true
			ec2IpamScopes.Delete(s.IpamScopeId)
		}
	}
	for _, p := range ec2IpamPools.List() {
		if scopeIDs[p.IpamScopeId] {
			ec2IpamPools.Delete(p.IpamPoolId)
		}
	}
	for _, rda := range ec2IpamResourceDiscoAsns.List() {
		if rda.IpamId == id {
			ec2IpamResourceDiscoAsns.Delete(rda.IpamResourceDiscoveryAssociationId)
		}
	}
	ec2IpamResourceDiscos.Delete(ipam.DefaultResourceDiscoveryId)

	ipam.State = "delete-complete"
	ec2Ipams.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamResponse %s>
  <requestId>%s</requestId>
  <ipam>%s</ipam>
</DeleteIpamResponse>`, ec2Xmlns(), generateUUID(), ipamBodyXML(ipam))
}

func handleCreateIpamScope(w http.ResponseWriter, r *http.Request) {
	ipamID := r.FormValue("IpamId")
	ipam, ok := ec2Ipams.Get(ipamID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", ipamID), http.StatusBadRequest)
		return
	}
	id := ec2ID("ipam-scope")
	scope := EC2IpamScope{
		IpamScopeId:   id,
		OwnerId:       ec2Owner(),
		IpamScopeArn:  ipamArn("ipam-scope/" + id),
		IpamArn:       ipam.IpamArn,
		IpamRegion:    ipam.IpamRegion,
		IpamScopeType: "private",
		IsDefault:     false,
		Description:   r.FormValue("Description"),
		PoolCount:     0,
		State:         "create-complete",
		Tags:          parseTags(r),
	}
	ec2IpamScopes.Put(id, scope)
	ec2Ipams.Update(ipamID, func(i *EC2Ipam) { i.ScopeCount++ })
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamScopeResponse %s>
  <requestId>%s</requestId>
  <ipamScope>%s</ipamScope>
</CreateIpamScopeResponse>`, ec2Xmlns(), generateUUID(), ipamScopeBodyXML(scope))
}

func ipamScopeBodyXML(s EC2IpamScope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ownerId>%s</ownerId><ipamScopeId>%s</ipamScopeId><ipamScopeArn>%s</ipamScopeArn><ipamArn>%s</ipamArn><ipamRegion>%s</ipamRegion>",
		s.OwnerId, s.IpamScopeId, s.IpamScopeArn, s.IpamArn, s.IpamRegion)
	fmt.Fprintf(&b, "<ipamScopeType>%s</ipamScopeType><isDefault>%t</isDefault>", s.IpamScopeType, s.IsDefault)
	if s.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", s.Description)
	}
	fmt.Fprintf(&b, "<poolCount>%d</poolCount><state>%s</state>", s.PoolCount, s.State)
	b.WriteString(writeTagSetXML(s.Tags))
	return b.String()
}

func ipamScopeMatchesFilters(s EC2IpamScope, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "ipam-scope-id":
			if !ec2StrInValues(s.IpamScopeId, vals) {
				return false
			}
		case "ipam-arn":
			if !ec2StrInValues(s.IpamArn, vals) {
				return false
			}
		case "ipam-scope-type":
			if !ec2StrInValues(s.IpamScopeType, vals) {
				return false
			}
		case "is-default":
			if s.IsDefault != ec2StrInValues("true", vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, s.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDescribeIpamScopes(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamScopeId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, s := range ec2IpamScopes.List() {
		if len(ids) > 0 && !ec2StrInValues(s.IpamScopeId, ids) {
			continue
		}
		if !ipamScopeMatchesFilters(s, filters) {
			continue
		}
		items.WriteString("<item>" + ipamScopeBodyXML(s) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamScopesResponse %s>
  <requestId>%s</requestId>
  <ipamScopeSet>%s</ipamScopeSet>
</DescribeIpamScopesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyIpamScope(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamScopeId")
	if _, ok := ec2IpamScopes.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamScopeId.NotFound", fmt.Sprintf("The ipam scope ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2IpamScopes.Update(id, func(s *EC2IpamScope) {
		if _, has := r.Form["Description"]; has {
			s.Description = r.FormValue("Description")
		}
	})
	s, _ := ec2IpamScopes.Get(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamScopeResponse %s>
  <requestId>%s</requestId>
  <ipamScope>%s</ipamScope>
</ModifyIpamScopeResponse>`, ec2Xmlns(), generateUUID(), ipamScopeBodyXML(s))
}

func handleDeleteIpamScope(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamScopeId")
	s, ok := ec2IpamScopes.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamScopeId.NotFound", fmt.Sprintf("The ipam scope ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if s.IsDefault {
		ec2ErrorXML(w, "IpamScopeCannotDelete", "A default IPAM scope cannot be deleted.", http.StatusBadRequest)
		return
	}
	for _, p := range ec2IpamPools.List() {
		if p.IpamScopeId == id {
			ec2IpamPools.Delete(p.IpamPoolId)
		}
	}
	s.State = "delete-complete"
	ec2IpamScopes.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamScopeResponse %s>
  <requestId>%s</requestId>
  <ipamScope>%s</ipamScope>
</DeleteIpamScopeResponse>`, ec2Xmlns(), generateUUID(), ipamScopeBodyXML(s))
}

func handleCreateIpamPool(w http.ResponseWriter, r *http.Request) {
	scopeID := r.FormValue("IpamScopeId")
	scope, ok := ec2IpamScopes.Get(scopeID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamScopeId.NotFound", fmt.Sprintf("The ipam scope ID '%s' does not exist", scopeID), http.StatusBadRequest)
		return
	}
	addrFamily := r.FormValue("AddressFamily")
	if addrFamily == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter addressFamily", http.StatusBadRequest)
		return
	}
	// Resolve the owning IPAM from the scope's ARN (arn:...:ipam/ipam-xxxx).
	ipamID := strings.TrimPrefix(scope.IpamArn[strings.LastIndex(scope.IpamArn, ":")+1:], "ipam/")
	id := ec2ID("ipam-pool")
	pool := EC2IpamPool{
		IpamPoolId:                     id,
		OwnerId:                        ec2Owner(),
		SourceIpamPoolId:               r.FormValue("SourceIpamPoolId"),
		IpamPoolArn:                    ipamArn("ipam-pool/" + id),
		IpamScopeArn:                   scope.IpamScopeArn,
		IpamScopeType:                  scope.IpamScopeType,
		IpamScopeId:                    scopeID,
		IpamArn:                        scope.IpamArn,
		IpamId:                         ipamID,
		IpamRegion:                     scope.IpamRegion,
		Locale:                         r.FormValue("Locale"),
		PoolDepth:                      1,
		State:                          "create-complete",
		Description:                    r.FormValue("Description"),
		AutoImport:                     r.FormValue("AutoImport") == "true",
		PubliclyAdvertisable:           r.FormValue("PubliclyAdvertisable") == "true",
		AddressFamily:                  addrFamily,
		AllocationMinNetmaskLength:     atoiOr(r.FormValue("AllocationMinNetmaskLength"), 0),
		AllocationMaxNetmaskLength:     atoiOr(r.FormValue("AllocationMaxNetmaskLength"), 0),
		AllocationDefaultNetmaskLength: atoiOr(r.FormValue("AllocationDefaultNetmaskLength"), 0),
		AllocationResourceTags:         parseAllocationResourceTags(r, "AllocationResourceTag"),
		PublicIpSource:                 r.FormValue("PublicIpSource"),
		Tags:                           parseTags(r),
	}
	if pool.SourceIpamPoolId != "" {
		pool.PoolDepth = 2
	}
	ec2IpamPools.Put(id, pool)
	ec2IpamScopes.Update(scopeID, func(s *EC2IpamScope) { s.PoolCount++ })
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamPoolResponse %s>
  <requestId>%s</requestId>
  <ipamPool>%s</ipamPool>
</CreateIpamPoolResponse>`, ec2Xmlns(), generateUUID(), ipamPoolBodyXML(pool))
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

// parseAllocationResourceTags reads AllocationResourceTag.N.Key/Value (the IPAM
// pool's allocation-resource-tag list, a flat key/value list, not a tag spec).
func parseAllocationResourceTags(r *http.Request, prefix string) []EC2Tag {
	var tags []EC2Tag
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("%s.%d.Key", prefix, i))
		if key == "" {
			break
		}
		tags = append(tags, EC2Tag{Key: key, Value: r.FormValue(fmt.Sprintf("%s.%d.Value", prefix, i))})
	}
	return tags
}

func resourceTagSetXML(elem string, tags []EC2Tag) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", elem)
	for _, t := range tags {
		fmt.Fprintf(&b, "<item><key>%s</key><value>%s</value></item>", t.Key, t.Value)
	}
	fmt.Fprintf(&b, "</%s>", elem)
	return b.String()
}

func ipamPoolBodyXML(p EC2IpamPool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ownerId>%s</ownerId><ipamPoolId>%s</ipamPoolId>", p.OwnerId, p.IpamPoolId)
	if p.SourceIpamPoolId != "" {
		fmt.Fprintf(&b, "<sourceIpamPoolId>%s</sourceIpamPoolId>", p.SourceIpamPoolId)
	}
	fmt.Fprintf(&b, "<ipamPoolArn>%s</ipamPoolArn><ipamScopeArn>%s</ipamScopeArn><ipamScopeType>%s</ipamScopeType><ipamArn>%s</ipamArn><ipamRegion>%s</ipamRegion>",
		p.IpamPoolArn, p.IpamScopeArn, p.IpamScopeType, p.IpamArn, p.IpamRegion)
	if p.Locale != "" {
		fmt.Fprintf(&b, "<locale>%s</locale>", p.Locale)
	}
	fmt.Fprintf(&b, "<poolDepth>%d</poolDepth><state>%s</state>", p.PoolDepth, p.State)
	if p.StateMessage != "" {
		fmt.Fprintf(&b, "<stateMessage>%s</stateMessage>", p.StateMessage)
	}
	if p.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", p.Description)
	}
	fmt.Fprintf(&b, "<autoImport>%t</autoImport>", p.AutoImport)
	if p.IpamScopeType == "public" {
		fmt.Fprintf(&b, "<publiclyAdvertisable>%t</publiclyAdvertisable>", p.PubliclyAdvertisable)
	}
	fmt.Fprintf(&b, "<addressFamily>%s</addressFamily>", p.AddressFamily)
	if p.AllocationMinNetmaskLength != 0 {
		fmt.Fprintf(&b, "<allocationMinNetmaskLength>%d</allocationMinNetmaskLength>", p.AllocationMinNetmaskLength)
	}
	if p.AllocationMaxNetmaskLength != 0 {
		fmt.Fprintf(&b, "<allocationMaxNetmaskLength>%d</allocationMaxNetmaskLength>", p.AllocationMaxNetmaskLength)
	}
	if p.AllocationDefaultNetmaskLength != 0 {
		fmt.Fprintf(&b, "<allocationDefaultNetmaskLength>%d</allocationDefaultNetmaskLength>", p.AllocationDefaultNetmaskLength)
	}
	if len(p.AllocationResourceTags) > 0 {
		b.WriteString(resourceTagSetXML("allocationResourceTagSet", p.AllocationResourceTags))
	}
	b.WriteString(writeTagSetXML(p.Tags))
	if p.PublicIpSource != "" {
		fmt.Fprintf(&b, "<publicIpSource>%s</publicIpSource>", p.PublicIpSource)
	}
	return b.String()
}

func ipamPoolMatchesFilters(p EC2IpamPool, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "ipam-pool-id":
			if !ec2StrInValues(p.IpamPoolId, vals) {
				return false
			}
		case "ipam-scope-id":
			if !ec2StrInValues(p.IpamScopeId, vals) {
				return false
			}
		case "ipam-scope-type":
			if !ec2StrInValues(p.IpamScopeType, vals) {
				return false
			}
		case "address-family":
			if !ec2StrInValues(p.AddressFamily, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(p.State, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, p.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDescribeIpamPools(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamPoolId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, p := range ec2IpamPools.List() {
		if len(ids) > 0 && !ec2StrInValues(p.IpamPoolId, ids) {
			continue
		}
		if !ipamPoolMatchesFilters(p, filters) {
			continue
		}
		items.WriteString("<item>" + ipamPoolBodyXML(p) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamPoolsResponse %s>
  <requestId>%s</requestId>
  <ipamPoolSet>%s</ipamPoolSet>
</DescribeIpamPoolsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyIpamPool(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPoolId")
	if _, ok := ec2IpamPools.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	add := parseAllocationResourceTags(r, "AddAllocationResourceTag")
	remove := parseAllocationResourceTags(r, "RemoveAllocationResourceTag")
	ec2IpamPools.Update(id, func(p *EC2IpamPool) {
		if _, has := r.Form["Description"]; has {
			p.Description = r.FormValue("Description")
		}
		if v := r.FormValue("AutoImport"); v != "" {
			p.AutoImport = v == "true"
		}
		if v := r.FormValue("AllocationMinNetmaskLength"); v != "" {
			p.AllocationMinNetmaskLength = atoiOr(v, p.AllocationMinNetmaskLength)
		}
		if v := r.FormValue("AllocationMaxNetmaskLength"); v != "" {
			p.AllocationMaxNetmaskLength = atoiOr(v, p.AllocationMaxNetmaskLength)
		}
		if r.FormValue("ClearAllocationDefaultNetmaskLength") == "true" {
			p.AllocationDefaultNetmaskLength = 0
		} else if v := r.FormValue("AllocationDefaultNetmaskLength"); v != "" {
			p.AllocationDefaultNetmaskLength = atoiOr(v, p.AllocationDefaultNetmaskLength)
		}
		if len(remove) > 0 {
			next := p.AllocationResourceTags[:0:0]
			for _, t := range p.AllocationResourceTags {
				drop := false
				for _, rm := range remove {
					if rm.Key == t.Key {
						drop = true
					}
				}
				if !drop {
					next = append(next, t)
				}
			}
			p.AllocationResourceTags = next
		}
		p.AllocationResourceTags = append(p.AllocationResourceTags, add...)
	})
	p, _ := ec2IpamPools.Get(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamPoolResponse %s>
  <requestId>%s</requestId>
  <ipamPool>%s</ipamPool>
</ModifyIpamPoolResponse>`, ec2Xmlns(), generateUUID(), ipamPoolBodyXML(p))
}

func handleDeleteIpamPool(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamPoolId")
	p, ok := ec2IpamPools.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if len(p.Allocations) > 0 && r.FormValue("Cascade") != "true" {
		ec2ErrorXML(w, "IpamPoolHasAllocations", "The IPAM pool has allocations; deprovision its CIDRs or delete with cascade.", http.StatusBadRequest)
		return
	}
	ec2IpamScopes.Update(p.IpamScopeId, func(s *EC2IpamScope) {
		if s.PoolCount > 0 {
			s.PoolCount--
		}
	})
	p.State = "delete-complete"
	ec2IpamPools.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamPoolResponse %s>
  <requestId>%s</requestId>
  <ipamPool>%s</ipamPool>
</DeleteIpamPoolResponse>`, ec2Xmlns(), generateUUID(), ipamPoolBodyXML(p))
}

func ipamPoolCidrBodyXML(c EC2IpamPoolCidr) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<cidr>%s</cidr><state>%s</state>", c.Cidr, c.State)
	if c.FailureReason != "" {
		fmt.Fprintf(&b, "<failureReason>%s</failureReason>", c.FailureReason)
	}
	if c.IpamPoolCidrId != "" {
		fmt.Fprintf(&b, "<ipamPoolCidrId>%s</ipamPoolCidrId>", c.IpamPoolCidrId)
	}
	if c.NetmaskLength != 0 {
		fmt.Fprintf(&b, "<netmaskLength>%d</netmaskLength>", c.NetmaskLength)
	}
	return b.String()
}

func handleProvisionIpamPoolCidr(w http.ResponseWriter, r *http.Request) {
	poolID := r.FormValue("IpamPoolId")
	if _, ok := ec2IpamPools.Get(poolID); !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", poolID), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("Cidr")
	netmask := atoiOr(r.FormValue("NetmaskLength"), 0)
	if cidr == "" && netmask == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain either cidr or netmaskLength", http.StatusBadRequest)
		return
	}
	if cidr != "" {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Invalid CIDR: %s", cidr), http.StatusBadRequest)
			return
		}
	}
	c := EC2IpamPoolCidr{
		Cidr:           cidr,
		State:          "provisioned",
		IpamPoolCidrId: ec2ID("ipam-pool-cidr"),
		NetmaskLength:  netmask,
	}
	ec2IpamPools.Update(poolID, func(p *EC2IpamPool) {
		next := append([]EC2IpamPoolCidr{}, p.Cidrs...)
		next = append(next, c)
		p.Cidrs = next
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ProvisionIpamPoolCidrResponse %s>
  <requestId>%s</requestId>
  <ipamPoolCidr>%s</ipamPoolCidr>
</ProvisionIpamPoolCidrResponse>`, ec2Xmlns(), generateUUID(), ipamPoolCidrBodyXML(c))
}

func handleDeprovisionIpamPoolCidr(w http.ResponseWriter, r *http.Request) {
	poolID := r.FormValue("IpamPoolId")
	pool, ok := ec2IpamPools.Get(poolID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", poolID), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("Cidr")
	var found *EC2IpamPoolCidr
	for i := range pool.Cidrs {
		if cidr == "" || pool.Cidrs[i].Cidr == cidr {
			c := pool.Cidrs[i]
			found = &c
			if cidr != "" {
				break
			}
		}
	}
	if found == nil {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("CIDR %s is not provisioned in pool %s", cidr, poolID), http.StatusBadRequest)
		return
	}
	found.State = "deprovisioned"
	ec2IpamPools.Update(poolID, func(p *EC2IpamPool) {
		next := p.Cidrs[:0:0]
		for _, c := range p.Cidrs {
			if cidr != "" && c.Cidr != cidr {
				next = append(next, c)
			}
		}
		p.Cidrs = next
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeprovisionIpamPoolCidrResponse %s>
  <requestId>%s</requestId>
  <ipamPoolCidr>%s</ipamPoolCidr>
</DeprovisionIpamPoolCidrResponse>`, ec2Xmlns(), generateUUID(), ipamPoolCidrBodyXML(*found))
}

func handleGetIpamPoolCidrs(w http.ResponseWriter, r *http.Request) {
	poolID := r.FormValue("IpamPoolId")
	pool, ok := ec2IpamPools.Get(poolID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", poolID), http.StatusBadRequest)
		return
	}
	filters := ec2Filters(r)
	var items strings.Builder
	for _, c := range pool.Cidrs {
		if !ipamPoolCidrMatchesFilters(c, filters) {
			continue
		}
		items.WriteString("<item>" + ipamPoolCidrBodyXML(c) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamPoolCidrsResponse %s>
  <requestId>%s</requestId>
  <ipamPoolCidrSet>%s</ipamPoolCidrSet>
</GetIpamPoolCidrsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func ipamPoolCidrMatchesFilters(c EC2IpamPoolCidr, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "cidr":
			if !ec2StrInValues(c.Cidr, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(c.State, vals) {
				return false
			}
		}
	}
	return true
}

func ipamPoolAllocationBodyXML(a EC2IpamPoolAllocation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<cidr>%s</cidr><ipamPoolAllocationId>%s</ipamPoolAllocationId>", a.Cidr, a.IpamPoolAllocationId)
	if a.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", a.Description)
	}
	if a.ResourceId != "" {
		fmt.Fprintf(&b, "<resourceId>%s</resourceId>", a.ResourceId)
	}
	if a.ResourceType != "" {
		fmt.Fprintf(&b, "<resourceType>%s</resourceType>", a.ResourceType)
	}
	if a.ResourceRegion != "" {
		fmt.Fprintf(&b, "<resourceRegion>%s</resourceRegion>", a.ResourceRegion)
	}
	if a.ResourceOwner != "" {
		fmt.Fprintf(&b, "<resourceOwner>%s</resourceOwner>", a.ResourceOwner)
	}
	return b.String()
}

// ipamCarveCidr carves the next free sub-CIDR of the given netmask length out of
// the pool's provisioned CIDRs, avoiding existing allocations. Returns "" if it
// can't fit. Faithful to how IPAM hands out non-overlapping sub-ranges.
func ipamCarveCidr(pool EC2IpamPool, netmask int) string {
	for _, prov := range pool.Cidrs {
		base := prov.Cidr
		if base == "" {
			continue
		}
		_, baseNet, err := net.ParseCIDR(base)
		if err != nil || baseNet.IP.To4() == nil {
			continue
		}
		parentLen, _ := baseNet.Mask.Size()
		if netmask < parentLen {
			continue
		}
		start := binary.BigEndian.Uint32(baseNet.IP.To4())
		blockSize := uint32(1) << (32 - netmask)
		parentSize := uint32(1) << (32 - parentLen)
		for off := uint32(0); off < parentSize; off += blockSize {
			candIP := make(net.IP, 4)
			binary.BigEndian.PutUint32(candIP, start+off)
			cand := fmt.Sprintf("%s/%d", candIP.String(), netmask)
			if !ipamCidrTaken(pool, cand) {
				return cand
			}
		}
	}
	return ""
}

func ipamCidrTaken(pool EC2IpamPool, cidr string) bool {
	_, candNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return true
	}
	for _, a := range pool.Allocations {
		_, allocNet, err := net.ParseCIDR(a.Cidr)
		if err != nil {
			continue
		}
		if candNet.Contains(allocNet.IP) || allocNet.Contains(candNet.IP) {
			return true
		}
	}
	return false
}

func handleAllocateIpamPoolCidr(w http.ResponseWriter, r *http.Request) {
	poolID := r.FormValue("IpamPoolId")
	pool, ok := ec2IpamPools.Get(poolID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", poolID), http.StatusBadRequest)
		return
	}
	cidr := r.FormValue("Cidr")
	netmask := atoiOr(r.FormValue("NetmaskLength"), 0)
	if cidr == "" {
		if netmask == 0 {
			ec2ErrorXML(w, "MissingParameter", "The request must contain either cidr or netmaskLength", http.StatusBadRequest)
			return
		}
		cidr = ipamCarveCidr(pool, netmask)
		if cidr == "" {
			ec2ErrorXML(w, "IpamAllocationNotFound", "No available CIDR of the requested size in the pool.", http.StatusBadRequest)
			return
		}
	} else if ipamCidrTaken(pool, cidr) {
		ec2ErrorXML(w, "IpamCidrAllocationConflict", fmt.Sprintf("CIDR %s overlaps an existing allocation.", cidr), http.StatusBadRequest)
		return
	}

	preview := r.FormValue("PreviewNextCidr") == "true"
	alloc := EC2IpamPoolAllocation{
		Cidr:                 cidr,
		IpamPoolAllocationId: ec2ID("ipam-pool-alloc"),
		Description:          r.FormValue("Description"),
		ResourceType:         "custom",
		ResourceOwner:        ec2Owner(),
		ResourceRegion:       pool.IpamRegion,
	}
	if !preview {
		ec2IpamPools.Update(poolID, func(p *EC2IpamPool) {
			next := append([]EC2IpamPoolAllocation{}, p.Allocations...)
			next = append(next, alloc)
			p.Allocations = next
		})
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AllocateIpamPoolCidrResponse %s>
  <requestId>%s</requestId>
  <ipamPoolAllocation>%s</ipamPoolAllocation>
</AllocateIpamPoolCidrResponse>`, ec2Xmlns(), generateUUID(), ipamPoolAllocationBodyXML(alloc))
}

func handleReleaseIpamPoolAllocation(w http.ResponseWriter, r *http.Request) {
	poolID := r.FormValue("IpamPoolId")
	allocID := r.FormValue("IpamPoolAllocationId")
	pool, ok := ec2IpamPools.Get(poolID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", poolID), http.StatusBadRequest)
		return
	}
	found := false
	for _, a := range pool.Allocations {
		if a.IpamPoolAllocationId == allocID {
			found = true
		}
	}
	if !found {
		ec2ErrorXML(w, "IpamAllocationNotFound", fmt.Sprintf("The allocation '%s' does not exist in pool %s", allocID, poolID), http.StatusBadRequest)
		return
	}
	ec2IpamPools.Update(poolID, func(p *EC2IpamPool) {
		next := p.Allocations[:0:0]
		for _, a := range p.Allocations {
			if a.IpamPoolAllocationId != allocID {
				next = append(next, a)
			}
		}
		p.Allocations = next
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReleaseIpamPoolAllocationResponse %s>
  <requestId>%s</requestId>
  <success>true</success>
</ReleaseIpamPoolAllocationResponse>`, ec2Xmlns(), generateUUID())
}

func ipamPoolAllocationsXML(allocs []EC2IpamPoolAllocation, allocID string, filters map[string][]string) string {
	var items strings.Builder
	for _, a := range allocs {
		if allocID != "" && a.IpamPoolAllocationId != allocID {
			continue
		}
		if !ipamAllocationMatchesFilters(a, filters) {
			continue
		}
		items.WriteString("<item>" + ipamPoolAllocationBodyXML(a) + "</item>")
	}
	return items.String()
}

func ipamAllocationMatchesFilters(a EC2IpamPoolAllocation, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "cidr":
			if !ec2StrInValues(a.Cidr, vals) {
				return false
			}
		case "ipam-pool-allocation-id":
			if !ec2StrInValues(a.IpamPoolAllocationId, vals) {
				return false
			}
		case "resource-id":
			if !ec2StrInValues(a.ResourceId, vals) {
				return false
			}
		case "resource-type":
			if !ec2StrInValues(a.ResourceType, vals) {
				return false
			}
		}
	}
	return true
}

func handleGetIpamPoolAllocations(w http.ResponseWriter, r *http.Request) {
	poolID := r.FormValue("IpamPoolId")
	pool, ok := ec2IpamPools.Get(poolID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamPoolId.NotFound", fmt.Sprintf("The ipam pool ID '%s' does not exist", poolID), http.StatusBadRequest)
		return
	}
	items := ipamPoolAllocationsXML(pool.Allocations, r.FormValue("IpamPoolAllocationId"), ec2Filters(r))
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamPoolAllocationsResponse %s>
  <requestId>%s</requestId>
  <ipamPoolAllocationSet>%s</ipamPoolAllocationSet>
</GetIpamPoolAllocationsResponse>`, ec2Xmlns(), generateUUID(), items)
}

func handleDescribeIpamPoolAllocations(w http.ResponseWriter, r *http.Request) {
	// DescribeIpamPoolAllocations selects by allocation IDs (or filters) across
	// every pool — it carries no IpamPoolId, unlike GetIpamPoolAllocations.
	wantIDs := ec2ParamList(r, "IpamPoolAllocationId")
	filters := ec2Filters(r)
	var all []EC2IpamPoolAllocation
	for _, p := range ec2IpamPools.List() {
		all = append(all, p.Allocations...)
	}
	if len(wantIDs) > 0 {
		kept := all[:0:0]
		for _, a := range all {
			if ec2StrInValues(a.IpamPoolAllocationId, wantIDs) {
				kept = append(kept, a)
			}
		}
		all = kept
	}
	items := ipamPoolAllocationsXML(all, "", filters)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamPoolAllocationsResponse %s>
  <requestId>%s</requestId>
  <ipamPoolAllocationSet>%s</ipamPoolAllocationSet>
</DescribeIpamPoolAllocationsResponse>`, ec2Xmlns(), generateUUID(), items)
}

func handleCreateIpamResourceDiscovery(w http.ResponseWriter, r *http.Request) {
	region := awsRegion()
	regions := ec2ParseOperatingRegions(r, "OperatingRegion")
	if len(regions) == 0 {
		regions = []EC2IpamOperatingRegion{{RegionName: region}}
	}
	id := ec2ID("ipam-res-disco")
	rd := EC2IpamResourceDiscovery{
		IpamResourceDiscoveryId:     id,
		OwnerId:                     ec2Owner(),
		IpamResourceDiscoveryArn:    ipamArn("ipam-resource-discovery/" + id),
		IpamResourceDiscoveryRegion: region,
		Description:                 r.FormValue("Description"),
		OperatingRegions:            regions,
		IsDefault:                   false,
		State:                       "create-complete",
		Tags:                        parseTags(r),
	}
	ec2IpamResourceDiscos.Put(id, rd)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateIpamResourceDiscoveryResponse %s>
  <requestId>%s</requestId>
  <ipamResourceDiscovery>%s</ipamResourceDiscovery>
</CreateIpamResourceDiscoveryResponse>`, ec2Xmlns(), generateUUID(), ipamResourceDiscoveryBodyXML(rd))
}

func ipamResourceDiscoveryBodyXML(rd EC2IpamResourceDiscovery) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ownerId>%s</ownerId><ipamResourceDiscoveryId>%s</ipamResourceDiscoveryId><ipamResourceDiscoveryArn>%s</ipamResourceDiscoveryArn><ipamResourceDiscoveryRegion>%s</ipamResourceDiscoveryRegion>",
		rd.OwnerId, rd.IpamResourceDiscoveryId, rd.IpamResourceDiscoveryArn, rd.IpamResourceDiscoveryRegion)
	if rd.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", rd.Description)
	}
	b.WriteString(operatingRegionSetXML(rd.OperatingRegions))
	fmt.Fprintf(&b, "<isDefault>%t</isDefault><state>%s</state>", rd.IsDefault, rd.State)
	b.WriteString(writeTagSetXML(rd.Tags))
	return b.String()
}

func ipamResourceDiscoveryMatchesFilters(rd EC2IpamResourceDiscovery, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "ipam-resource-discovery-id":
			if !ec2StrInValues(rd.IpamResourceDiscoveryId, vals) {
				return false
			}
		case "ipam-resource-discovery-region":
			if !ec2StrInValues(rd.IpamResourceDiscoveryRegion, vals) {
				return false
			}
		case "is-default":
			if rd.IsDefault != ec2StrInValues("true", vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(rd.State, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, rd.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDescribeIpamResourceDiscoveries(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamResourceDiscoveryId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, rd := range ec2IpamResourceDiscos.List() {
		if len(ids) > 0 && !ec2StrInValues(rd.IpamResourceDiscoveryId, ids) {
			continue
		}
		if !ipamResourceDiscoveryMatchesFilters(rd, filters) {
			continue
		}
		items.WriteString("<item>" + ipamResourceDiscoveryBodyXML(rd) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamResourceDiscoveriesResponse %s>
  <requestId>%s</requestId>
  <ipamResourceDiscoverySet>%s</ipamResourceDiscoverySet>
</DescribeIpamResourceDiscoveriesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyIpamResourceDiscovery(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamResourceDiscoveryId")
	if _, ok := ec2IpamResourceDiscos.Get(id); !ok {
		ec2ErrorXML(w, "InvalidIpamResourceDiscoveryId.NotFound", fmt.Sprintf("The ipam resource discovery ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	add := ec2ParseOperatingRegions(r, "AddOperatingRegion")
	remove := ec2ParseOperatingRegions(r, "RemoveOperatingRegion")
	ec2IpamResourceDiscos.Update(id, func(rd *EC2IpamResourceDiscovery) {
		if _, has := r.Form["Description"]; has {
			rd.Description = r.FormValue("Description")
		}
		if len(remove) > 0 {
			next := rd.OperatingRegions[:0:0]
			for _, reg := range rd.OperatingRegions {
				drop := false
				for _, rm := range remove {
					if rm.RegionName == reg.RegionName {
						drop = true
					}
				}
				if !drop {
					next = append(next, reg)
				}
			}
			rd.OperatingRegions = next
		}
		rd.OperatingRegions = append(rd.OperatingRegions, add...)
	})
	rd, _ := ec2IpamResourceDiscos.Get(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamResourceDiscoveryResponse %s>
  <requestId>%s</requestId>
  <ipamResourceDiscovery>%s</ipamResourceDiscovery>
</ModifyIpamResourceDiscoveryResponse>`, ec2Xmlns(), generateUUID(), ipamResourceDiscoveryBodyXML(rd))
}

func handleDeleteIpamResourceDiscovery(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamResourceDiscoveryId")
	rd, ok := ec2IpamResourceDiscos.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamResourceDiscoveryId.NotFound", fmt.Sprintf("The ipam resource discovery ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if rd.IsDefault {
		ec2ErrorXML(w, "IpamResourceDiscoveryCannotDelete", "A default IPAM resource discovery cannot be deleted.", http.StatusBadRequest)
		return
	}
	rd.State = "delete-complete"
	ec2IpamResourceDiscos.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteIpamResourceDiscoveryResponse %s>
  <requestId>%s</requestId>
  <ipamResourceDiscovery>%s</ipamResourceDiscovery>
</DeleteIpamResourceDiscoveryResponse>`, ec2Xmlns(), generateUUID(), ipamResourceDiscoveryBodyXML(rd))
}

func ipamResourceDiscoveryAssociationBodyXML(a EC2IpamResourceDiscoveryAssociation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ownerId>%s</ownerId><ipamResourceDiscoveryAssociationId>%s</ipamResourceDiscoveryAssociationId><ipamResourceDiscoveryAssociationArn>%s</ipamResourceDiscoveryAssociationArn>",
		a.OwnerId, a.IpamResourceDiscoveryAssociationId, a.IpamResourceDiscoveryAssociationArn)
	fmt.Fprintf(&b, "<ipamResourceDiscoveryId>%s</ipamResourceDiscoveryId><ipamId>%s</ipamId><ipamArn>%s</ipamArn><ipamRegion>%s</ipamRegion>",
		a.IpamResourceDiscoveryId, a.IpamId, a.IpamArn, a.IpamRegion)
	fmt.Fprintf(&b, "<isDefault>%t</isDefault><resourceDiscoveryStatus>%s</resourceDiscoveryStatus><state>%s</state>",
		a.IsDefault, a.ResourceDiscoveryStatus, a.State)
	b.WriteString(writeTagSetXML(a.Tags))
	return b.String()
}

func handleAssociateIpamResourceDiscovery(w http.ResponseWriter, r *http.Request) {
	ipamID := r.FormValue("IpamId")
	rdID := r.FormValue("IpamResourceDiscoveryId")
	ipam, ok := ec2Ipams.Get(ipamID)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamId.NotFound", fmt.Sprintf("The ipam ID '%s' does not exist", ipamID), http.StatusBadRequest)
		return
	}
	if _, ok := ec2IpamResourceDiscos.Get(rdID); !ok {
		ec2ErrorXML(w, "InvalidIpamResourceDiscoveryId.NotFound", fmt.Sprintf("The ipam resource discovery ID '%s' does not exist", rdID), http.StatusBadRequest)
		return
	}
	id := ec2ID("ipam-res-disco-assoc")
	a := EC2IpamResourceDiscoveryAssociation{
		IpamResourceDiscoveryAssociationId:  id,
		OwnerId:                             ec2Owner(),
		IpamResourceDiscoveryAssociationArn: ipamArn("ipam-resource-discovery-association/" + id),
		IpamResourceDiscoveryId:             rdID,
		IpamId:                              ipamID,
		IpamArn:                             ipam.IpamArn,
		IpamRegion:                          ipam.IpamRegion,
		IsDefault:                           false,
		ResourceDiscoveryStatus:             "active",
		State:                               "associate-complete",
		Tags:                                parseTags(r),
	}
	ec2IpamResourceDiscoAsns.Put(id, a)
	ec2Ipams.Update(ipamID, func(i *EC2Ipam) { i.ResourceDiscoveryAssociationCount++ })
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateIpamResourceDiscoveryResponse %s>
  <requestId>%s</requestId>
  <ipamResourceDiscoveryAssociation>%s</ipamResourceDiscoveryAssociation>
</AssociateIpamResourceDiscoveryResponse>`, ec2Xmlns(), generateUUID(), ipamResourceDiscoveryAssociationBodyXML(a))
}

func handleDisassociateIpamResourceDiscovery(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("IpamResourceDiscoveryAssociationId")
	a, ok := ec2IpamResourceDiscoAsns.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidIpamResourceDiscoveryAssociationId.NotFound", fmt.Sprintf("The ipam resource discovery association ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	if a.IsDefault {
		ec2ErrorXML(w, "IpamResourceDiscoveryAssociationCannotDelete", "A default IPAM resource discovery association cannot be disassociated.", http.StatusBadRequest)
		return
	}
	a.State = "disassociate-complete"
	ec2IpamResourceDiscoAsns.Delete(id)
	ec2Ipams.Update(a.IpamId, func(i *EC2Ipam) {
		if i.ResourceDiscoveryAssociationCount > 0 {
			i.ResourceDiscoveryAssociationCount--
		}
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateIpamResourceDiscoveryResponse %s>
  <requestId>%s</requestId>
  <ipamResourceDiscoveryAssociation>%s</ipamResourceDiscoveryAssociation>
</DisassociateIpamResourceDiscoveryResponse>`, ec2Xmlns(), generateUUID(), ipamResourceDiscoveryAssociationBodyXML(a))
}

func handleDescribeIpamResourceDiscoveryAssociations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "IpamResourceDiscoveryAssociationId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, a := range ec2IpamResourceDiscoAsns.List() {
		if len(ids) > 0 && !ec2StrInValues(a.IpamResourceDiscoveryAssociationId, ids) {
			continue
		}
		if !ipamResourceDiscoveryAssociationMatchesFilters(a, filters) {
			continue
		}
		items.WriteString("<item>" + ipamResourceDiscoveryAssociationBodyXML(a) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpamResourceDiscoveryAssociationsResponse %s>
  <requestId>%s</requestId>
  <ipamResourceDiscoveryAssociationSet>%s</ipamResourceDiscoveryAssociationSet>
</DescribeIpamResourceDiscoveryAssociationsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func ipamResourceDiscoveryAssociationMatchesFilters(a EC2IpamResourceDiscoveryAssociation, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "ipam-id":
			if !ec2StrInValues(a.IpamId, vals) {
				return false
			}
		case "ipam-resource-discovery-id":
			if !ec2StrInValues(a.IpamResourceDiscoveryId, vals) {
				return false
			}
		case "ipam-resource-discovery-association-id":
			if !ec2StrInValues(a.IpamResourceDiscoveryAssociationId, vals) {
				return false
			}
		case "is-default":
			if a.IsDefault != ec2StrInValues("true", vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(a.State, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, a.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

// GetIpamResourceCidrs surfaces the VPC/subnet CIDRs IPAM monitors. We synthesize
// the monitored set from each pool allocation that targets a resource, joined to
// the scope it belongs to — faithful in shape and content (a real IPAM populates
// these from resource discovery; in the sim the allocations are the discovered
// resources).

func handleGetIpamResourceCidrs(w http.ResponseWriter, r *http.Request) {
	scopeID := r.FormValue("IpamScopeId")
	if scopeID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ipamScopeId", http.StatusBadRequest)
		return
	}
	poolID := r.FormValue("IpamPoolId")
	resourceIDFilter := r.FormValue("ResourceId")
	resourceType := r.FormValue("ResourceType")
	var items strings.Builder
	for _, p := range ec2IpamPools.List() {
		if p.IpamScopeId != scopeID {
			continue
		}
		if poolID != "" && p.IpamPoolId != poolID {
			continue
		}
		for _, a := range p.Allocations {
			if a.ResourceId == "" {
				continue
			}
			if resourceIDFilter != "" && a.ResourceId != resourceIDFilter {
				continue
			}
			if resourceType != "" && a.ResourceType != resourceType {
				continue
			}
			items.WriteString("<item>")
			fmt.Fprintf(&items, "<ipamId>%s</ipamId><ipamScopeId>%s</ipamScopeId><ipamPoolId>%s</ipamPoolId>", p.IpamId, p.IpamScopeId, p.IpamPoolId)
			fmt.Fprintf(&items, "<resourceRegion>%s</resourceRegion><resourceOwnerId>%s</resourceOwnerId><resourceId>%s</resourceId>", a.ResourceRegion, a.ResourceOwner, a.ResourceId)
			fmt.Fprintf(&items, "<resourceCidr>%s</resourceCidr><resourceType>%s</resourceType>", a.Cidr, a.ResourceType)
			fmt.Fprintf(&items, "<ipUsage>%s</ipUsage><complianceStatus>compliant</complianceStatus><managementState>managed</managementState><overlapStatus>nonoverlapping</overlapStatus>", "0.0")
			items.WriteString("</item>")
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamResourceCidrsResponse %s>
  <requestId>%s</requestId>
  <ipamResourceCidrSet>%s</ipamResourceCidrSet>
</GetIpamResourceCidrsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleGetIpamAddressHistory(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Cidr")
	if cidr == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter cidr", http.StatusBadRequest)
		return
	}
	if r.FormValue("IpamScopeId") == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ipamScopeId", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var items strings.Builder
	// Surface the allocation history for the requested CIDR across all pools.
	for _, p := range ec2IpamPools.List() {
		for _, a := range p.Allocations {
			if a.Cidr != cidr {
				continue
			}
			items.WriteString("<item>")
			fmt.Fprintf(&items, "<resourceOwnerId>%s</resourceOwnerId><resourceRegion>%s</resourceRegion><resourceType>%s</resourceType>", a.ResourceOwner, a.ResourceRegion, "custom")
			fmt.Fprintf(&items, "<resourceId>%s</resourceId><resourceCidr>%s</resourceCidr>", a.ResourceId, a.Cidr)
			fmt.Fprintf(&items, "<resourceComplianceStatus>compliant</resourceComplianceStatus><resourceOverlapStatus>nonoverlapping</resourceOverlapStatus>")
			fmt.Fprintf(&items, "<sampledStartTime>%s</sampledStartTime>", now)
			items.WriteString("</item>")
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetIpamAddressHistoryResponse %s>
  <requestId>%s</requestId>
  <historyRecordSet>%s</historyRecordSet>
</GetIpamAddressHistoryResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleModifyIpamResourceCidr(w http.ResponseWriter, r *http.Request) {
	resourceID := r.FormValue("ResourceId")
	resourceCidr := r.FormValue("ResourceCidr")
	currentScope := r.FormValue("CurrentIpamScopeId")
	if resourceID == "" || resourceCidr == "" || currentScope == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain resourceId, resourceCidr, and currentIpamScopeId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2IpamScopes.Get(currentScope); !ok {
		ec2ErrorXML(w, "InvalidIpamScopeId.NotFound", fmt.Sprintf("The ipam scope ID '%s' does not exist", currentScope), http.StatusBadRequest)
		return
	}
	destScope := r.FormValue("DestinationIpamScopeId")
	if destScope == "" {
		destScope = currentScope
	}
	scope, _ := ec2IpamScopes.Get(destScope)
	region := r.FormValue("ResourceRegion")
	if region == "" {
		region = awsRegion()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<ipamId>%s</ipamId><ipamScopeId>%s</ipamScopeId>", strings.TrimPrefix(scope.IpamArn[strings.LastIndex(scope.IpamArn, ":")+1:], "ipam/"), destScope)
	fmt.Fprintf(&b, "<resourceRegion>%s</resourceRegion><resourceOwnerId>%s</resourceOwnerId><resourceId>%s</resourceId>", region, ec2Owner(), resourceID)
	fmt.Fprintf(&b, "<resourceCidr>%s</resourceCidr>", resourceCidr)
	fmt.Fprintf(&b, "<ipUsage>0.0</ipUsage><complianceStatus>compliant</complianceStatus><managementState>managed</managementState><overlapStatus>nonoverlapping</overlapStatus>")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyIpamResourceCidrResponse %s>
  <requestId>%s</requestId>
  <ipamResourceCidr>%s</ipamResourceCidr>
</ModifyIpamResourceCidrResponse>`, ec2Xmlns(), generateUUID(), b.String())
}

func handleEnableIpamOrganizationAdminAccount(w http.ResponseWriter, r *http.Request) {
	acct := r.FormValue("DelegatedAdminAccountId")
	if acct == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter delegatedAdminAccountId", http.StatusBadRequest)
		return
	}
	ec2IpamAdminAccounts.Put(acct, ec2IpamAdminAccount{AccountId: acct})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableIpamOrganizationAdminAccountResponse %s>
  <requestId>%s</requestId>
  <success>true</success>
</EnableIpamOrganizationAdminAccountResponse>`, ec2Xmlns(), generateUUID())
}

func handleDisableIpamOrganizationAdminAccount(w http.ResponseWriter, r *http.Request) {
	acct := r.FormValue("DelegatedAdminAccountId")
	if acct == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter delegatedAdminAccountId", http.StatusBadRequest)
		return
	}
	ec2IpamAdminAccounts.Delete(acct)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableIpamOrganizationAdminAccountResponse %s>
  <requestId>%s</requestId>
  <success>true</success>
</DisableIpamOrganizationAdminAccountResponse>`, ec2Xmlns(), generateUUID())
}
