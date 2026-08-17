package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// API Gateway v1 (REST API) — restJson1 protocol, REST path routing
// under /restapis. Surface scoped to the resources / methods /
// integrations / stages / deployments CRUD that
// `terraform-provider-aws::aws_api_gateway_rest_api` exercises.

type APIGWRestApi struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedDate int64  `json:"createdDate"`
	// ApiStatus is how a caller knows the API can be used: a client that
	// creates one waits for AVAILABLE before it deploys to it, and reads an
	// absent status as a state it has never seen. The simulator's REST API is
	// usable as soon as the create returns, so that is the status it reports
	// from creation onward.
	ApiStatus      string            `json:"apiStatus,omitempty"`
	RootResourceId string            `json:"rootResourceId,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

// apigwStatusAvailable is the ApiStatus of a REST API that can serve requests.
const apigwStatusAvailable = "AVAILABLE"

// Inner fields use the `restApiIdRef` tag (or similar non-canonical
// names) instead of `json:"-"` so they survive Store persistence
// round-trips. Real AWS doesn't emit these on per-resource GETs but
// the SDK ignores unknown fields, so the leak is harmless.
type APIGWResource struct {
	Id        string `json:"id"`
	RestApiId string `json:"restApiIdRef,omitempty"`
	ParentId  string `json:"parentId,omitempty"`
	PathPart  string `json:"pathPart,omitempty"`
	Path      string `json:"path"`
}

type APIGWMethod struct {
	HttpMethod        string            `json:"httpMethod"`
	ResourceId        string            `json:"resourceIdRef,omitempty"`
	RestApiId         string            `json:"restApiIdRef,omitempty"`
	AuthorizationType string            `json:"authorizationType,omitempty"`
	ApiKeyRequired    bool              `json:"apiKeyRequired,omitempty"`
	RequestParameters map[string]bool   `json:"requestParameters,omitempty"`
	RequestModels     map[string]string `json:"requestModels,omitempty"`
	MethodResponses   map[string]any    `json:"methodResponses,omitempty"`
	MethodIntegration *APIGWIntegration `json:"methodIntegration,omitempty"`
}

type APIGWIntegration struct {
	HttpMethod            string            `json:"methodRef,omitempty"`
	ResourceId            string            `json:"resourceIdRef,omitempty"`
	RestApiId             string            `json:"restApiIdRef,omitempty"`
	Type                  string            `json:"type"`
	Uri                   string            `json:"uri,omitempty"` // external (operator-supplied): integration target — Lambda ARN, HTTP backend, or VPC link target
	IntegrationHttpMethod string            `json:"httpMethod,omitempty"`
	RequestTemplates      map[string]string `json:"requestTemplates,omitempty"`
	RequestParameters     map[string]string `json:"requestParameters,omitempty"`
	CacheNamespace        string            `json:"cacheNamespace,omitempty"`
	TimeoutInMillis       int               `json:"timeoutInMillis,omitempty"`
	PassthroughBehavior   string            `json:"passthroughBehavior,omitempty"`
	ContentHandling       string            `json:"contentHandling,omitempty"`
}

// APIGWMethodResponse mirrors aws_api_gateway_method_response. Per-
// (method, statusCode) row keyed by `<restApiId>/<resourceId>/<httpMethod>/<statusCode>`.
type APIGWMethodResponse struct {
	StatusCode         string            `json:"statusCode"`
	ResponseModels     map[string]string `json:"responseModels,omitempty"`
	ResponseParameters map[string]bool   `json:"responseParameters,omitempty"`
}

// APIGWIntegrationResponse mirrors aws_api_gateway_integration_response.
// Per-(integration, statusCode) row keyed the same as APIGWMethodResponse.
type APIGWIntegrationResponse struct {
	StatusCode         string            `json:"statusCode"`
	SelectionPattern   string            `json:"selectionPattern,omitempty"`
	ResponseTemplates  map[string]string `json:"responseTemplates,omitempty"`
	ResponseParameters map[string]string `json:"responseParameters,omitempty"`
	ContentHandling    string            `json:"contentHandling,omitempty"`
}

type APIGWDeployment struct {
	Id          string `json:"id"`
	RestApiId   string `json:"restApiIdRef,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedDate int64  `json:"createdDate"`
}

type APIGWStage struct {
	StageName    string `json:"stageName"`
	RestApiId    string `json:"restApiIdRef,omitempty"`
	DeploymentId string `json:"deploymentId"`
	CreatedDate  int64  `json:"createdDate"`
}

// APIGWApiKey mirrors aws_api_gateway_api_key. Keyed by its own id.
type APIGWApiKey struct {
	Id              string            `json:"id"`
	Value           string            `json:"value,omitempty"`
	Name            string            `json:"name,omitempty"`
	CustomerId      string            `json:"customerId,omitempty"`
	Description     string            `json:"description,omitempty"`
	Enabled         bool              `json:"enabled"`
	CreatedDate     int64             `json:"createdDate"`
	LastUpdatedDate int64             `json:"lastUpdatedDate"`
	StageKeys       []string          `json:"stageKeys,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// APIGWThrottleSettings / APIGWQuotaSettings / APIGWApiStage mirror the
// nested usage-plan shapes the SDK serializes.
type APIGWThrottleSettings struct {
	BurstLimit int     `json:"burstLimit,omitempty"`
	RateLimit  float64 `json:"rateLimit,omitempty"`
}

type APIGWQuotaSettings struct {
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Period string `json:"period,omitempty"`
}

type APIGWApiStage struct {
	ApiId string `json:"apiId,omitempty"`
	Stage string `json:"stage,omitempty"`
}

// APIGWUsagePlan mirrors aws_api_gateway_usage_plan. Keyed by its own id.
type APIGWUsagePlan struct {
	Id          string                 `json:"id"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	ApiStages   []APIGWApiStage        `json:"apiStages,omitempty"`
	Throttle    *APIGWThrottleSettings `json:"throttle,omitempty"`
	Quota       *APIGWQuotaSettings    `json:"quota,omitempty"`
	ProductCode string                 `json:"productCode,omitempty"`
	Tags        map[string]string      `json:"tags,omitempty"`
}

// APIGWUsagePlanKey mirrors aws_api_gateway_usage_plan_key. Keyed by
// `<usagePlanId>/<keyId>`; the parent ref rides `usagePlanIdRef`.
type APIGWUsagePlanKey struct {
	Id          string `json:"id"`
	Type        string `json:"type,omitempty"`
	Value       string `json:"value,omitempty"`
	Name        string `json:"name,omitempty"`
	UsagePlanId string `json:"usagePlanIdRef,omitempty"`
}

// APIGWModel mirrors aws_api_gateway_model. Keyed by `<restApiId>/<name>`.
type APIGWModel struct {
	Id          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Schema      string `json:"schema,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	RestApiId   string `json:"restApiIdRef,omitempty"`
}

// APIGWRequestValidator mirrors aws_api_gateway_request_validator. Keyed
// by `<restApiId>/<id>`.
type APIGWRequestValidator struct {
	Id                        string `json:"id"`
	Name                      string `json:"name,omitempty"`
	ValidateRequestBody       bool   `json:"validateRequestBody,omitempty"`
	ValidateRequestParameters bool   `json:"validateRequestParameters,omitempty"`
	RestApiId                 string `json:"restApiIdRef,omitempty"`
}

// APIGWAuthorizer mirrors aws_api_gateway_authorizer. Keyed by
// `<restApiId>/<id>`.
type APIGWAuthorizer struct {
	Id                           string   `json:"id"`
	Name                         string   `json:"name,omitempty"`
	Type                         string   `json:"type,omitempty"`
	ProviderARNs                 []string `json:"providerARNs,omitempty"`
	AuthType                     string   `json:"authType,omitempty"`
	AuthorizerUri                string   `json:"authorizerUri,omitempty"`
	AuthorizerCredentials        string   `json:"authorizerCredentials,omitempty"`
	IdentitySource               string   `json:"identitySource,omitempty"`
	IdentityValidationExpression string   `json:"identityValidationExpression,omitempty"`
	AuthorizerResultTtlInSeconds *int     `json:"authorizerResultTtlInSeconds,omitempty"`
	RestApiId                    string   `json:"restApiIdRef,omitempty"`
}

var (
	apigwRestApis             sim.Store[APIGWRestApi]
	apigwResources            sim.Store[APIGWResource]
	apigwMethods              sim.Store[APIGWMethod]
	apigwIntegrations         sim.Store[APIGWIntegration]
	apigwDeployments          sim.Store[APIGWDeployment]
	apigwStages               sim.Store[APIGWStage]
	apigwMethodResponses      sim.Store[APIGWMethodResponse]
	apigwIntegrationResponses sim.Store[APIGWIntegrationResponse]
	apigwApiKeys              sim.Store[APIGWApiKey]
	apigwUsagePlans           sim.Store[APIGWUsagePlan]
	apigwUsagePlanKeys        sim.Store[APIGWUsagePlanKey]
	apigwModels               sim.Store[APIGWModel]
	apigwRequestValidators    sim.Store[APIGWRequestValidator]
	apigwAuthorizers          sim.Store[APIGWAuthorizer]
)

func registerAPIGateway(srv *sim.Server) {
	apigwRestApis = sim.MakeStore[APIGWRestApi](srv.DB(), "apigw_restapis")
	apigwResources = sim.MakeStore[APIGWResource](srv.DB(), "apigw_resources")
	apigwMethods = sim.MakeStore[APIGWMethod](srv.DB(), "apigw_methods")
	apigwIntegrations = sim.MakeStore[APIGWIntegration](srv.DB(), "apigw_integrations")
	apigwDeployments = sim.MakeStore[APIGWDeployment](srv.DB(), "apigw_deployments")
	apigwStages = sim.MakeStore[APIGWStage](srv.DB(), "apigw_stages")
	apigwMethodResponses = sim.MakeStore[APIGWMethodResponse](srv.DB(), "apigw_method_responses")
	apigwIntegrationResponses = sim.MakeStore[APIGWIntegrationResponse](srv.DB(), "apigw_integration_responses")
	apigwApiKeys = sim.MakeStore[APIGWApiKey](srv.DB(), "apigw_apikeys")
	apigwUsagePlans = sim.MakeStore[APIGWUsagePlan](srv.DB(), "apigw_usageplans")
	apigwUsagePlanKeys = sim.MakeStore[APIGWUsagePlanKey](srv.DB(), "apigw_usageplan_keys")
	apigwModels = sim.MakeStore[APIGWModel](srv.DB(), "apigw_models")
	apigwRequestValidators = sim.MakeStore[APIGWRequestValidator](srv.DB(), "apigw_request_validators")
	apigwAuthorizers = sim.MakeStore[APIGWAuthorizer](srv.DB(), "apigw_authorizers")

	mux := srv
	apiResource := cloudTrailRESTResource("AWS::ApiGateway::RestApi", "restApiId")
	mux.HandleFunc("POST /restapis", cloudTrailRecordedREST("CreateRestApi", "apigateway.amazonaws.com", nil, handleAPIGWCreateRestApi))
	mux.HandleFunc("GET /restapis", cloudTrailRecordedREST("GetRestApis", "apigateway.amazonaws.com", nil, handleAPIGWListRestApis))
	mux.HandleFunc("GET /restapis/{restApiId}", cloudTrailRecordedREST("GetRestApi", "apigateway.amazonaws.com", apiResource, handleAPIGWGetRestApi))
	mux.HandleFunc("DELETE /restapis/{restApiId}", cloudTrailRecordedREST("DeleteRestApi", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteRestApi))
	mux.HandleFunc("POST /restapis/{restApiId}/resources/{parentId}", cloudTrailRecordedREST("CreateResource", "apigateway.amazonaws.com", apiResource, handleAPIGWCreateResource))
	mux.HandleFunc("GET /restapis/{restApiId}/resources", cloudTrailRecordedREST("GetResources", "apigateway.amazonaws.com", apiResource, handleAPIGWListResources))
	mux.HandleFunc("GET /restapis/{restApiId}/resources/{resourceId}", cloudTrailRecordedREST("GetResource", "apigateway.amazonaws.com", apiResource, handleAPIGWGetResource))
	mux.HandleFunc("DELETE /restapis/{restApiId}/resources/{resourceId}", cloudTrailRecordedREST("DeleteResource", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteResource))
	mux.HandleFunc("PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}", cloudTrailRecordedREST("PutMethod", "apigateway.amazonaws.com", apiResource, handleAPIGWPutMethod))
	mux.HandleFunc("GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}", cloudTrailRecordedREST("GetMethod", "apigateway.amazonaws.com", apiResource, handleAPIGWGetMethod))
	mux.HandleFunc("DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}", cloudTrailRecordedREST("DeleteMethod", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteMethod))
	mux.HandleFunc("PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration", cloudTrailRecordedREST("PutIntegration", "apigateway.amazonaws.com", apiResource, handleAPIGWPutIntegration))
	mux.HandleFunc("GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration", cloudTrailRecordedREST("GetIntegration", "apigateway.amazonaws.com", apiResource, handleAPIGWGetIntegration))
	mux.HandleFunc("DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration", cloudTrailRecordedREST("DeleteIntegration", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteIntegration))
	mux.HandleFunc("POST /restapis/{restApiId}/deployments", cloudTrailRecordedREST("CreateDeployment", "apigateway.amazonaws.com", apiResource, handleAPIGWCreateDeployment))
	mux.HandleFunc("GET /restapis/{restApiId}/deployments", cloudTrailRecordedREST("GetDeployments", "apigateway.amazonaws.com", apiResource, handleAPIGWListDeployments))
	mux.HandleFunc("GET /restapis/{restApiId}/deployments/{deploymentId}", cloudTrailRecordedREST("GetDeployment", "apigateway.amazonaws.com", apiResource, handleAPIGWGetDeployment))
	mux.HandleFunc("DELETE /restapis/{restApiId}/deployments/{deploymentId}", cloudTrailRecordedREST("DeleteDeployment", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteDeployment))
	mux.HandleFunc("POST /restapis/{restApiId}/stages", cloudTrailRecordedREST("CreateStage", "apigateway.amazonaws.com", apiResource, handleAPIGWCreateStage))
	mux.HandleFunc("GET /restapis/{restApiId}/stages", cloudTrailRecordedREST("GetStages", "apigateway.amazonaws.com", apiResource, handleAPIGWListStages))
	mux.HandleFunc("GET /restapis/{restApiId}/stages/{stageName}", cloudTrailRecordedREST("GetStage", "apigateway.amazonaws.com", apiResource, handleAPIGWGetStage))
	mux.HandleFunc("DELETE /restapis/{restApiId}/stages/{stageName}", cloudTrailRecordedREST("DeleteStage", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteStage))

	// Method + integration response CRUD per status code.
	// terraform-provider-aws's `aws_api_gateway_method_response` +
	// `aws_api_gateway_integration_response` resources read/write
	// these on every plan; without them the canonical method-create
	// flow never gets past response wiring.
	mux.HandleFunc("PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}", cloudTrailRecordedREST("PutMethodResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWPutMethodResponse))
	mux.HandleFunc("GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}", cloudTrailRecordedREST("GetMethodResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWGetMethodResponse))
	mux.HandleFunc("DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/responses/{statusCode}", cloudTrailRecordedREST("DeleteMethodResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteMethodResponse))
	mux.HandleFunc("PUT /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}", cloudTrailRecordedREST("PutIntegrationResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWPutIntegrationResponse))
	mux.HandleFunc("GET /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}", cloudTrailRecordedREST("GetIntegrationResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWGetIntegrationResponse))
	mux.HandleFunc("DELETE /restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}/integration/responses/{statusCode}", cloudTrailRecordedREST("DeleteIntegrationResponse", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteIntegrationResponse))

	// API keys — top-level resources keyed by their own id.
	keyResource := cloudTrailRESTResource("AWS::ApiGateway::ApiKey", "apiKey")
	mux.HandleFunc("POST /apikeys", cloudTrailRecordedREST("CreateApiKey", "apigateway.amazonaws.com", nil, handleAPIGWCreateApiKey))
	mux.HandleFunc("GET /apikeys", cloudTrailRecordedREST("GetApiKeys", "apigateway.amazonaws.com", nil, handleAPIGWListApiKeys))
	mux.HandleFunc("GET /apikeys/{apiKey}", cloudTrailRecordedREST("GetApiKey", "apigateway.amazonaws.com", keyResource, handleAPIGWGetApiKey))
	mux.HandleFunc("PATCH /apikeys/{apiKey}", cloudTrailRecordedREST("UpdateApiKey", "apigateway.amazonaws.com", keyResource, handleAPIGWUpdateApiKey))
	mux.HandleFunc("DELETE /apikeys/{apiKey}", cloudTrailRecordedREST("DeleteApiKey", "apigateway.amazonaws.com", keyResource, handleAPIGWDeleteApiKey))

	// Usage plans + usage-plan keys.
	upResource := cloudTrailRESTResource("AWS::ApiGateway::UsagePlan", "usagePlanId")
	mux.HandleFunc("POST /usageplans", cloudTrailRecordedREST("CreateUsagePlan", "apigateway.amazonaws.com", nil, handleAPIGWCreateUsagePlan))
	mux.HandleFunc("GET /usageplans", cloudTrailRecordedREST("GetUsagePlans", "apigateway.amazonaws.com", nil, handleAPIGWListUsagePlans))
	mux.HandleFunc("GET /usageplans/{usagePlanId}", cloudTrailRecordedREST("GetUsagePlan", "apigateway.amazonaws.com", upResource, handleAPIGWGetUsagePlan))
	mux.HandleFunc("PATCH /usageplans/{usagePlanId}", cloudTrailRecordedREST("UpdateUsagePlan", "apigateway.amazonaws.com", upResource, handleAPIGWUpdateUsagePlan))
	mux.HandleFunc("DELETE /usageplans/{usagePlanId}", cloudTrailRecordedREST("DeleteUsagePlan", "apigateway.amazonaws.com", upResource, handleAPIGWDeleteUsagePlan))
	mux.HandleFunc("POST /usageplans/{usagePlanId}/keys", cloudTrailRecordedREST("CreateUsagePlanKey", "apigateway.amazonaws.com", upResource, handleAPIGWCreateUsagePlanKey))
	mux.HandleFunc("GET /usageplans/{usagePlanId}/keys", cloudTrailRecordedREST("GetUsagePlanKeys", "apigateway.amazonaws.com", upResource, handleAPIGWListUsagePlanKeys))
	mux.HandleFunc("GET /usageplans/{usagePlanId}/keys/{keyId}", cloudTrailRecordedREST("GetUsagePlanKey", "apigateway.amazonaws.com", upResource, handleAPIGWGetUsagePlanKey))
	mux.HandleFunc("DELETE /usageplans/{usagePlanId}/keys/{keyId}", cloudTrailRecordedREST("DeleteUsagePlanKey", "apigateway.amazonaws.com", upResource, handleAPIGWDeleteUsagePlanKey))

	// Models, request validators, authorizers — all scoped to a REST API.
	mux.HandleFunc("POST /restapis/{restApiId}/models", cloudTrailRecordedREST("CreateModel", "apigateway.amazonaws.com", apiResource, handleAPIGWCreateModel))
	mux.HandleFunc("GET /restapis/{restApiId}/models", cloudTrailRecordedREST("GetModels", "apigateway.amazonaws.com", apiResource, handleAPIGWListModels))
	mux.HandleFunc("GET /restapis/{restApiId}/models/{modelName}", cloudTrailRecordedREST("GetModel", "apigateway.amazonaws.com", apiResource, handleAPIGWGetModel))
	mux.HandleFunc("DELETE /restapis/{restApiId}/models/{modelName}", cloudTrailRecordedREST("DeleteModel", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteModel))
	mux.HandleFunc("POST /restapis/{restApiId}/requestvalidators", cloudTrailRecordedREST("CreateRequestValidator", "apigateway.amazonaws.com", apiResource, handleAPIGWCreateRequestValidator))
	mux.HandleFunc("GET /restapis/{restApiId}/requestvalidators", cloudTrailRecordedREST("GetRequestValidators", "apigateway.amazonaws.com", apiResource, handleAPIGWListRequestValidators))
	mux.HandleFunc("DELETE /restapis/{restApiId}/requestvalidators/{requestValidatorId}", cloudTrailRecordedREST("DeleteRequestValidator", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteRequestValidator))
	mux.HandleFunc("POST /restapis/{restApiId}/authorizers", cloudTrailRecordedREST("CreateAuthorizer", "apigateway.amazonaws.com", apiResource, handleAPIGWCreateAuthorizer))
	mux.HandleFunc("GET /restapis/{restApiId}/authorizers", cloudTrailRecordedREST("GetAuthorizers", "apigateway.amazonaws.com", apiResource, handleAPIGWListAuthorizers))
	mux.HandleFunc("GET /restapis/{restApiId}/authorizers/{authorizerId}", cloudTrailRecordedREST("GetAuthorizer", "apigateway.amazonaws.com", apiResource, handleAPIGWGetAuthorizer))
	mux.HandleFunc("DELETE /restapis/{restApiId}/authorizers/{authorizerId}", cloudTrailRecordedREST("DeleteAuthorizer", "apigateway.amazonaws.com", apiResource, handleAPIGWDeleteAuthorizer))

	registerAPIGatewayExtras(srv)
	registerAPIGatewayComplete(srv)
}

func handleAPIGWCreateRestApi(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Tags        map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	api := APIGWRestApi{
		Id:          generateUUID()[:10],
		Name:        req.Name,
		Description: req.Description,
		CreatedDate: time.Now().Unix(),
		ApiStatus:   apigwStatusAvailable,
		Tags:        req.Tags,
	}
	// Real API Gateway auto-creates the root "/" resource on Create and
	// surfaces its id as rootResourceId.
	root := APIGWResource{
		Id:        generateUUID()[:10],
		RestApiId: api.Id,
		Path:      "/",
	}
	api.RootResourceId = root.Id
	apigwRestApis.Put(api.Id, api)
	apigwResources.Put(api.Id+"/"+root.Id, root)
	sim.WriteJSON(w, http.StatusCreated, api)
}

func handleAPIGWListRestApis(w http.ResponseWriter, r *http.Request) {
	all := apigwRestApis.List()
	if all == nil {
		all = []APIGWRestApi{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": all})
}

func handleAPIGWGetRestApi(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "restApiId")
	api, ok := apigwRestApis.Get(id)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, api)
}

func handleAPIGWDeleteRestApi(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "restApiId")
	if !apigwRestApis.Delete(id) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	for _, res := range apigwResources.List() {
		if res.RestApiId == id {
			apigwResources.Delete(id + "/" + res.Id)
		}
	}
	for _, m := range apigwMethods.List() {
		if m.RestApiId == id {
			apigwMethods.Delete(apigwMethodKey(id, m.ResourceId, m.HttpMethod))
		}
	}
	for _, in := range apigwIntegrations.List() {
		if in.RestApiId == id {
			apigwIntegrations.Delete(apigwMethodKey(id, in.ResourceId, in.HttpMethod))
		}
	}
	for _, d := range apigwDeployments.List() {
		if d.RestApiId == id {
			apigwDeployments.Delete(apigwDeploymentKey(id, d.Id))
		}
	}
	for _, s := range apigwStages.List() {
		if s.RestApiId == id {
			apigwStages.Delete(apigwStageKey(id, s.StageName))
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateResource(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	parentId := sim.PathParam(r, "parentId")
	if _, ok := apigwRestApis.Get(apiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var req struct {
		PathPart string `json:"pathPart"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	parent, ok := apigwResources.Get(apiId + "/" + parentId)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Resource identifier specified")
		return
	}
	res := APIGWResource{
		Id:        generateUUID()[:10],
		RestApiId: apiId,
		ParentId:  parentId,
		PathPart:  req.PathPart,
		Path:      apigwChildPath(parent.Path, req.PathPart),
	}
	apigwResources.Put(apiId+"/"+res.Id, res)
	sim.WriteJSON(w, http.StatusCreated, res)
}

func handleAPIGWListResources(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	all := apigwResources.List()
	var out []APIGWResource
	for _, res := range all {
		if res.RestApiId == apiId {
			out = append(out, res)
		}
	}
	if out == nil {
		out = []APIGWResource{}
	}
	_ = all
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetResource(w http.ResponseWriter, r *http.Request) {
	res, ok := apigwResources.Get(sim.PathParam(r, "restApiId") + "/" + sim.PathParam(r, "resourceId"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Resource identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, res)
}

func handleAPIGWDeleteResource(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	if !apigwResources.Delete(apiId + "/" + resourceId) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Resource identifier specified")
		return
	}
	for _, m := range apigwMethods.List() {
		if m.RestApiId == apiId && m.ResourceId == resourceId {
			apigwMethods.Delete(apigwMethodKey(apiId, resourceId, m.HttpMethod))
		}
	}
	for _, in := range apigwIntegrations.List() {
		if in.RestApiId == apiId && in.ResourceId == resourceId {
			apigwIntegrations.Delete(apigwMethodKey(apiId, resourceId, in.HttpMethod))
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWPutMethod(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	var req struct {
		AuthorizationType string            `json:"authorizationType"`
		ApiKeyRequired    bool              `json:"apiKeyRequired"`
		RequestParameters map[string]bool   `json:"requestParameters"`
		RequestModels     map[string]string `json:"requestModels"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := apigwResources.Get(apiId + "/" + resourceId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Resource identifier specified")
		return
	}
	m := APIGWMethod{
		HttpMethod:        httpMethod,
		ResourceId:        resourceId,
		RestApiId:         apiId,
		AuthorizationType: req.AuthorizationType,
		ApiKeyRequired:    req.ApiKeyRequired,
		RequestParameters: req.RequestParameters,
		RequestModels:     req.RequestModels,
	}
	apigwMethods.Put(apigwMethodKey(apiId, resourceId, httpMethod), m)
	sim.WriteJSON(w, http.StatusCreated, m)
}

func handleAPIGWGetMethod(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	m, ok := apigwMethods.Get(apigwMethodKey(apiId, resourceId, httpMethod))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Method identifier specified")
		return
	}
	if in, ok := apigwIntegrations.Get(apigwMethodKey(apiId, resourceId, httpMethod)); ok {
		m.MethodIntegration = &in
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWDeleteMethod(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	if !apigwMethods.Delete(apigwMethodKey(apiId, resourceId, httpMethod)) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Method identifier specified")
		return
	}
	apigwIntegrations.Delete(apigwMethodKey(apiId, resourceId, httpMethod))
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWPutIntegration(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	var req struct {
		Type                  string            `json:"type"`
		Uri                   string            `json:"uri"`
		IntegrationHttpMethod string            `json:"httpMethod"`
		RequestTemplates      map[string]string `json:"requestTemplates"`
		RequestParameters     map[string]string `json:"requestParameters"`
		CacheNamespace        string            `json:"cacheNamespace"`
		TimeoutInMillis       int               `json:"timeoutInMillis"`
		PassthroughBehavior   string            `json:"passthroughBehavior"`
		ContentHandling       string            `json:"contentHandling"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	in := APIGWIntegration{
		HttpMethod:            httpMethod,
		ResourceId:            resourceId,
		RestApiId:             apiId,
		Type:                  req.Type,
		Uri:                   req.Uri,
		IntegrationHttpMethod: req.IntegrationHttpMethod,
		RequestTemplates:      req.RequestTemplates,
		RequestParameters:     req.RequestParameters,
		CacheNamespace:        req.CacheNamespace,
		TimeoutInMillis:       req.TimeoutInMillis,
		PassthroughBehavior:   req.PassthroughBehavior,
		ContentHandling:       req.ContentHandling,
	}
	apigwIntegrations.Put(apigwMethodKey(apiId, resourceId, httpMethod), in)
	sim.WriteJSON(w, http.StatusCreated, in)
}

func handleAPIGWGetIntegration(w http.ResponseWriter, r *http.Request) {
	in, ok := apigwIntegrations.Get(apigwMethodKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "resourceId"), sim.PathParam(r, "httpMethod")))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Integration identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, in)
}

func handleAPIGWDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	if !apigwIntegrations.Delete(apigwMethodKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "resourceId"), sim.PathParam(r, "httpMethod"))) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Integration identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateDeployment(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	var req struct {
		Description string `json:"description"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	d := APIGWDeployment{
		Id:          generateUUID()[:10],
		RestApiId:   apiId,
		Description: req.Description,
		CreatedDate: time.Now().Unix(),
	}
	apigwDeployments.Put(apiId+"/"+d.Id, d)
	sim.WriteJSON(w, http.StatusCreated, d)
}

func handleAPIGWListDeployments(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(apiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var out []APIGWDeployment
	for _, d := range apigwDeployments.List() {
		if d.RestApiId == apiId {
			out = append(out, d)
		}
	}
	if out == nil {
		out = []APIGWDeployment{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetDeployment(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	deploymentId := sim.PathParam(r, "deploymentId")
	d, ok := apigwDeployments.Get(apigwDeploymentKey(apiId, deploymentId))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Deployment identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, d)
}

func handleAPIGWDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	deploymentId := sim.PathParam(r, "deploymentId")
	if !apigwDeployments.Delete(apigwDeploymentKey(apiId, deploymentId)) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Deployment identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	var req struct {
		StageName    string `json:"stageName"`
		DeploymentId string `json:"deploymentId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	s := APIGWStage{
		StageName:    req.StageName,
		RestApiId:    apiId,
		DeploymentId: req.DeploymentId,
		CreatedDate:  time.Now().Unix(),
	}
	apigwStages.Put(apiId+"/"+s.StageName, s)
	sim.WriteJSON(w, http.StatusCreated, s)
}

func handleAPIGWListStages(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(apiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var out []APIGWStage
	for _, s := range apigwStages.List() {
		if s.RestApiId == apiId {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []APIGWStage{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	stageName := sim.PathParam(r, "stageName")
	s, ok := apigwStages.Get(apigwStageKey(apiId, stageName))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Stage identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleAPIGWDeleteStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	stageName := sim.PathParam(r, "stageName")
	if !apigwStages.Delete(apigwStageKey(apiId, stageName)) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Stage identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func apigwMethodKey(restApiId, resourceId, httpMethod string) string {
	return restApiId + "/" + resourceId + "/" + httpMethod
}

func apigwDeploymentKey(restApiId, deploymentId string) string {
	return restApiId + "/" + deploymentId
}

func apigwStageKey(restApiId, stageName string) string {
	return restApiId + "/" + stageName
}

func apigwChildPath(parentPath, pathPart string) string {
	part := strings.Trim(pathPart, "/")
	if parentPath == "/" {
		return "/" + part
	}
	return strings.TrimRight(parentPath, "/") + "/" + part
}

func apigwMethodResponseKey(restApiId, resourceId, httpMethod, statusCode string) string {
	return restApiId + "/" + resourceId + "/" + httpMethod + "/" + statusCode
}

func handleAPIGWPutMethodResponse(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	statusCode := sim.PathParam(r, "statusCode")
	var req APIGWMethodResponse
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.WriteJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid request body: " + err.Error()})
		return
	}
	req.StatusCode = statusCode
	key := apigwMethodResponseKey(restApiId, resourceId, httpMethod, statusCode)
	apigwMethodResponses.Put(key, req)
	sim.WriteJSON(w, http.StatusCreated, req)
}

func handleAPIGWGetMethodResponse(w http.ResponseWriter, r *http.Request) {
	key := apigwMethodResponseKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "resourceId"), sim.PathParam(r, "httpMethod"), sim.PathParam(r, "statusCode"))
	mr, ok := apigwMethodResponses.Get(key)
	if !ok {
		sim.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "method response not found"})
		return
	}
	sim.WriteJSON(w, http.StatusOK, mr)
}

func handleAPIGWDeleteMethodResponse(w http.ResponseWriter, r *http.Request) {
	key := apigwMethodResponseKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "resourceId"), sim.PathParam(r, "httpMethod"), sim.PathParam(r, "statusCode"))
	if !apigwMethodResponses.Delete(key) {
		sim.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "method response not found"})
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWPutIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	resourceId := sim.PathParam(r, "resourceId")
	httpMethod := sim.PathParam(r, "httpMethod")
	statusCode := sim.PathParam(r, "statusCode")
	var req APIGWIntegrationResponse
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.WriteJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid request body: " + err.Error()})
		return
	}
	req.StatusCode = statusCode
	key := apigwMethodResponseKey(restApiId, resourceId, httpMethod, statusCode)
	apigwIntegrationResponses.Put(key, req)
	sim.WriteJSON(w, http.StatusCreated, req)
}

func handleAPIGWGetIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	key := apigwMethodResponseKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "resourceId"), sim.PathParam(r, "httpMethod"), sim.PathParam(r, "statusCode"))
	ir, ok := apigwIntegrationResponses.Get(key)
	if !ok {
		sim.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "integration response not found"})
		return
	}
	sim.WriteJSON(w, http.StatusOK, ir)
}

func handleAPIGWDeleteIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	key := apigwMethodResponseKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "resourceId"), sim.PathParam(r, "httpMethod"), sim.PathParam(r, "statusCode"))
	if !apigwIntegrationResponses.Delete(key) {
		sim.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "integration response not found"})
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// apigwPatchOp is one JSON-Patch-style operation an API Gateway PATCH
// (Update*) request carries. Only "replace"/"add" on a "/<field>" path
// is honored — the shape the canonical SDK + CLI update flows produce.
type apigwPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
	From  string `json:"from"`
}

func handleAPIGWCreateApiKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Enabled     bool   `json:"enabled"`
		Value       string `json:"value"`
		CustomerId  string `json:"customerId"`
		StageKeys   []struct {
			RestApiId string `json:"restApiId"`
			StageName string `json:"stageName"`
		} `json:"stageKeys"`
		Tags map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	value := req.Value
	if value == "" {
		value = generateUUID() + generateUUID()[:8]
	}
	var stageKeys []string
	for _, sk := range req.StageKeys {
		stageKeys = append(stageKeys, sk.RestApiId+"/"+sk.StageName)
	}
	key := APIGWApiKey{
		Id:              generateUUID()[:10],
		Value:           value,
		Name:            req.Name,
		Description:     req.Description,
		Enabled:         req.Enabled,
		CustomerId:      req.CustomerId,
		CreatedDate:     now,
		LastUpdatedDate: now,
		StageKeys:       stageKeys,
		Tags:            req.Tags,
	}
	apigwApiKeys.Put(key.Id, key)
	sim.WriteJSON(w, http.StatusCreated, key)
}

func handleAPIGWListApiKeys(w http.ResponseWriter, r *http.Request) {
	all := apigwApiKeys.List()
	if all == nil {
		all = []APIGWApiKey{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": all})
}

func handleAPIGWGetApiKey(w http.ResponseWriter, r *http.Request) {
	key, ok := apigwApiKeys.Get(sim.PathParam(r, "apiKey"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API Key identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, key)
}

func handleAPIGWUpdateApiKey(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "apiKey")
	key, ok := apigwApiKeys.Get(id)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API Key identifier specified")
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
		switch strings.TrimPrefix(op.Path, "/") {
		case "name":
			key.Name = op.Value
		case "description":
			key.Description = op.Value
		case "customerId":
			key.CustomerId = op.Value
		case "enabled":
			key.Enabled = op.Value == "true"
		}
	}
	key.LastUpdatedDate = time.Now().Unix()
	apigwApiKeys.Put(id, key)
	sim.WriteJSON(w, http.StatusOK, key)
}

func handleAPIGWDeleteApiKey(w http.ResponseWriter, r *http.Request) {
	if !apigwApiKeys.Delete(sim.PathParam(r, "apiKey")) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API Key identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateUsagePlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		ApiStages   []APIGWApiStage        `json:"apiStages"`
		Throttle    *APIGWThrottleSettings `json:"throttle"`
		Quota       *APIGWQuotaSettings    `json:"quota"`
		Tags        map[string]string      `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	up := APIGWUsagePlan{
		Id:          generateUUID()[:10],
		Name:        req.Name,
		Description: req.Description,
		ApiStages:   req.ApiStages,
		Throttle:    req.Throttle,
		Quota:       req.Quota,
		Tags:        req.Tags,
	}
	apigwUsagePlans.Put(up.Id, up)
	sim.WriteJSON(w, http.StatusCreated, up)
}

func handleAPIGWListUsagePlans(w http.ResponseWriter, r *http.Request) {
	all := apigwUsagePlans.List()
	if all == nil {
		all = []APIGWUsagePlan{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": all})
}

func handleAPIGWGetUsagePlan(w http.ResponseWriter, r *http.Request) {
	up, ok := apigwUsagePlans.Get(sim.PathParam(r, "usagePlanId"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan ID specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, up)
}

func handleAPIGWUpdateUsagePlan(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "usagePlanId")
	up, ok := apigwUsagePlans.Get(id)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan ID specified")
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
		switch strings.TrimPrefix(op.Path, "/") {
		case "name":
			up.Name = op.Value
		case "description":
			up.Description = op.Value
		case "productCode":
			up.ProductCode = op.Value
		case "throttle/rateLimit":
			if up.Throttle == nil {
				up.Throttle = &APIGWThrottleSettings{}
			}
			if f, err := strconv.ParseFloat(op.Value, 64); err == nil {
				up.Throttle.RateLimit = f
			}
		case "throttle/burstLimit":
			if up.Throttle == nil {
				up.Throttle = &APIGWThrottleSettings{}
			}
			if n, err := strconv.Atoi(op.Value); err == nil {
				up.Throttle.BurstLimit = n
			}
		case "quota/limit":
			if up.Quota == nil {
				up.Quota = &APIGWQuotaSettings{}
			}
			if n, err := strconv.Atoi(op.Value); err == nil {
				up.Quota.Limit = n
			}
		}
	}
	apigwUsagePlans.Put(id, up)
	sim.WriteJSON(w, http.StatusOK, up)
}

func handleAPIGWDeleteUsagePlan(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "usagePlanId")
	if !apigwUsagePlans.Delete(id) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan ID specified")
		return
	}
	for _, k := range apigwUsagePlanKeys.List() {
		if k.UsagePlanId == id {
			apigwUsagePlanKeys.Delete(id + "/" + k.Id)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateUsagePlanKey(w http.ResponseWriter, r *http.Request) {
	usagePlanId := sim.PathParam(r, "usagePlanId")
	if _, ok := apigwUsagePlans.Get(usagePlanId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan ID specified")
		return
	}
	var req struct {
		KeyId   string `json:"keyId"`
		KeyType string `json:"keyType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	// The usage-plan key surfaces the referenced API key's id/name/value.
	name := ""
	value := ""
	if ak, ok := apigwApiKeys.Get(req.KeyId); ok {
		name = ak.Name
		value = ak.Value
	}
	k := APIGWUsagePlanKey{
		Id:          req.KeyId,
		Type:        req.KeyType,
		Value:       value,
		Name:        name,
		UsagePlanId: usagePlanId,
	}
	apigwUsagePlanKeys.Put(usagePlanId+"/"+k.Id, k)
	sim.WriteJSON(w, http.StatusCreated, k)
}

func handleAPIGWListUsagePlanKeys(w http.ResponseWriter, r *http.Request) {
	usagePlanId := sim.PathParam(r, "usagePlanId")
	if _, ok := apigwUsagePlans.Get(usagePlanId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan ID specified")
		return
	}
	var out []APIGWUsagePlanKey
	for _, k := range apigwUsagePlanKeys.List() {
		if k.UsagePlanId == usagePlanId {
			out = append(out, k)
		}
	}
	if out == nil {
		out = []APIGWUsagePlanKey{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetUsagePlanKey(w http.ResponseWriter, r *http.Request) {
	usagePlanId := sim.PathParam(r, "usagePlanId")
	keyId := sim.PathParam(r, "keyId")
	k, ok := apigwUsagePlanKeys.Get(usagePlanId + "/" + keyId)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan Key identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, k)
}

func handleAPIGWDeleteUsagePlanKey(w http.ResponseWriter, r *http.Request) {
	usagePlanId := sim.PathParam(r, "usagePlanId")
	keyId := sim.PathParam(r, "keyId")
	if !apigwUsagePlanKeys.Delete(usagePlanId + "/" + keyId) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Usage Plan Key identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateModel(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(restApiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Schema      string `json:"schema"`
		ContentType string `json:"contentType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	m := APIGWModel{
		Id:          generateUUID()[:10],
		Name:        req.Name,
		Description: req.Description,
		Schema:      req.Schema,
		ContentType: req.ContentType,
		RestApiId:   restApiId,
	}
	apigwModels.Put(restApiId+"/"+m.Name, m)
	sim.WriteJSON(w, http.StatusCreated, m)
}

func handleAPIGWListModels(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(restApiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var out []APIGWModel
	for _, m := range apigwModels.List() {
		if m.RestApiId == restApiId {
			out = append(out, m)
		}
	}
	if out == nil {
		out = []APIGWModel{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetModel(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	modelName := sim.PathParam(r, "modelName")
	m, ok := apigwModels.Get(restApiId + "/" + modelName)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Model Name specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWDeleteModel(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	modelName := sim.PathParam(r, "modelName")
	if !apigwModels.Delete(restApiId + "/" + modelName) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Model Name specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateRequestValidator(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(restApiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var req struct {
		Name                      string `json:"name"`
		ValidateRequestBody       bool   `json:"validateRequestBody"`
		ValidateRequestParameters bool   `json:"validateRequestParameters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	rv := APIGWRequestValidator{
		Id:                        generateUUID()[:10],
		Name:                      req.Name,
		ValidateRequestBody:       req.ValidateRequestBody,
		ValidateRequestParameters: req.ValidateRequestParameters,
		RestApiId:                 restApiId,
	}
	apigwRequestValidators.Put(restApiId+"/"+rv.Id, rv)
	sim.WriteJSON(w, http.StatusCreated, rv)
}

func handleAPIGWListRequestValidators(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(restApiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var out []APIGWRequestValidator
	for _, rv := range apigwRequestValidators.List() {
		if rv.RestApiId == restApiId {
			out = append(out, rv)
		}
	}
	if out == nil {
		out = []APIGWRequestValidator{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWDeleteRequestValidator(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	requestValidatorId := sim.PathParam(r, "requestValidatorId")
	if !apigwRequestValidators.Delete(restApiId + "/" + requestValidatorId) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Request Validator Id specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWCreateAuthorizer(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(restApiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var req struct {
		Name                         string   `json:"name"`
		Type                         string   `json:"type"`
		ProviderARNs                 []string `json:"providerARNs"`
		AuthType                     string   `json:"authType"`
		AuthorizerUri                string   `json:"authorizerUri"`
		AuthorizerCredentials        string   `json:"authorizerCredentials"`
		IdentitySource               string   `json:"identitySource"`
		IdentityValidationExpression string   `json:"identityValidationExpression"`
		AuthorizerResultTtlInSeconds *int     `json:"authorizerResultTtlInSeconds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	a := APIGWAuthorizer{
		Id:                           generateUUID()[:10],
		Name:                         req.Name,
		Type:                         req.Type,
		ProviderARNs:                 req.ProviderARNs,
		AuthType:                     req.AuthType,
		AuthorizerUri:                req.AuthorizerUri,
		AuthorizerCredentials:        req.AuthorizerCredentials,
		IdentitySource:               req.IdentitySource,
		IdentityValidationExpression: req.IdentityValidationExpression,
		AuthorizerResultTtlInSeconds: req.AuthorizerResultTtlInSeconds,
		RestApiId:                    restApiId,
	}
	apigwAuthorizers.Put(restApiId+"/"+a.Id, a)
	sim.WriteJSON(w, http.StatusCreated, a)
}

func handleAPIGWListAuthorizers(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(restApiId); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	var out []APIGWAuthorizer
	for _, a := range apigwAuthorizers.List() {
		if a.RestApiId == restApiId {
			out = append(out, a)
		}
	}
	if out == nil {
		out = []APIGWAuthorizer{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetAuthorizer(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	authorizerId := sim.PathParam(r, "authorizerId")
	a, ok := apigwAuthorizers.Get(restApiId + "/" + authorizerId)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Authorizer identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleAPIGWDeleteAuthorizer(w http.ResponseWriter, r *http.Request) {
	restApiId := sim.PathParam(r, "restApiId")
	authorizerId := sim.PathParam(r, "authorizerId")
	if !apigwAuthorizers.Delete(restApiId + "/" + authorizerId) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Authorizer identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
