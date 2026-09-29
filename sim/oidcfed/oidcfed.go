// Package oidcfed verifies tokens an external OpenID Connect issuer signed,
// the step every cloud's workload identity federation takes before it checks
// the token against its own trust configuration.
package oidcfed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// discoveryTimeout bounds each discovery and key-set fetch, so an issuer that
// never answers cannot stall a token exchange.
const discoveryTimeout = 10 * time.Second

// Verifiers caches one verifier per issuer. Discovery metadata and the remote
// key set are issuer configuration, not request state, so a cloud reuses them
// across exchanges; the verifier still checks every token's signature, issuer
// and expiry.
type Verifiers struct {
	client  *http.Client
	mu      sync.Mutex
	entries map[string]*discovery
}

type discovery struct {
	done     chan struct{}
	verifier *oidc.IDTokenVerifier
	err      error
}

func New() *Verifiers {
	return &Verifiers{
		client:  &http.Client{Timeout: discoveryTimeout},
		entries: map[string]*discovery{},
	}
}

// Verifier returns the verifier for one exact issuer, discovering it on first
// use. Concurrent first callers share one discovery. The provider outlives the
// request that triggered it and refetches its key set through the context it
// was built with, so it is built on a background context: a canceled first
// caller must not break every later verification. ctx bounds only this
// caller's wait. The verifier skips the client-id check because each cloud
// matches audiences against its own trust configuration.
func (v *Verifiers) Verifier(ctx context.Context, issuer string) (*oidc.IDTokenVerifier, error) {
	v.mu.Lock()
	entry, ok := v.entries[issuer]
	if !ok {
		entry = &discovery{done: make(chan struct{})}
		v.entries[issuer] = entry
		go v.discover(issuer, entry)
	}
	v.mu.Unlock()
	select {
	case <-entry.done:
		return entry.verifier, entry.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (v *Verifiers) discover(issuer string, entry *discovery) {
	defer close(entry.done)
	provider, err := oidc.NewProvider(oidc.ClientContext(context.Background(), v.client), issuer)
	if err != nil {
		// A failed discovery is not cached: the issuer may come up later.
		v.mu.Lock()
		delete(v.entries, issuer)
		v.mu.Unlock()
		entry.err = err
		return
	}
	entry.verifier = provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
}

// UnverifiedIssuer reads the `iss` claim without verifying the signature, so
// the issuer can be discovered before the token is verified against it. Errors
// read as a predicate of the token, for the caller to prefix with its own name
// for the token.
func UnverifiedIssuer(rawToken string) (string, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return "", errors.New("is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("payload could not be decoded: " + err.Error())
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", errors.New("claims could not be read: " + err.Error())
	}
	if claims.Issuer == "" {
		return "", errors.New("has no issuer")
	}
	return claims.Issuer, nil
}

// NormalizeIssuer drops the scheme and trailing slash, the form AWS IAM and
// Microsoft Entra store a federated issuer in.
func NormalizeIssuer(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://"), "/")
}

// AudienceIntersects reports whether any of the token's audiences is allowed.
func AudienceIntersects(tokenAudiences, allowed []string) bool {
	for _, aud := range tokenAudiences {
		for _, candidate := range allowed {
			if aud == candidate {
				return true
			}
		}
	}
	return false
}
