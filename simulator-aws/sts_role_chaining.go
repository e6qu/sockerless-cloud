package main

import "net/http"

// stsChainedFederationKeys are the identity provider's claims AWS STS keeps on
// a web-identity or SAML session for the AssumeRole that session chains to.
// They are the provider keys the Service Reference declares on sts:AssumeRole,
// which is how a chained role's trust policy still names the person behind
// the first session.
var stsChainedFederationKeys = []string{
	"accounts.google.com:aud",
	"accounts.google.com:sub",
	"cognito-identity.amazonaws.com:amr",
	"cognito-identity.amazonaws.com:aud",
	"cognito-identity.amazonaws.com:sub",
	"graph.facebook.com:app_id",
	"graph.facebook.com:id",
	"saml:namequalifier",
	"saml:sub",
	"saml:sub_type",
	"www.amazon.com:app_id",
	"www.amazon.com:user_id",
}

// stsChainedClaims picks, from the context a federation call was authorized
// with, the claims a session it opens carries into an AssumeRole.
func stsChainedClaims(ctx map[string][]string) map[string][]string {
	claims := map[string][]string{}
	for _, key := range stsChainedFederationKeys {
		if values := ctx[key]; len(values) > 0 {
			claims[key] = append([]string(nil), values...)
		}
	}
	if len(claims) == 0 {
		return nil
	}
	return claims
}

// stsCallerFederatedClaims are the claims the session signing r carries, which
// a role it assumes keeps in turn.
func stsCallerFederatedClaims(r *http.Request) map[string][]string {
	tc, ok := iamTempCreds.Get(iamAccessKeyIDFromRequest(r))
	if !ok {
		return nil
	}
	return tc.FederatedClaims
}
