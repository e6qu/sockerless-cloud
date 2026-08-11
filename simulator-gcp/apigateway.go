package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// API Gateway v1 — REST surface scoped to Api / ApiConfig / Gateway
// CRUD. Real API: https://apigateway.googleapis.com/$discovery/rest?version=v1

type APIGWApi struct {
	Name        string            `json:"name"` // projects/{p}/locations/global/apis/{api}
	DisplayName string            `json:"displayName,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	State       string            `json:"state,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type APIGWApiConfig struct {
	Name        string            `json:"name"` // projects/{p}/locations/global/apis/{api}/configs/{cfg}
	DisplayName string            `json:"displayName,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	State       string            `json:"state,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	// openapiDocuments is ForceNew on google_api_gateway_api_config; the read
	// must echo it back or terraform plans a replacement on every refresh.
	OpenapiDocuments      json.RawMessage `json:"openapiDocuments,omitempty"`
	GatewayServiceAccount string          `json:"gatewayServiceAccount,omitempty"`
}

type APIGWGateway struct {
	Name            string            `json:"name"` // projects/{p}/locations/{loc}/gateways/{gw}
	DisplayName     string            `json:"displayName,omitempty"`
	ApiConfig       string            `json:"apiConfig,omitempty"`
	CreateTime      string            `json:"createTime,omitempty"`
	State           string            `json:"state,omitempty"`
	DefaultHostname string            `json:"defaultHostname,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

var (
	apigwApis     sim.Store[APIGWApi]
	apigwConfigs  sim.Store[APIGWApiConfig]
	apigwGateways sim.Store[APIGWGateway]
)

func registerGCPAPIGateway(srv *sim.Server) {
	apigwApis = sim.MakeStore[APIGWApi](srv.DB(), "gcp_apigw_apis")
	apigwConfigs = sim.MakeStore[APIGWApiConfig](srv.DB(), "gcp_apigw_configs")
	apigwGateways = sim.MakeStore[APIGWGateway](srv.DB(), "gcp_apigw_gateways")
	apigwIamPolicies = sim.MakeStore[GCPAPIGWIamPolicy](srv.DB(), "gcp_apigw_iam_policies")

	// Apis (always under locations/global per real GCP).
	srv.HandleFunc("POST /v1/projects/{project}/locations/global/apis", handleGCPAPIGWCreateApi)
	srv.HandleFunc("GET /v1/projects/{project}/locations/global/apis/{api}", handleGCPAPIGWGetApi)
	srv.HandleFunc("GET /v1/projects/{project}/locations/global/apis", handleGCPAPIGWListApis)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/global/apis/{api}", handleGCPAPIGWPatchApi)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/global/apis/{api}", handleGCPAPIGWDeleteApi)

	// ApiConfigs (under Apis).
	srv.HandleFunc("POST /v1/projects/{project}/locations/global/apis/{api}/configs", handleGCPAPIGWCreateConfig)
	srv.HandleFunc("GET /v1/projects/{project}/locations/global/apis/{api}/configs/{cfg}", handleGCPAPIGWGetConfig)
	srv.HandleFunc("GET /v1/projects/{project}/locations/global/apis/{api}/configs", handleGCPAPIGWListConfigs)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/global/apis/{api}/configs/{cfg}", handleGCPAPIGWPatchConfig)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/global/apis/{api}/configs/{cfg}", handleGCPAPIGWDeleteConfig)

	// Gateways (regional).
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/gateways", handleGCPAPIGWCreateGateway)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/gateways/{gw}", handleGCPAPIGWGetGateway)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/gateways", handleGCPAPIGWListGateways)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/gateways/{gw}", handleGCPAPIGWPatchGateway)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/gateways/{gw}", handleGCPAPIGWDeleteGateway)

	// IAM v1 per AIP-130. Empty default policy until setIamPolicy
	// lands a real one; testIamPermissions returns the permission set
	// as-allowed (sim doesn't model authorization).
	//
	// Go ServeMux can't parse `{gw}:getIamPolicy`; capture the action
	// suffix in a single wildcard and split on `:` in the handler.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/gateways/{gwAction}", handleGCPAPIGWIamAction)
}

func handleGCPAPIGWIamAction(w http.ResponseWriter, r *http.Request) {
	gwAction := sim.PathParam(r, "gwAction")
	gw, action, found := strings.Cut(gwAction, ":")
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"unknown action on gateway %q", gwAction)
		return
	}
	switch action {
	case "getIamPolicy":
		handleGCPAPIGWGetIamPolicy(w, r, gw)
	case "setIamPolicy":
		handleGCPAPIGWSetIamPolicy(w, r, gw)
	case "testIamPermissions":
		handleGCPAPIGWTestIamPermissions(w, r, gw)
	default:
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"unknown action %q on gateway %q", action, gw)
	}
}

// GCPAPIGWIamBinding mirrors google.iam.v1.Binding.
type GCPAPIGWIamBinding struct {
	Role    string   `json:"role"`
	Members []string `json:"members"`
}

// GCPAPIGWIamPolicy is the canonical google.iam.v1.Policy shape.
type GCPAPIGWIamPolicy struct {
	Version  int                  `json:"version"`
	Etag     string               `json:"etag,omitempty"`
	Bindings []GCPAPIGWIamBinding `json:"bindings,omitempty"`
}

var apigwIamPolicies sim.Store[GCPAPIGWIamPolicy]

func apigwIamPolicyKey(project, location, gw string) string {
	return project + "/" + location + "/" + gw
}

func handleGCPAPIGWGetIamPolicy(w http.ResponseWriter, r *http.Request, gw string) {
	key := apigwIamPolicyKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"), gw)
	p, ok := apigwIamPolicies.Get(key)
	if !ok {
		p = GCPAPIGWIamPolicy{Version: 1, Etag: "ACAB"}
	}
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleGCPAPIGWSetIamPolicy(w http.ResponseWriter, r *http.Request, gw string) {
	var req struct {
		Policy GCPAPIGWIamPolicy `json:"policy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	key := apigwIamPolicyKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"), gw)
	apigwIamPolicies.Put(key, req.Policy)
	sim.WriteJSON(w, http.StatusOK, req.Policy)
}

func handleGCPAPIGWTestIamPermissions(w http.ResponseWriter, r *http.Request, _ string) {
	var req struct {
		Permissions []string `json:"permissions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	// Sim doesn't model authorization; echo the requested set as allowed.
	sim.WriteJSON(w, http.StatusOK, map[string]any{"permissions": req.Permissions})
}

func handleGCPAPIGWCreateApi(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	apiId := r.URL.Query().Get("apiId")
	if apiId == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "apiId query param is required")
		return
	}
	var req APIGWApi
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	name := fmt.Sprintf("projects/%s/locations/global/apis/%s", project, apiId)
	api := APIGWApi{
		Name:        name,
		DisplayName: defaultStr(req.DisplayName, apiId),
		CreateTime:  nowTimestamp(),
		State:       "ACTIVE",
		Labels:      req.Labels,
	}
	apigwApis.Put(name, api)
	op := newLRO(project, "global", api, "type.googleapis.com/google.cloud.apigateway.v1.Api")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleGCPAPIGWGetApi(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/global/apis/%s", sim.PathParam(r, "project"), sim.PathParam(r, "api"))
	api, ok := apigwApis.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "api not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, api)
}

func handleGCPAPIGWListApis(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/global/apis/", sim.PathParam(r, "project"))
	var out []APIGWApi
	for _, a := range apigwApis.List() {
		if strings.HasPrefix(a.Name, prefix) && !strings.Contains(strings.TrimPrefix(a.Name, prefix), "/") {
			out = append(out, a)
		}
	}
	if out == nil {
		out = []APIGWApi{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"apis": out})
}

func handleGCPAPIGWPatchApi(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := fmt.Sprintf("projects/%s/locations/global/apis/%s", project, sim.PathParam(r, "api"))
	api, ok := apigwApis.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "api not found: %s", name)
		return
	}
	var req APIGWApi
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.DisplayName != "" {
		api.DisplayName = req.DisplayName
	}
	if req.Labels != nil {
		api.Labels = req.Labels
	}
	apigwApis.Put(name, api)
	op := newLRO(project, "global", api, "type.googleapis.com/google.cloud.apigateway.v1.Api")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleGCPAPIGWDeleteApi(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := fmt.Sprintf("projects/%s/locations/global/apis/%s", project, sim.PathParam(r, "api"))
	if !apigwApis.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "api not found: %s", name)
		return
	}
	op := newLRO(project, "global", nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleGCPAPIGWCreateConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	api := sim.PathParam(r, "api")
	cfgId := r.URL.Query().Get("apiConfigId")
	if cfgId == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "apiConfigId query param is required")
		return
	}
	var req APIGWApiConfig
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	name := fmt.Sprintf("projects/%s/locations/global/apis/%s/configs/%s", project, api, cfgId)
	c := APIGWApiConfig{
		Name:                  name,
		DisplayName:           defaultStr(req.DisplayName, cfgId),
		CreateTime:            nowTimestamp(),
		State:                 "ACTIVE",
		Labels:                req.Labels,
		OpenapiDocuments:      req.OpenapiDocuments,
		GatewayServiceAccount: req.GatewayServiceAccount,
	}
	apigwConfigs.Put(name, c)
	op := newLRO(project, "global", c, "type.googleapis.com/google.cloud.apigateway.v1.ApiConfig")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleGCPAPIGWGetConfig(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/global/apis/%s/configs/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "api"), sim.PathParam(r, "cfg"))
	c, ok := apigwConfigs.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "config not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleGCPAPIGWListConfigs(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/global/apis/%s/configs/", sim.PathParam(r, "project"), sim.PathParam(r, "api"))
	var out []APIGWApiConfig
	for _, c := range apigwConfigs.List() {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []APIGWApiConfig{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"apiConfigs": out})
}

func handleGCPAPIGWPatchConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := fmt.Sprintf("projects/%s/locations/global/apis/%s/configs/%s",
		project, sim.PathParam(r, "api"), sim.PathParam(r, "cfg"))
	c, ok := apigwConfigs.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "config not found: %s", name)
		return
	}
	var req APIGWApiConfig
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.DisplayName != "" {
		c.DisplayName = req.DisplayName
	}
	if req.Labels != nil {
		c.Labels = req.Labels
	}
	apigwConfigs.Put(name, c)
	op := newLRO(project, "global", c, "type.googleapis.com/google.cloud.apigateway.v1.ApiConfig")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleGCPAPIGWDeleteConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := fmt.Sprintf("projects/%s/locations/global/apis/%s/configs/%s",
		project, sim.PathParam(r, "api"), sim.PathParam(r, "cfg"))
	if !apigwConfigs.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "config not found: %s", name)
		return
	}
	op := newLRO(project, "global", nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleGCPAPIGWCreateGateway(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	gwId := r.URL.Query().Get("gatewayId")
	if gwId == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "gatewayId query param is required")
		return
	}
	var req APIGWGateway
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	name := fmt.Sprintf("projects/%s/locations/%s/gateways/%s", project, location, gwId)
	g := APIGWGateway{
		Name:            name,
		DisplayName:     defaultStr(req.DisplayName, gwId),
		ApiConfig:       req.ApiConfig,
		CreateTime:      nowTimestamp(),
		State:           "ACTIVE",
		DefaultHostname: fmt.Sprintf("%s-%s.example", gwId, location),
		Labels:          req.Labels,
	}
	apigwGateways.Put(name, g)
	op := newLRO(project, location, g, "type.googleapis.com/google.cloud.apigateway.v1.Gateway")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleGCPAPIGWGetGateway(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/gateways/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "gw"))
	g, ok := apigwGateways.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "gateway not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, g)
}

func handleGCPAPIGWListGateways(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/gateways/", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	var out []APIGWGateway
	for _, g := range apigwGateways.List() {
		if strings.HasPrefix(g.Name, prefix) {
			out = append(out, g)
		}
	}
	if out == nil {
		out = []APIGWGateway{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"gateways": out})
}

func handleGCPAPIGWPatchGateway(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/gateways/%s", project, location, sim.PathParam(r, "gw"))
	g, ok := apigwGateways.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "gateway not found: %s", name)
		return
	}
	var req APIGWGateway
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.DisplayName != "" {
		g.DisplayName = req.DisplayName
	}
	if req.ApiConfig != "" {
		g.ApiConfig = req.ApiConfig
	}
	if req.Labels != nil {
		g.Labels = req.Labels
	}
	apigwGateways.Put(name, g)
	op := newLRO(project, location, g, "type.googleapis.com/google.cloud.apigateway.v1.Gateway")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleGCPAPIGWDeleteGateway(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/gateways/%s", project, location, sim.PathParam(r, "gw"))
	if !apigwGateways.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "gateway not found: %s", name)
		return
	}
	op := newLRO(project, location, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}
