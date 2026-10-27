package main

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A connection function is versioned like a CloudFront Function (DEVELOPMENT
// → LIVE via PublishConnectionFunction) but lives under
// /2020-05-31/connection-function. The simulator stores its code verbatim and
// does not interpret it.

// The Smithy FunctionConfig shape is shared with CloudFront Functions. No
// XMLName: the enclosing field tag names the element, which differs by
// operation.
type CFConnectionFunctionConfig struct {
	Comment                   string                       `xml:"Comment"`
	Runtime                   string                       `xml:"Runtime"`
	KeyValueStoreAssociations *CFKeyValueStoreAssociations `xml:"KeyValueStoreAssociations,omitempty"`
}

// Field order and names follow the Smithy ConnectionFunctionSummary shape.
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

type cfCreateConnectionFunctionRequest struct {
	XMLName                  xml.Name                   `xml:"CreateConnectionFunctionRequest"`
	Name                     string                     `xml:"Name"`
	ConnectionFunctionConfig CFConnectionFunctionConfig `xml:"ConnectionFunctionConfig"`
	ConnectionFunctionCode   []byte                     `xml:"ConnectionFunctionCode"`
	Tags                     *CFTags                    `xml:"Tags,omitempty"`
}

type cfUpdateConnectionFunctionRequest struct {
	XMLName                  xml.Name                   `xml:"UpdateConnectionFunctionRequest"`
	ConnectionFunctionConfig CFConnectionFunctionConfig `xml:"ConnectionFunctionConfig"`
	ConnectionFunctionCode   []byte                     `xml:"ConnectionFunctionCode"`
}

type cfListConnectionFunctionsRequest struct {
	XMLName  xml.Name `xml:"ListConnectionFunctionsRequest"`
	Marker   string   `xml:"Marker,omitempty"`
	MaxItems *int     `xml:"MaxItems,omitempty"`
	Stage    string   `xml:"Stage,omitempty"`
}

type cfTestConnectionFunctionRequest struct {
	XMLName          xml.Name `xml:"TestConnectionFunctionRequest"`
	Stage            string   `xml:"Stage,omitempty"`
	ConnectionObject []byte   `xml:"ConnectionObject"`
}

// Members sit under <ConnectionFunctions><ConnectionFunctionSummary>, per
// the Smithy ConnectionFunctionSummaryList shape.
type CFConnectionFunctionList struct {
	XMLName             xml.Name                      `xml:"ListConnectionFunctionsResult"`
	Xmlns               string                        `xml:"xmlns,attr,omitempty"`
	NextMarker          string                        `xml:"NextMarker,omitempty"`
	ConnectionFunctions []CFConnectionFunctionSummary `xml:"ConnectionFunctions>ConnectionFunctionSummary,omitempty"`
}

type CFConnectionFunctionTestResult struct {
	XMLName                         xml.Name                    `xml:"ConnectionFunctionTestResult"`
	Xmlns                           string                      `xml:"xmlns,attr,omitempty"`
	ConnectionFunctionSummary       CFConnectionFunctionSummary `xml:"ConnectionFunctionSummary"`
	ComputeUtilization              string                      `xml:"ComputeUtilization"`
	ConnectionFunctionExecutionLogs *CFExecutionLogs            `xml:"ConnectionFunctionExecutionLogs,omitempty"`
	ConnectionFunctionErrorMessage  string                      `xml:"ConnectionFunctionErrorMessage,omitempty"`
	ConnectionFunctionOutput        string                      `xml:"ConnectionFunctionOutput,omitempty"`
}

type CFTestResult struct {
	XMLName               xml.Name          `xml:"TestResult"`
	Xmlns                 string            `xml:"xmlns,attr,omitempty"`
	FunctionSummary       CFFunctionSummary `xml:"FunctionSummary"`
	ComputeUtilization    string            `xml:"ComputeUtilization"`
	FunctionExecutionLogs *CFExecutionLogs  `xml:"FunctionExecutionLogs,omitempty"`
	FunctionErrorMessage  string            `xml:"FunctionErrorMessage,omitempty"`
	FunctionOutput        string            `xml:"FunctionOutput,omitempty"`
}

// FunctionExecutionLogList members are bare <member> elements.
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

// encoding/xml leaves the SDK's base64 text verbatim in a []byte, so decode
// it here. The CLI passes raw bytes, so return a non-base64 value unchanged.
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

// The /tagging and create-distribution dispatchers in cloudfront.go already
// serve some operations here; register their names statically so the
// conformance gate counts them.
func registerCloudFrontComplete(srv *sim.Server) {
	cfConnectionFunctions = sim.MakeStore[cfStoredConnectionFunction](srv.DB(), "cloudfront_connection_functions")

	mux := srv
	const src = "cloudfront.amazonaws.com"
	v := cfAPIVersion
	cnxResource := cloudTrailRESTResource("AWS::CloudFront::ConnectionFunction", "Identifier", "Id")
	fnResource := cloudTrailRESTResource("AWS::CloudFront::Function", "Name")

	mux.HandleFunc("POST /"+v+"/connection-function", cloudTrailRecordedREST("CreateConnectionFunction", src, nil, handleCFCreateConnectionFunction))
	mux.HandleFunc("POST /"+v+"/connection-functions", cloudTrailRecordedREST("ListConnectionFunctions", src, nil, handleCFListConnectionFunctions))
	mux.HandleFunc("GET /"+v+"/connection-function/{Identifier}/describe", cloudTrailRecordedREST("DescribeConnectionFunction", src, cnxResource, handleCFDescribeConnectionFunction))
	mux.HandleFunc("GET /"+v+"/connection-function/{Identifier}", cloudTrailRecordedREST("GetConnectionFunction", src, cnxResource, handleCFGetConnectionFunction))
	mux.HandleFunc("PUT /"+v+"/connection-function/{Id}", cloudTrailRecordedREST("UpdateConnectionFunction", src, cnxResource, handleCFUpdateConnectionFunction))
	mux.HandleFunc("DELETE /"+v+"/connection-function/{Id}", cloudTrailRecordedREST("DeleteConnectionFunction", src, cnxResource, handleCFDeleteConnectionFunction))
	mux.HandleFunc("POST /"+v+"/connection-function/{Id}/publish", cloudTrailRecordedREST("PublishConnectionFunction", src, cnxResource, handleCFPublishConnectionFunction))
	mux.HandleFunc("POST /"+v+"/connection-function/{Id}/test", cloudTrailRecordedREST("TestConnectionFunction", src, cnxResource, handleCFTestConnectionFunction))
	mux.HandleFunc("GET /"+v+"/distributionsByConnectionFunction", cloudTrailRecordedREST("ListDistributionsByConnectionFunction", src, nil, handleCFListDistributionsByConnectionFunction))

	mux.HandleFunc("POST /"+v+"/function/{Name}/test", cloudTrailRecordedREST("TestFunction", src, fnResource, handleCFTestFunction))

	// handleCFTagDispatch serves POST /tagging?Operation=Tag|Untag.
	restRegisterOp(src, "TagResource")
	restRegisterOp(src, "UntagResource")

	// handleCFCreateDistribution serves the POST /distribution?WithTags variants.
	restRegisterOp(src, "CreateDistribution")
	restRegisterOp(src, "CreateDistributionWithTags")
	// The dynamic streaming-distribution create route registers neither name
	// through cloudTrailRecordedREST.
	restRegisterOp(src, "CreateStreamingDistribution")
	restRegisterOp(src, "CreateStreamingDistributionWithTags")
}

func handleCFCreateConnectionFunction(w http.ResponseWriter, r *http.Request) {
	var req cfCreateConnectionFunctionRequest
	if !cfDecodeXML(w, r, "CreateConnectionFunctionRequest", &req) {
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

// The GET path's Identifier may be an ID or a name.
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

// Like GetFunction: the code in the body, the ETag in a header.
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
	if !cfDecodeXML(w, r, "UpdateConnectionFunctionRequest", &req) {
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
	if !cfDecodeXML(w, r, "TestConnectionFunctionRequest", &req) {
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
		// The body is optional.
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

// Distributions carry no connection-function association in the simulator,
// so the list is empty.
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
	if !cfIfMatches(w, r, stored.ETag) {
		return
	}
	var req cfTestFunctionRequest
	if !cfDecodeXML(w, r, "TestFunctionRequest", &req) {
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
		cfWriteIfMatchError(w, "InvalidIfMatchVersion")
		return
	}
	cfWriteIfMatchError(w, "PreconditionFailed")
}
