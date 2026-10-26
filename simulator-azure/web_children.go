package main

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Four site child families at site and slot level: public certificates, domain
// ownership identifiers, premier add-ons, and config/pushsettings. Each store
// keys by canonical resource ID, /slots/{slot} included, so slot resources are
// their own rows.

// "Microsoft.Web/sites/<child>" on a site, "Microsoft.Web/sites/slots/<child>"
// on a slot.
func webChildType(r *http.Request, child string) string {
	if sim.PathParam(r, "slot") != "" {
		return "Microsoft.Web/sites/slots/" + child
	}
	return "Microsoft.Web/sites/" + child
}

type WebPublicCertificate struct {
	ID         string                         `json:"id,omitempty"`
	Name       string                         `json:"name,omitempty"`
	Type       string                         `json:"type,omitempty"`
	Kind       string                         `json:"kind,omitempty"`
	Properties WebPublicCertificateProperties `json:"properties"`
}

// The DER bytes, the store location, and the thumbprint derived from the blob.
type WebPublicCertificateProperties struct {
	Blob                      []byte `json:"blob,omitempty"`
	PublicCertificateLocation string `json:"publicCertificateLocation,omitempty"`
	Thumbprint                string `json:"thumbprint,omitempty"`
}

var webPublicCertificates sim.Store[WebPublicCertificate]

// The identity string travels as properties.id.
type WebDomainOwnershipIdentifier struct {
	ID         string                                 `json:"id,omitempty"`
	Name       string                                 `json:"name,omitempty"`
	Type       string                                 `json:"type,omitempty"`
	Kind       string                                 `json:"kind,omitempty"`
	Properties WebDomainOwnershipIdentifierProperties `json:"properties"`
}

type WebDomainOwnershipIdentifierProperties struct {
	Value string `json:"id,omitempty"`
}

var webDomainOwnershipIdentifiers sim.Store[WebDomainOwnershipIdentifier]

type WebPremierAddOn struct {
	ID         string                    `json:"id,omitempty"`
	Name       string                    `json:"name,omitempty"`
	Type       string                    `json:"type,omitempty"`
	Kind       string                    `json:"kind,omitempty"`
	Location   string                    `json:"location,omitempty"`
	Tags       map[string]string         `json:"tags,omitempty"`
	Properties WebPremierAddOnProperties `json:"properties"`
}

type WebPremierAddOnProperties struct {
	Sku                  string `json:"sku,omitempty"`
	Product              string `json:"product,omitempty"`
	Vendor               string `json:"vendor,omitempty"`
	MarketplacePublisher string `json:"marketplacePublisher,omitempty"`
	MarketplaceOffer     string `json:"marketplaceOffer,omitempty"`
}

var webPremierAddOns sim.Store[WebPremierAddOn]

type WebPushSettings struct {
	ID         string                    `json:"id,omitempty"`
	Name       string                    `json:"name,omitempty"`
	Type       string                    `json:"type,omitempty"`
	Kind       string                    `json:"kind,omitempty"`
	Properties WebPushSettingsProperties `json:"properties"`
}

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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Properties.Blob) == 0 {
			AzureError(w, "InvalidRequestContent", "The 'properties.blob' property is required.", http.StatusBadRequest)
			return
		}
		// App Service rejects a blob that is not a DER certificate. The thumbprint is
		// the SHA-1 of the DER bytes, as every Azure certificate surface reports.
		if _, err := x509.ParseCertificate(req.Properties.Blob); err != nil {
			AzureError(w, "InvalidRequestContent", "The certificate blob is not a valid X.509 certificate: "+err.Error(), http.StatusBadRequest)
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Domain ownership identifier %q not found.", sim.PathParam(r, "domainOwnershipIdentifierName"))
			return
		}
		// Azure Resource Manager PATCH merges: only keys present change.
		var req struct {
			Kind       *string `json:"kind"`
			Properties *struct {
				Value *string `json:"id"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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

func registerWebPremierAddOns(both func(string, string, http.HandlerFunc)) {
	addonID := func(r *http.Request) string {
		return webResourceID(r) + "/premieraddons/" + sim.PathParam(r, "premierAddOnName")
	}
	// WebApps_ListPremierAddOns declares a single PremierAddOn as its 200 schema,
	// not a collection, so answer the first by resource ID, or an empty object.
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Premier add-on %q not found.", sim.PathParam(r, "premierAddOnName"))
			return
		}
		// Azure Resource Manager PATCH merges: only keys present change.
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
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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

func registerWebPushSettings(both func(string, string, http.HandlerFunc)) {
	// "Microsoft.Web/sites/config" at site and slot level, like the other config
	// sub-resources.
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
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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
