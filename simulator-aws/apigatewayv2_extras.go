package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

var errAPIGWv2EmptyBody = errors.New("the OpenAPI definition body is required")

func apigwv2Now() string { return time.Now().UTC().Format(time.RFC3339) }

// apigwv2OpenAPITitle pulls info.title out of an OpenAPI JSON document, falling
// back to a generic name when the body is YAML or omits a title.
func apigwv2OpenAPITitle(body string) string {
	var doc struct {
		Info struct {
			Title string `json:"title"`
		} `json:"info"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err == nil && doc.Info.Title != "" {
		return doc.Info.Title
	}
	return "imported-api"
}

// apigwv2ExportDocument shapes a minimal OpenAPI 3.0 document for a stored API,
// listing each of the API's routes as a path entry. This is the export payload
// ExportApi streams back as its blob body.
func apigwv2ExportDocument(api APIGWv2Api) []byte {
	paths := map[string]any{}
	for _, rt := range apigwv2Routes.List() {
		if rt.ApiId != api.ApiId {
			continue
		}
		method, path := apigwv2SplitRouteKey(rt.RouteKey)
		entry, ok := paths[path].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[path] = entry
		}
		entry[method] = map[string]any{
			"responses": map[string]any{"default": map[string]any{"description": "default response"}},
		}
	}
	doc := map[string]any{
		"openapi": "3.0.1",
		"info": map[string]any{
			"title":   api.Name,
			"version": "1.0",
		},
		"paths": paths,
	}
	out, _ := json.Marshal(doc)
	return out
}

// apigwv2SplitRouteKey splits an HTTP-API route key ("GET /pets") into a
// lower-cased method and a path. The catch-all "$default" maps to "/".
func apigwv2SplitRouteKey(routeKey string) (method, path string) {
	if routeKey == "$default" || routeKey == "" {
		return "x-amazon-apigateway-any-method", "/"
	}
	parts := strings.SplitN(routeKey, " ", 2)
	if len(parts) == 2 {
		return strings.ToLower(parts[0]), parts[1]
	}
	return "x-amazon-apigateway-any-method", parts[0]
}

// API Gateway v2 (HTTP/WebSocket) extension surface — the "classic" v2
// operations beyond the core api/route/integration/stage CRUD in
// apigatewayv2.go: integration responses, route responses, model
// templates, OpenAPI import/reimport/export, resource tags, and the
// per-stage / per-api config deletes (access-log settings, CORS,
// route request parameters, route settings). All restJson1, REST path
// routing under /v2/. Member names serialize as lowerCamelCase (the
// smithy jsonName trait); list wrappers use `items`.

// APIGWv2IntegrationResponse mirrors the IntegrationResponse shape — a
// child of an integration, keyed by `<apiId>/<integrationId>/<responseId>`.
type APIGWv2IntegrationResponse struct {
	IntegrationResponseId       string            `json:"integrationResponseId"`
	ApiId                       string            `json:"apiIdRef,omitempty"`
	IntegrationId               string            `json:"integrationIdRef,omitempty"`
	IntegrationResponseKey      string            `json:"integrationResponseKey"`
	ContentHandlingStrategy     string            `json:"contentHandlingStrategy,omitempty"`
	ResponseParameters          map[string]string `json:"responseParameters,omitempty"`
	ResponseTemplates           map[string]string `json:"responseTemplates,omitempty"`
	TemplateSelectionExpression string            `json:"templateSelectionExpression,omitempty"`
}

// APIGWv2RouteResponse mirrors the RouteResponse shape — a child of a
// route, keyed by `<apiId>/<routeId>/<responseId>`. ResponseParameters
// is a map of parameter-name -> {required:bool} (ParameterConstraints).
type APIGWv2RouteResponse struct {
	RouteResponseId          string                            `json:"routeResponseId"`
	ApiId                    string                            `json:"apiIdRef,omitempty"`
	RouteId                  string                            `json:"routeIdRef,omitempty"`
	RouteResponseKey         string                            `json:"routeResponseKey"`
	ModelSelectionExpression string                            `json:"modelSelectionExpression,omitempty"`
	ResponseModels           map[string]string                 `json:"responseModels,omitempty"`
	ResponseParameters       map[string]APIGWv2ParamConstraint `json:"responseParameters,omitempty"`
}

// APIGWv2ParamConstraint mirrors the ParameterConstraints shape.
type APIGWv2ParamConstraint struct {
	Required bool `json:"required"`
}

var (
	apigwv2IntegrationResponses sim.Store[APIGWv2IntegrationResponse]
	apigwv2RouteResponses       sim.Store[APIGWv2RouteResponse]
)

func registerAPIGatewayV2Extras(srv *sim.Server) {
	apigwv2IntegrationResponses = sim.MakeStore[APIGWv2IntegrationResponse](srv.DB(), "apigwv2_integration_responses")
	apigwv2RouteResponses = sim.MakeStore[APIGWv2RouteResponse](srv.DB(), "apigwv2_route_responses")

	mux := srv
	apiResource := cloudTrailRESTResource("AWS::ApiGatewayV2::Api", "apiId")

	// Integration responses — children of an integration.
	mux.HandleFunc("POST /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses", cloudTrailRecordedREST("CreateIntegrationResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWv2CreateIntegrationResponse))
	mux.HandleFunc("GET /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses", cloudTrailRecordedREST("GetIntegrationResponses", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ListIntegrationResponses))
	mux.HandleFunc("GET /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}", cloudTrailRecordedREST("GetIntegrationResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetIntegrationResponse))
	mux.HandleFunc("PATCH /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}", cloudTrailRecordedREST("UpdateIntegrationResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWv2UpdateIntegrationResponse))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/integrations/{integrationId}/integrationresponses/{integrationResponseId}", cloudTrailRecordedREST("DeleteIntegrationResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteIntegrationResponse))

	// Route responses — children of a route.
	mux.HandleFunc("POST /v2/apis/{apiId}/routes/{routeId}/routeresponses", cloudTrailRecordedREST("CreateRouteResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWv2CreateRouteResponse))
	mux.HandleFunc("GET /v2/apis/{apiId}/routes/{routeId}/routeresponses", cloudTrailRecordedREST("GetRouteResponses", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ListRouteResponses))
	mux.HandleFunc("GET /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}", cloudTrailRecordedREST("GetRouteResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetRouteResponse))
	mux.HandleFunc("PATCH /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}", cloudTrailRecordedREST("UpdateRouteResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWv2UpdateRouteResponse))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/routes/{routeId}/routeresponses/{routeResponseId}", cloudTrailRecordedREST("DeleteRouteResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteRouteResponse))

	// Model template — the schema template of a stored model.
	mux.HandleFunc("GET /v2/apis/{apiId}/models/{modelId}/template", cloudTrailRecordedREST("GetModelTemplate", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetModelTemplate))

	// OpenAPI import / reimport / export.
	mux.HandleFunc("PUT /v2/apis", cloudTrailRecordedREST("ImportApi", "apigateway.amazonaws.com", nil, handleAPIGWv2ImportApi))
	mux.HandleFunc("PUT /v2/apis/{apiId}", cloudTrailRecordedREST("ReimportApi", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ReimportApi))
	mux.HandleFunc("GET /v2/apis/{apiId}/exports/{specification}", cloudTrailRecordedREST("ExportApi", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ExportApi))

	// Resource tags read.
	mux.HandleFunc("GET /v2/tags/{resourceArn}", cloudTrailRecordedREST("GetTags", "apigateway.amazonaws.com", nil, handleAPIGWv2GetTags))

	// Per-stage / per-api config deletes.
	mux.HandleFunc("DELETE /v2/apis/{apiId}/stages/{stageName}/accesslogsettings", cloudTrailRecordedREST("DeleteAccessLogSettings", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteAccessLogSettings))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/cors", cloudTrailRecordedREST("DeleteCorsConfiguration", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteCorsConfiguration))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/routes/{routeId}/requestparameters/{requestParameterKey}", cloudTrailRecordedREST("DeleteRouteRequestParameter", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteRouteRequestParameter))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/stages/{stageName}/routesettings/{routeKey}", cloudTrailRecordedREST("DeleteRouteSettings", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteRouteSettings))
}

func apigwv2IntegrationResponseKey(apiId, integrationId, responseId string) string {
	return apiId + "/" + integrationId + "/" + responseId
}

func apigwv2RouteResponseKey(apiId, routeId, responseId string) string {
	return apiId + "/" + routeId + "/" + responseId
}

func handleAPIGWv2CreateIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	integrationId := sim.PathParam(r, "integrationId")
	if _, ok := apigwv2Integrations.Get(apigwv2StoreKey(apiId, integrationId)); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Integration identifier specified %s", integrationId)
		return
	}
	var req struct {
		IntegrationResponseKey      string            `json:"IntegrationResponseKey"`
		ContentHandlingStrategy     string            `json:"ContentHandlingStrategy"`
		ResponseParameters          map[string]string `json:"ResponseParameters"`
		ResponseTemplates           map[string]string `json:"ResponseTemplates"`
		TemplateSelectionExpression string            `json:"TemplateSelectionExpression"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.IntegrationResponseKey == "" {
		AWSError(w, "BadRequestException", "IntegrationResponseKey is required", http.StatusBadRequest)
		return
	}
	ir := APIGWv2IntegrationResponse{
		IntegrationResponseId:       generateUUID()[:10],
		ApiId:                       apiId,
		IntegrationId:               integrationId,
		IntegrationResponseKey:      req.IntegrationResponseKey,
		ContentHandlingStrategy:     req.ContentHandlingStrategy,
		ResponseParameters:          req.ResponseParameters,
		ResponseTemplates:           req.ResponseTemplates,
		TemplateSelectionExpression: req.TemplateSelectionExpression,
	}
	apigwv2IntegrationResponses.Put(apigwv2IntegrationResponseKey(apiId, integrationId, ir.IntegrationResponseId), ir)
	sim.WriteJSON(w, http.StatusCreated, ir)
}

func handleAPIGWv2ListIntegrationResponses(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	integrationId := sim.PathParam(r, "integrationId")
	if _, ok := apigwv2Integrations.Get(apigwv2StoreKey(apiId, integrationId)); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Integration identifier specified %s", integrationId)
		return
	}
	out := []APIGWv2IntegrationResponse{}
	for _, ir := range apigwv2IntegrationResponses.List() {
		if ir.ApiId == apiId && ir.IntegrationId == integrationId {
			out = append(out, ir)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	integrationId := sim.PathParam(r, "integrationId")
	responseId := sim.PathParam(r, "integrationResponseId")
	ir, ok := apigwv2IntegrationResponses.Get(apigwv2IntegrationResponseKey(apiId, integrationId, responseId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid IntegrationResponse identifier specified %s", responseId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ir)
}

func handleAPIGWv2UpdateIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	integrationId := sim.PathParam(r, "integrationId")
	responseId := sim.PathParam(r, "integrationResponseId")
	key := apigwv2IntegrationResponseKey(apiId, integrationId, responseId)
	ir, ok := apigwv2IntegrationResponses.Get(key)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid IntegrationResponse identifier specified %s", responseId)
		return
	}
	// PATCH is a partial update: pointer fields distinguish absent from empty.
	var req struct {
		IntegrationResponseKey      *string           `json:"IntegrationResponseKey"`
		ContentHandlingStrategy     *string           `json:"ContentHandlingStrategy"`
		ResponseParameters          map[string]string `json:"ResponseParameters"`
		ResponseTemplates           map[string]string `json:"ResponseTemplates"`
		TemplateSelectionExpression *string           `json:"TemplateSelectionExpression"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.IntegrationResponseKey != nil {
		ir.IntegrationResponseKey = *req.IntegrationResponseKey
	}
	if req.ContentHandlingStrategy != nil {
		ir.ContentHandlingStrategy = *req.ContentHandlingStrategy
	}
	if req.ResponseParameters != nil {
		ir.ResponseParameters = req.ResponseParameters
	}
	if req.ResponseTemplates != nil {
		ir.ResponseTemplates = req.ResponseTemplates
	}
	if req.TemplateSelectionExpression != nil {
		ir.TemplateSelectionExpression = *req.TemplateSelectionExpression
	}
	apigwv2IntegrationResponses.Put(key, ir)
	sim.WriteJSON(w, http.StatusOK, ir)
}

func handleAPIGWv2DeleteIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	integrationId := sim.PathParam(r, "integrationId")
	responseId := sim.PathParam(r, "integrationResponseId")
	if !apigwv2IntegrationResponses.Delete(apigwv2IntegrationResponseKey(apiId, integrationId, responseId)) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid IntegrationResponse identifier specified %s", responseId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateRouteResponse(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	if _, ok := apigwv2Routes.Get(apigwv2StoreKey(apiId, routeId)); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Route identifier specified %s", routeId)
		return
	}
	var req struct {
		RouteResponseKey         string                            `json:"RouteResponseKey"`
		ModelSelectionExpression string                            `json:"ModelSelectionExpression"`
		ResponseModels           map[string]string                 `json:"ResponseModels"`
		ResponseParameters       map[string]APIGWv2ParamConstraint `json:"ResponseParameters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.RouteResponseKey == "" {
		AWSError(w, "BadRequestException", "RouteResponseKey is required", http.StatusBadRequest)
		return
	}
	rr := APIGWv2RouteResponse{
		RouteResponseId:          generateUUID()[:10],
		ApiId:                    apiId,
		RouteId:                  routeId,
		RouteResponseKey:         req.RouteResponseKey,
		ModelSelectionExpression: req.ModelSelectionExpression,
		ResponseModels:           req.ResponseModels,
		ResponseParameters:       req.ResponseParameters,
	}
	apigwv2RouteResponses.Put(apigwv2RouteResponseKey(apiId, routeId, rr.RouteResponseId), rr)
	sim.WriteJSON(w, http.StatusCreated, rr)
}

func handleAPIGWv2ListRouteResponses(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	if _, ok := apigwv2Routes.Get(apigwv2StoreKey(apiId, routeId)); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Route identifier specified %s", routeId)
		return
	}
	out := []APIGWv2RouteResponse{}
	for _, rr := range apigwv2RouteResponses.List() {
		if rr.ApiId == apiId && rr.RouteId == routeId {
			out = append(out, rr)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetRouteResponse(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	responseId := sim.PathParam(r, "routeResponseId")
	rr, ok := apigwv2RouteResponses.Get(apigwv2RouteResponseKey(apiId, routeId, responseId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid RouteResponse identifier specified %s", responseId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, rr)
}

func handleAPIGWv2UpdateRouteResponse(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	responseId := sim.PathParam(r, "routeResponseId")
	key := apigwv2RouteResponseKey(apiId, routeId, responseId)
	rr, ok := apigwv2RouteResponses.Get(key)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid RouteResponse identifier specified %s", responseId)
		return
	}
	var req struct {
		RouteResponseKey         *string                           `json:"RouteResponseKey"`
		ModelSelectionExpression *string                           `json:"ModelSelectionExpression"`
		ResponseModels           map[string]string                 `json:"ResponseModels"`
		ResponseParameters       map[string]APIGWv2ParamConstraint `json:"ResponseParameters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.RouteResponseKey != nil {
		rr.RouteResponseKey = *req.RouteResponseKey
	}
	if req.ModelSelectionExpression != nil {
		rr.ModelSelectionExpression = *req.ModelSelectionExpression
	}
	if req.ResponseModels != nil {
		rr.ResponseModels = req.ResponseModels
	}
	if req.ResponseParameters != nil {
		rr.ResponseParameters = req.ResponseParameters
	}
	apigwv2RouteResponses.Put(key, rr)
	sim.WriteJSON(w, http.StatusOK, rr)
}

func handleAPIGWv2DeleteRouteResponse(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	responseId := sim.PathParam(r, "routeResponseId")
	if !apigwv2RouteResponses.Delete(apigwv2RouteResponseKey(apiId, routeId, responseId)) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid RouteResponse identifier specified %s", responseId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2GetModelTemplate(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	modelId := sim.PathParam(r, "modelId")
	m, ok := apigwv2Models.Get(apigwv2StoreKey(apiId, modelId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Model identifier specified %s", modelId)
		return
	}
	// GetModelTemplate returns the model's schema as a mapping template.
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": m.Schema})
}

func handleAPIGWv2ImportApi(w http.ResponseWriter, r *http.Request) {
	api, err := apigwv2APIFromOpenAPI(r, "")
	if err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	apigwv2Apis.Put(api.ApiId, api)
	sim.WriteJSON(w, http.StatusCreated, api)
}

func handleAPIGWv2ReimportApi(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	existing, ok := apigwv2Apis.Get(apiId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	api, err := apigwv2APIFromOpenAPI(r, apiId)
	if err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	// Reimport replaces the definition but keeps the API's identity coordinates.
	api.ApiId = existing.ApiId
	api.ApiEndpoint = existing.ApiEndpoint
	api.CreatedDate = existing.CreatedDate
	if api.Tags == nil {
		api.Tags = existing.Tags
	}
	apigwv2Apis.Put(api.ApiId, api)
	sim.WriteJSON(w, http.StatusCreated, api)
}

// apigwv2APIFromOpenAPI builds an APIGWv2Api from an OpenAPI document in the
// request body. The body member carries the raw OpenAPI JSON/YAML; we read the
// info.title as the API name and default to an HTTP protocol. A fresh apiId is
// minted unless one is supplied (reimport).
func apigwv2APIFromOpenAPI(r *http.Request, apiId string) (APIGWv2Api, error) {
	var req struct {
		Body string `json:"body"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		return APIGWv2Api{}, err
	}
	if strings.TrimSpace(req.Body) == "" {
		return APIGWv2Api{}, errAPIGWv2EmptyBody
	}
	name := apigwv2OpenAPITitle(req.Body)
	if apiId == "" {
		apiId = generateUUID()[:10]
	}
	return APIGWv2Api{
		ApiId:        apiId,
		Name:         name,
		ProtocolType: "HTTP",
		ApiEndpoint:  "https://" + apiId + ".execute-api." + awsRegion() + ".amazonaws.com",
		CreatedDate:  apigwv2Now(),
	}, nil
}

func handleAPIGWv2ExportApi(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	api, ok := apigwv2Apis.Get(apiId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	// The export is the OpenAPI 3.0 document for the stored API, shaped from
	// the API's routes. ExportApiResponse has a single blob @httpPayload
	// member, so the body is the raw document (no JSON envelope).
	doc := apigwv2ExportDocument(api)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

func handleAPIGWv2GetTags(w http.ResponseWriter, r *http.Request) {
	arn := sim.PathParam(r, "resourceArn")
	tags := apigwv2TagsForARN(arn)
	if tags == nil {
		tags = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// apigwv2TagsForARN resolves the tags stored on the resource the ARN names.
// apigatewayv2 ARNs are arn:aws:apigateway:<region>::/apis/<id>,
// .../domainnames/<name>, or .../vpclinks/<id>.
func apigwv2TagsForARN(arn string) map[string]string {
	switch {
	case strings.Contains(arn, "/apis/"):
		id := arn[strings.LastIndex(arn, "/apis/")+len("/apis/"):]
		if a, ok := apigwv2Apis.Get(id); ok {
			return a.Tags
		}
	case strings.Contains(arn, "/domainnames/"):
		name := arn[strings.LastIndex(arn, "/domainnames/")+len("/domainnames/"):]
		if d, ok := apigwv2DomainNames.Get(name); ok {
			return d.Tags
		}
	case strings.Contains(arn, "/vpclinks/"):
		id := arn[strings.LastIndex(arn, "/vpclinks/")+len("/vpclinks/"):]
		if v, ok := apigwv2VpcLinks.Get(id); ok {
			return v.Tags
		}
	}
	return nil
}

func handleAPIGWv2DeleteAccessLogSettings(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	stageName := sim.PathParam(r, "stageName")
	if _, ok := apigwv2Stages.Get(apigwv2StoreKey(apiId, stageName)); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Stage identifier specified %s", stageName)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2DeleteCorsConfiguration(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	if _, ok := apigwv2Apis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2DeleteRouteRequestParameter(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	if _, ok := apigwv2Routes.Get(apigwv2StoreKey(apiId, routeId)); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Route identifier specified %s", routeId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2DeleteRouteSettings(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	stageName := sim.PathParam(r, "stageName")
	if _, ok := apigwv2Stages.Get(apigwv2StoreKey(apiId, stageName)); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Stage identifier specified %s", stageName)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
