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

// Authentication for the Amazon ECR private-registry data plane — the Docker
// Registry HTTP API v2 surface under /v2/ that `docker push` and `docker pull`
// speak to `<aws_account_id>.dkr.ecr.<region>.amazonaws.com`.
//
// Amazon ECR is a private registry, so it authenticates every registry
// request: "Amazon ECR supports the Docker Registry HTTP API. However, because
// Amazon ECR is a private registry, you must provide an authorization token
// with every HTTP request." The credential is HTTP Basic, and the whole
// authorization token is the Basic parameter — the same documentation passes it
// straight through as one:
//
//	curl -i -H "Authorization: Basic $TOKEN" https://…/v2/amazonlinux/tags/list
//
// (Amazon ECR User Guide, "Private registry authentication in Amazon ECR" §
// "Using HTTP API authentication".)
//
// The token itself comes from the control plane: "To obtain an authorization
// token, you must use the GetAuthorizationToken API operation to retrieve a
// base64-encoded authorization token containing the username AWS and an encoded
// password", and "When the string is decoded, it is presented in the format
// user:password for private registry authentication using docker login"
// (Amazon ECR API Reference, AuthorizationData.authorizationToken). Which is
// why `aws ecr get-login-password | docker login --username AWS --password-stdin
// <registry>` works: get-login-password decodes the token and prints the
// password half, and the Docker client re-encodes `AWS:<password>` as its Basic
// credential — byte-for-byte the authorizationToken again.
//
// Amazon ECR answers an unaccepted credential with a Basic challenge, not the
// Docker Registry v2 Bearer/token-service flow. A real exchange against a
// private registry, captured by a client that expected a token service and
// found none (NVIDIA/enroot#59):
//
//	HTTP/1.1 401 Unauthorized
//	Docker-Distribution-Api-Version: registry/2.0
//	Www-Authenticate: Basic realm="https://1234.dkr.ecr.eu-west-2.amazonaws.com/",service="ecr.amazonaws.com"
//	Content-Length: 15
//	Content-Type: text/plain; charset=utf-8
//
// The fifteen-byte text/plain body is `Not Authorized`; a repository-scoped
// route answers with the same body, which is how go-containerregistry renders
// it as `GET https://acctid.dkr.ecr.us-east-2.amazonaws.com/v2/repo/manifests/tag:
// unexpected status code 401 Unauthorized: Not Authorized`
// (google/go-containerregistry#861). The one refusal Amazon ECR distinguishes
// is an expired token, which the Docker client surfaces as
// `denied: Your authorization token has expired. Reauthenticate and try again.`
// — the `denied:` prefix being how it renders the DENIED code of the Docker
// Registry v2 error envelope, so that refusal carries the envelope rather than
// the plain text.
//
// The token is bound to the IAM principal that asked for it and to nothing
// else: "An authorization token represents your IAM authentication credentials
// and can be used to access any Amazon ECR registry that your IAM principal has
// access to. The authorization token is valid for 12 hours." (Amazon ECR API
// Reference, GetAuthorizationToken.) So this data plane verifies possession and
// freshness — a registry-scoping check would refuse requests real Amazon ECR
// accepts.

// ecrAuthorizationTokenTTL is how long an Amazon ECR authorization token stays
// valid: "An authentication token is used to access any Amazon ECR registry
// that your IAM principal has access to and is valid for 12 hours."
const ecrAuthorizationTokenTTL = 12 * time.Hour

// ecrDockerLoginUsername is the user half of the decoded authorization token —
// the value `docker login --username` is documented to take.
const ecrDockerLoginUsername = "AWS"

// ecrRegistryService is the `service` parameter of the registry's Basic
// challenge, which Amazon ECR emits as a constant rather than per-registry.
const ecrRegistryService = "ecr.amazonaws.com"

// ecrAuthorizationTokenBytes is the size of the password half GetAuthorizationToken
// mints. Amazon ECR's own password is opaque and long — "The size of the
// authorization token returned by Amazon ECR is not fixed. We recommend that you
// don't make assumptions about the maximum size." — so it is generated from the
// cryptographic random source and never derived from anything a caller can
// guess.
const ecrAuthorizationTokenBytes = 48

// ECRAuthorizationToken is one authorization token GetAuthorizationToken has
// issued, stored under its password so the data plane can resolve a presented
// Basic credential back to the grant that produced it.
type ECRAuthorizationToken struct {
	Password  string `json:"password"`
	IssuedAt  int64  `json:"issuedAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// ecrAuthorizationTokens holds the tokens this registry has issued.
var ecrAuthorizationTokens sim.Store[ECRAuthorizationToken]

// ecrIssueAuthorizationToken mints one authorization token and returns it in
// the wire form GetAuthorizationToken serves — base64 of `AWS:<password>` —
// together with the moment it expires.
func ecrIssueAuthorizationToken() (string, time.Time, error) {
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
	return base64.StdEncoding.EncodeToString(
		[]byte(ecrDockerLoginUsername + ":" + password)), expiresAt, nil
}

// ecrExpireAuthorizationTokens drops the tokens that are past their twelve
// hours. A token that no longer authenticates is not kept: the registry's
// answer to it is the expiry refusal, which the absent record produces just as
// a retained one would, so keeping them would only grow the store.
func ecrExpireAuthorizationTokens(now time.Time) {
	for _, token := range ecrAuthorizationTokens.List() {
		if now.Unix() >= token.ExpiresAt {
			ecrAuthorizationTokens.Delete(token.Password)
		}
	}
}

// ecrAuthorizeV2 is the OCIRegistry.Authorize hook the Amazon ECR data plane
// mounts. Every /v2/ route needs the same credential — the registry is private,
// so there is no anonymous read — and the repository the shared registry parsed
// is therefore not part of the decision.
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
	// Constant-time comparison of the resolved record against the presented
	// secret: the store lookup already matched, but the compare keeps the
	// verification itself free of an early-exit byte comparison.
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

// ecrPresentedPassword reads the password half out of an Authorization header
// carrying the authorization token as its Basic credential. The user half must
// be `AWS`, the only user Amazon ECR's token decodes to.
func ecrPresentedPassword(authorization string) (string, bool) {
	authorization = strings.TrimSpace(authorization)
	const scheme = "Basic"
	if len(authorization) <= len(scheme) || !strings.EqualFold(authorization[:len(scheme)], scheme) {
		return "", false
	}
	if authorization[len(scheme)] != ' ' {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authorization[len(scheme)+1:]))
	if err != nil {
		return "", false
	}
	username, password, ok := strings.Cut(string(raw), ":")
	if !ok || username != ecrDockerLoginUsername || password == "" {
		return "", false
	}
	return password, true
}

// ecrRegistryChallenge sets the Basic challenge Amazon ECR answers an
// unaccepted credential with. The realm is the registry the request reached and
// the service is the constant Amazon ECR names itself by.
func ecrRegistryChallenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Www-Authenticate",
		`Basic realm="`+awsRequestURLBase(r)+`/",service="`+ecrRegistryService+`"`)
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
}

// ecrRegistryUnauthorized writes the refusal Amazon ECR answers a request that
// presented no credential, or one it does not accept: the Basic challenge and
// the plain-text `Not Authorized` body.
func ecrRegistryUnauthorized(w http.ResponseWriter, r *http.Request) {
	ecrRegistryChallenge(w, r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("Not Authorized\n"))
}

// ecrRegistryTokenExpired writes the one refusal Amazon ECR distinguishes: a
// token that authenticated once but is past its twelve hours. It carries the
// Docker Registry v2 error envelope so a Docker client renders the service's
// own instruction, `denied: Your authorization token has expired. Reauthenticate
// and try again.`
func ecrRegistryTokenExpired(w http.ResponseWriter, r *http.Request) {
	ecrRegistryChallenge(w, r)
	sim.WriteJSON(w, http.StatusUnauthorized, map[string]any{
		"errors": []map[string]any{{
			"code":    "DENIED",
			"message": "Your authorization token has expired. Reauthenticate and try again.",
		}},
	})
}
