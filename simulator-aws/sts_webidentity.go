package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/oidcfed"
)

// stsOIDCVerifiers caches the external issuers AWS STS federates.
var stsOIDCVerifiers = oidcfed.New()

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
func verifyWebIdentityToken(ctx context.Context, rawToken string) (webIdentity, error) {
	issuer, err := oidcfed.UnverifiedIssuer(rawToken)
	if err != nil {
		return webIdentity{}, fmt.Errorf("web identity token %w", err)
	}
	provider, ok := oidcProviderForIssuer(issuer)
	if !ok {
		return webIdentity{}, fmt.Errorf("no OpenID Connect provider is registered for issuer %q", issuer)
	}

	verifier, err := stsOIDCVerifiers.Verifier(ctx, issuer)
	if err != nil {
		return webIdentity{}, fmt.Errorf("issuer %q could not be discovered: %w", issuer, err)
	}
	verified, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return webIdentity{}, fmt.Errorf("web identity token failed verification: %w", err)
	}
	if len(provider.ClientIDList) > 0 && !oidcfed.AudienceIntersects(verified.Audience, provider.ClientIDList) {
		return webIdentity{}, fmt.Errorf("web identity token audience is not in the provider's client ID list")
	}
	if verified.Subject == "" {
		return webIdentity{}, fmt.Errorf("web identity token has no subject")
	}
	claims := map[string]any{}
	if err := verified.Claims(&claims); err != nil {
		return webIdentity{}, fmt.Errorf("web identity token claims: %w", err)
	}
	return webIdentity{Subject: verified.Subject, Provider: provider, Claims: claims}, nil
}

// webIdentity is a verified web identity token and the provider it came from.
type webIdentity struct {
	Subject  string
	Provider IAMOIDCProvider
	Claims   map[string]any
}

// providerName is how IAM names the provider in its ARN and in its condition
// keys: the issuer URL without its scheme.
func (id webIdentity) providerName() string {
	return oidcfed.NormalizeIssuer(id.Provider.URL)
}

// conditionContext is the request context a role's trust policy is evaluated
// against. Every OpenID Connect provider carries amr, aud (the azp claim when
// the token sets one), email, oaud and sub under its own name; the providers
// AWS STS lists carry their further claims too.
func (id webIdentity) conditionContext(sessionName string) map[string][]string {
	name := id.providerName()
	ctx := map[string][]string{
		"aws:FederatedProvider": {id.Provider.Arn},
		"sts:RoleSessionName":   {sessionName},
	}
	set := func(key string, claim any) {
		if values := webIdentityClaimValues(claim); len(values) > 0 {
			ctx[key] = values
		}
	}
	set(name+":amr", id.Claims["amr"])
	if azp, ok := id.Claims["azp"]; ok {
		set(name+":aud", azp)
	} else {
		set(name+":aud", id.Claims["aud"])
	}
	set(name+":email", id.Claims["email"])
	set(name+":oaud", id.Claims["aud"])
	set(name+":sub", id.Claims["sub"])
	for _, key := range stsProviderClaimKeys {
		host, claim, _ := strings.Cut(key, ":")
		if !webIdentityProviderMatches(host, name) {
			continue
		}
		var value any = id.Claims
		for _, part := range strings.Split(claim, "/") {
			object, ok := value.(map[string]any)
			if !ok {
				value = nil
				break
			}
			value = object[part]
		}
		set(name+":"+claim, value)
	}
	return ctx
}

// webIdentityProviderMatches reports whether a declared provider name, whose
// ${...} segments stand for any value, names the provider.
func webIdentityProviderMatches(template, name string) bool {
	pattern := regexp.QuoteMeta(template)
	pattern = regexp.MustCompile(`\\\$\\\{[^}]*\\\}`).ReplaceAllString(pattern, `[^/:]+`)
	return regexp.MustCompile("^" + pattern + "$").MatchString(name)
}

func webIdentityClaimValues(claim any) []string {
	switch v := claim.(type) {
	case string:
		return []string{v}
	case bool:
		return []string{strconv.FormatBool(v)}
	case float64:
		return []string{strconv.FormatFloat(v, 'f', -1, 64)}
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, webIdentityClaimValues(item)...)
		}
		return out
	}
	return nil
}

// oidcProviderForIssuer finds the IAM OpenID Connect provider registered for an
// issuer. AWS stores a provider's URL without its scheme, so the issuer is
// matched with the scheme stripped.
func oidcProviderForIssuer(issuer string) (IAMOIDCProvider, bool) {
	want := oidcfed.NormalizeIssuer(issuer)
	for _, provider := range iamOIDCProviders.List() {
		if oidcfed.NormalizeIssuer(provider.URL) == want {
			return provider, true
		}
	}
	return IAMOIDCProvider{}, false
}
