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

// Client certificates through the two front ends. App Service terminates TLS
// at its front end and, with client certificates enabled on a site, asks the
// client for one and forwards it to the app as X-ARR-ClientCert, the
// certificate base64-encoded; Required refuses a request that presents none
// outside the site's exclusion paths, and an excluded path is not asked for
// one. Container Apps ingress forwards the certificate as
// X-Forwarded-Client-Cert when the mode is accept or require, as
// Hash=<SHA-256>;Cert="<PEM>";Chain="<PEM>", and require refuses a client
// that presents none. Both are documented in the vendored pages
// appservice-tls-mutual-auth.md and containerapps-client-certificate-authorization.md.

// azureTLSConfigForClient asks the client for a certificate when the
// hostname it names belongs to a site with client certificates enabled or to
// a container app whose ingress accepts or requires one. A site is asked
// rather than required at the handshake, because its exclusion paths are
// known only once the request arrives; a container app's require has no
// exclusions, so the handshake itself refuses a client without one.
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

// clientCertificate is the certificate the client presented on the TLS
// connection the request arrived on, if any.
func clientCertificate(r *http.Request) *x509.Certificate {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
}

// appServiceClientCertExcluded reports whether the request's path is one of
// the site's clientCertExclusionPaths: a prefix match, case-insensitive, over
// the paths the setting separates with commas or semicolons.
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

// appServiceClientCertRequired reports whether the front end refuses the
// request for presenting no client certificate: the site requires one, the
// request came over TLS, and its path is not excluded.
func appServiceClientCertRequired(r *http.Request, site *Site) bool {
	if !site.Properties.ClientCertEnabled || r.TLS == nil || !strings.EqualFold(site.Properties.ClientCertMode, "Required") {
		return false
	}
	return clientCertificate(r) == nil && !appServiceClientCertExcluded(site, r.URL.Path)
}

// appServiceClientCertHeader is the X-ARR-ClientCert value the front end
// forwards: the client certificate base64-encoded, when the site asks for one
// and the path is not excluded.
func appServiceClientCertHeader(r *http.Request, site *Site) string {
	cert := clientCertificate(r)
	if cert == nil || !site.Properties.ClientCertEnabled || appServiceClientCertExcluded(site, r.URL.Path) {
		return ""
	}
	return base64.StdEncoding.EncodeToString(cert.Raw)
}

// acaClientCertificateMode is the ingress's clientCertificateMode, ignore
// when it sets none.
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

// acaClientCertHeader is the X-Forwarded-Client-Cert value the ingress
// forwards when it accepts or requires a certificate and the client presented
// one: the certificate's SHA-256 thumbprint, the certificate in URL-encoded
// PEM, and the intermediates the client sent with it, likewise.
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

// percentEncode encodes every byte but the unreserved ones, as the ingress's
// Envoy encodes the PEM it puts in X-Forwarded-Client-Cert: the header's own
// delimiters (; = , ") and the PEM's newlines, pluses and slashes all travel
// escaped.
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
