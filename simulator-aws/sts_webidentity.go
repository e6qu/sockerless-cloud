package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/sync/singleflight"
)

var (
	stsOIDCVerifiers sync.Map
	stsOIDCDiscovery singleflight.Group
)

// verifyWebIdentityToken verifies a web identity token the way STS does for
// AssumeRoleWithWebIdentity: it finds the IAM OpenID Connect identity provider
// registered for the token's issuer and verifies the token against that
// issuer — real discovery, real JSON Web Key Set, real signature, issuer, and
// expiry — then checks the audience against the provider's client ID list. It
// returns the subject STS reports as SubjectFromWebIdentityToken.
//
// This is the console's federation path: an operator signed in through the
// deployment's identity provider exchanges that assertion for temporary
// credentials, exactly as a workload federating into AWS does.
func verifyWebIdentityToken(ctx context.Context, rawToken string) (subject string, err error) {
	issuer, err := unverifiedIssuer(rawToken)
	if err != nil {
		return "", err
	}
	provider, ok := oidcProviderForIssuer(issuer)
	if !ok {
		return "", fmt.Errorf("no OpenID Connect provider is registered for issuer %q", issuer)
	}

	verifier, err := stsOIDCVerifier(ctx, issuer)
	if err != nil {
		return "", fmt.Errorf("issuer %q could not be discovered: %w", issuer, err)
	}
	verified, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", fmt.Errorf("web identity token failed verification: %w", err)
	}
	if len(provider.ClientIDList) > 0 && !audienceInList(verified.Audience, provider.ClientIDList) {
		return "", fmt.Errorf("web identity token audience is not in the provider's client ID list")
	}
	if verified.Subject == "" {
		return "", fmt.Errorf("web identity token has no subject")
	}
	return verified.Subject, nil
}

// stsOIDCVerifier reuses issuer discovery metadata and its remote JSON Web Key
// Set across web-identity exchanges. The verifier validates every token's
// signature, issuer, expiry, and claims; only the issuer-scoped network client
// is retained. singleflight prevents concurrent first exchanges from repeating
// discovery for the same issuer.
func stsOIDCVerifier(ctx context.Context, issuer string) (*oidc.IDTokenVerifier, error) {
	cacheKey := strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	if cached, ok := stsOIDCVerifiers.Load(cacheKey); ok {
		return stsCachedOIDCVerifier(cached)
	}
	value, err, _ := stsOIDCDiscovery.Do(cacheKey, func() (any, error) {
		if cached, ok := stsOIDCVerifiers.Load(cacheKey); ok {
			return stsCachedOIDCVerifier(cached)
		}
		provider, err := oidc.NewProvider(ctx, issuer)
		if err != nil {
			return nil, err
		}
		// The audience is checked against the IAM provider's current client ID
		// list after cryptographic verification, rather than one fixed client.
		verifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
		stsOIDCVerifiers.Store(cacheKey, verifier)
		return verifier, nil
	})
	if err != nil {
		return nil, err
	}
	return stsCachedOIDCVerifier(value)
}

func stsCachedOIDCVerifier(value any) (*oidc.IDTokenVerifier, error) {
	verifier, ok := value.(*oidc.IDTokenVerifier)
	if !ok {
		return nil, fmt.Errorf("cached OpenID Connect verifier has unexpected type %T", value)
	}
	return verifier, nil
}

// unverifiedIssuer reads the `iss` claim without verifying the signature, so the
// right provider can be located before the token is verified against it.
func unverifiedIssuer(rawToken string) (string, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("web identity token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("web identity token payload could not be decoded: %w", err)
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("web identity token claims could not be read: %w", err)
	}
	if claims.Issuer == "" {
		return "", fmt.Errorf("web identity token has no issuer")
	}
	return claims.Issuer, nil
}

// oidcProviderForIssuer finds the IAM OpenID Connect provider registered for an
// issuer. AWS stores a provider's URL without its scheme, so the issuer is
// matched with the scheme stripped.
func oidcProviderForIssuer(issuer string) (IAMOIDCProvider, bool) {
	want := normalizeIssuer(issuer)
	for _, provider := range iamOIDCProviders.List() {
		if normalizeIssuer(provider.URL) == want {
			return provider, true
		}
	}
	return IAMOIDCProvider{}, false
}

func normalizeIssuer(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://"), "/")
}

func audienceInList(audiences, allowed []string) bool {
	for _, aud := range audiences {
		for _, candidate := range allowed {
			if aud == candidate {
				return true
			}
		}
	}
	return false
}
