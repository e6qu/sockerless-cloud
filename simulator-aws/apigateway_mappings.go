package main

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A REST API's base path mapping and an API mapping are one resource, as the
// vendored api-gateway-rest-api-mappings page describes: both API versions
// read and write apigwv2ApiMappings, keyed "<domain key>/<apiMappingId>". The
// v1 API renders a mapping as a BasePathMapping, whose empty key reads
// "(none)".

type APIGWBasePathMapping struct {
	BasePath  string `json:"basePath"`
	RestApiId string `json:"restApiId,omitempty"`
	Stage     string `json:"stage,omitempty"`
}

// apigwMappingKeyChars is the character set the page allows in a mapping path.
var apigwMappingKeyChars = regexp.MustCompile(`^[A-Za-z0-9$\-_.+!*'()/]*$`)

func apigwBasePathView(m APIGWv2ApiMapping) APIGWBasePathMapping {
	basePath := m.ApiMappingKey
	if basePath == "" {
		basePath = "(none)"
	}
	return APIGWBasePathMapping{BasePath: basePath, RestApiId: m.ApiId, Stage: m.Stage}
}

func apigwMappingsOf(domainKey string) []sim.Keyed[APIGWv2ApiMapping] {
	return apigwv2ApiMappings.ListPrefix(domainKey + "/")
}

// apigwMappingByBasePath finds the mapping a v1 path names; "(none)" and an
// empty path name the empty key.
func apigwMappingByBasePath(domainKey, basePath string) (sim.Keyed[APIGWv2ApiMapping], bool) {
	if basePath == "(none)" {
		basePath = ""
	}
	for _, m := range apigwMappingsOf(domainKey) {
		if m.Item.ApiMappingKey == basePath {
			return m, true
		}
	}
	return sim.Keyed[APIGWv2ApiMapping]{}, false
}

type apigwMappingError struct {
	code   string
	status int
	msg    string
}

// apigwCheckMapping validates a mapping of key to an API stage on a domain;
// selfID names the mapping being updated, so it does not conflict with itself.
func apigwCheckMapping(domainKey, apiID, stage, key, selfID string, multiLevel bool) *apigwMappingError {
	d, ok := apigwV1DomainNames.Get(domainKey)
	if !ok {
		return &apigwMappingError{"NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified"}
	}
	if d.RoutingMode == "ROUTING_RULE_ONLY" {
		return &apigwMappingError{"BadRequestException", http.StatusBadRequest,
			"The domain name's routing mode is ROUTING_RULE_ONLY; set it to a mode that uses API mappings first"}
	}
	if len(key) > 300 || !apigwMappingKeyChars.MatchString(key) {
		return &apigwMappingError{"BadRequestException", http.StatusBadRequest,
			"An API mapping path holds at most 300 letters, numbers and the characters $-_.+!*'()/"}
	}
	if strings.Contains(key, "/") {
		switch {
		case !multiLevel:
			return &apigwMappingError{"BadRequestException", http.StatusBadRequest,
				"A base path mapping has a single level; create a multi-level API mapping with the apigatewayv2 API"}
		case d.RegionalDomainName == "" || d.SecurityPolicy != "TLS_1_2":
			return &apigwMappingError{"BadRequestException", http.StatusBadRequest,
				"Multi-level API mappings need a Regional custom domain name with the TLS_1_2 security policy"}
		}
	}
	if _, rest := apigwRestApis.Get(apiID); rest {
		if stage != "" {
			if _, ok := apigwStages.Get(apigwStageKey(apiID, stage)); !ok {
				return &apigwMappingError{"BadRequestException", http.StatusBadRequest, "Invalid stage identifier specified"}
			}
		}
	} else if _, isHTTPAPI := apigwv2Apis.Get(apiID); isHTTPAPI {
		if _, ok := apigwv2Stages.Get(apigwv2StoreKey(apiID, stage)); !ok {
			return &apigwMappingError{"BadRequestException", http.StatusBadRequest, "Invalid stage identifier specified"}
		}
	} else {
		return &apigwMappingError{"NotFoundException", http.StatusNotFound, "Invalid API identifier specified " + apiID}
	}
	for _, m := range apigwMappingsOf(domainKey) {
		if m.Item.ApiMappingKey == key && m.Item.ApiMappingId != selfID {
			return &apigwMappingError{"ConflictException", http.StatusConflict, "Only one base path mapping is allowed if the base path is empty, and base paths must be unique"}
		}
	}
	return nil
}

func (e *apigwMappingError) write(w http.ResponseWriter) {
	AWSError(w, e.code, e.msg, e.status)
}

func handleAPIGWCreateBasePathMapping(w http.ResponseWriter, r *http.Request) {
	domain := apigwDomainKey(r)
	var req struct {
		BasePath  string `json:"basePath"`
		RestApiId string `json:"restApiId"`
		Stage     string `json:"stage"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	key := req.BasePath
	if key == "(none)" {
		key = ""
	}
	if _, ok := apigwRestApis.Get(req.RestApiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid REST API identifier specified")
		return
	}
	if e := apigwCheckMapping(domain, req.RestApiId, req.Stage, key, "", false); e != nil {
		e.write(w)
		return
	}
	m := APIGWv2ApiMapping{ApiMappingId: apigwRandomLower(10), ApiId: req.RestApiId, ApiMappingKey: key, Stage: req.Stage, DomainName: domain}
	apigwv2ApiMappings.Put(domain+"/"+m.ApiMappingId, m)
	sim.WriteJSON(w, http.StatusCreated, apigwBasePathView(m))
}

func handleAPIGWListBasePathMappings(w http.ResponseWriter, r *http.Request) {
	domain := apigwDomainKey(r)
	if _, ok := apigwV1DomainNames.Get(domain); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	out := []APIGWBasePathMapping{}
	for _, m := range apigwMappingsOf(domain) {
		out = append(out, apigwBasePathView(m.Item))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetBasePathMapping(w http.ResponseWriter, r *http.Request) {
	m, ok := apigwMappingByBasePath(apigwDomainKey(r), sim.PathParam(r, "basePath"))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid base path mapping identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, apigwBasePathView(m.Item))
}

func handleAPIGWUpdateBasePathMapping(w http.ResponseWriter, r *http.Request) {
	domain := apigwDomainKey(r)
	found, ok := apigwMappingByBasePath(domain, sim.PathParam(r, "basePath"))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid base path mapping identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	m := found.Item
	for _, op := range ops {
		if op.Op != "replace" {
			AWSError(w, "BadRequestException", "Invalid patch path '"+op.Path+"' specified for op '"+op.Op+"'", http.StatusBadRequest)
			return
		}
		switch strings.TrimPrefix(op.Path, "/") {
		case "stage":
			m.Stage = op.Value
		case "restapiId", "restApiId":
			m.ApiId = op.Value
		case "basePath":
			m.ApiMappingKey = op.Value
			if m.ApiMappingKey == "(none)" {
				m.ApiMappingKey = ""
			}
		default:
			AWSError(w, "BadRequestException", "Invalid patch path '"+op.Path+"' specified for op 'replace'", http.StatusBadRequest)
			return
		}
	}
	if e := apigwCheckMapping(domain, m.ApiId, m.Stage, m.ApiMappingKey, m.ApiMappingId, false); e != nil {
		e.write(w)
		return
	}
	apigwv2ApiMappings.Put(found.ID, m)
	sim.WriteJSON(w, http.StatusOK, apigwBasePathView(m))
}

func handleAPIGWDeleteBasePathMapping(w http.ResponseWriter, r *http.Request) {
	m, ok := apigwMappingByBasePath(apigwDomainKey(r), sim.PathParam(r, "basePath"))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid base path mapping identifier specified")
		return
	}
	apigwv2ApiMappings.Delete(m.ID)
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWv2CreateApiMapping(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	if _, ok := apigwPublicDomain(domainName); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	var req struct {
		ApiId         string `json:"apiId"`
		ApiMappingKey string `json:"apiMappingKey"`
		Stage         string `json:"stage"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if e := apigwCheckMapping(domainName, req.ApiId, req.Stage, req.ApiMappingKey, "", true); e != nil {
		e.write(w)
		return
	}
	m := APIGWv2ApiMapping{ApiMappingId: apigwRandomLower(10), ApiId: req.ApiId, ApiMappingKey: req.ApiMappingKey, Stage: req.Stage, DomainName: domainName}
	apigwv2ApiMappings.Put(domainName+"/"+m.ApiMappingId, m)
	sim.WriteJSON(w, http.StatusCreated, m)
}

func handleAPIGWv2ListApiMappings(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	if _, ok := apigwPublicDomain(domainName); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	out := []APIGWv2ApiMapping{}
	for _, m := range apigwMappingsOf(domainName) {
		out = append(out, m.Item)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetApiMapping(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "apiMappingId")
	m, ok := apigwv2ApiMappings.Get(sim.PathParam(r, "domainName") + "/" + id)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API mapping identifier specified %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWv2UpdateApiMapping(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	id := sim.PathParam(r, "apiMappingId")
	m, ok := apigwv2ApiMappings.Get(domainName + "/" + id)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API mapping identifier specified %s", id)
		return
	}
	var req struct {
		ApiId         *string `json:"apiId"`
		ApiMappingKey *string `json:"apiMappingKey"`
		Stage         *string `json:"stage"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.ApiId != nil {
		m.ApiId = *req.ApiId
	}
	if req.ApiMappingKey != nil {
		m.ApiMappingKey = *req.ApiMappingKey
	}
	if req.Stage != nil {
		m.Stage = *req.Stage
	}
	if e := apigwCheckMapping(domainName, m.ApiId, m.Stage, m.ApiMappingKey, id, true); e != nil {
		e.write(w)
		return
	}
	apigwv2ApiMappings.Put(domainName+"/"+id, m)
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleAPIGWv2DeleteApiMapping(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "apiMappingId")
	if !apigwv2ApiMappings.Delete(sim.PathParam(r, "domainName") + "/" + id) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API mapping identifier specified %s", id)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
