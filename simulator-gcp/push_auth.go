package main

import "time"

// gcpOAuthToken asks a service that calls an HTTP target on a caller's behalf
// to attach an OAuth access token for a service account.
type gcpOAuthToken struct {
	ServiceAccountEmail string `json:"serviceAccountEmail,omitempty"`
	Scope               string `json:"scope,omitempty"`
}

// gcpOIDCToken asks for an OpenID Connect ID token for a service account
// instead.
type gcpOIDCToken struct {
	ServiceAccountEmail string `json:"serviceAccountEmail,omitempty"`
	Audience            string `json:"audience,omitempty"`
}

// gcpPushAuthorization is the Authorization header Cloud Scheduler and Cloud
// Tasks send to an HTTP target: an ID token whose audience defaults to the
// target's URI, or an access token whose scope defaults to cloud-platform.
// It is empty when the target asks for neither.
func gcpPushAuthorization(oidc *gcpOIDCToken, oauth *gcpOAuthToken, uri string, now time.Time) string {
	switch {
	case oidc != nil:
		audience := oidc.Audience
		if audience == "" {
			audience = uri
		}
		return "Bearer " + signServiceAccountIDToken(oidc.ServiceAccountEmail, audience, true, now, now.Add(time.Hour))
	case oauth != nil:
		scope := oauth.Scope
		if scope == "" {
			scope = "https://www.googleapis.com/auth/cloud-platform"
		}
		return "Bearer " + signWithAccessKey(map[string]any{
			"iss": googleTokenIssuer, "aud": simAccessTokenAudience, "sub": oauth.ServiceAccountEmail,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "scope": scope,
		})
	}
	return ""
}
