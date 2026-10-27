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

	"github.com/e6qu/sockerless-cloud/sim"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// Microsoft.Web/certificates and the site-scoped certificates child. Every
// derived member — thumbprint, subject, issuer, SANs, validity — comes from
// parsing the uploaded PFX or PEM, as App Service parses `az webapp config ssl
// upload`. A Key Vault-sourced certificate resolves against the simulator's Key
// Vault stores, and KeyVaultSecretStatus reports what that lookup finds.

// PfxBlob and Password are the x-ms-secret members: never serialized, kept
// across restarts by the persistence sidecar, which carries only top-level
// json:"-" fields.
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

// The stored struct hides the x-ms-secret pfxBlob and password from JSON, so
// requests need their own view.
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

// A password-protected PKCS#12 archive, a PEM bundle, or bare DER: the three
// encodings App Service accepts.
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

// SHA-1 thumbprint over the DER bytes (what every Azure certificate surface
// reports), subject and issuer common names, SAN hosts and validity. cerBlob
// stays absent, as Azure's reads answer; the CLI cannot decode `az webapp config
// ssl` output otherwise.
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

// KeyVaultSecretStatus from what the lookup finds:
//
//	KeyVaultDoesNotExist          — no vault at that resource ID
//	OperationNotPermittedOnKeyVault — vault does not grant App Service's
//	                                 service principal the secret get
//	                                 permission
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
	if !webCertVaultPermitsSecretGet(vault, p.KeyVaultSecretName) {
		p.KeyVaultSecretStatus = "OperationNotPermittedOnKeyVault"
		return
	}
	rec, ok := keyVaultData.Get(keyVaultSecretKey(vault.Name, p.KeyVaultSecretName))
	if !ok || rec.isDeleted() || len(rec.Versions) == 0 {
		p.KeyVaultSecretStatus = "KeyVaultSecretDoesNotExist"
		return
	}
	value := rec.latest().Value
	// The secret carries the PKCS#12 archive base64-encoded
	// (application/x-pkcs12) or PEM text.
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

// Case-insensitive, because clients vary the namespace casing in hand-built
// IDs.
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

// App Service reads the secret as its first-party service principal, which the
// vault's access policies or Azure RBAC must grant secret get itself.
func webCertVaultPermitsSecretGet(v KeyVault, secretName string) bool {
	return keyVaultGrantsSecretGet(v, appServiceSPObjectID, secretName)
}

// The message is non-empty for an invalid request (400).
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
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "certificates", sim.PathParam(r, "name"))
	}
	writeList := func(w http.ResponseWriter, prefix string) {
		out := webCertificates.Filter(func(c WebCertificate) bool { return strings.HasPrefix(c.ID, prefix) })
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		if out == nil {
			out = []WebCertificate{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/certificates", func(w http.ResponseWriter, r *http.Request) {
		writeList(w, armSubscriptionID(sim.PathParam(r, "subscriptionId"))+"/")
	})

	srv.HandleFunc("GET "+webProvider+"/certificates", func(w http.ResponseWriter, r *http.Request) {
		writeList(w, armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "certificates")+"/")
	})

	srv.HandleFunc("GET "+webProvider+"/certificates/{name}", func(w http.ResponseWriter, r *http.Request) {
		c, ok := webCertificates.Get(certID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/certificates/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "name"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, c)
	})

	srv.HandleFunc("PUT "+webProvider+"/certificates/{name}", func(w http.ResponseWriter, r *http.Request) {
		var req webCertificatePutRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		props, msg := buildWebCertificateProperties(req)
		if msg != "" {
			AzureError(w, "InvalidRequestContent", msg, http.StatusBadRequest)
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

	// A new pfxBlob or Key Vault reference re-derives the members; absent members
	// stay.
	srv.HandleFunc("PATCH "+webProvider+"/certificates/{name}", func(w http.ResponseWriter, r *http.Request) {
		cert, ok := webCertificates.Get(certID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/certificates/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "name"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		updated, msg := patchWebCertificate(r, cert)
		if msg != "" {
			AzureError(w, "InvalidRequestContent", msg, http.StatusBadRequest)
			return
		}
		webCertificates.Put(updated.ID, updated)
		sim.WriteJSON(w, http.StatusOK, updated)
	})

	// 200 when deleted, 204 when it never existed.
	srv.HandleFunc("DELETE "+webProvider+"/certificates/{name}", func(w http.ResponseWriter, r *http.Request) {
		if webCertificates.Delete(certID(r)) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// kind merges, and a payload or Key Vault reference re-runs the create's
// derivation.
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

// Keyed under the site or slot.
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		props, msg := buildWebCertificateProperties(req)
		if msg != "" {
			AzureError(w, "InvalidRequestContent", msg, http.StatusBadRequest)
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Certificate %q not found.", sim.PathParam(r, "certificateName"))
			return
		}
		updated, msg := patchWebCertificate(r, cert)
		if msg != "" {
			AzureError(w, "InvalidRequestContent", msg, http.StatusBadRequest)
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
