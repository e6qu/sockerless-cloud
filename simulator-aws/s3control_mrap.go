package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon S3 Multi-Region Access Points: one global endpoint over buckets in
// several regions, with a traffic dial per region deciding where a request
// lands. Creating, deleting, and re-policying one is asynchronous — each
// returns a request token the caller polls
// DescribeMultiRegionAccessPointOperation with, and the simulator records the
// operation and its outcome so the poll reports what actually happened.

// S3MultiRegionAccessPoint is one multi-region endpoint.
type S3MultiRegionAccessPoint struct {
	AccountID         string                           `json:"accountId"`
	Name              string                           `json:"name"`
	Alias             string                           `json:"alias"`
	CreatedAt         string                           `json:"createdAt"`
	Status            string                           `json:"status"`
	PublicAccessBlock s3PublicAccessBlock              `json:"publicAccessBlock"`
	Regions           []S3MultiRegionAccessPointRegion `json:"regions"`
	EstablishedPolicy string                           `json:"establishedPolicy,omitempty"`
	ProposedPolicy    string                           `json:"proposedPolicy,omitempty"`
}

// S3MultiRegionAccessPointRegion is one region's bucket behind the endpoint,
// and the share of traffic its dial admits.
type S3MultiRegionAccessPointRegion struct {
	Bucket                string `json:"bucket"`
	Region                string `json:"region"`
	BucketAccountID       string `json:"bucketAccountId,omitempty"`
	TrafficDialPercentage int    `json:"trafficDialPercentage"`
}

type s3PublicAccessBlock struct {
	BlockPublicAcls       bool `xml:"BlockPublicAcls" json:"blockPublicAcls"`
	IgnorePublicAcls      bool `xml:"IgnorePublicAcls" json:"ignorePublicAcls"`
	BlockPublicPolicy     bool `xml:"BlockPublicPolicy" json:"blockPublicPolicy"`
	RestrictPublicBuckets bool `xml:"RestrictPublicBuckets" json:"restrictPublicBuckets"`
}

// S3AsyncOperation is one asynchronous control-plane request and its outcome.
type S3AsyncOperation struct {
	AccountID       string `json:"accountId"`
	RequestTokenARN string `json:"requestTokenArn"`
	Operation       string `json:"operation"`
	CreationTime    string `json:"creationTime"`
	RequestStatus   string `json:"requestStatus"`
	Name            string `json:"name"`
	Policy          string `json:"policy,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

var (
	s3MultiRegionAccessPoints sim.Store[S3MultiRegionAccessPoint]
	s3AsyncOperations         sim.Store[S3AsyncOperation]
)

func registerS3ControlMultiRegion(srv *sim.Server) {
	s3MultiRegionAccessPoints = sim.MakeStore[S3MultiRegionAccessPoint](srv.DB(), "s3_multi_region_access_points")
	s3AsyncOperations = sim.MakeStore[S3AsyncOperation](srv.DB(), "s3_async_operations")

	srv.HandleFunc("POST /v20180820/async-requests/mrap/create", handleS3CreateMultiRegionAccessPoint)
	srv.HandleFunc("POST /v20180820/async-requests/mrap/delete", handleS3DeleteMultiRegionAccessPoint)
	srv.HandleFunc("POST /v20180820/async-requests/mrap/put-policy", handleS3PutMultiRegionAccessPointPolicy)
	srv.HandleFunc("GET /v20180820/async-requests/mrap/{token...}", handleS3DescribeMultiRegionAccessPointOperation)

	srv.HandleFunc("GET /v20180820/mrap/instances", handleS3ListMultiRegionAccessPoints)
	srv.HandleFunc("GET /v20180820/mrap/instances/{name}", handleS3GetMultiRegionAccessPoint)
	srv.HandleFunc("GET /v20180820/mrap/instances/{name}/policy", handleS3GetMultiRegionAccessPointPolicy)
	srv.HandleFunc("GET /v20180820/mrap/instances/{name}/policystatus", handleS3GetMultiRegionAccessPointPolicyStatus)
	srv.HandleFunc("GET /v20180820/mrap/instances/{name}/routes", handleS3GetMultiRegionAccessPointRoutes)
	srv.HandleFunc("PATCH /v20180820/mrap/instances/{name}/routes", handleS3SubmitMultiRegionAccessPointRoutes)
}

// s3MultiRegionAlias is the global name a client addresses the endpoint by.
func s3MultiRegionAlias(name, account string) string {
	return strings.ToLower(name) + "." + account[len(account)-4:] + ".mrap"
}

// s3RecordAsyncOperation stores an asynchronous request's outcome and returns
// the token the caller polls it with.
func s3RecordAsyncOperation(account, operation, name, policy, errCode, errMessage string) string {
	token := fmt.Sprintf("arn:aws:s3:us-west-2:%s:async-request/mrap/%s/%s",
		account, s3AsyncOperationSlug(operation), s3ObjectLambdaID())
	status := "SUCCEEDED"
	if errCode != "" {
		status = "FAILED"
	}
	s3AsyncOperations.Put(token, S3AsyncOperation{
		AccountID: account, RequestTokenARN: token, Operation: operation,
		CreationTime: time.Now().UTC().Format(time.RFC3339), RequestStatus: status,
		Name: name, Policy: policy, ErrorCode: errCode, ErrorMessage: errMessage,
	})
	return token
}

// s3AsyncOperationSlug is the verb the request token carries, which is how a
// token says which asynchronous request it belongs to.
func s3AsyncOperationSlug(operation string) string {
	switch operation {
	case "CreateMultiRegionAccessPoint":
		return "create"
	case "DeleteMultiRegionAccessPoint":
		return "delete"
	case "PutMultiRegionAccessPointPolicy":
		return "put-policy"
	}
	return strings.ToLower(operation)
}

func s3WriteAsyncToken(w http.ResponseWriter, element, token string) {
	type result struct {
		XMLName         xml.Name
		RequestTokenARN string `xml:"RequestTokenARN"`
	}
	WriteXML(w, http.StatusOK, result{XMLName: xml.Name{Local: element}, RequestTokenARN: token})
}

func handleS3CreateMultiRegionAccessPoint(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	body, ok := s3ControlReadXMLBody(w, r, "CreateMultiRegionAccessPointRequest")
	if !ok {
		return
	}
	if body.ChildText("ClientToken") == "" {
		s3ControlError(w, "InvalidRequest", "ClientToken is required", http.StatusBadRequest)
		return
	}
	details, hasDetails := body.Child("Details")
	if !hasDetails {
		s3ControlError(w, "InvalidRequest", "Details is required", http.StatusBadRequest)
		return
	}
	name := details.ChildText("Name")
	if name == "" {
		s3ControlError(w, "InvalidRequest", "the endpoint must have a Name", http.StatusBadRequest)
		return
	}
	regions, err := s3MultiRegionRegionsFrom(details)
	if err != nil {
		s3ControlError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	// The asynchronous request is accepted either way; whether it succeeded is
	// what the caller learns by polling it.
	if _, exists := s3MultiRegionAccessPoints.Get(s3AccessPointKey(account, name)); exists {
		token := s3RecordAsyncOperation(account, "CreateMultiRegionAccessPoint", name, "",
			"MultiRegionAccessPointAlreadyOwnedByYou",
			"A Multi-Region Access Point named "+name+" already exists in this account")
		s3WriteAsyncToken(w, "CreateMultiRegionAccessPointResult", token)
		return
	}
	mrap := S3MultiRegionAccessPoint{
		AccountID: account, Name: name, Alias: s3MultiRegionAlias(name, account),
		CreatedAt: time.Now().UTC().Format(time.RFC3339), Status: "READY",
		PublicAccessBlock: s3PublicAccessBlockFrom(details),
		Regions:           regions,
	}
	s3MultiRegionAccessPoints.Put(s3AccessPointKey(account, name), mrap)
	s3WriteAsyncToken(w, "CreateMultiRegionAccessPointResult",
		s3RecordAsyncOperation(account, "CreateMultiRegionAccessPoint", name, "", "", ""))
}

// s3MultiRegionRegionsFrom reads the regional buckets the endpoint fronts.
// Every bucket has to exist and the dials start evenly split, which is the
// state a freshly created endpoint routes with.
func s3MultiRegionRegionsFrom(details s3ControlXMLNode) ([]S3MultiRegionAccessPointRegion, error) {
	list, ok := details.Child("Regions")
	if !ok || len(list.Children) == 0 {
		return nil, fmt.Errorf("the endpoint must name at least one Region")
	}
	var regions []S3MultiRegionAccessPointRegion
	for _, child := range list.Children {
		if child.Name != "Region" {
			continue
		}
		bucket := child.ChildText("Bucket")
		if bucket == "" {
			return nil, fmt.Errorf("each Region must name a Bucket")
		}
		if _, ok := s3Buckets_.Get(bucket); !ok {
			return nil, fmt.Errorf("the bucket %s does not exist", bucket)
		}
		regions = append(regions, S3MultiRegionAccessPointRegion{
			Bucket: bucket, Region: awsRegion(),
			BucketAccountID:       child.ChildText("BucketAccountId"),
			TrafficDialPercentage: 100,
		})
	}
	if len(regions) == 0 {
		return nil, fmt.Errorf("the endpoint must name at least one Region")
	}
	return regions, nil
}

func s3PublicAccessBlockFrom(details s3ControlXMLNode) s3PublicAccessBlock {
	// S3 blocks public access on a new Multi-Region Access Point unless the
	// request says otherwise, so an absent configuration is the blocked one.
	block, ok := details.Child("PublicAccessBlock")
	if !ok {
		return s3PublicAccessBlock{true, true, true, true}
	}
	return s3PublicAccessBlock{
		BlockPublicAcls:       block.ChildText("BlockPublicAcls") == "true",
		IgnorePublicAcls:      block.ChildText("IgnorePublicAcls") == "true",
		BlockPublicPolicy:     block.ChildText("BlockPublicPolicy") == "true",
		RestrictPublicBuckets: block.ChildText("RestrictPublicBuckets") == "true",
	}
}

func handleS3DeleteMultiRegionAccessPoint(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	body, ok := s3ControlReadXMLBody(w, r, "DeleteMultiRegionAccessPointRequest")
	if !ok {
		return
	}
	details, _ := body.Child("Details")
	name := details.ChildText("Name")
	if name == "" {
		s3ControlError(w, "InvalidRequest", "Details must name the endpoint to delete", http.StatusBadRequest)
		return
	}
	errCode, errMessage := "", ""
	if !s3MultiRegionAccessPoints.Delete(s3AccessPointKey(account, name)) {
		errCode, errMessage = "NoSuchMultiRegionAccessPoint",
			"The specified Multi-Region Access Point does not exist"
	}
	s3WriteAsyncToken(w, "DeleteMultiRegionAccessPointResult",
		s3RecordAsyncOperation(account, "DeleteMultiRegionAccessPoint", name, "", errCode, errMessage))
}

func handleS3PutMultiRegionAccessPointPolicy(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	body, ok := s3ControlReadXMLBody(w, r, "PutMultiRegionAccessPointPolicyRequest")
	if !ok {
		return
	}
	details, _ := body.Child("Details")
	name, policy := details.ChildText("Name"), details.ChildText("Policy")
	if name == "" || policy == "" {
		s3ControlError(w, "InvalidRequest", "Details must carry a Name and a Policy", http.StatusBadRequest)
		return
	}
	errCode, errMessage := "", ""
	// A policy that has been applied is the established one; the proposed copy
	// is what the caller last submitted, which is how the read distinguishes
	// them.
	if !s3MultiRegionAccessPoints.Update(s3AccessPointKey(account, name),
		func(m *S3MultiRegionAccessPoint) { m.ProposedPolicy, m.EstablishedPolicy = policy, policy }) {
		errCode, errMessage = "NoSuchMultiRegionAccessPoint",
			"The specified Multi-Region Access Point does not exist"
	}
	s3WriteAsyncToken(w, "PutMultiRegionAccessPointPolicyResult",
		s3RecordAsyncOperation(account, "PutMultiRegionAccessPointPolicy", name, policy, errCode, errMessage))
}

func handleS3DescribeMultiRegionAccessPointOperation(w http.ResponseWriter, r *http.Request) {
	token := sim.PathParam(r, "token")
	operation, ok := s3AsyncOperations.Get(token)
	if !ok || operation.AccountID != s3ControlAccountID(r) {
		s3ControlError(w, "NoSuchAsyncRequest",
			"The specified asynchronous request does not exist", http.StatusNotFound)
		return
	}
	type errorDetails struct {
		Code    string `xml:"Code,omitempty"`
		Message string `xml:"Message,omitempty"`
	}
	type responseDetails struct {
		ErrorDetails *errorDetails `xml:"ErrorDetails,omitempty"`
	}
	type createRequest struct {
		Name string `xml:"Name"`
	}
	type policyRequest struct {
		Name   string `xml:"Name"`
		Policy string `xml:"Policy"`
	}
	type requestParameters struct {
		Create *createRequest `xml:"CreateMultiRegionAccessPointRequest,omitempty"`
		Delete *createRequest `xml:"DeleteMultiRegionAccessPointRequest,omitempty"`
		Policy *policyRequest `xml:"PutMultiRegionAccessPointPolicyRequest,omitempty"`
	}
	params := requestParameters{}
	switch operation.Operation {
	case "CreateMultiRegionAccessPoint":
		params.Create = &createRequest{Name: operation.Name}
	case "DeleteMultiRegionAccessPoint":
		params.Delete = &createRequest{Name: operation.Name}
	case "PutMultiRegionAccessPointPolicy":
		params.Policy = &policyRequest{Name: operation.Name, Policy: operation.Policy}
	}
	details := responseDetails{}
	if operation.ErrorCode != "" {
		details.ErrorDetails = &errorDetails{Code: operation.ErrorCode, Message: operation.ErrorMessage}
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName        xml.Name          `xml:"DescribeMultiRegionAccessPointOperationResult"`
		CreationTime   string            `xml:"AsyncOperation>CreationTime"`
		Operation      string            `xml:"AsyncOperation>Operation"`
		RequestToken   string            `xml:"AsyncOperation>RequestTokenARN"`
		Params         requestParameters `xml:"AsyncOperation>RequestParameters"`
		RequestStatus  string            `xml:"AsyncOperation>RequestStatus"`
		ResponseDetail responseDetails   `xml:"AsyncOperation>ResponseDetails"`
	}{
		CreationTime: operation.CreationTime, Operation: operation.Operation,
		RequestToken: operation.RequestTokenARN, Params: params,
		RequestStatus: operation.RequestStatus, ResponseDetail: details,
	})
}

// s3MultiRegionReport is the shape both the read and the list report an
// endpoint in.
type s3MultiRegionReport struct {
	Name              string              `xml:"Name"`
	Alias             string              `xml:"Alias"`
	CreatedAt         string              `xml:"CreatedAt"`
	PublicAccessBlock s3PublicAccessBlock `xml:"PublicAccessBlock"`
	Status            string              `xml:"Status"`
	Regions           []s3RegionReport    `xml:"Regions>Region"`
}

type s3RegionReport struct {
	Bucket          string `xml:"Bucket"`
	Region          string `xml:"Region"`
	BucketAccountID string `xml:"BucketAccountId,omitempty"`
}

func s3MultiRegionReportOf(mrap S3MultiRegionAccessPoint) s3MultiRegionReport {
	report := s3MultiRegionReport{
		Name: mrap.Name, Alias: mrap.Alias, CreatedAt: mrap.CreatedAt,
		PublicAccessBlock: mrap.PublicAccessBlock, Status: mrap.Status,
	}
	for _, region := range mrap.Regions {
		report.Regions = append(report.Regions, s3RegionReport{
			Bucket: region.Bucket, Region: region.Region, BucketAccountID: region.BucketAccountID,
		})
	}
	return report
}

func handleS3GetMultiRegionAccessPoint(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	mrap, ok := s3MultiRegionAccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchMultiRegionAccessPoint",
			"The specified Multi-Region Access Point does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName     xml.Name            `xml:"GetMultiRegionAccessPointResult"`
		AccessPoint s3MultiRegionReport `xml:"AccessPoint"`
	}{AccessPoint: s3MultiRegionReportOf(mrap)})
}

func handleS3ListMultiRegionAccessPoints(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	var items []s3MultiRegionReport
	for _, mrap := range s3MultiRegionAccessPoints.List() {
		if mrap.AccountID != account {
			continue
		}
		items = append(items, s3MultiRegionReportOf(mrap))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	WriteXML(w, http.StatusOK, struct {
		XMLName      xml.Name              `xml:"ListMultiRegionAccessPointsResult"`
		AccessPoints []s3MultiRegionReport `xml:"AccessPoints>AccessPoint"`
	}{AccessPoints: items})
}

func handleS3GetMultiRegionAccessPointPolicy(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	mrap, ok := s3MultiRegionAccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchMultiRegionAccessPoint",
			"The specified Multi-Region Access Point does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName     xml.Name `xml:"GetMultiRegionAccessPointPolicyResult"`
		Established string   `xml:"Policy>Established>Policy,omitempty"`
		Proposed    string   `xml:"Policy>Proposed>Policy,omitempty"`
	}{Established: mrap.EstablishedPolicy, Proposed: mrap.ProposedPolicy})
}

func handleS3GetMultiRegionAccessPointPolicyStatus(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	mrap, ok := s3MultiRegionAccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchMultiRegionAccessPoint",
			"The specified Multi-Region Access Point does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName  xml.Name `xml:"GetMultiRegionAccessPointPolicyStatusResult"`
		IsPublic bool     `xml:"Established>IsPublic"`
	}{IsPublic: s3PolicyIsPublic(mrap.EstablishedPolicy)})
}

func handleS3GetMultiRegionAccessPointRoutes(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), s3MultiRegionNameFromID(sim.PathParam(r, "name"))
	mrap, ok := s3MultiRegionAccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchMultiRegionAccessPoint",
			"The specified Multi-Region Access Point does not exist", http.StatusNotFound)
		return
	}
	type route struct {
		Bucket                string `xml:"Bucket"`
		Region                string `xml:"Region"`
		TrafficDialPercentage int    `xml:"TrafficDialPercentage"`
	}
	var routes []route
	for _, region := range mrap.Regions {
		routes = append(routes, route{
			Bucket: region.Bucket, Region: region.Region,
			TrafficDialPercentage: region.TrafficDialPercentage,
		})
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"GetMultiRegionAccessPointRoutesResult"`
		Mrap    string   `xml:"Mrap"`
		Routes  []route  `xml:"Routes>Route"`
	}{Mrap: mrap.Alias, Routes: routes})
}

// s3MultiRegionNameFromID accepts either spelling of the identifier the routes
// operations take — the endpoint's name, or its alias / ARN.
func s3MultiRegionNameFromID(id string) string {
	if i := strings.LastIndex(id, ":accesspoint/"); i >= 0 {
		id = id[i+len(":accesspoint/"):]
	}
	if i := strings.Index(id, "."); i >= 0 {
		id = id[:i]
	}
	return id
}

func handleS3SubmitMultiRegionAccessPointRoutes(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), s3MultiRegionNameFromID(sim.PathParam(r, "name"))
	body, ok := s3ControlReadXMLBody(w, r, "SubmitMultiRegionAccessPointRoutesRequest")
	if !ok {
		return
	}
	list, hasRoutes := body.Child("RouteUpdates")
	if !hasRoutes {
		s3ControlError(w, "InvalidRequest", "RouteUpdates is required", http.StatusBadRequest)
		return
	}
	updates := map[string]int{}
	for _, child := range list.Children {
		if child.Name != "Route" {
			continue
		}
		dial, err := strconv.Atoi(child.ChildText("TrafficDialPercentage"))
		if err != nil || dial < 0 || dial > 100 {
			s3ControlError(w, "InvalidRequest",
				"TrafficDialPercentage must be between 0 and 100", http.StatusBadRequest)
			return
		}
		// A route names the bucket, the region, or both; the bucket is what
		// identifies which regional endpoint the dial belongs to.
		key := child.ChildText("Bucket")
		if key == "" {
			key = child.ChildText("Region")
		}
		updates[key] = dial
	}
	if !s3MultiRegionAccessPoints.Update(s3AccessPointKey(account, name), func(m *S3MultiRegionAccessPoint) {
		for i := range m.Regions {
			if dial, ok := updates[m.Regions[i].Bucket]; ok {
				m.Regions[i].TrafficDialPercentage = dial
				continue
			}
			if dial, ok := updates[m.Regions[i].Region]; ok {
				m.Regions[i].TrafficDialPercentage = dial
			}
		}
	}) {
		s3ControlError(w, "NoSuchMultiRegionAccessPoint",
			"The specified Multi-Region Access Point does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}
