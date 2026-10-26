package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"strings"
)

// Client certificates through the two front ends. App Service, with client
// certificates enabled, asks for one at TLS and forwards it base64-encoded as
// X-ARR-ClientCert; Required refuses a request presenting none outside the
// exclusion paths, which are not asked. Container Apps ingress forwards it as
// X-Forwarded-Client-Cert (Hash=<SHA-256>;Cert="<PEM>";Chain="<PEM>") in
// accept or require mode, and require refuses a client presenting none. See
// the vendored appservice-tls-mutual-auth.md and
// containerapps-client-certificate-authorization.md.

// A site asks rather than requires at the handshake, because its exclusion
// paths are known only once the request arrives; a container app's require has
// no exclusions, so the handshake refuses.
func azureTLSConfigForClient(hello *tls.ClientHelloInfo, base *tls.Config) (*tls.Config, error) {
	host := strings.ToLower(hello.ServerName)
	if site, ok := appServiceSiteByHost(host); ok {
		if site.Properties.ClientCertEnabled {
			base.ClientAuth = tls.RequestClientCert
			return base, nil
		}
		return nil, nil
	}
	app, ok := acaAppByIngressFqdn(host)
	if !ok {
		rev, found := acaRevisionByFqdn(host)
		if !found {
			return nil, nil
		}
		if app, ok = acaApps.Get(rev.AppID); !ok {
			return nil, nil
		}
	}
	switch acaClientCertificateMode(app) {
	case "accept":
		base.ClientAuth = tls.RequestClientCert
	case "require":
		base.ClientAuth = tls.RequireAnyClientCert
	default:
		return nil, nil
	}
	return base, nil
}

func clientCertificate(r *http.Request) *x509.Certificate {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
}

// A case-insensitive prefix match over paths separated by commas or
// semicolons.
func appServiceClientCertExcluded(site *Site, requestPath string) bool {
	path := strings.ToLower(requestPath)
	for _, prefix := range strings.FieldsFunc(site.Properties.ClientCertExclusionPaths, func(c rune) bool { return c == ',' || c == ';' }) {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix != "" && strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// The site requires one, the request came over TLS, and its path is not
// excluded.
func appServiceClientCertRequired(r *http.Request, site *Site) bool {
	if !site.Properties.ClientCertEnabled || r.TLS == nil || !strings.EqualFold(site.Properties.ClientCertMode, "Required") {
		return false
	}
	return clientCertificate(r) == nil && !appServiceClientCertExcluded(site, r.URL.Path)
}

// When the site asks for one and the path is not excluded.
func appServiceClientCertHeader(r *http.Request, site *Site) string {
	cert := clientCertificate(r)
	if cert == nil || !site.Properties.ClientCertEnabled || appServiceClientCertExcluded(site, r.URL.Path) {
		return ""
	}
	return base64.StdEncoding.EncodeToString(cert.Raw)
}

// ignore when unset.
func acaClientCertificateMode(app ContainerApp) string {
	if app.Properties.Configuration == nil || app.Properties.Configuration.Ingress == nil {
		return "ignore"
	}
	mode := strings.ToLower(app.Properties.Configuration.Ingress.ClientCertificateMode)
	if mode == "" {
		return "ignore"
	}
	return mode
}

// The SHA-256 thumbprint, the certificate in URL-encoded PEM, and the client's
// intermediates likewise.
func acaClientCertHeader(r *http.Request, app ContainerApp) string {
	cert := clientCertificate(r)
	if cert == nil || acaClientCertificateMode(app) == "ignore" {
		return ""
	}
	hash := sha256.Sum256(cert.Raw)
	value := "Hash=" + hex.EncodeToString(hash[:]) + `;Cert="` + percentEncode(pemCertificate(cert)) + `"`
	if chain := r.TLS.PeerCertificates[1:]; len(chain) > 0 {
		var intermediates strings.Builder
		for _, c := range chain {
			intermediates.WriteString(pemCertificate(c))
		}
		value += `;Chain="` + percentEncode(intermediates.String()) + `"`
	}
	return value
}

func pemCertificate(cert *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

// As the ingress's Envoy encodes the PEM: the header's delimiters (; = , ") and
// the PEM's newlines, pluses and slashes travel escaped.
func percentEncode(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			out.WriteByte(c)
		default:
			out.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return out.String()
}
