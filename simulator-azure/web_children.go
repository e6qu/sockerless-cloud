package main

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_children.go serves four Microsoft.Web/sites child-resource families on
// production sites and their deployment-slot twins: public certificates,
// domain ownership identifiers, premier add-ons, and the config/pushsettings
// section. Each family keeps one store keyed by the canonical ARM resource ID
// (the /slots/{slot} segment included when a slot is addressed), so slot
// resources are their own rows, never views of the production site's.

// webChildType is the ARM resource type of a site child at the addressed
// level: "Microsoft.Web/sites/<child>" on the production site,
// "Microsoft.Web/sites/slots/<child>" on a deployment slot.
func webChildType(r *http.Request, child string) string {
	if sim.PathParam(r, "slot") != "" {
		return "Microsoft.Web/sites/slots/" + child
	}
	return "Microsoft.Web/sites/" + child
}

// WebPublicCertificate mirrors the swagger PublicCertificate resource.
type WebPublicCertificate struct {
	ID         string                         `json:"id,omitempty"`
	Name       string                         `json:"name,omitempty"`
	Type       string                         `json:"type,omitempty"`
	Kind       string                         `json:"kind,omitempty"`
	Properties WebPublicCertificateProperties `json:"properties"`
}

// WebPublicCertificateProperties mirrors PublicCertificateProperties: the DER
// certificate bytes, the certificate store location, and the read-only
// thumbprint the service derives from the blob.
type WebPublicCertificateProperties struct {
	Blob                      []byte `json:"blob,omitempty"`
	PublicCertificateLocation string `json:"publicCertificateLocation,omitempty"`
	Thumbprint                string `json:"thumbprint,omitempty"`
}

var webPublicCertificates sim.Store[WebPublicCertificate]

// WebDomainOwnershipIdentifier mirrors the swagger Identifier resource; the
// identity string rides the wire as properties.id.
type WebDomainOwnershipIdentifier struct {
	ID         string                                 `json:"id,omitempty"`
	Name       string                                 `json:"name,omitempty"`
	Type       string                                 `json:"type,omitempty"`
	Kind       string                                 `json:"kind,omitempty"`
	Properties WebDomainOwnershipIdentifierProperties `json:"properties"`
}

// WebDomainOwnershipIdentifierProperties mirrors IdentifierProperties.
type WebDomainOwnershipIdentifierProperties struct {
	Value string `json:"id,omitempty"`
}

var webDomainOwnershipIdentifiers sim.Store[WebDomainOwnershipIdentifier]

// WebPremierAddOn mirrors the swagger PremierAddOn tracked resource.
type WebPremierAddOn struct {
	ID         string                    `json:"id,omitempty"`
	Name       string                    `json:"name,omitempty"`
	Type       string                    `json:"type,omitempty"`
	Kind       string                    `json:"kind,omitempty"`
	Location   string                    `json:"location,omitempty"`
	Tags       map[string]string         `json:"tags,omitempty"`
	Properties WebPremierAddOnProperties `json:"properties"`
}

// WebPremierAddOnProperties mirrors PremierAddOnProperties.
type WebPremierAddOnProperties struct {
	Sku                  string `json:"sku,omitempty"`
	Product              string `json:"product,omitempty"`
	Vendor               string `json:"vendor,omitempty"`
	MarketplacePublisher string `json:"marketplacePublisher,omitempty"`
	MarketplaceOffer     string `json:"marketplaceOffer,omitempty"`
}

var webPremierAddOns sim.Store[WebPremierAddOn]

// WebPushSettings mirrors the swagger PushSettings config sub-resource.
type WebPushSettings struct {
	ID         string                    `json:"id,omitempty"`
	Name       string                    `json:"name,omitempty"`
	Type       string                    `json:"type,omitempty"`
	Kind       string                    `json:"kind,omitempty"`
	Properties WebPushSettingsProperties `json:"properties"`
}

// WebPushSettingsProperties mirrors PushSettingsProperties.
type WebPushSettingsProperties struct {
	IsPushEnabled     bool   `json:"isPushEnabled"`
	TagWhitelistJSON  string `json:"tagWhitelistJson,omitempty"`
	TagsRequiringAuth string `json:"tagsRequiringAuth,omitempty"`
	DynamicTagsJSON   string `json:"dynamicTagsJson,omitempty"`
}

var webPushSettings sim.Store[WebPushSettings]

func registerWebChildResources(srv *sim.Server) {
	webPublicCertificates = sim.MakeStore[WebPublicCertificate](srv.DB(), "web_public_certificates")
	webDomainOwnershipIdentifiers = sim.MakeStore[WebDomainOwnershipIdentifier](srv.DB(), "web_domain_ownership_identifiers")
	webPremierAddOns = sim.MakeStore[WebPremierAddOn](srv.DB(), "web_premier_addons")
	webPushSettings = sim.MakeStore[WebPushSettings](srv.DB(), "web_pushsettings")

	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}

	registerWebPublicCertificates(both)
	registerWebDomainOwnershipIdentifiers(both)
	registerWebPremierAddOns(both)
	registerWebPushSettings(both)
}

// ---- publicCertificates -----------------------------------------------------

func registerWebPublicCertificates(both func(string, string, http.HandlerFunc)) {
	certID := func(r *http.Request) string {
		return webResourceID(r) + "/publicCertificates/" + sim.PathParam(r, "publicCertificateName")
	}
	both("GET", "/publicCertificates", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/publicCertificates/"
		out := webPublicCertificates.Filter(func(c WebPublicCertificate) bool { return strings.HasPrefix(c.ID, prefix) })
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		if out == nil {
			out = []WebPublicCertificate{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/publicCertificates/{publicCertificateName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		c, ok := webPublicCertificates.Get(certID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Public certificate %q not found.", sim.PathParam(r, "publicCertificateName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, c)
	})
	both("PUT", "/publicCertificates/{publicCertificateName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebPublicCertificate
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Properties.Blob) == 0 {
			sim.AzureError(w, "InvalidRequestContent", "The 'properties.blob' property is required.", http.StatusBadRequest)
			return
		}
		// The blob is a DER certificate; real App Service rejects bytes that
		// do not parse as one. The thumbprint is the SHA-1 of the DER bytes —
		// the fingerprint every Azure certificate surface reports.
		if _, err := x509.ParseCertificate(req.Properties.Blob); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "The certificate blob is not a valid X.509 certificate: "+err.Error(), http.StatusBadRequest)
			return
		}
		sum := sha1.Sum(req.Properties.Blob)
		req.ID = certID(r)
		req.Name = sim.PathParam(r, "publicCertificateName")
		req.Type = webChildType(r, "publicCertificates")
		req.Properties.Thumbprint = strings.ToUpper(hex.EncodeToString(sum[:]))
		webPublicCertificates.Put(req.ID, req)
		sim.WriteJSON(w, http.StatusOK, req)
	})
	both("DELETE", "/publicCertificates/{publicCertificateName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		if webPublicCertificates.Delete(certID(r)) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ---- domainOwnershipIdentifiers ----------------------------------------------

func registerWebDomainOwnershipIdentifiers(both func(string, string, http.HandlerFunc)) {
	identID := func(r *http.Request) string {
		return webResourceID(r) + "/domainOwnershipIdentifiers/" + sim.PathParam(r, "domainOwnershipIdentifierName")
	}
	both("GET", "/domainOwnershipIdentifiers", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/domainOwnershipIdentifiers/"
		out := webDomainOwnershipIdentifiers.Filter(func(d WebDomainOwnershipIdentifier) bool { return strings.HasPrefix(d.ID, prefix) })
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		if out == nil {
			out = []WebDomainOwnershipIdentifier{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/domainOwnershipIdentifiers/{domainOwnershipIdentifierName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		d, ok := webDomainOwnershipIdentifiers.Get(identID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Domain ownership identifier %q not found.", sim.PathParam(r, "domainOwnershipIdentifierName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, d)
	})
	both("PUT", "/domainOwnershipIdentifiers/{domainOwnershipIdentifierName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebDomainOwnershipIdentifier
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		req.ID = identID(r)
		req.Name = sim.PathParam(r, "domainOwnershipIdentifierName")
		req.Type = webChildType(r, "domainOwnershipIdentifiers")
		webDomainOwnershipIdentifiers.Put(req.ID, req)
		sim.WriteJSON(w, http.StatusOK, req)
	})
	both("PATCH", "/domainOwnershipIdentifiers/{domainOwnershipIdentifierName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		d, ok := webDomainOwnershipIdentifiers.Get(identID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Domain ownership identifier %q not found.", sim.PathParam(r, "domainOwnershipIdentifierName"))
			return
		}
		// ARM PATCH is a merge: only keys present in the body change.
		var req struct {
			Kind       *string `json:"kind"`
			Properties *struct {
				Value *string `json:"id"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Kind != nil {
			d.Kind = *req.Kind
		}
		if req.Properties != nil && req.Properties.Value != nil {
			d.Properties.Value = *req.Properties.Value
		}
		webDomainOwnershipIdentifiers.Put(d.ID, d)
		sim.WriteJSON(w, http.StatusOK, d)
	})
	both("DELETE", "/domainOwnershipIdentifiers/{domainOwnershipIdentifierName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		if webDomainOwnershipIdentifiers.Delete(identID(r)) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ---- premieraddons -----------------------------------------------------------

func registerWebPremierAddOns(both func(string, string, http.HandlerFunc)) {
	addonID := func(r *http.Request) string {
		return webResourceID(r) + "/premieraddons/" + sim.PathParam(r, "premierAddOnName")
	}
	// The swagger models the collection GET as a single PremierAddOn (the
	// operation is WebApps_ListPremierAddOns but its declared 200 schema is
	// the resource, not a collection), so the response is the app's premier
	// add-on — the first by resource ID when several exist, an empty object
	// when none does.
	both("GET", "/premieraddons", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/premieraddons/"
		out := webPremierAddOns.Filter(func(a WebPremierAddOn) bool { return strings.HasPrefix(a.ID, prefix) })
		if len(out) == 0 {
			sim.WriteJSON(w, http.StatusOK, map[string]any{})
			return
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		sim.WriteJSON(w, http.StatusOK, out[0])
	})
	both("GET", "/premieraddons/{premierAddOnName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		a, ok := webPremierAddOns.Get(addonID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Premier add-on %q not found.", sim.PathParam(r, "premierAddOnName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, a)
	})
	both("PUT", "/premieraddons/{premierAddOnName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebPremierAddOn
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		req.ID = addonID(r)
		req.Name = sim.PathParam(r, "premierAddOnName")
		req.Type = webChildType(r, "premieraddons")
		webPremierAddOns.Put(req.ID, req)
		sim.WriteJSON(w, http.StatusOK, req)
	})
	both("PATCH", "/premieraddons/{premierAddOnName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		a, ok := webPremierAddOns.Get(addonID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Premier add-on %q not found.", sim.PathParam(r, "premierAddOnName"))
			return
		}
		// ARM PATCH is a merge: only keys present in the body change.
		var req struct {
			Kind       *string           `json:"kind"`
			Tags       map[string]string `json:"tags"`
			Properties *struct {
				Sku                  *string `json:"sku"`
				Product              *string `json:"product"`
				Vendor               *string `json:"vendor"`
				MarketplacePublisher *string `json:"marketplacePublisher"`
				MarketplaceOffer     *string `json:"marketplaceOffer"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Kind != nil {
			a.Kind = *req.Kind
		}
		if req.Tags != nil {
			a.Tags = req.Tags
		}
		if p := req.Properties; p != nil {
			if p.Sku != nil {
				a.Properties.Sku = *p.Sku
			}
			if p.Product != nil {
				a.Properties.Product = *p.Product
			}
			if p.Vendor != nil {
				a.Properties.Vendor = *p.Vendor
			}
			if p.MarketplacePublisher != nil {
				a.Properties.MarketplacePublisher = *p.MarketplacePublisher
			}
			if p.MarketplaceOffer != nil {
				a.Properties.MarketplaceOffer = *p.MarketplaceOffer
			}
		}
		webPremierAddOns.Put(a.ID, a)
		sim.WriteJSON(w, http.StatusOK, a)
	})
	both("DELETE", "/premieraddons/{premierAddOnName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webPremierAddOns.Delete(addonID(r))
		w.WriteHeader(http.StatusOK)
	})
}

// ---- config/pushsettings -------------------------------------------------------

func registerWebPushSettings(both func(string, string, http.HandlerFunc)) {
	// The type spelling matches the other config sub-resources this slice
	// serves (appsettings, metadata, ...): "Microsoft.Web/sites/config" at
	// both the production and the slot level.
	wirePush := func(r *http.Request, props WebPushSettingsProperties, kind string) WebPushSettings {
		return WebPushSettings{
			ID:         webResourceID(r) + "/config/pushsettings",
			Name:       "pushsettings",
			Type:       "Microsoft.Web/sites/config",
			Kind:       kind,
			Properties: props,
		}
	}
	both("PUT", "/config/pushsettings", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebPushSettings
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		stored := wirePush(r, req.Properties, req.Kind)
		webPushSettings.Put(stored.ID, stored)
		sim.WriteJSON(w, http.StatusOK, stored)
	})
	both("POST", "/config/pushsettings/list", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		stored, ok := webPushSettings.Get(webResourceID(r) + "/config/pushsettings")
		if !ok {
			// A site that never configured push reports the disabled default.
			stored = wirePush(r, WebPushSettingsProperties{IsPushEnabled: false}, "")
		}
		sim.WriteJSON(w, http.StatusOK, stored)
	})
}
