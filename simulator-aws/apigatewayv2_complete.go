package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// API Gateway v2 — developer-portal, portal-product, product-page,
// routing-rule, and API/api-mapping update slices that complete the
// restJson1 ApiGatewayV2 service surface. restJson1 serializes v2
// members in lowerCamelCase (the smithy jsonName trait); list wrappers
// use `items` (portals/products/pages) or `routingRules`.
//
// Nested objects whose request and response shapes coincide
// (authorization, portalContent, displayContent, routing-rule actions
// and conditions, restEndpointIdentifier) are stored as the raw decoded
// JSON the client sent and echoed back verbatim, so the round-trip stays
// faithful to whatever the client serialized. Where request and response
// shapes diverge (endpointConfiguration is EndpointConfigurationRequest
// inbound but EndpointConfigurationResponse outbound) the response shape
// is built explicitly.

// APIGWv2Portal models a developer portal (CreatePortal / GetPortal).
// Stored by PortalId. The nested authorization / portalContent objects are
// kept verbatim; endpointConfiguration is synthesized on read.
type APIGWv2Portal struct {
	PortalId                  string                       `json:"portalId"`
	PortalArn                 string                       `json:"portalArn"`
	PublishStatus             string                       `json:"publishStatus"`
	Authorization             json.RawMessage              `json:"authorization,omitempty"`
	PortalContent             json.RawMessage              `json:"portalContent,omitempty"`
	IncludedPortalProductArns []string                     `json:"includedPortalProductArns,omitempty"`
	RumAppMonitorName         string                       `json:"rumAppMonitorName,omitempty"`
	EndpointConfiguration     *APIGWv2PortalEndpointConfig `json:"endpointConfiguration,omitempty"`
	LastModified              string                       `json:"lastModified,omitempty"`
	LastPublished             string                       `json:"lastPublished,omitempty"`
	LastPublishedDescription  string                       `json:"lastPublishedDescription,omitempty"`
	Tags                      map[string]string            `json:"tags,omitempty"`
}

// APIGWv2PortalEndpointConfig mirrors EndpointConfigurationResponse.
type APIGWv2PortalEndpointConfig struct {
	CertificateArn           string `json:"certificateArn,omitempty"`
	DomainName               string `json:"domainName,omitempty"`
	PortalDefaultDomainName  string `json:"portalDefaultDomainName,omitempty"`
	PortalDomainHostedZoneId string `json:"portalDomainHostedZoneId,omitempty"`
}

// APIGWv2PortalProduct models a portal product (top-level resource keyed by id).
type APIGWv2PortalProduct struct {
	PortalProductId  string            `json:"portalProductId"`
	PortalProductArn string            `json:"portalProductArn"`
	DisplayName      string            `json:"displayName,omitempty"`
	Description      string            `json:"description,omitempty"`
	LastModified     string            `json:"lastModified,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	// SharingPolicy is the product's sharing-policy document
	// (PutPortalProductSharingPolicy). Held off the wire — emitted only by
	// GetPortalProductSharingPolicy.
	SharingPolicy string `json:"-"`
}

// APIGWv2ProductPage models a product page (child of a portal product).
// Keyed by `<portalProductId>/<productPageId>`.
type APIGWv2ProductPage struct {
	ProductPageId   string          `json:"productPageId"`
	ProductPageArn  string          `json:"productPageArn"`
	PortalProductId string          `json:"portalProductIdRef,omitempty"`
	DisplayContent  json.RawMessage `json:"displayContent,omitempty"`
	LastModified    string          `json:"lastModified,omitempty"`
}

// APIGWv2ProductRestEndpointPage models a product REST-endpoint page (child of
// a portal product). Keyed by `<portalProductId>/<productRestEndpointPageId>`.
type APIGWv2ProductRestEndpointPage struct {
	ProductRestEndpointPageId  string          `json:"productRestEndpointPageId"`
	ProductRestEndpointPageArn string          `json:"productRestEndpointPageArn"`
	PortalProductId            string          `json:"portalProductIdRef,omitempty"`
	RestEndpointIdentifier     json.RawMessage `json:"restEndpointIdentifier,omitempty"`
	DisplayContent             json.RawMessage `json:"displayContent,omitempty"`
	TryItState                 string          `json:"tryItState,omitempty"`
	Status                     string          `json:"status,omitempty"`
	LastModified               string          `json:"lastModified,omitempty"`
}

// APIGWv2RoutingRule models a routing rule under a domain name. Keyed by
// `<domainName>/<routingRuleId>`.
type APIGWv2RoutingRule struct {
	RoutingRuleId  string          `json:"routingRuleId"`
	RoutingRuleArn string          `json:"routingRuleArn"`
	DomainName     string          `json:"domainNameRef,omitempty"`
	Priority       int             `json:"priority"`
	Actions        json.RawMessage `json:"actions,omitempty"`
	Conditions     json.RawMessage `json:"conditions,omitempty"`
}

var (
	apigwv2Portals                  sim.Store[APIGWv2Portal]
	apigwv2PortalProducts           sim.Store[APIGWv2PortalProduct]
	apigwv2ProductPages             sim.Store[APIGWv2ProductPage]
	apigwv2ProductRestEndpointPages sim.Store[APIGWv2ProductRestEndpointPage]
	apigwv2RoutingRules             sim.Store[APIGWv2RoutingRule]
)

func registerAPIGatewayV2Complete(srv *sim.Server) {
	apigwv2Portals = sim.MakeStore[APIGWv2Portal](srv.DB(), "apigwv2_portals")
	apigwv2PortalProducts = sim.MakeStore[APIGWv2PortalProduct](srv.DB(), "apigwv2_portalproducts")
	apigwv2ProductPages = sim.MakeStore[APIGWv2ProductPage](srv.DB(), "apigwv2_productpages")
	apigwv2ProductRestEndpointPages = sim.MakeStore[APIGWv2ProductRestEndpointPage](srv.DB(), "apigwv2_productrestendpointpages")
	apigwv2RoutingRules = sim.MakeStore[APIGWv2RoutingRule](srv.DB(), "apigwv2_routingrules")

	mux := srv
	apiResource := cloudTrailRESTResource("AWS::ApiGatewayV2::Api", "apiId")
	domainResource := cloudTrailRESTResource("AWS::ApiGatewayV2::DomainName", "domainName")
	portalResource := cloudTrailRESTResource("AWS::ApiGatewayV2::Portal", "portalId")
	productResource := cloudTrailRESTResource("AWS::ApiGatewayV2::PortalProduct", "portalProductId")

	// Developer portals.
	mux.HandleFunc("POST /v2/portals", cloudTrailRecordedREST("CreatePortal", "apigateway.amazonaws.com", nil, handleAPIGWv2CreatePortal))
	mux.HandleFunc("GET /v2/portals", cloudTrailRecordedREST("ListPortals", "apigateway.amazonaws.com", nil, handleAPIGWv2ListPortals))
	mux.HandleFunc("GET /v2/portals/{portalId}", cloudTrailRecordedREST("GetPortal", "apigateway.amazonaws.com", portalResource, handleAPIGWv2GetPortal))
	mux.HandleFunc("PATCH /v2/portals/{portalId}", cloudTrailRecordedREST("UpdatePortal", "apigateway.amazonaws.com", portalResource, handleAPIGWv2UpdatePortal))
	mux.HandleFunc("DELETE /v2/portals/{portalId}", cloudTrailRecordedREST("DeletePortal", "apigateway.amazonaws.com", portalResource, handleAPIGWv2DeletePortal))
	mux.HandleFunc("POST /v2/portals/{portalId}/preview", cloudTrailRecordedREST("PreviewPortal", "apigateway.amazonaws.com", portalResource, handleAPIGWv2PreviewPortal))
	mux.HandleFunc("POST /v2/portals/{portalId}/publish", cloudTrailRecordedREST("PublishPortal", "apigateway.amazonaws.com", portalResource, handleAPIGWv2PublishPortal))
	mux.HandleFunc("DELETE /v2/portals/{portalId}/publish", cloudTrailRecordedREST("DisablePortal", "apigateway.amazonaws.com", portalResource, handleAPIGWv2DisablePortal))

	// Portal products.
	mux.HandleFunc("POST /v2/portalproducts", cloudTrailRecordedREST("CreatePortalProduct", "apigateway.amazonaws.com", nil, handleAPIGWv2CreatePortalProduct))
	mux.HandleFunc("GET /v2/portalproducts", cloudTrailRecordedREST("ListPortalProducts", "apigateway.amazonaws.com", nil, handleAPIGWv2ListPortalProducts))
	mux.HandleFunc("GET /v2/portalproducts/{portalProductId}", cloudTrailRecordedREST("GetPortalProduct", "apigateway.amazonaws.com", productResource, handleAPIGWv2GetPortalProduct))
	mux.HandleFunc("PATCH /v2/portalproducts/{portalProductId}", cloudTrailRecordedREST("UpdatePortalProduct", "apigateway.amazonaws.com", productResource, handleAPIGWv2UpdatePortalProduct))
	mux.HandleFunc("DELETE /v2/portalproducts/{portalProductId}", cloudTrailRecordedREST("DeletePortalProduct", "apigateway.amazonaws.com", productResource, handleAPIGWv2DeletePortalProduct))

	// Portal product sharing policy.
	mux.HandleFunc("GET /v2/portalproducts/{portalProductId}/sharingpolicy", cloudTrailRecordedREST("GetPortalProductSharingPolicy", "apigateway.amazonaws.com", productResource, handleAPIGWv2GetPortalProductSharingPolicy))
	mux.HandleFunc("PUT /v2/portalproducts/{portalProductId}/sharingpolicy", cloudTrailRecordedREST("PutPortalProductSharingPolicy", "apigateway.amazonaws.com", productResource, handleAPIGWv2PutPortalProductSharingPolicy))
	mux.HandleFunc("DELETE /v2/portalproducts/{portalProductId}/sharingpolicy", cloudTrailRecordedREST("DeletePortalProductSharingPolicy", "apigateway.amazonaws.com", productResource, handleAPIGWv2DeletePortalProductSharingPolicy))

	// Product pages.
	mux.HandleFunc("POST /v2/portalproducts/{portalProductId}/productpages", cloudTrailRecordedREST("CreateProductPage", "apigateway.amazonaws.com", productResource, handleAPIGWv2CreateProductPage))
	mux.HandleFunc("GET /v2/portalproducts/{portalProductId}/productpages", cloudTrailRecordedREST("ListProductPages", "apigateway.amazonaws.com", productResource, handleAPIGWv2ListProductPages))
	mux.HandleFunc("GET /v2/portalproducts/{portalProductId}/productpages/{productPageId}", cloudTrailRecordedREST("GetProductPage", "apigateway.amazonaws.com", productResource, handleAPIGWv2GetProductPage))
	mux.HandleFunc("PATCH /v2/portalproducts/{portalProductId}/productpages/{productPageId}", cloudTrailRecordedREST("UpdateProductPage", "apigateway.amazonaws.com", productResource, handleAPIGWv2UpdateProductPage))
	mux.HandleFunc("DELETE /v2/portalproducts/{portalProductId}/productpages/{productPageId}", cloudTrailRecordedREST("DeleteProductPage", "apigateway.amazonaws.com", productResource, handleAPIGWv2DeleteProductPage))

	// Product REST-endpoint pages.
	mux.HandleFunc("POST /v2/portalproducts/{portalProductId}/productrestendpointpages", cloudTrailRecordedREST("CreateProductRestEndpointPage", "apigateway.amazonaws.com", productResource, handleAPIGWv2CreateProductRestEndpointPage))
	mux.HandleFunc("GET /v2/portalproducts/{portalProductId}/productrestendpointpages", cloudTrailRecordedREST("ListProductRestEndpointPages", "apigateway.amazonaws.com", productResource, handleAPIGWv2ListProductRestEndpointPages))
	mux.HandleFunc("GET /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}", cloudTrailRecordedREST("GetProductRestEndpointPage", "apigateway.amazonaws.com", productResource, handleAPIGWv2GetProductRestEndpointPage))
	mux.HandleFunc("PATCH /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}", cloudTrailRecordedREST("UpdateProductRestEndpointPage", "apigateway.amazonaws.com", productResource, handleAPIGWv2UpdateProductRestEndpointPage))
	mux.HandleFunc("DELETE /v2/portalproducts/{portalProductId}/productrestendpointpages/{productRestEndpointPageId}", cloudTrailRecordedREST("DeleteProductRestEndpointPage", "apigateway.amazonaws.com", productResource, handleAPIGWv2DeleteProductRestEndpointPage))

	// Routing rules — children of a domain name.
	mux.HandleFunc("POST /v2/domainnames/{domainName}/routingrules", cloudTrailRecordedREST("CreateRoutingRule", "apigateway.amazonaws.com", domainResource, handleAPIGWv2CreateRoutingRule))
	mux.HandleFunc("GET /v2/domainnames/{domainName}/routingrules", cloudTrailRecordedREST("ListRoutingRules", "apigateway.amazonaws.com", domainResource, handleAPIGWv2ListRoutingRules))
	mux.HandleFunc("GET /v2/domainnames/{domainName}/routingrules/{routingRuleId}", cloudTrailRecordedREST("GetRoutingRule", "apigateway.amazonaws.com", domainResource, handleAPIGWv2GetRoutingRule))
	mux.HandleFunc("PUT /v2/domainnames/{domainName}/routingrules/{routingRuleId}", cloudTrailRecordedREST("PutRoutingRule", "apigateway.amazonaws.com", domainResource, handleAPIGWv2PutRoutingRule))
	mux.HandleFunc("DELETE /v2/domainnames/{domainName}/routingrules/{routingRuleId}", cloudTrailRecordedREST("DeleteRoutingRule", "apigateway.amazonaws.com", domainResource, handleAPIGWv2DeleteRoutingRule))

	// API + api-mapping updates (PATCH the existing v2 stores).
	mux.HandleFunc("PATCH /v2/apis/{apiId}", cloudTrailRecordedREST("UpdateApi", "apigateway.amazonaws.com", apiResource, handleAPIGWv2UpdateApi))
	mux.HandleFunc("PATCH /v2/domainnames/{domainName}/apimappings/{apiMappingId}", cloudTrailRecordedREST("UpdateApiMapping", "apigateway.amazonaws.com", domainResource, handleAPIGWv2UpdateApiMapping))

	// Reset a stage's authorizer cache.
	mux.HandleFunc("DELETE /v2/apis/{apiId}/stages/{stageName}/cache/authorizers", cloudTrailRecordedREST("ResetAuthorizersCache", "apigateway.amazonaws.com", apiResource, handleAPIGWv2ResetAuthorizersCache))
}

func apigwv2ChildKey(parent, child string) string { return parent + "/" + child }

// ---- Developer portals ----

func handleAPIGWv2CreatePortal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Authorization             json.RawMessage   `json:"authorization"`
		EndpointConfiguration     json.RawMessage   `json:"endpointConfiguration"`
		PortalContent             json.RawMessage   `json:"portalContent"`
		IncludedPortalProductArns []string          `json:"includedPortalProductArns"`
		LogoUri                   string            `json:"logoUri"`
		RumAppMonitorName         string            `json:"rumAppMonitorName"`
		Tags                      map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	portalID := generateUUID()[:10]
	now := time.Now().UTC().Format(time.RFC3339)
	p := APIGWv2Portal{
		PortalId:                  portalID,
		PortalArn:                 fmt.Sprintf("arn:aws:apigateway:%s::/portals/%s", awsRegion(), portalID),
		PublishStatus:             "DISABLED",
		Authorization:             req.Authorization,
		PortalContent:             req.PortalContent,
		IncludedPortalProductArns: req.IncludedPortalProductArns,
		RumAppMonitorName:         req.RumAppMonitorName,
		EndpointConfiguration: &APIGWv2PortalEndpointConfig{
			PortalDefaultDomainName:  fmt.Sprintf("%s.portal.%s.amazonaws.com", portalID, awsRegion()),
			PortalDomainHostedZoneId: "Z2FDTNDATAQYW2",
		},
		LastModified: now,
		Tags:         req.Tags,
	}
	apigwv2Portals.Put(p.PortalId, p)
	sim.WriteJSON(w, http.StatusCreated, p)
}

func handleAPIGWv2ListPortals(w http.ResponseWriter, r *http.Request) {
	all := apigwv2Portals.List()
	if all == nil {
		all = []APIGWv2Portal{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": all})
}

func handleAPIGWv2GetPortal(w http.ResponseWriter, r *http.Request) {
	p, ok := apigwv2Portals.Get(sim.PathParam(r, "portalId"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Portal identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleAPIGWv2UpdatePortal(w http.ResponseWriter, r *http.Request) {
	portalID := sim.PathParam(r, "portalId")
	p, ok := apigwv2Portals.Get(portalID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Portal identifier specified")
		return
	}
	var req struct {
		Authorization             json.RawMessage `json:"authorization"`
		PortalContent             json.RawMessage `json:"portalContent"`
		IncludedPortalProductArns []string        `json:"includedPortalProductArns"`
		RumAppMonitorName         *string         `json:"rumAppMonitorName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Authorization != nil {
		p.Authorization = req.Authorization
	}
	if req.PortalContent != nil {
		p.PortalContent = req.PortalContent
	}
	if req.IncludedPortalProductArns != nil {
		p.IncludedPortalProductArns = req.IncludedPortalProductArns
	}
	if req.RumAppMonitorName != nil {
		p.RumAppMonitorName = *req.RumAppMonitorName
	}
	p.LastModified = time.Now().UTC().Format(time.RFC3339)
	apigwv2Portals.Put(portalID, p)
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleAPIGWv2DeletePortal(w http.ResponseWriter, r *http.Request) {
	if !apigwv2Portals.Delete(sim.PathParam(r, "portalId")) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Portal identifier specified")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIGWv2PreviewPortal(w http.ResponseWriter, r *http.Request) {
	if _, ok := apigwv2Portals.Get(sim.PathParam(r, "portalId")); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Portal identifier specified")
		return
	}
	// PreviewPortalResponse has zero modeled members — empty body, 202.
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWv2PublishPortal(w http.ResponseWriter, r *http.Request) {
	portalID := sim.PathParam(r, "portalId")
	p, ok := apigwv2Portals.Get(portalID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Portal identifier specified")
		return
	}
	p.PublishStatus = "PUBLISHED"
	p.LastPublished = time.Now().UTC().Format(time.RFC3339)
	apigwv2Portals.Put(portalID, p)
	// PublishPortalResponse has zero modeled members — empty body, 202.
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIGWv2DisablePortal(w http.ResponseWriter, r *http.Request) {
	portalID := sim.PathParam(r, "portalId")
	p, ok := apigwv2Portals.Get(portalID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Portal identifier specified")
		return
	}
	p.PublishStatus = "DISABLED"
	apigwv2Portals.Put(portalID, p)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Portal products ----

func handleAPIGWv2CreatePortalProduct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName string            `json:"displayName"`
		Description string            `json:"description"`
		Tags        map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	productID := generateUUID()[:10]
	p := APIGWv2PortalProduct{
		PortalProductId:  productID,
		PortalProductArn: fmt.Sprintf("arn:aws:apigateway:%s::/portalproducts/%s", awsRegion(), productID),
		DisplayName:      req.DisplayName,
		Description:      req.Description,
		LastModified:     time.Now().UTC().Format(time.RFC3339),
		Tags:             req.Tags,
	}
	apigwv2PortalProducts.Put(p.PortalProductId, p)
	sim.WriteJSON(w, http.StatusCreated, p)
}

func handleAPIGWv2ListPortalProducts(w http.ResponseWriter, r *http.Request) {
	all := apigwv2PortalProducts.List()
	if all == nil {
		all = []APIGWv2PortalProduct{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": all})
}

func handleAPIGWv2GetPortalProduct(w http.ResponseWriter, r *http.Request) {
	p, ok := apigwv2PortalProducts.Get(sim.PathParam(r, "portalProductId"))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleAPIGWv2UpdatePortalProduct(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	p, ok := apigwv2PortalProducts.Get(productID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	var req struct {
		DisplayName *string `json:"displayName"`
		Description *string `json:"description"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DisplayName != nil {
		p.DisplayName = *req.DisplayName
	}
	if req.Description != nil {
		p.Description = *req.Description
	}
	p.LastModified = time.Now().UTC().Format(time.RFC3339)
	apigwv2PortalProducts.Put(productID, p)
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleAPIGWv2DeletePortalProduct(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	if !apigwv2PortalProducts.Delete(productID) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	// Cascade-delete the product's pages.
	for _, pg := range apigwv2ProductPages.List() {
		if pg.PortalProductId == productID {
			apigwv2ProductPages.Delete(apigwv2ChildKey(productID, pg.ProductPageId))
		}
	}
	for _, pg := range apigwv2ProductRestEndpointPages.List() {
		if pg.PortalProductId == productID {
			apigwv2ProductRestEndpointPages.Delete(apigwv2ChildKey(productID, pg.ProductRestEndpointPageId))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Portal product sharing policy ----

func handleAPIGWv2GetPortalProductSharingPolicy(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	p, ok := apigwv2PortalProducts.Get(productID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	out := map[string]any{"portalProductId": productID}
	if p.SharingPolicy != "" {
		out["policyDocument"] = p.SharingPolicy
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleAPIGWv2PutPortalProductSharingPolicy(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	p, ok := apigwv2PortalProducts.Get(productID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	var req struct {
		PolicyDocument string `json:"policyDocument"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	p.SharingPolicy = req.PolicyDocument
	apigwv2PortalProducts.Put(productID, p)
	// PutPortalProductSharingPolicyResponse has zero modeled members.
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleAPIGWv2DeletePortalProductSharingPolicy(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	p, ok := apigwv2PortalProducts.Get(productID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	p.SharingPolicy = ""
	apigwv2PortalProducts.Put(productID, p)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Product pages ----

func handleAPIGWv2CreateProductPage(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	if _, ok := apigwv2PortalProducts.Get(productID); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	var req struct {
		DisplayContent json.RawMessage `json:"displayContent"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	pageID := generateUUID()[:10]
	pg := APIGWv2ProductPage{
		ProductPageId:   pageID,
		ProductPageArn:  fmt.Sprintf("arn:aws:apigateway:%s::/portalproducts/%s/productpages/%s", awsRegion(), productID, pageID),
		PortalProductId: productID,
		DisplayContent:  req.DisplayContent,
		LastModified:    time.Now().UTC().Format(time.RFC3339),
	}
	apigwv2ProductPages.Put(apigwv2ChildKey(productID, pageID), pg)
	sim.WriteJSON(w, http.StatusCreated, pg)
}

func handleAPIGWv2ListProductPages(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	if _, ok := apigwv2PortalProducts.Get(productID); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	// ProductPageSummaryNoBody: lastModified, pageTitle, productPageArn, productPageId.
	type summary struct {
		LastModified   string `json:"lastModified,omitempty"`
		PageTitle      string `json:"pageTitle,omitempty"`
		ProductPageArn string `json:"productPageArn,omitempty"`
		ProductPageId  string `json:"productPageId,omitempty"`
	}
	out := []summary{}
	for _, pg := range apigwv2ProductPages.List() {
		if pg.PortalProductId != productID {
			continue
		}
		out = append(out, summary{
			LastModified:   pg.LastModified,
			PageTitle:      apigwv2DisplayContentTitle(pg.DisplayContent),
			ProductPageArn: pg.ProductPageArn,
			ProductPageId:  pg.ProductPageId,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// apigwv2DisplayContentTitle extracts the `title` field of a stored
// DisplayContent object, used to surface the summary `pageTitle`.
func apigwv2DisplayContentTitle(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var dc struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(raw, &dc); err != nil {
		return ""
	}
	return dc.Title
}

func handleAPIGWv2GetProductPage(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	pageID := sim.PathParam(r, "productPageId")
	pg, ok := apigwv2ProductPages.Get(apigwv2ChildKey(productID, pageID))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid ProductPage identifier specified %s", pageID)
		return
	}
	sim.WriteJSON(w, http.StatusOK, pg)
}

func handleAPIGWv2UpdateProductPage(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	pageID := sim.PathParam(r, "productPageId")
	pg, ok := apigwv2ProductPages.Get(apigwv2ChildKey(productID, pageID))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid ProductPage identifier specified %s", pageID)
		return
	}
	var req struct {
		DisplayContent json.RawMessage `json:"displayContent"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DisplayContent != nil {
		pg.DisplayContent = req.DisplayContent
	}
	pg.LastModified = time.Now().UTC().Format(time.RFC3339)
	apigwv2ProductPages.Put(apigwv2ChildKey(productID, pageID), pg)
	sim.WriteJSON(w, http.StatusOK, pg)
}

func handleAPIGWv2DeleteProductPage(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	pageID := sim.PathParam(r, "productPageId")
	if !apigwv2ProductPages.Delete(apigwv2ChildKey(productID, pageID)) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid ProductPage identifier specified %s", pageID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Product REST-endpoint pages ----

func handleAPIGWv2CreateProductRestEndpointPage(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	if _, ok := apigwv2PortalProducts.Get(productID); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	var req struct {
		RestEndpointIdentifier json.RawMessage `json:"restEndpointIdentifier"`
		DisplayContent         json.RawMessage `json:"displayContent"`
		TryItState             string          `json:"tryItState"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	pageID := generateUUID()[:10]
	pg := APIGWv2ProductRestEndpointPage{
		ProductRestEndpointPageId:  pageID,
		ProductRestEndpointPageArn: fmt.Sprintf("arn:aws:apigateway:%s::/portalproducts/%s/productrestendpointpages/%s", awsRegion(), productID, pageID),
		PortalProductId:            productID,
		RestEndpointIdentifier:     req.RestEndpointIdentifier,
		DisplayContent:             apigwv2RestEndpointDisplayResponse(req.DisplayContent),
		TryItState:                 req.TryItState,
		Status:                     "AVAILABLE",
		LastModified:               time.Now().UTC().Format(time.RFC3339),
	}
	apigwv2ProductRestEndpointPages.Put(apigwv2ChildKey(productID, pageID), pg)
	sim.WriteJSON(w, http.StatusCreated, pg)
}

// apigwv2RestEndpointDisplayResponse maps an inbound EndpointDisplayContent
// (a None/Overrides union) onto the EndpointDisplayContentResponse shape
// (body/endpoint/operationName). The request union carries no body verbatim,
// so the response display content is built from the overrides when present.
func apigwv2RestEndpointDisplayResponse(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var in struct {
		Overrides *struct {
			Body          string `json:"body"`
			Endpoint      string `json:"endpoint"`
			OperationName string `json:"operationName"`
		} `json:"overrides"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.Overrides == nil {
		return nil
	}
	out := map[string]string{}
	if in.Overrides.Body != "" {
		out["body"] = in.Overrides.Body
	}
	if in.Overrides.Endpoint != "" {
		out["endpoint"] = in.Overrides.Endpoint
	}
	if in.Overrides.OperationName != "" {
		out["operationName"] = in.Overrides.OperationName
	}
	if len(out) == 0 {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

func handleAPIGWv2ListProductRestEndpointPages(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	if _, ok := apigwv2PortalProducts.Get(productID); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid PortalProduct identifier specified")
		return
	}
	// ProductRestEndpointPageSummaryNoBody.
	type summary struct {
		Endpoint                   string          `json:"endpoint,omitempty"`
		LastModified               string          `json:"lastModified,omitempty"`
		OperationName              string          `json:"operationName,omitempty"`
		ProductRestEndpointPageArn string          `json:"productRestEndpointPageArn,omitempty"`
		ProductRestEndpointPageId  string          `json:"productRestEndpointPageId,omitempty"`
		RestEndpointIdentifier     json.RawMessage `json:"restEndpointIdentifier,omitempty"`
		Status                     string          `json:"status,omitempty"`
		TryItState                 string          `json:"tryItState,omitempty"`
	}
	out := []summary{}
	for _, pg := range apigwv2ProductRestEndpointPages.List() {
		if pg.PortalProductId != productID {
			continue
		}
		out = append(out, summary{
			LastModified:               pg.LastModified,
			ProductRestEndpointPageArn: pg.ProductRestEndpointPageArn,
			ProductRestEndpointPageId:  pg.ProductRestEndpointPageId,
			RestEndpointIdentifier:     pg.RestEndpointIdentifier,
			Status:                     pg.Status,
			TryItState:                 pg.TryItState,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func handleAPIGWv2GetProductRestEndpointPage(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	pageID := sim.PathParam(r, "productRestEndpointPageId")
	pg, ok := apigwv2ProductRestEndpointPages.Get(apigwv2ChildKey(productID, pageID))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid ProductRestEndpointPage identifier specified %s", pageID)
		return
	}
	sim.WriteJSON(w, http.StatusOK, pg)
}

func handleAPIGWv2UpdateProductRestEndpointPage(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	pageID := sim.PathParam(r, "productRestEndpointPageId")
	pg, ok := apigwv2ProductRestEndpointPages.Get(apigwv2ChildKey(productID, pageID))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid ProductRestEndpointPage identifier specified %s", pageID)
		return
	}
	var req struct {
		DisplayContent json.RawMessage `json:"displayContent"`
		TryItState     *string         `json:"tryItState"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.DisplayContent != nil {
		if dc := apigwv2RestEndpointDisplayResponse(req.DisplayContent); dc != nil {
			pg.DisplayContent = dc
		}
	}
	if req.TryItState != nil {
		pg.TryItState = *req.TryItState
	}
	pg.LastModified = time.Now().UTC().Format(time.RFC3339)
	apigwv2ProductRestEndpointPages.Put(apigwv2ChildKey(productID, pageID), pg)
	sim.WriteJSON(w, http.StatusOK, pg)
}

func handleAPIGWv2DeleteProductRestEndpointPage(w http.ResponseWriter, r *http.Request) {
	productID := sim.PathParam(r, "portalProductId")
	pageID := sim.PathParam(r, "productRestEndpointPageId")
	if !apigwv2ProductRestEndpointPages.Delete(apigwv2ChildKey(productID, pageID)) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid ProductRestEndpointPage identifier specified %s", pageID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Routing rules ----

func handleAPIGWv2CreateRoutingRule(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	if _, ok := apigwv2DomainNames.Get(domainName); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	var req struct {
		Actions    json.RawMessage `json:"actions"`
		Conditions json.RawMessage `json:"conditions"`
		Priority   int             `json:"priority"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	ruleID := generateUUID()[:10]
	rr := APIGWv2RoutingRule{
		RoutingRuleId:  ruleID,
		RoutingRuleArn: fmt.Sprintf("arn:aws:apigateway:%s::/domainnames/%s/routingrules/%s", awsRegion(), domainName, ruleID),
		DomainName:     domainName,
		Priority:       req.Priority,
		Actions:        req.Actions,
		Conditions:     req.Conditions,
	}
	apigwv2RoutingRules.Put(apigwv2ChildKey(domainName, ruleID), rr)
	sim.WriteJSON(w, http.StatusCreated, rr)
}

func handleAPIGWv2ListRoutingRules(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	if _, ok := apigwv2DomainNames.Get(domainName); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	out := []APIGWv2RoutingRule{}
	for _, rr := range apigwv2RoutingRules.List() {
		if rr.DomainName == domainName {
			out = append(out, rr)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"routingRules": out})
}

func handleAPIGWv2GetRoutingRule(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	ruleID := sim.PathParam(r, "routingRuleId")
	rr, ok := apigwv2RoutingRules.Get(apigwv2ChildKey(domainName, ruleID))
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid RoutingRule identifier specified %s", ruleID)
		return
	}
	sim.WriteJSON(w, http.StatusOK, rr)
}

func handleAPIGWv2PutRoutingRule(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	ruleID := sim.PathParam(r, "routingRuleId")
	if _, ok := apigwv2DomainNames.Get(domainName); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid domain name identifier specified")
		return
	}
	var req struct {
		Actions    json.RawMessage `json:"actions"`
		Conditions json.RawMessage `json:"conditions"`
		Priority   int             `json:"priority"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	rr := APIGWv2RoutingRule{
		RoutingRuleId:  ruleID,
		RoutingRuleArn: fmt.Sprintf("arn:aws:apigateway:%s::/domainnames/%s/routingrules/%s", awsRegion(), domainName, ruleID),
		DomainName:     domainName,
		Priority:       req.Priority,
		Actions:        req.Actions,
		Conditions:     req.Conditions,
	}
	apigwv2RoutingRules.Put(apigwv2ChildKey(domainName, ruleID), rr)
	sim.WriteJSON(w, http.StatusOK, rr)
}

func handleAPIGWv2DeleteRoutingRule(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	ruleID := sim.PathParam(r, "routingRuleId")
	if !apigwv2RoutingRules.Delete(apigwv2ChildKey(domainName, ruleID)) {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid RoutingRule identifier specified %s", ruleID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- API + api-mapping updates ----

func handleAPIGWv2UpdateApi(w http.ResponseWriter, r *http.Request) {
	apiID := sim.PathParam(r, "apiId")
	api, ok := apigwv2Apis.Get(apiID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid API identifier")
		return
	}
	var req struct {
		ApiKeySelectionExpression *string `json:"apiKeySelectionExpression"`
		Name                      *string `json:"name"`
		Description               *string `json:"description"`
		RouteSelectionExpression  *string `json:"routeSelectionExpression"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		api.Name = *req.Name
	}
	if req.ApiKeySelectionExpression != nil {
		api.ApiKeySelectionExpression = *req.ApiKeySelectionExpression
	}
	if req.RouteSelectionExpression != nil {
		api.RouteKey = *req.RouteSelectionExpression
	}
	apigwv2Apis.Put(apiID, api)
	sim.WriteJSON(w, http.StatusOK, api)
}

func handleAPIGWv2UpdateApiMapping(w http.ResponseWriter, r *http.Request) {
	domainName := sim.PathParam(r, "domainName")
	apiMappingID := sim.PathParam(r, "apiMappingId")
	m, ok := apigwv2ApiMappings.Get(domainName + "/" + apiMappingID)
	if !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid API mapping identifier specified %s", apiMappingID)
		return
	}
	var req struct {
		ApiId         *string `json:"apiId"`
		ApiMappingKey *string `json:"apiMappingKey"`
		Stage         *string `json:"stage"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
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
	apigwv2ApiMappings.Put(domainName+"/"+apiMappingID, m)
	sim.WriteJSON(w, http.StatusOK, m)
}

// ---- Reset authorizers cache ----

func handleAPIGWv2ResetAuthorizersCache(w http.ResponseWriter, r *http.Request) {
	apiID := sim.PathParam(r, "apiId")
	stageName := sim.PathParam(r, "stageName")
	if _, ok := apigwv2Stages.Get(apigwv2StoreKey(apiID, stageName)); !ok {
		sim.AWSErrorf(w, "NotFoundException", http.StatusNotFound,
			"Invalid Stage identifier specified %s", stageName)
		return
	}
	// Clearing the (empty) per-stage authorizer cache is a no-op against the
	// sim's metadata store. ResetAuthorizersCache has no modeled output — 204.
	w.WriteHeader(http.StatusNoContent)
}
