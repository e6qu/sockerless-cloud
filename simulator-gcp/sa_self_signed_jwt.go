package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// selfSignedJWTClockSkew is the leeway the verifier gives a client clock that
// runs ahead of the simulator's when it stamps iat.
const selfSignedJWTClockSkew = 5 * time.Minute

// isServiceAccountSelfSignedJWT reports whether raw claims to be a JWT a
// service account signed itself: its iss and sub both name the account.
// Google's client libraries send one in place of an access token when they
// hold a service-account key ("Service account authorization without OAuth").
func isServiceAccountSelfSignedJWT(raw string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	return claims.Iss != "" && claims.Iss == claims.Sub && strings.HasSuffix(claims.Iss, ".gserviceaccount.com")
}

// verifySelfSignedServiceAccountJWT verifies a self-signed service-account JWT
// as Google does: RS256 under the key its kid names, one IAM registered for the
// account and has not disabled, on an account that is not disabled; iss and sub
// the account's email; aud an API's service URL, or a scope claim in its place;
// and iat and exp a window of at most an hour that contains now. The caller is
// the service account.
func verifySelfSignedServiceAccountJWT(raw string, now time.Time) (accessTokenClaims, error) {
	var none accessTokenClaims
	signed, grantErr := verifyServiceAccountJWT(raw)
	if grantErr != nil {
		return none, errors.New(grantErr.description)
	}
	if signed.kid == "" {
		return none, errors.New("the self-signed JWT names no key in its kid header")
	}
	var claims accessTokenClaims
	if err := json.Unmarshal(signed.claims, &claims); err != nil {
		return none, fmt.Errorf("parse the self-signed JWT's claims: %w", err)
	}
	// golang.org/x/oauth2's scoped self-signed JWT carries an empty aud.
	aud, isString := claims.Aud.(string)
	hasAud := claims.Aud != nil && (!isString || aud != "")
	switch {
	case !hasAud && claims.Scope == "":
		return none, errors.New("the self-signed JWT carries neither an aud nor a scope claim")
	case hasAud && (!isString || !isGoogleAPIServiceURL(aud)):
		return none, fmt.Errorf("the self-signed JWT's audience %v is not a Google API service URL", claims.Aud)
	case claims.Iat == 0 || claims.Exp == 0:
		return none, errors.New("the self-signed JWT carries no iat or exp claim")
	case claims.Exp-claims.Iat > int64(time.Hour/time.Second):
		return none, errors.New("the self-signed JWT's lifetime exceeds an hour")
	case now.Add(selfSignedJWTClockSkew).Before(time.Unix(claims.Iat, 0)):
		return none, errors.New("the self-signed JWT was issued in the future")
	case !now.Before(time.Unix(claims.Exp, 0)):
		return none, errors.New("the self-signed JWT has expired")
	}
	return claims, nil
}

// isGoogleAPIServiceURL reports whether aud is an API's service URL, the
// audience Google's client libraries put in a self-signed JWT:
// https://run.googleapis.com/, with or without the trailing slash.
func isGoogleAPIServiceURL(aud string) bool {
	host, ok := strings.CutPrefix(aud, "https://")
	if !ok {
		return false
	}
	host = strings.TrimSuffix(host, "/")
	name, ok := strings.CutSuffix(host, ".googleapis.com")
	return ok && name != "" && !strings.ContainsAny(name, "/:")
}
