package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Both API Gateway API versions serve one set of custom domain names: the v2
// handlers read and write apigwV1DomainNames and render each record in the
// v2 shape.

func registerAPIGatewayV2Domains(srv *sim.Server) {
	mux := srv
	src := "apigateway.amazonaws.com"
	apiResource := cloudTrailRESTResource("AWS::ApiGatewayV2::Api", "apiId")
	domainResource := cloudTrailRESTResource("AWS::ApiGatewayV2::DomainName", "domainName")
	mux.HandleFunc("PATCH /v2/domainnames/{domainName}", cloudTrailRecordedREST("UpdateDomainName", src, domainResource, handleAPIGWv2UpdateDomainName))
	mux.HandleFunc("POST /v2/tags/{resourceArn}", cloudTrailRecordedREST("TagResource", src, nil, handleAPIGWv2TagResource))
	mux.HandleFunc("DELETE /v2/tags/{resourceArn}", cloudTrailRecordedREST("UntagResource", src, nil, handleAPIGWv2UntagResource))
	mux.HandleFunc("PATCH /v2/apis/{apiId}/deployments/{deploymentId}", cloudTrailRecordedREST("UpdateDeployment", src, apiResource, handleAPIGWv2UpdateDeployment))
	mux.HandleFunc("PATCH /v2/apis/{apiId}/models/{modelId}", cloudTrailRecordedREST("UpdateModel", src, apiResource, handleAPIGWv2UpdateModel))
	mux.HandleFunc("PATCH /v2/vpclinks/{vpcLinkId}", cloudTrailRecordedREST("UpdateVpcLink", src, nil, handleAPIGWv2UpdateVpcLink))
}

// apigwPublicDomain returns a domain name both API versions can address; a
// private custom domain name is reachable only through the v1 API with its
// domainNameId.
func apigwPublicDomain(name string) (APIGWv1DomainName, bool) {
	d, ok := apigwV1DomainNames.Get(name)
	if !ok || d.DomainNameId != "" {
		return APIGWv1DomainName{}, false
	}
	return d, true
}

// apigwV2RoutingModes maps the v1 RoutingMode enum the store keeps to the v2
// enum, which names a base path mapping an API mapping.
var apigwV2RoutingModes = map[string]string{
	"BASE_PATH_MAPPING_ONLY":              "API_MAPPING_ONLY",
	"ROUTING_RULE_ONLY":                   "ROUTING_RULE_ONLY",
	"ROUTING_RULE_THEN_BASE_PATH_MAPPING": "ROUTING_RULE_THEN_API_MAPPING",
}

// apigwV1RoutingMode returns the stored form of a v2 RoutingMode, or "" for a
// value outside the v2 enum.
func apigwV1RoutingMode(v2 string) string {
	for v1, name := range apigwV2RoutingModes {
		if name == v2 {
			return v1
		}
	}
	return ""
}

func apigwV2DomainView(d APIGWv1DomainName) APIGWv2DomainName {
	cfg := APIGWv2DomainNameConfig{
		DomainNameStatus: d.DomainNameStatus,
		SecurityPolicy:   d.SecurityPolicy,
	}
	if d.EndpointConfiguration != nil {
		cfg.IpAddressType = d.EndpointConfiguration.IpAddressType
	}
	if d.DistributionDomainName != "" {
		cfg.EndpointType = "EDGE"
		cfg.ApiGatewayDomainName = d.DistributionDomainName
		cfg.HostedZoneId = d.DistributionHostedZoneId
		cfg.CertificateArn = d.CertificateArn
		cfg.CertificateName = d.CertificateName
	} else {
		cfg.EndpointType = "REGIONAL"
		cfg.ApiGatewayDomainName = d.RegionalDomainName
		cfg.HostedZoneId = d.RegionalHostedZoneId
		cfg.CertificateArn = d.RegionalCertificateArn
		cfg.CertificateName = d.RegionalCertificateName
	}
	if d.CertificateUploadDate != 0 {
		cfg.CertificateUploadDate = time.Unix(d.CertificateUploadDate, 0).UTC().Format(time.RFC3339)
	}
	return APIGWv2DomainName{
		DomainName:                    d.DomainName,
		DomainNameArn:                 d.DomainNameArn,
		ApiMappingSelectionExpression: "$request.basepath",
		DomainNameConfigurations:      []APIGWv2DomainNameConfig{cfg},
		RoutingMode:                   apigwV2RoutingModes[d.RoutingMode],
		Tags:                          d.Tags,
	}
}

// apigwApplyV2DomainConfig sets a domain's endpoint and certificate from a v2
// DomainNameConfiguration and returns "" or the BadRequestException message.
func apigwApplyV2DomainConfig(d *APIGWv1DomainName, cfg APIGWv2DomainNameConfig) string {
	endpoint := cfg.EndpointType
	if endpoint == "" {
		endpoint = "REGIONAL"
	}
	if cfg.CertificateArn == "" {
		return "A domain name configuration requires certificateArn"
	}
	ipType := cfg.IpAddressType
	if ipType == "" {
		ipType = "ipv4"
	}
	switch endpoint {
	case "REGIONAL":
		if msg := apigwCheckCertificate(cfg.CertificateArn, awsRegion()); msg != "" {
			return msg
		}
		if d.RegionalDomainName == "" {
			d.RegionalDomainName = fmt.Sprintf("d-%s.execute-api.%s.amazonaws.com", apigwRandomLower(10), awsRegion())
			d.RegionalHostedZoneId = apigwRegionalHostedZones[awsRegion()]
		}
		d.RegionalCertificateArn = cfg.CertificateArn
		d.RegionalCertificateName = cfg.CertificateName
		d.CertificateArn, d.CertificateName, d.DistributionDomainName, d.DistributionHostedZoneId = "", "", "", ""
	case "EDGE":
		// Amazon CloudFront serves edge-optimized endpoints and reads its
		// certificates from US East (N. Virginia).
		if msg := apigwCheckCertificate(cfg.CertificateArn, "us-east-1"); msg != "" {
			return msg
		}
		if d.DistributionDomainName == "" {
			d.DistributionDomainName = "d" + apigwRandomLower(13) + ".cloudfront.net"
			d.DistributionHostedZoneId = apigwEdgeHostedZone
		}
		d.CertificateArn = cfg.CertificateArn
		d.CertificateName = cfg.CertificateName
		d.RegionalCertificateArn, d.RegionalCertificateName, d.RegionalDomainName, d.RegionalHostedZoneId = "", "", "", ""
	default:
		return "Invalid endpoint type " + endpoint
	}
	d.CertificateUploadDate = time.Now().Unix()
	d.EndpointConfiguration = &APIGWEndpointConfiguration{Types: []string{endpoint}, IpAddressType: ipType}
	if cfg.SecurityPolicy != "" {
		d.SecurityPolicy = cfg.SecurityPolicy
	}
	return ""
}

func handleAPIGWv2CreateDomainName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainName               string                    `json:"domainName"`
		DomainNameConfigurations []APIGWv2DomainNameConfig `json:"domainNameConfigurations"`
		RoutingMode              string                    `json:"routingMode"`
		Tags                     map[string]string         `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DomainName == "" {
		AWSError(w, "BadRequestException", "DomainName is required", http.StatusBadRequest)
		return
	}
	if len(req.DomainNameConfigurations) != 1 {
		AWSError(w, "BadRequestException", "domainNameConfigurations must hold exactly one configuration", http.StatusBadRequest)
		return
	}
	routing := "BASE_PATH_MAPPING_ONLY"
	if req.RoutingMode != "" {
		if routing = apigwV1RoutingMode(req.RoutingMode); routing == "" {
			AWSError(w, "BadRequestException", "Invalid routingMode "+req.RoutingMode, http.StatusBadRequest)
			return
		}
	}
	d := APIGWv1DomainName{
		DomainName:       req.DomainName,
		DomainNameArn:    apigwDomainNameARN(req.DomainName),
		SecurityPolicy:   "TLS_1_2",
		RoutingMode:      routing,
		DomainNameStatus: "AVAILABLE",
		Tags:             req.Tags,
	}
	if msg := apigwApplyV2DomainConfig(&d, req.DomainNameConfigurations[0]); msg != "" {
		AWSError(w, "BadRequestException", msg, http.StatusBadRequest)
		return
	}
	if _, exists := apigwV1DomainNames.Get(req.DomainName); exists {
		AWSErrorf(w, "ConflictException", http.StatusConflict, "The domain name you provided already exists.")
		return
	}
	apigwV1DomainNames.Put(req.DomainName, d)
	sim.WriteJSON(w, http.StatusCreated, apigwV2DomainView(d))
}

func handleAPIGWv2ListDomainNames(w http.ResponseWriter, r *http.Request) {
	out := []APIGWv2DomainName{}
	for _, d := range apigwV1DomainNames.List() {
		if d.DomainNameId == "" {
			out = append(out, apigwV2DomainView(d))
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetDomainName(w http.ResponseWriter, r *http.Request) {
	d, ok := apigwPublicDomain(sim.PathParam(r, "domainName"))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, apigwV2DomainView(d))
}

func handleAPIGWv2UpdateDomainName(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "domainName")
	d, ok := apigwPublicDomain(name)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	var req struct {
		DomainNameConfigurations []APIGWv2DomainNameConfig `json:"domainNameConfigurations"`
		RoutingMode              string                    `json:"routingMode"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	switch len(req.DomainNameConfigurations) {
	case 0:
	case 1:
		if msg := apigwApplyV2DomainConfig(&d, req.DomainNameConfigurations[0]); msg != "" {
			AWSError(w, "BadRequestException", msg, http.StatusBadRequest)
			return
		}
	default:
		AWSError(w, "BadRequestException", "domainNameConfigurations must hold exactly one configuration", http.StatusBadRequest)
		return
	}
	if req.RoutingMode != "" {
		if d.RoutingMode = apigwV1RoutingMode(req.RoutingMode); d.RoutingMode == "" {
			AWSError(w, "BadRequestException", "Invalid routingMode "+req.RoutingMode, http.StatusBadRequest)
			return
		}
	}
	apigwV1DomainNames.Put(name, d)
	sim.WriteJSON(w, http.StatusOK, apigwV2DomainView(d))
}

func handleAPIGWv2DeleteDomainName(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "domainName")
	if _, ok := apigwPublicDomain(name); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	apigwDeleteDomain(name)
	w.WriteHeader(http.StatusNoContent)
}

// apigwDeleteDomain removes a custom domain name with the base path mappings,
// API mappings and routing rules that route through it.
func apigwDeleteDomain(key string) {
	apigwV1DomainNames.Delete(key)
	for _, m := range apigwBasePathMappings.ListPrefix(key + "/") {
		apigwBasePathMappings.Delete(m.ID)
	}
	for _, m := range apigwv2ApiMappings.ListPrefix(key + "/") {
		apigwv2ApiMappings.Delete(m.ID)
	}
	for _, rr := range apigwv2RoutingRules.ListPrefix(key + "/") {
		apigwv2RoutingRules.Delete(rr.ID)
	}
}

// apigwv2Tagged runs fn on the tag map of the resource an ARN names: an API
// (…/apis/<id>), its stage (…/apis/<id>/stages/<name>), a custom domain name
// (…/domainnames/<name>) or a VPC link (…/vpclinks/<id>).
func apigwv2Tagged(arn string, fn func(*map[string]string)) bool {
	tail := arn
	if idx := strings.LastIndex(arn, ":"); idx >= 0 {
		tail = arn[idx+1:]
	}
	parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	switch {
	case len(parts) == 4 && parts[0] == "apis" && parts[2] == "stages":
		return apigwTagsIn(apigwv2Stages, apigwv2StoreKey(parts[1], parts[3]), func(v *APIGWv2Stage) *map[string]string { return &v.Tags }, fn)
	case len(parts) != 2:
		return false
	case parts[0] == "apis":
		return apigwTagsIn(apigwv2Apis, parts[1], func(v *APIGWv2Api) *map[string]string { return &v.Tags }, fn)
	case parts[0] == "domainnames":
		if _, ok := apigwPublicDomain(parts[1]); !ok {
			return false
		}
		return apigwTagsIn(apigwV1DomainNames, parts[1], func(v *APIGWv1DomainName) *map[string]string { return &v.Tags }, fn)
	case parts[0] == "vpclinks":
		return apigwTagsIn(apigwv2VpcLinks, parts[1], func(v *APIGWv2VpcLink) *map[string]string { return &v.Tags }, fn)
	}
	return false
}

func handleAPIGWv2GetTags(w http.ResponseWriter, r *http.Request) {
	var tags map[string]string
	if !apigwv2Tagged(sim.PathParam(r, "resourceArn"), func(m *map[string]string) { tags = *m }) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid resource ARN specified")
		return
	}
	if tags == nil {
		tags = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func handleAPIGWv2TagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tags map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	found := apigwv2Tagged(sim.PathParam(r, "resourceArn"), func(m *map[string]string) {
		if *m == nil {
			*m = map[string]string{}
		}
		for k, v := range req.Tags {
			(*m)[k] = v
		}
	})
	if !found {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid resource ARN specified")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func handleAPIGWv2UntagResource(w http.ResponseWriter, r *http.Request) {
	keys := r.URL.Query()["tagKeys"]
	found := apigwv2Tagged(sim.PathParam(r, "resourceArn"), func(m *map[string]string) {
		for _, k := range keys {
			delete(*m, k)
		}
	})
	if !found {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid resource ARN specified")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2UpdateDeployment(w http.ResponseWriter, r *http.Request) {
	key := apigwv2StoreKey(sim.PathParam(r, "apiId"), sim.PathParam(r, "deploymentId"))
	var req struct {
		Description *string `json:"description"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	var out APIGWv2Deployment
	if !apigwv2Deployments.Update(key, func(d *APIGWv2Deployment) {
		if req.Description != nil {
			d.Description = *req.Description
		}
		out = *d
	}) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Deployment identifier specified %s", sim.PathParam(r, "deploymentId"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleAPIGWv2UpdateModel(w http.ResponseWriter, r *http.Request) {
	key := apigwv2StoreKey(sim.PathParam(r, "apiId"), sim.PathParam(r, "modelId"))
	var req struct {
		ContentType *string `json:"contentType"`
		Description *string `json:"description"`
		Name        *string `json:"name"`
		Schema      *string `json:"schema"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	var out APIGWv2Model
	if !apigwv2Models.Update(key, func(m *APIGWv2Model) {
		for dst, src := range map[*string]*string{&m.ContentType: req.ContentType, &m.Description: req.Description, &m.Name: req.Name, &m.Schema: req.Schema} {
			if src != nil {
				*dst = *src
			}
		}
		out = *m
	}) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Model identifier specified %s", sim.PathParam(r, "modelId"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleAPIGWv2UpdateVpcLink(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "vpcLinkId")
	var req struct {
		Name *string `json:"name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	var out APIGWv2VpcLink
	if !apigwv2VpcLinks.Update(id, func(v *APIGWv2VpcLink) {
		if req.Name != nil {
			v.Name = *req.Name
		}
		out = *v
	}) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid VpcLink identifier specified %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
