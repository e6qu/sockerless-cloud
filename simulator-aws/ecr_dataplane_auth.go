package main

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon ECR authenticates every /v2/ registry request with HTTP Basic, the
// whole authorization token being the Basic parameter:
//
//	curl -i -H "Authorization: Basic $TOKEN" https://…/v2/amazonlinux/tags/list
//
// GetAuthorizationToken returns base64("AWS:<password>"); `aws ecr
// get-login-password` prints the password and `docker login --username AWS`
// re-encodes the same token.
//
// An unaccepted credential gets a Basic challenge, not the Registry v2 Bearer
// flow (NVIDIA/enroot#59):
//
//	HTTP/1.1 401 Unauthorized
//	Docker-Distribution-Api-Version: registry/2.0
//	Www-Authenticate: Basic realm="https://1234.dkr.ecr.eu-west-2.amazonaws.com/",service="ecr.amazonaws.com"
//	Content-Length: 15
//	Content-Type: text/plain; charset=utf-8
//
// with the body `Not Authorized` (google/go-containerregistry#861). Only an
// expired token gets a distinct answer, the Registry v2 DENIED envelope Docker
// renders as `denied: Your authorization token has expired. Reauthenticate and
// try again.`
//
// The token is bound only to the IAM principal: it "can be used to access any
// Amazon ECR registry that your IAM principal has access to" for 12 hours, so
// verify possession and freshness, not registry scope.

// "…valid for 12 hours."
const ecrAuthorizationTokenTTL = 12 * time.Hour

// The user `docker login --username` takes.
const ecrDockerLoginUsername = "AWS"

// Amazon ECR sends a constant service, not a per-registry one.
const ecrRegistryService = "ecr.amazonaws.com"

// "The size of the authorization token returned by Amazon ECR is not fixed",
// so mint it from the cryptographic random source.
const ecrAuthorizationTokenBytes = 48

// Stored under its password so a presented Basic credential resolves to its
// grant.
type ECRAuthorizationToken struct {
	Password  string `json:"password"`
	IssuedAt  int64  `json:"issuedAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

var ecrAuthorizationTokens sim.Store[ECRAuthorizationToken]

// Returns base64("AWS:<password>") and the expiry.
func ecrIssueAuthorizationToken() (string, time.Time, error) {
	password, expiresAt, err := ecrIssueAuthorizationPassword()
	if err != nil {
		return "", time.Time{}, err
	}
	return base64.StdEncoding.EncodeToString(
		[]byte(ecrDockerLoginUsername + ":" + password)), expiresAt, nil
}

// Returns the password half, as `aws ecr get-login-password` prints it, and
// the expiry.
func ecrIssueAuthorizationPassword() (string, time.Time, error) {
	raw := make([]byte, ecrAuthorizationTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	expiresAt := now.Add(ecrAuthorizationTokenTTL)
	ecrExpireAuthorizationTokens(now)
	ecrAuthorizationTokens.Put(password, ECRAuthorizationToken{
		Password:  password,
		IssuedAt:  now.Unix(),
		ExpiresAt: expiresAt.Unix(),
	})
	return password, expiresAt, nil
}

// An absent record gets the same expiry refusal as a retained one, so drop
// expired tokens.
func ecrExpireAuthorizationTokens(now time.Time) {
	for _, token := range ecrAuthorizationTokens.List() {
		if now.Unix() >= token.ExpiresAt {
			ecrAuthorizationTokens.Delete(token.Password)
		}
	}
}

// The registry is private, so every /v2/ route needs the credential and the
// repository plays no part.
func ecrAuthorizeV2(w http.ResponseWriter, r *http.Request, _ string) bool {
	password, ok := ecrPresentedPassword(r.Header.Get("Authorization"))
	if !ok {
		ecrRegistryUnauthorized(w, r)
		return false
	}
	token, found := ecrAuthorizationTokens.Get(password)
	if !found {
		ecrRegistryUnauthorized(w, r)
		return false
	}
	// Compare in constant time even though the lookup already matched.
	if !hmac.Equal([]byte(token.Password), []byte(password)) {
		ecrRegistryUnauthorized(w, r)
		return false
	}
	if time.Now().Unix() >= token.ExpiresAt {
		ecrAuthorizationTokens.Delete(password)
		ecrRegistryTokenExpired(w, r)
		return false
	}
	return true
}

// The user half must be `AWS`.
func ecrPresentedPassword(authorization string) (string, bool) {
	scheme, credential := sim.ParseAuthorization(authorization)
	if !strings.EqualFold(scheme, "Basic") {
		return "", false
	}
	username, password, ok := sim.BasicCredential(credential)
	if !ok || username != ecrDockerLoginUsername || password == "" {
		return "", false
	}
	return password, true
}

// The realm is the registry the request reached.
func ecrRegistryChallenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Www-Authenticate", sim.BasicChallenge(awsRequestURLBase(r)+"/", ecrRegistryService))
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
}

// The Basic challenge and the plain-text `Not Authorized` body.
func ecrRegistryUnauthorized(w http.ResponseWriter, r *http.Request) {
	ecrRegistryChallenge(w, r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("Not Authorized\n"))
}

// The Registry v2 error envelope lets Docker render the service's own
// instruction.
func ecrRegistryTokenExpired(w http.ResponseWriter, r *http.Request) {
	ecrRegistryChallenge(w, r)
	sim.RegistryError(w, http.StatusUnauthorized, "DENIED",
		"Your authorization token has expired. Reauthenticate and try again.", nil)
}
