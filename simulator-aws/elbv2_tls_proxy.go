package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ELBv2 HTTPS / TLS listeners terminate the listener certificate(s) at the load
// balancer and forward the decrypted stream to targets — the path an Application
// Load Balancer HTTPS listener (TLS → HTTP to the target) and a Network Load
// Balancer TLS listener (TLS → TCP to the target) take. The sim realizes that
// by binding a real TLS listener on the load balancer's stable host at the
// listener port: the listener's ACM certificates are loaded into a
// tls.Config (with SNI selection across the default + SNI certs), the listener
// is wrapped with tls.NewListener so the handshake is terminated in-process, and
// the decrypted stream is then forwarded — HTTP (https.Serve) for an HTTPS
// listener, raw TCP (io.Copy both directions) for a TLS listener — to a healthy
// target, exactly as on real AWS.
//
// A client reaches the data plane the same way it reaches an NLB stream
// listener: resolve the load balancer's AWS-shaped DNS name to the stable proxy
// host (elbv2NLBHostEntries) and connect on the listener port — here over TLS.

var (
	elbv2TLSProxies = map[string]*elbv2TLSProxy{}
)

// elbv2TLSProxy pairs the running TLS listener with the load balancer it serves
// so teardown can release the stable-host lease once the last listener for the
// load balancer goes away. For an HTTPS listener srv serves decrypted HTTP; for
// a TLS listener srv is nil and acceptLoop forwards the decrypted byte stream.
type elbv2TLSProxy struct {
	ln    net.Listener
	srv   *http.Server
	lbArn string
	done  chan struct{}
	once  sync.Once
	tls   bool // true for a TLS (raw-TCP-after-termination) listener
}

// elbv2ListenerIsTLS reports whether a listener terminates TLS at the load
// balancer (HTTPS for an ALB, TLS for an NLB). Such listeners require
// certificate(s) and a real TLS handshake; the sim binds a real TLS listener
// for them rather than the plain-HTTP / raw-TCP paths used by HTTP and stream
// listeners.
func elbv2ListenerIsTLS(listener ELBv2Listener) bool {
	switch strings.ToUpper(listener.Protocol) {
	case "HTTPS", "TLS":
		return true
	default:
		return false
	}
}

// elbv2StartTLSProxy binds a real TLS listener for an HTTPS / TLS listener and
// forwards every accepted (and TLS-terminated) connection to a healthy
// registered target, chosen at request time so target (de)registration and
// health are honored. HTTPS serves decrypted HTTP (forwarded to the target over
// plain HTTP, matching real AWS where an HTTPS listener fronts HTTP targets);
// TLS forwards the decrypted byte stream to the target. Idempotent per listener
// ARN.
func elbv2StartTLSProxy(listener ELBv2Listener) error {
	if !elbv2ListenerIsTLS(listener) {
		return nil
	}
	tlsCert, err := elbv2BuildTLSCertificate(listener)
	if err != nil {
		return err
	}
	elbv2NLBProxyMu.Lock()
	defer elbv2NLBProxyMu.Unlock()
	if _, ok := elbv2TLSProxies[listener.Arn]; ok {
		return nil
	}
	host, err := elbv2AcquireStableHost(listener.LoadBalancerArn, listener.Port)
	if err != nil {
		return err
	}
	bindAddr := net.JoinHostPort(host.host, strconv.Itoa(listener.Port))
	raw, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return fmt.Errorf("bind TLS listener %s on %s: %w", listener.Arn, bindAddr, err)
	}
	tlsConfig := elbv2TLSConfig(listener, tlsCert)
	tlsListener := tls.NewListener(raw, tlsConfig)
	elbv2NLBHosts[listener.LoadBalancerArn] = host
	p := &elbv2TLSProxy{
		ln:    tlsListener,
		lbArn: listener.LoadBalancerArn,
		done:  make(chan struct{}),
		tls:   strings.EqualFold(listener.Protocol, "TLS"),
	}
	if p.tls {
		go p.serveStream()
	} else {
		p.srv = &http.Server{
			Handler:      http.HandlerFunc(elbv2TLSHTTPSHandler(listener.Arn)),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		}
		go func() {
			_ = p.srv.Serve(tlsListener)
			close(p.done)
		}()
	}
	elbv2TLSProxies[listener.Arn] = p
	return nil
}

// elbv2TLSProxyHostPort returns the host of the bound TLS listener for a given
// listener ARN, read back from the bound socket so the advertised address can
// never drift from where the proxy really listens. Empty if no TLS proxy is
// running for the listener.
func elbv2TLSProxyHostPort(listenerArn string) string {
	elbv2NLBProxyMu.Lock()
	defer elbv2NLBProxyMu.Unlock()
	entry := elbv2TLSProxies[listenerArn]
	if entry == nil {
		return ""
	}
	addr := entry.ln.Addr().String()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return host
}

// elbv2StopTLSProxy closes and forgets the TLS listener for a listener (on
// DeleteListener / DeleteLoadBalancer / ModifyListener restart). No-op if none
// is running.
func elbv2StopTLSProxy(listenerArn string) {
	elbv2NLBProxyMu.Lock()
	entry := elbv2TLSProxies[listenerArn]
	delete(elbv2TLSProxies, listenerArn)
	if entry != nil {
		elbv2ReleaseNLBHostIfUnused(entry.lbArn)
	}
	elbv2NLBProxyMu.Unlock()
	if entry != nil {
		entry.once.Do(func() {
			if entry.srv != nil {
				_ = entry.srv.Close()
			} else {
				_ = entry.ln.Close()
			}
			<-entry.done
		})
	}
}

// elbv2BuildTLSCertificate loads the listener's default certificate(s) from the
// ACM store into a tls.Certificate. At least one ISSUED certificate with key
// material is required; the first reachable one is the default.
func elbv2BuildTLSCertificate(listener ELBv2Listener) (*tls.Certificate, error) {
	for _, arn := range listener.Certificates {
		cert, err := elbv2LoadTLSCert(arn)
		if err == nil {
			return cert, nil
		}
	}
	if len(listener.Certificates) == 0 {
		return nil, fmt.Errorf("HTTPS/TLS listener %s has no certificate", listener.Arn)
	}
	return nil, fmt.Errorf("listener %s: none of its certificates are ISSUED with exportable key material", listener.Arn)
}

// elbv2LoadTLSCert builds a tls.Certificate from an ACM certificate ARN's PEM
// material, parsing the leaf so SNI selection can read its DNS names.
func elbv2LoadTLSCert(arn string) (*tls.Certificate, error) {
	certPEM, keyPEM, ok := acmCertMaterial(arn)
	if !ok {
		return nil, fmt.Errorf("certificate %s is not available for TLS termination", arn)
	}
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("load certificate %s: %w", arn, err)
	}
	if cert.Leaf == nil {
		for _, block := range parsePEMBlocks([]byte(certPEM)) {
			parsed, perr := x509.ParseCertificate(block)
			if perr == nil {
				cert.Leaf = parsed
				break
			}
		}
	}
	return &cert, nil
}

// elbv2TLSConfig assembles the tls.Config for a listener: the default
// certificate is presented to clients that don't send SNI; GetCertificate
// selects the SNI-matching cert across the default + SNI certificates when the
// client sends SNI.
func elbv2TLSConfig(listener ELBv2Listener, defaultCert *tls.Certificate) *tls.Config {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{*defaultCert},
		MinVersion:   tls.VersionTLS12,
	}
	sniCerts := make(map[string]*tls.Certificate, len(listener.SNICertificates))
	for _, arn := range listener.SNICertificates {
		c, err := elbv2LoadTLSCert(arn)
		if err != nil {
			continue
		}
		for _, name := range c.Leaf.DNSNames {
			sniCerts[strings.ToLower(name)] = c
		}
	}
	if len(sniCerts) > 0 {
		cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello.ServerName != "" {
				if c, ok := sniCerts[strings.ToLower(hello.ServerName)]; ok {
					return c, nil
				}
			}
			return defaultCert, nil
		}
	}
	return cfg
}

// elbv2TLSHTTPSHandler returns the http.Handler that serves a single HTTPS
// listener after TLS termination: find the listener by ARN, pick a healthy
// target, and forward the (now-plain-HTTP) request to it — the same forward
// path the HTTP data plane uses.
func elbv2TLSHTTPSHandler(listenerArn string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		listener, ok := elbv2Listeners.Get(listenerArn)
		if !ok {
			http.Error(w, "listener no longer exists", http.StatusBadGateway)
			return
		}
		tg, target, ok := elbv2HealthyTargetForListener(r.Context(), listener)
		if !ok {
			http.Error(w, "no healthy targets", http.StatusServiceUnavailable)
			return
		}
		address, err := elbv2TargetAddress(tg, target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err := elbv2ProxyHTTPRequest(w, r, listener, tg, address); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
}

// serveStream serves a TLS (NLB) listener: accept a TLS-terminated connection
// and pipe the decrypted byte stream bidirectionally to a healthy target, the
// stream analogue of the NLB raw-TCP proxy but with the handshake terminated.
func (p *elbv2TLSProxy) serveStream() {
	defer close(p.done)
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handleStream(conn)
	}
}

func (p *elbv2TLSProxy) handleStream(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	if tlsConn, ok := client.(*tls.Conn); ok {
		if err := tlsConn.HandshakeContext(context.Background()); err != nil {
			return
		}
	}
	_ = client.SetDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listenerArn := p.listenerArn()
	current, ok := elbv2Listeners.Get(listenerArn)
	if !ok {
		return
	}
	tg, target, ok := elbv2HealthyTargetForListener(ctx, current)
	if !ok {
		return
	}
	address, err := elbv2TargetAddress(tg, target)
	if err != nil {
		return
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	upstream, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return
	}
	defer upstream.Close()
	errs := make(chan error, 2)
	go func() {
		_, err := io.Copy(upstream, client)
		errs <- err
	}()
	go func() {
		_, err := io.Copy(client, upstream)
		errs <- err
	}()
	<-errs
}

// listenerArn recovers the listener ARN for this TLS proxy by looking it up in
// the registry; serveStream is invoked only after the proxy is registered, so
// the ARN is always present.
func (p *elbv2TLSProxy) listenerArn() string {
	elbv2NLBProxyMu.Lock()
	defer elbv2NLBProxyMu.Unlock()
	for arn, entry := range elbv2TLSProxies {
		if entry == p {
			return arn
		}
	}
	return ""
}

// parsePEMBlocks returns the DER bytes of every CERTIFICATE block in data.
func parsePEMBlocks(data []byte) [][]byte {
	var blocks [][]byte
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return blocks
		}
		if block.Type == "CERTIFICATE" {
			blocks = append(blocks, block.Bytes)
		}
	}
}
