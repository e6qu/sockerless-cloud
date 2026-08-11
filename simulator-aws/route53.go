package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Route 53 — REST + XML, path version /2013-04-01/, namespace
// https://route53.amazonaws.com/doc/2013-04-01/. Same protocol family
// as CloudFront and S3 (sim already uses encoding/xml for both).
// CloudFront's reserved hosted-zone ID is Z2FDTNDATAQYW2 — when
// aws_route53_record's alias { name = aws_cloudfront_distribution.x.domain_name,
// zone_id = aws_cloudfront_distribution.x.hosted_zone_id } resolves at
// apply time, that's the Z2FDTNDATAQYW2 the SDK sends.

const (
	r53APIVersion = "2013-04-01"
	r53Namespace  = "https://route53.amazonaws.com/doc/2013-04-01/"
)

// ---------- HostedZone ----------

type R53HostedZone struct {
	XMLName                xml.Name             `xml:"HostedZone"`
	Xmlns                  string               `xml:"xmlns,attr,omitempty"`
	Id                     string               `xml:"Id"`
	Name                   string               `xml:"Name"`
	CallerReference        string               `xml:"CallerReference"`
	Config                 *R53HostedZoneConfig `xml:"Config,omitempty"`
	ResourceRecordSetCount int                  `xml:"ResourceRecordSetCount"`
	LinkedService          *R53LinkedService    `xml:"LinkedService,omitempty"`
}

type R53HostedZoneConfig struct {
	Comment     string `xml:"Comment,omitempty"`
	PrivateZone bool   `xml:"PrivateZone"`
}

type R53LinkedService struct {
	ServicePrincipal string `xml:"ServicePrincipal"`
	Description      string `xml:"Description"`
}

type R53HostedZoneSummary R53HostedZone

type R53HostedZoneList struct {
	XMLName     xml.Name        `xml:"ListHostedZonesResponse"`
	Xmlns       string          `xml:"xmlns,attr,omitempty"`
	HostedZones []R53HostedZone `xml:"HostedZones>HostedZone"`
	Marker      string          `xml:"Marker"`
	IsTruncated bool            `xml:"IsTruncated"`
	MaxItems    string          `xml:"MaxItems"`
	NextMarker  string          `xml:"NextMarker,omitempty"`
}

type R53HostedZoneByNameList struct {
	XMLName          xml.Name        `xml:"ListHostedZonesByNameResponse"`
	Xmlns            string          `xml:"xmlns,attr,omitempty"`
	HostedZones      []R53HostedZone `xml:"HostedZones>HostedZone"`
	DNSName          string          `xml:"DNSName,omitempty"`
	HostedZoneId     string          `xml:"HostedZoneId,omitempty"`
	IsTruncated      bool            `xml:"IsTruncated"`
	NextDNSName      string          `xml:"NextDNSName,omitempty"`
	NextHostedZoneId string          `xml:"NextHostedZoneId,omitempty"`
	MaxItems         string          `xml:"MaxItems"`
}

type R53CreateHostedZoneRequest struct {
	XMLName          xml.Name             `xml:"CreateHostedZoneRequest"`
	Name             string               `xml:"Name"`
	CallerReference  string               `xml:"CallerReference"`
	HostedZoneConfig *R53HostedZoneConfig `xml:"HostedZoneConfig,omitempty"`
	VPC              *R53VPC              `xml:"VPC,omitempty"`
	DelegationSetId  string               `xml:"DelegationSetId,omitempty"`
}

type R53VPC struct {
	VPCRegion string `xml:"VPCRegion"`
	VPCId     string `xml:"VPCId"`
}

type R53CreateHostedZoneResponse struct {
	XMLName       xml.Name         `xml:"CreateHostedZoneResponse"`
	Xmlns         string           `xml:"xmlns,attr,omitempty"`
	HostedZone    R53HostedZone    `xml:"HostedZone"`
	ChangeInfo    R53ChangeInfo    `xml:"ChangeInfo"`
	DelegationSet R53DelegationSet `xml:"DelegationSet"`
}

type R53DelegationSet struct {
	NameServers []string `xml:"NameServers>NameServer"`
}

type R53GetHostedZoneResponse struct {
	XMLName       xml.Name         `xml:"GetHostedZoneResponse"`
	Xmlns         string           `xml:"xmlns,attr,omitempty"`
	HostedZone    R53HostedZone    `xml:"HostedZone"`
	DelegationSet R53DelegationSet `xml:"DelegationSet"`
	VPCs          []R53VPC         `xml:"VPCs>VPC,omitempty"`
}

// ---------- ResourceRecordSet ----------

type R53ResourceRecordSet struct {
	Name                    string              `xml:"Name"`
	Type                    string              `xml:"Type"`
	SetIdentifier           string              `xml:"SetIdentifier,omitempty"`
	Weight                  *int64              `xml:"Weight,omitempty"`
	Region                  string              `xml:"Region,omitempty"`
	GeoLocation             *R53GeoLocation     `xml:"GeoLocation,omitempty"`
	Failover                string              `xml:"Failover,omitempty"`
	MultiValueAnswer        *bool               `xml:"MultiValueAnswer,omitempty"`
	TTL                     *int64              `xml:"TTL,omitempty"`
	ResourceRecords         *R53ResourceRecords `xml:"ResourceRecords,omitempty"`
	AliasTarget             *R53AliasTarget     `xml:"AliasTarget,omitempty"`
	HealthCheckId           string              `xml:"HealthCheckId,omitempty"`
	TrafficPolicyInstanceId string              `xml:"TrafficPolicyInstanceId,omitempty"`
	CidrRoutingConfig       *R53CidrRouting     `xml:"CidrRoutingConfig,omitempty"`
}

type R53ResourceRecords struct {
	Items []R53ResourceRecord `xml:"ResourceRecord"`
}

type R53ResourceRecord struct {
	Value string `xml:"Value"`
}

type R53AliasTarget struct {
	HostedZoneId         string `xml:"HostedZoneId"`
	DNSName              string `xml:"DNSName"`
	EvaluateTargetHealth bool   `xml:"EvaluateTargetHealth"`
}

type R53GeoLocation struct {
	ContinentCode   string `xml:"ContinentCode,omitempty"`
	CountryCode     string `xml:"CountryCode,omitempty"`
	SubdivisionCode string `xml:"SubdivisionCode,omitempty"`
}

type R53CidrRouting struct {
	CollectionId string `xml:"CollectionId"`
	LocationName string `xml:"LocationName"`
}

type R53Change struct {
	Action            string               `xml:"Action"`
	ResourceRecordSet R53ResourceRecordSet `xml:"ResourceRecordSet"`
}

type R53ChangeBatch struct {
	Comment string      `xml:"Comment,omitempty"`
	Changes []R53Change `xml:"Changes>Change"`
}

type R53ChangeRRSetRequest struct {
	XMLName     xml.Name       `xml:"ChangeResourceRecordSetsRequest"`
	ChangeBatch R53ChangeBatch `xml:"ChangeBatch"`
}

type R53ChangeInfo struct {
	Id          string `xml:"Id"`
	Status      string `xml:"Status"`
	SubmittedAt string `xml:"SubmittedAt"`
	Comment     string `xml:"Comment,omitempty"`
}

type R53ChangeResourceRecordSetsResponse struct {
	XMLName    xml.Name      `xml:"ChangeResourceRecordSetsResponse"`
	Xmlns      string        `xml:"xmlns,attr,omitempty"`
	ChangeInfo R53ChangeInfo `xml:"ChangeInfo"`
}

type R53ListResourceRecordSetsResponse struct {
	XMLName              xml.Name               `xml:"ListResourceRecordSetsResponse"`
	Xmlns                string                 `xml:"xmlns,attr,omitempty"`
	ResourceRecordSets   []R53ResourceRecordSet `xml:"ResourceRecordSets>ResourceRecordSet"`
	IsTruncated          bool                   `xml:"IsTruncated"`
	MaxItems             string                 `xml:"MaxItems"`
	NextRecordName       string                 `xml:"NextRecordName,omitempty"`
	NextRecordType       string                 `xml:"NextRecordType,omitempty"`
	NextRecordIdentifier string                 `xml:"NextRecordIdentifier,omitempty"`
}

type R53GetChangeResponse struct {
	XMLName    xml.Name      `xml:"GetChangeResponse"`
	Xmlns      string        `xml:"xmlns,attr,omitempty"`
	ChangeInfo R53ChangeInfo `xml:"ChangeInfo"`
}

type R53DeleteHostedZoneResponse struct {
	XMLName    xml.Name      `xml:"DeleteHostedZoneResponse"`
	Xmlns      string        `xml:"xmlns,attr,omitempty"`
	ChangeInfo R53ChangeInfo `xml:"ChangeInfo"`
}

// ---------- Error ----------

type R53ErrorResponse struct {
	XMLName   xml.Name `xml:"ErrorResponse"`
	Xmlns     string   `xml:"xmlns,attr,omitempty"`
	Error     R53Error `xml:"Error"`
	RequestId string   `xml:"RequestId"`
}

type R53Error struct {
	Type    string `xml:"Type,omitempty"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// ---------- Storage ----------

type r53StoredZone struct {
	Zone    R53HostedZone
	Records []R53ResourceRecordSet // keyed by (Name, Type, SetIdentifier)
}

type r53StoredChange struct {
	Info R53ChangeInfo
}

var (
	r53Zones   sim.Store[r53StoredZone]
	r53Changes sim.Store[r53StoredChange]
	r53Tags    sim.Store[[]R53Tag]
	r53Mu      sync.Mutex // serialises ChangeResourceRecordSets
)

// ---------- Helpers ----------

func r53RandomID() string {
	buf := make([]byte, 7)
	_, _ = rand.Read(buf)
	return "Z" + strings.ToUpper(hex.EncodeToString(buf))
}

func r53ChangeID() string {
	buf := make([]byte, 7)
	_, _ = rand.Read(buf)
	return "C" + strings.ToUpper(hex.EncodeToString(buf))
}

func r53NowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func r53ZoneIDFromPath(p string) string {
	// AWS accepts either "Z123..." or "/hostedzone/Z123..."
	if strings.HasPrefix(p, "/hostedzone/") {
		return strings.TrimPrefix(p, "/hostedzone/")
	}
	return p
}

func r53NormalizeName(name string) string {
	if name == "" || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

func r53HostedZoneByNameSortKey(name string) string {
	trimmed := strings.TrimSuffix(strings.ToLower(r53NormalizeName(name)), ".")
	if trimmed == "" {
		return ""
	}
	labels := strings.Split(trimmed, ".")
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return strings.Join(labels, ".") + "."
}

func r53WriteXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(v)
}

func r53WriteError(w http.ResponseWriter, status int, code, msg string) {
	r53WriteXML(w, status, R53ErrorResponse{
		Xmlns:     r53Namespace,
		Error:     R53Error{Type: "Sender", Code: code, Message: msg},
		RequestId: r53ChangeID(),
	})
}

// ---------- Registration ----------

func registerRoute53(srv *sim.Server, startDataPlane bool) {
	r53Zones = sim.MakeStore[r53StoredZone](srv.DB(), "route53_zones")
	r53Changes = sim.MakeStore[r53StoredChange](srv.DB(), "route53_changes")
	r53Tags = sim.MakeStore[[]R53Tag](srv.DB(), "route53_tags")

	mux := srv
	hostedZoneResource := cloudTrailRESTResource("AWS::Route53::HostedZone", "id", "resourceId")
	changeResource := cloudTrailRESTResource("AWS::Route53::Change", "id")
	mux.HandleFunc("POST /"+r53APIVersion+"/hostedzone", cloudTrailRecordedREST("CreateHostedZone", "route53.amazonaws.com", nil, handleR53CreateHostedZone))
	mux.HandleFunc("GET /"+r53APIVersion+"/hostedzone", cloudTrailRecordedREST("ListHostedZones", "route53.amazonaws.com", nil, handleR53ListHostedZones))
	mux.HandleFunc("GET /"+r53APIVersion+"/hostedzonesbyname", cloudTrailRecordedREST("ListHostedZonesByName", "route53.amazonaws.com", nil, handleR53ListHostedZonesByName))
	mux.HandleFunc("GET /"+r53APIVersion+"/hostedzone/{id}", cloudTrailRecordedREST("GetHostedZone", "route53.amazonaws.com", hostedZoneResource, handleR53GetHostedZone))
	mux.HandleFunc("DELETE /"+r53APIVersion+"/hostedzone/{id}", cloudTrailRecordedREST("DeleteHostedZone", "route53.amazonaws.com", hostedZoneResource, handleR53DeleteHostedZone))
	mux.HandleFunc("POST /"+r53APIVersion+"/hostedzone/{id}/rrset", cloudTrailRecordedREST("ChangeResourceRecordSets", "route53.amazonaws.com", hostedZoneResource, handleR53ChangeRRSets))
	mux.HandleFunc("POST /"+r53APIVersion+"/hostedzone/{id}/rrset/", cloudTrailRecordedREST("ChangeResourceRecordSets", "route53.amazonaws.com", hostedZoneResource, handleR53ChangeRRSets)) // CLI uses trailing slash
	mux.HandleFunc("GET /"+r53APIVersion+"/hostedzone/{id}/rrset", cloudTrailRecordedREST("ListResourceRecordSets", "route53.amazonaws.com", hostedZoneResource, handleR53ListRRSets))
	mux.HandleFunc("GET /"+r53APIVersion+"/hostedzone/{id}/rrset/", cloudTrailRecordedREST("ListResourceRecordSets", "route53.amazonaws.com", hostedZoneResource, handleR53ListRRSets))
	mux.HandleFunc("GET /"+r53APIVersion+"/change/{id}", cloudTrailRecordedREST("GetChange", "route53.amazonaws.com", changeResource, handleR53GetChange))

	// Tagging — path /2013-04-01/tags/{ResourceType}/{ResourceId}
	mux.HandleFunc("GET /"+r53APIVersion+"/tags/{resourceType}/{resourceId}", cloudTrailRecordedREST("ListTagsForResource", "route53.amazonaws.com", hostedZoneResource, handleR53ListTagsForResource))
	mux.HandleFunc("POST /"+r53APIVersion+"/tags/{resourceType}/{resourceId}", cloudTrailRecordedREST("ChangeTagsForResource", "route53.amazonaws.com", hostedZoneResource, handleR53ChangeTagsForResource))

	registerRoute53Extra(mux)
	registerRoute53More(mux)

	if startDataPlane {
		startRoute53DNSServer()
	}
}

// ---------- Tag types ----------

type R53Tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value,omitempty"`
}

type R53ChangeTagsRequest struct {
	XMLName xml.Name `xml:"ChangeTagsForResourceRequest"`
	AddTags struct {
		Items []R53Tag `xml:"Tag"`
	} `xml:"AddTags,omitempty"`
	RemoveTagKeys struct {
		Items []string `xml:"Key"`
	} `xml:"RemoveTagKeys,omitempty"`
}

type R53ResourceTagSet struct {
	ResourceType string   `xml:"ResourceType"`
	ResourceId   string   `xml:"ResourceId"`
	Tags         []R53Tag `xml:"Tags>Tag,omitempty"`
}

type R53ListTagsResponse struct {
	XMLName        xml.Name          `xml:"ListTagsForResourceResponse"`
	Xmlns          string            `xml:"xmlns,attr,omitempty"`
	ResourceTagSet R53ResourceTagSet `xml:"ResourceTagSet"`
}

type R53ChangeTagsResponse struct {
	XMLName xml.Name `xml:"ChangeTagsForResourceResponse"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
}

func r53TagKey(rtype, rid string) string { return rtype + "/" + rid }

func handleR53ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	rtype := r.PathValue("resourceType")
	rid := r.PathValue("resourceId")
	tags, _ := r53Tags.Get(r53TagKey(rtype, rid))
	r53WriteXML(w, http.StatusOK, R53ListTagsResponse{
		Xmlns: r53Namespace,
		ResourceTagSet: R53ResourceTagSet{
			ResourceType: rtype, ResourceId: rid, Tags: tags,
		},
	})
}

func handleR53ChangeTagsForResource(w http.ResponseWriter, r *http.Request) {
	rtype := r.PathValue("resourceType")
	rid := r.PathValue("resourceId")
	var req R53ChangeTagsRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	key := r53TagKey(rtype, rid)
	tags, _ := r53Tags.Get(key)
	tagMap := map[string]string{}
	for _, t := range tags {
		tagMap[t.Key] = t.Value
	}
	for _, t := range req.AddTags.Items {
		tagMap[t.Key] = t.Value
	}
	for _, k := range req.RemoveTagKeys.Items {
		delete(tagMap, k)
	}
	merged := make([]R53Tag, 0, len(tagMap))
	for k, v := range tagMap {
		merged = append(merged, R53Tag{Key: k, Value: v})
	}
	r53Tags.Put(key, merged)
	r53WriteXML(w, http.StatusOK, R53ChangeTagsResponse{Xmlns: r53Namespace})
}

// ---------- HostedZone handlers ----------

func handleR53CreateHostedZone(w http.ResponseWriter, r *http.Request) {
	var req R53CreateHostedZoneRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	if req.Name == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidDomainName", "Name is required")
		return
	}
	if req.CallerReference == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "CallerReference is required")
		return
	}
	if req.VPC != nil && (req.VPC.VPCId == "" || req.VPC.VPCRegion == "") {
		r53WriteError(w, http.StatusBadRequest, "InvalidVPCId", "VPCId and VPCRegion are required")
		return
	}
	// CallerReference is the idempotency key — a reused value (a safe retry of a
	// zone that already exists) returns HostedZoneAlreadyExists, not a duplicate.
	for _, sz := range r53Zones.List() {
		if sz.Zone.CallerReference == req.CallerReference {
			r53WriteError(w, http.StatusConflict, "HostedZoneAlreadyExists",
				"A hosted zone has already been created with the specified caller reference "+req.CallerReference+".")
			return
		}
	}
	name := r53NormalizeName(req.Name)
	id := r53RandomID()
	if req.VPC != nil {
		if req.HostedZoneConfig == nil {
			req.HostedZoneConfig = &R53HostedZoneConfig{}
		}
		req.HostedZoneConfig.PrivateZone = true
	}
	zone := R53HostedZone{
		Xmlns:                  r53Namespace,
		Id:                     "/hostedzone/" + id,
		Name:                   name,
		CallerReference:        req.CallerReference,
		Config:                 req.HostedZoneConfig,
		ResourceRecordSetCount: 2, // NS + SOA records auto-created
	}
	// Seed NS + SOA per real Route 53 behavior so ListResourceRecordSets
	// returns them on a zone-create round-trip.
	defaultRecords := []R53ResourceRecordSet{
		{
			Name: name,
			Type: "NS",
			TTL:  ptrInt64(172800),
			ResourceRecords: &R53ResourceRecords{Items: []R53ResourceRecord{
				{Value: "ns-1.awsdns-00.com."},
				{Value: "ns-2.awsdns-01.net."},
				{Value: "ns-3.awsdns-02.org."},
				{Value: "ns-4.awsdns-03.co.uk."},
			}},
		},
		{
			Name: name,
			Type: "SOA",
			TTL:  ptrInt64(900),
			ResourceRecords: &R53ResourceRecords{Items: []R53ResourceRecord{
				{Value: "ns-1.awsdns-00.com. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400"},
			}},
		},
	}
	r53Zones.Put(id, r53StoredZone{Zone: zone, Records: defaultRecords})
	if req.VPC != nil {
		r53ZoneVPCs.Put(id, []R53VPC{*req.VPC})
	}
	change := newR53Change("INSYNC", "Hosted zone created")
	r53Changes.Put(strings.TrimPrefix(change.Id, "/change/"), r53StoredChange{Info: change})
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/hostedzone/"+id)
	r53WriteXML(w, http.StatusCreated, R53CreateHostedZoneResponse{
		Xmlns:         r53Namespace,
		HostedZone:    zone,
		ChangeInfo:    change,
		DelegationSet: R53DelegationSet{NameServers: []string{"ns-1.awsdns-00.com", "ns-2.awsdns-01.net", "ns-3.awsdns-02.org", "ns-4.awsdns-03.co.uk"}},
	})
}

func ptrInt64(v int64) *int64 { return &v }

func newR53Change(status, comment string) R53ChangeInfo {
	return R53ChangeInfo{
		Id:          "/change/" + r53ChangeID(),
		Status:      status,
		SubmittedAt: r53NowISO(),
		Comment:     comment,
	}
}

func handleR53GetHostedZone(w http.ResponseWriter, r *http.Request) {
	id := r53ZoneIDFromPath(r.PathValue("id"))
	stored, ok := r53Zones.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	r53WriteXML(w, http.StatusOK, R53GetHostedZoneResponse{
		Xmlns:         r53Namespace,
		HostedZone:    stored.Zone,
		DelegationSet: R53DelegationSet{NameServers: []string{"ns-1.awsdns-00.com", "ns-2.awsdns-01.net", "ns-3.awsdns-02.org", "ns-4.awsdns-03.co.uk"}},
		VPCs:          r53ZoneVPCList(id),
	})
}

func handleR53DeleteHostedZone(w http.ResponseWriter, r *http.Request) {
	id := r53ZoneIDFromPath(r.PathValue("id"))
	stored, ok := r53Zones.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	// Real Route 53 requires the zone to have only NS + SOA records.
	userRecords := 0
	for _, rr := range stored.Records {
		if rr.Type != "NS" && rr.Type != "SOA" {
			userRecords++
		}
	}
	if userRecords > 0 {
		r53WriteError(w, http.StatusBadRequest, "HostedZoneNotEmpty",
			fmt.Sprintf("The hosted zone is not empty (%d non-required records).", userRecords))
		return
	}
	r53Zones.Delete(id)
	r53Tags.Delete(r53TagKey("hostedzone", id))
	r53ZoneVPCs.Delete(id)
	r53VPCAuthz.Delete(id)
	r53DNSSEC.Delete(id)
	change := newR53Change("INSYNC", "Hosted zone deleted")
	r53Changes.Put(strings.TrimPrefix(change.Id, "/change/"), r53StoredChange{Info: change})
	r53WriteXML(w, http.StatusOK, R53DeleteHostedZoneResponse{Xmlns: r53Namespace, ChangeInfo: change})
}

func handleR53ListHostedZones(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	maxItems := 100
	if raw := q.Get("maxitems"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			r53WriteError(w, http.StatusBadRequest, "InvalidInput", "maxitems must be between 1 and 100")
			return
		}
		maxItems = parsed
	}

	items := []R53HostedZone{}
	for _, stored := range r53Zones.List() {
		items = append(items, stored.Zone)
	}
	// Stable order so the opaque offset Marker pages each zone exactly once.
	sort.Slice(items, func(i, j int) bool {
		return r53ZoneIDFromPath(items[i].Id) < r53ZoneIDFromPath(items[j].Id)
	})

	page, next := awsPageExplicit(items, q.Get("marker"), maxItems)
	r53WriteXML(w, http.StatusOK, R53HostedZoneList{
		Xmlns:       r53Namespace,
		HostedZones: page,
		Marker:      q.Get("marker"),
		IsTruncated: next != "",
		NextMarker:  next,
		MaxItems:    strconv.Itoa(maxItems),
	})
}

type r53HostedZoneByNameItem struct {
	zone R53HostedZone
	key  string
	id   string
}

func handleR53ListHostedZonesByName(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	requestName := q.Get("dnsname")
	requestID := q.Get("hostedzoneid")
	startName := r53NormalizeName(requestName)
	startID := r53ZoneIDFromPath(requestID)
	if startName == "" && startID != "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "hostedzoneid requires dnsname")
		return
	}

	maxItems := 100
	if raw := q.Get("maxitems"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			r53WriteError(w, http.StatusBadRequest, "InvalidInput", "maxitems must be between 1 and 100")
			return
		}
		maxItems = parsed
	}

	items := make([]r53HostedZoneByNameItem, 0)
	for _, stored := range r53Zones.List() {
		items = append(items, r53HostedZoneByNameItem{
			zone: stored.Zone,
			key:  r53HostedZoneByNameSortKey(stored.Zone.Name),
			id:   r53ZoneIDFromPath(stored.Zone.Id),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].key != items[j].key {
			return items[i].key < items[j].key
		}
		return items[i].id < items[j].id
	})

	if startName != "" {
		startKey := r53HostedZoneByNameSortKey(startName)
		filtered := items[:0]
		for _, item := range items {
			if item.key < startKey {
				continue
			}
			if item.key == startKey && startID != "" && item.id < startID {
				continue
			}
			filtered = append(filtered, item)
		}
		items = filtered
	}

	isTruncated := len(items) > maxItems
	if isTruncated {
		items = items[:maxItems+1]
	}
	pageItems := items
	if isTruncated {
		pageItems = items[:maxItems]
	}
	hostedZones := make([]R53HostedZone, 0, len(pageItems))
	for _, item := range pageItems {
		hostedZones = append(hostedZones, item.zone)
	}

	resp := R53HostedZoneByNameList{
		Xmlns:        r53Namespace,
		HostedZones:  hostedZones,
		DNSName:      requestName,
		HostedZoneId: requestID,
		IsTruncated:  isTruncated,
		MaxItems:     strconv.Itoa(maxItems),
	}
	if isTruncated {
		next := items[maxItems]
		resp.NextDNSName = next.zone.Name
		resp.NextHostedZoneId = next.id
	}
	r53WriteXML(w, http.StatusOK, resp)
}

// ---------- ResourceRecordSet handlers ----------

func handleR53ChangeRRSets(w http.ResponseWriter, r *http.Request) {
	r53Mu.Lock()
	defer r53Mu.Unlock()

	id := r53ZoneIDFromPath(r.PathValue("id"))
	stored, ok := r53Zones.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	var req R53ChangeRRSetRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	for _, ch := range req.ChangeBatch.Changes {
		// Real Route 53 stores all names with a trailing dot. The SDK
		// preserves what the caller sent, so Terraform / aws CLI / SDK
		// can send either form. Normalise on store so the List filter
		// can do exact match.
		if !strings.HasSuffix(ch.ResourceRecordSet.Name, ".") {
			ch.ResourceRecordSet.Name += "."
		}
		switch strings.ToUpper(ch.Action) {
		case "CREATE":
			if rrsetExists(stored.Records, ch.ResourceRecordSet) {
				r53WriteError(w, http.StatusBadRequest, "InvalidChangeBatch",
					"Tried to create resource record set that already exists.")
				return
			}
			stored.Records = append(stored.Records, ch.ResourceRecordSet)
		case "UPSERT":
			stored.Records = rrsetReplace(stored.Records, ch.ResourceRecordSet)
		case "DELETE":
			before := len(stored.Records)
			stored.Records = rrsetDelete(stored.Records, ch.ResourceRecordSet)
			if len(stored.Records) == before {
				r53WriteError(w, http.StatusBadRequest, "InvalidChangeBatch",
					"Tried to delete resource record set that does not exist.")
				return
			}
		default:
			r53WriteError(w, http.StatusBadRequest, "InvalidChangeBatch",
				"Unknown change action: "+ch.Action)
			return
		}
	}
	stored.Zone.ResourceRecordSetCount = len(stored.Records)
	r53Zones.Put(id, stored)
	change := newR53Change("INSYNC", req.ChangeBatch.Comment)
	r53Changes.Put(strings.TrimPrefix(change.Id, "/change/"), r53StoredChange{Info: change})
	r53WriteXML(w, http.StatusOK, R53ChangeResourceRecordSetsResponse{
		Xmlns: r53Namespace, ChangeInfo: change,
	})
}

func rrsetKey(rr R53ResourceRecordSet) string {
	return strings.ToLower(rr.Name) + "|" + strings.ToUpper(rr.Type) + "|" + rr.SetIdentifier
}

func rrsetExists(records []R53ResourceRecordSet, rr R53ResourceRecordSet) bool {
	key := rrsetKey(rr)
	for _, r := range records {
		if rrsetKey(r) == key {
			return true
		}
	}
	return false
}

func rrsetReplace(records []R53ResourceRecordSet, rr R53ResourceRecordSet) []R53ResourceRecordSet {
	key := rrsetKey(rr)
	for i, r := range records {
		if rrsetKey(r) == key {
			records[i] = rr
			return records
		}
	}
	return append(records, rr)
}

func rrsetDelete(records []R53ResourceRecordSet, rr R53ResourceRecordSet) []R53ResourceRecordSet {
	key := rrsetKey(rr)
	out := records[:0]
	for _, r := range records {
		if rrsetKey(r) == key {
			continue
		}
		out = append(out, r)
	}
	return out
}

func handleR53ListRRSets(w http.ResponseWriter, r *http.Request) {
	id := r53ZoneIDFromPath(r.PathValue("id"))
	stored, ok := r53Zones.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	startName := r53NormalizeRecordName(r.URL.Query().Get("name"))
	startType := strings.ToUpper(r.URL.Query().Get("type"))
	startIdentifier := r.URL.Query().Get("identifier")
	if startName == "" && startType != "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "StartRecordType cannot be specified without StartRecordName.")
		return
	}
	maxItems := 300
	if raw := r.URL.Query().Get("maxitems"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			r53WriteError(w, http.StatusBadRequest, "InvalidInput", "maxitems must be a positive integer.")
			return
		}
		maxItems = parsed
	}
	if maxItems > 300 {
		maxItems = 300
	}

	records := append([]R53ResourceRecordSet(nil), stored.Records...)
	sort.SliceStable(records, func(i, j int) bool {
		return r53RRSetLess(records[i], records[j])
	})

	filtered := records[:0]
	for _, rr := range records {
		if startName != "" && r53RRSetCompareCursor(rr, startName, startType, startIdentifier) < 0 {
			continue
		}
		filtered = append(filtered, rr)
	}

	isTruncated := len(filtered) > maxItems
	var nextName, nextType, nextIdentifier string
	if isTruncated {
		next := filtered[maxItems]
		nextName = next.Name
		nextType = strings.ToUpper(next.Type)
		nextIdentifier = next.SetIdentifier
		filtered = filtered[:maxItems]
	}

	r53WriteXML(w, http.StatusOK, R53ListResourceRecordSetsResponse{
		Xmlns:                r53Namespace,
		ResourceRecordSets:   filtered,
		IsTruncated:          isTruncated,
		MaxItems:             strconv.Itoa(maxItems),
		NextRecordName:       nextName,
		NextRecordType:       nextType,
		NextRecordIdentifier: nextIdentifier,
	})
}

func r53NormalizeRecordName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "" && !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}

func r53RecordSortName(name string) string {
	name = r53NormalizeRecordName(name)
	trimmed := strings.TrimSuffix(name, ".")
	if trimmed == "" {
		return "."
	}
	labels := strings.Split(trimmed, ".")
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return strings.Join(labels, ".") + "."
}

func r53RRSetLess(a, b R53ResourceRecordSet) bool {
	ak, bk := r53RecordSortName(a.Name), r53RecordSortName(b.Name)
	if ak != bk {
		return ak < bk
	}
	at, bt := strings.ToUpper(a.Type), strings.ToUpper(b.Type)
	if at != bt {
		return at < bt
	}
	return a.SetIdentifier < b.SetIdentifier
}

func r53RRSetCompareCursor(rr R53ResourceRecordSet, startName, startType, startIdentifier string) int {
	rrName, cursorName := r53RecordSortName(rr.Name), r53RecordSortName(startName)
	if rrName < cursorName {
		return -1
	}
	if rrName > cursorName {
		return 1
	}
	rrType := strings.ToUpper(rr.Type)
	if startType != "" {
		if rrType < startType {
			return -1
		}
		if rrType > startType {
			return 1
		}
	}
	if startIdentifier != "" {
		if rr.SetIdentifier < startIdentifier {
			return -1
		}
		if rr.SetIdentifier > startIdentifier {
			return 1
		}
	}
	return 0
}

func handleR53GetChange(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("id"), "/change/")
	stored, ok := r53Changes.Get(id)
	if !ok {
		// Real Route 53 returns INSYNC for any unknown change ID after a
		// short window (changes are short-lived). Sim does the same so
		// Terraform's apply-then-poll converges.
		r53WriteXML(w, http.StatusOK, R53GetChangeResponse{
			Xmlns:      r53Namespace,
			ChangeInfo: R53ChangeInfo{Id: "/change/" + id, Status: "INSYNC", SubmittedAt: r53NowISO()},
		})
		return
	}
	r53WriteXML(w, http.StatusOK, R53GetChangeResponse{Xmlns: r53Namespace, ChangeInfo: stored.Info})
}
