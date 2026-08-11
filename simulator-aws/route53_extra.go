package main

import (
	"encoding/xml"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Route 53 — additional operation slices (health checks, traffic policies,
// VPC associations, query logging, change/count/geolocation reads). All
// REST + XML on the /2013-04-01/ path, matching the real Route 53 wire shapes
// the AWS SDK Go v2 + `aws` CLI parse.

// ---------- Stores ----------

type r53StoredHealthCheck struct {
	HealthCheck R53HealthCheck
}

type r53StoredTrafficPolicy struct {
	// Versions keyed by version number → policy document/comment.
	Versions map[int]R53TrafficPolicy
}

type r53StoredQueryLoggingConfig struct {
	Config R53QueryLoggingConfig
}

var (
	r53HealthChecks    sim.Store[r53StoredHealthCheck]
	r53TrafficPolicies sim.Store[r53StoredTrafficPolicy]
	r53QueryConfigs    sim.Store[r53StoredQueryLoggingConfig]
	r53ZoneVPCs        sim.Store[[]R53VPC]
	r53ExtraMu         sync.Mutex
)

func registerRoute53Extra(mux *sim.Server) {
	r53HealthChecks = sim.MakeStore[r53StoredHealthCheck](mux.DB(), "route53_health_checks")
	r53TrafficPolicies = sim.MakeStore[r53StoredTrafficPolicy](mux.DB(), "route53_traffic_policies")
	r53QueryConfigs = sim.MakeStore[r53StoredQueryLoggingConfig](mux.DB(), "route53_query_logging")
	r53ZoneVPCs = sim.MakeStore[[]R53VPC](mux.DB(), "route53_zone_vpcs")

	hcResource := cloudTrailRESTResource("AWS::Route53::HealthCheck", "id")
	tpResource := cloudTrailRESTResource("AWS::Route53::TrafficPolicy", "id")
	zoneResource := cloudTrailRESTResource("AWS::Route53::HostedZone", "id")
	qlResource := cloudTrailRESTResource("AWS::Route53::QueryLoggingConfig", "id")

	v := "/" + r53APIVersion

	// Health checks
	mux.HandleFunc("POST "+v+"/healthcheck", cloudTrailRecordedREST("CreateHealthCheck", "route53.amazonaws.com", nil, handleR53CreateHealthCheck))
	mux.HandleFunc("GET "+v+"/healthcheck", cloudTrailRecordedREST("ListHealthChecks", "route53.amazonaws.com", nil, handleR53ListHealthChecks))
	mux.HandleFunc("GET "+v+"/healthcheckcount", cloudTrailRecordedREST("GetHealthCheckCount", "route53.amazonaws.com", nil, handleR53GetHealthCheckCount))
	mux.HandleFunc("GET "+v+"/healthcheck/{id}", cloudTrailRecordedREST("GetHealthCheck", "route53.amazonaws.com", hcResource, handleR53GetHealthCheck))
	mux.HandleFunc("GET "+v+"/healthcheck/{id}/status", cloudTrailRecordedREST("GetHealthCheckStatus", "route53.amazonaws.com", hcResource, handleR53GetHealthCheckStatus))
	mux.HandleFunc("POST "+v+"/healthcheck/{id}", cloudTrailRecordedREST("UpdateHealthCheck", "route53.amazonaws.com", hcResource, handleR53UpdateHealthCheck))
	mux.HandleFunc("DELETE "+v+"/healthcheck/{id}", cloudTrailRecordedREST("DeleteHealthCheck", "route53.amazonaws.com", hcResource, handleR53DeleteHealthCheck))

	// Traffic policies
	mux.HandleFunc("POST "+v+"/trafficpolicy", cloudTrailRecordedREST("CreateTrafficPolicy", "route53.amazonaws.com", nil, handleR53CreateTrafficPolicy))
	mux.HandleFunc("GET "+v+"/trafficpolicies", cloudTrailRecordedREST("ListTrafficPolicies", "route53.amazonaws.com", nil, handleR53ListTrafficPolicies))
	mux.HandleFunc("GET "+v+"/trafficpolicies/{id}/versions", cloudTrailRecordedREST("ListTrafficPolicyVersions", "route53.amazonaws.com", tpResource, handleR53ListTrafficPolicyVersions))
	mux.HandleFunc("POST "+v+"/trafficpolicy/{id}", cloudTrailRecordedREST("CreateTrafficPolicyVersion", "route53.amazonaws.com", tpResource, handleR53CreateTrafficPolicyVersion))
	mux.HandleFunc("GET "+v+"/trafficpolicy/{id}/{version}", cloudTrailRecordedREST("GetTrafficPolicy", "route53.amazonaws.com", tpResource, handleR53GetTrafficPolicy))
	mux.HandleFunc("DELETE "+v+"/trafficpolicy/{id}/{version}", cloudTrailRecordedREST("DeleteTrafficPolicy", "route53.amazonaws.com", tpResource, handleR53DeleteTrafficPolicy))

	// VPC associations
	mux.HandleFunc("POST "+v+"/hostedzone/{id}/associatevpc", cloudTrailRecordedREST("AssociateVPCWithHostedZone", "route53.amazonaws.com", zoneResource, handleR53AssociateVPC))
	mux.HandleFunc("POST "+v+"/hostedzone/{id}/disassociatevpc", cloudTrailRecordedREST("DisassociateVPCFromHostedZone", "route53.amazonaws.com", zoneResource, handleR53DisassociateVPC))
	mux.HandleFunc("GET "+v+"/hostedzonesbyvpc", cloudTrailRecordedREST("ListHostedZonesByVPC", "route53.amazonaws.com", nil, handleR53ListHostedZonesByVPC))

	// Query logging
	mux.HandleFunc("POST "+v+"/queryloggingconfig", cloudTrailRecordedREST("CreateQueryLoggingConfig", "route53.amazonaws.com", nil, handleR53CreateQueryLoggingConfig))
	mux.HandleFunc("GET "+v+"/queryloggingconfig", cloudTrailRecordedREST("ListQueryLoggingConfigs", "route53.amazonaws.com", nil, handleR53ListQueryLoggingConfigs))
	mux.HandleFunc("GET "+v+"/queryloggingconfig/{id}", cloudTrailRecordedREST("GetQueryLoggingConfig", "route53.amazonaws.com", qlResource, handleR53GetQueryLoggingConfig))
	mux.HandleFunc("DELETE "+v+"/queryloggingconfig/{id}", cloudTrailRecordedREST("DeleteQueryLoggingConfig", "route53.amazonaws.com", qlResource, handleR53DeleteQueryLoggingConfig))

	// Misc counts + geolocation
	mux.HandleFunc("GET "+v+"/hostedzonecount", cloudTrailRecordedREST("GetHostedZoneCount", "route53.amazonaws.com", nil, handleR53GetHostedZoneCount))
	mux.HandleFunc("GET "+v+"/geolocation", cloudTrailRecordedREST("GetGeoLocation", "route53.amazonaws.com", nil, handleR53GetGeoLocation))
	mux.HandleFunc("GET "+v+"/geolocations", cloudTrailRecordedREST("ListGeoLocations", "route53.amazonaws.com", nil, handleR53ListGeoLocations))
}

// ---------- Health check types ----------

type R53HealthCheckConfig struct {
	IPAddress                    string   `xml:"IPAddress,omitempty"`
	Port                         *int     `xml:"Port,omitempty"`
	Type                         string   `xml:"Type,omitempty"`
	ResourcePath                 string   `xml:"ResourcePath,omitempty"`
	FullyQualifiedDomainName     string   `xml:"FullyQualifiedDomainName,omitempty"`
	SearchString                 string   `xml:"SearchString,omitempty"`
	RequestInterval              *int     `xml:"RequestInterval,omitempty"`
	FailureThreshold             *int     `xml:"FailureThreshold,omitempty"`
	MeasureLatency               *bool    `xml:"MeasureLatency,omitempty"`
	Inverted                     *bool    `xml:"Inverted,omitempty"`
	Disabled                     *bool    `xml:"Disabled,omitempty"`
	HealthThreshold              *int     `xml:"HealthThreshold,omitempty"`
	ChildHealthChecks            []string `xml:"ChildHealthChecks>ChildHealthCheck,omitempty"`
	EnableSNI                    *bool    `xml:"EnableSNI,omitempty"`
	Regions                      []string `xml:"Regions>Region,omitempty"`
	InsufficientDataHealthStatus string   `xml:"InsufficientDataHealthStatus,omitempty"`
}

type R53HealthCheck struct {
	XMLName            xml.Name             `xml:"HealthCheck"`
	Xmlns              string               `xml:"xmlns,attr,omitempty"`
	Id                 string               `xml:"Id"`
	CallerReference    string               `xml:"CallerReference"`
	HealthCheckConfig  R53HealthCheckConfig `xml:"HealthCheckConfig"`
	HealthCheckVersion int64                `xml:"HealthCheckVersion"`
}

type R53CreateHealthCheckRequest struct {
	XMLName           xml.Name             `xml:"CreateHealthCheckRequest"`
	CallerReference   string               `xml:"CallerReference"`
	HealthCheckConfig R53HealthCheckConfig `xml:"HealthCheckConfig"`
}

type R53CreateHealthCheckResponse struct {
	XMLName     xml.Name       `xml:"CreateHealthCheckResponse"`
	Xmlns       string         `xml:"xmlns,attr,omitempty"`
	HealthCheck R53HealthCheck `xml:"HealthCheck"`
}

type R53GetHealthCheckResponse struct {
	XMLName     xml.Name       `xml:"GetHealthCheckResponse"`
	Xmlns       string         `xml:"xmlns,attr,omitempty"`
	HealthCheck R53HealthCheck `xml:"HealthCheck"`
}

type R53ListHealthChecksResponse struct {
	XMLName      xml.Name         `xml:"ListHealthChecksResponse"`
	Xmlns        string           `xml:"xmlns,attr,omitempty"`
	HealthChecks []R53HealthCheck `xml:"HealthChecks>HealthCheck"`
	Marker       string           `xml:"Marker"`
	IsTruncated  bool             `xml:"IsTruncated"`
	NextMarker   string           `xml:"NextMarker,omitempty"`
	MaxItems     string           `xml:"MaxItems"`
}

type R53UpdateHealthCheckRequest struct {
	XMLName                      xml.Name `xml:"UpdateHealthCheckRequest"`
	HealthCheckVersion           *int64   `xml:"HealthCheckVersion,omitempty"`
	IPAddress                    *string  `xml:"IPAddress,omitempty"`
	Port                         *int     `xml:"Port,omitempty"`
	ResourcePath                 *string  `xml:"ResourcePath,omitempty"`
	FullyQualifiedDomainName     *string  `xml:"FullyQualifiedDomainName,omitempty"`
	SearchString                 *string  `xml:"SearchString,omitempty"`
	FailureThreshold             *int     `xml:"FailureThreshold,omitempty"`
	Inverted                     *bool    `xml:"Inverted,omitempty"`
	Disabled                     *bool    `xml:"Disabled,omitempty"`
	HealthThreshold              *int     `xml:"HealthThreshold,omitempty"`
	EnableSNI                    *bool    `xml:"EnableSNI,omitempty"`
	InsufficientDataHealthStatus *string  `xml:"InsufficientDataHealthStatus,omitempty"`
}

type R53UpdateHealthCheckResponse struct {
	XMLName     xml.Name       `xml:"UpdateHealthCheckResponse"`
	Xmlns       string         `xml:"xmlns,attr,omitempty"`
	HealthCheck R53HealthCheck `xml:"HealthCheck"`
}

type R53GetHealthCheckCountResponse struct {
	XMLName          xml.Name `xml:"GetHealthCheckCountResponse"`
	Xmlns            string   `xml:"xmlns,attr,omitempty"`
	HealthCheckCount int64    `xml:"HealthCheckCount"`
}

type R53StatusReport struct {
	Status      string `xml:"Status"`
	CheckedTime string `xml:"CheckedTime"`
}

type R53HealthCheckObservation struct {
	Region       string          `xml:"Region,omitempty"`
	IPAddress    string          `xml:"IPAddress,omitempty"`
	StatusReport R53StatusReport `xml:"StatusReport"`
}

type R53GetHealthCheckStatusResponse struct {
	XMLName                 xml.Name                    `xml:"GetHealthCheckStatusResponse"`
	Xmlns                   string                      `xml:"xmlns,attr,omitempty"`
	HealthCheckObservations []R53HealthCheckObservation `xml:"HealthCheckObservations>HealthCheckObservation"`
}

// ---------- Health check handlers ----------

func handleR53CreateHealthCheck(w http.ResponseWriter, r *http.Request) {
	r53ExtraMu.Lock()
	defer r53ExtraMu.Unlock()

	var req R53CreateHealthCheckRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	if req.CallerReference == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "CallerReference is required")
		return
	}
	// CallerReference is the idempotency key. A reused value returns
	// HealthCheckAlreadyExists, matching real Route 53.
	for _, sc := range r53HealthChecks.List() {
		if sc.HealthCheck.CallerReference == req.CallerReference {
			r53WriteError(w, http.StatusConflict, "HealthCheckAlreadyExists",
				"A health check with the specified caller reference already exists.")
			return
		}
	}
	id := r53ChildID()
	hc := R53HealthCheck{
		Xmlns:              r53Namespace,
		Id:                 id,
		CallerReference:    req.CallerReference,
		HealthCheckConfig:  req.HealthCheckConfig,
		HealthCheckVersion: 1,
	}
	r53HealthChecks.Put(id, r53StoredHealthCheck{HealthCheck: hc})
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/healthcheck/"+id)
	r53WriteXML(w, http.StatusCreated, R53CreateHealthCheckResponse{Xmlns: r53Namespace, HealthCheck: hc})
}

func handleR53GetHealthCheck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := r53HealthChecks.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHealthCheck", "A health check with id "+id+" does not exist.")
		return
	}
	r53WriteXML(w, http.StatusOK, R53GetHealthCheckResponse{Xmlns: r53Namespace, HealthCheck: stored.HealthCheck})
}

func handleR53ListHealthChecks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	maxItems := 100
	if raw := q.Get("maxitems"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= 1000 {
			maxItems = parsed
		}
	}
	items := []R53HealthCheck{}
	for _, sc := range r53HealthChecks.List() {
		items = append(items, sc.HealthCheck)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Id < items[j].Id })
	page, next := awsPageExplicit(items, q.Get("marker"), maxItems)
	r53WriteXML(w, http.StatusOK, R53ListHealthChecksResponse{
		Xmlns:        r53Namespace,
		HealthChecks: page,
		Marker:       q.Get("marker"),
		IsTruncated:  next != "",
		NextMarker:   next,
		MaxItems:     strconv.Itoa(maxItems),
	})
}

func handleR53UpdateHealthCheck(w http.ResponseWriter, r *http.Request) {
	r53ExtraMu.Lock()
	defer r53ExtraMu.Unlock()

	id := r.PathValue("id")
	stored, ok := r53HealthChecks.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHealthCheck", "A health check with id "+id+" does not exist.")
		return
	}
	var req R53UpdateHealthCheckRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	cfg := &stored.HealthCheck.HealthCheckConfig
	if req.IPAddress != nil {
		cfg.IPAddress = *req.IPAddress
	}
	if req.Port != nil {
		cfg.Port = req.Port
	}
	if req.ResourcePath != nil {
		cfg.ResourcePath = *req.ResourcePath
	}
	if req.FullyQualifiedDomainName != nil {
		cfg.FullyQualifiedDomainName = *req.FullyQualifiedDomainName
	}
	if req.SearchString != nil {
		cfg.SearchString = *req.SearchString
	}
	if req.FailureThreshold != nil {
		cfg.FailureThreshold = req.FailureThreshold
	}
	if req.Inverted != nil {
		cfg.Inverted = req.Inverted
	}
	if req.Disabled != nil {
		cfg.Disabled = req.Disabled
	}
	if req.HealthThreshold != nil {
		cfg.HealthThreshold = req.HealthThreshold
	}
	if req.EnableSNI != nil {
		cfg.EnableSNI = req.EnableSNI
	}
	if req.InsufficientDataHealthStatus != nil {
		cfg.InsufficientDataHealthStatus = *req.InsufficientDataHealthStatus
	}
	stored.HealthCheck.HealthCheckVersion++
	r53HealthChecks.Put(id, stored)
	r53WriteXML(w, http.StatusOK, R53UpdateHealthCheckResponse{Xmlns: r53Namespace, HealthCheck: stored.HealthCheck})
}

func handleR53DeleteHealthCheck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := r53HealthChecks.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHealthCheck", "A health check with id "+id+" does not exist.")
		return
	}
	r53HealthChecks.Delete(id)
	r53WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"DeleteHealthCheckResponse"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
	}{Xmlns: r53Namespace})
}

func handleR53GetHealthCheckCount(w http.ResponseWriter, _ *http.Request) {
	r53WriteXML(w, http.StatusOK, R53GetHealthCheckCountResponse{
		Xmlns:            r53Namespace,
		HealthCheckCount: int64(len(r53HealthChecks.List())),
	})
}

func handleR53GetHealthCheckStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := r53HealthChecks.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHealthCheck", "A health check with id "+id+" does not exist.")
		return
	}
	now := r53NowISO()
	r53WriteXML(w, http.StatusOK, R53GetHealthCheckStatusResponse{
		Xmlns: r53Namespace,
		HealthCheckObservations: []R53HealthCheckObservation{
			{Region: "us-east-1", StatusReport: R53StatusReport{
				Status: "Success: HTTP Status Code 200, OK", CheckedTime: now,
			}},
		},
	})
}

// ---------- Traffic policy types ----------

type R53TrafficPolicy struct {
	XMLName  xml.Name `xml:"TrafficPolicy"`
	Xmlns    string   `xml:"xmlns,attr,omitempty"`
	Id       string   `xml:"Id"`
	Version  int      `xml:"Version"`
	Name     string   `xml:"Name"`
	Type     string   `xml:"Type"`
	Document string   `xml:"Document"`
	Comment  string   `xml:"Comment,omitempty"`
}

type R53CreateTrafficPolicyRequest struct {
	XMLName  xml.Name `xml:"CreateTrafficPolicyRequest"`
	Name     string   `xml:"Name"`
	Document string   `xml:"Document"`
	Comment  string   `xml:"Comment,omitempty"`
}

type R53CreateTrafficPolicyResponse struct {
	XMLName       xml.Name         `xml:"CreateTrafficPolicyResponse"`
	Xmlns         string           `xml:"xmlns,attr,omitempty"`
	TrafficPolicy R53TrafficPolicy `xml:"TrafficPolicy"`
}

type R53CreateTrafficPolicyVersionResponse struct {
	XMLName       xml.Name         `xml:"CreateTrafficPolicyVersionResponse"`
	Xmlns         string           `xml:"xmlns,attr,omitempty"`
	TrafficPolicy R53TrafficPolicy `xml:"TrafficPolicy"`
}

type R53CreateTrafficPolicyVersionRequest struct {
	XMLName  xml.Name `xml:"CreateTrafficPolicyVersionRequest"`
	Document string   `xml:"Document"`
	Comment  string   `xml:"Comment,omitempty"`
}

type R53GetTrafficPolicyResponse struct {
	XMLName       xml.Name         `xml:"GetTrafficPolicyResponse"`
	Xmlns         string           `xml:"xmlns,attr,omitempty"`
	TrafficPolicy R53TrafficPolicy `xml:"TrafficPolicy"`
}

type R53TrafficPolicySummary struct {
	Id                 string `xml:"Id"`
	Name               string `xml:"Name"`
	Type               string `xml:"Type"`
	LatestVersion      int    `xml:"LatestVersion"`
	TrafficPolicyCount int    `xml:"TrafficPolicyCount"`
}

type R53ListTrafficPoliciesResponse struct {
	XMLName                xml.Name                  `xml:"ListTrafficPoliciesResponse"`
	Xmlns                  string                    `xml:"xmlns,attr,omitempty"`
	TrafficPolicySummaries []R53TrafficPolicySummary `xml:"TrafficPolicySummaries>TrafficPolicySummary"`
	IsTruncated            bool                      `xml:"IsTruncated"`
	TrafficPolicyIdMarker  string                    `xml:"TrafficPolicyIdMarker"`
	MaxItems               string                    `xml:"MaxItems"`
}

type R53ListTrafficPolicyVersionsResponse struct {
	XMLName                    xml.Name           `xml:"ListTrafficPolicyVersionsResponse"`
	Xmlns                      string             `xml:"xmlns,attr,omitempty"`
	TrafficPolicies            []R53TrafficPolicy `xml:"TrafficPolicies>TrafficPolicy"`
	IsTruncated                bool               `xml:"IsTruncated"`
	TrafficPolicyVersionMarker string             `xml:"TrafficPolicyVersionMarker"`
	MaxItems                   string             `xml:"MaxItems"`
}

// ---------- Traffic policy handlers ----------

func handleR53CreateTrafficPolicy(w http.ResponseWriter, r *http.Request) {
	r53ExtraMu.Lock()
	defer r53ExtraMu.Unlock()

	var req R53CreateTrafficPolicyRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	if req.Name == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidTrafficPolicyDocument", "Name is required")
		return
	}
	id := r53ChildID()
	tp := R53TrafficPolicy{
		Xmlns:    r53Namespace,
		Id:       id,
		Version:  1,
		Name:     req.Name,
		Type:     r53TrafficPolicyType(req.Document),
		Document: req.Document,
		Comment:  req.Comment,
	}
	r53TrafficPolicies.Put(id, r53StoredTrafficPolicy{Versions: map[int]R53TrafficPolicy{1: tp}})
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/trafficpolicy/"+id+"/1")
	r53WriteXML(w, http.StatusCreated, R53CreateTrafficPolicyResponse{Xmlns: r53Namespace, TrafficPolicy: tp})
}

func handleR53CreateTrafficPolicyVersion(w http.ResponseWriter, r *http.Request) {
	r53ExtraMu.Lock()
	defer r53ExtraMu.Unlock()

	id := r.PathValue("id")
	stored, ok := r53TrafficPolicies.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy", "No traffic policy exists with ID "+id)
		return
	}
	var req R53CreateTrafficPolicyVersionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	maxVer := 0
	var name string
	for ver, p := range stored.Versions {
		if ver > maxVer {
			maxVer = ver
		}
		name = p.Name
	}
	newVer := maxVer + 1
	tp := R53TrafficPolicy{
		Xmlns:    r53Namespace,
		Id:       id,
		Version:  newVer,
		Name:     name,
		Type:     r53TrafficPolicyType(req.Document),
		Document: req.Document,
		Comment:  req.Comment,
	}
	stored.Versions[newVer] = tp
	r53TrafficPolicies.Put(id, stored)
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/trafficpolicy/"+id+"/"+strconv.Itoa(newVer))
	r53WriteXML(w, http.StatusCreated, R53CreateTrafficPolicyVersionResponse{Xmlns: r53Namespace, TrafficPolicy: tp})
}

func handleR53GetTrafficPolicy(w http.ResponseWriter, r *http.Request) {
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
	r53WriteXML(w, http.StatusOK, R53GetTrafficPolicyResponse{Xmlns: r53Namespace, TrafficPolicy: tp})
}

func handleR53DeleteTrafficPolicy(w http.ResponseWriter, r *http.Request) {
	r53ExtraMu.Lock()
	defer r53ExtraMu.Unlock()

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
	if _, ok := stored.Versions[ver]; !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy", "No traffic policy version exists with ID "+id+" and version "+strconv.Itoa(ver))
		return
	}
	delete(stored.Versions, ver)
	if len(stored.Versions) == 0 {
		r53TrafficPolicies.Delete(id)
	} else {
		r53TrafficPolicies.Put(id, stored)
	}
	r53WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"DeleteTrafficPolicyResponse"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
	}{Xmlns: r53Namespace})
}

func handleR53ListTrafficPolicies(w http.ResponseWriter, _ *http.Request) {
	summaries := []R53TrafficPolicySummary{}
	for _, stored := range r53TrafficPolicies.List() {
		maxVer := 0
		var name, typ, id string
		for ver, p := range stored.Versions {
			if ver > maxVer {
				maxVer = ver
				name, typ, id = p.Name, p.Type, p.Id
			}
		}
		summaries = append(summaries, R53TrafficPolicySummary{
			Id:                 id,
			Name:               name,
			Type:               typ,
			LatestVersion:      maxVer,
			TrafficPolicyCount: len(stored.Versions),
		})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Id < summaries[j].Id })
	r53WriteXML(w, http.StatusOK, R53ListTrafficPoliciesResponse{
		Xmlns:                  r53Namespace,
		TrafficPolicySummaries: summaries,
		IsTruncated:            false,
		MaxItems:               "100",
	})
}

func handleR53ListTrafficPolicyVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := r53TrafficPolicies.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchTrafficPolicy", "No traffic policy exists with ID "+id)
		return
	}
	versions := make([]int, 0, len(stored.Versions))
	for ver := range stored.Versions {
		versions = append(versions, ver)
	}
	sort.Ints(versions)
	policies := make([]R53TrafficPolicy, 0, len(versions))
	for _, ver := range versions {
		policies = append(policies, stored.Versions[ver])
	}
	r53WriteXML(w, http.StatusOK, R53ListTrafficPolicyVersionsResponse{
		Xmlns:           r53Namespace,
		TrafficPolicies: policies,
		IsTruncated:     false,
		MaxItems:        "100",
	})
}

// r53TrafficPolicyType reads the policy document's RecordType. Falls back to
// "A" when the document doesn't carry one (real Route 53 requires RecordType
// in the document).
func r53TrafficPolicyType(document string) string {
	if i := strings.Index(document, `"RecordType"`); i >= 0 {
		rest := document[i+len(`"RecordType"`):]
		if c := strings.Index(rest, ":"); c >= 0 {
			rest = rest[c+1:]
			if q := strings.Index(rest, `"`); q >= 0 {
				rest = rest[q+1:]
				if e := strings.Index(rest, `"`); e >= 0 {
					return rest[:e]
				}
			}
		}
	}
	return "A"
}

// ---------- VPC association types ----------

type R53VPCRequest struct {
	XMLName xml.Name `xml:""`
	VPC     R53VPC   `xml:"VPC"`
	Comment string   `xml:"Comment,omitempty"`
}

type R53AssociateVPCResponse struct {
	XMLName    xml.Name      `xml:"AssociateVPCWithHostedZoneResponse"`
	Xmlns      string        `xml:"xmlns,attr,omitempty"`
	ChangeInfo R53ChangeInfo `xml:"ChangeInfo"`
}

type R53DisassociateVPCResponse struct {
	XMLName    xml.Name      `xml:"DisassociateVPCFromHostedZoneResponse"`
	Xmlns      string        `xml:"xmlns,attr,omitempty"`
	ChangeInfo R53ChangeInfo `xml:"ChangeInfo"`
}

type R53HostedZoneOwner struct {
	OwningAccount string `xml:"OwningAccount,omitempty"`
	OwningService string `xml:"OwningService,omitempty"`
}

type R53HostedZoneVPCSummary struct {
	HostedZoneId string             `xml:"HostedZoneId"`
	Name         string             `xml:"Name"`
	Owner        R53HostedZoneOwner `xml:"Owner"`
}

type R53ListHostedZonesByVPCResponse struct {
	XMLName             xml.Name                  `xml:"ListHostedZonesByVPCResponse"`
	Xmlns               string                    `xml:"xmlns,attr,omitempty"`
	HostedZoneSummaries []R53HostedZoneVPCSummary `xml:"HostedZoneSummaries>HostedZoneSummary"`
	MaxItems            string                    `xml:"MaxItems"`
	NextToken           string                    `xml:"NextToken,omitempty"`
}

// ---------- VPC association handlers ----------

func r53ZoneVPCList(zoneID string) []R53VPC {
	list, _ := r53ZoneVPCs.Get(zoneID)
	return list
}

func handleR53AssociateVPC(w http.ResponseWriter, r *http.Request) {
	r53ExtraMu.Lock()
	defer r53ExtraMu.Unlock()

	id := r53ZoneIDFromPath(r.PathValue("id"))
	if _, ok := r53Zones.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	var req R53VPCRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	list := r53ZoneVPCList(id)
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
	r53ZoneVPCs.Put(id, list)
	change := newR53Change("INSYNC", req.Comment)
	r53Changes.Put(strings.TrimPrefix(change.Id, "/change/"), r53StoredChange{Info: change})
	r53WriteXML(w, http.StatusOK, R53AssociateVPCResponse{Xmlns: r53Namespace, ChangeInfo: change})
}

func handleR53DisassociateVPC(w http.ResponseWriter, r *http.Request) {
	r53ExtraMu.Lock()
	defer r53ExtraMu.Unlock()

	id := r53ZoneIDFromPath(r.PathValue("id"))
	if _, ok := r53Zones.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	var req R53VPCRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	list := r53ZoneVPCList(id)
	out := make([]R53VPC, 0, len(list))
	for _, v := range list {
		if v.VPCId != req.VPC.VPCId {
			out = append(out, v)
		}
	}
	r53ZoneVPCs.Put(id, out)
	change := newR53Change("INSYNC", req.Comment)
	r53Changes.Put(strings.TrimPrefix(change.Id, "/change/"), r53StoredChange{Info: change})
	r53WriteXML(w, http.StatusOK, R53DisassociateVPCResponse{Xmlns: r53Namespace, ChangeInfo: change})
}

func handleR53ListHostedZonesByVPC(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	vpcID := q.Get("vpcid")
	vpcRegion := q.Get("vpcregion")
	if vpcID == "" || vpcRegion == "" {
		r53WriteError(w, http.StatusBadRequest, "InvalidInput", "vpcid and vpcregion are required")
		return
	}
	summaries := []R53HostedZoneVPCSummary{}
	for _, stored := range r53Zones.List() {
		zoneID := r53ZoneIDFromPath(stored.Zone.Id)
		for _, v := range r53ZoneVPCList(zoneID) {
			if v.VPCId == vpcID {
				summaries = append(summaries, R53HostedZoneVPCSummary{
					HostedZoneId: zoneID,
					Name:         stored.Zone.Name,
					Owner:        R53HostedZoneOwner{OwningAccount: awsAccountID()},
				})
				break
			}
		}
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].HostedZoneId < summaries[j].HostedZoneId })
	r53WriteXML(w, http.StatusOK, R53ListHostedZonesByVPCResponse{
		Xmlns:               r53Namespace,
		HostedZoneSummaries: summaries,
		MaxItems:            "100",
	})
}

// ---------- Query logging types ----------

type R53QueryLoggingConfig struct {
	XMLName                   xml.Name `xml:"QueryLoggingConfig"`
	Xmlns                     string   `xml:"xmlns,attr,omitempty"`
	Id                        string   `xml:"Id"`
	HostedZoneId              string   `xml:"HostedZoneId"`
	CloudWatchLogsLogGroupArn string   `xml:"CloudWatchLogsLogGroupArn"`
}

type R53CreateQueryLoggingConfigRequest struct {
	XMLName                   xml.Name `xml:"CreateQueryLoggingConfigRequest"`
	HostedZoneId              string   `xml:"HostedZoneId"`
	CloudWatchLogsLogGroupArn string   `xml:"CloudWatchLogsLogGroupArn"`
}

type R53CreateQueryLoggingConfigResponse struct {
	XMLName            xml.Name              `xml:"CreateQueryLoggingConfigResponse"`
	Xmlns              string                `xml:"xmlns,attr,omitempty"`
	QueryLoggingConfig R53QueryLoggingConfig `xml:"QueryLoggingConfig"`
}

type R53GetQueryLoggingConfigResponse struct {
	XMLName            xml.Name              `xml:"GetQueryLoggingConfigResponse"`
	Xmlns              string                `xml:"xmlns,attr,omitempty"`
	QueryLoggingConfig R53QueryLoggingConfig `xml:"QueryLoggingConfig"`
}

type R53ListQueryLoggingConfigsResponse struct {
	XMLName             xml.Name                `xml:"ListQueryLoggingConfigsResponse"`
	Xmlns               string                  `xml:"xmlns,attr,omitempty"`
	QueryLoggingConfigs []R53QueryLoggingConfig `xml:"QueryLoggingConfigs>QueryLoggingConfig"`
	NextToken           string                  `xml:"NextToken,omitempty"`
}

// ---------- Query logging handlers ----------

func handleR53CreateQueryLoggingConfig(w http.ResponseWriter, r *http.Request) {
	r53ExtraMu.Lock()
	defer r53ExtraMu.Unlock()

	var req R53CreateQueryLoggingConfigRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		r53WriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	zoneID := r53ZoneIDFromPath(req.HostedZoneId)
	if _, ok := r53Zones.Get(zoneID); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchHostedZone", "The specified hosted zone does not exist.")
		return
	}
	for _, sc := range r53QueryConfigs.List() {
		if r53ZoneIDFromPath(sc.Config.HostedZoneId) == zoneID {
			r53WriteError(w, http.StatusConflict, "QueryLoggingConfigAlreadyExists",
				"A query logging configuration already exists for this hosted zone.")
			return
		}
	}
	id := r53ChildID()
	cfg := R53QueryLoggingConfig{
		Xmlns:                     r53Namespace,
		Id:                        id,
		HostedZoneId:              zoneID,
		CloudWatchLogsLogGroupArn: req.CloudWatchLogsLogGroupArn,
	}
	r53QueryConfigs.Put(id, r53StoredQueryLoggingConfig{Config: cfg})
	w.Header().Set("Location", "https://route53.amazonaws.com/"+r53APIVersion+"/queryloggingconfig/"+id)
	r53WriteXML(w, http.StatusCreated, R53CreateQueryLoggingConfigResponse{Xmlns: r53Namespace, QueryLoggingConfig: cfg})
}

func handleR53GetQueryLoggingConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := r53QueryConfigs.Get(id)
	if !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchQueryLoggingConfig", "The query logging configuration "+id+" does not exist.")
		return
	}
	r53WriteXML(w, http.StatusOK, R53GetQueryLoggingConfigResponse{Xmlns: r53Namespace, QueryLoggingConfig: stored.Config})
}

func handleR53ListQueryLoggingConfigs(w http.ResponseWriter, r *http.Request) {
	zoneFilter := r53ZoneIDFromPath(r.URL.Query().Get("hostedzoneid"))
	configs := []R53QueryLoggingConfig{}
	for _, sc := range r53QueryConfigs.List() {
		if zoneFilter != "" && r53ZoneIDFromPath(sc.Config.HostedZoneId) != zoneFilter {
			continue
		}
		configs = append(configs, sc.Config)
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Id < configs[j].Id })
	r53WriteXML(w, http.StatusOK, R53ListQueryLoggingConfigsResponse{
		Xmlns:               r53Namespace,
		QueryLoggingConfigs: configs,
	})
}

func handleR53DeleteQueryLoggingConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := r53QueryConfigs.Get(id); !ok {
		r53WriteError(w, http.StatusNotFound, "NoSuchQueryLoggingConfig", "The query logging configuration "+id+" does not exist.")
		return
	}
	r53QueryConfigs.Delete(id)
	r53WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"DeleteQueryLoggingConfigResponse"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
	}{Xmlns: r53Namespace})
}

// ---------- Misc: counts + geolocation ----------

type R53GetHostedZoneCountResponse struct {
	XMLName         xml.Name `xml:"GetHostedZoneCountResponse"`
	Xmlns           string   `xml:"xmlns,attr,omitempty"`
	HostedZoneCount int64    `xml:"HostedZoneCount"`
}

func handleR53GetHostedZoneCount(w http.ResponseWriter, _ *http.Request) {
	r53WriteXML(w, http.StatusOK, R53GetHostedZoneCountResponse{
		Xmlns:           r53Namespace,
		HostedZoneCount: int64(len(r53Zones.List())),
	})
}

type R53GeoLocationDetails struct {
	ContinentCode   string `xml:"ContinentCode,omitempty"`
	ContinentName   string `xml:"ContinentName,omitempty"`
	CountryCode     string `xml:"CountryCode,omitempty"`
	CountryName     string `xml:"CountryName,omitempty"`
	SubdivisionCode string `xml:"SubdivisionCode,omitempty"`
	SubdivisionName string `xml:"SubdivisionName,omitempty"`
}

type R53GetGeoLocationResponse struct {
	XMLName            xml.Name              `xml:"GetGeoLocationResponse"`
	Xmlns              string                `xml:"xmlns,attr,omitempty"`
	GeoLocationDetails R53GeoLocationDetails `xml:"GeoLocationDetails"`
}

type R53ListGeoLocationsResponse struct {
	XMLName                xml.Name                `xml:"ListGeoLocationsResponse"`
	Xmlns                  string                  `xml:"xmlns,attr,omitempty"`
	GeoLocationDetailsList []R53GeoLocationDetails `xml:"GeoLocationDetailsList>GeoLocationDetails"`
	IsTruncated            bool                    `xml:"IsTruncated"`
	NextContinentCode      string                  `xml:"NextContinentCode,omitempty"`
	NextCountryCode        string                  `xml:"NextCountryCode,omitempty"`
	NextSubdivisionCode    string                  `xml:"NextSubdivisionCode,omitempty"`
	MaxItems               string                  `xml:"MaxItems"`
}

// r53GeoLocations is the fixed reference set Route 53 exposes — continents
// plus a representative sample of countries and subdivisions. The data is
// static cloud metadata (the same on every account).
var r53GeoLocations = []R53GeoLocationDetails{
	{ContinentCode: "AF", ContinentName: "Africa"},
	{ContinentCode: "AN", ContinentName: "Antarctica"},
	{ContinentCode: "AS", ContinentName: "Asia"},
	{ContinentCode: "EU", ContinentName: "Europe"},
	{ContinentCode: "OC", ContinentName: "Oceania"},
	{ContinentCode: "NA", ContinentName: "North America"},
	{ContinentCode: "SA", ContinentName: "South America"},
	{CountryCode: "US", CountryName: "United States"},
	{CountryCode: "CA", CountryName: "Canada"},
	{CountryCode: "GB", CountryName: "United Kingdom"},
	{CountryCode: "DE", CountryName: "Germany"},
	{CountryCode: "FR", CountryName: "France"},
	{CountryCode: "JP", CountryName: "Japan"},
	{CountryCode: "AU", CountryName: "Australia"},
	{CountryCode: "BR", CountryName: "Brazil"},
	{CountryCode: "US", CountryName: "United States", SubdivisionCode: "CA", SubdivisionName: "California"},
	{CountryCode: "US", CountryName: "United States", SubdivisionCode: "WA", SubdivisionName: "Washington"},
}

func handleR53GetGeoLocation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	continent := strings.ToUpper(q.Get("continentcode"))
	country := strings.ToUpper(q.Get("countrycode"))
	subdivision := strings.ToUpper(q.Get("subdivisioncode"))
	if continent == "" && country == "" {
		// GetGeoLocation with no params returns the default "*" location.
		r53WriteXML(w, http.StatusOK, R53GetGeoLocationResponse{
			Xmlns:              r53Namespace,
			GeoLocationDetails: R53GeoLocationDetails{},
		})
		return
	}
	for _, g := range r53GeoLocations {
		if continent != "" && g.ContinentCode == continent && g.CountryCode == "" {
			r53WriteXML(w, http.StatusOK, R53GetGeoLocationResponse{Xmlns: r53Namespace, GeoLocationDetails: g})
			return
		}
		if country != "" && g.CountryCode == country && g.SubdivisionCode == subdivision {
			r53WriteXML(w, http.StatusOK, R53GetGeoLocationResponse{Xmlns: r53Namespace, GeoLocationDetails: g})
			return
		}
	}
	r53WriteError(w, http.StatusBadRequest, "NoSuchGeoLocation", "The geo location you are trying to get does not exist.")
}

func handleR53ListGeoLocations(w http.ResponseWriter, r *http.Request) {
	maxItems := 100
	if raw := r.URL.Query().Get("maxitems"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 {
			maxItems = parsed
		}
	}
	list := append([]R53GeoLocationDetails(nil), r53GeoLocations...)
	isTruncated := len(list) > maxItems
	if isTruncated {
		list = list[:maxItems]
	}
	r53WriteXML(w, http.StatusOK, R53ListGeoLocationsResponse{
		Xmlns:                  r53Namespace,
		GeoLocationDetailsList: list,
		IsTruncated:            isTruncated,
		MaxItems:               strconv.Itoa(maxItems),
	})
}

// r53ChildID returns an opaque id for non-zone child resources (health
// checks, traffic policies, query logging configs). Route 53 uses
// UUID-shaped ids for these; the sim uses a stable random token.
func r53ChildID() string {
	return strings.ToLower(strings.TrimPrefix(r53ChangeID(), "C")) + "-" +
		strconv.FormatInt(time.Now().UnixNano()%1000000, 10)
}
