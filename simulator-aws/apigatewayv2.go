package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// API Gateway v2 (HTTP API + WebSocket) — restJson1 protocol, REST
// path routing under /v2/. Surface scoped to the routes-+-stages
// CRUD that terraform-provider-aws + aws-sdk-go-v2's `apigatewayv2`
// client + the `aws apigatewayv2` CLI exercise for the 90th-
// percentile "deploy an HTTP API + integration + stage" flow.

type APIGWv2Api struct {
	ApiId                     string            `json:"apiId"`
	ApiKeySelectionExpression string            `json:"apiKeySelectionExpression,omitempty"`
	Name                      string            `json:"name"`
	ProtocolType              string            `json:"protocolType"`
	RouteKey                  string            `json:"routeSelectionExpression,omitempty"`
	ApiEndpoint               string            `json:"apiEndpoint,omitempty"` // external: the HTTP API invoke URL (<api-id>.execute-api.<region>.amazonaws.com) — a canonical AWS host the sim does not serve as a data plane (like ECR repositoryUri / Amplify WebhookUrl)
	CreatedDate               string            `json:"createdDate"`
	Tags                      map[string]string `json:"tags,omitempty"`
}

// Inner fields whose JSON tag is `apiIdRef` (custom, non-public)
// hold the parent reference for cascade-delete + per-api filtering.
// The SDK ignores unknown fields, so leaking the parent ID on the
// response is harmless and avoids stripping the field during Store
// persistence.
type APIGWv2Route struct {
	RouteId           string `json:"routeId"`
	ApiId             string `json:"apiIdRef,omitempty"`
	RouteKey          string `json:"routeKey"`
	Target            string `json:"target,omitempty"`
	AuthorizationType string `json:"authorizationType,omitempty"`
	ApiKeyRequired    bool   `json:"apiKeyRequired,omitempty"`
	OperationName     string `json:"operationName,omitempty"`
}

type APIGWv2Integration struct {
	IntegrationId        string `json:"integrationId"`
	ApiId                string `json:"apiIdRef,omitempty"`
	ConnectionType       string `json:"connectionType,omitempty"`
	IntegrationType      string `json:"integrationType"`
	IntegrationUri       string `json:"integrationUri,omitempty"` // external (operator-supplied): integration target — Lambda ARN, HTTP backend, or VPC link target
	IntegrationMethod    string `json:"integrationMethod,omitempty"`
	PayloadFormatVersion string `json:"payloadFormatVersion,omitempty"`
	TimeoutInMillis      int    `json:"timeoutInMillis,omitempty"`
}

type APIGWv2Stage struct {
	StageName    string            `json:"stageName"`
	ApiId        string            `json:"apiIdRef,omitempty"`
	Description  string            `json:"description,omitempty"`
	DeploymentId string            `json:"deploymentId,omitempty"`
	AutoDeploy   bool              `json:"autoDeploy"`
	CreatedDate  string            `json:"createdDate"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// APIGWv2Deployment models the snapshot of an HTTP API's routes +
// integrations + stages at a point in time. terraform-provider-aws
// emits `POST /v2/apis/{apiId}/deployments` on `aws_apigatewayv2_deployment`
// — without this route registered the request used to fall through
// to the S3 wildcard dispatcher and 400 with an InvalidRequest envelope.
type APIGWv2Deployment struct {
	DeploymentId     string `json:"deploymentId"`
	ApiId            string `json:"apiIdRef,omitempty"`
	Description      string `json:"description,omitempty"`
	DeploymentStatus string `json:"deploymentStatus"` // DEPLOYED | PENDING | FAILED
	CreatedDate      string `json:"createdDate"`
}

// APIGWv2JWTConfig mirrors the JWTConfiguration shape on a JWT authorizer.
// restJson1 serializes v2 members in camelCase (the smithy jsonName trait),
// so every response tag is the lower-camel wire name.
type APIGWv2JWTConfig struct {
	Audience []string `json:"audience,omitempty"`
	Issuer   string   `json:"issuer,omitempty"`
}

// APIGWv2Authorizer mirrors aws_apigatewayv2_authorizer. Keyed by
// `<apiId>/<authorizerId>`.
type APIGWv2Authorizer struct {
	AuthorizerId                   string            `json:"authorizerId"`
	ApiId                          string            `json:"apiIdRef,omitempty"`
	Name                           string            `json:"name,omitempty"`
	AuthorizerType                 string            `json:"authorizerType,omitempty"`
	AuthorizerUri                  string            `json:"authorizerUri,omitempty"`
	AuthorizerCredentialsArn       string            `json:"authorizerCredentialsArn,omitempty"`
	AuthorizerPayloadFormatVersion string            `json:"authorizerPayloadFormatVersion,omitempty"`
	AuthorizerResultTtlInSeconds   int               `json:"authorizerResultTtlInSeconds,omitempty"`
	EnableSimpleResponses          bool              `json:"enableSimpleResponses,omitempty"`
	IdentitySource                 []string          `json:"identitySource,omitempty"`
	IdentityValidationExpression   string            `json:"identityValidationExpression,omitempty"`
	JwtConfiguration               *APIGWv2JWTConfig `json:"jwtConfiguration,omitempty"`
}

// APIGWv2Model mirrors aws_apigatewayv2_model. Keyed by `<apiId>/<modelId>`.
type APIGWv2Model struct {
	ModelId     string `json:"modelId"`
	ApiId       string `json:"apiIdRef,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Schema      string `json:"schema,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// APIGWv2DomainName mirrors aws_apigatewayv2_domain_name. Keyed by the
// domain name itself.
type APIGWv2DomainName struct {
	DomainName                    string                    `json:"domainName"`
	DomainNameArn                 string                    `json:"domainNameArn,omitempty"` // external: synthesized domain-name ARN
	ApiMappingSelectionExpression string                    `json:"apiMappingSelectionExpression,omitempty"`
	DomainNameConfigurations      []APIGWv2DomainNameConfig `json:"domainNameConfigurations,omitempty"`
	Tags                          map[string]string         `json:"tags,omitempty"`
}

// APIGWv2DomainNameConfig mirrors the DomainNameConfiguration shape.
type APIGWv2DomainNameConfig struct {
	ApiGatewayDomainName string `json:"apiGatewayDomainName,omitempty"`
	CertificateArn       string `json:"certificateArn,omitempty"`
	CertificateName      string `json:"certificateName,omitempty"`
	DomainNameStatus     string `json:"domainNameStatus,omitempty"`
	EndpointType         string `json:"endpointType,omitempty"`
	HostedZoneId         string `json:"hostedZoneId,omitempty"`
	SecurityPolicy       string `json:"securityPolicy,omitempty"`
}

// APIGWv2ApiMapping mirrors aws_apigatewayv2_api_mapping. Keyed by
// `<domainName>/<apiMappingId>`; the parent domain rides `domainNameRef`.
type APIGWv2ApiMapping struct {
	ApiMappingId  string `json:"apiMappingId"`
	ApiId         string `json:"apiId,omitempty"`
	ApiMappingKey string `json:"apiMappingKey,omitempty"`
	Stage         string `json:"stage,omitempty"`
	DomainName    string `json:"domainNameRef,omitempty"`
}

// APIGWv2VpcLink mirrors aws_apigatewayv2_vpc_link. Keyed by its own id.
type APIGWv2VpcLink struct {
	VpcLinkId        string            `json:"vpcLinkId"`
	Name             string            `json:"name,omitempty"`
	SecurityGroupIds []string          `json:"securityGroupIds,omitempty"`
	SubnetIds        []string          `json:"subnetIds,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	CreatedDate      string            `json:"createdDate,omitempty"`
	VpcLinkStatus    string            `json:"vpcLinkStatus,omitempty"`
	VpcLinkVersion   string            `json:"vpcLinkVersion,omitempty"`
}

var (
	apigwv2Apis         sim.Store[APIGWv2Api]
	apigwv2Routes       sim.Store[APIGWv2Route]
	apigwv2Integrations sim.Store[APIGWv2Integration]
	apigwv2Stages       sim.Store[APIGWv2Stage]
	apigwv2Deployments  sim.Store[APIGWv2Deployment]
	apigwv2Authorizers  sim.Store[APIGWv2Authorizer]
	apigwv2Models       sim.Store[APIGWv2Model]
	apigwv2DomainNames  sim.Store[APIGWv2DomainName]
	apigwv2ApiMappings  sim.Store[APIGWv2ApiMapping]
	apigwv2VpcLinks     sim.Store[APIGWv2VpcLink]
)

func registerAPIGatewayV2(srv *sim.Server) {
	apigwv2Apis = sim.MakeStore[APIGWv2Api](srv.DB(), "apigwv2_apis")
	apigwv2Routes = sim.MakeStore[APIGWv2Route](srv.DB(), "apigwv2_routes")
	apigwv2Integrations = sim.MakeStore[APIGWv2Integration](srv.DB(), "apigwv2_integrations")
	apigwv2Stages = sim.MakeStore[APIGWv2Stage](srv.DB(), "apigwv2_stages")
	apigwv2Deployments = sim.MakeStore[APIGWv2Deployment](srv.DB(), "apigwv2_deployments")
	apigwv2Authorizers = sim.MakeStore[APIGWv2Authorizer](srv.DB(), "apigwv2_authorizers")
	apigwv2Models = sim.MakeStore[APIGWv2Model](srv.DB(), "apigwv2_models")
	apigwv2DomainNames = sim.MakeStore[APIGWv2DomainName](srv.DB(), "apigwv2_domainnames")
	apigwv2ApiMappings = sim.MakeStore[APIGWv2ApiMapping](srv.DB(), "apigwv2_apimappings")
	apigwv2VpcLinks = sim.MakeStore[APIGWv2VpcLink](srv.DB(), "apigwv2_vpclinks")

	mux := srv
	apiResource := cloudTrailRESTResource("AWS::ApiGatewayV2::Api", "apiId")
	mux.HandleFunc("POST /v2/apis", cloudTrailRecordedREST("CreateApi", "apigateway.amazonaws.com", nil, handleAPIGWv2CreateApi))
	mux.HandleFunc("GET /v2/apis", cloudTrailRecordedREST("GetApis", "apigateway.amazonaws.com", nil, handleAPIGWv2ListApis))
	mux.HandleFunc("GET /v2/apis/{apiId}", cloudTrailRecordedREST("GetApi", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetApi))
	mux.HandleFunc("DELETE /v2/apis/{apiId}", cloudTrailRecordedREST("DeleteApi", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteApi))
	mux.HandleFunc("POST /v2/apis/{apiId}/routes", cloudTrailRecordedREST("CreateRoute", "apigateway.amazonaws.com", apiResource, handleAPIGWv2CreateRoute))
	mux.HandleFunc("GET /v2/apis/{apiId}/routes", cloudTrailRecordedREST("GetRoutes", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ListRoutes))
	mux.HandleFunc("GET /v2/apis/{apiId}/routes/{routeId}", cloudTrailRecordedREST("GetRoute", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetRoute))
	mux.HandleFunc("PATCH /v2/apis/{apiId}/routes/{routeId}", cloudTrailRecordedREST("UpdateRoute", "apigateway.amazonaws.com", apiResource, handleAPIGWv2UpdateRoute))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/routes/{routeId}", cloudTrailRecordedREST("DeleteRoute", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteRoute))
	mux.HandleFunc("POST /v2/apis/{apiId}/integrations", cloudTrailRecordedREST("CreateIntegration", "apigateway.amazonaws.com", apiResource, handleAPIGWv2CreateIntegration))
	mux.HandleFunc("GET /v2/apis/{apiId}/integrations", cloudTrailRecordedREST("GetIntegrations", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ListIntegrations))
	mux.HandleFunc("GET /v2/apis/{apiId}/integrations/{integrationId}", cloudTrailRecordedREST("GetIntegration", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetIntegration))
	mux.HandleFunc("PATCH /v2/apis/{apiId}/integrations/{integrationId}", cloudTrailRecordedREST("UpdateIntegration", "apigateway.amazonaws.com", apiResource, handleAPIGWv2UpdateIntegration))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/integrations/{integrationId}", cloudTrailRecordedREST("DeleteIntegration", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteIntegration))
	mux.HandleFunc("POST /v2/apis/{apiId}/stages", cloudTrailRecordedREST("CreateStage", "apigateway.amazonaws.com", apiResource, handleAPIGWv2CreateStage))
	mux.HandleFunc("GET /v2/apis/{apiId}/stages", cloudTrailRecordedREST("GetStages", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ListStages))
	mux.HandleFunc("GET /v2/apis/{apiId}/stages/{stageName}", cloudTrailRecordedREST("GetStage", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetStage))
	mux.HandleFunc("PATCH /v2/apis/{apiId}/stages/{stageName}", cloudTrailRecordedREST("UpdateStage", "apigateway.amazonaws.com", apiResource, handleAPIGWv2UpdateStage))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/stages/{stageName}", cloudTrailRecordedREST("DeleteStage", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteStage))
	mux.HandleFunc("POST /v2/apis/{apiId}/deployments", cloudTrailRecordedREST("CreateDeployment", "apigateway.amazonaws.com", apiResource, handleAPIGWv2CreateDeployment))
	mux.HandleFunc("GET /v2/apis/{apiId}/deployments", cloudTrailRecordedREST("GetDeployments", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ListDeployments))
	mux.HandleFunc("GET /v2/apis/{apiId}/deployments/{deploymentId}", cloudTrailRecordedREST("GetDeployment", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetDeployment))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/deployments/{deploymentId}", cloudTrailRecordedREST("DeleteDeployment", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteDeployment))

	// Authorizers — scoped to an API.
	mux.HandleFunc("POST /v2/apis/{apiId}/authorizers", cloudTrailRecordedREST("CreateAuthorizer", "apigateway.amazonaws.com", apiResource, handleAPIGWv2CreateAuthorizer))
	mux.HandleFunc("GET /v2/apis/{apiId}/authorizers", cloudTrailRecordedREST("GetAuthorizers", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ListAuthorizers))
	mux.HandleFunc("GET /v2/apis/{apiId}/authorizers/{authorizerId}", cloudTrailRecordedREST("GetAuthorizer", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetAuthorizer))
	mux.HandleFunc("PATCH /v2/apis/{apiId}/authorizers/{authorizerId}", cloudTrailRecordedREST("UpdateAuthorizer", "apigateway.amazonaws.com", apiResource, handleAPIGWv2UpdateAuthorizer))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/authorizers/{authorizerId}", cloudTrailRecordedREST("DeleteAuthorizer", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteAuthorizer))

	// Models — scoped to an API.
	mux.HandleFunc("POST /v2/apis/{apiId}/models", cloudTrailRecordedREST("CreateModel", "apigateway.amazonaws.com", apiResource, handleAPIGWv2CreateModel))
	mux.HandleFunc("GET /v2/apis/{apiId}/models", cloudTrailRecordedREST("GetModels", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ListModels))
	mux.HandleFunc("GET /v2/apis/{apiId}/models/{modelId}", cloudTrailRecordedREST("GetModel", "apigateway.amazonaws.com", apiResource, handleAPIGWv2GetModel))
	mux.HandleFunc("DELETE /v2/apis/{apiId}/models/{modelId}", cloudTrailRecordedREST("DeleteModel", "apigateway.amazonaws.com", apiResource, handleAPIGWv2DeleteModel))

	// Domain names + API mappings.
	domainResource := cloudTrailRESTResource("AWS::ApiGatewayV2::DomainName", "domainName")
	mux.HandleFunc("POST /v2/domainnames", cloudTrailRecordedREST("CreateDomainName", "apigateway.amazonaws.com", nil, handleAPIGWv2CreateDomainName))
	mux.HandleFunc("GET /v2/domainnames", cloudTrailRecordedREST("GetDomainNames", "apigateway.amazonaws.com", nil, handleAPIGWv2ListDomainNames))
	mux.HandleFunc("GET /v2/domainnames/{domainName}", cloudTrailRecordedREST("GetDomainName", "apigateway.amazonaws.com", domainResource, handleAPIGWv2GetDomainName))
	mux.HandleFunc("DELETE /v2/domainnames/{domainName}", cloudTrailRecordedREST("DeleteDomainName", "apigateway.amazonaws.com", domainResource, handleAPIGWv2DeleteDomainName))
	mux.HandleFunc("POST /v2/domainnames/{domainName}/apimappings", cloudTrailRecordedREST("CreateApiMapping", "apigateway.amazonaws.com", domainResource, handleAPIGWv2CreateApiMapping))
	mux.HandleFunc("GET /v2/domainnames/{domainName}/apimappings", cloudTrailRecordedREST("GetApiMappings", "apigateway.amazonaws.com", domainResource, handleAPIGWv2ListApiMappings))
	mux.HandleFunc("GET /v2/domainnames/{domainName}/apimappings/{apiMappingId}", cloudTrailRecordedREST("GetApiMapping", "apigateway.amazonaws.com", domainResource, handleAPIGWv2GetApiMapping))
	mux.HandleFunc("DELETE /v2/domainnames/{domainName}/apimappings/{apiMappingId}", cloudTrailRecordedREST("DeleteApiMapping", "apigateway.amazonaws.com", domainResource, handleAPIGWv2DeleteApiMapping))

	// VPC links — top-level resources keyed by their own id.
	vpcLinkResource := cloudTrailRESTResource("AWS::ApiGatewayV2::VpcLink", "vpcLinkId")
	mux.HandleFunc("POST /v2/vpclinks", cloudTrailRecordedREST("CreateVpcLink", "apigateway.amazonaws.com", nil, handleAPIGWv2CreateVpcLink))
	mux.HandleFunc("GET /v2/vpclinks", cloudTrailRecordedREST("GetVpcLinks", "apigateway.amazonaws.com", nil, handleAPIGWv2ListVpcLinks))
	mux.HandleFunc("GET /v2/vpclinks/{vpcLinkId}", cloudTrailRecordedREST("GetVpcLink", "apigateway.amazonaws.com", vpcLinkResource, handleAPIGWv2GetVpcLink))
	mux.HandleFunc("DELETE /v2/vpclinks/{vpcLinkId}", cloudTrailRecordedREST("DeleteVpcLink", "apigateway.amazonaws.com", vpcLinkResource, handleAPIGWv2DeleteVpcLink))

	registerAPIGatewayV2Extras(srv)
	registerAPIGatewayV2Complete(srv)
}

func apigwv2StoreKey(apiId, resource string) string { return apiId + "/" + resource }

func handleAPIGWv2CreateApi(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApiKeySelectionExpression string            `json:"ApiKeySelectionExpression"`
		Name                      string            `json:"Name"`
		ProtocolType              string            `json:"ProtocolType"`
		RouteSelectionExpression  string            `json:"RouteSelectionExpression"`
		Tags                      map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ApiKeySelectionExpression == "" {
		req.ApiKeySelectionExpression = "$request.header.x-api-key"
	}
	apiID := generateUUID()[:10]
	api := APIGWv2Api{
		ApiId:                     apiID,
		ApiKeySelectionExpression: req.ApiKeySelectionExpression,
		Name:                      req.Name,
		ProtocolType:              req.ProtocolType,
		RouteKey:                  req.RouteSelectionExpression,
		// external: canonical HTTP API invoke host; the sim does not serve the
		// execute-api data plane (see the ApiEndpoint field comment).
		ApiEndpoint: fmt.Sprintf("https://%s.execute-api.%s.amazonaws.com", apiID, awsRegion()),
		CreatedDate: time.Now().UTC().Format(time.RFC3339),
		Tags:        req.Tags,
	}
	apigwv2Apis.Put(api.ApiId, api)
	sim.WriteJSON(w, http.StatusCreated, api)
}

func handleAPIGWv2ListApis(w http.ResponseWriter, r *http.Request) {
	all := apigwv2Apis.List()
	if all == nil {
		all = []APIGWv2Api{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": all})
}

func handleAPIGWv2GetApi(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	api, ok := apigwv2Apis.Get(apiId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	sim.WriteJSON(w, http.StatusOK, api)
}

func handleAPIGWv2DeleteApi(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	if !apigwv2Apis.Delete(apiId) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	// Cascade-delete children.
	for _, rt := range apigwv2Routes.List() {
		if rt.ApiId == apiId {
			apigwv2Routes.Delete(apigwv2StoreKey(apiId, rt.RouteId))
		}
	}
	for _, in := range apigwv2Integrations.List() {
		if in.ApiId == apiId {
			apigwv2Integrations.Delete(apigwv2StoreKey(apiId, in.IntegrationId))
		}
	}
	for _, s := range apigwv2Stages.List() {
		if s.ApiId == apiId {
			apigwv2Stages.Delete(apigwv2StoreKey(apiId, s.StageName))
		}
	}
	for _, d := range apigwv2Deployments.List() {
		if d.ApiId == apiId {
			apigwv2Deployments.Delete(apigwv2StoreKey(apiId, d.DeploymentId))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateRoute(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	if _, ok := apigwv2Apis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	var req struct {
		RouteKey          string `json:"RouteKey"`
		Target            string `json:"Target"`
		AuthorizationType string `json:"AuthorizationType"`
		ApiKeyRequired    bool   `json:"ApiKeyRequired"`
		OperationName     string `json:"OperationName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	route := APIGWv2Route{
		RouteId:           generateUUID()[:10],
		ApiId:             apiId,
		RouteKey:          req.RouteKey,
		Target:            req.Target,
		AuthorizationType: req.AuthorizationType,
		ApiKeyRequired:    req.ApiKeyRequired,
		OperationName:     req.OperationName,
	}
	apigwv2Routes.Put(apigwv2StoreKey(apiId, route.RouteId), route)
	sim.WriteJSON(w, http.StatusCreated, route)
}

func handleAPIGWv2ListRoutes(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	var out []APIGWv2Route
	for _, rt := range apigwv2Routes.List() {
		if rt.ApiId == apiId {
			out = append(out, rt)
		}
	}
	if out == nil {
		out = []APIGWv2Route{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetRoute(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	route, ok := apigwv2Routes.Get(apigwv2StoreKey(apiId, routeId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Route identifier specified %s", routeId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, route)
}

func handleAPIGWv2UpdateRoute(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	route, ok := apigwv2Routes.Get(apigwv2StoreKey(apiId, routeId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Route identifier specified %s", routeId)
		return
	}
	// PATCH is a partial update: pointer fields distinguish absent from empty,
	// so an omitted field leaves the stored value unchanged.
	var req struct {
		RouteKey          *string `json:"RouteKey"`
		Target            *string `json:"Target"`
		AuthorizationType *string `json:"AuthorizationType"`
		ApiKeyRequired    *bool   `json:"ApiKeyRequired"`
		OperationName     *string `json:"OperationName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.RouteKey != nil {
		route.RouteKey = *req.RouteKey
	}
	if req.Target != nil {
		route.Target = *req.Target
	}
	if req.AuthorizationType != nil {
		route.AuthorizationType = *req.AuthorizationType
	}
	if req.ApiKeyRequired != nil {
		route.ApiKeyRequired = *req.ApiKeyRequired
	}
	if req.OperationName != nil {
		route.OperationName = *req.OperationName
	}
	apigwv2Routes.Put(apigwv2StoreKey(apiId, routeId), route)
	sim.WriteJSON(w, http.StatusOK, route)
}

func handleAPIGWv2DeleteRoute(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	routeId := sim.PathParam(r, "routeId")
	if !apigwv2Routes.Delete(apigwv2StoreKey(apiId, routeId)) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Route identifier specified %s", routeId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateIntegration(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	if _, ok := apigwv2Apis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	var req struct {
		ConnectionType       string `json:"ConnectionType"`
		IntegrationType      string `json:"IntegrationType"`
		IntegrationUri       string `json:"IntegrationUri"`
		IntegrationMethod    string `json:"IntegrationMethod"`
		PayloadFormatVersion string `json:"PayloadFormatVersion"`
		TimeoutInMillis      int    `json:"TimeoutInMillis"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.ConnectionType == "" {
		req.ConnectionType = "INTERNET"
	}
	in := APIGWv2Integration{
		IntegrationId:        generateUUID()[:10],
		ApiId:                apiId,
		ConnectionType:       req.ConnectionType,
		IntegrationType:      req.IntegrationType,
		IntegrationUri:       req.IntegrationUri,
		IntegrationMethod:    req.IntegrationMethod,
		PayloadFormatVersion: req.PayloadFormatVersion,
		TimeoutInMillis:      req.TimeoutInMillis,
	}
	apigwv2Integrations.Put(apigwv2StoreKey(apiId, in.IntegrationId), in)
	sim.WriteJSON(w, http.StatusCreated, in)
}

func handleAPIGWv2UpdateIntegration(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	integrationId := sim.PathParam(r, "integrationId")
	integration, ok := apigwv2Integrations.Get(apigwv2StoreKey(apiId, integrationId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Integration identifier specified %s", integrationId)
		return
	}
	var req struct {
		ConnectionType       *string `json:"ConnectionType"`
		IntegrationType      *string `json:"IntegrationType"`
		IntegrationUri       *string `json:"IntegrationUri"`
		IntegrationMethod    *string `json:"IntegrationMethod"`
		PayloadFormatVersion *string `json:"PayloadFormatVersion"`
		TimeoutInMillis      *int    `json:"TimeoutInMillis"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.ConnectionType != nil {
		integration.ConnectionType = *req.ConnectionType
	}
	if req.IntegrationType != nil {
		integration.IntegrationType = *req.IntegrationType
	}
	if req.IntegrationUri != nil {
		integration.IntegrationUri = *req.IntegrationUri
	}
	if req.IntegrationMethod != nil {
		integration.IntegrationMethod = *req.IntegrationMethod
	}
	if req.PayloadFormatVersion != nil {
		integration.PayloadFormatVersion = *req.PayloadFormatVersion
	}
	if req.TimeoutInMillis != nil {
		integration.TimeoutInMillis = *req.TimeoutInMillis
	}
	apigwv2Integrations.Put(apigwv2StoreKey(apiId, integrationId), integration)
	sim.WriteJSON(w, http.StatusOK, integration)
}

func handleAPIGWv2ListIntegrations(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	var out []APIGWv2Integration
	for _, in := range apigwv2Integrations.List() {
		if in.ApiId == apiId {
			out = append(out, in)
		}
	}
	if out == nil {
		out = []APIGWv2Integration{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetIntegration(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	integrationId := sim.PathParam(r, "integrationId")
	in, ok := apigwv2Integrations.Get(apigwv2StoreKey(apiId, integrationId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Integration identifier specified %s", integrationId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, in)
}

func handleAPIGWv2DeleteIntegration(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	integrationId := sim.PathParam(r, "integrationId")
	if !apigwv2Integrations.Delete(apigwv2StoreKey(apiId, integrationId)) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Integration identifier specified %s", integrationId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	if _, ok := apigwv2Apis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	var req struct {
		StageName    string            `json:"StageName"`
		Description  string            `json:"Description"`
		DeploymentId string            `json:"DeploymentId"`
		AutoDeploy   bool              `json:"AutoDeploy"`
		Tags         map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	s := APIGWv2Stage{
		StageName:    req.StageName,
		ApiId:        apiId,
		Description:  req.Description,
		DeploymentId: req.DeploymentId,
		AutoDeploy:   req.AutoDeploy,
		CreatedDate:  time.Now().UTC().Format(time.RFC3339),
		Tags:         req.Tags,
	}
	apigwv2Stages.Put(apigwv2StoreKey(apiId, s.StageName), s)
	sim.WriteJSON(w, http.StatusCreated, s)
}

func handleAPIGWv2UpdateStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	stageName := sim.PathParam(r, "stageName")
	stage, ok := apigwv2Stages.Get(apigwv2StoreKey(apiId, stageName))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Stage identifier specified %s", stageName)
		return
	}
	var req struct {
		Description  *string `json:"Description"`
		DeploymentId *string `json:"DeploymentId"`
		AutoDeploy   *bool   `json:"AutoDeploy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Description != nil {
		stage.Description = *req.Description
	}
	if req.DeploymentId != nil {
		stage.DeploymentId = *req.DeploymentId
	}
	if req.AutoDeploy != nil {
		stage.AutoDeploy = *req.AutoDeploy
	}
	apigwv2Stages.Put(apigwv2StoreKey(apiId, stageName), stage)
	sim.WriteJSON(w, http.StatusOK, stage)
}

func handleAPIGWv2ListStages(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	var out []APIGWv2Stage
	for _, s := range apigwv2Stages.List() {
		if s.ApiId == apiId {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []APIGWv2Stage{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	stageName := sim.PathParam(r, "stageName")
	stage, ok := apigwv2Stages.Get(apigwv2StoreKey(apiId, stageName))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Stage identifier specified %s", stageName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, stage)
}

func handleAPIGWv2DeleteStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	stageName := sim.PathParam(r, "stageName")
	if !apigwv2Stages.Delete(apigwv2StoreKey(apiId, stageName)) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Stage identifier specified %s", stageName)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateDeployment(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	if _, ok := apigwv2Apis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	var req struct {
		Description string `json:"Description"`
		StageName   string `json:"StageName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	d := APIGWv2Deployment{
		DeploymentId:     generateUUID()[:10],
		ApiId:            apiId,
		Description:      req.Description,
		DeploymentStatus: "DEPLOYED",
		CreatedDate:      time.Now().UTC().Format(time.RFC3339),
	}
	apigwv2Deployments.Put(apigwv2StoreKey(apiId, d.DeploymentId), d)
	sim.WriteJSON(w, http.StatusCreated, d)
}

func handleAPIGWv2ListDeployments(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	var out []APIGWv2Deployment
	for _, d := range apigwv2Deployments.List() {
		if d.ApiId == apiId {
			out = append(out, d)
		}
	}
	if out == nil {
		out = []APIGWv2Deployment{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetDeployment(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	deploymentId := sim.PathParam(r, "deploymentId")
	d, ok := apigwv2Deployments.Get(apigwv2StoreKey(apiId, deploymentId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Deployment identifier specified %s", deploymentId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d)
}

func handleAPIGWv2DeleteDeployment(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	deploymentId := sim.PathParam(r, "deploymentId")
	if !apigwv2Deployments.Delete(apigwv2StoreKey(apiId, deploymentId)) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Deployment identifier specified %s", deploymentId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateAuthorizer(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	if _, ok := apigwv2Apis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	var req struct {
		Name                           string            `json:"Name"`
		AuthorizerType                 string            `json:"AuthorizerType"`
		AuthorizerUri                  string            `json:"AuthorizerUri"`
		AuthorizerCredentialsArn       string            `json:"AuthorizerCredentialsArn"`
		AuthorizerPayloadFormatVersion string            `json:"AuthorizerPayloadFormatVersion"`
		AuthorizerResultTtlInSeconds   int               `json:"AuthorizerResultTtlInSeconds"`
		EnableSimpleResponses          bool              `json:"EnableSimpleResponses"`
		IdentitySource                 []string          `json:"IdentitySource"`
		IdentityValidationExpression   string            `json:"IdentityValidationExpression"`
		JwtConfiguration               *APIGWv2JWTConfig `json:"JwtConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	a := APIGWv2Authorizer{
		AuthorizerId:                   generateUUID()[:10],
		ApiId:                          apiId,
		Name:                           req.Name,
		AuthorizerType:                 req.AuthorizerType,
		AuthorizerUri:                  req.AuthorizerUri,
		AuthorizerCredentialsArn:       req.AuthorizerCredentialsArn,
		AuthorizerPayloadFormatVersion: req.AuthorizerPayloadFormatVersion,
		AuthorizerResultTtlInSeconds:   req.AuthorizerResultTtlInSeconds,
		EnableSimpleResponses:          req.EnableSimpleResponses,
		IdentitySource:                 req.IdentitySource,
		IdentityValidationExpression:   req.IdentityValidationExpression,
		JwtConfiguration:               req.JwtConfiguration,
	}
	apigwv2Authorizers.Put(apigwv2StoreKey(apiId, a.AuthorizerId), a)
	sim.WriteJSON(w, http.StatusCreated, a)
}

func handleAPIGWv2ListAuthorizers(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	var out []APIGWv2Authorizer
	for _, a := range apigwv2Authorizers.List() {
		if a.ApiId == apiId {
			out = append(out, a)
		}
	}
	if out == nil {
		out = []APIGWv2Authorizer{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetAuthorizer(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	authorizerId := sim.PathParam(r, "authorizerId")
	a, ok := apigwv2Authorizers.Get(apigwv2StoreKey(apiId, authorizerId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Authorizer identifier specified %s", authorizerId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleAPIGWv2UpdateAuthorizer(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	authorizerId := sim.PathParam(r, "authorizerId")
	a, ok := apigwv2Authorizers.Get(apigwv2StoreKey(apiId, authorizerId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Authorizer identifier specified %s", authorizerId)
		return
	}
	// PATCH is a partial update: pointer fields distinguish absent from empty.
	var req struct {
		Name                           *string           `json:"Name"`
		AuthorizerType                 *string           `json:"AuthorizerType"`
		AuthorizerUri                  *string           `json:"AuthorizerUri"`
		AuthorizerCredentialsArn       *string           `json:"AuthorizerCredentialsArn"`
		AuthorizerPayloadFormatVersion *string           `json:"AuthorizerPayloadFormatVersion"`
		AuthorizerResultTtlInSeconds   *int              `json:"AuthorizerResultTtlInSeconds"`
		EnableSimpleResponses          *bool             `json:"EnableSimpleResponses"`
		IdentitySource                 []string          `json:"IdentitySource"`
		IdentityValidationExpression   *string           `json:"IdentityValidationExpression"`
		JwtConfiguration               *APIGWv2JWTConfig `json:"JwtConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		a.Name = *req.Name
	}
	if req.AuthorizerType != nil {
		a.AuthorizerType = *req.AuthorizerType
	}
	if req.AuthorizerUri != nil {
		a.AuthorizerUri = *req.AuthorizerUri
	}
	if req.AuthorizerCredentialsArn != nil {
		a.AuthorizerCredentialsArn = *req.AuthorizerCredentialsArn
	}
	if req.AuthorizerPayloadFormatVersion != nil {
		a.AuthorizerPayloadFormatVersion = *req.AuthorizerPayloadFormatVersion
	}
	if req.AuthorizerResultTtlInSeconds != nil {
		a.AuthorizerResultTtlInSeconds = *req.AuthorizerResultTtlInSeconds
	}
	if req.EnableSimpleResponses != nil {
		a.EnableSimpleResponses = *req.EnableSimpleResponses
	}
	if req.IdentitySource != nil {
		a.IdentitySource = req.IdentitySource
	}
	if req.IdentityValidationExpression != nil {
		a.IdentityValidationExpression = *req.IdentityValidationExpression
	}
	if req.JwtConfiguration != nil {
		a.JwtConfiguration = req.JwtConfiguration
	}
	apigwv2Authorizers.Put(apigwv2StoreKey(apiId, authorizerId), a)
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleAPIGWv2DeleteAuthorizer(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	authorizerId := sim.PathParam(r, "authorizerId")
	if !apigwv2Authorizers.Delete(apigwv2StoreKey(apiId, authorizerId)) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Authorizer identifier specified %s", authorizerId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateModel(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	if _, ok := apigwv2Apis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	var req struct {
		Name        string `json:"Name"`
		Description string `json:"Description"`
		Schema      string `json:"Schema"`
		ContentType string `json:"ContentType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	m := APIGWv2Model{
		ModelId:     generateUUID()[:10],
		ApiId:       apiId,
		Name:        req.Name,
		Description: req.Description,
		Schema:      req.Schema,
		ContentType: req.ContentType,
	}
	apigwv2Models.Put(apigwv2StoreKey(apiId, m.ModelId), m)
	sim.WriteJSON(w, http.StatusCreated, m)
}

func handleAPIGWv2ListModels(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	var out []APIGWv2Model
	for _, m := range apigwv2Models.List() {
		if m.ApiId == apiId {
			out = append(out, m)
		}
	}
	if out == nil {
		out = []APIGWv2Model{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetModel(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	modelId := sim.PathParam(r, "modelId")
	m, ok := apigwv2Models.Get(apigwv2StoreKey(apiId, modelId))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Model identifier specified %s", modelId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWv2DeleteModel(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "apiId")
	modelId := sim.PathParam(r, "modelId")
	if !apigwv2Models.Delete(apigwv2StoreKey(apiId, modelId)) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Model identifier specified %s", modelId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateDomainName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainName               string                    `json:"DomainName"`
		DomainNameConfigurations []APIGWv2DomainNameConfig `json:"DomainNameConfigurations"`
		Tags                     map[string]string         `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DomainName == "" {
		AWSError(w, "BadRequestException", "DomainName is required", http.StatusBadRequest)
		return
	}
	if _, ok := apigwv2DomainNames.Get(req.DomainName); ok {
		AWSErrorf(w, "ConflictException", http.StatusConflict,
			"The domain name you provided already exists.")
		return
	}
	cfgs := req.DomainNameConfigurations
	for i := range cfgs {
		if cfgs[i].DomainNameStatus == "" {
			cfgs[i].DomainNameStatus = "AVAILABLE"
		}
		if cfgs[i].ApiGatewayDomainName == "" {
			cfgs[i].ApiGatewayDomainName = fmt.Sprintf("d-%s.execute-api.%s.amazonaws.com", generateUUID()[:10], awsRegion())
		}
	}
	d := APIGWv2DomainName{
		DomainName:                    req.DomainName,
		DomainNameArn:                 fmt.Sprintf("arn:aws:apigateway:%s::/domainnames/%s", awsRegion(), req.DomainName),
		ApiMappingSelectionExpression: "$request.basepath",
		DomainNameConfigurations:      cfgs,
		Tags:                          req.Tags,
	}
	apigwv2DomainNames.Put(d.DomainName, d)
	sim.WriteJSON(w, http.StatusCreated, d)
}

func handleAPIGWv2ListDomainNames(w http.ResponseWriter, r *http.Request) {
	all := apigwv2DomainNames.List()
	if all == nil {
		all = []APIGWv2DomainName{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": all})
}

func handleAPIGWv2GetDomainName(w http.ResponseWriter, r *http.Request) {
	d, ok := apigwv2DomainNames.Get(sim.PathParam(r, "domainName"))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, d)
}

func handleAPIGWv2DeleteDomainName(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	if !apigwv2DomainNames.Delete(domainName) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	for _, m := range apigwv2ApiMappings.List() {
		if m.DomainName == domainName {
			apigwv2ApiMappings.Delete(domainName + "/" + m.ApiMappingId)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateApiMapping(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	if _, ok := apigwv2DomainNames.Get(domainName); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	var req struct {
		ApiId         string `json:"ApiId"`
		ApiMappingKey string `json:"ApiMappingKey"`
		Stage         string `json:"Stage"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := apigwv2Apis.Get(req.ApiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	m := APIGWv2ApiMapping{
		ApiMappingId:  generateUUID()[:10],
		ApiId:         req.ApiId,
		ApiMappingKey: req.ApiMappingKey,
		Stage:         req.Stage,
		DomainName:    domainName,
	}
	apigwv2ApiMappings.Put(domainName+"/"+m.ApiMappingId, m)
	sim.WriteJSON(w, http.StatusCreated, m)
}

func handleAPIGWv2ListApiMappings(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	if _, ok := apigwv2DomainNames.Get(domainName); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	var out []APIGWv2ApiMapping
	for _, m := range apigwv2ApiMappings.List() {
		if m.DomainName == domainName {
			out = append(out, m)
		}
	}
	if out == nil {
		out = []APIGWv2ApiMapping{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetApiMapping(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	apiMappingId := sim.PathParam(r, "apiMappingId")
	m, ok := apigwv2ApiMappings.Get(domainName + "/" + apiMappingId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid API mapping identifier specified %s", apiMappingId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWv2DeleteApiMapping(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	apiMappingId := sim.PathParam(r, "apiMappingId")
	if !apigwv2ApiMappings.Delete(domainName + "/" + apiMappingId) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid API mapping identifier specified %s", apiMappingId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2CreateVpcLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string            `json:"Name"`
		SecurityGroupIds []string          `json:"SecurityGroupIds"`
		SubnetIds        []string          `json:"SubnetIds"`
		Tags             map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	v := APIGWv2VpcLink{
		VpcLinkId:        generateUUID()[:10],
		Name:             req.Name,
		SecurityGroupIds: req.SecurityGroupIds,
		SubnetIds:        req.SubnetIds,
		Tags:             req.Tags,
		CreatedDate:      time.Now().UTC().Format(time.RFC3339),
		VpcLinkStatus:    "AVAILABLE",
		VpcLinkVersion:   "V2",
	}
	apigwv2VpcLinks.Put(v.VpcLinkId, v)
	sim.WriteJSON(w, http.StatusCreated, v)
}

func handleAPIGWv2ListVpcLinks(w http.ResponseWriter, r *http.Request) {
	all := apigwv2VpcLinks.List()
	if all == nil {
		all = []APIGWv2VpcLink{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": all})
}

func handleAPIGWv2GetVpcLink(w http.ResponseWriter, r *http.Request) {
	v, ok := apigwv2VpcLinks.Get(sim.PathParam(r, "vpcLinkId"))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid VPC link identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, v)
}

func handleAPIGWv2DeleteVpcLink(w http.ResponseWriter, r *http.Request) {
	if !apigwv2VpcLinks.Delete(sim.PathParam(r, "vpcLinkId")) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid VPC link identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
