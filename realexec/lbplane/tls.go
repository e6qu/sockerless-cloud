package lbplane

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// ListenerCertificate parses a PEM certificate chain and its key into the
// certificate a TLS listener presents, with its leaf parsed so SNI selection
// can read the names it covers.
func ListenerCertificate(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("parse leaf certificate: %w", err)
		}
		cert.Leaf = leaf
	}
	return &cert, nil
}

// ListenerTLSConfig is the server configuration of a TLS-terminating listener:
// it presents defaultCert to a client that sends no server name or one no SNI
// certificate covers, and the SNI certificate whose names match otherwise.
func ListenerTLSConfig(defaultCert *tls.Certificate, sniCerts []*tls.Certificate) *tls.Config {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{*defaultCert},
		MinVersion:   tls.VersionTLS12,
	}
	byName := map[string]*tls.Certificate{}
	for _, cert := range sniCerts {
		if cert.Leaf == nil {
			continue
		}
		for _, name := range cert.Leaf.DNSNames {
			byName[strings.ToLower(name)] = cert
		}
	}
	if len(byName) > 0 {
		cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if cert, ok := byName[strings.ToLower(hello.ServerName)]; ok {
				return cert, nil
			}
			return defaultCert, nil
		}
	}
	return cfg
}

// HTTPSServer terminates TLS on a listener and serves the decrypted requests.
type HTTPSServer struct {
	Address string
	srv     *http.Server
	done    chan struct{}
}

// StartHTTPSServer binds listenAddress, terminates TLS with config, and serves
// handler. It bounds how long a client may take to send its request headers
// and how long a kept-alive connection may idle, never how long an exchange
// lasts: a load balancer streams a long response and carries a WebSocket for
// as long as either end keeps it busy.
func StartHTTPSServer(listenAddress string, config *tls.Config, handler http.Handler, idleTimeout time.Duration) (*HTTPSServer, error) {
	raw, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, err
	}
	s := &HTTPSServer{
		Address: raw.Addr().String(),
		srv: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 30 * time.Second,
			IdleTimeout:       idleTimeout,
		},
		done: make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		// Serve returns only once Close has closed the listener.
		_ = s.srv.Serve(tls.NewListener(raw, config))
	}()
	return s, nil
}

// Close stops the server, closing its connections, and returns once it has
// stopped serving.
func (s *HTTPSServer) Close() error {
	err := s.srv.Close()
	<-s.done
	return err
}
