package main

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// web_certificates.go implements the Microsoft.Web SSL certificate surfaces:
// the top-level Microsoft.Web/certificates ARM resource (Certificates_List /
// ListByResourceGroup / Get / CreateOrUpdate / Update / Delete) and the
// site-scoped Microsoft.Web/sites[/slots]/certificates child resource
// (SiteCertificates_*). Every derived member — thumbprint, subject, issuer,
// SANs, validity dates — comes from the uploaded certificate
// itself: the PFX (PKCS#12) or PEM payload is parsed for real, exactly as
// App Service parses an `az webapp config ssl upload`. A Key Vault-sourced
// certificate (keyVaultId + keyVaultSecretName) resolves the secret against
// the simulator's own Key Vault stores and reports the swagger's
// KeyVaultSecretStatus truthfully from what that lookup finds.

// WebCertificate mirrors the swagger Certificate resource (a TrackedResource:
// id/name/type/location/tags plus kind). PfxBlob and Password hold the
// request's x-ms-secret members: write-only on the wire (never serialized
// into a response), retained across restarts through the persistence
// sidecar via `json:"-"` — which only rides top-level fields, so they live
// here rather than inside Properties.
type WebCertificate struct {
	ID         string                   `json:"id,omitempty"`
	Name       string                   `json:"name,omitempty"`
	Type       string                   `json:"type,omitempty"`
	Kind       string                   `json:"kind,omitempty"`
	Location   string                   `json:"location"`
	Tags       map[string]string        `json:"tags,omitempty"`
	Properties WebCertificateProperties `json:"properties"`
	PfxBlob    []byte                   `json:"-"`
	Password   string                   `json:"-"`
}

// WebCertificateProperties mirrors the swagger CertificateProperties (the
// members the simulator serves; the request's write-only pfxBlob/password
// pair rides WebCertificate's hidden top-level fields).
type WebCertificateProperties struct {
	SubjectName            string   `json:"subjectName,omitempty"`
	HostNames              []string `json:"hostNames,omitempty"`
	SiteName               string   `json:"siteName,omitempty"`
	Issuer                 string   `json:"issuer,omitempty"`
	IssueDate              string   `json:"issueDate,omitempty"`
	ExpirationDate         string   `json:"expirationDate,omitempty"`
	Thumbprint             string   `json:"thumbprint,omitempty"`
	Valid                  *bool    `json:"valid,omitempty"`
	KeyVaultID             string   `json:"keyVaultId,omitempty"`
	KeyVaultSecretName     string   `json:"keyVaultSecretName,omitempty"`
	KeyVaultSecretStatus   string   `json:"keyVaultSecretStatus,omitempty"`
	ServerFarmID           string   `json:"serverFarmId,omitempty"`
	CanonicalName          string   `json:"canonicalName,omitempty"`
	DomainValidationMethod string   `json:"domainValidationMethod,omitempty"`
}

var (
	webCertificates     sim.Store[WebCertificate]
	webSiteCertificates sim.Store[WebCertificate]
)

// webCertificatePutRequest is the decode shape for Certificate
// CreateOrUpdate / Update bodies. It exists because pfxBlob and password are
// x-ms-secret (write-only): the stored struct hides them from JSON entirely,
// so the request needs its own view that can still read them.
type webCertificatePutRequest struct {
	Kind       string            `json:"kind"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags"`
	Properties struct {
		Password               string   `json:"password"`
		PfxBlob                []byte   `json:"pfxBlob"`
		HostNames              []string `json:"hostNames"`
		KeyVaultID             string   `json:"keyVaultId"`
		KeyVaultSecretName     string   `json:"keyVaultSecretName"`
		ServerFarmID           string   `json:"serverFarmId"`
		CanonicalName          string   `json:"canonicalName"`
		DomainValidationMethod string   `json:"domainValidationMethod"`
	} `json:"properties"`
}

// parseWebCertificatePayload extracts the leaf X.509 certificate from an
// uploaded payload: a PKCS#12 (PFX) archive protected by password, a PEM
// bundle, or bare DER bytes — the three encodings real App Service accepts.
func parseWebCertificatePayload(blob []byte, password string) (*x509.Certificate, error) {
	_, leaf, _, pfxErr := pkcs12.DecodeChain(blob, password)
	if pfxErr == nil && leaf != nil {
		return leaf, nil
	}
	rest := blob
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
	if leaf, derErr := x509.ParseCertificate(blob); derErr == nil {
		return leaf, nil
	}
	return nil, fmt.Errorf("the certificate payload is not a valid PKCS#12 archive, PEM bundle, or DER certificate: %v", pfxErr)
}

// applyX509ToWebCertificate fills the certificate resource's derived members
// from the parsed leaf certificate: SHA-1 thumbprint over the DER bytes (the
// fingerprint every Azure certificate surface reports), subject and issuer
// common names, the SAN host list and the validity window. The cerBlob
// member stays absent, as real Microsoft.Web/certificates reads answer —
// `az webapp config ssl` output would otherwise be undecodable for the CLI.
func applyX509ToWebCertificate(p *WebCertificateProperties, leaf *x509.Certificate) {
	sum := sha1.Sum(leaf.Raw)
	p.Thumbprint = strings.ToUpper(hex.EncodeToString(sum[:]))
	p.SubjectName = leaf.Subject.CommonName
	p.Issuer = leaf.Issuer.CommonName
	p.IssueDate = leaf.NotBefore.UTC().Format(time.RFC3339)
	p.ExpirationDate = leaf.NotAfter.UTC().Format(time.RFC3339)
	now := time.Now()
	valid := now.After(leaf.NotBefore) && now.Before(leaf.NotAfter)
	p.Valid = &valid
	if len(p.HostNames) == 0 {
		if len(leaf.DNSNames) > 0 {
			p.HostNames = leaf.DNSNames
		} else if leaf.Subject.CommonName != "" {
			p.HostNames = []string{leaf.Subject.CommonName}
		}
	}
}

// resolveKeyVaultCertificate resolves a keyVaultId/keyVaultSecretName-sourced
// certificate against the simulator's own Key Vault stores and reports the
// swagger's KeyVaultSecretStatus from what the lookup actually finds:
//
//	KeyVaultDoesNotExist          — no vault at that resource ID
//	OperationNotPermittedOnKeyVault — vault grants no access policy with
//	                                 secret "get" permission
//	KeyVaultSecretDoesNotExist    — vault exists, secret does not
//	Succeeded                     — secret resolved and parsed as a certificate
//	Initialized                   — link recorded but the secret's material
//	                                 could not be realized as a certificate yet
func resolveKeyVaultCertificate(p *WebCertificateProperties) {
	vault, ok := webCertLookupVault(p.KeyVaultID)
	if !ok {
		p.KeyVaultSecretStatus = "KeyVaultDoesNotExist"
		return
	}
	if !webCertVaultPermitsSecretGet(vault) {
		p.KeyVaultSecretStatus = "OperationNotPermittedOnKeyVault"
		return
	}
	rec, ok := keyVaultData.Get(keyVaultSecretKey(vault.Name, p.KeyVaultSecretName))
	if !ok || rec.isDeleted() || len(rec.Versions) == 0 {
		p.KeyVaultSecretStatus = "KeyVaultSecretDoesNotExist"
		return
	}
	value := rec.latest().Value
	// A Key Vault certificate's secret carries the full PKCS#12 archive
	// base64-encoded (content type application/x-pkcs12) or as PEM text.
	payload := []byte(value)
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value)); err == nil {
		payload = decoded
	}
	leaf, err := parseWebCertificatePayload(payload, "")
	if err != nil {
		p.KeyVaultSecretStatus = "Initialized"
		return
	}
	applyX509ToWebCertificate(p, leaf)
	p.KeyVaultSecretStatus = "Succeeded"
}

// webCertLookupVault finds the Key Vault addressed by an ARM resource ID,
// matching case-insensitively because clients vary the provider-namespace
// casing in hand-built IDs.
func webCertLookupVault(keyVaultID string) (KeyVault, bool) {
	if v, ok := keyVaults.Get(keyVaultID); ok {
		return v, true
	}
	for _, v := range keyVaults.List() {
		if strings.EqualFold(v.ID, keyVaultID) {
			return v, true
		}
	}
	return KeyVault{}, false
}

// webCertVaultPermitsSecretGet reports whether any of the vault's access
// policies grants the secret "get" verb — the permission App Service needs to
// pull the certificate material. A vault with RBAC authorization enabled
// delegates to Azure RBAC instead of access policies, which the simulator's
// single-principal model always satisfies.
func webCertVaultPermitsSecretGet(v KeyVault) bool {
	if v.Properties.EnableRbacAuthorization {
		return true
	}
	for _, pol := range v.Properties.AccessPolicies {
		for _, verb := range pol.Permissions.Secrets {
			if strings.EqualFold(verb, "get") {
				return true
			}
		}
	}
	return false
}

// buildWebCertificateProperties assembles the stored properties from a
// CreateOrUpdate/Update request: a PFX/PEM payload is parsed for its real
// members, a Key Vault reference is resolved against the vault stores. The
// returned message is non-empty when the request is invalid (HTTP 400).
func buildWebCertificateProperties(req webCertificatePutRequest) (WebCertificateProperties, string) {
	p := WebCertificateProperties{
		HostNames:              req.Properties.HostNames,
		KeyVaultID:             req.Properties.KeyVaultID,
		KeyVaultSecretName:     req.Properties.KeyVaultSecretName,
		ServerFarmID:           req.Properties.ServerFarmID,
		CanonicalName:          req.Properties.CanonicalName,
		DomainValidationMethod: req.Properties.DomainValidationMethod,
	}
	switch {
	case len(req.Properties.PfxBlob) > 0:
		leaf, err := parseWebCertificatePayload(req.Properties.PfxBlob, req.Properties.Password)
		if err != nil {
			return p, err.Error()
		}
		applyX509ToWebCertificate(&p, leaf)
	case req.Properties.KeyVaultID != "" && req.Properties.KeyVaultSecretName != "":
		resolveKeyVaultCertificate(&p)
	default:
		return p, "Either the 'properties.pfxBlob' payload or the 'properties.keyVaultId' and 'properties.keyVaultSecretName' pair is required."
	}
	return p, ""
}

func registerWebCertificates(srv *sim.Server) {
	webCertificates = sim.MakeStore[WebCertificate](srv.DB(), "web_certificates")
	webSiteCertificates = sim.MakeStore[WebCertificate](srv.DB(), "web_site_certificates")

	certID := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/certificates/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	}
	writeList := func(w http.ResponseWriter, prefix string) {
		out := webCertificates.Filter(func(c WebCertificate) bool { return strings.HasPrefix(c.ID, prefix) })
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		if out == nil {
			out = []WebCertificate{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}

	// Certificates_List — every certificate in the subscription.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/certificates", func(w http.ResponseWriter, r *http.Request) {
		writeList(w, "/subscriptions/"+sim.PathParam(r, "subscriptionId")+"/")
	})

	// Certificates_ListByResourceGroup.
	srv.HandleFunc("GET "+webProvider+"/certificates", func(w http.ResponseWriter, r *http.Request) {
		writeList(w, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/certificates/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName")))
	})

	// Certificates_Get.
	srv.HandleFunc("GET "+webProvider+"/certificates/{name}", func(w http.ResponseWriter, r *http.Request) {
		c, ok := webCertificates.Get(certID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/certificates/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "name"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, c)
	})

	// Certificates_CreateOrUpdate.
	srv.HandleFunc("PUT "+webProvider+"/certificates/{name}", func(w http.ResponseWriter, r *http.Request) {
		var req webCertificatePutRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		props, msg := buildWebCertificateProperties(req)
		if msg != "" {
			sim.AzureError(w, "InvalidRequestContent", msg, http.StatusBadRequest)
			return
		}
		cert := WebCertificate{
			ID:         certID(r),
			Name:       sim.PathParam(r, "name"),
			Type:       "Microsoft.Web/certificates",
			Kind:       req.Kind,
			Location:   req.Location,
			Tags:       req.Tags,
			Properties: props,
			PfxBlob:    req.Properties.PfxBlob,
			Password:   req.Properties.Password,
		}
		webCertificates.Put(cert.ID, cert)
		sim.WriteJSON(w, http.StatusOK, cert)
	})

	// Certificates_Update — PATCH with CertificatePatchResource. A new
	// pfxBlob or Key Vault reference re-derives the certificate members;
	// members absent from the patch stay as stored.
	srv.HandleFunc("PATCH "+webProvider+"/certificates/{name}", func(w http.ResponseWriter, r *http.Request) {
		cert, ok := webCertificates.Get(certID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/certificates/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "name"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		updated, msg := patchWebCertificate(r, cert)
		if msg != "" {
			sim.AzureError(w, "InvalidRequestContent", msg, http.StatusBadRequest)
			return
		}
		webCertificates.Put(updated.ID, updated)
		sim.WriteJSON(w, http.StatusOK, updated)
	})

	// Certificates_Delete — 200 when deleted, 204 when it never existed.
	srv.HandleFunc("DELETE "+webProvider+"/certificates/{name}", func(w http.ResponseWriter, r *http.Request) {
		if webCertificates.Delete(certID(r)) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// patchWebCertificate applies a CertificatePatchResource body on top of the
// stored certificate: kind merges, and a payload or Key Vault reference in
// the patch re-runs the same derivation as a create.
func patchWebCertificate(r *http.Request, cert WebCertificate) (WebCertificate, string) {
	var req webCertificatePutRequest
	if err := sim.ReadJSON(r, &req); err != nil {
		return cert, err.Error()
	}
	if req.Kind != "" {
		cert.Kind = req.Kind
	}
	if len(req.Properties.PfxBlob) > 0 || (req.Properties.KeyVaultID != "" && req.Properties.KeyVaultSecretName != "") {
		props, msg := buildWebCertificateProperties(req)
		if msg != "" {
			return cert, msg
		}
		cert.Properties = props
		cert.PfxBlob = req.Properties.PfxBlob
		cert.Password = req.Properties.Password
	} else if len(req.Properties.HostNames) > 0 {
		cert.Properties.HostNames = req.Properties.HostNames
	}
	return cert, ""
}

// registerWebSiteCertificates wires the SiteCertificates_* operations — the
// same certificate parsing, keyed under the site or slot resource.
func registerWebSiteCertificates(both func(string, string, http.HandlerFunc)) {
	certID := func(r *http.Request) string {
		return webResourceID(r) + "/certificates/" + sim.PathParam(r, "certificateName")
	}
	both("GET", "/certificates", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/certificates/"
		out := webSiteCertificates.Filter(func(c WebCertificate) bool { return strings.HasPrefix(c.ID, prefix) })
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		if out == nil {
			out = []WebCertificate{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/certificates/{certificateName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		c, ok := webSiteCertificates.Get(certID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Certificate %q not found.", sim.PathParam(r, "certificateName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, c)
	})
	both("PUT", "/certificates/{certificateName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req webCertificatePutRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		props, msg := buildWebCertificateProperties(req)
		if msg != "" {
			sim.AzureError(w, "InvalidRequestContent", msg, http.StatusBadRequest)
			return
		}
		site, _ := webResource(r)
		location := req.Location
		if location == "" {
			location = site.Location
		}
		props.SiteName = site.Name
		cert := WebCertificate{
			ID:         certID(r),
			Name:       sim.PathParam(r, "certificateName"),
			Type:       webChildType(r, "certificates"),
			Kind:       req.Kind,
			Location:   location,
			Tags:       req.Tags,
			Properties: props,
			PfxBlob:    req.Properties.PfxBlob,
			Password:   req.Properties.Password,
		}
		webSiteCertificates.Put(cert.ID, cert)
		sim.WriteJSON(w, http.StatusOK, cert)
	})
	both("PATCH", "/certificates/{certificateName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		cert, ok := webSiteCertificates.Get(certID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Certificate %q not found.", sim.PathParam(r, "certificateName"))
			return
		}
		updated, msg := patchWebCertificate(r, cert)
		if msg != "" {
			sim.AzureError(w, "InvalidRequestContent", msg, http.StatusBadRequest)
			return
		}
		webSiteCertificates.Put(updated.ID, updated)
		sim.WriteJSON(w, http.StatusOK, updated)
	})
	both("DELETE", "/certificates/{certificateName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		if webSiteCertificates.Delete(certID(r)) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
