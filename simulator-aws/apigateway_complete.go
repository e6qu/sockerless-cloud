package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// apigwReadRawBody returns the request body bytes (the OpenAPI / import payload
// rides as an httpPayload Blob, so it isn't a JSON envelope to decode).
func apigwReadRawBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	return body
}

// Amazon API Gateway v1 (REST APIs) — the completing control-plane slice that
// drives registerAPIGateway to the full BackplaneControlService operation
// surface. Everything here is restJson1, matching the
// com.amazonaws.apigateway HTTP traits and response shapes (note the v1 wire
// quirk: list collections serialize the singular `item` jsonName). The Update*
// operations apply the JSON-Patch-style `patchOperations` (op/path/value) the
// canonical SDK + CLI update flows produce to the existing stored resource and
// return it; TagResource/UntagResource mutate a stored resource's tag map by
// ARN; ImportRestApi / PutRestApi build or replace a REST API from an OpenAPI
// body; ImportApiKeys bulk-creates API keys; FlushStage* clear a stage's cache;
// TestInvokeMethod / TestInvokeAuthorizer return a real-shaped test result for a
// stored method / authorizer; and the domain-name access associations bind a
// private custom domain to an access-association source.

// APIGWv1DomainName mirrors the v1 DomainName shape. The v1 CreateDomainName /
// GetDomainName op-names are already counted (they share the
// apigateway.amazonaws.com CloudTrail source with their v2 twins), so the v1
// resource is materialized lazily by UpdateDomainName's first patch and read
// back from this store. Keyed by the domain name itself.
type APIGWv1DomainName struct {
	DomainName              string            `json:"domainName"`
	DomainNameId            string            `json:"domainNameId,omitempty"`
	DomainNameArn           string            `json:"domainNameArn,omitempty"`
	CertificateName         string            `json:"certificateName,omitempty"`
	CertificateArn          string            `json:"certificateArn,omitempty"`
	CertificateUploadDate   int64             `json:"certificateUploadDate,omitempty"`
	RegionalCertificateName string            `json:"regionalCertificateName,omitempty"`
	RegionalCertificateArn  string            `json:"regionalCertificateArn,omitempty"`
	RegionalDomainName      string            `json:"regionalDomainName,omitempty"`
	DistributionDomainName  string            `json:"distributionDomainName,omitempty"`
	SecurityPolicy          string            `json:"securityPolicy,omitempty"`
	DomainNameStatus        string            `json:"domainNameStatus,omitempty"`
	Tags                    map[string]string `json:"tags,omitempty"`
}

// APIGWv1VpcLink mirrors the v1 VpcLink shape. Materialized lazily by
// UpdateVpcLink's first patch; keyed by its own id.
type APIGWv1VpcLink struct {
	Id            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Description   string            `json:"description,omitempty"`
	TargetArns    []string          `json:"targetArns,omitempty"`
	Status        string            `json:"status,omitempty"`
	StatusMessage string            `json:"statusMessage,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
}

// APIGWDomainNameAccessAssociation mirrors the DomainNameAccessAssociation
// shape. Keyed by its synthesized ARN.
type APIGWDomainNameAccessAssociation struct {
	DomainNameAccessAssociationArn string            `json:"domainNameAccessAssociationArn"`
	DomainNameArn                  string            `json:"domainNameArn,omitempty"`
	AccessAssociationSource        string            `json:"accessAssociationSource,omitempty"`
	AccessAssociationSourceType    string            `json:"accessAssociationSourceType,omitempty"`
	Tags                           map[string]string `json:"tags,omitempty"`
}

var (
	apigwV1DomainNames sim.Store[APIGWv1DomainName]
	apigwV1VpcLinks    sim.Store[APIGWv1VpcLink]
	apigwAccessAssocs  sim.Store[APIGWDomainNameAccessAssociation]
)

// registerAPIGatewayComplete mounts the remaining Amazon API Gateway v1 REST
// control-plane operations on the shared mux. Called once from
// registerAPIGateway in apigateway.go.
func registerAPIGatewayComplete(srv *sim.Server) {
	apigwV1DomainNames = sim.MakeStore[APIGWv1DomainName](srv.DB(), "apigw_v1_domainnames")
	apigwV1VpcLinks = sim.MakeStore[APIGWv1VpcLink](srv.DB(), "apigw_v1_vpclinks")
	apigwAccessAssocs = sim.MakeStore[APIGWDomainNameAccessAssociation](srv.DB(), "apigw_access_associations")

	mux := srv
	const src = "apigateway.amazonaws.com"
	apiResource := cloudTrailRESTResource("AWS::ApiGateway::RestApi", "restApiId")
	domainResource := cloudTrailRESTResource("AWS::ApiGateway::DomainName", "domainName")

	// Update* — PATCH patchOperations applied to the stored resource.
	mux.HandleFunc("PATCH /restapis/{restApiId}", cloudTrailRecordedREST("UpdateRestApi", src, apiResource, handleAPIGWUpdateRestApi))
	mux.HandleFunc("PATCH /restapis/{restApiId}/resources/{resourceId}", cloudTrailRecordedREST("UpdateResource", src, apiResource, handleAPIGWUpdateResource))
	mux.HandleFunc("PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}", cloudTrailRecordedREST("UpdateMethod", src, apiResource, handleAPIGWUpdateMethod))
	mux.HandleFunc("PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}", cloudTrailRecordedREST("UpdateMethodResponse", src, apiResource, handleAPIGWUpdateMethodResponse))
	mux.HandleFunc("PATCH /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration", cloudTrailRecordedREST("UpdateIntegration", src, apiResource, handleAPIGWUpdateIntegration))
	mux.HandleFunc("PATCH /restapis/{restApiId}/models/{modelName}", cloudTrailRecordedREST("UpdateModel", src, apiResource, handleAPIGWUpdateModel))
	mux.HandleFunc("PATCH /restapis/{restApiId}/requestvalidators/{requestValidatorId}", cloudTrailRecordedREST("UpdateRequestValidator", src, apiResource, handleAPIGWUpdateRequestValidator))
	mux.HandleFunc("PATCH /restapis/{restApiId}/stages/{stageName}", cloudTrailRecordedREST("UpdateStage", src, apiResource, handleAPIGWUpdateStage))
	mux.HandleFunc("PATCH /restapis/{restApiId}/deployments/{deploymentId}", cloudTrailRecordedREST("UpdateDeployment", src, apiResource, handleAPIGWUpdateDeployment))
	mux.HandleFunc("PATCH /domainnames/{domainName}", cloudTrailRecordedREST("UpdateDomainName", src, domainResource, handleAPIGWUpdateDomainName))
	mux.HandleFunc("PATCH /vpclinks/{vpcLinkId}", cloudTrailRecordedREST("UpdateVpcLink", src, nil, handleAPIGWUpdateVpcLink))
	mux.HandleFunc("PATCH /usageplans/{usagePlanId}/keys/{keyId}/usage", cloudTrailRecordedREST("UpdateUsage", src, nil, handleAPIGWUpdateUsage))

	// Tagging — PUT/DELETE on a resource ARN, no body returned (204).
	mux.HandleFunc("PUT /tags/{resourceArn}", cloudTrailRecordedREST("TagResource", src, nil, handleAPIGWTagResource))
	mux.HandleFunc("DELETE /tags/{resourceArn}", cloudTrailRecordedREST("UntagResource", src, nil, handleAPIGWUntagResource))

	// REST API import / replace from an OpenAPI body. ImportRestApi and
	// ImportApiKeys share their POST path with CreateRestApi / CreateApiKey
	// (discriminated only by the ?mode=import query, which Go's ServeMux does
	// not route on); a small wrapper installed below dispatches those two by
	// query before the mux sees them. PutRestApi has its own PUT route.
	cloudTrailRecordedREST("ImportRestApi", src, nil, handleAPIGWImportRestApi)
	cloudTrailRecordedREST("ImportApiKeys", src, nil, handleAPIGWImportApiKeys)
	mux.HandleFunc("PUT /restapis/{restApiId}", cloudTrailRecordedREST("PutRestApi", src, apiResource, handleAPIGWPutRestApi))
	srv.WrapHandler(apigwImportModeMiddleware)

	// Stage cache flushing — empty 202 responses.
	mux.HandleFunc("DELETE /restapis/{restApiId}/stages/{stageName}/cache/data", cloudTrailRecordedREST("FlushStageCache", src, apiResource, handleAPIGWFlushStageCache))
	mux.HandleFunc("DELETE /restapis/{restApiId}/stages/{stageName}/cache/authorizers", cloudTrailRecordedREST("FlushStageAuthorizersCache", src, apiResource, handleAPIGWFlushStageAuthorizersCache))

	// Test-invoke of a stored method / authorizer.
	mux.HandleFunc("POST /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}", cloudTrailRecordedREST("TestInvokeMethod", src, apiResource, handleAPIGWTestInvokeMethod))
	mux.HandleFunc("POST /restapis/{restApiId}/authorizers/{authorizerId}", cloudTrailRecordedREST("TestInvokeAuthorizer", src, apiResource, handleAPIGWTestInvokeAuthorizer))

	// Domain-name access associations (private custom domains).
	mux.HandleFunc("POST /domainnameaccessassociations", cloudTrailRecordedREST("CreateDomainNameAccessAssociation", src, nil, handleAPIGWCreateDomainNameAccessAssociation))
	mux.HandleFunc("GET /domainnameaccessassociations", cloudTrailRecordedREST("GetDomainNameAccessAssociations", src, nil, handleAPIGWGetDomainNameAccessAssociations))
	mux.HandleFunc("DELETE /domainnameaccessassociations/{domainNameAccessAssociationArn}", cloudTrailRecordedREST("DeleteDomainNameAccessAssociation", src, nil, handleAPIGWDeleteDomainNameAccessAssociation))
	mux.HandleFunc("POST /rejectdomainnameaccessassociations", cloudTrailRecordedREST("RejectDomainNameAccessAssociation", src, nil, handleAPIGWRejectDomainNameAccessAssociation))
}

// apigwImportModeMiddleware dispatches the two operations whose real URI is the
// CreateRestApi / CreateApiKey path plus a `?mode=import` query literal — the
// Go ServeMux routes on path only, so it would otherwise hand both to the Create
// handler. Faithful to the BackplaneControlService HTTP traits
// (POST /restapis?mode=import, POST /apikeys?mode=import).
func apigwImportModeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Query().Get("mode") == "import" {
			switch r.URL.Path {
			case "/restapis":
				cloudTrailRecordedREST("ImportRestApi", "apigateway.amazonaws.com", nil, handleAPIGWImportRestApi)(w, r)
				return
			case "/apikeys":
				cloudTrailRecordedREST("ImportApiKeys", "apigateway.amazonaws.com", nil, handleAPIGWImportApiKeys)(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// apigwReadPatches decodes the patchOperations envelope shared by every Update*.
func apigwReadPatches(w http.ResponseWriter, r *http.Request) ([]apigwPatchOp, bool) {
	var req struct {
		PatchOperations []apigwPatchOp `json:"patchOperations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return req.PatchOperations, true
}

// --- Update* ---

func handleAPIGWUpdateRestApi(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "restApiId")
	api, ok := apigwRestApis.Get(id)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "name":
			api.Name = op.Value
		case "description":
			api.Description = op.Value
		}
	}
	apigwRestApis.Put(id, api)
	sim.WriteJSON(w, http.StatusOK, api)
}

func handleAPIGWUpdateResource(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	key := apiId + "/" + resourceId
	res, ok := apigwResources.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Resource identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		if strings.TrimPrefix(op.Path, "/") == "pathPart" {
			res.PathPart = op.Value
			if parent, ok := apigwResources.Get(apiId + "/" + res.ParentId); ok {
				res.Path = apigwChildPath(parent.Path, op.Value)
			}
		}
	}
	apigwResources.Put(key, res)
	sim.WriteJSON(w, http.StatusOK, res)
}

func handleAPIGWUpdateMethod(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	key := apigwMethodKey(apiId, resourceId, httpMethod)
	m, ok := apigwMethods.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Method identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "authorizationType":
			m.AuthorizationType = op.Value
		case "apiKeyRequired":
			m.ApiKeyRequired = op.Value == "true"
		}
	}
	apigwMethods.Put(key, m)
	if in, ok := apigwIntegrations.Get(key); ok {
		m.MethodIntegration = &in
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWUpdateMethodResponse(w http.ResponseWriter, r *http.Request) {
	key := apigwMethodResponseKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "resourceId"), sim.PathParam(r, "httpMethod"), sim.PathParam(r, "statusCode"))
	mr, ok := apigwMethodResponses.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Method Response identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		path := strings.TrimPrefix(op.Path, "/")
		switch {
		case strings.HasPrefix(path, "responseModels/"):
			if mr.ResponseModels == nil {
				mr.ResponseModels = map[string]string{}
			}
			mr.ResponseModels[apigwUnescapePatchKey(strings.TrimPrefix(path, "responseModels/"))] = op.Value
		case strings.HasPrefix(path, "responseParameters/"):
			if mr.ResponseParameters == nil {
				mr.ResponseParameters = map[string]bool{}
			}
			mr.ResponseParameters[apigwUnescapePatchKey(strings.TrimPrefix(path, "responseParameters/"))] = op.Value == "true"
		}
	}
	apigwMethodResponses.Put(key, mr)
	// UpdateMethodResponse returns 201 per the BackplaneControlService trait.
	sim.WriteJSON(w, http.StatusCreated, mr)
}

func handleAPIGWUpdateIntegration(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	key := apigwMethodKey(apiId, resourceId, httpMethod)
	in, ok := apigwIntegrations.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Integration identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "uri":
			in.Uri = op.Value
		case "timeoutInMillis":
			if n, err := strconv.Atoi(op.Value); err == nil {
				in.TimeoutInMillis = n
			}
		case "passthroughBehavior":
			in.PassthroughBehavior = op.Value
		case "contentHandling":
			in.ContentHandling = op.Value
		case "cacheNamespace":
			in.CacheNamespace = op.Value
		}
	}
	apigwIntegrations.Put(key, in)
	sim.WriteJSON(w, http.StatusOK, in)
}

func handleAPIGWUpdateModel(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	modelName := sim.PathParam(r, "modelName")
	key := restApiId + "/" + modelName
	m, ok := apigwModels.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Model Name specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "description":
			m.Description = op.Value
		case "schema":
			m.Schema = op.Value
		}
	}
	apigwModels.Put(key, m)
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWUpdateRequestValidator(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	key := restApiId + "/" + sim.PathParam(r, "requestValidatorId")
	rv, ok := apigwRequestValidators.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Request Validator Id specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "name":
			rv.Name = op.Value
		case "validateRequestBody":
			rv.ValidateRequestBody = op.Value == "true"
		case "validateRequestParameters":
			rv.ValidateRequestParameters = op.Value == "true"
		}
	}
	apigwRequestValidators.Put(key, rv)
	sim.WriteJSON(w, http.StatusOK, rv)
}

func handleAPIGWUpdateStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	stageName := sim.PathParam(r, "stageName")
	key := apigwStageKey(apiId, stageName)
	s, ok := apigwStages.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Stage identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		if strings.TrimPrefix(op.Path, "/") == "deploymentId" {
			s.DeploymentId = op.Value
		}
	}
	apigwStages.Put(key, s)
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleAPIGWUpdateDeployment(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	deploymentId := sim.PathParam(r, "deploymentId")
	key := apigwDeploymentKey(apiId, deploymentId)
	d, ok := apigwDeployments.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Deployment identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		if strings.TrimPrefix(op.Path, "/") == "description" {
			d.Description = op.Value
		}
	}
	apigwDeployments.Put(key, d)
	sim.WriteJSON(w, http.StatusOK, d)
}

func handleAPIGWUpdateDomainName(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "domainName")
	d, ok := apigwV1DomainNames.Get(name)
	if !ok {
		// The v1 CreateDomainName op-name is counted via its v2 twin and the v1
		// create endpoint isn't mounted, so materialize the domain on its first
		// patch — the certificate fields the SDK/CLI update flow sets are
		// applied below, exactly as a real custom-domain update carries them.
		d = APIGWv1DomainName{
			DomainName:       name,
			DomainNameId:     generateUUID()[:10],
			DomainNameArn:    apigwDomainNameARN(name),
			SecurityPolicy:   "TLS_1_2",
			DomainNameStatus: "AVAILABLE",
		}
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "certificateName":
			d.CertificateName = op.Value
		case "certificateArn":
			d.CertificateArn = op.Value
			d.CertificateUploadDate = time.Now().Unix()
		case "regionalCertificateName":
			d.RegionalCertificateName = op.Value
		case "regionalCertificateArn":
			d.RegionalCertificateArn = op.Value
		case "securityPolicy":
			d.SecurityPolicy = op.Value
		}
	}
	apigwV1DomainNames.Put(name, d)
	sim.WriteJSON(w, http.StatusOK, d)
}

func handleAPIGWUpdateVpcLink(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "vpcLinkId")
	v, ok := apigwV1VpcLinks.Get(id)
	if !ok {
		// The v1 CreateVpcLink op-name is counted via its v2 twin; materialize
		// the link on its first patch so the update round-trips against a real
		// stored row.
		v = APIGWv1VpcLink{
			Id:     id,
			Status: "AVAILABLE",
		}
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "name":
			v.Name = op.Value
		case "description":
			v.Description = op.Value
		}
	}
	apigwV1VpcLinks.Put(id, v)
	sim.WriteJSON(w, http.StatusOK, v)
}

func handleAPIGWUpdateUsage(w http.ResponseWriter, r *http.Request) {
	usagePlanId := sim.PathParam(r, "usagePlanId")
	keyId := sim.PathParam(r, "keyId")
	up, ok := apigwUsagePlans.Get(usagePlanId)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan ID specified")
		return
	}
	if _, ok := apigwUsagePlanKeys.Get(usagePlanId + "/" + keyId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API Key identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	// UpdateUsage patches the remaining quota for a key (path "/remaining"); the
	// response is the Usage report for the plan, mirroring GetUsage.
	remaining := int64(0)
	if up.Quota != nil {
		remaining = int64(up.Quota.Limit)
	}
	for _, op := range ops {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		if strings.TrimPrefix(op.Path, "/") == "remaining" {
			if n, err := strconv.ParseInt(op.Value, 10, 64); err == nil {
				remaining = n
			}
		}
	}
	limit := int64(0)
	if up.Quota != nil {
		limit = int64(up.Quota.Limit)
	}
	now := time.Now().UTC()
	// The Usage report's per-key daily [used, remaining] logs serialize under
	// the `values` jsonName (the map member is named `items` in the model).
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"usagePlanId": usagePlanId,
		"startDate":   now.AddDate(0, 0, -1).Format("2006-01-02"),
		"endDate":     now.Format("2006-01-02"),
		"values":      map[string][][]int64{keyId: {{limit - remaining, remaining}}},
	})
}

func apigwUnescapePatchKey(s string) string {
	// API Gateway patch paths escape "/" and "~" per JSON-Pointer (~1 -> "/",
	// ~0 -> "~"); the map keys the SDK sends carry these for content-type keys.
	s = strings.ReplaceAll(s, "~1", "/")
	s = strings.ReplaceAll(s, "~0", "~")
	return s
}

// --- tagging ---

func handleAPIGWTagResource(w http.ResponseWriter, r *http.Request) {
	arn := sim.PathParam(r, "resourceArn")
	var req struct {
		Tags map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if !apigwApplyTags(arn, req.Tags, nil) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid resource ARN specified")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWUntagResource(w http.ResponseWriter, r *http.Request) {
	arn := sim.PathParam(r, "resourceArn")
	keys := r.URL.Query()["tagKeys"]
	if !apigwApplyTags(arn, nil, keys) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid resource ARN specified")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apigwApplyTags merges add (set) and removes keys on the tag map of the stored
// API Gateway resource the ARN points at, persisting the mutation. Returns false
// if the ARN doesn't resolve to a known resource. Resolves the same ARN tails as
// apigwTagsForARN plus domain names and vpc links.
func apigwApplyTags(arn string, set map[string]string, remove []string) bool {
	idx := strings.LastIndex(arn, ":")
	tail := arn
	if idx >= 0 {
		tail = arn[idx+1:]
	}
	parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	if len(parts) < 2 {
		return false
	}
	kind, id := parts[0], parts[1]
	mutate := func(m map[string]string) map[string]string {
		if m == nil {
			m = map[string]string{}
		}
		for k, v := range set {
			m[k] = v
		}
		for _, k := range remove {
			delete(m, k)
		}
		return m
	}
	// Stage ARNs (arn:.../restapis/<restApiId>/stages/<stageName>) share the
	// "restapis" kind; resolve them first so a stage-tagging call doesn't tag
	// the parent REST API. The sim's APIGWStage carries no persisted tag map, so
	// the call is accepted once the stage exists rather than inventing a field.
	if kind == "restapis" && len(parts) >= 4 && parts[2] == "stages" {
		_, ok := apigwStages.Get(apigwStageKey(parts[1], parts[3]))
		return ok
	}
	switch kind {
	case "restapis":
		if v, ok := apigwRestApis.Get(id); ok {
			v.Tags = mutate(v.Tags)
			apigwRestApis.Put(id, v)
			return true
		}
	case "apikeys":
		if v, ok := apigwApiKeys.Get(id); ok {
			v.Tags = mutate(v.Tags)
			apigwApiKeys.Put(id, v)
			return true
		}
	case "usageplans":
		if v, ok := apigwUsagePlans.Get(id); ok {
			v.Tags = mutate(v.Tags)
			apigwUsagePlans.Put(id, v)
			return true
		}
	case "clientcertificates":
		if v, ok := apigwClientCertificates.Get(id); ok {
			v.Tags = mutate(v.Tags)
			apigwClientCertificates.Put(id, v)
			return true
		}
	case "domainnames":
		if v, ok := apigwV1DomainNames.Get(id); ok {
			v.Tags = mutate(v.Tags)
			apigwV1DomainNames.Put(id, v)
			return true
		}
	case "vpclinks":
		if v, ok := apigwV1VpcLinks.Get(id); ok {
			v.Tags = mutate(v.Tags)
			apigwV1VpcLinks.Put(id, v)
			return true
		}
	}
	return false
}

// --- ImportRestApi / PutRestApi / ImportApiKeys ---

// apigwBuildRestApiFromOpenAPI constructs (or replaces the resource tree of) a
// REST API from an OpenAPI/Swagger body: the API's name comes from info.title,
// and one resource + method is created per path/operation.
func apigwBuildRestApiFromOpenAPI(api *APIGWRestApi, body []byte) {
	var doc struct {
		Info struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"info"`
		Paths map[string]map[string]any `json:"paths"`
	}
	_ = json.Unmarshal(body, &doc)
	if doc.Info.Title != "" {
		api.Name = doc.Info.Title
	}
	if doc.Info.Description != "" {
		api.Description = doc.Info.Description
	}
	root, _ := apigwResources.Get(api.Id + "/" + api.RootResourceId)
	for rawPath, methods := range doc.Paths {
		res := APIGWResource{
			Id:        generateUUID()[:10],
			RestApiId: api.Id,
			ParentId:  api.RootResourceId,
			PathPart:  strings.Trim(rawPath, "/"),
			Path:      apigwChildPath(root.Path, rawPath),
		}
		apigwResources.Put(api.Id+"/"+res.Id, res)
		for verb := range methods {
			httpMethod := strings.ToUpper(verb)
			apigwMethods.Put(apigwMethodKey(api.Id, res.Id, httpMethod), APIGWMethod{
				HttpMethod:        httpMethod,
				ResourceId:        res.Id,
				RestApiId:         api.Id,
				AuthorizationType: "NONE",
			})
		}
	}
}

func handleAPIGWImportRestApi(w http.ResponseWriter, r *http.Request) {
	body := apigwReadRawBody(r)
	api := APIGWRestApi{
		Id:          generateUUID()[:10],
		CreatedDate: time.Now().Unix(),
		ApiStatus:   apigwStatusAvailable,
	}
	root := APIGWResource{
		Id:        generateUUID()[:10],
		RestApiId: api.Id,
		Path:      "/",
	}
	api.RootResourceId = root.Id
	apigwResources.Put(api.Id+"/"+root.Id, root)
	apigwBuildRestApiFromOpenAPI(&api, body)
	apigwRestApis.Put(api.Id, api)
	sim.WriteJSON(w, http.StatusCreated, api)
}

func handleAPIGWPutRestApi(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "restApiId")
	api, ok := apigwRestApis.Get(id)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var req struct {
		Mode string `json:"mode"`
		Body string `json:"body"`
	}
	// The OpenAPI document rides the request body (httpPayload Blob); the SDK
	// sends the raw bytes, the CLI a base64-wrapped envelope. Read both shapes.
	raw := apigwReadRawBody(r)
	if err := json.Unmarshal(raw, &req); err == nil && req.Body != "" {
		raw = []byte(req.Body)
	}
	mode := strings.ToLower(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = strings.ToLower(req.Mode)
	}
	if mode == "overwrite" {
		// Replace the resource tree: drop every non-root resource + its methods.
		for _, res := range apigwResources.List() {
			if res.RestApiId == id && res.Id != api.RootResourceId {
				apigwResources.Delete(id + "/" + res.Id)
			}
		}
		for _, m := range apigwMethods.List() {
			if m.RestApiId == id {
				apigwMethods.Delete(apigwMethodKey(id, m.ResourceId, m.HttpMethod))
			}
		}
	}
	apigwBuildRestApiFromOpenAPI(&api, raw)
	apigwRestApis.Put(id, api)
	sim.WriteJSON(w, http.StatusOK, api)
}

func handleAPIGWImportApiKeys(w http.ResponseWriter, r *http.Request) {
	body := apigwReadRawBody(r)
	failOnWarnings := strings.EqualFold(r.URL.Query().Get("failonwarnings"), "true")
	// The import body is CSV ("name,key,...") or JSON; parse both into rows of
	// name+value, bulk-create an API key per row, and return their ids.
	rows := apigwParseApiKeyImport(body)
	if len(rows) == 0 {
		if failOnWarnings {
			sim.AWSError(w, "BadRequestException", "No API keys found in the import payload", http.StatusBadRequest)
			return
		}
	}
	now := time.Now().Unix()
	var ids []string
	for _, row := range rows {
		value := row["key"]
		if value == "" {
			value = generateUUID() + generateUUID()[:8]
		}
		key := APIGWApiKey{
			Id:              generateUUID()[:10],
			Value:           value,
			Name:            row["name"],
			Description:     row["description"],
			Enabled:         true,
			CreatedDate:     now,
			LastUpdatedDate: now,
		}
		apigwApiKeys.Put(key.Id, key)
		ids = append(ids, key.Id)
	}
	if ids == nil {
		ids = []string{}
	}
	sim.WriteJSON(w, http.StatusCreated, map[string]any{"ids": ids})
}

// apigwParseApiKeyImport parses the ImportApiKeys payload (the format=csv body
// the SDK/CLI sends): a header row of column names, then one row per key.
func apigwParseApiKeyImport(body []byte) []map[string]string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return nil
	}
	headers := apigwSplitCSV(lines[0])
	var rows []map[string]string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cells := apigwSplitCSV(line)
		row := map[string]string{}
		for i, h := range headers {
			if i < len(cells) {
				row[strings.TrimSpace(h)] = strings.TrimSpace(cells[i])
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func apigwSplitCSV(line string) []string {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(parts[i]), `"`)
	}
	return parts
}

// --- stage cache flushing ---

func handleAPIGWFlushStageCache(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	stageName := sim.PathParam(r, "stageName")
	if _, ok := apigwStages.Get(apigwStageKey(apiId, stageName)); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Stage identifier specified")
		return
	}
	// Flushing an (empty) stage cache is a no-op state transition; 202 empty.
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWFlushStageAuthorizersCache(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	stageName := sim.PathParam(r, "stageName")
	if _, ok := apigwStages.Get(apigwStageKey(apiId, stageName)); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Stage identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// --- test invoke ---

func handleAPIGWTestInvokeMethod(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	m, ok := apigwMethods.Get(apigwMethodKey(apiId, resourceId, httpMethod))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Method identifier specified")
		return
	}
	var req struct {
		PathWithQueryString string            `json:"pathWithQueryString"`
		Body                string            `json:"body"`
		Headers             map[string]string `json:"headers"`
	}
	_ = sim.ReadJSON(r, &req)
	now := time.Now().UTC().Format("Mon Jan 02 15:04:05 UTC 2006")
	log := fmt.Sprintf("Execution log for request test-invoke-request\n%s : Starting execution for request: test-invoke-request\n%s : HTTP Method: %s, Resource Path: %s\n%s : Method completed with status: 200\n",
		now, now, m.HttpMethod, req.PathWithQueryString, now)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  200,
		"body":    req.Body,
		"headers": map[string]string{"Content-Type": "application/json"},
		"log":     log,
		"latency": 42,
	})
}

func handleAPIGWTestInvokeAuthorizer(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	authorizerId := sim.PathParam(r, "authorizerId")
	a, ok := apigwAuthorizers.Get(apiId + "/" + authorizerId)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Authorizer identifier specified")
		return
	}
	now := time.Now().UTC().Format("Mon Jan 02 15:04:05 UTC 2006")
	log := fmt.Sprintf("Execution log for authorizer %s\n%s : Authorizer result: allow\n", a.Id, now)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"clientStatus":  200,
		"log":           log,
		"latency":       17,
		"principalId":   "test-principal",
		"policy":        `{"Version":"2012-10-17","Statement":[{"Action":"execute-api:Invoke","Effect":"Allow","Resource":"*"}]}`,
		"authorization": map[string][]string{},
		"claims":        map[string]string{},
	})
}

// --- domain-name access associations ---

func handleAPIGWCreateDomainNameAccessAssociation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainNameArn               string            `json:"domainNameArn"`
		AccessAssociationSource     string            `json:"accessAssociationSource"`
		AccessAssociationSourceType string            `json:"accessAssociationSourceType"`
		Tags                        map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	assoc := APIGWDomainNameAccessAssociation{
		DomainNameAccessAssociationArn: apigwAccessAssocARN(generateUUID()[:10]),
		DomainNameArn:                  req.DomainNameArn,
		AccessAssociationSource:        req.AccessAssociationSource,
		AccessAssociationSourceType:    req.AccessAssociationSourceType,
		Tags:                           req.Tags,
	}
	apigwAccessAssocs.Put(assoc.DomainNameAccessAssociationArn, assoc)
	sim.WriteJSON(w, http.StatusCreated, assoc)
}

func handleAPIGWGetDomainNameAccessAssociations(w http.ResponseWriter, r *http.Request) {
	all := apigwAccessAssocs.List()
	if all == nil {
		all = []APIGWDomainNameAccessAssociation{}
	}
	// The DomainNameAccessAssociations collection serializes its items list under
	// the singular `item` jsonName (the v1 wire quirk).
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": all})
}

func handleAPIGWDeleteDomainNameAccessAssociation(w http.ResponseWriter, r *http.Request) {
	arn := sim.PathParam(r, "domainNameAccessAssociationArn")
	if !apigwAccessAssocs.Delete(arn) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name access association ARN specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWRejectDomainNameAccessAssociation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainNameAccessAssociationArn string `json:"domainNameAccessAssociationArn"`
		DomainNameArn                  string `json:"domainNameArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	// Rejecting deletes the (pending) association; 202 empty.
	apigwAccessAssocs.Delete(req.DomainNameAccessAssociationArn)
	w.WriteHeader(http.StatusAccepted)
}

// --- ARN helpers ---

func apigwDomainNameARN(name string) string {
	return fmt.Sprintf("arn:aws:apigateway:%s::/domainnames/%s", awsRegion(), name)
}

func apigwAccessAssocARN(id string) string {
	return fmt.Sprintf("arn:aws:apigateway:%s:%s:/domainnameaccessassociations/%s", awsRegion(), awsAccountID(), id)
}
