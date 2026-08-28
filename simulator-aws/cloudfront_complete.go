package main

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudFront connection functions, the CloudFront Functions test endpoint,
// tagging op-name registration, and static op-name registration for the
// create-distribution variants that the shared dynamic dispatcher already
// services. All restXml, sharing the cloudfront.amazonaws.com namespace,
// ETag headers, and If-Match semantics with cloudfront.go.
//
// A CloudFront connection function is a named, versioned resource (stage
// DEVELOPMENT → LIVE via PublishConnectionFunction) with an ARN + ETag. It
// mirrors the CloudFront Functions resource shape but lives at its own
// /2020-05-31/connection-function path. The simulator stores and returns the
// connection-function code verbatim; it does not interpret the JS.

// CFConnectionFunctionConfig reuses the same Comment/Runtime/KeyValueStore
// shape CloudFront Functions use (the smithy FunctionConfig shape is shared).
// It carries no XMLName: the element name is always set by the enclosing field
// tag (ConnectionFunctionConfig), which differs by op from a bare FunctionConfig.
type CFConnectionFunctionConfig struct {
	Comment                   string                       `xml:"Comment"`
	Runtime                   string                       `xml:"Runtime"`
	KeyValueStoreAssociations *CFKeyValueStoreAssociations `xml:"KeyValueStoreAssociations,omitempty"`
}

// CFConnectionFunctionSummary is the response payload element for the
// connection-function CRUD ops. Field order and names mirror the smithy
// ConnectionFunctionSummary shape so the spec-shape validator passes.
type CFConnectionFunctionSummary struct {
	XMLName                  xml.Name                   `xml:"ConnectionFunctionSummary"`
	Xmlns                    string                     `xml:"xmlns,attr,omitempty"`
	Name                     string                     `xml:"Name"`
	Id                       string                     `xml:"Id"`
	ConnectionFunctionConfig CFConnectionFunctionConfig `xml:"ConnectionFunctionConfig"`
	ConnectionFunctionArn    string                     `xml:"ConnectionFunctionArn"`
	Status                   string                     `xml:"Status"`
	Stage                    string                     `xml:"Stage"`
	CreatedTime              string                     `xml:"CreatedTime"`
	LastModifiedTime         string                     `xml:"LastModifiedTime"`
}

// cfCreateConnectionFunctionRequest mirrors the SDK body wrapper for
// POST /connection-function (root element CreateConnectionFunctionRequest).
type cfCreateConnectionFunctionRequest struct {
	XMLName                  xml.Name                   `xml:"CreateConnectionFunctionRequest"`
	Name                     string                     `xml:"Name"`
	ConnectionFunctionConfig CFConnectionFunctionConfig `xml:"ConnectionFunctionConfig"`
	ConnectionFunctionCode   []byte                     `xml:"ConnectionFunctionCode"`
	Tags                     *CFTags                    `xml:"Tags,omitempty"`
}

// cfUpdateConnectionFunctionRequest mirrors the SDK body for
// PUT /connection-function/{Id} (root UpdateConnectionFunctionRequest).
type cfUpdateConnectionFunctionRequest struct {
	XMLName                  xml.Name                   `xml:"UpdateConnectionFunctionRequest"`
	ConnectionFunctionConfig CFConnectionFunctionConfig `xml:"ConnectionFunctionConfig"`
	ConnectionFunctionCode   []byte                     `xml:"ConnectionFunctionCode"`
}

// cfListConnectionFunctionsRequest mirrors the SDK body for the POST list op.
type cfListConnectionFunctionsRequest struct {
	XMLName  xml.Name `xml:"ListConnectionFunctionsRequest"`
	Marker   string   `xml:"Marker,omitempty"`
	MaxItems *int     `xml:"MaxItems,omitempty"`
	Stage    string   `xml:"Stage,omitempty"`
}

// cfTestConnectionFunctionRequest mirrors the SDK body for the test op.
type cfTestConnectionFunctionRequest struct {
	XMLName          xml.Name `xml:"TestConnectionFunctionRequest"`
	Stage            string   `xml:"Stage,omitempty"`
	ConnectionObject []byte   `xml:"ConnectionObject"`
}

// CFConnectionFunctionList is the ListConnectionFunctions response. The root
// element is the operation output shape (ListConnectionFunctionsResult); the
// list members live under <ConnectionFunctions><ConnectionFunctionSummary>…,
// per the smithy ConnectionFunctionSummaryList shape.
type CFConnectionFunctionList struct {
	XMLName             xml.Name                      `xml:"ListConnectionFunctionsResult"`
	Xmlns               string                        `xml:"xmlns,attr,omitempty"`
	NextMarker          string                        `xml:"NextMarker,omitempty"`
	ConnectionFunctions []CFConnectionFunctionSummary `xml:"ConnectionFunctions>ConnectionFunctionSummary,omitempty"`
}

// CFConnectionFunctionTestResult is the TestConnectionFunction response payload.
type CFConnectionFunctionTestResult struct {
	XMLName                         xml.Name                    `xml:"ConnectionFunctionTestResult"`
	Xmlns                           string                      `xml:"xmlns,attr,omitempty"`
	ConnectionFunctionSummary       CFConnectionFunctionSummary `xml:"ConnectionFunctionSummary"`
	ComputeUtilization              string                      `xml:"ComputeUtilization"`
	ConnectionFunctionExecutionLogs *CFExecutionLogs            `xml:"ConnectionFunctionExecutionLogs,omitempty"`
	ConnectionFunctionErrorMessage  string                      `xml:"ConnectionFunctionErrorMessage,omitempty"`
	ConnectionFunctionOutput        string                      `xml:"ConnectionFunctionOutput,omitempty"`
}

// CFTestResult is the TestFunction response payload (httpPayload TestResult).
type CFTestResult struct {
	XMLName               xml.Name          `xml:"TestResult"`
	Xmlns                 string            `xml:"xmlns,attr,omitempty"`
	FunctionSummary       CFFunctionSummary `xml:"FunctionSummary"`
	ComputeUtilization    string            `xml:"ComputeUtilization"`
	FunctionExecutionLogs *CFExecutionLogs  `xml:"FunctionExecutionLogs,omitempty"`
	FunctionErrorMessage  string            `xml:"FunctionErrorMessage,omitempty"`
	FunctionOutput        string            `xml:"FunctionOutput,omitempty"`
}

// CFExecutionLogs is the FunctionExecutionLogList shape — a list whose members
// are bare <member> elements (smithy default member name).
type CFExecutionLogs struct {
	Items []string `xml:"member,omitempty"`
}

type cfStoredConnectionFunction struct {
	Summary CFConnectionFunctionSummary
	Code    []byte
	ETag    string
	Tags    []CFTag
}

var cfConnectionFunctions sim.Store[cfStoredConnectionFunction]

func cfConnectionFunctionARN(id string) string {
	return "arn:aws:cloudfront::" + awsAccountID() + ":connection-function/" + id
}

// cfDecodeBlob undoes the base64 the SDK applies to a blob XML member
// (FunctionBlob / FunctionEventObject). Go's encoding/xml leaves the base64
// text verbatim in a []byte field, so we decode it to recover the raw bytes
// the GetConnectionFunction payload (a raw FunctionBlob) must return. A value
// that is not valid base64 is returned unchanged (the CLI passes raw bytes).
func cfDecodeBlob(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	decoded, err := base64.StdEncoding.DecodeString(string(b))
	if err != nil {
		return b
	}
	return decoded
}

// registerCloudFrontComplete mounts the connection-function CRUD + lifecycle,
// the CloudFront Functions TestFunction op, registers the tagging op names that
// the shared /tagging dynamic dispatcher in cloudfront.go already services, and
// statically records the create-distribution variant op names that the shared
// POST /distribution and POST /streaming-distribution dynamic dispatchers
// already service. Invoked from registerCloudFront in cloudfront.go.
func registerCloudFrontComplete(srv *sim.Server) {
	cfConnectionFunctions = sim.MakeStore[cfStoredConnectionFunction](srv.DB(), "cloudfront_connection_functions")

	mux := srv
	const src = "cloudfront.amazonaws.com"
	v := cfAPIVersion
	cnxResource := cloudTrailRESTResource("AWS::CloudFront::ConnectionFunction", "Identifier", "Id")
	fnResource := cloudTrailRESTResource("AWS::CloudFront::Function", "Name")

	// Connection functions.
	mux.HandleFunc("POST /"+v+"/connection-function", cloudTrailRecordedREST("CreateConnectionFunction", src, nil, handleCFCreateConnectionFunction))
	mux.HandleFunc("POST /"+v+"/connection-functions", cloudTrailRecordedREST("ListConnectionFunctions", src, nil, handleCFListConnectionFunctions))
	mux.HandleFunc("GET /"+v+"/connection-function/{Identifier}/describe", cloudTrailRecordedREST("DescribeConnectionFunction", src, cnxResource, handleCFDescribeConnectionFunction))
	mux.HandleFunc("GET /"+v+"/connection-function/{Identifier}", cloudTrailRecordedREST("GetConnectionFunction", src, cnxResource, handleCFGetConnectionFunction))
	mux.HandleFunc("PUT /"+v+"/connection-function/{Id}", cloudTrailRecordedREST("UpdateConnectionFunction", src, cnxResource, handleCFUpdateConnectionFunction))
	mux.HandleFunc("DELETE /"+v+"/connection-function/{Id}", cloudTrailRecordedREST("DeleteConnectionFunction", src, cnxResource, handleCFDeleteConnectionFunction))
	mux.HandleFunc("POST /"+v+"/connection-function/{Id}/publish", cloudTrailRecordedREST("PublishConnectionFunction", src, cnxResource, handleCFPublishConnectionFunction))
	mux.HandleFunc("POST /"+v+"/connection-function/{Id}/test", cloudTrailRecordedREST("TestConnectionFunction", src, cnxResource, handleCFTestConnectionFunction))
	mux.HandleFunc("GET /"+v+"/distributionsByConnectionFunction", cloudTrailRecordedREST("ListDistributionsByConnectionFunction", src, nil, handleCFListDistributionsByConnectionFunction))

	// CloudFront Functions test endpoint.
	mux.HandleFunc("POST /"+v+"/function/{Name}/test", cloudTrailRecordedREST("TestFunction", src, fnResource, handleCFTestFunction))

	// Tagging op names — the request itself is serviced by the shared
	// POST /tagging?Operation=Tag|Untag dynamic dispatcher in cloudfront.go
	// (handleCFTagDispatch). Register the static op names so the conformance
	// gate counts them.
	restRegisterOp(src, "TagResource")
	restRegisterOp(src, "UntagResource")

	// Create-distribution variant op names — the requests are serviced by the
	// shared POST /distribution?WithTags dynamic dispatcher (handleCFCreateDistribution)
	// and the POST /streaming-distribution?WithTags route (handled below). Register
	// the static op names so the gate counts each working variant.
	restRegisterOp(src, "CreateDistribution")
	restRegisterOp(src, "CreateDistributionWithTags")
	// The streaming-distribution create route in cloudfront_extras2.go is a
	// dynamic dispatcher (it serves both the plain and ?WithTags variants), so
	// neither op name is auto-registered by cloudTrailRecordedREST. Register
	// both static names here so the conformance gate counts each working op.
	restRegisterOp(src, "CreateStreamingDistribution")
	restRegisterOp(src, "CreateStreamingDistributionWithTags")
}

func handleCFCreateConnectionFunction(w http.ResponseWriter, r *http.Request) {
	var req cfCreateConnectionFunctionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateConnectionFunctionRequest: "+err.Error())
		return
	}
	if req.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	if req.ConnectionFunctionConfig.Runtime == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "ConnectionFunctionConfig.Runtime is required")
		return
	}
	if cfConnectionFunctionExists(req.Name) {
		cfWriteError(w, http.StatusConflict, "EntityAlreadyExists", "A connection function with the same name already exists.")
		return
	}
	id := cfRandomID("cf-cnx-fn-")
	etag := cfETag()
	now := cfNowISO()
	summary := CFConnectionFunctionSummary{
		Xmlns:                    cfNamespace,
		Name:                     req.Name,
		Id:                       id,
		ConnectionFunctionConfig: req.ConnectionFunctionConfig,
		ConnectionFunctionArn:    cfConnectionFunctionARN(id),
		Status:                   "UNPUBLISHED",
		Stage:                    "DEVELOPMENT",
		CreatedTime:              now,
		LastModifiedTime:         now,
	}
	var tags []CFTag
	if req.Tags != nil {
		tags = req.Tags.Items
	}
	cfConnectionFunctions.Put(id, cfStoredConnectionFunction{Summary: summary, Code: cfDecodeBlob(req.ConnectionFunctionCode), ETag: etag, Tags: tags})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/connection-function/"+id)
	cfWriteXML(w, http.StatusCreated, summary)
}

// cfResolveConnectionFunction looks a connection function up by ID first, then
// by name (the GET path is keyed on an Identifier that may be either).
func cfResolveConnectionFunction(identifier string) (cfStoredConnectionFunction, bool) {
	if stored, ok := cfConnectionFunctions.Get(identifier); ok {
		return stored, true
	}
	for _, stored := range cfConnectionFunctions.List() {
		if stored.Summary.Name == identifier {
			return stored, true
		}
	}
	return cfStoredConnectionFunction{}, false
}

func cfConnectionFunctionExists(name string) bool {
	for _, stored := range cfConnectionFunctions.List() {
		if stored.Summary.Name == name {
			return true
		}
	}
	return false
}

func handleCFDescribeConnectionFunction(w http.ResponseWriter, r *http.Request) {
	stored, ok := cfResolveConnectionFunction(r.PathValue("Identifier"))
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified connection function does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Summary)
}

// handleCFGetConnectionFunction returns the connection-function code in the
// body (octet-stream), with the ETag in a header — mirroring GetFunction.
func handleCFGetConnectionFunction(w http.ResponseWriter, r *http.Request) {
	stored, ok := cfResolveConnectionFunction(r.PathValue("Identifier"))
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified connection function does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(stored.Code)
}

func handleCFUpdateConnectionFunction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfConnectionFunctions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified connection function does not exist.")
		return
	}
	if msg := cfConnectionRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteConnectionIfMatchError(w, msg)
		return
	}
	var req cfUpdateConnectionFunctionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateConnectionFunctionRequest: "+err.Error())
		return
	}
	newETag := cfETag()
	stored.Summary.ConnectionFunctionConfig = req.ConnectionFunctionConfig
	stored.Summary.LastModifiedTime = cfNowISO()
	stored.Code = cfDecodeBlob(req.ConnectionFunctionCode)
	stored.ETag = newETag
	cfConnectionFunctions.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Summary)
}

func handleCFDeleteConnectionFunction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfConnectionFunctions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified connection function does not exist.")
		return
	}
	if msg := cfConnectionRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteConnectionIfMatchError(w, msg)
		return
	}
	cfConnectionFunctions.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFPublishConnectionFunction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfConnectionFunctions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified connection function does not exist.")
		return
	}
	if msg := cfConnectionRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteConnectionIfMatchError(w, msg)
		return
	}
	stored.Summary.Stage = "LIVE"
	stored.Summary.Status = "DEPLOYED"
	stored.Summary.LastModifiedTime = cfNowISO()
	cfConnectionFunctions.Put(id, stored)
	cfWriteXML(w, http.StatusOK, stored.Summary)
}

func handleCFTestConnectionFunction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfConnectionFunctions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified connection function does not exist.")
		return
	}
	if msg := cfConnectionRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteConnectionIfMatchError(w, msg)
		return
	}
	var req cfTestConnectionFunctionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode TestConnectionFunctionRequest: "+err.Error())
		return
	}
	if len(req.ConnectionObject) == 0 {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "ConnectionObject is required")
		return
	}
	result := CFConnectionFunctionTestResult{
		Xmlns:                     cfNamespace,
		ConnectionFunctionSummary: stored.Summary,
		ComputeUtilization:        "0",
		ConnectionFunctionExecutionLogs: &CFExecutionLogs{
			Items: []string{},
		},
		ConnectionFunctionOutput: string(cfDecodeBlob(req.ConnectionObject)),
	}
	cfWriteXML(w, http.StatusOK, result)
}

func handleCFListConnectionFunctions(w http.ResponseWriter, r *http.Request) {
	var req cfListConnectionFunctionsRequest
	if r.ContentLength != 0 {
		// The body is optional; ignore a decode error on an empty/blank body.
		_ = xml.NewDecoder(r.Body).Decode(&req)
	}
	items := []CFConnectionFunctionSummary{}
	for _, stored := range cfConnectionFunctions.List() {
		if req.Stage != "" && !strings.EqualFold(stored.Summary.Stage, req.Stage) {
			continue
		}
		items = append(items, stored.Summary)
	}
	list := CFConnectionFunctionList{
		Xmlns:               cfNamespace,
		ConnectionFunctions: items,
	}
	cfWriteXML(w, http.StatusOK, list)
}

// handleCFListDistributionsByConnectionFunction returns the distributions that
// reference the given connection function. The simulator does not yet model a
// connection-function association on distributions, so this honestly returns an
// empty DistributionList (the same shape ListDistributions returns).
func handleCFListDistributionsByConnectionFunction(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("ConnectionFunctionIdentifier") == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "ConnectionFunctionIdentifier query parameter is required")
		return
	}
	list := CFDistributionList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: 0,
		Items:    []CFDistributionSummary{},
	}
	cfWriteXML(w, http.StatusOK, list)
}

// cfTestFunctionRequest mirrors the SDK body for POST /function/{Name}/test
// (root TestFunctionRequest).
type cfTestFunctionRequest struct {
	XMLName     xml.Name `xml:"TestFunctionRequest"`
	Stage       string   `xml:"Stage,omitempty"`
	EventObject []byte   `xml:"EventObject"`
}

func handleCFTestFunction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("Name")
	stored, ok := cfFunctions.Get(name)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFunctionExists", "The specified function does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	var req cfTestFunctionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode TestFunctionRequest: "+err.Error())
		return
	}
	if len(req.EventObject) == 0 {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "EventObject is required")
		return
	}
	result := CFTestResult{
		Xmlns:              cfNamespace,
		FunctionSummary:    stored.Summary,
		ComputeUtilization: "0",
		FunctionExecutionLogs: &CFExecutionLogs{
			Items: []string{},
		},
		FunctionOutput: string(cfDecodeBlob(req.EventObject)),
	}
	cfWriteXML(w, http.StatusOK, result)
}

func cfConnectionRequireIfMatch(r *http.Request, current string) string {
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		return "missing"
	}
	if ifMatch != current {
		return "mismatch"
	}
	return ""
}

func cfWriteConnectionIfMatchError(w http.ResponseWriter, msg string) {
	if msg == "missing" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
}
