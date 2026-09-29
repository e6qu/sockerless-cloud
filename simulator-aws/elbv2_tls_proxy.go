package main

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
)

// elbv2ListenerTLSConfig builds the TLS configuration an HTTPS or TLS listener
// terminates with, from its AWS Certificate Manager certificates: the first
// certificate that is ISSUED with key material is the default, and the SNI
// certificates are presented to clients that name one of theirs.
func elbv2ListenerTLSConfig(listener ELBv2Listener) (*tls.Config, error) {
	if len(listener.Certificates) == 0 {
		return nil, fmt.Errorf("HTTPS/TLS listener %s has no certificate", listener.Arn)
	}
	var defaultCert *tls.Certificate
	for _, arn := range listener.Certificates {
		if cert, err := elbv2LoadTLSCert(arn); err == nil {
			defaultCert = cert
			break
		}
	}
	if defaultCert == nil {
		return nil, fmt.Errorf("listener %s: none of its certificates are ISSUED with exportable key material", listener.Arn)
	}
	var sniCerts []*tls.Certificate
	for _, arn := range listener.SNICertificates {
		// A certificate not yet ISSUED is not presented, which is what a
		// listener does with one it cannot use.
		if cert, err := elbv2LoadTLSCert(arn); err == nil {
			sniCerts = append(sniCerts, cert)
		}
	}
	return lbplane.ListenerTLSConfig(defaultCert, sniCerts), nil
}

func elbv2LoadTLSCert(arn string) (*tls.Certificate, error) {
	certPEM, keyPEM, ok := acmCertMaterial(arn)
	if !ok {
		return nil, fmt.Errorf("certificate %s is not available for TLS termination", arn)
	}
	cert, err := lbplane.ListenerCertificate([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("load certificate %s: %w", arn, err)
	}
	return cert, nil
}

// elbv2HTTPSListenerHandler serves one HTTPS listener after TLS termination:
// it forwards each decrypted request the way the HTTP front end does.
func elbv2HTTPSListenerHandler(listenerArn string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listener, ok := elbv2Listeners.Get(listenerArn)
		if !ok {
			http.Error(w, "listener no longer exists", http.StatusBadGateway)
			return
		}
		if !wafAssociatedRequestAllowed(listener.LoadBalancerArn, r) {
			http.Error(w, "AWS WAF blocked the request", http.StatusForbidden)
			return
		}
		elbv2ForwardToHealthyTarget(w, r, listener)
	})
}
