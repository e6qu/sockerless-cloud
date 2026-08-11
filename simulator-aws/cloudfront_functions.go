package main

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudFront Functions + Invalidations.
// Functions live at /2020-05-31/function and follow the Stage state
// machine (DEVELOPMENT → LIVE via PublishFunction). The simulator
// stores + returns the function code verbatim; it does NOT interpret
// the JS (out of scope, intentional limitation).
// Invalidations live at /2020-05-31/distribution/{DistributionId}/invalidation
// and only matter for the create/get/list flow — they're operations,
// not state, so no Terraform resource references them.

// ---------- Function types ----------

type CFFunctionConfig struct {
	XMLName                   xml.Name                     `xml:"FunctionConfig"`
	Comment                   string                       `xml:"Comment"`
	Runtime                   string                       `xml:"Runtime"`
	KeyValueStoreAssociations *CFKeyValueStoreAssociations `xml:"KeyValueStoreAssociations,omitempty"`
}

type CFKeyValueStoreAssociations struct {
	Quantity int                          `xml:"Quantity"`
	Items    []CFKeyValueStoreAssociation `xml:"Items>KeyValueStoreAssociation,omitempty"`
}

type CFKeyValueStoreAssociation struct {
	KeyValueStoreARN string `xml:"KeyValueStoreARN"`
}

type CFFunctionMetadata struct {
	FunctionARN      string `xml:"FunctionARN"`
	Stage            string `xml:"Stage"`
	CreatedTime      string `xml:"CreatedTime,omitempty"`
	LastModifiedTime string `xml:"LastModifiedTime"`
}

type CFFunctionSummary struct {
	XMLName          xml.Name           `xml:"FunctionSummary"`
	Xmlns            string             `xml:"xmlns,attr,omitempty"`
	Name             string             `xml:"Name"`
	Status           string             `xml:"Status,omitempty"`
	FunctionConfig   CFFunctionConfig   `xml:"FunctionConfig"`
	FunctionMetadata CFFunctionMetadata `xml:"FunctionMetadata"`
}

type CFFunctionList struct {
	XMLName    xml.Name            `xml:"FunctionList"`
	Xmlns      string              `xml:"xmlns,attr,omitempty"`
	MaxItems   int                 `xml:"MaxItems"`
	Quantity   int                 `xml:"Quantity"`
	NextMarker string              `xml:"NextMarker,omitempty"`
	Items      []CFFunctionSummary `xml:"Items>FunctionSummary,omitempty"`
}

// CreateFunctionRequest mirrors the SDK body wrapper.
type cfCreateFunctionRequest struct {
	XMLName        xml.Name         `xml:"CreateFunctionRequest"`
	Name           string           `xml:"Name"`
	FunctionConfig CFFunctionConfig `xml:"FunctionConfig"`
	FunctionCode   []byte           `xml:"FunctionCode"`
	Tags           *CFTags          `xml:"Tags,omitempty"`
}

// UpdateFunctionRequest mirrors the SDK body for PUT /function/{Name}.
type cfUpdateFunctionRequest struct {
	XMLName        xml.Name         `xml:"UpdateFunctionRequest"`
	FunctionConfig CFFunctionConfig `xml:"FunctionConfig"`
	FunctionCode   []byte           `xml:"FunctionCode"`
}

type cfStoredFunction struct {
	Summary CFFunctionSummary
	Code    []byte
	ETag    string
	Tags    []CFTag
}

func cfDecodeFunctionCode(encoded []byte) ([]byte, error) {
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(decoded, encoded)
	if err != nil {
		return nil, err
	}
	return decoded[:n], nil
}

// ---------- Invalidation types ----------

type CFInvalidationBatch struct {
	XMLName         xml.Name `xml:"InvalidationBatch"`
	Xmlns           string   `xml:"xmlns,attr,omitempty"`
	CallerReference string   `xml:"CallerReference"`
	Paths           CFPaths  `xml:"Paths"`
}

type CFPaths struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Path,omitempty"`
}

type CFInvalidation struct {
	XMLName           xml.Name            `xml:"Invalidation"`
	Xmlns             string              `xml:"xmlns,attr,omitempty"`
	Id                string              `xml:"Id"`
	Status            string              `xml:"Status"`
	CreateTime        string              `xml:"CreateTime"`
	InvalidationBatch CFInvalidationBatch `xml:"InvalidationBatch"`
}

type CFInvalidationSummary struct {
	Id         string `xml:"Id"`
	CreateTime string `xml:"CreateTime"`
	Status     string `xml:"Status"`
}

type CFInvalidationList struct {
	XMLName     xml.Name                `xml:"InvalidationList"`
	Xmlns       string                  `xml:"xmlns,attr,omitempty"`
	Marker      string                  `xml:"Marker,omitempty"`
	NextMarker  string                  `xml:"NextMarker,omitempty"`
	MaxItems    int                     `xml:"MaxItems"`
	IsTruncated bool                    `xml:"IsTruncated"`
	Quantity    int                     `xml:"Quantity"`
	Items       []CFInvalidationSummary `xml:"Items>InvalidationSummary,omitempty"`
}

type cfStoredInvalidation struct {
	Invalidation   CFInvalidation
	DistributionID string
}

// ---------- State ----------

var (
	cfFunctions     sim.Store[cfStoredFunction]
	cfInvalidations sim.Store[cfStoredInvalidation]
)

func cfFunctionARN(name, stage string) string {
	return "arn:aws:cloudfront::" + awsAccountID() + ":function/" + name + "/" + stage
}

// registerCloudFrontFunctions registers the function + invalidation routes.
// Invoked from registerCloudFront() in cloudfront.go.
func registerCloudFrontFunctions(srv *sim.Server) {
	cfFunctions = sim.MakeStore[cfStoredFunction](srv.DB(), "cloudfront_functions")
	cfInvalidations = sim.MakeStore[cfStoredInvalidation](srv.DB(), "cloudfront_invalidations")

	mux := srv

	functionResource := cloudTrailRESTResource("AWS::CloudFront::Function", "name")
	distributionResource := cloudTrailRESTResource("AWS::CloudFront::Distribution", "distId")
	// Functions
	mux.HandleFunc("POST /"+cfAPIVersion+"/function", cloudTrailRecordedREST("CreateFunction", "cloudfront.amazonaws.com", nil, handleCFCreateFunction))
	mux.HandleFunc("GET /"+cfAPIVersion+"/function", cloudTrailRecordedREST("ListFunctions", "cloudfront.amazonaws.com", nil, handleCFListFunctions))
	mux.HandleFunc("GET /"+cfAPIVersion+"/function/{name}/describe", cloudTrailRecordedREST("DescribeFunction", "cloudfront.amazonaws.com", functionResource, handleCFDescribeFunction))
	mux.HandleFunc("GET /"+cfAPIVersion+"/function/{name}", cloudTrailRecordedREST("GetFunction", "cloudfront.amazonaws.com", functionResource, handleCFGetFunction))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/function/{name}", cloudTrailRecordedREST("UpdateFunction", "cloudfront.amazonaws.com", functionResource, handleCFUpdateFunction))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/function/{name}", cloudTrailRecordedREST("DeleteFunction", "cloudfront.amazonaws.com", functionResource, handleCFDeleteFunction))
	mux.HandleFunc("POST /"+cfAPIVersion+"/function/{name}/publish", cloudTrailRecordedREST("PublishFunction", "cloudfront.amazonaws.com", functionResource, handleCFPublishFunction))

	// Invalidations
	mux.HandleFunc("POST /"+cfAPIVersion+"/distribution/{distId}/invalidation", cloudTrailRecordedREST("CreateInvalidation", "cloudfront.amazonaws.com", distributionResource, handleCFCreateInvalidation))
	mux.HandleFunc("GET /"+cfAPIVersion+"/distribution/{distId}/invalidation", cloudTrailRecordedREST("ListInvalidations", "cloudfront.amazonaws.com", distributionResource, handleCFListInvalidations))
	mux.HandleFunc("GET /"+cfAPIVersion+"/distribution/{distId}/invalidation/{id}", cloudTrailRecordedREST("GetInvalidation", "cloudfront.amazonaws.com", distributionResource, handleCFGetInvalidation))
}

// ----- Function handlers -----

func handleCFCreateFunction(w http.ResponseWriter, r *http.Request) {
	var req cfCreateFunctionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateFunctionRequest: "+err.Error())
		return
	}
	if req.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	if req.FunctionConfig.Runtime == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "FunctionConfig.Runtime is required")
		return
	}
	if _, exists := cfFunctions.Get(req.Name); exists {
		cfWriteError(w, http.StatusConflict, "FunctionAlreadyExists", "A function with the same name already exists.")
		return
	}
	etag := cfETag()
	now := cfNowISO()
	summary := CFFunctionSummary{
		Xmlns:          cfNamespace,
		Name:           req.Name,
		Status:         "UNPUBLISHED",
		FunctionConfig: req.FunctionConfig,
		FunctionMetadata: CFFunctionMetadata{
			FunctionARN:      cfFunctionARN(req.Name, "DEVELOPMENT"),
			Stage:            "DEVELOPMENT",
			CreatedTime:      now,
			LastModifiedTime: now,
		},
	}
	code, err := cfDecodeFunctionCode(req.FunctionCode)
	if err != nil {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "FunctionCode must be valid base64.")
		return
	}
	cfFunctions.Put(req.Name, cfStoredFunction{Summary: summary, Code: code, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/function/"+req.Name)
	cfWriteXML(w, http.StatusCreated, summary)
}

func handleCFDescribeFunction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	stored, ok := cfFunctions.Get(name)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFunctionExists", "The specified function does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Summary)
}

// GetFunction returns the function code in the body and metadata in the
// ETag header. AWS uses Content-Type=application/octet-stream; the
// FunctionSummary header is X-Amz-Cf-Functionsummary (skipped in sim).
func handleCFGetFunction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	stored, ok := cfFunctions.Get(name)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFunctionExists", "The specified function does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(stored.Code)
}

func handleCFUpdateFunction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
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
	var req cfUpdateFunctionRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateFunctionRequest: "+err.Error())
		return
	}
	newETag := cfETag()
	stored.Summary.FunctionConfig = req.FunctionConfig
	stored.Summary.FunctionMetadata.LastModifiedTime = cfNowISO()
	code, err := cfDecodeFunctionCode(req.FunctionCode)
	if err != nil {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "FunctionCode must be valid base64.")
		return
	}
	stored.Code = code
	stored.ETag = newETag
	cfFunctions.Put(name, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Summary)
}

func handleCFDeleteFunction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
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
	cfFunctions.Delete(name)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFPublishFunction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
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
	stored.Summary.Status = "UNASSOCIATED"
	stored.Summary.FunctionMetadata.Stage = "LIVE"
	stored.Summary.FunctionMetadata.FunctionARN = cfFunctionARN(name, "LIVE")
	stored.Summary.FunctionMetadata.LastModifiedTime = cfNowISO()
	cfFunctions.Put(name, stored)
	cfWriteXML(w, http.StatusOK, stored.Summary)
}

func handleCFListFunctions(w http.ResponseWriter, r *http.Request) {
	items := []CFFunctionSummary{}
	for _, stored := range cfFunctions.List() {
		items = append(items, stored.Summary)
	}
	list := CFFunctionList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}

// ----- Invalidation handlers -----

func handleCFCreateInvalidation(w http.ResponseWriter, r *http.Request) {
	distID := r.PathValue("distId")
	if _, ok := cfDistributions.Get(distID); !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	var batch CFInvalidationBatch
	if err := xml.NewDecoder(r.Body).Decode(&batch); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode InvalidationBatch: "+err.Error())
		return
	}
	if batch.CallerReference == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "CallerReference is required")
		return
	}
	batch.Xmlns = ""
	id := cfRandomID("I")
	inv := CFInvalidation{
		Xmlns:             cfNamespace,
		Id:                id,
		Status:            "Completed", // sim: eager completion, real AWS: InProgress→Completed
		CreateTime:        cfNowISO(),
		InvalidationBatch: batch,
	}
	cfInvalidations.Put(id, cfStoredInvalidation{Invalidation: inv, DistributionID: distID})
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/distribution/"+distID+"/invalidation/"+id)
	cfWriteXML(w, http.StatusCreated, inv)
}

func handleCFGetInvalidation(w http.ResponseWriter, r *http.Request) {
	distID := r.PathValue("distId")
	id := r.PathValue("id")
	stored, ok := cfInvalidations.Get(id)
	if !ok || stored.DistributionID != distID {
		cfWriteError(w, http.StatusNotFound, "NoSuchInvalidation", "The specified invalidation does not exist.")
		return
	}
	cfWriteXML(w, http.StatusOK, stored.Invalidation)
}

func handleCFListInvalidations(w http.ResponseWriter, r *http.Request) {
	distID := r.PathValue("distId")
	if _, ok := cfDistributions.Get(distID); !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	items := []CFInvalidationSummary{}
	for _, stored := range cfInvalidations.List() {
		if stored.DistributionID != distID {
			continue
		}
		items = append(items, CFInvalidationSummary{
			Id:         stored.Invalidation.Id,
			CreateTime: stored.Invalidation.CreateTime,
			Status:     stored.Invalidation.Status,
		})
	}
	list := CFInvalidationList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}
