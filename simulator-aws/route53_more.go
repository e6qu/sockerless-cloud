package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Route 53 — remaining operation slices: reusable delegation sets, CIDR
// collections (blocks/locations), DNSSEC key signing keys + per-zone DNSSEC
// status, traffic policy instances, VPC association authorizations, account /
// hosted-zone limits, the health-checker IP ranges, last-failure reason,
// batched tag reads, TestDNSAnswer, and hosted-zone comment/feature updates.
// All REST + XML on the /2013-04-01/ path, matching the real Route 53 wire
// shapes the AWS SDK Go v2 + `aws` CLI parse and the smithy model validates.

type r53StoredDelegationSet struct {
	Set R53ReusableDelegationSet
}

type r53StoredCidrCollection struct {
	Collection R53CidrCollection
	// Blocks keyed by location name → ordered CIDR blocks.
	Blocks map[string][]string
}

type r53StoredDNSSEC struct {
	// ServeSignature is "SIGNING", "NOT_SIGNING", "DELETING", etc.
	ServeSignature string
	// KSKs keyed by name.
	KSKs map[string]R53KeySigningKey
}

type r53StoredTrafficPolicyInstance struct {
	Instance R53TrafficPolicyInstance
}

var (
	r53DelegationSets  sim.Store[r53StoredDelegationSet]
	r53CidrCollections sim.Store[r53StoredCidrCollection]
	r53DNSSEC          sim.Store[r53StoredDNSSEC]
	r53PolicyInstances sim.Store[r53StoredTrafficPolicyInstance]
	r53VPCAuthz        sim.Store[[]R53VPC]
	r53MoreMu          sync.Mutex
)

// registerRoute53More mounts the remaining Route 53 REST slices on the shared
// mux. Called once from registerRoute53 in route53.go.
func registerRoute53More(srv *sim.Server) {
	r53DelegationSets = sim.MakeStore[r53StoredDelegationSet](srv.DB(), "route53_delegation_sets")
	r53CidrCollections = sim.MakeStore[r53StoredCidrCollection](srv.DB(), "route53_cidr_collections")
	r53DNSSEC = sim.MakeStore[r53StoredDNSSEC](srv.DB(), "route53_dnssec")
	r53PolicyInstances = sim.MakeStore[r53StoredTrafficPolicyInstance](srv.DB(), "route53_policy_instances")
	r53VPCAuthz = sim.MakeStore[[]R53VPC](srv.DB(), "route53_vpc_authorizations")

	dsResource := cloudTrailRESTResource("AWS::Route53::ReusableDelegationSet", "id")
	cidrResource := cloudTrailRESTResource("AWS::Route53::CidrCollection", "id", "collectionId")
	zoneResource := cloudTrailRESTResource("AWS::Route53::HostedZone", "id", "hostedZoneId")
	kskResource := cloudTrailRESTResource("AWS::Route53::KeySigningKey", "name")
	tpiResource := cloudTrailRESTResource("AWS::Route53::TrafficPolicyInstance", "id")

	v := "/" + r53APIVersion

	// Reusable delegation sets
	srv.HandleFunc("POST "+v+"/delegationset", cloudTrailRecordedREST("CreateReusableDelegationSet", "route53.amazonaws.com", nil, handleR53CreateReusableDelegationSet))
	srv.HandleFunc("GET "+v+"/delegationset", cloudTrailRecordedREST("ListReusableDelegationSets", "route53.amazonaws.com", nil, handleR53ListReusableDelegationSets))
	srv.HandleFunc("GET "+v+"/delegationset/{id}", cloudTrailRecordedREST("GetReusableDelegationSet", "route53.amazonaws.com", dsResource, handleR53GetReusableDelegationSet))
	srv.HandleFunc("DELETE "+v+"/delegationset/{id}", cloudTrailRecordedREST("DeleteReusableDelegationSet", "route53.amazonaws.com", dsResource, handleR53DeleteReusableDelegationSet))
	srv.HandleFunc("GET "+v+"/reusabledelegationsetlimit/{id}/{type}", cloudTrailRecordedREST("GetReusableDelegationSetLimit", "route53.amazonaws.com", dsResource, handleR53GetReusableDelegationSetLimit))

	// CIDR collections
	srv.HandleFunc("POST "+v+"/cidrcollection", cloudTrailRecordedREST("CreateCidrCollection", "route53.amazonaws.com", nil, handleR53CreateCidrCollection))
	srv.HandleFunc("GET "+v+"/cidrcollection", cloudTrailRecordedREST("ListCidrCollections", "route53.amazonaws.com", nil, handleR53ListCidrCollections))
	srv.HandleFunc("POST "+v+"/cidrcollection/{id}", cloudTrailRecordedREST("ChangeCidrCollection", "route53.amazonaws.com", cidrResource, handleR53ChangeCidrCollection))
	srv.HandleFunc("DELETE "+v+"/cidrcollection/{id}", cloudTrailRecordedREST("DeleteCidrCollection", "route53.amazonaws.com", cidrResource, handleR53DeleteCidrCollection))
	srv.HandleFunc("GET "+v+"/cidrcollection/{collectionId}", cloudTrailRecordedREST("ListCidrLocations", "route53.amazonaws.com", cidrResource, handleR53ListCidrLocations))
	srv.HandleFunc("GET "+v+"/cidrcollection/{collectionId}/cidrblocks", cloudTrailRecordedREST("ListCidrBlocks", "route53.amazonaws.com", cidrResource, handleR53ListCidrBlocks))

	// DNSSEC key signing keys + per-zone status
	srv.HandleFunc("POST "+v+"/keysigningkey", cloudTrailRecordedREST("CreateKeySigningKey", "route53.amazonaws.com", nil, handleR53CreateKeySigningKey))
	srv.HandleFunc("DELETE "+v+"/keysigningkey/{hostedZoneId}/{name}", cloudTrailRecordedREST("DeleteKeySigningKey", "route53.amazonaws.com", kskResource, handleR53DeleteKeySigningKey))
	srv.HandleFunc("POST "+v+"/keysigningkey/{hostedZoneId}/{name}/activate", cloudTrailRecordedREST("ActivateKeySigningKey", "route53.amazonaws.com", kskResource, handleR53ActivateKeySigningKey))
	srv.HandleFunc("POST "+v+"/keysigningkey/{hostedZoneId}/{name}/deactivate", cloudTrailRecordedREST("DeactivateKeySigningKey", "route53.amazonaws.com", kskResource, handleR53DeactivateKeySigningKey))
	srv.HandleFunc("POST "+v+"/hostedzone/{hostedZoneId}/enable-dnssec", cloudTrailRecordedREST("EnableHostedZoneDNSSEC", "route53.amazonaws.com", zoneResource, handleR53EnableHostedZoneDNSSEC))
	srv.HandleFunc("POST "+v+"/hostedzone/{hostedZoneId}/disable-dnssec", cloudTrailRecordedREST("DisableHostedZoneDNSSEC", "route53.amazonaws.com", zoneResource, handleR53DisableHostedZoneDNSSEC))
	srv.HandleFunc("GET "+v+"/hostedzone/{hostedZoneId}/dnssec", cloudTrailRecordedREST("GetDNSSEC", "route53.amazonaws.com", zoneResource, handleR53GetDNSSEC))

	// Traffic policy instances
	srv.HandleFunc("POST "+v+"/trafficpolicyinstance", cloudTrailRecordedREST("CreateTrafficPolicyInstance", "route53.amazonaws.com", nil, handleR53CreateTrafficPolicyInstance))
	srv.HandleFunc("GET "+v+"/trafficpolicyinstance/{id}", cloudTrailRecordedREST("GetTrafficPolicyInstance", "route53.amazonaws.com", tpiResource, handleR53GetTrafficPolicyInstance))
	srv.HandleFunc("POST "+v+"/trafficpolicyinstance/{id}", cloudTrailRecordedREST("UpdateTrafficPolicyInstance", "route53.amazonaws.com", tpiResource, handleR53UpdateTrafficPolicyInstance))
	srv.HandleFunc("DELETE "+v+"/trafficpolicyinstance/{id}", cloudTrailRecordedREST("DeleteTrafficPolicyInstance", "route53.amazonaws.com", tpiResource, handleR53DeleteTrafficPolicyInstance))
	srv.HandleFunc("GET "+v+"/trafficpolicyinstances", cloudTrailRecordedREST("ListTrafficPolicyInstances", "route53.amazonaws.com", nil, handleR53ListTrafficPolicyInstances))
	srv.HandleFunc("GET "+v+"/trafficpolicyinstances/hostedzone", cloudTrailRecordedREST("ListTrafficPolicyInstancesByHostedZone", "route53.amazonaws.com", nil, handleR53ListTrafficPolicyInstancesByHostedZone))
	srv.HandleFunc("GET "+v+"/trafficpolicyinstances/trafficpolicy", cloudTrailRecordedREST("ListTrafficPolicyInstancesByPolicy", "route53.amazonaws.com", nil, handleR53ListTrafficPolicyInstancesByPolicy))
	srv.HandleFunc("GET "+v+"/trafficpolicyinstancecount", cloudTrailRecordedREST("GetTrafficPolicyInstanceCount", "route53.amazonaws.com", nil, handleR53GetTrafficPolicyInstanceCount))

	// Traffic policy comment update (POST /trafficpolicy/{id}/{version} —
	// distinct from CreateTrafficPolicyVersion, which is POST /trafficpolicy/{id}).
	srv.HandleFunc("POST "+v+"/trafficpolicy/{id}/{version}", cloudTrailRecordedREST("UpdateTrafficPolicyComment", "route53.amazonaws.com", cloudTrailRESTResource("AWS::Route53::TrafficPolicy", "id"), handleR53UpdateTrafficPolicyComment))

	// VPC association authorizations
	srv.HandleFunc("POST "+v+"/hostedzone/{hostedZoneId}/authorizevpcassociation", cloudTrailRecordedREST("CreateVPCAssociationAuthorization", "route53.amazonaws.com", zoneResource, handleR53CreateVPCAssociationAuthorization))
	srv.HandleFunc("POST "+v+"/hostedzone/{hostedZoneId}/deauthorizevpcassociation", cloudTrailRecordedREST("DeleteVPCAssociationAuthorization", "route53.amazonaws.com", zoneResource, handleR53DeleteVPCAssociationAuthorization))
	srv.HandleFunc("GET "+v+"/hostedzone/{hostedZoneId}/authorizevpcassociation", cloudTrailRecordedREST("ListVPCAssociationAuthorizations", "route53.amazonaws.com", zoneResource, handleR53ListVPCAssociationAuthorizations))

	// Limits
	srv.HandleFunc("GET "+v+"/accountlimit/{type}", cloudTrailRecordedREST("GetAccountLimit", "route53.amazonaws.com", nil, handleR53GetAccountLimit))
	srv.HandleFunc("GET "+v+"/hostedzonelimit/{hostedZoneId}/{type}", cloudTrailRecordedREST("GetHostedZoneLimit", "route53.amazonaws.com", zoneResource, handleR53GetHostedZoneLimit))

	// Static-ish reads + tag batch + TestDNSAnswer
	srv.HandleFunc("GET "+v+"/checkeripranges", cloudTrailRecordedREST("GetCheckerIpRanges", "route53.amazonaws.com", nil, handleR53GetCheckerIpRanges))
	srv.HandleFunc("GET "+v+"/healthcheck/{id}/lastfailurereason", cloudTrailRecordedREST("GetHealthCheckLastFailureReason", "route53.amazonaws.com", cloudTrailRESTResource("AWS::Route53::HealthCheck", "id"), handleR53GetHealthCheckLastFailureReason))
	srv.HandleFunc("POST "+v+"/tags/{resourceType}", cloudTrailRecordedREST("ListTagsForResources", "route53.amazonaws.com", nil, handleR53ListTagsForResources))
	srv.HandleFunc("GET "+v+"/testdnsanswer", cloudTrailRecordedREST("TestDNSAnswer", "route53.amazonaws.com", nil, handleR53TestDNSAnswer))

	// Hosted-zone comment + feature updates
	srv.HandleFunc("POST "+v+"/hostedzone/{id}", cloudTrailRecordedREST("UpdateHostedZoneComment", "route53.amazonaws.com", cloudTrailRESTResource("AWS::Route53::HostedZone", "id"), handleR53UpdateHostedZoneComment))
	srv.HandleFunc("POST "+v+"/hostedzone/{hostedZoneId}/features", cloudTrailRecordedREST("UpdateHostedZoneFeatures", "route53.amazonaws.com", zoneResource, handleR53UpdateHostedZoneFeatures))
}

// r53DefaultNameServers are the four NS hostnames a real reusable delegation
// set (and a default hosted-zone delegation set) advertises.
func r53DefaultNameServers() []string {
	return []string{"ns-1.awsdns-00.com", "ns-2.awsdns-01.net", "ns-3.awsdns-02.org", "ns-4.awsdns-03.co.uk"}
}

// R53ReusableDelegationSet mirrors the smithy DelegationSet shape: Id,
// CallerReference, and four name servers. (Distinct from R53DelegationSet in
// route53.go, which is the trimmed NameServers-only shape the hosted-zone
// responses embed.)
type R53ReusableDelegationSet struct {
	XMLName         xml.Name `xml:"DelegationSet"`
	Xmlns           string   `xml:"xmlns,attr,omitempty"`
	Id              string   `xml:"Id"`
	CallerReference string   `xml:"CallerReference"`
	NameServers     []string `xml:"NameServers>NameServer"`
}

type R53CreateReusableDelegationSetRequest struct {
	XMLName         xml.Name `xml:"CreateReusableDelegationSetRequest"`
	CallerReference string   `xml:"CallerReference"`
	HostedZoneId    string   `xml:"HostedZoneId,omitempty"`
}

type R53CreateReusableDelegationSetResponse struct {
	XMLName       xml.Name                 `xml:"CreateReusableDelegationSetResponse"`
	Xmlns         string                   `xml:"xmlns,attr,omitempty"`
	DelegationSet R53ReusableDelegationSet `xml:"DelegationSet"`
}

type R53GetReusableDelegationSetResponse struct {
	XMLName       xml.Name                 `xml:"GetReusableDelegationSetResponse"`
	Xmlns         string                   `xml:"xmlns,attr,omitempty"`
	DelegationSet R53ReusableDelegationSet `xml:"DelegationSet"`
}

type R53ListReusableDelegationSetsResponse struct {
	XMLName        xml.Name                   `xml:"ListReusableDelegationSetsResponse"`
	Xmlns          string                     `xml:"xmlns,attr,omitempty"`
	DelegationSets []R53ReusableDelegationSet `xml:"DelegationSets>DelegationSet"`
	Marker         string                     `xml:"Marker"`
	IsTruncated    bool                       `xml:"IsTruncated"`
	NextMarker     string                     `xml:"NextMarker,omitempty"`
	MaxItems       string                     `xml:"MaxItems"`
}

type R53ReusableDelegationSetLimit struct {
	Type  string `xml:"Type"`
	Value int64  `xml:"Value"`
}

type R53GetReusableDelegationSetLimitResponse struct {
	XMLName xml.Name                      `xml:"GetReusableDelegationSetLimitResponse"`
	Xmlns   string                        `xml:"xmlns,attr,omitempty"`
	Limit   R53ReusableDelegationSetLimit `xml:"Limit"`
	Count   int64                         `xml:"Count"`
}

func handleR53CreateReusableDelegationSet(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	var req R53CreateReusableDelegationSetRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	if req.CallerReference == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "CallerReference is required")
		return
	}
	for _, sd := range r53DelegationSets.List() {
		if sd.Set.CallerReference == req.CallerReference {
			r53WriteError(w, http.StatusConflict, "DelegationSetAlreadyCreated",
				"A reusable delegation set already exists with the specified caller reference "+req.CallerReference+".")
			return
		}
	}
	if req.HostedZoneId != "" {
		if _, ok := r53Zones.Get(r53ZoneIDFromPath(req.HostedZoneId)); !ok {
			r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
			return
		}
	}
	id := r53RandomID()
	set := R53ReusableDelegationSet{
		Xmlns:           r53Namespace,
		Id:              "/delegationset/" + id,
		CallerReference: req.CallerReference,
		NameServers:     r53DefaultNameServers(),
	}
	r53DelegationSets.Put(id, r53StoredDelegationSet{Set: set})
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/delegationset/"+id)
	r53WriteXML(w, http.StatusCreated, R53CreateReusableDelegationSetResponse{Xmlns: r53Namespace, DelegationSet: set})
}

func r53DelegationSetIDFromPath(p string) string {
	if strings.HasPrefix(p, "/delegationset/") {
		return strings.TrimPrefix(p, "/delegationset/")
	}
	return p
}

func handleR53GetReusableDelegationSet(w http.ResponseWriter, r *http.Request) {
	id := r53DelegationSetIDFromPath(r.PathValue("id"))
	stored, ok := r53DelegationSets.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchDelegationSet", "A reusable delegation set with id "+id+" does not exist.")
		return
	}
	r53WriteXML(w, http.StatusOK, R53GetReusableDelegationSetResponse{Xmlns: r53Namespace, DelegationSet: stored.Set})
}

func handleR53DeleteReusableDelegationSet(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	id := r53DelegationSetIDFromPath(r.PathValue("id"))
	if _, ok := r53DelegationSets.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchDelegationSet", "A reusable delegation set with id "+id+" does not exist.")
		return
	}
	r53DelegationSets.Delete(id)
	r53WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"DeleteReusableDelegationSetResponse"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
	}{Xmlns: r53Namespace})
}

func handleR53ListReusableDelegationSets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	maxItems := 100
	if raw := q.Get("maxitems"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= 100 {
			maxItems = parsed
		}
	}
	items := []R53ReusableDelegationSet{}
	for _, sd := range r53DelegationSets.List() {
		items = append(items, sd.Set)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Id < items[j].Id })
	page, next := awsPageExplicit(items, q.Get("marker"), maxItems)
	r53WriteXML(w, http.StatusOK, R53ListReusableDelegationSetsResponse{
		Xmlns:          r53Namespace,
		DelegationSets: page,
		Marker:         q.Get("marker"),
		IsTruncated:    next != "",
		NextMarker:     next,
		MaxItems:       strconv.Itoa(maxItems),
	})
}

func handleR53GetReusableDelegationSetLimit(w http.ResponseWriter, r *http.Request) {
	id := r53DelegationSetIDFromPath(r.PathValue("id"))
	if _, ok := r53DelegationSets.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchDelegationSet", "A reusable delegation set with id "+id+" does not exist.")
		return
	}
	limitType := r.PathValue("type")
	if limitType == "" {
		limitType = "MAX_ZONES_BY_REUSABLE_DELEGATION_SET"
	}
	r53WriteXML(w, http.StatusOK, R53GetReusableDelegationSetLimitResponse{
		Xmlns: r53Namespace,
		Limit: R53ReusableDelegationSetLimit{Type: limitType, Value: 100},
		Count: 0,
	})
}

type R53CidrCollection struct {
	XMLName xml.Name `xml:"Collection"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Arn     string   `xml:"Arn"`
	Id      string   `xml:"Id"`
	Name    string   `xml:"Name"`
	Version int64    `xml:"Version"`
}

type R53CreateCidrCollectionRequest struct {
	XMLName         xml.Name `xml:"CreateCidrCollectionRequest"`
	Name            string   `xml:"Name"`
	CallerReference string   `xml:"CallerReference"`
}

type R53CreateCidrCollectionResponse struct {
	XMLName    xml.Name          `xml:"CreateCidrCollectionResponse"`
	Xmlns      string            `xml:"xmlns,attr,omitempty"`
	Collection R53CidrCollection `xml:"Collection"`
}

type R53CidrCollectionChange struct {
	LocationName string   `xml:"LocationName"`
	Action       string   `xml:"Action"`
	CidrList     []string `xml:"CidrList>Cidr"`
}

type R53ChangeCidrCollectionRequest struct {
	XMLName           xml.Name                  `xml:"ChangeCidrCollectionRequest"`
	CollectionVersion int64                     `xml:"CollectionVersion,omitempty"`
	Changes           []R53CidrCollectionChange `xml:"Changes>member"`
}

type R53ChangeCidrCollectionResponse struct {
	XMLName xml.Name `xml:"ChangeCidrCollectionResponse"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Id      string   `xml:"Id"`
}

type R53CollectionSummary struct {
	Arn     string `xml:"Arn"`
	Id      string `xml:"Id"`
	Name    string `xml:"Name"`
	Version int64  `xml:"Version"`
}

type R53ListCidrCollectionsResponse struct {
	XMLName         xml.Name               `xml:"ListCidrCollectionsResponse"`
	Xmlns           string                 `xml:"xmlns,attr,omitempty"`
	NextToken       string                 `xml:"NextToken,omitempty"`
	CidrCollections []R53CollectionSummary `xml:"CidrCollections>member"`
}

type R53CidrBlockSummary struct {
	CidrBlock    string `xml:"CidrBlock"`
	LocationName string `xml:"LocationName"`
}

type R53ListCidrBlocksResponse struct {
	XMLName    xml.Name              `xml:"ListCidrBlocksResponse"`
	Xmlns      string                `xml:"xmlns,attr,omitempty"`
	NextToken  string                `xml:"NextToken,omitempty"`
	CidrBlocks []R53CidrBlockSummary `xml:"CidrBlocks>member"`
}

type R53LocationSummary struct {
	LocationName string `xml:"LocationName"`
}

type R53ListCidrLocationsResponse struct {
	XMLName       xml.Name             `xml:"ListCidrLocationsResponse"`
	Xmlns         string               `xml:"xmlns,attr,omitempty"`
	NextToken     string               `xml:"NextToken,omitempty"`
	CidrLocations []R53LocationSummary `xml:"CidrLocations>member"`
}

func r53CidrCollectionARN(id string) string {
	return "arn:aws:route53::" + awsAccountID() + ":cidrcollection/" + id
}

func handleR53CreateCidrCollection(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	var req R53CreateCidrCollectionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	if req.Name == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "Name is required")
		return
	}
	if req.CallerReference == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "CallerReference is required")
		return
	}
	for _, sc := range r53CidrCollections.List() {
		if sc.Collection.Name == req.Name {
			r53WriteError(w, http.StatusConflict, "CidrCollectionAlreadyExists",
				"A CIDR collection with name "+req.Name+" already exists.")
			return
		}
	}
	id := r53UUID()
	coll := R53CidrCollection{
		Xmlns:   r53Namespace,
		Arn:     r53CidrCollectionARN(id),
		Id:      id,
		Name:    req.Name,
		Version: 1,
	}
	r53CidrCollections.Put(id, r53StoredCidrCollection{Collection: coll, Blocks: map[string][]string{}})
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/cidrcollection/"+id)
	r53WriteXML(w, http.StatusCreated, R53CreateCidrCollectionResponse{Xmlns: r53Namespace, Collection: coll})
}

func handleR53ChangeCidrCollection(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	id := r.PathValue("id")
	stored, ok := r53CidrCollections.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchCidrCollectionException", "A CIDR collection with id "+id+" does not exist.")
		return
	}
	var req R53ChangeCidrCollectionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	if stored.Blocks == nil {
		stored.Blocks = map[string][]string{}
	}
	for _, ch := range req.Changes {
		switch strings.ToUpper(ch.Action) {
		case "PUT":
			existing := map[string]bool{}
			for _, b := range stored.Blocks[ch.LocationName] {
				existing[b] = true
			}
			for _, c := range ch.CidrList {
				if !existing[c] {
					stored.Blocks[ch.LocationName] = append(stored.Blocks[ch.LocationName], c)
					existing[c] = true
				}
			}
		case "DELETE_IF_EXISTS":
			remove := map[string]bool{}
			for _, c := range ch.CidrList {
				remove[c] = true
			}
			kept := stored.Blocks[ch.LocationName][:0]
			for _, b := range stored.Blocks[ch.LocationName] {
				if !remove[b] {
					kept = append(kept, b)
				}
			}
			if len(kept) == 0 {
				delete(stored.Blocks, ch.LocationName)
			} else {
				stored.Blocks[ch.LocationName] = kept
			}
		default:
			r53WriteError(w, http.StatusBadRequest, "InvalidInput", "Unknown CIDR collection change action: "+ch.Action)
			return
		}
	}
	stored.Collection.Version++
	r53CidrCollections.Put(id, stored)
	r53WriteXML(w, http.StatusOK, R53ChangeCidrCollectionResponse{
		Xmlns: r53Namespace,
		Id:    "/change/" + strings.TrimPrefix(r53ChangeID(), "C"),
	})
}

func handleR53DeleteCidrCollection(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	id := r.PathValue("id")
	stored, ok := r53CidrCollections.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchCidrCollectionException", "A CIDR collection with id "+id+" does not exist.")
		return
	}
	if len(stored.Blocks) > 0 {
		r53WriteError(w, http.StatusBadRequest, "CidrCollectionInUseException",
			"This CIDR collection contains CIDR blocks and cannot be deleted.")
		return
	}
	r53CidrCollections.Delete(id)
	r53WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"DeleteCidrCollectionResponse"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
	}{Xmlns: r53Namespace})
}

func handleR53ListCidrCollections(w http.ResponseWriter, _ *http.Request) {
	summaries := []R53CollectionSummary{}
	for _, sc := range r53CidrCollections.List() {
		summaries = append(summaries, R53CollectionSummary{
			Arn:     sc.Collection.Arn,
			Id:      sc.Collection.Id,
			Name:    sc.Collection.Name,
			Version: sc.Collection.Version,
		})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Id < summaries[j].Id })
	r53WriteXML(w, http.StatusOK, R53ListCidrCollectionsResponse{
		Xmlns:           r53Namespace,
		CidrCollections: summaries,
	})
}

func handleR53ListCidrLocations(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("collectionId")
	stored, ok := r53CidrCollections.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchCidrCollectionException", "A CIDR collection with id "+id+" does not exist.")
		return
	}
	names := make([]string, 0, len(stored.Blocks))
	for name := range stored.Blocks {
		names = append(names, name)
	}
	sort.Strings(names)
	locations := make([]R53LocationSummary, 0, len(names))
	for _, name := range names {
		locations = append(locations, R53LocationSummary{LocationName: name})
	}
	r53WriteXML(w, http.StatusOK, R53ListCidrLocationsResponse{
		Xmlns:         r53Namespace,
		CidrLocations: locations,
	})
}

func handleR53ListCidrBlocks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("collectionId")
	stored, ok := r53CidrCollections.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchCidrCollectionException", "A CIDR collection with id "+id+" does not exist.")
		return
	}
	locationFilter := r.URL.Query().Get("location")
	names := make([]string, 0, len(stored.Blocks))
	for name := range stored.Blocks {
		names = append(names, name)
	}
	sort.Strings(names)
	blocks := []R53CidrBlockSummary{}
	for _, name := range names {
		if locationFilter != "" && name != locationFilter {
			continue
		}
		for _, b := range stored.Blocks[name] {
			blocks = append(blocks, R53CidrBlockSummary{CidrBlock: b, LocationName: name})
		}
	}
	r53WriteXML(w, http.StatusOK, R53ListCidrBlocksResponse{
		Xmlns:      r53Namespace,
		CidrBlocks: blocks,
	})
}

type R53KeySigningKey struct {
	Name                     string `xml:"Name"`
	KmsArn                   string `xml:"KmsArn"`
	Flag                     int    `xml:"Flag"`
	SigningAlgorithmMnemonic string `xml:"SigningAlgorithmMnemonic"`
	SigningAlgorithmType     int    `xml:"SigningAlgorithmType"`
	DigestAlgorithmMnemonic  string `xml:"DigestAlgorithmMnemonic"`
	DigestAlgorithmType      int    `xml:"DigestAlgorithmType"`
	KeyTag                   int    `xml:"KeyTag"`
	DigestValue              string `xml:"DigestValue"`
	PublicKey                string `xml:"PublicKey"`
	DSRecord                 string `xml:"DSRecord"`
	DNSKEYRecord             string `xml:"DNSKEYRecord"`
	Status                   string `xml:"Status"`
	StatusMessage            string `xml:"StatusMessage,omitempty"`
	CreatedDate              string `xml:"CreatedDate"`
	LastModifiedDate         string `xml:"LastModifiedDate"`
}

type R53CreateKeySigningKeyRequest struct {
	XMLName                 xml.Name `xml:"CreateKeySigningKeyRequest"`
	CallerReference         string   `xml:"CallerReference"`
	HostedZoneId            string   `xml:"HostedZoneId"`
	KeyManagementServiceArn string   `xml:"KeyManagementServiceArn"`
	Name                    string   `xml:"Name"`
	Status                  string   `xml:"Status"`
}

type R53CreateKeySigningKeyResponse struct {
	XMLName       xml.Name         `xml:"CreateKeySigningKeyResponse"`
	Xmlns         string           `xml:"xmlns,attr,omitempty"`
	ChangeInfo    R53ChangeInfo    `xml:"ChangeInfo"`
	KeySigningKey R53KeySigningKey `xml:"KeySigningKey"`
}

type R53KSKChangeInfoResponse struct {
	XMLName    xml.Name      `xml:""`
	Xmlns      string        `xml:"xmlns,attr,omitempty"`
	ChangeInfo R53ChangeInfo `xml:"ChangeInfo"`
}

type R53DNSSECStatus struct {
	ServeSignature string `xml:"ServeSignature,omitempty"`
	StatusMessage  string `xml:"StatusMessage,omitempty"`
}

type R53GetDNSSECResponse struct {
	XMLName        xml.Name           `xml:"GetDNSSECResponse"`
	Xmlns          string             `xml:"xmlns,attr,omitempty"`
	Status         R53DNSSECStatus    `xml:"Status"`
	KeySigningKeys []R53KeySigningKey `xml:"KeySigningKeys>member"`
}

func r53DNSSECState(zoneID string) r53StoredDNSSEC {
	if st, ok := r53DNSSEC.Get(zoneID); ok {
		if st.KSKs == nil {
			st.KSKs = map[string]R53KeySigningKey{}
		}
		return st
	}
	return r53StoredDNSSEC{ServeSignature: "NOT_SIGNING", KSKs: map[string]R53KeySigningKey{}}
}

func r53NewKSKChange(w http.ResponseWriter, root string) {
	change := newR53Change("INSYNC", "")
	r53Changes.Put(strings.TrimPrefix(change.Id, "/change/"), r53StoredChange{Info: change})
	r53WriteXML(w, http.StatusOK, R53KSKChangeInfoResponse{XMLName: xml.Name{Local: root}, Xmlns: r53Namespace, ChangeInfo: change})
}

func handleR53CreateKeySigningKey(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	var req R53CreateKeySigningKeyRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	zoneID := r53ZoneIDFromPath(req.HostedZoneId)
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	if req.Name == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidKeySigningKeyName", "Name is required")
		return
	}
	if req.KeyManagementServiceArn == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidArgument", "KeyManagementServiceArn is required")
		return
	}
	state := r53DNSSECState(zoneID)
	if _, exists := state.KSKs[req.Name]; exists {
		r53WriteError(w, http.StatusConflict, "KeySigningKeyAlreadyExists",
			"A key signing key with name "+req.Name+" already exists for this hosted zone.")
		return
	}
	status := req.Status
	if status == "" {
		status = "ACTIVE"
	}
	now := r53NowISO()
	ksk := R53KeySigningKey{
		Name:                     req.Name,
		KmsArn:                   req.KeyManagementServiceArn,
		Flag:                     257,
		SigningAlgorithmMnemonic: "ECDSAP256SHA256",
		SigningAlgorithmType:     13,
		DigestAlgorithmMnemonic:  "SHA-256",
		DigestAlgorithmType:      2,
		KeyTag:                   12345,
		DigestValue:              "B5D2A...",
		PublicKey:                "mdsswUyr3DPW132mOi8V9xESWE8jTo0dxCjjnopKl+GqJxpVXckHAeF+KkxLbxILfDLUT0rAK9iUzy1L53eKGQ==",
		DSRecord:                 "12345 13 2 B5D2A...",
		DNSKEYRecord:             "257 3 13 mdsswUyr3DPW132mOi8V9xESWE8jTo0dxCjjnopKl+GqJxpVXckHAeF+KkxLbxILfDLUT0rAK9iUzy1L53eKGQ==",
		Status:                   status,
		CreatedDate:              now,
		LastModifiedDate:         now,
	}
	state.KSKs[req.Name] = ksk
	r53DNSSEC.Put(zoneID, state)
	change := newR53Change("INSYNC", "")
	r53Changes.Put(strings.TrimPrefix(change.Id, "/change/"), r53StoredChange{Info: change})
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/keysigningkey/"+zoneID+"/"+req.Name)
	r53WriteXML(w, http.StatusCreated, R53CreateKeySigningKeyResponse{
		Xmlns:         r53Namespace,
		ChangeInfo:    change,
		KeySigningKey: ksk,
	})
}

func handleR53DeleteKeySigningKey(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	name := r.PathValue("name")
	state := r53DNSSECState(zoneID)
	if _, ok := state.KSKs[name]; !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchKeySigningKey", "A key signing key with name "+name+" does not exist.")
		return
	}
	delete(state.KSKs, name)
	r53DNSSEC.Put(zoneID, state)
	r53NewKSKChange(w, "DeleteKeySigningKeyResponse")
}

func r53SetKSKStatus(w http.ResponseWriter, r *http.Request, status, root string) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	name := r.PathValue("name")
	state := r53DNSSECState(zoneID)
	ksk, ok := state.KSKs[name]
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchKeySigningKey", "A key signing key with name "+name+" does not exist.")
		return
	}
	ksk.Status = status
	ksk.LastModifiedDate = r53NowISO()
	state.KSKs[name] = ksk
	r53DNSSEC.Put(zoneID, state)
	r53NewKSKChange(w, root)
}

func handleR53ActivateKeySigningKey(w http.ResponseWriter, r *http.Request) {
	r53SetKSKStatus(w, r, "ACTIVE", "ActivateKeySigningKeyResponse")
}

func handleR53DeactivateKeySigningKey(w http.ResponseWriter, r *http.Request) {
	r53SetKSKStatus(w, r, "INACTIVE", "DeactivateKeySigningKeyResponse")
}

func handleR53EnableHostedZoneDNSSEC(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	state := r53DNSSECState(zoneID)
	state.ServeSignature = "SIGNING"
	r53DNSSEC.Put(zoneID, state)
	r53NewKSKChange(w, "EnableHostedZoneDNSSECResponse")
}

func handleR53DisableHostedZoneDNSSEC(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	state := r53DNSSECState(zoneID)
	state.ServeSignature = "NOT_SIGNING"
	r53DNSSEC.Put(zoneID, state)
	r53NewKSKChange(w, "DisableHostedZoneDNSSECResponse")
}

func handleR53GetDNSSEC(w http.ResponseWriter, r *http.Request) {
	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	state := r53DNSSECState(zoneID)
	serve := state.ServeSignature
	if serve == "" {
		serve = "NOT_SIGNING"
	}
	names := make([]string, 0, len(state.KSKs))
	for name := range state.KSKs {
		names = append(names, name)
	}
	sort.Strings(names)
	ksks := make([]R53KeySigningKey, 0, len(names))
	for _, name := range names {
		ksks = append(ksks, state.KSKs[name])
	}
	r53WriteXML(w, http.StatusOK, R53GetDNSSECResponse{
		Xmlns:          r53Namespace,
		Status:         R53DNSSECStatus{ServeSignature: serve},
		KeySigningKeys: ksks,
	})
}

type R53TrafficPolicyInstance struct {
	XMLName              xml.Name `xml:"TrafficPolicyInstance"`
	Xmlns                string   `xml:"xmlns,attr,omitempty"`
	Id                   string   `xml:"Id"`
	HostedZoneId         string   `xml:"HostedZoneId"`
	Name                 string   `xml:"Name"`
	TTL                  int64    `xml:"TTL"`
	State                string   `xml:"State"`
	Message              string   `xml:"Message"`
	TrafficPolicyId      string   `xml:"TrafficPolicyId"`
	TrafficPolicyVersion int      `xml:"TrafficPolicyVersion"`
	TrafficPolicyType    string   `xml:"TrafficPolicyType"`
}

type R53CreateTrafficPolicyInstanceRequest struct {
	XMLName              xml.Name `xml:"CreateTrafficPolicyInstanceRequest"`
	HostedZoneId         string   `xml:"HostedZoneId"`
	Name                 string   `xml:"Name"`
	TTL                  int64    `xml:"TTL"`
	TrafficPolicyId      string   `xml:"TrafficPolicyId"`
	TrafficPolicyVersion int      `xml:"TrafficPolicyVersion"`
}

type R53CreateTrafficPolicyInstanceResponse struct {
	XMLName               xml.Name                 `xml:"CreateTrafficPolicyInstanceResponse"`
	Xmlns                 string                   `xml:"xmlns,attr,omitempty"`
	TrafficPolicyInstance R53TrafficPolicyInstance `xml:"TrafficPolicyInstance"`
}

type R53GetTrafficPolicyInstanceResponse struct {
	XMLName               xml.Name                 `xml:"GetTrafficPolicyInstanceResponse"`
	Xmlns                 string                   `xml:"xmlns,attr,omitempty"`
	TrafficPolicyInstance R53TrafficPolicyInstance `xml:"TrafficPolicyInstance"`
}

type R53UpdateTrafficPolicyInstanceRequest struct {
	XMLName              xml.Name `xml:"UpdateTrafficPolicyInstanceRequest"`
	TTL                  int64    `xml:"TTL"`
	TrafficPolicyId      string   `xml:"TrafficPolicyId"`
	TrafficPolicyVersion int      `xml:"TrafficPolicyVersion"`
}

type R53UpdateTrafficPolicyInstanceResponse struct {
	XMLName               xml.Name                 `xml:"UpdateTrafficPolicyInstanceResponse"`
	Xmlns                 string                   `xml:"xmlns,attr,omitempty"`
	TrafficPolicyInstance R53TrafficPolicyInstance `xml:"TrafficPolicyInstance"`
}

type R53ListTrafficPolicyInstancesResponse struct {
	XMLName                         xml.Name                   `xml:"ListTrafficPolicyInstancesResponse"`
	Xmlns                           string                     `xml:"xmlns,attr,omitempty"`
	TrafficPolicyInstances          []R53TrafficPolicyInstance `xml:"TrafficPolicyInstances>TrafficPolicyInstance"`
	HostedZoneIdMarker              string                     `xml:"HostedZoneIdMarker,omitempty"`
	TrafficPolicyInstanceNameMarker string                     `xml:"TrafficPolicyInstanceNameMarker,omitempty"`
	TrafficPolicyInstanceTypeMarker string                     `xml:"TrafficPolicyInstanceTypeMarker,omitempty"`
	IsTruncated                     bool                       `xml:"IsTruncated"`
	MaxItems                        string                     `xml:"MaxItems"`
}

type R53ListTrafficPolicyInstancesByHostedZoneResponse struct {
	XMLName                         xml.Name                   `xml:"ListTrafficPolicyInstancesByHostedZoneResponse"`
	Xmlns                           string                     `xml:"xmlns,attr,omitempty"`
	TrafficPolicyInstances          []R53TrafficPolicyInstance `xml:"TrafficPolicyInstances>TrafficPolicyInstance"`
	TrafficPolicyInstanceNameMarker string                     `xml:"TrafficPolicyInstanceNameMarker,omitempty"`
	TrafficPolicyInstanceTypeMarker string                     `xml:"TrafficPolicyInstanceTypeMarker,omitempty"`
	IsTruncated                     bool                       `xml:"IsTruncated"`
	MaxItems                        string                     `xml:"MaxItems"`
}

type R53ListTrafficPolicyInstancesByPolicyResponse struct {
	XMLName                         xml.Name                   `xml:"ListTrafficPolicyInstancesByPolicyResponse"`
	Xmlns                           string                     `xml:"xmlns,attr,omitempty"`
	TrafficPolicyInstances          []R53TrafficPolicyInstance `xml:"TrafficPolicyInstances>TrafficPolicyInstance"`
	HostedZoneIdMarker              string                     `xml:"HostedZoneIdMarker,omitempty"`
	TrafficPolicyInstanceNameMarker string                     `xml:"TrafficPolicyInstanceNameMarker,omitempty"`
	TrafficPolicyInstanceTypeMarker string                     `xml:"TrafficPolicyInstanceTypeMarker,omitempty"`
	IsTruncated                     bool                       `xml:"IsTruncated"`
	MaxItems                        string                     `xml:"MaxItems"`
}

type R53GetTrafficPolicyInstanceCountResponse struct {
	XMLName                    xml.Name `xml:"GetTrafficPolicyInstanceCountResponse"`
	Xmlns                      string   `xml:"xmlns,attr,omitempty"`
	TrafficPolicyInstanceCount int      `xml:"TrafficPolicyInstanceCount"`
}

func handleR53CreateTrafficPolicyInstance(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	var req R53CreateTrafficPolicyInstanceRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	zoneID := r53ZoneIDFromPath(req.HostedZoneId)
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	policy, ok := r53TrafficPolicies.Get(req.TrafficPolicyId)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy", "No traffic policy exists with ID "+req.TrafficPolicyId)
		return
	}
	tp, ok := policy.Versions[req.TrafficPolicyVersion]
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy",
			"No traffic policy version "+strconv.Itoa(req.TrafficPolicyVersion)+" exists with ID "+req.TrafficPolicyId)
		return
	}
	id := r53UUID()
	inst := R53TrafficPolicyInstance{
		Xmlns:                r53Namespace,
		Id:                   id,
		HostedZoneId:         zoneID,
		Name:                 r53NormalizeName(req.Name),
		TTL:                  req.TTL,
		State:                "Applied",
		Message:              "",
		TrafficPolicyId:      req.TrafficPolicyId,
		TrafficPolicyVersion: req.TrafficPolicyVersion,
		TrafficPolicyType:    tp.Type,
	}
	r53PolicyInstances.Put(id, r53StoredTrafficPolicyInstance{Instance: inst})
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/trafficpolicyinstance/"+id)
	r53WriteXML(w, http.StatusCreated, R53CreateTrafficPolicyInstanceResponse{Xmlns: r53Namespace, TrafficPolicyInstance: inst})
}

func handleR53GetTrafficPolicyInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := r53PolicyInstances.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicyInstance", "No traffic policy instance exists with ID "+id)
		return
	}
	r53WriteXML(w, http.StatusOK, R53GetTrafficPolicyInstanceResponse{Xmlns: r53Namespace, TrafficPolicyInstance: stored.Instance})
}

func handleR53UpdateTrafficPolicyInstance(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	id := r.PathValue("id")
	stored, ok := r53PolicyInstances.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicyInstance", "No traffic policy instance exists with ID "+id)
		return
	}
	var req R53UpdateTrafficPolicyInstanceRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	policy, ok := r53TrafficPolicies.Get(req.TrafficPolicyId)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy", "No traffic policy exists with ID "+req.TrafficPolicyId)
		return
	}
	tp, ok := policy.Versions[req.TrafficPolicyVersion]
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy",
			"No traffic policy version "+strconv.Itoa(req.TrafficPolicyVersion)+" exists with ID "+req.TrafficPolicyId)
		return
	}
	stored.Instance.TTL = req.TTL
	stored.Instance.TrafficPolicyId = req.TrafficPolicyId
	stored.Instance.TrafficPolicyVersion = req.TrafficPolicyVersion
	stored.Instance.TrafficPolicyType = tp.Type
	stored.Instance.State = "Applied"
	r53PolicyInstances.Put(id, stored)
	r53WriteXML(w, http.StatusOK, R53UpdateTrafficPolicyInstanceResponse{Xmlns: r53Namespace, TrafficPolicyInstance: stored.Instance})
}

func handleR53DeleteTrafficPolicyInstance(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	id := r.PathValue("id")
	if _, ok := r53PolicyInstances.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicyInstance", "No traffic policy instance exists with ID "+id)
		return
	}
	r53PolicyInstances.Delete(id)
	r53WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"DeleteTrafficPolicyInstanceResponse"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
	}{Xmlns: r53Namespace})
}

func r53AllPolicyInstances() []R53TrafficPolicyInstance {
	items := []R53TrafficPolicyInstance{}
	for _, sc := range r53PolicyInstances.List() {
		items = append(items, sc.Instance)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Id < items[j].Id })
	return items
}

func handleR53ListTrafficPolicyInstances(w http.ResponseWriter, r *http.Request) {
	maxItems := r53ParseMaxItems(r, 100)
	r53WriteXML(w, http.StatusOK, R53ListTrafficPolicyInstancesResponse{
		Xmlns:                  r53Namespace,
		TrafficPolicyInstances: r53AllPolicyInstances(),
		IsTruncated:            false,
		MaxItems:               strconv.Itoa(maxItems),
	})
}

func handleR53ListTrafficPolicyInstancesByHostedZone(w http.ResponseWriter, r *http.Request) {
	zoneID := r53ZoneIDFromPath(r.URL.Query().Get("id"))
	if zoneID == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "id (hosted zone) is required")
		return
	}
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	maxItems := r53ParseMaxItems(r, 100)
	items := []R53TrafficPolicyInstance{}
	for _, inst := range r53AllPolicyInstances() {
		if inst.HostedZoneId == zoneID {
			items = append(items, inst)
		}
	}
	r53WriteXML(w, http.StatusOK, R53ListTrafficPolicyInstancesByHostedZoneResponse{
		Xmlns:                  r53Namespace,
		TrafficPolicyInstances: items,
		IsTruncated:            false,
		MaxItems:               strconv.Itoa(maxItems),
	})
}

func handleR53ListTrafficPolicyInstancesByPolicy(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	policyID := q.Get("id")
	if policyID == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "id (traffic policy) is required")
		return
	}
	version := q.Get("version")
	maxItems := r53ParseMaxItems(r, 100)
	items := []R53TrafficPolicyInstance{}
	for _, inst := range r53AllPolicyInstances() {
		if inst.TrafficPolicyId != policyID {
			continue
		}
		if version != "" && strconv.Itoa(inst.TrafficPolicyVersion) != version {
			continue
		}
		items = append(items, inst)
	}
	r53WriteXML(w, http.StatusOK, R53ListTrafficPolicyInstancesByPolicyResponse{
		Xmlns:                  r53Namespace,
		TrafficPolicyInstances: items,
		IsTruncated:            false,
		MaxItems:               strconv.Itoa(maxItems),
	})
}

func handleR53GetTrafficPolicyInstanceCount(w http.ResponseWriter, _ *http.Request) {
	r53WriteXML(w, http.StatusOK, R53GetTrafficPolicyInstanceCountResponse{
		Xmlns:                      r53Namespace,
		TrafficPolicyInstanceCount: len(r53PolicyInstances.List()),
	})
}

type R53UpdateTrafficPolicyCommentRequest struct {
	XMLName xml.Name `xml:"UpdateTrafficPolicyCommentRequest"`
	Comment string   `xml:"Comment"`
}

type R53UpdateTrafficPolicyCommentResponse struct {
	XMLName       xml.Name         `xml:"UpdateTrafficPolicyCommentResponse"`
	Xmlns         string           `xml:"xmlns,attr,omitempty"`
	TrafficPolicy R53TrafficPolicy `xml:"TrafficPolicy"`
}

func handleR53UpdateTrafficPolicyComment(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	id := r.PathValue("id")
	ver, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "Version must be an integer")
		return
	}
	stored, ok := r53TrafficPolicies.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy", "No traffic policy exists with ID "+id)
		return
	}
	tp, ok := stored.Versions[ver]
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy", "No traffic policy version exists with ID "+id+" and version "+strconv.Itoa(ver))
		return
	}
	var req R53UpdateTrafficPolicyCommentRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	tp.Comment = req.Comment
	stored.Versions[ver] = tp
	r53TrafficPolicies.Put(id, stored)
	r53WriteXML(w, http.StatusOK, R53UpdateTrafficPolicyCommentResponse{Xmlns: r53Namespace, TrafficPolicy: tp})
}

type R53CreateVPCAssociationAuthorizationRequest struct {
	XMLName xml.Name `xml:"CreateVPCAssociationAuthorizationRequest"`
	VPC     R53VPC   `xml:"VPC"`
}

type R53CreateVPCAssociationAuthorizationResponse struct {
	XMLName      xml.Name `xml:"CreateVPCAssociationAuthorizationResponse"`
	Xmlns        string   `xml:"xmlns,attr,omitempty"`
	HostedZoneId string   `xml:"HostedZoneId"`
	VPC          R53VPC   `xml:"VPC"`
}

type R53DeleteVPCAssociationAuthorizationRequest struct {
	XMLName xml.Name `xml:"DeleteVPCAssociationAuthorizationRequest"`
	VPC     R53VPC   `xml:"VPC"`
}

type R53ListVPCAssociationAuthorizationsResponse struct {
	XMLName      xml.Name `xml:"ListVPCAssociationAuthorizationsResponse"`
	Xmlns        string   `xml:"xmlns,attr,omitempty"`
	HostedZoneId string   `xml:"HostedZoneId"`
	NextToken    string   `xml:"NextToken,omitempty"`
	VPCs         []R53VPC `xml:"VPCs>VPC"`
}

func r53VPCAuthzList(zoneID string) []R53VPC {
	list, _ := r53VPCAuthz.Get(zoneID)
	return list
}

func handleR53CreateVPCAssociationAuthorization(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	var req R53CreateVPCAssociationAuthorizationRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	list := r53VPCAuthzList(zoneID)
	exists := false
	for _, v := range list {
		if v.VPCId == req.VPC.VPCId {
			exists = true
			break
		}
	}
	if !exists {
		list = append(list, req.VPC)
	}
	r53VPCAuthz.Put(zoneID, list)
	r53WriteXML(w, http.StatusOK, R53CreateVPCAssociationAuthorizationResponse{
		Xmlns:        r53Namespace,
		HostedZoneId: zoneID,
		VPC:          req.VPC,
	})
}

func handleR53DeleteVPCAssociationAuthorization(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	var req R53DeleteVPCAssociationAuthorizationRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	list := r53VPCAuthzList(zoneID)
	out := make([]R53VPC, 0, len(list))
	for _, v := range list {
		if v.VPCId != req.VPC.VPCId {
			out = append(out, v)
		}
	}
	r53VPCAuthz.Put(zoneID, out)
	r53WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"DeleteVPCAssociationAuthorizationResponse"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
	}{Xmlns: r53Namespace})
}

func handleR53ListVPCAssociationAuthorizations(w http.ResponseWriter, r *http.Request) {
	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	list := r53VPCAuthzList(zoneID)
	r53WriteXML(w, http.StatusOK, R53ListVPCAssociationAuthorizationsResponse{
		Xmlns:        r53Namespace,
		HostedZoneId: zoneID,
		VPCs:         list,
	})
}

type R53AccountLimit struct {
	Type  string `xml:"Type"`
	Value int64  `xml:"Value"`
}

type R53GetAccountLimitResponse struct {
	XMLName xml.Name        `xml:"GetAccountLimitResponse"`
	Xmlns   string          `xml:"xmlns,attr,omitempty"`
	Limit   R53AccountLimit `xml:"Limit"`
	Count   int64           `xml:"Count"`
}

type R53HostedZoneLimit struct {
	Type  string `xml:"Type"`
	Value int64  `xml:"Value"`
}

type R53GetHostedZoneLimitResponse struct {
	XMLName xml.Name           `xml:"GetHostedZoneLimitResponse"`
	Xmlns   string             `xml:"xmlns,attr,omitempty"`
	Limit   R53HostedZoneLimit `xml:"Limit"`
	Count   int64              `xml:"Count"`
}

// r53AccountLimits holds the real default Route 53 per-account limits.
var r53AccountLimits = map[string]int64{
	"MAX_HEALTH_CHECKS_BY_OWNER":            200,
	"MAX_HOSTED_ZONES_BY_OWNER":             500,
	"MAX_TRAFFIC_POLICY_INSTANCES_BY_OWNER": 50,
	"MAX_REUSABLE_DELEGATION_SETS_BY_OWNER": 100,
	"MAX_TRAFFIC_POLICIES_BY_OWNER":         50,
}

func handleR53GetAccountLimit(w http.ResponseWriter, r *http.Request) {
	limitType := r.PathValue("type")
	value, ok := r53AccountLimits[limitType]
	if !ok {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "Unknown account limit type: "+limitType)
		return
	}
	var count int64
	switch limitType {
	case "MAX_HEALTH_CHECKS_BY_OWNER":
		count = int64(len(r53HealthChecks.List()))
	case "MAX_HOSTED_ZONES_BY_OWNER":
		count = int64(len(r53Zones.List()))
	case "MAX_TRAFFIC_POLICY_INSTANCES_BY_OWNER":
		count = int64(len(r53PolicyInstances.List()))
	case "MAX_REUSABLE_DELEGATION_SETS_BY_OWNER":
		count = int64(len(r53DelegationSets.List()))
	case "MAX_TRAFFIC_POLICIES_BY_OWNER":
		count = int64(len(r53TrafficPolicies.List()))
	}
	r53WriteXML(w, http.StatusOK, R53GetAccountLimitResponse{
		Xmlns: r53Namespace,
		Limit: R53AccountLimit{Type: limitType, Value: value},
		Count: count,
	})
}

func handleR53GetHostedZoneLimit(w http.ResponseWriter, r *http.Request) {
	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	stored, ok := r53Zones.Get(zoneID)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	limitType := r.PathValue("type")
	var value, count int64
	switch limitType {
	case "MAX_RRSETS_BY_ZONE":
		value = 10000
		count = int64(stored.Zone.ResourceRecordSetCount)
	case "MAX_VPCS_ASSOCIATED_BY_ZONE":
		value = 100
		count = int64(len(r53ZoneVPCList(zoneID)))
	default:
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "Unknown hosted zone limit type: "+limitType)
		return
	}
	r53WriteXML(w, http.StatusOK, R53GetHostedZoneLimitResponse{
		Xmlns: r53Namespace,
		Limit: R53HostedZoneLimit{Type: limitType, Value: value},
		Count: count,
	})
}

type R53GetCheckerIpRangesResponse struct {
	XMLName         xml.Name `xml:"GetCheckerIpRangesResponse"`
	Xmlns           string   `xml:"xmlns,attr,omitempty"`
	CheckerIpRanges []string `xml:"CheckerIpRanges>member"`
}

// r53CheckerIpRanges is a representative subset of the real Route 53 health
// checker IP ranges (static cloud metadata, the same on every account).
var r53CheckerIpRanges = []string{
	"15.177.0.0/18",
	"54.183.255.128/26",
	"54.228.16.0/26",
	"54.232.40.64/26",
	"54.241.32.64/26",
	"54.243.31.192/26",
	"54.244.52.192/26",
	"54.245.168.0/26",
	"54.248.220.0/26",
	"54.250.253.192/26",
}

func handleR53GetCheckerIpRanges(w http.ResponseWriter, _ *http.Request) {
	r53WriteXML(w, http.StatusOK, R53GetCheckerIpRangesResponse{
		Xmlns:           r53Namespace,
		CheckerIpRanges: r53CheckerIpRanges,
	})
}

func handleR53GetHealthCheckLastFailureReason(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := r53HealthChecks.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHealthCheck", "A health check with id "+id+" does not exist.")
		return
	}
	// A passing health check has no failure observation. Real Route 53
	// returns an empty observation list in that case.
	r53WriteXML(w, http.StatusOK, struct {
		XMLName                 xml.Name                    `xml:"GetHealthCheckLastFailureReasonResponse"`
		Xmlns                   string                      `xml:"xmlns,attr,omitempty"`
		HealthCheckObservations []R53HealthCheckObservation `xml:"HealthCheckObservations>HealthCheckObservation"`
	}{Xmlns: r53Namespace, HealthCheckObservations: []R53HealthCheckObservation{}})
}

type R53ListTagsForResourcesRequest struct {
	XMLName     xml.Name `xml:"ListTagsForResourcesRequest"`
	ResourceIds []string `xml:"ResourceIds>ResourceId"`
}

type R53ListTagsForResourcesResponse struct {
	XMLName         xml.Name            `xml:"ListTagsForResourcesResponse"`
	Xmlns           string              `xml:"xmlns,attr,omitempty"`
	ResourceTagSets []R53ResourceTagSet `xml:"ResourceTagSets>ResourceTagSet"`
}

func handleR53ListTagsForResources(w http.ResponseWriter, r *http.Request) {
	rtype := r.PathValue("resourceType")
	var req R53ListTagsForResourcesRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	sets := make([]R53ResourceTagSet, 0, len(req.ResourceIds))
	for _, rid := range req.ResourceIds {
		tags, _ := r53Tags.Get(r53TagKey(rtype, rid))
		sets = append(sets, R53ResourceTagSet{ResourceType: rtype, ResourceId: rid, Tags: tags})
	}
	r53WriteXML(w, http.StatusOK, R53ListTagsForResourcesResponse{Xmlns: r53Namespace, ResourceTagSets: sets})
}

type R53TestDNSAnswerResponse struct {
	XMLName      xml.Name `xml:"TestDNSAnswerResponse"`
	Xmlns        string   `xml:"xmlns,attr,omitempty"`
	Nameserver   string   `xml:"Nameserver"`
	RecordName   string   `xml:"RecordName"`
	RecordType   string   `xml:"RecordType"`
	RecordData   []string `xml:"RecordData>RecordDataEntry"`
	ResponseCode string   `xml:"ResponseCode"`
	Protocol     string   `xml:"Protocol"`
}

func handleR53TestDNSAnswer(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	zoneID := r53ZoneIDFromPath(q.Get("hostedzoneid"))
	recordName := r53NormalizeName(q.Get("recordname"))
	recordType := strings.ToUpper(q.Get("recordtype"))
	if zoneID == "" || recordName == "" || recordType == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "hostedzoneid, recordname and recordtype are required")
		return
	}
	stored, ok := r53Zones.Get(zoneID)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	data := []string{}
	responseCode := "NXDOMAIN"
	for _, rr := range stored.Records {
		if strings.EqualFold(rr.Name, recordName) && strings.EqualFold(rr.Type, recordType) {
			responseCode = "NOERROR"
			if rr.ResourceRecords != nil {
				for _, v := range rr.ResourceRecords.Items {
					data = append(data, v.Value)
				}
			}
			break
		}
	}
	r53WriteXML(w, http.StatusOK, R53TestDNSAnswerResponse{
		Xmlns:        r53Namespace,
		Nameserver:   "ns-2048.awsdns-64.com",
		RecordName:   recordName,
		RecordType:   recordType,
		RecordData:   data,
		ResponseCode: responseCode,
		Protocol:     "UDP",
	})
}

type R53UpdateHostedZoneCommentRequest struct {
	XMLName xml.Name `xml:"UpdateHostedZoneCommentRequest"`
	Comment string   `xml:"Comment"`
}

type R53UpdateHostedZoneCommentResponse struct {
	XMLName    xml.Name      `xml:"UpdateHostedZoneCommentResponse"`
	Xmlns      string        `xml:"xmlns,attr,omitempty"`
	HostedZone R53HostedZone `xml:"HostedZone"`
}

func handleR53UpdateHostedZoneComment(w http.ResponseWriter, r *http.Request) {
	r53MoreMu.Lock()
	defer r53MoreMu.Unlock()

	id := r53ZoneIDFromPath(r.PathValue("id"))
	stored, ok := r53Zones.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	var req R53UpdateHostedZoneCommentRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	if stored.Zone.Config == nil {
		stored.Zone.Config = &R53HostedZoneConfig{}
	}
	stored.Zone.Config.Comment = req.Comment
	r53Zones.Put(id, stored)
	r53WriteXML(w, http.StatusOK, R53UpdateHostedZoneCommentResponse{Xmlns: r53Namespace, HostedZone: stored.Zone})
}

func handleR53UpdateHostedZoneFeatures(w http.ResponseWriter, r *http.Request) {
	zoneID := r53ZoneIDFromPath(r.PathValue("hostedZoneId"))
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	// Accelerated-recovery toggle; the smithy response has no body members.
	r53WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"UpdateHostedZoneFeaturesResponse"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
	}{Xmlns: r53Namespace})
}

func r53ParseMaxItems(r *http.Request, def int) int {
	if raw := r.URL.Query().Get("maxitems"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			return parsed
		}
	}
	return def
}

// r53UUID returns a UUID-shaped id for CIDR collections and traffic policy
// instances (real Route 53 uses UUIDs for these).
func r53UUID() string {
	h := strings.ToLower(strings.TrimPrefix(r53RandomID(), "Z")) +
		strings.ToLower(strings.TrimPrefix(r53ChangeID(), "C"))
	for len(h) < 32 {
		h += "0"
	}
	h = h[:32]
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
