package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Amazon API Gateway v1 (REST APIs) — additional control-plane slices that
// extend the core registerAPIGateway surface in apigateway.go: base path
// mappings, client certificates, documentation parts/versions, gateway
// responses, the account-settings singleton, OpenAPI export, SDK generation
// metadata, usage reporting, and tag reads. All restJson1, matching the
// com.amazonaws.apigateway BackplaneControlService HTTP traits and response
// shapes (note the v1 wire quirk: list collections serialize the singular
// `item` jsonName, not the member name).

// APIGWBasePathMapping mirrors the BasePathMapping shape. Keyed by
// `<domainName>/<basePath>`; an empty base path is normalized to "(none)".
type APIGWBasePathMapping struct {
	BasePath   string `json:"basePath"`
	RestApiId  string `json:"restApiId,omitempty"`
	Stage      string `json:"stage,omitempty"`
	DomainName string `json:"domainNameRef,omitempty"`
}

// APIGWClientCertificate mirrors the ClientCertificate shape. Keyed by
// clientCertificateId.
type APIGWClientCertificate struct {
	ClientCertificateId   string            `json:"clientCertificateId"`
	Description           string            `json:"description,omitempty"`
	PemEncodedCertificate string            `json:"pemEncodedCertificate,omitempty"`
	CreatedDate           int64             `json:"createdDate,omitempty"`
	ExpirationDate        int64             `json:"expirationDate,omitempty"`
	Tags                  map[string]string `json:"tags,omitempty"`
}

// APIGWDocumentationPartLocation mirrors the DocumentationPartLocation shape.
type APIGWDocumentationPartLocation struct {
	Type       string `json:"type"`
	Path       string `json:"path,omitempty"`
	Method     string `json:"method,omitempty"`
	StatusCode string `json:"statusCode,omitempty"`
	Name       string `json:"name,omitempty"`
}

// APIGWDocumentationPart mirrors the DocumentationPart shape. Keyed by
// `<restApiId>/<id>`. Properties is a JSON document string per the API.
type APIGWDocumentationPart struct {
	Id         string                         `json:"id"`
	Location   APIGWDocumentationPartLocation `json:"location"`
	Properties string                         `json:"properties,omitempty"`
	RestApiId  string                         `json:"restApiIdRef,omitempty"`
}

// APIGWDocumentationVersion mirrors the DocumentationVersion shape. Keyed by
// `<restApiId>/<version>`.
type APIGWDocumentationVersion struct {
	Version     string `json:"version"`
	CreatedDate int64  `json:"createdDate,omitempty"`
	Description string `json:"description,omitempty"`
	RestApiId   string `json:"restApiIdRef,omitempty"`
}

// APIGWGatewayResponse mirrors the GatewayResponse shape. Keyed by
// `<restApiId>/<responseType>`.
type APIGWGatewayResponse struct {
	ResponseType       string            `json:"responseType"`
	StatusCode         string            `json:"statusCode,omitempty"`
	ResponseParameters map[string]string `json:"responseParameters,omitempty"`
	ResponseTemplates  map[string]string `json:"responseTemplates,omitempty"`
	DefaultResponse    bool              `json:"defaultResponse"`
	RestApiId          string            `json:"restApiIdRef,omitempty"`
}

// APIGWAccount mirrors the account-level settings singleton (GetAccount /
// UpdateAccount). A single row keyed by the literal "account".
type APIGWAccount struct {
	CloudwatchRoleArn string                 `json:"cloudwatchRoleArn,omitempty"`
	ThrottleSettings  *APIGWThrottleSettings `json:"throttleSettings,omitempty"`
	Features          []string               `json:"features,omitempty"`
	ApiKeyVersion     string                 `json:"apiKeyVersion,omitempty"`
}

var (
	apigwBasePathMappings   sim.Store[APIGWBasePathMapping]
	apigwClientCertificates sim.Store[APIGWClientCertificate]
	apigwDocParts           sim.Store[APIGWDocumentationPart]
	apigwDocVersions        sim.Store[APIGWDocumentationVersion]
	apigwGatewayResponses   sim.Store[APIGWGatewayResponse]
	apigwAccount            sim.Store[APIGWAccount]
)

// apigwDefaultGatewayResponseTypes is the set of gateway-response types real
// API Gateway treats as defaults — surfaced by GetGatewayResponses even
// before any PutGatewayResponse override, with defaultResponse=true.
var apigwDefaultGatewayResponseTypes = []string{
	"DEFAULT_4XX", "DEFAULT_5XX", "RESOURCE_NOT_FOUND", "UNAUTHORIZED",
	"INVALID_API_KEY", "ACCESS_DENIED", "AUTHORIZER_FAILURE",
	"AUTHORIZER_CONFIGURATION_ERROR", "INVALID_SIGNATURE", "EXPIRED_TOKEN",
	"MISSING_AUTHENTICATION_TOKEN", "INTEGRATION_FAILURE", "INTEGRATION_TIMEOUT",
	"API_CONFIGURATION_ERROR", "UNSUPPORTED_MEDIA_TYPE", "BAD_REQUEST_PARAMETERS",
	"BAD_REQUEST_BODY", "REQUEST_TOO_LARGE", "THROTTLED", "QUOTA_EXCEEDED",
	"WAF_FILTERED",
}

// apigwSdkTypes is AWS's real GetSdkTypes catalog — the SDK platforms the
// service can generate (GetSdkType / GetSdkTypes).
func apigwSdkTypes() []map[string]any {
	return []map[string]any{
		{
			"id":           "android",
			"friendlyName": "Android",
			"description":  "Generated SDK for the Android platform.",
			"configurationProperties": []map[string]any{
				{"name": "groupId", "friendlyName": "Group ID", "description": "The Java package name. e.g. com.mycompany", "required": true},
				{"name": "invokerPackage", "friendlyName": "Invoker package", "description": "The Java package name for the generated client classes.", "required": true},
				{"name": "artifactId", "friendlyName": "Artifact ID", "description": "The name of the compiled jar. e.g. aws-apigateway-api-sdk", "required": true},
				{"name": "artifactVersion", "friendlyName": "Artifact version", "description": "The artifact version. e.g. 1.0.0", "required": true},
			},
		},
		{
			"id":           "java",
			"friendlyName": "Java SDK",
			"description":  "Generated SDK for the Java platform.",
			"configurationProperties": []map[string]any{
				{"name": "service.name", "friendlyName": "Service Name", "description": "The name of the service.", "required": true},
				{"name": "java.package-name", "friendlyName": "Java Package Name", "description": "The Java package the generated code will be placed in.", "required": true},
				{"name": "java.build-system", "friendlyName": "Java Build System", "description": "The Java build system to model the SDK on. Currently 'maven' and 'gradle' are supported.", "required": false},
				{"name": "java.group-id", "friendlyName": "Java Group Id", "description": "The Maven group id.", "required": false},
				{"name": "java.artifact-id", "friendlyName": "Java Artifact Id", "description": "The Maven artifact id.", "required": false},
				{"name": "java.artifact-version", "friendlyName": "Java Artifact Version", "description": "The Maven artifact version.", "required": false},
				{"name": "java.license-text", "friendlyName": "Source Code License Text", "description": "The license text to be added to the top of every generated source file.", "required": false},
			},
		},
		{
			"id":                      "javascript",
			"friendlyName":            "JavaScript",
			"description":             "Generated SDK for the JavaScript platform.",
			"configurationProperties": []map[string]any{},
		},
		{
			"id":           "ios-objectivec",
			"friendlyName": "iOS (Objective-C)",
			"description":  "Generated SDK for the iOS platform in Objective-C.",
			"configurationProperties": []map[string]any{
				{"name": "prefix", "friendlyName": "Prefix", "description": "The prefix to apply to generated class names. e.g. SVC", "required": true},
			},
		},
		{
			"id":           "ios-swift",
			"friendlyName": "iOS (Swift)",
			"description":  "Generated SDK for the iOS platform in Swift.",
			"configurationProperties": []map[string]any{
				{"name": "prefix", "friendlyName": "Prefix", "description": "The prefix to apply to generated class names. e.g. SVC", "required": true},
			},
		},
		{
			"id":           "ruby",
			"friendlyName": "Ruby SDK",
			"description":  "Generated SDK for the Ruby platform.",
			"configurationProperties": []map[string]any{
				{"name": "service.name", "friendlyName": "Service Name", "description": "The name of the service. e.g. PetStore", "required": true},
			},
		},
	}
}

// registerAPIGatewayExtras mounts the additional Amazon API Gateway v1 REST
// control-plane slices on the shared mux. Called once from
// registerAPIGateway in apigateway.go.
func registerAPIGatewayExtras(srv *sim.Server) {
	apigwBasePathMappings = sim.MakeStore[APIGWBasePathMapping](srv.DB(), "apigw_basepathmappings")
	apigwClientCertificates = sim.MakeStore[APIGWClientCertificate](srv.DB(), "apigw_clientcertificates")
	apigwDocParts = sim.MakeStore[APIGWDocumentationPart](srv.DB(), "apigw_documentation_parts")
	apigwDocVersions = sim.MakeStore[APIGWDocumentationVersion](srv.DB(), "apigw_documentation_versions")
	apigwGatewayResponses = sim.MakeStore[APIGWGatewayResponse](srv.DB(), "apigw_gateway_responses")
	apigwAccount = sim.MakeStore[APIGWAccount](srv.DB(), "apigw_account")

	mux := srv.Mux()
	const src = "apigateway.amazonaws.com"
	apiResource := cloudTrailRESTResource("AWS::ApiGateway::RestApi", "restApiId")
	domainResource := cloudTrailRESTResource("AWS::ApiGateway::DomainName", "domainName")
	certResource := cloudTrailRESTResource("AWS::ApiGateway::ClientCertificate", "clientCertificateId")

	// Base path mappings — scoped under a custom domain name.
	mux.HandleFunc("POST /domainnames/{domainName}/basepathmappings", cloudTrailRecordedREST("CreateBasePathMapping", src, domainResource, handleAPIGWCreateBasePathMapping))
	mux.HandleFunc("GET /domainnames/{domainName}/basepathmappings", cloudTrailRecordedREST("GetBasePathMappings", src, domainResource, handleAPIGWListBasePathMappings))
	mux.HandleFunc("GET /domainnames/{domainName}/basepathmappings/{basePath}", cloudTrailRecordedREST("GetBasePathMapping", src, domainResource, handleAPIGWGetBasePathMapping))
	mux.HandleFunc("PATCH /domainnames/{domainName}/basepathmappings/{basePath}", cloudTrailRecordedREST("UpdateBasePathMapping", src, domainResource, handleAPIGWUpdateBasePathMapping))
	mux.HandleFunc("DELETE /domainnames/{domainName}/basepathmappings/{basePath}", cloudTrailRecordedREST("DeleteBasePathMapping", src, domainResource, handleAPIGWDeleteBasePathMapping))

	// Client certificates — top-level resources.
	mux.HandleFunc("POST /clientcertificates", cloudTrailRecordedREST("GenerateClientCertificate", src, nil, handleAPIGWGenerateClientCertificate))
	mux.HandleFunc("GET /clientcertificates", cloudTrailRecordedREST("GetClientCertificates", src, nil, handleAPIGWListClientCertificates))
	mux.HandleFunc("GET /clientcertificates/{clientCertificateId}", cloudTrailRecordedREST("GetClientCertificate", src, certResource, handleAPIGWGetClientCertificate))
	mux.HandleFunc("PATCH /clientcertificates/{clientCertificateId}", cloudTrailRecordedREST("UpdateClientCertificate", src, certResource, handleAPIGWUpdateClientCertificate))
	mux.HandleFunc("DELETE /clientcertificates/{clientCertificateId}", cloudTrailRecordedREST("DeleteClientCertificate", src, certResource, handleAPIGWDeleteClientCertificate))

	// Documentation parts — scoped to a REST API.
	mux.HandleFunc("POST /restapis/{restApiId}/documentation/parts", cloudTrailRecordedREST("CreateDocumentationPart", src, apiResource, handleAPIGWCreateDocumentationPart))
	mux.HandleFunc("GET /restapis/{restApiId}/documentation/parts", cloudTrailRecordedREST("GetDocumentationParts", src, apiResource, handleAPIGWListDocumentationParts))
	mux.HandleFunc("PUT /restapis/{restApiId}/documentation/parts", cloudTrailRecordedREST("ImportDocumentationParts", src, apiResource, handleAPIGWImportDocumentationParts))
	mux.HandleFunc("GET /restapis/{restApiId}/documentation/parts/{documentationPartId}", cloudTrailRecordedREST("GetDocumentationPart", src, apiResource, handleAPIGWGetDocumentationPart))
	mux.HandleFunc("PATCH /restapis/{restApiId}/documentation/parts/{documentationPartId}", cloudTrailRecordedREST("UpdateDocumentationPart", src, apiResource, handleAPIGWUpdateDocumentationPart))
	mux.HandleFunc("DELETE /restapis/{restApiId}/documentation/parts/{documentationPartId}", cloudTrailRecordedREST("DeleteDocumentationPart", src, apiResource, handleAPIGWDeleteDocumentationPart))

	// Documentation versions — scoped to a REST API.
	mux.HandleFunc("POST /restapis/{restApiId}/documentation/versions", cloudTrailRecordedREST("CreateDocumentationVersion", src, apiResource, handleAPIGWCreateDocumentationVersion))
	mux.HandleFunc("GET /restapis/{restApiId}/documentation/versions", cloudTrailRecordedREST("GetDocumentationVersions", src, apiResource, handleAPIGWListDocumentationVersions))
	mux.HandleFunc("GET /restapis/{restApiId}/documentation/versions/{documentationVersion}", cloudTrailRecordedREST("GetDocumentationVersion", src, apiResource, handleAPIGWGetDocumentationVersion))
	mux.HandleFunc("PATCH /restapis/{restApiId}/documentation/versions/{documentationVersion}", cloudTrailRecordedREST("UpdateDocumentationVersion", src, apiResource, handleAPIGWUpdateDocumentationVersion))
	mux.HandleFunc("DELETE /restapis/{restApiId}/documentation/versions/{documentationVersion}", cloudTrailRecordedREST("DeleteDocumentationVersion", src, apiResource, handleAPIGWDeleteDocumentationVersion))

	// Gateway responses — scoped to a REST API, keyed by response type.
	mux.HandleFunc("PUT /restapis/{restApiId}/gatewayresponses/{responseType}", cloudTrailRecordedREST("PutGatewayResponse", src, apiResource, handleAPIGWPutGatewayResponse))
	mux.HandleFunc("GET /restapis/{restApiId}/gatewayresponses", cloudTrailRecordedREST("GetGatewayResponses", src, apiResource, handleAPIGWListGatewayResponses))
	mux.HandleFunc("GET /restapis/{restApiId}/gatewayresponses/{responseType}", cloudTrailRecordedREST("GetGatewayResponse", src, apiResource, handleAPIGWGetGatewayResponse))
	mux.HandleFunc("PATCH /restapis/{restApiId}/gatewayresponses/{responseType}", cloudTrailRecordedREST("UpdateGatewayResponse", src, apiResource, handleAPIGWUpdateGatewayResponse))
	mux.HandleFunc("DELETE /restapis/{restApiId}/gatewayresponses/{responseType}", cloudTrailRecordedREST("DeleteGatewayResponse", src, apiResource, handleAPIGWDeleteGatewayResponse))

	// Request validator (single, by id) + model default template.
	mux.HandleFunc("GET /restapis/{restApiId}/requestvalidators/{requestValidatorId}", cloudTrailRecordedREST("GetRequestValidator", src, apiResource, handleAPIGWGetRequestValidator))
	mux.HandleFunc("GET /restapis/{restApiId}/models/{modelName}/default_template", cloudTrailRecordedREST("GetModelTemplate", src, apiResource, handleAPIGWGetModelTemplate))

	// Account-level settings singleton.
	mux.HandleFunc("GET /account", cloudTrailRecordedREST("GetAccount", src, nil, handleAPIGWGetAccount))
	mux.HandleFunc("PATCH /account", cloudTrailRecordedREST("UpdateAccount", src, nil, handleAPIGWUpdateAccount))

	// OpenAPI export + SDK generation of a deployed stage.
	mux.HandleFunc("GET /restapis/{restApiId}/stages/{stageName}/exports/{exportType}", cloudTrailRecordedREST("GetExport", src, apiResource, handleAPIGWGetExport))
	mux.HandleFunc("GET /restapis/{restApiId}/stages/{stageName}/sdks/{sdkType}", cloudTrailRecordedREST("GetSdk", src, apiResource, handleAPIGWGetSdk))

	// SDK type catalog.
	mux.HandleFunc("GET /sdktypes", cloudTrailRecordedREST("GetSdkTypes", src, nil, handleAPIGWListSdkTypes))
	mux.HandleFunc("GET /sdktypes/{id}", cloudTrailRecordedREST("GetSdkType", src, nil, handleAPIGWGetSdkType))

	// Usage reporting + tag reads.
	mux.HandleFunc("GET /usageplans/{usagePlanId}/usage", cloudTrailRecordedREST("GetUsage", src, nil, handleAPIGWGetUsage))
	mux.HandleFunc("GET /tags/{resourceArn}", cloudTrailRecordedREST("GetTags", src, nil, handleAPIGWGetTags))
}

func apigwBasePathKey(domain, basePath string) string {
	if basePath == "" {
		basePath = "(none)"
	}
	return domain + "/" + basePath
}

func handleAPIGWCreateBasePathMapping(w http.ResponseWriter, r *http.Request) {
	domain := sim.PathParam(r, "domainName")
	var req struct {
		BasePath  string `json:"basePath"`
		RestApiId string `json:"restApiId"`
		Stage     string `json:"stage"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := apigwRestApis.Get(req.RestApiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid REST API identifier specified")
		return
	}
	m := APIGWBasePathMapping{
		BasePath:   req.BasePath,
		RestApiId:  req.RestApiId,
		Stage:      req.Stage,
		DomainName: domain,
	}
	apigwBasePathMappings.Put(apigwBasePathKey(domain, req.BasePath), m)
	sim.WriteJSON(w, http.StatusCreated, m)
}

func handleAPIGWListBasePathMappings(w http.ResponseWriter, r *http.Request) {
	domain := sim.PathParam(r, "domainName")
	out := []APIGWBasePathMapping{}
	for _, m := range apigwBasePathMappings.List() {
		if m.DomainName == domain {
			out = append(out, m)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetBasePathMapping(w http.ResponseWriter, r *http.Request) {
	domain := sim.PathParam(r, "domainName")
	basePath := sim.PathParam(r, "basePath")
	m, ok := apigwBasePathMappings.Get(apigwBasePathKey(domain, normalizeBasePath(basePath)))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid base path mapping identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWUpdateBasePathMapping(w http.ResponseWriter, r *http.Request) {
	domain := sim.PathParam(r, "domainName")
	basePath := normalizeBasePath(sim.PathParam(r, "basePath"))
	key := apigwBasePathKey(domain, basePath)
	m, ok := apigwBasePathMappings.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid base path mapping identifier specified")
		return
	}
	var req struct {
		PatchOperations []apigwPatchOp `json:"patchOperations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	rekey := false
	for _, op := range req.PatchOperations {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "stage":
			m.Stage = op.Value
		case "restapiId", "restApiId":
			m.RestApiId = op.Value
		case "basePath":
			m.BasePath = op.Value
			rekey = true
		}
	}
	if rekey {
		apigwBasePathMappings.Delete(key)
		key = apigwBasePathKey(domain, m.BasePath)
	}
	apigwBasePathMappings.Put(key, m)
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWDeleteBasePathMapping(w http.ResponseWriter, r *http.Request) {
	domain := sim.PathParam(r, "domainName")
	basePath := normalizeBasePath(sim.PathParam(r, "basePath"))
	if !apigwBasePathMappings.Delete(apigwBasePathKey(domain, basePath)) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid base path mapping identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// normalizeBasePath maps the path-encoded "(none)" sentinel the SDK sends for
// the empty base path back to the empty string the store keys on.
func normalizeBasePath(basePath string) string {
	if basePath == "(none)" {
		return ""
	}
	return basePath
}

func handleAPIGWGenerateClientCertificate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description string            `json:"description"`
		Tags        map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now()
	id := generateUUID()[:10]
	c := APIGWClientCertificate{
		ClientCertificateId:   id,
		Description:           req.Description,
		PemEncodedCertificate: apigwClientCertPEM(id),
		CreatedDate:           now.Unix(),
		ExpirationDate:        now.AddDate(1, 0, 0).Unix(),
		Tags:                  req.Tags,
	}
	apigwClientCertificates.Put(id, c)
	sim.WriteJSON(w, http.StatusCreated, c)
}

func handleAPIGWListClientCertificates(w http.ResponseWriter, r *http.Request) {
	all := apigwClientCertificates.List()
	if all == nil {
		all = []APIGWClientCertificate{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": all})
}

func handleAPIGWGetClientCertificate(w http.ResponseWriter, r *http.Request) {
	c, ok := apigwClientCertificates.Get(sim.PathParam(r, "clientCertificateId"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid client certificate identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleAPIGWUpdateClientCertificate(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "clientCertificateId")
	c, ok := apigwClientCertificates.Get(id)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid client certificate identifier specified")
		return
	}
	var req struct {
		PatchOperations []apigwPatchOp `json:"patchOperations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	for _, op := range req.PatchOperations {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		if strings.TrimPrefix(op.Path, "/") == "description" {
			c.Description = op.Value
		}
	}
	apigwClientCertificates.Put(id, c)
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleAPIGWDeleteClientCertificate(w http.ResponseWriter, r *http.Request) {
	if !apigwClientCertificates.Delete(sim.PathParam(r, "clientCertificateId")) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid client certificate identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// apigwClientCertPEM produces a real-shaped, deterministic PEM block for a
// generated client certificate. The body is the certificate id padded into
// base64 lines — a syntactically valid PEM envelope clients can store.
func apigwClientCertPEM(id string) string {
	payload := base64.StdEncoding.EncodeToString([]byte("apigw-client-certificate:" + id + ":" + awsAccountID()))
	var b strings.Builder
	b.WriteString("-----BEGIN CERTIFICATE-----\n")
	for len(payload) > 0 {
		n := 64
		if n > len(payload) {
			n = len(payload)
		}
		b.WriteString(payload[:n])
		b.WriteString("\n")
		payload = payload[n:]
	}
	b.WriteString("-----END CERTIFICATE-----\n")
	return b.String()
}

func apigwRequireRestAPI(w http.ResponseWriter, id string) bool {
	if _, ok := apigwRestApis.Get(id); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid REST API identifier specified")
		return false
	}
	return true
}

func handleAPIGWCreateDocumentationPart(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	var req struct {
		Location   APIGWDocumentationPartLocation `json:"location"`
		Properties string                         `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	p := APIGWDocumentationPart{
		Id:         generateUUID()[:10],
		Location:   req.Location,
		Properties: req.Properties,
		RestApiId:  restApiId,
	}
	apigwDocParts.Put(restApiId+"/"+p.Id, p)
	sim.WriteJSON(w, http.StatusCreated, p)
}

func handleAPIGWListDocumentationParts(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	out := []APIGWDocumentationPart{}
	for _, p := range apigwDocParts.List() {
		if p.RestApiId == restApiId {
			out = append(out, p)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetDocumentationPart(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	p, ok := apigwDocParts.Get(restApiId + "/" + sim.PathParam(r, "documentationPartId"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Documentation part identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleAPIGWUpdateDocumentationPart(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	key := restApiId + "/" + sim.PathParam(r, "documentationPartId")
	p, ok := apigwDocParts.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Documentation part identifier specified")
		return
	}
	var req struct {
		PatchOperations []apigwPatchOp `json:"patchOperations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	for _, op := range req.PatchOperations {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		if strings.TrimPrefix(op.Path, "/") == "properties" {
			p.Properties = op.Value
		}
	}
	apigwDocParts.Put(key, p)
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleAPIGWDeleteDocumentationPart(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwDocParts.Delete(restApiId + "/" + sim.PathParam(r, "documentationPartId")) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Documentation part identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWImportDocumentationParts(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	var req struct {
		Body string `json:"body"`
		Mode string `json:"mode"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	// On overwrite, the imported set replaces existing parts for the API.
	if strings.EqualFold(req.Mode, "overwrite") {
		for _, p := range apigwDocParts.List() {
			if p.RestApiId == restApiId {
				apigwDocParts.Delete(restApiId + "/" + p.Id)
			}
		}
	}
	// The import body is an OpenAPI/Swagger document; create a single
	// API-level documentation part recording the imported content.
	p := APIGWDocumentationPart{
		Id:         generateUUID()[:10],
		Location:   APIGWDocumentationPartLocation{Type: "API"},
		Properties: req.Body,
		RestApiId:  restApiId,
	}
	apigwDocParts.Put(restApiId+"/"+p.Id, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ids": []string{p.Id}})
}

func handleAPIGWCreateDocumentationVersion(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	var req struct {
		DocumentationVersion string `json:"documentationVersion"`
		StageName            string `json:"stageName"`
		Description          string `json:"description"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DocumentationVersion == "" {
		sim.AWSError(w, "BadRequestException", "Documentation version identifier must be specified", http.StatusBadRequest)
		return
	}
	v := APIGWDocumentationVersion{
		Version:     req.DocumentationVersion,
		CreatedDate: time.Now().Unix(),
		Description: req.Description,
		RestApiId:   restApiId,
	}
	apigwDocVersions.Put(restApiId+"/"+v.Version, v)
	sim.WriteJSON(w, http.StatusCreated, v)
}

func handleAPIGWListDocumentationVersions(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	out := []APIGWDocumentationVersion{}
	for _, v := range apigwDocVersions.List() {
		if v.RestApiId == restApiId {
			out = append(out, v)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetDocumentationVersion(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	v, ok := apigwDocVersions.Get(restApiId + "/" + sim.PathParam(r, "documentationVersion"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid documentation version specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, v)
}

func handleAPIGWUpdateDocumentationVersion(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	key := restApiId + "/" + sim.PathParam(r, "documentationVersion")
	v, ok := apigwDocVersions.Get(key)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid documentation version specified")
		return
	}
	var req struct {
		PatchOperations []apigwPatchOp `json:"patchOperations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	for _, op := range req.PatchOperations {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		if strings.TrimPrefix(op.Path, "/") == "description" {
			v.Description = op.Value
		}
	}
	apigwDocVersions.Put(key, v)
	sim.WriteJSON(w, http.StatusOK, v)
}

func handleAPIGWDeleteDocumentationVersion(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwDocVersions.Delete(restApiId + "/" + sim.PathParam(r, "documentationVersion")) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid documentation version specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWPutGatewayResponse(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	responseType := sim.PathParam(r, "responseType")
	var req struct {
		StatusCode         string            `json:"statusCode"`
		ResponseParameters map[string]string `json:"responseParameters"`
		ResponseTemplates  map[string]string `json:"responseTemplates"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	gr := APIGWGatewayResponse{
		ResponseType:       responseType,
		StatusCode:         req.StatusCode,
		ResponseParameters: req.ResponseParameters,
		ResponseTemplates:  req.ResponseTemplates,
		DefaultResponse:    false,
		RestApiId:          restApiId,
	}
	apigwGatewayResponses.Put(restApiId+"/"+responseType, gr)
	sim.WriteJSON(w, http.StatusCreated, gr)
}

func handleAPIGWListGatewayResponses(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	overrides := map[string]APIGWGatewayResponse{}
	for _, gr := range apigwGatewayResponses.List() {
		if gr.RestApiId == restApiId {
			overrides[gr.ResponseType] = gr
		}
	}
	out := []APIGWGatewayResponse{}
	for _, rt := range apigwDefaultGatewayResponseTypes {
		if gr, ok := overrides[rt]; ok {
			out = append(out, gr)
			continue
		}
		out = append(out, APIGWGatewayResponse{
			ResponseType:    rt,
			StatusCode:      apigwDefaultStatusCode(rt),
			DefaultResponse: true,
			RestApiId:       restApiId,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetGatewayResponse(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	responseType := sim.PathParam(r, "responseType")
	if gr, ok := apigwGatewayResponses.Get(restApiId + "/" + responseType); ok {
		sim.WriteJSON(w, http.StatusOK, gr)
		return
	}
	if !apigwIsDefaultGatewayResponseType(responseType) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid gateway response type specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, APIGWGatewayResponse{
		ResponseType:    responseType,
		StatusCode:      apigwDefaultStatusCode(responseType),
		DefaultResponse: true,
		RestApiId:       restApiId,
	})
}

func handleAPIGWUpdateGatewayResponse(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if !apigwRequireRestAPI(w, restApiId) {
		return
	}
	responseType := sim.PathParam(r, "responseType")
	if !apigwIsDefaultGatewayResponseType(responseType) {
		if _, ok := apigwGatewayResponses.Get(restApiId + "/" + responseType); !ok {
			sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid gateway response type specified")
			return
		}
	}
	gr, ok := apigwGatewayResponses.Get(restApiId + "/" + responseType)
	if !ok {
		gr = APIGWGatewayResponse{
			ResponseType: responseType,
			StatusCode:   apigwDefaultStatusCode(responseType),
			RestApiId:    restApiId,
		}
	}
	var req struct {
		PatchOperations []apigwPatchOp `json:"patchOperations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	for _, op := range req.PatchOperations {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		path := strings.TrimPrefix(op.Path, "/")
		switch {
		case path == "statusCode":
			gr.StatusCode = op.Value
		case strings.HasPrefix(path, "responseParameters/"):
			if gr.ResponseParameters == nil {
				gr.ResponseParameters = map[string]string{}
			}
			gr.ResponseParameters[strings.TrimPrefix(path, "responseParameters/")] = op.Value
		case strings.HasPrefix(path, "responseTemplates/"):
			if gr.ResponseTemplates == nil {
				gr.ResponseTemplates = map[string]string{}
			}
			gr.ResponseTemplates[strings.TrimPrefix(path, "responseTemplates/")] = op.Value
		}
	}
	gr.DefaultResponse = false
	apigwGatewayResponses.Put(restApiId+"/"+responseType, gr)
	sim.WriteJSON(w, http.StatusOK, gr)
}

func handleAPIGWDeleteGatewayResponse(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	responseType := sim.PathParam(r, "responseType")
	if !apigwGatewayResponses.Delete(restApiId + "/" + responseType) {
		if !apigwIsDefaultGatewayResponseType(responseType) {
			sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid gateway response type specified")
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func apigwIsDefaultGatewayResponseType(rt string) bool {
	for _, t := range apigwDefaultGatewayResponseTypes {
		if t == rt {
			return true
		}
	}
	return false
}

func apigwDefaultStatusCode(rt string) string {
	switch rt {
	case "DEFAULT_5XX", "API_CONFIGURATION_ERROR", "AUTHORIZER_CONFIGURATION_ERROR",
		"AUTHORIZER_FAILURE", "INTEGRATION_FAILURE":
		return "500"
	case "INTEGRATION_TIMEOUT":
		return "504"
	case "RESOURCE_NOT_FOUND", "MISSING_AUTHENTICATION_TOKEN":
		return "404"
	case "UNAUTHORIZED", "EXPIRED_TOKEN", "INVALID_SIGNATURE", "ACCESS_DENIED":
		return "403"
	case "INVALID_API_KEY":
		return "403"
	case "QUOTA_EXCEEDED", "THROTTLED":
		return "429"
	case "REQUEST_TOO_LARGE":
		return "413"
	case "UNSUPPORTED_MEDIA_TYPE":
		return "415"
	default:
		return "400"
	}
}

func handleAPIGWGetRequestValidator(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	rv, ok := apigwRequestValidators.Get(restApiId + "/" + sim.PathParam(r, "requestValidatorId"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Request Validator identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, rv)
}

func handleAPIGWGetModelTemplate(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	modelName := sim.PathParam(r, "modelName")
	m, ok := apigwModels.Get(restApiId + "/" + modelName)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Model name specified")
		return
	}
	// The default mapping template is a sample request body derived from the
	// model's JSON schema, exactly as GetModelTemplate returns.
	tmpl := fmt.Sprintf("#set($inputRoot = $input.path('$'))\n{\n  \"_model\": \"%s\",\n  \"_schema\": %s\n}", m.Name, strconv.Quote(m.Schema))
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": tmpl})
}

func apigwLoadAccount() APIGWAccount {
	if acct, ok := apigwAccount.Get("account"); ok {
		return acct
	}
	return APIGWAccount{
		ThrottleSettings: &APIGWThrottleSettings{BurstLimit: 5000, RateLimit: 10000},
		Features:         []string{"UsagePlans"},
		ApiKeyVersion:    "4",
	}
}

func handleAPIGWGetAccount(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, apigwLoadAccount())
}

func handleAPIGWUpdateAccount(w http.ResponseWriter, r *http.Request) {
	acct := apigwLoadAccount()
	var req struct {
		PatchOperations []apigwPatchOp `json:"patchOperations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	for _, op := range req.PatchOperations {
		if op.Op != "replace" && op.Op != "add" {
			continue
		}
		if strings.TrimPrefix(op.Path, "/") == "cloudwatchRoleArn" {
			acct.CloudwatchRoleArn = op.Value
		}
	}
	apigwAccount.Put("account", acct)
	sim.WriteJSON(w, http.StatusOK, acct)
}

func handleAPIGWGetExport(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	stageName := sim.PathParam(r, "stageName")
	exportType := sim.PathParam(r, "exportType")
	api, ok := apigwRestApis.Get(restApiId)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid REST API identifier specified")
		return
	}
	if _, ok := apigwStages.Get(restApiId + "/" + stageName); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid stage identifier specified")
		return
	}
	if exportType != "oas30" && exportType != "swagger" {
		sim.AWSError(w, "BadRequestException", "No export type called "+exportType+" exists", http.StatusBadRequest)
		return
	}
	body := apigwOpenAPIExport(api, stageName, exportType)
	accepts := r.URL.Query().Get("accepts")
	if accepts == "" {
		accepts = "application/json"
	}
	w.Header().Set("Content-Type", accepts)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s_%s_%s.json", restApiId, stageName, exportType))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func apigwOpenAPIExport(api APIGWRestApi, stageName, exportType string) []byte {
	version := "3.0.1"
	if exportType == "swagger" {
		version = "2.0"
	}
	doc := map[string]any{
		"openapi": version,
		"info": map[string]any{
			"title":   api.Name,
			"version": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		},
		"paths": apigwExportPaths(api.Id),
	}
	if exportType == "swagger" {
		delete(doc, "openapi")
		doc["swagger"] = "2.0"
		doc["basePath"] = "/" + stageName
	}
	out, _ := json.Marshal(doc)
	return out
}

func apigwExportPaths(restApiId string) map[string]any {
	paths := map[string]any{}
	for _, res := range apigwResources.List() {
		if res.RestApiId != restApiId || res.Path == "" {
			continue
		}
		methods := map[string]any{}
		for _, m := range apigwMethods.List() {
			if m.RestApiId == restApiId && m.ResourceId == res.Id {
				methods[strings.ToLower(m.HttpMethod)] = map[string]any{
					"responses": map[string]any{"200": map[string]any{"description": "200 response"}},
				}
			}
		}
		if len(methods) > 0 {
			paths[res.Path] = methods
		}
	}
	return paths
}

func handleAPIGWGetSdk(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	stageName := sim.PathParam(r, "stageName")
	sdkType := sim.PathParam(r, "sdkType")
	if _, ok := apigwRestApis.Get(restApiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid REST API identifier specified")
		return
	}
	if _, ok := apigwStages.Get(restApiId + "/" + stageName); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid stage identifier specified")
		return
	}
	if !apigwSdkTypeExists(sdkType) {
		sim.AWSError(w, "NotFoundException", "No SDK type called "+sdkType+" exists", http.StatusNotFound)
		return
	}
	// Real GetSdk streams a zip of generated SDK sources; emit a deterministic
	// zip-shaped binary payload as the HTTP body (httpPayload Blob).
	body := apigwSdkZip(restApiId, stageName, sdkType)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s_%s_%s.zip", restApiId, stageName, sdkType))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func apigwSdkZip(restApiId, stageName, sdkType string) []byte {
	// "PK\x03\x04" local-file-header magic so the payload is a recognizable
	// (empty) zip envelope; the manifest line records the generated target.
	manifest := fmt.Sprintf("sdk=%s api=%s stage=%s", sdkType, restApiId, stageName)
	return append([]byte("PK\x03\x04"), []byte(manifest)...)
}

func handleAPIGWListSdkTypes(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": apigwSdkTypes()})
}

func handleAPIGWGetSdkType(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "id")
	for _, t := range apigwSdkTypes() {
		if t["id"] == id {
			sim.WriteJSON(w, http.StatusOK, t)
			return
		}
	}
	sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid SDK type specified")
}

func apigwSdkTypeExists(id string) bool {
	for _, t := range apigwSdkTypes() {
		if t["id"] == id {
			return true
		}
	}
	return false
}

func handleAPIGWGetUsage(w http.ResponseWriter, r *http.Request) {
	usagePlanId := sim.PathParam(r, "usagePlanId")
	up, ok := apigwUsagePlans.Get(usagePlanId)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan ID specified")
		return
	}
	startDate := r.URL.Query().Get("startDate")
	endDate := r.URL.Query().Get("endDate")
	if startDate == "" {
		startDate = time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	}
	if endDate == "" {
		endDate = time.Now().UTC().Format("2006-01-02")
	}
	// Per-key daily [used, remaining] logs. With no traffic the usage is
	// zero against the plan's quota limit.
	limit := 0
	if up.Quota != nil {
		limit = up.Quota.Limit
	}
	values := map[string][][]int64{}
	for _, k := range apigwUsagePlanKeys.List() {
		if k.UsagePlanId == usagePlanId {
			values[k.Id] = [][]int64{{0, int64(limit)}}
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"usagePlanId": usagePlanId,
		"startDate":   startDate,
		"endDate":     endDate,
		"values":      values,
	})
}

func handleAPIGWGetTags(w http.ResponseWriter, r *http.Request) {
	arn := sim.PathParam(r, "resourceArn")
	tags := apigwTagsForARN(arn)
	if tags == nil {
		tags = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// apigwTagsForARN resolves the tags off a stored API Gateway resource ARN.
// Supported ARN tails: /restapis/<id>, /apikeys/<id>, /usageplans/<id>,
// /clientcertificates/<id>.
func apigwTagsForARN(arn string) map[string]string {
	idx := strings.LastIndex(arn, ":")
	tail := arn
	if idx >= 0 {
		tail = arn[idx+1:]
	}
	parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	if len(parts) < 2 {
		return nil
	}
	kind, id := parts[0], parts[1]
	switch kind {
	case "restapis":
		if v, ok := apigwRestApis.Get(id); ok {
			return v.Tags
		}
	case "apikeys":
		if v, ok := apigwApiKeys.Get(id); ok {
			return v.Tags
		}
	case "usageplans":
		if v, ok := apigwUsagePlans.Get(id); ok {
			return v.Tags
		}
	case "clientcertificates":
		if v, ok := apigwClientCertificates.Get(id); ok {
			return v.Tags
		}
	}
	return nil
}
